// tunnel.go — manage the lifecycle of a cloudflared subprocess.
//
// Two modes:
//
//   Quick (v0.1, default): `cloudflared tunnel --url <target> --no-autoupdate`,
//   tail stderr for https://*.trycloudflare.com, fresh URL every run.
//
//   Named (v0.2): `cloudflared tunnel --no-autoupdate run --token <token>`,
//   URL is the operator-chosen hostname and known up-front (the API
//   call that minted the token also configured ingress + DNS), so
//   there's nothing to scrape. publicURL is populated at Start time
//   and onURLAssigned fires synchronously before Start returns.
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

// ngrokURL matches the URL the ngrok agent prints to stdout under
// `--log stdout --log-format logfmt`. Format:
//   t=... lvl=info msg="started tunnel" name=command_line addr=http://localhost:5280 url=https://abc.ngrok-free.app
// Matches both free (*.ngrok-free.app) and paid (*.ngrok.app /
// *.ngrok.io and operator-reserved domains) forms.
var ngrokURL = regexp.MustCompile(`url=(https://[a-z0-9.-]+\.(?:ngrok-free\.app|ngrok\.app|ngrok\.io))`)

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

// Mode picks between Quick (anonymous, ephemeral) and Named (stable
// hostname on a CF zone the operator owns).
type Mode string

const (
	ModeQuick Mode = "quick"
	ModeNamed Mode = "named"
	// ModeNgrok spawns the ngrok agent instead of cloudflared. The
	// agent assigns a *.ngrok-free.app URL on the free tier and a
	// reserved domain on paid plans; we discover the URL by scraping
	// ngrok's logfmt-mode stdout for `url=https://...`.
	ModeNgrok Mode = "ngrok"
)

