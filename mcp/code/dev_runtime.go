package main

// Dev runtime — Replit-style "Run" for repos.
//
// One supervised child process per (project_id, repo_id), spawned with
// cwd set to the repo's on-disk storage_root. Edits via code_edit_file /
// code_write_file land directly in that directory (atomic temp+rename
// in LocalFileStore.Write) so the framework's own watcher (next dev,
// bun --hot, vite, …) picks them up instantly.
//
// Boundary with the deploy app: deploy snapshots source, builds an
// artifact, supervises the production release, wires domains. Code's
// dev runtime does none of that — no build phase, no domain wiring,
// no release rows. Cheaper, faster restart cycle, scoped to the
// editing experience.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// ─── Framework registry ───────────────────────────────────────────

// devFramework knows how to detect a framework from a file tree and
// produce a dev-server command. Mirrors deploy's Builder interface
// shape but specialised for dev (no build, different commands, no
// artifact dir). Lives separately from deploy's because:
//   - dev's command is `next dev` not `next start`, etc.
//   - dev cares about static-server-in-process, deploy doesn't here
//   - the apps are independent Go modules (per the workspace's
//     "each top-level subdirectory is its own git repo" convention)
type devFramework struct {
	Name    string
	Detect  func(store FileStore, slug string) bool
	Command func(srcDir string) (bin string, args []string, err error)
	// SelfHostsServer = true for "static" — we run an in-process
	// FileServer instead of spawning a child. The supervisor branches
	// on this flag in startDevRun.
	SelfHostsServer bool
	// RemoteRunner names a sibling app the dev run is delegated to
	// instead of spawning a local child. "" = local. "simulator" for
	// mobile frameworks — startDevRun routes to startRemoteRun, which
	// calls the Simulator app's sims_run over the cross-app RPC.
	RemoteRunner string
}

var devFrameworks = []devFramework{
	{Name: "nextjs", Detect: detectDevNextJS, Command: cmdDevNextJS},
	{Name: "node", Detect: detectDevNode, Command: cmdDevNode},
	{Name: "go", Detect: detectDevGo, Command: cmdDevGo},
	{Name: "static", Detect: detectDevStatic, SelfHostsServer: true},
	// Mobile frameworks — no local command; delegated to the Simulator
	// app. Listed before "static"-style fallbacks would matter but
	// after the web frameworks so a repo that's somehow both (a
	// Next.js web companion next to an Android module) prefers the web
	// dev loop. detectDevAndroid/IOS are specific enough that this
	// ordering rarely bites.
	{Name: "ios", Detect: detectDevIOS, RemoteRunner: "simulator"},
	{Name: "android", Detect: detectDevAndroid, RemoteRunner: "simulator"},
	// "blank" has no detector — caller must specify framework=blank
	// AND pass run_cmd. Listed last because Detect == nil never fires.
}

func devFrameworkByName(name string) *devFramework {
	for i := range devFrameworks {
		if devFrameworks[i].Name == name {
			return &devFrameworks[i]
		}
	}
	if name == "blank" {
		return &devFramework{Name: "blank"}
	}
	return nil
}

// detectDevFramework returns the first matching framework's name.
// Order in devFrameworks is the priority — nextjs before generic node
// so `next` wins; go before static so a Go server with an embedded
// /static/ dir doesn't get misclassified.
func detectDevFramework(store FileStore, slug string) string {
	for _, f := range devFrameworks {
		if f.Detect != nil && f.Detect(store, slug) {
			return f.Name
		}
	}
	return ""
}

// ─── Per-framework detectors ──────────────────────────────────────

func detectDevNextJS(store FileStore, slug string) bool {
	body, err := store.Read(slug, "package.json")
	if err != nil {
		return false
	}
	return strings.Contains(string(body), `"next":`)
}

func detectDevNode(store FileStore, slug string) bool {
	_, err := store.Read(slug, "package.json")
	return err == nil
}

func detectDevGo(store FileStore, slug string) bool {
	_, err := store.Read(slug, "go.mod")
	return err == nil
}

func detectDevStatic(store FileStore, slug string) bool {
	_, err := store.Read(slug, "index.html")
	return err == nil
}

