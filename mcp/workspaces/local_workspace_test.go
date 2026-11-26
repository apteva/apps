package main

import (
	"database/sql"
	"os"
	"path/filepath"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func localWorkspaceFixture(t *testing.T, enabled bool) (*App, *sdk.AppCtx, *platformStub) {
	t.Helper()
	kind := detectSandbox()
	if kind == sandboxNone {
		t.Skip("no OS sandbox on this machine")
	}
	localRuntime.mu.Lock()
	localRuntime.driver = newLocalDriver(t.TempDir(), kind, "open")
	localRuntime.mu.Unlock()
	t.Cleanup(func() {
		localRuntime.mu.Lock()
		localRuntime.driver = nil
		localRuntime.mu.Unlock()
	})
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "workspaces.db"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	paths, _ := filepath.Glob("migrations/*.sql")
	for _, path := range paths {
		raw, _ := os.ReadFile(path)
		if _, err := db.Exec(string(raw)); err != nil {
			t.Fatalf("apply %s: %v", path, err)
		}
	}
	platform := &platformStub{}
	app := &App{}
	manifest := app.Manifest()
	ctx := sdk.NewAppCtxForTest(&manifest, db, sdk.Config{
		"default_profile": "go", "default_ttl_minutes": "120", "max_ttl_minutes": "480",
		"local_driver_enabled": map[bool]string{true: "true", false: "false"}[enabled],
	}, platform, nil)
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	return app, ctx, platform
}

func TestLocalDriverIsOffUntilEnabled(t *testing.T) {
	a, ctx, _ := localWorkspaceFixture(t, false)
	_, err := a.createWorkspace(agentContext("p", 5, "t1", "tc1"), ctx, map[string]any{"name": "laptop", "driver": "local"}, false)
	if err == nil || !strings.Contains(err.Error(), "local_driver_enabled") {
		t.Fatalf("err = %v", err)
	}
	if _, err := a.createWorkspace(agentContext("p", 5, "t1", "tc2"), ctx, map[string]any{"name": "laptop", "driver": "local", "instance_id": 3}, false); err == nil {
		t.Fatal("local with instance_id accepted")
	}
}

func TestLocalWorkspaceEndToEnd(t *testing.T) {
	a, ctx, platform := localWorkspaceFixture(t, true)
	call := agentContext("p", 5, "t1", "tc-create")
	w, err := a.createWorkspace(call, ctx, map[string]any{"name": "laptop", "driver": "local"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if w.Driver != driverLocal || w.HostLabel != "This machine (sandbox)" || w.Image != "" || !strings.HasPrefix(w.WorkloadID, localWorkloadPrefix) {
		t.Fatalf("workspace = %+v", w)
	}
	if w.Capabilities.Isolation != "os_sandbox" || w.Capabilities.Hardened || w.Capabilities.Sessions {
		t.Fatalf("capabilities = %+v", w.Capabilities)
	}

	if _, err := a.toolFileWrite(agentContext("p", 5, "t1", "tc-w"), ctx, map[string]any{"workspace_id": w.ID, "path": "src/main.txt", "content": "alpha\n"}); err != nil {
		t.Fatal(err)
	}
	if _, err := a.toolApplyEdits(agentContext("p", 5, "t1", "tc-e"), ctx, map[string]any{"workspace_id": w.ID, "path": "src/main.txt", "edits": []any{map[string]any{"old_string": "alpha", "new_string": "beta"}}}); err != nil {
		t.Fatal(err)
	}
	read, err := a.toolFileRead(agentContext("p", 5, "t1", "tc-r"), ctx, map[string]any{"workspace_id": w.ID, "path": "src/main.txt"})
	if err != nil || !strings.Contains(mustJSON(read), "beta") {
		t.Fatalf("read = %v, %v", mustJSON(read), err)
	}
	listed, err := a.toolFileList(agentContext("p", 5, "t1", "tc-l"), ctx, map[string]any{"workspace_id": w.ID})
	if err != nil || !strings.Contains(mustJSON(listed), `"src/main.txt"`) {
		t.Fatalf("list = %v, %v", mustJSON(listed), err)
	}

	started, err := a.toolCommandStart(agentContext("p", 5, "t1", "tc-c"), ctx, map[string]any{"workspace_id": w.ID, "shell_command": "cat src/main.txt && pwd", "working_directory": "/workspace"})
	if err != nil {
		t.Fatal(err)
	}
	commandID := started.(map[string]any)["command"].(*Command).ID
	var final map[string]any
	for i := 0; i < 20; i++ {
		got, err := a.toolCommandWait(agentContext("p", 5, "t1", "tc-wait"), ctx, map[string]any{"workspace_id": w.ID, "command_id": commandID, "wait_ms": 2000})
		if err != nil {
			t.Fatal(err)
		}
		final = got.(map[string]any)
		if c, ok := final["command"].(*Command); ok && commandTerminal(c.Status) {
			break
		}
	}
	out := mustJSON(final)
	if !strings.Contains(out, `"succeeded"`) || !strings.Contains(out, "beta") || !strings.Contains(out, "/workspace") {
		t.Fatalf("command result = %s", out)
	}
	activity, _ := listActivity(ctx.AppDB(), "p", w.ID, 20)
	if !strings.Contains(mustJSON(activity), "Local sandbox") {
		t.Fatalf("activity actor missing: %s", mustJSON(activity))
	}

	if _, err := a.toolDestroy(agentContext("p", 5, "t1", "tc-d"), ctx, map[string]any{"workspace_id": w.ID, "confirm": true}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(localRuntime.driver.workloadDir(w.WorkloadID)); err == nil {
		t.Fatal("destroy left the folder")
	}
	for _, c := range platform.calls {
		if c.App == "containers" {
			t.Fatalf("local workspace called Containers: %s", c.Tool)
		}
	}
	rows, err := listWorkspacesFiltered(ctx.AppDB(), "p", workspaceFilter{Host: "machine", IncludeDestroyed: true})
	if err != nil || len(rows) != 1 {
		t.Fatalf("machine filter = %d, %v", len(rows), err)
	}
	if rows, _ := listWorkspacesFiltered(ctx.AppDB(), "p", workspaceFilter{Host: "local", IncludeDestroyed: true}); len(rows) != 0 {
		t.Fatalf("local Docker filter matched the local driver: %d", len(rows))
	}
}

func TestNoContainersFallsBackToLocal(t *testing.T) {
	a, ctx, platform := localWorkspaceFixture(t, true)
	platform.containersGone = true
	w, err := a.createWorkspace(agentContext("p", 5, "t1", "tc-fb"), ctx, map[string]any{"name": "fallback"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if w.Driver != driverLocal {
		t.Fatalf("driver = %q, want local", w.Driver)
	}
	_, err = a.createWorkspace(agentContext("p", 5, "t1", "tc-dk"), ctx, map[string]any{"name": "docker", "driver": "containers"}, false)
	if err == nil || !strings.Contains(err.Error(), "need the Containers app") || !strings.Contains(err.Error(), "driver local") {
		t.Fatalf("explicit containers err = %v", err)
	}
}

func TestNoContainersWithLocalOffExplains(t *testing.T) {
	a, ctx, platform := localWorkspaceFixture(t, false)
	platform.containersGone = true
	_, err := a.createWorkspace(agentContext("p", 5, "t1", "tc-off"), ctx, map[string]any{"name": "docker"}, false)
	if err == nil || !strings.Contains(err.Error(), "need the Containers app") || !strings.Contains(err.Error(), "local_driver_enabled") {
		t.Fatalf("err = %v", err)
	}
}

func TestContainersPresentKeepsDockerDefault(t *testing.T) {
	a, ctx, _ := localWorkspaceFixture(t, true)
	w, err := a.createWorkspace(agentContext("p", 5, "t1", "tc-dd"), ctx, map[string]any{"name": "docker", "profile": "go"}, false)
	if err != nil {
		t.Fatal(err)
	}
	if w.Driver != driverContainers {
		t.Fatalf("driver = %q, want containers", w.Driver)
	}
}

func TestLocalDriverOnByDefault(t *testing.T) {
	if detectSandbox() == sandboxNone {
		t.Skip("no OS sandbox on this machine")
	}
	localRuntime.mu.Lock()
	localRuntime.driver = newLocalDriver(t.TempDir(), detectSandbox(), "open")
	localRuntime.mu.Unlock()
	t.Cleanup(func() {
		localRuntime.mu.Lock()
		localRuntime.driver = nil
		localRuntime.mu.Unlock()
	})
	app := &App{}
	manifest := app.Manifest()
	platform := &platformStub{containersGone: true}
	ctx := sdk.NewAppCtxForTest(&manifest, nil, sdk.Config{}, platform, nil)
	if err := localDriverAvailable(ctx); err != nil {
		t.Fatalf("local driver should be on without config: %v", err)
	}
	if got := defaultDriver(ctx); got != driverLocal {
		t.Fatalf("default without Containers = %q, want local", got)
	}
	platform.containersGone = false
	if got := defaultDriver(ctx); got != driverContainers {
		t.Fatalf("default with Containers = %q, want containers", got)
	}
}
