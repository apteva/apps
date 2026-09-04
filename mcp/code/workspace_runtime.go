package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const workspacesAppName = "workspaces"

type workspaceWire struct {
	ID              string   `json:"id"`
	LifecycleStatus string   `json:"lifecycle_status"`
	SourceDigest    string   `json:"source_digest"`
	SourcePaths     []string `json:"source_paths"`
}

type workspaceCommandWire struct {
	ID              string `json:"id"`
	Status          string `json:"status"`
	ExitCode        *int   `json:"exit_code"`
	Error           string `json:"error"`
	OutputTruncated bool   `json:"output_truncated"`
}

type workspacePrepareResult struct {
	Link      *RepoWorkspace
	Code      *sourceSnapshot
	Workspace *sourceSnapshot
	Created   bool
	Synced    bool
}

type workspaceChangesResult struct {
	WorkspaceID      string         `json:"workspace_id"`
	BaselineDigest   string         `json:"baseline_digest"`
	CodeDigest       string         `json:"code_digest"`
	WorkspaceDigest  string         `json:"workspace_digest"`
	CodeChanged      bool           `json:"code_changed_since_sync"`
	WorkspaceChanged bool           `json:"workspace_changed_since_sync"`
	Conflict         bool           `json:"conflict"`
	Changes          []SourceChange `json:"changes"`
}

func (a *App) prepareExecutionWorkspace(callCtx context.Context, app *sdk.AppCtx, repo *Repo, profile string) (*workspacePrepareResult, error) {
	codeSnapshot, err := a.snapshotRepoSource(repo)
	if err != nil {
		return nil, err
	}
	link, err := dbGetRepoWorkspace(app.AppDB(), repo.ProjectID, repo.ID)
	if err != nil {
		return nil, err
	}
	if link == nil {
		return a.createExecutionWorkspace(callCtx, app, repo, profile, codeSnapshot)
	}
	workspaceSnapshot, err := a.exportExecutionWorkspace(app, link.WorkspaceID)
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "not found") || strings.Contains(strings.ToLower(err.Error()), "destroy") {
			_ = dbDeleteRepoWorkspace(app.AppDB(), repo.ProjectID, repo.ID)
			return a.createExecutionWorkspace(callCtx, app, repo, profile, codeSnapshot)
		}
		return nil, err
	}
	result := &workspacePrepareResult{Link: link, Code: codeSnapshot, Workspace: workspaceSnapshot}
	codeChanged := codeSnapshot.Digest != link.SourceDigest
	workspaceChanged := workspaceSnapshot.Digest != link.SourceDigest
	if codeSnapshot.Digest == workspaceSnapshot.Digest {
		if link.SourceDigest != codeSnapshot.Digest {
			if err := a.acceptWorkspaceSource(app, link.WorkspaceID, codeSnapshot); err != nil {
				return nil, err
			}
			link.SourceDigest, link.SourcePaths = codeSnapshot.Digest, codeSnapshot.Paths
			if err := dbPutRepoWorkspace(app.AppDB(), link); err != nil {
				return nil, err
			}
		}
		return result, nil
	}
	if codeChanged && workspaceChanged {
		return nil, errors.New("both Code and the linked workspace changed since the last sync; preview and reconcile workspace changes before running another command")
	}
	if codeChanged {
		if err := a.syncExecutionWorkspace(app, link, codeSnapshot); err != nil {
			return nil, err
		}
		link.SourceDigest, link.SourcePaths = codeSnapshot.Digest, codeSnapshot.Paths
		if err := dbPutRepoWorkspace(app.AppDB(), link); err != nil {
			return nil, err
		}
		result.Workspace = codeSnapshot
		result.Synced = true
	}
	return result, nil
}

