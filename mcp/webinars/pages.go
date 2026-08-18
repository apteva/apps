package main

import (
	"html/template"
	"net/http"
	"strings"
)

// Shared presentation for the attendee-facing pages.
//
// Two things live here that public.go's handlers lean on:
//
//   - `html/template` instead of fmt.Fprintf. The old pages interpolated
//     with %s and leaned on the author remembering html.EscapeString at
//     every call site. Chat bodies and display names are still stored
//     unsanitized (by design — the renderer is what makes them safe), so
//     "the author remembered" is a thin thing to stand on. Contextual
//     autoescaping makes it structural: a display name lands escaped in
//     HTML text, in an attribute, and in a JS string literal, without
//     the template author choosing correctly each time.
//   - Designed lifecycle states. Attendees hit "not started yet",
//     "already ended", "replay expired" and "wrong link" constantly —
//     someone always clicks their join link an hour early. Those used to
//     return a raw JSON blob, a plain-text 410, and Go's default 404
//     page respectively.

// ─── The pinned hls.js ────────────────────────────────────────────

// The player is the one external asset on any of these pages.
//
// It was `hls.js@1` — a floating tag, no SRI. That is a supply-chain
// hole with an unusually good payoff for an attacker: arbitrary JS on
// our origin, in the page whose URL *is* the attendee's capability
// token. Pinned to an exact version with a Subresource Integrity hash,
// and loaded `crossorigin="anonymous"` so the browser can actually
// check it. `Referrer-Policy: no-referrer` (see websec.go) keeps the
// join token out of the request that fetches it.
const (
	hlsScriptURL       = "https://cdn.jsdelivr.net/npm/hls.js@1.7.0/dist/hls.min.js"
	hlsScriptIntegrity = "sha384-NsaFqWMOpy26cQK1F9VfwDdMFB97h7JCesDaPSI1sr79bzoezFrUOTYBhdsLJgha"
)

// ─── Shared template definitions ──────────────────────────────────

// sharedDefs are {{define}} blocks every public page composes in. Each
// block is only ever used in one context (CSS inside <style>, JS inside
// <script>), which is what html/template's contextual escaping needs.
const sharedDefs = `
{{define "base-css"}}
  :root {
    color-scheme: light dark;
    --bg: #ffffff; --fg: #1a1a1a; --muted: #5f6368; --line: #d7d7d7;
    --card: #ffffff; --accent: #1a73e8; --accent-fg: #ffffff; --focus: #1a73e8;
  }
  @media (prefers-color-scheme: dark) {
    :root {
      --bg: #101013; --fg: #ececef; --muted: #a0a0a3; --line: #2a2a2d;
      --card: #17171a; --accent: #7aa7ff; --accent-fg: #101013; --focus: #7aa7ff;
    }
  }
  * { box-sizing: border-box; }
  body {
    font-family: -apple-system, BlinkMacSystemFont, "Segoe UI", Roboto, sans-serif;
    background: var(--bg); color: var(--fg); margin: 0;
    -webkit-text-size-adjust: 100%;
  }
  a { color: var(--accent); }
  /* Every interactive element gets a visible ring. The dark live room
     had none at all, so keyboard users had no idea where they were. */
  :focus-visible {
    outline: 2px solid var(--focus); outline-offset: 2px; border-radius: 4px;
  }
  .sr-only {
    position: absolute; width: 1px; height: 1px; padding: 0; margin: -1px;
    overflow: hidden; clip: rect(0 0 0 0); white-space: nowrap; border: 0;
  }
{{end}}

{{define "localtime-js"}}
// Render every <time datetime> in the reader's own timezone. The server
// stores and emits UTC RFC3339; a webinar at "2026-06-01T15:00:00Z" is
// not a useful string to show someone in Denver, and the previous page
// printed exactly that.
function localizeTimes(root) {
  (root || document).querySelectorAll("time[datetime]").forEach(function (el) {
    var d = new Date(el.getAttribute("datetime"));
    if (isNaN(d.getTime())) return;
    var opts = el.dataset.fmt === "short"
      ? { dateStyle: "medium", timeStyle: "short" }
      : { dateStyle: "full", timeStyle: "short" };
    try { el.textContent = d.toLocaleString(undefined, opts); } catch (e) {}
    el.title = d.toString();
  });
}
localizeTimes(document);
{{end}}

{{define "countdown-js"}}
// Coarse "starts in …" counter. Deliberately not a ticking clock for
// anything more than an hour out — a 3-day countdown updating every
// second is noise.
function startCountdown(el, onElapsed) {
  if (!el) return;
  var target = new Date(el.dataset.until);
  if (isNaN(target.getTime())) return;
  var fired = false;
  function tick() {
    var ms = target.getTime() - Date.now();
    if (ms <= 0) {
      el.textContent = "starting now";
      if (!fired) { fired = true; if (onElapsed) onElapsed(); }
      return;
    }
    var s = Math.floor(ms / 1000), m = Math.floor(s / 60);
    var h = Math.floor(m / 60), d = Math.floor(h / 24);
    if (d >= 1)      el.textContent = "starts in " + d + (d === 1 ? " day" : " days");
    else if (h >= 1) el.textContent = "starts in " + h + "h " + (m % 60) + "m";
    else if (m >= 1) el.textContent = "starts in " + m + "m " + (s % 60) + "s";
    else             el.textContent = "starts in " + s + "s";
  }
  tick();
  setInterval(tick, 1000);
}
{{end}}
`

