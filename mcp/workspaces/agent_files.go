package main

// Agent-facing file tools. Agents previously had to read and write files via
// shell heredocs; these tools move bytes through Containers' bounded volume
// archive transfer instead, so content is exact and edits can be guarded by a
// SHA-256 precondition.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"path"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	maxAgentFileBytes      = 2 << 20
	maxAgentImportEncoded  = 12 << 20
	defaultReadLineLimit   = 2000
	maxReadLineLimit       = 10000
	defaultListLimit       = 500
	maxListLimit           = 5000
	maxListDepth           = 20
	listMaintenanceTimeout = 60
)

var defaultListExcludes = []string{".git", "node_modules", ".home"}

// normalizeWorkspaceFilePath accepts a path relative to /workspace (an
// absolute /workspace/... path is also accepted and made relative). Unlike
// managed-source paths, agents may touch dotfiles such as .home/.bashrc.
func normalizeWorkspaceFilePath(value string, allowRoot bool) (string, error) {
	value = strings.TrimSpace(value)
	if value == "/workspace" {
		value = "."
	} else if strings.HasPrefix(value, "/workspace/") {
		value = strings.TrimPrefix(value, "/workspace/")
	}
	if value == "" && allowRoot {
		return ".", nil
	}
	if value == "" || strings.HasPrefix(value, "/") || strings.IndexByte(value, 0) >= 0 {
		return "", errors.New("path must be relative to /workspace (or start with /workspace/)")
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." {
			return "", errors.New("path must not contain '..'")
		}
	}
	clean := path.Clean(value)
	if !allowRoot && clean == "." {
		return "", errors.New("path must name one file")
	}
	return clean, nil
}

func sha256Hex(data []byte) string {
	sum := sha256.Sum256(data)
	return hex.EncodeToString(sum[:])
}

// fileWorkspace resolves the workspace for a file tool and checks that its
// volume can be transferred. Volumes stay readable while suspended.
func fileWorkspace(callCtx context.Context, app *sdk.AppCtx, args map[string]any) (Actor, *Workspace, error) {
	actor, err := actorFrom(callCtx, app)
	if err != nil {
		return actor, nil, err
	}
	w, err := requireWorkspaceForActor(app.AppDB(), actor, strArg(args, "workspace_id"))
	if err != nil {
		return actor, nil, err
	}
	if err := requireDevWorkspace(w, "file access"); err != nil {
		return actor, nil, err
	}
	if err := requireFileAccessible(w); err != nil {
		return actor, nil, err
	}
	return actor, w, nil
}

func requireFileAccessible(w *Workspace) error {
	if w.WorkloadID == "" || (w.LifecycleStatus != statusRunning && w.LifecycleStatus != statusSuspended && w.LifecycleStatus != statusExpired) {
		return fmt.Errorf("workspace files are not accessible in status %q", w.LifecycleStatus)
	}
	return nil
}

// errFileUnavailable marks an export that Containers refused: the path does
// not exist or crosses a symlink. Other errors mean the file exists but is
// not editable text (directory, binary, larger than 2 MB).
type errFileUnavailable struct{ err error }

func (e *errFileUnavailable) Error() string { return e.err.Error() }
func (e *errFileUnavailable) Unwrap() error { return e.err }

// exportWorkspaceFile fetches one regular text file of at most 2 MB.
func exportWorkspaceFile(app *sdk.AppCtx, w *Workspace, rel string) ([]byte, *tar.Header, error) {
	var exported struct {
		ArchiveBase64 string `json:"archive_base64"`
	}
	if err := runtimeCall(app, "containers_volume_export", map[string]any{
		"workload_id": w.WorkloadID, "volume": "workspace", "path": rel,
	}, &exported); err != nil {
		return nil, nil, &errFileUnavailable{fmt.Errorf("read %s: %w (the file may not exist or may be behind a symlink)", rel, err)}
	}
	return unpackTextFile(exported.ArchiveBase64, rel)
}

