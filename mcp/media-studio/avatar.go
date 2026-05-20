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

// buildAvatarArgs assembles the provider's create-video request from
// the unified args. prompt carries the spoken script; `avatar` carries
// the replica/avatar id; `voice` is an optional override.
func buildAvatarArgs(args map[string]any, providerSlug, capability string) (map[string]any, error) {
	switch providerSlug {
	case "tavus":
		return buildTavusAvatarArgs(args)
	case "heygen":
		return nil, fmt.Errorf("heygen avatar not wired in v0.6 (different create-video payload) — use Tavus")
	}
	return nil, fmt.Errorf("unsupported avatar provider slug: %q", providerSlug)
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
		return nil, errors.New("heygen avatar listing not wired in v0.6")
	}
	return nil, fmt.Errorf("unsupported avatar provider: %s", bound.AppSlug)
}
