// calendar v0.1 — multiple calendars, events with recurrence, holidays
// helper. No external accounts; v0.2 adds Google/Outlook sync via the
// existing platform.oauth.start path the social app uses.
//
// Recurrence is rrule strings on the master event; events_list expands
// at read-time. Edit-scope (this | this_and_following | all) is wired
// in; child rows are created lazily on "this" edits and the master's
// exdate skips that date so the expansion stays consistent.
package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: calendar
display_name: Calendar
version: 0.1.0
description: Self-contained calendar with multiple calendars and recurrence.
author: Apteva
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
			Handler: a.toolEventsList},
		{Name: "events_get",
			Description: "Read a single event row by id. Args: event_id.",
			InputSchema: schemaObject(map[string]any{
				"event_id": map[string]any{"type": "integer"},
			}, []string{"event_id"}),
			Handler: a.toolEventsGet},
		{Name: "events_create",
			Description: "Create an event. start_at and end_at are RFC3339 UTC. Use rrule for recurrence (e.g. 'FREQ=WEEKLY;BYDAY=MO'). Args: calendar_id, title, start_at, end_at, all_day?, description?, location?, rrule?.",
			InputSchema: schemaObject(map[string]any{
				"calendar_id": map[string]any{"type": "integer"},
				"title":       map[string]any{"type": "string"},
				"start_at":    map[string]any{"type": "string"},
				"end_at":      map[string]any{"type": "string"},
				"all_day":     map[string]any{"type": "boolean"},
				"description": map[string]any{"type": "string"},
				"location":    map[string]any{"type": "string"},
				"rrule":       map[string]any{"type": "string"},
			}, []string{"calendar_id", "title", "start_at", "end_at"}),
			Handler: a.toolEventsCreate},
		{Name: "events_update",
			Description: "Update an event. For recurring events, scope picks which occurrences are affected: 'this' (default — only this occurrence, creates a child row), 'this_and_following' (truncates the master via UNTIL + creates a new master from this date), 'all' (edits the master). Args: event_id, scope?, occurrence_start_at? (required for 'this'), title?, description?, location?, start_at?, end_at?, all_day?, rrule?.",
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
			}, []string{"event_id"}),
			Handler: a.toolEventsUpdate},
		{Name: "events_delete",
			Description: "Delete an event. Same scope semantics as events_update. Args: event_id, scope?, occurrence_start_at?.",
			InputSchema: schemaObject(map[string]any{
				"event_id":            map[string]any{"type": "integer"},
				"scope":               map[string]any{"type": "string"},
				"occurrence_start_at": map[string]any{"type": "string"},
			}, []string{"event_id"}),
			Handler: a.toolEventsDelete},
		{Name: "events_find_slot",
			Description: "Find open slots. Walks the [window_start, window_end] range, snaps to working_hours per weekday, drops slots overlapping any event on the listed calendars (with buffers). Args: duration_minutes, window_start, window_end, calendar_ids? (default all enabled), working_hours? ({mon|tue|...|sun: {start: 'HH:MM', end: 'HH:MM'}}; default Mon-Fri 09:00-18:00), buffer_before_minutes? (default 0), buffer_after_minutes? (default 0), limit? (default 10).",
			InputSchema: schemaObject(map[string]any{
				"duration_minutes":       map[string]any{"type": "integer"},
				"window_start":           map[string]any{"type": "string"},
				"window_end":             map[string]any{"type": "string"},
				"calendar_ids":           map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
				"working_hours":          map[string]any{"type": "object"},
				"buffer_before_minutes":  map[string]any{"type": "integer"},
				"buffer_after_minutes":   map[string]any{"type": "integer"},
				"limit":                  map[string]any{"type": "integer"},
			}, []string{"duration_minutes", "window_start", "window_end"}),
			Handler: a.toolEventsFindSlot},
		{Name: "holidays_set",
			Description: "Bulk-load common holidays for a country into a kind=holidays calendar (creates the calendar if it doesn't exist). Args: year, country (FR|US|GB).",
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

func (a *App) toolCalendarsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid := os.Getenv("APTEVA_PROJECT_ID")
	rows, err := ctx.AppDB().Query(
		`SELECT id, project_id, name, color, kind, enabled, created_at
		 FROM calendars WHERE project_id=? ORDER BY id`,
		pid,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Calendar{}
	for rows.Next() {
		var c Calendar
		var en int
		if err := rows.Scan(&c.ID, &c.ProjectID, &c.Name, &c.Color, &c.Kind, &en, &c.CreatedAt); err == nil {
			c.Enabled = en == 1
			out = append(out, c)
		}
	}
	return map[string]any{"calendars": out}, nil
}

func (a *App) toolCalendarsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	name, _ := args["name"].(string)
	if strings.TrimSpace(name) == "" {
		return nil, errors.New("name required")
	}
	color := strArg(args, "color", "#3b82f6")
	kind := strArg(args, "kind", "custom")
	pid := os.Getenv("APTEVA_PROJECT_ID")
	res, err := ctx.AppDB().Exec(
		`INSERT INTO calendars (project_id, name, color, kind) VALUES (?, ?, ?, ?)`,
		pid, name, color, kind,
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	ctx.Emit("calendar.created", map[string]any{"calendar_id": id, "name": name})
	return getCalendar(ctx, id)
}

func (a *App) toolCalendarsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64(intArg(args, "id", 0))
	if id == 0 {
		return nil, errors.New("id required")
	}
	cols := []string{}
	vals := []any{}
	for _, k := range []string{"name", "color", "kind"} {
		if v, ok := args[k].(string); ok && v != "" {
			cols = append(cols, k+"=?")
			vals = append(vals, v)
		}
	}
	if v, ok := args["enabled"].(bool); ok {
		cols = append(cols, "enabled=?")
		if v {
			vals = append(vals, 1)
		} else {
			vals = append(vals, 0)
		}
	}
	if len(cols) == 0 {
		// Refuse a no-op so callers (especially LLM agents) get a
		// clear signal that they passed unsupported fields. Silently
		// returning the unchanged row makes it look like the call
		// "succeeded" and triggers infinite retry loops.
		return nil, errors.New("no updatable fields supplied — pass at least one of: name, color, kind, enabled")
	}
	vals = append(vals, id)
	if _, err := ctx.AppDB().Exec(
		`UPDATE calendars SET `+strings.Join(cols, ", ")+` WHERE id=?`, vals...,
	); err != nil {
		return nil, err
	}
	ctx.Emit("calendar.updated", map[string]any{"calendar_id": id})
	return getCalendar(ctx, id)
}

