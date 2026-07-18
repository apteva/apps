package main

import (
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

type storageUploadInitResult struct {
	UploadID    string         `json:"upload_id"`
	PartSize    int64          `json:"part_size"`
	WasExisting bool           `json:"was_existing"`
	File        map[string]any `json:"file"`
}

func storageUploadInit(ctx *sdk.AppCtx, pid, name, folder, contentType string, size int64) (*storageUploadInitResult, error) {
	if name == "" || size <= 0 {
		return nil, errors.New("storage upload init: name + positive size required")
	}
	full := storageRoot(ctx)
	if folder = strings.Trim(folder, "/"); folder != "" {
		full += "/" + folder
	}
	args := map[string]any{
		"name":       name,
		"size_bytes": size,
		"folder":     full,
		"visibility": "private",
		"source":     "gigs-worker",
	}
	if contentType != "" {
		args["content_type"] = contentType
	}
	var got storageUploadInitResult
	if err := ctx.WithProject(pid).PlatformAPI().CallAppResult("storage", "storage_upload_init", args, &got); err != nil {
		return nil, fmt.Errorf("storage.storage_upload_init: %w", err)
	}
	return &got, nil
}

func storageUploadPart(ctx *sdk.AppCtx, pid, uploadID string, partNumber int, contentBase64 string) error {
	args := map[string]any{
		"upload_id":      uploadID,
		"part_number":    partNumber,
		"content_base64": contentBase64,
	}
	var got map[string]any
	if err := ctx.WithProject(pid).PlatformAPI().CallAppResult("storage", "storage_upload_part", args, &got); err != nil {
		return fmt.Errorf("storage.storage_upload_part: %w", err)
	}
	return nil
}

func storageUploadComplete(ctx *sdk.AppCtx, pid, uploadID string) (int64, error) {
	var got struct {
		File map[string]any `json:"file"`
	}
	if err := ctx.WithProject(pid).PlatformAPI().CallAppResult("storage", "storage_upload_complete", map[string]any{"upload_id": uploadID}, &got); err != nil {
		return 0, fmt.Errorf("storage.storage_upload_complete: %w", err)
	}
	id := int64Cast(got.File["id"])
	if id == 0 {
		return 0, errors.New("storage upload completed without a file id")
	}
	return id, nil
}

func storageUploadAbort(ctx *sdk.AppCtx, pid, uploadID, reason string) error {
	var got map[string]any
	if err := ctx.WithProject(pid).PlatformAPI().CallAppResult("storage", "storage_abort_upload", map[string]any{
		"id": uploadID, "reason": reason,
	}, &got); err != nil {
		return fmt.Errorf("storage.storage_abort_upload: %w", err)
	}
	return nil
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
