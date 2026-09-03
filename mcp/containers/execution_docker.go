package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

const persistentExecutionScript = `set -eu
state_dir="$1"
shift
mkdir -p "$state_dir"
: > "$state_dir/output"
rm -f "$state_dir/pid" "$state_dir/process_group"
if command -v setsid >/dev/null 2>&1; then
  setsid "$@" > "$state_dir/output" 2>&1 &
  child=$!
  : > "$state_dir/process_group"
else
  "$@" > "$state_dir/output" 2>&1 &
  child=$!
fi
printf '%s\n' "$child" > "$state_dir/pid"
set +e
wait "$child"
exit $?
`

const stopPersistentExecutionScript = `state_dir="$1"
i=0
while [ ! -s "$state_dir/pid" ] && [ "$i" -lt 5 ]; do
  sleep 1
  i=$((i + 1))
done
[ -s "$state_dir/pid" ] || exit 0
pid=$(cat "$state_dir/pid")
if [ -f "$state_dir/process_group" ]; then
  kill -TERM -"$pid" 2>/dev/null || true
else
  kill -TERM "$pid" 2>/dev/null || true
fi
i=0
while kill -0 "$pid" 2>/dev/null && [ "$i" -lt 5 ]; do
  sleep 1
  i=$((i + 1))
done
if kill -0 "$pid" 2>/dev/null; then
  if [ -f "$state_dir/process_group" ]; then
    kill -KILL -"$pid" 2>/dev/null || true
  else
    kill -KILL "$pid" 2>/dev/null || true
  fi
fi
`

type dockerEngineConnection struct {
	client  *http.Client
	baseURL string
}

var dockerEngineState struct {
	sync.Mutex
	connection *dockerEngineConnection
}

func executionControlDir(executionID string) string {
	return "/tmp/.apteva-executions/" + executionID
}

func legacyExecutionContainer(execution *Execution) bool {
	return execution != nil && strings.HasPrefix(execution.RuntimeContainerName, "containers-exec-")
}

func (d LocalDocker) StartExecution(ctx context.Context, spec executionRuntimeSpec) (string, error) {
	if spec.ContainerName == "" || spec.ExecutionID == "" || len(spec.Argv) == 0 {
		return "", errors.New("persistent execution requires a container, execution id, and command")
	}
	cmd := []string{"/bin/sh", "-c", persistentExecutionScript, "apteva-exec", executionControlDir(spec.ExecutionID)}
	cmd = append(cmd, spec.Argv...)
	env := make([]string, 0, len(spec.Env))
	for _, key := range envKeys(spec.Env) {
		env = append(env, key+"="+spec.Env[key])
	}
	create := map[string]any{
		"AttachStdin":  false,
		"AttachStdout": false,
		"AttachStderr": false,
		"Tty":          false,
		"Cmd":          cmd,
		"Env":          env,
	}
	if spec.WorkingDirectory != "" {
		create["WorkingDir"] = spec.WorkingDirectory
	}
	if spec.User != "" {
		create["User"] = spec.User
	}
	var created struct {
		ID string `json:"Id"`
	}
	endpoint := "/containers/" + url.PathEscape(spec.ContainerName) + "/exec"
	if err := dockerEngineJSON(ctx, http.MethodPost, endpoint, create, &created); err != nil {
		return "", err
	}
	if created.ID == "" {
		return "", errors.New("Docker returned an empty exec id")
	}
	if err := dockerEngineJSON(ctx, http.MethodPost, "/exec/"+url.PathEscape(created.ID)+"/start", map[string]any{
		"Detach": true,
		"Tty":    false,
	}, nil); err != nil {
		return "", err
	}
	return created.ID, nil
}

func (d LocalDocker) InspectExecution(ctx context.Context, execution *Execution) (*ContainerState, error) {
	if legacyExecutionContainer(execution) {
		return d.Inspect(ctx, execution.RuntimeContainerName)
	}
	if execution == nil || execution.RuntimeContainerID == "" {
		return nil, errors.New("persistent execution has no Docker exec id")
	}
	var inspected struct {
		ID       string `json:"ID"`
		Running  bool   `json:"Running"`
		ExitCode int    `json:"ExitCode"`
	}
	if err := dockerEngineJSON(ctx, http.MethodGet, "/exec/"+url.PathEscape(execution.RuntimeContainerID)+"/json", nil, &inspected); err != nil {
		return nil, err
	}
	status := "exited"
	if inspected.Running {
		status = "running"
	}
	return &ContainerState{ID: inspected.ID, Status: status, Running: inspected.Running, ExitCode: inspected.ExitCode}, nil
}

