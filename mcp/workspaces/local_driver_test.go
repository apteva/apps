package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"encoding/base64"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestPathMapRewritesOnlyWholePaths(t *testing.T) {
	m := pathMap{pairs: [][2]string{{"/workspace", "/r/w"}, {"/cache", "/r/c"}}}
	cases := map[string]string{
		"/workspace":                   "/r/w",
		"/workspace/src":               "/r/w/src",
		"cd /workspace/src && go test": "cd /r/w/src && go test",
		"GOCACHE=/cache/go":            "GOCACHE=/r/c/go",
		"'/workspace'":                 "'/r/w'",
		"/workspaces":                  "/workspaces",
		"/home/workspace":              "/home/workspace",
		"/x/cache/y":                   "/x/cache/y",
		"a:/workspace:/cache":          "a:/r/w:/r/c",
	}
	for in, want := range cases {
		if got := m.toReal(in); got != want {
			t.Errorf("toReal(%q) = %q, want %q", in, got, want)
		}
	}
	if got := m.toVirtual("open /r/w/a.go: no such file"); got != "open /workspace/a.go: no such file" {
		t.Errorf("toVirtual = %q", got)
	}
}

func requireSandbox(t *testing.T) *localDriver {
	t.Helper()
	kind := detectSandbox()
	if kind == sandboxNone {
		t.Skip("no OS sandbox on this machine")
	}
	return newLocalDriver(testLocalRoot(t, kind), kind, "open")
}

func localRun(t *testing.T, d *localDriver, input map[string]any) string {
	t.Helper()
	var out workloadResponse
	if err := d.Call("containers_run", input, &out); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(out.Workload.ID, localWorkloadPrefix) {
		t.Fatalf("workload id %q", out.Workload.ID)
	}
	return out.Workload.ID
}

