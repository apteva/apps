package main

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

// Opt-in acceptance uses the real game and engine sources, captured in separate
// Code repositories. It verifies a real unsigned iOS archive; store signing and
// publication require the user's Deploy accounts and are reported separately.
func TestIntegration_MoonhordeSetupCodeDeploy(t *testing.T) {
	root := os.Getenv("MOONHORDE_SOURCE_ROOT")
	if testing.Short() || root == "" {
		t.Skip("set MOONHORDE_SOURCE_ROOT to the directory containing moonhorde and engine")
	}
	code := tk.SpawnSidecar(t, "../code", tk.WithProjectID("test-proj"), tk.WithEnv("CODE_REPOS_DIR", t.TempDir()), tk.WithEnv("APTEVA_BIND_HOST", "127.0.0.1"))
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/apps/callback/apps/code/call" {
			var in struct {
				Tool  string         `json:"tool"`
				Input map[string]any `json:"input"`
			}
			if e := json.NewDecoder(r.Body).Decode(&in); e != nil {
				httpErr(w, 400, e.Error())
				return
			}
			payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": in.Tool, "arguments": in.Input}})
			req, _ := http.NewRequestWithContext(r.Context(), "POST", code.URL()+"/mcp", bytes.NewReader(payload))
			req.Header.Set("Authorization", "Bearer "+code.Token())
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Apteva-Project-ID", "test-proj")
			resp, e := http.DefaultClient.Do(req)
			if e != nil {
				httpErr(w, 502, e.Error())
				return
			}
			defer resp.Body.Close()
			w.WriteHeader(resp.StatusCode)
			_, _ = io.Copy(w, resp.Body)
			return
		}
		if strings.Contains(r.URL.Path, "whoami") {
			httpJSON(w, map[string]any{"install_id": 3, "project_id": "test-proj", "bindings": map[string]any{"code": 2}})
			return
		}
		httpJSON(w, map[string]any{"ok": true, "public_url": "http://127.0.0.1"})
	}))
	defer gateway.Close()
	deploy := tk.SpawnSidecar(t, "../deploy", tk.WithProjectID("test-proj"), tk.WithEnv("DEPLOY_DATA_DIR", t.TempDir()), tk.WithEnv("USER", os.Getenv("USER")), tk.WithEnv("APTEVA_GATEWAY_URL", gateway.URL), tk.WithEnv("APTEVA_BIND_HOST", "127.0.0.1"))
	for _, name := range []string{"moonhorde", "engine"} {
		code.MCP("repos_create", map[string]any{"slug": name, "name": name, "framework": "blank"})
		archive := moonhordeSourceZip(t, filepath.Join(root, name))
		req, _ := http.NewRequest("POST", code.URL()+"/api/repos/"+name+"/import?project_id=test-proj", bytes.NewReader(archive))
		req.Header.Set("Authorization", "Bearer "+code.Token())
		req.Header.Set("Content-Type", "application/zip")
		resp, e := http.DefaultClient.Do(req)
		if e != nil {
			t.Fatal(e)
		}
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode != 200 {
			t.Fatalf("import %s: %d %s", name, resp.StatusCode, body)
		}
		t.Logf("Captured %s (%d ZIP bytes)", name, len(archive))
	}
	_, fake, _ := newStudioFixture(t)
	bridge := &studioSidecars{studioFake: fake, code: code, deploy: deploy}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("test-proj"), tk.WithPlatform(bridge))
	if e := initializeGames(ctx); e != nil {
		t.Fatal(e)
	}
	if e := initializeStudio(ctx); e != nil {
		t.Fatal(e)
	}
	created, e := gameAction(ctx, "create", map[string]any{"slug": "moonhorde", "name": "Moonhorde"})
	if e != nil {
		t.Fatal(e)
	}
	s := created.(map[string]any)["game"].(*Game).Scope()
	pin, e := studioPin(ctx, s, map[string]any{"repo_slug": "engine"})
	if e != nil {
		t.Fatal(e)
	}
	recipe := map[string]any{"id": "moonhorde-ios-validation", "version": "1", "name": "Moonhorde unsigned iOS acceptance", "platform": "desktop", "framework": "command", "os": "darwin", "dependency_path": "engine", "prepare": []any{
		map[string]any{"name": "dependencies", "command": []string{"sh", "-ec", "(cd ../engine && bun install --frozen-lockfile); bun install --frozen-lockfile"}},
		map[string]any{"name": "source-tests", "command": []string{"sh", "-ec", "bun test; bun run typecheck"}},
		map[string]any{"name": "export", "command": []string{"sh", "-ec", "export LOGNAME=\"$(id -un)\"; bun ../engine/cli/kiln.ts export ios --out generated/ios --signing external"}, "outputs": []string{"generated/ios/kiln-export.json"}},
	}, "build_directory": "generated/ios", "build_cmd": "xcodebuild -project Moonhorde.xcodeproj -scheme Moonhorde -configuration Release -sdk iphoneos -destination 'generic/platform=iOS' -archivePath \"$DEPLOY_ARTIFACT_DIR/Moonhorde.xcarchive\" -derivedDataPath \"$DEPLOY_SOURCE_DIR/DerivedData\" CODE_SIGNING_ALLOWED=NO archive", "outputs": []string{"Moonhorde.xcarchive/Products/Applications/Moonhorde.app/Info.plist"}, "tests": []any{map[string]any{"name": "ios-archive-content", "command": []string{"sh", "-ec", "test -s Moonhorde.xcarchive/Products/Applications/Moonhorde.app/Kiln/game.js; test \"$(/usr/libexec/PlistBuddy -c 'Print CFBundleIdentifier' Moonhorde.xcarchive/Products/Applications/Moonhorde.app/Info.plist)\" = com.moonhorde.game"}}}}
	if _, e = recipeSave(ctx, s, map[string]any{"recipe": recipe}); e != nil {
		t.Fatal(e)
	}
	in := map[string]any{"repo_slug": "moonhorde", "deployment_name": "moonhorde-ios-validation", "environment": "production", "platform": "desktop", "recipe_id": recipe["id"], "recipe_version": "1", "runner_backend": "local", "dependencies": []any{map[string]any{"slug": "engine", "path": "engine", "snapshot_id": object(pin)["snapshot_id"]}}}
	setup, e := studioSetup(ctx, s, map[string]any{"request_key": "moonhorde-setup", "setup": in})
	if e != nil || object(setup)["status"] != "complete" {
		t.Fatal(setup, e)
	}
	target := object(object(object(setup)["results"])["target"])
	if _, e = studioSetup(ctx, s, map[string]any{"request_key": "moonhorde-setup", "setup": in}); e != nil {
		t.Fatal(e)
	}
	receipt, e := studioDispatch(ctx, s, "build", map[string]any{"target_id": target["id"], "request_key": "moonhorde-build"})
	if e != nil {
		t.Fatal(e)
	}
	t.Log("Build requested through Games", contentJSON(receipt))
	deadline := time.Now().Add(12 * time.Minute)
	for time.Now().Before(deadline) {
		state, e := studioDeployment(ctx, s, number(target["deployment_id"]), "production")
		if e != nil {
			t.Fatal(e)
		}
		for _, b := range records(state["builds"]) {
			if b["status"] == "failed" {
				logs, _ := studioCall(ctx, s, "deploy", "deploy_logs", map[string]any{"id": target["deployment_id"], "build_id": b["id"], "tail": 100})
				t.Fatalf("stage %s: %s\n%s", failingBuildStage(b), txt(b["error"]), contentJSON(logs))
			}
			if b["status"] == "succeeded" {
				manifest := jsonObject(b["artifact_manifest_json"])
				if !strings.Contains(contentJSON(manifest), "ios-archive-content") {
					t.Fatal("missing real archive test evidence")
				}
				if pipelineDigest(object(jsonObject(b["target_config_json"])["pipeline"])) != txt(object(manifest["pipeline"])["config_sha256"]) {
					t.Fatal("pipeline evidence mismatch")
				}
				t.Logf("PASS: Moonhorde Games → Code → Deploy build %v; real unsigned iOS archive, retained sibling snapshots and artifact tests", b["id"])
				return
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatal("Moonhorde build timed out")
}
func moonhordeSourceZip(t *testing.T, root string) []byte {
	t.Helper()
	var b bytes.Buffer
	zw := zip.NewWriter(&b)
	e := filepath.WalkDir(root, func(p string, d os.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if d.IsDir() {
			switch d.Name() {
			case ".git", ".kiln", "node_modules", "dist", "target", "build", ".cache":
				return filepath.SkipDir
			}
			return nil
		}
		if d.Type()&os.ModeSymlink != 0 {
			return nil
		}
		rel, e := filepath.Rel(root, p)
		if e != nil {
			return e
		}
		info, e := d.Info()
		if e != nil {
			return e
		}
		h, e := zip.FileInfoHeader(info)
		if e != nil {
			return e
		}
		h.Name = filepath.ToSlash(rel)
		h.Method = zip.Deflate
		w, e := zw.CreateHeader(h)
		if e != nil {
			return e
		}
		f, e := os.Open(p)
		if e != nil {
			return e
		}
		_, e = io.Copy(w, f)
		f.Close()
		return e
	})
	if e != nil {
		t.Fatal(e)
	}
	if e = zw.Close(); e != nil {
		t.Fatal(e)
	}
	return b.Bytes()
}
