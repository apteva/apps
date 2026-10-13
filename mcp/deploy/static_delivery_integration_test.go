//go:build integration

package main

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/andybalholm/brotli"
	tk "github.com/apteva/app-sdk/testkit"
)

func TestSidecar_StaticDeliveryBuildReleaseTransitionDestroy(t *testing.T) {
	src, dataDir := t.TempDir(), t.TempDir()
	oldAsset := strings.Repeat("console.log('old released chunk');\n", 500)
	mkFile(t, filepath.Join(src, "index.html"), "old HTML")
	mkFile(t, filepath.Join(src, "assets", "app-1234abcd.js"), oldAsset)
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("static-delivery-test"),
		tk.WithEnv("DEPLOY_DATA_DIR", dataDir),
		tk.WithConfig(map[string]string{"port_range_start": "9600", "port_range_end": "9699"}))
	out := sc.MCP("deploy_init", map[string]any{"name": "static-delivery", "source_kind": "local", "source_ref": src, "framework": "static"})
	deployment, ok := out["deployment"].(map[string]any)
	if !ok {
		t.Fatalf("init: %v", out)
	}
	id := int64(deployment["id"].(float64))
	buildRelease := func() (map[string]any, int) {
		t.Helper()
		out := sc.MCP("deploy_build", map[string]any{"id": id, "release": true})
		out = awaitBuildResult(t, sc, id, out, true)
		build, ok := out["build"].(map[string]any)
		if !ok || build["status"] != "succeeded" {
			t.Fatalf("build: %v", out)
		}
		release, ok := out["release"].(map[string]any)
		if !ok || release["status"] != "live" {
			t.Fatalf("release: %v", out)
		}
		return build, int(release["port"].(float64))
	}
	first, _ := buildRelease()
	firstPath, _ := first["artifact_path"].(string)
	if !fileExists(filepath.Join(firstPath, "assets", "app-1234abcd.js.br")) {
		t.Fatal("real build omitted Brotli sidecar")
	}
	mkFile(t, filepath.Join(src, "index.html"), "new HTML")
	if err := os.Remove(filepath.Join(src, "assets", "app-1234abcd.js")); err != nil {
		t.Fatal(err)
	}
	mkFile(t, filepath.Join(src, "assets", "app-9876dcba.js"), "new chunk")
	_, port := buildRelease()
	if err := os.RemoveAll(firstPath); err != nil {
		t.Fatal(err)
	}
	base := "http://127.0.0.1:" + strconv.Itoa(port)
	client := &http.Client{Transport: &http.Transport{DisableCompression: true}}
	defer client.CloseIdleConnections()
	req, _ := http.NewRequest("GET", base+"/assets/app-1234abcd.js", nil)
	req.Header.Set("Accept-Encoding", "br")
	resp, err := client.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := io.ReadAll(brotli.NewReader(resp.Body))
	resp.Body.Close()
	if err != nil || resp.StatusCode != 200 || resp.Header.Get("Content-Encoding") != "br" || string(decoded) != oldAsset {
		t.Fatalf("old tab delivery: status=%d error=%v", resp.StatusCode, err)
	}
	resp, err = client.Get(base + "/")
	if err != nil {
		t.Fatal(err)
	}
	html, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	if string(html) != "new HTML" || resp.Header.Get("Cache-Control") != "no-cache" {
		t.Fatal("did not serve fresh active HTML")
	}
	resp, err = client.Get(base + "/assets/missing.js")
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 404 {
		t.Fatal("missing chunk became SPA HTML")
	}
	out = sc.MCP("deploy_destroy", map[string]any{"id": id})
	if out["destroyed"] != true {
		t.Fatalf("destroy: %v", out)
	}
	if _, err := os.Stat(filepath.Join(dataDir, "static-assets", strconv.FormatInt(id, 10))); !os.IsNotExist(err) {
		t.Fatal("destroy left asset-cache data")
	}
}