// detectDevIOS matches an Xcode project (*.xcodeproj / *.xcworkspace
// at the repo root) or a SwiftPM package declaring an iOS platform.
// We only scan the top level — nested example projects shouldn't make
// the whole repo "an iOS app".
func detectDevIOS(store FileStore, slug string) bool {
	entries, err := store.List(slug, "", false)
	if err == nil {
		for _, e := range entries {
			if strings.HasSuffix(e.Path, ".xcodeproj") || strings.HasSuffix(e.Path, ".xcworkspace") {
				return true
			}
		}
	}
	if body, err := store.Read(slug, "Package.swift"); err == nil {
		return strings.Contains(string(body), ".iOS(")
	}
	return false
}

// detectDevAndroid matches a Gradle project with an Android
// application module: a root settings.gradle[.kts] plus a module
// applying the com.android.application plugin. Checking the plugin
// (not just the presence of gradle) avoids classifying a plain JVM /
// Kotlin-Multiplatform-without-Android repo as Android.
func detectDevAndroid(store FileStore, slug string) bool {
	hasSettings := false
	for _, name := range []string{"settings.gradle", "settings.gradle.kts"} {
		if _, err := store.Read(slug, name); err == nil {
			hasSettings = true
			break
		}
	}
	if !hasSettings {
		return false
	}
	// Scan module build files for the android application plugin.
	files, err := store.List(slug, "", true)
	if err != nil {
		return false
	}
	for _, f := range files {
		base := f.Path
		if i := strings.LastIndex(base, "/"); i >= 0 {
			base = base[i+1:]
		}
		if base != "build.gradle" && base != "build.gradle.kts" {
			continue
		}
		body, err := store.Read(slug, f.Path)
		if err != nil {
			continue
		}
		s := string(body)
		if strings.Contains(s, "com.android.application") {
			return true
		}
	}
	return false
}

// ─── Per-framework dev commands ───────────────────────────────────

// cmdDevNextJS prefers the project's local next binary over a global
// install — `node_modules/.bin/next dev` is what npm/bun/pnpm scripts
// resolve to anyway. Falls back to `<pm> run dev` when no local bin
// (the framework was just installed cold).
func cmdDevNextJS(srcDir string) (string, []string, error) {
	if exists(filepath.Join(srcDir, "node_modules", ".bin", "next")) {
		return filepath.Join(srcDir, "node_modules", ".bin", "next"), []string{"dev"}, nil
	}
	pm := detectPackageManagerInDir(srcDir)
	if _, err := exec.LookPath(pm); err != nil {
		return "", nil, fmt.Errorf("%s not on PATH; install it or run npm install in the repo first", pm)
	}
	return pm, []string{"run", "dev"}, nil
}

// cmdDevNode prefers `<pm> run dev` if the package.json has one,
// else `<pm> start`. Doesn't try to run a non-existent script — the
// readiness probe would just time out and the user gets a confusing
// crash.
//
// Bun-script convention fallback: package.json with no dev/start
// script but a root-level serve.ts (or server.ts) → run it via
// `bun run serve.ts`. Common in Bun-native projects that drive their
// own server with Bun.serve. Only kicks in when bun is on PATH —
// npm/yarn/pnpm can't run a .ts file without a TS runner.
func cmdDevNode(srcDir string) (string, []string, error) {
	pm := detectPackageManagerInDir(srcDir)
	if _, err := exec.LookPath(pm); err != nil {
		return "", nil, fmt.Errorf("%s not on PATH", pm)
	}
	if hasScript(srcDir, "dev") {
		return pm, []string{"run", "dev"}, nil
	}
	if hasScript(srcDir, "start") {
		return pm, []string{"run", "start"}, nil
	}
	if bunScript := findBunRunScript(srcDir); bunScript != "" {
		if _, err := exec.LookPath("bun"); err == nil {
			return "bun", []string{"run", bunScript}, nil
		}
	}
	return "", nil, errors.New(`no "dev" or "start" script in package.json — set run_cmd explicitly (or add bun + serve.ts for Bun-script convention)`)
}

// findBunRunScript looks for the Bun-script convention's runtime
// entry. serve.ts is canonical (apteva-site et al); server.ts is the
// next most common. Returns the filename or "" if none.
func findBunRunScript(dir string) string {
	for _, name := range []string{"serve.ts", "server.ts"} {
		if exists(filepath.Join(dir, name)) {
			return name
		}
	}
	return ""
}

func cmdDevGo(srcDir string) (string, []string, error) {
	if _, err := exec.LookPath("go"); err != nil {
		return "", nil, errors.New("go not on PATH")
	}
	return "go", []string{"run", "."}, nil
}

