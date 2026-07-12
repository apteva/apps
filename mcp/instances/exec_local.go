package main

// Local-instance execution — the in-process side of the same uniform
// MCP surface that drives remote instances.
//
// Trust model: the local instance runs in the same OS user as the
// Apteva sidecar, so it's implicitly trusted (no auth, no SSH).
// Path validation on file uploads keeps callers from clobbering
// arbitrary system files — writes are confined to a per-app data
// dir under ctx.DataDir().

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// localFilesRoot is the only path prefix instance_upload_file accepts
// for the local instance. Keeps the API safe by construction —
// nobody can write to /etc, /usr, or arbitrary user dirs from MCP.
// Subdirs allowed; absolute paths must start with this prefix.
func localFilesRoot(ctx *sdk.AppCtx) string {
	root := filepath.Join(ctx.DataDir(), "local-files")
	_ = os.MkdirAll(root, 0o755)
	return root
}

// runLocal executes a shell command on the local machine. Output
// captures stdout+stderr together (matches how SSH exec returns it),
// up to a 1 MB cap to avoid blowing memory on a noisy command. The
// timeout is best-effort — sub-shells respect SIGTERM, but a
// non-responsive child will get SIGKILL after timeout+5s.
func runLocal(cmd string, timeout time.Duration) (output string, exitCode int, err error) {
	if timeout <= 0 {
		timeout = 30 * time.Second
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	c := exec.CommandContext(ctx, "sh", "-c", cmd)
	writer := &lockedWriter{max: maxCommandOutputBytes}
	c.Stdout = writer
	c.Stderr = writer
	runErr := c.Run()
	out := writer.String()
	exit := -1
	if c.ProcessState != nil {
		exit = c.ProcessState.ExitCode()
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return string(out), exit, fmt.Errorf("command timed out after %s", timeout)
	}
	return string(out), exit, runErr
}

// uploadLocal writes file content to a path under localFilesRoot.
// Refuses absolute paths outside that root and refuses any traversal
// (../) — even though sandboxes leak through symlinks anyway, both
// guards together raise the bar enough for the MCP surface.
func uploadLocal(ctx *sdk.AppCtx, path string, contentB64 string) (bytesWritten int, err error) {
	if len(contentB64) > maxEncodedFileBytes {
		return 0, fmt.Errorf("file exceeds %d byte upload limit", maxFileTransferBytes)
	}
	root := localFilesRoot(ctx)
	cleaned, err := resolveLocalPath(root, path)
	if err != nil {
		return 0, err
	}
	body, err := base64.StdEncoding.DecodeString(contentB64)
	if err != nil {
		return 0, fmt.Errorf("invalid base64: %w", err)
	}
	if len(body) > maxFileTransferBytes {
		return 0, fmt.Errorf("file exceeds %d byte upload limit", maxFileTransferBytes)
	}
	if err := os.MkdirAll(filepath.Dir(cleaned), 0o755); err != nil {
		return 0, err
	}
	if err := os.WriteFile(cleaned, body, 0o644); err != nil {
		return 0, err
	}
	return len(body), nil
}

func downloadLocal(ctx *sdk.AppCtx, path string) (contentB64 string, bytesRead int, err error) {
	root := localFilesRoot(ctx)
	cleaned, err := resolveLocalPath(root, path)
	if err != nil {
		return "", 0, err
	}
	info, err := os.Stat(cleaned)
	if err != nil {
		return "", 0, err
	}
	if info.Size() > maxFileTransferBytes {
		return "", 0, fmt.Errorf("file exceeds %d byte download limit", maxFileTransferBytes)
	}
	body, err := os.ReadFile(cleaned)
	if err != nil {
		return "", 0, err
	}
	return base64.StdEncoding.EncodeToString(body), len(body), nil
}

// resolveLocalPath joins the requested path under root, verifies the
// lexical path, then resolves the nearest existing ancestor so a
// symlink cannot redirect writes or reads outside the data root.
func resolveLocalPath(root, requested string) (string, error) {
	if requested == "" {
		return "", errors.New("path required")
	}
	cleaned := filepath.Clean(requested)
	if filepath.IsAbs(cleaned) && !strings.HasPrefix(cleaned, root+string(filepath.Separator)) && cleaned != root {
		return "", fmt.Errorf("path must be relative or under %q", root)
	}
	full := filepath.Join(root, cleaned)
	abs, err := filepath.Abs(full)
	if err != nil {
		return "", err
	}
	rootAbs, err := filepath.Abs(root)
	if err != nil {
		return "", err
	}
	if abs != rootAbs && !strings.HasPrefix(abs, rootAbs+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes data root")
	}
	rootResolved, err := filepath.EvalSymlinks(rootAbs)
	if err != nil {
		return "", err
	}
	ancestor := abs
	for {
		if _, statErr := os.Lstat(ancestor); statErr == nil {
			break
		} else if !errors.Is(statErr, os.ErrNotExist) {
			return "", statErr
		}
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", fmt.Errorf("cannot resolve path ancestor")
		}
		ancestor = parent
	}
	resolvedAncestor, err := filepath.EvalSymlinks(ancestor)
	if err != nil {
		return "", err
	}
	if resolvedAncestor != rootResolved && !strings.HasPrefix(resolvedAncestor, rootResolved+string(filepath.Separator)) {
		return "", fmt.Errorf("path escapes data root through symlink")
	}
	return abs, nil
}
