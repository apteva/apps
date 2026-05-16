// Messaging v0.1 — channel-agnostic send/receive built on a unified
// `messages` table. v0.1 ships email via AWS SES; SMS/push reserved.
//
// Architecture:
//   - Manifest declares one required integration (role=email_provider,
//     compatible_slugs=[aws-ses], capability=email.send→send_email)
//     and one optional app dependency (storage, for attachments).
//   - send_message resolves recipient URIs to channels (mailto: → email),
//     checks suppression + idempotency, calls the bound provider via
//     ctx.PlatformAPI().ExecuteIntegrationTool, and persists a row.
//   - Bounce/complaint SNS webhooks land at /webhooks/ses-bounces,
//     update the message row, append delivery_events, and auto-add
//     to the suppression list for hard bounces and complaints.
//   - Inbound SNS webhooks land at /webhooks/ses-inbound, parse the
//     embedded MIME (SES "Content" action; S3-action fetch is v0.2),
//     persist a `direction='in'` row, look up `inbound_routes` by
//     recipient URI, and POST a normalized JSON to the target app's
//     HTTP route via ctx.PlatformAPI().CallApp.
package main

import (
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/url"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

// ─── Manifest (also lives in apteva.yaml) ──────────────────────────

const manifestYAML = `schema: apteva-app/v1
name: messaging
display_name: Messaging
version: 0.11.3
description: |
  Send and receive messages across channels. v0.1 ships email via
  AWS SES.
author: Apteva
scopes: [project, global]
requires:
  permissions:
    - db.write.app
    - net.egress
    - platform.connections.execute
    - platform.apps.call
  integrations:
    - role: email_provider
      kind: integration
      compatible_slugs: [aws-ses]
      capabilities: [email.send]
      tools:
        email.send: send_email
      required: true
      label: "Email provider (AWS SES)"
    - role: phone_provider
      kind: integration
      compatible_slugs: [twilio]
      capabilities: [sms.send, whatsapp.send]
      tools:
        sms.send: send_sms
        whatsapp.send: send_whatsapp
      required: false
      label: "Phone provider (SMS + WhatsApp via Twilio)"
    - role: storage
      kind: app
      compatible_app_names: [storage]
      capabilities: [files.write]
      required: false
      label: "Storage (optional)"
    - role: domains
      kind: app
      compatible_app_names: [domains]
      capabilities: [dns.upsert_record]
      required: false
      label: "Domains (optional)"
    - role: inbound_storage
      kind: integration
      compatible_slugs: [aws-s3]
      capabilities: [files.read, files.write]
      tools:
        files.read: get_object
        files.write: put_object
      required: false
      label: "Inbound storage (AWS S3)"
    - role: inbound_notifications
      kind: integration
      compatible_slugs: [aws-sns]
      capabilities: [topic.manage, topic.subscribe]
      tools:
        topic.manage: set_topic_attributes
        topic.subscribe: subscribe
      required: false
      label: "Inbound notifications (AWS SNS)"
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: send_message,           description: "Send a message. Channel is an explicit arg (email|sms|whatsapp)." }
    - { name: send_message_template,  description: "Render a saved template + send." }
    - { name: message_get,            description: "Fetch one message by id." }
    - { name: message_list,           description: "List messages with filters." }
    - { name: inbound_redispatch,     description: "Re-attempt routing for an inbound message." }
    - { name: inbound_route_set,      description: "Bind a recipient pattern to an app+route." }
    - { name: inbound_route_list,     description: "List configured inbound routes." }
    - { name: inbound_route_delete,   description: "Remove an inbound route." }
    - { name: template_create,        description: "Create a template." }
    - { name: template_update,        description: "Update a template (partial)." }
    - { name: template_get,           description: "Fetch a template." }
    - { name: template_list,          description: "List templates." }
    - { name: template_delete,        description: "Delete a template." }
    - { name: suppression_list,       description: "List suppressed addresses." }
    - { name: suppression_add,        description: "Suppress a recipient." }
    - { name: suppression_remove,     description: "Remove an address from suppression." }
    - { name: suppression_check,      description: "Single-row suppression lookup; returns {suppressed, reason?, source?, suppressed_at?}." }
    - { name: senders_list,           description: "List sending identities. Returns canonical URI rows." }
    - { name: senders_get,            description: "Get one identity's verification + DKIM state." }
    - { name: senders_delete,         description: "Remove a sending identity from the provider." }
    - { name: senders_get_quota,      description: "Provider sandbox + send-quota status." }
    - { name: senders_create,         description: "Register a sender across email (SES) + SMS (Twilio). Domain → DKIM + DNS + optional inbound bootstrap. Phone → adopt + optional SmsUrl wiring." }
    - { name: senders_refresh,        description: "Reconcile local senders with bound providers." }
    - { name: senders_set_default,    description: "Flip the per-(project, channel) default sender." }
  ui_panels:
    - slot: project.page
      label: Messaging
      icon: mail
      entry: /ui/MessagingPanel.mjs
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/messaging
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/messaging.db
  migrations: migrations/
upgrade_policy: auto-patch
`

// ─── App ───────────────────────────────────────────────────────────

type App struct{}

var globalCtx *sdk.AppCtx

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("messaging requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("messaging mounted",
		"scope_project_id", os.Getenv("APTEVA_PROJECT_ID"))
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/webhooks/ses-bounces", Handler: a.handleBounceWebhook},
		{Pattern: "/webhooks/ses-inbound", Handler: a.handleInboundWebhook},
		{Pattern: "/webhooks/twilio-inbound", Handler: a.handleTwilioInboundWebhook},
		{Pattern: "/messages", Handler: a.handleMessagesList},
		{Pattern: "/messages/", Handler: a.handleMessageItem},
		{Pattern: "/templates", Handler: a.handleTemplatesList},
		{Pattern: "/inbound-routes", Handler: a.handleInboundRoutesList},
		{Pattern: "/suppressions", Handler: a.handleSuppressionsList},
		{Pattern: "/senders", Handler: a.handleSendersList},
		{Pattern: "/senders/quota", Handler: a.handleSendersQuota},
		{Pattern: "/senders/domains", Handler: a.handleSendersDomains},
		// Internal/panel routes for provider-template sync. Not MCP —
		// the panel hits these from a button + per-row action; agents
		// don't trigger Twilio list calls.
		{Pattern: "/templates/sync", Handler: a.handleTemplatesSync},
		// Unified sender registration. Email → SES verify_email. Domain
		// → verify_domain + DNS publish + (auto if aws-s3 + aws-sns
		// bound) full inbound bootstrap (S3 + SNS + receipt rule + MX
		// + webhook subscribe). Idempotent. Mirrors the senders_create
		// MCP tool.
		{Pattern: "/senders/create", Handler: a.handleSendersCreate},
		{Pattern: "/templates/", Handler: a.handleTemplateItem}, // /templates/<id>/refresh-status
		// Generic dispatcher so the panel can invoke any MCP tool via
		// HTTP — saves declaring a per-tool route for every mutation.
		// Body: {"tool": "<name>", "args": {...}}.
		{Pattern: "/tools/call", Handler: a.handleToolsCall},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name: "send_message",
			Description: "Send a message. Args: channel (email|sms|whatsapp), from, to (string|string[]), body. " +
				"Email-only fields: subject, body_html, cc, bcc, reply_to, headers, attachment_storage_ids. " +
				"SMS/WhatsApp-only fields: media_url, content_sid, content_variables. " +
				"Common: template_id, vars, idempotency_key. " +
				"Addresses are plain — emails (alice@x.com) and E.164 phone numbers (+15551234567), no scheme prefix. " +
				"Returns {id, channel, status, recipients:[{address, status}], provider_message_id?}.",
			InputSchema: schemaObject(map[string]any{
				"channel":                map[string]any{"type": "string", "enum": []string{"email", "sms", "whatsapp"}},
				"from":                   map[string]any{"type": "string"},
				"to":                     map[string]any{},
				"body":                   map[string]any{"type": "string"},
				"subject":                map[string]any{"type": "string"},
				"body_html":              map[string]any{"type": "string"},
				"reply_to":               map[string]any{"type": "string"},
				"cc":                     map[string]any{},
				"bcc":                    map[string]any{},
				"headers":                map[string]any{"type": "object"},
				"attachment_storage_ids": map[string]any{"type": "array"},
				"media_url":              map[string]any{"type": "string"},
				"content_sid":            map[string]any{"type": "string"},
				"content_variables":      map[string]any{"type": "string"},
				"template_id":            map[string]any{"type": "integer"},
				"vars":                   map[string]any{"type": "object"},
				"idempotency_key":        map[string]any{"type": "string"},
			}, []string{"channel", "from", "to"}),
			Handler: a.toolSendMessage,
		},
		{
			Name:        "send_message_template",
			Description: "Render a saved template and send. Args: template_id, channel, from, to, vars?, idempotency_key?.",
			InputSchema: schemaObject(map[string]any{
				"template_id":            map[string]any{"type": "integer"},
				"channel":                map[string]any{"type": "string", "enum": []string{"email", "sms", "whatsapp"}},
				"from":                   map[string]any{"type": "string"},
				"to":                     map[string]any{},
				"vars":                   map[string]any{"type": "object"},
				"attachment_storage_ids": map[string]any{"type": "array"},
				"idempotency_key":        map[string]any{"type": "string"},
			}, []string{"template_id", "channel", "from", "to"}),
			Handler: a.toolSendMessageTemplate,
		},
		{
			Name:        "message_get",
			Description: "Fetch one message by id. Returns {message, events}.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolMessageGet,
		},
		{
			Name:        "message_list",
			Description: "List messages. Filters: direction? (in|out), channel?, status?, since? (RFC3339), address? (URI), limit? (default 50, max 200).",
			InputSchema: schemaObject(map[string]any{
				"direction": map[string]any{"type": "string"},
				"channel":   map[string]any{"type": "string"},
				"status":    map[string]any{"type": "string"},
				"since":     map[string]any{"type": "string"},
				"address":   map[string]any{"type": "string"},
				"limit":     map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolMessageList,
		},
		{
			Name:        "inbound_redispatch",
			Description: "Re-attempt routing for an inbound message that previously failed or had no_match. Args: id.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolInboundRedispatch,
		},
		{
			Name: "inbound_route_set",
			Description: "Bind a recipient pattern (per channel) to a target app+route. Idempotent on (channel, pattern, target_app, target_route). " +
				"Args: channel (default email), pattern (e.g. 'support+*@acme.com'), target_app, target_route, priority?.",
			InputSchema: schemaObject(map[string]any{
				"channel":      map[string]any{"type": "string"},
				"pattern":      map[string]any{"type": "string"},
				"target_app":   map[string]any{"type": "string"},
				"target_route": map[string]any{"type": "string"},
				"priority":     map[string]any{"type": "integer"},
			}, []string{"pattern", "target_app", "target_route"}),
			Handler: a.toolInboundRouteSet,
		},
		{
			Name:        "inbound_route_list",
			Description: "List configured inbound routes.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Handler:     a.toolInboundRouteList,
		},
		{
			Name:        "inbound_route_delete",
			Description: "Remove an inbound route. Args: id.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolInboundRouteDelete,
		},
		{
			Name: "template_create",
			Description: "Create a template. Args: name, channel? (default 'email'), subject?, body_text?, body_html?, vars_schema?. " +
				"Body fields use {{var}} placeholders.",
			InputSchema: schemaObject(map[string]any{
				"name":        map[string]any{"type": "string"},
				"channel":     map[string]any{"type": "string"},
				"subject":     map[string]any{"type": "string"},
				"body_text":   map[string]any{"type": "string"},
				"body_html":   map[string]any{"type": "string"},
				"vars_schema": map[string]any{"type": "object"},
			}, []string{"name"}),
			Handler: a.toolTemplateCreate,
		},
		{
			Name:        "template_update",
			Description: "Update a template (partial). Args: id, name?, subject?, body_text?, body_html?, vars_schema?.",
			InputSchema: schemaObject(map[string]any{
				"id":          map[string]any{"type": "integer"},
				"name":        map[string]any{"type": "string"},
				"subject":     map[string]any{"type": "string"},
				"body_text":   map[string]any{"type": "string"},
				"body_html":   map[string]any{"type": "string"},
				"vars_schema": map[string]any{"type": "object"},
			}, []string{"id"}),
			Handler: a.toolTemplateUpdate,
		},
		{
			Name:        "template_get",
			Description: "Fetch a template by id.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolTemplateGet,
		},
		{
			Name:        "template_list",
			Description: "List templates. Args: channel?, limit?.",
			InputSchema: schemaObject(map[string]any{
				"channel": map[string]any{"type": "string"},
				"limit":   map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolTemplateList,
		},
		{
			Name:        "template_delete",
			Description: "Delete a template by id (soft delete).",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolTemplateDelete,
		},
		// templates_sync_provider + templates_refresh_status are NOT
		// MCP tools — agents should never trigger a Twilio list call;
		// sync is operator-driven (panel button) or automatic (TTL on
		// template_list). The handlers live as plain methods and are
		// reachable only via the internal HTTP routes
		// /templates/sync + /templates/<id>/refresh-status.
		{
			Name:        "suppression_list",
			Description: "List suppressed addresses. Args: channel?, limit?.",
			InputSchema: schemaObject(map[string]any{
				"channel": map[string]any{"type": "string"},
				"limit":   map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolSuppressionList,
		},
		{
			Name:        "suppression_add",
			Description: "Manually suppress an address. Args: address, channel? (auto-detected from address shape if omitted), reason.",
			InputSchema: schemaObject(map[string]any{
				"address": map[string]any{"type": "string"},
				"channel": map[string]any{"type": "string"},
				"reason":  map[string]any{"type": "string"},
			}, []string{"address", "reason"}),
			Handler: a.toolSuppressionAdd,
		},
		{
			Name:        "suppression_remove",
			Description: "Remove an address from suppression. Args: address, channel? (auto-detected if omitted).",
			InputSchema: schemaObject(map[string]any{
				"address": map[string]any{"type": "string"},
				"channel": map[string]any{"type": "string"},
			}, []string{"address"}),
			Handler: a.toolSuppressionRemove,
		},
		{
			Name:        "suppression_check",
			Description: "Cheap single-row suppression lookup. Returns {suppressed (bool), reason, source, channel, address (canonical), suppressed_at}. Args: address, channel? (auto-detected if omitted).",
			InputSchema: schemaObject(map[string]any{
				"address": map[string]any{"type": "string"},
				"channel": map[string]any{"type": "string"},
			}, []string{"address"}),
			Handler: a.toolSuppressionCheck,
		},
		{
			Name: "senders_list",
			Description: "List sending identities. Returns canonical URI rows (mailto: today; tel: when SMS lands). " +
				"Args: channel? (default 'email'), verified_only? (default false). Returns {senders: [{address, kind, verified, dkim_status?}]}.",
			InputSchema: schemaObject(map[string]any{
				"channel":        map[string]any{"type": "string"},
				"verified_only":  map[string]any{"type": "boolean"},
			}, nil),
			Handler: a.toolSendersList,
		},
		{
			Name: "senders_get",
			Description: "Fetch one sending identity's verification + DKIM state. Args: address (URI or bare email/domain). " +
				"For domains, response includes dkim_tokens — three CNAMEs that must be published in DNS to complete verification.",
			InputSchema: schemaObject(map[string]any{"address": map[string]any{"type": "string"}}, []string{"address"}),
			Handler:     a.toolSendersGet,
		},
		{
			Name:        "senders_delete",
			Description: "Remove a sending identity from the provider. Args: address (URI or bare email/domain). Future sends from this identity will fail.",
			InputSchema: schemaObject(map[string]any{"address": map[string]any{"type": "string"}}, []string{"address"}),
			Handler:     a.toolSendersDelete,
		},
		{
			Name:        "senders_get_quota",
			Description: "Provider-account stats: sandbox flag, 24h send quota, current usage, sending-enabled flag. Drives the sandbox banner.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Handler:     a.toolSendersGetQuota,
		},
		{
			Name: "senders_create",
			Description: "Register a sender end-to-end across email + SMS providers. The address shape picks the path: " +
				"\"foo@x.com\" → SES verify_email; \"x.com\" → SES verify_domain + DKIM/SPF DNS + (auto when aws-s3+aws-sns bound) full inbound bootstrap; \"+15551234567\" → adopt the Twilio phone + (when inbound=auto/true) wire SmsUrl to /webhooks/twilio-inbound. " +
				"Args: address (required), channel? (email|sms|whatsapp; auto-detected if blank), inbound? (auto|true|false; default auto), publish_dns? (default true), spf? (default true), region? (email/SES inbound, default eu-west-1), bucket_name?, topic_name?, rule_set_name?, rule_name?, display_name?, set_default? (bool). " +
				"Idempotent. Writes a row in the local senders table. Returns {address, kind, dkim_tokens?, dns_records?, inbound:{bootstrapped, …}, steps[]}.",
			InputSchema: schemaObject(map[string]any{
				"address":       map[string]any{"type": "string"},
				"channel":       map[string]any{"type": "string"},
				"inbound":       map[string]any{"type": "string"},
				"publish_dns":   map[string]any{"type": "boolean"},
				"spf":           map[string]any{"type": "boolean"},
				"region":        map[string]any{"type": "string"},
				"bucket_name":   map[string]any{"type": "string"},
				"topic_name":    map[string]any{"type": "string"},
				"rule_set_name": map[string]any{"type": "string"},
				"rule_name":     map[string]any{"type": "string"},
				"display_name":  map[string]any{"type": "string"},
				"set_default":   map[string]any{"type": "boolean"},
			}, []string{"address"}),
			Handler: a.toolSendersCreate,
		},
		{
			Name:        "senders_refresh",
			Description: "Refresh verification/DKIM and sending-enabled status for senders already tracked locally, by re-listing identities at each bound provider. Does NOT import unknown upstream identities — use senders_create to add a sender. Soft-deletes local rows whose address no longer exists upstream. Idempotent. Returns {refreshed, count}.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Handler:     a.toolSendersRefresh,
		},
		{
			Name:        "senders_set_default",
			Description: "Flip the per-(project, channel) default sender. send_message uses the default when 'from' is omitted. Args: address, channel? (auto-detected if blank). At most one default per (project, channel) enforced at SQL level.",
			InputSchema: schemaObject(map[string]any{
				"address": map[string]any{"type": "string"},
				"channel": map[string]any{"type": "string"},
			}, []string{"address"}),
			Handler: a.toolSendersSetDefault,
		},
	}
}

func main() { sdk.Run(&App{}) }

// ─── Project resolution ────────────────────────────────────────────

func resolveProjectFromArgs(args map[string]any) (string, error) {
	if env := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); env != "" {
		return env, nil
	}
	if v, ok := args["_project_id"].(string); ok && v != "" {
		return v, nil
	}
	return "", errors.New("project_id missing — pass _project_id when scope=global")
}

func resolveProjectFromRequest(r *http.Request) (string, error) {
	if env := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); env != "" {
		return env, nil
	}
	if v := r.URL.Query().Get("project_id"); v != "" {
		return v, nil
	}
	return "", errors.New("project_id required in query string when install scope=global")
}

// ─── Address normalisation ────────────────────────────────────────
//
// v0.3 takes channel as an explicit send_message argument and stores
// plain addresses — no URI scheme prefixes anywhere in the data path
// or on the wire. validation is per-channel:
//
//   email           — lowercased local-part-and-domain, must look
//                     like an email (one '@', dot in domain)
//   sms / whatsapp  — E.164 phone number (^\+[1-9]\d{6,14}$)
//
// Twilio's "whatsapp:+1..." prefix is added internally just before
// the wire call (sendViaTwilio); callers never see or pass it.
//
// For tolerance, we accept a leading mailto:/tel:/whatsapp: on the
// way in and strip it — old data and habit-typing both keep working
// — but the canonical stored form is always plain.

const (
	channelEmail    = "email"
	channelSMS      = "sms"
	channelWhatsApp = "whatsapp"
)

// validChannel reports whether c is a known channel name.
func validChannel(c string) bool {
	switch c {
	case channelEmail, channelSMS, channelWhatsApp:
		return true
	}
	return false
}

// stripScheme removes a leading scheme prefix if present. Used
// defensively on inputs and on rows migrated from v0.2 (the 002
// migration normally strips them, but a partial run shouldn't break
// reads).
func stripScheme(s string) string {
	for _, p := range []string{"mailto:", "tel:", "whatsapp:"} {
		if l := len(p); len(s) >= l && strings.EqualFold(s[:l], p) {
			return s[l:]
		}
	}
	return s
}

// normaliseAddress validates and canonicalises a single address for
// the given channel. Returns the plain-form address ready to store.
func normaliseAddress(channel, raw string) (string, error) {
	raw = strings.TrimSpace(stripScheme(strings.TrimSpace(raw)))
	if raw == "" {
		return "", errors.New("empty address")
	}
	switch channel {
	case channelEmail:
		addr := strings.ToLower(raw)
		if !looksLikeEmail(addr) {
			return "", fmt.Errorf("invalid email %q", raw)
		}
		return addr, nil
	case channelSMS, channelWhatsApp:
		if !looksLikeE164(raw) {
			return "", fmt.Errorf("invalid phone number %q (expected E.164, e.g. +15551234567)", raw)
		}
		return raw, nil
	}
	return "", fmt.Errorf("unsupported channel %q", channel)
}

// normaliseAddressList accepts a string or []any/[]string and
// returns a deduped, validated list for the given channel.
func normaliseAddressList(channel string, v any) ([]string, error) {
	out := []string{}
	add := func(s string) error {
		if s == "" {
			return nil
		}
		a, err := normaliseAddress(channel, s)
		if err != nil {
			return err
		}
		for _, e := range out {
			if e == a {
				return nil
			}
		}
		out = append(out, a)
		return nil
	}
	switch x := v.(type) {
	case nil:
		return out, nil
	case string:
		if err := add(x); err != nil {
			return nil, err
		}
	case []any:
		for _, it := range x {
			s, _ := it.(string)
			if err := add(s); err != nil {
				return nil, err
			}
		}
	case []string:
		for _, s := range x {
			if err := add(s); err != nil {
				return nil, err
			}
		}
	default:
		return nil, fmt.Errorf("expected string or string[], got %T", v)
	}
	return out, nil
}

func looksLikeEmail(s string) bool {
	at := strings.IndexByte(s, '@')
	if at <= 0 || at == len(s)-1 {
		return false
	}
	if strings.IndexByte(s[at+1:], '.') < 0 {
		return false
	}
	if strings.ContainsAny(s, " \t\r\n") {
		return false
	}
	return true
}

// looksLikeE164: '+' then 7..15 digits, leading digit 1-9.
// Twilio is stricter about the upstream API but this is good enough
// to reject obvious typos before paying for a request.
func looksLikeE164(s string) bool {
	if len(s) < 8 || len(s) > 16 || s[0] != '+' {
		return false
	}
	if s[1] < '1' || s[1] > '9' {
		return false
	}
	for i := 2; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// extractSubaddress returns the "+tag" portion of an email's local
// part if present, else the empty string. e.g.
// "support+T-1234@acme.com" → "T-1234". Email-only.
func extractSubaddress(addr string) string {
	addr = stripScheme(addr)
	at := strings.IndexByte(addr, '@')
	if at < 0 {
		return ""
	}
	local := addr[:at]
	plus := strings.IndexByte(local, '+')
	if plus < 0 {
		return ""
	}
	return local[plus+1:]
}

// ─── Domain types ──────────────────────────────────────────────────

type Message struct {
	ID                   int64           `json:"id"`
	ProjectID            string          `json:"project_id,omitempty"`
	Channel              string          `json:"channel"`
	Direction            string          `json:"direction"`
	From                 string          `json:"from"`
	To                   []string        `json:"to"`
	CC                   []string        `json:"cc"`
	BCC                  []string        `json:"bcc"`
	Subject              string          `json:"subject,omitempty"`
	BodyText             string          `json:"body_text,omitempty"`
	BodyHTML             string          `json:"body_html,omitempty"`
	Headers              json.RawMessage `json:"headers"`
	AttachmentStorageIDs []int64         `json:"attachment_storage_ids"`
	MessageIDHeader      string          `json:"message_id_header,omitempty"`
	InReplyTo            string          `json:"in_reply_to,omitempty"`
	References           []string        `json:"references"`
	Status               string          `json:"status"`
	StatusReason         string          `json:"status_reason,omitempty"`
	ProviderMessageID    string          `json:"provider_message_id,omitempty"`
	IdempotencyKey       string          `json:"idempotency_key,omitempty"`
	RouteTargetApp       string          `json:"route_target_app,omitempty"`
	RouteTargetRoute     string          `json:"route_target_route,omitempty"`
	RouteStatus          string          `json:"route_status,omitempty"`
	RouteError           string          `json:"route_error,omitempty"`
	RouteAttempts        int             `json:"route_attempts,omitempty"`
	MatchedRecipient     string          `json:"matched_recipient,omitempty"`
	MatchedPattern       string          `json:"matched_pattern,omitempty"`
	ToSubaddress         string          `json:"to_subaddress,omitempty"`
	TemplateID           int64           `json:"template_id,omitempty"`
	// v0.5: verdicts (SES) and S3-mode raw .eml location.
	Verdicts             json.RawMessage `json:"verdicts,omitempty"`
	S3Key                string          `json:"s3_key,omitempty"`
	CreatedAt            string          `json:"created_at,omitempty"`
	SentAt               string          `json:"sent_at,omitempty"`
	ReceivedAt           string          `json:"received_at,omitempty"`
	LastEventAt          string          `json:"last_event_at,omitempty"`
}

type Template struct {
	ID                 int64           `json:"id"`
	ProjectID          string          `json:"project_id,omitempty"`
	Channel            string          `json:"channel"`
	Name               string          `json:"name"`
	Subject            string          `json:"subject,omitempty"`
	BodyText           string          `json:"body_text,omitempty"`
	BodyHTML           string          `json:"body_html,omitempty"`
	VarsSchema         json.RawMessage `json:"vars_schema"`
	// Provider-mirrored fields (v0.4). NULL/empty for local templates.
	ProviderTemplateID string `json:"provider_template_id,omitempty"` // Twilio ContentSid
	ProviderStatus     string `json:"provider_status,omitempty"`      // approved | pending | rejected | deleted
	VarStyle           string `json:"var_style,omitempty"`            // named (default) | numbered (Twilio)
	LastSyncedAt       string `json:"last_synced_at,omitempty"`
	CreatedAt          string `json:"created_at,omitempty"`
	UpdatedAt          string `json:"updated_at,omitempty"`
}

type InboundRoute struct {
	ID          int64  `json:"id"`
	ProjectID   string `json:"project_id,omitempty"`
	Channel     string `json:"channel"`
	Pattern     string `json:"pattern"`
	TargetApp   string `json:"target_app"`
	TargetRoute string `json:"target_route"`
	Priority    int    `json:"priority"`
	CreatedAt   string `json:"created_at,omitempty"`
}

type Suppression struct {
	ProjectID string `json:"project_id,omitempty"`
	Channel   string `json:"channel"`
	Address   string `json:"address"`
	Reason    string `json:"reason"`
	Source    string `json:"source"`
	FirstSeen string `json:"first_seen,omitempty"`
	LastSeen  string `json:"last_seen,omitempty"`
}

type DeliveryEvent struct {
	ID         int64           `json:"id"`
	MessageID  int64           `json:"message_id"`
	Kind       string          `json:"kind"`
	Recipient  string          `json:"recipient,omitempty"`
	Reason     string          `json:"reason,omitempty"`
	Raw        json.RawMessage `json:"raw"`
	OccurredAt string          `json:"occurred_at,omitempty"`
}

// ─── send_message ──────────────────────────────────────────────────

func (a *App) toolSendMessage(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}

	// Idempotency: short-circuit if we've seen the key before for this project.
	if idem := strArg(args, "idempotency_key"); idem != "" {
		if existing, err := dbFindByIdempotencyKey(ctx.AppDB(), pid, idem); err == nil && existing != nil {
			return sendResponse(existing), nil
		}
	}

	channel := strings.ToLower(strings.TrimSpace(strArg(args, "channel")))
	if channel == "" {
		return nil, errors.New("channel: required (one of email, sms, whatsapp)")
	}
	if !validChannel(channel) {
		return nil, fmt.Errorf("channel: unsupported value %q (one of email, sms, whatsapp)", channel)
	}

	to, err := normaliseAddressList(channel, args["to"])
	if err != nil {
		return nil, fmt.Errorf("to: %w", err)
	}
	if len(to) == 0 {
		return nil, errors.New("to: at least one recipient required")
	}
	cc, err := normaliseAddressList(channel, args["cc"])
	if err != nil {
		return nil, fmt.Errorf("cc: %w", err)
	}
	bcc, err := normaliseAddressList(channel, args["bcc"])
	if err != nil {
		return nil, fmt.Errorf("bcc: %w", err)
	}
	// cc/bcc on phone channels make no sense — warn and discard rather
	// than reject, so a generic compose form doesn't have to know.
	if (channel == channelSMS || channel == channelWhatsApp) && (len(cc) > 0 || len(bcc) > 0) {
		ctx.Logger().Warn("messaging: cc/bcc ignored on phone channels", "channel", channel)
		cc = nil
		bcc = nil
	}

	// Optional template render. Two paths:
	//   - local (no provider_template_id): {{var}} substitution into
	//     subject/body_text/body_html, sent inline.
	//   - provider-mirrored (Twilio Content with a ContentSid): we
	//     pass ContentSid + ContentVariables (JSON-stringified vars)
	//     through to Twilio, which renders server-side using the
	//     Meta-approved template.
	body := strArg(args, "body")
	subject := strArg(args, "subject")
	bodyHTML := strArg(args, "body_html")
	mediaURL := strArg(args, "media_url")
	contentSid := strArg(args, "content_sid")
	contentVars := strArg(args, "content_variables")
	templateID := int64Arg(args, "template_id")
	if templateID > 0 {
		tpl, err := dbTemplateGet(ctx.AppDB(), pid, templateID)
		if err != nil {
			return nil, err
		}
		if tpl == nil {
			return nil, fmt.Errorf("template_id %d not found", templateID)
		}
		// Channel must match the template's channel (per-channel
		// templates are the v0.3 contract). Fail-fast rather than
		// silently picking the wrong template.
		if tpl.Channel != channel {
			return nil, fmt.Errorf("template_id %d is for channel=%q, send_message channel=%q",
				templateID, tpl.Channel, channel)
		}
		if tpl.ProviderTemplateID != "" {
			// Provider-mirrored route. Refuse to send through a
			// non-approved template — that's a hard Meta error and
			// surfacing it pre-flight is far clearer.
			if tpl.ProviderStatus != "" && tpl.ProviderStatus != "approved" {
				return nil, fmt.Errorf("template_id %d has provider_status=%q (need 'approved'); call templates_refresh_status to refresh",
					templateID, tpl.ProviderStatus)
			}
			contentSid = tpl.ProviderTemplateID
			vars := mapArg(args, "vars")
			if len(vars) > 0 {
				if cv, err := json.Marshal(vars); err == nil {
					contentVars = string(cv)
				}
			}
			// Provider templates render server-side; the local body
			// stays empty so we don't fail the body-required check.
			body = "(provider template " + tpl.ProviderTemplateID + ")"
		} else {
			// Local template — {{var}} substitution as before.
			vars := mapArg(args, "vars")
			if subject == "" {
				subject = renderTemplate(tpl.Subject, vars)
			}
			if body == "" {
				body = renderTemplate(tpl.BodyText, vars)
			}
			if bodyHTML == "" {
				bodyHTML = renderTemplate(tpl.BodyHTML, vars)
			}
		}
	}

	if body == "" && bodyHTML == "" && contentSid == "" {
		return nil, errors.New("body, body_html, or content_sid required (directly or via template)")
	}

	from := strArg(args, "from")
	if from == "" {
		return nil, errors.New("from: required (pick a verified sender via senders_list)")
	}
	from, err = normaliseAddress(channel, from)
	if err != nil {
		return nil, fmt.Errorf("from: %w", err)
	}
	replyTo := strArg(args, "reply_to")
	if replyTo != "" {
		replyTo, err = normaliseAddress(channel, replyTo)
		if err != nil {
			return nil, fmt.Errorf("reply_to: %w", err)
		}
	}

	headers, _ := args["headers"].(map[string]any)
	headersJSON, _ := json.Marshal(headers)
	if len(headersJSON) == 0 {
		headersJSON = []byte("{}")
	}

	attachIDs := int64ArrayArg(args, "attachment_storage_ids")
	attachJSON, _ := json.Marshal(attachIDs)

	// Suppression check — drop any recipient that's on the list.
	allowedTo, suppressedTo := filterSuppressed(ctx.AppDB(), pid, channel, to)
	allowedCC, _ := filterSuppressed(ctx.AppDB(), pid, channel, cc)
	allowedBCC, _ := filterSuppressed(ctx.AppDB(), pid, channel, bcc)
	if len(allowedTo) == 0 {
		return nil, fmt.Errorf("all 'to' recipients are suppressed: %v", suppressedTo)
	}

	// Persist as pending first so a provider error still leaves a row.
	toJSON, _ := json.Marshal(allowedTo)
	ccJSON, _ := json.Marshal(allowedCC)
	bccJSON, _ := json.Marshal(allowedBCC)
	idem := strArg(args, "idempotency_key")
	var idemNullable any
	if idem != "" {
		idemNullable = idem
	}
	res, err := ctx.AppDB().Exec(
		`INSERT INTO messages
			(project_id, channel, direction, from_addr, to_addrs, cc_addrs, bcc_addrs,
			 subject, body_text, body_html, headers, attachment_storage_ids,
			 message_id_header, in_reply_to, references_json,
			 status, idempotency_key, template_id)
		 VALUES (?, ?, 'out', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'pending', ?, ?)`,
		pid, channel, from, string(toJSON), string(ccJSON), string(bccJSON),
		subject, body, bodyHTML, string(headersJSON), string(attachJSON),
		strArg(args, "message_id_header"),
		strArg(args, "in_reply_to"),
		`[]`,
		idemNullable, nullableInt64(templateID),
	)
	if err != nil {
		return nil, fmt.Errorf("insert message: %w", err)
	}
	id, _ := res.LastInsertId()

	// Provider call — dispatch by channel. Body / contentSid / etc.
	// were resolved up in the template-render block above (raw args
	// or template-derived).
	in := providerSendInput{
		Channel: channel,
		From:    from, To: allowedTo, CC: allowedCC, BCC: allowedBCC,
		Subject: subject, BodyText: body, BodyHTML: bodyHTML,
		ReplyTo: replyTo, Headers: headers,
		MediaURL:         mediaURL,
		ContentSid:       contentSid,
		ContentVariables: contentVars,
	}
	var providerMessageID string
	var providerErr error
	switch channel {
	case channelEmail:
		providerMessageID, providerErr = sendViaSES(ctx, in)
	case channelSMS, channelWhatsApp:
		providerMessageID, providerErr = sendViaTwilio(ctx, in)
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if providerErr != nil {
		_, _ = ctx.AppDB().Exec(
			`UPDATE messages SET status='failed', status_reason=?, last_event_at=? WHERE id=?`,
			truncate(providerErr.Error(), 500), now, id,
		)
		ctx.Logger().Warn("send_message: provider failed", "id", id, "err", providerErr)
		m, _ := dbMessageGet(ctx.AppDB(), pid, id)
		return sendResponse(m), nil
	}

	_, _ = ctx.AppDB().Exec(
		`UPDATE messages SET status='sent', provider_message_id=?, sent_at=?, last_event_at=? WHERE id=?`,
		providerMessageID, now, now, id,
	)
	ctx.Emit("message.sent", map[string]any{
		"id":      id,
		"channel": channel,
		"to":      allowedTo,
	})
	m, _ := dbMessageGet(ctx.AppDB(), pid, id)
	return sendResponse(m), nil
}

func sendResponse(m *Message) map[string]any {
	if m == nil {
		return map[string]any{"id": 0, "status": "failed"}
	}
	recips := make([]map[string]any, 0, len(m.To))
	for _, r := range m.To {
		recips = append(recips, map[string]any{"address": r, "status": m.Status})
	}
	return map[string]any{
		"id":                  m.ID,
		"channel":             m.Channel,
		"status":              m.Status,
		"recipients":          recips,
		"provider_message_id": m.ProviderMessageID,
		"status_reason":       m.StatusReason,
	}
}

// ─── send_message_template ─────────────────────────────────────────

func (a *App) toolSendMessageTemplate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	tplID := int64Arg(args, "template_id")
	if tplID == 0 {
		return nil, errors.New("template_id required")
	}
	// Forward to send_message — the template lookup happens there.
	args["template_id"] = tplID
	return a.toolSendMessage(ctx, args)
}

// ─── Provider invocation ───────────────────────────────────────────

type providerSendInput struct {
	Channel       string
	From, ReplyTo string
	To, CC, BCC   []string
	Subject       string
	BodyText      string
	BodyHTML      string
	Headers       map[string]any
	// SMS / WhatsApp only:
	MediaURL         string
	ContentSid       string
	ContentVariables string
}

// sendViaSES maps our flat input to AWS SES v2's nested SendEmail
// payload (FromEmailAddress / Destination / Content.Simple). All
// addresses are already plain (no scheme prefix) — that's the v0.3
// contract — so we hand them straight to SES.
//
// Custom headers are silently dropped: SES Simple content doesn't
// accept arbitrary headers. Setting them requires send_raw_email
// with a fully-built MIME blob (v0.4+).
func sendViaSES(ctx *sdk.AppCtx, in providerSendInput) (string, error) {
	bound := ctx.IntegrationFor("email_provider")
	if bound == nil {
		return "", errors.New("no email_provider bound — install/select an aws-ses connection")
	}
	tool := bound.ToolFor("email.send")
	if tool == "" {
		tool = "send_email"
	}

	dest := map[string]any{}
	if len(in.To) > 0 {
		dest["ToAddresses"] = in.To
	}
	if len(in.CC) > 0 {
		dest["CcAddresses"] = in.CC
	}
	if len(in.BCC) > 0 {
		dest["BccAddresses"] = in.BCC
	}

	body := map[string]any{}
	if in.BodyText != "" {
		body["Text"] = map[string]any{"Data": in.BodyText, "Charset": "UTF-8"}
	}
	if in.BodyHTML != "" {
		body["Html"] = map[string]any{"Data": in.BodyHTML, "Charset": "UTF-8"}
	}
	subj := in.Subject
	if subj == "" {
		subj = "(no subject)"
	}
	payload := map[string]any{
		"FromEmailAddress": in.From,
		"Destination":      dest,
		"Content": map[string]any{
			"Simple": map[string]any{
				"Subject": map[string]any{"Data": subj, "Charset": "UTF-8"},
				"Body":    body,
			},
		},
	}
	if in.ReplyTo != "" {
		payload["ReplyToAddresses"] = []string{in.ReplyTo}
	}
	if len(in.Headers) > 0 {
		ctx.Logger().Warn("messaging: custom headers ignored — SES v2 Simple content doesn't carry them. Use raw mode in v0.4.")
	}

	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, tool, payload)
	if err != nil {
		return "", err
	}
	if res == nil || !res.Success {
		body := ""
		if res != nil {
			body = string(res.Data)
		}
		return "", fmt.Errorf("provider non-2xx: %s", truncate(body, 400))
	}
	var probe map[string]any
	_ = json.Unmarshal(res.Data, &probe)
	for _, key := range []string{"MessageId", "message_id", "messageId", "id"} {
		if v, ok := probe[key].(string); ok && v != "" {
			return v, nil
		}
	}
	return "", nil
}

// sendViaTwilio invokes the bound phone_provider for SMS or WhatsApp.
// One Twilio request per recipient (the API takes one To at a time).
// Returns the SID of the first successful send; if all fail, returns
// the first error.
//
// WhatsApp wire-form prefix: Twilio's API expects "whatsapp:+1..." on
// both From and To. We add that prefix here so messaging callers and
// stored rows stay scheme-free.
func sendViaTwilio(ctx *sdk.AppCtx, in providerSendInput) (string, error) {
	bound := ctx.IntegrationFor("phone_provider")
	if bound == nil {
		return "", errors.New("no phone_provider bound — install/select a Twilio connection for SMS/WhatsApp")
	}
	capability := "sms.send"
	if in.Channel == channelWhatsApp {
		capability = "whatsapp.send"
	}
	tool := bound.ToolFor(capability)
	if tool == "" {
		if in.Channel == channelWhatsApp {
			tool = "send_whatsapp"
		} else {
			tool = "send_sms"
		}
	}

	prefix := ""
	if in.Channel == channelWhatsApp {
		prefix = "whatsapp:"
	}
	// Twilio accepts EITHER a free-form Body OR a ContentSid (Meta-
	// approved template). At least one must be present.
	if in.BodyText == "" && in.ContentSid == "" {
		return "", errors.New("body or content_sid required for sms/whatsapp")
	}

	var firstSID string
	var firstErr error
	for _, to := range in.To {
		payload := map[string]any{
			"From": prefix + in.From,
			"To":   prefix + to,
		}
		if in.ContentSid != "" {
			// ContentSid path: server-side rendering against approved
			// template. Body is omitted; ContentVariables is a JSON
			// string of the slot values.
			payload["ContentSid"] = in.ContentSid
			if in.ContentVariables != "" {
				payload["ContentVariables"] = in.ContentVariables
			}
		} else {
			payload["Body"] = in.BodyText
		}
		if in.MediaURL != "" {
			payload["MediaUrl"] = in.MediaURL
		}
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, tool, payload)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if res == nil || !res.Success {
			body := ""
			if res != nil {
				body = string(res.Data)
			}
			if firstErr == nil {
				firstErr = fmt.Errorf("twilio non-2xx: %s", truncate(body, 400))
			}
			continue
		}
		// Twilio Messages.create returns { sid: "SMxxx", ... }.
		var probe map[string]any
		_ = json.Unmarshal(res.Data, &probe)
		if firstSID == "" {
			for _, key := range []string{"sid", "Sid", "SID"} {
				if v, ok := probe[key].(string); ok && v != "" {
					firstSID = v
					break
				}
			}
		}
	}
	if firstSID == "" && firstErr != nil {
		return "", firstErr
	}
	return firstSID, nil
}

