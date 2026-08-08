package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"hash/fnv"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/gorilla/websocket"
)

const simulatorVersion = "0.1.25"

var remoteHostLocks sync.Map // map[int64]*sync.Mutex

type instanceSummary struct {
	ID       int64  `json:"id"`
	Name     string `json:"name"`
	Status   string `json:"status"`
	Provider string `json:"provider"`
}

type remoteWorkerClient struct {
	instanceID int64
	hostName   string
	targetPort int
	baseURL    string
	token      string
	http       *http.Client
}

type remoteBuildRequest struct {
	Framework       string   `json:"framework"`
	SourceTGZB64    string   `json:"source_tgz_b64"`
	DeviceID        string   `json:"device_id"`
	AndroidModule   string   `json:"android_module,omitempty"`
	IOSScheme       string   `json:"ios_scheme,omitempty"`
	BuildCmd        string   `json:"build_cmd,omitempty"`
	GradleExtra     []string `json:"gradle_extra_args,omitempty"`
	XcodeExtra      []string `json:"xcodebuild_extra_args,omitempty"`
	AllowedBuildEnv []string `json:"allowed_build_env,omitempty"`
}

type remoteBuildResult struct {
	ArtifactID string `json:"artifact_id"`
	BundleID   string `json:"bundle_id"`
	Activity   string `json:"activity"`
	LogID      string `json:"log_id"`
}

func hostIDArg(args map[string]any, key string) int64 {
	v := args[key]
	switch n := v.(type) {
	case int:
		return int64(n)
	case int64:
		return n
	case float64:
		return int64(n)
	case json.Number:
		i, _ := n.Int64()
		return i
	case string:
		i, _ := strconv.ParseInt(strings.TrimSpace(n), 10, 64)
		return i
	default:
		return 0
	}
}

func configuredHostID(ctx *sdk.AppCtx, platform string) int64 {
	if ctx == nil {
		return 0
	}
	key := platform + "_host_id"
	id, _ := strconv.ParseInt(strings.TrimSpace(ctx.Config().Get(key)), 10, 64)
	if id < 0 {
		return 0
	}
	return id
}

func resolvedHostID(ctx *sdk.AppCtx, args map[string]any, platform string) int64 {
	if _, present := args["host_id"]; present {
		return hostIDArg(args, "host_id")
	}
	return configuredHostID(ctx, platform)
}

func workerPortFor(instanceID int64) int {
	installID := strings.TrimSpace(os.Getenv("APTEVA_INSTALL_ID"))
	if installID == "" {
		installID = strings.TrimSpace(os.Getenv("APTEVA_APP_TOKEN"))
	}
	h := fnv.New32a()
	_, _ = h.Write([]byte(installID + ":" + strconv.FormatInt(instanceID, 10)))
	return 42000 + int(h.Sum32()%10000)
}

func workerNamespace() string {
	value := strings.TrimSpace(os.Getenv("APTEVA_INSTALL_ID"))
	if value == "" {
		value = "default"
	}
	if len(value) > 40 {
		value = value[:40]
	}
	return safeWorkerName(value)
}

