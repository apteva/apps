package main

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"

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
	if err := a.enforceRoomLimit(ctx, pid); err != nil {
		return nil, err
	}
	meta, err := metadataArg(args, "metadata")
	if err != nil {
		return nil, err
	}
	baseSlug := slugify(strArg(args, "slug"))
	if strArg(args, "slug") == "" {
		baseSlug = slugify(title)
	}
	slug, err := a.uniqueSlug(ctx, pid, baseSlug)
	if err != nil {
		return nil, err
	}
	res, err := ctx.AppDB().Exec(
		`INSERT INTO rooms (project_id, slug, title, status, started_at, metadata)
		 VALUES (?, ?, ?, 'open', CURRENT_TIMESTAMP, ?)`,
		pid, slug, title, meta)
	if err != nil {
		return nil, fmt.Errorf("insert room: %w", err)
	}
	id, _ := res.LastInsertId()
	room, err := a.dbGetRoom(ctx, pid, id)
	if err != nil {
		return nil, err
	}
	a.emit(ctx, pid, "room.created", map[string]any{"room_id": id})

	tokenOut, err := a.toolCreateJoinToken(ctx, map[string]any{
		"_project_id":       pid,
		"room_id":           id,
		"participant_kind":  "human",
		"role":              "host",
		"display_name":      "Host",
		"max_uses":          1,
		"capabilities":      map[string]any{"audio": true, "video": true, "screen": true, "chat": true, "room_control": true},
		"suppress_room_get": true,
	})
	if err != nil {
		return nil, err
	}
	jt := tokenOut.(map[string]any)["join_token"].(*JoinToken)
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
		`SELECT id FROM rooms WHERE `+strings.Join(where, " AND ")+` ORDER BY created_at DESC LIMIT ?`,
		qargs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	rooms := []*Room{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		room, err := a.dbGetRoom(ctx, pid, id)
		if err != nil {
			return nil, err
		}
		if room != nil {
			rooms = append(rooms, room)
		}
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
		if _, err := ctx.AppDB().Exec(
			`UPDATE peer_sessions SET status='closed', closed_at = CURRENT_TIMESTAMP WHERE room_id = ? AND project_id = ? AND status IN ('negotiating','connected');
			 UPDATE media_tracks SET status='ended', ended_at = CURRENT_TIMESTAMP WHERE room_id = ? AND project_id = ? AND status = 'live';
			 UPDATE participants SET status='left', left_at = COALESCE(left_at, CURRENT_TIMESTAMP) WHERE room_id = ? AND project_id = ? AND status IN ('joining','active');
			 UPDATE rooms SET status='ended', ended_at = CURRENT_TIMESTAMP WHERE id = ? AND project_id = ?`,
			id, pid, id, pid, id, pid, id, pid); err != nil {
			return nil, err
		}
		a.emit(ctx, pid, "room.ended", map[string]any{"room_id": id})
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
	kind := validateKind(strArg(args, "participant_kind"))
	role := validateRole(strArg(args, "role"))
	maxUses := intArg(args, "max_uses", 1)
	if maxUses <= 0 {
		maxUses = 1
	}
	token := randomToken()
	res, err := ctx.AppDB().Exec(
		`INSERT INTO join_tokens
			(project_id, room_id, token, participant_kind, role, display_name, capabilities, expires_at, max_uses)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, roomID, token, kind, role, nullStr(strArg(args, "display_name")), caps, nullStr(strArg(args, "expires_at")), maxUses)
	if err != nil {
		return nil, fmt.Errorf("insert join token: %w", err)
	}
	id, _ := res.LastInsertId()
	jt, err := a.dbGetJoinTokenByID(ctx, pid, id)
	if err != nil {
		return nil, err
	}
	jt.JoinURL = a.joinURL(ctx, jt.Token)
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
	if jt.ExpiresAt != "" && jt.ExpiresAt < nowRFC3339() {
		return nil, errors.New("join token expired")
	}
	if jt.MaxUses > 0 && jt.Uses >= jt.MaxUses {
		return nil, errors.New("join token exhausted")
	}
	room, err := a.dbGetRoom(ctx, jt.ProjectID, jt.RoomID)
	if err != nil {
		return nil, err
	}
	if room == nil || room.Status == "ended" {
		return nil, errors.New("room unavailable")
	}
	if err := a.enforceParticipantLimit(ctx, jt.ProjectID, jt.RoomID); err != nil {
		return nil, err
	}
	displayName := strArg(args, "display_name")
	if displayName == "" {
		displayName = jt.DisplayName
	}
	clientInfo, err := metadataArg(args, "client_info")
	if err != nil {
		return nil, err
	}
	participantKey := randomToken()
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE join_tokens SET uses = uses + 1 WHERE id = ?`, jt.ID); err != nil {
		return nil, err
	}
	res, err := tx.Exec(
		`INSERT INTO participants
			(project_id, room_id, participant_key, kind, role, display_name, status, capabilities, last_seen_at, metadata)
		 VALUES (?, ?, ?, ?, ?, ?, 'active', ?, CURRENT_TIMESTAMP, ?)`,
		jt.ProjectID, jt.RoomID, participantKey, jt.ParticipantKind, jt.Role, nullStr(displayName), jt.Capabilities, clientInfo)
	if err != nil {
		return nil, err
	}
	participantID, _ := res.LastInsertId()
	sessionID := randomToken()
	res, err = tx.Exec(
		`INSERT INTO peer_sessions
			(project_id, room_id, participant_id, session_id, transport, status, connected_at, connection_state)
		 VALUES (?, ?, ?, ?, 'webrtc', 'connected', CURRENT_TIMESTAMP, 'connected')`,
		jt.ProjectID, jt.RoomID, participantID, sessionID)
	if err != nil {
		return nil, err
	}
	sessionRowID, _ := res.LastInsertId()
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
	a.emit(ctx, jt.ProjectID, "participant.joined", map[string]any{
		"room_id":          jt.RoomID,
		"participant_id":   participantID,
		"participant_kind": jt.ParticipantKind,
		"session_id":       s.SessionID,
	})
	a.emit(ctx, jt.ProjectID, "peer.connected", map[string]any{"room_id": jt.RoomID, "participant_id": participantID, "peer_session_id": s.ID})
	return map[string]any{
		"participant": p,
		"session":     s,
		"rtc_config": map[string]any{
			"ice_servers": []any{},
			"mode":        "app-signaling",
		},
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
	if _, err := ctx.AppDB().Exec(
		`UPDATE peer_sessions SET status='closed', closed_at = CURRENT_TIMESTAMP WHERE participant_id = ? AND project_id = ? AND status IN ('negotiating','connected');
		 UPDATE media_tracks SET status='ended', ended_at = CURRENT_TIMESTAMP WHERE participant_id = ? AND project_id = ? AND status = 'live';
		 UPDATE participants SET status='left', left_at = CURRENT_TIMESTAMP WHERE id = ? AND project_id = ?`,
		participantID, pid, participantID, pid, participantID, pid); err != nil {
		return nil, err
	}
	p, err = a.dbGetParticipant(ctx, pid, roomID, participantID)
	if err != nil {
		return nil, err
	}
	a.emit(ctx, pid, "participant.left", map[string]any{"room_id": roomID, "participant_id": participantID, "participant_kind": p.Kind})
	a.emit(ctx, pid, "peer.closed", map[string]any{"room_id": roomID, "participant_id": participantID})
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
		sets = append(sets, "display_name = ?")
		qargs = append(qargs, nullStr(v))
	}
	if v, ok := patch["role"].(string); ok {
		sets = append(sets, "role = ?")
		qargs = append(qargs, validateRole(v))
	}
	if _, ok := patch["capabilities"]; ok {
		caps, err := metadataArg(patch, "capabilities")
		if err != nil {
			return nil, err
		}
		sets = append(sets, "capabilities = ?")
		qargs = append(qargs, caps)
	}
	if _, ok := patch["muted_audio"]; ok {
		sets = append(sets, "muted_audio = ?")
		qargs = append(qargs, boolToInt(boolPatch(patch, "muted_audio")))
	}
	if _, ok := patch["muted_video"]; ok {
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
	if _, err := ctx.AppDB().Exec(
		`UPDATE peer_sessions SET status='closed', closed_at = CURRENT_TIMESTAMP, error = ? WHERE participant_id = ? AND project_id = ? AND status IN ('negotiating','connected');
		 UPDATE media_tracks SET status='ended', ended_at = CURRENT_TIMESTAMP WHERE participant_id = ? AND project_id = ? AND status = 'live';
		 UPDATE participants SET status='removed', left_at = CURRENT_TIMESTAMP WHERE id = ? AND project_id = ?`,
		nullStr(reason), participantID, pid, participantID, pid, participantID, pid); err != nil {
		return nil, err
	}
	a.emit(ctx, pid, "participant.removed", map[string]any{"room_id": roomID, "participant_id": participantID, "participant_kind": p.Kind, "reason": reason})
	p, err = a.dbGetParticipant(ctx, pid, roomID, participantID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"participant": p, "removed": true}, nil
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
	kind := validateMessageKind(strArg(args, "kind"))
	visibility := validateVisibility(strArg(args, "visibility"))
	res, err := ctx.AppDB().Exec(
		`INSERT INTO room_messages (project_id, room_id, participant_id, kind, visibility, body)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		pid, roomID, participantID, kind, visibility, body)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
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
	rows, err := ctx.AppDB().Query(
		`SELECT id FROM room_messages
		 WHERE project_id = ? AND room_id = ? AND id > ?
		 ORDER BY id ASC LIMIT ?`,
		pid, roomID, int64Arg(args, "since_id"), limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	msgs := []*Message{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		msg, err := a.dbGetMessage(ctx, pid, id)
		if err != nil {
			return nil, err
		}
		if msg != nil {
			msgs = append(msgs, msg)
		}
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
	qargs = append(qargs, limit)
	rows, err := ctx.AppDB().Query(
		`SELECT id FROM transcripts WHERE `+strings.Join(where, " AND ")+` ORDER BY id ASC LIMIT ?`,
		qargs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := []*TranscriptItem{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		item, err := a.dbGetTranscriptItem(ctx, pid, id)
		if err != nil {
			return nil, err
		}
		if item != nil {
			items = append(items, item)
		}
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
	source := strArg(args, "source")
	if source == "" {
		source = "audio"
	}
	res, err := ctx.AppDB().Exec(
		`INSERT INTO transcripts
			(project_id, room_id, participant_id, speaker_name, text, started_at_ms, ended_at_ms, confidence, source)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, roomID, participantID, nullStr(p.DisplayName), text,
		nullInt64(int64Arg(args, "started_at_ms")), nullInt64(int64Arg(args, "ended_at_ms")), floatArg(args, "confidence", 0), source)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	item, err := a.dbGetTranscriptItem(ctx, pid, id)
	if err != nil {
		return nil, err
	}
	a.emit(ctx, pid, "transcript.created", map[string]any{"room_id": roomID, "participant_id": participantID, "transcript_id": id, "participant_kind": p.Kind})
	return map[string]any{"transcript_item": item}, nil
}

func (a *App) enforceRoomLimit(ctx *sdk.AppCtx, pid string) error {
	max := 50
	if v := ctx.Config().Get("max_rooms"); v != "" {
		if n := intArg(map[string]any{"v": v}, "v", max); n > 0 {
			max = n
		}
	}
	var count int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM rooms WHERE project_id = ? AND status = 'open'`, pid).Scan(&count); err != nil {
		return err
	}
	if count >= max {
		return fmt.Errorf("at max_rooms=%d open rooms", max)
	}
	return nil
}

func (a *App) enforceParticipantLimit(ctx *sdk.AppCtx, pid string, roomID int64) error {
	max := 16
	if v := ctx.Config().Get("max_participants_per_room"); v != "" {
		if n := intArg(map[string]any{"v": v}, "v", max); n > 0 {
			max = n
		}
	}
	var count int
	if err := ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM participants WHERE project_id = ? AND room_id = ? AND status IN ('joining','active')`,
		pid, roomID).Scan(&count); err != nil {
		return err
	}
	if count >= max {
		return fmt.Errorf("at max_participants_per_room=%d active participants", max)
	}
	return nil
}

