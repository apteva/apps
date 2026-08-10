package main

// simulator-worker is the host-side execution plane used by optional remote
// Instances runners. It intentionally has no app-sdk, database, or SSH access:
// the Simulator sidecar remains the control plane and reaches this loopback-
// only server through an Instances-owned tunnel.

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/gorilla/websocket"
)

const workerProtocolVersion = "1"

type workerConfig struct {
	Listen   string `json:"listen"`
	Token    string `json:"token"`
	DataDir  string `json:"data_dir"`
	Version  string `json:"version"`
	MaxSims  int    `json:"max_sims"`
	LogLevel string `json:"log_level,omitempty"`
}

type workerDevice struct {
	ID         string `json:"id"`
	Platform   string `json:"platform"`
	Runtime    string `json:"runtime"`
	DeviceType string `json:"device_type"`
	Status     string `json:"status"`
	PID        int64  `json:"pid,omitempty"`
	Serial     string `json:"serial,omitempty"`
	Error      string `json:"error,omitempty"`
	BootedAt   string `json:"booted_at,omitempty"`
}

type workerServer struct {
	cfg workerConfig
	app *App

	mu      sync.Mutex
	devices map[string]*workerDevice
	procs   map[string]*simProcess
}

func runWorker(configPath string) error {
	body, err := os.ReadFile(configPath)
	if err != nil {
		return fmt.Errorf("read worker config: %w", err)
	}
	var cfg workerConfig
	if err := json.Unmarshal(body, &cfg); err != nil {
		return fmt.Errorf("decode worker config: %w", err)
	}
	if strings.TrimSpace(cfg.Token) == "" {
		return errors.New("worker token is required")
	}
	if cfg.Version != "" && cfg.Version != simulatorVersion {
		return fmt.Errorf("worker config expects simulator %s, binary is %s", cfg.Version, simulatorVersion)
	}
	if cfg.Listen == "" {
		cfg.Listen = "127.0.0.1:48190"
	}
	host, _, err := net.SplitHostPort(cfg.Listen)
	if err != nil || (host != "127.0.0.1" && host != "localhost") {
		return errors.New("worker listen address must be loopback")
	}
	if cfg.DataDir == "" {
		return errors.New("worker data_dir is required")
	}
	if cfg.MaxSims <= 0 {
		cfg.MaxSims = 2
	}
	for _, dir := range []string{
		cfg.DataDir,
		filepath.Join(cfg.DataDir, "artifacts"),
		filepath.Join(cfg.DataDir, "sim-logs"),
		filepath.Join(cfg.DataDir, "boot-logs"),
	} {
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return err
		}
	}
	app := &App{
		dataDir:      cfg.DataDir,
		artifactsDir: filepath.Join(cfg.DataDir, "artifacts"),
		simLogsDir:   filepath.Join(cfg.DataDir, "sim-logs"),
		bootLogsDir:  filepath.Join(cfg.DataDir, "boot-logs"),
		streams:      map[string][]activeStream{},
		runLocks:     map[string]*sync.Mutex{},
	}
	w := &workerServer{
		cfg: cfg, app: app,
		devices: map[string]*workerDevice{},
		procs:   map[string]*simProcess{},
	}
	if err := w.loadDevices(); err != nil {
		return fmt.Errorf("load worker devices: %w", err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/health", w.auth(w.handleHealth))
	mux.HandleFunc("/v1/capabilities", w.auth(w.handleCapabilities))
	mux.HandleFunc("/v1/devices", w.auth(w.handleDevices))
	mux.HandleFunc("/v1/devices/boot", w.auth(w.handleBoot))
	mux.HandleFunc("/v1/devices/", w.auth(w.handleDevice))
	mux.HandleFunc("/v1/builds", w.auth(w.handleBuild))
	server := &http.Server{
		Addr:              cfg.Listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
		IdleTimeout:       90 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	return server.ListenAndServe()
}

func (w *workerServer) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(rw http.ResponseWriter, r *http.Request) {
		header := r.Header.Get("Authorization")
		if !strings.HasPrefix(header, "Bearer ") {
			http.Error(rw, "unauthorized", http.StatusUnauthorized)
			return
		}
		got := strings.TrimSpace(strings.TrimPrefix(header, "Bearer "))
		if got == "" || len(got) != len(w.cfg.Token) || subtle.ConstantTimeCompare([]byte(got), []byte(w.cfg.Token)) != 1 {
			http.Error(rw, "unauthorized", http.StatusUnauthorized)
			return
		}
		next(rw, r)
	}
}

func (w *workerServer) handleHealth(rw http.ResponseWriter, _ *http.Request) {
	writeJSON(rw, http.StatusOK, map[string]any{
		"ok": true, "protocol": workerProtocolVersion, "version": simulatorVersion,
	})
}

func (w *workerServer) handleCapabilities(rw http.ResponseWriter, _ *http.Request) {
	writeJSON(rw, http.StatusOK, probeCapabilities(nil))
}

func (w *workerServer) handleDevices(rw http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/v1/devices" || r.Method != http.MethodGet {
		writeErr(rw, http.StatusMethodNotAllowed, errBadMethod)
		return
	}
	w.mu.Lock()
	devices := make([]workerDevice, 0, len(w.devices))
	for _, device := range w.devices {
		w.refreshDeviceLocked(device)
		devices = append(devices, *device)
	}
	_ = w.saveDevicesLocked()
	w.mu.Unlock()
	writeJSON(rw, http.StatusOK, map[string]any{"devices": devices})
}

