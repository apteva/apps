package main

import (
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func projectScope(ctx *sdk.AppCtx) string {
	if ctx != nil {
		if pid := strings.TrimSpace(ctx.CurrentProject()); pid != "" {
			return pid
		}
	}
	return strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID"))
}

func projectArg(args map[string]any) string {
	if v := strings.TrimSpace(strArg(args, "_project_id", "")); v != "" {
		return v
	}
	return strings.TrimSpace(strArg(args, "project_id", ""))
}

func projectScopeFromArgs(ctx *sdk.AppCtx, args map[string]any) string {
	if pid := projectArg(args); pid != "" {
		return pid
	}
	return projectScope(ctx)
}

func withProjectScope(ctx *sdk.AppCtx, args map[string]any) *sdk.AppCtx {
	if ctx == nil {
		return nil
	}
	if pid := projectScopeFromArgs(ctx, args); pid != "" {
		return ctx.WithProject(pid)
	}
	return ctx
}

const (
	KindImage    = "image"
	KindVideo    = "video"
	KindAudioTTS = "audio_tts"
	KindAudioSFX = "audio_sfx"
	KindMusic    = "music"
	KindAvatar   = "avatar"
)

// asyncKinds render off-thread at the provider — the tool returns a
// queued job and a worker polls for completion. Both share the
// video_jobs table + poll worker (rows discriminated by the kind
// column added in migration 004).
var asyncKinds = map[string]bool{
	KindVideo:  true,
	KindAvatar: true,
}

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
	// ResolveTool optionally overrides the manifest's tool name per
	// provider slug. Needed when compatible providers name the same
	// capability's tool differently. nil → use bound.ToolFor(capability).
	ResolveTool func(slug, capability string) string
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

// resolveImageCapability picks image.edit when the caller supplied one or
// more source images (source_image or source_images); image.generate otherwise.
func resolveImageCapability(args map[string]any) string {
	if len(sourceImageRefs(args)) > 0 {
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
	KindAvatar: {
		Role:              "avatar_provider",
		ResolveCapability: constCap("avatar.generate"),
		ResolveTool:       avatarToolForSlug,
		BuildArgs:         buildAvatarArgs,
		Normalize:         normalizeAvatarResponse,
		StorageDir:        "avatars",
	},
}

// toolMediaGenerate is the unified MCP entry point. Discriminates on
// kind, resolves the bound integration, builds the provider request,
// normalizes the response, optionally persists to storage, and shapes
// the MCP result per kind.
func (a *App) toolMediaGenerate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx = withProjectScope(ctx, args)
	if draftID := draftIDArg(args); draftID > 0 {
		loaded, err := loadDraftRequest(ctx, args, draftID)
		if err != nil {
			return mcpError("draft: " + err.Error()), nil
		}
		args = loaded
		ctx = withProjectScope(ctx, args)
	}
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
	estimatedSeconds := estimatedDurationSeconds(kind, args)
	pid := projectScope(ctx)
	if wantsDraft(args) {
		for _, ref := range sourceImageRefs(args) {
			if isInlineMediaRef(ref) {
				return mcpError("draft source images must use storage:N or an HTTP(S) URL"), nil
			}
		}
		return a.createDraftGeneration(ctx, args, kind, prompt, estimatedSeconds), nil
	}
	cacheKey := strings.TrimSpace(strArg(args, "cache_key", ""))
	cachePolicy := strings.TrimSpace(strArg(args, "cache_policy", "reuse"))
	if cacheKey != "" && cachePolicy != "refresh" && int64Arg(args, "_draft_generation_id", 0) == 0 {
		if row, err := queryGenerationByCacheKey(ctx, pid, kind, cacheKey); err == nil {
			return cachedGenerationResult(row), nil
		}
		if job, ok := queryPendingJobByCacheKey(ctx, pid, kind, cacheKey); ok {
			return job, nil
		}
	}

	capability := h.ResolveCapability(args)
	bound, err := selectBoundProvider(ctx, h, args, capability)
	if err != nil {
		return mcpError(err.Error()), nil
	}
	if bound == nil {
		return mcpError("no " + h.Role + " bound — pick one in app settings"), nil
	}
	draftID := int64Arg(args, "_draft_generation_id", 0)
	tool := bound.ToolFor(capability)
	// Per-slug tool override — for kinds where compatible providers name
	// the same capability's tool differently (avatar).
	if h.ResolveTool != nil {
		if t := h.ResolveTool(bound.AppSlug, capability); t != "" {
			tool = t
		}
	}
	if tool == "" {
		return mcpError("bound " + h.Role + " (" + bound.AppSlug + ") doesn't support " + capability), nil
	}
	storageFolder, err := storageFolderArg(args, h.StorageDir)
	if err != nil {
		return mcpError("storage_folder: " + err.Error()), nil
	}
	args["_storage_folder"] = storageFolder
	if kind == KindVideo && bound.AppSlug == "venice-ai" {
		normalizeVeniceVideoDurationForModel(ctx, args, capability)
		estimatedSeconds = estimatedDurationSeconds(kind, args)
	}
	if err := validateProviderPrompt(ctx, bound, kind, capability, args); err != nil {
		return mcpError("prompt: " + err.Error()), nil
	}
	requestedOutputFormat := ""
	if kind == KindImage {
		requestedOutputFormat, err = requestedImageOutputFormat(args)
		if err != nil {
			return mcpError(err.Error()), nil
		}
		canonicalizeImageOutputFormat(args, requestedOutputFormat)
	}
	requestJSON := generationRequestJSON(args)

	// Source images — resolve "storage:N" / URL / base64 into the
	// bytes-or-URL values the per-provider builder will pass through.
	// Used by image.edit (single-image /image/edit and Venice multi-edit)
	// and video.generate image-to-video. Original refs are preserved for
	// history and cache lineage.
	if refs := sourceImageRefs(args); len(refs) > 0 {
		maxRefs := maxSourceImagesFor(bound.AppSlug, capability, strArg(args, "model", ""))
		if maxRefs > 0 && len(refs) > maxRefs {
			return mcpError("model supports at most " + strconv.Itoa(maxRefs) + " source image(s), got " + strconv.Itoa(len(refs))), nil
		}
		resolved := make([]string, 0, len(refs))
		for _, orig := range refs {
			one, err := resolveSourceImage(ctx, orig)
			if err != nil {
				return mcpError("source_images: " + err.Error()), nil
			}
			resolved = append(resolved, one)
		}
		args["_source_image_refs"] = refs
		args["source_images"] = resolved
		args["_source_image_ref"] = refs[0]
		args["source_image"] = resolved[0]
		if kind == KindImage && capability == "image.edit" && bound.AppSlug == "venice-ai" && len(resolved) > 1 {
			tool = "multi_edit_image"
		}
	}

	if kind == KindAvatar && bound.AppSlug == "heygen" {
		if err := validateHeyGenAvatarEngine(ctx, bound, args); err != nil {
			return mcpError("heygen avatar: " + err.Error()), nil
		}
	}

	providerArgs, err := h.BuildArgs(args, bound.AppSlug, capability)
	if err != nil {
		return mcpError("build args: " + err.Error()), nil
	}
	updateGenerationStatus(ctx, pid, draftID, "generating")

	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, tool, providerArgs)
	if err != nil {
		updateGenerationStatus(ctx, pid, draftID, "failed")
		return mcpError("provider call failed: " + err.Error()), nil
	}
	if res == nil || !res.Success {
		body := ""
		if res != nil {
			body = string(res.Data)
		}
		updateGenerationStatus(ctx, pid, draftID, "failed")
		return mcpError("provider returned non-2xx: " + body), nil
	}

	media, revisedPrompt, normalizedModel, err := h.Normalize(bound.AppSlug, capability, res.Data)
	if err != nil {
		updateGenerationStatus(ctx, pid, draftID, "failed")
		return mcpError("provider response parse: " + err.Error()), nil
	}
	if len(media) == 0 {
		updateGenerationStatus(ctx, pid, draftID, "failed")
		return mcpError("provider returned zero items"), nil
	}
	providerRequestID := providerRequestIDFromHeaders(res.Headers)

	// Async kinds (video, avatar) return a job handle, not bytes.
	// Short-circuit the sync save pipeline — the worker (worker.go)
	// takes over the polling + storage save.
	if asyncKinds[kind] {
		queueID := media[0].UpstreamURL
		modelEcho := normalizedModel
		if modelEcho == "" {
			modelEcho = strArg(args, "model", "")
		}
		args["_draft_generation_id"] = draftID
		args["_request_json"] = requestJSON
		return a.handleAsyncQueueResponse(ctx, kind, h.Role, bound.AppSlug, bound.ConnectionID, args, queueID, modelEcho), nil
	}

	model := strArg(args, "model", "")
	if model == "" {
		model = normalizedModel
	}

	storage := ctx.IntegrationFor("storage")
	storageIDs := make([]int64, 0, len(media))
	upstreamURLs := make([]string, 0, len(media))
	var firstThumbB64 string
	var firstBody []byte
	var firstExt string
	firstStored := false
	storageWarnings := []string{}
	var totalDurationMs int64
	var totalActualSeconds float64

	for i, item := range media {
		upstreamURLs = append(upstreamURLs, item.UpstreamURL)
		totalDurationMs += item.DurationMs
		totalActualSeconds += mediaActualDurationSeconds(item)

		body, err := mediaBytes(item)
		if err != nil {
			ctx.Logger().Warn("fetch media bytes failed", "url", item.UpstreamURL, "err", err)
			continue
		}
		if kind == KindImage {
			item, body, err = enforceImageOutputFormat(item, body, requestedOutputFormat)
			if err != nil {
				updateGenerationStatus(ctx, pid, draftID, "failed")
				return mcpError("output_format: " + err.Error()), nil
			}
		} else {
			item = withSniffedImageMediaType(item, body)
		}
		media[i] = item
		if i == 0 {
			firstBody = append([]byte(nil), body...)
			firstExt = item.Ext
		}
		if h.MakeThumbnail && i == 0 {
			if thumb := makeThumbnail(body, 256); thumb != nil {
				firstThumbB64 = base64.StdEncoding.EncodeToString(thumb)
			}
		}

		if storage != nil {
			id, err := saveToStorage(ctx, item, storageFolder, bound.AppSlug, i)
			if err != nil {
				ctx.Logger().Warn("storage save failed", "err", err)
				storageWarnings = append(storageWarnings, fmt.Sprintf("item %d: %v", i, err))
				continue
			}
			if id != 0 {
				storageIDs = append(storageIDs, id)
				if i == 0 {
					firstStored = true
				}
			}
		}
	}

	size := strArg(args, "size", "")
	extraJSON := encodeExtras(kind, args)
	if providerRequestID != "" {
		extraJSON = addExtraJSONField(extraJSON, "provider_request_id", providerRequestID)
	}
	if len(storageWarnings) > 0 {
		extraJSON = addExtraJSONField(extraJSON, "storage_warning", strings.Join(storageWarnings, "; "))
	}
	costUSD := computeGenerationCost(ctx, bound, kind, capability, model, args)
	record := generationRecord{
		ProjectID:                pid,
		Kind:                     kind,
		Prompt:                   prompt,
		Revised:                  revisedPrompt,
		Provider:                 bound.AppSlug,
		Model:                    model,
		Size:                     size,
		DurationMs:               totalDurationMs,
		StorageIDs:               storageIDs,
		UpstreamURLs:             upstreamURLs,
		ThumbnailB64:             firstThumbB64,
		ExtraJSON:                extraJSON,
		Count:                    len(media),
		CostUSD:                  costUSD,
		CacheKey:                 cacheKey,
		EstimatedDurationSeconds: estimatedSeconds,
		ActualDurationSeconds:    totalActualSeconds,
		Status:                   "ready",
		RequestJSON:              requestJSON,
	}
	var genID int64
	if draftID > 0 {
		if a.dbUpdateGeneration(record, draftID) {
			genID = draftID
		}
	}
	if genID == 0 {
		genID = a.dbInsertGeneration(record)
	}

	// Keep a full local copy whenever the first item did not reach Storage.
	// Only the first item — multi-variant cases will need a richer
	// cache key if/when we render more than one image per row.
	if !firstStored && genID > 0 && len(firstBody) > 0 {
		if err := writeLocalCache(genID, base64.StdEncoding.EncodeToString(firstBody), firstExt); err != nil {
			ctx.Logger().Warn("writeLocalCache failed", "gen_id", genID, "err", err)
		}
	}

	ctx.Emit("media.generated", map[string]any{
		"kind": kind, "prompt": prompt, "model": model, "count": len(media), "storage_folder": storageFolder,
	})

	return buildMCPResult(buildResultArgs{
		Kind:                     kind,
		Prompt:                   prompt,
		Revised:                  revisedPrompt,
		Model:                    model,
		Provider:                 bound.AppSlug,
		ProjectID:                pid,
		StorageIDs:               storageIDs,
		UpstreamURLs:             upstreamURLs,
		FirstThumbB64:            firstThumbB64,
		Count:                    len(media),
		MimeType:                 media[0].MimeType,
		CostUSD:                  costUSD,
		GenerationID:             genID,
		ProviderRequestID:        providerRequestID,
		StorageFolder:            storageFolder,
		EstimatedDurationSeconds: estimatedSeconds,
		ActualDurationSeconds:    totalActualSeconds,
	}), nil
}

