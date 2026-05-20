package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	sdk "github.com/apteva/app-sdk"
)

// Avatar (talking-head) generation. v0.6 wires Tavus; HeyGen returns
// errKindStub until v0.7 (its create-video payload is shaped
// differently — video_inputs with character + voice + background).
//
// Both providers are async: create returns a handle (Tavus video_id /
// HeyGen video_id); the worker polls the provider's get-status tool and
// fetches the finished bytes from a download URL. That handle rides the
// same video_jobs table + worker as the video kind (kind='avatar').

// avatarToolForSlug maps the avatar.generate capability to the
// provider's actual create tool — they diverge: Tavus names it
// create_video, HeyGen generate_video. The dispatcher applies this
// over the manifest's (single) tool mapping.
func avatarToolForSlug(slug, capability string) string {
	switch slug {
	case "tavus":
		return "create_video"
	case "heygen":
		return "generate_video"
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

// buildHeyGenAvatarArgs → POST /v2/videos. HeyGen wants a nested
// video_inputs array (character + voice) plus a dimension object —
// quite different from Tavus's flat replica_id+script. voice_id is
// required (the script needs a voice; HeyGen doesn't bake voice into
// the avatar the way Tavus bakes it into the replica).
func buildHeyGenAvatarArgs(args map[string]any) (map[string]any, error) {
	avatarID := strArg(args, "avatar", "")
	if avatarID == "" {
		return nil, errors.New("avatar (HeyGen avatar_id) required — pick one from /avatars")
	}
	script := strArg(args, "prompt", "")
	if script == "" {
		return nil, errors.New("prompt (the spoken script) required")
	}
	voiceID := strArg(args, "voice", "")
	if voiceID == "" {
		return nil, errors.New("voice (HeyGen voice_id) required — pick one from /voices")
	}

	res := "1080p"
	aspect := "16:9"
	var background any
	var title string
	if opts, ok := args["options"].(map[string]any); ok {
		if v := strArg(opts, "resolution", ""); v != "" {
			res = v
		}
		if v := strArg(opts, "aspect", ""); v != "" {
			aspect = v
		}
		if v, exists := opts["background"]; exists {
			background = v
		}
		title = strArg(opts, "title", "")
	}
	w, h := heygenDimension(res, aspect)

	character := map[string]any{
		"type":         "avatar",
		"avatar_id":    avatarID,
		"avatar_style": "normal",
	}
	voice := map[string]any{
		"type":       "text",
		"input_text": script,
		"voice_id":   voiceID,
	}
	if opts, ok := args["options"].(map[string]any); ok {
		if vs, exists := opts["voice_settings"]; exists {
			// Merge tuning (speed/pitch/locale) onto the voice object.
			if m, ok := vs.(map[string]any); ok {
				for k, v := range m {
					voice[k] = v
				}
			}
		}
	}
	videoInput := map[string]any{"character": character, "voice": voice}
	if background != nil {
		videoInput["background"] = background
	}
	out := map[string]any{
		"video_inputs": []any{videoInput},
		"dimension":    map[string]any{"width": w, "height": h},
	}
	if title != "" {
		out["title"] = title
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
	ID        string `json:"id"`
	Name      string `json:"name"`
	Thumbnail string `json:"thumbnail,omitempty"`
	Status    string `json:"status,omitempty"`
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
	resp := map[string]any{"bound": false, "avatars": []avatarEntry{}}
	bound := globalCtx.IntegrationFor("avatar_provider")
	if bound != nil {
		resp["bound"] = true
		resp["provider"] = bound.AppSlug
		avatars, err := listAvatarsFor(globalCtx, bound)
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
				ReplicaID          string `json:"replica_id"`
				ReplicaName        string `json:"replica_name"`
				ThumbnailVideoURL  string `json:"thumbnail_video_url"`
				Status             string `json:"status"`
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
				ID:        r.ReplicaID,
				Name:      r.ReplicaName,
				Thumbnail: r.ThumbnailVideoURL,
				Status:    r.Status,
			})
		}
		return out, nil
	case "heygen":
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, "list_avatars", map[string]any{})
		if err != nil {
			return nil, err
		}
		if res == nil || !res.Success {
			return nil, errors.New("heygen list_avatars non-2xx")
		}
		var body struct {
			Data struct {
				Avatars []struct {
					AvatarID        string `json:"avatar_id"`
					AvatarName      string `json:"avatar_name"`
					PreviewImageURL string `json:"preview_image_url"`
					PreviewVideoURL string `json:"preview_video_url"`
				} `json:"avatars"`
			} `json:"data"`
		}
		if err := json.Unmarshal(res.Data, &body); err != nil {
			return nil, err
		}
		out := make([]avatarEntry, 0, len(body.Data.Avatars))
		for _, av := range body.Data.Avatars {
			if av.AvatarID == "" {
				continue
			}
			thumb := av.PreviewVideoURL
			if thumb == "" {
				thumb = av.PreviewImageURL
			}
			out = append(out, avatarEntry{ID: av.AvatarID, Name: av.AvatarName, Thumbnail: thumb})
		}
		return out, nil
	}
	return nil, fmt.Errorf("unsupported avatar provider: %s", bound.AppSlug)
}

// --- voices (HeyGen only — Tavus bakes voice into the replica) ----

type voiceEntry struct {
	ID       string `json:"id"`
	Name     string `json:"name"`
	Language string `json:"language,omitempty"`
	Gender   string `json:"gender,omitempty"`
	Preview  string `json:"preview,omitempty"`
}

// handleListVoices → GET /voices. Returns the bound avatar provider's
// voice catalog. Empty for Tavus (voice is part of the replica).
func (a *App) handleListVoices(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	if globalCtx == nil {
		http.Error(w, "app not mounted", http.StatusServiceUnavailable)
		return
	}
	resp := map[string]any{"voices": []voiceEntry{}}
	bound := globalCtx.IntegrationFor("avatar_provider")
	if bound != nil {
		resp["provider"] = bound.AppSlug
		voices, err := listVoicesFor(globalCtx, bound)
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
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, "list_voices", map[string]any{})
		if err != nil {
			return nil, err
		}
		if res == nil || !res.Success {
			return nil, errors.New("heygen list_voices non-2xx")
		}
		var body struct {
			Data struct {
				Voices []struct {
					VoiceID      string `json:"voice_id"`
					Name         string `json:"name"`
					Language     string `json:"language"`
					Gender       string `json:"gender"`
					PreviewAudio string `json:"preview_audio"`
				} `json:"voices"`
			} `json:"data"`
		}
		if err := json.Unmarshal(res.Data, &body); err != nil {
			return nil, err
		}
		out := make([]voiceEntry, 0, len(body.Data.Voices))
		for _, v := range body.Data.Voices {
			if v.VoiceID == "" {
				continue
			}
			out = append(out, voiceEntry{
				ID: v.VoiceID, Name: v.Name, Language: v.Language,
				Gender: v.Gender, Preview: v.PreviewAudio,
			})
		}
		return out, nil
	case "tavus":
		// Voice is part of the Tavus replica — no separate voice list.
		return []voiceEntry{}, nil
	}
	return nil, fmt.Errorf("unsupported avatar provider: %s", bound.AppSlug)
}
