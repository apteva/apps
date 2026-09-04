package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	_ "modernc.org/sqlite"
)

type appCall struct {
	App   string
	Tool  string
	Input map[string]any
}

type platformStub struct {
	tk.BasePlatformClient
	calls           []appCall
	workloadStatus  string
	executionStatus string
	exitCode        *int
	volumeArchive   string
}

func (p *platformStub) CallAppResult(appName, tool string, input map[string]any, out any) error {
	p.calls = append(p.calls, appCall{App: appName, Tool: tool, Input: input})
	if p.workloadStatus == "" {
		p.workloadStatus = "running"
	}
	if p.executionStatus == "" {
		p.executionStatus = "running"
	}
	var response any
	switch tool {
	case "containers_run":
		response = map[string]any{"workload": map[string]any{
			"id": "wrk_test", "name": input["name"], "image": input["image"],
			"status": "running", "health_status": "healthy",
			"resources": input["resources"], "working_directory": "/workspace",
			"volumes": input["volumes"],
		}}
	case "containers_get", "containers_start", "containers_stop":
		if tool == "containers_start" {
			p.workloadStatus = "running"
		}
		if tool == "containers_stop" {
			p.workloadStatus = "stopped"
		}
		response = map[string]any{"workload": map[string]any{
			"id": "wrk_test", "status": p.workloadStatus, "health_status": p.workloadStatus,
			"image": "golang:1.25-bookworm", "resources": map[string]any{"cpu": 2, "memory_mb": 4096},
			"volumes": []map[string]any{{"name": "workspace", "mount_path": "/workspace"}, {"name": "cache", "mount_path": "/cache"}},
		}}
	case "containers_destroy":
		p.workloadStatus = "destroyed"
		response = map[string]any{"destroyed": true, "workload_id": input["workload_id"]}
	case "containers_exec_start":
		response = map[string]any{"execution_id": "exe_test", "status": "running", "execution": map[string]any{
			"id": "exe_test", "workload_id": "wrk_test", "status": "running",
			"argv": input["argv"], "working_directory": input["working_directory"],
			"started_at": nowUTC(), "updated_at": nowUTC(),
		}}
	case "containers_exec_get":
		response = map[string]any{"execution": map[string]any{
			"id": "exe_test", "workload_id": "wrk_test", "status": p.executionStatus,
			"exit_code": p.exitCode, "started_at": nowUTC(), "finished_at": terminalTime(p.executionStatus),
			"updated_at": nowUTC(), "output_bytes": 12,
		}}
	case "containers_exec_cancel":
		p.executionStatus = "cancelled"
		response = map[string]any{"cancelled": true, "execution": map[string]any{
			"id": "exe_test", "status": "cancelled", "finished_at": nowUTC(),
		}}
	case "containers_exec_logs":
		response = map[string]any{"execution_id": "exe_test", "status": p.executionStatus, "logs": "hello\nworld", "output_bytes": 11}
	case "containers_usage_get":
		response = map[string]any{"metrics": []map[string]any{{
			"feature_key": "containers.storage.bytes", "quantity": 2048,
			"unit": "bytes", "kind": "gauge", "source": "docker_volume_total",
		}}}
	case "containers_volume_import":
		response = map[string]any{"workload_id": "wrk_test", "volume": "workspace", "compressed_bytes": 10}
	case "containers_volume_export":
		archive := p.volumeArchive
		if archive == "" {
			archive = "H4sI"
		}
		response = map[string]any{"workload_id": "wrk_test", "volume": input["volume"], "format": "tar.gz", "archive_base64": archive}
	default:
		response = map[string]any{}
	}
	raw, _ := json.Marshal(response)
	return json.Unmarshal(raw, out)
}

func terminalTime(status string) string {
	if commandTerminal(status) {
		return nowUTC()
	}
	return ""
}

func newTestContext(t *testing.T, platform sdk.PlatformClient) (*sdk.AppCtx, *sql.DB) {
	t.Helper()
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "workspaces.db"))
	if err != nil {
		t.Fatal(err)
	}
	paths, err := filepath.Glob("migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(raw)); err != nil {
			t.Fatalf("apply %s: %v", path, err)
		}
	}
	app := &App{}
	manifest := app.Manifest()
	ctx := sdk.NewAppCtxForTest(&manifest, db, sdk.Config{
		"default_profile":           "go",
		"default_ttl_minutes":       "120",
		"max_ttl_minutes":           "480",
		"expired_retention_minutes": "1440",
		"default_cpu":               "2",
		"default_memory_mb":         "4096",
	}, platform, nil)
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return ctx, db
}

