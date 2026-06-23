package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type StorageUploadResult struct {
	ID        int64  `json:"id"`
	URL       string `json:"url"`
	SHA256    string `json:"sha256"`
	SizeBytes int64  `json:"size_bytes"`
	Folder    string `json:"folder"`
	Name      string `json:"name"`
}

func uploadToStorage(app *sdk.AppCtx, name, folder, contentType string, body []byte) (*StorageUploadResult, error) {
	if app == nil || app.PlatformAPI() == nil {
		return nil, errors.New("books: no platform client; cannot reach storage")
	}
	if name == "" {
		return nil, errors.New("upload name required")
	}
	if folder == "" {
		folder = "/books/"
	}
	if !strings.HasPrefix(folder, "/") {
		folder = "/" + folder
	}
	if !strings.HasSuffix(folder, "/") {
		folder += "/"
	}
	args := map[string]any{
		"name":           name,
		"folder":         folder,
		"content_base64": base64.StdEncoding.EncodeToString(body),
		"source":         "books-export",
	}
	if contentType != "" {
		args["content_type"] = contentType
	}
	var out StorageUploadResult
	if err := app.PlatformAPI().CallAppResult("storage", "files_upload", args, &out); err != nil {
		return nil, fmt.Errorf("storage.files_upload: %w", err)
	}
	if out.ID == 0 {
		return nil, errors.New("storage returned id=0")
	}
	return &out, nil
}