func selectBoundProvider(ctx *sdk.AppCtx, h kindHandler, args map[string]any, capability string) (*sdk.BoundIntegration, error) {
	if h.Role != "image_provider" && h.Role != "audio_provider" {
		return ctx.IntegrationFor(h.Role), nil
	}
	bounds := boundIntegrationsFor(ctx, h.Role)
	if len(bounds) == 0 {
		return nil, nil
	}
	provider := ""
	if h.Role == "audio_provider" {
		var err error
		provider, err = normalizeAudioProviderArgs(args)
		if err != nil {
			return nil, err
		}
	} else if parsed, stripped, ok := splitProviderModel(strArg(args, "model", "")); ok {
		provider = parsed
		args["model"] = stripped
	}
	if provider != "" {
		for _, bound := range bounds {
			if bound != nil && bound.AppSlug == provider && providerSupportsCapability(bound.AppSlug, capability) {
				return bound, nil
			}
		}
		return nil, fmt.Errorf("provider %s is not bound for %s or does not support %s", provider, h.Role, capability)
	}
	for _, bound := range bounds {
		if bound != nil && providerSupportsCapability(bound.AppSlug, capability) {
			return bound, nil
		}
	}
	if h.Role == "audio_provider" {
		return nil, fmt.Errorf("bound audio providers do not support %s", capability)
	}
	return nil, nil
}