func agentContext(project string, agentID int64, threadID, toolCallID string) context.Context {
	return sdk.WithCaller(context.Background(), &sdk.Caller{
		ProjectID: project, AgentID: agentID, ThreadID: threadID, ToolCallID: toolCallID,
	})
}

func TestManifestAndToolsAgree(t *testing.T) {
	app := &App{}
	manifest := app.Manifest()
	if manifest.Name != "workspaces" || manifest.Version != "0.4.0" {
		t.Fatalf("unexpected manifest identity: %s %s", manifest.Name, manifest.Version)
	}
	requiredContainers := false
	for _, dependency := range manifest.Requires.Apps {
		if dependency.Name == "containers" && !dependency.Optional && dependency.Version == ">=0.4.0" {
			requiredContainers = true
		}
	}
	if !requiredContainers {
		t.Fatal("Containers >=0.4.0 must be the required app dependency")
	}
	if len(manifest.Scopes) != 1 || string(manifest.Scopes[0]) != "project" {
		t.Fatalf("Workspaces must remain project-scoped: %+v", manifest.Scopes)
	}
	if len(manifest.Provides.UIPanels) != 1 || manifest.Provides.UIPanels[0].Entry != "/ui/WorkspacesPanel.mjs" {
		t.Fatalf("workspace panel not declared correctly: %+v", manifest.Provides.UIPanels)
	}
	if len(manifest.Provides.Skills) != 1 || manifest.Provides.Skills[0].BodyFile != "skills/how-to-use-workspaces.md" {
		t.Fatalf("workspace skill not declared correctly: %+v", manifest.Provides.Skills)
	}
	declared := map[string]bool{}
	for _, tool := range manifest.Provides.MCPTools {
		declared[tool.Name] = true
	}
	for _, tool := range app.MCPTools() {
		if !declared[tool.Name] {
			t.Errorf("handler %s missing from manifest", tool.Name)
		}
		delete(declared, tool.Name)
	}
	for name := range declared {
		t.Errorf("manifest tool %s has no handler", name)
	}
}

func TestCreateWorkspaceUsesApprovedProfileAndDurableVolumes(t *testing.T) {
	platform := &platformStub{}
	ctx, _ := newTestContext(t, platform)
	app := &App{}
	result, err := app.toolCreate(agentContext("project-a", 42, "task-184", "create-1"), ctx, map[string]any{
		"name": "Code cloud runtime", "purpose": "Develop apteva/apps", "profile": "go", "ttl_minutes": 90,
	})
	if err != nil {
		t.Fatal(err)
	}
	w := result.(map[string]any)["workspace"].(*Workspace)
	if w.LifecycleStatus != statusRunning || w.OwnerAgentID != 42 || w.OwnerThreadID != "task-184" {
		t.Fatalf("unexpected workspace: %+v", w)
	}
	if len(platform.calls) == 0 || platform.calls[0].Tool != "containers_run" {
		t.Fatalf("expected containers_run, got %+v", platform.calls)
	}
	input := platform.calls[0].Input
	if input["image"] != "golang:1.25-bookworm" || input["working_directory"] != "/workspace" {
		t.Fatalf("unexpected runtime input: %+v", input)
	}
	volumes, ok := input["volumes"].([]map[string]any)
	if !ok || len(volumes) != 2 || volumes[0]["name"] != "workspace" || volumes[1]["name"] != "cache" {
		t.Fatalf("durable volumes missing: %#v", input["volumes"])
	}

	result, err = app.toolCreate(agentContext("project-a", 42, "task-184", "create-1"), ctx, map[string]any{"name": "Different name"})
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["workspace"].(*Workspace).ID != w.ID || countCalls(platform.calls, "containers_run") != 1 {
		t.Fatal("retried tool call should return the existing workspace")
	}
}

