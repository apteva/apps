package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

const (
	KindImage    = "image"
	KindVideo    = "video"
	KindAudioTTS = "audio_tts"
	KindAudioSFX = "audio_sfx"
	KindMusic    = "music"
)

// kindHandler binds a kind to its role + capability + per-provider
// arg builder + per-provider response normalizer. To add a new kind
// or wire a new provider for an existing kind, edit only the per-kind
// file (image.go, video.go, audio.go, music.go) — never this map.
type kindHandler struct {
	Role string
	// ResolveCapability picks the capability per-call. Lets a single
	// kind drive multiple sub-flows (e.g. image.generate vs image.edit
	// based on presence of source_image).
	ResolveCapability func(args map[string]any) string
	// BuildArgs assembles the provider request body. providerSlug is
	// the bound integration's app_slug so per-provider quirks can be
	// gated inline; capability disambiguates within a multi-cap kind.
	BuildArgs func(args map[string]any, providerSlug, capability string) (map[string]any, error)
	// Normalize parses the provider response into a uniform media list.
	Normalize func(providerSlug, capability string, raw json.RawMessage) ([]generatedMedia, string, string, error)
	// StorageDir is the sub-folder under /.generated/ where storage
	// hand-offs land (images/, videos/, audio/, music/).
	StorageDir string
	// MakeThumbnail returns true when the pipeline should generate a
	// thumbnail from the bytes (currently image only).
	MakeThumbnail bool
}

// constCap returns a ResolveCapability that always picks the same
// capability — for kinds (video, music, audio_*) that don't fork.
func constCap(name string) func(map[string]any) string {
	return func(map[string]any) string { return name }
}

// resolveImageCapability picks image.edit when the caller supplied a
// source_image (any of "storage:N", URL, base64); image.generate otherwise.
func resolveImageCapability(args map[string]any) string {
	if v, ok := args["source_image"].(string); ok && strings.TrimSpace(v) != "" {
		return "image.edit"
	}
	return "image.generate"
}

var handlers = map[string]kindHandler{
	KindImage: {
		Role:              "image_provider",
		ResolveCapability: resolveImageCapability,
		BuildArgs:         buildImageArgs,
		Normalize:         normalizeImageResponse,
		StorageDir:        "images",
		MakeThumbnail:     true,
	},
	KindVideo: {
		Role:              "video_provider",
		ResolveCapability: constCap("video.generate"),
		BuildArgs:         buildVideoArgs,
		Normalize:         normalizeVideoResponse,
		StorageDir:        "videos",
	},
	KindAudioTTS: {
		Role:              "audio_provider",
		ResolveCapability: constCap("audio.tts"),
		BuildArgs:         buildAudioTTSArgs,
		Normalize:         normalizeAudioResponse,
		StorageDir:        "audio",
	},
	KindAudioSFX: {
		Role:              "audio_provider",
		ResolveCapability: constCap("audio.sfx"),
		BuildArgs:         buildAudioSFXArgs,
		Normalize:         normalizeAudioResponse,
		StorageDir:        "audio",
	},
	KindMusic: {
		Role:              "music_provider",
		ResolveCapability: constCap("music.generate"),
		BuildArgs:         buildMusicArgs,
		Normalize:         normalizeMusicResponse,
		StorageDir:        "music",
	},
}

