package main

import (
	"encoding/json"
	"fmt"
	"html"
	"net/http"
	"strconv"
	"strings"
)

func (a *App) handleJoinPage(w http.ResponseWriter, r *http.Request) {
	token := strings.Trim(strings.TrimPrefix(r.URL.Path, "/join/"), "/")
	if token == "" {
		httpErr(w, http.StatusNotFound, "missing token")
		return
	}
	jt, err := a.dbGetJoinToken(globalCtx, token)
	if err != nil || jt == nil {
		httpErr(w, http.StatusNotFound, "join token not found")
		return
	}
	room, err := a.dbGetRoom(globalCtx, jt.ProjectID, jt.RoomID)
	if err != nil || room == nil {
		httpErr(w, http.StatusNotFound, "room not found")
		return
	}
	config, err := json.Marshal(map[string]any{
		"roomId": room.ID, "title": room.Title, "roomStatus": room.Status, "capabilities": capabilities(jt.Capabilities),
	})
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "could not prepare room")
		return
	}
	nonce := randomToken()
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Permissions-Policy", "camera=(self), microphone=(self), display-capture=(self)")
	w.Header().Set("Content-Security-Policy", "default-src 'none'; script-src 'nonce-"+nonce+"'; style-src 'nonce-"+nonce+"'; connect-src 'self'; media-src 'self' blob:; img-src 'self' data: blob:; base-uri 'none'; form-action 'self'; frame-ancestors 'none'")
	page := strings.NewReplacer(
		"__TITLE__", html.EscapeString(room.Title),
		"__ROOM_STATUS__", html.EscapeString(room.Status),
		"__DISPLAY_NAME__", html.EscapeString(jt.DisplayName),
		"__ROLE__", html.EscapeString(jt.Role),
		"__KIND__", html.EscapeString(jt.ParticipantKind),
		"__CONFIG__", string(config),
		"__GUEST_SCRIPT__", string(guestScript),
		"__NONCE__", nonce,
	).Replace(joinPageHTML)
	fmt.Fprint(w, page)
}

const joinPageHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>__TITLE__</title>
  <style nonce="__NONCE__">
    :root {
      color-scheme: dark;
      --bg: #0f1117;
      --panel: #171b24;
      --panel-2: #202635;
      --text: #f6f7fb;
      --muted: #9aa4b8;
      --line: rgba(255,255,255,.12);
      --accent: #5b8cff;
      --accent-2: #7ad7c4;
      --danger: #ff6b6b;
      font-family: Inter, ui-sans-serif, system-ui, -apple-system, BlinkMacSystemFont, "Segoe UI", sans-serif;
    }
    * { box-sizing: border-box; }
    body {
      margin: 0;
      min-height: 100vh;
      background:
        radial-gradient(circle at 20% 20%, rgba(91,140,255,.20), transparent 30%),
        radial-gradient(circle at 85% 10%, rgba(122,215,196,.16), transparent 28%),
        var(--bg);
      color: var(--text);
    }
    .shell {
      min-height: 100vh;
      display: grid;
      grid-template-rows: auto 1fr;
    }
    header {
      height: 64px;
      display: flex;
      align-items: center;
      justify-content: space-between;
      padding: 0 28px;
      border-bottom: 1px solid var(--line);
      background: rgba(15,17,23,.72);
      backdrop-filter: blur(18px);
    }
    .brand { display: flex; align-items: center; gap: 10px; font-weight: 700; }
    .mark {
      width: 30px;
      height: 30px;
      border-radius: 8px;
      display: grid;
      place-items: center;
      background: linear-gradient(135deg, var(--accent), var(--accent-2));
      color: #071018;
      font-size: 14px;
    }
    .status {
      color: var(--muted);
      font-size: 13px;
      padding: 6px 10px;
      border: 1px solid var(--line);
      border-radius: 999px;
      background: rgba(255,255,255,.04);
    }
    main {
      width: min(1180px, 100%);
      margin: 0 auto;
      padding: 28px;
      display: grid;
      grid-template-columns: minmax(0, 1fr) 360px;
      gap: 24px;
      align-items: stretch;
    }
    .stage {
      min-height: 560px;
      border: 1px solid var(--line);
      border-radius: 8px;
      overflow: hidden;
      background: #090b10;
      display: grid;
      grid-template-rows: 1fr auto;
      box-shadow: 0 24px 70px rgba(0,0,0,.34);
    }
    .video-grid {
      padding: 18px;
      display: grid;
      grid-template-columns: repeat(auto-fit, minmax(240px, 1fr));
      gap: 14px;
    }
    .tile {
      min-height: 220px;
      border-radius: 8px;
      border: 1px solid rgba(255,255,255,.10);
      background:
        linear-gradient(145deg, rgba(255,255,255,.08), rgba(255,255,255,.02)),
        #151925;
      display: grid;
      place-items: center;
      position: relative;
      overflow: hidden;
    }
    .tile.primary { min-height: 320px; }
    .tile video {
      display: none;
      width: 100%;
      height: 100%;
      object-fit: cover;
    }
    .tile.video-on video { display: block; }
    .tile.video-on .avatar { display: none; }
    .avatar {
      width: 88px;
      height: 88px;
      border-radius: 999px;
      display: grid;
      place-items: center;
      font-weight: 800;
      font-size: 32px;
      color: #061018;
      background: linear-gradient(135deg, var(--accent), var(--accent-2));
    }
    .tile-label {
      position: absolute;
      left: 12px;
      bottom: 12px;
      font-size: 12px;
      color: var(--text);
      background: rgba(0,0,0,.46);
      border: 1px solid rgba(255,255,255,.10);
      padding: 5px 8px;
      border-radius: 6px;
    }
    .controls {
      border-top: 1px solid var(--line);
      padding: 14px;
      display: flex;
      align-items: center;
      justify-content: center;
      gap: 10px;
      background: rgba(255,255,255,.03);
    }
    .round {
      width: 42px;
      height: 42px;
      border: 1px solid var(--line);
      border-radius: 999px;
      color: var(--text);
      background: var(--panel-2);
      font-size: 15px;
    }
    .round.active { border-color: rgba(122,215,196,.65); background: rgba(122,215,196,.16); }
    .round:disabled { opacity: .45; }
    .round.end { background: var(--danger); border-color: transparent; color: white; }
    .side {
      border: 1px solid var(--line);
      border-radius: 8px;
      background: rgba(23,27,36,.88);
      box-shadow: 0 24px 70px rgba(0,0,0,.24);
      overflow: hidden;
      display: grid;
      grid-template-rows: auto auto auto auto minmax(180px, 1fr);
    }
    .side-head { padding: 18px; border-bottom: 1px solid var(--line); }
    h1 { margin: 0; font-size: 24px; line-height: 1.2; letter-spacing: 0; }
    .meta { margin-top: 8px; color: var(--muted); font-size: 13px; }
    form { padding: 18px; display: grid; gap: 14px; }
    label { display: grid; gap: 7px; color: var(--muted); font-size: 12px; }
    input {
      width: 100%;
      border: 1px solid var(--line);
      border-radius: 8px;
      background: rgba(255,255,255,.06);
      color: var(--text);
      padding: 11px 12px;
      font: inherit;
      outline: none;
    }
    input:focus { border-color: rgba(91,140,255,.8); box-shadow: 0 0 0 3px rgba(91,140,255,.18); }
    .join {
      border: 0;
      border-radius: 8px;
      padding: 12px 14px;
      background: var(--accent);
      color: white;
      font-weight: 700;
      font: inherit;
    }
    .join:disabled { opacity: .55; }
    .joined {
      display: none;
      margin: 0 18px 18px;
      border: 1px solid rgba(122,215,196,.28);
      background: rgba(122,215,196,.08);
      color: var(--text);
      border-radius: 8px;
      padding: 12px;
      font-size: 13px;
    }
    .error {
      display: none;
      margin: 0 18px 18px;
      border: 1px solid rgba(255,107,107,.30);
      background: rgba(255,107,107,.08);
      color: #ffd7d7;
      border-radius: 8px;
      padding: 12px;
      font-size: 13px;
    }
    .details {
      padding: 0 18px 18px;
      display: grid;
      gap: 8px;
      color: var(--muted);
      font-size: 13px;
    }
    .row { display: flex; justify-content: space-between; gap: 16px; }
    .row strong { color: var(--text); font-weight: 600; }
    .chat {
      display: none;
      border-top: 1px solid var(--line);
      min-height: 0;
      grid-template-rows: 1fr auto;
    }
    .chat.open { display: grid; }
    .messages {
      min-height: 0;
      overflow-y: auto;
      padding: 14px 18px;
      display: flex;
      flex-direction: column;
      gap: 10px;
    }
    .message {
      border: 1px solid var(--line);
      border-radius: 8px;
      padding: 9px 10px;
      background: rgba(255,255,255,.04);
    }
    .message .by { color: var(--muted); font-size: 11px; margin-bottom: 4px; }
    .message .body { color: var(--text); font-size: 13px; line-height: 1.4; white-space: pre-wrap; }
    .chat-form {
      padding: 12px 18px 18px;
      display: grid;
      grid-template-columns: minmax(0, 1fr) auto;
      gap: 8px;
    }
    .chat-form input { min-width: 0; }
    @media (max-width: 860px) {
      header { padding: 0 18px; }
      main { grid-template-columns: 1fr; padding: 18px; }
      .stage { min-height: 420px; }
      .tile.primary { min-height: 340px; }
    }
  </style>
