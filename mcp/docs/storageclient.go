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
