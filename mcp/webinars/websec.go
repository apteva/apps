package main

import (
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

// Browser-facing security primitives for the three public pages.
//
// The attendee pages are the only unauthenticated HTML this app serves,
// and two of them carry a capability token in their URL
// (/live/<join_token>, /replay/<slug>?t=…). That shapes everything here:
//
//   - The page URL must not leak. `Referrer-Policy: no-referrer` stops
//     the browser attaching /live/<join_token> as `Referer` on the
//     cross-origin hls.js fetch — a token that authorizes chat-as-that-
//     registrant, attendance and offer clicks was previously handed to a
//     CDN on every page load.
//   - Script execution must be pinned. A floating `hls.js@1` tag with no
//     SRI meant a jsdelivr (or tag) compromise ran arbitrary JS on our
//     origin in every attendee's browser. The pin + integrity hash live
//     in pages.go; the CSP below is the second lock.
//   - The registration POST is the one public write with no secret in
//     its path, so it needs CSRF protection of its own.

// cryptoRandRead fills b with cryptographic randomness. A failing CSPRNG
// is not something a web handler can degrade around — every caller here
// is minting a security token — so it panics, matching randomToken.
func cryptoRandRead(b []byte) {
	if _, err := rand.Read(b); err != nil {
		panic("rand.Read: " + err.Error())
	}
}

// ─── Constant-time comparison ─────────────────────────────────────

// constantTimeEqual compares two secrets without leaking their contents
// through timing. Length differences are visible either way (that's
// inherent to comparing strings of different sizes), which is fine for
// fixed-length tokens.
func constantTimeEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

// ─── Content-Security-Policy ──────────────────────────────────────

// newCSPNonce mints a per-response nonce. Every inline <style> and
// <script> we emit carries it, and so does the one external script
// (hls.js) — which is why script-src needs no host allowlist at all: a
// nonce on a <script src> element authorizes that specific element,
// nothing else from that host.
//
// URL-safe base64 rather than standard: CSP's base64-value grammar
// accepts both, but html/template escapes '+' and '=' inside attribute
// values (to &#43; / &#61;), so a standard-alphabet nonce reaches the
// browser entity-encoded. Browsers decode it correctly, but a nonce you
// cannot grep for in the served HTML is a nonce nobody can verify.
func newCSPNonce() string {
	var b [16]byte
	cryptoRandRead(b[:])
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// pageCSP builds the policy shared by every public page.
//
// It starts from `default-src 'none'` and opens exactly what the pages
// use, which is deliberately little: no images, no fonts, no frames, no
// object/embed — those all stay denied by the default. The two
// non-obvious entries:
//
//	media-src blob:   hls.js plays through MSE, and MSE attaches to the
//	                  <video> element as a blob: URL. Without it the
//	                  player is a black rectangle.
//	worker-src blob:  hls.js runs its transmuxer in a worker built from
//	                  an inline blob. hls.js falls back to main-thread
//	                  demuxing if the worker is blocked, so this is a
//	                  performance guard rather than a correctness one —
//	                  but a CSP that quietly degrades playback is the
//	                  kind that gets ripped out later.
//
// extraOrigins carries the streaming app's origin when the platform's
// public URL is absolute. In every deployment we've seen, streaming and
// webinars are the same scheme+host+port (both are `/api/apps/<name>`
// under one reverse proxy) so 'self' already covers it — the explicit
// entry is there so a split-host deploy doesn't silently break the
// player.
func pageCSP(nonce string, extraOrigins ...string) string {
	media := []string{"'self'", "blob:"}
	connect := []string{"'self'"}
	for _, o := range dedupeOrigins(extraOrigins) {
		media = append(media, o)
		connect = append(connect, o)
	}
	return strings.Join([]string{
		"default-src 'none'",
		"script-src 'nonce-" + nonce + "'",
		"style-src 'nonce-" + nonce + "'",
		"media-src " + strings.Join(media, " "),
		"connect-src " + strings.Join(connect, " "),
		"worker-src blob:",
		"form-action 'self'",
		"base-uri 'none'",
		"frame-ancestors 'none'",
	}, "; ")
}

// writePageHeaders stamps the headers every public HTML response shares.
// Call it before writing the body.
func writePageHeaders(w http.ResponseWriter, nonce string, extraOrigins ...string) {
	h := w.Header()
	h.Set("Content-Type", "text/html; charset=utf-8")
	h.Set("Content-Security-Policy", pageCSP(nonce, extraOrigins...))
	h.Set("Referrer-Policy", "no-referrer")
	h.Set("X-Content-Type-Options", "nosniff")
	// Redundant with frame-ancestors for modern browsers, free for old ones.
	h.Set("X-Frame-Options", "DENY")
}

// originOf returns "scheme://host" for an absolute URL and "" for a
// relative one (which 'self' already covers).
func originOf(raw string) string {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return ""
	}
	return u.Scheme + "://" + u.Host
}

func dedupeOrigins(in []string) []string {
	seen := map[string]bool{}
	out := []string{}
	for _, o := range in {
		o = strings.TrimSpace(o)
		if o == "" || seen[o] {
			continue
		}
		seen[o] = true
		out = append(out, o)
	}
	return out
}

// ─── CSRF for the registration POST ───────────────────────────────
//
// POST /r/<slug> is the only public write not already gated by a secret
// in the path — chat, heartbeat, poll and offer writes all live under
// /live/<join_token>, which an attacker's page cannot know. Registration
// was fully forgeable: any page could POST a victim's browser into
// signing somebody up (and, before the tool-layer rate limit, into
// driving the reminder fan-out).
//
// The scheme is a signed double-submit cookie, which needs no session
// store:
//
//	cookie  webinars_csrf = <nonce>                (HttpOnly, SameSite=Lax)
//	form    csrf_token    = <nonce>.<expiry>.<HMAC(secret, nonce.expiry)>
//
// A cross-site attacker can read neither (same-origin policy on our HTML,
// HttpOnly on the cookie). Signing the pair means even an attacker who
// can *write* our cookie — the classic double-submit weakness, reachable
// from a sibling subdomain — still cannot produce a matching form value.
// SameSite=Lax is a third, independent layer: it stops the cookie riding
// along on a cross-site POST at all.

const (
	csrfCookieName = "webinars_csrf"
	csrfFormField  = "csrf_token"
	// csrfTTL bounds how long a rendered form stays submittable. Long
	// enough that someone can fill in a form, take a phone call and come
	// back; short enough that a token scraped from a shared screenshot
	// is dead.
	csrfTTL = 2 * time.Hour
)

// csrfSecret is a process-lifetime HMAC key. Deliberately not persisted:
// the sidecar has no keystore, and the failure mode of a restart is that
// open registration forms need one resubmit — which the handler turns
// into a friendly inline "please try again" rather than an error page.
var (
	csrfSecretOnce sync.Once
	csrfSecretVal  []byte
)

func csrfSecret() []byte {
	csrfSecretOnce.Do(func() {
		buf := make([]byte, 32)
		cryptoRandRead(buf)
		csrfSecretVal = buf
	})
	return csrfSecretVal
}

func csrfSign(payload string) string {
	mac := hmac.New(sha256.New, csrfSecret())
	mac.Write([]byte(payload))
	return base64.RawURLEncoding.EncodeToString(mac.Sum(nil))
}

// issueCSRFToken sets the cookie half and returns the form half. Call it
// on every GET that renders the registration form.
func issueCSRFToken(w http.ResponseWriter, r *http.Request) string {
	nonce := randomToken()
	payload := nonce + "." + strconv.FormatInt(time.Now().Add(csrfTTL).Unix(), 10)
	http.SetCookie(w, &http.Cookie{
		Name:  csrfCookieName,
		Value: nonce,
		// Path "/" rather than the app's mount prefix: the sidecar sees
		// paths with `/api/apps/webinars` already stripped, so it cannot
		// name its own browser-visible prefix (which differs between the
		// gateway deploy and Dockerfile.combined). The value is an
		// unprivileged random nonce, so the wider path costs nothing.
		Path:     "/",
		HttpOnly: true,
		Secure:   requestIsTLS(r),
		SameSite: http.SameSiteLaxMode,
		MaxAge:   int(csrfTTL / time.Second),
	})
	return payload + "." + csrfSign(payload)
}

// verifyCSRF checks the form value against the cookie and the signature.
// Returns false for anything malformed, expired, unsigned or mismatched.
func verifyCSRF(r *http.Request) bool {
	parts := strings.Split(r.FormValue(csrfFormField), ".")
	if len(parts) != 3 {
		return false
	}
	nonce, expStr, sig := parts[0], parts[1], parts[2]
	payload := nonce + "." + expStr
	if !constantTimeEqual(sig, csrfSign(payload)) {
		return false
	}
	exp, err := strconv.ParseInt(expStr, 10, 64)
	if err != nil || time.Now().Unix() > exp {
		return false
	}
	cookie, err := r.Cookie(csrfCookieName)
	if err != nil || cookie.Value == "" {
		return false
	}
	return constantTimeEqual(cookie.Value, nonce)
}

// requestIsTLS reports whether the browser reached us over HTTPS. The
// sidecar is always behind apteva-server's reverse proxy, so the
// forwarded header is the signal that actually fires in production; the
// direct r.TLS check covers a standalone run.
func requestIsTLS(r *http.Request) bool {
	if r.TLS != nil {
		return true
	}
	proto := r.Header.Get("X-Forwarded-Proto")
	if i := strings.IndexByte(proto, ','); i >= 0 {
		proto = proto[:i]
	}
	return strings.EqualFold(strings.TrimSpace(proto), "https")
}
