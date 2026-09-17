package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"regexp"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const defaultDueTime = "09:00"

// A sidecar that was down overnight should still deliver yesterday's events, but
// never an unbounded backlog. Anything older than this is considered missed.
const dueCatchUp = 48 * time.Hour

// The SDK's schedule grammar is "@every <duration>" only — cron is not
// implemented — so "due at 09:00 local" is a frequent tick that asks whether the
// instant has passed, not a cron expression.
const dueSchedule = "@every 5m"

var dueTimePattern = regexp.MustCompile(`^([01][0-9]|2[0-3]):[0-5][0-9]$`)

func dueLocation(s Settings) (*time.Location, error) {
	name := strings.TrimSpace(s.Timezone)
	if name == "" {
		return time.UTC, nil
	}
	loc, e := time.LoadLocation(name)
	if e != nil {
		return time.UTC, invalid("timezone must be an IANA name such as Europe/Paris")
	}
	return loc, nil
}

func dueOffset(s Settings) (time.Duration, error) {
	value := strings.TrimSpace(s.DueTime)
	if value == "" {
		value = defaultDueTime
	}
	if !dueTimePattern.MatchString(value) {
		return 0, invalid("due_time must be a 24-hour HH:MM time")
	}
	t, e := time.Parse("15:04", value)
	if e != nil {
		return 0, invalid("due_time must be a 24-hour HH:MM time")
	}
	return time.Duration(t.Hour())*time.Hour + time.Duration(t.Minute())*time.Minute, nil
}

// dueInstant resolves a stored date to the moment it becomes due. A timestamp
// already carries its own offset and is used as written; a bare day is anchored
// to the project's timezone and due time.
func dueInstant(value string, loc *time.Location, offset time.Duration) (time.Time, bool) {
	value = strings.TrimSpace(value)
	if value == "" {
		return time.Time{}, false
	}
	if day, e := time.ParseInLocation("2006-01-02", value, loc); e == nil {
		return day.Add(offset), true
	}
	if at, e := time.Parse(time.RFC3339, value); e == nil {
		return at, true
	}
	return time.Time{}, false
}

// dueWindow is the span this scan may fire for. The first scan of a project
// starts the clock at now, so installing into a project holding a year of
// back-dated content does not stampede the bus.
func dueWindow(db *sql.DB, pid string, now time.Time) (time.Time, error) {
	var raw string
	e := db.QueryRow("SELECT since FROM editorial_due_state WHERE project_id=?", pid).Scan(&raw)
	if errors.Is(e, sql.ErrNoRows) {
		_, e = db.Exec("INSERT INTO editorial_due_state(project_id,since) VALUES(?,?)", pid, now.UTC().Format(time.RFC3339))
		return now, e
	}
	if e != nil {
		return time.Time{}, e
	}
	since, e := time.Parse(time.RFC3339, raw)
	if e != nil {
		return now, nil
	}
	if floor := now.Add(-dueCatchUp); since.Before(floor) {
		return floor, nil
	}
	return since, nil
}

// claimNotice records that this exact (topic, record, due instant) fired. The
// primary key is the whole guarantee: a second scan inserts nothing and reports
// false, and a rescheduled date is a different key, so it re-arms.
func claimNotice(db *sql.DB, pid, topic string, ref int64, due time.Time) (bool, error) {
	res, e := db.Exec(
		"INSERT OR IGNORE INTO editorial_due_notices(project_id,topic,ref_id,due_at) VALUES(?,?,?,?)",
		pid, topic, ref, due.UTC().Format(time.RFC3339))
	if e != nil {
		return false, e
	}
	n, e := res.RowsAffected()
	return n > 0, e
}

// Subscribers route on brand far more often than they look one up, so every due
// payload carries the id and the human name. Both are empty for unassigned
// content rather than absent, so the shape never varies.
func brandFields(s Settings, id string) (string, string) {
	if b := findBrand(s, id); b != nil {
		return id, b.Name
	}
	return id, ""
}

type dueCandidate struct {
	topic   string
	ref     int64
	due     time.Time
	payload map[string]any
}

func (a *App) runDueScanner(runCtx context.Context, ctx *sdk.AppCtx) error {
	pid := strings.TrimSpace(ctx.CurrentProject())
	if pid == "" {
		// The SDK runs one empty-project tick when ListProjects fails, so a
		// platform blip does not silence the worker. There is nothing to scan.
		return nil
	}
	db := ctx.AppDB()
	if db == nil {
		return nil
	}
	settings, e := getSettings(db, pid)
	if e != nil {
		return e
	}
	loc, e := dueLocation(settings)
	if e != nil {
		// Unusable settings must not wedge the worker for every other project.
		ctx.Logger().Warn("due scanner: unusable timezone", "project", pid, "err", e)
		loc = time.UTC
	}
	offset, e := dueOffset(settings)
	if e != nil {
		offset = 9 * time.Hour
	}

	now := a.clock()
	since, e := dueWindow(db, pid, now)
	if e != nil {
		return e
	}
	if !since.Before(now) {
		return nil
	}

	candidates, e := a.dueCandidates(db, pid, settings, loc, offset, since, now)
	if e != nil {
		return e
	}
	for _, c := range candidates {
		select {
		case <-runCtx.Done():
			return nil
		default:
		}
		fresh, e := claimNotice(db, pid, c.topic, c.ref, c.due)
		if e != nil {
			return e
		}
		if fresh {
			ctx.Emit(c.topic, c.payload)
		}
	}
	return nil
}