// ─── message_get / message_list ────────────────────────────────────

func (a *App) toolMessageGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	m, err := dbMessageGet(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if m == nil {
		return map[string]any{"message": nil, "found": false}, nil
	}
	events, _ := dbDeliveryEvents(ctx.AppDB(), id)
	return map[string]any{"message": m, "events": events, "found": true}, nil
}

func (a *App) toolMessageList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	opts := messageListOpts{
		Direction: strArg(args, "direction"),
		Channel:   strArg(args, "channel"),
		Status:    strArg(args, "status"),
		Since:     strArg(args, "since"),
		Address:   strArg(args, "address"),
		Limit:     intArg(args, "limit", 50),
	}
	if opts.Address != "" {
		// Best-effort normalise; callers may pass mailto:foo@bar.com
		// or +1555... — strip the prefix and lowercase emails so the
		// LIKE search works whether the row is plain or legacy URI.
		opts.Address = strings.TrimSpace(stripScheme(opts.Address))
		if strings.Contains(opts.Address, "@") {
			opts.Address = strings.ToLower(opts.Address)
		}
	}
	if opts.Limit <= 0 || opts.Limit > 200 {
		opts.Limit = 50
	}
	out, err := dbMessageList(ctx.AppDB(), pid, opts)
	if err != nil {
		return nil, err
	}
	return map[string]any{"messages": out, "count": len(out)}, nil
}