func normalizeAudioProviderArgs(args map[string]any) (string, error) {
	provider := strings.TrimSpace(strArg(args, "provider", ""))
	selectedBy := "provider"
	for _, key := range []string{"model", "voice", "voice_id"} {
		value := strArg(args, key, "")
		parsed, stripped, ok := splitProviderModel(value)
		if !ok || (parsed != "elevenlabs" && parsed != "fish-audio") {
			continue
		}
		if provider != "" && provider != parsed {
			return "", fmt.Errorf("provider mismatch: %s selects %s but %s selects %s", key, parsed, selectedBy, provider)
		}
		provider = parsed
		selectedBy = key
		args[key] = stripped
	}
	return provider, nil
}

func splitProviderModel(model string) (string, string, bool) {
	model = strings.TrimSpace(model)
	if model == "" {
		return "", "", false
	}
	for _, sep := range []string{":", "/"} {
		parts := strings.SplitN(model, sep, 2)
		if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
			continue
		}
		switch parts[0] {
		case "openai-api", "openai-codex", "venice-ai", "gemini", "elevenlabs", "fish-audio":
			return parts[0], parts[1], true
		}
	}
	return "", model, false
}

func providerSupportsCapability(provider, capability string) bool {
	if strings.HasPrefix(capability, "image.") {
		return imageProviderSupports(provider, capability)
	}
	return audioProviderSupports(provider, capability)
}

