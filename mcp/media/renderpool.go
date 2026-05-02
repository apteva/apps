package main

// Render worker pool. N goroutines, started in OnMount, each looping:
//   1. claim oldest pending render (atomic UPDATE … RETURNING)
//   2. download source(s) to scratch dir
//   3. build ffmpeg argv from the operation
//   4. run ffmpeg with -progress pipe:1, parsing chunks → progress_pct
//   5. upload the produced file back to storage
//   6. mark the row ok / failed; clean scratch
//
// Cancellation: a global map of render_id → context.CancelFunc lets
// the cancel-tool trigger an immediate ffmpeg kill. The pool also
// shuts down cleanly on AppCtx.Done() so the SDK's lifecycle stays
// honest.

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// activeCancels tracks the ffmpeg cancel funcs of currently-running
// renders. Goroutines register on claim, deregister on completion.
// Cancel calls look up + invoke; missing keys are a no-op (the row
// either hadn't started or already finished).
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

// triggerCancel kills the ffmpeg child of a running render. Safe to
// call when the render isn't running — returns false in that case so
// the caller can decide whether to also flip the DB row.
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

	scratchRoot := strings.TrimSpace(cfg.Get("render_scratch_dir"))
	if scratchRoot == "" {
		scratchRoot = "/data/renders"
	}
	if err := os.MkdirAll(scratchRoot, 0o755); err != nil {
		log.Error("render worker: scratch root", "err", err)
		return
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

	for {
		select {
		case <-app.Done():
			log.Info("render worker stopping", "id", id)
			return
		default:
		}

		row, err := claimNextPending(db)
		if errors.Is(err, sql.ErrNoRows) {
			// Nothing to do. Sleep with cancellation awareness so
			// shutdown isn't held up by the idle delay.
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

		runOneRender(app, row, ffmpegPath, scratchRoot, outputFolder, timeoutSec)
	}
}

// runOneRender owns a single render's full lifecycle. Splitting it
// out keeps the worker loop easy to read; each terminal state
// (ok/failed/cancelled) writes its own DB row update.
func runOneRender(app *sdk.AppCtx, row *RenderRow, ffmpegPath, scratchRoot, outputFolder string, timeoutSec int) {
	log := app.Logger()
	db := app.AppDB()
	sc := newStorageClient()

	jobDir := filepath.Join(scratchRoot, fmt.Sprintf("render-%d", row.ID))
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		_ = renderMarkFailed(db, row.ID, "scratch mkdir: "+err.Error())
		return
	}
	// Cleanup is unconditional: we always want the scratch dir gone
	// when this render's terminal state is written, regardless of
	// outcome (ok/failed/cancelled/panic).
	defer os.RemoveAll(jobDir)

	plan, err := buildPlan(row.Operation, row.SourceFileIDs, row.Params, row.OutputName)
	if err != nil {
		_ = renderMarkFailed(db, row.ID, "build plan: "+err.Error())
		return
	}

	// Wall-clock cap. Combined with the per-render cancel func so
	// either timeout OR explicit cancel terminates ffmpeg promptly.
	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(timeoutSec)*time.Second)
	defer cancel()
	registerCancel(row.ID, cancel)
	defer deregisterCancel(row.ID)

	// 1. Download source(s). For the v0.2 ops we have, all sources
	// are single-file except concat (handled below).
	srcPaths := make([]string, 0, len(row.SourceFileIDs))
	for _, fidStr := range row.SourceFileIDs {
		fid, err := strconv.ParseInt(fidStr, 10, 64)
		if err != nil {
			_ = renderMarkFailed(db, row.ID, fmt.Sprintf("source file_id %q not numeric", fidStr))
			return
		}
		f, err := sc.GetFile(ctx, row.ProjectID, fid)
		if err != nil {
			_ = renderMarkFailed(db, row.ID, "source lookup: "+err.Error())
			return
		}
		// Use the storage-side basename when present so ffmpeg can
		// pick up the right demuxer from extension.
		local := filepath.Join(jobDir, fmt.Sprintf("src-%s%s", fidStr, filepath.Ext(f.Name)))
		fh, err := os.Create(local)
		if err != nil {
			_ = renderMarkFailed(db, row.ID, "source create: "+err.Error())
			return
		}
		dlErr := sc.DownloadContent(ctx, row.ProjectID, fid, fh)
		fh.Close()
		if dlErr != nil {
			_ = renderMarkFailed(db, row.ID, "source download: "+dlErr.Error())
			return
		}
		srcPaths = append(srcPaths, local)
	}

	outputPath := filepath.Join(jobDir, plan.Filename)

	// 2. Substitute the {input}/{concat_list} placeholder in argv.
	args, err := materialiseArgs(plan.Args, srcPaths, jobDir)
	if err != nil {
		_ = renderMarkFailed(db, row.ID, "materialise args: "+err.Error())
		return
	}
	args = append(args, outputPath)

	// 3. Run ffmpeg with progress pipe.
	cmd := exec.CommandContext(ctx, ffmpegPath, args...)
	stdout, _ := cmd.StdoutPipe()
	var stderrBuf strings.Builder
	cmd.Stderr = &stderrBuf
	if err := cmd.Start(); err != nil {
		_ = renderMarkFailed(db, row.ID, "ffmpeg start: "+err.Error())
		return
	}

	// Tail -progress chunks. Each chunk ends with `progress=continue`
	// or `progress=end`; we forward `out_time_ms` as percentage if
	// we know the source duration. Without total duration we just
	// keep the row alive (progress_pct stays 0 → 100 jump on done).
	go forwardProgress(stdout, db, row.ID)

	if err := cmd.Wait(); err != nil {
		// Distinguish cancellation (context error) from ffmpeg failure.
		if ctx.Err() == context.Canceled {
			_ = renderMarkCancelled(db, row.ID)
			log.Info("render cancelled", "id", row.ID)
			return
		}
		if ctx.Err() == context.DeadlineExceeded {
			_ = renderMarkFailed(db, row.ID, fmt.Sprintf("timeout after %ds", timeoutSec))
			return
		}
		msg := strings.TrimSpace(stderrBuf.String())
		if msg == "" {
			msg = err.Error()
		}
		_ = renderMarkFailed(db, row.ID, "ffmpeg: "+msg)
		return
	}

	// 4. Upload result. Stream the file rather than reading it all
	// into memory — outputs can be >100MB.
	out, err := os.Open(outputPath)
	if err != nil {
		_ = renderMarkFailed(db, row.ID, "open output: "+err.Error())
		return
	}
	defer out.Close()

	uploaded, err := sc.UploadRender(ctx, row.ProjectID, outputFolder, plan.Filename, plan.ContentType, out)
	if err != nil {
		_ = renderMarkFailed(db, row.ID, "upload: "+err.Error())
		return
	}

	if err := renderMarkOk(db, row.ID, strconv.FormatInt(uploaded, 10)); err != nil {
		log.Error("render mark ok", "id", row.ID, "err", err)
		return
	}
	log.Info("render done", "id", row.ID, "op", row.Operation, "output_file_id", uploaded)
}

