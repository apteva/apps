package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// ─── Registration (public) ────────────────────────────────────────
//
// GET  /r/<slug>           → HTML form
// POST /r/<slug>           → submit form, 302 to /live/<token>

func (a *App) handleRegistrationPage(w http.ResponseWriter, r *http.Request) {
	slug := strings.TrimPrefix(r.URL.Path, "/r/")
	slug = strings.Trim(slug, "/")
	if slug == "" {
		http.NotFound(w, r)
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
		http.NotFound(w, r)
		return
	}

	switch r.Method {
	case http.MethodGet:
		renderRegistrationForm(w, webinar)
	case http.MethodPost:
		_ = r.ParseForm()
		email := strings.TrimSpace(r.FormValue("email"))
		phone := strings.TrimSpace(r.FormValue("phone"))
		name := strings.TrimSpace(r.FormValue("display_name"))
		if email == "" && phone == "" {
			renderRegistrationForm(w, webinar)
			return
		}
		out, err := app.toolRegister(ctx, map[string]any{
			"_project_id":  pid,
			"webinar_id":   webinar.ID,
			"email":        email,
			"phone":        phone,
			"display_name": name,
			"source":       "form",
		})
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		reg := out.(map[string]any)["registrant"].(*Registrant)
		// Redirect to the live room URL; tack on project_id so the
		// scope=global path keeps working.
		live := reg.JoinURL
		if !strings.Contains(live, "?") {
			live += "?project_id=" + pid
		} else {
			live += "&project_id=" + pid
		}
		http.Redirect(w, r, live, http.StatusFound)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "GET or POST")
	}
}

func renderRegistrationForm(w http.ResponseWriter, webinar *Webinar) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	when := webinar.ScheduledAt
	if when == "" {
		when = "Soon"
	}
	host := html.EscapeString(webinar.HostName)
	title := html.EscapeString(webinar.Title)
	desc := html.EscapeString(webinar.Description)
	fmt.Fprintf(w, `<!doctype html>
<html><head><meta charset="utf-8"><title>Register: %s</title>
<meta name="viewport" content="width=device-width, initial-scale=1">
<style>
  body { font-family: -apple-system, BlinkMacSystemFont, sans-serif; max-width: 540px; margin: 4rem auto; padding: 0 1rem; color: #1a1a1a; }
  h1 { margin-bottom: 0.25rem; }
  .meta { color: #666; margin-bottom: 1.5rem; }
  form { display: grid; gap: 0.75rem; }
  input { font-size: 1rem; padding: 0.6rem; border: 1px solid #ccc; border-radius: 6px; }
  button { font-size: 1rem; padding: 0.7rem; background: #1a73e8; color: white; border: 0; border-radius: 6px; cursor: pointer; }
  button:hover { background: #155ec0; }
  .desc { white-space: pre-wrap; margin-bottom: 1rem; }
</style></head>
<body>
  <h1>%s</h1>
  <div class="meta">%s · %s</div>
  <div class="desc">%s</div>
  <form method="POST">
    <input type="text"  name="display_name" placeholder="Your name" required>
    <input type="email" name="email"        placeholder="Email"      required>
    <input type="tel"   name="phone"        placeholder="Phone (optional, for SMS reminders)">
    <button type="submit">Save my seat</button>
  </form>
</body></html>`,
		title, title, html.EscapeString(when), host, desc)
}

// ─── Live route dispatcher ────────────────────────────────────────
//
// One handler so we can route /live/<token>, /live/<token>/heartbeat,
// /live/<token>/chat, /live/<token>/poll-response, /live/<token>/offer-click,
// /live/<token>/events all in one place.

func (a *App) handleLiveRoute(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/live/")
	rest = strings.Trim(rest, "/")
	if rest == "" {
		http.NotFound(w, r)
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
		http.NotFound(w, r)
		return
	}
	webinar, err := app.dbGet(ctx, pid, reg.WebinarID)
	if err != nil || webinar == nil {
		http.NotFound(w, r)
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
		http.NotFound(w, r)
	}
}