func audioProviderSupports(provider, capability string) bool {
	switch capability {
	case "audio.tts", "voice.create":
		return provider == "elevenlabs" || provider == "fish-audio"
	case "audio.sfx":
		return provider == "elevenlabs"
	}
	return false
}

func imageProviderSupports(provider, capability string) bool {
	switch capability {
	case "image.generate":
		switch provider {
		case "openai-api", "openai-codex", "venice-ai", "gemini":
			return true
		}
	case "image.edit":
		switch provider {
		case "openai-api", "venice-ai", "gemini":
			return true
		}
	}
	return false
}

func wantsDraft(args map[string]any) bool {
	mode := strings.ToLower(strings.TrimSpace(strArg(args, "mode", "")))
	return mode == "draft" || mode == "defer" || boolArg(args, "defer", false)
}

func draftIDArg(args map[string]any) int64 {
	if id := int64Arg(args, "draft_id", 0); id > 0 {
		return id
	}
	return int64Arg(args, "generation_id", 0)
}

func (a *App) createDraftGeneration(ctx *sdk.AppCtx, args map[string]any, kind, prompt string, estimatedSeconds float64) map[string]any {
	pid := projectScope(ctx)
	h := handlers[kind]
	provider := ""
	if bound := ctx.IntegrationFor(h.Role); bound != nil {
		provider = bound.AppSlug
	}
	costUSD := 0.0
	if estimate, err := a.estimateGeneration(ctx, args); err == nil && estimate.Available {
		costUSD = estimate.CostUSD
	}
	model := strArg(args, "model", "")
	extraJSON := encodeExtras(kind, args)
	requestJSON := generationRequestJSON(args)
	id := a.dbInsertGeneration(generationRecord{
		ProjectID:                pid,
		Kind:                     kind,
		Prompt:                   prompt,
		Provider:                 provider,
		Model:                    model,
		Size:                     strArg(args, "size", ""),
		DurationMs:               int64(estimatedSeconds * 1000),
		StorageIDs:               []int64{},
		UpstreamURLs:             []string{},
		ExtraJSON:                extraJSON,
		Count:                    1,
		CostUSD:                  costUSD,
		CacheKey:                 strings.TrimSpace(strArg(args, "cache_key", "")),
		EstimatedDurationSeconds: estimatedSeconds,
		Status:                   "draft",
		RequestJSON:              requestJSON,
	})
	return map[string]any{
		"content": []map[string]any{{
			"type": "text",
			"text": "Saved draft " + kind + " generation #" + strconvFormatInt(id) + ".",
		}},
		"_meta": map[string]any{
			"kind":                       kind,
			"status":                     "draft",
			"generation_id":              id,
			"model":                      model,
			"provider":                   provider,
			"cost_usd":                   costUSD,
			"cache_key":                  strings.TrimSpace(strArg(args, "cache_key", "")),
			"estimated_duration_seconds": estimatedSeconds,
		},
	}
}

