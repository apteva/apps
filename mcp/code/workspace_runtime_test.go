package main

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type codeWorkspacePlatform struct {
	tk.BasePlatformClient
	archive string
	digest  string
	paths   []string
	calls   []string
}

func (p *codeWorkspacePlatform) CallAppResult(appName, tool string, input map[string]any, out any) error {
	p.calls = append(p.calls, appName+"/"+tool)
	var response any
	switch tool {
	case "workspace_create_for_resource":
		p.archive, _ = input["source_archive_base64"].(string)
		p.digest, _ = input["source_digest"].(string)
		p.paths = anyStrings(input["source_paths"])
		response = map[string]any{"workspace": map[string]any{"id": "wsp_code", "lifecycle_status": "running", "source_digest": p.digest, "source_paths": p.paths}}
	case "workspace_source_export":
		response = map[string]any{"workspace_id": "wsp_code", "archive_base64": p.archive, "source_digest": p.digest}
	case "workspace_source_sync":
		p.archive, _ = input["source_archive_base64"].(string)
		p.digest, _ = input["source_digest"].(string)
		p.paths = anyStrings(input["source_paths"])
		response = map[string]any{"source_digest": p.digest, "synced_paths": len(p.paths)}
	case "workspace_source_accept":
		p.digest, _ = input["source_digest"].(string)
		p.paths = anyStrings(input["source_paths"])
		response = map[string]any{"source_digest": p.digest}
	case "workspace_command_start":
		zero := 0
		response = map[string]any{"command": map[string]any{"id": "cmd_code", "status": "succeeded", "exit_code": zero}}
	case "workspace_command_logs":
		response = map[string]any{"logs": "workspace command output\n"}
	case "workspace_destroy":
		response = map[string]any{"destroyed": true, "workspace": map[string]any{"id": "wsp_code", "lifecycle_status": "destroyed"}}
	default:
		response = map[string]any{}
	}
	raw, _ := json.Marshal(response)
	return json.Unmarshal(raw, out)
}

