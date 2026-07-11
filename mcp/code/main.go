// Apteva Code v0.1 — repositories with first-class editing tools.
//
// Disk layout (LocalFileStore — v0.1 backend):
//
//	/data/repos/<slug>/files/<path>       file content
//	/data/code.db                         repositories metadata
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
	"io"
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
version: 0.5.23
description: |
  Repositories — code workspaces scoped to Apteva projects, with
  first-class editing tools modelled on Claude Code. Optionally
  imports repositories from GitHub when a github connection is bound,
  imports ZIP archives through the UI,
  manages native repo issues for bugs, feature requests, and tasks,
  makes grep/read/patch tools more compact for agents with reusable
  patch previews, targeted rejected-hunk context, and stale-hunk recovery,
  isolates global repository storage and hardens command/import boundaries,
  runs finite build/test/lint/generator commands through repos_run_command,
  unifies project navigation with a Repositories tab,
  gives the project-wide Issues inbox the full panel width,
  adds a project-wide Issues inbox in the UI,
  exposes project-wide issue search for agents,
  edits issue metadata in a focused modal,
  presents a compact issue list and chip-based issue metadata controls,
  separates issue lifecycle state from workflow status,
  presents Code and Issues as top-level repository tabs,
  and optionally publishes dev runs at <slug>.<dev_base_hostname>
  through server-native ingress.