// ─── Inline icons ─────────────────────────────────────────────────
//
// Workspace convention: no emoji as UI glyphs. These are small stroke
// SVGs authored here rather than pulled from an icon font — the CSP
// denies external assets outright, and the pages have to work on a
// conference-centre connection.

type iconName string

const (
	iconCalendar iconName = "calendar"
	iconPlay     iconName = "play"
	iconClock    iconName = "clock"
	iconSearch   iconName = "search"
	iconSignal   iconName = "signal"
)

var stateIcons = map[iconName]template.HTML{
	iconCalendar: `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" aria-hidden="true"><rect x="3" y="5" width="18" height="16" rx="2"/><path d="M3 10h18M8 3v4M16 3v4"/></svg>`,
	iconPlay:     `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linejoin="round" aria-hidden="true"><circle cx="12" cy="12" r="9"/><path d="M10 8.5l6 3.5-6 3.5z"/></svg>`,
	iconClock:    `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" aria-hidden="true"><circle cx="12" cy="12" r="9"/><path d="M12 7v5l3 2"/></svg>`,
	iconSearch:   `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" aria-hidden="true"><circle cx="10.5" cy="10.5" r="6.5"/><path d="M15.5 15.5L21 21"/></svg>`,
	iconSignal:   `<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.5" stroke-linecap="round" aria-hidden="true"><path d="M5 12.5a9 9 0 0114 0M8 15.5a5 5 0 018 0"/><circle cx="12" cy="19" r="1.2" fill="currentColor" stroke="none"/></svg>`,
}

// ─── Lifecycle state page ─────────────────────────────────────────

// statePage is every non-player attendee page: waiting, ended, expired,
// not found. One template, because they differ only in copy and in
// whether they carry a time, an action or a self-refresh.
type statePage struct {
	Status   int // HTTP status to write
	Nonce    string
	Icon     iconName
	Title    string // browser tab
	Headline string
	Body     string
	Note     string // smaller line under the body

	// WhenISO renders a localized date line (and a countdown when
	// Countdown is set). WhenLabel is the pre-JS fallback text.
	WhenISO   string
	WhenLabel string
	Countdown bool

	ActionURL   string
	ActionLabel string

	// ReloadMS re-fetches the page on a timer. Used by the "hasn't
	// started" states so an attendee who arrives early lands in the live
	// room on their own, without watching for a moment to hit refresh.
	ReloadMS int
}

func (p statePage) IconSVG() template.HTML { return stateIcons[p.Icon] }

var stateTmpl = template.Must(
	template.Must(template.New("state").Parse(sharedDefs)).Parse(statePageHTML))

const statePageHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<title>{{.Title}}</title>
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<style nonce="{{.Nonce}}">
{{template "base-css" .}}
  body { display: grid; place-items: center; min-height: 100vh; padding: 1.5rem; }
  main {
    background: var(--card); border: 1px solid var(--line); border-radius: 14px;
    max-width: 33rem; width: 100%; padding: 2rem; text-align: center;
  }
  .icon { width: 2.5rem; height: 2.5rem; color: var(--muted); margin: 0 auto 1rem; }
  .icon svg { width: 100%; height: 100%; display: block; }
  h1 { font-size: 1.35rem; line-height: 1.3; margin: 0 0 0.6rem; }
  p { margin: 0 0 0.75rem; color: var(--muted); line-height: 1.5; }
  .when { color: var(--fg); font-weight: 600; }
  .countdown { display: block; color: var(--muted); font-weight: 400; font-size: 0.95rem; margin-top: 0.2rem; }
  .note { font-size: 0.875rem; }
  .action {
    display: inline-block; margin-top: 0.75rem; padding: 0.7rem 1.2rem;
    background: var(--accent); color: var(--accent-fg); border-radius: 8px;
    text-decoration: none; font-weight: 600;
  }
</style></head>
<body>
<main>
  <div class="icon">{{.IconSVG}}</div>
  <h1>{{.Headline}}</h1>
  {{if .Body}}<p>{{.Body}}</p>{{end}}
  {{if .WhenISO}}
  <p class="when">
    <time datetime="{{.WhenISO}}">{{.WhenLabel}}</time>
    {{if .Countdown}}<span class="countdown" id="countdown" data-until="{{.WhenISO}}"></span>{{end}}
  </p>
  {{end}}
  {{if .ActionURL}}<a class="action" href="{{.ActionURL}}">{{.ActionLabel}}</a>{{end}}
  {{if .Note}}<p class="note">{{.Note}}</p>{{end}}