func (a *App) createExecutionWorkspace(callCtx context.Context, app *sdk.AppCtx, repo *Repo, requestedProfile string, snapshot *sourceSnapshot) (*workspacePrepareResult, error) {
	profile := strings.TrimSpace(requestedProfile)
	if profile == "" {
		profile = sourceProfile(repo, snapshot)
	}
	input := map[string]any{
		"name": repo.Name + " development", "purpose": "Run and test Code repository " + repo.Slug,
		"profile": profile, "ttl_minutes": 240, "resource_kind": "code.repository",
		"resource_id": fmt.Sprintf("%d", repo.ID), "repo_label": repo.Slug,
		"source_archive_base64": snapshot.Archive, "source_digest": snapshot.Digest,
		"source_paths": snapshot.Paths,
	}
	if caller := sdk.CallerFrom(callCtx); caller != nil {
		input["owner_agent_id"] = caller.AgentID
		input["owner_thread_id"] = caller.ThreadID
	}
	var out struct {
		Workspace workspaceWire `json:"workspace"`
	}
	if err := app.PlatformAPI().CallAppResult(workspacesAppName, "workspace_create_for_resource", input, &out); err != nil {
		return nil, fmt.Errorf("create execution workspace: %w", err)
	}
	if out.Workspace.ID == "" {
		return nil, errors.New("Workspaces returned an empty workspace id")
	}
	link := &RepoWorkspace{ProjectID: repo.ProjectID, RepoID: repo.ID, WorkspaceID: out.Workspace.ID,
		Profile: profile, SourceDigest: snapshot.Digest, SourcePaths: snapshot.Paths}
	if err := dbPutRepoWorkspace(app.AppDB(), link); err != nil {
		return nil, err
	}
	return &workspacePrepareResult{Link: link, Code: snapshot, Workspace: snapshot, Created: true}, nil
}

func (a *App) syncExecutionWorkspace(app *sdk.AppCtx, link *RepoWorkspace, snapshot *sourceSnapshot) error {
	var out map[string]any
	err := app.PlatformAPI().CallAppResult(workspacesAppName, "workspace_source_sync", map[string]any{
		"workspace_id": link.WorkspaceID, "source_archive_base64": snapshot.Archive,
		"source_digest": snapshot.Digest, "expected_source_digest": link.SourceDigest,
		"source_paths": snapshot.Paths,
	}, &out)
	if err != nil {
		var destroyed map[string]any
		destroyErr := app.PlatformAPI().CallAppResult(workspacesAppName, "workspace_destroy", map[string]any{
			"workspace_id": link.WorkspaceID, "confirm": true,
		}, &destroyed)
		_ = dbDeleteRepoWorkspace(app.AppDB(), link.ProjectID, link.RepoID)
		if destroyErr != nil {
			return fmt.Errorf("sync Code source to workspace: %w; the partial workspace could not be destroyed: %v", err, destroyErr)
		}
		return fmt.Errorf("sync Code source to workspace: %w; the partial workspace was destroyed and will be recreated on the next run", err)
	}
	return nil
}

func (a *App) exportExecutionWorkspace(app *sdk.AppCtx, workspaceID string) (*sourceSnapshot, error) {
	var out struct {
		Archive string `json:"archive_base64"`
	}
	if err := app.PlatformAPI().CallAppResult(workspacesAppName, "workspace_source_export", map[string]any{
		"workspace_id": workspaceID, "managed": true,
	}, &out); err != nil {
		return nil, fmt.Errorf("export workspace source: %w", err)
	}
	if out.Archive == "" {
		return nil, errors.New("Workspaces returned an empty source archive")
	}
	return parseSourceArchive(out.Archive)
}

func (a *App) acceptWorkspaceSource(app *sdk.AppCtx, workspaceID string, snapshot *sourceSnapshot) error {
	var out map[string]any
	if err := app.PlatformAPI().CallAppResult(workspacesAppName, "workspace_source_accept", map[string]any{
		"workspace_id": workspaceID, "source_digest": snapshot.Digest, "source_paths": snapshot.Paths,
	}, &out); err != nil {
		return fmt.Errorf("accept workspace source revision: %w", err)
	}
	return nil
}

func sourceProfile(repo *Repo, snapshot *sourceSnapshot) string {
	if _, ok := snapshot.Entries["go.mod"]; ok || repo.Framework == "go" {
		return "go"
	}
	if _, ok := snapshot.Entries["package.json"]; ok || repo.Framework == "nextjs" || repo.Framework == "static" {
		return "bun"
	}
	if _, ok := snapshot.Entries["requirements.txt"]; ok || repo.Framework == "python" {
		return "python"
	}
	return "go"
}

