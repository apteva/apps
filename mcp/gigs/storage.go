package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// Thin wrappers around the storage app's MCP tools. Like CRM, every
// call goes through ctx.WithProject(pid) so global-scope storage
// installs work without manual `_project_id` plumbing.

// storageRoot returns the configured root folder (e.g. "/.gigs") for
// gig media + submissions, dotted by convention so it stays out of
// the storage dashboard's default view.
func storageRoot(ctx *sdk.AppCtx) string {
	if v := ctx.Config().Get("storage_root_folder"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return "/.gigs"
}

// storageUpload posts bytes to storage. `folder` is a sub-path
// appended to the root (e.g. "submissions/42").
func storageUpload(ctx *sdk.AppCtx, pid, name, folder, contentType string, body []byte) (fileID int64, signedURL string, err error) {
	if name == "" || len(body) == 0 {
		return 0, "", errors.New("storage upload: name + body required")
	}
	folder = strings.TrimLeft(folder, "/")
	full := storageRoot(ctx)
	if folder != "" {
		full = full + "/" + folder
	}
	args := map[string]any{
		"name":           name,
		"folder":         full,
		"content_base64": base64.StdEncoding.EncodeToString(body),
	}
	if contentType != "" {
		args["content_type"] = contentType
	}
	var got struct {
		ID  int64  `json:"id"`
		URL string `json:"url"`
	}
	if err := ctx.WithProject(pid).PlatformAPI().CallAppResult("storage", "files_upload", args, &got); err != nil {
		return 0, "", fmt.Errorf("storage.files_upload: %w", err)
	}
	return got.ID, got.URL, nil
}

// storageSignedURL mints a TTL-bounded fetch URL for the worker page.
func storageSignedURL(ctx *sdk.AppCtx, pid string, fileID int64, ttlSeconds int) (string, error) {
	if fileID == 0 {
		return "", errors.New("file_id required")
	}
	args := map[string]any{"id": fileID}
	if ttlSeconds > 0 {
		args["ttl_seconds"] = ttlSeconds
	}
	var got struct {
		URL string `json:"url"`
	}
	if err := ctx.WithProject(pid).PlatformAPI().CallAppResult("storage", "files_get_url", args, &got); err != nil {
		return "", fmt.Errorf("storage.files_get_url(%d): %w", fileID, err)
	}
	if got.URL == "" {
		return "", fmt.Errorf("storage.files_get_url(%d) returned empty url", fileID)
	}
	return got.URL, nil
}
