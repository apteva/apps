// Telephony v0.1 — outbound voice calls via Twilio, bridged to apteva
// realtime threads.
//
// Architecture:
//   - Manifest declares one integration dep: carrier (required,
//     kind=integration, compatible_slugs=[twilio]).
//   - Agent invokes telephony_place_call(to, directive). The app:
//       1. Reads the Twilio connection's phone_number (From=).
//       2. Spawns a realtime thread in core via SDK
//          (platform.realtime.spawn), getting back an audio bridge URL.
//       3. Calls Twilio make_call with inline TwiML pointing
//          <Connect><Stream/> at this app's /media/twilio/{call_id}.
//   - When Twilio dials the callee and opens its Media Streams WS to
//     our /media endpoint, bridge_twilio.go transcodes μ-law↔PCM16
//     and pipes frames to/from core's audio WS.
//   - Status callbacks (/webhook/status/{call_id}) update DB rows and
//     kill the realtime thread on terminal carrier states.
//
// The app never speaks to the model directly. Audio flows through it
// as a transcoded pipe; conversation state and tool calls live in the
// realtime thread inside core.
package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: telephony
display_name: Telephony
version: 0.1.0
description: |
  Place outbound voice calls via Twilio. Each call runs as a realtime
  sub-thread in core; carrier audio is bridged through this sidecar.
author: Apteva
scopes: [project, global]
min_apteva_version: "0.11.0"
requires:
  permissions:
    - db.write.app
    - platform.connections.execute
    - platform.connections.read_credentials
    - platform.realtime.spawn
  integrations:
    - role: carrier
      kind: integration
      compatible_slugs: [twilio]
      capabilities: [voice.place, voice.update]
      tools:
        voice.place:  make_call
        voice.update: update_call
      events:
        - call.initiated
        - call.ringing
        - call.completed
        - call.failed
      required: true
      label: "Twilio carrier"
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: telephony_place_call,   description: "Place an outbound voice call." }
    - { name: telephony_hangup,       description: "End an active call." }
    - { name: telephony_active_calls, description: "List ongoing calls." }
  ui_panels:
    - slot: project.page
      label: Calls
      icon: phone
      entry: /ui/CallsPanel.mjs
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/telephony
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/telephony.db
  migrations: migrations/