func (a *App) toolCalendarsDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64(intArg(args, "id", 0))
	if id == 0 {
		return nil, errors.New("id required")
	}
	if _, err := ctx.AppDB().Exec(`DELETE FROM calendars WHERE id=?`, id); err != nil {
		return nil, err
	}
	ctx.Emit("calendar.deleted", map[string]any{"calendar_id": id})
	return map[string]any{"deleted": id}, nil
}

func getCalendar(ctx *sdk.AppCtx, id int64) (Calendar, error) {
	var c Calendar
	var en int
	err := ctx.AppDB().QueryRow(
		`SELECT id, project_id, name, color, kind, enabled, created_at FROM calendars WHERE id=?`,
		id,
	).Scan(&c.ID, &c.ProjectID, &c.Name, &c.Color, &c.Kind, &en, &c.CreatedAt)
	c.Enabled = en == 1
	return c, err
}

// ─── Events ──────────────────────────────────────────────────────

type Event struct {
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

func (a *App) toolEventsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	from, _ := args["from"].(string)
	to, _ := args["to"].(string)
	if from == "" || to == "" {
		return nil, errors.New("from and to required (RFC3339)")
	}
	fromT, err := parseFlexibleTime(from)
	if err != nil {
		return nil, fmt.Errorf("from: %w", err)
	}
	toT, err := parseFlexibleTime(to)
	if err != nil {
		return nil, fmt.Errorf("to: %w", err)
	}
	if !toT.After(fromT) {
		return nil, errors.New("to must be after from")
	}
	calIDs := intSliceArg(args, "calendar_ids")
	pid := os.Getenv("APTEVA_PROJECT_ID")

	q := `SELECT e.id, e.calendar_id, e.title, e.description, e.location,
	             e.start_at, e.end_at, e.all_day, e.status, e.rrule, e.exdate,
	             COALESCE(e.parent_event_id, 0), COALESCE(e.occurrence_start_at, '')
	      FROM events e JOIN calendars c ON c.id = e.calendar_id
	      WHERE c.project_id=? AND c.enabled=1`
	a2 := []any{pid}
	if len(calIDs) > 0 {
		placeholders := make([]string, len(calIDs))
		for i, id := range calIDs {
			placeholders[i] = "?"
			a2 = append(a2, id)
		}
		q += " AND e.calendar_id IN (" + strings.Join(placeholders, ",") + ")"
	}
	rows, err := ctx.AppDB().Query(q, a2...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	occ := []Occurrence{}
	for rows.Next() {
		var ev Event
		var allDay int
		var exdateJSON string
		if err := rows.Scan(&ev.ID, &ev.CalendarID, &ev.Title, &ev.Description, &ev.Location,
			&ev.StartAt, &ev.EndAt, &allDay, &ev.Status, &ev.RRule, &exdateJSON,
			&ev.ParentEventID, &ev.OccurrenceStartAt); err != nil {
			continue
		}
		ev.AllDay = allDay == 1
		_ = json.Unmarshal([]byte(exdateJSON), &ev.ExDate)
		occ = append(occ, expandOccurrences(ev, fromT, toT)...)
	}
	sort.Slice(occ, func(i, j int) bool { return occ[i].StartAt < occ[j].StartAt })
	return map[string]any{"events": occ}, nil
}

func (a *App) toolEventsGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64(intArg(args, "event_id", 0))
	if id == 0 {
		return nil, errors.New("event_id required")
	}
	ev, err := readEvent(ctx, id)
	if err != nil {
		return nil, err
	}
	return ev, nil
}

