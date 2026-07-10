package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// Avatar (talking-head) generation. Tavus and HeyGen are both adapted
// behind the same media-studio kind. HeyGen uses its v3 video API for
// both saved avatar looks and one-off image-to-video when source_image
// is provided.
//
// Both providers are async: create returns a handle (Tavus video_id /
// HeyGen video_id); the worker polls the provider's get-status tool and
// fetches the finished bytes from a download URL. That handle rides the
// same video_jobs table + worker as the video kind (kind='avatar').

// avatarToolForSlug maps the avatar.generate capability to the
// provider's actual create tool. The names now align for Tavus and
// HeyGen, but keeping this resolver makes the dispatch rule explicit.
func avatarToolForSlug(slug, capability string) string {
	switch slug {
	case "tavus":
		return "create_video"
	case "heygen":
		return "create_video"
	}
	return ""
}

// buildAvatarArgs assembles the provider's create-video request from
// the unified args. prompt carries the spoken script; `avatar` carries
// the replica/avatar id; `voice` is an optional override (required for
// HeyGen, baked into the replica for Tavus).
func buildAvatarArgs(args map[string]any, providerSlug, capability string) (map[string]any, error) {
	switch providerSlug {
	case "tavus":
		return buildTavusAvatarArgs(args)
	case "heygen":
		return buildHeyGenAvatarArgs(args)
	}
	return nil, fmt.Errorf("unsupported avatar provider slug: %q", providerSlug)
}

// buildHeyGenAvatarArgs → POST /v3/videos. With avatar set it renders
// an existing HeyGen avatar look; with source_image and no avatar it
// renders a one-off image-to-video talking head. Provider-specific
// controls ride in options while the public media-studio surface stays
// provider-neutral.
func buildHeyGenAvatarArgs(args map[string]any) (map[string]any, error) {
	avatarID := strArg(args, "avatar", "")
	sourceImage := strArg(args, "source_image", "")
	script := strArg(args, "prompt", "")
	if script == "" {
		return nil, errors.New("prompt (the spoken script) required")
	}
	voiceID := strArg(args, "voice", "")
	opts, _ := args["options"].(map[string]any)

	res := "1080p"
	aspect := "16:9"
	if opts != nil {
		if v := strArg(opts, "resolution", ""); v != "" {
			res = v
		}
		if v := strArg(opts, "aspect", ""); v != "" {
			aspect = v
		}
		if v := strArg(opts, "aspect_ratio", ""); v != "" {
			aspect = v
		}
	}

	out := map[string]any{
		"script":       script,
		"resolution":   res,
		"aspect_ratio": aspect,
	}
	if avatarID != "" {
		out["type"] = "avatar"
		out["avatar_id"] = avatarID
	} else if sourceImage != "" {
		if voiceID == "" {
			return nil, errors.New("voice (HeyGen voice_id) required for image-to-video — pick one from /voices")
		}
		out["type"] = "image"
		out["image"] = heygenAssetInput(sourceImage)
	} else {
		return nil, errors.New("avatar (HeyGen avatar_id) or source_image required")
	}
	if voiceID != "" {
		out["voice_id"] = voiceID
	}

	if opts != nil {
		for _, k := range []string{
			"title", "background", "remove_background", "callback_url",
			"callback_id", "watermark", "caption", "output_format", "fit",
			"voice_settings", "audio_url", "audio_asset_id",
		} {
			if v, exists := opts[k]; exists {
				out[k] = v
			}
		}
		if v, exists := opts["engine"]; exists {
			if engine := heygenEngineConfig(v); engine != nil {
				out["engine"] = engine
			}
		}
		if !heygenIsAvatarV(out["engine"]) {
			for _, k := range []string{"motion_prompt", "expressiveness"} {
				if v, exists := opts[k]; exists {
					out[k] = v
				}
			}
		}
		if out["audio_url"] != nil || out["audio_asset_id"] != nil {
			delete(out, "script")
			delete(out, "voice_id")
		}
	}

	return out, nil
}

