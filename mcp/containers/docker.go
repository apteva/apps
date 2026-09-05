package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type DockerBackend interface {
	Probe(ctx context.Context) error
	EnsureNetwork(ctx context.Context, name string) (bool, error)
	EnsureVolume(ctx context.Context, name string) (bool, error)
	WriteVolumeFile(ctx context.Context, volumeName, relPath string, content []byte, mode string) error
	Run(ctx context.Context, spec RunSpec, containerName, networkName string) (string, error)
	Start(ctx context.Context, containerName string) error
	Stop(ctx context.Context, containerName string) error
	Restart(ctx context.Context, containerName string) error
	Remove(ctx context.Context, containerName string, force bool) error
	RemoveManagedContainer(ctx context.Context, containerName, workloadID string) error
	RemoveNetwork(ctx context.Context, name string) error
	RemoveVolume(ctx context.Context, name string) error
	VolumeUsage(ctx context.Context, name string) (int64, error)
	Logs(ctx context.Context, containerName string, tail int) (string, error)
	Inspect(ctx context.Context, containerName string) (*ContainerState, error)
}

type LocalDocker struct{}

type RemoteDocker struct {
	app        *sdk.AppCtx
	instanceID int64
	ensureOnce sync.Once
	ensureErr  error
}

const (
	maxDockerOutputBytes = 2 << 20
	maxDockerErrorBytes  = 256 << 10
)

type ContainerState struct {
	ID       string `json:"id"`
	Status   string `json:"status"`
	Running  bool   `json:"running"`
	Health   string `json:"health"`
	ExitCode int    `json:"exit_code"`
}

func (d LocalDocker) Probe(ctx context.Context) error {
	_, err := docker(ctx, "version", "--format", "{{.Server.Version}}")
	return err
}

func (d LocalDocker) EnsureNetwork(ctx context.Context, name string) (bool, error) {
	if _, err := docker(ctx, "network", "inspect", name); err == nil {
		return false, nil
	}
	_, err := docker(ctx, "network", "create", name)
	return err == nil, err
}

func (d LocalDocker) EnsureVolume(ctx context.Context, name string) (bool, error) {
	if _, err := docker(ctx, "volume", "inspect", name); err == nil {
		return false, nil
	}
	_, err := docker(ctx, "volume", "create", name)
	return err == nil, err
}

func (d LocalDocker) WriteVolumeFile(ctx context.Context, volumeName, relPath string, content []byte, mode string) error {
	return d.WriteOwnedVolumeFile(ctx, volumeName, relPath, content, mode, "0:0")
}

