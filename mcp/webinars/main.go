// Webinars v0.1 — funnel + live + replay on top of streaming.
//
// Composition:
//   - Hard dep on streaming for the pipe (RTMP→HLS+recording).
//   - Soft dep on CRM for contact + activity attach.
//   - Soft dep on messaging for reminder fan-out.
//
// Public pages (NoAuth):
//   /r/<slug>                  — registration form
//   /r/<slug>/submit           — POST → 302 to /live/<token>
//   /live/<token>              — live room: HLS player + chat + offers
//   /live/<token>/heartbeat    — 10s heartbeat for attendance
//   /live/<token>/chat         — chat send
//   /live/<token>/poll-response
//   /live/<token>/offer-click
//   /live/<token>/events?since=N — long-poll for chat/offer/poll updates
//   /replay/<slug>?t=<token>   — replay page (when status=ended + published)
package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

// ─── Embedded manifest ────────────────────────────────────────────

const manifestYAML = `schema: apteva-app/v1
name: webinars
display_name: Webinars
version: 0.1.0
description: |
  Live, scheduled, and on-demand webinars on top of streaming + CRM
  + messaging.
author: Apteva
scopes: [project, global]
requires:
  permissions:
    - db.write.app
    - net.egress
    - platform.apps.call
  apps:
    - name: streaming
      version: ">=0.1.0"
      reason: allocates a stream per webinar; reads metrics + replay URL
  integrations:
    - role: crm
      kind: app
      compatible_app_names: [crm]
      capabilities: [contacts.upsert_by_channel, contacts.log_activity]
      required: false
      label: "CRM (optional)"
      hint: "Registrants become contacts; touches land on the activity timeline."
    - role: messaging
      kind: app
      compatible_app_names: [messaging]
      capabilities: [message.send]
      required: false
      label: "Messaging (optional)"
      hint: "Reminders fan out via email/SMS when bound."
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: webinars_create,           description: "Create a webinar; allocates a stream." }
    - { name: webinars_get,              description: "Snapshot + stream metrics + counts." }
    - { name: webinars_list,             description: "Filter by status/kind/scheduled-window." }
    - { name: webinars_update,           description: "Patch fields; reschedules reminders if scheduled_at moves." }
    - { name: webinars_delete,           description: "Cancel + tear down stream." }
    - { name: webinars_register,         description: "Add a registrant; CRM contact upsert when bound." }
    - { name: webinars_list_registrants, description: "List registrants with attendance status." }
    - { name: webinars_send_reminder,    description: "Fire a reminder now (manual override)." }
    - { name: webinars_define_offer,     description: "Script an offer at offset_seconds." }
    - { name: webinars_post_offer,       description: "Push an ad-hoc offer to the live room now." }
    - { name: webinars_push_poll,        description: "Open a poll." }
    - { name: webinars_publish_replay,   description: "Mark recording published; mint replay URL." }
    - { name: webinars_get_engagement,   description: "Funnel report: regs, attendance, watch %, CTRs." }
    - { name: webinars_close,            description: "End webinar — stop stream, snapshot." }
runtime:
  kind: source
  source: { repo: github.com/apteva/apps, ref: main, entry: mcp/webinars }
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/webinars.db
  migrations: migrations/
upgrade_policy: auto-patch
`

// ─── App ──────────────────────────────────────────────────────────

type App struct {
	// streamingCaller round-trips MCP calls to the streaming sidecar.
	// Tests inject a fake; production uses the SDK PlatformAPI.
	streamingCaller streamingCaller
	crmCaller       crmCaller       // nil-safe when CRM unbound
	messagingCaller messagingCaller // nil-safe when messaging unbound
}

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("webinars requires a db block")
	}
	globalCtx = ctx
	globalApp = a

	if a.streamingCaller == nil {
		a.streamingCaller = newPlatformStreamingCaller(ctx)
	}
	if a.crmCaller == nil {
		a.crmCaller = newPlatformCRMCaller(ctx)
	}
	if a.messagingCaller == nil {
		a.messagingCaller = newPlatformMessagingCaller(ctx)
	}

	// Reconciler: any in-flight live webinar across a restart needs
	// its status checked against streaming. Keep simple for v0.1: any
	// status=live row gets demoted to ended, since we lost the
	// in-process schedulers.
	if _, err := ctx.AppDB().Exec(
		`UPDATE webinars SET status='ended', ended_at = CURRENT_TIMESTAMP
		 WHERE status='live'`); err != nil {
		return fmt.Errorf("reconcile: %w", err)
	}

	ctx.Logger().Info("webinars mounted")
	return nil
}