// dueCandidates gathers everything whose due instant falls inside the window.
// Rows are narrowed in SQL by date prefix — widened a day either side so a
// timezone can never cut one off — and the exact instant is decided in Go, where
// the location is known.
func (a *App) dueCandidates(db *sql.DB, pid string, settings Settings, loc *time.Location, offset time.Duration, since, now time.Time) ([]dueCandidate, error) {
	from := since.Add(-24 * time.Hour).UTC().Format("2006-01-02")
	to := now.Add(24 * time.Hour).UTC().Format("2006-01-02")
	within := func(at time.Time) bool { return !at.Before(since) && !at.After(now) }

	out := []dueCandidate{}
	rows, e := db.Query(
		"SELECT id,revision,data FROM editorial_items WHERE project_id=? AND json_extract(data,'$.archived')=?"+
			" AND (substr(COALESCE(json_extract(data,'$.planned_at'),''),1,10) BETWEEN ? AND ?"+
			"   OR substr(COALESCE(json_extract(data,'$.deadline'),''),1,10) BETWEEN ? AND ?)"+
			" ORDER BY id", pid, false, from, to, from, to)
	if e != nil {
		return nil, e
	}
	items := map[int64]ItemData{}
	for rows.Next() {
		var id, revision int64
		var raw string
		if e = rows.Scan(&id, &revision, &raw); e != nil {
			rows.Close()
			return nil, e
		}
		var d ItemData
		if e = json.Unmarshal([]byte(raw), &d); e != nil {
			rows.Close()
			return nil, e
		}
		items[id] = d
		brandID, brandName := brandFields(settings, d.BrandID)
		for _, f := range []struct {
			field, value, topic string
		}{
			{"planned_at", d.PlannedAt, "content.due"},
			{"deadline", d.Deadline, "content.deadline"},
		} {
			at, ok := dueInstant(f.value, loc, offset)
			if !ok || !within(at) {
				continue
			}
			out = append(out, dueCandidate{topic: f.topic, ref: id, due: at, payload: map[string]any{
				"id": id, "revision": revision, "brand_id": brandID, "brand": brandName,
				"title": d.Title, "format": d.Format, "status": d.Status, "approval": d.Approval,
				"owner": d.Owner, "date_field": f.field, "due_at": at.UTC().Format(time.RFC3339),
			}})
		}
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}

	rows, e = db.Query(
		"SELECT r.id,r.item_id,r.revision,r.data,i.data FROM editorial_releases r"+
			" JOIN editorial_items i ON i.project_id=r.project_id AND i.id=r.item_id"+
			" WHERE r.project_id=? AND json_extract(r.data,'$.archived')=? AND json_extract(i.data,'$.archived')=?"+
			" AND substr(COALESCE(json_extract(r.data,'$.planned_at'),''),1,10) BETWEEN ? AND ?"+
			" ORDER BY r.id", pid, false, false, from, to)
	if e != nil {
		return nil, e
	}
	defer rows.Close()
	for rows.Next() {
		var id, itemID, revision int64
		var rawRelease, rawItem string
		if e = rows.Scan(&id, &itemID, &revision, &rawRelease, &rawItem); e != nil {
			return nil, e
		}
		var rd ReleaseData
		var parent ItemData
		if e = json.Unmarshal([]byte(rawRelease), &rd); e != nil {
			return nil, e
		}
		if e = json.Unmarshal([]byte(rawItem), &parent); e != nil {
			return nil, e
		}
		at, ok := dueInstant(rd.PlannedAt, loc, offset)
		if !ok || !within(at) {
			continue
		}
		brandID, brandName := brandFields(settings, parent.BrandID)
		out = append(out, dueCandidate{topic: "release.due", ref: id, due: at, payload: map[string]any{
			"id": id, "item_id": itemID, "revision": revision, "brand_id": brandID, "brand": brandName,
			"title": parent.Title, "channel": rd.Channel, "status": rd.Status, "url": rd.URL,
			"approval": parent.Approval, "owner": parent.Owner, "due_at": at.UTC().Format(time.RFC3339),
		}})
	}
	return out, rows.Err()
}