func unpackTextFile(encoded, wanted string) ([]byte, *tar.Header, error) {
	if len(encoded) == 0 || len(encoded) > maxAgentImportEncoded {
		return nil, nil, errors.New("file archive is empty or exceeds the archive limit")
	}
	archive, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, nil, errors.New("invalid file archive from container runtime")
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return nil, nil, errors.New("invalid gzip file archive from container runtime")
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return nil, nil, err
		}
		if path.Clean(strings.TrimPrefix(header.Name, "./")) != wanted {
			continue
		}
		if header.Typeflag == tar.TypeDir {
			return nil, nil, fmt.Errorf("%s is a directory; use workspace_file_list", wanted)
		}
		if header.Typeflag != tar.TypeReg && header.Typeflag != tar.TypeRegA {
			return nil, nil, fmt.Errorf("%s is not a regular file", wanted)
		}
		if header.Size < 0 || header.Size > maxAgentFileBytes {
			return nil, nil, fmt.Errorf("%s is larger than 2 MB; use a shell command such as head or sed -n", wanted)
		}
		content, err := io.ReadAll(io.LimitReader(tr, maxAgentFileBytes+1))
		if err != nil {
			return nil, nil, err
		}
		if len(content) > maxAgentFileBytes {
			return nil, nil, fmt.Errorf("%s is larger than 2 MB", wanted)
		}
		if bytes.IndexByte(content, 0) >= 0 {
			return nil, nil, fmt.Errorf("%s looks binary; file tools handle text only", wanted)
		}
		return content, header, nil
	}
	return nil, nil, fmt.Errorf("%s was not found in the exported archive", wanted)
}

// packWorkspaceFile builds a tar.gz holding only the file's base name and is
// imported at the parent directory, so no other path is touched. Containers
// stages imports under umask 0077 (the same umask workspace processes run
// with), so new parent directories are created as 0700 just like `mkdir -p`
// in the agent's shell would create them.
func packWorkspaceFile(rel string, mode int64, uid, gid int, content []byte) (string, string, error) {
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	tw := tar.NewWriter(gz)
	if mode == 0 {
		mode = 0o644
	}
	if err := tw.WriteHeader(&tar.Header{
		Name: path.Base(rel), Mode: mode, Uid: uid, Gid: gid,
		Size: int64(len(content)), Typeflag: tar.TypeReg, ModTime: time.Now(),
	}); err != nil {
		return "", "", err
	}
	if _, err := tw.Write(content); err != nil {
		return "", "", err
	}
	if err := tw.Close(); err != nil {
		return "", "", err
	}
	if err := gz.Close(); err != nil {
		return "", "", err
	}
	return base64.StdEncoding.EncodeToString(buffer.Bytes()), path.Dir(rel), nil
}

func importWorkspaceFile(app *sdk.AppCtx, w *Workspace, rel string, header *tar.Header, content []byte) error {
	var mode int64
	uid, gid := 0, 0
	if header != nil {
		mode, uid, gid = header.Mode, header.Uid, header.Gid
	}
	archive, dir, err := packWorkspaceFile(rel, mode, uid, gid, content)
	if err != nil {
		return err
	}
	var imported map[string]any
	return runtimeCall(app, "containers_volume_import", map[string]any{
		"workload_id": w.WorkloadID, "volume": "workspace", "path": dir, "archive_base64": archive,
	}, &imported)
}

