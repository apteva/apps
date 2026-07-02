// Telephony v0.1 — outbound voice calls bridged to apteva
// realtime threads.
//
// Architecture:
//   - Manifest declares one integration dep: carrier (required,
//     kind=integration, compatible_slugs=[twilio, telnyx, plivo,
//     signalwire, vonage]).
//   - Agent invokes telephony_place_call(to, directive). The app:
//     1. Reads the carrier connection's phone_number (From=).
//     2. Spawns a realtime thread in core via SDK
//     (platform.realtime.spawn), getting back an audio bridge URL.
//     3. Calls the carrier API with a media stream pointing at this
//     app's provider-specific /media/{carrier}/{call_id}.
//   - When the carrier dials the callee and opens its media WS to our
//     /media endpoint, the bridge transcodes/resamples carrier audio
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
version: 0.1.3
description: |
  Place and receive voice calls via programmable carriers. Calls run as realtime
  sub-threads in core; carrier audio is bridged through this sidecar.
author: Apteva
icon: https://raw.githubusercontent.com/apteva/apps/main/mcp/telephony/icon.svg
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
      compatible_slugs: [twilio, telnyx, plivo, signalwire, vonage]
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
      label: "Voice carrier"
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: telephony_place_call,   description: "Place an outbound voice call." }
    - { name: telephony_answer_call,  description: "Answer an inbound call by spawning a realtime thread." }
    - { name: telephony_reject_call,  description: "Reject a pending inbound call." }
    - { name: telephony_routes_create, description: "Create an inbound phone-number route to an agent." }
    - { name: telephony_routes_configure_carrier, description: "Configure the carrier phone number webhook for an inbound route." }
    - { name: telephony_routes_list,  description: "List inbound call routes." }
    - { name: telephony_pending_calls, description: "List pending inbound calls." }
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
		// Carrier media stream WS — opened by the carrier when the call connects.
		{Pattern: "/media/twilio/", Handler: a.handleTwilioMediaStream},
		{Pattern: "/media/signalwire/", Handler: a.handleSignalWireMediaStream},
		{Pattern: "/media/telnyx/", Handler: a.handleTelnyxMediaStream},
		{Pattern: "/media/plivo/", Handler: a.handlePlivoMediaStream},
		{Pattern: "/media/vonage/", Handler: a.handleVonageMediaStream},
		// Plivo fetches XML call control from an answer_url.
		{Pattern: "/xml/plivo/", Handler: a.handlePlivoXML},
		// Carrier status callbacks (initiated, ringing, in-progress, completed, ...).
		{Pattern: "/webhook/status/", Handler: a.handleStatusCallback},
		// Twilio inbound call control. The route id maps a phone number
		// to the agent that should receive the incoming-call event.
		{Pattern: "/inbound/twilio/", Handler: a.handleTwilioInbound},
		// Panel data endpoint — lists active + recent calls.
		{Pattern: "/calls", Handler: a.handleListCalls},
		// Panel action endpoint.
		{Pattern: "/calls/", Handler: a.handleCallAction},
	}
}

