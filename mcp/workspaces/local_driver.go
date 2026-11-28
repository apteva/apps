package main

// The local driver runs a workspace as a folder on the machine running
// Workspaces. Layout under the driver root:
//
//	<root>/<workload id>/workload.json   state
//	<root>/<workload id>/workspace/      what commands see as /workspace
//	<root>/<workload id>/cache/          what commands see as /cache
//	<root>/<workload id>/tmp/            TMPDIR
//	<root>/<workload id>/exec/<id>.json  one command's state, <id>.log its output
//
// Commands run as the user running Workspaces, inside the OS sandbox, which
// lets them write only under the workload folder. In container mode (no OS
// sandbox inside a container) each workload gets its own unprivileged uid
// instead: workspace/, cache/ and tmp/ belong to it, everything else under
// the workload folder stays root-only, and file transfers go through os.Root
// so a symlink planted by a command cannot redirect them. "/workspace" and "/cache"
// in arguments, the working directory and environment values are rewritten
// to the real folders, and the real folders are rewritten back in logs, so
// agents use the same paths as in a container.

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const (
	localWorkloadPrefix   = "lwk_"
	localExecPrefix       = "lex_"
	localMaxLogBytes      = 16 * 1024 * 1024
	localMaxArchiveBytes  = 32 * 1024 * 1024
	localMaxExpandedBytes = 1024 * 1024 * 1024
	localMaxArchiveFiles  = 50000
	localKillGrace        = 3 * time.Second
	// Container mode gives each workload its own uid (and gid) from here up.
	localUIDBase = 200000
)

var (
	errLocalUnsupported = errors.New("not supported by the local driver")
	localWorkloadIDRe   = regexp.MustCompile(`^lwk_[0-9a-f]{24}$`)
	localExecIDRe       = regexp.MustCompile(`^lex_([0-9a-f]{24})_[0-9a-f]{16}$`)
)

type localWorkload struct {
	ID      string            `json:"id"`
	Name    string            `json:"name"`
	Status  string            `json:"status"`
	Ports   []ContainerPort   `json:"ports"`
	Env     map[string]string `json:"env"`
	Sandbox sandboxKind       `json:"sandbox,omitempty"`
	Network string            `json:"network,omitempty"`
	UID     int               `json:"uid,omitempty"`
	// PkgToken authorises the workload's `pkg` requests; Packages are the
	// system packages it asked for (container mode).
	PkgToken  string   `json:"pkg_token,omitempty"`
	Packages  []string `json:"packages,omitempty"`
	CreatedAt string   `json:"created_at"`
	UpdatedAt string   `json:"updated_at"`
}

type localExec struct {
	ID               string   `json:"id"`
	WorkloadID       string   `json:"workload_id"`
	Argv             []string `json:"argv"`
	WorkingDirectory string   `json:"working_directory"`
	TimeoutSeconds   int      `json:"timeout_s"`
	IdempotencyKey   string   `json:"idempotency_key,omitempty"`
	Status           string   `json:"status"`
	ExitCode         *int     `json:"exit_code,omitempty"`
	ErrorCode        string   `json:"error_code,omitempty"`
	Error            string   `json:"error,omitempty"`
	OutputBytes      int      `json:"output_bytes"`
	OutputTruncated  bool     `json:"output_truncated"`
	CreatedAt        string   `json:"created_at"`
	StartedAt        string   `json:"started_at,omitempty"`
	FinishedAt       string   `json:"finished_at,omitempty"`
	UpdatedAt        string   `json:"updated_at"`
}

type localProc struct {
	cmd    *exec.Cmd
	output *cappedFile
	mu     sync.Mutex
	reason string // cancelled | timeout, set before the process group is killed
}

type localDriver struct {
	root    string
	sandbox sandboxKind
	network string

	mu      sync.Mutex
	running map[string]*localProc
	usage   map[string]localUsage
	allocMu sync.Mutex // serialises uid allocation

	maxProcs    int  // RLIMIT_NPROC per workload uid in container mode; 0 = none
	packagesOff bool // setting local_packages off
	pkg         pkgService
	replayed    map[string]bool
}

// setLimits applies the process cap and package settings.
func (d *localDriver) setLimits(maxProcs int, packages bool) {
	d.mu.Lock()
	d.maxProcs, d.packagesOff = maxProcs, !packages
	d.mu.Unlock()
}

// setMode changes the sandbox and network used for new workloads.
func (d *localDriver) setMode(kind sandboxKind, network string) {
	if network != "none" {
		network = "open"
	}
	d.mu.Lock()
	d.sandbox, d.network = kind, network
	d.mu.Unlock()
}

func (d *localDriver) mode() (sandboxKind, string) {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.sandbox, d.network
}

// workloadMode is the sandbox a workload was created with; workloads from
// before 0.12.0 recorded none and use the driver's current one.
func (d *localDriver) workloadMode(wl *localWorkload) (sandboxKind, string) {
	kind, network := d.mode()
	if wl.Sandbox != "" {
		kind = wl.Sandbox
	}
	if wl.Network != "" {
		network = wl.Network
	}
	return kind, network
}

type localUsage struct {
	bytes int64
	at    time.Time
}

func newLocalDriver(root string, sandbox sandboxKind, network string) *localDriver {
	if network != "none" {
		network = "open"
	}
	// Sandbox rules match real paths, so resolve symlinks such as macOS's
	// /var -> /private/var once here.
	if real, err := filepath.EvalSymlinks(root); err == nil {
		root = real
	}
	return &localDriver{root: root, sandbox: sandbox, network: network, running: map[string]*localProc{}, usage: map[string]localUsage{}, maxProcs: 512, replayed: map[string]bool{}}
}

