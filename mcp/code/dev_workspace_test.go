package main

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type codePreviewPlatform struct {
	codeWorkspacePlatform
	bound       bool
	status      string
	failStart   bool
	identityErr bool
	previewArgs map[string]any
}

func (p *codePreviewPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	if p.identityErr {
		return nil, errors.New("platform offline")
	}
	bindings := map[string]any{}
	if p.bound {
		bindings["workspaces"] = int64(42)
	}
	return &sdk.InstallIdentity{Bindings: bindings}, nil
}
func (p *codePreviewPlatform) CallAppResult(app, tool string, in map[string]any, out any) error {
	switch tool {
	case "workspace_preview_start", "workspace_preview_get":
		p.calls = append(p.calls, tool)
		if tool == "workspace_preview_start" {
			p.previewArgs = in
			if p.failStart {
				return errors.New("cannot create preview")
			}
			p.status = "starting"
		}
		raw, _ := json.Marshal(map[string]any{"preview": map[string]any{"workspace_id": "wsp_code", "status": p.status, "host_port": 45678}})
		return json.Unmarshal(raw, out)
	case "workspace_preview_stop":
		p.calls = append(p.calls, tool)
		p.status = "stopped"
		return nil
	case "workspace_preview_logs":
		raw, _ := json.Marshal(map[string]any{"logs": "container output"})
		return json.Unmarshal(raw, out)
	}
	return p.codeWorkspacePlatform.CallAppResult(app, tool, in, out)
}
func TestRunSelectsWorkspaceWithoutLocalPermission(t *testing.T) {
	a, old, repo := reliabilityApp(t)
	p := &codePreviewPlatform{bound: true}
	m := a.Manifest()
	ctx := sdk.NewAppCtxForTest(&m, old.AppDB(), sdk.Config{}, p, nil)
	root := a.storeFor(repo).(FileStoreLocalPath).RepoPath(repo.Slug)
	os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"scripts":{"dev":"vite"}}`), 0600)
	a.dev = newDevSupervisor(a.dataDir, a.store, a, 6100, 6110)
	perm, err := a.executionPermission(ctx, repo)
	if err != nil || perm.RequiresLocalExecution || perm.Enabled {
		t.Fatalf("workspace asked for local permission: %v %v", perm, err)
	}
	dr, err := a.dev.startDevRun(ctx, startDevInput{ProjectID: repo.ProjectID, Repo: repo})
	if err != nil {
		t.Fatal(err)
	}
	if dr.Runner != "workspaces" || dr.WorkspaceID == "" || dr.PID != 0 || len(a.dev.all_()) != 0 {
		t.Fatalf("local process created: %+v", dr)
	}
	cmd := strings.Join(p.previewArgs["argv"].([]string), " ")
	if !strings.Contains(cmd, "bun install") || !strings.Contains(cmd, "0.0.0.0") || !strings.Contains(cmd, "3000") {
		t.Fatalf("invalid container command %s", cmd)
	}
	p.status = "live"
	dr, err = a.refreshWorkspacePreview(context.Background(), ctx, repo, dr)
	if err != nil || dr.Status != "live" || dr.PreviewURL != "http://127.0.0.1:45678/" {
		t.Fatalf("bad readiness: %+v %v", dr, err)
	}
	a.storeFor(repo).Write(repo.Slug, "new.txt", []byte("edit"))
	dr, err = a.refreshWorkspacePreview(context.Background(), ctx, repo, dr)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, call := range p.calls {
		if call == "workspaces/workspace_source_sync" {
			found = true
		}
	}
	if !found {
		t.Fatal("edits not synced")
	}
	if err = a.dev.reconcileOrphanDevRuns(ctx); err != nil {
		t.Fatal(err)
	}
	current, _ := dbGetDevRun(ctx.AppDB(), repo.ProjectID, repo.ID)
	if current.Status != "live" {
		t.Fatal("restart orphaned container")
	}
	if err = a.dev.stopDevRun(ctx, repo.ProjectID, repo.ID); err != nil {
		t.Fatal(err)
	}
	current, _ = dbGetDevRun(ctx.AppDB(), repo.ProjectID, repo.ID)
	if current.Status != "stopped" || p.status != "stopped" {
		t.Fatal("stop did not delegate")
	}
	p.failStart = true
	if _, err = a.dev.startDevRun(ctx, startDevInput{ProjectID: repo.ProjectID, Repo: repo}); err == nil {
		t.Fatal("failed preview silently fell back")
	}
	if len(a.dev.all_()) != 0 {
		t.Fatal("local fallback")
	}
	p.identityErr = true
	if _, err = a.dev.startDevRun(ctx, startDevInput{ProjectID: repo.ProjectID, Repo: repo}); err == nil {
		t.Fatal("binding failure silently fell back")
	}
}
func TestWorkspaceCommandDoesNotNeedHostBinaries(t *testing.T) {
	root := t.TempDir()
	os.WriteFile(filepath.Join(root, "package.json"), []byte(`{"scripts":{"dev":"vite --port 5173"}}`), 0600)
	t.Setenv("PATH", "")
	argv, err := workspacePreviewCommand(root, "node", "")
	if err != nil {
		t.Fatal(err)
	}
	text := strings.Join(argv, " ")
	if !strings.Contains(text, "bun --bun run dev --host 0.0.0.0 --port 3000 --strictPort") {
		t.Fatal(text)
	}
	argv, err = workspacePreviewCommand(root, "blank", "python -m http.server $PORT --bind 0.0.0.0")
	if err != nil || argv[2] != "python -m http.server $PORT --bind 0.0.0.0" {
		t.Fatal(argv, err)
	}
}

type blockedPreviewCreate struct {
	codePreviewPlatform
	entered chan struct{}
	release chan struct{}
}

func (p *blockedPreviewCreate) CallAppResult(app, tool string, in map[string]any, out any) error {
	err := p.codePreviewPlatform.CallAppResult(app, tool, in, out)
	if tool == "workspace_create_for_resource" {
		close(p.entered)
		<-p.release
	}
	return err
}
func TestStopDuringWorkspaceProvisioning(t *testing.T) {
	a, old, repo := reliabilityApp(t)
	p := &blockedPreviewCreate{codePreviewPlatform: codePreviewPlatform{bound: true}, entered: make(chan struct{}), release: make(chan struct{})}
	m := a.Manifest()
	ctx := sdk.NewAppCtxForTest(&m, old.AppDB(), nil, p, nil)
	a.dev = newDevSupervisor(a.dataDir, a.store, a, 6100, 6110)
	done := make(chan error, 1)
	go func() {
		_, err := a.dev.startDevRun(ctx, startDevInput{ProjectID: repo.ProjectID, Repo: repo, Framework: "static"})
		done <- err
	}()
	select {
	case <-p.entered:
	case <-time.After(5 * time.Second):
		t.Fatal("start not reached")
	}
	if err := a.dev.stopDevRun(ctx, repo.ProjectID, repo.ID); err != nil {
		t.Fatal(err)
	}
	close(p.release)
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("start did not cancel")
	}
	dr, _ := dbGetDevRun(ctx.AppDB(), repo.ProjectID, repo.ID)
	if dr.Status != "stopped" || dr.WorkspaceID == "" || p.status != "stopped" {
		t.Fatalf("leaked startup workspace: %+v %s", dr, p.status)
	}
}
