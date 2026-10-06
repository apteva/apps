package main

import (
	"errors"
	"fmt"
	"strconv"

	sdk "github.com/apteva/app-sdk"
)

// importPreview reads Storage under the brand root. It never creates sessions
// or links files: legacy folder names are evidence for a human to review.
func (a *App) importPreview(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := project(ctx)
	if err != nil {
		return nil, err
	}
	if err := required(args, "session_id"); err != nil {
		return nil, err
	}
	session, err := sessionByID(ctx.AppDB(), pid, str(args, "session_id"))
	if err != nil {
		return nil, err
	}
	brand, err := brandByID(ctx.AppDB(), pid, session.BrandID)
	if err != nil {
		return nil, err
	}
	bound := ctx.IntegrationFor("storage")
	if bound == nil || bound.InstallID <= 0 {
		return nil, errors.New("Storage app is not bound")
	}
	var listed struct {
		Files []struct {
			ID          int64  `json:"id"`
			Name        string `json:"name"`
			Folder      string `json:"folder"`
			ContentType string `json:"content_type"`
		} `json:"files"`
	}
	const maxFiles = 200
	if err := ctx.PlatformAPI().CallAppResult("storage", "files_list", map[string]any{
		"_project_id": pid, "folder": brand.StorageRoot, "recursive": true, "limit": maxFiles,
	}, &listed); err != nil {
		return nil, fmt.Errorf("list Storage files: %w", err)
	}
	candidates := make([]map[string]any, 0, len(listed.Files))
	for _, file := range listed.Files {
		if file.ID <= 0 {
			continue
		}
		fileID := strconv.FormatInt(file.ID, 10)
		rows, err := ctx.AppDB().Query(`SELECT session_id FROM assets WHERE project_id=? AND storage_install_id=? AND storage_file_id=?`, pid, bound.InstallID, fileID)
		if err != nil {
			return nil, err
		}
		linked := []string{}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			linked = append(linked, id)
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
		candidates = append(candidates, map[string]any{
			"storage_file_id": fileID, "storage_install_id": bound.InstallID, "name": file.Name, "folder": file.Folder, "content_type": file.ContentType,
			"linked_session_ids": linked, "needs_review": true,
		})
	}
	return map[string]any{"brand_id": brand.ID, "session_id": session.ID, "storage_root": brand.StorageRoot,
		"candidates": candidates, "limit_reached": len(listed.Files) == maxFiles}, nil
}