func (a *App) uniqueSlug(ctx *sdk.AppCtx, pid, base string) (string, error) {
	for i := 0; i < 100; i++ {
		candidate := base
		if i > 0 {
			candidate = fmt.Sprintf("%s-%d", base, i+1)
		}
		var existing int
		if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM rooms WHERE project_id = ? AND slug = ?`, pid, candidate).Scan(&existing); err != nil {
			return "", err
		}
		if existing == 0 {
			return candidate, nil
		}
	}
	return "", errors.New("could not allocate unique slug")
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
		        COALESCE(started_at,''), COALESCE(ended_at,''), metadata
		   FROM rooms WHERE project_id = ? AND id = ?`,
		pid, id)
	r := &Room{}
	if err := row.Scan(&r.ID, &r.ProjectID, &r.Slug, &r.Title, &r.Status, &r.CreatedBy, &r.CreatedAt, &r.StartedAt, &r.EndedAt, &r.Metadata); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return r, nil
}

func (a *App) dbGetJoinToken(ctx *sdk.AppCtx, token string) (*JoinToken, error) {
	row := ctx.AppDB().QueryRow(
		`SELECT id, project_id, room_id, token, participant_kind, role, COALESCE(display_name,''),
		        capabilities, COALESCE(expires_at,''), COALESCE(max_uses, 0), uses, created_at
		   FROM join_tokens WHERE token = ?`,
		token)
	return scanJoinToken(row)
}

func (a *App) dbGetJoinTokenByID(ctx *sdk.AppCtx, pid string, id int64) (*JoinToken, error) {
	row := ctx.AppDB().QueryRow(
		`SELECT id, project_id, room_id, token, participant_kind, role, COALESCE(display_name,''),
		        capabilities, COALESCE(expires_at,''), COALESCE(max_uses, 0), uses, created_at
		   FROM join_tokens WHERE project_id = ? AND id = ?`,
		pid, id)
	return scanJoinToken(row)
}

type scanner interface {
	Scan(dest ...any) error
}

func scanJoinToken(row scanner) (*JoinToken, error) {
	j := &JoinToken{}
	if err := row.Scan(&j.ID, &j.ProjectID, &j.RoomID, &j.Token, &j.ParticipantKind, &j.Role, &j.DisplayName, &j.Capabilities, &j.ExpiresAt, &j.MaxUses, &j.Uses, &j.CreatedAt); err != nil {
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
