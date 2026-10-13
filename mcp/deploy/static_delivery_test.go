package main

import (
	"bytes"
	"compress/gzip"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/andybalholm/brotli"
)

func staticRequest(handler http.Handler, method, path, encoding string, headers map[string]string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	if encoding != "" {
		req.Header.Set("Accept-Encoding", encoding)
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)
	return w
}

func makeStaticDelivery(t *testing.T) (string, []byte) {
	t.Helper()
	root := t.TempDir()
	body := []byte(strings.Repeat("console.log('hello from the static site');\n", 1000))
	mkFile(t, filepath.Join(root, "assets", "app-a1b2c3d4.js"), string(body))
	mkFile(t, filepath.Join(root, "assets", "style-DqZz9aBx.css"), strings.Repeat("body { color: red; }\n", 500))
	mkFile(t, filepath.Join(root, "index.html"), "<!doctype html><title>release one</title>")
	mkFile(t, filepath.Join(root, "version.json"), `{"version":1}`)
	mkFile(t, filepath.Join(root, "ordinary.js"), "console.log('not hashed');")
	if err := prepareStaticDelivery(context.Background(), "static", root, "", io.Discard); err != nil {
		t.Fatal(err)
	}
	return root, body
}

func TestStaticDeliveryCompressionAndNegotiation(t *testing.T) {
	root, body := makeStaticDelivery(t)
	h := staticHandler(root)
	for _, tc := range []struct {
		accept, want string
		status       int
	}{
		{"", "", 200}, {"br, gzip", "br", 200}, {"gzip", "gzip", 200},
		{"gzip;q=0.9, br;q=0.2", "gzip", 200}, {"br;q=0, gzip;q=0.5", "gzip", 200},
		{"BR;q=1, gzip;q=0.8", "br", 200}, {"*;q=1", "br", 200},
		{"br;q=0, gzip;q=0", "", 200}, {"identity;q=1, br;q=0.5", "", 200},
		{"br;q=oops, gzip;q=0.5", "gzip", 200}, {"br;q=NaN", "", 200},
		{"*;q=0", "", 406}, {"identity;q=0, br;q=0, gzip;q=0", "", 406},
		{"deflate, identity;q=0", "", 406},
	} {
		t.Run(tc.accept, func(t *testing.T) {
			w := staticRequest(h, "GET", "/assets/app-a1b2c3d4.js", tc.accept, nil)
			if w.Code != tc.status || w.Header().Get("Content-Encoding") != tc.want {
				t.Fatalf("status=%d encoding=%q", w.Code, w.Header().Get("Content-Encoding"))
			}
			if w.Header().Get("Vary") != "Accept-Encoding" {
				t.Fatal("missing encoding variation")
			}
			if tc.status != 200 {
				return
			}
			if !strings.Contains(w.Header().Get("Content-Type"), "javascript") {
				t.Fatalf("wrong original MIME: %s", w.Header().Get("Content-Type"))
			}
			var reader io.Reader = bytes.NewReader(w.Body.Bytes())
			if tc.want == "br" {
				reader = brotli.NewReader(reader)
			}
			if tc.want == "gzip" {
				gz, err := gzip.NewReader(reader)
				if err != nil {
					t.Fatal(err)
				}
				defer gz.Close()
				reader = gz
			}
			decoded, err := io.ReadAll(reader)
			if err != nil || !bytes.Equal(decoded, body) {
				t.Fatal("decompressed bytes differ", err)
			}
			if tc.want != "" && w.Body.Len() >= len(body) {
				t.Fatal("compression did not reduce asset")
			}
		})
	}
}

