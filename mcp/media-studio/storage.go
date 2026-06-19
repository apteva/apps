package main

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path"
	"strconv"
	"strings"
	"time"
	"unicode"

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
// By default, media lands under /.generated/<storageDir>/ — the
// dotted-folder convention so storage panels hide app-internal output by
// default. Callers may override this with storage_folder.
func saveToStorage(ctx *sdk.AppCtx, m generatedMedia, folder, providerSlug string, idx int) (int64, error) {
	ext := m.Ext
	if ext == "" {
		ext = "bin"
	}
	contentType := m.MimeType
	if contentType == "" {
		contentType = "application/octet-stream"
	}
	name := fmt.Sprintf("media-%d-%d.%s", time.Now().Unix(), idx, ext)
	tags := []string{"ai", "generated", providerSlug}

	var got struct {
		ID int64 `json:"id"`
	}
	if m.B64 != "" {
		if err := ctx.PlatformAPI().CallAppResult("storage", "files_upload", storageArgs(ctx, map[string]any{
			"name":           name,
			"content_base64": m.B64,
			"folder":         folder,
			"content_type":   contentType,
			"tags":           tags,
		}), &got); err != nil {
			return 0, err
		}
		return got.ID, nil
	}
	if m.UpstreamURL != "" {
		if err := ctx.PlatformAPI().CallAppResult("storage", "files_from_url", storageArgs(ctx, map[string]any{
			"url":    m.UpstreamURL,
			"folder": folder,
			"name":   name,
			"tags":   tags,
		}), &got); err != nil {
			return 0, err
		}
		return got.ID, nil
	}
	return 0, errors.New("no media source")
}

func defaultStorageFolder(storageDir string) string {
	return "/.generated/" + strings.Trim(strings.TrimSpace(storageDir), "/") + "/"
}

func storageFolderArg(args map[string]any, storageDir string) (string, error) {
	raw := strings.TrimSpace(strArg(args, "storage_folder", ""))
	if raw == "" {
		return defaultStorageFolder(storageDir), nil
	}
	return normalizeStorageFolder(raw)
}

func normalizeStorageFolder(raw string) (string, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return "", errors.New("storage_folder is empty")
	}
	for _, r := range s {
		if unicode.IsControl(r) {
			return "", errors.New("storage_folder contains control characters")
		}
	}
	parts := strings.Split(s, "/")
	for _, part := range parts {
		if part == ".." {
			return "", errors.New("storage_folder must not contain ..")
		}
	}
	if !strings.HasPrefix(s, "/") {
		s = "/" + s
	}
	clean := path.Clean(s)
	if clean == "." || clean == "/" {
		return "/", nil
	}
	if !strings.HasPrefix(clean, "/") {
		clean = "/" + clean
	}
	return clean + "/", nil
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
	return fmt.Sprintf("/api/apps/storage/files/%d/content?project_id=%s", id, url.QueryEscape(projectID))
}

func storageArgs(ctx *sdk.AppCtx, args map[string]any) map[string]any {
	pid := projectScope(ctx)
	if pid == "" {
		return args
	}
	cp := make(map[string]any, len(args)+1)
	for k, v := range args {
		cp[k] = v
	}
	if _, ok := cp["_project_id"]; !ok {
		cp["_project_id"] = pid
	}
	return cp
}

func writeJSON(w http.ResponseWriter, v any, err error) {
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusBadRequest)
		_ = json.NewEncoder(w).Encode(map[string]any{"error": err.Error()})
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func intQuery(r *http.Request, key string, def int) int {
	if s := strings.TrimSpace(r.URL.Query().Get(key)); s != "" {
		if n, err := strconv.Atoi(s); err == nil {
			return n
		}
	}
	return def
}

func boolQuery(r *http.Request, key string, def bool) bool {
	if s := strings.TrimSpace(r.URL.Query().Get(key)); s != "" {
		return s == "1" || strings.EqualFold(s, "true") || strings.EqualFold(s, "yes")
	}
	return def
}

func filterStorageBrowserOutput(out map[string]any) {
	if out == nil {
		return
	}
	raw, ok := out["files"].([]any)
	if !ok {
		return
	}
	filtered := make([]any, 0, len(raw))
	for _, item := range raw {
		m, ok := item.(map[string]any)
		if !ok || storageBrowserHidden(m) {
			continue
		}
		filtered = append(filtered, item)
	}
	out["files"] = filtered
	if _, ok := out["count"]; ok {
		out["count"] = len(filtered)
	}
}

func storageBrowserHidden(m map[string]any) bool {
	folder := strings.TrimSpace(fmt.Sprint(m["folder"]))
	name := strings.TrimSpace(fmt.Sprint(m["name"]))
	return pathHasDotSegment(folder) || strings.HasPrefix(name, ".")
}

func pathHasDotSegment(p string) bool {
	for _, seg := range strings.Split(p, "/") {
		if strings.HasPrefix(seg, ".") {
			return true
		}
	}
	return false
}