// StartParams bundles everything the Manager needs to spawn a tunnel
// agent. The set of populated fields depends on Mode:
//
//	Quick (cloudflared): Binary, Target, RunID
//	Named (cloudflared): Binary, RunID, Token, Hostname (Target optional, label only)
//	Ngrok:               Binary, Target, RunID, Authtoken (Hostname optional — pre-reserved domain)
type StartParams struct {
	Binary    string
	Target    string // local URL to forward to (used as label in named mode)
	RunID     int64
	Mode      Mode
	Token     string // named-cloudflare: connector token from cfd_tunnel create
	Hostname  string // named-cloudflare: e.g. "tunnel.example.com"; pre-known public host. ngrok: optional reserved domain (paid plans).
	Authtoken string // ngrok: agent authtoken from operator's ngrok integration
}

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
// (in quick mode the URL is assigned async via onURLAssigned; in named
// mode the URL is known up-front so onURLAssigned fires synchronously).
// Returns ErrAlreadyRunning if a tunnel is already up.
func (m *Manager) Start(p StartParams) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.status == StatusRunning {
		return ErrAlreadyRunning
	}
	binary := p.Binary
	if binary == "" {
		binary = "cloudflared"
	}
	if p.Mode == "" {
		p.Mode = ModeQuick
	}

	switch p.Mode {
	case ModeQuick:
		if p.Target == "" {
			return errors.New("target URL is empty")
		}
	case ModeNamed:
		if p.Token == "" {
			return errors.New("named mode: tunnel token is empty")
		}
		if p.Hostname == "" {
			return errors.New("named mode: hostname is empty")
		}
	case ModeNgrok:
		if p.Target == "" {
			return errors.New("ngrok mode: target URL is empty")
		}
		if p.Authtoken == "" {
			return errors.New("ngrok mode: authtoken is empty (bind the ngrok integration first)")
		}
	default:
		return fmt.Errorf("unknown mode %q", p.Mode)
	}

	ctx, cancel := context.WithCancel(context.Background())
	var args []string
	switch p.Mode {
	case ModeQuick:
		args = []string{"tunnel", "--no-autoupdate", "--url", p.Target}
	case ModeNamed:
		// Ingress was configured server-side via the API; the connector
		// just needs the token to dial home.
		args = []string{"tunnel", "--no-autoupdate", "run", "--token", p.Token}
	case ModeNgrok:
		// `ngrok http <target>` proxies <target> to a fresh public
		// hostname (or a reserved domain if --domain is given). We force
		// logfmt to stdout so ngrokURL can scrape the assigned URL.
		// --log-level info keeps the line we need without filling the
		// pipe with debug noise.
		args = []string{
			"http", p.Target,
			"--authtoken", p.Authtoken,
			"--log", "stdout",
			"--log-format", "logfmt",
			"--log-level", "info",
		}
		if p.Hostname != "" {
			args = append(args, "--domain", p.Hostname)
		}
	}
	cmd := exec.CommandContext(ctx, binary, args...)
	// Combined stderr is where cloudflared writes the assigned URL.
	// Stdout is mostly empty but we read it anyway so the pipe doesn't
	// fill and block the child.
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
		// clear message naming the binary the operator needs to fix.
		if errors.Is(err, exec.ErrNotFound) {
			hint := "install from github.com/cloudflare/cloudflared or set the cloudflared_path config"
			if p.Mode == ModeNgrok {
				hint = "install ngrok (brew install ngrok) or set the ngrok_path config"
			}
			return fmt.Errorf("agent binary not found at %q — %s", binary, hint)
		}
		return fmt.Errorf("spawn agent: %w", err)
	}

	m.cmd = cmd
	m.cancel = cancel
	m.targetURL = p.Target
	m.startedAt = time.Now()
	m.runID = p.RunID
	m.status = StatusRunning
	m.lastError = ""
	m.publicURL = ""

	// Wait for exit in the background; clean up when it ends.
	go m.waitForExit(p.RunID)

	// Per-mode wiring of stdout vs stderr to URL-scanner vs Discard.
	// cloudflared writes its URL to STDERR; ngrok writes ours to STDOUT
	// (because we passed --log stdout). Named mode knows the URL up
	// front and just drains both streams.
	switch p.Mode {
	case ModeQuick:
		go io.Copy(io.Discard, stdout)
		go m.scanForURL(stderr, p.RunID, trycloudflareURL)
	case ModeNgrok:
		go io.Copy(io.Discard, stderr)
		go m.scanForURL(stdout, p.RunID, ngrokURL)
	case ModeNamed:
		// URL is known up-front; just drain both streams.
		go io.Copy(io.Discard, stdout)
		go io.Copy(io.Discard, stderr)
		m.publicURL = "https://" + p.Hostname
		url, runID, cb := m.publicURL, p.RunID, m.onURLAssigned
		if cb != nil {
			go cb(runID, url)
		}
	}

	return nil
}

// scanForURL reads agent output line-by-line until either pattern
// matches (we record the URL once) or the pipe closes (process
// exited). Mode-neutral — caller passes the regex (trycloudflareURL
// for cloudflared stderr, ngrokURL for ngrok stdout). The regex's
// first capture group, if present, is preferred over the full match
// — lets ngrok's `url=https://...` line strip the prefix without a
// post-match substring.
func (m *Manager) scanForURL(r io.ReadCloser, runID int64, re *regexp.Regexp) {
	defer r.Close()
	scanner := bufio.NewScanner(r)
	// Agent lines fit easily in the default 64KB Scanner buffer.
	captured := false
	for scanner.Scan() {
		line := scanner.Text()
		if !captured {
			groups := re.FindStringSubmatch(line)
			if len(groups) > 0 {
				match := groups[0]
				if len(groups) > 1 && groups[1] != "" {
					match = groups[1]
				}
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
		// We deliberately don't log every agent line — too noisy for
		// normal operation. If we ever need to debug, add a flag.
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
			// SIGTERM/SIGINT/SIGKILL → user stop; any other signal
			// (SIGSEGV/SIGBUS/SIGABRT/…) is a crash; non-signal exits
			// are also failures.
			ws, ok := exitErr.ProcessState.Sys().(syscall.WaitStatus)
			switch {
			case ok && ws.Signaled():
				sig := ws.Signal()
				reason = fmt.Sprintf("signal %s", sig)
				if sig != syscall.SIGTERM && sig != syscall.SIGINT && sig != syscall.SIGKILL {
					finalStatus = StatusFailed
				}
			default:
				reason = fmt.Sprintf("exit %d", exitErr.ExitCode())
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
