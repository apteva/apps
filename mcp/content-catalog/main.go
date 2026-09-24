package main

import (
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

//go:embed apteva.yaml
var manifestBytes []byte

var globalCtx *sdk.AppCtx

type App struct{}

func main() { sdk.Run(&App{}) }

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest(manifestBytes)
	if err != nil {
		panic(err)
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("content-catalog requires its own database")
	}
	globalCtx = ctx
	return nil
}
func (a *App) OnUnmount(*sdk.AppCtx) error    { return nil }
func (a *App) Channels() []sdk.ChannelFactory { return nil }
func (a *App) Workers() []sdk.Worker          { return nil }
func (a *App) EventHandlers() []sdk.EventHandler {
	return []sdk.EventHandler{{Event: "media.completed", Handler: a.onMediaCompleted}}
}

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/overview", Handler: a.handleOverview},
		{Pattern: "/search", Handler: a.handleSearch},
		{Pattern: "/brands", Handler: a.handleList("content_catalog_brands_list")},
		{Pattern: "/sessions", Handler: a.handleList("content_catalog_sessions_list")},
		{Pattern: "/sessions/", Handler: a.handleSession},
		{Pattern: "/assets", Handler: a.handleList("content_catalog_assets_list")},
		{Pattern: "/import-preview", Handler: a.handleList("content_catalog_import_preview")},
		{Pattern: "/assets/", Handler: a.handleAsset},
		{Pattern: "/publications", Handler: a.handleList("content_catalog_asset_publications_list")},
		{Pattern: "/posts", Handler: a.handleList("content_catalog_posts_list")},
		{Pattern: "/hostings", Handler: a.handleList("content_catalog_hosting_list")},
		{Pattern: "/video-hosts", Handler: a.handleVideoHosts},
		{Pattern: "/action", Handler: a.handleAction},
	}
}

func schema(required ...string) map[string]any {
	return map[string]any{"type": "object", "additionalProperties": true, "required": required}
}

