package main

import (
	"context"
	"errors"
	"os/exec"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// defaultBlockedKeycodes: power, sleep and soft sleep turn the phone off
// mid-session; operators can replace the list.
const defaultBlockedKeycodes = "26,223,276"

// operatorSettings fills the runner options shared by the sidecar and the
// standalone runner from their string settings.
func operatorSettings(cfg *runnerConfig, get func(string) string) error {
	cfg.PresenceInterval = 3 * time.Second
	cfg.HealthInterval = 10 * time.Second
	grace, err := boundedSetting(get("offline_grace_seconds"), 300, 0, 86400)
	if err != nil {
		return errors.New("offline_grace_seconds must be 0..86400")
	}
	cfg.OfflineGrace = time.Duration(grace) * time.Second
	if cfg.StreamViewers, err = boundedSetting(get("max_stream_viewers"), 4, 1, 20); err != nil {
		return errors.New("max_stream_viewers must be 1..20")
	}
	raw := get("blocked_keycodes")
	if strings.TrimSpace(raw) == "" {
		raw = defaultBlockedKeycodes
	}
	cfg.BlockedKeycodes = map[int]bool{}
	for _, part := range strings.Split(raw, ",") {
		part = strings.TrimSpace(part)
		if part == "" || part == "none" {
			continue
		}
		n, err := strconv.Atoi(part)
		if err != nil || n < 0 || n > 1000 {
			return errors.New("blocked_keycodes must be a comma-separated list of Android keycodes")
		}
		cfg.BlockedKeycodes[n] = true
	}
	return nil
}

func boundedSetting(raw string, def, min, max int) (int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil || n < min || n > max {
		return 0, errors.New("out of range")
	}
	return n, nil
}

func appVersion() string {
	m, err := sdk.ParseManifest(manifestBytes)
	if err != nil {
		return ""
	}
	return m.Version
}

func toolVersion(ctx context.Context, name string, args ...string) string {
	bin, err := exec.LookPath(name)
	if err != nil {
		if bin, err = lookTool(name); err != nil {
			return ""
		}
	}
	out, err := runCommand(ctx, bin, args...)
	if err != nil {
		return ""
	}
	line, _, _ := strings.Cut(strings.TrimSpace(string(out)), "\n")
	return strings.TrimSpace(line)
}

// healthReport is the runner's authenticated /v1/health: versions, phones
// with their health, and active sessions and streams. No secrets.
func (e *engine) healthReport(ctx context.Context) map[string]any {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	sessions := map[string]int{}
	if all, err := e.allActive(); err == nil {
		for _, s := range all {
			sessions[s.Status]++
		}
	}
	return map[string]any{
		"status": "ok", "protocol": protocol, "runner_version": appVersion(),
		"adb_version":    toolVersion(ctx, "adb", "version"),
		"scrcpy_version": toolVersion(ctx, "scrcpy", "--version"),
		"phones":         e.health.snapshot(),
		"sessions":       sessions,
		"streams":        e.streams.count(),
	}
}
