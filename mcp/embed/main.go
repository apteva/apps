// Embed creates public viewer URLs and oEmbed-compatible metadata for
// storage-backed assets. Storage owns bytes and URL signing; media can
// enrich metadata later. This app owns the publishing surface.
package main

import (
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"

	"github.com/google/uuid"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

//go:embed apteva.yaml
var manifestYAML []byte

var globalCtx *sdk.AppCtx

type App struct{}

type Embed struct {
	ID               int64  `json:"id"`
	Token            string `json:"token"`
	ProjectID        string `json:"project_id"`
	StorageFileID    int64  `json:"storage_file_id"`
	StorageProjectID string `json:"storage_project_id,omitempty"`
	Title            string `json:"title"`
	Name             string `json:"name"`
	ContentType      string `json:"content_type"`
	SizeBytes        int64  `json:"size_bytes"`
	Width            int    `json:"width"`
	Height           int    `json:"height"`
	Status           string `json:"status"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
}

type StorageFile struct {
	ID          int64  `json:"id"`
	ProjectID   string `json:"project_id"`
	Name        string `json:"name"`
	ContentType string `json:"content_type"`
	SizeBytes   int64  `json:"size_bytes"`
	Visibility  string `json:"visibility"`
	URL         string `json:"url"`
}

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest(manifestYAML)
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("embed requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("embed mounted", "version", "0.1.1")
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/embeds", Handler: a.handleEmbedsCollection},
		{Pattern: "/embeds/", Handler: a.handleEmbedsItem},
		{Pattern: "/embed/", Handler: a.handleViewer, NoAuth: true},
		{Pattern: "/oembed", Handler: a.handleOEmbed, NoAuth: true},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "embed_create",
			Description: "Create a shareable viewer and oEmbed record for a storage file. Args: storage_file_id, title?, width?, height?, storage_project_id?. Returns viewer_url, oembed_url, and iframe html.",
			InputSchema: schemaObject(map[string]any{
				"storage_file_id":    map[string]any{"type": "integer"},
				"title":              map[string]any{"type": "string"},
				"width":              map[string]any{"type": "integer"},
				"height":             map[string]any{"type": "integer"},
				"storage_project_id": map[string]any{"type": "string"},
			}, []string{"storage_file_id"}),
			Handler: a.toolCreate,
		},
		{
			Name:        "embed_get",
			Description: "Fetch one embed by id, token, or viewer URL. Args: id? token? url?. Returns viewer_url, oembed_url, and iframe html.",
			InputSchema: schemaObject(map[string]any{
				"id":    map[string]any{"type": "integer"},
				"token": map[string]any{"type": "string"},
				"url":   map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolGet,
		},
		{
			Name:        "embed_list",
			Description: "List recent embeds in the current project. Args: limit?.",
			InputSchema: schemaObject(map[string]any{
				"limit": map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolList,
		},
		{
			Name:        "embed_delete",
			Description: "Delete one embed record. Args: id or token.",
			InputSchema: schemaObject(map[string]any{
				"id":    map[string]any{"type": "integer"},
				"token": map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolDelete,
		},
		{
			Name:        "embed_refresh",
			Description: "Re-resolve storage metadata for an embed. Args: id or token.",
			InputSchema: schemaObject(map[string]any{
				"id":    map[string]any{"type": "integer"},
				"token": map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolRefresh,
		},
	}
}

func (a *App) toolCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProject(args)
	if err != nil {
		return nil, err
	}
	fileID := int64Arg(args, "storage_file_id")
	if fileID == 0 {
		return nil, errors.New("storage_file_id required")
	}
	f, err := fetchStorageFile(ctx, fileID, strArg(args, "storage_project_id"))
	if err != nil {
		return nil, err
	}
	width := intArg(args, "width", defaultInt(ctx, "default_width", 640))
	height := intArg(args, "height", defaultInt(ctx, "default_height", 360))
	if width <= 0 {
		width = 640
	}
	if height <= 0 {
		height = heightForContent(f.ContentType, width, 360)
	}
	title := strings.TrimSpace(strArg(args, "title"))
	if title == "" {
		title = f.Name
	}
	e := &Embed{
		Token:            newToken(),
		ProjectID:        pid,
		StorageFileID:    f.ID,
		StorageProjectID: firstNonEmpty(strArg(args, "storage_project_id"), f.ProjectID, pid),
		Title:            title,
		Name:             f.Name,
		ContentType:      f.ContentType,
		SizeBytes:        f.SizeBytes,
		Width:            width,
		Height:           height,
		Status:           "active",
	}
	if err := dbInsertEmbed(ctx.AppDB(), e); err != nil {
		return nil, err
	}
	return a.envelope(ctx, e), nil
}

func (a *App) toolGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	e, err := a.lookupEmbed(ctx, args)
	if err != nil {
		return nil, err
	}
	return a.envelope(ctx, e), nil
}

func (a *App) toolList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProject(args)
	if err != nil {
		return nil, err
	}
	limit := intArg(args, "limit", 50)
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := dbListEmbeds(ctx.AppDB(), pid, limit)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]any, 0, len(rows))
	for _, e := range rows {
		out = append(out, a.envelope(ctx, e))
	}
	return map[string]any{"embeds": out, "count": len(out)}, nil
}

func (a *App) toolDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	e, err := a.lookupEmbed(ctx, args)
	if err != nil {
		return nil, err
	}
	if err := dbDeleteEmbed(ctx.AppDB(), e.ProjectID, e.ID); err != nil {
		return nil, err
	}
	return map[string]any{"deleted": true, "id": e.ID, "token": e.Token}, nil
}

func (a *App) toolRefresh(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	e, err := a.lookupEmbed(ctx, args)
	if err != nil {
		return nil, err
	}
	f, err := fetchStorageFile(ctx, e.StorageFileID, e.StorageProjectID)
	if err != nil {
		return nil, err
	}
	e.Name = f.Name
	e.ContentType = f.ContentType
	e.SizeBytes = f.SizeBytes
	if e.Title == "" {
		e.Title = f.Name
	}
	if err := dbUpdateStorageMeta(ctx.AppDB(), e); err != nil {
		return nil, err
	}
	return a.envelope(ctx, e), nil
}

func (a *App) lookupEmbed(ctx *sdk.AppCtx, args map[string]any) (*Embed, error) {
	pid, err := resolveProject(args)
	if err != nil {
		return nil, err
	}
	token := strArg(args, "token")
	if token == "" {
		token = tokenFromURL(strArg(args, "url"))
	}
	if token != "" {
		return dbGetEmbedByToken(ctx.AppDB(), pid, token)
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id, token, or url required")
	}
	return dbGetEmbedByID(ctx.AppDB(), pid, id)
}

func (a *App) handleEmbedsCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		out, err := a.toolList(globalCtx, map[string]any{
			"_project_id": r.URL.Query().Get("project_id"),
			"limit":       r.URL.Query().Get("limit"),
		})
		writeToolResult(w, out, err)
	case http.MethodPost:
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		body["_project_id"] = r.URL.Query().Get("project_id")
		out, err := a.toolCreate(globalCtx, body)
		writeToolResult(w, out, err)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "GET or POST")
	}
}

func (a *App) handleEmbedsItem(w http.ResponseWriter, r *http.Request) {
	tokenOrID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/embeds/"), "/")
	args := map[string]any{"_project_id": r.URL.Query().Get("project_id")}
	if id, err := strconv.ParseInt(tokenOrID, 10, 64); err == nil {
		args["id"] = id
	} else {
		args["token"] = tokenOrID
	}
	switch r.Method {
	case http.MethodGet:
		out, err := a.toolGet(globalCtx, args)
		writeToolResult(w, out, err)
	case http.MethodPatch:
		out, err := a.toolRefresh(globalCtx, args)
		writeToolResult(w, out, err)
	case http.MethodDelete:
		out, err := a.toolDelete(globalCtx, args)
		writeToolResult(w, out, err)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "GET, PATCH or DELETE")
	}
}

func (a *App) handleViewer(w http.ResponseWriter, r *http.Request) {
	token := strings.Trim(strings.TrimPrefix(r.URL.Path, "/embed/"), "/")
	if token == "" {
		http.NotFound(w, r)
		return
	}
	e, err := dbGetEmbedByTokenAnyProject(globalCtx.AppDB(), token)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if e == nil || e.Status != "active" {
		http.NotFound(w, r)
		return
	}
	contentURL, err := storageSignedURL(globalCtx, e.StorageFileID, e.StorageProjectID)
	if err != nil {
		httpErr(w, http.StatusBadGateway, err.Error())
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write([]byte(renderViewerHTML(e, contentURL)))
}

func (a *App) handleOEmbed(w http.ResponseWriter, r *http.Request) {
	rawURL := r.URL.Query().Get("url")
	token := tokenFromURL(rawURL)
	if token == "" {
		httpErr(w, http.StatusBadRequest, "url must point to an embed viewer")
		return
	}
	e, err := dbGetEmbedByTokenAnyProject(globalCtx.AppDB(), token)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if e == nil || e.Status != "active" {
		http.NotFound(w, r)
		return
	}
	resp := oEmbedFor(globalCtx, e)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "private, max-age=300")
	_ = json.NewEncoder(w).Encode(resp)
}

func (a *App) envelope(ctx *sdk.AppCtx, e *Embed) map[string]any {
	viewer := viewerURL(ctx, e.Token)
	oembed := oEmbedURL(ctx, viewer)
	return map[string]any{
		"embed":      e,
		"viewer_url": viewer,
		"oembed_url": oembed,
		"html":       iframeHTML(viewer, e.Width, e.Height, e.Title),
	}
}

func fetchStorageFile(ctx *sdk.AppCtx, id int64, projectID string) (*StorageFile, error) {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return nil, errors.New("embed: no platform client; cannot reach storage")
	}
	args := map[string]any{"id": id}
	if projectID != "" {
		args["_project_id"] = projectID
	}
	var out struct {
		File  *StorageFile `json:"file"`
		Found bool         `json:"found"`
	}
	if err := ctx.PlatformAPI().CallAppResult("storage", "files_get", args, &out); err != nil {
		return nil, fmt.Errorf("storage.files_get: %w", err)
	}
	if out.File == nil || !out.Found {
		return nil, fmt.Errorf("storage file %d not found", id)
	}
	if out.File.ID == 0 {
		out.File.ID = id
	}
	return out.File, nil
}

func storageSignedURL(ctx *sdk.AppCtx, id int64, projectID string) (string, error) {
	args := map[string]any{"id": id, "ttl_seconds": signedURLTTL(ctx)}
	if projectID != "" {
		args["_project_id"] = projectID
	}
	var out struct {
		URL string `json:"url"`
	}
	if err := ctx.PlatformAPI().CallAppResult("storage", "files_get_url", args, &out); err != nil {
		return "", fmt.Errorf("storage.files_get_url: %w", err)
	}
	if out.URL == "" {
		return "", errors.New("storage returned empty signed URL")
	}
	return absolutize(ctx, out.URL), nil
}

func renderViewerHTML(e *Embed, contentURL string) string {
	title := html.EscapeString(firstNonEmpty(e.Title, e.Name, "Embed"))
	body := viewerBody(e, contentURL)
	return "<!doctype html><html><head><meta charset=\"utf-8\">" +
		"<meta name=\"viewport\" content=\"width=device-width,initial-scale=1\">" +
		"<title>" + title + "</title>" +
		"<style>html,body{margin:0;height:100%;background:#0b0d10;color:#eef2f7;font-family:Inter,system-ui,-apple-system,BlinkMacSystemFont,'Segoe UI',sans-serif}body{display:flex;align-items:center;justify-content:center}.wrap{width:100%;height:100%;display:flex;align-items:center;justify-content:center}.fallback{max-width:520px;padding:24px;line-height:1.45}.fallback a{color:#8ab4ff}video,img,iframe{max-width:100%;max-height:100%;border:0}audio{width:min(680px,90vw)}</style>" +
		"</head><body><main class=\"wrap\">" + body + "</main></body></html>"
}

func viewerBody(e *Embed, contentURL string) string {
	src := html.EscapeString(contentURL)
	title := html.EscapeString(firstNonEmpty(e.Title, e.Name, "Embed"))
	ct := strings.ToLower(e.ContentType)
	switch {
	case strings.HasPrefix(ct, "video/"):
		return "<video src=\"" + src + "\" controls playsinline preload=\"metadata\" title=\"" + title + "\"></video>"
	case strings.HasPrefix(ct, "audio/"):
		return "<audio src=\"" + src + "\" controls preload=\"metadata\" title=\"" + title + "\"></audio>"
	case strings.HasPrefix(ct, "image/"):
		return "<img src=\"" + src + "\" alt=\"" + title + "\">"
	case ct == "application/pdf":
		return "<iframe src=\"" + src + "\" title=\"" + title + "\" style=\"width:100%;height:100%\"></iframe>"
	default:
		return "<section class=\"fallback\"><h1>" + title + "</h1><p>This file can be opened from storage.</p><p><a href=\"" + src + "\">Open file</a></p></section>"
	}
}

func oEmbedFor(ctx *sdk.AppCtx, e *Embed) map[string]any {
	viewer := viewerURL(ctx, e.Token)
	out := map[string]any{
		"version":       "1.0",
		"type":          oEmbedType(e.ContentType),
		"provider_name": "Apteva Embed",
		"provider_url":  publicBase(ctx),
		"title":         firstNonEmpty(e.Title, e.Name, "Embed"),
		"width":         e.Width,
		"height":        e.Height,
		"html":          iframeHTML(viewer, e.Width, e.Height, e.Title),
	}
	return out
}

func oEmbedType(contentType string) string {
	ct := strings.ToLower(contentType)
	if strings.HasPrefix(ct, "video/") {
		return "video"
	}
	return "rich"
}

func iframeHTML(src string, width, height int, title string) string {
	if width <= 0 {
		width = 640
	}
	if height <= 0 {
		height = 360
	}
	return fmt.Sprintf(`<iframe src="%s" width="%d" height="%d" title="%s" frameborder="0" allow="fullscreen; picture-in-picture" allowfullscreen loading="lazy"></iframe>`,
		html.EscapeString(src), width, height, html.EscapeString(firstNonEmpty(title, "Embed")))
}

func viewerURL(ctx *sdk.AppCtx, token string) string {
	return publicBase(ctx) + "/api/apps/embed/embed/" + url.PathEscape(token)
}

func oEmbedURL(ctx *sdk.AppCtx, viewer string) string {
	return publicBase(ctx) + "/api/apps/embed/oembed?url=" + url.QueryEscape(viewer)
}

func publicBase(ctx *sdk.AppCtx) string {
	if ctx != nil {
		if info, err := ctx.PlatformInfo(); err == nil && info != nil && strings.TrimSpace(info.PublicURL) != "" {
			return strings.TrimRight(strings.TrimSpace(info.PublicURL), "/")
		}
	}
	for _, key := range []string{"EMBED_PUBLIC_URL", "APTEVA_PUBLIC_URL"} {
		if v := strings.TrimSpace(os.Getenv(key)); v != "" {
			return strings.TrimRight(v, "/")
		}
	}
	return ""
}

func absolutize(ctx *sdk.AppCtx, raw string) string {
	if strings.HasPrefix(raw, "http://") || strings.HasPrefix(raw, "https://") {
		return raw
	}
	base := publicBase(ctx)
	if base == "" {
		return raw
	}
	if strings.HasPrefix(raw, "/") {
		return base + raw
	}
	return base + "/" + raw
}

func tokenFromURL(raw string) string {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return ""
	}
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	path := u.EscapedPath()
	if path == "" {
		path = raw
	}
	for _, marker := range []string{"/api/apps/embed/embed/", "/embed/"} {
		if i := strings.Index(path, marker); i >= 0 {
			tok, _ := url.PathUnescape(strings.Trim(strings.TrimPrefix(path[i:], marker), "/"))
			if j := strings.IndexByte(tok, '/'); j >= 0 {
				tok = tok[:j]
			}
			return tok
		}
	}
	return ""
}

func resolveProject(args map[string]any) (string, error) {
	for _, key := range []string{"_project_id", "project_id"} {
		if v := strings.TrimSpace(strArg(args, key)); v != "" {
			return v, nil
		}
	}
	if v := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); v != "" {
		return v, nil
	}
	return "", errors.New("project_id required")
}

func dbInsertEmbed(db *sql.DB, e *Embed) error {
	res, err := db.Exec(`
		INSERT INTO embeds (token, project_id, storage_file_id, storage_project_id,
			title, name, content_type, size_bytes, width, height, status)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		e.Token, e.ProjectID, e.StorageFileID, e.StorageProjectID,
		e.Title, e.Name, e.ContentType, e.SizeBytes, e.Width, e.Height, e.Status)
	if err != nil {
		return err
	}
	e.ID, _ = res.LastInsertId()
	return dbGetInto(db, e.ProjectID, e.ID, e)
}

