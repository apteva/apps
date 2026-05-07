package main

// Wrapper around app-sdk's CallApp("storage", ...) for the one
// thing docs needs from storage: upload PDF bytes and get back a
// file_id + URL.
//
// We use CallApp (the app-to-app MCP path) rather than direct HTTP
// to storage's REST endpoints so the platform's permission system
// (per-(install, instance) grants) gates docs's access to storage
// the same way it gates everything else. Storage's files.write
// permission is the relevant scope.

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"

	sdk "github.com/apteva/app-sdk"
)

// StorageUploadResult mirrors the subset of storage's files_upload
// response we need. URL is absolute (storage v0.8+) and ready to
// share with end users.
type StorageUploadResult struct {
	ID        int64  `json:"id"`
	URL       string `json:"url"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
	Folder    string `json:"folder"`
	Name      string `json:"name"`
}

// uploadToStorage POSTs the PDF bytes via files_upload. CallApp
// returns a JSON-RPC envelope; we unwrap one layer to get the
// tool's result map.
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

	args := map[string]any{
		"name":           name,
		"folder":         folder,
		"content_base64": base64.StdEncoding.EncodeToString(body),
	}
	if contentType != "" {
		args["content_type"] = contentType
	}

	raw, err := app.PlatformAPI().CallApp("storage", "files_upload", args)
	if err != nil {
		return nil, fmt.Errorf("storage.files_upload: %w", err)
	}
	return parseStorageUploadResult(raw)
}

// parseStorageUploadResult — strip the JSON-RPC envelope that
// CallApp returns. Two shapes seen in the wild:
//
//	1. Full envelope: {"jsonrpc":..., "result":{"content":[{"text":"<json>"}]}}
//	2. Bare inner JSON (some test paths, older platforms)
//
// Tries 1 first; falls through to 2. Lets docs's tests hand-roll
// either shape without branching.
func parseStorageUploadResult(raw json.RawMessage) (*StorageUploadResult, error) {
	if len(raw) == 0 {
		return nil, errors.New("storage returned empty response")
	}
	// Envelope shape.
	var env struct {
		Result *struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}
	if err := json.Unmarshal(raw, &env); err == nil {
		if env.Error != nil {
			return nil, fmt.Errorf("storage.files_upload: %s (code=%d)", env.Error.Message, env.Error.Code)
		}
		if env.Result != nil && len(env.Result.Content) > 0 && env.Result.Content[0].Text != "" {
			var out StorageUploadResult
			if err := json.Unmarshal([]byte(env.Result.Content[0].Text), &out); err == nil && out.ID > 0 {
				return &out, nil
			}
		}
	}
	// Bare-JSON fallback.
	var out StorageUploadResult
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode storage response: %w (raw: %.200s)", err, string(raw))
	}
	if out.ID == 0 {
		return nil, fmt.Errorf("storage response had no id: %.200s", string(raw))
	}
	return &out, nil
}
