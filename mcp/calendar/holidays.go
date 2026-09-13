package main

import (
	"database/sql"
	"errors"
	sdk "github.com/apteva/app-sdk"
	"strings"
	"time"
)

func (a *App) toolHolidaysSet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	p, err := project(ctx)
	if err != nil {
		return nil, err
	}
	year := intArg(args, "year", 0)
	if year < 1900 || year > 2199 {
		return nil, errors.New("year must be 1900–2199")
	}
	country := strings.ToUpper(strArg(args, "country", ""))
	dates, err := holidayDates(year, country)
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
	var id int64
	err = tx.QueryRow(`SELECT id FROM calendars WHERE project_id=? AND holiday_country=?`, p, country).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		// Adopt only an exactly named legacy country calendar; never combine countries.
		err = tx.QueryRow(`SELECT id FROM calendars WHERE project_id=? AND kind='holidays' AND name=? AND holiday_country='' ORDER BY id LIMIT 1`, p, "Holidays — "+country).Scan(&id)
		if err == nil {
			_, err = tx.Exec(`UPDATE calendars SET holiday_country=? WHERE id=?`, country, id)
		} else if errors.Is(err, sql.ErrNoRows) {
			var res sql.Result
			res, err = tx.Exec(`INSERT INTO calendars(project_id,name,color,kind,holiday_country) VALUES(?,?,'#94a3b8','holidays',?)`, p, "Holidays — "+country, country)
			if err == nil {
				id, err = res.LastInsertId()
			}
		}
	}
	if err != nil {
		return nil, err
	}
	created := 0
	for _, h := range dates {
		var n int
		start := h.date.Format(time.RFC3339)
		if err := tx.QueryRow(`SELECT COUNT(*) FROM events WHERE calendar_id=? AND title=? AND start_at=?`, id, h.name, start).Scan(&n); err != nil {
			return nil, err
		}
		if n > 0 {
			continue
		}
		if _, err := insertEvent(tx, Event{CalendarID: id, Title: h.name, StartAt: start, EndAt: h.date.AddDate(0, 0, 1).Format(time.RFC3339), AllDay: true, Status: "confirmed", Timezone: "UTC"}); err != nil {
			return nil, err
		}
		created++
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	ctx.Emit("holidays.set", map[string]any{"calendar_id": id, "created": created})
	return map[string]any{"calendar_id": id, "created": created, "country": country, "year": year}, nil
}