// ─── inbound_redispatch ────────────────────────────────────────────

func (a *App) toolInboundRedispatch(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	m, err := dbMessageGet(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if m == nil || m.Direction != "in" {
		return nil, errors.New("inbound message not found")
	}
	if err := dispatchInbound(ctx, pid, m); err != nil {
		return nil, err
	}
	updated, _ := dbMessageGet(ctx.AppDB(), pid, id)
	return map[string]any{"message": updated}, nil
}

// ─── inbound_route_* tools ─────────────────────────────────────────

func (a *App) toolInboundRouteSet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	pattern := strArg(args, "pattern")
	if pattern == "" {
		return nil, errors.New("pattern required")
	}
	// Patterns are now plain addresses with optional '*' wildcards
	// in the local part (email) or matching the whole address. We do
	// a light syntax check by replacing '*' with 'x' and validating
	// against the supplied channel.
	channel := strArg(args, "channel")
	if channel == "" {
		channel = channelEmail
	}
	if !validChannel(channel) {
		return nil, fmt.Errorf("channel: unsupported value %q", channel)
	}
	probe := strings.ReplaceAll(pattern, "*", "x")
	if _, err := normaliseAddress(channel, probe); err != nil {
		return nil, fmt.Errorf("pattern: %w", err)
	}
	pattern = strings.ToLower(strings.TrimSpace(stripScheme(pattern)))
	targetApp := strArg(args, "target_app")
	targetRoute := strArg(args, "target_route")
	if targetApp == "" || targetRoute == "" {
		return nil, errors.New("target_app and target_route required")
	}
	priority := intArg(args, "priority", 100)
	id, err := dbInboundRouteUpsert(ctx.AppDB(), pid, channel, pattern, targetApp, targetRoute, priority)
	if err != nil {
		return nil, err
	}
	r, _ := dbInboundRouteGet(ctx.AppDB(), pid, id)
	return map[string]any{"route": r}, nil
}

func (a *App) toolInboundRouteList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	out, err := dbInboundRouteList(ctx.AppDB(), pid)
	if err != nil {
		return nil, err
	}
	return map[string]any{"routes": out, "count": len(out)}, nil
}

func (a *App) toolInboundRouteDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	if err := dbInboundRouteDelete(ctx.AppDB(), pid, id); err != nil {
		return nil, err
	}
	return map[string]any{"deleted": true}, nil
}

// ─── template_* tools ──────────────────────────────────────────────

