package main

// Render worker pool. N goroutines, started in OnMount, each looping:
//   1. claim oldest pending render (atomic UPDATE … RETURNING)
//   2. pick an executor (local ffmpeg / cloudinary / …)
//   3. delegate to executor.Execute
//   4. mark the row ok / failed / cancelled
//
// Cancellation: a global map of render_id → context.CancelFunc lets
// the cancel-tool trigger immediate abort. The pool also shuts down
// cleanly on AppCtx.Done() so the SDK's lifecycle stays honest.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// activeCancels tracks cancel funcs of currently-running renders.
// Workers register on claim, deregister on completion. triggerCancel
// looks up + invokes; missing keys are a no-op (the row either hadn't
// started or already finished).
var (
	activeCancelsMu sync.Mutex
	activeCancels   = map[int64]context.CancelFunc{}
)

func registerCancel(id int64, cancel context.CancelFunc) {
	activeCancelsMu.Lock()
	defer activeCancelsMu.Unlock()
	activeCancels[id] = cancel
}

func deregisterCancel(id int64) {
	activeCancelsMu.Lock()
	defer activeCancelsMu.Unlock()
	delete(activeCancels, id)
}

// triggerCancel aborts a running render. Safe to call when the render
// isn't running — returns false in that case so the caller can decide
// whether to also flip the DB row.
func triggerCancel(id int64) bool {
	activeCancelsMu.Lock()
	cancel, ok := activeCancels[id]
	activeCancelsMu.Unlock()
	if !ok {
		return false
	}
	cancel()
	return true
}

// startRenderPool spawns size workers + returns. They run until
// ctx.Done() fires (SDK shutdown).
func startRenderPool(app *sdk.AppCtx, size int) {
	if size < 1 {
		size = 1
	}
	for i := 0; i < size; i++ {
		go renderWorker(app, i)
	}
	app.Logger().Info("render pool started", "size", size)
}

func renderWorker(app *sdk.AppCtx, id int) {
	log := app.Logger()
	db := app.AppDB()
	cfg := app.Config()

	scratchRoot := resolveScratchRoot(app, cfg.Get("render_scratch_dir"))
	if err := os.MkdirAll(scratchRoot, 0o755); err != nil {
		// Last-ditch fallback: try OS temp before giving up. The
		// worker exiting silently means rendered jobs sit pending
		// forever (the pool drains to zero) — far worse than using
		// a less-than-ideal scratch location for one boot.
		fallback := filepath.Join(os.TempDir(), "apteva-media-renders")
		if mkErr := os.MkdirAll(fallback, 0o755); mkErr != nil {
			log.Error("render worker: scratch root + fallback failed", "configured", scratchRoot, "fallback", fallback, "configured_err", err, "fallback_err", mkErr)
			return
		}
		log.Warn("render worker: configured scratch unwritable, using OS temp", "configured", scratchRoot, "fallback", fallback, "err", err)
		scratchRoot = fallback
	}
	timeoutSec := parseConfigIntFallback(cfg.Get("render_timeout_seconds"), 1800)
	ffmpegPath := strings.TrimSpace(cfg.Get("ffmpeg_path"))
	if ffmpegPath == "" {
		ffmpegPath = "ffmpeg"
	}
	outputFolder := strings.TrimSpace(cfg.Get("render_output_folder"))
	if outputFolder == "" {
		outputFolder = "/renders/"
	}

	local := &localExecutor{
		ffmpegPath:   ffmpegPath,
		scratchRoot:  scratchRoot,
		outputFolder: outputFolder,
	}

	// Remote backend is opt-in via render_host_id. >0 means "send
	// renders to that instances host_id". 0 (default) keeps everything
	// local. Construction-time failures (missing PUBLIC_URL, missing
	// outbound token) are logged once and the feature disables itself
	// for this worker — local + cloudinary stay available.
	hostID := int64(parseConfigIntFallback(cfg.Get("render_host_id"), 0))
	var remote *remoteExecutor
	if hostID > 0 {
		var err error
		remote, err = newRemoteExecutor(hostID, sharedRemoteInstaller(), local)
		if err != nil {
			log.Warn("render worker: remote backend disabled", "host_id", hostID, "err", err)
		} else {
			log.Info("render worker: remote backend enabled", "host_id", hostID)
		}
	}

	for {
		select {
		case <-app.Done():
			log.Info("render worker stopping", "id", id)
			return
		default:
		}

		row, err := claimNextPending(db)
		if errors.Is(err, sql.ErrNoRows) {
			select {
			case <-app.Done():
				return
			case <-time.After(5 * time.Second):
				continue
			}
		}
		if err != nil {
			log.Error("render worker: claim", "err", err)
			time.Sleep(2 * time.Second)
			continue
		}

		runOneRender(app, row, local, remote, timeoutSec)
	}
}

