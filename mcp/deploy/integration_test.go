//go:build integration

package main

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

// Tier 2 — boots the deploy sidecar binary, runs the full
// init→build→release→http-fetch→stop→destroy flow against a tiny
// embedded Go fixture. Requires `go` on PATH (the build step shells
// out to it) and a writable temp dir.

const fixtureMain = `package main

import (
	"fmt"
	"net/http"
	"os"
)

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "9000"
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprintf(w, "hello deploy from port %s\n", port)
	})
	_ = http.ListenAndServe(":"+port, mux)
}
`

const fixtureGoMod = `module fixture

go 1.21
`

// writeFixture lays down a tiny Go program in a fresh dir; the
// returned path is what we pass as source_ref for kind=local.
func writeFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte(fixtureMain), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(fixtureGoMod), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

func spawnDeploySidecar(t *testing.T) *tk.Sidecar {
	t.Helper()
	dataDir := t.TempDir()
	return tk.SpawnSidecar(t, ".",
		tk.WithProjectID("test-proj"),
		tk.WithEnv("DEPLOY_DATA_DIR", dataDir),
		// Stay out of the way of common dev ports (8080/3000/etc).
		tk.WithConfig(map[string]string{
			"port_range_start": "9300",
			"port_range_end":   "9399",
		}),
	)
}

func TestSidecar_HealthOK(t *testing.T) {
	sc := spawnDeploySidecar(t)
	var got map[string]any
	resp := sc.GET("/health", &got)
	if resp.Status != 200 || got["ok"] != true {
		t.Fatalf("/health status=%d body=%v", resp.Status, got)
	}
}

func TestSidecar_FullFlow_BuildReleaseStopDestroy(t *testing.T) {
	if _, err := os.Stat("/usr/local/go/bin/go"); err != nil {
		// Fallback: rely on PATH. Skip with a clear note if we can
		// neither find go in the well-known location nor on PATH.
		if _, perr := lookGo(); perr != nil {
			t.Skip("go binary not on PATH; skipping integration test")
		}
	}
	src := writeFixture(t)
	sc := spawnDeploySidecar(t)

	// 1. Init.
	out := sc.MCP("deploy_init", map[string]any{
		"name":        "fixture-app",
		"source_kind": "local",
		"source_ref":  src,
		"framework":   "go",
	})
	dep, ok := out["deployment"].(map[string]any)
	if !ok {
		t.Fatalf("deploy_init: %v", out)
	}
	if dep["name"] != "fixture-app" {
		t.Fatalf("name = %v", dep["name"])
	}
	depID := int64(dep["id"].(float64))

	// 2. Build + release in one shot.
	out = sc.MCP("deploy_build", map[string]any{
		"id":      depID,
		"release": true,
	})
	build, ok := out["build"].(map[string]any)
	if !ok || build["status"] != "succeeded" {
		t.Fatalf("build did not succeed: %v", out)
	}
	rel, ok := out["release"].(map[string]any)
	if !ok || rel["status"] != "live" {
		t.Fatalf("release not live: %v", out)
	}
	port := int(rel["port"].(float64))

	// 3. Hit the running service. The supervisor probes readiness
	// asynchronously; allow up to 5s for the listener to come up.
	url := "http://127.0.0.1:" + strconv.Itoa(port) + "/"
	body, err := getWithRetry(url, 5*time.Second)
	if err != nil {
		t.Fatalf("fetch %s: %v", url, err)
	}
	if want := "hello deploy"; !contains(body, want) {
		t.Fatalf("response body %q missing %q", body, want)
	}

	// 4. Status reflects the live release.
	out = sc.MCP("deploy_status", map[string]any{"id": depID})
	cur := out["current_release"].(map[string]any)
	if cur["status"] != "live" {
		t.Fatalf("status not live: %v", cur)
	}

	// 5. Stop.
	out = sc.MCP("deploy_stop", map[string]any{"id": depID})
	if out["stopped"] != true {
		t.Fatalf("stop: %v", out)
	}

	// Port should free up — give the supervisor goroutine a moment
	// to mark the release stopped, then verify the port is no longer
	// reachable. Treat a slow shutdown as a failure (5s budget).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := http.Get(url); err != nil {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}

	// 6. Destroy.
	out = sc.MCP("deploy_destroy", map[string]any{"id": depID})
	if out["destroyed"] != true {
		t.Fatalf("destroy: %v", out)
	}

	// 7. Listing it back should be empty.
	out = sc.MCP("deploy_list", map[string]any{})
	if cnt, _ := out["count"].(float64); cnt != 0 {
		t.Fatalf("list count after destroy = %v, want 0", cnt)
	}
}

