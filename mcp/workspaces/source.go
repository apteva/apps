package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"path"
	"sort"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const maxManagedSourceArchiveBytes = 8 << 20

const (
	maxManagedSourceExpandedBytes = 128 << 20
	maxManagedSourceFiles         = 20000
)

var defaultSourceExcludes = []string{
	".git", ".home", "node_modules", ".next", ".nuxt", ".cache", "coverage",
	"dist", "build", "target", "vendor", ".gradle", "Pods", "DerivedData",
}

func sourcePathsArg(args map[string]any, key string) ([]string, error) {
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil, nil
	}
	var values []string
	switch list := raw.(type) {
	case []string:
		values = append(values, list...)
	case []any:
		for i, value := range list {
			s, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("%s[%d] must be a string", key, i)
			}
			values = append(values, s)
		}
	default:
		return nil, fmt.Errorf("%s must be an array of strings", key)
	}
	if len(values) > 20000 {
		return nil, fmt.Errorf("%s exceeds 20000 paths", key)
	}
	seen := make(map[string]struct{}, len(values))
	out := make([]string, 0, len(values))
	for _, value := range values {
		clean, err := normalizeSourcePath(value)
		if err != nil {
			return nil, err
		}
		if _, exists := seen[clean]; exists {
			continue
		}
		seen[clean] = struct{}{}
		out = append(out, clean)
	}
	sort.Strings(out)
	return out, nil
}

func normalizeSourcePath(value string) (string, error) {
	value = strings.TrimSpace(strings.ReplaceAll(value, "\\", "/"))
	if value == "" || strings.HasPrefix(value, "/") || strings.IndexByte(value, 0) >= 0 {
		return "", errors.New("source paths must be non-empty relative paths")
	}
	clean := path.Clean(value)
	if clean == "." || clean == ".." || strings.HasPrefix(clean, "../") {
		return "", fmt.Errorf("invalid source path %q", value)
	}
	first := strings.SplitN(clean, "/", 2)[0]
	if first == ".git" || first == ".home" {
		return "", fmt.Errorf("source path %q uses reserved workspace state", value)
	}
	return clean, nil
}

func validateSourceArchive(encoded string, manifest []string) error {
	compressed, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return fmt.Errorf("decode source archive: %w", err)
	}
	if len(compressed) > maxManagedSourceArchiveBytes {
		return errors.New("source archive exceeds 8 MiB compressed")
	}
	gz, err := gzip.NewReader(bytes.NewReader(compressed))
	if err != nil {
		return fmt.Errorf("open source archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	seen := make(map[string]struct{}, len(manifest))
	var expanded int64
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return fmt.Errorf("read source archive: %w", err)
		}
		name := strings.TrimPrefix(strings.TrimSpace(strings.ReplaceAll(header.Name, "\\", "/")), "./")
		if name == "" || name == "." || header.Typeflag == tar.TypeDir {
			continue
		}
		clean, err := normalizeSourcePath(name)
		if err != nil {
			return err
		}
		switch header.Typeflag {
		case tar.TypeReg, tar.TypeRegA:
			expanded += header.Size
			if expanded > maxManagedSourceExpandedBytes {
				return errors.New("source archive exceeds 128 MiB expanded")
			}
		case tar.TypeSymlink:
			if header.Linkname == "" || path.IsAbs(header.Linkname) {
				return fmt.Errorf("source symlink %q has an unsafe target", clean)
			}
			resolved := path.Clean(path.Join(path.Dir(clean), header.Linkname))
			if resolved == ".." || strings.HasPrefix(resolved, "../") {
				return fmt.Errorf("source symlink %q escapes the workspace source", clean)
			}
		default:
			return fmt.Errorf("unsupported source archive entry %q", clean)
		}
		seen[clean] = struct{}{}
		if len(seen) > maxManagedSourceFiles {
			return errors.New("source archive exceeds 20000 files")
		}
	}
	want := make(map[string]struct{}, len(manifest))
	for _, value := range manifest {
		want[value] = struct{}{}
	}
	if len(want) != len(seen) {
		return errors.New("source_paths does not match the source archive")
	}
	for value := range want {
		if _, exists := seen[value]; !exists {
			return fmt.Errorf("source_paths entry %q is missing from the source archive", value)
		}
	}
	return nil
}

func requireOriginatingApp(actor Actor) error {
	if actor.InstallID <= 0 || actor.AppName == "" {
		return errors.New("authenticated app caller required")
	}
	return nil
}

func (a *App) lockWorkspace(id string) func() {
	value, _ := a.workspaceLocks.LoadOrStore(id, &sync.Mutex{})
	lock := value.(*sync.Mutex)
	lock.Lock()
	return lock.Unlock
}