func (a *App) OnUnmount(ctx *sdk.AppCtx) error    { return nil }
func (a *App) Channels() []sdk.ChannelFactory     { return nil }

func (a *App) EventHandlers() []sdk.EventHandler {
	return []sdk.EventHandler{
		// Lifecycle bridge: when streaming fires a stream.* event, see
		// if it's our stream and react.
		{Topic: "stream.started", Handler: a.handleStreamStarted},
		{Topic: "stream.ended", Handler: a.handleStreamEnded},
		{Topic: "stream.errored", Handler: a.handleStreamErrored},
	}
}

func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{
		{Name: "reminder-scheduler", Schedule: "@every 1m", Run: a.runReminderScheduler},
		{Name: "offer-broadcaster", Schedule: "@every 5s", Run: a.runOfferBroadcaster},
		{Name: "attendance-decay", Schedule: "@every 30s", Run: a.runAttendanceDecay},
	}
}

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		// Public funnel — NoAuth, identified by slug or join_token.
		{Pattern: "/r/", Handler: a.handleRegistrationPage, NoAuth: true},
		{Pattern: "/live/", Handler: a.handleLiveRoute, NoAuth: true},
		{Pattern: "/replay/", Handler: a.handleReplayPage, NoAuth: true},

		// Admin REST mirror for the dashboard panel.
		{Pattern: "/admin/webinars", Handler: a.handleAdminCollection},
		{Pattern: "/admin/webinars/", Handler: a.handleAdminItem},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "webinars_create",
			Description: "Create a webinar — allocates a stream via streaming.streams_create. Args: title, scheduled_at? (RFC3339), host_name?, kind? (live|scheduled|replay, default scheduled), duration_minutes? (default 60), description?.",
			InputSchema: schemaObject(map[string]any{
				"title":            map[string]any{"type": "string"},
				"scheduled_at":     map[string]any{"type": "string"},
				"host_name":        map[string]any{"type": "string"},
				"kind":             map[string]any{"type": "string"},
				"duration_minutes": map[string]any{"type": "integer"},
				"description":      map[string]any{"type": "string"},
			}, []string{"title"}),
			Handler: a.toolCreate,
		},
		{
			Name:        "webinars_get",
			Description: "Full webinar with stream metrics + counts. Args: id.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolGet,
		},
		{
			Name:        "webinars_list",
			Description: "Filter by status/kind/scheduled-window. Args: status?, kind?, scheduled_at_after?, scheduled_at_before?, limit? (default 50).",
			InputSchema: schemaObject(map[string]any{
				"status":              map[string]any{"type": "string"},
				"kind":                map[string]any{"type": "string"},
				"scheduled_at_after":  map[string]any{"type": "string"},
				"scheduled_at_before": map[string]any{"type": "string"},
				"limit":               map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolList,
		},
		{
			Name:        "webinars_update",
			Description: "Patch fields. Args: id, patch.",
			InputSchema: schemaObject(map[string]any{
				"id":    map[string]any{"type": "integer"},
				"patch": map[string]any{"type": "object"},
			}, []string{"id", "patch"}),
			Handler: a.toolUpdate,
		},
		{
			Name:        "webinars_delete",
			Description: "Cancel + tear down stream. Args: id.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolDelete,
		},
		{
			Name:        "webinars_register",
			Description: "Add a registrant. Creates a CRM contact if bound. Args: webinar_id, email?, phone?, display_name?, source? (form|agent|import).",
			InputSchema: schemaObject(map[string]any{
				"webinar_id":   map[string]any{"type": "integer"},
				"email":        map[string]any{"type": "string"},
				"phone":        map[string]any{"type": "string"},
				"display_name": map[string]any{"type": "string"},
				"source":       map[string]any{"type": "string"},
			}, []string{"webinar_id"}),
			Handler: a.toolRegister,
		},
		{
			Name:        "webinars_list_registrants",
			Description: "List registrants with attendance status. Args: webinar_id, attended?, limit? (default 100).",
			InputSchema: schemaObject(map[string]any{
				"webinar_id": map[string]any{"type": "integer"},
				"attended":   map[string]any{"type": "boolean"},
				"limit":      map[string]any{"type": "integer"},
			}, []string{"webinar_id"}),
			Handler: a.toolListRegistrants,
		},
		{
			Name:        "webinars_send_reminder",
			Description: "Fire a reminder now. Args: id, channel? (email|sms|all), audience? (all|registered|joined|no_show), body? (template override).",
			InputSchema: schemaObject(map[string]any{
				"id":       map[string]any{"type": "integer"},
				"channel":  map[string]any{"type": "string"},
				"audience": map[string]any{"type": "string"},
				"body":     map[string]any{"type": "string"},
			}, []string{"id"}),
			Handler: a.toolSendReminder,
		},
		{
			Name:        "webinars_define_offer",
			Description: "Script an offer at offset_seconds. Args: id, offset_seconds, headline, body?, cta_label, cta_url, duration_seconds? (default 30).",
			InputSchema: schemaObject(map[string]any{
				"id":               map[string]any{"type": "integer"},
				"offset_seconds":   map[string]any{"type": "integer"},
				"headline":         map[string]any{"type": "string"},
				"body":             map[string]any{"type": "string"},
				"cta_label":        map[string]any{"type": "string"},
				"cta_url":          map[string]any{"type": "string"},
				"duration_seconds": map[string]any{"type": "integer"},
			}, []string{"id", "offset_seconds", "headline", "cta_label", "cta_url"}),
			Handler: a.toolDefineOffer,
		},
		{
			Name:        "webinars_post_offer",
			Description: "Push an ad-hoc offer to the live room now. Args: id, headline, body?, cta_label, cta_url, duration_seconds? (default 30).",
			InputSchema: schemaObject(map[string]any{
				"id":               map[string]any{"type": "integer"},
				"headline":         map[string]any{"type": "string"},
				"body":             map[string]any{"type": "string"},
				"cta_label":        map[string]any{"type": "string"},
				"cta_url":          map[string]any{"type": "string"},
				"duration_seconds": map[string]any{"type": "integer"},
			}, []string{"id", "headline", "cta_label", "cta_url"}),
			Handler: a.toolPostOffer,
		},
		{
			Name:        "webinars_push_poll",
			Description: "Open a poll. Args: id, question, choices (array of strings), duration_seconds? (default 60).",
			InputSchema: schemaObject(map[string]any{
				"id":               map[string]any{"type": "integer"},
				"question":         map[string]any{"type": "string"},
				"choices":          map[string]any{"type": "array"},
				"duration_seconds": map[string]any{"type": "integer"},
			}, []string{"id", "question", "choices"}),
			Handler: a.toolPushPoll,
		},
		{
			Name:        "webinars_publish_replay",
			Description: "Mark recording published; mint replay URL. Args: id, expires_at? (RFC3339; null = no expiry).",
			InputSchema: schemaObject(map[string]any{
				"id":         map[string]any{"type": "integer"},
				"expires_at": map[string]any{"type": "string"},
			}, []string{"id"}),
			Handler: a.toolPublishReplay,
		},
		{
			Name:        "webinars_get_engagement",
			Description: "Funnel report. Args: id.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolGetEngagement,
		},
		{
			Name:        "webinars_close",
			Description: "End webinar — stop stream, snapshot final attendance. Args: id.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolClose,
		},
	}
}