func localExecWait(t *testing.T, d *localDriver, input map[string]any) (ContainerExecution, string) {
	t.Helper()
	var started executionResponse
	if err := d.Call("containers_exec_start", input, &started); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		var got executionResponse
		if err := d.Call("containers_exec_get", map[string]any{"execution_id": started.ExecutionID}, &got); err != nil {
			t.Fatal(err)
		}
		if commandTerminal(got.Execution.Status) {
			var logs executionLogsResponse
			if err := d.Call("containers_exec_logs", map[string]any{"execution_id": started.ExecutionID, "tail": 50}, &logs); err != nil {
				t.Fatal(err)
			}
			return got.Execution, logs.Logs
		}
		if time.Now().After(deadline) {
			t.Fatalf("execution still %s", got.Execution.Status)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

func TestLocalDriverRunsCommandsInTheWorkspaceFolder(t *testing.T) {
	d := requireSandbox(t)
	id := localRun(t, d, map[string]any{"name": "t", "env": map[string]string{"HOME": "/workspace/.home", "GOCACHE": "/cache/go"}})
	exec, logs := localExecWait(t, d, map[string]any{
		"workload_id": id, "working_directory": "/workspace", "umask": "022", "timeout_s": 10,
		"argv": []string{"/bin/sh", "-c", `mkdir -p /workspace/src && echo hi > /workspace/src/a.txt && cat /workspace/src/a.txt && pwd && echo "$HOME $GOCACHE" && umask`},
	})
	if exec.Status != "succeeded" {
		t.Fatalf("status %s (%s): %s", exec.Status, exec.Error, logs)
	}
	for _, want := range []string{"hi\n", "/workspace\n", "/workspace/.home /cache/go\n", "0022"} {
		if !strings.Contains(logs, want) {
			t.Errorf("logs missing %q:\n%s", want, logs)
		}
	}
	if raw, _ := os.ReadFile(filepath.Join(d.workspaceDir(id), "src", "a.txt")); string(raw) != "hi\n" {
		t.Fatalf("file on disk = %q", raw)
	}
	failed, _ := localExecWait(t, d, map[string]any{"workload_id": id, "argv": []string{"/bin/sh", "-c", "exit 3"}})
	if failed.Status != "failed" || failed.ExitCode == nil || *failed.ExitCode != 3 {
		t.Fatalf("exit 3 gave %+v", failed)
	}
}

func TestLocalDriverSandboxConfinesWritesAndHidesSecrets(t *testing.T) {
	d := requireSandbox(t)
	home := t.TempDir()
	if err := os.MkdirAll(filepath.Join(home, ".ssh"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(home, ".ssh", "id_test"), []byte("secret"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	outside := filepath.Join(t.TempDir(), "escaped.txt")
	id := localRun(t, d, map[string]any{"name": "t"})
	exec, logs := localExecWait(t, d, map[string]any{"workload_id": id, "argv": []string{"/bin/sh", "-c", "echo x > " + outside}})
	if exec.Status == "succeeded" {
		t.Fatalf("write outside the workspace succeeded: %s", logs)
	}
	if _, err := os.Stat(outside); err == nil {
		t.Fatal("file outside the workspace was created")
	}
	exec, logs = localExecWait(t, d, map[string]any{"workload_id": id, "argv": []string{"/bin/cat", filepath.Join(home, ".ssh", "id_test")}})
	if exec.Status == "succeeded" || strings.Contains(logs, "secret") {
		t.Fatalf("secret readable: %s %s", exec.Status, logs)
	}
}

func TestLocalDriverCancelAndTimeout(t *testing.T) {
	d := requireSandbox(t)
	id := localRun(t, d, map[string]any{"name": "t"})
	var started executionResponse
	if err := d.Call("containers_exec_start", map[string]any{"workload_id": id, "argv": []string{"/bin/sh", "-c", "sleep 30"}}, &started); err != nil {
		t.Fatal(err)
	}
	if err := d.Call("containers_exec_cancel", map[string]any{"execution_id": started.ExecutionID}, &started); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		var got executionResponse
		_ = d.Call("containers_exec_get", map[string]any{"execution_id": started.ExecutionID}, &got)
		if got.Execution.Status == "cancelled" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("still %s after cancel", got.Execution.Status)
		}
		time.Sleep(50 * time.Millisecond)
	}
	timedOut, _ := localExecWait(t, d, map[string]any{"workload_id": id, "timeout_s": 1, "argv": []string{"/bin/sh", "-c", "sleep 30"}})
	if timedOut.Status != "timed_out" {
		t.Fatalf("timeout gave %s", timedOut.Status)
	}
	// Idempotency: the same key returns the same execution.
	var a, b executionResponse
	in := map[string]any{"workload_id": id, "idempotency_key": "k1", "argv": []string{"/bin/echo", "once"}}
	if err := d.Call("containers_exec_start", in, &a); err != nil {
		t.Fatal(err)
	}
	if err := d.Call("containers_exec_start", in, &b); err != nil {
		t.Fatal(err)
	}
	if a.ExecutionID != b.ExecutionID {
		t.Fatalf("idempotency key started two executions: %s %s", a.ExecutionID, b.ExecutionID)
	}
}

func tarGz(t *testing.T, entries map[string]string, links map[string]string) string {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for name, body := range entries {
		_ = tw.WriteHeader(&tar.Header{Name: name, Mode: 0o644, Size: int64(len(body)), Typeflag: tar.TypeReg})
		_, _ = tw.Write([]byte(body))
	}
	for name, target := range links {
		_ = tw.WriteHeader(&tar.Header{Name: name, Linkname: target, Typeflag: tar.TypeSymlink})
	}
	_ = tw.Close()
	_ = gz.Close()
	return base64.StdEncoding.EncodeToString(buf.Bytes())
}

func TestLocalDriverArchivesRoundTripAndRefuseSymlinkEscapes(t *testing.T) {
	d := newLocalDriver(t.TempDir(), sandboxNone, "open")
	id := localRun(t, d, map[string]any{"name": "t"})
	var out map[string]any
	if err := d.Call("containers_volume_import", map[string]any{"workload_id": id, "volume": "workspace", "path": "src", "archive_base64": tarGz(t, map[string]string{"pkg/a.go": "package a\n"}, nil)}, &out); err != nil {
		t.Fatal(err)
	}
	var exported struct {
		ArchiveBase64 string `json:"archive_base64"`
	}
	if err := d.Call("containers_volume_export", map[string]any{"workload_id": id, "volume": "workspace", "path": "src/pkg/a.go"}, &exported); err != nil {
		t.Fatal(err)
	}
	content, _, err := unpackTextFile(exported.ArchiveBase64, "src/pkg/a.go")
	if err != nil || string(content) != "package a\n" {
		t.Fatalf("round trip = %q, %v", content, err)
	}
	if err := d.Call("containers_volume_export", map[string]any{"workload_id": id, "volume": "workspace", "path": "."}, &exported); err != nil {
		t.Fatal(err)
	}
	if _, _, err := unpackTextFile(exported.ArchiveBase64, "src/pkg/a.go"); err != nil {
		t.Fatalf("root export: %v", err)
	}

	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(d.workspaceDir(id), "evil")); err != nil {
		t.Fatal(err)
	}
	err = d.Call("containers_volume_import", map[string]any{"workload_id": id, "volume": "workspace", "path": ".", "archive_base64": tarGz(t, map[string]string{"evil/x": "pwned"}, nil)}, &out)
	if err == nil {
		t.Fatal("import through a symlink succeeded")
	}
	if _, statErr := os.Stat(filepath.Join(outside, "x")); statErr == nil {
		t.Fatal("file written outside the workspace")
	}
	if err := d.Call("containers_volume_import", map[string]any{"workload_id": id, "volume": "workspace", "path": "evil", "archive_base64": tarGz(t, map[string]string{"y": "pwned"}, nil)}, &out); err == nil {
		t.Fatal("import into a symlinked dir succeeded")
	}
	if err := d.Call("containers_volume_export", map[string]any{"workload_id": id, "volume": "workspace", "path": "evil/z"}, &exported); err == nil {
		t.Fatal("export through a symlink succeeded")
	}
	if err := d.Call("containers_volume_import", map[string]any{"workload_id": id, "volume": "workspace", "path": ".", "archive_base64": tarGz(t, nil, map[string]string{"up": "../../x"})}, &out); err == nil {
		t.Fatal("escaping symlink entry accepted")
	}
	if err := d.Call("containers_destroy", map[string]any{"workload_id": id}, &out); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(d.workloadDir(id)); err == nil {
		t.Fatal("destroy left the folder")
	}
	if err := d.Call("containers_get", map[string]any{"workload_id": "lwk_../../etc"}, &out); err == nil {
		t.Fatal("malformed workload id accepted")
	}
}

func TestRuntimeCallRoutesByRef(t *testing.T) {
	for input, want := range map[string]bool{
		`{"driver":"local"}`:                   true,
		`{"workload_id":"lwk_0123"}`:           true,
		`{"execution_id":"lex_01_02"}`:         true,
		`{"workload_id":"wl_123"}`:             false,
		`{"execution_id":"exe_1","driver":""}`: false,
	} {
		var parsed map[string]any
		if err := json.Unmarshal([]byte(input), &parsed); err != nil {
			t.Fatal(err)
		}
		if got := ownedByLocal(parsed); got != want {
			t.Errorf("ownedByLocal(%s) = %v", input, got)
		}
	}
}

func TestLocalRootMustBeOutsideHiddenFolders(t *testing.T) {
	home := t.TempDir()
	real, _ := filepath.EvalSymlinks(home)
	if got := rootInsideHidden(filepath.Join(real, ".apteva", "apps", "data"), home); got != "~/.apteva" {
		t.Fatalf("root under ~/.apteva gave %q", got)
	}
	if got := rootInsideHidden(filepath.Join(real, "apteva-workspaces", "install-1"), home); got != "" {
		t.Fatalf("plain root flagged as %q", got)
	}
}

func TestLocalDriverCommandsResolveTheirDirectory(t *testing.T) {
	d := requireSandbox(t)
	id := localRun(t, d, map[string]any{"name": "t"})
	// env -u PWD forces getcwd, which needs every parent directory readable.
	exec, logs := localExecWait(t, d, map[string]any{"workload_id": id, "argv": []string{"/usr/bin/env", "-u", "PWD", "/bin/sh", "-c", "mkdir -p hello && cd hello && cd .. && /bin/pwd"}})
	if exec.Status != "succeeded" || !strings.Contains(logs, "/workspace") {
		t.Fatalf("getcwd: %s %s", exec.Status, logs)
	}
}