// handleLiveRoom — serve the HTML + JS that runs the player + polls
// the events endpoint.
func (a *App) handleLiveRoom(rw http.ResponseWriter, r *http.Request, ctx *sdk.AppCtx, webinar *Webinar, reg *Registrant) {
	// Fetch playback URL via streaming — it's the source of truth.
	var snap *StreamSnapshot
	if webinar.StreamID != 0 {
		s, err := globalApp.streamingCaller.GetStream(webinar.StreamID)
		if err == nil {
			snap = &s
		}
	}
	if snap == nil {
		httpErr(rw, http.StatusServiceUnavailable, "stream not ready")
		return
	}

	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	rw.Header().Set("Cache-Control", "no-cache, no-store, must-revalidate")
	playback := html.EscapeString(snap.PlaybackURL)
	streamID := snap.ID
	playbackToken := html.EscapeString(snap.PlaybackToken)
	title := html.EscapeString(webinar.Title)
	hostName := html.EscapeString(webinar.HostName)
	displayName := html.EscapeString(reg.DisplayName)
	if displayName == "" {
		displayName = "Guest"
	}
	joinToken := html.EscapeString(reg.JoinToken)
	publicBase := globalApp.publicAppPath(ctx)
	streamingBase := strings.Replace(publicBase, "/api/apps/webinars", "/api/apps/streaming", 1)

	fmt.Fprintf(rw, liveRoomHTML,
		title, title, hostName,
		playback, displayName, joinToken,
		publicBase, streamingBase,
		streamID, playbackToken)
}

