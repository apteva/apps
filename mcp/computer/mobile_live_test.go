package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

// TestComputerMobileLivePhone runs an isolated Computer binary against an
// already-installed Mobile app and a physical phone. The relay authenticates
// with an operator API key; it does not test server-side install permissions.
// It never changes the installed Computer app or Mobile's configuration.
//
// COMPUTER_MOBILE_LIVE=1 COMPUTER_MOBILE_PROJECT=<project> \
// COMPUTER_MOBILE_DEVICE=<exact-device-id> COMPUTER_MOBILE_KEY_FILE=<path> \
// GOWORK=off go test -run TestComputerMobileLivePhone -v -timeout 8m .
func TestComputerMobileLivePhone(t *testing.T) {
	if os.Getenv("COMPUTER_MOBILE_LIVE") != "1" {
		t.Skip("set COMPUTER_MOBILE_LIVE=1 with an idle physical phone")
	}
	project, device := os.Getenv("COMPUTER_MOBILE_PROJECT"), os.Getenv("COMPUTER_MOBILE_DEVICE")
	if project == "" || device == "" {
		t.Fatal("COMPUTER_MOBILE_PROJECT and COMPUTER_MOBILE_DEVICE are required")
	}
	keyData, err := os.ReadFile(os.Getenv("COMPUTER_MOBILE_KEY_FILE"))
	if err != nil {
		t.Fatal("read configured API key file:", err)
	}
	key := strings.TrimSpace(string(keyData))
	base := os.Getenv("COMPUTER_MOBILE_BASE_URL")
	if base == "" {
		base = "http://127.0.0.1:5280"
	}
	parsed, err := url.Parse(base)
	if err != nil || !(parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "localhost") {
		t.Fatal("live phone test only accepts a local Apteva server")
	}
	endpoint := strings.TrimRight(base, "/") + "/api/apps/mobile/mcp?project_id=" + url.QueryEscape(project)
	client := &http.Client{Timeout: 90 * time.Second}
	var mu sync.Mutex
	mobileIDs := map[string]bool{}
	upstream := func(tool string, args map[string]any) ([]byte, int, error) {
		data, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}})
		req, err := http.NewRequest("POST", endpoint, bytes.NewReader(data))
		if err != nil {
			return nil, 0, err
		}
		req.Header.Set("Authorization", "Bearer "+key)
		req.Header.Set("Content-Type", "application/json")
		res, err := client.Do(req)
		if err != nil {
			return nil, 0, err
		}
		defer res.Body.Close()
		b, err := io.ReadAll(io.LimitReader(res.Body, 16<<20))
		return b, res.StatusCode, err
	}
	t.Cleanup(func() {
		mu.Lock()
		defer mu.Unlock()
		for id := range mobileIDs {
			_, _, _ = upstream("mobile_session", map[string]any{"action": "close", "session_id": id})
		}
	})
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/apps/callback/apps/mobile/call" {
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{}`))
			return
		}
		var call struct {
			Tool  string         `json:"tool"`
			Input map[string]any `json:"input"`
		}
		if json.NewDecoder(r.Body).Decode(&call) != nil || call.Input["_project_id"] != project {
			http.Error(w, "invalid project bridge", 403)
			return
		}
		delete(call.Input, "_project_id")
		data, status, err := upstream(call.Tool, call.Input)
		if err != nil {
			http.Error(w, "local Mobile request failed", 502)
			return
		}
		if call.Tool == "mobile_session" && call.Input["action"] == "open" {
			var envelope struct {
				Result struct {
					Content []struct {
						Text string `json:"text"`
					} `json:"content"`
				} `json:"result"`
			}
			if json.Unmarshal(data, &envelope) == nil {
				for _, content := range envelope.Result.Content {
					var v struct {
						Session struct {
							ID string `json:"id"`
						} `json:"session"`
					}
					if json.Unmarshal([]byte(content.Text), &v) == nil && v.Session.ID != "" {
						mu.Lock()
						mobileIDs[v.Session.ID] = true
						mu.Unlock()
					}
				}
			}
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write(data)
	}))
	defer gateway.Close()
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID(project), tk.WithEnv("APTEVA_GATEWAY_URL", gateway.URL))
	evidence := os.Getenv("COMPUTER_MOBILE_EVIDENCE_DIR")
	if evidence == "" {
		evidence, err = os.MkdirTemp("", "computer-mobile-live-")
		if err != nil {
			t.Fatal(err)
		}
	}
	if err = os.MkdirAll(evidence, 0700); err != nil {
		t.Fatal(err)
	}
	t.Log("evidence:", evidence)
	contextName := "phone-smoke-" + time.Now().Format("20060102-150405")
	saved := sc.MCP("computer_context_create", map[string]any{"name": contextName, "backend": "mobile", "device_id": device})
	rec, _ := saved["context"].(map[string]any)
	contextID, _ := rec["id"].(string)
	if contextID == "" {
		t.Fatalf("context not saved: %v", saved)
	}
	open := func() map[string]any {
		deadline := time.Now().Add(3 * time.Minute)
		for {
			out, err := sc.MCPRaw("tools/call", map[string]any{"name": "browser_session", "arguments": map[string]any{"action": "open", "context_id": contextID}})
			if err == nil {
				return out
			}
			if (!strings.Contains(err.Error(), "setup_in_progress") && !strings.Contains(err.Error(), "runner is still starting")) || time.Now().After(deadline) {
				t.Fatal("open phone:", err)
			}
			t.Log("native runner preparation in progress")
			time.Sleep(3 * time.Second)
		}
	}
	opened := open()
	id, _ := opened["session_id"].(string)
	if id == "" || opened["backend"] != "mobile" {
		t.Fatalf("phone session not opened: %v", opened)
	}
	t.Logf("opened %s through Mobile; screen=%vx%v", id, opened["width"], opened["height"])
	current := id
	t.Cleanup(func() {
		if current != "" {
			_, _ = sc.MCPRaw("tools/call", map[string]any{"name": "browser_session", "arguments": map[string]any{"action": "close", "session_id": current}})
		}
	})
	shot := func(name string) []byte {
		t.Helper()
		out := sc.MCP("computer_use", map[string]any{"session_id": current, "action": "screenshot"})
		image, ok := out["screenshot"].(map[string]any)
		if !ok {
			t.Fatalf("no screenshot: %v", out)
		}
		b, err := base64.StdEncoding.DecodeString(stringArg(image, "base64"))
		if err != nil || len(b) < 100 {
			t.Fatal("invalid real screenshot")
		}
		if err = os.WriteFile(filepath.Join(evidence, name+".png"), b, 0600); err != nil {
			t.Fatal(err)
		}
		return b
	}
	shot("01-initial")
	sc.MCP("computer_use", map[string]any{"session_id": current, "action": "home"})
	time.Sleep(time.Second)
	home := shot("02-home")
	sc.MCP("computer_use", map[string]any{"session_id": current, "action": "navigate", "url": "https://example.com"})
	time.Sleep(3 * time.Second)
	page := shot("03-example")
	if bytes.Equal(home, page) {
		t.Fatal("navigation did not change the screen")
	}
	sc.MCP("computer_use", map[string]any{"session_id": current, "action": "scroll", "direction": "down", "amount": 200})
	shot("04-after-scroll")
	sc.MCP("browser_session", map[string]any{"action": "close", "session_id": current})
	current = ""
	historical := sc.MCP("browser_session", map[string]any{"action": "status", "session_id": id})
	if historical["status"] != "closed" {
		t.Fatalf("session did not close: %v", historical)
	}
	reopened := open()
	current, _ = reopened["session_id"].(string)
	if current == "" || current == id || reopened["app_context_id"] != contextID {
		t.Fatalf("saved context not reused with a fresh lease: %v", reopened)
	}
	shot("05-reopened-context")
	sc.MCP("browser_session", map[string]any{"action": "close", "session_id": current})
	current = ""
	report := fmt.Sprintf("PASS: real phone %s, saved context %s, screenshot, Home, Safari navigation, scroll, close, reopen and final close.\n", device, contextID)
	if err = os.WriteFile(filepath.Join(evidence, "result.txt"), []byte(report), 0600); err != nil {
		t.Fatal(err)
	}
	t.Log(strings.TrimSpace(report))
}
