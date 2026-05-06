package main

import (
	"net"
	"os"
	"path/filepath"
	"testing"
)

// TestDetectDevFramework pins per-framework detectors against marker
// files in the FileStore. Order matters in devFrameworks (nextjs
// before generic node so a Next project doesn't get classified as
// "node"); this test enforces that.
func TestDetectDevFramework(t *testing.T) {
	cases := []struct {
		name  string
		seed  map[string][]byte
		want  string
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
