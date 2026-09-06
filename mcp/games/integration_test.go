package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// Runs real Auth and Games sidecars, real SDK HTTP authentication and real
// JWT/refresh flows. Only the platform callback transport is a local proxy.
// No production services, credentials or databases are used.
func TestIntegration_RealAuthAndGames(t *testing.T) {
	if testing.Short() {
		t.Skip("sidecar integration; run without -short")
	}
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	token := "test-" + randomID() + randomID()
	var mu sync.RWMutex
	authURL := ""
	var gateway *httptest.Server
	gateway = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/api/apps/callback/platform-info" {
			httpJSON(w, map[string]any{"public_url": gateway.URL})
			return
		}
		mu.RLock()
		auth := authURL
		mu.RUnlock()
		if r.URL.Path == "/api/apps/callback/apps/auth/call" {
			var body struct {
				Tool  string         `json:"tool"`
				Input map[string]any `json:"input"`
			}
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				httpErr(w, 400, err.Error())
				return
			}
			payload, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": body.Tool, "arguments": body.Input}})
			req, _ := http.NewRequestWithContext(r.Context(), "POST", auth+"/mcp", bytes.NewReader(payload))
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("X-Apteva-Bound-Caller-Install-ID", "1")
			req.Header.Set("X-Apteva-Bound-Caller-App-Name", "games")
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				httpErr(w, 502, err.Error())
				return
			}
			defer resp.Body.Close()
			w.WriteHeader(resp.StatusCode)
			_, _ = io.Copy(w, resp.Body)
			return
		}
		if r.URL.Path == "/api/apps/auth/refresh" {
			req, _ := http.NewRequestWithContext(r.Context(), "POST", auth+"/refresh?"+r.URL.RawQuery, r.Body)
			req.Header.Set("Content-Type", "application/json")
			resp, err := http.DefaultClient.Do(req)
			if err != nil {
				httpErr(w, 502, err.Error())
				return
			}
			defer resp.Body.Close()
			w.WriteHeader(resp.StatusCode)
			_, _ = io.Copy(w, resp.Body)
			return
		}
		if strings.HasPrefix(r.URL.Path, "/api/apps/callback/cors-origins") || r.URL.Path == "/api/app-events/internal/emit" {
			httpJSON(w, map[string]any{"registrations": []any{}, "ok": true})
			return
		}
		httpErr(w, 404, "unsupported test gateway endpoint")
	}))
	defer gateway.Close()
	start := func(name, dir string) string {
		t.Helper()
		bin := filepath.Join(tmp, name)
		build := exec.Command("go", "build", "-o", bin, ".")
		build.Dir = dir
		build.Env = append(os.Environ(), "GOWORK=off")
		if output, err := build.CombinedOutput(); err != nil {
			t.Fatalf("build %s: %v %s", name, err, output)
		}
		listener, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			t.Fatal(err)
		}
		port := listener.Addr().(*net.TCPAddr).Port
		listener.Close()
		url := fmt.Sprintf("http://127.0.0.1:%d", port)
		log, err := os.Create(filepath.Join(tmp, name+".log"))
		if err != nil {
			t.Fatal(err)
		}
		cmd := exec.Command(bin)
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "APTEVA_APP_TOKEN="+token, "APTEVA_OUTBOUND_TOKEN="+token, "APTEVA_GATEWAY_URL="+gateway.URL, "APTEVA_PROJECT_ID=integration", "APTEVA_BIND_HOST=127.0.0.1", fmt.Sprintf("APTEVA_APP_PORT=%d", port), "DB_PATH="+filepath.Join(tmp, name+".db"), "APTEVA_APP_CONFIG={\"analytics_enabled\":\"false\"}", "APTEVA_MIGRATIONS_DIR="+filepath.Join(dir, "migrations"))
		cmd.Stdout = log
		cmd.Stderr = log
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Signal(os.Interrupt); _ = cmd.Wait(); _ = log.Close() })
		client := &http.Client{Timeout: time.Second}
		deadline := time.Now().Add(15 * time.Second)
		for time.Now().Before(deadline) {
			resp, err := client.Get(url + "/health")
			if err == nil {
				resp.Body.Close()
				if resp.StatusCode == 200 {
					return url
				}
			}
			time.Sleep(20 * time.Millisecond)
		}
		data, _ := os.ReadFile(filepath.Join(tmp, name+".log"))
		t.Fatalf("%s did not start: %s", name, data)
		return ""
	}
	url := start("auth", filepath.Join(root, "..", "auth"))
	mu.Lock()
	authURL = url
	mu.Unlock()
	gamesURL := start("games", root)
	request := func(method, path string, body any, bearerToken string) (int, map[string]any) {
		t.Helper()
		payload, _ := json.Marshal(body)
		req, _ := http.NewRequest(method, gamesURL+path, bytes.NewReader(payload))
		req.Header.Set("Content-Type", "application/json")
		if bearerToken != "" {
			req.Header.Set("Authorization", "Bearer "+bearerToken)
		}
		resp, err := (&http.Client{Timeout: 10 * time.Second}).Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var out map[string]any
		raw, _ := io.ReadAll(resp.Body)
		if err = json.Unmarshal(raw, &out); err != nil {
			if resp.StatusCode < 400 {
				t.Fatalf("decode %s: %v %s", path, err, raw)
			}
			out = map[string]any{"error": string(raw)}
		}
		return resp.StatusCode, out
	}
	if code, _ := request("GET", "/admin/games", nil, ""); code != 401 {
		t.Fatalf("SDK exposed admin routes: %d", code)
	}
	code, created := request("POST", "/admin/games", map[string]any{"slug": "integration-game", "name": "Integration"}, token)
	if code != 200 {
		t.Fatalf("create: %v", created)
	}
	id := created["game"].(map[string]any)["id"].(string)
	base := "/v2/games/" + id
	code, login := request("POST", base+"/login/device", map[string]any{}, "")
	if code != 201 {
		t.Fatalf("real Auth login: %d %v", code, login)
	}
	access := login["access_token"].(string)
	if code, out := request("GET", base+"/me", nil, access); code != 200 {
		t.Fatalf("real JWT rejected: %v", out)
	}
	code, refresh := request("POST", base+"/session/refresh", map[string]any{"refresh_token": login["refresh_token"]}, "")
	if code != 200 {
		t.Fatalf("real refresh failed: %d %v", code, refresh)
	}
	if code, out := request("GET", base+"/me", nil, refresh["access_token"].(string)); code != 200 {
		t.Fatalf("refreshed token rejected: %v", out)
	}
	// Link a second random guest credential through the real Auth identity API.
	linked := randomID() + randomID()
	if code, out := request("POST", base+"/login/link", map[string]any{"device_id": linked}, access); code != 200 {
		t.Fatalf("link: %v", out)
	}
	code, again := request("POST", base+"/login/device", map[string]any{"device_id": linked}, "")
	if code != 200 || again["player"].(map[string]any)["id"] != login["player"].(map[string]any)["id"] {
		t.Fatalf("linked identity failed: %v", again)
	}
	code, ticket := request("POST", "/admin/games/"+id+"/login-ticket", map[string]any{"custom_id": "verified-external-account"}, token)
	if code != 200 {
		t.Fatalf("ticket: %v", ticket)
	}
	if code, out := request("POST", base+"/login/custom", map[string]any{"login_ticket": ticket["login_ticket"]}, ""); code != 201 {
		t.Fatalf("custom ticket login: %v", out)
	}
}
