package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

type codemagicLogTransport func(*http.Request) (*http.Response, error)

func (f codemagicLogTransport) RoundTrip(r *http.Request) (*http.Response, error) { return f(r) }

func TestCodemagicLogsIncludeFailedScriptAndDeduplicate(t *testing.T) {
	calls := 0
	client := &http.Client{Transport: codemagicLogTransport(func(r *http.Request) (*http.Response, error) {
		calls++
		if r.Header.Get("x-auth-token") != "provider-secret" {
			t.Fatal("missing authentication")
		}
		body := `{"build":{"buildActions":[{"name":"Build mobile artifact","logUrl":"https://api.codemagic.io/builds/job/step/failed","subactions":[{"logUrl":"https://api.codemagic.io/builds/job/step/failed"}]}]}}`
		if r.URL.Path != "/builds/job" {
			body = "token provider-secret\nline 14: xcodegen: command not found\nStep 4 script exited with status code 127\n"
		}
		return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(body))}, nil
	})}
	log, err := fetchCodemagicLogs(t.Context(), client, "provider-secret", "job", 200)
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{"Build mobile artifact", "xcodegen: command not found", "status code 127", "[REDACTED]"} {
		if !strings.Contains(log, text) {
			t.Fatalf("missing %q in %s", text, log)
		}
	}
	if calls != 2 || strings.Contains(log, "provider-secret") {
		t.Fatalf("calls=%d log=%s", calls, log)
	}
}

func TestCodemagicLogsRejectUnexpectedURLsBeforeSendingCredentials(t *testing.T) {
	for _, location := range []string{"https://attacker.test/builds/job/step/1", "http://api.codemagic.io/builds/job/step/1", "https://api.codemagic.io/builds/other/step/1", "//attacker.test/builds/job/step/1"} {
		t.Run(location, func(t *testing.T) {
			calls := 0
			client := &http.Client{Transport: codemagicLogTransport(func(r *http.Request) (*http.Response, error) {
				calls++
				raw, _ := json.Marshal(map[string]any{"build": map[string]any{"buildActions": []any{map[string]any{"name": "Build", "subactions": []any{map[string]any{"logUrl": location}}}}}})
				return &http.Response{StatusCode: 200, Body: io.NopCloser(strings.NewReader(string(raw)))}, nil
			})}
			log, _ := fetchCodemagicLogs(t.Context(), client, "secret", "job", 200)
			if calls != 1 || !strings.Contains(log, "unexpected log URL") {
				t.Fatalf("calls=%d log=%s", calls, log)
			}
		})
	}
}

func TestCodemagicLogsProviderFailuresAndBounds(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		expected   string
	}{
		{"expired", "", 403, "HTTP 403"},
		{"html", "<html>login</html>", 200, "invalid build details"},
		{"oversized", strings.Repeat("x", maxCodemagicLogBytes+1), 200, "exceeds 2 MiB"},
		{"no logs", `{"build":{"buildActions":[]}}`, 200, "not available yet"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			client := &http.Client{Transport: codemagicLogTransport(func(*http.Request) (*http.Response, error) {
				return &http.Response{StatusCode: tc.status, Body: io.NopCloser(strings.NewReader(tc.body))}, nil
			})}
			_, err := fetchCodemagicLogs(context.Background(), client, "secret", "job", 200)
			if err == nil || !strings.Contains(err.Error(), tc.expected) {
				t.Fatalf("err=%v", err)
			}
		})
	}
}

func TestCodemagicBuildLogCacheAndProviderFallback(t *testing.T) {
	withCloudBuildContext(t, &cloudBuildPlatform{provider: "codemagic"})
	app := &App{}
	path := filepath.Join(t.TempDir(), "build.log")
	if err := os.WriteFile(path, []byte("local summary\n"), 0600); err != nil {
		t.Fatal(err)
	}
	build := &Build{LogPath: path, BuildBackend: "codemagic", ExternalJobID: "job"}
	log, err := app.buildLog(t.Context(), build, 200)
	if err != nil || !strings.Contains(log, "local summary") || !strings.Contains(log, "Provider logs unavailable") {
		t.Fatalf("log=%s err=%v", log, err)
	}
	if err := os.WriteFile(path+".codemagic", []byte("provider line 1\nprovider line 2\n"), 0600); err != nil {
		t.Fatal(err)
	}
	log, err = app.buildLog(t.Context(), build, 2)
	if err != nil || log != "provider line 1\nprovider line 2\n" {
		t.Fatalf("log=%q err=%v", log, err)
	}
}

func TestCodemagicLogsReachMCPAndDashboardWithProjectScope(t *testing.T) {
	ctx := withCloudBuildContext(t, &cloudBuildPlatform{provider: "codemagic"})
	t.Setenv("APTEVA_PROJECT_ID", "")
	d, err := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{Name: "ios", SourceKind: "local", SourceRef: "/src"})
	if err != nil {
		t.Fatal(err)
	}
	build, err := dbCreateBuild(ctx.AppDB(), d.ID, "ios", "")
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "build.log")
	if err := os.WriteFile(path, []byte("local summary\n"), 0600); err != nil {
		t.Fatal(err)
	}
	output := "--- Codemagic: Build mobile artifact ---\nline 14: xcodegen: command not found\n"
	if err := os.WriteFile(path+".codemagic", []byte(output), 0600); err != nil {
		t.Fatal(err)
	}
	if err := dbUpdateBuild(ctx.AppDB(), build.ID, map[string]any{"log_path": path, "build_backend": "codemagic", "external_job_id": "job"}); err != nil {
		t.Fatal(err)
	}
	app := &App{}
	result, err := app.toolLogs(ctx, map[string]any{"_project_id": "p1", "build_id": float64(build.ID)})
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(result.(map[string]any)["log"].(string), "xcodegen: command not found") {
		t.Fatalf("MCP result=%v", result)
	}
	recorder := httptest.NewRecorder()
	app.handleBuildItem(recorder, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/builds/%d/log?project_id=p1", build.ID), nil))
	if recorder.Code != 200 || !strings.Contains(recorder.Body.String(), "xcodegen: command not found") {
		t.Fatalf("REST response=%d %s", recorder.Code, recorder.Body.String())
	}
	_, err = app.toolLogs(ctx, map[string]any{"_project_id": "other", "build_id": float64(build.ID)})
	if err == nil {
		t.Fatal("other project accessed provider logs")
	}
	recorder = httptest.NewRecorder()
	app.handleBuildItem(recorder, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/builds/%d/log?project_id=other", build.ID), nil))
	if recorder.Code != 404 {
		t.Fatalf("other project HTTP status=%d", recorder.Code)
	}
}

func TestCodemagicLongLogKeepsFinalFailure(t *testing.T) {
	body := strings.Repeat("compiler output\n", maxCodemagicLogBytes/10) + "xcodegen: command not found\n"
	suffix, err := readCodemagicLogTail(strings.NewReader(body))
	if err != nil || !strings.Contains(string(suffix), "Earlier provider log output omitted") || !strings.HasSuffix(string(suffix), "xcodegen: command not found\n") || len(suffix) > maxCodemagicLogBytes+100 {
		t.Fatalf("suffix bytes=%d err=%v", len(suffix), err)
	}
}
