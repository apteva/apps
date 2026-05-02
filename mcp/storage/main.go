// Storage v0.1 — file storage with virtual folders, signed URLs, dedup hints.
//
// Disk layout:
//
//   <STORAGE_BLOBS_DIR>/<sha256[:2]>/<storage_key>
//
// Each upload writes a fresh storage_key; sha256 is recorded for ETag
// and dedup checks but bytes are not yet shared between rows. v0.2
// reference-counts and reclaims.
package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/google/uuid"
	_ "modernc.org/sqlite"
)

// ─── Manifest (also lives in apteva.yaml) ──────────────────────────

const manifestYAML = `schema: apteva-app/v1
name: storage
display_name: Storage
version: 0.4.1
description: |
  File storage with virtual folders, signed URLs, dedup. Local-disk
  backend; cloud backend slot reserved for v0.2.
author: Apteva
scopes: [project, global]
requires:
  permissions:
    - db.write.app
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: files_upload,           description: "Upload bytes (base64). Returns id, url, sha256." }
    - { name: files_get,              description: "Metadata for one file." }
    - { name: files_get_url,          description: "Mint a signed time-limited URL." }
    - { name: files_search,           description: "Filtered file list." }
    - { name: files_list,             description: "List files in one folder." }
    - { name: files_list_folders,     description: "List immediate child folders." }
    - { name: files_move,             description: "Move + optionally rename a file." }
    - { name: files_set_tags,         description: "Append/remove tags." }
    - { name: files_set_visibility,   description: "private | signed | public." }
    - { name: files_dedupe_check,     description: "Find an existing file by sha256." }
    - { name: files_delete,           description: "Soft-delete a file." }
    - { name: files_from_url,         description: "Fetch a URL into storage." }
  ui_panels:
    - slot: project.page
      label: Files
      icon: folder
      entry: /ui/StoragePanel.mjs
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/storage
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/storage.db
  migrations: migrations/
upgrade_policy: auto-patch
`

type App struct{}

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("storage requires a db block")
	}
	globalCtx = ctx
	if err := os.MkdirAll(blobsDir(ctx), 0755); err != nil {
		return fmt.Errorf("mkdir blobs: %w", err)
	}
	if err := os.MkdirAll(uploadsDir(ctx), 0755); err != nil {
		return fmt.Errorf("mkdir uploads: %w", err)
	}
	ctx.Logger().Info("storage mounted",
		"scope_project_id", os.Getenv("APTEVA_PROJECT_ID"),
		"blobs_dir", blobsDir(ctx),
		"uploads_dir", uploadsDir(ctx))

	// Background sweeper for stale upload sessions. Runs once on
	// boot (so a restart immediately reclaims anything older than
	// 24h) and then hourly. Goroutine has no shutdown — apps don't
	// have a clean teardown signal in v0.1, but the sweeper does
	// nothing destructive past the TTL gate so an abrupt exit is
	// fine.
	go func() {
		sweepStaleUploads(ctx)
		t := time.NewTicker(time.Hour)
		defer t.Stop()
		for range t.C {
			sweepStaleUploads(ctx)
		}
	}()
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error          { return nil }
func (a *App) Channels() []sdk.ChannelFactory       { return nil }
func (a *App) Workers() []sdk.Worker                { return nil }
func (a *App) EventHandlers() []sdk.EventHandler    { return nil }

// ─── HTTP routes ────────────────────────────────────────────────────

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		// /files/* dispatches in-handler by method + path tail.
		{Pattern: "/files", Handler: a.handleFilesCollection},
		{Pattern: "/files/", Handler: a.handleFilesItem},
		{Pattern: "/folders", Handler: a.handleFolders},
		// Resumable upload protocol (browser-only — not in MCP). See
		// uploads.go for the on-disk session layout + endpoint shapes.
		{Pattern: "/uploads", Handler: a.handleUploadsCollection},
		{Pattern: "/uploads/", Handler: a.handleUploadsItem},
	}
}