func (a *App) toolTemplateCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	name := strArg(args, "name")
	if name == "" {
		return nil, errors.New("name required")
	}
	channel := strArg(args, "channel")
	if channel == "" {
		channel = channelEmail
	}
	varsRaw, _ := json.Marshal(mapArg(args, "vars_schema"))
	res, err := ctx.AppDB().Exec(
		`INSERT INTO templates (project_id, channel, name, subject, body_text, body_html, vars_schema)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		pid, channel, name, strArg(args, "subject"),
		strArg(args, "body_text"), strArg(args, "body_html"),
		string(varsRaw),
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	t, _ := dbTemplateGet(ctx.AppDB(), pid, id)
	return map[string]any{"template": t}, nil
}

func (a *App) toolTemplateUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	updates := map[string]any{}
	for _, k := range []string{"name", "subject", "body_text", "body_html"} {
		if v, ok := args[k]; ok {
			if s, ok := v.(string); ok {
				updates[k] = s
			}
		}
	}
	if v, ok := args["vars_schema"]; ok {
		raw, _ := json.Marshal(v)
		updates["vars_schema"] = string(raw)
	}
	if len(updates) == 0 {
		t, _ := dbTemplateGet(ctx.AppDB(), pid, id)
		return map[string]any{"template": t}, nil
	}
	sets := []string{}
	vals := []any{}
	for k, v := range updates {
		sets = append(sets, k+" = ?")
		vals = append(vals, v)
	}
	sets = append(sets, "updated_at = CURRENT_TIMESTAMP")
	vals = append(vals, id, pid)
	_, err = ctx.AppDB().Exec(
		`UPDATE templates SET `+strings.Join(sets, ", ")+
			` WHERE id = ? AND project_id = ? AND deleted_at IS NULL`, vals...,
	)
	if err != nil {
		return nil, err
	}
	t, _ := dbTemplateGet(ctx.AppDB(), pid, id)
	return map[string]any{"template": t}, nil
}

func (a *App) toolTemplateGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	t, err := dbTemplateGet(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return map[string]any{"template": nil, "found": false}, nil
	}
	return map[string]any{"template": t, "found": true}, nil
}

func (a *App) toolTemplateList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	limit := intArg(args, "limit", 100)
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	channel := strArg(args, "channel")
	out, err := dbTemplateList(ctx.AppDB(), pid, channel, limit)
	if err != nil {
		return nil, err
	}
	// Auto-sync TTL: when the caller asks for a channel that has a
	// provider sync path (whatsapp today), kick off a background
	// refresh if the cache is stale. The current call returns the
	// existing rows immediately; subscribers to "templates.synced"
	// see the fresh data when it lands.
	maybeAutoSync(ctx, pid, channel)
	lastSynced, lastErr, _ := dbSyncStateGet(ctx.AppDB(), pid, channel)
	return map[string]any{
		"templates":      out,
		"count":          len(out),
		"last_synced_at": lastSynced,
		"last_sync_error": lastErr,
	}, nil
}

// autoSyncTTL gates how often template_list-driven background syncs
// fire per (project, channel). 10 minutes is the polish-y default
// from the v0.4 plan: refresh often enough to surface a freshly-
// approved Meta template within an operator's typical review window
// without hammering Twilio on every list call.
const autoSyncTTL = 10 * time.Minute

// maybeAutoSync inspects last_synced_at + the in-memory in-flight
// flag and fires a background sync goroutine when both indicate
// it's time. Best-effort — failures land in template_sync_state's
// last_error column for the panel to surface.
func maybeAutoSync(ctx *sdk.AppCtx, pid, channel string) {
	if pid == "" || !providerSyncableChannel(channel) {
		return
	}
	lastSynced, _, _ := dbSyncStateGet(ctx.AppDB(), pid, channel)
	if lastSynced != "" {
		// Parse the SQLite timestamp and compare to autoSyncTTL.
		// SQLite returns "YYYY-MM-DD HH:MM:SS" by default for CURRENT_TIMESTAMP.
		layouts := []string{time.RFC3339, "2006-01-02 15:04:05"}
		var t time.Time
		var err error
		for _, layout := range layouts {
			t, err = time.Parse(layout, lastSynced)
			if err == nil {
				break
			}
		}
		if err == nil && time.Since(t) < autoSyncTTL {
			return
		}
	}
	if !tryStartSync(pid, channel) {
		return
	}
	go func() {
		defer endSync(pid, channel)
		_, err := syncProviderTemplates(ctx, pid, channel)
		if err != nil {
			ctx.Logger().Warn("auto-sync failed", "channel", channel, "err", err)
		}
	}()
}

// providerSyncableChannel: channels for which we have a provider
// list-templates endpoint. Email is local-only (SES has templates
// but messaging renders {{var}} itself); SMS has no Twilio Content
// equivalent. Today only WhatsApp.
func providerSyncableChannel(channel string) bool {
	return channel == channelWhatsApp
}

// ─── templates_sync_provider + templates_refresh_status ───────────

func (a *App) toolTemplatesSyncProvider(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	channel := strArg(args, "channel")
	if !validChannel(channel) {
		return nil, fmt.Errorf("channel: required (one of email, sms, whatsapp); got %q", channel)
	}
	if !providerSyncableChannel(channel) {
		return map[string]any{
			"synced":   0,
			"skipped":  true,
			"reason":   fmt.Sprintf("no provider sync for channel %q (local templates only)", channel),
		}, nil
	}
	if !tryStartSync(pid, channel) {
		return nil, errors.New("a sync for this channel is already in flight; try again in a moment")
	}
	defer endSync(pid, channel)
	count, err := syncProviderTemplates(ctx, pid, channel)
	if err != nil {
		return nil, err
	}
	return map[string]any{"synced": count, "channel": channel}, nil
}

func (a *App) toolTemplatesRefreshStatus(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	t, err := dbTemplateGet(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if t == nil || t.ProviderTemplateID == "" {
		return nil, errors.New("template not found, or not a provider-mirrored row")
	}
	bound := ctx.IntegrationFor("phone_provider")
	if bound == nil {
		return nil, errors.New("no phone_provider bound — install/select a Twilio connection")
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, "get_content_template", map[string]any{
		"ContentSid": t.ProviderTemplateID,
	})
	if err != nil {
		return nil, fmt.Errorf("get_content_template: %w", err)
	}
	if res == nil || !res.Success {
		body := ""
		if res != nil {
			body = string(res.Data)
		}
		return nil, fmt.Errorf("provider non-2xx: %s", truncate(body, 400))
	}
	// Twilio's Content GET response carries approval_requests differently
	// from the list endpoint; the per-template path returns the row
	// shape with `approval_requests` array. We extract the first entry's
	// status the same way as the sync path.
	var raw struct {
		ApprovalRequests []struct {
			Status string `json:"status"`
		} `json:"approval_requests"`
	}
	_ = json.Unmarshal(res.Data, &raw)
	status := t.ProviderStatus
	if len(raw.ApprovalRequests) > 0 {
		status = strings.ToLower(raw.ApprovalRequests[0].Status)
	}
	_, _ = ctx.AppDB().Exec(
		`UPDATE templates SET provider_status = ?, last_synced_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ?`,
		status, id,
	)
	updated, _ := dbTemplateGet(ctx.AppDB(), pid, id)
	return map[string]any{"template": updated, "status": status}, nil
}

func (a *App) toolTemplateDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	_, err = ctx.AppDB().Exec(
		`UPDATE templates SET deleted_at = CURRENT_TIMESTAMP WHERE id = ? AND project_id = ?`,
		id, pid,
	)
	if err != nil {
		return nil, err
	}
	return map[string]any{"deleted": true}, nil
}

// renderTemplate is a tiny {{var}} substituter — no conditionals, no
// loops. Missing vars are left as-is (`{{name}}`) so the operator
// notices in the rendered output rather than getting silent gaps.
func renderTemplate(s string, vars map[string]any) string {
	if s == "" || len(vars) == 0 {
		return s
	}
	out := s
	for k, v := range vars {
		val := fmt.Sprintf("%v", v)
		out = strings.ReplaceAll(out, "{{"+k+"}}", val)
		out = strings.ReplaceAll(out, "{{ "+k+" }}", val)
	}
	return out
}

// ─── suppression_* tools ───────────────────────────────────────────

func (a *App) toolSuppressionList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	limit := intArg(args, "limit", 200)
	if limit <= 0 || limit > 1000 {
		limit = 200
	}
	out, err := dbSuppressionList(ctx.AppDB(), pid, strArg(args, "channel"), limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{"suppressions": out, "count": len(out)}, nil
}

func (a *App) toolSuppressionAdd(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	addrRaw := strArg(args, "address")
	if addrRaw == "" {
		return nil, errors.New("address required")
	}
	channel := strArg(args, "channel")
	if channel == "" {
		channel = guessChannelFromAddress(addrRaw)
	}
	if !validChannel(channel) {
		return nil, errors.New("channel: required for suppression_add (one of email, sms, whatsapp)")
	}
	addr, err := normaliseAddress(channel, addrRaw)
	if err != nil {
		return nil, err
	}
	reason := strArg(args, "reason")
	if reason == "" {
		reason = "manual"
	}
	if err := dbSuppressionUpsert(ctx.AppDB(), pid, channel, addr, reason, "manual"); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "address": addr, "channel": channel, "reason": reason}, nil
}

// toolSuppressionCheck answers "is this one address suppressed?"
// without paginating the full list. CRM (and any campaign sender)
// uses this on every send; the previous suppression_list call was
// O(N) over all suppressions per check.
func (a *App) toolSuppressionCheck(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	addrRaw := strArg(args, "address")
	if addrRaw == "" {
		return nil, errors.New("address required")
	}
	channel := strArg(args, "channel")
	if channel == "" {
		channel = guessChannelFromAddress(addrRaw)
	}
	if !validChannel(channel) {
		return nil, errors.New("channel: required (one of email, sms, whatsapp)")
	}
	addr, err := normaliseAddress(channel, addrRaw)
	if err != nil {
		return nil, err
	}
	row := ctx.AppDB().QueryRow(
		`SELECT reason, source, COALESCE(first_seen,'')
		 FROM suppressions WHERE project_id = ? AND channel = ? AND address = ?`,
		pid, channel, addr,
	)
	var reason, source, firstSeen string
	if err := row.Scan(&reason, &source, &firstSeen); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return map[string]any{
				"suppressed": false,
				"channel":    channel,
				"address":    addr,
			}, nil
		}
		return nil, err
	}
	return map[string]any{
		"suppressed":     true,
		"reason":         reason,
		"source":         source,
		"channel":        channel,
		"address":        addr,
		"suppressed_at":  firstSeen,
	}, nil
}

func (a *App) toolSuppressionRemove(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	addrRaw := strArg(args, "address")
	channel := strArg(args, "channel")
	if channel == "" {
		channel = guessChannelFromAddress(addrRaw)
	}
	if !validChannel(channel) {
		return nil, errors.New("channel: required for suppression_remove (one of email, sms, whatsapp)")
	}
	addr, err := normaliseAddress(channel, addrRaw)
	if err != nil {
		return nil, err
	}
	_, err = ctx.AppDB().Exec(
		`DELETE FROM suppressions WHERE project_id = ? AND channel = ? AND address = ?`,
		pid, channel, addr,
	)
	if err != nil {
		return nil, err
	}
	return map[string]any{"removed": true, "address": addr, "channel": channel}, nil
}

// guessChannelFromAddress returns the most likely channel for a
// given address shape — email if it has '@', sms if it's an E.164
// phone, else "" (caller must supply channel explicitly). Used to
// keep panel UX terse for the common single-channel cases.
func guessChannelFromAddress(s string) string {
	s = strings.TrimSpace(stripScheme(s))
	if strings.Contains(s, "@") {
		return channelEmail
	}
	if looksLikeE164(s) {
		return channelSMS
	}
	return ""
}

// ─── senders_* tools ───────────────────────────────────────────────
//
// Thin proxies over the bound email_provider integration's SES v2
// surface, with response normalisation so panel + agents see a
// uniform shape across channels (only mailto: today; tel:/etc when
// SMS arrives). The address argument accepts either a canonical URI
// ("mailto:foo@bar.com") or a bare value ("foo@bar.com" / "bar.com").
// Domains are detected by absence of "@".

type Sender struct {
	Channel    string   `json:"channel"`               // "email" | "sms" | "whatsapp"
	Address    string   `json:"address"`               // plain (alice@x.com or +15551234567)
	Kind       string   `json:"kind"`                  // "email" | "domain" | "phone"
	Verified   bool     `json:"verified"`
	DKIMStatus string   `json:"dkim_status,omitempty"` // email-only — "SUCCESS"|"PENDING"|"FAILED"|"NOT_STARTED"
	DKIMTokens []string `json:"dkim_tokens,omitempty"` // populated by senders_get for domain identities
	Sending    bool     `json:"sending_enabled"`
}

// classifyEmailIdentity returns ("domain", "bar.com") or ("email",
// "foo@bar.com") given a free-form input. Bare; URI prefix is stripped.
func classifyEmailIdentity(s string) (kind, raw string, err error) {
	s = strings.TrimSpace(s)
	if strings.HasPrefix(strings.ToLower(s), "mailto:") {
		s = s[7:]
	}
	if s == "" {
		return "", "", errors.New("address required")
	}
	if strings.Contains(s, "@") {
		if !looksLikeEmail(strings.ToLower(s)) {
			return "", "", fmt.Errorf("invalid email: %q", s)
		}
		return "domain_member_email", strings.ToLower(s), nil
	}
	// crude domain check: at least one dot, no spaces, no slashes.
	if strings.IndexByte(s, '.') < 0 || strings.ContainsAny(s, " /\t\r\n") {
		return "", "", fmt.Errorf("not an email or domain: %q", s)
	}
	return "domain", strings.ToLower(s), nil
}

// canonicalSenderAddress returns the plain stored form for a sender
// identity. v0.3 dropped scheme prefixes; the lowercased raw value
// IS the canonical form. Kept as a wrapper so the call sites read
// like a deliberate canonicalisation step.
func canonicalSenderAddress(_ string, raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// emailProviderTool resolves the bound email_provider's tool name
// for a given capability, returning the failure string the panel
// understands when no provider is bound.
func emailProviderConn(ctx *sdk.AppCtx) (connID int64, toolFor func(string) string, err error) {
	bound := ctx.IntegrationFor("email_provider")
	if bound == nil {
		return 0, nil, errors.New("no email_provider bound — install/select an aws-ses connection in app settings")
	}
	return bound.ConnectionID, bound.ToolFor, nil
}

// toolSendersList reads from the local senders table. The local
// table is the operator's curated set — empty means empty, even if
// the bound SES/Twilio accounts have identities. To add a sender,
// call senders_create (which also adopts already-verified upstream
// identities). Staleness on known rows (> senderStaleThreshold)
// triggers a background refresh that updates DKIM / verification
// status without blocking the response or importing unknowns.
//
// Filters: channel? (email|sms|whatsapp), verified_only? (bool).
func (a *App) toolSendersList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	channel := strArg(args, "channel")
	verifiedOnly, _ := args["verified_only"].(bool)

	rows, err := dbListSenders(ctx.AppDB(), pid, channel, verifiedOnly)
	if err != nil {
		return nil, fmt.Errorf("list senders (local): %w", err)
	}
	if stale, _ := dbHasStaleSenders(ctx.AppDB(), pid, channel); stale {
		// Stale known rows → fire-and-forget background refresh.
		go func() {
			if err := a.refreshSendersFromProviders(ctx, pid); err != nil {
				ctx.Logger().Warn("senders background refresh", "err", err)
			}
		}()
	}
	out := make([]map[string]any, 0, len(rows))
	for _, r := range rows {
		out = append(out, senderRowToMap(r))
	}
	return map[string]any{"senders": out, "count": len(out)}, nil
}

// toolSendersGet reads the local row + does an opportunistic provider
// probe to refresh DKIM / verification status. This is the "I clicked
// re-check on a row" path — always picks up the latest.
func (a *App) toolSendersGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	addr := strArg(args, "address")
	// Default channel = email when address looks like one; phones can
	// be probed by explicit channel arg.
	channel := strArg(args, "channel")
	if channel == "" {
		channel = inferChannelFromAddress(addr)
		if channel == "" {
			channel = "email"
		}
	}
	local, _ := dbFindSender(ctx.AppDB(), pid, channel, addr)
	// Probe the provider for the freshest state — best-effort. If
	// the probe fails we still return the local row.
	if channel == "email" {
		_ = a.refreshOneSESIdentity(ctx, pid, addr)
	} else if channel == "sms" || channel == "whatsapp" {
		_ = a.refreshOneTwilioNumber(ctx, pid, channel, addr)
	}
	local, _ = dbFindSender(ctx.AppDB(), pid, channel, addr)
	if local == nil {
		return nil, fmt.Errorf("sender %s not found in project %s", addr, pid)
	}
	return senderRowToMap(local), nil
}

// refreshOneSESIdentity probes SES for a single identity and upserts
// the local row. Used by senders_get for the click-to-recheck path.
func (a *App) refreshOneSESIdentity(ctx *sdk.AppCtx, pid, addr string) error {
	bound := ctx.IntegrationFor("email_provider")
	if bound == nil {
		return errors.New("email_provider not bound")
	}
	_, raw, err := classifyEmailIdentity(addr)
	if err != nil {
		return err
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, "get_identity_verification", map[string]any{
		"EmailIdentity": raw,
	})
	if err != nil {
		return err
	}
	if res == nil || !res.Success {
		body := ""
		if res != nil {
			body = string(res.Data)
		}
		return fmt.Errorf("provider non-2xx: %s", truncate(body, 400))
	}
	// SES v2 GetEmailIdentity:
	//   { IdentityType, VerifiedForSendingStatus, DkimAttributes:{Status, Tokens, SigningEnabled},
	//     FeedbackForwardingStatus, Policies, ConfigurationSetName? }
	var inner struct {
		IdentityType             string   `json:"IdentityType"`
		VerifiedForSendingStatus bool     `json:"VerifiedForSendingStatus"`
		DkimAttributes           struct {
			Status         string   `json:"Status"`
			Tokens         []string `json:"Tokens"`
			SigningEnabled bool     `json:"SigningEnabled"`
		} `json:"DkimAttributes"`
		FeedbackForwardingStatus bool `json:"FeedbackForwardingStatus"`
	}
	_ = json.Unmarshal(res.Data, &inner)
	kind := "email"
	if inner.IdentityType == "DOMAIN" || inner.IdentityType == "MANAGED_DOMAIN" {
		kind = "domain"
	}
	dkimStatus := inner.DkimAttributes.Status
	_, err = dbUpsertSender(ctx.AppDB(), &senderUpsert{
		ProjectID:          pid,
		Channel:            "email",
		Address:            raw,
		Kind:               kind,
		Provider:           "aws-ses",
		ProviderIdentityID: raw,
		Verified:           inner.VerifiedForSendingStatus,
		VerificationStatus: domainVerificationStatus(dkimStatus),
		SendingEnabled:     true,
		DkimStatus:         dkimStatus,
		MarkSyncedNow:      true,
	})
	return err
}

// refreshOneTwilioNumber probes Twilio for a single phone number and
// upserts the local row.
func (a *App) refreshOneTwilioNumber(ctx *sdk.AppCtx, pid, channel, addr string) error {
	bound := ctx.IntegrationFor("phone_provider")
	if bound == nil {
		return errors.New("phone_provider not bound")
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, "list_phone_numbers", map[string]any{
		"PhoneNumber": addr,
		"PageSize":    10,
	})
	if err != nil {
		return err
	}
	if res == nil || !res.Success {
		body := ""
		if res != nil {
			body = string(res.Data)
		}
		return fmt.Errorf("provider non-2xx: %s", truncate(body, 400))
	}
	var listed struct {
		IncomingPhoneNumbers []struct {
			SID         string `json:"sid"`
			PhoneNumber string `json:"phone_number"`
			SmsURL      string `json:"sms_url"`
		} `json:"incoming_phone_numbers"`
	}
	_ = json.Unmarshal(res.Data, &listed)
	for _, pn := range listed.IncomingPhoneNumbers {
		if pn.PhoneNumber == addr {
			_, err := dbUpsertSender(ctx.AppDB(), &senderUpsert{
				ProjectID:          pid,
				Channel:            channel,
				Address:            addr,
				Kind:               "phone",
				Provider:           "twilio",
				ProviderIdentityID: pn.SID,
				Verified:           true,
				VerificationStatus: "verified",
				SendingEnabled:     true,
				MarkSyncedNow:      true,
			})
			return err
		}
	}
	// Not found — the number was released. Soft-delete the local row.
	return dbSoftDeleteSender(ctx.AppDB(), pid, channel, addr)
}

func (a *App) toolSendersDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	addr := strArg(args, "address")
	channel := strArg(args, "channel")
	if channel == "" {
		channel = inferChannelFromAddress(addr)
		if channel == "" {
			channel = "email"
		}
	}
	// Look up provider from the local row (so we know which integration
	// to call). Fall back to channel-based default if the row is missing.
	local, _ := dbFindSender(ctx.AppDB(), pid, channel, addr)
	provider := ""
	if local != nil {
		provider = local.Provider
	} else if channel == "email" {
		provider = "aws-ses"
	} else {
		provider = "twilio"
	}

	switch provider {
	case "aws-ses":
		connID, _, err := emailProviderConn(ctx)
		if err != nil {
			return nil, err
		}
		_, raw, err := classifyEmailIdentity(addr)
		if err != nil {
			return nil, err
		}
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "delete_identity", map[string]any{
			"EmailIdentity": raw,
		})
		if err != nil {
			return nil, fmt.Errorf("delete_identity: %w", err)
		}
		if res == nil || !res.Success {
			body := ""
			if res != nil {
				body = string(res.Data)
			}
			return nil, fmt.Errorf("provider non-2xx: %s", truncate(body, 400))
		}
	case "twilio":
		// Releasing a Twilio number stops billing for it but is destructive
		// (the number goes back to the pool). For now we just clear the
		// SmsUrl webhook + soft-delete locally — operators who want to
		// fully release the number do it via twilio.release_phone_number.
		if local != nil && local.ProviderIdentityID != "" {
			phoneBound := ctx.IntegrationFor("phone_provider")
			if phoneBound != nil {
				_, _ = ctx.PlatformAPI().ExecuteIntegrationTool(phoneBound.ConnectionID, "update_phone_number", map[string]any{
					"PhoneNumberSid": local.ProviderIdentityID,
					"SmsUrl":         "",
				})
			}
		}
	default:
		return nil, fmt.Errorf("unsupported provider %q for senders_delete", provider)
	}

	if err := dbSoftDeleteSender(ctx.AppDB(), pid, channel, addr); err != nil {
		return nil, fmt.Errorf("soft delete: %w", err)
	}
	return map[string]any{"deleted": true, "address": addr, "channel": channel}, nil
}

// dkimCNAMERecords formats SES's three DKIM tokens as ready-to-paste
// CNAME records: <token>._domainkey.<domain>  CNAME  <token>.dkim.amazonses.com
func dkimCNAMERecords(domain string, tokens []string) []map[string]string {
	out := make([]map[string]string, 0, len(tokens))
	for _, t := range tokens {
		out = append(out, map[string]string{
			"name":  t + "._domainkey." + domain,
			"type":  "CNAME",
			"value": t + ".dkim.amazonses.com",
		})
	}
	return out
}

func normaliseSenderKind(k string) string {
	if k == "domain" {
		return "domain"
	}
	return "email"
}

func verifyNextStepHint(kind string) string {
	if kind == "domain" {
		return "Publish the three CNAME records in your DNS, then call senders_get to re-check status."
	}
	return "Click the verification link the provider just emailed to that address, then call senders_get to re-check status."
}

func (a *App) toolSendersGetQuota(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	connID, _, err := emailProviderConn(ctx)
	if err != nil {
		return nil, err
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "get_quota", map[string]any{})
	if err != nil {
		return nil, fmt.Errorf("get_quota: %w", err)
	}
	if res == nil || !res.Success {
		body := ""
		if res != nil {
			body = string(res.Data)
		}
		return nil, fmt.Errorf("provider non-2xx: %s", truncate(body, 400))
	}
	// SES v2 GetAccount:
	//   { SendQuota:{Max24HourSend, MaxSendRate, SentLast24Hours},
	//     SendingEnabled, ProductionAccessEnabled, EnforcementStatus, ... }
	var inner struct {
		SendQuota struct {
			Max24HourSend    float64 `json:"Max24HourSend"`
			MaxSendRate      float64 `json:"MaxSendRate"`
			SentLast24Hours  float64 `json:"SentLast24Hours"`
		} `json:"SendQuota"`
		SendingEnabled           bool   `json:"SendingEnabled"`
		ProductionAccessEnabled  bool   `json:"ProductionAccessEnabled"`
		EnforcementStatus        string `json:"EnforcementStatus"`
	}
	_ = json.Unmarshal(res.Data, &inner)
	return map[string]any{
		"sandboxed":                !inner.ProductionAccessEnabled,
		"sending_enabled":          inner.SendingEnabled,
		"production_access":        inner.ProductionAccessEnabled,
		"enforcement_status":       inner.EnforcementStatus,
		"send_quota_24h":           inner.SendQuota.Max24HourSend,
		"send_rate_per_second":     inner.SendQuota.MaxSendRate,
		"sent_last_24h":            inner.SendQuota.SentLast24Hours,
	}, nil
}

// ─── Bounce / complaint webhook ────────────────────────────────────

// SES → SNS notifications come in as JSON. Two relevant fields:
//   - notificationType: "Bounce" | "Complaint" | "Delivery" | "Reject"
//   - bounce / complaint / delivery: the per-event payload
//   - mail.messageId: the SES MessageId we stored as provider_message_id
//
// We stash the full envelope into delivery_events.raw and update the
// summary fields on the parent message row.
func (a *App) handleBounceWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 1<<20))
	if err != nil {
		httpErr(w, http.StatusBadRequest, "read body")
		return
	}
	if !verifySNS(r, body, globalCtx) {
		httpErr(w, http.StatusForbidden, "signature failed")
		return
	}
	env, err := parseSNSEnvelope(body)
	if err != nil {
		httpErr(w, http.StatusBadRequest, "envelope: "+err.Error())
		return
	}
	if env.Type == "SubscriptionConfirmation" {
		// Auto-confirm by GETting SubscribeURL.
		if env.SubscribeURL != "" {
			go confirmSNSSubscription(env.SubscribeURL)
		}
		httpJSON(w, map[string]any{"confirmed": true})
		return
	}

	notif, err := parseSESNotification(env.Message)
	if err != nil {
		httpErr(w, http.StatusBadRequest, "ses notification: "+err.Error())
		return
	}
	pid, _ := resolveProjectFromRequest(r)
	if pid == "" {
		// Webhook came in without a project query param — fall back to
		// looking up the message across all projects via provider id.
		pid = ""
	}
	msg, err := dbMessageByProviderID(globalCtx.AppDB(), pid, notif.MessageID)
	if err != nil || msg == nil {
		// Unknown SES message — store the event with a NULL
		// message_id-attached row would violate FK, so we just log.
		globalCtx.Logger().Warn("webhook: unknown provider message id",
			"provider_message_id", notif.MessageID,
			"kind", notif.Kind)
		httpJSON(w, map[string]any{"ok": true, "matched": false})
		return
	}

	now := time.Now().UTC().Format(time.RFC3339)
	for _, recip := range notif.Recipients {
		_, _ = globalCtx.AppDB().Exec(
			`INSERT INTO delivery_events (message_id, kind, recipient, reason, raw)
			 VALUES (?, ?, ?, ?, ?)`,
			msg.ID, notif.Kind, recip.Address, recip.Reason, string(notif.Raw),
		)
		if (notif.Kind == "bounced" && recip.Permanent) || notif.Kind == "complained" {
			suppressionReason := "hard-bounce"
			if notif.Kind == "complained" {
				suppressionReason = "complaint"
			}
			canonical := canonicalAddrForChannel(msg.Channel, recip.Address)
			_ = dbSuppressionUpsert(globalCtx.AppDB(), msg.ProjectID, msg.Channel, canonical, suppressionReason, "auto")
		}
	}
	terminal := mapSESKindToStatus(notif.Kind)
	_, _ = globalCtx.AppDB().Exec(
		`UPDATE messages SET status = ?, last_event_at = ? WHERE id = ?`,
		terminal, now, msg.ID,
	)
	globalCtx.Emit("message.event", map[string]any{
		"message_id": msg.ID,
		"kind":       notif.Kind,
	})
	httpJSON(w, map[string]any{"ok": true, "matched": true, "message_id": msg.ID, "kind": notif.Kind})
}

func mapSESKindToStatus(kind string) string {
	switch kind {
	case "delivered":
		return "delivered"
	case "bounced":
		return "bounced"
	case "complained":
		return "complained"
	case "rejected":
		return "failed"
	}
	return "sent"
}

// ─── Inbound webhook ───────────────────────────────────────────────

// SES inbound notifications come either with `content` (full MIME)
// or with an S3 action pointer. v0.1 supports the `content` path
// only — S3 fetch is v0.2.
func (a *App) handleInboundWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	body, err := io.ReadAll(io.LimitReader(r.Body, 30<<20))
	if err != nil {
		httpErr(w, http.StatusBadRequest, "read body")
		return
	}
	if !verifySNS(r, body, globalCtx) {
		httpErr(w, http.StatusForbidden, "signature failed")
		return
	}
	env, err := parseSNSEnvelope(body)
	if err != nil {
		httpErr(w, http.StatusBadRequest, "envelope: "+err.Error())
		return
	}
	if env.Type == "SubscriptionConfirmation" {
		if env.SubscribeURL != "" {
			go confirmSNSSubscription(env.SubscribeURL)
		}
		httpJSON(w, map[string]any{"confirmed": true})
		return
	}

	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}

	parsed, sesEnv, err := parseSESInboundContent(env.Message)
	if err != nil {
		httpErr(w, http.StatusBadRequest, "ses inbound: "+err.Error())
		return
	}
	// S3-action mode: no inline content, but receipt.action.bucketName +
	// objectKey tell us where to fetch the .eml from.
	s3Key := ""
	if parsed == nil && sesEnv != nil &&
		sesEnv.Receipt.Action.Type == "S3" &&
		sesEnv.Receipt.Action.BucketName != "" &&
		sesEnv.Receipt.Action.ObjectKey != "" {
		s3Key = sesEnv.Receipt.Action.BucketName + "/" + sesEnv.Receipt.Action.ObjectKey
		bytes, err := fetchSESInboundFromS3(globalCtx, sesEnv.Receipt.Action.BucketName, sesEnv.Receipt.Action.ObjectKey)
		if err != nil {
			// Persist a minimal row so the operator sees it failed
			// rather than silently dropping. inbound_redispatch can
			// re-attempt once the catalog gains s3.get_object.
			globalCtx.Logger().Warn("ses S3-mode fetch failed", "key", s3Key, "err", err)
			httpErr(w, http.StatusBadGateway, "ses S3 fetch: "+err.Error())
			return
		}
		parsed, err = parseRawEml(bytes, sesEnv.Mail.MessageID)
		if err != nil {
			httpErr(w, http.StatusBadRequest, "ses S3 parse: "+err.Error())
			return
		}
	}
	if parsed == nil {
		// Notification carried neither inline content nor an S3 pointer —
		// SES "Stop" or "Bounce" actions don't deliver a body to us.
		httpJSON(w, map[string]any{"ok": true, "skipped": "no content/S3 pointer in notification"})
		return
	}

	verdictsJSON, _ := json.Marshal(sesEnv.extractVerdicts())
	if len(verdictsJSON) == 0 {
		verdictsJSON = []byte("{}")
	}

	to := normaliseEmailListPlain(parsed.To)
	cc := normaliseEmailListPlain(parsed.Cc)
	from := normaliseEmailFromHeader(parsed.From)
	if from == "" {
		from = "unknown@invalid"
	}
	hdrJSON, _ := json.Marshal(parsed.Headers)
	toJSON, _ := json.Marshal(to)
	ccJSON, _ := json.Marshal(cc)
	refsJSON, _ := json.Marshal(parsed.References)
	now := time.Now().UTC().Format(time.RFC3339)

	var s3KeyArg any
	if s3Key != "" {
		s3KeyArg = s3Key
	}
	res, err := globalCtx.AppDB().Exec(
		`INSERT INTO messages
			(project_id, channel, direction, from_addr, to_addrs, cc_addrs,
			 subject, body_text, body_html, headers,
			 message_id_header, in_reply_to, references_json,
			 status, route_status, received_at, last_event_at,
			 verdicts, s3_key)
		 VALUES (?, 'email', 'in', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'received', 'pending', ?, ?, ?, ?)`,
		pid, from, string(toJSON), string(ccJSON),
		parsed.Subject, parsed.BodyText, parsed.BodyHTML, string(hdrJSON),
		parsed.MessageID, parsed.InReplyTo, string(refsJSON),
		now, now,
		string(verdictsJSON), s3KeyArg,
	)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "persist: "+err.Error())
		return
	}
	id, _ := res.LastInsertId()
	m, _ := dbMessageGet(globalCtx.AppDB(), pid, id)

	if err := dispatchInbound(globalCtx, pid, m); err != nil {
		globalCtx.Logger().Warn("dispatch failed", "id", id, "err", err)
	}
	globalCtx.Emit("message.received", map[string]any{
		"id":      id,
		"channel": "email",
		"from":    from,
	})
	httpJSON(w, map[string]any{"ok": true, "id": id})
}

// ─── Twilio inbound webhook ────────────────────────────────────────
//
// Twilio POSTs application/x-www-form-urlencoded with fields:
//   From, To, Body, MessageSid, AccountSid, NumMedia, MediaUrl0...,
//   MediaContentType0..., MessagingServiceSid, FromCountry, FromCity, ...
//
// For WhatsApp, From + To carry the literal "whatsapp:+1..." prefix.
// We strip it before persistence and tag channel="whatsapp".
//
// Authenticity: Twilio signs each request with HMAC-SHA1 of
//   (request URL + sorted-and-concatenated form params)
// using the connection's auth_token, sent in X-Twilio-Signature.
// We verify before doing anything else.

func (a *App) handleTwilioInboundWebhook(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	if err := r.ParseForm(); err != nil {
		httpErr(w, http.StatusBadRequest, "form parse: "+err.Error())
		return
	}
	form := r.PostForm

	bound := globalCtx.IntegrationFor("phone_provider")
	if bound == nil {
		// Without a bound provider we can't even verify the signature.
		// Refuse rather than risk persisting unverified inbound.
		httpErr(w, http.StatusServiceUnavailable, "no phone_provider bound")
		return
	}
	conn, err := globalCtx.PlatformAPI().GetConnection(bound.ConnectionID)
	if err != nil || conn == nil {
		httpErr(w, http.StatusServiceUnavailable, "lookup phone_provider connection: "+errString(err))
		return
	}

	// Twilio's signature URL needs to be exactly what they sent the
	// request to — including scheme, host, path, and query. Behind the
	// platform's reverse proxy we rebuild it from X-Forwarded-* headers.
	signedURL := reconstructPublicURL(r)
	gotSig := r.Header.Get("X-Twilio-Signature")

	// Auth token is in the bound connection's credentials. Today
	// PlatformClient.GetConnection doesn't expose plaintext credentials —
	// we'd need a separate helper to fetch one for signing. v0.5 keeps
	// the verification structure in place; if the runner can't return
	// the auth_token, signature check is skipped with a logged warning.
	authToken := lookupConnectionCredential(globalCtx, bound.ConnectionID, "auth_token")
	if authToken == "" {
		globalCtx.Logger().Warn("twilio inbound: auth_token not retrievable, signature NOT verified", "url", signedURL)
	} else if !verifyTwilioSignature(signedURL, form, authToken, gotSig) {
		httpErr(w, http.StatusForbidden, "twilio signature failed")
		return
	}

	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}

	rawFrom := form.Get("From")
	rawTo := form.Get("To")
	body := form.Get("Body")
	messageSid := form.Get("MessageSid")

	// Channel detection: WhatsApp messages have "whatsapp:+1..." on From.
	channel := channelSMS
	if strings.HasPrefix(strings.ToLower(rawFrom), "whatsapp:") {
		channel = channelWhatsApp
	}
	from := stripScheme(rawFrom)
	to := stripScheme(rawTo)
	// Twilio's "+15551234567" format is already E.164; normalise just
	// in case (case-insensitive scheme strip, whitespace trim).
	from = strings.TrimSpace(from)
	to = strings.TrimSpace(to)

	toJSON, _ := json.Marshal([]string{to})
	hdrs := map[string]string{
		"X-Twilio-Message-Sid":        messageSid,
		"X-Twilio-Account-Sid":        form.Get("AccountSid"),
		"X-Twilio-MessagingService":   form.Get("MessagingServiceSid"),
		"X-Twilio-NumMedia":           form.Get("NumMedia"),
		"X-Twilio-FromCountry":        form.Get("FromCountry"),
	}
	hdrJSON, _ := json.Marshal(hdrs)
	now := time.Now().UTC().Format(time.RFC3339)

	res, err := globalCtx.AppDB().Exec(
		`INSERT INTO messages
			(project_id, channel, direction, from_addr, to_addrs, cc_addrs,
			 subject, body_text, body_html, headers,
			 message_id_header, in_reply_to, references_json,
			 status, route_status, received_at, last_event_at,
			 provider_message_id, verdicts)
		 VALUES (?, ?, 'in', ?, ?, '[]', '', ?, '', ?, ?, '', '[]',
		         'received', 'pending', ?, ?, ?, '{}')`,
		pid, channel, from, string(toJSON),
		body, string(hdrJSON),
		messageSid,
		now, now,
		messageSid,
	)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, "persist: "+err.Error())
		return
	}
	id, _ := res.LastInsertId()

	// STOP-keyword auto-suppression. SMS/WhatsApp opt-out conventions —
	// Twilio handles these server-side too, but mirroring locally means
	// our own send_message blocks it pre-flight.
	if isStopKeyword(body) {
		canonical := canonicalAddrForChannel(channel, from)
		if err := dbSuppressionUpsert(globalCtx.AppDB(), pid, channel, canonical, "stop-keyword", "auto"); err != nil {
			globalCtx.Logger().Warn("auto-suppress on STOP failed", "err", err)
		}
		globalCtx.Logger().Info("auto-suppressed on STOP keyword", "channel", channel, "address", canonical)
	}

	m, _ := dbMessageGet(globalCtx.AppDB(), pid, id)
	if err := dispatchInbound(globalCtx, pid, m); err != nil {
		globalCtx.Logger().Warn("dispatch failed", "id", id, "err", err)
	}
	globalCtx.Emit("message.received", map[string]any{
		"id":      id,
		"channel": channel,
		"from":    from,
	})
	// Twilio expects a 2xx within 15s or it retries. Empty TwiML body
	// tells Twilio "I handled it; no auto-reply please."
	w.Header().Set("Content-Type", "text/xml")
	w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Response/>`))
}

