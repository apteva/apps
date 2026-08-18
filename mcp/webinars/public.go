package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html/template"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// The attendee-facing surface: registration form, live room, replay.
//
// There is no dashboard panel for this app — these three pages ARE the
// product's UI, and every one of them is unauthenticated HTML rendered
// straight out of Go. Two consequences run through the whole file:
//
//   - Rendering goes through html/template, not fmt.Fprintf. Chat bodies
//     and display names are stored unsanitized on purpose; the renderer
//     is the only thing between this app and stored XSS, so it is
//     structural (contextual autoescaping) rather than a discipline the
//     next editor has to remember. The client-side code never touches
//     innerHTML — every user string goes in via textContent.
//   - Writes go through the ownership-checked helpers in engagement.go /
//     attendance.go rather than raw INSERTs. poll_id and offer_id arrive
//     from the browser as guessable primary keys.

// ─── Registration (public) ────────────────────────────────────────
//
// GET  /r/<slug>           → HTML form
// POST /r/<slug>           → submit form, 302 to /live/<token>

func (a *App) handleRegistrationPage(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimPrefix(r.URL.Path, "/r/")
	slug = strings.Trim(slug, "/")
	if slug == "" {
		notFoundPage(w)
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx := globalCtx
	app := globalApp
	if ctx == nil || app == nil {
		httpErr(w, http.StatusServiceUnavailable, "sidecar not mounted")
		return
	}
	webinar, err := app.dbGetBySlug(ctx, pid, slug)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if webinar == nil {
		notFoundPage(w)
		return
	}

	switch r.Method {
	case http.MethodGet:
		slots, err := app.dbListSlots(ctx, pid, webinar.ID, "", "", true)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		app.renderRegistrationForm(w, r, webinar, slots, "", http.StatusOK)
	case http.MethodPost:
		app.handleRegistrationSubmit(w, r, ctx, pid, webinar)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "GET or POST")
	}
}

// handleRegistrationSubmit is the only public write with no secret in
// its path, so it carries CSRF verification of its own; everything under
// /live/ is already gated by the join_token.
//
// Every rejection re-renders the form with an inline message instead of
// replacing the page with an error blob — this is the top of the funnel,
// and a bare `{"error":"…"}` at this point loses the registration.
func (a *App) handleRegistrationSubmit(w http.ResponseWriter, r *http.Request, ctx *sdk.AppCtx, pid string, webinar *Webinar) {
	_ = r.ParseForm()
	slots, _ := a.dbListSlots(ctx, pid, webinar.ID, "", "", true)

	if !verifyCSRF(r) {
		// Almost always a stale form (tab left open past the token TTL,
		// or a sidecar restart) rather than an attack, so the copy tells
		// the visitor what to do rather than accusing them of anything.
		a.renderRegistrationForm(w, r, webinar, slots,
			"That form had been open a while — please check your details and submit again.",
			http.StatusForbidden)
		return
	}

	name := strings.TrimSpace(r.FormValue("display_name"))
	slotID, _ := strconv.ParseInt(strings.TrimSpace(r.FormValue("slot_id")), 10, 64)

	// Normalize before the tool call purely so the failure is a friendly
	// inline message; toolRegister re-runs the same validation, which is
	// what actually protects the MCP path.
	email, phone, err := NormalizeRegistrationContact(
		r.FormValue("email"), r.FormValue("phone"))
	if err != nil {
		a.renderRegistrationForm(w, r, webinar, slots, err.Error(), http.StatusBadRequest)
		return
	}

	out, err := a.toolRegister(ctx, map[string]any{
		"_project_id":  pid,
		"webinar_id":   webinar.ID,
		"slot_id":      slotID,
		"email":        email,
		"phone":        phone,
		"display_name": name,
		"source":       "form",
	})
	if err != nil {
		// The per-webinar registration budget is enforced inside
		// toolRegister (so the MCP path is covered too); the HTTP layer
		// only has to say so honestly. 429 rather than 400 is what makes
		// this legible to a proxy, to monitoring, and to a well-behaved
		// client that would otherwise retry immediately.
		status := http.StatusBadRequest
		if errors.Is(err, errRegistrationRateLimited) {
			status = http.StatusTooManyRequests
			w.Header().Set("Retry-After", "60")
		}
		ctx.Logger().Warn("registration rejected",
			"webinar_id", webinar.ID, "status", status, "err", err)
		a.renderRegistrationForm(w, r, webinar, slots, registrationErrorMessage(err), status)
		return
	}

	// Checked assertion. This used to be a bare `.(*Registrant)` followed
	// by a dereference: when the duplicate-registration bug had
	// dbGetRegistrant return (nil, nil), the assertion panicked the
	// handler and the visitor got a dropped connection. The bug is fixed;
	// the unchecked assertion should not have been the thing standing
	// between it and a 500 either way.
	reg, ok := registrantFromToolOutput(out)
	if !ok {
		ctx.Logger().Error("register returned no registrant",
			"webinar_id", webinar.ID, "output_type", typeName(out))
		a.renderRegistrationForm(w, r, webinar, slots,
			"We couldn’t complete your registration. Please try again.",
			http.StatusInternalServerError)
		return
	}

	// Redirect to the live room URL; tack on project_id so the
	// scope=global path keeps working.
	http.Redirect(w, r, withQuery(reg.JoinURL, "project_id", pid), http.StatusFound)
}

// registrantFromToolOutput unwraps toolRegister's return value without
// asserting anything it hasn't checked.
func registrantFromToolOutput(out any) (*Registrant, bool) {
	m, ok := out.(map[string]any)
	if !ok {
		return nil, false
	}
	reg, ok := m["registrant"].(*Registrant)
	if !ok || reg == nil {
		return nil, false
	}
	return reg, true
}

func typeName(v any) string { return fmt.Sprintf("%T", v) }

// registrationErrorMessage renders a tool error for a public page.
//
// Most of what toolRegister returns is genuinely useful to the visitor
// ("this slot is full", "webinar is cancelled") so it is shown as-is,
// escaped by the template. The length cap is the guard against a driver
// error dumping SQL into the page.
func registrationErrorMessage(err error) string {
	if err == nil {
		return ""
	}
	msg := strings.TrimSpace(err.Error())
	if msg == "" {
		return "We couldn’t complete your registration. Please try again."
	}
	return truncateRunes(msg, 200)
}

// ─── Registration form ────────────────────────────────────────────

type slotChoice struct {
	ID      int64
	ISO     string // RFC3339, localized client-side
	Label   string // pre-JS fallback
	Meta    string
	Checked bool
}

type registrationView struct {
	Nonce       string
	Title       string
	HostName    string
	Description string
	WhenISO     string
	WhenLabel   string
	CSRFToken   string
	Message     string
	Slots       []slotChoice
	SingleSlot  bool
	Name        string
	Email       string
	Phone       string
}

var registrationTmpl = template.Must(
	template.Must(template.New("registration").Parse(sharedDefs)).Parse(registrationHTML))

// renderRegistrationForm draws the form, optionally with an inline
// message, and always with a fresh CSRF token (the old one may have been
// consumed or expired).
func (a *App) renderRegistrationForm(w http.ResponseWriter, r *http.Request, webinar *Webinar, slots []*WebinarSlot, message string, status int) {
	view := registrationView{
		Nonce:       newCSPNonce(),
		Title:       webinar.Title,
		HostName:    webinar.HostName,
		Description: webinar.Description,
		Message:     message,
		Slots:       slotChoices(slots),
		SingleSlot:  len(slots) == 1,
		// Re-fill what the visitor typed so a rejected submit is not a
		// blank form.
		Name:  strings.TrimSpace(r.FormValue("display_name")),
		Email: strings.TrimSpace(r.FormValue("email")),
		Phone: strings.TrimSpace(r.FormValue("phone")),
	}
	if t, err := parseDBTime(webinar.ScheduledAt); err == nil {
		view.WhenISO = formatRFC3339(t)
		view.WhenLabel = t.Format("Mon, Jan 2, 2006 15:04 MST")
	}

	// SetCookie must happen before WriteHeader.
	view.CSRFToken = issueCSRFToken(w, r)
	writePageHeaders(w, view.Nonce)
	w.Header().Set("Cache-Control", "no-store")
	if status == 0 {
		status = http.StatusOK
	}
	w.WriteHeader(status)
	if err := registrationTmpl.Execute(w, view); err != nil && globalCtx != nil {
		globalCtx.Logger().Warn("render registration form", "webinar_id", webinar.ID, "err", err)
	}
}

