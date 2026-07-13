// Navigation menus + items. Multi-site (v2.0): menus are site-scoped;
// menu items continue to inherit siting via their parent menu (no
// site_id on menu_items).

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
	SiteID    int64      `json:"site_id"`
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
	TargetKind string `json:"target_kind"`
	TargetID   *int64 `json:"target_id,omitempty"`
	// TargetSlug is the SLUG of the linked post / page / term, resolved
	// at read time via a LEFT JOIN against posts/terms. Public render
	// uses slug-based URLs (/posts/welcome, /about, /category/markets),
	// not id-based, so the slug is what we actually need at render.
	// Empty when target_kind='url' or the linked row was deleted.
	TargetSlug string     `json:"target_slug,omitempty"`
	TargetURL  string     `json:"target_url,omitempty"`
	Position   int        `json:"position"`
	Children   []MenuItem `json:"children,omitempty"`
}

func dbCreateMenu(db *sql.DB, projectID string, siteID int64, slug, name string) (*Menu, error) {
	if slug == "" {
		return nil, errors.New("slug required")
	}
	res, err := db.Exec(`INSERT INTO menus (project_id, site_id, slug, name) VALUES (?, ?, ?, ?)`,
		projectID, siteID, slugify(slug), name)
	if err != nil {
		return nil, fmt.Errorf("insert menu: %w", err)
	}
	id, _ := res.LastInsertId()
	return dbGetMenu(db, projectID, siteID, id)
}

func dbGetMenu(db *sql.DB, projectID string, siteID int64, id int64) (*Menu, error) {
	row := db.QueryRow(`SELECT id, project_id, COALESCE(site_id, 0), slug, name, created_at
		FROM menus WHERE project_id=? AND site_id=? AND id=?`, projectID, siteID, id)
	m := &Menu{}
	var created sql.NullString
	if err := row.Scan(&m.ID, &m.ProjectID, &m.SiteID, &m.Slug, &m.Name, &created); err != nil {
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

func dbGetMenuBySlug(db *sql.DB, projectID string, siteID int64, slug string) (*Menu, error) {
	var id int64
	if err := db.QueryRow(`SELECT id FROM menus WHERE project_id=? AND site_id=? AND slug=?`,
		projectID, siteID, slug).Scan(&id); err != nil {
		return nil, err
	}
	return dbGetMenu(db, projectID, siteID, id)
}

func dbListMenus(db *sql.DB, projectID string, siteID int64) ([]Menu, error) {
	rows, err := db.Query(`SELECT id, project_id, COALESCE(site_id, 0), slug, name, created_at
		FROM menus WHERE project_id=? AND site_id=? ORDER BY name`, projectID, siteID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Menu
	for rows.Next() {
		var m Menu
		var created sql.NullString
		if err := rows.Scan(&m.ID, &m.ProjectID, &m.SiteID, &m.Slug, &m.Name, &created); err != nil {
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
	// LEFT JOIN against posts + terms so the slug of the linked row
	// comes back alongside the menu_item. Both joins use the same
	// target_id; only one matches per row (target_kind disambiguates).
	rows, err := db.Query(`
		SELECT mi.id, mi.menu_id, mi.parent_id, mi.label, mi.target_kind,
		       mi.target_id, mi.target_url, mi.position,
		       COALESCE(
		         CASE WHEN mi.target_kind IN ('post','page') THEN p.slug END,
		         CASE WHEN mi.target_kind = 'term'           THEN t.slug END,
		         ''
		       ) AS target_slug
			FROM menu_items mi
			JOIN menus m ON m.id = mi.menu_id
			LEFT JOIN posts p
			  ON p.id = mi.target_id
			 AND mi.target_kind IN ('post','page')
			 AND p.project_id = m.project_id
			 AND p.site_id = m.site_id
			 AND p.deleted_at IS NULL
			LEFT JOIN terms t
			  ON t.id = mi.target_id
			 AND mi.target_kind = 'term'
			 AND t.project_id = m.project_id
			 AND t.site_id = m.site_id
		WHERE mi.menu_id = ?
		ORDER BY mi.position, mi.id`, menuID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []MenuItem
	for rows.Next() {
		var it MenuItem
		var parent, tid sql.NullInt64
		if err := rows.Scan(&it.ID, &it.MenuID, &parent, &it.Label, &it.TargetKind, &tid, &it.TargetURL, &it.Position, &it.TargetSlug); err != nil {
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

func dbSetMenuItems(db *sql.DB, projectID string, siteID, menuID int64, items []MenuItem) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var exists int
	if err := tx.QueryRow(`SELECT 1 FROM menus WHERE id=? AND project_id=? AND site_id=?`, menuID, projectID, siteID).Scan(&exists); err != nil {
		return fmt.Errorf("menu %d not found in site: %w", menuID, err)
	}
	var validateTargets func([]MenuItem) error
	validateTargets = func(items []MenuItem) error {
		for _, item := range items {
			if item.TargetID != nil {
				var found int
				switch item.TargetKind {
				case "post", "page":
					err = tx.QueryRow(`SELECT 1 FROM posts WHERE id=? AND project_id=? AND site_id=? AND kind=? AND deleted_at IS NULL`,
						*item.TargetID, projectID, siteID, item.TargetKind).Scan(&found)
				case "term":
					err = tx.QueryRow(`SELECT 1 FROM terms WHERE id=? AND project_id=? AND site_id=?`,
						*item.TargetID, projectID, siteID).Scan(&found)
				}
				if err != nil {
					return fmt.Errorf("menu target %s %d not found in site", item.TargetKind, *item.TargetID)
				}
			}
			if err := validateTargets(item.Children); err != nil {
				return err
			}
		}
		return nil
	}
	if err := validateTargets(items); err != nil {
		return err
	}
	if _, err := tx.Exec(`DELETE FROM menu_items WHERE menu_id=?`, menuID); err != nil {
		return err
	}
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
	siteID, err := resolveSiteIDFromArgs(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	m, err := dbCreateMenu(ctx.AppDB(), pid, siteID, asString(args["slug"]), asString(args["name"]))
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
	siteID, err := resolveSiteIDFromArgs(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	menus, err := dbListMenus(ctx.AppDB(), pid, siteID)
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
	siteID, err := resolveSiteIDFromArgs(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	if id, ok := asInt64(args["id"]); ok && id > 0 {
		m, err := dbGetMenu(ctx.AppDB(), pid, siteID, id)
		if err != nil {
			return nil, err
		}
		return map[string]any{"menu": m}, nil
	}
	slug := asString(args["slug"])
	if slug == "" {
		return nil, errors.New("id or slug required")
	}
	m, err := dbGetMenuBySlug(ctx.AppDB(), pid, siteID, slug)
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
	siteID, err := resolveSiteIDFromArgs(ctx.AppDB(), pid, args)
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
	if err := dbSetMenuItems(ctx.AppDB(), pid, siteID, menuID, items); err != nil {
		return nil, err
	}
	invalidatePageCacheForSite(siteID)
	m, err := dbGetMenu(ctx.AppDB(), pid, siteID, menuID)
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
	siteID, err := resolveSiteIDFromRequest(ctx.AppDB(), pid, r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		menus, err := dbListMenus(ctx.AppDB(), pid, siteID)
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
		body["_site_id"] = siteID
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

var _ = strconv.Atoi
var _ = strings.TrimSpace
