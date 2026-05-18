// Screenshots v0.1 — capture/store/replay browser screenshots.
//
// This sidecar does not own a browser; it CallApp's into the
// `computer` app for the session lifecycle (browser_open /
// browser_screenshot / browser_close) and into `storage` for
// persistence. The only state it owns is a registry table mapping
// a local screenshot_id to {storage_id, url, label, captured_at,
// idempotency_key, project_id}.
//
// Idempotency model: callers that pass idempotency_key get
// deduplication across the most-recent 10 minutes. Without a key,
// every capture opens a fresh browser session — same URL twice in
// a row is two intentional shots (and the cost is on the caller).
//
// Project context: scopes are [project, global]. The handlers inject
// `_project_id` on every cross-app call via withProjectID so global
// installs hit the right storage / computer install per the
// platform's routing rules.
package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"

	_ "modernc.org/sqlite"
)

// ─── Manifest (also lives in apteva.yaml) ──────────────────────────

const manifestYAML = `schema: apteva-app/v1
name: screenshots
display_name: Screenshots
version: 0.1.0
description: |
  Capture browser screenshots from a URL, save them to storage, and
  browse them in a gallery. v0.1: URL-driven capture only.
scopes: [project, global]
requires:
  permissions:
    - db.write.app
    - platform.apps.call
  apps:
    - name: computer
      version: ">=0.3.0"
    - name: storage
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - name: screenshot_capture
      description: "Capture a screenshot of a URL. Returns {screenshot_id, storage_id, url, captured_at, label}."
    - name: screenshot_list
      description: "List captures, newest first."
    - name: screenshot_get
      description: "Fetch one capture by id, with a fresh signed url."
    - name: screenshot_delete
      description: "Soft-delete a capture and remove the underlying storage blob."
  ui_panels:
    - slot: project.page
      label: Screenshots
      icon: camera
      entry: /ui/GalleryPanel.mjs
  ui_components:
    - name: screenshot-card
      entry: /ui/ScreenshotCard.mjs
      slots: [chat.message_attachment]
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/screenshots
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/screenshots.db
  migrations: migrations/
`

// idempotencyWindow — how far back screenshot_capture looks for an
// existing row with the same idempotency_key. Short enough that the
// caller's "is this still the same capture I meant?" intuition holds;
// long enough to deduplicate a quick retry after a transient failure.
const idempotencyWindow = 10 * time.Minute

// defaultListLimit, maxListLimit — bounds for screenshot_list. Max is
// the ceiling regardless of what the caller asked for; the gallery
// pages with `since` cursors past that.
const (
	defaultListLimit = 50
	maxListLimit     = 200
)

// ─── App ───────────────────────────────────────────────────────────

type App struct{}

// globalCtx is the AppCtx captured at OnMount. HTTP handlers need an
// AppCtx (for DB, PlatformAPI, Logger) and the framework hands them
// only a stdlib http.ResponseWriter/*http.Request. This is the same
// pattern storage and other sidecars use; tests bypass it by calling
// tool handlers directly with an explicit ctx.
var globalCtx *sdk.AppCtx

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("screenshots requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("screenshots mounted", "tools", 4, "idempotency_window", idempotencyWindow.String())
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }
// HTTPRoutes — small JSON API the GalleryPanel calls. We deliberately
// keep it thin: each handler builds the args map and delegates to the
// matching MCP tool function. That keeps the contract single-sourced.
// All routes inherit the SDK's token-auth gate (we don't set NoAuth).
func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Method: http.MethodGet, Pattern: "/api/screenshots", Handler: a.handleList},
		{Method: http.MethodPost, Pattern: "/api/screenshots", Handler: a.handleCapture},
		{Method: http.MethodGet, Pattern: "/api/screenshots/{id}", Handler: a.handleGet},
		{Method: http.MethodDelete, Pattern: "/api/screenshots/{id}", Handler: a.handleDelete},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name: "screenshot_capture",
			Description: "Capture a screenshot of a URL. Args: url (required), backend?, viewport?, label?, " +
				"idempotency_key?. Returns {screenshot_id, storage_id, url, captured_at, label}. " +
				"Same idempotency_key within 10 min returns the existing record.",
			InputSchema: schemaObject(map[string]any{
				"url":     map[string]any{"type": "string"},
				"backend": map[string]any{"type": "string", "enum": []string{"local", "browserbase", "steel"}},
				"viewport": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"width":  map[string]any{"type": "integer"},
						"height": map[string]any{"type": "integer"},
					},
				},
				"label":           map[string]any{"type": "string"},
				"idempotency_key": map[string]any{"type": "string"},
			}, []string{"url"}),
			Handler: a.toolCapture,
		},
		{
			Name:        "screenshot_list",
			Description: "List captures, newest first. Args: limit? (default 50, max 200), since? (ISO), url_contains?, label_contains?.",
			InputSchema: schemaObject(map[string]any{
				"limit":          map[string]any{"type": "integer"},
				"since":          map[string]any{"type": "string"},
				"url_contains":   map[string]any{"type": "string"},
				"label_contains": map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolList,
		},
		{
			Name:        "screenshot_get",
			Description: "Fetch one capture by id with a fresh signed url. Args: screenshot_id.",
			InputSchema: schemaObject(map[string]any{
				"screenshot_id": map[string]any{"type": "integer"},
			}, []string{"screenshot_id"}),
			Handler: a.toolGet,
		},
		{
			Name:        "screenshot_delete",
			Description: "Soft-delete and drop the storage blob. Idempotent. Args: screenshot_id.",
			InputSchema: schemaObject(map[string]any{
				"screenshot_id": map[string]any{"type": "integer"},
			}, []string{"screenshot_id"}),
			Handler: a.toolDelete,
		},
	}
}

