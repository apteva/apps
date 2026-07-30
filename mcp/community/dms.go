// Direct messages — community-owned.
//
// A DM thread can have 2+ participants (group DMs). dms_open is the
// canonical idempotent open: pass a sorted participant set, get back
// the existing thread if one already matches; otherwise create.
//
// Authors must be in the same community as every other participant
// — cross-community DMs are not allowed in 0.1.

package main

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type DMThread struct {
	ID            string   `json:"id"`
	CommunityID   string   `json:"community_id"`
	CreatedAt     string   `json:"created_at"`
	LastMessageAt string   `json:"last_message_at"`
	Participants  []string `json:"participants"`
	UnreadCount   int      `json:"unread_count,omitempty"` // populated by list_threads when called with a member_id
}

type DMMessage struct {
	ID          string `json:"id"`
	CommunityID string `json:"community_id"`
	DMThreadID  string `json:"dm_thread_id"`
	AuthorID    string `json:"author_id"`
	Body        string `json:"body"`
	CreatedAt   string `json:"created_at"`
}

type DMThreadView struct {
	DMThread
	Messages []DMMessage `json:"messages"`
}

func dmsTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "dms_open",
			Description: "Open or fetch a DM thread between members. Args: participants (required, array of >=2 member ids). Idempotent — re-opening returns the existing thread.",
			InputSchema: schemaObject(map[string]any{
				"community_id": map[string]any{"type": "string"},
				"participants": map[string]any{
					"type":  "array",
					"items": map[string]any{"type": "string"},
				},
			}, []string{"participants"}),
			Handler: toolDMsOpen,
		},
		{
			Name:        "dms_send",
			Description: "Send a message in a DM thread. Args: dm_thread_id (required), author_id (required), body (required).",
			InputSchema: schemaObject(map[string]any{
				"dm_thread_id": map[string]any{"type": "string"},
				"author_id":    map[string]any{"type": "string"},
				"body":         map[string]any{"type": "string"},
			}, []string{"dm_thread_id", "author_id", "body"}),
			Handler: toolDMsSend,
		},
		{
			Name:        "dms_list_threads",
			Description: "List DM threads a member participates in, most recent first. Args: member_id (required), limit? (default 50).",
			InputSchema: schemaObject(map[string]any{
				"community_id": map[string]any{"type": "string"},
				"member_id":    map[string]any{"type": "string"},
				"limit":        map[string]any{"type": "integer"},
			}, []string{"member_id"}),
			Handler: toolDMsListThreads,
		},
		{
			Name:        "dms_get_thread",
			Description: "Fetch a DM thread by id with its messages (oldest first). Args: id (required), caller_member_id (required), limit? (default 200).",
			InputSchema: schemaObject(map[string]any{
				"id":               map[string]any{"type": "string"},
				"caller_member_id": map[string]any{"type": "string"},
				"limit":            map[string]any{"type": "integer"},
			}, []string{"id", "caller_member_id"}),
			Handler: toolDMsGetThread,
		},
		{
			Name:        "dms_mark_read",
			Description: "Move a member's read cursor in a DM thread to now. Idempotent. Args: dm_thread_id (required), member_id (required).",
			InputSchema: schemaObject(map[string]any{
				"dm_thread_id": map[string]any{"type": "string"},
				"member_id":    map[string]any{"type": "string"},
			}, []string{"dm_thread_id", "member_id"}),
			Handler: toolDMsMarkRead,
		},
		{
			Name:        "dms_unread_count",
			Description: "Total unread DM messages for a member across all their DM threads. Args: member_id (required).",
			InputSchema: schemaObject(map[string]any{
				"community_id": map[string]any{"type": "string"},
				"member_id":    map[string]any{"type": "string"},
			}, []string{"member_id"}),
			Handler: toolDMsUnreadCount,
		},
	}
}

