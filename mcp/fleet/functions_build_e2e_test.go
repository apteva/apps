package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// A real Functions sidecar and fresh Go cache, with an 11-second compiler gate
// guaranteeing this remains longer than Fleet's former 10-second client cap.
func TestFleetColdGoDeploymentBeyondTenSeconds(t *testing.T) {
	binary := os.Getenv("FUNCTIONS_TEST_BINARY")
	if binary == "" {
		t.Skip("set FUNCTIONS_TEST_BINARY to a built Functions sidecar")
	}
	root, err := filepath.Abs("../functions")
	if err != nil {
		t.Fatal(err)
	}
	realGo, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	t.Cleanup(func() {
		_ = filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
			if err == nil && info.IsDir() {
				_ = os.Chmod(path, 0700)
			}
			return nil
		})
	})
	binDir := filepath.Join(dir, "bin")
	os.MkdirAll(binDir, 0700)
	quote := func(s string) string { return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'" }
	marker := filepath.Join(dir, "compiler-entered")
	script := "#!/bin/sh\nif [ \"$1\" = build ] && [ ! -e " + quote(marker) + " ]; then touch " + quote(marker) + "; sleep 11; fi\nexec " + quote(realGo) + " \"$@\"\n"
	if err := os.WriteFile(filepath.Join(binDir, "go"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	log, err := os.Create(filepath.Join(dir, "functions.log"))
	if err != nil {
		t.Fatal(err)
	}
	defer log.Close()
	cmd := exec.Command(binary)
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "PATH="+binDir+string(os.PathListSeparator)+os.Getenv("PATH"), "APTEVA_DATA_DIR="+filepath.Join(dir, "data"), "DB_PATH="+filepath.Join(dir, "app.db"), "APTEVA_MIGRATIONS_DIR="+filepath.Join(root, "migrations"), fmt.Sprintf("APTEVA_APP_PORT=%d", port), "APTEVA_APP_TOKEN=fixture-token", "APTEVA_PROJECT_ID=test", "APTEVA_INSTALL_ID=10", "APTEVA_FUNCTIONS_STDLIB_CACHE="+filepath.Join(dir, "stdlib"))
	cmd.Stdout, cmd.Stderr = log, log
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { cmd.Process.Kill(); cmd.Wait() }()
	target, _ := url.Parse(fmt.Sprintf("http://127.0.0.1:%d", port))
	client := &http.Client{Timeout: time.Second}
	ready := false
	for until := time.Now().Add(15 * time.Second); time.Now().Before(until); time.Sleep(30 * time.Millisecond) {
		r, e := client.Get(target.String() + "/health")
		if e == nil {
			r.Body.Close()
			if r.StatusCode == 200 {
				ready = true
				break
			}
		}
	}
	if !ready {
		b, _ := os.ReadFile(log.Name())
		t.Fatalf("sidecar not ready: %s", b)
	}
	proxy := httputil.NewSingleHostReverseProxy(target)
	proxy.Director = func(r *http.Request) {
		r.URL.Scheme = target.Scheme
		r.URL.Host = target.Host
		r.URL.Path = "/mcp"
		r.Header.Set("Authorization", "Bearer fixture-token")
	}
	gateway := httptest.NewServer(proxy)
	defer gateway.Close()
	app, ctx := newTestApp(t)
	id := seedTenantWithKey(t, app, gateway.URL, "tenant-fixture")
	started := time.Now()
	out, err := app.toolTenantAppCallContext(context.Background(), ctx, map[string]any{"tenant_id": id, "app": "functions", "tool": "functions_create", "project_id": "test", "arguments": map[string]any{"name": "cold-go", "runtime": "go", "source": "package main\nimport \"encoding/json\"\nfunc Handle(e json.RawMessage,c *Context)(any,error){return 42,nil}"}})
	elapsed := time.Since(started)
	if err != nil {
		b, _ := os.ReadFile(log.Name())
		t.Fatalf("deployment: %v\n%s", err, b)
	}
	raw, _ := json.Marshal(out)
	if elapsed <= 10*time.Second || !bytes.Contains(raw, []byte(`"status":"active"`)) {
		t.Fatalf("elapsed=%s result=%s", elapsed, raw)
	}
	t.Logf("real cold Go deployment through Fleet succeeded in %s (11-second controlled compiler gate, empty cache)", elapsed)
}
