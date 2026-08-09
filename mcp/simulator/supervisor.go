package main

// In-process registry of live sim processes — mirror of code's
// devSupervisor. One entry per sim_id. The DB row in sims is the
// durable status; this struct carries the live cmd handles + WS
// clients used for stop/restart/stream.
//
// Per-platform stop logic differs (SIGTERM the emulator process group
// on android vs `xcrun simctl shutdown <udid>` on ios), so each
// platform's lifecycle code populates simProcess fields that match
// its model. The supervisor itself stays platform-agnostic.

import (
	"context"
	"os"
	"os/exec"
	"sync"

	sdk "github.com/apteva/app-sdk"
)

// simProcess holds the live runtime state for one booted sim. Fields
// are platform-specific:
//
//	android: Cmd + Cancel point at the emulator child; Streamers points
//	         at the scrcpy bridge once a stream WS is attached.
//	ios:     Cmd is nil (simctl boot is host-managed); Streamers points
//	         at idb_companion + idb video-stream once attached.
type simProcess struct {
	SimID    string
	Platform string

	// Cmd + Cancel non-nil only when this sidecar owns a child process
	// for the sim. nil for ios sims (simctl boot leaves a host service
	// running independently of us; shutdown is `simctl shutdown <udid>`).
	Cmd    *exec.Cmd
	Cancel context.CancelFunc

	// Streamers — per-platform stream-source children (scrcpy / idb).
	// Spawned on first WS client connect, torn down when the last
	// client disconnects so we don't burn CPU when nobody is watching.
	Streamers []*exec.Cmd

	// LogFile — boot-time log capturing emulator stdout/stderr. The
	// per-run build/install log is separate (per sim_runs row).
	LogFile *os.File

	stopCh chan struct{}
}

type simSupervisor struct {
	mu      sync.Mutex
	all     map[string]*simProcess
	app     *App
	dataDir string
}

func newSimSupervisor(app *App, dataDir string) *simSupervisor {
	return &simSupervisor{
		all:     map[string]*simProcess{},
		app:     app,
		dataDir: dataDir,
	}
}

func (s *simSupervisor) get(id string) *simProcess {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.all[id]
}

func (s *simSupervisor) put(p *simProcess) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.all[p.SimID] = p
}

func (s *simSupervisor) drop(id string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.all, id)
}

func (s *simSupervisor) snapshot() []*simProcess {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]*simProcess, 0, len(s.all))
	for _, p := range s.all {
		out = append(out, p)
	}
	return out
}

// reconcileOrphans runs at OnMount. Rows in booting|booted whose
// underlying process is no longer alive get demoted. For android we
// can probe via `kill 0 <pid>`; for ios we ask simctl whether the
// UDID is currently booted. Either way, the sidecar process is the
// parent group leader, so under normal restarts every android child
// died with us — this just brings the DB in line with reality.
//
// The actual probes live in android_boot.go (processAlive) and
// ios_boot.go (simctlIsBooted) — added in their chunks. Until those
// land this is a stub that no-ops; rows stay as they are. A second
// pass on OnUnmount + boot is what actually catches stale rows.
func (s *simSupervisor) reconcileOrphans(ctx *sdk.AppCtx) error {
	rows, err := dbListLiveSims(ctx.AppDB())
	if err != nil {
		return err
	}
	for i := range rows {
		row := rows[i]
		// Remote devices are owned by simulator-worker and reconciled through
		// its authenticated API. Never demote them with local PID/simctl probes.
		if row.IsRemote() {
			continue
		}
		alive := s.probeAlive(row)
		if alive {
			continue
		}
		_ = dbUpdateSim(ctx.AppDB(), row.ID, map[string]any{
			"status": "shutdown",
			"pid":    0,
			"error":  "supervisor restarted; sim marked shutdown on cold boot",
		})
	}
	return nil
}

// probeAlive dispatches to the platform-specific liveness probe via
// the {android,ios}{ProcessAlive,Shutdown}Fn variables, which the
// per-platform files overwrite in their init(). Until both override
// this returns false — the OnMount reconciler then marks every
// previously-live sim as shutdown, which is the safe default.
func (s *simSupervisor) probeAlive(row Sim) bool {
	switch row.Platform {
	case "android":
		return androidProcessAliveFn(row)
	case "ios":
		return iosProcessAliveFn(row)
	}
	return false
}

// stopAll terminates every supervised sim before the sidecar exits.
// Called from OnUnmount. Per-platform shutdown logic lives in the
// android_/ios_ files; this just dispatches.
func (s *simSupervisor) stopAll() {
	for _, p := range s.snapshot() {
		s.shutdownProcess(p)
	}
}

func (s *simSupervisor) shutdownProcess(p *simProcess) {
	switch p.Platform {
	case "android":
		androidShutdownFn(p)
	case "ios":
		iosShutdownFn(p)
	}
	for _, c := range p.Streamers {
		if c != nil && c.Process != nil {
			_ = c.Process.Kill()
		}
	}
	if p.LogFile != nil {
		_ = p.LogFile.Close()
	}
}

// ─── Per-platform hooks ────────────────────────────────────────────
//
// These are function variables overridden at init() time by the
// per-platform files (android_boot.go, ios_boot.go). Default
// implementations are safe no-ops so supervisor.go compiles before
// any backend lands.

var (
	androidProcessAliveFn = func(_ Sim) bool { return false }
	androidShutdownFn     = func(_ *simProcess) {}

	iosProcessAliveFn = func(_ Sim) bool { return false }
	iosShutdownFn     = func(_ *simProcess) {}
)
