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
//     recipient URI, and dispatch a normalized JSON payload to the
//     target app via ctx.PlatformAPI().CallApp.
package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha1"
	"crypto/sha256"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"mime"
	"mime/quotedprintable"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"regexp"
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
version: 0.13.34
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
    - platform.connections.read_credentials
    - platform.apps.call
  dynamic_app_calls: true
  integrations:
    - role: email_provider
      kind: integration
      compatible_slugs: [aws-ses]
      capabilities: [email.send]
      tools:
        email.send: send_email
      required: false
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
    - { name: send_message,           description: "Send a message. Channel is an explicit arg (email|sms|whatsapp). Email replies may include in_reply_to + references for threading." }
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
    - { name: suppression_list,       description: "List suppressed exact addresses and domains." }
    - { name: suppression_add,        description: "Suppress an address or email domain for outbound and inbound." }
    - { name: suppression_remove,     description: "Remove an address or email domain from suppression." }
    - { name: suppression_check,      description: "Suppression lookup for an address; checks exact address plus email domain." }
    - { name: senders_list,           description: "List sending identities. Returns canonical URI rows." }
    - { name: senders_get,            description: "Get one identity's verification + DKIM state." }
    - { name: senders_delete,         description: "Remove a sending identity from the provider." }
    - { name: senders_get_quota,      description: "Provider sandbox + send-quota status." }
    - { name: senders_create,         description: "Register a sender across email (SES) + SMS/WhatsApp (Twilio). Domain → DKIM + DNS + optional inbound bootstrap. Phone → adopt + optional Twilio inbound webhook wiring." }
    - { name: senders_refresh,        description: "Reconcile local senders with bound providers." }
    - { name: senders_set_default,    description: "Flip the per-(project, channel) default sender." }
    - { name: senders_update,         description: "Patch local-mutable fields on a sender (display_name, notes)." }
    - { name: identities_list,        description: "List anchor identities (DKIM domains, WABAs)." }
  ui_panels:
    - slot: project.page
      label: Messaging
      icon: mail
      entry: /ui/MessagingPanel.mjs
  workers:
    - name: ses-verify-poller
      schedule: "@every 5m"
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
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

// pollVerifyMaxAge caps how long a non-terminal verification keeps
// getting auto-polled. Past this, a stuck identity (e.g. a permanent
// TEMPORARY_FAILURE) stops generating provider calls and waits for a
// manual senders_refresh. SES DKIM probes normally resolve well within
// this window.
const pollVerifyMaxAge = 7 * 24 * time.Hour

// Workers runs the background SES verification poller. It self-heals
// NOT_STARTED / PENDING / TEMPORARY_FAILURE statuses that the cached
// local row would otherwise hold stale until someone called
// senders_list/get. The worker enumerates the projects with pending
// rows straight from the DB (the install's DB carries every project's
// rows under a project_id column), so it works the same on project- and
// global-scope installs without depending on per-project tick dispatch.
// It makes zero provider calls when every row is already terminal
// (verified/failed) or older than the poll cap.
func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{
		{
			Name:     "ses-verify-poller",
			Schedule: "@every 5m",
			Run: func(_ context.Context, app *sdk.AppCtx) error {
				return a.pollVerifications(app)
			},
		},
	}
}

