// Bookings provides Calendly-style public scheduling links on top of
// Calendar, with optional Calls rooms and CRM contact/activity wiring.
package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/mail"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"
	_ "time/tzdata"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: bookings
display_name: Bookings
version: 0.2.2
description: Calendly-style booking links for client meetings.
author: Apteva
homepage: https://github.com/apteva/apps/tree/main/mcp/bookings
icon: /ui/icon.svg
icon_style: monochrome
tags: [bookings, scheduling, calendar, meetings]
scopes: [project, global]
min_apteva_version: "0.10.0"
requires:
  permissions:
    - db.write.app
    - platform.apps.call
  integrations:
    - role: calendar
      kind: app
      compatible_app_names: [calendar]
      capabilities: [calendar.read, calendar.write]
      required: true
      label: "Calendar"
    - role: calls
      kind: app
      compatible_app_names: [calls]
      capabilities: [calls.room]
      required: false
      label: "Calls"
    - role: crm
      kind: app
      compatible_app_names: [crm]
      capabilities: [contact.read, contact.write]
      required: false
      label: "CRM"
provides:
  http_routes:
    - prefix: /public/
      no_auth: true
    - prefix: /b/
      no_auth: true
    - prefix: /
  mcp_tools:
    - { name: booking_types_list, description: "List booking types." }
    - { name: booking_types_get, description: "Fetch one booking type." }
    - { name: booking_types_create, description: "Create a booking type." }
    - { name: booking_types_update, description: "Patch a booking type." }
    - { name: booking_types_archive, description: "Archive a booking type." }
    - { name: bookings_find_slots, description: "Find slots for a booking type." }
    - { name: bookings_create, description: "Create a booking." }
    - { name: bookings_get, description: "Fetch one booking." }
    - { name: bookings_list, description: "List bookings." }
    - { name: bookings_reschedule, description: "Reschedule a booking." }
    - { name: bookings_cancel, description: "Cancel a booking." }
    - { name: bookings_mark_completed, description: "Mark a booking completed." }
    - { name: bookings_mark_no_show, description: "Mark a booking no-show." }
    - { name: bookings_public_link, description: "Return a public booking link." }
    - { name: bookings_prepare_agent_context, description: "Return context for an AI-agent call." }
  ui_panels:
    - slot: project.page
      label: Bookings
      icon: calendar-plus
      entry: /ui/BookingsPanel.mjs
runtime:
  kind: source
  source: { repo: github.com/apteva/apps, ref: main, entry: mcp/bookings }
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/bookings.db
  migrations: migrations/