// ─── Domain row ────────────────────────────────────────────────────

type screenshot struct {
	ID             int64
	StorageID      int64
	URL            string
	FinalURL       string
	Width          int
	Height         int
	Backend        string
	Label          string
	IdempotencyKey string
	ProjectID      string
	CapturedAt     time.Time
}

// ─── Tool handlers ─────────────────────────────────────────────────

func (a *App) toolCapture(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	url := stringArg(args, "url")
	if url == "" {
		return nil, errors.New("url required")
	}
	key := stringArg(args, "idempotency_key")
	label := stringArg(args, "label")

	// Idempotency replay — short-circuit if a recent row with this
	// key exists. Cheaper to query than to open Chrome.
	if key != "" {
		if existing, err := findRecentByIdempotency(ctx, key, idempotencyWindow); err != nil {
			return nil, fmt.Errorf("idempotency lookup: %w", err)
		} else if existing != nil {
			ctx.Logger().Info("screenshot_capture idempotency hit", "screenshot_id", existing.ID, "key", key)
			return captureResult(ctx, existing)
		}
	}

	// 1. Open a session via computer.
	var openRes struct {
		SessionID  string `json:"session_id"`
		Backend    string `json:"backend"`
		CurrentURL string `json:"current_url"`
		Width      int    `json:"width"`
		Height     int    `json:"height"`
	}
	openArgs := map[string]any{"url": url}
	if b := stringArg(args, "backend"); b != "" {
		openArgs["backend"] = b
	}
	if vp, ok := args["viewport"].(map[string]any); ok {
		openArgs["viewport"] = vp
	}
	if err := ctx.PlatformAPI().CallAppResult("computer", "browser_open", withProjectID(ctx, openArgs), &openRes); err != nil {
		return nil, fmt.Errorf("computer.browser_open: %w", err)
	}

	// Deferred close fires on every exit path. Logged-only on error —
	// the session is the computer app's 30-min reaper's problem if
	// our close call drops.
	defer func() {
		closeArgs := withProjectID(ctx, map[string]any{"session_id": openRes.SessionID})
		var closeRes struct{}
		if err := ctx.PlatformAPI().CallAppResult("computer", "browser_close", closeArgs, &closeRes); err != nil {
			ctx.Logger().Warn("computer.browser_close failed (reaper will clean up)", "session_id", openRes.SessionID, "err", err.Error())
		}
	}()

	// 2. Screenshot.
	var shotRes struct {
		PNGB64     string `json:"png_b64"`
		CurrentURL string `json:"current_url"`
		Width      int    `json:"width"`
		Height     int    `json:"height"`
	}
	shotArgs := withProjectID(ctx, map[string]any{"session_id": openRes.SessionID})
	if err := ctx.PlatformAPI().CallAppResult("computer", "browser_screenshot", shotArgs, &shotRes); err != nil {
		return nil, fmt.Errorf("computer.browser_screenshot: %w", err)
	}

	// 3. Upload to storage under /.screenshots/<yyyy-mm>/ — dotted folder
	// per the storage-conventions skill.
	folder := "/.screenshots/" + time.Now().UTC().Format("2006-01")
	var upRes struct {
		ID  int64  `json:"id"`
		URL string `json:"url"`
	}
	upArgs := withProjectID(ctx, map[string]any{
		"name":           randName() + ".png",
		"content_base64": shotRes.PNGB64,
		"folder":         folder,
		"content_type":   "image/png",
		"source":         "screenshots:capture",
	})
	if err := ctx.PlatformAPI().CallAppResult("storage", "files_upload", upArgs, &upRes); err != nil {
		return nil, fmt.Errorf("storage.files_upload: %w", err)
	}

	// 4. Insert registry row.
	now := time.Now().UTC()
	row := &screenshot{
		StorageID:      upRes.ID,
		URL:            url,
		FinalURL:       firstNonEmpty(shotRes.CurrentURL, openRes.CurrentURL, url),
		Width:          shotRes.Width,
		Height:         shotRes.Height,
		Backend:        openRes.Backend,
		Label:          label,
		IdempotencyKey: key,
		ProjectID:      ctx.CurrentProject(),
		CapturedAt:     now,
	}
	id, err := insertScreenshot(ctx, row)
	if err != nil {
		return nil, fmt.Errorf("registry insert: %w", err)
	}
	row.ID = id

	ctx.Logger().Info("screenshot captured", "screenshot_id", id, "storage_id", row.StorageID, "backend", row.Backend)
	return map[string]any{
		"screenshot_id": id,
		"storage_id":    row.StorageID,
		"url":           upRes.URL,
		"captured_at":   row.CapturedAt.Format(time.RFC3339),
		"label":         label,
	}, nil
}

