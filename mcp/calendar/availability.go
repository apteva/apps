package main

import (
	"context"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"sort"
	"strconv"
	"strings"
	"time"
)

type timeRange struct{ start, end time.Time }
type workingHours map[time.Weekday]struct{ start, end string }

func defaultWorkingHours() workingHours {
	wh := workingHours{}
	for _, d := range []time.Weekday{time.Monday, time.Tuesday, time.Wednesday, time.Thursday, time.Friday} {
		wh[d] = struct{ start, end string }{"09:00", "18:00"}
	}
	return wh
}
func minuteOfDay(s string) (int, error) {
	parts := strings.Split(s, ":")
	if len(parts) != 2 || len(parts[0]) != 2 || len(parts[1]) != 2 {
		return 0, errors.New("working hours must be HH:MM")
	}
	h, e := strconv.Atoi(parts[0])
	m, e2 := strconv.Atoi(parts[1])
	if e != nil || e2 != nil || h < 0 || h > 24 || m < 0 || m > 59 || (h == 24 && m != 0) {
		return 0, errors.New("invalid working hours")
	}
	return h*60 + m, nil
}
func parseWorkingHours(v any) (workingHours, error) {
	if v == nil {
		return defaultWorkingHours(), nil
	}
	m, ok := v.(map[string]any)
	if !ok {
		return nil, errors.New("working_hours must be an object")
	}
	out := workingHours{}
	days := map[string]time.Weekday{"mon": time.Monday, "tue": time.Tuesday, "wed": time.Wednesday, "thu": time.Thursday, "fri": time.Friday, "sat": time.Saturday, "sun": time.Sunday}
	for k, v := range m {
		day, ok := days[strings.ToLower(k)]
		if !ok {
			return nil, fmt.Errorf("unknown weekday %s", k)
		}
		item, ok := v.(map[string]any)
		if !ok {
			return nil, errors.New("working hours require start/end")
		}
		s, _ := item["start"].(string)
		e, _ := item["end"].(string)
		sm, err := minuteOfDay(s)
		if err != nil {
			return nil, err
		}
		em, err := minuteOfDay(e)
		if err != nil || em <= sm {
			return nil, errors.New("working-hour end must be after start on the same day")
		}
		out[day] = struct{ start, end string }{s, e}
	}
	return out, nil
}
func snapToStep(t time.Time, step time.Duration) time.Time {
	n := t.Truncate(step)
	if n.Before(t) {
		n = n.Add(step)
	}
	return n
}
func (a *App) toolEventsFindSlot(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return a.findSlot(context.Background(), ctx, args)
}
func (a *App) findSlot(call context.Context, ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if err := call.Err(); err != nil {
		return nil, err
	}
	duration := intArg(args, "duration_minutes", 0)
	before := intArg(args, "buffer_before_minutes", 0)
	after := intArg(args, "buffer_after_minutes", 0)
	limit := intArg(args, "limit", 10)
	if duration < 1 || duration > 1440 || before < 0 || before > 1440 || after < 0 || after > 1440 || limit < 1 || limit > 100 {
		return nil, errors.New("duration 1–1440, buffers 0–1440, limit 1–100 required")
	}
	from, err := parseFlexibleTime(strArg(args, "window_start", ""))
	if err != nil {
		return nil, err
	}
	to, err := parseFlexibleTime(strArg(args, "window_end", ""))
	if err != nil {
		return nil, err
	}
	if !to.After(from) || to.Sub(from) > 366*24*time.Hour {
		return nil, errors.New("window must be positive and at most 366 days")
	}
	loc, err := time.LoadLocation(strArg(args, "timezone", "UTC"))
	if err != nil {
		return nil, errors.New("invalid timezone")
	}
	wh, err := parseWorkingHours(args["working_hours"])
	if err != nil {
		return nil, err
	}
	out, err := a.listEvents(call, ctx, map[string]any{"from": from.Add(-time.Duration(after)*time.Minute - 24*time.Hour).Format(time.RFC3339), "to": to.Add(time.Duration(before)*time.Minute + 24*time.Hour).Format(time.RFC3339), "calendar_ids": args["calendar_ids"]})
	if err != nil {
		return nil, err
	}
	busy := []timeRange{}
	for _, o := range out.(map[string]any)["events"].([]Occurrence) {
		if o.Status == "cancelled" {
			continue
		}
		s, _ := parseFlexibleTime(o.StartAt)
		e, _ := parseFlexibleTime(o.EndAt)
		// Date-only events block their local date, not UTC midnight translated to a
		// different date. The same semantics are used by the panel.
		if o.AllDay {
			s = time.Date(s.Year(), s.Month(), s.Day(), 0, 0, 0, 0, loc)
			e = time.Date(e.Year(), e.Month(), e.Day(), 0, 0, 0, 0, loc)
		}
		busy = append(busy, timeRange{s.Add(-time.Duration(before) * time.Minute), e.Add(time.Duration(after) * time.Minute)})
	}
	sort.Slice(busy, func(i, j int) bool { return busy[i].start.Before(busy[j].start) })
	merged := []timeRange{}
	for _, b := range busy {
		if len(merged) > 0 && !b.start.After(merged[len(merged)-1].end) {
			if b.end.After(merged[len(merged)-1].end) {
				merged[len(merged)-1].end = b.end
			}
		} else {
			merged = append(merged, b)
		}
	}
	slots := []map[string]string{}
	dur := time.Duration(duration) * time.Minute
	local := from.In(loc)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	bi := 0
	for ; day.Before(to); day = day.AddDate(0, 0, 1) {
		if err := call.Err(); err != nil {
			return nil, err
		}
		hours, ok := wh[day.Weekday()]
		if !ok {
			continue
		}
		sm, _ := minuteOfDay(hours.start)
		em, _ := minuteOfDay(hours.end)
		s := time.Date(day.Year(), day.Month(), day.Day(), sm/60, sm%60, 0, 0, loc)
		end := time.Date(day.Year(), day.Month(), day.Day(), em/60, em%60, 0, 0, loc)
		if s.Before(from) {
			s = from
		}
		if end.After(to) {
			end = to
		}
		s = snapToStep(s, 15*time.Minute)
		for !s.Add(dur).After(end) {
			for bi < len(merged) && !merged[bi].end.After(s) {
				bi++
			}
			if bi < len(merged) && merged[bi].start.Before(s.Add(dur)) {
				s = snapToStep(merged[bi].end, 15*time.Minute)
				continue
			}
			slots = append(slots, map[string]string{"start": s.UTC().Format(time.RFC3339), "end": s.Add(dur).UTC().Format(time.RFC3339)})
			if len(slots) >= limit {
				return map[string]any{"slots": slots}, nil
			}
			s = s.Add(15 * time.Minute)
		}
	}
	return map[string]any{"slots": slots}, nil
}
