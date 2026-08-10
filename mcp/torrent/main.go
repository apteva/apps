// Apteva torrent app — local BitTorrent client + indexer search
// frontend, with completed files handed off to storage.
//
// Three workers:
//  1. engine        — the bittorrent client (Engine.Run loop)
//  2. scheduler     — runs saved searches on their cadence
//  3. boot-reconcile — one-shot on startup: re-add tracked torrents
//     to the engine and run any deferred completion.
//
// State source-of-truth split:
//   - The torrent engine's session state lives in working_dir, owned
//     by anacrolix/torrent.
//   - The `torrents` table is OUR projection — what was added, what
//     storage rows it produced, what the user-facing state was last
//     time we polled.
//   - On boot we walk the table and re-AddMagnet anything that wasn't
//     a final state, so the engine is in sync with the DB.
package main

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

//go:embed apteva.yaml
var manifestYAML []byte

type App struct {
	ctx    *sdk.AppCtx
	engine *Engine
	runCtx context.Context
	cancel context.CancelFunc

	statsMu        sync.Mutex
	statsScannedAt time.Time
	statsDirBytes  int64
	bootOnce       sync.Once
}

type addTorrentRequest struct {
	Magnet       string `json:"magnet"`
	Infohash     string `json:"infohash"`
	TorrentURL   string `json:"torrent_url"`
	TargetFolder string `json:"target_folder"`
	Paused       bool   `json:"paused"`
}

type addIndexerRequest struct {
	Name       string   `json:"name"`
	Kind       string   `json:"kind"`
	BaseURL    string   `json:"base_url"`
	APIKey     string   `json:"api_key"`
	Categories []string `json:"categories"`
	Priority   int      `json:"priority"`
}

var globalApp *App
var globalAppOnce sync.Once

// ─── lifecycle ──────────────────────────────────────────────────────

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest(manifestYAML)
	if err != nil {
		panic("torrent: invalid manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("torrent: requires a db block")
	}
	a.ctx = ctx
	globalApp = a
	a.runCtx, a.cancel = context.WithCancel(context.Background())

	cfg := EngineConfig{
		WorkingDir:            resolveWorkingDir(ctx),
		ListenPort:            configInt(ctx, "listen_port", 0),
		BindInterface:         configString(ctx, "bind_interface", ""),
		DHTEnabled:            configFlag(ctx, "dht_enabled", true),
		EncryptionForced:      configFlag(ctx, "peer_encryption_required", true),
		GlobalDownKiBps:       configInt(ctx, "max_global_down_kibps", 0),
		GlobalUpKiBps:         configInt(ctx, "max_global_up_kibps", 0),
		MaxConcurrent:         configInt(ctx, "max_concurrent_downloads", 3),
		FreeDiskSafetyPercent: configInt(ctx, "free_disk_safety_pct", 20),
		SeedRatioTarget:       configFloat(ctx, "seed_ratio_target", 1),
		SeedTimeTarget:        time.Duration(configInt(ctx, "seed_time_target_minutes", 60)) * time.Minute,
	}
	eng, err := NewEngine(cfg, func(scope, msg string) {
		ctx.Logger().Info(scope, "msg", msg)
	})
	if err != nil {
		a.cancel()
		return fmt.Errorf("engine: %w", err)
	}
	eng.SetTransitionHandler(a.onTransition)
	a.engine = eng
	ctx.Logger().Info("torrent mounted",
		"working_dir", cfg.WorkingDir, "port", cfg.ListenPort)
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error {
	if a.cancel != nil {
		a.cancel()
	}
	if a.engine != nil {
		a.engine.Close()
	}
	return nil
}

func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{
		{
			Name: "engine",
			Run: func(ctx context.Context, _ *sdk.AppCtx) error {
				return a.engine.Run(ctx)
			},
		},
		{
			Name: "boot-reconcile",
			Run: func(ctx context.Context, _ *sdk.AppCtx) error {
				a.bootOnce.Do(func() {
					select {
					case <-ctx.Done():
						return
					case <-time.After(2 * time.Second):
					}
					a.reconcileOnBoot(ctx)
				})
				return nil // one-shot
			},
		},
		{
			Name: "scheduler",
			Run: func(ctx context.Context, _ *sdk.AppCtx) error {
				t := time.NewTicker(60 * time.Second)
				defer t.Stop()
				for {
					select {
					case <-ctx.Done():
						return nil
					case <-t.C:
						a.runDueSearches(ctx)
					}
				}
			},
		},
		{
			Name: "completion-retry",
			Run: func(ctx context.Context, _ *sdk.AppCtx) error {
				t := time.NewTicker(time.Minute)
				defer t.Stop()
				for {
					select {
					case <-ctx.Done():
						return nil
					case <-t.C:
						a.retryPendingCompletions()
					}
				}
			},
		},
	}
}

// reconcileOnBoot walks the torrents table and re-adds anything that
// isn't in a final state (`completed` with file_ids set or `error`).
// The engine deduplicates by infohash, so re-adding completed-but-not-
// yet-uploaded torrents triggers the completion-mover via the next
// poll-transition.
func (a *App) reconcileOnBoot(ctx context.Context) {
	rows, err := listAllTorrentRows(a.ctx.AppDB())
	if err != nil {
		a.ctx.Logger().Warn("reconcile", "err", err.Error())
		return
	}
	restored := map[string][]TorrentRow{}
	for _, r := range rows {
		select {
		case <-ctx.Done():
			return
		default:
		}
		// Always re-add — the engine will recheck existing bytes in
		// working_dir and short-circuit to the right state. Skip rows
		// in final states only when storage
		// already has the file_ids.
		if r.State == "completed" && r.CompletedAt != "" &&
			!configFlag(a.ctx, "keep_working_copy", false) {
			continue
		}
		restored[r.Infohash] = append(restored[r.Infohash], r)
		if a.engine.Snapshot(r.Infohash) != nil {
			continue
		}
		var snap *TorrentSnapshot
		if r.Magnet != "" {
			snap, err = a.engine.AddMagnet(r.Magnet)
			if err != nil {
				a.ctx.Logger().Warn("re-add magnet", "ih", r.Infohash, "err", err.Error())
			}
		} else if r.Infohash != "" {
			snap, err = a.engine.AddInfohash(r.Infohash)
			if err != nil {
				a.ctx.Logger().Warn("re-add infohash", "ih", r.Infohash, "err", err.Error())
			}
		}
		_ = snap
	}
	for infohash, projectRows := range restored {
		allPaused := len(projectRows) > 0
		for _, row := range projectRows {
			if row.State != "paused" {
				allPaused = false
				break
			}
		}
		a.engine.RestoreState(infohash, allPaused, effectivePriorities(projectRows))
	}
}

// ─── HTTP routes ────────────────────────────────────────────────────

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/torrents", Handler: a.handleTorrents},
		{Pattern: "/torrents/", Handler: a.handleTorrentItem},
		{Pattern: "/searches", Handler: a.handleSearches},
		{Pattern: "/searches/", Handler: a.handleSearchItem},
		{Pattern: "/indexers", Handler: a.handleIndexers},
		{Pattern: "/indexers/test", Handler: a.handleIndexersTest},
		{Pattern: "/indexers/", Handler: a.handleIndexerItem},
		{Pattern: "/stats", Handler: a.handleStatsHTTP},
		{Pattern: "/search", Handler: a.handleSearchHTTP},
	}
}

