package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) toolCreateRoom(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	title := strArg(args, "title")
	if title == "" {
		return nil, errors.New("title required")
	}
	if err := requireLength("title", title, 200); err != nil {
		return nil, err
	}
	meta, err := metadataArg(args, "metadata")
	if err != nil {
		return nil, err
	}
	if len(meta) > 64<<10 {
		return nil, errors.New("metadata exceeds 65536 bytes")
	}
	if err := validateJSONObject(meta, "metadata"); err != nil {
		return nil, err
	}
	baseSlug := slugify(strArg(args, "slug"))
	if strArg(args, "slug") == "" {
		baseSlug = slugify(title)
	}
	if err := requireLength("slug", baseSlug, 120); err != nil {
		return nil, err
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	maxRooms := configInt(ctx, "max_rooms", 50)
	var roomCount int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM rooms WHERE project_id = ? AND status = 'open'`, pid).Scan(&roomCount); err != nil {
		return nil, err
	}
	if roomCount >= maxRooms {
		return nil, fmt.Errorf("at max_rooms=%d open rooms", maxRooms)
	}
	slug, err := uniqueSlugTx(tx, pid, baseSlug)
	if err != nil {
		return nil, err
	}
	res, err := tx.Exec(
		`INSERT INTO rooms (project_id, slug, title, status, started_at, last_activity_at, metadata)
		 VALUES (?, ?, ?, 'open', CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, ?)`, pid, slug, title, meta)
	if err != nil {
		return nil, fmt.Errorf("insert room: %w", err)
	}
	id, _ := res.LastInsertId()
	rawToken := randomToken()
	tokenHash := hashSecret(rawToken)
	expiresAt := time.Now().UTC().Add(time.Duration(configInt(ctx, "join_token_ttl_seconds", 86400)) * time.Second).Format(time.RFC3339)
	caps := `{"audio":true,"video":true,"screen":true,"chat":true,"transcript_read":true,"transcript_write":true,"room_control":true}`
	res, err = tx.Exec(
		`INSERT INTO join_tokens
		 (project_id, room_id, token, token_hash, participant_kind, role, display_name, capabilities, expires_at, max_uses)
		 VALUES (?, ?, ?, ?, 'human', 'host', 'Host', ?, ?, 1)`,
		pid, id, "sha256:"+tokenHash, tokenHash, caps, expiresAt)
	if err != nil {
		return nil, fmt.Errorf("insert host token: %w", err)
	}
	tokenID, _ := res.LastInsertId()
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	room, err := a.dbGetRoom(ctx, pid, id)
	if err != nil {
		return nil, err
	}
	jt, err := a.dbGetJoinTokenByID(ctx, pid, tokenID)
	if err != nil {
		return nil, err
	}
	jt.Token = rawToken
	jt.JoinURL = a.joinURL(ctx, rawToken, pid)
	a.emit(ctx, pid, "room.created", map[string]any{"room_id": id})
	return map[string]any{"room": room, "host_join_token": jt, "host_join_url": jt.JoinURL}, nil
}

func (a *App) toolGetRoom(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	room, err := a.dbGetRoom(ctx, pid, id)
	if err != nil {
		return nil, err
	}
	if room == nil {
		return map[string]any{"room": nil, "found": false}, nil
	}
	participants, err := a.dbListParticipants(ctx, pid, id, "", "")
	if err != nil {
		return nil, err
	}
	tracks, err := a.dbListTracks(ctx, pid, id)
	if err != nil {
		return nil, err
	}
	active := 0
	for _, p := range participants {
		if p.Status == "active" || p.Status == "joining" {
			active++
		}
	}
	return map[string]any{"room": room, "found": true, "participants": participants, "tracks": tracks, "active_participant_count": active}, nil
}

func (a *App) toolListRooms(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	limit := intArg(args, "limit", 50)
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	where := []string{"project_id = ?"}
	qargs := []any{pid}
	if status := strArg(args, "status"); status != "" {
		where = append(where, "status = ?")
		qargs = append(qargs, status)
	}
	qargs = append(qargs, limit)
	rows, err := ctx.AppDB().Query(
		`SELECT id, project_id, slug, title, status, COALESCE(created_by,''), created_at,
		        COALESCE(started_at,''), COALESCE(ended_at,''), metadata, COALESCE(last_activity_at,'')
		   FROM rooms WHERE `+strings.Join(where, " AND ")+` ORDER BY created_at DESC LIMIT ?`,
		qargs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rooms := []*Room{}
	for rows.Next() {
		room := &Room{}
		if err := scanRoomRow(rows, room); err != nil {
			return nil, err
		}
		rooms = append(rooms, room)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{"rooms": rooms, "count": len(rooms)}, nil
}

func (a *App) toolEndRoom(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	room, err := a.dbGetRoom(ctx, pid, id)
	if err != nil {
		return nil, err
	}
	if room == nil {
		return nil, errors.New("room not found")
	}
	if room.Status != "ended" {
		tx, err := ctx.AppDB().Begin()
		if err != nil {
			return nil, err
		}
		defer tx.Rollback()
		ended, err := tx.Exec(`UPDATE rooms SET status='ended', ended_at=CURRENT_TIMESTAMP, last_activity_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=? AND status<>'ended'`, id, pid)
		if err != nil {
			return nil, err
		}
		changed, _ := ended.RowsAffected()
		if changed == 1 {
			if _, err := tx.Exec(`UPDATE peer_sessions SET status='closed', closed_at=CURRENT_TIMESTAMP, connection_state='closed' WHERE room_id=? AND project_id=? AND status IN ('negotiating','connected')`, id, pid); err != nil {
				return nil, err
			}
			if _, err := tx.Exec(`UPDATE media_tracks SET status='ended', ended_at=CURRENT_TIMESTAMP WHERE room_id=? AND project_id=? AND status='live'`, id, pid); err != nil {
				return nil, err
			}
			if _, err := tx.Exec(`UPDATE participants SET status='left', left_at=COALESCE(left_at,CURRENT_TIMESTAMP) WHERE room_id=? AND project_id=? AND status IN ('joining','active')`, id, pid); err != nil {
				return nil, err
			}
		}
		if err := tx.Commit(); err != nil {
			return nil, err
		}
		if changed == 1 {
			a.emit(ctx, pid, "room.ended", map[string]any{"room_id": id})
		}
	}
	room, err = a.dbGetRoom(ctx, pid, id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"room": room}, nil
}

func (a *App) toolCreateJoinToken(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	roomID := int64Arg(args, "room_id")
	if roomID == 0 {
		return nil, errors.New("room_id required")
	}
	room, err := a.dbGetRoom(ctx, pid, roomID)
	if err != nil {
		return nil, err
	}
	if room == nil {
		return nil, errors.New("room not found")
	}
	if room.Status == "ended" {
		return nil, errors.New("room ended")
	}
	caps, err := metadataArg(args, "capabilities")
	if err != nil {
		return nil, err
	}
	if len(caps) > 16<<10 {
		return nil, errors.New("capabilities exceeds 16384 bytes")
	}
	rawKind := strArg(args, "participant_kind")
	rawRole := strArg(args, "role")
	if rawKind != "" && rawKind != "human" && rawKind != "agent" && rawKind != "service" {
		return nil, errors.New("participant_kind must be human, agent, or service")
	}
	if rawRole != "" && rawRole != "host" && rawRole != "guest" && rawRole != "observer" {
		return nil, errors.New("role must be host, guest, or observer")
	}
	kind := validateKind(rawKind)
	role := validateRole(rawRole)
	if _, exists := args["capabilities"]; !exists {
		caps = defaultCapabilities(kind, role)
	}
	if err := validateCapabilitiesJSON(caps); err != nil {
		return nil, err
	}
	displayName := strArg(args, "display_name")
	if err := requireLength("display_name", displayName, 120); err != nil {
		return nil, err
	}
	maxUses := intArg(args, "max_uses", 1)
	if maxUses <= 0 || maxUses > 1000 {
		return nil, errors.New("max_uses must be between 1 and 1000")
	}
	expiresAt := strArg(args, "expires_at")
	if expiresAt == "" {
		expiresAt = time.Now().UTC().Add(time.Duration(configInt(ctx, "join_token_ttl_seconds", 86400)) * time.Second).Format(time.RFC3339)
	} else {
		parsed, err := time.Parse(time.RFC3339, expiresAt)
		if err != nil {
			return nil, errors.New("expires_at must be RFC3339")
		}
		if !parsed.After(time.Now()) {
			return nil, errors.New("expires_at must be in the future")
		}
		expiresAt = parsed.UTC().Format(time.RFC3339)
	}
	token := randomToken()
	tokenHash := hashSecret(token)
	res, err := ctx.AppDB().Exec(
		`INSERT INTO join_tokens
			(project_id, room_id, token, token_hash, participant_kind, role, display_name, capabilities, expires_at, max_uses)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, roomID, "sha256:"+tokenHash, tokenHash, kind, role, nullStr(displayName), caps, expiresAt, maxUses)
	if err != nil {
		return nil, fmt.Errorf("insert join token: %w", err)
	}
	id, _ := res.LastInsertId()
	jt, err := a.dbGetJoinTokenByID(ctx, pid, id)
	if err != nil {
		return nil, err
	}
	jt.Token = token
	jt.JoinURL = a.joinURL(ctx, token, jt.ProjectID)
	return map[string]any{"join_token": jt, "token": jt.Token, "join_url": jt.JoinURL}, nil
}