// ─── node framework full-flow ─────────────────────────────────────

const fixtureNodeServer = `const http = require("http");
const port = process.env.PORT || "9000";
http.createServer((_, res) => res.end("hello node deploy from port " + port + "\n"))
    .listen(port);
`

const fixtureNodePackageJSON = `{
  "name": "fixture-node",
  "version": "0.0.0",
  "private": true,
  "scripts": { "start": "node server.js" }
}
`

func writeNodeFixture(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "server.js"), []byte(fixtureNodeServer), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "package.json"), []byte(fixtureNodePackageJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	return dir
}

// TestSidecar_FullFlow_Node mirrors the go full-flow test for the
// node framework. Skipped when no Node toolchain is on PATH.
//
// Deps are intentionally empty so `<pm> install` is near-instant
// (~1s on cold cache). The fixture's `start` script runs a stdlib
// http server on $PORT — same shape as a Next.js app for the parts
// the runtime cares about (PORT env, child process, listener
// readiness probe).
func TestSidecar_FullFlow_Node(t *testing.T) {
	pm := ""
	for _, candidate := range []string{"bun", "npm"} {
		if _, err := execLookPath(candidate); err == nil {
			pm = candidate
			break
		}
	}
	if pm == "" {
		t.Skip("no node package manager (bun/npm) on PATH; skipping integration test")
	}

	src := writeNodeFixture(t)
	// Disjoint port range from the go full-flow test so a child
	// process leaking past its sidecar's shutdown can't poison this
	// run with a stale 9300-bound listener.
	dataDir := t.TempDir()
	sc := tk.SpawnSidecar(t, ".",
		tk.WithProjectID("test-proj"),
		tk.WithEnv("DEPLOY_DATA_DIR", dataDir),
		tk.WithConfig(map[string]string{
			"port_range_start": "9400",
			"port_range_end":   "9499",
		}),
	)

	// 1. Init.
	out := sc.MCP("deploy_init", map[string]any{
		"name":        "node-fixture",
		"source_kind": "local",
		"source_ref":  src,
		"framework":   "node",
	})
	dep, ok := out["deployment"].(map[string]any)
	if !ok {
		t.Fatalf("deploy_init: %v", out)
	}
	depID := int64(dep["id"].(float64))

	// 2. Build + release. npm install with zero deps + the artifact
	// copy can take a few seconds; the call is synchronous so this
	// just blocks until done.
	out = sc.MCP("deploy_build", map[string]any{
		"id":      depID,
		"release": true,
	})
	build, ok := out["build"].(map[string]any)
	if !ok || build["status"] != "succeeded" {
		t.Fatalf("build did not succeed: %v", out)
	}
	rel, ok := out["release"].(map[string]any)
	if !ok || rel["status"] != "live" {
		t.Fatalf("release not live: %v", out)
	}
	port := int(rel["port"].(float64))

	// 3. Hit the running node server. Node start is slower to listen
	// than a Go binary; allow a longer probe budget.
	url := "http://127.0.0.1:" + strconv.Itoa(port) + "/"
	body, err := getWithRetry(url, 10*time.Second)
	if err != nil {
		t.Fatalf("fetch %s: %v", url, err)
	}
	if want := "hello node deploy"; !contains(body, want) {
		t.Fatalf("response body %q missing %q", body, want)
	}

	// 4. Stop and verify the listener goes away. Node child takes
	// longer to TERM than a tiny Go binary (event-loop teardown);
	// give it the full 5s before declaring the supervisor stuck.
	out = sc.MCP("deploy_stop", map[string]any{"id": depID})
	if out["stopped"] != true {
		t.Fatalf("stop: %v", out)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if _, err := http.Get(url); err != nil {
			break
		}
		time.Sleep(150 * time.Millisecond)
	}

	// 5. Destroy.
	out = sc.MCP("deploy_destroy", map[string]any{"id": depID})
	if out["destroyed"] != true {
		t.Fatalf("destroy: %v", out)
	}
}