func (d LocalDocker) Run(ctx context.Context, spec RunSpec, containerName, networkName string) (string, error) {
	args, err := dockerRunArgs(spec, containerName, networkName)
	if err != nil {
		return "", err
	}
	out, err := docker(ctx, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (d LocalDocker) Start(ctx context.Context, containerName string) error {
	_, err := docker(ctx, "start", containerName)
	return err
}

func (d LocalDocker) Stop(ctx context.Context, containerName string) error {
	_, err := docker(ctx, "stop", "-t", "10", containerName)
	return err
}

func (d LocalDocker) Restart(ctx context.Context, containerName string) error {
	_, err := docker(ctx, "restart", "-t", "10", containerName)
	return err
}

func (d LocalDocker) Remove(ctx context.Context, containerName string, force bool) error {
	args := []string{"rm"}
	if force {
		args = append(args, "-f")
	}
	args = append(args, containerName)
	_, err := docker(ctx, args...)
	return err
}

func (d LocalDocker) RemoveManagedContainer(ctx context.Context, containerName, workloadID string) error {
	owner, err := docker(ctx, "container", "inspect", "--format", `{{ index .Config.Labels "com.apteva.workload-id" }}`, containerName)
	if err != nil {
		return err
	}
	if strings.TrimSpace(owner) != workloadID {
		return nil
	}
	return d.Remove(ctx, containerName, true)
}

func (d LocalDocker) RemoveNetwork(ctx context.Context, name string) error {
	_, err := docker(ctx, "network", "rm", name)
	return err
}

func (d LocalDocker) RemoveVolume(ctx context.Context, name string) error {
	_, err := docker(ctx, "volume", "rm", name)
	return err
}

func (d LocalDocker) VolumeUsage(ctx context.Context, name string) (int64, error) {
	out, err := docker(ctx, "run", "--rm", "-v", name+":/volume:ro", "alpine:3.20", "sh", "-c", "du -sk /volume | awk '{print $1 * 1024}'")
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse volume usage for %s: %w", name, err)
	}
	return n, nil
}

func (d LocalDocker) Logs(ctx context.Context, containerName string, tail int) (string, error) {
	if tail <= 0 || tail > 2000 {
		tail = 200
	}
	return dockerCombined(ctx, "logs", "--tail", strconv.Itoa(tail), containerName)
}

func (d LocalDocker) Inspect(ctx context.Context, containerName string) (*ContainerState, error) {
	raw, err := docker(ctx, "container", "inspect", containerName)
	if err != nil {
		return nil, err
	}
	var arr []struct {
		ID    string `json:"Id"`
		State struct {
			Status   string `json:"Status"`
			Running  bool   `json:"Running"`
			ExitCode int    `json:"ExitCode"`
			Health   *struct {
				Status string `json:"Status"`
			} `json:"Health"`
		} `json:"State"`
	}
	if err := json.Unmarshal([]byte(raw), &arr); err != nil {
		return nil, err
	}
	if len(arr) == 0 {
		return nil, errors.New("container not found")
	}
	st := &ContainerState{ID: arr[0].ID, Status: arr[0].State.Status, Running: arr[0].State.Running, ExitCode: arr[0].State.ExitCode}
	if arr[0].State.Health != nil {
		st.Health = arr[0].State.Health.Status
	}
	return st, nil
}

func (d *RemoteDocker) Probe(ctx context.Context) error {
	_, err := d.remoteDocker(ctx, 15, "version", "--format", "{{.Server.Version}}")
	return err
}

func (d *RemoteDocker) EnsureNetwork(ctx context.Context, name string) (bool, error) {
	if err := d.ensureDocker(ctx); err != nil {
		return false, err
	}
	quoted := shellQuote(name)
	cmd := "if docker network inspect " + quoted + " >/dev/null 2>&1; then printf existing; else docker network create " + quoted + " >/dev/null && printf created; fi"
	out, _, err := d.runRemote(ctx, cmd, 30)
	return strings.TrimSpace(out) == "created", err
}

func (d *RemoteDocker) EnsureVolume(ctx context.Context, name string) (bool, error) {
	if err := d.ensureDocker(ctx); err != nil {
		return false, err
	}
	quoted := shellQuote(name)
	cmd := "if docker volume inspect " + quoted + " >/dev/null 2>&1; then printf existing; else docker volume create " + quoted + " >/dev/null && printf created; fi"
	out, _, err := d.runRemote(ctx, cmd, 30)
	return strings.TrimSpace(out) == "created", err
}

func (d *RemoteDocker) WriteVolumeFile(ctx context.Context, volumeName, relPath string, content []byte, mode string) error {
	return d.writeOwnedRemoteFile(ctx, volumeName, relPath, content, mode, "0:0")
}
func (d *RemoteDocker) writeOwnedRemoteFile(ctx context.Context, volumeName, relPath string, content []byte, mode, owner string) error {
	if d.app == nil || d.app.PlatformAPI() == nil {
		return errors.New("platform API unavailable")
	}
	if err := d.ensureDocker(ctx); err != nil {
		return err
	}
	hostDir := "/tmp/apteva-containers-files-" + newExecutionID()
	hostPath := hostDir + "/payload"
	if _, _, err := d.runRemote(ctx, "umask 077; mkdir "+shellQuote(hostDir), 15); err != nil {
		return err
	}
	defer func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		_, _, _ = d.runRemote(cleanupCtx, "rm -rf "+shellQuote(hostDir), 15)
	}()
	var uploadOut map[string]any
	if err := d.app.PlatformAPI().CallAppResult("instances", "instance_upload_file", map[string]any{
		"id":          d.instanceID,
		"path":        hostPath,
		"content_b64": base64.StdEncoding.EncodeToString(content),
	}, &uploadOut); err != nil {
		return fmt.Errorf("stage remote volume file: %w", err)
	}
	name := "containers-file-" + newExecutionID()
	script := "exec 0</payload\n" + ownedVolumeWriteScript
	cleanup := "docker rm -f " + shellQuote(name) + " >/dev/null 2>&1 || true"
	cmd := "trap " + shellQuote(cleanup) + " EXIT; " + shellJoin("docker", "run", "--rm", "--name", name, "--network", "none", "--memory", "64m", "--cpus", "1", "--pids-limit", "32", "-v", volumeName+":/target", "-v", hostPath+":/payload:ro", "alpine:3.20", "sh", "-c", script, "sh", relPath, mode, owner)
	_, _, err := d.runRemote(ctx, cmd, 120)
	if err != nil {
		return formatDockerError([]string{"run", "--rm", "-v", volumeName + ":/target", "-v", "<staged-file>:/payload:ro", "alpine:3.20", "sh", "-c", "<write-file>"}, err.Error())
	}
	return nil
}