func searchSchema() map[string]any {
	field := func(description string) map[string]any {
		return map[string]any{"type": "string", "description": description}
	}
	return map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"entity_type":   map[string]any{"type": "string", "enum": []string{"all", "assets", "sessions"}, "description": "Result type; default all."},
			"query":         field("Text in Catalog file names and session notes or titles."),
			"brand_id":      field("Limit results to one explicit Catalog brand ID."),
			"session_id":    field("Limit asset results to one session ID."),
			"date_from":     field("Inclusive YYYY-MM-DD session date."),
			"date_to":       field("Inclusive YYYY-MM-DD session date."),
			"kind":          field("Asset kind, such as video, image, or audio."),
			"lineage":       map[string]any{"type": "string", "enum": []string{"source", "derivative"}, "description": "Asset without or with linked parent sources."},
			"sort":          map[string]any{"type": "string", "enum": []string{"session_newest", "asset_newest"}, "description": "Asset order; default session_newest. Other result types sort by their own date."},
			"review_status": map[string]any{"type": "string", "enum": []string{"pending", "approved", "rejected"}},
			"destination":   field("Network or channel, such as instagram. Required for destination availability filters."),
			"account_ref":   field("Specific destination account or tier reference; requires destination."),
			"availability":  map[string]any{"type": "string", "enum": []string{"any", "never_used", "not_published", "ready_to_publish", "scheduled", "published", "failed"}, "description": "Asset publication state. published means verified live; ready_to_publish requires approved review and no active publication for the destination/account."},
			"limit":         map[string]any{"type": "integer", "minimum": 1, "maximum": 100, "description": "Maximum items per result type; default 30."},
			"cursors":       map[string]any{"type": "object", "properties": map[string]any{"assets": field("next_cursor for assets"), "sessions": field("next_cursor for sessions")}, "additionalProperties": false},
		},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{Name: "content_catalog_overview", Description: "Count brands, sessions, assets, per-asset publication records, and hosting records.", InputSchema: schema(), Handler: a.overview},
		{Name: "content_catalog_search", Description: "Read-only search of linked sessions and assets. Use brand_id, destination, account_ref, availability=ready_to_publish, entity_type=assets to find approved assets with no active publication for that account. Published means verified live. No Storage scan or external write.", InputSchema: searchSchema(), Handler: a.search},
		{Name: "content_catalog_brands_create", Description: "Create a brand. Args: slug, name, storage_root; optional host_provider, host_connection_id, host_library_id, host_collection_id. Writes only Catalog.", InputSchema: schema("slug", "name", "storage_root"), Handler: a.brandCreate},
		{Name: "content_catalog_brands_list", Description: "List brands.", InputSchema: schema(), Handler: a.brandsList},
		{Name: "content_catalog_brands_update", Description: "Update a brand's name, storage_root, or host settings. Args: id and fields to change. Writes only Catalog.", InputSchema: schema("id"), Handler: a.brandUpdate},
		{Name: "content_catalog_sessions_create", Description: "Create a stable production session. Args: brand_id, title; optional session_date (YYYY-MM-DD, empty means unknown), notes, host_collection_id. Writes only Catalog.", InputSchema: schema("brand_id", "title"), Handler: a.sessionCreate},
		{Name: "content_catalog_sessions_list", Description: "List sessions; brand_id optional.", InputSchema: schema(), Handler: a.sessionsList},
		{Name: "content_catalog_sessions_get", Description: "Get one session with assets and linked Gigs. Args: id.", InputSchema: schema("id"), Handler: a.sessionGet},
		{Name: "content_catalog_sessions_update", Description: "Edit an existing session's title, notes, recording date, or optional host_collection_id override. Args: id and fields to change. Empty session_date means unknown. Storage folder remains stable.", InputSchema: schema("id"), Handler: a.sessionUpdate},
		{Name: "content_catalog_sessions_link_gig", Description: "Read an existing Gig, then link it to a Catalog session. Args: session_id, gig_id, role?. Does not change Gigs.", InputSchema: schema("session_id", "gig_id"), Handler: a.sessionLinkGig},
		{Name: "content_catalog_assets_attach", Description: "Read an existing Storage file, then link it to a session. Args: session_id, storage_file_id, kind?. Does not upload or change Storage.", InputSchema: schema("session_id", "storage_file_id"), Handler: a.assetAttach},
		{Name: "content_catalog_session_upload_target", Description: "Return the exact Storage folder and install ID for explicit uploads into a session. Does not scan or upload. Args: session_id.", InputSchema: schema("session_id"), Handler: a.sessionUploadTarget},
		{Name: "content_catalog_assets_attach_uploaded", Description: "Attach a file uploaded to the session's exact Storage folder after verifying its Storage metadata. Idempotent. Args: session_id, storage_file_id.", InputSchema: schema("session_id", "storage_file_id"), Handler: a.assetAttachUploaded},
		{Name: "content_catalog_import_preview", Description: "Read up to 200 Storage files under a session brand root. Return candidates needing human review; no files or Catalog records are changed. Args: session_id.", InputSchema: schema("session_id"), Handler: a.importPreview},
		{Name: "content_catalog_assets_list", Description: "List assets; session_id required.", InputSchema: schema("session_id"), Handler: a.assetsList},
		{Name: "content_catalog_assets_get", Description: "Get an asset with source lineage, hosting, and per-asset publication records. Args: id.", InputSchema: schema("id"), Handler: a.assetGet},
		{Name: "content_catalog_assets_link_source", Description: "Record one source relationship, supporting multi-input derivatives. Args: child_asset_id, source_asset_id, relation?, source_order?, media_render_id?.", InputSchema: schema("child_asset_id", "source_asset_id"), Handler: a.assetLinkSource},
		{Name: "content_catalog_assets_review", Description: "Set a Catalog asset's editorial review_status to pending, approved, or rejected. Args: asset_id, review_status.", InputSchema: schema("asset_id", "review_status"), Handler: a.assetReview},
		{Name: "content_catalog_asset_publications_list", Description: "List the platforms and observed post details for one asset. Args: asset_id.", InputSchema: schema("asset_id"), Handler: a.assetPublicationsList},
		{Name: "content_catalog_asset_publications_record", Description: "Create or update a publication record on one asset. Args: asset_id, destination (new record), status, publication_id? (update), account_ref?, audience?, planned_at?, actual_at?, external_post_id?, external_url?, evidence_source?, failure_details?. Verified live requires evidence and URL or post ID. Writes only Catalog; never publishes externally.", InputSchema: schema("asset_id", "status"), Handler: a.assetPublicationRecord},
		{Name: "content_catalog_posts_list", Description: "List shared platform posts; optional session_id, asset_id, brand_id. Each post contains its asset IDs and one observed outcome.", InputSchema: schema(), Handler: a.postsList},
		{Name: "content_catalog_posts_get", Description: "Get one shared platform post and its asset IDs. Args: id.", InputSchema: schema("id"), Handler: a.postsGet},
		{Name: "content_catalog_posts_record", Description: "Create or update a shared platform post. Args: asset_ids (one or more same-brand Catalog assets), destination and status for new posts; post_id for updates. Supports title, account_ref, audience, planned_at, actual_at, external_post_id, external_url, evidence_source, failure_details. Writes Catalog evidence only; does not publish externally.", InputSchema: schema("status"), Handler: a.postsRecord},
		{Name: "content_catalog_hosting_request", Description: "REAL EXTERNAL HOSTING: request an approved asset's video upload to the brand's video host. Bunny Stream is supported. Does not publish to a channel.", InputSchema: schema("asset_id"), Handler: a.hostingRequest},
		{Name: "content_catalog_hosting_check", Description: "Fetch provider readiness for one hosting id and update Catalog's observation. Args: id.", InputSchema: schema("id"), Handler: a.hostingCheck},
		{Name: "content_catalog_hosting_link_existing", Description: "Backfill a video asset with an existing Bunny GUID using only get_video. Args: asset_id, remote_id, connection_id. Confirms library, collection, duration, and readiness; never calls fetch_video. Media checksum is supporting Storage evidence, not a Bunny source-file match.", InputSchema: schema("asset_id", "remote_id", "connection_id"), Handler: a.hostingLinkExisting},
		{Name: "content_catalog_hosting_list", Description: "List hosting records for an asset. Args: asset_id.", InputSchema: schema("asset_id"), Handler: a.hostingsList},
	}
}