func loadDraftRequest(ctx *sdk.AppCtx, args map[string]any, draftID int64) (map[string]any, error) {
	row, err := queryGenerationByID(ctx, projectScope(ctx), draftID)
	if err != nil {
		return nil, err
	}
	status := strAny(row["status"])
	if status == "ready" {
		return nil, errors.New("generation is already ready")
	}
	if status == "queued" || status == "generating" {
		return nil, errors.New("generation is already " + status)
	}
	reqJSON := strAny(row["request_json"])
	req := map[string]any{}
	if reqJSON != "" {
		_ = json.Unmarshal([]byte(reqJSON), &req)
	}
	if len(req) == 0 {
		req["kind"] = row["kind"]
		req["prompt"] = row["prompt"]
		req["model"] = row["model"]
	}
	for k, v := range args {
		if k == "draft_id" || k == "generation_id" {
			continue
		}
		req[k] = v
	}
	req["project_id"] = projectScope(ctx)
	req["_project_id"] = projectScope(ctx)
	req["_draft_generation_id"] = draftID
	delete(req, "mode")
	delete(req, "defer")
	return req, nil
}

func generationRequestJSON(args map[string]any) string {
	req := map[string]any{}
	for k, v := range args {
		if strings.HasPrefix(k, "_") {
			continue
		}
		switch k {
		case "mode", "defer", "draft_id", "generation_id":
			continue
		}
		req[k] = persistedRequestValue(k, v)
	}
	if _, ok := req["project_id"]; !ok {
		if v := strArg(args, "_project_id", ""); v != "" {
			req["project_id"] = v
		}
	}
	b, err := json.Marshal(req)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func persistedRequestValue(key string, value any) any {
	switch v := value.(type) {
	case string:
		if isMediaReferenceKey(key) && isInlineMediaRef(v) {
			sum := sha256.Sum256([]byte(v))
			return fmt.Sprintf("inline:sha256:%x:chars:%d", sum[:8], len(v))
		}
		return v
	case []string:
		out := make([]string, len(v))
		for i, item := range v {
			out[i] = fmt.Sprint(persistedRequestValue(key, item))
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			out[i] = persistedRequestValue(key, item)
		}
		return out
	case map[string]any:
		out := make(map[string]any, len(v))
		for childKey, child := range v {
			out[childKey] = persistedRequestValue(childKey, child)
		}
		return out
	default:
		return value
	}
}

func isMediaReferenceKey(key string) bool {
	key = strings.ToLower(strings.TrimSpace(key))
	return strings.Contains(key, "image") || strings.Contains(key, "video") ||
		strings.Contains(key, "audio") || strings.Contains(key, "file")
}

func isInlineMediaRef(ref string) bool {
	ref = strings.TrimSpace(ref)
	if strings.HasPrefix(ref, "data:") {
		return true
	}
	if strings.HasPrefix(ref, "storage:") || strings.HasPrefix(ref, "http://") || strings.HasPrefix(ref, "https://") {
		return false
	}
	return len(ref) > 256
}

func persistedMediaRef(ref string) string {
	return fmt.Sprint(persistedRequestValue("source_image", ref))
}

func persistedMediaRefs(refs []string) []string {
	out := make([]string, len(refs))
	for i, ref := range refs {
		out[i] = persistedMediaRef(ref)
	}
	return out
}

func cachedGenerationResult(row map[string]any) map[string]any {
	storageIDs, _ := row["storage_ids"].([]int64)
	storageURLs, _ := row["storage_urls"].([]string)
	upstreamURLs, _ := row["upstream_urls"].([]string)
	kind := strAny(row["kind"])
	prompt := strAny(row["prompt"])
	model := strAny(row["model"])
	provider := strAny(row["provider"])
	costUSD := floatAny(row["cost_usd"])
	estimatedSeconds := floatAny(row["estimated_duration_seconds"])
	actualSeconds := floatAny(row["actual_duration_seconds"])
	providerRequestID := providerRequestIDFromExtraJSON(strAny(row["extra_json"]))
	count := int(int64Any(row["count"]))
	if count <= 0 {
		count = 1
	}
	content := []map[string]any{{
		"type": "text",
		"text": "Reused cached " + kind + " generation #" + strconvFormatInt(int64Any(row["id"])) + ".",
	}}
	for i, url := range storageURLs {
		name := "storage"
		if i < len(storageIDs) {
			name = "storage:" + strconvFormatInt(storageIDs[i])
		}
		content = append(content, map[string]any{
			"type": "resource",
			"resource": map[string]any{
				"uri":      url,
				"mimeType": defaultMime(kind),
				"name":     name,
			},
		})
	}
	return map[string]any{
		"content": content,
		"_meta": map[string]any{
			"kind":                       kind,
			"prompt":                     prompt,
			"revised_prompt":             strAny(row["revised_prompt"]),
			"model":                      model,
			"provider":                   provider,
			"storage_ids":                storageIDs,
			"storage_urls":               storageURLs,
			"upstream_urls":              upstreamURLs,
			"cost_usd":                   costUSD,
			"count":                      count,
			"generation_id":              int64Any(row["id"]),
			"provider_request_id":        providerRequestID,
			"cache_hit":                  true,
			"cache_key":                  strAny(row["cache_key"]),
			"estimated_duration_seconds": estimatedSeconds,
			"actual_duration_seconds":    actualSeconds,
		},
	}
}

func queryPendingJobByCacheKey(ctx *sdk.AppCtx, pid, kind, cacheKey string) (map[string]any, bool) {
	var (
		id                        int64
		queueID, model, prompt    string
		costUSD, estimatedSeconds float64
	)
	err := ctx.AppDB().QueryRow(
		`SELECT id, queue_id, model, prompt, cost_usd, estimated_duration_seconds
		 FROM video_jobs
		 WHERE project_id = ? AND kind = ? AND cache_key = ?
		   AND status IN ('queued', 'polling')
		 ORDER BY id DESC LIMIT 1`,
		pid, kind, cacheKey,
	).Scan(&id, &queueID, &model, &prompt, &costUSD, &estimatedSeconds)
	if err != nil {
		return nil, false
	}
	return map[string]any{
		"content": []map[string]any{{
			"type": "text",
			"text": kind + " generation already queued as job #" + strconvFormatInt(id) + ".",
		}},
		"_meta": map[string]any{
			"kind":                       kind,
			"status":                     "queued",
			"job_id":                     id,
			"queue_id":                   queueID,
			"model":                      model,
			"prompt":                     prompt,
			"cost_usd":                   costUSD,
			"cache_hit":                  true,
			"cache_key":                  cacheKey,
			"estimated_duration_seconds": estimatedSeconds,
		},
	}, true
}

func strAny(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}

func int64Any(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	}
	return 0
}