// toolMediaGenerate is the unified MCP entry point. Discriminates on
// kind, resolves the bound integration, builds the provider request,
// normalizes the response, optionally persists to storage, and shapes
// the MCP result per kind.
func (a *App) toolMediaGenerate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	kind := strArg(args, "kind", "")
	if kind == "" {
		return nil, errors.New("kind required")
	}
	h, ok := handlers[kind]
	if !ok {
		return mcpError("unknown kind: " + kind), nil
	}
	prompt := strArg(args, "prompt", "")
	if strings.TrimSpace(prompt) == "" {
		return nil, errors.New("prompt required")
	}

	bound := ctx.IntegrationFor(h.Role)
	if bound == nil {
		return mcpError("no " + h.Role + " bound — pick one in app settings"), nil
	}
	capability := h.ResolveCapability(args)
	tool := bound.ToolFor(capability)
	if tool == "" {
		return mcpError("bound " + h.Role + " (" + bound.AppSlug + ") doesn't support " + capability), nil
	}

	// Source-image (edit flow) — resolve "storage:N" / URL / base64 into
	// the bytes-or-URL the per-provider builder will pass through.
	// We preserve the original ref under _source_image_ref so the
	// history row (and the MCP _meta) can show "edit of #1234".
	if capability == "image.edit" {
		orig := strArg(args, "source_image", "")
		resolved, err := resolveSourceImage(ctx, orig)
		if err != nil {
			return mcpError("source_image: " + err.Error()), nil
		}
		args["_source_image_ref"] = orig
		args["source_image"] = resolved
	}

	providerArgs, err := h.BuildArgs(args, bound.AppSlug, capability)
	if err != nil {
		return mcpError("build args: " + err.Error()), nil
	}

	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, tool, providerArgs)
	if err != nil {
		return mcpError("provider call failed: " + err.Error()), nil
	}
	if res == nil || !res.Success {
		body := ""
		if res != nil {
			body = string(res.Data)
		}
		return mcpError("provider returned non-2xx: " + body), nil
	}

	media, revisedPrompt, normalizedModel, err := h.Normalize(bound.AppSlug, capability, res.Data)
	if err != nil {
		return mcpError("provider response parse: " + err.Error()), nil
	}
	if len(media) == 0 {
		return mcpError("provider returned zero items"), nil
	}

	model := strArg(args, "model", "")
	if model == "" {
		model = normalizedModel
	}

	storage := ctx.IntegrationFor("storage")
	pid := os.Getenv("APTEVA_PROJECT_ID")
	storageIDs := make([]int64, 0, len(media))
	upstreamURLs := make([]string, 0, len(media))
	var firstThumbB64 string
	var totalDurationMs int64

	for i, item := range media {
		upstreamURLs = append(upstreamURLs, item.UpstreamURL)
		totalDurationMs += item.DurationMs

		body, err := mediaBytes(item)
		if err != nil {
			ctx.Logger().Warn("fetch media bytes failed", "url", item.UpstreamURL, "err", err)
			continue
		}
		if h.MakeThumbnail && i == 0 {
			if thumb := makeThumbnail(body, 256); thumb != nil {
				firstThumbB64 = base64.StdEncoding.EncodeToString(thumb)
			}
		}

		if storage != nil {
			id, err := saveToStorage(ctx, item, h.StorageDir, bound.AppSlug, i)
			if err != nil {
				ctx.Logger().Warn("storage save failed", "err", err)
				continue
			}
			if id != 0 {
				storageIDs = append(storageIDs, id)
			}
		}
	}

	size := strArg(args, "size", "")
	extraJSON := encodeExtras(kind, args)
	a.dbInsertGeneration(generationRecord{
		ProjectID:    pid,
		Kind:         kind,
		Prompt:       prompt,
		Revised:      revisedPrompt,
		Provider:     bound.AppSlug,
		Model:        model,
		Size:         size,
		DurationMs:   totalDurationMs,
		StorageIDs:   storageIDs,
		UpstreamURLs: upstreamURLs,
		ThumbnailB64: firstThumbB64,
		ExtraJSON:    extraJSON,
		Count:        len(media),
	})

	ctx.Emit("media.generated", map[string]any{
		"kind": kind, "prompt": prompt, "model": model, "count": len(media),
	})

	return buildMCPResult(buildResultArgs{
		Kind:          kind,
		Prompt:        prompt,
		Revised:       revisedPrompt,
		Model:         model,
		Provider:      bound.AppSlug,
		ProjectID:     pid,
		StorageIDs:    storageIDs,
		UpstreamURLs:  upstreamURLs,
		FirstThumbB64: firstThumbB64,
		Count:         len(media),
		MimeType:      media[0].MimeType,
	}), nil
}

// encodeExtras stashes per-kind args that aren't first-class columns
// (voice, aspect, options.*, edit lineage) into the row's extra_json
// blob. Best-effort; failure to encode just drops the metadata.
func encodeExtras(kind string, args map[string]any) string {
	extras := map[string]any{}
	for _, k := range []string{"voice", "aspect", "duration", "n"} {
		if v, ok := args[k]; ok {
			extras[k] = v
		}
	}
	if opts, ok := args["options"].(map[string]any); ok && len(opts) > 0 {
		extras["options"] = opts
	}
	// Edit-flow lineage: the original source_image reference (e.g.
	// "storage:1234") + the capability so history can render
	// "edit of #1234" without re-deriving from the resolved bytes.
	if ref, ok := args["_source_image_ref"].(string); ok && ref != "" {
		extras["source_image_ref"] = ref
		extras["capability"] = "image.edit"
	}
	if len(extras) == 0 {
		return "{}"
	}
	b, err := json.Marshal(extras)
	if err != nil {
		return "{}"
	}
	return string(b)
}