func dbGetEmbedByID(db *sql.DB, pid string, id int64) (*Embed, error) {
	e := &Embed{}
	if err := dbGetInto(db, pid, id, e); err != nil {
		return nil, err
	}
	return e, nil
}

func dbGetEmbedByToken(db *sql.DB, pid, token string) (*Embed, error) {
	e := &Embed{}
	err := db.QueryRow(`
		SELECT id, token, project_id, storage_file_id, storage_project_id, title,
		       name, content_type, size_bytes, width, height, status, created_at, updated_at
		  FROM embeds
		 WHERE project_id = ? AND token = ? AND status = 'active'`,
		pid, token,
	).Scan(&e.ID, &e.Token, &e.ProjectID, &e.StorageFileID, &e.StorageProjectID, &e.Title,
		&e.Name, &e.ContentType, &e.SizeBytes, &e.Width, &e.Height, &e.Status, &e.CreatedAt, &e.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, errors.New("embed not found")
	}
	return e, err
}

func dbGetEmbedByTokenAnyProject(db *sql.DB, token string) (*Embed, error) {
	e := &Embed{}
	err := db.QueryRow(`
		SELECT id, token, project_id, storage_file_id, storage_project_id, title,
		       name, content_type, size_bytes, width, height, status, created_at, updated_at
		  FROM embeds
		 WHERE token = ? AND status = 'active'`,
		token,
	).Scan(&e.ID, &e.Token, &e.ProjectID, &e.StorageFileID, &e.StorageProjectID, &e.Title,
		&e.Name, &e.ContentType, &e.SizeBytes, &e.Width, &e.Height, &e.Status, &e.CreatedAt, &e.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return e, err
}

func dbGetInto(db *sql.DB, pid string, id int64, e *Embed) error {
	err := db.QueryRow(`
		SELECT id, token, project_id, storage_file_id, storage_project_id, title,
		       name, content_type, size_bytes, width, height, status, created_at, updated_at
		  FROM embeds
		 WHERE project_id = ? AND id = ? AND status = 'active'`,
		pid, id,
	).Scan(&e.ID, &e.Token, &e.ProjectID, &e.StorageFileID, &e.StorageProjectID, &e.Title,
		&e.Name, &e.ContentType, &e.SizeBytes, &e.Width, &e.Height, &e.Status, &e.CreatedAt, &e.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return errors.New("embed not found")
	}
	return err
}

func dbListEmbeds(db *sql.DB, pid string, limit int) ([]*Embed, error) {
	rows, err := db.Query(`
		SELECT id, token, project_id, storage_file_id, storage_project_id, title,
		       name, content_type, size_bytes, width, height, status, created_at, updated_at
		  FROM embeds
		 WHERE project_id = ? AND status = 'active'
		 ORDER BY created_at DESC, id DESC
		 LIMIT ?`, pid, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Embed{}
	for rows.Next() {
		e := &Embed{}
		if err := rows.Scan(&e.ID, &e.Token, &e.ProjectID, &e.StorageFileID, &e.StorageProjectID, &e.Title,
			&e.Name, &e.ContentType, &e.SizeBytes, &e.Width, &e.Height, &e.Status, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func dbUpdateStorageMeta(db *sql.DB, e *Embed) error {
	_, err := db.Exec(`
		UPDATE embeds
		   SET name = ?, content_type = ?, size_bytes = ?, title = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE project_id = ? AND id = ?`,
		e.Name, e.ContentType, e.SizeBytes, e.Title, e.ProjectID, e.ID)
	if err != nil {
		return err
	}
	return dbGetInto(db, e.ProjectID, e.ID, e)
}

func dbDeleteEmbed(db *sql.DB, pid string, id int64) error {
	_, err := db.Exec(`UPDATE embeds SET status = 'deleted', updated_at = CURRENT_TIMESTAMP WHERE project_id = ? AND id = ?`, pid, id)
	return err
}

func writeToolResult(w http.ResponseWriter, out any, err error) {
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func signedURLTTL(ctx *sdk.AppCtx) int {
	return defaultInt(ctx, "signed_url_ttl_seconds", 86400)
}

func defaultInt(ctx *sdk.AppCtx, key string, fallback int) int {
	if ctx == nil {
		return fallback
	}
	n, err := strconv.Atoi(strings.TrimSpace(ctx.Config().Get(key)))
	if err != nil || n <= 0 {
		return fallback
	}
	return n
}

func heightForContent(contentType string, width, fallback int) int {
	if strings.HasPrefix(strings.ToLower(contentType), "video/") && width > 0 {
		return int(float64(width) * 9 / 16)
	}
	return fallback
}

func newToken() string {
	return strings.ReplaceAll(uuid.NewString(), "-", "")
}

func strArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	switch v := args[key].(type) {
	case string:
		return v
	case fmt.Stringer:
		return v.String()
	case float64:
		if v == float64(int64(v)) {
			return strconv.FormatInt(int64(v), 10)
		}
		return strconv.FormatFloat(v, 'f', -1, 64)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	}
	return ""
}

func intArg(args map[string]any, key string, fallback int) int {
	if args == nil {
		return fallback
	}
	switch v := args[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case json.Number:
		n, _ := v.Int64()
		return int(n)
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err == nil {
			return n
		}
	}
	return fallback
}

func int64Arg(args map[string]any, key string) int64 {
	if args == nil {
		return 0
	}
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
		n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return n
	}
	return 0
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}

func schemaObject(props map[string]any, required []string) map[string]any {
	if props == nil {
		props = map[string]any{}
	}
	o := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		o["required"] = required
	}
	return o
}

func main() {
	sdk.Run(&App{})
}
