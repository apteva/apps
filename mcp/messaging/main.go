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
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/mail"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

// ─── Manifest (also lives in apteva.yaml) ──────────────────────────

const manifestYAML = `schema: apteva-app/v1
name: messaging
display_name: Messaging
version: 0.2.3
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
    - role: storage
      kind: app
      compatible_app_names: [storage]
      capabilities: [files.write]
      required: false
      label: "Storage (optional)"
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: send_message,           description: "Send a message. URI scheme picks the channel." }
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
    - { name: senders_list,           description: "List sending identities. Returns canonical URI rows." }
    - { name: senders_get,            description: "Get one identity's verification + DKIM state." }
    - { name: senders_delete,         description: "Remove a sending identity from the provider." }
    - { name: senders_verify_email,   description: "Verify an email or domain. Domain returns DKIM CNAMEs." }
    - { name: senders_get_quota,      description: "Provider sandbox + send-quota status." }
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
		{Pattern: "/messages", Handler: a.handleMessagesList},
		{Pattern: "/messages/", Handler: a.handleMessageItem},
		{Pattern: "/templates", Handler: a.handleTemplatesList},
		{Pattern: "/inbound-routes", Handler: a.handleInboundRoutesList},
		{Pattern: "/suppressions", Handler: a.handleSuppressionsList},
		{Pattern: "/senders", Handler: a.handleSendersList},
		{Pattern: "/senders/quota", Handler: a.handleSendersQuota},
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
			Description: "Send a message. The recipient URI scheme picks the channel " +
				"(mailto: → email; tel: and apteva://contact/N reserved for future channels). " +
				"Bare email strings are auto-coerced to mailto:. " +
				"Args: to (string|string[]), body, subject?, body_html?, from?, reply_to?, " +
				"cc?, bcc?, headers?, attachment_storage_ids?, template_id?, vars?, " +
				"prefer_channel?, idempotency_key?. " +
				"Returns {id, channel, status, recipients:[{address, status}], provider_message_id?}.",
			InputSchema: schemaObject(map[string]any{
				"to":                     map[string]any{},
				"body":                   map[string]any{"type": "string"},
				"subject":                map[string]any{"type": "string"},
				"body_html":              map[string]any{"type": "string"},
				"from":                   map[string]any{"type": "string"},
				"reply_to":               map[string]any{"type": "string"},
				"cc":                     map[string]any{},
				"bcc":                    map[string]any{},
				"headers":                map[string]any{"type": "object"},
				"attachment_storage_ids": map[string]any{"type": "array"},
				"template_id":            map[string]any{"type": "integer"},
				"vars":                   map[string]any{"type": "object"},
				"prefer_channel":         map[string]any{"type": "string"},
				"idempotency_key":        map[string]any{"type": "string"},
			}, []string{"to"}),
			Handler: a.toolSendMessage,
		},
		{
			Name:        "send_message_template",
			Description: "Render a saved template and send. Args: template_id, to, vars?, attachment_storage_ids?, idempotency_key?.",
			InputSchema: schemaObject(map[string]any{
				"template_id":            map[string]any{"type": "integer"},
				"to":                     map[string]any{},
				"vars":                   map[string]any{"type": "object"},
				"attachment_storage_ids": map[string]any{"type": "array"},
				"idempotency_key":        map[string]any{"type": "string"},
			}, []string{"template_id", "to"}),
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
			Description: "Bind a recipient URI pattern to a target app+route. Idempotent on (pattern, target_app, target_route). " +
				"Args: pattern (e.g. 'mailto:support+*@acme.com'), target_app, target_route, priority?.",
			InputSchema: schemaObject(map[string]any{
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
			Description: "Manually suppress an address. Args: address (URI), reason.",
			InputSchema: schemaObject(map[string]any{
				"address": map[string]any{"type": "string"},
				"reason":  map[string]any{"type": "string"},
			}, []string{"address", "reason"}),
			Handler: a.toolSuppressionAdd,
		},
		{
			Name:        "suppression_remove",
			Description: "Remove an address from suppression. Args: address (URI).",
			InputSchema: schemaObject(map[string]any{"address": map[string]any{"type": "string"}}, []string{"address"}),
			Handler:     a.toolSuppressionRemove,
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
			Name: "senders_verify_email",
			Description: "Verify an email address or domain with the email provider. The shape of the input picks the operation: " +
				"\"foo@bar.com\" verifies an address (provider sends a confirmation link); \"bar.com\" verifies a domain (returns DKIM CNAME records to publish in DNS).",
			InputSchema: schemaObject(map[string]any{"address": map[string]any{"type": "string"}}, []string{"address"}),
			Handler:     a.toolSendersVerifyEmail,
		},
		{
			Name:        "senders_get_quota",
			Description: "Provider-account stats: sandbox flag, 24h send quota, current usage, sending-enabled flag. Drives the sandbox banner.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Handler:     a.toolSendersGetQuota,
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

// ─── URI / address normalisation ───────────────────────────────────
//
// We normalise *to canonical URI form* on the way in and store that.
// Email addresses are lowercased (RFC 5321 mailbox-local-part
// case-sensitivity is technically unspecified but in practice every
// MTA folds it). Phone numbers are kept as-is for now; SMS isn't in
// v0.1. apteva://contact/N is normalised to a clean integer id.
//
// Schemes recognised:
//   - "mailto:foo@bar.com"
//   - "tel:+15551234"            (reserved; v0.1 errors)
//   - "apteva://contact/42"      (reserved; v0.1 errors)
//
// Bare strings are auto-coerced: anything containing "@" with no
// scheme becomes "mailto:<lower>". This keeps the agent ergonomics
// good without ambiguous parsing.

const (
	channelEmail = "email"
)

func normaliseURI(s string) (string, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return "", errors.New("empty address")
	}
	low := strings.ToLower(s)
	switch {
	case strings.HasPrefix(low, "mailto:"):
		addr := strings.TrimPrefix(s, s[:7]) // preserve case for the local part during validation
		addr = strings.ToLower(strings.TrimSpace(addr))
		if !looksLikeEmail(addr) {
			return "", fmt.Errorf("invalid email in mailto: %q", addr)
		}
		return "mailto:" + addr, nil
	case strings.HasPrefix(low, "tel:"):
		// reserved
		return "", errors.New("tel: addresses are not supported in v0.1 (sms channel pending)")
	case strings.HasPrefix(low, "apteva://contact/"):
		return "", errors.New("apteva://contact/N addresses are not supported in v0.1 (CRM resolver pending)")
	}
	// Bare string with @ → coerce to mailto:.
	if strings.Contains(s, "@") && !strings.Contains(s, " ") {
		addr := strings.ToLower(s)
		if !looksLikeEmail(addr) {
			return "", fmt.Errorf("invalid email: %q", addr)
		}
		return "mailto:" + addr, nil
	}
	return "", fmt.Errorf("unrecognised address %q (expected mailto:, tel:, or bare email)", s)
}

func channelOfURI(uri string) (string, error) {
	low := strings.ToLower(uri)
	switch {
	case strings.HasPrefix(low, "mailto:"):
		return channelEmail, nil
	}
	return "", fmt.Errorf("cannot determine channel for %q", uri)
}

func bareAddress(uri string) string {
	if strings.HasPrefix(strings.ToLower(uri), "mailto:") {
		return uri[7:]
	}
	return uri
}

// extractSubaddress returns the "+tag" portion of an email's local
// part if present, else the empty string. e.g.
// "support+T-1234@acme.com" → "T-1234".
func extractSubaddress(uri string) string {
	addr := bareAddress(uri)
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

// normaliseURIList accepts a string or []any/[]string and returns
// a deduped list of canonical URIs.
func normaliseURIList(v any) ([]string, error) {
	out := []string{}
	switch x := v.(type) {
	case nil:
		return out, nil
	case string:
		if x == "" {
			return out, nil
		}
		u, err := normaliseURI(x)
		if err != nil {
			return nil, err
		}
		return []string{u}, nil
	case []any:
		seen := map[string]bool{}
		for _, it := range x {
			s, ok := it.(string)
			if !ok || s == "" {
				continue
			}
			u, err := normaliseURI(s)
			if err != nil {
				return nil, err
			}
			if !seen[u] {
				seen[u] = true
				out = append(out, u)
			}
		}
	case []string:
		seen := map[string]bool{}
		for _, s := range x {
			if s == "" {
				continue
			}
			u, err := normaliseURI(s)
			if err != nil {
				return nil, err
			}
			if !seen[u] {
				seen[u] = true
				out = append(out, u)
			}
		}
	default:
		return nil, fmt.Errorf("expected string or string[], got %T", v)
	}
	return out, nil
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
	CreatedAt            string          `json:"created_at,omitempty"`
	SentAt               string          `json:"sent_at,omitempty"`
	ReceivedAt           string          `json:"received_at,omitempty"`
	LastEventAt          string          `json:"last_event_at,omitempty"`
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
	CreatedAt  string          `json:"created_at,omitempty"`
	UpdatedAt  string          `json:"updated_at,omitempty"`
}

type InboundRoute struct {
	ID          int64  `json:"id"`
	ProjectID   string `json:"project_id,omitempty"`
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

	to, err := normaliseURIList(args["to"])
	if err != nil {
		return nil, fmt.Errorf("to: %w", err)
	}
	if len(to) == 0 {
		return nil, errors.New("to: at least one recipient required")
	}
	cc, err := normaliseURIList(args["cc"])
	if err != nil {
		return nil, fmt.Errorf("cc: %w", err)
	}
	bcc, err := normaliseURIList(args["bcc"])
	if err != nil {
		return nil, fmt.Errorf("bcc: %w", err)
	}

	// Channel must be uniform across all recipients in v0.1.
	channel, err := channelOfURI(to[0])
	if err != nil {
		return nil, err
	}
	for _, r := range append(append([]string{}, cc...), bcc...) {
		ch, err := channelOfURI(r)
		if err != nil {
			return nil, err
		}
		if ch != channel {
			return nil, fmt.Errorf("mixed channels not supported: %s vs %s", channel, ch)
		}
	}

	// Optional template render.
	body := strArg(args, "body")
	subject := strArg(args, "subject")
	bodyHTML := strArg(args, "body_html")
	templateID := int64Arg(args, "template_id")
	if templateID > 0 {
		tpl, err := dbTemplateGet(ctx.AppDB(), pid, templateID)
		if err != nil {
			return nil, err
		}
		if tpl == nil {
			return nil, fmt.Errorf("template_id %d not found", templateID)
		}
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

	if body == "" && bodyHTML == "" {
		return nil, errors.New("body or body_html required (directly or via template)")
	}

	from := strArg(args, "from")
	if from == "" {
		return nil, errors.New("from: required (pick a verified sender via senders_list)")
	}
	from, err = normaliseURI(from)
	if err != nil {
		return nil, fmt.Errorf("from: %w", err)
	}
	replyTo := strArg(args, "reply_to")
	if replyTo != "" {
		replyTo, err = normaliseURI(replyTo)
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

	// Provider call.
	providerMessageID, providerErr := sendViaProvider(ctx, providerSendInput{
		From: from, To: allowedTo, CC: allowedCC, BCC: allowedBCC,
		Subject: subject, BodyText: body, BodyHTML: bodyHTML,
		ReplyTo: replyTo, Headers: headers,
	})
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
	From, ReplyTo string
	To, CC, BCC   []string
	Subject       string
	BodyText      string
	BodyHTML      string
	Headers       map[string]any
}

// sendViaProvider invokes the bound email_provider integration's
// send_email tool, mapping our flat input to AWS SES v2's nested
// SendEmail payload (FromEmailAddress / Destination / Content.Simple).
// We strip the mailto: prefix from addresses — SES expects bare addrs.
//
// Custom headers are silently dropped in v0.1: SES Simple content
// doesn't accept arbitrary headers. Setting them requires switching to
// send_raw_email with a fully-built MIME blob, which is a v0.2 feature.
func sendViaProvider(ctx *sdk.AppCtx, in providerSendInput) (string, error) {
	bound := ctx.IntegrationFor("email_provider")
	if bound == nil {
		return "", errors.New("no email_provider bound — install/select an aws-ses connection")
	}
	tool := bound.ToolFor("email.send")
	if tool == "" {
		tool = "send_email"
	}

	// SES v2 SendEmail — nested shape per integrations/src/apps/aws-ses.json.
	dest := map[string]any{}
	if to := stripMailto(in.To); len(to) > 0 {
		dest["ToAddresses"] = to
	}
	if cc := stripMailto(in.CC); len(cc) > 0 {
		dest["CcAddresses"] = cc
	}
	if bcc := stripMailto(in.BCC); len(bcc) > 0 {
		dest["BccAddresses"] = bcc
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
		// SES requires a Subject; default rather than reject.
		subj = "(no subject)"
	}
	payload := map[string]any{
		"FromEmailAddress": bareAddress(in.From),
		"Destination":      dest,
		"Content": map[string]any{
			"Simple": map[string]any{
				"Subject": map[string]any{"Data": subj, "Charset": "UTF-8"},
				"Body":    body,
			},
		},
	}
	if in.ReplyTo != "" {
		payload["ReplyToAddresses"] = []string{bareAddress(in.ReplyTo)}
	}
	if len(in.Headers) > 0 {
		ctx.Logger().Warn("messaging: custom headers ignored — SES v2 Simple content doesn't carry them. Use raw mode in v0.2.")
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
	// Try to pull a MessageId out of the response. SES returns
	// {"MessageId": "..."} or similar; we hunt for a few common keys.
	var probe map[string]any
	_ = json.Unmarshal(res.Data, &probe)
	for _, key := range []string{"MessageId", "message_id", "messageId", "id"} {
		if v, ok := probe[key].(string); ok && v != "" {
			return v, nil
		}
	}
	return "", nil
}

func stripMailto(uris []string) []string {
	out := make([]string, 0, len(uris))
	for _, u := range uris {
		out = append(out, bareAddress(u))
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
	}
	if opts.Address != "" {
		// best-effort normalise so callers can pass bare emails.
		if u, err := normaliseURI(opts.Address); err == nil {
			opts.Address = u
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
	// Light validation — must be a recognisable URI scheme. Wildcards
	// (`*`) are allowed in the local part, so we replace them with
	// "x" before normalising for a syntax check.
	probe := strings.ReplaceAll(pattern, "*", "x")
	if _, err := normaliseURI(probe); err != nil {
		return nil, fmt.Errorf("pattern: %w", err)
	}
	pattern = strings.ToLower(strings.TrimSpace(pattern))
	targetApp := strArg(args, "target_app")
	targetRoute := strArg(args, "target_route")
	if targetApp == "" || targetRoute == "" {
		return nil, errors.New("target_app and target_route required")
	}
	priority := intArg(args, "priority", 100)
	id, err := dbInboundRouteUpsert(ctx.AppDB(), pid, pattern, targetApp, targetRoute, priority)
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
	out, err := dbTemplateList(ctx.AppDB(), pid, strArg(args, "channel"), limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{"templates": out, "count": len(out)}, nil
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
	addr, err := normaliseURI(addrRaw)
	if err != nil {
		return nil, err
	}
	channel, err := channelOfURI(addr)
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

func (a *App) toolSuppressionRemove(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	addrRaw := strArg(args, "address")
	addr, err := normaliseURI(addrRaw)
	if err != nil {
		return nil, err
	}
	channel, err := channelOfURI(addr)
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
	return map[string]any{"removed": true, "address": addr}, nil
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
	Address     string   `json:"address"`               // canonical URI
	Kind        string   `json:"kind"`                  // "email" | "domain" (channel-specific)
	Verified    bool     `json:"verified"`
	DKIMStatus  string   `json:"dkim_status,omitempty"` // "SUCCESS"|"PENDING"|"FAILED"|"NOT_STARTED"
	DKIMTokens  []string `json:"dkim_tokens,omitempty"` // populated by senders_get for domain identities
	Sending     bool     `json:"sending_enabled"`
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

// canonicalSenderURI returns the URI form for a bare SES identity.
// Domains and addresses both fold under the mailto: scheme — the
// scheme is "messaging-channel," not "RFC mailbox" — i.e., a
// "mailto:bar.com" sender means "this domain can send email," not a
// receivable mailbox. v0.2 keeps it simple; an explicit mailto:domain://
// scheme would be more correct but adds parser complexity for no
// callee win today.
func canonicalSenderURI(kind, raw string) string {
	return "mailto:" + raw
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

func (a *App) toolSendersList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	connID, _, err := emailProviderConn(ctx)
	if err != nil {
		return nil, err
	}
	verifiedOnly, _ := args["verified_only"].(bool)
	// PageSize is capped at 100 by SES; for accounts with many identities
	// we follow NextToken until exhausted. Practical ceiling: 1000 to
	// keep the panel snappy and bound memory.
	const maxIdentitiesToList = 1000
	out := make([]Sender, 0, 64)
	nextToken := ""
	for {
		args := map[string]any{"PageSize": 100}
		if nextToken != "" {
			args["NextToken"] = nextToken
		}
		res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "list_identities", args)
		if err != nil {
			return nil, fmt.Errorf("list_identities: %w", err)
		}
		if res == nil || !res.Success {
			body := ""
			if res != nil {
				body = string(res.Data)
			}
			return nil, fmt.Errorf("provider non-2xx: %s", truncate(body, 400))
		}
		// SES v2 ListEmailIdentities response shape (the JSON key is
		// `EmailIdentities`, NOT `Identities` — that's the v1/legacy
		// API; we hit v2 via /v2/email/identities):
		//   { EmailIdentities: [{ IdentityName, IdentityType, SendingEnabled, VerificationStatus }], NextToken? }
		var raw struct {
			EmailIdentities []struct {
				IdentityName       string `json:"IdentityName"`
				IdentityType       string `json:"IdentityType"`       // EMAIL_ADDRESS | DOMAIN | MANAGED_DOMAIN
				SendingEnabled     bool   `json:"SendingEnabled"`
				VerificationStatus string `json:"VerificationStatus"` // SUCCESS | PENDING | FAILED | TEMPORARY_FAILURE | NOT_STARTED
			} `json:"EmailIdentities"`
			NextToken string `json:"NextToken"`
		}
		_ = json.Unmarshal(res.Data, &raw)
		for _, id := range raw.EmailIdentities {
			kind := "email"
			if id.IdentityType == "DOMAIN" || id.IdentityType == "MANAGED_DOMAIN" {
				kind = "domain"
			}
			verified := strings.EqualFold(id.VerificationStatus, "SUCCESS")
			if verifiedOnly && !verified {
				continue
			}
			out = append(out, Sender{
				Address:  canonicalSenderURI(kind, strings.ToLower(id.IdentityName)),
				Kind:     kind,
				Verified: verified,
				Sending:  id.SendingEnabled,
			})
		}
		if raw.NextToken == "" || len(out) >= maxIdentitiesToList {
			break
		}
		nextToken = raw.NextToken
	}
	return map[string]any{"senders": out, "count": len(out)}, nil
}

func (a *App) toolSendersGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	connID, _, err := emailProviderConn(ctx)
	if err != nil {
		return nil, err
	}
	_, raw, err := classifyEmailIdentity(strArg(args, "address"))
	if err != nil {
		return nil, err
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, "get_identity_verification", map[string]any{
		"EmailIdentity": raw,
	})
	if err != nil {
		return nil, fmt.Errorf("get_identity_verification: %w", err)
	}
	if res == nil || !res.Success {
		body := ""
		if res != nil {
			body = string(res.Data)
		}
		return nil, fmt.Errorf("provider non-2xx: %s", truncate(body, 400))
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
	out := Sender{
		Address:    canonicalSenderURI(kind, raw),
		Kind:       kind,
		Verified:   inner.VerifiedForSendingStatus,
		DKIMStatus: inner.DkimAttributes.Status,
		DKIMTokens: inner.DkimAttributes.Tokens,
	}
	return map[string]any{
		"sender":                     out,
		"feedback_forwarding_enabled": inner.FeedbackForwardingStatus,
	}, nil
}

func (a *App) toolSendersDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	connID, _, err := emailProviderConn(ctx)
	if err != nil {
		return nil, err
	}
	_, raw, err := classifyEmailIdentity(strArg(args, "address"))
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
	return map[string]any{"deleted": true, "address": canonicalSenderURI("", raw)}, nil
}

// toolSendersVerifyEmail picks verify_email vs verify_domain from
// the address shape. Domains return DKIM CNAME tokens for DNS.
func (a *App) toolSendersVerifyEmail(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	connID, _, err := emailProviderConn(ctx)
	if err != nil {
		return nil, err
	}
	kind, raw, err := classifyEmailIdentity(strArg(args, "address"))
	if err != nil {
		return nil, err
	}
	tool := "verify_email"
	if kind == "domain" {
		tool = "verify_domain"
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, tool, map[string]any{
		"EmailIdentity": raw,
	})
	if err != nil {
		return nil, fmt.Errorf("%s: %w", tool, err)
	}
	if res == nil || !res.Success {
		body := ""
		if res != nil {
			body = string(res.Data)
		}
		return nil, fmt.Errorf("provider non-2xx: %s", truncate(body, 400))
	}
	// verify_email has empty body. verify_domain returns DkimAttributes.Tokens.
	out := map[string]any{
		"address":          canonicalSenderURI(kind, raw),
		"kind":             normaliseSenderKind(kind),
		"pending":          true,
		"next_step":        verifyNextStepHint(kind),
	}
	if kind == "domain" {
		var inner struct {
			DkimAttributes struct {
				Tokens []string `json:"Tokens"`
				Status string   `json:"Status"`
			} `json:"DkimAttributes"`
		}
		_ = json.Unmarshal(res.Data, &inner)
		out["dkim_tokens"] = inner.DkimAttributes.Tokens
		out["dkim_status"] = inner.DkimAttributes.Status
		out["dns_records"] = dkimCNAMERecords(raw, inner.DkimAttributes.Tokens)
	}
	return out, nil
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

	parsed, err := parseSESInboundContent(env.Message)
	if err != nil {
		httpErr(w, http.StatusBadRequest, "ses inbound: "+err.Error())
		return
	}
	if parsed == nil {
		httpJSON(w, map[string]any{"ok": true, "skipped": "no content (S3-only mode not supported in v0.1)"})
		return
	}

	to := normaliseEmailList(parsed.To)
	cc := normaliseEmailList(parsed.Cc)
	from := normaliseEmailURI(parsed.From)
	if from == "" {
		from = "mailto:unknown@invalid"
	}
	hdrJSON, _ := json.Marshal(parsed.Headers)
	toJSON, _ := json.Marshal(to)
	ccJSON, _ := json.Marshal(cc)
	refsJSON, _ := json.Marshal(parsed.References)
	now := time.Now().UTC().Format(time.RFC3339)

	res, err := globalCtx.AppDB().Exec(
		`INSERT INTO messages
			(project_id, channel, direction, from_addr, to_addrs, cc_addrs,
			 subject, body_text, body_html, headers,
			 message_id_header, in_reply_to, references_json,
			 status, route_status, received_at, last_event_at)
		 VALUES (?, 'email', 'in', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'received', 'pending', ?, ?)`,
		pid, from, string(toJSON), string(ccJSON),
		parsed.Subject, parsed.BodyText, parsed.BodyHTML, string(hdrJSON),
		parsed.MessageID, parsed.InReplyTo, string(refsJSON),
		now, now,
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
			ok, sub := patternMatches(r.Pattern, recip)
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
func patternMatches(pattern, addr string) (bool, string) {
	pattern = strings.ToLower(pattern)
	addr = strings.ToLower(addr)
	// Both must be mailto: in v0.1.
	if !strings.HasPrefix(pattern, "mailto:") || !strings.HasPrefix(addr, "mailto:") {
		return pattern == addr, ""
	}
	pAddr := pattern[7:]
	aAddr := addr[7:]
	pAt := strings.IndexByte(pAddr, '@')
	aAt := strings.IndexByte(aAddr, '@')
	if pAt < 0 || aAt < 0 {
		return false, ""
	}
	pLocal, pDomain := pAddr[:pAt], pAddr[pAt+1:]
	aLocal, aDomain := aAddr[:aAt], aAddr[aAt+1:]
	if pDomain != aDomain {
		return false, ""
	}
	// Domain wildcard not supported in v0.1.
	// Local-part: support exact, "*" (full-local wildcard), and
	// "<prefix>+*" (subaddress wildcard).
	switch {
	case pLocal == aLocal:
		return true, ""
	case pLocal == "*":
		return true, extractSubaddress(addr)
	case strings.HasSuffix(pLocal, "+*"):
		prefix := strings.TrimSuffix(pLocal, "+*")
		// "support+T-1234" matches "support+*"
		if !strings.HasPrefix(aLocal, prefix+"+") {
			return false, ""
		}
		return true, aLocal[len(prefix)+1:]
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

// parseSESInboundContent unwraps an SES Received notification's
// embedded `content` (RFC 822 bytes) and extracts the headers + body.
// Returns (nil, nil) when the notification doesn't carry content
// (S3-action mode), so the caller can decide what to do.
func parseSESInboundContent(message string) (*parsedInbound, error) {
	var outer struct {
		NotificationType string `json:"notificationType"`
		Content          string `json:"content"`
		Mail             struct {
			MessageID string            `json:"messageId"`
			Headers   []struct{ Name, Value string } `json:"headers"`
		} `json:"mail"`
	}
	if err := json.Unmarshal([]byte(message), &outer); err != nil {
		return nil, err
	}
	if outer.Content == "" {
		return nil, nil
	}
	rawBytes := []byte(outer.Content)
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
	if parsed.MessageID == "" && outer.Mail.MessageID != "" {
		parsed.MessageID = outer.Mail.MessageID
	}
	return parsed, nil
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

func normaliseEmailURI(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	addr, err := mail.ParseAddress(s)
	if err == nil {
		return "mailto:" + strings.ToLower(addr.Address)
	}
	if u, err := normaliseURI(s); err == nil {
		return u
	}
	return ""
}

func normaliseEmailList(addrs []string) []string {
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		if u := normaliseEmailURI(a); u != "" {
			out = append(out, u)
		}
	}
	return out
}

func canonicalAddrForChannel(channel, addr string) string {
	switch channel {
	case channelEmail:
		return "mailto:" + strings.ToLower(strings.TrimSpace(addr))
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
	if r.URL.Query().Get("verified_only") == "true" {
		args["verified_only"] = true
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
	var to, cc, bcc, headers, attachIDs, refs string
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
		        vars_schema, COALESCE(created_at,''), COALESCE(updated_at,'')
		 FROM templates WHERE id = ? AND project_id = ? AND deleted_at IS NULL`,
		id, pid,
	)
	t := &Template{}
	var vars string
	err := row.Scan(&t.ID, &t.ProjectID, &t.Channel, &t.Name, &t.Subject,
		&t.BodyText, &t.BodyHTML, &vars, &t.CreatedAt, &t.UpdatedAt)
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

