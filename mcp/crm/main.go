// CRM v0.1 — contacts only.
//
// Multi-value channels, typed custom attributes with provenance,
// append-only activity log, soft-delete + merge. Every row is
// project-partitioned so the same code serves both `scope: project`
// (one install per project) and `scope: global` (one install across
// projects, isolation by project_id).
//
// The agent calls the MCP tools; the dashboard panel calls the REST
// surface. Both end up at the same DB layer through resolveProject(),
// which derives the project_id from either the install's env (project
// scope) or the calling agent / dashboard request (global scope).
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
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

// ─── Manifest (also lives in apteva.yaml; embedded so the running
// binary is self-describing for `crm --help` etc.) ─────────────────

const manifestYAML = `schema: apteva-app/v1
name: crm
display_name: CRM
version: 0.1.0
description: |
  Contacts store for Apteva agents and human teams. Multi-value channels,
  typed custom attributes with provenance, append-only activity log,
  soft-delete + merge.
author: Apteva
scopes: [project, global]
requires:
  permissions:
    - db.write.app
    - platform.instances.read
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - name: contacts_search
      description: Filtered contact search.
    - name: contacts_get
      description: Fetch one contact (snapshot only).
    - name: contacts_get_context
      description: Snapshot + recent activities + tags + attributes.
    - name: contacts_create
      description: Create a contact with channels, tags, and attributes.
    - name: contacts_update
      description: Partial-patch a contact.
    - name: contacts_upsert_by_channel
      description: Find-or-create by email or phone.
    - name: contacts_merge
      description: Merge one contact into another.
    - name: contacts_log_activity
      description: Append to a contact's timeline.
    - name: contacts_set_attribute
      description: Write one custom-attribute value with provenance.
    - name: contacts_define_attribute
      description: Create or update an attribute definition.
  ui_panels:
    - slot: instance.tab
      label: Contacts
      icon: contacts
      entry: /ui/ContactsPanel.html
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/crm
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/crm.db
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
		return errors.New("crm requires a db block")
	}
	// Stash the ctx so HTTP handlers — which the SDK invokes without
	// passing AppCtx — can reach it. The SDK's request signature is
	// (w, r) only; we keep the ctx in a package var until that gets
	// a request-scoped hook.
	globalCtx = ctx
	ctx.Logger().Info("crm mounted",
		"scope_project_id", os.Getenv("APTEVA_PROJECT_ID"))
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error          { return nil }
func (a *App) Channels() []sdk.ChannelFactory       { return nil }
func (a *App) Workers() []sdk.Worker                { return nil }
func (a *App) EventHandlers() []sdk.EventHandler    { return nil }

// ─── HTTP routes (REST surface for the dashboard panel) ────────────
//
// Reverse-proxied at /api/apps/crm/* by apteva-server. The dashboard
// passes ?project_id=<current> and ?instance_id=<id> on every URL;
// resolveProject() picks the right one based on install scope.

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		// Method-aware dispatcher for /contacts (collection-level GET/POST).
		{Pattern: "/contacts", Handler: a.handleHTTPContactsCollection},
		// Method-aware dispatcher for /contacts/<id>[/<sub>]. ServeMux
		// only routes by path; method is dispatched inside.
		{Pattern: "/contacts/", Handler: a.handleHTTPContactItem},
		{Pattern: "/attribute-defs", Handler: a.handleHTTPAttrDefs},
	}
}

// handleHTTPContactsCollection dispatches GET (search) / POST (create)
// at /contacts. The framework's per-route Method filter is bypassed
// here because we register one Pattern and want both verbs.
func (a *App) handleHTTPContactsCollection(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.handleHTTPSearch(w, r)
	case http.MethodPost:
		a.handleHTTPCreate(w, r)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleHTTPContactItem dispatches /contacts/<id> by method, and
// routes /contacts/<id>/activities to the activity sub-handlers.
func (a *App) handleHTTPContactItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/contacts/")
	parts := strings.SplitN(rest, "/", 2)
	if len(parts) == 2 && parts[1] == "activities" {
		switch r.Method {
		case http.MethodGet:
			a.handleHTTPGetOrChild(w, r) // also handles activities GET
		case http.MethodPost:
			a.handleHTTPPostActivity(w, r)
		default:
			httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		}
		return
	}
	switch r.Method {
	case http.MethodGet:
		a.handleHTTPGetOrChild(w, r)
	case http.MethodPatch:
		a.handleHTTPUpdate(w, r)
	case http.MethodDelete:
		a.handleHTTPArchive(w, r)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleHTTPAttrDefs dispatches GET (list) / POST (define) on
// /attribute-defs.
func (a *App) handleHTTPAttrDefs(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		a.handleHTTPListAttrDefs(w, r)
	case http.MethodPost:
		a.handleHTTPCreateAttrDef(w, r)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// handleHTTPPostActivity creates an activity via POST. Body:
// { kind, body, occurred_at?, source? }.
func (a *App) handleHTTPPostActivity(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/contacts/")
	parts := strings.SplitN(rest, "/", 2)
	id, _ := strconv.ParseInt(parts[0], 10, 64)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	kind, _ := body["kind"].(string)
	bodyText, _ := body["body"].(string)
	if kind == "" || bodyText == "" {
		httpErr(w, http.StatusBadRequest, "kind and body required")
		return
	}
	occurred, _ := body["occurred_at"].(string)
	if occurred == "" {
		occurred = time.Now().UTC().Format(time.RFC3339)
	}
	source, _ := body["source"].(string)
	act, err := dbLogActivity(ctx.AppDB(), pid, id, kind, bodyText, occurred, source)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"activity": act})
}

// ─── MCP tools (the agent's surface) ───────────────────────────────

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name: "contacts_search",
			Description: "Filtered contact search. Args: filters [{field,op,value}], q (free text), limit (default 50, max 200).",
			InputSchema: schemaObject(map[string]any{
				"filters": map[string]any{"type": "array"},
				"q":       map[string]any{"type": "string"},
				"limit":   map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolSearch,
		},
		{
			Name:        "contacts_get",
			Description: "Fetch one contact (snapshot only). Args: id OR email OR phone.",
			InputSchema: schemaObject(map[string]any{
				"id":    map[string]any{"type": "integer"},
				"email": map[string]any{"type": "string"},
				"phone": map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolGet,
		},
		{
			Name:        "contacts_get_context",
			Description: "Snapshot + recent activities + tags + attributes — the agent's pre-flight read. Args: id OR email OR phone, activity_limit (default 10).",
			InputSchema: schemaObject(map[string]any{
				"id":             map[string]any{"type": "integer"},
				"email":          map[string]any{"type": "string"},
				"phone":          map[string]any{"type": "string"},
				"activity_limit": map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolGetContext,
		},
		{
			Name:        "contacts_create",
			Description: "Create a contact. Args: first_name, last_name, display_name, company, job_title, channels [{kind,value,label,is_primary}], tags [string], attributes [{key,value}], source.",
			InputSchema: schemaObject(map[string]any{
				"first_name":   map[string]any{"type": "string"},
				"last_name":    map[string]any{"type": "string"},
				"display_name": map[string]any{"type": "string"},
				"pronouns":     map[string]any{"type": "string"},
				"company":      map[string]any{"type": "string"},
				"job_title":    map[string]any{"type": "string"},
				"channels":     map[string]any{"type": "array"},
				"tags":         map[string]any{"type": "array"},
				"attributes":   map[string]any{"type": "array"},
				"source":       map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolCreate,
		},
		{
			Name:        "contacts_update",
			Description: "Partial-patch a contact. Args: id, patch (any subset of contact fields), source.",
			InputSchema: schemaObject(map[string]any{
				"id":     map[string]any{"type": "integer"},
				"patch":  map[string]any{"type": "object"},
				"source": map[string]any{"type": "string"},
			}, []string{"id", "patch"}),
			Handler: a.toolUpdate,
		},
		{
			Name:        "contacts_upsert_by_channel",
			Description: "Find-or-create by email/phone. Returns {contact, was_created}. Args: kind (email|phone), value, defaults (subset of contact fields used only on create), source.",
			InputSchema: schemaObject(map[string]any{
				"kind":     map[string]any{"type": "string"},
				"value":    map[string]any{"type": "string"},
				"defaults": map[string]any{"type": "object"},
				"source":   map[string]any{"type": "string"},
			}, []string{"kind", "value"}),
			Handler: a.toolUpsertByChannel,
		},
		{
			Name:        "contacts_merge",
			Description: "Merge loser_id into winner_id. Channels and attributes are reassigned, loser is marked status=merged. Args: loser_id, winner_id, notes.",
			InputSchema: schemaObject(map[string]any{
				"loser_id":  map[string]any{"type": "integer"},
				"winner_id": map[string]any{"type": "integer"},
				"notes":     map[string]any{"type": "string"},
				"source":    map[string]any{"type": "string"},
			}, []string{"loser_id", "winner_id"}),
			Handler: a.toolMerge,
		},
		{
			Name:        "contacts_log_activity",
			Description: "Append a row to a contact's timeline. Args: contact_id, kind (email_sent|email_received|call|meeting|note|system), body, occurred_at (RFC3339, default now), source.",
			InputSchema: schemaObject(map[string]any{
				"contact_id":  map[string]any{"type": "integer"},
				"kind":        map[string]any{"type": "string"},
				"body":        map[string]any{"type": "string"},
				"occurred_at": map[string]any{"type": "string"},
				"source":      map[string]any{"type": "string"},
			}, []string{"contact_id", "kind", "body"}),
			Handler: a.toolLogActivity,
		},
		{
			Name:        "contacts_set_attribute",
			Description: "Write one custom-attribute value. Args: contact_id, key, value, source.",
			InputSchema: schemaObject(map[string]any{
				"contact_id": map[string]any{"type": "integer"},
				"key":        map[string]any{"type": "string"},
				"value":      map[string]any{},
				"source":     map[string]any{"type": "string"},
			}, []string{"contact_id", "key", "value"}),
			Handler: a.toolSetAttribute,
		},
		{
			Name:        "contacts_define_attribute",
			Description: "Create or update an attribute definition. Args: key, label, type (text|number|date|bool|select|multi_select|url), enum_values (when type ∈ select/multi_select).",
			InputSchema: schemaObject(map[string]any{
				"key":         map[string]any{"type": "string"},
				"label":       map[string]any{"type": "string"},
				"type":        map[string]any{"type": "string"},
				"enum_values": map[string]any{"type": "array"},
				"required":    map[string]any{"type": "boolean"},
				"sort_order":  map[string]any{"type": "integer"},
			}, []string{"key", "label", "type"}),
			Handler: a.toolDefineAttribute,
		},
	}
}

func main() { sdk.Run(&App{}) }

// ─── Project resolution ────────────────────────────────────────────
//
// resolveProject picks the partition key for this call.
//
//   - `scope: project` install — APTEVA_PROJECT_ID env is set at boot.
//     Every call uses it. Args/headers ignored as a defensive measure.
//   - `scope: global` install — env is empty. The caller MUST pass
//     project_id explicitly: agents do it via the `_project_id` arg
//     the platform injects; dashboard requests do it via ?project_id.
//
// Returning ("", error) is the safe-default — handlers refuse to
// touch the DB without a partition key.

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

// ─── Domain types ──────────────────────────────────────────────────

type Contact struct {
	ID              int64    `json:"id"`
	ProjectID       string   `json:"project_id,omitempty"`
	FirstName       string   `json:"first_name,omitempty"`
	LastName        string   `json:"last_name,omitempty"`
	DisplayName     string   `json:"display_name,omitempty"`
	Pronouns        string   `json:"pronouns,omitempty"`
	PrimaryEmail    string   `json:"primary_email,omitempty"`
	PrimaryPhone    string   `json:"primary_phone,omitempty"`
	Company         string   `json:"company,omitempty"`
	JobTitle        string   `json:"job_title,omitempty"`
	OwnerUserID     *int64   `json:"owner_user_id,omitempty"`
	Status          string   `json:"status"`
	Source          string   `json:"source,omitempty"`
	FirstContactAt  string   `json:"first_contact_at,omitempty"`
	LastContactAt   string   `json:"last_contact_at,omitempty"`
	CreatedAt       string   `json:"created_at,omitempty"`
	UpdatedAt       string   `json:"updated_at,omitempty"`
	Channels        []Channel `json:"channels,omitempty"`
	Tags            []string  `json:"tags,omitempty"`
	Attributes      []Attribute `json:"attributes,omitempty"`
}

type Channel struct {
	ID         int64  `json:"id,omitempty"`
	Kind       string `json:"kind"`
	Value      string `json:"value"`
	Label      string `json:"label,omitempty"`
	IsPrimary  bool   `json:"is_primary,omitempty"`
	VerifiedAt string `json:"verified_at,omitempty"`
	Source     string `json:"source,omitempty"`
}

type Attribute struct {
	Key         string `json:"key"`
	Label       string `json:"label,omitempty"`
	Type        string `json:"type,omitempty"`
	Value       any    `json:"value"`
	Source      string `json:"source,omitempty"`
	SourceDetail string `json:"source_detail,omitempty"`
	SetAt       string `json:"set_at,omitempty"`
}

type Activity struct {
	ID          int64  `json:"id"`
	ContactID   int64  `json:"contact_id"`
	Kind        string `json:"kind"`
	Body        string `json:"body"`
	OccurredAt  string `json:"occurred_at"`
	Source      string `json:"source,omitempty"`
}

// ─── Tool handlers ─────────────────────────────────────────────────

func (a *App) toolSearch(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	q, _ := args["q"].(string)
	limit := intArg(args, "limit", 50)
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	filters, _ := args["filters"].([]any)
	rows, err := dbSearch(ctx.AppDB(), pid, q, filters, limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{"contacts": rows, "count": len(rows)}, nil
}

func (a *App) toolGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	c, err := lookupContact(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return map[string]any{"contact": nil, "found": false}, nil
	}
	if err := loadChannels(ctx.AppDB(), c); err != nil {
		return nil, err
	}
	return map[string]any{"contact": c, "found": true}, nil
}

func (a *App) toolGetContext(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	c, err := lookupContact(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	if c == nil {
		return map[string]any{"contact": nil, "found": false}, nil
	}
	if err := loadChannels(ctx.AppDB(), c); err != nil {
		return nil, err
	}
	if err := loadTags(ctx.AppDB(), c); err != nil {
		return nil, err
	}
	if err := loadAttributes(ctx.AppDB(), c); err != nil {
		return nil, err
	}
	limit := intArg(args, "activity_limit", 10)
	activities, err := dbActivities(ctx.AppDB(), pid, c.ID, limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"contact":    c,
		"activities": activities,
		"found":      true,
	}, nil
}

func (a *App) toolCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	c, err := dbCreate(ctx.AppDB(), pid, args)
	if err != nil {
		return nil, err
	}
	if err := loadChannels(ctx.AppDB(), c); err != nil {
		return nil, err
	}
	return map[string]any{"contact": c}, nil
}

func (a *App) toolUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	patch, _ := args["patch"].(map[string]any)
	if id == 0 || patch == nil {
		return nil, errors.New("id and patch required")
	}
	source, _ := args["source"].(string)
	c, err := dbUpdate(ctx.AppDB(), pid, id, patch, source)
	if err != nil {
		return nil, err
	}
	if err := loadChannels(ctx.AppDB(), c); err != nil {
		return nil, err
	}
	return map[string]any{"contact": c}, nil
}

func (a *App) toolUpsertByChannel(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	kind, _ := args["kind"].(string)
	value, _ := args["value"].(string)
	if kind == "" || value == "" {
		return nil, errors.New("kind and value required")
	}
	defaults, _ := args["defaults"].(map[string]any)
	source, _ := args["source"].(string)
	c, created, err := dbUpsertByChannel(ctx.AppDB(), pid, kind, value, defaults, source)
	if err != nil {
		return nil, err
	}
	if err := loadChannels(ctx.AppDB(), c); err != nil {
		return nil, err
	}
	return map[string]any{"contact": c, "was_created": created}, nil
}

func (a *App) toolMerge(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	loser := int64Arg(args, "loser_id")
	winner := int64Arg(args, "winner_id")
	if loser == 0 || winner == 0 || loser == winner {
		return nil, errors.New("loser_id and winner_id required and must differ")
	}
	notes, _ := args["notes"].(string)
	source, _ := args["source"].(string)
	if err := dbMerge(ctx.AppDB(), pid, loser, winner, notes, source); err != nil {
		return nil, err
	}
	return map[string]any{"merged": true, "winner_id": winner, "loser_id": loser}, nil
}

func (a *App) toolLogActivity(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	cid := int64Arg(args, "contact_id")
	kind, _ := args["kind"].(string)
	body, _ := args["body"].(string)
	if cid == 0 || kind == "" || body == "" {
		return nil, errors.New("contact_id, kind, body required")
	}
	occurred, _ := args["occurred_at"].(string)
	if occurred == "" {
		occurred = time.Now().UTC().Format(time.RFC3339)
	}
	source, _ := args["source"].(string)
	act, err := dbLogActivity(ctx.AppDB(), pid, cid, kind, body, occurred, source)
	if err != nil {
		return nil, err
	}
	return map[string]any{"activity": act}, nil
}

func (a *App) toolSetAttribute(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	cid := int64Arg(args, "contact_id")
	key, _ := args["key"].(string)
	if cid == 0 || key == "" {
		return nil, errors.New("contact_id and key required")
	}
	source, _ := args["source"].(string)
	if err := dbSetAttribute(ctx.AppDB(), pid, cid, key, args["value"], source); err != nil {
		return nil, err
	}
	return map[string]any{"ok": true}, nil
}

func (a *App) toolDefineAttribute(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	key, _ := args["key"].(string)
	label, _ := args["label"].(string)
	typ, _ := args["type"].(string)
	if key == "" || label == "" || typ == "" {
		return nil, errors.New("key, label, type required")
	}
	enumValues, _ := args["enum_values"].([]any)
	required, _ := args["required"].(bool)
	sortOrder := intArg(args, "sort_order", 0)
	def, err := dbDefineAttribute(ctx.AppDB(), pid, key, label, typ, enumValues, required, sortOrder)
	if err != nil {
		return nil, err
	}
	return map[string]any{"attribute_def": def}, nil
}

// ─── HTTP handlers (delegate to the same DB helpers) ───────────────

func (a *App) handleHTTPSearch(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	q := r.URL.Query().Get("q")
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := dbSearch(ctx.AppDB(), pid, q, nil, limit)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"contacts": rows})
}

func (a *App) handleHTTPCreate(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	c, err := dbCreate(ctx.AppDB(), pid, body)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if err := loadChannels(ctx.AppDB(), c); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"contact": c})
}

// /contacts/<id> — split here between detail GET and the activities sub-route.
func (a *App) handleHTTPGetOrChild(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/contacts/")
	parts := strings.SplitN(rest, "/", 2)
	id, _ := strconv.ParseInt(parts[0], 10, 64)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	if len(parts) == 2 && parts[1] == "activities" {
		acts, err := dbActivities(ctx.AppDB(), pid, id, 50)
		if err != nil {
			httpErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		httpJSON(w, map[string]any{"activities": acts})
		return
	}
	c, err := dbGetByID(ctx.AppDB(), pid, id)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	if c == nil {
		httpErr(w, http.StatusNotFound, "not found")
		return
	}
	_ = loadChannels(ctx.AppDB(), c)
	_ = loadTags(ctx.AppDB(), c)
	_ = loadAttributes(ctx.AppDB(), c)
	httpJSON(w, map[string]any{"contact": c})
}

func (a *App) handleHTTPUpdate(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id, _ := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/contacts/"), 10, 64)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	var patch map[string]any
	if err := json.NewDecoder(r.Body).Decode(&patch); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	source, _ := patch["source"].(string)
	delete(patch, "source")
	c, err := dbUpdate(ctx.AppDB(), pid, id, patch, source)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"contact": c})
}

func (a *App) handleHTTPArchive(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id, _ := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/contacts/"), 10, 64)
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	if _, err := ctx.AppDB().Exec(
		`UPDATE contacts SET status='archived', updated_at = CURRENT_TIMESTAMP, deleted_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND project_id = ?`, id, pid); err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"archived": true})
}

func (a *App) handleHTTPListAttrDefs(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	defs, err := dbListAttrDefs(ctx.AppDB(), pid)
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"attribute_defs": defs})
}

func (a *App) handleHTTPCreateAttrDef(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	var body map[string]any
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return
	}
	key, _ := body["key"].(string)
	label, _ := body["label"].(string)
	typ, _ := body["type"].(string)
	if key == "" || label == "" || typ == "" {
		httpErr(w, http.StatusBadRequest, "key, label, type required")
		return
	}
	enumValues, _ := body["enum_values"].([]any)
	required, _ := body["required"].(bool)
	sort, _ := body["sort_order"].(float64)
	def, err := dbDefineAttribute(ctx.AppDB(), pid, key, label, typ, enumValues, required, int(sort))
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	httpJSON(w, map[string]any{"attribute_def": def})
}

// ─── DB helpers ────────────────────────────────────────────────────

func dbCreate(db *sql.DB, pid string, args map[string]any) (*Contact, error) {
	c := &Contact{
		ProjectID:   pid,
		FirstName:   strArg(args, "first_name"),
		LastName:    strArg(args, "last_name"),
		DisplayName: strArg(args, "display_name"),
		Pronouns:    strArg(args, "pronouns"),
		Company:     strArg(args, "company"),
		JobTitle:    strArg(args, "job_title"),
		Status:      "active",
		Source:      strArg(args, "source"),
	}
	now := time.Now().UTC().Format(time.RFC3339)

	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(
		`INSERT INTO contacts (project_id, first_name, last_name, display_name, pronouns,
			company, job_title, status, source, created_at, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		c.ProjectID, c.FirstName, c.LastName, c.DisplayName, c.Pronouns,
		c.Company, c.JobTitle, c.Status, c.Source, now, now)
	if err != nil {
		return nil, err
	}
	c.ID, _ = res.LastInsertId()
	c.CreatedAt = now
	c.UpdatedAt = now

	// Channels.
	channels, _ := args["channels"].([]any)
	for _, ch := range channels {
		m, ok := ch.(map[string]any)
		if !ok {
			continue
		}
		kind, _ := m["kind"].(string)
		value, _ := m["value"].(string)
		if kind == "" || value == "" {
			continue
		}
		label, _ := m["label"].(string)
		isPrimary := false
		if v, ok := m["is_primary"].(bool); ok {
			isPrimary = v
		}
		if _, err := tx.Exec(
			`INSERT OR IGNORE INTO contact_channels
				(project_id, contact_id, kind, value, label, is_primary, source)
			 VALUES (?, ?, ?, ?, ?, ?, ?)`,
			pid, c.ID, kind, normaliseChannel(kind, value), label, boolToInt(isPrimary), c.Source); err != nil {
			return nil, err
		}
		// Mirror the primary email/phone onto the contact row for fast index seeks.
		if isPrimary {
			switch kind {
			case "email":
				tx.Exec(`UPDATE contacts SET primary_email = ? WHERE id = ?`,
					normaliseChannel(kind, value), c.ID)
				c.PrimaryEmail = normaliseChannel(kind, value)
			case "phone":
				tx.Exec(`UPDATE contacts SET primary_phone = ? WHERE id = ?`,
					normaliseChannel(kind, value), c.ID)
				c.PrimaryPhone = normaliseChannel(kind, value)
			}
		}
	}

	// Tags.
	tags, _ := args["tags"].([]any)
	for _, t := range tags {
		name, ok := t.(string)
		if !ok || name == "" {
			continue
		}
		tx.Exec(`INSERT OR IGNORE INTO contact_tags (project_id, contact_id, tag_name) VALUES (?, ?, ?)`,
			pid, c.ID, name)
	}

	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return c, nil
}