upgrade_policy: auto-patch
`

var (
	globalCtx       *sdk.AppCtx
	slugRe          = regexp.MustCompile(`[^a-z0-9]+`)
	bookingWriteMu  sync.Mutex
	publicRateLimit = newRequestLimiter()
)

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
		return errors.New("bookings requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("bookings mounted")
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/calendars", Handler: a.handleCalendars},
		{Pattern: "/booking-types", Handler: a.handleBookingTypes},
		{Pattern: "/booking-types/", Handler: a.handleBookingTypeItem},
		{Pattern: "/bookings", Handler: a.handleBookings},
		{Pattern: "/bookings/", Handler: a.handleBookingItem},
		{Pattern: "/public/", Handler: a.handlePublic, NoAuth: true},
		{Pattern: "/b/", Handler: a.handleTokenPage, NoAuth: true},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{Name: "booking_types_list", Description: "List booking types. Args: active? (default true), limit?.", InputSchema: schemaObject(map[string]any{
			"active": map[string]any{"type": "boolean"}, "limit": map[string]any{"type": "integer"},
		}, nil), Handler: a.toolBookingTypesList},
		{Name: "booking_types_get", Description: "Fetch a booking type by id or slug. Args: id? or slug?.", InputSchema: schemaObject(map[string]any{
			"id": map[string]any{"type": "integer"}, "slug": map[string]any{"type": "string"},
		}, nil), Handler: a.toolBookingTypesGet},
		{Name: "booking_types_create", Description: "Create a booking type. Args: title, duration_minutes?, slug?, description?, timezone?, calendar_ids?, destination_calendar_id?, location_kind?, location_value?, calls_enabled?, crm_enabled?, availability_rules?, intake_schema?.", InputSchema: schemaObject(map[string]any{
			"title": map[string]any{"type": "string"}, "duration_minutes": map[string]any{"type": "integer"}, "slug": map[string]any{"type": "string"}, "description": map[string]any{"type": "string"},
			"timezone": map[string]any{"type": "string"}, "calendar_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}}, "destination_calendar_id": map[string]any{"type": "integer"},
			"location_kind": map[string]any{"type": "string"}, "location_value": map[string]any{"type": "string"}, "calls_enabled": map[string]any{"type": "boolean"}, "crm_enabled": map[string]any{"type": "boolean"},
			"availability_rules": map[string]any{"type": "object"}, "intake_schema": map[string]any{"type": "array"}, "confirmation_policy": map[string]any{"type": "object"},
		}, []string{"title"}), Handler: a.toolBookingTypesCreate},
		{Name: "booking_types_update", Description: "Patch a booking type. Args: id plus any mutable fields.", InputSchema: schemaObject(map[string]any{
			"id": map[string]any{"type": "integer"}, "title": map[string]any{"type": "string"}, "slug": map[string]any{"type": "string"}, "description": map[string]any{"type": "string"},
			"duration_minutes": map[string]any{"type": "integer"}, "timezone": map[string]any{"type": "string"}, "calendar_ids": map[string]any{"type": "array", "items": map[string]any{"type": "integer"}}, "destination_calendar_id": map[string]any{"type": "integer"},
			"location_kind": map[string]any{"type": "string"}, "location_value": map[string]any{"type": "string"}, "calls_enabled": map[string]any{"type": "boolean"}, "crm_enabled": map[string]any{"type": "boolean"}, "active": map[string]any{"type": "boolean"},
			"availability_rules": map[string]any{"type": "object"}, "intake_schema": map[string]any{"type": "array"}, "confirmation_policy": map[string]any{"type": "object"},
		}, []string{"id"}), Handler: a.toolBookingTypesUpdate},
		{Name: "booking_types_archive", Description: "Archive a booking type. Args: id.", InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}), Handler: a.toolBookingTypesArchive},
		{Name: "bookings_find_slots", Description: "Find available slots for a booking type. Args: booking_type_id? or slug?, window_start?, window_end?, limit?.", InputSchema: schemaObject(map[string]any{
			"booking_type_id": map[string]any{"type": "integer"}, "slug": map[string]any{"type": "string"}, "window_start": map[string]any{"type": "string"}, "window_end": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer"},
		}, nil), Handler: a.toolBookingsFindSlots},
		{Name: "bookings_create", Description: "Create a booking after revalidating availability. Args: booking_type_id? or slug?, start_at, invitee_name?, invitee_email, invitee_phone?, intake_answers?, idempotency_key?.", InputSchema: schemaObject(map[string]any{
			"booking_type_id": map[string]any{"type": "integer"}, "slug": map[string]any{"type": "string"}, "start_at": map[string]any{"type": "string"}, "invitee_name": map[string]any{"type": "string"}, "invitee_email": map[string]any{"type": "string"}, "invitee_phone": map[string]any{"type": "string"}, "intake_answers": map[string]any{"type": "object"}, "source": map[string]any{"type": "string"}, "idempotency_key": map[string]any{"type": "string"},
		}, []string{"start_at"}), Handler: a.toolBookingsCreate},
		{Name: "bookings_get", Description: "Fetch one booking. Args: id.", InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}), Handler: a.toolBookingsGet},
		{Name: "bookings_list", Description: "List bookings. Args: status?, booking_type_id?, from?, to?, limit?.", InputSchema: schemaObject(map[string]any{
			"status": map[string]any{"type": "string"}, "booking_type_id": map[string]any{"type": "integer"}, "from": map[string]any{"type": "string"}, "to": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer"},
		}, nil), Handler: a.toolBookingsList},
		{Name: "bookings_reschedule", Description: "Reschedule a booking. Args: id, start_at.", InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}, "start_at": map[string]any{"type": "string"}}, []string{"id", "start_at"}), Handler: a.toolBookingsReschedule},
		{Name: "bookings_cancel", Description: "Cancel a booking and delete its Calendar event. Args: id, reason?.", InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}, "reason": map[string]any{"type": "string"}}, []string{"id"}), Handler: a.toolBookingsCancel},
		{Name: "bookings_mark_completed", Description: "Mark a booking completed. Args: id.", InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}), Handler: a.toolBookingsMarkCompleted},
		{Name: "bookings_mark_no_show", Description: "Mark a booking no-show. Args: id.", InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}), Handler: a.toolBookingsMarkNoShow},
		{Name: "bookings_public_link", Description: "Return the public link for a booking type. Args: id? or slug?.", InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}, "slug": map[string]any{"type": "string"}}, nil), Handler: a.toolBookingsPublicLink},
		{Name: "bookings_prepare_agent_context", Description: "Return booking + booking type + optional CRM context for an AI-agent call. Args: id.", InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}), Handler: a.toolBookingsPrepareAgentContext},
	}
}

func main() { sdk.Run(&App{}) }

type BookingType struct {
	ID                    int64           `json:"id"`
	ProjectID             string          `json:"project_id,omitempty"`
	Slug                  string          `json:"slug"`
	Title                 string          `json:"title"`
	Description           string          `json:"description"`
	DurationMinutes       int             `json:"duration_minutes"`
	Timezone              string          `json:"timezone"`
	LocationKind          string          `json:"location_kind"`
	LocationValue         string          `json:"location_value"`
	TargetKind            string          `json:"target_kind"`
	CalendarIDs           []int64         `json:"calendar_ids"`
	DestinationCalendarID int64           `json:"destination_calendar_id,omitempty"`
	AgentInstanceID       string          `json:"agent_instance_id"`
	CallsEnabled          bool            `json:"calls_enabled"`
	CRMEnabled            bool            `json:"crm_enabled"`
	Active                bool            `json:"active"`
	AvailabilityRules     json.RawMessage `json:"availability_rules"`
	IntakeSchema          json.RawMessage `json:"intake_schema"`
	ConfirmationPolicy    json.RawMessage `json:"confirmation_policy"`
	CreatedAt             string          `json:"created_at"`
	UpdatedAt             string          `json:"updated_at"`
	PublicURL             string          `json:"public_url,omitempty"`
}

type Booking struct {
	ID                      int64           `json:"id"`
	ProjectID               string          `json:"project_id,omitempty"`
	BookingTypeID           int64           `json:"booking_type_id"`
	Status                  string          `json:"status"`
	StartAt                 string          `json:"start_at"`
	EndAt                   string          `json:"end_at"`
	Timezone                string          `json:"timezone"`
	InviteeName             string          `json:"invitee_name"`
	InviteeEmail            string          `json:"invitee_email"`
	InviteePhone            string          `json:"invitee_phone"`
	IntakeAnswers           json.RawMessage `json:"intake_answers"`
	CalendarEventID         int64           `json:"calendar_event_id,omitempty"`
	CallsRoomID             int64           `json:"calls_room_id,omitempty"`
	CallsGuestJoinURL       string          `json:"calls_guest_join_url,omitempty"`
	CallsHostJoinURL        string          `json:"calls_host_join_url,omitempty"`
	CRMContactID            int64           `json:"crm_contact_id,omitempty"`
	AssignedTargetKind      string          `json:"assigned_target_kind"`
	AssignedAgentInstanceID string          `json:"assigned_agent_instance_id,omitempty"`
	CancellationToken       string          `json:"cancellation_token,omitempty"`
	RescheduleToken         string          `json:"reschedule_token,omitempty"`
	IdempotencyKey          string          `json:"-"`
	Source                  string          `json:"source"`
	CreatedAt               string          `json:"created_at"`
	UpdatedAt               string          `json:"updated_at"`
	PublicManageURL         string          `json:"public_manage_url,omitempty"`
}

type availabilityRules struct {
	WorkingHours        map[string]map[string]string `json:"working_hours"`
	BufferBeforeMinutes int                          `json:"buffer_before_minutes"`
	BufferAfterMinutes  int                          `json:"buffer_after_minutes"`
	MinimumNoticeMins   int                          `json:"minimum_notice_minutes"`
	BookingHorizonDays  int                          `json:"booking_horizon_days"`
}

type requestLimiter struct {
	mu      sync.Mutex
	buckets map[string][]time.Time
}

type publicBookingType struct {
	Slug            string `json:"slug"`
	Title           string `json:"title"`
	Description     string `json:"description"`
	DurationMinutes int    `json:"duration_minutes"`
	Timezone        string `json:"timezone"`
	LocationKind    string `json:"location_kind"`
	LocationValue   string `json:"location_value,omitempty"`
}

type publicBooking struct {
	ID              int64           `json:"id"`
	Status          string          `json:"status"`
	StartAt         string          `json:"start_at"`
	EndAt           string          `json:"end_at"`
	Timezone        string          `json:"timezone"`
	InviteeName     string          `json:"invitee_name"`
	InviteeEmail    string          `json:"invitee_email"`
	IntakeAnswers   json.RawMessage `json:"intake_answers"`
	GuestJoinURL    string          `json:"calls_guest_join_url,omitempty"`
	PublicManageURL string          `json:"public_manage_url"`
}

// ─── Tools: booking types ─────────────────────────────────────────

func (a *App) toolBookingTypesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	activeOnly := boolArg(args, "active", true)
	limit := intArg(args, "limit", 100)
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	where := "project_id=?"
	qargs := []any{pid}
	if activeOnly {
		where += " AND active=1"
	}
	qargs = append(qargs, limit)
	rows, err := ctx.AppDB().Query(`SELECT id, project_id, slug, title, description, duration_minutes, timezone, location_kind, location_value,
		       target_kind, calendar_ids, destination_calendar_id, agent_instance_id, calls_enabled, crm_enabled, active,
		       availability_rules, intake_schema, confirmation_policy, created_at, updated_at
		  FROM booking_types WHERE `+where+` ORDER BY title LIMIT ?`, qargs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*BookingType{}
	for rows.Next() {
		bt, err := scanBookingType(rows)
		if err != nil {
			return nil, err
		}
		if bt != nil {
			bt.PublicURL = publicBookingURL(ctx, pid, bt.Slug)
			out = append(out, bt)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{"booking_types": out, "count": len(out)}, nil
}

func (a *App) toolBookingTypesGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	bt, err := resolveBookingType(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	if bt == nil {
		return map[string]any{"booking_type": nil, "found": false}, nil
	}
	bt.PublicURL = publicBookingURL(ctx, pid, bt.Slug)
	return map[string]any{"booking_type": bt, "found": true}, nil
}

func (a *App) toolBookingTypesCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	title := strArg(args, "title")
	if title == "" {
		return nil, errors.New("title required")
	}
	if len(title) > 200 {
		return nil, errors.New("title must be at most 200 characters")
	}
	if len(strArg(args, "description")) > 10_000 {
		return nil, errors.New("description must be at most 10000 characters")
	}
	duration := intArg(args, "duration_minutes", 30)
	if duration < 5 || duration > 24*60 {
		return nil, errors.New("duration_minutes must be between 5 and 1440")
	}
	slug := slugify(strArg(args, "slug"))
	if strArg(args, "slug") == "" {
		slug = slugify(title)
	}
	slug, err = uniqueBookingTypeSlug(ctx, pid, slug, 0)
	if err != nil {
		return nil, err
	}
	availability, err := jsonStringArg(args, "availability_rules", defaultAvailabilityJSON())
	if err != nil {
		return nil, err
	}
	if err := validateAvailabilityJSON(availability); err != nil {
		return nil, err
	}
	intake, err := jsonStringArg(args, "intake_schema", "[]")
	if err != nil {
		return nil, err
	}
	policy, err := jsonStringArg(args, "confirmation_policy", "{}")
	if err != nil {
		return nil, err
	}
	calendarIDs := int64SliceArg(args, "calendar_ids")
	if err := validateCalendarIDs(calendarIDs, int64Arg(args, "destination_calendar_id")); err != nil {
		return nil, err
	}
	calIDs, err := json.Marshal(calendarIDs)
	if err != nil {
		return nil, err
	}
	allowedLocations := []string{"calls", "phone", "in_person", "external_url"}
	if requested := strArg(args, "location_kind"); requested != "" && !containsString(allowedLocations, requested) {
		return nil, fmt.Errorf("unsupported location_kind %q", requested)
	}
	locationKind := enumArg(args, "location_kind", "calls", allowedLocations)
	if requested := strArg(args, "target_kind"); requested != "" && requested != "human" {
		return nil, errors.New("target_kind currently supports only human")
	}
	timezone := strArgDefault(args, "timezone", "UTC")
	if len(timezone) > 128 {
		return nil, errors.New("timezone must be at most 128 characters")
	}
	if _, err := time.LoadLocation(timezone); err != nil {
		return nil, fmt.Errorf("timezone must be a valid IANA timezone: %w", err)
	}
	callsEnabled := boolArg(args, "calls_enabled", locationKind == "calls")
	if err := validateLocation(locationKind, strArg(args, "location_value"), callsEnabled); err != nil {
		return nil, err
	}
	res, err := ctx.AppDB().Exec(
		`INSERT INTO booking_types
		   (project_id, slug, title, description, duration_minutes, timezone, location_kind, location_value,
		    target_kind, calendar_ids, destination_calendar_id, agent_instance_id, calls_enabled, crm_enabled, active,
		    availability_rules, intake_schema, confirmation_policy)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'human', ?, ?, '', ?, ?, 1, ?, ?, ?)`,
		pid, slug, title, strArg(args, "description"), duration, timezone,
		locationKind, strArg(args, "location_value"), string(calIDs), int64Arg(args, "destination_calendar_id"),
		boolToInt(callsEnabled), boolToInt(boolArg(args, "crm_enabled", true)),
		availability, intake, policy,
	)
	if err != nil {
		return nil, fmt.Errorf("insert booking type: %w", err)
	}
	id, _ := res.LastInsertId()
	bt, err := loadBookingTypeByID(ctx, pid, id)
	if err != nil {
		return nil, err
	}
	bt.PublicURL = publicBookingURL(ctx, pid, bt.Slug)
	ctx.Emit("booking_type.created", map[string]any{"project_id": pid, "booking_type_id": id, "slug": slug})
	return map[string]any{"booking_type": bt}, nil
}

func (a *App) toolBookingTypesUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	bt, err := loadBookingTypeByID(ctx, pid, id)
	if err != nil {
		return nil, err
	}
	if bt == nil {
		return nil, errors.New("booking type not found")
	}
	prospectiveLocationKind := bt.LocationKind
	if _, ok := args["location_kind"]; ok {
		allowedLocations := []string{"calls", "phone", "in_person", "external_url"}
		requested := strArg(args, "location_kind")
		if !containsString(allowedLocations, requested) {
			return nil, fmt.Errorf("unsupported location_kind %q", requested)
		}
		prospectiveLocationKind = requested
	}
	prospectiveLocationValue := bt.LocationValue
	if _, ok := args["location_value"]; ok {
		prospectiveLocationValue = strArg(args, "location_value")
	}
	prospectiveCallsEnabled := bt.CallsEnabled
	if _, ok := args["calls_enabled"]; ok {
		prospectiveCallsEnabled = boolArg(args, "calls_enabled", false)
	}
	if err := validateLocation(prospectiveLocationKind, prospectiveLocationValue, prospectiveCallsEnabled); err != nil {
		return nil, err
	}
	sets := []string{}
	vals := []any{}
	add := func(col string, val any) {
		sets = append(sets, col+"=?")
		vals = append(vals, val)
	}
	for _, key := range []string{"title", "description", "location_value"} {
		if _, ok := args[key]; ok {
			value := strArg(args, key)
			if key == "title" && value == "" {
				return nil, errors.New("title cannot be empty")
			}
			if key == "title" && len(value) > 200 {
				return nil, errors.New("title must be at most 200 characters")
			}
			if key == "description" && len(value) > 10_000 {
				return nil, errors.New("description must be at most 10000 characters")
			}
			add(key, value)
		}
	}
	if _, ok := args["timezone"]; ok {
		tz := strArg(args, "timezone")
		if len(tz) > 128 {
			return nil, errors.New("timezone must be at most 128 characters")
		}
		if _, err := time.LoadLocation(tz); err != nil {
			return nil, fmt.Errorf("timezone must be a valid IANA timezone: %w", err)
		}
		add("timezone", tz)
	}
	if _, ok := args["slug"]; ok {
		slug := slugify(strArg(args, "slug"))
		if slug == "" {
			return nil, errors.New("slug cannot be empty")
		}
		uniq, err := uniqueBookingTypeSlug(ctx, pid, slug, id)
		if err != nil {
			return nil, err
		}
		add("slug", uniq)
	}
	if _, ok := args["duration_minutes"]; ok {
		duration := intArg(args, "duration_minutes", bt.DurationMinutes)
		if duration < 5 || duration > 24*60 {
			return nil, errors.New("duration_minutes must be between 5 and 1440")
		}
		add("duration_minutes", duration)
	}
	if _, ok := args["location_kind"]; ok {
		add("location_kind", enumArg(args, "location_kind", bt.LocationKind, []string{"calls", "phone", "in_person", "external_url"}))
	}
	if _, ok := args["target_kind"]; ok {
		if strArg(args, "target_kind") != "human" {
			return nil, errors.New("target_kind currently supports only human")
		}
		add("target_kind", "human")
	}
	if _, ok := args["calendar_ids"]; ok {
		calendarIDs := int64SliceArg(args, "calendar_ids")
		destinationID := bt.DestinationCalendarID
		if _, exists := args["destination_calendar_id"]; exists {
			destinationID = int64Arg(args, "destination_calendar_id")
		}
		if err := validateCalendarIDs(calendarIDs, destinationID); err != nil {
			return nil, err
		}
		b, _ := json.Marshal(calendarIDs)
		add("calendar_ids", string(b))
	}
	if _, ok := args["destination_calendar_id"]; ok {
		destinationID := int64Arg(args, "destination_calendar_id")
		if err := validateCalendarIDs(bt.CalendarIDs, destinationID); err != nil {
			return nil, err
		}
		add("destination_calendar_id", destinationID)
	}
	for _, key := range []string{"calls_enabled", "crm_enabled", "active"} {
		if _, ok := args[key]; ok {
			add(key, boolToInt(boolArg(args, key, false)))
		}
	}
	for _, key := range []string{"availability_rules", "intake_schema", "confirmation_policy"} {
		if _, ok := args[key]; ok {
			s, err := jsonStringArg(args, key, "{}")
			if err != nil {
				return nil, err
			}
			if key == "availability_rules" {
				if err := validateAvailabilityJSON(s); err != nil {
					return nil, err
				}
			}
			add(key, s)
		}
	}
	if len(sets) == 0 {
		return nil, errors.New("no fields to update")
	}
	sets = append(sets, "updated_at=CURRENT_TIMESTAMP")
	vals = append(vals, id, pid)
	if _, err := ctx.AppDB().Exec(`UPDATE booking_types SET `+strings.Join(sets, ", ")+` WHERE id=? AND project_id=?`, vals...); err != nil {
		return nil, err
	}
	bt, err = loadBookingTypeByID(ctx, pid, id)
	if err != nil {
		return nil, err
	}
	bt.PublicURL = publicBookingURL(ctx, pid, bt.Slug)
	ctx.Emit("booking_type.updated", map[string]any{"project_id": pid, "booking_type_id": id})
	return map[string]any{"booking_type": bt}, nil
}

func (a *App) toolBookingTypesArchive(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	args["active"] = false
	return a.toolBookingTypesUpdate(ctx, args)
}

// ─── Tools: bookings ──────────────────────────────────────────────

func (a *App) toolBookingsFindSlots(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	bt, err := resolveBookingType(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	if bt == nil || !bt.Active {
		return nil, errors.New("active booking type not found")
	}
	slots, err := findSlotsForType(ctx, pid, bt, args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"slots": slots, "booking_type": bt}, nil
}

func (a *App) toolBookingsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	bookingWriteMu.Lock()
	defer bookingWriteMu.Unlock()

	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	bt, err := resolveBookingType(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	if bt == nil || !bt.Active {
		return nil, errors.New("active booking type not found")
	}
	if bt.TargetKind != "human" {
		return nil, errors.New("this booking type uses an unsupported target_kind; change it to human")
	}
	if err := validateInvitee(args); err != nil {
		return nil, err
	}
	if _, exists := args["end_at"]; exists {
		return nil, errors.New("end_at is derived from the booking type and cannot be supplied")
	}
	idempotencyKey := strArg(args, "idempotency_key")
	if len(idempotencyKey) > 128 {
		return nil, errors.New("idempotency_key must be at most 128 characters")
	}
	if len(strArgDefault(args, "source", "manual")) > 80 {
		return nil, errors.New("source must be at most 80 characters")
	}
	if idempotencyKey != "" {
		existing, err := loadBookingByIdempotencyKey(ctx, pid, idempotencyKey)
		if err != nil {
			return nil, err
		}
		if existing != nil {
			return map[string]any{"booking": existing, "booking_type": bt, "idempotent_replay": true}, nil
		}
	}
	start, err := parseTimeArg(args, "start_at")
	if err != nil {
		return nil, err
	}
	end := start.Add(time.Duration(bt.DurationMinutes) * time.Minute)
	if err := validateRequestedSlot(ctx, pid, bt, start, 0); err != nil {
		return nil, err
	}
	answers, err := jsonStringArg(args, "intake_answers", "{}")
	if err != nil {
		return nil, err
	}
	if len(answers) > 64*1024 {
		return nil, errors.New("intake_answers must be at most 64 KiB")
	}
	cancelToken := randomToken()
	rescheduleToken := randomToken()
	res, err := ctx.AppDB().Exec(
		`INSERT INTO bookings
		   (project_id, booking_type_id, status, start_at, end_at, timezone, invitee_name, invitee_email,
		    invitee_phone, intake_answers, assigned_target_kind, assigned_agent_instance_id,
		    cancellation_token, reschedule_token, source, idempotency_key)
		 VALUES (?, ?, 'confirmed', ?, ?, ?, ?, ?, ?, ?, 'human', '', ?, ?, ?, ?)`,
		pid, bt.ID, start.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339), bt.Timezone,
		strArg(args, "invitee_name"), strArg(args, "invitee_email"), strArg(args, "invitee_phone"), answers,
		cancelToken, rescheduleToken, strArgDefault(args, "source", "manual"), idempotencyKey,
	)
	if err != nil {
		return nil, fmt.Errorf("insert booking: %w", err)
	}
	id, _ := res.LastInsertId()
	b, err := loadBookingByID(ctx, pid, id)
	if err != nil || b == nil {
		_, _ = ctx.AppDB().Exec(`DELETE FROM bookings WHERE id=? AND project_id=?`, id, pid)
		if err == nil {
			err = errors.New("inserted booking could not be reloaded")
		}
		return nil, err
	}
	if err := a.attachOptionalCalls(ctx, pid, bt, b); err != nil {
		_, _ = ctx.AppDB().Exec(`DELETE FROM bookings WHERE id=? AND project_id=?`, id, pid)
		return nil, fmt.Errorf("create Calls room: %w", err)
	}
	eventID, err := a.createCalendarEvent(ctx, pid, bt, b)
	if err != nil {
		a.cleanupFailedBooking(ctx, pid, b, 0)
		return nil, err
	}
	if _, err := ctx.AppDB().Exec(`UPDATE bookings SET calendar_event_id=?, updated_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=?`, eventID, id, pid); err != nil {
		a.cleanupFailedBooking(ctx, pid, b, eventID)
		return nil, err
	}
	b.CalendarEventID = eventID
	if err := a.attachOptionalCRM(ctx, pid, bt, b); err != nil {
		ctx.Logger().Warn("bookings crm attach failed", "err", err.Error(), "booking_id", id)
	}
	b, err = loadBookingByID(ctx, pid, id)
	if err != nil {
		return nil, err
	}
	a.recordEvent(ctx, pid, id, "created", map[string]any{"source": b.Source})
	a.emitBooking(ctx, pid, "booking.created", b)
	return map[string]any{"booking": b, "booking_type": bt}, nil
}

func (a *App) toolBookingsGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	b, err := loadBookingByID(ctx, pid, id)
	if err != nil {
		return nil, err
	}
	if b == nil {
		return map[string]any{"booking": nil, "found": false}, nil
	}
	return map[string]any{"booking": b, "found": true}, nil
}

func (a *App) toolBookingsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	where := []string{"project_id=?"}
	qargs := []any{pid}
	if status := strArg(args, "status"); status != "" {
		where = append(where, "status=?")
		qargs = append(qargs, status)
	}
	if tid := int64Arg(args, "booking_type_id"); tid > 0 {
		where = append(where, "booking_type_id=?")
		qargs = append(qargs, tid)
	}
	if from := strArg(args, "from"); from != "" {
		t, err := time.Parse(time.RFC3339, from)
		if err != nil {
			return nil, fmt.Errorf("from: %w", err)
		}
		where = append(where, "start_at>=?")
		qargs = append(qargs, t.UTC().Format(time.RFC3339))
	}
	if to := strArg(args, "to"); to != "" {
		t, err := time.Parse(time.RFC3339, to)
		if err != nil {
			return nil, fmt.Errorf("to: %w", err)
		}
		where = append(where, "start_at<=?")
		qargs = append(qargs, t.UTC().Format(time.RFC3339))
	}
	limit := intArg(args, "limit", 100)
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	qargs = append(qargs, limit)
	rows, err := ctx.AppDB().Query(`SELECT id, project_id, booking_type_id, status, start_at, end_at, timezone,
		       invitee_name, invitee_email, invitee_phone, intake_answers,
		       calendar_event_id, calls_room_id, calls_guest_join_url, calls_host_join_url, crm_contact_id,
		       assigned_target_kind, assigned_agent_instance_id, cancellation_token, reschedule_token,
		       source, idempotency_key, created_at, updated_at
		  FROM bookings WHERE `+strings.Join(where, " AND ")+` ORDER BY start_at DESC LIMIT ?`, qargs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*Booking{}
	for rows.Next() {
		b, err := scanBooking(ctx, rows)
		if err != nil {
			return nil, err
		}
		if b != nil {
			out = append(out, b)
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{"bookings": out, "count": len(out)}, nil
}

func (a *App) toolBookingsReschedule(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	bookingWriteMu.Lock()
	defer bookingWriteMu.Unlock()

	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	b, err := loadBookingByID(ctx, pid, id)
	if err != nil {
		return nil, err
	}
	if b == nil {
		return nil, errors.New("booking not found")
	}
	if b.Status != "confirmed" && b.Status != "rescheduled" {
		return nil, fmt.Errorf("cannot reschedule a %s booking", b.Status)
	}
	bt, err := loadBookingTypeByID(ctx, pid, b.BookingTypeID)
	if err != nil {
		return nil, err
	}
	start, err := parseTimeArg(args, "start_at")
	if err != nil {
		return nil, err
	}
	end := start.Add(time.Duration(bt.DurationMinutes) * time.Minute)
	if err := validateRequestedSlot(ctx, pid, bt, start, b.CalendarEventID); err != nil {
		return nil, err
	}
	oldStart, _ := time.Parse(time.RFC3339, b.StartAt)
	oldEnd, _ := time.Parse(time.RFC3339, b.EndAt)
	createdEvent := false
	if b.CalendarEventID > 0 {
		if err := callCalendarUpdateEvent(ctx, pid, b.CalendarEventID, calendarTitle(bt, b), start, end, calendarLocation(bt, b), calendarDescription(ctx, bt, b)); err != nil {
			return nil, err
		}
	} else {
		b.StartAt = start.UTC().Format(time.RFC3339)
		b.EndAt = end.UTC().Format(time.RFC3339)
		eventID, err := a.createCalendarEvent(ctx, pid, bt, b)
		if err != nil {
			return nil, err
		}
		b.CalendarEventID = eventID
		createdEvent = true
	}
	if _, err := ctx.AppDB().Exec(`UPDATE bookings SET status='rescheduled', start_at=?, end_at=?, updated_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=?`,
		start.UTC().Format(time.RFC3339), end.UTC().Format(time.RFC3339), id, pid); err != nil {
		if createdEvent {
			_ = callCalendarDeleteEvent(ctx, pid, b.CalendarEventID)
		} else if b.CalendarEventID > 0 {
			_ = callCalendarUpdateEvent(ctx, pid, b.CalendarEventID, calendarTitle(bt, b), oldStart, oldEnd, calendarLocation(bt, b), calendarDescription(ctx, bt, b))
		}
		return nil, err
	}
	b, _ = loadBookingByID(ctx, pid, id)
	a.recordEvent(ctx, pid, id, "rescheduled", map[string]any{"start_at": b.StartAt, "end_at": b.EndAt})
	a.emitBooking(ctx, pid, "booking.rescheduled", b)
	return map[string]any{"booking": b}, nil
}

func (a *App) toolBookingsCancel(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	bookingWriteMu.Lock()
	defer bookingWriteMu.Unlock()

	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	b, err := loadBookingByID(ctx, pid, id)
	if err != nil {
		return nil, err
	}
	if b == nil {
		return nil, errors.New("booking not found")
	}
	if b.Status == "cancelled" {
		return map[string]any{"booking": b, "idempotent_replay": true}, nil
	}
	if b.Status != "confirmed" && b.Status != "rescheduled" {
		return nil, fmt.Errorf("cannot cancel a %s booking", b.Status)
	}
	if b.CalendarEventID > 0 {
		if err := callCalendarDeleteEvent(ctx, pid, b.CalendarEventID); err != nil && !crossAppNotFound(err) {
			return nil, fmt.Errorf("delete Calendar event: %w", err)
		}
		if _, err := ctx.AppDB().Exec(`UPDATE bookings SET calendar_event_id=NULL WHERE id=? AND project_id=?`, id, pid); err != nil {
			return nil, err
		}
	}
	if b.CallsRoomID > 0 {
		if err := endCallsRoom(ctx, pid, b.CallsRoomID); err != nil && !crossAppNotFound(err) {
			return nil, fmt.Errorf("end Calls room: %w", err)
		}
		if _, err := ctx.AppDB().Exec(`UPDATE bookings SET calls_room_id=NULL, calls_guest_join_url='', calls_host_join_url='' WHERE id=? AND project_id=?`, id, pid); err != nil {
			return nil, err
		}
	}
	if _, err := ctx.AppDB().Exec(`UPDATE bookings SET status='cancelled', updated_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=?`, id, pid); err != nil {
		return nil, err
	}
	b, _ = loadBookingByID(ctx, pid, id)
	a.recordEvent(ctx, pid, id, "cancelled", map[string]any{"reason": strArg(args, "reason")})
	a.emitBooking(ctx, pid, "booking.cancelled", b)
	return map[string]any{"booking": b}, nil
}

func (a *App) toolBookingsMarkCompleted(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return a.setBookingStatus(ctx, args, "completed", "booking.completed")
}

func (a *App) toolBookingsMarkNoShow(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return a.setBookingStatus(ctx, args, "no_show", "booking.no_show")
}

func (a *App) toolBookingsPublicLink(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	bt, err := resolveBookingType(ctx, pid, args)
	if err != nil {
		return nil, err
	}
	if bt == nil {
		return nil, errors.New("booking type not found")
	}
	return map[string]any{"url": publicBookingURL(ctx, pid, bt.Slug), "booking_type": bt}, nil
}

func (a *App) toolBookingsPrepareAgentContext(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	b, err := loadBookingByID(ctx, pid, id)
	if err != nil {
		return nil, err
	}
	if b == nil {
		return nil, errors.New("booking not found")
	}
	bt, err := loadBookingTypeByID(ctx, pid, b.BookingTypeID)
	if err != nil {
		return nil, err
	}
	out := map[string]any{"booking": b, "booking_type": bt}
	if b.CRMContactID > 0 && ctx.PlatformAPI() != nil {
		var crm map[string]any
		if err := ctx.WithProject(pid).PlatformAPI().CallAppResult("crm", "contacts_get_context", map[string]any{"id": b.CRMContactID}, &crm); err == nil {
			out["crm_context"] = crm
		}
	}
	if b.CallsRoomID > 0 && ctx.PlatformAPI() != nil {
		var calls map[string]any
		if err := ctx.WithProject(pid).PlatformAPI().CallAppResult("calls", "calls_get_room", map[string]any{"id": b.CallsRoomID}, &calls); err == nil {
			out["calls_room"] = calls
		}
	}
	return out, nil
}

func (a *App) setBookingStatus(ctx *sdk.AppCtx, args map[string]any, status, topic string) (any, error) {
	bookingWriteMu.Lock()
	defer bookingWriteMu.Unlock()

	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	b, err := loadBookingByID(ctx, pid, id)
	if err != nil {
		return nil, err
	}
	if b == nil {
		return nil, errors.New("booking not found")
	}
	if b.Status == status {
		return map[string]any{"booking": b, "idempotent_replay": true}, nil
	}
	if b.Status != "confirmed" && b.Status != "rescheduled" {
		return nil, fmt.Errorf("cannot mark a %s booking as %s", b.Status, status)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE bookings SET status=?, updated_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=?`, status, id, pid); err != nil {
		return nil, err
	}
	b, err = loadBookingByID(ctx, pid, id)
	if err != nil {
		return nil, err
	}
	a.recordEvent(ctx, pid, id, status, nil)
	a.emitBooking(ctx, pid, topic, b)
	return map[string]any{"booking": b}, nil
}