// detectPackageManagerInDir mirrors deploy's lockfile-precedence rule
// (bun > pnpm > yarn > npm) so the dev runtime and the deploy build
// pick the same tool when both observe the same repo.
//
// Both bun.lockb (legacy binary) and bun.lock (current text format)
// are accepted — the latter is what current bun init / bun install
// creates, so missing it silently falls through to npm.
func detectPackageManagerInDir(dir string) string {
	switch {
	case exists(filepath.Join(dir, "bun.lockb")) || exists(filepath.Join(dir, "bun.lock")):
		return "bun"
	case exists(filepath.Join(dir, "pnpm-lock.yaml")):
		return "pnpm"
	case exists(filepath.Join(dir, "yarn.lock")):
		return "yarn"
	default:
		body, err := os.ReadFile(filepath.Join(dir, "package.json"))
		if err == nil {
			var pkg struct {
				PackageManager string `json:"packageManager"`
			}
			if json.Unmarshal(body, &pkg) == nil {
				pm := strings.ToLower(pkg.PackageManager)
				switch {
				case strings.HasPrefix(pm, "bun@"):
					return "bun"
				case strings.HasPrefix(pm, "pnpm@"):
					return "pnpm"
				case strings.HasPrefix(pm, "yarn@"):
					return "yarn"
				case strings.HasPrefix(pm, "npm@"):
					return "npm"
				}
			}
		}
		return "npm"
	}
}

func hasScript(dir, name string) bool {
	body, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if err != nil {
		return false
	}
	var pkg struct {
		Scripts map[string]string `json:"scripts"`
	}
	if json.Unmarshal(body, &pkg) != nil {
		return false
	}
	_, ok := pkg.Scripts[name]
	return ok
}

func exists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// ─── Supervisor ───────────────────────────────────────────────────

// devSupervisor is the in-process registry of running dev processes.
// One entry per (project_id, repo_id). The DB row in dev_runs is the
// durable state; this struct is the live cmd handle for stop/restart.
type devSupervisor struct {
	mu      sync.Mutex
	all     map[int64]*devProcess // keyed by dev_runs.id
	dataDir string
	store   FileStore
	app     *App

	portRangeStart int
	portRangeEnd   int
}

type devProcess struct {
	DevRunID int64
	Port     int
	cmd      *exec.Cmd // nil for static
	cancel   context.CancelFunc
	server   *http.Server // non-nil for static
	logFile  *os.File
	stopCh   chan struct{}
}

func newDevSupervisor(dataDir string, store FileStore, app *App, portStart, portEnd int) *devSupervisor {
	return &devSupervisor{
		all:            map[int64]*devProcess{},
		dataDir:        dataDir,
		store:          store,
		app:            app,
		portRangeStart: portStart,
		portRangeEnd:   portEnd,
	}
}

func (s *devSupervisor) get(id int64) *devProcess {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.all[id]
}

func (s *devSupervisor) put(p *devProcess) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.all[p.DevRunID] = p
}

func (s *devSupervisor) drop(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.all, id)
}

func (s *devSupervisor) all_() []*devProcess {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*devProcess, 0, len(s.all))
	for _, p := range s.all {
		out = append(out, p)
	}
	return out
}

// ─── Lifecycle ────────────────────────────────────────────────────

type startDevInput struct {
	ProjectID string
	Repo      *Repo
	Framework string // optional override
	RunCmd    string // optional shell override
	EnvJSON   string
}

