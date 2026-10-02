package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"os"
	"os/exec"
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
	createArgs  map[string]any
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
	if tool == "workspace_create_for_resource" {
		p.createArgs = in
	}
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

func TestBlankHTMLPreviewSelectsBunWorkspace(t *testing.T) {
	a, old, repo := reliabilityApp(t)
	if repo.Framework != "blank" {
		t.Fatalf("fixture must reproduce blank repository metadata: %s", repo.Framework)
	}
	p := &codePreviewPlatform{bound: true}
	m := a.Manifest()
	ctx := sdk.NewAppCtxForTest(&m, old.AppDB(), nil, p, nil)
	want := "<!doctype html><html><body>STATIC_PREVIEW_OK</body></html>"
	if _, err := a.storeFor(repo).Write(repo.Slug, "index.html", []byte(want)); err != nil {
		t.Fatal(err)
	}
	a.dev = newDevSupervisor(a.dataDir, a.store, a, 6100, 6110)
	dr, err := a.dev.startDevRun(ctx, startDevInput{ProjectID: repo.ProjectID, Repo: repo})
	if err != nil {
		t.Fatal(err)
	}
	if dr.Framework != "static" || dr.Runner != "workspaces" || p.createArgs["profile"] != "bun" {
		t.Fatalf("static command and workspace runtime disagree: run=%+v profile=%v", dr, p.createArgs["profile"])
	}
	cmd := strings.Join(p.previewArgs["argv"].([]string), " ")
	if !strings.Contains(cmd, "'bun' '-e'") || !strings.Contains(cmd, "Bun.serve") || strings.Contains(cmd, "bun install") {
		t.Fatalf("unexpected static preview command: %s", cmd)
	}
	if len(a.dev.all_()) != 0 {
		t.Fatal("static workspace preview created a local process")
	}
	snapshot, err := parseSourceArchive(p.archive)
	if err != nil || string(snapshot.Entries["index.html"].Data) != want {
		t.Fatalf("HTML was not transferred unchanged: snapshot=%v err=%v", snapshot, err)
	}
	p.status = "live"
	dr, err = a.refreshWorkspacePreview(context.Background(), ctx, repo, dr)
	if err != nil || dr.Status != "live" {
		t.Fatalf("preview failed readiness: %+v %v", dr, err)
	}
	stored, err := dbGetRepoBySlug(ctx.AppDB(), repo.ProjectID, repo.Slug)
	if err != nil || stored.Framework != "blank" {
		t.Fatalf("preview changed repository metadata: %+v %v", stored, err)
	}
	if err := a.dev.stopDevRun(ctx, repo.ProjectID, repo.ID); err != nil {
		t.Fatal(err)
	}
}

func TestWorkspacePreviewProfileUsesResolvedFramework(t *testing.T) {
	for _, tc := range []struct {
		name, stored, resolved, marker, want string
	}{
		{"blank HTML", "blank", "static", "index.html", "bun"},
		{"explicit static over Go", "go", "static", "go.mod", "bun"},
		{"explicit Go over JS", "nextjs", "go", "package.json", "go"},
		{"node over stale Go metadata", "go", "node", "package.json", "bun"},
		{"nextjs", "blank", "nextjs", "package.json", "bun"},
		{"custom Python", "blank", "blank", "requirements.txt", "python"},
		{"custom Go", "blank", "blank", "go.mod", "go"},
		{"custom default", "blank", "blank", "", "go"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			snapshot := &sourceSnapshot{Entries: map[string]sourceEntry{}}
			if tc.marker != "" {
				snapshot.Entries[tc.marker] = sourceEntry{Path: tc.marker}
			}
			if got := workspacePreviewProfile(&Repo{Framework: tc.stored}, snapshot, tc.resolved); got != tc.want {
				t.Fatalf("profile=%s, want %s", got, tc.want)
			}
		})
	}
}

// Opt-in runtime check against Workspaces' default Bun profile image. Uses a
// disposable container and generated fixture, never the user's repository.
func TestStaticWorkspacePreviewDocker(t *testing.T) {
	if os.Getenv("CODE_TEST_DOCKER") != "1" {
		t.Skip("set CODE_TEST_DOCKER=1 to verify the static preview in Docker")
	}
	root := t.TempDir()
	want := "<!doctype html><html><body>STATIC_DOCKER_OK</body></html>"
	if err := os.WriteFile(filepath.Join(root, "index.html"), []byte(want), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "style.css"), []byte("body{color:red}"), 0644); err != nil {
		t.Fatal(err)
	}
	argv, err := workspacePreviewCommand(root, "static", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	args := []string{"run", "--detach", "--rm", "--publish", "127.0.0.1::3000", "--mount", "type=bind,source=" + root + ",target=/workspace,readonly", "--workdir", "/workspace", "oven/bun:1-debian"}
	args = append(args, argv...)
	// stdout is only the newly created container id; pull progress uses stderr.
	var stderr strings.Builder
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("create test container: %v: %s", err, stderr.String())
	}
	id := strings.TrimSpace(string(out))
	if len(id) != 64 || strings.ContainsAny(id, " \r\n") {
		t.Fatalf("unexpected test container id: %q", id)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		if output, err := exec.CommandContext(cleanup, "docker", "rm", "--force", id).CombinedOutput(); err != nil {
			t.Errorf("cleanup test container: %v: %s", err, output)
		}
	})
	port, err := exec.CommandContext(ctx, "docker", "port", id, "3000/tcp").Output()
	if err != nil {
		t.Fatal(err)
	}
	url := "http://" + strings.TrimSpace(string(port))
	client := &http.Client{Timeout: time.Second}
	deadline := time.Now().Add(20 * time.Second)
	ready := false
	for time.Now().Before(deadline) {
		resp, err := client.Get(url + "/")
		if err == nil {
			body, readErr := io.ReadAll(resp.Body)
			resp.Body.Close()
			if readErr == nil && resp.StatusCode == 200 && string(body) == want {
				ready = true
				break
			}
		}
		time.Sleep(100 * time.Millisecond)
	}
	if !ready {
		logs, _ := exec.CommandContext(ctx, "docker", "logs", id).CombinedOutput()
		t.Fatalf("static preview was not ready: %s", logs)
	}
	resp, err := client.Get(url + "/style.css")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil || resp.StatusCode != 200 || string(body) != "body{color:red}" {
		t.Fatalf("relative asset: HTTP %d %q, err=%v", resp.StatusCode, body, err)
	}
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

func TestPreviewNamesAreUniqueAndBounded(t *testing.T) {
	repo := &Repo{Slug: strings.Repeat("long-repository-", 10)}
	first, second := workspacePreviewName(repo), workspacePreviewName(repo)
	if first == second || len(first) > 80 || len(second) > 80 {
		t.Fatal(first, second)
	}
}
