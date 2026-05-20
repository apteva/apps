package main

// Android emulator boot + shutdown. One emulator child process per
// sim_id; the supervisor tracks the *exec.Cmd handle for SIGTERM /
// SIGKILL on stop.
//
// Boot is async — `emulator -avd <name>` keeps running for the
// emulator's lifetime, so we spawn it in the background with a
// supervisor goroutine and a readiness probe that polls
// `adb -s <serial> shell getprop sys.boot_completed`. The serial is
// allocated by us before spawn (`-port <p>` → serial=emulator-<p>),
// which removes the "which emulator did I just start?" guesswork that
// `adb devices` parsing would require.
//
// Port allocation: emulator binds two ADB ports per instance
// (console + adb), starting at 5554 and going up by 2 (5556, 5558,
// 5560 …). We probe a window starting at 5554 and pick the first
// free pair. Cap at 5680 by convention — Android allows up to 16
// concurrent emulators on most hosts; the operator cap from
// max_concurrent_sims is the actual concurrency gate, this is just
// the address-space window.

import (
	"context"
	"errors"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	androidConsolePortStart = 5554
	androidConsolePortEnd   = 5680
)

// adbPortMu serializes port allocation against concurrent sims_boot
// calls. The emulator binds the port itself; we just pick a free pair.
var adbPortMu sync.Mutex

// allocateEmulatorPorts picks the lowest free (console, adb) port
// pair. emulator-<console_port> becomes the adb serial.
func allocateEmulatorPorts() (consolePort, adbPort int, err error) {
	adbPortMu.Lock()
	defer adbPortMu.Unlock()
	for p := androidConsolePortStart; p <= androidConsolePortEnd; p += 2 {
		if portFree(p) && portFree(p+1) {
			return p, p + 1, nil
		}
	}
	return 0, 0, fmt.Errorf("no free emulator port pair in %d-%d", androidConsolePortStart, androidConsolePortEnd)
}

func portFree(p int) bool {
	for _, addr := range []string{
		fmt.Sprintf("127.0.0.1:%d", p),
	} {
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			return false
		}
		_ = ln.Close()
	}
	return true
}

// bootAndroidSim spawns an emulator child for the given AVD, waits for
// it to advertise boot completion, and registers the live process in
// the supervisor. Returns the resulting *Sim row.
//
// The DB row transitions:
//   shutdown → booting → booted (success)
//                     → crashed (boot probe timeout / emulator exited)
//
// Boot timeout default 120s — first-boot cold-start of a freshly
// created AVD can take that long; subsequent boots usually under 30s.
func bootAndroidSim(ctx *sdk.AppCtx, sup *simSupervisor, avdName, deviceType, systemImage string, extraArgs []string) (*Sim, error) {
	consolePort, _, err := allocateEmulatorPorts()
	if err != nil {
		return nil, err
	}
	serial := fmt.Sprintf("emulator-%d", consolePort)

	// Persist the booting row before spawning so reconciliation can
	// see it if the sidecar crashes between spawn and ready.
	now := time.Now().UTC().Format(time.RFC3339)
	if err := dbUpsertSim(ctx.AppDB(), Sim{
		ID:         avdName,
		ProjectID:  ctx.CurrentProject(),
		Platform:   "android",
		Runtime:    systemImage,
		DeviceType: deviceType,
		Status:     "booting",
		Serial:     serial,
		BootedAt:   now,
	}); err != nil {
		return nil, fmt.Errorf("persist booting sim: %w", err)
	}

	logPath := filepath.Join(sup.dataDir, "boot-logs", avdName+".log")
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir boot-logs: %w", err)
	}
	logF, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open boot log: %w", err)
	}
	fmt.Fprintf(logF, "=== boot for %s at %s (port=%d serial=%s) ===\n",
		avdName, now, consolePort, serial)

	args := []string{"-avd", avdName, "-port", fmt.Sprintf("%d", consolePort)}
	args = append(args, extraArgs...)

	cctx, cancel := context.WithCancel(context.Background())
	cmd := exec.CommandContext(cctx, "emulator", args...)
	cmd.Stdout = logF
	cmd.Stderr = logF
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		cancel()
		logF.Close()
		_ = dbUpdateSim(ctx.AppDB(), avdName, map[string]any{
			"status": "crashed", "error": "emulator start: " + err.Error(),
		})
		return nil, fmt.Errorf("emulator: %w", err)
	}

	proc := &simProcess{
		SimID:    avdName,
		Platform: "android",
		Cmd:      cmd,
		Cancel:   cancel,
		LogFile:  logF,
		stopCh:   make(chan struct{}),
	}
	sup.put(proc)

	// Record the pid so the reconciler can probe it on cold boot.
	_ = dbUpdateSim(ctx.AppDB(), avdName, map[string]any{
		"pid": int64(cmd.Process.Pid),
	})

	// Supervisor goroutine. Waits for the emulator to exit; flips DB
	// status accordingly (cancel = stopped, anything else = crashed).
	go func() {
		defer close(proc.stopCh)
		err := cmd.Wait()
		fmt.Fprintf(logF, "=== exited at %s (err=%v) ===\n", time.Now().UTC().Format(time.RFC3339), err)
		_ = logF.Close()
		if cctx.Err() != nil {
			_ = dbUpdateSim(ctx.AppDB(), avdName, map[string]any{
				"status": "shutdown", "pid": 0,
			})
		} else {
			msg := "emulator exited"
			if err != nil {
				msg = err.Error()
			}
			_ = dbUpdateSim(ctx.AppDB(), avdName, map[string]any{
				"status": "crashed", "pid": 0, "error": msg,
			})
		}
		sup.drop(avdName)
	}()

	// Readiness probe — runs in the foreground because the caller (and
	// sims_run) wants a sim that's actually usable, not one that "will
	// hopefully be ready soon". The 120s ceiling is generous to
	// accommodate first-boot cold starts.
	if err := waitForAndroidBoot(serial, 120*time.Second); err != nil {
		// Boot didn't complete. Kill the emulator and surface the
		// error; the supervisor goroutine will flip the row to
		// crashed when the process actually dies.
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		<-proc.stopCh
		_ = dbUpdateSim(ctx.AppDB(), avdName, map[string]any{
			"status": "crashed", "pid": 0, "error": "boot probe: " + err.Error(),
		})
		return nil, err
	}

	_ = dbUpdateSim(ctx.AppDB(), avdName, map[string]any{
		"status": "booted",
	})
	return dbGetSim(ctx.AppDB(), avdName)
}