func (a *App) toolList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	limit := intArg(args, "limit")
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}
	rows, err := listScreenshots(ctx, listFilter{
		Limit:         limit,
		Since:         parseTimeArg(stringArg(args, "since")),
		URLContains:   stringArg(args, "url_contains"),
		LabelContains: stringArg(args, "label_contains"),
		ProjectID:     ctx.CurrentProject(),
	})
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, map[string]any{
			"id":          r.ID,
			"url":         r.URL,
			"final_url":   r.FinalURL,
			"label":       r.Label,
			"width":       r.Width,
			"height":      r.Height,
			"backend":     r.Backend,
			"storage_id":  r.StorageID,
			"captured_at": r.CapturedAt.Format(time.RFC3339),
		})
	}
	return map[string]any{"screenshots": out}, nil
}

func (a *App) toolGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64(intArg(args, "screenshot_id"))
	if id <= 0 {
		return nil, errors.New("screenshot_id required")
	}
	row, err := getScreenshot(ctx, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return nil, fmt.Errorf("screenshot %d not found", id)
	}
	return captureResult(ctx, row)
}

func (a *App) toolDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64(intArg(args, "screenshot_id"))
	if id <= 0 {
		return nil, errors.New("screenshot_id required")
	}
	row, err := getScreenshot(ctx, id)
	if err != nil {
		return nil, err
	}
	if row == nil {
		return map[string]any{"deleted": false}, nil
	}

	// Drop the storage blob first. If it fails, we still soft-delete
	// the row — the storage row is the bytes-of-record, but a dangling
	// registry row is worse UX than a leaked blob (the gallery would
	// keep showing it with a broken thumbnail).
	delArgs := withProjectID(ctx, map[string]any{"id": row.StorageID})
	var delRes struct{}
	if err := ctx.PlatformAPI().CallAppResult("storage", "files_delete", delArgs, &delRes); err != nil {
		ctx.Logger().Warn("storage.files_delete failed; soft-deleting registry row anyway",
			"screenshot_id", id, "storage_id", row.StorageID, "err", err.Error())
	}

	if err := softDeleteScreenshot(ctx, id); err != nil {
		return nil, err
	}
	return map[string]any{"deleted": true}, nil
}