// runOneRender is the per-render orchestrator. It owns the DB
// lifecycle (mark ok/failed/cancelled) and the cancel registration;
// the actual produce-output work is delegated to the chosen executor.
func runOneRender(app *sdk.AppCtx, row *RenderRow, local *localExecutor, remote *remoteExecutor, timeoutSec int) {
	log := app.Logger()
	db := app.AppDB()

	// Wall-clock cap. Combined with the per-render cancel func so
	// either timeout OR explicit cancel terminates work promptly.
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()
	registerCancel(row.ID, cancel)
	defer deregisterCancel(row.ID)

	executor := selectExecutor(app, local, remote, row)
	log.Info("render claimed", "id", row.ID, "op", row.Operation, "executor", executor.Name())
	emitRenderStarted(app, row, executor.Name())

	outputFileID, err := executor.Execute(ctx, app, row)
	if err != nil {
		// Distinguish cancellation / timeout from a backend failure so
		// the row reflects the right terminal state. We check ctx.Err()
		// rather than the returned error: backends are encouraged to
		// propagate ctx.Err() upwards, but anything that lost the
		// context wrap still gets classified correctly.
		if ctx.Err() == context.Canceled {
			_ = renderMarkCancelled(db, row.ID)
			emitRenderCancelled(app, row.ID, row.ProjectID, row.Operation)
			log.Info("render cancelled", "id", row.ID, "executor", executor.Name())
			return
		}
		if ctx.Err() == context.DeadlineExceeded {
			msg := fmt.Sprintf("timeout after %ds", timeoutSec)
			_ = renderMarkFailed(db, row.ID, msg)
			emitRenderFailed(app, row.ID, row.ProjectID, row.Operation, msg)
			return
		}
		_ = renderMarkFailed(db, row.ID, err.Error())
		emitRenderFailed(app, row.ID, row.ProjectID, row.Operation, err.Error())
		return
	}

	outputFileIDStr := strconv.FormatInt(outputFileID, 10)
	if err := renderMarkOk(db, row.ID, outputFileIDStr); err != nil {
		log.Error("render mark ok", "id", row.ID, "err", err)
		return
	}
	emitRenderCompleted(app, row.ID, row.ProjectID, row.Operation, outputFileIDStr)
	log.Info("render done",
		"id", row.ID, "op", row.Operation,
		"executor", executor.Name(), "output_file_id", outputFileID)
}

func parseConfigIntFallback(s string, def int) int {
	if s == "" {
		return def
	}
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil || n <= 0 {
		return def
	}
	return n
}

// resolveScratchRoot picks the scratch directory for ffmpeg renders.
//
// Resolution order:
//
//   1. Operator override via render_scratch_dir config — absolute path
//      they explicitly set, used as-is.
//   2. ctx.DataDir() — the per-install writable dir the platform
//      provisions ("<persistentRoot>/<install_id>/" on local installs,
//      a Docker volume in containerized deploys). This is the right
//      default on a dev laptop AND a production Linux box; the SDK
//      hands us the platform's chosen path so we don't have to guess.
//   3. /data/renders — the legacy default, kept as a final fallback
//      for older platforms that don't set APTEVA_DATA_DIR yet. The
//      worker also catches any mkdir failure and falls back to
//      os.TempDir() at runtime, so even a misconfigured install
//      doesn't end up with a zero-worker render pool.
func resolveScratchRoot(app *sdk.AppCtx, override string) string {
	override = strings.TrimSpace(override)
	if override != "" {
		return override
	}
	if dd := app.DataDir(); dd != "" {
		return filepath.Join(dd, "renders")
	}
	return "/data/renders"
}