func main() { sdk.Run(&App{}) }

// ─── Project resolution (CRM / streaming pattern) ─────────────────

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

// ─── Domain types ─────────────────────────────────────────────────

type Webinar struct {
	ID                 int64  `json:"id"`
	ProjectID          string `json:"project_id,omitempty"`
	Slug               string `json:"slug"`
	Title              string `json:"title"`
	HostName           string `json:"host_name,omitempty"`
	Description        string `json:"description,omitempty"`
	Kind               string `json:"kind"`
	ScheduledAt        string `json:"scheduled_at,omitempty"`
	DurationMinutes    int    `json:"duration_minutes"`
	Status             string `json:"status"`
	StreamID           int64  `json:"stream_id,omitempty"`
	RecordingPublished bool   `json:"recording_published"`
	ReplayToken        string `json:"replay_token,omitempty"`
	ReplayExpiresAt    string `json:"replay_expires_at,omitempty"`
	CreatedAt          string `json:"created_at"`
	StartedAt          string `json:"started_at,omitempty"`
	EndedAt            string `json:"ended_at,omitempty"`

	// Materialized at read time — not stored on the row.
	RegistrationURL string `json:"registration_url,omitempty"`
	IngestURL       string `json:"ingest_url,omitempty"`
	StreamKey       string `json:"stream_key,omitempty"`
	PlaybackURL     string `json:"playback_url,omitempty"`
	ReplayURL       string `json:"replay_url,omitempty"`
}

