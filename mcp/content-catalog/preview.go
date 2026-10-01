package main

import (
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// previewFileID chooses a small visual representation while keeping Media and
// Storage as the owners of derivatives and bytes. Missing Media data is an
// ordinary no-preview result, not a reason to mutate or reindex either app.
func previewFileID(ctx *sdk.AppCtx, pid string, asset *Asset) (int64, bool) {
	isImage := strings.HasPrefix(asset.ContentType, "image/") || asset.Kind == "image"
	isVideo := strings.HasPrefix(asset.ContentType, "video/") || asset.Kind == "video"
	isAudio := strings.HasPrefix(asset.ContentType, "audio/") || asset.Kind == "audio"
	if isImage {
		id, err := strconv.ParseInt(asset.StorageFileID, 10, 64)
		return id, err == nil && id > 0
	}
	if !isVideo && !isAudio {
		return 0, false
	}
	if ctx.IntegrationFor("media") == nil {
		return 0, false
	}
	var result struct {
		Found bool `json:"found"`
		Media struct {
			Derivations []struct {
				Kind          string `json:"kind"`
				Status        string `json:"status"`
				StorageFileID string `json:"storage_file_id"`
			} `json:"derivations"`
		} `json:"media"`
	}
	if err := ctx.PlatformAPI().CallAppResult("media", "media_get", map[string]any{"_project_id": pid, "file_id": asset.StorageFileID}, &result); err != nil || !result.Found {
		return 0, false
	}
	preferred := []string{"thumbnail", "cover", "keyframe"}
	if isAudio {
		preferred = []string{"waveform", "cover", "thumbnail"}
	}
	for _, kind := range preferred {
		for _, derivation := range result.Media.Derivations {
			if derivation.Kind != kind || derivation.Status != "ok" {
				continue
			}
			id, err := strconv.ParseInt(derivation.StorageFileID, 10, 64)
			if err == nil && id > 0 {
				return id, true
			}
		}
	}
	return 0, false
}

func sessionPreviewAsset(db *sql.DB, pid, sessionID string) ([]string, error) {
	if _, err := sessionByID(db, pid, sessionID); err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT id FROM assets WHERE project_id=? AND session_id=? AND (content_type LIKE 'image/%' OR content_type LIKE 'video/%' OR content_type LIKE 'audio/%' OR kind IN ('image','video','audio')) ORDER BY CASE WHEN content_type LIKE 'image/%' OR kind='image' THEN 0 WHEN content_type LIKE 'video/%' OR kind='video' THEN 1 ELSE 2 END,created_at DESC,id DESC LIMIT 30`, pid, sessionID)
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	err = rows.Err()
	rows.Close()
	return ids, err
}

func (a *App) servePreview(w http.ResponseWriter, r *http.Request, recordType, id string) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	pid, err := requestProject(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if globalCtx == nil {
		http.Error(w, "app not mounted", http.StatusServiceUnavailable)
		return
	}
	ctx := globalCtx.WithProject(pid)
	ids := []string{id}
	if recordType == "session" {
		ids, err = sessionPreviewAsset(ctx.AppDB(), pid, id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
	}
	for _, assetID := range ids {
		asset, err := assetByID(ctx.AppDB(), pid, assetID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			http.Error(w, err.Error(), http.StatusNotFound)
			return
		}
		fileID, found := previewFileID(ctx, pid, asset)
		if !found {
			continue
		}
		path := fmt.Sprintf("/api/apps/storage/files/%d/content?project_id=%s&install_id=%d", fileID, url.QueryEscape(pid), asset.StorageInstallID)
		w.Header().Set("Cache-Control", "private, no-store")
		http.Redirect(w, r, path, http.StatusFound)
		return
	}
	http.Error(w, "preview unavailable", http.StatusNotFound)
}