// ─── MCP tools ─────────────────────────────────────────────────────

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "files_upload",
			Description: "Upload bytes via base64. Args: name, content_base64, folder?, content_type?, tags?, source?, visibility?. Returns {id, url, sha256, was_existing} — was_existing=true means an identical file already existed and the upload returned its id.",
			InputSchema: schemaObject(map[string]any{
				"name":            map[string]any{"type": "string"},
				"content_base64":  map[string]any{"type": "string"},
				"folder":          map[string]any{"type": "string"},
				"content_type":    map[string]any{"type": "string"},
				"tags":            map[string]any{"type": "array"},
				"source":          map[string]any{"type": "string"},
				"visibility":      map[string]any{"type": "string"},
			}, []string{"name", "content_base64"}),
			Handler: a.toolUpload,
		},
		{
			Name:        "files_get",
			Description: "Fetch metadata for one file. Args: id.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolGet,
		},
		{
			Name:        "files_get_url",
			Description: "Mint a signed time-limited URL. Args: id, ttl_seconds?. Returns {url, expires_at}.",
			InputSchema: schemaObject(map[string]any{
				"id":          map[string]any{"type": "integer"},
				"ttl_seconds": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolGetURL,
		},
		{
			Name:        "files_search",
			Description: "Filtered list. Args: q?, folder?, content_type?, sha256?, tag?, source?, limit?.",
			InputSchema: schemaObject(map[string]any{
				"q":            map[string]any{"type": "string"},
				"folder":       map[string]any{"type": "string"},
				"content_type": map[string]any{"type": "string"},
				"sha256":       map[string]any{"type": "string"},
				"tag":          map[string]any{"type": "string"},
				"source":       map[string]any{"type": "string"},
				"limit":        map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolSearch,
		},
		{
			Name:        "files_list",
			Description: "List files in one folder. Args: folder (default '/'), recursive?, limit?.",
			InputSchema: schemaObject(map[string]any{
				"folder":    map[string]any{"type": "string"},
				"recursive": map[string]any{"type": "boolean"},
				"limit":     map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolList,
		},
		{
			Name:        "files_list_folders",
			Description: "List immediate child folders one level under a parent (default '/'). Args: parent?.",
			InputSchema: schemaObject(map[string]any{
				"parent": map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolListFolders,
		},
		{
			Name:        "files_move",
			Description: "Move and/or rename a file. Args: id, folder?, name?.",
			InputSchema: schemaObject(map[string]any{
				"id":     map[string]any{"type": "integer"},
				"folder": map[string]any{"type": "string"},
				"name":   map[string]any{"type": "string"},
			}, []string{"id"}),
			Handler: a.toolMove,
		},
		{
			Name:        "files_set_tags",
			Description: "Replace a file's tags. Args: id, tags (array of strings).",
			InputSchema: schemaObject(map[string]any{
				"id":   map[string]any{"type": "integer"},
				"tags": map[string]any{"type": "array"},
			}, []string{"id", "tags"}),
			Handler: a.toolSetTags,
		},
		{
			Name:        "files_set_visibility",
			Description: "Change visibility. Args: id, visibility (private | signed | public).",
			InputSchema: schemaObject(map[string]any{
				"id":         map[string]any{"type": "integer"},
				"visibility": map[string]any{"type": "string"},
			}, []string{"id", "visibility"}),
			Handler: a.toolSetVisibility,
		},
		{
			Name:        "files_dedupe_check",
			Description: "Find an existing file by sha256. Args: sha256. Returns {found, file?}.",
			InputSchema: schemaObject(map[string]any{
				"sha256": map[string]any{"type": "string"},
			}, []string{"sha256"}),
			Handler: a.toolDedupe,
		},
		{
			Name:        "files_delete",
			Description: "Soft-delete a file. Args: id.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolDelete,
		},
		{
			Name:        "files_from_url",
			Description: "Fetch a remote URL into storage. Args: url, name?, folder?, tags?.",
			InputSchema: schemaObject(map[string]any{
				"url":    map[string]any{"type": "string"},
				"name":   map[string]any{"type": "string"},
				"folder": map[string]any{"type": "string"},
				"tags":   map[string]any{"type": "array"},
			}, []string{"url"}),
			Handler: a.toolFromURL,
		},
	}
}

func main() { sdk.Run(&App{}) }

// ─── Upload payload decoding ───────────────────────────────────────

// decodeUploadPayload accepts either a plain base64 string (legacy
// path — direct base64 from the agent or an HTTP client) or a
// {"_binary": true, "base64": ..., ...} envelope. The envelope is
// what apteva-core's MCP proxy substitutes when the agent passes a
// blobref://<id> file handle as the argument value (see
// blobs.go RehydrateFileRefs); without unwrapping it here, the
// JSON would fail base64 decode and reject the upload.
func decodeUploadPayload(raw string) ([]byte, error) {
	trimmed := strings.TrimSpace(raw)
	if strings.HasPrefix(trimmed, "{") && strings.Contains(trimmed, `"_binary"`) {
		var env struct {
			Binary bool   `json:"_binary"`
			Base64 string `json:"base64"`
		}
		if err := json.Unmarshal([]byte(trimmed), &env); err == nil && env.Binary && env.Base64 != "" {
			body, err := base64.StdEncoding.DecodeString(env.Base64)
			if err != nil {
				return nil, fmt.Errorf("invalid base64 in _binary envelope: %w", err)
			}
			return body, nil
		}
	}
	body, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return nil, fmt.Errorf("invalid base64: %w", err)
	}
	return body, nil
}

// ─── Project resolution ────────────────────────────────────────────

func resolveProjectFromArgs(args map[string]any) (string, error) {
	if env := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); env != "" {
		return env, nil
	}
	if v, ok := args["_project_id"].(string); ok && v != "" {
		return v, nil
	}
	return "", errors.New("project_id missing — pass _project_id when scope=global")
}

func resolveProjectFromRequest(r *http.Request) (string, error) {
	if env := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); env != "" {
		return env, nil
	}
	if v := r.URL.Query().Get("project_id"); v != "" {
		return v, nil
	}
	return "", errors.New("project_id required in query string when install scope=global")
}

// ─── Domain types ──────────────────────────────────────────────────

type File struct {
	ID          int64    `json:"id"`
	ProjectID   string   `json:"project_id,omitempty"`
	Name        string   `json:"name"`
	Folder      string   `json:"folder"`
	StorageKey  string   `json:"storage_key,omitempty"`
	ContentType string   `json:"content_type,omitempty"`
	SizeBytes   int64    `json:"size_bytes"`
	SHA256      string   `json:"sha256"`
	UploadedBy  string   `json:"uploaded_by,omitempty"`
	Source      string   `json:"source,omitempty"`
	Tags        []string `json:"tags"`
	Visibility  string   `json:"visibility"`
	URL         string   `json:"url,omitempty"`
	CreatedAt   string   `json:"created_at,omitempty"`
	UpdatedAt   string   `json:"updated_at,omitempty"`
}

// ─── Path / folder normalisation ───────────────────────────────────

// normaliseFolder ensures the folder string is well-formed:
// always starts and ends with "/", no doubled slashes, no segments
// of just "." or "..", root is "/".
func normaliseFolder(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "/"
	}
	if !strings.HasPrefix(s, "/") {
		s = "/" + s
	}
	if !strings.HasSuffix(s, "/") {
		s = s + "/"
	}
	// Collapse multiple slashes.
	for strings.Contains(s, "//") {
		s = strings.ReplaceAll(s, "//", "/")
	}
	// Reject path-traversal-ish segments. We don't touch the FS by
	// folder name but rejecting them keeps the data clean and the
	// dashboard breadcrumbs predictable.
	for _, seg := range strings.Split(s, "/") {
		if seg == "." || seg == ".." {
			return "/"
		}
	}
	return s
}

// normaliseFilename strips path separators from the name field —
// the folder column is the only place a slash should ever appear.
func normaliseFilename(s string) string {
	s = strings.TrimSpace(s)
	s = strings.ReplaceAll(s, "/", "_")
	s = strings.ReplaceAll(s, "\\", "_")
	if s == "" {
		s = "untitled"
	}
	return s
}

// ─── Tool handlers ─────────────────────────────────────────────────

func (a *App) toolUpload(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	name := normaliseFilename(strArg(args, "name"))
	if name == "" {
		return nil, errors.New("name required")
	}
	b64 := strArg(args, "content_base64")
	if b64 == "" {
		return nil, errors.New("content_base64 required")
	}
	body, err := decodeUploadPayload(b64)
	if err != nil {
		return nil, err
	}
	if maxBytes := maxUploadBytes(ctx); int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("upload exceeds max_upload_size_mb (%d bytes > %d)", len(body), maxBytes)
	}
	in := uploadInput{
		Name:        name,
		Folder:      normaliseFolder(strArg(args, "folder")),
		ContentType: strArg(args, "content_type"),
		Tags:        strArrayArg(args, "tags"),
		Source:      strArg(args, "source"),
		Visibility:  visibilityOrDefault(strArg(args, "visibility")),
	}
	f, existed, err := saveBytes(ctx, pid, in, body)
	if err != nil {
		return nil, err
	}
	emitFileEvent(ctx, "file.added", f, existed)
	return map[string]any{
		"id":           f.ID,
		"url":          buildContentURL(f),
		"sha256":       f.SHA256,
		"size_bytes":   f.SizeBytes,
		"folder":       f.Folder,
		"name":         f.Name,
		"was_existing": existed,
	}, nil
}

func (a *App) toolGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	f, err := dbGetByID(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if f == nil {
		return map[string]any{"file": nil, "found": false}, nil
	}
	return map[string]any{"file": f, "found": true}, nil
}

func (a *App) toolGetURL(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	f, err := dbGetByID(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if f == nil {
		return nil, errors.New("file not found")
	}
	ttl := intArg(args, "ttl_seconds", defaultSignedTTL(ctx))
	if ttl <= 0 {
		ttl = defaultSignedTTL(ctx)
	}
	exp := time.Now().Add(time.Duration(ttl) * time.Second).Unix()
	sig := signFile(f.ID, exp)
	url := fmt.Sprintf("/files/%d/content?sig=%s&exp=%d", f.ID, sig, exp)
	return map[string]any{"url": url, "expires_at": exp, "file_id": f.ID}, nil
}

func (a *App) toolSearch(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	limit := intArg(args, "limit", 50)
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	out, err := dbSearch(ctx.AppDB(), pid, searchOpts{
		Q:           strArg(args, "q"),
		Folder:      strArg(args, "folder"),
		ContentType: strArg(args, "content_type"),
		SHA256:      strArg(args, "sha256"),
		Tag:         strArg(args, "tag"),
		Source:      strArg(args, "source"),
		Limit:       limit,
	})
	if err != nil {
		return nil, err
	}
	return map[string]any{"files": out, "count": len(out)}, nil
}

func (a *App) toolList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	folder := normaliseFolder(strArg(args, "folder"))
	recursive, _ := args["recursive"].(bool)
	limit := intArg(args, "limit", 200)
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	out, err := dbListFolder(ctx.AppDB(), pid, folder, recursive, limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{"files": out, "count": len(out), "folder": folder, "recursive": recursive}, nil
}

func (a *App) toolListFolders(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	parent := normaliseFolder(strArg(args, "parent"))
	folders, err := dbListChildFolders(ctx.AppDB(), pid, parent)
	if err != nil {
		return nil, err
	}
	return map[string]any{"folders": folders, "count": len(folders), "parent": parent}, nil
}

func (a *App) toolMove(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	updates := map[string]any{}
	if v, ok := args["folder"]; ok {
		if s, ok := v.(string); ok {
			updates["folder"] = normaliseFolder(s)
		}
	}
	if v, ok := args["name"]; ok {
		if s, ok := v.(string); ok {
			updates["name"] = normaliseFilename(s)
		}
	}
	if len(updates) == 0 {
		return nil, errors.New("specify folder and/or name")
	}
	f, err := dbUpdate(ctx.AppDB(), pid, id, updates)
	if err != nil {
		return nil, err
	}
	return map[string]any{"file": f}, nil
}

func (a *App) toolSetTags(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	tags := strArrayArg(args, "tags")
	raw, _ := json.Marshal(tags)
	f, err := dbUpdate(ctx.AppDB(), pid, id, map[string]any{"tags": string(raw)})
	if err != nil {
		return nil, err
	}
	return map[string]any{"file": f}, nil
}

func (a *App) toolSetVisibility(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	vis := visibilityOrDefault(strArg(args, "visibility"))
	f, err := dbUpdate(ctx.AppDB(), pid, id, map[string]any{"visibility": vis})
	if err != nil {
		return nil, err
	}
	return map[string]any{"file": f}, nil
}

func (a *App) toolDedupe(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	hash := strings.ToLower(strings.TrimSpace(strArg(args, "sha256")))
	if hash == "" {
		return nil, errors.New("sha256 required")
	}
	f, err := dbFindBySHA(ctx.AppDB(), pid, hash)
	if err != nil {
		return nil, err
	}
	if f == nil {
		return map[string]any{"found": false}, nil
	}
	return map[string]any{"found": true, "file": f}, nil
}

func (a *App) toolDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	prior, _ := dbGetByID(ctx.AppDB(), pid, id)
	if err := dbSoftDelete(ctx.AppDB(), pid, id); err != nil {
		return nil, err
	}
	emitFileEvent(ctx, "file.deleted", prior, false)
	return map[string]any{"deleted": true}, nil
}

func (a *App) toolFromURL(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	url := strArg(args, "url")
	if url == "" {
		return nil, errors.New("url required")
	}
	// Many CDNs (vecteezy, cloudfront-fronted hosts, anything behind
	// Cloudflare's bot scoring) reject requests with Go's default
	// User-Agent of "Go-http-client/1.1". Spoof a recent Chrome on
	// macOS — same approach we use elsewhere when fetching public
	// content. Accept matches what a browser would send.
	req, err := http.NewRequest("GET", url, nil)
	if err != nil {
		return nil, fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("User-Agent", "Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0.0.0 Safari/537.36")
	req.Header.Set("Accept", "*/*")
	client := &http.Client{Timeout: 30 * time.Second}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch %s: %w", url, err)
	}
	defer resp.Body.Close()
	if resp.StatusCode >= 400 {
		return nil, fmt.Errorf("fetch %s: status %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, maxUploadBytes(ctx)))
	if err != nil {
		return nil, err
	}
	name := strArg(args, "name")
	if name == "" {
		name = filepath.Base(url)
	}
	in := uploadInput{
		Name:        normaliseFilename(name),
		Folder:      normaliseFolder(strArg(args, "folder")),
		ContentType: resp.Header.Get("Content-Type"),
		Tags:        strArrayArg(args, "tags"),
		Source:      "imported",
		Visibility:  "private",
	}
	f, existed, err := saveBytes(ctx, pid, in, body)
	if err != nil {
		return nil, err
	}
	emitFileEvent(ctx, "file.added", f, existed)
	return map[string]any{
		"id":           f.ID,
		"url":          buildContentURL(f),
		"sha256":       f.SHA256,
		"was_existing": existed,
	}, nil
}

// ─── HTTP handlers ─────────────────────────────────────────────────

func (a *App) handleFilesCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.httpListOrSearch(w, r)
	case http.MethodPost:
		a.httpUpload(w, r)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleFilesItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/files/")
	parts := strings.SplitN(rest, "/", 2)
	id, _ := strconv.ParseInt(parts[0], 10, 64)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	tail := ""
	if len(parts) == 2 {
		tail = parts[1]
	}
	switch tail {
	case "content":
		a.httpServeContent(w, r, id)
	case "":
		switch r.Method {
		case http.MethodGet:
			a.httpGetMetadata(w, r, id)
		case http.MethodPatch:
			a.httpPatch(w, r, id)
		case http.MethodDelete:
			a.httpDelete(w, r, id)
		default:
			httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	default:
		httpErr(w, http.StatusNotFound, "not found")
	}
}

func (a *App) handleFolders(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	ctx := globalCtx
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	parent := normaliseFolder(r.URL.Query().Get("parent"))
	folders, err := dbListChildFolders(ctx.AppDB(), pid, parent)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"folders": folders, "parent": parent})
}

func (a *App) httpListOrSearch(w http.ResponseWriter, r *http.Request) {
	ctx := globalCtx
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	q := r.URL.Query()
	folderRaw := q.Get("folder")
	if folderRaw != "" {
		folder := normaliseFolder(folderRaw)
		recursive := q.Get("recursive") == "true" || q.Get("recursive") == "1"
		out, err := dbListFolder(ctx.AppDB(), pid, folder, recursive, 200)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpJSON(w, map[string]any{"files": out, "folder": folder, "recursive": recursive})
		return
	}
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	out, err := dbSearch(ctx.AppDB(), pid, searchOpts{
		Q: q.Get("q"), Folder: q.Get("folder"),
		ContentType: q.Get("content_type"), SHA256: q.Get("sha256"),
		Tag: q.Get("tag"), Source: q.Get("source"),
		Limit: limit,
	})
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"files": out})
}

