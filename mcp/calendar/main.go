// Calendar provides project-scoped events, timezone-aware recurrence and availability.
package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: calendar
display_name: Calendar
version: 0.4.0
description: Self-contained calendar with multiple calendars and recurrence.
author: Apteva
icon: /ui/icon.svg
icon_style: monochrome
scopes: [project, global]
requires:
  permissions: [db.write.app]
  integrations: []
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: calendars_list,    description: "List enabled calendars." }
    - { name: calendars_create,  description: "Create a calendar." }
    - { name: calendars_update,  description: "Update a calendar." }
    - { name: calendars_delete,  description: "Delete a calendar + its events." }
    - { name: events_list,       description: "List event occurrences in a window." }
    - { name: events_get,        description: "Read one event." }
    - { name: events_create,     description: "Create an event." }
    - { name: events_update,     description: "Update an event (with edit-scope)." }
    - { name: events_delete,     description: "Delete an event (with edit-scope)." }
    - { name: events_find_slot,  description: "Find open slots." }
    - { name: holidays_set,      description: "Bulk-load common holidays." }
  ui_panels:
    - slot: project.page
      label: Calendar
      icon: calendar
      entry: /ui/CalendarPanel.mjs
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/calendar
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/calendar.db
  migrations: migrations/
upgrade_policy: auto-patch
`

var globalCtx *sdk.AppCtx

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
		return errors.New("calendar requires a db block")
	}
	if err := repairLegacyRules(ctx.AppDB()); err != nil {
		return err
	}
	globalCtx = ctx
	ctx.Logger().Info("calendar mounted")
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

// ─── HTTP routes ─────────────────────────────────────────────────
//
// The SDK reserves /events for platform event ingestion, so the
// app's calendar-events surface is exposed under /items (calendar
// items / entries). The MCP tool names still use "events_…" because
// that's what agents reason about — the URL path naming is a wire
// detail.
func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/calendars", Handler: a.handleCalendars},
		{Pattern: "/calendars/", Handler: a.handleCalendarsItem},
		{Pattern: "/items", Handler: a.handleEvents},
		{Pattern: "/items/", Handler: a.handleEventsItem},
		{Pattern: "/find_slot", Handler: a.handleFindSlot},
		{Pattern: "/holidays/set", Handler: a.handleHolidaysSet},
	}
}

// ─── MCP tools ───────────────────────────────────────────────────

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{Name: "calendars_list",
			Description: "List all calendars in this project (both enabled and disabled). Each row carries an `enabled` boolean — filter client-side if you only want enabled ones.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Handler:     a.toolCalendarsList},
		{Name: "calendars_create",
			Description: "Create a new calendar. Args: name (required), color? (#hex), kind? (personal|work|holidays|blocked|custom; default 'custom'). Returns the calendar row including its `id` — use that id as `calendar_id` in subsequent events_create calls.",
			InputSchema: schemaObject(map[string]any{
				"name":  map[string]any{"type": "string"},
				"color": map[string]any{"type": "string"},
				"kind":  map[string]any{"type": "string", "enum": []string{"personal", "work", "holidays", "blocked", "custom"}},
			}, []string{"name"}),
			Handler: a.toolCalendarsCreate},
		{Name: "calendars_update",
			Description: "Update calendar fields. Args: id (required), name?, color?, kind?, enabled?.",
			InputSchema: schemaObject(map[string]any{
				"id":      map[string]any{"type": "integer"},
				"name":    map[string]any{"type": "string"},
				"color":   map[string]any{"type": "string"},
				"kind":    map[string]any{"type": "string"},
				"enabled": map[string]any{"type": "boolean"},
			}, []string{"id"}),
			Handler: a.toolCalendarsUpdate},
		{Name: "calendars_delete",
			Description: "Delete a calendar and all its events (cascade). Args: id.",
			InputSchema: schemaObject(map[string]any{
				"id": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolCalendarsDelete},
		{Name: "events_list",
			Description: "List event occurrences in a time window. Recurring events expand into their occurrences. Args: from (RFC3339), to (RFC3339), calendar_ids? (default: all enabled).",
			InputSchema: schemaObject(map[string]any{
				"from":         map[string]any{"type": "string"},
				"to":           map[string]any{"type": "string"},
				"calendar_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
			}, []string{"from", "to"}),
			HandlerCtx: a.listEvents},
		{Name: "events_get",
			Description: "Read a single event row by id. Args: event_id.",
			InputSchema: schemaObject(map[string]any{
				"event_id": map[string]any{"type": "integer"},
			}, []string{"event_id"}),
			Handler: a.toolEventsGet},
		{Name: "events_create",
			Description: "Create an event. Timed start_at/end_at are RFC3339; timezone is an IANA zone for local recurrence. All-day times are date-only or UTC midnight, with an exclusive end date. Use rrule for recurrence (e.g. 'FREQ=WEEKLY;BYDAY=MO'). Args: calendar_id, title, start_at, end_at, all_day?, description?, location?, rrule?.",
			InputSchema: schemaObject(map[string]any{
				"calendar_id": map[string]any{"type": "integer"},
				"title":       map[string]any{"type": "string"},
				"start_at":    map[string]any{"type": "string"},
				"end_at":      map[string]any{"type": "string"},
				"all_day":     map[string]any{"type": "boolean"},
				"description": map[string]any{"type": "string"},
				"location":    map[string]any{"type": "string"},
				"rrule":       map[string]any{"type": "string"},
				"timezone":    map[string]any{"type": "string", "description": "IANA timezone, e.g. Europe/Madrid; default UTC"},
				"status":      map[string]any{"type": "string", "enum": []string{"confirmed", "tentative", "cancelled"}},
			}, []string{"calendar_id", "title", "start_at", "end_at"}),
			Handler: a.toolEventsCreate},
		{Name: "events_update",
			Description: "Update an event. For recurring events, scope picks which occurrences are affected: 'this' (default — only this occurrence, creates a child row), 'this_and_following' (truncates the master via UNTIL + creates a new master from this date), 'all' (edits the master). Args: event_id, scope?, occurrence_start_at? (required for 'this' and 'this_and_following'), title?, description?, location?, start_at?, end_at?, all_day?, rrule?.",
			InputSchema: schemaObject(map[string]any{
				"event_id":            map[string]any{"type": "integer"},
				"scope":               map[string]any{"type": "string", "enum": []string{"this", "this_and_following", "all"}},
				"occurrence_start_at": map[string]any{"type": "string"},
				"title":               map[string]any{"type": "string"},
				"description":         map[string]any{"type": "string"},
				"location":            map[string]any{"type": "string"},
				"start_at":            map[string]any{"type": "string"},
				"end_at":              map[string]any{"type": "string"},
				"all_day":             map[string]any{"type": "boolean"},
				"rrule":               map[string]any{"type": "string"},
				"timezone":            map[string]any{"type": "string"},
				"calendar_id":         map[string]any{"type": "integer"},
				"status":              map[string]any{"type": "string", "enum": []string{"confirmed", "tentative", "cancelled"}},
			}, []string{"event_id"}),
			Handler: a.toolEventsUpdate},
		{Name: "events_delete",
			Description: "Delete an event. Default scope is all; specify this or this_and_following plus occurrence_start_at to limit a recurring deletion. Args: event_id, scope?, occurrence_start_at?.",
			InputSchema: schemaObject(map[string]any{
				"event_id":            map[string]any{"type": "integer"},
				"scope":               map[string]any{"type": "string"},
				"occurrence_start_at": map[string]any{"type": "string"},
			}, []string{"event_id"}),
			Handler: a.toolEventsDelete},
		{Name: "events_find_slot",
			Description: "Find open slots. Walks the [window_start, window_end] range, snaps to working_hours per weekday, drops slots overlapping any event on the listed calendars (with buffers). Args: duration_minutes, window_start, window_end, calendar_ids? (default all enabled), working_hours? ({mon|tue|...|sun: {start: 'HH:MM', end: 'HH:MM'}}; default Mon-Fri 09:00-18:00), buffer_before_minutes? (default 0), buffer_after_minutes? (default 0), limit? (default 10).",
			InputSchema: schemaObject(map[string]any{
				"duration_minutes":      map[string]any{"type": "integer"},
				"window_start":          map[string]any{"type": "string"},
				"window_end":            map[string]any{"type": "string"},
				"calendar_ids":          map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
				"working_hours":         map[string]any{"type": "object"},
				"timezone":              map[string]any{"type": "string", "description": "IANA timezone for working hours; default UTC"},
				"buffer_before_minutes": map[string]any{"type": "integer"},
				"buffer_after_minutes":  map[string]any{"type": "integer"},
				"limit":                 map[string]any{"type": "integer"},
			}, []string{"duration_minutes", "window_start", "window_end"}),
			HandlerCtx: a.findSlot},
		{Name: "holidays_set",
			Description: "Bulk-load selected fixed-date holidays (not a complete national/observed holiday calendar) for a country into a kind=holidays calendar (creates the calendar if it doesn't exist). Args: year, country (FR|US|GB).",
			InputSchema: schemaObject(map[string]any{
				"year":    map[string]any{"type": "integer"},
				"country": map[string]any{"type": "string", "enum": []string{"FR", "US", "GB"}},
			}, []string{"year", "country"}),
			Handler: a.toolHolidaysSet},
	}
}

func main() { sdk.Run(&App{}) }

// ─── Calendars ───────────────────────────────────────────────────

type Calendar struct {
	ID        int64  `json:"id"`
	ProjectID string `json:"project_id"`
	Name      string `json:"name"`
	Color     string `json:"color"`
	Kind      string `json:"kind"`
	Enabled   bool   `json:"enabled"`
	CreatedAt string `json:"created_at"`
}

// ─── Events ──────────────────────────────────────────────────────

type Event struct {
	Timezone          string   `json:"timezone"`
	ID                int64    `json:"id"`
	CalendarID        int64    `json:"calendar_id"`
	Title             string   `json:"title"`
	Description       string   `json:"description"`
	Location          string   `json:"location"`
	StartAt           string   `json:"start_at"`
	EndAt             string   `json:"end_at"`
	AllDay            bool     `json:"all_day"`
	Status            string   `json:"status"`
	RRule             string   `json:"rrule"`
	ExDate            []string `json:"exdate"`
	ParentEventID     int64    `json:"parent_event_id,omitempty"`
	OccurrenceStartAt string   `json:"occurrence_start_at,omitempty"`
	CreatedAt         string   `json:"created_at"`
	UpdatedAt         string   `json:"updated_at"`
}

// Occurrence is the materialised view a list query returns. It carries
// the master's id + the occurrence's start so editing scope can target
// either the master or this single instance.
//
// `id` is the master event's id (same value as `event_id`) — emitted
// under both names so the shape matches the events_create response
// (`{id: …}`) without forcing callers to translate field names. An
// agent that lists events and then calls events_update can pass either.
type Occurrence struct {
	Timezone          string `json:"timezone"`
	RRule             string `json:"rrule"`
	ID                int64  `json:"id"`
	EventID           int64  `json:"event_id"`
	CalendarID        int64  `json:"calendar_id"`
	Title             string `json:"title"`
	Description       string `json:"description"`
	Location          string `json:"location"`
	StartAt           string `json:"start_at"`
	EndAt             string `json:"end_at"`
	AllDay            bool   `json:"all_day"`
	Status            string `json:"status"`
	IsRecurring       bool   `json:"is_recurring"`
	OccurrenceStartAt string `json:"occurrence_start_at"`
}

// ─── Holidays ────────────────────────────────────────────────────

// holidayDates returns a list of (date, name) pairs for the given
// year+country. v0.1 supports FR, US, GB with the most-recognised
// fixed holidays. Movable holidays (Easter etc.) are deliberately
// skipped — adding them later is mechanical.
func holidayDates(year int, country string) ([]struct {
	date time.Time
	name string
}, error) {
	type h = struct {
		date time.Time
		name string
	}
	d := func(m time.Month, day int) time.Time {
		return time.Date(year, m, day, 0, 0, 0, 0, time.UTC)
	}
	switch strings.ToUpper(country) {
	case "FR":
		return []h{
			{d(time.January, 1), "Jour de l'an"},
			{d(time.May, 1), "Fête du Travail"},
			{d(time.May, 8), "Victoire 1945"},
			{d(time.July, 14), "Fête nationale"},
			{d(time.August, 15), "Assomption"},
			{d(time.November, 1), "Toussaint"},
			{d(time.November, 11), "Armistice"},
			{d(time.December, 25), "Noël"},
		}, nil
	case "US":
		return []h{
			{d(time.January, 1), "New Year's Day"},
			{d(time.July, 4), "Independence Day"},
			{d(time.November, 11), "Veterans Day"},
			{d(time.December, 25), "Christmas Day"},
		}, nil
	case "GB":
		return []h{
			{d(time.January, 1), "New Year's Day"},
			{d(time.December, 25), "Christmas Day"},
			{d(time.December, 26), "Boxing Day"},
		}, nil
	}
	return nil, fmt.Errorf("unknown country %q (FR|US|GB)", country)
}

// ─── HTTP wrappers ───────────────────────────────────────────────

func (a *App) handleCalendars(w http.ResponseWriter, r *http.Request) {
	ctx, err := requestContext(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	switch r.Method {
	case http.MethodGet:
		out, err := a.toolCalendarsList(ctx, map[string]any{})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, out)
	case http.MethodPost:
		var body map[string]any
		if !decodeBody(w, r, &body) {
			return
		}
		out, err := a.toolCalendarsCreate(ctx, body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, out)
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleCalendarsItem(w http.ResponseWriter, r *http.Request) {
	ctx, err := requestContext(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	id, ok := pathID(r.URL.Path, "/calendars/")
	if !ok {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		c, err := getCalendar(ctx, id)
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		writeJSON(w, c)
	case http.MethodPatch, http.MethodPut:
		body := map[string]any{}
		if !decodeBody(w, r, &body) {
			return
		}
		body["id"] = id
		out, err := a.toolCalendarsUpdate(ctx, body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, out)
	case http.MethodDelete:
		if _, err := a.toolCalendarsDelete(ctx, map[string]any{"id": id}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "GET, PATCH or DELETE", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleEvents(w http.ResponseWriter, r *http.Request) {
	ctx, err := requestContext(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	switch r.Method {
	case http.MethodGet:
		args := map[string]any{
			"from": r.URL.Query().Get("from"),
			"to":   r.URL.Query().Get("to"),
		}
		if cs := r.URL.Query()["calendar_ids"]; len(cs) > 0 {
			ids := make([]any, 0, len(cs))
			for _, s := range cs {
				if n, err := strconv.ParseInt(s, 10, 64); err == nil {
					ids = append(ids, float64(n))
				}
			}
			args["calendar_ids"] = ids
		}
		out, err := a.listEvents(r.Context(), ctx, args)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, out)
	case http.MethodPost:
		var body map[string]any
		if !decodeBody(w, r, &body) {
			return
		}
		out, err := a.toolEventsCreate(ctx, body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, out)
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleEventsItem(w http.ResponseWriter, r *http.Request) {
	ctx, err := requestContext(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	// /events/find_slot is its own handler — Go's mux dispatches
	// longer-prefix matches first, so this branch only fires for
	// /events/<id>.
	id, ok := pathID(r.URL.Path, "/items/")
	if !ok {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		ev, err := readEvent(ctx, id)
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		writeJSON(w, ev)
	case http.MethodPatch, http.MethodPut:
		body := map[string]any{}
		if !decodeBody(w, r, &body) {
			return
		}
		body["event_id"] = id
		out, err := a.toolEventsUpdate(ctx, body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, out)
	case http.MethodDelete:
		body := map[string]any{}
		if r.ContentLength != 0 && !decodeBody(w, r, &body) {
			return
		}
		body["event_id"] = id
		if _, err := a.toolEventsDelete(ctx, body); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "GET, PATCH or DELETE", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleFindSlot(w http.ResponseWriter, r *http.Request) {
	ctx, err := requestContext(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var body map[string]any
	if !decodeBody(w, r, &body) {
		return
	}
	out, err := a.findSlot(r.Context(), ctx, body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, out)
}

func (a *App) handleHolidaysSet(w http.ResponseWriter, r *http.Request) {
	ctx, err := requestContext(r)
	if err != nil {
		http.Error(w, err.Error(), http.StatusForbidden)
		return
	}
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var body map[string]any
	if !decodeBody(w, r, &body) {
		return
	}
	out, err := a.toolHolidaysSet(ctx, body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, out)
}

// ─── helpers ─────────────────────────────────────────────────────

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
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
	case string:
		// LLM agents frequently stringify numbers when echoing them
		// back from a previous tool's response. Be lenient.
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return def
}

func intSliceArg(m map[string]any, key string) []int64 {
	v, ok := m[key].([]any)
	if !ok {
		return nil
	}
	out := make([]int64, 0, len(v))
	for _, x := range v {
		switch n := x.(type) {
		case float64:
			out = append(out, int64(n))
		case int64:
			out = append(out, n)
		case int:
			out = append(out, int64(n))
		case string:
			if k, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64); err == nil {
				out = append(out, k)
			}
		}
	}
	return out
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func buildUpdateCols(args map[string]any, fields []string) ([]string, []any) {
	cols := []string{}
	vals := []any{}
	for _, f := range fields {
		if v, ok := args[f].(string); ok {
			cols = append(cols, f+"=?")
			vals = append(vals, v)
		}
	}
	return cols, vals
}

// pathID extracts a numeric id from URL paths like "/events/42" or
// "/events/42/something". Returns false when nothing parses.
func pathID(path, prefix string) (int64, bool) {
	rest := strings.TrimPrefix(path, prefix)
	if i := strings.Index(rest, "/"); i >= 0 {
		rest = rest[:i]
	}
	id, err := strconv.ParseInt(rest, 10, 64)
	if err != nil || id == 0 {
		return 0, false
	}
	return id, true
}