func (a *App) pollVerifications(ctx *sdk.AppCtx) error {
	bound := ctx.IntegrationFor("email_provider")
	if bound == nil {
		return nil
	}
	pids, err := dbProjectsWithNonTerminalVerifications(ctx.AppDB(), pollVerifyMaxAge)
	if err != nil || len(pids) == 0 {
		return err
	}
	var firstErr error
	for _, pid := range pids {
		if err := a.refreshSESIdentities(ctx, pid, bound.ConnectionID); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/webhooks/ses-bounces", Handler: a.handleBounceWebhook},
		{Pattern: "/webhooks/ses-inbound", Handler: a.handleInboundWebhook},
		{Pattern: "/webhooks/twilio-inbound", Handler: a.handleTwilioInboundWebhook},
		{Pattern: "/webhooks/twilio-status", Handler: a.handleTwilioStatusWebhook},
		{Pattern: "/messages", Handler: a.handleMessagesList},
		{Pattern: "/messages/", Handler: a.handleMessageItem},
		{Pattern: "/templates", Handler: a.handleTemplatesList},
		{Pattern: "/inbound-routes", Handler: a.handleInboundRoutesList},
		{Pattern: "/suppressions", Handler: a.handleSuppressionsList},
		{Pattern: "/senders", Handler: a.handleSendersList},
		{Pattern: "/senders/quota", Handler: a.handleSendersQuota},
		{Pattern: "/senders/domains", Handler: a.handleSendersDomains},
		{Pattern: "/senders/provider-options", Handler: a.handleSendersProviderOptions},
		{Pattern: "/senders/edit", Handler: a.handleSendersEdit},
		{Pattern: "/identities", Handler: a.handleIdentitiesList},
		// Internal/panel routes for provider-template sync. Not MCP —
		// the panel hits these from a button + per-row action; agents
		// don't trigger Twilio list calls.
		{Pattern: "/templates/sync", Handler: a.handleTemplatesSync},
		{Pattern: "/templates/provider-preview", Handler: a.handleTemplatesProviderPreview},
		{Pattern: "/templates/import", Handler: a.handleTemplatesImport},
		{Pattern: "/templates/refresh-statuses", Handler: a.handleTemplatesRefreshStatuses},
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
				"Email-only fields: subject, body_html, cc, bcc, reply_to, in_reply_to, references, headers, attachment_storage_ids. " +
				"SMS/WhatsApp-only fields: media_url, content_sid, content_variables. " +
				"Common: template_id, vars, idempotency_key. " +
				"Addresses are plain — emails (alice@x.com) and E.164 phone numbers (+15551234567), no scheme prefix. " +
				"Returns {id, channel, status, recipients:[{address, status}], provider_message_id?}.",
			InputSchema: schemaObject(map[string]any{
				"channel":                map[string]any{"type": "string", "enum": []string{"email", "sms", "whatsapp"}},
				"from":                   map[string]any{"type": "string"},
				"from_name":              map[string]any{"type": "string", "description": "Email-only friendly From display name. Composes \"Name\" <addr>. Defaults to sender.display_name when unset."},
				"to":                     map[string]any{},
				"body":                   map[string]any{"type": "string"},
				"subject":                map[string]any{"type": "string"},
				"body_html":              map[string]any{"type": "string"},
				"reply_to":               map[string]any{"type": "string"},
				"in_reply_to":            map[string]any{"type": "string"},
				"references":             map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
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
			Description: "List messages. Filters: direction? (in|out), channel?, status?, since? (RFC3339), address? (URI), limit? (default 50, max 200), offset? (default 0). Returns total for pagination.",
			InputSchema: schemaObject(map[string]any{
				"direction": map[string]any{"type": "string"},
				"channel":   map[string]any{"type": "string"},
				"status":    map[string]any{"type": "string"},
				"since":     map[string]any{"type": "string"},
				"address":   map[string]any{"type": "string"},
				"limit":     map[string]any{"type": "integer"},
				"offset":    map[string]any{"type": "integer"},
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
				"Body fields use {{var}} placeholders. Phone templates can set provider_create=true to create Twilio Content; WhatsApp submits for approval by default.",
			InputSchema: schemaObject(map[string]any{
				"name":                map[string]any{"type": "string"},
				"channel":             map[string]any{"type": "string"},
				"subject":             map[string]any{"type": "string"},
				"body_text":           map[string]any{"type": "string"},
				"body_html":           map[string]any{"type": "string"},
				"vars_schema":         map[string]any{"type": "object"},
				"language":            map[string]any{"type": "string"},
				"category":            map[string]any{"type": "string", "enum": []string{"UTILITY", "MARKETING", "AUTHENTICATION"}},
				"provider_create":     map[string]any{"type": "boolean"},
				"submit_for_approval": map[string]any{"type": "boolean"},
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
			Description: "Delete a template by id. Provider-linked Twilio templates are deleted upstream first unless local_only is true.",
			InputSchema: schemaObject(map[string]any{
				"id":         map[string]any{"type": "integer"},
				"local_only": map[string]any{"type": "boolean"},
			}, []string{"id"}),
			Handler: a.toolTemplateDelete,
		},
		{
			Name:        "suppression_list",
			Description: "List suppressed exact addresses and domains. Args: channel?, limit?.",
			InputSchema: schemaObject(map[string]any{
				"channel": map[string]any{"type": "string"},
				"limit":   map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolSuppressionList,
		},
		{
			Name:        "suppression_add",
			Description: "Suppress an exact address or email domain. Suppressions are bidirectional: outbound sends are blocked and inbound messages are persisted but not dispatched. Args: address, channel? (auto-detected), kind? (address|domain; inferred for bare domains), reason?, source?, force? (required for common email domains).",
			InputSchema: schemaObject(map[string]any{
				"address": map[string]any{"type": "string"},
				"channel": map[string]any{"type": "string"},
				"kind":    map[string]any{"type": "string"},
				"reason":  map[string]any{"type": "string"},
				"source":  map[string]any{"type": "string"},
				"force":   map[string]any{"type": "boolean"},
			}, []string{"address"}),
			Handler: a.toolSuppressionAdd,
		},
		{
			Name:        "suppression_remove",
			Description: "Remove an address or domain from suppression. Args: address, channel? (auto-detected), kind? (address|domain; inferred for bare domains).",
			InputSchema: schemaObject(map[string]any{
				"address": map[string]any{"type": "string"},
				"channel": map[string]any{"type": "string"},
				"kind":    map[string]any{"type": "string"},
			}, []string{"address"}),
			Handler: a.toolSuppressionRemove,
		},
		{
			Name:        "suppression_check",
			Description: "Cheap suppression lookup for an address. Checks exact address and, for email, its domain. Returns {suppressed, kind, reason, source, channel, address, matched, suppressed_at}. Args: address, channel? (auto-detected if omitted).",
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
				"channel":       map[string]any{"type": "string"},
				"verified_only": map[string]any{"type": "boolean"},
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
				"\"foo@x.com\" → SES verify_email; \"x.com\" → SES verify_domain + DKIM/SPF/DMARC/custom-MAIL-FROM DNS + (auto when aws-s3+aws-sns bound) full inbound bootstrap; \"+15551234567\" → adopt the Twilio phone for SMS or the approved WhatsApp sender for WhatsApp; SMS auto-wires SmsUrl and WhatsApp auto-wires sender callback_url to /webhooks/twilio-inbound. " +
				"Args: address (required), channel? (email|sms|whatsapp; auto-detected if blank), inbound? (auto|true|false; default auto), publish_dns? (default true), spf? (default true), dmarc? (default true), mail_from? (default true), mail_from_sub? (default mail), region? (email/SES inbound, default eu-west-1), bucket_name?, topic_name?, rule_set_name?, rule_name?, display_name?, set_default? (bool). " +
				"Idempotent. Writes a row in the local senders table. Returns {address, kind, dkim_tokens?, dns_records?, inbound:{bootstrapped, …}, steps[]}.",
			InputSchema: schemaObject(map[string]any{
				"address":       map[string]any{"type": "string"},
				"channel":       map[string]any{"type": "string"},
				"inbound":       map[string]any{"type": "string"},
				"publish_dns":   map[string]any{"type": "boolean"},
				"spf":           map[string]any{"type": "boolean"},
				"dmarc":         map[string]any{"type": "boolean"},
				"mail_from":     map[string]any{"type": "boolean"},
				"mail_from_sub": map[string]any{"type": "string"},
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
		{
			Name:        "senders_update",
			Description: "Patch local-mutable fields on a sender — display_name (used as the friendly From: \"Name\" <addr>) and notes. No provider round-trip. For the default-sender flag use senders_set_default. To change verification / DKIM / inbound state, use senders_create (idempotent) or senders_refresh. Empty values preserve existing.",
			InputSchema: schemaObject(map[string]any{
				"address":      map[string]any{"type": "string"},
				"channel":      map[string]any{"type": "string"},
				"display_name": map[string]any{"type": "string"},
				"notes":        map[string]any{"type": "string"},
			}, []string{"address"}),
			Handler: a.toolSendersUpdate,
		},
		{
			Name:        "identities_list",
			Description: "List authentication anchors (DKIM-verified domains, WhatsApp Business Accounts, etc.) — the verify-once identities that enable sending without being valid From values themselves. NOT for compose/From dropdowns; use senders_list for that. Args: kind? (filter, e.g. 'email_domain').",
			InputSchema: schemaObject(map[string]any{
				"kind": map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolIdentitiesList,
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

func emitMessagingEvent(ctx *sdk.AppCtx, pid, topic string, payload map[string]any) {
	if ctx == nil {
		return
	}
	if strings.TrimSpace(pid) == "" {
		ctx.Logger().Warn("messaging emit without project", "topic", topic)
		ctx.Emit(topic, payload)
		return
	}
	ctx.EmitWithProject(topic, pid, payload)
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
// formatFriendlyAddress builds an RFC 5322 mailbox-list entry of the
// form `"Display Name" <addr>`. The display name is always quoted
// even when it doesn't strictly need to be — quoting handles every
// edge (commas, parentheses, dots in names) without us re-implementing
// RFC 5322's atext rules. Inner quotes and backslashes get escaped.
//
// SES's FromEmailAddress accepts this shape directly; mail clients
// render the display name in the inbox instead of the bare address.
func formatFriendlyAddress(displayName, addr string) string {
	dn := strings.TrimSpace(displayName)
	if dn == "" {
		return addr
	}
	escaped := strings.ReplaceAll(dn, `\`, `\\`)
	escaped = strings.ReplaceAll(escaped, `"`, `\"`)
	return fmt.Sprintf(`"%s" <%s>`, escaped, addr)
}

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
	Verdicts    json.RawMessage `json:"verdicts,omitempty"`
	S3Key       string          `json:"s3_key,omitempty"`
	CreatedAt   string          `json:"created_at,omitempty"`
	SentAt      string          `json:"sent_at,omitempty"`
	ReceivedAt  string          `json:"received_at,omitempty"`
	LastEventAt string          `json:"last_event_at,omitempty"`
	EventCounts map[string]int  `json:"event_counts,omitempty"`
}

type Template struct {
	ID         int64           `json:"id"`
	ProjectID  string          `json:"project_id,omitempty"`
	Channel    string          `json:"channel"`
	Name       string          `json:"name"`
	Subject    string          `json:"subject,omitempty"`
	BodyText   string          `json:"body_text,omitempty"`
	BodyHTML   string          `json:"body_html,omitempty"`
	VarsSchema json.RawMessage `json:"vars_schema"`
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
	Kind      string `json:"kind"`
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

type providerEvent struct {
	Provider          string
	ProviderMessageID string
	Kind              string
	Recipient         string
	Reason            string
	OccurredAt        string
	Metadata          map[string]any
	Raw               json.RawMessage
	Permanent         bool
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
			if channel == channelWhatsApp && tpl.ProviderStatus != "" && tpl.ProviderStatus != "approved" {
				return nil, fmt.Errorf("template_id %d has provider_status=%q (need 'approved'); call templates_refresh_status to refresh",
					templateID, tpl.ProviderStatus)
			}
			if tpl.ProviderStatus == "deleted" || tpl.ProviderStatus == "rejected" {
				return nil, fmt.Errorf("template_id %d has provider_status=%q", templateID, tpl.ProviderStatus)
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
		if def, err := dbDefaultSender(ctx.AppDB(), pid, channel); err == nil && def != nil {
			from = def.Address
		}
	}
	if from == "" {
		return nil, errors.New("from: required (pick a verified sender via senders_list or set a default sender)")
	}
	from, err = normaliseAddress(channel, from)
	if err != nil {
		return nil, fmt.Errorf("from: %w", err)
	}
	// Compose RFC 5322 friendly-form From for email: "Display Name"
	// <addr>. Precedence: explicit from_name arg > sender.display_name
	// looked up from the local senders row > none (raw address).
	// SES's FromEmailAddress accepts the friendly form directly; SMS
	// has no analogous concept so we skip it on non-email channels.
	if channel == "email" {
		fromName := strArg(args, "from_name")
		if fromName == "" {
			if s, _ := dbFindSender(ctx.AppDB(), pid, "email", from); s != nil {
				fromName = s.DisplayName
			}
		}
		if fromName != "" {
			from = formatFriendlyAddress(fromName, from)
		}
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
	inReplyTo := strings.TrimSpace(strArg(args, "in_reply_to"))
	references := stringArrayArg(args, "references")
	referencesJSON, _ := json.Marshal(references)

	attachIDs := int64ArrayArg(args, "attachment_storage_ids")
	attachJSON, _ := json.Marshal(attachIDs)

	// Suppression check — drop any recipient that's on the list.
	allowedTo, suppressedTo := filterSuppressed(ctx.AppDB(), pid, channel, to)
	allowedCC, _ := filterSuppressed(ctx.AppDB(), pid, channel, cc)
	allowedBCC, _ := filterSuppressed(ctx.AppDB(), pid, channel, bcc)
	if len(allowedTo) == 0 {
		return nil, fmt.Errorf("all 'to' recipients are suppressed: %v", suppressedTo)
	}
	if channel == channelWhatsApp && contentSid == "" {
		if missing := whatsAppRecipientsOutsideSession(ctx.AppDB(), pid, from, allowedTo, time.Now().UTC()); len(missing) > 0 {
			return nil, fmt.Errorf("whatsapp free-form send requires an inbound message from each recipient within the last 24 hours; use an approved template for: %s", strings.Join(missing, ", "))
		}
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
		inReplyTo,
		string(referencesJSON),
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
		ReplyTo: replyTo, InReplyTo: inReplyTo, References: references, Headers: headers,
		MediaURL:         mediaURL,
		ContentSid:       contentSid,
		ContentVariables: contentVars,
		MessageID:        id,
		ProjectID:        pid,
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
	emitMessagingEvent(ctx, pid, "message.sent", map[string]any{
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
	InReplyTo     string
	References    []string
	Headers       map[string]any
	MessageID     int64
	ProjectID     string
	// SMS / WhatsApp only:
	MediaURL         string
	ContentSid       string
	ContentVariables string
}

const sesEventConfigurationSetName = "apteva-messaging"

// sendViaSES maps our flat input to AWS SES v2. Normal sends use
// SendEmail Content.Simple. Threaded replies switch to send_raw_email
// so RFC 5322 In-Reply-To / References headers actually reach mail
// clients instead of only being stored locally.
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
	if in.InReplyTo != "" || len(in.References) > 0 {
		return sendViaSESRaw(ctx, bound.ConnectionID, dest, in)
	}

	body := map[string]any{}
	if in.BodyText != "" {
		body["Text"] = map[string]any{"Data": in.BodyText, "Charset": "UTF-8"}
	}
	bodyHTML := in.BodyHTML
	if bodyHTML == "" && in.BodyText != "" {
		bodyHTML = textBodyToTrackingHTML(in.BodyText)
	}
	if bodyHTML != "" {
		body["Html"] = map[string]any{"Data": bodyHTML, "Charset": "UTF-8"}
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
	trackingEnabled := in.MessageID > 0
	if trackingEnabled {
		payload["ConfigurationSetName"] = sesEventConfigurationSetName
		payload["EmailTags"] = []map[string]string{
			{"Name": "apteva_message_id", "Value": strconv.FormatInt(in.MessageID, 10)},
			{"Name": "project_id", "Value": in.ProjectID},
		}
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
		if trackingEnabled && looksLikeSESConfigSetMissing(body) {
			delete(payload, "ConfigurationSetName")
			delete(payload, "EmailTags")
			ctx.Logger().Warn("messaging: SES event config set missing; retrying send without event tracking",
				"config_set", sesEventConfigurationSetName)
			res, err = ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, tool, payload)
			if err != nil {
				return "", err
			}
			if res != nil && res.Success {
				body = ""
			} else if res != nil {
				body = string(res.Data)
			}
		}
		if res != nil && res.Success {
			goto parseResult
		}
		return "", fmt.Errorf("provider non-2xx: %s", truncate(body, 400))
	}
parseResult:
	var probe map[string]any
	_ = json.Unmarshal(res.Data, &probe)
	for _, key := range []string{"MessageId", "message_id", "messageId", "id"} {
		if v, ok := probe[key].(string); ok && v != "" {
			return v, nil
		}
	}
	return "", nil
}

func sendViaSESRaw(ctx *sdk.AppCtx, connID int64, dest map[string]any, in providerSendInput) (string, error) {
	raw, err := buildRawEmail(in)
	if err != nil {
		return "", err
	}
	payload := map[string]any{
		"FromEmailAddress": sesEnvelopeFrom(in.From),
		"Destination":      dest,
		"Content": map[string]any{
			"Raw": map[string]any{
				"Data": base64.StdEncoding.EncodeToString(raw),
			},
		},
	}
	trackingEnabled := in.MessageID > 0
	if trackingEnabled {
		payload["ConfigurationSetName"] = sesEventConfigurationSetName
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "send_raw_email", payload)
	if err != nil {
		return "", err
	}
	if res == nil || !res.Success {
		body := ""
		if res != nil {
			body = string(res.Data)
		}
		if trackingEnabled && looksLikeSESConfigSetMissing(body) {
			delete(payload, "ConfigurationSetName")
			ctx.Logger().Warn("messaging: SES event config set missing; retrying raw send without event tracking",
				"config_set", sesEventConfigurationSetName)
			res, err = ctx.PlatformAPI().ExecuteIntegrationTool(connID, "send_raw_email", payload)
			if err != nil {
				return "", err
			}
			if res != nil && res.Success {
				body = ""
			} else if res != nil {
				body = string(res.Data)
			}
		}
		if res == nil || !res.Success {
			return "", fmt.Errorf("provider non-2xx: %s", truncate(body, 400))
		}
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

func buildRawEmail(in providerSendInput) ([]byte, error) {
	subj := in.Subject
	if subj == "" {
		subj = "(no subject)"
	}
	bodyHTML := in.BodyHTML
	if bodyHTML == "" && in.BodyText != "" {
		bodyHTML = textBodyToTrackingHTML(in.BodyText)
	}
	var b bytes.Buffer
	writeHeader(&b, "From", in.From)
	writeHeader(&b, "To", strings.Join(in.To, ", "))
	if len(in.CC) > 0 {
		writeHeader(&b, "Cc", strings.Join(in.CC, ", "))
	}
	if in.ReplyTo != "" {
		writeHeader(&b, "Reply-To", in.ReplyTo)
	}
	writeHeader(&b, "Subject", mime.QEncoding.Encode("UTF-8", subj))
	writeHeader(&b, "Date", time.Now().UTC().Format(time.RFC1123Z))
	if in.MessageID > 0 {
		writeHeader(&b, "Message-ID", fmt.Sprintf("<apteva-message-%d@apteva.local>", in.MessageID))
	}
	if in.InReplyTo != "" {
		writeHeader(&b, "In-Reply-To", in.InReplyTo)
	}
	if len(in.References) > 0 {
		writeHeader(&b, "References", strings.Join(in.References, " "))
	}
	writeHeader(&b, "MIME-Version", "1.0")
	for k, v := range in.Headers {
		if !rawCustomHeaderAllowed(k) {
			continue
		}
		if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
			writeHeader(&b, k, s)
		}
	}

	switch {
	case in.BodyText != "" && bodyHTML != "":
		boundary := fmt.Sprintf("apteva-alt-%d", in.MessageID)
		writeHeader(&b, "Content-Type", fmt.Sprintf(`multipart/alternative; boundary="%s"`, boundary))
		b.WriteString("\r\n")
		writeMIMEPart(&b, boundary, "text/plain; charset=UTF-8", in.BodyText)
		writeMIMEPart(&b, boundary, "text/html; charset=UTF-8", bodyHTML)
		fmt.Fprintf(&b, "--%s--\r\n", boundary)
	case bodyHTML != "":
		writeHeader(&b, "Content-Type", "text/html; charset=UTF-8")
		writeHeader(&b, "Content-Transfer-Encoding", "base64")
		b.WriteString("\r\n")
		b.WriteString(wrapBase64(bodyHTML))
	case in.BodyText != "":
		writeHeader(&b, "Content-Type", "text/plain; charset=UTF-8")
		writeHeader(&b, "Content-Transfer-Encoding", "base64")
		b.WriteString("\r\n")
		b.WriteString(wrapBase64(in.BodyText))
	default:
		return nil, errors.New("raw email body is empty")
	}
	return b.Bytes(), nil
}

func writeMIMEPart(b *bytes.Buffer, boundary, contentType, body string) {
	fmt.Fprintf(b, "--%s\r\n", boundary)
	writeHeader(b, "Content-Type", contentType)
	writeHeader(b, "Content-Transfer-Encoding", "base64")
	b.WriteString("\r\n")
	b.WriteString(wrapBase64(body))
}

func writeHeader(b *bytes.Buffer, name, value string) {
	value = strings.TrimSpace(strings.NewReplacer("\r", " ", "\n", " ").Replace(value))
	if value == "" {
		return
	}
	fmt.Fprintf(b, "%s: %s\r\n", name, value)
}

func wrapBase64(s string) string {
	enc := base64.StdEncoding.EncodeToString([]byte(s))
	var b strings.Builder
	for len(enc) > 76 {
		b.WriteString(enc[:76])
		b.WriteString("\r\n")
		enc = enc[76:]
	}
	b.WriteString(enc)
	b.WriteString("\r\n")
	return b.String()
}

func rawCustomHeaderAllowed(name string) bool {
	switch strings.ToLower(strings.TrimSpace(name)) {
	case "", "from", "to", "cc", "bcc", "subject", "date", "message-id", "in-reply-to", "references", "mime-version", "content-type", "content-transfer-encoding":
		return false
	default:
		return !strings.ContainsAny(name, ":\r\n")
	}
}

func sesEnvelopeFrom(from string) string {
	if addr, err := mail.ParseAddress(from); err == nil && addr.Address != "" {
		return addr.Address
	}
	return from
}

func textBodyToTrackingHTML(s string) string {
	escaped := html.EscapeString(s)
	escaped = strings.ReplaceAll(escaped, "\r\n", "\n")
	escaped = strings.ReplaceAll(escaped, "\r", "\n")
	escaped = strings.ReplaceAll(escaped, "\n", "<br>\n")
	return "<!doctype html><html><body>" + escaped + "</body></html>"
}

func looksLikeSESConfigSetMissing(body string) bool {
	low := strings.ToLower(body)
	return strings.Contains(low, "configuration set") &&
		(strings.Contains(low, "not exist") ||
			strings.Contains(low, "not found") ||
			strings.Contains(low, "doesn't exist"))
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
		if cb := twilioWebhookURL(ctx, "/webhooks/twilio-status", in.ProjectID); cb != "" {
			payload["StatusCallback"] = cb
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

func twilioWebhookURL(ctx *sdk.AppCtx, routePath, projectID string) string {
	if ctx == nil {
		return ""
	}
	id, _ := ctx.PlatformAPI().WhoAmI()
	if id == nil || strings.TrimSpace(id.PublicURL) == "" {
		return ""
	}
	base := strings.TrimSuffix(strings.TrimSpace(id.PublicURL), "/")
	q := url.Values{}
	if token := strings.TrimSpace(os.Getenv("APTEVA_APP_TOKEN")); token != "" {
		q.Set("api_key", token)
	}
	if projectID != "" {
		q.Set("project_id", projectID)
	}
	out := base + "/api/apps/messaging" + routePath
	if enc := q.Encode(); enc != "" {
		out += "?" + enc
	}
	return out
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
		Offset:    intArg(args, "offset", 0),
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
	if opts.Offset < 0 {
		opts.Offset = 0
	}
	out, total, err := dbMessageListPage(ctx.AppDB(), pid, opts)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"messages": out,
		"count":    len(out),
		"total":    total,
		"limit":    opts.Limit,
		"offset":   opts.Offset,
		"has_more": opts.Offset+len(out) < total,
	}, nil
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
	if strings.TrimSpace(pattern) != "*" {
		probe := strings.ReplaceAll(pattern, "*", "x")
		if _, err := normaliseAddress(channel, probe); err != nil {
			return nil, fmt.Errorf("pattern: %w", err)
		}
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
	if !validChannel(channel) {
		return nil, fmt.Errorf("channel: unsupported value %q (one of email, sms, whatsapp)", channel)
	}
	subject := strArg(args, "subject")
	bodyText := strArg(args, "body_text")
	bodyHTML := strArg(args, "body_html")
	varsSchema := mapArg(args, "vars_schema")
	varsRaw, _ := json.Marshal(varsSchema)
	var providerID, providerStatus string
	varStyle := "named"
	var lastSynced any
	providerMeta := map[string]any{}
	providerCreate := boolArg(args, "provider_create", channel == channelWhatsApp)
	if (channel == channelSMS || channel == channelWhatsApp) && providerCreate {
		created, err := createProviderTemplate(ctx, channel, name, bodyText, varsSchema, args)
		if err != nil {
			return nil, err
		}
		providerID = created.ProviderTemplateID
		providerStatus = created.ProviderStatus
		varStyle = "numbered"
		lastSynced = time.Now().UTC().Format(time.RFC3339)
		providerMeta = created.Meta
	}
	res, err := ctx.AppDB().Exec(
		`INSERT INTO templates
			(project_id, channel, name, subject, body_text, body_html, vars_schema,
			 provider_template_id, provider_status, var_style, last_synced_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, channel, name, subject, bodyText, bodyHTML, string(varsRaw),
		nullableString(providerID), nullableString(providerStatus), varStyle, lastSynced,
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	t, _ := dbTemplateGet(ctx.AppDB(), pid, id)
	out := map[string]any{"template": t}
	if len(providerMeta) > 0 {
		out["provider"] = providerMeta
	}
	return out, nil
}

type providerTemplateCreate struct {
	ProviderTemplateID string
	ProviderStatus     string
	Meta               map[string]any
}

func createProviderTemplate(ctx *sdk.AppCtx, channel, name, bodyText string, varsSchema map[string]any, args map[string]any) (*providerTemplateCreate, error) {
	if bodyText == "" {
		return nil, errors.New("body_text required for Twilio provider-backed templates")
	}
	if err := validateWhatsAppTemplatePlaceholders(bodyText); err != nil {
		return nil, err
	}
	bound := ctx.IntegrationFor("phone_provider")
	if bound == nil {
		return nil, errors.New("no phone_provider bound — install/select a Twilio connection for phone templates")
	}
	language := strings.TrimSpace(strArg(args, "language"))
	if language == "" {
		language = "en"
	}
	category := normaliseTemplateCategory(strArg(args, "category"))
	variables := twilioTemplateVariables(bodyText, varsSchema)
	createRes, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, "create_content_template", map[string]any{
		"friendly_name": name,
		"language":      language,
		"variables":     variables,
		"types": map[string]any{
			"twilio/text": map[string]any{"body": bodyText},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("create provider template: %w", err)
	}
	if createRes == nil || !createRes.Success {
		body := ""
		if createRes != nil {
			body = string(createRes.Data)
		}
		return nil, fmt.Errorf("create provider template: provider non-2xx: %s", truncate(body, 400))
	}
	providerID := extractProviderTemplateID(createRes.Data)
	if providerID == "" {
		return nil, fmt.Errorf("create provider template: provider response missing template id: %s", truncate(string(createRes.Data), 400))
	}
	status := "created"
	submitted := false
	if channel == channelWhatsApp {
		status = "draft"
	}
	if channel == channelWhatsApp && boolArg(args, "submit_for_approval", true) {
		submitStatus, err := submitProviderTemplate(ctx, bound.ConnectionID, providerID, name, category)
		if err != nil {
			return nil, err
		}
		status = submitStatus
		submitted = true
	}
	return &providerTemplateCreate{
		ProviderTemplateID: providerID,
		ProviderStatus:     status,
		Meta: map[string]any{
			"provider_template_id": providerID,
			"provider_status":      status,
			"submitted":            submitted,
			"language":             language,
			"category":             category,
		},
	}, nil
}

func submitProviderTemplate(ctx *sdk.AppCtx, connectionID int64, providerID, name, category string) (string, error) {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connectionID, "submit_content_template_approval", map[string]any{
		"ContentSid": providerID,
		"name":       providerApprovalName(name),
		"category":   normaliseTemplateCategory(category),
	})
	if err != nil {
		return "", fmt.Errorf("submit provider template: %w", err)
	}
	if res == nil || !res.Success {
		body := ""
		if res != nil {
			body = string(res.Data)
		}
		return "", fmt.Errorf("submit provider template: provider non-2xx: %s", truncate(body, 400))
	}
	if status := extractApprovalStatus(res.Data); status != "" {
		return status, nil
	}
	return "pending", nil
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

func (a *App) toolTemplateSubmit(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	t, err := dbTemplateGet(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, fmt.Errorf("template_id %d not found", id)
	}
	if t.Channel != channelWhatsApp {
		return map[string]any{
			"template": t,
			"skipped":  true,
			"reason":   fmt.Sprintf("no provider approval flow for channel %q", t.Channel),
		}, nil
	}
	bound := ctx.IntegrationFor("phone_provider")
	if bound == nil {
		return nil, errors.New("no phone_provider bound — install/select a Twilio connection for WhatsApp templates")
	}
	providerID := t.ProviderTemplateID
	if providerID == "" {
		vars := map[string]any{}
		if len(t.VarsSchema) > 0 {
			_ = json.Unmarshal(t.VarsSchema, &vars)
		}
		created, err := createProviderTemplate(ctx, t.Channel, t.Name, t.BodyText, vars, map[string]any{
			"language":            strArg(args, "language"),
			"category":            strArg(args, "category"),
			"submit_for_approval": false,
		})
		if err != nil {
			return nil, err
		}
		providerID = created.ProviderTemplateID
		_, err = ctx.AppDB().Exec(
			`UPDATE templates SET provider_template_id = ?, provider_status = ?, var_style = 'numbered',
			       last_synced_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
			 WHERE id = ? AND project_id = ?`,
			providerID, created.ProviderStatus, id, pid,
		)
		if err != nil {
			return nil, err
		}
	}
	status, err := submitProviderTemplate(ctx, bound.ConnectionID, providerID, t.Name, strArg(args, "category"))
	if err != nil {
		return nil, err
	}
	_, err = ctx.AppDB().Exec(
		`UPDATE templates SET provider_status = ?, var_style = 'numbered',
		       last_synced_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND project_id = ?`,
		status, id, pid,
	)
	if err != nil {
		return nil, err
	}
	updated, _ := dbTemplateGet(ctx.AppDB(), pid, id)
	return map[string]any{"template": updated, "status": status, "submitted": true}, nil
}

func (a *App) templateCreateProvider(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	t, err := dbTemplateGet(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, fmt.Errorf("template_id %d not found", id)
	}
	if t.Channel != channelSMS && t.Channel != channelWhatsApp {
		return map[string]any{
			"template": t,
			"skipped":  true,
			"reason":   fmt.Sprintf("no provider template flow for channel %q", t.Channel),
		}, nil
	}
	if t.ProviderTemplateID != "" {
		return map[string]any{"template": t, "skipped": true, "reason": "already provider-backed"}, nil
	}
	vars := map[string]any{}
	if len(t.VarsSchema) > 0 {
		_ = json.Unmarshal(t.VarsSchema, &vars)
	}
	created, err := createProviderTemplate(ctx, t.Channel, t.Name, t.BodyText, vars, map[string]any{
		"language":            strArg(args, "language"),
		"category":            strArg(args, "category"),
		"submit_for_approval": false,
	})
	if err != nil {
		return nil, err
	}
	_, err = ctx.AppDB().Exec(
		`UPDATE templates SET provider_template_id = ?, provider_status = ?, var_style = 'numbered',
		       last_synced_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND project_id = ?`,
		created.ProviderTemplateID, created.ProviderStatus, id, pid,
	)
	if err != nil {
		return nil, err
	}
	updated, _ := dbTemplateGet(ctx.AppDB(), pid, id)
	return map[string]any{"template": updated, "provider": created.Meta}, nil
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
		"templates":       out,
		"count":           len(out),
		"last_synced_at":  lastSynced,
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
			"synced":  0,
			"skipped": true,
			"reason":  fmt.Sprintf("no provider sync for channel %q (local templates only)", channel),
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
	var raw struct {
		ApprovalRequests any `json:"approval_requests"`
	}
	_ = json.Unmarshal(res.Data, &raw)
	status := t.ProviderStatus
	if info := approvalInfoFromAny(raw.ApprovalRequests); info.Status != "" {
		status = info.Status
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
	if id == 0 {
		return nil, errors.New("id required")
	}
	t, err := dbTemplateGet(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	if t == nil {
		return nil, fmt.Errorf("template_id %d not found", id)
	}

	localOnly := boolArg(args, "local_only", false)
	providerDeleted := false
	providerAlreadyGone := false
	if t.ProviderTemplateID != "" && !localOnly {
		bound := ctx.IntegrationFor("phone_provider")
		if bound == nil {
			return nil, errors.New("no phone_provider bound — cannot delete provider-backed template; pass local_only=true to hide only the local row")
		}
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, "delete_content_template", map[string]any{
			"ContentSid": t.ProviderTemplateID,
		})
		if err != nil {
			return nil, fmt.Errorf("delete provider template: %w", err)
		}
		if res == nil || !res.Success {
			body := ""
			if res != nil {
				body = string(res.Data)
			}
			if providerTemplateDeleteAlreadyGone(res, body) {
				providerAlreadyGone = true
			} else {
				return nil, fmt.Errorf("delete provider template: provider non-2xx: %s", truncate(body, 400))
			}
		} else {
			providerDeleted = true
		}
	}

	_, err = ctx.AppDB().Exec(
		`UPDATE templates SET deleted_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND project_id = ?`,
		id, pid,
	)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"deleted":               true,
		"local_only":            localOnly,
		"provider_deleted":      providerDeleted,
		"provider_already_gone": providerAlreadyGone,
		"provider_template_id":  t.ProviderTemplateID,
	}, nil
}

func providerTemplateDeleteAlreadyGone(res *sdk.ExecuteResult, body string) bool {
	if res != nil && res.Status == http.StatusNotFound {
		return true
	}
	lower := strings.ToLower(body)
	for _, marker := range []string{"20404", "not found", "does not exist", "was not found"} {
		if strings.Contains(lower, marker) {
			return true
		}
	}
	return false
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
	channel, kind, value, err := normaliseSuppressionTarget(strArg(args, "channel"), strArg(args, "kind"), strArg(args, "address"))
	if err != nil {
		return nil, err
	}
	if kind == "domain" && isCommonSuppressionDomain(value) && !boolArg(args, "force", false) {
		return nil, fmt.Errorf("domain %q is a common mailbox provider; pass force=true to suppress it", value)
	}
	reason := strArg(args, "reason")
	if reason == "" {
		reason = "manual"
	}
	source := strArg(args, "source")
	if source == "" {
		source = "manual"
	}
	if err := dbSuppressionUpsertKind(ctx.AppDB(), pid, channel, kind, value, reason, source); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true, "address": value, "channel": channel, "kind": kind, "reason": reason}, nil
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
	match, err := dbSuppressionMatch(ctx.AppDB(), pid, channel, addr)
	if err != nil {
		return nil, err
	}
	if match == nil {
		return map[string]any{
			"suppressed": false,
			"channel":    channel,
			"address":    addr,
		}, nil
	}
	return map[string]any{
		"suppressed":    true,
		"reason":        match.Reason,
		"source":        match.Source,
		"channel":       channel,
		"address":       addr,
		"kind":          match.Kind,
		"matched":       match.Address,
		"suppressed_at": match.FirstSeen,
	}, nil
}

func (a *App) toolSuppressionRemove(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	channel, kind, value, err := normaliseSuppressionTarget(strArg(args, "channel"), strArg(args, "kind"), strArg(args, "address"))
	if err != nil {
		return nil, err
	}
	_, err = ctx.AppDB().Exec(
		`DELETE FROM suppressions WHERE project_id = ? AND channel = ? AND kind = ? AND address = ?`,
		pid, channel, kind, value,
	)
	if err != nil {
		return nil, err
	}
	return map[string]any{"removed": true, "address": value, "channel": channel, "kind": kind}, nil
}

func normaliseSuppressionTarget(channel, kind, raw string) (string, string, string, error) {
	raw = strings.TrimSpace(stripScheme(raw))
	if raw == "" {
		return "", "", "", errors.New("address required")
	}
	kind = strings.ToLower(strings.TrimSpace(kind))
	channel = strings.ToLower(strings.TrimSpace(channel))
	if kind == "" {
		if !strings.Contains(raw, "@") && !looksLikeE164(raw) && looksLikeDomain(raw) {
			kind = "domain"
		} else {
			kind = "address"
		}
	}
	switch kind {
	case "address":
		if channel == "" {
			channel = guessChannelFromAddress(raw)
		}
		if !validChannel(channel) {
			return "", "", "", errors.New("channel: required for address suppression (one of email, sms, whatsapp)")
		}
		addr, err := normaliseAddress(channel, raw)
		if err != nil {
			return "", "", "", err
		}
		return channel, kind, addr, nil
	case "domain":
		if channel == "" {
			channel = channelEmail
		}
		if channel != channelEmail {
			return "", "", "", errors.New("domain suppression is only supported for email")
		}
		d, err := normaliseSuppressionDomain(raw)
		if err != nil {
			return "", "", "", err
		}
		return channel, kind, d, nil
	default:
		return "", "", "", fmt.Errorf("kind: unsupported value %q (address|domain)", kind)
	}
}

func normaliseSuppressionDomain(raw string) (string, error) {
	raw = strings.ToLower(strings.TrimSpace(stripScheme(strings.TrimPrefix(raw, "@"))))
	if !looksLikeDomain(raw) {
		return "", fmt.Errorf("invalid domain %q", raw)
	}
	return raw, nil
}

func looksLikeDomain(s string) bool {
	s = strings.ToLower(strings.TrimSpace(strings.TrimPrefix(s, "@")))
	if strings.IndexByte(s, '.') < 0 || strings.ContainsAny(s, " /\\\t\r\n") {
		return false
	}
	if strings.Contains(s, "@") || strings.HasPrefix(s, ".") || strings.HasSuffix(s, ".") {
		return false
	}
	return true
}

func emailDomain(addr string) string {
	i := strings.LastIndexByte(addr, '@')
	if i < 0 || i == len(addr)-1 {
		return ""
	}
	return strings.ToLower(strings.TrimSpace(addr[i+1:]))
}

func isCommonSuppressionDomain(domain string) bool {
	switch strings.ToLower(strings.TrimSpace(domain)) {
	case "gmail.com", "googlemail.com", "outlook.com", "hotmail.com", "live.com", "msn.com",
		"icloud.com", "me.com", "mac.com", "yahoo.com", "ymail.com", "aol.com",
		"proton.me", "protonmail.com", "pm.me", "fastmail.com":
		return true
	}
	return false
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
	Channel    string   `json:"channel"` // "email" | "sms" | "whatsapp"
	Address    string   `json:"address"` // plain (alice@x.com or +15551234567)
	Kind       string   `json:"kind"`    // "email" | "domain" | "phone"
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
	if channel == "email" {
		if kind, raw, err := classifyEmailIdentity(addr); err == nil && kind == "domain" {
			_ = a.refreshOneSESIdentity(ctx, pid, raw)
			if ident, _ := dbFindIdentity(ctx.AppDB(), pid, "email_domain", raw); ident != nil {
				return identityRowToMap(ident), nil
			}
		}
	}
	local, _ := dbFindSender(ctx.AppDB(), pid, channel, addr)
	// Probe the provider for the freshest state — best-effort. If
	// the probe fails we still return the local row.
	if channel == "email" {
		if local == nil || local.Kind != "email_mailbox" || local.ParentIdentityID == nil {
			_ = a.refreshOneSESIdentity(ctx, pid, addr)
		}
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
		IdentityType             string `json:"IdentityType"`
		VerifiedForSendingStatus bool   `json:"VerifiedForSendingStatus"`
		DkimAttributes           struct {
			Status         string   `json:"Status"`
			Tokens         []string `json:"Tokens"`
			SigningEnabled bool     `json:"SigningEnabled"`
		} `json:"DkimAttributes"`
		FeedbackForwardingStatus bool `json:"FeedbackForwardingStatus"`
		MailFromAttributes       struct {
			MailFromDomain       string `json:"MailFromDomain"`
			MailFromDomainStatus string `json:"MailFromDomainStatus"`
			BehaviorOnMxFailure  string `json:"BehaviorOnMxFailure"`
		} `json:"MailFromAttributes"`
	}
	_ = json.Unmarshal(res.Data, &inner)
	dkimStatus := inner.DkimAttributes.Status
	verifiedStatus := domainVerificationStatus(dkimStatus)
	metadata := ""
	if inner.MailFromAttributes.MailFromDomain != "" || inner.MailFromAttributes.MailFromDomainStatus != "" {
		if b, err := json.Marshal(map[string]any{
			"mail_from_domain":          inner.MailFromAttributes.MailFromDomain,
			"mail_from_domain_status":   inner.MailFromAttributes.MailFromDomainStatus,
			"mail_from_mx_failure_mode": inner.MailFromAttributes.BehaviorOnMxFailure,
		}); err == nil {
			metadata = string(b)
		}
	}
	// Route to the right table by what SES says this identity is.
	if inner.IdentityType == "DOMAIN" || inner.IdentityType == "MANAGED_DOMAIN" {
		_, err = dbUpsertIdentity(ctx.AppDB(), &identityUpsert{
			ProjectID:          pid,
			Kind:               "email_domain",
			Address:            raw,
			Provider:           "aws-ses",
			ProviderIdentityID: raw,
			Verified:           inner.VerifiedForSendingStatus,
			VerificationStatus: verifiedStatus,
			DkimStatus:         dkimStatus,
			Metadata:           metadata,
			MarkSyncedNow:      true,
		})
		return err
	}
	_, err = dbUpsertSender(ctx.AppDB(), &senderUpsert{
		ProjectID:          pid,
		Channel:            "email",
		Address:            raw,
		Kind:               "email_mailbox",
		Provider:           "aws-ses",
		ProviderIdentityID: raw,
		Verified:           inner.VerifiedForSendingStatus,
		VerificationStatus: verifiedStatus,
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

// looksLikeIdentityNotFound classifies a delete_identity failure as a
// no-op "already gone" rather than a real error. Used to make
// senders_delete idempotent across inheritance mailboxes + identities
// the operator removed in the AWS console.
func looksLikeIdentityNotFound(res *sdk.ExecuteResult, body string) bool {
	if res != nil && res.Status == 404 {
		return true
	}
	return strings.Contains(strings.ToLower(body), "does not exist")
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
		// Inheritance mailboxes weren't created as SES identities —
		// v0.11's inheritance flow registers them locally only and
		// relies on the parent's DKIM. v0.12 makes that inheritance
		// explicit via senders.parent_identity_id, so the skip check
		// is now a single FK existence test instead of a string-
		// suffix walk.
		skipUpstream := false
		if local != nil && local.Kind == "email_mailbox" && local.ParentIdentityID != nil {
			if p, _ := dbGetIdentity(ctx.AppDB(), *local.ParentIdentityID); p != nil && p.Verified && p.DeletedAt == nil {
				skipUpstream = true
			}
		}
		if !skipUpstream {
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
				// Treat "identity does not exist" as idempotent
				// success — the upstream state already matches what
				// the operator wants (covers inheritance mailboxes
				// without a local parent row, plus identities the
				// operator already removed via the AWS console).
				if !looksLikeIdentityNotFound(res, body) {
					return nil, fmt.Errorf("provider non-2xx: %s", truncate(body, 400))
				}
			}
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

func defaultDMARCRecord(domain string) string {
	return "v=DMARC1; p=none; rua=mailto:dmarc@" + domain + "; adkim=s; aspf=r"
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
			Max24HourSend   float64 `json:"Max24HourSend"`
			MaxSendRate     float64 `json:"MaxSendRate"`
			SentLast24Hours float64 `json:"SentLast24Hours"`
		} `json:"SendQuota"`
		SendingEnabled          bool   `json:"SendingEnabled"`
		ProductionAccessEnabled bool   `json:"ProductionAccessEnabled"`
		EnforcementStatus       string `json:"EnforcementStatus"`
	}
	_ = json.Unmarshal(res.Data, &inner)
	return map[string]any{
		"sandboxed":            !inner.ProductionAccessEnabled,
		"sending_enabled":      inner.SendingEnabled,
		"production_access":    inner.ProductionAccessEnabled,
		"enforcement_status":   inner.EnforcementStatus,
		"send_quota_24h":       inner.SendQuota.Max24HourSend,
		"send_rate_per_second": inner.SendQuota.MaxSendRate,
		"sent_last_24h":        inner.SendQuota.SentLast24Hours,
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

	events, err := parseSESProviderEvents(env.Message)
	if err != nil {
		httpErr(w, http.StatusBadRequest, "ses notification: "+err.Error())
		return
	}
	if len(events) == 0 {
		httpJSON(w, map[string]any{"ok": true, "matched": false, "skipped": "no SES event"})
		return
	}
	pid, _ := resolveProjectFromRequest(r)
	if pid == "" {
		// Webhook came in without a project query param — fall back to
		// looking up the message across all projects via provider id.
		pid = ""
	}
	msg, err := dbMessageByProviderID(globalCtx.AppDB(), pid, events[0].ProviderMessageID)
	if err != nil || msg == nil {
		// Unknown SES message — store the event with a NULL
		// message_id-attached row would violate FK, so we just log.
		globalCtx.Logger().Warn("webhook: unknown provider message id",
			"provider_message_id", events[0].ProviderMessageID,
			"kind", events[0].Kind)
		httpJSON(w, map[string]any{"ok": true, "matched": false})
		return
	}

	for _, ev := range events {
		persistAndEmitProviderEvent(globalCtx, msg, ev)
	}
	httpJSON(w, map[string]any{
		"ok": true, "matched": true, "message_id": msg.ID,
		"events": len(events), "kind": events[0].Kind,
	})
}

func mapSESKindToStatus(kind string) string {
	switch kind {
	case "sent":
		return "sent"
	case "delivery_delayed":
		return "delivery_delayed"
	case "delivered":
		return "delivered"
	case "opened":
		return "opened"
	case "clicked":
		return "clicked"
	case "bounced":
		return "bounced"
	case "complained":
		return "complained"
	case "failed", "undelivered", "rejected", "rendering_failed":
		return "failed"
	}
	return ""
}

func messageStatusRank(status string) int {
	switch status {
	case "sent":
		return 10
	case "delivery_delayed":
		return 15
	case "delivered":
		return 20
	case "opened":
		return 30
	case "clicked":
		return 40
	case "failed", "bounced", "complained":
		return 100
	default:
		return 0
	}
}

func shouldPromoteMessageStatus(current, next string) bool {
	if next == "" {
		return false
	}
	return messageStatusRank(next) >= messageStatusRank(current)
}

func effectiveMessageStatus(current string, counts map[string]int) string {
	status := current
	for kind, count := range counts {
		if count <= 0 {
			continue
		}
		next := mapSESKindToStatus(kind)
		if shouldPromoteMessageStatus(status, next) {
			status = next
		}
	}
	return status
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

	// Parse SES payload BEFORE resolving project — global-scope
	// installs can't safely stamp project_id into the SNS subscription
	// URL (one topic per install, but potentially many projects
	// sharing it), so v0.12.6 falls back to deriving project_id from
	// the addressed-to domain via the local identities table. The
	// recipient list lives inside the SES payload, which means we
	// have to parse first.
	parsed, sesEnv, err := parseSESInboundContent(env.Message)
	if err != nil {
		httpErr(w, http.StatusBadRequest, "ses inbound: "+err.Error())
		return
	}

	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		pid = resolveProjectFromInboundEmail(globalCtx, parsed, sesEnv)
		if pid == "" {
			httpErr(w, http.StatusBadRequest, "project_id required: not in URL and could not derive from recipients")
			return
		}
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
	emitMessagingEvent(globalCtx, pid, "message.received", map[string]any{
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

	// v0.12.6: Twilio SmsUrls historically didn't stamp project_id
	// (global-scope installs can't safely pick one — multiple
	// projects share the install). Derive project from the
	// destination phone number via the senders table when the URL
	// lacks it.
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		pid = resolveProjectFromInboundPhone(globalCtx, channel, to)
		if pid == "" {
			httpErr(w, http.StatusBadRequest, "project_id required: not in URL and no sender row found for To="+to)
			return
		}
	}

	toJSON, _ := json.Marshal([]string{to})
	hdrs := map[string]string{
		"X-Twilio-Message-Sid":      messageSid,
		"X-Twilio-Account-Sid":      form.Get("AccountSid"),
		"X-Twilio-MessagingService": form.Get("MessagingServiceSid"),
		"X-Twilio-NumMedia":         form.Get("NumMedia"),
		"X-Twilio-FromCountry":      form.Get("FromCountry"),
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
	emitMessagingEvent(globalCtx, pid, "message.received", map[string]any{
		"id":      id,
		"channel": channel,
		"from":    from,
	})
	// Twilio expects a 2xx within 15s or it retries. Empty TwiML body
	// tells Twilio "I handled it; no auto-reply please."
	w.Header().Set("Content-Type", "text/xml")
	w.Write([]byte(`<?xml version="1.0" encoding="UTF-8"?><Response/>`))
}

func (a *App) handleTwilioStatusWebhook(w http.ResponseWriter, r *http.Request) {
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
		httpErr(w, http.StatusServiceUnavailable, "no phone_provider bound")
		return
	}
	conn, err := globalCtx.PlatformAPI().GetConnection(bound.ConnectionID)
	if err != nil || conn == nil {
		httpErr(w, http.StatusServiceUnavailable, "lookup phone_provider connection: "+errString(err))
		return
	}
	signedURL := reconstructPublicURL(r)
	authToken := lookupConnectionCredential(globalCtx, bound.ConnectionID, "auth_token")
	if authToken == "" {
		globalCtx.Logger().Warn("twilio status: auth_token not retrievable, signature NOT verified", "url", signedURL)
	} else if !verifyTwilioSignature(signedURL, form, authToken, r.Header.Get("X-Twilio-Signature")) {
		httpErr(w, http.StatusForbidden, "twilio signature failed")
		return
	}

	messageSID := strings.TrimSpace(form.Get("MessageSid"))
	if messageSID == "" {
		messageSID = strings.TrimSpace(form.Get("SmsSid"))
	}
	if messageSID == "" {
		httpErr(w, http.StatusBadRequest, "MessageSid required")
		return
	}
	status := strings.ToLower(strings.TrimSpace(form.Get("MessageStatus")))
	if status == "" {
		status = strings.ToLower(strings.TrimSpace(form.Get("SmsStatus")))
	}
	kind := mapTwilioMessageStatusToEventKind(status)
	if kind == "" {
		httpJSON(w, map[string]any{"ok": true, "matched": false, "skipped": "unknown Twilio status"})
		return
	}

	pid, _ := resolveProjectFromRequest(r)
	msg, err := dbMessageByProviderID(globalCtx.AppDB(), pid, messageSID)
	if err != nil || msg == nil {
		globalCtx.Logger().Warn("twilio status: unknown provider message id",
			"provider_message_id", messageSID, "status", status)
		httpJSON(w, map[string]any{"ok": true, "matched": false})
		return
	}
	raw, _ := json.Marshal(form)
	reason := strings.TrimSpace(status)
	if code := strings.TrimSpace(form.Get("ErrorCode")); code != "" {
		reason = strings.TrimSpace(reason + " error_code=" + code)
	}
	if msgText := strings.TrimSpace(form.Get("ErrorMessage")); msgText != "" {
		reason = strings.TrimSpace(reason + " " + msgText)
	}
	ev := providerEvent{
		Provider:          "twilio",
		ProviderMessageID: messageSID,
		Kind:              kind,
		Recipient:         strings.TrimSpace(stripScheme(form.Get("To"))),
		Reason:            reason,
		Raw:               raw,
		OccurredAt:        time.Now().UTC().Format(time.RFC3339),
		Metadata: map[string]any{
			"twilio_status": status,
			"error_code":    strings.TrimSpace(form.Get("ErrorCode")),
			"error_message": strings.TrimSpace(form.Get("ErrorMessage")),
		},
	}
	persistAndEmitProviderEvent(globalCtx, msg, ev)
	httpJSON(w, map[string]any{"ok": true, "matched": true, "message_id": msg.ID, "kind": kind})
}

func mapTwilioMessageStatusToEventKind(status string) string {
	switch strings.ToLower(strings.TrimSpace(status)) {
	case "accepted", "queued", "scheduled", "sending", "sent":
		return "sent"
	case "delivered":
		return "delivered"
	case "read":
		return "opened"
	case "failed", "undelivered", "canceled":
		return "failed"
	default:
		return ""
	}
}

// verifyTwilioSignature checks the X-Twilio-Signature header per
// Twilio's documented algorithm:
//  1. Concatenate fullURL (including query) with sorted form params
//     written as KEY1VALUE1KEY2VALUE2... (no separators).
//  2. HMAC-SHA1 with authToken as key.
//  3. Base64-encode.
//
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
	if globalCtx != nil {
		if id, err := globalCtx.PlatformAPI().WhoAmI(); err == nil && id != nil && strings.TrimSpace(id.PublicURL) != "" {
			path := r.URL.RequestURI()
			if !strings.HasPrefix(path, "/api/apps/") {
				path = "/api/apps/messaging" + path
			}
			return strings.TrimRight(strings.TrimSpace(id.PublicURL), "/") + path
		}
	}
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

// lookupConnectionCredential retrieves a single field from a bound
// integration connection. Server-side gates enforce that the manifest
// declared platform.connections.read_credentials, the connection is
// actually bound to this install, and its slug is compatible with the
// declared role. The local config value remains as a fallback for
// older platform builds or emergency override.
func lookupConnectionCredential(ctx *sdk.AppCtx, connID int64, field string) string {
	if ctx != nil && connID > 0 {
		if creds, err := ctx.PlatformAPI().GetConnectionCredentials(connID); err == nil && creds != nil {
			if v := strings.TrimSpace(creds.Fields[field]); v != "" {
				return v
			}
		}
	}
	if ctx != nil && field == "auth_token" {
		if v := strings.TrimSpace(ctx.Config().Get("twilio_auth_token")); v != "" {
			return v
		}
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

const crmInboundReceiveTool = "messaging_inbound_receive"

// dispatchInbound looks up the matching inbound_route for each
// recipient on the message and calls the target app tool with the
// normalised JSON. First match wins per message; ties go to priority
// DESC, then longest-pattern.
func dispatchInbound(ctx *sdk.AppCtx, pid string, m *Message) error {
	if m == nil {
		return errors.New("nil message")
	}
	sender := canonicalAddrForChannel(m.Channel, m.From)
	if sender != "" {
		match, err := dbSuppressionMatch(ctx.AppDB(), pid, m.Channel, sender)
		if err != nil {
			return err
		}
		if match != nil {
			now := time.Now().UTC().Format(time.RFC3339)
			reason := fmt.Sprintf("suppressed by %s %s", match.Kind, match.Address)
			_, _ = ctx.AppDB().Exec(
				`UPDATE messages
				 SET route_status='suppressed', route_error = ?, route_attempts = route_attempts + 1, last_event_at = ?
				 WHERE id = ?`,
				reason, now, m.ID,
			)
			emitMessagingEvent(ctx, pid, "message.suppressed", map[string]any{
				"id":      m.ID,
				"channel": m.Channel,
				"from":    sender,
				"kind":    match.Kind,
				"matched": match.Address,
				"reason":  match.Reason,
			})
			return nil
		}
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
		"message_id":        m.ID,
		"channel":           m.Channel,
		"matched_recipient": winner.recipient,
		"matched_pattern":   winner.route.Pattern,
		"to_subaddress":     winner.subaddr,
		"from":              m.From,
		"to":                m.To,
		"cc":                m.CC,
		"subject":           m.Subject,
		"body_text":         m.BodyText,
		"body_html":         m.BodyHTML,
		"message_id_header": m.MessageIDHeader,
		"in_reply_to":       m.InReplyTo,
		"references":        m.References,
		"headers":           hdr,
		"received_at":       m.ReceivedAt,
	}

	targetTool := inboundRouteTargetTool(winner.route.TargetApp, winner.route.TargetRoute)
	callCtx := ctx
	if strings.TrimSpace(pid) != "" {
		callCtx = ctx.WithProject(pid)
	}
	_, callErr := callCtx.PlatformAPI().CallApp(winner.route.TargetApp, targetTool, payload)
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

func inboundRouteTargetTool(appName, route string) string {
	route = strings.TrimSpace(route)
	if strings.EqualFold(strings.TrimSpace(appName), "crm") {
		switch route {
		case "", "/", "/inbound", "inbound", crmInboundReceiveTool:
			return crmInboundReceiveTool
		}
	}
	return normaliseRoutePath(route)
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
	if pattern == "*" {
		return true, ""
	}
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

func parseSESProviderEvents(message string) ([]providerEvent, error) {
	var probe struct {
		EventType        string `json:"eventType"`
		NotificationType string `json:"notificationType"`
	}
	if err := json.Unmarshal([]byte(message), &probe); err != nil {
		return nil, err
	}
	if probe.EventType != "" {
		return parseSESEventPublishing(message)
	}
	notif, err := parseSESNotification(message)
	if err != nil {
		return nil, err
	}
	if notif.Kind == "" || notif.MessageID == "" {
		return nil, nil
	}
	out := make([]providerEvent, 0, len(notif.Recipients))
	if len(notif.Recipients) == 0 {
		out = append(out, providerEvent{
			Provider: "aws-ses", ProviderMessageID: notif.MessageID,
			Kind: notif.Kind, Raw: notif.Raw,
		})
		return out, nil
	}
	for _, r := range notif.Recipients {
		out = append(out, providerEvent{
			Provider: "aws-ses", ProviderMessageID: notif.MessageID,
			Kind: notif.Kind, Recipient: r.Address, Reason: r.Reason,
			Raw: notif.Raw, Permanent: r.Permanent,
		})
	}
	return out, nil
}

func parseSESEventPublishing(message string) ([]providerEvent, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal([]byte(message), &raw); err != nil {
		return nil, err
	}
	rawEvent := json.RawMessage(message)
	var env struct {
		EventType string `json:"eventType"`
		Mail      struct {
			MessageID   string              `json:"messageId"`
			Timestamp   string              `json:"timestamp"`
			Destination []string            `json:"destination"`
			Tags        map[string][]string `json:"tags"`
		} `json:"mail"`
	}
	_ = json.Unmarshal([]byte(message), &env)
	kind := sesEventTypeToKind(env.EventType)
	if kind == "" || env.Mail.MessageID == "" {
		return nil, nil
	}
	occurred := eventTimestamp(raw, strings.ToLower(env.EventType), env.Mail.Timestamp)
	recipients := eventRecipients(raw, strings.ToLower(env.EventType), env.Mail.Destination)
	reason := eventReason(raw, strings.ToLower(env.EventType))
	metadata := eventMetadata(raw, strings.ToLower(env.EventType), env.Mail.Tags)
	if len(recipients) == 0 {
		recipients = []string{""}
	}
	out := make([]providerEvent, 0, len(recipients))
	for _, recip := range recipients {
		out = append(out, providerEvent{
			Provider: "aws-ses", ProviderMessageID: env.Mail.MessageID,
			Kind: kind, Recipient: recip, Reason: reason, OccurredAt: occurred,
			Metadata: metadata, Raw: rawEvent,
			Permanent: kind == "complained" || (kind == "bounced" && isPermanentBounce(raw)),
		})
	}
	return out, nil
}

func sesEventTypeToKind(t string) string {
	switch strings.ToUpper(strings.TrimSpace(t)) {
	case "SEND":
		return "sent"
	case "DELIVERY":
		return "delivered"
	case "BOUNCE":
		return "bounced"
	case "COMPLAINT":
		return "complained"
	case "REJECT":
		return "rejected"
	case "OPEN":
		return "opened"
	case "CLICK":
		return "clicked"
	case "RENDERING_FAILURE":
		return "rendering_failed"
	case "DELIVERY_DELAY":
		return "delivery_delayed"
	case "SUBSCRIPTION":
		return "subscription_changed"
	}
	return ""
}

func eventTimestamp(raw map[string]json.RawMessage, section, fallback string) string {
	if body, ok := raw[section]; ok {
		var x struct {
			Timestamp string `json:"timestamp"`
		}
		_ = json.Unmarshal(body, &x)
		if x.Timestamp != "" {
			return x.Timestamp
		}
	}
	return fallback
}

func eventRecipients(raw map[string]json.RawMessage, section string, fallback []string) []string {
	if body, ok := raw[section]; ok {
		var x struct {
			Recipients        []string `json:"recipients"`
			Recipient         string   `json:"recipient"`
			BouncedRecipients []struct {
				EmailAddress string `json:"emailAddress"`
			} `json:"bouncedRecipients"`
			ComplainedRecipients []struct {
				EmailAddress string `json:"emailAddress"`
			} `json:"complainedRecipients"`
		}
		_ = json.Unmarshal(body, &x)
		out := append([]string{}, x.Recipients...)
		if x.Recipient != "" {
			out = append(out, x.Recipient)
		}
		for _, r := range x.BouncedRecipients {
			if r.EmailAddress != "" {
				out = append(out, r.EmailAddress)
			}
		}
		for _, r := range x.ComplainedRecipients {
			if r.EmailAddress != "" {
				out = append(out, r.EmailAddress)
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return append([]string{}, fallback...)
}

func eventReason(raw map[string]json.RawMessage, section string) string {
	if body, ok := raw[section]; ok {
		var x struct {
			BounceType            string `json:"bounceType"`
			BounceSubType         string `json:"bounceSubType"`
			ComplaintFeedbackType string `json:"complaintFeedbackType"`
			Reason                string `json:"reason"`
			FailureReason         string `json:"failureReason"`
			DelayType             string `json:"delayType"`
			DiagnosticCode        string `json:"diagnosticCode"`
			BouncedRecipients     []struct {
				DiagnosticCode string `json:"diagnosticCode"`
			} `json:"bouncedRecipients"`
		}
		_ = json.Unmarshal(body, &x)
		parts := []string{}
		for _, p := range []string{x.BounceType, x.BounceSubType, x.ComplaintFeedbackType, x.DelayType, x.Reason, x.FailureReason, x.DiagnosticCode} {
			if p != "" {
				parts = append(parts, p)
			}
		}
		for _, r := range x.BouncedRecipients {
			if r.DiagnosticCode != "" {
				parts = append(parts, r.DiagnosticCode)
				break
			}
		}
		return strings.Join(parts, ": ")
	}
	return ""
}

func eventMetadata(raw map[string]json.RawMessage, section string, tags map[string][]string) map[string]any {
	out := map[string]any{}
	if len(tags) > 0 {
		out["tags"] = tags
	}
	if body, ok := raw[section]; ok {
		var x map[string]any
		_ = json.Unmarshal(body, &x)
		for _, k := range []string{"ipAddress", "userAgent", "link", "url", "timestamp", "delayType", "expirationTime", "subscriptionTopic"} {
			if v, ok := x[k]; ok {
				out[k] = v
			}
		}
	}
	return out
}

func isPermanentBounce(raw map[string]json.RawMessage) bool {
	if body, ok := raw["bounce"]; ok {
		var x struct {
			BounceType string `json:"bounceType"`
		}
		_ = json.Unmarshal(body, &x)
		return strings.EqualFold(x.BounceType, "Permanent")
	}
	return false
}

func persistAndEmitProviderEvent(ctx *sdk.AppCtx, msg *Message, ev providerEvent) {
	if ctx == nil || msg == nil {
		return
	}
	raw := ev.Raw
	if len(ev.Metadata) > 0 {
		enriched := map[string]any{"raw": json.RawMessage(ev.Raw), "metadata": ev.Metadata}
		if b, err := json.Marshal(enriched); err == nil {
			raw = b
		}
	}
	occurred := ev.OccurredAt
	if occurred == "" {
		occurred = time.Now().UTC().Format(time.RFC3339)
	}
	_, _ = ctx.AppDB().Exec(
		`INSERT INTO delivery_events (message_id, kind, recipient, reason, raw, occurred_at)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		msg.ID, ev.Kind, ev.Recipient, ev.Reason, string(raw), occurred,
	)
	if (ev.Kind == "bounced" && ev.Permanent) || ev.Kind == "complained" {
		suppressionReason := "hard-bounce"
		if ev.Kind == "complained" {
			suppressionReason = "complaint"
		}
		canonical := canonicalAddrForChannel(msg.Channel, ev.Recipient)
		_ = dbSuppressionUpsert(ctx.AppDB(), msg.ProjectID, msg.Channel, canonical, suppressionReason, "auto")
	}
	if status := mapSESKindToStatus(ev.Kind); status != "" && shouldPromoteMessageStatus(msg.Status, status) {
		_, _ = ctx.AppDB().Exec(
			`UPDATE messages SET status = ?, last_event_at = ? WHERE id = ?`,
			status, occurred, msg.ID,
		)
		msg.Status = status
	} else {
		_, _ = ctx.AppDB().Exec(`UPDATE messages SET last_event_at = ? WHERE id = ?`, occurred, msg.ID)
	}
	payload := map[string]any{
		"message_id":          msg.ID,
		"provider":            ev.Provider,
		"provider_message_id": ev.ProviderMessageID,
		"kind":                ev.Kind,
		"recipient":           ev.Recipient,
		"occurred_at":         occurred,
		"metadata":            ev.Metadata,
	}
	if ev.Reason != "" {
		payload["reason"] = ev.Reason
	}
	emitMessagingEvent(ctx, msg.ProjectID, "message."+ev.Kind, payload)
	emitMessagingEvent(ctx, msg.ProjectID, "message.event", payload)
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
		MessageID string                         `json:"messageId"`
		Headers   []struct{ Name, Value string } `json:"headers"`
	} `json:"mail"`
	Receipt struct {
		// Recipients lists the addressed-to mailboxes the SES receipt
		// rule matched on. Used by the inbound webhook as the v0.12.6
		// fallback for resolving project_id when the SNS subscription
		// URL doesn't carry one (global-scope installs).
		Recipients   []string                `json:"recipients"`
		SpamVerdict  struct{ Status string } `json:"spamVerdict"`
		VirusVerdict struct{ Status string } `json:"virusVerdict"`
		SPFVerdict   struct{ Status string } `json:"spfVerdict"`
		DKIMVerdict  struct{ Status string } `json:"dkimVerdict"`
		DMARCVerdict struct{ Status string } `json:"dmarcVerdict"`
		Action       struct {
			Type       string `json:"type"`
			BucketName string `json:"bucketName"` // populated for S3 action
			ObjectKey  string `json:"objectKey"`  // populated for S3 action
		} `json:"action"`
	} `json:"receipt"`
}

// resolveProjectFromInboundEmail derives the owning project_id from
// the recipient list when the SNS subscription URL didn't carry one
// (global-scope installs where multiple projects can share a single
// SNS topic). Walks the candidate recipients, strips each to its
// parent domain, looks the domain up against the identities table —
// the first match wins. Returns "" when nothing matches; caller
// surfaces a clean error in that case.
//
// Order of trust:
//  1. sesEnv.Receipt.Recipients (what the SES rule matched on —
//     always present, even in S3-action mode where the .eml isn't
//     fetched yet)
//  2. parsed.To (inline-content path; equivalent in practice but
//     kept as a fallback in case the receipt.recipients block is
//     ever empty)
func resolveProjectFromInboundEmail(ctx *sdk.AppCtx, parsed *parsedInbound, sesEnv *sesInboundEnvelope) string {
	if ctx == nil {
		return ""
	}
	candidates := []string{}
	if sesEnv != nil {
		candidates = append(candidates, sesEnv.Receipt.Recipients...)
	}
	if parsed != nil {
		candidates = append(candidates, parsed.To...)
	}
	seen := map[string]bool{}
	for _, addr := range candidates {
		clean := normaliseEmailFromHeader(addr)
		if clean == "" {
			clean = strings.TrimSpace(strings.ToLower(addr))
		}
		domain := parentDomainOf(clean)
		if domain == "" || seen[domain] {
			continue
		}
		seen[domain] = true
		if id, _ := dbFindIdentityByAddress(ctx.AppDB(), "email_domain", domain); id != nil {
			return id.ProjectID
		}
	}
	return ""
}

// resolveProjectFromInboundPhone is the Twilio analogue of
// resolveProjectFromInboundEmail. Looks up the destination phone
// number against the senders table to find its project. WhatsApp
// numbers carry the "whatsapp:" prefix on the To field; both phone
// and whatsapp_number sender kinds are stored without it (the
// channel column captures the distinction), so we strip the prefix
// before matching.
func resolveProjectFromInboundPhone(ctx *sdk.AppCtx, channel, to string) string {
	if ctx == nil || to == "" {
		return ""
	}
	addr := strings.TrimSpace(to)
	if strings.HasPrefix(strings.ToLower(addr), "whatsapp:") {
		addr = strings.TrimSpace(addr[len("whatsapp:"):])
	}
	if s, _ := dbFindSenderByAddress(ctx.AppDB(), channel, addr); s != nil {
		return s.ProjectID
	}
	// Channel may have been mis-detected (e.g., Twilio routed a WA
	// number through the SMS handler). Try the other channel as a
	// last resort.
	otherChannel := "sms"
	if channel == "sms" {
		otherChannel = "whatsapp"
	}
	if s, _ := dbFindSenderByAddress(ctx.AppDB(), otherChannel, addr); s != nil {
		return s.ProjectID
	}
	return ""
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
	bodyText, bodyHTML := extractBodies(msg.Header.Get("Content-Type"), msg.Header.Get("Content-Transfer-Encoding"), body)

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
// pulling the first text/plain and text/html parts. Decodes
// Content-Transfer-Encoding (base64, quoted-printable) on each part —
// pre-v0.12.7 it stored the raw transfer-encoded bytes, which made
// Proton mail (always base64-encodes) render as opaque blobs in the
// inbox.
//
// Single-part path takes the top-level Content-Transfer-Encoding;
// multipart path reads each part's own header (top-level is
// "multipart/mixed" with no real encoding, parts carry their own).
//
// Best-effort: nested multiparts beyond one level fall through;
// charset decoding (Content-Type: text/plain; charset=ISO-8859-1)
// isn't done here — a separate quality issue.
func extractBodies(contentType, topEncoding string, body []byte) (text string, html string) {
	ct := strings.ToLower(contentType)
	switch {
	case strings.HasPrefix(ct, "text/plain"):
		return decodeTransferEncoding(topEncoding, string(body)), ""
	case strings.HasPrefix(ct, "text/html"):
		return "", decodeTransferEncoding(topEncoding, string(body))
	}
	// Naive multipart split — finds boundary= and walks parts. For
	// production we'd swap to mime/multipart but keeping the import
	// surface tiny here.
	if !strings.HasPrefix(ct, "multipart/") {
		return decodeTransferEncoding(topEncoding, string(body)), ""
	}
	boundary := boundaryFromContentType(contentType)
	if boundary == "" {
		return decodeTransferEncoding(topEncoding, string(body)), ""
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
		partEncoding := headerValueFromBlock(head, "content-transfer-encoding")
		switch {
		case strings.Contains(head, "content-type: text/plain") && text == "":
			text = decodeTransferEncoding(partEncoding, bodyPart)
		case strings.Contains(head, "content-type: text/html") && html == "":
			html = decodeTransferEncoding(partEncoding, bodyPart)
		}
	}
	return text, html
}

// decodeTransferEncoding turns transfer-encoded body bytes into the
// readable string. 7bit / 8bit / binary / empty / unknown values pass
// through unchanged; base64 and quoted-printable are the two encodings
// that make text unreadable when stored raw, and both are stdlib.
//
// Decode failure falls back to the raw input — preserves the data so
// the operator can still inspect it, rather than dropping the body.
func decodeTransferEncoding(encoding, raw string) string {
	switch strings.ToLower(strings.TrimSpace(encoding)) {
	case "base64":
		// Mail clients fold base64 with CRLFs; the std decoder
		// tolerates whitespace inside the input but explicit strip is
		// cheap insurance against rarer variants (tabs, NBSP).
		cleaned := strings.NewReplacer("\r", "", "\n", "", " ", "", "\t", "").Replace(raw)
		if cleaned == "" {
			return raw
		}
		decoded, err := base64.StdEncoding.DecodeString(cleaned)
		if err != nil {
			return raw
		}
		return string(decoded)
	case "quoted-printable":
		decoded, err := io.ReadAll(quotedprintable.NewReader(strings.NewReader(raw)))
		if err != nil {
			return raw
		}
		return string(decoded)
	}
	return raw
}

// headerValueFromBlock pulls a header value out of a lowercase header
// block already split off from a MIME part body. Used in extractBodies
// alongside the existing Contains-based content-type sniffing — cheap
// and avoids reaching for net/textproto for a one-off read.
func headerValueFromBlock(headBlock, lowercaseName string) string {
	prefix := lowercaseName + ":"
	for _, line := range strings.Split(headBlock, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		return strings.TrimSpace(line[len(prefix):])
	}
	return ""
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
	offset, _ := strconv.Atoi(q.Get("offset"))
	if offset < 0 {
		offset = 0
	}
	out, total, err := dbMessageListPage(globalCtx.AppDB(), pid, messageListOpts{
		Direction: q.Get("direction"),
		Channel:   q.Get("channel"),
		Status:    q.Get("status"),
		Since:     q.Get("since"),
		Address:   q.Get("address"),
		Limit:     limit,
		Offset:    offset,
	})
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{
		"messages": out,
		"count":    len(out),
		"total":    total,
		"limit":    limit,
		"offset":   offset,
		"has_more": offset+len(out) < total,
	})
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

// handleSendersEdit — panel-only inline-edit affordance for local-
// mutable sender fields (display_name today; notes when surfaced).
// Doesn't fire any provider calls — pure DB write. Deliberately not
// an MCP tool: agents shouldn't be renaming senders without operator
// intent, and the operator already has senders_create for the
// "create-or-update both local+provider state" case.
func (a *App) handleSendersEdit(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body struct {
		Address     string `json:"address"`
		Channel     string `json:"channel"`
		DisplayName string `json:"display_name"`
		Notes       string `json:"notes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid JSON: "+err.Error())
		return
	}
	if body.Address == "" {
		httpErr(w, http.StatusBadRequest, "address required")
		return
	}
	if body.Channel == "" {
		body.Channel = inferChannelFromAddress(body.Address)
		if body.Channel == "" {
			body.Channel = "email"
		}
	}
	if err := dbUpdateSenderLocal(globalCtx.AppDB(), pid, body.Channel, body.Address, body.DisplayName, body.Notes); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"ok": true, "address": body.Address, "channel": body.Channel})
}

// handleIdentitiesList — thin HTTP wrapper over the identities_list
// MCP tool. Feeds the messaging panel's "Verified domains & accounts"
// section.
func (a *App) handleIdentitiesList(w http.ResponseWriter, r *http.Request) {
	args := map[string]any{}
	if pid := strings.TrimSpace(r.URL.Query().Get("project_id")); pid != "" {
		args["_project_id"] = pid
	}
	if k := strings.TrimSpace(r.URL.Query().Get("kind")); k != "" {
		args["kind"] = k
	}
	out, err := a.toolIdentitiesList(globalCtx, args)
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

type providerSenderOption struct {
	Channel    string `json:"channel"`
	Address    string `json:"address"`
	Label      string `json:"label,omitempty"`
	Kind       string `json:"kind,omitempty"`
	ProviderID string `json:"provider_id,omitempty"`
	Status     string `json:"status,omitempty"`
}

// handleSendersProviderOptions lists upstream phone senders from the
// bound phone_provider so the panel can offer a picker before calling
// the generic senders_create adoption flow. This stays HTTP-only:
// agents still manage senders through senders_create/list/get.
func (a *App) handleSendersProviderOptions(w http.ResponseWriter, r *http.Request) {
	if _, err := resolveProjectFromRequest(r); err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	bound := globalCtx.IntegrationFor("phone_provider")
	if bound == nil {
		httpJSON(w, map[string]any{"available": false, "options": []any{}, "error": "no phone_provider bound"})
		return
	}
	channel := strings.TrimSpace(r.URL.Query().Get("channel"))
	if channel != "" && channel != channelSMS && channel != channelWhatsApp {
		httpErr(w, http.StatusBadRequest, "channel must be sms or whatsapp")
		return
	}
	options := []providerSenderOption{}
	errorsOut := []string{}
	if channel == "" || channel == channelSMS {
		sms, err := listTwilioPhoneSenderOptions(globalCtx, bound.ConnectionID)
		if err != nil {
			errorsOut = append(errorsOut, err.Error())
		} else {
			options = append(options, sms...)
		}
	}
	if channel == "" || channel == channelWhatsApp {
		wa, err := listTwilioWhatsAppSenderOptions(globalCtx, bound.ConnectionID)
		if err != nil {
			errorsOut = append(errorsOut, err.Error())
		} else {
			options = append(options, wa...)
		}
	}
	out := map[string]any{"available": true, "options": options}
	if len(errorsOut) > 0 {
		out["error"] = strings.Join(errorsOut, "; ")
	}
	httpJSON(w, out)
}

func listTwilioPhoneSenderOptions(ctx *sdk.AppCtx, connID int64) ([]providerSenderOption, error) {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "list_phone_numbers", map[string]any{"PageSize": 100})
	if err != nil {
		return nil, fmt.Errorf("list_phone_numbers: %w", err)
	}
	if res == nil || !res.Success {
		return nil, fmt.Errorf("list_phone_numbers: provider non-2xx: %s", truncateResData(res))
	}
	var raw struct {
		IncomingPhoneNumbers []map[string]any `json:"incoming_phone_numbers"`
	}
	if err := json.Unmarshal(res.Data, &raw); err != nil {
		return nil, fmt.Errorf("list_phone_numbers: parse: %w", err)
	}
	out := []providerSenderOption{}
	for _, row := range raw.IncomingPhoneNumbers {
		addr := strings.TrimSpace(strFromMap(row, "phone_number", "PhoneNumber"))
		if addr == "" {
			continue
		}
		if caps, ok := row["capabilities"].(map[string]any); ok {
			if v, ok := caps["sms"].(bool); ok && !v {
				continue
			}
		}
		label := strings.TrimSpace(strFromMap(row, "friendly_name", "FriendlyName"))
		out = append(out, providerSenderOption{
			Channel:    channelSMS,
			Address:    addr,
			Label:      label,
			Kind:       "phone",
			ProviderID: strings.TrimSpace(strFromMap(row, "sid", "Sid")),
			Status:     "active",
		})
	}
	return out, nil
}

func listTwilioWhatsAppSenderOptions(ctx *sdk.AppCtx, connID int64) ([]providerSenderOption, error) {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "list_whatsapp_senders", map[string]any{"PageSize": 100})
	if err != nil {
		return nil, fmt.Errorf("list_whatsapp_senders: %w", err)
	}
	if res == nil || !res.Success {
		return nil, fmt.Errorf("list_whatsapp_senders: provider non-2xx: %s", truncateResData(res))
	}
	var raw struct {
		Senders []map[string]any `json:"senders"`
	}
	if err := json.Unmarshal(res.Data, &raw); err != nil {
		return nil, fmt.Errorf("list_whatsapp_senders: parse: %w", err)
	}
	out := []providerSenderOption{}
	for _, row := range raw.Senders {
		addr := strings.TrimSpace(strFromMap(row, "phone_number", "PhoneNumber", "address", "sender_id", "SenderId"))
		addr = strings.TrimPrefix(addr, "whatsapp:")
		if addr == "" {
			continue
		}
		label := strings.TrimSpace(strFromMap(row, "friendly_name", "FriendlyName", "name"))
		out = append(out, providerSenderOption{
			Channel:    channelWhatsApp,
			Address:    addr,
			Label:      label,
			Kind:       "whatsapp_number",
			ProviderID: strings.TrimSpace(strFromMap(row, "sid", "Sid")),
			Status:     strings.ToLower(strings.TrimSpace(strFromMap(row, "status", "Status"))),
		})
	}
	return out, nil
}

func strFromMap(m map[string]any, keys ...string) string {
	for _, k := range keys {
		if v, ok := m[k]; ok {
			switch x := v.(type) {
			case string:
				return x
			case float64, int, int64, bool:
				return fmt.Sprint(x)
			}
		}
	}
	return ""
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
	args := map[string]any{"channel": channel}
	if pid := strings.TrimSpace(r.URL.Query().Get("project_id")); pid != "" {
		args["_project_id"] = pid
	}
	out, err := a.toolTemplatesSyncProvider(globalCtx, args)
	if err != nil {
		httpErr(w, http.StatusBadGateway, err.Error())
		return
	}
	httpJSON(w, out)
}

func (a *App) handleTemplatesProviderPreview(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "GET only")
		return
	}
	channel := r.URL.Query().Get("channel")
	if channel == "" {
		channel = channelWhatsApp
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	items, err := listProviderTemplates(globalCtx, pid, channel)
	if err != nil {
		httpErr(w, http.StatusBadGateway, err.Error())
		return
	}
	httpJSON(w, map[string]any{
		"channel":   channel,
		"templates": items,
		"count":     len(items),
	})
}

func (a *App) handleTemplatesImport(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	var req struct {
		Channel        string   `json:"channel"`
		SIDs           []string `json:"sids"`
		ApprovedOnly   bool     `json:"approved_only"`
		UpdateExisting bool     `json:"update_existing"`
	}
	_ = json.NewDecoder(r.Body).Decode(&req)
	if req.Channel == "" {
		req.Channel = channelWhatsApp
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	items, err := listProviderTemplates(globalCtx, pid, req.Channel)
	if err != nil {
		httpErr(w, http.StatusBadGateway, err.Error())
		return
	}
	want := map[string]bool{}
	for _, sid := range req.SIDs {
		sid = strings.TrimSpace(sid)
		if sid != "" {
			want[sid] = true
		}
	}
	importAllApproved := len(want) == 0 && req.ApprovedOnly
	imported := 0
	updated := 0
	skipped := 0
	for _, item := range items {
		if importAllApproved {
			if item.Status != "approved" {
				skipped++
				continue
			}
		} else if !want[item.Sid] {
			skipped++
			continue
		}
		existing, err := dbTemplateGetByProviderID(globalCtx.AppDB(), pid, item.Sid)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if existing != nil && !req.UpdateExisting {
			skipped++
			continue
		}
		created, err := upsertProviderTemplate(globalCtx, pid, item)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		if created {
			imported++
		} else {
			updated++
		}
	}
	if imported > 0 || updated > 0 {
		emitMessagingEvent(globalCtx, pid, "templates.synced", map[string]any{
			"channel":  req.Channel,
			"imported": imported,
			"updated":  updated,
		})
	}
	httpJSON(w, map[string]any{
		"channel":  req.Channel,
		"imported": imported,
		"updated":  updated,
		"skipped":  skipped,
	})
}

func (a *App) handleTemplatesRefreshStatuses(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "POST only")
		return
	}
	channel := r.URL.Query().Get("channel")
	if channel == "" {
		channel = channelWhatsApp
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if channel != channelWhatsApp {
		httpJSON(w, map[string]any{
			"channel":   channel,
			"refreshed": 0,
			"skipped":   true,
			"reason":    fmt.Sprintf("no provider approval status for channel %q", channel),
		})
		return
	}
	count, err := syncProviderTemplates(globalCtx, pid, channel)
	if err != nil {
		httpErr(w, http.StatusBadGateway, err.Error())
		return
	}
	httpJSON(w, map[string]any{"channel": channel, "refreshed": count})
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
// actions are panel-only provider operations such as refresh-status
// and submit. They deliberately stay off the MCP tool surface; agents
// manage generic template rows, while the operator panel controls
// provider synchronization and approval checks.
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
		args := map[string]any{"id": id}
		if pid := strings.TrimSpace(r.URL.Query().Get("project_id")); pid != "" {
			args["_project_id"] = pid
		}
		out, err := a.toolTemplatesRefreshStatus(globalCtx, args)
		if err != nil {
			httpErr(w, http.StatusBadGateway, err.Error())
			return
		}
		httpJSON(w, out)
	case "submit":
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body == nil {
			body = map[string]any{}
		}
		body["id"] = id
		if pid := strings.TrimSpace(r.URL.Query().Get("project_id")); pid != "" {
			body["_project_id"] = pid
		}
		out, err := a.toolTemplateSubmit(globalCtx, body)
		if err != nil {
			httpErr(w, http.StatusBadGateway, err.Error())
			return
		}
		httpJSON(w, out)
	case "provider-create":
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "POST only")
			return
		}
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		if body == nil {
			body = map[string]any{}
		}
		body["id"] = id
		if pid := strings.TrimSpace(r.URL.Query().Get("project_id")); pid != "" {
			body["_project_id"] = pid
		}
		out, err := a.templateCreateProvider(globalCtx, body)
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
	if _, ok := body.Args["_project_id"]; !ok {
		if pid := strings.TrimSpace(r.URL.Query().Get("project_id")); pid != "" {
			body.Args["_project_id"] = pid
		}
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
	m, err := scanMessage(row)
	if err != nil {
		return nil, err
	}
	m.EventCounts = dbDeliveryEventCounts(db, m.ID)
	m.Status = effectiveMessageStatus(m.Status, m.EventCounts)
	return m, nil
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

func whatsAppRecipientsOutsideSession(db *sql.DB, pid, from string, recipients []string, now time.Time) []string {
	if len(recipients) == 0 {
		return nil
	}
	since := now.Add(-24 * time.Hour).Format(time.RFC3339)
	missing := []string{}
	for _, recipient := range recipients {
		recipient = strings.TrimSpace(stripScheme(recipient))
		if recipient == "" {
			continue
		}
		q := `SELECT id FROM messages
		      WHERE project_id = ?
		        AND channel = 'whatsapp'
		        AND direction = 'in'
		        AND from_addr = ?
		        AND datetime(COALESCE(NULLIF(received_at,''), created_at)) >= datetime(?)
		        AND (to_addrs LIKE ? OR matched_recipient = ?)
		      ORDER BY created_at DESC
		      LIMIT 1`
		likeFrom := `%"` + from + `"%`
		var id int64
		err := db.QueryRow(q, pid, recipient, since, likeFrom, from).Scan(&id)
		if err == sql.ErrNoRows {
			missing = append(missing, recipient)
			continue
		}
		if err != nil {
			missing = append(missing, recipient)
		}
	}
	return missing
}

type messageListOpts struct {
	Direction, Channel, Status, Since, Address string
	Limit, Offset                              int
}

func dbMessageList(db *sql.DB, pid string, opts messageListOpts) ([]*Message, error) {
	out, _, err := dbMessageListPage(db, pid, opts)
	return out, err
}

func dbMessageListPage(db *sql.DB, pid string, opts messageListOpts) ([]*Message, int, error) {
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
		where = append(where, "datetime(created_at) >= datetime(?)")
		args = append(args, opts.Since)
	}
	if opts.Address != "" {
		where = append(where, "(from_addr = ? OR to_addrs LIKE ? OR cc_addrs LIKE ?)")
		args = append(args, opts.Address, `%"`+opts.Address+`"%`, `%"`+opts.Address+`"%`)
	}
	whereSQL := strings.Join(where, " AND ")
	var total int
	if err := db.QueryRow(`SELECT COUNT(*) FROM messages WHERE `+whereSQL, args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	q := `SELECT id FROM messages WHERE ` + strings.Join(where, " AND ") +
		` ORDER BY created_at DESC, id DESC LIMIT ? OFFSET ?`
	args = append(args, opts.Limit, opts.Offset)
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, 0, err
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
	return out, total, nil
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

func dbDeliveryEventCounts(db *sql.DB, msgID int64) map[string]int {
	rows, err := db.Query(
		`SELECT kind, COUNT(*) FROM delivery_events WHERE message_id = ? GROUP BY kind`,
		msgID,
	)
	if err != nil {
		return nil
	}
	defer rows.Close()
	out := map[string]int{}
	for rows.Next() {
		var kind string
		var count int
		if err := rows.Scan(&kind, &count); err == nil && kind != "" {
			out[kind] = count
		}
	}
	if len(out) == 0 {
		return nil
	}
	return out
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

type providerTemplateInfo struct {
	Sid        string         `json:"sid"`
	Name       string         `json:"name"`
	Language   string         `json:"language,omitempty"`
	Category   string         `json:"category,omitempty"`
	Status     string         `json:"status"`
	BodyText   string         `json:"body_text,omitempty"`
	Variables  map[string]any `json:"variables,omitempty"`
	LocalID    int64          `json:"local_id,omitempty"`
	LocalState string         `json:"local_state"` // new | imported | changed
}

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
	items, err := listProviderTemplates(ctx, pid, channel)
	if err != nil {
		_ = dbSyncStateMark(ctx.AppDB(), pid, channel, 0, err.Error())
		return 0, err
	}

	count := 0
	for _, item := range items {
		if _, err := upsertProviderTemplate(ctx, pid, item); err != nil {
			ctx.Logger().Warn("template sync upsert failed", "sid", item.Sid, "err", err)
			continue
		}
		count++
	}
	markMissingProviderTemplatesDeleted(ctx, pid, items)
	_ = dbSyncStateMark(ctx.AppDB(), pid, channel, count, "")
	emitMessagingEvent(ctx, pid, "templates.synced", map[string]any{
		"channel": channel,
		"count":   count,
	})
	return count, nil
}

func listProviderTemplates(ctx *sdk.AppCtx, pid, channel string) ([]providerTemplateInfo, error) {
	if channel != channelWhatsApp {
		return nil, fmt.Errorf("no provider import for channel %q (local templates only)", channel)
	}
	bound := ctx.IntegrationFor("phone_provider")
	if bound == nil {
		return nil, errors.New("no phone_provider bound — install/select a Twilio connection")
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, "list_content_templates", map[string]any{
		"PageSize": 100,
	})
	if err != nil {
		return nil, fmt.Errorf("list_content_templates: %w", err)
	}
	if res == nil || !res.Success {
		body := ""
		if res != nil {
			body = string(res.Data)
		}
		return nil, fmt.Errorf("provider non-2xx: %s", truncate(body, 400))
	}
	// Twilio /v2/ContentAndApprovals response:
	//   { contents: [{ sid, friendly_name, language, variables, types,
	//                  approval_requests: { status: "approved", … } }],
	//     meta: { … } }
	// Older fixtures and some endpoints use approval_requests as an
	// array, so approvalInfoFromAny handles both shapes.
	var raw struct {
		Contents []struct {
			Sid          string         `json:"sid"`
			FriendlyName string         `json:"friendly_name"`
			Language     string         `json:"language"`
			Variables    map[string]any `json:"variables"`
			Types        map[string]any `json:"types"`
			Approval     any            `json:"approval_requests"`
		} `json:"contents"`
	}
	_ = json.Unmarshal(res.Data, &raw)

	out := make([]providerTemplateInfo, 0, len(raw.Contents))
	for _, c := range raw.Contents {
		if c.Sid == "" {
			continue
		}
		status := "pending"
		category := ""
		approval := approvalInfoFromAny(c.Approval)
		if approval.Status != "" {
			status = approval.Status
			category = approval.Category
		}
		variables := c.Variables
		if variables == nil {
			variables = map[string]any{}
		}
		item := providerTemplateInfo{
			Sid:        c.Sid,
			Name:       c.FriendlyName,
			Language:   c.Language,
			Category:   category,
			Status:     status,
			BodyText:   providerTemplatePreviewBody(c.Types),
			Variables:  variables,
			LocalState: "new",
		}
		existing, _ := dbTemplateGetByProviderID(ctx.AppDB(), pid, c.Sid)
		if existing != nil {
			if existing.Channel == channelSMS && approval.Status == "" && existing.ProviderStatus != "" {
				item.Status = existing.ProviderStatus
			}
			item.LocalID = existing.ID
			if providerTemplateChanged(existing, item) {
				item.LocalState = "changed"
			} else {
				item.LocalState = "imported"
			}
		}
		out = append(out, item)
	}
	return out, nil
}

func providerTemplatePreviewBody(types map[string]any) string {
	if t, ok := types["twilio/text"].(map[string]any); ok {
		if b, ok := t["body"].(string); ok {
			return b
		}
	}
	for _, v := range types {
		if t, ok := v.(map[string]any); ok {
			if b, ok := t["body"].(string); ok {
				return b
			}
		}
	}
	return ""
}

func providerTemplateChanged(existing *Template, item providerTemplateInfo) bool {
	if existing.Name != item.Name || existing.BodyText != item.BodyText || existing.ProviderStatus != item.Status {
		return true
	}
	varsJSON, _ := json.Marshal(item.Variables)
	return compactJSON(string(existing.VarsSchema)) != compactJSON(string(varsJSON))
}

func compactJSON(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return "{}"
	}
	var buf bytes.Buffer
	if err := json.Compact(&buf, []byte(s)); err != nil {
		return s
	}
	return buf.String()
}

func upsertProviderTemplate(ctx *sdk.AppCtx, pid string, item providerTemplateInfo) (bool, error) {
	varsJSON, _ := json.Marshal(item.Variables)
	if len(varsJSON) == 0 {
		varsJSON = []byte("{}")
	}
	existing, err := dbTemplateGetByProviderID(ctx.AppDB(), pid, item.Sid)
	if err != nil {
		return false, err
	}
	if existing == nil {
		_, err := ctx.AppDB().Exec(
			`INSERT INTO templates
				(project_id, channel, name, subject, body_text, body_html,
				 vars_schema, provider_template_id, provider_status,
				 var_style, last_synced_at)
			 VALUES (?, 'whatsapp', ?, '', ?, '', ?, ?, ?, 'numbered', CURRENT_TIMESTAMP)`,
			pid, item.Name, item.BodyText, string(varsJSON), item.Sid, item.Status,
		)
		return true, err
	}
	_, err = ctx.AppDB().Exec(
		`UPDATE templates SET
			name = ?, body_text = ?, vars_schema = ?,
			provider_status = ?, var_style = 'numbered',
			last_synced_at = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ?`,
		item.Name, item.BodyText, string(varsJSON), item.Status, existing.ID,
	)
	return false, err
}

func markMissingProviderTemplatesDeleted(ctx *sdk.AppCtx, pid string, items []providerTemplateInfo) {
	// Mark rows that disappeared upstream as deleted. Soft — we keep
	// them around so audit/history queries still resolve. Sends fail-
	// fast against status='deleted'.
	if len(items) > 0 {
		seen := make([]string, 0, len(items))
		for _, item := range items {
			if item.Sid != "" {
				seen = append(seen, item.Sid)
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
}

var templatePlaceholderRE = regexp.MustCompile(`\{\{\s*([A-Za-z0-9_]+)\s*\}\}`)

func validateWhatsAppTemplatePlaceholders(body string) error {
	for _, m := range templatePlaceholderRE.FindAllStringSubmatch(body, -1) {
		if len(m) < 2 {
			continue
		}
		if _, err := strconv.Atoi(m[1]); err != nil {
			return fmt.Errorf("whatsapp templates use numbered placeholders like {{1}}; found {{%s}}", m[1])
		}
	}
	return nil
}

func twilioTemplateVariables(body string, schema map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range schema {
		key := strings.TrimSpace(k)
		if key == "" {
			continue
		}
		out[key] = templateVariableExample(key, v)
	}
	for _, m := range templatePlaceholderRE.FindAllStringSubmatch(body, -1) {
		if len(m) < 2 {
			continue
		}
		key := m[1]
		if _, ok := out[key]; !ok {
			out[key] = "example " + key
		}
	}
	return out
}

func templateVariableExample(key string, v any) string {
	switch x := v.(type) {
	case string:
		if strings.TrimSpace(x) != "" {
			return x
		}
	case map[string]any:
		for _, k := range []string{"example", "default", "sample", "name", "description"} {
			if s, ok := x[k].(string); ok && strings.TrimSpace(s) != "" {
				return s
			}
		}
	case float64, int, int64, bool:
		return fmt.Sprint(x)
	}
	return "example " + key
}

func extractProviderTemplateID(raw json.RawMessage) string {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	for _, k := range []string{"sid", "content_sid", "ContentSid", "id"} {
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	if content, ok := m["content"].(map[string]any); ok {
		for _, k := range []string{"sid", "content_sid", "ContentSid", "id"} {
			if s, ok := content[k].(string); ok && strings.TrimSpace(s) != "" {
				return strings.TrimSpace(s)
			}
		}
	}
	return ""
}

func extractApprovalStatus(raw json.RawMessage) string {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err != nil {
		return ""
	}
	for _, k := range []string{"status", "approval_status"} {
		if s, ok := m[k].(string); ok && strings.TrimSpace(s) != "" {
			return strings.ToLower(strings.TrimSpace(s))
		}
	}
	if req, ok := m["approval_request"].(map[string]any); ok {
		if info := approvalInfoFromAny(req); info.Status != "" {
			return info.Status
		}
	}
	if info := approvalInfoFromAny(m["approval_requests"]); info.Status != "" {
		return info.Status
	}
	return ""
}

type templateApprovalInfo struct {
	Status   string
	Category string
}

func approvalInfoFromAny(v any) templateApprovalInfo {
	switch x := v.(type) {
	case map[string]any:
		return templateApprovalInfo{
			Status:   strings.ToLower(strings.TrimSpace(strFromMap(x, "status", "Status", "approval_status"))),
			Category: strings.TrimSpace(strFromMap(x, "category", "Category")),
		}
	case []any:
		for _, item := range x {
			if info := approvalInfoFromAny(item); info.Status != "" || info.Category != "" {
				return info
			}
		}
	}
	return templateApprovalInfo{}
}

func normaliseTemplateCategory(s string) string {
	switch strings.ToUpper(strings.TrimSpace(s)) {
	case "MARKETING":
		return "MARKETING"
	case "AUTHENTICATION":
		return "AUTHENTICATION"
	default:
		return "UTILITY"
	}
}

func providerApprovalName(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	lastUnderscore := false
	for _, r := range name {
		ok := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if ok {
			b.WriteRune(r)
			lastUnderscore = false
			continue
		}
		if !lastUnderscore {
			b.WriteByte('_')
			lastUnderscore = true
		}
	}
	out := strings.Trim(b.String(), "_")
	if out == "" {
		return "template"
	}
	return out
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
	return dbSuppressionUpsertKind(db, pid, channel, "address", addr, reason, source)
}

func dbSuppressionUpsertKind(db *sql.DB, pid, channel, kind, addr, reason, source string) error {
	_, err := db.Exec(
		`INSERT INTO suppressions (project_id, channel, kind, address, reason, source, first_seen, last_seen)
		 VALUES (?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
		 ON CONFLICT(project_id, channel, address) DO UPDATE SET
		   kind = excluded.kind,
		   reason = excluded.reason,
		   source = CASE WHEN suppressions.source = 'manual' THEN 'manual' ELSE excluded.source END,
		   last_seen = CURRENT_TIMESTAMP`,
		pid, channel, kind, addr, reason, source,
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
		`SELECT project_id, channel, COALESCE(kind,'address'), address, reason, source,
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
		if err := rows.Scan(&s.ProjectID, &s.Channel, &s.Kind, &s.Address, &s.Reason, &s.Source, &s.FirstSeen, &s.LastSeen); err == nil {
			out = append(out, s)
		}
	}
	return out, nil
}

func dbSuppressionMatch(db *sql.DB, pid, channel, addr string) (*Suppression, error) {
	candidates := []struct {
		kind  string
		value string
	}{
		{kind: "address", value: strings.ToLower(addr)},
	}
	if channel == channelEmail {
		if d := emailDomain(addr); d != "" {
			candidates = append(candidates, struct {
				kind  string
				value string
			}{kind: "domain", value: d})
		}
	}
	for _, c := range candidates {
		row := db.QueryRow(
			`SELECT project_id, channel, COALESCE(kind,'address'), address, reason, source,
			        COALESCE(first_seen,''), COALESCE(last_seen,'')
			 FROM suppressions
			 WHERE project_id = ? AND channel = ? AND kind = ? AND address = ?`,
			pid, channel, c.kind, c.value,
		)
		s := &Suppression{}
		if err := row.Scan(&s.ProjectID, &s.Channel, &s.Kind, &s.Address, &s.Reason, &s.Source, &s.FirstSeen, &s.LastSeen); err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				continue
			}
			return nil, err
		}
		return s, nil
	}
	return nil, nil
}

func filterSuppressed(db *sql.DB, pid, channel string, addrs []string) (allowed, suppressed []string) {
	if len(addrs) == 0 {
		return addrs, nil
	}
	rows, err := db.Query(
		`SELECT COALESCE(kind,'address'), address FROM suppressions WHERE project_id = ? AND channel = ?`,
		pid, channel,
	)
	if err != nil {
		return addrs, nil
	}
	defer rows.Close()
	addresses := map[string]bool{}
	domains := map[string]bool{}
	for rows.Next() {
		var kind, a string
		if err := rows.Scan(&kind, &a); err == nil {
			switch kind {
			case "domain":
				domains[strings.ToLower(a)] = true
			default:
				addresses[strings.ToLower(a)] = true
			}
		}
	}
	for _, a := range addrs {
		lower := strings.ToLower(a)
		if addresses[lower] || (channel == channelEmail && domains[emailDomain(lower)]) {
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

func boolArg(args map[string]any, key string, def bool) bool {
	switch v := args[key].(type) {
	case bool:
		return v
	case string:
		if b, err := strconv.ParseBool(strings.TrimSpace(v)); err == nil {
			return b
		}
	case float64:
		return v != 0
	case int:
		return v != 0
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

func stringArrayArg(args map[string]any, key string) []string {
	out := []string{}
	add := func(s string) {
		s = strings.TrimSpace(s)
		if s == "" {
			return
		}
		for _, existing := range out {
			if existing == s {
				return
			}
		}
		out = append(out, s)
	}
	switch v := args[key].(type) {
	case string:
		for _, s := range strings.Fields(v) {
			add(s)
		}
	case []string:
		for _, s := range v {
			add(s)
		}
	case []any:
		for _, it := range v {
			if s, ok := it.(string); ok {
				add(s)
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

func nullableString(v string) any {
	if strings.TrimSpace(v) == "" {
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
