package main

import (
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Playback URL minting.
//
// Replay expiry used to be theatre. The /replay/<slug> page checked the
// replay token and replay_expires_at, then handed out a media URL built
// on streaming's per-stream `playback_token` — a static credential that
// never expires. Anyone who watched once (or who saw the URL in a
// devtools network tab) kept streaming the recording for the whole
// retention window, "expired" or not.
//
// Streaming now exposes signed URLs (`exp` + HMAC `sig`) plus a
// per-stream require_signed_urls policy. Webinars asks for a URL whose
// lifetime is bounded by replay_expires_at, and turns the policy on for
// the streams it owns so the raw token form stops being accepted.
//
// Everything here degrades rather than fails: if the streaming install
// predates those tools, the cross-app call errors and we fall back to
// the legacy URL with Signed=false, which the caller can surface.

const (
	// defaultReplayTTL is the signed-URL lifetime for a replay with no
	// replay_expires_at. Long enough to watch a 2h recording with
	// pauses, short enough that a leaked URL dies the same day.
	defaultReplayTTL = 6 * time.Hour
	// liveTTLSlack is added to the webinar's scheduled duration so an
	// over-running webinar never needs a mid-session URL refresh.
	liveTTLSlack = 2 * time.Hour

	minSignedTTL = 60 * time.Second
	maxSignedTTL = 24 * time.Hour
)

var (
	// errReplayExpired — the replay window has closed. Map to HTTP 410.
	errReplayExpired = simpleErr("replay has expired")
	// errReplayUnavailable — no recording to serve (no stream, streaming
	// says the recording isn't ready, or the call failed). Map to 503.
	errReplayUnavailable = simpleErr("replay not available")
)

// PlaybackURL is a ready-to-embed media URL plus the little bit of
// metadata a player needs to choose its path.
type PlaybackURL struct {
	URL  string `json:"url"`
	Kind string `json:"kind"` // "hls" | "mp4"
	// Signed is false when the signed-URL call failed and we fell back
	// to streaming's static token URL. Worth logging; not worth
	// blocking playback over.
	Signed bool `json:"signed"`
	// ExpiresAt is when the signed URL stops working (empty when
	// Signed is false).
	ExpiresAt string `json:"expires_at,omitempty"`
}

// LiveRoomPlayback returns the HLS URL to embed in the live room.
//
// snap is the stream snapshot the caller already fetched (pass nil to
// skip the fallback). The TTL covers the whole webinar plus slack, so
// there is no mid-webinar refresh: a viewer who joins at minute 0 can
// still be watching at the end without the URL going stale.
func (a *App) LiveRoomPlayback(ctx *sdk.AppCtx, w *Webinar, snap *StreamSnapshot) PlaybackURL {
	fallback := PlaybackURL{Kind: "hls"}
	if snap != nil {
		fallback.URL = snap.PlaybackURL
	}
	if a == nil || w == nil || w.StreamID == 0 || a.streamingCaller == nil {
		return fallback
	}
	ttl := a.liveSignedTTL(ctx, w)
	out, err := a.streamingCaller.SignedURL(SignedURLReq{
		ID:               w.StreamID,
		ExpiresInSeconds: int(ttl / time.Second),
		Kind:             "hls",
		ProjectID:        w.ProjectID,
	})
	if err != nil || out.URL == "" {
		if ctx != nil && err != nil {
			ctx.Logger().Warn("streaming.streams_signed_url (live)",
				"webinar_id", w.ID, "stream_id", w.StreamID, "err", err)
		}
		return fallback
	}
	return PlaybackURL{
		URL:       out.URL,
		Kind:      "hls",
		Signed:    true,
		ExpiresAt: formatRFC3339(nowUTC().Add(ttl)),
	}
}

// LiveRoomStreamHeartbeat returns streaming's viewer-heartbeat endpoint
// for the webinar's stream, signed.
//
// It has to be signed for the same reason the player URL does.
// EnforceSignedPlayback turns on require_signed_urls, and streaming runs
// the *same* authorization gate on /heartbeat/<id> as it does on the
// media routes — so a live room that posts a bare `?t=` heartbeat under
// that policy keeps playing video perfectly while every beat is
// rejected, and the viewer count silently reads zero.
//
// Returns "" when no signed URL could be minted (no stream, or a
// streaming install predating kind=heartbeat). Callers should fall back
// to the legacy bare-token URL in that case: those installs aren't
// enforcing the policy either, so the bare form still counts.
func (a *App) LiveRoomStreamHeartbeat(ctx *sdk.AppCtx, w *Webinar) string {
	if a == nil || w == nil || w.StreamID == 0 || a.streamingCaller == nil {
		return ""
	}
	// Same TTL as the player URL, so the two expire together and a
	// long-running webinar never needs a mid-session refresh of either.
	ttl := a.liveSignedTTL(ctx, w)
	out, err := a.streamingCaller.SignedURL(SignedURLReq{
		ID:               w.StreamID,
		ExpiresInSeconds: int(ttl / time.Second),
		Kind:             "heartbeat",
		ProjectID:        w.ProjectID,
	})
	if err != nil || out.URL == "" {
		if ctx != nil && err != nil {
			ctx.Logger().Warn("streaming.streams_signed_url (heartbeat)",
				"webinar_id", w.ID, "stream_id", w.StreamID, "err", err)
		}
		return ""
	}
	return out.URL
}

// ReplayPlayback returns the URL to embed on the replay page, with a TTL
// that can never outlive replay_expires_at.
//
// Returns errReplayExpired when the replay window has already closed —
// callers should treat that as 410 and not render a player.
func (a *App) ReplayPlayback(ctx *sdk.AppCtx, w *Webinar) (PlaybackURL, error) {
	if a == nil || w == nil || w.StreamID == 0 || a.streamingCaller == nil {
		return PlaybackURL{}, errReplayUnavailable
	}
	ttl, err := a.replaySignedTTL(ctx, w)
	if err != nil {
		return PlaybackURL{}, err
	}

	// HLS first (the page prefers it), MP4 when the recording has no
	// HLS rendition.
	for _, kind := range []string{"hls", "mp4"} {
		out, sErr := a.streamingCaller.SignedURL(SignedURLReq{
			ID:               w.StreamID,
			ExpiresInSeconds: int(ttl / time.Second),
			Kind:             kind,
			ProjectID:        w.ProjectID,
		})
		if sErr == nil && out.URL != "" {
			return PlaybackURL{
				URL:       out.URL,
				Kind:      kind,
				Signed:    true,
				ExpiresAt: formatRFC3339(nowUTC().Add(ttl)),
			}, nil
		}
		if sErr != nil && ctx != nil {
			ctx.Logger().Warn("streaming.streams_signed_url (replay)",
				"webinar_id", w.ID, "stream_id", w.StreamID, "kind", kind, "err", sErr)
		}
	}

	// Fall back to the legacy non-expiring URL so an older streaming
	// install keeps serving replays. Signed=false marks the gap.
	urls, uErr := a.streamingCaller.ReplayURL(w.StreamID)
	if uErr != nil || !urls.Available {
		return PlaybackURL{}, errReplayUnavailable
	}
	if urls.HLSURL != "" {
		return PlaybackURL{URL: urls.HLSURL, Kind: "hls"}, nil
	}
	if urls.MP4URL != "" {
		return PlaybackURL{URL: urls.MP4URL, Kind: "mp4"}, nil
	}
	return PlaybackURL{}, errReplayUnavailable
}

// EnforceSignedPlayback turns on require_signed_urls for the webinar's
// stream, retiring the static `?t=<playback_token>` form. Best-effort:
// a streaming install without the tool logs a warning and carries on.
func (a *App) EnforceSignedPlayback(ctx *sdk.AppCtx, w *Webinar) {
	if a == nil || w == nil || w.StreamID == 0 || a.streamingCaller == nil {
		return
	}
	if err := a.streamingCaller.SetURLPolicy(URLPolicyReq{
		ID:                w.StreamID,
		RequireSignedURLs: true,
		ProjectID:         w.ProjectID,
	}); err != nil && ctx != nil {
		ctx.Logger().Warn("streaming.streams_set_url_policy",
			"webinar_id", w.ID, "stream_id", w.StreamID, "err", err)
	}
}

// liveSignedTTL — webinar duration + slack, config-overridable, clamped.
func (a *App) liveSignedTTL(ctx *sdk.AppCtx, w *Webinar) time.Duration {
	if n := configInt(ctx, "live_signed_url_ttl_seconds", 0); n > 0 {
		return clampTTL(time.Duration(n) * time.Second)
	}
	dur := time.Duration(maxInt(w.DurationMinutes, 60)) * time.Minute
	return clampTTL(dur + liveTTLSlack)
}

// replaySignedTTL — never longer than what's left of replay_expires_at.
// The expiry bound is applied AFTER clamping, so "10 seconds of replay
// window left" mints a 10-second URL rather than being rounded up to
// the clamp floor.
func (a *App) replaySignedTTL(ctx *sdk.AppCtx, w *Webinar) (time.Duration, error) {
	ttl := defaultReplayTTL
	if n := configInt(ctx, "replay_signed_url_ttl_seconds", 0); n > 0 {
		ttl = time.Duration(n) * time.Second
	}
	ttl = clampTTL(ttl)
	if w.ReplayExpiresAt != "" {
		if exp, err := parseDBTime(w.ReplayExpiresAt); err == nil {
			remaining := exp.Sub(nowUTC())
			if remaining <= 0 {
				return 0, errReplayExpired
			}
			if remaining < ttl {
				ttl = remaining
			}
		}
	}
	return ttl, nil
}

func clampTTL(d time.Duration) time.Duration {
	if d < minSignedTTL {
		return minSignedTTL
	}
	if d > maxSignedTTL {
		return maxSignedTTL
	}
	return d
}