// startDevRun is the orchestrating entry point: stop any existing run
// for this repo, choose a framework + command, allocate a port,
// spawn / mount, persist the new state. Returns once the supervisor
// has started; the readiness probe runs in a goroutine.
func (s *devSupervisor) startDevRun(ctx *sdk.AppCtx, in startDevInput) (*DevRun, error) {
	if in.Repo == nil {
		return nil, errors.New("repo required")
	}
	startCtx, cancelStart := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancelStart()
	if s.app != nil {
		release, err := s.app.commands.acquire(startCtx, in.Repo.ID)
		if err != nil {
			return nil, err
		}
		defer release()
	}
	repoStore := bindRepoStore(s.store, in.Repo)
	pp, ok := repoStore.(FileStoreLocalPath)
	if !ok {
		return nil, errors.New("dev runtime requires a local filesystem store; remote storage backends aren't supported yet")
	}
	srcDir := pp.RepoPath(in.Repo.Slug)
	if _, err := os.Stat(srcDir); err != nil {
		return nil, fmt.Errorf("repo source dir not found: %w", err)
	}

	// Stop any prior run for this repo before we start a new one — the
	// UNIQUE(project_id, repo_id) on dev_runs enforces one row, but a
	// stale process might still be alive if a previous start succeeded
	// and the panel called start again.
	if existing, _ := dbGetDevRun(ctx.AppDB(), in.ProjectID, in.Repo.ID); existing != nil {
		_ = s.stopDevRun(ctx, in.ProjectID, in.Repo.ID)
	}

	fw := in.Framework
	if fw == "" {
		fw = detectDevFramework(repoStore, in.Repo.Slug)
	}
	if fw == "" {
		return nil, errors.New(`could not detect framework — pass framework="blank" with a run_cmd, or add a marker file (package.json, go.mod, index.html)`)
	}

	// Mobile frameworks are delegated to the Simulator app rather than
	// spawned locally. startRemoteRun tars the source, calls sims_run,
	// and persists a dev_runs row with runner=simulator + the stream
	// URL the panel embeds. See dev_remote.go.
	if def := devFrameworkByName(fw); def != nil && def.RemoteRunner != "" {
		return s.startRemoteRun(ctx, in, srcDir, fw, def.RemoteRunner)
	}

	port, err := s.allocateDevPort()
	if err != nil {
		return nil, err
	}

	logPath := s.logPathForRepo(in.Repo.ID)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir log dir: %w", err)
	}
	// Truncate on each start — dev runs aren't archived; the file
	// stays small and the user sees only the current session's output.
	logF, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open log: %w", err)
	}
	logOut := newCappedWriter(logF, maxProcessLogBytes)
	fmt.Fprintf(logOut, "=== dev run for %s/%d (%s) at %s ===\n",
		in.ProjectID, in.Repo.ID, in.Repo.Slug, time.Now().UTC().Format(time.RFC3339))

	dr, err := dbUpsertDevRun(ctx.AppDB(), DevRun{
		ProjectID: in.ProjectID, RepoID: in.Repo.ID,
		Status: "starting", Port: port, PID: 0,
		Framework: fw, RunCmd: in.RunCmd, EnvJSON: in.EnvJSON,
		LogPath: logPath, StartedAt: time.Now().UTC().Format(time.RFC3339),
	})
	if err != nil {
		logF.Close()
		return nil, fmt.Errorf("persist dev_run: %w", err)
	}

	// JS/Bun/Node repos need dependencies before dev/test commands can
	// run. This is based on repo contents, not just the detected
	// framework, so framework=blank with run_cmd="bun run build" still
	// bootstraps local CLIs such as Tailwind, Vite, Astro, Next, and
	// TypeScript on a clean workspace. Code's dev runner remains a
	// test/preview path; production releases still belong to Deploy.
	processEnv, err := commandEnvironment(s.dataDir, in.EnvJSON)
	if err != nil {
		logF.Close()
		return nil, err
	}
	if in.ProjectID != "" {
		processEnv = append(processEnv, "APTEVA_PROJECT_ID="+in.ProjectID)
	}
	if plan, err := nodeDepsInstallPlan(srcDir); err != nil {
		logF.Close()
		return nil, err
	} else if plan.Needed {
		if err := installNodeDeps(startCtx, srcDir, logOut, plan, processEnv); err != nil {
			_ = dbUpdateDevRun(ctx.AppDB(), dr.ID, map[string]any{
				"status":     "crashed",
				"stopped_at": time.Now().UTC().Format(time.RFC3339),
				"error":      err.Error(),
			})
			logF.Close()
			return nil, err
		}
	}

	if err := s.spawn(ctx, dr, srcDir, fw, in.RunCmd, in.EnvJSON, port, logF, logOut); err != nil {
		_ = dbUpdateDevRun(ctx.AppDB(), dr.ID, map[string]any{
			"status":     "crashed",
			"stopped_at": time.Now().UTC().Format(time.RFC3339),
			"error":      err.Error(),
		})
		logF.Close()
		return nil, err
	}
	return dbGetDevRun(ctx.AppDB(), in.ProjectID, in.Repo.ID)
}

var dependencyFingerprintFiles = []string{
	"package.json",
	"bun.lock",
	"bun.lockb",
	"package-lock.json",
	"pnpm-lock.yaml",
	"yarn.lock",
}

type nodeDepsInstall struct {
	Needed bool
	Reason string
	PM     string
	Args   []string
	Hash   string
}

