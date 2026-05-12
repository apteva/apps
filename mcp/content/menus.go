// Navigation menus + items. Menus are named buckets (slug + name); a
// menu's items are a flat list with parent_id for hierarchy + a
// position int for sibling order. The set_items tool replaces the
// whole tree atomically so the editor can persist a drag-reorder
// without diffing.

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

type Menu struct {
	ID        int64      `json:"id"`
	ProjectID string     `json:"project_id,omitempty"`
	Slug      string     `json:"slug"`
	Name      string     `json:"name"`
	CreatedAt string     `json:"created_at,omitempty"`
	Items     []MenuItem `json:"items,omitempty"`
}

type MenuItem struct {
	ID         int64  `json:"id"`
	MenuID     int64  `json:"menu_id"`
	ParentID   *int64 `json:"parent_id,omitempty"`
	Label      string `json:"label"`
	TargetKind string `json:"target_kind"` // post | page | term | url
	TargetID   *int64 `json:"target_id,omitempty"`
	TargetURL  string `json:"target_url,omitempty"`
	Position   int    `json:"position"`
	Children   []MenuItem `json:"children,omitempty"`
}

func dbCreateMenu(db *sql.DB, projectID, slug, name string) (*Menu, error) {
	if slug == "" {
		return nil, errors.New("slug required")
	}
	res, err := db.Exec(`INSERT INTO menus (project_id, slug, name) VALUES (?, ?, ?)`,
		projectID, slugify(slug), name)
	if err != nil {
		return nil, fmt.Errorf("insert menu: %w", err)
	}
	id, _ := res.LastInsertId()
	return dbGetMenu(db, projectID, id)
}

func dbGetMenu(db *sql.DB, projectID string, id int64) (*Menu, error) {
	row := db.QueryRow(`SELECT id, project_id, slug, name, created_at FROM menus WHERE project_id=? AND id=?`,
		projectID, id)
	m := &Menu{}
	var created sql.NullString
	if err := row.Scan(&m.ID, &m.ProjectID, &m.Slug, &m.Name, &created); err != nil {
		return nil, err
	}
	if created.Valid {
		m.CreatedAt = created.String
	}
	items, err := dbListMenuItems(db, m.ID)
	if err != nil {
		return nil, err
	}
	m.Items = nestItems(items)
	return m, nil
}

func dbGetMenuBySlug(db *sql.DB, projectID, slug string) (*Menu, error) {
	var id int64
	if err := db.QueryRow(`SELECT id FROM menus WHERE project_id=? AND slug=?`, projectID, slug).Scan(&id); err != nil {
		return nil, err
	}
	return dbGetMenu(db, projectID, id)
}

