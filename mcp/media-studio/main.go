// Media Studio v0.3 — generate images, video, audio, and music via any
// compatible provider.
//
// Architecture:
//   - manifest declares 5 single-binding integration roles:
//       image_provider, video_provider, audio_provider, music_provider, storage
//     each optional; tools enforce "is this role bound?" at call time.
//   - one unified MCP tool (media_generate) discriminates on `kind` and
//     routes to per-kind builders + normalizers (image.go, video.go, …).
//   - bytes are downloaded from the upstream URL while it's still fresh,
//     handed off to storage when bound, or returned inline / via upstream
//     URL otherwise.
//
// History lives in the app's own DB so the panel can render a gallery
// across restarts and sessions, filterable by kind.
package main

import (
	"bytes"
	"database/sql"
	"errors"
	"image"
	"image/jpeg"
	_ "image/png"
	"io"
	"net/http"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: media-studio
display_name: Media Studio
version: 0.5.5
description: |
  Generate images, video, audio, and music via any compatible provider.
  Optionally saves outputs to the Storage app for permanent references.
author: Apteva
scopes: [project, global]
requires:
  permissions:
    - db.write.app
    - net.egress
    - platform.connections.execute
    - platform.apps.call
  integrations:
    - role: image_provider
      kind: integration
      compatible_slugs: [openai-api, venice-ai]
      capabilities: [image.generate, image.edit]
      tools:
        image.generate: generate_image
        image.edit: edit_image
      required: false
      label: "Image provider"
    - role: video_provider
      kind: integration
      compatible_slugs: [venice-ai, replicate, runway, pika]
      capabilities: [video.generate]
      tools: { video.generate: queue_video }
      required: false
      label: "Video provider"
    - role: audio_provider
      kind: integration
      compatible_slugs: [elevenlabs, openai-api, venice-ai]
      capabilities: [audio.tts, audio.sfx]
      tools:
        audio.tts: text_to_speech
        audio.sfx: generate_sfx
      required: false
      label: "Audio provider"
    - role: music_provider
      kind: integration
      compatible_slugs: [suno, replicate]
      capabilities: [music.generate]
      tools: { music.generate: generate_music }
      required: false
      label: "Music provider"
    - role: storage
      kind: app
      compatible_app_names: [storage]
      capabilities: [files.write]
      required: false
      label: "Storage (optional)"
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: media_generate, description: "Generate media (image/video/audio/music). Args: kind, prompt, model?, size?, duration?, voice?, aspect?, n?, options?." }
    - { name: media_history,  description: "List recent generations. Args: kind?, limit?, since?." }
  ui_panels:
    - slot: project.page
      label: Studio
      icon: image
      entry: /ui/MediaPanel.mjs
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/media-studio
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/media-studio.db
  migrations: migrations/