const liveRoomHTML = `<!doctype html>
<html><head><meta charset="utf-8"><title>%s</title>
<meta name="viewport" content="width=device-width, initial-scale=1">
<script src="https://cdn.jsdelivr.net/npm/hls.js@1"></script>
<style>
  body { font-family: -apple-system, BlinkMacSystemFont, sans-serif; margin: 0; background: #0e0e10; color: #efeff1; }
  .layout { display: grid; grid-template-columns: 1fr 320px; height: 100vh; }
  .stage  { display: flex; flex-direction: column; }
  video   { width: 100%%; flex: 1; background: #000; }
  .meta   { padding: 0.75rem 1rem; border-top: 1px solid #2a2a2d; }
  .h1     { font-size: 1.05rem; font-weight: 600; margin: 0 0 0.2rem; }
  .host   { color: #a0a0a3; font-size: 0.9rem; }
  .side   { border-left: 1px solid #2a2a2d; display: flex; flex-direction: column; }
  .tabs   { display: flex; border-bottom: 1px solid #2a2a2d; }
  .tab    { flex: 1; padding: 0.6rem; text-align: center; cursor: pointer; color: #a0a0a3; }
  .tab.active { color: #fff; border-bottom: 2px solid #9147ff; }
  .pane   { flex: 1; overflow-y: auto; padding: 0.75rem; }
  .pane.hidden { display: none; }
  .msg    { margin-bottom: 0.4rem; font-size: 0.9rem; }
  .msg .name { font-weight: 600; color: #bf94ff; margin-right: 0.4rem; }
  .composer { display: flex; padding: 0.5rem; border-top: 1px solid #2a2a2d; gap: 0.4rem; }
  .composer input { flex: 1; padding: 0.4rem; background: #18181b; color: #fff; border: 1px solid #2a2a2d; border-radius: 4px; }
  .composer button { background: #9147ff; color: #fff; border: 0; padding: 0.4rem 0.8rem; border-radius: 4px; cursor: pointer; }
  .offer  { margin: 0.5rem 0; padding: 0.7rem; background: #18181b; border-left: 3px solid #00d684; border-radius: 4px; }
  .offer .h { font-weight: 600; margin-bottom: 0.3rem; }
  .offer a { display: inline-block; margin-top: 0.4rem; padding: 0.4rem 0.8rem; background: #00d684; color: #000; border-radius: 4px; text-decoration: none; }
  .poll   { margin: 0.5rem 0; padding: 0.7rem; background: #18181b; border-left: 3px solid #f7c948; border-radius: 4px; }
  .poll .q { font-weight: 600; margin-bottom: 0.4rem; }
  .poll button { display: block; width: 100%%; margin: 0.2rem 0; padding: 0.4rem; background: #2a2a2d; color: #fff; border: 0; border-radius: 4px; cursor: pointer; text-align: left; }
  .poll button:hover { background: #3a3a3d; }
  .status { font-size: 0.8rem; color: #a0a0a3; }
</style></head>
<body>
<div class="layout">
  <div class="stage">
    <video id="player" controls autoplay playsinline></video>
    <div class="meta">
      <div class="h1">%s</div>
      <div class="host">Hosted by %s · <span id="viewers" class="status">– viewers</span></div>
    </div>
  </div>
  <div class="side">
    <div class="tabs">
      <div class="tab active" data-pane="chat">Chat</div>
      <div class="tab" data-pane="offers">Updates</div>
    </div>
    <div class="pane" id="chat-pane"></div>
    <div class="pane hidden" id="offers-pane"></div>
    <form class="composer" id="composer">
      <input id="msg" placeholder="Say something…" maxlength="500">
      <button type="submit">Send</button>
    </form>
  </div>
</div>
<script>
const PLAYBACK_URL  = %q;
const VIEWER_NAME   = %q;
const JOIN_TOKEN    = %q;
const WEBINARS_BASE = %q;
const STREAMING_BASE= %q;
const STREAM_ID     = %d;
const PLAYBACK_TOK  = %q;
const PROJECT_ID    = new URLSearchParams(location.search).get("project_id") || "";

// Player.
const video = document.getElementById("player");
if (Hls.isSupported()) {
  const hls = new Hls({ lowLatencyMode: true });
  hls.loadSource(PLAYBACK_URL);
  hls.attachMedia(video);
} else if (video.canPlayType("application/vnd.apple.mpegurl")) {
  video.src = PLAYBACK_URL;
}

// Heartbeats: streaming (anonymous capacity gauge) + webinars (per-registrant attendance).
const params = (extra) => {
  const p = new URLSearchParams({ project_id: PROJECT_ID, ...extra });
  return p.toString();
};
async function heartbeat() {
  try {
    fetch(STREAMING_BASE + "/heartbeat/" + STREAM_ID + "?" + params({ t: PLAYBACK_TOK }), { method: "POST", credentials: "include" });
    fetch(WEBINARS_BASE + "/live/" + JOIN_TOKEN + "/heartbeat?" + params({}), { method: "POST" });
  } catch (e) {}
}
heartbeat();
setInterval(heartbeat, 10000);

// Tab switching.
document.querySelectorAll(".tab").forEach(t => t.onclick = () => {
  document.querySelectorAll(".tab").forEach(x => x.classList.toggle("active", x === t));
  document.getElementById("chat-pane").classList.toggle("hidden", t.dataset.pane !== "chat");
  document.getElementById("offers-pane").classList.toggle("hidden", t.dataset.pane !== "offers");
});

// Compose.
document.getElementById("composer").onsubmit = async (e) => {
  e.preventDefault();
  const inp = document.getElementById("msg");
  const text = inp.value.trim();
  if (!text) return;
  inp.value = "";
  await fetch(WEBINARS_BASE + "/live/" + JOIN_TOKEN + "/chat?" + params({}), {
    method: "POST",
    headers: { "Content-Type": "application/json" },
    body: JSON.stringify({ body: text }),
  });
};

// Poll for events. Single endpoint returns chat + offers + polls
// since a sequence cursor.
let cursor = 0;
async function pollEvents() {
  try {
    const r = await fetch(WEBINARS_BASE + "/live/" + JOIN_TOKEN + "/events?" + params({ since: cursor }));
    if (!r.ok) return;
    const data = await r.json();
    cursor = data.cursor || cursor;
    if (data.viewers != null) document.getElementById("viewers").innerText = data.viewers + " viewers";
    for (const e of (data.events || [])) {
      if (e.kind === "chat") {
        const div = document.createElement("div");
        div.className = "msg";
        div.innerHTML = '<span class="name"></span><span class="body"></span>';
        div.querySelector(".name").innerText = e.display_name + ":";
        div.querySelector(".body").innerText = e.body;
        document.getElementById("chat-pane").appendChild(div);
        document.getElementById("chat-pane").scrollTop = 999999;
      } else if (e.kind === "offer") {
        const div = document.createElement("div");
        div.className = "offer";
        const h = document.createElement("div");
        h.className = "h";
        h.innerText = e.headline;
        const b = document.createElement("div");
        b.innerText = e.body || "";
        const a = document.createElement("a");
        a.href = "#";
        a.innerText = e.cta_label;
        a.onclick = (ev) => {
          ev.preventDefault();
          fetch(WEBINARS_BASE + "/live/" + JOIN_TOKEN + "/offer-click?" + params({ offer_id: e.id }), { method: "POST" });
          window.open(e.cta_url, "_blank");
        };
        div.appendChild(h); div.appendChild(b); div.appendChild(a);
        document.getElementById("offers-pane").appendChild(div);
      } else if (e.kind === "poll") {
        const div = document.createElement("div");
        div.className = "poll";
        const q = document.createElement("div");
        q.className = "q";
        q.innerText = e.question;
        div.appendChild(q);
        (e.choices || []).forEach((c, i) => {
          const btn = document.createElement("button");
          btn.innerText = c;
          btn.onclick = () => {
            fetch(WEBINARS_BASE + "/live/" + JOIN_TOKEN + "/poll-response?" + params({ poll_id: e.id, choice: i }), { method: "POST" });
            div.querySelectorAll("button").forEach(b => b.disabled = true);
          };
          div.appendChild(btn);
        });
        document.getElementById("offers-pane").appendChild(div);
      }
    }
  } catch (e) {}
  setTimeout(pollEvents, 2000);
}
pollEvents();
</script>
</body></html>`

