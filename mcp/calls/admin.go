package main

import (
	"encoding/json"
	"net/http"
	"strconv"
	"strings"
)

func (a *App) handleAdminRooms(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		args := map[string]any{"_project_id": pid}
		if v := r.URL.Query().Get("status"); v != "" {
			args["status"] = v
		}
		if v := r.URL.Query().Get("limit"); v != "" {
			if n, err := strconv.Atoi(v); err == nil {
				args["limit"] = n
			}
		}
		out, err := a.toolListRooms(globalCtx, args)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
	case http.MethodPost:
		var body map[string]any
		if err := requireSameOrigin(r); err != nil {
			httpErr(w, http.StatusForbidden, err.Error())
			return
		}
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&body); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		body["_project_id"] = pid
		out, err := a.toolCreateRoom(globalCtx, body)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleAdminRoomItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		if err := requireSameOrigin(r); err != nil {
			httpErr(w, http.StatusForbidden, err.Error())
			return
		}
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/admin/rooms/")
	parts := strings.Split(strings.Trim(rest, "/"), "/")
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		httpErr(w, http.StatusBadRequest, "invalid id")
		return
	}
	args := map[string]any{"_project_id": pid, "id": id}
	if len(parts) >= 2 {
		switch parts[1] {
		case "end":
			if r.Method != http.MethodPost {
				httpErr(w, http.StatusMethodNotAllowed, "POST")
				return
			}
			out, err := a.toolEndRoom(globalCtx, args)
			if err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
			httpJSON(w, out)
			return
		case "participants":
			a.handleAdminParticipants(w, r, pid, id, parts)
			return
		case "messages":
			a.handleAdminMessages(w, r, pid, id)
			return
		case "transcript":
			a.handleAdminTranscript(w, r, pid, id)
			return
		case "join-tokens":
			a.handleAdminJoinTokens(w, r, pid, id, parts)
			return
		}
	}
	switch r.Method {
	case http.MethodGet:
		out, err := a.toolGetRoom(globalCtx, args)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleAdminJoinTokens(w http.ResponseWriter, r *http.Request, pid string, roomID int64, parts []string) {
	if len(parts) == 2 && r.Method == http.MethodGet {
		items, err := a.listJoinTokens(pid, roomID)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpJSON(w, map[string]any{"join_tokens": items, "count": len(items)})
		return
	}
	if len(parts) == 2 && r.Method == http.MethodPost {
		if err := requireSameOrigin(r); err != nil {
			httpErr(w, http.StatusForbidden, err.Error())
			return
		}
		var body map[string]any
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&body); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		body["_project_id"] = pid
		body["room_id"] = roomID
		out, err := a.toolCreateJoinToken(globalCtx, body)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
		return
	}
	if len(parts) == 4 && parts[3] == "revoke" && r.Method == http.MethodPost {
		tokenID, err := strconv.ParseInt(parts[2], 10, 64)
		if err != nil || tokenID <= 0 {
			httpErr(w, http.StatusBadRequest, "invalid token id")
			return
		}
		revoked, err := revokeJoinToken(pid, roomID, tokenID)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpJSON(w, map[string]any{"revoked": revoked})
		return
	}
	httpErr(w, http.StatusMethodNotAllowed, "unsupported join-token operation")
}

func (a *App) handleAdminParticipants(w http.ResponseWriter, r *http.Request, pid string, roomID int64, parts []string) {
	if len(parts) == 2 && r.Method == http.MethodGet {
		out, err := a.toolListParticipants(globalCtx, map[string]any{
			"_project_id": pid,
			"room_id":     roomID,
			"status":      r.URL.Query().Get("status"),
			"kind":        r.URL.Query().Get("kind"),
		})
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
		return
	}
	if len(parts) < 4 {
		httpErr(w, http.StatusBadRequest, "invalid participant route")
		return
	}
	participantID, err := strconv.ParseInt(parts[2], 10, 64)
	if err != nil || participantID <= 0 {
		httpErr(w, http.StatusBadRequest, "invalid participant id")
		return
	}
	switch parts[3] {
	case "remove":
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "POST")
			return
		}
		var body map[string]any
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&body); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		if body == nil {
			body = map[string]any{}
		}
		body["_project_id"] = pid
		body["room_id"] = roomID
		body["participant_id"] = participantID
		out, err := a.toolRemoveParticipant(globalCtx, body)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
	case "update":
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "POST")
			return
		}
		var patch map[string]any
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&patch); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid json")
			return
		}
		out, err := a.toolUpdateParticipant(globalCtx, map[string]any{
			"_project_id":    pid,
			"room_id":        roomID,
			"participant_id": participantID,
			"patch":          patch,
		})
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
	default:
		httpErr(w, http.StatusNotFound, "not found")
	}
}

func (a *App) handleAdminMessages(w http.ResponseWriter, r *http.Request, pid string, roomID int64) {
	switch r.Method {
	case http.MethodGet:
		args := map[string]any{"_project_id": pid, "room_id": roomID}
		if v := r.URL.Query().Get("since_id"); v != "" {
			if n, err := strconv.ParseInt(v, 10, 64); err == nil {
				args["since_id"] = n
			}
		}
		out, err := a.toolGetMessages(globalCtx, args)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
	case http.MethodPost:
		var body map[string]any
		if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 256<<10)).Decode(&body); err != nil {
			httpErr(w, http.StatusBadRequest, "invalid json")
			return
		}
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
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleAdminTranscript(w http.ResponseWriter, r *http.Request, pid string, roomID int64) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "GET")
		return
	}
	args := map[string]any{"_project_id": pid, "room_id": roomID}
	if v := r.URL.Query().Get("since_id"); v != "" {
		if n, err := strconv.ParseInt(v, 10, 64); err == nil {
			args["since_id"] = n
		}
	}
	out, err := a.toolGetTranscript(globalCtx, args)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, out)
}