func (w *workerServer) handleBoot(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(rw, http.StatusMethodNotAllowed, errBadMethod)
		return
	}
	r.Body = http.MaxBytesReader(rw, r.Body, 64<<10)
	var req struct {
		Platform   string   `json:"platform"`
		Image      string   `json:"image"`
		DeviceType string   `json:"device_type"`
		ExtraArgs  []string `json:"extra_args"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(rw, http.StatusBadRequest, err)
		return
	}
	if req.Platform != "android" && req.Platform != "ios" {
		writeErr(rw, http.StatusBadRequest, errBadPlatform)
		return
	}
	w.mu.Lock()
	live := 0
	for _, device := range w.devices {
		if device.Platform == req.Platform && device.Runtime == req.Image &&
			device.DeviceType == req.DeviceType && device.Status == "booted" {
			copy := *device
			w.mu.Unlock()
			writeJSON(rw, http.StatusOK, map[string]any{"device": &copy})
			return
		}
		if device.Status == "booting" || device.Status == "booted" {
			live++
		}
	}
	w.mu.Unlock()
	if live >= w.cfg.MaxSims {
		writeErr(rw, http.StatusConflict, fmt.Errorf("capacity_exceeded: worker limit is %d", w.cfg.MaxSims))
		return
	}
	var (
		device *workerDevice
		err    error
	)
	if req.Platform == "ios" {
		device, err = w.bootIOS(r.Context(), req.Image, req.DeviceType)
	} else {
		device, err = w.bootAndroid(r.Context(), req.Image, req.DeviceType, req.ExtraArgs)
	}
	if err != nil {
		writeErr(rw, http.StatusInternalServerError, err)
		return
	}
	writeJSON(rw, http.StatusOK, map[string]any{"device": device})
}

func (w *workerServer) bootIOS(parent context.Context, runtimeID, deviceType string) (*workerDevice, error) {
	ctx, cancel := context.WithTimeout(parent, 3*time.Minute)
	defer cancel()
	udid, err := ensureIOSDevice(ctx, deviceType, runtimeID)
	if err != nil {
		return nil, err
	}
	resolvedRuntime, _ := resolveIOSRuntime(ctx, runtimeID)
	device := &workerDevice{ID: udid, Platform: "ios", Runtime: resolvedRuntime, DeviceType: deviceType, Status: "booting"}
	w.mu.Lock()
	if existing := w.devices[udid]; existing != nil && existing.Status == "booted" {
		copy := *existing
		w.mu.Unlock()
		if state, _ := simctlDeviceState(udid); state == "Booted" {
			return &copy, nil
		}
		w.mu.Lock()
	}
	w.devices[udid] = device
	_ = w.saveDevicesLocked()
	w.mu.Unlock()
	out, err := exec.CommandContext(ctx, "xcrun", "simctl", "boot", udid).CombinedOutput()
	if err != nil && !strings.Contains(string(out), "current state: Booted") {
		w.setDeviceError(udid, "simctl boot: "+strings.TrimSpace(string(out)))
		return nil, fmt.Errorf("simctl boot: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	out, err = exec.CommandContext(ctx, "xcrun", "simctl", "bootstatus", udid, "-b").CombinedOutput()
	if err != nil {
		w.setDeviceError(udid, "simctl bootstatus: "+strings.TrimSpace(string(out)))
		return nil, fmt.Errorf("simctl bootstatus: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	w.mu.Lock()
	device.Status = "booted"
	device.BootedAt = time.Now().UTC().Format(time.RFC3339)
	_ = w.saveDevicesLocked()
	copy := *device
	w.mu.Unlock()
	return &copy, nil
}

func (w *workerServer) bootAndroid(parent context.Context, image, deviceType string, extraArgs []string) (*workerDevice, error) {
	ctx, cancel := context.WithTimeout(parent, 75*time.Second)
	defer cancel()
	avd, err := ensureAVD(ctx, deviceType, image)
	if err != nil {
		return nil, err
	}
	w.mu.Lock()
	if existing := w.devices[avd]; existing != nil && existing.Status == "booted" {
		copy := *existing
		w.mu.Unlock()
		probeCtx, probeCancel := context.WithTimeout(context.Background(), 3*time.Second)
		out, probeErr := exec.CommandContext(probeCtx, "adb", "-s", copy.Serial, "shell", "getprop", "sys.boot_completed").CombinedOutput()
		probeCancel()
		if probeErr == nil && strings.TrimSpace(string(out)) == "1" {
			return &copy, nil
		}
		w.mu.Lock()
	}
	w.mu.Unlock()

	consolePort, _, releasePort, err := allocateEmulatorPorts()
	if err != nil {
		return nil, err
	}
	serial := fmt.Sprintf("emulator-%d", consolePort)
	logPath := filepath.Join(w.app.bootLogsDir, safeWorkerName(avd)+".log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		releasePort()
		return nil, err
	}
	cmdCtx, cmdCancel := context.WithCancel(context.Background())
	args := []string{"-avd", avd, "-port", strconv.Itoa(consolePort)}
	args = append(args, extraArgs...)
	cmd := exec.CommandContext(cmdCtx, "emulator", args...)
	cmd.Stdout, cmd.Stderr = logFile, logFile
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		cmdCancel()
		_ = logFile.Close()
		releasePort()
		return nil, err
	}
	device := &workerDevice{
		ID: avd, Platform: "android", Runtime: image, DeviceType: deviceType,
		Status: "booting", PID: int64(cmd.Process.Pid), Serial: serial,
	}
	proc := &simProcess{SimID: avd, Platform: "android", Cmd: cmd, Cancel: cmdCancel, LogFile: logFile, stopCh: make(chan struct{})}
	w.mu.Lock()
	w.devices[avd] = device
	w.procs[avd] = proc
	_ = w.saveDevicesLocked()
	w.mu.Unlock()
	go func() {
		err := cmd.Wait()
		releasePort()
		_ = logFile.Close()
		w.mu.Lock()
		delete(w.procs, avd)
		if current := w.devices[avd]; current != nil {
			current.PID = 0
			if cmdCtx.Err() != nil {
				current.Status = "shutdown"
			} else {
				current.Status = "crashed"
				if err != nil {
					current.Error = err.Error()
				}
			}
		}
		_ = w.saveDevicesLocked()
		close(proc.stopCh)
		w.mu.Unlock()
	}()
	if err := waitForAndroidBoot(serial, 120*time.Second); err != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		w.setDeviceError(avd, err.Error())
		return nil, err
	}
	w.mu.Lock()
	device.Status = "booted"
	device.BootedAt = time.Now().UTC().Format(time.RFC3339)
	_ = w.saveDevicesLocked()
	copy := *device
	w.mu.Unlock()
	return &copy, nil
}

func (w *workerServer) handleBuild(rw http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(rw, http.StatusMethodNotAllowed, errBadMethod)
		return
	}
	r.Body = http.MaxBytesReader(rw, r.Body, maxSourceArchiveBytes*2)
	var req struct {
		Framework       string   `json:"framework"`
		SourceTGZB64    string   `json:"source_tgz_b64"`
		DeviceID        string   `json:"device_id"`
		Module          string   `json:"android_module"`
		Scheme          string   `json:"ios_scheme"`
		BuildCmd        string   `json:"build_cmd"`
		GradleExtra     []string `json:"gradle_extra_args"`
		XcodeExtra      []string `json:"xcodebuild_extra_args"`
		AllowedBuildEnv []string `json:"allowed_build_env"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(rw, http.StatusBadRequest, err)
		return
	}
	w.mu.Lock()
	device := w.devices[req.DeviceID]
	w.mu.Unlock()
	if device == nil || device.Status != "booted" || device.Platform != req.Framework {
		writeErr(rw, http.StatusConflict, errors.New("device is not booted for requested platform"))
		return
	}
	srcDir := filepath.Join(os.TempDir(), "apteva-worker-src-"+randHex(8))
	if err := os.MkdirAll(srcDir, 0o700); err != nil {
		writeErr(rw, http.StatusInternalServerError, err)
		return
	}
	defer os.RemoveAll(srcDir)
	if _, err := extractSourceTarGz(req.SourceTGZB64, srcDir); err != nil {
		writeErr(rw, http.StatusBadRequest, err)
		return
	}
	root, err := sourceBuildRoot(srcDir)
	if err != nil {
		writeErr(rw, http.StatusBadRequest, err)
		return
	}
	opID := randHex(12)
	logPath := filepath.Join(w.app.simLogsDir, opID+".log")
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		writeErr(rw, http.StatusInternalServerError, err)
		return
	}
	defer logFile.Close()
	buildCtx, cancel := buildContext(r.Context())
	defer cancel()
	var result buildResult
	if req.Framework == "android" {
		built, buildErr := w.app.buildAndroid(buildCtx, root, req.Module, req.BuildCmd, req.GradleExtra, req.AllowedBuildEnv, logFile)
		if buildErr != nil {
			writeErr(rw, http.StatusInternalServerError, buildErr)
			return
		}
		activity, _ := androidLaunchableActivity(built.APKPath)
		result = buildResult{ArtifactPath: built.APKPath, BundleID: built.BundleID, Activity: activity}
	} else if req.Framework == "ios" {
		built, buildErr := w.app.buildIOS(buildCtx, root, req.Scheme, req.BuildCmd, req.DeviceID, req.XcodeExtra, req.AllowedBuildEnv, logFile)
		if buildErr != nil {
			writeErr(rw, http.StatusInternalServerError, buildErr)
			return
		}
		result = buildResult{ArtifactPath: built.AppPath, BundleID: built.BundleID}
	} else {
		writeErr(rw, http.StatusBadRequest, errBadPlatform)
		return
	}
	artifactID := filepath.Base(result.ArtifactPath)
	cutoff := time.Now().Add(-storageRetention)
	_ = removeOldUnreferenced(w.app.artifactsDir, map[string]struct{}{result.ArtifactPath: {}}, cutoff)
	_ = removeOldUnreferenced(w.app.simLogsDir, map[string]struct{}{logPath: {}}, cutoff)
	writeJSON(rw, http.StatusOK, map[string]any{
		"artifact_id": artifactID, "bundle_id": result.BundleID,
		"activity": result.Activity, "log_id": opID,
	})
}

