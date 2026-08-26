package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const maxPublicJSONBody = 1 << 20

func decodePublicJSON(w http.ResponseWriter, r *http.Request, dst any) error {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxPublicJSONBody))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		return fmt.Errorf("invalid json: %w", err)
	}
	return nil
}

func requireSameOrigin(r *http.Request) error {
	origin := strings.TrimSpace(r.Header.Get("Origin"))
	if origin == "" {
		return nil
	}
	u, err := url.Parse(origin)
	if err != nil || u.Host == "" || !strings.EqualFold(u.Host, r.Host) {
		return errors.New("cross-origin request rejected")
	}
	return nil
}

func bearerToken(r *http.Request) string {
	const prefix = "Bearer "
	auth := strings.TrimSpace(r.Header.Get("Authorization"))
	if len(auth) > len(prefix) && strings.EqualFold(auth[:len(prefix)], prefix) {
		return strings.TrimSpace(auth[len(prefix):])
	}
	return strings.TrimSpace(r.Header.Get("X-Calls-Participant-Token"))
}

func (a *App) authenticateParticipant(r *http.Request, pid string, roomID int64) (*Participant, error) {
	token := bearerToken(r)
	if token == "" || len(token) > 256 {
		return nil, errors.New("participant bearer token required")
	}
	row := globalCtx.AppDB().QueryRow(
		`SELECT id, project_id, room_id, participant_key, kind, role, COALESCE(display_name,''),
		        status, capabilities, joined_at, COALESCE(left_at,''), COALESCE(last_seen_at,''),
		        muted_audio, muted_video, metadata
		   FROM participants
		  WHERE project_id=? AND room_id=? AND participant_key=? AND status='active'`, pid, roomID, hashSecret(token))
	p, err := scanParticipant(row)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, errors.New("invalid or inactive participant token")
	}
	return p, nil
}

func (a *App) handleAuthenticatedRoomAPI(w http.ResponseWriter, r *http.Request, pid string, roomID int64, parts []string) {
	p, err := a.authenticateParticipant(r, pid, roomID)
	if err != nil {
		httpErr(w, http.StatusUnauthorized, err.Error())
		return
	}
	resource := parts[1]
	switch resource {
	case "heartbeat":
		a.handleHeartbeat(w, r, pid, roomID, p)
	case "leave":
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "POST")
			return
		}
		out, err := a.toolLeaveRoom(globalCtx, map[string]any{"_project_id": pid, "room_id": roomID, "participant_id": p.ID})
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
	case "end":
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "POST")
			return
		}
		if !hasCapability(p, "room_control") {
			httpErr(w, http.StatusForbidden, "room_control capability required")
			return
		}
		out, err := a.toolEndRoom(globalCtx, map[string]any{"_project_id": pid, "id": roomID})
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
	case "participants":
		if len(parts) == 4 && parts[3] == "remove" {
			if r.Method != http.MethodPost {
				httpErr(w, http.StatusMethodNotAllowed, "POST")
				return
			}
			if !hasCapability(p, "room_control") {
				httpErr(w, http.StatusForbidden, "room_control capability required")
				return
			}
			targetID, parseErr := strconv.ParseInt(parts[2], 10, 64)
			if parseErr != nil || targetID <= 0 || targetID == p.ID {
				httpErr(w, http.StatusBadRequest, "invalid participant id")
				return
			}
			out, err := a.toolRemoveParticipant(globalCtx, map[string]any{"_project_id": pid, "room_id": roomID, "participant_id": targetID, "reason": "removed by room host"})
			if err != nil {
				httpErr(w, http.StatusBadRequest, err.Error())
				return
			}
			httpJSON(w, out)
			return
		}
		if r.Method != http.MethodGet {
			httpErr(w, http.StatusMethodNotAllowed, "GET")
			return
		}
		participants, err := a.dbListParticipants(globalCtx, pid, roomID, "active", "")
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		publicParticipants := make([]map[string]any, 0, len(participants))
		for _, item := range participants {
			publicParticipants = append(publicParticipants, map[string]any{
				"id": item.ID, "kind": item.Kind, "role": item.Role,
				"display_name": item.DisplayName, "status": item.Status,
				"muted_audio": item.MutedAudio, "muted_video": item.MutedVideo,
			})
		}
		httpJSON(w, map[string]any{"participants": publicParticipants, "count": len(publicParticipants)})
	case "messages":
		a.handleParticipantMessages(w, r, pid, roomID, p)
	case "transcript":
		a.handleParticipantTranscript(w, r, pid, roomID, p)
	case "signal":
		a.handleParticipantSignal(w, r, pid, roomID, p, parts)
	default:
		httpErr(w, http.StatusNotFound, "not found")
	}
}

