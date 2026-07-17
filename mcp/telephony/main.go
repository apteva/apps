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
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: telephony
display_name: Telephony
version: 0.1.9
description: |
  Place and receive voice calls via programmable carriers. Calls run as realtime
  sub-threads in core; carrier audio is bridged through this sidecar.
author: Apteva
icon: https://raw.githubusercontent.com/apteva/apps/main/mcp/telephony/icon.svg
scopes: [project, global]
min_apteva_version: "0.25.12"
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
    - { prefix: /media/, no_auth: true }
    - { prefix: /webhook/, no_auth: true }
    - { prefix: /inbound/, no_auth: true }
    - { prefix: /xml/, no_auth: true }
    - { prefix: /ui/ }
    - { prefix: /calls }
    - { prefix: /calls/ }
    - { prefix: /numbers/ }
  mcp_tools:
    - { name: telephony_place_call,   description: "Place an outbound voice call." }
    - { name: telephony_answer_call,  description: "Answer an inbound call by spawning a realtime thread." }
    - { name: telephony_reject_call,  description: "Reject a pending inbound call." }
    - { name: telephony_routes_create, description: "Create an inbound phone-number route to an agent." }
    - { name: telephony_routes_set_answer_mode, description: "Configure agent-decided or immediate realtime answering for an inbound route." }
    - { name: telephony_routes_configure_carrier, description: "Configure the carrier phone number webhook for an inbound route." }
    - { name: telephony_routes_disable, description: "Disable an inbound route and restore the prior carrier webhook." }
    - { name: telephony_routes_list,  description: "List inbound call routes." }
    - { name: telephony_pending_calls, description: "List pending inbound calls." }
    - { name: telephony_hangup,       description: "End an active call." }
    - { name: telephony_active_calls, description: "List ongoing calls." }
    - { name: telephony_numbers_search, description: "Search and compare carrier phone-number inventory and pricing." }
    - { name: telephony_numbers_purchase, description: "Purchase a quoted phone number after explicit confirmation, with address and bundle when required." }
    - { name: telephony_addresses_list, description: "List provider addresses." }
    - { name: telephony_address_create, description: "Create and validate a provider address." }
    - { name: telephony_regulatory_requirements, description: "Discover current provider regulatory requirements." }
    - { name: telephony_regulatory_bundles_list, description: "List provider regulatory bundles." }
    - { name: telephony_regulatory_bundle_create, description: "Create a draft regulatory bundle." }
    - { name: telephony_regulatory_bundle_get, description: "Get a regulatory bundle, requirements, and items." }
    - { name: telephony_regulatory_bundle_item_create, description: "Create and assign an end user or document to a bundle." }
    - { name: telephony_regulatory_bundle_evaluate, description: "Evaluate bundle compliance." }
    - { name: telephony_regulatory_bundle_submit, description: "Submit a compliant bundle for review." }
    - { name: telephony_compliance_profiles_list, description: "List provider compliance profiles." }
    - { name: telephony_compliance_profile_create, description: "Create a provider compliance profile." }
    - { name: telephony_compliance_profile_get, description: "Get a provider compliance profile and its requirements." }
    - { name: telephony_compliance_requirement_set, description: "Set a compliance requirement value or document." }
    - { name: telephony_compliance_profile_evaluate, description: "Evaluate compliance-profile completeness." }
    - { name: telephony_compliance_profile_submit, description: "Submit a complete compliance profile for review." }
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

type App struct{ installID int64 }

const (
	answerModeAgent             = "agent"
	answerModeRealtimeImmediate = "realtime_immediate"
	defaultInboundGreeting      = "Greet the caller naturally, introduce yourself, and ask how you can help."
)

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
	identity, err := ctx.PlatformAPI().WhoAmI()
	if err != nil {
		return fmt.Errorf("resolve telephony installation identity: %w", err)
	}
	if identity == nil || identity.InstallID <= 0 {
		return errors.New("resolve telephony installation identity: platform returned no install id")
	}
	a.installID = identity.InstallID
	if _, err := ctx.AppDB().Exec(`UPDATE calls SET media_active = 0,
        status = CASE WHEN status IN ('answered','in-progress') THEN 'media-disconnected' ELSE status END,
        state_expires_at = CASE WHEN status IN ('answered','in-progress') THEN ? ELSE state_expires_at END
        WHERE media_active <> 0 OR status IN ('answered','in-progress')`,
		time.Now().UTC().Add(20*time.Second).Format(time.RFC3339)); err != nil {
		return fmt.Errorf("reset stale media claims: %w", err)
	}
	ctx.Logger().Info("telephony mounted")
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error    { return nil }
func (a *App) Channels() []sdk.ChannelFactory { return nil }
func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{
		{
			Name:     "call-auto-answer",
			Schedule: "@every 1s",
			Run:      a.runAutoAnswerTick,
		},
		{
			Name:     "call-lifecycle",
			Schedule: "@every 5s",
			Run:      a.runLifecycleTick,
		},
	}
}
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

// ─── HTTP routes ───────────────────────────────────────────────────

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		// Carrier media stream WS — opened by the carrier when the call connects.
		{Pattern: "/media/twilio/", Handler: a.handleTwilioMediaStream, NoAuth: true},
		{Pattern: "/media/signalwire/", Handler: a.handleSignalWireMediaStream, NoAuth: true},
		{Pattern: "/media/telnyx/", Handler: a.handleTelnyxMediaStream, NoAuth: true},
		{Pattern: "/media/plivo/", Handler: a.handlePlivoMediaStream, NoAuth: true},
		{Pattern: "/media/vonage/", Handler: a.handleVonageMediaStream, NoAuth: true},
		// Plivo fetches XML call control from an answer_url.
		{Pattern: "/xml/plivo/", Handler: a.handlePlivoXML, NoAuth: true},
		// Carrier status callbacks (initiated, ringing, in-progress, completed, ...).
		{Pattern: "/webhook/status/", Handler: a.handleStatusCallback, NoAuth: true},
		// Twilio inbound call control. The route id maps a phone number
		// to the agent that should receive the incoming-call event.
		{Pattern: "/inbound/twilio/", Handler: a.handleTwilioInbound, NoAuth: true},
		{Pattern: "/inbound/telnyx/", Handler: a.handleTelnyxInbound, NoAuth: true},
		// Panel data endpoint — lists active + recent calls.
		{Pattern: "/calls", Handler: a.handleListCalls},
		// Panel action endpoint.
		{Pattern: "/calls/", Handler: a.handleCallAction},
		// Provider-neutral phone-number discovery and confirmed purchase.
		{Pattern: "/numbers/", Handler: a.handleNumbers},
	}
}

