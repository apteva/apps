package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// remoteFFmpegExecutor runs the same ffmpeg command on a host managed
// by the `instances` app. Strategy lifted from media's remote_exec.go:
//
//   1. Pre-flight: ffmpeg + ffprobe installed on the remote (cached
//      after first success).
//   2. Resolve every asset.src to a URL the remote can curl (storage's
//      signed URLs cover the storage:N case; https:// pass-through).
//   3. SSH a single bash script via instances.instance_run_command
//      that downloads the inputs, runs ffmpeg with the same filter
//      graph the local executor builds, then multipart-POSTs the
//      output back to storage's /files endpoint and echoes a result
//      marker the sidecar parses.
//
// v0.1 is best-effort — the install probe + storage upload paths from
// media's remote executor aren't fully ported. When called and
// something's missing, returns a clear error rather than silently
// falling back; the caller can re-run with executor=local.
type remoteFFmpegExecutor struct {
	hostID int64
}

func (e *remoteFFmpegExecutor) Name() string { return "remote" }

func (e *remoteFFmpegExecutor) Render(
	ctx context.Context,
	app *sdk.AppCtx,
	edit *Edit,
	output Output,
	projectID string,
) (Result, error) {
	start := time.Now()

	// Pre-flight: instances app must be bound (best-effort check via
	// CallApp dry-run; instances will surface the error if not).
	if err := remotePreflight(app, e.hostID); err != nil {
		return Result{}, fmt.Errorf("remote preflight on host_id=%d: %w", e.hostID, err)
	}

	// Resolve every input to a URL the remote can fetch. storage:N →
	// signed URL via storage.files_get_url; https:// pass-through.
	track := edit.Timeline.Tracks[0]
	urls := make([]string, 0, len(track.Clips)+1)
	for i, c := range track.Clips {
		url, err := resolveAssetURL(app, c.Asset.Src)
		if err != nil {
			return Result{}, fmt.Errorf("clip[%d]: resolve %q: %w", i, c.Asset.Src, err)
		}
		urls = append(urls, url)
	}
	if s := edit.Timeline.Soundtrack; s != nil {
		url, err := resolveAssetURL(app, s.Src)
		if err != nil {
			return Result{}, fmt.Errorf("soundtrack resolve %q: %w", s.Src, err)
		}
		urls = append(urls, url)
	}

	// Build the same ffmpeg arg list the local executor uses, but
	// against local-on-remote file paths. We let bash assemble them
	// from the curl outputs by referring to ./in0, ./in1, … below.
	soundtrackIdx := -1
	if edit.Timeline.Soundtrack != nil {
		soundtrackIdx = len(track.Clips)
	}
	localPaths := make([]string, len(urls))
	for i := range urls {
		localPaths[i] = fmt.Sprintf("./in%d", i)
	}
	args := buildLocalFFmpegArgs(edit, output, localPaths, soundtrackIdx, "./out."+output.Format)
	cmd := shellEcho(ffmpegPath(), args)

	script := remoteRenderScript(urls, cmd, output.Format, projectID)

	app.Logger().Info("remote ffmpeg render", "host_id", e.hostID, "inputs", len(urls), "format", output.Format)

	res, err := remoteRunScript(app, e.hostID, script)
	if err != nil {
		return Result{FFmpegCommand: cmd}, fmt.Errorf("remote exec: %w", err)
	}

	storageID, parseErr := parseRemoteResult(res)
	if parseErr != nil {
		return Result{FFmpegCommand: cmd}, fmt.Errorf("remote result parse: %w (raw: %s)", parseErr, truncTail(res, 600))
	}

	return Result{
		Sync:          true,
		LocalPath:     fmt.Sprintf("storage://files/%d", storageID),
		DurationMS:    time.Since(start).Milliseconds(),
		FFmpegCommand: cmd,
	}, nil
}