func (a *App) handleTorrents(w http.ResponseWriter, r *http.Request) {
	appCtx, ok := a.requestContext(w, r)
	if !ok {
		return
	}
	switch r.Method {
	case http.MethodGet:
		state := r.URL.Query().Get("state")
		out, err := a.combinedTorrentList(appCtx, state)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		writeJSON(w, out)
	case http.MethodPost:
		var body addTorrentRequest
		if err := decodeJSONBody(r, &body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		out, err := a.addTorrent(appCtx, body.Magnet, body.Infohash, body.TorrentURL, body.TargetFolder, body.Paused)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSONStatus(w, http.StatusCreated, out)
	default:
		http.Error(w, "GET or POST", 405)
	}
}

func (a *App) handleTorrentItem(w http.ResponseWriter, r *http.Request) {
	appCtx, ok := a.requestContext(w, r)
	if !ok {
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/torrents/")
	parts := strings.SplitN(rest, "/", 2)
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid torrent id", http.StatusBadRequest)
		return
	}
	action := ""
	if len(parts) > 1 {
		action = parts[1]
	}
	switch {
	case action == "" && r.Method == http.MethodGet:
		out, err := a.getTorrentDetail(appCtx, id)
		if err != nil {
			http.Error(w, err.Error(), 404)
			return
		}
		writeJSON(w, out)
	case action == "" && r.Method == http.MethodDelete:
		del := r.URL.Query().Get("delete_files") == "true"
		if err := a.removeTorrent(appCtx, id, del); err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		w.WriteHeader(204)
	case action == "pause" && r.Method == http.MethodPost:
		if err := a.pauseTorrent(appCtx, id); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		w.WriteHeader(204)
	case action == "resume" && r.Method == http.MethodPost:
		if err := a.resumeTorrent(appCtx, id); err != nil {
			http.Error(w, err.Error(), http.StatusConflict)
			return
		}
		w.WriteHeader(204)
	case action == "priority" && r.Method == http.MethodPost:
		var body struct {
			FileIndex int    `json:"file_index"`
			Priority  string `json:"priority"`
		}
		if err := decodeJSONBody(r, &body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if err := a.setPriority(appCtx, id, body.FileIndex, body.Priority); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		w.WriteHeader(204)
	default:
		http.Error(w, "method/path not supported", 405)
	}
}

func (a *App) handleSearches(w http.ResponseWriter, r *http.Request) {
	appCtx, ok := a.requestContext(w, r)
	if !ok {
		return
	}
	pid := projectScope(appCtx)
	switch r.Method {
	case http.MethodGet:
		out, err := listSavedSearches(a.ctx.AppDB(), pid)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		writeJSON(w, out)
	case http.MethodPost:
		var body SavedSearch
		if err := decodeJSONBody(r, &body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		out, err := addSavedSearch(a.ctx.AppDB(), pid, body)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSONStatus(w, http.StatusCreated, out)
	default:
		http.Error(w, "GET or POST", 405)
	}
}

func (a *App) handleSearchItem(w http.ResponseWriter, r *http.Request) {
	appCtx, ok := a.requestContext(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodDelete {
		http.Error(w, "DELETE", 405)
		return
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/searches/"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid saved-search id", http.StatusBadRequest)
		return
	}
	if _, err := a.ctx.AppDB().Exec(`DELETE FROM saved_searches WHERE id = ? AND project_id = ?`,
		id, projectScope(appCtx)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(204)
}

func (a *App) handleIndexers(w http.ResponseWriter, r *http.Request) {
	appCtx, ok := a.requestContext(w, r)
	if !ok {
		return
	}
	pid := projectScope(appCtx)
	switch r.Method {
	case http.MethodGet:
		out, err := listIndexers(a.ctx.AppDB(), pid, false)
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		writeJSON(w, out)
	case http.MethodPost:
		var body addIndexerRequest
		if err := decodeJSONBody(r, &body); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		out, err := addIndexer(a.ctx.AppDB(), pid, body.Name, body.Kind, body.BaseURL, body.APIKey, body.Categories, body.Priority)
		if err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		writeJSONStatus(w, http.StatusCreated, out)
	default:
		http.Error(w, "GET or POST", 405)
	}
}

func (a *App) handleIndexersTest(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST", http.StatusMethodNotAllowed)
		return
	}
	appCtx, ok := a.requestContext(w, r)
	if !ok {
		return
	}
	out, err := a.testIndexers(r.Context(), appCtx)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, out)
}

func (a *App) handleIndexerItem(w http.ResponseWriter, r *http.Request) {
	appCtx, ok := a.requestContext(w, r)
	if !ok {
		return
	}
	if r.Method != http.MethodDelete {
		http.Error(w, "DELETE", 405)
		return
	}
	id, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/indexers/"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid indexer id", http.StatusBadRequest)
		return
	}
	if _, err := a.ctx.AppDB().Exec(`DELETE FROM indexers WHERE id = ? AND project_id = ?`,
		id, projectScope(appCtx)); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(204)
}

func (a *App) handleStatsHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET", http.StatusMethodNotAllowed)
		return
	}
	appCtx, ok := a.requestContext(w, r)
	if !ok {
		return
	}
	out := a.globalStats(appCtx)
	writeJSON(w, out)
}

func (a *App) handleSearchHTTP(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET", http.StatusMethodNotAllowed)
		return
	}
	appCtx, ok := a.requestContext(w, r)
	if !ok {
		return
	}
	q := r.URL.Query().Get("q")
	if strings.TrimSpace(q) == "" {
		http.Error(w, "query required", http.StatusBadRequest)
		return
	}
	cat := r.URL.Query().Get("category")
	min, _ := strconv.Atoi(r.URL.Query().Get("min_seeders"))
	sortBy := r.URL.Query().Get("sort")
	out, err := a.searchIndexers(r.Context(), appCtx, q, cat, min, sortBy)
	if err != nil {
		http.Error(w, err.Error(), 500)
		return
	}
	writeJSON(w, out)
}

// ─── MCP tools ──────────────────────────────────────────────────────

