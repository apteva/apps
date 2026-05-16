// trips v0.1 — plan trips with full itinerary + budget; mirrored into a
// dedicated per-trip calendar.
//
// Each trip owns a calendar created via calendar.calendars_create at
// trips_create time; transport_legs / accommodations / activities (with
// a start_at) become events in that calendar via calendar.events_create.
// The rows store the returned event_id so subsequent updates upsert via
// calendar.events_update instead of creating duplicates. Trip delete
// cascades to calendar via calendars_delete.
//
// Calendar mirror failures are best-effort: if calendar is unreachable
// we log + continue. The DB row stays consistent (calendar_event_id
// just stays NULL) and a future explicit re-sync can heal the drift.
//
// Money is signed integer minor units. Per-item currency is supported;
// budget_summary aggregates with a 1:1 fallback when currencies differ
// (v0.2 wires real FX, probably via finance).
package main

import (
	"database/sql"
	_ "embed"
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

//go:embed apteva.yaml
var manifestYAML []byte

var globalCtx *sdk.AppCtx

type App struct{}

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest(manifestYAML)
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("trips requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("trips mounted")
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

// ─── HTTP routes ─────────────────────────────────────────────────

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/trips", Handler: a.handleTrips},
		{Pattern: "/trips/", Handler: a.handleTripsItem},
		{Pattern: "/destinations", Handler: a.handleDestinations},
		{Pattern: "/destinations/", Handler: a.handleDestinationsItem},
		{Pattern: "/destinations/reorder", Handler: a.handleDestinationsReorder},
		{Pattern: "/transport-legs", Handler: a.handleTransportLegs},
		{Pattern: "/transport-legs/", Handler: a.handleTransportLegsItem},
		{Pattern: "/accommodations", Handler: a.handleAccommodations},
		{Pattern: "/accommodations/", Handler: a.handleAccommodationsItem},
		{Pattern: "/activities", Handler: a.handleActivities},
		{Pattern: "/activities/", Handler: a.handleActivitiesItem},
		{Pattern: "/todos", Handler: a.handleTodos},
		{Pattern: "/todos/", Handler: a.handleTodosItem},
		{Pattern: "/budget", Handler: a.handleBudget},
		{Pattern: "/budget/summary", Handler: a.handleBudgetSummary},
		{Pattern: "/dashboard", Handler: a.handleDashboard},
	}
}