func slotChoices(slots []*WebinarSlot) []slotChoice {
	out := make([]slotChoice, 0, len(slots))
	for i, slot := range slots {
		if slot == nil {
			continue
		}
		c := slotChoice{
			ID:      slot.ID,
			Label:   slot.StartsAt,
			Meta:    formatSlotMeta(slot),
			Checked: i == 0,
		}
		if t, err := parseDBTime(slot.StartsAt); err == nil {
			c.ISO = formatRFC3339(t)
			c.Label = t.Format("Mon, Jan 2, 2006 15:04 MST")
		}
		out = append(out, c)
	}
	return out
}

// formatSlotMeta — the secondary line under a slot's start time. The end
// time is rendered client-side alongside it, so only the capacity
// fragment is built here.
func formatSlotMeta(slot *WebinarSlot) string {
	if slot == nil || slot.Capacity <= 0 {
		return ""
	}
	return strconv.Itoa(slot.Registered) + " of " + strconv.Itoa(slot.Capacity) + " seats taken"
}

const registrationHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<title>Register: {{.Title}}</title>
<meta name="viewport" content="width=device-width, initial-scale=1">
<meta name="robots" content="noindex">
<style nonce="{{.Nonce}}">
{{template "base-css" .}}
  [hidden] { display: none !important; }
  body { max-width: 34rem; margin: 0 auto; padding: 3rem 1rem 4rem; }
  h1 { margin: 0 0 0.35rem; font-size: 1.6rem; line-height: 1.25; }
  .meta { color: var(--muted); margin-bottom: 1.25rem; }
  .desc { white-space: pre-wrap; margin-bottom: 1.5rem; line-height: 1.55; }
  form { display: grid; gap: 1rem; }
  .field { display: grid; gap: 0.35rem; }
  label { font-size: 0.875rem; font-weight: 600; }
  .hint { font-size: 0.8125rem; color: var(--muted); font-weight: 400; }
  input[type=text], input[type=email], input[type=tel] {
    font: inherit; padding: 0.7rem; border: 1px solid var(--line);
    border-radius: 8px; background: var(--card); color: var(--fg); width: 100%;
  }
  fieldset { border: 0; padding: 0; margin: 0; display: grid; gap: 0.5rem; }
  legend { font-size: 0.875rem; font-weight: 600; padding: 0; margin-bottom: 0.35rem; }
  .slot {
    display: flex; gap: 0.7rem; align-items: flex-start; padding: 0.8rem;
    border: 1px solid var(--line); border-radius: 10px; cursor: pointer;
    background: var(--card);
  }
  .slot:hover { border-color: var(--accent); }
  .slot:has(input:checked) { border-color: var(--accent); box-shadow: inset 0 0 0 1px var(--accent); }
  .slot strong { display: block; font-weight: 600; }
  .slot span { color: var(--muted); font-size: 0.875rem; }
  button[type=submit] {
    font: inherit; font-weight: 600; padding: 0.8rem; background: var(--accent);
    color: var(--accent-fg); border: 0; border-radius: 8px; cursor: pointer;
  }
  button[type=submit]:hover { filter: brightness(1.08); }
  .message {
    border: 1px solid #d93025; border-left-width: 4px; border-radius: 8px;
    padding: 0.7rem 0.85rem; margin-bottom: 1.25rem; font-size: 0.9375rem;
  }
</style></head>
<body>
  <h1>{{.Title}}</h1>
  <div class="meta">
    {{if .WhenISO}}<time datetime="{{.WhenISO}}">{{.WhenLabel}}</time>{{end}}
    {{if and .WhenISO .HostName}} &middot; {{end}}
    {{if .HostName}}{{.HostName}}{{end}}
  </div>
  {{if .Message}}<div class="message" role="alert">{{.Message}}</div>{{end}}
  {{if .Description}}<div class="desc">{{.Description}}</div>{{end}}
  <form method="POST" novalidate>
    <input type="hidden" name="csrf_token" value="{{.CSRFToken}}">
    {{if .Slots}}
      {{if .SingleSlot}}
        {{range .Slots}}
        <input type="hidden" name="slot_id" value="{{.ID}}">
        <div class="slot">
          <div>
            <strong>{{if .ISO}}<time datetime="{{.ISO}}">{{.Label}}</time>{{else}}{{.Label}}{{end}}</strong>
            {{if .Meta}}<span>{{.Meta}}</span>{{end}}
          </div>
        </div>
        {{end}}
      {{else}}
        <fieldset>
          <legend>Pick a time</legend>
          {{range .Slots}}
          <label class="slot">
            <input type="radio" name="slot_id" value="{{.ID}}"{{if .Checked}} checked{{end}} required>
            <div>
              <strong>{{if .ISO}}<time datetime="{{.ISO}}">{{.Label}}</time>{{else}}{{.Label}}{{end}}</strong>
              {{if .Meta}}<span>{{.Meta}}</span>{{end}}
            </div>
          </label>
          {{end}}
        </fieldset>
      {{end}}
    {{end}}
    <div class="field">
      <label for="display_name">Your name</label>
      <input id="display_name" type="text" name="display_name" value="{{.Name}}" autocomplete="name" required>
    </div>
    <div class="field">
      <label for="email">Email</label>
      <input id="email" type="email" name="email" value="{{.Email}}" autocomplete="email" inputmode="email" required>
    </div>
    <div class="field">
      <label for="phone">Phone <span class="hint">optional, for SMS reminders</span></label>
      <input id="phone" type="tel" name="phone" value="{{.Phone}}" autocomplete="tel" inputmode="tel"
             placeholder="+15551234567" aria-describedby="phone-hint">
      <span class="hint" id="phone-hint">Include your country code, e.g. +1 555 123 4567.</span>
    </div>
    <button type="submit">Save my seat</button>
  </form>
