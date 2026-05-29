package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: calls
display_name: Calls
version: 0.1.2
description: Standalone realtime audio/video calls with unified participants.
author: Apteva
scopes: [project, global]
requires:
  permissions:
    - db.write.app
    - net.egress
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: calls_create_room,        description: "Create a call room." }
    - { name: calls_get_room,           description: "Get a room snapshot." }
    - { name: calls_list_rooms,         description: "List call rooms." }
    - { name: calls_end_room,           description: "End a call room." }
    - { name: calls_create_join_token,  description: "Create a room join token." }
    - { name: calls_join_room,          description: "Join a room with a token." }
    - { name: calls_leave_room,         description: "Leave a room." }
    - { name: calls_list_participants,  description: "List room participants." }
    - { name: calls_update_participant, description: "Patch participant fields." }
    - { name: calls_remove_participant, description: "Remove a participant." }
    - { name: calls_send_message,       description: "Send a room message." }
    - { name: calls_get_messages,       description: "Get room messages." }
    - { name: calls_get_transcript,     description: "Get room transcript items." }
    - { name: calls_append_transcript,  description: "Append a transcript item." }
  ui_panels:
    - slot: project.page
      label: Calls
      icon: video
      entry: /ui/CallsPanel.mjs
runtime:
  kind: source
  source: { repo: github.com/apteva/apps, ref: main, entry: mcp/calls }
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/calls.db
  migrations: migrations/
