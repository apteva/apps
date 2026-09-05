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

//go:embed apteva.yaml
var manifestYAML string

// ─── App ───────────────────────────────────────────────────────────

type App struct {
	store     FileStore
	dataDir   string
	dev       *devSupervisor
	commands  commandCoordinator
	git       *gitService
	locks     *repoLockSet
	summaries summaryCache
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
	a.locks = newRepoLockSet()
	a.store = &lockedFileStore{inner: localStore, locks: a.locks}

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
	gitService, err := newGitService(dataDir, localStore, a.locks)
	if err != nil {
		return fmt.Errorf("initialize Git service: %w", err)
	}
	a.git = gitService
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
	a.commands.cancelAll()
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
		{Pattern: "/api/git/import", Handler: a.handleGitImport},
		{Pattern: "/api/git/connections", Handler: a.handleGitConnections},
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
	_, err := withRepoWrite(store, slug, func(raw FileStore) (bool, error) { return true, zipRepoUnlocked(w, raw, slug) })
	return err
}
func zipRepoUnlocked(w io.Writer, store FileStore, slug string) error {
	files, err := listSourceFiles(store, slug, "", true, false)
	if err != nil {
		return err
	}
	var total int64
	for _, f := range files {
		if !f.IsDir {
			if f.Size > maxFileBytes() {
				return fmt.Errorf("file %s exceeds export size limit", f.Path)
			}
			total += f.Size
		}
	}
	if total > currentImportLimits().TotalBytes {
		return errors.New("repository source exceeds export limit; export a smaller source scope")
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
		mode := os.FileMode(f.Mode)
		if mode == 0 {
			mode = 0644
		}
		hdr.SetMode(mode)
		fw, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		if _, err := fw.Write(body); err != nil {
			return err
		}
	}
	return zw.Close()
}

// shouldSkipForExport drops dev/build cache directories so exports
// stay source-only. A path is skipped if any of its segments names a
// well-known dev artifact dir.
func shouldSkipForExport(rel string) bool { return shouldSkipGenerated(rel) }

func shouldSkipGenerated(rel string) bool {
	if raw, ok := os.LookupEnv("CODE_SOURCE_EXCLUDE_DIRS"); ok {
		names := strings.Split(raw, ",")
		for _, seg := range strings.Split(filepath.ToSlash(rel), "/") {
			if seg == ".git" {
				return true
			}
			for _, name := range names {
				if seg == strings.TrimSpace(name) {
					return true
				}
			}
		}
		return false
	}
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
	return withRepoWrite(store, slug, func(raw FileStore) (int, error) { return readZipIntoUnlocked(raw, slug, zr) })
}
func readZipIntoUnlocked(store FileStore, slug string, zr *zip.Reader) (int, error) {
	count := 0
	var total int64
	limits := currentImportLimits()
	type pendingFile struct {
		path string
		body []byte
	}
	pending := make([]pendingFile, 0, len(zr.File))
	modes := map[string]os.FileMode{}
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
		if _, exists := modes[clean]; exists {
			return 0, fmt.Errorf("duplicate archive path %q", clean)
		}
		if f.Mode()&os.ModeSymlink != 0 {
			return 0, errors.New("ZIP symlinks are not supported; use Git or workspace transfer")
		}
		modes[clean] = f.Mode().Perm()
		if modes[clean] == 0 {
			modes[clean] = 0644
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
	changes := make([]fileMutation, 0, len(pending))
	for _, file := range pending {
		changes = append(changes, fileMutation{Path: file.path, Body: file.body, Mode: modes[file.path]})
	}
	if err := applyFileMutations(store, slug, changes); err != nil {
		return 0, err
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