func TestStaticDeliveryCachingHeadAndConditions(t *testing.T) {
	root, _ := makeStaticDelivery(t)
	h := staticHandler(root)
	for _, path := range []string{"/", "/index.html", "/version.json", "/ordinary.js", "/client/route"} {
		w := staticRequest(h, "GET", path, "br", nil)
		if w.Code != 200 || w.Header().Get("Cache-Control") != "no-cache" {
			t.Fatalf("%s: %d %s", path, w.Code, w.Header().Get("Cache-Control"))
		}
	}
	var previousETag string
	for _, encoding := range []string{"identity", "gzip", "br"} {
		get := staticRequest(h, "GET", "/assets/app-a1b2c3d4.js", encoding, nil)
		if get.Header().Get("Cache-Control") != "public, max-age=31536000, immutable" {
			t.Fatal("missing immutable cache policy")
		}
		if get.Header().Get("ETag") == "" || get.Header().Get("ETag") == previousETag {
			t.Fatal("ETag must identify the selected representation")
		}
		previousETag = get.Header().Get("ETag")
		head := staticRequest(h, "HEAD", "/assets/app-a1b2c3d4.js", encoding, nil)
		if head.Code != 200 || head.Body.Len() != 0 {
			t.Fatal("HEAD returned body")
		}
		for _, key := range []string{"Content-Type", "Content-Length", "Content-Encoding", "ETag", "Vary", "Cache-Control"} {
			if head.Header().Get(key) != get.Header().Get(key) {
				t.Fatalf("HEAD differs: %s", key)
			}
		}
		for _, condition := range []map[string]string{
			{"If-None-Match": get.Header().Get("ETag")},
			{"If-Modified-Since": get.Header().Get("Last-Modified")},
		} {
			w := staticRequest(h, "GET", "/assets/app-a1b2c3d4.js", encoding, condition)
			if w.Code != 304 || w.Body.Len() != 0 || w.Header().Get("Vary") != "Accept-Encoding" {
				t.Fatalf("bad 304: %d %v", w.Code, w.Header())
			}
		}
		w := staticRequest(h, "GET", "/assets/app-a1b2c3d4.js", encoding, map[string]string{"Range": "bytes=0-9"})
		if w.Code != 206 || w.Body.Len() != 10 {
			t.Fatalf("bad representation range: %d %d", w.Code, w.Body.Len())
		}
	}
}

func TestStaticDeliveryMissingAssetsAndTraversal(t *testing.T) {
	root, _ := makeStaticDelivery(t)
	h := staticHandler(root)
	mkFile(t, filepath.Join(root, "docs", "index.html"), "directory index")
	if w := staticRequest(h, "GET", "/docs?view=1", "", nil); w.Code != 301 || w.Header().Get("Location") != "/docs/?view=1" {
		t.Fatal("directory redirect lost relative-asset base or query")
	}
	if w := staticRequest(h, "GET", "/docs/", "", nil); w.Code != 200 || w.Body.String() != "directory index" {
		t.Fatal("directory index")
	}
	for _, method := range []string{"GET", "HEAD"} {
		for _, path := range []string{"/assets/missing-12345678.js", "/missing.css", "/version-missing.json", "/assets/no-extension", "/_next/static/chunk"} {
			w := staticRequest(h, method, path, "br", nil)
			if w.Code != 404 || strings.Contains(w.Body.String(), "release one") {
				t.Fatalf("missing asset %s became HTML", path)
			}
			if method == "HEAD" && w.Body.Len() != 0 {
				t.Fatal("missing HEAD has body")
			}
		}
	}
	outside := t.TempDir()
	mkFile(t, filepath.Join(outside, "secret.js"), "private")
	if err := os.Symlink(filepath.Join(outside, "secret.js"), filepath.Join(root, "escape.js")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(root, "ordinary.js.br")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, "secret.js"), filepath.Join(root, "ordinary.js.br")); err != nil {
		t.Fatal(err)
	}
	if w := staticRequest(h, "GET", "/escape.js", "", nil); w.Code != 404 {
		t.Fatal("served outside symlink")
	}
	if w := staticRequest(h, "GET", "/ordinary.js", "br", nil); strings.Contains(w.Body.String(), "private") || w.Header().Get("Content-Encoding") != "" {
		t.Fatal("served outside sidecar")
	}
	mkFile(t, filepath.Join(root, "404.html"), "custom missing page")
	if w := staticRequest(h, "HEAD", "/missing-route", "", nil); w.Code != 404 || w.Body.Len() != 0 {
		t.Fatal("custom 404 HEAD")
	}
	if w := staticRequest(h, "POST", "/", "", nil); w.Code != 405 {
		t.Fatal("accepted mutation method")
	}
}

