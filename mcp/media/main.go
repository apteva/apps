// Media v0.1 — catalog + cheap derivations for storage's media files.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

// ─── Manifest (also lives in apteva.yaml) ──────────────────────────

const manifestYAML = `schema: apteva-app/v1
name: media
display_name: Media
version: 0.1.0
description: |
  Indexes audio/video/image files held by storage. Probes new
  uploads with ffprobe and generates thumbnails (video/image) or
  waveform images (audio). Read-only over storage; derivations land
  back in storage as separate files.
author: Apteva
scopes: [project, global]
requires:
  permissions:
    - db.write.app
  apps:
    - name: storage
      version: ">=0.1.0"
      reason: reads source bytes; writes thumbnails + waveforms back as derivations
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: media_get,            description: "Fetch one media record by storage file_id." }
    - { name: media_search,         description: "Filter by duration / dimensions / codec / has_video / has_audio." }
    - { name: media_get_thumbnail,  description: "Get the thumbnail derivation pointer (storage file_id) — generates if missing." }
    - { name: media_get_waveform,   description: "Get the waveform derivation pointer (audio only)." }
  workers:
    - name: indexer
      schedule: "@every 30s"
  ui_panels:
    - slot: project.page
      label: Media
      icon: video
      entry: /ui/MediaPanel.mjs
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/media
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/media.db
  migrations: migrations/
upgrade_policy: auto-patch
`

// globalCtx — set in OnMount so HTTP handlers can read AppDB() +
// logger without threading the ctx through every layer.
var globalCtx *sdk.AppCtx

type App struct{}

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("media requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("media mounted",
		"scope_project_id", os.Getenv("APTEVA_PROJECT_ID"),
		"gateway", os.Getenv("APTEVA_GATEWAY_URL"),
	)
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) Workers() []sdk.Worker {
	// poll_interval_seconds drives the indexer cadence. Read straight
	// from APTEVA_APP_CONFIG (set at process boot) since the SDK
	// captures the schedule string before OnMount sets ctx.
	interval := readConfigInt("poll_interval_seconds", 30)
	if interval < 1 {
		interval = 1
	}
	return []sdk.Worker{
		{
			Name:     "indexer",
			Schedule: fmt.Sprintf("@every %ds", interval),
			Run:      runIndexer,
		},
	}
}

// readConfigInt parses APTEVA_APP_CONFIG (a JSON object the platform
// sets at spawn time) for an int field. Falls back to def when the
// var is missing, the JSON is malformed, or the field isn't there.
func readConfigInt(name string, def int) int {
	raw := os.Getenv("APTEVA_APP_CONFIG")
	if raw == "" {
		return def
	}
	var cfg map[string]string
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		return def
	}
	v, ok := cfg[name]
	if !ok || v == "" {
		return def
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return def
	}
	return n
}

// ─── HTTP routes ────────────────────────────────────────────────────

func (a *App) HTTPRoutes() []sdk.Route {
	// The SDK auto-mounts /ui/ from the ./ui directory. We just add
	// the data routes here.
	return []sdk.Route{
		{Pattern: "/media", Handler: a.handleMediaCollection},
		{Pattern: "/media/", Handler: a.handleMediaItem},
		{Pattern: "/status", Handler: a.handleStatus},
		{Pattern: "/reindex", Handler: a.handleReindex},
	}
}