func (a *App) toolJoinRoom(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	token := strArg(args, "token")
	if token == "" {
		return nil, errors.New("token required")
	}
	jt, err := a.dbGetJoinToken(ctx, token)
	if err != nil {
		return nil, err
	}
	if jt == nil {
		return nil, errors.New("join token not found")
	}
	if jt.RevokedAt != "" {
		return nil, errors.New("join token revoked")
	}
	if jt.ExpiresAt != "" {
		expiresAt, parseErr := time.Parse(time.RFC3339, jt.ExpiresAt)
		if parseErr != nil || !expiresAt.After(time.Now()) {
			return nil, errors.New("join token expired")
		}
	}
	if jt.MaxUses > 0 && jt.Uses >= jt.MaxUses {
		return nil, errors.New("join token exhausted")
	}
	displayName := strArg(args, "display_name")
	if displayName == "" {
		displayName = jt.DisplayName
	}
	if err := requireLength("display_name", displayName, 120); err != nil {
		return nil, err
	}
	clientInfo, err := metadataArg(args, "client_info")
	if err != nil {
		return nil, err
	}
	if len(clientInfo) > 16<<10 {
		return nil, errors.New("client_info exceeds 16384 bytes")
	}
	if err := validateJSONObject(clientInfo, "client_info"); err != nil {
		return nil, err
	}
	participantToken := randomToken()
	participantHash := hashSecret(participantToken)
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	consume, err := tx.Exec(
		`UPDATE join_tokens
		    SET uses = uses + 1
		  WHERE id = ? AND revoked_at IS NULL
		    AND (max_uses IS NULL OR max_uses <= 0 OR uses < max_uses)
		    AND (expires_at IS NULL OR datetime(expires_at) > CURRENT_TIMESTAMP)`, jt.ID)
	if err != nil {
		return nil, err
	}
	consumed, _ := consume.RowsAffected()
	if consumed != 1 {
		return nil, errors.New("join token expired, revoked, or exhausted")
	}
	var roomStatus string
	if err := tx.QueryRow(`SELECT status FROM rooms WHERE project_id = ? AND id = ?`, jt.ProjectID, jt.RoomID).Scan(&roomStatus); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("room unavailable")
		}
		return nil, err
	}
	if roomStatus != "open" {
		return nil, errors.New("room unavailable")
	}
	maxParticipants := configInt(ctx, "max_participants_per_room", 16)
	var participantCount int
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM participants WHERE project_id = ? AND room_id = ? AND status IN ('joining','active')`,
		jt.ProjectID, jt.RoomID).Scan(&participantCount); err != nil {
		return nil, err
	}
	if participantCount >= maxParticipants {
		return nil, fmt.Errorf("at max_participants_per_room=%d active participants", maxParticipants)
	}
	res, err := tx.Exec(
		`INSERT INTO participants
			(project_id, room_id, participant_key, kind, role, display_name, status, capabilities, last_seen_at, metadata)
		 VALUES (?, ?, ?, ?, ?, ?, 'active', ?, CURRENT_TIMESTAMP, ?)`,
		jt.ProjectID, jt.RoomID, participantHash, jt.ParticipantKind, jt.Role, nullStr(displayName), jt.Capabilities, clientInfo)
	if err != nil {
		return nil, err
	}
	participantID, _ := res.LastInsertId()
	sessionID := randomToken()
	res, err = tx.Exec(
		`INSERT INTO peer_sessions
			(project_id, room_id, participant_id, session_id, transport, status, connection_state)
		 VALUES (?, ?, ?, ?, 'webrtc-mesh', 'negotiating', 'new')`,
		jt.ProjectID, jt.RoomID, participantID, sessionID)
	if err != nil {
		return nil, err
	}
	sessionRowID, _ := res.LastInsertId()
	if _, err := tx.Exec(`UPDATE rooms SET last_activity_at = CURRENT_TIMESTAMP WHERE id = ? AND project_id = ?`, jt.RoomID, jt.ProjectID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	p, err := a.dbGetParticipant(ctx, jt.ProjectID, jt.RoomID, participantID)
	if err != nil {
		return nil, err
	}
	s, err := a.dbGetPeerSession(ctx, jt.ProjectID, sessionRowID)
	if err != nil {
		return nil, err
	}
	iceServers, err := configuredICEServers(ctx)
	if err != nil {
		return nil, err
	}
	a.emit(ctx, jt.ProjectID, "participant.joined", map[string]any{
		"room_id": jt.RoomID, "participant_id": participantID,
		"participant_kind": jt.ParticipantKind, "session_id": s.SessionID,
	})
	return map[string]any{
		"participant": p, "participant_token": participantToken, "session": s,
		"rtc_config": map[string]any{"ice_servers": iceServers, "mode": "mesh-signaling", "heartbeat_interval_seconds": 15},
	}, nil
}

func (a *App) toolLeaveRoom(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	roomID := int64Arg(args, "room_id")
	participantID := int64Arg(args, "participant_id")
	if roomID == 0 || participantID == 0 {
		return nil, errors.New("room_id and participant_id required")
	}
	p, err := a.dbGetParticipant(ctx, pid, roomID, participantID)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, errors.New("participant not found")
	}
	p, changed, err := a.transitionParticipant(ctx, pid, roomID, participantID, "left", "")
	if err != nil {
		return nil, err
	}
	if changed {
		a.emit(ctx, pid, "participant.left", map[string]any{"room_id": roomID, "participant_id": participantID, "participant_kind": p.Kind})
		a.emit(ctx, pid, "peer.closed", map[string]any{"room_id": roomID, "participant_id": participantID})
	}
	return map[string]any{"participant": p}, nil
}

func (a *App) toolListParticipants(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	roomID := int64Arg(args, "room_id")
	if roomID == 0 {
		return nil, errors.New("room_id required")
	}
	ps, err := a.dbListParticipants(ctx, pid, roomID, strArg(args, "status"), strArg(args, "kind"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"participants": ps, "count": len(ps)}, nil
}

func (a *App) toolUpdateParticipant(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	roomID := int64Arg(args, "room_id")
	participantID := int64Arg(args, "participant_id")
	patch, _ := args["patch"].(map[string]any)
	if roomID == 0 || participantID == 0 || patch == nil {
		return nil, errors.New("room_id, participant_id, and patch required")
	}
	p, err := a.dbGetParticipant(ctx, pid, roomID, participantID)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, errors.New("participant not found")
	}
	sets := []string{}
	qargs := []any{}
	if v, ok := patch["display_name"].(string); ok {
		if err := requireLength("display_name", v, 120); err != nil {
			return nil, err
		}
		sets = append(sets, "display_name = ?")
		qargs = append(qargs, nullStr(v))
	}
	if v, ok := patch["role"].(string); ok {
		if v != "host" && v != "guest" && v != "observer" {
			return nil, errors.New("role must be host, guest, or observer")
		}
		sets = append(sets, "role = ?")
		qargs = append(qargs, validateRole(v))
	}
	if _, ok := patch["capabilities"]; ok {
		caps, err := metadataArg(patch, "capabilities")
		if err != nil {
			return nil, err
		}
		if len(caps) > 16<<10 {
			return nil, errors.New("capabilities exceeds 16384 bytes")
		}
		if err := validateCapabilitiesJSON(caps); err != nil {
			return nil, err
		}
		sets = append(sets, "capabilities = ?")
		qargs = append(qargs, caps)
	}
	if _, ok := patch["muted_audio"]; ok {
		if _, valid := patch["muted_audio"].(bool); !valid {
			return nil, errors.New("muted_audio must be boolean")
		}
		sets = append(sets, "muted_audio = ?")
		qargs = append(qargs, boolToInt(boolPatch(patch, "muted_audio")))
	}
	if _, ok := patch["muted_video"]; ok {
		if _, valid := patch["muted_video"].(bool); !valid {
			return nil, errors.New("muted_video must be boolean")
		}
		sets = append(sets, "muted_video = ?")
		qargs = append(qargs, boolToInt(boolPatch(patch, "muted_video")))
	}
	if len(sets) == 0 {
		return map[string]any{"participant": p}, nil
	}
	qargs = append(qargs, participantID, roomID, pid)
	if _, err := ctx.AppDB().Exec(
		`UPDATE participants SET `+strings.Join(sets, ", ")+` WHERE id = ? AND room_id = ? AND project_id = ?`,
		qargs...); err != nil {
		return nil, err
	}
	p, err = a.dbGetParticipant(ctx, pid, roomID, participantID)
	if err != nil {
		return nil, err
	}
	a.emit(ctx, pid, "participant.updated", map[string]any{"room_id": roomID, "participant_id": participantID, "participant_kind": p.Kind})
	return map[string]any{"participant": p}, nil
}

func (a *App) toolRemoveParticipant(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	roomID := int64Arg(args, "room_id")
	participantID := int64Arg(args, "participant_id")
	if roomID == 0 || participantID == 0 {
		return nil, errors.New("room_id and participant_id required")
	}
	p, err := a.dbGetParticipant(ctx, pid, roomID, participantID)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, errors.New("participant not found")
	}
	reason := strArg(args, "reason")
	if err := requireLength("reason", reason, 500); err != nil {
		return nil, err
	}
	p, changed, err := a.transitionParticipant(ctx, pid, roomID, participantID, "removed", reason)
	if err != nil {
		return nil, err
	}
	if changed {
		a.emit(ctx, pid, "participant.removed", map[string]any{"room_id": roomID, "participant_id": participantID, "participant_kind": p.Kind, "reason": reason})
	}
	return map[string]any{"participant": p, "removed": changed}, nil
}

func (a *App) transitionParticipant(ctx *sdk.AppCtx, pid string, roomID, participantID int64, status, reason string) (*Participant, bool, error) {
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`UPDATE participants SET status=?, left_at=CURRENT_TIMESTAMP WHERE id=? AND room_id=? AND project_id=? AND status IN ('joining','active')`, status, participantID, roomID, pid)
	if err != nil {
		return nil, false, err
	}
	changed, _ := res.RowsAffected()
	if changed == 1 {
		if _, err := tx.Exec(`UPDATE peer_sessions SET status='closed', closed_at=CURRENT_TIMESTAMP, connection_state='closed', error=? WHERE participant_id=? AND room_id=? AND project_id=? AND status IN ('negotiating','connected')`, nullStr(reason), participantID, roomID, pid); err != nil {
			return nil, false, err
		}
		if _, err := tx.Exec(`UPDATE media_tracks SET status='ended', ended_at=CURRENT_TIMESTAMP WHERE participant_id=? AND room_id=? AND project_id=? AND status='live'`, participantID, roomID, pid); err != nil {
			return nil, false, err
		}
		if _, err := tx.Exec(`DELETE FROM signaling_messages WHERE room_id=? AND project_id=? AND (from_participant_id=? OR to_participant_id=?)`, roomID, pid, participantID, participantID); err != nil {
			return nil, false, err
		}
		if _, err := tx.Exec(`UPDATE rooms SET last_activity_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=?`, roomID, pid); err != nil {
			return nil, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	p, err := a.dbGetParticipant(ctx, pid, roomID, participantID)
	return p, changed == 1, err
}

func (a *App) toolSendMessage(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	roomID := int64Arg(args, "room_id")
	participantID := int64Arg(args, "participant_id")
	body := strings.TrimSpace(strArg(args, "body"))
	if roomID == 0 || participantID == 0 || body == "" {
		return nil, errors.New("room_id, participant_id, and body required")
	}
	p, err := a.dbGetParticipant(ctx, pid, roomID, participantID)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, errors.New("participant not found")
	}
	if p.Status != "active" {
		return nil, errors.New("participant is not active")
	}
	if !hasCapability(p, "chat") {
		return nil, errors.New("participant lacks chat capability")
	}
	if err := requireLength("body", body, 64<<10); err != nil {
		return nil, err
	}
	rawKind := strArg(args, "kind")
	rawVisibility := strArg(args, "visibility")
	if rawKind != "" && rawKind != "chat" && rawKind != "system" && rawKind != "note" {
		return nil, errors.New("kind must be chat, system, or note")
	}
	if rawVisibility != "" && rawVisibility != "room" && rawVisibility != "private" && rawVisibility != "internal" {
		return nil, errors.New("visibility must be room, private, or internal")
	}
	kind := validateMessageKind(rawKind)
	visibility := validateVisibility(rawVisibility)
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(
		`INSERT INTO room_messages (project_id, room_id, participant_id, kind, visibility, body)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		pid, roomID, participantID, kind, visibility, body)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	if _, err := tx.Exec(`UPDATE rooms SET last_activity_at = CURRENT_TIMESTAMP WHERE project_id = ? AND id = ?`, pid, roomID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	msg, err := a.dbGetMessage(ctx, pid, id)
	if err != nil {
		return nil, err
	}
	a.emit(ctx, pid, "message.created", map[string]any{"room_id": roomID, "participant_id": participantID, "message_id": id, "participant_kind": p.Kind})
	return map[string]any{"message": msg}, nil
}