upgrade_policy: auto-patch
`

type App struct{}

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("calls requires a db block")
	}
	globalCtx = ctx
	globalApp = a
	if _, err := ctx.AppDB().Exec(
		`UPDATE peer_sessions
		    SET status='closed', closed_at = CURRENT_TIMESTAMP, error='sidecar restarted'
		  WHERE status IN ('negotiating','connected');
		  UPDATE media_tracks SET status='ended', ended_at = CURRENT_TIMESTAMP WHERE status='live';
		  UPDATE participants SET status='left', left_at = CURRENT_TIMESTAMP WHERE status IN ('joining','active')`); err != nil {
		return fmt.Errorf("reconcile: %w", err)
	}
	ctx.Logger().Info("calls mounted")
	return nil
}

func (a *App) OnUnmount(ctx *sdk.AppCtx) error   { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }
func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/join/", Handler: a.handleJoinPage, NoAuth: true},
		{Pattern: "/room/", Handler: a.handleRoomPage, NoAuth: true},
		{Pattern: "/api/join", Handler: a.handleAPIJoin, NoAuth: true},
		{Pattern: "/api/rooms/", Handler: a.handleAPIRooms, NoAuth: true},
		{Pattern: "/admin/rooms", Handler: a.handleAdminRooms},
		{Pattern: "/admin/rooms/", Handler: a.handleAdminRoomItem},
	}
}

func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{
		{Name: "presence-reaper", Schedule: "@every 10s", Run: a.runPresenceReaper},
		{Name: "room-idle-closer", Schedule: "@every 30s", Run: a.runRoomIdleCloser},
		{Name: "session-cleaner", Schedule: "@every 1m", Run: a.runSessionCleaner},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{Name: "calls_create_room", Description: "Create a call room. Args: title, slug?, metadata?.", InputSchema: schemaObject(map[string]any{
			"title": map[string]any{"type": "string"}, "slug": map[string]any{"type": "string"}, "metadata": map[string]any{"type": "object"},
		}, []string{"title"}), Handler: a.toolCreateRoom},
		{Name: "calls_get_room", Description: "Get a room with participants and tracks. Args: id.", InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}), Handler: a.toolGetRoom},
		{Name: "calls_list_rooms", Description: "List rooms. Args: status?, limit?.", InputSchema: schemaObject(map[string]any{
			"status": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer"},
		}, nil), Handler: a.toolListRooms},
		{Name: "calls_end_room", Description: "End a room. Args: id.", InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}), Handler: a.toolEndRoom},
		{Name: "calls_create_join_token", Description: "Create a room join token. Args: room_id, participant_kind?, role?, display_name?, capabilities?, expires_at?, max_uses?.", InputSchema: schemaObject(map[string]any{
			"room_id": map[string]any{"type": "integer"}, "participant_kind": map[string]any{"type": "string"}, "role": map[string]any{"type": "string"}, "display_name": map[string]any{"type": "string"}, "capabilities": map[string]any{"type": "object"}, "expires_at": map[string]any{"type": "string"}, "max_uses": map[string]any{"type": "integer"},
		}, []string{"room_id"}), Handler: a.toolCreateJoinToken},
		{Name: "calls_join_room", Description: "Join a room with a token. Args: token, display_name?, client_info?.", InputSchema: schemaObject(map[string]any{
			"token": map[string]any{"type": "string"}, "display_name": map[string]any{"type": "string"}, "client_info": map[string]any{"type": "object"},
		}, []string{"token"}), Handler: a.toolJoinRoom},
		{Name: "calls_leave_room", Description: "Leave a room. Args: room_id, participant_id.", InputSchema: schemaObject(map[string]any{
			"room_id": map[string]any{"type": "integer"}, "participant_id": map[string]any{"type": "integer"},
		}, []string{"room_id", "participant_id"}), Handler: a.toolLeaveRoom},
		{Name: "calls_list_participants", Description: "List room participants. Args: room_id, status?, kind?.", InputSchema: schemaObject(map[string]any{
			"room_id": map[string]any{"type": "integer"}, "status": map[string]any{"type": "string"}, "kind": map[string]any{"type": "string"},
		}, []string{"room_id"}), Handler: a.toolListParticipants},
		{Name: "calls_update_participant", Description: "Patch participant fields. Args: room_id, participant_id, patch.", InputSchema: schemaObject(map[string]any{
			"room_id": map[string]any{"type": "integer"}, "participant_id": map[string]any{"type": "integer"}, "patch": map[string]any{"type": "object"},
		}, []string{"room_id", "participant_id", "patch"}), Handler: a.toolUpdateParticipant},
		{Name: "calls_remove_participant", Description: "Remove a participant. Args: room_id, participant_id, reason?.", InputSchema: schemaObject(map[string]any{
			"room_id": map[string]any{"type": "integer"}, "participant_id": map[string]any{"type": "integer"}, "reason": map[string]any{"type": "string"},
		}, []string{"room_id", "participant_id"}), Handler: a.toolRemoveParticipant},
		{Name: "calls_send_message", Description: "Send a room message. Args: room_id, participant_id, body, kind?, visibility?.", InputSchema: schemaObject(map[string]any{
			"room_id": map[string]any{"type": "integer"}, "participant_id": map[string]any{"type": "integer"}, "body": map[string]any{"type": "string"}, "kind": map[string]any{"type": "string"}, "visibility": map[string]any{"type": "string"},
		}, []string{"room_id", "participant_id", "body"}), Handler: a.toolSendMessage},
		{Name: "calls_get_messages", Description: "Get room messages. Args: room_id, since_id?, limit?.", InputSchema: schemaObject(map[string]any{
			"room_id": map[string]any{"type": "integer"}, "since_id": map[string]any{"type": "integer"}, "limit": map[string]any{"type": "integer"},
		}, []string{"room_id"}), Handler: a.toolGetMessages},
		{Name: "calls_get_transcript", Description: "Get room transcript items. Args: room_id, since_id?, participant_id?, limit?.", InputSchema: schemaObject(map[string]any{
			"room_id": map[string]any{"type": "integer"}, "since_id": map[string]any{"type": "integer"}, "participant_id": map[string]any{"type": "integer"}, "limit": map[string]any{"type": "integer"},
		}, []string{"room_id"}), Handler: a.toolGetTranscript},
		{Name: "calls_append_transcript", Description: "Append a transcript item. Args: room_id, participant_id, text, started_at_ms?, ended_at_ms?, confidence?, source?.", InputSchema: schemaObject(map[string]any{
			"room_id": map[string]any{"type": "integer"}, "participant_id": map[string]any{"type": "integer"}, "text": map[string]any{"type": "string"}, "started_at_ms": map[string]any{"type": "integer"}, "ended_at_ms": map[string]any{"type": "integer"}, "confidence": map[string]any{"type": "number"}, "source": map[string]any{"type": "string"},
		}, []string{"room_id", "participant_id", "text"}), Handler: a.toolAppendTranscript},
	}
}

func main() { sdk.Run(&App{}) }

type Room struct {
	ID        int64  `json:"id"`
	ProjectID string `json:"project_id,omitempty"`
	Slug      string `json:"slug"`
	Title     string `json:"title"`
	Status    string `json:"status"`
	CreatedBy string `json:"created_by,omitempty"`
	CreatedAt string `json:"created_at"`
	StartedAt string `json:"started_at,omitempty"`
	EndedAt   string `json:"ended_at,omitempty"`
	Metadata  string `json:"metadata"`
	JoinURL   string `json:"join_url,omitempty"`
}

type JoinToken struct {
	ID              int64  `json:"id"`
	ProjectID       string `json:"project_id,omitempty"`
	RoomID          int64  `json:"room_id"`
	Token           string `json:"token"`
	ParticipantKind string `json:"participant_kind"`
	Role            string `json:"role"`
	DisplayName     string `json:"display_name,omitempty"`
	Capabilities    string `json:"capabilities"`
	ExpiresAt       string `json:"expires_at,omitempty"`
	MaxUses         int    `json:"max_uses"`
	Uses            int    `json:"uses"`
	CreatedAt       string `json:"created_at"`
	JoinURL         string `json:"join_url,omitempty"`
}

type Participant struct {
	ID             int64  `json:"id"`
	ProjectID      string `json:"project_id,omitempty"`
	RoomID         int64  `json:"room_id"`
	ParticipantKey string `json:"participant_key"`
	Kind           string `json:"kind"`
	Role           string `json:"role"`
	DisplayName    string `json:"display_name,omitempty"`
	Status         string `json:"status"`
	Capabilities   string `json:"capabilities"`
	JoinedAt       string `json:"joined_at"`
	LeftAt         string `json:"left_at,omitempty"`
	LastSeenAt     string `json:"last_seen_at,omitempty"`
	MutedAudio     bool   `json:"muted_audio"`
	MutedVideo     bool   `json:"muted_video"`
	Metadata       string `json:"metadata"`
}

type PeerSession struct {
	ID              int64  `json:"id"`
	ProjectID       string `json:"project_id,omitempty"`
	RoomID          int64  `json:"room_id"`
	ParticipantID   int64  `json:"participant_id"`
	SessionID       string `json:"session_id"`
	Transport       string `json:"transport"`
	Status          string `json:"status"`
	OfferSDP        string `json:"offer_sdp,omitempty"`
	AnswerSDP       string `json:"answer_sdp,omitempty"`
	ICEState        string `json:"ice_state,omitempty"`
	ConnectionState string `json:"connection_state,omitempty"`
	CreatedAt       string `json:"created_at"`
	ConnectedAt     string `json:"connected_at,omitempty"`
	ClosedAt        string `json:"closed_at,omitempty"`
	Error           string `json:"error,omitempty"`
}

type MediaTrack struct {
	ID            int64  `json:"id"`
	ProjectID     string `json:"project_id,omitempty"`
	RoomID        int64  `json:"room_id"`
	ParticipantID int64  `json:"participant_id"`
	PeerSessionID int64  `json:"peer_session_id,omitempty"`
	TrackID       string `json:"track_id"`
	Kind          string `json:"kind"`
	Source        string `json:"source,omitempty"`
	Label         string `json:"label,omitempty"`
	Status        string `json:"status"`
	StartedAt     string `json:"started_at"`
	EndedAt       string `json:"ended_at,omitempty"`
	Metadata      string `json:"metadata"`
}

type Message struct {
	ID            int64  `json:"id"`
	ProjectID     string `json:"project_id,omitempty"`
	RoomID        int64  `json:"room_id"`
	ParticipantID int64  `json:"participant_id,omitempty"`
	Kind          string `json:"kind"`
	Visibility    string `json:"visibility"`
	Body          string `json:"body"`
	CreatedAt     string `json:"created_at"`
}

type TranscriptItem struct {
	ID            int64   `json:"id"`
	ProjectID     string  `json:"project_id,omitempty"`
	RoomID        int64   `json:"room_id"`
	ParticipantID int64   `json:"participant_id,omitempty"`
	SpeakerName   string  `json:"speaker_name,omitempty"`
	Text          string  `json:"text"`
	StartedAtMS   int64   `json:"started_at_ms,omitempty"`
	EndedAtMS     int64   `json:"ended_at_ms,omitempty"`
	Confidence    float64 `json:"confidence,omitempty"`
	Source        string  `json:"source"`
	CreatedAt     string  `json:"created_at"`
}

var (
	globalCtx *sdk.AppCtx
	globalApp *App
	slugRe    = regexp.MustCompile(`[^a-z0-9]+`)
)

func resolveProjectFromArgs(args map[string]any) (string, error) {
	if env := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); env != "" {
		return env, nil
	}
	if v, ok := args["_project_id"].(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v), nil
	}
	return "", errors.New("project_id missing - pass _project_id when scope=global")
}

func resolveProjectFromRequest(r *http.Request) (string, error) {
	if env := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); env != "" {
		return env, nil
	}
	if v := strings.TrimSpace(r.URL.Query().Get("project_id")); v != "" {
		return v, nil
	}
	return "", errors.New("project_id required in query string when install scope=global")
}

func (a *App) publicBase(ctx *sdk.AppCtx) string {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return "/api/apps/calls"
	}
	id, err := ctx.PlatformAPI().WhoAmI()
	if err != nil || id == nil || id.PublicURL == "" {
		return "/api/apps/calls"
	}
	return strings.TrimRight(id.PublicURL, "/") + "/api/apps/calls"
}

func (a *App) joinURL(ctx *sdk.AppCtx, token string) string {
	return a.publicBase(ctx) + "/join/" + token
}

func (a *App) emit(ctx *sdk.AppCtx, projectID, topic string, payload map[string]any) {
	if payload == nil {
		payload = map[string]any{}
	}
	payload["project_id"] = projectID
	ctx.Emit("calls."+topic, payload)
}

func schemaObject(props map[string]any, required []string) map[string]any {
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func randomToken() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("rand.Read: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "room"
	}
	return s
}

func strArg(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func intArg(args map[string]any, key string, def int) int {
	switch v := args[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return def
}

func int64Arg(args map[string]any, key string) int64 {
	switch v := args[key].(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return n
	}
	return 0
}

func boolArg(args map[string]any, key string, def bool) bool {
	if v, ok := args[key].(bool); ok {
		return v
	}
	return def
}

func floatArg(args map[string]any, key string, def float64) float64 {
	switch v := args[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case string:
		if f, err := strconv.ParseFloat(strings.TrimSpace(v), 64); err == nil {
			return f
		}
	}
	return def
}

func nullStr(s string) sql.NullString {
	return sql.NullString{String: s, Valid: strings.TrimSpace(s) != ""}
}

func nullInt64(n int64) sql.NullInt64 {
	return sql.NullInt64{Int64: n, Valid: n != 0}
}

func metadataArg(args map[string]any, key string) (string, error) {
	if v, ok := args[key]; ok && v != nil {
		switch vv := v.(type) {
		case string:
			if strings.TrimSpace(vv) == "" {
				return "{}", nil
			}
			if !json.Valid([]byte(vv)) {
				return "", fmt.Errorf("%s must be valid JSON when string", key)
			}
			return vv, nil
		default:
			raw, err := json.Marshal(vv)
			if err != nil {
				return "", err
			}
			return string(raw), nil
		}
	}
	return "{}", nil
}

func validateKind(kind string) string {
	switch kind {
	case "agent", "service":
		return kind
	default:
		return "human"
	}
}

func validateRole(role string) string {
	switch role {
	case "host", "observer":
		return role
	default:
		return "guest"
	}
}

func validateMessageKind(kind string) string {
	switch kind {
	case "system", "note":
		return kind
	default:
		return "chat"
	}
}

func validateVisibility(v string) string {
	switch v {
	case "private", "internal":
		return v
	default:
		return "room"
	}
}

func validateTrackKind(kind string) (string, error) {
	switch kind {
	case "audio", "video", "screen":
		return kind, nil
	default:
		return "", fmt.Errorf("track kind must be audio|video|screen, got %q", kind)
	}
}

func nowRFC3339() string { return time.Now().UTC().Format(time.RFC3339) }

func httpJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func withDeadline(parent context.Context, d time.Duration) (context.Context, context.CancelFunc) {
	if d <= 0 {
		return parent, func() {}
	}
	return context.WithTimeout(parent, d)
}