func (d *RemoteDocker) Run(ctx context.Context, spec RunSpec, containerName, networkName string) (string, error) {
	args, err := dockerRunArgs(spec, containerName, networkName)
	if err != nil {
		return "", err
	}
	out, err := d.remoteDocker(ctx, 120, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func dockerRunArgs(spec RunSpec, containerName, networkName string) ([]string, error) {
	args := []string{"run", "-d", "--name", containerName, "--restart", spec.RestartPolicy, "--network", networkName}
	if spec.runtimeWorkloadID != "" {
		args = append(args, "--label", "com.apteva.workload-id="+spec.runtimeWorkloadID)
	}
	if spec.PullPolicy != "" {
		args = append(args, "--pull", spec.PullPolicy)
	}
	for _, p := range spec.Ports {
		hostPort := p.HostPort
		args = append(args, "-p", fmt.Sprintf("%s:%d:%d/%s", p.BindAddr, hostPort, p.ContainerPort, p.Protocol))
	}
	for k, v := range spec.Env {
		if !validEnvKey(k) {
			return nil, fmt.Errorf("invalid env key %q", k)
		}
		args = append(args, "-e", k+"="+v)
	}
	for _, v := range spec.Volumes {
		args = append(args, "-v", fmt.Sprintf("%s:%s", v.DockerVolumeName, v.MountPath))
	}
	if spec.Resources.MemoryMB > 0 {
		args = append(args, "--memory", strconv.Itoa(spec.Resources.MemoryMB)+"m")
	}
	if spec.Resources.CPU > 0 {
		args = append(args, "--cpus", strconv.FormatFloat(spec.Resources.CPU, 'f', -1, 64))
	}
	if spec.WorkingDirectory != "" {
		args = append(args, "--workdir", spec.WorkingDirectory)
	}
	if spec.User != "" {
		args = append(args, "--user", spec.User)
	}
	if strings.HasPrefix(spec.Image, "-") {
		return nil, errors.New("invalid Docker image")
	}
	args = append(args, "--log-driver", "local", "--log-opt", "max-size=10m", "--log-opt", "max-file=3", "--pids-limit", "512", "--", spec.Image)
	args = append(args, spec.Command...)
	return args, nil
}

func (d *RemoteDocker) Start(ctx context.Context, containerName string) error {
	_, err := d.remoteDocker(ctx, 30, "start", containerName)
	return err
}

func (d *RemoteDocker) Stop(ctx context.Context, containerName string) error {
	_, err := d.remoteDocker(ctx, 30, "stop", "-t", "10", containerName)
	return err
}

func (d *RemoteDocker) Restart(ctx context.Context, containerName string) error {
	_, err := d.remoteDocker(ctx, 45, "restart", "-t", "10", containerName)
	return err
}

func (d *RemoteDocker) Remove(ctx context.Context, containerName string, force bool) error {
	args := []string{"rm"}
	if force {
		args = append(args, "-f")
	}
	args = append(args, containerName)
	_, err := d.remoteDocker(ctx, 45, args...)
	return err
}

func (d *RemoteDocker) RemoveManagedContainer(ctx context.Context, containerName, workloadID string) error {
	owner, err := d.remoteDocker(ctx, 30, "container", "inspect", "--format", `{{ index .Config.Labels "com.apteva.workload-id" }}`, containerName)
	if err != nil {
		return err
	}
	if strings.TrimSpace(owner) != workloadID {
		return nil
	}
	return d.Remove(ctx, containerName, true)
}

func (d *RemoteDocker) RemoveNetwork(ctx context.Context, name string) error {
	_, err := d.remoteDocker(ctx, 30, "network", "rm", name)
	return err
}

func (d *RemoteDocker) RemoveVolume(ctx context.Context, name string) error {
	_, err := d.remoteDocker(ctx, 30, "volume", "rm", name)
	return err
}

func (d *RemoteDocker) VolumeUsage(ctx context.Context, name string) (int64, error) {
	out, err := d.remoteDocker(ctx, 120, "run", "--rm", "-v", name+":/volume:ro", "alpine:3.20", "sh", "-c", "du -sk /volume | awk '{print $1 * 1024}'")
	if err != nil {
		return 0, err
	}
	n, err := strconv.ParseInt(strings.TrimSpace(out), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("parse volume usage for %s: %w", name, err)
	}
	return n, nil
}

func (d *RemoteDocker) Logs(ctx context.Context, containerName string, tail int) (string, error) {
	if tail <= 0 || tail > 2000 {
		tail = 200
	}
	if err := d.ensureDocker(ctx); err != nil {
		return "", err
	}
	inspect := shellJoin("docker", "container", "inspect", containerName) + " >/dev/null 2>&1"
	logs := shellJoin("docker", "logs", "--tail", strconv.Itoa(tail), containerName) + " 2>&1"
	cmd := inspect + " && " + logs + " | tail -c " + strconv.Itoa(maxDockerOutputBytes)
	out, _, err := d.runRemote(ctx, cmd, 30)
	return out, err
}

func (d *RemoteDocker) Inspect(ctx context.Context, containerName string) (*ContainerState, error) {
	raw, err := d.remoteDocker(ctx, 30, "container", "inspect", containerName)
	if err != nil {
		return nil, err
	}
	var arr []struct {
		ID    string `json:"Id"`
		State struct {
			Status   string `json:"Status"`
			Running  bool   `json:"Running"`
			ExitCode int    `json:"ExitCode"`
			Health   *struct {
				Status string `json:"Status"`
			} `json:"Health"`
		} `json:"State"`
	}
	if err := json.Unmarshal([]byte(raw), &arr); err != nil {
		return nil, err
	}
	if len(arr) == 0 {
		return nil, errors.New("container not found")
	}
	st := &ContainerState{ID: arr[0].ID, Status: arr[0].State.Status, Running: arr[0].State.Running, ExitCode: arr[0].State.ExitCode}
	if arr[0].State.Health != nil {
		st.Health = arr[0].State.Health.Status
	}
	return st, nil
}

func (d *RemoteDocker) remoteDocker(ctx context.Context, timeoutS int, args ...string) (string, error) {
	if err := d.ensureDocker(ctx); err != nil {
		return "", err
	}
	cmd := shellJoin(append([]string{"docker"}, args...)...)
	out, _, err := d.runRemote(ctx, cmd, timeoutS)
	if err != nil {
		return out, formatDockerError(args, err.Error())
	}
	return out, nil
}

func (d *RemoteDocker) ensureDocker(ctx context.Context) error {
	d.ensureOnce.Do(func() {
		key := fmt.Sprintf("%p/%s/%d", d.app.AppDB(), d.app.CurrentProject(), d.instanceID)
		if d.app.AppDB() == nil {
			key = fmt.Sprintf("%p/%s/%d", d.app, d.app.CurrentProject(), d.instanceID)
		}
		remoteCapabilities.mu.Lock()
		ready := remoteCapabilities.ready[key]
		remoteCapabilities.mu.Unlock()
		if time.Since(ready) < time.Minute {
			return
		}
		_, _, err := d.runRemote(ctx, remoteDockerBootstrapScript, 360)
		if err != nil {
			d.ensureErr = fmt.Errorf("bootstrap remote docker: %w", err)
		} else {
			remoteCapabilities.mu.Lock()
			if remoteCapabilities.ready == nil {
				remoteCapabilities.ready = map[string]time.Time{}
			}
			for k, t := range remoteCapabilities.ready {
				if time.Since(t) > time.Minute {
					delete(remoteCapabilities.ready, k)
				}
			}
			remoteCapabilities.ready[key] = time.Now()
			remoteCapabilities.mu.Unlock()
		}
	})
	return d.ensureErr
}

func (d *RemoteDocker) runRemote(ctx context.Context, cmd string, timeoutS int) (string, int, error) {
	if err := ctx.Err(); err != nil {
		return "", 0, err
	}
	if d.app == nil || d.app.PlatformAPI() == nil {
		return "", 0, errors.New("platform API unavailable")
	}
	if d.instanceID <= 0 {
		return "", 0, errors.New("remote docker requires instance_id > 0")
	}
	type result struct {
		Output   string `json:"output"`
		ExitCode int    `json:"exit_code"`
		Err      string `json:"error"`
	}
	type response struct {
		result
		err error
	}
	done := make(chan response, 1)
	go func() {
		var res response
		res.err = d.app.PlatformAPI().CallAppResult("instances", "instance_run_command", map[string]any{"id": d.instanceID, "cmd": cmd, "timeout_s": timeoutS}, &res.result)
		done <- res
	}()
	// The Instances API has no cancellation operation. Wait for its bounded command
	// to settle before returning cancellation, so cleanup cannot race the command.
	res := <-done
	if ctx.Err() != nil {
		return "", 0, ctx.Err()
	}
	res.Output = truncateOutput(res.Output, maxDockerOutputBytes)
	if res.err != nil {
		return res.Output, res.ExitCode, fmt.Errorf("instance_run_command instance_id=%d: %w", d.instanceID, res.err)
	}
	if res.Err != "" {
		return res.Output, res.ExitCode, errors.New(res.Err)
	}
	if res.ExitCode != 0 {
		return res.Output, res.ExitCode, fmt.Errorf("remote command exited %d: %s", res.ExitCode, res.Output)
	}
	return res.Output, res.ExitCode, nil
}

const remoteDockerBootstrapScript = `set -eu
if command -v docker >/dev/null 2>&1 && docker version >/dev/null 2>&1; then
  exit 0
fi

SUDO=""
if [ "$(id -u)" != "0" ]; then
  if ! command -v sudo >/dev/null 2>&1; then
    echo "docker is not installed and sudo is unavailable" >&2
    exit 1
  fi
  SUDO="sudo"
fi

if ! command -v docker >/dev/null 2>&1; then
  if command -v apt-get >/dev/null 2>&1; then
    $SUDO env DEBIAN_FRONTEND=noninteractive apt-get update -y
    $SUDO env DEBIAN_FRONTEND=noninteractive apt-get install -y docker.io
  elif command -v dnf >/dev/null 2>&1; then
    $SUDO dnf install -y docker || $SUDO dnf install -y moby-engine
  elif command -v yum >/dev/null 2>&1; then
    $SUDO yum install -y docker
  elif command -v apk >/dev/null 2>&1; then
    $SUDO apk add --no-cache docker
    $SUDO rc-update add docker boot >/dev/null 2>&1 || true
  else
    echo "unsupported host: no apt-get, dnf, yum, or apk package manager found" >&2
    exit 1
  fi
fi

$SUDO systemctl enable --now docker >/dev/null 2>&1 ||
  $SUDO service docker start >/dev/null 2>&1 ||
  true

docker version >/dev/null 2>&1`

func docker(ctx context.Context, args ...string) (string, error) {
	return dockerWithInput(ctx, nil, args...)
}

func dockerWithInput(ctx context.Context, stdin []byte, args ...string) (string, error) {
	return dockerWithInputLimit(ctx, stdin, maxDockerOutputBytes, args...)
}

func dockerWithInputLimit(ctx context.Context, stdin []byte, stdoutLimit int, args ...string) (string, error) {
	start := time.Now()
	log.Printf("[containers] docker start args=%s", redactDockerArgs(args))
	cmd := exec.CommandContext(ctx, "docker", args...)
	out := newLimitedBuffer(stdoutLimit)
	stderr := newLimitedBuffer(maxDockerErrorBytes)
	cmd.Stdout = out
	cmd.Stderr = stderr
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = err.Error()
		}
		log.Printf("[containers] docker error args=%s duration=%s err=%q stderr=%q ctx_err=%v",
			redactDockerArgs(args), time.Since(start).Round(time.Millisecond), err.Error(), msg, ctx.Err())
		return out.String(), formatDockerError(args, msg)
	}
	log.Printf("[containers] docker ok args=%s duration=%s stdout_bytes=%d",
		redactDockerArgs(args), time.Since(start).Round(time.Millisecond), out.Len())
	return out.String(), nil
}

