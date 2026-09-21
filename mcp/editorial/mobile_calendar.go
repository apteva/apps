package main

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const (
	defaultMobileHorizonDays = 30
	maxMobileHorizonDays     = 365
	defaultMobileEventLimit  = 12
	maxMobileEventLimit      = 20
	maxMobileCalendarScan    = 2000
)

type mobileCalendarEvent struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	At        string `json:"at"`
	Detail    string `json:"detail"`
	BrandName string `json:"brand_name"`
	KindLabel string `json:"kind_label"`
}

type mobileCalendarSummary struct {
	Events    []mobileCalendarEvent `json:"events"`
	Truncated bool                  `json:"truncated"`
}

func boundedMobileInt(value string, fallback, minimum, maximum int) int {
	n, e := strconv.Atoi(strings.TrimSpace(value))
	if e != nil {
		n = fallback
	}
	if n < minimum {
		return minimum
	}
	if n > maximum {
		return maximum
	}
	return n
}

func mobileLabel(value string) string {
	words := strings.Fields(strings.ReplaceAll(strings.TrimSpace(value), "_", " "))
	for i := range words {
		letters := []rune(words[i])
		if len(letters) > 0 {
			letters[0] = unicode.ToUpper(letters[0])
			words[i] = string(letters)
		}
	}
	return strings.Join(words, " ")
}

func mobileEventDetail(event CalendarEvent) string {
	if event.Kind == "release" {
		return strings.Join(nonemptyStrings(event.Channel, mobileLabel(event.Status)), " · ")
	}
	return strings.Join(nonemptyStrings(mobileLabel(event.Format), mobileLabel(event.Status)), " · ")
}

func nonemptyStrings(values ...string) []string {
	out := make([]string, 0, len(values))
	for _, value := range values {
		if value = strings.TrimSpace(value); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func mobileEventAt(value string, loc *time.Location, offset time.Duration) string {
	if at, ok := dueInstant(value, loc, offset); ok {
		return at.Format(time.RFC3339)
	}
	return value
}

func (a *App) mobileProjectID(r *http.Request) string {
	if a.ctx != nil {
		if projectID := strings.TrimSpace(a.ctx.CurrentProject()); projectID != "" {
			return projectID
		}
	}
	if r == nil {
		return ""
	}
	return strings.TrimSpace(r.Header.Get("X-Apteva-Project-ID"))
}

func (a *App) mobileCalendarSummary(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		w.WriteHeader(http.StatusMethodNotAllowed)
		return
	}
	if a.ctx == nil || a.ctx.AppDB() == nil {
		respond(w, nil, fmt.Errorf("app not mounted"))
		return
	}
	projectID := a.mobileProjectID(r)
	if projectID == "" {
		respond(w, nil, invalid("a project context is required"))
		return
	}

	settings, e := getSettings(a.ctx.AppDB(), projectID)
	if e != nil {
		respond(w, nil, e)
		return
	}
	loc, e := dueLocation(settings)
	if e != nil {
		respond(w, nil, e)
		return
	}
	offset, e := dueOffset(settings)
	if e != nil {
		respond(w, nil, e)
		return
	}

	query := r.URL.Query()
	horizonDays := boundedMobileInt(query.Get("horizon_days"), defaultMobileHorizonDays, 1, maxMobileHorizonDays)
	limit := boundedMobileInt(query.Get("limit"), defaultMobileEventLimit, 1, maxMobileEventLimit)
	today := a.clock().In(loc)
	from := today.Format("2006-01-02")
	to := today.AddDate(0, 0, horizonDays).Format("2006-01-02")
	includeReleases := true
	if raw := strings.TrimSpace(query.Get("include_releases")); raw != "" {
		includeReleases, e = strconv.ParseBool(raw)
		if e != nil {
			respond(w, nil, invalid("include_releases must be true or false"))
			return
		}
	}

	result, e := calendarEvents(a.ctx.AppDB(), projectID, map[string]any{
		"from":             from,
		"to":               to,
		"date_field":       query.Get("date_field"),
		"brand_id":         query.Get("brand_id"),
		"include_releases": includeReleases,
		"limit":            int64(maxMobileCalendarScan),
	})
	if e != nil {
		respond(w, nil, e)
		return
	}
	payload := result.(map[string]any)
	calendar := payload["events"].([]CalendarEvent)
	truncated, _ := payload["truncated"].(bool)
	if len(calendar) > limit {
		calendar = calendar[:limit]
		truncated = true
	}

	brandNames := map[string]string{}
	for _, brand := range settings.Brands {
		brandNames[brand.ID] = brand.Name
	}
	events := make([]mobileCalendarEvent, 0, len(calendar))
	for _, event := range calendar {
		id := fmt.Sprintf("item:%d", event.ItemID)
		kindLabel := "Content"
		if event.Kind == "release" {
			id = fmt.Sprintf("release:%d", event.ReleaseID)
			kindLabel = "Release"
		}
		events = append(events, mobileCalendarEvent{
			ID: id, Title: event.Title, At: mobileEventAt(event.At, loc, offset),
			Detail: mobileEventDetail(event), BrandName: brandNames[event.BrandID], KindLabel: kindLabel,
		})
	}
	respond(w, mobileCalendarSummary{Events: events, Truncated: truncated}, nil)
}