func dependencyPlan(snapshot *sourceSnapshot) (string, string) {
	paths := []string{"package.json", "bun.lock", "bun.lockb", "package-lock.json", "pnpm-lock.yaml", "yarn.lock", "go.mod", "go.sum", "requirements.txt", "pyproject.toml", "uv.lock"}
	h := sha256.New()
	for _, path := range paths {
		if entry, ok := snapshot.Entries[path]; ok {
			fmt.Fprintf(h, "%s\x00%s\n", path, shaOf(entry.Data))
		}
	}
	digest := fmt.Sprintf("%x", h.Sum(nil))
	if _, ok := snapshot.Entries["package.json"]; ok {
		if _, ok := snapshot.Entries["bun.lock"]; ok {
			return "bun install --frozen-lockfile", digest
		}
		if _, ok := snapshot.Entries["bun.lockb"]; ok {
			return "bun install --frozen-lockfile", digest
		}
		return "bun install", digest
	}
	if _, ok := snapshot.Entries["go.mod"]; ok {
		return "go mod download", digest
	}
	if _, ok := snapshot.Entries["requirements.txt"]; ok {
		return "python -m pip install -r requirements.txt", digest
	}
	return "", digest
}

func (a *App) runWorkspaceCommand(callCtx context.Context, app *sdk.AppCtx, prep *workspacePrepareResult, input repoCommandInput) (*repoCommandResult, error) {
	command := strings.TrimSpace(input.Command)
	if command == "" {
		return nil, errors.New("command required")
	}
	timeout := input.TimeoutSeconds
	if timeout <= 0 {
		timeout = 300
	}
	if timeout > 1800 {
		timeout = 1800
	}
	startedAt := time.Now()
	result := &repoCommandResult{Status: "failed", Command: command, ExitCode: -1, WorkspaceID: prep.Link.WorkspaceID, Runtime: "workspace"}
	activeSnapshot := prep.Workspace
	if prep.Synced || prep.Created {
		activeSnapshot = prep.Code
	}
	installCommand, dependencyDigest := dependencyPlan(activeSnapshot)
	if installCommand != "" && prep.Link.DependencyDigest != dependencyDigest {
		result.DependencyInstallRan = true
		result.DependencyInstallNote = installCommand
		install, logs, err := a.executeWorkspaceCommand(callCtx, app, prep.Link.WorkspaceID, installCommand, nil, timeout, input.TailLines, "install")
		if err != nil {
			result.Error, result.LogTail = err.Error(), logs
			result.DurationMS = time.Since(startedAt).Milliseconds()
			return result, nil
		}
		if install.ExitCode == nil || *install.ExitCode != 0 || install.Status != "succeeded" {
			result.Error, result.LogTail = firstNonEmpty(install.Error, "dependency installation failed"), logs
			result.DurationMS = time.Since(startedAt).Milliseconds()
			return result, nil
		}
		prep.Link.DependencyDigest = dependencyDigest
		if err := dbPutRepoWorkspace(app.AppDB(), prep.Link); err != nil {
			return nil, err
		}
	}
	env, err := workspaceEnv(input.EnvJSON)
	if err != nil {
		return nil, err
	}
	wire, logs, err := a.executeWorkspaceCommand(callCtx, app, prep.Link.WorkspaceID, command, env, timeout, input.TailLines, "run")
	result.DurationMS = time.Since(startedAt).Milliseconds()
	result.LogTail, result.StdoutTail = logs, logs
	if err != nil {
		result.Error = err.Error()
		result.TimedOut = errors.Is(err, context.DeadlineExceeded)
		return result, nil
	}
	if wire.ExitCode != nil {
		result.ExitCode = *wire.ExitCode
	}
	if wire.Status == "succeeded" && result.ExitCode == 0 {
		result.Status = "success"
	} else {
		result.Error = firstNonEmpty(wire.Error, "workspace command "+wire.Status)
	}
	return result, nil
}

