package main

import (
	"encoding/base64"
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func storageDownload(ctx *sdk.AppCtx, fileID int64) (StorageFile, []byte, error) {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return StorageFile{}, nil, errors.New("storage is unavailable")
	}
	if fileID == 0 {
		return StorageFile{}, nil, errors.New("source_file_id required")
	}
	var file StorageFile
	if err := ctx.PlatformAPI().CallAppResult("storage", "files_get_content", map[string]any{"id": fileID}, &file); err != nil {
		return StorageFile{}, nil, fmt.Errorf("storage.files_get_content: %w", err)
	}
	if file.ID == 0 {
		return StorageFile{}, nil, errors.New("storage returned an empty file record")
	}
	body, err := base64.StdEncoding.DecodeString(file.ContentBase64)
	if err != nil {
		return StorageFile{}, nil, fmt.Errorf("decode storage content: %w", err)
	}
	if len(body) == 0 {
		return StorageFile{}, nil, errors.New("source PDF is empty")
	}
	return file, body, nil
}

func storageUpload(ctx *sdk.AppCtx, name, folder, contentType string, body []byte) (StorageUpload, error) {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return StorageUpload{}, errors.New("storage is unavailable")
	}
	if len(body) == 0 {
		return StorageUpload{}, errors.New("cannot upload an empty artifact")
	}
	args := map[string]any{
		"name":           name,
		"folder":         folder,
		"content_type":   contentType,
		"content_base64": base64.StdEncoding.EncodeToString(body),
		"source":         "signatures",
		"visibility":     "private",
	}
	var out StorageUpload
	if err := ctx.PlatformAPI().CallAppResult("storage", "files_upload", args, &out); err != nil {
		return StorageUpload{}, fmt.Errorf("storage.files_upload: %w", err)
	}
	if out.ID == 0 {
		return StorageUpload{}, errors.New("storage upload returned id=0")
	}
	if out.SHA256 == "" {
		out.SHA256 = bytesHash(body)
	}
	return out, nil
}

func sourcePDF(ctx *sdk.AppCtx, fileID int64) (StorageFile, []byte, int, error) {
	file, body, err := storageDownload(ctx, fileID)
	if err != nil {
		return StorageFile{}, nil, 0, err
	}
	if !strings.EqualFold(file.ContentType, "application/pdf") && !strings.HasSuffix(strings.ToLower(file.Name), ".pdf") {
		return StorageFile{}, nil, 0, errors.New("source file must be a PDF")
	}
	if len(body) < 5 || string(body[:5]) != "%PDF-" {
		return StorageFile{}, nil, 0, errors.New("source file does not contain a valid PDF header")
	}
	pageCount, err := pdfPageCount(body)
	if err != nil {
		return StorageFile{}, nil, 0, fmt.Errorf("validate source PDF: %w", err)
	}
	if pageCount < 1 || pageCount > 100 {
		return StorageFile{}, nil, 0, fmt.Errorf("source PDF has %d pages; supported range is 1 to 100", pageCount)
	}
	return file, body, pageCount, nil
}
