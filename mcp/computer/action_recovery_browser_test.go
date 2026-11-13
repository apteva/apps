package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestComputerActionRecoveryBrowser(t *testing.T) {
	if os.Getenv("RUN_COMPUTER_APP_BROWSER_TESTS") == "" {
		t.Skip("set RUN_COMPUTER_APP_BROWSER_TESTS=1")
	}
	fixture := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.Write([]byte(`<!doctype html><title>Recovery fixture</title><h1>Read-only board</h1><section aria-label="Editor notes" role="region" style="height:120px;overflow:auto"><div style="height:800px">Do not edit these notes</div></section><div style="height:1200px"></div><button>Release details</button>`))
	}))
	defer fixture.Close()
	sidecar := tk.SpawnSidecar(t, ".", tk.WithEnv("APTEVA_HEADLESS_BROWSER", "1"), tk.WithEnv("APTEVA_INSTALL_ID", "4242"))
	opened := callComputerMCP(t, sidecar, "browser_session", map[string]any{"action": "open", "backend": "local", "url": fixture.URL})
	sid := opened["session_id"].(string)
	defer callComputerMCP(t, sidecar, "browser_session", map[string]any{"action": "close", "session_id": sid})
	shot := callComputerMCP(t, sidecar, "computer_use", map[string]any{"session_id": sid, "action": "screenshot", "include_som": true, "compact": true})
	if shot["screenshot"] == nil || shot["observation_format"] != "compact" {
		t.Fatalf("screenshot=%v", shot["error"])
	}
	failed := callComputerMCP(t, sidecar, "computer_use", map[string]any{"session_id": sid, "action": "scroll", "direction": "down", "compact": true})
	if failed["error_code"] != "ambiguous_scroll_target" || failed["action_dispatched"] != false {
		t.Fatalf("missing structured ambiguity: %+v", failed)
	}
	regions := failed["scroll_regions"].([]any)
	var documentID string
	for _, raw := range regions {
		region := raw.(map[string]any)
		if region["role"] == "document" || region["id"] == "scroll_document" {
			documentID = region["id"].(string)
		}
	}
	if documentID == "" || len(regions) < 2 {
		t.Fatalf("available regions=%+v", regions)
	}
	moved := callComputerMCP(t, sidecar, "computer_use", map[string]any{"session_id": sid, "action": "scroll", "direction": "down", "amount": 300, "target_id": documentID, "compact": true})
	if moved["scroll_moved"] != true || moved["scroll_actual_target_id"] != documentID {
		t.Fatalf("targeted scroll=%+v", moved)
	}
	if moved["screenshot"] != nil {
		t.Fatal("default semantic scroll embedded duplicate pixels")
	}
	for _, value := range []string{"som_delta", "screenshot", "bogus"} {
		rejected := callComputerMCP(t, sidecar, "computer_use", map[string]any{"session_id": sid, "action": "screenshot", "observation": value})
		if rejected["error_code"] != "invalid_observation" || rejected["action_dispatched"] != false {
			t.Fatalf("screenshot rejection=%+v", rejected)
		}
		recovery := rejected["recovery"].(map[string]any)
		if strings.Contains(recovery["next_step"].(string), "Use observation=") {
			t.Fatal("invalid recovery instruction")
		}
		args := recovery["arguments"].(map[string]any)
		retried := callComputerMCP(t, sidecar, "computer_use", args)
		if retried["screenshot"] == nil {
			t.Fatalf("suggested repair did not execute: %v", retried["error"])
		}
	}
}