func (a *App) MCPTools() []sdk.Tool {
	obj := func(p map[string]any, req []string) map[string]any {
		s := map[string]any{"type": "object", "properties": p}
		if len(req) > 0 {
			s["required"] = req
		}
		return s
	}
	str := map[string]any{"type": "string"}
	num := map[string]any{"type": "integer"}
	boo := map[string]any{"type": "boolean"}

	return []sdk.Tool{
		{Name: "torrent_search",
			Description: "Multi-indexer search. Aggregates across enabled indexers, dedupes by infohash, sorts by seeders descending. Args: query (required), category? (movie|tv|music|book|software), min_seeders? (default 0), sort? (seeders|size|newest, default seeders).",
			InputSchema: obj(map[string]any{
				"query": str, "category": str, "min_seeders": num, "sort": str,
			}, []string{"query"}),
			Handler: a.toolSearch},
		{Name: "torrent_search_save",
			Description: "Save a recurring search. If auto_add_top_n>0, the top N matching results are auto-added; otherwise just emits torrent.search_match for the agent to react to.",
			InputSchema: obj(map[string]any{
				"name": str, "query": str, "category": str,
				"min_seeders": num, "max_size_bytes": num, "exclude_terms": str,
				"auto_add_top_n": num, "run_interval_minutes": num,
			}, []string{"query"}),
			Handler: a.toolSearchSave},
		{Name: "torrent_search_save_list",
			Description: "List saved searches.",
			InputSchema: obj(nil, nil),
			Handler:     a.toolSearchSaveList},
		{Name: "torrent_search_save_delete",
			Description: "Delete a saved search by id.",
			InputSchema: obj(map[string]any{"id": num}, []string{"id"}),
			Handler:     a.toolSearchSaveDelete},
		{Name: "torrent_add",
			Description: "Start a download. Provide one of: magnet | infohash (40-char hex) | torrent_url (.torrent file URL). target_folder is a storage path; defaults to default_target_folder config. paused=true queues without starting.",
			InputSchema: obj(map[string]any{
				"magnet":        str,
				"infohash":      str,
				"torrent_url":   str,
				"target_folder": str,
				"paused":        boo,
			}, nil),
			Handler: a.toolAdd},
		{Name: "torrent_list",
			Description: "List downloads. state filter: downloading | seeding | paused | completed | error | all (default 'all').",
			InputSchema: obj(map[string]any{"state": str}, nil),
			Handler:     a.toolList},
		{Name: "torrent_get",
			Description: "Read one download by id, including per-file progress + tracker / peer status.",
			InputSchema: obj(map[string]any{"id": num}, []string{"id"}),
			Handler:     a.toolGet},
		{Name: "torrent_pause",
			Description: "Pause a download.",
			InputSchema: obj(map[string]any{"id": num}, []string{"id"}),
			Handler:     a.toolPause},
		{Name: "torrent_resume",
			Description: "Resume a paused download.",
			InputSchema: obj(map[string]any{"id": num}, []string{"id"}),
			Handler:     a.toolResume},
		{Name: "torrent_remove",
			Description: "Remove a download. delete_files=true also drops the storage rows produced on completion (and removes local working copy if any).",
			InputSchema: obj(map[string]any{
				"id": num, "delete_files": boo,
			}, []string{"id"}),
			Handler: a.toolRemove},
		{Name: "torrent_set_priority",
			Description: "Set priority of one file in a multi-file torrent. priority: skip | low | normal | high. Useful to skip sample.mkv / .nfo / .jpg in movie packs.",
			InputSchema: obj(map[string]any{
				"id": num, "file_index": num, "priority": str,
			}, []string{"id", "file_index", "priority"}),
			Handler: a.toolSetPriority},
		{Name: "torrent_stats",
			Description: "Global stats: down/up rate, active count, free disk, working-dir bytes, queue depth.",
			InputSchema: obj(nil, nil),
			Handler:     a.toolStats},
		{Name: "torrent_indexers_test",
			Description: "Health-check each enabled indexer. Returns latency + last-error per source.",
			InputSchema: obj(nil, nil),
			Handler:     a.toolIndexersTest},
	}
}

// ─── tool handlers ──────────────────────────────────────────────────

func (a *App) toolSearch(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	q, _ := args["query"].(string)
	if strings.TrimSpace(q) == "" {
		return nil, errors.New("query required")
	}
	cat, _ := args["category"].(string)
	min := int(toInt64(args["min_seeders"]))
	sortBy, _ := args["sort"].(string)
	return a.searchIndexers(a.operationContext(), ctx, q, cat, min, sortBy)
}

func (a *App) toolSearchSave(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx)
	if err != nil {
		return nil, err
	}
	q, _ := args["query"].(string)
	if strings.TrimSpace(q) == "" {
		return nil, errors.New("query required")
	}
	s := SavedSearch{
		Name:               strArg(args, "name"),
		Query:              q,
		Category:           strArg(args, "category"),
		MinSeeders:         int(toInt64(args["min_seeders"])),
		MaxSizeBytes:       toInt64(args["max_size_bytes"]),
		ExcludeTerms:       strArg(args, "exclude_terms"),
		AutoAddTopN:        int(toInt64(args["auto_add_top_n"])),
		RunIntervalMinutes: int(toInt64(args["run_interval_minutes"])),
	}
	if s.MinSeeders == 0 {
		s.MinSeeders = 1
	}
	if s.RunIntervalMinutes == 0 {
		s.RunIntervalMinutes = 60
	}
	if s.Name == "" {
		s.Name = q
	}
	return addSavedSearch(ctx.AppDB(), pid, s)
}

func (a *App) toolSearchSaveList(ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	pid, err := requireProject(ctx)
	if err != nil {
		return nil, err
	}
	return listSavedSearches(ctx.AppDB(), pid)
}

func (a *App) toolSearchSaveDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := requireProject(ctx)
	if err != nil {
		return nil, err
	}
	id := toInt64(args["id"])
	_, err = ctx.AppDB().Exec(`DELETE FROM saved_searches WHERE id = ? AND project_id = ?`, id, pid)
	return map[string]any{"removed": id}, err
}

func (a *App) toolAdd(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	mag, _ := args["magnet"].(string)
	ih, _ := args["infohash"].(string)
	url, _ := args["torrent_url"].(string)
	folder, _ := args["target_folder"].(string)
	paused, _ := args["paused"].(bool)
	return a.addTorrent(ctx, mag, ih, url, folder, paused)
}

func (a *App) toolList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	state, _ := args["state"].(string)
	return a.combinedTorrentList(ctx, state)
}

func (a *App) toolGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := toInt64(args["id"])
	return a.getTorrentDetail(ctx, id)
}

func (a *App) toolPause(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return nil, a.pauseTorrent(ctx, toInt64(args["id"]))
}

func (a *App) toolResume(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return nil, a.resumeTorrent(ctx, toInt64(args["id"]))
}

func (a *App) toolRemove(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := toInt64(args["id"])
	del, _ := args["delete_files"].(bool)
	return nil, a.removeTorrent(ctx, id, del)
}

func (a *App) toolSetPriority(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return nil, a.setPriority(ctx, toInt64(args["id"]),
		int(toInt64(args["file_index"])),
		strArg(args, "priority"))
}

func (a *App) toolStats(ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	if _, err := requireProject(ctx); err != nil {
		return nil, err
	}
	return a.globalStats(ctx), nil
}

func (a *App) toolIndexersTest(ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	return a.testIndexers(a.operationContext(), ctx)
}

// ─── add / lifecycle ────────────────────────────────────────────────

