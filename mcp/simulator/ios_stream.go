package main

// iOS screen streaming via idb's video-stream, the simulator analog of
// android's screenrecord:
//
//   idb video-stream --udid <udid> --format h264 --fps 30 --compression-quality 0.7 -
//
// idb_companion must be running for the udid; `idb` auto-spawns a
// companion for booted simulators, so a bare `idb video-stream` works
// once idb_companion is installed (the capability probe enforces that).
// Output is an H.264 Annex-B stream on stdout, framed identically to
// the android path.

import (
	"context"
	"io"
	"os/exec"
)

func startIOSVideoStream(ctx context.Context, udid string) (*exec.Cmd, io.Reader, error) {
	cmd := exec.CommandContext(ctx, "idb", "video-stream",
		"--udid", udid,
		"--format", "h264",
		"--fps", "30",
		"--compression-quality", "0.7",
		"-",
	)
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, nil, err
	}
	if err := cmd.Start(); err != nil {
		return nil, nil, err
	}
	return cmd, stdout, nil
}
