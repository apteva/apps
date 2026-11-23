package main

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// shellInstances is a fake Instances app whose "host" is this machine: it runs
// commands with sh and reads/writes files directly, with the real app's 1 MiB
// output cap. Pointing RemoteDocker at it exercises the remote scripts,
// quoting, paging and transfers against real Docker.
type shellInstances struct {
	tk.BasePlatformClient
	mu       sync.Mutex
	commands []string
}

func (p *shellInstances) CallAppResult(appName, tool string, input map[string]any, out any) error {
	var result any
	switch tool {
	case "instance_run_command":
		cmd, _ := input["cmd"].(string)
		p.mu.Lock()
		p.commands = append(p.commands, cmd)
		p.mu.Unlock()
		raw, err := exec.Command("sh", "-c", cmd).CombinedOutput()
		if len(raw) > 1<<20 {
			raw = raw[:1<<20]
		}
		res := map[string]any{"output": string(raw), "exit_code": 0}
		if err != nil {
			code := 1
			if exit, ok := err.(*exec.ExitError); ok {
				code = exit.ExitCode()
			}
			res["exit_code"] = code
			res["error"] = "Process exited with status " + err.Error()
		}
		result = res
	case "instance_upload_file":
		path, _ := input["path"].(string)
		content, _ := base64.StdEncoding.DecodeString(input["content_b64"].(string))
		if err := os.WriteFile(path, content, 0o600); err != nil {
			return err
		}
		result = map[string]any{"bytes_written": len(content)}
	case "instance_download_file":
		path, _ := input["path"].(string)
		content, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		result = map[string]any{"content_b64": base64.StdEncoding.EncodeToString(content), "bytes": len(content)}
	default:
		result = map[string]any{}
	}
	raw, _ := json.Marshal(result)
	return json.Unmarshal(raw, out)
}

func (p *shellInstances) allCommands() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return strings.Join(p.commands, "\n")
}