// ─── Orchestration ────────────────────────────────────────────────

func findSlotsForType(ctx *sdk.AppCtx, pid string, bt *BookingType, args map[string]any) ([]map[string]string, error) {
	rules := parseRules(bt.AvailabilityRules)
	now := time.Now().UTC()
	startFloor := now.Add(time.Duration(rules.MinimumNoticeMins) * time.Minute)
	endCeiling := now.AddDate(0, 0, rules.BookingHorizonDays)
	start := startFloor
	end := endCeiling
	if s := strArg(args, "window_start"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return nil, fmt.Errorf("window_start: %w", err)
		}
		if t.After(start) {
			start = t
		}
	}
	if s := strArg(args, "window_end"); s != "" {
		t, err := time.Parse(time.RFC3339, s)
		if err != nil {
			return nil, fmt.Errorf("window_end: %w", err)
		}
		if t.Before(end) {
			end = t
		}
	}
	if !end.After(start) {
		return []map[string]string{}, nil
	}
	limit := intArg(args, "limit", 20)
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	loc, err := time.LoadLocation(bt.Timezone)
	if err != nil {
		return nil, fmt.Errorf("invalid booking timezone %q: %w", bt.Timezone, err)
	}
	duration := time.Duration(bt.DurationMinutes) * time.Minute
	calendarIDs, err := availabilityCalendarIDs(ctx, pid, bt)
	if err != nil {
		return nil, err
	}
	busy, err := calendarBusyRanges(ctx, pid, calendarIDs, start, end, rules, 0)
	if err != nil {
		return nil, err
	}
	out := make([]map[string]string, 0, limit)
	for cursor := snapToQuarterHour(start); !cursor.Add(duration).After(end); cursor = cursor.Add(15 * time.Minute) {
		candidateEnd := cursor.Add(duration)
		if !withinWorkingHours(cursor, candidateEnd, loc, rules.WorkingHours) || overlapsRanges(cursor, candidateEnd, busy) {
			continue
		}
		out = append(out, map[string]string{
			"start": cursor.UTC().Format(time.RFC3339),
			"end":   candidateEnd.UTC().Format(time.RFC3339),
		})
		if len(out) >= limit {
			break
		}
	}
	return out, nil
}