// ─── MCP tools ─────────────────────────────────────────────────────

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name: "telephony_place_call",
			Description: "Place an outbound voice call via the bound carrier. Telephony spawns a realtime sub-thread and bridges carrier audio into it. " +
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
			Name:        "telephony_routes_create",
			Description: "Create an inbound route from a carrier phone number to the calling agent. After creating it, call telephony_routes_configure_carrier(route_id) to set the provider webhook. Args: phone_number? (defaults to bound connection phone_number), phone_number_sid? (Twilio PN SID if known), hold_prompt?, timeout_sec?.",
			InputSchema: schemaObject(map[string]any{
				"phone_number":     map[string]any{"type": "string", "description": "Inbound number in E.164. Defaults to bound carrier connection phone_number."},
				"phone_number_sid": map[string]any{"type": "string", "description": "Carrier phone number id. For Twilio this is PNxxxxxxxx; can be auto-discovered if omitted."},
				"hold_prompt":      map[string]any{"type": "string", "description": "Short prompt callers hear while the agent decides whether to answer."},
				"timeout_sec":      map[string]any{"type": "integer", "description": "How long to hold before ending if no answer.", "default": 60, "minimum": 10, "maximum": 300},
			}, nil),
			HandlerCtx: a.toolRoutesCreate,
		},
		{
			Name:        "telephony_routes_configure_carrier",
			Description: "Configure the bound carrier phone number webhook for an inbound route. Currently implemented for Twilio update_phone_number. Args: route_id (required).",
			InputSchema: schemaObject(map[string]any{
				"route_id": map[string]any{"type": "string", "description": "Route id returned by telephony_routes_create."},
			}, []string{"route_id"}),
			HandlerCtx: a.toolRoutesConfigureCarrier,
		},
		{
			Name:        "telephony_routes_list",
			Description: "List inbound phone-number routes for this app/project.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Handler:     a.toolRoutesList,
		},
		{
			Name:        "telephony_pending_calls",
			Description: "List pending inbound calls for the calling agent. Prefer reacting to the incoming-call event; this is for recovery/inspection.",
			InputSchema: schemaObject(map[string]any{}, nil),
			HandlerCtx:  a.toolPendingCalls,
		},
		{
			Name:        "telephony_answer_call",
			Description: "Answer a pending inbound call by spawning a realtime sub-thread and redirecting the carrier call into it. Args: call_id, directive, voice?.",
			InputSchema: schemaObject(map[string]any{
				"call_id":   map[string]any{"type": "string", "description": "Pending inbound call id from the event."},
				"directive": map[string]any{"type": "string", "description": "System instructions for the realtime call thread."},
				"voice":     map[string]any{"type": "string", "description": "Realtime voice id.", "enum": []string{"alloy", "echo", "fable", "onyx", "nova", "shimmer"}, "default": "alloy"},
			}, []string{"call_id", "directive"}),
			HandlerCtx: a.toolAnswerCall,
		},
		{
			Name:        "telephony_reject_call",
			Description: "Reject or end a pending inbound call. Args: call_id (required), reason?.",
			InputSchema: schemaObject(map[string]any{
				"call_id": map[string]any{"type": "string", "description": "Pending inbound call id."},
				"reason":  map[string]any{"type": "string", "description": "Optional reason stored on the call."},
			}, []string{"call_id"}),
			HandlerCtx: a.toolRejectCall,
		},
		{
			Name:        "telephony_hangup",
			Description: "End an active call. Args: call_id (required). Hangs up the carrier call and kills the underlying realtime thread.",
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
		return mcpError("no carrier bound — pick Twilio, Telnyx, Plivo, SignalWire, or Vonage in app settings"), nil
	}

	// Read the carrier connection's phone_number for the From= field.
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

	carrier, err := a.carrierFor(bound, creds.Slug, creds.Fields)
	if err != nil {
		_ = ctx.PlatformAPI().KillThread(threadID)
		return mcpError(err.Error()), nil
	}

	// Persist before placing the carrier call: Telnyx/Plivo/Vonage can
	// connect their media WebSocket immediately after the API accepts
	// the call, so the bridge route must already be able to resolve
	// callID -> audio_bridge_url.
	if err := a.db().insertCall(callRow{
		ID:                  callID,
		ThreadID:            threadID,
		Direction:           "outbound",
		AgentID:             agentID,
		CarrierSlug:         carrier.Slug(),
		CarrierConnectionID: bound.ConnectionID,
		ToNumber:            to,
		FromNumber:          from,
		Directive:           directive,
		Voice:               voice,
		AudioBridgeURL:      rt.AudioBridgeURL,
		Status:              "initiated",
		PlacedAt:            time.Now().UTC().Format(time.RFC3339),
		ProjectID:           currentProject(ctx),
	}); err != nil {
		_ = ctx.PlatformAPI().KillThread(threadID)
		return mcpError("persist call before carrier placement: " + err.Error()), nil
	}

	placed, err := carrier.Place(ctx, carrierPlaceRequest{
		CallID:         callID,
		To:             to,
		From:           from,
		TimeoutSec:     timeout,
		AudioBridgeURL: rt.AudioBridgeURL,
	})
	if err != nil {
		_ = a.db().updateStatus(callID, "failed", err.Error())
		_ = ctx.PlatformAPI().KillThread(threadID)
		return mcpError(err.Error()), nil
	}
	if placed != nil && placed.CarrierSID != "" {
		if err := a.db().updateCarrierSID(callID, placed.CarrierSID); err != nil {
			ctx.Logger().Warn("persist carrier call id failed (proceeding)", "err", err)
		}
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
	if msg := a.hangupCall(ctx, callID); msg != "" {
		return mcpError(msg), nil
	}

	return "ok", nil
}

