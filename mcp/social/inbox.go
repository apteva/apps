// inbox — comments, DMs, mentions and reviews pulled from connected
// social accounts. v1 is poll-only: a background worker (not yet
// wired) pages each account's APIs on a cadence and upserts here.
// Tool handlers in main.go consume this layer; nothing here touches
// the SDK, HTTP or platform integrations.
package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

// inboxItem mirrors one inbox_items row. JSON tags double as the
// shape returned by inbox_list / inbox_get tools — keep them stable.
type inboxItem struct {
	ID                int64           `json:"id"`
	ProjectID         string          `json:"project_id"`
	SocialAccountID   int64           `json:"social_account_id"`
	Platform          string          `json:"platform"`
	Kind              string          `json:"kind"` // comment | dm | mention | review
	ExternalID        string          `json:"external_id"`
	ParentExternalID  string          `json:"parent_external_id,omitempty"`
	PostID            int64           `json:"post_id,omitempty"`
	ExternalPostID    string          `json:"external_post_id,omitempty"`
	AuthorExternalID  string          `json:"author_external_id,omitempty"`
	AuthorName        string          `json:"author_name,omitempty"`
	AuthorHandle      string          `json:"author_handle,omitempty"`
	AuthorAvatarURL   string          `json:"author_avatar_url,omitempty"`
	Body              string          `json:"body,omitempty"`
	Media             json.RawMessage `json:"media,omitempty"`
	Permalink         string          `json:"permalink,omitempty"`
	Rating            int             `json:"rating,omitempty"`
	OccurredAt        string          `json:"occurred_at"`
	FetchedAt         string          `json:"fetched_at"`
	Status            string          `json:"status"`
}

// Inbox item kinds.
const (
	inboxKindComment = "comment"
	inboxKindDM      = "dm"
	inboxKindMention = "mention"
	inboxKindReview  = "review"
)

// Inbox item statuses. unread/read/replied/hidden/archived. Status
// transitions are local except for hidden — which mirrors a platform
// action when the underlying platform supports it.
const (
	inboxStatusUnread   = "unread"
	inboxStatusRead     = "read"
	inboxStatusReplied  = "replied"
	inboxStatusHidden   = "hidden"
	inboxStatusArchived = "archived"
)

var validInboxKinds = map[string]bool{
	inboxKindComment: true,
	inboxKindDM:      true,
	inboxKindMention: true,
	inboxKindReview:  true,
}

var validInboxStatuses = map[string]bool{
	inboxStatusUnread:   true,
	inboxStatusRead:     true,
	inboxStatusReplied:  true,
	inboxStatusHidden:   true,
	inboxStatusArchived: true,
}

// platformSupportsInbox returns true if the platform supports the
// given (kind, action) pair. action is "read" or "write" for
// comments/dms/mentions/reviews; "hide" / "like" / "delete" for
// comment moderation; "private_reply" for IG. Unknown combos return
// false. Used by tool handlers to short-circuit per-account fan-outs
// with status='unsupported'.
func platformSupportsInbox(platform, kind, action string) bool {
	def, ok := platforms[platform]
	if !ok {
		return false
	}
	c := def.Inbox
	switch kind {
	case inboxKindComment:
		switch action {
		case "read":
			return c.CommentsRead
		case "write":
			return c.CommentsWrite
		case "hide":
			return c.CommentsHide
		case "like":
			return c.CommentsLike
		case "delete":
			return c.CommentsDelete
		}
	case inboxKindDM:
		switch action {
		case "read":
			return c.DMsRead
		case "write":
			return c.DMsWrite
		}
	case inboxKindMention:
		if action == "read" {
			return c.MentionsRead
		}
	case inboxKindReview:
		switch action {
		case "read":
			return c.ReviewsRead
		case "write":
			return c.ReviewsReply
		}
	}
	if kind == inboxKindComment && action == "private_reply" {
		return c.PrivateReply
	}
	return false
}

