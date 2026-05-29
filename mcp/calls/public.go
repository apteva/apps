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
	titleJSON, _ := json.Marshal(room.Title)
	tokenJSON, _ := json.Marshal(token)
	nameJSON, _ := json.Marshal(jt.DisplayName)
	roleJSON, _ := json.Marshal(jt.Role)
	kindJSON, _ := json.Marshal(jt.ParticipantKind)
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	page := strings.NewReplacer(
		"__TITLE__", html.EscapeString(room.Title),
		"__ROOM_STATUS__", html.EscapeString(room.Status),
		"__DISPLAY_NAME__", html.EscapeString(jt.DisplayName),
		"__TOKEN_JSON__", string(tokenJSON),
		"__TITLE_JSON__", string(titleJSON),
		"__NAME_JSON__", string(nameJSON),
		"__ROLE_JSON__", string(roleJSON),
		"__KIND_JSON__", string(kindJSON),
	).Replace(joinPageHTML)
	fmt.Fprint(w, page)
}

const joinPageHTML = `<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>__TITLE__</title>
  <style>
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
      grid-template-columns: repeat(2, minmax(0, 1fr));
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
    .tile.primary { grid-column: span 2; min-height: 300px; }
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
    .round.end { background: var(--danger); border-color: transparent; color: white; }
    .side {
      border: 1px solid var(--line);
      border-radius: 8px;
      background: rgba(23,27,36,.88);
      box-shadow: 0 24px 70px rgba(0,0,0,.24);
      overflow: hidden;
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
    @media (max-width: 860px) {
      header { padding: 0 18px; }
      main { grid-template-columns: 1fr; padding: 18px; }
      .stage { min-height: 420px; }
      .video-grid { grid-template-columns: 1fr; }
      .tile.primary { grid-column: span 1; }
    }
  </style>
</head>
<body>
  <div class="shell">
    <header>
      <div class="brand"><div class="mark">C</div><span>Calls</span></div>
      <div class="status">__ROOM_STATUS__</div>
    </header>
    <main>
      <section class="stage" aria-label="Call room">
        <div class="video-grid">
          <div class="tile primary">
            <div class="avatar" id="avatar">?</div>
            <div class="tile-label" id="tile-label">Preview</div>
          </div>
          <div class="tile">
            <div class="avatar">+</div>
            <div class="tile-label">Waiting</div>
          </div>
          <div class="tile">
            <div class="avatar">A</div>
            <div class="tile-label">Agent ready</div>
          </div>
        </div>
        <div class="controls">
          <button class="round" type="button" title="Microphone">Mic</button>
          <button class="round" type="button" title="Camera">Cam</button>
          <button class="round" type="button" title="Screen">Share</button>
          <button class="round end" type="button" title="Leave">End</button>
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
        <div class="joined" id="joined"></div>
        <div class="error" id="error"></div>
        <div class="details">
          <div class="row"><span>Kind</span><strong id="kind"></strong></div>
          <div class="row"><span>Role</span><strong id="role"></strong></div>
        </div>
      </aside>
    </main>
  </div>
  <script>
    const TOKEN = __TOKEN_JSON__;
    const ROOM_TITLE = __TITLE_JSON__;
    const DEFAULT_NAME = __NAME_JSON__;
    const ROLE = __ROLE_JSON__;
    const KIND = __KIND_JSON__;
    const base = location.pathname.split("/join/")[0];
    const nameInput = document.getElementById("display-name");
    const avatar = document.getElementById("avatar");
    const tileLabel = document.getElementById("tile-label");
    const joined = document.getElementById("joined");
    const error = document.getElementById("error");
    const button = document.getElementById("join-button");
    document.getElementById("kind").textContent = KIND;
    document.getElementById("role").textContent = ROLE;
    function syncName() {
      const name = (nameInput.value || DEFAULT_NAME || "Guest").trim();
      avatar.textContent = name.slice(0, 1).toUpperCase();
      tileLabel.textContent = name;
    }
    nameInput.addEventListener("input", syncName);
    syncName();
    document.getElementById("join-form").addEventListener("submit", async (event) => {
      event.preventDefault();
      error.style.display = "none";
      joined.style.display = "none";
      button.disabled = true;
      button.textContent = "Joining...";
      try {
        const res = await fetch(base + "/api/join", {
          method: "POST",
          credentials: "same-origin",
          headers: { "Content-Type": "application/json" },
          body: JSON.stringify({ token: TOKEN, display_name: nameInput.value })
        });
        const data = await res.json().catch(() => ({}));
        if (!res.ok) throw new Error(data.error || "Join failed");
        const p = data.participant || {};
        joined.textContent = "Joined " + ROOM_TITLE + " as " + (p.display_name || nameInput.value || "Guest") + ".";
        joined.style.display = "block";
        button.textContent = "Joined";
      } catch (e) {
        error.textContent = e.message;
        error.style.display = "block";
        button.disabled = false;
        button.textContent = "Join room";
      }
    });
  </script>
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
	out, err := a.toolGetRoom(globalCtx, map[string]any{"_project_id": pid, "id": id})
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, out)
}

func (a *App) handleAPIJoin(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	args := map[string]any{}
	ct := r.Header.Get("Content-Type")
	if strings.Contains(ct, "application/json") {
		_ = json.NewDecoder(r.Body).Decode(&args)
	} else {
		_ = r.ParseForm()
		args["token"] = r.FormValue("token")
		args["display_name"] = r.FormValue("display_name")
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
	switch parts[1] {
	case "leave":
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body == nil {
			body = map[string]any{}
		}
		body["_project_id"] = pid
		body["room_id"] = roomID
		out, err := a.toolLeaveRoom(globalCtx, body)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
	case "participants":
		out, err := a.toolListParticipants(globalCtx, map[string]any{"_project_id": pid, "room_id": roomID})
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
	case "messages":
		if r.Method == http.MethodPost {
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			if body == nil {
				body = map[string]any{}
			}
			body["_project_id"] = pid
			body["room_id"] = roomID
			out, err := a.toolSendMessage(globalCtx, body)
			if err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
			httpJSON(w, out)
			return
		}
		out, err := a.toolGetMessages(globalCtx, map[string]any{"_project_id": pid, "room_id": roomID})
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
	case "transcript":
		out, err := a.toolGetTranscript(globalCtx, map[string]any{"_project_id": pid, "room_id": roomID})
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
	case "signal":
		a.handleSignal(w, r, pid, roomID, parts)
	default:
		httpErr(w, http.StatusNotFound, "not found")
	}
}

func (a *App) handleSignal(w http.ResponseWriter, r *http.Request, pid string, roomID int64, parts []string) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	if len(parts) < 3 {
		httpErr(w, http.StatusBadRequest, "signal kind required")
		return
	}
	var body struct {
		SessionID string `json:"session_id"`
		SDP       string `json:"sdp"`
		State     string `json:"state"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	switch parts[2] {
	case "offer":
		_, err := globalCtx.AppDB().Exec(
			`UPDATE peer_sessions SET offer_sdp = ?, status='negotiating' WHERE project_id = ? AND room_id = ? AND session_id = ?`,
			body.SDP, pid, roomID, body.SessionID)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
	case "answer":
		_, err := globalCtx.AppDB().Exec(
			`UPDATE peer_sessions SET answer_sdp = ?, status='connected', connected_at = COALESCE(connected_at, CURRENT_TIMESTAMP), connection_state='connected' WHERE project_id = ? AND room_id = ? AND session_id = ?`,
			body.SDP, pid, roomID, body.SessionID)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
	case "ice":
		_, err := globalCtx.AppDB().Exec(
			`UPDATE peer_sessions SET ice_state = ? WHERE project_id = ? AND room_id = ? AND session_id = ?`,
			body.State, pid, roomID, body.SessionID)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
	default:
		httpErr(w, http.StatusNotFound, "not found")
		return
	}
	httpJSON(w, map[string]any{"ok": true})
}
