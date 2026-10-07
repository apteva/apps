package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRemoteRunScriptThroughInstancesShell(t *testing.T) {
	// The render command writes a fixture instead of encoding media. A stub
	// ffmpeg keeps bootstrap from installing anything on the test machine.
	binDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(binDir, "ffmpeg"), []byte("#!/bin/sh\nexit 0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	// macOS sh accepts some Bash syntax. Prefer dash when available to
	// reproduce the POSIX shell used by Debian/Ubuntu render hosts.
	if dash, err := exec.LookPath("dash"); err == nil {
		if err := os.Symlink(dash, filepath.Join(binDir, "sh")); err != nil {
			t.Fatal(err)
		}
	}
	t.Setenv("PATH", binDir+string(os.PathListSeparator)+os.Getenv("PATH"))
	const payload = "remote's $variable $(printf substitution) `printf backtick` \\path\nsecond line"
	const token = "test's $token `literal`"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/apps/callback/apps/instances/call":
			var request struct {
				Tool  string `json:"tool"`
				Input struct {
					ID       int64  `json:"id"`
					Cmd      string `json:"cmd"`
					TimeoutS int    `json:"timeout_s"`
				} `json:"input"`
			}
			if err := json.NewDecoder(r.Body).Decode(&request); err != nil {
				t.Error(err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if request.Tool != "instance_run_command" || request.Input.ID != 42 || request.Input.TimeoutS != 1800 {
				t.Errorf("unexpected instances request: %+v", request)
			}
			if r.Header.Get("Authorization") != "Bearer "+token {
				t.Error("instances authentication changed during transport")
			}
			ctx, cancel := context.WithTimeout(r.Context(), 10*time.Second)
			defer cancel()
			// This is the shell boundary used by Instances for local and SSH
			// commands. Passing the bare Bash script here reproduced the bug.
			cmd := exec.CommandContext(ctx, "sh", "-c", request.Input.Cmd)
			output, err := cmd.CombinedOutput()
			exitCode := 0
			if err != nil {
				exitCode = -1
				if cmd.ProcessState != nil {
					exitCode = cmd.ProcessState.ExitCode()
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"output": string(output), "exit_code": exitCode})
		case "/input":
			if r.URL.Query().Get("label") != payload {
				t.Error("input URL changed during shell transport")
			}
			_, _ = w.Write([]byte(payload))
		case composerBoundStorageProxyPath + "/files/init":
			if r.Header.Get("Authorization") != "Bearer "+token || r.URL.Query().Get("project_id") != "project-1" {
				t.Error("storage authentication or project changed during transport")
			}
			// Exercise the actual upload retry array without sending media.
			_, _ = w.Write([]byte(`{"was_existing":true,"file":{"id":123}}`))
		default:
			t.Errorf("unexpected request: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	t.Setenv("APTEVA_GATEWAY_URL", server.URL)
	t.Setenv("APTEVA_OUTBOUND_TOKEN", token)

	t.Run("generated render and upload", func(t *testing.T) {
		script := remoteRenderScript(
			[]string{server.URL + "/input?label=" + url.QueryEscape(payload)},
			"cmp ./in0 <("+shellEcho("printf", []string{"%s", payload})+")\ncp ./in0 ./out.mp4",
			"mp4", "project-1", server.URL, token, "composition.mp4", "video/mp4", nil,
		)
		output, err := remoteRunScript(context.Background(), 42, script)
		if err != nil {
			t.Fatalf("remote render through sh: %v\n%s", err, output)
		}
		id, err := parseRemoteResult(output)
		if err != nil || id != 123 {
			t.Fatalf("storage result: id=%d err=%v output=%s", id, err, output)
		}
	})
	t.Run("pipefail exit status", func(t *testing.T) {
		output, err := remoteRunScript(context.Background(), 42, "set -eu -o pipefail\nprintf 'before failure\\n'\nfalse | true\nprintf 'unreachable\\n'\n")
		if err == nil || !strings.Contains(err.Error(), "remote exit_code=1") {
			t.Fatalf("pipeline failure lost: %v\n%s", err, output)
		}
		if !strings.Contains(output, "before failure") || strings.Contains(output, "unreachable") {
			t.Fatalf("unexpected pipeline output: %s", output)
		}
	})
}