<script nonce="{{.Nonce}}">
{{template "localtime-js" .}}
</script>
</body></html>`

// ─── Live route dispatcher ────────────────────────────────────────
//
// One handler so we can route /live/<token>, /live/<token>/heartbeat,
// /live/<token>/chat, /live/<token>/poll-response, /live/<token>/offer-click,
// /live/<token>/events all in one place.

func (a *App) handleLiveRoute(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/live/")
	rest = strings.Trim(rest, "/")
	if rest == "" {
		notFoundPage(w)
		return
	}
	parts := strings.SplitN(rest, "/", 2)
	token := parts[0]
	sub := ""
	if len(parts) == 2 {
		sub = parts[1]
	}

	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	ctx := globalCtx
	app := globalApp
	if ctx == nil || app == nil {
		httpErr(w, http.StatusServiceUnavailable, "sidecar not mounted")
		return
	}
	reg, err := app.dbGetRegistrantByToken(ctx, pid, token)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if reg == nil {
		a.liveRouteNotFound(w, sub)
		return
	}
	webinar, err := app.dbGet(ctx, pid, reg.WebinarID)
	if err != nil || webinar == nil {
		a.liveRouteNotFound(w, sub)
		return
	}

	switch sub {
	case "":
		a.handleLiveRoom(w, r, ctx, webinar, reg)
	case "heartbeat":
		a.handleLiveHeartbeat(w, r, ctx, webinar, reg)
	case "chat":
		a.handleLiveChat(w, r, ctx, webinar, reg)
	case "poll-response":
		a.handleLivePollResponse(w, r, ctx, webinar, reg)
	case "offer-click":
		a.handleLiveOfferClick(w, r, ctx, webinar, reg)
	case "events":
		a.handleLiveEvents(w, r, ctx, webinar, reg)
	default:
		a.liveRouteNotFound(w, sub)
	}
}

// liveRouteNotFound answers in the shape the caller expects: a designed
// page for the room itself, JSON for the XHR sub-routes.
func (a *App) liveRouteNotFound(w http.ResponseWriter, sub string) {
	if sub == "" {
		notFoundPage(w)
		return
	}
	httpErr(w, http.StatusNotFound, "not found")
}

// ─── Live room ────────────────────────────────────────────────────

type liveRoomView struct {
	Nonce         string
	Title         string
	HostName      string
	DisplayName   string
	JoinToken     string
	WebinarsBase  string
	StreamingBase string
	StreamID      int64
	PlaybackURL   string
	PlaybackKind  string
	Viewers       int
	HeartbeatMS   int
	HLSSrc        string
	HLSIntegrity  string

	// HeartbeatURL is streaming's signed viewer-heartbeat endpoint,
	// minted for us by streams_signed_url (kind=heartbeat). Empty when
	// streaming can't sign one, in which case the beat is assembled from
	// the HB* parts below.
	HeartbeatURL string

	// Heartbeat auth, fallback path. See streamAuthParams — with
	// require_signed_urls on (which this app now sets for its own
	// streams) a bare token is rejected, so the signature travels with
	// the beat.
	HBToken string
	HBExp   string
	HBSig   string
}

var liveRoomTmpl = template.Must(
	template.Must(template.New("liveroom").Parse(sharedDefs)).Parse(liveRoomHTML))

// handleLiveRoom — serve the HTML + JS that runs the player and polls
// the events endpoint, or the matching lifecycle page when there is
// nothing to play.
func (a *App) handleLiveRoom(rw http.ResponseWriter, r *http.Request, ctx *sdk.AppCtx, webinar *Webinar, reg *Registrant) {
	// Terminal states first — an ended webinar should send people to the
	// replay, not to a player pointed at a stopped stream.
	if webinar.Status == "ended" || webinar.Status == "cancelled" {
		endedPage(rw, webinar, a.replayLinkFor(ctx, webinar, reg, r))
		return
	}

	var snap *StreamSnapshot
	if webinar.StreamID != 0 {
		if s, err := globalApp.streamingCaller.GetStream(webinar.StreamID); err == nil && s.ID != 0 {
			snap = &s
		}
	}
	if !liveRoomReady(webinar, snap) {
		// A registrant who clicks their link an hour early gets the
		// scheduled time and a countdown, not `503 {"error":…}`.
		if startsAt := a.startTimeFor(ctx, webinar, reg); isFutureTime(startsAt) {
			waitingPage(rw, webinar, startsAt)
			return
		}
		startingPage(rw, webinar)
		return
	}

	// Signed, TTL-bounded playback URL. snap.PlaybackURL is signed too
	// once require_signed_urls is on, but with streaming's fixed 1h
	// replay TTL — a webinar longer than an hour would lose video
	// mid-session. LiveRoomPlayback sizes the TTL to the webinar.
	playback := globalApp.LiveRoomPlayback(ctx, webinar, snap)
	if !playback.Signed {
		// Not fatal: an older streaming install without
		// streams_signed_url still serves the legacy static-token URL.
		// Worth a line in the log, because under a signed-URL policy it
		// is also what a black player looks like.
		ctx.Logger().Warn("live room fell back to an unsigned playback URL",
			"webinar_id", webinar.ID, "stream_id", webinar.StreamID)
	}

	displayName := reg.DisplayName
	if displayName == "" {
		displayName = "Guest"
	}
	publicBase := globalApp.publicAppPath(ctx)
	streamingBase := strings.Replace(publicBase, "/api/apps/webinars", "/api/apps/streaming", 1)
	// Ask streaming for a purpose-built signed heartbeat URL. Only when
	// it can't mint one (an install predating kind=heartbeat) do we fall
	// back to reassembling the beat from the playback URL's credentials.
	heartbeatURL := globalApp.LiveRoomStreamHeartbeat(ctx, webinar)
	hbToken, hbExp, hbSig := streamAuthParams(playback.URL, snap.PlaybackToken)

	view := liveRoomView{
		Nonce:         newCSPNonce(),
		Title:         webinar.Title,
		HostName:      webinar.HostName,
		DisplayName:   displayName,
		JoinToken:     reg.JoinToken,
		WebinarsBase:  publicBase,
		StreamingBase: streamingBase,
		StreamID:      snap.ID,
		PlaybackURL:   playback.URL,
		PlaybackKind:  playback.Kind,
		Viewers:       snap.CurrentViewers,
		HeartbeatMS:   globalApp.heartbeatIntervalSeconds(ctx) * 1000,
		HLSSrc:        hlsScriptURL,
		HLSIntegrity:  hlsScriptIntegrity,
		HeartbeatURL:  heartbeatURL,
		HBToken:       hbToken,
		HBExp:         hbExp,
		HBSig:         hbSig,
	}

	writePageHeaders(rw, view.Nonce,
		originOf(playback.URL), originOf(publicBase), originOf(streamingBase))
	rw.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	if err := liveRoomTmpl.Execute(rw, view); err != nil {
		ctx.Logger().Warn("render live room", "webinar_id", webinar.ID, "err", err)
	}
}

// liveRoomReady reports whether there is actually something to play. A
// stream row that exists but has never ingested is "starting shortly",
// not a player.
func liveRoomReady(w *Webinar, snap *StreamSnapshot) bool {
	if snap == nil || snap.ID == 0 || w == nil {
		return false
	}
	// Streaming's snapshot is the source of truth: a stream row exists
	// from the moment the webinar is created, and its playback URL 404s
	// until something actually ingests. Rendering a player against an
	// idle stream is a black rectangle with no explanation, which is
	// exactly what an attendee arriving before the host sees.
	if snap.Status == "live" {
		return true
	}
	// Only when streaming reports no status at all (an older install)
	// do we fall back to the webinar's own lifecycle, which tracks
	// streaming's stream.started event.
	return snap.Status == "" && w.Status == "live"
}

// startTimeFor returns the start this registrant actually signed up for:
// their slot when they picked one, the webinar's scheduled_at otherwise.
func (a *App) startTimeFor(ctx *sdk.AppCtx, w *Webinar, reg *Registrant) string {
	if reg != nil && reg.SlotID != 0 {
		if slot, err := a.dbGetSlot(ctx, w.ProjectID, reg.SlotID); err == nil && slot != nil && slot.StartsAt != "" {
			return slot.StartsAt
		}
	}
	return w.ScheduledAt
}

func isFutureTime(s string) bool {
	if strings.TrimSpace(s) == "" {
		return false
	}
	t, err := parseDBTime(s)
	return err == nil && t.After(nowUTC())
}

// replayLinkFor builds this registrant's replay link, or "" when there
// is nothing published (or the window has closed).
//
// The `r=<join_token>` parameter is what makes replay attendance
// attributable: /replay/<slug> is a shared link with no identity in it,
// so without this the replay page can only count anonymous views. See
// handleReplayPage.
func (a *App) replayLinkFor(ctx *sdk.AppCtx, w *Webinar, reg *Registrant, req *http.Request) string {
	if w == nil || !w.RecordingPublished {
		return ""
	}
	if w.ReplayExpiresAt != "" {
		if exp, err := parseDBTime(w.ReplayExpiresAt); err == nil && !nowUTC().Before(exp) {
			return ""
		}
	}
	prefix := strings.TrimSuffix(suppressNonEmptyOr(ctx.Config().Get("replay_url_prefix"), "/replay"), "/")
	link := a.publicAppPath(ctx) + prefix + "/" + w.Slug
	if w.ReplayToken != "" {
		link = withQuery(link, "t", w.ReplayToken)
	}
	if reg != nil && reg.JoinToken != "" {
		link = withQuery(link, "r", reg.JoinToken)
	}
	if req != nil {
		if pid := req.URL.Query().Get("project_id"); pid != "" {
			link = withQuery(link, "project_id", pid)
		}
	}
	return link
}

// withQuery appends one query parameter, picking ? or & correctly.
func withQuery(base, key, value string) string {
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return base + sep + url.QueryEscape(key) + "=" + url.QueryEscape(value)
}

// streamAuthParams pulls the query parameters streaming's public
// endpoints authenticate with out of a playback URL.
//
// Why this exists: streaming gates BOTH media playback and the viewer
// heartbeat through one `playbackAuthorized` check, and under
// require_signed_urls — which this app now turns on for its own streams
// — a bare `?t=<playback_token>` is answered with 404. The heartbeat
// would fail silently and every viewer count would read zero.
//
// The signature covers "<stream_id>:<exp>" only, with no path or method
// in the MAC, and streaming documents the heartbeat as carrying "the
// same token + project_id (+ signature) the media URLs do" — so lifting
// exp/sig off the signed playback URL is the contract, not a trick.
// (A cleaner shape would be for streaming to hand back heartbeat_url or
// exp/sig as fields; SignedURLResp only decodes `url` today. Noted for
// streaming_client.go, which this change does not own.)
func streamAuthParams(playbackURL, fallbackToken string) (token, exp, sig string) {
	token = fallbackToken
	u, err := url.Parse(playbackURL)
	if err != nil {
		return token, "", ""
	}
	q := u.Query()
	if t := q.Get("t"); t != "" {
		token = t
	}
	return token, q.Get("exp"), q.Get("sig")
}

const liveRoomHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<title>{{.Title}}</title>
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
<meta name="robots" content="noindex">
<script src="{{.HLSSrc}}" integrity="{{.HLSIntegrity}}" crossorigin="anonymous" nonce="{{.Nonce}}" defer></script>
<style nonce="{{.Nonce}}">
{{template "base-css" .}}
  /* The live room is dark in every scheme. These land after base-css so
     they win in both, without duplicating the media query. */
  :root {
    color-scheme: dark;
    --bg: #0e0e10; --fg: #efeff1; --muted: #a8a8ab; --line: #2a2a2d;
    --card: #18181b; --accent: #9147ff; --accent-fg: #ffffff; --focus: #bf94ff;
  }
  [hidden] { display: none !important; }
  .layout { display: grid; grid-template-columns: minmax(0, 1fr) 340px; height: 100dvh; }
  .stage { display: flex; flex-direction: column; min-width: 0; min-height: 0; }
  .video-wrap { flex: 1; min-height: 0; background: #000; }
  video { display: block; width: 100%; height: 100%; background: #000; }
  .meta { padding: 0.75rem 1rem; border-top: 1px solid var(--line); }
  .h1 { font-size: 1.05rem; font-weight: 600; margin: 0 0 0.25rem; }
  .host { color: var(--muted); font-size: 0.875rem; display: flex; flex-wrap: wrap; gap: 0.5rem; align-items: center; }
  .pill { display: inline-flex; align-items: center; gap: 0.3rem; font-weight: 600; color: #ff6b6b; }
  .pill svg { width: 0.7rem; height: 0.7rem; }
  .note { color: var(--muted); font-size: 0.875rem; padding: 0.5rem 1rem; }

  .side { border-left: 1px solid var(--line); display: flex; flex-direction: column; min-height: 0; }
  .tabs { display: flex; border-bottom: 1px solid var(--line); }
  .tab {
    flex: 1; padding: 0.7rem 0.5rem; text-align: center; cursor: pointer;
    color: var(--muted); background: none; border: 0; border-bottom: 2px solid transparent;
    font: inherit; font-weight: 600; display: inline-flex; align-items: center;
    justify-content: center; gap: 0.4rem;
  }
  .tab[aria-selected="true"] { color: var(--fg); border-bottom-color: var(--accent); }
  .badge {
    min-width: 1.25rem; padding: 0 0.35rem; border-radius: 999px; background: var(--accent);
    color: var(--accent-fg); font-size: 0.75rem; line-height: 1.25rem; font-weight: 700;
  }
  .pane { flex: 1; overflow-y: auto; overscroll-behavior: contain; padding: 0.75rem; min-height: 0; }
  .empty { color: var(--muted); font-size: 0.875rem; }
  .msg { margin-bottom: 0.45rem; font-size: 0.9rem; line-height: 1.45; overflow-wrap: anywhere; }
  .msg .name { font-weight: 600; color: #bf94ff; margin-right: 0.4rem; }
  .msg.question .name::after { content: " asks"; font-weight: 400; color: var(--muted); }

  .card { margin: 0 0 0.6rem; padding: 0.75rem; background: var(--card); border-radius: 6px; }
  .card.offer { border-left: 3px solid #00d684; }
  .card.poll { border-left: 3px solid #f7c948; }
  .card .h { font-weight: 600; margin-bottom: 0.3rem; overflow-wrap: anywhere; }
  .card .b { color: var(--muted); font-size: 0.9rem; overflow-wrap: anywhere; }
  .card a.cta {
    display: inline-block; margin-top: 0.5rem; padding: 0.45rem 0.85rem; background: #00d684;
    color: #04180f; border-radius: 6px; text-decoration: none; font-weight: 600;
  }
  .card .choice {
    display: block; width: 100%; margin: 0.25rem 0; padding: 0.5rem; background: #2a2a2d;
    color: var(--fg); border: 0; border-radius: 6px; cursor: pointer; text-align: left; font: inherit;
  }
  .card .choice:hover:not(:disabled) { background: #3a3a3d; }
  .card .choice:disabled { opacity: 0.6; cursor: default; }
  .card .choice[aria-pressed="true"] { background: var(--accent); color: var(--accent-fg); }

  .jump {
    position: absolute; left: 50%; transform: translateX(-50%); bottom: 3.6rem;
    background: var(--accent); color: var(--accent-fg); border: 0; border-radius: 999px;
    padding: 0.35rem 0.85rem; font: inherit; font-size: 0.8125rem; font-weight: 600; cursor: pointer;
  }
  .side { position: relative; }
  .conn { padding: 0.35rem 0.75rem; font-size: 0.8125rem; color: #f7c948; border-top: 1px solid var(--line); }
  .composer { display: flex; padding: 0.5rem; border-top: 1px solid var(--line); gap: 0.4rem; }
  .composer input {
    flex: 1; min-width: 0; padding: 0.5rem; background: #101013; color: var(--fg);
    border: 1px solid var(--line); border-radius: 6px; font: inherit;
  }
  .composer button {
    background: var(--accent); color: var(--accent-fg); border: 0; padding: 0.4rem 0.7rem;
    border-radius: 6px; cursor: pointer; display: inline-flex; align-items: center;
  }
  .composer button svg { width: 1.1rem; height: 1.1rem; }
  .composer button:disabled { opacity: 0.5; cursor: default; }

  .toast {
    position: fixed; right: 1rem; bottom: 1rem; max-width: 20rem; z-index: 10;
    background: var(--card); border: 1px solid var(--line); border-left: 3px solid var(--accent);
    border-radius: 8px; padding: 0.7rem 0.85rem; font-size: 0.9rem;
    display: flex; gap: 0.75rem; align-items: center; box-shadow: 0 6px 24px rgba(0,0,0,0.45);
  }
  .toast button {
    background: none; border: 0; color: var(--accent); font: inherit; font-weight: 600; cursor: pointer;
  }

  /* Phones and short windows: stack the video over the chat instead of
     squeezing it next to a fixed 340px column. */
  @media (max-width: 900px) {
    .layout { grid-template-columns: minmax(0, 1fr); grid-template-rows: auto minmax(12rem, 1fr); }
    .video-wrap { flex: none; aspect-ratio: 16 / 9; }
    .side { border-left: 0; border-top: 1px solid var(--line); }
    .toast { left: 1rem; right: 1rem; max-width: none; bottom: 4.5rem; }
  }
  @media (prefers-reduced-motion: reduce) {
    * { scroll-behavior: auto !important; }
  }
</style></head>
<body>
<div class="layout">
  <div class="stage">
    <div class="video-wrap"><video id="player" controls autoplay playsinline></video></div>
    <div class="meta">
      <div class="h1">{{.Title}}</div>
      <div class="host">
        {{if .HostName}}<span>Hosted by {{.HostName}}</span>{{end}}
        <span class="pill"><svg viewBox="0 0 8 8" aria-hidden="true"><circle cx="4" cy="4" r="4" fill="currentColor"/></svg>LIVE</span>
        <span id="viewers">{{.Viewers}} watching</span>
      </div>
      <div class="note" id="player-note" hidden></div>
    </div>
  </div>
  <div class="side">
    <div class="tabs" role="tablist" aria-label="Live room panels">
      <button type="button" class="tab" id="tab-chat" role="tab"
              aria-selected="true" aria-controls="chat-pane" data-pane="chat">Chat</button>
      <button type="button" class="tab" id="tab-offers" role="tab"
              aria-selected="false" aria-controls="offers-pane" data-pane="offers" tabindex="-1">
        Updates <span class="badge" id="badge" hidden></span>
      </button>
    </div>
    <div class="pane" id="chat-pane" role="tabpanel" aria-labelledby="tab-chat" tabindex="0">
      <p class="empty" id="chat-empty">No messages yet. Say hello.</p>
    </div>
    <div class="pane" id="offers-pane" role="tabpanel" aria-labelledby="tab-offers" tabindex="0" hidden>
      <p class="empty" id="offers-empty">Offers and polls from the host will appear here.</p>
    </div>
    <button type="button" class="jump" id="jump" hidden>New messages</button>
    <div class="conn" id="conn" role="status" hidden>Reconnecting&hellip;</div>
    <form class="composer" id="composer">
      <label class="sr-only" for="msg">Send a message to the room</label>
      <span class="sr-only" id="whoami">Posting as {{.DisplayName}}</span>
      <input id="msg" name="msg" placeholder="Say something&hellip;" maxlength="500"
             autocomplete="off" aria-describedby="whoami">
      <button type="submit" id="send" aria-label="Send message">
        <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8"
             stroke-linecap="round" stroke-linejoin="round" aria-hidden="true">
          <path d="M20 12L4 4l5 8-5 8z"/>
        </svg>
      </button>
    </form>
  </div>
</div>
<div class="toast" id="toast" role="status" aria-live="polite" hidden>
  <span id="toast-text"></span>
  <button type="button" id="toast-action">View</button>
</div>
<script nonce="{{.Nonce}}">
var PLAYBACK_URL   = "{{.PlaybackURL}}";
var PLAYBACK_KIND  = "{{.PlaybackKind}}";
var JOIN_TOKEN     = "{{.JoinToken}}";
var WEBINARS_BASE  = "{{.WebinarsBase}}";
var STREAMING_BASE = "{{.StreamingBase}}";
var STREAM_ID      = {{.StreamID}};
var STREAM_AUTH    = { t: "{{.HBToken}}", exp: "{{.HBExp}}", sig: "{{.HBSig}}" };
var HEARTBEAT_URL  = "{{.HeartbeatURL}}";
var HEARTBEAT_MS   = {{.HeartbeatMS}};
var PROJECT_ID     = new URLSearchParams(location.search).get("project_id") || "";

var POLL_MS = 2000, MAX_BACKOFF_MS = 30000, MAX_CHAT_NODES = 300, PIN_SLOP_PX = 48;

var chatPane   = document.getElementById("chat-pane");
var offersPane = document.getElementById("offers-pane");
var badge      = document.getElementById("badge");
var jumpBtn    = document.getElementById("jump");
var connEl     = document.getElementById("conn");
var toastEl    = document.getElementById("toast");

function params(extra) {
  var p = new URLSearchParams();
  if (PROJECT_ID) p.set("project_id", PROJECT_ID);
  for (var k in extra) if (extra[k] !== undefined && extra[k] !== null && extra[k] !== "") p.set(k, extra[k]);
  return p.toString();
}

function setPlayerNote(text) {
  var el = document.getElementById("player-note");
  el.textContent = text;
  el.hidden = !text;
}

/* Player. PLAYBACK_KIND comes from the server rather than sniffing the
   URL for ".m3u8" — a signed URL carries a query string, so the old
   endsWith check was already wrong for MP4 replays. */
function initPlayer() {
  var video = document.getElementById("player");
  var native = video.canPlayType("application/vnd.apple.mpegurl") !== "";
  if (PLAYBACK_KIND === "hls" && typeof Hls !== "undefined" && Hls.isSupported()) {
    var hls = new Hls({ lowLatencyMode: true });
    hls.loadSource(PLAYBACK_URL);
    hls.attachMedia(video);
    hls.on(Hls.Events.ERROR, function (evt, data) {
      if (!data || !data.fatal) return;
      if (data.type === Hls.ErrorTypes.NETWORK_ERROR) { hls.startLoad(); return; }
      if (data.type === Hls.ErrorTypes.MEDIA_ERROR) { hls.recoverMediaError(); return; }
      setPlayerNote("Playback stopped. Reload the page to try again.");
    });
  } else if (PLAYBACK_KIND !== "hls" || native) {
    video.src = PLAYBACK_URL;
  } else {
    setPlayerNote("This browser cannot play the stream. Try Safari, Chrome or Firefox.");
  }
}
document.addEventListener("DOMContentLoaded", initPlayer);

/* Heartbeats: streaming (anonymous capacity gauge) + webinars
   (per-registrant attendance).

   Streaming gates the heartbeat through the same authorization as media
   playback, so under a signed-URL policy a bare token is answered with
   404 and every viewer count silently reads zero. HEARTBEAT_URL is the
   signed endpoint streaming minted for us; STREAM_AUTH reassembles the
   beat when it couldn't. The v parameter keeps one viewer counted as one
   viewer when cookies are blocked. */
function viewerID() {
  try {
    var v = sessionStorage.getItem("apteva_viewer_id");
    if (!v) {
      v = "w" + Math.random().toString(36).slice(2, 12);
      sessionStorage.setItem("apteva_viewer_id", v);
    }
    return v;
  } catch (e) { return ""; }
}
var VIEWER_ID = viewerID();

function streamBeatURL() {
  if (HEARTBEAT_URL) {
    return HEARTBEAT_URL + (HEARTBEAT_URL.indexOf("?") === -1 ? "?" : "&") +
      params({ v: VIEWER_ID });
  }
  return STREAMING_BASE + "/heartbeat/" + STREAM_ID + "?" + params({
    t: STREAM_AUTH.t, exp: STREAM_AUTH.exp, sig: STREAM_AUTH.sig, v: VIEWER_ID
  });
}

function heartbeat() {
  try {
    fetch(streamBeatURL(),
      { method: "POST", credentials: "include", keepalive: true });
    fetch(WEBINARS_BASE + "/live/" + JOIN_TOKEN + "/heartbeat?" + params({}),
      { method: "POST", keepalive: true });
  } catch (e) {}
}
heartbeat();
setInterval(heartbeat, HEARTBEAT_MS);

/* Tabs: real buttons with a roving tabindex, so the panel switcher is
   reachable by keyboard. It used to be a div with an onclick. */
var tabs = Array.prototype.slice.call(document.querySelectorAll(".tab"));
function selectTab(tab, focus) {
  tabs.forEach(function (t) {
    var on = t === tab;
    t.setAttribute("aria-selected", on ? "true" : "false");
    t.tabIndex = on ? 0 : -1;
    document.getElementById(t.getAttribute("aria-controls")).hidden = !on;
  });
  if (focus) tab.focus();
  document.getElementById("composer").hidden = tab.dataset.pane !== "chat";
  if (tab.dataset.pane === "offers") clearUnseen();
  if (tab.dataset.pane === "chat") scrollChatToEnd();
}
tabs.forEach(function (tab, i) {
  tab.addEventListener("click", function () { selectTab(tab, false); });
  tab.addEventListener("keydown", function (e) {
    var next = null;
    if (e.key === "ArrowRight") next = tabs[(i + 1) % tabs.length];
    else if (e.key === "ArrowLeft") next = tabs[(i - 1 + tabs.length) % tabs.length];
    else if (e.key === "Home") next = tabs[0];
    else if (e.key === "End") next = tabs[tabs.length - 1];
    if (next) { e.preventDefault(); selectTab(next, true); }
  });
});

/* Unread indicator. An offer or a poll landing in a hidden tab used to
   be completely silent, which defeats the point of the funnel. */
var unseen = 0;
function noteUnseen(label) {
  if (document.getElementById("tab-offers").getAttribute("aria-selected") === "true") return;
  unseen++;
  badge.textContent = unseen;
  badge.hidden = false;
  document.getElementById("tab-offers").setAttribute("aria-label", "Updates, " + unseen + " new");
  showToast(label);
}
function clearUnseen() {
  unseen = 0;
  badge.hidden = true;
  document.getElementById("tab-offers").removeAttribute("aria-label");
  hideToast();
}
var toastTimer = null;
function showToast(text) {
  document.getElementById("toast-text").textContent = text;
  toastEl.hidden = false;
  clearTimeout(toastTimer);
  toastTimer = setTimeout(hideToast, 9000);
}
function hideToast() { toastEl.hidden = true; }
document.getElementById("toast-action").addEventListener("click", function () {
  selectTab(document.getElementById("tab-offers"), true);
});

/* Scroll anchoring. The old loop set scrollTop = 999999 on every tick,
   so reading anything older than the last message was impossible during
   a busy room. */
function chatPinned() {
  return chatPane.scrollHeight - chatPane.scrollTop - chatPane.clientHeight < PIN_SLOP_PX;
}
function scrollChatToEnd() {
  chatPane.scrollTop = chatPane.scrollHeight;
  jumpBtn.hidden = true;
}
jumpBtn.addEventListener("click", scrollChatToEnd);
chatPane.addEventListener("scroll", function () { if (chatPinned()) jumpBtn.hidden = true; });

/* Trim the transcript. A two-hour room at a few messages a second would
   otherwise grow the DOM without limit until the tab is unusable. */
function trimChat() {
  while (chatPane.children.length > MAX_CHAT_NODES) chatPane.removeChild(chatPane.firstChild);
}

function el(tag, cls, text) {
  var n = document.createElement(tag);
  if (cls) n.className = cls;
  if (text !== undefined && text !== null) n.textContent = text;
  return n;
}

function renderChat(e) {
  var hide = document.getElementById("chat-empty");
  if (hide) hide.remove();
  var wasPinned = chatPinned();
  var div = el("div", "msg" + (e.kind_detail === "question" ? " question" : ""));
  div.appendChild(el("span", "name", (e.display_name || "Guest") + ":"));
  div.appendChild(el("span", "body", e.body || ""));
  chatPane.appendChild(div);
  trimChat();
  if (wasPinned) scrollChatToEnd(); else jumpBtn.hidden = false;
}

function renderOffer(e) {
  var hide = document.getElementById("offers-empty");
  if (hide) hide.remove();
  var card = el("div", "card offer");
  card.appendChild(el("div", "h", e.headline || ""));
  if (e.body) card.appendChild(el("div", "b", e.body));
  if (e.cta_url && /^https?:\/\//i.test(e.cta_url)) {
    var a = el("a", "cta", e.cta_label || "Open");
    a.href = e.cta_url;
    a.target = "_blank";
    a.rel = "noopener noreferrer";
    a.addEventListener("click", function () {
      fetch(WEBINARS_BASE + "/live/" + JOIN_TOKEN + "/offer-click?" + params({ offer_id: e.id }),
        { method: "POST", keepalive: true });
    });
    card.appendChild(a);
  }
  offersPane.appendChild(card);
  noteUnseen("The host shared an offer");
}

function renderPoll(e) {
  var hide = document.getElementById("offers-empty");
  if (hide) hide.remove();
  var card = el("div", "card poll");
  var q = el("div", "h", e.question || "");
  q.id = "poll-q-" + e.id;
  card.appendChild(q);
  var group = el("div", "");
  group.setAttribute("role", "group");
  group.setAttribute("aria-labelledby", q.id);
  (e.choices || []).forEach(function (choice, i) {
    var btn = el("button", "choice", choice);
    btn.type = "button";
    btn.setAttribute("aria-pressed", "false");
    btn.addEventListener("click", function () {
      group.querySelectorAll(".choice").forEach(function (b) {
        b.disabled = true;
        b.setAttribute("aria-pressed", b === btn ? "true" : "false");
      });
      fetch(WEBINARS_BASE + "/live/" + JOIN_TOKEN + "/poll-response?" + params({ poll_id: e.id, choice: i }),
        { method: "POST", keepalive: true });
    });
    group.appendChild(btn);
  });
  card.appendChild(group);
  offersPane.appendChild(card);
  noteUnseen("The host opened a poll");
}

/* Event poll. Backs off on failure instead of hammering a struggling
   sidecar every 2s, and says so once it has failed twice. */
var cursor = 0, backoff = POLL_MS, failures = 0;

function setConn(down) { connEl.hidden = !down; }

function applyEvents(data) {
  if (data.status === "ended" || data.status === "cancelled") { location.reload(); return; }
  cursor = data.cursor || cursor;
  if (data.viewers !== null && data.viewers !== undefined) {
    document.getElementById("viewers").textContent = data.viewers + " watching";
  }
  (data.events || []).forEach(function (e) {
    if (e.kind === "chat") renderChat(e);
    else if (e.kind === "offer") renderOffer(e);
    else if (e.kind === "poll") renderPoll(e);
  });
}

async function pollEvents() {
  var ok = false;
  try {
    var res = await fetch(WEBINARS_BASE + "/live/" + JOIN_TOKEN + "/events?" + params({ since: cursor }));
    if (res.ok) { applyEvents(await res.json()); ok = true; }
  } catch (e) {}
  if (ok) { failures = 0; backoff = POLL_MS; setConn(false); }
  else {
    failures++;
    backoff = Math.min(backoff * 2, MAX_BACKOFF_MS);
    if (failures >= 2) setConn(true);
  }
  setTimeout(pollEvents, backoff);
}
pollEvents();

/* Compose. */
document.getElementById("composer").addEventListener("submit", async function (e) {
  e.preventDefault();
  var input = document.getElementById("msg");
  var send = document.getElementById("send");
  var text = input.value.trim();
  if (!text) return;
  input.value = "";
  send.disabled = true;
  try {
    var res = await fetch(WEBINARS_BASE + "/live/" + JOIN_TOKEN + "/chat?" + params({}), {
      method: "POST",
      headers: { "Content-Type": "application/json" },
      body: JSON.stringify({ body: text }),
    });
    if (!res.ok) throw new Error("send failed");
  } catch (err) {
    input.value = text;
    showToast("That message did not send. Try again.");
  }
  send.disabled = false;
  input.focus();
});
</script>
</body></html>`