// heygenDimension maps resolution + aspect to pixel width/height.
func heygenDimension(resolution, aspect string) (int, int) {
	long, short := 1280, 720 // 720p
	if resolution == "1080p" {
		long, short = 1920, 1080
	}
	if aspect == "9:16" {
		return short, long // portrait
	}
	return long, short // 16:9 landscape
}

func heygenAssetInput(ref string) map[string]any {
	if len(ref) >= 5 && (ref[:5] == "http:" || ref[:5] == "https") {
		return map[string]any{"type": "url", "url": ref}
	}
	if len(ref) >= 5 && ref[:5] == "data:" {
		if idx := strings.Index(ref, ","); idx >= 0 {
			ref = ref[idx+1:]
		}
	}
	return map[string]any{"type": "base64", "data": ref}
}

func heygenEngineConfig(v any) map[string]any {
	switch t := v.(type) {
	case string:
		if t == "" || t == "auto" {
			return nil
		}
		return map[string]any{"type": t}
	case map[string]any:
		if strArg(t, "type", "") == "" || strArg(t, "type", "") == "auto" {
			return nil
		}
		return t
	}
	return nil
}

func heygenIsAvatarV(v any) bool {
	m, ok := v.(map[string]any)
	return ok && strArg(m, "type", "") == "avatar_v"
}

func validateHeyGenAvatarEngine(ctx *sdk.AppCtx, bound *sdk.BoundIntegration, args map[string]any) error {
	opts, _ := args["options"].(map[string]any)
	if opts == nil || !heygenIsAvatarV(heygenEngineConfig(opts["engine"])) {
		return nil
	}
	avatarID := strArg(args, "avatar", "")
	if avatarID == "" {
		// Image-to-video with Avatar V is validated by HeyGen because
		// there is no reusable look id to preflight.
		return nil
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, "get_avatar_look",
		map[string]any{"look_id": avatarID})
	if err != nil {
		return err
	}
	if res == nil || !res.Success {
		return errors.New("get_avatar_look non-2xx")
	}
	var body struct {
		Data struct {
			SupportedAPIEngines []string `json:"supported_api_engines"`
		} `json:"data"`
	}
	if err := json.Unmarshal(res.Data, &body); err != nil {
		return err
	}
	for _, engine := range body.Data.SupportedAPIEngines {
		if engine == "avatar_v" {
			return nil
		}
	}
	return errors.New("selected avatar look does not advertise supported_api_engines=[avatar_v]")
}

// buildTavusAvatarArgs → POST /v2/videos. replica_id + script required;
// options.* passes through Tavus extras (background_url, fast,
// transparent_background, watermark_image_url, properties).
func buildTavusAvatarArgs(args map[string]any) (map[string]any, error) {
	replica := strArg(args, "avatar", "")
	if replica == "" {
		return nil, errors.New("avatar (Tavus replica_id) required — pick one from /avatars or list_replicas")
	}
	script := strArg(args, "prompt", "")
	if script == "" {
		return nil, errors.New("prompt (the spoken script) required")
	}
	out := map[string]any{
		"replica_id": replica,
		"script":     script,
	}
	if opts, ok := args["options"].(map[string]any); ok {
		passThrough := []string{
			"video_name", "background_url", "background_source_url",
			"fast", "transparent_background", "watermark_image_url", "properties",
		}
		for _, k := range passThrough {
			if v, exists := opts[k]; exists {
				out[k] = v
			}
		}
	}
	return out, nil
}