func (w *workerServer) handleDevice(rw http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/v1/devices/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) != 2 || parts[0] == "" {
		writeErr(rw, http.StatusBadRequest, errBadPath)
		return
	}
	id, action := parts[0], parts[1]
	w.mu.Lock()
	device := w.devices[id]
	if device != nil {
		copy := *device
		device = &copy
	}
	w.mu.Unlock()
	if device == nil {
		writeErr(rw, http.StatusNotFound, errNotFound)
		return
	}
	sim := &Sim{ID: device.ID, BackendID: device.ID, Platform: device.Platform, Serial: device.Serial, DeviceType: device.DeviceType, Status: device.Status}
	switch action {
	case "state":
		w.mu.Lock()
		w.refreshDeviceLocked(device)
		_ = w.saveDevicesLocked()
		w.mu.Unlock()
		writeJSON(rw, http.StatusOK, map[string]any{"device": device})
	case "shutdown":
		if r.Method != http.MethodPost {
			writeErr(rw, http.StatusMethodNotAllowed, errBadMethod)
			return
		}
		if err := w.shutdown(id); err != nil {
			writeErr(rw, http.StatusInternalServerError, err)
			return
		}
		writeJSON(rw, http.StatusOK, map[string]any{"ok": true})
	case "screenshot":
		var body []byte
		var err error
		if device.Platform == "android" {
			body, err = androidScreenshot(device.Serial)
		} else {
			body, err = iosScreenshot(device.ID)
		}
		if err != nil {
			writeErr(rw, http.StatusInternalServerError, err)
			return
		}
		rw.Header().Set("Content-Type", "image/png")
		rw.Header().Set("Cache-Control", "no-store")
		_, _ = rw.Write(body)
	case "logs":
		lines, _ := strconv.Atoi(r.URL.Query().Get("lines"))
		lines = normalizeLogLines(lines)
		var content string
		var err error
		if device.Platform == "android" {
			content, err = androidLogs(device.Serial, lines)
		} else {
			content, err = iosLogs(device.ID, lines)
		}
		if err != nil {
			writeErr(rw, http.StatusInternalServerError, err)
			return
		}
		writeJSON(rw, http.StatusOK, map[string]any{"content": content})
	case "input":
		if r.Method != http.MethodPost {
			writeErr(rw, http.StatusMethodNotAllowed, errBadMethod)
			return
		}
		var event inputEvent
		if err := json.NewDecoder(http.MaxBytesReader(rw, r.Body, 64<<10)).Decode(&event); err != nil {
			writeErr(rw, http.StatusBadRequest, err)
			return
		}
		if err := w.app.sendInput(sim, event); err != nil {
			writeErr(rw, http.StatusInternalServerError, err)
			return
		}
		writeJSON(rw, http.StatusOK, map[string]any{"ok": true})
	case "install":
		var req struct {
			ArtifactID string `json:"artifact_id"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(rw, http.StatusBadRequest, err)
			return
		}
		path, err := w.artifactPath(req.ArtifactID, device.Platform)
		if err == nil && device.Platform == "android" {
			err = installAndroidAPK(device.Serial, path)
		} else if err == nil {
			err = installIOSApp(device.ID, path)
		}
		if err != nil {
			writeErr(rw, http.StatusInternalServerError, err)
			return
		}
		writeJSON(rw, http.StatusOK, map[string]any{"ok": true})
	case "launch":
		var req struct {
			BundleID string `json:"bundle_id"`
			Activity string `json:"activity"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			writeErr(rw, http.StatusBadRequest, err)
			return
		}
		var err error
		if device.Platform == "android" {
			err = launchAndroid(device.Serial, req.BundleID, req.Activity)
		} else {
			err = launchIOS(device.ID, req.BundleID)
		}
		if err != nil {
			writeErr(rw, http.StatusInternalServerError, err)
			return
		}
		writeJSON(rw, http.StatusOK, map[string]any{"ok": true})
	case "stream":
		upgrader := websocket.Upgrader{ReadBufferSize: 4096, WriteBufferSize: 1 << 16, CheckOrigin: func(*http.Request) bool { return true }}
		conn, err := upgrader.Upgrade(rw, r, nil)
		if err == nil {
			w.app.runStreamSession(sim, conn)
		}
	default:
		writeErr(rw, http.StatusNotFound, errBadPath)
	}
}

