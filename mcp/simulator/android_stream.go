package main

// Android screen streaming via the device's built-in screenrecord,
// piped raw over adb. No scrcpy jar to push, no socket dance:
//
//   adb -s <serial> exec-out screenrecord --output-format=h264 \
//       --time-limit 180 --bit-rate 6000000 -
//
// screenrecord writes an H.264 Annex-B elementary stream to stdout;
// exec-out gives a clean binary pipe (no PTY newline translation).
// The 180s ceiling is screenrecord's hard max — when it elapses the
// process exits and the WebSocket session ends; the panel reconnects
// (sims_stream_url tokens last an hour). A future version can respawn
// in-process for a seamless loop.

import (
	"context"
	"io"
	"os/exec"
)

func startAndroidScreenStream(ctx context.Context, serial string) (*exec.Cmd, io.Reader, error) {
	cmd := exec.CommandContext(ctx, "adb", "-s", serial, "exec-out",
		"screenrecord",
		"--output-format=h264",
		"--time-limit", "180",
		"--bit-rate", "6000000",
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