// verifyTwilioSignature checks the X-Twilio-Signature header per
// Twilio's documented algorithm:
//   1. Concatenate fullURL (including query) with sorted form params
//      written as KEY1VALUE1KEY2VALUE2... (no separators).
//   2. HMAC-SHA1 with authToken as key.
//   3. Base64-encode.
// https://www.twilio.com/docs/usage/webhooks/webhooks-security
func verifyTwilioSignature(fullURL string, form url.Values, authToken, expected string) bool {
	if expected == "" {
		return false
	}
	keys := make([]string, 0, len(form))
	for k := range form {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b strings.Builder
	b.WriteString(fullURL)
	for _, k := range keys {
		// Twilio takes the FIRST value when a key repeats.
		b.WriteString(k)
		if vs := form[k]; len(vs) > 0 {
			b.WriteString(vs[0])
		}
	}
	mac := hmac.New(sha1.New, []byte(authToken))
	mac.Write([]byte(b.String()))
	want := base64.StdEncoding.EncodeToString(mac.Sum(nil))
	return hmac.Equal([]byte(want), []byte(expected))
}

// reconstructPublicURL rebuilds the URL Twilio actually called us at,
// from request headers + X-Forwarded-* fields the platform proxy adds.
// Twilio signs the *external* URL, not the per-pod forwarded form.
func reconstructPublicURL(r *http.Request) string {
	scheme := r.Header.Get("X-Forwarded-Proto")
	if scheme == "" {
		if r.TLS != nil {
			scheme = "https"
		} else {
			scheme = "http"
		}
	}
	host := r.Header.Get("X-Forwarded-Host")
	if host == "" {
		host = r.Host
	}
	path := r.URL.RequestURI() // includes ?query
	return scheme + "://" + host + path
}

// lookupConnectionCredential is a placeholder for retrieving a single
// credential field (e.g. auth_token) for signature verification.
// PlatformClient.GetConnection currently returns metadata only — no
// plaintext credentials cross the wire. v0.5 ships the verification
// structure; production deployments either expose a credential-fetch
// method on the platform API, or rely on a shared webhook secret
// that's stored as a config field on the messaging install.
func lookupConnectionCredential(ctx *sdk.AppCtx, connID int64, field string) string {
	// Allow operator override via app config — useful for the
	// "platform doesn't expose plaintext" case. They set
	// twilio_auth_token on the messaging install and we use that.
	if v := strings.TrimSpace(ctx.Config().Get("twilio_auth_token")); v != "" && field == "auth_token" {
		return v
	}
	return ""
}

// isStopKeyword detects SMS/WhatsApp opt-out body text.
func isStopKeyword(body string) bool {
	t := strings.TrimSpace(strings.ToUpper(body))
	switch t {
	case "STOP", "STOPALL", "UNSUBSCRIBE", "END", "QUIT", "CANCEL", "OPTOUT", "OPT-OUT":
		return true
	}
	return false
}

func errString(e error) string {
	if e == nil {
		return ""
	}
	return e.Error()
}

// dispatchInbound looks up the matching inbound_route for each
// recipient on the message and POSTs the normalised JSON to the
// target app's HTTP route. First match wins per message; ties go to
// priority DESC, then longest-pattern.
func dispatchInbound(ctx *sdk.AppCtx, pid string, m *Message) error {
	if m == nil {
		return errors.New("nil message")
	}
	routes, err := dbInboundRouteList(ctx.AppDB(), pid)
	if err != nil {
		return err
	}
	type matched struct {
		recipient string
		route     *InboundRoute
		subaddr   string
	}
	var winner *matched
	for _, recip := range append(append([]string{}, m.To...), m.CC...) {
		for i := range routes {
			r := &routes[i]
			if r.Channel != "" && r.Channel != m.Channel {
				continue
			}
			ok, sub := patternMatches(m.Channel, r.Pattern, recip)
			if !ok {
				continue
			}
			if winner == nil ||
				r.Priority > winner.route.Priority ||
				(r.Priority == winner.route.Priority && len(r.Pattern) > len(winner.route.Pattern)) {
				winner = &matched{recipient: recip, route: r, subaddr: sub}
			}
		}
	}
	now := time.Now().UTC().Format(time.RFC3339)
	if winner == nil {
		_, _ = ctx.AppDB().Exec(
			`UPDATE messages SET route_status='no_match', route_attempts = route_attempts + 1, last_event_at = ? WHERE id = ?`,
			now, m.ID,
		)
		return nil
	}

	// Build dispatch payload.
	hdr := map[string]any{}
	_ = json.Unmarshal(m.Headers, &hdr)
	payload := map[string]any{
		"message_id":         m.ID,
		"channel":            m.Channel,
		"matched_recipient":  winner.recipient,
		"matched_pattern":    winner.route.Pattern,
		"to_subaddress":      winner.subaddr,
		"from":               m.From,
		"to":                 m.To,
		"cc":                 m.CC,
		"subject":            m.Subject,
		"body_text":          m.BodyText,
		"body_html":          m.BodyHTML,
		"message_id_header":  m.MessageIDHeader,
		"in_reply_to":        m.InReplyTo,
		"references":         m.References,
		"headers":            hdr,
		"received_at":        m.ReceivedAt,
	}

	_, callErr := ctx.PlatformAPI().CallApp(winner.route.TargetApp, normaliseRoutePath(winner.route.TargetRoute), payload)
	status := "ok"
	errMsg := ""
	if callErr != nil {
		status = "target_failed"
		errMsg = truncate(callErr.Error(), 500)
	}
	_, _ = ctx.AppDB().Exec(
		`UPDATE messages
		 SET route_status = ?, route_target_app = ?, route_target_route = ?,
		     route_error = ?, route_attempts = route_attempts + 1,
		     matched_recipient = ?, matched_pattern = ?, to_subaddress = ?, last_event_at = ?
		 WHERE id = ?`,
		status, winner.route.TargetApp, winner.route.TargetRoute, errMsg,
		winner.recipient, winner.route.Pattern, winner.subaddr, now, m.ID,
	)
	if callErr != nil {
		return callErr
	}
	return nil
}

// patternMatches checks whether `addr` (a canonical URI) matches
// `pattern`. Wildcards (`*`) are allowed in the local part of an
// email pattern only — the domain and scheme must match exactly.
// Returns (matched, subaddress) — subaddress is the captured "+tag"
// when the pattern contains a literal "+*" marker.
// patternMatches checks whether `addr` matches `pattern` for the
// given channel. Both inputs are plain addresses (no scheme prefix).
//
// Email patterns support local-part wildcards:
//   - exact:        "support@acme.com"
//   - full-local:   "*@acme.com" (any local part) — captures full local in subaddress slot
//   - subaddress:   "support+*@acme.com" (any +tag) — captures the tag
//
// SMS / WhatsApp patterns support exact match or "*" for any number;
// no subaddress concept on phone channels.
func patternMatches(channel, pattern, addr string) (bool, string) {
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	addr = strings.ToLower(strings.TrimSpace(addr))
	if pattern == addr {
		return true, ""
	}
	switch channel {
	case channelEmail:
		pAt := strings.IndexByte(pattern, '@')
		aAt := strings.IndexByte(addr, '@')
		if pAt < 0 || aAt < 0 {
			return false, ""
		}
		pLocal, pDomain := pattern[:pAt], pattern[pAt+1:]
		aLocal, aDomain := addr[:aAt], addr[aAt+1:]
		if pDomain != aDomain {
			return false, ""
		}
		switch {
		case pLocal == aLocal:
			return true, ""
		case pLocal == "*":
			return true, extractSubaddress(addr)
		case strings.HasSuffix(pLocal, "+*"):
			prefix := strings.TrimSuffix(pLocal, "+*")
			if !strings.HasPrefix(aLocal, prefix+"+") {
				return false, ""
			}
			return true, aLocal[len(prefix)+1:]
		}
	case channelSMS, channelWhatsApp:
		if pattern == "*" {
			return true, ""
		}
	}
	return false, ""
}

func normaliseRoutePath(p string) string {
	if p == "" {
		return "/"
	}
	if !strings.HasPrefix(p, "/") {
		p = "/" + p
	}
	return p
}

// ─── SNS / SES parsing ─────────────────────────────────────────────

type snsEnvelope struct {
	Type             string          `json:"Type"`
	MessageID        string          `json:"MessageId"`
	TopicARN         string          `json:"TopicArn"`
	Message          string          `json:"Message"`
	Timestamp        string          `json:"Timestamp"`
	Signature        string          `json:"Signature"`
	SignatureVersion string          `json:"SignatureVersion"`
	SigningCertURL   string          `json:"SigningCertURL"`
	SubscribeURL     string          `json:"SubscribeURL"`
	Token            string          `json:"Token"`
	UnsubURL         string          `json:"UnsubscribeURL"`
	_extra           json.RawMessage // unused
}

func parseSNSEnvelope(body []byte) (*snsEnvelope, error) {
	var env snsEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return nil, err
	}
	return &env, nil
}