// normalizeAvatarResponse parses the provider's create-video response
// into a generatedMedia carrying the job handle (video_id) in
// UpstreamURL — the same convention the video kind uses so the
// dispatcher's async branch can reuse handleAsyncQueueResponse.
func normalizeAvatarResponse(slug, capability string, raw json.RawMessage) ([]generatedMedia, string, string, error) {
	switch slug {
	case "tavus":
		var body struct {
			VideoID   string `json:"video_id"`
			VideoName string `json:"video_name"`
			Status    string `json:"status"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, "", "", err
		}
		if body.VideoID == "" {
			return nil, "", "", fmt.Errorf("tavus create response missing video_id: %s", truncate(string(raw), 200))
		}
		return []generatedMedia{{
			UpstreamURL: body.VideoID, // job handle, re-used as the poll key
			MimeType:    "video/mp4",
			Ext:         "mp4",
		}}, "", "tavus-replica", nil
	case "heygen":
		// HeyGen wraps the result: {error, data:{video_id}}.
		var body struct {
			Data struct {
				VideoID string `json:"video_id"`
			} `json:"data"`
			Error any `json:"error"`
		}
		if err := json.Unmarshal(raw, &body); err != nil {
			return nil, "", "", err
		}
		if body.Data.VideoID == "" {
			return nil, "", "", fmt.Errorf("heygen create response missing data.video_id: %s", truncate(string(raw), 200))
		}
		return []generatedMedia{{
			UpstreamURL: body.Data.VideoID,
			MimeType:    "video/mp4",
			Ext:         "mp4",
		}}, "", "heygen-avatar", nil
	}
	return nil, "", "", fmt.Errorf("unsupported avatar provider slug: %q", slug)
}

// --- avatar resource discovery (the panel's replica/avatar picker) ---

type avatarEntry struct {
	ID                  string   `json:"id"`
	Name                string   `json:"name"`
	Thumbnail           string   `json:"thumbnail,omitempty"`
	ThumbnailType       string   `json:"thumbnail_type,omitempty"`
	Status              string   `json:"status,omitempty"`
	DefaultVoiceID      string   `json:"default_voice_id,omitempty"`
	SupportedAPIEngines []string `json:"supported_api_engines,omitempty"`
	AvatarType          string   `json:"avatar_type,omitempty"`
	Ownership           string   `json:"ownership,omitempty"`
}

// handleListAvatars → GET /avatars. Calls the bound avatar_provider's
// list tool and normalizes to [{id, name, thumbnail}]. Empty list when
// no provider is bound.
func (a *App) handleListAvatars(w http.ResponseWriter, r *http.Request) {
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
	resp := map[string]any{"bound": false, "avatars": []avatarEntry{}}
	bound := ctx.IntegrationFor("avatar_provider")
	if bound != nil {
		resp["bound"] = true
		resp["provider"] = bound.AppSlug
		avatars, err := listAvatarsFor(ctx, bound)
		if err != nil {
			resp["error"] = err.Error()
		} else {
			resp["avatars"] = avatars
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func listAvatarsFor(ctx *sdk.AppCtx, bound *sdk.BoundIntegration) ([]avatarEntry, error) {
	switch bound.AppSlug {
	case "tavus":
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, "list_replicas",
			map[string]any{"limit": 100, "verbose": true})
		if err != nil {
			return nil, err
		}
		if res == nil || !res.Success {
			return nil, errors.New("tavus list_replicas non-2xx")
		}
		var body struct {
			Data []struct {
				ReplicaID         string `json:"replica_id"`
				ReplicaName       string `json:"replica_name"`
				ThumbnailVideoURL string `json:"thumbnail_video_url"`
				Status            string `json:"status"`
			} `json:"data"`
		}
		if err := json.Unmarshal(res.Data, &body); err != nil {
			return nil, err
		}
		out := make([]avatarEntry, 0, len(body.Data))
		for _, r := range body.Data {
			if r.ReplicaID == "" {
				continue
			}
			out = append(out, avatarEntry{
				ID:            r.ReplicaID,
				Name:          r.ReplicaName,
				Thumbnail:     r.ThumbnailVideoURL,
				ThumbnailType: "video",
				Status:        r.Status,
				Ownership:     "private",
			})
		}
		return out, nil
	case "heygen":
		return listHeyGenAvatarLooks(ctx, bound)
	}
	return nil, fmt.Errorf("unsupported avatar provider: %s", bound.AppSlug)
}

type heygenAvatarLook struct {
	ID                  string   `json:"id"`
	Name                string   `json:"name"`
	PreviewImageURL     string   `json:"preview_image_url"`
	PreviewVideoURL     string   `json:"preview_video_url"`
	Status              string   `json:"status"`
	DefaultVoiceID      string   `json:"default_voice_id"`
	SupportedAPIEngines []string `json:"supported_api_engines"`
	AvatarType          string   `json:"avatar_type"`
	Ownership           string   `json:"ownership"`
}

func listHeyGenAvatarLooks(ctx *sdk.AppCtx, bound *sdk.BoundIntegration) ([]avatarEntry, error) {
	out := []avatarEntry{}
	seen := map[string]bool{}
	for _, ownership := range []string{"private", "public"} {
		items, err := fetchHeyGenAvatarLookPages(ctx, bound, ownership)
		if err != nil {
			items, err = fetchHeyGenAvatarLookPages(ctx, bound, "")
			if err != nil {
				return nil, err
			}
		}
		for _, av := range items {
			if av.ID == "" || seen[av.ID] {
				continue
			}
			seen[av.ID] = true
			if av.Ownership == "" {
				av.Ownership = ownership
			}
			out = append(out, normalizeHeyGenAvatarLook(av))
		}
	}
	return out, nil
}

func fetchHeyGenAvatarLookPages(ctx *sdk.AppCtx, bound *sdk.BoundIntegration, ownership string) ([]heygenAvatarLook, error) {
	var out []heygenAvatarLook
	token := ""
	for page := 0; page < 3; page++ {
		req := map[string]any{"limit": 50}
		if ownership != "" {
			req["ownership"] = ownership
		}
		if token != "" {
			req["token"] = token
		}
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, "list_avatar_looks", req)
		if err != nil {
			return nil, err
		}
		if res == nil || !res.Success {
			return nil, errors.New("heygen list_avatar_looks non-2xx")
		}
		items, next, err := parseHeyGenAvatarLookPage(res.Data)
		if err != nil {
			return nil, err
		}
		out = append(out, items...)
		if next == "" {
			break
		}
		token = next
	}
	return out, nil
}

func parseHeyGenAvatarLookPage(raw json.RawMessage) ([]heygenAvatarLook, string, error) {
	var body struct {
		Data       json.RawMessage `json:"data"`
		Token      string          `json:"token"`
		NextToken  string          `json:"next_token"`
		Next       string          `json:"next"`
		Pagination struct {
			Token     string `json:"token"`
			NextToken string `json:"next_token"`
			Next      string `json:"next"`
		} `json:"pagination"`
	}
	if err := json.Unmarshal(raw, &body); err != nil {
		return nil, "", err
	}
	next := firstNonEmpty(body.NextToken, body.Token, body.Next, body.Pagination.NextToken, body.Pagination.Token, body.Pagination.Next)
	var items []heygenAvatarLook
	if len(body.Data) == 0 {
		return items, next, nil
	}
	if err := json.Unmarshal(body.Data, &items); err == nil {
		return items, next, nil
	}
	var wrapped struct {
		Looks      []heygenAvatarLook `json:"looks"`
		Avatars    []heygenAvatarLook `json:"avatars"`
		Items      []heygenAvatarLook `json:"items"`
		Token      string             `json:"token"`
		NextToken  string             `json:"next_token"`
		Pagination struct {
			Token     string `json:"token"`
			NextToken string `json:"next_token"`
			Next      string `json:"next"`
		} `json:"pagination"`
	}
	if err := json.Unmarshal(body.Data, &wrapped); err != nil {
		return nil, "", err
	}
	if len(wrapped.Looks) > 0 {
		items = wrapped.Looks
	} else if len(wrapped.Avatars) > 0 {
		items = wrapped.Avatars
	} else {
		items = wrapped.Items
	}
	next = firstNonEmpty(next, wrapped.NextToken, wrapped.Token, wrapped.Pagination.NextToken, wrapped.Pagination.Token, wrapped.Pagination.Next)
	return items, next, nil
}

func normalizeHeyGenAvatarLook(av heygenAvatarLook) avatarEntry {
	thumb := av.PreviewVideoURL
	thumbType := "video"
	if thumb == "" {
		thumb = av.PreviewImageURL
		thumbType = "image"
	}
	return avatarEntry{
		ID:                  av.ID,
		Name:                av.Name,
		Thumbnail:           thumb,
		ThumbnailType:       thumbType,
		Status:              av.Status,
		DefaultVoiceID:      av.DefaultVoiceID,
		SupportedAPIEngines: av.SupportedAPIEngines,
		AvatarType:          av.AvatarType,
		Ownership:           av.Ownership,
	}
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// --- voices (avatar + audio providers) -----------------------------

type voiceEntry struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Language string `json:"language,omitempty"`
	Gender   string `json:"gender,omitempty"`
	Preview  string `json:"preview,omitempty"`
}

// handleListVoices → GET /voices. Returns the bound provider's voice
// catalog. ?kind=audio_tts reads audio_provider; default/avatar reads
// avatar_provider. Empty for Tavus (voice is part of the replica).
func (a *App) handleListVoices(w http.ResponseWriter, r *http.Request) {
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
	resp := map[string]any{"voices": []voiceEntry{}}
	role := "avatar_provider"
	if r.URL.Query().Get("kind") == KindAudioTTS {
		role = "audio_provider"
	}
	bound := ctx.IntegrationFor(role)
	if bound != nil {
		resp["provider"] = bound.AppSlug
		voices, err := listVoicesFor(ctx, bound)
		if err != nil {
			resp["error"] = err.Error()
		} else {
			resp["voices"] = voices
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func listVoicesFor(ctx *sdk.AppCtx, bound *sdk.BoundIntegration) ([]voiceEntry, error) {
	switch bound.AppSlug {
	case "heygen":
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, "list_voices",
			map[string]any{"limit": 100})
		if err != nil {
			return nil, err
		}
		if res == nil || !res.Success {
			return nil, errors.New("heygen list_voices non-2xx")
		}
		var body struct {
			Data []struct {
				VoiceID         string `json:"voice_id"`
				Name            string `json:"name"`
				Language        string `json:"language"`
				Gender          string `json:"gender"`
				PreviewAudioURL string `json:"preview_audio_url"`
			} `json:"data"`
		}
		if err := json.Unmarshal(res.Data, &body); err != nil {
			return nil, err
		}
		out := make([]voiceEntry, 0, len(body.Data))
		for _, v := range body.Data {
			if v.VoiceID == "" {
				continue
			}
			out = append(out, voiceEntry{
				ID: v.VoiceID, Name: v.Name, Language: v.Language,
				Gender: v.Gender, Preview: v.PreviewAudioURL,
			})
		}
		return out, nil
	case "elevenlabs":
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, "list_voices",
			map[string]any{"page_size": 100, "include_total_count": true})
		if err != nil {
			return nil, err
		}
		if res == nil || !res.Success {
			return nil, errors.New("elevenlabs list_voices non-2xx")
		}
		var body struct {
			Voices []struct {
				VoiceID    string         `json:"voice_id"`
				Name       string         `json:"name"`
				PreviewURL string         `json:"preview_url"`
				Labels     map[string]any `json:"labels"`
			} `json:"voices"`
		}
		if err := json.Unmarshal(res.Data, &body); err != nil {
			return nil, err
		}
		out := make([]voiceEntry, 0, len(body.Voices))
		for _, v := range body.Voices {
			if v.VoiceID == "" {
				continue
			}
			language := ""
			gender := ""
			if v.Labels != nil {
				language = fmt.Sprint(v.Labels["language"])
				if language == "<nil>" {
					language = ""
				}
				gender = fmt.Sprint(v.Labels["gender"])
				if gender == "<nil>" {
					gender = ""
				}
			}
			out = append(out, voiceEntry{
				ID: v.VoiceID, Name: v.Name, Language: language,
				Gender: gender, Preview: v.PreviewURL,
			})
		}
		return out, nil
	case "tavus":
		// Voice is part of the Tavus replica — no separate voice list.
		return []voiceEntry{}, nil
	}
	return nil, fmt.Errorf("unsupported avatar provider: %s", bound.AppSlug)
}
