// Taxonomy — categories + tags share one table, distinguished by
// `kind`. Hierarchical (parent_id) so categories can nest; tags do not
// nest in practice but the column is available for both.

package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type Term struct {
	ID          int64  `json:"id"`
	ProjectID   string `json:"project_id,omitempty"`
	Kind        string `json:"kind"`
	Name        string `json:"name"`
	Slug        string `json:"slug"`
	ParentID    *int64 `json:"parent_id,omitempty"`
	Description string `json:"description,omitempty"`
	CreatedAt   string `json:"created_at,omitempty"`
}

func dbCreateTerm(db *sql.DB, projectID, kind, name, slug, description string, parentID *int64) (*Term, error) {
	if kind != "category" && kind != "tag" {
		return nil, fmt.Errorf("kind must be category or tag (got %q)", kind)
	}
	if name == "" {
		return nil, errors.New("name required")
	}
	if slug == "" {
		slug = slugify(name)
	} else {
		slug = slugify(slug)
	}
	res, err := db.Exec(`INSERT INTO terms (project_id, kind, name, slug, parent_id, description)
		VALUES (?, ?, ?, ?, ?, ?)`,
		projectID, kind, name, slug, nullableInt(parentID), description)
	if err != nil {
		return nil, fmt.Errorf("insert term: %w", err)
	}
	id, _ := res.LastInsertId()
	return dbGetTerm(db, projectID, id)
}

func dbGetTerm(db *sql.DB, projectID string, id int64) (*Term, error) {
	row := db.QueryRow(`SELECT id, project_id, kind, name, slug, parent_id, description, created_at
		FROM terms WHERE project_id=? AND id=?`, projectID, id)
	return scanTerm(row)
}

func dbGetTermBySlug(db *sql.DB, projectID, kind, slug string) (*Term, error) {
	row := db.QueryRow(`SELECT id, project_id, kind, name, slug, parent_id, description, created_at
		FROM terms WHERE project_id=? AND kind=? AND slug=?`, projectID, kind, slug)
	return scanTerm(row)
}

func scanTerm(row rowScanner) (*Term, error) {
	var t Term
	var parent sql.NullInt64
	var created sql.NullString
	if err := row.Scan(&t.ID, &t.ProjectID, &t.Kind, &t.Name, &t.Slug, &parent, &t.Description, &created); err != nil {
		return nil, err
	}
	if parent.Valid {
		v := parent.Int64
		t.ParentID = &v
	}
	if created.Valid {
		t.CreatedAt = created.String
	}
	return &t, nil
}

func dbListTerms(db *sql.DB, projectID, kind, q string) ([]Term, error) {
	where := []string{"project_id = ?"}
	args := []any{projectID}
	if kind != "" {
		where = append(where, "kind = ?")
		args = append(args, kind)
	}
	if q != "" {
		where = append(where, "(name LIKE ? OR slug LIKE ?)")
		like := "%" + q + "%"
		args = append(args, like, like)
	}
	rows, err := db.Query(`SELECT id, project_id, kind, name, slug, parent_id, description, created_at
		FROM terms WHERE `+strings.Join(where, " AND ")+` ORDER BY kind, name`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Term
	for rows.Next() {
		t, err := scanTerm(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, nil
}

func dbAssignTerms(db *sql.DB, postID int64, termIDs []int64) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	stmt, err := tx.Prepare(`INSERT OR IGNORE INTO post_terms (post_id, term_id) VALUES (?, ?)`)
	if err != nil {
		return err
	}
	defer stmt.Close()
	for _, tid := range termIDs {
		if _, err := stmt.Exec(postID, tid); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func dbUnassignTerms(db *sql.DB, postID int64, termIDs []int64) error {
	if len(termIDs) == 0 {
		return nil
	}
	placeholders := strings.Repeat("?,", len(termIDs))
	placeholders = placeholders[:len(placeholders)-1]
	args := []any{postID}
	for _, tid := range termIDs {
		args = append(args, tid)
	}
	_, err := db.Exec(`DELETE FROM post_terms WHERE post_id=? AND term_id IN (`+placeholders+`)`, args...)
	return err
}

// dbListPostTerms returns the terms attached to a post, joined for
// display.
func dbListPostTerms(db *sql.DB, postID int64) ([]Term, error) {
	rows, err := db.Query(`SELECT t.id, t.project_id, t.kind, t.name, t.slug, t.parent_id, t.description, t.created_at
		FROM terms t JOIN post_terms pt ON pt.term_id = t.id
		WHERE pt.post_id = ? ORDER BY t.kind, t.name`, postID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Term
	for rows.Next() {
		t, err := scanTerm(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *t)
	}
	return out, nil
}

// ── MCP tool handlers ────────────────────────────────────────────

func (a *App) toolTermsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	var parent *int64
	if v, ok := asInt64(args["parent_id"]); ok && v > 0 {
		parent = &v
	}
	term, err := dbCreateTerm(ctx.AppDB(), pid,
		asString(args["kind"]),
		asString(args["name"]),
		asString(args["slug"]),
		asString(args["description"]),
		parent)
	if err != nil {
		return nil, err
	}
	return map[string]any{"term": term}, nil
}

func (a *App) toolTermsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	terms, err := dbListTerms(ctx.AppDB(), pid, asString(args["kind"]), asString(args["q"]))
	if err != nil {
		return nil, err
	}
	return map[string]any{"terms": terms}, nil
}

func (a *App) toolTermsAssign(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if _, err := resolveProjectFromArgs(args); err != nil {
		return nil, err
	}
	postID, ok := asInt64(args["post_id"])
	if !ok || postID == 0 {
		return nil, errors.New("post_id required")
	}
	ids, err := parseIDList(args["term_ids"])
	if err != nil {
		return nil, err
	}
	if err := dbAssignTerms(ctx.AppDB(), postID, ids); err != nil {
		return nil, err
	}
	invalidatePageCache()
	return map[string]any{"ok": true}, nil
}

func (a *App) toolTermsUnassign(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if _, err := resolveProjectFromArgs(args); err != nil {
		return nil, err
	}
	postID, ok := asInt64(args["post_id"])
	if !ok || postID == 0 {
		return nil, errors.New("post_id required")
	}
	ids, err := parseIDList(args["term_ids"])
	if err != nil {
		return nil, err
	}
	if err := dbUnassignTerms(ctx.AppDB(), postID, ids); err != nil {
		return nil, err
	}
	invalidatePageCache()
	return map[string]any{"ok": true}, nil
}

func parseIDList(raw any) ([]int64, error) {
	if raw == nil {
		return nil, errors.New("term_ids required")
	}
	b, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	var arr []any
	if err := json.Unmarshal(b, &arr); err != nil {
		return nil, fmt.Errorf("term_ids: expected array, got %T", raw)
	}
	out := make([]int64, 0, len(arr))
	for _, v := range arr {
		if n, ok := asInt64(v); ok {
			out = append(out, n)
		}
	}
	return out, nil
}

// ── REST handler ─────────────────────────────────────────────────

func (a *App) handleHTTPTermsCollection(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		kind := r.URL.Query().Get("kind")
		q := r.URL.Query().Get("q")
		terms, err := dbListTerms(ctx.AppDB(), pid, kind, q)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpJSON(w, map[string]any{"terms": terms})
	case http.MethodPost:
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		body["_project_id"] = pid
		out, err := a.toolTermsCreate(ctx, body)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// silence import linters when only used in REST helpers.
var _ = strconv.Atoi
