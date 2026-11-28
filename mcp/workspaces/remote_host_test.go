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

	sdk "github.com/apteva/app-sdk"
)

// remotePlatform places every workload on an Instances host and serves the
// preview through a fake tunnel whose local port is an httptest server.
type remotePlatform struct {
	platformStub
	instanceID  int64
	remotePort  int
	tunnelPort  int
	tunnelErr   error
	opens       int
	closes      int
	lastRunArgs map[string]any
}

func (p *remotePlatform) CallAppResult(app, tool string, in map[string]any, out any) error {
	switch tool {
	case "instance_open_tunnel":
		p.platformStub.calls = append(p.platformStub.calls, appCall{App: app, Tool: tool, Input: in})
		if p.tunnelErr != nil {
			return p.tunnelErr
		}
		p.opens++
		if in["id"] != p.instanceID || in["target_port"] != p.remotePort {
			return errors.New("tunnel to wrong target")
		}
		raw, _ := json.Marshal(map[string]any{"id": p.instanceID, "local_host": "127.0.0.1", "local_port": p.tunnelPort})
		return json.Unmarshal(raw, out)
	case "instance_close_tunnel":
		p.platformStub.calls = append(p.platformStub.calls, appCall{App: app, Tool: tool, Input: in})
		p.closes++
		return json.Unmarshal([]byte(`{"closed":true}`), out)
	}
	if err := p.platformStub.CallAppResult(app, tool, in, out); err != nil {
		return err
	}
	if tool == "containers_run" {
		p.lastRunArgs = in
	}
	if tool == "containers_get" || tool == "containers_run" {
		target := p.instanceID
		if id, ok := in["instance_id"].(int64); ok && id > 0 {
			target = id
		}
		if in["use_local"] == true {
			target = 0
		}
		raw, _ := json.Marshal(map[string]any{"workload": map[string]any{
			"id": "wrk_test", "status": p.workloadStatus, "host_id": target, "instance_id": target,
			"ports": []map[string]any{{"container_port": 3000, "host_port": p.remotePort, "bind_addr": "127.0.0.1", "protocol": "tcp"}},
		}})
		return json.Unmarshal(raw, out)
	}
	return nil
}

func remoteFixture(t *testing.T) (*App, *sdk.AppCtx, context.Context, *remotePlatform) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte("preview")) }))
	t.Cleanup(server.Close)
	port, _ := strconv.Atoi(strings.Split(server.URL, ":")[2])
	p := &remotePlatform{instanceID: 7, remotePort: 41000, tunnelPort: port}
	ctx, _ := newTestContext(t, p)
	c := sdk.WithCaller(context.Background(), &sdk.Caller{ProjectID: "p", AppName: "code", AppInstallID: 91})
	return &App{}, ctx, c, p
}

func execStarts(calls []appCall) []map[string]any {
	var out []map[string]any
	for _, call := range calls {
		if call.Tool == "containers_exec_start" {
			out = append(out, call.Input)
		}
	}
	return out
}

func TestRemoteWorkspaceRunsCommandsWithoutSession(t *testing.T) {
	a, ctx, c, p := remoteFixture(t)
	w, err := a.createWorkspace(c, ctx, map[string]any{"name": "Remote", "profile": "go", "instance_id": float64(7)}, true)
	if err != nil {
		t.Fatal(err)
	}
	if p.lastRunArgs["instance_id"] != int64(7) || p.lastRunArgs["use_local"] != nil {
		t.Fatalf("placement not passed to Containers: %v", p.lastRunArgs)
	}
	if w.InstanceID != 7 || w.HostLabel != "Instance #7" {
		t.Fatalf("host not recorded: %d %q", w.InstanceID, w.HostLabel)
	}
	actor := Actor{Kind: "app", ProjectID: "p", AppName: "code", InstallID: 91}
	if _, err = a.startCommand(c, ctx, map[string]any{"argv": []any{"go", "test", "./..."}, "working_directory": "/workspace/src"}, actor, w); err != nil {
		t.Fatal(err)
	}
	starts := execStarts(p.calls)
	if len(starts) != 1 || starts[0]["session_key"] != nil || starts[0]["working_directory"] != "/workspace/src" {
		t.Fatalf("remote command used a shell session: %v", starts)
	}
	if _, err = a.createWorkspace(c, ctx, map[string]any{"name": "Bad", "instance_id": float64(-1)}, true); err == nil {
		t.Fatal("negative instance_id accepted")
	}
}