// verifySNS does the cheap-but-reasonable v0.1 check:
//   - SigningCertURL host must be on amazonaws.com
//   - Optional shared HMAC secret in header X-Apteva-Webhook-HMAC must
//     match HMAC(secret, body) when the secret is configured.
//
// Full X.509 cert-chain verification is v0.2; documented in README.
func verifySNS(r *http.Request, body []byte, ctx *sdk.AppCtx) bool {
	if ctx != nil {
		secret := strings.TrimSpace(ctx.Config().Get("webhook_signing_secret"))
		if secret != "" {
			got := r.Header.Get("X-Apteva-Webhook-HMAC")
			want := hmacHex(secret, body)
			if got != want {
				return false
			}
			return true
		}
	}
	// Without a configured secret, fall back to "looks like AWS".
	var env snsEnvelope
	if err := json.Unmarshal(body, &env); err != nil {
		return false
	}
	if env.SigningCertURL == "" {
		// Allow if header marker present (test mode) — production
		// installs should set webhook_signing_secret.
		return r.Header.Get("X-Amz-Sns-Message-Type") != ""
	}
	low := strings.ToLower(env.SigningCertURL)
	return strings.Contains(low, "amazonaws.com")
}

func hmacHex(secret string, body []byte) string {
	h := sha256.New()
	h.Write([]byte(secret))
	h.Write(body)
	return hex.EncodeToString(h.Sum(nil))
}

func confirmSNSSubscription(url string) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(url)
	if err != nil {
		return
	}
	_ = resp.Body.Close()
}

type sesNotification struct {
	Kind       string // delivered | bounced | complained | rejected
	MessageID  string // SES MessageId from the inner mail.messageId
	Recipients []sesRecipient
	Raw        json.RawMessage
}

type sesRecipient struct {
	Address   string
	Reason    string
	Permanent bool
}

func parseSESNotification(message string) (*sesNotification, error) {
	var inner struct {
		NotificationType string `json:"notificationType"`
		Mail             struct {
			MessageID string `json:"messageId"`
		} `json:"mail"`
		Bounce struct {
			BounceType        string `json:"bounceType"`
			BouncedRecipients []struct {
				EmailAddress   string `json:"emailAddress"`
				DiagnosticCode string `json:"diagnosticCode"`
			} `json:"bouncedRecipients"`
		} `json:"bounce"`
		Complaint struct {
			ComplainedRecipients []struct {
				EmailAddress string `json:"emailAddress"`
			} `json:"complainedRecipients"`
			ComplaintFeedbackType string `json:"complaintFeedbackType"`
		} `json:"complaint"`
		Delivery struct {
			Recipients []string `json:"recipients"`
		} `json:"delivery"`
	}
	if err := json.Unmarshal([]byte(message), &inner); err != nil {
		return nil, err
	}
	out := &sesNotification{
		MessageID: inner.Mail.MessageID,
		Raw:       json.RawMessage(message),
	}
	switch strings.ToLower(inner.NotificationType) {
	case "bounce":
		out.Kind = "bounced"
		permanent := strings.EqualFold(inner.Bounce.BounceType, "Permanent")
		for _, r := range inner.Bounce.BouncedRecipients {
			out.Recipients = append(out.Recipients, sesRecipient{
				Address:   r.EmailAddress,
				Reason:    r.DiagnosticCode,
				Permanent: permanent,
			})
		}
	case "complaint":
		out.Kind = "complained"
		for _, r := range inner.Complaint.ComplainedRecipients {
			out.Recipients = append(out.Recipients, sesRecipient{
				Address:   r.EmailAddress,
				Reason:    inner.Complaint.ComplaintFeedbackType,
				Permanent: true,
			})
		}
	case "delivery":
		out.Kind = "delivered"
		for _, addr := range inner.Delivery.Recipients {
			out.Recipients = append(out.Recipients, sesRecipient{Address: addr})
		}
	default:
		out.Kind = strings.ToLower(inner.NotificationType)
	}
	return out, nil
}

// ─── SES inbound parsing ───────────────────────────────────────────

type parsedInbound struct {
	From       string
	To         []string
	Cc         []string
	Subject    string
	BodyText   string
	BodyHTML   string
	MessageID  string
	InReplyTo  string
	References []string
	Headers    map[string]string
}

// sesInboundEnvelope is the parsed shape of the inner JSON SNS
// carries on a Received notification — used to decide between the
// inline-content path and the S3 path, and to extract verdicts.
type sesInboundEnvelope struct {
	NotificationType string `json:"notificationType"`
	Content          string `json:"content"`
	Mail             struct {
		MessageID string                            `json:"messageId"`
		Headers   []struct{ Name, Value string } `json:"headers"`
	} `json:"mail"`
	Receipt struct {
		SpamVerdict   struct{ Status string } `json:"spamVerdict"`
		VirusVerdict  struct{ Status string } `json:"virusVerdict"`
		SPFVerdict    struct{ Status string } `json:"spfVerdict"`
		DKIMVerdict   struct{ Status string } `json:"dkimVerdict"`
		DMARCVerdict  struct{ Status string } `json:"dmarcVerdict"`
		Action        struct {
			Type       string `json:"type"`
			BucketName string `json:"bucketName"` // populated for S3 action
			ObjectKey  string `json:"objectKey"`  // populated for S3 action
		} `json:"action"`
	} `json:"receipt"`
}

// extractVerdicts collapses the SES verdict block into a small map
// the panel can render uniformly. Empty when the receipt didn't
// declare verdicts (e.g., legacy receipt rules without spam scoring
// enabled).
func (e *sesInboundEnvelope) extractVerdicts() map[string]string {
	out := map[string]string{}
	if v := e.Receipt.SpamVerdict.Status; v != "" {
		out["spam"] = v
	}
	if v := e.Receipt.VirusVerdict.Status; v != "" {
		out["virus"] = v
	}
	if v := e.Receipt.SPFVerdict.Status; v != "" {
		out["spf"] = v
	}
	if v := e.Receipt.DKIMVerdict.Status; v != "" {
		out["dkim"] = v
	}
	if v := e.Receipt.DMARCVerdict.Status; v != "" {
		out["dmarc"] = v
	}
	return out
}

// parseSESInboundContent unwraps an SES Received notification.
// Returns (parsed, env, err). env is non-nil even when parsed is
// nil — the caller uses it to read receipt.action for S3-mode
// fallback and verdicts.
func parseSESInboundContent(message string) (*parsedInbound, *sesInboundEnvelope, error) {
	var env sesInboundEnvelope
	if err := json.Unmarshal([]byte(message), &env); err != nil {
		return nil, nil, err
	}
	if env.Content == "" {
		return nil, &env, nil
	}
	parsed, err := parseRawEml([]byte(env.Content), env.Mail.MessageID)
	if err != nil {
		return nil, &env, err
	}
	return parsed, &env, nil
}

// parseRawEml turns a raw RFC 822 .eml byte stream into our shaped
// parsedInbound. Used by both the inline-content path (env.Content)
// and the S3 path (bytes fetched from S3).
func parseRawEml(rawBytes []byte, fallbackMessageID string) (*parsedInbound, error) {
	msg, err := mail.ReadMessage(strings.NewReader(string(rawBytes)))
	if err != nil {
		return nil, fmt.Errorf("read message: %w", err)
	}
	hdrs := map[string]string{}
	for k := range msg.Header {
		hdrs[k] = msg.Header.Get(k)
	}
	body, _ := io.ReadAll(msg.Body)
	bodyText, bodyHTML := extractBodies(msg.Header.Get("Content-Type"), body)

	parsed := &parsedInbound{
		From:       hdrs["From"],
		To:         splitAddrList(hdrs["To"]),
		Cc:         splitAddrList(hdrs["Cc"]),
		Subject:    hdrs["Subject"],
		BodyText:   bodyText,
		BodyHTML:   bodyHTML,
		MessageID:  hdrs["Message-Id"],
		InReplyTo:  hdrs["In-Reply-To"],
		References: splitRefs(hdrs["References"]),
		Headers:    hdrs,
	}
	if parsed.MessageID == "" && fallbackMessageID != "" {
		parsed.MessageID = fallbackMessageID
	}
	return parsed, nil
}