func (d LocalDocker) StopExecution(ctx context.Context, execution *Execution) error {
	if execution == nil {
		return nil
	}
	if legacyExecutionContainer(execution) {
		_, err := docker(ctx, "kill", execution.RuntimeContainerName)
		return err
	}
	_, err := docker(ctx, "exec", execution.RuntimeContainerName, "/bin/sh", "-c", stopPersistentExecutionScript,
		"apteva-exec", executionControlDir(execution.ID))
	if dockerExecUnavailable(err) {
		return nil
	}
	return err
}

func (d LocalDocker) ExecutionLogs(ctx context.Context, execution *Execution, tail int) (string, error) {
	if execution == nil {
		return "", nil
	}
	if legacyExecutionContainer(execution) {
		return d.Logs(ctx, execution.RuntimeContainerName, tail)
	}
	if tail <= 0 || tail > 2000 {
		tail = 200
	}
	script := `if [ -f "$1/output" ]; then tail -n "$2" "$1/output"; fi`
	return docker(ctx, "exec", execution.RuntimeContainerName, "/bin/sh", "-c", script, "apteva-exec",
		executionControlDir(execution.ID), strconv.Itoa(tail))
}

func (d LocalDocker) RemoveExecution(ctx context.Context, execution *Execution) error {
	if execution == nil || execution.RuntimeContainerName == "" {
		return nil
	}
	if legacyExecutionContainer(execution) {
		_, err := docker(ctx, "rm", "-f", execution.RuntimeContainerName)
		return err
	}
	_, err := docker(ctx, "exec", execution.RuntimeContainerName, "rm", "-rf", executionControlDir(execution.ID))
	if dockerExecUnavailable(err) {
		return nil
	}
	return err
}

func dockerExecUnavailable(err error) bool {
	if err == nil {
		return false
	}
	msg := strings.ToLower(err.Error())
	return isDockerMissingResourceError(err, "container") || strings.Contains(msg, "is not running") ||
		strings.Contains(msg, "container is restarting")
}

func dockerEngineJSON(ctx context.Context, method, endpoint string, input, output any) error {
	connection, err := localDockerEngine(ctx)
	if err != nil {
		return err
	}
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, connection.baseURL+endpoint, body)
	if err != nil {
		return err
	}
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := connection.client.Do(req)
	if err != nil {
		return fmt.Errorf("Docker Engine API: %w", err)
	}
	defer resp.Body.Close()
	payload, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var engineErr struct {
			Message string `json:"message"`
		}
		_ = json.Unmarshal(payload, &engineErr)
		if engineErr.Message == "" {
			engineErr.Message = strings.TrimSpace(string(payload))
		}
		return fmt.Errorf("Docker Engine API %s %s: %s", method, endpoint, engineErr.Message)
	}
	if output != nil && len(payload) > 0 {
		if err := json.Unmarshal(payload, output); err != nil {
			return fmt.Errorf("decode Docker Engine API response: %w", err)
		}
	}
	return nil
}

func localDockerEngine(ctx context.Context) (*dockerEngineConnection, error) {
	dockerEngineState.Lock()
	defer dockerEngineState.Unlock()
	if dockerEngineState.connection != nil {
		return dockerEngineState.connection, nil
	}
	host := strings.TrimSpace(os.Getenv("DOCKER_HOST"))
	if host == "" {
		out, err := docker(ctx, "context", "inspect", "--format", "{{.Endpoints.docker.Host}}")
		if err == nil {
			host = strings.TrimSpace(out)
		}
	}
	if host == "" {
		host = "unix:///var/run/docker.sock"
	}
	if !strings.HasPrefix(host, "unix://") {
		return nil, fmt.Errorf("persistent execution requires a local Docker unix socket, got %q", host)
	}
	socketPath := strings.TrimPrefix(host, "unix://")
	if socketPath == "" {
		return nil, errors.New("Docker unix socket path is empty")
	}
	if !filepath.IsAbs(socketPath) {
		return nil, fmt.Errorf("Docker unix socket path must be absolute: %q", socketPath)
	}
	transport := &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{Timeout: 10 * time.Second}).DialContext(ctx, "unix", socketPath)
		},
	}
	client := &http.Client{Transport: transport, Timeout: 30 * time.Second}
	versionReq, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://docker/version", nil)
	if err != nil {
		return nil, err
	}
	resp, err := client.Do(versionReq)
	if err != nil {
		return nil, fmt.Errorf("connect to Docker Engine API: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Docker Engine version endpoint returned HTTP %d", resp.StatusCode)
	}
	var version struct {
		APIVersion string `json:"ApiVersion"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&version); err != nil {
		return nil, fmt.Errorf("decode Docker Engine version: %w", err)
	}
	if version.APIVersion == "" {
		return nil, errors.New("Docker Engine returned an empty API version")
	}
	connection := &dockerEngineConnection{client: client, baseURL: "http://docker/v" + version.APIVersion}
	dockerEngineState.connection = connection
	return connection, nil
}