func TestCommandLifecycleAndStopCancelsBeforeContainer(t *testing.T) {
	platform := &platformStub{}
	ctx, _ := newTestContext(t, platform)
	app := &App{}
	created, err := app.toolCreate(agentContext("project-a", 7, "worker", "create"), ctx, map[string]any{"name": "SDK upgrade"})
	if err != nil {
		t.Fatal(err)
	}
	w := created.(map[string]any)["workspace"].(*Workspace)
	started, err := app.toolCommandStart(agentContext("project-a", 7, "worker", "command-1"), ctx, map[string]any{
		"workspace_id": w.ID, "argv": []any{"go", "test", "./..."},
	})
	if err != nil {
		t.Fatal(err)
	}
	command := started.(map[string]any)["command"].(*Command)
	if command.ExecutionID != "exe_test" || command.Status != "running" {
		t.Fatalf("unexpected command: %+v", command)
	}
	commandCall := platform.calls[len(platform.calls)-1]
	if commandCall.Tool != "containers_exec_start" || commandCall.Input["session_key"] != "workspace" || commandCall.Input["working_directory"] != "" {
		t.Fatalf("workspace command did not use the persistent shell session: %+v", commandCall)
	}
	if _, err := app.toolCommandStart(agentContext("project-a", 7, "worker", "command-2"), ctx, map[string]any{
		"workspace_id": w.ID, "argv": []any{"go", "vet", "./..."},
	}); err == nil {
		t.Fatal("second active command should be rejected")
	}
	before := len(platform.calls)
	if _, err := app.toolStop(agentContext("project-a", 7, "worker", "stop"), ctx, map[string]any{"workspace_id": w.ID}); err != nil {
		t.Fatal(err)
	}
	after := platform.calls[before:]
	if len(after) < 2 || after[0].Tool != "containers_exec_cancel" || after[1].Tool != "containers_stop" {
		t.Fatalf("stop must cancel execution before stopping workload: %+v", after)
	}
	w, err = requireWorkspace(ctx.AppDB(), "project-a", w.ID)
	if err != nil || w.LifecycleStatus != statusSuspended {
		t.Fatalf("workspace not suspended: %+v, %v", w, err)
	}
}

func TestCommandCompletionLogsUsageAndDestroy(t *testing.T) {
	platform := &platformStub{}
	ctx, _ := newTestContext(t, platform)
	app := &App{}
	created, err := app.toolCreate(agentContext("project-a", 9, "main", "create"), ctx, map[string]any{"name": "Report build"})
	if err != nil {
		t.Fatal(err)
	}
	w := created.(map[string]any)["workspace"].(*Workspace)
	started, err := app.toolCommandStart(agentContext("project-a", 9, "main", "command"), ctx, map[string]any{
		"workspace_id": w.ID, "shell_command": "go test ./...",
	})
	if err != nil {
		t.Fatal(err)
	}
	c := started.(map[string]any)["command"].(*Command)
	zero := 0
	platform.executionStatus = "succeeded"
	platform.exitCode = &zero
	got, err := app.toolCommandGet(agentContext("project-a", 9, "main", "get"), ctx, map[string]any{"workspace_id": w.ID, "command_id": c.ID})
	if err != nil {
		t.Fatal(err)
	}
	c = got.(map[string]any)["command"].(*Command)
	if c.Status != "succeeded" || c.ExitCode == nil || *c.ExitCode != 0 {
		t.Fatalf("completion not reconciled: %+v", c)
	}
	logs, err := app.toolCommandLogs(agentContext("project-a", 9, "main", "logs"), ctx, map[string]any{"workspace_id": w.ID, "command_id": c.ID})
	if err != nil || logs.(map[string]any)["logs"] != "hello\nworld" {
		t.Fatalf("logs=%v err=%v", logs, err)
	}
	detail, err := app.toolGet(agentContext("project-a", 9, "main", "detail"), ctx, map[string]any{"workspace_id": w.ID})
	if err != nil {
		t.Fatal(err)
	}
	if detail.(map[string]any)["workspace"].(*Workspace).StorageBytes != 2048 {
		t.Fatalf("storage usage missing: %+v", detail)
	}
	if _, err := app.toolDestroy(agentContext("project-a", 9, "main", "destroy-no"), ctx, map[string]any{"workspace_id": w.ID, "confirm": false}); err == nil {
		t.Fatal("destroy without confirmation should fail")
	}
	if _, err := app.toolDestroy(agentContext("project-a", 9, "main", "destroy"), ctx, map[string]any{"workspace_id": w.ID, "confirm": true}); err != nil {
		t.Fatal(err)
	}
	last := platform.calls[len(platform.calls)-1]
	if last.Tool != "containers_destroy" || last.Input["delete_volumes"] != true {
		t.Fatalf("destroy must permanently delete volumes: %+v", last)
	}
}