func (a *App) toolFileRead(callCtx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	_, w, err := fileWorkspace(callCtx, app, args)
	if err != nil {
		return nil, err
	}
	rel, err := normalizeWorkspaceFilePath(strArg(args, "path"), false)
	if err != nil {
		return nil, err
	}
	offset := intArg(args, "offset", 1)
	limit := intArg(args, "limit", defaultReadLineLimit)
	if offset < 1 {
		return nil, errors.New("offset is a 1-based line number")
	}
	if limit < 1 || limit > maxReadLineLimit {
		return nil, fmt.Errorf("limit must be between 1 and %d lines", maxReadLineLimit)
	}
	content, _, err := exportWorkspaceFile(app, w, rel)
	if err != nil {
		return nil, err
	}
	window, start, end, total := lineWindow(string(content), offset, limit)
	return map[string]any{
		"workspace_id": w.ID, "path": rel, "content": window,
		"start_line": start, "end_line": end, "total_lines": total,
		"truncated": end < total || start > 1, "bytes": len(content), "sha256": sha256Hex(content),
	}, nil
}

// lineWindow returns lines [offset, offset+limit) using 1-based numbering.
// Line endings are preserved so content can be fed back into edits verbatim.
func lineWindow(content string, offset, limit int) (string, int, int, int) {
	if content == "" {
		return "", 0, 0, 0
	}
	lines := strings.SplitAfter(content, "\n")
	if lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	total := len(lines)
	if offset > total {
		return "", offset, offset - 1, total
	}
	end := offset - 1 + limit
	if end > total {
		end = total
	}
	return strings.Join(lines[offset-1:end], ""), offset, end, total
}

func (a *App) toolFileWrite(callCtx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	actor, w, err := fileWorkspace(callCtx, app, args)
	if err != nil {
		return nil, err
	}
	rel, err := normalizeWorkspaceFilePath(strArg(args, "path"), false)
	if err != nil {
		return nil, err
	}
	content, ok := args["content"].(string)
	if !ok {
		return nil, errors.New("content must be a string")
	}
	if len(content) > maxAgentFileBytes || strings.IndexByte(content, 0) >= 0 {
		return nil, errors.New("content must be text of at most 2 MB")
	}
	release := a.lockWorkspace(w.ID)
	defer release()
	expected := strings.ToLower(strings.TrimSpace(strArg(args, "expected_sha256")))
	createOnly := boolArg(args, "create_only")
	before, header, readErr := exportWorkspaceFile(app, w, rel)
	var unavailable *errFileUnavailable
	if readErr != nil && !errors.As(readErr, &unavailable) {
		// The path exists but is a directory, binary, or too large. Refuse
		// rather than silently replacing something the agent never read.
		return nil, fmt.Errorf("refusing to overwrite: %w", readErr)
	}
	existed := readErr == nil
	switch {
	case createOnly && existed:
		return nil, fmt.Errorf("%s already exists; read it and use workspace_apply_edits, or drop create_only", rel)
	case expected != "" && !existed:
		return nil, fmt.Errorf("expected_sha256 was given but %s could not be read: %v", rel, readErr)
	case expected != "" && expected != sha256Hex(before):
		return nil, fmt.Errorf("file changed: expected SHA-256 %s, current %s; read it again", expected, sha256Hex(before))
	}
	if existed && string(before) == content {
		return map[string]any{"workspace_id": w.ID, "path": rel, "changed": false, "created": false, "sha256": sha256Hex(before), "bytes": len(content)}, nil
	}
	if !existed {
		header = nil
	}
	if err := importWorkspaceFile(app, w, rel, header, []byte(content)); err != nil {
		return nil, err
	}
	summary := "Workspace file written"
	if !existed {
		summary = "Workspace file created"
	}
	_ = recordActivity(app.AppDB(), w.ID, w.ProjectID, "file.written", actor, summary+": "+rel, map[string]any{"path": rel, "bytes": len(content)})
	result := map[string]any{"workspace_id": w.ID, "path": rel, "changed": true, "created": !existed, "sha256": sha256Hex([]byte(content)), "bytes": len(content)}
	if existed {
		result["previous_sha256"] = sha256Hex(before)
	}
	return result, nil
}

type fileEdit struct {
	Old        string
	New        string
	ReplaceAll bool
}

