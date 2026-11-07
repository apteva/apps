package main

import (
	"errors"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// youtubeReleaseAt reads the per-target release_at option: the time YouTube
// itself makes a privately uploaded video public (status.publishAt). Social's
// own schedule_at decides when the upload happens; release_at is separate.
func youtubeReleaseAt(opts map[string]any) (string, error) {
	raw := strOption(opts, "release_at")
	if raw == "" {
		return "", nil
	}
	at, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		return "", errors.New("release_at must be an RFC3339 timestamp")
	}
	return at.UTC().Format(time.RFC3339), nil
}

// youtubeReleaseState describes where a delivered YouTube video actually
// stands. A successful upload is not the same as public: the video may be
// private, unlisted, or private with a scheduled release. A fresh native
// observation wins; otherwise the state Social requested at upload/edit is
// reported, marked as such.
func youtubeReleaseState(options map[string]any, metadata any, observedAt string, now time.Time) map[string]any {
	visibility, releaseAt, source := "", "", "requested"
	if m, ok := metadata.(map[string]any); ok {
		if v, _ := m["visibility"].(string); v != "" {
			visibility, source = v, "observed"
			releaseAt, _ = m["scheduled_publish_at"].(string)
		}
	}
	if source == "requested" {
		visibility = strOption(options, "visibility")
		if at, err := youtubeReleaseAt(options); err == nil && at != "" && (visibility == "" || visibility == "private") {
			visibility, releaseAt = "private", at
		}
		if visibility == "" {
			visibility = "public"
		}
	}
	state := visibility
	if visibility == "private" && releaseAt != "" {
		state = "scheduled"
		if at, err := time.Parse(time.RFC3339Nano, releaseAt); err == nil && !at.After(now) {
			// YouTube should have released it; only a fresh observation
			// can confirm that.
			state = "release_due"
		}
	}
	out := map[string]any{"state": state, "visibility": visibility, "source": source}
	if releaseAt != "" {
		out["release_at"] = releaseAt
	}
	if source == "observed" && observedAt != "" {
		out["observed_at"] = observedAt
	}
	return out
}

// toolPostTargetNativeRefresh re-reads one delivered target from the
// platform so its native state (visibility, scheduled release) is current.
// Read-only upstream: it never publishes or edits.
func (a *App) toolPostTargetNativeRefresh(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	ctx = ctx.WithProject(projectScope(ctx, args))
	post, target, err := recoveryIDs(args)
	if err != nil {
		return nil, err
	}
	r, err := loadRecoveryTarget(ctx, post, target)
	if err != nil {
		return mcpError("target not found in this project"), nil
	}
	if strings.TrimSpace(r.PublicID) == "" {
		return mcpError("target has no native post yet"), nil
	}
	return a.toolPostNativeInspect(ctx, map[string]any{"social_account_id": r.AccountID, "native_post_id": r.PublicID})
}