func toolDMsMarkRead(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	threadID, err := mustStr(args, "dm_thread_id")
	if err != nil {
		return nil, err
	}
	memberID, err := mustStr(args, "member_id")
	if err != nil {
		return nil, err
	}
	db := ctx.AppDB()
	t, err := loadDMThread(db, threadID)
	if err != nil {
		return nil, err
	}
	if err := ensureCommunityVisible(ctx, db, t.CommunityID); err != nil {
		return nil, err
	}
	res, err := db.Exec(
		`UPDATE dm_participants SET last_read_at = CURRENT_TIMESTAMP
		 WHERE dm_thread_id = ? AND member_id = ?`,
		threadID, memberID,
	)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return nil, errors.New("member is not a participant in this dm thread")
	}
	emit(ctx, "dm.read", map[string]any{
		"dm_thread_id": threadID,
		"member_id":    memberID,
	})
	return map[string]any{"ok": true}, nil
}

func toolDMsUnreadCount(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	memberID, err := mustStr(args, "member_id")
	if err != nil {
		return nil, err
	}
	m, err := loadMember(ctx.AppDB(), memberID)
	if err != nil {
		return nil, err
	}
	if err := ensureCommunityVisible(ctx, ctx.AppDB(), m.CommunityID); err != nil {
		return nil, err
	}
	var total int
	err = ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM dm_messages m
		 JOIN dm_participants p ON p.dm_thread_id = m.dm_thread_id AND p.member_id = ?
		 WHERE m.author_id <> ?
		   AND (p.last_read_at IS NULL OR m.created_at > p.last_read_at)`,
		memberID, memberID,
	).Scan(&total)
	if err != nil {
		return nil, err
	}
	return map[string]any{"member_id": memberID, "unread": total}, nil
}

// ─── handlers ────────────────────────────────────────────────────

func toolDMsOpen(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	rawParts, ok := args["participants"].([]any)
	if !ok || len(rawParts) < 2 {
		return nil, errors.New("participants must be an array of >=2 member ids")
	}
	parts := make([]string, 0, len(rawParts))
	seen := map[string]bool{}
	for _, p := range rawParts {
		s, _ := p.(string)
		s = strings.TrimSpace(s)
		if s == "" {
			return nil, errors.New("empty participant id")
		}
		if seen[s] {
			continue
		}
		seen[s] = true
		parts = append(parts, s)
	}
	if len(parts) < 2 {
		return nil, errors.New("need at least 2 distinct participants")
	}
	sort.Strings(parts)
	participantKey := strings.Join(parts, "|")

	db := ctx.AppDB()
	// All participants must exist in the same community.
	communityID, err := commonCommunity(db, parts)
	if err != nil {
		return nil, err
	}
	if err := ensureCommunityVisible(ctx, db, communityID); err != nil {
		return nil, err
	}
	// Find an existing thread whose participant set matches exactly.
	if id, ok, err := findDMThreadByParticipants(db, communityID, parts); err != nil {
		return nil, err
	} else if ok {
		return loadDMThread(db, id)
	}
	// Create.
	id := newID("dm")
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(
		`INSERT OR IGNORE INTO dm_threads (id, community_id, participant_key) VALUES (?, ?, ?)`,
		id, communityID, participantKey,
	)
	if err != nil {
		return nil, fmt.Errorf("create dm thread: %w", err)
	}
	inserted, _ := res.RowsAffected()
	if inserted == 0 {
		var existingID string
		if err := tx.QueryRow(
			`SELECT id FROM dm_threads WHERE community_id = ? AND participant_key = ?`,
			communityID, participantKey,
		).Scan(&existingID); err != nil {
			return nil, err
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		return loadDMThread(db, existingID)
	}
	for _, p := range parts {
		if _, err := tx.Exec(
			`INSERT INTO dm_participants (dm_thread_id, member_id) VALUES (?, ?)`,
			id, p,
		); err != nil {
			return nil, fmt.Errorf("add dm participant: %w", err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return loadDMThread(db, id)
}

func toolDMsSend(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	threadID, err := mustStr(args, "dm_thread_id")
	if err != nil {
		return nil, err
	}
	authorID, err := mustStr(args, "author_id")
	if err != nil {
		return nil, err
	}
	body, err := mustStr(args, "body")
	if err != nil {
		return nil, err
	}
	db := ctx.AppDB()
	// Author must be a participant.
	var communityID string
	if err := db.QueryRow(
		`SELECT t.community_id FROM dm_threads t
		 JOIN dm_participants p ON p.dm_thread_id = t.id
		 WHERE t.id = ? AND p.member_id = ?`,
		threadID, authorID,
	).Scan(&communityID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("author is not a participant in this dm thread")
		}
		return nil, err
	}
	if err := ensureCommunityVisible(ctx, db, communityID); err != nil {
		return nil, err
	}
	msgID := newID("dmm")
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`INSERT INTO dm_messages (id, community_id, dm_thread_id, author_id, body) VALUES (?, ?, ?, ?, ?)`,
		msgID, communityID, threadID, authorID, body,
	); err != nil {
		return nil, fmt.Errorf("insert dm message: %w", err)
	}
	if _, err := tx.Exec(
		`UPDATE dm_threads SET last_message_at = CURRENT_TIMESTAMP WHERE id = ?`, threadID,
	); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	// Load + return.
	var m DMMessage
	row := db.QueryRow(`SELECT `+dmMessageCols+` FROM dm_messages WHERE id = ?`, msgID)
	if err := row.Scan(&m.ID, &m.CommunityID, &m.DMThreadID, &m.AuthorID, &m.Body, &m.CreatedAt); err != nil {
		return nil, err
	}
	emit(ctx, "dm.received", map[string]any{
		"community_id": m.CommunityID,
		"dm_thread_id": m.DMThreadID,
		"message_id":   m.ID,
		"author_id":    m.AuthorID,
		"preview":      preview(m.Body),
	})
	return m, nil
}

func toolDMsListThreads(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	memberID, err := mustStr(args, "member_id")
	if err != nil {
		return nil, err
	}
	m, err := loadMember(ctx.AppDB(), memberID)
	if err != nil {
		return nil, err
	}
	if err := ensureCommunityVisible(ctx, ctx.AppDB(), m.CommunityID); err != nil {
		return nil, err
	}
	limit := boundedLimit(args, "limit", 50, 200)
	db := ctx.AppDB()
	rows, err := db.Query(
		`SELECT t.id, t.community_id, t.created_at, t.last_message_at,
		        COUNT(CASE
		          WHEN msg.author_id <> ? AND (p.last_read_at IS NULL OR msg.created_at > p.last_read_at)
		          THEN 1 END) AS unread_count
		 FROM dm_threads t
		 JOIN dm_participants p ON p.dm_thread_id = t.id
		 LEFT JOIN dm_messages msg ON msg.dm_thread_id = t.id
		 WHERE p.member_id = ?
		 GROUP BY t.id, t.community_id, t.created_at, t.last_message_at
		 ORDER BY t.last_message_at DESC, t.id DESC LIMIT ?`,
		memberID, memberID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []DMThread{}
	ids := []string{}
	for rows.Next() {
		var t DMThread
		if err := rows.Scan(&t.ID, &t.CommunityID, &t.CreatedAt, &t.LastMessageAt, &t.UnreadCount); err != nil {
			return nil, err
		}
		out = append(out, t)
		ids = append(ids, t.ID)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if err := hydrateParticipants(db, ids, out); err != nil {
		return nil, err
	}
	return map[string]any{"threads": out}, nil
}

func toolDMsGetThread(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := mustStr(args, "id")
	if err != nil {
		return nil, err
	}
	callerID, err := mustStr(args, "caller_member_id")
	if err != nil {
		return nil, err
	}
	limit := boundedLimit(args, "limit", 200, 500)
	db := ctx.AppDB()
	t, err := loadDMThread(db, id)
	if err != nil {
		return nil, err
	}
	if err := ensureCommunityVisible(ctx, db, t.CommunityID); err != nil {
		return nil, err
	}
	if err := verifyDMParticipant(db, id, callerID); err != nil {
		return nil, err
	}
	rows, err := db.Query(
		`SELECT `+dmMessageCols+` FROM dm_messages WHERE dm_thread_id = ? ORDER BY created_at, id LIMIT ?`,
		id, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	view := DMThreadView{DMThread: t}
	for rows.Next() {
		var m DMMessage
		if err := rows.Scan(&m.ID, &m.CommunityID, &m.DMThreadID, &m.AuthorID, &m.Body, &m.CreatedAt); err != nil {
			return nil, err
		}
		view.Messages = append(view.Messages, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return view, nil
}

// ─── DB helpers ──────────────────────────────────────────────────

const dmMessageCols = `id, community_id, dm_thread_id, author_id, body, created_at`

func commonCommunity(db *sql.DB, memberIDs []string) (string, error) {
	communities := map[string]bool{}
	for _, id := range memberIDs {
		var c string
		var status string
		err := db.QueryRow(`SELECT community_id, status FROM members WHERE id = ?`, id).Scan(&c, &status)
		if errors.Is(err, sql.ErrNoRows) {
			return "", fmt.Errorf("member %q not found", id)
		}
		if err != nil {
			return "", err
		}
		if status != "active" {
			return "", fmt.Errorf("member %q is %s", id, status)
		}
		communities[c] = true
	}
	if len(communities) != 1 {
		return "", errors.New("all participants must belong to the same community")
	}
	for c := range communities {
		return c, nil
	}
	return "", errors.New("unreachable")
}

func findDMThreadByParticipants(db *sql.DB, communityID string, sortedParts []string) (string, bool, error) {
	var id string
	err := db.QueryRow(
		`SELECT id FROM dm_threads WHERE community_id = ? AND participant_key = ?`,
		communityID, strings.Join(sortedParts, "|"),
	).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return "", false, nil
	}
	return id, err == nil, err
}

func verifyDMParticipant(db *sql.DB, threadID, memberID string) error {
	var exists int
	err := db.QueryRow(
		`SELECT 1 FROM dm_participants WHERE dm_thread_id = ? AND member_id = ?`,
		threadID, memberID,
	).Scan(&exists)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("member is not a participant in this dm thread")
	}
	return err
}

func loadDMThread(db *sql.DB, id string) (DMThread, error) {
	var t DMThread
	row := db.QueryRow(
		`SELECT id, community_id, created_at, last_message_at FROM dm_threads WHERE id = ?`, id,
	)
	if err := row.Scan(&t.ID, &t.CommunityID, &t.CreatedAt, &t.LastMessageAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return t, fmt.Errorf("dm thread %q not found", id)
		}
		return t, err
	}
	parts, err := participantsFor(db, t.ID)
	if err != nil {
		return t, err
	}
	t.Participants = parts
	return t, nil
}

func participantsFor(db *sql.DB, threadID string) ([]string, error) {
	rows, err := db.Query(
		`SELECT member_id FROM dm_participants WHERE dm_thread_id = ? ORDER BY member_id`,
		threadID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []string{}
	for rows.Next() {
		var m string
		if err := rows.Scan(&m); err != nil {
			return nil, err
		}
		out = append(out, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return out, nil
}

func hydrateParticipants(db *sql.DB, threadIDs []string, threads []DMThread) error {
	if len(threadIDs) == 0 {
		return nil
	}
	placeholders := strings.Repeat("?,", len(threadIDs))
	placeholders = strings.TrimRight(placeholders, ",")
	args := make([]any, len(threadIDs))
	for i, id := range threadIDs {
		args[i] = id
	}
	rows, err := db.Query(
		`SELECT dm_thread_id, member_id FROM dm_participants WHERE dm_thread_id IN (`+placeholders+`)
		 ORDER BY dm_thread_id, member_id`,
		args...,
	)
	if err != nil {
		return err
	}
	defer rows.Close()
	byThread := map[string][]string{}
	for rows.Next() {
		var tID, mID string
		if err := rows.Scan(&tID, &mID); err != nil {
			return err
		}
		byThread[tID] = append(byThread[tID], mID)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for i := range threads {
		threads[i].Participants = byThread[threads[i].ID]
	}
	return nil
}

// ─── HTTP ────────────────────────────────────────────────────────

func (a *App) httpDMs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, 405, "method not allowed")
		return
	}
	memberID := r.URL.Query().Get("member_id")
	if memberID == "" {
		writeErr(w, 400, "member_id required")
		return
	}
	out, err := toolDMsListThreads(globalCtx, map[string]any{"member_id": memberID})
	if err != nil {
		writeDomainErr(w, err)
		return
	}
	writeJSON(w, out)
}