author: Apteva
scopes: [project, global]
requires:
  permissions:
    - db.write.app
    - platform.connections.execute
    - platform.apps.call
    - platform.ingress.write
  integrations:
    - role: github
      kind: integration
      required: false
      compatible_slugs: [github]
      label: GitHub
      hint: Connect GitHub to import repositories. Optional — local templates work without it.
      capabilities: [repo.import]
      tools:
        list_repos:  list_repos
        get_archive: get_archive
        get_repo:    get_repo
    - role: simulator
      kind: app
      required: false
      compatible_app_names: [simulator]
      label: Simulator app
      hint: Install the Simulator app to run iOS/Android repos. repos_dev_start delegates mobile frameworks to it automatically.
      capabilities: [sim.run]
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: repos_list,             description: "List repositories in this project." }
    - { name: repos_create,           description: "Create a repository from a template." }
    - { name: repos_get,              description: "Repository metadata + tree summary." }
    - { name: repos_archive,          description: "Archive (or hard-delete) a repository." }
    - { name: repos_set_deploy_hints, description: "Set build_cmd / start_cmd / port / env_json." }
    - { name: repos_export,           description: "Export a repo as a zip; returns base64 bytes for cross-app calls." }
    - { name: code_list_files,        description: "List files in a repository." }
    - { name: code_glob,              description: "Find files by glob pattern." }
    - { name: code_grep,              description: "Search file contents compactly; defaults to file paths." }
    - { name: code_read_file,         description: "Read small paged slices with line numbers; supports offset+limit." }
    - { name: code_read_excerpt,      description: "Read targeted excerpts around lines, ranges, starts, or tails." }
    - { name: code_file_outline,      description: "Return compact Markdown/code structure with line numbers." }
    - { name: code_write_file,        description: "Write or overwrite a file (full content)." }
    - { name: code_apply_patch,       description: "Apply unified diffs with dry-run preview support." }
    - { name: code_edit_file,         description: "Exact-string replacement; uniqueness enforced." }
    - { name: code_multi_edit,        description: "Multiple edits to one file, atomic." }
    - { name: code_rename_path,       description: "Move or rename a file or folder." }
    - { name: code_delete_file,       description: "Delete a file or folder." }
    - { name: repos_mark_template,    description: "Flip a repo into being a template (and set scope)." }
    - { name: repos_unmark_template,  description: "Clear template flag on a repo. Existing forks unaffected." }
    - { name: templates_list,         description: "List user templates visible to this project + embedded ones." }
    - { name: repos_fork,             description: "Create a new repo by snapshot-copying a template or another repo." }
    - { name: repos_import_github,    description: "Import a GitHub repo as a local repository (gzip tarball snapshot)." }
    - { name: repos_dev_start,        description: "Start a long-running dev preview server for a repo." }
    - { name: repos_run_command,      description: "Run a finite repo command such as build, test, lint, typecheck, or generator and return exit-code semantics." }
    - { name: repos_dev_stop,         description: "Stop the dev process for a repo." }
    - { name: repos_dev_status,       description: "Get the current dev run state (port, pid, status, framework)." }
    - { name: repos_dev_logs,         description: "Tail the dev run's stdout/stderr log file." }
    - { name: issues_list,            description: "List native Code issues for a repository." }
    - { name: issues_search,          description: "Search native Code issues across all repositories in a project." }
    - { name: issues_get,             description: "Get a native Code issue with comments, links, and activity." }
    - { name: issues_create,          description: "Create a native Code issue." }
    - { name: issues_update,          description: "Update native Code issue fields." }
    - { name: issues_comment,         description: "Add a comment to a native Code issue." }
    - { name: issues_close,           description: "Close a native Code issue." }
    - { name: issues_reopen,          description: "Reopen a native Code issue." }
    - { name: issues_link_path,       description: "Link an issue to a repository path or line range." }
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
	store    FileStore
	dataDir  string
	dev      *devSupervisor
	commands commandCoordinator
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
		// ctx.DataDir() points at the writable per-install directory the
		// platform reserved for this app (same dir AppDB lives in).
		// Falls back to dirname(DB_PATH) on older platforms via the SDK.
		// Only when neither is available do we use the legacy "/data/repos"
		// container default — and at that point the app is almost certainly
		// running on a host that doesn't have /data writable, so MkdirAll
		// will fail with a clear error rather than silently picking a
		// useless directory.
		if dd := ctx.DataDir(); dd != "" {
			root = filepath.Join(dd, "repos")
		} else {
			root = "/data/repos"
		}
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("mkdir repos root: %w", err)
	}
	localStore := NewLocalFileStore(root)
	if err := migrateLegacyRepoStorage(ctx.AppDB(), localStore); err != nil {
		return fmt.Errorf("migrate repository storage: %w", err)
	}
	a.store = localStore

	// Dev runtime — live "Run" surface for repos. Lives inside this
	// sidecar process; one supervised child per (project, repo). The
	// dataDir is the per-install writable dir, separate from the
	// repos root so log files / future dev-run state don't pollute
	// the storage tree the user actually edits.
	dataDir := ctx.DataDir()
	if dataDir == "" {
		dataDir = filepath.Dir(root)
	}
	a.dataDir = dataDir
	portStart := atoiOr(os.Getenv("CODE_DEV_PORT_RANGE_START"), 6100)
	portEnd := atoiOr(os.Getenv("CODE_DEV_PORT_RANGE_END"), 6199)
	a.dev = newDevSupervisor(dataDir, a.store, a, portStart, portEnd)
	if err := a.dev.reconcileOrphanDevRuns(ctx); err != nil {
		ctx.Logger().Warn("dev orphan reconcile failed", "err", err)
	}

	ctx.Logger().Info("code mounted",
		"scope_project_id", os.Getenv("APTEVA_PROJECT_ID"),
		"repos_dir", root,
		"dev_port_range", fmt.Sprintf("%d-%d", portStart, portEnd))
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error {
	if a.dev != nil {
		a.dev.stopAll()
	}
	return nil
}
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
	// The platform-injected header is authoritative. Query fallback remains
	// for older dashboards that predate header propagation.
	if v := r.Header.Get("X-Apteva-Project-ID"); v != "" {
		return v, nil
	}
	if v := r.URL.Query().Get("project_id"); v != "" {
		return v, nil
	}
	return "", errors.New("project_id required in query string when install scope=global")
}

// ─── Template materialisation ──────────────────────────────────────

// applyTemplate copies an embedded template's tree into a freshly-
// created repo via the FileStore. Thin wrapper over fork() — kept
// because both the create-repo paths still call it by name.
func applyTemplate(store FileStore, slug, framework string) (int, error) {
	return fork(embeddedReader{}, framework, store, slug)
}

