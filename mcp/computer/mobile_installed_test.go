package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestComputerMobileInstalled uses the installed Computer sidecar and the real
// server's app-to-app callback, grants and Mobile binding. It starts no relay or
// substitute sidecar. Run explicitly with an idle, unlocked physical phone:
//
// COMPUTER_MOBILE_INSTALLED=1 COMPUTER_MOBILE_PROJECT=<project> \
// COMPUTER_MOBILE_DEVICE=<exact-device-id> COMPUTER_MOBILE_KEY_FILE=<path> \
// GOWORK=off go test -run '^TestComputerMobileInstalled$' -v -timeout 8m .
func TestComputerMobileInstalled(t *testing.T) {
	if os.Getenv("COMPUTER_MOBILE_INSTALLED") != "1" {
		t.Skip("set COMPUTER_MOBILE_INSTALLED=1 to test installed Computer and Mobile")
	}
	project, device := os.Getenv("COMPUTER_MOBILE_PROJECT"), os.Getenv("COMPUTER_MOBILE_DEVICE")
	if project == "" || device == "" {
		t.Fatal("COMPUTER_MOBILE_PROJECT and COMPUTER_MOBILE_DEVICE are required")
	}
	base := os.Getenv("COMPUTER_MOBILE_BASE_URL")
	if base == "" {
		base = "http://127.0.0.1:5280"
	}
	parsed, err := url.Parse(base)
	if err != nil || !(parsed.Hostname() == "127.0.0.1" || parsed.Hostname() == "localhost") {
		t.Fatal("installed phone test only accepts a local Apteva server")
	}
	key, err := os.ReadFile(os.Getenv("COMPUTER_MOBILE_KEY_FILE"))
	if err != nil {
		t.Fatal("read API key file:", err)
	}
	endpoint := strings.TrimRight(base, "/") + "/api/apps/computer/mcp?project_id=" + url.QueryEscape(project)
	client := &http.Client{Timeout: 90 * time.Second}
	call := func(tool string, args map[string]any) (map[string]any, error) {
		body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": tool, "arguments": args}})
		req, err := http.NewRequest(http.MethodPost, endpoint, bytes.NewReader(body))
		if err != nil {
			return nil, err
		}
		req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(string(key)))
		req.Header.Set("Content-Type", "application/json")
		res, err := client.Do(req)
		if err != nil {
			return nil, err
		}
		defer res.Body.Close()
		var envelope struct {
			Error  any `json:"error"`
			Result struct {
				IsError bool `json:"isError"`
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
			} `json:"result"`
		}
		if err = json.NewDecoder(io.LimitReader(res.Body, 16<<20)).Decode(&envelope); err != nil {
			return nil, fmt.Errorf("%s HTTP %d: %w", tool, res.StatusCode, err)
		}
		if res.StatusCode != 200 || envelope.Error != nil || envelope.Result.IsError {
			return nil, fmt.Errorf("%s HTTP %d: %v %v", tool, res.StatusCode, envelope.Error, envelope.Result.Content)
		}
		for _, content := range envelope.Result.Content {
			var out map[string]any
			if json.Unmarshal([]byte(content.Text), &out) == nil && out != nil {
				return out, nil
			}
		}
		return nil, fmt.Errorf("%s returned no JSON result", tool)
	}
	mustCall := func(tool string, args map[string]any) map[string]any {
		t.Helper()
		out, err := call(tool, args)
		if err != nil {
			t.Fatal(err)
		}
		if out["success"] == false || out["failed"] == true {
			t.Fatalf("%s failed: %v", tool, out)
		}
		return out
	}
	evidence := os.Getenv("COMPUTER_MOBILE_EVIDENCE_DIR")
	if evidence == "" {
		evidence = t.TempDir()
	}
	if err = os.MkdirAll(evidence, 0700); err != nil {
		t.Fatal(err)
	}
	saved := mustCall("computer_context_create", map[string]any{"name": "installed-phone-" + time.Now().Format("20060102-150405"), "backend": "mobile", "device_id": device})
	rec, _ := saved["context"].(map[string]any)
	contextID, _ := rec["id"].(string)
	if contextID == "" {
		t.Fatalf("no saved context: %v", saved)
	}
	open := func() map[string]any {
		t.Helper()
		deadline := time.Now().Add(4 * time.Minute)
		for {
			out, err := call("browser_session", map[string]any{"action": "open", "context_id": contextID})
			if err != nil {
				t.Fatal(err)
			}
			if out["status"] == "starting" && out["retryable"] == true && time.Now().Before(deadline) {
				t.Log("native Mobile runner is preparing")
				time.Sleep(2 * time.Second)
				continue
			}
			if out["session_id"] == nil || out["success"] == false || out["backend"] != "mobile" {
				t.Fatalf("open failed: %v", out)
			}
			return out
		}
	}
	opened := open()
	id, _ := opened["session_id"].(string)
	current := id
	t.Cleanup(func() {
		if current != "" {
			_, err := call("browser_session", map[string]any{"action": "close", "session_id": current})
			if err != nil {
				t.Error("cleanup phone lease:", err)
			}
		}
	})
	shot := func(name string) []byte {
		t.Helper()
		out := mustCall("computer_use", map[string]any{"session_id": current, "action": "screenshot"})
		screenshot, _ := out["screenshot"].(map[string]any)
		encoded, _ := screenshot["base64"].(string)
		data, err := base64.StdEncoding.DecodeString(encoded)
		if err != nil || len(data) < 100 {
			t.Fatal("missing real screenshot")
		}
		if err = os.WriteFile(filepath.Join(evidence, name+".png"), data, 0600); err != nil {
			t.Fatal(err)
		}
		return data
	}
	shot("01-initial")
	mustCall("computer_use", map[string]any{"session_id": current, "action": "home"})
	time.Sleep(time.Second)
	home := shot("02-home")
	mustCall("computer_use", map[string]any{"session_id": current, "action": "navigate", "url": "https://example.com"})
	time.Sleep(3 * time.Second)
	if bytes.Equal(home, shot("03-example")) {
		t.Fatal("navigation did not change the screen")
	}
	mustCall("computer_use", map[string]any{"session_id": current, "action": "scroll", "direction": "down", "amount": 200})
	shot("04-scrolled")
	mustCall("browser_session", map[string]any{"action": "close", "session_id": current})
	current = ""
	if status := mustCall("browser_session", map[string]any{"action": "status", "session_id": id}); status["status"] != "closed" {
		t.Fatalf("lease not closed: %v", status)
	}
	reopened := open()
	current, _ = reopened["session_id"].(string)
	if current == "" || current == id || reopened["app_context_id"] != contextID {
		t.Fatalf("context did not reopen with a fresh lease: %v", reopened)
	}
	shot("05-reopened")
	mustCall("browser_session", map[string]any{"action": "close", "session_id": current})
	current = ""
	report := fmt.Sprintf("PASS: installed Computer -> real server app-to-app authorization -> Mobile -> %s. Screenshot, Home, navigation, scroll, close and saved context reopen passed. Context: %s. No relay.\n", device, contextID)
	if err = os.WriteFile(filepath.Join(evidence, "result.txt"), []byte(report), 0600); err != nil {
		t.Fatal(err)
	}
	t.Log(strings.TrimSpace(report))
}