func remoteTestDocker(t *testing.T) (*RemoteDocker, *shellInstances, string, context.Context) {
	t.Helper()
	if os.Getenv("RUN_CONTAINERS_TESTS") != "1" {
		t.Skip("set RUN_CONTAINERS_TESTS=1 to run Docker tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	t.Cleanup(cancel)
	platform := &shellInstances{}
	manifest := (&App{}).Manifest()
	remote := &RemoteDocker{app: sdk.NewAppCtxForTest(&manifest, nil, sdk.Config{}, platform, nil), instanceID: 7}
	name := "containers-test-remote-" + strings.ToLower(newExecutionID()[4:12])
	if _, err := docker(ctx, "run", "-d", "--name", name, "alpine:3.20", "sleep", "infinity"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = docker(context.Background(), "rm", "-f", name) })
	return remote, platform, name, ctx
}

func remoteRun(t *testing.T, ctx context.Context, d *RemoteDocker, container string, spec executionRuntimeSpec) *Execution {
	t.Helper()
	spec.ExecutionID = newExecutionID()
	spec.ContainerName = container
	var ready string
	spec.RuntimeReady = func(id string) error { ready = id; return nil }
	id, err := d.StartExecution(ctx, spec)
	if err != nil {
		t.Fatalf("start: %v", err)
	}
	if id != ready || !strings.HasPrefix(id, remoteExecPrefix) {
		t.Fatalf("runtime id %q, ready %q", id, ready)
	}
	return &Execution{ID: spec.ExecutionID, RuntimeContainerID: id, RuntimeContainerName: container, User: spec.User, StartedAt: nowUTC()}
}

func waitRemote(t *testing.T, ctx context.Context, d *RemoteDocker, e *Execution) *ContainerState {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		state, err := d.InspectExecution(ctx, e)
		if err != nil {
			t.Fatal(err)
		}
		if !state.Running {
			return state
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("remote run did not finish")
	return nil
}

func TestDockerRemoteRunEnvOutputAndExitCode(t *testing.T) {
	d, platform, container, ctx := remoteTestDocker(t)
	secret := "s3cret-value-" + newExecutionID()
	multi := "line one\nline 'two' $HOME `x`"
	// 1.5 MB of output spans several 600 KiB pages and the 1 MiB command cap.
	e := remoteRun(t, ctx, d, container, executionRuntimeSpec{
		User: "1000:1000", Umask: "0022", WorkingDirectory: "/tmp", MaxOutputBytes: 2 << 20,
		Env:  map[string]string{"API_KEY": secret, "MULTI": multi, "state_dir": "not-the-script's"},
		Argv: []string{"sh", "-c", `id -u; pwd; printf '%s\n' "$API_KEY"; printf '%s\n' "$MULTI"; i=0; while [ $i -lt 15000 ]; do printf '%099d\n' $i; i=$((i+1)); done; exit 3`},
	})
	state := waitRemote(t, ctx, d, e)
	if state.ExitCode != 3 {
		t.Fatalf("exit code %d", state.ExitCode)
	}
	out, total, truncated, err := d.ExecutionOutput(ctx, e)
	if err != nil {
		t.Fatal(err)
	}
	head := "1000\n/tmp\n" + secret + "\n" + multi + "\n"
	if !strings.HasPrefix(out, head) || truncated || total != len(out) || len(out) != len(head)+15000*100 {
		t.Fatalf("output: len=%d total=%d truncated=%v head=%q", len(out), total, truncated, out[:min(len(out), 120)])
	}
	if !strings.HasSuffix(out, strings.Repeat("0", 94)+"14999\n") {
		t.Fatalf("output tail %q", out[len(out)-120:])
	}
	logs, err := d.ExecutionLogs(ctx, e, 2)
	if err != nil || logs != strings.Repeat("0", 94)+"14998\n"+strings.Repeat("0", 94)+"14999\n" {
		t.Fatalf("logs %q %v", logs, err)
	}
	// Env values never reach the host's command line, and the env file is gone.
	if cmds := platform.allCommands(); strings.Contains(cmds, secret) || strings.Contains(cmds, "line 'two'") {
		t.Fatal("env value appeared in a remote command")
	}
	if out, _ := dockerCombined(ctx, "exec", container, "ls", "-a", executionControlDir(e.ID)); strings.Contains(out, "env") {
		t.Fatalf("env file left behind: %s", out)
	}
	if err := d.RemoveExecution(ctx, e); err != nil {
		t.Fatal(err)
	}
	if _, err := docker(ctx, "exec", container, "test", "!", "-e", executionControlDir(e.ID)); err != nil {
		t.Fatal("run folder not removed")
	}
}

func TestDockerRemoteRunCancelAndLostRunner(t *testing.T) {
	d, _, container, ctx := remoteTestDocker(t)
	sleepers := func() string {
		out, _ := dockerCombined(ctx, "exec", container, "sh", "-c", `for p in /proc/[0-9]*; do case "$(tr "\\0" " " < $p/cmdline 2>/dev/null)" in *300[1-4]*) echo $p;; esac; done`)
		return strings.TrimSpace(out)
	}
	waitGone := func(what string) {
		t.Helper()
		for i := 0; i < 50 && sleepers() != ""; i++ {
			time.Sleep(100 * time.Millisecond)
		}
		if s := sleepers(); s != "" {
			t.Fatalf("%s: processes survived: %s", what, s)
		}
	}

	e := remoteRun(t, ctx, d, container, executionRuntimeSpec{Argv: []string{"sh", "-c", "(setsid sleep 3001 &); sleep 3002 & wait"}})
	time.Sleep(time.Second)
	if state, err := d.InspectExecution(ctx, e); err != nil || !state.Running {
		t.Fatalf("not running: %+v %v", state, err)
	}
	if err := d.StopExecution(ctx, e); err != nil {
		t.Fatal(err)
	}
	waitGone("cancel")
	if state := waitRemote(t, ctx, d, e); state.Running {
		t.Fatal("still running after cancel")
	}

	// A runner killed from outside leaves no exit code: the run is reported as
	// exited 125 and whatever it started is killed.
	lost := remoteRun(t, ctx, d, container, executionRuntimeSpec{Argv: []string{"sh", "-c", "sleep 3003 & sleep 3004 & wait"}})
	time.Sleep(time.Second)
	if _, err := docker(ctx, "exec", container, "sh", "-c", "kill -9 $(cat "+executionControlDir(lost.ID)+"/runner)"); err != nil {
		t.Fatal(err)
	}
	if state := waitRemote(t, ctx, d, lost); state.ExitCode != 125 {
		t.Fatalf("lost runner exit %d", state.ExitCode)
	}
	waitGone("lost runner")

	// A container that is gone reports the run as exited rather than erroring.
	_, _ = docker(ctx, "rm", "-f", container)
	if state, err := d.InspectExecution(ctx, lost); err != nil || state.Running || state.ExitCode != 125 {
		t.Fatalf("missing container: %+v %v", state, err)
	}
}

func TestDockerRemoteVolumeArchiveRoundTrip(t *testing.T) {
	d, _, _, ctx := remoteTestDocker(t)
	vol := "containers-test-remote-vol-" + strings.ToLower(newExecutionID()[4:12])
	if _, err := docker(ctx, "volume", "create", vol); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = docker(context.Background(), "volume", "rm", "-f", vol) })
	archive := testTarGzip(t, []*tar.Header{
		{Name: "src/main.go", Typeflag: tar.TypeReg, Mode: 0o644, Size: 12, Uid: 501, Gid: 20},
	}, [][]byte{[]byte("package main")})
	if err := d.ImportVolumeArchive(ctx, vol, "app", archive, "1000:1000"); err != nil {
		t.Fatal(err)
	}
	out, err := docker(ctx, "run", "--rm", "-v", vol+":/v", "alpine:3.20", "stat", "-c", "%a %u:%g", "/v/app/src/main.go")
	if err != nil || strings.TrimSpace(out) != "644 1000:1000" {
		t.Fatalf("imported file: %q %v", out, err)
	}
	exported, err := d.exportVolumeArchiveLimited(ctx, vol, "app", 1<<20, defaultMaxExpandedBytes, defaultMaxArchiveFiles)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateTarGzip(exported, 1<<20, defaultMaxExpandedBytes, defaultMaxArchiveFiles); err != nil || !bytes.Contains(gunzipForTest(t, exported), []byte("package main")) {
		t.Fatalf("exported archive: %v", err)
	}
	// The size limit is checked on the host, before the transfer.
	if _, err := d.exportVolumeArchiveLimited(ctx, vol, "app", 10, defaultMaxExpandedBytes, defaultMaxArchiveFiles); err == nil || !strings.Contains(err.Error(), "archive exceeds 10 bytes") {
		t.Fatalf("small limit: %v", err)
	}
	// Helper containers and host temp folders are cleaned up.
	if out, _ := docker(ctx, "ps", "-a", "--filter", "name=containers-helper-", "-q"); strings.TrimSpace(out) != "" {
		t.Fatalf("helper containers left: %s", out)
	}
}