func (a *App) toolEventsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	calID := int64(intArg(args, "calendar_id", 0))
	if calID == 0 {
		return nil, errors.New("calendar_id required (call calendars_list first if you don't have one)")
	}
	// Verify the calendar exists in this project. Without this, an
	// agent that hallucinates a calendar_id silently inserts orphan
	// event rows that never appear in events_list (the JOIN filter
	// drops them) and the agent gets no signal that anything is
	// wrong. Returning a clear error here lets the agent course-correct.
	pid := os.Getenv("APTEVA_PROJECT_ID")
	var existsCount int
	if err := ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM calendars WHERE id=? AND project_id=?`, calID, pid,
	).Scan(&existsCount); err != nil {
		return nil, err
	}
	if existsCount == 0 {
		return nil, fmt.Errorf("calendar_id=%d not found in this project — call calendars_list to see available ids", calID)
	}
	title, _ := args["title"].(string)
	if strings.TrimSpace(title) == "" {
		return nil, errors.New("title required")
	}
	startAtRaw, _ := args["start_at"].(string)
	endAtRaw, _ := args["end_at"].(string)
	startT, err := parseFlexibleTime(startAtRaw)
	if err != nil {
		return nil, fmt.Errorf("start_at: %w", err)
	}
	endT, err := parseFlexibleTime(endAtRaw)
	if err != nil {
		return nil, fmt.Errorf("end_at: %w", err)
	}
	startAt := startT.UTC().Format(time.RFC3339)
	endAt := endT.UTC().Format(time.RFC3339)
	allDay := 0
	if v, ok := args["all_day"].(bool); ok && v {
		allDay = 1
	}
	desc := strArg(args, "description", "")
	loc := strArg(args, "location", "")
	rrule := strArg(args, "rrule", "")
	if rrule != "" {
		if _, err := parseRRule(rrule); err != nil {
			return nil, fmt.Errorf("rrule: %w", err)
		}
	}
	res, err := ctx.AppDB().Exec(
		`INSERT INTO events (calendar_id, title, description, location, start_at, end_at, all_day, rrule)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		calID, title, desc, loc, startAt, endAt, allDay, rrule,
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	ctx.Emit("event.created", map[string]any{"event_id": id, "calendar_id": calID, "title": title})
	return readEvent(ctx, id)
}

