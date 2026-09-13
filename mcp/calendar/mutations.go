package main

import (
	"database/sql"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"time"
)

func appendExdate(e *Event, at string) {
	for _, x := range e.ExDate {
		if x == at {
			return
		}
	}
	e.ExDate = append(e.ExDate, at)
}
func hasPatch(args map[string]any) bool {
	for _, k := range []string{"title", "description", "location", "start_at", "end_at", "all_day", "rrule", "timezone", "calendar_id", "status"} {
		if _, ok := args[k]; ok {
			return true
		}
	}
	return false
}
func (a *App) toolEventsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	p, err := project(ctx)
	if err != nil {
		return nil, err
	}
	if !hasPatch(args) {
		return nil, errors.New("no updatable fields supplied")
	}
	mutationMu.Lock()
	defer mutationMu.Unlock()
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	master, err := eventIn(tx, p, int64(intArg(args, "event_id", 0)))
	if err != nil {
		return nil, err
	}
	scope := strArg(args, "scope", "")
	if scope == "" {
		scope = "all"
		if master.RRule != "" {
			scope = "this"
		}
	}
	target := master
	id := master.ID
	if scope == "this" || scope == "this_and_following" {
		if master.RRule == "" {
			return nil, errors.New("occurrence scope requires a recurring master")
		}
		at, err := parseFlexibleTime(strArg(args, "occurrence_start_at", ""))
		if err != nil {
			return nil, err
		}
		index, err := occurrenceIndex(master, at)
		if err != nil {
			return nil, err
		}
		key := at.Format(time.RFC3339)
		ms, _ := parseFlexibleTime(master.StartAt)
		me, _ := parseFlexibleTime(master.EndAt)
		target.StartAt = key
		target.EndAt = at.Add(me.Sub(ms)).Format(time.RFC3339)
		target.ExDate = nil
		if scope == "this" {
			target.RRule = ""
			target.ParentEventID = master.ID
			target.OccurrenceStartAt = key
			// Reuse an existing override so network retries and concurrent edits update
			// one row rather than appending additional occurrences.
			existing, readErr := scanEvent(tx.QueryRow(`SELECT `+eventColumns+` FROM events e WHERE parent_event_id=? AND occurrence_start_at=?`, master.ID, key))
			if readErr != nil && !errors.Is(readErr, sql.ErrNoRows) {
				return nil, readErr
			}
			if readErr == nil {
				target = existing
			}
			if value, ok := args["rrule"]; ok && value != "" {
				return nil, errors.New("recurrence can only be changed for the series")
			}
			target, err = patchEvent(target, args)
			if err != nil {
				return nil, err
			}
			if _, err := calendarIn(tx, p, target.CalendarID); err != nil {
				return nil, err
			}
			appendExdate(&master, key)
			if err := writeEvent(tx, master); err != nil {
				return nil, err
			}
			if readErr == nil {
				id = target.ID
				err = writeEvent(tx, target)
			} else {
				id, err = insertEvent(tx, target)
			}
			if err != nil {
				return nil, err
			}
		} else {
			// Preserve the number remaining unless a replacement rule was explicitly supplied.
			opt, err := parseRRule(master.RRule)
			if err != nil {
				return nil, err
			}
			if opt.Count > 0 {
				target.RRule = setRRulePart(target.RRule, "COUNT", fmt.Sprint(opt.Count-index))
			}
			target, err = patchEvent(target, args)
			if err != nil {
				return nil, err
			}
			if _, err := calendarIn(tx, p, target.CalendarID); err != nil {
				return nil, err
			}
			id, err = insertEvent(tx, target)
			if err != nil {
				return nil, err
			}
			target.ID = id
			prior := master
			prior.RRule = truncateRule(master.RRule, at)
			if err := redistributeExceptions(tx, master, &prior, &target, index); err != nil {
				return nil, err
			}
			if err := writeEvent(tx, prior); err != nil {
				return nil, err
			}
			if err := writeEvent(tx, target); err != nil {
				return nil, err
			}
		}
	} else if scope == "all" {
		target, err = patchEvent(master, args)
		if err != nil {
			return nil, err
		}
		if _, err := calendarIn(tx, p, target.CalendarID); err != nil {
			return nil, err
		}
		if master.RRule != "" {
			if err := redistributeExceptions(tx, master, nil, &target, 0); err != nil {
				return nil, err
			}
		}
		if err := writeEvent(tx, target); err != nil {
			return nil, err
		}
	} else {
		return nil, errors.New("unknown scope")
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	ctx.Emit("event.updated", map[string]any{"event_id": id, "scope": scope})
	return readEvent(ctx, id)
}

