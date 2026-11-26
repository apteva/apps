package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// These run only as root inside a Linux container, the setting container
// mode is for. CI: docker run --rm -v "$PWD":/src -w /src golang:alpine
// go test ./... (see the 0.12.0 release notes).
func requireContainerMode(t *testing.T) *localDriver {
	t.Helper()
	if runtime.GOOS != "linux" || os.Geteuid() != 0 || !insideContainer() {
		t.Skip("container mode needs root inside a Linux container")
	}
	if !currentSandboxFacts().SetUID {
		t.Skip("cannot switch users here")
	}
	return newLocalDriver(testLocalRoot(t, sandboxContainer), sandboxContainer, "open")
}

func loadUID(t *testing.T, d *localDriver, id string) int {
	t.Helper()
	wl, err := d.loadWorkload(map[string]any{"workload_id": id})
	if err != nil {
		t.Fatal(err)
	}
	if wl.UID < localUIDBase || wl.Sandbox != sandboxContainer {
		t.Fatalf("workload uid %d sandbox %q", wl.UID, wl.Sandbox)
	}
	return wl.UID
}

func TestContainerModeRunsAsItsOwnUser(t *testing.T) {
	d := requireContainerMode(t)
	t.Setenv("APTEVA_APP_TOKEN", "do-not-leak")
	secret := filepath.Join(filepath.Dir(d.root), "apteva.json")
	if err := os.WriteFile(secret, []byte("sk-root-only"), 0o600); err != nil {
		t.Fatal(err)
	}
	a := localRun(t, d, map[string]any{"name": "a", "env": map[string]string{"HOME": "/workspace/.home"}})
	b := localRun(t, d, map[string]any{"name": "b", "env": map[string]string{"HOME": "/workspace/.home"}})
	uidA, uidB := loadUID(t, d, a), loadUID(t, d, b)
	if uidA == uidB {
		t.Fatalf("workspaces share uid %d", uidA)
	}

	run, logs := localExecWait(t, d, map[string]any{"workload_id": a, "timeout_s": 20, "argv": []string{"/bin/sh", "-c",
		"id -u; echo hi > /workspace/f.txt && cat /workspace/f.txt; env; cat " + secret + " && echo READ-SECRET; touch /etc/owned && echo WROTE-ETC; true"}})
	if run.Status != "succeeded" {
		t.Fatalf("status %s: %s", run.Status, logs)
	}
	if !strings.Contains(logs, strconv.Itoa(uidA)+"\n") || !strings.Contains(logs, "hi\n") {
		t.Fatalf("not running as %d or cannot write its workspace:\n%s", uidA, logs)
	}
	for _, bad := range []string{"do-not-leak", "APTEVA_APP_TOKEN", "READ-SECRET", "sk-root-only", "WROTE-ETC"} {
		if strings.Contains(logs, bad) {
			t.Fatalf("workspace user saw %q:\n%s", bad, logs)
		}
	}

	// b cannot read a's files or list a's folder.
	_, other := localExecWait(t, d, map[string]any{"workload_id": b, "argv": []string{"/bin/sh", "-c",
		"cat " + filepath.Join(d.workspaceDir(a), "f.txt") + " && echo CROSS-READ; ls " + filepath.Join(d.workloadDir(a), "exec") + " && echo CROSS-LIST; true"}})
	if strings.Contains(other, "CROSS-") {
		t.Fatalf("workspace b reached workspace a:\n%s", other)
	}
}

func TestContainerModeFileTransfersBelongToTheUserAndIgnoreSymlinks(t *testing.T) {
	d := requireContainerMode(t)
	id := localRun(t, d, map[string]any{"name": "t"})
	uid := loadUID(t, d, id)
	if err := d.Call("containers_volume_import", map[string]any{"workload_id": id, "volume": "workspace", "path": "src", "archive_base64": tarGz(t, map[string]string{"pkg/a.go": "package a\n"}, nil)}, nil); err != nil {
		t.Fatal(err)
	}
	for _, rel := range []string{"src", "src/pkg", "src/pkg/a.go"} {
		info, err := os.Lstat(filepath.Join(d.workspaceDir(id), rel))
		if err != nil {
			t.Fatal(err)
		}
		if st := info.Sys().(*syscall.Stat_t); int(st.Uid) != uid {
			t.Fatalf("%s owned by %d, want %d", rel, st.Uid, uid)
		}
	}
	// The user can now edit what Workspaces wrote.
	run, logs := localExecWait(t, d, map[string]any{"workload_id": id, "argv": []string{"/bin/sh", "-c", "echo more >> /workspace/src/pkg/a.go && rm -rf /workspace/src/pkg && echo ok"}})
	if run.Status != "succeeded" || !strings.Contains(logs, "ok") {
		t.Fatalf("user cannot edit imported files: %s %s", run.Status, logs)
	}

	// A planted symlink to a root-only folder must not redirect an import
	// or leak through an export.
	target := filepath.Join(filepath.Dir(d.root), "rootonly")
	if err := os.MkdirAll(target, 0o700); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(target, "key"), []byte("sk-secret"), 0o600)
	if r, l := localExecWait(t, d, map[string]any{"workload_id": id, "argv": []string{"/bin/sh", "-c", "ln -s " + target + " /workspace/evil && ln -s " + target + "/key /workspace/key"}}); r.Status != "succeeded" {
		t.Fatalf("plant symlinks: %s", l)
	}
	_ = d.Call("containers_volume_import", map[string]any{"workload_id": id, "volume": "workspace", "path": ".", "archive_base64": tarGz(t, map[string]string{"evil/x": "pwned"}, nil)}, nil)
	if _, err := os.Stat(filepath.Join(target, "x")); err == nil {
		t.Fatal("import followed a symlink out of the workspace")
	}
	var exported struct {
		ArchiveBase64 string `json:"archive_base64"`
	}
	if err := d.Call("containers_volume_export", map[string]any{"workload_id": id, "volume": "workspace", "path": "."}, &exported); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(untarText(t, exported.ArchiveBase64), "sk-secret") {
		t.Fatal("export followed a symlink out of the workspace")
	}
}

func TestContainerModeStopKillsEscapedProcesses(t *testing.T) {
	d := requireContainerMode(t)
	id := localRun(t, d, map[string]any{"name": "t"})
	uid := loadUID(t, d, id)
	var started executionResponse
	if err := d.Call("containers_exec_start", map[string]any{"workload_id": id, "argv": []string{"/bin/sh", "-c", "setsid sleep 300 & sleep 300 & sleep 300"}}, &started); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for len(pidsOfUID(uid)) < 3 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if n := len(pidsOfUID(uid)); n < 3 {
		t.Fatalf("expected 3 processes, saw %d", n)
	}
	if err := d.Call("containers_stop", map[string]any{"workload_id": id}, nil); err != nil {
		t.Fatal(err)
	}
	deadline = time.Now().Add(5 * time.Second)
	for len(pidsOfUID(uid)) > 0 && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if left := pidsOfUID(uid); len(left) > 0 {
		t.Fatalf("processes survived stop: %v", left)
	}
}

func untarText(t *testing.T, encoded string) string {
	t.Helper()
	raw, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	gz, err := gzip.NewReader(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	var out strings.Builder
	tr := tar.NewReader(gz)
	for {
		header, err := tr.Next()
		if err != nil {
			break
		}
		out.WriteString(header.Name + " -> " + header.Linkname + "\n")
		body, _ := io.ReadAll(tr)
		out.Write(body)
	}
	return out.String()
}
