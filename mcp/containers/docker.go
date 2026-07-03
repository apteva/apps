package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os/exec"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type DockerBackend interface {
	Probe(ctx context.Context) error
	CreateNetwork(ctx context.Context, name string) error
	CreateVolume(ctx context.Context, name string) error
	Run(ctx context.Context, spec RunSpec, containerName, networkName string) (string, error)
	Start(ctx context.Context, containerName string) error
	Stop(ctx context.Context, containerName string) error
	Restart(ctx context.Context, containerName string) error
	Remove(ctx context.Context, containerName string, force bool) error
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
}

type ContainerState struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Running bool   `json:"running"`
	Health  string `json:"health"`
}

func (d LocalDocker) Probe(ctx context.Context) error {
	_, err := docker(ctx, "version", "--format", "{{.Server.Version}}")
	return err
}

func (d LocalDocker) CreateNetwork(ctx context.Context, name string) error {
	if _, err := docker(ctx, "network", "inspect", name); err == nil {
		return nil
	}
	_, err := docker(ctx, "network", "create", name)
	return err
}

func (d LocalDocker) CreateVolume(ctx context.Context, name string) error {
	if _, err := docker(ctx, "volume", "inspect", name); err == nil {
		return nil
	}
	_, err := docker(ctx, "volume", "create", name)
	return err
}

