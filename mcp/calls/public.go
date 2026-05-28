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
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	fmt.Fprintf(w, `<!doctype html>
<html><head><meta charset="utf-8"><title>%s</title></head>
<body>
<main>
  <h1>%s</h1>
  <form method="post" action="/api/join">
    <input type="hidden" name="token" value="%s">
    <label>Name <input name="display_name" value="%s"></label>
    <button type="submit">Join</button>
  </form>
</main>
</body></html>`, html.EscapeString(room.Title), html.EscapeString(room.Title), html.EscapeString(token), html.EscapeString(jt.DisplayName))
}

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
