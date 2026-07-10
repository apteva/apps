package main

// iOS device logs. simctl can stream the system log; for a polled tail
// matching the android path we spawn a short-lived `simctl spawn log
// show` over a recent window. idb also offers `idb log`, but
// `simctl spawn log show --last` needs no companion and returns a
// bounded chunk, which fits the poll model better.

import (
	"bufio"
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

func iosLogs(udid string, lines int) (string, error) {
	lines = normalizeLogLines(lines)
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	// `log show --last 2m` over the booted device's log store. We cap
	// the window rather than the line count (log show has no -n), then
	// tail client-side to `lines`.
	cmd := exec.CommandContext(ctx, "xcrun", "simctl", "spawn", udid,
		"log", "show", "--last", "2m", "--style", "compact")
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return "", err
	}
	stderr := &cappedBuffer{limit: 64 << 10}
	cmd.Stderr = stderr
	if err := cmd.Start(); err != nil {
		return "", err
	}
	ring := make([]string, lines)
	count := 0
	scanner := bufio.NewScanner(stdout)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		ring[count%lines] = scanner.Text()
		count++
	}
	scanErr := scanner.Err()
	err = cmd.Wait()
	if err != nil {
		return "", fmt.Errorf("simctl log show: %w (%s)", err, strings.TrimSpace(stderr.String()))
	}
	if scanErr != nil {
		return "", fmt.Errorf("read simctl logs: %w", scanErr)
	}
	if count == 0 {
		return "", nil
	}
	kept := count
	if kept > lines {
		kept = lines
	}
	out := make([]string, 0, kept)
	start := count - kept
	for i := start; i < count; i++ {
		out = append(out, ring[i%lines])
	}
	return strings.Join(out, "\n"), nil
}

type cappedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	written := len(p)
	remaining := b.limit - b.Len()
	if remaining <= 0 {
		return written, nil
	}
	if len(p) > remaining {
		p = p[:remaining]
	}
	_, err := b.Buffer.Write(p)
	return written, err
}