type limitedBuffer struct {
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func newLimitedBuffer(limit int) *limitedBuffer {
	return &limitedBuffer{limit: limit}
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	originalLen := len(p)
	remaining := b.limit - b.buf.Len()
	if remaining <= 0 {
		b.truncated = b.truncated || originalLen > 0
		return originalLen, nil
	}
	if len(p) > remaining {
		p = p[:remaining]
		b.truncated = true
	}
	_, _ = b.buf.Write(p)
	return originalLen, nil
}

func (b *limitedBuffer) String() string {
	out := b.buf.String()
	if b.truncated {
		out += "\n[output truncated]"
	}
	return out
}

func (b *limitedBuffer) Len() int { return b.buf.Len() }

func truncateOutput(value string, limit int) string {
	if len(value) <= limit {
		return value
	}
	return value[:limit] + "\n[output truncated]"
}

func formatDockerError(args []string, msg string) error {
	return fmt.Errorf("docker %s: %s", redactDockerArgs(args), msg)
}

func redactDockerArgs(args []string) string {
	out := append([]string(nil), args...)
	for i := 0; i < len(out); i++ {
		switch out[i] {
		case "-e", "--env":
			if i+1 < len(out) {
				key := strings.SplitN(out[i+1], "=", 2)[0]
				out[i+1] = key + "=<redacted>"
			}
		default:
			if strings.HasPrefix(out[i], "--env=") {
				key := strings.SplitN(strings.TrimPrefix(out[i], "--env="), "=", 2)[0]
				out[i] = "--env=" + key + "=<redacted>"
			}
		}
	}
	return strings.Join(out, " ")
}

func validEnvKey(k string) bool {
	if k == "" {
		return false
	}
	for i, r := range k {
		if !(r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z') || (i > 0 && r >= '0' && r <= '9')) {
			return false
		}
	}
	return true
}

func shellJoin(args ...string) string {
	parts := make([]string, 0, len(args))
	for _, arg := range args {
		parts = append(parts, shellQuote(arg))
	}
	return strings.Join(parts, " ")
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

func probeHTTP(ctx context.Context, url string) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 4096))
	if resp.StatusCode < 200 || resp.StatusCode >= 500 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}