func gunzipForTest(t *testing.T, archive []byte) []byte {
	t.Helper()
	zr, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(zr)
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// The whole app path for a remote workload: start, supervision with remote
// back-off, output capture, and the session refusal.
func TestDockerRemoteExecutionThroughApp(t *testing.T) {
	_, platform, container, ctx := remoteTestDocker(t)
	db := testDB(t)
	manifest := (&App{}).Manifest()
	app := sdk.NewAppCtxForTest(&manifest, db, sdk.Config{}, platform, nil)
	a := &App{backend: LocalDocker{}}
	w := testWorkload("wrk_remote_"+strings.ToLower(newExecutionID()[4:12]), "remote", StatusRunning)
	w.InstanceID, w.ContainerName = 7, container
	if err := insertWorkload(db, w, nil, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := a.startExecution(ctx, app, w, ownerIdentity{}, executionInput{Argv: []string{"true"}, SessionKey: "s1"}); err == nil || !strings.Contains(err.Error(), "local workloads only") {
		t.Fatalf("remote session accepted: %v", err)
	}
	e, err := a.startExecution(ctx, app, w, ownerIdentity{}, executionInput{ShellCommand: `echo "out:$GREETING"; exit 4`, Env: map[string]string{"GREETING": "hello remote"}})
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		got, err := getExecution(db, e.ID)
		if err != nil {
			t.Fatal(err)
		}
		if executionTerminal(got.Status) {
			var output string
			_ = db.QueryRow(`SELECT output FROM containers_executions WHERE id=?`, e.ID).Scan(&output)
			if got.Status != executionFailed || got.ExitCode == nil || *got.ExitCode != 4 || output != "out:hello remote\n" || !strings.HasPrefix(got.RuntimeContainerID, remoteExecPrefix) {
				t.Fatalf("finished: %+v output=%q", got, output)
			}
			if strings.Contains(platform.allCommands(), "hello remote") {
				t.Fatal("env value appeared in a remote command")
			}
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatal("remote execution did not finish")
}

// Remote imports pause the workload on its host while copying, and resume it.
func TestDockerRemoteImportThroughAppPausesWorkload(t *testing.T) {
	_, platform, container, ctx := remoteTestDocker(t)
	db := testDB(t)
	manifest := (&App{}).Manifest()
	app := sdk.NewAppCtxForTest(&manifest, db, sdk.Config{}, platform, nil)
	a := &App{backend: LocalDocker{}}
	vol := "containers-test-remote-vol-" + strings.ToLower(newExecutionID()[4:12])
	if _, err := docker(ctx, "volume", "create", vol); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = docker(context.Background(), "volume", "rm", "-f", vol) })
	w := testWorkload("wrk_remote_"+strings.ToLower(newExecutionID()[4:12]), "remote-imp", StatusRunning)
	w.InstanceID, w.ContainerName = 7, container
	volumes := []VolumeSpec{{Name: "data", MountPath: "/data", DockerVolumeName: vol}}
	if err := insertWorkload(db, w, nil, volumes); err != nil {
		t.Fatal(err)
	}
	w.Volumes = volumes
	archive := testTarGzip(t, []*tar.Header{{Name: "f.txt", Typeflag: tar.TypeReg, Mode: 0o644, Size: 2}}, [][]byte{[]byte("hi")})
	res, err := a.importVolumeArchive(app, w, "data", "in", base64.StdEncoding.EncodeToString(archive), "")
	if err != nil {
		t.Fatal(err)
	}
	if res["compressed_bytes"] != len(archive) {
		t.Fatalf("result %+v", res)
	}
	cmds := platform.allCommands()
	if !strings.Contains(cmds, "'pause'") || !strings.Contains(cmds, "'unpause'") {
		t.Fatal("remote workload was not paused and resumed around the import")
	}
	if state, _ := docker(ctx, "inspect", "--format", "{{.State.Paused}}", container); strings.TrimSpace(state) != "false" {
		t.Fatalf("workload left paused: %q", state)
	}
	if out, err := docker(ctx, "run", "--rm", "-v", vol+":/v", "alpine:3.20", "cat", "/v/in/f.txt"); err != nil || out != "hi" {
		t.Fatalf("imported: %q %v", out, err)
	}
	var pending int
	_ = db.QueryRow(`SELECT COUNT(*) FROM containers_archive_pauses`).Scan(&pending)
	if pending != 0 {
		t.Fatal("pause record left behind")
	}
	// A configured limit above the Instances transfer cap is refused up front.
	big := sdk.NewAppCtxForTest(&manifest, db, sdk.Config{"max_archive_bytes": "20000000"}, platform, nil)
	if _, err := a.exportVolumeArchive(big, w, "data", "in"); err == nil || !strings.Contains(err.Error(), "remote hosts transfer at most") {
		t.Fatalf("over-limit export: %v", err)
	}
}