func (a *App) handleHeartbeat(w http.ResponseWriter, r *http.Request, pid string, roomID int64, p *Participant) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST")
		return
	}
	var body struct {
		ConnectionState string `json:"connection_state"`
		MutedAudio      *bool  `json:"muted_audio"`
		MutedVideo      *bool  `json:"muted_video"`
		Tracks          []struct {
			ID      string `json:"id"`
			Kind    string `json:"kind"`
			Source  string `json:"source"`
			Label   string `json:"label"`
			Enabled bool   `json:"enabled"`
		} `json:"tracks"`
	}
	if r.ContentLength != 0 {
		if err := decodePublicJSON(w, r, &body); err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
	}
	allowedStates := map[string]bool{"": true, "new": true, "connecting": true, "connected": true, "disconnected": true, "failed": true, "closed": true}
	if !allowedStates[body.ConnectionState] {
		httpErr(w, http.StatusBadRequest, "invalid connection_state")
		return
	}
	if len(body.Tracks) > 8 {
		httpErr(w, http.StatusBadRequest, "too many media tracks")
		return
	}
	tx, err := globalCtx.AppDB().Begin()
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer tx.Rollback()
	sets := []string{"last_seen_at=CURRENT_TIMESTAMP"}
	args := []any{}
	if body.MutedAudio != nil {
		sets = append(sets, "muted_audio=?")
		args = append(args, boolToInt(*body.MutedAudio))
	}
	if body.MutedVideo != nil {
		sets = append(sets, "muted_video=?")
		args = append(args, boolToInt(*body.MutedVideo))
	}
	args = append(args, p.ID, roomID, pid)
	if _, err := tx.Exec(`UPDATE participants SET `+strings.Join(sets, ",")+` WHERE id=? AND room_id=? AND project_id=? AND status='active'`, args...); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if body.ConnectionState != "" {
		status := "negotiating"
		if body.ConnectionState == "connected" {
			status = "connected"
		} else if body.ConnectionState == "closed" || body.ConnectionState == "failed" {
			status = "closed"
		}
		if _, err := tx.Exec(`UPDATE peer_sessions SET connection_state=?, status=?, connected_at=CASE WHEN ?='connected' THEN COALESCE(connected_at,CURRENT_TIMESTAMP) ELSE connected_at END, closed_at=CASE WHEN ? IN ('closed','failed') THEN COALESCE(closed_at,CURRENT_TIMESTAMP) ELSE closed_at END WHERE participant_id=? AND room_id=? AND project_id=?`, body.ConnectionState, status, body.ConnectionState, body.ConnectionState, p.ID, roomID, pid); err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if _, err := tx.Exec(`UPDATE media_tracks SET status='ended', ended_at=COALESCE(ended_at,CURRENT_TIMESTAMP) WHERE participant_id=? AND room_id=? AND project_id=? AND status='live'`, p.ID, roomID, pid); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	for _, track := range body.Tracks {
		kind, err := validateTrackKind(track.Kind)
		if err != nil || track.ID == "" || len(track.ID) > 160 || len(track.Label) > 200 || len(track.Source) > 40 {
			httpErr(w, http.StatusBadRequest, "invalid media track")
			return
		}
		if kind == "audio" && !hasCapability(p, "audio") || kind == "video" && !hasCapability(p, "video") || kind == "screen" && !hasCapability(p, "screen") {
			httpErr(w, http.StatusForbidden, kind+" capability required")
			return
		}
		status := "ended"
		if track.Enabled {
			status = "live"
		}
		if _, err := tx.Exec(
			`INSERT INTO media_tracks(project_id,room_id,participant_id,track_id,kind,source,label,status,started_at,ended_at)
			 VALUES(?,?,?,?,?,?,?,?,CURRENT_TIMESTAMP,CASE WHEN ?='ended' THEN CURRENT_TIMESTAMP ELSE NULL END)
			 ON CONFLICT(participant_id,track_id) DO UPDATE SET kind=excluded.kind,source=excluded.source,label=excluded.label,status=excluded.status,ended_at=CASE WHEN excluded.status='ended' THEN CURRENT_TIMESTAMP ELSE NULL END`,
			pid, roomID, p.ID, track.ID, kind, nullStr(track.Source), nullStr(track.Label), status, status); err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
	}
	if _, err := tx.Exec(`UPDATE rooms SET last_activity_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=?`, roomID, pid); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := tx.Commit(); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	var roomStatus string
	_ = globalCtx.AppDB().QueryRow(`SELECT status FROM rooms WHERE id=? AND project_id=?`, roomID, pid).Scan(&roomStatus)
	httpJSON(w, map[string]any{"ok": true, "room_status": roomStatus, "server_time": time.Now().UTC().Format(time.RFC3339)})
}