func (w *workerServer) artifactPath(id, platform string) (string, error) {
	id = strings.TrimSpace(id)
	if id == "" || filepath.Base(id) != id {
		return "", errors.New("invalid artifact_id")
	}
	wantSuffix := ".apk"
	if platform == "ios" {
		wantSuffix = ".app"
	}
	if !strings.HasSuffix(id, wantSuffix) {
		return "", errors.New("artifact does not match simulator platform")
	}
	path := filepath.Join(w.app.artifactsDir, id)
	if _, err := os.Stat(path); err != nil {
		return "", fmt.Errorf("artifact not found: %w", err)
	}
	return path, nil
}

func (w *workerServer) shutdown(id string) error {
	w.mu.Lock()
	var device *workerDevice
	if current := w.devices[id]; current != nil {
		copy := *current
		device = &copy
	}
	proc := w.procs[id]
	w.mu.Unlock()
	if device == nil {
		return nil
	}
	w.app.stopStream(id)
	if device.Platform == "ios" {
		if err := shutdownIOSSim(id); err != nil {
			return err
		}
	} else {
		_ = shutdownAndroidSim(device.Serial)
		if proc != nil && proc.Cmd != nil && proc.Cmd.Process != nil {
			_ = syscall.Kill(-proc.Cmd.Process.Pid, syscall.SIGTERM)
		}
	}
	w.mu.Lock()
	if current := w.devices[id]; current != nil {
		current.Status = "shutdown"
		current.PID = 0
		current.Error = ""
	}
	_ = w.saveDevicesLocked()
	w.mu.Unlock()
	return nil
}