func (a *App) toolSourceSync(callCtx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	actor, err := actorFrom(callCtx, app)
	if err != nil {
		return nil, err
	}
	if err := requireOriginatingApp(actor); err != nil {
		return nil, err
	}
	w, err := requireWorkspaceForActor(app.AppDB(), actor, strArg(args, "workspace_id"))
	if err != nil {
		return nil, err
	}
	release := a.lockWorkspace(w.ID)
	defer release()
	w, err = requireWorkspaceForActor(app.AppDB(), actor, w.ID)
	if err != nil {
		return nil, err
	}
	if w.LifecycleStatus != statusRunning {
		return nil, fmt.Errorf("workspace is not writable in status %q", w.LifecycleStatus)
	}
	if err := ensureNoActiveCommand(app.AppDB(), w.ID); err != nil {
		return nil, err
	}
	archive := strArg(args, "source_archive_base64")
	digest := strArg(args, "source_digest")
	if archive == "" || digest == "" {
		return nil, errors.New("source_archive_base64 and source_digest are required")
	}
	expected := strArg(args, "expected_source_digest")
	if expected != "" && expected != w.SourceDigest {
		return nil, fmt.Errorf("source revision conflict: workspace baseline is %q, expected %q", w.SourceDigest, expected)
	}
	paths, err := sourcePathsArg(args, "source_paths")
	if err != nil {
		return nil, err
	}
	if err := validateSourceArchive(archive, paths); err != nil {
		return nil, err
	}
	current := make(map[string]struct{}, len(paths))
	for _, value := range paths {
		current[value] = struct{}{}
	}
	deleted := make([]string, 0)
	for _, value := range w.SourceManifest {
		if _, exists := current[value]; !exists {
			deleted = append(deleted, value)
		}
	}
	var imported map[string]any
	if err := app.PlatformAPI().CallAppResult("containers", "containers_volume_import", map[string]any{
		"workload_id": w.WorkloadID, "volume": "workspace", "path": ".", "archive_base64": archive,
	}, &imported); err != nil {
		return nil, err
	}
	for start := 0; start < len(deleted); start += 200 {
		end := start + 200
		if end > len(deleted) {
			end = len(deleted)
		}
		argv := []string{"/bin/rm", "-f", "--"}
		for _, value := range deleted[start:end] {
			argv = append(argv, "/workspace/"+value)
		}
		if err := a.runMaintenanceExecution(callCtx, app, w, argv, 120); err != nil {
			return nil, fmt.Errorf("remove stale source paths: %w", err)
		}
	}
	now := nowUTC()
	if err := updateWorkspace(app.AppDB(), w.ID, map[string]any{
		"source_digest": digest, "source_manifest_json": mustJSON(paths),
		"source_synced_at": now, "updated_at": now,
	}); err != nil {
		return nil, err
	}
	_ = recordActivity(app.AppDB(), w.ID, w.ProjectID, "source.synced", actor, "Managed source synchronized", map[string]any{
		"source_digest": digest, "paths": len(paths), "deleted_paths": len(deleted),
	})
	w, err = requireWorkspace(app.AppDB(), w.ProjectID, w.ID)
	return map[string]any{"workspace": w, "source_digest": digest, "synced_paths": len(paths), "deleted_paths": len(deleted)}, err
}

func (a *App) toolSourceAccept(callCtx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	actor, err := actorFrom(callCtx, app)
	if err != nil {
		return nil, err
	}
	if err := requireOriginatingApp(actor); err != nil {
		return nil, err
	}
	w, err := requireWorkspaceForActor(app.AppDB(), actor, strArg(args, "workspace_id"))
	if err != nil {
		return nil, err
	}
	release := a.lockWorkspace(w.ID)
	defer release()
	digest := strArg(args, "source_digest")
	if digest == "" {
		return nil, errors.New("source_digest is required")
	}
	paths, err := sourcePathsArg(args, "source_paths")
	if err != nil {
		return nil, err
	}
	now := nowUTC()
	if err := updateWorkspace(app.AppDB(), w.ID, map[string]any{
		"source_digest": digest, "source_manifest_json": mustJSON(paths),
		"source_synced_at": now, "updated_at": now,
	}); err != nil {
		return nil, err
	}
	_ = recordActivity(app.AppDB(), w.ID, w.ProjectID, "source.accepted", actor, "Exported source revision accepted", map[string]any{"source_digest": digest, "paths": len(paths)})
	w, err = requireWorkspace(app.AppDB(), w.ProjectID, w.ID)
	return map[string]any{"workspace": w, "source_digest": digest}, err
}