func (a *App) addTorrent(ctx *sdk.AppCtx, magnet, infohash, torrentURL, targetFolder string, paused bool) (*combinedView, error) {
	pid, err := requireProject(ctx)
	if err != nil {
		return nil, err
	}
	magnet = strings.TrimSpace(magnet)
	infohash = strings.TrimSpace(infohash)
	torrentURL = strings.TrimSpace(torrentURL)
	sources := 0
	for _, source := range []string{magnet, infohash, torrentURL} {
		if source != "" {
			sources++
		}
	}
	if sources != 1 {
		return nil, errors.New("exactly one of magnet, infohash, or torrent_url is required")
	}
	if targetFolder == "" {
		targetFolder = configString(a.ctx, "default_target_folder", "/downloads")
	}
	targetFolder, err = cleanStorageFolder(targetFolder)
	if err != nil {
		return nil, err
	}

	var snap *TorrentSnapshot
	switch {
	case magnet != "":
		snap, err = a.engine.AddMagnet(magnet)
	case torrentURL != "":
		snap, err = a.engine.AddTorrentURL(torrentURL)
	default:
		snap, err = a.engine.AddInfohash(infohash)
	}
	if err != nil {
		return nil, err
	}
	row, err := upsertTorrentRow(a.ctx.AppDB(), pid, TorrentRow{
		Infohash:     snap.Infohash,
		Name:         snap.Name,
		Magnet:       magnet,
		TargetFolder: targetFolder,
		TotalBytes:   snap.Length,
		State:        snap.State,
		AddedAt:      time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		return nil, err
	}
	if paused {
		if err := a.pauseTorrent(ctx, row.ID); err != nil {
			return nil, err
		}
		row.State = "paused"
		snap.State = "paused"
		snap.IsPaused = true
	} else if err := a.engine.Resume(snap.Infohash); err != nil {
		return nil, err
	}
	emitCtx := ctx
	if emitCtx == nil {
		emitCtx = a.ctx.WithProject(projectScope())
	}
	emitCtx.Emit("torrent.added", map[string]any{
		"id": row.ID, "infohash": snap.Infohash, "name": snap.Name, "magnet": magnet,
	})
	return &combinedView{TorrentRow: *row, Snap: *snap}, nil
}

func (a *App) pauseTorrent(ctx *sdk.AppCtx, id int64) error {
	pid := projectScope(ctx)
	row, err := getTorrentRowByID(a.ctx.AppDB(), pid, id)
	if err != nil {
		return err
	}
	if _, err = a.ctx.AppDB().Exec(`UPDATE torrents SET state = 'paused' WHERE id = ? AND project_id = ?`, id, pid); err != nil {
		return err
	}
	// A global install can expose the same swarm to multiple projects.
	// Pausing one project must not stop another project's active row.
	var otherActive int
	if err := a.ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM torrents
		  WHERE infohash = ? AND id <> ? AND state <> 'paused'`,
		row.Infohash, id,
	).Scan(&otherActive); err != nil {
		return err
	}
	if otherActive == 0 {
		return a.engine.Pause(row.Infohash)
	}
	return nil
}

func (a *App) resumeTorrent(ctx *sdk.AppCtx, id int64) error {
	pid := projectScope(ctx)
	row, err := getTorrentRowByID(a.ctx.AppDB(), pid, id)
	if err != nil {
		return err
	}
	if err := a.engine.Resume(row.Infohash); err != nil {
		return err
	}
	state := "queued"
	if snap := a.engine.Snapshot(row.Infohash); snap != nil {
		state = snap.State
	}
	_, err = a.ctx.AppDB().Exec(`UPDATE torrents SET state = ? WHERE id = ? AND project_id = ?`, state, id, pid)
	return err
}

func (a *App) removeTorrent(ctx *sdk.AppCtx, id int64, deleteFiles bool) error {
	pid := projectScope(ctx)
	row, err := getTorrentRowByID(a.ctx.AppDB(), pid, id)
	if err != nil {
		return err
	}
	if deleteFiles {
		for _, fid := range storageIDsForRow(row) {
			var out map[string]any
			if err := ctx.PlatformAPI().CallAppResult("storage", "files_delete",
				map[string]any{"id": fid, "keep_record": false}, &out); err != nil {
				return fmt.Errorf("delete storage file %d: %w", fid, err)
			}
		}
	}
	if _, err = a.ctx.AppDB().Exec(
		`DELETE FROM torrents WHERE id = ? AND project_id = ?`, id, pid); err != nil {
		return err
	}
	var refs int
	if err := a.ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM torrents WHERE infohash = ?`, row.Infohash).Scan(&refs); err != nil {
		return err
	}
	if refs == 0 {
		if err := a.engine.Remove(row.Infohash, deleteFiles); err != nil && !errors.Is(err, errNotFound) {
			return err
		}
	} else if err := a.syncSharedEngineIntent(row.Infohash); err != nil {
		return err
	}
	return nil
}

func (a *App) syncSharedEngineIntent(infohash string) error {
	rows, err := torrentRowsForInfohash(a.ctx.AppDB(), infohash)
	if err != nil {
		return err
	}
	allPaused := len(rows) > 0
	for _, row := range rows {
		if row.State != "paused" {
			allPaused = false
			break
		}
	}
	if allPaused {
		if err := a.engine.Pause(infohash); err != nil {
			return err
		}
	} else if err := a.engine.Resume(infohash); err != nil {
		return err
	}
	for index, priority := range effectivePriorities(rows) {
		if err := a.engine.SetFilePriority(infohash, index, priority); err != nil && !strings.Contains(err.Error(), "metadata not available") {
			return err
		}
	}
	return nil
}

func (a *App) setPriority(ctx *sdk.AppCtx, id int64, fileIndex int, priority string) error {
	pid := projectScope(ctx)
	row, err := getTorrentRowByID(a.ctx.AppDB(), pid, id)
	if err != nil {
		return err
	}
	if _, err := parsePriority(priority); err != nil {
		return err
	}
	files, err := a.engine.FileSnapshots(row.Infohash)
	if err != nil {
		return err
	}
	if fileIndex < 0 || fileIndex >= len(files) {
		return fmt.Errorf("file_index %d out of range", fileIndex)
	}
	priorities := decodePriorities(row.FilePrioritiesJSON)
	priorities[fileIndex] = priority
	b, _ := json.Marshal(priorities)
	if _, err = a.ctx.AppDB().Exec(`UPDATE torrents SET file_priorities_json = ? WHERE id = ? AND project_id = ?`, string(b), id, pid); err != nil {
		return err
	}
	rows, err := torrentRowsForInfohash(a.ctx.AppDB(), row.Infohash)
	if err != nil {
		return err
	}
	effective := effectivePriorities(rows)
	return a.engine.SetFilePriority(row.Infohash, fileIndex, effective[fileIndex])
}

// effectivePriorities merges per-project intent for one shared swarm.
// A file is skipped physically only when every project explicitly skips
// it; otherwise the highest requested priority wins. Each project's
// completion handoff still uses its own persisted selection.
func effectivePriorities(rows []TorrentRow) map[int]string {
	keys := map[int]struct{}{}
	decoded := make([]map[int]string, len(rows))
	for i, row := range rows {
		decoded[i] = decodePriorities(row.FilePrioritiesJSON)
		for index := range decoded[i] {
			keys[index] = struct{}{}
		}
	}
	out := map[int]string{}
	for index := range keys {
		value := "skip"
		for _, priorities := range decoded {
			requested, ok := priorities[index]
			if !ok || requested == "normal" || requested == "low" {
				value = "normal"
			}
			if requested == "high" {
				value = "high"
				break
			}
		}
		out[index] = value
	}
	return out
}