upgrade_policy: auto-patch
`

var globalCtx *sdk.AppCtx

type App struct{}

func main() { sdk.Run(&App{}) }

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("telephony requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("telephony mounted")
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

// ─── HTTP routes ───────────────────────────────────────────────────

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		// Twilio Media Streams WS — opened by Twilio when the call connects.
		{Pattern: "/media/twilio/", Handler: a.handleTwilioMediaStream},
		// Twilio status callbacks (initiated, ringing, in-progress, completed, ...).
		{Pattern: "/webhook/status/", Handler: a.handleStatusCallback},
		// Panel data endpoint — lists active + recent calls.
		{Pattern: "/calls", Handler: a.handleListCalls},
	}
}

// ─── MCP tools ─────────────────────────────────────────────────────

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name: "telephony_place_call",
			Description: "Place an outbound voice call via Twilio. The conversation runs in a realtime sub-thread you can reach via send(id=<thread_id>, text=...) for live guidance. The thread escalates via send(id='main', ...) and reports completion via [thread:<thread_id> done]. " +
				"Args: to (E.164 phone number, required), directive (system instructions for the call, required), voice? (alloy/echo/fable/onyx/nova/shimmer, default alloy), timeout_sec? (ring timeout, default 30). " +
				"Returns: { call_id, thread_id }. Use send/done events to monitor — do not poll telephony_active_calls in a tight loop.",
			InputSchema: schemaObject(map[string]any{
				"to":          map[string]any{"type": "string", "description": "Phone number to dial in E.164 format (e.g. +14155551234)."},
				"directive":   map[string]any{"type": "string", "description": "System instructions the realtime model runs with. Should describe the persona, the goal of the call, and when to escalate to main via send(). Keep it short — 2-4 sentences."},
				"voice":       map[string]any{"type": "string", "description": "Realtime voice id.", "enum": []string{"alloy", "echo", "fable", "onyx", "nova", "shimmer"}, "default": "alloy"},
				"timeout_sec": map[string]any{"type": "integer", "description": "Ring timeout before giving up.", "default": 30, "minimum": 5, "maximum": 120},
			}, []string{"to", "directive"}),
			// Use HandlerCtx so we can pull the calling agent's id from
			// the Caller context — the realtime thread needs to spawn
			// INSIDE that agent so send/done flows between them.
			HandlerCtx: a.toolPlaceCall,
		},
		{
			Name:        "telephony_hangup",
			Description: "End an active call. Args: call_id (required). Updates Twilio to mark the call completed and kills the underlying realtime thread.",
			InputSchema: schemaObject(map[string]any{
				"call_id": map[string]any{"type": "string", "description": "Call id returned by telephony_place_call."},
			}, []string{"call_id"}),
			Handler: a.toolHangup,
		},
		{
			Name:        "telephony_active_calls",
			Description: "List currently-ongoing calls with their thread IDs, durations, and statuses. Use sparingly — prefer reacting to send()/done() events.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Handler:     a.toolActiveCalls,
		},
	}
}

// ─── telephony_place_call ──────────────────────────────────────────

func (a *App) toolPlaceCall(callerCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	caller := sdk.CallerFrom(callerCtx)
	var agentID int64
	if caller != nil {
		agentID = caller.AgentID
	}
	if agentID == 0 {
		// Without an agent id we don't know which apteva instance to
		// spawn the realtime thread under. Surface as a clear error
		// rather than silently routing to install owner's "first"
		// instance, which would be confusing in multi-instance setups.
		return mcpError("could not determine calling agent id — older platform that doesn't forward X-Apteva-Caller-Agent, or test caller without a Caller in context"), nil
	}

	to := strArg(args, "to", "")
	directive := strArg(args, "directive", "")
	voice := strArg(args, "voice", "alloy")
	timeout := intArg(args, "timeout_sec", 30)

	if !strings.HasPrefix(to, "+") {
		return mcpError("to must be E.164 format (+<countrycode><number>)"), nil
	}
	if strings.TrimSpace(directive) == "" {
		return mcpError("directive required"), nil
	}

	bound := ctx.IntegrationFor("carrier")
	if bound == nil {
		return mcpError("no carrier bound — pick Twilio in app settings"), nil
	}

	// Read the Twilio connection's phone_number for the From= field.
	// The credentials endpoint is permission-gated server-side; the
	// manifest declares platform.connections.read_credentials.
	creds, err := ctx.PlatformAPI().GetConnectionCredentials(bound.ConnectionID)
	if err != nil {
		return mcpError("read carrier credentials: " + err.Error()), nil
	}
	from := creds.Fields["phone_number"]
	if from == "" {
		return mcpError("carrier connection has no phone_number configured"), nil
	}

	callID := newCallID()
	threadID := "tel-" + callID

	// 1. Spawn the realtime thread in core. The audio bridge URL it
	//    returns is what Twilio's Media Stream will (indirectly) feed.
	rt, err := ctx.PlatformAPI().SpawnRealtimeThread(sdk.RealtimeSpawnRequest{
		AgentID:   agentID,
		ThreadID:  threadID,
		Directive: directive,
		Voice:     voice,
	})
	if err != nil {
		return mcpError("spawn realtime thread: " + err.Error()), nil
	}
	if rt.AudioBridgeURL == "" {
		_ = ctx.PlatformAPI().KillThread(threadID)
		return mcpError("realtime spawn returned no audio bridge URL"), nil
	}

	// 2. Build inline TwiML that opens a Media Stream pointed at this
	//    app's /media/twilio/{call_id}. The status callback URL on
	//    the same app receives initiated/ringing/completed pings.
	streamURL := a.publicWSStreamURL(callID)
	twiml := fmt.Sprintf(`<Response><Connect><Stream url="%s"/></Connect></Response>`, streamURL)
	statusCB := a.publicAppURL() + "/webhook/status/" + callID

	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(
		bound.ConnectionID,
		bound.ToolFor("voice.place"),
		map[string]any{
			"To":             to,
			"From":           from,
			"Twiml":          twiml,
			"StatusCallback": statusCB,
			"Timeout":        timeout,
		},
	)
	if err != nil || res == nil || !res.Success {
		_ = ctx.PlatformAPI().KillThread(threadID)
		body := ""
		if res != nil {
			body = string(res.Data)
		}
		if err != nil {
			body = err.Error() + " " + body
		}
		return mcpError("twilio make_call failed: " + body), nil
	}

	// Extract Twilio CallSid from the response. Best-effort — the call
	// is already placed; missing SID means we can't hang up via the
	// app, but the call itself proceeds.
	var twResp struct {
		SID string `json:"sid"`
	}
	_ = json.Unmarshal(res.Data, &twResp)

	// 3. Persist mapping. Connect the Twilio Media Stream handler
	//    (when Twilio dials in) with the audio bridge URL on core.
	if err := a.db().insertCall(callRow{
		ID:             callID,
		ThreadID:       threadID,
		CarrierSID:     twResp.SID,
		ToNumber:       to,
		FromNumber:     from,
		Directive:      directive,
		Voice:          voice,
		AudioBridgeURL: rt.AudioBridgeURL,
		Status:         "initiated",
		PlacedAt:       time.Now().UTC().Format(time.RFC3339),
		ProjectID:      currentProject(ctx),
	}); err != nil {
		ctx.Logger().Warn("persist call failed (proceeding)", "err", err)
	}

	return map[string]any{
		"content": []map[string]any{
			{"type": "text", "text": fmt.Sprintf("Calling %s. Thread: %s. The call is running — wait for send() escalations or [thread:%s done].", to, threadID, threadID)},
		},
		"_meta": map[string]any{
			"call_id":   callID,
			"thread_id": threadID,
		},
	}, nil
}

// ─── telephony_hangup ──────────────────────────────────────────────

func (a *App) toolHangup(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	callID := strArg(args, "call_id", "")
	if callID == "" {
		return mcpError("call_id required"), nil
	}
	row, err := a.db().findCall(callID)
	if err != nil || row == nil {
		return mcpError("unknown call_id"), nil
	}

	bound := ctx.IntegrationFor("carrier")
	if bound == nil {
		return mcpError("no carrier bound"), nil
	}
	if row.CarrierSID != "" {
		_, err := ctx.PlatformAPI().ExecuteIntegrationTool(
			bound.ConnectionID,
			bound.ToolFor("voice.update"),
			map[string]any{
				"CallSid": row.CarrierSID,
				"Status":  "completed",
			},
		)
		if err != nil {
			ctx.Logger().Warn("twilio update_call hangup failed (still killing thread)", "err", err)
		}
	}
	if err := ctx.PlatformAPI().KillThread(row.ThreadID); err != nil {
		ctx.Logger().Warn("kill thread failed", "err", err)
	}
	_ = a.db().updateStatus(callID, "completed", "")

	return "ok", nil
}

// ─── telephony_active_calls ────────────────────────────────────────

func (a *App) toolActiveCalls(ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	rows, err := a.db().listActive(currentProject(ctx))
	if err != nil {
		return mcpError("db error: " + err.Error()), nil
	}
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, map[string]any{
			"call_id":   r.ID,
			"thread_id": r.ThreadID,
			"to":        r.ToNumber,
			"status":    r.Status,
			"placed_at": r.PlacedAt,
			"duration":  callDuration(r),
		})
	}
	return map[string]any{"calls": out}, nil
}

// ─── webhook + panel handlers ──────────────────────────────────────

func (a *App) handleStatusCallback(w http.ResponseWriter, r *http.Request) {
	callID := strings.TrimPrefix(r.URL.Path, "/webhook/status/")
	callID = strings.TrimSuffix(callID, "/")
	if callID == "" {
		http.Error(w, "missing call_id", http.StatusBadRequest)
		return
	}
	status := r.FormValue("CallStatus")
	if status == "" {
		status = r.URL.Query().Get("CallStatus")
	}
	if status != "" {
		_ = a.db().updateStatus(callID, status, r.FormValue("ErrorMessage"))
	}
	switch status {
	case "completed", "failed", "no-answer", "busy", "canceled":
		row, _ := a.db().findCall(callID)
		if row != nil && row.ThreadID != "" && globalCtx != nil {
			_ = globalCtx.PlatformAPI().KillThread(row.ThreadID)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleListCalls(w http.ResponseWriter, r *http.Request) {
	project := r.URL.Query().Get("project_id")
	rows, err := a.db().recent(project, 100)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"calls": rows})
}

// ─── helpers ───────────────────────────────────────────────────────

func (a *App) db() *callsDB { return &callsDB{globalCtx.AppDB()} }

// publicBase resolves the externally-reachable URL the platform is
// hosting under. WhoAmI() is the live source of truth (admin-editable
// in Settings → Server); falls back to APTEVA_PUBLIC_URL env for
// older platforms / dev. Mirrors storage's publicBase() pattern.
func (a *App) publicBase() string {
	if globalCtx != nil && globalCtx.PlatformAPI() != nil {
		if id, err := globalCtx.PlatformAPI().WhoAmI(); err == nil && id != nil && id.PublicURL != "" {
			return strings.TrimRight(id.PublicURL, "/")
		}
	}
	if v := os.Getenv("APTEVA_PUBLIC_URL"); v != "" {
		return strings.TrimRight(v, "/")
	}
	return ""
}

// publicAppURL returns the app's externally-reachable base for its
// own routes. Apps live under /api/apps/<name>/... per the
// platform's MCP gateway convention.
func (a *App) publicAppURL() string {
	base := a.publicBase()
	if base == "" {
		return ""
	}
	return base + "/api/apps/telephony"
}

// publicWSStreamURL builds the wss:// URL Twilio dials for Media
// Streams. Twilio requires wss (TLS); a public_url over plain http
// won't work with a real Twilio account but is fine for local mock
// testing.
func (a *App) publicWSStreamURL(callID string) string {
	base := a.publicAppURL()
	if strings.HasPrefix(base, "https://") {
		return "wss://" + strings.TrimPrefix(base, "https://") + "/media/twilio/" + callID
	}
	return "ws://" + strings.TrimPrefix(base, "http://") + "/media/twilio/" + callID
}

func currentProject(ctx *sdk.AppCtx) string {
	return ctx.CurrentProject()
}

func callDuration(r callRow) string {
	t, err := time.Parse(time.RFC3339, r.PlacedAt)
	if err != nil {
		return ""
	}
	if r.EndedAt != "" {
		end, err := time.Parse(time.RFC3339, r.EndedAt)
		if err == nil {
			return end.Sub(t).Round(time.Second).String()
		}
	}
	return time.Since(t).Round(time.Second).String()
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func mcpError(msg string) map[string]any {
	return map[string]any{
		"isError": true,
		"content": []map[string]any{{"type": "text", "text": msg}},
	}
}

func schemaObject(props map[string]any, required []string) map[string]any {
	s := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func strArg(m map[string]any, key, def string) string {
	if v, ok := m[key].(string); ok && v != "" {
		return v
	}
	return def
}

func intArg(m map[string]any, key string, def int) int {
	switch v := m[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	}
	return def
}

// newCallID returns a short, sortable id. Time-prefixed so DB scans
// in time order are cheap.
func newCallID() string {
	return fmt.Sprintf("%d-%06x", time.Now().UnixNano()/1e6, randomU24())
}

func randomU24() uint32 {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return uint32(b[0])<<16 | uint32(b[1])<<8 | uint32(b[2])
}

// ─── DB layer ──────────────────────────────────────────────────────

type callRow struct {
	ID             string
	ThreadID       string
	CarrierSID     string
	ToNumber       string
	FromNumber     string
	Directive      string
	Voice          string
	AudioBridgeURL string
	Status         string
	PlacedAt       string
	AnsweredAt     string
	EndedAt        string
	ProjectID      string
	ErrorMessage   string
}

type callsDB struct{ db *sql.DB }

func (c *callsDB) insertCall(r callRow) error {
	_, err := c.db.Exec(`INSERT INTO calls
        (id, thread_id, carrier_sid, to_number, from_number, directive, voice,
         audio_bridge_url, status, placed_at, project_id)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.ThreadID, r.CarrierSID, r.ToNumber, r.FromNumber, r.Directive, r.Voice,
		r.AudioBridgeURL, r.Status, r.PlacedAt, r.ProjectID,
	)
	return err
}