// ─── MCP tools ─────────────────────────────────────────────────────

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "media_get",
			Description: "Fetch one media record by storage file_id. Returns probe data + derivation pointers.",
			InputSchema: schemaObject(map[string]any{"file_id": map[string]any{"type": "string"}}, []string{"file_id"}),
			Handler:     a.toolGet,
		},
		{
			Name:        "media_search",
			Description: "Filter the catalog. Args: duration_min_ms, duration_max_ms, has_video, has_audio, is_image, width_min, width_max, video_codec, audio_codec, limit, order_by ('duration_ms'|'created_at'|'updated_at').",
			InputSchema: schemaObject(map[string]any{
				"duration_min_ms": map[string]any{"type": "integer"},
				"duration_max_ms": map[string]any{"type": "integer"},
				"has_video":       map[string]any{"type": "boolean"},
				"has_audio":       map[string]any{"type": "boolean"},
				"is_image":        map[string]any{"type": "boolean"},
				"width_min":       map[string]any{"type": "integer"},
				"width_max":       map[string]any{"type": "integer"},
				"video_codec":     map[string]any{"type": "string"},
				"audio_codec":     map[string]any{"type": "string"},
				"limit":           map[string]any{"type": "integer"},
				"order_by":        map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolSearch,
		},
		{
			Name:        "media_get_thumbnail",
			Description: "Return the storage file_id (and pointer URL on the storage app) of the cached thumbnail. If missing or stale, kicks off generation. Args: file_id.",
			InputSchema: schemaObject(map[string]any{"file_id": map[string]any{"type": "string"}}, []string{"file_id"}),
			Handler:     a.toolGetDerivation("thumbnail"),
		},
		{
			Name:        "media_get_waveform",
			Description: "Return the storage file_id of the cached waveform PNG. Args: file_id.",
			InputSchema: schemaObject(map[string]any{"file_id": map[string]any{"type": "string"}}, []string{"file_id"}),
			Handler:     a.toolGetDerivation("waveform"),
		},
	}
}

func main() { sdk.Run(&App{}) }

// ─── Project resolution (mirrors storage) ──────────────────────────

func resolveProjectFromArgs(args map[string]any) (string, error) {
	if env := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); env != "" {
		return env, nil
	}
	if v, ok := args["_project_id"].(string); ok && v != "" {
		return v, nil
	}
	return "", errors.New("project_id missing — pass _project_id when scope=global")
}

func resolveProjectFromRequest(r *http.Request) (string, error) {
	if env := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); env != "" {
		return env, nil
	}
	if v := r.URL.Query().Get("project_id"); v != "" {
		return v, nil
	}
	return "", errors.New("project_id required in query string when install scope=global")
}

// ─── Tool handlers ─────────────────────────────────────────────────

func (a *App) toolGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	fid, _ := args["file_id"].(string)
	if fid == "" {
		return nil, errors.New("file_id required")
	}
	m, err := getMedia(ctx.AppDB(), pid, fid)
	if err != nil {
		if notFound(err) {
			return map[string]any{"found": false}, nil
		}
		return nil, err
	}
	return map[string]any{"found": true, "media": m}, nil
}

func (a *App) toolSearch(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	f := SearchFilters{}
	f.DurationMinMs = int64Arg(args["duration_min_ms"])
	f.DurationMaxMs = int64Arg(args["duration_max_ms"])
	if v, ok := args["has_video"].(bool); ok {
		f.HasVideo = &v
	}
	if v, ok := args["has_audio"].(bool); ok {
		f.HasAudio = &v
	}
	if v, ok := args["is_image"].(bool); ok {
		f.IsImage = &v
	}
	f.WidthMin = int(int64Arg(args["width_min"]))
	f.WidthMax = int(int64Arg(args["width_max"]))
	f.VideoCodec, _ = args["video_codec"].(string)
	f.AudioCodec, _ = args["audio_codec"].(string)
	f.Limit = int(int64Arg(args["limit"]))
	f.OrderBy, _ = args["order_by"].(string)
	rows, err := searchMedia(ctx.AppDB(), pid, f)
	if err != nil {
		return nil, err
	}
	return map[string]any{"media": rows}, nil
}

// toolGetDerivation closes over the derivation kind so the same body
// works for thumbnail + waveform without copy-paste.
func (a *App) toolGetDerivation(kind string) sdk.ToolHandler {
	return func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
		pid, err := resolveProjectFromArgs(args)
		if err != nil {
			return nil, err
		}
		fid, _ := args["file_id"].(string)
		if fid == "" {
			return nil, errors.New("file_id required")
		}
		ds, err := listDerivations(ctx.AppDB(), pid, fid)
		if err != nil {
			return nil, err
		}
		for _, d := range ds {
			if d.Kind == kind && d.Status == "ok" {
				return map[string]any{
					"found":           true,
					"derivation":      d,
					"storage_file_id": d.StorageFileID,
				}, nil
			}
		}
		return map[string]any{"found": false, "kind": kind}, nil
	}
}