// Call runs one operation and decodes its result into out, like CallAppResult.
func (d *localDriver) Call(tool string, input map[string]any, out any) error {
	var result any
	var err error
	switch tool {
	case "containers_run":
		result, err = d.run(input)
	case "containers_get":
		result, err = d.getWorkload(input)
	case "containers_start":
		result, err = d.setStatus(input, "running")
	case "containers_stop":
		result, err = d.stop(input)
	case "containers_destroy":
		result, err = d.destroy(input)
	case "containers_exec_start":
		result, err = d.execStart(input)
	case "containers_exec_get":
		result, err = d.execGet(input)
	case "containers_exec_logs":
		result, err = d.execLogs(input)
	case "containers_exec_cancel":
		result, err = d.execCancel(input)
	case "containers_volume_import":
		result, err = d.volumeImport(input)
	case "containers_volume_export":
		result, err = d.volumeExport(input)
	case "containers_usage_get":
		result, err = d.usageGet(input)
	case "containers_port_set":
		result, err = d.portSet(input)
	case "containers_processes":
		result, err = d.processes(input)
	case "containers_process_kill":
		result, err = d.processKill(input)
	default:
		return fmt.Errorf("%s is %w", tool, errLocalUnsupported)
	}
	if err != nil {
		return err
	}
	if out == nil {
		return nil
	}
	raw, err := json.Marshal(result)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

// --- workloads -------------------------------------------------------------

func (d *localDriver) workloadDir(id string) string { return filepath.Join(d.root, id) }
func (d *localDriver) workspaceDir(id string) string {
	return filepath.Join(d.root, id, "workspace")
}
func (d *localDriver) cacheDir(id string) string { return filepath.Join(d.root, id, "cache") }

func (d *localDriver) run(input map[string]any) (any, error) {
	wl := &localWorkload{ID: localWorkloadPrefix + randomHex(12), Name: stringIn(input, "name"), Status: "running"}
	var ports []ContainerPort
	if err := remarshal(input["ports"], &ports); err != nil {
		return nil, fmt.Errorf("ports: %w", err)
	}
	for _, p := range ports {
		if p.ContainerPort < 1 || p.ContainerPort > 65535 {
			return nil, errors.New("ports: container_port must be 1-65535")
		}
		// No port mapping on the host: the process listens on its own port.
		wl.Ports = append(wl.Ports, ContainerPort{Protocol: "tcp", ContainerPort: p.ContainerPort, HostPort: p.ContainerPort, BindAddr: "127.0.0.1"})
	}
	if err := remarshal(input["env"], &wl.Env); err != nil {
		return nil, fmt.Errorf("env: %w", err)
	}
	// Commands refuse to start without a sandbox (sandboxArgv).
	wl.Sandbox, wl.Network = d.mode()
	wl.CreatedAt, wl.UpdatedAt = nowUTC(), nowUTC()
	if wl.Sandbox == sandboxContainer {
		if err := d.createContainerWorkload(wl); err != nil {
			_ = os.RemoveAll(d.workloadDir(wl.ID))
			return nil, err
		}
		return map[string]any{"workload": d.view(wl)}, nil
	}
	for _, dir := range []string{"workspace/.home", "cache", "tmp", "exec"} {
		if err := os.MkdirAll(filepath.Join(d.workloadDir(wl.ID), dir), 0o755); err != nil {
			return nil, err
		}
	}
	if err := d.saveWorkload(wl); err != nil {
		return nil, err
	}
	return map[string]any{"workload": d.view(wl)}, nil
}

// prepareExec makes sure a workload has what its commands expect: the pkg
// command and, in container mode, a pkg token and a passwd entry (both are
// lost when the container is reset, and workloads from before 0.13.0 have
// neither).
func (d *localDriver) prepareExec(wl *localWorkload, kind sandboxKind) error {
	if kind == sandboxContainer {
		if wl.PkgToken == "" {
			wl.PkgToken = randomHex(24)
			if err := d.saveWorkload(wl); err != nil {
				return err
			}
		}
		if err := ensureWorkspaceUser(wl.UID, filepath.Join(d.workspaceDir(wl.ID), ".home")); err != nil {
			return fmt.Errorf("add the workspace user to /etc/passwd: %w", err)
		}
	}
	return d.writePkgScript(wl)
}

// portSet records the preview port of a local workload. There is no port
// mapping: the process listens on that port on the machine itself.
func (d *localDriver) portSet(input map[string]any) (any, error) {
	wl, err := d.loadWorkload(input)
	if err != nil {
		return nil, err
	}
	port := intIn(input, "container_port", 0)
	if port < 1 || port > 65535 {
		return nil, errors.New("container_port must be 1-65535")
	}
	wl.Ports = []ContainerPort{{Protocol: "tcp", ContainerPort: port, HostPort: port, BindAddr: "127.0.0.1"}}
	wl.UpdatedAt = nowUTC()
	if err := d.saveWorkload(wl); err != nil {
		return nil, err
	}
	return map[string]any{"workload": d.view(wl)}, nil
}

// createContainerWorkload allocates the workload's uid and lays out its
// folder: the workload folder and exec/ belong to root, workspace/, cache/
// and tmp/ to the workload's uid, each 0700.
func (d *localDriver) createContainerWorkload(wl *localWorkload) error {
	d.allocMu.Lock()
	defer d.allocMu.Unlock()
	uid, err := d.nextUID()
	if err != nil {
		return err
	}
	wl.UID = uid
	dir := d.workloadDir(wl.ID)
	if err := os.Mkdir(dir, 0o711); err != nil {
		return err
	}
	if err := os.Chmod(dir, 0o711); err != nil {
		return err
	}
	if err := os.Mkdir(filepath.Join(dir, "exec"), 0o700); err != nil {
		return err
	}
	for _, name := range []string{"workspace", "workspace/.home", "cache", "tmp"} {
		target := filepath.Join(dir, name)
		if err := os.Mkdir(target, 0o700); err != nil {
			return err
		}
		if err := os.Chown(target, uid, uid); err != nil {
			return fmt.Errorf("give %s to the workspace user: %w", name, err)
		}
		if err := os.Chmod(target, 0o700); err != nil {
			return err
		}
	}
	wl.PkgToken = randomHex(24)
	if err := ensureWorkspaceUser(uid, filepath.Join(dir, "workspace", ".home")); err != nil {
		return fmt.Errorf("add the workspace user to /etc/passwd: %w", err)
	}
	// Saved while allocMu is held, so the next allocation sees this uid.
	return d.saveWorkload(wl)
}

// nextUID returns one more than the highest uid any workload uses.
func (d *localDriver) nextUID() (int, error) {
	entries, err := os.ReadDir(d.root)
	if err != nil {
		return 0, err
	}
	next := localUIDBase
	for _, entry := range entries {
		if !localWorkloadIDRe.MatchString(entry.Name()) {
			continue
		}
		raw, err := os.ReadFile(filepath.Join(d.root, entry.Name(), "workload.json"))
		if err != nil {
			continue
		}
		var wl localWorkload
		if json.Unmarshal(raw, &wl) == nil && wl.UID >= next {
			next = wl.UID + 1
		}
	}
	if next > 2_000_000_000 {
		return 0, errors.New("no free workspace uid")
	}
	return next, nil
}

func (d *localDriver) view(wl *localWorkload) ContainerWorkload {
	return ContainerWorkload{
		ID: wl.ID, Name: wl.Name, Status: wl.Status, DesiredStatus: wl.Status,
		Ports: wl.Ports, WorkingDirectory: "/workspace", CreatedAt: wl.CreatedAt, UpdatedAt: wl.UpdatedAt,
		Volumes: []ContainerVolume{{Name: "workspace", MountPath: "/workspace"}, {Name: "cache", MountPath: "/cache"}},
		Sandbox: isolationLabel(wl.Sandbox),
	}
}

func (d *localDriver) loadWorkload(input map[string]any) (*localWorkload, error) {
	id := stringIn(input, "workload_id")
	if !localWorkloadIDRe.MatchString(id) {
		return nil, errors.New("workload not found")
	}
	raw, err := os.ReadFile(filepath.Join(d.workloadDir(id), "workload.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, errors.New("workload not found")
	}
	if err != nil {
		return nil, err
	}
	var wl localWorkload
	if err := json.Unmarshal(raw, &wl); err != nil {
		return nil, err
	}
	return &wl, nil
}

func (d *localDriver) saveWorkload(wl *localWorkload) error {
	return writeJSONFile(filepath.Join(d.workloadDir(wl.ID), "workload.json"), wl)
}

func (d *localDriver) getWorkload(input map[string]any) (any, error) {
	wl, err := d.loadWorkload(input)
	if err != nil {
		return nil, err
	}
	return map[string]any{"workload": d.view(wl)}, nil
}

func (d *localDriver) setStatus(input map[string]any, status string) (any, error) {
	wl, err := d.loadWorkload(input)
	if err != nil {
		return nil, err
	}
	wl.Status, wl.UpdatedAt = status, nowUTC()
	if err := d.saveWorkload(wl); err != nil {
		return nil, err
	}
	return map[string]any{"workload": d.view(wl)}, nil
}

func (d *localDriver) stop(input map[string]any) (any, error) {
	wl, err := d.loadWorkload(input)
	if err != nil {
		return nil, err
	}
	d.killWorkload(wl.ID, "cancelled")
	if wl.UID > 0 {
		// Also what escaped its process group (setsid, nohup daemons).
		killUID(wl.UID)
	}
	return d.setStatus(input, "stopped")
}

func (d *localDriver) destroy(input map[string]any) (any, error) {
	id := stringIn(input, "workload_id")
	if !localWorkloadIDRe.MatchString(id) {
		return nil, errors.New("workload not found")
	}
	d.killWorkload(id, "cancelled")
	if wl, err := d.loadWorkload(input); err == nil && wl.UID > 0 {
		killUID(wl.UID)
		removeWorkspaceUser(wl.UID)
	}
	if err := os.RemoveAll(d.workloadDir(id)); err != nil {
		return nil, err
	}
	d.mu.Lock()
	delete(d.usage, id)
	d.mu.Unlock()
	return map[string]any{"workload_id": id, "destroyed": true}, nil
}

// killWorkload stops every running command of a workload and waits briefly
// for them to exit.
func (d *localDriver) killWorkload(id, reason string) {
	d.mu.Lock()
	procs := make([]*localProc, 0)
	for execID, proc := range d.running {
		if m := localExecIDRe.FindStringSubmatch(execID); m != nil && localWorkloadPrefix+m[1] == id {
			procs = append(procs, proc)
		}
	}
	d.mu.Unlock()
	for _, proc := range procs {
		proc.kill(reason)
	}
	deadline := time.Now().Add(localKillGrace + time.Second)
	for time.Now().Before(deadline) {
		d.mu.Lock()
		left := 0
		for execID := range d.running {
			if m := localExecIDRe.FindStringSubmatch(execID); m != nil && localWorkloadPrefix+m[1] == id {
				left++
			}
		}
		d.mu.Unlock()
		if left == 0 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// --- commands --------------------------------------------------------------

func (d *localDriver) execPaths(execID string) (string, string, error) {
	m := localExecIDRe.FindStringSubmatch(execID)
	if m == nil {
		return "", "", errors.New("execution not found")
	}
	dir := filepath.Join(d.workloadDir(localWorkloadPrefix+m[1]), "exec")
	return filepath.Join(dir, execID+".json"), filepath.Join(dir, execID+".log"), nil
}

func (d *localDriver) execStart(input map[string]any) (any, error) {
	wl, err := d.loadWorkload(input)
	if err != nil {
		return nil, err
	}
	if wl.Status != "running" {
		return nil, fmt.Errorf("workload is %s; start it first", wl.Status)
	}
	key := stringIn(input, "idempotency_key")
	if key != "" {
		if existing := d.findByIdempotency(wl.ID, key); existing != nil {
			return execResponse(existing), nil
		}
	}
	var argv []string
	if err := remarshal(input["argv"], &argv); err != nil || len(argv) == 0 {
		return nil, errors.New("argv is required")
	}
	var env map[string]string
	if err := remarshal(input["env"], &env); err != nil {
		return nil, fmt.Errorf("env: %w", err)
	}
	timeout := intIn(input, "timeout_s", 1800)
	if timeout < 1 {
		timeout = 1800
	}
	workingDir := firstNonEmpty(stringIn(input, "working_directory"), "/workspace")
	paths := d.pathMap(wl.ID)
	realDir := filepath.Clean(paths.toReal(workingDir))
	if !withinDir(d.workloadDir(wl.ID), realDir) {
		return nil, fmt.Errorf("working_directory %q is outside the workspace", workingDir)
	}
	if info, err := os.Stat(realDir); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("working_directory %q does not exist", workingDir)
	}
	realArgv := make([]string, len(argv))
	for i, arg := range argv {
		realArgv[i] = paths.toReal(arg)
	}
	if umask := stringIn(input, "umask"); umask != "" {
		if !regexp.MustCompile(`^0?[0-7]{3}$`).MatchString(umask) {
			return nil, errors.New("umask must be octal such as 022")
		}
		realArgv = append([]string{"/bin/sh", "-c", `umask "$0"; exec "$@"`, umask}, realArgv...)
	}
	kind, network := d.workloadMode(wl)
	if err := d.prepareExec(wl, kind); err != nil {
		return nil, err
	}
	if kind == sandboxContainer {
		d.mu.Lock()
		maxProcs := d.maxProcs
		d.mu.Unlock()
		if maxProcs > 0 {
			// RLIMIT_NPROC counts per real uid, and each workload has its own.
			// The command lowers its own limit after the switch to the
			// workspace user; setting it from root on another uid's process
			// would need CAP_SYS_RESOURCE, which containers usually drop.
			realArgv = append([]string{"/bin/sh", "-c", `ulimit -u "$0" || exit 126; exec "$@"`, strconv.Itoa(maxProcs)}, realArgv...)
		}
	}
	full, err := sandboxArgv(kind, d.workloadDir(wl.ID), network, realDir, realArgv)
	if err != nil {
		return nil, err
	}
	now := nowUTC()
	state := &localExec{
		ID: localExecPrefix + strings.TrimPrefix(wl.ID, localWorkloadPrefix) + "_" + randomHex(8), WorkloadID: wl.ID,
		Argv: argv, WorkingDirectory: workingDir, TimeoutSeconds: timeout, IdempotencyKey: key,
		Status: "running", CreatedAt: now, StartedAt: now, UpdatedAt: now,
	}
	statePath, logPath, _ := d.execPaths(state.ID)
	output, err := newCappedFile(logPath, localMaxLogBytes)
	if err != nil {
		return nil, err
	}
	if kind == sandboxContainer {
		d.replayPackages(wl, output)
	}
	cmd := exec.Command(full[0], full[1:]...)
	cmd.Dir = realDir
	cmd.Env = d.commandEnv(wl, kind, paths, env)
	cmd.Stdout, cmd.Stderr = output, output
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if kind == sandboxContainer {
		if wl.UID < localUIDBase {
			output.Close()
			return nil, errors.New("container-mode workload has no uid of its own")
		}
		// Drops root's supplementary groups too (empty Groups).
		cmd.SysProcAttr.Credential = &syscall.Credential{Uid: uint32(wl.UID), Gid: uint32(wl.UID), Groups: []uint32{}}
	}
	if err := cmd.Start(); err != nil {
		output.Close()
		if kind == sandboxContainer && errors.Is(err, fs.ErrPermission) {
			return nil, fmt.Errorf("start command as the workspace user: %w (the program or a parent of %s may not be reachable by other users)", err, d.root)
		}
		return nil, fmt.Errorf("start command: %w", err)
	}
	proc := &localProc{cmd: cmd, output: output}
	if err := writeJSONFile(statePath, state); err != nil {
		proc.kill("cancelled")
		return nil, err
	}
	d.mu.Lock()
	d.running[state.ID] = proc
	d.mu.Unlock()
	go d.wait(state, statePath, proc, time.Duration(timeout)*time.Second)
	return execResponse(state), nil
}

func (d *localDriver) wait(state *localExec, statePath string, proc *localProc, timeout time.Duration) {
	timer := time.AfterFunc(timeout, func() { proc.kill("timeout") })
	err := proc.cmd.Wait()
	timer.Stop()
	proc.output.Close()
	proc.mu.Lock()
	reason := proc.reason
	proc.mu.Unlock()
	now := nowUTC()
	state.FinishedAt, state.UpdatedAt = now, now
	state.OutputBytes, state.OutputTruncated = proc.output.Written(), proc.output.Truncated()
	if ps := proc.cmd.ProcessState; ps != nil && ps.ExitCode() >= 0 {
		code := ps.ExitCode()
		state.ExitCode = &code
	}
	switch {
	case reason == "cancelled":
		state.Status, state.ErrorCode = "cancelled", "cancelled"
	case reason == "timeout":
		state.Status, state.ErrorCode, state.Error = "timed_out", "timeout", "execution exceeded timeout"
	case err == nil:
		state.Status = "succeeded"
	default:
		state.Status = "failed"
		var exitErr *exec.ExitError
		if !errors.As(err, &exitErr) {
			state.ErrorCode, state.Error = "wait_failed", err.Error()
		}
	}
	_ = writeJSONFile(statePath, state)
	d.mu.Lock()
	delete(d.running, state.ID)
	d.mu.Unlock()
}

func (p *localProc) kill(reason string) {
	p.mu.Lock()
	if p.reason == "" {
		p.reason = reason
	}
	p.mu.Unlock()
	if p.cmd.Process == nil {
		return
	}
	pgid := p.cmd.Process.Pid
	_ = syscall.Kill(-pgid, syscall.SIGTERM)
	go func() {
		time.Sleep(localKillGrace)
		_ = syscall.Kill(-pgid, syscall.SIGKILL)
	}()
}

func (d *localDriver) loadExec(execID string) (*localExec, string, error) {
	statePath, logPath, err := d.execPaths(execID)
	if err != nil {
		return nil, "", err
	}
	raw, err := os.ReadFile(statePath)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, "", errors.New("execution not found")
	}
	if err != nil {
		return nil, "", err
	}
	var state localExec
	if err := json.Unmarshal(raw, &state); err != nil {
		return nil, "", err
	}
	if state.Status == "running" || state.Status == "cancelling" {
		d.mu.Lock()
		proc := d.running[execID]
		d.mu.Unlock()
		if proc == nil {
			// Workspaces restarted while the command ran: its result is lost.
			state.Status, state.ErrorCode = "failed", "runtime_restarted"
			state.Error = "Workspaces restarted while this command was running; its result was lost"
			state.FinishedAt, state.UpdatedAt = nowUTC(), nowUTC()
			_ = writeJSONFile(statePath, &state)
		} else {
			state.OutputBytes, state.OutputTruncated = proc.output.Written(), proc.output.Truncated()
			proc.mu.Lock()
			if proc.reason != "" {
				state.Status = "cancelling"
			}
			proc.mu.Unlock()
		}
	}
	return &state, logPath, nil
}

func (d *localDriver) findByIdempotency(workloadID, key string) *localExec {
	entries, _ := os.ReadDir(filepath.Join(d.workloadDir(workloadID), "exec"))
	for _, entry := range entries {
		if !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		state, _, err := d.loadExec(strings.TrimSuffix(entry.Name(), ".json"))
		if err == nil && state.IdempotencyKey == key {
			return state
		}
	}
	return nil
}

func execResponse(state *localExec) map[string]any {
	return map[string]any{"execution_id": state.ID, "status": state.Status, "execution": ContainerExecution{
		ID: state.ID, WorkloadID: state.WorkloadID, Argv: state.Argv, WorkingDirectory: state.WorkingDirectory,
		TimeoutSeconds: state.TimeoutSeconds, Status: state.Status, ExitCode: state.ExitCode,
		ErrorCode: state.ErrorCode, Error: state.Error, OutputBytes: state.OutputBytes, OutputTruncated: state.OutputTruncated,
		CreatedAt: state.CreatedAt, StartedAt: state.StartedAt, FinishedAt: state.FinishedAt, UpdatedAt: state.UpdatedAt,
	}}
}

func (d *localDriver) execGet(input map[string]any) (any, error) {
	state, _, err := d.loadExec(stringIn(input, "execution_id"))
	if err != nil {
		return nil, err
	}
	return execResponse(state), nil
}

func (d *localDriver) execLogs(input map[string]any) (any, error) {
	state, logPath, err := d.loadExec(stringIn(input, "execution_id"))
	if err != nil {
		return nil, err
	}
	raw, err := os.ReadFile(logPath)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	logs := d.pathMap(state.WorkloadID).toVirtual(string(raw))
	if tail := intIn(input, "tail", 0); tail > 0 {
		lines := strings.SplitAfter(logs, "\n")
		if len(lines) > 0 && lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		if len(lines) > tail {
			logs = strings.Join(lines[len(lines)-tail:], "")
		}
	}
	return map[string]any{
		"execution_id": state.ID, "status": state.Status, "logs": logs,
		"output_bytes": state.OutputBytes, "output_truncated": state.OutputTruncated,
	}, nil
}

func (d *localDriver) execCancel(input map[string]any) (any, error) {
	execID := stringIn(input, "execution_id")
	d.mu.Lock()
	proc := d.running[execID]
	d.mu.Unlock()
	if proc != nil {
		proc.kill("cancelled")
	}
	state, _, err := d.loadExec(execID)
	if err != nil {
		return nil, err
	}
	return execResponse(state), nil
}

func (d *localDriver) commandEnv(wl *localWorkload, kind sandboxKind, paths pathMap, extra map[string]string) []string {
	env := map[string]string{"TERM": "dumb"}
	inherit := []string{"PATH", "LANG", "LC_ALL", "USER", "LOGNAME", "SHELL"}
	if kind == sandboxContainer {
		// Not Apteva's own identity: the command runs as the workspace user.
		inherit = []string{"PATH", "LANG", "LC_ALL"}
		user := "workspace"
		if wl.UID >= localUIDBase {
			user = workspaceUserName(wl.UID)
		}
		env["USER"], env["LOGNAME"], env["SHELL"] = user, user, "/bin/sh"
	}
	for _, key := range inherit {
		if value := os.Getenv(key); value != "" {
			env[key] = value
		}
	}
	for key, value := range wl.Env {
		env[key] = paths.toReal(value)
	}
	for key, value := range extra {
		env[key] = paths.toReal(value)
	}
	for key, value := range d.pkgEnv(wl) {
		env[key] = value
	}
	// pkg, then tools the user installs without root: npm -g, pip --user,
	// go install.
	path := []string{filepath.Join(d.workloadDir(wl.ID), "bin")}
	if home := env["HOME"]; home != "" {
		if _, set := env["NPM_CONFIG_PREFIX"]; !set {
			env["NPM_CONFIG_PREFIX"] = filepath.Join(home, ".local")
		}
		path = append(path, filepath.Join(home, ".local", "bin"), filepath.Join(home, "go", "bin"))
	}
	env["PATH"] = strings.Join(append(path, firstNonEmpty(env["PATH"], "/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin")), ":")
	env["TMPDIR"] = filepath.Join(d.workloadDir(wl.ID), "tmp")
	env["APTEVA_WORKSPACE_DIR"] = d.workspaceDir(wl.ID)
	keys := make([]string, 0, len(env))
	for key := range env {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+env[key])
	}
	return out
}

// --- files -----------------------------------------------------------------

func (d *localDriver) volumeBase(wl *localWorkload, volume string) (string, error) {
	switch volume {
	case "workspace":
		return d.workspaceDir(wl.ID), nil
	case "cache":
		return d.cacheDir(wl.ID), nil
	}
	return "", fmt.Errorf("volume %q is not attached to workload", volume)
}

func (d *localDriver) volumeImport(input map[string]any) (any, error) {
	wl, err := d.loadWorkload(input)
	if err != nil {
		return nil, err
	}
	base, err := d.volumeBase(wl, stringIn(input, "volume"))
	if err != nil {
		return nil, err
	}
	rel, err := cleanVolumePath(stringIn(input, "path"))
	if err != nil {
		return nil, err
	}
	encoded := stringIn(input, "archive_base64")
	if len(encoded) > ((localMaxArchiveBytes+2)/3)*4+4 {
		return nil, fmt.Errorf("archive exceeds compressed size limit of %d bytes", localMaxArchiveBytes)
	}
	archive, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return nil, errors.New("archive_base64 is not valid base64")
	}
	// A clear error for the common case; os.Root below is what enforces it.
	if err := noSymlinkBetween(base, filepath.Join(base, filepath.FromSlash(rel))); err != nil {
		return nil, err
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	owner := -1
	if wl.UID > 0 {
		owner = wl.UID
	}
	if err := mkdirAllIn(root, rel, owner); err != nil {
		return nil, err
	}
	if err := extractTarGz(archive, root, rel, owner); err != nil {
		return nil, err
	}
	d.forgetUsage(wl.ID)
	return map[string]any{"workload_id": wl.ID, "volume": stringIn(input, "volume"), "path": rel, "compressed_bytes": len(archive)}, nil
}

// mkdirAllIn creates rel (slash-separated) under root, giving each folder it
// passes to owner when owner >= 0.
func mkdirAllIn(root *os.Root, rel string, owner int) error {
	if rel == "." || rel == "" {
		return nil
	}
	current := ""
	for _, part := range strings.Split(rel, "/") {
		current = path.Join(current, part)
		name := filepath.FromSlash(current)
		if err := root.Mkdir(name, 0o755); err != nil && !errors.Is(err, fs.ErrExist) {
			return err
		}
		if owner >= 0 {
			if err := root.Lchown(name, owner, owner); err != nil {
				return err
			}
		}
	}
	return nil
}

// extractTarGz unpacks archive into destRel under root. Every operation goes
// through root, so a symlink (even one swapped in while this runs) cannot
// make it write outside the volume.
func extractTarGz(archive []byte, root *os.Root, destRel string, owner int) error {
	if len(archive) == 0 {
		return errors.New("archive is empty")
	}
	gz, err := gzip.NewReader(bytes.NewReader(archive))
	if err != nil {
		return errors.New("archive must be a valid tar.gz stream")
	}
	defer gz.Close()
	tr := tar.NewReader(gz)
	var expanded int64
	files := 0
	chown := func(name string) error {
		if owner < 0 {
			return nil
		}
		return root.Lchown(name, owner, owner)
	}
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("read tar.gz: %w", err)
		}
		if files++; files > localMaxArchiveFiles {
			return fmt.Errorf("archive exceeds file limit of %d", localMaxArchiveFiles)
		}
		name, err := cleanArchiveEntry(header.Name)
		if err != nil {
			return err
		}
		if name == "." {
			continue
		}
		targetSlash := path.Join(destRel, name)
		target := filepath.FromSlash(targetSlash)
		if err := mkdirAllIn(root, path.Dir(targetSlash), owner); err != nil {
			return err
		}
		mode := os.FileMode(header.Mode) & 0o777
		switch header.Typeflag {
		case tar.TypeDir:
			if info, err := root.Lstat(target); err == nil && !info.IsDir() {
				if err := root.Remove(target); err != nil {
					return err
				}
			}
			if err := mkdirAllIn(root, targetSlash, owner); err != nil {
				return err
			}
			if mode != 0 {
				_ = root.Chmod(target, mode|0o700)
			}
		case tar.TypeReg, tar.TypeRegA:
			if expanded += header.Size; header.Size < 0 || expanded > localMaxExpandedBytes {
				return errors.New("archive exceeds expanded size limit")
			}
			if info, err := root.Lstat(target); err == nil && info.Mode()&fs.ModeSymlink != 0 {
				if err := root.Remove(target); err != nil {
					return err
				}
			}
			if mode == 0 {
				mode = 0o644
			}
			file, err := root.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_TRUNC|syscall.O_NOFOLLOW, mode)
			if err != nil {
				return err
			}
			_, copyErr := io.Copy(file, io.LimitReader(tr, header.Size))
			closeErr := file.Close()
			if copyErr != nil {
				return copyErr
			}
			if closeErr != nil {
				return closeErr
			}
			_ = root.Chmod(target, mode)
			if err := chown(target); err != nil {
				return err
			}
		case tar.TypeSymlink:
			if err := checkArchiveLink(name, header.Linkname, true); err != nil {
				return err
			}
			_ = root.Remove(target)
			if err := root.Symlink(header.Linkname, target); err != nil {
				return err
			}
			if err := chown(target); err != nil {
				return err
			}
		case tar.TypeLink:
			if err := checkArchiveLink(name, header.Linkname, false); err != nil {
				return err
			}
			source := filepath.FromSlash(path.Join(destRel, path.Clean(header.Linkname)))
			_ = root.Remove(target)
			if err := root.Link(source, target); err != nil {
				return err
			}
		default:
			return fmt.Errorf("archive entry %q has unsupported type %d", header.Name, header.Typeflag)
		}
	}
}

