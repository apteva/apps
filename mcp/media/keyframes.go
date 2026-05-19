package main

// Keyframe-set calculation + config readers shared by the local and
// remote indexer paths.
//
// "Keyframes" here = the timeline storyboard frames the indexer
// generates every keyframe_interval_seconds seconds across a video.
// Distinct from "the canonical thumbnail" (a single
// best-representative frame, picked via the multi-seek + luma-check
// path). Keyframes are additive: the canonical thumbnail still
// generates first; keyframes follow.
//
// Defaults (per-install configurable via app.Config()):
//   keyframe_interval_seconds  30
//   keyframe_max_count         60   (caps storage on very long videos)
//   keyframes_enabled          true
//
// For a 10-minute video: 20 keyframes. For an hour-long video:
// 60 keyframes (capped, spaced every minute instead of every 30s).
// The first keyframe always sits ~1s in to skip splash / black opens.

import (
	sdk "github.com/apteva/app-sdk"
)

const (
	defaultKeyframeIntervalSeconds = 30
	defaultKeyframeMaxCount        = 60
	// First keyframe sits ~1s in — same reason the canonical thumbnail
	// defaults seek to 1s, just to skip splash/black at t=0.
	firstKeyframeOffsetMs = 1000
)

// keyframesEnabled reads the keyframes_enabled install config. Default
// true: any install that doesn't explicitly disable gets keyframes.
// Operators with tight storage budgets can flip it off.
func keyframesEnabled(app *sdk.AppCtx) bool {
	if app == nil {
		return true
	}
	v := app.Config().Get("keyframes_enabled")
	if v == "" {
		return true
	}
	switch v {
	case "false", "0", "no", "off":
		return false
	}
	return true
}

// keyframePositions returns the ms-timestamps to extract for a given
// duration, honoring the install's interval + max-count config.
// Returns empty for durationMs <= 0 (image / un-probable source) or
// when keyframes are disabled.
//
// Spacing rule: ideal spacing is keyframe_interval_seconds, but if
// that would produce more than keyframe_max_count keyframes we
// stretch the interval so total stays under the cap. For an hour
// video with default 30s interval + 60-frame cap, the effective
// interval becomes 60s.
func keyframePositions(durationMs int64, app *sdk.AppCtx) []int64 {
	if durationMs <= 0 {
		return nil
	}
	interval := parseConfigIntFallback(app.Config().Get("keyframe_interval_seconds"), defaultKeyframeIntervalSeconds)
	maxCount := parseConfigIntFallback(app.Config().Get("keyframe_max_count"), defaultKeyframeMaxCount)
	if interval <= 0 {
		interval = defaultKeyframeIntervalSeconds
	}
	if maxCount <= 0 {
		maxCount = defaultKeyframeMaxCount
	}

	intervalMs := int64(interval) * 1000
	// Stretch interval if natural spacing exceeds the cap.
	natural := (durationMs - firstKeyframeOffsetMs) / intervalMs
	if natural <= 0 {
		// Source shorter than first-keyframe-offset + one interval — emit
		// a single keyframe at the configured offset (capped if past
		// duration).
		pos := int64(firstKeyframeOffsetMs)
		if pos >= durationMs {
			pos = durationMs / 2
		}
		return []int64{pos}
	}
	if natural+1 > int64(maxCount) {
		// (durationMs - offset) / max = effective interval; clamp.
		intervalMs = (durationMs - firstKeyframeOffsetMs) / int64(maxCount-1)
		if intervalMs <= 0 {
			intervalMs = 1000
		}
	}

	out := make([]int64, 0, maxCount)
	for pos := int64(firstKeyframeOffsetMs); pos < durationMs && len(out) < maxCount; pos += intervalMs {
		out = append(out, pos)
	}
	return out
}