func (a *App) httpUpload(w http.ResponseWriter, r *http.Request) {
	ctx := globalCtx
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := r.ParseMultipartForm(maxUploadBytes(ctx)); err != nil {
		// Fallback: JSON body with content_base64.
		var body map[string]any
		if jerr := json.NewDecoder(r.Body).Decode(&body); jerr == nil {
			out, err := a.toolUpload(ctx, body)
			if err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
			httpJSON(w, out)
			return
		}
		httpErr(w, http.StatusBadRequest, "expected multipart/form-data or JSON body")
		return
	}
	file, header, err := r.FormFile("file")
	if err != nil {
		httpErr(w, http.StatusBadRequest, "field 'file' required")
		return
	}
	defer file.Close()
	body, err := io.ReadAll(io.LimitReader(file, maxUploadBytes(ctx)))
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	in := uploadInput{
		Name:        normaliseFilename(header.Filename),
		Folder:      normaliseFolder(r.FormValue("folder")),
		ContentType: header.Header.Get("Content-Type"),
		Source:      "human",
		Visibility:  visibilityOrDefault(r.FormValue("visibility")),
	}
	if t := r.FormValue("tags"); t != "" {
		var tags []string
		_ = json.Unmarshal([]byte(t), &tags)
		in.Tags = tags
	}
	f, existed, err := saveBytes(ctx, pid, in, body)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	emitFileEvent(ctx, "file.added", f, existed)
	httpJSON(w, map[string]any{
		"id": f.ID, "url": buildContentURL(f), "sha256": f.SHA256,
		"size_bytes": f.SizeBytes, "name": f.Name, "folder": f.Folder,
		"was_existing": existed,
	})
}