// ─── views ──────────────────────────────────────────────────────────

type combinedView struct {
	TorrentRow
	Snap  TorrentSnapshot `json:"snapshot"`
	Files []FileSnapshot  `json:"files,omitempty"`
}

func (a *App) combinedTorrentList(ctx *sdk.AppCtx, stateFilter string) ([]combinedView, error) {
	stateFilter = strings.ToLower(strings.TrimSpace(stateFilter))
	switch stateFilter {
	case "", "all", "queued", "downloading", "seeding", "paused", "completed", "error":
	default:
		return nil, fmt.Errorf("unsupported state %q", stateFilter)
	}
	rows, err := listTorrentRows(a.ctx.AppDB(), projectScope(ctx), stateFilter)
	if err != nil {
		return nil, err
	}
	out := make([]combinedView, 0, len(rows))
	for _, r := range rows {
		s := a.engine.Snapshot(r.Infohash)
		if s == nil {
			// Engine doesn't have this torrent in memory (e.g. across
			// restarts before the resume path picks it up). Surface the
			// row's stored state so the panel shows it in the right
			// bucket and the operator sees any persisted error.
			s = &TorrentSnapshot{Infohash: r.Infohash, Name: r.Name, State: r.State,
				Length: r.TotalBytes, BytesCompleted: r.DownloadedBytes,
				LastError: r.LastError}
		}
		if r.State == "error" && r.LastError != "" {
			s.State = "error"
			s.LastError = r.LastError
		} else if r.State == "paused" {
			s.State = "paused"
			s.IsPaused = true
		}
		if stateFilter != "" && stateFilter != "all" && s.State != stateFilter {
			continue
		}
		out = append(out, combinedView{TorrentRow: r, Snap: *s})
	}
	return out, nil
}

func (a *App) getTorrentDetail(ctx *sdk.AppCtx, id int64) (*combinedView, error) {
	row, err := getTorrentRowByID(a.ctx.AppDB(), projectScope(ctx), id)
	if err != nil {
		return nil, err
	}
	snap := a.engine.Snapshot(row.Infohash)
	if snap == nil {
		snap = &TorrentSnapshot{Infohash: row.Infohash, Name: row.Name, State: row.State}
	}
	if row.State == "error" && row.LastError != "" {
		snap.State = "error"
		snap.LastError = row.LastError
	} else if row.State == "paused" {
		snap.State = "paused"
		snap.IsPaused = true
	}
	files, _ := a.engine.FileSnapshots(row.Infohash)
	priorities := decodePriorities(row.FilePrioritiesJSON)
	for i := range files {
		if priority, ok := priorities[files[i].Index]; ok {
			files[i].Priority = priority
		}
	}
	return &combinedView{TorrentRow: *row, Snap: *snap, Files: files}, nil
}

type GlobalStats struct {
	Aggregate          AggregateStats `json:"aggregate"`
	WorkingDirBytes    int64          `json:"working_dir_bytes"`
	WorkingDirFreeMB   int64          `json:"working_dir_free_mb"`
	IndexersConfigured int            `json:"indexers_configured"`
}

func (a *App) globalStats(ctx *sdk.AppCtx) *GlobalStats {
	projectRows, _ := listTorrentRows(a.ctx.AppDB(), projectScope(ctx), "all")
	infohashes := make(map[string]struct{}, len(projectRows))
	for _, row := range projectRows {
		infohashes[row.Infohash] = struct{}{}
	}
	agg := a.engine.AggregateStatsFor(infohashes)
	wd := resolveWorkingDir(a.ctx)
	a.statsMu.Lock()
	if a.statsScannedAt.IsZero() || time.Since(a.statsScannedAt) >= 30*time.Second {
		if used, err := dirSize(wd); err == nil {
			a.statsDirBytes = used
			a.statsScannedAt = time.Now()
		}
	}
	used := a.statsDirBytes
	a.statsMu.Unlock()
	free := freeDiskMB(wd)
	ix, _ := listIndexers(a.ctx.AppDB(), projectScope(ctx), false)
	return &GlobalStats{
		Aggregate:          agg,
		WorkingDirBytes:    used,
		WorkingDirFreeMB:   free,
		IndexersConfigured: len(ix),
	}
}

type IndexerProbe struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Kind      string `json:"kind"`
	OK        bool   `json:"ok"`
	LatencyMS int64  `json:"latency_ms"`
	Error     string `json:"error,omitempty"`
}

func (a *App) testIndexers(ctx context.Context, appCtx *sdk.AppCtx) ([]IndexerProbe, error) {
	pid, err := requireProject(appCtx)
	if err != nil {
		return nil, err
	}
	indexers, err := listIndexers(a.ctx.AppDB(), pid, true)
	if err != nil {
		return nil, err
	}
	out := make([]IndexerProbe, len(indexers))
	timeout := time.Duration(configInt(a.ctx, "indexer_query_timeout_seconds", 8)) * time.Second
	var wg sync.WaitGroup
	for i, ix := range indexers {
		wg.Add(1)
		go func(i int, ix Indexer) {
			defer wg.Done()
			t0 := time.Now()
			_, err := a.queryIndexer(ctx, ix, "test", "", timeout)
			probe := IndexerProbe{ID: ix.ID, Name: ix.Name, Kind: ix.Kind, LatencyMS: time.Since(t0).Milliseconds()}
			if err != nil {
				probe.Error = err.Error()
				a.markIndexerError(ix.ID, err.Error())
			} else {
				probe.OK = true
				a.markIndexerOK(ix.ID)
			}
			out[i] = probe
		}(i, ix)
	}
	wg.Wait()
	return out, nil
}

// ─── saved searches scheduler ──────────────────────────────────────

type SavedSearch struct {
	ID                 int64  `json:"id"`
	Name               string `json:"name"`
	Query              string `json:"query"`
	Category           string `json:"category"`
	MinSeeders         int    `json:"min_seeders"`
	MaxSizeBytes       int64  `json:"max_size_bytes"`
	ExcludeTerms       string `json:"exclude_terms"`
	AutoAddTopN        int    `json:"auto_add_top_n"`
	RunIntervalMinutes int    `json:"run_interval_minutes"`
	LastRunAt          string `json:"last_run_at,omitempty"`
	NextRunAt          string `json:"next_run_at,omitempty"`
	CreatedAt          string `json:"created_at"`
}