func (a *App) exportManagedSource(callCtx context.Context, app *sdk.AppCtx, actor Actor, w *Workspace, args map[string]any) (any, error) {
	if err := requireOriginatingApp(actor); err != nil {
		return nil, err
	}
	release := a.lockWorkspace(w.ID)
	defer release()
	var err error
	w, err = requireWorkspaceForActor(app.AppDB(), actor, w.ID)
	if err != nil {
		return nil, err
	}
	if w.LifecycleStatus != statusRunning {
		return nil, fmt.Errorf("workspace is not readable in status %q", w.LifecycleStatus)
	}
	if err := ensureNoActiveCommand(app.AppDB(), w.ID); err != nil {
		return nil, err
	}
	extra, err := sourcePathsArg(args, "exclude_paths")
	if err != nil {
		return nil, err
	}
	archiveName := "source-export-" + strings.TrimPrefix(newID("src"), "src_") + ".tar.gz"
	archivePath := "/cache/" + archiveName
	argv := []string{"tar", "-czf", archivePath}
	for _, value := range append(append([]string(nil), defaultSourceExcludes...), extra...) {
		argv = append(argv, "--exclude=./"+strings.TrimSuffix(value, "/"), "--exclude=./"+strings.TrimSuffix(value, "/")+"/**")
	}
	argv = append(argv, "-C", "/workspace", ".")
	if err := a.runMaintenanceExecution(callCtx, app, w, argv, 300); err != nil {
		return nil, fmt.Errorf("build managed source archive: %w", err)
	}
	defer func() {
		_ = a.runMaintenanceExecution(context.Background(), app, w, []string{"/bin/rm", "-f", "--", archivePath}, 30)
	}()
	var outer map[string]any
	if err := app.PlatformAPI().CallAppResult("containers", "containers_volume_export", map[string]any{
		"workload_id": w.WorkloadID, "volume": "cache", "path": archiveName,
	}, &outer); err != nil {
		return nil, err
	}
	encoded, _ := outer["archive_base64"].(string)
	inner, err := unwrapSingleArchiveFile(encoded)
	if err != nil {
		return nil, err
	}
	_ = recordActivity(app.AppDB(), w.ID, w.ProjectID, "source.exported", actor, "Managed source archive exported", map[string]any{"baseline_digest": w.SourceDigest})
	return map[string]any{
		"workspace_id": w.ID, "format": "tar.gz", "archive_base64": base64.StdEncoding.EncodeToString(inner),
		"compressed_bytes": len(inner), "source_digest": w.SourceDigest, "source_synced_at": w.SourceSyncedAt,
	}, nil
}

func (a *App) runMaintenanceExecution(callCtx context.Context, app *sdk.AppCtx, w *Workspace, argv []string, timeoutSeconds int) error {
	var started executionResponse
	if err := app.PlatformAPI().CallAppResult("containers", "containers_exec_start", map[string]any{
		"workload_id": w.WorkloadID, "argv": argv, "working_directory": "/workspace", "timeout_s": timeoutSeconds,
	}, &started); err != nil {
		return err
	}
	id := started.Execution.ID
	if id == "" {
		id = started.ExecutionID
	}
	if id == "" {
		return errors.New("containers_exec_start returned an empty execution id")
	}
	deadline := time.NewTimer(time.Duration(timeoutSeconds+5) * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-callCtx.Done():
			return callCtx.Err()
		case <-deadline.C:
			return errors.New("maintenance execution did not finish before deadline")
		case <-ticker.C:
			var out executionResponse
			if err := app.PlatformAPI().CallAppResult("containers", "containers_exec_get", map[string]any{"execution_id": id}, &out); err != nil {
				return err
			}
			exec := out.Execution
			if !commandTerminal(exec.Status) {
				continue
			}
			if exec.Status == "succeeded" && (exec.ExitCode == nil || *exec.ExitCode == 0) {
				return nil
			}
			var logs executionLogsResponse
			_ = app.PlatformAPI().CallAppResult("containers", "containers_exec_logs", map[string]any{"execution_id": id, "tail": 50}, &logs)
			return fmt.Errorf("execution %s: %s", exec.Status, strings.TrimSpace(logs.Logs))
		}
	}
}

func unwrapSingleArchiveFile(encoded string) ([]byte, error) {
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, fmt.Errorf("decode exported archive: %w", err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		return nil, fmt.Errorf("open exported archive: %w", err)
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, fmt.Errorf("read exported archive: %w", err)
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA {
			continue
		}
		if h.Size < 0 || h.Size > maxManagedSourceArchiveBytes {
			return nil, errors.New("managed source archive exceeds 8 MiB compressed")
		}
		data, err := io.ReadAll(io.LimitReader(tr, maxManagedSourceArchiveBytes+1))
		if err != nil {
			return nil, err
		}
		if len(data) > maxManagedSourceArchiveBytes {
			return nil, errors.New("managed source archive exceeds 8 MiB compressed")
		}
		return data, nil
	}
	return nil, errors.New("containers export did not contain the managed source archive")
}