// handleLiveHeartbeat — updates webinar_attendance for this registrant.
func (a *App) handleLiveHeartbeat(rw http.ResponseWriter, r *http.Request, ctx *sdk.AppCtx, webinar *Webinar, reg *Registrant) {
	if r.Method != http.MethodPost && r.Method != http.MethodGet {
		httpErr(rw, http.StatusMethodNotAllowed, "POST")
		return
	}
	source := "live"
	if webinar.Status == "ended" {
		source = "replay"
	}
	now := time.Now().UTC().Format(time.RFC3339)
	// Upsert attendance row + bump watch_seconds by 10s per heartbeat.
	// The decay worker will close `left_at` once heartbeats stop.
	if _, err := ctx.AppDB().Exec(
		`INSERT INTO webinar_attendance
			(project_id, webinar_id, registrant_id, source,
			 joined_at, last_heartbeat, watch_seconds)
		 VALUES (?, ?, ?, ?, ?, ?, 10)
		 ON CONFLICT(registrant_id, source) DO UPDATE SET
			last_heartbeat = excluded.last_heartbeat,
			watch_seconds = watch_seconds + 10`,
		webinar.ProjectID, webinar.ID, reg.ID, source, now, now); err != nil {
		httpErr(rw, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(rw, map[string]any{"ok": true})
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
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(rw, http.StatusBadRequest, "invalid json")
		return
	}
	body.Body = strings.TrimSpace(body.Body)
	if body.Body == "" {
		httpErr(rw, http.StatusBadRequest, "body required")
		return
	}
	if len(body.Body) > 500 {
		body.Body = body.Body[:500]
	}
	kind := "message"
	if body.Kind == "question" {
		kind = "question"
	}
	displayName := reg.DisplayName
	if displayName == "" {
		displayName = "Guest"
	}
	seq := globalApp.nextWebinarSequence(ctx, webinar.ID)
	res, err := ctx.AppDB().Exec(
		`INSERT INTO webinar_chat
			(project_id, webinar_id, registrant_id, display_name, body, kind, sequence)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		webinar.ProjectID, webinar.ID, reg.ID, displayName, body.Body, kind, seq)
	if err != nil {
		httpErr(rw, http.StatusInternalServerError, err.Error())
		return
	}
	id, _ := res.LastInsertId()
	httpJSON(rw, map[string]any{"id": id, "sequence": seq})
}

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
	if _, err := ctx.AppDB().Exec(
		`INSERT INTO webinar_poll_responses (poll_id, registrant_id, choice_index)
		 VALUES (?, ?, ?)
		 ON CONFLICT(poll_id, registrant_id) DO UPDATE SET
			choice_index = excluded.choice_index`,
		pollID, reg.ID, choice); err != nil {
		httpErr(rw, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(rw, map[string]any{"ok": true})
}

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
	_, _ = ctx.AppDB().Exec(
		`INSERT INTO webinar_offer_clicks (project_id, offer_id, registrant_id)
		 VALUES (?, ?, ?)`, webinar.ProjectID, offerID, reg.ID)
	httpJSON(rw, map[string]any{"ok": true})
}

// handleLiveEvents — long-poll endpoint returning chat + offers +
// polls newer than the cursor. Single response, ~2s client poll.
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
		Headline    string   `json:"headline,omitempty"`
		CTALabel    string   `json:"cta_label,omitempty"`
		CTAURL      string   `json:"cta_url,omitempty"`
		Question    string   `json:"question,omitempty"`
		Choices     []string `json:"choices,omitempty"`
	}
	events := []event{}
	maxSeq := since

	chatRows, err := ctx.AppDB().Query(
		`SELECT id, sequence, display_name, body FROM webinar_chat
		 WHERE webinar_id = ? AND sequence > ?
		 ORDER BY sequence ASC LIMIT 200`, webinar.ID, since)
	if err == nil {
		for chatRows.Next() {
			e := event{Kind: "chat"}
			_ = chatRows.Scan(&e.ID, &e.Sequence, &e.DisplayName, &e.Body)
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

	// Anonymous viewer count: read straight from streaming.
	viewers := 0
	if webinar.StreamID != 0 {
		if m, err := globalApp.streamingCaller.GetMetrics(webinar.StreamID); err == nil {
			viewers = m.CurrentViewers
		}
	}

	httpJSON(rw, map[string]any{
		"cursor":  maxSeq,
		"events":  events,
		"viewers": viewers,
	})
}

// ─── Replay (public) ──────────────────────────────────────────────

func (a *App) handleReplayPage(rw http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/replay/")
	rest = strings.Trim(rest, "/")
	if rest == "" {
		http.NotFound(rw, r)
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
		http.NotFound(rw, r)
		return
	}
	if w.ReplayToken != "" {
		if r.URL.Query().Get("t") != w.ReplayToken {
			http.NotFound(rw, r)
			return
		}
	}
	if w.ReplayExpiresAt != "" {
		if exp, err := time.Parse(time.RFC3339, w.ReplayExpiresAt); err == nil && time.Now().After(exp) {
			httpErr(rw, http.StatusGone, "replay has expired")
			return
		}
	}

	// Pull the replay URL from streaming.
	urls, err := app.streamingCaller.ReplayURL(w.StreamID)
	if err != nil || !urls.Available {
		httpErr(rw, http.StatusServiceUnavailable, "replay not available")
		return
	}
	playback := urls.HLSURL
	if playback == "" {
		playback = urls.MP4URL
	}

	rw.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(rw, replayHTML,
		html.EscapeString(w.Title),
		html.EscapeString(w.Title),
		html.EscapeString(w.HostName),
		html.EscapeString(playback))
}

const replayHTML = `<!doctype html>
<html><head><meta charset="utf-8"><title>%s — replay</title>
<meta name="viewport" content="width=device-width, initial-scale=1">
<script src="https://cdn.jsdelivr.net/npm/hls.js@1"></script>
<style>
  body { font-family: -apple-system, sans-serif; margin: 0; background: #000; color: #fff; }
  .meta { padding: 1rem; }
  video { width: 100%%; height: 80vh; background: #000; }
</style></head>
<body>
<video id="player" controls playsinline></video>
<div class="meta"><h1>%s</h1><div>Hosted by %s</div></div>
<script>
const URL = %q;
const v = document.getElementById("player");
if (URL.endsWith(".m3u8") && Hls.isSupported()) {
  const h = new Hls(); h.loadSource(URL); h.attachMedia(v);
} else { v.src = URL; }
</script>
</body></html>`

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