// materialiseArgs replaces placeholder tokens with real paths. For
// concat, it also writes the demuxer list-file into jobDir.
func materialiseArgs(template, srcPaths []string, jobDir string) ([]string, error) {
	out := make([]string, 0, len(template))
	for _, a := range template {
		switch a {
		case "{input}":
			if len(srcPaths) != 1 {
				return nil, fmt.Errorf("{input} placeholder needs exactly 1 source, got %d", len(srcPaths))
			}
			out = append(out, srcPaths[0])
		case "{concat_list}":
			listPath := filepath.Join(jobDir, "concat-list.txt")
			f, err := os.Create(listPath)
			if err != nil {
				return nil, err
			}
			for _, p := range srcPaths {
				// concat demuxer wants `file '<path>'` per line.
				if _, err := fmt.Fprintf(f, "file '%s'\n", p); err != nil {
					f.Close()
					return nil, err
				}
			}
			f.Close()
			out = append(out, listPath)
		default:
			out = append(out, a)
		}
	}
	return out, nil
}

// forwardProgress reads ffmpeg's -progress pipe:1 stream. Each
// chunk is a series of key=value lines ending with progress=...
// We don't have total duration without an extra ffprobe call here,
// so v0.2 just bumps progress to 50 on first activity and 100 on
// completion. A proper percentage requires probing the source
// upfront — easy v0.3 follow-up.
func forwardProgress(r io.ReadCloser, db *sql.DB, id int64) {
	defer r.Close()
	scanner := bufio.NewScanner(r)
	bumped := false
	for scanner.Scan() {
		if !bumped && strings.HasPrefix(scanner.Text(), "out_time_ms=") {
			_ = renderUpdateProgress(db, id, 50)
			bumped = true
		}
	}
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
