// Redirect rules. Checked before slug resolution on every public
// request — old paths from a content migration (WordPress, custom) can
// be preserved with one INSERT each. Codes default to 301.

package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"net/http"

	sdk "github.com/apteva/app-sdk"
)

type Redirect struct {
	ID        int64  `json:"id"`
	ProjectID string `json:"project_id,omitempty"`
	From      string `json:"from_path"`
	To        string `json:"to_path"`
	Code      int    `json:"code"`
	CreatedAt string `json:"created_at,omitempty"`
}

func dbCreateRedirect(db *sql.DB, projectID, from, to string, code int) (*Redirect, error) {
	if from == "" || to == "" {
		return nil, errors.New("from_path and to_path required")
	}
	if code != 301 && code != 302 {
		code = 301
	}
	_, err := db.Exec(`INSERT INTO redirects (project_id, from_path, to_path, code)
		VALUES (?, ?, ?, ?)
		ON CONFLICT(project_id, from_path) DO UPDATE SET to_path=excluded.to_path, code=excluded.code`,
		projectID, from, to, code)
	if err != nil {
		return nil, err
	}
	var r Redirect
	var created sql.NullString
	if err := db.QueryRow(`SELECT id, project_id, from_path, to_path, code, created_at FROM redirects
		WHERE project_id=? AND from_path=?`, projectID, from).Scan(&r.ID, &r.ProjectID, &r.From, &r.To, &r.Code, &created); err != nil {
		return nil, err
	}
	if created.Valid {
		r.CreatedAt = created.String
	}
	return &r, nil
}

func dbLookupRedirect(db *sql.DB, projectID, fromPath string) (*Redirect, error) {
	var r Redirect
	var created sql.NullString
	err := db.QueryRow(`SELECT id, project_id, from_path, to_path, code, created_at FROM redirects
		WHERE project_id=? AND from_path=?`, projectID, fromPath).Scan(&r.ID, &r.ProjectID, &r.From, &r.To, &r.Code, &created)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if created.Valid {
		r.CreatedAt = created.String
	}
	return &r, nil
}

func dbListRedirects(db *sql.DB, projectID string) ([]Redirect, error) {
	rows, err := db.Query(`SELECT id, project_id, from_path, to_path, code, created_at FROM redirects
		WHERE project_id=? ORDER BY from_path`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Redirect
	for rows.Next() {
		var r Redirect
		var created sql.NullString
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.From, &r.To, &r.Code, &created); err != nil {
			return nil, err
		}
		if created.Valid {
			r.CreatedAt = created.String
		}
		out = append(out, r)
	}
	return out, nil
}

// ── MCP tool handlers ────────────────────────────────────────────

func (a *App) toolRedirectsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	code := 301
	if v, ok := asInt64(args["code"]); ok {
		code = int(v)
	}
	r, err := dbCreateRedirect(ctx.AppDB(), pid, asString(args["from_path"]), asString(args["to_path"]), code)
	if err != nil {
		return nil, err
	}
	return map[string]any{"redirect": r}, nil
}

func (a *App) toolRedirectsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	rs, err := dbListRedirects(ctx.AppDB(), pid)
	if err != nil {
		return nil, err
	}
	return map[string]any{"redirects": rs}, nil
}

// ── REST handler ─────────────────────────────────────────────────

func (a *App) handleHTTPRedirects(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		rs, err := dbListRedirects(ctx.AppDB(), pid)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpJSON(w, map[string]any{"redirects": rs})
	case http.MethodPost:
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		body["_project_id"] = pid
		out, err := a.toolRedirectsCreate(ctx, body)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}