func dbGetByID(db *sql.DB, pid string, id int64) (*Contact, error) {
	c := &Contact{}
	var ownerID sql.NullInt64
	var first, last, dn, pron, pe, pp, comp, jt, src, fc, lc sql.NullString
	err := db.QueryRow(
		`SELECT id, project_id, first_name, last_name, display_name, pronouns,
			primary_email, primary_phone, company, job_title, owner_user_id,
			status, source, first_contact_at, last_contact_at, created_at, updated_at
		 FROM contacts WHERE id = ? AND project_id = ? AND deleted_at IS NULL`,
		id, pid).Scan(
		&c.ID, &c.ProjectID, &first, &last, &dn, &pron,
		&pe, &pp, &comp, &jt, &ownerID,
		&c.Status, &src, &fc, &lc, &c.CreatedAt, &c.UpdatedAt)
	if err == sql.ErrNoRows {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	c.FirstName = first.String
	c.LastName = last.String
	c.DisplayName = dn.String
	c.Pronouns = pron.String
	c.PrimaryEmail = pe.String
	c.PrimaryPhone = pp.String
	c.Company = comp.String
	c.JobTitle = jt.String
	c.Source = src.String
	c.FirstContactAt = fc.String
	c.LastContactAt = lc.String
	if ownerID.Valid {
		v := ownerID.Int64
		c.OwnerUserID = &v
	}
	return c, nil
}

func lookupContact(db *sql.DB, pid string, args map[string]any) (*Contact, error) {
	if id := int64Arg(args, "id"); id != 0 {
		return dbGetByID(db, pid, id)
	}
	if email, _ := args["email"].(string); email != "" {
		return dbGetByPrimary(db, pid, "email", normaliseChannel("email", email))
	}
	if phone, _ := args["phone"].(string); phone != "" {
		return dbGetByPrimary(db, pid, "phone", normaliseChannel("phone", phone))
	}
	return nil, errors.New("id, email, or phone required")
}

func dbGetByPrimary(db *sql.DB, pid, kind, value string) (*Contact, error) {
	col := "primary_email"
	if kind == "phone" {
		col = "primary_phone"
	}
	row := db.QueryRow(
		`SELECT id FROM contacts WHERE project_id = ? AND `+col+` = ? AND deleted_at IS NULL LIMIT 1`,
		pid, value)
	var id int64
	if err := row.Scan(&id); err != nil {
		if err == sql.ErrNoRows {
			// Fall back through contact_channels in case the primary
			// flag drifted.
			row = db.QueryRow(
				`SELECT contact_id FROM contact_channels
				 WHERE project_id = ? AND kind = ? AND value = ? LIMIT 1`,
				pid, kind, value)
			if err := row.Scan(&id); err != nil {
				if err == sql.ErrNoRows {
					return nil, nil
				}
				return nil, err
			}
		} else {
			return nil, err
		}
	}
	return dbGetByID(db, pid, id)
}

func dbSearch(db *sql.DB, pid, q string, filters []any, limit int) ([]*Contact, error) {
	where := []string{"project_id = ?", "deleted_at IS NULL", "status != 'merged'"}
	args := []any{pid}

	if q = strings.TrimSpace(q); q != "" {
		// Free-text against name fragments + email/phone via the
		// denormalised columns. Cheap enough on tens of thousands of
		// rows; if it gets slow we add FTS5 in v0.2.
		where = append(where, `(
			COALESCE(first_name,'') LIKE ? OR
			COALESCE(last_name,'')  LIKE ? OR
			COALESCE(display_name,'') LIKE ? OR
			COALESCE(primary_email,'') LIKE ? OR
			COALESCE(primary_phone,'') LIKE ? OR
			COALESCE(company,'') LIKE ?
		)`)
		like := "%" + strings.ToLower(q) + "%"
		args = append(args, like, like, like, like, like, like)
	}

	for _, f := range filters {
		m, ok := f.(map[string]any)
		if !ok {
			continue
		}
		field, _ := m["field"].(string)
		op, _ := m["op"].(string)
		val := m["value"]
		clause, params, err := buildFilterClause(field, op, val)
		if err != nil {
			return nil, err
		}
		where = append(where, clause)
		args = append(args, params...)
	}

	q2 := `SELECT id FROM contacts WHERE ` + strings.Join(where, " AND ") +
		` ORDER BY updated_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := db.Query(q2, args...)
	if err != nil {
		return nil, err
	}
	// Drain ids first, THEN do the per-row dbGetByID calls. Holding
	// rows open while issuing nested queries on the same *sql.DB
	// stalls the modernc/sqlite driver — the outer iterator and the
	// inner QueryRow contend for the same connection.
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			continue
		}
		ids = append(ids, id)
	}
	rows.Close()

	out := []*Contact{}
	for _, id := range ids {
		c, err := dbGetByID(db, pid, id)
		if err != nil || c == nil {
			continue
		}
		out = append(out, c)
	}
	return out, nil
}

// buildFilterClause translates a single Pipedrive-style filter into a
// SQL WHERE fragment. Allow-listed fields only — never interpolate
// raw user input into SQL identifiers.
func buildFilterClause(field, op string, val any) (string, []any, error) {
	allowed := map[string]bool{
		"first_name": true, "last_name": true, "display_name": true,
		"company": true, "job_title": true, "primary_email": true,
		"primary_phone": true, "status": true, "owner_user_id": true,
		"source": true,
	}
	if !allowed[field] {
		return "", nil, fmt.Errorf("unknown filter field %q", field)
	}
	switch op {
	case "eq", "":
		return field + " = ?", []any{val}, nil
	case "neq":
		return field + " != ?", []any{val}, nil
	case "gt":
		return field + " > ?", []any{val}, nil
	case "gte":
		return field + " >= ?", []any{val}, nil
	case "lt":
		return field + " < ?", []any{val}, nil
	case "lte":
		return field + " <= ?", []any{val}, nil
	case "contains":
		return field + " LIKE ?", []any{"%" + fmt.Sprint(val) + "%"}, nil
	case "starts_with":
		return field + " LIKE ?", []any{fmt.Sprint(val) + "%"}, nil
	case "is_null":
		return field + " IS NULL", nil, nil
	case "in":
		arr, ok := val.([]any)
		if !ok || len(arr) == 0 {
			return "", nil, errors.New("in op requires non-empty array")
		}
		placeholders := strings.Repeat("?,", len(arr))
		placeholders = placeholders[:len(placeholders)-1]
		return field + " IN (" + placeholders + ")", arr, nil
	}
	return "", nil, fmt.Errorf("unknown op %q", op)
}

func dbUpdate(db *sql.DB, pid string, id int64, patch map[string]any, source string) (*Contact, error) {
	allowed := map[string]bool{
		"first_name": true, "last_name": true, "display_name": true, "pronouns": true,
		"company": true, "job_title": true, "owner_user_id": true,
		"primary_email": true, "primary_phone": true,
		"status": true, "first_contact_at": true, "last_contact_at": true,
	}
	sets := []string{}
	args := []any{}
	for k, v := range patch {
		if !allowed[k] {
			continue
		}
		sets = append(sets, k+" = ?")
		args = append(args, v)
	}
	if len(sets) == 0 {
		return dbGetByID(db, pid, id)
	}
	sets = append(sets, "updated_at = ?")
	args = append(args, time.Now().UTC().Format(time.RFC3339))
	if source != "" {
		sets = append(sets, "source = ?")
		args = append(args, source)
	}
	args = append(args, id, pid)
	if _, err := db.Exec(
		`UPDATE contacts SET `+strings.Join(sets, ", ")+` WHERE id = ? AND project_id = ?`,
		args...); err != nil {
		return nil, err
	}
	return dbGetByID(db, pid, id)
}

func dbUpsertByChannel(db *sql.DB, pid, kind, value string, defaults map[string]any, source string) (*Contact, bool, error) {
	value = normaliseChannel(kind, value)

	// Fast path: do we already have it?
	c, err := dbGetByPrimary(db, pid, kind, value)
	if err != nil {
		return nil, false, err
	}
	if c != nil {
		return c, false, nil
	}

	// Create. Build args by merging defaults + this channel as primary.
	args := map[string]any{}
	for k, v := range defaults {
		args[k] = v
	}
	args["source"] = source
	args["channels"] = []any{
		map[string]any{
			"kind":       kind,
			"value":      value,
			"is_primary": true,
		},
	}
	c, err = dbCreate(db, pid, args)
	if err != nil {
		return nil, false, err
	}
	return c, true, nil
}

func dbMerge(db *sql.DB, pid string, loserID, winnerID int64, notes, source string) error {
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	// Verify both contacts belong to this project.
	var n int
	if err := tx.QueryRow(
		`SELECT COUNT(*) FROM contacts WHERE id IN (?, ?) AND project_id = ?`,
		loserID, winnerID, pid).Scan(&n); err != nil {
		return err
	}
	if n != 2 {
		return errors.New("loser_id or winner_id not found in this project")
	}

	// Move channels (skip on dup-key conflicts — winner already has it).
	tx.Exec(
		`UPDATE OR IGNORE contact_channels SET contact_id = ? WHERE contact_id = ? AND project_id = ?`,
		winnerID, loserID, pid)
	tx.Exec(
		`DELETE FROM contact_channels WHERE contact_id = ? AND project_id = ?`,
		loserID, pid)

	// Move attributes (winner wins on key collision via ON CONFLICT-style ignore).
	tx.Exec(
		`UPDATE OR IGNORE contact_attributes SET contact_id = ? WHERE contact_id = ? AND project_id = ?`,
		winnerID, loserID, pid)
	tx.Exec(
		`DELETE FROM contact_attributes WHERE contact_id = ? AND project_id = ?`,
		loserID, pid)

	// Move tags (winner inherits the union).
	tx.Exec(
		`INSERT OR IGNORE INTO contact_tags (project_id, contact_id, tag_name)
		 SELECT project_id, ?, tag_name FROM contact_tags
		 WHERE contact_id = ? AND project_id = ?`,
		winnerID, loserID, pid)

	// Move activities — preserved as winner's history.
	tx.Exec(
		`UPDATE contact_activities SET contact_id = ? WHERE contact_id = ? AND project_id = ?`,
		winnerID, loserID, pid)

	// Mark loser as merged. Activities are kept; archive/soft-delete
	// would lose timeline — explicit 'merged' status is clearer.
	tx.Exec(
		`UPDATE contacts SET status='merged', updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND project_id = ?`,
		loserID, pid)

	tx.Exec(
		`INSERT INTO contact_merges (project_id, loser_id, winner_id, source, notes)
		 VALUES (?, ?, ?, ?, ?)`,
		pid, loserID, winnerID, source, notes)

	return tx.Commit()
}

func dbLogActivity(db *sql.DB, pid string, contactID int64, kind, body, occurredAt, source string) (*Activity, error) {
	res, err := db.Exec(
		`INSERT INTO contact_activities (project_id, contact_id, kind, body, occurred_at, source)
		 VALUES (?, ?, ?, ?, ?, ?)`,
		pid, contactID, kind, body, occurredAt, source)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	// Bump last_contact_at on the contact row for fast "who haven't I
	// talked to in 30 days" queries.
	db.Exec(
		`UPDATE contacts SET last_contact_at = ?, updated_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND project_id = ?`,
		occurredAt, contactID, pid)
	return &Activity{
		ID: id, ContactID: contactID, Kind: kind, Body: body,
		OccurredAt: occurredAt, Source: source,
	}, nil
}

func dbActivities(db *sql.DB, pid string, contactID int64, limit int) ([]*Activity, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	rows, err := db.Query(
		`SELECT id, contact_id, kind, body, occurred_at, COALESCE(source,'')
		 FROM contact_activities
		 WHERE project_id = ? AND contact_id = ?
		 ORDER BY occurred_at DESC LIMIT ?`,
		pid, contactID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Activity{}
	for rows.Next() {
		a := &Activity{}
		if err := rows.Scan(&a.ID, &a.ContactID, &a.Kind, &a.Body, &a.OccurredAt, &a.Source); err == nil {
			out = append(out, a)
		}
	}
	return out, nil
}

func dbSetAttribute(db *sql.DB, pid string, contactID int64, key string, value any, source string) error {
	// Resolve def.
	var defID int64
	var typ string
	err := db.QueryRow(
		`SELECT id, type FROM contact_attribute_defs WHERE project_id = ? AND key = ?`,
		pid, key).Scan(&defID, &typ)
	if err == sql.ErrNoRows {
		return fmt.Errorf("attribute %q not defined — call contacts_define_attribute first", key)
	}
	if err != nil {
		return err
	}
	var vt sql.NullString
	var vn sql.NullFloat64
	var vd sql.NullString
	var vb sql.NullBool
	switch typ {
	case "text", "url", "select":
		vt = sql.NullString{String: fmt.Sprint(value), Valid: value != nil}
	case "number":
		switch v := value.(type) {
		case float64:
			vn = sql.NullFloat64{Float64: v, Valid: true}
		case int:
			vn = sql.NullFloat64{Float64: float64(v), Valid: true}
		case int64:
			vn = sql.NullFloat64{Float64: float64(v), Valid: true}
		}
	case "date":
		vd = sql.NullString{String: fmt.Sprint(value), Valid: value != nil}
	case "bool":
		if v, ok := value.(bool); ok {
			vb = sql.NullBool{Bool: v, Valid: true}
		}
	case "multi_select":
		// Store as JSON in value_text for v0.1.
		raw, _ := json.Marshal(value)
		vt = sql.NullString{String: string(raw), Valid: true}
	}
	_, err = db.Exec(
		`INSERT INTO contact_attributes
			(project_id, contact_id, def_id, value_text, value_number, value_date, value_bool, source, set_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(contact_id, def_id) DO UPDATE SET
			value_text = excluded.value_text,
			value_number = excluded.value_number,
			value_date = excluded.value_date,
			value_bool = excluded.value_bool,
			source = excluded.source,
			set_at = CURRENT_TIMESTAMP`,
		pid, contactID, defID, vt, vn, vd, vb, source)
	return err
}

func dbDefineAttribute(db *sql.DB, pid, key, label, typ string, enumValues []any, required bool, sortOrder int) (map[string]any, error) {
	enumJSON := ""
	if len(enumValues) > 0 {
		raw, _ := json.Marshal(enumValues)
		enumJSON = string(raw)
	}
	_, err := db.Exec(
		`INSERT INTO contact_attribute_defs (project_id, key, label, type, enum_values, required, sort_order)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(project_id, key) DO UPDATE SET
			label = excluded.label,
			type = excluded.type,
			enum_values = excluded.enum_values,
			required = excluded.required,
			sort_order = excluded.sort_order`,
		pid, key, label, typ, nullStr(enumJSON), boolToInt(required), sortOrder)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"key": key, "label": label, "type": typ,
		"enum_values": enumValues, "required": required, "sort_order": sortOrder,
	}, nil
}

func dbListAttrDefs(db *sql.DB, pid string) ([]map[string]any, error) {
	rows, err := db.Query(
		`SELECT key, label, type, COALESCE(enum_values,''), required, sort_order, is_system
		 FROM contact_attribute_defs WHERE project_id = ? ORDER BY sort_order, key`,
		pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var key, label, typ, enumStr string
		var required, isSystem int
		var sortOrder int
		if err := rows.Scan(&key, &label, &typ, &enumStr, &required, &sortOrder, &isSystem); err != nil {
			continue
		}
		var enumVals []any
		if enumStr != "" {
			_ = json.Unmarshal([]byte(enumStr), &enumVals)
		}
		out = append(out, map[string]any{
			"key": key, "label": label, "type": typ,
			"enum_values": enumVals, "required": required != 0,
			"sort_order": sortOrder, "is_system": isSystem != 0,
		})
	}
	return out, nil
}

func loadChannels(db *sql.DB, c *Contact) error {
	rows, err := db.Query(
		`SELECT id, kind, value, COALESCE(label,''), is_primary, COALESCE(verified_at,''), COALESCE(source,'')
		 FROM contact_channels WHERE contact_id = ? ORDER BY is_primary DESC, kind, id`,
		c.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	c.Channels = []Channel{}
	for rows.Next() {
		ch := Channel{}
		var primary int
		if err := rows.Scan(&ch.ID, &ch.Kind, &ch.Value, &ch.Label, &primary, &ch.VerifiedAt, &ch.Source); err == nil {
			ch.IsPrimary = primary != 0
			c.Channels = append(c.Channels, ch)
		}
	}
	return nil
}

func loadTags(db *sql.DB, c *Contact) error {
	rows, err := db.Query(
		`SELECT tag_name FROM contact_tags WHERE contact_id = ? ORDER BY tag_name`, c.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	c.Tags = []string{}
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err == nil {
			c.Tags = append(c.Tags, t)
		}
	}
	return nil
}

func loadAttributes(db *sql.DB, c *Contact) error {
	rows, err := db.Query(
		`SELECT d.key, d.label, d.type,
			a.value_text, a.value_number, a.value_date, a.value_bool,
			COALESCE(a.source,''), COALESCE(a.set_at,'')
		 FROM contact_attributes a
		 JOIN contact_attribute_defs d ON d.id = a.def_id
		 WHERE a.contact_id = ?
		 ORDER BY d.sort_order, d.key`,
		c.ID)
	if err != nil {
		return err
	}
	defer rows.Close()
	c.Attributes = []Attribute{}
	for rows.Next() {
		var key, label, typ, src, setAt string
		var vt, vd sql.NullString
		var vn sql.NullFloat64
		var vb sql.NullBool
		if err := rows.Scan(&key, &label, &typ, &vt, &vn, &vd, &vb, &src, &setAt); err != nil {
			continue
		}
		var v any
		switch typ {
		case "text", "url", "select":
			if vt.Valid {
				v = vt.String
			}
		case "number":
			if vn.Valid {
				v = vn.Float64
			}
		case "date":
			if vd.Valid {
				// SQLite's date affinity reformats bare YYYY-MM-DD into
				// YYYY-MM-DDT00:00:00Z on read. Strip the time-of-day
				// part if it's exactly midnight UTC — preserves the
				// caller's intent (a date, not a timestamp).
				s := vd.String
				if strings.HasSuffix(s, "T00:00:00Z") {
					s = strings.TrimSuffix(s, "T00:00:00Z")
				}
				v = s
			}
		case "bool":
			if vb.Valid {
				v = vb.Bool
			}
		case "multi_select":
			if vt.Valid {
				var arr []any
				_ = json.Unmarshal([]byte(vt.String), &arr)
				v = arr
			}
		}
		c.Attributes = append(c.Attributes, Attribute{
			Key: key, Label: label, Type: typ, Value: v,
			Source: src, SetAt: setAt,
		})
	}
	return nil
}

// ─── Tiny utils ─────────────────────────────────────────────────────

func intArg(args map[string]any, key string, def int) int {
	if v, ok := args[key].(float64); ok {
		return int(v)
	}
	if v, ok := args[key].(int); ok {
		return v
	}
	return def
}

func int64Arg(args map[string]any, key string) int64 {
	if v, ok := args[key].(float64); ok {
		return int64(v)
	}
	if v, ok := args[key].(int); ok {
		return int64(v)
	}
	if v, ok := args[key].(int64); ok {
		return v
	}
	return 0
}

func strArg(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return ""
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

// normaliseChannel applies the cheap normalisation rules — lowercase
// emails, strip whitespace, hard-tighten phones to digits + leading +.
// Real E.164 normalisation needs a phone library; for v0.1 we keep
// what the agent / dashboard sends and just trim. v0.2 adds libphone.
func normaliseChannel(kind, value string) string {
	value = strings.TrimSpace(value)
	switch kind {
	case "email":
		return strings.ToLower(value)
	case "phone":
		// Keep only +, digits, spaces, dashes; collapse whitespace.
		var b strings.Builder
		for _, r := range value {
			if r == '+' || (r >= '0' && r <= '9') {
				b.WriteRune(r)
			}
		}
		return b.String()
	}
	return value
}

// ─── HTTP utilities ────────────────────────────────────────────────

func httpJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// getAppCtx fetches the AppCtx the SDK threaded into the request via
// a stable global. The SDK does not currently expose a public hook to
// pass the ctx into HTTP handlers, so we keep our own pointer wired
// up at OnMount time.
//
// (If the SDK grows a request-scoped accessor we'll switch to it.)
var globalCtx *sdk.AppCtx

func getAppCtx(_ *http.Request) *sdk.AppCtx { return globalCtx }
