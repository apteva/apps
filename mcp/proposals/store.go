package main

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"
)

type store struct {
	db     *sql.DB
	secret []byte
}

const revisionSelect = `
SELECT r.id, r.proposal_id, r.number, r.version, r.state, r.content, r.expires_at, r.published_at,
       COALESCE(r.link_nonce, ''), s.outcome, s.name, s.note, s.selection, s.total_minor, s.created_at
FROM revisions r JOIN proposals p ON p.id = r.proposal_id
LEFT JOIN responses s ON s.revision_id = r.id`

func scanRevision(row interface{ Scan(...any) error }) (revision, error) {
	var r revision
	var raw string
	var outcome, name, note, choices sql.NullString
	var total, created sql.NullInt64
	err := row.Scan(&r.ID, &r.ProposalID, &r.Number, &r.Version, &r.State, &raw, &r.ExpiresAt,
		&r.PublishedAt, &r.nonce, &outcome, &name, &note, &choices, &total, &created)
	if errors.Is(err, sql.ErrNoRows) {
		return r, errNotFound
	}
	if err != nil {
		return r, err
	}
	if err := json.Unmarshal([]byte(raw), &r.Content); err != nil {
		return r, err
	}
	if outcome.Valid {
		r.Response = &response{Outcome: outcome.String, Name: name.String, Note: note.String, TotalMinor: total.Int64, CreatedAt: created.Int64}
		err = json.Unmarshal([]byte(choices.String), &r.Response.Selection)
	}
	return r, err
}

func revisions(rows *sql.Rows, now int64) ([]revision, error) {
	defer rows.Close()
	result := []revision{}
	for rows.Next() {
		r, err := scanRevision(rows)
		if err != nil {
			return nil, err
		}
		r.State = r.stateAt(now)
		result = append(result, r)
	}
	return result, rows.Err()
}

func (s store) list(ctx context.Context, project string, before int64, limit int) ([]revision, error) {
	rows, err := s.db.QueryContext(ctx, revisionSelect+`
WHERE p.project_id = ? AND (? = 0 OR p.id < ?)
  AND r.number = (SELECT MAX(number) FROM revisions WHERE proposal_id = p.id)
ORDER BY p.id DESC LIMIT ?`, project, before, before, limit)
	if err != nil {
		return nil, err
	}
	return revisions(rows, time.Now().Unix())
}

func (s store) get(ctx context.Context, project string, id int64) (detail, error) {
	rows, err := s.db.QueryContext(ctx, revisionSelect+" WHERE p.project_id = ? AND p.id = ? ORDER BY r.number DESC", project, id)
	if err != nil {
		return detail{}, err
	}
	rs, err := revisions(rows, time.Now().Unix())
	if err == nil && len(rs) == 0 {
		err = errNotFound
	}
	return detail{ID: id, Revisions: rs}, err
}

func (s store) create(ctx context.Context, project string, c content) (detail, error) {
	if err := c.validate(false); err != nil {
		return detail{}, err
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return detail{}, err
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return detail{}, err
	}
	defer tx.Rollback()
	inserted, err := tx.ExecContext(ctx, "INSERT INTO proposals(project_id) VALUES (?)", project)
	if err != nil {
		return detail{}, err
	}
	id, err := inserted.LastInsertId()
	if err != nil {
		return detail{}, err
	}
	if _, err := tx.ExecContext(ctx, "INSERT INTO revisions(proposal_id, number, state, content) VALUES (?, 1, 'draft', ?)", id, string(raw)); err != nil {
		return detail{}, err
	}
	if err := tx.Commit(); err != nil {
		return detail{}, err
	}
	return s.get(ctx, project, id)
}

