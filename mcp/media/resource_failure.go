package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"
	"sync"
)

type mediaLease struct {
	mu      sync.Mutex
	cancel  context.CancelFunc
	failure error
}

// SIGKILL is possible OOM, not proof; either way it must stop heavy fallback.
func resourceFailure(err error, output string, exit int) error {
	detail := strings.ToLower(output)
	if err != nil {
		detail += " " + strings.ToLower(err.Error())
	}
	for _, signature := range []string{"media_resource_exhausted", "out of memory", "cannot allocate memory", "std::bad_alloc", "signal: killed", "exit status 137"} {
		if strings.Contains(detail, signature) {
			return fmt.Errorf("media_resource_exhausted: process killed or memory allocation failed; fallback stopped")
		}
	}
	if exit == 137 || exit == -9 {
		return fmt.Errorf("media_resource_exhausted: process killed (possible OOM); fallback stopped")
	}
	return nil
}
func markMediaResourceFailure(ctx context.Context, err error, output string, exit int) error {
	if existing := mediaWorkFailure(ctx); existing != nil {
		return existing
	}
	if ctx.Err() != nil {
		return nil
	}
	failure := resourceFailure(err, output, exit)
	if failure == nil {
		return mediaWorkFailure(ctx)
	}
	if lease, ok := ctx.Value(mediaLeaseKey{}).(*mediaLease); ok {
		lease.mu.Lock()
		if lease.failure == nil {
			lease.failure = failure
		}
		lease.mu.Unlock()
		lease.cancel()
	}
	return failure
}
func mediaWorkFailure(ctx context.Context) error {
	if lease, ok := ctx.Value(mediaLeaseKey{}).(*mediaLease); ok {
		lease.mu.Lock()
		defer lease.mu.Unlock()
		return lease.failure
	}
	return nil
}
func cropWorkError(raw []byte, err error) []byte {
	p := map[string]any{}
	_ = json.Unmarshal(raw, &p)
	code := "media_processing_interrupted"
	if resourceFailure(err, "", 0) != nil {
		code = "media_resource_exhausted"
	}
	p["runtime_error"] = code
	for _, k := range []string{"crop_w", "crop_h", "crop_x", "crop_y", "crop_path", "crop_version"} {
		delete(p, k)
	}
	b, _ := json.Marshal(p)
	return b
}
func cropProcessingError(raw []byte) error {
	var p map[string]any
	_ = json.Unmarshal(raw, &p)
	if code := stringJSONValue(p["runtime_error"]); code != "" {
		return &renderInputError{Code: code, Message: "Media processing stopped; no further fallback or render was started."}
	}
	return nil
}

// Decoder flags precede every input. Render encoder threads remain configurable.
func limitedFFmpegArgs(args []string) []string {
	out := []string{"-filter_threads", "1", "-filter_complex_threads", "1"}
	for _, arg := range args {
		if arg == "-i" {
			out = append(out, "-threads", "1")
		}
		out = append(out, arg)
	}
	return out
}
func mediaFFmpegCommand(ctx context.Context, path string, args []string) *exec.Cmd {
	bounded := limitedFFmpegArgs(args)
	// Commands passed here are complete, unlike a planner's argument fragment.
	if len(args) > 1 && (len(args) < 3 || args[len(args)-3] != "-threads") {
		last := bounded[len(bounded)-1]
		bounded = append(bounded[:len(bounded)-1], "-threads", "1", last)
	}
	cmd := exec.CommandContext(ctx, path, bounded...)
	configureRuntimeProcess(cmd)
	return cmd
}

// Parent-shell invocation lets OOM abort even soft-failure candidate loops.
const remoteFFmpegResourceGuard = `
media_ffmpeg() {
  MEDIA_FFMPEG_STATUS=0
  "$FFMPEG" -threads 1 -filter_threads 1 -filter_complex_threads 1 "$@" 2>"$WORK/resource-ffmpeg.log" || MEDIA_FFMPEG_STATUS=$?
  if [ "$MEDIA_FFMPEG_STATUS" -eq 137 ] || grep -qiE 'out of memory|cannot allocate memory|std::bad_alloc' "$WORK/resource-ffmpeg.log"; then
    echo 'media_resource_exhausted: FFmpeg killed or memory allocation failed; fallback stopped' >&2
    exit 137
  fi
  cat "$WORK/resource-ffmpeg.log" >&2
  return "$MEDIA_FFMPEG_STATUS"
}
`