// remotePreflight checks the instances app is reachable and the host
// exists. Full ffmpeg-install probe lives in media's
// remote_ffmpeg_install.go — for v0.1 we trust the operator set up
// the host and let the script's ffmpeg invocation fail loudly if not.
func remotePreflight(app *sdk.AppCtx, hostID int64) error {
	if app == nil {
		return errors.New("nil app ctx")
	}
	var probe struct {
		ID int64 `json:"id"`
	}
	err := app.PlatformAPI().CallAppResult("instances", "instance_get",
		map[string]any{"id": hostID}, &probe)
	if err != nil {
		return fmt.Errorf("instance_get failed (is instances bound?): %w", err)
	}
	if probe.ID != hostID {
		return fmt.Errorf("instances returned id=%d, want %d", probe.ID, hostID)
	}
	return nil
}

// remoteRenderScript assembles the bash script the remote runs.
// Convention: input URLs become ./in0, ./in1, … in the working dir,
// the ffmpeg command is appended verbatim, and the output is
// echoed back as APTEVA_RESULT:{...} for the parser.
//
// v0.1 stops at echoing — it does NOT upload to storage from the
// remote because that path needs media's outbound-bearer-token
// dance. Operator-mode follow-up.
func remoteRenderScript(urls []string, ffmpegCmd, format, projectID string) string {
	var b strings.Builder
	b.WriteString("set -eu -o pipefail\n")
	b.WriteString("WORKDIR=$(mktemp -d)\n")
	b.WriteString("trap 'rm -rf \"$WORKDIR\"' EXIT\n")
	b.WriteString("cd \"$WORKDIR\"\n")
	for i, u := range urls {
		fmt.Fprintf(&b, "curl -fsSL --retry 3 -o ./in%d %q\n", i, u)
	}
	b.WriteString(ffmpegCmd)
	b.WriteByte('\n')
	fmt.Fprintf(&b, "BYTES=$(stat -c %%s ./out.%s 2>/dev/null || stat -f %%z ./out.%s)\n", format, format)
	b.WriteString("SHA=$(shasum -a 256 ./out.* | awk '{print $1}')\n")
	// v0.1: emit a marker so the caller knows we got the bytes.
	// Upload-back-to-storage is a follow-up.
	b.WriteString(`echo "APTEVA_RESULT:{\"bytes\":${BYTES},\"sha256\":\"${SHA}\",\"format\":\"` + format + `\"}"` + "\n")
	return b.String()
}

// remoteRunScript SSHes via instances.instance_run_command. Returns
// the combined stdout/stderr.
func remoteRunScript(app *sdk.AppCtx, hostID int64, script string) (string, error) {
	var out struct {
		Stdout   string `json:"stdout"`
		Stderr   string `json:"stderr"`
		ExitCode int    `json:"exit_code"`
	}
	err := app.PlatformAPI().CallAppResult("instances", "instance_run_command",
		map[string]any{"id": hostID, "command": script, "timeout_seconds": 600}, &out)
	if err != nil {
		return out.Stdout + out.Stderr, err
	}
	if out.ExitCode != 0 {
		return out.Stdout + "\n" + out.Stderr, fmt.Errorf("remote exit_code=%d", out.ExitCode)
	}
	return out.Stdout, nil
}

// parseRemoteResult pulls the JSON object after the APTEVA_RESULT:
// marker line. Returns the storage id when present (v0.1 has none —
// just the bytes count, so we return 0 and the caller logs).
func parseRemoteResult(s string) (int64, error) {
	idx := strings.Index(s, "APTEVA_RESULT:")
	if idx < 0 {
		return 0, errors.New("APTEVA_RESULT marker missing")
	}
	tail := s[idx+len("APTEVA_RESULT:"):]
	end := strings.Index(tail, "\n")
	if end > 0 {
		tail = tail[:end]
	}
	// v0.1: we don't actually return a storage id from the remote path —
	// the bash script only computes the bytes. Parse just to verify the
	// shape; full upload-back-to-storage lands in the next iteration.
	_ = tail
	return 0, errors.New("v0.1 remote executor doesn't push bytes back to storage yet — use executor=local")
}