func (a *App) toolGetMessages(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	roomID := int64Arg(args, "room_id")
	if roomID == 0 {
		return nil, errors.New("room_id required")
	}
	limit := intArg(args, "limit", 100)
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	sinceID := int64Arg(args, "since_id")
	latest := boolArg(args, "latest", sinceID == 0)
	order := "ASC"
	where := `project_id = ? AND room_id = ? AND id > ?`
	if latest && sinceID == 0 {
		order = "DESC"
		where = `project_id = ? AND room_id = ?`
	}
	qargs := []any{pid, roomID}
	if !(latest && sinceID == 0) {
		qargs = append(qargs, sinceID)
	}
	qargs = append(qargs, limit)
	rows, err := ctx.AppDB().Query(
		`SELECT id, project_id, room_id, COALESCE(participant_id,0), kind, visibility, body, created_at
		   FROM room_messages WHERE `+where+` ORDER BY id `+order+` LIMIT ?`, qargs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	msgs := []*Message{}
	for rows.Next() {
		msg := &Message{}
		if err := rows.Scan(&msg.ID, &msg.ProjectID, &msg.RoomID, &msg.ParticipantID, &msg.Kind, &msg.Visibility, &msg.Body, &msg.CreatedAt); err != nil {
			return nil, err
		}
		msgs = append(msgs, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if order == "DESC" {
		slices.Reverse(msgs)
	}
	return map[string]any{"messages": msgs, "count": len(msgs)}, nil
}

func (a *App) toolGetTranscript(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	roomID := int64Arg(args, "room_id")
	if roomID == 0 {
		return nil, errors.New("room_id required")
	}
	limit := intArg(args, "limit", 100)
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	where := []string{"project_id = ?", "room_id = ?", "id > ?"}
	qargs := []any{pid, roomID, int64Arg(args, "since_id")}
	if participantID := int64Arg(args, "participant_id"); participantID != 0 {
		where = append(where, "participant_id = ?")
		qargs = append(qargs, participantID)
	}
	latest := boolArg(args, "latest", int64Arg(args, "since_id") == 0)
	order := "ASC"
	if latest && int64Arg(args, "since_id") == 0 {
		where = []string{"project_id = ?", "room_id = ?"}
		qargs = []any{pid, roomID}
		if participantID := int64Arg(args, "participant_id"); participantID != 0 {
			where = append(where, "participant_id = ?")
			qargs = append(qargs, participantID)
		}
		order = "DESC"
	}
	qargs = append(qargs, limit)
	rows, err := ctx.AppDB().Query(
		`SELECT id, project_id, room_id, COALESCE(participant_id,0), COALESCE(speaker_name,''),
		        text, COALESCE(started_at_ms,0), COALESCE(ended_at_ms,0), COALESCE(confidence,0), source, created_at
		   FROM transcripts WHERE `+strings.Join(where, " AND ")+` ORDER BY id `+order+` LIMIT ?`,
		qargs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []*TranscriptItem{}
	for rows.Next() {
		item := &TranscriptItem{}
		if err := rows.Scan(&item.ID, &item.ProjectID, &item.RoomID, &item.ParticipantID, &item.SpeakerName, &item.Text, &item.StartedAtMS, &item.EndedAtMS, &item.Confidence, &item.Source, &item.CreatedAt); err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	if order == "DESC" {
		slices.Reverse(items)
	}
	return map[string]any{"transcript_items": items, "count": len(items)}, nil
}

func (a *App) toolAppendTranscript(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	roomID := int64Arg(args, "room_id")
	participantID := int64Arg(args, "participant_id")
	text := strings.TrimSpace(strArg(args, "text"))
	if roomID == 0 || participantID == 0 || text == "" {
		return nil, errors.New("room_id, participant_id, and text required")
	}
	p, err := a.dbGetParticipant(ctx, pid, roomID, participantID)
	if err != nil {
		return nil, err
	}
	if p == nil {
		return nil, errors.New("participant not found")
	}
	if p.Status != "active" {
		return nil, errors.New("participant is not active")
	}
	if !hasCapability(p, "transcript_write") {
		return nil, errors.New("participant lacks transcript_write capability")
	}
	if err := requireLength("text", text, 64<<10); err != nil {
		return nil, err
	}
	started := int64Arg(args, "started_at_ms")
	ended := int64Arg(args, "ended_at_ms")
	if started < 0 || ended < 0 || (ended != 0 && ended < started) {
		return nil, errors.New("transcript timing is invalid")
	}
	confidence := floatArg(args, "confidence", 0)
	if _, supplied := args["confidence"]; supplied && (confidence < 0 || confidence > 1) {
		return nil, errors.New("confidence must be between 0 and 1")
	}
	var confidenceDB any
	if _, supplied := args["confidence"]; supplied {
		confidenceDB = confidence
	}
	source := strArg(args, "source")
	if source == "" {
		source = "audio"
	}
	if err := requireLength("source", source, 64); err != nil {
		return nil, err
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(
		`INSERT INTO transcripts
			(project_id, room_id, participant_id, speaker_name, text, started_at_ms, ended_at_ms, confidence, source)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, roomID, participantID, nullStr(p.DisplayName), text,
		nullInt64(started), nullInt64(ended), confidenceDB, source)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	if _, err := tx.Exec(`UPDATE rooms SET last_activity_at = CURRENT_TIMESTAMP WHERE project_id = ? AND id = ?`, pid, roomID); err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	item, err := a.dbGetTranscriptItem(ctx, pid, id)
	if err != nil {
		return nil, err
	}
	a.emit(ctx, pid, "transcript.created", map[string]any{"room_id": roomID, "participant_id": participantID, "transcript_id": id, "participant_kind": p.Kind})
	return map[string]any{"transcript_item": item}, nil
}

type queryRower interface {
	QueryRow(query string, args ...any) *sql.Row
}

func uniqueSlugTx(q queryRower, pid, base string) (string, error) {
	for i := 0; i < 1000; i++ {
		candidate := base
		if i > 0 {
			candidate = fmt.Sprintf("%s-%d", base, i+1)
		}
		var existing int
		if err := q.QueryRow(`SELECT COUNT(*) FROM rooms WHERE project_id = ? AND slug = ?`, pid, candidate).Scan(&existing); err != nil {
			return "", err
		}
		if existing == 0 {
			return candidate, nil
		}
	}
	return "", errors.New("could not allocate unique slug")
}

func configInt(ctx *sdk.AppCtx, name string, fallback int) int {
	if ctx == nil {
		return fallback
	}
	if v := ctx.Config().Get(name); v != "" {
		if n := intArg(map[string]any{"value": v}, "value", fallback); n > 0 {
			return n
		}
	}
	return fallback
}

func defaultCapabilities(kind, role string) string {
	caps := map[string]bool{
		"audio": true, "video": true, "screen": true, "chat": true,
		"transcript_read": kind == "agent" || kind == "service", "transcript_write": kind == "agent" || kind == "service",
		"room_control": role == "host",
	}
	raw, _ := json.Marshal(caps)
	return string(raw)
}

func configuredICEServers(ctx *sdk.AppCtx) ([]map[string]any, error) {
	raw := strings.TrimSpace(ctx.Config().Get("ice_servers_json"))
	if raw == "" {
		raw = `[{"urls":["stun:stun.l.google.com:19302"]}]`
	}
	if len(raw) > 32<<10 {
		return nil, errors.New("ice_servers_json exceeds 32768 bytes")
	}
	var servers []map[string]any
	if err := json.Unmarshal([]byte(raw), &servers); err != nil {
		return nil, fmt.Errorf("ice_servers_json: %w", err)
	}
	if len(servers) > 16 {
		return nil, errors.New("ice_servers_json contains too many servers")
	}
	for _, server := range servers {
		if _, ok := server["urls"]; !ok {
			return nil, errors.New("each ICE server requires urls")
		}
	}
	return servers, nil
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func boolPatch(patch map[string]any, key string) bool {
	v, _ := patch[key].(bool)
	return v
}

func (a *App) dbGetRoom(ctx *sdk.AppCtx, pid string, id int64) (*Room, error) {
	row := ctx.AppDB().QueryRow(
		`SELECT id, project_id, slug, title, status, COALESCE(created_by,''), created_at,
		        COALESCE(started_at,''), COALESCE(ended_at,''), metadata, COALESCE(last_activity_at,'')
		   FROM rooms WHERE project_id = ? AND id = ?`,
		pid, id)
	r := &Room{}
	if err := scanRoomRow(row, r); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return r, nil
}

func scanRoomRow(row scanner, r *Room) error {
	return row.Scan(&r.ID, &r.ProjectID, &r.Slug, &r.Title, &r.Status, &r.CreatedBy, &r.CreatedAt, &r.StartedAt, &r.EndedAt, &r.Metadata, &r.LastActivityAt)
}

func (a *App) dbGetJoinToken(ctx *sdk.AppCtx, token string) (*JoinToken, error) {
	hash := hashSecret(token)
	row := ctx.AppDB().QueryRow(
		`SELECT id, project_id, room_id, '', participant_kind, role, COALESCE(display_name,''),
		        capabilities, COALESCE(expires_at,''), COALESCE(max_uses, 0), uses, created_at,
		        COALESCE(revoked_at,'')
		   FROM join_tokens WHERE token_hash = ? OR (token_hash IS NULL AND token = ?)`, hash, token)
	return scanJoinToken(row)
}

func (a *App) dbGetJoinTokenByID(ctx *sdk.AppCtx, pid string, id int64) (*JoinToken, error) {
	row := ctx.AppDB().QueryRow(
		`SELECT id, project_id, room_id, '', participant_kind, role, COALESCE(display_name,''),
		        capabilities, COALESCE(expires_at,''), COALESCE(max_uses, 0), uses, created_at,
		        COALESCE(revoked_at,'')
		   FROM join_tokens WHERE project_id = ? AND id = ?`,
		pid, id)
	return scanJoinToken(row)
}

type scanner interface {
	Scan(dest ...any) error
}

func scanJoinToken(row scanner) (*JoinToken, error) {
	j := &JoinToken{}
	if err := row.Scan(&j.ID, &j.ProjectID, &j.RoomID, &j.Token, &j.ParticipantKind, &j.Role, &j.DisplayName, &j.Capabilities, &j.ExpiresAt, &j.MaxUses, &j.Uses, &j.CreatedAt, &j.RevokedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return j, nil
}

func (a *App) dbGetParticipant(ctx *sdk.AppCtx, pid string, roomID, id int64) (*Participant, error) {
	row := ctx.AppDB().QueryRow(
		`SELECT id, project_id, room_id, participant_key, kind, role, COALESCE(display_name,''),
		        status, capabilities, joined_at, COALESCE(left_at,''), COALESCE(last_seen_at,''),
		        muted_audio, muted_video, metadata
		   FROM participants WHERE project_id = ? AND room_id = ? AND id = ?`,
		pid, roomID, id)
	return scanParticipant(row)
}

func scanParticipant(row scanner) (*Participant, error) {
	p := &Participant{}
	var mutedAudio, mutedVideo int
	if err := row.Scan(&p.ID, &p.ProjectID, &p.RoomID, &p.ParticipantKey, &p.Kind, &p.Role, &p.DisplayName, &p.Status, &p.Capabilities, &p.JoinedAt, &p.LeftAt, &p.LastSeenAt, &mutedAudio, &mutedVideo, &p.Metadata); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	p.MutedAudio = mutedAudio != 0
	p.MutedVideo = mutedVideo != 0
	return p, nil
}

func (a *App) dbListParticipants(ctx *sdk.AppCtx, pid string, roomID int64, status, kind string) ([]*Participant, error) {
	where := []string{"project_id = ?", "room_id = ?"}
	qargs := []any{pid, roomID}
	if status != "" {
		where = append(where, "status = ?")
		qargs = append(qargs, status)
	}
	if kind != "" {
		where = append(where, "kind = ?")
		qargs = append(qargs, kind)
	}
	rows, err := ctx.AppDB().Query(
		`SELECT id, project_id, room_id, participant_key, kind, role, COALESCE(display_name,''),
		        status, capabilities, joined_at, COALESCE(left_at,''), COALESCE(last_seen_at,''),
		        muted_audio, muted_video, metadata
		   FROM participants WHERE `+strings.Join(where, " AND ")+` ORDER BY joined_at ASC`,
		qargs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Participant{}
	for rows.Next() {
		p, err := scanParticipant(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func (a *App) dbGetPeerSession(ctx *sdk.AppCtx, pid string, id int64) (*PeerSession, error) {
	row := ctx.AppDB().QueryRow(
		`SELECT id, project_id, room_id, participant_id, session_id, transport, status,
		        COALESCE(offer_sdp,''), COALESCE(answer_sdp,''), COALESCE(ice_state,''),
		        COALESCE(connection_state,''), created_at, COALESCE(connected_at,''),
		        COALESCE(closed_at,''), COALESCE(error,'')
		   FROM peer_sessions WHERE project_id = ? AND id = ?`,
		pid, id)
	s := &PeerSession{}
	if err := row.Scan(&s.ID, &s.ProjectID, &s.RoomID, &s.ParticipantID, &s.SessionID, &s.Transport, &s.Status, &s.OfferSDP, &s.AnswerSDP, &s.ICEState, &s.ConnectionState, &s.CreatedAt, &s.ConnectedAt, &s.ClosedAt, &s.Error); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return s, nil
}

func (a *App) dbListTracks(ctx *sdk.AppCtx, pid string, roomID int64) ([]*MediaTrack, error) {
	rows, err := ctx.AppDB().Query(
		`SELECT id, project_id, room_id, participant_id, COALESCE(peer_session_id,0), track_id, kind,
		        COALESCE(source,''), COALESCE(label,''), status, started_at, COALESCE(ended_at,''), metadata
		   FROM media_tracks WHERE project_id = ? AND room_id = ? ORDER BY started_at ASC`,
		pid, roomID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*MediaTrack{}
	for rows.Next() {
		t := &MediaTrack{}
		if err := rows.Scan(&t.ID, &t.ProjectID, &t.RoomID, &t.ParticipantID, &t.PeerSessionID, &t.TrackID, &t.Kind, &t.Source, &t.Label, &t.Status, &t.StartedAt, &t.EndedAt, &t.Metadata); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, nil
}

func (a *App) dbGetMessage(ctx *sdk.AppCtx, pid string, id int64) (*Message, error) {
	row := ctx.AppDB().QueryRow(
		`SELECT id, project_id, room_id, COALESCE(participant_id,0), kind, visibility, body, created_at
		   FROM room_messages WHERE project_id = ? AND id = ?`,
		pid, id)
	m := &Message{}
	if err := row.Scan(&m.ID, &m.ProjectID, &m.RoomID, &m.ParticipantID, &m.Kind, &m.Visibility, &m.Body, &m.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return m, nil
}

func (a *App) dbGetTranscriptItem(ctx *sdk.AppCtx, pid string, id int64) (*TranscriptItem, error) {
	row := ctx.AppDB().QueryRow(
		`SELECT id, project_id, room_id, COALESCE(participant_id,0), COALESCE(speaker_name,''),
		        text, COALESCE(started_at_ms,0), COALESCE(ended_at_ms,0), COALESCE(confidence,0),
		        source, created_at
		   FROM transcripts WHERE project_id = ? AND id = ?`,
		pid, id)
	item := &TranscriptItem{}
	if err := row.Scan(&item.ID, &item.ProjectID, &item.RoomID, &item.ParticipantID, &item.SpeakerName, &item.Text, &item.StartedAtMS, &item.EndedAtMS, &item.Confidence, &item.Source, &item.CreatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return item, nil
}
