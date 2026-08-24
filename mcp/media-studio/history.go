package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// generationRecord is the value-object dbInsertGeneration takes —
// keeps the argument list at one column rather than fifteen positional
// fields.
type generationRecord struct {
	ProjectID                string
	Kind                     string
	Prompt                   string
	Revised                  string
	Provider                 string
	Model                    string
	Size                     string
	DurationMs               int64
	StorageIDs               []int64
	UpstreamURLs             []string
	ThumbnailB64             string
	ExtraJSON                string
	Count                    int
	CostUSD                  float64
	CacheKey                 string
	EstimatedDurationSeconds float64
	ActualDurationSeconds    float64
	Status                   string
	RequestJSON              string
}

type generationExecer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

func insertGenerationWith(exec generationExecer, r generationRecord) (int64, error) {
	sj, _ := json.Marshal(r.StorageIDs)
	uj, _ := json.Marshal(r.UpstreamURLs)
	if r.ExtraJSON == "" {
		r.ExtraJSON = "{}"
	}
	if r.Status == "" {
		r.Status = "ready"
	}
	if r.RequestJSON == "" {
		r.RequestJSON = "{}"
	}
	res, err := exec.Exec(
		`INSERT INTO generations
			(project_id, kind, prompt, revised_prompt, provider, model,
			 size, duration_ms, storage_ids, upstream_urls, thumbnail_b64,
			 extra_json, count, cost_usd, cache_key, estimated_duration_seconds,
			 actual_duration_seconds, status, request_json)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ProjectID, r.Kind, r.Prompt, r.Revised, r.Provider, r.Model,
		r.Size, r.DurationMs, string(sj), string(uj), r.ThumbnailB64,
		r.ExtraJSON, r.Count, r.CostUSD, r.CacheKey, r.EstimatedDurationSeconds,
		r.ActualDurationSeconds, r.Status, r.RequestJSON,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func updateGenerationWith(exec generationExecer, r generationRecord, id int64) (bool, error) {
	sj, _ := json.Marshal(r.StorageIDs)
	uj, _ := json.Marshal(r.UpstreamURLs)
	if r.ExtraJSON == "" {
		r.ExtraJSON = "{}"
	}
	if r.Status == "" {
		r.Status = "ready"
	}
	if r.RequestJSON == "" {
		r.RequestJSON = "{}"
	}
	res, err := exec.Exec(
		`UPDATE generations
		 SET kind=?, prompt=?, revised_prompt=?, provider=?, model=?, size=?,
		     duration_ms=?, storage_ids=?, upstream_urls=?, thumbnail_b64=?,
		     extra_json=?, count=?, cost_usd=?, cache_key=?,
		     estimated_duration_seconds=?, actual_duration_seconds=?,
		     status=?, request_json=?
		 WHERE id=? AND project_id=?`,
		r.Kind, r.Prompt, r.Revised, r.Provider, r.Model, r.Size,
		r.DurationMs, string(sj), string(uj), r.ThumbnailB64,
		r.ExtraJSON, r.Count, r.CostUSD, r.CacheKey,
		r.EstimatedDurationSeconds, r.ActualDurationSeconds,
		r.Status, r.RequestJSON, id, r.ProjectID,
	)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n > 0, err
}

func (a *App) dbInsertGeneration(r generationRecord) int64 {
	if globalCtx == nil {
		return 0
	}
	id, err := insertGenerationWith(globalCtx.AppDB(), r)
	if err != nil {
		globalCtx.Logger().Warn("dbInsertGeneration failed", "err", err)
		return 0
	}
	return id
}

func (a *App) dbUpdateGeneration(r generationRecord, id int64) bool {
	if globalCtx == nil || id == 0 {
		return false
	}
	ok, err := updateGenerationWith(globalCtx.AppDB(), r, id)
	if err != nil {
		globalCtx.Logger().Warn("dbUpdateGeneration failed", "id", id, "err", err)
		return false
	}
	return ok
}

func updateGenerationStatus(ctx *sdk.AppCtx, pid string, id int64, status string) {
	if id == 0 {
		return
	}
	if _, err := ctx.AppDB().Exec(
		`UPDATE generations SET status=? WHERE id=? AND project_id=?`,
		status, id, pid,
	); err != nil {
		ctx.Logger().Warn("generation status update failed", "id", id, "status", status, "err", err)
	}
}

// toolMediaHistory is the MCP read tool — kind-aware, paginated.
func (a *App) toolMediaHistory(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid := projectScopeFromArgs(ctx, args)
	query, err := newHistoryQuery(
		strArg(args, "kind", ""),
		intArg(args, "limit", 50),
		strArg(args, "cursor", ""),
		strArg(args, "since", ""),
	)
	if err != nil {
		return nil, err
	}
	return queryHistory(ctx, pid, query)
}

func (a *App) toolMediaGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx = withProjectScope(ctx, args)
	id := int64Arg(args, "id", 0)
	if id == 0 {
		return nil, errors.New("id required")
	}
	row, err := queryGenerationByID(ctx, projectScope(ctx), id)
	if err != nil {
		return nil, err
	}
	enrichGenerationStorageURLs(ctx, row)
	return map[string]any{"generation": row}, nil
}

type historyQuery struct {
	Kind     string
	Limit    int
	BeforeID int64
	Since    string
}

func newHistoryQuery(kind string, limit int, cursor, since string) (historyQuery, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 200 {
		limit = 200
	}
	query := historyQuery{Kind: kind, Limit: limit}
	if cursor = strings.TrimSpace(cursor); cursor != "" {
		beforeID, err := strconv.ParseInt(cursor, 10, 64)
		if err != nil || beforeID <= 0 {
			return historyQuery{}, errors.New("cursor must be a positive generation id returned by media_history")
		}
		query.BeforeID = beforeID
	}
	if since = strings.TrimSpace(since); since != "" {
		parsed, err := time.Parse(time.RFC3339, since)
		if err != nil {
			return historyQuery{}, fmt.Errorf("since must be RFC3339: %w", err)
		}
		query.Since = parsed.UTC().Format("2006-01-02 15:04:05")
	}
	return query, nil
}

// queryHistory uses id-based keyset pagination so later pages remain fast
// even when a project has accumulated a large generation history.
func queryHistory(ctx *sdk.AppCtx, pid string, query historyQuery) (map[string]any, error) {
	statement := `SELECT id, kind, prompt, revised_prompt, provider, model, size,
		        duration_ms, storage_ids, upstream_urls, thumbnail_b64,
		        extra_json, count, cost_usd, cache_key, estimated_duration_seconds,
		        actual_duration_seconds, status, request_json, created_at
		 FROM generations
		 WHERE project_id = ?`
	params := []any{pid}
	if query.Kind != "" {
		statement += ` AND kind = ?`
		params = append(params, query.Kind)
	}
	if query.BeforeID > 0 {
		statement += ` AND id < ?`
		params = append(params, query.BeforeID)
	}
	if query.Since != "" {
		statement += ` AND created_at >= ?`
		params = append(params, query.Since)
	}
	statement += ` ORDER BY id DESC LIMIT ?`
	params = append(params, query.Limit+1)

	rows, err := ctx.AppDB().Query(statement, params...)
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
			cacheKey, status, requestJSON, createdAt              string
			costUSD, estimatedDuration, actualDuration            float64
		)
		if err := rows.Scan(&id, &kind, &prompt, &revised, &provider, &model, &size,
			&durationMs, &storageIDsJSON, &upstreamURLsJSON, &thumbB64,
			&extraJSON, &count, &costUSD, &cacheKey, &estimatedDuration,
			&actualDuration, &status, &requestJSON, &createdAt); err != nil {
			continue
		}
		generation := generationMap(pid, id, count, durationMs, kind, prompt, revised,
			provider, model, size, storageIDsJSON, upstreamURLsJSON, thumbB64,
			extraJSON, cacheKey, status, requestJSON, createdAt, costUSD, estimatedDuration, actualDuration)
		enrichGenerationStorageURLs(ctx.WithProject(pid), generation)
		out = append(out, generation)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	hasMore := len(out) > query.Limit
	if hasMore {
		out = out[:query.Limit]
	}
	nextCursor := ""
	if hasMore && len(out) > 0 {
		nextCursor = strconv.FormatInt(out[len(out)-1]["id"].(int64), 10)
	}
	return map[string]any{
		"generations": out,
		"has_more":    hasMore,
		"next_cursor": nextCursor,
	}, nil
}

func queryGenerationByID(ctx *sdk.AppCtx, pid string, id int64) (map[string]any, error) {
	var (
		count, durationMs                                     int64
		kind, prompt, revised, provider, model, size          string
		storageIDsJSON, upstreamURLsJSON, thumbB64, extraJSON string
		cacheKey, status, requestJSON, createdAt              string
		costUSD, estimatedDuration, actualDuration            float64
	)
	err := ctx.AppDB().QueryRow(
		`SELECT kind, prompt, revised_prompt, provider, model, size,
		        duration_ms, storage_ids, upstream_urls, thumbnail_b64,
		        extra_json, count, cost_usd, cache_key, estimated_duration_seconds,
		        actual_duration_seconds, status, request_json, created_at
		 FROM generations
		 WHERE project_id = ? AND id = ?`,
		pid, id,
	).Scan(&kind, &prompt, &revised, &provider, &model, &size,
		&durationMs, &storageIDsJSON, &upstreamURLsJSON, &thumbB64,
		&extraJSON, &count, &costUSD, &cacheKey, &estimatedDuration,
		&actualDuration, &status, &requestJSON, &createdAt)
	if err != nil {
		return nil, err
	}
	return generationMap(pid, id, count, durationMs, kind, prompt, revised,
		provider, model, size, storageIDsJSON, upstreamURLsJSON, thumbB64,
		extraJSON, cacheKey, status, requestJSON, createdAt, costUSD, estimatedDuration, actualDuration), nil
}

func queryGenerationByCacheKey(ctx *sdk.AppCtx, pid, kind, cacheKey string) (map[string]any, error) {
	if cacheKey == "" {
		return nil, errors.New("cache_key required")
	}
	var id int64
	err := ctx.AppDB().QueryRow(
		`SELECT id FROM generations
		 WHERE project_id = ? AND kind = ? AND cache_key = ?
		   AND status = 'ready'
		 ORDER BY id DESC LIMIT 1`,
		pid, kind, cacheKey,
	).Scan(&id)
	if err != nil {
		return nil, err
	}
	row, err := queryGenerationByID(ctx, pid, id)
	if err != nil {
		return nil, err
	}
	enrichGenerationStorageURLs(ctx.WithProject(pid), row)
	return row, nil
}

func generationMap(pid string, id, count, durationMs int64, kind, prompt, revised, provider, model, size, storageIDsJSON, upstreamURLsJSON, thumbB64, extraJSON, cacheKey, status, requestJSON, createdAt string, costUSD, estimatedDuration, actualDuration float64) map[string]any {
	var storageIDs []int64
	_ = json.Unmarshal([]byte(storageIDsJSON), &storageIDs)
	var upstreamURLs []string
	_ = json.Unmarshal([]byte(upstreamURLsJSON), &upstreamURLs)
	storageURLs := []string{}
	if len(storageIDs) > 0 {
		// Storage owns the durable copy. Provider URLs may expire and must
		// not be surfaced as an alternative downstream contract.
		upstreamURLs = []string{}
	}
	localURL := ""
	if len(storageIDs) == 0 {
		localURL = localCacheURL(id, pid)
	}
	out := map[string]any{
		"id":                         id,
		"kind":                       kind,
		"prompt":                     prompt,
		"revised_prompt":             revised,
		"provider":                   provider,
		"model":                      model,
		"size":                       size,
		"duration_ms":                durationMs,
		"estimated_duration_seconds": estimatedDuration,
		"actual_duration_seconds":    actualDuration,
		"storage_ids":                storageIDs,
		"storage_urls":               storageURLs,
		"upstream_urls":              upstreamURLs,
		"thumbnail_b64":              thumbB64,
		"local_cache_url":            localURL,
		"extra_json":                 extraJSON,
		"cache_key":                  cacheKey,
		"status":                     status,
		"request_json":               requestJSON,
		"count":                      count,
		"cost_usd":                   costUSD,
		"created_at":                 createdAt,
	}
	return out
}

// enrichGenerationStorageURLs refreshes signed URLs for user-facing history,
// detail, and cache-hit responses. Internal generation lookups deliberately
// remain ID-only so operations such as delete and draft loading do not mint
// URLs they never return.
func enrichGenerationStorageURLs(ctx *sdk.AppCtx, generation map[string]any) {
	ids, _ := generation["storage_ids"].([]int64)
	if len(ids) == 0 || ctx == nil || ctx.IntegrationFor("storage") == nil {
		return
	}
	urls, err := storageHTTPSURLs(ctx, ids)
	if err != nil {
		generation["storage_urls"] = []string{}
		generation["storage_url_error"] = err.Error()
		return
	}
	generation["storage_urls"] = urls
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
	limit := 24
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 {
			limit = n
		}
	}
	query, err := newHistoryQuery(
		r.URL.Query().Get("kind"),
		limit,
		r.URL.Query().Get("cursor"),
		r.URL.Query().Get("since"),
	)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}

	out, err := queryHistory(globalCtx, pid, query)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

// HTTP /generations/<id> supplies one project-scoped generation to chat
// components without loading or scanning a gallery page.
func (a *App) handleGetGeneration(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	id, err := pathID(r.URL.Path, "/generations/")
	if err != nil {
		http.Error(w, "bad id", http.StatusBadRequest)
		return
	}
	ctx, pid, err := projectContextFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	row, err := queryGenerationByID(ctx, pid, id)
	if errors.Is(err, sql.ErrNoRows) {
		http.Error(w, "not found", http.StatusNotFound)
		return
	}
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	enrichGenerationStorageURLs(ctx.WithProject(pid), row)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"generation": row})
}

func pathID(path, prefix string) (int64, error) {
	raw := strings.SplitN(strings.TrimPrefix(path, prefix), "/", 2)[0]
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id <= 0 {
		return 0, errors.New("invalid id")
	}
	return id, nil
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

func projectContextFromRequest(r *http.Request) (*sdk.AppCtx, string, error) {
	if globalCtx == nil {
		return nil, "", errors.New("app not mounted")
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		return nil, "", err
	}
	return globalCtx.WithProject(pid), pid, nil
}