func (a *App) hangupCall(ctx *sdk.AppCtx, callID string) string {
	row, err := a.db().findCall(callID)
	if err != nil || row == nil {
		return "unknown call_id"
	}

	bound := ctx.IntegrationFor("carrier")
	if bound == nil {
		return "no carrier bound"
	}
	if row.CarrierSID != "" {
		carrier, err := a.carrierForRow(ctx, bound, row)
		if err != nil {
			ctx.Logger().Warn("resolve carrier hangup adapter failed (still killing thread)", "err", err)
		} else if err := carrier.Hangup(ctx, row); err != nil {
			ctx.Logger().Warn("carrier hangup failed (still killing thread)", "err", err)
		}
	}
	if err := a.killCallThread(ctx, row); err != nil {
		ctx.Logger().Warn("kill thread failed", "err", err)
	}
	_ = a.db().updateStatus(callID, "completed", "")

	return ""
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
			"carrier":   r.CarrierSlug,
			"to":        r.ToNumber,
			"status":    r.Status,
			"placed_at": r.PlacedAt,
			"duration":  callDuration(r),
		})
	}
	return map[string]any{"calls": out}, nil
}

// ─── inbound routing tools ────────────────────────────────────────

func (a *App) toolRoutesCreate(callerCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	agentID := callerAgentID(callerCtx)
	if agentID == 0 {
		return mcpError("could not determine calling agent id"), nil
	}
	bound := ctx.IntegrationFor("carrier")
	if bound == nil {
		return mcpError("no carrier bound"), nil
	}
	creds, err := ctx.PlatformAPI().GetConnectionCredentials(bound.ConnectionID)
	if err != nil {
		return mcpError("read carrier credentials: " + err.Error()), nil
	}
	phone := strArg(args, "phone_number", creds.Fields["phone_number"])
	if phone == "" {
		return mcpError("phone_number required, or carrier connection must define phone_number"), nil
	}
	slug := strings.ToLower(firstNonEmpty(creds.Slug, bound.AppSlug))
	if slug == "" {
		return mcpError("could not determine carrier slug"), nil
	}
	if slug != "twilio" {
		return mcpError("inbound call routing is currently implemented for Twilio only"), nil
	}
	holdPrompt := strArg(args, "hold_prompt", "Please hold while I connect you.")
	timeout := intArg(args, "timeout_sec", 60)
	if timeout < 10 {
		timeout = 10
	}
	if timeout > 300 {
		timeout = 300
	}
	now := time.Now().UTC().Format(time.RFC3339)
	route := routeRow{
		ID:                  "route-" + newCallID(),
		ProjectID:           currentProject(ctx),
		CarrierSlug:         slug,
		CarrierConnectionID: bound.ConnectionID,
		PhoneNumber:         phone,
		PhoneNumberSID:      strArg(args, "phone_number_sid", ""),
		AgentID:             agentID,
		Enabled:             true,
		HoldPrompt:          holdPrompt,
		TimeoutSec:          timeout,
		Secret:              newSecret(),
		CreatedAt:           now,
		UpdatedAt:           now,
	}
	if err := a.db().insertRoute(route); err != nil {
		return mcpError("persist inbound route: " + err.Error()), nil
	}
	return map[string]any{
		"route":       routePublic(a, route),
		"inbound_url": a.inboundRouteURL(route),
		"next":        "Call telephony_routes_configure_carrier with route_id to set the carrier webhook.",
	}, nil
}

func (a *App) toolRoutesConfigureCarrier(_ context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	routeID := strArg(args, "route_id", "")
	if routeID == "" {
		return mcpError("route_id required"), nil
	}
	route, err := a.db().findRoute(routeID)
	if err != nil {
		return mcpError("load route: " + err.Error()), nil
	}
	if route == nil {
		return mcpError("unknown route_id"), nil
	}
	if route.CarrierSlug != "twilio" {
		return mcpError("carrier webhook configuration is currently implemented for Twilio routes only"), nil
	}
	sid := route.PhoneNumberSID
	if sid == "" {
		var err error
		sid, err = a.findTwilioPhoneNumberSID(ctx, route)
		if err != nil {
			return mcpError("find Twilio phone number SID: " + err.Error()), nil
		}
		if sid == "" {
			return mcpError("could not find Twilio phone number SID for " + route.PhoneNumber), nil
		}
		_ = a.db().updateRoutePhoneSID(route.ID, sid)
		route.PhoneNumberSID = sid
	}
	_, err = executeCarrierTool(ctx, route.CarrierConnectionID, "update_phone_number", map[string]any{
		"PhoneNumberSid": sid,
		"VoiceUrl":       a.inboundRouteURL(*route),
	})
	if err != nil {
		return mcpError("configure Twilio webhook: " + err.Error()), nil
	}
	return map[string]any{
		"ok":          true,
		"route":       routePublic(a, *route),
		"inbound_url": a.inboundRouteURL(*route),
	}, nil
}

func (a *App) toolRoutesList(ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	routes, err := a.db().listRoutes(currentProject(ctx))
	if err != nil {
		return mcpError("db error: " + err.Error()), nil
	}
	out := make([]map[string]any, 0, len(routes))
	for _, r := range routes {
		out = append(out, routePublic(a, r))
	}
	return map[string]any{"routes": out}, nil
}

