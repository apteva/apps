package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestCallInstancesRunCommandDirectCallback(t *testing.T) {
	var sawAuth bool
	var sawTimeout bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			t.Errorf("method=%s want POST", r.Method)
		}
		if r.URL.Path != "/api/apps/callback/apps/instances/call" {
			t.Errorf("path=%s", r.URL.Path)
		}
		sawAuth = r.Header.Get("Authorization") == "Bearer app-token"
		var body struct {
			Tool  string         `json:"tool"`
			Input map[string]any `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if body.Tool != "instance_run_command" {
			t.Errorf("tool=%q", body.Tool)
		}
		if body.Input["id"].(float64) != 7 || body.Input["cmd"] != "echo ok" || body.Input["timeout_s"].(float64) != 61 {
			t.Errorf("unexpected input: %#v", body.Input)
		}
		sawTimeout = true
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","result":{"content":[{"type":"text","text":"{\"output\":\"ok\\n\",\"exit_code\":0}"}]}}`))
	}))
	defer srv.Close()

	t.Setenv("APTEVA_GATEWAY_URL", srv.URL)
	t.Setenv("APTEVA_APP_TOKEN", "app-token")
	var out struct {
		Output   string `json:"output"`
		ExitCode int    `json:"exit_code"`
		Err      string `json:"error"`
	}
	err := callInstancesRunCommand(t.Context(), 61, map[string]any{
		"id":        int64(7),
		"cmd":       "echo ok",
		"timeout_s": 61,
	}, &out)
	if err != nil {
		t.Fatalf("callInstancesRunCommand: %v", err)
	}
	if !sawAuth {
		t.Error("expected bearer auth header")
	}
	if !sawTimeout {
		t.Error("server did not see request")
	}
	if out.Output != "ok\n" || out.ExitCode != 0 || out.Err != "" {
		t.Fatalf("unexpected output: %+v", out)
	}
}

func TestCallInstancesRunCommandAllowsSlowResponsePastSDKFastTimeouts(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(120 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"content":[{"type":"text","text":"{\"output\":\"slow ok\",\"exit_code\":0}"}]}`))
	}))
	defer srv.Close()

	t.Setenv("APTEVA_GATEWAY_URL", srv.URL)
	t.Setenv("APTEVA_APP_TOKEN", "app-token")
	var out struct {
		Output   string `json:"output"`
		ExitCode int    `json:"exit_code"`
	}
	if err := callInstancesRunCommand(t.Context(), 1, map[string]any{"id": 1, "cmd": "sleep", "timeout_s": 1}, &out); err != nil {
		t.Fatalf("callInstancesRunCommand slow response: %v", err)
	}
	if out.Output != "slow ok" {
		t.Fatalf("output=%q", out.Output)
	}
}