// fetchSESInboundFromS3 fetches the raw .eml from S3 using SigV4-
// signed GET via the inbound_storage (aws-s3) integration. Returns the
// bytes ready for parseRawEml.
//
// Resolution order: inbound_storage → email_provider (legacy fallback,
// since some installs from before the inbound_storage role landed had
// the SES connection pulling double duty).
func fetchSESInboundFromS3(ctx *sdk.AppCtx, bucket, key string) ([]byte, error) {
	connID, tool, err := resolveInboundStorageTool(ctx)
	if err != nil {
		return nil, err
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, tool, map[string]any{
		"bucket": bucket,
		"key":    key,
	})
	if err != nil {
		return nil, fmt.Errorf("s3 get_object: %w", err)
	}
	if res == nil || !res.Success {
		body := ""
		if res != nil {
			body = string(res.Data)
		}
		return nil, fmt.Errorf("s3 get_object non-2xx: %s", truncate(body, 400))
	}
	// Three reasonable response shapes:
	//  1. http-executor binary envelope: { _binary: true, base64: …,
	//     mimeType: …, size: … } — what aws-s3 get_object returns when
	//     S3 sends back application/octet-stream.
	//  2. {body_base64: …} — older variant, still seen for some apps.
	//  3. raw bytes in res.Data — text/plain branch of the executor.
	var probe struct {
		Binary     bool   `json:"_binary"`
		Base64     string `json:"base64"`
		Body       string `json:"body"`
		BodyBase64 string `json:"body_base64"`
	}
	_ = json.Unmarshal(res.Data, &probe)
	if probe.Binary && probe.Base64 != "" {
		if decoded, err := base64.StdEncoding.DecodeString(probe.Base64); err == nil {
			return decoded, nil
		}
	}
	if probe.BodyBase64 != "" {
		if decoded, err := base64.StdEncoding.DecodeString(probe.BodyBase64); err == nil {
			return decoded, nil
		}
	}
	if probe.Body != "" {
		return []byte(probe.Body), nil
	}
	return []byte(res.Data), nil
}

// resolveInboundStorageTool returns the (connection_id, tool_name) pair
// that fetchSESInboundFromS3 should use. Prefers the new
// inbound_storage role; falls back to email_provider for installs from
// before the role landed.
func resolveInboundStorageTool(ctx *sdk.AppCtx) (int64, string, error) {
	if bound := ctx.IntegrationFor("inbound_storage"); bound != nil {
		return bound.ConnectionID, "get_object", nil
	}
	if bound := ctx.IntegrationFor("email_provider"); bound != nil {
		// Legacy fallback. Most aws-ses connections won't expose
		// get_object — this branch will fail with a clear error rather
		// than crash, prompting the operator to bind aws-s3.
		return bound.ConnectionID, "get_object", nil
	}
	return 0, "", errors.New("no S3 binding for inbound — bind the aws-s3 integration to the inbound_storage role")
}

// extractBodies handles single-part text/* directly and multipart by
// pulling the first text/plain and text/html parts. v0.1 is
// best-effort: nested multiparts beyond one level fall through.
func extractBodies(contentType string, body []byte) (text string, html string) {
	ct := strings.ToLower(contentType)
	switch {
	case strings.HasPrefix(ct, "text/plain"):
		return string(body), ""
	case strings.HasPrefix(ct, "text/html"):
		return "", string(body)
	}
	// Naive multipart split — finds boundary= and walks parts. For
	// production we'd swap to mime/multipart but keeping the import
	// surface tiny here.
	if !strings.HasPrefix(ct, "multipart/") {
		return string(body), ""
	}
	boundary := boundaryFromContentType(contentType)
	if boundary == "" {
		return string(body), ""
	}
	parts := strings.Split(string(body), "--"+boundary)
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || p == "--" {
			continue
		}
		// split headers from body inside the part
		hdrEnd := strings.Index(p, "\r\n\r\n")
		if hdrEnd < 0 {
			hdrEnd = strings.Index(p, "\n\n")
		}
		if hdrEnd < 0 {
			continue
		}
		head := strings.ToLower(p[:hdrEnd])
		bodyPart := p[hdrEnd+2:]
		// trim doubled newline depending on which separator hit
		if strings.HasPrefix(p[hdrEnd:], "\r\n\r\n") {
			bodyPart = p[hdrEnd+4:]
		}
		switch {
		case strings.Contains(head, "content-type: text/plain") && text == "":
			text = bodyPart
		case strings.Contains(head, "content-type: text/html") && html == "":
			html = bodyPart
		}
	}
	return text, html
}

func boundaryFromContentType(ct string) string {
	low := strings.ToLower(ct)
	i := strings.Index(low, "boundary=")
	if i < 0 {
		return ""
	}
	rest := ct[i+len("boundary="):]
	rest = strings.TrimSpace(rest)
	rest = strings.TrimPrefix(rest, `"`)
	if j := strings.IndexAny(rest, `";`); j >= 0 {
		rest = rest[:j]
	}
	return rest
}

func splitAddrList(s string) []string {
	if s == "" {
		return nil
	}
	addrs, err := mail.ParseAddressList(s)
	if err != nil {
		// Fall back to simple split.
		out := []string{}
		for _, p := range strings.Split(s, ",") {
			p = strings.TrimSpace(p)
			if p != "" {
				out = append(out, p)
			}
		}
		return out
	}
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, a.Address)
	}
	return out
}

func splitRefs(s string) []string {
	if s == "" {
		return nil
	}
	out := []string{}
	for _, r := range strings.Fields(s) {
		out = append(out, r)
	}
	return out
}

// normaliseEmailFromHeader parses an RFC 5322-shaped header value
// ("Foo <foo@bar.com>") and returns the plain lowercased address.
// Renamed in v0.3 — used by SES inbound parsing only — and no longer
// returns a URI form.
func normaliseEmailFromHeader(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	addr, err := mail.ParseAddress(s)
	if err == nil {
		return strings.ToLower(addr.Address)
	}
	if a, err := normaliseAddress(channelEmail, s); err == nil {
		return a
	}
	return ""
}

func normaliseEmailListPlain(addrs []string) []string {
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		if u := normaliseEmailFromHeader(a); u != "" {
			out = append(out, u)
		}
	}
	return out
}

func canonicalAddrForChannel(channel, addr string) string {
	addr = strings.TrimSpace(stripScheme(addr))
	switch channel {
	case channelEmail:
		return strings.ToLower(addr)
	}
	return addr
}

// ─── HTTP panel handlers ───────────────────────────────────────────

func (a *App) handleMessagesList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	out, err := dbMessageList(globalCtx.AppDB(), pid, messageListOpts{
		Direction: q.Get("direction"),
		Channel:   q.Get("channel"),
		Status:    q.Get("status"),
		Since:     q.Get("since"),
		Address:   q.Get("address"),
		Limit:     limit,
	})
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"messages": out})
}

func (a *App) handleMessageItem(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/messages/")
	id, _ := strconv.ParseInt(rest, 10, 64)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	m, err := dbMessageGet(globalCtx.AppDB(), pid, id)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if m == nil {
		httpErr(w, http.StatusNotFound, "not found")
		return
	}
	events, _ := dbDeliveryEvents(globalCtx.AppDB(), id)
	httpJSON(w, map[string]any{"message": m, "events": events})
}

func (a *App) handleTemplatesList(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	out, err := dbTemplateList(globalCtx.AppDB(), pid, r.URL.Query().Get("channel"), 200)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"templates": out})
}

func (a *App) handleInboundRoutesList(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	out, err := dbInboundRouteList(globalCtx.AppDB(), pid)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"routes": out})
}

func (a *App) handleSuppressionsList(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	out, err := dbSuppressionList(globalCtx.AppDB(), pid, r.URL.Query().Get("channel"), 500)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"suppressions": out})
}

// /senders proxies senders_list — straight pass-through to the tool.
// Errors (no provider bound, provider 5xx) surface as JSON {error}.
func (a *App) handleSendersList(w http.ResponseWriter, r *http.Request) {
	args := map[string]any{}
	if pid := strings.TrimSpace(r.URL.Query().Get("project_id")); pid != "" {
		args["_project_id"] = pid
	}
	if r.URL.Query().Get("verified_only") == "true" {
		args["verified_only"] = true
	}
	if ch := strings.TrimSpace(r.URL.Query().Get("channel")); ch != "" {
		args["channel"] = ch
	}
	out, err := a.toolSendersList(globalCtx, args)
	if err != nil {
		httpErr(w, http.StatusBadGateway, err.Error())
		return
	}
	httpJSON(w, out)
}

func (a *App) handleSendersQuota(w http.ResponseWriter, r *http.Request) {
	out, err := a.toolSendersGetQuota(globalCtx, map[string]any{})
	if err != nil {
		httpErr(w, http.StatusBadGateway, err.Error())
		return
	}
	httpJSON(w, out)
}

// handleSendersDomains — feeds the Add Sender form's domain picker.
// When the Domains app is bound, returns the project's curated domain
// list so the operator picks from it instead of typing free-text.
// When unbound, returns {available: false, domains: []} — the panel
// falls back to the free-text input. Never an error path; the form
// should keep working even if the Domains app is down.
func (a *App) handleSendersDomains(w http.ResponseWriter, r *http.Request) {
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if !isAppDepBound(globalCtx, "domains") {
		httpJSON(w, map[string]any{"available": false, "domains": []any{}})
		return
	}
	domains, err := listDomainsForProject(globalCtx, pid)
	if err != nil {
		// Soft-fail — log and return empty so the form falls back to
		// free-text rather than blocking the panel on a transient
		// Domains-app blip.
		globalCtx.Logger().Warn("senders/domains lookup", "err", err)
		httpJSON(w, map[string]any{"available": true, "domains": []any{}, "error": err.Error()})
		return
	}
	httpJSON(w, map[string]any{"available": true, "domains": domains})
}

// handleTemplatesSync — panel "Sync templates" button. Pulls Twilio
// Content templates into messaging.templates for the requested
// channel. Pulled out of the MCP tool surface deliberately: agents
// shouldn't be triggering provider list calls; sync is either
// operator-driven (this route) or automatic (template_list TTL).
func (a *App) handleTemplatesSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	channel := r.URL.Query().Get("channel")
	if channel == "" {
		channel = channelWhatsApp
	}
	out, err := a.toolTemplatesSyncProvider(globalCtx, map[string]any{"channel": channel})
	if err != nil {
		httpErr(w, http.StatusBadGateway, err.Error())
		return
	}
	httpJSON(w, out)
}

// isAppDepBound checks whether a kind=app dependency is currently
// bound on this install. We don't have a clean SDK helper for "is app
// X reachable?", so we attempt the lightest possible CallApp probe.
// Cheaper alternative: look at WhoAmI bindings.
func isAppDepBound(ctx *sdk.AppCtx, name string) bool {
	id, _ := ctx.PlatformAPI().WhoAmI()
	if id == nil {
		return false
	}
	if v, ok := id.Bindings[name]; ok {
		return v != nil
	}
	return false
}

// handleTemplateItem dispatches /templates/<id>/<action>. Today the
// only action is refresh-status; future actions (preview, force-resync)
// land here.
func (a *App) handleTemplateItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/templates/")
	parts := strings.SplitN(rest, "/", 2)
	id, _ := strconv.ParseInt(parts[0], 10, 64)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	action := ""
	if len(parts) == 2 {
		action = parts[1]
	}
	switch action {
	case "refresh-status":
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		out, err := a.toolTemplatesRefreshStatus(globalCtx, map[string]any{"id": id})
		if err != nil {
			httpErr(w, http.StatusBadGateway, err.Error())
			return
		}
		httpJSON(w, out)
	default:
		httpErr(w, http.StatusNotFound, "unknown action")
	}
}

// handleToolsCall lets the panel invoke any registered MCP tool via
// HTTP. The shape is {"tool": "send_message", "args": {...}} — same
// surface MCP exposes over JSON-RPC, but as plain REST so the panel
// can use its existing api() helper instead of building MCP framing.
//
// Auth: the platform proxy puts the install's bearer token on the
// request before forwarding; sdk.Run's withTokenAuth gates everything
// except /health, so unauthenticated callers can't reach this route
// in production.
func (a *App) handleToolsCall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var body struct {
		Tool string         `json:"tool"`
		Args map[string]any `json:"args"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.Tool == "" {
		httpErr(w, http.StatusBadRequest, "tool required")
		return
	}
	if body.Args == nil {
		body.Args = map[string]any{}
	}
	var handler sdk.ToolHandler
	for _, t := range a.MCPTools() {
		if t.Name == body.Tool {
			handler = t.Handler
			break
		}
	}
	if handler == nil {
		httpErr(w, http.StatusNotFound, "unknown tool: "+body.Tool)
		return
	}
	out, err := handler(globalCtx, body.Args)
	if err != nil {
		httpErr(w, http.StatusBadGateway, err.Error())
		return
	}
	httpJSON(w, out)
}

// ─── DB helpers ────────────────────────────────────────────────────

func dbMessageGet(db *sql.DB, pid string, id int64) (*Message, error) {
	q := `SELECT id, project_id, channel, direction, from_addr, to_addrs, cc_addrs, bcc_addrs,
		COALESCE(subject,''), COALESCE(body_text,''), COALESCE(body_html,''),
		headers, attachment_storage_ids,
		COALESCE(message_id_header,''), COALESCE(in_reply_to,''), references_json,
		status, COALESCE(status_reason,''), COALESCE(provider_message_id,''),
		COALESCE(idempotency_key,''),
		COALESCE(route_target_app,''), COALESCE(route_target_route,''),
		COALESCE(route_status,''), COALESCE(route_error,''), COALESCE(route_attempts,0),
		COALESCE(matched_recipient,''), COALESCE(matched_pattern,''), COALESCE(to_subaddress,''),
		COALESCE(template_id,0),
		COALESCE(verdicts,'{}'), COALESCE(s3_key,''),
		COALESCE(created_at,''), COALESCE(sent_at,''), COALESCE(received_at,''), COALESCE(last_event_at,'')
	FROM messages WHERE id = ?`
	args := []any{id}
	if pid != "" {
		q += ` AND project_id = ?`
		args = append(args, pid)
	}
	row := db.QueryRow(q, args...)
	return scanMessage(row)
}

func dbMessageByProviderID(db *sql.DB, pid, providerID string) (*Message, error) {
	if providerID == "" {
		return nil, nil
	}
	q := `SELECT id FROM messages WHERE provider_message_id = ?`
	args := []any{providerID}
	if pid != "" {
		q += ` AND project_id = ?`
		args = append(args, pid)
	}
	q += ` ORDER BY id DESC LIMIT 1`
	var id int64
	err := db.QueryRow(q, args...).Scan(&id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return dbMessageGet(db, pid, id)
}

func dbFindByIdempotencyKey(db *sql.DB, pid, key string) (*Message, error) {
	if key == "" {
		return nil, nil
	}
	var id int64
	err := db.QueryRow(
		`SELECT id FROM messages WHERE project_id = ? AND idempotency_key = ?`,
		pid, key).Scan(&id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return dbMessageGet(db, pid, id)
}

type messageListOpts struct {
	Direction, Channel, Status, Since, Address string
	Limit                                       int
}

func dbMessageList(db *sql.DB, pid string, opts messageListOpts) ([]*Message, error) {
	where := []string{"project_id = ?"}
	args := []any{pid}
	if opts.Direction != "" {
		where = append(where, "direction = ?")
		args = append(args, opts.Direction)
	}
	if opts.Channel != "" {
		where = append(where, "channel = ?")
		args = append(args, opts.Channel)
	}
	if opts.Status != "" {
		where = append(where, "status = ?")
		args = append(args, opts.Status)
	}
	if opts.Since != "" {
		where = append(where, "created_at >= ?")
		args = append(args, opts.Since)
	}
	if opts.Address != "" {
		where = append(where, "(from_addr = ? OR to_addrs LIKE ? OR cc_addrs LIKE ?)")
		args = append(args, opts.Address, `%"`+opts.Address+`"%`, `%"`+opts.Address+`"%`)
	}
	q := `SELECT id FROM messages WHERE ` + strings.Join(where, " AND ") +
		` ORDER BY created_at DESC LIMIT ?`
	args = append(args, opts.Limit)
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	out := []*Message{}
	for _, id := range ids {
		m, err := dbMessageGet(db, pid, id)
		if err == nil && m != nil {
			out = append(out, m)
		}
	}
	return out, nil
}

func scanMessage(row *sql.Row) (*Message, error) {
	m := &Message{}
	var to, cc, bcc, headers, attachIDs, refs, verdicts string
	var templateID sql.NullInt64
	err := row.Scan(
		&m.ID, &m.ProjectID, &m.Channel, &m.Direction, &m.From,
		&to, &cc, &bcc,
		&m.Subject, &m.BodyText, &m.BodyHTML,
		&headers, &attachIDs,
		&m.MessageIDHeader, &m.InReplyTo, &refs,
		&m.Status, &m.StatusReason, &m.ProviderMessageID,
		&m.IdempotencyKey,
		&m.RouteTargetApp, &m.RouteTargetRoute,
		&m.RouteStatus, &m.RouteError, &m.RouteAttempts,
		&m.MatchedRecipient, &m.MatchedPattern, &m.ToSubaddress,
		&templateID,
		&verdicts, &m.S3Key,
		&m.CreatedAt, &m.SentAt, &m.ReceivedAt, &m.LastEventAt,
	)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(to), &m.To)
	_ = json.Unmarshal([]byte(cc), &m.CC)
	_ = json.Unmarshal([]byte(bcc), &m.BCC)
	_ = json.Unmarshal([]byte(refs), &m.References)
	_ = json.Unmarshal([]byte(attachIDs), &m.AttachmentStorageIDs)
	if m.To == nil {
		m.To = []string{}
	}
	if m.CC == nil {
		m.CC = []string{}
	}
	if m.BCC == nil {
		m.BCC = []string{}
	}
	if m.References == nil {
		m.References = []string{}
	}
	if m.AttachmentStorageIDs == nil {
		m.AttachmentStorageIDs = []int64{}
	}
	if headers == "" {
		headers = "{}"
	}
	m.Headers = json.RawMessage(headers)
	if verdicts == "" {
		verdicts = "{}"
	}
	m.Verdicts = json.RawMessage(verdicts)
	if templateID.Valid {
		m.TemplateID = templateID.Int64
	}
	return m, nil
}

func dbDeliveryEvents(db *sql.DB, msgID int64) ([]*DeliveryEvent, error) {
	rows, err := db.Query(
		`SELECT id, message_id, kind, COALESCE(recipient,''), COALESCE(reason,''),
		        raw, COALESCE(occurred_at,'')
		 FROM delivery_events WHERE message_id = ? ORDER BY id`,
		msgID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*DeliveryEvent{}
	for rows.Next() {
		e := &DeliveryEvent{}
		var raw string
		if err := rows.Scan(&e.ID, &e.MessageID, &e.Kind, &e.Recipient, &e.Reason, &raw, &e.OccurredAt); err == nil {
			e.Raw = json.RawMessage(raw)
			out = append(out, e)
		}
	}
	return out, nil
}

func dbTemplateGet(db *sql.DB, pid string, id int64) (*Template, error) {
	row := db.QueryRow(
		`SELECT id, project_id, channel, name, COALESCE(subject,''),
		        COALESCE(body_text,''), COALESCE(body_html,''),
		        vars_schema,
		        COALESCE(provider_template_id,''), COALESCE(provider_status,''),
		        COALESCE(var_style,'named'), COALESCE(last_synced_at,''),
		        COALESCE(created_at,''), COALESCE(updated_at,'')
		 FROM templates WHERE id = ? AND project_id = ? AND deleted_at IS NULL`,
		id, pid,
	)
	t := &Template{}
	var vars string
	err := row.Scan(&t.ID, &t.ProjectID, &t.Channel, &t.Name, &t.Subject,
		&t.BodyText, &t.BodyHTML, &vars,
		&t.ProviderTemplateID, &t.ProviderStatus, &t.VarStyle, &t.LastSyncedAt,
		&t.CreatedAt, &t.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if vars == "" {
		vars = "{}"
	}
	t.VarsSchema = json.RawMessage(vars)
	return t, nil
}

// dbTemplateGetByProviderID looks up a mirrored row by the provider's
// immutable handle (Twilio ContentSid). Used by the sync upsert path.
func dbTemplateGetByProviderID(db *sql.DB, pid, providerID string) (*Template, error) {
	if providerID == "" {
		return nil, nil
	}
	var id int64
	err := db.QueryRow(
		`SELECT id FROM templates
		 WHERE project_id = ? AND provider_template_id = ? AND deleted_at IS NULL`,
		pid, providerID,
	).Scan(&id)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return dbTemplateGet(db, pid, id)
}

func dbTemplateList(db *sql.DB, pid, channel string, limit int) ([]*Template, error) {
	where := []string{"project_id = ?", "deleted_at IS NULL"}
	args := []any{pid}
	if channel != "" {
		where = append(where, "channel = ?")
		args = append(args, channel)
	}
	args = append(args, limit)
	rows, err := db.Query(
		`SELECT id FROM templates WHERE `+strings.Join(where, " AND ")+
			` ORDER BY name LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()
	out := []*Template{}
	for _, id := range ids {
		t, err := dbTemplateGet(db, pid, id)
		if err == nil && t != nil {
			out = append(out, t)
		}
	}
	return out, nil
}

