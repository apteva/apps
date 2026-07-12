package main

import (
	"encoding/base64"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestRunLocal_Output(t *testing.T) {
	out, exit, err := runLocal("echo hello && echo world", 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if exit != 0 {
		t.Errorf("exit = %d, want 0", exit)
	}
	if !strings.Contains(out, "hello") || !strings.Contains(out, "world") {
		t.Errorf("output missing markers: %q", out)
	}
}

func TestRunLocal_Timeout(t *testing.T) {
	_, _, err := runLocal("sleep 5", 200*time.Millisecond)
	if err == nil || !strings.Contains(err.Error(), "timed out") {
		t.Errorf("expected timeout error, got %v", err)
	}
}

func TestUploadLocal_AllowedRoot(t *testing.T) {
	tmp := t.TempDir()
	root := filepath.Join(tmp, "local-files")
	_ = os.MkdirAll(root, 0o755)

	// Resolve a relative path → must end up under root.
	got, err := resolveLocalPath(root, "subdir/file.txt")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(got, root+string(filepath.Separator)) {
		t.Errorf("resolved %q escapes root %q", got, root)
	}

	// Traversal must be rejected.
	if _, err := resolveLocalPath(root, "../../etc/passwd"); err == nil {
		t.Error("traversal not rejected")
	}

	// Absolute outside root must be rejected.
	if _, err := resolveLocalPath(root, "/etc/passwd"); err == nil {
		t.Error("absolute outside-root not rejected")
	}
}

func TestUploadLocal_WriteRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	t.Setenv("APTEVA_DATA_DIR", tmp)

	// uploadLocal needs ctx.DataDir() — emulate with a minimal env path.
	// For this test we go around the SDK and call resolveLocalPath +
	// the os write directly so the test stays self-contained.
	root := filepath.Join(tmp, "local-files")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("hello apteva")
	b64 := base64.StdEncoding.EncodeToString(body)
	resolved, err := resolveLocalPath(root, "greetings.txt")
	if err != nil {
		t.Fatal(err)
	}
	dec, err := base64.StdEncoding.DecodeString(b64)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(resolved, dec, 0o644); err != nil {
		t.Fatal(err)
	}
	read, err := os.ReadFile(resolved)
	if err != nil {
		t.Fatal(err)
	}
	if string(read) != string(body) {
		t.Errorf("round-trip = %q, want %q", read, body)
	}
}

func TestDownloadLocal_ReadRoundTrip(t *testing.T) {
	tmp := t.TempDir()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithEnv("APTEVA_DATA_DIR", tmp))
	root := filepath.Join(tmp, "local-files")
	if err := os.MkdirAll(root, 0o755); err != nil {
		t.Fatal(err)
	}
	body := []byte("download me")
	path := filepath.Join(root, "artifact.tgz")
	if err := os.WriteFile(path, body, 0o644); err != nil {
		t.Fatal(err)
	}
	gotB64, n, err := downloadLocal(ctx, "artifact.tgz")
	if err != nil {
		t.Fatal(err)
	}
	if n != len(body) {
		t.Fatalf("bytes = %d, want %d", n, len(body))
	}
	got, err := base64.StdEncoding.DecodeString(gotB64)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(body) {
		t.Errorf("download round-trip = %q, want %q", got, body)
	}
}

func TestResolveLocalPath_RejectsSymlinkEscape(t *testing.T) {
	root := t.TempDir()
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(root, "escape")); err != nil {
		t.Fatal(err)
	}
	if _, err := resolveLocalPath(root, "escape/owned.txt"); err == nil {
		t.Fatal("symlink escape was accepted")
	}
}