// ─── MCP tools ───────────────────────────────────────────────────

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		// Trips
		{Name: "trips_list", Description: "List trips with derived planned/actual totals + days-until.",
			InputSchema: schemaObject(map[string]any{"include_archived": map[string]any{"type": "boolean"}}, nil),
			Handler:     a.toolTripsList},
		{Name: "trips_get", Description: "Read one trip (no children; use dashboard for the full payload).",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolTripsGet},
		{Name: "trips_create", Description: "Create a trip. Args: name, start_at, end_at, home_currency?, color?, purpose?, notes?, sync_calendar? (default true). Also calls calendar.calendars_create.",
			InputSchema: schemaObject(map[string]any{
				"name":          map[string]any{"type": "string"},
				"start_at":      map[string]any{"type": "string"},
				"end_at":        map[string]any{"type": "string"},
				"home_currency": map[string]any{"type": "string"},
				"color":         map[string]any{"type": "string"},
				"purpose":       map[string]any{"type": "string"},
				"notes":         map[string]any{"type": "string"},
				"sync_calendar": map[string]any{"type": "boolean"},
			}, []string{"name", "start_at", "end_at"}),
			Handler: a.toolTripsCreate},
		{Name: "trips_update", Description: "Update a trip. Renames the linked calendar when name changes; toggling sync_calendar on/off retroactively adds/removes calendar events.",
			InputSchema: schemaObject(map[string]any{
				"id":            map[string]any{"type": "integer"},
				"name":          map[string]any{"type": "string"},
				"start_at":      map[string]any{"type": "string"},
				"end_at":        map[string]any{"type": "string"},
				"status":        map[string]any{"type": "string", "enum": []string{"planning", "booked", "in_progress", "done", "cancelled"}},
				"color":         map[string]any{"type": "string"},
				"notes":         map[string]any{"type": "string"},
				"total_budget":  map[string]any{"type": "integer"},
				"sync_calendar": map[string]any{"type": "boolean"},
				"archived":      map[string]any{"type": "boolean"},
			}, []string{"id"}),
			Handler: a.toolTripsUpdate},
		{Name: "trips_delete", Description: "Delete a trip and cascade-delete its calendar.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolTripsDelete},

		// Destinations
		{Name: "destinations_add", Description: "Add a destination. Args: trip_id, place_name, arrive_at, depart_at, country?, lat?, lng?, notes?.",
			InputSchema: schemaObject(map[string]any{
				"trip_id":    map[string]any{"type": "integer"},
				"place_name": map[string]any{"type": "string"},
				"country":    map[string]any{"type": "string"},
				"lat":        map[string]any{"type": "number"},
				"lng":        map[string]any{"type": "number"},
				"arrive_at":  map[string]any{"type": "string"},
				"depart_at":  map[string]any{"type": "string"},
				"notes":      map[string]any{"type": "string"},
			}, []string{"trip_id", "place_name", "arrive_at", "depart_at"}),
			Handler: a.toolDestinationsAdd},
		{Name: "destinations_update", Description: "Update a destination.",
			InputSchema: schemaObject(map[string]any{
				"id":         map[string]any{"type": "integer"},
				"place_name": map[string]any{"type": "string"},
				"country":    map[string]any{"type": "string"},
				"lat":        map[string]any{"type": "number"},
				"lng":        map[string]any{"type": "number"},
				"arrive_at":  map[string]any{"type": "string"},
				"depart_at":  map[string]any{"type": "string"},
				"notes":      map[string]any{"type": "string"},
			}, []string{"id"}),
			Handler: a.toolDestinationsUpdate},
		{Name: "destinations_delete", Description: "Delete a destination.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolDestinationsDelete},
		{Name: "destinations_reorder", Description: "Reorder a trip's destinations. Args: trip_id, order (array of destination ids in the new sequence).",
			InputSchema: schemaObject(map[string]any{
				"trip_id": map[string]any{"type": "integer"},
				"order":   map[string]any{"type": "array", "items": map[string]any{"type": "integer"}},
			}, []string{"trip_id", "order"}),
			Handler: a.toolDestinationsReorder},

		// Transport
		{Name: "transport_legs_add", Description: "Add a transport leg. Mirrors into the trip's calendar.",
			InputSchema: schemaObject(transportSchemaProps(true), []string{"trip_id", "kind", "depart_at", "arrive_at"}),
			Handler:     a.toolTransportLegsAdd},
		{Name: "transport_legs_update", Description: "Update a transport leg; upserts its calendar event.",
			InputSchema: schemaObject(transportSchemaProps(false), []string{"id"}),
			Handler:     a.toolTransportLegsUpdate},
		{Name: "transport_legs_delete", Description: "Delete a transport leg + its calendar event.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolTransportLegsDelete},
		{Name: "transport_legs_mark_booked", Description: "Flip booked=1 and optionally set cost_actual + confirmation_number.",
			InputSchema: schemaObject(map[string]any{
				"id":                  map[string]any{"type": "integer"},
				"cost_actual":         map[string]any{"type": "integer"},
				"confirmation_number": map[string]any{"type": "string"},
			}, []string{"id"}),
			Handler: a.toolTransportLegsMarkBooked},

		// Accommodations
		{Name: "accommodations_add", Description: "Add an accommodation. Mirrors as an all-day event from check_in_at to check_out_at.",
			InputSchema: schemaObject(accommodationSchemaProps(true), []string{"trip_id", "name", "check_in_at", "check_out_at"}),
			Handler:     a.toolAccommodationsAdd},
		{Name: "accommodations_update", Description: "Update an accommodation; upserts its calendar event.",
			InputSchema: schemaObject(accommodationSchemaProps(false), []string{"id"}),
			Handler:     a.toolAccommodationsUpdate},
		{Name: "accommodations_delete", Description: "Delete an accommodation.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolAccommodationsDelete},
		{Name: "accommodations_mark_booked", Description: "Mark booked + optional cost_actual + confirmation_number.",
			InputSchema: schemaObject(map[string]any{
				"id":                  map[string]any{"type": "integer"},
				"cost_actual":         map[string]any{"type": "integer"},
				"confirmation_number": map[string]any{"type": "string"},
			}, []string{"id"}),
			Handler: a.toolAccommodationsMarkBooked},

		// Activities
		{Name: "activities_add", Description: "Add an activity. Mirrors into the calendar when start_at is set.",
			InputSchema: schemaObject(activitySchemaProps(true), []string{"trip_id", "name"}),
			Handler:     a.toolActivitiesAdd},
		{Name: "activities_update", Description: "Update an activity; upserts the calendar event if applicable.",
			InputSchema: schemaObject(activitySchemaProps(false), []string{"id"}),
			Handler:     a.toolActivitiesUpdate},
		{Name: "activities_delete", Description: "Delete an activity.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolActivitiesDelete},
		{Name: "activities_mark_booked", Description: "Mark booked + optional cost_actual.",
			InputSchema: schemaObject(map[string]any{
				"id":          map[string]any{"type": "integer"},
				"cost_actual": map[string]any{"type": "integer"},
			}, []string{"id"}),
			Handler: a.toolActivitiesMarkBooked},

		// Todos
		{Name: "todos_list", Description: "List todos for a trip. Args: trip_id, include_done? (default false).",
			InputSchema: schemaObject(map[string]any{
				"trip_id":      map[string]any{"type": "integer"},
				"include_done": map[string]any{"type": "boolean"},
			}, []string{"trip_id"}),
			Handler: a.toolTodosList},
		{Name: "todos_add", Description: "Add a todo. Args: trip_id, label, due_at?.",
			InputSchema: schemaObject(map[string]any{
				"trip_id": map[string]any{"type": "integer"},
				"label":   map[string]any{"type": "string"},
				"due_at":  map[string]any{"type": "string"},
			}, []string{"trip_id", "label"}),
			Handler: a.toolTodosAdd},
		{Name: "todos_toggle", Description: "Toggle done state.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolTodosToggle},
		{Name: "todos_delete", Description: "Delete a todo.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolTodosDelete},

		// Budget
		{Name: "budget_set", Description: "Upsert a per-category cap. Pass amount=0 to clear. Args: trip_id, category (transport|lodging|food|activities|shopping|other), amount.",
			InputSchema: schemaObject(map[string]any{
				"trip_id":  map[string]any{"type": "integer"},
				"category": map[string]any{"type": "string", "enum": budgetCategories()},
				"amount":   map[string]any{"type": "integer"},
			}, []string{"trip_id", "category", "amount"}),
			Handler: a.toolBudgetSet},
		{Name: "budget_summary", Description: "Per-category planned/actual/cap/delta + totals.",
			InputSchema: schemaObject(map[string]any{"trip_id": map[string]any{"type": "integer"}}, []string{"trip_id"}),
			Handler:     a.toolBudgetSummary},

		// Dashboard
		{Name: "dashboard", Description: "Trip + all children + budget summary in a single payload (drives the panel detail view).",
			InputSchema: schemaObject(map[string]any{"trip_id": map[string]any{"type": "integer"}}, []string{"trip_id"}),
			Handler:     a.toolDashboard},
	}
}

func main() { sdk.Run(&App{}) }

// ─── Constants ───────────────────────────────────────────────────

func transportKinds() []string {
	return []string{"flight", "train", "car", "bus", "ferry", "other"}
}
func accommodationKinds() []string {
	return []string{"hotel", "airbnb", "hostel", "rental", "friend", "other"}
}
func activityCategories() []string {
	return []string{"food", "activity", "shopping", "transport_local", "other"}
}
func budgetCategories() []string {
	return []string{"transport", "lodging", "food", "activities", "shopping", "other"}
}

// activityToBudget maps the per-activity `category` enum onto the
// trip-level budget bucket. transport_local rolls up to "transport"
// alongside flights/trains; "activity" rolls up to "activities".
func activityToBudget(cat string) string {
	switch cat {
	case "food":
		return "food"
	case "activity":
		return "activities"
	case "shopping":
		return "shopping"
	case "transport_local":
		return "transport"
	}
	return "other"
}

func transportSchemaProps(forAdd bool) map[string]any {
	p := map[string]any{
		"trip_id":             map[string]any{"type": "integer"},
		"kind":                map[string]any{"type": "string", "enum": transportKinds()},
		"depart_at":           map[string]any{"type": "string"},
		"arrive_at":           map[string]any{"type": "string"},
		"provider":            map[string]any{"type": "string"},
		"reference":           map[string]any{"type": "string"},
		"depart_location":     map[string]any{"type": "string"},
		"arrive_location":     map[string]any{"type": "string"},
		"cost_estimated":      map[string]any{"type": "integer"},
		"cost_actual":         map[string]any{"type": "integer"},
		"currency":            map[string]any{"type": "string"},
		"confirmation_number": map[string]any{"type": "string"},
		"from_destination_id": map[string]any{"type": "integer"},
		"to_destination_id":   map[string]any{"type": "integer"},
		"notes":               map[string]any{"type": "string"},
	}
	if !forAdd {
		p["id"] = map[string]any{"type": "integer"}
	}
	return p
}

func accommodationSchemaProps(forAdd bool) map[string]any {
	p := map[string]any{
		"trip_id":             map[string]any{"type": "integer"},
		"destination_id":      map[string]any{"type": "integer"},
		"name":                map[string]any{"type": "string"},
		"kind":                map[string]any{"type": "string", "enum": accommodationKinds()},
		"address":             map[string]any{"type": "string"},
		"check_in_at":         map[string]any{"type": "string"},
		"check_out_at":        map[string]any{"type": "string"},
		"cost_estimated":      map[string]any{"type": "integer"},
		"cost_actual":         map[string]any{"type": "integer"},
		"currency":            map[string]any{"type": "string"},
		"confirmation_number": map[string]any{"type": "string"},
		"notes":               map[string]any{"type": "string"},
	}
	if !forAdd {
		p["id"] = map[string]any{"type": "integer"}
	}
	return p
}

func activitySchemaProps(forAdd bool) map[string]any {
	p := map[string]any{
		"trip_id":        map[string]any{"type": "integer"},
		"destination_id": map[string]any{"type": "integer"},
		"name":           map[string]any{"type": "string"},
		"category":       map[string]any{"type": "string", "enum": activityCategories()},
		"start_at":       map[string]any{"type": "string"},
		"end_at":         map[string]any{"type": "string"},
		"location":       map[string]any{"type": "string"},
		"cost_estimated": map[string]any{"type": "integer"},
		"cost_actual":    map[string]any{"type": "integer"},
		"currency":       map[string]any{"type": "string"},
		"notes":          map[string]any{"type": "string"},
	}
	if !forAdd {
		p["id"] = map[string]any{"type": "integer"}
	}
	return p
}

// ─── Types ───────────────────────────────────────────────────────

type Trip struct {
	ID            int64  `json:"id"`
	ProjectID     string `json:"project_id"`
	Name          string `json:"name"`
	Purpose       string `json:"purpose"`
	Status        string `json:"status"`
	StartAt       string `json:"start_at"`
	EndAt         string `json:"end_at"`
	HomeCurrency  string `json:"home_currency"`
	TotalBudget   *int64 `json:"total_budget,omitempty"`
	Participants  []string `json:"participants"`
	Notes         string `json:"notes"`
	Color         string `json:"color"`
	CalendarID    *int64 `json:"calendar_id,omitempty"`
	SyncCalendar  bool   `json:"sync_calendar"`
	Archived      bool   `json:"archived"`
	CreatedAt     string `json:"created_at"`
	UpdatedAt     string `json:"updated_at"`

	// Aggregates populated by trips_list (and only there — toolTripsGet
	// leaves them nil since the list rollup hits computeBudgetSummary
	// per row and would be wasteful for a single-trip read). Sums are
	// in trip.home_currency, minor units.
	TotalPlanned *int64 `json:"total_planned,omitempty"`
	TotalActual  *int64 `json:"total_actual,omitempty"`
}

type Destination struct {
	ID         int64    `json:"id"`
	TripID     int64    `json:"trip_id"`
	PlaceName  string   `json:"place_name"`
	Country    string   `json:"country"`
	Lat        *float64 `json:"lat,omitempty"`
	Lng        *float64 `json:"lng,omitempty"`
	ArriveAt   string   `json:"arrive_at"`
	DepartAt   string   `json:"depart_at"`
	OrderIdx   int      `json:"order_idx"`
	Notes      string   `json:"notes"`
	CreatedAt  string   `json:"created_at"`
}

type TransportLeg struct {
	ID                  int64  `json:"id"`
	TripID              int64  `json:"trip_id"`
	FromDestinationID   int64  `json:"from_destination_id,omitempty"`
	ToDestinationID     int64  `json:"to_destination_id,omitempty"`
	Kind                string `json:"kind"`
	Provider            string `json:"provider"`
	Reference           string `json:"reference"`
	DepartAt            string `json:"depart_at"`
	ArriveAt            string `json:"arrive_at"`
	DepartLocation      string `json:"depart_location"`
	ArriveLocation      string `json:"arrive_location"`
	CostEstimated       *int64 `json:"cost_estimated,omitempty"`
	CostActual          *int64 `json:"cost_actual,omitempty"`
	Currency            string `json:"currency"`
	ConfirmationNumber  string `json:"confirmation_number"`
	Booked              bool   `json:"booked"`
	Notes               string `json:"notes"`
	CalendarEventID     *int64 `json:"calendar_event_id,omitempty"`
	CreatedAt           string `json:"created_at"`
	UpdatedAt           string `json:"updated_at"`
}

type Accommodation struct {
	ID                  int64  `json:"id"`
	TripID              int64  `json:"trip_id"`
	DestinationID       int64  `json:"destination_id,omitempty"`
	Name                string `json:"name"`
	Kind                string `json:"kind"`
	Address             string `json:"address"`
	CheckInAt           string `json:"check_in_at"`
	CheckOutAt          string `json:"check_out_at"`
	CostEstimated       *int64 `json:"cost_estimated,omitempty"`
	CostActual          *int64 `json:"cost_actual,omitempty"`
	Currency            string `json:"currency"`
	ConfirmationNumber  string `json:"confirmation_number"`
	Booked              bool   `json:"booked"`
	Notes               string `json:"notes"`
	CalendarEventID     *int64 `json:"calendar_event_id,omitempty"`
	CreatedAt           string `json:"created_at"`
	UpdatedAt           string `json:"updated_at"`
}

type Activity struct {
	ID              int64  `json:"id"`
	TripID          int64  `json:"trip_id"`
	DestinationID   int64  `json:"destination_id,omitempty"`
	Name            string `json:"name"`
	Category        string `json:"category"`
	StartAt         string `json:"start_at,omitempty"`
	EndAt           string `json:"end_at,omitempty"`
	Location        string `json:"location"`
	CostEstimated   *int64 `json:"cost_estimated,omitempty"`
	CostActual      *int64 `json:"cost_actual,omitempty"`
	Currency        string `json:"currency"`
	Booked          bool   `json:"booked"`
	Notes           string `json:"notes"`
	CalendarEventID *int64 `json:"calendar_event_id,omitempty"`
	CreatedAt       string `json:"created_at"`
	UpdatedAt       string `json:"updated_at"`
}

type Todo struct {
	ID        int64  `json:"id"`
	TripID    int64  `json:"trip_id"`
	Label     string `json:"label"`
	DueAt     string `json:"due_at,omitempty"`
	Done      bool   `json:"done"`
	DoneAt    string `json:"done_at,omitempty"`
	CreatedAt string `json:"created_at"`
}

type TripBudget struct {
	ID       int64  `json:"id"`
	TripID   int64  `json:"trip_id"`
	Category string `json:"category"`
	Amount   int64  `json:"amount"`
	Notes    string `json:"notes"`
}

// BudgetCategoryRow is one row in budget_summary's `categories` slice.
type BudgetCategoryRow struct {
	Category string `json:"category"`
	Cap      int64  `json:"cap"`
	Capped   bool   `json:"capped"`
	Planned  int64  `json:"planned"` // sum of cost_estimated on items in this bucket
	Actual   int64  `json:"actual"`  // sum of cost_actual on items in this bucket
	Delta    int64  `json:"delta"`   // cap - actual when capped; else planned - actual
}

type BudgetSummary struct {
	HomeCurrency string              `json:"home_currency"`
	Categories   []BudgetCategoryRow `json:"categories"`
	TotalPlanned int64               `json:"total_planned"`
	TotalActual  int64               `json:"total_actual"`
	TotalCap     int64               `json:"total_cap"`
}

// TripDashboard is the dashboard tool's combined payload.
type TripDashboard struct {
	Trip           Trip            `json:"trip"`
	Destinations   []Destination   `json:"destinations"`
	TransportLegs  []TransportLeg  `json:"transport_legs"`
	Accommodations []Accommodation `json:"accommodations"`
	Activities     []Activity      `json:"activities"`
	Todos          []Todo          `json:"todos"`
	Budget         BudgetSummary   `json:"budget"`
}

// ─── Trips ───────────────────────────────────────────────────────

func (a *App) toolTripsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid := projectID()
	includeArchived, _ := args["include_archived"].(bool)
	q := `SELECT id, project_id, name, purpose, status, start_at, end_at,
	             home_currency, total_budget, participants, notes, color,
	             calendar_id, sync_calendar, archived, created_at, updated_at
	      FROM trips WHERE project_id=?`
	if !includeArchived {
		q += " AND archived=0"
	}
	q += " ORDER BY start_at DESC"
	rows, err := ctx.AppDB().Query(q, pid)
	if err != nil {
		return nil, err
	}
	bare := []Trip{}
	for rows.Next() {
		t, err := scanTrip(rows)
		if err != nil {
			continue
		}
		bare = append(bare, t)
	}
	rows.Close()
	// Compute per-trip budget aggregates after closing the cursor —
	// testkit caps connections at 1 and computeBudgetSummary issues
	// its own queries.
	out := make([]Trip, 0, len(bare))
	for _, t := range bare {
		s, err := computeBudgetSummary(ctx, t.ID)
		if err == nil {
			planned, actual := s.TotalPlanned, s.TotalActual
			t.TotalPlanned = &planned
			t.TotalActual = &actual
		}
		out = append(out, t)
	}
	return map[string]any{"trips": out}, nil
}

func (a *App) toolTripsGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64(intArg(args, "id", 0))
	if id == 0 {
		return nil, errors.New("id required")
	}
	return readTrip(ctx, id)
}

func (a *App) toolTripsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	name := strings.TrimSpace(strArg(args, "name", ""))
	startAt := strArg(args, "start_at", "")
	endAt := strArg(args, "end_at", "")
	if name == "" || startAt == "" || endAt == "" {
		return nil, errors.New("name, start_at, end_at required")
	}
	startT, err := parseFlexibleTime(startAt)
	if err != nil {
		return nil, fmt.Errorf("start_at: %w", err)
	}
	endT, err := parseFlexibleTime(endAt)
	if err != nil {
		return nil, fmt.Errorf("end_at: %w", err)
	}
	if !endT.After(startT) {
		return nil, errors.New("end_at must be after start_at")
	}
	homeCcy := strings.ToUpper(strArg(args, "home_currency", "EUR"))
	color := strArg(args, "color", "#3b82f6")
	purpose := strArg(args, "purpose", "")
	notes := strArg(args, "notes", "")
	sync := true
	if v, ok := args["sync_calendar"].(bool); ok {
		sync = v
	}
	pid := projectID()
	res, err := ctx.AppDB().Exec(
		`INSERT INTO trips (project_id, name, purpose, start_at, end_at, home_currency,
		                    color, notes, sync_calendar)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, name, purpose, startT.UTC().Format(time.RFC3339), endT.UTC().Format(time.RFC3339),
		homeCcy, color, notes, boolToInt(sync),
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()

	// Calendar mirror: create the trip's dedicated calendar. Best-
	// effort — if the call fails, the trip lives on without sync;
	// the user can heal via trips_update.sync_calendar later.
	if sync {
		if calID, err := callCreateCalendar(ctx, "Trip: "+name, color); err == nil {
			_, _ = ctx.AppDB().Exec(`UPDATE trips SET calendar_id=? WHERE id=?`, calID, id)
		} else {
			ctx.Logger().Warn("calendar mirror create failed; trip created without calendar", "err", err)
		}
	}
	ctx.Emit("trip.created", map[string]any{"trip_id": id, "name": name})
	return readTrip(ctx, id)
}

func (a *App) toolTripsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64(intArg(args, "id", 0))
	if id == 0 {
		return nil, errors.New("id required")
	}
	old, err := readTrip(ctx, id)
	if err != nil {
		return nil, err
	}
	cols, vals := []string{}, []any{}
	for _, k := range []string{"name", "start_at", "end_at", "status", "color", "notes", "purpose"} {
		if v, ok := args[k].(string); ok && v != "" {
			cols = append(cols, k+"=?")
			vals = append(vals, v)
		}
	}
	if v, ok := args["archived"].(bool); ok {
		cols = append(cols, "archived=?")
		vals = append(vals, boolToInt(v))
	}
	if v, ok := args["total_budget"]; ok {
		cols = append(cols, "total_budget=?")
		vals = append(vals, int64(intArgFromAny(v, 0)))
	}
	syncChanged := false
	newSync := old.SyncCalendar
	if v, ok := args["sync_calendar"].(bool); ok {
		if v != old.SyncCalendar {
			syncChanged = true
			newSync = v
		}
		cols = append(cols, "sync_calendar=?")
		vals = append(vals, boolToInt(v))
	}
	if len(cols) == 0 {
		return nil, errors.New("no updatable fields supplied")
	}
	cols = append(cols, "updated_at=CURRENT_TIMESTAMP")
	vals = append(vals, id)
	if _, err := ctx.AppDB().Exec(
		`UPDATE trips SET `+strings.Join(cols, ", ")+` WHERE id=?`, vals...,
	); err != nil {
		return nil, err
	}

	updated, _ := readTrip(ctx, id)

	// Reflect rename into the linked calendar.
	if name, ok := args["name"].(string); ok && name != "" && updated.CalendarID != nil && newSync {
		_ = callUpdateCalendar(ctx, *updated.CalendarID, "Trip: "+name)
	}

	// Toggle sync_calendar — retroactive event create/delete.
	if syncChanged {
		if newSync {
			// Off → On: ensure calendar exists, then create events for
			// every item that doesn't have one yet.
			if updated.CalendarID == nil {
				if calID, err := callCreateCalendar(ctx, "Trip: "+updated.Name, updated.Color); err == nil {
					_, _ = ctx.AppDB().Exec(`UPDATE trips SET calendar_id=? WHERE id=?`, calID, id)
					updated.CalendarID = &calID
				} else {
					ctx.Logger().Warn("re-creating trip calendar failed", "err", err)
				}
			}
			if updated.CalendarID != nil {
				rehydrateCalendarForTrip(ctx, updated)
			}
		} else {
			// On → Off: delete all linked events, keep the calendar shell.
			pruneCalendarEventsForTrip(ctx, id)
		}
	}
	ctx.Emit("trip.updated", map[string]any{"trip_id": id})
	return updated, nil
}

func (a *App) toolTripsDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64(intArg(args, "id", 0))
	if id == 0 {
		return nil, errors.New("id required")
	}
	t, err := readTrip(ctx, id)
	if err != nil {
		return nil, err
	}
	// Best-effort: drop the trip's calendar (events cascade in calendar).
	if t.CalendarID != nil {
		_ = callDeleteCalendar(ctx, *t.CalendarID)
	}
	if _, err := ctx.AppDB().Exec(`DELETE FROM trips WHERE id=?`, id); err != nil {
		return nil, err
	}
	ctx.Emit("trip.deleted", map[string]any{"trip_id": id})
	return map[string]any{"deleted": id}, nil
}

func scanTrip(r rowScanner) (Trip, error) {
	var t Trip
	var sync, arch int
	var totalBudget sql.NullInt64
	var calendarID sql.NullInt64
	var participants string
	if err := r.Scan(&t.ID, &t.ProjectID, &t.Name, &t.Purpose, &t.Status,
		&t.StartAt, &t.EndAt, &t.HomeCurrency, &totalBudget, &participants,
		&t.Notes, &t.Color, &calendarID, &sync, &arch,
		&t.CreatedAt, &t.UpdatedAt); err != nil {
		return t, err
	}
	if totalBudget.Valid {
		v := totalBudget.Int64
		t.TotalBudget = &v
	}
	if calendarID.Valid {
		v := calendarID.Int64
		t.CalendarID = &v
	}
	t.SyncCalendar = sync == 1
	t.Archived = arch == 1
	_ = json.Unmarshal([]byte(participants), &t.Participants)
	return t, nil
}

func readTrip(ctx *sdk.AppCtx, id int64) (Trip, error) {
	row := ctx.AppDB().QueryRow(
		`SELECT id, project_id, name, purpose, status, start_at, end_at,
		        home_currency, total_budget, participants, notes, color,
		        calendar_id, sync_calendar, archived, created_at, updated_at
		 FROM trips WHERE id=?`, id,
	)
	return scanTrip(row)
}

type rowScanner interface{ Scan(...any) error }

// ─── Destinations ────────────────────────────────────────────────

func (a *App) toolDestinationsAdd(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	tripID := int64(intArg(args, "trip_id", 0))
	name := strings.TrimSpace(strArg(args, "place_name", ""))
	arriveAt := strArg(args, "arrive_at", "")
	departAt := strArg(args, "depart_at", "")
	if tripID == 0 || name == "" || arriveAt == "" || departAt == "" {
		return nil, errors.New("trip_id, place_name, arrive_at, depart_at required")
	}
	if _, err := readTrip(ctx, tripID); err != nil {
		return nil, fmt.Errorf("trip %d not found", tripID)
	}
	// Append at end: next order_idx is (max(order_idx) + 1).
	var nextIdx int
	_ = ctx.AppDB().QueryRow(
		`SELECT COALESCE(MAX(order_idx), -1) + 1 FROM destinations WHERE trip_id=?`, tripID,
	).Scan(&nextIdx)
	country := strArg(args, "country", "")
	notes := strArg(args, "notes", "")
	lat, latOK := args["lat"].(float64)
	lng, lngOK := args["lng"].(float64)
	var latV, lngV any
	if latOK {
		latV = lat
	}
	if lngOK {
		lngV = lng
	}
	res, err := ctx.AppDB().Exec(
		`INSERT INTO destinations (trip_id, place_name, country, lat, lng, arrive_at, depart_at, order_idx, notes)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		tripID, name, country, latV, lngV, arriveAt, departAt, nextIdx, notes,
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	ctx.Emit("destination.added", map[string]any{"trip_id": tripID, "destination_id": id})
	return readDestination(ctx, id)
}

func (a *App) toolDestinationsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64(intArg(args, "id", 0))
	if id == 0 {
		return nil, errors.New("id required")
	}
	cols, vals := []string{}, []any{}
	for _, k := range []string{"place_name", "country", "arrive_at", "depart_at", "notes"} {
		if v, ok := args[k].(string); ok && v != "" {
			cols = append(cols, k+"=?")
			vals = append(vals, v)
		}
	}
	if v, ok := args["lat"].(float64); ok {
		cols = append(cols, "lat=?")
		vals = append(vals, v)
	}
	if v, ok := args["lng"].(float64); ok {
		cols = append(cols, "lng=?")
		vals = append(vals, v)
	}
	if len(cols) == 0 {
		return nil, errors.New("no updatable fields supplied")
	}
	vals = append(vals, id)
	if _, err := ctx.AppDB().Exec(
		`UPDATE destinations SET `+strings.Join(cols, ", ")+` WHERE id=?`, vals...,
	); err != nil {
		return nil, err
	}
	return readDestination(ctx, id)
}

func (a *App) toolDestinationsDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64(intArg(args, "id", 0))
	if id == 0 {
		return nil, errors.New("id required")
	}
	if _, err := ctx.AppDB().Exec(`DELETE FROM destinations WHERE id=?`, id); err != nil {
		return nil, err
	}
	return map[string]any{"deleted": id}, nil
}

func (a *App) toolDestinationsReorder(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	tripID := int64(intArg(args, "trip_id", 0))
	order, ok := args["order"].([]any)
	if tripID == 0 || !ok {
		return nil, errors.New("trip_id and order (array of ids) required")
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for i, v := range order {
		destID := int64(intArgFromAny(v, 0))
		if destID == 0 {
			continue
		}
		if _, err := tx.Exec(
			`UPDATE destinations SET order_idx=? WHERE id=? AND trip_id=?`,
			i, destID, tripID,
		); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return map[string]any{"reordered": len(order)}, nil
}

func readDestination(ctx *sdk.AppCtx, id int64) (Destination, error) {
	row := ctx.AppDB().QueryRow(
		`SELECT id, trip_id, place_name, country, lat, lng, arrive_at, depart_at, order_idx, notes, created_at
		 FROM destinations WHERE id=?`, id,
	)
	return scanDestination(row)
}

func scanDestination(r rowScanner) (Destination, error) {
	var d Destination
	var lat, lng sql.NullFloat64
	if err := r.Scan(&d.ID, &d.TripID, &d.PlaceName, &d.Country, &lat, &lng,
		&d.ArriveAt, &d.DepartAt, &d.OrderIdx, &d.Notes, &d.CreatedAt); err != nil {
		return d, err
	}
	if lat.Valid {
		v := lat.Float64
		d.Lat = &v
	}
	if lng.Valid {
		v := lng.Float64
		d.Lng = &v
	}
	return d, nil
}

func listDestinationsByTrip(ctx *sdk.AppCtx, tripID int64) ([]Destination, error) {
	rows, err := ctx.AppDB().Query(
		`SELECT id, trip_id, place_name, country, lat, lng, arrive_at, depart_at, order_idx, notes, created_at
		 FROM destinations WHERE trip_id=? ORDER BY order_idx, id`, tripID,
	)
	if err != nil {
		return nil, err
	}
	out := []Destination{}
	for rows.Next() {
		d, err := scanDestination(rows)
		if err != nil {
			continue
		}
		out = append(out, d)
	}
	rows.Close()
	return out, nil
}

// ─── Transport ───────────────────────────────────────────────────

func (a *App) toolTransportLegsAdd(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	tripID := int64(intArg(args, "trip_id", 0))
	if tripID == 0 {
		return nil, errors.New("trip_id required")
	}
	trip, err := readTrip(ctx, tripID)
	if err != nil {
		return nil, fmt.Errorf("trip %d not found", tripID)
	}
	kind := strArg(args, "kind", "")
	if !contains(transportKinds(), kind) {
		return nil, fmt.Errorf("kind must be one of %v", transportKinds())
	}
	depart := strArg(args, "depart_at", "")
	arrive := strArg(args, "arrive_at", "")
	if depart == "" || arrive == "" {
		return nil, errors.New("depart_at and arrive_at required")
	}
	currency := strings.ToUpper(strArg(args, "currency", trip.HomeCurrency))
	provider := strArg(args, "provider", "")
	reference := strArg(args, "reference", "")
	departLoc := strArg(args, "depart_location", "")
	arriveLoc := strArg(args, "arrive_location", "")
	confirm := strArg(args, "confirmation_number", "")
	notes := strArg(args, "notes", "")
	res, err := ctx.AppDB().Exec(
		`INSERT INTO transport_legs (trip_id, from_destination_id, to_destination_id, kind,
		   provider, reference, depart_at, arrive_at, depart_location, arrive_location,
		   cost_estimated, cost_actual, currency, confirmation_number, notes)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		tripID,
		nullableInt64(args, "from_destination_id"),
		nullableInt64(args, "to_destination_id"),
		kind, provider, reference, depart, arrive, departLoc, arriveLoc,
		nullableInt64(args, "cost_estimated"),
		nullableInt64(args, "cost_actual"),
		currency, confirm, notes,
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	leg, _ := readTransport(ctx, id)
	// Calendar mirror.
	if trip.SyncCalendar && trip.CalendarID != nil {
		if evtID, err := callCreateEvent(ctx, *trip.CalendarID,
			transportEventTitle(leg), leg.DepartAt, leg.ArriveAt, false,
			leg.DepartLocation, transportEventDescription(trip, leg)); err == nil {
			_, _ = ctx.AppDB().Exec(`UPDATE transport_legs SET calendar_event_id=? WHERE id=?`, evtID, id)
			leg.CalendarEventID = &evtID
		} else {
			ctx.Logger().Warn("transport calendar mirror failed", "err", err)
		}
	}
	ctx.Emit("transport.added", map[string]any{"trip_id": tripID, "leg_id": id})
	return leg, nil
}

func (a *App) toolTransportLegsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64(intArg(args, "id", 0))
	if id == 0 {
		return nil, errors.New("id required")
	}
	old, err := readTransport(ctx, id)
	if err != nil {
		return nil, err
	}
	cols, vals := []string{}, []any{}
	for _, k := range []string{"kind", "provider", "reference", "depart_at", "arrive_at",
		"depart_location", "arrive_location", "currency", "confirmation_number", "notes"} {
		if v, ok := args[k].(string); ok && v != "" {
			cols = append(cols, k+"=?")
			vals = append(vals, v)
		}
	}
	for _, k := range []string{"from_destination_id", "to_destination_id", "cost_estimated", "cost_actual"} {
		if v, ok := args[k]; ok && v != nil {
			cols = append(cols, k+"=?")
			vals = append(vals, int64(intArgFromAny(v, 0)))
		}
	}
	if len(cols) == 0 {
		return nil, errors.New("no updatable fields supplied")
	}
	cols = append(cols, "updated_at=CURRENT_TIMESTAMP")
	vals = append(vals, id)
	if _, err := ctx.AppDB().Exec(
		`UPDATE transport_legs SET `+strings.Join(cols, ", ")+` WHERE id=?`, vals...,
	); err != nil {
		return nil, err
	}
	leg, _ := readTransport(ctx, id)
	trip, _ := readTrip(ctx, leg.TripID)
	// Calendar upsert.
	if trip.SyncCalendar && trip.CalendarID != nil {
		if old.CalendarEventID != nil {
			_ = callUpdateEvent(ctx, *old.CalendarEventID,
				transportEventTitle(leg), leg.DepartAt, leg.ArriveAt, false,
				leg.DepartLocation, transportEventDescription(trip, leg))
		} else if evtID, err := callCreateEvent(ctx, *trip.CalendarID,
			transportEventTitle(leg), leg.DepartAt, leg.ArriveAt, false,
			leg.DepartLocation, transportEventDescription(trip, leg)); err == nil {
			_, _ = ctx.AppDB().Exec(`UPDATE transport_legs SET calendar_event_id=? WHERE id=?`, evtID, id)
			leg.CalendarEventID = &evtID
		}
	}
	return leg, nil
}

func (a *App) toolTransportLegsDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64(intArg(args, "id", 0))
	if id == 0 {
		return nil, errors.New("id required")
	}
	leg, err := readTransport(ctx, id)
	if err != nil {
		return nil, err
	}
	if leg.CalendarEventID != nil {
		_ = callDeleteEvent(ctx, *leg.CalendarEventID)
	}
	if _, err := ctx.AppDB().Exec(`DELETE FROM transport_legs WHERE id=?`, id); err != nil {
		return nil, err
	}
	return map[string]any{"deleted": id}, nil
}

func (a *App) toolTransportLegsMarkBooked(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64(intArg(args, "id", 0))
	if id == 0 {
		return nil, errors.New("id required")
	}
	cols := []string{"booked=1"}
	vals := []any{}
	if v, ok := args["cost_actual"]; ok && v != nil {
		cols = append(cols, "cost_actual=?")
		vals = append(vals, int64(intArgFromAny(v, 0)))
	}
	if v, ok := args["confirmation_number"].(string); ok && v != "" {
		cols = append(cols, "confirmation_number=?")
		vals = append(vals, v)
	}
	cols = append(cols, "updated_at=CURRENT_TIMESTAMP")
	vals = append(vals, id)
	if _, err := ctx.AppDB().Exec(
		`UPDATE transport_legs SET `+strings.Join(cols, ", ")+` WHERE id=?`, vals...,
	); err != nil {
		return nil, err
	}
	return readTransport(ctx, id)
}

func readTransport(ctx *sdk.AppCtx, id int64) (TransportLeg, error) {
	row := ctx.AppDB().QueryRow(
		`SELECT id, trip_id, COALESCE(from_destination_id,0), COALESCE(to_destination_id,0),
		        kind, provider, reference, depart_at, arrive_at, depart_location, arrive_location,
		        cost_estimated, cost_actual, currency, confirmation_number, booked, notes,
		        calendar_event_id, created_at, updated_at
		 FROM transport_legs WHERE id=?`, id,
	)
	return scanTransport(row)
}

func scanTransport(r rowScanner) (TransportLeg, error) {
	var l TransportLeg
	var booked int
	var estCost, actCost, evtID sql.NullInt64
	if err := r.Scan(&l.ID, &l.TripID, &l.FromDestinationID, &l.ToDestinationID,
		&l.Kind, &l.Provider, &l.Reference, &l.DepartAt, &l.ArriveAt,
		&l.DepartLocation, &l.ArriveLocation, &estCost, &actCost,
		&l.Currency, &l.ConfirmationNumber, &booked, &l.Notes,
		&evtID, &l.CreatedAt, &l.UpdatedAt); err != nil {
		return l, err
	}
	if estCost.Valid {
		v := estCost.Int64
		l.CostEstimated = &v
	}
	if actCost.Valid {
		v := actCost.Int64
		l.CostActual = &v
	}
	if evtID.Valid {
		v := evtID.Int64
		l.CalendarEventID = &v
	}
	l.Booked = booked == 1
	return l, nil
}

func listTransportByTrip(ctx *sdk.AppCtx, tripID int64) ([]TransportLeg, error) {
	rows, err := ctx.AppDB().Query(
		`SELECT id, trip_id, COALESCE(from_destination_id,0), COALESCE(to_destination_id,0),
		        kind, provider, reference, depart_at, arrive_at, depart_location, arrive_location,
		        cost_estimated, cost_actual, currency, confirmation_number, booked, notes,
		        calendar_event_id, created_at, updated_at
		 FROM transport_legs WHERE trip_id=? ORDER BY depart_at, id`, tripID,
	)
	if err != nil {
		return nil, err
	}
	out := []TransportLeg{}
	for rows.Next() {
		l, err := scanTransport(rows)
		if err != nil {
			continue
		}
		out = append(out, l)
	}
	rows.Close()
	return out, nil
}

func transportEventTitle(l TransportLeg) string {
	// No emojis (per house rules). Compact form: "AF1234 CDG → LIN".
	parts := []string{}
	if l.Provider != "" {
		parts = append(parts, l.Provider)
	}
	if l.Reference != "" {
		parts = append(parts, l.Reference)
	}
	if l.DepartLocation != "" || l.ArriveLocation != "" {
		parts = append(parts, l.DepartLocation+" → "+l.ArriveLocation)
	}
	if len(parts) == 0 {
		parts = append(parts, strings.Title(l.Kind))
	}
	return strings.Join(parts, " ")
}

func transportEventDescription(t Trip, l TransportLeg) string {
	return fmt.Sprintf("Trip: %s\nLeg id: %d\n%s", t.Name, l.ID, l.Notes)
}

// ─── Accommodations ──────────────────────────────────────────────

func (a *App) toolAccommodationsAdd(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	tripID := int64(intArg(args, "trip_id", 0))
	if tripID == 0 {
		return nil, errors.New("trip_id required")
	}
	trip, err := readTrip(ctx, tripID)
	if err != nil {
		return nil, fmt.Errorf("trip %d not found", tripID)
	}
	name := strings.TrimSpace(strArg(args, "name", ""))
	checkIn := strArg(args, "check_in_at", "")
	checkOut := strArg(args, "check_out_at", "")
	if name == "" || checkIn == "" || checkOut == "" {
		return nil, errors.New("name, check_in_at, check_out_at required")
	}
	kind := strArg(args, "kind", "hotel")
	if !contains(accommodationKinds(), kind) {
		return nil, fmt.Errorf("kind must be one of %v", accommodationKinds())
	}
	currency := strings.ToUpper(strArg(args, "currency", trip.HomeCurrency))
	address := strArg(args, "address", "")
	confirm := strArg(args, "confirmation_number", "")
	notes := strArg(args, "notes", "")
	res, err := ctx.AppDB().Exec(
		`INSERT INTO accommodations (trip_id, destination_id, name, kind, address, check_in_at,
		   check_out_at, cost_estimated, cost_actual, currency, confirmation_number, notes)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		tripID,
		nullableInt64(args, "destination_id"),
		name, kind, address, checkIn, checkOut,
		nullableInt64(args, "cost_estimated"),
		nullableInt64(args, "cost_actual"),
		currency, confirm, notes,
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	acc, _ := readAccommodation(ctx, id)
	if trip.SyncCalendar && trip.CalendarID != nil {
		if evtID, err := callCreateEvent(ctx, *trip.CalendarID,
			"Stay: "+acc.Name, acc.CheckInAt, acc.CheckOutAt, true,
			acc.Address, fmt.Sprintf("Trip: %s\nAccommodation id: %d\n%s", trip.Name, acc.ID, acc.Notes)); err == nil {
			_, _ = ctx.AppDB().Exec(`UPDATE accommodations SET calendar_event_id=? WHERE id=?`, evtID, id)
			acc.CalendarEventID = &evtID
		} else {
			ctx.Logger().Warn("accommodation calendar mirror failed", "err", err)
		}
	}
	ctx.Emit("accommodation.added", map[string]any{"trip_id": tripID, "accommodation_id": id})
	return acc, nil
}

func (a *App) toolAccommodationsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64(intArg(args, "id", 0))
	if id == 0 {
		return nil, errors.New("id required")
	}
	old, err := readAccommodation(ctx, id)
	if err != nil {
		return nil, err
	}
	cols, vals := []string{}, []any{}
	for _, k := range []string{"name", "kind", "address", "check_in_at", "check_out_at",
		"currency", "confirmation_number", "notes"} {
		if v, ok := args[k].(string); ok && v != "" {
			cols = append(cols, k+"=?")
			vals = append(vals, v)
		}
	}
	for _, k := range []string{"destination_id", "cost_estimated", "cost_actual"} {
		if v, ok := args[k]; ok && v != nil {
			cols = append(cols, k+"=?")
			vals = append(vals, int64(intArgFromAny(v, 0)))
		}
	}
	if len(cols) == 0 {
		return nil, errors.New("no updatable fields supplied")
	}
	cols = append(cols, "updated_at=CURRENT_TIMESTAMP")
	vals = append(vals, id)
	if _, err := ctx.AppDB().Exec(
		`UPDATE accommodations SET `+strings.Join(cols, ", ")+` WHERE id=?`, vals...,
	); err != nil {
		return nil, err
	}
	acc, _ := readAccommodation(ctx, id)
	trip, _ := readTrip(ctx, acc.TripID)
	if trip.SyncCalendar && trip.CalendarID != nil {
		if old.CalendarEventID != nil {
			_ = callUpdateEvent(ctx, *old.CalendarEventID,
				"Stay: "+acc.Name, acc.CheckInAt, acc.CheckOutAt, true,
				acc.Address, fmt.Sprintf("Trip: %s\nAccommodation id: %d\n%s", trip.Name, acc.ID, acc.Notes))
		} else if evtID, err := callCreateEvent(ctx, *trip.CalendarID,
			"Stay: "+acc.Name, acc.CheckInAt, acc.CheckOutAt, true,
			acc.Address, fmt.Sprintf("Trip: %s\nAccommodation id: %d\n%s", trip.Name, acc.ID, acc.Notes)); err == nil {
			_, _ = ctx.AppDB().Exec(`UPDATE accommodations SET calendar_event_id=? WHERE id=?`, evtID, id)
			acc.CalendarEventID = &evtID
		}
	}
	return acc, nil
}

func (a *App) toolAccommodationsDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64(intArg(args, "id", 0))
	if id == 0 {
		return nil, errors.New("id required")
	}
	acc, err := readAccommodation(ctx, id)
	if err != nil {
		return nil, err
	}
	if acc.CalendarEventID != nil {
		_ = callDeleteEvent(ctx, *acc.CalendarEventID)
	}
	if _, err := ctx.AppDB().Exec(`DELETE FROM accommodations WHERE id=?`, id); err != nil {
		return nil, err
	}
	return map[string]any{"deleted": id}, nil
}

func (a *App) toolAccommodationsMarkBooked(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64(intArg(args, "id", 0))
	if id == 0 {
		return nil, errors.New("id required")
	}
	cols := []string{"booked=1"}
	vals := []any{}
	if v, ok := args["cost_actual"]; ok && v != nil {
		cols = append(cols, "cost_actual=?")
		vals = append(vals, int64(intArgFromAny(v, 0)))
	}
	if v, ok := args["confirmation_number"].(string); ok && v != "" {
		cols = append(cols, "confirmation_number=?")
		vals = append(vals, v)
	}
	cols = append(cols, "updated_at=CURRENT_TIMESTAMP")
	vals = append(vals, id)
	if _, err := ctx.AppDB().Exec(
		`UPDATE accommodations SET `+strings.Join(cols, ", ")+` WHERE id=?`, vals...,
	); err != nil {
		return nil, err
	}
	return readAccommodation(ctx, id)
}

func readAccommodation(ctx *sdk.AppCtx, id int64) (Accommodation, error) {
	row := ctx.AppDB().QueryRow(
		`SELECT id, trip_id, COALESCE(destination_id,0), name, kind, address, check_in_at,
		        check_out_at, cost_estimated, cost_actual, currency, confirmation_number,
		        booked, notes, calendar_event_id, created_at, updated_at
		 FROM accommodations WHERE id=?`, id,
	)
	return scanAccommodation(row)
}

func scanAccommodation(r rowScanner) (Accommodation, error) {
	var a Accommodation
	var booked int
	var estCost, actCost, evtID sql.NullInt64
	if err := r.Scan(&a.ID, &a.TripID, &a.DestinationID, &a.Name, &a.Kind, &a.Address,
		&a.CheckInAt, &a.CheckOutAt, &estCost, &actCost, &a.Currency,
		&a.ConfirmationNumber, &booked, &a.Notes, &evtID,
		&a.CreatedAt, &a.UpdatedAt); err != nil {
		return a, err
	}
	if estCost.Valid {
		v := estCost.Int64
		a.CostEstimated = &v
	}
	if actCost.Valid {
		v := actCost.Int64
		a.CostActual = &v
	}
	if evtID.Valid {
		v := evtID.Int64
		a.CalendarEventID = &v
	}
	a.Booked = booked == 1
	return a, nil
}

func listAccommodationsByTrip(ctx *sdk.AppCtx, tripID int64) ([]Accommodation, error) {
	rows, err := ctx.AppDB().Query(
		`SELECT id, trip_id, COALESCE(destination_id,0), name, kind, address, check_in_at,
		        check_out_at, cost_estimated, cost_actual, currency, confirmation_number,
		        booked, notes, calendar_event_id, created_at, updated_at
		 FROM accommodations WHERE trip_id=? ORDER BY check_in_at, id`, tripID,
	)
	if err != nil {
		return nil, err
	}
	out := []Accommodation{}
	for rows.Next() {
		a, err := scanAccommodation(rows)
		if err != nil {
			continue
		}
		out = append(out, a)
	}
	rows.Close()
	return out, nil
}

// ─── Activities ──────────────────────────────────────────────────

func (a *App) toolActivitiesAdd(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	tripID := int64(intArg(args, "trip_id", 0))
	if tripID == 0 {
		return nil, errors.New("trip_id required")
	}
	trip, err := readTrip(ctx, tripID)
	if err != nil {
		return nil, fmt.Errorf("trip %d not found", tripID)
	}
	name := strings.TrimSpace(strArg(args, "name", ""))
	if name == "" {
		return nil, errors.New("name required")
	}
	category := strArg(args, "category", "activity")
	if !contains(activityCategories(), category) {
		return nil, fmt.Errorf("category must be one of %v", activityCategories())
	}
	currency := strings.ToUpper(strArg(args, "currency", trip.HomeCurrency))
	startAt := strArg(args, "start_at", "")
	endAt := strArg(args, "end_at", "")
	location := strArg(args, "location", "")
	notes := strArg(args, "notes", "")
	res, err := ctx.AppDB().Exec(
		`INSERT INTO activities (trip_id, destination_id, name, category, start_at, end_at,
		   location, cost_estimated, cost_actual, currency, notes)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		tripID,
		nullableInt64(args, "destination_id"),
		name, category,
		nullIfEmpty(startAt), nullIfEmpty(endAt),
		location,
		nullableInt64(args, "cost_estimated"),
		nullableInt64(args, "cost_actual"),
		currency, notes,
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	act, _ := readActivity(ctx, id)
	if trip.SyncCalendar && trip.CalendarID != nil && act.StartAt != "" {
		endForEvent := act.EndAt
		if endForEvent == "" {
			endForEvent = act.StartAt
		}
		if evtID, err := callCreateEvent(ctx, *trip.CalendarID,
			act.Name, act.StartAt, endForEvent, false,
			act.Location, fmt.Sprintf("Trip: %s\nActivity id: %d\n%s", trip.Name, act.ID, act.Notes)); err == nil {
			_, _ = ctx.AppDB().Exec(`UPDATE activities SET calendar_event_id=? WHERE id=?`, evtID, id)
			act.CalendarEventID = &evtID
		}
	}
	ctx.Emit("activity.added", map[string]any{"trip_id": tripID, "activity_id": id})
	return act, nil
}

func (a *App) toolActivitiesUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64(intArg(args, "id", 0))
	if id == 0 {
		return nil, errors.New("id required")
	}
	old, err := readActivity(ctx, id)
	if err != nil {
		return nil, err
	}
	cols, vals := []string{}, []any{}
	for _, k := range []string{"name", "category", "start_at", "end_at", "location", "currency", "notes"} {
		if v, ok := args[k].(string); ok && v != "" {
			cols = append(cols, k+"=?")
			vals = append(vals, v)
		}
	}
	for _, k := range []string{"destination_id", "cost_estimated", "cost_actual"} {
		if v, ok := args[k]; ok && v != nil {
			cols = append(cols, k+"=?")
			vals = append(vals, int64(intArgFromAny(v, 0)))
		}
	}
	if len(cols) == 0 {
		return nil, errors.New("no updatable fields supplied")
	}
	cols = append(cols, "updated_at=CURRENT_TIMESTAMP")
	vals = append(vals, id)
	if _, err := ctx.AppDB().Exec(
		`UPDATE activities SET `+strings.Join(cols, ", ")+` WHERE id=?`, vals...,
	); err != nil {
		return nil, err
	}
	act, _ := readActivity(ctx, id)
	trip, _ := readTrip(ctx, act.TripID)
	if trip.SyncCalendar && trip.CalendarID != nil {
		endForEvent := act.EndAt
		if endForEvent == "" {
			endForEvent = act.StartAt
		}
		if act.StartAt != "" {
			if old.CalendarEventID != nil {
				_ = callUpdateEvent(ctx, *old.CalendarEventID,
					act.Name, act.StartAt, endForEvent, false,
					act.Location, fmt.Sprintf("Trip: %s\nActivity id: %d\n%s", trip.Name, act.ID, act.Notes))
			} else if evtID, err := callCreateEvent(ctx, *trip.CalendarID,
				act.Name, act.StartAt, endForEvent, false,
				act.Location, fmt.Sprintf("Trip: %s\nActivity id: %d\n%s", trip.Name, act.ID, act.Notes)); err == nil {
				_, _ = ctx.AppDB().Exec(`UPDATE activities SET calendar_event_id=? WHERE id=?`, evtID, id)
				act.CalendarEventID = &evtID
			}
		} else if old.CalendarEventID != nil {
			// start_at cleared → drop the calendar event.
			_ = callDeleteEvent(ctx, *old.CalendarEventID)
			_, _ = ctx.AppDB().Exec(`UPDATE activities SET calendar_event_id=NULL WHERE id=?`, id)
			act.CalendarEventID = nil
		}
	}
	return act, nil
}

func (a *App) toolActivitiesDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64(intArg(args, "id", 0))
	if id == 0 {
		return nil, errors.New("id required")
	}
	act, err := readActivity(ctx, id)
	if err != nil {
		return nil, err
	}
	if act.CalendarEventID != nil {
		_ = callDeleteEvent(ctx, *act.CalendarEventID)
	}
	if _, err := ctx.AppDB().Exec(`DELETE FROM activities WHERE id=?`, id); err != nil {
		return nil, err
	}
	return map[string]any{"deleted": id}, nil
}

func (a *App) toolActivitiesMarkBooked(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64(intArg(args, "id", 0))
	if id == 0 {
		return nil, errors.New("id required")
	}
	cols := []string{"booked=1"}
	vals := []any{}
	if v, ok := args["cost_actual"]; ok && v != nil {
		cols = append(cols, "cost_actual=?")
		vals = append(vals, int64(intArgFromAny(v, 0)))
	}
	cols = append(cols, "updated_at=CURRENT_TIMESTAMP")
	vals = append(vals, id)
	if _, err := ctx.AppDB().Exec(
		`UPDATE activities SET `+strings.Join(cols, ", ")+` WHERE id=?`, vals...,
	); err != nil {
		return nil, err
	}
	return readActivity(ctx, id)
}

func readActivity(ctx *sdk.AppCtx, id int64) (Activity, error) {
	row := ctx.AppDB().QueryRow(
		`SELECT id, trip_id, COALESCE(destination_id,0), name, category,
		        COALESCE(start_at,''), COALESCE(end_at,''), location,
		        cost_estimated, cost_actual, currency, booked, notes,
		        calendar_event_id, created_at, updated_at
		 FROM activities WHERE id=?`, id,
	)
	return scanActivity(row)
}

func scanActivity(r rowScanner) (Activity, error) {
	var a Activity
	var booked int
	var estCost, actCost, evtID sql.NullInt64
	if err := r.Scan(&a.ID, &a.TripID, &a.DestinationID, &a.Name, &a.Category,
		&a.StartAt, &a.EndAt, &a.Location, &estCost, &actCost,
		&a.Currency, &booked, &a.Notes, &evtID, &a.CreatedAt, &a.UpdatedAt); err != nil {
		return a, err
	}
	if estCost.Valid {
		v := estCost.Int64
		a.CostEstimated = &v
	}
	if actCost.Valid {
		v := actCost.Int64
		a.CostActual = &v
	}
	if evtID.Valid {
		v := evtID.Int64
		a.CalendarEventID = &v
	}
	a.Booked = booked == 1
	return a, nil
}

func listActivitiesByTrip(ctx *sdk.AppCtx, tripID int64) ([]Activity, error) {
	rows, err := ctx.AppDB().Query(
		`SELECT id, trip_id, COALESCE(destination_id,0), name, category,
		        COALESCE(start_at,''), COALESCE(end_at,''), location,
		        cost_estimated, cost_actual, currency, booked, notes,
		        calendar_event_id, created_at, updated_at
		 FROM activities WHERE trip_id=? ORDER BY COALESCE(start_at,''), id`, tripID,
	)
	if err != nil {
		return nil, err
	}
	out := []Activity{}
	for rows.Next() {
		a, err := scanActivity(rows)
		if err != nil {
			continue
		}
		out = append(out, a)
	}
	rows.Close()
	return out, nil
}

// ─── Todos ───────────────────────────────────────────────────────

func (a *App) toolTodosList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	tripID := int64(intArg(args, "trip_id", 0))
	if tripID == 0 {
		return nil, errors.New("trip_id required")
	}
	includeDone, _ := args["include_done"].(bool)
	q := `SELECT id, trip_id, label, COALESCE(due_at,''), done, COALESCE(done_at,''), created_at
	      FROM todos WHERE trip_id=?`
	if !includeDone {
		q += " AND done=0"
	}
	q += " ORDER BY done, COALESCE(due_at, '9999'), id"
	rows, err := ctx.AppDB().Query(q, tripID)
	if err != nil {
		return nil, err
	}
	out := []Todo{}
	for rows.Next() {
		t, err := scanTodo(rows)
		if err != nil {
			continue
		}
		out = append(out, t)
	}
	rows.Close()
	return map[string]any{"todos": out}, nil
}

func (a *App) toolTodosAdd(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	tripID := int64(intArg(args, "trip_id", 0))
	label := strings.TrimSpace(strArg(args, "label", ""))
	if tripID == 0 || label == "" {
		return nil, errors.New("trip_id and label required")
	}
	dueAt := strArg(args, "due_at", "")
	res, err := ctx.AppDB().Exec(
		`INSERT INTO todos (trip_id, label, due_at) VALUES (?, ?, ?)`,
		tripID, label, nullIfEmpty(dueAt),
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return readTodo(ctx, id)
}

func (a *App) toolTodosToggle(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64(intArg(args, "id", 0))
	if id == 0 {
		return nil, errors.New("id required")
	}
	old, err := readTodo(ctx, id)
	if err != nil {
		return nil, err
	}
	newDone := !old.Done
	var doneAt any
	if newDone {
		doneAt = time.Now().UTC().Format(time.RFC3339)
	}
	if _, err := ctx.AppDB().Exec(
		`UPDATE todos SET done=?, done_at=? WHERE id=?`,
		boolToInt(newDone), doneAt, id,
	); err != nil {
		return nil, err
	}
	return readTodo(ctx, id)
}

func (a *App) toolTodosDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64(intArg(args, "id", 0))
	if id == 0 {
		return nil, errors.New("id required")
	}
	if _, err := ctx.AppDB().Exec(`DELETE FROM todos WHERE id=?`, id); err != nil {
		return nil, err
	}
	return map[string]any{"deleted": id}, nil
}

func readTodo(ctx *sdk.AppCtx, id int64) (Todo, error) {
	row := ctx.AppDB().QueryRow(
		`SELECT id, trip_id, label, COALESCE(due_at,''), done, COALESCE(done_at,''), created_at
		 FROM todos WHERE id=?`, id,
	)
	return scanTodo(row)
}

func scanTodo(r rowScanner) (Todo, error) {
	var t Todo
	var done int
	if err := r.Scan(&t.ID, &t.TripID, &t.Label, &t.DueAt, &done, &t.DoneAt, &t.CreatedAt); err != nil {
		return t, err
	}
	t.Done = done == 1
	return t, nil
}

func listTodosByTrip(ctx *sdk.AppCtx, tripID int64) ([]Todo, error) {
	rows, err := ctx.AppDB().Query(
		`SELECT id, trip_id, label, COALESCE(due_at,''), done, COALESCE(done_at,''), created_at
		 FROM todos WHERE trip_id=? ORDER BY done, COALESCE(due_at,'9999'), id`, tripID,
	)
	if err != nil {
		return nil, err
	}
	out := []Todo{}
	for rows.Next() {
		t, err := scanTodo(rows)
		if err != nil {
			continue
		}
		out = append(out, t)
	}
	rows.Close()
	return out, nil
}

// ─── Budget ──────────────────────────────────────────────────────

func (a *App) toolBudgetSet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	tripID := int64(intArg(args, "trip_id", 0))
	category := strArg(args, "category", "")
	amount := int64(intArg(args, "amount", 0))
	if tripID == 0 || !contains(budgetCategories(), category) {
		return nil, fmt.Errorf("trip_id and valid category required (one of %v)", budgetCategories())
	}
	if amount == 0 {
		if _, err := ctx.AppDB().Exec(
			`DELETE FROM trip_budgets WHERE trip_id=? AND category=?`, tripID, category,
		); err != nil {
			return nil, err
		}
		return map[string]any{"cleared": true}, nil
	}
	// Upsert via INSERT OR REPLACE since (trip_id, category) is UNIQUE
	// without NULL columns — safe to rely on.
	if _, err := ctx.AppDB().Exec(
		`INSERT INTO trip_budgets (trip_id, category, amount) VALUES (?, ?, ?)
		 ON CONFLICT(trip_id, category) DO UPDATE SET amount=excluded.amount`,
		tripID, category, amount,
	); err != nil {
		return nil, err
	}
	var b TripBudget
	_ = ctx.AppDB().QueryRow(
		`SELECT id, trip_id, category, amount, notes FROM trip_budgets WHERE trip_id=? AND category=?`,
		tripID, category,
	).Scan(&b.ID, &b.TripID, &b.Category, &b.Amount, &b.Notes)
	return b, nil
}

func (a *App) toolBudgetSummary(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	tripID := int64(intArg(args, "trip_id", 0))
	if tripID == 0 {
		return nil, errors.New("trip_id required")
	}
	return computeBudgetSummary(ctx, tripID)
}

// computeBudgetSummary walks the trip's items and the caps table,
// producing one row per budget category. Currency conversion is
// degraded to 1:1 in v0.1 — items in mixed currencies sum naively.
func computeBudgetSummary(ctx *sdk.AppCtx, tripID int64) (BudgetSummary, error) {
	trip, err := readTrip(ctx, tripID)
	if err != nil {
		return BudgetSummary{}, err
	}
	planned := map[string]int64{}
	actual := map[string]int64{}
	for _, c := range budgetCategories() {
		planned[c] = 0
		actual[c] = 0
	}

	legs, _ := listTransportByTrip(ctx, tripID)
	for _, l := range legs {
		if l.CostEstimated != nil {
			planned["transport"] += *l.CostEstimated
		}
		if l.CostActual != nil {
			actual["transport"] += *l.CostActual
		}
	}
	accs, _ := listAccommodationsByTrip(ctx, tripID)
	for _, ac := range accs {
		if ac.CostEstimated != nil {
			planned["lodging"] += *ac.CostEstimated
		}
		if ac.CostActual != nil {
			actual["lodging"] += *ac.CostActual
		}
	}
	acts, _ := listActivitiesByTrip(ctx, tripID)
	for _, at := range acts {
		bucket := activityToBudget(at.Category)
		if at.CostEstimated != nil {
			planned[bucket] += *at.CostEstimated
		}
		if at.CostActual != nil {
			actual[bucket] += *at.CostActual
		}
	}

	// Load caps.
	caps := map[string]int64{}
	if rows, err := ctx.AppDB().Query(
		`SELECT category, amount FROM trip_budgets WHERE trip_id=?`, tripID,
	); err == nil {
		for rows.Next() {
			var c string
			var a int64
			if err := rows.Scan(&c, &a); err == nil {
				caps[c] = a
			}
		}
		rows.Close()
	}

	out := BudgetSummary{HomeCurrency: trip.HomeCurrency}
	for _, c := range budgetCategories() {
		row := BudgetCategoryRow{
			Category: c,
			Cap:      caps[c],
			Capped:   caps[c] > 0,
			Planned:  planned[c],
			Actual:   actual[c],
		}
		if row.Capped {
			row.Delta = row.Cap - row.Actual
		} else {
			row.Delta = row.Planned - row.Actual
		}
		out.Categories = append(out.Categories, row)
		out.TotalPlanned += row.Planned
		out.TotalActual += row.Actual
		out.TotalCap += row.Cap
	}
	return out, nil
}

// ─── Dashboard ───────────────────────────────────────────────────

func (a *App) toolDashboard(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	tripID := int64(intArg(args, "trip_id", 0))
	if tripID == 0 {
		return nil, errors.New("trip_id required")
	}
	trip, err := readTrip(ctx, tripID)
	if err != nil {
		return nil, err
	}
	dests, _ := listDestinationsByTrip(ctx, tripID)
	legs, _ := listTransportByTrip(ctx, tripID)
	accs, _ := listAccommodationsByTrip(ctx, tripID)
	acts, _ := listActivitiesByTrip(ctx, tripID)
	todos, _ := listTodosByTrip(ctx, tripID)
	budget, _ := computeBudgetSummary(ctx, tripID)
	return TripDashboard{
		Trip: trip, Destinations: dests, TransportLegs: legs,
		Accommodations: accs, Activities: acts, Todos: todos, Budget: budget,
	}, nil
}

// ─── Calendar coupling helpers ───────────────────────────────────

// callCreateCalendar wraps calendar.calendars_create. Returns the new
// calendar's id. Best-effort: a non-nil error means the caller should
// proceed without a linked calendar.
func callCreateCalendar(ctx *sdk.AppCtx, name, color string) (int64, error) {
	type out struct {
		ID int64 `json:"id"`
	}
	var got out
	if err := ctx.PlatformAPI().CallAppResult("calendar", "calendars_create", map[string]any{
		"name": name, "color": color, "kind": "custom",
	}, &got); err != nil {
		return 0, err
	}
	if got.ID == 0 {
		return 0, errors.New("calendar.calendars_create returned no id")
	}
	return got.ID, nil
}

func callUpdateCalendar(ctx *sdk.AppCtx, id int64, name string) error {
	var got map[string]any
	return ctx.PlatformAPI().CallAppResult("calendar", "calendars_update", map[string]any{
		"id": id, "name": name,
	}, &got)
}

func callDeleteCalendar(ctx *sdk.AppCtx, id int64) error {
	var got map[string]any
	return ctx.PlatformAPI().CallAppResult("calendar", "calendars_delete", map[string]any{
		"id": id,
	}, &got)
}

func callCreateEvent(ctx *sdk.AppCtx, calendarID int64, title, startAt, endAt string, allDay bool, location, description string) (int64, error) {
	type out struct {
		ID int64 `json:"id"`
	}
	var got out
	input := map[string]any{
		"calendar_id": calendarID,
		"title":       title,
		"start_at":    startAt,
		"end_at":      endAt,
		"all_day":     allDay,
		"location":    location,
		"description": description,
	}
	if err := ctx.PlatformAPI().CallAppResult("calendar", "events_create", input, &got); err != nil {
		return 0, err
	}
	return got.ID, nil
}

func callUpdateEvent(ctx *sdk.AppCtx, eventID int64, title, startAt, endAt string, allDay bool, location, description string) error {
	var got map[string]any
	input := map[string]any{
		"event_id":    eventID,
		"scope":       "all",
		"title":       title,
		"start_at":    startAt,
		"end_at":      endAt,
		"all_day":     allDay,
		"location":    location,
		"description": description,
	}
	return ctx.PlatformAPI().CallAppResult("calendar", "events_update", input, &got)
}

func callDeleteEvent(ctx *sdk.AppCtx, eventID int64) error {
	var got map[string]any
	return ctx.PlatformAPI().CallAppResult("calendar", "events_delete", map[string]any{
		"event_id": eventID, "scope": "all",
	}, &got)
}

// rehydrateCalendarForTrip creates calendar events for every item on
// the trip that doesn't have one yet. Used when sync flips off→on.
func rehydrateCalendarForTrip(ctx *sdk.AppCtx, trip Trip) {
	if trip.CalendarID == nil {
		return
	}
	calID := *trip.CalendarID
	legs, _ := listTransportByTrip(ctx, trip.ID)
	for _, l := range legs {
		if l.CalendarEventID != nil {
			continue
		}
		if evtID, err := callCreateEvent(ctx, calID,
			transportEventTitle(l), l.DepartAt, l.ArriveAt, false,
			l.DepartLocation, transportEventDescription(trip, l)); err == nil {
			_, _ = ctx.AppDB().Exec(`UPDATE transport_legs SET calendar_event_id=? WHERE id=?`, evtID, l.ID)
		}
	}
	accs, _ := listAccommodationsByTrip(ctx, trip.ID)
	for _, ac := range accs {
		if ac.CalendarEventID != nil {
			continue
		}
		if evtID, err := callCreateEvent(ctx, calID,
			"Stay: "+ac.Name, ac.CheckInAt, ac.CheckOutAt, true,
			ac.Address, fmt.Sprintf("Trip: %s\nAccommodation id: %d\n%s", trip.Name, ac.ID, ac.Notes)); err == nil {
			_, _ = ctx.AppDB().Exec(`UPDATE accommodations SET calendar_event_id=? WHERE id=?`, evtID, ac.ID)
		}
	}
	acts, _ := listActivitiesByTrip(ctx, trip.ID)
	for _, at := range acts {
		if at.CalendarEventID != nil || at.StartAt == "" {
			continue
		}
		endForEvent := at.EndAt
		if endForEvent == "" {
			endForEvent = at.StartAt
		}
		if evtID, err := callCreateEvent(ctx, calID,
			at.Name, at.StartAt, endForEvent, false,
			at.Location, fmt.Sprintf("Trip: %s\nActivity id: %d\n%s", trip.Name, at.ID, at.Notes)); err == nil {
			_, _ = ctx.AppDB().Exec(`UPDATE activities SET calendar_event_id=? WHERE id=?`, evtID, at.ID)
		}
	}
}

// pruneCalendarEventsForTrip deletes every linked event but keeps the
// trip's calendar around (so a subsequent on-toggle reuses it).
func pruneCalendarEventsForTrip(ctx *sdk.AppCtx, tripID int64) {
	type evtRef struct {
		table   string
		id      int64
		eventID int64
	}
	refs := []evtRef{}
	for _, table := range []string{"transport_legs", "accommodations", "activities"} {
		rows, err := ctx.AppDB().Query(
			`SELECT id, calendar_event_id FROM `+table+
				` WHERE trip_id=? AND calendar_event_id IS NOT NULL`, tripID)
		if err != nil {
			continue
		}
		for rows.Next() {
			var id, eid int64
			if err := rows.Scan(&id, &eid); err == nil {
				refs = append(refs, evtRef{table: table, id: id, eventID: eid})
			}
		}
		rows.Close()
	}
	for _, r := range refs {
		_ = callDeleteEvent(ctx, r.eventID)
		_, _ = ctx.AppDB().Exec(`UPDATE `+r.table+` SET calendar_event_id=NULL WHERE id=?`, r.id)
	}
}

// ─── HTTP wrappers ───────────────────────────────────────────────

func (a *App) handleTrips(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		args := map[string]any{}
		if r.URL.Query().Get("include_archived") == "true" {
			args["include_archived"] = true
		}
		out, err := a.toolTripsList(globalCtx, args)
		writeOrErr(w, out, err)
	case http.MethodPost:
		body := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		out, err := a.toolTripsCreate(globalCtx, body)
		writeOrErr(w, out, err)
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleTripsItem(w http.ResponseWriter, r *http.Request) {
	id, ok := pathID(r.URL.Path, "/trips/")
	if !ok {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	// Sub-path: /trips/{id}/dashboard
	if strings.HasSuffix(r.URL.Path, "/dashboard") {
		out, err := a.toolDashboard(globalCtx, map[string]any{"trip_id": id})
		writeOrErr(w, out, err)
		return
	}
	switch r.Method {
	case http.MethodGet:
		out, err := a.toolTripsGet(globalCtx, map[string]any{"id": id})
		writeOrErr(w, out, err)
	case http.MethodPatch, http.MethodPut:
		body := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		body["id"] = id
		out, err := a.toolTripsUpdate(globalCtx, body)
		writeOrErr(w, out, err)
	case http.MethodDelete:
		if _, err := a.toolTripsDelete(globalCtx, map[string]any{"id": id}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "GET, PATCH or DELETE", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleDestinations(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodPost:
		body := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		out, err := a.toolDestinationsAdd(globalCtx, body)
		writeOrErr(w, out, err)
	default:
		http.Error(w, "POST", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleDestinationsItem(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/reorder") {
		// /destinations/reorder handled separately
		http.NotFound(w, r)
		return
	}
	id, ok := pathID(r.URL.Path, "/destinations/")
	if !ok {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodPatch, http.MethodPut:
		body := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		body["id"] = id
		out, err := a.toolDestinationsUpdate(globalCtx, body)
		writeOrErr(w, out, err)
	case http.MethodDelete:
		if _, err := a.toolDestinationsDelete(globalCtx, map[string]any{"id": id}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "PATCH or DELETE", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleDestinationsReorder(w http.ResponseWriter, r *http.Request) {
	postBody(w, r, a.toolDestinationsReorder)
}

func (a *App) handleTransportLegs(w http.ResponseWriter, r *http.Request) {
	postBody(w, r, a.toolTransportLegsAdd)
}

func (a *App) handleTransportLegsItem(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/booked") {
		id, ok := pathID(strings.TrimSuffix(r.URL.Path, "/booked"), "/transport-legs/")
		if !ok {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		body := map[string]any{"id": id}
		_ = json.NewDecoder(r.Body).Decode(&body)
		body["id"] = id
		out, err := a.toolTransportLegsMarkBooked(globalCtx, body)
		writeOrErr(w, out, err)
		return
	}
	id, ok := pathID(r.URL.Path, "/transport-legs/")
	if !ok {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodPatch, http.MethodPut:
		body := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		body["id"] = id
		out, err := a.toolTransportLegsUpdate(globalCtx, body)
		writeOrErr(w, out, err)
	case http.MethodDelete:
		if _, err := a.toolTransportLegsDelete(globalCtx, map[string]any{"id": id}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "PATCH or DELETE", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleAccommodations(w http.ResponseWriter, r *http.Request) {
	postBody(w, r, a.toolAccommodationsAdd)
}

func (a *App) handleAccommodationsItem(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/booked") {
		id, ok := pathID(strings.TrimSuffix(r.URL.Path, "/booked"), "/accommodations/")
		if !ok {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		body := map[string]any{"id": id}
		_ = json.NewDecoder(r.Body).Decode(&body)
		body["id"] = id
		out, err := a.toolAccommodationsMarkBooked(globalCtx, body)
		writeOrErr(w, out, err)
		return
	}
	id, ok := pathID(r.URL.Path, "/accommodations/")
	if !ok {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodPatch, http.MethodPut:
		body := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		body["id"] = id
		out, err := a.toolAccommodationsUpdate(globalCtx, body)
		writeOrErr(w, out, err)
	case http.MethodDelete:
		if _, err := a.toolAccommodationsDelete(globalCtx, map[string]any{"id": id}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "PATCH or DELETE", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleActivities(w http.ResponseWriter, r *http.Request) {
	postBody(w, r, a.toolActivitiesAdd)
}

func (a *App) handleActivitiesItem(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/booked") {
		id, ok := pathID(strings.TrimSuffix(r.URL.Path, "/booked"), "/activities/")
		if !ok {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		body := map[string]any{"id": id}
		_ = json.NewDecoder(r.Body).Decode(&body)
		body["id"] = id
		out, err := a.toolActivitiesMarkBooked(globalCtx, body)
		writeOrErr(w, out, err)
		return
	}
	id, ok := pathID(r.URL.Path, "/activities/")
	if !ok {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodPatch, http.MethodPut:
		body := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		body["id"] = id
		out, err := a.toolActivitiesUpdate(globalCtx, body)
		writeOrErr(w, out, err)
	case http.MethodDelete:
		if _, err := a.toolActivitiesDelete(globalCtx, map[string]any{"id": id}); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	default:
		http.Error(w, "PATCH or DELETE", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleTodos(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		tripID := r.URL.Query().Get("trip_id")
		if tripID == "" {
			http.Error(w, "trip_id required", http.StatusBadRequest)
			return
		}
		args := map[string]any{}
		if n, err := strconv.ParseInt(tripID, 10, 64); err == nil {
			args["trip_id"] = float64(n)
		}
		if r.URL.Query().Get("include_done") == "true" {
			args["include_done"] = true
		}
		out, err := a.toolTodosList(globalCtx, args)
		writeOrErr(w, out, err)
	case http.MethodPost:
		body := map[string]any{}
		_ = json.NewDecoder(r.Body).Decode(&body)
		out, err := a.toolTodosAdd(globalCtx, body)
		writeOrErr(w, out, err)
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleTodosItem(w http.ResponseWriter, r *http.Request) {
	if strings.HasSuffix(r.URL.Path, "/toggle") {
		id, ok := pathID(strings.TrimSuffix(r.URL.Path, "/toggle"), "/todos/")
		if !ok {
			http.Error(w, "id required", http.StatusBadRequest)
			return
		}
		out, err := a.toolTodosToggle(globalCtx, map[string]any{"id": id})
		writeOrErr(w, out, err)
		return
	}
	id, ok := pathID(r.URL.Path, "/todos/")
	if !ok {
		http.Error(w, "id required", http.StatusBadRequest)
		return
	}
	if r.Method != http.MethodDelete {
		http.Error(w, "DELETE", http.StatusMethodNotAllowed)
		return
	}
	if _, err := a.toolTodosDelete(globalCtx, map[string]any{"id": id}); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

func (a *App) handleBudget(w http.ResponseWriter, r *http.Request) {
	postBody(w, r, a.toolBudgetSet)
}

func (a *App) handleBudgetSummary(w http.ResponseWriter, r *http.Request) {
	tripID := r.URL.Query().Get("trip_id")
	if tripID == "" {
		http.Error(w, "trip_id required", http.StatusBadRequest)
		return
	}
	n, err := strconv.ParseInt(tripID, 10, 64)
	if err != nil {
		http.Error(w, "trip_id must be int", http.StatusBadRequest)
		return
	}
	out, err := a.toolBudgetSummary(globalCtx, map[string]any{"trip_id": float64(n)})
	writeOrErr(w, out, err)
}

func (a *App) handleDashboard(w http.ResponseWriter, r *http.Request) {
	tripID := r.URL.Query().Get("trip_id")
	if tripID == "" {
		http.Error(w, "trip_id required", http.StatusBadRequest)
		return
	}
	n, err := strconv.ParseInt(tripID, 10, 64)
	if err != nil {
		http.Error(w, "trip_id must be int", http.StatusBadRequest)
		return
	}
	out, err := a.toolDashboard(globalCtx, map[string]any{"trip_id": float64(n)})
	writeOrErr(w, out, err)
}

// ─── helpers ─────────────────────────────────────────────────────

func postBody(w http.ResponseWriter, r *http.Request, fn func(*sdk.AppCtx, map[string]any) (any, error)) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	body := map[string]any{}
	_ = json.NewDecoder(r.Body).Decode(&body)
	out, err := fn(globalCtx, body)
	writeOrErr(w, out, err)
}

func writeOrErr(w http.ResponseWriter, v any, err error) {
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func projectID() string { return os.Getenv("APTEVA_PROJECT_ID") }

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
	return intArgFromAny(m[key], def)
}

func intArgFromAny(v any, def int) int {
	switch v := v.(type) {
	case float64:
		return int(v)
	case int:
		return v
	case int64:
		return int(v)
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(v)); err == nil {
			return n
		}
	}
	return def
}

func nullableInt64(m map[string]any, key string) any {
	v, ok := m[key]
	if !ok || v == nil {
		return nil
	}
	n := int64(intArgFromAny(v, 0))
	if n == 0 {
		return nil
	}
	return n
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

func contains(haystack []string, needle string) bool {
	for _, s := range haystack {
		if s == needle {
			return true
		}
	}
	return false
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

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

func parseFlexibleTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, errors.New("empty")
	}
	formats := []string{
		time.RFC3339, time.RFC3339Nano,
		"2006-01-02T15:04:05",
		"2006-01-02 15:04:05",
		"2006-01-02",
	}
	for _, f := range formats {
		if t, err := time.Parse(f, s); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("can't parse %q (try RFC3339)", s)
}

// silence the sort package "imported and not used" warning if the
// reports section is ever stripped; safe no-op.
var _ = sort.Slice