func (a *App) httpGetMetadata(w http.ResponseWriter, r *http.Request, id int64) {
	ctx := globalCtx
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	f, err := dbGetByID(ctx.AppDB(), pid, id)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if f == nil {
		httpErr(w, http.StatusNotFound, "not found")
		return
	}
	httpJSON(w, map[string]any{"file": f})
}

func (a *App) httpServeContent(w http.ResponseWriter, r *http.Request, id int64) {
	ctx := globalCtx
	pid, err := resolveProjectFromRequest(r)
	// For signed/public access we don't require a project_id query
	// param — visibility + signature are the only auth that matters.
	// We still need the project to look up the file though, so we
	// fall back to scanning every project when pid is missing. v0.2:
	// embed pid in storage_key or signed URL.
	if err != nil {
		pid = ""
	}
	var f *File
	if pid != "" {
		f, err = dbGetByID(ctx.AppDB(), pid, id)
	} else {
		f, err = dbGetByIDAnyProject(ctx.AppDB(), id)
	}
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if f == nil {
		httpErr(w, http.StatusNotFound, "not found")
		return
	}

	// Authorise based on visibility + signature.
	q := r.URL.Query()
	sig := q.Get("sig")
	exp, _ := strconv.ParseInt(q.Get("exp"), 10, 64)
	switch f.Visibility {
	case "public":
		// Anyone can fetch.
	case "signed":
		if !verifySignature(f.ID, exp, sig) {
			httpErr(w, http.StatusForbidden, "invalid or expired signature")
			return
		}
	case "private":
		// Either a valid signature OR rely on the platform's auth
		// proxy to have stripped the request's bearer. v0.1: if a sig
		// is present we accept it; otherwise we trust the proxy
		// (sidecar's withTokenAuth empty token = pass-through).
		if sig != "" && !verifySignature(f.ID, exp, sig) {
			httpErr(w, http.StatusForbidden, "invalid or expired signature")
			return
		}
	}

	// ETag + cache headers.
	etag := `"` + f.SHA256 + `"`
	w.Header().Set("ETag", etag)
	switch f.Visibility {
	case "public":
		w.Header().Set("Cache-Control", "public, max-age=31536000, immutable")
	case "signed":
		remaining := exp - time.Now().Unix()
		if remaining < 0 {
			remaining = 0
		}
		w.Header().Set("Cache-Control", fmt.Sprintf("private, max-age=%d", remaining))
	default:
		w.Header().Set("Cache-Control", "private, no-store")
	}
	if im := r.Header.Get("If-None-Match"); im == etag {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	if ct := f.ContentType; ct != "" {
		w.Header().Set("Content-Type", ct)
	}

	// http.ServeFile gives us Range + Last-Modified + 200 OK for free.
	path := blobPath(ctx, f.SHA256, f.StorageKey)
	http.ServeFile(w, r, path)
}

func (a *App) httpPatch(w http.ResponseWriter, r *http.Request, id int64) {
	ctx := globalCtx
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	updates := map[string]any{}
	if v, ok := body["folder"].(string); ok {
		updates["folder"] = normaliseFolder(v)
	}
	if v, ok := body["name"].(string); ok {
		updates["name"] = normaliseFilename(v)
	}
	if v, ok := body["visibility"].(string); ok {
		updates["visibility"] = visibilityOrDefault(v)
	}
	if v, ok := body["tags"].([]any); ok {
		strs := []string{}
		for _, it := range v {
			if s, ok := it.(string); ok {
				strs = append(strs, s)
			}
		}
		raw, _ := json.Marshal(strs)
		updates["tags"] = string(raw)
	}
	f, err := dbUpdate(ctx.AppDB(), pid, id, updates)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"file": f})
}

