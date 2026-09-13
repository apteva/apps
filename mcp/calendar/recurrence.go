package main

import (
	"context"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	rr "github.com/teambition/rrule-go"
	"sort"
	"strconv"
	"strings"
	"time"
	_ "time/tzdata"
)

func parseFlexibleTime(s string) (time.Time, error) {
	for _, f := range []string{time.RFC3339Nano, "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02 15:04:05Z", "2006-01-02"} {
		if t, err := time.Parse(f, strings.TrimSpace(s)); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, fmt.Errorf("invalid datetime %q", s)
}
func parseRRule(s string) (*rr.ROption, error) {
	if len(s) > 1000 {
		return nil, errors.New("rrule too long")
	}
	seen := map[string]bool{}
	parts := strings.Split(strings.ToUpper(strings.TrimSpace(s)), ";")
	for i, p := range parts {
		kv := strings.SplitN(strings.TrimSpace(p), "=", 2)
		if len(kv) != 2 || seen[kv[0]] {
			return nil, errors.New("invalid or duplicate recurrence field")
		}
		seen[kv[0]] = true
		switch kv[0] {
		case "FREQ", "UNTIL", "BYDAY", "BYMONTHDAY", "BYMONTH", "BYSETPOS", "WKST":
		case "COUNT", "INTERVAL":
			n, err := strconv.Atoi(kv[1])
			if err != nil || n < 1 || n > 110000 {
				return nil, errors.New("COUNT/INTERVAL must be between 1 and 110000")
			}
		default:
			return nil, fmt.Errorf("unsupported recurrence field %s", kv[0])
		}
		parts[i] = kv[0] + "=" + kv[1]
	}
	if seen["COUNT"] && seen["UNTIL"] {
		return nil, errors.New("COUNT and UNTIL are mutually exclusive")
	}
	o, err := rr.StrToROption(strings.Join(parts, ";"))
	if err != nil {
		return nil, err
	}
	if o.Freq > rr.DAILY {
		return nil, errors.New("recurrence supports DAILY, WEEKLY, MONTHLY or YEARLY")
	}
	if _, err = rr.NewRRule(*o); err != nil {
		return nil, err
	}
	return o, nil
}
func ruleFor(e Event, until time.Time, seek time.Time) (*rr.RRule, error) {
	o, err := parseRRule(e.RRule)
	if err != nil {
		return nil, err
	}
	start, err := parseFlexibleTime(e.StartAt)
	if err != nil {
		return nil, err
	}
	loc := time.UTC
	if !e.AllDay && e.Timezone != "" {
		loc, err = time.LoadLocation(e.Timezone)
		if err != nil {
			return nil, err
		}
	}
	o.Dtstart = start.In(loc)
	if o.Until.IsZero() || until.Before(o.Until) {
		o.Until = until
	}
	rule, err := rr.NewRRule(*o)
	if err != nil {
		return nil, err
	}
	// Seek whole recurrence periods only for uncounted rules. Normalized options
	// retain the original default month/day/weekday before DTSTART is advanced.
	if o.Count == 0 && seek.After(start) {
		n := rule.Options
		anchor := o.Dtstart
		target := seek.In(loc)
		interval := n.Interval
		switch n.Freq {
		case rr.DAILY, rr.WEEKLY:
			a := time.Date(anchor.Year(), anchor.Month(), anchor.Day(), 0, 0, 0, 0, time.UTC)
			b := time.Date(target.Year(), target.Month(), target.Day(), 0, 0, 0, 0, time.UTC)
			step := interval
			if n.Freq == rr.WEEKLY {
				step *= 7
			}
			days := int(b.Sub(a) / (24 * time.Hour))
			cycles := days/step - 1
			if cycles > 0 {
				anchor = anchor.AddDate(0, 0, cycles*step)
			}
		case rr.MONTHLY:
			months := (target.Year()-anchor.Year())*12 + int(target.Month()-anchor.Month())
			cycles := months/interval - 1
			if cycles > 0 {
				anchor = time.Date(anchor.Year(), anchor.Month()+time.Month(cycles*interval), 1, anchor.Hour(), anchor.Minute(), anchor.Second(), 0, loc)
			}
		case rr.YEARLY:
			cycles := (target.Year()-anchor.Year())/interval - 1
			if cycles > 0 {
				anchor = time.Date(anchor.Year()+cycles*interval, 1, 1, anchor.Hour(), anchor.Minute(), anchor.Second(), 0, loc)
			}
		}
		n.Dtstart = anchor
		return rr.NewRRule(n)
	}
	return rule, nil
}
func occurrenceOf(e Event, start time.Time, dur time.Duration) Occurrence {
	return Occurrence{ID: e.ID, EventID: e.ID, CalendarID: e.CalendarID, Title: e.Title, Description: e.Description, Location: e.Location, StartAt: start.UTC().Format(time.RFC3339), EndAt: start.Add(dur).UTC().Format(time.RFC3339), AllDay: e.AllDay, Status: e.Status, IsRecurring: e.RRule != "", OccurrenceStartAt: start.UTC().Format(time.RFC3339), Timezone: e.Timezone, RRule: e.RRule}
}
func expandOccurrences(e Event, from, to time.Time) []Occurrence {
	out, _ := expandOccurrencesContext(context.Background(), e, from, to)
	return out
}
func expandOccurrencesContext(ctx context.Context, e Event, from, to time.Time) ([]Occurrence, error) {
	out := []Occurrence{}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	start, err := parseFlexibleTime(e.StartAt)
	if err != nil {
		return nil, err
	}
	end, err := parseFlexibleTime(e.EndAt)
	if err != nil {
		return nil, err
	}
	if !end.After(start) {
		return nil, errors.New("invalid event duration")
	}
	dur := end.Sub(start)
	if e.RRule == "" {
		if end.After(from) && start.Before(to) {
			out = append(out, occurrenceOf(e, start, dur))
		}
		return out, nil
	}
	rule, err := ruleFor(e, to, from.Add(-dur))
	if err != nil {
		return nil, err
	}
	excluded := map[string]bool{}
	for _, x := range e.ExDate {
		t, err := parseFlexibleTime(x)
		if err != nil {
			return nil, err
		}
		excluded[t.Format(time.RFC3339)] = true
	}
	next := rule.Iterator()
	for t, ok := next(); ok && t.Before(to); t, ok = next() {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		if t.Add(dur).After(from) && !excluded[t.UTC().Format(time.RFC3339)] {
			out = append(out, occurrenceOf(e, t, dur))
		}
	}
	return out, nil
}

// index counts the rule's scheduled occurrences, including excluded dates.
func occurrenceIndex(e Event, at time.Time) (int, error) {
	if at.Year() < 1900 || at.Year() > 2200 {
		return 0, errors.New("occurrence date must be within 1900–2200")
	}
	rule, err := ruleFor(e, at, time.Time{})
	if err != nil {
		return 0, err
	}
	next := rule.Iterator()
	i := 0
	for t, ok := next(); ok && !t.After(at); t, ok = next() {
		if t.Equal(at) {
			return i, nil
		}
		i++
	}
	return 0, errors.New("occurrence_start_at is not a scheduled occurrence")
}
func setRRulePart(s, key, value string) string {
	parts := []string{}
	for _, p := range strings.Split(s, ";") {
		kv := strings.SplitN(p, "=", 2)
		if len(kv) == 2 && strings.EqualFold(kv[0], key) {
			continue
		}
		if p != "" {
			parts = append(parts, p)
		}
	}
	if value != "" {
		parts = append(parts, key+"="+value)
	}
	return strings.Join(parts, ";")
}
func truncateRule(s string, at time.Time) string {
	return setRRulePart(setRRulePart(s, "COUNT", ""), "UNTIL", at.Add(-time.Second).UTC().Format("20060102T150405Z"))
}
func (a *App) toolEventsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return a.listEvents(context.Background(), ctx, args)
}
func (a *App) listEvents(call context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if err := call.Err(); err != nil {
		return nil, err
	}
	p, err := project(ctx)
	if err != nil {
		return nil, err
	}
	from, err := parseFlexibleTime(strArg(args, "from", ""))
	if err != nil {
		return nil, err
	}
	to, err := parseFlexibleTime(strArg(args, "to", ""))
	if err != nil {
		return nil, err
	}
	if !to.After(from) || to.Sub(from) > 370*24*time.Hour || from.Year() < 1900 || to.Year() > 2200 {
		return nil, errors.New("query window must be positive, at most 370 days, within 1900–2200")
	}
	base := `SELECT ` + eventColumns + ` FROM events e JOIN calendars c ON c.id=e.calendar_id WHERE c.project_id=? AND c.enabled=1`
	baseVals := []any{p}
	ids := intSliceArg(args, "calendar_ids")
	if len(ids) > 1000 {
		return nil, errors.New("too many calendars")
	}
	if len(ids) > 0 {
		base += " AND e.calendar_id IN (" + strings.TrimSuffix(strings.Repeat("?,", len(ids)), ",") + ")"
		for _, id := range ids {
			baseVals = append(baseVals, id)
		}
	}
	// Separate branches let SQLite use the partial one-off/end and recurring
	// indexes instead of scanning each calendar's entire event history.
	q := base + ` AND e.rrule='' AND e.start_at<? AND e.end_at>? UNION ALL ` + base + ` AND e.rrule<>'' AND e.start_at<?`
	vals := append([]any{}, baseVals...)
	vals = append(vals, to.Format(time.RFC3339), from.Format(time.RFC3339))
	vals = append(vals, baseVals...)
	vals = append(vals, to.Format(time.RFC3339))
	rows, err := ctx.AppDB().QueryContext(call, q+" LIMIT 20001", vals...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Occurrence{}
	count := 0
	for rows.Next() {
		count++
		if count > 20000 {
			return nil, errors.New("too many events; select fewer calendars")
		}
		e, err := scanEvent(rows)
		if err != nil {
			return nil, err
		}
		if _, err := validateEvent(e); err != nil {
			return nil, fmt.Errorf("event %d has invalid stored data: %w", e.ID, err)
		}
		expanded, err := expandOccurrencesContext(call, e, from, to)
		if err != nil {
			return nil, err
		}
		out = append(out, expanded...)
		if len(out) > 20000 {
			return nil, errors.New("too many occurrences; request a smaller window")
		}
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].StartAt == out[j].StartAt {
			return out[i].ID < out[j].ID
		}
		return out[i].StartAt < out[j].StartAt
	})
	return map[string]any{"events": out}, nil
}