func (s store) update(ctx context.Context, project string, id, draftID int64, version int, c content) (detail, error) {
	if err := c.validate(false); err != nil {
		return detail{}, err
	}
	raw, err := json.Marshal(c)
	if err != nil {
		return detail{}, err
	}
	result, err := s.db.ExecContext(ctx, `
UPDATE revisions SET content = ?, version = version + 1
WHERE id = ? AND proposal_id = ? AND state = 'draft' AND version = ?
  AND EXISTS (SELECT 1 FROM proposals WHERE id = ? AND project_id = ?)`, string(raw), draftID, id, version, id, project)
	if err := changed(result, err); err != nil {
		return detail{}, err
	}
	return s.get(ctx, project, id)
}

func (s store) revise(ctx context.Context, project string, id int64) (detail, error) {
	result, err := s.db.ExecContext(ctx, `
INSERT INTO revisions(proposal_id, number, state, content)
SELECT p.id, r.number + 1, 'draft', r.content
FROM proposals p JOIN revisions r ON r.proposal_id = p.id
WHERE p.id = ? AND p.project_id = ? AND r.state NOT IN ('draft', 'accepted')
  AND r.number = (SELECT MAX(number) FROM revisions WHERE proposal_id = p.id)`, id, project)
	if err := changed(result, err); err != nil {
		return detail{}, err
	}
	return s.get(ctx, project, id)
}

func (s store) publish(ctx context.Context, project string, id, draftID int64, version int) (detail, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return detail{}, err
	}
	defer tx.Rollback()
	draft, err := scanRevision(tx.QueryRowContext(ctx, revisionSelect+" WHERE p.project_id = ? AND p.id = ? AND r.state = 'draft'", project, id))
	if err != nil {
		return detail{}, err
	}
	if draft.ID != draftID || draft.Version != version {
		return detail{}, errConflict
	}
	if err := draft.Content.validate(true); err != nil {
		return detail{}, invalid(err.Error())
	}
	var accepted bool
	if err := tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT 1 FROM revisions WHERE proposal_id = ? AND state = 'accepted')", id).Scan(&accepted); err != nil {
		return detail{}, err
	}
	if accepted {
		return detail{}, errConflict
	}
	now := time.Now().Unix()
	if _, err := tx.ExecContext(ctx, "UPDATE revisions SET state = 'superseded' WHERE proposal_id = ? AND state = 'published'", id); err != nil {
		return detail{}, err
	}
	if _, err := tx.ExecContext(ctx, `
UPDATE revisions SET state = 'published', expires_at = ?, published_at = ?, link_nonce = ?
WHERE id = ?`, now+int64(draft.Content.ValidityDays)*86400, now, newNonce(), draft.ID); err != nil {
		return detail{}, err
	}
	if err := enqueue(ctx, tx, project, "proposal.published", draft, nil); err != nil {
		return detail{}, err
	}
	if err := tx.Commit(); err != nil {
		return detail{}, err
	}
	return s.get(ctx, project, id)
}

func (s store) withdraw(ctx context.Context, project string, id, revisionID int64) (detail, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return detail{}, err
	}
	defer tx.Rollback()
	r, err := scanRevision(tx.QueryRowContext(ctx, revisionSelect+" WHERE p.project_id = ? AND p.id = ? AND r.id = ?", project, id, revisionID))
	if err != nil {
		return detail{}, err
	}
	if r.stateAt(time.Now().Unix()) != "published" {
		return detail{}, errConflict
	}
	if _, err := tx.ExecContext(ctx, "UPDATE revisions SET state = 'withdrawn' WHERE id = ?", r.ID); err != nil {
		return detail{}, err
	}
	if err := enqueue(ctx, tx, project, "proposal.withdrawn", r, nil); err != nil {
		return detail{}, err
	}
	if err := tx.Commit(); err != nil {
		return detail{}, err
	}
	return s.get(ctx, project, id)
}