func (d *localDriver) volumeExport(input map[string]any) (any, error) {
	wl, err := d.loadWorkload(input)
	if err != nil {
		return nil, err
	}
	base, err := d.volumeBase(wl, stringIn(input, "volume"))
	if err != nil {
		return nil, err
	}
	rel, err := cleanVolumePath(stringIn(input, "path"))
	if err != nil {
		return nil, err
	}
	if rel != "." {
		// The last component may itself be a symlink: it is archived as one.
		src := filepath.Join(base, filepath.FromSlash(rel))
		if err := noSymlinkBetween(base, filepath.Dir(src)); err != nil {
			return nil, err
		}
	}
	root, err := os.OpenRoot(base)
	if err != nil {
		return nil, err
	}
	defer root.Close()
	if _, err := root.Lstat(filepath.FromSlash(rel)); err != nil {
		return nil, fmt.Errorf("%s: path not found", rel)
	}
	archive, err := packTarGz(root, rel, localMaxArchiveBytes)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"workload_id": wl.ID, "volume": stringIn(input, "volume"), "path": rel, "format": "tar.gz",
		"archive_base64": base64.StdEncoding.EncodeToString(archive), "compressed_bytes": len(archive),
	}, nil
}

// packTarGz archives rel under root with entry names like `tar -C base rel`:
// "rel/..." for a path, "./..." for the volume root. Symlinks are archived as
// links, never followed, and every read goes through root.
func packTarGz(root *os.Root, rel string, maxBytes int) ([]byte, error) {
	var buffer bytes.Buffer
	gz := gzip.NewWriter(&buffer)
	tw := tar.NewWriter(gz)
	entryName := func(current string, dir bool) string {
		inner := "."
		if rel == "." {
			inner = current
		} else if current != rel {
			inner = strings.TrimPrefix(current, rel+"/")
		}
		name := path.Join(rel, inner)
		if rel == "." {
			name = "./" + inner
			if inner == "." {
				name = "./"
			}
		}
		if dir && !strings.HasSuffix(name, "/") {
			name += "/"
		}
		return name
	}
	add := func(current string, info fs.FileInfo) error {
		link := ""
		if info.Mode()&fs.ModeSymlink != 0 {
			var err error
			if link, err = root.Readlink(filepath.FromSlash(current)); err != nil {
				return err
			}
		}
		header, err := tar.FileInfoHeader(info, link)
		if err != nil {
			return nil // sockets and devices are skipped
		}
		header.Name = entryName(current, info.IsDir())
		header.Uid, header.Gid, header.Uname, header.Gname = 0, 0, "", ""
		if err := tw.WriteHeader(header); err != nil {
			return err
		}
		if info.Mode().IsRegular() {
			file, err := root.OpenFile(filepath.FromSlash(current), os.O_RDONLY|syscall.O_NOFOLLOW, 0)
			if err != nil {
				return err
			}
			_, err = io.Copy(tw, io.LimitReader(file, info.Size()))
			file.Close()
			if err != nil {
				return err
			}
		}
		if buffer.Len() > maxBytes {
			return fmt.Errorf("archive exceeds compressed size limit of %d bytes", maxBytes)
		}
		return nil
	}
	var err error
	if top, lerr := root.Lstat(filepath.FromSlash(rel)); lerr != nil {
		err = lerr
	} else if top.Mode()&fs.ModeSymlink != 0 {
		err = add(rel, top)
	} else {
		err = fs.WalkDir(root.FS(), rel, func(current string, entry fs.DirEntry, err error) error {
			if err != nil {
				return err
			}
			info, err := entry.Info()
			if err != nil {
				return err
			}
			return add(current, info)
		})
	}
	if err != nil {
		return nil, err
	}
	if err := tw.Close(); err != nil {
		return nil, err
	}
	if err := gz.Close(); err != nil {
		return nil, err
	}
	if buffer.Len() > maxBytes {
		return nil, fmt.Errorf("archive exceeds compressed size limit of %d bytes", maxBytes)
	}
	return buffer.Bytes(), nil
}