// Rebase exclusions by ordinal, not by elapsed seconds: calendar recurrences can
// cross DST and change their weekday rule. Overrides retain their explicit time
// and content, while their original occurrence identity follows the new series.
func redistributeExceptions(tx *sql.Tx, original Event, prior *Event, next *Event, offset int) error {
	rows, err := tx.Query(`SELECT `+eventColumns+` FROM events e WHERE parent_event_id=?`, original.ID)
	if err != nil {
		return err
	}
	children := []Event{}
	for rows.Next() {
		e, err := scanEvent(rows)
		if err != nil {
			rows.Close()
			return err
		}
		children = append(children, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	indices := map[string]int{}
	maxIndex := -1
	for _, key := range original.ExDate {
		at, err := parseFlexibleTime(key)
		if err != nil {
			return err
		}
		index, err := occurrenceIndex(original, at)
		if err != nil {
			return err
		}
		indices[key] = index
		if index-offset > maxIndex {
			maxIndex = index - offset
		}
	}
	for _, e := range children {
		if _, ok := indices[e.OccurrenceStartAt]; !ok {
			at, err := parseFlexibleTime(e.OccurrenceStartAt)
			if err != nil {
				return err
			}
			index, err := occurrenceIndex(original, at)
			if err != nil {
				return err
			}
			indices[e.OccurrenceStartAt] = index
			if index-offset > maxIndex {
				maxIndex = index - offset
			}
		}
	}
	dates := map[int]string{}
	if maxIndex >= 0 && next.RRule != "" {
		rule, err := ruleFor(*next, time.Date(2200, 12, 31, 23, 59, 59, 0, time.UTC), time.Time{})
		if err != nil {
			return err
		}
		iter := rule.Iterator()
		for i := 0; i <= maxIndex; i++ {
			at, ok := iter()
			if !ok {
				break
			}
			dates[i] = at.UTC().Format(time.RFC3339)
		}
	}
	next.ExDate = nil
	if prior != nil {
		prior.ExDate = nil
	}
	for key, index := range indices {
		if index < offset {
			if prior != nil {
				appendExdate(prior, key)
			}
		} else if date, ok := dates[index-offset]; ok {
			appendExdate(next, date)
		}
	}
	// Temporarily free the unique occurrence keys before shifts can collide.
	for _, e := range children {
		if indices[e.OccurrenceStartAt] >= offset {
			if _, err := tx.Exec(`UPDATE events SET occurrence_start_at=? WHERE id=?`, fmt.Sprintf("rebase:%d", e.ID), e.ID); err != nil {
				return err
			}
		}
	}
	for _, e := range children {
		index := indices[e.OccurrenceStartAt]
		if index < offset {
			continue
		}
		date, ok := dates[index-offset]
		if !ok {
			if _, err := tx.Exec(`DELETE FROM events WHERE id=?`, e.ID); err != nil {
				return err
			}
			continue
		}
		if _, err := tx.Exec(`UPDATE events SET parent_event_id=?,occurrence_start_at=?,calendar_id=? WHERE id=?`, next.ID, date, next.CalendarID, e.ID); err != nil {
			return err
		}
	}
	return nil
}
func (a *App) toolEventsDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	p, err := project(ctx)
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
	e, err := eventIn(tx, p, int64(intArg(args, "event_id", 0)))
	if err != nil {
		return nil, err
	}
	scope := strArg(args, "scope", "all")
	switch scope {
	case "all":
		_, err = tx.Exec(`DELETE FROM events WHERE id=?`, e.ID)
	case "this", "this_and_following":
		if e.RRule == "" {
			if scope == "this_and_following" {
				return nil, errors.New("following scope requires a recurring event")
			}
			_, err = tx.Exec(`DELETE FROM events WHERE id=?`, e.ID)
			break
		}
		at, err2 := parseFlexibleTime(strArg(args, "occurrence_start_at", ""))
		if err2 != nil {
			return nil, err2
		}
		if _, err2 := occurrenceIndex(e, at); err2 != nil {
			return nil, err2
		}
		key := at.Format(time.RFC3339)
		if scope == "this" {
			appendExdate(&e, key)
			_, err = tx.Exec(`DELETE FROM events WHERE parent_event_id=? AND occurrence_start_at=?`, e.ID, key)
		} else {
			e.RRule = truncateRule(e.RRule, at)
			kept := []string{}
			for _, x := range e.ExDate {
				if x < key {
					kept = append(kept, x)
				}
			}
			e.ExDate = kept
			_, err = tx.Exec(`DELETE FROM events WHERE parent_event_id=? AND occurrence_start_at>=?`, e.ID, key)
		}
		if err == nil {
			err = writeEvent(tx, e)
		}
	default:
		return nil, errors.New("unknown scope")
	}
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	ctx.Emit("event.deleted", map[string]any{"event_id": e.ID, "scope": scope})
	return map[string]any{"deleted": e.ID}, nil
}
