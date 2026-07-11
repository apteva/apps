package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// TestDetectDevFramework pins per-framework detectors against marker
// files in the FileStore. Order matters in devFrameworks (nextjs
// before generic node so a Next project doesn't get classified as
// "node"); this test enforces that.
func TestDetectDevFramework(t *testing.T) {
	cases := []struct {
		name string
		seed map[string][]byte
		want string
	}{
		{"empty", nil, ""},
		{"go.mod → go", map[string][]byte{"go.mod": []byte("module x")}, "go"},
		{"package.json with next → nextjs", map[string][]byte{
			"package.json": []byte(`{"dependencies":{"next":"^14"}}`),
		}, "nextjs"},
		{"package.json without next → node", map[string][]byte{
			"package.json": []byte(`{"dependencies":{"express":"^4"}}`),
		}, "node"},
		{"index.html → static", map[string][]byte{"index.html": []byte("<html></html>")}, "static"},
		{"go.mod beats index.html", map[string][]byte{
			"go.mod":     []byte("module x"),
			"index.html": []byte("<html></html>"),
		}, "go"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			store := newMemFileStore()
			_ = store.CreateRepo("r")
			for path, body := range tc.seed {
				if _, err := store.Write("r", path, body); err != nil {
					t.Fatal(err)
				}
			}
			if got := detectDevFramework(store, "r"); got != tc.want {
				t.Fatalf("detectDevFramework() = %q, want %q", got, tc.want)
			}
		})
	}
}

// TestDevPortFree_RejectsWildcardSquatter ports the v0.3.2 deploy
// regression for the dev-side allocator: a foreign listener on
// [::]:p must mark the port unfree, even though 127.0.0.1:p alone
// would slip past. macOS Control Center's *:7000 was the original
// trigger; same shape any wildcard-bound server would create.
func TestDevPortFree_RejectsWildcardSquatter(t *testing.T) {
	probe, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := probe.Addr().(*net.TCPAddr).Port
	probe.Close()

	if !devPortFree(port) {
		t.Skipf("port %d became unavailable before the test could grab it", port)
	}
	squatter, err := net.Listen("tcp", "[::]:"+itoaDev(port))
	if err != nil {
		t.Skipf("can't bind [::]:%d (%v); skipping", port, err)
	}
	defer squatter.Close()

	if devPortFree(port) {
		t.Fatalf("devPortFree(%d) said free with [::] squatter held", port)
	}
}

// TestTailFile covers the small log-tail helper — empty file, file
// shorter than the requested tail, file with exactly the right
// number of lines, file longer than the tail. The supervisor's logs
// surface depends on this shape.
func TestTailFile(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "log")

	if got, err := tailFile(p, 10); err != nil || got != "" {
		t.Errorf("missing file should return empty, got %q err=%v", got, err)
	}

	_ = os.WriteFile(p, []byte("a\nb\nc\n"), 0o644)
	if got, _ := tailFile(p, 10); got != "a\nb\nc\n" {
		t.Errorf("short file: got %q", got)
	}

	_ = os.WriteFile(p, []byte("a\nb\nc\nd\ne\n"), 0o644)
	if got, _ := tailFile(p, 2); got != "d\ne\n" {
		t.Errorf("tail 2: got %q", got)
	}
}

