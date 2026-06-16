// Community-level handlers — create / list / get.
//
// "communities" is the tenancy boundary: every other table joins back
// here via community_id. Slug is unique within (project_id), so callers
// can address a community by slug from outside the DB without juggling
// the random id.

package main

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type Community struct {
	ID          string  `json:"id"`
	ProjectID   string  `json:"project_id"`
	Slug        string  `json:"slug"`
	Name        string  `json:"name"`
	Description string  `json:"description"`
	CreatedAt   string  `json:"created_at"`
	ArchivedAt  *string `json:"archived_at,omitempty"`
}

func communitiesTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "communities_create",
			Description: "Create a community. Args: slug (required, url-safe), name (required), description?.",
			InputSchema: schemaObject(map[string]any{
				"slug":        map[string]any{"type": "string"},
				"name":        map[string]any{"type": "string"},
				"description": map[string]any{"type": "string"},
			}, []string{"slug", "name"}),
			Handler: toolCommunitiesCreate,
		},
		{
			Name:        "communities_list",
			Description: "List communities in this project scope. Archived hidden by default; pass include_archived=true to see them.",
			InputSchema: schemaObject(map[string]any{
				"include_archived": map[string]any{"type": "boolean"},
			}, nil),
			Handler: toolCommunitiesList,
		},
		{
			Name:        "communities_get",
			Description: "Fetch one community by id or slug. Args: id? or slug?. Exactly one must be set.",
			InputSchema: schemaObject(map[string]any{
				"id":   map[string]any{"type": "string"},
				"slug": map[string]any{"type": "string"},
			}, nil),
			Handler: toolCommunitiesGet,
		},
		{
			Name:        "communities_update",
			Description: "Update a community's name or description. Args: id (required), name?, description?.",
			InputSchema: schemaObject(map[string]any{
				"id":          map[string]any{"type": "string"},
				"name":        map[string]any{"type": "string"},
				"description": map[string]any{"type": "string"},
			}, []string{"id"}),
			Handler: toolCommunitiesUpdate,
		},
		{
			Name:        "communities_archive",
			Description: "Soft-delete a community. Rows stay; default list filters it out. Args: id (required).",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "string"},
			}, []string{"id"}),
			Handler: toolCommunitiesArchive,
		},
	}
}

func toolCommunitiesUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := mustStr(args, "id")
	if err != nil {
		return nil, err
	}
	db := ctx.AppDB()
	if err := ensureCommunityVisible(ctx, db, id); err != nil {
		return nil, err
	}
	sets := []string{}
	vals := []any{}
	if v, ok := args["name"].(string); ok && v != "" {
		sets = append(sets, "name = ?")
		vals = append(vals, v)
	}
	if v, ok := args["description"].(string); ok {
		sets = append(sets, "description = ?")
		vals = append(vals, v)
	}
	if len(sets) == 0 {
		return nil, errors.New("nothing to update")
	}
	vals = append(vals, id)
	if _, err := db.Exec(
		`UPDATE communities SET `+strings.Join(sets, ", ")+` WHERE id = ?`, vals...,
	); err != nil {
		return nil, err
	}
	c, err := loadCommunity(db, id)
	if err != nil {
		return nil, err
	}
	emit(ctx, "community.updated", map[string]any{
		"community_id": c.ID,
		"slug":         c.Slug,
	})
	return c, nil
}

func toolCommunitiesArchive(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id, err := mustStr(args, "id")
	if err != nil {
		return nil, err
	}
	db := ctx.AppDB()
	if err := ensureCommunityVisible(ctx, db, id); err != nil {
		return nil, err
	}
	if _, err := db.Exec(
		`UPDATE communities SET archived_at = CURRENT_TIMESTAMP WHERE id = ?`, id,
	); err != nil {
		return nil, err
	}
	emit(ctx, "community.archived", map[string]any{"community_id": id})
	return map[string]any{"ok": true}, nil
}

