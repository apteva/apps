// Storage v0.6 — file storage with pluggable backend (disk or
// S3-compatible), virtual folders, signed URLs, dedup.
//
// Blob layout (key, identical on both backends):
//
//   <sha256[:2]>/<storage_key>
//
// Disk: relative to blobsDir (filesystem path). S3: bucket-relative
// object key. The two-byte hex prefix exists for the disk's benefit
// (avoids 1M files in one directory) and is harmless on S3.
//
// Each upload writes a fresh storage_key; sha256 is recorded for ETag
// and dedup checks but bytes are not yet shared between rows. v0.7
// reference-counts and reclaims.
package main

import (
	"bytes"
	"context"
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
	"net/url"
	"os"
	"path/filepath"
	"sort"
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
version: 0.9.6
description: |
  File storage with virtual folders, signed URLs, dedup. Pluggable
  backend: local disk by default, S3-compatible (AWS / R2 / B2 /
  Wasabi / MinIO) when an integration is bound. Direct presigned
  uploads + downloads on S3 — bytes never touch the storage container.
author: Apteva
scopes: [project, global]
requires:
  permissions:
    - db.write.app
    - platform.connections.read_credentials
  integrations:
    - role: backend
      kind: integration
      compatible_slugs: [aws-s3, cloudflare-r2, backblaze-b2, hetzner-object-storage]
      required: false
      label: "S3-compatible backend (optional)"
      hint: "Bind to host blobs in S3/R2/B2/Hetzner; otherwise blobs live on local disk."
provides:
  http_routes:
    - prefix: /
  resources:
    - name: folder
      label: "Folder"
      list_endpoint: /folders
      matcher: glob
      picker: tree
      listing_visibility: navigable
  permissions:
    - { name: files.read,   resource: folder, description: "List + download files" }
    - { name: files.write,  resource: folder, description: "Upload, move, rename, set tags + visibility" }
    - { name: files.delete, resource: folder, description: "Hard-delete or soft-delete files" }
  mcp_tools:
    - { name: files_upload,         description: "Upload bytes (base64). Returns id, url, sha256.", requires: files.write,  resource_from: "folder/{arg.folder?}" }
    - { name: files_get,            description: "Metadata for one file." }
    - { name: files_get_url,        description: "Mint a signed time-limited URL." }
    - { name: files_get_content,    description: "Fetch bytes inline as base64 (≤25 MB). For cross-sidecar reads where signed-URL routing is fragile across multiple storage installs." }
    - { name: files_search,         description: "Filtered file list." }
    - { name: files_list,           description: "List files in one folder." }
    - { name: files_list_folders,   description: "List immediate child folders." }
    - { name: files_create_folder,  description: "Create an empty folder via a 0-byte .placeholder upload. Idempotent.", requires: files.write, resource_from: "folder/{arg.path?}" }
    - { name: files_move,           description: "Move + optionally rename a file.", requires: files.write,  resource_from: "folder/{arg.folder?}" }
    - { name: files_set_tags,       description: "Append/remove tags." }
    - { name: files_set_visibility, description: "private | signed | public." }
    - { name: files_dedupe_check,   description: "Find an existing file by sha256." }
    - { name: files_delete,         description: "Delete a file (hard or soft)." }
    - { name: files_from_url,       description: "Fetch a URL into storage.", requires: files.write, resource_from: "folder/{arg.folder?}" }
    - { name: storage_abort_upload, description: "Abort a leaked multipart upload session and reclaim its bytes." }
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

	// Resolve the configured backend (disk by default, s3 when wired
	// via the install's config_schema). Boot fails loud here when s3
	// creds are missing — better than silently routing writes to the
	// wrong place.
	bound := ctx.IntegrationFor("backend")
	id, whoamiErr := ctx.PlatformAPI().WhoAmI()
	if id != nil {
		ctx.Logger().Info("storage backend role probe",
			"bindings", id.Bindings,
			"resolved_bound", bound)
	} else {
		ctx.Logger().Warn("storage backend role probe — WhoAmI returned nil identity",
			"err", fmt.Sprintf("%v", whoamiErr),
			"gateway", os.Getenv("APTEVA_GATEWAY_URL"),
			"token", os.Getenv("APTEVA_APP_TOKEN"))
	}
	be, err := initBackend(ctx)
	if err != nil {
		return fmt.Errorf("init backend: %w", err)
	}
	globalBackend = be

	// uploads scratch + (for disk) blobs root always live on the
	// local FS — uploads.go stitches parts there before handing the
	// final blob to the backend. On s3 installs the blobs dir is
	// effectively empty (only used as a transient stitch target);
	// keeping the mkdir keeps boot simple.
	if err := os.MkdirAll(blobsDir(ctx), 0755); err != nil {
		return fmt.Errorf("mkdir blobs: %w", err)
	}
	if err := os.MkdirAll(uploadsDir(ctx), 0755); err != nil {
		return fmt.Errorf("mkdir uploads: %w", err)
	}
	ctx.Logger().Info("storage mounted",
		"scope_project_id", os.Getenv("APTEVA_PROJECT_ID"),
		"backend", be.Kind(),
		"blobs_dir", blobsDir(ctx),
		"uploads_dir", uploadsDir(ctx))

	// Background sweeper for stale upload sessions. Runs once on
	// boot (so a restart immediately reclaims anything older than
	// the TTL) and then on the configured interval. Goroutine has
	// no shutdown — apps don't have a clean teardown signal in
	// v0.1, but the sweeper does nothing destructive past the TTL
	// gate so an abrupt exit is fine.
	go func() {
		sweepStaleUploads(ctx)
		sweepStalePendingUploads(ctx)
		interval := configuredSweepInterval(ctx)
		ctx.Logger().Info("upload sweeper started",
			"interval", interval.String(),
			"ttl", configuredUploadIdleTTL(ctx).String(),
		)
		t := time.NewTicker(interval)
		defer t.Stop()
		for range t.C {
			sweepStaleUploads(ctx)
			sweepStalePendingUploads(ctx)
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
			HandlerCtx:  a.toolGetCtx,
		},
		{
			Name:        "files_get_url",
			Description: "Mint a signed time-limited URL. Args: id, ttl_seconds?. Returns {url, expires_at}.",
			InputSchema: schemaObject(map[string]any{
				"id":          map[string]any{"type": "integer"},
				"ttl_seconds": map[string]any{"type": "integer"},
			}, []string{"id"}),
			HandlerCtx: a.toolGetURLCtx,
		},
		{
			Name:        "files_get_content",
			Description: "Fetch the file's bytes inline as base64 (≤25 MB). Use this for cross-sidecar reads where the platform's HTTP-routed signed URL doesn't resolve to the right storage instance — this call goes through the binding (CallAppResult) so it always hits the storage install bound to the caller. For files >25 MB or for handing URLs to third parties (Deepgram, browsers, humans) use files_get_url instead. Args: id. Returns {id, name, content_type, size_bytes, content_base64}.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			HandlerCtx:  a.toolGetContentCtx,
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
			HandlerCtx: a.toolSearchCtx,
		},
		{
			Name:        "files_list",
			Description: "List files in one folder. Args: folder (default '/'), recursive?, limit?.",
			InputSchema: schemaObject(map[string]any{
				"folder":    map[string]any{"type": "string"},
				"recursive": map[string]any{"type": "boolean"},
				"limit":     map[string]any{"type": "integer"},
			}, nil),
			HandlerCtx: a.toolListCtx,
		},
		{
			Name:        "files_list_folders",
			Description: "List immediate child folders one level under a parent (default '/'). Args: parent?.",
			InputSchema: schemaObject(map[string]any{
				"parent": map[string]any{"type": "string"},
			}, nil),
			HandlerCtx: a.toolListFoldersCtx,
		},
		{
			Name:        "files_create_folder",
			Description: "Create an empty folder by uploading a 0-byte .placeholder file at the given path. Idempotent — silently no-ops if the path already exists. Args: path (e.g. '/raw-footage/2026-05/').",
			InputSchema: schemaObject(map[string]any{
				"path": map[string]any{"type": "string"},
			}, []string{"path"}),
			HandlerCtx: a.toolCreateFolderCtx,
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
			HandlerCtx: a.toolSetTagsCtx,
		},
		{
			Name:        "files_set_visibility",
			Description: "Change visibility. Args: id, visibility (private | signed | public).",
			InputSchema: schemaObject(map[string]any{
				"id":         map[string]any{"type": "integer"},
				"visibility": map[string]any{"type": "string"},
			}, []string{"id", "visibility"}),
			HandlerCtx: a.toolSetVisibilityCtx,
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
			Description: "Delete a file. Default is hard delete: row removed from the database AND bytes removed from disk. Pass keep_record=true to soft-delete instead — sets deleted_at on the row, leaves bytes on disk (audit-history use cases). Either path emits file.deleted with the pre-delete metadata so subscribers see what went. Args: id, keep_record? (default false).",
			InputSchema: schemaObject(map[string]any{
				"id":          map[string]any{"type": "integer"},
				"keep_record": map[string]any{"type": "boolean"},
			}, []string{"id"}),
			HandlerCtx: a.toolDeleteCtx,
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
		{
			Name:        "storage_abort_upload",
			Description: "Abort an in-progress multipart upload session. Removes the partial bytes on disk and emits upload.aborted. Idempotent: aborting an already-removed session returns found=false. Args: id (the upload session id from /uploads init), reason? (free-text label for the audit log). Use this when a client cancels a large upload — without it, partial bytes sit on disk until the sweeper TTL fires.",
			InputSchema: schemaObject(map[string]any{
				"id":     map[string]any{"type": "string"},
				"reason": map[string]any{"type": "string"},
			}, []string{"id"}),
			HandlerCtx: a.toolAbortUploadCtx,
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
	// URL — the file's canonical absolute URL. Reachable per the
	// file's visibility:
	//
	//   public  → anyone can fetch (no auth, no signature)
	//   signed  → requires the ?sig=&exp= query the URL was minted
	//             with; agents call files_get_url to get one
	//   private → requires an authenticated request (dashboard
	//             session, API key, or app-install bearer)
	//
	// Same path shape across all three — like S3, where a public
	// bucket's object URL and a presigned URL share the same prefix
	// and only differ in query params. Agents share `url` with
	// external recipients when visibility=public; for signed sharing
	// they call files_get_url and share the returned (signed) form.
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

// toolCreateFolderCtx materialises an empty folder by uploading a
// 0-byte .placeholder file at the requested path. S3-style folders
// only exist when something lives in them; the placeholder is the
// universal trick for "make this folder visible to listings without
// putting real content in it yet".
//
// Idempotent: if the folder already has any file (placeholder or
// otherwise), returns {created: false, path}. The dashboard panel
// has been doing this inline since v0.1; this just exposes the same
// semantics as a proper MCP tool so agents and other apps can call
// it without learning storage's HTTP surface.
func (a *App) toolCreateFolderCtx(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	path := normaliseFolder(strArg(args, "path"))
	if path == "" || path == "/" {
		return nil, errors.New("path required (e.g. '/raw/2026-05/')")
	}
	// Idempotency: if any file already lives in this folder, do
	// nothing and report created=false. Cheap sub-second query —
	// the project_id+folder index is the same one /folders walks.
	if hasFiles, err := dbFolderHasFiles(ctx.AppDB(), pid, path); err == nil && hasFiles {
		return map[string]any{"created": false, "path": path}, nil
	}
	in := uploadInput{
		Name:        ".placeholder",
		Folder:      path,
		ContentType: "application/x-empty",
		Source:      "system",
		Visibility:  effectiveVisibility(ctx, ""),
	}
	f, existed, err := saveBytes(ctx, pid, in, []byte{})
	if err != nil {
		return nil, err
	}
	emitFileEvent(ctx, "file.added", f, existed)
	return map[string]any{"created": true, "path": path}, nil
}

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
		Visibility:  effectiveVisibility(ctx, strArg(args, "visibility")),
	}
	f, existed, err := saveBytes(ctx, pid, in, body)
	if err != nil {
		return nil, err
	}
	emitFileEvent(ctx, "file.added", f, existed)
	return map[string]any{
		"id":           f.ID,
		"url":          absoluteContentURL(ctx, f),
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

	// On s3-backed installs, mint a presigned S3 URL directly so
	// downstream consumers (Deepgram, Cloudinary, Mux, browser
	// players) fetch bytes from the bucket without bouncing through
	// our gateway. Disk falls back to the HMAC path-style URL the
	// gateway resolves locally — same as pre-v0.6.
	if be := backend(); be.Kind() == "s3" {
		key := objectKey(f.SHA256, f.StorageKey)
		url, err := be.PresignGet(context.Background(), key, f.Name, f.ContentType,
			time.Duration(ttl)*time.Second)
		if err != nil {
			return nil, fmt.Errorf("presign: %w", err)
		}
		return map[string]any{
			"url":        url,
			"expires_at": exp,
			"file_id":    f.ID,
			"presigned":  true,
		}, nil
	}

	sig := signFile(f.ID, exp)
	// Absolute URL so callers can hand it to third-party services
	// (Deepgram, Cloudinary, plain links shared with humans) without
	// having to know the platform's host. Falls back to relative
	// when public_url isn't configured (dev, no-network installs).
	url := signedAbsoluteURL(ctx, f, sig, exp)
	return map[string]any{"url": url, "expires_at": exp, "file_id": f.ID}, nil
}

// toolGetContent reads bytes inline and returns them as base64. Built
// for the cross-sidecar case where the platform routes app HTTP paths
// to one storage install but the bound storage is a different one —
// e.g. a project has multiple storage installs and the bills sidecar
// uploaded via the bound storage but its signed-URL fetch lands at
// the wrong instance and 404s. CallAppResult always routes via
// binding, so this tool is the route-correct way to get bytes back.
//
// 25 MB cap matches the multipart upload limit. Larger files should
// use files_get_url + http.GET (the routing fragility is an operator
// problem at that scale; the right fix is to consolidate to one
// storage install per project).
func (a *App) toolGetContent(ctx *sdk.AppCtx, args map[string]any) (any, error) {
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
		return nil, fmt.Errorf("file %d not found", id)
	}

	const maxBytes int64 = 25 << 20
	if f.SizeBytes > maxBytes {
		return nil, fmt.Errorf(
			"file %d is %d bytes, exceeds %d-byte limit for files_get_content; use files_get_url for large files",
			id, f.SizeBytes, maxBytes)
	}

	key := objectKey(f.SHA256, f.StorageKey)
	var data []byte
	if path, ok := backend().LocalPath(key); ok {
		data, err = os.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read disk: %w", err)
		}
	} else {
		// S3 / remote backend — presign briefly + http.GET.
		signedURL, err := backend().PresignGet(context.Background(), key, f.Name, f.ContentType, 5*time.Minute)
		if err != nil {
			return nil, fmt.Errorf("presign: %w", err)
		}
		req, err := http.NewRequest(http.MethodGet, signedURL, nil)
		if err != nil {
			return nil, err
		}
		client := &http.Client{Timeout: 60 * time.Second}
		resp, err := client.Do(req)
		if err != nil {
			return nil, fmt.Errorf("fetch presigned: %w", err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("presigned URL: status %d", resp.StatusCode)
		}
		data, err = io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))
		if err != nil {
			return nil, err
		}
	}
	if int64(len(data)) > maxBytes {
		return nil, fmt.Errorf("file %d exceeded %d bytes during read", id, maxBytes)
	}

	return map[string]any{
		"id":             f.ID,
		"name":           f.Name,
		"content_type":   f.ContentType,
		"size_bytes":     int64(len(data)),
		"content_base64": base64.StdEncoding.EncodeToString(data),
	}, nil
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
	// Fan out file.updated so subscribers (storage panel,
	// media's storageevents.go cascade, FileCard chat attachments)
	// can react instantly. Existed=false because move/rename is
	// always a real change — the dedup short-circuit doesn't apply.
	emitFileEvent(ctx, "file.updated", f, false)
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
	if vis == "" {
		return nil, errors.New("visibility must be one of: private, signed, public")
	}
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
	keepRecord := false
	if v, ok := args["keep_record"].(bool); ok {
		keepRecord = v
	}
	hard, err := deleteFile(ctx, pid, id, keepRecord)
	if err != nil {
		return nil, err
	}
	return map[string]any{"deleted": true, "hard": hard}, nil
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
		Visibility:  configuredDefaultVisibility(ctx),
	}
	f, existed, err := saveBytes(ctx, pid, in, body)
	if err != nil {
		return nil, err
	}
	emitFileEvent(ctx, "file.added", f, existed)
	return map[string]any{
		"id":           f.ID,
		"url":          absoluteContentURL(ctx, f),
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

	// Direct presigned-upload protocol (Phase 3): /files/init and
	// /files/{upload_id}/finalize live in this path namespace alongside
	// the numeric file_id routes. Try those first; on miss, fall
	// through to the per-file routes below.
	if a.dispatchDirectUpload(w, r, rest) {
		return
	}

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
	// First segment of the tail decides the action — the rest is
	// cosmetic. /files/6/content/foo.mp4 routes to httpServeContent
	// the same as /files/6/content; the trailing filename is for
	// content-sniffers (Twitter cards, OG scrapers, CDN edges) that
	// route by URL path extension regardless of Content-Type header.
	firstSeg := tail
	if i := strings.IndexByte(tail, '/'); i >= 0 {
		firstSeg = tail[:i]
	}
	switch firstSeg {
	case "content":
		a.httpServeContent(w, r, id)
	case "url":
		// POST /files/:id/url — mint a signed time-limited URL.
		// Mirrors the files_get_url MCP tool but available over plain
		// HTTP for callers that aren't speaking MCP (the dashboard,
		// the media app's storageclient, ad-hoc scripts).
		// Body: {ttl_seconds?: int}. Response: {url, expires_at, file_id}.
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		a.httpMintSignedURL(w, r, id)
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

// httpMintSignedURL is the HTTP-protocol wrapper around the
// files_get_url MCP tool. Same logic, plain-HTTP envelope. Lets the
// media app (and any non-Go caller) skip the JSON-RPC dance just to
// mint a signed URL.
func (a *App) httpMintSignedURL(w http.ResponseWriter, r *http.Request, id int64) {
	ctx := globalCtx
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body struct {
		TTLSeconds int `json:"ttl_seconds"`
	}
	if r.ContentLength > 0 {
		// Body is optional — a TTL of 0 / missing falls back to the
		// install's default (24h today).
		if err := json.NewDecoder(io.LimitReader(r.Body, 1024)).Decode(&body); err != nil && err != io.EOF {
			httpErr(w, http.StatusBadRequest, "invalid json: "+err.Error())
			return
		}
	}
	args := map[string]any{"_project_id": pid, "id": id}
	if body.TTLSeconds > 0 {
		args["ttl_seconds"] = body.TTLSeconds
	}
	out, err := a.toolGetURL(ctx, args)
	if err != nil {
		// Reuse toolGetURL's error semantics: not-found → 404.
		if strings.Contains(err.Error(), "not found") {
			httpErr(w, http.StatusNotFound, err.Error())
			return
		}
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, out)
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
	// format=picker returns the {items:[{id,label,parent}]} envelope
	// the dashboard's permission tree-picker expects (per the
	// list_endpoint convention in app-sdk's ResourceDecl). Without
	// the param, behavior is the original "list child folders of
	// `parent`" — used by the storage-panel UI.
	if r.URL.Query().Get("format") == "picker" {
		items, err := dbAllFoldersAsPickerTree(ctx.AppDB(), pid)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpJSON(w, map[string]any{"items": items})
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

// dbAllFoldersAsPickerTree returns every folder in the project as a
// flat list of {id, label, parent} rows the dashboard's tree picker
// stitches into a tree. ID is a complete resource string ("folder/x/y")
// so the operator's selection becomes a grant.resource directly.
//
// Folders are derived from files.folder; we don't store empty folders
// today, which is fine — operators don't need to grant access to a
// folder that doesn't yet contain anything.
func dbAllFoldersAsPickerTree(db *sql.DB, pid string) ([]map[string]any, error) {
	rows, err := db.Query(
		`SELECT DISTINCT folder FROM files
		 WHERE project_id = ? AND deleted_at IS NULL
		 ORDER BY folder`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]bool{}
	var paths []string
	for rows.Next() {
		var f string
		if err := rows.Scan(&f); err != nil {
			continue
		}
		// Decompose into ancestors so intermediate folders surface
		// even if no file is at that level. Trailing-slash form
		// throughout — matches normaliseFolder.
		f = strings.TrimSuffix(f, "/")
		for f != "" {
			if !seen[f] {
				paths = append(paths, f)
				seen[f] = true
			}
			i := strings.LastIndexByte(f, '/')
			if i < 0 {
				break
			}
			f = f[:i]
		}
	}
	sort.Strings(paths)
	items := make([]map[string]any, 0, len(paths))
	for _, p := range paths {
		// p is "/invoices/q3" form. Strip leading slash for the ID
		// (resources don't double-slash the namespace separator).
		stripped := strings.TrimPrefix(p, "/")
		if stripped == "" {
			continue
		}
		entry := map[string]any{
			"id":    "folder/" + stripped,
			"label": stripped,
		}
		if i := strings.LastIndexByte(stripped, '/'); i > 0 {
			entry["parent"] = "folder/" + stripped[:i]
		}
		items = append(items, entry)
	}
	return items, nil
}

func (a *App) httpListOrSearch(w http.ResponseWriter, r *http.Request) {
	ctx := globalCtx
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	q := r.URL.Query()

	// Batch lookup by id — used by the media app's enrichment helper
	// to resolve a search-result's URLs/metadata in one round-trip.
	// Comma-separated ids; missing rows are silently absent (caller
	// chose how to render the gap). Caps at 500 to keep URL length
	// under the typical 8 KB reverse-proxy limit; callers chunk.
	if idsRaw := q.Get("ids"); idsRaw != "" {
		ids := parseIDList(idsRaw)
		if len(ids) > 500 {
			httpErr(w, http.StatusBadRequest, "ids exceeds 500 — chunk and retry")
			return
		}
		out := make([]*File, 0, len(ids))
		for _, id := range ids {
			f, err := dbGetByID(ctx.AppDB(), pid, id)
			if err == nil && f != nil {
				out = append(out, f)
			}
		}
		httpJSON(w, map[string]any{"files": out})
		return
	}

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

// parseIDList splits "1,2,3" into a slice of int64. Bad entries are
// silently skipped — the endpoint silently absent-bys missing rows
// anyway, so an unparseable id is treated the same as a missing one.
func parseIDList(raw string) []int64 {
	parts := strings.Split(raw, ",")
	out := make([]int64, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" {
			continue
		}
		if id, err := strconv.ParseInt(p, 10, 64); err == nil && id > 0 {
			out = append(out, id)
		}
	}
	return out
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
		Visibility:  effectiveVisibility(ctx, r.FormValue("visibility")),
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
		"id": f.ID, "url": absoluteContentURL(ctx, f), "sha256": f.SHA256,
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

	// Authorise based on visibility + signature. The platform's
	// authMiddleware lets unauthenticated GETs through to app
	// routes, leaving the per-resource decision to the app — same
	// shape as S3, where the bucket's public-read ACL governs
	// anonymous access without the URL needing to be different.
	//
	// X-User-ID is set by authMiddleware on authenticated requests
	// (session cookie, API key, app-install bearer); its absence
	// means the request reached us anonymously, in which case
	// private/signed files are gated by their own auth carrier.
	q := r.URL.Query()
	sig := q.Get("sig")
	exp, _ := strconv.ParseInt(q.Get("exp"), 10, 64)
	authed := r.Header.Get("X-User-ID") != ""
	switch f.Visibility {
	case "public":
		// Anyone can fetch — no auth, no signature.
	case "signed":
		// Signed URL required. Authenticated users (the dashboard
		// browsing files) also get to fetch via their session, so we
		// accept either a valid sig OR a present X-User-ID.
		if !authed && !verifySignature(f.ID, exp, sig) {
			httpErr(w, http.StatusForbidden, "invalid or expired signature")
			return
		}
	case "private":
		// Authenticated request, OR a valid sig (private files can
		// still be shared via files_get_url). Anonymous requests with
		// no sig are refused — this is the gap that needed closing
		// once we relaxed the platform's auth gate for app routes.
		if !authed {
			if !verifySignature(f.ID, exp, sig) {
				httpErr(w, http.StatusForbidden, "private file requires authentication or a valid signature")
				return
			}
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

	key := objectKey(f.SHA256, f.StorageKey)

	// Disk: serve via http.ServeFile (Range + Last-Modified + 304 for
	// free). S3 / remote: 302-redirect to a freshly-minted presigned
	// URL so bytes never proxy through us. The TTL is short — long
	// enough to cover slow downloads, short enough that the URL
	// shouldn't be useful past the request that minted it.
	if path, ok := backend().LocalPath(key); ok {
		http.ServeFile(w, r, path)
		return
	}
	url, err := backend().PresignGet(r.Context(), key, f.Name, f.ContentType, 15*time.Minute)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "presign: "+err.Error())
		return
	}
	http.Redirect(w, r, url, http.StatusFound)
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
		vis := visibilityOrDefault(v)
		if vis == "" {
			httpErr(w, http.StatusBadRequest, "visibility must be one of: private, signed, public")
			return
		}
		updates["visibility"] = vis
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
	keepRecord := r.URL.Query().Get("keep_record") == "true"
	hard, err := deleteFile(ctx, pid, id, keepRecord)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"deleted": true, "hard": hard})
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
	if err := backend().Put(
		context.Background(),
		objectKey(hex, key),
		in.ContentType,
		bytes.NewReader(body),
		int64(len(body)),
	); err != nil {
		return nil, false, fmt.Errorf("backend put: %w", err)
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
	// Absolute URL derived live from the platform's public_url
	// setting (via WhoAmI's sub-second cache) so changing the
	// setting in Settings → Server reflects immediately. The same
	// URL shape is used regardless of visibility — public files just
	// don't require auth at the platform layer (see the GET-app-route
	// carve-out in authMiddleware).
	f.URL = absoluteContentURL(globalCtx, f)
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

// dbFolderHasFiles reports whether any non-deleted file exists at
// the EXACT folder path (children don't count). Used by
// toolCreateFolderCtx for idempotency — if anything is already
// there, the folder doesn't need a placeholder.
func dbFolderHasFiles(db *sql.DB, pid, folder string) (bool, error) {
	folder = normaliseFolder(folder)
	var n int
	err := db.QueryRow(
		`SELECT 1 FROM files
		 WHERE project_id = ? AND folder = ? AND deleted_at IS NULL
		 LIMIT 1`,
		pid, folder,
	).Scan(&n)
	if err == sql.ErrNoRows {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
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

// dbHardDelete removes the row entirely. Caller is responsible for
// removing the blob bytes from disk separately — order matters
// (row first, then blob; if blob removal fails we log + continue,
// leaving an orphan blob recoverable via a sweep, vs the inverse
// where the row points at missing bytes).
func dbHardDelete(db *sql.DB, pid string, id int64) error {
	_, err := db.Exec(
		`DELETE FROM files WHERE id = ? AND project_id = ?`,
		id, pid)
	return err
}

// deleteFile is the unified delete path used by both the MCP tool
// and the HTTP route. keepRecord=true preserves the soft-delete
// behaviour (audit history, dangling bytes); default = hard delete
// (row gone + blob removed). Either way emits file.deleted with
// the pre-delete row so subscribers see the metadata.
//
// Returns hard=true when bytes were physically removed.
func deleteFile(ctx *sdk.AppCtx, pid string, id int64, keepRecord bool) (bool, error) {
	prior, _ := dbGetByID(ctx.AppDB(), pid, id)
	if prior == nil {
		// Already gone; treat as success so the caller doesn't
		// loop on a stale view of the catalog.
		return false, nil
	}

	if keepRecord {
		if err := dbSoftDelete(ctx.AppDB(), pid, id); err != nil {
			return false, err
		}
		emitFileEvent(ctx, "file.deleted", prior, false)
		return false, nil
	}

	if err := dbHardDelete(ctx.AppDB(), pid, id); err != nil {
		return false, err
	}
	// Best-effort blob removal via the backend. Failure here doesn't
	// roll back the row deletion (rolling back would re-introduce a
	// row pointing at potentially-already-gone bytes — worse). The
	// blob becomes an orphan a future sweep can reclaim.
	key := objectKey(prior.SHA256, prior.StorageKey)
	if err := backend().Delete(context.Background(), key); err != nil {
		ctx.Logger().Warn("blob removal failed; row deleted but bytes remain",
			"id", id, "key", key, "err", err)
	}
	emitFileEvent(ctx, "file.deleted", prior, false)
	return true, nil
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

// buildContentURL returns the path-only content URL for a file.
// The filename is appended (URL-escaped) so the URL ends in the
// proper extension — Twitter/Slack/OG scrapers and CDN edges sniff
// content type from the path even when Content-Type headers are
// correct. Storage's router treats the trailing path segment as
// cosmetic (httpServeContent looks the file up by id and ignores
// what comes after `/content`).
//
// Example: /files/6/content/14246297_2160_3840_60fps.mp4
//
// When name is empty (rare — shouldn't happen on a saved row but
// defensive against junk data), falls back to the bare id form
// rather than producing a trailing slash.
func buildContentURL(f *File) string {
	if f == nil || f.Name == "" {
		return fmt.Sprintf("/files/%d/content", f.ID)
	}
	return fmt.Sprintf("/files/%d/content/%s", f.ID, url.PathEscape(f.Name))
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

// visibilityOrDefault normalises an explicit visibility arg. Returns
// "" when the arg is empty / unknown so callers can fall back to the
// install's configured default via configuredDefaultVisibility.
func visibilityOrDefault(s string) string {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "public":
		return "public"
	case "signed":
		return "signed"
	case "private":
		return "private"
	}
	return ""
}

// configuredDefaultVisibility reads the install's default_visibility
// setting (operator-set via the dashboard's settings panel) and falls
// back to "private" when unset or invalid. Source-of-truth is the
// install's config_encrypted blob, surfaced by the framework as
// AppCtx.Config().
func configuredDefaultVisibility(ctx *sdk.AppCtx) string {
	v := visibilityOrDefault(ctx.Config().Get("default_visibility"))
	if v == "" {
		return "private"
	}
	return v
}

// effectiveVisibility picks the visibility for a new upload: the
// explicit arg if it's a valid value, otherwise the install's
// configured default. Used by files_upload + the multipart upload
// route so settings changes take effect immediately without
// per-tool plumbing.
func effectiveVisibility(ctx *sdk.AppCtx, s string) string {
	if v := visibilityOrDefault(s); v != "" {
		return v
	}
	return configuredDefaultVisibility(ctx)
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
