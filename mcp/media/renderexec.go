package main

// Pluggable backends for the render queue. The default is local
// ffmpeg (preserved bit-for-bit from v0.5.x); a Cloudinary backend
// kicks in when an operator binds the cloudinary integration to the
// optional `render_executor` role.
//
// Contract: an executor takes a queued RenderRow and produces the
// storage file_id of the resulting output. Lifecycle (claim, mark
// ok/failed/cancelled, register cancel) is owned by runOneRender —
// the executor is just the "produce the bytes, hand back a file_id"
// step. Cancellation flows through ctx.

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

	sdk "github.com/apteva/app-sdk"
)

// renderExecutor is the per-backend hook. Implementations live next
// to this file (localExecutor below; cloudinaryExecutor in
// cloudinary_exec.go).
type renderExecutor interface {
	// Name is a short tag used in logs ("local", "cloudinary").
	Name() string
	// Execute runs the operation and returns the storage file_id of
	// the produced output. ctx cancellation must abort in-flight work
	// promptly.
	Execute(ctx context.Context, app *sdk.AppCtx, row *RenderRow) (outputFileID int64, err error)
}

// selectExecutor picks the backend for one render. The local ffmpeg
// executor is the always-present fallback — even when an integration
// is bound, an unsupported slug or an op the cloud backend can't
// handle falls back here. ffmpeg is the default in three senses:
//
//  1. The render_executor manifest role is required:false — operators
//     don't have to bind anything.
//  2. selectExecutor's last-resort return is always &localExecutor{}.
//  3. The Cloudinary backend declines ops it can't handle (currently
//     concat + audio_extract); the orchestrator retries on local.
//
// We deliberately don't auto-fall-back on Cloudinary *runtime errors*
// — masking config issues (bad creds, quota exhaustion) by silently
// re-running on local would make those bugs invisible. An operator
// who wants to disable the cloud backend just clears the binding.
func selectExecutor(app *sdk.AppCtx, fallback *localExecutor, row *RenderRow) renderExecutor {
	bound := app.IntegrationFor("render_executor")
	if bound == nil {
		return fallback
	}
	switch bound.AppSlug {
	case "cloudinary":
		exec := &cloudinaryExecutor{bound: bound, fallback: fallback}
		// Per-op compatibility: ops the cloud backend doesn't model
		// well (concat, audio_extract) deliberately stay local. The
		// operator gets the cloud benefit for the common cases —
		// trim/resize/transcode/crop/extract_frame — without a
		// surprise failure on the long-tail ones.
		if !exec.supports(row.Operation) {
			app.Logger().Info("render: cloud backend can't handle op; using local",
				"op", row.Operation, "backend", bound.AppSlug)
			return fallback
		}
		return exec
	default:
		app.Logger().Warn("render: unknown render_executor backend; using local",
			"slug", bound.AppSlug)
		return fallback
	}
}

// ─── local ffmpeg executor ─────────────────────────────────────────
//
// Same logic that lived inline in runOneRender pre-v0.6: download
// sources to scratch, build argv from the per-op planner, exec ffmpeg
// under the supplied ctx, upload result back to storage. Behaviour is
// identical — only the call site moved.

type localExecutor struct {
	ffmpegPath   string
	scratchRoot  string
	outputFolder string
}

func (e *localExecutor) Name() string { return "local" }

func (e *localExecutor) Execute(ctx context.Context, app *sdk.AppCtx, row *RenderRow) (int64, error) {
	db := app.AppDB()
	sc := newStorageClient()

	jobDir := filepath.Join(e.scratchRoot, fmt.Sprintf("render-%d", row.ID))
	if err := os.MkdirAll(jobDir, 0o755); err != nil {
		return 0, fmt.Errorf("scratch mkdir: %w", err)
	}
	defer os.RemoveAll(jobDir)

	plan, err := buildPlan(row.Operation, row.SourceFileIDs, row.Params, row.OutputName)
	if err != nil {
		return 0, fmt.Errorf("build plan: %w", err)
	}

	// Download source(s) to scratch.
	srcPaths := make([]string, 0, len(row.SourceFileIDs))
	for _, fidStr := range row.SourceFileIDs {
		fid, err := strconv.ParseInt(fidStr, 10, 64)
		if err != nil {
			return 0, fmt.Errorf("source file_id %q not numeric", fidStr)
		}
		f, err := sc.GetFile(ctx, row.ProjectID, fid)
		if err != nil {
			return 0, fmt.Errorf("source lookup: %w", err)
		}
		local := filepath.Join(jobDir, fmt.Sprintf("src-%s%s", fidStr, filepath.Ext(f.Name)))
		fh, err := os.Create(local)
		if err != nil {
			return 0, fmt.Errorf("source create: %w", err)
		}
		dlErr := sc.DownloadContent(ctx, row.ProjectID, fid, fh)
		fh.Close()
		if dlErr != nil {
			return 0, fmt.Errorf("source download: %w", dlErr)
		}
		srcPaths = append(srcPaths, local)
	}

	outputPath := filepath.Join(jobDir, plan.Filename)
	args, err := materialiseArgs(plan.Args, srcPaths, jobDir)
	if err != nil {
		return 0, fmt.Errorf("materialise args: %w", err)
	}
	args = append(args, outputPath)

	cmd := exec.CommandContext(ctx, e.ffmpegPath, args...)
	stdout, _ := cmd.StdoutPipe()
	var stderrBuf strings.Builder
	cmd.Stderr = &stderrBuf
	if err := cmd.Start(); err != nil {
		return 0, fmt.Errorf("ffmpeg start: %w", err)
	}
	go forwardProgress(stdout, db, row.ID)
	if err := cmd.Wait(); err != nil {
		// Cancellation/timeout get the raw context error — the
		// orchestrator distinguishes them via ctx.Err().
		if ctx.Err() != nil {
			return 0, ctx.Err()
		}
		msg := strings.TrimSpace(stderrBuf.String())
		if msg == "" {
			msg = err.Error()
		}
		return 0, fmt.Errorf("ffmpeg: %s", msg)
	}

	out, err := os.Open(outputPath)
	if err != nil {
		return 0, fmt.Errorf("open output: %w", err)
	}
	defer out.Close()
	// Per-render output folder takes precedence over the install
	// default. Renders submitted before this column existed (or
	// without an explicit folder) fall back to e.outputFolder.
	folder := row.OutputFolder
	if folder == "" {
		folder = e.outputFolder
	}
	uploaded, err := sc.UploadRender(ctx, row.ProjectID, folder, plan.Filename, plan.ContentType, out)
	if err != nil {
		return 0, fmt.Errorf("upload: %w", err)
	}
	return uploaded, nil
}

// forwardProgress reads ffmpeg's -progress pipe:1 stream. Each chunk
// is a series of key=value lines ending with progress=...; we don't
// have total duration without an extra ffprobe call here, so v0.2's
// behaviour is preserved: bump to 50 on first activity, jump to 100
// on done (via renderMarkOk).
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

// errExecutorDeclined lets a backend bow out of an op without it
// being treated as a hard failure. Reserved for future use — today's
// declines happen earlier (selectExecutor.supports).
var errExecutorDeclined = errors.New("executor declined this operation")
