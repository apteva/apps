package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"io"
	"net/http"
	"regexp"
	"strings"
	"sync"
	"time"
)

// One sidecar owns the database. Serialize read/modify/write mutations before
// opening their transactions so simultaneous exception edits cannot lose data.
var mutationMu sync.Mutex

type dbReader interface {
	Query(string, ...any) (*sql.Rows, error)
	QueryRow(string, ...any) *sql.Row
}

func project(ctx *sdk.AppCtx) (string, error) {
	p := strings.TrimSpace(ctx.CurrentProject())
	if p == "" {
		return "", errors.New("project context required")
	}
	return p, nil
}
func requestContext(r *http.Request) (*sdk.AppCtx, error) {
	if err := r.Context().Err(); err != nil {
		return nil, err
	}
	if globalCtx == nil {
		return nil, errors.New("calendar not mounted")
	}
	p := strings.TrimSpace(r.Header.Get("X-Apteva-Project-ID"))
	installed := strings.TrimSpace(globalCtx.CurrentProject())
	if p == "" {
		p = installed
	}
	if p == "" {
		return nil, errors.New("authorized project context required")
	}
	if installed != "" && installed != p {
		return nil, errors.New("project does not match installation")
	}
	if q := r.URL.Query().Get("project_id"); q != "" && q != p {
		return nil, errors.New("project selector does not match authorized project")
	}
	return globalCtx.WithProject(p), nil
}
func decodeBody(w http.ResponseWriter, r *http.Request, out *map[string]any) bool {
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20))
	if err := dec.Decode(out); err != nil || *out == nil {
		http.Error(w, "a JSON object is required", 400)
		return false
	}
	var extra any
	if err := dec.Decode(&extra); err != io.EOF {
		http.Error(w, "expected a single JSON object", 400)
		return false
	}
	return true
}

var colorPattern = regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)

func validateCalendar(name, color, kind string) error {
	if strings.TrimSpace(name) == "" || len(name) > 200 {
		return errors.New("name required (max 200 characters)")
	}
	if !colorPattern.MatchString(color) {
		return errors.New("color must be #RRGGBB")
	}
	switch kind {
	case "personal", "work", "holidays", "blocked", "custom":
	default:
		return errors.New("invalid calendar kind")
	}
	return nil
}
func calendarIn(db dbReader, p string, id int64) (Calendar, error) {
	var c Calendar
	var en int
	err := db.QueryRow(`SELECT id,project_id,name,color,kind,enabled,created_at FROM calendars WHERE id=? AND project_id=?`, id, p).Scan(&c.ID, &c.ProjectID, &c.Name, &c.Color, &c.Kind, &en, &c.CreatedAt)
	c.Enabled = en == 1
	return c, err
}
func getCalendar(ctx *sdk.AppCtx, id int64) (Calendar, error) {
	p, err := project(ctx)
	if err != nil {
		return Calendar{}, err
	}
	return calendarIn(ctx.AppDB(), p, id)
}
func (a *App) toolCalendarsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	p, err := project(ctx)
	if err != nil {
		return nil, err
	}
	rows, err := ctx.AppDB().Query(`SELECT id,project_id,name,color,kind,enabled,created_at FROM calendars WHERE project_id=? ORDER BY id`, p)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Calendar{}
	for rows.Next() {
		var c Calendar
		var en int
		if err := rows.Scan(&c.ID, &c.ProjectID, &c.Name, &c.Color, &c.Kind, &en, &c.CreatedAt); err != nil {
			return nil, err
		}
		c.Enabled = en == 1
		out = append(out, c)
	}
	return map[string]any{"calendars": out}, rows.Err()
}
func (a *App) toolCalendarsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	p, err := project(ctx)
	if err != nil {
		return nil, err
	}
	name := strings.TrimSpace(strArg(args, "name", ""))
	color := strArg(args, "color", "#3b82f6")
	kind := strArg(args, "kind", "custom")
	enabled := true
	if raw, ok := args["enabled"]; ok {
		var valid bool
		enabled, valid = raw.(bool)
		if !valid {
			return nil, errors.New("enabled must be boolean")
		}
	}
	if err := validateCalendar(name, color, kind); err != nil {
		return nil, err
	}
	mutationMu.Lock()
	defer mutationMu.Unlock()
	res, err := ctx.AppDB().Exec(`INSERT INTO calendars(project_id,name,color,kind,enabled) VALUES(?,?,?,?,?)`, p, name, color, kind, boolToInt(enabled))
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	ctx.Emit("calendar.created", map[string]any{"calendar_id": id})
	return getCalendar(ctx, id)
}
func (a *App) toolCalendarsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	mutationMu.Lock()
	defer mutationMu.Unlock()
	id := int64(intArg(args, "id", 0))
	c, err := getCalendar(ctx, id)
	if err != nil {
		return nil, err
	}
	changed := false
	for _, field := range []string{"name", "color", "kind"} {
		if v, ok := args[field]; ok {
			s, ok := v.(string)
			if !ok {
				return nil, fmt.Errorf("%s must be a string", field)
			}
			switch field {
			case "name":
				c.Name = strings.TrimSpace(s)
			case "color":
				c.Color = s
			case "kind":
				c.Kind = s
			}
			changed = true
		}
	}
	if v, ok := args["enabled"]; ok {
		b, ok := v.(bool)
		if !ok {
			return nil, errors.New("enabled must be boolean")
		}
		c.Enabled = b
		changed = true
	}
	if !changed {
		return nil, errors.New("no updatable fields supplied")
	}
	if err := validateCalendar(c.Name, c.Color, c.Kind); err != nil {
		return nil, err
	}
	_, err = ctx.AppDB().Exec(`UPDATE calendars SET name=?,color=?,kind=?,enabled=? WHERE id=? AND project_id=?`, c.Name, c.Color, c.Kind, boolToInt(c.Enabled), id, c.ProjectID)
	if err != nil {
		return nil, err
	}
	ctx.Emit("calendar.updated", map[string]any{"calendar_id": id})
	return getCalendar(ctx, id)
}
func (a *App) toolCalendarsDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	mutationMu.Lock()
	defer mutationMu.Unlock()
	id := int64(intArg(args, "id", 0))
	c, err := getCalendar(ctx, id)
	if err != nil {
		return nil, err
	}
	_, err = ctx.AppDB().Exec(`DELETE FROM calendars WHERE id=? AND project_id=?`, id, c.ProjectID)
	if err != nil {
		return nil, err
	}
	ctx.Emit("calendar.deleted", map[string]any{"calendar_id": id})
	return map[string]any{"deleted": id}, nil
}