func (d LocalDocker) Run(ctx context.Context, spec RunSpec, containerName, networkName string) (string, error) {
	args := []string{"run", "-d", "--name", containerName, "--restart", spec.RestartPolicy, "--network", networkName}
	for _, p := range spec.Ports {
		hostPort := p.HostPort
		if hostPort == 0 {
			allocated, err := freePort()
			if err != nil {
				return "", err
			}
			hostPort = allocated
		}
		args = append(args, "-p", fmt.Sprintf("%s:%d:%d/%s", p.BindAddr, hostPort, p.ContainerPort, p.Protocol))
	}
	for k, v := range spec.Env {
		if !validEnvKey(k) {
			return "", fmt.Errorf("invalid env key %q", k)
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
	args = append(args, spec.Image)
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
	return docker(ctx, "logs", "--tail", strconv.Itoa(tail), containerName)
}

func (d LocalDocker) Inspect(ctx context.Context, containerName string) (*ContainerState, error) {
	raw, err := docker(ctx, "inspect", containerName)
	if err != nil {
		return nil, err
	}
	var arr []struct {
		ID    string `json:"Id"`
		State struct {
			Status  string `json:"Status"`
			Running bool   `json:"Running"`
			Health  *struct {
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
	st := &ContainerState{ID: arr[0].ID, Status: arr[0].State.Status, Running: arr[0].State.Running}
	if arr[0].State.Health != nil {
		st.Health = arr[0].State.Health.Status
	}
	return st, nil
}

func (d RemoteDocker) Probe(ctx context.Context) error {
	_, err := d.remoteDocker(ctx, 15, "version", "--format", "{{.Server.Version}}")
	return err
}

func (d RemoteDocker) CreateNetwork(ctx context.Context, name string) error {
	if err := d.ensureDocker(ctx); err != nil {
		return err
	}
	cmd := "docker network inspect " + shellQuote(name) + " >/dev/null 2>&1 || docker network create " + shellQuote(name)
	_, _, err := d.runRemote(ctx, cmd, 30)
	return err
}

func (d RemoteDocker) CreateVolume(ctx context.Context, name string) error {
	if err := d.ensureDocker(ctx); err != nil {
		return err
	}
	cmd := "docker volume inspect " + shellQuote(name) + " >/dev/null 2>&1 || docker volume create " + shellQuote(name)
	_, _, err := d.runRemote(ctx, cmd, 30)
	return err
}

func (d RemoteDocker) Run(ctx context.Context, spec RunSpec, containerName, networkName string) (string, error) {
	args := []string{"run", "-d", "--name", containerName, "--restart", spec.RestartPolicy, "--network", networkName}
	for _, p := range spec.Ports {
		hostPort := p.HostPort
		if hostPort == 0 {
			allocated, err := freePort()
			if err != nil {
				return "", err
			}
			hostPort = allocated
		}
		args = append(args, "-p", fmt.Sprintf("%s:%d:%d/%s", p.BindAddr, hostPort, p.ContainerPort, p.Protocol))
	}
	for k, v := range spec.Env {
		if !validEnvKey(k) {
			return "", fmt.Errorf("invalid env key %q", k)
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
	args = append(args, spec.Image)
	out, err := d.remoteDocker(ctx, 120, args...)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(out), nil
}

func (d RemoteDocker) Start(ctx context.Context, containerName string) error {
	_, err := d.remoteDocker(ctx, 30, "start", containerName)
	return err
}

func (d RemoteDocker) Stop(ctx context.Context, containerName string) error {
	_, err := d.remoteDocker(ctx, 30, "stop", "-t", "10", containerName)
	return err
}

func (d RemoteDocker) Restart(ctx context.Context, containerName string) error {
	_, err := d.remoteDocker(ctx, 45, "restart", "-t", "10", containerName)
	return err
}

func (d RemoteDocker) Remove(ctx context.Context, containerName string, force bool) error {
	args := []string{"rm"}
	if force {
		args = append(args, "-f")
	}
	args = append(args, containerName)
	_, err := d.remoteDocker(ctx, 45, args...)
	return err
}

func (d RemoteDocker) RemoveNetwork(ctx context.Context, name string) error {
	_, err := d.remoteDocker(ctx, 30, "network", "rm", name)
	return err
}

func (d RemoteDocker) RemoveVolume(ctx context.Context, name string) error {
	_, err := d.remoteDocker(ctx, 30, "volume", "rm", name)
	return err
}

func (d RemoteDocker) VolumeUsage(ctx context.Context, name string) (int64, error) {
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

func (d RemoteDocker) Logs(ctx context.Context, containerName string, tail int) (string, error) {
	if tail <= 0 || tail > 2000 {
		tail = 200
	}
	return d.remoteDocker(ctx, 30, "logs", "--tail", strconv.Itoa(tail), containerName)
}

func (d RemoteDocker) Inspect(ctx context.Context, containerName string) (*ContainerState, error) {
	raw, err := d.remoteDocker(ctx, 30, "inspect", containerName)
	if err != nil {
		return nil, err
	}
	var arr []struct {
		ID    string `json:"Id"`
		State struct {
			Status  string `json:"Status"`
			Running bool   `json:"Running"`
			Health  *struct {
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
	st := &ContainerState{ID: arr[0].ID, Status: arr[0].State.Status, Running: arr[0].State.Running}
	if arr[0].State.Health != nil {
		st.Health = arr[0].State.Health.Status
	}
	return st, nil
}

func (d RemoteDocker) remoteDocker(ctx context.Context, timeoutS int, args ...string) (string, error) {
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

func (d RemoteDocker) ensureDocker(ctx context.Context) error {
	_, _, err := d.runRemote(ctx, remoteDockerBootstrapScript, 360)
	if err != nil {
		return fmt.Errorf("bootstrap remote docker: %w", err)
	}
	return nil
}

func (d RemoteDocker) runRemote(ctx context.Context, cmd string, timeoutS int) (string, int, error) {
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
	var resp result
	done := make(chan error, 1)
	go func() {
		done <- d.app.PlatformAPI().CallAppResult("instances", "instance_run_command", map[string]any{
			"id":        d.instanceID,
			"cmd":       cmd,
			"timeout_s": timeoutS,
		}, &resp)
	}()
	select {
	case <-ctx.Done():
		return resp.Output, resp.ExitCode, ctx.Err()
	case err := <-done:
		if err != nil {
			return resp.Output, resp.ExitCode, fmt.Errorf("instance_run_command instance_id=%d: %w", d.instanceID, err)
		}
		if resp.Err != "" {
			return resp.Output, resp.ExitCode, errors.New(resp.Err)
		}
		if resp.ExitCode != 0 {
			return resp.Output, resp.ExitCode, fmt.Errorf("remote command exited %d: %s", resp.ExitCode, strings.TrimSpace(resp.Output))
		}
		return resp.Output, resp.ExitCode, nil
	}
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
    $SUDO env DEBIAN_FRONTEND=noninteractive apt-get install -y docker.io || (
      $SUDO env DEBIAN_FRONTEND=noninteractive apt-get install -y ca-certificates curl &&
      curl -fsSL https://get.docker.com | $SUDO sh
    )
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
	start := time.Now()
	log.Printf("[containers] docker start args=%s", redactDockerArgs(args))
	cmd := exec.CommandContext(ctx, "docker", args...)
	var out, stderr bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = &stderr
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

func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
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