// ─── template_sync_state helpers ───────────────────────────────────

// dbSyncStateGet returns last_synced_at + last_error for a (pid,
// channel) pair. Empty timestamp means "never synced." in_progress
// is intentionally NOT loaded — the source of truth for that lives
// in the in-memory mutex (syncInFlight) so a process crash doesn't
// strand a row in in_progress=1.
func dbSyncStateGet(db *sql.DB, pid, channel string) (lastSynced string, lastError string, syncedCount int) {
	_ = db.QueryRow(
		`SELECT COALESCE(last_synced_at,''), COALESCE(last_error,''), COALESCE(last_synced_count,0)
		 FROM template_sync_state WHERE project_id = ? AND channel = ?`,
		pid, channel,
	).Scan(&lastSynced, &lastError, &syncedCount)
	return
}

func dbSyncStateMark(db *sql.DB, pid, channel string, count int, errMsg string) error {
	_, err := db.Exec(
		`INSERT INTO template_sync_state (project_id, channel, last_synced_at, last_error, last_synced_count, in_progress)
		 VALUES (?, ?, CURRENT_TIMESTAMP, ?, ?, 0)
		 ON CONFLICT(project_id, channel) DO UPDATE SET
		   last_synced_at = CURRENT_TIMESTAMP,
		   last_error = excluded.last_error,
		   last_synced_count = excluded.last_synced_count,
		   in_progress = 0`,
		pid, channel, errMsg, count,
	)
	return err
}

// ─── Provider template sync (Twilio Content) ───────────────────────
//
// Two layers:
//   1. syncProviderTemplates does the actual list-and-upsert against
//      the bound phone_provider. Synchronous; returns the count + err.
//   2. tryStartBackgroundSync gates concurrent syncs via an in-memory
//      mutex so template_list's auto-sync TTL never fires the same
//      sync twice in flight. Lost on process restart, which is fine
//      — the worst case is one extra Twilio list call after a crash.

var (
	syncMu       sync.Mutex
	syncInFlight = map[string]bool{}
)

func tryStartSync(pid, channel string) bool {
	key := pid + ":" + channel
	syncMu.Lock()
	defer syncMu.Unlock()
	if syncInFlight[key] {
		return false
	}
	syncInFlight[key] = true
	return true
}

func endSync(pid, channel string) {
	key := pid + ":" + channel
	syncMu.Lock()
	delete(syncInFlight, key)
	syncMu.Unlock()
}

// syncProviderTemplates fetches all Twilio Content templates for the
// project's bound phone_provider, upserts them into messaging.templates
// keyed on ContentSid, and records sync state. Returns the upserted
// count + any error. Email channel is a no-op (we render local
// {{var}} templates ourselves; SES templates are out of scope for
// v0.4). SMS is also a no-op today — Twilio's content templates are
// WhatsApp-only.
func syncProviderTemplates(ctx *sdk.AppCtx, pid, channel string) (int, error) {
	if channel != channelWhatsApp {
		// Record a no-op sync so the TTL doesn't keep firing.
		_ = dbSyncStateMark(ctx.AppDB(), pid, channel, 0, "")
		return 0, nil
	}
	bound := ctx.IntegrationFor("phone_provider")
	if bound == nil {
		err := errors.New("no phone_provider bound — install/select a Twilio connection")
		_ = dbSyncStateMark(ctx.AppDB(), pid, channel, 0, err.Error())
		return 0, err
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, "list_content_templates", map[string]any{
		"PageSize": 100,
	})
	if err != nil {
		_ = dbSyncStateMark(ctx.AppDB(), pid, channel, 0, err.Error())
		return 0, fmt.Errorf("list_content_templates: %w", err)
	}
	if res == nil || !res.Success {
		body := ""
		if res != nil {
			body = string(res.Data)
		}
		msg := fmt.Sprintf("provider non-2xx: %s", truncate(body, 400))
		_ = dbSyncStateMark(ctx.AppDB(), pid, channel, 0, msg)
		return 0, errors.New(msg)
	}
	// Twilio /v2/ContentAndApprovals response:
	//   { contents: [{ sid, friendly_name, language, variables, types,
	//                  approval_requests: [{ status: "approved", … }] }],
	//     meta: { … } }
	var raw struct {
		Contents []struct {
			Sid          string         `json:"sid"`
			FriendlyName string         `json:"friendly_name"`
			Language     string         `json:"language"`
			Variables    map[string]any `json:"variables"`
			Types        map[string]any `json:"types"`
			Approval     []struct {
				Status string `json:"status"`
			} `json:"approval_requests"`
		} `json:"contents"`
	}
	_ = json.Unmarshal(res.Data, &raw)

	count := 0
	for _, c := range raw.Contents {
		if c.Sid == "" {
			continue
		}
		status := "pending"
		if len(c.Approval) > 0 {
			status = strings.ToLower(c.Approval[0].Status)
		}
		// Build a preview body from the template's first text type
		// (best-effort — Twilio's `types` is a map keyed by content
		// type like "twilio/text"). We keep it informational only;
		// the real send goes via ContentSid.
		preview := ""
		if t, ok := c.Types["twilio/text"].(map[string]any); ok {
			if b, ok := t["body"].(string); ok {
				preview = b
			}
		}
		varsJSON, _ := json.Marshal(c.Variables)
		if len(varsJSON) == 0 {
			varsJSON = []byte("{}")
		}
		// Upsert by provider_template_id. New rows get inserted; the
		// friendly_name + body preview + status update on existing rows.
		existing, _ := dbTemplateGetByProviderID(ctx.AppDB(), pid, c.Sid)
		if existing == nil {
			_, err := ctx.AppDB().Exec(
				`INSERT INTO templates
					(project_id, channel, name, subject, body_text, body_html,
					 vars_schema, provider_template_id, provider_status,
					 var_style, last_synced_at)
				 VALUES (?, 'whatsapp', ?, '', ?, '', ?, ?, ?, 'numbered', CURRENT_TIMESTAMP)`,
				pid, c.FriendlyName, preview, string(varsJSON), c.Sid, status,
			)
			if err != nil {
				ctx.Logger().Warn("template sync insert failed", "sid", c.Sid, "err", err)
				continue
			}
		} else {
			_, err := ctx.AppDB().Exec(
				`UPDATE templates SET
					name = ?, body_text = ?, vars_schema = ?,
					provider_status = ?, var_style = 'numbered',
					last_synced_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
				 WHERE id = ?`,
				c.FriendlyName, preview, string(varsJSON), status, existing.ID,
			)
			if err != nil {
				ctx.Logger().Warn("template sync update failed", "sid", c.Sid, "err", err)
				continue
			}
		}
		count++
	}
	// Mark rows that disappeared upstream as deleted. Soft — we keep
	// them around so audit/history queries still resolve. Sends fail-
	// fast against status='deleted'.
	if len(raw.Contents) > 0 {
		seen := make([]string, 0, len(raw.Contents))
		for _, c := range raw.Contents {
			if c.Sid != "" {
				seen = append(seen, c.Sid)
			}
		}
		placeholders := strings.Repeat(",?", len(seen))[1:]
		args := []any{pid}
		for _, s := range seen {
			args = append(args, s)
		}
		_, _ = ctx.AppDB().Exec(
			`UPDATE templates
			 SET provider_status = 'deleted', last_synced_at = CURRENT_TIMESTAMP
			 WHERE project_id = ? AND provider_template_id IS NOT NULL
			   AND provider_template_id NOT IN (`+placeholders+`)
			   AND deleted_at IS NULL AND provider_status != 'deleted'`,
			args...,
		)
	}
	_ = dbSyncStateMark(ctx.AppDB(), pid, channel, count, "")
	ctx.Emit("templates.synced", map[string]any{
		"channel": channel,
		"count":   count,
	})
	return count, nil
}

func dbInboundRouteUpsert(db *sql.DB, pid, channel, pattern, app, route string, priority int) (int64, error) {
	var id int64
	err := db.QueryRow(
		`SELECT id FROM inbound_routes
		 WHERE project_id = ? AND channel = ? AND pattern = ? AND target_app = ? AND target_route = ?`,
		pid, channel, pattern, app, route,
	).Scan(&id)
	if err == nil {
		_, err = db.Exec(`UPDATE inbound_routes SET priority = ? WHERE id = ?`, priority, id)
		return id, err
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	res, err := db.Exec(
		`INSERT INTO inbound_routes (project_id, channel, pattern, target_app, target_route, priority)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		pid, channel, pattern, app, route, priority,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func dbInboundRouteGet(db *sql.DB, pid string, id int64) (*InboundRoute, error) {
	row := db.QueryRow(
		`SELECT id, project_id, COALESCE(channel,'email'), pattern, target_app, target_route, priority, COALESCE(created_at,'')
		 FROM inbound_routes WHERE id = ? AND project_id = ?`, id, pid)
	r := &InboundRoute{}
	err := row.Scan(&r.ID, &r.ProjectID, &r.Channel, &r.Pattern, &r.TargetApp, &r.TargetRoute, &r.Priority, &r.CreatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return r, nil
}

func dbInboundRouteList(db *sql.DB, pid string) ([]InboundRoute, error) {
	rows, err := db.Query(
		`SELECT id, project_id, COALESCE(channel,'email'), pattern, target_app, target_route, priority, COALESCE(created_at,'')
		 FROM inbound_routes WHERE project_id = ?
		 ORDER BY priority DESC, length(pattern) DESC`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []InboundRoute{}
	for rows.Next() {
		r := InboundRoute{}
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.Channel, &r.Pattern, &r.TargetApp, &r.TargetRoute, &r.Priority, &r.CreatedAt); err == nil {
			out = append(out, r)
		}
	}
	// Stable secondary sort for deterministic match order.
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Priority != out[j].Priority {
			return out[i].Priority > out[j].Priority
		}
		return len(out[i].Pattern) > len(out[j].Pattern)
	})
	return out, nil
}

func dbInboundRouteDelete(db *sql.DB, pid string, id int64) error {
	_, err := db.Exec(`DELETE FROM inbound_routes WHERE id = ? AND project_id = ?`, id, pid)
	return err
}

func dbSuppressionUpsert(db *sql.DB, pid, channel, addr, reason, source string) error {
	_, err := db.Exec(
		`INSERT INTO suppressions (project_id, channel, address, reason, source, first_seen, last_seen)
		 VALUES (?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		 ON CONFLICT(project_id, channel, address) DO UPDATE SET
		   reason = excluded.reason,
		   source = CASE WHEN suppressions.source = 'manual' THEN 'manual' ELSE excluded.source END,
		   last_seen = CURRENT_TIMESTAMP`,
		pid, channel, addr, reason, source,
	)
	return err
}

func dbSuppressionList(db *sql.DB, pid, channel string, limit int) ([]Suppression, error) {
	where := []string{"project_id = ?"}
	args := []any{pid}
	if channel != "" {
		where = append(where, "channel = ?")
		args = append(args, channel)
	}
	args = append(args, limit)
	rows, err := db.Query(
		`SELECT project_id, channel, address, reason, source,
		        COALESCE(first_seen,''), COALESCE(last_seen,'')
		 FROM suppressions WHERE `+strings.Join(where, " AND ")+
			` ORDER BY last_seen DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Suppression{}
	for rows.Next() {
		s := Suppression{}
		if err := rows.Scan(&s.ProjectID, &s.Channel, &s.Address, &s.Reason, &s.Source, &s.FirstSeen, &s.LastSeen); err == nil {
			out = append(out, s)
		}
	}
	return out, nil
}

func filterSuppressed(db *sql.DB, pid, channel string, addrs []string) (allowed, suppressed []string) {
	if len(addrs) == 0 {
		return addrs, nil
	}
	rows, err := db.Query(
		`SELECT address FROM suppressions WHERE project_id = ? AND channel = ?`,
		pid, channel,
	)
	if err != nil {
		return addrs, nil
	}
	defer rows.Close()
	suppr := map[string]bool{}
	for rows.Next() {
		var a string
		if err := rows.Scan(&a); err == nil {
			suppr[strings.ToLower(a)] = true
		}
	}
	for _, a := range addrs {
		if suppr[strings.ToLower(a)] {
			suppressed = append(suppressed, a)
		} else {
			allowed = append(allowed, a)
		}
	}
	return allowed, suppressed
}

// ─── tiny utilities ────────────────────────────────────────────────

func intArg(args map[string]any, key string, def int) int {
	switch v := args[key].(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case string:
		n, err := strconv.Atoi(strings.TrimSpace(v))
		if err == nil {
			return n
		}
	}
	return def
}

func int64Arg(args map[string]any, key string) int64 {
	switch v := args[key].(type) {
	case float64:
		return int64(v)
	case int:
		return int64(v)
	case int64:
		return v
	case string:
		n, err := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		if err == nil {
			return n
		}
	}
	return 0
}

func int64ArrayArg(args map[string]any, key string) []int64 {
	out := []int64{}
	if arr, ok := args[key].([]any); ok {
		for _, v := range arr {
			switch x := v.(type) {
			case float64:
				out = append(out, int64(x))
			case int64:
				out = append(out, x)
			case int:
				out = append(out, int64(x))
			}
		}
	}
	return out
}

func strArg(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

func mapArg(args map[string]any, key string) map[string]any {
	if v, ok := args[key].(map[string]any); ok {
		return v
	}
	return map[string]any{}
}

func nullableInt64(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}

func schemaObject(props map[string]any, required []string) map[string]any {
	out := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func httpJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