// resolveSourceImage takes the raw source_image arg (one of: "storage:N",
// a URL, or a base64 string) and returns the value the per-provider
// builder will pass through. "storage:N" fetches via files_get_content;
// URLs + bare base64 are passed through unchanged.
func resolveSourceImage(ctx *sdk.AppCtx, raw string) (string, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return "", errors.New("required (got empty)")
	}
	if strings.HasPrefix(raw, "storage:") {
		idStr := strings.TrimPrefix(raw, "storage:")
		id, err := parseInt64(idStr)
		if err != nil {
			return "", errors.New("malformed storage handle: " + raw)
		}
		var got struct {
			ContentBase64 string `json:"content_base64"`
		}
		if err := ctx.PlatformAPI().CallAppResult("storage", "files_get_content",
			map[string]any{"id": id}, &got); err != nil {
			return "", errors.New("storage fetch failed: " + err.Error())
		}
		if got.ContentBase64 == "" {
			return "", errors.New("storage returned empty content for id " + idStr)
		}
		return got.ContentBase64, nil
	}
	// URL or already-base64 — pass through unchanged.
	return raw, nil
}

func parseInt64(s string) (int64, error) {
	var n int64
	for _, c := range s {
		if c < '0' || c > '9' {
			return 0, errors.New("not a positive integer")
		}
		n = n*10 + int64(c-'0')
	}
	if n == 0 && s != "0" {
		return 0, errors.New("empty")
	}
	return n, nil
}

// ─── HTTP /generate — panel hand-off ───────────────────────────────

func (a *App) handleGenerate(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	if globalCtx == nil {
		http.Error(w, "app not mounted", http.StatusServiceUnavailable)
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if _, ok := body["kind"]; !ok {
		http.Error(w, "kind required", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(strArg(body, "prompt", "")) == "" {
		http.Error(w, "prompt required", http.StatusBadRequest)
		return
	}
	// Project context is fixed by the sidecar's APTEVA_PROJECT_ID env —
	// each install gets its own sidecar with its own project — so we
	// ignore body.project_id rather than mutating env at request time.
	out, err := a.toolMediaGenerate(globalCtx, body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// ─── HTTP /bindings — panel role-status ────────────────────────────

// handleBindings reports which roles have a bound integration so the
// panel can render badges per tab ("Image ✓" / "Video — not bound").
// Returns: { image: {bound, slug?}, video: {bound, slug?}, … }.
func (a *App) handleBindings(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if globalCtx == nil {
		http.Error(w, "app not mounted", http.StatusServiceUnavailable)
		return
	}
	out := map[string]any{}
	for kind, h := range handlers {
		entry := map[string]any{"bound": false}
		if b := globalCtx.IntegrationFor(h.Role); b != nil {
			entry["bound"] = true
			entry["slug"] = b.AppSlug
			// Default-capability support — what an empty-args call would
			// route to (image.generate for the image kind, etc).
			defaultCap := h.ResolveCapability(map[string]any{})
			entry["default_capability"] = defaultCap
			entry["capability_supported"] = b.ToolFor(defaultCap) != ""
			// For the image kind, also surface whether edit is bound.
			if kind == KindImage {
				entry["edit_supported"] = b.ToolFor("image.edit") != ""
			}
		}
		out[kind] = entry
	}
	// Storage doesn't have a kind row — surface separately.
	storageEntry := map[string]any{"bound": false}
	if b := globalCtx.IntegrationFor("storage"); b != nil {
		storageEntry["bound"] = true
		storageEntry["app"] = b.AppName
	}
	out["storage"] = storageEntry

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// errKindStub is returned by per-kind builders/normalizers that
// haven't been wired up yet. Surfaces as a clean mcpError so the
// agent sees a usable message.
var errKindStub = errors.New("kind not yet wired — provider integration pending")