type busyRange struct{ start, end time.Time }

func validateRequestedSlot(ctx *sdk.AppCtx, pid string, bt *BookingType, start time.Time, excludeEventID int64) error {
	rules := parseRules(bt.AvailabilityRules)
	now := time.Now().UTC()
	start = start.UTC()
	end := start.Add(time.Duration(bt.DurationMinutes) * time.Minute)
	if start.Before(now.Add(time.Duration(rules.MinimumNoticeMins) * time.Minute)) {
		return errors.New("requested slot does not satisfy minimum notice")
	}
	if end.After(now.AddDate(0, 0, rules.BookingHorizonDays)) {
		return errors.New("requested slot is beyond the booking horizon")
	}
	if start.Second() != 0 || start.Nanosecond() != 0 || start.Minute()%15 != 0 {
		return errors.New("requested slot must start on a 15-minute boundary")
	}
	loc, err := time.LoadLocation(bt.Timezone)
	if err != nil {
		return fmt.Errorf("invalid booking timezone %q: %w", bt.Timezone, err)
	}
	if !withinWorkingHours(start, end, loc, rules.WorkingHours) {
		return errors.New("requested slot is outside working hours")
	}
	calendarIDs, err := availabilityCalendarIDs(ctx, pid, bt)
	if err != nil {
		return err
	}
	busy, err := calendarBusyRanges(ctx, pid, calendarIDs, start, end, rules, excludeEventID)
	if err != nil {
		return err
	}
	if overlapsRanges(start, end, busy) {
		return errors.New("requested slot is no longer available")
	}
	return nil
}

func calendarBusyRanges(ctx *sdk.AppCtx, pid string, calendarIDs []int64, start, end time.Time, rules availabilityRules, excludeEventID int64) ([]busyRange, error) {
	if ctx.PlatformAPI() == nil {
		return nil, errors.New("calendar app call requires PlatformAPI")
	}
	queryStart := start.Add(-time.Duration(rules.BufferAfterMinutes) * time.Minute)
	queryEnd := end.Add(time.Duration(rules.BufferBeforeMinutes) * time.Minute)
	input := map[string]any{"from": queryStart.UTC().Format(time.RFC3339), "to": queryEnd.UTC().Format(time.RFC3339)}
	if len(calendarIDs) > 0 {
		input["calendar_ids"] = calendarIDs
	}
	var got struct {
		Events []struct {
			ID      int64  `json:"id"`
			EventID int64  `json:"event_id"`
			StartAt string `json:"start_at"`
			EndAt   string `json:"end_at"`
		} `json:"events"`
	}
	if err := ctx.WithProject(pid).PlatformAPI().CallAppResult("calendar", "events_list", input, &got); err != nil {
		return nil, fmt.Errorf("calendar.events_list: %w", err)
	}
	busy := make([]busyRange, 0, len(got.Events))
	for _, event := range got.Events {
		id := event.EventID
		if id == 0 {
			id = event.ID
		}
		if excludeEventID > 0 && id == excludeEventID {
			continue
		}
		eventStart, err1 := time.Parse(time.RFC3339, event.StartAt)
		eventEnd, err2 := time.Parse(time.RFC3339, event.EndAt)
		if err1 != nil || err2 != nil || !eventEnd.After(eventStart) {
			continue
		}
		busy = append(busy, busyRange{
			start: eventStart.Add(-time.Duration(rules.BufferBeforeMinutes) * time.Minute),
			end:   eventEnd.Add(time.Duration(rules.BufferAfterMinutes) * time.Minute),
		})
	}
	return busy, nil
}

func snapToQuarterHour(t time.Time) time.Time {
	rounded := t.UTC().Truncate(15 * time.Minute)
	if rounded.Before(t.UTC()) {
		rounded = rounded.Add(15 * time.Minute)
	}
	return rounded
}

func withinWorkingHours(start, end time.Time, loc *time.Location, hours map[string]map[string]string) bool {
	localStart := start.In(loc)
	localEnd := end.In(loc)
	if localStart.Year() != localEnd.Year() || localStart.YearDay() != localEnd.YearDay() {
		return false
	}
	day := []string{"sun", "mon", "tue", "wed", "thu", "fri", "sat"}[int(localStart.Weekday())]
	rangeForDay, ok := hours[day]
	if !ok {
		return false
	}
	startMinute, okStart := hhmmMinutes(rangeForDay["start"])
	endMinute, okEnd := hhmmMinutes(rangeForDay["end"])
	if !okStart || !okEnd || startMinute >= endMinute {
		return false
	}
	candidateStart := localStart.Hour()*60 + localStart.Minute()
	candidateEnd := localEnd.Hour()*60 + localEnd.Minute()
	return candidateStart >= startMinute && candidateEnd <= endMinute
}

func overlapsRanges(start, end time.Time, ranges []busyRange) bool {
	for _, item := range ranges {
		if start.Before(item.end) && item.start.Before(end) {
			return true
		}
	}
	return false
}

func availabilityCalendarIDs(ctx *sdk.AppCtx, pid string, bt *BookingType) ([]int64, error) {
	if len(bt.CalendarIDs) == 0 {
		return nil, nil // An omitted Calendar filter means every enabled calendar.
	}
	ids := append([]int64(nil), bt.CalendarIDs...)
	destinationID := bt.DestinationCalendarID
	if destinationID == 0 {
		var err error
		destinationID, err = getOrCreateBookingsCalendar(ctx, pid)
		if err != nil {
			return nil, err
		}
		bt.DestinationCalendarID = destinationID
		if _, err := ctx.AppDB().Exec(`UPDATE booking_types SET destination_calendar_id=?, updated_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=?`, destinationID, bt.ID, pid); err != nil {
			return nil, err
		}
	}
	if destinationID > 0 {
		found := false
		for _, id := range ids {
			if id == destinationID {
				found = true
				break
			}
		}
		if !found {
			ids = append(ids, destinationID)
		}
	}
	return ids, nil
}

func (a *App) attachOptionalCRM(ctx *sdk.AppCtx, pid string, bt *BookingType, b *Booking) error {
	if !bt.CRMEnabled || b == nil || b.InviteeEmail == "" || ctx.PlatformAPI() == nil {
		return nil
	}
	defaults := map[string]any{
		"display_name": b.InviteeName,
		"source":       "bookings",
	}
	if b.InviteePhone != "" {
		defaults["primary_phone"] = b.InviteePhone
	}
	var got struct {
		Contact struct {
			ID int64 `json:"id"`
		} `json:"contact"`
		WasCreated bool `json:"was_created"`
	}
	if err := ctx.WithProject(pid).PlatformAPI().CallAppResult("crm", "contacts_upsert_by_channel", map[string]any{
		"kind": "email", "value": b.InviteeEmail, "defaults": defaults, "source": "bookings",
	}, &got); err != nil {
		return err
	}
	if got.Contact.ID > 0 {
		if _, err := ctx.AppDB().Exec(`UPDATE bookings SET crm_contact_id=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`, got.Contact.ID, b.ID); err != nil {
			return err
		}
		b.CRMContactID = got.Contact.ID
		_ = ctx.WithProject(pid).PlatformAPI().CallAppResult("crm", "contacts_log_activity", map[string]any{
			"contact_id": got.Contact.ID,
			"kind":       "meeting",
			"body":       fmt.Sprintf("Booked %s for %s", bt.Title, b.StartAt),
			"source":     "bookings",
		}, &map[string]any{})
	}
	return nil
}

