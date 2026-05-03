// Media v0.2 — catalog + cheap derivations + parameterised renders
// over storage's media files.
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
version: 0.5.5
description: |
  Catalog + derivations + renders + transcripts + auto-descriptions
  for media files in storage. Indexes uploads (probe, thumbnail,
  waveform), runs on-demand edits (trim/resize/transcode/concat/
  crop/extract_frame/audio_extract), auto-transcribes audio + video
  via Deepgram, and auto-generates descriptions via OpenCode Go
  (Kimi K2.6 default — vision-capable) when integrations are bound.
  Outputs all flow through storage.
author: Apteva
scopes: [project, global]
requires:
  permissions:
    - db.write.app
    - net.egress
    - platform.connections.execute
  apps:
    - name: storage
      version: ">=0.1.0"
      reason: reads source bytes; writes thumbnails, waveforms, and render outputs back to storage
    - name: jobs
      version: ">=0.1.0"
      optional: true
      reason: optional — schedule recurring or delayed renders against media's HTTP routes
  integrations:
    - role: transcripts
      kind: integration
      compatible_slugs: [deepgram]
      capabilities: [audio.transcribe]
      tools:
        audio.transcribe: listen
        transcribe: listen
      required: false
      label: "Speech-to-text provider"
      hint: "Connect Deepgram to auto-transcribe audio + video. Without it, transcripts stay manual (media_set_transcript)."
    - role: descriptions
      kind: integration
      compatible_slugs: [opencode-go]
      capabilities: [chat.complete, vision.describe]
      tools:
        chat.complete: chat_completion
        vision.describe: chat_completion
      required: false
      label: "Auto-description provider"
      hint: "Connect OpenCode Go to auto-generate descriptions from thumbnails + transcripts. Default model: kimi-k2.6 (vision-capable). Without it, descriptions stay manual."
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: media_get,             description: "Fetch one media record by storage file_id." }
    - { name: media_search,          description: "Filter by duration / dimensions / codec / has_video / has_audio." }
    - { name: media_get_thumbnail,   description: "Get the thumbnail derivation pointer (storage file_id) — generates if missing." }
    - { name: media_get_waveform,    description: "Get the waveform derivation pointer (audio only)." }
    - { name: media_reindex,         description: "Force a re-probe + re-derive — for one file_id or all failed rows." }
    - { name: media_index_status,    description: "Counts of pending / ok / failed / unsupported / skipped_size." }
    - { name: media_trim,            description: "Cut a clip from a video/audio source. Returns render_id." }
    - { name: media_resize,          description: "Scale a video/image to new dimensions. Returns render_id." }
    - { name: media_transcode,       description: "Re-encode to a new container/codec. Returns render_id." }
    - { name: media_concat,          description: "Join multiple sources end-to-end. Returns render_id." }
    - { name: media_crop,            description: "Crop a video or image to a rectangular region. Returns render_id." }
    - { name: media_extract_frame,   description: "Save a single frame at a specific timestamp as PNG. Returns render_id." }
    - { name: media_audio_extract,   description: "Strip audio from a video into a standalone file. Returns render_id." }
    - { name: media_get_render,      description: "Status of one render — progress + output_file_id when ready." }
    - { name: media_list_renders,    description: "List renders filtered by status / operation." }
    - { name: media_cancel_render,   description: "Cancel a pending or running render. Idempotent." }
    - { name: media_set_description, description: "Set title / description / alt_text on a media row. Partial update; omitted fields preserved." }
    - { name: media_transcribe,      description: "Queue a transcription for one media file. Returns transcript_id; poll media_get_transcript." }
    - { name: media_get_transcript,  description: "Status + text + segments of one file's transcript." }
    - { name: media_set_transcript,  description: "Upsert an externally-produced transcript (imported / manual). Skips the auto pipeline." }
    - { name: media_describe,        description: "Queue an auto-generated description for one media file. force=true reattempts even after success / cooldown." }
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
	// Render pool runs alongside the indexer worker. Pool size is
	// independent: the indexer is a single scheduled tick, the pool
	// is N hot goroutines.
	poolSize := readConfigInt("render_pool_size", 2)
	startRenderPool(ctx, poolSize)
	// Auto-transcriber: separate goroutine, isolated from indexer +
	// render pool. Skips itself if transcribe_auto=false; degrades
	// gracefully when the deepgram integration isn't bound.
	startTranscriber(ctx)
	// Auto-describer: another isolated goroutine. Reads transcripts
	// + thumbnails when present, calls opencode-go (Kimi K2.6 by
	// default), writes the description back via setDescription.
	startDescriber(ctx)
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
		// Renders. /renders accepts POST {operation, ...} for
		// jobs-app-style scheduled triggers; GET lists. /renders/{id}
		// supports GET (status) + DELETE (cancel).
		{Pattern: "/renders", Handler: a.handleRendersCollection},
		{Pattern: "/renders/", Handler: a.handleRenderItem},
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
		{
			Name:        "media_reindex",
			Description: "Force a re-probe + re-derive. Pass file_id to re-index one row, or failed_only=true to retry every failed/unsupported row in the project.",
			InputSchema: schemaObject(map[string]any{
				"file_id":     map[string]any{"type": "string"},
				"failed_only": map[string]any{"type": "boolean"},
			}, nil),
			Handler: a.toolReindex,
		},
		{
			Name:        "media_index_status",
			Description: "Counts of pending / ok / failed / unsupported / skipped_size for the catalog.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Handler:     a.toolIndexStatus,
		},
		// ─── Render submit tools ────────────────────────────────────
		// Each builds a render row; the worker pool picks it up
		// asynchronously. Callers poll media_get_render for status.
		{
			Name:        "media_trim",
			Description: "Cut a clip from a video/audio file. Args: file_id (string), start_ms, end_ms (int), output_name (string, optional).",
			InputSchema: schemaObject(map[string]any{
				"file_id":     map[string]any{"type": "string"},
				"start_ms":    map[string]any{"type": "integer"},
				"end_ms":      map[string]any{"type": "integer"},
				"output_name": map[string]any{"type": "string"},
			}, []string{"file_id", "start_ms", "end_ms"}),
			Handler: a.toolSubmitRender("trim", []string{"start_ms", "end_ms"}, []string{"file_id"}),
		},
		{
			Name:        "media_resize",
			Description: "Scale a video/image. Args: file_id, width (int), height (int, optional if keep_aspect), keep_aspect (bool, optional), output_name (string, optional).",
			InputSchema: schemaObject(map[string]any{
				"file_id":     map[string]any{"type": "string"},
				"width":       map[string]any{"type": "integer"},
				"height":      map[string]any{"type": "integer"},
				"keep_aspect": map[string]any{"type": "boolean"},
				"output_name": map[string]any{"type": "string"},
			}, []string{"file_id", "width"}),
			Handler: a.toolSubmitRender("resize", []string{"width", "height", "keep_aspect"}, []string{"file_id"}),
		},
		{
			Name:        "media_transcode",
			Description: "Re-encode to a new container/codec. Args: file_id, format (mp4|webm|mp3|...), video_codec (string, optional), audio_codec (string, optional), bitrate (string, optional, e.g. '2M').",
			InputSchema: schemaObject(map[string]any{
				"file_id":     map[string]any{"type": "string"},
				"format":      map[string]any{"type": "string"},
				"video_codec": map[string]any{"type": "string"},
				"audio_codec": map[string]any{"type": "string"},
				"bitrate":     map[string]any{"type": "string"},
				"output_name": map[string]any{"type": "string"},
			}, []string{"file_id", "format"}),
			Handler: a.toolSubmitRender("transcode", []string{"format", "video_codec", "audio_codec", "bitrate"}, []string{"file_id"}),
		},
		{
			Name:        "media_concat",
			Description: "Join multiple sources end-to-end (must share container/codec). Args: file_ids (array of strings, 2+), output_name (string, required).",
			InputSchema: schemaObject(map[string]any{
				"file_ids":    map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"output_name": map[string]any{"type": "string"},
			}, []string{"file_ids", "output_name"}),
			Handler: a.toolSubmitRender("concat", nil, []string{"file_ids"}),
		},
		{
			Name:        "media_crop",
			Description: "Crop a video or image. Args: file_id, x, y, width, height (all int, in pixels).",
			InputSchema: schemaObject(map[string]any{
				"file_id":     map[string]any{"type": "string"},
				"x":           map[string]any{"type": "integer"},
				"y":           map[string]any{"type": "integer"},
				"width":       map[string]any{"type": "integer"},
				"height":      map[string]any{"type": "integer"},
				"output_name": map[string]any{"type": "string"},
			}, []string{"file_id", "x", "y", "width", "height"}),
			Handler: a.toolSubmitRender("crop", []string{"x", "y", "width", "height"}, []string{"file_id"}),
		},
		{
			Name:        "media_extract_frame",
			Description: "Save a single frame at a specific timestamp as PNG. Args: file_id, at_ms (int), width (int, optional), output_name (string, optional).",
			InputSchema: schemaObject(map[string]any{
				"file_id":     map[string]any{"type": "string"},
				"at_ms":       map[string]any{"type": "integer"},
				"width":       map[string]any{"type": "integer"},
				"output_name": map[string]any{"type": "string"},
			}, []string{"file_id", "at_ms"}),
			Handler: a.toolSubmitRender("extract_frame", []string{"at_ms", "width"}, []string{"file_id"}),
		},
		{
			Name:        "media_audio_extract",
			Description: "Pull the audio track from a video into a standalone file. Args: file_id, format (mp3|wav|m4a|opus|flac).",
			InputSchema: schemaObject(map[string]any{
				"file_id":     map[string]any{"type": "string"},
				"format":      map[string]any{"type": "string"},
				"output_name": map[string]any{"type": "string"},
			}, []string{"file_id", "format"}),
			Handler: a.toolSubmitRender("audio_extract", []string{"format"}, []string{"file_id"}),
		},
		// ─── Render manage tools ────────────────────────────────────
		{
			Name:        "media_get_render",
			Description: "Status of one render. Args: render_id.",
			InputSchema: schemaObject(map[string]any{"render_id": map[string]any{"type": "integer"}}, []string{"render_id"}),
			Handler:     a.toolGetRender,
		},
		{
			Name:        "media_list_renders",
			Description: "List renders filtered by status (pending|running|ok|failed|cancelled), operation, or limit.",
			InputSchema: schemaObject(map[string]any{
				"status":    map[string]any{"type": "string"},
				"operation": map[string]any{"type": "string"},
				"limit":     map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolListRenders,
		},
		{
			Name:        "media_cancel_render",
			Description: "Cancel a pending or running render. Idempotent — already-terminal rows are no-ops. Args: render_id.",
			InputSchema: schemaObject(map[string]any{"render_id": map[string]any{"type": "integer"}}, []string{"render_id"}),
			Handler:     a.toolCancelRender,
		},
		{
			Name:        "media_set_description",
			Description: "Set title / description / alt_text on a media row. Partial update — omitted fields preserved, empty string clears. Requires the media row to already exist (the indexer creates it on probe). Args: file_id (required), title (optional), description (optional), alt_text (optional).",
			InputSchema: schemaObject(map[string]any{
				"file_id":     map[string]any{"type": "string"},
				"title":       map[string]any{"type": "string"},
				"description": map[string]any{"type": "string"},
				"alt_text":    map[string]any{"type": "string"},
			}, []string{"file_id"}),
			Handler: a.toolSetDescription,
		},
		{
			Name:        "media_transcribe",
			Description: "Queue a transcription for one media file. Inserts a pending row that the transcriber picks up on its next tick. force=true also re-queues already-ok rows (useful when you want a re-run after model upgrades or to retry a failed attempt). Args: file_id, force?.",
			InputSchema: schemaObject(map[string]any{
				"file_id": map[string]any{"type": "string"},
				"force":   map[string]any{"type": "boolean"},
			}, []string{"file_id"}),
			Handler: a.toolTranscribe,
		},
		{
			Name:        "media_get_transcript",
			Description: "Fetch one file's transcript (status, language, full text, segments).",
			InputSchema: schemaObject(map[string]any{"file_id": map[string]any{"type": "string"}}, []string{"file_id"}),
			Handler:     a.toolGetTranscript,
		},
		{
			Name:        "media_set_transcript",
			Description: "Upsert an externally-produced transcript (e.g. uploaded captions, third-party tool). Bypasses the auto pipeline. Args: file_id, text, language?, segments? (array of {start_ms, end_ms, text, speaker?}), provider?.",
			InputSchema: schemaObject(map[string]any{
				"file_id":  map[string]any{"type": "string"},
				"text":     map[string]any{"type": "string"},
				"language": map[string]any{"type": "string"},
				"segments": map[string]any{"type": "array"},
				"provider": map[string]any{"type": "string"},
			}, []string{"file_id", "text"}),
			Handler: a.toolSetTranscript,
		},
		{
			Name:        "media_describe",
			Description: "Queue an auto-generated description for one media file. The describer worker picks it up on its next sweep, calls the bound LLM integration (OpenCode Go default), and writes the result via media_set_description with description_source='ai-generated'. force=true ignores both the cooldown and any existing ai-generated description. Won't overwrite human-set descriptions in any case. Args: file_id, force?.",
			InputSchema: schemaObject(map[string]any{
				"file_id": map[string]any{"type": "string"},
				"force":   map[string]any{"type": "boolean"},
			}, []string{"file_id"}),
			Handler: a.toolDescribe,
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

// toolSetDescription writes prose columns on the media row. Partial
// update: pointer-distinguishes "preserve" (key not in args) from
// "clear" (key set to ""). Returns found=false when the file_id
// has no media row yet — agents should call media_reindex first or
// wait for the next indexer tick.
func (a *App) toolSetDescription(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	fid, _ := args["file_id"].(string)
	if fid == "" {
		return nil, errors.New("file_id required")
	}
	f := DescriptionFields{}
	if v, ok := args["title"].(string); ok {
		f.Title = &v
	}
	if v, ok := args["description"].(string); ok {
		f.Description = &v
	}
	if v, ok := args["alt_text"].(string); ok {
		f.AltText = &v
	}
	if f.Title == nil && f.Description == nil && f.AltText == nil {
		return nil, errors.New("provide at least one of title, description, alt_text")
	}
	created, err := setDescription(ctx.AppDB(), pid, fid, f)
	if err != nil {
		return nil, err
	}
	// Always found:true now — setDescription upserts a stub when
	// the row doesn't exist yet, so the description sticks even
	// before the indexer has probed the file.
	resp := map[string]any{"found": true, "file_id": fid, "updated": true}
	if created {
		resp["created"] = true
	}
	return resp, nil
}

// toolDescribe queues a media row for auto-description on the next
// describer sweep. force=true clears the cooldown so a manually-
// triggered retry doesn't have to wait describe_retry_cooldown_seconds.
// Will not overwrite human-set descriptions — the worker's candidate
// query filters those out unconditionally.
func (a *App) toolDescribe(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	fid, _ := args["file_id"].(string)
	if fid == "" {
		return nil, errors.New("file_id required")
	}
	media, err := getMedia(ctx.AppDB(), pid, fid)
	if err != nil {
		if notFound(err) {
			return map[string]any{"found": false}, nil
		}
		return nil, err
	}
	if media.DescriptionSource == "human" || media.DescriptionSource == "agent" {
		return map[string]any{
			"found":  true,
			"queued": false,
			"reason": "description is human-set; clear it via media_set_description first if you want auto-generation",
		}, nil
	}
	force, _ := args["force"].(bool)
	if force {
		// Wipe both the existing description and the cooldown so the
		// next sweep re-attempts. Targeted UPDATE keeps the rest of
		// the row intact (probe data, transcript pointer, etc.).
		if _, err := ctx.AppDB().Exec(
			`UPDATE media SET description='', description_source='', description_attempted_at=NULL, description_error=''
			   WHERE project_id=? AND file_id=?`, pid, fid,
		); err != nil {
			return nil, err
		}
	}
	return map[string]any{"found": true, "queued": true}, nil
}

// toolTranscribe queues a pending transcript row. force=true also
// re-queues rows that are already ok (useful for retries / model
// upgrades). The actual work happens in the transcriber goroutine.
func (a *App) toolTranscribe(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	fid, _ := args["file_id"].(string)
	if fid == "" {
		return nil, errors.New("file_id required")
	}
	force, _ := args["force"].(bool)
	if force {
		// Wipe any existing row so insertPendingTranscript treats this
		// as a fresh queue entry. The auto-policy uses ON CONFLICT to
		// avoid disturbing in-flight rows; manual force is the explicit
		// override.
		if _, err := ctx.AppDB().Exec(`DELETE FROM transcripts WHERE project_id=? AND file_id=?`, pid, fid); err != nil {
			return nil, err
		}
	}
	if err := insertPendingTranscript(ctx.AppDB(), pid, fid, "manual"); err != nil {
		return nil, err
	}
	return map[string]any{"file_id": fid, "status": "pending"}, nil
}

func (a *App) toolGetTranscript(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	fid, _ := args["file_id"].(string)
	if fid == "" {
		return nil, errors.New("file_id required")
	}
	t, err := getTranscript(ctx.AppDB(), pid, fid)
	if err != nil {
		if notFound(err) {
			return map[string]any{"found": false}, nil
		}
		return nil, err
	}
	return map[string]any{"found": true, "transcript": t}, nil
}

// toolSetTranscript installs a pre-made transcript without going
// through Deepgram. Use case: imported captions, manual upload from
// the panel, or testing. Marks the row as ok directly.
func (a *App) toolSetTranscript(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	fid, _ := args["file_id"].(string)
	if fid == "" {
		return nil, errors.New("file_id required")
	}
	text, _ := args["text"].(string)
	if text == "" {
		return nil, errors.New("text required")
	}
	provider, _ := args["provider"].(string)
	if provider == "" {
		provider = "imported"
	}
	t := &TranscriptRow{
		FileID:     fid,
		ProjectID:  pid,
		Status:     "ok",
		Text:       text,
		Provider:   provider,
		SourceKind: "imported",
	}
	if v, ok := args["language"].(string); ok {
		t.Language = v
	}
	// Segments come over the wire as []any of map[string]any; round-
	// trip through json so the persisted JSON matches our shape.
	if raw, ok := args["segments"]; ok && raw != nil {
		bb, err := json.Marshal(raw)
		if err == nil && len(bb) > 0 {
			t.Segments = bb
		}
	}
	// Snapshot media duration when we can — keeps the row coherent
	// with the catalog without forcing the caller to provide it.
	if media, mErr := getMedia(ctx.AppDB(), pid, fid); mErr == nil {
		t.DurationMs = media.DurationMs
		t.SourceSHA256 = media.SourceSHA256
	}
	if err := upsertTranscript(ctx.AppDB(), t); err != nil {
		return nil, err
	}
	return map[string]any{"file_id": fid, "status": "ok"}, nil
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
	case tail == "transcript" && r.Method == http.MethodGet:
		// Lazy-fetch for the panel's drawer. Returns found:false when
		// no row yet (file is queued or pre-transcribe).
		tr, err := getTranscript(globalCtx.AppDB(), pid, fid)
		if err != nil {
			if notFound(err) {
				writeJSON(w, map[string]any{"found": false})
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"found": true, "transcript": tr})
	case tail == "transcript" && r.Method == http.MethodPut:
		// Imported / manual transcript upload. Same partial-update
		// shape as media_set_transcript MCP tool.
		var body struct {
			Text     string              `json:"text"`
			Language string              `json:"language"`
			Provider string              `json:"provider"`
			Segments []TranscriptSegment `json:"segments"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
		if body.Text == "" {
			http.Error(w, "text required", http.StatusBadRequest)
			return
		}
		t := &TranscriptRow{
			FileID: fid, ProjectID: pid,
			Text:       body.Text,
			Language:   body.Language,
			Provider:   firstNonEmpty(body.Provider, "imported"),
			SourceKind: "imported",
		}
		if len(body.Segments) > 0 {
			segsJSON, err := formatSegments(body.Segments)
			if err != nil {
				http.Error(w, "segments: "+err.Error(), http.StatusBadRequest)
				return
			}
			t.Segments = segsJSON
		}
		if media, mErr := getMedia(globalCtx.AppDB(), pid, fid); mErr == nil {
			t.DurationMs = media.DurationMs
			t.SourceSHA256 = media.SourceSHA256
		}
		if err := upsertTranscript(globalCtx.AppDB(), t); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"file_id": fid, "status": "ok"})
	case tail == "transcribe" && r.Method == http.MethodPost:
		// Queue a transcript (or force-requeue when ?force=true).
		// Mirrors the media_transcribe MCP tool.
		if r.URL.Query().Get("force") == "true" {
			if _, err := globalCtx.AppDB().Exec(`DELETE FROM transcripts WHERE project_id=? AND file_id=?`, pid, fid); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		if err := insertPendingTranscript(globalCtx.AppDB(), pid, fid, "manual"); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		writeJSON(w, map[string]any{"file_id": fid, "status": "pending"})
	case tail == "description" && r.Method == http.MethodPut:
		// Panel + agent use this to set/update prose. Same partial-
		// update semantics as the MCP tool: pointer-distinguished
		// fields so {"description":""} clears, missing keys preserve.
		var body struct {
			Title       *string `json:"title"`
			Description *string `json:"description"`
			AltText     *string `json:"alt_text"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
		f := DescriptionFields{Title: body.Title, Description: body.Description, AltText: body.AltText}
		if f.Title == nil && f.Description == nil && f.AltText == nil {
			http.Error(w, "provide at least one of title, description, alt_text", http.StatusBadRequest)
			return
		}
		created, err := setDescription(globalCtx.AppDB(), pid, fid, f)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		resp := map[string]any{"file_id": fid, "updated": true}
		if created {
			resp["created"] = true
		}
		writeJSON(w, resp)
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

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

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

// toolReindex flips one row (or all failed rows) back to pending so
// the indexer's next tick re-probes them. MCP wrapper around the
// existing /reindex HTTP route.
func (a *App) toolReindex(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	if fid, _ := args["file_id"].(string); fid != "" {
		_, err := ctx.AppDB().Exec(
			`UPDATE media SET probe_status='pending', probe_error='', source_sha256='' WHERE project_id=? AND file_id=?`,
			pid, fid,
		)
		if err != nil {
			return nil, err
		}
		return map[string]any{"queued": 1, "file_id": fid}, nil
	}
	if v, _ := args["failed_only"].(bool); v {
		res, err := ctx.AppDB().Exec(
			`UPDATE media SET probe_status='pending', probe_error='' WHERE project_id=? AND probe_status IN ('failed','unsupported','skipped_size')`,
			pid,
		)
		if err != nil {
			return nil, err
		}
		n, _ := res.RowsAffected()
		return map[string]any{"queued": n}, nil
	}
	return nil, errors.New("provide file_id or failed_only=true")
}

// toolIndexStatus returns probe-status counts. Same data the
// /status HTTP route serves.
func (a *App) toolIndexStatus(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	rows, err := ctx.AppDB().Query(
		`SELECT probe_status, COUNT(*) FROM media WHERE project_id=? GROUP BY probe_status`, pid,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	counts := map[string]int64{}
	for rows.Next() {
		var status string
		var n int64
		if err := rows.Scan(&status, &n); err != nil {
			return nil, err
		}
		counts[status] = n
	}
	return map[string]any{"counts": counts}, nil
}

// ─── Render tool handlers ──────────────────────────────────────────
//
// toolSubmitRender returns a generic submit handler closed over the
// operation name + which arg keys to copy into the params blob and
// which to interpret as source file_ids. Single-source ops list
// "file_id" in sourceKeys; concat lists "file_ids".

func (a *App) toolSubmitRender(operation string, paramKeys, sourceKeys []string) sdk.ToolHandler {
	return func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
		pid, err := resolveProjectFromArgs(args)
		if err != nil {
			return nil, err
		}
		sources, err := extractSourceIDs(args, sourceKeys)
		if err != nil {
			return nil, err
		}
		params := pickParams(args, paramKeys)
		outputName, _ := args["output_name"].(string)
		requestedBy, _ := args["_requested_by"].(string)

		// Pre-validate by building the plan now. Fast-fail bad params
		// at submit time rather than letting the worker pick up a
		// guaranteed-failed render.
		paramJSON, _ := json.Marshal(params)
		if _, err := buildPlan(operation, sources, paramJSON, outputName); err != nil {
			return nil, err
		}

		id, err := insertRender(ctx.AppDB(), pid, operation, sources, params, outputName, requestedBy)
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"render_id": id,
			"status":    "pending",
			"operation": operation,
		}, nil
	}
}

func (a *App) toolGetRender(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args["render_id"])
	if id == 0 {
		return nil, errors.New("render_id required")
	}
	r, err := getRender(ctx.AppDB(), pid, id)
	if err != nil {
		if notFound(err) {
			return map[string]any{"found": false}, nil
		}
		return nil, err
	}
	return map[string]any{"found": true, "render": r}, nil
}

func (a *App) toolListRenders(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	f := RenderFilters{}
	f.Status, _ = args["status"].(string)
	f.Operation, _ = args["operation"].(string)
	f.Limit = int(int64Arg(args["limit"]))
	rows, err := listRenders(ctx.AppDB(), pid, f)
	if err != nil {
		return nil, err
	}
	return map[string]any{"renders": rows}, nil
}

func (a *App) toolCancelRender(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args["render_id"])
	if id == 0 {
		return nil, errors.New("render_id required")
	}
	// Project-scope check: only act on rows in our project. getRender
	// already enforces this; we return found=false rather than touching
	// other tenants' renders.
	r, err := getRender(ctx.AppDB(), pid, id)
	if err != nil {
		if notFound(err) {
			return map[string]any{"found": false}, nil
		}
		return nil, err
	}
	if r.Status != "pending" && r.Status != "running" {
		// Idempotent: terminal states are a no-op success.
		return map[string]any{"found": true, "status": r.Status, "noop": true}, nil
	}
	// Order matters: kill the ffmpeg child first (worker will mark
	// the row cancelled when it sees ctx.Err == Canceled). For
	// pending rows there's no child — flip the row directly.
	if !triggerCancel(id) {
		if err := renderMarkCancelled(ctx.AppDB(), id); err != nil {
			return nil, err
		}
	}
	return map[string]any{"found": true, "status": "cancelled"}, nil
}

// extractSourceIDs handles both "file_id": "x" and "file_ids": ["a","b"].
// Anything that comes through MCP as numeric (file_id: 42) gets
// stringified so the renders.source_file_ids JSON is consistently
// strings on the way out.
func extractSourceIDs(args map[string]any, keys []string) ([]string, error) {
	for _, k := range keys {
		v, ok := args[k]
		if !ok {
			continue
		}
		switch x := v.(type) {
		case string:
			if x == "" {
				return nil, fmt.Errorf("%s required", k)
			}
			return []string{x}, nil
		case float64: // JSON numbers
			return []string{strconv.FormatInt(int64(x), 10)}, nil
		case int64:
			return []string{strconv.FormatInt(x, 10)}, nil
		case int:
			return []string{strconv.Itoa(x)}, nil
		case []any:
			out := make([]string, 0, len(x))
			for _, e := range x {
				switch ev := e.(type) {
				case string:
					out = append(out, ev)
				case float64:
					out = append(out, strconv.FormatInt(int64(ev), 10))
				case int64:
					out = append(out, strconv.FormatInt(ev, 10))
				default:
					return nil, fmt.Errorf("%s: unsupported element type %T", k, e)
				}
			}
			if len(out) == 0 {
				return nil, fmt.Errorf("%s must be non-empty", k)
			}
			return out, nil
		}
	}
	return nil, fmt.Errorf("source ids missing — expected one of: %v", keys)
}

func pickParams(args map[string]any, keys []string) map[string]any {
	out := map[string]any{}
	for _, k := range keys {
		if v, ok := args[k]; ok {
			out[k] = v
		}
	}
	return out
}

// ─── Render HTTP handlers ──────────────────────────────────────────
//
// These exist primarily so the jobs app can schedule renders by
// firing HTTP at media. Same shape as the MCP tools but speaking
// HTTP. Dashboard panels also use them (the panel's Renders tab
// hits GET /renders to populate).

func (a *App) handleRendersCollection(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		f := RenderFilters{}
		q := r.URL.Query()
		f.Status = q.Get("status")
		f.Operation = q.Get("operation")
		if v := q.Get("limit"); v != "" {
			f.Limit, _ = strconv.Atoi(v)
		}
		rows, err := listRenders(globalCtx.AppDB(), pid, f)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"renders": rows})
	case http.MethodPost:
		var body struct {
			Operation   string         `json:"operation"`
			FileID      string         `json:"file_id"`
			FileIDs     []string       `json:"file_ids"`
			OutputName  string         `json:"output_name"`
			RequestedBy string         `json:"requested_by"`
			Params      map[string]any `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
			return
		}
		if body.Operation == "" {
			http.Error(w, "operation required", http.StatusBadRequest)
			return
		}
		sources := body.FileIDs
		if len(sources) == 0 && body.FileID != "" {
			sources = []string{body.FileID}
		}
		if len(sources) == 0 {
			http.Error(w, "file_id or file_ids required", http.StatusBadRequest)
			return
		}
		if body.Params == nil {
			body.Params = map[string]any{}
		}
		paramJSON, _ := json.Marshal(body.Params)
		if _, err := buildPlan(body.Operation, sources, paramJSON, body.OutputName); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		id, err := insertRender(globalCtx.AppDB(), pid, body.Operation, sources, body.Params, body.OutputName, body.RequestedBy)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusAccepted)
		writeJSON(w, map[string]any{"render_id": id, "status": "pending"})
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleRenderItem(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/renders/")
	idStr := strings.SplitN(rest, "/", 2)[0]
	id, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		http.Error(w, "render id required", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		row, err := getRender(globalCtx.AppDB(), pid, id)
		if err != nil {
			if notFound(err) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, row)
	case http.MethodDelete:
		// Same logic as toolCancelRender — kill child if running,
		// otherwise flip the row.
		row, err := getRender(globalCtx.AppDB(), pid, id)
		if err != nil {
			if notFound(err) {
				http.Error(w, "not found", http.StatusNotFound)
				return
			}
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if row.Status != "pending" && row.Status != "running" {
			writeJSON(w, map[string]any{"status": row.Status, "noop": true})
			return
		}
		if !triggerCancel(id) {
			if err := renderMarkCancelled(globalCtx.AppDB(), id); err != nil {
				http.Error(w, err.Error(), http.StatusInternalServerError)
				return
			}
		}
		writeJSON(w, map[string]any{"status": "cancelled"})
	default:
		http.Error(w, "GET or DELETE", http.StatusMethodNotAllowed)
	}
}

