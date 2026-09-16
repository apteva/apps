package main

import (
	"database/sql"
	"encoding/json"
	"sort"
	"strings"
	"time"
)

// CalendarEvent is one dated thing on the planning calendar: a content item on
// its chosen date field, or a channel release on its planned date. The shape is
// flat and pre-merged because the dashboard widget renders it directly and has
// no room to page through items and stitch their releases back together.
type CalendarEvent struct {
	Kind string `json:"kind"`
	// Date is the server-side bucket used for windowing; At is the value as
	// stored. A client that renders in the viewer's timezone buckets on At and
	// pads its window by a day, which is what the panel already does.
	Date      string `json:"date"`
	At        string `json:"at"`
	ItemID    int64  `json:"item_id"`
	ReleaseID int64  `json:"release_id,omitempty"`
	Title     string `json:"title"`
	BrandID   string `json:"brand_id"`
	Format    string `json:"format"`
	Status    string `json:"status"`
	Approval  string `json:"approval"`
	Owner     string `json:"owner"`
	Channel   string `json:"channel,omitempty"`
	URL       string `json:"url,omitempty"`
}

// A window wider than this is a client bug, not a calendar. Bounding it keeps a
// widget render from walking the whole table.
const calendarMaxDays = 400

// Stored dates are YYYY-MM-DD or RFC3339; both share a sortable date prefix, so
// windowing compares that prefix rather than parsing every row. The prefix of an
// RFC3339 value is its date in the stored offset, which can differ by a day from
// the viewer's local date — callers that care pad the window and re-bucket
// locally, as the panel already does when rendering.
func dayPart(s string) string {
	if len(s) >= 10 {
		return s[:10]
	}
	return s
}

func calendarWindow(args map[string]any) (string, string, error) {
	from, to := dayPart(strings.TrimSpace(str(args, "from"))), dayPart(strings.TrimSpace(str(args, "to")))
	if from == "" {
		from = time.Now().UTC().Format("2006-01-02")
	}
	start, e := time.Parse("2006-01-02", from)
	if e != nil {
		return "", "", invalid("from must be YYYY-MM-DD or RFC3339")
	}
	if to == "" {
		to = start.AddDate(0, 0, 30).Format("2006-01-02")
	}
	end, e := time.Parse("2006-01-02", to)
	if e != nil {
		return "", "", invalid("to must be YYYY-MM-DD or RFC3339")
	}
	if end.Before(start) {
		return "", "", invalid("to cannot be before from")
	}
	if end.Sub(start) > calendarMaxDays*24*time.Hour {
		return "", "", invalid("the calendar window cannot exceed 400 days")
	}
	return from, to, nil
}

func boolArg(args map[string]any, k string, fallback bool) bool {
	switch v := args[k].(type) {
	case bool:
		return v
	case string:
		if v == "" {
			return fallback
		}
		return v == "true" || v == "1"
	}
	return fallback
}

