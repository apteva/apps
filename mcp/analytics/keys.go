package main

// Write keys — public, write-only credentials for the static-site tag.
// Management routes (list/create/revoke) are operator-only: they require
// an authenticated dashboard user (X-User-ID set by the platform). The
// public ingest path that *uses* a key lives in collect.go.

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"strings"
	"time"
)

type WriteKey struct {
	ID             int64    `json:"id"`
	Key            string   `json:"key"`
	Site           string   `json:"site"`
	ProjectID      string   `json:"project_id"`
	AllowedOrigins []string `json:"allowed_origins,omitempty"`
	CreatedAt      int64    `json:"created_at"`
	RevokedAt      int64    `json:"revoked_at,omitempty"`
	LastUsedTS     int64    `json:"last_used_ts,omitempty"`
	EventCount     int64    `json:"event_count"`
}

func newWriteKeyValue() string {
	b := make([]byte, 18)
	_, _ = rand.Read(b)
	return "wk_live_" + hex.EncodeToString(b)
}

func createWriteKey(db *sql.DB, site, projectID string, origins []string) (*WriteKey, error) {
	wk := &WriteKey{
		Key:            newWriteKeyValue(),
		Site:           site,
		ProjectID:      projectID,
		AllowedOrigins: origins,
		CreatedAt:      time.Now().UnixMilli(),
	}
	var originsJSON any
	if len(origins) > 0 {
		b, _ := json.Marshal(origins)
		originsJSON = string(b)
	}
	res, err := db.Exec(
		`INSERT INTO write_keys (key, site, project_id, allowed_origins, created_at)
		 VALUES (?, ?, ?, ?, ?)`,
		wk.Key, wk.Site, wk.ProjectID, originsJSON, wk.CreatedAt,
	)
	if err != nil {
		return nil, err
	}
	wk.ID, _ = res.LastInsertId()
	return wk, nil
}

func scanWriteKey(rows interface{ Scan(...any) error }) (*WriteKey, error) {
	var wk WriteKey
	var origins sql.NullString
	var revoked, lastUsed sql.NullInt64
	if err := rows.Scan(&wk.ID, &wk.Key, &wk.Site, &wk.ProjectID, &origins, &wk.CreatedAt, &revoked, &lastUsed, &wk.EventCount); err != nil {
		return nil, err
	}
	if origins.Valid && origins.String != "" {
		_ = json.Unmarshal([]byte(origins.String), &wk.AllowedOrigins)
	}
	wk.RevokedAt = revoked.Int64
	wk.LastUsedTS = lastUsed.Int64
	return &wk, nil
}

const writeKeyCols = `id, key, site, project_id, allowed_origins, created_at, revoked_at, last_used_ts, event_count`

// listWriteKeys returns keys for a project (or all when projectID == "").
func listWriteKeys(db *sql.DB, projectID string) ([]*WriteKey, error) {
	q := `SELECT ` + writeKeyCols + ` FROM write_keys`
	var args []any
	if projectID != "" {
		q += " WHERE project_id = ?"
		args = append(args, projectID)
	}
	q += " ORDER BY created_at DESC"
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*WriteKey{}
	for rows.Next() {
		wk, err := scanWriteKey(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, wk)
	}
	return out, rows.Err()
}

// lookupActiveWriteKey resolves a key value to its active row, or nil.
func lookupActiveWriteKey(db *sql.DB, key string) *WriteKey {
	if key == "" {
		return nil
	}
	row := db.QueryRow(`SELECT `+writeKeyCols+` FROM write_keys WHERE key = ? AND revoked_at IS NULL`, key)
	wk, err := scanWriteKey(row)
	if err != nil {
		return nil
	}
	return wk
}

func revokeWriteKey(db *sql.DB, id int64, projectID string) error {
	_, err := db.Exec(
		`UPDATE write_keys SET revoked_at = ? WHERE id = ? AND project_id = ? AND revoked_at IS NULL`,
		time.Now().UnixMilli(), id, projectID,
	)
	return err
}

func touchWriteKey(db *sql.DB, id int64) {
	_, _ = db.Exec(
		`UPDATE write_keys SET last_used_ts = ?, event_count = event_count + 1 WHERE id = ?`,
		time.Now().UnixMilli(), id,
	)
}

// ─── HTTP handlers (operator-only) ────────────────────────────────────

// requireUser returns the project scope for an authenticated request, or
// false when the caller is anonymous. The platform sets X-User-ID only
// for authenticated requests (session cookie / api key); the anonymous
// GET fall-through leaves it empty. Key management must never serve
// anonymous callers — the GET /keys route is reachable via that
// fall-through, so this check is the real gate.
func requireUser(r *http.Request) bool {
	return r.Header.Get("X-User-ID") != ""
}

// GET /keys?project_id= — list write keys for the project. Auth required.
func (a *App) handleKeysList(w http.ResponseWriter, r *http.Request) {
	if !requireUser(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	keys, err := listWriteKeys(globalCtx.AppDB(), r.URL.Query().Get("project_id"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"keys": keys})
}

// POST /keys — create a write key. Body: {site, project_id, allowed_origins?}.
func (a *App) handleKeysCreate(w http.ResponseWriter, r *http.Request) {
	if !requireUser(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var body struct {
		Site           string   `json:"site"`
		ProjectID      string   `json:"project_id"`
		AllowedOrigins []string `json:"allowed_origins"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	body.Site = strings.TrimSpace(body.Site)
	if body.Site == "" {
		http.Error(w, "site required", http.StatusBadRequest)
		return
	}
	if body.ProjectID == "" {
		body.ProjectID = globalCtx.CurrentProject()
	}
	wk, err := createWriteKey(globalCtx.AppDB(), body.Site, body.ProjectID, body.AllowedOrigins)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, wk)
}

// POST /keys/revoke — revoke a key. Body: {id, project_id}.
func (a *App) handleKeysRevoke(w http.ResponseWriter, r *http.Request) {
	if !requireUser(r) {
		http.Error(w, "unauthorized", http.StatusUnauthorized)
		return
	}
	var body struct {
		ID        int64  `json:"id"`
		ProjectID string `json:"project_id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json", http.StatusBadRequest)
		return
	}
	if body.ProjectID == "" {
		body.ProjectID = globalCtx.CurrentProject()
	}
	if err := revokeWriteKey(globalCtx.AppDB(), body.ID, body.ProjectID); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}
