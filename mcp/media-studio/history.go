package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"os"
	"strconv"

	sdk "github.com/apteva/app-sdk"
)

// generationRecord is the value-object dbInsertGeneration takes —
// keeps the argument list at one column rather than fifteen positional
// fields.
type generationRecord struct {
	ProjectID    string
	Kind         string
	Prompt       string
	Revised      string
	Provider     string
	Model        string
	Size         string
	DurationMs   int64
	StorageIDs   []int64
	UpstreamURLs []string
	ThumbnailB64 string
	ExtraJSON    string
	Count        int
	CostUSD      float64
}

func (a *App) dbInsertGeneration(r generationRecord) int64 {
	if globalCtx == nil {
		return 0
	}
	sj, _ := json.Marshal(r.StorageIDs)
	uj, _ := json.Marshal(r.UpstreamURLs)
	if r.ExtraJSON == "" {
		r.ExtraJSON = "{}"
	}
	res, err := globalCtx.AppDB().Exec(
		`INSERT INTO generations
			(project_id, kind, prompt, revised_prompt, provider, model,
			 size, duration_ms, storage_ids, upstream_urls, thumbnail_b64,
			 extra_json, count, cost_usd)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ProjectID, r.Kind, r.Prompt, r.Revised, r.Provider, r.Model,
		r.Size, r.DurationMs, string(sj), string(uj), r.ThumbnailB64,
		r.ExtraJSON, r.Count, r.CostUSD,
	)
	if err != nil {
		globalCtx.Logger().Warn("dbInsertGeneration failed", "err", err)
		return 0
	}
	id, _ := res.LastInsertId()
	return id
}

// toolMediaHistory is the MCP read tool — kind-aware, paginated.
func (a *App) toolMediaHistory(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid := os.Getenv("APTEVA_PROJECT_ID")
	if v := strArg(args, "project_id", ""); v != "" {
		pid = v
	}
	limit := intArg(args, "limit", 50)
	if limit > 200 {
		limit = 200
	}
	kindFilter := strArg(args, "kind", "")
	return queryHistory(ctx, pid, kindFilter, limit)
}

// queryHistory pages over generations rows, optionally filtered by
// kind. The "kind='' OR kind=?" trick avoids two near-identical
// query branches; sqlite plans it identically to a bare equality.
func queryHistory(ctx *sdk.AppCtx, pid, kindFilter string, limit int) (map[string]any, error) {
	rows, err := ctx.AppDB().Query(
		`SELECT id, kind, prompt, revised_prompt, provider, model, size,
		        duration_ms, storage_ids, upstream_urls, thumbnail_b64,
		        extra_json, count, cost_usd, created_at
		 FROM generations
		 WHERE project_id = ? AND (? = '' OR kind = ?)
		 ORDER BY id DESC LIMIT ?`,
		pid, kindFilter, kindFilter, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []map[string]any{}
	for rows.Next() {
		var (
			id, count, durationMs                                 int64
			kind, prompt, revised, provider, model, size          string
			storageIDsJSON, upstreamURLsJSON, thumbB64, extraJSON string
			createdAt                                             string
			costUSD                                               float64
		)
		if err := rows.Scan(&id, &kind, &prompt, &revised, &provider, &model, &size,
			&durationMs, &storageIDsJSON, &upstreamURLsJSON, &thumbB64,
			&extraJSON, &count, &costUSD, &createdAt); err != nil {
			continue
		}
		var storageIDs []int64
		_ = json.Unmarshal([]byte(storageIDsJSON), &storageIDs)
		var upstreamURLs []string
		_ = json.Unmarshal([]byte(upstreamURLsJSON), &upstreamURLs)
		storageURLs := make([]string, 0, len(storageIDs))
		for _, sid := range storageIDs {
			storageURLs = append(storageURLs, storageContentURL(sid, pid))
		}
		// When storage isn't bound, the sidecar may have cached the full
		// bytes locally (cache.go writeLocalCache). Surface that URL so
		// the panel can render the original instead of the thumbnail.
		localURL := ""
		if len(storageIDs) == 0 {
			localURL = localCacheURL(id)
		}
		out = append(out, map[string]any{
			"id":               id,
			"kind":             kind,
			"prompt":           prompt,
			"revised_prompt":   revised,
			"provider":         provider,
			"model":            model,
			"size":             size,
			"duration_ms":      durationMs,
			"storage_ids":      storageIDs,
			"storage_urls":     storageURLs,
			"upstream_urls":    upstreamURLs,
			"thumbnail_b64":    thumbB64,
			"local_cache_url":  localURL,
			"extra_json":       extraJSON,
			"count":            count,
			"cost_usd":         costUSD,
			"created_at":       createdAt,
		})
	}
	return map[string]any{"generations": out}, nil
}

// ─── HTTP /generations — panel gallery ─────────────────────────────

func (a *App) handleListGenerations(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	if limit > 200 {
		limit = 200
	}
	kindFilter := r.URL.Query().Get("kind")

	out, err := queryHistory(globalCtx, pid, kindFilter, limit)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func resolveProjectFromRequest(r *http.Request) (string, error) {
	if v := r.URL.Query().Get("project_id"); v != "" {
		return v, nil
	}
	if v := os.Getenv("APTEVA_PROJECT_ID"); v != "" {
		return v, nil
	}
	return "", errors.New("project_id required")
}
