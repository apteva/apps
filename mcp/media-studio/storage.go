package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// generatedMedia is the uniform shape every per-kind normalizer
// returns. Providers populate either UpstreamURL or B64; the storage
// hand-off picks the right tool (files_from_url vs files_upload).
type generatedMedia struct {
	UpstreamURL string // populated for URL-shape providers (DALL·E default, most video/audio CDNs)
	B64         string // populated for inline-bytes providers (gpt-image-*, some TTS responses)
	MimeType    string // image/png, image/jpeg, video/mp4, audio/mpeg, …
	Ext         string // png, jpeg, mp4, mp3, wav, webp
	DurationMs  int64  // video / audio / music only
}

// mediaBytes returns the raw bytes for a generated item regardless of
// which shape the provider used. B64 wins when both are present
// (cheaper, no extra round-trip to the provider's CDN).
func mediaBytes(m generatedMedia) ([]byte, error) {
	if m.B64 != "" {
		return base64.StdEncoding.DecodeString(m.B64)
	}
	if m.UpstreamURL != "" {
		return fetchBytes(m.UpstreamURL)
	}
	return nil, errors.New("media has neither b64 nor URL")
}

// saveToStorage hands a generated item off to the storage app. For URL
// responses we use files_from_url so storage fetches its own bytes
// (cheaper, no double-buffering); for inline base64 we use files_upload
// and pass the b64 string through unchanged.
//
// All media lands under /.generated/<storageDir>/ — the dotted-folder
// convention so storage panels hide app-internal output by default.
func saveToStorage(ctx *sdk.AppCtx, m generatedMedia, storageDir, providerSlug string, idx int) (int64, error) {
	ext := m.Ext
	if ext == "" {
		ext = "bin"
	}
	contentType := m.MimeType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	folder := "/.generated/" + storageDir + "/"
	name := fmt.Sprintf("media-%d-%d.%s", time.Now().Unix(), idx, ext)
	tags := []string{"ai", "generated", providerSlug}

	var got struct {
		ID int64 `json:"id"`
	}
	if m.B64 != "" {
		if err := ctx.PlatformAPI().CallAppResult("storage", "files_upload", map[string]any{
			"name":           name,
			"content_base64": m.B64,
			"folder":         folder,
			"content_type":   contentType,
			"tags":           tags,
		}, &got); err != nil {
			return 0, err
		}
		return got.ID, nil
	}
	if m.UpstreamURL != "" {
		if err := ctx.PlatformAPI().CallAppResult("storage", "files_from_url", map[string]any{
			"url":    m.UpstreamURL,
			"folder": folder,
			"name":   name,
			"tags":   tags,
		}, &got); err != nil {
			return 0, err
		}
		return got.ID, nil
	}
	return 0, errors.New("no media source")
}

// pickExt maps a requested output_format to a file extension. PNG is
// the universal image default; jpeg/webp only ever come back from
// gpt-image-* when explicitly requested. Kept for the image tests'
// table-driven coverage.
func pickExt(outputFormat string) string {
	switch outputFormat {
	case "jpeg", "jpg":
		return "jpg"
	case "webp":
		return "webp"
	}
	return "png"
}

// storageContentURL returns the relative URL the dashboard / MCP host
// can fetch to stream the saved bytes back. Routed through the platform
// proxy at /api/apps/storage/* (auth via the host's session); media-studio
// itself never needs to mint a signed URL for this path.
func storageContentURL(id int64, projectID string) string {
	return fmt.Sprintf("/api/apps/storage/files/%d/content?project_id=%s", id, projectID)
}