func parseFileEdits(raw any) ([]fileEdit, error) {
	values, ok := raw.([]any)
	if !ok || len(values) == 0 || len(values) > 100 {
		return nil, errors.New("edits must contain 1 to 100 replacements")
	}
	edits := make([]fileEdit, 0, len(values))
	for i, value := range values {
		entry, ok := value.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("edits[%d] must be an object", i)
		}
		oldValue, oldOK := entry["old_string"].(string)
		newValue, newOK := entry["new_string"].(string)
		if !oldOK || !newOK || oldValue == "" {
			return nil, fmt.Errorf("edits[%d] requires a nonempty old_string and a string new_string", i)
		}
		if len(oldValue) > maxAgentFileBytes || len(newValue) > maxAgentFileBytes {
			return nil, fmt.Errorf("edits[%d] exceeds the file size limit", i)
		}
		replaceAll, _ := entry["replace_all"].(bool)
		edits = append(edits, fileEdit{Old: oldValue, New: newValue, ReplaceAll: replaceAll})
	}
	return edits, nil
}

// applyFileEdits applies replacements in order; each sees the previous result.
func applyFileEdits(content string, edits []fileEdit) (string, error) {
	for i, edit := range edits {
		count := strings.Count(content, edit.Old)
		if count == 0 {
			return "", fmt.Errorf("edits[%d]: old_string was not found", i)
		}
		if count > 1 && !edit.ReplaceAll {
			return "", fmt.Errorf("edits[%d]: old_string matches %d times; add surrounding context or set replace_all", i, count)
		}
		if edit.ReplaceAll {
			content = strings.ReplaceAll(content, edit.Old, edit.New)
		} else {
			content = strings.Replace(content, edit.Old, edit.New, 1)
		}
		if len(content) > maxAgentFileBytes {
			return "", errors.New("edited file would exceed 2 MB")
		}
	}
	return content, nil
}

func (a *App) toolApplyEdits(callCtx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	actor, w, err := fileWorkspace(callCtx, app, args)
	if err != nil {
		return nil, err
	}
	rel, err := normalizeWorkspaceFilePath(strArg(args, "path"), false)
	if err != nil {
		return nil, err
	}
	edits, err := parseFileEdits(args["edits"])
	if err != nil {
		return nil, err
	}
	release := a.lockWorkspace(w.ID)
	defer release()
	before, header, err := exportWorkspaceFile(app, w, rel)
	if err != nil {
		return nil, err
	}
	priorHash := sha256Hex(before)
	if expected := strings.TrimSpace(strArg(args, "expected_sha256")); expected != "" && !strings.EqualFold(expected, priorHash) {
		return nil, fmt.Errorf("file changed: expected SHA-256 %s, current %s; read it again", expected, priorHash)
	}
	after, err := applyFileEdits(string(before), edits)
	if err != nil {
		return nil, err
	}
	if after == string(before) {
		return map[string]any{"workspace_id": w.ID, "path": rel, "changed": false, "sha256": priorHash}, nil
	}
	if err := importWorkspaceFile(app, w, rel, header, []byte(after)); err != nil {
		return nil, err
	}
	_ = recordActivity(app.AppDB(), w.ID, w.ProjectID, "file.edited", actor, "Workspace file edited: "+rel, map[string]any{"path": rel, "edits": len(edits)})
	return map[string]any{
		"workspace_id": w.ID, "path": rel, "changed": true,
		"previous_sha256": priorHash, "sha256": sha256Hex([]byte(after)), "bytes": len(after),
	}, nil
}