func (a *App) toolEventsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64(intArg(args, "event_id", 0))
	if id == 0 {
		return nil, errors.New("event_id required")
	}
	scope := strArg(args, "scope", "all")
	master, err := readEvent(ctx, id)
	if err != nil {
		return nil, err
	}
	now := time.Now().UTC().Format(time.RFC3339)

	switch scope {
	case "this":
		// Only valid on a recurring master.
		if master.RRule == "" {
			return nil, errors.New("scope=this requires a recurring event; pass scope=all for one-offs")
		}
		occStart, _ := args["occurrence_start_at"].(string)
		if occStart == "" {
			return nil, errors.New("occurrence_start_at required for scope=this")
		}
		// Add the date to the master's exdate.
		master.ExDate = append(master.ExDate, occStart)
		ex, _ := json.Marshal(master.ExDate)
		if _, err := ctx.AppDB().Exec(`UPDATE events SET exdate=?, updated_at=? WHERE id=?`,
			string(ex), now, id); err != nil {
			return nil, err
		}
		// Insert a child event row representing this overridden occurrence.
		childStart := strArg(args, "start_at", occStart)
		// Compute default child end from master duration if not provided.
		childEnd := strArg(args, "end_at", "")
		if childEnd == "" {
			masterStart, _ := time.Parse(time.RFC3339, master.StartAt)
			masterEnd, _ := time.Parse(time.RFC3339, master.EndAt)
			d := masterEnd.Sub(masterStart)
			childStartT, _ := time.Parse(time.RFC3339, childStart)
			childEnd = childStartT.Add(d).UTC().Format(time.RFC3339)
		}
		title := strArg(args, "title", master.Title)
		desc := strArg(args, "description", master.Description)
		loc := strArg(args, "location", master.Location)
		res, err := ctx.AppDB().Exec(
			`INSERT INTO events (calendar_id, title, description, location, start_at, end_at, all_day, rrule, parent_event_id, occurrence_start_at)
			 VALUES (?, ?, ?, ?, ?, ?, ?, '', ?, ?)`,
			master.CalendarID, title, desc, loc, childStart, childEnd, boolToInt(master.AllDay), id, occStart,
		)
		if err != nil {
			return nil, err
		}
		childID, _ := res.LastInsertId()
		ctx.Emit("event.updated", map[string]any{"event_id": id, "child_id": childID, "scope": "this"})
		return readEvent(ctx, childID)

	case "this_and_following":
		// Truncate master at occurrence_start_at - 1 day (UNTIL clause)
		// and create a fresh master from this date forward with the
		// updated fields. Anything prior keeps its original shape.
		occStart, _ := args["occurrence_start_at"].(string)
		if occStart == "" {
			return nil, errors.New("occurrence_start_at required for scope=this_and_following")
		}
		occT, err := time.Parse(time.RFC3339, occStart)
		if err != nil {
			return nil, fmt.Errorf("occurrence_start_at: %w", err)
		}
		// Master gets UNTIL = occurrence_start_at - 1s (rrule UNTIL is
		// inclusive; we want the prior occurrence to be the last).
		newRRule := setRRulePart(master.RRule, "UNTIL", occT.Add(-1*time.Second).UTC().Format("20060102T150405Z"))
		if _, err := ctx.AppDB().Exec(`UPDATE events SET rrule=?, updated_at=? WHERE id=?`,
			newRRule, now, id); err != nil {
			return nil, err
		}
		// New master from this date forward.
		title := strArg(args, "title", master.Title)
		desc := strArg(args, "description", master.Description)
		loc := strArg(args, "location", master.Location)
		startAt := strArg(args, "start_at", occStart)
		endAt := strArg(args, "end_at", "")
		if endAt == "" {
			masterStart, _ := time.Parse(time.RFC3339, master.StartAt)
			masterEnd, _ := time.Parse(time.RFC3339, master.EndAt)
			d := masterEnd.Sub(masterStart)
			t, _ := time.Parse(time.RFC3339, startAt)
			endAt = t.Add(d).UTC().Format(time.RFC3339)
		}
		newRR := strArg(args, "rrule", master.RRule)
		res, err := ctx.AppDB().Exec(
			`INSERT INTO events (calendar_id, title, description, location, start_at, end_at, all_day, rrule)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
			master.CalendarID, title, desc, loc, startAt, endAt, boolToInt(master.AllDay), newRR,
		)
		if err != nil {
			return nil, err
		}
		newID, _ := res.LastInsertId()
		ctx.Emit("event.updated", map[string]any{"event_id": id, "new_master_id": newID, "scope": "this_and_following"})
		return readEvent(ctx, newID)

	case "all", "":
		cols, vals := buildUpdateCols(args, []string{"title", "description", "location", "start_at", "end_at", "rrule"})
		if v, ok := args["all_day"].(bool); ok {
			cols = append(cols, "all_day=?")
			vals = append(vals, boolToInt(v))
		}
		if len(cols) == 0 {
			// Refuse no-op calls — same reason as calendars_update.
			// A silent success on "I tried to set X but X isn't a
			// supported field" makes agents loop.
			return nil, errors.New("no updatable fields supplied — pass at least one of: title, description, location, start_at, end_at, all_day, rrule")
		}
		cols = append(cols, "updated_at=?")
		vals = append(vals, now, id)
		if _, err := ctx.AppDB().Exec(
			`UPDATE events SET `+strings.Join(cols, ", ")+` WHERE id=?`, vals...,
		); err != nil {
			return nil, err
		}
		ctx.Emit("event.updated", map[string]any{"event_id": id, "scope": "all"})
		return readEvent(ctx, id)

	default:
		return nil, fmt.Errorf("unknown scope %q", scope)
	}
}

func (a *App) toolEventsDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64(intArg(args, "event_id", 0))
	if id == 0 {
		return nil, errors.New("event_id required")
	}
	scope := strArg(args, "scope", "all")
	master, err := readEvent(ctx, id)
	if err != nil {
		return nil, err
	}
	switch scope {
	case "this":
		if master.RRule == "" {
			// One-off → just delete it.
			if _, err := ctx.AppDB().Exec(`DELETE FROM events WHERE id=?`, id); err != nil {
				return nil, err
			}
			ctx.Emit("event.deleted", map[string]any{"event_id": id, "scope": "this"})
			return map[string]any{"deleted": id}, nil
		}
		occStart, _ := args["occurrence_start_at"].(string)
		if occStart == "" {
			return nil, errors.New("occurrence_start_at required for scope=this on recurring events")
		}
		master.ExDate = append(master.ExDate, occStart)
		ex, _ := json.Marshal(master.ExDate)
		if _, err := ctx.AppDB().Exec(`UPDATE events SET exdate=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
			string(ex), id); err != nil {
			return nil, err
		}
		ctx.Emit("event.deleted", map[string]any{"event_id": id, "scope": "this", "occurrence_start_at": occStart})
		return map[string]any{"skipped_occurrence": occStart}, nil
	case "this_and_following":
		occStart, _ := args["occurrence_start_at"].(string)
		if occStart == "" {
			return nil, errors.New("occurrence_start_at required for scope=this_and_following")
		}
		occT, err := time.Parse(time.RFC3339, occStart)
		if err != nil {
			return nil, err
		}
		newRRule := setRRulePart(master.RRule, "UNTIL", occT.Add(-1*time.Second).UTC().Format("20060102T150405Z"))
		if _, err := ctx.AppDB().Exec(`UPDATE events SET rrule=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
			newRRule, id); err != nil {
			return nil, err
		}
		ctx.Emit("event.deleted", map[string]any{"event_id": id, "scope": "this_and_following"})
		return map[string]any{"truncated": id, "until": occStart}, nil
	case "all", "":
		if _, err := ctx.AppDB().Exec(`DELETE FROM events WHERE id=?`, id); err != nil {
			return nil, err
		}
		ctx.Emit("event.deleted", map[string]any{"event_id": id, "scope": "all"})
		return map[string]any{"deleted": id}, nil
	default:
		return nil, fmt.Errorf("unknown scope %q", scope)
	}
}

func readEvent(ctx *sdk.AppCtx, id int64) (Event, error) {
	var ev Event
	var allDay int
	var exdateJSON string
	err := ctx.AppDB().QueryRow(
		`SELECT id, calendar_id, title, description, location, start_at, end_at,
		        all_day, status, rrule, exdate, COALESCE(parent_event_id,0),
		        COALESCE(occurrence_start_at,''), created_at, updated_at
		 FROM events WHERE id=?`, id,
	).Scan(&ev.ID, &ev.CalendarID, &ev.Title, &ev.Description, &ev.Location,
		&ev.StartAt, &ev.EndAt, &allDay, &ev.Status, &ev.RRule, &exdateJSON,
		&ev.ParentEventID, &ev.OccurrenceStartAt, &ev.CreatedAt, &ev.UpdatedAt)
	if err != nil {
		return ev, err
	}
	ev.AllDay = allDay == 1
	_ = json.Unmarshal([]byte(exdateJSON), &ev.ExDate)
	return ev, nil
}

// ─── Recurrence (rrule) ──────────────────────────────────────────

// rrule is a tiny RFC 5545 subset:
//   FREQ=DAILY|WEEKLY|MONTHLY|YEARLY (required)
//   INTERVAL=N (default 1)
//   COUNT=N         (mutually exclusive with UNTIL)
//   UNTIL=YYYYMMDDTHHMMSSZ
//   BYDAY=MO,TU,…   (WEEKLY only in this implementation)
//
// Anything else is ignored. This covers the everyday cases; the full
// spec is a v0.2 concern (BYMONTHDAY, BYSETPOS, etc.).

type rruleParsed struct {
	freq     string
	interval int
	count    int
	until    time.Time
	byday    []time.Weekday
	hasUntil bool
}

var weekdayMap = map[string]time.Weekday{
	"MO": time.Monday, "TU": time.Tuesday, "WE": time.Wednesday,
	"TH": time.Thursday, "FR": time.Friday, "SA": time.Saturday, "SU": time.Sunday,
}

func parseRRule(s string) (rruleParsed, error) {
	r := rruleParsed{interval: 1}
	for _, part := range strings.Split(s, ";") {
		kv := strings.SplitN(strings.TrimSpace(part), "=", 2)
		if len(kv) != 2 {
			continue
		}
		k, v := strings.ToUpper(kv[0]), kv[1]
		switch k {
		case "FREQ":
			r.freq = strings.ToUpper(v)
		case "INTERVAL":
			n, _ := strconv.Atoi(v)
			if n > 0 {
				r.interval = n
			}
		case "COUNT":
			r.count, _ = strconv.Atoi(v)
		case "UNTIL":
			t, err := time.Parse("20060102T150405Z", v)
			if err != nil {
				// Try date-only.
				t2, err2 := time.Parse("20060102", v)
				if err2 != nil {
					return r, fmt.Errorf("UNTIL: %w", err)
				}
				t = t2
			}
			r.until = t
			r.hasUntil = true
		case "BYDAY":
			for _, d := range strings.Split(v, ",") {
				if wd, ok := weekdayMap[strings.ToUpper(strings.TrimSpace(d))]; ok {
					r.byday = append(r.byday, wd)
				}
			}
		}
	}
	switch r.freq {
	case "DAILY", "WEEKLY", "MONTHLY", "YEARLY":
	default:
		return r, fmt.Errorf("unsupported FREQ %q (DAILY|WEEKLY|MONTHLY|YEARLY)", r.freq)
	}
	return r, nil
}

// setRRulePart replaces (or adds) one KEY=VALUE pair in an rrule
// string. Used by edit-scope=this_and_following to slap UNTIL on the
// master.
func setRRulePart(rrule, key, value string) string {
	parts := []string{}
	found := false
	for _, p := range strings.Split(rrule, ";") {
		kv := strings.SplitN(strings.TrimSpace(p), "=", 2)
		if len(kv) == 2 && strings.EqualFold(kv[0], key) {
			parts = append(parts, key+"="+value)
			found = true
		} else if p != "" {
			parts = append(parts, p)
		}
	}
	if !found {
		parts = append(parts, key+"="+value)
	}
	return strings.Join(parts, ";")
}

// expandOccurrences walks an event (master or one-off) and emits
// Occurrence rows for any instance that overlaps [from, to]. For
// rrule-less events this returns at most one row; for recurring it
// expands up to a hard cap (1000) so a runaway DAILY event can't pin
// the CPU.
const maxOccurrences = 1000

func expandOccurrences(ev Event, from, to time.Time) []Occurrence {
	out := []Occurrence{}
	startT, err := time.Parse(time.RFC3339, ev.StartAt)
	if err != nil {
		return out
	}
	endT, err := time.Parse(time.RFC3339, ev.EndAt)
	if err != nil {
		return out
	}
	dur := endT.Sub(startT)

	// Non-recurring: emit once if it overlaps.
	if ev.RRule == "" {
		if endT.After(from) && startT.Before(to) {
			out = append(out, occurrenceOf(ev, startT, dur))
		}
		return out
	}
	r, err := parseRRule(ev.RRule)
	if err != nil {
		return out
	}
	exdates := map[string]bool{}
	for _, d := range ev.ExDate {
		if t, err := time.Parse(time.RFC3339, d); err == nil {
			exdates[t.UTC().Format(time.RFC3339)] = true
		}
	}

	emit := func(occStart time.Time) bool {
		if r.hasUntil && occStart.After(r.until) {
			return false
		}
		if exdates[occStart.UTC().Format(time.RFC3339)] {
			return true
		}
		occEnd := occStart.Add(dur)
		if occEnd.After(from) && occStart.Before(to) {
			occ := occurrenceOf(ev, occStart, dur)
			occ.IsRecurring = true
			occ.OccurrenceStartAt = occStart.UTC().Format(time.RFC3339)
			out = append(out, occ)
		}
		return true
	}

	emitted := 0
	switch r.freq {
	case "DAILY":
		for t := startT; emitted < maxOccurrences && (!t.After(to)); t = t.AddDate(0, 0, r.interval) {
			if r.count > 0 && emitted >= r.count {
				break
			}
			if !emit(t) {
				break
			}
			emitted++
		}
	case "WEEKLY":
		// If BYDAY is set, emit one occurrence per matching weekday in
		// each interval-week. Otherwise, just step by 7*interval days.
		if len(r.byday) > 0 {
			// Walk week by week starting from the start of the week
			// containing startT (use Monday as week-start).
			weekStart := startOfWeek(startT)
			for emitted < maxOccurrences {
				if weekStart.After(to) {
					break
				}
				for _, wd := range r.byday {
					occStart := weekStart.AddDate(0, 0, weekdayOffset(wd))
					// Stitch the original time-of-day onto the date.
					occStart = time.Date(occStart.Year(), occStart.Month(), occStart.Day(),
						startT.Hour(), startT.Minute(), startT.Second(), 0, time.UTC)
					if occStart.Before(startT) {
						continue
					}
					if r.count > 0 && emitted >= r.count {
						break
					}
					if !emit(occStart) {
						break
					}
					emitted++
				}
				weekStart = weekStart.AddDate(0, 0, 7*r.interval)
			}
		} else {
			for t := startT; emitted < maxOccurrences && (!t.After(to)); t = t.AddDate(0, 0, 7*r.interval) {
				if r.count > 0 && emitted >= r.count {
					break
				}
				if !emit(t) {
					break
				}
				emitted++
			}
		}
	case "MONTHLY":
		for t := startT; emitted < maxOccurrences && (!t.After(to)); t = t.AddDate(0, r.interval, 0) {
			if r.count > 0 && emitted >= r.count {
				break
			}
			if !emit(t) {
				break
			}
			emitted++
		}
	case "YEARLY":
		for t := startT; emitted < maxOccurrences && (!t.After(to)); t = t.AddDate(r.interval, 0, 0) {
			if r.count > 0 && emitted >= r.count {
				break
			}
			if !emit(t) {
				break
			}
			emitted++
		}
	}
	return out
}

func occurrenceOf(ev Event, start time.Time, dur time.Duration) Occurrence {
	return Occurrence{
		ID:                ev.ID, // same value as EventID; emitted under both names for shape parity with events_create.
		EventID:           ev.ID,
		CalendarID:        ev.CalendarID,
		Title:             ev.Title,
		Description:       ev.Description,
		Location:          ev.Location,
		StartAt:           start.UTC().Format(time.RFC3339),
		EndAt:             start.Add(dur).UTC().Format(time.RFC3339),
		AllDay:            ev.AllDay,
		Status:            ev.Status,
		IsRecurring:       false,
		OccurrenceStartAt: start.UTC().Format(time.RFC3339),
	}
}

// startOfWeek returns the Monday of the week containing t (UTC).
func startOfWeek(t time.Time) time.Time {
	t = t.UTC()
	wd := int(t.Weekday())
	if wd == 0 {
		wd = 7 // Sunday → 7 so Monday is offset 0
	}
	d := wd - 1
	return time.Date(t.Year(), t.Month(), t.Day()-d, 0, 0, 0, 0, time.UTC)
}

// weekdayOffset returns 0..6 with Monday=0.
func weekdayOffset(wd time.Weekday) int {
	if wd == time.Sunday {
		return 6
	}
	return int(wd) - 1
}

// ─── Find slot ───────────────────────────────────────────────────

func (a *App) toolEventsFindSlot(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	durationMin := intArg(args, "duration_minutes", 0)
	if durationMin <= 0 {
		return nil, errors.New("duration_minutes required (positive)")
	}
	from, _ := args["window_start"].(string)
	to, _ := args["window_end"].(string)
	if from == "" || to == "" {
		return nil, errors.New("window_start and window_end required (RFC3339)")
	}
	fromT, err := parseFlexibleTime(from)
	if err != nil {
		return nil, fmt.Errorf("window_start: %w", err)
	}
	toT, err := parseFlexibleTime(to)
	if err != nil {
		return nil, fmt.Errorf("window_end: %w", err)
	}
	calIDs := intSliceArg(args, "calendar_ids")
	wh := parseWorkingHours(args["working_hours"])
	bufBefore := time.Duration(intArg(args, "buffer_before_minutes", 0)) * time.Minute
	bufAfter := time.Duration(intArg(args, "buffer_after_minutes", 0)) * time.Minute
	limit := intArg(args, "limit", 10)
	if limit <= 0 {
		limit = 10
	}
	dur := time.Duration(durationMin) * time.Minute

	// Read events as occurrences across the window.
	occArgs := map[string]any{
		"from": fromT.UTC().Format(time.RFC3339),
		"to":   toT.UTC().Format(time.RFC3339),
	}
	if len(calIDs) > 0 {
		ids := make([]any, len(calIDs))
		for i, id := range calIDs {
			ids[i] = float64(id)
		}
		occArgs["calendar_ids"] = ids
	}
	rawOcc, err := a.toolEventsList(ctx, occArgs)
	if err != nil {
		return nil, err
	}
	busy := []timeRange{}
	for _, o := range rawOcc.(map[string]any)["events"].([]Occurrence) {
		s, _ := time.Parse(time.RFC3339, o.StartAt)
		e, _ := time.Parse(time.RFC3339, o.EndAt)
		busy = append(busy, timeRange{
			start: s.Add(-bufBefore),
			end:   e.Add(bufAfter),
		})
	}
	sort.Slice(busy, func(i, j int) bool { return busy[i].start.Before(busy[j].start) })

	// Walk in 15-minute steps inside working_hours, return slots that
	// don't overlap any busy range.
	step := 15 * time.Minute
	out := []map[string]string{}
	for cursor := snapToStep(fromT, step); cursor.Add(dur).Before(toT) || cursor.Add(dur).Equal(toT); cursor = cursor.Add(step) {
		if !inWorkingHours(cursor, cursor.Add(dur), wh) {
			continue
		}
		if overlapsAny(cursor, cursor.Add(dur), busy) {
			continue
		}
		out = append(out, map[string]string{
			"start": cursor.UTC().Format(time.RFC3339),
			"end":   cursor.Add(dur).UTC().Format(time.RFC3339),
		})
		if len(out) >= limit {
			break
		}
	}
	return map[string]any{"slots": out}, nil
}

type timeRange struct{ start, end time.Time }

func overlapsAny(start, end time.Time, ranges []timeRange) bool {
	for _, r := range ranges {
		if start.Before(r.end) && r.start.Before(end) {
			return true
		}
	}
	return false
}

func snapToStep(t time.Time, step time.Duration) time.Time {
	rounded := t.Truncate(step)
	if rounded.Before(t) {
		rounded = rounded.Add(step)
	}
	return rounded
}

type workingHours map[time.Weekday]struct{ start, end string }

func defaultWorkingHours() workingHours {
	wh := workingHours{}
	for _, wd := range []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday} {
		wh[wd] = struct{ start, end string }{"09:00", "18:00"}
	}
	return wh
}

func parseWorkingHours(v any) workingHours {
	wh := workingHours{}
	m, ok := v.(map[string]any)
	if !ok || len(m) == 0 {
		return defaultWorkingHours()
	}
	dayMap := map[string]time.Weekday{
		"mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday,
		"thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday, "sun": time.Sunday,
	}
	for k, v := range m {
		wd, ok := dayMap[strings.ToLower(k)]
		if !ok {
			continue
		}
		if entry, ok := v.(map[string]any); ok {
			s, _ := entry["start"].(string)
			e, _ := entry["end"].(string)
			if s != "" && e != "" {
				wh[wd] = struct{ start, end string }{s, e}
			}
		}
	}
	return wh
}

func inWorkingHours(start, end time.Time, wh workingHours) bool {
	day := start.UTC().Weekday()
	hours, ok := wh[day]
	if !ok {
		return false
	}
	startMin := hhmmToMinutes(hours.start)
	endMin := hhmmToMinutes(hours.end)
	startOfDay := time.Date(start.Year(), start.Month(), start.Day(), 0, 0, 0, 0, time.UTC)
	whStart := startOfDay.Add(time.Duration(startMin) * time.Minute)
	whEnd := startOfDay.Add(time.Duration(endMin) * time.Minute)
	return !start.Before(whStart) && !end.After(whEnd)
}

// parseFlexibleTime accepts several common datetime shapes the agent
// (or HTTP clients) might emit. Canonicalises to UTC. Honours these
// formats in order:
//   - RFC3339 with timezone (the canonical form)
//   - RFC3339 with milliseconds
//   - "2006-01-02 15:04:05" (no T, no tz; treated as UTC)
//   - "2006-01-02T15:04:05" (no tz; treated as UTC)
//   - "2006-01-02"          (date-only; treated as midnight UTC)
func parseFlexibleTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, errors.New("empty")
	}
	formats := []string{
		time.RFC3339,
		time.RFC3339Nano,
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02 15:04:05Z",
		"2006-01-02",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("can't parse %q (try RFC3339 like 2026-05-04T12:00:00Z)", s)
}

func hhmmToMinutes(s string) int {
	parts := strings.Split(s, ":")
	if len(parts) != 2 {
		return 0
	}
	h, _ := strconv.Atoi(parts[0])
	m, _ := strconv.Atoi(parts[1])
	return h*60 + m
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

func (a *App) toolHolidaysSet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	year := intArg(args, "year", 0)
	country, _ := args["country"].(string)
	if year < 1900 || year > 2200 {
		return nil, errors.New("year out of range")
	}
	dates, err := holidayDates(year, country)
	if err != nil {
		return nil, err
	}
	pid := os.Getenv("APTEVA_PROJECT_ID")

	// Find or create the holidays calendar.
	var calID int64
	err = ctx.AppDB().QueryRow(
		`SELECT id FROM calendars WHERE project_id=? AND kind='holidays' LIMIT 1`, pid,
	).Scan(&calID)
	if errors.Is(err, sql.ErrNoRows) {
		res, err2 := ctx.AppDB().Exec(
			`INSERT INTO calendars (project_id, name, color, kind) VALUES (?, ?, ?, 'holidays')`,
			pid, "Holidays — "+strings.ToUpper(country), "#94a3b8",
		)
		if err2 != nil {
			return nil, err2
		}
		calID, _ = res.LastInsertId()
	} else if err != nil {
		return nil, err
	}

	created := 0
	for _, h := range dates {
		startStr := h.date.Format(time.RFC3339)
		endStr := h.date.Add(24 * time.Hour).Format(time.RFC3339)
		// De-dupe by (calendar_id, title, start_at) — re-running for
		// the same year is idempotent.
		var existing int64
		ctx.AppDB().QueryRow(
			`SELECT id FROM events WHERE calendar_id=? AND title=? AND start_at=?`,
			calID, h.name, startStr,
		).Scan(&existing)
		if existing > 0 {
			continue
		}
		if _, err := ctx.AppDB().Exec(
			`INSERT INTO events (calendar_id, title, start_at, end_at, all_day) VALUES (?, ?, ?, ?, 1)`,
			calID, h.name, startStr, endStr,
		); err != nil {
			continue
		}
		created++
	}
	ctx.Emit("holidays.set", map[string]any{"calendar_id": calID, "year": year, "country": country, "created": created})
	return map[string]any{"calendar_id": calID, "year": year, "country": country, "created": created}, nil
}

// ─── HTTP wrappers ───────────────────────────────────────────────

func (a *App) handleCalendars(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		out, err := a.toolCalendarsList(globalCtx, map[string]any{})
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, out)
	case http.MethodPost:
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		out, err := a.toolCalendarsCreate(globalCtx, body)
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
	id, ok := pathID(r.URL.Path, "/calendars/")
	if !ok {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		c, err := getCalendar(globalCtx, id)
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		writeJSON(w, c)
	case http.MethodPatch, http.MethodPut:
		body := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		body["id"] = id
		out, err := a.toolCalendarsUpdate(globalCtx, body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, out)
	case http.MethodDelete:
		if _, err := a.toolCalendarsDelete(globalCtx, map[string]any{"id": id}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "GET, PATCH or DELETE", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleEvents(w http.ResponseWriter, r *http.Request) {
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
		out, err := a.toolEventsList(globalCtx, args)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, out)
	case http.MethodPost:
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		out, err := a.toolEventsCreate(globalCtx, body)
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
		ev, err := readEvent(globalCtx, id)
		if err != nil {
			http.Error(w, "not found", http.StatusNotFound)
			return
		}
		writeJSON(w, ev)
	case http.MethodPatch, http.MethodPut:
		body := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		body["event_id"] = id
		out, err := a.toolEventsUpdate(globalCtx, body)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, out)
	case http.MethodDelete:
		body := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		body["event_id"] = id
		if _, err := a.toolEventsDelete(globalCtx, body); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "GET, PATCH or DELETE", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleFindSlot(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body)
	out, err := a.toolEventsFindSlot(globalCtx, body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, out)
}

func (a *App) handleHolidaysSet(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var body map[string]any
	json.NewDecoder(r.Body).Decode(&body)
	out, err := a.toolHolidaysSet(globalCtx, body)
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
		if v, ok := args[f].(string); ok && v != "" {
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
