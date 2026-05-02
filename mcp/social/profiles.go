package main

// Profiles — brand/client/site containers inside the social app.
// One project, one social install, many profiles, each grouping a
// set of social_accounts (FB Page + IG + X for one brand).
//
// This file owns:
//   - the Profile row type + slug helper
//   - CRUD MCP tools (profile_create / list / get / update / delete)
//   - HTTP wrappers for the panel
//   - resolveProfileArg: turns optional `profile` (slug) or
//     `profile_id` (int) input into a numeric id, used by the
//     existing account/post tools to filter or scope writes.
//
// Schema lives in migrations/003_profiles.sql. profile_id=0 means
// "unassigned"; legacy rows pre-migration carry that value.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Profile mirrors the row shape returned by all profile tools.
type Profile struct {
	ID           int64  `json:"id"`
	ProjectID    string `json:"project_id"`
	Name         string `json:"name"`
	Slug         string `json:"slug"`
	Description  string `json:"description"`
	Color        string `json:"color"`
	IsDefault    bool   `json:"is_default"`
	CreatedAt    string `json:"created_at"`
	AccountCount int    `json:"account_count,omitempty"`
	PostCount    int    `json:"post_count,omitempty"`
}

// ─── slug helper ─────────────────────────────────────────────────────

var slugTrimRE = regexp.MustCompile(`[^a-z0-9]+`)

// slugify produces a kebab-case key from a free-form name. Empty
// names → "profile". Duplicates are disambiguated by makeUniqueSlug.
func slugify(name string) string {
	s := strings.ToLower(strings.TrimSpace(name))
	s = slugTrimRE.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "profile"
	}
	if len(s) > 64 {
		s = s[:64]
	}
	return s
}

// makeUniqueSlug appends -2, -3, … until UNIQUE(project_id, slug)
// passes. Linear scan is fine — no project will have hundreds of
// near-collisions.
func makeUniqueSlug(db *sql.DB, projectID, base string) (string, error) {
	candidate := base
	for n := 1; n < 1000; n++ {
		var x int64
		err := db.QueryRow(
			`SELECT id FROM profiles WHERE project_id=? AND slug=?`,
			projectID, candidate,
		).Scan(&x)
		if err == sql.ErrNoRows {
			return candidate, nil
		}
		if err != nil {
			return "", err
		}
		n++
		candidate = fmt.Sprintf("%s-%d", base, n)
	}
	return "", errors.New("could not derive unique slug after 1000 attempts")
}

// projectDefaultProfileID returns the id of the profile flagged
// is_default=1 in this project, or 0 if the project has none yet.
// Used as the implicit fallback when a tool call doesn't pass
// `profile` / `profile_id` — keeps single-brand projects ceremony-
// free (the default profile picks itself up).
func projectDefaultProfileID(ctx *sdk.AppCtx, projectID string) int64 {
	var id int64
	_ = ctx.AppDB().QueryRow(
		`SELECT id FROM profiles WHERE project_id=? AND is_default=1 LIMIT 1`,
		projectID,
	).Scan(&id)
	return id
}

// ─── lookup helpers used by other tools ─────────────────────────────

// resolveProfileArg accepts either `profile` (slug) or `profile_id`
// (int) in args, returns a numeric id, 0 if neither is set, -1 if
// the slug doesn't resolve. -1 lets the caller distinguish "no
// scope" from "scope not found" so an explicit bad slug returns
// 404 rather than silently widening to project-wide.
func resolveProfileArg(ctx *sdk.AppCtx, projectID string, args map[string]any) int64 {
	if id := intArg(args, "profile_id", 0); id > 0 {
		return int64(id)
	}
	slug, _ := args["profile"].(string)
	slug = strings.TrimSpace(slug)
	if slug == "" {
		return 0
	}
	var id int64
	err := ctx.AppDB().QueryRow(
		`SELECT id FROM profiles WHERE project_id=? AND slug=?`,
		projectID, slug,
	).Scan(&id)
	if err == sql.ErrNoRows {
		return -1
	}
	if err != nil {
		ctx.Logger().Warn("resolveProfileArg query", "slug", slug, "err", err)
		return -1
	}
	return id
}