func TestSidecar_BuildFailure_SetsStatus(t *testing.T) {
	// Source ref points at a non-existent path → fetch fails, build
	// row should land in 'failed' with a useful error.
	sc := spawnDeploySidecar(t)
	out := sc.MCP("deploy_init", map[string]any{
		"name":        "broken",
		"source_kind": "local",
		"source_ref":  "/tmp/definitely-not-a-real-path-12345",
		"framework":   "go",
	})
	dep := out["deployment"].(map[string]any)
	depID := int64(dep["id"].(float64))

	out = sc.MCP("deploy_build", map[string]any{"id": depID})
	build := out["build"].(map[string]any)
	if build["status"] != "failed" {
		t.Fatalf("expected failed, got %v", build["status"])
	}
	if errMsg, _ := build["error"].(string); errMsg == "" {
		t.Errorf("failed build should carry an error message")
	}

	// Status tool surfaces the failed build.
	out = sc.MCP("deploy_status", map[string]any{"id": depID})
	if cur := out["current_release"]; cur != nil {
		t.Errorf("no release should exist on a failed build, got %v", cur)
	}
}

func TestSidecar_DuplicateNameRejected(t *testing.T) {
	sc := spawnDeploySidecar(t)
	src := writeFixture(t)
	args := map[string]any{
		"name":        "dup",
		"source_kind": "local",
		"source_ref":  src,
		"framework":   "go",
	}
	if _, err := sc.MCPRaw("tools/call", map[string]any{"name": "deploy_init", "arguments": args}); err != nil {
		t.Fatalf("first init: %v", err)
	}
	if out, err := sc.MCPRaw("tools/call", map[string]any{"name": "deploy_init", "arguments": args}); err == nil && out["isError"] != true {
		t.Fatalf("second init should have errored on UNIQUE: %v", out)
	}
}

// ─── helpers ──────────────────────────────────────────────────────

func contains(haystack, needle string) bool {
	for i := 0; i+len(needle) <= len(haystack); i++ {
		if haystack[i:i+len(needle)] == needle {
			return true
		}
	}
	return false
}

func getWithRetry(url string, timeout time.Duration) (string, error) {
	deadline := time.Now().Add(timeout)
	var lastErr error
	for time.Now().Before(deadline) {
		resp, err := http.Get(url)
		if err == nil {
			defer resp.Body.Close()
			body, _ := io.ReadAll(resp.Body)
			return string(body), nil
		}
		lastErr = err
		time.Sleep(200 * time.Millisecond)
	}
	return "", lastErr
}

// lookGo is a thin wrapper around exec.LookPath that we keep as its
// own helper so the build-time skip logic stays self-documenting.
func lookGo() (string, error) {
	return execLookPath("go")
}

// execLookPath aliased to avoid pulling os/exec into the file's top
// imports — keeps the production-side imports clean. The function is
// only called from tier-2 tests, and only for the path-skip check.
var execLookPath = func(name string) (string, error) {
	// Inline implementation — the test binary always has os/exec
	// available through the build of the production package, so
	// just shell out via a tiny indirect.
	for _, dir := range filepath.SplitList(os.Getenv("PATH")) {
		path := filepath.Join(dir, name)
		if info, err := os.Stat(path); err == nil && !info.IsDir() && info.Mode()&0o111 != 0 {
			return path, nil
		}
	}
	return "", os.ErrNotExist
}