func (d *localDriver) usageGet(input map[string]any) (any, error) {
	wl, err := d.loadWorkload(input)
	if err != nil {
		return nil, err
	}
	d.mu.Lock()
	cached, ok := d.usage[wl.ID]
	d.mu.Unlock()
	if !ok || time.Since(cached.at) > time.Minute {
		var total int64
		_ = filepath.WalkDir(d.workloadDir(wl.ID), func(_ string, entry fs.DirEntry, err error) error {
			if err == nil && entry.Type().IsRegular() {
				if info, err := entry.Info(); err == nil {
					total += info.Size()
				}
			}
			return nil
		})
		cached = localUsage{bytes: total, at: time.Now()}
		d.mu.Lock()
		d.usage[wl.ID] = cached
		d.mu.Unlock()
	}
	return usageResponse{Metrics: []usageMetric{{
		FeatureKey: "containers.storage.bytes", Quantity: cached.bytes, Unit: "bytes", Kind: "gauge", Source: "docker_volume_total",
	}}}, nil
}

func (d *localDriver) forgetUsage(id string) {
	d.mu.Lock()
	delete(d.usage, id)
	d.mu.Unlock()
}

// --- paths -----------------------------------------------------------------

type pathMap struct{ pairs [][2]string }

func (d *localDriver) pathMap(workloadID string) pathMap {
	return pathMap{pairs: [][2]string{{"/workspace", d.workspaceDir(workloadID)}, {"/cache", d.cacheDir(workloadID)}}}
}