func (a *App) executeWorkspaceCommand(callCtx context.Context, app *sdk.AppCtx, workspaceID, command string, env []string, timeout, tail int, phase string) (*workspaceCommandWire, string, error) {
	argv := append([]string{"env"}, env...)
	argv = append(argv, "/bin/sh", "-c", command)
	var started struct {
		Command workspaceCommandWire `json:"command"`
	}
	if err := app.PlatformAPI().CallAppResult(workspacesAppName, "workspace_command_start", map[string]any{
		"workspace_id": workspaceID, "argv": argv, "working_directory": "/workspace", "timeout_s": timeout,
		"idempotency_key": workspaceCommandIdempotencyKey(callCtx, phase, command),
	}, &started); err != nil {
		return nil, "", err
	}
	if started.Command.ID == "" {
		return nil, "", errors.New("Workspaces returned an empty command id")
	}
	deadline := time.NewTimer(time.Duration(timeout+10) * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	commandWire := started.Command
	for !workspaceCommandTerminal(commandWire.Status) {
		select {
		case <-callCtx.Done():
			a.cancelWorkspaceCommand(app, workspaceID, commandWire.ID)
			return nil, "", callCtx.Err()
		case <-deadline.C:
			a.cancelWorkspaceCommand(app, workspaceID, commandWire.ID)
			return nil, "", context.DeadlineExceeded
		case <-ticker.C:
			var current struct {
				Command workspaceCommandWire `json:"command"`
			}
			if err := app.PlatformAPI().CallAppResult(workspacesAppName, "workspace_command_get", map[string]any{
				"workspace_id": workspaceID, "command_id": commandWire.ID,
			}, &current); err != nil {
				return nil, "", err
			}
			commandWire = current.Command
		}
	}
	if tail <= 0 {
		tail = 200
	}
	if tail > 2000 {
		tail = 2000
	}
	var logOut struct {
		Logs string `json:"logs"`
	}
	_ = app.PlatformAPI().CallAppResult(workspacesAppName, "workspace_command_logs", map[string]any{
		"workspace_id": workspaceID, "command_id": commandWire.ID, "tail": tail,
	}, &logOut)
	return &commandWire, logOut.Logs, nil
}

func workspaceCommandIdempotencyKey(callCtx context.Context, phase, command string) string {
	callID := ""
	if caller := sdk.CallerFrom(callCtx); caller != nil {
		callID = strings.TrimSpace(caller.ToolCallID)
	}
	if callID == "" {
		callID = fmt.Sprintf("%d", time.Now().UnixNano())
	}
	hash := sha256.Sum256([]byte(command))
	return fmt.Sprintf("code:%s:%s:%x", callID, phase, hash[:8])
}

func (a *App) cancelWorkspaceCommand(app *sdk.AppCtx, workspaceID, commandID string) {
	var out map[string]any
	_ = app.PlatformAPI().CallAppResult(workspacesAppName, "workspace_command_cancel", map[string]any{
		"workspace_id": workspaceID, "command_id": commandID,
	}, &out)
}

func workspaceCommandTerminal(status string) bool {
	switch status {
	case "succeeded", "failed", "cancelled", "timed_out":
		return true
	default:
		return false
	}
}

func workspaceEnv(raw string) ([]string, error) {
	if strings.TrimSpace(raw) == "" {
		return nil, nil
	}
	values := map[string]string{}
	if err := json.Unmarshal([]byte(raw), &values); err != nil {
		return nil, errors.New("env_json must be a JSON object of string values")
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		if !validEnvKey(key) {
			return nil, fmt.Errorf("invalid environment variable name %q", key)
		}
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		if strings.IndexByte(values[key], 0) >= 0 {
			return nil, fmt.Errorf("environment variable %s contains NUL", key)
		}
		out = append(out, key+"="+values[key])
	}
	return out, nil
}

func validEnvKey(key string) bool {
	if key == "" || (key[0] < 'A' || key[0] > 'Z') && (key[0] < 'a' || key[0] > 'z') && key[0] != '_' {
		return false
	}
	for _, ch := range key[1:] {
		if (ch < 'A' || ch > 'Z') && (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') && ch != '_' {
			return false
		}
	}
	return true
}

func repoLocalPath(store FileStore, slug string) (string, error) {
	local, ok := store.(FileStoreLocalPath)
	if !ok || local.RepoPath(slug) == "" {
		return "", errors.New("workspace execution requires a local Code repository store")
	}
	return local.RepoPath(slug), nil
}

func (a *App) snapshotRepoSource(repo *Repo) (*sourceSnapshot, error) {
	root, err := repoLocalPath(a.storeFor(repo), repo.Slug)
	if err != nil {
		return nil, err
	}
	if a.locks == nil {
		return buildSourceSnapshot(root)
	}
	release := a.locks.rlock(repoStoreKey(repo))
	defer release()
	return buildSourceSnapshot(root)
}

func (a *App) workspaceChanges(app *sdk.AppCtx, repo *Repo) (*workspaceChangesResult, *sourceSnapshot, *sourceSnapshot, *RepoWorkspace, error) {
	link, err := dbGetRepoWorkspace(app.AppDB(), repo.ProjectID, repo.ID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if link == nil {
		return nil, nil, nil, nil, errors.New("repository has no linked execution workspace; run a command with runtime=workspace first")
	}
	code, err := a.snapshotRepoSource(repo)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	workspace, err := a.exportExecutionWorkspace(app, link.WorkspaceID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	result := &workspaceChangesResult{
		WorkspaceID: link.WorkspaceID, BaselineDigest: link.SourceDigest,
		CodeDigest: code.Digest, WorkspaceDigest: workspace.Digest,
		CodeChanged:      code.Digest != link.SourceDigest,
		WorkspaceChanged: workspace.Digest != link.SourceDigest,
		Changes:          diffSourceSnapshots(code, workspace),
	}
	result.Conflict = result.CodeChanged && result.WorkspaceChanged && code.Digest != workspace.Digest
	return result, code, workspace, link, nil
}

func (a *App) applyWorkspaceChanges(app *sdk.AppCtx, repo *Repo, expectedDigest string) (*workspaceChangesResult, error) {
	preview, _, workspace, link, err := a.workspaceChanges(app, repo)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(expectedDigest) == "" || expectedDigest != workspace.Digest {
		return nil, errors.New("expected_workspace_digest must match a fresh repos_workspace_changes preview")
	}
	if preview.Conflict || (preview.CodeChanged && preview.CodeDigest != workspace.Digest) {
		return nil, errors.New("Code changed since this workspace was synchronized; changes were not applied")
	}
	if preview.CodeDigest == workspace.Digest {
		if err := a.acceptWorkspaceSource(app, link.WorkspaceID, workspace); err != nil {
			return nil, err
		}
		link.SourceDigest, link.SourcePaths = workspace.Digest, workspace.Paths
		if err := dbPutRepoWorkspace(app.AppDB(), link); err != nil {
			return nil, err
		}
		return preview, nil
	}
	root, err := repoLocalPath(a.storeFor(repo), repo.Slug)
	if err != nil {
		return nil, err
	}
	if a.locks == nil {
		return nil, errors.New("repository locks are not initialized")
	}
	release := a.locks.lock(repoStoreKey(repo))
	current, currentErr := buildSourceSnapshot(root)
	if currentErr == nil && current.Digest != preview.CodeDigest {
		currentErr = errors.New("Code changed after the workspace preview; changes were not applied")
	}
	if currentErr == nil {
		currentErr = applySourceSnapshot(root, link.SourcePaths, workspace)
	}
	release()
	if currentErr != nil {
		return nil, currentErr
	}
	if err := a.acceptWorkspaceSource(app, link.WorkspaceID, workspace); err != nil {
		return nil, fmt.Errorf("workspace files were safely applied to Code, but revision acknowledgement failed: %w", err)
	}
	link.SourceDigest, link.SourcePaths = workspace.Digest, workspace.Paths
	if err := dbPutRepoWorkspace(app.AppDB(), link); err != nil {
		return nil, err
	}
	preview.BaselineDigest = workspace.Digest
	preview.CodeDigest = workspace.Digest
	preview.CodeChanged = false
	preview.WorkspaceChanged = false
	preview.Conflict = false
	return preview, nil
}