// nodeDepsInstallPlan decides whether the dev runner should install JS
// dependencies before executing a repo command. It intentionally does
// not look at the framework; run_cmd overrides for "blank" repos still
// need package-local CLIs when package.json is present.
func nodeDepsInstallPlan(srcDir string) (nodeDepsInstall, error) {
	if os.Getenv("CODE_SKIP_AUTO_INSTALL") != "" {
		return nodeDepsInstall{}, nil
	}
	if !exists(filepath.Join(srcDir, "package.json")) {
		return nodeDepsInstall{}, nil
	}
	hash, err := dependencyFingerprint(srcDir)
	if err != nil {
		return nodeDepsInstall{}, err
	}
	pm := detectPackageManagerInDir(srcDir)
	plan := nodeDepsInstall{PM: pm, Args: installArgsForPackageManager(srcDir, pm), Hash: hash}
	if !exists(filepath.Join(srcDir, "node_modules")) {
		plan.Needed = true
		plan.Reason = "dependencies missing"
		return plan, nil
	}
	prev, err := os.ReadFile(nodeDepsStatePath(srcDir))
	if err != nil || strings.TrimSpace(string(prev)) != hash {
		plan.Needed = true
		plan.Reason = "dependency files changed"
		return plan, nil
	}
	return plan, nil
}

func installArgsForPackageManager(srcDir, pm string) []string {
	switch pm {
	case "bun":
		if exists(filepath.Join(srcDir, "bun.lock")) || exists(filepath.Join(srcDir, "bun.lockb")) {
			return []string{"install", "--frozen-lockfile"}
		}
	case "pnpm":
		if exists(filepath.Join(srcDir, "pnpm-lock.yaml")) {
			return []string{"install", "--frozen-lockfile"}
		}
	case "yarn":
		if exists(filepath.Join(srcDir, "yarn.lock")) {
			return []string{"install", "--frozen-lockfile"}
		}
	case "npm":
		if exists(filepath.Join(srcDir, "package-lock.json")) {
			return []string{"ci"}
		}
	}
	return []string{"install"}
}

func dependencyFingerprint(srcDir string) (string, error) {
	type entry struct {
		Path string
		Sum  string
	}
	entries := make([]entry, 0, len(dependencyFingerprintFiles))
	for _, name := range dependencyFingerprintFiles {
		body, err := os.ReadFile(filepath.Join(srcDir, name))
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				continue
			}
			return "", err
		}
		sum := sha256.Sum256(body)
		entries = append(entries, entry{Path: name, Sum: fmt.Sprintf("%x", sum[:])})
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Path < entries[j].Path })
	body, _ := json.Marshal(entries)
	sum := sha256.Sum256(body)
	return fmt.Sprintf("%x", sum[:]), nil
}

func nodeDepsStatePath(srcDir string) string {
	return filepath.Join(srcDir, "node_modules", ".apteva-code-deps.sha256")
}