func TestAgentOwnershipAndAppSourceBoundary(t *testing.T) {
	platform := &platformStub{}
	ctx, _ := newTestContext(t, platform)
	app := &App{}
	created, err := app.toolCreate(agentContext("project-a", 10, "owner", "create"), ctx, map[string]any{"name": "Private workspace"})
	if err != nil {
		t.Fatal(err)
	}
	w := created.(map[string]any)["workspace"].(*Workspace)
	if _, err := app.toolGet(agentContext("project-a", 11, "other", "get"), ctx, map[string]any{"workspace_id": w.ID}); err == nil {
		t.Fatal("another agent should not access an assigned workspace")
	}
	if _, err := app.toolGet(agentContext("project-a", 10, "another-thread", "get-2"), ctx, map[string]any{"workspace_id": w.ID}); err == nil {
		t.Fatal("another thread should not access a thread-owned workspace")
	}

	appCaller := sdk.WithCaller(context.Background(), &sdk.Caller{
		ProjectID: "project-a", AppInstallID: 77, AppName: "code", ToolCallID: "code-create",
	})
	result, err := app.toolCreateForResource(appCaller, ctx, map[string]any{
		"name": "Code workspace", "resource_kind": "repository", "resource_id": "repo-1",
		"source_archive_base64": "H4sI", "owner_agent_id": 12,
	})
	if err != nil {
		t.Fatal(err)
	}
	appWorkspace := result.(map[string]any)["workspace"].(*Workspace)
	if appWorkspace.ConsumerApp != "code" || appWorkspace.ConsumerInstallID != 77 || appWorkspace.OwnerAgentID != 12 {
		t.Fatalf("app ownership missing: %+v", appWorkspace)
	}
	otherApp := sdk.WithCaller(context.Background(), &sdk.Caller{ProjectID: "project-a", AppInstallID: 78, AppName: "code"})
	if _, err := app.toolSourceExport(otherApp, ctx, map[string]any{"workspace_id": appWorkspace.ID}); err == nil {
		t.Fatal("another app install should not export source")
	}
	if _, err := app.toolSourceExport(appCaller, ctx, map[string]any{"workspace_id": appWorkspace.ID}); err != nil {
		t.Fatal(err)
	}
}

