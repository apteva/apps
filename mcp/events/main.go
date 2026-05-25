// Apteva Events app — simple event, ticket, performer application, and
// schedule management for small shows and festivals.
package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"regexp"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: events
display_name: Events
version: 0.1.0
description: Create shows, collect performer applications, curate lineups, issue simple tickets, and run check-in.
author: Apteva
scopes: [project]
requires:
  permissions: [db.write.app]
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: events_create, description: "Create a show/event." }
    - { name: events_list, description: "List events." }
    - { name: applications_submit, description: "Submit a performer application for an event." }
    - { name: applications_list, description: "List performer applications." }
    - { name: applications_review, description: "Shortlist, accept, reject, or annotate a performer application." }
    - { name: schedule_application, description: "Accept an application if needed and place it into a performance slot." }
    - { name: tickets_issue, description: "Issue a simple ticket." }
    - { name: tickets_check_in, description: "Check in a ticket by id or code." }
  ui_panels:
    - slot: project.page
      label: Events
      icon: calendar-days
      entry: /ui/EventsPanel.mjs
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/events
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/events.db
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
		return errors.New("events requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("events mounted")
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/events", Handler: a.handleEvents},
		{Pattern: "/events/", Handler: a.handleEventsItem},
		{Pattern: "/venues", Handler: a.handleVenues},
		{Pattern: "/ticket_types", Handler: a.handleTicketTypes},
		{Pattern: "/tickets", Handler: a.handleTickets},
		{Pattern: "/tickets/", Handler: a.handleTicketsItem},
		{Pattern: "/applications", Handler: a.handleApplications},
		{Pattern: "/applications/", Handler: a.handleApplicationsItem},
		{Pattern: "/slots", Handler: a.handleSlots},
		{Pattern: "/public/", Handler: a.handlePublic, NoAuth: true},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{Name: "events_create", Description: "Create an event/show. Args: title required; slug, description, status, visibility, timezone, starts_at, ends_at, venue_id, capacity, external_checkout_url optional.",
			InputSchema: schemaObject(map[string]any{
				"title":                 map[string]any{"type": "string"},
				"slug":                  map[string]any{"type": "string"},
				"description":           map[string]any{"type": "string"},
				"status":                map[string]any{"type": "string"},
				"visibility":            map[string]any{"type": "string"},
				"timezone":              map[string]any{"type": "string"},
				"starts_at":             map[string]any{"type": "string"},
				"ends_at":               map[string]any{"type": "string"},
				"venue_id":              map[string]any{"type": "integer"},
				"capacity":              map[string]any{"type": "integer"},
				"external_checkout_url": map[string]any{"type": "string"},
			}, []string{"title"}), Handler: a.toolEventsCreate},
		{Name: "events_list", Description: "List events. Args: status? (draft|published|closed|archived), limit?.",
			InputSchema: schemaObject(map[string]any{"status": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer"}}, nil), Handler: a.toolEventsList},
		{Name: "applications_submit", Description: "Submit a performer application. Args: event_id, applicant_name, email required; stage_name, phone, bio, set_length_minutes, video_url, tech_needs, notes optional.",
			InputSchema: schemaObject(map[string]any{
				"event_id":           map[string]any{"type": "integer"},
				"applicant_name":     map[string]any{"type": "string"},
				"stage_name":         map[string]any{"type": "string"},
				"email":              map[string]any{"type": "string"},
				"phone":              map[string]any{"type": "string"},
				"bio":                map[string]any{"type": "string"},
				"set_length_minutes": map[string]any{"type": "integer"},
				"video_url":          map[string]any{"type": "string"},
				"tech_needs":         map[string]any{"type": "string"},
				"notes":              map[string]any{"type": "string"},
			}, []string{"event_id", "applicant_name", "email"}), Handler: a.toolApplicationsSubmit},
		{Name: "applications_list", Description: "List performer applications. Args: event_id required; status? optional.",
			InputSchema: schemaObject(map[string]any{"event_id": map[string]any{"type": "integer"}, "status": map[string]any{"type": "string"}}, []string{"event_id"}), Handler: a.toolApplicationsList},
		{Name: "applications_review", Description: "Review an application. Args: id required; status?, score?, reviewer_notes?.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}, "status": map[string]any{"type": "string"}, "score": map[string]any{"type": "integer"}, "reviewer_notes": map[string]any{"type": "string"}}, []string{"id"}), Handler: a.toolApplicationsReview},
		{Name: "schedule_application", Description: "Accept an application and create a performance slot. Args: application_id, starts_at, ends_at optional venue_id, title, notes.",
			InputSchema: schemaObject(map[string]any{"application_id": map[string]any{"type": "integer"}, "venue_id": map[string]any{"type": "integer"}, "starts_at": map[string]any{"type": "string"}, "ends_at": map[string]any{"type": "string"}, "title": map[string]any{"type": "string"}, "notes": map[string]any{"type": "string"}}, []string{"application_id"}), Handler: a.toolScheduleApplication},
		{Name: "tickets_issue", Description: "Issue one or more simple tickets. Args: event_id, buyer_name, buyer_email, attendee_name optional, attendee_email optional, ticket_type_id optional, quantity optional.",
			InputSchema: schemaObject(map[string]any{"event_id": map[string]any{"type": "integer"}, "ticket_type_id": map[string]any{"type": "integer"}, "buyer_name": map[string]any{"type": "string"}, "buyer_email": map[string]any{"type": "string"}, "attendee_name": map[string]any{"type": "string"}, "attendee_email": map[string]any{"type": "string"}, "quantity": map[string]any{"type": "integer"}}, []string{"event_id", "buyer_name", "buyer_email"}), Handler: a.toolTicketsIssue},
		{Name: "tickets_check_in", Description: "Check in a ticket by id or code. Args: id? or code?.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}, "code": map[string]any{"type": "string"}}, nil), Handler: a.toolTicketsCheckIn},
	}
}

type Event struct {
	ID                  int64  `json:"id"`
	ProjectID           string `json:"project_id,omitempty"`
	Title               string `json:"title"`
	Slug                string `json:"slug"`
	Description         string `json:"description"`
	Status              string `json:"status"`
	Visibility          string `json:"visibility"`
	Timezone            string `json:"timezone"`
	StartsAt            string `json:"starts_at"`
	EndsAt              string `json:"ends_at"`
	VenueID             *int64 `json:"venue_id,omitempty"`
	Capacity            int64  `json:"capacity"`
	ExternalCheckoutURL string `json:"external_checkout_url"`
	TicketCount         int64  `json:"ticket_count"`
	ApplicationCount    int64  `json:"application_count"`
	SlotCount           int64  `json:"slot_count"`
	CreatedAt           string `json:"created_at"`
	UpdatedAt           string `json:"updated_at"`
}

type Venue struct {
	ID        int64  `json:"id"`
	Name      string `json:"name"`
	Address   string `json:"address"`
	City      string `json:"city"`
	Country   string `json:"country"`
	Capacity  int64  `json:"capacity"`
	Notes     string `json:"notes"`
	CreatedAt string `json:"created_at"`
	UpdatedAt string `json:"updated_at"`
}

type TicketType struct {
	ID           int64  `json:"id"`
	EventID      int64  `json:"event_id"`
	Name         string `json:"name"`
	Description  string `json:"description"`
	PriceCents   int64  `json:"price_cents"`
	Currency     string `json:"currency"`
	Capacity     int64  `json:"capacity"`
	SalesStartAt string `json:"sales_start_at"`
	SalesEndAt   string `json:"sales_end_at"`
	Status       string `json:"status"`
	CreatedAt    string `json:"created_at"`
	UpdatedAt    string `json:"updated_at"`
}

type Ticket struct {
	ID            int64  `json:"id"`
	OrderID       int64  `json:"order_id"`
	EventID       int64  `json:"event_id"`
	TicketTypeID  *int64 `json:"ticket_type_id,omitempty"`
	AttendeeName  string `json:"attendee_name"`
	AttendeeEmail string `json:"attendee_email"`
	Status        string `json:"status"`
	CheckinStatus string `json:"checkin_status"`
	CheckedInAt   string `json:"checked_in_at"`
	Code          string `json:"code"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

type Application struct {
	ID               int64  `json:"id"`
	EventID          int64  `json:"event_id"`
	ApplicantName    string `json:"applicant_name"`
	StageName        string `json:"stage_name"`
	Email            string `json:"email"`
	Phone            string `json:"phone"`
	Bio              string `json:"bio"`
	SetLengthMinutes int64  `json:"set_length_minutes"`
	VideoURL         string `json:"video_url"`
	SocialLinksJSON  string `json:"social_links_json"`
	AvailabilityJSON string `json:"availability_json"`
	TechNeeds        string `json:"tech_needs"`
	Notes            string `json:"notes"`
	Status           string `json:"status"`
	Score            int64  `json:"score"`
	ReviewerNotes    string `json:"reviewer_notes"`
	SubmittedAt      string `json:"submitted_at"`
	DecidedAt        string `json:"decided_at"`
	CreatedAt        string `json:"created_at"`
	UpdatedAt        string `json:"updated_at"`
}

type Slot struct {
	ID            int64  `json:"id"`
	EventID       int64  `json:"event_id"`
	VenueID       *int64 `json:"venue_id,omitempty"`
	ApplicationID *int64 `json:"application_id,omitempty"`
	PerformerName string `json:"performer_name"`
	Title         string `json:"title"`
	StartsAt      string `json:"starts_at"`
	EndsAt        string `json:"ends_at"`
	Status        string `json:"status"`
	Notes         string `json:"notes"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`
}

type issueTicketsInput struct {
	EventID       int64  `json:"event_id"`
	TicketTypeID  int64  `json:"ticket_type_id"`
	BuyerName     string `json:"buyer_name"`
	BuyerEmail    string `json:"buyer_email"`
	AttendeeName  string `json:"attendee_name"`
	AttendeeEmail string `json:"attendee_email"`
	Quantity      int64  `json:"quantity"`
	Source        string `json:"source"`
}

func db() *sql.DB { return globalCtx.AppDB() }

func projectID() string {
	return strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID"))
}

func now() string { return time.Now().UTC().Format(time.RFC3339) }

// --- event storage ----------------------------------------------------------

func createEvent(in map[string]any) (*Event, error) {
	title := strings.TrimSpace(argString(in, "title"))
	if title == "" {
		return nil, errors.New("title required")
	}
	slug := strings.TrimSpace(argString(in, "slug"))
	if slug == "" {
		slug = slugify(title)
	}
	status := defaultString(argString(in, "status"), "draft")
	visibility := defaultString(argString(in, "visibility"), "private")
	tz := defaultString(argString(in, "timezone"), "UTC")
	var venue any
	if id := argInt(in, "venue_id"); id > 0 {
		venue = id
	}
	res, err := db().Exec(`
		INSERT INTO events
			(project_id, title, slug, description, status, visibility, timezone, starts_at, ends_at, venue_id, capacity, external_checkout_url, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		projectID(), title, slug, argString(in, "description"), status, visibility, tz,
		argString(in, "starts_at"), argString(in, "ends_at"), venue, argInt(in, "capacity"),
		argString(in, "external_checkout_url"), now())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return getEvent(id)
}

func getEvent(id int64) (*Event, error) {
	row := db().QueryRow(`
		SELECT e.id, e.project_id, e.title, e.slug, e.description, e.status, e.visibility, e.timezone,
		       e.starts_at, e.ends_at, e.venue_id, e.capacity, e.external_checkout_url,
		       (SELECT COUNT(*) FROM tickets t WHERE t.event_id=e.id AND t.status='active'),
		       (SELECT COUNT(*) FROM performer_applications a WHERE a.event_id=e.id),
		       (SELECT COUNT(*) FROM performance_slots s WHERE s.event_id=e.id),
		       e.created_at, e.updated_at
		FROM events e WHERE e.id = ? AND e.project_id = ?`, id, projectID())
	return scanEvent(row)
}

func listEvents(status string, limit int64) ([]Event, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	q := `
		SELECT e.id, e.project_id, e.title, e.slug, e.description, e.status, e.visibility, e.timezone,
		       e.starts_at, e.ends_at, e.venue_id, e.capacity, e.external_checkout_url,
		       (SELECT COUNT(*) FROM tickets t WHERE t.event_id=e.id AND t.status='active'),
		       (SELECT COUNT(*) FROM performer_applications a WHERE a.event_id=e.id),
		       (SELECT COUNT(*) FROM performance_slots s WHERE s.event_id=e.id),
		       e.created_at, e.updated_at
		FROM events e WHERE e.project_id = ?`
	args := []any{projectID()}
	if status != "" {
		q += " AND e.status = ?"
		args = append(args, status)
	}
	q += " ORDER BY CASE WHEN e.starts_at = '' THEN 1 ELSE 0 END, e.starts_at, e.id DESC LIMIT ?"
	args = append(args, limit)
	rows, err := db().Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Event{}
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *e)
	}
	return out, rows.Err()
}

func updateEvent(id int64, in map[string]any) (*Event, error) {
	current, err := getEvent(id)
	if err != nil {
		return nil, err
	}
	if v, ok := in["title"]; ok {
		current.Title = strings.TrimSpace(fmt.Sprint(v))
	}
	if v, ok := in["slug"]; ok {
		current.Slug = slugify(fmt.Sprint(v))
	}
	if v, ok := in["description"]; ok {
		current.Description = fmt.Sprint(v)
	}
	if v, ok := in["status"]; ok {
		current.Status = fmt.Sprint(v)
	}
	if v, ok := in["visibility"]; ok {
		current.Visibility = fmt.Sprint(v)
	}
	if v, ok := in["timezone"]; ok {
		current.Timezone = fmt.Sprint(v)
	}
	if v, ok := in["starts_at"]; ok {
		current.StartsAt = fmt.Sprint(v)
	}
	if v, ok := in["ends_at"]; ok {
		current.EndsAt = fmt.Sprint(v)
	}
	if v, ok := in["capacity"]; ok {
		current.Capacity = toInt(v)
	}
	if v, ok := in["external_checkout_url"]; ok {
		current.ExternalCheckoutURL = fmt.Sprint(v)
	}
	var venue any
	venue = nil
	if current.VenueID != nil {
		venue = *current.VenueID
	}
	if v, ok := in["venue_id"]; ok {
		if n := toInt(v); n > 0 {
			venue = n
		} else {
			venue = nil
		}
	}
	_, err = db().Exec(`
		UPDATE events SET title=?, slug=?, description=?, status=?, visibility=?, timezone=?, starts_at=?, ends_at=?,
			venue_id=?, capacity=?, external_checkout_url=?, updated_at=?
		WHERE id=? AND project_id=?`,
		current.Title, current.Slug, current.Description, current.Status, current.Visibility, current.Timezone,
		current.StartsAt, current.EndsAt, venue, current.Capacity, current.ExternalCheckoutURL, now(), id, projectID())
	if err != nil {
		return nil, err
	}
	return getEvent(id)
}

type scanner interface{ Scan(dest ...any) error }

func scanEvent(row scanner) (*Event, error) {
	var e Event
	var venue sql.NullInt64
	if err := row.Scan(&e.ID, &e.ProjectID, &e.Title, &e.Slug, &e.Description, &e.Status, &e.Visibility, &e.Timezone,
		&e.StartsAt, &e.EndsAt, &venue, &e.Capacity, &e.ExternalCheckoutURL, &e.TicketCount, &e.ApplicationCount, &e.SlotCount, &e.CreatedAt, &e.UpdatedAt); err != nil {
		return nil, err
	}
	if venue.Valid {
		e.VenueID = &venue.Int64
	}
	return &e, nil
}

// --- venue / ticket type storage -------------------------------------------

func createVenue(in map[string]any) (*Venue, error) {
	name := strings.TrimSpace(argString(in, "name"))
	if name == "" {
		return nil, errors.New("name required")
	}
	res, err := db().Exec(`
		INSERT INTO venues (project_id, name, address, city, country, capacity, notes, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		projectID(), name, argString(in, "address"), argString(in, "city"), argString(in, "country"), argInt(in, "capacity"), argString(in, "notes"), now())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return getVenue(id)
}

func getVenue(id int64) (*Venue, error) {
	var v Venue
	err := db().QueryRow(`SELECT id, name, address, city, country, capacity, notes, created_at, updated_at FROM venues WHERE id=? AND project_id=?`, id, projectID()).
		Scan(&v.ID, &v.Name, &v.Address, &v.City, &v.Country, &v.Capacity, &v.Notes, &v.CreatedAt, &v.UpdatedAt)
	if err != nil {
		return nil, err
	}
	return &v, nil
}

func listVenues() ([]Venue, error) {
	rows, err := db().Query(`SELECT id, name, address, city, country, capacity, notes, created_at, updated_at FROM venues WHERE project_id=? ORDER BY name`, projectID())
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Venue{}
	for rows.Next() {
		var v Venue
		if err := rows.Scan(&v.ID, &v.Name, &v.Address, &v.City, &v.Country, &v.Capacity, &v.Notes, &v.CreatedAt, &v.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func createTicketType(in map[string]any) (*TicketType, error) {
	eventID := argInt(in, "event_id")
	if eventID == 0 {
		return nil, errors.New("event_id required")
	}
	if _, err := getEvent(eventID); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(argString(in, "name"))
	if name == "" {
		return nil, errors.New("name required")
	}
	currency := defaultString(argString(in, "currency"), "USD")
	status := defaultString(argString(in, "status"), "active")
	res, err := db().Exec(`
		INSERT INTO ticket_types (event_id, name, description, price_cents, currency, capacity, sales_start_at, sales_end_at, status, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		eventID, name, argString(in, "description"), argInt(in, "price_cents"), currency, argInt(in, "capacity"),
		argString(in, "sales_start_at"), argString(in, "sales_end_at"), status, now())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return getTicketType(id)
}

func getTicketType(id int64) (*TicketType, error) {
	var t TicketType
	err := db().QueryRow(`SELECT id, event_id, name, description, price_cents, currency, capacity, sales_start_at, sales_end_at, status, created_at, updated_at FROM ticket_types WHERE id=?`, id).
		Scan(&t.ID, &t.EventID, &t.Name, &t.Description, &t.PriceCents, &t.Currency, &t.Capacity, &t.SalesStartAt, &t.SalesEndAt, &t.Status, &t.CreatedAt, &t.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if _, err := getEvent(t.EventID); err != nil {
		return nil, err
	}
	return &t, nil
}

func listTicketTypes(eventID int64) ([]TicketType, error) {
	if eventID == 0 {
		return nil, errors.New("event_id required")
	}
	if _, err := getEvent(eventID); err != nil {
		return nil, err
	}
	rows, err := db().Query(`SELECT id, event_id, name, description, price_cents, currency, capacity, sales_start_at, sales_end_at, status, created_at, updated_at FROM ticket_types WHERE event_id=? ORDER BY id`, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []TicketType{}
	for rows.Next() {
		var t TicketType
		if err := rows.Scan(&t.ID, &t.EventID, &t.Name, &t.Description, &t.PriceCents, &t.Currency, &t.Capacity, &t.SalesStartAt, &t.SalesEndAt, &t.Status, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// --- tickets ----------------------------------------------------------------

func issueTickets(in issueTicketsInput) ([]Ticket, error) {
	if in.EventID == 0 || strings.TrimSpace(in.BuyerName) == "" || strings.TrimSpace(in.BuyerEmail) == "" {
		return nil, errors.New("event_id, buyer_name and buyer_email required")
	}
	if in.Quantity <= 0 {
		in.Quantity = 1
	}
	if in.Quantity > 25 {
		return nil, errors.New("quantity cannot exceed 25")
	}
	if in.AttendeeName == "" {
		in.AttendeeName = in.BuyerName
	}
	if in.AttendeeEmail == "" {
		in.AttendeeEmail = in.BuyerEmail
	}
	if in.Source == "" {
		in.Source = "manual"
	}
	event, err := getEvent(in.EventID)
	if err != nil {
		return nil, err
	}
	var price int64
	var currency = "USD"
	var tt any
	if in.TicketTypeID > 0 {
		t, err := getTicketType(in.TicketTypeID)
		if err != nil {
			return nil, err
		}
		price = t.PriceCents
		currency = t.Currency
		tt = in.TicketTypeID
		var sold int64
		_ = db().QueryRow(`SELECT COUNT(*) FROM tickets WHERE ticket_type_id=? AND status='active'`, in.TicketTypeID).Scan(&sold)
		if t.Capacity > 0 && sold+in.Quantity > t.Capacity {
			return nil, fmt.Errorf("ticket type capacity exceeded: %d sold, %d capacity", sold, t.Capacity)
		}
	}
	if event.Capacity > 0 && event.TicketCount+in.Quantity > event.Capacity {
		return nil, fmt.Errorf("event capacity exceeded: %d sold, %d capacity", event.TicketCount, event.Capacity)
	}
	tx, err := db().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(`INSERT INTO orders (event_id, buyer_name, buyer_email, status, total_cents, currency, source, updated_at) VALUES (?, ?, ?, 'confirmed', ?, ?, ?, ?)`,
		in.EventID, in.BuyerName, in.BuyerEmail, price*in.Quantity, currency, in.Source, now())
	if err != nil {
		return nil, err
	}
	orderID, _ := res.LastInsertId()
	out := []Ticket{}
	for i := int64(0); i < in.Quantity; i++ {
		code, err := newCode()
		if err != nil {
			return nil, err
		}
		res, err := tx.Exec(`
			INSERT INTO tickets (order_id, event_id, ticket_type_id, attendee_name, attendee_email, code, updated_at)
			VALUES (?, ?, ?, ?, ?, ?, ?)`,
			orderID, in.EventID, tt, in.AttendeeName, in.AttendeeEmail, code, now())
		if err != nil {
			return nil, err
		}
		id, _ := res.LastInsertId()
		out = append(out, Ticket{ID: id, OrderID: orderID, EventID: in.EventID, AttendeeName: in.AttendeeName, AttendeeEmail: in.AttendeeEmail, Status: "active", CheckinStatus: "not_checked_in", Code: code, UpdatedAt: now()})
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return out, nil
}

func listTickets(eventID int64) ([]Ticket, error) {
	if eventID == 0 {
		return nil, errors.New("event_id required")
	}
	if _, err := getEvent(eventID); err != nil {
		return nil, err
	}
	rows, err := db().Query(`SELECT id, order_id, event_id, ticket_type_id, attendee_name, attendee_email, status, checkin_status, checked_in_at, code, created_at, updated_at FROM tickets WHERE event_id=? ORDER BY id DESC`, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTickets(rows)
}

func checkInTicket(id int64, code string) (*Ticket, error) {
	if id == 0 && code == "" {
		return nil, errors.New("id or code required")
	}
	where := "id = ?"
	arg := any(id)
	if code != "" {
		where = "code = ?"
		arg = code
	}
	var eventID int64
	if err := db().QueryRow(`SELECT event_id FROM tickets WHERE `+where, arg).Scan(&eventID); err != nil {
		return nil, err
	}
	if _, err := getEvent(eventID); err != nil {
		return nil, err
	}
	_, err := db().Exec(`UPDATE tickets SET checkin_status='checked_in', checked_in_at=?, updated_at=? WHERE `+where, now(), now(), arg)
	if err != nil {
		return nil, err
	}
	return getTicketBy(where, arg)
}

func getTicketBy(where string, arg any) (*Ticket, error) {
	rows, err := db().Query(`SELECT id, order_id, event_id, ticket_type_id, attendee_name, attendee_email, status, checkin_status, checked_in_at, code, created_at, updated_at FROM tickets WHERE `+where+` LIMIT 1`, arg)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ts, err := scanTickets(rows)
	if err != nil {
		return nil, err
	}
	if len(ts) == 0 {
		return nil, sql.ErrNoRows
	}
	return &ts[0], nil
}

func scanTickets(rows *sql.Rows) ([]Ticket, error) {
	out := []Ticket{}
	for rows.Next() {
		var t Ticket
		var tt sql.NullInt64
		if err := rows.Scan(&t.ID, &t.OrderID, &t.EventID, &tt, &t.AttendeeName, &t.AttendeeEmail, &t.Status, &t.CheckinStatus, &t.CheckedInAt, &t.Code, &t.CreatedAt, &t.UpdatedAt); err != nil {
			return nil, err
		}
		if tt.Valid {
			t.TicketTypeID = &tt.Int64
		}
		out = append(out, t)
	}
	return out, rows.Err()
}

// --- applications and slots -------------------------------------------------

func submitApplication(in map[string]any) (*Application, error) {
	eventID := argInt(in, "event_id")
	if eventID == 0 || strings.TrimSpace(argString(in, "applicant_name")) == "" || strings.TrimSpace(argString(in, "email")) == "" {
		return nil, errors.New("event_id, applicant_name and email required")
	}
	if _, err := getEvent(eventID); err != nil {
		return nil, err
	}
	social := jsonString(in["social_links"], "{}")
	availability := jsonString(in["availability"], "{}")
	res, err := db().Exec(`
		INSERT INTO performer_applications
			(event_id, applicant_name, stage_name, email, phone, bio, set_length_minutes, video_url, social_links_json, availability_json, tech_needs, notes, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		eventID, argString(in, "applicant_name"), argString(in, "stage_name"), argString(in, "email"), argString(in, "phone"), argString(in, "bio"),
		argInt(in, "set_length_minutes"), argString(in, "video_url"), social, availability, argString(in, "tech_needs"), argString(in, "notes"), now())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return getApplication(id)
}

func getApplication(id int64) (*Application, error) {
	var a Application
	err := db().QueryRow(`SELECT id, event_id, applicant_name, stage_name, email, phone, bio, set_length_minutes, video_url, social_links_json, availability_json, tech_needs, notes, status, score, reviewer_notes, submitted_at, decided_at, created_at, updated_at FROM performer_applications WHERE id=?`, id).
		Scan(&a.ID, &a.EventID, &a.ApplicantName, &a.StageName, &a.Email, &a.Phone, &a.Bio, &a.SetLengthMinutes, &a.VideoURL, &a.SocialLinksJSON, &a.AvailabilityJSON, &a.TechNeeds, &a.Notes, &a.Status, &a.Score, &a.ReviewerNotes, &a.SubmittedAt, &a.DecidedAt, &a.CreatedAt, &a.UpdatedAt)
	if err != nil {
		return nil, err
	}
	if _, err := getEvent(a.EventID); err != nil {
		return nil, err
	}
	return &a, nil
}

func listApplications(eventID int64, status string) ([]Application, error) {
	if eventID == 0 {
		return nil, errors.New("event_id required")
	}
	if _, err := getEvent(eventID); err != nil {
		return nil, err
	}
	q := `SELECT id, event_id, applicant_name, stage_name, email, phone, bio, set_length_minutes, video_url, social_links_json, availability_json, tech_needs, notes, status, score, reviewer_notes, submitted_at, decided_at, created_at, updated_at FROM performer_applications WHERE event_id=?`
	args := []any{eventID}
	if status != "" {
		q += " AND status=?"
		args = append(args, status)
	}
	q += " ORDER BY submitted_at DESC, id DESC"
	rows, err := db().Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Application{}
	for rows.Next() {
		var a Application
		if err := rows.Scan(&a.ID, &a.EventID, &a.ApplicantName, &a.StageName, &a.Email, &a.Phone, &a.Bio, &a.SetLengthMinutes, &a.VideoURL, &a.SocialLinksJSON, &a.AvailabilityJSON, &a.TechNeeds, &a.Notes, &a.Status, &a.Score, &a.ReviewerNotes, &a.SubmittedAt, &a.DecidedAt, &a.CreatedAt, &a.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, a)
	}
	return out, rows.Err()
}

func reviewApplication(id int64, in map[string]any) (*Application, error) {
	app, err := getApplication(id)
	if err != nil {
		return nil, err
	}
	status := app.Status
	score := app.Score
	notes := app.ReviewerNotes
	decided := app.DecidedAt
	if v, ok := in["status"]; ok {
		status = strings.TrimSpace(fmt.Sprint(v))
		if status == "accepted" || status == "rejected" {
			decided = now()
		}
	}
	if v, ok := in["score"]; ok {
		score = toInt(v)
	}
	if v, ok := in["reviewer_notes"]; ok {
		notes = fmt.Sprint(v)
	}
	_, err = db().Exec(`UPDATE performer_applications SET status=?, score=?, reviewer_notes=?, decided_at=?, updated_at=? WHERE id=?`,
		status, score, notes, decided, now(), id)
	if err != nil {
		return nil, err
	}
	return getApplication(id)
}

func scheduleApplication(in map[string]any) (*Slot, error) {
	app, err := getApplication(argInt(in, "application_id"))
	if err != nil {
		return nil, err
	}
	if app.Status != "accepted" {
		if _, err := reviewApplication(app.ID, map[string]any{"status": "accepted"}); err != nil {
			return nil, err
		}
	}
	name := strings.TrimSpace(app.StageName)
	if name == "" {
		name = app.ApplicantName
	}
	slotIn := map[string]any{
		"event_id":       app.EventID,
		"application_id": app.ID,
		"performer_name": name,
		"title":          argString(in, "title"),
		"starts_at":      argString(in, "starts_at"),
		"ends_at":        argString(in, "ends_at"),
		"notes":          argString(in, "notes"),
	}
	if venue := argInt(in, "venue_id"); venue > 0 {
		slotIn["venue_id"] = venue
	}
	return createSlot(slotIn)
}

func createSlot(in map[string]any) (*Slot, error) {
	eventID := argInt(in, "event_id")
	if eventID == 0 {
		return nil, errors.New("event_id required")
	}
	if _, err := getEvent(eventID); err != nil {
		return nil, err
	}
	name := strings.TrimSpace(argString(in, "performer_name"))
	if name == "" {
		return nil, errors.New("performer_name required")
	}
	var venue, app any
	if id := argInt(in, "venue_id"); id > 0 {
		venue = id
	}
	if id := argInt(in, "application_id"); id > 0 {
		app = id
	}
	res, err := db().Exec(`
		INSERT INTO performance_slots (event_id, venue_id, application_id, performer_name, title, starts_at, ends_at, notes, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		eventID, venue, app, name, argString(in, "title"), argString(in, "starts_at"), argString(in, "ends_at"), argString(in, "notes"), now())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return getSlot(id)
}

func getSlot(id int64) (*Slot, error) {
	rows, err := db().Query(`SELECT id, event_id, venue_id, application_id, performer_name, title, starts_at, ends_at, status, notes, created_at, updated_at FROM performance_slots WHERE id=?`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	slots, err := scanSlots(rows)
	if err != nil {
		return nil, err
	}
	if len(slots) == 0 {
		return nil, sql.ErrNoRows
	}
	if _, err := getEvent(slots[0].EventID); err != nil {
		return nil, err
	}
	return &slots[0], nil
}

func listSlots(eventID int64) ([]Slot, error) {
	if eventID == 0 {
		return nil, errors.New("event_id required")
	}
	if _, err := getEvent(eventID); err != nil {
		return nil, err
	}
	rows, err := db().Query(`SELECT id, event_id, venue_id, application_id, performer_name, title, starts_at, ends_at, status, notes, created_at, updated_at FROM performance_slots WHERE event_id=? ORDER BY starts_at, id`, eventID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanSlots(rows)
}

func scanSlots(rows *sql.Rows) ([]Slot, error) {
	out := []Slot{}
	for rows.Next() {
		var s Slot
		var venue, app sql.NullInt64
		if err := rows.Scan(&s.ID, &s.EventID, &venue, &app, &s.PerformerName, &s.Title, &s.StartsAt, &s.EndsAt, &s.Status, &s.Notes, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, err
		}
		if venue.Valid {
			s.VenueID = &venue.Int64
		}
		if app.Valid {
			s.ApplicationID = &app.Int64
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// --- HTTP handlers ----------------------------------------------------------

func (a *App) handleEvents(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		out, err := listEvents(r.URL.Query().Get("status"), int64(queryInt(r, "limit")))
		writeResult(w, out, err)
	case http.MethodPost:
		var in map[string]any
		if !decodeJSON(w, r, &in) {
			return
		}
		out, err := createEvent(in)
		writeResult(w, out, err)
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleEventsItem(w http.ResponseWriter, r *http.Request) {
	id, _ := parseIDAction(r.URL.Path, "/events/")
	switch r.Method {
	case http.MethodGet:
		writeAppResult(w, resultOf(getEvent(id)))
	case http.MethodPatch, http.MethodPut:
		var in map[string]any
		if !decodeJSON(w, r, &in) {
			return
		}
		writeAppResult(w, resultOf(updateEvent(id, in)))
	default:
		http.Error(w, "GET or PATCH", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleVenues(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeAppResult(w, resultOf(listVenues()))
	case http.MethodPost:
		var in map[string]any
		if !decodeJSON(w, r, &in) {
			return
		}
		writeAppResult(w, resultOf(createVenue(in)))
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleTicketTypes(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeAppResult(w, resultOf(listTicketTypes(int64(queryInt(r, "event_id")))))
	case http.MethodPost:
		var in map[string]any
		if !decodeJSON(w, r, &in) {
			return
		}
		writeAppResult(w, resultOf(createTicketType(in)))
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleTickets(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeAppResult(w, resultOf(listTickets(int64(queryInt(r, "event_id")))))
	case http.MethodPost:
		var in issueTicketsInput
		if !decodeJSON(w, r, &in) {
			return
		}
		writeAppResult(w, resultOf(issueTickets(in)))
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleTicketsItem(w http.ResponseWriter, r *http.Request) {
	id, action := parseIDAction(r.URL.Path, "/tickets/")
	switch {
	case r.Method == http.MethodPost && action == "check_in":
		writeAppResult(w, resultOf(checkInTicket(id, "")))
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func (a *App) handleApplications(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeAppResult(w, resultOf(listApplications(int64(queryInt(r, "event_id")), r.URL.Query().Get("status"))))
	case http.MethodPost:
		var in map[string]any
		if !decodeJSON(w, r, &in) {
			return
		}
		writeAppResult(w, resultOf(submitApplication(in)))
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleApplicationsItem(w http.ResponseWriter, r *http.Request) {
	id, action := parseIDAction(r.URL.Path, "/applications/")
	switch {
	case r.Method == http.MethodPatch || r.Method == http.MethodPut:
		var in map[string]any
		if !decodeJSON(w, r, &in) {
			return
		}
		writeAppResult(w, resultOf(reviewApplication(id, in)))
	case r.Method == http.MethodPost && action == "accept":
		writeAppResult(w, resultOf(reviewApplication(id, map[string]any{"status": "accepted"})))
	case r.Method == http.MethodPost && action == "reject":
		writeAppResult(w, resultOf(reviewApplication(id, map[string]any{"status": "rejected"})))
	default:
		http.Error(w, "not found", http.StatusNotFound)
	}
}

func (a *App) handleSlots(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeAppResult(w, resultOf(listSlots(int64(queryInt(r, "event_id")))))
	case http.MethodPost:
		var in map[string]any
		if !decodeJSON(w, r, &in) {
			return
		}
		if argInt(in, "application_id") > 0 && strings.TrimSpace(argString(in, "performer_name")) == "" {
			writeAppResult(w, resultOf(scheduleApplication(in)))
		} else {
			writeAppResult(w, resultOf(createSlot(in)))
		}
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

func (a *App) handlePublic(w http.ResponseWriter, r *http.Request) {
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/public/"), "/")
	parts := strings.Split(rest, "/")
	if len(parts) == 0 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	event, err := getPublicEvent(parts[0])
	if err != nil {
		http.Error(w, err.Error(), http.StatusNotFound)
		return
	}
	if len(parts) == 1 && r.Method == http.MethodGet {
		types, _ := listTicketTypes(event.ID)
		slots, _ := listSlots(event.ID)
		writeJSON(w, map[string]any{"event": event, "ticket_types": types, "schedule": slots})
		return
	}
	var in map[string]any
	if !decodeJSON(w, r, &in) {
		return
	}
	in["event_id"] = event.ID
	switch {
	case len(parts) == 2 && parts[1] == "apply" && r.Method == http.MethodPost:
		writeAppResult(w, resultOf(submitApplication(in)))
	case len(parts) == 2 && parts[1] == "register" && r.Method == http.MethodPost:
		issue := issueTicketsInput{
			EventID: event.ID, BuyerName: argString(in, "buyer_name"), BuyerEmail: argString(in, "buyer_email"),
			AttendeeName: argString(in, "attendee_name"), AttendeeEmail: argString(in, "attendee_email"),
			TicketTypeID: argInt(in, "ticket_type_id"), Quantity: argInt(in, "quantity"), Source: "public",
		}
		writeAppResult(w, resultOf(issueTickets(issue)))
	default:
		http.NotFound(w, r)
	}
}

func getPublicEvent(slug string) (*Event, error) {
	row := db().QueryRow(`
		SELECT e.id, e.project_id, e.title, e.slug, e.description, e.status, e.visibility, e.timezone,
		       e.starts_at, e.ends_at, e.venue_id, e.capacity, e.external_checkout_url,
		       (SELECT COUNT(*) FROM tickets t WHERE t.event_id=e.id AND t.status='active'),
		       (SELECT COUNT(*) FROM performer_applications a WHERE a.event_id=e.id),
		       (SELECT COUNT(*) FROM performance_slots s WHERE s.event_id=e.id),
		       e.created_at, e.updated_at
		FROM events e WHERE e.slug = ? AND e.project_id = ? AND e.status='published' AND e.visibility='public'`,
		slug, projectID())
	return scanEvent(row)
}

// --- MCP handlers -----------------------------------------------------------

func (a *App) toolEventsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return createEvent(args)
}

func (a *App) toolEventsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return listEvents(argString(args, "status"), argInt(args, "limit"))
}

func (a *App) toolApplicationsSubmit(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return submitApplication(args)
}

func (a *App) toolApplicationsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return listApplications(argInt(args, "event_id"), argString(args, "status"))
}

func (a *App) toolApplicationsReview(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return reviewApplication(argInt(args, "id"), args)
}

func (a *App) toolScheduleApplication(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return scheduleApplication(args)
}

func (a *App) toolTicketsIssue(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	in := issueTicketsInput{
		EventID: argInt(args, "event_id"), TicketTypeID: argInt(args, "ticket_type_id"),
		BuyerName: argString(args, "buyer_name"), BuyerEmail: argString(args, "buyer_email"),
		AttendeeName: argString(args, "attendee_name"), AttendeeEmail: argString(args, "attendee_email"),
		Quantity: argInt(args, "quantity"), Source: "agent",
	}
	return issueTickets(in)
}

func (a *App) toolTicketsCheckIn(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return checkInTicket(argInt(args, "id"), argString(args, "code"))
}

// --- utilities --------------------------------------------------------------

func decodeJSON(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		http.Error(w, "invalid json: "+err.Error(), http.StatusBadRequest)
		return false
	}
	return true
}

func writeResult[T any](w http.ResponseWriter, v T, err error) {
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, sql.ErrNoRows) {
			status = http.StatusNotFound
		}
		http.Error(w, err.Error(), status)
		return
	}
	writeJSON(w, v)
}

type appResult struct {
	value any
	err   error
}

func resultOf(v any, err error) appResult {
	return appResult{value: v, err: err}
}

func writeAppResult(w http.ResponseWriter, r appResult) {
	writeResult(w, r.value, r.err)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func schemaObject(props map[string]any, required []string) map[string]any {
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func queryInt(r *http.Request, key string) int {
	n, _ := strconv.Atoi(r.URL.Query().Get(key))
	return n
}

func parseIDAction(path, prefix string) (int64, string) {
	rest := strings.Trim(strings.TrimPrefix(path, prefix), "/")
	parts := strings.Split(rest, "/")
	id, _ := strconv.ParseInt(parts[0], 10, 64)
	if len(parts) > 1 {
		return id, parts[1]
	}
	return id, ""
}

func argString(m map[string]any, key string) string {
	if m == nil {
		return ""
	}
	v, ok := m[key]
	if !ok || v == nil {
		return ""
	}
	return strings.TrimSpace(fmt.Sprint(v))
}

func argInt(m map[string]any, key string) int64 {
	if m == nil {
		return 0
	}
	return toInt(m[key])
}

func toInt(v any) int64 {
	switch t := v.(type) {
	case int:
		return int64(t)
	case int64:
		return t
	case float64:
		return int64(t)
	case json.Number:
		n, _ := t.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(t), 10, 64)
		return n
	default:
		return 0
	}
}

func defaultString(v, fallback string) string {
	if strings.TrimSpace(v) == "" {
		return fallback
	}
	return v
}

func jsonString(v any, fallback string) string {
	if v == nil {
		return fallback
	}
	if s, ok := v.(string); ok {
		if strings.TrimSpace(s) == "" {
			return fallback
		}
		return s
	}
	b, err := json.Marshal(v)
	if err != nil {
		return fallback
	}
	return string(b)
}

var slugRe = regexp.MustCompile(`[^a-z0-9]+`)

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = slugRe.ReplaceAllString(s, "-")
	s = strings.Trim(s, "-")
	if s == "" {
		return "event"
	}
	return s
}

func newCode() (string, error) {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return "", err
	}
	return "EVT-" + strings.ToUpper(hex.EncodeToString(b[:])), nil
}

func main() {
	sdk.Run(&App{})
}