func (a *App) httpDelete(w http.ResponseWriter, r *http.Request, id int64) {
	ctx := globalCtx
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	// Snapshot the row before deleting so we can broadcast the
	// folder/name to subscribers without a second lookup.
	prior, _ := dbGetByID(ctx.AppDB(), pid, id)
	if err := dbSoftDelete(ctx.AppDB(), pid, id); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	emitFileEvent(ctx, "file.deleted", prior, false)
	httpJSON(w, map[string]any{"deleted": true})
}

// emitFileEvent broadcasts a single file change onto the platform's
// app-event bus. Best-effort: ctx.Emit is fire-and-forget, so a
// flapping platform can't slow down the upload/delete handler.
// Pass `existed=true` for dedup-resolved uploads so subscribers can
// skip a duplicate row in the UI when the same content was already
// present (the file is reused, not re-added).
func emitFileEvent(ctx *sdk.AppCtx, topic string, f *File, existed bool) {
	if ctx == nil || f == nil {
		return
	}
	ctx.Emit(topic, map[string]any{
		"id":           f.ID,
		"name":         f.Name,
		"folder":       f.Folder,
		"size_bytes":   f.SizeBytes,
		"content_type": f.ContentType,
		"sha256":       f.SHA256,
		"visibility":   f.Visibility,
		"was_existing": existed,
	})
}