// ─── HTTP handlers ─────────────────────────────────────────────────

func (a *App) handleMediaCollection(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	q := r.URL.Query()
	f := SearchFilters{}
	f.DurationMinMs, _ = strconv.ParseInt(q.Get("duration_min_ms"), 10, 64)
	f.DurationMaxMs, _ = strconv.ParseInt(q.Get("duration_max_ms"), 10, 64)
	if v := q.Get("has_video"); v != "" {
		b := v == "true"
		f.HasVideo = &b
	}
	if v := q.Get("has_audio"); v != "" {
		b := v == "true"
		f.HasAudio = &b
	}
	if v := q.Get("is_image"); v != "" {
		b := v == "true"
		f.IsImage = &b
	}
	if v := q.Get("limit"); v != "" {
		f.Limit, _ = strconv.Atoi(v)
	}
	f.OrderBy = q.Get("order_by")
	rows, err := searchMedia(globalCtx.AppDB(), pid, f)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"media": rows})
}

func (a *App) handleMediaItem(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/media/")
	parts := strings.SplitN(rest, "/", 2)
	fid := parts[0]
	if fid == "" {
		http.Error(w, "file_id required", http.StatusBadRequest)
		return
	}
	tail := ""
	if len(parts) == 2 {
		tail = parts[1]
	}
	switch {
	case tail == "" && r.Method == http.MethodGet:
		m, err := getMedia(globalCtx.AppDB(), pid, fid)
		if err != nil {
			if notFound(err) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, m)
	case tail == "reindex" && r.Method == http.MethodPost:
		_, err := globalCtx.AppDB().Exec(
			`UPDATE media SET probe_status='pending', probe_error='', source_sha256='' WHERE project_id=? AND file_id=?`,
			pid, fid,
		)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"queued": 1})
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

// handleStatus returns probe-status counts. Dashboard footer uses
// it; agents don't — they query results, not ops state.
func (a *App) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rows, err := globalCtx.AppDB().Query(
		`SELECT probe_status, COUNT(*) FROM media WHERE project_id=? GROUP BY probe_status`, pid,
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	defer rows.Close()
	out := map[string]int64{}
	for rows.Next() {
		var status string
		var n int64
		if err := rows.Scan(&status, &n); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		out[status] = n
	}
	writeJSON(w, out)
}

// handleReindex flips one file or all failed rows back to pending so
// the next worker tick re-probes them. Dashboard panel's "retry
// failed" button hits this; same with the per-row re-index button.
func (a *App) handleReindex(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	q := r.URL.Query()
	if fid := q.Get("file_id"); fid != "" {
		_, err := globalCtx.AppDB().Exec(
			`UPDATE media SET probe_status='pending', probe_error='', source_sha256='' WHERE project_id=? AND file_id=?`,
			pid, fid,
		)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"queued": 1, "file_id": fid})
		return
	}
	if q.Get("failed_only") == "true" {
		res, err := globalCtx.AppDB().Exec(
			`UPDATE media SET probe_status='pending', probe_error='' WHERE project_id=? AND probe_status IN ('failed','unsupported','skipped_size')`,
			pid,
		)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		n, _ := res.RowsAffected()
		writeJSON(w, map[string]any{"queued": n})
		return
	}
	http.Error(w, "provide file_id or failed_only=true", http.StatusBadRequest)
}

// ─── helpers ───────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(v)
}

func schemaObject(props map[string]any, required []string) map[string]any {
	o := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		o["required"] = required
	}
	return o
}

func int64Arg(v any) int64 {
	switch x := v.(type) {
	case nil:
		return 0
	case int64:
		return x
	case int:
		return int64(x)
	case float64:
		return int64(x)
	case string:
		n, err := strconv.ParseInt(x, 10, 64)
		if err != nil {
			return 0
		}
		return n
	}
	return 0
}