func addSavedSearch(db *sql.DB, pid string, s SavedSearch) (*SavedSearch, error) {
	s.Name = strings.TrimSpace(s.Name)
	s.Query = strings.TrimSpace(s.Query)
	s.Category = strings.ToLower(strings.TrimSpace(s.Category))
	if s.Query == "" {
		return nil, errors.New("saved search query required")
	}
	if s.Name == "" {
		s.Name = s.Query
	}
	switch s.Category {
	case "", "movie", "tv", "music", "book", "software":
	default:
		return nil, fmt.Errorf("unsupported category %q", s.Category)
	}
	if s.MinSeeders < 0 {
		s.MinSeeders = 0
	}
	if s.MaxSizeBytes < 0 {
		s.MaxSizeBytes = 0
	}
	if s.AutoAddTopN < 0 {
		s.AutoAddTopN = 0
	}
	if s.RunIntervalMinutes < 5 {
		s.RunIntervalMinutes = 5 // floor — don't spam indexers
	}
	now := time.Now().UTC()
	next := now.Add(time.Duration(s.RunIntervalMinutes) * time.Minute).Format(time.RFC3339)
	res, err := db.Exec(
		`INSERT INTO saved_searches
		   (project_id, name, query, category, min_seeders, max_size_bytes,
		    exclude_terms, auto_add_top_n, run_interval_minutes, next_run_at)
		 VALUES (?,?,?,?,?,?,?,?,?,?)`,
		pid, s.Name, s.Query, s.Category, s.MinSeeders, s.MaxSizeBytes,
		s.ExcludeTerms, s.AutoAddTopN, s.RunIntervalMinutes, next)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	s.ID = id
	s.NextRunAt = next
	return &s, nil
}