func toolMap(a *App) map[string]sdk.ToolHandler {
	out := map[string]sdk.ToolHandler{}
	for _, t := range a.MCPTools() {
		out[t.Name] = t.Handler
	}
	return out
}

func requestProject(r *http.Request) (string, error) {
	if env := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); env != "" {
		return env, nil
	}
	if pid := strings.TrimSpace(r.URL.Query().Get("project_id")); pid != "" {
		return pid, nil
	}
	return "", errors.New("project_id required")
}

func (a *App) callHTTP(w http.ResponseWriter, r *http.Request, name string, args map[string]any) {
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
	args["_project_id"] = pid
	h, ok := toolMap(a)[name]
	if !ok {
		http.Error(w, "unknown action", http.StatusNotFound)
		return
	}
	out, err := h(ctx, args)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func (a *App) handleOverview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", 405)
		return
	}
	a.callHTTP(w, r, "content_catalog_overview", map[string]any{})
}
func (a *App) handleSearch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", http.StatusMethodNotAllowed)
		return
	}
	args := map[string]any{}
	for _, key := range []string{"entity_type", "query", "brand_id", "session_id", "date_from", "date_to", "kind", "lineage", "sort", "review_status", "destination", "account_ref", "availability", "limit", "assets_cursor", "sessions_cursor", "releases_cursor"} {
		if v := r.URL.Query().Get(key); v != "" {
			args[key] = v
		}
	}
	a.callHTTP(w, r, "content_catalog_search", args)
}
func (a *App) handleVideoHosts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", 405)
		return
	}
	pid, err := requestProject(r)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	if globalCtx == nil {
		http.Error(w, "app not mounted", 503)
		return
	}
	ctx := globalCtx.WithProject(pid)
	out := []map[string]any{}
	for _, b := range ctx.IntegrationsFor("video_host") {
		if b != nil && b.ConnectionID > 0 {
			out = append(out, map[string]any{"connection_id": b.ConnectionID, "provider": b.AppSlug, "default": b.IsDefault})
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"hosts": out})
}
func (a *App) handleList(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "GET only", 405)
			return
		}
		args := map[string]any{}
		for _, key := range []string{"brand_id", "session_id", "asset_id"} {
			if v := r.URL.Query().Get(key); v != "" {
				args[key] = v
			}
		}
		a.callHTTP(w, r, name, args)
	}
}
func (a *App) handleSession(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", 405)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/sessions/")
	if strings.HasSuffix(id, "/preview") {
		a.servePreview(w, r, "session", strings.TrimSuffix(id, "/preview"))
		return
	}
	a.callHTTP(w, r, "content_catalog_sessions_get", map[string]any{"id": id})
}
func (a *App) handleAsset(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", 405)
		return
	}
	id := strings.TrimPrefix(r.URL.Path, "/assets/")
	if strings.HasSuffix(id, "/preview") {
		a.servePreview(w, r, "asset", strings.TrimSuffix(id, "/preview"))
		return
	}
	a.callHTTP(w, r, "content_catalog_assets_get", map[string]any{"id": id})
}
func (a *App) handleRelease(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET only", 405)
		return
	}
	a.callHTTP(w, r, "content_catalog_releases_get", map[string]any{"id": strings.TrimPrefix(r.URL.Path, "/releases/")})
}
func (a *App) handleAction(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", 405)
		return
	}
	var body struct {
		Tool string         `json:"tool"`
		Args map[string]any `json:"args"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&body); err != nil {
		http.Error(w, "invalid JSON", 400)
		return
	}
	if body.Args == nil {
		body.Args = map[string]any{}
	}
	a.callHTTP(w, r, body.Tool, body.Args)
}

func project(ctx *sdk.AppCtx) (string, error) {
	if ctx == nil || strings.TrimSpace(ctx.CurrentProject()) == "" {
		return "", errors.New("project context required")
	}
	return ctx.CurrentProject(), nil
}
func str(args map[string]any, key string) string {
	return strings.TrimSpace(fmt.Sprint(value(args, key)))
}
func value(args map[string]any, key string) any {
	if v, ok := args[key]; ok && v != nil {
		return v
	}
	return ""
}
func number(args map[string]any, key string) int64 {
	switch v := args[key].(type) {
	case int64:
		return v
	case int:
		return int64(v)
	case float64:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(v, 10, 64)
		return n
	}
	return 0
}
func required(args map[string]any, keys ...string) error {
	for _, key := range keys {
		if str(args, key) == "" {
			return fmt.Errorf("%s required", key)
		}
	}
	return nil
}
func newID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic(err)
	}
	b[6] = (b[6] & 0x0f) | 0x40
	b[8] = (b[8] & 0x3f) | 0x80
	s := hex.EncodeToString(b[:])
	return s[:8] + "-" + s[8:12] + "-" + s[12:16] + "-" + s[16:20] + "-" + s[20:]
}

var slugRE = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

func limit(v int64) int64 {
	if v < 1 {
		return 100
	}
	if v > 200 {
		return 200
	}
	return v
}
func now() string { return time.Now().UTC().Format(time.RFC3339) }

func (a *App) overview(ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	pid, err := project(ctx)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	for key, table := range map[string]string{"brands": "brands", "sessions": "sessions", "assets": "assets", "publications": "posts", "hostings": "hostings"} {
		var n int64
		if err := ctx.AppDB().QueryRow("SELECT COUNT(*) FROM "+table+" WHERE project_id=?", pid).Scan(&n); err != nil {
			return nil, err
		}
		out[key] = n
	}
	return out, nil
}

func (a *App) brandCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := project(ctx)
	if err != nil {
		return nil, err
	}
	if err = required(args, "slug", "name", "storage_root"); err != nil {
		return nil, err
	}
	slug, name, root := str(args, "slug"), str(args, "name"), str(args, "storage_root")
	if !slugRE.MatchString(slug) {
		return nil, errors.New("slug must be lower-case letters, numbers, and hyphens")
	}
	if !strings.HasPrefix(root, "/") || !strings.HasSuffix(root, "/") || strings.Contains(root, "..") {
		return nil, errors.New("storage_root must be an absolute folder ending in / without ..")
	}
	provider, connectionID, libraryID := str(args, "host_provider"), number(args, "host_connection_id"), str(args, "host_library_id")
	if provider != "" {
		if connectionID <= 0 || libraryID == "" {
			return nil, errors.New("host_connection_id and host_library_id required for video hosting")
		}
		if err = validateHostBinding(ctx, provider, connectionID); err != nil {
			return nil, err
		}
	}
	id := newID()
	_, err = ctx.AppDB().Exec(`INSERT INTO brands(id,project_id,slug,name,storage_root,host_provider,host_connection_id,host_library_id,host_collection_id) VALUES(?,?,?,?,?,?,?,?,?)`, id, pid, slug, name, root, provider, connectionID, libraryID, str(args, "host_collection_id"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"brand": map[string]any{"id": id, "slug": slug, "name": name, "storage_root": root, "host_provider": provider, "host_connection_id": connectionID, "host_library_id": libraryID, "host_collection_id": str(args, "host_collection_id")}}, nil
}

type Brand struct {
	ID               string `json:"id"`
	Slug             string `json:"slug"`
	Name             string `json:"name"`
	StorageRoot      string `json:"storage_root"`
	HostProvider     string `json:"host_provider"`
	HostConnectionID int64  `json:"host_connection_id"`
	HostLibraryID    string `json:"host_library_id"`
	HostCollectionID string `json:"host_collection_id"`
}

func brandByID(db *sql.DB, pid, id string) (*Brand, error) {
	b := &Brand{}
	err := db.QueryRow(`SELECT id,slug,name,storage_root,host_provider,host_connection_id,host_library_id,host_collection_id FROM brands WHERE project_id=? AND id=?`, pid, id).Scan(&b.ID, &b.Slug, &b.Name, &b.StorageRoot, &b.HostProvider, &b.HostConnectionID, &b.HostLibraryID, &b.HostCollectionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("brand not found")
	}
	return b, err
}
func (a *App) brandsList(ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	pid, err := project(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := ctx.AppDB().Query(`SELECT id,slug,name,storage_root,host_provider,host_connection_id,host_library_id,host_collection_id FROM brands WHERE project_id=? ORDER BY name`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Brand{}
	for rows.Next() {
		var b Brand
		if err = rows.Scan(&b.ID, &b.Slug, &b.Name, &b.StorageRoot, &b.HostProvider, &b.HostConnectionID, &b.HostLibraryID, &b.HostCollectionID); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return map[string]any{"brands": out}, rows.Err()
}
func (a *App) brandUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := project(ctx)
	if err != nil {
		return nil, err
	}
	if err = required(args, "id"); err != nil {
		return nil, err
	}
	b, err := brandByID(ctx.AppDB(), pid, str(args, "id"))
	if err != nil {
		return nil, err
	}
	if _, ok := args["name"]; ok {
		if str(args, "name") == "" {
			return nil, errors.New("name cannot be empty")
		}
		b.Name = str(args, "name")
	}
	if _, ok := args["storage_root"]; ok {
		root := str(args, "storage_root")
		if !strings.HasPrefix(root, "/") || !strings.HasSuffix(root, "/") || strings.Contains(root, "..") {
			return nil, errors.New("invalid storage_root")
		}
		b.StorageRoot = root
	}
	for key, set := range map[string]func(){"host_provider": func() { b.HostProvider = str(args, "host_provider") }, "host_library_id": func() { b.HostLibraryID = str(args, "host_library_id") }, "host_collection_id": func() { b.HostCollectionID = str(args, "host_collection_id") }, "host_connection_id": func() { b.HostConnectionID = number(args, "host_connection_id") }} {
		if _, ok := args[key]; ok {
			set()
		}
	}
	if b.HostProvider != "" {
		if b.HostConnectionID <= 0 || b.HostLibraryID == "" {
			return nil, errors.New("video host connection and library required")
		}
		if err = validateHostBinding(ctx, b.HostProvider, b.HostConnectionID); err != nil {
			return nil, err
		}
	}
	_, err = ctx.AppDB().Exec(`UPDATE brands SET name=?,storage_root=?,host_provider=?,host_connection_id=?,host_library_id=?,host_collection_id=?,updated_at=? WHERE project_id=? AND id=?`, b.Name, b.StorageRoot, b.HostProvider, b.HostConnectionID, b.HostLibraryID, b.HostCollectionID, now(), pid, b.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"brand": b}, nil
}

type Session struct {
	ID               string `json:"id"`
	BrandID          string `json:"brand_id"`
	Title            string `json:"title"`
	Date             string `json:"session_date"`
	Status           string `json:"status"`
	Notes            string `json:"notes"`
	StorageFolder    string `json:"storage_folder"`
	HostCollectionID string `json:"host_collection_id"`
}

func sessionByID(db *sql.DB, pid, id string) (*Session, error) {
	s := &Session{}
	err := db.QueryRow(`SELECT id,brand_id,title,session_date,status,notes,storage_folder,host_collection_id FROM sessions WHERE project_id=? AND id=?`, pid, id).Scan(&s.ID, &s.BrandID, &s.Title, &s.Date, &s.Status, &s.Notes, &s.StorageFolder, &s.HostCollectionID)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("session not found")
	}
	return s, err
}
func (a *App) sessionCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := project(ctx)
	if err != nil {
		return nil, err
	}
	if err = required(args, "brand_id", "title"); err != nil {
		return nil, err
	}
	brand, err := brandByID(ctx.AppDB(), pid, str(args, "brand_id"))
	if err != nil {
		return nil, err
	}
	date := str(args, "session_date")
	if date != "" && !validSessionDate(date) {
		return nil, errors.New("session_date must be YYYY-MM-DD")
	}
	collection := str(args, "host_collection_id")
	if err = validateSessionCollection(brand, collection); err != nil {
		return nil, err
	}
	id := newID()
	s := Session{ID: id, BrandID: brand.ID, Title: str(args, "title"), Date: date, Status: "planned", Notes: str(args, "notes"), HostCollectionID: collection}
	s.StorageFolder = sessionStorageFolder(brand.StorageRoot, &s)
	_, err = ctx.AppDB().Exec(`INSERT INTO sessions(id,project_id,brand_id,title,session_date,notes,storage_folder,host_collection_id) VALUES(?,?,?,?,?,?,?,?)`, id, pid, brand.ID, s.Title, date, s.Notes, s.StorageFolder, collection)
	if err != nil {
		return nil, err
	}
	ctx.EmitWithProject("content-catalog.session.created", pid, map[string]any{"id": id, "brand_id": str(args, "brand_id")})
	return map[string]any{"session": s}, nil
}

func validSessionDate(date string) bool {
	parsed, err := time.Parse("2006-01-02", date)
	return err == nil && parsed.Format("2006-01-02") == date
}

func validateSessionCollection(brand *Brand, collection string) error {
	if collection == "" {
		return nil
	}
	if brand.HostProvider == "" || brand.HostConnectionID <= 0 || brand.HostLibraryID == "" {
		return errors.New("session collection override requires a configured brand video host")
	}
	if len(collection) > 128 || strings.ContainsAny(collection, " /\\\r\n\t") {
		return errors.New("invalid host_collection_id")
	}
	return nil
}

func (a *App) sessionUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := project(ctx)
	if err != nil {
		return nil, err
	}
	if err = required(args, "id"); err != nil {
		return nil, err
	}
	s, err := sessionByID(ctx.AppDB(), pid, str(args, "id"))
	if err != nil {
		return nil, err
	}
	if _, ok := args["title"]; ok {
		if str(args, "title") == "" {
			return nil, errors.New("title cannot be empty")
		}
		s.Title = str(args, "title")
	}
	if _, ok := args["notes"]; ok {
		s.Notes = str(args, "notes")
	}
	if _, ok := args["session_date"]; ok {
		s.Date = str(args, "session_date")
		if s.Date != "" && !validSessionDate(s.Date) {
			return nil, errors.New("session_date must be YYYY-MM-DD")
		}
	}
	if _, ok := args["host_collection_id"]; ok {
		brand, e := brandByID(ctx.AppDB(), pid, s.BrandID)
		if e != nil {
			return nil, e
		}
		s.HostCollectionID = str(args, "host_collection_id")
		if e = validateSessionCollection(brand, s.HostCollectionID); e != nil {
			return nil, e
		}
	}
	_, err = ctx.AppDB().Exec(`UPDATE sessions SET title=?,notes=?,session_date=?,host_collection_id=?,updated_at=? WHERE project_id=? AND id=?`, s.Title, s.Notes, s.Date, s.HostCollectionID, now(), pid, s.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"session": s}, nil
}
func (a *App) sessionsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := project(ctx)
	if err != nil {
		return nil, err
	}
	q := `SELECT id,brand_id,title,session_date,status,notes,storage_folder,host_collection_id FROM sessions WHERE project_id=?`
	params := []any{pid}
	if v := str(args, "brand_id"); v != "" {
		q += " AND brand_id=?"
		params = append(params, v)
	}
	q += " ORDER BY session_date DESC,id DESC LIMIT 200"
	rows, err := ctx.AppDB().Query(q, params...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Session{}
	for rows.Next() {
		var s Session
		if err = rows.Scan(&s.ID, &s.BrandID, &s.Title, &s.Date, &s.Status, &s.Notes, &s.StorageFolder, &s.HostCollectionID); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return map[string]any{"sessions": out}, rows.Err()
}
func (a *App) sessionGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := project(ctx)
	if err != nil {
		return nil, err
	}
	s, err := sessionByID(ctx.AppDB(), pid, str(args, "id"))
	if err != nil {
		return nil, err
	}
	assetsAny, err := a.assetsList(ctx, map[string]any{"session_id": s.ID})
	if err != nil {
		return nil, err
	}
	rows, err := ctx.AppDB().Query(`SELECT gigs_install_id,gig_id,role FROM session_gigs WHERE project_id=? AND session_id=? ORDER BY linked_at`, pid, s.ID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	gigs := []map[string]any{}
	for rows.Next() {
		var installID, gigID int64
		var role string
		if err = rows.Scan(&installID, &gigID, &role); err != nil {
			return nil, err
		}
		gigs = append(gigs, map[string]any{"gigs_install_id": installID, "gig_id": gigID, "role": role})
	}
	return map[string]any{"session": s, "assets": assetsAny.(map[string]any)["assets"], "gigs": gigs}, rows.Err()
}
func (a *App) sessionLinkGig(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := project(ctx)
	if err != nil {
		return nil, err
	}
	if err = required(args, "session_id"); err != nil {
		return nil, err
	}
	if number(args, "gig_id") <= 0 {
		return nil, errors.New("gig_id required")
	}
	if _, err = sessionByID(ctx.AppDB(), pid, str(args, "session_id")); err != nil {
		return nil, err
	}
	bound := ctx.IntegrationFor("gigs")
	if bound == nil || bound.InstallID <= 0 {
		return nil, errors.New("Gigs app is not bound")
	}
	var result map[string]any
	if err = ctx.PlatformAPI().CallAppResult("gigs", "gigs_status", map[string]any{"_project_id": pid, "id": number(args, "gig_id")}, &result); err != nil {
		return nil, fmt.Errorf("check Gig: %w", err)
	}
	if result["gig"] == nil {
		return nil, errors.New("Gig not found")
	}
	_, err = ctx.AppDB().Exec(`INSERT OR IGNORE INTO session_gigs(project_id,session_id,gigs_install_id,gig_id,role) VALUES(?,?,?,?,?)`, pid, str(args, "session_id"), bound.InstallID, number(args, "gig_id"), str(args, "role"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"linked": true, "gig_id": number(args, "gig_id")}, nil
}