// installNodeDeps runs the selected package-manager install
// synchronously, streaming stdout/stderr into the dev log so the panel
// shows progress live.
func installNodeDeps(ctx context.Context, srcDir string, logF io.Writer, plan nodeDepsInstall, env []string) error {
	pm := plan.PM
	if pm == "" {
		pm = detectPackageManagerInDir(srcDir)
	}
	args := plan.Args
	if len(args) == 0 {
		args = installArgsForPackageManager(srcDir, pm)
	}
	if _, err := exec.LookPath(pm); err != nil {
		return fmt.Errorf("%s not on PATH; install it first or set CODE_SKIP_AUTO_INSTALL=1 and run install manually", pm)
	}
	reason := plan.Reason
	if reason == "" {
		reason = "dependencies need bootstrap"
	}
	fmt.Fprintf(logF, "%s; running %s %s\n", reason, pm, strings.Join(args, " "))
	fmt.Fprintf(logF, "+ %s %s (cwd=%s)\n", pm, strings.Join(args, " "), srcDir)
	cmd := exec.Command(pm, args...)
	cmd.Dir = srcDir
	cmd.Env = env
	cmd.Stdout = logF
	cmd.Stderr = logF
	if err := runProcessGroup(ctx, cmd); err != nil {
		return fmt.Errorf("%s %s: %w (see dev log for details)", pm, strings.Join(args, " "), err)
	}
	hash := plan.Hash
	if hash == "" {
		var err error
		hash, err = dependencyFingerprint(srcDir)
		if err != nil {
			return err
		}
	}
	if err := os.MkdirAll(filepath.Join(srcDir, "node_modules"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(nodeDepsStatePath(srcDir), []byte(hash+"\n"), 0o644); err != nil {
		return err
	}
	fmt.Fprintln(logF, "=== install complete ===")
	return nil
}

// spawn does the actual exec / static-server dance. Splits the static
// path (no child process; in-process FileServer) from the process
// path (exec.Cmd + supervisor goroutine). Both end up in s.all so
// stopDevRun can shut them down uniformly.
func (s *devSupervisor) spawn(ctx *sdk.AppCtx, dr *DevRun, srcDir, framework, runCmd, envJSON string, port int, logF *os.File, logOut io.Writer) error {
	if framework == "static" {
		return s.spawnStatic(ctx, dr, srcDir, port, logF, logOut)
	}
	return s.spawnProcess(ctx, dr, srcDir, framework, runCmd, envJSON, port, logF, logOut)
}

func (s *devSupervisor) spawnStatic(ctx *sdk.AppCtx, dr *DevRun, srcDir string, port int, logF *os.File, logOut io.Writer) error {
	mux := http.NewServeMux()
	mux.Handle("/", staticPreviewHandler(srcDir))
	srv := &http.Server{Addr: fmt.Sprintf("127.0.0.1:%d", port), Handler: mux}
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		return fmt.Errorf("listen :%d: %w", port, err)
	}
	stop := make(chan struct{})
	go func() {
		defer close(stop)
		fmt.Fprintf(logOut, "+ in-process FileServer (cwd=%s, port=%d)\n", srcDir, port)
		err := srv.Serve(ln)
		if err != nil && !errors.Is(err, http.ErrServerClosed) {
			fmt.Fprintf(logOut, "static server error: %v\n", err)
			s.markCrashed(ctx, dr.ID, err.Error())
		} else {
			fmt.Fprintf(logOut, "=== static server stopped at %s ===\n", time.Now().UTC().Format(time.RFC3339))
		}
		_ = logF.Close()
	}()

	p := &devProcess{
		DevRunID: dr.ID, Port: port, server: srv, logFile: logF, stopCh: stop,
	}
	s.put(p)
	_ = dbUpdateDevRun(ctx.AppDB(), dr.ID, map[string]any{
		"status": "live",
		"pid":    os.Getpid(),
	})
	return nil
}

func (s *devSupervisor) spawnProcess(ctx *sdk.AppCtx, dr *DevRun, srcDir, framework, runCmd, envJSON string, port int, logF *os.File, logOut io.Writer) error {
	bin, args, err := resolveDevCommand(framework, runCmd, srcDir)
	if err != nil {
		return err
	}
	cctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(cctx, bin, args...)
	cmd.Dir = srcDir
	cmd.Stdout = logOut
	cmd.Stderr = logOut
	cmd.Env, err = mergeDevEnv(s.dataDir, envJSON, port, dr.ProjectID)
	if err != nil {
		cancel()
		return err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if strings.TrimSpace(runCmd) != "" {
		fmt.Fprintf(logOut, "+ %s (cwd=%s, port=%d)\n", strings.TrimSpace(runCmd), srcDir, port)
	} else {
		fmt.Fprintf(logOut, "+ %s %s (cwd=%s, port=%d)\n", bin, strings.Join(args, " "), srcDir, port)
	}
	if err := cmd.Start(); err != nil {
		cancel()
		return fmt.Errorf("exec %s: %w", bin, err)
	}

	stop := make(chan struct{})
	p := &devProcess{
		DevRunID: dr.ID, Port: port, cmd: cmd, cancel: cancel,
		logFile: logF, stopCh: stop,
	}
	s.put(p)

	// Update the row with the actual pid as soon as we have it.
	_ = dbUpdateDevRun(ctx.AppDB(), dr.ID, map[string]any{
		"pid": cmd.Process.Pid,
	})

	// Supervisor goroutine — waits for the process, demotes the row.
	go func() {
		defer close(stop)
		err := cmd.Wait()
		exit := -1
		if cmd.ProcessState != nil {
			exit = cmd.ProcessState.ExitCode()
		}
		fmt.Fprintf(logOut, "=== exited at %s (exit=%d, err=%v) ===\n",
			time.Now().UTC().Format(time.RFC3339), exit, err)
		_ = logF.Close()
		if cctx.Err() != nil {
			s.markStopped(ctx, dr.ID)
		} else {
			msg := fmt.Sprintf("exit %d", exit)
			if err != nil {
				msg = err.Error()
			}
			s.markCrashed(ctx, dr.ID, msg)
		}
		s.drop(dr.ID)
	}()

	// Tiny readiness probe — TCP-connect to the port for up to 5s.
	// Once the listener appears we mark the row 'live'. Doesn't block
	// the caller; the panel polls status via /api/repos/<slug>/dev/status.
	go func() {
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
			if err == nil {
				_ = conn.Close()
				_ = dbUpdateDevRun(ctx.AppDB(), dr.ID, map[string]any{"status": "live"})
				return
			}
			time.Sleep(150 * time.Millisecond)
		}
		// Probe timed out, but the process might still come up — leave
		// status='starting' so the panel can show "starting…" and the
		// supervisor goroutine will eventually flip it to crashed if
		// the process exits, or stay starting if it actually listens
		// later.
	}()

	return nil
}

// resolveDevCommand picks (bin, args) for a release. run_cmd override
// always wins; otherwise the framework's default. Run_cmd goes through
// `sh -c` so users can write multi-token commands without quoting.
func resolveDevCommand(framework, runCmd, srcDir string) (string, []string, error) {
	if rc := strings.TrimSpace(runCmd); rc != "" {
		return "sh", []string{"-c", rc}, nil
	}
	fw := devFrameworkByName(framework)
	if fw == nil {
		return "", nil, fmt.Errorf("unknown framework: %q", framework)
	}
	if fw.Command == nil {
		return "", nil, fmt.Errorf("framework %q has no default dev command — pass run_cmd", framework)
	}
	return fw.Command(srcDir)
}

func mergeDevEnv(dataDir, envJSON string, port int, projectID string) ([]string, error) {
	out, err := commandEnvironment(dataDir, envJSON)
	if err != nil {
		return nil, err
	}
	out = append(out, fmt.Sprintf("PORT=%d", port))
	out = append(out, "NODE_ENV=development")
	if projectID != "" {
		out = append(out, "APTEVA_PROJECT_ID="+projectID)
	}
	return out, nil
}

// stopDevRun terminates the supervised process / static server, marks
// the row stopped, frees the in-memory handle. Idempotent.
//
// Remote runs (runner=simulator) have no local process — instead we
// ask the Simulator app to shut the sim down. See stopRemoteRun.
func (s *devSupervisor) stopDevRun(ctx *sdk.AppCtx, projectID string, repoID int64) error {
	dr, err := dbGetDevRun(ctx.AppDB(), projectID, repoID)
	if err != nil || dr == nil {
		return nil
	}
	if dr.Runner == "simulator" {
		s.stopRemoteRun(ctx, dr)
		_ = dbUpdateDevRun(ctx.AppDB(), dr.ID, map[string]any{
			"status":     "stopped",
			"stopped_at": time.Now().UTC().Format(time.RFC3339),
		})
		return nil
	}
	p := s.get(dr.ID)
	if p != nil {
		s.shutdownProcess(p)
		s.drop(dr.ID)
	}
	_ = dbUpdateDevRun(ctx.AppDB(), dr.ID, map[string]any{
		"status":     "stopped",
		"stopped_at": time.Now().UTC().Format(time.RFC3339),
	})
	return nil
}

func (s *devSupervisor) shutdownProcess(p *devProcess) {
	if p.server != nil {
		c, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = p.server.Shutdown(c)
		<-p.stopCh
		return
	}
	if p.cancel != nil {
		// Send SIGTERM to the process group first; CommandContext's
		// cancel sends SIGKILL after Wait returns. The graceful step
		// matters because next dev wants to flush its compile cache
		// on shutdown.
		if p.cmd != nil && p.cmd.Process != nil {
			_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGTERM)
		}
		select {
		case <-p.stopCh:
		case <-time.After(5 * time.Second):
			if p.cmd != nil && p.cmd.Process != nil {
				_ = syscall.Kill(-p.cmd.Process.Pid, syscall.SIGKILL)
			}
			<-p.stopCh
		}
	}
}

