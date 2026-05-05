// tunnel.go — manage the lifecycle of a cloudflared subprocess.
//
// The whole "expose" feature reduces to:
//
//   1. spawn `cloudflared tunnel --url <target> --no-autoupdate`
//   2. tail its stderr until we see https://*.trycloudflare.com
//   3. hold the cmd handle so we can SIGTERM it on stop
//
// State is held in a single in-process Manager guarded by a mutex.
// Persistence (the runs table) is owned by main.go — this file only
// exposes hooks the caller wires up.

package main

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"sync"
	"syscall"
	"time"
)

// trycloudflareURL matches the URL cloudflared prints to stderr when a
// Quick Tunnel has been assigned. Format is stable across cloudflared
// versions; the surrounding box-drawing characters and JSON envelope
// are not, so we just regex the raw line stream.
var trycloudflareURL = regexp.MustCompile(`https://[a-z0-9-]+\.trycloudflare\.com`)

// Status — what's currently happening. Mirrors the runs.status column
// for the "live" row, plus an "idle" sentinel for "no run in flight".
type Status string

const (
	StatusIdle    Status = "idle"
	StatusRunning Status = "running"
	StatusFailed  Status = "failed"
)

// Manager owns the cloudflared subprocess for one app install.
// Goroutine-safe; all public methods take the mutex.
type Manager struct {
	mu sync.Mutex

	// Runtime state for the currently-active run. Cleared on Stop.
	cmd       *exec.Cmd
	cancel    context.CancelFunc
	publicURL string
	targetURL string
	startedAt time.Time
	runID     int64
	status    Status
	lastError string

	// Hooks the caller (main.go) installs to persist state.
	onURLAssigned func(runID int64, url string)
	onExit        func(runID int64, exitReason string, status Status)
}

// NewManager — single instance per process. There is at most one
// tunnel running at a time per install (in v0.1), so we don't need a
// map keyed by anything.
func NewManager(onURLAssigned func(int64, string), onExit func(int64, string, Status)) *Manager {
	return &Manager{
		status:        StatusIdle,
		onURLAssigned: onURLAssigned,
		onExit:        onExit,
	}
}

// Start spawns cloudflared and returns once the subprocess is launched
// (not once the URL is assigned — that arrives async via onURLAssigned).
// Returns ErrAlreadyRunning if a tunnel is already up.
func (m *Manager) Start(binary, target string, runID int64) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.status == StatusRunning {
		return ErrAlreadyRunning
	}
	if binary == "" {
		binary = "cloudflared"
	}
	if target == "" {
		return errors.New("target URL is empty")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(ctx, binary,
		"tunnel",
		"--url", target,
		"--no-autoupdate",
	)
	// Combined stderr is where cloudflared writes the assigned URL.
	// Stdout is mostly empty in --url mode but we read it anyway so
	// the pipe doesn't fill and block the child.
	stderr, err := cmd.StderrPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("stderr pipe: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		cancel()
		return fmt.Errorf("stdout pipe: %w", err)
	}

	if err := cmd.Start(); err != nil {
		cancel()
		// Most common failure: binary missing from PATH. Surface a
		// clear message rather than the raw exec.Error.
		if errors.Is(err, exec.ErrNotFound) {
			return fmt.Errorf("cloudflared binary not found at %q — install from github.com/cloudflare/cloudflared or set the cloudflared_path config", binary)
		}
		return fmt.Errorf("spawn cloudflared: %w", err)
	}

	m.cmd = cmd
	m.cancel = cancel
	m.targetURL = target
	m.startedAt = time.Now()
	m.runID = runID
	m.status = StatusRunning
	m.lastError = ""
	m.publicURL = ""

	// Drain stdout to avoid back-pressure. Discard contents.
	go io.Copy(io.Discard, stdout)

	// Tail stderr, scan for the URL, persist when we see it.
	go m.scanStderr(stderr, runID)

	// Wait for exit in the background; clean up when it ends.
	go m.waitForExit(runID)

	return nil
}

// scanStderr reads cloudflared's stderr until either the URL is
// assigned (we record it once) or the pipe closes (process exited).
func (m *Manager) scanStderr(r io.ReadCloser, runID int64) {
	defer r.Close()
	scanner := bufio.NewScanner(r)
	// cloudflared lines fit easily in 64KB but the default Scanner
	// buffer is 64KB anyway, which is fine.
	captured := false
	for scanner.Scan() {
		line := scanner.Text()
		if !captured {
			if match := trycloudflareURL.FindString(line); match != "" {
				m.mu.Lock()
				// Only record if this is still the active run — Stop
				// could have raced ahead of us.
				if m.runID == runID {
					m.publicURL = match
				}
				cb := m.onURLAssigned
				m.mu.Unlock()
				if cb != nil {
					cb(runID, match)
				}
				captured = true
			}
		}
		// We deliberately don't log every cloudflared line — too noisy
		// for normal operation. If we ever need to debug, add a flag.
	}
}

