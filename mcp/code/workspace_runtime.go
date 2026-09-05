package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"

	"sort"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const workspacesAppName = "workspaces"

type workspaceWire struct {
	ID              string   `json:"id"`
	LifecycleStatus string   `json:"lifecycle_status"`
	Image           string   `json:"image"`
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
	WorkspacePaths   []string       `json:"workspace_paths,omitempty"`
	SupportPaths     []string       `json:"support_paths,omitempty"`
	Changes          []SourceChange `json:"changes"`
}

func (a *App) prepareExecutionWorkspace(callCtx context.Context, app *sdk.AppCtx, repo *Repo, requestedProfile, requestedImage string, requestedWorkspacePaths, requestedSupportPaths []string) (*workspacePrepareResult, error) {
	workspacePaths, supportPaths, err := normalizeWorkspaceScope(requestedWorkspacePaths, requestedSupportPaths)
	if err != nil {
		return nil, err
	}
	link, err := dbGetRepoWorkspace(app.AppDB(), repo.ProjectID, repo.ID)
	if err != nil {
		return nil, err
	}
	// Omitted scope arguments reuse the linked workspace's scope. Use
	// workspace_paths=["**"] when intentionally switching back to a full,
	// editable repository scope.
	if link != nil && len(requestedWorkspacePaths) == 0 && len(requestedSupportPaths) == 0 {
		workspacePaths = append([]string(nil), link.WorkspacePaths...)
		supportPaths = append([]string(nil), link.SupportPaths...)
	}
	codeSnapshot, err := a.snapshotRepoSource(repo, workspacePaths, supportPaths)
	if err != nil {
		return nil, err
	}
	profile := strings.TrimSpace(requestedProfile)
	if profile == "" {
		profile = sourceProfile(repo, codeSnapshot)
	}
	image := strings.TrimSpace(requestedImage)
	if image == "" {
		image = strings.TrimSpace(repo.WorkspaceImage)
	}
	if link == nil {
		return a.createExecutionWorkspace(callCtx, app, repo, profile, image, workspacePaths, supportPaths, codeSnapshot)
	}
	workspaceSnapshot, err := a.exportExecutionWorkspace(app, link.WorkspaceID)
	if err != nil {

		return nil, err
	}
	profileChanged := link.Profile != profile
	imageChanged := image != "" && link.Image != image
	scopeChanged := !stringSlicesEqual(link.WorkspacePaths, workspacePaths) || !stringSlicesEqual(link.SupportPaths, supportPaths)
	if profileChanged || imageChanged || scopeChanged {
		if workspaceSnapshot.Digest != link.SourceDigest {
			return nil, errors.New("the requested workspace profile, image, or path scope requires replacement, but the current workspace has unapplied source changes; preview and apply or explicitly destroy it first")
		}
		// Keep the previous environment and its volumes intact. The new link is
		// saved only after provisioning succeeds; failure leaves the old link usable.
		// The old workspace remains available under its existing retention policy.
		return a.createExecutionWorkspace(callCtx, app, repo, profile, image, workspacePaths, supportPaths, codeSnapshot)
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

func (a *App) createExecutionWorkspace(callCtx context.Context, app *sdk.AppCtx, repo *Repo, profile, image string, workspacePaths, supportPaths []string, snapshot *sourceSnapshot) (*workspacePrepareResult, error) {
	input := map[string]any{
		"name": workspaceResourceName(repo), "purpose": "Run and test Code repository " + repo.Slug,
		"profile": profile, "ttl_minutes": 240, "resource_kind": "code.repository",
		"resource_id": fmt.Sprintf("%d", repo.ID), "repo_label": repo.Slug,
		"source_archive_base64": snapshot.Archive, "source_digest": snapshot.Digest,
		"source_paths": snapshot.Paths,
	}
	if image != "" {
		input["image"] = image
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
	resolvedImage := strings.TrimSpace(out.Workspace.Image)
	if resolvedImage == "" {
		resolvedImage = image
	}
	link := &RepoWorkspace{ProjectID: repo.ProjectID, RepoID: repo.ID, WorkspaceID: out.Workspace.ID,
		Profile: profile, Image: resolvedImage, SourceDigest: snapshot.Digest, SourcePaths: snapshot.Paths,
		WorkspacePaths: workspacePaths, SupportPaths: supportPaths}
	if err := dbPutRepoWorkspace(app.AppDB(), link); err != nil {
		return nil, err
	}
	return &workspacePrepareResult{Link: link, Code: snapshot, Workspace: snapshot, Created: true}, nil
}

func workspaceResourceName(repo *Repo) string {
	const suffix = " development"
	base := slugify(repo.Slug)
	if len(base) > 80-len(suffix) {
		base = strings.TrimRight(base[:80-len(suffix)], "-_. ")
	}
	if base == "" {
		base = "code-repository"
	}
	return base + suffix
}

func (a *App) syncExecutionWorkspace(app *sdk.AppCtx, link *RepoWorkspace, snapshot *sourceSnapshot) error {
	var out map[string]any
	err := app.PlatformAPI().CallAppResult(workspacesAppName, "workspace_source_sync", map[string]any{
		"workspace_id": link.WorkspaceID, "source_archive_base64": snapshot.Archive,
		"source_digest": snapshot.Digest, "expected_source_digest": link.SourceDigest,
		"source_paths": snapshot.Paths,
	}, &out)
	if err != nil {
		return fmt.Errorf("sync Code source to workspace: %w; workspace and its source link were preserved; preview and reconcile the changes before retrying", err)
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
	if _, ok := snapshot.Entries["pyproject.toml"]; ok {
		return "python"
	}
	return "go"
}

func dependencyPlan(snapshot *sourceSnapshot) (string, string) {
	names := map[string]bool{}
	for _, name := range []string{"package.json", "bun.lock", "bun.lockb", "package-lock.json", "pnpm-lock.yaml", "pnpm-workspace.yaml", "yarn.lock", "go.mod", "go.sum", "go.work", "go.work.sum", "requirements.txt", "pyproject.toml", "uv.lock"} {
		names[name] = true
	}
	paths := []string{}
	for path := range snapshot.Entries {
		if names[filepath.Base(path)] {
			paths = append(paths, path)
		}
	}
	sort.Strings(paths)
	h := sha256.New()
	for _, path := range paths {
		fmt.Fprintf(h, "%s\x00%s\n", path, shaOf(snapshot.Entries[path].Data))
	}
	sort.Slice(paths, func(i, j int) bool {
		a, b := strings.Count(paths[i], "/"), strings.Count(paths[j], "/")
		if a != b {
			return a < b
		}
		return paths[i] < paths[j]
	})
	commands := []string{}
	jsRoots := []string{}
	for _, path := range paths {
		base, dir := filepath.Base(path), filepath.ToSlash(filepath.Dir(path))
		command := ""
		exists := func(name string) bool {
			_, ok := snapshot.Entries[filepath.ToSlash(filepath.Join(dir, name))]
			return ok
		}
		switch base {
		case "package.json":
			covered := false
			for _, root := range jsRoots {
				if root == "." || strings.HasPrefix(dir, root+"/") {
					covered = true
				}
			}
			if covered {
				continue
			}
			jsRoots = append(jsRoots, dir)
			command = "bun install"
			if exists("bun.lock") || exists("bun.lockb") {
				command += " --frozen-lockfile"
			}
		case "go.mod":
			command = "go mod download"
		case "requirements.txt":
			command = "python -m pip install -r requirements.txt"
		case "pyproject.toml":
			if exists("requirements.txt") {
				continue
			}
			if exists("uv.lock") {
				command = "uv sync --frozen"
			} else {
				command = "python -m pip install ."
			}
		}
		if command != "" {
			if dir != "." {
				command = "(cd " + shellQuote(dir) + " && " + command + ")"
			}
			commands = append(commands, command)
		}
	}
	plan := strings.Join(commands, " && ")
	fmt.Fprintf(h, "plan-v2:%s", plan)
	return plan, fmt.Sprintf("%x", h.Sum(nil))
}
func shellQuote(value string) string { return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'" }

func (a *App) runWorkspaceCommand(callCtx context.Context, app *sdk.AppCtx, prep *workspacePrepareResult, input repoCommandInput) (*repoCommandResult, error) {
	env, err := workspaceEnv(input.EnvJSON)
	if err != nil {
		return nil, err
	}
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
	callCtx, cancel := context.WithTimeout(callCtx, time.Duration(timeout)*time.Second)
	defer cancel()
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
		install, logs, err := a.executeWorkspaceCommand(callCtx, app, prep.Link.WorkspaceID, installCommand, env, timeout, input.TailLines, "install")
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
			return nil, "", errors.Join(callCtx.Err(), a.cancelWorkspaceCommand(app, workspaceID, commandWire.ID))
		case <-deadline.C:
			return nil, "", errors.Join(context.DeadlineExceeded, a.cancelWorkspaceCommand(app, workspaceID, commandWire.ID))
		case <-ticker.C:
			var current struct {
				Command workspaceCommandWire `json:"command"`
			}
			if err := app.PlatformAPI().CallAppResult(workspacesAppName, "workspace_command_get", map[string]any{
				"workspace_id": workspaceID, "command_id": commandWire.ID,
			}, &current); err != nil {
				return nil, "", errors.Join(err, a.cancelWorkspaceCommand(app, workspaceID, commandWire.ID))
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

func (a *App) cancelWorkspaceCommand(app *sdk.AppCtx, workspaceID, commandID string) error {
	var out map[string]any
	err := app.PlatformAPI().CallAppResult(workspacesAppName, "workspace_command_cancel", map[string]any{
		"workspace_id": workspaceID, "command_id": commandID,
	}, &out)
	if err != nil {
		return fmt.Errorf("command %s in workspace %s may still be running; cancellation failed: %w", commandID, workspaceID, err)
	}
	return nil
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

func (a *App) snapshotRepoSource(repo *Repo, workspacePaths, supportPaths []string) (*sourceSnapshot, error) {
	return a.snapshotRepoSourceMode(repo, workspacePaths, supportPaths, true)
}
func (a *App) snapshotRepoSourceMode(repo *Repo, workspacePaths, supportPaths []string, archive bool) (*sourceSnapshot, error) {
	root, err := repoLocalPath(a.storeFor(repo), repo.Slug)
	if err != nil {
		return nil, err
	}
	if a.locks == nil {
		return buildSourceSnapshotMode(root, workspacePaths, supportPaths, archive)
	}
	release := a.locks.rlock(repoStoreKey(repo))
	defer release()
	return buildSourceSnapshotMode(root, workspacePaths, supportPaths, archive)
}

func (a *App) workspaceChanges(app *sdk.AppCtx, repo *Repo) (*workspaceChangesResult, *sourceSnapshot, *sourceSnapshot, *RepoWorkspace, error) {
	link, err := dbGetRepoWorkspace(app.AppDB(), repo.ProjectID, repo.ID)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	if link == nil {
		return nil, nil, nil, nil, errors.New("repository has no linked execution workspace; run a command with runtime=workspace first")
	}
	code, err := a.snapshotRepoSourceMode(repo, link.WorkspacePaths, link.SupportPaths, false)
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
		WorkspacePaths:   append([]string(nil), link.WorkspacePaths...),
		SupportPaths:     append([]string(nil), link.SupportPaths...),
		Changes:          diffSourceSnapshots(code, workspace),
	}
	for i := range result.Changes {
		result.Changes[i].Editable = len(link.WorkspacePaths) == 0 && len(link.SupportPaths) == 0 || matchesWorkspacePatterns(result.Changes[i].Path, link.WorkspacePaths)
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
	if len(link.WorkspacePaths) > 0 || len(link.SupportPaths) > 0 {
		for _, change := range preview.Changes {
			if !change.Editable {
				return nil, fmt.Errorf("workspace changed read-only or out-of-scope path %q; changes were not applied", change.Path)
			}
		}
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
	current, currentErr := buildSourceSnapshotMode(root, link.WorkspacePaths, link.SupportPaths, false)
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
