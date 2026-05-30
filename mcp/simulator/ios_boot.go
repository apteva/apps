package main

// iOS Simulator boot/shutdown via xcrun simctl. Unlike android, the
// host-managed CoreSimulator service owns the device's lifetime — we
// just send commands and the service does the work. That means:
//
//   - No supervised child process per sim (Cmd is nil in simProcess)
//   - Shutdown is `simctl shutdown <udid>`, not a kill of a tracked PID
//   - Liveness is "simctl bootstatus reports Booted", not "kill 0"
//
// `simctl bootstatus -b` blocks until the device finishes booting OR
// the deadline elapses. We use it as the readiness probe — much
// cleaner than polling `simctl list devices` ourselves.

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// bootIOSSim boots an iOS Simulator device, headless. Headless ==
// CoreSimulator service hosts the device but the Simulator.app GUI
// never launches; we never want a window popping on the user's Mac
// during a dev run.
//
// Reuse policy: if the device is already Booted, return its row
// immediately (idempotent sims_boot).
func bootIOSSim(ctx *sdk.AppCtx, sup *simSupervisor, udid, deviceType, runtimeID string) (*Sim, error) {
	// Check current state. If already booted, the caller is asking
	// for an idempotent boot — return the row as-is.
	state, _ := simctlDeviceState(udid)
	if state == "Booted" {
		if row, err := dbGetSim(ctx.AppDB(), udid); err == nil && row != nil {
			if row.Status != "booted" {
				_ = dbUpdateSim(ctx.AppDB(), udid, map[string]any{
					"status": "booted",
					"error":  "",
				})
				row.Status = "booted"
				row.Error = ""
			}
			return row, nil
		}
		// State says booted but no DB row — fall through to upsert
		// the row and return below.
	}

	now := time.Now().UTC().Format(time.RFC3339)
	if err := dbUpsertSim(ctx.AppDB(), Sim{
		ID:         udid,
		ProjectID:  ctx.CurrentProject(),
		Platform:   "ios",
		Runtime:    runtimeID,
		DeviceType: deviceType,
		Status:     "booting",
		BootedAt:   now,
	}); err != nil {
		return nil, fmt.Errorf("persist booting sim: %w", err)
	}

	// `simctl boot <udid>` returns once the boot is initiated.
	// `simctl bootstatus <udid> -b` blocks until boot completes (or
	// returns immediately if already booted). We chain them so the
	// caller doesn't get a row marked booting that never flips.
	{
		cctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		out, err := exec.CommandContext(cctx, "xcrun", "simctl", "boot", udid).CombinedOutput()
		if err != nil && !strings.Contains(string(out), "Unable to boot device in current state: Booted") {
			_ = dbUpdateSim(ctx.AppDB(), udid, map[string]any{
				"status": "crashed", "error": "simctl boot: " + strings.TrimSpace(string(out)),
			})
			return nil, fmt.Errorf("simctl boot: %w (output: %s)", err, strings.TrimSpace(string(out)))
		}
	}

	// bootstatus blocks. 120s gives even cold first-time-launching
	// device-types room. Cancellation kills the process; the device
	// continues booting on its own.
	{
		cctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		cmd := exec.CommandContext(cctx, "xcrun", "simctl", "bootstatus", udid, "-b")
		out, err := cmd.CombinedOutput()
		if err != nil {
			_ = dbUpdateSim(ctx.AppDB(), udid, map[string]any{
				"status": "crashed", "error": "bootstatus: " + strings.TrimSpace(string(out)),
			})
			return nil, fmt.Errorf("simctl bootstatus: %w (output: %s)", err, strings.TrimSpace(string(out)))
		}
	}

	// Register a simProcess entry so the supervisor can later send
	// shutdown signals through the normal lifecycle path. iOS sims
	// have nil Cmd — supervisor.shutdownProcess dispatches through
	// iosShutdownFn which calls simctl shutdown.
	sup.put(&simProcess{
		SimID:    udid,
		Platform: "ios",
		stopCh:   make(chan struct{}),
	})

	_ = dbUpdateSim(ctx.AppDB(), udid, map[string]any{
		"status": "booted",
	})
	return dbGetSim(ctx.AppDB(), udid)
}

// simctlDeviceState returns the State string for a UDID (Booted /
// Shutdown / Booting / Shutting Down / …) by listing devices and
// filtering. Returns "" if the UDID isn't found. Used for the
// idempotent-boot fast path.
func simctlDeviceState(udid string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	devs, err := listIOSDevices(ctx)
	if err != nil {
		return "", err
	}
	for _, d := range devs {
		if d.UDID == udid {
			return d.State, nil
		}
	}
	return "", nil
}

// shutdownIOSSim is the graceful path: `simctl shutdown <udid>`. The
// CoreSimulator service handles the cleanup; we don't need to track
// or kill anything.
func shutdownIOSSim(udid string) error {
	if udid == "" {
		return errors.New("udid required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, "xcrun", "simctl", "shutdown", udid).CombinedOutput()
	if err != nil {
		text := strings.TrimSpace(string(out))
		// Idempotent: "Unable to shutdown device in current state: Shutdown"
		// is the success-already-shutdown case.
		if strings.Contains(text, "current state: Shutdown") {
			return nil
		}
		return fmt.Errorf("simctl shutdown: %w (output: %s)", err, text)
	}
	return nil
}

// ─── Supervisor hooks ───────────────────────────────────────────────

// iosProcessAliveReal asks simctl whether the device is currently
// Booted. Used by the OnMount reconciler to demote stale sims rows.
func iosProcessAliveReal(row Sim) bool {
	state, err := simctlDeviceState(row.ID)
	if err != nil {
		return false
	}
	return state == "Booted"
}

// iosShutdownReal sends `simctl shutdown <udid>`. We don't need to
// wait — the CoreSimulator service handles the rest async. close()ing
// stopCh here keeps the supervisor's shutdownProcess flow consistent
// with android's path (which only returns once the child is gone).
func iosShutdownReal(p *simProcess) {
	if p == nil || p.SimID == "" {
		return
	}
	_ = shutdownIOSSim(p.SimID)
	if p.stopCh != nil {
		select {
		case <-p.stopCh:
			// already closed
		default:
			close(p.stopCh)
		}
	}
}

func init() {
	iosProcessAliveFn = iosProcessAliveReal
	iosShutdownFn = iosShutdownReal
}
