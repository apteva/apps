package main

// iOS device logs. simctl can stream the system log; for a polled tail
// matching the android path we spawn a short-lived `simctl spawn log
// show` over a recent window. idb also offers `idb log`, but
// `simctl spawn log show --last` needs no companion and returns a
// bounded chunk, which fits the poll model better.

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

func iosLogs(udid string, lines int) (string, error) {
	if lines <= 0 {
		lines = 200
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	// `log show --last 2m` over the booted device's log store. We cap
	// the window rather than the line count (log show has no -n), then
	// tail client-side to `lines`.
	out, err := exec.CommandContext(ctx, "xcrun", "simctl", "spawn", udid,
		"log", "show", "--last", "2m", "--style", "compact").CombinedOutput()
	if err != nil {
		return "", fmt.Errorf("simctl log show: %w (%s)", err, strings.TrimSpace(string(out)))
	}
	return tailLines(string(out), lines), nil
}

// tailLines returns the last n lines of s.
func tailLines(s string, n int) string {
	all := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(all) <= n {
		return s
	}
	return strings.Join(all[len(all)-n:], "\n")
}
