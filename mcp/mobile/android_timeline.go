package main

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"time"
)

// Android encoders can omit idle tails or extend a final packet while draining.
// Keep source timestamps within the take, hold its last image to the monotonic
// stop boundary, and trim shutdown latency. Never invent missing device audio.
func normaliseAndroidTake(ctx context.Context, path string, elapsed time.Duration, audio bool) error {
	probe, err := probeMedia(ctx, path)
	if err != nil {
		return fmt.Errorf("cannot verify Android capture: %w", err)
	}
	if audio && !probe.HasAudio {
		return fmt.Errorf("requested device audio was not captured")
	}
	bin, err := lookTool("ffmpeg")
	if err != nil {
		return problem("setup_required", "ffmpeg is required to finish Android recordings")
	}
	duration := strconv.FormatFloat(elapsed.Seconds(), 'f', 6, 64)
	out := path + ".timeline.mp4"
	defer os.Remove(out)
	args := []string{"-v", "error", "-nostdin", "-y", "-i", path, "-map", "0:v:0",
		"-vf", "setpts=PTS-STARTPTS,fps=30,tpad=stop_mode=clone:stop_duration=" + duration + ",trim=duration=" + duration,
		"-c:v", "libx264", "-preset", "veryfast", "-crf", "18", "-pix_fmt", "yuv420p"}
	if audio {
		args = append(args, "-map", "0:a:0", "-af", "aresample=async=1:first_pts=0,apad,atrim=duration="+duration,
			"-c:a", "aac", "-b:a", "256k")
	} else {
		args = append(args, "-an")
	}
	args = append(args, "-t", duration, "-movflags", "+faststart", out)
	if err := runLong(ctx, 4*time.Minute, bin, args...); err != nil {
		return fmt.Errorf("cannot finish Android timeline: %w", err)
	}
	if err := os.Chmod(out, 0600); err != nil {
		return err
	}
	if err := os.Rename(out, path); err != nil {
		return fmt.Errorf("replace %s: %w", filepath.Base(path), err)
	}
	return nil
}
