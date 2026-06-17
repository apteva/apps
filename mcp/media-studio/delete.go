package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) toolMediaDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx = withProjectScope(ctx, args)
	out, err := a.deleteGeneration(ctx, args)
	if err != nil {
		return mcpError("delete: " + err.Error()), nil
	}
	return map[string]any{
		"content": []map[string]any{{
			"type": "text",
			"text": "Deleted media generation #" + strconvFormatInt(out["id"].(int64)) + ".",
		}},
		"_meta": out,
	}, nil
}

func (a *App) handleDeleteGeneration(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodDelete {
		http.Error(w, "POST or DELETE only", http.StatusMethodNotAllowed)
		return
	}
	if globalCtx == nil {
		http.Error(w, "app not mounted", http.StatusServiceUnavailable)
		return
	}
	body := map[string]any{}
	if r.Method == http.MethodDelete {
		if id := strings.TrimSpace(r.URL.Query().Get("id")); id != "" {
			body["id"] = id
		}
		if v := strings.TrimSpace(r.URL.Query().Get("delete_storage")); v != "" {
			body["delete_storage"] = v
		}
	} else if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return
	}
	if pid := strings.TrimSpace(r.URL.Query().Get("project_id")); pid != "" && projectArg(body) == "" {
		body["project_id"] = pid
	}
	pid := projectScopeFromArgs(globalCtx, body)
	if pid == "" {
		http.Error(w, "project_id required", http.StatusBadRequest)
		return
	}
	body["_project_id"] = pid
	out, err := a.deleteGeneration(globalCtx.WithProject(pid), body)
	writeJSON(w, out, err)
}

func (a *App) deleteGeneration(ctx *sdk.AppCtx, args map[string]any) (map[string]any, error) {
	id := int64Arg(args, "id", 0)
	if id == 0 {
		return nil, errors.New("id required")
	}
	pid := projectScope(ctx)
	row, err := queryGenerationByID(ctx, pid, id)
	if err != nil {
		return nil, err
	}
	storageIDs, _ := row["storage_ids"].([]int64)
	deleteStorage := boolArg(args, "delete_storage", true)
	deletedStorage := []int64{}
	if deleteStorage {
		for _, storageID := range storageIDs {
			if storageID == 0 {
				continue
			}
			var got map[string]any
			if err := ctx.PlatformAPI().CallAppResult("storage", "files_delete", storageArgs(ctx, map[string]any{
				"id":          storageID,
				"keep_record": false,
			}), &got); err != nil {
				return nil, errors.New("storage delete #" + strconvFormatInt(storageID) + ": " + err.Error())
			}
			deletedStorage = append(deletedStorage, storageID)
		}
	}
	if err := deleteLocalCache(id); err != nil {
		ctx.Logger().Warn("delete local cache failed", "generation_id", id, "err", err)
	}
	if _, err := ctx.AppDB().Exec(
		`DELETE FROM video_jobs WHERE project_id=? AND generation_id=?`,
		pid, id,
	); err != nil {
		return nil, err
	}
	if _, err := ctx.AppDB().Exec(
		`DELETE FROM generations WHERE project_id=? AND id=?`,
		pid, id,
	); err != nil {
		return nil, err
	}
	ctx.Emit("media.deleted", map[string]any{
		"id":                  id,
		"kind":                strAny(row["kind"]),
		"storage_ids":         storageIDs,
		"deleted_storage_ids": deletedStorage,
	})
	return map[string]any{
		"id":                  id,
		"deleted":             true,
		"delete_storage":      deleteStorage,
		"storage_ids":         storageIDs,
		"deleted_storage_ids": deletedStorage,
	}, nil
}
