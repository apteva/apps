package main

import (
	"crypto/hmac"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"fmt"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// ─── Signed, expiring playback URLs ───────────────────────────────
//
// v0.1's playback_token was a static per-stream bearer string with no
// lifetime and no revocation short of deleting the stream: anyone who
// ever saw a replay link kept it forever, which made a consumer app's
// "replay expires in 7 days" policy decorative — the raw media URL
// outlived it. v0.2 gives every stream its own HMAC secret and an
// optional expiring signature over "<stream_id>:<exp>":
//
//	sig = hex(HMAC-SHA256(url_signing_secret, "<stream_id>:<exp>"))
//
// The signature stays OPTIONAL by default (require_signed_urls=0) so
// every URL v0.1 handed out keeps working. streams_set_url_policy
// flips a stream to require_signed_urls=1, after which a bare ?t= is
// no longer sufficient; streams_rotate_key(rotate_playback_token=true)
// rotates both the token and the secret, invalidating every URL
// outstanding for that stream.

// ─── Signature scopes ─────────────────────────────────────────────
//
// v0.2 signed only "<stream_id>:<exp>", so the `kind` argument
// streams_signed_url advertises scoped nothing: the signature minted
// for kind=heartbeat validated just as well on record.mp4. That
// mattered because the two have opposite natural lifetimes — a
// heartbeat URL has to outlive the whole broadcast to keep counting
// viewers, while an mp4 replay link is exactly the thing a consumer
// wants to expire quickly. A viewer could lift exp+sig+t off their
// heartbeat URL and hold the recording open for the heartbeat's
// lifetime.
//
// v0.3 binds the scope into the signed message. Scopes are classes,
// not filenames: the HLS manifest and its segments share one, because
// rewriteManifestQuery propagates the manifest's own query onto every
// segment URI and a per-file signature would 404 every segment.
const (
	scopeHLS       = "hls"
	scopeMP4       = "mp4"
	scopeHeartbeat = "heartbeat"
)

// scopeForFile maps a playback filename to its signature scope.
func scopeForFile(filename string) string {
	if filename == recordingFile {
		return scopeMP4
	}
	return scopeHLS
}

// signPlayback computes the hex signature for (streamID, exp, scope).
func signPlayback(secret string, streamID, exp int64, scope string) string {
	mac := hmac.New(sha256.New, []byte(secret))
	fmt.Fprintf(mac, "%d:%d:%s", streamID, exp, scope)
	return hex.EncodeToString(mac.Sum(nil))
}

// signatureOK verifies ?exp=&sig= against the stream's secret for one
// scope. Expiry is checked before the MAC so an expired URL never gets
// a comparison at all. Fails closed on a missing secret — a legacy row
// whose secret was never generated must not accept a signature over "".
func signatureOK(secret string, streamID int64, expRaw, sig, scope string, now time.Time) bool {
	if secret == "" || sig == "" {
		return false
	}
	exp, err := strconv.ParseInt(strings.TrimSpace(expRaw), 10, 64)
	if err != nil {
		return false
	}
	if now.Unix() > exp {
		return false
	}
	want := signPlayback(secret, streamID, exp, scope)
	got := strings.ToLower(strings.TrimSpace(sig))
	return subtle.ConstantTimeCompare([]byte(got), []byte(want)) == 1
}

// playbackAuthorized applies the visibility + signing policy to one
// media or heartbeat request, for the scope that request belongs to.
// Every failure mode returns false; the callers turn that into 404 so
// we never leak which streams exist.
func playbackAuthorized(rec *playbackRecord, q url.Values, scope string, now time.Time) bool {
	if rec == nil {
		return false
	}
	if rec.Visibility == "signed" {
		// Constant-time: this token gates every recorded byte.
		if subtle.ConstantTimeCompare([]byte(q.Get("t")), []byte(rec.PlaybackToken)) != 1 {
			return false
		}
	}
	if sig := q.Get("sig"); sig != "" {
		return signatureOK(rec.SigningSecret, rec.ID, q.Get("exp"), sig, scope, now)
	}
	// No signature presented — fine unless the stream demands one.
	return !rec.RequireSignedURLs
}

// ─── URL construction ─────────────────────────────────────────────

// urlProjectID returns the project id that generated URLs must carry,
// or "" when the install is project-scoped and the sidecar can infer
// it from its own env.
//
// A global-scoped install has no APTEVA_PROJECT_ID, and
// resolveProjectFromRequest then rejects (HTTP 400) any viewer request
// that doesn't name the project explicitly. v0.1 built every playback
// URL with only ?t=, so on a global install — which apteva.yaml
// declares support for — every single viewer request 400'd.
func urlProjectID(pid string) string {
	if strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")) != "" {
		return ""
	}
	return strings.TrimSpace(pid)
}

// mediaURL returns a fully-formed URL for one file of one stream:
//
//	<base>/streams/<id>/<file>?t=…[&project_id=…][&exp=…&sig=…]
//
// exp > 0 adds the expiring signature; exp == 0 produces the plain
// token-only URL (valid while require_signed_urls = 0).
func (a *App) mediaURL(ctx *sdk.AppCtx, s *Stream, file string, exp int64) string {
	q := url.Values{}
	q.Set("t", s.PlaybackToken)
	if v := urlProjectID(s.ProjectID); v != "" {
		q.Set("project_id", v)
	}
	if exp > 0 {
		q.Set("exp", strconv.FormatInt(exp, 10))
		q.Set("sig", signPlayback(s.URLSigningSecret, s.ID, exp, scopeForFile(file)))
	}
	return fmt.Sprintf("%s/streams/%d/%s?%s", a.publicPath(ctx), s.ID, file, q.Encode())
}

// heartbeatURL returns the viewer-heartbeat endpoint for a stream,
// carrying the same token + project_id (+ signature) the media URLs
// do — the heartbeat handler applies the same policy.
func (a *App) heartbeatURL(ctx *sdk.AppCtx, s *Stream, exp int64) string {
	q := url.Values{}
	q.Set("t", s.PlaybackToken)
	if v := urlProjectID(s.ProjectID); v != "" {
		q.Set("project_id", v)
	}
	if exp > 0 {
		q.Set("exp", strconv.FormatInt(exp, 10))
		q.Set("sig", signPlayback(s.URLSigningSecret, s.ID, exp, scopeHeartbeat))
	}
	return fmt.Sprintf("%s/heartbeat/%d?%s", a.publicPath(ctx), s.ID, q.Encode())
}

// ensureSigningSecret lazily fills in a stream's HMAC secret. Rows
// created by v0.1 were backfilled with a placeholder by migration
// 002; anything still empty (a row written by a half-upgraded binary,
// say) gets a real crypto/rand secret the first time we sign for it.
func (a *App) ensureSigningSecret(ctx *sdk.AppCtx, s *Stream) error {
	if s == nil || strings.TrimSpace(s.URLSigningSecret) != "" {
		return nil
	}
	secret := randomToken()
	if _, err := ctx.AppDB().Exec(
		`UPDATE streams SET url_signing_secret = ? WHERE id = ? AND project_id = ?`,
		secret, s.ID, s.ProjectID); err != nil {
		return err
	}
	s.URLSigningSecret = secret
	a.invalidatePlayback(s.ProjectID, s.ID)
	return nil
}