func (a *App) handleParticipantMessages(w http.ResponseWriter, r *http.Request, pid string, roomID int64, p *Participant) {
	if !hasCapability(p, "chat") {
		httpErr(w, http.StatusForbidden, "chat capability required")
		return
	}
	if r.Method == http.MethodPost {
		var body struct {
			Body       string `json:"body"`
			Kind       string `json:"kind"`
			Visibility string `json:"visibility"`
		}
		if err := decodePublicJSON(w, r, &body); err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		if body.Visibility == "internal" && p.Kind == "human" && p.Role != "host" {
			httpErr(w, http.StatusForbidden, "internal messages require host or service identity")
			return
		}
		out, err := a.toolSendMessage(globalCtx, map[string]any{
			"_project_id": pid, "room_id": roomID, "participant_id": p.ID,
			"body": body.Body, "kind": body.Kind, "visibility": body.Visibility,
		})
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		httpJSON(w, out)
		return
	}
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "GET or POST")
		return
	}
	sinceID, _ := strconv.ParseInt(r.URL.Query().Get("since_id"), 10, 64)
	limit := 100
	if n, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && n > 0 && n <= 200 {
		limit = n
	}
	rows, err := globalCtx.AppDB().Query(
		`SELECT id, project_id, room_id, COALESCE(participant_id,0), kind, visibility, body, created_at
		   FROM room_messages
		  WHERE project_id=? AND room_id=? AND id>?
		    AND (visibility='room' OR participant_id=? OR (visibility='internal' AND (?<>'human' OR ?='host')))
		  ORDER BY id ASC LIMIT ?`, pid, roomID, sinceID, p.ID, p.Kind, p.Role, limit)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer rows.Close()
	items := []*Message{}
	for rows.Next() {
		item := &Message{}
		if err := rows.Scan(&item.ID, &item.ProjectID, &item.RoomID, &item.ParticipantID, &item.Kind, &item.Visibility, &item.Body, &item.CreatedAt); err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"messages": items, "count": len(items)})
}

func (a *App) handleParticipantTranscript(w http.ResponseWriter, r *http.Request, pid string, roomID int64, p *Participant) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "GET")
		return
	}
	if !hasCapability(p, "transcript_read") {
		httpErr(w, http.StatusForbidden, "transcript_read capability required")
		return
	}
	args := map[string]any{"_project_id": pid, "room_id": roomID, "since_id": r.URL.Query().Get("since_id"), "latest": false, "limit": 200}
	out, err := a.toolGetTranscript(globalCtx, args)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, out)
}