func (a *App) toolSourceImport(callCtx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	actor, w, err := fileWorkspace(callCtx, app, args)
	if err != nil {
		return nil, err
	}
	rel, err := normalizeWorkspaceFilePath(strArg(args, "path"), true)
	if err != nil {
		return nil, err
	}
	archive := strings.TrimSpace(strArg(args, "archive_base64"))
	if archive == "" || len(archive) > maxAgentImportEncoded {
		return nil, errors.New("archive_base64 is required and must fit the 8 MB compressed archive limit")
	}
	release := a.lockWorkspace(w.ID)
	defer release()
	// A bulk import can overwrite files a running command is using.
	if err := ensureNoActiveCommand(app.AppDB(), w.ID); err != nil {
		return nil, err
	}
	var out map[string]any
	if err := runtimeCall(app, "containers_volume_import", map[string]any{
		"workload_id": w.WorkloadID, "volume": "workspace", "path": rel, "archive_base64": archive,
	}, &out); err != nil {
		return nil, err
	}
	_ = recordActivity(app.AppDB(), w.ID, w.ProjectID, "source.imported", actor, "Source archive imported", map[string]any{"path": rel})
	out["workspace_id"] = w.ID
	out["path"] = rel
	return out, nil
}

type listedEntry struct {
	Path string `json:"path"`
	Type string `json:"type"`
	Size int64  `json:"size"`
}

func (a *App) toolFileList(callCtx context.Context, app *sdk.AppCtx, args map[string]any) (any, error) {
	_, w, err := fileWorkspace(callCtx, app, args)
	if err != nil {
		return nil, err
	}
	// Listing runs find in the container, so it needs a running container.
	if w.LifecycleStatus != statusRunning {
		return nil, fmt.Errorf("workspace must be running to list files (status %q); resume it first", w.LifecycleStatus)
	}
	rel, err := normalizeWorkspaceFilePath(strArg(args, "path"), true)
	if err != nil {
		return nil, err
	}
	depth := intArg(args, "max_depth", 3)
	if depth < 1 || depth > maxListDepth {
		return nil, fmt.Errorf("max_depth must be between 1 and %d", maxListDepth)
	}
	limit := intArg(args, "limit", defaultListLimit)
	if limit < 1 || limit > maxListLimit {
		return nil, fmt.Errorf("limit must be between 1 and %d", maxListLimit)
	}
	pattern := strings.TrimSpace(strArg(args, "name_pattern"))
	if strings.ContainsAny(pattern, "/\x00") || len(pattern) > 200 {
		return nil, errors.New("name_pattern is a find -name glob such as *.go; it must not contain '/'")
	}
	excludes := defaultListExcludes
	if raw, ok := args["exclude"]; ok {
		excludes, err = stringListArg(raw, 64)
		if err != nil {
			return nil, fmt.Errorf("exclude: %w", err)
		}
	}
	root := "/workspace"
	if rel != "." {
		root += "/" + rel
	}
	logs, err := a.runMaintenanceCapture(callCtx, app, w, listArgv(root, depth, pattern, excludes, limit), listMaintenanceTimeout, limit+2)
	if err != nil {
		return nil, fmt.Errorf("list %s: %w", rel, err)
	}
	entries, truncated := parseListOutput(logs, limit)
	return map[string]any{
		"workspace_id": w.ID, "path": rel, "entries": entries,
		"count": len(entries), "truncated": truncated, "excluded": excludes,
	}, nil
}

func stringListArg(raw any, max int) ([]string, error) {
	values, ok := raw.([]any)
	if !ok {
		if typed, ok := raw.([]string); ok {
			values = make([]any, len(typed))
			for i, v := range typed {
				values[i] = v
			}
		} else {
			return nil, errors.New("must be an array of strings")
		}
	}
	if len(values) > max {
		return nil, fmt.Errorf("at most %d entries", max)
	}
	out := make([]string, 0, len(values))
	for _, value := range values {
		s, ok := value.(string)
		s = strings.TrimSpace(s)
		if !ok || s == "" || strings.ContainsAny(s, "/\x00") {
			return nil, errors.New("entries must be plain directory or file names")
		}
		out = append(out, s)
	}
	return out, nil
}