upgrade_policy: auto-patch
`

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
		return errors.New("media-studio requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("media-studio mounted")
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error    { return nil }
func (a *App) Channels() []sdk.ChannelFactory { return nil }
func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{
		{
			Name:     "video-poll",
			Schedule: "@every 15s",
			Run:      a.videoPollWorker,
		},
	}
}
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

// ─── HTTP routes (panel data) ──────────────────────────────────────

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/generations", Handler: a.handleListGenerations},
		{Pattern: "/generate", Handler: a.handleGenerate},
		{Pattern: "/bindings", Handler: a.handleBindings},
		{Pattern: "/models", Handler: a.handleListModels},
		{Pattern: "/video-jobs", Handler: a.handleListVideoJobs},
		{Pattern: "/cache/", Handler: a.handleCacheGet},
	}
}

// ─── MCP tools ─────────────────────────────────────────────────────

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name: "media_generate",
			Description: "Generate media (image / video / audio / music). " +
				"Args: kind (required: image|video|audio_tts|audio_sfx|music), prompt (required), " +
				"model?, size? (image), duration? (video/audio/music, seconds), voice? (audio_tts), " +
				"aspect? (video), n?, options? (provider-specific extras: background, output_format, " +
				"lyrics, style, seed, image_storage_id, …). Returns MCP content blocks: image " +
				"(thumbnail base64 for image kind only when no storage), text (summary with storage URLs " +
				"when bound), resource (fetchable URL per storage_id).",
			InputSchema: schemaObject(map[string]any{
				"kind": map[string]any{
					"type":        "string",
					"description": "Discriminates which provider to invoke.",
					"enum":        []string{"image", "video", "audio_tts", "audio_sfx", "music"},
				},
				"prompt": map[string]any{
					"type":        "string",
					"description": "Text prompt (or text-to-speak when kind=audio_tts).",
				},
				"model":    map[string]any{"type": "string", "description": "Provider model id; per-kind defaults apply if omitted."},
				"size":     map[string]any{"type": "string", "description": "Image size (image only). e.g. 1024x1024."},
				"duration": map[string]any{"type": "integer", "description": "Length in seconds (video/audio/music)."},
				"voice":    map[string]any{"type": "string", "description": "Voice id (audio_tts only)."},
				"aspect":   map[string]any{"type": "string", "description": "Aspect ratio (video only). e.g. 16:9."},
				"n":        map[string]any{"type": "integer", "default": 1, "minimum": 1, "maximum": 10},
				"options": map[string]any{
					"type":        "object",
					"description": "Per-provider extras passed through (background, output_format, lyrics, style, seed, image_storage_id, …).",
				},
			}, []string{"kind", "prompt"}),
			Handler: a.toolMediaGenerate,
		},
		{
			Name:        "media_history",
			Description: "List recent generations for this project. Args: kind? (filter), limit? (default 50, max 200), since? (ISO8601).",
			InputSchema: schemaObject(map[string]any{
				"kind":  map[string]any{"type": "string", "enum": []string{"image", "video", "audio_tts", "audio_sfx", "music"}},
				"limit": map[string]any{"type": "integer", "default": 50},
				"since": map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolMediaHistory,
		},
	}
}

func main() { sdk.Run(&App{}) }

// ─── generic helpers ───────────────────────────────────────────────

func schemaObject(props map[string]any, required []string) map[string]any {
	s := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func strArg(m map[string]any, key, def string) string {
	if v, ok := m[key].(string); ok && v != "" {
		return v
	}
	return def
}

func intArg(m map[string]any, key string, def int) int {
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	}
	return def
}

func fetchBytes(url string) ([]byte, error) {
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Apteva media-studio)")
	client := &http.Client{Timeout: 90 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, errors.New("upstream non-2xx")
	}
	// 200MB limit — video can be large; storage handles the heavy hand-off.
	return io.ReadAll(io.LimitReader(resp.Body, 200<<20))
}

// makeThumbnail JPEG-compresses image bytes to ~30KB at the given max
// edge. Best-effort; on any decode failure returns nil so the caller
// skips the image content block. Only meaningful for kind=image (and
// for video provider posters, if a provider ever returns one).
func makeThumbnail(src []byte, maxEdge int) []byte {
	img, _, err := image.Decode(bytes.NewReader(src))
	if err != nil {
		return nil
	}
	bounds := img.Bounds()
	w, h := bounds.Dx(), bounds.Dy()
	if w == 0 || h == 0 {
		return nil
	}
	scale := 1.0
	if w > maxEdge || h > maxEdge {
		if w >= h {
			scale = float64(maxEdge) / float64(w)
		} else {
			scale = float64(maxEdge) / float64(h)
		}
	}
	tw, th := int(float64(w)*scale), int(float64(h)*scale)
	if tw < 1 {
		tw = 1
	}
	if th < 1 {
		th = 1
	}
	thumb := image.NewRGBA(image.Rect(0, 0, tw, th))
	for y := 0; y < th; y++ {
		for x := 0; x < tw; x++ {
			sx := int(float64(x) / scale)
			sy := int(float64(y) / scale)
			thumb.Set(x, y, img.At(sx, sy))
		}
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, thumb, &jpeg.Options{Quality: 75}); err != nil {
		return nil
	}
	return buf.Bytes()
}

// quiet "imported and not used" for stdlib pkgs only used in some paths.
var _ = sql.Drivers