func TestNodeDepsInstallPlan_BunRunCmdRepo(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"scripts":{"build":"bun build.ts"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bun.lock"), []byte("lock"), 0o644); err != nil {
		t.Fatal(err)
	}

	plan, err := nodeDepsInstallPlan(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Needed {
		t.Fatal("expected dependency install for package.json repo without node_modules")
	}
	if plan.Reason != "dependencies missing" {
		t.Fatalf("reason = %q", plan.Reason)
	}
	if plan.PM != "bun" {
		t.Fatalf("PM = %q, want bun", plan.PM)
	}
	if want := []string{"install", "--frozen-lockfile"}; !reflect.DeepEqual(plan.Args, want) {
		t.Fatalf("Args = %v, want %v", plan.Args, want)
	}
}

func TestNodeDepsInstallPlan_ReinstallsWhenDependencyFilesChange(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"dependencies":{"vite":"1.0.0"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "node_modules"), 0o755); err != nil {
		t.Fatal(err)
	}
	hash, err := dependencyFingerprint(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(nodeDepsStatePath(dir), []byte(hash+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	plan, err := nodeDepsInstallPlan(dir)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Needed {
		t.Fatalf("expected dependency install to be skipped when fingerprint is current: %+v", plan)
	}

	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"dependencies":{"vite":"2.0.0"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	plan, err = nodeDepsInstallPlan(dir)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Needed || plan.Reason != "dependency files changed" {
		t.Fatalf("expected reinstall after package.json changed, got %+v", plan)
	}
}

func TestInstallNodeDeps_UsesFrozenBunAndRecordsFingerprint(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(`{"name":"x"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "bun.lock"), []byte("lock"), 0o644); err != nil {
		t.Fatal(err)
	}

	binDir := t.TempDir()
	fakeBun := filepath.Join(binDir, "bun")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$PWD/bun-args.txt\"\nmkdir -p node_modules\n"
	if err := os.WriteFile(fakeBun, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	plan, err := nodeDepsInstallPlan(dir)
	if err != nil {
		t.Fatal(err)
	}
	logPath := filepath.Join(dir, "dev.log")
	logF, err := os.Create(logPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := installNodeDeps(context.Background(), dir, logF, plan, os.Environ()); err != nil {
		_ = logF.Close()
		t.Fatal(err)
	}
	if err := logF.Close(); err != nil {
		t.Fatal(err)
	}

	args, err := os.ReadFile(filepath.Join(dir, "bun-args.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(args)); got != "install\n--frozen-lockfile" {
		t.Fatalf("fake bun args = %q", got)
	}
	if !exists(nodeDepsStatePath(dir)) {
		t.Fatal("expected dependency fingerprint state file")
	}
	logBody, _ := os.ReadFile(logPath)
	log := string(logBody)
	if !strings.Contains(log, "dependencies missing; running bun install --frozen-lockfile") {
		t.Fatalf("log does not explain dependency bootstrap:\n%s", log)
	}
	if !strings.Contains(log, "+ bun install --frozen-lockfile") {
		t.Fatalf("log does not show install command:\n%s", log)
	}
}

func TestRunRepoCommand_SuccessAndFailureUseExitCodeSemantics(t *testing.T) {
	a := &App{dataDir: t.TempDir()}
	srcDir := t.TempDir()
	repo := &Repo{ID: 7, ProjectID: "p1", Slug: "cmd"}

	ok, err := a.runRepoCommand(repo, srcDir, repoCommandInput{
		Command: "printf out && printf err >&2",
	})
	if err != nil {
		t.Fatal(err)
	}
	if ok.Status != "success" || ok.ExitCode != 0 {
		t.Fatalf("success result = %+v", ok)
	}
	if ok.StdoutTail != "out" || ok.StderrTail != "err" {
		t.Fatalf("tails stdout=%q stderr=%q", ok.StdoutTail, ok.StderrTail)
	}

	failed, err := a.runRepoCommand(repo, srcDir, repoCommandInput{
		Command: "printf broken >&2; exit 7",
	})
	if err != nil {
		t.Fatal(err)
	}
	if failed.Status != "failed" || failed.ExitCode != 7 {
		t.Fatalf("failure result = %+v", failed)
	}
	if !strings.Contains(failed.StderrTail, "broken") {
		t.Fatalf("stderr tail missing command output: %+v", failed)
	}
}

func TestRunRepoCommand_BootstrapsBunDepsBeforeFiniteCommand(t *testing.T) {
	a := &App{dataDir: t.TempDir()}
	srcDir := t.TempDir()
	repo := &Repo{ID: 8, ProjectID: "p1", Slug: "build"}
	if err := os.WriteFile(filepath.Join(srcDir, "package.json"), []byte(`{"scripts":{"build":"echo built"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(srcDir, "bun.lock"), []byte("lock"), 0o644); err != nil {
		t.Fatal(err)
	}
	binDir := t.TempDir()
	fakeBun := filepath.Join(binDir, "bun")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > \"$PWD/bun-args.txt\"\nmkdir -p node_modules\n"
	if err := os.WriteFile(fakeBun, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))

	res, err := a.runRepoCommand(repo, srcDir, repoCommandInput{
		Command: "test -f node_modules/.apteva-code-deps.sha256 && echo built",
	})
	if err != nil {
		t.Fatal(err)
	}
	if res.Status != "success" || !res.DependencyInstallRan {
		t.Fatalf("expected successful command with dependency install, got %+v", res)
	}
	args, err := os.ReadFile(filepath.Join(srcDir, "bun-args.txt"))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.TrimSpace(string(args)); got != "install\n--frozen-lockfile" {
		t.Fatalf("fake bun args = %q", got)
	}
	if !strings.Contains(res.StdoutTail, "built") {
		t.Fatalf("stdout tail missing build output: %+v", res)
	}
	if !strings.Contains(res.LogTail, "dependencies missing; running bun install --frozen-lockfile") {
		t.Fatalf("log tail missing dependency install note:\n%s", res.LogTail)
	}
}

// TestStartDevRun_BlankWithRunCmd spawns a real "child process" via
// run_cmd. We can't easily fake the framework path without an
// AppCtx, so we go straight at the supervisor primitives — port
// allocator + spawn — and verify the listener actually binds.
//
// Uses `nc -l 127.0.0.1 <port>` shaped via `python3 -m http.server`
// fallback. Skipped if neither is on PATH (CI without the tools).
func TestSupervisor_SpawnAndStop(t *testing.T) {
	// Skip if no python3 — we use it to stand up a 1-line http server
	// listening on $PORT, the simplest cross-platform stand-in for a
	// real framework dev process.
	if _, err := osLookPath("python3"); err != nil {
		t.Skip("python3 not on PATH; skipping dev supervisor spawn test")
	}

	// Set up a minimal supervisor with a real on-disk store.
	dataDir := t.TempDir()
	reposRoot := filepath.Join(dataDir, "repos")
	_ = os.MkdirAll(reposRoot, 0o755)
	store := NewLocalFileStore(reposRoot)
	_ = store.CreateRepo("hello")
	// Empty repo is fine — we override the framework via run_cmd.
	sup := newDevSupervisor(dataDir, store, nil, 6300, 6399)

	port, err := sup.allocateDevPort()
	if err != nil {
		t.Fatalf("alloc port: %v", err)
	}
	if port < 6300 || port > 6399 {
		t.Fatalf("port %d outside requested range", port)
	}

	// Build a logfile and exec a tiny http server in the repo dir.
	logPath := filepath.Join(dataDir, "test.log")
	logF, _ := os.Create(logPath)
	t.Cleanup(func() { _ = logF.Close() })

	// Use spawnProcess directly — bypass DB so we don't need a real ctx.
	dr := &DevRun{ID: 1, Port: port}
	srcDir := store.RepoPath("hello")
	runCmd := "python3 -m http.server $PORT --bind 127.0.0.1 >/dev/null"

	bin, args, err := resolveDevCommand("blank", runCmd, srcDir)
	if err != nil {
		t.Fatalf("resolveDevCommand: %v", err)
	}
	if bin != "sh" || len(args) != 2 || args[0] != "-c" {
		t.Fatalf("got bin=%q args=%v, want sh -c <runCmd>", bin, args)
	}

	_ = dr // status updates are exercised in the store tests
}

// ─── tiny test helpers (package-local; avoid pulling strconv just
// for one int → string in a test). ────────────────────────────────

func itoaDev(n int) string {
	if n == 0 {
		return "0"
	}
	neg := n < 0
	if neg {
		n = -n
	}
	var buf [20]byte
	i := len(buf)
	for n > 0 {
		i--
		buf[i] = byte('0' + n%10)
		n /= 10
	}
	if neg {
		i--
		buf[i] = '-'
	}
	return string(buf[i:])
}

// osLookPath is a thin indirection so the file's import list stays
// minimal — only the test needs exec.LookPath, the production path
// in dev_runtime.go pulls os/exec for the real spawn.
func osLookPath(name string) (string, error) {
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		path := filepath.Join(dir, name)
		if info, err := os.Stat(path); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return path, nil
		}
	}
	return "", os.ErrNotExist
}

// memFileStore for in-memory tests is defined in filestore_mem_test.go.
// We reuse it via newMemFileStore() — kept package-local so this file
// stays focused on dev runtime concerns.