// ─── Live room sub-routes ─────────────────────────────────────────

// handleLiveHeartbeat books attendance for this registrant.
//
// This used to be a straight-through SQLite upsert: 1000 viewers at one
// beat per 10s is 100 writes/sec on a pool the SDK caps at ONE
// connection, interleaved with the events poll's reads. It also never
// cleared left_at (one backgrounded tab marked a viewer "left" for the
// rest of the webinar while their watch time kept climbing) and credited
// a flat +10s per beat regardless of elapsed time, so anyone could mint
// their own watch_seconds and attended_live by looping the endpoint.
// RecordHeartbeat accumulates in memory, clamps credit to real elapsed
// time, and lets the attendance-flush worker write the batch.
func (a *App) handleLiveHeartbeat(rw http.ResponseWriter, r *http.Request, ctx *sdk.AppCtx, webinar *Webinar, reg *Registrant) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		httpErr(rw, http.StatusMethodNotAllowed, "POST")
		return
	}
	credited := globalApp.RecordHeartbeat(ctx, webinar, reg)
	httpJSON(rw, map[string]any{"ok": true, "credited_seconds": credited})
}

func (a *App) handleLiveChat(rw http.ResponseWriter, r *http.Request, ctx *sdk.AppCtx, webinar *Webinar, reg *Registrant) {
	if r.Method != http.MethodPost {
		httpErr(rw, http.StatusMethodNotAllowed, "POST")
		return
	}
	var body struct {
		Body string `json:"body"`
		Kind string `json:"kind"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(rw, r.Body, 8<<10)).Decode(&body); err != nil {
		httpErr(rw, http.StatusBadRequest, "invalid json")
		return
	}
	// Truncate by rune, not by byte — a byte slice through a multi-byte
	// character stores invalid UTF-8, which then comes back out of
	// /events as replacement characters for everyone in the room.
	text := truncateRunes(stripControlRunes(body.Body), 500)
	if text == "" {
		httpErr(rw, http.StatusBadRequest, "body required")
		return
	}
	kind := "message"
	if body.Kind == "question" {
		kind = "question"
	}
	displayName := reg.DisplayName
	if displayName == "" {
		displayName = "Guest"
	}

	// InsertChatMessage allocates the sequence atomically. The old
	// hand-rolled MAX+1 read-then-insert handed the same number to two
	// concurrent posts, and the /events cursor (`sequence > since`)
	// then stepped past both after delivering one — silent message loss
	// under exactly the load a live room is built for. It also writes
	// created_at as RFC3339 rather than leaning on SQLite's
	// CURRENT_TIMESTAMP, which the readers cannot parse.
	id, seq, err := globalApp.InsertChatMessage(ctx, webinar, reg, displayName, text, kind)
	if err != nil {
		ctx.Logger().Warn("chat insert", "webinar_id", webinar.ID, "err", err)
		httpErr(rw, http.StatusInternalServerError, "could not send message")
		return
	}
	httpJSON(rw, map[string]any{"id": id, "sequence": seq})
}

// handleLivePollResponse — record one vote.
//
// poll_id arrives from the browser as a sequential INTEGER PRIMARY KEY.
// The old handler INSERTed it with no check that the poll belonged to
// the caller's webinar (let alone their project), and no closes_at
// check: any registrant of any webinar could enumerate ids and stuff
// another tenant's poll results. RecordPollResponse resolves the id back
// to (project, webinar) first.
func (a *App) handleLivePollResponse(rw http.ResponseWriter, r *http.Request, ctx *sdk.AppCtx, webinar *Webinar, reg *Registrant) {
	if r.Method != http.MethodPost {
		httpErr(rw, http.StatusMethodNotAllowed, "POST")
		return
	}
	pollID, _ := strconv.ParseInt(r.URL.Query().Get("poll_id"), 10, 64)
	choice, _ := strconv.Atoi(r.URL.Query().Get("choice"))
	if pollID == 0 {
		httpErr(rw, http.StatusBadRequest, "poll_id required")
		return
	}
	if err := globalApp.RecordPollResponse(ctx, webinar, reg, pollID, choice); err != nil {
		writeEngagementError(rw, ctx, "poll response", webinar.ID, err)
		return
	}
	httpJSON(rw, map[string]any{"ok": true})
}

// handleLiveOfferClick — attribute one click. Same IDOR as the poll
// path: offer_id was written straight through, so another tenant's offer
// CTR was anyone's to inflate.
func (a *App) handleLiveOfferClick(rw http.ResponseWriter, r *http.Request, ctx *sdk.AppCtx, webinar *Webinar, reg *Registrant) {
	if r.Method != http.MethodPost {
		httpErr(rw, http.StatusMethodNotAllowed, "POST")
		return
	}
	offerID, _ := strconv.ParseInt(r.URL.Query().Get("offer_id"), 10, 64)
	if offerID == 0 {
		httpErr(rw, http.StatusBadRequest, "offer_id required")
		return
	}
	if err := globalApp.RecordOfferClick(ctx, webinar, reg, offerID); err != nil {
		writeEngagementError(rw, ctx, "offer click", webinar.ID, err)
		return
	}
	httpJSON(rw, map[string]any{"ok": true})
}

// writeEngagementError maps the engagement sentinels onto status codes.
// errEngagementNotFound deliberately covers both "no such id" and
// "belongs to someone else", so the response cannot be used as an
// id-enumeration oracle.
func writeEngagementError(rw http.ResponseWriter, ctx *sdk.AppCtx, what string, webinarID int64, err error) {
	switch {
	case errors.Is(err, errEngagementNotFound):
		httpErr(rw, http.StatusNotFound, "not found")
	case errors.Is(err, errPollClosed):
		httpErr(rw, http.StatusConflict, "poll is closed")
	case errors.Is(err, errInvalidChoice):
		httpErr(rw, http.StatusBadRequest, "invalid choice")
	default:
		ctx.Logger().Warn(what, "webinar_id", webinarID, "err", err)
		httpErr(rw, http.StatusInternalServerError, "could not record")
	}
}

// handleLiveEvents — poll endpoint returning chat + offers + polls newer
// than the cursor. Single response, ~2s client poll.
func (a *App) handleLiveEvents(rw http.ResponseWriter, r *http.Request, ctx *sdk.AppCtx, webinar *Webinar, reg *Registrant) {
	if r.Method != http.MethodGet {
		httpErr(rw, http.StatusMethodNotAllowed, "GET")
		return
	}
	since, _ := strconv.Atoi(r.URL.Query().Get("since"))

	type event struct {
		Kind        string   `json:"kind"`
		ID          int64    `json:"id"`
		Sequence    int      `json:"sequence"`
		DisplayName string   `json:"display_name,omitempty"`
		Body        string   `json:"body,omitempty"`
		KindDetail  string   `json:"kind_detail,omitempty"`
		Headline    string   `json:"headline,omitempty"`
		CTALabel    string   `json:"cta_label,omitempty"`
		CTAURL      string   `json:"cta_url,omitempty"`
		Question    string   `json:"question,omitempty"`
		Choices     []string `json:"choices,omitempty"`
	}
	events := []event{}
	maxSeq := since

	chatRows, err := ctx.AppDB().Query(
		`SELECT id, sequence, display_name, body, kind FROM webinar_chat
		 WHERE webinar_id = ? AND sequence > ?
		 ORDER BY sequence ASC LIMIT 200`, webinar.ID, since)
	if err == nil {
		for chatRows.Next() {
			e := event{Kind: "chat"}
			_ = chatRows.Scan(&e.ID, &e.Sequence, &e.DisplayName, &e.Body, &e.KindDetail)
			events = append(events, e)
			if e.Sequence > maxSeq {
				maxSeq = e.Sequence
			}
		}
		chatRows.Close()
	}

	offerRows, err := ctx.AppDB().Query(
		`SELECT id, sequence, headline, COALESCE(body,''), cta_label, cta_url
		 FROM webinar_offers
		 WHERE webinar_id = ? AND shown_at IS NOT NULL AND sequence > ?
		 ORDER BY sequence ASC LIMIT 50`, webinar.ID, since)
	if err == nil {
		for offerRows.Next() {
			e := event{Kind: "offer"}
			_ = offerRows.Scan(&e.ID, &e.Sequence, &e.Headline, &e.Body, &e.CTALabel, &e.CTAURL)
			events = append(events, e)
			if e.Sequence > maxSeq {
				maxSeq = e.Sequence
			}
		}
		offerRows.Close()
	}

	pollRows, err := ctx.AppDB().Query(
		`SELECT id, sequence, question, choices FROM webinar_polls
		 WHERE webinar_id = ? AND sequence > ?
		 ORDER BY sequence ASC LIMIT 20`, webinar.ID, since)
	if err == nil {
		for pollRows.Next() {
			e := event{Kind: "poll"}
			var choicesJSON string
			_ = pollRows.Scan(&e.ID, &e.Sequence, &e.Question, &choicesJSON)
			_ = json.Unmarshal([]byte(choicesJSON), &e.Choices)
			events = append(events, e)
			if e.Sequence > maxSeq {
				maxSeq = e.Sequence
			}
		}
		pollRows.Close()
	}

	httpJSON(rw, map[string]any{
		"cursor":  maxSeq,
		"events":  events,
		"viewers": a.cachedViewerCount(webinar.StreamID),
		// The client reloads on a terminal status, so a room that ends
		// while people are watching moves them to the replay instead of
		// leaving them staring at a frozen frame.
		"status": webinar.Status,
	})
}

// ─── Viewer-count cache ───────────────────────────────────────────
//
// The events endpoint is polled every 2s by every attendee, and it used
// to ask streaming for metrics on each one — 1000 viewers is 500
// cross-app MCP round-trips per second for a number that changes on a
// 10s heartbeat cadence. One short-TTL cache per stream collapses that
// to one call per tick, for everybody.

var (
	viewerCacheMu sync.Mutex
	viewerCache   = map[int64]viewerCacheEntry{}
)

type viewerCacheEntry struct {
	count int
	at    time.Time
}

const viewerCacheTTL = 3 * time.Second

func (a *App) cachedViewerCount(streamID int64) int {
	if streamID == 0 || a.streamingCaller == nil {
		return 0
	}
	viewerCacheMu.Lock()
	entry, ok := viewerCache[streamID]
	if ok && time.Since(entry.at) < viewerCacheTTL {
		viewerCacheMu.Unlock()
		return entry.count
	}
	viewerCacheMu.Unlock()

	count := entry.count
	if m, err := a.streamingCaller.GetMetrics(streamID); err == nil {
		count = m.CurrentViewers
	}
	viewerCacheMu.Lock()
	viewerCache[streamID] = viewerCacheEntry{count: count, at: time.Now()}
	viewerCacheMu.Unlock()
	return count
}

// ─── Replay (public) ──────────────────────────────────────────────

type replayView struct {
	Nonce        string
	Title        string
	HostName     string
	PlaybackURL  string
	PlaybackKind string
	HLSSrc       string
	HLSIntegrity string

	// JoinToken is set when the visitor arrived with `r=<join_token>`,
	// which is what makes replay watch time attributable.
	JoinToken    string
	WebinarsBase string
	HeartbeatMS  int
}

var replayTmpl = template.Must(
	template.Must(template.New("replay").Parse(sharedDefs)).Parse(replayHTML))

func (a *App) handleReplayPage(rw http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/replay/")
	rest = strings.Trim(rest, "/")
	if rest == "" {
		notFoundPage(rw)
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(rw, http.StatusBadRequest, err.Error())
		return
	}
	ctx := globalCtx
	app := globalApp
	if ctx == nil || app == nil {
		httpErr(rw, http.StatusServiceUnavailable, "sidecar not mounted")
		return
	}
	w, err := app.dbGetBySlug(ctx, pid, rest)
	if err != nil {
		httpErr(rw, http.StatusInternalServerError, err.Error())
		return
	}
	if w == nil || !w.RecordingPublished {
		notFoundPage(rw)
		return
	}
	// Constant-time: the replay token is a secret, and `!=` on a string
	// leaks its prefix through timing to anyone willing to measure.
	if w.ReplayToken != "" && !constantTimeEqual(r.URL.Query().Get("t"), w.ReplayToken) {
		notFoundPage(rw)
		return
	}

	// ReplayPlayback performs the expiry check itself and mints a signed
	// URL whose lifetime can never outlive replay_expires_at. The page
	// used to check the expiry and then hand out streaming's STATIC
	// playback_token, so "expired" stopped nobody who had already
	// loaded the page — or read the URL out of a devtools tab.
	playback, err := app.ReplayPlayback(ctx, w)
	switch {
	case errors.Is(err, errReplayExpired):
		replayExpiredPage(rw, w)
		return
	case errors.Is(err, errReplayUnavailable):
		replayUnavailablePage(rw, w)
		return
	case err != nil:
		ctx.Logger().Warn("replay playback", "webinar_id", w.ID, "err", err)
		replayUnavailablePage(rw, w)
		return
	}
	if !playback.Signed {
		ctx.Logger().Warn("replay fell back to an unsigned playback URL — expiry is not enforced on this URL",
			"webinar_id", w.ID, "stream_id", w.StreamID)
	}

	view := replayView{
		Nonce:        newCSPNonce(),
		Title:        w.Title,
		HostName:     w.HostName,
		PlaybackURL:  playback.URL,
		PlaybackKind: playback.Kind,
		HLSSrc:       hlsScriptURL,
		HLSIntegrity: hlsScriptIntegrity,
	}
	// Attendance on the replay page. Without this, attended_replay was
	// only ever set for people who happened to reopen their /live/<token>
	// link after the webinar ended, and replay viewership read as almost
	// nothing. `r=<join_token>` is minted by replayLinkFor.
	if rTok := strings.TrimSpace(r.URL.Query().Get("r")); rTok != "" {
		if reg, rErr := app.dbGetRegistrantByToken(ctx, pid, rTok); rErr == nil && reg != nil && reg.WebinarID == w.ID {
			view.JoinToken = reg.JoinToken
			view.WebinarsBase = app.publicAppPath(ctx)
			view.HeartbeatMS = app.heartbeatIntervalSeconds(ctx) * 1000
		}
	}

	writePageHeaders(rw, view.Nonce, originOf(playback.URL), originOf(view.WebinarsBase))
	rw.Header().Set("Cache-Control", "no-store")
	if err := replayTmpl.Execute(rw, view); err != nil {
		ctx.Logger().Warn("render replay page", "webinar_id", w.ID, "err", err)
	}
}

const replayHTML = `<!doctype html>
<html lang="en"><head><meta charset="utf-8">
<title>{{.Title}} &mdash; replay</title>
<meta name="viewport" content="width=device-width, initial-scale=1, viewport-fit=cover">
<meta name="robots" content="noindex">
<script src="{{.HLSSrc}}" integrity="{{.HLSIntegrity}}" crossorigin="anonymous" nonce="{{.Nonce}}" defer></script>
<style nonce="{{.Nonce}}">
{{template "base-css" .}}
  :root {
    color-scheme: dark;
    --bg: #0b0b0d; --fg: #efeff1; --muted: #a8a8ab; --line: #2a2a2d;
    --card: #18181b; --accent: #9147ff; --accent-fg: #ffffff; --focus: #bf94ff;
  }
  [hidden] { display: none !important; }
  body { display: flex; flex-direction: column; min-height: 100dvh; }
  .video-wrap { background: #000; aspect-ratio: 16 / 9; max-height: 80dvh; }
  video { display: block; width: 100%; height: 100%; background: #000; }
  .meta { padding: 1.25rem 1rem; max-width: 60rem; width: 100%; margin: 0 auto; }
  h1 { font-size: 1.35rem; margin: 0 0 0.3rem; line-height: 1.3; }
  .host { color: var(--muted); }
  .note { color: var(--muted); font-size: 0.875rem; margin-top: 0.75rem; }
</style></head>
<body>
<div class="video-wrap"><video id="player" controls playsinline></video></div>
<div class="meta">
  <h1>{{.Title}}</h1>
  {{if .HostName}}<div class="host">Hosted by {{.HostName}}</div>{{end}}
  <div class="note" id="note" hidden></div>
</div>
<script nonce="{{.Nonce}}">
var PLAYBACK_URL  = "{{.PlaybackURL}}";
var PLAYBACK_KIND = "{{.PlaybackKind}}";
var JOIN_TOKEN    = "{{.JoinToken}}";
var WEBINARS_BASE = "{{.WebinarsBase}}";
var HEARTBEAT_MS  = {{.HeartbeatMS}};
var PROJECT_ID    = new URLSearchParams(location.search).get("project_id") || "";

function setNote(text) {
  var el = document.getElementById("note");
  el.textContent = text;
  el.hidden = !text;
}

function initPlayer() {
  var video = document.getElementById("player");
  var native = video.canPlayType("application/vnd.apple.mpegurl") !== "";
  if (PLAYBACK_KIND === "hls" && typeof Hls !== "undefined" && Hls.isSupported()) {
    var hls = new Hls();
    hls.loadSource(PLAYBACK_URL);
    hls.attachMedia(video);
    hls.on(Hls.Events.ERROR, function (evt, data) {
      if (!data || !data.fatal) return;
      if (data.type === Hls.ErrorTypes.NETWORK_ERROR) { hls.startLoad(); return; }
      if (data.type === Hls.ErrorTypes.MEDIA_ERROR) { hls.recoverMediaError(); return; }
      setNote("This replay link has stopped working. Reload the page to get a fresh one.");
    });
  } else if (PLAYBACK_KIND !== "hls" || native) {
    video.src = PLAYBACK_URL;
  } else {
    setNote("This browser cannot play the recording. Try Safari, Chrome or Firefox.");
  }
}
document.addEventListener("DOMContentLoaded", initPlayer);

{{if .JoinToken}}
/* Attendance. Only beats while the recording is actually playing, so
   leaving the tab open overnight does not book eight hours of replay
   watch time. The server clamps credit to real elapsed time regardless.
   Emitted only when the visitor arrived with r=<join_token>; an
   anonymous viewer has no identity to attribute watch time to. */
var beat = function () {
  var video = document.getElementById("player");
  if (!video || video.paused || video.ended) return;
  var p = new URLSearchParams();
  if (PROJECT_ID) p.set("project_id", PROJECT_ID);
  try {
    fetch(WEBINARS_BASE + "/live/" + JOIN_TOKEN + "/heartbeat?" + p.toString(),
      { method: "POST", keepalive: true });
  } catch (e) {}
};
document.addEventListener("DOMContentLoaded", function () {
  document.getElementById("player").addEventListener("playing", beat, { once: true });
});
setInterval(beat, HEARTBEAT_MS || 10000);
{{end}}
</script>
</body></html>`

// ─── Small text helpers ───────────────────────────────────────────

// truncateRunes caps s at n runes without splitting a character.
func truncateRunes(s string, n int) string {
	if n <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return strings.TrimSpace(string(runes[:n]))
}

// stripControlRunes drops C0/C1 control characters other than newline
// and tab. Chat is rendered with textContent, so this is tidiness rather
// than a safety boundary — but a NUL or a bidi-override in a display
// name still has no business reaching the CRM or a reminder email.
func stripControlRunes(s string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if r < 0x20 || (r >= 0x7f && r <= 0x9f) {
			return -1
		}
		return r
	}, s))
}

// ─── DB helpers used only by public.go ────────────────────────────

func (a *App) dbGetBySlug(ctx *sdk.AppCtx, pid, slug string) (*Webinar, error) {
	var id int64
	err := ctx.AppDB().QueryRow(
		`SELECT id FROM webinars WHERE project_id = ? AND slug = ?`, pid, slug).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return a.dbGet(ctx, pid, id)
}

func (a *App) dbGetRegistrantByToken(ctx *sdk.AppCtx, pid, token string) (*Registrant, error) {
	var id int64
	err := ctx.AppDB().QueryRow(
		`SELECT id FROM webinar_registrants WHERE project_id = ? AND join_token = ?`,
		pid, token).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return a.dbGetRegistrant(ctx, pid, id)
}