func TestStaticDeliveryReleaseTransitionsAndIsolation(t *testing.T) {
	oldRoot, oldBody := makeStaticDelivery(t)
	cache := filepath.Join(t.TempDir(), "deployment-1", "production")
	now := time.Now()
	before, err := artifactTreeDigest(oldRoot)
	if err != nil {
		t.Fatal(err)
	}
	if err := retainStaticAssets(oldRoot, cache, 1, now); err != nil {
		t.Fatal(err)
	}
	newRoot := t.TempDir()
	mkFile(t, filepath.Join(newRoot, "index.html"), "new release")
	mkFile(t, filepath.Join(newRoot, "assets", "app-87654321.js"), "new chunk")
	if err := retainStaticAssets(newRoot, cache, 2, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if after, err := artifactTreeDigest(oldRoot); err != nil || before != after {
		t.Fatal("retention modified sealed artifact")
	}
	if err := os.RemoveAll(oldRoot); err != nil {
		t.Fatal(err)
	} // Simulate rollback-artifact pruning.
	h := staticHandlerWithCache(newRoot, cache)
	old := staticRequest(h, "GET", "/assets/app-a1b2c3d4.js", "br", nil)
	decoded, err := io.ReadAll(brotli.NewReader(old.Body))
	if old.Code != 200 || err != nil || !bytes.Equal(decoded, oldBody) {
		t.Fatal("old tab cannot fetch its previous chunk")
	}
	if w := staticRequest(h, "GET", "/", "", nil); w.Body.String() != "new release" {
		t.Fatal("served old HTML")
	}
	if w := staticRequest(h, "GET", "/ordinary.js", "", nil); w.Code != 404 {
		t.Fatal("retained mutable files")
	}
	isolated := staticHandlerWithCache(newRoot, filepath.Join(filepath.Dir(cache), "staging"))
	if w := staticRequest(isolated, "GET", "/assets/app-a1b2c3d4.js", "", nil); w.Code != 404 {
		t.Fatal("cross-environment asset leakage")
	}
	// Retention has a separate bounded lifecycle from build rollback retention.
	for id := int64(3); id <= 7; id++ {
		if err := retainStaticAssets(newRoot, cache, id, now.Add(time.Duration(id)*time.Second)); err != nil {
			t.Fatal(err)
		}
	}
	if len(staticAssetGenerations(cache)) != retainedStaticGenerations {
		t.Fatal("unbounded asset snapshots")
	}
	if w := staticRequest(h, "GET", "/assets/app-a1b2c3d4.js", "", nil); w.Code != 404 {
		t.Fatal("oldest snapshot not expired")
	}
	if err := retainStaticAssets(newRoot, cache, 8, now.Add(staticAssetMaxAge+time.Minute)); err != nil {
		t.Fatal(err)
	}
	if len(staticAssetGenerations(cache)) != 1 {
		t.Fatal("age retention not enforced")
	}
}

func TestStaticDeliveryPipelineEvidenceIncludesCompression(t *testing.T) {
	src, _ := makeStaticDelivery(t)
	dist := t.TempDir()
	cfg := `{"pipeline":{"outputs":["index.html"],"tests":[{"name":"compressed-assets","command":["sh","-c","test -f assets/app-a1b2c3d4.js.br && test -f assets/app-a1b2c3d4.js.gz"]}]}}`
	// Remove initial sidecars to ensure the builder's delivery phase creates them.
	for _, suffix := range []string{".br", ".gz"} {
		if err := os.Remove(filepath.Join(src, "assets", "app-a1b2c3d4.js") + suffix); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := buildWithPipeline(&staticBuilder{}, src, dist, BuildOverrides{TargetConfigJSON: cfg}, io.Discard); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dist, artifactManifestFilename))
	if err != nil {
		t.Fatal(err)
	}
	var m artifactManifest
	if err := json.Unmarshal(body, &m); err != nil {
		t.Fatal(err)
	}
	p, _ := pipelineConfig(cfg)
	if err := validatePipelineEvidence(p, dist, m); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dist, "assets", "app-a1b2c3d4.js.br"), []byte("changed"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := validatePipelineEvidence(p, dist, m); err == nil {
		t.Fatal("changed sidecar bypassed evidence")
	}
}

