package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type previewPlatform struct {
	platformStub
	port       int
	failCancel bool
}

func (p *previewPlatform) CallAppResult(app, tool string, in map[string]any, out any) error {
	if tool == "containers_exec_cancel" && p.failCancel {
		return errors.New("Docker unavailable")
	}
	if err := p.platformStub.CallAppResult(app, tool, in, out); err != nil {
		return err
	}
	if tool == "containers_get" || tool == "containers_run" {
		raw, _ := json.Marshal(map[string]any{"workload": map[string]any{"id": "wrk_test", "status": p.workloadStatus, "ports": []map[string]any{{"container_port": 3000, "host_port": p.port, "bind_addr": "127.0.0.1", "protocol": "tcp"}}}})
		return json.Unmarshal(raw, out)
	}
	return nil
}
func previewFixture(t *testing.T) (*App, *sdk.AppCtx, context.Context, *Workspace, *previewPlatform) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("preview")) }))
	t.Cleanup(server.Close)
	port, _ := strconv.Atoi(strings.Split(server.URL, ":")[2])
	p := &previewPlatform{port: port}
	ctx, _ := newTestContext(t, p)
	a := &App{}
	c := sdk.WithCaller(context.Background(), &sdk.Caller{ProjectID: "p", AppName: "code", AppInstallID: 91})
	w, err := a.createWorkspace(c, ctx, map[string]any{"name": "Preview", "profile": "bun", "preview_port": 3000}, true)
	if err != nil {
		t.Fatal(err)
	}
	return a, ctx, c, w, p
}
func TestWorkspacePreviewLifecycle(t *testing.T) {
	a, ctx, c, w, p := previewFixture(t)
	_, err := a.toolPreviewStart(c, ctx, map[string]any{"workspace_id": w.ID, "argv": []string{"bun", "--bun", "run", "dev"}})
	if err != nil {
		t.Fatal(err)
	}
	var run, execution map[string]any
	for _, call := range p.calls {
		if call.Tool == "containers_run" {
			run = call.Input
		}
		if call.Tool == "containers_exec_start" {
			execution = call.Input
		}
	}
	if run["use_local"] != true || run["ports"] == nil {
		t.Fatalf("no local published ports: %v", run)
	}
	if execution["session_key"] != nil || execution["timeout_s"].(int) < 60 {
		t.Fatalf("preview used finite command PTY: %v", execution)
	}
	// New App instance simulates a Workspaces restart. Handles live in SQLite.
	a = &App{}
	preview, err := a.refreshPreview(ctx, w)
	if err != nil || preview.Status != "live" || preview.HostPort != p.port {
		t.Fatalf("readiness %v %v", preview, err)
	}
	wrong := sdk.WithCaller(context.Background(), &sdk.Caller{ProjectID: "p", AppName: "code", AppInstallID: 92})
	if _, err = a.toolPreviewLogs(wrong, ctx, map[string]any{"workspace_id": w.ID}); err == nil {
		t.Fatal("another install read preview")
	}
	p.failCancel = true
	if _, err = a.toolPreviewStop(c, ctx, map[string]any{"workspace_id": w.ID}); err == nil {
		t.Fatal("cancellation failure hidden")
	}
	preview, _ = getPreview(ctx.AppDB(), w.ID)
	if preview.Status != "live" {
		t.Fatal("lost recovery handle on stop failure")
	}
	p.failCancel = false
	if _, err = a.toolPreviewStop(c, ctx, map[string]any{"workspace_id": w.ID}); err != nil {
		t.Fatal(err)
	}
	preview, _ = getPreview(ctx.AppDB(), w.ID)
	if preview.Status != "stopped" || p.workloadStatus != "stopped" {
		t.Fatal("container still running")
	}
	for _, call := range p.calls {
		if call.Tool == "containers_destroy" {
			t.Fatal("stop destroyed source")
		}
	}
}
func TestPreviewExitAndStartupDeadline(t *testing.T) {
	a, ctx, c, w, p := previewFixture(t)
	_, err := a.toolPreviewStart(c, ctx, map[string]any{"workspace_id": w.ID, "argv": []string{"false"}})
	if err != nil {
		t.Fatal(err)
	}
	p.executionStatus = "failed"
	code := 7
	p.exitCode = &code
	preview, err := a.refreshPreview(ctx, w)
	if err != nil || preview.Status != "crashed" {
		t.Fatalf("exit not reported: %v %v", preview, err)
	}
	p.executionStatus = "running"
	preview.Status = "starting"
	preview.StartedAt = time.Now().Add(-11 * time.Minute).UTC().Format(time.RFC3339)
	preview.HostPort = 1
	savePreview(ctx.AppDB(), preview)
	preview, err = a.refreshPreview(ctx, w)
	if err != nil || preview.Status != "crashed" || p.executionStatus != "cancelled" {
		t.Fatalf("startup timeout: %v %v", preview, err)
	}
}
func TestPreviewNeedsPublishedPortAndActiveTTL(t *testing.T) {
	a, ctx, c, w, p := previewFixture(t)
	p.port = 0
	args := map[string]any{"workspace_id": w.ID, "argv": []string{"true"}}
	if _, err := a.toolPreviewStart(c, ctx, args); err == nil {
		t.Fatal("missing port accepted")
	}
	p.port = 40000
	updateWorkspace(ctx.AppDB(), w.ID, map[string]any{"expires_at": time.Now().Add(-time.Minute).UTC().Format(time.RFC3339)})
	if _, err := a.toolPreviewStart(c, ctx, args); err == nil {
		t.Fatal("expired workspace accepted")
	}
}