func (a *App) handleParticipantSignal(w http.ResponseWriter, r *http.Request, pid string, roomID int64, p *Participant, parts []string) {
	if !hasCapability(p, "audio") && !hasCapability(p, "video") && !hasCapability(p, "screen") {
		httpErr(w, http.StatusForbidden, "media capability required")
		return
	}
	if r.Method == http.MethodGet {
		sinceID, _ := strconv.ParseInt(r.URL.Query().Get("since_id"), 10, 64)
		rows, err := globalCtx.AppDB().Query(
			`SELECT id, room_id, from_participant_id, to_participant_id, kind, payload, created_at
			   FROM signaling_messages WHERE project_id=? AND room_id=? AND to_participant_id=? AND id>? ORDER BY id ASC LIMIT 200`,
			pid, roomID, p.ID, sinceID)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		defer rows.Close()
		items := []*SignalMessage{}
		for rows.Next() {
			item := &SignalMessage{}
			var raw string
			if err := rows.Scan(&item.ID, &item.RoomID, &item.FromParticipantID, &item.ToParticipantID, &item.Kind, &raw, &item.CreatedAt); err != nil {
				httpErr(w, http.StatusInternalServerError, err.Error())
				return
			}
			item.Payload = json.RawMessage(raw)
			items = append(items, item)
		}
		httpJSON(w, map[string]any{"signals": items, "count": len(items)})
		return
	}
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "GET or POST")
		return
	}
	if len(parts) < 3 {
		httpErr(w, http.StatusBadRequest, "signal kind required")
		return
	}
	kind := parts[2]
	if kind != "offer" && kind != "answer" && kind != "ice" {
		httpErr(w, http.StatusBadRequest, "signal kind must be offer, answer, or ice")
		return
	}
	var body struct {
		ToParticipantID int64           `json:"to_participant_id"`
		Payload         json.RawMessage `json:"payload"`
	}
	if err := decodePublicJSON(w, r, &body); err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if body.ToParticipantID <= 0 || body.ToParticipantID == p.ID || len(body.Payload) == 0 || len(body.Payload) > 512<<10 || !json.Valid(body.Payload) {
		httpErr(w, http.StatusBadRequest, "invalid signaling payload")
		return
	}
	if err := validateSignalCapabilities(p, kind, body.Payload); err != nil {
		httpErr(w, http.StatusForbidden, err.Error())
		return
	}
	var targetStatus string
	if err := globalCtx.AppDB().QueryRow(`SELECT status FROM participants WHERE id=? AND room_id=? AND project_id=?`, body.ToParticipantID, roomID, pid).Scan(&targetStatus); err != nil || targetStatus != "active" {
		httpErr(w, http.StatusNotFound, "target participant unavailable")
		return
	}
	tx, err := globalCtx.AppDB().Begin()
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	defer tx.Rollback()
	res, err := tx.Exec(
		`INSERT INTO signaling_messages(project_id,room_id,from_participant_id,to_participant_id,kind,payload) VALUES(?,?,?,?,?,?)`,
		pid, roomID, p.ID, body.ToParticipantID, kind, string(body.Payload))
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	id, _ := res.LastInsertId()
	connectedChanged := false
	if kind == "answer" {
		updated, err := tx.Exec(`UPDATE peer_sessions SET status='connected', connection_state='connected', connected_at=COALESCE(connected_at,CURRENT_TIMESTAMP) WHERE participant_id=? AND room_id=? AND project_id=? AND status<>'connected'`, p.ID, roomID, pid)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		n, _ := updated.RowsAffected()
		connectedChanged = n > 0
	}
	if _, err := tx.Exec(`UPDATE rooms SET last_activity_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=?`, roomID, pid); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := tx.Commit(); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if connectedChanged {
		a.emit(globalCtx, pid, "peer.connected", map[string]any{"room_id": roomID, "participant_id": p.ID})
	}
	httpJSON(w, map[string]any{"ok": true, "signal_id": id})
}

func validateSignalCapabilities(p *Participant, kind string, payload json.RawMessage) error {
	if kind == "ice" {
		if len(payload) > 16<<10 {
			return errors.New("ICE candidate is too large")
		}
		return nil
	}
	var description struct {
		Type string `json:"type"`
		SDP  string `json:"sdp"`
	}
	if err := json.Unmarshal(payload, &description); err != nil || description.SDP == "" {
		return errors.New("invalid session description")
	}
	if len(description.SDP) > 512<<10 {
		return errors.New("session description is too large")
	}
	if strings.Contains(description.SDP, "m=audio") && !hasCapability(p, "audio") {
		return errors.New("audio capability required")
	}
	if strings.Contains(description.SDP, "m=video") && !hasCapability(p, "video") && !hasCapability(p, "screen") {
		return errors.New("video or screen capability required")
	}
	return nil
}

func (a *App) listJoinTokens(pid string, roomID int64) ([]*JoinToken, error) {
	rows, err := globalCtx.AppDB().Query(
		`SELECT id, project_id, room_id, '', participant_kind, role, COALESCE(display_name,''), capabilities,
		        COALESCE(expires_at,''), COALESCE(max_uses,0), uses, created_at, COALESCE(revoked_at,'')
		   FROM join_tokens WHERE project_id=? AND room_id=? ORDER BY created_at DESC LIMIT 200`, pid, roomID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*JoinToken{}
	for rows.Next() {
		item, err := scanJoinToken(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, item)
	}
	return out, rows.Err()
}

func revokeJoinToken(pid string, roomID, tokenID int64) (bool, error) {
	res, err := globalCtx.AppDB().Exec(`UPDATE join_tokens SET revoked_at=CURRENT_TIMESTAMP WHERE id=? AND room_id=? AND project_id=? AND revoked_at IS NULL`, tokenID, roomID, pid)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n == 1, nil
}