// toReal rewrites /workspace and /cache where they start a path: at the start
// or after a character that cannot be part of a path, and followed by '/', the
// end, or a character that cannot be part of a name.
func (m pathMap) toReal(s string) string {
	if !strings.Contains(s, "/workspace") && !strings.Contains(s, "/cache") {
		return s
	}
	var out strings.Builder
	for i := 0; i < len(s); {
		replaced := false
		if i == 0 || !isPathByte(s[i-1]) {
			for _, pair := range m.pairs {
				end := i + len(pair[0])
				if strings.HasPrefix(s[i:], pair[0]) && (end == len(s) || !isNameByte(s[end])) {
					out.WriteString(pair[1])
					i = end
					replaced = true
					break
				}
			}
		}
		if !replaced {
			out.WriteByte(s[i])
			i++
		}
	}
	return out.String()
}

func (m pathMap) toVirtual(s string) string {
	for _, pair := range m.pairs {
		s = strings.ReplaceAll(s, pair[1], pair[0])
	}
	return s
}

func isNameByte(c byte) bool {
	return c >= 'a' && c <= 'z' || c >= 'A' && c <= 'Z' || c >= '0' && c <= '9' || c == '_' || c == '-' || c == '.'
}

func isPathByte(c byte) bool { return isNameByte(c) || c == '/' || c == '~' || c == '$' }