func (s *devSupervisor) markCrashed(ctx *sdk.AppCtx, devRunID int64, msg string) {
	_ = dbUpdateDevRun(ctx.AppDB(), devRunID, map[string]any{
		"status":     "crashed",
		"stopped_at": time.Now().UTC().Format(time.RFC3339),
		"error":      msg,
	})
}

func (s *devSupervisor) markStopped(ctx *sdk.AppCtx, devRunID int64) {
	_ = dbUpdateDevRun(ctx.AppDB(), devRunID, map[string]any{
		"status":     "stopped",
		"stopped_at": time.Now().UTC().Format(time.RFC3339),
	})
}

// ─── Boot reconciliation ──────────────────────────────────────────

// reconcileOrphanDevRuns runs at OnMount. Rows in starting|live whose
// pid is no longer a live process get demoted to stopped. The sidecar
// is the parent process group leader, so under normal restarts every
// child died with us — the reconcile pass just brings DB state in
// line with reality. Same shape as deploy's reconcileOrphanReleases.
func (s *devSupervisor) reconcileOrphanDevRuns(ctx *sdk.AppCtx) error {
	rows, err := dbListLiveDevRuns(ctx.AppDB())
	if err != nil {
		return err
	}
	for _, dr := range rows {
		if dr.PID > 0 && processAlive(dr.PID) {
			continue
		}
		_ = dbUpdateDevRun(ctx.AppDB(), dr.ID, map[string]any{
			"status":     "stopped",
			"stopped_at": time.Now().UTC().Format(time.RFC3339),
			"error":      "supervisor restarted; dev run marked stopped on cold boot",
		})
	}
	return nil
}

