package main

// Android device logs via logcat. We dump the current buffer (-d) and
// return the tail; for the panel's live log view a future version can
// stream `adb logcat` continuously, but a polled tail keeps v0.1
// simple and matches the code app's dev-log polling shape.

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

func androidLogs(serial string, lines int) (string, error) {
	lines = normalizeLogLines(lines)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	// -d dumps and exits; -t <n> limits to the last n lines; -v brief
	// keeps each line compact.
	out, err := exec.CommandContext(ctx, "adb", "-s", serial, "logcat", "-d", "-t", fmt.Sprintf("%d", lines), "-v", "brief").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("adb logcat: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return string(out), nil
}

func normalizeLogLines(lines int) int {
	if lines <= 0 {
		return 200
	}
	if lines > 5000 {
		return 5000
	}
	return lines
}