func floatAny(v any) float64 {
	switch n := v.(type) {
	case float64:
		return n
	case int64:
		return float64(n)
	case int:
		return float64(n)
	}
	return 0
}

func strconvFormatInt(n int64) string {
	return strconv.FormatInt(n, 10)
}

// encodeExtras stashes per-kind args that aren't first-class columns
// (voice, aspect, options.*, edit lineage) into the row's extra_json
// blob. Best-effort; failure to encode just drops the metadata.
func encodeExtras(kind string, args map[string]any) string {
	extras := map[string]any{}
	for _, k := range []string{"voice", "aspect", "duration", "n", "storage_folder"} {
		if v, ok := args[k]; ok {
			extras[k] = v
		}
	}
	if v, ok := args["_storage_folder"]; ok {
		extras["storage_folder"] = v
	}
	if opts, ok := args["options"].(map[string]any); ok && len(opts) > 0 {
		extras["options"] = persistedRequestValue("options", opts)
	}
	// Edit-flow lineage: the original source image references (e.g.
	// "storage:1234") + the capability so history can render edit
	// provenance without re-deriving from the resolved bytes.
	if refs, ok := args["_source_image_refs"].([]string); ok && len(refs) > 0 {
		safeRefs := persistedMediaRefs(refs)
		extras["source_image_refs"] = safeRefs
		if len(refs) == 1 {
			extras["source_image_ref"] = safeRefs[0]
		}
		extras["capability"] = "image.edit"
	} else if ref, ok := args["_source_image_ref"].(string); ok && ref != "" {
		safeRef := persistedMediaRef(ref)
		extras["source_image_ref"] = safeRef
		extras["source_image_refs"] = []string{safeRef}
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

func addExtraJSONField(raw, key string, value any) string {
	if strings.TrimSpace(key) == "" || value == nil {
		return raw
	}
	extras := map[string]any{}
	if strings.TrimSpace(raw) != "" {
		_ = json.Unmarshal([]byte(raw), &extras)
	}
	if extras == nil {
		extras = map[string]any{}
	}
	extras[key] = value
	b, err := json.Marshal(extras)
	if err != nil {
		if raw == "" {
			return "{}"
		}
		return raw
	}
	return string(b)
}

func providerRequestIDFromHeaders(headers map[string]string) string {
	for k, v := range headers {
		if strings.EqualFold(k, "request-id") ||
			strings.EqualFold(k, "x-request-id") ||
			strings.EqualFold(k, "x-elevenlabs-request-id") ||
			strings.EqualFold(k, "xi-request-id") {
			if s := strings.TrimSpace(v); s != "" {
				return s
			}
		}
	}
	return ""
}

func providerRequestIDFromExtraJSON(raw string) string {
	if strings.TrimSpace(raw) == "" {
		return ""
	}
	var extras map[string]any
	if err := json.Unmarshal([]byte(raw), &extras); err != nil {
		return ""
	}
	return strings.TrimSpace(strAny(extras["provider_request_id"]))
}

// computeGenerationCost looks up Venice's per-model rate (cached or
// freshly fetched) and returns the USD cost for this generation.
// Returns 0 for providers without published rates (openai-api today)
// or when the model isn't in the cache after a refresh attempt.
func computeGenerationCost(ctx *sdk.AppCtx, bound *sdk.BoundIntegration, kind, capability, model string, args map[string]any) float64 {
	if bound == nil || bound.AppSlug != "venice-ai" || model == "" {
		return 0
	}
	// Map media-studio capability → Venice's model type bucket. Edit
	// models live under type=inpaint; generate under type=image. Video
	// has its own path (cost stored on video_jobs at queue time).
	veniceType := ""
	switch capability {
	case "image.generate":
		veniceType = "image"
	case "image.edit":
		veniceType = "inpaint"
	default:
		return 0
	}
	// Try cache first; fetch on miss.
	specRaw, ok := getVeniceModelSpec(bound.ConnectionID, veniceType, model)
	if !ok {
		ensureVeniceSpecLoaded(ctx, bound.ConnectionID, veniceType)
		specRaw, ok = getVeniceModelSpec(bound.ConnectionID, veniceType, model)
	}
	if !ok {
		return 0
	}
	cost, _ := computeVeniceImageCost(specRaw, capability, args)
	return cost
}

func sourceImageRefs(args map[string]any) []string {
	out := []string{}
	seen := map[string]bool{}
	add := func(v string) {
		v = strings.TrimSpace(v)
		if v == "" || seen[v] {
			return
		}
		seen[v] = true
		out = append(out, v)
	}
	add(strArg(args, "source_image", ""))
	switch v := args["source_images"].(type) {
	case []string:
		for _, s := range v {
			add(s)
		}
	case []any:
		for _, raw := range v {
			if s, ok := raw.(string); ok {
				add(s)
			}
		}
	case string:
		add(v)
	}
	return out
}

func maxSourceImagesFor(providerSlug, capability, model string) int {
	switch capability {
	case "image.edit":
		switch providerSlug {
		case "venice-ai":
			return 3
		case "openai-api":
			if strings.EqualFold(model, "dall-e-2") {
				return 1
			}
			return 16
		case "gemini":
			return 5
		}
		return 1
	case "video.generate":
		if providerSlug == "venice-ai" {
			m := strings.ToLower(model)
			if strings.Contains(m, "reference-to-video") {
				return 9
			}
			if strings.Contains(m, "image-to-video") {
				return 1
			}
		}
		return 1
	}
	return 0
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
			storageArgs(ctx, map[string]any{"id": id}), &got); err != nil {
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
	hasDraftID := draftIDArg(body) > 0
	if _, ok := body["kind"]; !ok && !hasDraftID {
		http.Error(w, "kind required", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(strArg(body, "prompt", "")) == "" && !hasDraftID {
		http.Error(w, "prompt required", http.StatusBadRequest)
		return
	}
	if pid := strings.TrimSpace(r.URL.Query().Get("project_id")); pid != "" && projectArg(body) == "" {
		body["project_id"] = pid
	}
	pid := projectScopeFromArgs(globalCtx, body)
	if pid == "" {
		http.Error(w, "project_id required", http.StatusBadRequest)
		return
	}
	body["_project_id"] = pid
	out, err := a.toolMediaGenerate(globalCtx.WithProject(pid), body)
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
	ctx, _, err := projectContextFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	out := map[string]any{}
	for kind, h := range handlers {
		entry := map[string]any{"bound": false}
		var b *sdk.BoundIntegration
		defaultCap := h.ResolveCapability(map[string]any{})
		if kind == KindImage || h.Role == "audio_provider" {
			providers := []map[string]any{}
			for _, bound := range boundIntegrationsFor(ctx, h.Role) {
				if bound == nil || !providerSupportsCapability(bound.AppSlug, defaultCap) {
					continue
				}
				providers = append(providers, map[string]any{
					"connection_id": bound.ConnectionID,
					"slug":          bound.AppSlug,
					"default":       b == nil,
				})
				if b == nil {
					b = bound
				}
			}
			if len(providers) > 0 {
				entry["providers"] = providers
			}
		} else {
			b = ctx.IntegrationFor(h.Role)
		}
		if b != nil {
			entry["bound"] = true
			entry["slug"] = b.AppSlug
			// Default-capability support — what an empty-args call would
			// route to (image.generate for the image kind, etc).
			entry["default_capability"] = defaultCap
			capabilitySupported := b.ToolFor(defaultCap) != ""
			if kind == KindImage || h.Role == "audio_provider" {
				capabilitySupported = capabilitySupported && providerSupportsCapability(b.AppSlug, defaultCap)
			}
			entry["capability_supported"] = capabilitySupported
			// For the image kind, also surface whether edit is bound.
			if kind == KindImage {
				entry["edit_supported"] = b.ToolFor("image.edit") != ""
			}
		}
		out[kind] = entry
	}
	// Storage doesn't have a kind row — surface separately.
	storageEntry := map[string]any{"bound": false}
	if b := ctx.IntegrationFor("storage"); b != nil {
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