func TestDefaultHostPlacementIsRecordedAndBackfilled(t *testing.T) {
	a, ctx, c, p := remoteFixture(t)
	p.instanceID = 9 // Containers default_host_id
	w, err := a.createWorkspace(c, ctx, map[string]any{"name": "Default", "profile": "go"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if p.lastRunArgs["instance_id"] != nil || p.lastRunArgs["use_local"] != nil {
		t.Fatalf("default placement overridden: %v", p.lastRunArgs)
	}
	if w.InstanceID != 9 {
		t.Fatalf("default host not recorded: %d", w.InstanceID)
	}
	// A pre-0.9.0 row has no host; reconcile fills it in from Containers.
	if err = updateWorkspace(ctx.AppDB(), w.ID, map[string]any{"instance_id": 0, "host_label": "Local Docker"}); err != nil {
		t.Fatal(err)
	}
	w, _ = requireWorkspace(ctx.AppDB(), "p", w.ID)
	if _, err = a.refreshWorkspace(ctx, w); err != nil {
		t.Fatal(err)
	}
	w, _ = requireWorkspace(ctx.AppDB(), "p", w.ID)
	if w.InstanceID != 9 || w.HostLabel != "Instance #9" {
		t.Fatalf("host not backfilled: %d %q", w.InstanceID, w.HostLabel)
	}
	// Preview workspaces without instance_id still stay local.
	if _, err = a.createWorkspace(c, ctx, map[string]any{"name": "Local preview", "profile": "bun", "preview_port": 3000}, true); err != nil {
		t.Fatal(err)
	}
	if p.lastRunArgs["use_local"] != true {
		t.Fatalf("preview workspace left local Docker: %v", p.lastRunArgs)
	}
}

func TestRemotePreviewUsesInstancesTunnel(t *testing.T) {
	a, ctx, c, p := remoteFixture(t)
	w, err := a.createWorkspace(c, ctx, map[string]any{"name": "Remote preview", "profile": "bun", "preview_port": 3000, "instance_id": float64(7)}, true)
	if err != nil {
		t.Fatal(err)
	}
	if p.lastRunArgs["use_local"] != nil || p.lastRunArgs["ports"] == nil {
		t.Fatalf("remote preview workspace forced local: %v", p.lastRunArgs)
	}
	out, err := a.toolPreviewStart(c, ctx, map[string]any{"workspace_id": w.ID, "argv": []string{"bun", "run", "dev"}})
	if err != nil {
		t.Fatal(err)
	}
	preview := out.(map[string]any)["preview"].(*Preview)
	want := "http://127.0.0.1:" + strconv.Itoa(p.tunnelPort) + "/"
	if preview.InstanceID != 7 || preview.HostPort != 41000 || preview.LocalURL != want || p.opens != 1 {
		t.Fatalf("preview not tunnelled: %+v opens=%d", preview, p.opens)
	}
	preview, err = a.refreshPreview(ctx, w)
	if err != nil || preview.Status != "live" || preview.LocalURL != want {
		t.Fatalf("tunnelled preview not live: %+v %v", preview, err)
	}
	// Instances restarted: the tunnel comes back on another port.
	p.tunnelErr = errors.New("instances restarting")
	preview, _ = a.refreshPreview(ctx, w)
	if preview.Status != "live" || !strings.Contains(preview.Error, "preview tunnel") {
		t.Fatalf("tunnel failure not reported: %+v", preview)
	}
	p.tunnelErr = nil
	preview, err = a.refreshPreview(ctx, w)
	if err != nil || preview.Status != "live" || preview.Error != "" {
		t.Fatalf("tunnel not reopened: %+v %v", preview, err)
	}
	if _, err = a.toolPreviewStop(c, ctx, map[string]any{"workspace_id": w.ID, "keep_workspace_running": true}); err != nil {
		t.Fatal(err)
	}
	preview, _ = getPreview(ctx.AppDB(), w.ID)
	if p.closes != 1 || preview.TunnelPort != 0 || preview.URL != "" || preview.Status != "stopped" {
		t.Fatalf("tunnel not closed on stop: %+v closes=%d", preview, p.closes)
	}
}

func TestRemotePreviewNeedsInstancesAndClosesOnDestroy(t *testing.T) {
	a, ctx, c, p := remoteFixture(t)
	w, err := a.createWorkspace(c, ctx, map[string]any{"name": "No instances", "profile": "bun", "preview_port": 3000, "instance_id": float64(7)}, true)
	if err != nil {
		t.Fatal(err)
	}
	p.tunnelErr = errors.New("app instances is not installed")
	if _, err = a.toolPreviewStart(c, ctx, map[string]any{"workspace_id": w.ID, "argv": []string{"bun", "run", "dev"}}); err == nil || !strings.Contains(err.Error(), "Instances") {
		t.Fatalf("missing Instances not reported: %v", err)
	}
	if len(execStarts(p.calls)) != 0 {
		t.Fatal("preview process started without a tunnel")
	}
	p.tunnelErr = nil
	if _, err = a.toolPreviewStart(c, ctx, map[string]any{"workspace_id": w.ID, "argv": []string{"bun", "run", "dev"}}); err != nil {
		t.Fatal(err)
	}
	actor := Actor{Kind: "app", ProjectID: "p", AppName: "code", InstallID: 91}
	if _, err = a.destroyWorkspace(ctx, actor, w); err != nil {
		t.Fatal(err)
	}
	if p.closes != 1 {
		t.Fatalf("tunnel left open after destroy: closes=%d", p.closes)
	}
}