func (c *callsDB) findCall(id string) (*callRow, error) {
	row := c.db.QueryRow(`SELECT id, thread_id, COALESCE(carrier_sid,''),
        to_number, from_number, directive, voice, audio_bridge_url, status,
        placed_at, COALESCE(answered_at,''), COALESCE(ended_at,''),
        project_id, COALESCE(error_message,'')
        FROM calls WHERE id = ?`, id)
	var r callRow
	if err := row.Scan(&r.ID, &r.ThreadID, &r.CarrierSID,
		&r.ToNumber, &r.FromNumber, &r.Directive, &r.Voice, &r.AudioBridgeURL, &r.Status,
		&r.PlacedAt, &r.AnsweredAt, &r.EndedAt, &r.ProjectID, &r.ErrorMessage); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &r, nil
}

// findByThreadID resolves the call row from a thread id. Used by the
// audio bridge handler to look up the AudioBridgeURL given a call id
// stamped in the WS path.
func (c *callsDB) findByThreadID(threadID string) (*callRow, error) {
	row := c.db.QueryRow(`SELECT id FROM calls WHERE thread_id = ?`, threadID)
	var id string
	if err := row.Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return c.findCall(id)
}

func (c *callsDB) updateStatus(id, status, errMsg string) error {
	end := ""
	switch status {
	case "completed", "failed", "no-answer", "busy", "canceled":
		end = time.Now().UTC().Format(time.RFC3339)
	}
	_, err := c.db.Exec(`UPDATE calls SET status = ?, error_message = ?,
        ended_at = COALESCE(NULLIF(?, ''), ended_at) WHERE id = ?`,
		status, errMsg, end, id)
	return err
}