func dbListMenus(db *sql.DB, projectID string) ([]Menu, error) {
	rows, err := db.Query(`SELECT id, project_id, slug, name, created_at FROM menus WHERE project_id=? ORDER BY name`,
		projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Menu
	for rows.Next() {
		var m Menu
		var created sql.NullString
		if err := rows.Scan(&m.ID, &m.ProjectID, &m.Slug, &m.Name, &created); err != nil {
			return nil, err
		}
		if created.Valid {
			m.CreatedAt = created.String
		}
		out = append(out, m)
	}
	return out, nil
}

func dbListMenuItems(db *sql.DB, menuID int64) ([]MenuItem, error) {
	rows, err := db.Query(`SELECT id, menu_id, parent_id, label, target_kind, target_id, target_url, position
		FROM menu_items WHERE menu_id=? ORDER BY position, id`, menuID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MenuItem
	for rows.Next() {
		var it MenuItem
		var parent, tid sql.NullInt64
		if err := rows.Scan(&it.ID, &it.MenuID, &parent, &it.Label, &it.TargetKind, &tid, &it.TargetURL, &it.Position); err != nil {
			return nil, err
		}
		if parent.Valid {
			v := parent.Int64
			it.ParentID = &v
		}
		if tid.Valid {
			v := tid.Int64
			it.TargetID = &v
		}
		out = append(out, it)
	}
	return out, nil
}

// nestItems turns a flat parent-id list into a tree.
func nestItems(items []MenuItem) []MenuItem {
	byID := map[int64]*MenuItem{}
	for i := range items {
		byID[items[i].ID] = &items[i]
	}
	var roots []MenuItem
	for i := range items {
		it := &items[i]
		if it.ParentID == nil || byID[*it.ParentID] == nil {
			roots = append(roots, *it)
			continue
		}
		p := byID[*it.ParentID]
		p.Children = append(p.Children, *it)
	}
	return roots
}

// dbSetMenuItems replaces a menu's entire item tree in one transaction.
func dbSetMenuItems(db *sql.DB, menuID int64, items []MenuItem) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`DELETE FROM menu_items WHERE menu_id=?`, menuID); err != nil {
		return err
	}
	// Two-pass: first insert all items with their parent's *external*
	// id (the position in input), then map to actual IDs once they're
	// minted. Simpler: clients submit a flat list with `temp_parent`
	// references — but we accept the nested shape and resolve here.
	pos := 0
	var insert func(items []MenuItem, parentID *int64) error
	insert = func(items []MenuItem, parentID *int64) error {
		for i := range items {
			it := &items[i]
			pos++
			res, err := tx.Exec(`INSERT INTO menu_items (menu_id, parent_id, label, target_kind, target_id, target_url, position)
				VALUES (?, ?, ?, ?, ?, ?, ?)`,
				menuID, nullableInt(parentID), it.Label,
				defaultTargetKind(it.TargetKind), nullableInt(it.TargetID), it.TargetURL, pos)
			if err != nil {
				return err
			}
			id, _ := res.LastInsertId()
			if len(it.Children) > 0 {
				if err := insert(it.Children, &id); err != nil {
					return err
				}
			}
		}
		return nil
	}
	if err := insert(items, nil); err != nil {
		return err
	}
	return tx.Commit()
}

func defaultTargetKind(k string) string {
	switch k {
	case "post", "page", "term", "url":
		return k
	default:
		return "url"
	}
}

// ── MCP tool handlers ────────────────────────────────────────────

func (a *App) toolMenusCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	m, err := dbCreateMenu(ctx.AppDB(), pid, asString(args["slug"]), asString(args["name"]))
	if err != nil {
		return nil, err
	}
	return map[string]any{"menu": m}, nil
}

func (a *App) toolMenusList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	menus, err := dbListMenus(ctx.AppDB(), pid)
	if err != nil {
		return nil, err
	}
	return map[string]any{"menus": menus}, nil
}

func (a *App) toolMenusGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	if id, ok := asInt64(args["id"]); ok && id > 0 {
		m, err := dbGetMenu(ctx.AppDB(), pid, id)
		if err != nil {
			return nil, err
		}
		return map[string]any{"menu": m}, nil
	}
	slug := asString(args["slug"])
	if slug == "" {
		return nil, errors.New("id or slug required")
	}
	m, err := dbGetMenuBySlug(ctx.AppDB(), pid, slug)
	if err != nil {
		return nil, err
	}
	return map[string]any{"menu": m}, nil
}

func (a *App) toolMenusSetItems(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	menuID, ok := asInt64(args["menu_id"])
	if !ok || menuID == 0 {
		return nil, errors.New("menu_id required")
	}
	raw, ok := args["items"]
	if !ok || raw == nil {
		return nil, errors.New("items required")
	}
	b, _ := json.Marshal(raw)
	var items []MenuItem
	if err := json.Unmarshal(b, &items); err != nil {
		return nil, fmt.Errorf("items: %w", err)
	}
	if err := dbSetMenuItems(ctx.AppDB(), menuID, items); err != nil {
		return nil, err
	}
	invalidatePageCache()
	m, err := dbGetMenu(ctx.AppDB(), pid, menuID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"menu": m}, nil
}

// ── REST handler ─────────────────────────────────────────────────

func (a *App) handleHTTPMenus(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		menus, err := dbListMenus(ctx.AppDB(), pid)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpJSON(w, map[string]any{"menus": menus})
	case http.MethodPost:
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		body["_project_id"] = pid
		out, err := a.toolMenusCreate(ctx, body)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// silence unused imports when only some helpers are used
var _ = strconv.Atoi
var _ = strings.TrimSpace