// inboxUpsertInput is the shape the poll worker (and, eventually,
// webhook handlers) hands to upsertInboxItem. Platform-side fetchers
// populate this; the repo layer doesn't care where it came from.
type inboxUpsertInput struct {
	ProjectID        string
	SocialAccountID  int64
	Platform         string
	Kind             string
	ExternalID       string
	ParentExternalID string
	PostID           int64  // 0 when not a reply to our post
	ExternalPostID   string // populated for mentions or replies to foreign posts
	AuthorExternalID string
	AuthorName       string
	AuthorHandle     string
	AuthorAvatarURL  string
	Body             string
	MediaJSON        string // pre-marshalled JSON; "" allowed
	Permalink        string
	Rating           int       // 0 = N/A
	OccurredAt       time.Time // platform-reported event time
	RawJSON          string    // raw upstream payload; "" allowed
}

// upsertInboxItem inserts a new inbox row or updates the body/status
// of an existing one. Returns (id, inserted, error) — `inserted` is
// false on update so callers can decide whether to fire a
// notification. Dedup is via UNIQUE(social_account_id, kind,
// external_id) — re-polls and (future) webhook replays are no-ops on
// the body/permalink axis but DO refresh fetched_at.
func upsertInboxItem(db *sql.DB, in inboxUpsertInput) (int64, bool, error) {
	if in.SocialAccountID == 0 {
		return 0, false, errors.New("social_account_id required")
	}
	if !validInboxKinds[in.Kind] {
		return 0, false, fmt.Errorf("invalid kind %q", in.Kind)
	}
	if in.ExternalID == "" {
		return 0, false, errors.New("external_id required")
	}
	if in.OccurredAt.IsZero() {
		in.OccurredAt = time.Now().UTC()
	}

	var existingID int64
	err := db.QueryRow(
		`SELECT id FROM inbox_items WHERE social_account_id=? AND kind=? AND external_id=?`,
		in.SocialAccountID, in.Kind, in.ExternalID,
	).Scan(&existingID)

	if err == nil && existingID > 0 {
		// Update body / author / permalink in case the platform mutated
		// them (comment edited, profile renamed). Don't touch status —
		// local state (read/replied/archived) stays.
		_, uerr := db.Exec(
			`UPDATE inbox_items
			   SET body=?, author_name=?, author_handle=?, author_avatar_url=?,
			       permalink=?, media_json=?, raw_json=?, fetched_at=CURRENT_TIMESTAMP
			 WHERE id=?`,
			in.Body, in.AuthorName, in.AuthorHandle, in.AuthorAvatarURL,
			in.Permalink, nullableStr(in.MediaJSON), nullableStr(in.RawJSON),
			existingID,
		)
		return existingID, false, uerr
	}
	if err != nil && err != sql.ErrNoRows {
		return 0, false, err
	}

	res, ierr := db.Exec(
		`INSERT INTO inbox_items
		   (project_id, social_account_id, platform, kind, external_id,
		    parent_external_id, post_id, external_post_id,
		    author_external_id, author_name, author_handle, author_avatar_url,
		    body, media_json, permalink, rating, occurred_at, raw_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.ProjectID, in.SocialAccountID, in.Platform, in.Kind, in.ExternalID,
		nullableStr(in.ParentExternalID), nullableInt(in.PostID), nullableStr(in.ExternalPostID),
		nullableStr(in.AuthorExternalID), in.AuthorName, in.AuthorHandle, in.AuthorAvatarURL,
		in.Body, nullableStr(in.MediaJSON), in.Permalink, nullableInt(int64(in.Rating)),
		in.OccurredAt.UTC().Format(time.RFC3339), nullableStr(in.RawJSON),
	)
	if ierr != nil {
		return 0, false, ierr
	}
	id, _ := res.LastInsertId()
	return id, true, nil
}

// inboxListFilter narrows the inbox_list query. Empty fields are
// no-ops. ProjectID is mandatory — the worker scopes all reads to
// the caller's project to keep multi-tenant prod safe.
type inboxListFilter struct {
	ProjectID        string
	SocialAccountIDs []int64
	Kinds            []string
	Statuses         []string
	Since            time.Time // zero = no lower bound
	Limit            int       // capped at 200; default 50
}

// listInboxItems returns inbox rows matching the filter, newest first
// by occurred_at. Caller-friendly defaults: limit 50, max 200, status
// defaults to all non-archived if Statuses is empty.
func listInboxItems(db *sql.DB, f inboxListFilter) ([]inboxItem, error) {
	if f.ProjectID == "" {
		return nil, errors.New("project_id required")
	}
	q := `SELECT id, project_id, social_account_id, platform, kind, external_id,
	             COALESCE(parent_external_id,''), COALESCE(post_id,0), COALESCE(external_post_id,''),
	             COALESCE(author_external_id,''), COALESCE(author_name,''),
	             COALESCE(author_handle,''), COALESCE(author_avatar_url,''),
	             COALESCE(body,''), COALESCE(media_json,''), COALESCE(permalink,''),
	             COALESCE(rating,0), occurred_at, fetched_at, status
	      FROM inbox_items
	      WHERE project_id=?`
	args := []any{f.ProjectID}

	if len(f.SocialAccountIDs) > 0 {
		q += " AND social_account_id IN (" + placeholders(len(f.SocialAccountIDs)) + ")"
		for _, id := range f.SocialAccountIDs {
			args = append(args, id)
		}
	}
	if len(f.Kinds) > 0 {
		q += " AND kind IN (" + placeholders(len(f.Kinds)) + ")"
		for _, k := range f.Kinds {
			args = append(args, k)
		}
	}
	if len(f.Statuses) > 0 {
		q += " AND status IN (" + placeholders(len(f.Statuses)) + ")"
		for _, s := range f.Statuses {
			args = append(args, s)
		}
	} else {
		// Default: hide archived items from the noisy default view.
		q += " AND status != 'archived'"
	}
	if !f.Since.IsZero() {
		q += " AND occurred_at >= ?"
		args = append(args, f.Since.UTC().Format(time.RFC3339))
	}

	limit := f.Limit
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	q += " ORDER BY occurred_at DESC LIMIT ?"
	args = append(args, limit)

	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []inboxItem{}
	for rows.Next() {
		var it inboxItem
		var mediaJSON string
		if err := rows.Scan(
			&it.ID, &it.ProjectID, &it.SocialAccountID, &it.Platform, &it.Kind, &it.ExternalID,
			&it.ParentExternalID, &it.PostID, &it.ExternalPostID,
			&it.AuthorExternalID, &it.AuthorName, &it.AuthorHandle, &it.AuthorAvatarURL,
			&it.Body, &mediaJSON, &it.Permalink, &it.Rating,
			&it.OccurredAt, &it.FetchedAt, &it.Status,
		); err != nil {
			return nil, err
		}
		if mediaJSON != "" {
			it.Media = json.RawMessage(mediaJSON)
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// getInboxItem fetches a single row scoped to the project. Returns
// (nil, nil) when no row matches so handlers can return a clean
// "not found" without scrutinising sql.ErrNoRows.
func getInboxItem(db *sql.DB, projectID string, id int64) (*inboxItem, error) {
	if projectID == "" {
		return nil, errors.New("project_id required")
	}
	row := db.QueryRow(
		`SELECT id, project_id, social_account_id, platform, kind, external_id,
		        COALESCE(parent_external_id,''), COALESCE(post_id,0), COALESCE(external_post_id,''),
		        COALESCE(author_external_id,''), COALESCE(author_name,''),
		        COALESCE(author_handle,''), COALESCE(author_avatar_url,''),
		        COALESCE(body,''), COALESCE(media_json,''), COALESCE(permalink,''),
		        COALESCE(rating,0), occurred_at, fetched_at, status
		 FROM inbox_items
		 WHERE id=? AND project_id=?`,
		id, projectID,
	)
	var it inboxItem
	var mediaJSON string
	err := row.Scan(
		&it.ID, &it.ProjectID, &it.SocialAccountID, &it.Platform, &it.Kind, &it.ExternalID,
		&it.ParentExternalID, &it.PostID, &it.ExternalPostID,
		&it.AuthorExternalID, &it.AuthorName, &it.AuthorHandle, &it.AuthorAvatarURL,
		&it.Body, &mediaJSON, &it.Permalink, &it.Rating,
		&it.OccurredAt, &it.FetchedAt, &it.Status,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if mediaJSON != "" {
		it.Media = json.RawMessage(mediaJSON)
	}
	return &it, nil
}

// getInboxThread walks parent_external_id back to the root and then
// pulls every descendant under that root. Returns rows in occurred_at
// ASC so callers can render the conversation top-down. Empty when the
// item has no parent and no children — caller falls back to the
// single-row response.
func getInboxThread(db *sql.DB, projectID string, item *inboxItem) ([]inboxItem, error) {
	if item == nil {
		return nil, nil
	}
	// Walk up to the root. Cap depth to avoid pathological cycles in
	// data we don't fully trust.
	rootExternal := item.ExternalID
	if item.ParentExternalID != "" {
		rootExternal = item.ParentExternalID
		for hops := 0; hops < 32; hops++ {
			var parent string
			err := db.QueryRow(
				`SELECT COALESCE(parent_external_id,'') FROM inbox_items
				 WHERE project_id=? AND social_account_id=? AND kind=? AND external_id=?`,
				projectID, item.SocialAccountID, item.Kind, rootExternal,
			).Scan(&parent)
			if err != nil || parent == "" {
				break
			}
			rootExternal = parent
		}
	}
	rows, err := db.Query(
		`SELECT id, project_id, social_account_id, platform, kind, external_id,
		        COALESCE(parent_external_id,''), COALESCE(post_id,0), COALESCE(external_post_id,''),
		        COALESCE(author_external_id,''), COALESCE(author_name,''),
		        COALESCE(author_handle,''), COALESCE(author_avatar_url,''),
		        COALESCE(body,''), COALESCE(media_json,''), COALESCE(permalink,''),
		        COALESCE(rating,0), occurred_at, fetched_at, status
		 FROM inbox_items
		 WHERE project_id=? AND social_account_id=? AND kind=?
		   AND (external_id=? OR parent_external_id=?)
		 ORDER BY occurred_at ASC`,
		projectID, item.SocialAccountID, item.Kind, rootExternal, rootExternal,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []inboxItem{}
	for rows.Next() {
		var it inboxItem
		var mediaJSON string
		if err := rows.Scan(
			&it.ID, &it.ProjectID, &it.SocialAccountID, &it.Platform, &it.Kind, &it.ExternalID,
			&it.ParentExternalID, &it.PostID, &it.ExternalPostID,
			&it.AuthorExternalID, &it.AuthorName, &it.AuthorHandle, &it.AuthorAvatarURL,
			&it.Body, &mediaJSON, &it.Permalink, &it.Rating,
			&it.OccurredAt, &it.FetchedAt, &it.Status,
		); err != nil {
			return nil, err
		}
		if mediaJSON != "" {
			it.Media = json.RawMessage(mediaJSON)
		}
		out = append(out, it)
	}
	return out, rows.Err()
}

// setInboxStatus writes a new status to one item, project-scoped.
// Caller is responsible for validating the status value before the
// call; this guards anyway and rejects unknown values.
func setInboxStatus(db *sql.DB, projectID string, id int64, status string) error {
	if !validInboxStatuses[status] {
		return fmt.Errorf("invalid status %q", status)
	}
	res, err := db.Exec(
		`UPDATE inbox_items SET status=? WHERE id=? AND project_id=?`,
		status, id, projectID,
	)
	if err != nil {
		return err
	}
	n, _ := res.RowsAffected()
	if n == 0 {
		return sql.ErrNoRows
	}
	return nil
}

// markInboxRepliedByExternalID flips the parent row to status='replied'
// once a reply has been posted upstream. No-op when the parent isn't
// in our table yet (we replied to a thread we hadn't synced).
func markInboxRepliedByExternalID(db *sql.DB, socialAccountID int64, kind, externalID string) error {
	if externalID == "" {
		return nil
	}
	_, err := db.Exec(
		`UPDATE inbox_items SET status='replied'
		 WHERE social_account_id=? AND kind=? AND external_id=?
		   AND status NOT IN ('replied','hidden','archived')`,
		socialAccountID, kind, externalID,
	)
	return err
}

func nullableStr(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullableInt(n int64) any {
	if n == 0 {
		return nil
	}
	return n
}

func placeholders(n int) string {
	if n <= 0 {
		return ""
	}
	return strings.Repeat("?,", n-1) + "?"
}