// processAlive checks /proc-style "kill 0" liveness. Returns true if
// signal 0 succeeds (process exists and we can signal it). On macOS
// and Linux this is the canonical "is it still there?" probe.
func processAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	proc, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// stopAll is called from OnUnmount to terminate every supervised
// child before the sidecar exits. Without this the children would
// keep running as orphans (Setpgid means they don't die when the
// parent does on macOS/Linux).
func (s *devSupervisor) stopAll() {
	for _, p := range s.all_() {
		s.shutdownProcess(p)
	}
}

// ─── Port allocator ───────────────────────────────────────────────

// devPortMu serialises probes; the dev_runs.port column is the
// durable claim across restarts (used by the reconciler when an
// orphan still has a port in use, though in practice the OS frees
// it as soon as the holding process exits).
var devPortMu sync.Mutex

// allocateDevPort picks the first free port in the configured range.
// Wildcard probe — same hardening as deploy v0.3.2 — so a foreign
// listener on 0.0.0.0:p or [::]:p can't sneak past a 127.0.0.1-only
// check and crash next dev with EADDRINUSE.
func (s *devSupervisor) allocateDevPort() (int, error) {
	devPortMu.Lock()
	defer devPortMu.Unlock()
	for p := s.portRangeStart; p <= s.portRangeEnd; p++ {
		if devPortFree(p) {
			return p, nil
		}
	}
	return 0, fmt.Errorf("no free port in %d-%d", s.portRangeStart, s.portRangeEnd)
}

// devPortFree mirrors deploy's portFreeForServer: bind both IPv4
// (0.0.0.0) and IPv6 ([::]) wildcards. If either fails the port can't
// be trusted for a real server.
func devPortFree(p int) bool {
	for _, addr := range []string{
		fmt.Sprintf("0.0.0.0:%d", p),
		fmt.Sprintf("[::]:%d", p),
	} {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return false
		}
		_ = ln.Close()
	}
	return true
}

// ─── Layout helpers ───────────────────────────────────────────────

func (s *devSupervisor) logPathForRepo(repoID int64) string {
	return filepath.Join(s.dataDir, "dev-logs", fmt.Sprintf("%d.log", repoID))
}

// tailFile returns up to `lines` lines from the end of a log file.
// For a streaming-friendly read, mmap-style would be better but
// these logs are bounded in practice and dev runs are short-lived;
// reading the whole file and slicing the tail is fine.
func tailFile(path string, lines int) (string, error) {
	if lines <= 0 {
		lines = 200
	}
	if lines > 2000 {
		lines = 2000
	}
	f, err := os.Open(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", nil
		}
		return "", err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return "", err
	}
	const maxTailBytes int64 = 1 << 20
	start := info.Size() - maxTailBytes
	if start < 0 {
		start = 0
	}
	body := make([]byte, info.Size()-start)
	if _, err := f.ReadAt(body, start); err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	s := string(body)
	if start > 0 {
		if newline := strings.IndexByte(s, '\n'); newline >= 0 {
			s = s[newline+1:]
		}
	}
	// Walk from the end counting newlines.
	count := 0
	for i := len(s) - 1; i >= 0; i-- {
		if s[i] == '\n' {
			count++
			if count > lines {
				return s[i+1:], nil
			}
		}
	}
	return s, nil
}