func withinDir(base, target string) bool {
	rel, err := filepath.Rel(base, target)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// noSymlinkBetween refuses a path whose existing components below base
// include a symlink, so file operations cannot be redirected outside it.
func noSymlinkBetween(base, target string) error {
	if !withinDir(base, target) {
		return errors.New("path escapes the volume")
	}
	rel, _ := filepath.Rel(base, target)
	if rel == "." {
		return nil
	}
	current := base
	for _, part := range strings.Split(rel, string(filepath.Separator)) {
		current = filepath.Join(current, part)
		info, err := os.Lstat(current)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			return err
		}
		if info.Mode()&fs.ModeSymlink != 0 {
			return fmt.Errorf("path crosses a symlink at %s", part)
		}
	}
	return nil
}

func removeIfLink(target string) error {
	if info, err := os.Lstat(target); err == nil && info.Mode()&fs.ModeSymlink != 0 {
		return os.Remove(target)
	}
	return nil
}

func cleanVolumePath(value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" || value == "." {
		return ".", nil
	}
	if strings.HasPrefix(value, "/") || strings.IndexByte(value, 0) >= 0 {
		return "", errors.New("path must be relative to the named volume")
	}
	for _, part := range strings.Split(value, "/") {
		if part == ".." {
			return "", errors.New("path must not contain '..'")
		}
	}
	return path.Clean(value), nil
}