const eventColumns = `e.id,e.calendar_id,e.title,e.description,e.location,e.start_at,e.end_at,e.all_day,e.status,e.rrule,e.exdate,COALESCE(e.parent_event_id,0),COALESCE(e.occurrence_start_at,''),e.created_at,e.updated_at,e.timezone`

type scanner interface{ Scan(...any) error }

func scanEvent(row scanner) (Event, error) {
	var e Event
	var ad int
	var ex string
	err := row.Scan(&e.ID, &e.CalendarID, &e.Title, &e.Description, &e.Location, &e.StartAt, &e.EndAt, &ad, &e.Status, &e.RRule, &ex, &e.ParentEventID, &e.OccurrenceStartAt, &e.CreatedAt, &e.UpdatedAt, &e.Timezone)
	if err != nil {
		return e, err
	}
	e.AllDay = ad == 1
	if err = json.Unmarshal([]byte(ex), &e.ExDate); err != nil {
		return e, err
	}
	return e, nil
}
func eventIn(db dbReader, p string, id int64) (Event, error) {
	return scanEvent(db.QueryRow(`SELECT `+eventColumns+` FROM events e JOIN calendars c ON c.id=e.calendar_id WHERE e.id=? AND c.project_id=?`, id, p))
}
func readEvent(ctx *sdk.AppCtx, id int64) (Event, error) {
	p, err := project(ctx)
	if err != nil {
		return Event{}, err
	}
	return eventIn(ctx.AppDB(), p, id)
}
func (a *App) toolEventsGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return readEvent(ctx, int64(intArg(args, "event_id", 0)))
}
func patchEvent(e Event, args map[string]any) (Event, error) {
	for _, f := range []string{"title", "description", "location", "start_at", "end_at", "rrule", "timezone", "status"} {
		if raw, ok := args[f]; ok {
			v, ok := raw.(string)
			if !ok {
				return e, fmt.Errorf("%s must be a string", f)
			}
			switch f {
			case "title":
				e.Title = strings.TrimSpace(v)
			case "description":
				e.Description = v
			case "location":
				e.Location = v
			case "start_at":
				e.StartAt = v
			case "end_at":
				e.EndAt = v
			case "rrule":
				e.RRule = v
			case "timezone":
				e.Timezone = v
			case "status":
				e.Status = v
			}
		}
	}
	if raw, ok := args["all_day"]; ok {
		v, ok := raw.(bool)
		if !ok {
			return e, errors.New("all_day must be boolean")
		}
		e.AllDay = v
	}
	if _, ok := args["calendar_id"]; ok {
		e.CalendarID = int64(intArg(args, "calendar_id", 0))
	}
	return validateEvent(e)
}
func validateEvent(e Event) (Event, error) {
	if strings.TrimSpace(e.Title) == "" || len(e.Title) > 500 {
		return e, errors.New("title required (max 500 characters)")
	}
	if len(e.Description) > 100000 || len(e.Location) > 2000 {
		return e, errors.New("description or location too long")
	}
	if e.Timezone == "" {
		e.Timezone = "UTC"
	}
	if _, err := time.LoadLocation(e.Timezone); err != nil {
		return e, errors.New("timezone must be a valid IANA zone")
	}
	start, err := parseFlexibleTime(e.StartAt)
	if err != nil {
		return e, fmt.Errorf("start_at: %w", err)
	}
	end, err := parseFlexibleTime(e.EndAt)
	if err != nil {
		return e, fmt.Errorf("end_at: %w", err)
	}
	start = start.Truncate(time.Second)
	end = end.Truncate(time.Second)
	if start.Year() < 1900 || end.Year() > 2200 || !end.After(start) || end.Sub(start) > 3660*24*time.Hour {
		return e, errors.New("event must have a positive duration of at most 10 years, between 1900 and 2200")
	}
	if e.AllDay && (start.Hour() != 0 || start.Minute() != 0 || start.Second() != 0 || end.Hour() != 0 || end.Minute() != 0 || end.Second() != 0) {
		return e, errors.New("all-day events require date-only or UTC-midnight boundaries (exclusive end)")
	}
	e.StartAt = start.Format(time.RFC3339)
	e.EndAt = end.Format(time.RFC3339)
	if e.RRule != "" {
		if _, err := parseRRule(e.RRule); err != nil {
			return e, err
		}
	}
	switch e.Status {
	case "confirmed", "tentative", "cancelled":
	default:
		return e, errors.New("invalid event status")
	}
	return e, nil
}
func insertEvent(tx *sql.Tx, e Event) (int64, error) {
	ex, _ := json.Marshal(e.ExDate)
	if e.ExDate == nil {
		ex = []byte("[]")
	}
	var parent, occ any
	if e.ParentEventID != 0 {
		parent = e.ParentEventID
		occ = e.OccurrenceStartAt
	}
	res, err := tx.Exec(`INSERT INTO events(calendar_id,title,description,location,start_at,end_at,all_day,status,rrule,exdate,parent_event_id,occurrence_start_at,timezone) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?)`, e.CalendarID, e.Title, e.Description, e.Location, e.StartAt, e.EndAt, boolToInt(e.AllDay), e.Status, e.RRule, string(ex), parent, occ, e.Timezone)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}