func (a *App) toolPendingCalls(callerCtx context.Context, ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	agentID := callerAgentID(callerCtx)
	rows, err := a.db().listPending(agentID, currentProject(ctx))
	if err != nil {
		return mcpError("db error: " + err.Error()), nil
	}
	return map[string]any{"calls": callsPublic(rows)}, nil
}

func (a *App) toolAnswerCall(callerCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	agentID := callerAgentID(callerCtx)
	if agentID == 0 {
		return mcpError("could not determine calling agent id"), nil
	}
	callID := strArg(args, "call_id", "")
	directive := strArg(args, "directive", "")
	voice := strArg(args, "voice", "alloy")
	if callID == "" || strings.TrimSpace(directive) == "" {
		return mcpError("call_id and directive required"), nil
	}
	row, err := a.db().findCall(callID)
	if err != nil {
		return mcpError("load call: " + err.Error()), nil
	}
	if row == nil {
		return mcpError("unknown call_id"), nil
	}
	if row.Direction != "inbound" || row.Status != "pending" {
		return mcpError("call is not a pending inbound call"), nil
	}
	if row.AgentID != 0 && row.AgentID != agentID {
		return mcpError("this call is routed to another agent"), nil
	}
	if row.CarrierSlug != "twilio" {
		return mcpError("answer redirect is currently implemented for Twilio inbound calls only"), nil
	}

	threadID := "tel-" + callID
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
	if err := a.db().attachCall(callID, threadID, rt.AudioBridgeURL); err != nil {
		_ = ctx.PlatformAPI().KillThread(threadID)
		return mcpError("persist call answer: " + err.Error()), nil
	}
	row.ThreadID = threadID
	row.AudioBridgeURL = rt.AudioBridgeURL
	row.Status = "answered"

	twiml := fmt.Sprintf(`<Response><Connect><Stream url="%s"/></Connect></Response>`, xmlEscape(a.publicWSStreamURL("twilio", callID)))
	if _, err := executeCarrierTool(ctx, row.CarrierConnectionID, "update_call", map[string]any{
		"CallSid": row.CarrierSID,
		"Twiml":   twiml,
	}); err != nil {
		_ = ctx.PlatformAPI().KillThread(threadID)
		return mcpError("redirect Twilio call to media stream: " + err.Error()), nil
	}
	return map[string]any{
		"ok":        true,
		"call_id":   callID,
		"thread_id": threadID,
	}, nil
}

func (a *App) toolRejectCall(callerCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	agentID := callerAgentID(callerCtx)
	callID := strArg(args, "call_id", "")
	if callID == "" {
		return mcpError("call_id required"), nil
	}
	row, err := a.db().findCall(callID)
	if err != nil {
		return mcpError("load call: " + err.Error()), nil
	}
	if row == nil {
		return mcpError("unknown call_id"), nil
	}
	if row.AgentID != 0 && agentID != 0 && row.AgentID != agentID {
		return mcpError("this call is routed to another agent"), nil
	}
	if row.CarrierSID != "" && row.CarrierSlug == "twilio" {
		_, _ = executeCarrierTool(ctx, row.CarrierConnectionID, "update_call", map[string]any{
			"CallSid": row.CarrierSID,
			"Status":  "completed",
		})
	}
	reason := strArg(args, "reason", "rejected by agent")
	_ = a.db().updateStatus(callID, "canceled", reason)
	return map[string]any{"ok": true, "call_id": callID}, nil
}

