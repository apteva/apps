// Apteva Code v0.1 — repositories with first-class editing tools.
//
// Disk layout (LocalFileStore — v0.1 backend):
//
//   /data/repos/<slug>/files/<path>       file content
//   /data/code.db                         repositories metadata
//
// v0.2 will swap LocalFileStore for a StorageAppFileStore backed by
// the Storage app over the cross-app RPC the SDK is gaining. The
// editing engine and MCP surface stay unchanged.
package main

import (
	"archive/zip"
	"context"
	"embed"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

//go:embed all:templates
var templatesFS embed.FS

// ─── Embedded manifest ─────────────────────────────────────────────

const manifestYAML = `schema: apteva-app/v1
name: code
display_name: Apteva Code
version: 0.1.2
description: |
  Repositories — code workspaces scoped to Apteva projects, with
  first-class editing tools modelled on Claude Code.
author: Apteva
scopes: [project, global]
requires:
  permissions:
    - db.write.app
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: repos_list,             description: "List repositories in this project." }
    - { name: repos_create,           description: "Create a repository from a template." }
    - { name: repos_get,              description: "Repository metadata + tree summary." }
    - { name: repos_archive,          description: "Archive (or hard-delete) a repository." }
    - { name: repos_set_deploy_hints, description: "Set build_cmd / start_cmd / port / env_json." }
    - { name: code_list_files,        description: "List files in a repository." }
    - { name: code_glob,              description: "Find files by glob pattern." }
    - { name: code_grep,              description: "Search file contents with regex or literal text." }
    - { name: code_read_file,         description: "Read with line numbers; supports offset+limit." }
    - { name: code_write_file,        description: "Write or overwrite a file (full content)." }
    - { name: code_edit_file,         description: "Exact-string replacement; uniqueness enforced." }
    - { name: code_multi_edit,        description: "Multiple edits to one file, atomic." }
    - { name: code_rename_path,       description: "Move or rename a file or folder." }
    - { name: code_delete_file,       description: "Delete a file or folder." }
  ui_panels:
    - { slot: project.page, label: "Code", icon: code, entry: /ui/CodePanel.mjs }
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/code
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/code.db
  migrations: migrations/
upgrade_policy: auto-patch
`

// ─── App ───────────────────────────────────────────────────────────

type App struct {
	store FileStore
}

var globalCtx *sdk.AppCtx

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("code requires a db block")
	}
	globalCtx = ctx
	root := os.Getenv("CODE_REPOS_DIR")
	if root == "" {
		root = "/data/repos"
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("mkdir repos root: %w", err)
	}
	a.store = NewLocalFileStore(root)
	ctx.Logger().Info("code mounted",
		"scope_project_id", os.Getenv("APTEVA_PROJECT_ID"),
		"repos_dir", root)
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func main() { sdk.Run(&App{}) }

// ─── Project resolution (mirrors storage app's pattern) ────────────

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

// ─── Template materialisation ──────────────────────────────────────

// applyTemplate copies an embedded template's tree into a freshly-
// created repo via the FileStore. Empty tree (template not found)
// is fine — the repo just starts blank.
func applyTemplate(store FileStore, slug, framework string) (int, error) {
	if framework == "" || framework == "blank" {
		return 0, nil
	}
	root := "templates/" + framework
	count := 0
	err := fs.WalkDir(templatesFS, root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// templates/<framework>/ doesn't exist — leave the repo
			// empty and let the caller carry on.
			if errors.Is(err, fs.ErrNotExist) {
				return fs.SkipAll
			}
			return err
		}
		if d.IsDir() {
			return nil
		}
		body, e := templatesFS.ReadFile(p)
		if e != nil {
			return e
		}
		rel := strings.TrimPrefix(p, root+"/")
		if _, e := store.Write(slug, rel, body); e != nil {
			return e
		}
		count++
		return nil
	})
	return count, err
}

// ─── HTTP routes ────────────────────────────────────────────────────

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		// /api/repos and /api/repos/<slug>/... — handled in handlers.go.
		{Pattern: "/api/repos", Handler: a.handleReposCollection},
		{Pattern: "/api/repos/", Handler: a.handleRepoItem},
	}
}

// ─── Zip export helper used by both REST and (future) deploy app ───

func writeZip(w http.ResponseWriter, store FileStore, slug string) error {
	files, err := store.List(slug, "", true)
	if err != nil {
		return err
	}
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", slug+".zip"))
	zw := zip.NewWriter(w)
	defer zw.Close()
	for _, f := range files {
		if f.IsDir {
			continue
		}
		body, err := store.Read(slug, f.Path)
		if err != nil {
			return err
		}
		hdr := &zip.FileHeader{Name: f.Path, Method: zip.Deflate}
		fw, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		if _, err := fw.Write(body); err != nil {
			return err
		}
	}
	return nil
}

// readZipInto unpacks a zip into a repo via the store. Used by import.
func readZipInto(store FileStore, slug string, zr *zip.Reader) (int, error) {
	count := 0
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		// Strip a single leading directory if present (zips often have
		// one). Reject any entry that, after stripping, escapes — the
		// store's resolve() catches it too but bailing early gives a
		// cleaner error.
		name := strings.TrimPrefix(f.Name, "./")
		if i := strings.Index(name, "/"); i >= 0 {
			// Keep nested paths.
		}
		_ = filepath.Base(name) // touch filepath to ensure import for future use
		clean, err := normalisePath(name)
		if err != nil {
			return count, fmt.Errorf("zip entry %q: %w", f.Name, err)
		}
		rc, err := f.Open()
		if err != nil {
			return count, err
		}
		body := make([]byte, f.UncompressedSize64)
		if _, err := readFull(rc, body); err != nil {
			rc.Close()
			return count, err
		}
		rc.Close()
		if _, err := store.Write(slug, clean, body); err != nil {
			return count, err
		}
		count++
	}
	return count, nil
}

func readFull(r interface{ Read([]byte) (int, error) }, buf []byte) (int, error) {
	total := 0
	for total < len(buf) {
		n, err := r.Read(buf[total:])
		total += n
		if err != nil {
			if total == len(buf) {
				return total, nil
			}
			return total, err
		}
	}
	return total, nil
}

// touch context so go vet doesn't complain about unused import in
// debug builds where nothing else references it yet.
var _ = context.Background