func (a *App) ensureRemoteWorker(ctx *sdk.AppCtx, instanceID int64) (*remoteWorkerClient, error) {
	if instanceID <= 0 {
		return nil, errors.New("remote runner requires a positive Instances host id")
	}
	lockAny, _ := remoteHostLocks.LoadOrStore(instanceID, &sync.Mutex{})
	lock := lockAny.(*sync.Mutex)
	lock.Lock()
	defer lock.Unlock()

	inst, err := getInstance(ctx, instanceID)
	if err != nil {
		return nil, fmt.Errorf("runner_unavailable: Instances host %d: %w", instanceID, err)
	}
	if inst.Status != "ready" {
		return nil, fmt.Errorf("runner_unavailable: Instances host %d is %s", instanceID, inst.Status)
	}
	host, err := dbGetSimulatorHost(ctx.AppDB(), instanceID)
	if err != nil {
		return nil, err
	}
	if host == nil {
		token, tokenErr := randomToken(32)
		if tokenErr != nil {
			return nil, tokenErr
		}
		host = &SimulatorHost{
			InstanceID: instanceID, InstanceName: inst.Name,
			WorkerPort: workerPortFor(instanceID), WorkerToken: token, Status: "unknown",
		}
		if err := dbUpsertSimulatorHost(ctx.AppDB(), *host); err != nil {
			return nil, err
		}
	}
	client, err := a.openRemoteWorkerTunnel(ctx, host)
	if err == nil {
		if healthErr := client.health(context.Background()); healthErr == nil {
			_ = a.recordRemoteHealth(ctx, host, client)
			return client, nil
		}
	}
	if err := a.bootstrapRemoteWorker(ctx, host); err != nil {
		_ = dbUpsertSimulatorHost(ctx.AppDB(), SimulatorHost{
			InstanceID: host.InstanceID, InstanceName: inst.Name, WorkerPort: host.WorkerPort,
			WorkerToken: host.WorkerToken, Status: "error", Error: err.Error(),
		})
		return nil, fmt.Errorf("runner_unavailable: bootstrap worker on %s: %w", inst.Name, err)
	}
	client, err = a.openRemoteWorkerTunnel(ctx, host)
	if err != nil {
		return nil, fmt.Errorf("runner_unavailable: open worker tunnel: %w", err)
	}
	deadline := time.Now().Add(20 * time.Second)
	for {
		if err := client.health(context.Background()); err == nil {
			_ = a.recordRemoteHealth(ctx, host, client)
			return client, nil
		}
		if time.Now().After(deadline) {
			return nil, errors.New("runner_unavailable: remote worker did not become healthy within 20s")
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func getInstance(ctx *sdk.AppCtx, instanceID int64) (*instanceSummary, error) {
	var out struct {
		Instance instanceSummary `json:"instance"`
	}
	if err := ctx.PlatformAPI().CallAppResult("instances", "instance_get", map[string]any{"id": instanceID}, &out); err != nil {
		return nil, err
	}
	if out.Instance.ID != instanceID {
		return nil, fmt.Errorf("Instances returned host id %d", out.Instance.ID)
	}
	return &out.Instance, nil
}

func (a *App) openRemoteWorkerTunnel(ctx *sdk.AppCtx, host *SimulatorHost) (*remoteWorkerClient, error) {
	var out struct {
		LocalHost string `json:"local_host"`
		LocalPort int    `json:"local_port"`
	}
	err := ctx.PlatformAPI().CallAppResult("instances", "instance_open_tunnel", map[string]any{
		"id": host.InstanceID, "target_port": host.WorkerPort,
	}, &out)
	if err != nil {
		return nil, err
	}
	if out.LocalPort <= 0 || (out.LocalHost != "" && out.LocalHost != "127.0.0.1" && out.LocalHost != "localhost") {
		return nil, errors.New("Instances returned an invalid tunnel endpoint")
	}
	return &remoteWorkerClient{
		instanceID: host.InstanceID, hostName: host.InstanceName, targetPort: host.WorkerPort,
		baseURL: fmt.Sprintf("http://127.0.0.1:%d", out.LocalPort), token: host.WorkerToken,
		http: &http.Client{Timeout: 25 * time.Minute},
	}, nil
}

func (a *App) bootstrapRemoteWorker(ctx *sdk.AppCtx, host *SimulatorHost) error {
	home, err := runInstanceCommand(ctx, host.InstanceID, `printf '%s' "$HOME"`, 15)
	if err != nil {
		return fmt.Errorf("resolve remote home: %w", err)
	}
	home = strings.TrimSpace(home)
	if home == "" || !strings.HasPrefix(home, "/") {
		return fmt.Errorf("remote HOME is not absolute: %q", home)
	}
	base := filepath.Join(home, ".apteva", "simulator-workers", workerNamespace())
	configPath := filepath.Join(base, "config.json")
	binaryPath := strings.TrimSpace(ctx.Config().Get("remote_worker_binary_path"))
	if binaryPath == "" {
		binaryPath = filepath.Join(base, "bin", "simulator")
	} else if !filepath.IsAbs(binaryPath) {
		return errors.New("remote_worker_binary_path must be absolute")
	}
	cfg := workerConfig{
		Listen: fmt.Sprintf("127.0.0.1:%d", host.WorkerPort), Token: host.WorkerToken,
		DataDir: filepath.Join(base, "data"), Version: simulatorVersion,
		MaxSims: configuredMaxSims(ctx),
	}
	body, _ := json.Marshal(cfg)
	var uploaded struct {
		BytesWritten int `json:"bytes_written"`
	}
	if err := ctx.PlatformAPI().CallAppResult("instances", "instance_upload_file", map[string]any{
		"id": host.InstanceID, "path": configPath,
		"content_b64": base64.StdEncoding.EncodeToString(body),
	}, &uploaded); err != nil {
		return fmt.Errorf("upload worker config: %w", err)
	}
	if uploaded.BytesWritten != len(body) {
		return fmt.Errorf("worker config upload wrote %d of %d bytes", uploaded.BytesWritten, len(body))
	}
	moduleRef := strings.TrimSpace(ctx.Config().Get("remote_worker_module_ref"))
	if moduleRef == "" {
		moduleRef = "simulator/v" + simulatorVersion
	}
	if !validWorkerModuleRef(moduleRef) {
		return errors.New("remote_worker_module_ref contains invalid characters")
	}
	install := ""
	if strings.TrimSpace(ctx.Config().Get("remote_worker_binary_path")) == "" {
		resolveRef := "worker_ref=" + shellQuote(moduleRef) + "; "
		if strings.HasPrefix(moduleRef, "simulator/v") {
			// App releases use simulator/vX.Y.Z tags, while Go's nested-module
			// resolver expects mcp/simulator/vX.Y.Z. Resolve the app tag to its
			// immutable commit, then give that commit to go install.
			resolveRef = "command -v git >/dev/null 2>&1 || { echo 'Git is required to resolve the simulator release'; exit 127; }; " +
				"worker_ref=$(git ls-remote https://github.com/apteva/apps.git " + shellQuote("refs/tags/"+moduleRef+"^{}") + " | awk 'NR == 1 { print $1 }'); " +
				"[ -n \"$worker_ref\" ] || { echo 'Simulator worker release tag was not found'; exit 1; }; "
		}
		install = "command -v go >/dev/null 2>&1 || { echo 'Go is required to install simulator-worker'; exit 127; }; " + resolveRef +
			"mkdir -p " + shellQuote(filepath.Dir(binaryPath)) + "; " +
			"GOBIN=" + shellQuote(filepath.Dir(binaryPath)) + " go install github.com/apteva/apps/mcp/simulator@\"$worker_ref\"; "
	}
	command := "set -eu; chmod 600 " + shellQuote(configPath) + "; " + install +
		"test -x " + shellQuote(binaryPath) + "; " +
		"if [ -f " + shellQuote(filepath.Join(base, "worker.pid")) + "]; then " +
		"oldpid=$(cat " + shellQuote(filepath.Join(base, "worker.pid")) + " 2>/dev/null || true); " +
		"if [ -n \"$oldpid\" ] && kill -0 \"$oldpid\" 2>/dev/null; then kill \"$oldpid\" || true; sleep 1; fi; fi; " +
		"mkdir -p " + shellQuote(filepath.Join(base, "data")) + "; " +
		"nohup " + shellQuote(binaryPath) + " --worker " + shellQuote(configPath) +
		" >" + shellQuote(filepath.Join(base, "worker.log")) + " 2>&1 </dev/null & " +
		"echo $! >" + shellQuote(filepath.Join(base, "worker.pid"))
	_, err = runInstanceCommand(ctx, host.InstanceID, command, 180)
	return err
}

func configuredMaxSims(ctx *sdk.AppCtx) int {
	n, _ := strconv.Atoi(configOrDefault(ctx, "max_concurrent_sims"))
	if n <= 0 {
		return 2
	}
	if n > 16 {
		return 16
	}
	return n
}

func runInstanceCommand(ctx *sdk.AppCtx, instanceID int64, command string, timeoutSeconds int) (string, error) {
	var out struct {
		Output   string `json:"output"`
		ExitCode int    `json:"exit_code"`
		Error    string `json:"error"`
	}
	if err := ctx.PlatformAPI().CallAppResult("instances", "instance_run_command", map[string]any{
		"id": instanceID, "cmd": command, "timeout_s": timeoutSeconds,
	}, &out); err != nil {
		return out.Output, err
	}
	if out.ExitCode != 0 || out.Error != "" {
		return out.Output, fmt.Errorf("remote command exited %d: %s", out.ExitCode, strings.TrimSpace(out.Error+" "+out.Output))
	}
	return out.Output, nil
}

func shellQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", `'"'"'`) + "'"
}

func validWorkerModuleRef(value string) bool {
	if value == "" || len(value) > 128 {
		return false
	}
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' ||
			r == '.' || r == '_' || r == '-' || r == '/' {
			continue
		}
		return false
	}
	return true
}

func (a *App) recordRemoteHealth(ctx *sdk.AppCtx, host *SimulatorHost, client *remoteWorkerClient) error {
	var caps Capabilities
	if err := client.getJSON(context.Background(), "/v1/capabilities", &caps); err != nil {
		return err
	}
	body, _ := json.Marshal(caps)
	host.WorkerVersion = simulatorVersion
	host.CapabilitiesJSON = string(body)
	host.Status = "ready"
	host.LastSeenAt = time.Now().UTC().Format(time.RFC3339)
	host.Error = ""
	return dbUpsertSimulatorHost(ctx.AppDB(), *host)
}

func (c *remoteWorkerClient) health(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	var out struct {
		OK       bool   `json:"ok"`
		Protocol string `json:"protocol"`
		Version  string `json:"version"`
	}
	if err := c.getJSON(ctx, "/v1/health", &out); err != nil {
		return err
	}
	if !out.OK || out.Protocol != workerProtocolVersion {
		return fmt.Errorf("incompatible worker protocol %q", out.Protocol)
	}
	if out.Version != simulatorVersion {
		return fmt.Errorf("remote worker version %q, want %q", out.Version, simulatorVersion)
	}
	return nil
}

func (c *remoteWorkerClient) request(ctx context.Context, method, path string, input any) (*http.Response, error) {
	var body io.Reader
	if input != nil {
		encoded, err := json.Marshal(input)
		if err != nil {
			return nil, err
		}
		body = bytes.NewReader(encoded)
	}
	req, err := http.NewRequestWithContext(ctx, method, c.baseURL+path, body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	if input != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, fmt.Errorf("runner_unavailable: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		var payload struct {
			Error string `json:"error"`
		}
		_ = json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&payload)
		if payload.Error == "" {
			payload.Error = resp.Status
		}
		return nil, errors.New(payload.Error)
	}
	return resp, nil
}

func (c *remoteWorkerClient) getJSON(ctx context.Context, path string, out any) error {
	resp, err := c.request(ctx, http.MethodGet, path, nil)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out)
}

func (c *remoteWorkerClient) postJSON(ctx context.Context, path string, input, out any) error {
	resp, err := c.request(ctx, http.MethodPost, path, input)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if out == nil {
		drainAndClose(resp.Body)
		return nil
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out)
}

func (c *remoteWorkerClient) capabilities(ctx context.Context) (Capabilities, error) {
	var caps Capabilities
	err := c.getJSON(ctx, "/v1/capabilities", &caps)
	return caps, err
}

func (c *remoteWorkerClient) boot(ctx context.Context, platform, image, deviceType string, extraArgs []string) (*workerDevice, error) {
	var out struct {
		Device workerDevice `json:"device"`
	}
	err := c.postJSON(ctx, "/v1/devices/boot", map[string]any{
		"platform": platform, "image": image, "device_type": deviceType, "extra_args": extraArgs,
	}, &out)
	return &out.Device, err
}

func (c *remoteWorkerClient) build(ctx context.Context, req remoteBuildRequest) (*remoteBuildResult, error) {
	var out remoteBuildResult
	err := c.postJSON(ctx, "/v1/builds", req, &out)
	return &out, err
}

func (c *remoteWorkerClient) devicePost(ctx context.Context, backendID, action string, input any) error {
	return c.postJSON(ctx, "/v1/devices/"+url.PathEscape(backendID)+"/"+action, input, nil)
}

func (c *remoteWorkerClient) screenshot(ctx context.Context, backendID string) ([]byte, error) {
	resp, err := c.request(ctx, http.MethodGet, "/v1/devices/"+url.PathEscape(backendID)+"/screenshot", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(io.LimitReader(resp.Body, 64<<20))
}

func (c *remoteWorkerClient) logs(ctx context.Context, backendID string, lines int) (string, error) {
	var out struct {
		Content string `json:"content"`
	}
	err := c.getJSON(ctx, "/v1/devices/"+url.PathEscape(backendID)+"/logs?lines="+strconv.Itoa(lines), &out)
	return out.Content, err
}

func (c *remoteWorkerClient) stream(ctx context.Context, backendID string) (*websocket.Conn, error) {
	endpoint := strings.Replace(c.baseURL, "http://", "ws://", 1) + "/v1/devices/" + url.PathEscape(backendID) + "/stream"
	header := http.Header{"Authorization": []string{"Bearer " + c.token}}
	dialer := websocket.Dialer{HandshakeTimeout: 15 * time.Second}
	conn, resp, err := dialer.DialContext(ctx, endpoint, header)
	if resp != nil && resp.Body != nil {
		drainAndClose(resp.Body)
	}
	return conn, err
}