func (a *App) findTwilioPhoneNumberSID(ctx *sdk.AppCtx, route *routeRow) (string, error) {
	data, err := executeCarrierTool(ctx, route.CarrierConnectionID, "list_phone_numbers", map[string]any{
		"PhoneNumber": route.PhoneNumber,
		"PageSize":    20,
	})
	if err != nil {
		return "", err
	}
	var out struct {
		IncomingPhoneNumbers []struct {
			SID         string `json:"sid"`
			PhoneNumber string `json:"phone_number"`
		} `json:"incoming_phone_numbers"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", err
	}
	for _, n := range out.IncomingPhoneNumbers {
		if n.PhoneNumber == route.PhoneNumber {
			return n.SID, nil
		}
	}
	if len(out.IncomingPhoneNumbers) == 1 {
		return out.IncomingPhoneNumbers[0].SID, nil
	}
	return "", nil
}

func (a *App) killCallThread(ctx *sdk.AppCtx, row *callRow) error {
	if row == nil || row.ThreadID == "" || strings.HasPrefix(row.ThreadID, "pending-") {
		return nil
	}
	return ctx.PlatformAPI().KillThread(row.ThreadID)
}

// ─── webhook + panel handlers ──────────────────────────────────────

func (a *App) handleStatusCallback(w http.ResponseWriter, r *http.Request) {
	callID := strings.TrimPrefix(r.URL.Path, "/webhook/status/")
	callID = strings.TrimSuffix(callID, "/")
	if callID == "" {
		http.Error(w, "missing call_id", http.StatusBadRequest)
		return
	}
	status, errMsg := callbackStatus(r)
	if status != "" {
		_ = a.db().updateStatus(callID, status, errMsg)
	}
	switch status {
	case "completed", "failed", "no-answer", "busy", "canceled":
		row, _ := a.db().findCall(callID)
		if row != nil && row.ThreadID != "" && globalCtx != nil {
			_ = a.killCallThread(globalCtx, row)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleTwilioInbound(w http.ResponseWriter, r *http.Request) {
	path := strings.Trim(strings.TrimPrefix(r.URL.Path, "/inbound/twilio/"), "/")
	parts := strings.Split(path, "/")
	if len(parts) == 0 || parts[0] == "" {
		http.Error(w, "missing route_id", http.StatusBadRequest)
		return
	}
	routeID := parts[0]
	if len(parts) > 1 && parts[1] == "wait" {
		a.handleTwilioInboundWait(w, r, routeID)
		return
	}

	route, err := a.db().findRoute(routeID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if route == nil || !route.Enabled || route.Secret == "" || r.URL.Query().Get("secret") != route.Secret {
		http.Error(w, "route not found", http.StatusNotFound)
		return
	}
	callSID := firstNonEmpty(r.FormValue("CallSid"), r.URL.Query().Get("CallSid"))
	from := firstNonEmpty(r.FormValue("From"), r.URL.Query().Get("From"))
	to := firstNonEmpty(r.FormValue("To"), r.URL.Query().Get("To"), route.PhoneNumber)
	if callSID == "" {
		http.Error(w, "missing CallSid", http.StatusBadRequest)
		return
	}
	callID := newCallID()
	now := time.Now().UTC().Format(time.RFC3339)
	if err := a.db().insertCall(callRow{
		ID:                  callID,
		ThreadID:            "pending-" + callID,
		Direction:           "inbound",
		AgentID:             route.AgentID,
		RouteID:             route.ID,
		CarrierSID:          callSID,
		CarrierSlug:         route.CarrierSlug,
		CarrierConnectionID: route.CarrierConnectionID,
		ToNumber:            to,
		FromNumber:          from,
		Directive:           "inbound pending",
		Voice:               "",
		AudioBridgeURL:      "pending",
		Status:              "pending",
		PlacedAt:            now,
		ProjectID:           route.ProjectID,
	}); err != nil {
		http.Error(w, "persist call: "+err.Error(), http.StatusInternalServerError)
		return
	}
	if globalCtx != nil && globalCtx.PlatformAPI() != nil {
		msg := fmt.Sprintf(
			"Incoming phone call. call_id=%s from=%s to=%s. To answer: call telephony_answer_call with call_id=%q and a directive for the realtime call thread. To decline: call telephony_reject_call with call_id=%q.",
			callID, from, to, callID, callID,
		)
		if err := globalCtx.PlatformAPI().SendEvent(route.AgentID, msg); err != nil {
			globalCtx.Logger().Warn("send incoming call event failed", "route", route.ID, "agent", route.AgentID, "err", err)
		}
	}
	writeTwilioHold(w, a.holdText(*route), a.twilioWaitURL(*route, callID))
}

func (a *App) handleTwilioInboundWait(w http.ResponseWriter, r *http.Request, routeID string) {
	route, err := a.db().findRoute(routeID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if route == nil || route.Secret == "" || r.URL.Query().Get("secret") != route.Secret {
		http.Error(w, "route not found", http.StatusNotFound)
		return
	}
	callID := r.URL.Query().Get("call_id")
	row, err := a.db().findCall(callID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if row == nil {
		writeTwilioSayHangup(w, "This call can no longer be connected.")
		return
	}
	switch row.Status {
	case "pending":
		if callTimedOut(*route, *row) {
			_ = a.db().updateStatus(row.ID, "no-answer", "agent did not answer before route timeout")
			writeTwilioSayHangup(w, "No one is available to take this call.")
			return
		}
		writeTwilioHold(w, a.holdText(*route), a.twilioWaitURL(*route, row.ID))
	case "answered", "in-progress", "initiated", "ringing":
		// The answer tool updates the live Twilio call directly. If a
		// pending redirect was already queued, keep the call open briefly.
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<Response><Pause length="1"/></Response>`))
	default:
		writeTwilioSayHangup(w, "This call has ended.")
	}
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

