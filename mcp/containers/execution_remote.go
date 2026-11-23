package main

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"
)

// Remote command runs use the same run script as local ones, sent through the
// Instances app. Every piece of a run's state (pid, output, byte count, exit
// code) lives in its folder inside the container, so status checks, output
// reads, cancellation and recovery after a sidecar restart only need that
// folder. Env values never appear on the host's command line: they travel in
// an uploaded private file that only the command's own subshell reads.

const remoteExecPrefix = "remote-exec:"

// remoteOutputPage keeps each base64 page under the 1 MiB output cap of
// instance_run_command.
const remoteOutputPage = 600 << 10

// remoteStartGrace bounds how long a run may sit before its runner records a
// pid; past it the run is treated as lost.
const remoteStartGrace = 2 * time.Minute

func remoteExecRuntimeID(executionID string) string { return remoteExecPrefix + executionID }

func remoteExecution(e *Execution) bool {
	return e != nil && strings.HasPrefix(e.RuntimeContainerID, remoteExecPrefix)
}

// remoteEnvScript renders env as shell assignments. Single quoting keeps every
// byte except NUL literal, and NUL is refused by input validation.
func remoteEnvScript(env map[string]string) string {
	var b strings.Builder
	for _, key := range envKeys(env) {
		b.WriteString("export " + key + "=" + shellQuote(env[key]) + "\n")
	}
	return b.String()
}

func dockerExecArgs(user, workdir string, flags []string, container string, argv ...string) []string {
	args := append([]string{"docker", "exec"}, flags...)
	if user != "" {
		args = append(args, "--user", user)
	}
	if workdir != "" {
		args = append(args, "--workdir", workdir)
	}
	return append(append(args, container), argv...)
}

func (d *RemoteDocker) remoteTempDir(ctx context.Context, kind string) (string, func(), error) {
	dir := "/tmp/apteva-containers-" + kind + "-" + newExecutionID()
	if _, _, err := d.runRemote(ctx, "umask 077; mkdir "+shellQuote(dir), 15); err != nil {
		return "", func() {}, err
	}
	return dir, func() {
		c, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, _, _ = d.runRemote(c, "rm -rf "+shellQuote(dir), 15)
	}, nil
}

func (d *RemoteDocker) uploadFile(path string, content []byte) error {
	var out map[string]any
	return d.app.PlatformAPI().CallAppResult("instances", "instance_upload_file", map[string]any{
		"id": d.instanceID, "path": path, "content_b64": base64.StdEncoding.EncodeToString(content),
	}, &out)
}

func (d *RemoteDocker) StartExecution(ctx context.Context, spec executionRuntimeSpec) (string, error) {
	if spec.ContainerName == "" || spec.ExecutionID == "" || len(spec.Argv) == 0 {
		return "", errors.New("persistent execution requires a container, execution id, and command")
	}
	if spec.SessionKey != "" {
		return "", errRemoteSession
	}
	if d.app == nil || d.app.PlatformAPI() == nil {
		return "", errors.New("platform API unavailable")
	}
	if err := d.ensureDocker(ctx); err != nil {
		return "", err
	}
	if err := prepareControlDirsWith(ctx, d.sandboxDocker(), spec); err != nil {
		return "", err
	}
	dir := executionControlDir(spec.ExecutionID)
	steps := []string{"set -e",
		// The run folder exists before the runner starts, so a status check can
		// tell "not started yet" from "gone".
		shellJoin(dockerExecArgs(spec.User, "", nil, spec.ContainerName, "/bin/sh", "-c", `umask 077; mkdir -p "$1"`, "sh", dir)...),
	}
	envFile := ""
	if len(spec.Env) > 0 {
		hostDir, cleanup, err := d.remoteTempDir(ctx, "exec")
		if err != nil {
			return "", err
		}
		defer cleanup()
		if err := d.uploadFile(hostDir+"/env", []byte(remoteEnvScript(spec.Env))); err != nil {
			return "", fmt.Errorf("stage remote execution env: %w", err)
		}
		envFile = dir + "/env"
		steps = append(steps, shellJoin(dockerExecArgs(spec.User, "", []string{"-i"}, spec.ContainerName, "/bin/sh", "-c", `umask 077; cat > "$1"`, "sh", envFile)...)+" < "+shellQuote(hostDir+"/env"))
	}
	runtimeID := remoteExecRuntimeID(spec.ExecutionID)
	if spec.RuntimeReady != nil {
		if err := spec.RuntimeReady(runtimeID); err != nil {
			return "", err
		}
	}
	run := []string{"/bin/sh", "-c", persistentExecutionScript, "apteva-exec", dir, strconv.Itoa(executionOutputLimit(spec.MaxOutputBytes)), spec.Umask, envFile}
	steps = append(steps, shellJoin(dockerExecArgs(spec.User, spec.WorkingDirectory, []string{"-d"}, spec.ContainerName, append(run, spec.Argv...)...)...))
	if out, _, err := d.runRemote(ctx, strings.Join(steps, "\n"), 60); err != nil {
		return "", fmt.Errorf("start remote execution: %s", strings.TrimSpace(firstNonEmpty(out, err.Error())))
	}
	return runtimeID, nil
}