func TestWorkspaceCommandSyncPreviewAndApply(t *testing.T) {
	db := openTestDB(t)
	platform := &codeWorkspacePlatform{}
	manifest := (&App{}).Manifest()
	ctx := sdk.NewAppCtxForTest(&manifest, db, nil, platform, nil)
	root := t.TempDir()
	local := NewLocalFileStore(root)
	locks := newRepoLockSet()
	app := &App{store: &lockedFileStore{inner: local, locks: locks}, locks: locks, dataDir: t.TempDir()}
	repo, err := dbCreateRepo(db, "project-a", CreateRepoInput{Name: "Workspace test", Framework: "blank"})
	if err != nil {
		t.Fatal(err)
	}
	if err := local.CreateRepo(repoStoreKey(repo)); err != nil {
		t.Fatal(err)
	}
	repoRoot := local.RepoPath(repoStoreKey(repo))
	writeTestSource(t, repoRoot, "hello.txt", []byte("one\n"), 0o644)
	callCtx := sdk.WithCaller(context.Background(), &sdk.Caller{ProjectID: "project-a", AgentID: 7, ThreadID: "task-1"})
	result, err := app.toolRunCommand(callCtx, ctx, map[string]any{
		"_project_id": "project-a", "slug": repo.Slug, "runtime": "workspace", "command": "true",
	})
	if err != nil {
		t.Fatal(err)
	}
	command := result.(*repoCommandResult)
	if command.Status != "success" || command.WorkspaceID != "wsp_code" || command.Runtime != "workspace" {
		t.Fatalf("unexpected workspace command result: %+v", command)
	}
	writeTestSource(t, repoRoot, "hello.txt", []byte("two\n"), 0o644)
	if _, err := app.toolRunCommand(callCtx, ctx, map[string]any{
		"_project_id": "project-a", "slug": repo.Slug, "runtime": "workspace", "command": "true",
	}); err != nil {
		t.Fatal(err)
	}
	if !containsCall(platform.calls, "workspaces/workspace_source_sync") {
		t.Fatal("Code source change was not synchronized to the existing workspace")
	}
	workspaceRoot := t.TempDir()
	writeTestSource(t, workspaceRoot, "hello.txt", []byte("generated\n"), 0o644)
	writeTestSource(t, workspaceRoot, "generated.txt", []byte("new\n"), 0o644)
	workspaceSnapshot, err := buildSourceSnapshot(workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	platform.archive = workspaceSnapshot.Archive
	previewAny, err := app.toolWorkspaceChanges(callCtx, ctx, map[string]any{"_project_id": "project-a", "slug": repo.Slug})
	if err != nil {
		t.Fatal(err)
	}
	preview := previewAny.(*workspaceChangesResult)
	if preview.Conflict || len(preview.Changes) != 2 {
		t.Fatalf("unexpected preview: %+v", preview)
	}
	if _, err := app.toolWorkspaceApply(callCtx, ctx, map[string]any{
		"_project_id": "project-a", "slug": repo.Slug, "expected_workspace_digest": preview.WorkspaceDigest,
	}); err != nil {
		t.Fatal(err)
	}
	if body, _ := os.ReadFile(filepath.Join(repoRoot, "hello.txt")); string(body) != "generated\n" {
		t.Fatalf("workspace change was not applied: %q", body)
	}
	if body, _ := os.ReadFile(filepath.Join(repoRoot, "generated.txt")); string(body) != "new\n" {
		t.Fatalf("workspace file was not created: %q", body)
	}
	writeTestSource(t, workspaceRoot, "generated.txt", []byte("workspace-two\n"), 0o644)
	workspaceSnapshot, err = buildSourceSnapshot(workspaceRoot)
	if err != nil {
		t.Fatal(err)
	}
	platform.archive = workspaceSnapshot.Archive
	previewAny, err = app.toolWorkspaceChanges(callCtx, ctx, map[string]any{"_project_id": "project-a", "slug": repo.Slug})
	if err != nil {
		t.Fatal(err)
	}
	preview = previewAny.(*workspaceChangesResult)
	writeTestSource(t, repoRoot, "hello.txt", []byte("concurrent Code edit\n"), 0o644)
	if _, err := app.toolWorkspaceApply(callCtx, ctx, map[string]any{
		"_project_id": "project-a", "slug": repo.Slug, "expected_workspace_digest": preview.WorkspaceDigest,
	}); err == nil {
		t.Fatal("concurrent Code edit should reject workspace apply")
	}
	if body, _ := os.ReadFile(filepath.Join(repoRoot, "hello.txt")); string(body) != "concurrent Code edit\n" {
		t.Fatalf("rejected apply overwrote Code: %q", body)
	}
	if _, err := app.toolWorkspaceDestroy(callCtx, ctx, map[string]any{
		"_project_id": "project-a", "slug": repo.Slug, "confirm": false,
	}); err == nil {
		t.Fatal("workspace destroy without confirmation should fail")
	}
	if _, err := app.toolWorkspaceDestroy(callCtx, ctx, map[string]any{
		"_project_id": "project-a", "slug": repo.Slug, "confirm": true,
	}); err != nil {
		t.Fatal(err)
	}
	if !containsCall(platform.calls, "workspaces/workspace_destroy") {
		t.Fatal("Code did not destroy the linked Workspaces environment")
	}
	link, err := dbGetRepoWorkspace(db, "project-a", repo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if link != nil {
		t.Fatalf("destroyed workspace link was retained: %+v", link)
	}
}

func anyStrings(value any) []string {
	switch values := value.(type) {
	case []string:
		return append([]string(nil), values...)
	case []any:
		out := make([]string, 0, len(values))
		for _, value := range values {
			if text, ok := value.(string); ok {
				out = append(out, text)
			}
		}
		return out
	default:
		return nil
	}
}

func containsCall(calls []string, want string) bool {
	for _, call := range calls {
		if call == want {
			return true
		}
	}
	return false
}