func (a *App) handleCallAction(w http.ResponseWriter, r *http.Request) {
	parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
	if len(parts) != 3 || parts[0] != "calls" || parts[2] != "hangup" {
		http.NotFound(w, r)
		return
	}
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	if globalCtx == nil {
		http.Error(w, "app context unavailable", http.StatusServiceUnavailable)
		return
	}
	if msg := a.hangupCall(globalCtx, parts[1]); msg != "" {
		http.Error(w, msg, http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
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

// publicWSStreamURL builds the wss:// URL a carrier dials for Media
// Streams. Real carriers require wss (TLS); a public_url over plain
// http is only useful for local mock testing.
func (a *App) publicWSStreamURL(provider, callID string) string {
	base := a.publicAppURL()
	if strings.HasPrefix(base, "https://") {
		return "wss://" + strings.TrimPrefix(base, "https://") + "/media/" + provider + "/" + callID
	}
	return "ws://" + strings.TrimPrefix(base, "http://") + "/media/" + provider + "/" + callID
}

func (a *App) inboundRouteURL(route routeRow) string {
	return fmt.Sprintf("%s/inbound/%s/%s?secret=%s",
		a.publicAppURL(),
		route.CarrierSlug,
		route.ID,
		route.Secret,
	)
}

func (a *App) twilioWaitURL(route routeRow, callID string) string {
	return fmt.Sprintf("%s/inbound/twilio/%s/wait?secret=%s&call_id=%s",
		a.publicAppURL(),
		route.ID,
		route.Secret,
		callID,
	)
}

func (a *App) holdText(route routeRow) string {
	if strings.TrimSpace(route.HoldPrompt) != "" {
		return route.HoldPrompt
	}
	return "Please hold while I connect you."
}

func writeTwilioHold(w http.ResponseWriter, prompt, redirectURL string) {
	w.Header().Set("Content-Type", "application/xml")
	_, _ = fmt.Fprintf(w, `<Response><Say>%s</Say><Pause length="5"/><Redirect method="POST">%s</Redirect></Response>`,
		xmlEscape(prompt),
		xmlEscape(redirectURL),
	)
}

func writeTwilioSayHangup(w http.ResponseWriter, prompt string) {
	w.Header().Set("Content-Type", "application/xml")
	_, _ = fmt.Fprintf(w, `<Response><Say>%s</Say><Hangup/></Response>`, xmlEscape(prompt))
}

func callTimedOut(route routeRow, row callRow) bool {
	timeout := route.TimeoutSec
	if timeout <= 0 {
		timeout = 60
	}
	start, err := time.Parse(time.RFC3339, row.PlacedAt)
	if err != nil {
		return false
	}
	return time.Since(start) > time.Duration(timeout)*time.Second
}

func currentProject(ctx *sdk.AppCtx) string {
	return ctx.CurrentProject()
}

func callerAgentID(ctx context.Context) int64 {
	caller := sdk.CallerFrom(ctx)
	if caller == nil {
		return 0
	}
	return caller.AgentID
}

func routePublic(a *App, r routeRow) map[string]any {
	return map[string]any{
		"id":                    r.ID,
		"project_id":            r.ProjectID,
		"carrier":               r.CarrierSlug,
		"carrier_connection_id": r.CarrierConnectionID,
		"phone_number":          r.PhoneNumber,
		"phone_number_sid":      r.PhoneNumberSID,
		"agent_id":              r.AgentID,
		"enabled":               r.Enabled,
		"hold_prompt":           r.HoldPrompt,
		"timeout_sec":           r.TimeoutSec,
		"inbound_url":           a.inboundRouteURL(r),
		"created_at":            r.CreatedAt,
		"updated_at":            r.UpdatedAt,
	}
}

func callsPublic(rows []callRow) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, map[string]any{
			"call_id":     r.ID,
			"thread_id":   r.ThreadID,
			"direction":   r.Direction,
			"agent_id":    r.AgentID,
			"route_id":    r.RouteID,
			"carrier":     r.CarrierSlug,
			"to":          r.ToNumber,
			"from":        r.FromNumber,
			"status":      r.Status,
			"placed_at":   r.PlacedAt,
			"answered_at": r.AnsweredAt,
			"duration":    callDuration(r),
		})
	}
	return out
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

func callbackStatus(r *http.Request) (string, string) {
	status := firstNonEmpty(
		r.FormValue("CallStatus"),
		r.FormValue("Status"),
		r.FormValue("status"),
		r.FormValue("Event"),
		r.FormValue("event"),
		r.URL.Query().Get("CallStatus"),
		r.URL.Query().Get("Status"),
		r.URL.Query().Get("status"),
	)
	errMsg := firstNonEmpty(r.FormValue("ErrorMessage"), r.FormValue("error"), r.FormValue("StatusReason"))
	if status == "" && strings.Contains(r.Header.Get("Content-Type"), "json") {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
			status = firstString(body, "CallStatus", "Status", "status", "Event", "event")
			errMsg = firstNonEmpty(errMsg, firstString(body, "ErrorMessage", "error", "reason", "StatusReason"))
		}
	}
	return normalizeCallStatus(status), errMsg
}

