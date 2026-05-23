package main

// Cross-app client for storage. Routed through CallApp (the platform's
// /api/apps/callback/apps/<name>/call surface) — NOT direct HTTP to
// the gateway. CallApp gates on integration_bindings, which the
// operator wires at install time when docs declares
// requires.apps[storage]. That keeps the trust boundary at the
// platform layer where it belongs: docs can't talk to a storage
// install the operator hasn't approved.
//
// If a fresh install hits "app not bound: storage", the install
// flow didn't auto-set the binding (or the operator skipped the
// confirmation). Fix is at the platform — set
// app_installs.integration_bindings to {"storage": <storage_install_id>}
// — not by switching to direct HTTP. (media still uses direct HTTP;
// that's pre-existing tech debt, not the pattern to copy.)

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// StorageUploadResult mirrors the subset of storage's files_upload
// response we use. URL is absolute (storage v0.8+).
type StorageUploadResult struct {
	ID        int64  `json:"id"`
	URL       string `json:"url"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
	Folder    string `json:"folder"`
	Name      string `json:"name"`
}

// uploadToStorage POSTs PDF bytes via the platform's CallApp. Uses
// CallAppResult so the JSON-RPC envelope is unwrapped before we get
// the inner files_upload response.
func uploadToStorage(app *sdk.AppCtx, name, folder, contentType string, body []byte) (*StorageUploadResult, error) {
	if app == nil || app.PlatformAPI() == nil {
		return nil, errors.New("docs: no platform client; cannot reach storage")
	}
	if name == "" {
		return nil, errors.New("upload name required")
	}
	if folder == "" {
		folder = "/docs/"
	}

	// content_base64 is files_upload's accepted body shape — same as
	// the dashboard uploader and the multipart fast path.
	args := map[string]any{
		"name":           name,
		"folder":         folder,
		"content_base64": base64.StdEncoding.EncodeToString(body),
		"source":         "docs-render",
	}
	if contentType != "" {
		args["content_type"] = contentType
	}

	var out StorageUploadResult
	if err := app.PlatformAPI().CallAppResult("storage", "files_upload", args, &out); err != nil {
		return nil, fmt.Errorf("storage.files_upload: %w", err)
	}
	if out.ID == 0 {
		return nil, fmt.Errorf("storage returned id=0")
	}
	return &out, nil
}

// ─── image resolution (v0.2) ──────────────────────────────────────────
//
// resolveImageSrc backs RenderOptions.ImageResolver. It maps a
// markdown image src to raw bytes + a maroto image extension. Two
// schemes are supported, both deterministic and side-effect-free for
// the renderer:
//
//	storage:<id>            → storage.files_get_content (the asset the
//	<id>                       operator uploaded once, e.g. a logo)
//	data:image/png;base64,…  → decoded inline
//
// http(s) sources are rejected on purpose: fetching arbitrary URLs at
// render time is an SSRF surface and makes renders non-deterministic.
// Lift this behind a config flag later if there's demand.
func resolveImageSrc(app *sdk.AppCtx, src string) ([]byte, string, error) {
	src = strings.TrimSpace(src)
	switch {
	case strings.HasPrefix(src, "data:"):
		return decodeDataURI(src)
	case strings.HasPrefix(strings.ToLower(src), "http://"),
		strings.HasPrefix(strings.ToLower(src), "https://"):
		return nil, "", fmt.Errorf("http(s) image sources are disabled (use storage:<id> or a data: URI): %s", src)
	case strings.HasPrefix(src, "storage:"):
		return downloadStorageImage(app, strings.TrimPrefix(src, "storage:"))
	default:
		// Bare numeric id is accepted as shorthand for storage:<id>.
		if _, err := strconv.ParseInt(src, 10, 64); err == nil {
			return downloadStorageImage(app, src)
		}
		return nil, "", fmt.Errorf("unsupported image source %q (use storage:<id> or data:)", src)
	}
}

// downloadStorageImage fetches one storage file's bytes inline via
// files_get_content (id-only; ≤25 MB). The extension is taken from the
// file's content-type, falling back to its name suffix.
func downloadStorageImage(app *sdk.AppCtx, idStr string) ([]byte, string, error) {
	if app == nil || app.PlatformAPI() == nil {
		return nil, "", errors.New("docs: no platform client; cannot reach storage")
	}
	id, err := strconv.ParseInt(strings.TrimSpace(idStr), 10, 64)
	if err != nil {
		return nil, "", fmt.Errorf("storage image ref must be storage:<numeric id>: %q", idStr)
	}
	var out struct {
		ContentBase64 string `json:"content_base64"`
		ContentType   string `json:"content_type"`
		Name          string `json:"name"`
	}
	if err := app.PlatformAPI().CallAppResult("storage", "files_get_content", map[string]any{"id": id}, &out); err != nil {
		return nil, "", fmt.Errorf("storage.files_get_content: %w", err)
	}
	data, err := base64.StdEncoding.DecodeString(out.ContentBase64)
	if err != nil {
		return nil, "", fmt.Errorf("decode storage content: %w", err)
	}
	ext := extFromContentType(out.ContentType)
	if ext == "" {
		ext = extFromName(out.Name)
	}
	return data, ext, nil
}

// decodeDataURI parses a base64 data URI: data:[<mime>][;base64],<data>.
func decodeDataURI(uri string) ([]byte, string, error) {
	comma := strings.IndexByte(uri, ',')
	if comma < 0 || !strings.HasPrefix(uri, "data:") {
		return nil, "", errors.New("malformed data URI")
	}
	meta := uri[len("data:"):comma]
	if !strings.Contains(meta, "base64") {
		return nil, "", errors.New("only base64 data URIs are supported")
	}
	data, err := base64.StdEncoding.DecodeString(uri[comma+1:])
	if err != nil {
		return nil, "", fmt.Errorf("decode data URI: %w", err)
	}
	mime := meta
	if i := strings.IndexByte(meta, ';'); i >= 0 {
		mime = meta[:i]
	}
	return data, extFromContentType(mime), nil
}

func extFromContentType(ct string) string {
	switch strings.ToLower(strings.TrimSpace(ct)) {
	case "image/png":
		return "png"
	case "image/jpeg", "image/jpg":
		return "jpg"
	}
	return ""
}

func extFromName(name string) string {
	switch n := strings.ToLower(name); {
	case strings.HasSuffix(n, ".png"):
		return "png"
	case strings.HasSuffix(n, ".jpg"), strings.HasSuffix(n, ".jpeg"):
		return "jpg"
	}
	return ""
}