// waitForExit blocks until the subprocess ends, then transitions the
// manager back to idle and notifies the caller for DB persistence.
func (m *Manager) waitForExit(runID int64) {
	m.mu.Lock()
	cmd := m.cmd
	m.mu.Unlock()
	if cmd == nil {
		return
	}

	waitErr := cmd.Wait()

	m.mu.Lock()
	// If the manager has already moved on (someone called Start again
	// for a different runID), don't clobber the new state.
	if m.runID != runID {
		m.mu.Unlock()
		return
	}
	cb := m.onExit
	reason := ""
	finalStatus := StatusIdle
	switch {
	case waitErr == nil:
		// Clean exit — usually because we sent SIGTERM ourselves.
		reason = "stopped"
	default:
		var exitErr *exec.ExitError
		if errors.As(waitErr, &exitErr) {
			// Distinguish "we asked it to stop" from "it crashed".
			// signal: terminated / killed → user stop; otherwise →
			// unexpected exit, mark failed so the UI can surface it.
			if exitErr.ProcessState != nil {
				ws, ok := exitErr.ProcessState.Sys().(syscall.WaitStatus)
				if ok && ws.Signaled() {
					reason = fmt.Sprintf("signal %s", ws.Signal())
					if ws.Signal() == syscall.SIGTERM || ws.Signal() == syscall.SIGINT || ws.Signal() == syscall.SIGKILL {
						// We almost always sent these ourselves.
						break
					}
				} else {
					reason = fmt.Sprintf("exit %d", exitErr.ExitCode())
					finalStatus = StatusFailed
				}
			} else {
				reason = waitErr.Error()
				finalStatus = StatusFailed
			}
		} else {
			reason = waitErr.Error()
			finalStatus = StatusFailed
		}
	}

	m.cmd = nil
	if m.cancel != nil {
		m.cancel()
		m.cancel = nil
	}
	m.status = finalStatus
	if finalStatus == StatusFailed {
		m.lastError = reason
	}
	m.mu.Unlock()

	if cb != nil {
		cb(runID, reason, finalStatus)
	}
}

// Stop sends SIGTERM to cloudflared and waits up to 5s for it to exit
// gracefully, falling back to SIGKILL. Idempotent — calling Stop on an
// idle manager is a no-op.
func (m *Manager) Stop() error {
	m.mu.Lock()
	cmd := m.cmd
	cancel := m.cancel
	m.mu.Unlock()

	if cmd == nil || cmd.Process == nil {
		return nil
	}

	// SIGTERM first; cloudflared exits cleanly on it. Cancel the ctx
	// so the WaitForExit goroutine sees the death promptly even if
	// the signal is somehow eaten.
	_ = cmd.Process.Signal(syscall.SIGTERM)
	deadline := time.NewTimer(5 * time.Second)
	defer deadline.Stop()

	done := make(chan struct{})
	go func() {
		// cmd.Wait was already called by waitForExit; reading m.cmd
		// here just gives us a way to poll. The simplest signal is to
		// poll the manager's status: it transitions away from running
		// once waitForExit fires.
		for {
			m.mu.Lock()
			running := m.status == StatusRunning
			m.mu.Unlock()
			if !running {
				close(done)
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
	}()

	select {
	case <-done:
		return nil
	case <-deadline.C:
		// Fallback: force kill via the context.
		if cancel != nil {
			cancel()
		}
		_ = cmd.Process.Kill()
		return nil
	}
}

// Snapshot returns the current state. Callers are read-only; do not
// mutate the returned values.
type Snapshot struct {
	Status    Status
	PublicURL string
	TargetURL string
	StartedAt time.Time
	RunID     int64
	LastError string
}

func (m *Manager) Snapshot() Snapshot {
	m.mu.Lock()
	defer m.mu.Unlock()
	return Snapshot{
		Status:    m.status,
		PublicURL: m.publicURL,
		TargetURL: m.targetURL,
		StartedAt: m.startedAt,
		RunID:     m.runID,
		LastError: m.lastError,
	}
}

// ErrAlreadyRunning — Start was called while a tunnel is up.
var ErrAlreadyRunning = errors.New("a tunnel is already running; stop it first")