// ─── Disk + DB helpers ─────────────────────────────────────────────

type uploadInput struct {
	Name        string
	Folder      string
	ContentType string
	Tags        []string
	Source      string
	Visibility  string
}

// saveBytes writes the bytes to disk, computes sha256, dedupes if a
// row with the same hash already exists for this project, and
// returns the resulting file row + a boolean indicating whether the
// content was already present.
func saveBytes(ctx *sdk.AppCtx, pid string, in uploadInput, body []byte) (*File, bool, error) {
	hash := sha256.Sum256(body)
	hex := hex.EncodeToString(hash[:])

	// Dedup at the row level: same project + sha256 + name + folder
	// returns the existing row. Same content under a different name
	// is allowed (different rows, but same disk blob). v0.1 still
	// writes a fresh blob for non-exact matches; v0.2 reference-
	// counts.
	if existing, err := dbFindExact(ctx.AppDB(), pid, hex, in.Folder, in.Name); err == nil && existing != nil {
		return existing, true, nil
	}

	key := uuid.NewString() + extOf(in.Name, in.ContentType)
	dir := filepath.Join(blobsDir(ctx), hex[:2])
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, false, err
	}
	if err := os.WriteFile(filepath.Join(dir, key), body, 0644); err != nil {
		return nil, false, err
	}
	tagsJSON, _ := json.Marshal(in.Tags)
	if in.Visibility == "" {
		in.Visibility = "private"
	}
	res, err := ctx.AppDB().Exec(
		`INSERT INTO files
			(project_id, name, folder, storage_key, content_type, size_bytes,
			 sha256, uploaded_by, source, tags, visibility)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, in.Name, in.Folder, key, in.ContentType, len(body),
		hex, callerLabel(), in.Source, string(tagsJSON), in.Visibility)
	if err != nil {
		return nil, false, err
	}
	id, _ := res.LastInsertId()
	f, err := dbGetByID(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, false, err
	}
	return f, false, nil
}

// dbFindExact returns the file whose (project, sha, folder, name)
// matches — used for upload dedup at the row level.
//
// Uses QueryRow rather than Query so the connection is released
// before the dbGetByID call below grabs one. With MaxOpenConns(1)
// in tests, holding a Rows cursor open while issuing a nested
// QueryRow deadlocks.
func dbFindExact(db *sql.DB, pid, hash, folder, name string) (*File, error) {
	var id int64
	err := db.QueryRow(
		`SELECT id FROM files WHERE project_id = ? AND sha256 = ? AND folder = ? AND name = ?
		 AND deleted_at IS NULL LIMIT 1`,
		pid, hash, folder, name).Scan(&id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return dbGetByID(db, pid, id)
}

func dbFindBySHA(db *sql.DB, pid, hash string) (*File, error) {
	var id int64
	err := db.QueryRow(
		`SELECT id FROM files WHERE project_id = ? AND sha256 = ? AND deleted_at IS NULL
		 ORDER BY id DESC LIMIT 1`, pid, hash).Scan(&id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return dbGetByID(db, pid, id)
}

func dbGetByID(db *sql.DB, pid string, id int64) (*File, error) {
	row := db.QueryRow(
		`SELECT id, project_id, name, folder, storage_key, COALESCE(content_type,''),
			COALESCE(size_bytes,0), COALESCE(sha256,''), COALESCE(uploaded_by,''),
			COALESCE(source,''), COALESCE(tags,'[]'), visibility,
			COALESCE(created_at,''), COALESCE(updated_at,'')
		 FROM files WHERE id = ? AND project_id = ? AND deleted_at IS NULL`,
		id, pid)
	return scanFile(row)
}

func dbGetByIDAnyProject(db *sql.DB, id int64) (*File, error) {
	row := db.QueryRow(
		`SELECT id, project_id, name, folder, storage_key, COALESCE(content_type,''),
			COALESCE(size_bytes,0), COALESCE(sha256,''), COALESCE(uploaded_by,''),
			COALESCE(source,''), COALESCE(tags,'[]'), visibility,
			COALESCE(created_at,''), COALESCE(updated_at,'')
		 FROM files WHERE id = ? AND deleted_at IS NULL`, id)
	return scanFile(row)
}

func scanFile(row *sql.Row) (*File, error) {
	f := &File{}
	var tagsRaw string
	err := row.Scan(&f.ID, &f.ProjectID, &f.Name, &f.Folder, &f.StorageKey,
		&f.ContentType, &f.SizeBytes, &f.SHA256, &f.UploadedBy, &f.Source,
		&tagsRaw, &f.Visibility, &f.CreatedAt, &f.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(tagsRaw), &f.Tags)
	if f.Tags == nil {
		f.Tags = []string{}
	}
	f.URL = buildContentURL(f)
	return f, nil
}

type searchOpts struct {
	Q, Folder, ContentType, SHA256, Tag, Source string
	Limit                                       int
}

func dbSearch(db *sql.DB, pid string, opts searchOpts) ([]*File, error) {
	where := []string{"project_id = ?", "deleted_at IS NULL"}
	args := []any{pid}
	if opts.Q != "" {
		where = append(where, "name LIKE ?")
		args = append(args, "%"+strings.ToLower(opts.Q)+"%")
	}
	if opts.Folder != "" {
		where = append(where, "folder = ?")
		args = append(args, normaliseFolder(opts.Folder))
	}
	if opts.ContentType != "" {
		where = append(where, "content_type LIKE ?")
		args = append(args, opts.ContentType+"%")
	}
	if opts.SHA256 != "" {
		where = append(where, "sha256 = ?")
		args = append(args, strings.ToLower(opts.SHA256))
	}
	if opts.Tag != "" {
		// JSON array contains the literal "<tag>" — cheap LIKE for v0.1.
		where = append(where, `tags LIKE ?`)
		args = append(args, `%"`+opts.Tag+`"%`)
	}
	if opts.Source != "" {
		where = append(where, "source = ?")
		args = append(args, opts.Source)
	}
	limit := opts.Limit
	if limit <= 0 {
		limit = 50
	}
	q := `SELECT id FROM files WHERE ` + strings.Join(where, " AND ") +
		` ORDER BY updated_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	out := []*File{}
	for _, id := range ids {
		f, err := dbGetByID(db, pid, id)
		if err == nil && f != nil {
			out = append(out, f)
		}
	}
	return out, nil
}