func (a *App) attachOptionalCalls(ctx *sdk.AppCtx, pid string, bt *BookingType, b *Booking) error {
	if !bt.CallsEnabled || bt.LocationKind != "calls" || b == nil || ctx.PlatformAPI() == nil {
		return nil
	}
	var roomOut struct {
		Room struct {
			ID int64 `json:"id"`
		} `json:"room"`
		HostJoinURL string `json:"host_join_url"`
	}
	meta := map[string]any{"booking_id": b.ID, "booking_type_id": bt.ID, "target_kind": bt.TargetKind}
	if err := ctx.WithProject(pid).PlatformAPI().CallAppResult("calls", "calls_create_room", map[string]any{
		"title":    calendarTitle(bt, b),
		"metadata": meta,
	}, &roomOut); err != nil {
		return err
	}
	if roomOut.Room.ID == 0 {
		return errors.New("calls.calls_create_room returned no room")
	}
	var guestOut struct {
		JoinURL string `json:"join_url"`
	}
	if err := ctx.WithProject(pid).PlatformAPI().CallAppResult("calls", "calls_create_join_token", map[string]any{
		"room_id":          roomOut.Room.ID,
		"participant_kind": "human",
		"role":             "guest",
		"display_name":     b.InviteeName,
		"max_uses":         1,
		"capabilities":     map[string]any{"audio": true, "video": true, "chat": true},
	}, &guestOut); err != nil {
		_ = endCallsRoom(ctx, pid, roomOut.Room.ID)
		return fmt.Errorf("calls.calls_create_join_token: %w", err)
	}
	if guestOut.JoinURL == "" {
		_ = endCallsRoom(ctx, pid, roomOut.Room.ID)
		return errors.New("calls.calls_create_join_token returned no guest join URL")
	}
	if _, err := ctx.AppDB().Exec(
		`UPDATE bookings SET calls_room_id=?, calls_guest_join_url=?, calls_host_join_url=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
		roomOut.Room.ID, guestOut.JoinURL, roomOut.HostJoinURL, b.ID,
	); err != nil {
		_ = endCallsRoom(ctx, pid, roomOut.Room.ID)
		return err
	}
	b.CallsRoomID = roomOut.Room.ID
	b.CallsGuestJoinURL = guestOut.JoinURL
	b.CallsHostJoinURL = roomOut.HostJoinURL
	return nil
}

func (a *App) createCalendarEvent(ctx *sdk.AppCtx, pid string, bt *BookingType, b *Booking) (int64, error) {
	if ctx.PlatformAPI() == nil {
		return 0, errors.New("calendar app call requires PlatformAPI")
	}
	calID := bt.DestinationCalendarID
	if calID == 0 && len(bt.CalendarIDs) > 0 {
		calID = bt.CalendarIDs[0]
	}
	if calID == 0 {
		id, err := getOrCreateBookingsCalendar(ctx, pid)
		if err != nil {
			return 0, err
		}
		calID = id
		bt.DestinationCalendarID = id
		if _, err := ctx.AppDB().Exec(`UPDATE booking_types SET destination_calendar_id=?, updated_at=CURRENT_TIMESTAMP WHERE id=? AND project_id=?`, id, bt.ID, pid); err != nil {
			return 0, err
		}
	}
	start, _ := time.Parse(time.RFC3339, b.StartAt)
	end, _ := time.Parse(time.RFC3339, b.EndAt)
	eventID, err := callCalendarCreateEvent(ctx, pid, calID, calendarTitle(bt, b), start, end, calendarLocation(bt, b), calendarDescription(ctx, bt, b))
	if err != nil {
		return 0, err
	}
	return eventID, nil
}

func (a *App) cleanupFailedBooking(ctx *sdk.AppCtx, pid string, b *Booking, eventID int64) {
	if eventID > 0 {
		_ = callCalendarDeleteEvent(ctx, pid, eventID)
	}
	if b != nil && b.CallsRoomID > 0 {
		_ = endCallsRoom(ctx, pid, b.CallsRoomID)
	}
	if b != nil {
		_, _ = ctx.AppDB().Exec(`DELETE FROM bookings WHERE id=? AND project_id=?`, b.ID, pid)
	}
}

func endCallsRoom(ctx *sdk.AppCtx, pid string, roomID int64) error {
	if roomID == 0 || ctx.PlatformAPI() == nil {
		return nil
	}
	var out map[string]any
	return ctx.WithProject(pid).PlatformAPI().CallAppResult("calls", "calls_end_room", map[string]any{"id": roomID}, &out)
}

func getOrCreateBookingsCalendar(ctx *sdk.AppCtx, pid string) (int64, error) {
	var list struct {
		Calendars []struct {
			ID      int64  `json:"id"`
			Name    string `json:"name"`
			Enabled bool   `json:"enabled"`
		} `json:"calendars"`
	}
	if err := ctx.WithProject(pid).PlatformAPI().CallAppResult("calendar", "calendars_list", map[string]any{}, &list); err != nil {
		return 0, fmt.Errorf("calendar.calendars_list: %w", err)
	}
	for _, c := range list.Calendars {
		if c.Name == "Bookings" {
			if !c.Enabled {
				var updated map[string]any
				if err := ctx.WithProject(pid).PlatformAPI().CallAppResult("calendar", "calendars_update", map[string]any{
					"id": c.ID, "enabled": true,
				}, &updated); err != nil {
					return 0, fmt.Errorf("calendar.calendars_update: %w", err)
				}
			}
			return c.ID, nil
		}
	}
	var created struct {
		ID int64 `json:"id"`
	}
	if err := ctx.WithProject(pid).PlatformAPI().CallAppResult("calendar", "calendars_create", map[string]any{
		"name": "Bookings", "color": "#0ea5e9", "kind": "work",
	}, &created); err != nil {
		return 0, fmt.Errorf("calendar.calendars_create: %w", err)
	}
	if created.ID == 0 {
		return 0, errors.New("calendar.calendars_create returned no id")
	}
	return created.ID, nil
}

func callCalendarCreateEvent(ctx *sdk.AppCtx, pid string, calendarID int64, title string, start, end time.Time, location, description string) (int64, error) {
	var got struct {
		ID int64 `json:"id"`
	}
	if err := ctx.WithProject(pid).PlatformAPI().CallAppResult("calendar", "events_create", map[string]any{
		"calendar_id": calendarID,
		"title":       title,
		"start_at":    start.UTC().Format(time.RFC3339),
		"end_at":      end.UTC().Format(time.RFC3339),
		"location":    location,
		"description": description,
	}, &got); err != nil {
		return 0, fmt.Errorf("calendar.events_create: %w", err)
	}
	if got.ID == 0 {
		return 0, errors.New("calendar.events_create returned no id")
	}
	return got.ID, nil
}

func callCalendarUpdateEvent(ctx *sdk.AppCtx, pid string, eventID int64, title string, start, end time.Time, location, description string) error {
	var got map[string]any
	return ctx.WithProject(pid).PlatformAPI().CallAppResult("calendar", "events_update", map[string]any{
		"event_id": eventID, "scope": "all", "title": title,
		"start_at": start.UTC().Format(time.RFC3339), "end_at": end.UTC().Format(time.RFC3339),
		"location": location, "description": description,
	}, &got)
}

func callCalendarDeleteEvent(ctx *sdk.AppCtx, pid string, eventID int64) error {
	var got map[string]any
	return ctx.WithProject(pid).PlatformAPI().CallAppResult("calendar", "events_delete", map[string]any{"event_id": eventID, "scope": "all"}, &got)
}

func calendarTitle(bt *BookingType, b *Booking) string {
	name := strings.TrimSpace(b.InviteeName)
	if name == "" {
		name = strings.TrimSpace(b.InviteeEmail)
	}
	if name == "" {
		name = "Guest"
	}
	return bt.Title + " with " + name
}

func calendarLocation(bt *BookingType, b *Booking) string {
	if bt.LocationKind == "calls" {
		if b.CallsGuestJoinURL != "" {
			return b.CallsGuestJoinURL
		}
		return "Calls"
	}
	return bt.LocationValue
}

func calendarDescription(ctx *sdk.AppCtx, bt *BookingType, b *Booking) string {
	lines := []string{
		"Booked via Apteva Bookings.",
		fmt.Sprintf("Booking ID: %d", b.ID),
		fmt.Sprintf("Booking type: %s", bt.Title),
	}
	if b.InviteeEmail != "" {
		lines = append(lines, "Email: "+b.InviteeEmail)
	}
	if b.InviteePhone != "" {
		lines = append(lines, "Phone: "+b.InviteePhone)
	}
	if b.CallsGuestJoinURL != "" {
		lines = append(lines, "Join: "+b.CallsGuestJoinURL)
	}
	if b.AssignedAgentInstanceID != "" {
		lines = append(lines, "Agent: "+b.AssignedAgentInstanceID)
	}
	lines = append(lines, "Manage: "+publicManageURL(ctx, b.ProjectID, b.CancellationToken))
	return strings.Join(lines, "\n")
}

// ─── HTTP handlers ────────────────────────────────────────────────

func (a *App) handleCalendars(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "GET")
		return
	}
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if globalCtx.PlatformAPI() == nil {
		httpErr(w, http.StatusBadRequest, "calendar app call requires PlatformAPI")
		return
	}
	var out map[string]any
	if err := globalCtx.WithProject(pid).PlatformAPI().CallAppResult("calendar", "calendars_list", map[string]any{}, &out); err != nil {
		httpErr(w, http.StatusBadRequest, "calendar.calendars_list: "+err.Error())
		return
	}
	writeToolOut(w, out, nil)
}

func (a *App) handleBookingTypes(w http.ResponseWriter, r *http.Request) {
	args, err := argsFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		out, err := a.toolBookingTypesList(globalCtx, args)
		writeToolOut(w, out, err)
	case http.MethodPost:
		out, err := a.toolBookingTypesCreate(globalCtx, args)
		writeToolOut(w, out, err)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleBookingTypeItem(w http.ResponseWriter, r *http.Request) {
	id := idFromPath(r.URL.Path, "/booking-types/")
	args, err := argsFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	args["id"] = id
	switch r.Method {
	case http.MethodGet:
		out, err := a.toolBookingTypesGet(globalCtx, args)
		writeToolOut(w, out, err)
	case http.MethodPatch, http.MethodPut:
		out, err := a.toolBookingTypesUpdate(globalCtx, args)
		writeToolOut(w, out, err)
	case http.MethodDelete:
		out, err := a.toolBookingTypesArchive(globalCtx, args)
		writeToolOut(w, out, err)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleBookings(w http.ResponseWriter, r *http.Request) {
	args, err := argsFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		out, err := a.toolBookingsList(globalCtx, args)
		writeToolOut(w, out, err)
	case http.MethodPost:
		out, err := a.toolBookingsCreate(globalCtx, args)
		writeToolOut(w, out, err)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleBookingItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/bookings/"), "/")
	parts := strings.Split(rest, "/")
	if len(parts) == 0 || parts[0] == "" {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	id, _ := strconv.ParseInt(parts[0], 10, 64)
	args, err := argsFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	args["id"] = id
	if len(parts) == 1 {
		if r.Method != http.MethodGet {
			httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		out, err := a.toolBookingsGet(globalCtx, args)
		writeToolOut(w, out, err)
		return
	}
	if r.Method != http.MethodPost && r.Method != http.MethodPatch {
		httpErr(w, http.StatusMethodNotAllowed, "POST or PATCH required")
		return
	}
	switch parts[1] {
	case "cancel":
		out, err := a.toolBookingsCancel(globalCtx, args)
		writeToolOut(w, out, err)
	case "reschedule":
		out, err := a.toolBookingsReschedule(globalCtx, args)
		writeToolOut(w, out, err)
	case "complete":
		out, err := a.toolBookingsMarkCompleted(globalCtx, args)
		writeToolOut(w, out, err)
	case "no-show":
		out, err := a.toolBookingsMarkNoShow(globalCtx, args)
		writeToolOut(w, out, err)
	default:
		httpErr(w, http.StatusNotFound, "not found")
	}
}

func (a *App) handlePublic(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/public/"), "/")
	parts := strings.Split(rest, "/")
	if len(parts) == 0 || parts[0] == "" {
		httpErr(w, http.StatusNotFound, "booking type not found")
		return
	}
	slug := parts[0]
	pid, err := resolveProjectFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	args, err := argsFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	args["_project_id"] = pid
	args["slug"] = slug
	if len(parts) == 1 {
		if r.Method == http.MethodGet && wantsJSON(r) {
			bt, err := loadBookingTypeBySlug(globalCtx, pid, slug)
			if err != nil || bt == nil || !bt.Active {
				httpErr(w, http.StatusNotFound, "booking type not found")
				return
			}
			writeToolOut(w, map[string]any{"booking_type": toPublicBookingType(bt)}, nil)
			return
		}
		if r.Method != http.MethodGet {
			httpErr(w, http.StatusMethodNotAllowed, "GET required")
			return
		}
		a.writePublicPage(w, pid, slug)
		return
	}
	switch parts[1] {
	case "slots":
		if r.Method != http.MethodGet {
			httpErr(w, http.StatusMethodNotAllowed, "GET required")
			return
		}
		if !publicRateLimit.allow(clientAddress(r)+":slots", 60, time.Minute) {
			httpErr(w, http.StatusTooManyRequests, "too many slot requests")
			return
		}
		out, err := a.toolBookingsFindSlots(globalCtx, args)
		if err != nil {
			publicError(w, err)
			return
		}
		result := out.(map[string]any)
		bt := result["booking_type"].(*BookingType)
		writeToolOut(w, map[string]any{"slots": result["slots"], "booking_type": toPublicBookingType(bt)}, nil)
	case "book":
		if r.Method != http.MethodPost {
			httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		if !publicRateLimit.allow(clientAddress(r)+":book", 10, time.Hour) {
			httpErr(w, http.StatusTooManyRequests, "too many booking attempts")
			return
		}
		args["source"] = "public"
		out, err := a.toolBookingsCreate(globalCtx, args)
		if err != nil {
			publicError(w, err)
			return
		}
		result := out.(map[string]any)
		booking := result["booking"].(*Booking)
		writeToolOut(w, map[string]any{"booking": toPublicBooking(booking), "booking_type": toPublicBookingType(result["booking_type"].(*BookingType))}, nil)
	default:
		httpErr(w, http.StatusNotFound, "not found")
	}
}

func (a *App) handleTokenPage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("Referrer-Policy", "no-referrer")
	r.Body = http.MaxBytesReader(w, r.Body, 64*1024)
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/b/"), "/")
	if rest == "" {
		httpErr(w, http.StatusNotFound, "token required")
		return
	}
	parts := strings.Split(rest, "/")
	token := parts[0]
	if len(parts) > 1 {
		if parts[1] == "cancel" && r.Method == http.MethodPost {
			a.handleTokenCancel(w, r, token)
			return
		}
		if parts[1] == "reschedule" {
			a.handleTokenReschedule(w, r, token)
			return
		}
		httpErr(w, http.StatusNotFound, "not found")
		return
	}
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "GET required")
		return
	}
	a.writeManagePage(w, token)
}

func (a *App) handleTokenCancel(w http.ResponseWriter, r *http.Request, token string) {
	pid, id, err := lookupBookingByToken(globalCtx, token, "cancellation_token")
	if err != nil {
		httpErr(w, http.StatusNotFound, "booking not found")
		return
	}
	args := map[string]any{"_project_id": pid, "id": id}
	out, err := a.toolBookingsCancel(globalCtx, args)
	if err != nil {
		publicError(w, err)
		return
	}
	booking := out.(map[string]any)["booking"].(*Booking)
	if !wantsJSON(r) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(`<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Booking cancelled</title></head><body style="font-family:system-ui;margin:32px;max-width:680px"><h1>Booking cancelled</h1><p>Your appointment has been cancelled.</p></body></html>`))
		return
	}
	writeToolOut(w, map[string]any{"booking": toPublicBooking(booking)}, nil)
}

func (a *App) handleTokenReschedule(w http.ResponseWriter, r *http.Request, token string) {
	pid, id, err := lookupBookingByToken(globalCtx, token, "reschedule_token")
	if err != nil {
		httpErr(w, http.StatusNotFound, "booking not found")
		return
	}
	b, err := loadBookingByID(globalCtx, pid, id)
	if err != nil || b == nil {
		httpErr(w, http.StatusNotFound, "booking not found")
		return
	}
	bt, err := loadBookingTypeByID(globalCtx, pid, b.BookingTypeID)
	if err != nil || bt == nil {
		httpErr(w, http.StatusNotFound, "booking type not found")
		return
	}
	if r.Method == http.MethodGet {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(reschedulePageHTML(pid, token, bt)))
		return
	}
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "GET or POST required")
		return
	}
	if !publicRateLimit.allow(clientAddress(r)+":reschedule", 10, time.Hour) {
		httpErr(w, http.StatusTooManyRequests, "too many reschedule attempts")
		return
	}
	args, err := argsFromRequest(r)
	if err != nil {
		httpErr(w, http.StatusBadRequest, "invalid request")
		return
	}
	args["_project_id"] = pid
	args["id"] = id
	out, err := a.toolBookingsReschedule(globalCtx, args)
	if err != nil {
		publicError(w, err)
		return
	}
	booking := out.(map[string]any)["booking"].(*Booking)
	writeToolOut(w, map[string]any{"booking": toPublicBooking(booking)}, nil)
}

func (a *App) writePublicPage(w http.ResponseWriter, pid, slug string) {
	bt, err := loadBookingTypeBySlug(globalCtx, pid, slug)
	if err != nil || bt == nil || !bt.Active {
		httpErr(w, http.StatusNotFound, "booking type not found")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(publicPageHTML(pid, slug, bt)))
}

func (a *App) writeManagePage(w http.ResponseWriter, token string) {
	pid, id, err := lookupBookingByToken(globalCtx, token, "cancellation_token")
	if err != nil {
		httpErr(w, http.StatusNotFound, "booking not found")
		return
	}
	b, err := loadBookingByID(globalCtx, pid, id)
	if err != nil || b == nil {
		httpErr(w, http.StatusNotFound, "booking not found")
		return
	}
	bt, err := loadBookingTypeByID(globalCtx, pid, b.BookingTypeID)
	if err != nil || bt == nil {
		httpErr(w, http.StatusNotFound, "booking type not found")
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(managePageHTML(b, bt)))
}

func publicPageHTML(pid, slug string, bt *BookingType) string {
	title := html.EscapeString(bt.Title)
	desc := html.EscapeString(bt.Description)
	location := html.EscapeString(publicLocationLabel(bt))
	return `<!doctype html>
<html lang="en"><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1">
<title>` + title + `</title>
<style>
body{font-family:Inter,ui-sans-serif,system-ui;margin:0;background:#f8fafc;color:#111827}.wrap{max-width:960px;margin:0 auto;padding:32px 18px}.grid{display:grid;grid-template-columns:minmax(0,.8fr) minmax(0,1.2fr);gap:24px}@media(max-width:760px){.grid{grid-template-columns:1fr}.wrap{padding-top:18px}}.panel{background:white;border:1px solid #e5e7eb;border-radius:10px;padding:20px}.muted{color:#6b7280}.meta{display:grid;gap:5px;margin:18px 0}.day{margin-top:18px}.day h3{font-size:14px;margin:0 0 8px}.slots{display:grid;grid-template-columns:repeat(auto-fill,minmax(170px,1fr));gap:8px}.slot{border:1px solid #d1d5db;background:#fff;border-radius:6px;padding:10px;text-align:left;cursor:pointer}.slot:hover,.slot:focus-visible{border-color:#2563eb}.slot.sel{border-color:#2563eb;background:#eff6ff}input,textarea{width:100%;box-sizing:border-box;border:1px solid #d1d5db;border-radius:6px;padding:9px;margin-top:5px}label{display:block;font-size:13px;margin:10px 0}button.primary{background:#111827;color:white;border:0;border-radius:6px;padding:10px 14px;cursor:pointer}button.primary:disabled{opacity:.45}.err{color:#b91c1c}.ok{color:#047857;word-break:break-word}
</style></head><body><main class="wrap"><div class="grid"><section><h1>` + title + `</h1><p class="muted">` + desc + `</p><div class="meta muted"><span>` + strconv.Itoa(bt.DurationMinutes) + ` minutes</span><span>` + location + `</span><span id="timezone"></span></div></section><section class="panel"><h2>Select a time</h2><div id="status" class="muted" aria-live="polite">Loading available times…</div><div id="slots"></div><form id="form" hidden><h2>Your details</h2><label>Name<input name="invitee_name" maxlength="200" autocomplete="name" required></label><label>Email<input name="invitee_email" type="email" maxlength="320" autocomplete="email" required></label><label>Phone<input name="invitee_phone" maxlength="80" autocomplete="tel"></label><label>Notes<textarea name="notes" maxlength="4000" rows="3"></textarea></label><button class="primary" id="book" type="submit">Book meeting</button></form><div id="done" aria-live="polite"></div></section></div></main>
<script>
const PID=` + jsString(pid) + `, SLUG=` + jsString(slug) + `;
let selected=null,idempotencyKey=null;
const statusEl=document.getElementById("status"), slotsEl=document.getElementById("slots"), form=document.getElementById("form"), done=document.getElementById("done");
const browserZone=Intl.DateTimeFormat().resolvedOptions().timeZone;document.getElementById("timezone").textContent="Times shown in "+browserZone;
const fmt=s=>new Intl.DateTimeFormat(undefined,{hour:"numeric",minute:"2-digit"}).format(new Date(s));
const dayFmt=s=>new Intl.DateTimeFormat(undefined,{weekday:"long",month:"long",day:"numeric"}).format(new Date(s));
function showError(target,message){target.replaceChildren();const p=document.createElement("p");p.className="err";p.textContent=message;target.appendChild(p)}
async function load(){try{const res=await fetch("/api/apps/bookings/public/"+encodeURIComponent(SLUG)+"/slots?project_id="+encodeURIComponent(PID)+"&limit=40");if(!res.ok)throw new Error("Could not load available times.");const data=await res.json(),slots=data.slots||[];statusEl.textContent=slots.length?"Pick one available slot.":"No slots are currently available.";slotsEl.replaceChildren();const groups=new Map();for(const slot of slots){const key=dayFmt(slot.start);if(!groups.has(key))groups.set(key,[]);groups.get(key).push(slot)}for(const [day,items] of groups){const section=document.createElement("section");section.className="day";const heading=document.createElement("h3");heading.textContent=day;section.appendChild(heading);const grid=document.createElement("div");grid.className="slots";for(const slot of items){const b=document.createElement("button");b.type="button";b.className="slot";b.textContent=fmt(slot.start);b.onclick=()=>{selected=slot;idempotencyKey=crypto.randomUUID?crypto.randomUUID():String(Date.now())+Math.random();document.querySelectorAll(".slot").forEach(x=>x.classList.remove("sel"));b.classList.add("sel");form.hidden=false};grid.appendChild(b)}section.appendChild(grid);slotsEl.appendChild(section)}}catch(e){showError(statusEl,e.message)}}
form.onsubmit=async e=>{e.preventDefault();if(!selected)return;const fd=new FormData(form),body={start_at:selected.start,invitee_name:fd.get("invitee_name"),invitee_email:fd.get("invitee_email"),invitee_phone:fd.get("invitee_phone"),intake_answers:{notes:fd.get("notes")},idempotency_key:idempotencyKey};const btn=document.getElementById("book");btn.disabled=true;done.replaceChildren();try{const res=await fetch("/api/apps/bookings/public/"+encodeURIComponent(SLUG)+"/book?project_id="+encodeURIComponent(PID),{method:"POST",headers:{"Content-Type":"application/json","Accept":"application/json"},body:JSON.stringify(body)});if(!res.ok)throw new Error((await res.text()).trim()||"Booking failed.");const data=await res.json();form.hidden=true;slotsEl.hidden=true;statusEl.textContent="";const confirmation=document.createElement("p");confirmation.className="ok";confirmation.textContent="Booked for "+dayFmt(data.booking.start_at)+" at "+fmt(data.booking.start_at)+".";done.appendChild(confirmation);if(data.booking.calls_guest_join_url){appendLink(done,data.booking.calls_guest_join_url,"Join call")}else if(data.booking_type.location_kind==="external_url"&&data.booking_type.location_value){appendLink(done,data.booking_type.location_value,"Open meeting link")}if(data.booking.public_manage_url){appendLink(done,data.booking.public_manage_url,"Manage or reschedule this booking")}}catch(e){showError(done,e.message);btn.disabled=false;}};
function appendLink(target,href,label){const p=document.createElement("p"),a=document.createElement("a");a.href=href;a.rel="noreferrer";a.textContent=label;p.appendChild(a);target.appendChild(p)}
load();
</script></body></html>`
}

func publicLocationLabel(bt *BookingType) string {
	switch bt.LocationKind {
	case "calls":
		return "Online call"
	case "phone":
		return "Phone call"
	case "in_person":
		if bt.LocationValue != "" {
			return bt.LocationValue
		}
		return "In person"
	case "external_url":
		return "Online meeting"
	default:
		return "Meeting"
	}
}

func managePageHTML(b *Booking, bt *BookingType) string {
	reschedule := ""
	cancel := ""
	if b.Status == "confirmed" || b.Status == "rescheduled" {
		projectQuery := "?project_id=" + url.QueryEscape(b.ProjectID)
		reschedule = `<p><a href="/api/apps/bookings/b/` + url.PathEscape(b.RescheduleToken) + `/reschedule` + projectQuery + `">Choose a new time</a></p>`
		cancel = `<form method="post" action="/api/apps/bookings/b/` + url.PathEscape(b.CancellationToken) + `/cancel` + projectQuery + `" onsubmit="return confirm('Cancel this booking?')"><button type="submit">Cancel booking</button></form>`
	}
	return `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Booking</title><style>body{font-family:system-ui;margin:32px;max-width:680px}.muted{color:#6b7280}button{background:#991b1b;color:white;border:0;border-radius:6px;padding:10px 14px}</style></head><body><h1>` + html.EscapeString(bt.Title) + `</h1><p id="time" data-time="` + html.EscapeString(b.StartAt) + `"></p><p class="muted">Status: ` + html.EscapeString(b.Status) + `</p>` + bookingLocationHTML(b, bt) + reschedule + cancel + `<script>const el=document.getElementById('time');el.textContent=new Intl.DateTimeFormat(undefined,{dateStyle:'full',timeStyle:'short'}).format(new Date(el.dataset.time));</script></body></html>`
}

func reschedulePageHTML(pid, token string, bt *BookingType) string {
	return `<!doctype html><html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>Reschedule</title><style>body{font-family:system-ui;margin:32px;max-width:760px}.slots{display:grid;grid-template-columns:repeat(auto-fill,minmax(190px,1fr));gap:8px}.slot{padding:10px;border:1px solid #d1d5db;background:white;border-radius:6px;cursor:pointer}.error{color:#b91c1c}</style></head><body><h1>Choose a new time</h1><p id="zone"></p><div id="status">Loading available times…</div><div id="slots" class="slots"></div><script>const PID=` + jsString(pid) + `,SLUG=` + jsString(bt.Slug) + `,TOKEN=` + jsString(token) + `;const statusEl=document.getElementById('status'),slotsEl=document.getElementById('slots');document.getElementById('zone').textContent='Times shown in '+Intl.DateTimeFormat().resolvedOptions().timeZone;const fmt=s=>new Intl.DateTimeFormat(undefined,{dateStyle:'medium',timeStyle:'short'}).format(new Date(s));async function load(){try{const res=await fetch('/api/apps/bookings/public/'+encodeURIComponent(SLUG)+'/slots?project_id='+encodeURIComponent(PID)+'&limit=40');if(!res.ok)throw new Error('Could not load available times');const data=await res.json();statusEl.textContent=data.slots.length?'Select a time:':'No times are currently available.';for(const slot of data.slots){const button=document.createElement('button');button.className='slot';button.textContent=fmt(slot.start);button.onclick=()=>choose(slot.start);slotsEl.appendChild(button)}}catch(error){statusEl.className='error';statusEl.textContent=error.message}}async function choose(start){if(!confirm('Move your appointment to '+fmt(start)+'?'))return;try{const res=await fetch('/api/apps/bookings/b/'+encodeURIComponent(TOKEN)+'/reschedule?project_id='+encodeURIComponent(PID),{method:'POST',headers:{'Content-Type':'application/json','Accept':'application/json'},body:JSON.stringify({start_at:start})});if(!res.ok)throw new Error(await res.text());slotsEl.replaceChildren();statusEl.textContent='Your appointment was rescheduled to '+fmt(start)+'.'}catch(error){statusEl.className='error';statusEl.textContent=error.message}}load();</script></body></html>`
}

func bookingLocationHTML(b *Booking, bt *BookingType) string {
	if b.CallsGuestJoinURL == "" {
		switch bt.LocationKind {
		case "external_url":
			return `<p><a rel="noreferrer" href="` + html.EscapeString(bt.LocationValue) + `">Open meeting link</a></p>`
		case "phone", "in_person":
			if bt.LocationValue != "" {
				return `<p>` + html.EscapeString(bt.LocationValue) + `</p>`
			}
		}
		return ""
	}
	return `<p><a rel="noreferrer" href="` + html.EscapeString(b.CallsGuestJoinURL) + `">Join call</a></p>`
}

// ─── Storage helpers ──────────────────────────────────────────────

func loadBookingTypeByID(ctx *sdk.AppCtx, pid string, id int64) (*BookingType, error) {
	return scanBookingType(ctx.AppDB().QueryRow(
		`SELECT id, project_id, slug, title, description, duration_minutes, timezone, location_kind, location_value,
		        target_kind, calendar_ids, destination_calendar_id, agent_instance_id, calls_enabled, crm_enabled, active,
		        availability_rules, intake_schema, confirmation_policy, created_at, updated_at
		   FROM booking_types WHERE project_id=? AND id=?`, pid, id))
}

func loadBookingTypeBySlug(ctx *sdk.AppCtx, pid, slug string) (*BookingType, error) {
	return scanBookingType(ctx.AppDB().QueryRow(
		`SELECT id, project_id, slug, title, description, duration_minutes, timezone, location_kind, location_value,
		        target_kind, calendar_ids, destination_calendar_id, agent_instance_id, calls_enabled, crm_enabled, active,
		        availability_rules, intake_schema, confirmation_policy, created_at, updated_at
		   FROM booking_types WHERE project_id=? AND slug=?`, pid, slug))
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanBookingType(row rowScanner) (*BookingType, error) {
	var bt BookingType
	var calendarIDs, availability, intake, policy string
	var calls, crm, active int
	err := row.Scan(&bt.ID, &bt.ProjectID, &bt.Slug, &bt.Title, &bt.Description, &bt.DurationMinutes, &bt.Timezone, &bt.LocationKind, &bt.LocationValue,
		&bt.TargetKind, &calendarIDs, &bt.DestinationCalendarID, &bt.AgentInstanceID, &calls, &crm, &active, &availability, &intake, &policy, &bt.CreatedAt, &bt.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(calendarIDs), &bt.CalendarIDs)
	bt.CallsEnabled = calls == 1
	bt.CRMEnabled = crm == 1
	bt.Active = active == 1
	bt.AvailabilityRules = json.RawMessage(availability)
	bt.IntakeSchema = json.RawMessage(intake)
	bt.ConfirmationPolicy = json.RawMessage(policy)
	return &bt, nil
}

func loadBookingByID(ctx *sdk.AppCtx, pid string, id int64) (*Booking, error) {
	return scanBooking(ctx, ctx.AppDB().QueryRow(
		`SELECT id, project_id, booking_type_id, status, start_at, end_at, timezone,
		        invitee_name, invitee_email, invitee_phone, intake_answers,
		        calendar_event_id, calls_room_id, calls_guest_join_url, calls_host_join_url, crm_contact_id,
		        assigned_target_kind, assigned_agent_instance_id, cancellation_token, reschedule_token,
		        source, idempotency_key, created_at, updated_at
		   FROM bookings WHERE project_id=? AND id=?`, pid, id,
	))
}

func loadBookingByIdempotencyKey(ctx *sdk.AppCtx, pid, key string) (*Booking, error) {
	return scanBooking(ctx, ctx.AppDB().QueryRow(
		`SELECT id, project_id, booking_type_id, status, start_at, end_at, timezone,
		        invitee_name, invitee_email, invitee_phone, intake_answers,
		        calendar_event_id, calls_room_id, calls_guest_join_url, calls_host_join_url, crm_contact_id,
		        assigned_target_kind, assigned_agent_instance_id, cancellation_token, reschedule_token,
		        source, idempotency_key, created_at, updated_at
		   FROM bookings WHERE project_id=? AND idempotency_key=?`, pid, key,
	))
}

func scanBooking(ctx *sdk.AppCtx, row rowScanner) (*Booking, error) {
	var b Booking
	var answers string
	var calID, roomID, crmID sql.NullInt64
	err := row.Scan(&b.ID, &b.ProjectID, &b.BookingTypeID, &b.Status, &b.StartAt, &b.EndAt, &b.Timezone,
		&b.InviteeName, &b.InviteeEmail, &b.InviteePhone, &answers,
		&calID, &roomID, &b.CallsGuestJoinURL, &b.CallsHostJoinURL, &crmID,
		&b.AssignedTargetKind, &b.AssignedAgentInstanceID, &b.CancellationToken, &b.RescheduleToken,
		&b.Source, &b.IdempotencyKey, &b.CreatedAt, &b.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	b.IntakeAnswers = json.RawMessage(answers)
	if calID.Valid {
		b.CalendarEventID = calID.Int64
	}
	if roomID.Valid {
		b.CallsRoomID = roomID.Int64
	}
	if crmID.Valid {
		b.CRMContactID = crmID.Int64
	}
	b.PublicManageURL = publicManageURL(ctx, b.ProjectID, b.CancellationToken)
	return &b, nil
}

func resolveBookingType(ctx *sdk.AppCtx, pid string, args map[string]any) (*BookingType, error) {
	if id := int64Arg(args, "booking_type_id"); id > 0 {
		return loadBookingTypeByID(ctx, pid, id)
	}
	if id := int64Arg(args, "id"); id > 0 {
		return loadBookingTypeByID(ctx, pid, id)
	}
	if slug := strArg(args, "slug"); slug != "" {
		return loadBookingTypeBySlug(ctx, pid, slug)
	}
	return nil, errors.New("booking_type_id, id, or slug required")
}

func lookupBookingByToken(ctx *sdk.AppCtx, token, column string) (string, int64, error) {
	if column != "cancellation_token" && column != "reschedule_token" {
		return "", 0, errors.New("invalid token column")
	}
	var pid string
	var id int64
	err := ctx.AppDB().QueryRow(`SELECT project_id, id FROM bookings WHERE `+column+`=?`, token).Scan(&pid, &id)
	return pid, id, err
}

func uniqueBookingTypeSlug(ctx *sdk.AppCtx, pid, base string, excludingID int64) (string, error) {
	if base == "" {
		base = "booking"
	}
	for i := 0; i < 100; i++ {
		slug := base
		if i > 0 {
			slug = fmt.Sprintf("%s-%d", base, i+1)
		}
		var id int64
		err := ctx.AppDB().QueryRow(`SELECT id FROM booking_types WHERE project_id=? AND slug=?`, pid, slug).Scan(&id)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && id == excludingID) {
			return slug, nil
		}
		if err != nil {
			return "", err
		}
	}
	return "", errors.New("could not allocate unique slug")
}

func (a *App) recordEvent(ctx *sdk.AppCtx, pid string, bookingID int64, kind string, payload map[string]any) {
	if payload == nil {
		payload = map[string]any{}
	}
	b, _ := json.Marshal(payload)
	_, _ = ctx.AppDB().Exec(`INSERT INTO booking_events (project_id, booking_id, kind, payload) VALUES (?, ?, ?, ?)`, pid, bookingID, kind, string(b))
}

func (a *App) emitBooking(ctx *sdk.AppCtx, pid, topic string, b *Booking) {
	if b == nil {
		return
	}
	ctx.Emit(topic, map[string]any{
		"project_id":                 pid,
		"booking_id":                 b.ID,
		"booking_type_id":            b.BookingTypeID,
		"status":                     b.Status,
		"start_at":                   b.StartAt,
		"end_at":                     b.EndAt,
		"calendar_event_id":          b.CalendarEventID,
		"calls_room_id":              b.CallsRoomID,
		"crm_contact_id":             b.CRMContactID,
		"assigned_target_kind":       b.AssignedTargetKind,
		"assigned_agent_instance_id": b.AssignedAgentInstanceID,
	})
}

// ─── Request / utility helpers ────────────────────────────────────

func resolveProjectFromArgs(args map[string]any) (string, error) {
	if env := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); env != "" {
		return env, nil
	}
	if v, ok := args["_project_id"].(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v), nil
	}
	if v, ok := args["project_id"].(string); ok && strings.TrimSpace(v) != "" {
		return strings.TrimSpace(v), nil
	}
	return "", errors.New("project_id missing - pass _project_id when scope=global")
}

func resolveProjectFromRequest(r *http.Request) (string, error) {
	if env := strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")); env != "" {
		return env, nil
	}
	if v := strings.TrimSpace(r.URL.Query().Get("project_id")); v != "" {
		return v, nil
	}
	return "", errors.New("project_id required in query string when install scope=global")
}

func argsFromRequest(r *http.Request) (map[string]any, error) {
	args := map[string]any{}
	if pid := strings.TrimSpace(r.URL.Query().Get("project_id")); pid != "" {
		args["_project_id"] = pid
	}
	for k, v := range r.URL.Query() {
		if k == "project_id" || len(v) == 0 {
			continue
		}
		args[k] = v[0]
	}
	if r.Body != nil && (r.Method == http.MethodPost || r.Method == http.MethodPatch || r.Method == http.MethodPut) {
		defer r.Body.Close()
		var body map[string]any
		decoder := json.NewDecoder(r.Body)
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(&body); err != nil && !errors.Is(err, io.EOF) {
			return nil, err
		}
		for k, v := range body {
			args[k] = v
		}
	}
	return args, nil
}

func writeToolOut(w http.ResponseWriter, out any, err error) {
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	http.Error(w, msg, code)
}

func wantsJSON(r *http.Request) bool {
	return strings.Contains(r.Header.Get("Accept"), "application/json") || r.URL.Query().Get("format") == "json"
}

func idFromPath(path, prefix string) int64 {
	rest := strings.Trim(strings.TrimPrefix(path, prefix), "/")
	id, _ := strconv.ParseInt(strings.Split(rest, "/")[0], 10, 64)
	return id
}

func schemaObject(props map[string]any, required []string) map[string]any {
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func parseTimeArg(args map[string]any, key string) (time.Time, error) {
	s := strArg(args, key)
	if s == "" {
		return time.Time{}, fmt.Errorf("%s required", key)
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("%s: %w", key, err)
	}
	return t, nil
}

func parseRules(raw json.RawMessage) availabilityRules {
	r := availabilityRules{
		MinimumNoticeMins:  120,
		BookingHorizonDays: 30,
		WorkingHours: map[string]map[string]string{
			"mon": {"start": "09:00", "end": "17:00"},
			"tue": {"start": "09:00", "end": "17:00"},
			"wed": {"start": "09:00", "end": "17:00"},
			"thu": {"start": "09:00", "end": "17:00"},
			"fri": {"start": "09:00", "end": "17:00"},
		},
	}
	var decoded struct {
		WorkingHours        map[string]map[string]string `json:"working_hours"`
		BufferBeforeMinutes *int                         `json:"buffer_before_minutes"`
		BufferAfterMinutes  *int                         `json:"buffer_after_minutes"`
		MinimumNoticeMins   *int                         `json:"minimum_notice_minutes"`
		BookingHorizonDays  *int                         `json:"booking_horizon_days"`
	}
	if json.Unmarshal(raw, &decoded) != nil {
		return r
	}
	if decoded.WorkingHours != nil {
		r.WorkingHours = decoded.WorkingHours
	}
	if decoded.BufferBeforeMinutes != nil {
		r.BufferBeforeMinutes = *decoded.BufferBeforeMinutes
	}
	if decoded.BufferAfterMinutes != nil {
		r.BufferAfterMinutes = *decoded.BufferAfterMinutes
	}
	if decoded.MinimumNoticeMins != nil {
		r.MinimumNoticeMins = *decoded.MinimumNoticeMins
	}
	if decoded.BookingHorizonDays != nil {
		r.BookingHorizonDays = *decoded.BookingHorizonDays
	}
	return r
}

func validateAvailabilityJSON(raw string) error {
	if !json.Valid([]byte(raw)) {
		return errors.New("availability_rules must be valid JSON")
	}
	rules := parseRules(json.RawMessage(raw))
	if rules.MinimumNoticeMins < 0 || rules.MinimumNoticeMins > 365*24*60 {
		return errors.New("minimum_notice_minutes must be between 0 and 525600")
	}
	if rules.BookingHorizonDays < 1 || rules.BookingHorizonDays > 365 {
		return errors.New("booking_horizon_days must be between 1 and 365")
	}
	if rules.BufferBeforeMinutes < 0 || rules.BufferBeforeMinutes > 24*60 || rules.BufferAfterMinutes < 0 || rules.BufferAfterMinutes > 24*60 {
		return errors.New("booking buffers must be between 0 and 1440 minutes")
	}
	for day, hours := range rules.WorkingHours {
		if !containsString([]string{"mon", "tue", "wed", "thu", "fri", "sat", "sun"}, strings.ToLower(day)) {
			return fmt.Errorf("invalid working-hours day %q", day)
		}
		start, okStart := hhmmMinutes(hours["start"])
		end, okEnd := hhmmMinutes(hours["end"])
		if !okStart || !okEnd || start >= end {
			return fmt.Errorf("working hours for %s must contain valid start/end times", day)
		}
	}
	return nil
}

func hhmmMinutes(value string) (int, bool) {
	parts := strings.Split(value, ":")
	if len(parts) != 2 {
		return 0, false
	}
	hour, errHour := strconv.Atoi(parts[0])
	minute, errMinute := strconv.Atoi(parts[1])
	if errHour != nil || errMinute != nil || hour < 0 || hour > 23 || minute < 0 || minute > 59 {
		return 0, false
	}
	return hour*60 + minute, true
}

func defaultAvailabilityJSON() string {
	return `{"minimum_notice_minutes":120,"booking_horizon_days":30,"working_hours":{"mon":{"start":"09:00","end":"17:00"},"tue":{"start":"09:00","end":"17:00"},"wed":{"start":"09:00","end":"17:00"},"thu":{"start":"09:00","end":"17:00"},"fri":{"start":"09:00","end":"17:00"}}}`
}

func publicBase(ctx *sdk.AppCtx) string {
	if ctx != nil {
		if info, err := ctx.PlatformInfo(); err == nil && info != nil && strings.TrimSpace(info.PublicURL) != "" {
			return strings.TrimRight(info.PublicURL, "/") + "/api/apps/bookings"
		}
	}
	if base := strings.TrimRight(os.Getenv("APTEVA_PUBLIC_URL"), "/"); base != "" {
		return base + "/api/apps/bookings"
	}
	return "/api/apps/bookings"
}

func publicBookingURL(ctx *sdk.AppCtx, pid, slug string) string {
	u := publicBase(ctx) + "/public/" + url.PathEscape(slug)
	if pid != "" {
		u += "?project_id=" + url.QueryEscape(pid)
	}
	return u
}

func publicManageURL(ctx *sdk.AppCtx, pid, token string) string {
	u := publicBase(ctx) + "/b/" + url.PathEscape(token)
	if pid != "" {
		u += "?project_id=" + url.QueryEscape(pid)
	}
	return u
}

func randomToken() string {
	var b [24]byte
	if _, err := rand.Read(b[:]); err != nil {
		panic("rand.Read: " + err.Error())
	}
	return base64.RawURLEncoding.EncodeToString(b[:])
}

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if len(s) > 120 {
		s = strings.TrimRight(s[:120], "-")
	}
	if s == "" {
		return "booking"
	}
	return s
}

func strArg(args map[string]any, key string) string {
	if v, ok := args[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func strArgDefault(args map[string]any, key, def string) string {
	if s := strArg(args, key); s != "" {
		return s
	}
	return def
}

func intArg(args map[string]any, key string, def int) int {
	switch v := args[key].(type) {
	case int:
		return v
	case int64:
		return int(v)
	case float64:
		return int(v)
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return def
}

func int64Arg(args map[string]any, key string) int64 {
	switch v := args[key].(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return n
	}
	return 0
}

func int64SliceArg(args map[string]any, key string) []int64 {
	raw, ok := args[key]
	if !ok || raw == nil {
		return nil
	}
	out := []int64{}
	switch v := raw.(type) {
	case []int64:
		return v
	case []int:
		for _, n := range v {
			out = append(out, int64(n))
		}
	case []any:
		for _, x := range v {
			switch n := x.(type) {
			case float64:
				out = append(out, int64(n))
			case int:
				out = append(out, int64(n))
			case string:
				if parsed, err := strconv.ParseInt(strings.TrimSpace(n), 10, 64); err == nil {
					out = append(out, parsed)
				}
			}
		}
	case string:
		for _, part := range strings.Split(v, ",") {
			if n, err := strconv.ParseInt(strings.TrimSpace(part), 10, 64); err == nil {
				out = append(out, n)
			}
		}
	}
	return out
}

func boolArg(args map[string]any, key string, def bool) bool {
	switch v := args[key].(type) {
	case bool:
		return v
	case string:
		if v == "" {
			return def
		}
		return v == "1" || strings.EqualFold(v, "true") || strings.EqualFold(v, "yes")
	}
	return def
}

func boolToInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func enumArg(args map[string]any, key, def string, allowed []string) string {
	v := strArg(args, key)
	if v == "" {
		return def
	}
	for _, a := range allowed {
		if v == a {
			return v
		}
	}
	return def
}

func jsonStringArg(args map[string]any, key, def string) (string, error) {
	v, ok := args[key]
	if !ok || v == nil {
		return def, nil
	}
	if s, ok := v.(string); ok {
		if strings.TrimSpace(s) == "" {
			return def, nil
		}
		if !json.Valid([]byte(s)) {
			return "", fmt.Errorf("%s must be valid JSON", key)
		}
		return s, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	return string(b), nil
}

func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func newRequestLimiter() *requestLimiter {
	return &requestLimiter{buckets: map[string][]time.Time{}}
}

func (l *requestLimiter) allow(key string, max int, window time.Duration) bool {
	now := time.Now()
	cutoff := now.Add(-window)
	l.mu.Lock()
	defer l.mu.Unlock()
	items := l.buckets[key]
	kept := items[:0]
	for _, item := range items {
		if item.After(cutoff) {
			kept = append(kept, item)
		}
	}
	if len(kept) >= max {
		l.buckets[key] = kept
		return false
	}
	l.buckets[key] = append(kept, now)
	return true
}

func clientAddress(r *http.Request) string {
	if forwarded := strings.TrimSpace(strings.Split(r.Header.Get("X-Forwarded-For"), ",")[0]); forwarded != "" {
		return forwarded
	}
	host, _, err := net.SplitHostPort(r.RemoteAddr)
	if err == nil && host != "" {
		return host
	}
	return r.RemoteAddr
}

func validateInvitee(args map[string]any) error {
	name := strArg(args, "invitee_name")
	email := strings.ToLower(strArg(args, "invitee_email"))
	phone := strArg(args, "invitee_phone")
	if strArg(args, "source") == "public" && name == "" {
		return errors.New("invitee_name required")
	}
	if email == "" {
		return errors.New("invitee_email required")
	}
	parsed, err := mail.ParseAddress(email)
	if err != nil || !strings.EqualFold(parsed.Address, email) {
		return errors.New("invitee_email must be a valid email address")
	}
	if len(name) > 200 || len(email) > 320 || len(phone) > 80 {
		return errors.New("invitee fields exceed their maximum length")
	}
	args["invitee_email"] = email
	return nil
}

func validateLocation(kind, value string, callsEnabled bool) error {
	if len(value) > 2048 {
		return errors.New("location_value must be at most 2048 characters")
	}
	if kind == "calls" && !callsEnabled {
		return errors.New("Calls locations require calls_enabled=true")
	}
	if kind != "calls" && callsEnabled {
		return errors.New("calls_enabled can only be used with a Calls location")
	}
	if kind == "external_url" {
		parsed, err := url.ParseRequestURI(value)
		if err != nil || parsed.Host == "" || (parsed.Scheme != "https" && parsed.Scheme != "http") {
			return errors.New("external_url locations require a valid http or https location_value")
		}
	}
	return nil
}

func validateCalendarIDs(calendarIDs []int64, destinationID int64) error {
	if destinationID < 0 {
		return errors.New("destination_calendar_id cannot be negative")
	}
	for _, id := range calendarIDs {
		if id <= 0 {
			return errors.New("calendar_ids must contain only positive ids")
		}
	}
	return nil
}

func toPublicBookingType(bt *BookingType) publicBookingType {
	return publicBookingType{
		Slug: bt.Slug, Title: bt.Title, Description: bt.Description,
		DurationMinutes: bt.DurationMinutes, Timezone: bt.Timezone,
		LocationKind: bt.LocationKind, LocationValue: bt.LocationValue,
	}
}

func toPublicBooking(b *Booking) publicBooking {
	return publicBooking{
		ID: b.ID, Status: b.Status, StartAt: b.StartAt, EndAt: b.EndAt,
		Timezone: b.Timezone, InviteeName: b.InviteeName, InviteeEmail: b.InviteeEmail,
		IntakeAnswers: b.IntakeAnswers, GuestJoinURL: b.CallsGuestJoinURL,
		PublicManageURL: b.PublicManageURL,
	}
}

func publicError(w http.ResponseWriter, err error) {
	message := err.Error()
	code := http.StatusBadRequest
	if strings.Contains(message, "no longer available") {
		code = http.StatusConflict
	}
	allowed := []string{
		"required", "valid email", "minimum notice", "booking horizon", "outside working hours",
		"15-minute boundary", "no longer available", "unsupported target_kind", "active booking type not found",
		"cannot cancel", "cannot reschedule", "maximum length", "must be at most",
	}
	for _, fragment := range allowed {
		if strings.Contains(message, fragment) {
			httpErr(w, code, message)
			return
		}
	}
	httpErr(w, http.StatusServiceUnavailable, "booking service is temporarily unavailable")
}

func crossAppNotFound(err error) bool {
	if err == nil {
		return false
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "not found") || strings.Contains(message, "no rows")
}

func containsString(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
