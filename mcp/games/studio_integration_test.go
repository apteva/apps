package main

import (
	"bytes"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

type studioSidecars struct {
	*studioFake
	code, deploy *tk.Sidecar
}

func (f *studioSidecars) CallAppResult(app, tool string, in map[string]any, out any) error {
	var sidecar *tk.Sidecar
	switch app {
	case "code":
		sidecar = f.code
	case "deploy":
		sidecar = f.deploy
	default:
		return f.studioFake.CallAppResult(app, tool, in, out)
	}
	result, e := sidecar.MCPRaw("tools/call", map[string]any{"name": tool, "arguments": in})
	if e != nil {
		return e
	}
	if result["isError"] == true {
		return fmt.Errorf("sidecar tool error: %v", result)
	}
	b, e := json.Marshal(result)
	if e != nil {
		return e
	}
	return json.Unmarshal(b, out)
}

// Real source capture/chunk transfer, command pipeline and artifact tests. The
// only substitute is the platform callback gateway; no store account is used.
func TestIntegration_StudioCodeDeploy(t *testing.T) {
	if testing.Short() {
		t.Skip("real Code/Deploy sidecars")
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
			response, e := http.DefaultClient.Do(req)
			if e != nil {
				httpErr(w, 502, e.Error())
				return
			}
			defer response.Body.Close()
			w.WriteHeader(response.StatusCode)
			_, _ = io.Copy(w, response.Body)
			return
		}
		if strings.Contains(r.URL.Path, "whoami") {
			httpJSON(w, map[string]any{"install_id": 3, "project_id": "test-proj", "bindings": map[string]any{"code": 2}})
			return
		}
		if strings.Contains(r.URL.Path, "platform-info") {
			httpJSON(w, map[string]any{"public_url": "http://127.0.0.1"})
			return
		}
		httpJSON(w, map[string]any{"ok": true})
	}))
	defer gateway.Close()
	deploy := tk.SpawnSidecar(t, "../deploy", tk.WithProjectID("test-proj"), tk.WithEnv("DEPLOY_DATA_DIR", t.TempDir()), tk.WithEnv("APTEVA_GATEWAY_URL", gateway.URL), tk.WithEnv("APTEVA_BIND_HOST", "127.0.0.1"))
	code.MCP("repos_create", map[string]any{"slug": "moon", "name": "Moon", "framework": "blank"})
	// Nine MiB of incompressible source crosses the old inline export ceiling.
	payload := make([]byte, 1<<20)
	for i := 0; i < 9; i++ {
		if _, e := rand.Read(payload); e != nil {
			t.Fatal(e)
		}
		code.MCP("code_write_file", map[string]any{"slug": "moon", "path": filepath.ToSlash(fmt.Sprintf("assets/%d.dat", i)), "content": base64.StdEncoding.EncodeToString(payload)})
	}
	code.MCP("code_write_file", map[string]any{"slug": "moon", "path": "game.txt", "content": "Moonhorde fixture"})
	pipeline := map[string]any{"pipeline": map[string]any{"outputs": []string{"game.txt"}, "tests": []any{map[string]any{"name": "artifact-content", "command": []string{"sh", "-c", "test \"$(cat game.txt)\" = 'Moonhorde fixture'"}}}}}
	config, _ := json.Marshal(pipeline)
	created := deploy.MCP("deploy_init", map[string]any{"name": "moon-desktop", "source_kind": "code", "source_ref": "moon", "target_kind": "artifact", "framework": "command", "build_cmd": "cp game.txt \"$DEPLOY_ARTIFACT_DIR/game.txt\"", "target_config_json": string(config)})
	d := object(created["deployment"])
	if number(d["id"]) <= 0 {
		t.Fatalf("deployment: %v", created)
	}
	_, fake, s := newStudioFixture(t)
	bridge := &studioSidecars{studioFake: fake, code: code, deploy: deploy}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("test-proj"), tk.WithPlatform(bridge))
	if e := initializeGames(ctx); e != nil {
		t.Fatal(e)
	}
	if e := initializeStudio(ctx); e != nil {
		t.Fatal(e)
	}
	game, e := gameAction(ctx, "create", map[string]any{"slug": "moon", "name": "Moon"})
	if e != nil {
		t.Fatal(e)
	}
	s = game.(map[string]any)["game"].(*Game).Scope()
	src, e := studioSourceSet(ctx, s, map[string]any{"repo_slug": "moon"})
	if e != nil {
		t.Fatal(e)
	}
	target, e := studioTargetSet(ctx, s, map[string]any{"source_id": src.(map[string]any)["id"], "deployment_id": d["id"], "platform": "desktop"})
	if e != nil {
		t.Fatal(e)
	}
	receipt, e := studioDispatch(ctx, s, "build", map[string]any{"target_id": target.(map[string]any)["id"], "request_key": "real-build"})
	if e != nil {
		t.Fatal(e)
	}
	if receipt.(map[string]any)["status"] != "accepted" {
		t.Fatal(receipt)
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		state, e := studioDeployment(ctx, s, number(d["id"]), "production")
		if e != nil {
			t.Fatal(e)
		}
		builds := records(state["builds"])
		if len(builds) > 0 {
			b := builds[0]
			if b["status"] == "succeeded" {
				manifest := txt(b["artifact_manifest_json"])
				if !strings.Contains(manifest, "artifact-content") {
					t.Fatal("test evidence missing", manifest)
				}
				t.Logf("Verified build %v with retained artifact and test evidence", b["id"])
				return
			}
			if b["status"] == "failed" {
				t.Fatal(b)
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("build did not finish")
}
