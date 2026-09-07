package main

import (
	"bytes"
	_ "embed"
	"html/template"
	"net/http"
	"strconv"
	"strings"
	"time"
)

//go:embed public.html
var publicHTML string

var publicTemplate = template.Must(template.New("event").Parse(publicHTML))

// Public responses deliberately exclude operational notes, application IDs,
// project identifiers, and private counts. Never serialize storage rows here.
type publicEvent struct {
	Title               string `json:"title"`
	Slug                string `json:"slug"`
	Description         string `json:"description"`
	Timezone            string `json:"timezone"`
	StartsAt            string `json:"starts_at"`
	EndsAt              string `json:"ends_at"`
	ExternalCheckoutURL string `json:"external_checkout_url,omitempty"`
}
type publicVenue struct {
	Name    string `json:"name"`
	Address string `json:"address"`
	City    string `json:"city"`
	Country string `json:"country"`
}
type publicSlot struct {
	PerformerName string `json:"performer_name"`
	Title         string `json:"title"`
	StartsAt      string `json:"starts_at"`
	EndsAt        string `json:"ends_at"`
	PhotoURL      string `json:"photo_url,omitempty"`
}
type publicTicketType struct {
	ID         int64  `json:"id"`
	Name       string `json:"name"`
	PriceCents int64  `json:"price_cents"`
	Currency   string `json:"currency"`
	Available  bool   `json:"available"`
}
type publicPage struct {
	Event              publicEvent        `json:"event"`
	Venue              *publicVenue       `json:"venue,omitempty"`
	Schedule           []publicSlot       `json:"schedule"`
	TicketTypes        []publicTicketType `json:"ticket_types"`
	CanRegister        bool               `json:"can_register"`
	When               string             `json:"-"`
	Settings           EventSettings      `json:"settings"`
	Status             string             `json:"status"`
	CanApply           bool               `json:"can_apply"`
	ApplicationMessage string             `json:"application_message"`
}

func makePublicPage(event *Event) (*publicPage, error) {
	types, err := listTicketTypes(event.ID)
	if err != nil {
		return nil, err
	}
	slots, err := listSlots(event.ID)
	if err != nil {
		return nil, err
	}
	checkout := event.ExternalCheckoutURL
	if !validCheckoutURL(checkout) {
		checkout = ""
	}
	page := &publicPage{
		Event:    publicEvent{event.Title, event.Slug, event.Description, event.Timezone, event.StartsAt, event.EndsAt, checkout},
		Schedule: []publicSlot{}, TicketTypes: []publicTicketType{},
		When: "Date to be announced",
	}
	settings, err := settingsFrom(db(), event.ID)
	if err != nil {
		return nil, err
	}
	page.Settings = settings
	page.Status = event.Status
	if settings.Cancelled {
		page.Status = "cancelled"
	}
	page.ApplicationMessage = applicationClosed(event, settings)
	page.CanApply = page.ApplicationMessage == ""
	loc, err := time.LoadLocation(event.Timezone)
	if err != nil {
		loc = time.UTC
	}
	page.Event.Timezone = loc.String()
	if start, err := time.Parse(time.RFC3339, event.StartsAt); err == nil {
		page.When = start.In(loc).Format("Monday, 2 January 2006 · 15:04")
		if end, err := time.Parse(time.RFC3339, event.EndsAt); err == nil {
			page.When += " – " + end.In(loc).Format("2 Jan 15:04")
		}
	}
	if event.VenueID != nil {
		venue, err := getVenue(*event.VenueID)
		if err != nil {
			return nil, err
		}
		page.Venue = &publicVenue{venue.Name, venue.Address, venue.City, venue.Country}
	}
	for _, slot := range slots {
		if slot.Status == "cancelled" || !settings.LineupPublished || settings.Cancelled {
			continue
		}
		photo := ""
		if slot.ApplicationID != nil {
			app, err := getApplication(*slot.ApplicationID)
			if err != nil || app.Status != "accepted" {
				continue
			}
			var consent int
			if db().QueryRow(`SELECT consent FROM application_photos WHERE application_id=?`, app.ID).Scan(&consent) == nil && consent == 1 {
				photo = "/public/" + event.Slug + "/photos/" + strconv.FormatInt(app.ID, 10)
			}
		}
		page.Schedule = append(page.Schedule, publicSlot{slot.PerformerName, slot.Title, slot.StartsAt, slot.EndsAt, photo})
	}
	room := event.Capacity == 0 || event.TicketCount < event.Capacity
	page.CanRegister = len(types) == 0 && room && event.ExternalCheckoutURL == ""
	for _, tt := range types {
		// A type being paused/archived does not permit untyped registration.
		if ticketTypeAvailable(tt, time.Now()) != nil {
			continue
		}
		var sold int64
		if err := db().QueryRow(`SELECT COUNT(*) FROM tickets WHERE ticket_type_id=? AND status='active'`, tt.ID).Scan(&sold); err != nil {
			return nil, err
		}
		available := room && (tt.Capacity == 0 || sold < tt.Capacity) && tt.PriceCents == 0 && event.ExternalCheckoutURL == ""
		page.TicketTypes = append(page.TicketTypes, publicTicketType{tt.ID, tt.Name, tt.PriceCents, tt.Currency, available})
		page.CanRegister = page.CanRegister || available
	}
	if settings.Cancelled || event.Status != "published" {
		page.CanRegister = false
		page.Event.ExternalCheckoutURL = ""
	}
	return page, nil
}

