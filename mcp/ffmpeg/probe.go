// probe.go — ffmpeg_probe implementation.
//
// Wraps ffprobe in JSON mode and returns the parsed output as a
// map[string]any so MCP callers can reach fields like
// `.streams[0].codec_name` or `.format.duration` directly.
//
// Wire shape:
//   ffprobe -v error -print_format json -show_streams -show_format URL

package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) toolProbe(_ *sdk.AppCtx, args map[string]any) (any, error) {
	url := strings.TrimSpace(strArg(args, "url"))
	if url == "" {
		return nil, errors.New("url required")
	}
	timeout := time.Duration(boundedTimeout(args, 8)) * time.Second

	c, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	cmd := exec.CommandContext(c, a.ffprobePath,
		"-v", "error",
		"-print_format", "json",
		"-show_streams",
		"-show_format",
		url,
	)
	stdoutPipe, err := cmd.StdoutPipe()
	if err != nil {
		return nil, err
	}
	stderrPipe, err := cmd.StderrPipe()
	if err != nil {
		return nil, err
	}
	start := time.Now()
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	out, _ := io.ReadAll(stdoutPipe)
	stderrBytes, _ := io.ReadAll(stderrPipe)
	if err := cmd.Wait(); err != nil {
		return nil, fmt.Errorf("ffprobe exited: %w (stderr: %s)", err, snippet(stderrBytes))
	}
	var parsed map[string]any
	if err := json.Unmarshal(out, &parsed); err != nil {
		return nil, fmt.Errorf("ffprobe JSON parse: %w (raw: %s)", err, snippet(out))
	}
	parsed["took_ms"] = time.Since(start).Milliseconds()
	return parsed, nil
}