</main>
<script nonce="{{.Nonce}}">
{{template "localtime-js" .}}
{{template "countdown-js" .}}
startCountdown(document.getElementById("countdown"), function () {
  setTimeout(function () { location.reload(); }, 2000);
});
{{if .ReloadMS}}setTimeout(function () { location.reload(); }, {{.ReloadMS}});{{end}}
</script>
</body></html>`

// renderStatePage writes one lifecycle page. The nonce is minted here so
// callers can't forget it — a page without one renders unstyled and
// scriptless under our own CSP, which is a loud enough failure that it
// would be caught, but not one worth risking.
func renderStatePage(rw http.ResponseWriter, p statePage) {
	p.Nonce = newCSPNonce()
	if p.Title == "" {
		p.Title = p.Headline
	}
	writePageHeaders(rw, p.Nonce)
	// The waiting pages self-refresh; nothing here should be cached.
	rw.Header().Set("Cache-Control", "no-store")
	if p.Status == 0 {
		p.Status = http.StatusOK
	}
	rw.WriteHeader(p.Status)
	if err := stateTmpl.Execute(rw, p); err != nil && globalCtx != nil {
		globalCtx.Logger().Warn("render state page", "headline", p.Headline, "err", err)
	}
}

// ─── Canned states ────────────────────────────────────────────────

// notFoundPage replaces Go's default 404 on the public routes. An
// attendee who lands here almost always has a link problem (truncated by
// a mail client, or from a webinar that was deleted), so the copy points
// at that rather than at HTTP.
func notFoundPage(rw http.ResponseWriter) {
	renderStatePage(rw, statePage{
		Status:   http.StatusNotFound,
		Icon:     iconSearch,
		Title:    "Link not found",
		Headline: "We couldn’t find that page",
		Body:     "This link may have expired, or it may have been cut short when it was copied.",
		Note:     "Check the most recent email you received, and use the full link from there.",
	})
}

// waitingPage — the webinar has a start time in the future.
func waitingPage(rw http.ResponseWriter, w *Webinar, startsAt string) {
	page := statePage{
		Icon:     iconCalendar,
		Title:    w.Title,
		Headline: w.Title,
		Body:     "You’re registered. This page will let you in automatically when the webinar starts.",
		// Poll while we wait: 60s is often enough to catch the host
		// starting early without hammering the sidecar from every seat.
		ReloadMS: 60000,
	}
	if startsAt != "" {
		if t, err := parseDBTime(startsAt); err == nil {
			page.WhenISO = formatRFC3339(t)
			page.WhenLabel = t.Format("Mon, Jan 2, 2006 15:04 MST")
			page.Countdown = true
		}
	}
	if page.WhenISO == "" {
		page.Body = "You’re registered. This page will let you in automatically once the host goes live."
	}
	renderStatePage(rw, page)
}

// startingPage — the webinar is live (or should be) but streaming has no
// playable snapshot yet. This is the state that used to answer with
// `503 {"error":"stream not ready"}`.
func startingPage(rw http.ResponseWriter, w *Webinar) {
	renderStatePage(rw, statePage{
		Status:   http.StatusServiceUnavailable,
		Icon:     iconSignal,
		Title:    w.Title,
		Headline: "Starting shortly",
		Body:     "The host is getting set up. This page refreshes on its own — you don’t need to do anything.",
		ReloadMS: 15000,
	})
}

// endedPage — the webinar is over. Carries the replay when one is
// published, and says so plainly when one isn’t.
func endedPage(rw http.ResponseWriter, w *Webinar, replayURL string) {
	page := statePage{
		Icon:     iconPlay,
		Title:    w.Title,
		Headline: "This webinar has ended",
		Body:     "Thanks for joining " + strings.TrimSpace(w.Title) + ".",
	}
	if replayURL != "" {
		page.ActionURL = replayURL
		page.ActionLabel = "Watch the replay"
	} else {
		page.Note = "If a replay is published, it’ll be sent to the address you registered with."
	}
	renderStatePage(rw, page)
}

// replayExpiredPage — 410, and it now means it. The old page checked
// replay_expires_at and then handed out a non-expiring media URL, so
// "expired" only ever stopped people who hadn't already loaded it.
func replayExpiredPage(rw http.ResponseWriter, w *Webinar) {
	renderStatePage(rw, statePage{
		Status:   http.StatusGone,
		Icon:     iconClock,
		Title:    "Replay expired",
		Headline: "This replay has expired",
		Body:     "The recording of " + strings.TrimSpace(w.Title) + " is no longer available to watch.",
		Note:     "If you need access, reply to the email you received and ask the host.",
	})
}

// replayUnavailablePage — published, but streaming has nothing to serve
// yet (recording still processing, or the call failed).
func replayUnavailablePage(rw http.ResponseWriter, w *Webinar) {
	renderStatePage(rw, statePage{
		Status:   http.StatusServiceUnavailable,
		Icon:     iconSignal,
		Title:    w.Title,
		Headline: "The replay isn’t ready yet",
		Body:     "The recording is still being processed. Try again in a few minutes.",
		ReloadMS: 60000,
	})
}