func (s store) link(ctx context.Context, project string, id, revisionID int64, rotate bool) (revision, string, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return revision{}, "", err
	}
	defer tx.Rollback()
	r, err := scanRevision(tx.QueryRowContext(ctx, revisionSelect+" WHERE p.project_id = ? AND p.id = ? AND r.id = ?", project, id, revisionID))
	if err != nil {
		return r, "", err
	}
	if r.stateAt(time.Now().Unix()) != "published" {
		return r, "", errConflict
	}
	if rotate {
		r.nonce = newNonce()
		if _, err := tx.ExecContext(ctx, "UPDATE revisions SET link_nonce = ? WHERE id = ?", r.nonce, r.ID); err != nil {
			return r, "", err
		}
	}
	return r, s.token(project, r), tx.Commit()
}

type queryRow interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}

func (s store) offer(ctx context.Context, q queryRow, project, token string) (revision, error) {
	nonce, _, ok := strings.Cut(token, ".")
	if !ok || len(nonce) != 32 || len(token) != 76 {
		return revision{}, errNotFound
	}
	r, err := scanRevision(q.QueryRowContext(ctx, revisionSelect+" WHERE p.project_id = ? AND r.link_nonce = ?", project, nonce))
	if err != nil {
		return r, err
	}
	if !hmac.Equal([]byte(s.token(project, r)), []byte(token)) {
		return revision{}, errNotFound
	}
	r.State = r.stateAt(time.Now().Unix())
	return r, nil
}

func (s store) respond(ctx context.Context, project, token, outcome, name, note string, choices selection) (revision, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return revision{}, err
	}
	defer tx.Rollback()
	r, err := s.offer(ctx, tx, project, token)
	if err != nil {
		return r, err
	}
	if r.Response != nil {
		retry, err := newResponse(r.Content, outcome, name, note, choices, time.Now())
		if err != nil || retry.Outcome != r.Response.Outcome || retry.Name != r.Response.Name ||
			retry.Note != r.Response.Note || !maps.Equal(retry.Selection, r.Response.Selection) {
			return r, errConflict
		}
		return r, nil
	}
	now := time.Now()
	if r.State != "published" || r.ExpiresAt <= now.Unix() {
		return r, errConflict
	}
	reply, err := newResponse(r.Content, outcome, name, note, choices, now)
	if err != nil {
		return r, invalid(err.Error())
	}
	raw, err := json.Marshal(reply.Selection)
	if err != nil {
		return r, err
	}
	if _, err := tx.ExecContext(ctx, `
INSERT INTO responses(revision_id, outcome, name, note, selection, total_minor, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)`, r.ID, reply.Outcome, reply.Name, reply.Note, string(raw), reply.TotalMinor, reply.CreatedAt); err != nil {
		return r, err
	}
	if _, err := tx.ExecContext(ctx, "UPDATE revisions SET state = ? WHERE id = ?", reply.Outcome, r.ID); err != nil {
		return r, err
	}
	if err := enqueue(ctx, tx, project, "proposal."+reply.Outcome, r, &reply); err != nil {
		return r, err
	}
	r.State, r.Response = reply.Outcome, &reply
	return r, tx.Commit()
}

func enqueue(ctx context.Context, tx *sql.Tx, project, topic string, r revision, reply *response) error {
	payload := map[string]any{"proposal_id": r.ProposalID, "revision_id": r.ID, "revision_number": r.Number}
	if reply != nil && reply.Outcome == "accepted" {
		payload["total_minor"] = reply.TotalMinor
		payload["currency"] = r.Content.Currency
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.ExecContext(ctx, "INSERT INTO outbox(project_id, topic, payload) VALUES (?, ?, ?)", project, topic, string(raw))
	return err
}

func (s store) token(project string, r revision) string {
	mac := hmac.New(sha256.New, s.secret)
	fmt.Fprintf(mac, "%s\n%d\n%s", project, r.ID, r.nonce)
	return r.nonce + "." + base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

func newNonce() string {
	key := make([]byte, 24)
	rand.Read(key)
	return base64.RawURLEncoding.EncodeToString(key)
}

func changed(result sql.Result, err error) error {
	if err != nil {
		return err
	}
	count, err := result.RowsAffected()
	if err == nil && count != 1 {
		err = errConflict
	}
	return err
}