// listArgv prunes excluded names and prints "type<TAB>size<TAB>path" lines.
// It builds find's expression in the positional parameters (no eval, so
// names are never re-parsed by the shell) and avoids GNU-only find flags so
// busybox-based custom images also work.
func listArgv(root string, depth int, pattern string, excludes []string, limit int) []string {
	script := `root="$1"; depth="$2"; pattern="$3"; limit="$4"; shift 4
[ -e "$root" ] || { echo "path not found" >&2; exit 2; }
[ -d "$root" ] || { echo "path is not a directory; use workspace_file_read" >&2; exit 2; }
n=$#; first=1
for name do
  if [ "$first" = 1 ]; then set -- "$@" "("; first=0; else set -- "$@" -o; fi
  set -- "$@" -name "$name"
done
shift "$n"
if [ "$n" -gt 0 ]; then set -- "$@" ")" -prune -o; fi
if [ -n "$pattern" ]; then set -- "$@" -name "$pattern"; fi
set -- "$@" -print
find "$root" -mindepth 1 -maxdepth "$depth" "$@" | head -n "$((limit + 1))" | while IFS= read -r entry; do
  if [ -L "$entry" ]; then t=l; size=0
  elif [ -d "$entry" ]; then t=d; size=0
  else t=f; size=$(wc -c < "$entry" 2>/dev/null || echo 0); fi
  printf '%s\t%s\t%s\n' "$t" "$size" "${entry#"$root"/}"
done`
	argv := []string{"/bin/sh", "-c", script, "sh", root, strconv.Itoa(depth), pattern, strconv.Itoa(limit)}
	return append(argv, excludes...)
}

func parseListOutput(logs string, limit int) ([]listedEntry, bool) {
	entries := make([]listedEntry, 0)
	truncated := false
	for _, line := range strings.Split(strings.ReplaceAll(logs, "\r\n", "\n"), "\n") {
		parts := strings.SplitN(line, "\t", 3)
		if len(parts) != 3 || parts[2] == "" {
			continue
		}
		if len(entries) == limit {
			truncated = true
			break
		}
		kind := map[string]string{"d": "dir", "f": "file", "l": "symlink"}[parts[0]]
		if kind == "" {
			continue
		}
		size, _ := strconv.ParseInt(strings.TrimSpace(parts[1]), 10, 64)
		entries = append(entries, listedEntry{Path: parts[2], Type: kind, Size: size})
	}
	return entries, truncated
}

// runMaintenanceCapture runs a short helper process outside the persistent
// shell session (so it never disturbs the agent's shell state) and returns
// its output on success.
func (a *App) runMaintenanceCapture(callCtx context.Context, app *sdk.AppCtx, w *Workspace, argv []string, timeoutSeconds, tailLines int) (string, error) {
	var started executionResponse
	if err := runtimeCall(app, "containers_exec_start", map[string]any{
		"workload_id": w.WorkloadID, "argv": argv, "working_directory": "/workspace", "timeout_s": timeoutSeconds,
	}, &started); err != nil {
		return "", err
	}
	id := firstNonEmpty(started.Execution.ID, started.ExecutionID)
	if id == "" {
		return "", errors.New("containers_exec_start returned an empty execution id")
	}
	deadline := time.NewTimer(time.Duration(timeoutSeconds+5) * time.Second)
	defer deadline.Stop()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-callCtx.Done():
			return "", callCtx.Err()
		case <-deadline.C:
			return "", errors.New("helper did not finish before its deadline")
		case <-ticker.C:
			var out executionResponse
			if err := runtimeCall(app, "containers_exec_get", map[string]any{"execution_id": id}, &out); err != nil {
				return "", err
			}
			if !commandTerminal(out.Execution.Status) {
				continue
			}
			var logs executionLogsResponse
			if err := runtimeCall(app, "containers_exec_logs", map[string]any{"execution_id": id, "tail": tailLines}, &logs); err != nil {
				return "", err
			}
			if out.Execution.Status == "succeeded" && (out.Execution.ExitCode == nil || *out.Execution.ExitCode == 0) {
				return logs.Logs, nil
			}
			return "", fmt.Errorf("%s: %s", out.Execution.Status, strings.TrimSpace(logs.Logs))
		}
	}
}