func dbListFolder(db *sql.DB, pid, folder string, recursive bool, limit int) ([]*File, error) {
	folder = normaliseFolder(folder)
	var rows *sql.Rows
	var err error
	if recursive {
		rows, err = db.Query(
			`SELECT id FROM files WHERE project_id = ? AND folder LIKE ?
			 AND deleted_at IS NULL ORDER BY folder, name LIMIT ?`,
			pid, folder+"%", limit)
	} else {
		rows, err = db.Query(
			`SELECT id FROM files WHERE project_id = ? AND folder = ?
			 AND deleted_at IS NULL ORDER BY name LIMIT ?`,
			pid, folder, limit)
	}
	if err != nil {
		return nil, err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	out := []*File{}
	for _, id := range ids {
		f, err := dbGetByID(db, pid, id)
		if err == nil && f != nil {
			out = append(out, f)
		}
	}
	return out, nil
}

// dbListChildFolders returns the immediate child folder names ONE
// level under `parent`. parent="/reports/" + a row at folder=
// "/reports/2026/q1/" returns "2026" (one entry, deduped).
func dbListChildFolders(db *sql.DB, pid, parent string) ([]string, error) {
	parent = normaliseFolder(parent)
	rows, err := db.Query(
		`SELECT DISTINCT folder FROM files
		 WHERE project_id = ? AND folder LIKE ? AND folder != ? AND deleted_at IS NULL
		 ORDER BY folder`,
		pid, parent+"%", parent)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	out := []string{}
	for rows.Next() {
		var folder string
		if err := rows.Scan(&folder); err != nil {
			continue
		}
		// Take the first path segment after parent.
		rel := strings.TrimPrefix(folder, parent)
		if rel == "" {
			continue
		}
		seg := rel
		if i := strings.IndexByte(rel, '/'); i >= 0 {
			seg = rel[:i]
		}
		if seg == "" || seen[seg] {
			continue
		}
		seen[seg] = true
		out = append(out, seg)
	}
	return out, nil
}

func dbUpdate(db *sql.DB, pid string, id int64, updates map[string]any) (*File, error) {
	allowed := map[string]bool{
		"name": true, "folder": true, "tags": true,
		"visibility": true, "metadata": true,
	}
	sets := []string{}
	args := []any{}
	for k, v := range updates {
		if !allowed[k] {
			continue
		}
		sets = append(sets, k+" = ?")
		args = append(args, v)
	}
	if len(sets) == 0 {
		return dbGetByID(db, pid, id)
	}
	sets = append(sets, "updated_at = CURRENT_TIMESTAMP")
	args = append(args, id, pid)
	_, err := db.Exec(
		`UPDATE files SET `+strings.Join(sets, ", ")+` WHERE id = ? AND project_id = ?`,
		args...)
	if err != nil {
		return nil, err
	}
	return dbGetByID(db, pid, id)
}

func dbSoftDelete(db *sql.DB, pid string, id int64) error {
	_, err := db.Exec(
		`UPDATE files SET deleted_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND project_id = ?`,
		id, pid)
	return err
}

// ─── Signed URL ────────────────────────────────────────────────────

func signFile(id int64, expires int64) string {
	mac := hmac.New(sha256.New, signingKey())
	fmt.Fprintf(mac, "%d:%d", id, expires)
	return hex.EncodeToString(mac.Sum(nil))
}

func verifySignature(id int64, expires int64, sig string) bool {
	if sig == "" || expires == 0 {
		return false
	}
	if time.Now().Unix() > expires {
		return false
	}
	want := signFile(id, expires)
	return hmac.Equal([]byte(want), []byte(sig))
}

// signingKey is derived from the platform's APTEVA_APP_TOKEN. Same
// value across this sidecar's lifetime; rotates when the platform
// re-spawns. v0.2 may move to a per-install key persisted in app DB.
func signingKey() []byte {
	tok := os.Getenv("APTEVA_APP_TOKEN")
	if tok == "" {
		tok = "storage-app-default-secret"
	}
	h := sha256.Sum256([]byte("apteva-storage-sign:" + tok))
	return h[:]
}

// ─── Misc helpers ──────────────────────────────────────────────────

var globalCtx *sdk.AppCtx

func blobsDir(ctx *sdk.AppCtx) string {
	// Override via env for tests; default sits next to the DB.
	if v := os.Getenv("STORAGE_BLOBS_DIR"); v != "" {
		return v
	}
	if ctx != nil && ctx.Manifest() != nil && ctx.Manifest().DB != nil {
		base := filepath.Dir(ctx.Manifest().DB.Path)
		if dbp := os.Getenv("DB_PATH"); dbp != "" {
			base = filepath.Dir(dbp)
		}
		return filepath.Join(base, "storage-blobs")
	}
	return "/data/storage-blobs"
}

func blobPath(ctx *sdk.AppCtx, sha256 string, storageKey string) string {
	prefix := "00"
	if len(sha256) >= 2 {
		prefix = sha256[:2]
	}
	return filepath.Join(blobsDir(ctx), prefix, storageKey)
}

func buildContentURL(f *File) string {
	return fmt.Sprintf("/files/%d/content", f.ID)
}

func extOf(name, contentType string) string {
	if i := strings.LastIndexByte(name, '.'); i >= 0 {
		return name[i:]
	}
	switch contentType {
	case "image/png":
		return ".png"
	case "image/jpeg":
		return ".jpg"
	case "application/pdf":
		return ".pdf"
	case "text/plain":
		return ".txt"
	case "application/json":
		return ".json"
	}
	return ".bin"
}

func visibilityOrDefault(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "public":
		return "public"
	case "signed":
		return "signed"
	}
	return "private"
}