func listSavedSearches(db *sql.DB, pid string) ([]SavedSearch, error) {
	rows, err := db.Query(
		`SELECT id, name, query, category, min_seeders, max_size_bytes,
		        exclude_terms, auto_add_top_n, run_interval_minutes,
		        last_run_at, next_run_at, created_at
		   FROM saved_searches WHERE project_id = ? ORDER BY id`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SavedSearch{}
	for rows.Next() {
		var s SavedSearch
		var lastRun, nextRun sql.NullString
		if err := rows.Scan(&s.ID, &s.Name, &s.Query, &s.Category, &s.MinSeeders,
			&s.MaxSizeBytes, &s.ExcludeTerms, &s.AutoAddTopN, &s.RunIntervalMinutes,
			&lastRun, &nextRun, &s.CreatedAt); err != nil {
			return nil, err
		}
		s.LastRunAt = lastRun.String
		s.NextRunAt = nextRun.String
		out = append(out, s)
	}
	return out, rows.Err()
}

// runDueSearches walks saved_searches whose next_run_at is in the
// past and runs each one. Each match is checked against existing
// torrents.infohash to skip duplicates; the top auto_add_top_n new
// matches are auto-added; otherwise a torrent.search_match event is
// emitted with the result list.
func (a *App) runDueSearches(ctx context.Context) {
	now := time.Now().UTC()
	rows, err := a.ctx.AppDB().Query(
		`SELECT id, project_id, name, query, category, min_seeders, max_size_bytes,
		        exclude_terms, auto_add_top_n, run_interval_minutes
		   FROM saved_searches
		  WHERE next_run_at IS NULL OR next_run_at <= ?`,
		now.Format(time.RFC3339))
	if err != nil {
		a.ctx.Logger().Warn("scheduler", "err", err.Error())
		return
	}
	type job struct {
		ID                                             int64
		ProjectID, Name, Query, Category, ExcludeTerms string
		MinSeeders, AutoAddTopN, RunIntervalMinutes    int
		MaxSizeBytes                                   int64
	}
	var jobs []job
	for rows.Next() {
		var j job
		if err := rows.Scan(
			&j.ID, &j.ProjectID, &j.Name, &j.Query, &j.Category, &j.MinSeeders,
			&j.MaxSizeBytes, &j.ExcludeTerms, &j.AutoAddTopN, &j.RunIntervalMinutes,
		); err != nil {
			a.ctx.Logger().Warn("scheduler scan", "err", err.Error())
			continue
		}
		jobs = append(jobs, j)
	}
	rows.Close()

	excludedSet := func(s string) []string {
		out := []string{}
		for _, t := range strings.Split(s, ",") {
			t = strings.TrimSpace(strings.ToLower(t))
			if t != "" {
				out = append(out, t)
			}
		}
		return out
	}

	for _, j := range jobs {
		scoped := a.ctx.WithProject(j.ProjectID)
		results, err := a.searchIndexers(ctx, scoped, j.Query, j.Category, j.MinSeeders, "seeders")
		if err != nil {
			a.ctx.Logger().Warn("saved search", "name", j.Name, "err", err.Error())
			continue
		}
		ex := excludedSet(j.ExcludeTerms)
		filtered := results[:0]
		for _, r := range results {
			if j.MaxSizeBytes > 0 && r.SizeBytes > j.MaxSizeBytes {
				continue
			}
			if matchesAny(strings.ToLower(r.Name), ex) {
				continue
			}
			if a.alreadyHave(j.ProjectID, r.Infohash) {
				continue
			}
			filtered = append(filtered, r)
		}

		// Auto-add the top N.
		if j.AutoAddTopN > 0 {
			n := j.AutoAddTopN
			if n > len(filtered) {
				n = len(filtered)
			}
			for _, r := range filtered[:n] {
				_, err := a.addTorrent(scoped, r.Magnet, r.Infohash, r.TorrentURL, "", false)
				if err != nil {
					a.ctx.Logger().Warn("auto-add", "name", r.Name, "err", err.Error())
				}
			}
		} else {
			scoped.Emit("torrent.search_match", map[string]any{
				"search_id": j.ID,
				"query":     j.Query,
				"results":   filtered,
			})
		}

		next := now.Add(time.Duration(j.RunIntervalMinutes) * time.Minute).Format(time.RFC3339)
		_, _ = a.ctx.AppDB().Exec(
			`UPDATE saved_searches SET last_run_at = ?, next_run_at = ?
			  WHERE id = ? AND project_id = ?`,
			now.Format(time.RFC3339), next, j.ID, j.ProjectID)
	}
}

func (a *App) alreadyHave(projectID, infohash string) bool {
	if infohash == "" {
		return false
	}
	var n int
	_ = a.ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM torrents WHERE project_id = ? AND infohash = ?`,
		projectID, infohash).Scan(&n)
	return n > 0
}

func matchesAny(haystack string, needles []string) bool {
	for _, n := range needles {
		if strings.Contains(haystack, n) {
			return true
		}
	}
	return false
}

// ─── DB layer: torrents ────────────────────────────────────────────

type TorrentRow struct {
	ID                 int64  `json:"id"`
	ProjectID          string `json:"project_id"`
	Infohash           string `json:"infohash"`
	Name               string `json:"name"`
	Magnet             string `json:"magnet,omitempty"`
	TargetFolder       string `json:"target_folder"`
	TotalBytes         int64  `json:"total_bytes"`
	DownloadedBytes    int64  `json:"downloaded_bytes"`
	State              string `json:"state"`
	StorageFileIDsJSON string `json:"storage_file_ids_json"`
	UploadProgressJSON string `json:"upload_progress_json,omitempty"`
	FilePrioritiesJSON string `json:"file_priorities_json,omitempty"`
	LastError          string `json:"last_error,omitempty"`
	AddedAt            string `json:"added_at"`
	CompletedAt        string `json:"completed_at,omitempty"`
}

func upsertTorrentRow(db *sql.DB, pid string, r TorrentRow) (*TorrentRow, error) {
	_, err := db.Exec(
		`INSERT INTO torrents
		   (project_id, infohash, name, magnet, target_folder,
		    total_bytes, downloaded_bytes, state, added_at)
		 VALUES (?,?,?,?,?,?,?,?,?)
		 ON CONFLICT(project_id, infohash) DO UPDATE SET
		   name = COALESCE(NULLIF(excluded.name, ''), torrents.name),
		   target_folder = COALESCE(NULLIF(excluded.target_folder, ''), torrents.target_folder),
		   magnet = COALESCE(NULLIF(excluded.magnet, ''), torrents.magnet)`,
		pid, r.Infohash, r.Name, r.Magnet, r.TargetFolder,
		r.TotalBytes, r.DownloadedBytes, r.State, r.AddedAt)
	if err != nil {
		return nil, err
	}
	return getTorrentRow(db, pid, r.Infohash)
}

func getTorrentRow(db *sql.DB, pid, infohash string) (*TorrentRow, error) {
	return scanTorrentRow(db.QueryRow(
		`SELECT id, project_id, infohash, name, magnet, target_folder,
		        total_bytes, downloaded_bytes, state, storage_file_ids_json,
		        upload_progress_json, file_priorities_json,
		        last_error, added_at, completed_at
		   FROM torrents WHERE project_id = ? AND infohash = ?`, pid, infohash))
}

func getTorrentRowByID(db *sql.DB, pid string, id int64) (*TorrentRow, error) {
	return scanTorrentRow(db.QueryRow(
		`SELECT id, project_id, infohash, name, magnet, target_folder,
		        total_bytes, downloaded_bytes, state, storage_file_ids_json,
		        upload_progress_json, file_priorities_json,
		        last_error, added_at, completed_at
		   FROM torrents WHERE project_id = ? AND id = ?`, pid, id))
}

func scanTorrentRow(row *sql.Row) (*TorrentRow, error) {
	var r TorrentRow
	var completed sql.NullString
	err := row.Scan(&r.ID, &r.ProjectID, &r.Infohash, &r.Name, &r.Magnet, &r.TargetFolder,
		&r.TotalBytes, &r.DownloadedBytes, &r.State, &r.StorageFileIDsJSON,
		&r.UploadProgressJSON, &r.FilePrioritiesJSON,
		&r.LastError, &r.AddedAt, &completed)
	if err != nil {
		return nil, err
	}
	r.CompletedAt = completed.String
	return &r, nil
}

func listTorrentRows(db *sql.DB, pid, stateFilter string) ([]TorrentRow, error) {
	q := `SELECT id, project_id, infohash, name, magnet, target_folder,
	             total_bytes, downloaded_bytes, state, storage_file_ids_json,
	             upload_progress_json, file_priorities_json,
	             last_error, added_at, completed_at
	        FROM torrents WHERE project_id = ?`
	args := []any{pid}
	if stateFilter != "" && stateFilter != "all" {
		q += ` AND state = ?`
		args = append(args, stateFilter)
	}
	q += ` ORDER BY added_at DESC`
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TorrentRow{}
	for rows.Next() {
		var r TorrentRow
		var completed sql.NullString
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.Infohash, &r.Name, &r.Magnet,
			&r.TargetFolder, &r.TotalBytes, &r.DownloadedBytes, &r.State,
			&r.StorageFileIDsJSON, &r.UploadProgressJSON, &r.FilePrioritiesJSON,
			&r.LastError, &r.AddedAt, &completed); err != nil {
			return nil, err
		}
		r.CompletedAt = completed.String
		out = append(out, r)
	}
	return out, rows.Err()
}

func listAllTorrentRows(db *sql.DB) ([]TorrentRow, error) {
	rows, err := db.Query(
		`SELECT id, project_id, infohash, name, magnet, target_folder,
		        total_bytes, downloaded_bytes, state, storage_file_ids_json,
		        upload_progress_json, file_priorities_json,
		        last_error, added_at, completed_at
		   FROM torrents ORDER BY added_at DESC`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TorrentRow{}
	for rows.Next() {
		var r TorrentRow
		var completed sql.NullString
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.Infohash, &r.Name, &r.Magnet,
			&r.TargetFolder, &r.TotalBytes, &r.DownloadedBytes, &r.State,
			&r.StorageFileIDsJSON, &r.UploadProgressJSON, &r.FilePrioritiesJSON,
			&r.LastError, &r.AddedAt, &completed); err != nil {
			return nil, err
		}
		r.CompletedAt = completed.String
		out = append(out, r)
	}
	return out, rows.Err()
}

func decodePriorities(raw string) map[int]string {
	out := map[int]string{}
	if strings.TrimSpace(raw) == "" {
		return out
	}
	var wire map[string]string
	if json.Unmarshal([]byte(raw), &wire) != nil {
		return out
	}
	for k, v := range wire {
		if i, err := strconv.Atoi(k); err == nil && i >= 0 {
			out[i] = v
		}
	}
	return out
}

func storageIDsForRow(row *TorrentRow) []int64 {
	seen := map[int64]bool{}
	out := []int64{}
	var final []int64
	_ = json.Unmarshal([]byte(row.StorageFileIDsJSON), &final)
	for _, id := range final {
		if id > 0 && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	var partial map[string]int64
	_ = json.Unmarshal([]byte(row.UploadProgressJSON), &partial)
	for _, id := range partial {
		if id > 0 && !seen[id] {
			seen[id] = true
			out = append(out, id)
		}
	}
	return out
}

// ─── DB layer: indexers ─────────────────────────────────────────────

func addIndexer(db *sql.DB, pid, name, kind, baseURL, apiKey string, categories []string, priority int) (*Indexer, error) {
	name = strings.TrimSpace(name)
	if name == "" {
		return nil, errors.New("indexer name required")
	}
	if kind == "" {
		kind = "jackett"
	}
	switch kind {
	case "jackett", "prowlarr", "rss", "apibay":
	default:
		return nil, fmt.Errorf("unsupported indexer kind %q", kind)
	}
	u, err := url.Parse(strings.TrimSpace(baseURL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") || u.Host == "" {
		return nil, errors.New("base_url must be an absolute http(s) URL")
	}
	baseURL = u.String()
	enc, err := encryptSecret(apiKey)
	if err != nil {
		return nil, err
	}
	cats, _ := json.Marshal(categories)
	_, err = db.Exec(
		`INSERT INTO indexers
		   (project_id, name, kind, base_url, api_key_enc, categories_json, priority, enabled)
		 VALUES (?,?,?,?,?,?,?,1)
		 ON CONFLICT(project_id, name) DO UPDATE SET
		   kind = excluded.kind, base_url = excluded.base_url,
		   api_key_enc = excluded.api_key_enc,
		   categories_json = excluded.categories_json,
		   priority = excluded.priority, enabled = 1`,
		pid, name, kind, baseURL, enc, string(cats), priority)
	if err != nil {
		return nil, err
	}
	var id int64
	if err := db.QueryRow(
		`SELECT id FROM indexers WHERE project_id = ? AND name = ?`, pid, name,
	).Scan(&id); err != nil {
		return nil, err
	}
	out := &Indexer{
		ID: id, ProjectID: pid, Name: name, Kind: kind,
		BaseURL: baseURL, APIKeyEnc: enc, Categories: categories,
		Priority: priority, Enabled: true,
	}
	return out, nil
}

// ─── secret crypto (AES-GCM, plaintext fallback) ───────────────────

func secretKey() ([]byte, error) {
	raw := os.Getenv("APTEVA_SECRET")
	if raw == "" && globalApp != nil && globalApp.ctx != nil {
		raw = configString(globalApp.ctx, "shared_secret", "")
	}
	if raw == "" {
		return nil, errors.New("no secret")
	}
	k, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("APTEVA_SECRET not base64: %w", err)
	}
	if len(k) != 32 {
		return nil, fmt.Errorf("APTEVA_SECRET must decode to 32 bytes, got %d", len(k))
	}
	return k, nil
}

func encryptSecret(s string) (string, error) {
	if s == "" {
		return "", nil
	}
	key, err := secretKey()
	if err != nil {
		return s, nil
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	nonce := make([]byte, g.NonceSize())
	if _, err := io.ReadFull(rand.Reader, nonce); err != nil {
		return "", err
	}
	ct := g.Seal(nonce, nonce, []byte(s), nil)
	return "enc::" + base64.StdEncoding.EncodeToString(ct), nil
}

func decryptSecret(stored string) (string, error) {
	if !strings.HasPrefix(stored, "enc::") {
		return stored, nil
	}
	key, err := secretKey()
	if err != nil {
		return "", fmt.Errorf("encrypted secret but no key: %w", err)
	}
	ct, err := base64.StdEncoding.DecodeString(strings.TrimPrefix(stored, "enc::"))
	if err != nil {
		return "", err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return "", err
	}
	g, err := cipher.NewGCM(block)
	if err != nil {
		return "", err
	}
	if len(ct) < g.NonceSize() {
		return "", errors.New("ciphertext too short")
	}
	nonce, body := ct[:g.NonceSize()], ct[g.NonceSize():]
	pt, err := g.Open(nil, nonce, body, nil)
	if err != nil {
		return "", err
	}
	return string(pt), nil
}

// ─── small helpers ──────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, v any) {
	writeJSONStatus(w, http.StatusOK, v)
}

func writeJSONStatus(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func decodeJSONBody(r *http.Request, dst any) error {
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("invalid JSON body: %w", err)
	}
	if dec.Decode(&struct{}{}) != io.EOF {
		return errors.New("invalid JSON body: multiple values")
	}
	return nil
}

func (a *App) requestContext(w http.ResponseWriter, r *http.Request) (*sdk.AppCtx, bool) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return nil, false
	}
	return a.ctx.WithProject(pid), true
}

func resolveProjectFromRequest(r *http.Request) (string, error) {
	if pid := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); pid != "" {
		return pid, nil
	}
	if pid := strings.TrimSpace(r.URL.Query().Get("project_id")); pid != "" {
		return pid, nil
	}
	return "", errors.New("project_id required for a global torrent install")
}

func projectScope(ctxs ...*sdk.AppCtx) string {
	if len(ctxs) > 0 && ctxs[0] != nil {
		if pid := strings.TrimSpace(ctxs[0].CurrentProject()); pid != "" {
			return pid
		}
	}
	if pid := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); pid != "" {
		return pid
	}
	return ""
}

func requireProject(ctx *sdk.AppCtx) (string, error) {
	pid := projectScope(ctx)
	if pid == "" {
		return "", errors.New("torrent: missing project context")
	}
	return pid, nil
}

func (a *App) operationContext() context.Context {
	if a.runCtx != nil {
		return a.runCtx
	}
	return context.Background()
}

func strArg(args map[string]any, k string) string {
	if v, ok := args[k].(string); ok {
		return v
	}
	return ""
}

func toInt64(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case float64:
		return int64(x)
	case string:
		n, _ := strconv.ParseInt(x, 10, 64)
		return n
	}
	return 0
}

// resolveWorkingDir picks the engine scratch directory in this order:
// explicit working_dir config → APTEVA_DATA_DIR/torrents → ./torrents.
// Hardcoding /data/torrents only worked inside the container layout
// where /data is a writable mount; on a macOS dev host the root is
// read-only and OnMount panicked.
func resolveWorkingDir(ctx *sdk.AppCtx) string {
	if v := configString(ctx, "working_dir", ""); v != "" {
		return v
	}
	if d := ctx.DataDir(); d != "" {
		return filepath.Join(d, "torrents")
	}
	return "torrents"
}

func configString(ctx *sdk.AppCtx, key, def string) string {
	if ctx == nil {
		return def
	}
	if v, ok := ctx.Config()[key]; ok && v != "" {
		return v
	}
	return def
}

func configInt(ctx *sdk.AppCtx, key string, def int) int {
	s := configString(ctx, key, "")
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(s)
	if err != nil {
		return def
	}
	return n
}

func configFloat(ctx *sdk.AppCtx, key string, def float64) float64 {
	s := configString(ctx, key, "")
	if s == "" {
		return def
	}
	n, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return def
	}
	return n
}

func configFlag(ctx *sdk.AppCtx, key string, def bool) bool {
	s := strings.ToLower(configString(ctx, key, ""))
	switch s {
	case "true", "1", "yes", "on":
		return true
	case "false", "0", "no", "off":
		return false
	}
	return def
}

func cleanStorageFolder(folder string) (string, error) {
	folder = strings.TrimSpace(strings.ReplaceAll(folder, "\\", "/"))
	if folder == "" {
		return "/", nil
	}
	for _, component := range strings.Split(folder, "/") {
		if component == ".." {
			return "", errors.New("target_folder must not contain '..'")
		}
	}
	cleaned := path.Clean("/" + strings.TrimPrefix(folder, "/"))
	return cleaned, nil
}

func dirSize(path string) (int64, error) {
	var total int64
	st, err := os.Lstat(path)
	if err != nil {
		return 0, err
	}
	if st.Mode()&os.ModeSymlink != 0 {
		return 0, nil
	}
	if !st.IsDir() {
		return st.Size(), nil
	}
	entries, err := os.ReadDir(path)
	if err != nil {
		return 0, err
	}
	for _, e := range entries {
		sub := filepath.Join(path, e.Name())
		s, err := dirSize(sub)
		if err != nil {
			return 0, err
		}
		total += s
	}
	return total, nil
}

// freeDiskMB returns the available disk space at `path` in MiB.
// Returns -1 when the filesystem does not expose free-space stats so
// the panel can render an unavailable value instead of a false zero.
func freeDiskMB(path string) int64 {
	free, err := availableDiskBytes(path)
	if err != nil {
		return -1
	}
	return free / (1024 * 1024)
}

// ─── main ───────────────────────────────────────────────────────────

func main() {
	globalAppOnce.Do(func() {
		globalApp = &App{}
	})
	sdk.Run(globalApp)
}