func normalizeCallStatus(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "answered", "in-progress", "in_progress", "started", "ringing":
		if strings.EqualFold(status, "started") {
			return "in-progress"
		}
		return strings.ToLower(strings.TrimSpace(status))
	case "completed", "complete", "disconnected", "stopped":
		return "completed"
	case "cancelled":
		return "canceled"
	case "unanswered", "no_answer":
		return "no-answer"
	case "timeout", "timed_out":
		return "no-answer"
	case "rejected":
		return "failed"
	case "busy", "failed", "canceled", "no-answer":
		return strings.ToLower(strings.TrimSpace(status))
	default:
		return strings.ToLower(strings.TrimSpace(status))
	}
}

func firstString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if v, ok := m[key].(string); ok && v != "" {
			return v
		}
	}
	return ""
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
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

func newSecret() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	return fmt.Sprintf("%x", b[:])
}

func randomU24() uint32 {
	var b [3]byte
	_, _ = rand.Read(b[:])
	return uint32(b[0])<<16 | uint32(b[1])<<8 | uint32(b[2])
}

// ─── DB layer ──────────────────────────────────────────────────────

type callRow struct {
	ID                  string
	ThreadID            string
	Direction           string
	AgentID             int64
	RouteID             string
	CarrierSID          string
	CarrierSlug         string
	CarrierConnectionID int64
	ToNumber            string
	FromNumber          string
	Directive           string
	Voice               string
	AudioBridgeURL      string
	Status              string
	PlacedAt            string
	AnsweredAt          string
	EndedAt             string
	ProjectID           string
	ErrorMessage        string
}

type routeRow struct {
	ID                  string
	ProjectID           string
	CarrierSlug         string
	CarrierConnectionID int64
	PhoneNumber         string
	PhoneNumberSID      string
	AgentID             int64
	Enabled             bool
	HoldPrompt          string
	TimeoutSec          int
	Secret              string
	CreatedAt           string
	UpdatedAt           string
}

type callsDB struct{ db *sql.DB }