</head>
<body>
  <div class="shell">
    <header>
      <div class="brand"><div class="mark">C</div><span>Calls</span></div>
      <div class="status" id="room-status" role="status" aria-live="polite">__ROOM_STATUS__</div>
    </header>
    <main>
      <section class="stage" aria-label="Call room">
        <div class="video-grid">
          <div class="tile primary" id="local-tile">
            <video id="local-video" autoplay playsinline muted></video>
            <div class="avatar" id="avatar">?</div>
            <div class="tile-label" id="tile-label">Preview</div>
          </div>
        </div>
        <div class="controls">
          <button class="round" id="mic-button" type="button" title="Microphone" aria-pressed="false">Mic</button>
          <button class="round" id="cam-button" type="button" title="Camera" aria-pressed="false">Cam</button>
          <button class="round" id="share-button" type="button" title="Screen" aria-pressed="false">Share</button>
          <button class="round end" id="leave-button" type="button" title="Leave">End</button>
        </div>
      </section>
      <aside class="side">
        <div class="side-head">
          <h1>__TITLE__</h1>
          <div class="meta">Join this room as a participant.</div>
        </div>
        <form id="join-form">
          <label>
            Display name
            <input id="display-name" name="display_name" autocomplete="name" value="__DISPLAY_NAME__">
          </label>
          <button class="join" id="join-button" type="submit">Join room</button>
        </form>
        <div class="joined" id="joined" role="status" aria-live="polite"></div>
        <div class="error" id="error" role="alert"></div>
        <div class="details">
          <div class="row"><span>Kind</span><strong id="kind">__KIND__</strong></div>
          <div class="row"><span>Role</span><strong id="role">__ROLE__</strong></div>
          <div class="row"><span>Participants</span><strong id="participant-count">0</strong></div>
        </div>
        <section class="chat" id="chat">
          <div class="messages" id="messages" aria-live="polite"></div>
          <form class="chat-form" id="chat-form">
            <input id="message-input" autocomplete="off" placeholder="Message the room">
            <button class="join" id="send-button" type="submit">Send</button>
          </form>
        </section>
      </aside>
    </main>
  </div>
  <script id="calls-config" type="application/json" nonce="__NONCE__">__CONFIG__</script>
  <script nonce="__NONCE__">__GUEST_SCRIPT__</script>
</body>
</html>`

func (a *App) handleRoomPage(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	slug := strings.Trim(strings.TrimPrefix(r.URL.Path, "/room/"), "/")
	if slug == "" {
		httpErr(w, http.StatusNotFound, "missing slug")
		return
	}
	var id int64
	if err := globalCtx.AppDB().QueryRow(`SELECT id FROM rooms WHERE project_id = ? AND slug = ?`, pid, slug).Scan(&id); err != nil {
		httpErr(w, http.StatusNotFound, "room not found")
		return
	}
	room, err := a.dbGetRoom(globalCtx, pid, id)
	if err != nil || room == nil {
		httpErr(w, http.StatusNotFound, "room not found")
		return
	}
	// Public room discovery deliberately exposes metadata only. Participant
	// rosters and content require a participant bearer token.
	httpJSON(w, map[string]any{"room": map[string]any{"id": room.ID, "slug": room.Slug, "title": room.Title, "status": room.Status}})
}

func (a *App) handleAPIJoin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	if !a.allowJoinOrigin(r) {
		httpErr(w, http.StatusForbidden, "cross-origin request rejected")
		return
	}
	args := map[string]any{}
	ct := r.Header.Get("Content-Type")
	if strings.Contains(ct, "application/json") {
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&args); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid json")
			return
		}
	} else {
		_ = r.ParseForm()
		args["token"] = r.FormValue("token")
		args["display_name"] = r.FormValue("display_name")
	}
	// The token owns the room, but a caller that explicitly selected a project
	// must not consume an invite from a different project.
	delete(args, "_project_id")
	if r.Header.Get("X-Apteva-Project-ID") != "" || r.URL.Query().Get("project_id") != "" {
		pid, err := resolveProjectFromRequest(r)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		args["_project_id"] = pid
	}
	out, err := a.toolJoinRoom(globalCtx, args)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, out)
}

func (a *App) handleAPIRooms(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/rooms/"), "/")
	parts := strings.Split(rest, "/")
	if len(parts) < 2 {
		httpErr(w, http.StatusNotFound, "not found")
		return
	}
	roomID, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || roomID <= 0 {
		httpErr(w, http.StatusBadRequest, "invalid room id")
		return
	}
	a.handleAuthenticatedRoomAPI(w, r, pid, roomID, parts)
}