func writeEvent(tx *sql.Tx, e Event) error {
	ex, _ := json.Marshal(e.ExDate)
	if e.ExDate == nil {
		ex = []byte("[]")
	}
	_, err := tx.Exec(`UPDATE events SET calendar_id=?,title=?,description=?,location=?,start_at=?,end_at=?,all_day=?,status=?,rrule=?,exdate=?,timezone=?,updated_at=? WHERE id=?`, e.CalendarID, e.Title, e.Description, e.Location, e.StartAt, e.EndAt, boolToInt(e.AllDay), e.Status, e.RRule, string(ex), e.Timezone, time.Now().UTC().Format(time.RFC3339), e.ID)
	return err
}
func (a *App) toolEventsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	p, err := project(ctx)
	if err != nil {
		return nil, err
	}
	e, err := patchEvent(Event{Status: "confirmed", Timezone: "UTC"}, args)
	if err != nil {
		return nil, err
	}
	mutationMu.Lock()
	defer mutationMu.Unlock()
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := calendarIn(tx, p, e.CalendarID); err != nil {
		return nil, errors.New("calendar_id not found in this project")
	}
	id, err := insertEvent(tx, e)
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	ctx.Emit("event.created", map[string]any{"event_id": id})
	return readEvent(ctx, id)
}