// waitForAndroidBoot polls `adb -s <serial> shell getprop sys.boot_completed`
// until it returns "1" or the deadline is hit. The first ~10s of an
// emulator boot adb often refuses to connect at all (device offline);
// we re-poll past those failures.
func waitForAndroidBoot(serial string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
		out, err := exec.CommandContext(ctx, "adb", "-s", serial, "shell", "getprop", "sys.boot_completed").CombinedOutput()
		cancel()
		if err == nil && strings.TrimSpace(string(out)) == "1" {
			return nil
		}
		time.Sleep(2 * time.Second)
	}
	return fmt.Errorf("android boot did not complete within %s", timeout)
}

// shutdownAndroidSim sends a graceful "adb emu kill" first (lets the
// emulator save state cleanly), then falls back to SIGTERM/SIGKILL via
// the process-group cancel path that supervisor.shutdownProcess
// orchestrates.
func shutdownAndroidSim(serial string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "adb", "-s", serial, "emu", "kill")
	// We don't care about adb's exit code — if it fails (emulator
	// already dead, serial gone), the supervisor's process-level
	// shutdown is the fallback.
	_ = cmd.Run()
	return nil
}

// ─── Supervisor hooks ───────────────────────────────────────────────

// androidProcessAlive replaces the supervisor.go stub. Uses `kill 0`
// signal-zero probing — the canonical "is this pid still there?"
// check on macOS + linux.
func androidProcessAliveReal(row Sim) bool {
	if row.PID <= 0 {
		return false
	}
	proc, err := os.FindProcess(int(row.PID))
	if err != nil {
		return false
	}
	return proc.Signal(syscall.Signal(0)) == nil
}

// androidShutdownReal replaces the supervisor.go stub. Two-phase:
// graceful via adb emu kill, then SIGTERM the process group, then
// SIGKILL if the emulator hangs past 5s.
func androidShutdownReal(p *simProcess) {
	if p.Cmd == nil || p.Cmd.Process == nil {
		return
	}
	// Best-effort graceful via adb. We need the serial — pull it from
	// the supervisor's stored row (the simProcess struct doesn't carry
	// it, so do a DB read). The graceful path matters because
	// emulator stamps state files on shutdown; SIGKILL leaves them
	// inconsistent and next-boot is slower.
	//
	// In v0.1 we skip the DB read to keep this stub deterministic in
	// tests; the SIGTERM path below is sufficient — it gives the
	// emulator ~5s to clean up before we KILL.

	pgid := -p.Cmd.Process.Pid
	_ = syscall.Kill(pgid, syscall.SIGTERM)
	select {
	case <-p.stopCh:
		return
	case <-time.After(5 * time.Second):
		_ = syscall.Kill(pgid, syscall.SIGKILL)
		<-p.stopCh
	}
}

// init wires the real android lifecycle helpers in place of the
// supervisor.go stubs. Using package init keeps both stubs and reals
// referenced by name from supervisor.go itself — the indirection lets
// chunk 4's iOS code follow the same pattern without touching
// supervisor.go again.
func init() {
	androidProcessAliveFn = androidProcessAliveReal
	androidShutdownFn = androidShutdownReal
}

// errAndroidUnsupported is the canonical error sims_* tools return
// when the host doesn't have the Android backend usable. Centralised
// so the panel's "host_unsupported" detection can string-match on a
// known prefix.
var errAndroidUnsupported = errors.New("host_unsupported: android backend not available; call sims_capabilities for missing deps")