func TestManagedSourceSyncDeletesOnlyOldManagedPaths(t *testing.T) {
	zero := 0
	platform := &platformStub{executionStatus: "succeeded", exitCode: &zero}
	ctx, _ := newTestContext(t, platform)
	app := &App{}
	caller := sdk.WithCaller(context.Background(), &sdk.Caller{
		ProjectID: "project-a", AppInstallID: 77, AppName: "code", ToolCallID: "create-source",
	})
	initialArchive := sourceArchiveForTest(t, map[string]string{"src/main.go": "package main", "src/removed.go": "package main"})
	created, err := app.toolCreateForResource(caller, ctx, map[string]any{
		"name": "Code source", "source_archive_base64": initialArchive, "source_digest": "revision-1",
		"source_paths": []any{"src/main.go", "src/removed.go"},
	})
	if err != nil {
		t.Fatal(err)
	}
	w := created.(map[string]any)["workspace"].(*Workspace)
	nextArchive := sourceArchiveForTest(t, map[string]string{"src/main.go": "package main", "src/new.go": "package main"})
	result, err := app.toolSourceSync(caller, ctx, map[string]any{
		"workspace_id": w.ID, "source_archive_base64": nextArchive, "source_digest": "revision-2",
		"expected_source_digest": "revision-1", "source_paths": []any{"src/main.go", "src/new.go"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.(map[string]any)["deleted_paths"] != 1 {
		t.Fatalf("unexpected sync result: %+v", result)
	}
	var removeCall *appCall
	for i := range platform.calls {
		if platform.calls[i].Tool == "containers_exec_start" {
			removeCall = &platform.calls[i]
			break
		}
	}
	if removeCall == nil {
		t.Fatal("expected stale-path removal execution")
	}
	argv, ok := removeCall.Input["argv"].([]string)
	if !ok || !containsString(argv, "/workspace/src/removed.go") || containsString(argv, "/workspace/src/main.go") {
		t.Fatalf("unsafe removal argv: %#v", removeCall.Input["argv"])
	}
	w, err = requireWorkspace(ctx.AppDB(), "project-a", w.ID)
	if err != nil || w.SourceDigest != "revision-2" || len(w.SourceManifest) != 2 {
		t.Fatalf("source revision was not stored: %+v err=%v", w, err)
	}
	beforeImports := countCalls(platform.calls, "containers_volume_import")
	if _, err := app.toolSourceSync(caller, ctx, map[string]any{
		"workspace_id": w.ID, "source_archive_base64": nextArchive, "source_digest": "revision-3",
		"expected_source_digest": "stale", "source_paths": []any{"src/main.go"},
	}); err == nil {
		t.Fatal("stale producer revision should be rejected")
	}
	if countCalls(platform.calls, "containers_volume_import") != beforeImports {
		t.Fatal("conflicting sync must not import source")
	}
}

func TestManagedSourceArchiveMustMatchDeclaredPaths(t *testing.T) {
	archive := sourceArchiveForTest(t, map[string]string{"src/main.go": "package main"})
	if err := validateSourceArchive(archive, []string{"src/other.go"}); err == nil {
		t.Fatal("mismatched source manifest should be rejected")
	}
	unsafe := sourceArchiveForTest(t, map[string]string{".git/config": "secret"})
	if err := validateSourceArchive(unsafe, []string{".git/config"}); err == nil {
		t.Fatal("Git metadata should be rejected from managed source")
	}
}

func TestManagedSourceExportUnwrapsFilteredArchive(t *testing.T) {
	inner := []byte("managed-source-tar-gzip")
	zero := 0
	platform := &platformStub{executionStatus: "succeeded", exitCode: &zero, volumeArchive: wrapArchiveForTest(t, "source.tar.gz", inner)}
	ctx, _ := newTestContext(t, platform)
	app := &App{}
	caller := sdk.WithCaller(context.Background(), &sdk.Caller{ProjectID: "project-a", AppInstallID: 77, AppName: "code"})
	created, err := app.toolCreateForResource(caller, ctx, map[string]any{"name": "Export source"})
	if err != nil {
		t.Fatal(err)
	}
	w := created.(map[string]any)["workspace"].(*Workspace)
	out, err := app.toolSourceExport(caller, ctx, map[string]any{"workspace_id": w.ID, "managed": true})
	if err != nil {
		t.Fatal(err)
	}
	encoded := out.(map[string]any)["archive_base64"].(string)
	decoded, _ := base64.StdEncoding.DecodeString(encoded)
	if !bytes.Equal(decoded, inner) {
		t.Fatalf("managed archive changed: %q", decoded)
	}
	if countCalls(platform.calls, "containers_exec_start") < 2 {
		t.Fatal("expected archive creation and cleanup executions")
	}
}

func wrapArchiveForTest(t *testing.T, name string, body []byte) string {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o600, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
		t.Fatal(err)
	}
	if _, err := tw.Write(body); err != nil {
		t.Fatal(err)
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(out.Bytes())
}

func sourceArchiveForTest(t *testing.T, files map[string]string) string {
	t.Helper()
	var out bytes.Buffer
	gz := gzip.NewWriter(&out)
	tw := tar.NewWriter(gz)
	paths := make([]string, 0, len(files))
	for name := range files {
		paths = append(paths, name)
	}
	sort.Strings(paths)
	for _, name := range paths {
		body := []byte(files[name])
		if err := tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg}); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(body); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	return base64.StdEncoding.EncodeToString(out.Bytes())
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}

func TestExpirySuspendsThenDestroysAfterRetention(t *testing.T) {
	platform := &platformStub{}
	ctx, _ := newTestContext(t, platform)
	app := &App{}
	created, err := app.toolCreate(agentContext("project-a", 5, "main", "create"), ctx, map[string]any{"name": "Expiring workspace"})
	if err != nil {
		t.Fatal(err)
	}
	w := created.(map[string]any)["workspace"].(*Workspace)
	past := time.Now().UTC().Add(-time.Minute).Format(time.RFC3339)
	if err := updateWorkspace(ctx.AppDB(), w.ID, map[string]any{"expires_at": past, "delete_at": time.Now().UTC().Add(time.Hour).Format(time.RFC3339)}); err != nil {
		t.Fatal(err)
	}
	if err := app.expire(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	w, _ = requireWorkspace(ctx.AppDB(), "project-a", w.ID)
	if w.LifecycleStatus != statusExpired {
		t.Fatalf("workspace should be expired, got %+v", w)
	}
	if err := updateWorkspace(ctx.AppDB(), w.ID, map[string]any{"delete_at": past}); err != nil {
		t.Fatal(err)
	}
	if err := app.expire(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	w, _ = requireWorkspace(ctx.AppDB(), "project-a", w.ID)
	if w.LifecycleStatus != statusDestroyed || w.DestroyedAt == "" {
		t.Fatalf("expired workspace should be destroyed after retention: %+v", w)
	}
}

func countCalls(calls []appCall, tool string) int {
	count := 0
	for _, call := range calls {
		if call.Tool == tool {
			count++
		}
	}
	return count
}