// ─── MCP tools ─────────────────────────────────────────────────────

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name: "telephony_place_call",
			Description: "Place an outbound voice call via the bound carrier. Telephony spawns a realtime sub-thread and bridges carrier audio into it. " +
				"Args: to (E.164 phone number, required), directive (system instructions for the call, required), voice? (provider-specific; omitted uses the realtime provider default), greeting?, timeout_sec? (ring timeout, default 30). " +
				"Returns: { call_id, thread_id }. Use send/done events to monitor — do not poll telephony_active_calls in a tight loop.",
			InputSchema: schemaObject(map[string]any{
				"to":               map[string]any{"type": "string", "description": "Phone number to dial in E.164 format (e.g. +14155551234)."},
				"directive":        map[string]any{"type": "string", "description": "System instructions the realtime model runs with. Should describe the persona, the goal of the call, and when to escalate to main via send(). Keep it short — 2-4 sentences."},
				"voice":            map[string]any{"type": "string", "description": "Provider-specific realtime voice id. Omit to use the configured provider default."},
				"greeting":         map[string]any{"type": "string", "description": "Opening instruction spoken after the callee connects."},
				"timeout_sec":      map[string]any{"type": "integer", "description": "Ring timeout before giving up.", "default": 30, "minimum": 5, "maximum": 120},
				"max_duration_sec": map[string]any{"type": "integer", "description": "Hard maximum connected-call duration.", "default": 3600, "minimum": 60, "maximum": 14400},
				"idempotency_key":  map[string]any{"type": "string", "description": "Stable unique key for safely retrying this call request."},
			}, []string{"to", "directive"}),
			// Use HandlerCtx so we can pull the calling agent's id from
			// the Caller context — the realtime thread needs to spawn
			// INSIDE that agent so send/done flows between them.
			HandlerCtx: a.toolPlaceCall,
		},
		{
			Name:        "telephony_routes_create",
			Description: "Create an inbound route from a carrier phone number to the calling agent. After creating it, call telephony_routes_configure_carrier(route_id) to set provider routing. Args: phone_number? (defaults to bound connection phone_number), phone_number_id?, hold_prompt?, timeout_sec?, answer_mode? (agent|realtime_immediate), directive?, voice?, greeting?.",
			InputSchema: schemaObject(map[string]any{
				"phone_number":     map[string]any{"type": "string", "description": "Inbound number in E.164. Defaults to bound carrier connection phone_number."},
				"phone_number_id":  map[string]any{"type": "string", "description": "Optional provider phone-number resource ID; auto-discovered when omitted."},
				"phone_number_sid": map[string]any{"type": "string", "description": "Legacy alias for phone_number_id."},
				"hold_prompt":      map[string]any{"type": "string", "description": "Short prompt supported carriers play while the agent decides whether to answer."},
				"timeout_sec":      map[string]any{"type": "integer", "description": "How long to wait before ending if no answer.", "default": 60, "minimum": 10, "maximum": 300},
				"answer_mode":      map[string]any{"type": "string", "enum": []string{"agent", "realtime_immediate"}, "default": "agent", "description": "Agent emits an answer decision, or Telephony immediately starts the configured realtime thread."},
				"directive":        map[string]any{"type": "string", "description": "Required system directive when answer_mode is realtime_immediate."},
				"voice":            map[string]any{"type": "string", "description": "Optional realtime voice for immediate answering."},
				"greeting":         map[string]any{"type": "string", "description": "Opening instruction spoken after the media bridge connects."},
			}, nil),
			HandlerCtx: a.toolRoutesCreate,
		},
		{
			Name:        "telephony_routes_set_answer_mode",
			Description: "Configure how an inbound route answers. agent preserves event-driven pickup. realtime_immediate starts the realtime thread as soon as the carrier webhook arrives and requires directive. Args: route_id, answer_mode, directive?, voice?, greeting?; changes apply to new calls.",
			InputSchema: schemaObject(map[string]any{
				"route_id":    map[string]any{"type": "string"},
				"answer_mode": map[string]any{"type": "string", "enum": []string{"agent", "realtime_immediate"}},
				"directive":   map[string]any{"type": "string"},
				"voice":       map[string]any{"type": "string"},
				"greeting":    map[string]any{"type": "string"},
			}, []string{"route_id", "answer_mode"}),
			HandlerCtx: a.toolRoutesSetAnswerMode,
		},
		{
			Name:        "telephony_routes_configure_carrier",
			Description: "Configure the bound carrier for an inbound route. Twilio sets the number webhook; Telnyx creates a dedicated Call Control Application and assigns the number. Args: route_id (required).",
			InputSchema: schemaObject(map[string]any{
				"route_id": map[string]any{"type": "string", "description": "Route id returned by telephony_routes_create."},
			}, []string{"route_id"}),
			HandlerCtx: a.toolRoutesConfigureCarrier,
		},
		{
			Name:        "telephony_routes_list",
			Description: "List inbound phone-number routes for this app/project.",
			InputSchema: schemaObject(map[string]any{}, nil),
			HandlerCtx:  a.toolRoutesList,
		},
		{
			Name:        "telephony_routes_disable",
			Description: "Disable an inbound route and restore the carrier phone number's previous webhook when available. Args: route_id (required).",
			InputSchema: schemaObject(map[string]any{
				"route_id": map[string]any{"type": "string", "description": "Inbound route id."},
			}, []string{"route_id"}),
			HandlerCtx: a.toolRoutesDisable,
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
				"voice":     map[string]any{"type": "string", "description": "Provider-specific realtime voice id. Omit to use the configured provider default."},
				"greeting":  map[string]any{"type": "string", "description": "Opening instruction spoken immediately after the call is connected."},
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
			HandlerCtx: a.toolHangup,
		},
		{
			Name:        "telephony_active_calls",
			Description: "List currently-ongoing calls with their thread IDs, durations, and statuses. Use sparingly — prefer reacting to send()/done() events.",
			InputSchema: schemaObject(map[string]any{}, nil),
			HandlerCtx:  a.toolActiveCalls,
		},
		{
			Name: "telephony_numbers_search",
			Description: "Search and compare voice-capable phone numbers through the bound carrier without purchasing anything. " +
				"Supports one country or up to 30 countries and returns normalized monthly, setup, and inbound prices where the provider exposes them. " +
				"Purchase-ready offers include a short-lived confirmation_token; show the exact quote to the user and obtain explicit confirmation before calling telephony_numbers_purchase.",
			InputSchema: schemaObject(map[string]any{
				"country":     map[string]any{"type": "string", "description": "One ISO alpha-2 country code, e.g. EE."},
				"countries":   map[string]any{"type": "array", "items": map[string]any{"type": "string"}, "description": "Up to 30 ISO alpha-2 country codes for comparison."},
				"number_type": map[string]any{"type": "string", "enum": []string{"any", "local", "mobile", "national", "toll_free"}, "default": "local"},
				"features":    map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []string{"voice", "sms", "mms"}}, "default": []string{"voice"}},
				"area_code":   map[string]any{"type": "string", "description": "Optional national destination or area code."},
				"pattern":     map[string]any{"type": "string", "description": "Optional number pattern supported by the provider."},
				"limit":       map[string]any{"type": "integer", "minimum": 1, "maximum": 20, "default": 10, "description": "Maximum offers per country."},
			}, nil),
			HandlerCtx: a.toolNumbersSearch,
		},
		{
			Name: "telephony_numbers_purchase",
			Description: "Purchase one phone number from an unexpired telephony_numbers_search quote. THIS SPENDS REAL MONEY. " +
				"Present the provider, number, recurring price, setup price, inbound rate, currency, and regulatory requirement to the user; call only after explicit confirmation. " +
				"For regulated numbers, provide the required address_id and compliance_id. Provider resources are validated before spending. Successful retries with the same token are idempotent; never automatically retry an in-progress or failed purchase.",
			InputSchema: schemaObject(map[string]any{
				"confirmation_token": map[string]any{"type": "string", "description": "Short-lived token returned with a purchase-ready search offer."},
				"address_id":         map[string]any{"type": "string", "description": "Provider address ID when the quote requires an address."},
				"compliance_id":      map[string]any{"type": "string", "description": "Provider compliance profile ID when the quote requires regulatory information."},
				"address_sid":        map[string]any{"type": "string", "description": "Legacy alias for address_id."},
				"bundle_sid":         map[string]any{"type": "string", "description": "Legacy alias for compliance_id."},
			}, []string{"confirmation_token"}),
			HandlerCtx: a.toolNumbersPurchase,
		},
		{
			Name:        "telephony_addresses_list",
			Description: "List addresses in the bound carrier account. Args: country?, customer_name?, limit?. Address data comes directly from the provider and is not stored by Telephony.",
			InputSchema: schemaObject(map[string]any{
				"country":       map[string]any{"type": "string", "description": "Optional ISO alpha-2 address country."},
				"customer_name": map[string]any{"type": "string"},
				"limit":         map[string]any{"type": "integer", "minimum": 1, "maximum": 1000, "default": 50},
			}, nil),
			HandlerCtx: a.toolAddressesList,
		},
		{
			Name:        "telephony_address_create",
			Description: "Create and validate an address in the bound carrier account. This transmits personal or business address data to the provider.",
			InputSchema: schemaObject(map[string]any{
				"customer_name":    map[string]any{"type": "string"},
				"business_name":    map[string]any{"type": "string"},
				"first_name":       map[string]any{"type": "string"},
				"last_name":        map[string]any{"type": "string"},
				"phone_number":     map[string]any{"type": "string"},
				"street":           map[string]any{"type": "string"},
				"street_secondary": map[string]any{"type": "string"},
				"city":             map[string]any{"type": "string"},
				"region":           map[string]any{"type": "string"},
				"postal_code":      map[string]any{"type": "string"},
				"country":          map[string]any{"type": "string", "description": "ISO alpha-2 address country."},
				"friendly_name":    map[string]any{"type": "string"},
				"auto_correct":     map[string]any{"type": "boolean", "default": true},
			}, []string{"street", "city", "country"}),
			HandlerCtx: a.toolAddressCreate,
		},
		{
			Name:        "telephony_regulatory_requirements",
			Description: "Read current provider requirements before creating a compliance profile. Args: country, number_type, end_user_type?. Use the returned dynamic fields exactly; never infer or hardcode regulatory requirements.",
			InputSchema: schemaObject(map[string]any{
				"country":       map[string]any{"type": "string"},
				"number_type":   map[string]any{"type": "string", "enum": []string{"local", "mobile", "national", "toll_free"}},
				"end_user_type": map[string]any{"type": "string", "enum": []string{"individual", "business"}},
			}, []string{"country", "number_type"}),
			HandlerCtx: a.toolRegulatoryRequirements,
		},
		{
			Name:        "telephony_regulatory_bundles_list",
			Description: "Legacy alias for listing provider compliance profiles and their approval state.",
			InputSchema: schemaObject(map[string]any{
				"country":       map[string]any{"type": "string"},
				"number_type":   map[string]any{"type": "string", "enum": []string{"local", "mobile", "national", "toll_free"}},
				"end_user_type": map[string]any{"type": "string", "enum": []string{"individual", "business"}},
				"status":        map[string]any{"type": "string"},
				"friendly_name": map[string]any{"type": "string"},
				"limit":         map[string]any{"type": "integer", "minimum": 1, "maximum": 1000, "default": 50},
			}, nil),
			HandlerCtx: a.toolRegulatoryBundlesList,
		},
		{
			Name:        "telephony_regulatory_bundle_create",
			Description: "Legacy alias for creating a provider compliance profile. First call telephony_regulatory_requirements.",
			InputSchema: schemaObject(map[string]any{
				"friendly_name":   map[string]any{"type": "string"},
				"email":           map[string]any{"type": "string"},
				"regulation_sid":  map[string]any{"type": "string"},
				"country":         map[string]any{"type": "string"},
				"number_type":     map[string]any{"type": "string", "enum": []string{"local", "mobile", "national", "toll_free"}},
				"end_user_type":   map[string]any{"type": "string", "enum": []string{"individual", "business"}},
				"status_callback": map[string]any{"type": "string"},
			}, nil),
			HandlerCtx: a.toolRegulatoryBundleCreate,
		},
		{
			Name:        "telephony_regulatory_bundle_get",
			Description: "Legacy alias for getting a provider compliance profile.",
			InputSchema: schemaObject(map[string]any{"compliance_id": map[string]any{"type": "string"}, "bundle_sid": map[string]any{"type": "string"}}, nil),
			HandlerCtx:  a.toolRegulatoryBundleGet,
		},
		{
			Name:        "telephony_regulatory_bundle_item_create",
			Description: "Legacy alias for setting a compliance requirement. Twilio accepts end-user/document objects; Telnyx accepts requirement_id plus field_value or file.",
			InputSchema: schemaObject(map[string]any{
				"bundle_sid":     map[string]any{"type": "string"},
				"compliance_id":  map[string]any{"type": "string"},
				"requirement_id": map[string]any{"type": "string"},
				"field_value":    map[string]any{"type": "string"},
				"kind":           map[string]any{"type": "string", "enum": []string{"end_user", "document"}},
				"friendly_name":  map[string]any{"type": "string"},
				"type":           map[string]any{"type": "string"},
				"attributes":     map[string]any{"type": "object", "description": "Dynamic fields from the selected Twilio Regulation."},
				"file":           map[string]any{"type": "string", "description": "Optional JPEG, PNG, or PDF as base64, data URL, blob reference, or binary envelope."},
				"file_name":      map[string]any{"type": "string"},
			}, nil),
			HandlerCtx: a.toolRegulatoryBundleItemCreate,
		},
		{
			Name:        "telephony_regulatory_bundle_evaluate",
			Description: "Legacy alias for evaluating provider compliance-profile completeness.",
			InputSchema: schemaObject(map[string]any{"compliance_id": map[string]any{"type": "string"}, "bundle_sid": map[string]any{"type": "string"}}, nil),
			HandlerCtx:  a.toolRegulatoryBundleEvaluate,
		},
		{
			Name:        "telephony_regulatory_bundle_submit",
			Description: "Legacy alias for submitting a complete provider compliance profile for review.",
			InputSchema: schemaObject(map[string]any{"compliance_id": map[string]any{"type": "string"}, "bundle_sid": map[string]any{"type": "string"}}, nil),
			HandlerCtx:  a.toolRegulatoryBundleSubmit,
		},
		{
			Name: "telephony_compliance_profiles_list", Description: "List provider compliance profiles. Twilio profiles are regulatory bundles; Telnyx profiles are requirement groups.",
			InputSchema: schemaObject(map[string]any{"country": map[string]any{"type": "string"}, "number_type": map[string]any{"type": "string"}, "status": map[string]any{"type": "string"}, "friendly_name": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer"}}, nil), HandlerCtx: a.toolRegulatoryBundlesList,
		},
		{
			Name: "telephony_compliance_profile_create", Description: "Create a provider compliance profile after discovering current requirements.",
			InputSchema: schemaObject(map[string]any{"country": map[string]any{"type": "string"}, "number_type": map[string]any{"type": "string"}, "friendly_name": map[string]any{"type": "string"}, "end_user_type": map[string]any{"type": "string"}, "email": map[string]any{"type": "string"}, "regulation_sid": map[string]any{"type": "string"}}, nil), HandlerCtx: a.toolRegulatoryBundleCreate,
		},
		{
			Name: "telephony_compliance_profile_get", Description: "Get a provider compliance profile and its current requirements.",
			InputSchema: schemaObject(map[string]any{"compliance_id": map[string]any{"type": "string"}}, []string{"compliance_id"}), HandlerCtx: a.toolRegulatoryBundleGet,
		},
		{
			Name: "telephony_compliance_requirement_set", Description: "Set one compliance requirement value or upload and assign its document.",
			InputSchema: schemaObject(map[string]any{"compliance_id": map[string]any{"type": "string"}, "requirement_id": map[string]any{"type": "string"}, "field_value": map[string]any{"type": "string"}, "kind": map[string]any{"type": "string"}, "friendly_name": map[string]any{"type": "string"}, "type": map[string]any{"type": "string"}, "attributes": map[string]any{"type": "object"}, "file": map[string]any{"type": "string"}, "file_name": map[string]any{"type": "string"}}, []string{"compliance_id"}), HandlerCtx: a.toolRegulatoryBundleItemCreate,
		},
		{
			Name: "telephony_compliance_profile_evaluate", Description: "Evaluate whether a provider compliance profile is complete and usable for ordering.",
			InputSchema: schemaObject(map[string]any{"compliance_id": map[string]any{"type": "string"}}, []string{"compliance_id"}), HandlerCtx: a.toolRegulatoryBundleEvaluate,
		},
		{
			Name: "telephony_compliance_profile_submit", Description: "Submit a complete provider compliance profile for optional provider review or pre-approval.",
			InputSchema: schemaObject(map[string]any{"compliance_id": map[string]any{"type": "string"}}, []string{"compliance_id"}), HandlerCtx: a.toolRegulatoryBundleSubmit,
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
	voice := strings.TrimSpace(strArg(args, "voice", ""))
	greeting := strings.TrimSpace(strArg(args, "greeting", "Greet the person who just joined the call, introduce yourself naturally, and begin the conversation."))
	timeout := intArg(args, "timeout_sec", 30)
	maxDuration := intArg(args, "max_duration_sec", 3600)
	idempotencyKey := strings.TrimSpace(strArg(args, "idempotency_key", ""))

	if !validE164(to) {
		return mcpError("to must be a valid E.164 number (+ followed by 8-15 digits)"), nil
	}
	if strings.TrimSpace(directive) == "" {
		return mcpError("directive required"), nil
	}
	if !validVoice(voice) {
		return mcpError("voice must be a provider voice id of at most 64 characters"), nil
	}
	if greeting == "" || len(greeting) > 500 {
		return mcpError("greeting must be between 1 and 500 characters"), nil
	}
	if timeout < 5 {
		timeout = 5
	}
	if timeout > 120 {
		timeout = 120
	}
	if maxDuration < 60 {
		maxDuration = 60
	}
	if maxDuration > 14400 {
		maxDuration = 14400
	}
	if len(idempotencyKey) > 128 {
		return mcpError("idempotency_key must be at most 128 characters"), nil
	}
	projectID := currentProject(ctx)
	if projectID == "" {
		return mcpError("project context required for telephony calls"), nil
	}
	if err := a.validatePublicEndpoint(); err != nil {
		return mcpError(err.Error()), nil
	}
	if idempotencyKey != "" {
		existing, err := a.db().findOutboundByIdempotency(agentID, projectID, idempotencyKey)
		if err != nil {
			return mcpError("check idempotency key: " + err.Error()), nil
		}
		if existing != nil {
			return callToolResult(existing.ID, existing.ThreadID, existing.ToNumber), nil
		}
	}
	if limit := maxCallsPerMinute(); limit > 0 {
		count, err := a.db().countOutboundSince(agentID, projectID, time.Now().UTC().Add(-time.Minute))
		if err != nil {
			return mcpError("check call rate limit: " + err.Error()), nil
		}
		if count >= limit {
			return mcpError(fmt.Sprintf("outbound call rate limit reached (%d per minute)", limit)), nil
		}
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
	if !validE164(from) {
		return mcpError("carrier connection has no phone_number configured"), nil
	}

	callID := newCallID()
	threadID := "tel-" + callID
	callbackSecret := newSecret()
	now := time.Now().UTC()

	rt, err := ctx.PlatformAPI().SpawnRealtimeThread(sdk.RealtimeSpawnRequest{
		AgentID:                    agentID,
		ThreadID:                   threadID,
		Directive:                  directive,
		Voice:                      voice,
		Ephemeral:                  true,
		InitialMessage:             greeting,
		BridgeDisconnectTTLSeconds: 30,
	})
	if err != nil {
		return mcpError("spawn realtime thread: " + err.Error()), nil
	}
	if rt.AudioBridgeURL == "" {
		_ = ctx.PlatformAPI().KillThread(agentID, threadID)
		return mcpError("realtime spawn returned no audio bridge URL"), nil
	}

	carrier, err := a.carrierFor(bound, creds.Slug, creds.Fields)
	if err != nil {
		_ = ctx.PlatformAPI().KillThread(agentID, threadID)
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
		CallbackSecret:      callbackSecret,
		ToNumber:            to,
		FromNumber:          from,
		Directive:           directive,
		Voice:               voice,
		AudioBridgeURL:      rt.AudioBridgeURL,
		Status:              "initiated",
		PlacedAt:            now.Format(time.RFC3339),
		ProjectID:           projectID,
		IdempotencyKey:      idempotencyKey,
		StateExpiresAt:      now.Add(time.Duration(timeout+30) * time.Second).Format(time.RFC3339),
		DeadlineAt:          now.Add(time.Duration(maxDuration) * time.Second).Format(time.RFC3339),
	}); err != nil {
		_ = ctx.PlatformAPI().KillThread(agentID, threadID)
		return mcpError("persist call before carrier placement: " + err.Error()), nil
	}

	placed, err := carrier.Place(ctx, carrierPlaceRequest{
		CallID:         callID,
		CallbackSecret: callbackSecret,
		ProjectID:      projectID,
		To:             to,
		From:           from,
		TimeoutSec:     timeout,
		MaxDurationSec: maxDuration,
		AudioBridgeURL: rt.AudioBridgeURL,
	})
	if err != nil {
		_ = a.db().updateStatus(callID, "failed", err.Error())
		_ = ctx.PlatformAPI().KillThread(agentID, threadID)
		return mcpError(err.Error()), nil
	}
	if placed != nil && (placed.CarrierSID != "" || placed.CarrierRequestID != "") {
		if err := a.db().updateCarrierIdentity(callID, placed.CarrierSID, placed.CarrierRequestID); err != nil {
			if placed.CarrierSID != "" {
				_ = carrier.Hangup(ctx, &callRow{CarrierSID: placed.CarrierSID})
			}
			_ = ctx.PlatformAPI().KillThread(agentID, threadID)
			_ = a.db().updateStatus(callID, "failed", "persist carrier call id: "+err.Error())
			return mcpError("persist carrier call id: " + err.Error()), nil
		}
	}

	return callToolResult(callID, threadID, to), nil
}

func callToolResult(callID, threadID, to string) map[string]any {
	return map[string]any{
		"content": []map[string]any{{"type": "text", "text": fmt.Sprintf("Calling %s. Thread: %s. The call is running — wait for send() escalations or [thread:%s done].", to, threadID, threadID)}},
		"_meta":   map[string]any{"call_id": callID, "thread_id": threadID},
	}
}

// ─── telephony_hangup ──────────────────────────────────────────────

func (a *App) toolHangup(callerCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	agentID := callerAgentID(callerCtx)
	if agentID == 0 {
		return mcpError("could not determine calling agent id"), nil
	}
	callID := strArg(args, "call_id", "")
	if callID == "" {
		return mcpError("call_id required"), nil
	}
	if msg := a.hangupCall(ctx, callID, agentID, currentProject(ctx)); msg != "" {
		return mcpError(msg), nil
	}

	return "ok", nil
}

func (a *App) hangupCall(ctx *sdk.AppCtx, callID string, agentID int64, projectID string) string {
	row, err := a.db().findCall(callID)
	if err != nil || row == nil {
		return "unknown call_id"
	}

	if projectID == "" || row.ProjectID != projectID {
		return "call does not belong to this project"
	}
	if agentID != 0 && row.AgentID != agentID {
		return "call belongs to another agent"
	}
	if isTerminalStatus(row.Status) {
		if err := a.killCallThread(ctx, row); err != nil {
			return "kill completed call thread: " + err.Error()
		}
		return ""
	}
	if row.CarrierSID == "" {
		return "carrier call id is not available yet"
	}
	carrier, err := a.carrierForRow(ctx, nil, row)
	if err != nil {
		return "resolve carrier hangup adapter: " + err.Error()
	}
	if err := carrier.Hangup(ctx, row); err != nil {
		return "carrier hangup failed: " + err.Error()
	}
	if err := a.killCallThread(ctx, row); err != nil {
		ctx.Logger().Warn("kill thread failed", "err", err)
	}
	if err := a.db().updateStatus(callID, "completed", ""); err != nil {
		return "persist completed status: " + err.Error()
	}

	return ""
}

// ─── telephony_active_calls ────────────────────────────────────────

func (a *App) toolActiveCalls(callerCtx context.Context, ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	agentID := callerAgentID(callerCtx)
	if agentID == 0 {
		return mcpError("could not determine calling agent id"), nil
	}
	rows, err := a.db().listActiveForAgent(agentID, currentProject(ctx))
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
	if !validE164(phone) {
		return mcpError("phone_number required, or carrier connection must define phone_number"), nil
	}
	slug := strings.ToLower(firstNonEmpty(creds.Slug, bound.AppSlug))
	if slug == "" {
		return mcpError("could not determine carrier slug"), nil
	}
	if slug != "twilio" && slug != "telnyx" {
		return mcpError("inbound call routing is not implemented for provider " + slug), nil
	}
	if err := a.validatePublicEndpoint(); err != nil {
		return mcpError(err.Error()), nil
	}
	projectID := currentProject(ctx)
	if projectID == "" {
		return mcpError("project context required"), nil
	}
	if existing, err := a.db().findRouteByNumber(bound.ConnectionID, phone); err != nil {
		return mcpError("check existing route: " + err.Error()), nil
	} else if existing != nil {
		return mcpError("an inbound route already exists for this carrier number: " + existing.ID), nil
	}
	holdPrompt := strArg(args, "hold_prompt", "Please hold while I connect you.")
	timeout := intArg(args, "timeout_sec", 60)
	if timeout < 10 {
		timeout = 10
	}
	if timeout > 300 {
		timeout = 300
	}
	answerMode, directive, voice, greeting, err := normalizeRouteAnswerConfig(
		strArg(args, "answer_mode", answerModeAgent),
		strings.TrimSpace(strArg(args, "directive", "")),
		strings.TrimSpace(strArg(args, "voice", "")),
		strings.TrimSpace(strArg(args, "greeting", "")),
	)
	if err != nil {
		return mcpError(err.Error()), nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	route := routeRow{
		ID:                  "route-" + newCallID(),
		ProjectID:           projectID,
		CarrierSlug:         slug,
		CarrierConnectionID: bound.ConnectionID,
		PhoneNumber:         phone,
		PhoneNumberSID:      firstNonEmpty(strArg(args, "phone_number_id", ""), strArg(args, "phone_number_sid", "")),
		AgentID:             agentID,
		Enabled:             true,
		HoldPrompt:          holdPrompt,
		TimeoutSec:          timeout,
		AnswerMode:          answerMode,
		AutoDirective:       directive,
		AutoVoice:           voice,
		AutoGreeting:        greeting,
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

func (a *App) toolRoutesSetAnswerMode(callerCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	agentID := callerAgentID(callerCtx)
	if agentID == 0 {
		return mcpError("could not determine calling agent id"), nil
	}
	routeID := strings.TrimSpace(strArg(args, "route_id", ""))
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
	if route.AgentID != agentID || route.ProjectID != currentProject(ctx) {
		return mcpError("route belongs to another agent or project"), nil
	}
	directive, voice, greeting := route.AutoDirective, route.AutoVoice, route.AutoGreeting
	if value, ok := optionalStringArg(args, "directive"); ok {
		directive = value
	}
	if value, ok := optionalStringArg(args, "voice"); ok {
		voice = value
	}
	if value, ok := optionalStringArg(args, "greeting"); ok {
		greeting = value
	}
	mode, directive, voice, greeting, err := normalizeRouteAnswerConfig(
		strArg(args, "answer_mode", ""), directive, voice, greeting,
	)
	if err != nil {
		return mcpError(err.Error()), nil
	}
	if err := a.db().updateRouteAnswerMode(route.ID, mode, directive, voice, greeting); err != nil {
		return mcpError("persist route answer mode: " + err.Error()), nil
	}
	route.AnswerMode = mode
	route.AutoDirective = directive
	route.AutoVoice = voice
	route.AutoGreeting = greeting
	route.UpdatedAt = time.Now().UTC().Format(time.RFC3339)
	return map[string]any{"ok": true, "route": routePublic(a, *route)}, nil
}

func (a *App) toolRoutesConfigureCarrier(callerCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	agentID := callerAgentID(callerCtx)
	if agentID == 0 {
		return mcpError("could not determine calling agent id"), nil
	}
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
	if route.AgentID != agentID || route.ProjectID != currentProject(ctx) {
		return mcpError("route belongs to another agent or project"), nil
	}
	if !route.Enabled {
		return mcpError("route is disabled"), nil
	}
	if err := a.validatePublicEndpoint(); err != nil {
		return mcpError(err.Error()), nil
	}
	switch route.CarrierSlug {
	case "twilio":
		err = a.configureTwilioRoute(ctx, route)
	case "telnyx":
		err = a.configureTelnyxRoute(ctx, route)
	default:
		err = fmt.Errorf("carrier webhook configuration is not implemented for provider %s", route.CarrierSlug)
	}
	if err != nil {
		return mcpError(err.Error()), nil
	}
	return map[string]any{
		"ok":          true,
		"route":       routePublic(a, *route),
		"inbound_url": a.inboundRouteURL(*route),
	}, nil
}

func (a *App) toolRoutesDisable(callerCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	agentID := callerAgentID(callerCtx)
	if agentID == 0 {
		return mcpError("could not determine calling agent id"), nil
	}
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
	if route.AgentID != agentID || route.ProjectID != currentProject(ctx) {
		return mcpError("route belongs to another agent or project"), nil
	}
	if !route.Enabled {
		return map[string]any{"ok": true, "route_id": route.ID, "already_disabled": true}, nil
	}
	switch route.CarrierSlug {
	case "twilio":
		err = a.disableTwilioRoute(ctx, route)
	case "telnyx":
		err = a.disableTelnyxRoute(ctx, route)
	default:
		err = fmt.Errorf("route cannot be safely disabled for provider %s", route.CarrierSlug)
	}
	if err != nil {
		return mcpError(err.Error()), nil
	}
	if err := a.db().disableRoute(route.ID); err != nil {
		return mcpError("persist disabled route: " + err.Error()), nil
	}
	return map[string]any{"ok": true, "route_id": route.ID, "carrier": route.CarrierSlug}, nil
}

func (a *App) toolRoutesList(callerCtx context.Context, ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	agentID := callerAgentID(callerCtx)
	if agentID == 0 {
		return mcpError("could not determine calling agent id"), nil
	}
	routes, err := a.db().listRoutesForAgent(agentID, currentProject(ctx))
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
	if agentID == 0 {
		return mcpError("could not determine calling agent id"), nil
	}
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
	voice := strings.TrimSpace(strArg(args, "voice", ""))
	greeting := strings.TrimSpace(strArg(args, "greeting", "Greet the caller naturally, introduce yourself, and ask how you can help."))
	if callID == "" || strings.TrimSpace(directive) == "" {
		return mcpError("call_id and directive required"), nil
	}
	if !validVoice(voice) {
		return mcpError("voice must be a provider voice id of at most 64 characters"), nil
	}
	if greeting == "" || len(greeting) > 500 {
		return mcpError("greeting must be between 1 and 500 characters"), nil
	}
	row, err := a.db().findCall(callID)
	if err != nil {
		return mcpError("load call: " + err.Error()), nil
	}
	if row == nil {
		return mcpError("unknown call_id"), nil
	}
	if row.Direction != "inbound" {
		return mcpError("call is not inbound"), nil
	}
	if row.AgentID != agentID || row.ProjectID != currentProject(ctx) {
		return mcpError("this call is routed to another agent"), nil
	}
	if row.CarrierSlug != "twilio" && row.CarrierSlug != "telnyx" {
		return mcpError("answer is not implemented for inbound provider " + row.CarrierSlug), nil
	}
	threadID, err := a.answerCall(ctx, row, directive, voice, greeting, false)
	if err != nil {
		return mcpError(err.Error()), nil
	}
	return map[string]any{
		"ok":        true,
		"call_id":   callID,
		"thread_id": threadID,
	}, nil
}

func (a *App) answerCall(ctx *sdk.AppCtx, row *callRow, directive, voice, greeting string, terminalOnCarrierError bool) (string, error) {
	if row.Status == "answered" || row.Status == "in-progress" {
		return row.ThreadID, nil
	}
	if row.Status != "pending" && row.Status != "answering" {
		return "", fmt.Errorf("call is not available to answer (status=%s)", row.Status)
	}

	threadID := row.ThreadID
	audioBridgeURL := row.AudioBridgeURL
	if row.Status == "pending" {
		claimed, err := a.db().claimPendingCall(row.ID, row.AgentID, row.ProjectID)
		if err != nil {
			return "", fmt.Errorf("claim pending call: %w", err)
		}
		if !claimed {
			return "", errors.New("call was already claimed")
		}
		threadID = "tel-" + row.ID
		rt, err := ctx.PlatformAPI().SpawnRealtimeThread(sdk.RealtimeSpawnRequest{
			AgentID:                    row.AgentID,
			ThreadID:                   threadID,
			Directive:                  directive,
			Voice:                      voice,
			Ephemeral:                  true,
			InitialMessage:             greeting,
			BridgeDisconnectTTLSeconds: 30,
		})
		if err != nil {
			_ = a.db().releaseAnswerClaim(row.ID)
			return "", fmt.Errorf("spawn realtime thread: %w", err)
		}
		if rt == nil || strings.TrimSpace(rt.AudioBridgeURL) == "" {
			_ = ctx.PlatformAPI().KillThread(row.AgentID, threadID)
			_ = a.db().releaseAnswerClaim(row.ID)
			return "", errors.New("realtime spawn returned no audio bridge URL")
		}
		audioBridgeURL = rt.AudioBridgeURL
		if err := a.db().attachCall(row.ID, threadID, audioBridgeURL, directive, voice); err != nil {
			_ = ctx.PlatformAPI().KillThread(row.AgentID, threadID)
			_ = a.db().releaseAnswerClaim(row.ID)
			return "", fmt.Errorf("persist call answer: %w", err)
		}
		row.ThreadID = threadID
		row.AudioBridgeURL = audioBridgeURL
		row.Directive = directive
		row.Voice = voice
		row.Status = "answering"
	}
	if threadID == "" || strings.HasPrefix(threadID, "pending-") || audioBridgeURL == "" || audioBridgeURL == "pending" {
		return "", errors.New("answer claim is incomplete; wait for lifecycle recovery and retry")
	}

	if err := a.answerInboundCarrierCall(ctx, row); err != nil {
		_ = ctx.PlatformAPI().KillThread(row.AgentID, threadID)
		if terminalOnCarrierError {
			_ = a.db().updateStatus(row.ID, "failed", "carrier answer failed: "+err.Error())
		} else {
			_ = a.db().resetAnswerClaim(row.ID)
		}
		return "", fmt.Errorf("answer carrier call failed: %w", err)
	}
	if err := a.db().updateStatus(row.ID, "answered", ""); err != nil {
		return "", fmt.Errorf("persist answered status: %w", err)
	}
	return threadID, nil
}

func (a *App) toolRejectCall(callerCtx context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	agentID := callerAgentID(callerCtx)
	if agentID == 0 {
		return mcpError("could not determine calling agent id"), nil
	}
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
	if row.AgentID != agentID || row.ProjectID != currentProject(ctx) {
		return mcpError("this call is routed to another agent"), nil
	}
	if row.Direction != "inbound" || (row.Status != "pending" && row.Status != "answering") {
		return mcpError("call is not a rejectable pending inbound call"), nil
	}
	if row.CarrierSID != "" {
		if err := a.rejectInboundCarrierCall(ctx, row); err != nil {
			return mcpError("carrier reject failed: " + err.Error()), nil
		}
	}
	reason := strArg(args, "reason", "rejected by agent")
	if err := a.killCallThread(ctx, row); err != nil {
		ctx.Logger().Warn("kill rejected call thread", "call", callID, "err", err)
	}
	if err := a.db().updateStatus(callID, "canceled", reason); err != nil {
		return mcpError("persist rejection: " + err.Error()), nil
	}
	return map[string]any{"ok": true, "call_id": callID}, nil
}

func (a *App) findTwilioPhoneNumber(ctx *sdk.AppCtx, route *routeRow) (string, string, error) {
	data, err := executeCarrierTool(ctx, route.CarrierConnectionID, "list_phone_numbers", map[string]any{
		"PhoneNumber": route.PhoneNumber,
		"PageSize":    20,
	})
	if err != nil {
		return "", "", err
	}
	var out struct {
		IncomingPhoneNumbers []struct {
			SID         string `json:"sid"`
			PhoneNumber string `json:"phone_number"`
			VoiceURL    string `json:"voice_url"`
		} `json:"incoming_phone_numbers"`
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return "", "", err
	}
	for _, n := range out.IncomingPhoneNumbers {
		if n.PhoneNumber == route.PhoneNumber {
			return n.SID, n.VoiceURL, nil
		}
	}
	return "", "", nil
}

func (a *App) configureTwilioRoute(ctx *sdk.AppCtx, route *routeRow) error {
	sid, previousVoiceURL, err := a.findTwilioPhoneNumber(ctx, route)
	if err != nil {
		return fmt.Errorf("find Twilio phone number SID: %w", err)
	}
	if sid == "" {
		return fmt.Errorf("could not find an exact Twilio phone number SID match for %s", route.PhoneNumber)
	}
	if route.PhoneNumberSID != "" && route.PhoneNumberSID != sid {
		return errors.New("provided phone_number_id does not belong to " + route.PhoneNumber)
	}
	if err := a.db().updateRoutePhoneSID(route.ID, sid); err != nil {
		return fmt.Errorf("persist Twilio phone number SID: %w", err)
	}
	route.PhoneNumberSID = sid
	if route.PreviousVoiceURL == "" && previousVoiceURL != a.inboundRouteURL(*route) {
		if err := a.db().updateRoutePreviousVoiceURL(route.ID, previousVoiceURL); err != nil {
			return fmt.Errorf("persist previous Twilio webhook: %w", err)
		}
		route.PreviousVoiceURL = previousVoiceURL
	}
	if _, err := executeCarrierTool(ctx, route.CarrierConnectionID, "update_phone_number", map[string]any{
		"PhoneNumberSid": sid,
		"VoiceUrl":       a.inboundRouteURL(*route),
	}); err != nil {
		return fmt.Errorf("configure Twilio webhook: %w", err)
	}
	return nil
}

func (a *App) disableTwilioRoute(ctx *sdk.AppCtx, route *routeRow) error {
	if route.PhoneNumberSID == "" {
		return errors.New("route cannot be safely disabled because its Twilio phone number SID is unavailable")
	}
	if _, err := executeCarrierTool(ctx, route.CarrierConnectionID, "update_phone_number", map[string]any{
		"PhoneNumberSid": route.PhoneNumberSID,
		"VoiceUrl":       route.PreviousVoiceURL,
	}); err != nil {
		return fmt.Errorf("restore Twilio webhook: %w", err)
	}
	return nil
}

type telnyxRouteConfig struct {
	PreviousConnectionID string `json:"previous_connection_id"`
	ApplicationID        string `json:"application_id"`
}

func (a *App) findTelnyxPhoneNumber(ctx *sdk.AppCtx, route *routeRow) (string, string, error) {
	raw, err := executeCarrierTool(ctx, route.CarrierConnectionID, "list_phone_numbers", map[string]any{
		"filter[phone_number]": route.PhoneNumber,
		"page[size]":           100,
	})
	if err != nil {
		return "", "", err
	}
	response, err := telnyxResponse(raw)
	if err != nil {
		return "", "", err
	}
	for _, value := range telnyxDataList(response) {
		number, _ := value.(map[string]any)
		if stringValue(number["phone_number"]) == route.PhoneNumber {
			return stringValue(number["id"]), stringValue(number["connection_id"]), nil
		}
	}
	return "", "", nil
}

func (a *App) configureTelnyxRoute(ctx *sdk.AppCtx, route *routeRow) error {
	phoneID, previousConnectionID, err := a.findTelnyxPhoneNumber(ctx, route)
	if err != nil {
		return fmt.Errorf("find Telnyx phone number: %w", err)
	}
	if phoneID == "" {
		return fmt.Errorf("could not find an exact Telnyx phone number match for %s", route.PhoneNumber)
	}
	if route.PhoneNumberSID != "" && route.PhoneNumberSID != phoneID {
		return errors.New("provided phone_number_id does not belong to " + route.PhoneNumber)
	}
	if route.PreviousVoiceURL != "" {
		var existing telnyxRouteConfig
		if json.Unmarshal([]byte(route.PreviousVoiceURL), &existing) == nil &&
			existing.ApplicationID == previousConnectionID && validProviderResourceID(existing.ApplicationID) {
			return nil
		}
		return errors.New("Telnyx route has saved carrier configuration that does not match the number; disable or repair it before reconfiguring")
	}
	if previousConnectionID == "" {
		provider, providerErr := a.numberProviderFor(ctx)
		if providerErr != nil {
			return providerErr
		}
		previousConnectionID = strings.TrimSpace(provider.Fields["connection_id"])
	}
	if !validProviderResourceID(previousConnectionID) {
		return errors.New("Telnyx number has no previous connection to restore; assign it to the bound connection before configuring the route")
	}
	createdRaw, err := executeCarrierTool(ctx, route.CarrierConnectionID, "create_call_control_application", map[string]any{
		"application_name":    "Apteva inbound " + route.ID,
		"webhook_event_url":   a.inboundRouteURL(*route),
		"active":              true,
		"webhook_api_version": "2",
	})
	if err != nil {
		return fmt.Errorf("create Telnyx Call Control Application: %w", err)
	}
	createdResponse, err := telnyxResponse(createdRaw)
	if err != nil {
		return err
	}
	applicationID := stringValue(telnyxDataMap(createdResponse)["id"])
	if !validProviderResourceID(applicationID) {
		return errors.New("Telnyx created the Call Control Application but returned no usable ID")
	}
	if _, err := executeCarrierTool(ctx, route.CarrierConnectionID, "update_phone_number", map[string]any{
		"id": phoneID, "connection_id": applicationID,
	}); err != nil {
		_, _ = executeCarrierTool(ctx, route.CarrierConnectionID, "delete_call_control_application", map[string]any{"id": applicationID})
		return fmt.Errorf("assign Telnyx phone number to inbound application: %w", err)
	}
	configJSON, err := json.Marshal(telnyxRouteConfig{PreviousConnectionID: previousConnectionID, ApplicationID: applicationID})
	if err != nil {
		return err
	}
	rollback := func() {
		_, _ = executeCarrierTool(ctx, route.CarrierConnectionID, "update_phone_number", map[string]any{
			"id": phoneID, "connection_id": previousConnectionID,
		})
		_, _ = executeCarrierTool(ctx, route.CarrierConnectionID, "delete_call_control_application", map[string]any{"id": applicationID})
	}
	if err := a.db().updateRoutePhoneSID(route.ID, phoneID); err != nil {
		rollback()
		return fmt.Errorf("persist Telnyx phone number ID: %w", err)
	}
	if err := a.db().updateRoutePreviousVoiceURL(route.ID, string(configJSON)); err != nil {
		rollback()
		_ = a.db().updateRoutePhoneSID(route.ID, "")
		return fmt.Errorf("persist previous Telnyx routing configuration: %w", err)
	}
	route.PhoneNumberSID = phoneID
	route.PreviousVoiceURL = string(configJSON)
	return nil
}

func (a *App) disableTelnyxRoute(ctx *sdk.AppCtx, route *routeRow) error {
	if route.PhoneNumberSID == "" || route.PreviousVoiceURL == "" {
		return errors.New("route cannot be safely disabled because its Telnyx routing configuration is unavailable")
	}
	var config telnyxRouteConfig
	if err := json.Unmarshal([]byte(route.PreviousVoiceURL), &config); err != nil || !validProviderResourceID(config.ApplicationID) {
		return errors.New("route contains invalid saved Telnyx routing configuration")
	}
	if config.PreviousConnectionID == "" {
		return errors.New("route cannot restore the Telnyx number because its previous connection is unknown")
	}
	if _, err := executeCarrierTool(ctx, route.CarrierConnectionID, "update_phone_number", map[string]any{
		"id": route.PhoneNumberSID, "connection_id": config.PreviousConnectionID,
	}); err != nil {
		return fmt.Errorf("restore Telnyx phone number connection: %w", err)
	}
	if _, err := executeCarrierTool(ctx, route.CarrierConnectionID, "delete_call_control_application", map[string]any{"id": config.ApplicationID}); err != nil {
		ctx.Logger().Warn("delete restored Telnyx inbound application", "application_id", config.ApplicationID, "err", err)
	}
	return nil
}

func (a *App) answerInboundCarrierCall(ctx *sdk.AppCtx, row *callRow) error {
	switch row.CarrierSlug {
	case "twilio":
		twiml := fmt.Sprintf(`<Response><Connect><Stream url="%s"/></Connect></Response>`, xmlEscape(a.publicWSStreamURL("twilio", row.ID, row.CallbackSecret)))
		_, err := executeCarrierTool(ctx, row.CarrierConnectionID, "update_call", map[string]any{
			"CallSid": row.CarrierSID, "Twiml": twiml,
		})
		return err
	case "telnyx":
		_, err := executeCarrierTool(ctx, row.CarrierConnectionID, "answer_call", map[string]any{
			"call_control_id":            row.CarrierSID,
			"stream_url":                 a.publicWSStreamURL("telnyx", row.ID, row.CallbackSecret),
			"stream_track":               "inbound_track",
			"stream_codec":               "PCMU",
			"stream_bidirectional_mode":  "rtp",
			"stream_bidirectional_codec": "PCMU",
			"webhook_url":                a.statusCallbackURL(row.ID, row.CallbackSecret, row.ProjectID),
			"webhook_url_method":         "POST",
		})
		return err
	default:
		return fmt.Errorf("unsupported inbound provider %s", row.CarrierSlug)
	}
}

func (a *App) rejectInboundCarrierCall(ctx *sdk.AppCtx, row *callRow) error {
	switch row.CarrierSlug {
	case "twilio":
		_, err := executeCarrierTool(ctx, row.CarrierConnectionID, "update_call", map[string]any{
			"CallSid": row.CarrierSID, "Status": "completed",
		})
		return err
	case "telnyx":
		_, err := executeCarrierTool(ctx, row.CarrierConnectionID, "reject_call", map[string]any{
			"call_control_id": row.CarrierSID,
		})
		return err
	default:
		return fmt.Errorf("unsupported inbound provider %s", row.CarrierSlug)
	}
}

func (a *App) killCallThread(ctx *sdk.AppCtx, row *callRow) error {
	if row == nil || row.ThreadID == "" || strings.HasPrefix(row.ThreadID, "pending-") {
		return nil
	}
	return ctx.PlatformAPI().KillThread(row.AgentID, row.ThreadID)
}

func (a *App) mediaBridgeURL(row *callRow) (string, error) {
	if row == nil {
		return "", errors.New("call unavailable")
	}
	if row.Status != "media-disconnected" {
		return row.AudioBridgeURL, nil
	}
	if globalCtx == nil || globalCtx.PlatformAPI() == nil {
		return "", errors.New("app context unavailable")
	}
	renewed, err := globalCtx.PlatformAPI().RenewRealtimeAudioBridge(row.AgentID, row.ThreadID)
	if err != nil {
		return "", fmt.Errorf("renew realtime audio bridge: %w", err)
	}
	if renewed == nil || renewed.AudioBridgeURL == "" {
		return "", errors.New("renew realtime audio bridge returned no URL")
	}
	if err := a.db().updateAudioBridgeURL(row.ID, renewed.AudioBridgeURL); err != nil {
		return "", fmt.Errorf("persist renewed audio bridge: %w", err)
	}
	row.AudioBridgeURL = renewed.AudioBridgeURL
	return row.AudioBridgeURL, nil
}

// ─── webhook + panel handlers ──────────────────────────────────────

func (a *App) handleStatusCallback(w http.ResponseWriter, r *http.Request) {
	callID := strings.TrimPrefix(r.URL.Path, "/webhook/status/")
	callID = strings.TrimSuffix(callID, "/")
	if callID == "" {
		http.Error(w, "missing call_id", http.StatusBadRequest)
		return
	}
	row, err := a.db().findCall(callID)
	if err != nil || row == nil {
		http.Error(w, "unknown call_id", http.StatusNotFound)
		return
	}
	if err := a.authorizeCallRequest(r, row); err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	status, errMsg, carrierSID := callbackStatusFor(row.CarrierSlug, r)
	if status == "" && errMsg == "invalid Telnyx callback" {
		http.Error(w, errMsg, http.StatusBadRequest)
		return
	}
	if carrierSID != "" && row.CarrierSID == "" {
		if err := a.db().updateCarrierIdentity(callID, carrierSID, row.CarrierRequestID); err != nil {
			http.Error(w, "persist carrier call id", http.StatusInternalServerError)
			return
		}
	}
	if status != "" {
		if err := a.db().updateStatus(callID, status, errMsg); err != nil {
			http.Error(w, "persist status", http.StatusInternalServerError)
			return
		}
	}
	switch status {
	case "completed", "failed", "no-answer", "busy", "canceled":
		row, _ = a.db().findCall(callID)
		if row != nil && row.ThreadID != "" && globalCtx != nil {
			_ = a.killCallThread(globalCtx, row)
		}
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleTwilioInbound(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
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
	if route == nil || !route.Enabled || route.Secret == "" || !secureEqual(r.URL.Query().Get("secret"), route.Secret) {
		http.Error(w, "route not found", http.StatusNotFound)
		return
	}
	if err := a.verifyTwilioRequest(r, route.CarrierConnectionID); err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	callSID := firstNonEmpty(r.FormValue("CallSid"), r.URL.Query().Get("CallSid"))
	from := firstNonEmpty(r.FormValue("From"), r.URL.Query().Get("From"))
	to := firstNonEmpty(r.FormValue("To"), r.URL.Query().Get("To"), route.PhoneNumber)
	if callSID == "" {
		http.Error(w, "missing CallSid", http.StatusBadRequest)
		return
	}
	if to != route.PhoneNumber {
		http.Error(w, "called number does not match route", http.StatusForbidden)
		return
	}
	stored, created, err := a.recordInboundCall(route, callSID, from, to)
	if err != nil {
		http.Error(w, "persist call: "+err.Error(), http.StatusInternalServerError)
		return
	}
	writeTwilioHold(w, a.holdText(*route), a.twilioWaitURL(*route, stored.ID))
	if created {
		a.enqueueImmediateAnswer(route, stored.ID)
	}
}

func (a *App) recordInboundCall(route *routeRow, carrierSID, from, to string) (*callRow, bool, error) {
	callID := newCallID()
	now := time.Now().UTC()
	call := callRow{
		ID:                  callID,
		ThreadID:            "pending-" + callID,
		Direction:           "inbound",
		AgentID:             route.AgentID,
		RouteID:             route.ID,
		CarrierSID:          carrierSID,
		CarrierSlug:         route.CarrierSlug,
		CarrierConnectionID: route.CarrierConnectionID,
		CallbackSecret:      newSecret(),
		ToNumber:            to,
		FromNumber:          from,
		Directive:           "inbound pending",
		Voice:               "",
		AudioBridgeURL:      "pending",
		Status:              "pending",
		PlacedAt:            now.Format(time.RFC3339),
		ProjectID:           route.ProjectID,
		StateExpiresAt:      now.Add(time.Duration(route.TimeoutSec) * time.Second).Format(time.RFC3339),
		DeadlineAt:          now.Add(time.Hour).Format(time.RFC3339),
	}
	msg := fmt.Sprintf(
		"Incoming phone call. call_id=%s from=%s to=%s. To answer: call telephony_answer_call with call_id=%q and a directive for the realtime call thread. To decline: call telephony_reject_call with call_id=%q.",
		callID, from, to, callID, callID,
	)
	if route.AnswerMode == answerModeRealtimeImmediate {
		msg = fmt.Sprintf(
			"Incoming phone call. call_id=%s from=%s to=%s. Telephony is immediately starting the route's configured realtime thread; this event is informational and does not require telephony_answer_call.",
			callID, from, to,
		)
	}
	stored, created, err := a.db().insertInboundCallWithEvent(call, msg)
	if err != nil {
		return nil, false, err
	}
	if created && globalCtx != nil {
		globalCtx.WithProject(route.ProjectID).Emit("call.incoming", callEventPublic(*stored))
	}
	// Immediate routes do not need the main agent to make a pickup decision.
	// Leave their informational event in the durable outbox so answering stays
	// on the carrier request's fast path; the lifecycle worker delivers it.
	if globalCtx != nil && route.AnswerMode != answerModeRealtimeImmediate {
		if err := a.deliverOutboxCall(globalCtx.WithProject(route.ProjectID), stored.ID); err != nil {
			globalCtx.Logger().Warn("send incoming call event deferred", "route", route.ID, "agent", route.AgentID, "err", err)
		}
	}
	return stored, created, nil
}

func (a *App) handleTelnyxInbound(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		w.Header().Set("Allow", http.MethodPost)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	routeID := strings.Trim(strings.TrimPrefix(r.URL.Path, "/inbound/telnyx/"), "/")
	if routeID == "" || strings.Contains(routeID, "/") {
		http.Error(w, "missing route_id", http.StatusBadRequest)
		return
	}
	route, err := a.db().findRoute(routeID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if route == nil || route.CarrierSlug != "telnyx" || !route.Enabled || route.Secret == "" || !secureEqual(r.URL.Query().Get("secret"), route.Secret) {
		http.Error(w, "route not found", http.StatusNotFound)
		return
	}
	if projectID := r.URL.Query().Get("project_id"); projectID == "" || projectID != route.ProjectID {
		http.Error(w, "project does not match route", http.StatusForbidden)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "read Telnyx webhook", http.StatusBadRequest)
		return
	}
	if err := a.verifyTelnyxInboundRequest(r, route, body); err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
		return
	}
	var event struct {
		Data struct {
			EventType string `json:"event_type"`
			Payload   struct {
				CallControlID string `json:"call_control_id"`
				CallLegID     string `json:"call_leg_id"`
				ConnectionID  string `json:"connection_id"`
				Direction     string `json:"direction"`
				From          string `json:"from"`
				To            string `json:"to"`
			} `json:"payload"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &event); err != nil {
		http.Error(w, "invalid Telnyx webhook", http.StatusBadRequest)
		return
	}
	if event.Data.EventType != "call.initiated" || (event.Data.Payload.Direction != "" && event.Data.Payload.Direction != "incoming") {
		w.WriteHeader(http.StatusNoContent)
		return
	}
	var config telnyxRouteConfig
	if json.Unmarshal([]byte(route.PreviousVoiceURL), &config) != nil || event.Data.Payload.ConnectionID != config.ApplicationID {
		http.Error(w, "webhook connection does not match route", http.StatusForbidden)
		return
	}
	carrierSID := firstNonEmpty(event.Data.Payload.CallControlID, event.Data.Payload.CallLegID)
	to := firstNonEmpty(event.Data.Payload.To, route.PhoneNumber)
	if carrierSID == "" {
		http.Error(w, "missing call control id", http.StatusBadRequest)
		return
	}
	if to != route.PhoneNumber {
		http.Error(w, "called number does not match route", http.StatusForbidden)
		return
	}
	stored, created, err := a.recordInboundCall(route, carrierSID, event.Data.Payload.From, to)
	if err != nil {
		http.Error(w, "persist call: "+err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
	if created {
		a.enqueueImmediateAnswer(route, stored.ID)
	}
}

func decodeTelnyxSignatureValue(value string) ([]byte, error) {
	value = strings.TrimSpace(value)
	decoded, err := base64.StdEncoding.DecodeString(value)
	if err == nil {
		return decoded, nil
	}
	return base64.RawStdEncoding.DecodeString(value)
}

func (a *App) verifyTelnyxInboundRequest(r *http.Request, route *routeRow, body []byte) error {
	bound := globalCtx.WithProject(route.ProjectID)
	creds, err := bound.PlatformAPI().GetConnectionCredentials(route.CarrierConnectionID)
	if err != nil {
		return fmt.Errorf("read Telnyx credentials: %w", err)
	}
	publicKeyText := strings.TrimSpace(creds.Fields["public_key"])
	if publicKeyText == "" {
		// The route secret and dedicated application ID are still checked.
		// Accounts should configure public_key to enable carrier signatures.
		return nil
	}
	return verifyTelnyxSignature(publicKeyText, r.Header.Get("Telnyx-Timestamp"), r.Header.Get("Telnyx-Signature-Ed25519"), body, time.Now())
}

func verifyTelnyxSignature(publicKeyText, timestamp, signatureText string, body []byte, now time.Time) error {
	timestamp = strings.TrimSpace(timestamp)
	signatureText = strings.TrimSpace(signatureText)
	seconds, err := strconv.ParseInt(timestamp, 10, 64)
	if err != nil || now.Sub(time.Unix(seconds, 0)) > 5*time.Minute || time.Unix(seconds, 0).Sub(now) > 5*time.Minute {
		return errors.New("invalid or stale Telnyx timestamp")
	}
	publicKey, err := decodeTelnyxSignatureValue(publicKeyText)
	if err != nil || len(publicKey) != ed25519.PublicKeySize {
		return errors.New("invalid Telnyx public key")
	}
	signature, err := decodeTelnyxSignatureValue(signatureText)
	if err != nil || len(signature) != ed25519.SignatureSize {
		return errors.New("invalid Telnyx signature")
	}
	message := append([]byte(timestamp+"|"), body...)
	if !ed25519.Verify(ed25519.PublicKey(publicKey), message, signature) {
		return errors.New("invalid Telnyx signature")
	}
	return nil
}

func (a *App) handleTwilioInboundWait(w http.ResponseWriter, r *http.Request, routeID string) {
	route, err := a.db().findRoute(routeID)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	if route == nil || !route.Enabled || route.Secret == "" || !secureEqual(r.URL.Query().Get("secret"), route.Secret) {
		http.Error(w, "route not found", http.StatusNotFound)
		return
	}
	if err := a.verifyTwilioRequest(r, route.CarrierConnectionID); err != nil {
		http.Error(w, "forbidden", http.StatusForbidden)
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
	if row.RouteID != route.ID || row.CarrierSID != r.FormValue("CallSid") {
		http.Error(w, "call does not belong to route", http.StatusForbidden)
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
	case "answering", "answered", "in-progress", "initiated", "ringing":
		// The answer tool updates the live Twilio call directly. If a
		// pending redirect was already queued, keep the call open briefly.
		w.Header().Set("Content-Type", "application/xml")
		_, _ = w.Write([]byte(`<Response><Pause length="1"/></Response>`))
	default:
		writeTwilioSayHangup(w, "This call has ended.")
	}
}

func (a *App) handleListCalls(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.Header().Set("Allow", http.MethodGet)
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	project, err := a.panelProject(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	rows, err := a.db().recent(project, 100)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	writeJSON(w, map[string]any{"calls": callsPanelPublic(rows)})
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
	project, err := a.panelProject(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if msg := a.hangupCall(globalCtx.WithProject(project), parts[1], 0, project); msg != "" {
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

func (a *App) publicInstalledAppURL() string {
	base := a.publicBase()
	if base == "" || a.installID <= 0 {
		return ""
	}
	return fmt.Sprintf("%s/api/apps/telephony/_install/%d", base, a.installID)
}

// publicWSStreamURL builds the wss:// URL a carrier dials for Media
// Streams. Real carriers require wss (TLS); a public_url over plain
// http is only useful for local mock testing.
func (a *App) publicWSStreamURL(provider, callID, secret string) string {
	base := a.publicInstalledAppURL()
	path := "/media/" + url.PathEscape(provider) + "/" + url.PathEscape(callID) + "/" + url.PathEscape(secret)
	if strings.HasPrefix(base, "https://") {
		return "wss://" + strings.TrimPrefix(base, "https://") + path
	}
	return "ws://" + strings.TrimPrefix(base, "http://") + path
}

func (a *App) statusCallbackURL(callID, secret, projectID string) string {
	query := url.Values{"token": {secret}, "project_id": {projectID}}.Encode()
	return a.publicAppURL() + "/webhook/status/" + url.PathEscape(callID) + "?" + query
}

func (a *App) plivoXMLURL(callID, secret, projectID string) string {
	query := url.Values{"token": {secret}, "project_id": {projectID}}.Encode()
	return a.publicAppURL() + "/xml/plivo/" + url.PathEscape(callID) + "?" + query
}

func (a *App) inboundRouteURL(route routeRow) string {
	query := url.Values{"secret": {route.Secret}, "project_id": {route.ProjectID}}.Encode()
	return fmt.Sprintf("%s/inbound/%s/%s?%s",
		a.publicAppURL(),
		route.CarrierSlug,
		route.ID,
		query,
	)
}

func (a *App) twilioWaitURL(route routeRow, callID string) string {
	query := url.Values{"secret": {route.Secret}, "call_id": {callID}, "project_id": {route.ProjectID}}.Encode()
	return fmt.Sprintf("%s/inbound/twilio/%s/wait?%s",
		a.publicAppURL(),
		route.ID,
		query,
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
		"phone_number_id":       r.PhoneNumberSID,
		"phone_number_sid":      r.PhoneNumberSID,
		"agent_id":              r.AgentID,
		"enabled":               r.Enabled,
		"hold_prompt":           r.HoldPrompt,
		"timeout_sec":           r.TimeoutSec,
		"answer_mode":           firstNonEmpty(r.AnswerMode, answerModeAgent),
		"directive":             r.AutoDirective,
		"voice":                 r.AutoVoice,
		"greeting":              r.AutoGreeting,
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

func callsPanelPublic(rows []callRow) []map[string]any {
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, map[string]any{
			"id": r.ID, "thread_id": r.ThreadID, "carrier_sid": r.CarrierSID,
			"direction": r.Direction, "to_number": r.ToNumber, "from_number": r.FromNumber,
			"directive": r.Directive, "voice": r.Voice, "status": r.Status,
			"placed_at": r.PlacedAt, "answered_at": r.AnsweredAt, "ended_at": r.EndedAt,
			"project_id": r.ProjectID, "error_message": r.ErrorMessage,
		})
	}
	return out
}

func callEventPublic(r callRow) map[string]any {
	return map[string]any{
		"call_id": r.ID, "direction": r.Direction, "agent_id": r.AgentID,
		"route_id": r.RouteID, "carrier": r.CarrierSlug, "to": r.ToNumber,
		"from": r.FromNumber, "status": r.Status, "placed_at": r.PlacedAt,
	}
}

func (a *App) panelProject(r *http.Request) (string, error) {
	requested := strings.TrimSpace(r.URL.Query().Get("project_id"))
	forwarded := strings.TrimSpace(r.Header.Get("X-Apteva-Project-ID"))
	installed := currentProject(globalCtx)
	if installed != "" {
		if (requested != "" && requested != installed) || (forwarded != "" && forwarded != installed) {
			return "", errors.New("project does not match app installation")
		}
		return installed, nil
	}
	if forwarded != "" {
		if requested != "" && requested != forwarded {
			return "", errors.New("project query does not match trusted proxy context")
		}
		return forwarded, nil
	}
	if requested == "" {
		return "", errors.New("project_id required for a global installation")
	}
	return requested, nil
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

func callbackStatusFor(carrier string, r *http.Request) (string, string, string) {
	if carrier != "telnyx" {
		status, errMsg := callbackStatus(r)
		carrierSID := firstNonEmpty(r.FormValue("CallSid"), r.FormValue("CallUUID"), r.FormValue("uuid"))
		return status, errMsg, carrierSID
	}
	var body struct {
		Data struct {
			EventType string `json:"event_type"`
			Payload   struct {
				CallControlID string `json:"call_control_id"`
				HangupCause   string `json:"hangup_cause"`
				HangupSource  string `json:"hangup_source"`
			} `json:"payload"`
		} `json:"data"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 1<<20)).Decode(&body); err != nil {
		return "", "invalid Telnyx callback", ""
	}
	status := ""
	switch body.Data.EventType {
	case "call.initiated":
		status = "initiated"
	case "call.ringing":
		status = "ringing"
	case "call.answered":
		status = "answered"
	case "streaming.started", "call.streaming.started":
		status = "in-progress"
	case "streaming.stopped", "call.streaming.stopped":
		status = "media-disconnected"
	case "call.hangup":
		status = "completed"
	}
	errMsg := firstNonEmpty(body.Data.Payload.HangupCause, body.Data.Payload.HangupSource)
	return status, errMsg, body.Data.Payload.CallControlID
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
	case "media-disconnected", "answering":
		return strings.ToLower(strings.TrimSpace(status))
	default:
		return ""
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

func optionalStringArg(m map[string]any, key string) (string, bool) {
	v, ok := m[key].(string)
	return strings.TrimSpace(v), ok
}

func normalizeRouteAnswerConfig(mode, directive, voice, greeting string) (string, string, string, string, error) {
	mode = strings.ToLower(strings.TrimSpace(mode))
	directive = strings.TrimSpace(directive)
	voice = strings.TrimSpace(voice)
	greeting = strings.TrimSpace(greeting)
	if mode != answerModeAgent && mode != answerModeRealtimeImmediate {
		return "", "", "", "", errors.New("answer_mode must be agent or realtime_immediate")
	}
	if len(directive) > 16000 {
		return "", "", "", "", errors.New("directive must be at most 16000 characters")
	}
	if !validVoice(voice) {
		return "", "", "", "", errors.New("voice must be a provider voice id of at most 64 characters")
	}
	if len(greeting) > 500 {
		return "", "", "", "", errors.New("greeting must be at most 500 characters")
	}
	if mode == answerModeRealtimeImmediate {
		if directive == "" {
			return "", "", "", "", errors.New("directive is required for realtime_immediate answering")
		}
		if greeting == "" {
			greeting = defaultInboundGreeting
		}
	}
	return mode, directive, voice, greeting, nil
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

func newCallID() string {
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("secure random source unavailable: " + err.Error())
	}
	return fmt.Sprintf("%x", b[:])
}

func newSecret() string {
	var b [32]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("secure random source unavailable: " + err.Error())
	}
	return fmt.Sprintf("%x", b[:])
}

// ─── DB layer ──────────────────────────────────────────────────────

type callRow struct {
	ID                  string
	ThreadID            string
	Direction           string
	AgentID             int64
	RouteID             string
	CarrierSID          string
	CarrierRequestID    string
	CarrierSlug         string
	CarrierConnectionID int64
	CallbackSecret      string
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
	IdempotencyKey      string
	StateExpiresAt      string
	DeadlineAt          string
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
	AnswerMode          string
	AutoDirective       string
	AutoVoice           string
	AutoGreeting        string
	Secret              string
	PreviousVoiceURL    string
	CreatedAt           string
	UpdatedAt           string
}

type callsDB struct{ db *sql.DB }

const callSelectColumns = `id, thread_id,
    COALESCE(direction,'outbound'), COALESCE(agent_id,0), COALESCE(route_id,''),
    COALESCE(carrier_sid,''), COALESCE(carrier_request_id,''),
    COALESCE(carrier_slug,'twilio'), COALESCE(carrier_connection_id,0), COALESCE(callback_secret,''),
    to_number, from_number, directive, voice, audio_bridge_url, status,
    placed_at, COALESCE(answered_at,''), COALESCE(ended_at,''),
    project_id, COALESCE(error_message,''), COALESCE(idempotency_key,''),
    COALESCE(state_expires_at,''), COALESCE(deadline_at,'')`

type rowScanner interface{ Scan(dest ...any) error }

func scanCall(row rowScanner) (*callRow, error) {
	var r callRow
	if err := row.Scan(&r.ID, &r.ThreadID, &r.Direction, &r.AgentID, &r.RouteID,
		&r.CarrierSID, &r.CarrierRequestID, &r.CarrierSlug, &r.CarrierConnectionID, &r.CallbackSecret,
		&r.ToNumber, &r.FromNumber, &r.Directive, &r.Voice, &r.AudioBridgeURL, &r.Status,
		&r.PlacedAt, &r.AnsweredAt, &r.EndedAt, &r.ProjectID, &r.ErrorMessage,
		&r.IdempotencyKey, &r.StateExpiresAt, &r.DeadlineAt); err != nil {
		return nil, err
	}
	return &r, nil
}

func (c *callsDB) insertCall(r callRow) error {
	_, err := c.db.Exec(`INSERT INTO calls
	        (id, thread_id, direction, agent_id, route_id, carrier_sid, carrier_request_id,
	         carrier_slug, carrier_connection_id, callback_secret, to_number, from_number,
	         directive, voice, audio_bridge_url, status, placed_at, project_id,
	         idempotency_key, state_expires_at, deadline_at)
	        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.ThreadID, r.Direction, r.AgentID, r.RouteID, r.CarrierSID, r.CarrierRequestID,
		r.CarrierSlug, r.CarrierConnectionID, r.CallbackSecret,
		r.ToNumber, r.FromNumber, r.Directive, r.Voice, r.AudioBridgeURL,
		r.Status, r.PlacedAt, r.ProjectID, r.IdempotencyKey, r.StateExpiresAt, r.DeadlineAt,
	)
	return err
}

func (c *callsDB) findCall(id string) (*callRow, error) {
	r, err := scanCall(c.db.QueryRow(`SELECT `+callSelectColumns+` FROM calls WHERE id = ?`, id))
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return r, nil
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

func (c *callsDB) updateCarrierIdentity(id, carrierSID, requestID string) error {
	_, err := c.db.Exec(`UPDATE calls SET
        carrier_sid = COALESCE(NULLIF(?, ''), carrier_sid),
        carrier_request_id = COALESCE(NULLIF(?, ''), carrier_request_id)
        WHERE id = ?`, carrierSID, requestID, id)
	return err
}

func (c *callsDB) updateAudioBridgeURL(id, bridgeURL string) error {
	_, err := c.db.Exec(`UPDATE calls SET audio_bridge_url = ? WHERE id = ?`, bridgeURL, id)
	return err
}

func (c *callsDB) updateStatus(id, status, errMsg string) error {
	tx, err := c.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var current string
	if err := tx.QueryRow(`SELECT status FROM calls WHERE id = ?`, id).Scan(&current); err != nil {
		return err
	}
	if !canTransitionStatus(current, status) {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	end := ""
	answered := ""
	if isTerminalStatus(status) {
		end = now
	}
	if status == "answered" || status == "in-progress" {
		answered = now
	}
	_, err = tx.Exec(`UPDATE calls SET status = ?,
        error_message = CASE WHEN ? <> '' THEN ? ELSE error_message END,
        answered_at = COALESCE(answered_at, NULLIF(?, '')),
        ended_at = COALESCE(ended_at, NULLIF(?, '')),
        media_active = CASE WHEN ? THEN 0 ELSE media_active END
        WHERE id = ? AND status = ?`,
		status, errMsg, errMsg, answered, end, isTerminalStatus(status), id, current)
	if err != nil {
		return err
	}
	if isTerminalStatus(status) {
		if _, err := tx.Exec(`UPDATE inbound_event_outbox
            SET delivered_at = COALESCE(NULLIF(delivered_at, ''), ?), last_error = ''
            WHERE call_id = ?`, now, id); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func isTerminalStatus(status string) bool {
	switch status {
	case "completed", "failed", "no-answer", "busy", "canceled":
		return true
	default:
		return false
	}
}

func canTransitionStatus(from, to string) bool {
	if to == "" || isTerminalStatus(from) {
		return false
	}
	if from == to || isTerminalStatus(to) {
		return true
	}
	allowed := map[string]map[string]bool{
		"pending":            {"answering": true},
		"answering":          {"answered": true, "in-progress": true, "media-disconnected": true, "pending": true},
		"initiated":          {"ringing": true, "answered": true, "in-progress": true, "media-disconnected": true},
		"ringing":            {"answered": true, "in-progress": true, "media-disconnected": true},
		"answered":           {"in-progress": true, "media-disconnected": true},
		"in-progress":        {"media-disconnected": true},
		"media-disconnected": {"in-progress": true},
	}
	return allowed[from][to]
}

func (c *callsDB) claimPendingCall(id string, agentID int64, project string) (bool, error) {
	res, err := c.db.Exec(`UPDATE calls SET status = 'answering'
        WHERE id = ? AND direction = 'inbound' AND status = 'pending' AND agent_id = ? AND project_id = ?`,
		id, agentID, project)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (c *callsDB) releaseAnswerClaim(id string) error {
	_, err := c.db.Exec(`UPDATE calls SET status = 'pending'
        WHERE id = ? AND status = 'answering' AND thread_id LIKE 'pending-%'`, id)
	return err
}

func (c *callsDB) resetAnswerClaim(id string) error {
	_, err := c.db.Exec(`UPDATE calls SET status = 'pending', thread_id = 'pending-' || id,
        audio_bridge_url = 'pending', directive = 'inbound pending', voice = ''
        WHERE id = ? AND status = 'answering' AND media_active = 0`, id)
	return err
}

func (c *callsDB) attachCall(id, threadID, audioBridgeURL, directive, voice string) error {
	res, err := c.db.Exec(`UPDATE calls SET thread_id = ?, audio_bridge_url = ?, directive = ?, voice = ?
        WHERE id = ? AND status = 'answering'`, threadID, audioBridgeURL, directive, voice, id)
	if err != nil {
		return err
	}
	n, err := res.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return errors.New("call answer claim was lost")
	}
	return nil
}

func (c *callsDB) listActiveForAgent(agentID int64, project string) ([]callRow, error) {
	return c.listWhere(`status IN ('initiated','ringing','in-progress','answered','pending','answering','media-disconnected')
        AND agent_id = ? AND project_id = ? ORDER BY placed_at DESC`, agentID, project)
}

func (c *callsDB) listPending(agentID int64, project string) ([]callRow, error) {
	return c.listWhere(`direction = 'inbound' AND status IN ('pending','answering') AND agent_id = ? AND project_id = ? ORDER BY placed_at DESC`,
		agentID, project)
}

func (c *callsDB) recent(project string, limit int) ([]callRow, error) {
	return c.listWhere(`(? = '' OR project_id = ?) ORDER BY placed_at DESC LIMIT `+fmt.Sprintf("%d", limit),
		project, project)
}

func (c *callsDB) listWhere(where string, argv ...any) ([]callRow, error) {
	rows, err := c.db.Query(`SELECT `+callSelectColumns+` FROM calls WHERE `+where, argv...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []callRow
	for rows.Next() {
		r, err := scanCall(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *r)
	}
	return out, rows.Err()
}

func (c *callsDB) findOutboundByIdempotency(agentID int64, project, key string) (*callRow, error) {
	row := c.db.QueryRow(`SELECT id FROM calls WHERE direction = 'outbound' AND agent_id = ? AND project_id = ? AND idempotency_key = ?`, agentID, project, key)
	var id string
	if err := row.Scan(&id); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return c.findCall(id)
}

func (c *callsDB) countOutboundSince(agentID int64, project string, since time.Time) (int, error) {
	var count int
	err := c.db.QueryRow(`SELECT COUNT(*) FROM calls
        WHERE direction = 'outbound' AND agent_id = ? AND project_id = ? AND placed_at >= ?`,
		agentID, project, since.UTC().Format(time.RFC3339)).Scan(&count)
	return count, err
}

func (c *callsDB) claimMedia(id string) (bool, error) {
	res, err := c.db.Exec(`UPDATE calls SET media_active = 1
        WHERE id = ? AND media_active = 0 AND status NOT IN ('completed','failed','no-answer','busy','canceled')`, id)
	if err != nil {
		return false, err
	}
	n, err := res.RowsAffected()
	return n == 1, err
}

func (c *callsDB) releaseMedia(id string) error {
	_, err := c.db.Exec(`UPDATE calls SET media_active = 0 WHERE id = ?`, id)
	return err
}

func (c *callsDB) setStateExpiry(id string, at time.Time) error {
	_, err := c.db.Exec(`UPDATE calls SET state_expires_at = ? WHERE id = ?`, at.UTC().Format(time.RFC3339), id)
	return err
}

func (c *callsDB) clearStateExpiry(id string) error {
	_, err := c.db.Exec(`UPDATE calls SET state_expires_at = '' WHERE id = ?`, id)
	return err
}

func (c *callsDB) insertRoute(r routeRow) error {
	enabled := 0
	if r.Enabled {
		enabled = 1
	}
	_, err := c.db.Exec(`INSERT INTO inbound_routes
	        (id, project_id, carrier_slug, carrier_connection_id, phone_number, phone_number_sid,
	         agent_id, enabled, hold_prompt, timeout_sec, answer_mode, auto_directive, auto_voice,
	         auto_greeting, secret, previous_voice_url, created_at, updated_at)
	        VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		r.ID, r.ProjectID, r.CarrierSlug, r.CarrierConnectionID, r.PhoneNumber, r.PhoneNumberSID,
		r.AgentID, enabled, r.HoldPrompt, r.TimeoutSec, firstNonEmpty(r.AnswerMode, answerModeAgent),
		r.AutoDirective, r.AutoVoice, r.AutoGreeting, r.Secret, r.PreviousVoiceURL, r.CreatedAt, r.UpdatedAt,
	)
	return err
}

func (c *callsDB) findRoute(id string) (*routeRow, error) {
	row := c.db.QueryRow(`SELECT id, project_id, carrier_slug, carrier_connection_id, phone_number,
	        phone_number_sid, agent_id, enabled, hold_prompt, timeout_sec,
	        COALESCE(answer_mode,'agent'), COALESCE(auto_directive,''), COALESCE(auto_voice,''),
	        COALESCE(auto_greeting,''), secret,
	        COALESCE(previous_voice_url,''), created_at, updated_at
	        FROM inbound_routes WHERE id = ?`, id)
	var r routeRow
	var enabled int
	if err := row.Scan(&r.ID, &r.ProjectID, &r.CarrierSlug, &r.CarrierConnectionID, &r.PhoneNumber,
		&r.PhoneNumberSID, &r.AgentID, &enabled, &r.HoldPrompt, &r.TimeoutSec,
		&r.AnswerMode, &r.AutoDirective, &r.AutoVoice, &r.AutoGreeting, &r.Secret,
		&r.PreviousVoiceURL, &r.CreatedAt, &r.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	r.Enabled = enabled != 0
	return &r, nil
}

func (c *callsDB) findRouteByNumber(connectionID int64, phone string) (*routeRow, error) {
	var id string
	err := c.db.QueryRow(`SELECT id FROM inbound_routes
        WHERE carrier_connection_id = ? AND phone_number = ? AND enabled = 1
        ORDER BY created_at DESC LIMIT 1`, connectionID, phone).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return c.findRoute(id)
}

func (c *callsDB) listRoutesForAgent(agentID int64, project string) ([]routeRow, error) {
	rows, err := c.db.Query(`SELECT id, project_id, carrier_slug, carrier_connection_id, phone_number,
	        phone_number_sid, agent_id, enabled, hold_prompt, timeout_sec,
	        COALESCE(answer_mode,'agent'), COALESCE(auto_directive,''), COALESCE(auto_voice,''),
	        COALESCE(auto_greeting,''), secret,
	        COALESCE(previous_voice_url,''), created_at, updated_at
	        FROM inbound_routes WHERE agent_id = ? AND project_id = ? ORDER BY created_at DESC`, agentID, project)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []routeRow
	for rows.Next() {
		var r routeRow
		var enabled int
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.CarrierSlug, &r.CarrierConnectionID, &r.PhoneNumber,
			&r.PhoneNumberSID, &r.AgentID, &enabled, &r.HoldPrompt, &r.TimeoutSec,
			&r.AnswerMode, &r.AutoDirective, &r.AutoVoice, &r.AutoGreeting, &r.Secret,
			&r.PreviousVoiceURL, &r.CreatedAt, &r.UpdatedAt); err != nil {
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

func (c *callsDB) updateRoutePreviousVoiceURL(id, voiceURL string) error {
	_, err := c.db.Exec(`UPDATE inbound_routes SET previous_voice_url = ?, updated_at = ? WHERE id = ?`,
		voiceURL, time.Now().UTC().Format(time.RFC3339), id)
	return err
}

func (c *callsDB) updateRouteAnswerMode(id, mode, directive, voice, greeting string) error {
	_, err := c.db.Exec(`UPDATE inbound_routes SET answer_mode = ?, auto_directive = ?,
        auto_voice = ?, auto_greeting = ?, updated_at = ? WHERE id = ?`,
		mode, directive, voice, greeting, time.Now().UTC().Format(time.RFC3339), id)
	return err
}

func (c *callsDB) disableRoute(id string) error {
	_, err := c.db.Exec(`UPDATE inbound_routes SET enabled = 0, updated_at = ? WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339), id)
	return err
}
