package main

import (
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const defaultConnectedDurationSec = 4 * 60 * 60

// Keep the existing supported four-hour ceiling; provider/account caps may be lower.
const maximumConnectedDurationSec = 4 * 60 * 60

// Installation/project-context config is snapshotted when a call is created.
// Configuration changes never extend a running call or reset its watchdogs.
func durationSetting(config map[string]string, name string, fallback, low, high int) int {
	value, err := strconv.Atoi(strings.TrimSpace(config[name]))
	if err != nil || value < low || value > high {
		return fallback
	}
	return value
}
func configureCallDuration(ctx *sdk.AppCtx, row *callRow, override int) {
	var config map[string]string
	if ctx != nil {
		config = ctx.Config()
	}
	row.MaxDurationSec = durationSetting(config, "connected_call_max_duration_seconds", defaultConnectedDurationSec, 60, maximumConnectedDurationSec)
	if override > 0 {
		row.MaxDurationSec = max(60, min(override, maximumConnectedDurationSec))
	}
	row.MediaRecoveryTimeoutSec = durationSetting(config, "call_media_recovery_timeout_seconds", 120, 30, 600)
	setup := durationSetting(config, "call_setup_timeout_seconds", 3600, 60, 3600)
	if placed, err := time.Parse(time.RFC3339Nano, row.PlacedAt); err == nil {
		row.DeadlineAt = placed.Add(time.Duration(setup) * time.Second).Format(time.RFC3339Nano)
	}
}
func callDurationOrDefault(seconds int) int {
	if seconds < 60 || seconds > maximumConnectedDurationSec {
		return defaultConnectedDurationSec
	}
	return seconds
}
func mediaRecoveryOrDefault(seconds int) int {
	if seconds < 30 || seconds > 600 {
		return 120
	}
	return seconds
}

// Select the first deadline actually exceeded, keeping setup, connection
// duration and media recovery distinct. No audio amplitude/silence test exists.
func callExpiryReason(row *callRow, now time.Time) string {
	var earliest time.Time
	reason := ""
	consider := func(raw, why string) {
		at, err := time.Parse(time.RFC3339Nano, raw)
		if err == nil && !now.Before(at) && (earliest.IsZero() || at.Before(earliest)) {
			earliest, reason = at, why
		}
	}
	// A persisted termination request remains authoritative through a late
	// provider callback or a retry after a carrier-command failure.
	if row.TerminationReason == terminationTimeLimit && row.TerminationInitiator == "telephony" {
		return terminationTimeLimit
	}
	consider(row.ConnectedDeadlineAt, terminationTimeLimit)
	consider(row.MediaDeadlineAt, "media_timeout")
	consider(row.StateExpiresAt, "state_timeout")
	if row.DurationStartedAt == "" {
		consider(row.DeadlineAt, "setup_timeout")
	}
	return reason
}