func dbInboundRouteUpsert(db *sql.DB, pid, pattern, app, route string, priority int) (int64, error) {
	var id int64
	err := db.QueryRow(
		`SELECT id FROM inbound_routes
		 WHERE project_id = ? AND pattern = ? AND target_app = ? AND target_route = ?`,
		pid, pattern, app, route,
	).Scan(&id)
	if err == nil {
		_, err = db.Exec(`UPDATE inbound_routes SET priority = ? WHERE id = ?`, priority, id)
		return id, err
	}
	if err != sql.ErrNoRows {
		return 0, err
	}
	res, err := db.Exec(
		`INSERT INTO inbound_routes (project_id, pattern, target_app, target_route, priority)
		 VALUES (?, ?, ?, ?, ?)`,
		pid, pattern, app, route, priority,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func dbInboundRouteGet(db *sql.DB, pid string, id int64) (*InboundRoute, error) {
	row := db.QueryRow(
		`SELECT id, project_id, pattern, target_app, target_route, priority, COALESCE(created_at,'')
		 FROM inbound_routes WHERE id = ? AND project_id = ?`, id, pid)
	r := &InboundRoute{}
	err := row.Scan(&r.ID, &r.ProjectID, &r.Pattern, &r.TargetApp, &r.TargetRoute, &r.Priority, &r.CreatedAt)
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
		`SELECT id, project_id, pattern, target_app, target_route, priority, COALESCE(created_at,'')
		 FROM inbound_routes WHERE project_id = ?
		 ORDER BY priority DESC, length(pattern) DESC`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []InboundRoute{}
	for rows.Next() {
		r := InboundRoute{}
		if err := rows.Scan(&r.ID, &r.ProjectID, &r.Pattern, &r.TargetApp, &r.TargetRoute, &r.Priority, &r.CreatedAt); err == nil {
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
