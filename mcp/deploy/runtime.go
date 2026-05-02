package main

import (
	"context"
	"errors"
	"fmt"
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
)

// Runtime is the abstraction every "deploy target" implements.
// LocalRuntime ships in v0.1; SSHRuntime / DockerRuntime plug in
// later behind the same interface.
type Runtime interface {
	// Start launches the release. Returns once the process is
	// running (or has failed to start). Long-running supervision
	// happens in a goroutine the runtime owns.
	Start(spec ReleaseSpec) (*RunningRelease, error)
	// Stop terminates a running release; idempotent.
	Stop(release *RunningRelease) error
	// LogPath returns the absolute path of the runtime log file.
	LogPath(releaseID int64) string
}

type ReleaseSpec struct {
	ReleaseID    int64
	DeploymentID int64
	Name         string  // for log/process labels
	Framework    string  // 'go' | 'static' | 'blank'
	ArtifactDir  string  // /data/builds/<id>/dist/
	Entrypoint   string  // relative path within ArtifactDir; "" = static FileServer
	StartCmd     string  // override; if non-empty wins over framework default
	Port         int     // assigned port
	Env          map[string]string
}

// RunningRelease is the supervisor's view of a live process. The
// store holds the persistent record; this struct is the in-memory
// handle for stop/restart.
type RunningRelease struct {
	ReleaseID  int64
	Port       int
	PID        int
	cmd        *exec.Cmd       // nil for static (in-process FileServer)
	server     *http.Server    // non-nil for static
	cancel     context.CancelFunc
	logFile    *os.File
	stopCh     chan struct{}   // closed when supervisor exits
}

// ─── LocalRuntime ─────────────────────────────────────────────────

type LocalRuntime struct {
	dataDir string // /data
	app     *App   // back-ref so the supervisor can update DB + emit
}

func NewLocalRuntime(dataDir string, app *App) *LocalRuntime {
	return &LocalRuntime{dataDir: dataDir, app: app}
}

func (r *LocalRuntime) LogPath(releaseID int64) string {
	return filepath.Join(r.dataDir, "releases", fmt.Sprintf("%d", releaseID), "runtime.log")
}

func (r *LocalRuntime) Start(spec ReleaseSpec) (*RunningRelease, error) {
	logPath := r.LogPath(spec.ReleaseID)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return nil, err
	}
	logF, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, err
	}
	fmt.Fprintf(logF, "\n=== release %d starting at %s ===\n", spec.ReleaseID, nowUTC())

	// Static gets a tiny in-process FileServer; no child process.
	if spec.Framework == "static" {
		return r.startStatic(spec, logF, logPath)
	}
	return r.startProcess(spec, logF, logPath)
}

func (r *LocalRuntime) startStatic(spec ReleaseSpec, logF *os.File, logPath string) (*RunningRelease, error) {
	mux := http.NewServeMux()
	mux.Handle("/", http.FileServer(http.Dir(spec.ArtifactDir)))
	srv := &http.Server{
		Addr:    fmt.Sprintf("127.0.0.1:%d", spec.Port),
		Handler: mux,
	}
	ln, err := net.Listen("tcp", srv.Addr)
	if err != nil {
		logF.Close()
		return nil, fmt.Errorf("listen :%d: %w", spec.Port, err)
	}
	stop := make(chan struct{})
	go func() {
		defer close(stop)
		if err := srv.Serve(ln); err != nil && err != http.ErrServerClosed {
			fmt.Fprintf(logF, "static server error: %v\n", err)
			r.app.markCrashed(spec.ReleaseID, err)
			return
		}
		fmt.Fprintf(logF, "static server stopped\n")
	}()
	rr := &RunningRelease{
		ReleaseID: spec.ReleaseID, Port: spec.Port, PID: os.Getpid(),
		server: srv, logFile: logF, stopCh: stop,
	}
	return rr, nil
}