func TestStaticDeliveryDetectionAndDeterminism(t *testing.T) {
	for _, tc := range []struct {
		name   string
		hashed bool
	}{
		{"app-a1b2c3d4.js", true}, {"main-5c79fx2g.js", true}, {"style-DqZz9aBx.css", true},
		{"index-CC-tkX9_.js", true},
		{"12345678abcdef.js", true}, {"main.js", false}, {"vendor-components.js", false},
		{"release-2026.js", false}, {"index-a1b2c3d4.html", false}, {"version.json", false},
		{"Inter-latin-400-normal.woff2", false}, {"InterSans400Regular.woff2", false},
	} {
		if hashedStaticAsset(tc.name) != tc.hashed {
			t.Fatalf("hash classification %s", tc.name)
		}
	}
	root, _ := makeStaticDelivery(t)
	first, _ := artifactTreeDigest(root)
	if err := prepareStaticDelivery(context.Background(), "static", root, "", io.Discard); err != nil {
		t.Fatal(err)
	}
	second, _ := artifactTreeDigest(root)
	if first != second {
		t.Fatal("compression not deterministic")
	}
	for _, framework := range []string{"bun", "node"} {
		root := t.TempDir()
		out := filepath.Join(root, "dist")
		mkFile(t, filepath.Join(out, "index.html"), "static export")
		if err := prepareStaticDelivery(context.Background(), framework, root, "", io.Discard); err != nil {
			t.Fatal(err)
		}
		if !fileExists(filepath.Join(out, "index.html.br")) {
			t.Fatal("static export not compressed")
		}
	}
	root = t.TempDir()
	mkFile(t, filepath.Join(root, "dist", "index.html"), "SSR override")
	if err := prepareStaticDelivery(context.Background(), "bun", root, "bun serve.ts", io.Discard); err != nil {
		t.Fatal(err)
	}
	if fileExists(filepath.Join(root, "dist", "index.html.br")) {
		t.Fatal("overrode explicit runtime")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if err := prepareStaticDelivery(ctx, "static", root, "", io.Discard); err == nil {
		t.Fatal("ignored cancelled build")
	}
}

func TestStaticDeliveryRuntimeSeedsPreUpgradeRelease(t *testing.T) {
	oldRoot, oldBody := makeStaticDelivery(t)
	newRoot := t.TempDir()
	mkFile(t, filepath.Join(newRoot, "index.html"), "new active release")
	mkFile(t, filepath.Join(newRoot, "assets", "app-87654321.js"), "new asset")
	runtime := NewLocalRuntime(t.TempDir(), &App{})
	port := freePortForTest(t)
	running, err := runtime.Start(ReleaseSpec{
		ReleaseID: 2, DeploymentID: 7, EnvironmentID: 11,
		Framework: "static", ArtifactDir: newRoot, Port: port,
		PreviousStaticRoot: oldRoot, PreviousReleaseID: 1,
	})
	if err != nil {
		t.Fatal(err)
	}
	defer runtime.Stop(running)
	if got := httpGet(t, port, "/"); got != "new active release" {
		t.Fatal("wrong active HTML")
	}
	if err := os.RemoveAll(oldRoot); err != nil {
		t.Fatal(err)
	}
	if got := httpGet(t, port, "/assets/app-a1b2c3d4.js"); got != string(oldBody) {
		t.Fatal("first post-upgrade release lost previous assets")
	}
	cache := filepath.Join(runtime.dataDir, "static-assets", "7", "11")
	if len(staticAssetGenerations(cache)) != 2 {
		t.Fatal("runtime did not scope or seed asset cache")
	}
}