const remoteInspectScript = `d="$1"
if [ -f "$d/exit_code" ]; then echo "exited $(cat "$d/exit_code")"; exit 0; fi
[ -d "$d" ] || { echo missing; exit 0; }
r=$(cat "$d/runner" 2>/dev/null || true)
case "$r" in ''|*[!0-9]*) echo starting; exit 0;; esac
case "$({ tr '\0' ' ' < "/proc/$r/cmdline"; } 2>/dev/null)" in
 *apteva-exec*) echo running;;
 *) if [ -f "$d/exit_code" ]; then echo "exited $(cat "$d/exit_code")"; else echo lost; fi;;
esac`

func (d *RemoteDocker) InspectExecution(ctx context.Context, e *Execution) (*ContainerState, error) {
	if !remoteExecution(e) {
		return nil, errors.New("remote execution has no runtime id")
	}
	out, err := d.remoteDocker(ctx, 30, execAs(e.User, e.RuntimeContainerName, "/bin/sh", "-c", remoteInspectScript, "sh", executionControlDir(e.ID))...)
	if dockerExecUnavailable(err) {
		return &ContainerState{ID: e.RuntimeContainerID, Status: "exited", ExitCode: 125}, nil
	}
	if err != nil {
		return nil, err
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	fields := strings.Fields(lines[len(lines)-1])
	if len(fields) == 0 {
		return nil, errors.New("remote execution status was empty")
	}
	switch fields[0] {
	case "exited":
		code := 125
		if len(fields) > 1 {
			if n, err := strconv.Atoi(fields[1]); err == nil {
				code = n
			}
		}
		return &ContainerState{ID: e.RuntimeContainerID, Status: "exited", ExitCode: code}, nil
	case "running":
		return &ContainerState{ID: e.RuntimeContainerID, Status: "running", Running: true}, nil
	case "starting":
		started, _ := time.Parse(time.RFC3339, e.StartedAt)
		if started.IsZero() || time.Since(started) < remoteStartGrace {
			return &ContainerState{ID: e.RuntimeContainerID, Status: "running", Running: true}, nil
		}
	case "missing", "lost":
	default:
		return nil, fmt.Errorf("unexpected remote execution status %q", out)
	}
	// The runner is gone without an exit code (container restarted, or killed
	// from outside). Kill anything it left behind, as for a vanished local exec.
	if err := d.StopExecution(ctx, e); err != nil {
		return nil, err
	}
	return &ContainerState{ID: e.RuntimeContainerID, Status: "exited", ExitCode: 125}, nil
}

func (d *RemoteDocker) StopExecution(ctx context.Context, e *Execution) error {
	if e == nil || e.RuntimeContainerName == "" {
		return nil
	}
	_, err := d.remoteDocker(ctx, 60, execAs(e.User, e.RuntimeContainerName, "/bin/sh", "-c", stopPersistentExecutionScript, "apteva-exec", executionControlDir(e.ID))...)
	if dockerExecUnavailable(err) {
		return nil
	}
	return err
}

func (d *RemoteDocker) RemoveExecution(ctx context.Context, e *Execution) error {
	if e == nil || e.RuntimeContainerName == "" {
		return nil
	}
	_, err := d.remoteDocker(ctx, 30, execAs(e.User, e.RuntimeContainerName, "rm", "-rf", executionControlDir(e.ID))...)
	if dockerExecUnavailable(err) {
		return nil
	}
	return err
}

// ExecutionLogs returns the last lines of output, base64-encoded on the host
// so binary output survives the JSON transport.
func (d *RemoteDocker) ExecutionLogs(ctx context.Context, e *Execution, tail int) (string, error) {
	if e == nil {
		return "", nil
	}
	if tail <= 0 || tail > 2000 {
		tail = 200
	}
	if err := d.ensureDocker(ctx); err != nil {
		return "", err
	}
	hostDir, cleanup, err := d.remoteTempDir(ctx, "logs")
	if err != nil {
		return "", err
	}
	defer cleanup()
	read := shellJoin(dockerExecArgs(e.User, "", nil, e.RuntimeContainerName, "/bin/sh", "-c", `if [ -f "$1/output" ]; then tail -n "$2" "$1/output"; fi`, "sh", executionControlDir(e.ID), strconv.Itoa(tail))...)
	cmd := "set -e\n" + read + " > " + shellQuote(hostDir+"/out") + "\n" +
		"tail -c " + strconv.Itoa(remoteOutputPage) + " " + shellQuote(hostDir+"/out") + " | base64 | tr -d '\\n'"
	out, _, err := d.runRemote(ctx, cmd, 30)
	if err != nil {
		return "", err
	}
	decoded, err := base64.StdEncoding.DecodeString(strings.TrimSpace(out))
	if err != nil {
		return "", fmt.Errorf("decode remote logs: %w", err)
	}
	return string(decoded), nil
}

// ExecutionOutput copies the run's output to a private host file once, then
// reads it in pages, so a run still writing cannot mix two versions.
func (d *RemoteDocker) ExecutionOutput(ctx context.Context, e *Execution) (string, int, bool, error) {
	if e == nil {
		return "", 0, false, nil
	}
	if err := d.ensureDocker(ctx); err != nil {
		return "", 0, false, err
	}
	hostDir, cleanup, err := d.remoteTempDir(ctx, "out")
	if err != nil {
		return "", 0, false, err
	}
	defer cleanup()
	file := shellQuote(hostDir + "/out")
	dir := executionControlDir(e.ID)
	snapshot := shellJoin(dockerExecArgs(e.User, "", nil, e.RuntimeContainerName, "/bin/sh", "-c", `cat "$1/bytes" 2>/dev/null || printf 0; printf '\n'; [ ! -f "$1/output" ] || cat "$1/output"`, "sh", dir)...)
	page := func(offset int) string {
		return "tail -c +" + strconv.Itoa(offset+1) + " " + file + " | head -c " + strconv.Itoa(remoteOutputPage) + " | base64 | tr -d '\\n'"
	}
	// The first call snapshots and returns the byte count, the snapshot size,
	// and the first page.
	first := "set -e\n" + snapshot + " > " + file + ".raw\n" +
		"head -n 1 " + file + ".raw\n" +
		"tail -n +2 " + file + ".raw > " + file + "\n" +
		"wc -c < " + file + "\n" + page(0)
	out, _, err := d.runRemote(ctx, first, 60)
	if err != nil {
		return "", 0, false, err
	}
	lines := strings.SplitN(out, "\n", 3)
	if len(lines) < 2 {
		return "", 0, false, fmt.Errorf("unexpected remote output header %q", truncateOutput(out, 200))
	}
	total, _ := strconv.Atoi(strings.TrimSpace(lines[0]))
	size, err := strconv.Atoi(strings.TrimSpace(lines[1]))
	if err != nil || size < 0 || size > maxDockerOutputBytes {
		return "", 0, false, fmt.Errorf("unexpected remote output size %q", lines[1])
	}
	encoded := ""
	if len(lines) == 3 {
		encoded = lines[2]
	}
	body, err := base64.StdEncoding.DecodeString(strings.TrimSpace(encoded))
	if err != nil {
		return "", 0, false, fmt.Errorf("decode remote output: %w", err)
	}
	for len(body) < size {
		more, _, err := d.runRemote(ctx, page(len(body)), 30)
		if err != nil {
			return "", 0, false, err
		}
		chunk, err := base64.StdEncoding.DecodeString(strings.TrimSpace(more))
		if err != nil {
			return "", 0, false, fmt.Errorf("decode remote output: %w", err)
		}
		if len(chunk) == 0 {
			return "", 0, false, errors.New("remote output ended early")
		}
		body = append(body, chunk...)
	}
	if total < len(body) {
		total = len(body)
	}
	return string(body), total, total > len(body), nil
}

var errRemoteSession = errors.New("persistent shell sessions (session_key) are supported on local workloads only")

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}