type Registrant struct {
	ID             int64  `json:"id"`
	WebinarID      int64  `json:"webinar_id"`
	ContactID      *int64 `json:"contact_id,omitempty"`
	Email          string `json:"email,omitempty"`
	Phone          string `json:"phone,omitempty"`
	DisplayName    string `json:"display_name,omitempty"`
	JoinToken      string `json:"join_token"`
	JoinURL        string `json:"join_url,omitempty"`
	RegisteredAt   string `json:"registered_at"`
	Source         string `json:"source,omitempty"`
	AttendedLive   bool   `json:"attended_live"`
	AttendedReplay bool   `json:"attended_replay"`
}

// ─── Shared globals (CRM/streaming pattern) ───────────────────────
//
// The SDK's HTTP handler signature is (w, r) only. Stash these at
// OnMount so the public-page handlers can reach them.
var (
	globalCtx *sdk.AppCtx
	globalApp *App
)

// ─── Tiny utilities ───────────────────────────────────────────────

func intArg(args map[string]any, key string, def int) int {
	if v, ok := args[key].(float64); ok {
		return int(v)
	}
	if v, ok := args[key].(int); ok {
		return v
	}
	if v, ok := args[key].(string); ok {
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
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
		if err != nil {
			return 0
		}
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

func boolArg(args map[string]any, key string, def bool) bool {
	if v, ok := args[key].(bool); ok {
		return v
	}
	return def
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func nullStr(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
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

// randomToken — URL-safe random string ≥32 chars.
func randomToken() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("rand.Read: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

// slugify produces a URL-safe slug from a title. Lowercase, strip
// non-alphanumerics → '-', collapse runs, strip leading/trailing '-'.
// Falls back to a random token when the title yields nothing.
func slugify(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	prevDash := false
	for _, r := range s {
		switch {
		case (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9'):
			b.WriteRune(r)
			prevDash = false
		default:
			if !prevDash {
				b.WriteRune('-')
				prevDash = true
			}
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return randomToken()[:12]
	}
	if len(out) > 80 {
		out = out[:80]
	}
	return out
}

// httpJSON writes a JSON response.
func httpJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// httpErr writes a JSON error response.
func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// publicURL returns the platform's public URL (Settings → Server),
// minus trailing slash. Empty when unconfigured (dev/local).
func (a *App) publicURL(ctx *sdk.AppCtx) string {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return ""
	}
	id, err := ctx.PlatformAPI().WhoAmI()
	if err != nil || id == nil {
		return ""
	}
	return strings.TrimRight(id.PublicURL, "/")
}

// publicAppPath — base URL prefix viewers use to reach this sidecar.
func (a *App) publicAppPath(ctx *sdk.AppCtx) string {
	host := a.publicURL(ctx)
	if host == "" {
		return "/api/apps/webinars"
	}
	return host + "/api/apps/webinars"
}

// reminderLeadHours parses the comma-separated config into a sorted
// slice of hours-before-start. Drops invalid entries silently.
func (a *App) reminderLeadHours(ctx *sdk.AppCtx) []float64 {
	raw := strings.TrimSpace(ctx.Config().Get("reminder_lead_hours"))
	if raw == "" {
		raw = "24,1,0.25"
	}
	out := []float64{}
	for _, part := range strings.Split(raw, ",") {
		f, err := strconv.ParseFloat(strings.TrimSpace(part), 64)
		if err != nil || f <= 0 {
			continue
		}
		out = append(out, f)
	}
	return out
}

// reminderLeadLabel — human-readable label for a lead-hours value.
func reminderLeadLabel(hours float64) string {
	switch {
	case hours >= 24:
		return fmt.Sprintf("T-%dh", int(hours))
	case hours >= 1:
		return fmt.Sprintf("T-%dh", int(hours))
	default:
		return fmt.Sprintf("T-%dm", int(hours*60))
	}
}

// suppressNonEmptyOr returns a if non-empty, else b.
func suppressNonEmptyOr(a, b string) string {
	if strings.TrimSpace(a) != "" {
		return a
	}
	return b
}

// minInt returns the smaller of two ints.
func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// maxInt returns the larger of two ints.
func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// silence unused imports during code-shuffling — strip me on cleanup.
var (
	_ = sync.Mutex{}
	_ = time.Now
	_ = errors.New
)
