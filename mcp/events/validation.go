package main

import (
	"errors"
	"fmt"
	"net/mail"
	"net/url"
	"strings"
	"time"
	_ "time/tzdata"
)

func validateBuyer(name, email string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("name required")
	}
	address, err := mail.ParseAddress(email)
	if err != nil || address.Address != strings.TrimSpace(email) {
		return errors.New("valid email required")
	}
	return nil
}

func validCheckoutURL(raw string) bool {
	u, err := url.Parse(raw)
	return err == nil && (u.Scheme == "https" || u.Scheme == "http") && u.Hostname() != "" && u.User == nil
}

// datetime-local fields are interpreted in the event's timezone, then stored
// as UTC. API callers can continue supplying RFC3339 timestamps with offsets.
func normalizeDate(raw string, loc *time.Location) (string, error) {
	if raw == "" {
		return "", nil
	}
	if date, err := time.Parse(time.RFC3339, raw); err == nil {
		return date.UTC().Format(time.RFC3339), nil
	}
	for _, layout := range []string{"2006-01-02T15:04", "2006-01-02T15:04:05"} {
		date, err := time.ParseInLocation(layout, raw, loc)
		if err == nil && date.Format(layout) == raw {
			return date.UTC().Format(time.RFC3339), nil
		}
	}
	return "", errors.New("use a valid date and time or RFC3339 timestamp")
}

func validateEventInput(in map[string]any, current *Event) error {
	return validateEventInputFrom(db(), in, current)
}
func validateEventInputFrom(q rowQuerier, in map[string]any, current *Event) error {
	if title, ok := in["title"]; ok && strings.TrimSpace(fmt.Sprint(title)) == "" {
		return errors.New("title required")
	}
	tz, start, end := "UTC", "", ""
	if current != nil {
		tz, start, end = current.Timezone, current.StartsAt, current.EndsAt
	}
	if _, ok := in["timezone"]; ok {
		tz = defaultString(argString(in, "timezone"), "UTC")
		in["timezone"] = tz
	}
	loc, err := time.LoadLocation(tz)
	if err != nil {
		return errors.New("invalid timezone")
	}
	for _, field := range []string{"starts_at", "ends_at"} {
		if _, ok := in[field]; !ok {
			continue
		}
		value, err := normalizeDate(argString(in, field), loc)
		if err != nil {
			return fmt.Errorf("%s: %w", field, err)
		}
		in[field] = value
		if field == "starts_at" {
			start = value
		} else {
			end = value
		}
	}
	if start != "" && end != "" {
		s, se := time.Parse(time.RFC3339, start)
		e, ee := time.Parse(time.RFC3339, end)
		if se != nil || ee != nil || !e.After(s) {
			return errors.New("end must be after start")
		}
	}
	if argInt(in, "capacity") < 0 {
		return errors.New("capacity must be zero or positive")
	}
	if raw := argString(in, "external_checkout_url"); raw != "" && !validCheckoutURL(raw) {
		return errors.New("checkout URL must be an http or https URL")
	}
	if id := argInt(in, "venue_id"); id > 0 {
		var found int64
		if err := q.QueryRow(`SELECT id FROM venues WHERE id=? AND project_id=?`, id, projectID()).Scan(&found); err != nil {
			return errors.New("venue not found in this project")
		}
	}
	return nil
}

func validateTicketTypeInput(in map[string]any) error {
	if argInt(in, "price_cents") < 0 || argInt(in, "capacity") < 0 {
		return errors.New("price and capacity must be zero or positive")
	}
	start, end := argString(in, "sales_start_at"), argString(in, "sales_end_at")
	for _, value := range []string{start, end} {
		if value != "" {
			if _, err := time.Parse(time.RFC3339, value); err != nil {
				return errors.New("sales dates must be RFC3339 timestamps")
			}
		}
	}
	if start != "" && end != "" {
		s, _ := time.Parse(time.RFC3339, start)
		e, _ := time.Parse(time.RFC3339, end)
		if !e.After(s) {
			return errors.New("sales end must be after sales start")
		}
	}
	return nil
}

func ticketTypeAvailable(t TicketType, at time.Time) error {
	if t.Status != "active" {
		return errors.New("ticket type is not active")
	}
	if t.SalesStartAt != "" {
		start, err := time.Parse(time.RFC3339, t.SalesStartAt)
		if err != nil || at.Before(start) {
			return errors.New("ticket sales have not started")
		}
	}
	if t.SalesEndAt != "" {
		end, err := time.Parse(time.RFC3339, t.SalesEndAt)
		if err != nil || !at.Before(end) {
			return errors.New("ticket sales have ended")
		}
	}
	return nil
}