func (r *LocalRuntime) startProcess(spec ReleaseSpec, logF *os.File, logPath string) (*RunningRelease, error) {
	bin, args, err := resolveCommand(spec)
	if err != nil {
		logF.Close()
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, bin, args...)
	cmd.Dir = spec.ArtifactDir
	cmd.Stdout = logF
	cmd.Stderr = logF
	cmd.Env = mergeEnv(spec.Env, spec.Port)
	// New process group so we can kill children if the entrypoint
	// spawned any (next/python tend to).
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	fmt.Fprintf(logF, "+ %s %s (cwd=%s, port=%d)\n", bin, strings.Join(args, " "), spec.ArtifactDir, spec.Port)
	if err := cmd.Start(); err != nil {
		cancel()
		logF.Close()
		return nil, fmt.Errorf("exec %s: %w", bin, err)
	}

	rr := &RunningRelease{
		ReleaseID: spec.ReleaseID, Port: spec.Port, PID: cmd.Process.Pid,
		cmd: cmd, cancel: cancel, logFile: logF, stopCh: make(chan struct{}),
	}

	// Supervisor goroutine: waits for the process, marks state.
	go func() {
		defer close(rr.stopCh)
		err := cmd.Wait()
		exit := -1
		if cmd.ProcessState != nil {
			exit = cmd.ProcessState.ExitCode()
		}
		fmt.Fprintf(logF, "=== process exited at %s (exit=%d, err=%v) ===\n", nowUTC(), exit, err)
		_ = logF.Close()
		// Distinguish clean stop (cancel was called) vs crash.
		if ctx.Err() != nil {
			r.app.markStopped(rr.ReleaseID)
		} else {
			r.app.markCrashed(rr.ReleaseID, fmt.Errorf("exit %d", exit))
		}
	}()

	// Tiny health probe: TCP-connect to the port for up to 5s. If
	// the process started cleanly the listener should appear quickly.
	go r.app.probeReady(rr.ReleaseID, spec.Port, 5*time.Second)

	return rr, nil
}

func (r *LocalRuntime) Stop(rr *RunningRelease) error {
	if rr == nil {
		return nil
	}
	if rr.server != nil {
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = rr.server.Shutdown(ctx)
		<-rr.stopCh
		return nil
	}
	if rr.cancel != nil {
		rr.cancel() // sends SIGKILL after Wait returns; CommandContext owns the lifecycle
	}
	if rr.cmd != nil && rr.cmd.Process != nil {
		// Try graceful first.
		_ = syscall.Kill(-rr.cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-rr.stopCh:
		case <-time.After(5 * time.Second):
			_ = syscall.Kill(-rr.cmd.Process.Pid, syscall.SIGKILL)
			<-rr.stopCh
		}
	}
	return nil
}

// ─── helpers ──────────────────────────────────────────────────────

// resolveCommand picks (binary, args) for a release. start_cmd
// override always wins; otherwise framework defaults.
func resolveCommand(spec ReleaseSpec) (string, []string, error) {
	if strings.TrimSpace(spec.StartCmd) != "" {
		return "sh", []string{"-c", spec.StartCmd}, nil
	}
	switch spec.Framework {
	case "go":
		if spec.Entrypoint == "" {
			return "", nil, errors.New("go release missing entrypoint binary")
		}
		// Entrypoint is absolute (build wrote it under artifactDir).
		return spec.Entrypoint, nil, nil
	case "blank":
		return "", nil, errors.New("blank framework requires start_cmd")
	default:
		return "", nil, fmt.Errorf("no default start command for framework %q", spec.Framework)
	}
}

func mergeEnv(extra map[string]string, port int) []string {
	out := append([]string{}, os.Environ()...)
	out = append(out, "PORT="+strconv.Itoa(port))
	for k, v := range extra {
		out = append(out, k+"="+v)
	}
	return out
}

// ─── supervisor registry ──────────────────────────────────────────

// SupervisorRegistry holds in-memory handles to RunningReleases so
// stop/destroy can find them again. The DB has the durable record;
// this is just the live cmd handle.
type SupervisorRegistry struct {
	mu  sync.Mutex
	all map[int64]*RunningRelease
}

func NewSupervisorRegistry() *SupervisorRegistry {
	return &SupervisorRegistry{all: map[int64]*RunningRelease{}}
}

func (s *SupervisorRegistry) Put(rr *RunningRelease) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.all[rr.ReleaseID] = rr
}

func (s *SupervisorRegistry) Get(id int64) *RunningRelease {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.all[id]
}

func (s *SupervisorRegistry) Delete(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.all, id)
}

func (s *SupervisorRegistry) All() []*RunningRelease {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*RunningRelease, 0, len(s.all))
	for _, rr := range s.all {
		out = append(out, rr)
	}
	return out
}