func defaultSignedTTL(ctx *sdk.AppCtx) int {
	v := ctx.Config().Get("signed_url_default_ttl_seconds")
	if v == "" {
		return 86400
	}
	n, _ := strconv.Atoi(v)
	if n <= 0 {
		return 86400
	}
	return n
}

func maxUploadBytes(ctx *sdk.AppCtx) int64 {
	v := ctx.Config().Get("max_upload_size_mb")
	if v == "" {
		return 100 * 1024 * 1024
	}
	mb, _ := strconv.Atoi(v)
	if mb <= 0 {
		mb = 100
	}
	return int64(mb) * 1024 * 1024
}

func callerLabel() string {
	// The platform ought to inject the calling instance — for v0.1 we
	// just use "agent" as a default; humans get tagged by the panel
	// upload form via source="human".
	return "agent"
}

// ─── tiny utilities ────────────────────────────────────────────────

func intArg(args map[string]any, key string, def int) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err == nil {
			return n
		}
	}
	return def
}

func int64Arg(args map[string]any, key string) int64 {
	switch v := args[key].(type) {
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err == nil {
			return n
		}
	}
	return 0
}

func strArg(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

func strArrayArg(args map[string]any, key string) []string {
	out := []string{}
	if arr, ok := args[key].([]any); ok {
		for _, v := range arr {
			if s, ok := v.(string); ok {
				out = append(out, s)
			}
		}
	}
	return out
}

func schemaObject(props map[string]any, required []string) map[string]any {
	out := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func httpJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}