// ─── HTTP routes ────────────────────────────────────────────────────

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		// /api/repos and /api/repos/<slug>/... — handled in handlers.go.
		{Pattern: "/api/repos", Handler: a.handleReposCollection},
		{Pattern: "/api/repos/", Handler: a.handleRepoItem},
		{Pattern: "/api/issues", Handler: a.httpIssuesCollection},
		{Pattern: "/api/templates", Handler: a.handleTemplatesList},
		{Pattern: "/api/github/import", Handler: a.handleGithubImport},
		{Pattern: "/api/github/repos", Handler: a.handleGithubReposList},
	}
}

// ─── Zip export helper used by both REST and (future) deploy app ───

func writeZip(w http.ResponseWriter, store FileStore, slug string) error {
	w.Header().Set("Content-Type", "application/zip")
	w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", slug+".zip"))
	return zipRepo(w, store, slug)
}

// zipRepo streams a repo zip into any io.Writer. Used by the HTTP
// export handler (writes to the response) and by the repos_export MCP
// tool (writes to a bytes.Buffer for base64 envelope).
//
// Dev artifacts are skipped — node_modules / .next / .git / dist /
// build / .cache. Once the dev runtime ran on a repo, those dirs
// can be hundreds of megabytes; consumers (Deploy, GitHub re-export,
// agent-side downloads) need the source tree, not the build cache.
// Same skip list Deploy's kind=local fetcher already uses, so a
// kind=code fetch and a kind=local fetch produce equivalent trees.
func zipRepo(w io.Writer, store FileStore, slug string) error {
	files, err := store.List(slug, "", true)
	if err != nil {
		return err
	}
	zw := zip.NewWriter(w)
	defer zw.Close()
	for _, f := range files {
		if f.IsDir {
			continue
		}
		if shouldSkipForExport(f.Path) {
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

// shouldSkipForExport drops dev/build cache directories so exports
// stay source-only. A path is skipped if any of its segments names a
// well-known dev artifact dir.
func shouldSkipForExport(rel string) bool {
	for _, seg := range strings.Split(rel, "/") {
		switch seg {
		case "node_modules", ".next", ".git", "dist", "build", ".cache":
			return true
		}
	}
	return false
}

func shouldSkipGenerated(rel string) bool {
	for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
		switch seg {
		case "node_modules", ".git", ".next", ".nuxt", ".cache", ".turbo",
			"dist", "build", "coverage", "vendor", "Pods", "DerivedData":
			return true
		}
	}
	return false
}

func filterGenerated(files []FileMeta, include bool) []FileMeta {
	if include {
		return files
	}
	out := files[:0]
	for _, file := range files {
		if !shouldSkipGenerated(file.Path) {
			out = append(out, file)
		}
	}
	return out
}

type sourceFileLister interface {
	ListSource(slug, path string, recursive, includeGenerated bool) ([]FileMeta, error)
}

func listSourceFiles(store FileStore, slug, path string, recursive, includeGenerated bool) ([]FileMeta, error) {
	if source, ok := store.(sourceFileLister); ok {
		return source.ListSource(slug, path, recursive, includeGenerated)
	}
	files, err := store.List(slug, path, recursive)
	if err != nil {
		return nil, err
	}
	return filterGenerated(files, includeGenerated), nil
}

// readZipInto unpacks a zip into a repo via the store. Used by import.
func readZipInto(store FileStore, slug string, zr *zip.Reader) (int, error) {
	count := 0
	var total int64
	limits := currentImportLimits()
	type pendingFile struct {
		path string
		body []byte
	}
	pending := make([]pendingFile, 0, len(zr.File))
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
		count++
		size := int64(f.UncompressedSize64)
		if err := checkImportEntry(limits, clean, size, total, count); err != nil {
			return count - 1, err
		}
		rc, err := f.Open()
		if err != nil {
			return count, err
		}
		body, err := io.ReadAll(io.LimitReader(rc, size+1))
		if err != nil {
			rc.Close()
			return count, err
		}
		rc.Close()
		if int64(len(body)) != size {
			return count - 1, fmt.Errorf("zip entry %q size mismatch", clean)
		}
		pending = append(pending, pendingFile{path: clean, body: body})
		total += size
	}
	for _, file := range pending {
		if _, err := store.Write(slug, file.path, file.body); err != nil {
			return 0, err
		}
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