// loadProfile fetches one row, project-scoped. Returns nil if not
// found (or the row belongs to a different project).
func loadProfile(db *sql.DB, projectID string, id int64) (*Profile, error) {
	row := db.QueryRow(
		`SELECT id, project_id, name, slug, COALESCE(description,''), COALESCE(color,''),
		        is_default, COALESCE(created_at,'')
		   FROM profiles WHERE id=? AND project_id=?`,
		id, projectID,
	)
	var p Profile
	var isDefault int
	if err := row.Scan(&p.ID, &p.ProjectID, &p.Name, &p.Slug, &p.Description, &p.Color,
		&isDefault, &p.CreatedAt); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	p.IsDefault = isDefault == 1
	return &p, nil
}

// ─── MCP tools ───────────────────────────────────────────────────────

// profileTools returns the 5 CRUD tools registered alongside the
// existing account/post ones via a.MCPTools().
func (a *App) profileTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name: "profile_create",
			Description: "Create a profile (brand/client/site container) inside the social app. Args: name, description?, color? (#hex), is_default?. Returns the row including the auto-generated slug. The first profile in a project is automatically marked default.",
			InputSchema: schemaObject(map[string]any{
				"name":        map[string]any{"type": "string"},
				"description": map[string]any{"type": "string"},
				"color":       map[string]any{"type": "string"},
				"is_default":  map[string]any{"type": "boolean"},
			}, []string{"name"}),
			Handler: a.toolProfileCreate,
		},
		{
			Name: "profile_list",
			Description: "List profiles in the current project with their account_count + post_count. Returns [{id, slug, name, color, account_count, post_count, is_default}]. Use this when the agent prompt mentions a profile name and you need its slug/id, or to surface available brands to the operator.",
			InputSchema: schemaObject(nil, nil),
			Handler:     a.toolProfileList,
		},
		{
			Name: "profile_get",
			Description: "Fetch one profile by id or slug. Returns the row + nested accounts + recent posts. Args: id?, profile? (slug). One of the two is required.",
			InputSchema: schemaObject(map[string]any{
				"id":      map[string]any{"type": "integer"},
				"profile": map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolProfileGet,
		},
		{
			Name: "profile_update",
			Description: "Rename / recolor / promote-to-default a profile. Args: id (required), name?, description?, color?, is_default?. Setting is_default=true demotes the previous default.",
			InputSchema: schemaObject(map[string]any{
				"id":          map[string]any{"type": "integer"},
				"name":        map[string]any{"type": "string"},
				"description": map[string]any{"type": "string"},
				"color":       map[string]any{"type": "string"},
				"is_default":  map[string]any{"type": "boolean"},
			}, []string{"id"}),
			Handler: a.toolProfileUpdate,
		},
		{
			Name: "profile_delete",
			Description: "Delete a profile. Args: id (required), reassign_to? (profile_id to move accounts+posts to; defaults to 0 = unassigned). Refuses to delete the last default profile if other profiles still exist — promote a replacement first.",
			InputSchema: schemaObject(map[string]any{
				"id":           map[string]any{"type": "integer"},
				"reassign_to":  map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolProfileDelete,
		},
	}
}

func (a *App) toolProfileCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid := os.Getenv("APTEVA_PROJECT_ID")
	name, _ := args["name"].(string)
	name = strings.TrimSpace(name)
	if name == "" {
		return mcpError("name required"), nil
	}
	slug, err := makeUniqueSlug(ctx.AppDB(), pid, slugify(name))
	if err != nil {
		return nil, fmt.Errorf("derive slug: %w", err)
	}
	desc, _ := args["description"].(string)
	color, _ := args["color"].(string)
	isDefault := boolArg(args, "is_default", false)

	// First profile in a project: auto-default. Ergonomics — the
	// user shouldn't have to think about it.
	if !isDefault {
		var n int
		_ = ctx.AppDB().QueryRow(
			`SELECT COUNT(*) FROM profiles WHERE project_id=?`, pid,
		).Scan(&n)
		if n == 0 {
			isDefault = true
		}
	}

	if isDefault {
		// Promote-to-default demotes any prior default.
		_, _ = ctx.AppDB().Exec(`UPDATE profiles SET is_default=0 WHERE project_id=?`, pid)
	}
	res, err := ctx.AppDB().Exec(
		`INSERT INTO profiles (project_id, name, slug, description, color, is_default)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		pid, name, slug, desc, color, boolToInt(isDefault),
	)
	if err != nil {
		return nil, fmt.Errorf("insert profile: %w", err)
	}
	id, _ := res.LastInsertId()
	p, err := loadProfile(ctx.AppDB(), pid, id)
	if err != nil || p == nil {
		return nil, fmt.Errorf("load created: %w", err)
	}
	ctx.Emit("profile.created", map[string]any{
		"profile_id": p.ID, "slug": p.Slug, "name": p.Name,
	})
	ctx.Logger().Info("profile created",
		"id", p.ID, "slug", p.Slug, "is_default", p.IsDefault)
	return map[string]any{"profile": p}, nil
}

func (a *App) toolProfileList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid := os.Getenv("APTEVA_PROJECT_ID")
	// First pass: gather row data + close the cursor BEFORE issuing
	// the per-row count queries. Holding a Rows open while running
	// nested QueryRows deadlocks under MaxOpenConns(1) in tests
	// (same gotcha called out in dbFindExact's doc comment).
	rows, err := ctx.AppDB().Query(
		`SELECT id, project_id, name, slug, COALESCE(description,''), COALESCE(color,''),
		        is_default, COALESCE(created_at,'')
		   FROM profiles WHERE project_id=? ORDER BY is_default DESC, name`,
		pid,
	)
	if err != nil {
		return nil, err
	}
	out := make([]Profile, 0)
	for rows.Next() {
		var p Profile
		var isDefault int
		if err := rows.Scan(&p.ID, &p.ProjectID, &p.Name, &p.Slug, &p.Description, &p.Color,
			&isDefault, &p.CreatedAt); err != nil {
			continue
		}
		p.IsDefault = isDefault == 1
		out = append(out, p)
	}
	rows.Close()
	// Second pass: counts.
	for i := range out {
		_ = ctx.AppDB().QueryRow(
			`SELECT COUNT(*) FROM social_accounts WHERE profile_id=? AND status='active'`, out[i].ID,
		).Scan(&out[i].AccountCount)
		_ = ctx.AppDB().QueryRow(
			`SELECT COUNT(*) FROM posts WHERE profile_id=?`, out[i].ID,
		).Scan(&out[i].PostCount)
	}
	return map[string]any{"profiles": out}, nil
}

func (a *App) toolProfileGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid := os.Getenv("APTEVA_PROJECT_ID")
	id := int64(intArg(args, "id", 0))
	if id == 0 {
		if slug, _ := args["profile"].(string); slug != "" {
			_ = ctx.AppDB().QueryRow(
				`SELECT id FROM profiles WHERE project_id=? AND slug=?`,
				pid, slug,
			).Scan(&id)
		}
	}
	if id == 0 {
		return mcpError("id or profile (slug) required"), nil
	}
	p, err := loadProfile(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return mcpError("profile not found"), nil
	}
	// Nested accounts.
	accounts := []map[string]any{}
	rows, err := ctx.AppDB().Query(
		`SELECT id, platform, COALESCE(external_account_id,''), display_name,
		        COALESCE(avatar_url,''), status
		   FROM social_accounts
		  WHERE project_id=? AND profile_id=?
		  ORDER BY id`,
		pid, id,
	)
	if err == nil {
		for rows.Next() {
			var (
				aid                                            int64
				platform, ext, name, avatar, status            string
			)
			if err := rows.Scan(&aid, &platform, &ext, &name, &avatar, &status); err != nil {
				continue
			}
			accounts = append(accounts, map[string]any{
				"id": aid, "platform": platform, "external_account_id": ext,
				"display_name": name, "avatar_url": avatar, "status": status,
			})
		}
		rows.Close()
	}
	p.AccountCount = len(accounts)
	_ = ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM posts WHERE profile_id=?`, id,
	).Scan(&p.PostCount)
	return map[string]any{"profile": p, "accounts": accounts}, nil
}

func (a *App) toolProfileUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid := os.Getenv("APTEVA_PROJECT_ID")
	id := int64(intArg(args, "id", 0))
	if id == 0 {
		return mcpError("id required"), nil
	}
	current, err := loadProfile(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if current == nil {
		return mcpError("profile not found"), nil
	}
	sets := []string{}
	vals := []any{}
	if name, ok := args["name"].(string); ok && strings.TrimSpace(name) != "" {
		newName := strings.TrimSpace(name)
		sets = append(sets, "name=?")
		vals = append(vals, newName)
		// Re-derive slug only if the name actually changed.
		if newName != current.Name {
			newSlug, err := makeUniqueSlug(ctx.AppDB(), pid, slugify(newName))
			if err == nil && newSlug != current.Slug {
				sets = append(sets, "slug=?")
				vals = append(vals, newSlug)
			}
		}
	}
	if desc, ok := args["description"].(string); ok {
		sets = append(sets, "description=?")
		vals = append(vals, desc)
	}
	if color, ok := args["color"].(string); ok {
		sets = append(sets, "color=?")
		vals = append(vals, color)
	}
	if v, ok := args["is_default"]; ok {
		want := boolFromAny(v)
		if want {
			_, _ = ctx.AppDB().Exec(`UPDATE profiles SET is_default=0 WHERE project_id=?`, pid)
		}
		sets = append(sets, "is_default=?")
		vals = append(vals, boolToInt(want))
	}
	if len(sets) == 0 {
		return map[string]any{"profile": current}, nil
	}
	vals = append(vals, id, pid)
	_, err = ctx.AppDB().Exec(
		`UPDATE profiles SET `+strings.Join(sets, ", ")+` WHERE id=? AND project_id=?`,
		vals...,
	)
	if err != nil {
		return nil, fmt.Errorf("update profile: %w", err)
	}
	updated, _ := loadProfile(ctx.AppDB(), pid, id)
	ctx.Emit("profile.updated", map[string]any{"profile_id": id, "slug": updated.Slug})
	return map[string]any{"profile": updated}, nil
}

func (a *App) toolProfileDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid := os.Getenv("APTEVA_PROJECT_ID")
	id := int64(intArg(args, "id", 0))
	if id == 0 {
		return mcpError("id required"), nil
	}
	current, err := loadProfile(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if current == nil {
		return mcpError("profile not found"), nil
	}
	reassignTo := int64(intArg(args, "reassign_to", 0))
	if reassignTo > 0 {
		target, err := loadProfile(ctx.AppDB(), pid, reassignTo)
		if err != nil || target == nil {
			return mcpError(fmt.Sprintf("reassign_to=%d not found in this project", reassignTo)), nil
		}
	}

	// If this is the default and there are other profiles, refuse —
	// the operator should promote a replacement first to avoid
	// transient "no default" states.
	if current.IsDefault {
		var siblings int
		_ = ctx.AppDB().QueryRow(
			`SELECT COUNT(*) FROM profiles WHERE project_id=? AND id!=?`, pid, id,
		).Scan(&siblings)
		if siblings > 0 {
			return mcpError("can't delete the default profile while siblings exist — promote another with profile_update is_default=true first"), nil
		}
	}

	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(
		`UPDATE social_accounts SET profile_id=? WHERE project_id=? AND profile_id=?`,
		reassignTo, pid, id,
	); err != nil {
		return nil, fmt.Errorf("reassign accounts: %w", err)
	}
	if _, err := tx.Exec(
		`UPDATE posts SET profile_id=? WHERE project_id=? AND profile_id=?`,
		reassignTo, pid, id,
	); err != nil {
		return nil, fmt.Errorf("reassign posts: %w", err)
	}
	if _, err := tx.Exec(
		`DELETE FROM profiles WHERE id=? AND project_id=?`, id, pid,
	); err != nil {
		return nil, fmt.Errorf("delete profile: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	ctx.Emit("profile.deleted", map[string]any{
		"profile_id": id, "reassigned_to": reassignTo,
	})
	return map[string]any{"deleted": id, "reassigned_to": reassignTo}, nil
}

// ─── HTTP wrappers (panel) ───────────────────────────────────────────

// handleProfilesCollection: GET /profiles → list, POST /profiles → create.
func (a *App) handleProfilesCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		out, err := a.toolProfileList(globalCtx, map[string]any{})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, out)
	case http.MethodPost:
		var body struct {
			Name        string `json:"name"`
			Description string `json:"description"`
			Color       string `json:"color"`
			IsDefault   *bool  `json:"is_default,omitempty"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
		args := map[string]any{
			"name":        body.Name,
			"description": body.Description,
			"color":       body.Color,
		}
		if body.IsDefault != nil {
			args["is_default"] = *body.IsDefault
		}
		out, err := a.toolProfileCreate(globalCtx, args)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, out)
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

// handleProfilesItem: GET/PATCH/DELETE /profiles/:id and
// POST /profiles/:id/move (bulk-reassigns accounts to this profile).
func (a *App) handleProfilesItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/profiles/")
	if rest == "" {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	parts := strings.SplitN(rest, "/", 2)
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil {
		http.Error(w, "invalid id", http.StatusBadRequest)
		return
	}
	tail := ""
	if len(parts) == 2 {
		tail = parts[1]
	}
	switch tail {
	case "":
		switch r.Method {
		case http.MethodGet:
			out, err := a.toolProfileGet(globalCtx, map[string]any{"id": id})
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeJSON(w, out)
		case http.MethodPatch:
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
				return
			}
			body["id"] = id
			out, err := a.toolProfileUpdate(globalCtx, body)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeJSON(w, out)
		case http.MethodDelete:
			args := map[string]any{"id": id}
			if v := r.URL.Query().Get("reassign_to"); v != "" {
				if rt, err := strconv.ParseInt(v, 10, 64); err == nil {
					args["reassign_to"] = rt
				}
			}
			out, err := a.toolProfileDelete(globalCtx, args)
			if err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
			writeJSON(w, out)
		default:
			http.Error(w, "GET/PATCH/DELETE", http.StatusMethodNotAllowed)
		}
	case "move":
		// Bulk-reassign accounts (and optionally posts) to this profile.
		// Body: {account_ids: [int]}. Posts inherit via the account
		// linkage at next post_create — we don't retroactively rewrite
		// historic post.profile_id (Sprout-style M:N is out of scope).
		if r.Method != http.MethodPost {
			http.Error(w, "POST only", http.StatusMethodNotAllowed)
			return
		}
		var body struct {
			AccountIDs []int64 `json:"account_ids"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
		out, err := a.bulkAssignAccounts(globalCtx, id, body.AccountIDs)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, out)
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func (a *App) bulkAssignAccounts(ctx *sdk.AppCtx, profileID int64, accountIDs []int64) (map[string]any, error) {
	pid := os.Getenv("APTEVA_PROJECT_ID")
	target, err := loadProfile(ctx.AppDB(), pid, profileID)
	if err != nil {
		return nil, err
	}
	if target == nil {
		return nil, errors.New("target profile not found")
	}
	if len(accountIDs) == 0 {
		return map[string]any{"moved": 0}, nil
	}
	placeholders := make([]string, len(accountIDs))
	args := make([]any, 0, len(accountIDs)+2)
	args = append(args, profileID, pid)
	for i, id := range accountIDs {
		placeholders[i] = "?"
		args = append(args, id)
	}
	res, err := ctx.AppDB().Exec(
		`UPDATE social_accounts SET profile_id=?
		   WHERE project_id=? AND id IN (`+strings.Join(placeholders, ",")+`)`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	ctx.Emit("profile.accounts_moved", map[string]any{
		"profile_id": profileID,
		"count":      n,
	})
	return map[string]any{"moved": n, "profile_id": profileID}, nil
}

// ─── tiny helpers (keep main.go unchanged) ───────────────────────────

func boolArg(args map[string]any, key string, def bool) bool {
	v, ok := args[key]
	if !ok {
		return def
	}
	return boolFromAny(v)
}

func boolFromAny(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x == "true" || x == "1"
	case float64:
		return x != 0
	case int:
		return x != 0
	}
	return false
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// touch time so the linter doesn't whine if a future edit removes the
// only time consumer in this file.
var _ = time.Now