func (c *callsDB) insertCall(r callRow) error {
	_, err := c.db.Exec(`INSERT INTO calls
        (id, thread_id, direction, agent_id, route_id, carrier_sid, carrier_slug, carrier_connection_id,
         to_number, from_number, directive, voice, audio_bridge_url, status,
         placed_at, project_id)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.ThreadID, r.Direction, r.AgentID, r.RouteID, r.CarrierSID, r.CarrierSlug, r.CarrierConnectionID,
		r.ToNumber, r.FromNumber, r.Directive, r.Voice, r.AudioBridgeURL,
		r.Status, r.PlacedAt, r.ProjectID,
	)
	return err
}

func (c *callsDB) findCall(id string) (*callRow, error) {
	row := c.db.QueryRow(`SELECT id, thread_id,
        COALESCE(direction,'outbound'), COALESCE(agent_id,0), COALESCE(route_id,''),
        COALESCE(carrier_sid,''),
        COALESCE(carrier_slug,'twilio'), COALESCE(carrier_connection_id,0),
        to_number, from_number, directive, voice, audio_bridge_url, status,
        placed_at, COALESCE(answered_at,''), COALESCE(ended_at,''),
        project_id, COALESCE(error_message,'')
        FROM calls WHERE id = ?`, id)
	var r callRow
	if err := row.Scan(&r.ID, &r.ThreadID, &r.Direction, &r.AgentID, &r.RouteID, &r.CarrierSID,
		&r.CarrierSlug, &r.CarrierConnectionID,
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

func (c *callsDB) updateCarrierSID(id, carrierSID string) error {
	_, err := c.db.Exec(`UPDATE calls SET carrier_sid = ? WHERE id = ?`, carrierSID, id)
	return err
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

func (c *callsDB) attachCall(id, threadID, audioBridgeURL string) error {
	now := time.Now().UTC().Format(time.RFC3339)
	_, err := c.db.Exec(`UPDATE calls
        SET thread_id = ?, audio_bridge_url = ?, status = 'answered', answered_at = COALESCE(answered_at, ?)
        WHERE id = ?`, threadID, audioBridgeURL, now, id)
	return err
}

func (c *callsDB) listActive(project string) ([]callRow, error) {
	return c.listWhere(`status IN ('initiated','ringing','in-progress','answered','pending') AND (? = '' OR project_id = ?) ORDER BY placed_at DESC`,
		project, project)
}

func (c *callsDB) listPending(agentID int64, project string) ([]callRow, error) {
	return c.listWhere(`direction = 'inbound' AND status = 'pending' AND (? = 0 OR agent_id = ?) AND (? = '' OR project_id = ?) ORDER BY placed_at DESC`,
		agentID, agentID, project, project)
}

func (c *callsDB) recent(project string, limit int) ([]callRow, error) {
	return c.listWhere(`(? = '' OR project_id = ?) ORDER BY placed_at DESC LIMIT `+fmt.Sprintf("%d", limit),
		project, project)
}

func (c *callsDB) listWhere(where string, argv ...any) ([]callRow, error) {
	rows, err := c.db.Query(`SELECT id, thread_id,
        COALESCE(direction,'outbound'), COALESCE(agent_id,0), COALESCE(route_id,''),
        COALESCE(carrier_sid,''),
        COALESCE(carrier_slug,'twilio'), COALESCE(carrier_connection_id,0),
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
		if err := rows.Scan(&r.ID, &r.ThreadID, &r.Direction, &r.AgentID, &r.RouteID, &r.CarrierSID,
			&r.CarrierSlug, &r.CarrierConnectionID,
			&r.ToNumber, &r.FromNumber, &r.Directive, &r.Voice, &r.AudioBridgeURL, &r.Status,
			&r.PlacedAt, &r.AnsweredAt, &r.EndedAt, &r.ProjectID, &r.ErrorMessage); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, nil
}

func (c *callsDB) insertRoute(r routeRow) error {
	enabled := 0
	if r.Enabled {
		enabled = 1
	}
	_, err := c.db.Exec(`INSERT INTO inbound_routes
        (id, project_id, carrier_slug, carrier_connection_id, phone_number, phone_number_sid,
         agent_id, enabled, hold_prompt, timeout_sec, secret, created_at, updated_at)
        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.ProjectID, r.CarrierSlug, r.CarrierConnectionID, r.PhoneNumber, r.PhoneNumberSID,
		r.AgentID, enabled, r.HoldPrompt, r.TimeoutSec, r.Secret, r.CreatedAt, r.UpdatedAt,
	)
	return err
}

func (c *callsDB) findRoute(id string) (*routeRow, error) {
	row := c.db.QueryRow(`SELECT id, project_id, carrier_slug, carrier_connection_id, phone_number,
        phone_number_sid, agent_id, enabled, hold_prompt, timeout_sec, secret, created_at, updated_at
        FROM inbound_routes WHERE id = ?`, id)
	var r routeRow
	var enabled int
	if err := row.Scan(&r.ID, &r.ProjectID, &r.CarrierSlug, &r.CarrierConnectionID, &r.PhoneNumber,
		&r.PhoneNumberSID, &r.AgentID, &enabled, &r.HoldPrompt, &r.TimeoutSec, &r.Secret, &r.CreatedAt, &r.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	r.Enabled = enabled != 0
	return &r, nil
}

func (c *callsDB) listRoutes(project string) ([]routeRow, error) {
	rows, err := c.db.Query(`SELECT id, project_id, carrier_slug, carrier_connection_id, phone_number,
        phone_number_sid, agent_id, enabled, hold_prompt, timeout_sec, secret, created_at, updated_at
        FROM inbound_routes WHERE (? = '' OR project_id = ?) ORDER BY created_at DESC`, project, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []routeRow
	for rows.Next() {
		var r routeRow
		var enabled int
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.CarrierSlug, &r.CarrierConnectionID, &r.PhoneNumber,
			&r.PhoneNumberSID, &r.AgentID, &enabled, &r.HoldPrompt, &r.TimeoutSec, &r.Secret, &r.CreatedAt, &r.UpdatedAt); err != nil {
			return nil, err
		}
		r.Enabled = enabled != 0
		out = append(out, r)
	}
	return out, nil
}

func (c *callsDB) updateRoutePhoneSID(id, sid string) error {
	_, err := c.db.Exec(`UPDATE inbound_routes SET phone_number_sid = ?, updated_at = ? WHERE id = ?`,
		sid, time.Now().UTC().Format(time.RFC3339), id)
	return err
}