func (c *callsDB) listActive(project string) ([]callRow, error) {
	return c.listWhere(`status IN ('initiated','ringing','in-progress','answered') AND (? = '' OR project_id = ?) ORDER BY placed_at DESC`,
		project, project)
}

func (c *callsDB) recent(project string, limit int) ([]callRow, error) {
	return c.listWhere(`(? = '' OR project_id = ?) ORDER BY placed_at DESC LIMIT `+fmt.Sprintf("%d", limit),
		project, project)
}

func (c *callsDB) listWhere(where string, argv ...any) ([]callRow, error) {
	rows, err := c.db.Query(`SELECT id, thread_id, COALESCE(carrier_sid,''),
        to_number, from_number, directive, voice, audio_bridge_url, status,
        placed_at, COALESCE(answered_at,''), COALESCE(ended_at,''),
        project_id, COALESCE(error_message,'')
        FROM calls WHERE `+where, argv...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []callRow
	for rows.Next() {
		var r callRow
		if err := rows.Scan(&r.ID, &r.ThreadID, &r.CarrierSID,
			&r.ToNumber, &r.FromNumber, &r.Directive, &r.Voice, &r.AudioBridgeURL, &r.Status,
			&r.PlacedAt, &r.AnsweredAt, &r.EndedAt, &r.ProjectID, &r.ErrorMessage); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}