// captureResult mints the response shape both toolCapture (cache hit)
// and toolGet return: the registry fields plus a fresh signed URL
// from storage.
func captureResult(ctx *sdk.AppCtx, row *screenshot) (any, error) {
	var urlRes struct {
		URL string `json:"url"`
	}
	urlArgs := withProjectID(ctx, map[string]any{"id": row.StorageID})
	if err := ctx.PlatformAPI().CallAppResult("storage", "files_get_url", urlArgs, &urlRes); err != nil {
		return nil, fmt.Errorf("storage.files_get_url: %w", err)
	}
	return map[string]any{
		"screenshot_id": row.ID,
		"storage_id":    row.StorageID,
		"url":           urlRes.URL,
		"captured_at":   row.CapturedAt.Format(time.RFC3339),
		"label":         row.Label,
		"final_url":     row.FinalURL,
		"width":         row.Width,
		"height":        row.Height,
		"backend":       row.Backend,
	}, nil
}

// ─── DB ops ────────────────────────────────────────────────────────

func insertScreenshot(ctx *sdk.AppCtx, r *screenshot) (int64, error) {
	res, err := ctx.AppDB().Exec(
		`INSERT INTO screenshots (storage_id, url, final_url, width, height, backend, label, idempotency_key, project_id, captured_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?)`,
		r.StorageID, r.URL, r.FinalURL, r.Width, r.Height, r.Backend, nullIfEmpty(r.Label),
		nullIfEmpty(r.IdempotencyKey), nullIfEmpty(r.ProjectID), r.CapturedAt,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func findRecentByIdempotency(ctx *sdk.AppCtx, key string, window time.Duration) (*screenshot, error) {
	row := ctx.AppDB().QueryRow(
		`SELECT id, storage_id, url, final_url, width, height, backend, COALESCE(label,''), COALESCE(idempotency_key,''), COALESCE(project_id,''), captured_at
		 FROM screenshots
		 WHERE idempotency_key = ? AND captured_at >= ? AND deleted_at IS NULL
		 ORDER BY captured_at DESC
		 LIMIT 1`,
		key, time.Now().UTC().Add(-window),
	)
	return scanScreenshot(row)
}

func getScreenshot(ctx *sdk.AppCtx, id int64) (*screenshot, error) {
	row := ctx.AppDB().QueryRow(
		`SELECT id, storage_id, url, final_url, width, height, backend, COALESCE(label,''), COALESCE(idempotency_key,''), COALESCE(project_id,''), captured_at
		 FROM screenshots
		 WHERE id = ? AND deleted_at IS NULL`,
		id,
	)
	return scanScreenshot(row)
}

func softDeleteScreenshot(ctx *sdk.AppCtx, id int64) error {
	_, err := ctx.AppDB().Exec(
		`UPDATE screenshots SET deleted_at = ? WHERE id = ? AND deleted_at IS NULL`,
		time.Now().UTC(), id,
	)
	return err
}

type listFilter struct {
	Limit         int
	Since         time.Time
	URLContains   string
	LabelContains string
	ProjectID     string
}

func listScreenshots(ctx *sdk.AppCtx, f listFilter) ([]*screenshot, error) {
	var (
		conds []string
		args  []any
	)
	conds = append(conds, "deleted_at IS NULL")
	if !f.Since.IsZero() {
		conds = append(conds, "captured_at >= ?")
		args = append(args, f.Since)
	}
	if f.URLContains != "" {
		conds = append(conds, "(url LIKE ? OR final_url LIKE ?)")
		like := "%" + f.URLContains + "%"
		args = append(args, like, like)
	}
	if f.LabelContains != "" {
		conds = append(conds, "label LIKE ?")
		args = append(args, "%"+f.LabelContains+"%")
	}
	if f.ProjectID != "" {
		// project-scoped install: only show this project's rows.
		// Global installs that haven't pinned a project see everything.
		conds = append(conds, "project_id = ?")
		args = append(args, f.ProjectID)
	}
	q := `SELECT id, storage_id, url, final_url, width, height, backend, COALESCE(label,''), COALESCE(idempotency_key,''), COALESCE(project_id,''), captured_at
	      FROM screenshots
	      WHERE ` + strings.Join(conds, " AND ") + `
	      ORDER BY captured_at DESC LIMIT ?`
	args = append(args, f.Limit)

	rows, err := ctx.AppDB().Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var out []*screenshot
	for rows.Next() {
		s, err := scanScreenshotRows(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

func scanScreenshot(row *sql.Row) (*screenshot, error) {
	var s screenshot
	if err := row.Scan(&s.ID, &s.StorageID, &s.URL, &s.FinalURL, &s.Width, &s.Height, &s.Backend, &s.Label, &s.IdempotencyKey, &s.ProjectID, &s.CapturedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &s, nil
}

func scanScreenshotRows(rows *sql.Rows) (*screenshot, error) {
	var s screenshot
	if err := rows.Scan(&s.ID, &s.StorageID, &s.URL, &s.FinalURL, &s.Width, &s.Height, &s.Backend, &s.Label, &s.IdempotencyKey, &s.ProjectID, &s.CapturedAt); err != nil {
		return nil, err
	}
	return &s, nil
}

// ─── Helpers ───────────────────────────────────────────────────────

// withProjectID injects _project_id when this install is project-scoped
// or has WithProject'd the call. The platform's cross-app router uses
// it to disambiguate between multiple installs of the same target app
// (one per project on a global server). Calls from a project-scoped
// install have CurrentProject() set already; global installs that
// haven't pinned a project leave it empty and the platform falls back
// to the single-install default — same shape image-studio uses.
func withProjectID(ctx *sdk.AppCtx, args map[string]any) map[string]any {
	pid := ctx.CurrentProject()
	if pid == "" {
		return args
	}
	if _, present := args["_project_id"]; present {
		return args
	}
	out := make(map[string]any, len(args)+1)
	for k, v := range args {
		out[k] = v
	}
	out["_project_id"] = pid
	return out
}

func randName() string {
	var b [8]byte
	_, _ = rand.Read(b[:])
	return hex.EncodeToString(b[:])
}

func parseTimeArg(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

func firstNonEmpty(s ...string) string {
	for _, v := range s {
		if v != "" {
			return v
		}
	}
	return ""
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func stringArg(args map[string]any, k string) string {
	if v, ok := args[k].(string); ok {
		return v
	}
	return ""
}

func intArg(args map[string]any, k string) int {
	switch v := args[k].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	}
	return 0
}

func schemaObject(props map[string]any, required []string) map[string]any {
	out := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

// ─── HTTP handlers (thin wrappers around the MCP tool functions) ───

func (a *App) handleList(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	args := map[string]any{}
	if v := q.Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			args["limit"] = n
		}
	}
	if v := q.Get("since"); v != "" {
		args["since"] = v
	}
	if v := q.Get("url_contains"); v != "" {
		args["url_contains"] = v
	}
	if v := q.Get("label_contains"); v != "" {
		args["label_contains"] = v
	}
	out, err := a.toolList(globalCtx, args)
	writeJSON(w, out, err)
}

func (a *App) handleCapture(w http.ResponseWriter, r *http.Request) {
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil && err.Error() != "EOF" {
		httpErr(w, http.StatusBadRequest, "bad JSON body: "+err.Error())
		return
	}
	if body == nil {
		body = map[string]any{}
	}
	out, err := a.toolCapture(globalCtx, body)
	writeJSON(w, out, err)
}

func (a *App) handleGet(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "/api/screenshots/")
	if !ok {
		httpErr(w, http.StatusBadRequest, "bad id")
		return
	}
	out, err := a.toolGet(globalCtx, map[string]any{"screenshot_id": id})
	writeJSON(w, out, err)
}

func (a *App) handleDelete(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r, "/api/screenshots/")
	if !ok {
		httpErr(w, http.StatusBadRequest, "bad id")
		return
	}
	out, err := a.toolDelete(globalCtx, map[string]any{"screenshot_id": id})
	writeJSON(w, out, err)
}

func pathID(r *http.Request, prefix string) (int, bool) {
	rest := strings.TrimPrefix(r.URL.Path, prefix)
	rest = strings.TrimSuffix(rest, "/")
	if rest == "" {
		return 0, false
	}
	n, err := strconv.Atoi(rest)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

func writeJSON(w http.ResponseWriter, payload any, err error) {
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func main() { sdk.Run(&App{}) }
