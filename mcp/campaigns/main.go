// Campaigns v0.1 — bulk-send orchestrator.
//
// Layered on top of CRM (audience source: lists + segments) and
// messaging (the actual sender). Jobs drives the materialise → tick
// loop so this app doesn't need its own scheduler.
//
// File map:
//   main.go      — manifest, App struct, MCP/HTTP registration, helpers
//   db.go        — SQL layer (campaigns, recipients, unsubscribe tokens)
//   pipeline.go  — handlers: CRUD, lifecycle, send pipeline, public endpoints
package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

// ─── Manifest (mirrors apteva.yaml; embedded so the binary is self-
// describing for tooling that can read it without filesystem access) ─

const manifestYAML = `schema: apteva-app/v1
name: campaigns
display_name: Campaigns
version: 0.1.0
description: |
  Bulk-send orchestrator. Compose a campaign, target a CRM segment or
  list, schedule it; jobs drives the materialise → tick loop, messaging
  does the sending. Per-recipient state, suppression respect, email
  unsubscribe with HMAC-validated tokens, basic stats.
author: Apteva
scopes: [project, global]
requires:
  permissions:
    - db.write.app
    - net.egress
    - platform.instances.read
    - platform.apps.call
  integrations:
    - role: crm
      kind: app
      compatible_app_names: [crm]
      capabilities: [contacts.list, segments.eval]
      required: true
      label: "CRM (audience source)"
    - role: messaging
      kind: app
      compatible_app_names: [messaging]
      capabilities: [message.send, suppression.check]
      required: true
      label: "Messaging (sender)"
    - role: jobs
      kind: app
      compatible_app_names: [jobs]
      capabilities: [job.schedule]
      required: true
      label: "Jobs (scheduler)"
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - name: campaigns_create
      description: Create a new campaign in draft state.
    - name: campaigns_list
      description: List campaigns with status / channel filter.
    - name: campaigns_get
      description: Fetch one campaign with stats.
    - name: campaigns_update
      description: Partial-patch a draft / paused campaign.
    - name: campaigns_clone
      description: Duplicate a campaign as a new draft.
    - name: campaigns_delete
      description: Archive a campaign.
    - name: campaigns_send_test
      description: Send a single test message to a contact.
    - name: campaigns_schedule
      description: Move a draft to scheduled at a specific timestamp.
    - name: campaigns_start_now
      description: Move a campaign straight to materialising.
    - name: campaigns_pause
      description: Pause a sending campaign.
    - name: campaigns_resume
      description: Resume a paused campaign.
    - name: campaigns_cancel
      description: Cancel a campaign permanently.
    - name: campaigns_recipients
      description: List a campaign's recipients with status filter.
    - name: campaigns_stats
      description: Aggregate counts per status.
  ui_panels:
    - slot: project.page
      label: Campaigns
      icon: send
      entry: /ui/CampaignsPanel.mjs
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/campaigns
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/campaigns.db
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
		return errors.New("campaigns requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("campaigns mounted",
		"scope_project_id", os.Getenv("APTEVA_PROJECT_ID"))
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

// ─── HTTP routes ──────────────────────────────────────────────────

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		// Dashboard / agent-callable.
		{Pattern: "/campaigns", Handler: a.handleHTTPCampaigns},
		{Pattern: "/campaigns/", Handler: a.handleHTTPCampaignItem},
		// Public (no auth, token-validated). Mounted at the same root
		// so the platform's reverse proxy serves them under
		// /api/apps/campaigns/unsubscribe.
		{Pattern: "/unsubscribe", Handler: a.handleHTTPUnsubscribe},
	}
}

// handleHTTPCampaigns dispatches GET / POST on /campaigns.
func (a *App) handleHTTPCampaigns(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.handleHTTPCampaignsList(w, r)
	case http.MethodPost:
		a.handleHTTPCampaignsCreate(w, r)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleHTTPCampaignItem dispatches /campaigns/{id}[/{sub}].
//
// Sub-paths:
//   - recipients          — GET list with status filter
//   - stats               — GET aggregate counts
//   - schedule | start_now| pause | resume | cancel | send_test — POST lifecycle
//   - materialise | tick  — POST internal (called by jobs only)
func (a *App) handleHTTPCampaignItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/campaigns/")
	parts := strings.SplitN(rest, "/", 2)
	id, _ := strconv.ParseInt(parts[0], 10, 64)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "campaign id required")
		return
	}
	if len(parts) >= 2 {
		switch parts[1] {
		case "recipients":
			a.handleHTTPRecipientsList(w, r, id)
			return
		case "stats":
			a.handleHTTPStats(w, r, id)
			return
		case "schedule":
			a.handleHTTPSchedule(w, r, id)
			return
		case "start_now":
			a.handleHTTPStartNow(w, r, id)
			return
		case "pause":
			a.handleHTTPPause(w, r, id)
			return
		case "resume":
			a.handleHTTPResume(w, r, id)
			return
		case "cancel":
			a.handleHTTPCancel(w, r, id)
			return
		case "send_test":
			a.handleHTTPSendTest(w, r, id)
			return
		case "materialise":
			a.handleHTTPMaterialise(w, r, id)
			return
		case "tick":
			a.handleHTTPTick(w, r, id)
			return
		case "clone":
			a.handleHTTPClone(w, r, id)
			return
		}
	}
	switch r.Method {
	case http.MethodGet:
		a.handleHTTPCampaignGet(w, r, id)
	case http.MethodPatch:
		a.handleHTTPCampaignUpdate(w, r, id)
	case http.MethodDelete:
		a.handleHTTPCampaignDelete(w, r, id)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// ─── MCP tools ────────────────────────────────────────────────────

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		// Authoring.
		{
			Name:        "campaigns_create",
			Description: "Create a campaign in draft state. Args: name, channel (email|sms|whatsapp), subject?, body_text?, body_html?, sender_address?, list_id?, segment_id?, batch_size?, tick_interval_seconds?.",
			InputSchema: schemaObject(map[string]any{
				"name":                  map[string]any{"type": "string"},
				"description":           map[string]any{"type": "string"},
				"channel":               map[string]any{"type": "string"},
				"sender_address":        map[string]any{"type": "string"},
				"subject":               map[string]any{"type": "string"},
				"body_text":             map[string]any{"type": "string"},
				"body_html":             map[string]any{"type": "string"},
				"template_name":         map[string]any{"type": "string"},
				"list_id":               map[string]any{"type": "integer"},
				"segment_id":            map[string]any{"type": "integer"},
				"batch_size":            map[string]any{"type": "integer"},
				"tick_interval_seconds": map[string]any{"type": "integer"},
			}, []string{"name", "channel"}),
			Handler: a.toolCampaignsCreate,
		},
		{
			Name:        "campaigns_list",
			Description: "List campaigns. Args: status? (filter), channel? (filter), include_archived? (default false).",
			InputSchema: schemaObject(map[string]any{
				"status":           map[string]any{"type": "string"},
				"channel":          map[string]any{"type": "string"},
				"include_archived": map[string]any{"type": "boolean"},
			}, nil),
			Handler: a.toolCampaignsList,
		},
		{
			Name:        "campaigns_get",
			Description: "Fetch one campaign with status counts. Args: id.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolCampaignsGet,
		},
		{
			Name:        "campaigns_update",
			Description: "Partial-patch a draft / paused campaign. Args: id, patch (any subset of editable fields).",
			InputSchema: schemaObject(map[string]any{
				"id":    map[string]any{"type": "integer"},
				"patch": map[string]any{"type": "object"},
			}, []string{"id", "patch"}),
			Handler: a.toolCampaignsUpdate,
		},
		{
			Name:        "campaigns_clone",
			Description: "Duplicate a campaign as a new draft. Args: id, name? (default '<original> (copy)').",
			InputSchema: schemaObject(map[string]any{
				"id":   map[string]any{"type": "integer"},
				"name": map[string]any{"type": "string"},
			}, []string{"id"}),
			Handler: a.toolCampaignsClone,
		},
		{
			Name:        "campaigns_delete",
			Description: "Archive a campaign. Recipients are kept; the row just stops appearing.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolCampaignsDelete,
		},
		{
			Name:        "campaigns_send_test",
			Description: "Send a single test message via this campaign's content + sender to a specific contact. Logged in CRM as <channel>_test_sent. Args: id (campaign), contact_id.",
			InputSchema: schemaObject(map[string]any{
				"id":         map[string]any{"type": "integer"},
				"contact_id": map[string]any{"type": "integer"},
			}, []string{"id", "contact_id"}),
			Handler: a.toolCampaignsSendTest,
		},

		// Lifecycle.
		{
			Name:        "campaigns_schedule",
			Description: "Move a draft to scheduled at scheduled_at (ISO-8601). Schedules a 'once' job in jobs that fires materialise. Args: id, scheduled_at.",
			InputSchema: schemaObject(map[string]any{
				"id":           map[string]any{"type": "integer"},
				"scheduled_at": map[string]any{"type": "string"},
			}, []string{"id", "scheduled_at"}),
			Handler: a.toolCampaignsSchedule,
		},
		{
			Name:        "campaigns_start_now",
			Description: "Skip the scheduled wait and materialise + start sending immediately. Args: id.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolCampaignsStartNow,
		},
		{
			Name:        "campaigns_pause",
			Description: "Pause a sending campaign. The tick job is cancelled; resume re-creates it.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolCampaignsPause,
		},
		{
			Name:        "campaigns_resume",
			Description: "Resume a paused campaign.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolCampaignsResume,
		},
		{
			Name:        "campaigns_cancel",
			Description: "Cancel a campaign permanently. Pending recipients become 'skipped'.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolCampaignsCancel,
		},

		// Read-only.
		{
			Name:        "campaigns_recipients",
			Description: "List a campaign's recipients with optional status filter. Args: id, status?, limit? (default 200).",
			InputSchema: schemaObject(map[string]any{
				"id":     map[string]any{"type": "integer"},
				"status": map[string]any{"type": "string"},
				"limit":  map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolCampaignsRecipients,
		},
		{
			Name:        "campaigns_stats",
			Description: "Aggregate counts per recipient status — drives the panel progress bar. Args: id.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolCampaignsStats,
		},
	}
}

func main() { sdk.Run(&App{}) }

// ─── Project resolution ────────────────────────────────────────────

func resolveProjectFromArgs(args map[string]any) (string, error) {
	if pid := os.Getenv("APTEVA_PROJECT_ID"); pid != "" {
		return pid, nil
	}
	pid, _ := args["_project_id"].(string)
	if pid == "" {
		pid, _ = args["project_id"].(string)
	}
	if pid == "" {
		return "", errors.New("_project_id required (install scope=global)")
	}
	return pid, nil
}

func resolveProjectFromRequest(r *http.Request) (string, error) {
	if pid := os.Getenv("APTEVA_PROJECT_ID"); pid != "" {
		return pid, nil
	}
	if pid := r.URL.Query().Get("project_id"); pid != "" {
		return pid, nil
	}
	if pid := r.Header.Get("X-Apteva-Project-Id"); pid != "" {
		return pid, nil
	}
	return "", errors.New("project_id required in query string when install scope=global")
}

// ─── Domain types ─────────────────────────────────────────────────

// Status enum for campaigns. Stored as TEXT; new values can be added
// without a migration. See migration 001 for the state-machine
// diagram.
const (
	StatusDraft         = "draft"
	StatusScheduled     = "scheduled"
	StatusMaterialising = "materialising"
	StatusSending       = "sending"
	StatusPaused        = "paused"
	StatusSent          = "sent"
	StatusCancelled     = "cancelled"
	StatusFailed        = "failed"
)

const (
	RecipPending      = "pending"
	RecipSending      = "sending"
	RecipSent         = "sent"
	RecipDelivered    = "delivered"
	RecipBounced      = "bounced"
	RecipComplained   = "complained"
	RecipFailed       = "failed"
	RecipSkipped      = "skipped"
	RecipUnsubscribed = "unsubscribed"
)

const (
	ChannelEmail    = "email"
	ChannelSMS      = "sms"
	ChannelWhatsApp = "whatsapp"
)

type Campaign struct {
	ID                  int64  `json:"id"`
	ProjectID           string `json:"project_id,omitempty"`
	Name                string `json:"name"`
	Description         string `json:"description,omitempty"`
	Status              string `json:"status"`
	Channel             string `json:"channel"`
	SenderAddress       string `json:"sender_address,omitempty"`
	Subject             string `json:"subject,omitempty"`
	BodyText            string `json:"body_text,omitempty"`
	BodyHTML            string `json:"body_html,omitempty"`
	TemplateName        string `json:"template_name,omitempty"`
	ListID              *int64 `json:"list_id,omitempty"`
	SegmentID           *int64 `json:"segment_id,omitempty"`
	ScheduleKind        string `json:"schedule_kind"`
	ScheduledAt         string `json:"scheduled_at,omitempty"`
	BatchSize           int64  `json:"batch_size,omitempty"`
	TickIntervalSeconds int64  `json:"tick_interval_seconds,omitempty"`
	OpenTracking        bool   `json:"open_tracking"`
	ClickTracking       bool   `json:"click_tracking"`
	JobIDs              string `json:"job_ids,omitempty"`
	CreatedAt           string `json:"created_at,omitempty"`
	UpdatedAt           string `json:"updated_at,omitempty"`
	StartedAt           string `json:"started_at,omitempty"`
	CompletedAt         string `json:"completed_at,omitempty"`
	ArchivedAt          string `json:"archived_at,omitempty"`
	Error               string `json:"error,omitempty"`

	// Optional populated only when the caller asks for stats.
	Stats map[string]int64 `json:"stats,omitempty"`
}

type Recipient struct {
	ID            int64  `json:"id"`
	CampaignID    int64  `json:"campaign_id"`
	ContactID     int64  `json:"contact_id"`
	Address       string `json:"address"`
	Status        string `json:"status"`
	MessagingID   *int64 `json:"messaging_id,omitempty"`
	AttemptCount  int64  `json:"attempt_count"`
	LastAttemptAt string `json:"last_attempt_at,omitempty"`
	SentAt        string `json:"sent_at,omitempty"`
	DeliveredAt   string `json:"delivered_at,omitempty"`
	Error         string `json:"error,omitempty"`
	CreatedAt     string `json:"created_at,omitempty"`
}

// ─── Helpers ──────────────────────────────────────────────────────

func httpJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
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

func nullStr(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}

func intArg(args map[string]any, key string, def int) int {
	if v, ok := args[key].(float64); ok {
		return int(v)
	}
	if v, ok := args[key].(int); ok {
		return v
	}
	if v, ok := args[key].(int64); ok {
		return int(v)
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
		n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return n
	}
	return 0
}

func strArg(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
}

func boolArg(args map[string]any, key string) bool {
	v, _ := args[key].(bool)
	return v
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func errString(e error) string {
	if e == nil {
		return ""
	}
	return e.Error()
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max] + "…"
}

// validStatusForUpdate gates campaigns_update to states where mutation
// is safe. Mid-send mutation would race with the tick loop; only draft
// and paused are mutable.
func validStatusForUpdate(s string) bool {
	return s == StatusDraft || s == StatusPaused
}

// validChannel returns true for the three channels we support.
func validChannel(c string) bool {
	return c == ChannelEmail || c == ChannelSMS || c == ChannelWhatsApp
}

// fmtError wraps a status-update with a concise error string.
func fmtError(format string, args ...any) string {
	return truncate(fmt.Sprintf(format, args...), 1000)
}

// globalCtx — the SDK doesn't thread AppCtx through HTTP handlers
// today, so we stash it at OnMount. Same pattern CRM and messaging use.
var globalCtx *sdk.AppCtx