var slugRE = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{1,62}$`)

func toolCommunitiesCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	slug, err := mustStr(args, "slug")
	if err != nil {
		return nil, err
	}
	slug = strings.ToLower(slug)
	if !slugRE.MatchString(slug) {
		return nil, fmt.Errorf("slug %q invalid: must be lowercase, 2-63 chars, [a-z0-9-]", slug)
	}
	name, err := mustStr(args, "name")
	if err != nil {
		return nil, err
	}
	desc := strArg(args, "description", "")
	projectID := scopeProject(ctx)
	if projectID == "" {
		return nil, errors.New("no project context")
	}
	id := newID("c")
	db := ctx.AppDB()
	_, err = db.Exec(
		`INSERT INTO communities (id, project_id, slug, name, description) VALUES (?, ?, ?, ?, ?)`,
		id, projectID, slug, name, desc,
	)
	if err != nil {
		return nil, fmt.Errorf("create community: %w", err)
	}
	c, err := loadCommunity(db, id)
	if err != nil {
		return nil, err
	}
	emit(ctx, "community.created", map[string]any{
		"community_id": c.ID,
		"slug":         c.Slug,
		"name":         c.Name,
	})
	return c, nil
}

func toolCommunitiesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	includeArchived, _ := args["include_archived"].(bool)
	projectID := scopeProject(ctx)
	if projectID == "" {
		return nil, errors.New("no project context")
	}
	q := `SELECT id, project_id, slug, name, description, created_at, archived_at
	      FROM communities WHERE project_id = ?`
	if !includeArchived {
		q += ` AND archived_at IS NULL`
	}
	q += ` ORDER BY created_at`
	rows, err := ctx.AppDB().Query(q, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Community{}
	for rows.Next() {
		c, err := scanCommunity(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return map[string]any{"communities": out}, nil
}

func toolCommunitiesGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := strArg(args, "id", "")
	slug := strArg(args, "slug", "")
	if id == "" && slug == "" {
		return nil, errors.New("id or slug required")
	}
	projectID := scopeProject(ctx)
	db := ctx.AppDB()
	var c Community
	var err error
	if id != "" {
		c, err = loadCommunity(db, id)
	} else {
		c, err = loadCommunityBySlug(db, projectID, slug)
	}
	if err != nil {
		return nil, err
	}
	if err := ensureCommunityReadable(ctx, c); err != nil {
		return nil, err
	}
	return c, nil
}

// ─── DB helpers ──────────────────────────────────────────────────

const communityCols = `id, project_id, slug, name, description, created_at, archived_at`

func scanCommunity(scan func(...any) error) (Community, error) {
	var c Community
	var arch sql.NullString
	if err := scan(&c.ID, &c.ProjectID, &c.Slug, &c.Name, &c.Description, &c.CreatedAt, &arch); err != nil {
		return c, err
	}
	if arch.Valid {
		v := arch.String
		c.ArchivedAt = &v
	}
	return c, nil
}

func loadCommunity(db *sql.DB, id string) (Community, error) {
	row := db.QueryRow(`SELECT `+communityCols+` FROM communities WHERE id = ?`, id)
	c, err := scanCommunity(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return c, fmt.Errorf("community %q not found", id)
	}
	return c, err
}

func loadCommunityBySlug(db *sql.DB, projectID, slug string) (Community, error) {
	row := db.QueryRow(
		`SELECT `+communityCols+` FROM communities WHERE project_id = ? AND slug = ?`,
		projectID, slug,
	)
	c, err := scanCommunity(row.Scan)
	if errors.Is(err, sql.ErrNoRows) {
		return c, fmt.Errorf("community %q not found", slug)
	}
	return c, err
}

// ─── HTTP ────────────────────────────────────────────────────────

func (a *App) httpCommunities(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, 405, "method not allowed")
		return
	}
	out, err := toolCommunitiesList(globalCtx, map[string]any{
		"include_archived": r.URL.Query().Get("include_archived") == "true",
	})
	if err != nil {
		writeErr(w, 500, err.Error())
		return
	}
	writeJSON(w, out)
}