func (w *workerServer) refreshDeviceLocked(device *workerDevice) {
	if device == nil || (device.Status != "booted" && device.Status != "booting") {
		return
	}
	if device.Platform == "ios" {
		state, err := simctlDeviceState(device.ID)
		if err == nil && state == "Shutdown" {
			device.Status = "shutdown"
		}
		return
	}
	if device.Platform == "android" {
		if device.Serial == "" {
			device.Status = "crashed"
			device.Error = "android device has no adb serial"
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		out, err := exec.CommandContext(ctx, "adb", "-s", device.Serial, "shell", "getprop", "sys.boot_completed").CombinedOutput()
		cancel()
		if err != nil || strings.TrimSpace(string(out)) != "1" {
			device.Status = "crashed"
			device.Error = "android emulator is not reachable through adb"
		}
	}
}

func (w *workerServer) setDeviceError(id, message string) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if device := w.devices[id]; device != nil {
		device.Status = "crashed"
		device.Error = message
	}
	_ = w.saveDevicesLocked()
}

func (w *workerServer) devicesPath() string {
	return filepath.Join(w.cfg.DataDir, "devices.json")
}

func (w *workerServer) loadDevices() error {
	body, err := os.ReadFile(w.devicesPath())
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	var devices []workerDevice
	if err := json.Unmarshal(body, &devices); err != nil {
		return err
	}
	for i := range devices {
		device := devices[i]
		w.refreshDeviceLocked(&device)
		w.devices[device.ID] = &device
	}
	return nil
}

// saveDevicesLocked persists only non-secret device locators and lifecycle
// state. Callers hold w.mu, except during single-threaded startup.
func (w *workerServer) saveDevicesLocked() error {
	devices := make([]workerDevice, 0, len(w.devices))
	for _, device := range w.devices {
		devices = append(devices, *device)
	}
	body, err := json.Marshal(devices)
	if err != nil {
		return err
	}
	tmp := w.devicesPath() + ".tmp"
	if err := os.WriteFile(tmp, body, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, w.devicesPath())
}

func safeWorkerName(value string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '-' || r == '_' {
			return r
		}
		return '_'
	}, value)
}

func copyWorkerStream(dst, src *websocket.Conn, done chan<- error) {
	for {
		kind, body, err := src.ReadMessage()
		if err != nil {
			done <- err
			return
		}
		if err := dst.WriteMessage(kind, body); err != nil {
			done <- err
			return
		}
	}
}

func drainAndClose(body io.ReadCloser) {
	_, _ = io.Copy(io.Discard, io.LimitReader(body, 64<<10))
	_ = body.Close()
}