func (a *App) handlePublic(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Cache-Control", "no-store")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	w.Header().Set("Referrer-Policy", "no-referrer")
	rest := strings.Trim(strings.TrimPrefix(r.URL.Path, "/public/"), "/")
	parts := strings.Split(rest, "/")
	if rest == "" && r.Method == "GET" {
		events, err := listEvents("", 500)
		if err != nil {
			http.Error(w, "Unable to list events", 500)
			return
		}
		pages := []*publicPage{}
		for _, e := range events {
			if e.Visibility != "public" || (e.Status != "published" && e.Status != "closed") {
				continue
			}
			end := e.EndsAt
			if end == "" {
				end = e.StartsAt
			}
			if end != "" && end < now() {
				continue
			}
			page, err := makePublicPage(&e)
			if err != nil {
				http.Error(w, "Unable to load events", 500)
				return
			}
			pages = append(pages, page)
		}
		writeJSON(w, pages)
		return
	}
	if len(parts) > 3 || parts[0] == "" {
		http.NotFound(w, r)
		return
	}
	event, err := getPublicEvent(parts[0])
	if err != nil {
		http.NotFound(w, r)
		return
	}
	if len(parts) == 3 && parts[1] == "photos" && r.Method == "GET" {
		id, _ := strconv.ParseInt(parts[2], 10, 64)
		page, err := makePublicPage(event)
		if err == nil {
			expected := "/public/" + event.Slug + "/photos/" + parts[2]
			for _, slot := range page.Schedule {
				if slot.PhotoURL == expected {
					serveApplicationPhoto(w, r, id)
					return
				}
			}
		}
		http.NotFound(w, r)
		return
	}
	if len(parts) == 1 && r.Method == http.MethodGet {
		page, err := makePublicPage(event)
		if err != nil {
			http.Error(w, "Unable to load event", http.StatusInternalServerError)
			return
		}
		if r.URL.Query().Get("format") == "json" || strings.Contains(r.Header.Get("Accept"), "application/json") {
			writeJSON(w, page)
			return
		}
		var body bytes.Buffer
		if err := publicTemplate.Execute(&body, page); err != nil {
			http.Error(w, "Unable to render event", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write(body.Bytes())
		return
	}
	if r.Method != http.MethodPost || len(parts) != 2 || (parts[1] != "apply" && parts[1] != "register") {
		http.NotFound(w, r)
		return
	}
	r.Body = http.MaxBytesReader(w, r.Body, 3<<20)
	var in map[string]any
	if !decodeJSON(w, r, &in) {
		return
	}
	if in == nil {
		http.Error(w, "JSON object required", http.StatusBadRequest)
		return
	}
	in["event_id"] = event.ID
	if parts[1] == "apply" {
		if err := submitPublicApplication(event, in); err != nil {
			writeResult[any](w, nil, err)
			return
		}
		writeJSON(w, map[string]any{"status": "received", "message": "Application received. Your spot is not confirmed yet; the organiser will contact you."})
		return
	}
	tickets, err := issueTickets(issueTicketsInput{
		EventID: event.ID, BuyerName: argString(in, "buyer_name"), BuyerEmail: argString(in, "buyer_email"),
		AttendeeName: argString(in, "attendee_name"), AttendeeEmail: argString(in, "attendee_email"),
		TicketTypeID: argInt(in, "ticket_type_id"), Quantity: argInt(in, "quantity"), Source: "public",
	})
	if err != nil {
		writeResult[any](w, nil, err)
		return
	}
	codes := make([]string, 0, len(tickets))
	for _, ticket := range tickets {
		codes = append(codes, ticket.Code)
	}
	writeJSON(w, map[string]any{"message": "Registration confirmed. Save your ticket codes for check-in.", "ticket_codes": codes})
}