func cleanArchiveEntry(name string) (string, error) {
	if name == "" || strings.HasPrefix(name, "/") || strings.IndexByte(name, 0) >= 0 {
		return "", fmt.Errorf("unsafe archive entry %q", name)
	}
	for _, part := range strings.Split(name, "/") {
		if part == ".." {
			return "", fmt.Errorf("archive entry %q escapes its destination", name)
		}
	}
	return path.Clean(name), nil
}

func checkArchiveLink(entry, target string, symbolic bool) error {
	if target == "" || strings.HasPrefix(target, "/") || strings.IndexByte(target, 0) >= 0 {
		return fmt.Errorf("archive link %q has unsafe target %q", entry, target)
	}
	resolved := target
	if symbolic {
		resolved = path.Join(path.Dir(entry), target)
	}
	clean := path.Clean(resolved)
	if clean == ".." || strings.HasPrefix(clean, "../") {
		return fmt.Errorf("archive link %q escapes its destination", entry)
	}
	return nil
}

// --- small helpers ---------------------------------------------------------

type cappedFile struct {
	mu        sync.Mutex
	file      *os.File
	limit     int
	written   int
	truncated bool
}

func newCappedFile(name string, limit int) (*cappedFile, error) {
	file, err := os.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0o600)
	if err != nil {
		return nil, err
	}
	return &cappedFile{file: file, limit: limit}, nil
}

func (c *cappedFile) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	n := len(p)
	c.written += n
	if room := c.limit - (c.written - n); room < len(p) {
		c.truncated = true
		if room <= 0 {
			return n, nil
		}
		p = p[:room]
	}
	if c.file != nil {
		_, _ = c.file.Write(p)
	}
	return n, nil
}

func (c *cappedFile) Written() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.written
}

func (c *cappedFile) Truncated() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.truncated
}

func (c *cappedFile) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.file != nil {
		_ = c.file.Close()
		c.file = nil
	}
}

func writeJSONFile(name string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	tmp := name + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, name)
}

func remarshal(in any, out any) error {
	if in == nil {
		return nil
	}
	raw, err := json.Marshal(in)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func stringIn(input map[string]any, key string) string {
	value, _ := input[key].(string)
	return value
}

func intIn(input map[string]any, key string, fallback int) int {
	switch value := input[key].(type) {
	case int:
		return value
	case int64:
		return int(value)
	case float64:
		return int(value)
	case json.Number:
		n, err := value.Int64()
		if err == nil {
			return int(n)
		}
	}
	return fallback
}

func randomHex(n int) string {
	buf := make([]byte, n)
	_, _ = rand.Read(buf)
	return hex.EncodeToString(buf)
}
