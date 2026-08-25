// Media v0.2 — catalog + cheap derivations + parameterised renders
// over storage's media files.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

// ─── Manifest (also lives in apteva.yaml) ──────────────────────────

const manifestYAML = `schema: apteva-app/v1
name: media
display_name: Media
version: 0.13.92
description: |
  Catalog + derivations + renders + transcripts + auto-descriptions
  for media files in storage. Indexes uploads (probe, thumbnail,
  waveform), runs on-demand edits (trim/resize/transcode/concat/
  crop/extract_frame/audio_extract/audio_filter) via local ffmpeg by default or
  Cloudinary when bound, auto-transcribes audio + video via Deepgram,
  and auto-generates descriptions via OpenCode Go, OpenAI API, or
  OpenAI Codex when integrations are bound. Outputs all flow
  through storage.
author: Apteva
scopes: [project, global]
min_apteva_version: "0.25.9"
requires:
  permissions:
    - db.write.app
    - net.egress
    - platform.connections.execute
    - platform.apps.call
  apps:
    - name: storage
      version: ">=0.10.23"
      reason: reads source bytes; writes destination-preserving thumbnails, waveforms, and render outputs back to storage
    - name: jobs
      version: ">=0.1.0"
      optional: true
      reason: optional — schedule recurring or delayed renders against media's HTTP routes
    - name: instances
      version: ">=0.2.0"
      optional: true
      reason: optional — when render_host_id > 0, renders run on that instances host via SSH
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
      compatible_slugs: [opencode-go, openai-api, openai-codex]
      capabilities: [chat.complete, vision.describe]
      tools:
        chat.complete: chat_completion
        vision.describe: chat_completion
      required: false
      label: "Auto-description provider"
      hint: "Connect OpenCode Go, OpenAI API, or OpenAI Codex to auto-generate descriptions and answer media_ask questions from existing thumbnails, keyframes, and transcripts. Defaults: kimi-k2.6 on OpenCode Go, gpt-4o-mini on OpenAI API, gpt-5.5 on OpenAI Codex."
    - role: render_executor
      kind: integration
      compatible_slugs: [cloudinary]
      capabilities: [video.transform, image.transform]
      tools:
        video.transform: upload
        image.transform: upload
      required: false
      label: "Cloud render backend"
      hint: "Optional. Connect Cloudinary to offload trim/resize/transcode/crop/extract_frame to the cloud — useful on Pi-class hosts. Without it (the default), renders run on local ffmpeg. concat + audio_extract + audio_filter always stay local."
  binaries:
    - name: ffmpeg
      version: "7.0.2"
      executables: [ffmpeg, ffprobe]
      required: true
      hint: "Auto-fetched on install."
      sources:
        linux-amd64:
          url: https://johnvansickle.com/ffmpeg/releases/ffmpeg-7.0.2-amd64-static.tar.xz
          sha256: abda8d77ce8309141f83ab8edf0596834087c52467f6badf376a6a2a4c87cf67
          archive: tar.xz
          strip_root: 1
        linux-arm64:
          url: https://johnvansickle.com/ffmpeg/releases/ffmpeg-7.0.2-arm64-static.tar.xz
          sha256: f4149bb2b0784e30e99bdda85471c9b5930d3402014e934a5098b41d0f7201b1
          archive: tar.xz
          strip_root: 1
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: media_get,             description: "Fetch one media record by storage file_id. The returned url is a fresh public/signed fetch URL suitable for third-party ingestion such as Bunny Stream fetch uploads." }
    - { name: media_analyze,         description: "Read-only technical and quality analysis for an image, video, or audio file. Returns encoding metadata, decode integrity, visual measurements and timeline anomalies, and audio loudness/peak/silence measurements where applicable. Creates no artifacts." }
    - { name: media_ask,             description: "Ask a grounded question using only existing source images, cached thumbnails/keyframes, and completed transcripts. Never runs ffmpeg, creates derivations, or writes files." }
    - { name: media_search,          description: "Compact catalog discovery with q/filename/title, folder_scope exact|subtree, type, aspect, duration, rating, dimensions, and codec filters. Empty exact searches diagnose matching descendants; call media_get for full details." }
    - { name: media_list_folders,    description: "List immediate child folders of parent that contain media." }
    - { name: media_create_folder,   description: "Create an empty folder in storage that media files can later land in. Idempotent. Args - path." }
    - { name: media_move,            description: "Move and/or rename a media file in storage. Media's row auto-updates via the file.updated event handler. Args - file_id, folder?, name?." }
    - { name: media_delete,          description: "Delete a media file and its backing storage file. Hard-deletes storage plus media's catalog data and derivations. Args - file_id." }
    - { name: media_get_thumbnail,   description: "Get the thumbnail derivation pointer (storage file_id) — generates if missing." }
    - { name: media_get_waveform,    description: "Get the waveform derivation pointer (audio only)." }
    - { name: media_reindex,         description: "Queue one atomic re-probe + re-derive for a file_id, or requeue all failed rows. Exact file IDs are fetched directly from Storage, so reindexing does not depend on catalog position or inventory size. A queued response is asynchronous; wait for media.derived or poll media_get/media_get_keyframes. Do not submit a second force request merely because keyframes are still being generated." }
    - { name: media_index_status,    description: "Counts of pending / ok / failed / unsupported / skipped_size." }
    - name: media_trim
      description: "Cut a clip from a video/audio source. Returns render_id."
      async_result:
        id_field: render_id
        notify:
          target: caller
          mode: once
          events:
            - render.completed
            - render.failed
            - render.cancelled
          match:
            render_id: "$result.render_id"
          expires_after: 24h
    - name: media_resize
      description: "Scale a video/image to new dimensions. Returns render_id."
      async_result:
        id_field: render_id
        notify:
          target: caller
          mode: once
          events:
            - render.completed
            - render.failed
            - render.cancelled
          match:
            render_id: "$result.render_id"
          expires_after: 24h
    - name: media_transcode
      description: "Re-encode to a new container/codec. Returns render_id."
      async_result:
        id_field: render_id
        notify:
          target: caller
          mode: once
          events:
            - render.completed
            - render.failed
            - render.cancelled
          match:
            render_id: "$result.render_id"
          expires_after: 24h
    - name: media_concat
      description: "Join multiple sources end-to-end. Returns render_id."
      async_result:
        id_field: render_id
        notify:
          target: caller
          mode: once
          events:
            - render.completed
            - render.failed
            - render.cancelled
          match:
            render_id: "$result.render_id"
          expires_after: 24h
    - name: media_crop
      description: "Crop or smart-reframe an image/video. Smart Crop v2 analyzes the cached canonical thumbnail, centers subjects, and protects visible faces/heads near portrait edges. Returns render_id."
      async_result:
        id_field: render_id
        notify:
          target: caller
          mode: once
          events:
            - render.completed
            - render.failed
            - render.cancelled
          match:
            render_id: "$result.render_id"
          expires_after: 24h
    - name: media_extract_frame
      description: "Save a frame as PNG; Smart Crop v2 uses cached storyboard frames when dense, bounded temporary source screenshots when sparse, and automatic face/head edge protection. Returns render_id."
      async_result:
        id_field: render_id
        notify:
          target: caller
          mode: once
          events:
            - render.completed
            - render.failed
            - render.cancelled
          match:
            render_id: "$result.render_id"
          expires_after: 24h
    - name: media_audio_extract
      description: "Strip audio from a video into a standalone file. Returns render_id."
      async_result:
        id_field: render_id
        notify:
          target: caller
          mode: once
          events:
            - render.completed
            - render.failed
            - render.cancelled
          match:
            render_id: "$result.render_id"
          expires_after: 24h
    - name: media_audio_filter
      description: "Normalize, clean, adjust, or mute audio in an audio/video source. Normalization preserves the indexed source sample rate and applies a lossy-codec-safe peak limiter. For video outputs, copies video and only re-encodes audio. Returns render_id."
      async_result:
        id_field: render_id
        notify:
          target: caller
          mode: once
          events:
            - render.completed
            - render.failed
            - render.cancelled
          match:
            render_id: "$result.render_id"
          expires_after: 24h
    - name: media_extract_reel
      description: "Trim + reframe in one pass. Smart Crop v2 tracks subjects across bounded cached or temporary source screenshots, protects visible faces/heads near portrait edges, and uses a stable fixed crop when movement is low. Returns render_id."
      async_result:
        id_field: render_id
        notify:
          target: caller
          mode: once
          events:
            - render.completed
            - render.failed
            - render.cancelled
          match:
            render_id: "$result.render_id"
          expires_after: 24h
    - { name: media_get_render,      description: "Status of one render — progress + output_file_id when ready." }
    - { name: media_list_renders,    description: "List renders filtered by status / operation." }
    - { name: media_cancel_render,   description: "Cancel a pending or running render. Idempotent." }
    - { name: media_set_description, description: "Set title / description / alt_text on a media row. Partial update; omitted fields preserved." }
    - { name: media_set_audience_rating, description: "Override audience rating (general | mature | adult | unrated). 'unrated' clears and re-queues for the describer." }
    - { name: media_get_keyframes,   description: "Storyboard frames for a video as [{position_ms, storage_file_id, url}, …]." }
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
  ui_components:
    - name: media-card
      entry: /ui/MediaCard.mjs
      slots: [chat.message_attachment]
      props_schema: {type: object, required: [file_id], properties: {file_id: {type: [string, integer]}}}
      preview_props: {preview: true}
    - name: render-card
      entry: /ui/RenderCard.mjs
      slots: [chat.message_attachment]
      props_schema: {type: object, required: [render_id], properties: {render_id: {type: integer}}}
      preview_props: {preview: true}
    - name: transcript-card
      entry: /ui/TranscriptCard.mjs
      slots: [chat.message_attachment]
      props_schema: {type: object, required: [file_id], properties: {file_id: {type: [string, integer]}, max_lines: {type: integer}}}
      preview_props: {preview: true}
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: media/v0.13.92
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
	if n, err := recoverInterruptedRenders(ctx.AppDB()); err != nil {
		return fmt.Errorf("recover interrupted renders: %w", err)
	} else if n > 0 {
		ctx.Logger().Warn("requeued interrupted renders", "count", n)
	}
	if n, err := recoverInterruptedTranscripts(ctx.AppDB()); err != nil {
		return fmt.Errorf("recover interrupted transcripts: %w", err)
	} else if n > 0 {
		ctx.Logger().Warn("requeued interrupted transcripts", "count", n)
	}
	// Render pool runs alongside the indexer worker. Pool size is
	// independent: the indexer is a single scheduled tick, the pool
	// is N hot goroutines.
	poolSize := readConfigInt("render_pool_size", 4)
	startRenderPool(ctx, poolSize)
	// Auto-transcriber: separate goroutine, isolated from indexer +
	// render pool. Skips itself if transcribe_auto=false; degrades
	// gracefully when the deepgram integration isn't bound.
	startTranscriber(ctx)
	// Auto-describer: another isolated goroutine. Reads transcripts
	// + thumbnails when present, calls the bound LLM integration,
	// writes the description back via setDescription.
	startDescriber(ctx)
	// Storage event subscriber — listens for storage.file.deleted
	// over SSE and cascades cleanup immediately. Indexer's 30s
	// orphan sweep stays as the safety net.
	startStorageEventSubscriber(ctx)
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
		{Pattern: "/media/facets", Handler: a.handleMediaFacets},
		{Pattern: "/media/", Handler: a.handleMediaItem},
		{Pattern: "/folders", Handler: a.handleFolders},
		{Pattern: "/status", Handler: a.handleStatus},
		{Pattern: "/reindex", Handler: a.handleReindex},
		{Pattern: "/smartcrop", Handler: a.handleSmartCropPreview},
		// Renders. /renders accepts POST {operation, ...} for
		// jobs-app-style scheduled triggers; GET lists. /renders/{id}
		// supports GET (status) + DELETE (cancel).
		{Pattern: "/renders", Handler: a.handleRendersCollection},
		// /renders/summary must come BEFORE /renders/ in registration
		// order so the more-specific pattern wins — sdk's HTTP route
		// mounting respects insertion order for subtree disambiguation.
		{Pattern: "/renders/summary", Handler: a.handleRendersSummary},
		{Pattern: "/renders/", Handler: a.handleRenderItem},
	}
}

// handleFolders is the HTTP twin of media_list_folders. The dashboard
// panel uses it to render the folder navigation tree.
func (a *App) handleFolders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	parent := r.URL.Query().Get("parent")
	if parent == "" {
		parent = "/"
	} else {
		if !strings.HasPrefix(parent, "/") {
			parent = "/" + parent
		}
		if !strings.HasSuffix(parent, "/") {
			parent = parent + "/"
		}
	}
	folders, err := listChildFolders(globalCtx.AppDB(), pid, parent)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"folders": folders, "parent": parent})
}

// ─── MCP tools ─────────────────────────────────────────────────────

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "media_get",
			Description: "Fetch one media record by storage file_id. Returns display-space width/height/orientation + derivation pointers. The returned media.url is a fresh public/signed fetch URL suitable for third-party ingestion such as Bunny Stream fetch uploads. Raw ffprobe JSON and renderer-only rotation metadata are hidden unless include_raw_probe=true.",
			InputSchema: schemaObject(map[string]any{
				"file_id":           map[string]any{"type": "string"},
				"include_raw_probe": map[string]any{"type": "boolean"},
			}, []string{"file_id"}),
			Handler: a.toolGet,
		},
		{
			Name:        "media_analyze",
			Description: "Analyze an existing image, video, or audio source without modifying it. Returns catalog/stream encoding details, decode integrity, sampled visual measurements, black/frozen video segments, and LUFS/peak/RMS/silence audio measurements where applicable. depth=standard analyzes at most 60 seconds; depth=full analyzes the requested/full duration. No render, derivation, or Storage object is created.",
			InputSchema: schemaObject(map[string]any{
				"file_id":              map[string]any{"type": "string"},
				"depth":                map[string]any{"type": "string", "enum": []string{"standard", "full"}, "default": "standard"},
				"start_ms":             map[string]any{"type": "integer", "minimum": 0},
				"end_ms":               map[string]any{"type": "integer", "minimum": 0},
				"silence_threshold_db": map[string]any{"type": "number", "default": -50},
				"silence_min_ms":       map[string]any{"type": "integer", "minimum": 100, "default": 1000},
			}, []string{"file_id"}),
			Handler: a.toolAnalyze,
		},
		{
			Name:        "media_ask",
			Description: "Ask a grounded question about a media file using the configured descriptions vision/chat integration. Images use an existing thumbnail or source object. Videos use the existing canonical thumbnail plus cached storyboard keyframes; at_ms selects the nearest existing keyframe and reports its actual timestamp. Audio uses an existing completed transcript. This tool never runs ffmpeg, generates frames/derivations, or writes files.",
			InputSchema: schemaObject(map[string]any{
				"file_id":            map[string]any{"type": "string"},
				"question":           map[string]any{"type": "string", "maxLength": maxAskQuestionChars},
				"at_ms":              map[string]any{"type": "integer", "minimum": 0, "description": "Video only. Selects the nearest cached keyframe; does not extract an exact frame."},
				"frame_count":        map[string]any{"type": "integer", "minimum": 1, "maximum": maxAskFrameCount, "default": defaultAskFrameCount},
				"include_transcript": map[string]any{"type": "boolean", "default": true},
			}, []string{"file_id", "question"}),
			Handler: a.toolAsk,
		},
		{
			Name:        "media_search",
			Description: "Search the media catalog before calling media_get. Use q for a case-insensitive match across filename, title, description, and alt text; use filename or title for narrower matching. With folder, folder_scope='exact' (the default) searches only that folder; folder_scope='subtree' searches it and every descendant. Namespace roots such as /hgv, /ashley, /monika, /alexa, and /lily normally require subtree. An empty exact result does not prove its subtree is empty: inspect has_matching_descendants and retry_recommended. Results are compact discovery rows (file_id, filename/title, type, duration, dimensions, folder, thumbnail) by default. Filters include folder, media_type, aspect (portrait|landscape|square|reel|wide), duration, rating, dimensions, and codec. Call media_get with the chosen file_id for complete metadata and source URLs. Default limit is 20, maximum 100. If has_more is true, repeat the same filters with next_cursor. detail and include_raw_probe are exceptional opt-ins.",
			InputSchema: schemaObject(map[string]any{
				"q":               map[string]any{"type": "string", "description": "Case-insensitive contains match across filename, title, description, and alt text."},
				"filename":        map[string]any{"type": "string", "description": "Case-insensitive contains match on the storage filename only."},
				"title":           map[string]any{"type": "string", "description": "Case-insensitive contains match on the media title only."},
				"folder":          map[string]any{"type": "string", "description": "Storage folder to search. Normalized to leading and trailing slashes."},
				"folder_scope":    map[string]any{"type": "string", "enum": []string{"exact", "subtree"}, "default": "exact", "description": "exact searches only files directly in folder; subtree searches folder and every descendant. Root namespaces with session subfolders normally need subtree."},
				"recursive":       map[string]any{"type": "boolean", "description": "Deprecated compatibility alias: false maps to folder_scope=exact; true maps to folder_scope=subtree. Do not pass conflicting values."},
				"media_type":      map[string]any{"type": "string", "enum": []string{"image", "video", "audio"}},
				"aspect":          map[string]any{"type": "string", "enum": []string{"portrait", "landscape", "square", "reel", "wide"}},
				"duration":        map[string]any{"type": "string", "enum": []string{"short", "medium", "long", "extended"}},
				"duration_min_ms": map[string]any{"type": "integer"},
				"duration_max_ms": map[string]any{"type": "integer"},
				// Legacy raw ffprobe flags remain supported for
				// compatibility, but media_type is safer for agents.
				"has_video":   map[string]any{"type": "boolean"},
				"has_audio":   map[string]any{"type": "boolean"},
				"is_image":    map[string]any{"type": "boolean"},
				"width_min":   map[string]any{"type": "integer"},
				"width_max":   map[string]any{"type": "integer"},
				"video_codec": map[string]any{"type": "string"},
				"audio_codec": map[string]any{"type": "string"},
				"audience_rating": map[string]any{
					"oneOf": []map[string]any{
						{"type": "string", "enum": []string{"general", "mature", "adult", "unrated"}},
						{"type": "array", "items": map[string]any{"type": "string", "enum": []string{"general", "mature", "adult", "unrated"}}},
					},
				},
				"exclude_audience_rating": map[string]any{
					"oneOf": []map[string]any{
						{"type": "string", "enum": []string{"general", "mature", "adult", "unrated"}},
						{"type": "array", "items": map[string]any{"type": "string", "enum": []string{"general", "mature", "adult", "unrated"}}},
					},
				},
				"limit":             map[string]any{"type": "integer", "minimum": 1, "maximum": mediaSearchMaxLimit, "default": mediaSearchDefaultLimit},
				"cursor":            map[string]any{"type": "string", "description": "Opaque next_cursor from the preceding page. Reuse the same filters."},
				"offset":            map[string]any{"type": "integer", "minimum": 0, "description": "Legacy pagination offset. Prefer cursor; do not pass both."},
				"order_by":          map[string]any{"type": "string", "enum": []string{"duration_ms", "created_at", "updated_at"}},
				"detail":            map[string]any{"type": "boolean", "default": false, "description": "Return full metadata rows. Prefer media_get for a selected file."},
				"include_raw_probe": map[string]any{"type": "boolean", "default": false, "description": "Include raw ffprobe JSON. Implies detail=true."},
			}, nil),
			Handler: a.toolSearch,
		},
		{
			Name:        "media_list_folders",
			Description: "List immediate child folders of `parent` that contain media (audio/video/image). Args: parent (default '/').",
			InputSchema: schemaObject(map[string]any{
				"parent": map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolListFolders,
		},
		{
			Name:        "media_create_folder",
			Description: "Create an empty folder in storage. Pass-through to storage's files_create_folder; idempotent. Args: path (e.g. '/raw-footage/2026-05/').",
			InputSchema: schemaObject(map[string]any{
				"path": map[string]any{"type": "string"},
			}, []string{"path"}),
			Handler: a.toolCreateFolder,
		},
		{
			Name:        "media_move",
			Description: "Move and/or rename a media file in storage. At least one of folder / name must be set. Media's row auto-updates via the file.updated event. Args: file_id (string), folder?, name?.",
			InputSchema: schemaObject(map[string]any{
				"file_id": map[string]any{"type": "string"},
				"folder":  map[string]any{"type": "string"},
				"name":    map[string]any{"type": "string"},
			}, []string{"file_id"}),
			Handler: a.toolMove,
		},
		{
			Name:        "media_delete",
			Description: "Delete a media file and its backing storage file. This is destructive: the storage row/blob is hard-deleted, then media removes its catalog row, transcript, thumbnails, waveform, and keyframes. Args: file_id (string).",
			InputSchema: schemaObject(map[string]any{
				"file_id": map[string]any{"type": "string"},
			}, []string{"file_id"}),
			Handler: a.toolDelete,
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
			Name: "media_reindex",
			Description: "Queue one atomic re-probe + re-derive. Exact file IDs are fetched directly from Storage, so reindexing is independent of catalog position and inventory size. A queued response is asynchronous; wait for media.derived or poll media_get/media_get_keyframes instead of submitting a second force request while keyframes are still being generated. Pass file_id to re-index one row, " +
				"or failed_only=true to retry every failed/unsupported row in the project. " +
				"force=true (with file_id) bypasses the max_probe_size_mb cap for that one file " +
				"— useful for genuinely huge sources where you accept the temp-disk hit.",
			InputSchema: schemaObject(map[string]any{
				"file_id":     map[string]any{"type": "string"},
				"failed_only": map[string]any{"type": "boolean"},
				"force":       map[string]any{"type": "boolean"},
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
				"file_id":       map[string]any{"type": "string"},
				"start_ms":      map[string]any{"type": "integer"},
				"end_ms":        map[string]any{"type": "integer"},
				"output_name":   map[string]any{"type": "string"},
				"output_folder": map[string]any{"type": "string"},
			}, []string{"file_id", "start_ms", "end_ms"}),
			Handler: a.toolSubmitRender("trim", []string{"start_ms", "end_ms"}, []string{"file_id"}),
		},
		{
			Name:        "media_resize",
			Description: "Scale a video/image. Args: file_id, width (int), height (int, optional if keep_aspect), keep_aspect (bool, optional), output_name (string, optional).",
			InputSchema: schemaObject(map[string]any{
				"file_id":       map[string]any{"type": "string"},
				"width":         map[string]any{"type": "integer"},
				"height":        map[string]any{"type": "integer"},
				"keep_aspect":   map[string]any{"type": "boolean"},
				"output_name":   map[string]any{"type": "string"},
				"output_folder": map[string]any{"type": "string"},
			}, []string{"file_id", "width"}),
			Handler: a.toolSubmitRender("resize", []string{"width", "height", "keep_aspect"}, []string{"file_id"}),
		},
		{
			Name:        "media_transcode",
			Description: "Re-encode to a new container/codec. Args: file_id, format (mp4|webm|mp3|...), video_codec (string, optional), audio_codec (string, optional), bitrate (string, optional, e.g. '2M').",
			InputSchema: schemaObject(map[string]any{
				"file_id":       map[string]any{"type": "string"},
				"format":        map[string]any{"type": "string"},
				"video_codec":   map[string]any{"type": "string"},
				"audio_codec":   map[string]any{"type": "string"},
				"bitrate":       map[string]any{"type": "string"},
				"output_name":   map[string]any{"type": "string"},
				"output_folder": map[string]any{"type": "string"},
			}, []string{"file_id", "format"}),
			Handler: a.toolSubmitRender("transcode", []string{"format", "video_codec", "audio_codec", "bitrate"}, []string{"file_id"}),
		},
		{
			Name:        "media_concat",
			Description: "Join multiple sources end-to-end (must share container/codec). Args: file_ids (array of strings, 2+), output_name (string, required).",
			InputSchema: schemaObject(map[string]any{
				"file_ids":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"output_name":   map[string]any{"type": "string"},
				"output_folder": map[string]any{"type": "string"},
			}, []string{"file_ids", "output_name"}),
			Handler: a.toolSubmitRender("concat", nil, []string{"file_ids"}),
		},
		{
			Name:        "media_crop",
			Description: "Crop or smart-reframe an existing video/image. Exact mode: file_id, x, y, width, height. Smart Crop v2 mode analyzes the canonical cached image/video thumbnail, centers subjects, and protects visible faces/heads near portrait edges: file_id, target_ratio, crop_mode? ('smart' default|'center'), output_width?. Use this for still images or a whole video; use media_extract_frame for a video timestamp.",
			InputSchema: schemaObject(map[string]any{
				"file_id":       map[string]any{"type": "string"},
				"x":             map[string]any{"type": "integer", "description": "Exact crop left offset in pixels. Used with y, width, and height when target_ratio is not set."},
				"y":             map[string]any{"type": "integer", "description": "Exact crop top offset in pixels. Used with x, width, and height when target_ratio is not set."},
				"width":         map[string]any{"type": "integer", "description": "Exact crop width in pixels. Required for exact mode; optional fallback when target_ratio is set."},
				"height":        map[string]any{"type": "integer", "description": "Exact crop height in pixels. Required for exact mode; optional fallback when target_ratio is set."},
				"target_ratio":  map[string]any{"type": "string", "description": "When set, crop/reframe to this aspect ratio ('W:H'), e.g. '9:16', '1:1', '4:5'. Enables smart/center mode for existing images and full video clips."},
				"output_width":  map[string]any{"type": "integer", "description": "Optional scale width after target_ratio crop. Omit to preserve the computed crop dimensions."},
				"crop_mode":     map[string]any{"type": "string", "description": "'smart' (default) for subject-aware crop via the source's cached thumbnail/keyframe saliency, or 'center' for geometric center. Smart falls back to center when derivations are unavailable."},
				"output_name":   map[string]any{"type": "string"},
				"output_folder": map[string]any{"type": "string"},
			}, []string{"file_id"}),
			Handler: a.toolSubmitRender("crop", []string{"x", "y", "width", "height", "target_ratio", "output_width", "crop_mode"}, []string{"file_id"}),
		},
		{
			Name:        "media_extract_frame",
			Description: "Save a video frame as PNG. With target_ratio, Smart Crop v2 uses dense cached storyboard frames or bounded temporary source screenshots around at_ms, including zero-motion person centering and automatic face/head edge protection. Args: file_id, at_ms, width?, target_ratio?, output_width?, crop_mode?.",
			InputSchema: schemaObject(map[string]any{
				"file_id":       map[string]any{"type": "string"},
				"at_ms":         map[string]any{"type": "integer"},
				"width":         map[string]any{"type": "integer", "description": "Output width when target_ratio is NOT set (pure scale, keeps aspect)."},
				"target_ratio":  map[string]any{"type": "string", "description": "When set, crop + scale to this aspect ratio (\"W:H\"). E.g. \"1:1\", \"9:16\", \"4:5\"."},
				"output_width":  map[string]any{"type": "integer", "description": "Output width when target_ratio is set. Default 1080; height derives from ratio."},
				"crop_mode":     map[string]any{"type": "string", "description": "\"smart\" (default) for subject-aware crop via the nearest cached keyframe for timed operations, or \"center\" for geometric center. Smart falls back to thumbnail/center when keyframes are not ready."},
				"output_name":   map[string]any{"type": "string"},
				"output_folder": map[string]any{"type": "string"},
			}, []string{"file_id", "at_ms"}),
			Handler: a.toolSubmitRender("extract_frame", []string{"at_ms", "width", "target_ratio", "output_width", "crop_mode"}, []string{"file_id"}),
		},
		{
			Name:        "media_audio_extract",
			Description: "Pull the audio track from a video into a standalone file. Args: file_id, format (mp3|wav|m4a|opus|flac).",
			InputSchema: schemaObject(map[string]any{
				"file_id":       map[string]any{"type": "string"},
				"format":        map[string]any{"type": "string"},
				"output_name":   map[string]any{"type": "string"},
				"output_folder": map[string]any{"type": "string"},
			}, []string{"file_id", "format"}),
			Handler: a.toolSubmitRender("audio_extract", []string{"format"}, []string{"file_id"}),
		},
		{
			Name:        "media_audio_filter",
			Description: "Modify audio in an audio or video file. Normalization preserves the indexed source sample rate and applies a lossy-codec-safe peak limiter. For videos, copies the video stream unchanged and only filters/re-encodes audio. Args: file_id, mode ('normalize' default | 'speech_clean' | 'volume' | 'mute'), target_lufs (optional, default -16 for normalize/speech_clean), gain_db (for volume), output_name?, output_folder?. Audio-only inputs keep their audio container; video inputs keep their video container unless output_name has an audio extension.",
			InputSchema: schemaObject(map[string]any{
				"file_id":       map[string]any{"type": "string"},
				"mode":          map[string]any{"type": "string", "description": "'normalize' (default), 'speech_clean', 'volume', or 'mute'."},
				"target_lufs":   map[string]any{"type": "number", "description": "Target integrated loudness for normalize/speech_clean. Default -16."},
				"gain_db":       map[string]any{"type": "number", "description": "Gain in dB for mode='volume', e.g. 3 or -2."},
				"output_name":   map[string]any{"type": "string"},
				"output_folder": map[string]any{"type": "string"},
			}, []string{"file_id"}),
			Handler: a.toolSubmitRender("audio_filter", []string{"mode", "target_lufs", "gain_db"}, []string{"file_id"}),
		},
		{
			Name:        "media_extract_reel",
			Description: "Cut and reframe a clip in one ffmpeg pass. Smart Crop v2 analyzes a bounded cached storyboard or temporary source screenshots when sparse, centers stable people, protects visible faces/heads near portrait edges, and follows movement with a smoothed path. Args: file_id, start_ms, end_ms, target_ratio? (default '9:16'), output_width?, crop_mode? ('smart' default|'center').",
			InputSchema: schemaObject(map[string]any{
				"file_id":       map[string]any{"type": "string", "description": "Storage file_id of the source video."},
				"start_ms":      map[string]any{"type": "integer", "description": "Clip start, milliseconds from start of source. Same convention as media_trim."},
				"end_ms":        map[string]any{"type": "integer", "description": "Clip end, milliseconds from start of source. Must be > start_ms."},
				"target_ratio":  map[string]any{"type": "string", "description": "Output aspect ratio as 'W:H'. Default '9:16'. Common: '9:16' (vertical reels), '1:1' (square), '4:5' (Instagram portrait), '16:9' (passthrough crop)."},
				"output_width":  map[string]any{"type": "integer", "description": "Output width in pixels. Default 1080. Height auto-derives from target_ratio (rounded to even for codec compatibility)."},
				"crop_mode":     map[string]any{"type": "string", "description": "\"smart\" (default) keeps the most interesting subject in frame using the nearest cached keyframe for the reel/frame; \"center\" uses a geometric center crop. Smart falls back to thumbnail/center when keyframes are not ready."},
				"output_name":   map[string]any{"type": "string", "description": "Optional output filename. Extension auto-corrected to .mp4."},
				"output_folder": map[string]any{"type": "string", "description": "Optional storage folder for the rendered output. Defaults to install's render_output_folder (typically /renders/)."},
			}, []string{"file_id", "start_ms", "end_ms"}),
			Handler: a.toolSubmitRender("extract_reel", []string{"start_ms", "end_ms", "target_ratio", "output_width", "crop_mode"}, []string{"file_id"}),
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
			Name:        "media_set_audience_rating",
			Description: "Override the audience rating on a media row. The describer auto-rates new files (general/mature/adult); this tool lets an operator/agent set it manually. Use rating='unrated' to clear and re-queue for the describer. Args: file_id, rating (general|mature|adult|unrated), reasoning? (short explanation).",
			InputSchema: schemaObject(map[string]any{
				"file_id":   map[string]any{"type": "string"},
				"rating":    map[string]any{"type": "string", "enum": []string{"general", "mature", "adult", "unrated"}},
				"reasoning": map[string]any{"type": "string"},
			}, []string{"file_id", "rating"}),
			Handler: a.toolSetAudienceRating,
		},
		{
			Name:        "media_get_keyframes",
			Description: "Get the storyboard keyframes for a video — list of {position_ms, storage_file_id, url} ordered by position. Empty for non-video files or videos before the indexer's keyframe step has run. The set is what the describer samples for multi-image prompts; agents can use it for timeline scrubbing, scene detection, or as input to further analysis tools.",
			InputSchema: schemaObject(map[string]any{
				"file_id": map[string]any{"type": "string"},
			}, []string{"file_id"}),
			Handler: a.toolGetKeyframes,
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
			Description: "Queue an auto-generated description for one media file. The describer worker picks it up on its next sweep, calls the bound LLM integration, and writes the result via media_set_description with description_source='ai-generated'. force=true ignores both the cooldown and any existing ai-generated description. Won't overwrite human-set descriptions in any case. Args: file_id, force?.",
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
	includeRawProbe, _ := boolArg(args["include_raw_probe"])
	rows := sanitizeMediaToolRows([]MediaRow{*m}, includeRawProbe)
	enriched, _, eerr := enrichRows(context.Background(), pid, rows)
	if eerr != nil {
		// Storage roundtrip failed — surface unenriched row with a
		// flag so agents can tell it apart from a deleted file.
		rows[0].Derivations = nil
		return map[string]any{"found": true, "media": &rows[0], "storage_unavailable": true}, nil
	}
	row := enriched[0]
	if signed, err := signedFetchURLForMedia(ctx, pid, fid, row.Visibility, row.URL); err == nil && signed != "" {
		row.URL = signed
	} else if row.Visibility != "public" {
		// Avoid returning a private canonical URL that looks usable
		// to third-party services. The rest of the probe metadata is
		// still valuable, and the flag tells agents to retry later.
		row.URL = ""
		return map[string]any{"found": true, "media": row, "storage_unavailable": true}, nil
	}
	return map[string]any{"found": true, "media": row}, nil
}

const mediaGetSignedURLTTLSeconds = 24 * 60 * 60

// signedFetchURLForMedia returns a URL that can be handed to an
// external fetcher such as Bunny Stream. Public files can use their
// canonical URL. Private/signed files ask storage to mint a fresh
// signed/presigned URL via the bound app; tests and older platforms
// fall back to the HTTP helper.
func signedFetchURLForMedia(ctx *sdk.AppCtx, projectID, fileID, visibility, canonicalURL string) (string, error) {
	if visibility == "public" && canonicalURL != "" {
		return canonicalURL, nil
	}
	id, err := strconv.ParseInt(fileID, 10, 64)
	if err != nil || id <= 0 {
		return "", fmt.Errorf("invalid file_id %q", fileID)
	}
	args := map[string]any{
		"_project_id": projectID,
		"id":          id,
		"ttl_seconds": mediaGetSignedURLTTLSeconds,
	}
	if ctx != nil && ctx.PlatformAPI() != nil {
		var out struct {
			URL string `json:"url"`
		}
		if err := ctx.PlatformAPI().CallAppResult("storage", "files_get_url", args, &out); err == nil && out.URL != "" {
			return absolutizeStorageFetchURL(ctx, out.URL), nil
		}
	}
	return newStorageClient().GetSignedURL(context.Background(), projectID, id, mediaGetSignedURLTTLSeconds)
}

func absolutizeStorageFetchURL(ctx *sdk.AppCtx, u string) string {
	if !strings.HasPrefix(u, "/") {
		return u
	}
	publicURL, err := resolvePublicURL(ctx)
	if err != nil {
		return u
	}
	if strings.HasPrefix(u, "/api/apps/storage/") {
		return publicURL + u
	}
	return publicURL + "/api/apps/storage" + u
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

// toolSetAudienceRating writes the operator/agent's audience verdict
// to a media row. Bypasses the describer pipeline — the rating
// sticks even on subsequent re-indexes (unless the operator explicitly
// resets it to "unrated", which clears the column and re-queues the
// row for the describer).
func (a *App) toolSetAudienceRating(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	fid, _ := args["file_id"].(string)
	if fid == "" {
		return nil, errors.New("file_id required")
	}
	rating, _ := args["rating"].(string)
	rating = strings.ToLower(strings.TrimSpace(rating))
	switch rating {
	case "general", "mature", "adult", "unrated":
		// valid
	default:
		return nil, errors.New("rating must be one of: general, mature, adult, unrated")
	}
	reasoning, _ := args["reasoning"].(string)
	if rating == "unrated" {
		// Reset path — clear reasoning + timestamp so the describer
		// re-queues on its next sweep.
		_, err := ctx.AppDB().Exec(
			`UPDATE media SET audience_rating='unrated', audience_reasoning='', audience_updated_at=NULL
			  WHERE project_id=? AND file_id=?`, pid, fid)
		if err != nil {
			return nil, err
		}
	} else {
		if err := setAudienceRating(ctx.AppDB(), pid, fid, rating, reasoning); err != nil {
			return nil, err
		}
	}
	return map[string]any{"file_id": fid, "rating": rating, "reasoning": reasoning}, nil
}

// toolGetKeyframes returns the storyboard keyframes for a video as
// [{position_ms, storage_file_id, url}, …]. Empty for non-video files
// or videos that haven't reached the indexer's keyframe step yet
// (e.g. just-uploaded). URLs are 60-minute signed reads.
func (a *App) toolGetKeyframes(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	fid, _ := args["file_id"].(string)
	if fid == "" {
		return nil, errors.New("file_id required")
	}
	derivs, err := listDerivations(ctx.AppDB(), pid, fid)
	if err != nil {
		return nil, err
	}
	sc := newStorageClient()
	cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	derivs, err = resolveValidDerivations(cctx, sc, pid, derivs)
	if err != nil {
		return nil, err
	}
	type keyframeOut struct {
		PositionMs    int64  `json:"position_ms"`
		StorageFileID string `json:"storage_file_id"`
		URL           string `json:"url"`
	}
	out := make([]keyframeOut, 0)
	for _, d := range derivs {
		if d.Kind != "keyframe" || d.Status != "ok" {
			continue
		}
		entry := keyframeOut{PositionMs: d.PositionMs, StorageFileID: d.StorageFileID}
		if id, err := strconv.ParseInt(d.StorageFileID, 10, 64); err == nil {
			if u, err := sc.GetSignedURL(cctx, pid, id, 3600); err == nil {
				entry.URL = u
			}
		}
		out = append(out, entry)
	}
	return map[string]any{"file_id": fid, "keyframes": out}, nil
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

func normalizeMediaType(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "image", "images":
		return "image"
	case "video", "videos":
		return "video"
	case "audio", "audios":
		return "audio"
	default:
		return ""
	}
}

func normalizeAspect(v string) string {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "portrait", "vertical":
		return "portrait"
	case "landscape":
		return "landscape"
	case "square", "1:1":
		return "square"
	case "reel", "story", "9:16":
		return "reel"
	case "wide", "16:9":
		return "wide"
	default:
		return ""
	}
}

func applyDurationBucket(f *SearchFilters, v string) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "short":
		f.DurationMinMs = 1
		f.DurationMaxMs = 30_000
	case "medium":
		f.DurationMinMs = 30_000
		f.DurationMaxMs = 120_000
	case "long":
		f.DurationMinMs = 120_000
		f.DurationMaxMs = 600_000
	case "extended":
		f.DurationMinMs = 600_000
		f.DurationMaxMs = 0
	}
}

func normalizeFolderFilter(folder string) string {
	folder = strings.TrimSpace(folder)
	if folder == "" {
		return ""
	}
	if !strings.HasPrefix(folder, "/") {
		folder = "/" + folder
	}
	if !strings.HasSuffix(folder, "/") {
		folder = folder + "/"
	}
	return folder
}

func ratingFilterArg(v any) []string {
	var out []string
	add := func(s string) {
		s = strings.ToLower(strings.TrimSpace(s))
		switch s {
		case "general", "mature", "adult", "unrated":
			out = append(out, s)
		}
	}
	switch x := v.(type) {
	case string:
		add(x)
	case []any:
		for _, item := range x {
			if s, ok := item.(string); ok {
				add(s)
			}
		}
	case []string:
		for _, s := range x {
			add(s)
		}
	}
	return out
}

func ratingFilterQuery(values []string) []string {
	var out []string
	for _, s := range values {
		out = append(out, ratingFilterArg(s)...)
	}
	return out
}

func (a *App) toolSearch(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	f := SearchFilters{}
	f.Q, _ = args["q"].(string)
	f.Filename, _ = args["filename"].(string)
	f.Title, _ = args["title"].(string)
	f.DurationMinMs = int64Arg(args["duration_min_ms"])
	f.DurationMaxMs = int64Arg(args["duration_max_ms"])
	if v, ok := args["duration"].(string); ok {
		applyDurationBucket(&f, v)
	}
	if v, ok := args["media_type"].(string); ok {
		f.MediaType = normalizeMediaType(v)
	}
	if v, ok := args["aspect"].(string); ok {
		f.Aspect = normalizeAspect(v)
	}
	if v, ok := boolArg(args["has_video"]); ok {
		f.HasVideo = &v
	}
	if v, ok := boolArg(args["has_audio"]); ok {
		f.HasAudio = &v
	}
	if v, ok := boolArg(args["is_image"]); ok {
		f.IsImage = &v
	}
	f.WidthMin = int(int64Arg(args["width_min"]))
	f.WidthMax = int(int64Arg(args["width_max"]))
	f.VideoCodec, _ = args["video_codec"].(string)
	f.AudioCodec, _ = args["audio_codec"].(string)
	f.Folder, _ = args["folder"].(string)
	f.Folder = normalizeFolderFilter(f.Folder)
	f.FolderScope, err = resolveMediaSearchFolderScope(args, f.Folder)
	if err != nil {
		return nil, err
	}
	f.Recursive = f.FolderScope == folderScopeSubtree
	pageLimit := mediaSearchLimit(args["limit"])
	pageOffset, err := mediaSearchOffset(args)
	if err != nil {
		return nil, err
	}
	// Fetch one extra row so has_more does not require a COUNT query.
	f.Limit = pageLimit + 1
	f.Offset = pageOffset
	f.OrderBy, _ = args["order_by"].(string)
	f.AudienceRatingIn = ratingFilterArg(args["audience_rating"])
	f.AudienceRatingNotIn = ratingFilterArg(args["exclude_audience_rating"])
	rows, err := searchMedia(ctx.AppDB(), pid, f)
	if err != nil {
		return nil, err
	}
	var diagnostic *MediaSearchEmptyDiagnostic
	if len(rows) == 0 && pageOffset == 0 && f.Folder != "" {
		var d MediaSearchEmptyDiagnostic
		if f.effectiveFolderScope() == folderScopeSubtree {
			d, err = diagnoseEmptySubtreeMediaSearch(ctx.AppDB(), pid, f)
		} else {
			d, err = diagnoseEmptyExactMediaSearch(ctx.AppDB(), pid, f)
		}
		if err != nil {
			return nil, err
		}
		diagnostic = &d
	}
	responseMetadata := mediaSearchResponseMetadata(f, diagnostic)
	moreFromDB := len(rows) > pageLimit
	if moreFromDB {
		rows = rows[:pageLimit]
	}
	includeRawProbe, _ := boolArg(args["include_raw_probe"])
	detail, _ := boolArg(args["detail"])
	if includeRawProbe {
		detail = true
	}

	if !detail {
		compact, eerr := compactMediaSearchRows(context.Background(), pid, rows)
		if eerr != nil {
			// Compact discovery still works without Storage; only resolved
			// thumbnail URLs may be absent.
			compact = projectMediaSearchRows(rows, nil)
			return fitMediaSearchPage(compact, pageOffset, moreFromDB, true, responseMetadata)
		}
		return fitMediaSearchPage(compact, pageOffset, moreFromDB, false, responseMetadata)
	}

	rows = sanitizeMediaToolRows(rows, includeRawProbe)
	enriched, _, eerr := enrichRows(context.Background(), pid, rows)
	if eerr != nil {
		// Storage temporarily unreachable — return un-enriched rows
		// with a flag so the agent doesn't read missing URLs as
		// "files deleted". media's own probe + description data is
		// still useful even without storage's metadata.
		for i := range rows {
			rows[i].Derivations = nil
		}
		return fitMediaSearchPage(rows, pageOffset, moreFromDB, true, responseMetadata)
	}
	return fitMediaSearchPage(enriched, pageOffset, moreFromDB, false, responseMetadata)
}

// toolListFolders mirrors storage's files_list_folders semantics —
// immediate children of `parent` that contain media. Lets agents
// browse the folder tree without leaving the media MCP.
func (a *App) toolListFolders(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	parent, _ := args["parent"].(string)
	if parent == "" {
		parent = "/"
	} else {
		// Same normalization as toolSearch — accept "clips",
		// "/clips", "/clips/" interchangeably.
		if !strings.HasPrefix(parent, "/") {
			parent = "/" + parent
		}
		if !strings.HasSuffix(parent, "/") {
			parent = parent + "/"
		}
	}
	folders, err := listChildFolders(ctx.AppDB(), pid, parent)
	if err != nil {
		return nil, err
	}
	return map[string]any{"folders": folders, "parent": parent, "count": len(folders)}, nil
}

// toolCreateFolder is a thin pass-through to storage's
// files_create_folder. Media has no folder-state of its own — folders
// only exist in storage's files table — so the right move is to
// delegate. Goes through CallApp so the binding is honoured and we
// don't bypass the platform's authorization gate.
func (a *App) toolCreateFolder(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	path, _ := args["path"].(string)
	path = strings.TrimSpace(path)
	if path == "" {
		return nil, errors.New("path required")
	}
	in := map[string]any{
		"path":        path,
		"_project_id": pid,
	}
	var out struct {
		Created bool   `json:"created"`
		Path    string `json:"path"`
	}
	if err := ctx.PlatformAPI().CallAppResult("storage", "files_create_folder", in, &out); err != nil {
		return nil, fmt.Errorf("storage.files_create_folder: %w", err)
	}
	return map[string]any{"created": out.Created, "path": out.Path}, nil
}

// toolMove relays a file move/rename to storage's files_move. Media's
// own row auto-updates because the indexer subscribes to storage's
// file.updated event and runs updateFolderFromEvent — so by the time
// this returns, media_search / media_list_folders already reflect
// the new location. We don't write the row ourselves to keep storage
// as the single source of truth for file location.
func (a *App) toolMove(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	fid, _ := args["file_id"].(string)
	fid = strings.TrimSpace(fid)
	if fid == "" {
		return nil, errors.New("file_id required")
	}
	folder, _ := args["folder"].(string)
	name, _ := args["name"].(string)
	folder = strings.TrimSpace(folder)
	name = strings.TrimSpace(name)
	if folder == "" && name == "" {
		return nil, errors.New("at least one of folder, name must be set")
	}
	idNum, perr := strconv.ParseInt(fid, 10, 64)
	if perr != nil {
		return nil, fmt.Errorf("file_id %q must be numeric", fid)
	}
	in := map[string]any{
		"id":          idNum,
		"_project_id": pid,
	}
	if folder != "" {
		in["folder"] = folder
	}
	if name != "" {
		in["name"] = name
	}
	var out map[string]any
	if err := ctx.PlatformAPI().CallAppResult("storage", "files_move", in, &out); err != nil {
		return nil, fmt.Errorf("storage.files_move: %w", err)
	}
	// Pass storage's response through verbatim — it carries the
	// updated id/folder/name plus an absolute URL the agent might
	// want to surface in chat.
	return out, nil
}

// toolDelete hard-deletes the source storage file and immediately
// removes media-owned catalog data. Storage also emits file.deleted,
// so the storage event subscriber may run the same cascade later; the
// cascade is intentionally idempotent.
func (a *App) toolDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	fid, _ := args["file_id"].(string)
	fid = strings.TrimSpace(fid)
	if fid == "" {
		return nil, errors.New("file_id required")
	}
	idNum, perr := strconv.ParseInt(fid, 10, 64)
	if perr != nil || idNum <= 0 {
		return nil, fmt.Errorf("file_id %q must be numeric", fid)
	}
	m, err := getMedia(ctx.AppDB(), pid, fid)
	if err != nil {
		if notFound(err) {
			return map[string]any{"found": false, "deleted": false, "file_id": fid}, nil
		}
		return nil, err
	}
	sc := newStorageClient()
	deleteCtx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	if err := sc.DeleteFile(deleteCtx, pid, idNum); err != nil {
		return nil, fmt.Errorf("storage.files_delete: %w", err)
	}
	if err := cascadeDeleteOne(ctx, sc, ctx.AppDB(), pid, fid); err != nil {
		return nil, err
	}
	ctx.EmitWithProject("media.deleted", pid, map[string]any{"file_id": fid})
	return map[string]any{
		"found":           true,
		"deleted":         true,
		"file_id":         fid,
		"name":            m.Name,
		"folder":          m.Folder,
		"storage_deleted": true,
	}, nil
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
		cctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		files, err := newStorageClient().ResolveFiles(cctx, pid, derivationStorageIDs(ds))
		if err != nil {
			return nil, err
		}
		ds = filterResolvedDerivations(ds, files)
		for _, d := range ds {
			if d.Kind == kind && d.Status == "ok" {
				// Resolve the derivation's storage URL so an agent
				// gets a directly-usable link without a follow-up
				// storage call.
				enriched := enrichDerivation(d, files)
				return map[string]any{
					"found":           true,
					"derivation":      enriched,
					"storage_file_id": d.StorageFileID,
					"url":             enriched.URL,
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
	applyDurationBucket(&f, q.Get("duration"))
	f.MediaType = normalizeMediaType(q.Get("media_type"))
	f.Aspect = normalizeAspect(q.Get("aspect"))
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
	if v := q.Get("offset"); v != "" {
		f.Offset, _ = strconv.Atoi(v)
	}
	if v := q.Get("width_min"); v != "" {
		f.WidthMin, _ = strconv.Atoi(v)
	}
	if v := q.Get("width_max"); v != "" {
		f.WidthMax, _ = strconv.Atoi(v)
	}
	f.VideoCodec = q.Get("video_codec")
	f.AudioCodec = q.Get("audio_codec")
	f.AudienceRatingIn = ratingFilterQuery(q["audience_rating"])
	f.AudienceRatingNotIn = ratingFilterQuery(q["exclude_audience_rating"])
	f.OrderBy = q.Get("order_by")
	f.Folder = normalizeFolderFilter(q.Get("folder"))
	scopeArgs := map[string]any{}
	if v := q.Get("folder_scope"); v != "" {
		scopeArgs["folder_scope"] = v
	}
	if _, exists := q["recursive"]; exists {
		scopeArgs["recursive"] = q.Get("recursive")
	}
	f.FolderScope, err = resolveMediaSearchFolderScope(scopeArgs, f.Folder)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.Recursive = f.FolderScope == folderScopeSubtree
	rows, err := searchMedia(globalCtx.AppDB(), pid, f)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"media": rows})
}

func (a *App) handleMediaFacets(w http.ResponseWriter, r *http.Request) {
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
	f := SearchFilters{Folder: normalizeFolderFilter(q.Get("folder"))}
	scopeArgs := map[string]any{}
	if v := q.Get("folder_scope"); v != "" {
		scopeArgs["folder_scope"] = v
	}
	if _, exists := q["recursive"]; exists {
		scopeArgs["recursive"] = q.Get("recursive")
	}
	f.FolderScope, err = resolveMediaSearchFolderScope(scopeArgs, f.Folder)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.Recursive = f.FolderScope == folderScopeSubtree
	counts, err := mediaFacetCounts(globalCtx.AppDB(), pid, f)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"counts": counts})
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
		if err := queueMediaReindex(globalCtx.AppDB(), pid, fid, false); err != nil {
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
		if err := queueMediaReindex(globalCtx.AppDB(), pid, fid, false); err != nil {
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

func boolArg(v any) (bool, bool) {
	switch x := v.(type) {
	case bool:
		return x, true
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "true", "1", "yes", "y", "on":
			return true, true
		case "false", "0", "no", "n", "off":
			return false, true
		}
	case float64:
		if x == 1 {
			return true, true
		}
		if x == 0 {
			return false, true
		}
	case int:
		if x == 1 {
			return true, true
		}
		if x == 0 {
			return false, true
		}
	case int64:
		if x == 1 {
			return true, true
		}
		if x == 0 {
			return false, true
		}
	}
	return false, false
}

// toolReindex flips one row (or all failed rows) back to pending so
// the indexer's next tick re-probes them. MCP wrapper around the
// existing /reindex HTTP route. force=true (file_id only) sets
// force_probe=1 on the row so processOne skips the size cap.
func (a *App) toolReindex(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	if fid, _ := args["file_id"].(string); fid != "" {
		force := false
		if v, _ := args["force"].(bool); v {
			force = true
		}
		if err := queueMediaReindex(ctx.AppDB(), pid, fid, force); err != nil {
			return nil, err
		}
		return map[string]any{"queued": 1, "file_id": fid, "force": force}, nil
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
		outputFolder, _ := args["output_folder"].(string)
		// Normalize same way as toolSearch — agents pass "clips" /
		// "/clips" / "/clips/" and we land on the same target.
		if outputFolder != "" {
			if !strings.HasPrefix(outputFolder, "/") {
				outputFolder = "/" + outputFolder
			}
			if !strings.HasSuffix(outputFolder, "/") {
				outputFolder = outputFolder + "/"
			}
		}
		requestedBy, _ := args["_requested_by"].(string)

		// Pre-validate by building the plan now. Fast-fail bad params
		// at submit time rather than letting the worker pick up a
		// guaranteed-failed render. sourceExt is left "" here: the
		// source may not be probed yet at submit time, and validation
		// only cares about params, not the output extension (which the
		// executor re-resolves from the indexed row).
		paramJSON, _ := json.Marshal(params)
		if _, err := buildPlan(operation, sources, paramJSON, outputName, ""); err != nil {
			return nil, err
		}

		id, err := insertRender(ctx.AppDB(), pid, operation, sources, params, outputName, outputFolder, requestedBy)
		if err != nil {
			return nil, err
		}
		emitRenderQueued(ctx, id, pid, operation, sources, requestedBy)
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
	// Resolve the output file's URL when the render is done. Agents
	// using just media MCP get a directly-usable link without
	// chaining a storage call.
	files, ferr := newStorageClient().ResolveFiles(
		context.Background(), pid, []string{r.OutputFileID})
	out := map[string]any{"found": true, "render": enrichRender(*r, files)}
	if ferr != nil && r.OutputFileID != "" {
		out["storage_unavailable"] = true
	}
	return out, nil
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
	// Enrich completed renders with storage URLs in one batch.
	ids := make([]string, 0, len(rows))
	for _, r := range rows {
		if r.OutputFileID != "" {
			ids = append(ids, r.OutputFileID)
		}
	}
	files, ferr := newStorageClient().ResolveFiles(context.Background(), pid, ids)
	enriched := make([]EnrichedRender, len(rows))
	for i, r := range rows {
		enriched[i] = enrichRender(r, files)
	}
	out := map[string]any{"renders": enriched}
	if ferr != nil {
		out["storage_unavailable"] = true
	}
	return out, nil
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
	// pending rows there's no child — flip the row directly. The
	// emit fires from runOneRender for running rows; the pending
	// branch emits here since no worker ever picked it up.
	triggered := triggerCancel(id)
	if err := renderMarkCancelled(ctx.AppDB(), id); err != nil {
		return nil, err
	}
	if !triggered {
		emitRenderCancelled(ctx, id, r.ProjectID, r.Operation)
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
		v, ok := args[k]
		if !ok {
			continue
		}
		// Coerce numeric-looking strings to numbers. Smaller LLMs
		// (and some MCP clients) emit at_ms / start_ms / width etc.
		// as JSON strings even when the tool's input_schema declares
		// "type": "integer". Without this coercion, json.Unmarshal
		// into the per-op params struct (e.g. extractFrameParams's
		// AtMs int64 field) fails on "1000" → "json: cannot unmarshal
		// string into Go struct field …", and the agent retries
		// forever in a loop that never actually reaches ffmpeg.
		//
		// The set of keys that flow through pickParams is closed and
		// entirely numeric (start_ms, end_ms, at_ms, width, height,
		// x, y, bitrate, keep_aspect). String params (format,
		// video_codec, audio_codec) live on different ops or skip
		// pickParams. So coercing string→number here doesn't risk
		// trampling a legitimate string value.
		out[k] = coerceNumeric(v)
	}
	return out
}

// coerceNumeric turns "1000" into 1000 (float64), "1.5" into 1.5,
// "true" / "false" into bool. Non-string values pass through. Strings
// that don't parse cleanly are left as-is.
func coerceNumeric(v any) any {
	s, ok := v.(string)
	if !ok {
		return v
	}
	// Booleans first — keep_aspect is the one bool-shaped param.
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "true":
		return true
	case "false":
		return false
	}
	if n, err := strconv.ParseFloat(s, 64); err == nil {
		return n
	}
	return s
}

// ─── Render HTTP handlers ──────────────────────────────────────────
//
// These exist primarily so the jobs app can schedule renders by
// firing HTTP at media. Same shape as the MCP tools but speaking
// HTTP. Dashboard panels also use them (the panel's Renders tab
// hits GET /renders to populate).

func (a *App) handleSmartCropPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST", http.StatusMethodNotAllowed)
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	var body struct {
		FileID      string `json:"file_id"`
		Operation   string `json:"operation"`
		TargetRatio string `json:"target_ratio"`
		CropMode    string `json:"crop_mode"`
		StartMs     int64  `json:"start_ms"`
		EndMs       int64  `json:"end_ms"`
		AtMs        int64  `json:"at_ms"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(body.FileID) == "" {
		http.Error(w, "file_id required", http.StatusBadRequest)
		return
	}
	op := strings.TrimSpace(body.Operation)
	if op == "" {
		op = "extract_reel"
	}
	ratio := strings.TrimSpace(body.TargetRatio)
	if ratio == "" {
		ratio = "9:16"
	}
	rw, rh, err := parseAspectRatio(ratio)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	mode := strings.ToLower(strings.TrimSpace(body.CropMode))
	if mode == "" {
		mode = "smart"
	}
	if mode != "smart" && mode != "center" {
		http.Error(w, "crop_mode must be smart or center", http.StatusBadRequest)
		return
	}
	row, err := getMedia(globalCtx.AppDB(), pid, body.FileID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	target := smartCropTarget{FocusMs: body.StartMs, StartMs: body.StartMs, EndMs: body.EndMs}
	target.PreferKeyframe = op == "extract_reel" || op == "extract_frame"
	if op == "extract_frame" {
		target.FocusMs = body.AtMs
		target.StartMs = body.AtMs
		target.EndMs = body.AtMs
	} else if op == "extract_reel" && body.EndMs > body.StartMs {
		target.FocusMs = body.StartMs + (body.EndMs-body.StartMs)/2
	}
	target = target.Normalized()
	win, err := computeSmartCrop(r.Context(), globalCtx, newStorageClient(), pid, body.FileID, rw, rh, mode, target)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	out := map[string]any{
		"crop": map[string]any{
			"crop_w": win.W,
			"crop_h": win.H,
			"crop_x": win.X,
			"crop_y": win.Y,
		},
		"source_width":  row.Width,
		"source_height": row.Height,
		"mode":          mode,
	}
	if mode == "smart" {
		if d := pickSmartCropDerivation(row.Derivations, target); d.StorageFileID != "" {
			out["derivation"] = map[string]any{
				"kind":            d.Kind,
				"storage_file_id": d.StorageFileID,
				"position_ms":     d.PositionMs,
			}
		}
	}
	writeJSON(w, out)
}

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
			Operation    string         `json:"operation"`
			FileID       string         `json:"file_id"`
			FileIDs      []string       `json:"file_ids"`
			OutputName   string         `json:"output_name"`
			OutputFolder string         `json:"output_folder"`
			RequestedBy  string         `json:"requested_by"`
			Params       map[string]any `json:"params"`
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
		// sourceExt "" — validation-only; executor re-resolves it.
		if _, err := buildPlan(body.Operation, sources, paramJSON, body.OutputName, ""); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		id, err := insertRender(globalCtx.AppDB(), pid, body.Operation, sources, body.Params, body.OutputName, body.OutputFolder, body.RequestedBy)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		emitRenderQueued(globalCtx, id, pid, body.Operation, sources, body.RequestedBy)
		w.WriteHeader(http.StatusAccepted)
		writeJSON(w, map[string]any{"render_id": id, "status": "pending"})
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

// handleRendersSummary powers the MediaPanel's queue widget. Single
// call returns the counts pill row + the running / pending / recent
// lists the panel renders. Panel uses it for initial load + as a
// re-sync when reconnecting an SSE stream (network blip, tab
// background-resume, etc.).
func (a *App) handleRendersSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	summary, err := queueSummary(globalCtx.AppDB(), pid)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, summary)
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
		triggered := triggerCancel(id)
		if err := renderMarkCancelled(globalCtx.AppDB(), id); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		if !triggered {
			emitRenderCancelled(globalCtx, id, row.ProjectID, row.Operation)
		}
		writeJSON(w, map[string]any{"status": "cancelled"})
	default:
		http.Error(w, "GET or DELETE", http.StatusMethodNotAllowed)
	}
}