// calendarEvents answers "what lands between these two dates" in one query pair.
// Releases are matched on their own planned date and joined back to their parent,
// so a release inside the window still appears when its item's own date sits
// outside it — the reason this cannot be expressed as a filter on /items.
func calendarEvents(db *sql.DB, pid string, args map[string]any) (any, error) {
	from, to, e := calendarWindow(args)
	if e != nil {
		return nil, e
	}
	field := strings.TrimSpace(str(args, "date_field"))
	if field == "" {
		field = "planned_at"
	}
	if field != "planned_at" && field != "deadline" {
		return nil, invalid("date_field must be planned_at or deadline")
	}
	limit := number(args, "limit")
	if limit <= 0 {
		limit = 500
	}
	if limit > 2000 {
		limit = 2000
	}

	brandWhere, brandParams := "", []any{}
	if brand := str(args, "brand_id"); brand != "" {
		if brand == "unassigned" {
			brand = ""
		}
		brandWhere = " AND COALESCE(json_extract(%s,'$.brand_id'),'')=?"
		brandParams = append(brandParams, brand)
	}

	events := []CalendarEvent{}
	itemSQL := "SELECT id,data FROM editorial_items WHERE project_id=? AND json_extract(data,'$.archived')=?" +
		" AND substr(COALESCE(json_extract(data,'$." + field + "'),''),1,10) BETWEEN ? AND ?"
	params := []any{pid, false, from, to}
	if brandWhere != "" {
		itemSQL += strings.Replace(brandWhere, "%s", "data", 1)
		params = append(params, brandParams...)
	}
	rows, e := db.Query(itemSQL+" ORDER BY id LIMIT ?", append(params, limit+1)...)
	if e != nil {
		return nil, e
	}
	for rows.Next() {
		var id int64
		var raw string
		if e = rows.Scan(&id, &raw); e != nil {
			rows.Close()
			return nil, e
		}
		var d ItemData
		if e = json.Unmarshal([]byte(raw), &d); e != nil {
			rows.Close()
			return nil, e
		}
		date := d.PlannedAt
		if field == "deadline" {
			date = d.Deadline
		}
		events = append(events, CalendarEvent{Kind: "item", Date: dayPart(date), At: date, ItemID: id, Title: d.Title,
			BrandID: d.BrandID, Format: d.Format, Status: d.Status, Approval: d.Approval, Owner: d.Owner})
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return nil, e
	}

	// Releases carry publication dates, so they belong to a publication calendar
	// and not to a deadline one. The panel draws them under the same rule.
	if field == "planned_at" && boolArg(args, "include_releases", true) {
		releaseSQL := "SELECT r.id,r.item_id,r.data,i.data FROM editorial_releases r" +
			" JOIN editorial_items i ON i.project_id=r.project_id AND i.id=r.item_id" +
			" WHERE r.project_id=? AND json_extract(r.data,'$.archived')=? AND json_extract(i.data,'$.archived')=?" +
			" AND substr(COALESCE(json_extract(r.data,'$.planned_at'),''),1,10) BETWEEN ? AND ?"
		params = []any{pid, false, false, from, to}
		if brandWhere != "" {
			releaseSQL += strings.Replace(brandWhere, "%s", "i.data", 1)
			params = append(params, brandParams...)
		}
		rows, e = db.Query(releaseSQL+" ORDER BY r.id LIMIT ?", append(params, limit+1)...)
		if e != nil {
			return nil, e
		}
		for rows.Next() {
			var id, itemID int64
			var rawRelease, rawItem string
			if e = rows.Scan(&id, &itemID, &rawRelease, &rawItem); e != nil {
				rows.Close()
				return nil, e
			}
			var rd ReleaseData
			var id2 ItemData
			if e = json.Unmarshal([]byte(rawRelease), &rd); e != nil {
				rows.Close()
				return nil, e
			}
			if e = json.Unmarshal([]byte(rawItem), &id2); e != nil {
				rows.Close()
				return nil, e
			}
			events = append(events, CalendarEvent{Kind: "release", Date: dayPart(rd.PlannedAt), At: rd.PlannedAt, ItemID: itemID,
				ReleaseID: id, Title: id2.Title, BrandID: id2.BrandID, Format: id2.Format, Status: rd.Status,
				Approval: id2.Approval, Owner: id2.Owner, Channel: rd.Channel, URL: rd.URL})
		}
		e = rows.Err()
		rows.Close()
		if e != nil {
			return nil, e
		}
	}

	sort.SliceStable(events, func(x, y int) bool {
		if events[x].Date != events[y].Date {
			return events[x].Date < events[y].Date
		}
		if events[x].Kind != events[y].Kind {
			return events[x].Kind < events[y].Kind
		}
		if events[x].ItemID != events[y].ItemID {
			return events[x].ItemID < events[y].ItemID
		}
		return events[x].ReleaseID < events[y].ReleaseID
	})
	truncated := int64(len(events)) > limit
	if truncated {
		events = events[:limit]
	}
	return map[string]any{"events": events, "from": from, "to": to, "date_field": field, "truncated": truncated}, nil
}
