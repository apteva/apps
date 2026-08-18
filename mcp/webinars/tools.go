package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// ─── webinars_create ─────────────────────────────────────────────

func (a *App) toolCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	title := strings.TrimSpace(strArg(args, "title"))
	if title == "" {
		return nil, errors.New("title required")
	}
	kind := strArg(args, "kind")
	if kind == "" {
		kind = "scheduled"
	}
	if kind != "live" && kind != "scheduled" && kind != "replay" {
		return nil, fmt.Errorf("kind must be live|scheduled|replay, got %q", kind)
	}
	scheduledAt := strings.TrimSpace(strArg(args, "scheduled_at"))
	if scheduledAt != "" {
		norm, err := normalizeRFC3339(scheduledAt)
		if err != nil {
			return nil, fmt.Errorf("scheduled_at must be RFC3339: %w", err)
		}
		scheduledAt = norm
	}
	durationMinutes := intArg(args, "duration_minutes", 60)
	if durationMinutes <= 0 {
		durationMinutes = 60
	}
	schedulingMode := strArg(args, "scheduling_mode")
	if schedulingMode == "" {
		schedulingMode = "single"
	}
	if schedulingMode != "single" && schedulingMode != "multi" && schedulingMode != "evergreen" && schedulingMode != "replay" {
		return nil, fmt.Errorf("scheduling_mode must be single|multi|evergreen|replay, got %q", schedulingMode)
	}
	timezone := strArg(args, "timezone")
	if timezone == "" {
		timezone = "UTC"
	}
	slotDuration := intArg(args, "slot_duration_minutes", durationMinutes)
	if slotDuration <= 0 {
		slotDuration = durationMinutes
	}

	slug := uniqueSlug(ctx, pid, slugify(title))

	res, err := ctx.AppDB().Exec(
		`INSERT INTO webinars
			(project_id, slug, title, host_name, description, kind,
			 scheduled_at, duration_minutes, scheduling_mode, timezone,
			 slot_duration_minutes, registration_policy, status, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'draft', ?)`,
		pid, slug, title,
		nullStr(strArg(args, "host_name")),
		nullStr(strArg(args, "description")),
		kind,
		nullStr(scheduledAt),
		durationMinutes,
		schedulingMode,
		timezone,
		slotDuration,
		nullStr(strArg(args, "registration_policy")),
		nowRFC3339())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()

	// Allocate the stream. Failure here rolls back the row so we don't
	// leave webinars dangling without a pipe.
	created, sErr := a.streamingCaller.CreateStream(CreateStreamReq{
		Name:      title,
		OwnerApp:  "webinars",
		OwnerTag:  fmt.Sprintf("webinar:%d", id),
		Record:    true,
		ProjectID: pid,
	})
	if sErr != nil {
		_, _ = ctx.AppDB().Exec(`DELETE FROM webinars WHERE id = ?`, id)
		return nil, fmt.Errorf("streaming.streams_create: %w", sErr)
	}
	if _, err := ctx.AppDB().Exec(
		`UPDATE webinars SET stream_id = ?,
			status = CASE WHEN scheduled_at IS NULL THEN 'draft' ELSE 'scheduled' END
		 WHERE id = ?`,
		created.Stream.ID, id); err != nil {
		return nil, err
	}
	if scheduledAt != "" && schedulingMode == "single" {
		if _, err := a.createSlot(ctx, pid, id, scheduledAt, "", timezone, slotDuration, 0, "scheduled", "scheduled_at", ""); err != nil {
			return nil, err
		}
	}

	w, _ := a.dbGet(ctx, pid, id)
	// Retire the static ?t=<playback_token> form for streams we own, so
	// the only way to watch is a signed URL this app minted with a
	// bounded TTL. Best-effort — see playback.go.
	a.EnforceSignedPlayback(ctx, w)
	a.materialize(ctx, w, &created.Stream)
	ctx.Emit("webinar.created", map[string]any{"id": id, "slug": slug})
	return map[string]any{"webinar": w}, nil
}

// ─── webinars_create_slot / webinars_list_slots ──────────────────

func (a *App) toolCreateSlot(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	webinarID := int64Arg(args, "webinar_id")
	startsAt := strArg(args, "starts_at")
	if webinarID == 0 || startsAt == "" {
		return nil, errors.New("webinar_id and starts_at required")
	}
	w, err := a.dbGet(ctx, pid, webinarID)
	if err != nil || w == nil {
		return nil, errors.New("webinar not found")
	}
	endsAt := strArg(args, "ends_at")
	timezone := strArg(args, "timezone")
	if timezone == "" {
		timezone = w.Timezone
	}
	if timezone == "" {
		timezone = "UTC"
	}
	duration := intArg(args, "duration_minutes", w.SlotDurationMin)
	if duration <= 0 {
		duration = w.DurationMinutes
	}
	capacity := intArg(args, "capacity", 0)
	status := strArg(args, "status")
	if status == "" {
		status = "scheduled"
	}
	slot, err := a.createSlot(ctx, pid, webinarID, startsAt, endsAt, timezone, duration, capacity, status, "manual", "")
	if err != nil {
		return nil, err
	}
	return map[string]any{"slot": slot}, nil
}

func (a *App) toolListSlots(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	webinarID := int64Arg(args, "webinar_id")
	if webinarID == 0 {
		return nil, errors.New("webinar_id required")
	}
	if w, err := a.dbGet(ctx, pid, webinarID); err != nil || w == nil {
		return nil, errors.New("webinar not found")
	}
	slots, err := a.dbListSlots(ctx, pid, webinarID, strArg(args, "from"), strArg(args, "to"), boolArg(args, "available_only", false))
	if err != nil {
		return nil, err
	}
	return map[string]any{"slots": slots, "count": len(slots)}, nil
}

// uniqueSlug ensures the slug is unique per project. Tries base, then
// base-2 … base-100, then falls back to a random suffix. SQLite-side
// uniqueness still enforces it.
//
// The old loop assigned base-99 as the candidate and fell out of the
// loop without ever testing it, so the random-suffix path could trigger
// one collision early.
func uniqueSlug(ctx *sdk.AppCtx, pid, base string) string {
	if base == "" {
		base = randomToken()[:8]
	}
	for n := 1; n <= 100; n++ {
		candidate := base
		if n > 1 {
			candidate = fmt.Sprintf("%s-%d", base, n)
		}
		var exists int
		_ = ctx.AppDB().QueryRow(
			`SELECT COUNT(*) FROM webinars WHERE project_id = ? AND slug = ?`,
			pid, candidate).Scan(&exists)
		if exists == 0 {
			return candidate
		}
	}
	// Last-ditch: append a random suffix.
	return base + "-" + randomToken()[:6]
}

// ─── webinars_get ────────────────────────────────────────────────

func (a *App) toolGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	w, err := a.dbGet(ctx, pid, id)
	if err != nil {
		return nil, err
	}
	if w == nil {
		return map[string]any{"webinar": nil, "found": false}, nil
	}

	// Fetch stream snapshot — best-effort. A webinar may exist with
	// stream_id pointing at a deleted stream; surface what we have.
	var snap *StreamSnapshot
	if w.StreamID != 0 {
		s, err := a.streamingCaller.GetStream(w.StreamID)
		if err == nil {
			snap = &s
		}
	}
	a.materialize(ctx, w, snap)

	// Counts.
	regCount := 0
	_ = ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM webinar_registrants WHERE webinar_id = ?`, id).Scan(&regCount)
	attendedLive := 0
	_ = ctx.AppDB().QueryRow(
		`SELECT COUNT(DISTINCT registrant_id) FROM webinar_attendance
		 WHERE webinar_id = ? AND source = 'live'`, id).Scan(&attendedLive)

	return map[string]any{
		"webinar":             w,
		"found":               true,
		"registrant_count":    regCount,
		"attended_live_count": attendedLive,
		"stream":              snap,
	}, nil
}

// ─── webinars_list ───────────────────────────────────────────────

func (a *App) toolList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	limit := intArg(args, "limit", 50)
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	where := []string{"project_id = ?"}
	qargs := []any{pid}
	if v := strArg(args, "status"); v != "" {
		where = append(where, "status = ?")
		qargs = append(qargs, v)
	}
	if v := strArg(args, "kind"); v != "" {
		where = append(where, "kind = ?")
		qargs = append(qargs, v)
	}
	if v := strArg(args, "scheduled_at_after"); v != "" {
		where = append(where, "scheduled_at >= ?")
		qargs = append(qargs, v)
	}
	if v := strArg(args, "scheduled_at_before"); v != "" {
		where = append(where, "scheduled_at <= ?")
		qargs = append(qargs, v)
	}
	qargs = append(qargs, limit)

	// One SELECT of every column, not "SELECT id" followed by a dbGet
	// per row. The SDK caps the pool at ONE connection, so the old
	// shape cost 2N+1 strictly sequential round-trips (up to 201 for a
	// 200-row page) with every other reader and the workers queued
	// behind it.
	rows, err := ctx.AppDB().Query(
		`SELECT `+webinarColumns+` FROM webinars WHERE `+strings.Join(where, " AND ")+
			` ORDER BY COALESCE(scheduled_at, created_at) DESC LIMIT ?`, qargs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []*Webinar{}
	for rows.Next() {
		w, err := scanWebinar(rows)
		if err != nil {
			return nil, err
		}
		a.materialize(ctx, w, nil)
		out = append(out, w)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{"webinars": out, "count": len(out)}, nil
}

// ─── webinars_update ─────────────────────────────────────────────

func (a *App) toolUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	patch, _ := args["patch"].(map[string]any)
	if id == 0 || patch == nil {
		return nil, errors.New("id and patch required")
	}
	w, err := a.dbGet(ctx, pid, id)
	if err != nil {
		return nil, err
	}
	if w == nil {
		return nil, errors.New("webinar not found")
	}

	p, err := buildWebinarPatch(patch, w)
	if err != nil {
		return nil, err
	}
	if len(p.sets) == 0 {
		return map[string]any{"webinar": w, "noop": true}, nil
	}

	qargs := append([]any{}, p.args...)
	qargs = append(qargs, id, pid)
	if _, err := ctx.AppDB().Exec(
		`UPDATE webinars SET `+strings.Join(p.sets, ", ")+
			` WHERE id = ? AND project_id = ?`, qargs...); err != nil {
		return nil, err
	}

	if p.cancel {
		if err := a.cancelWebinar(ctx, pid, w); err != nil {
			ctx.Logger().Warn("cancel webinar", "id", id, "err", err)
		}
		w, _ = a.dbGet(ctx, pid, id)
		a.materialize(ctx, w, nil)
		ctx.Emit("webinar.cancelled", map[string]any{"id": id})
		return map[string]any{"webinar": w}, nil
	}

	// A reschedule has to move the SLOT too. webinars_create materializes
	// a slot from scheduled_at in single mode, the registration page
	// renders from dbListSlots, and registration resolves reminder times
	// from the slot — so patching only the webinars row left the public
	// page advertising the old time and gave new registrants reminders
	// for a date that had moved.
	if p.scheduledChanged || p.durationChanged {
		if err := a.syncSingleModeSlot(ctx, pid, id); err != nil {
			ctx.Logger().Warn("sync scheduled_at slot", "id", id, "err", err)
		}
	}

	// If scheduled_at moved, regenerate pending reminders.
	if p.scheduledChanged {
		if err := a.regenerateReminders(ctx, pid, id); err != nil {
			ctx.Logger().Warn("regenerate reminders", "id", id, "err", err)
		}
	}

	w, _ = a.dbGet(ctx, pid, id)
	a.materialize(ctx, w, nil)
	ctx.Emit("webinar.updated", map[string]any{"id": id})
	return map[string]any{"webinar": w}, nil
}

// webinarPatch is the validated result of a webinars_update patch map.
type webinarPatch struct {
	sets             []string
	args             []any
	scheduledChanged bool
	durationChanged  bool
	cancel           bool
}

// buildWebinarPatch validates every patch key BEFORE anything is written.
//
// The old version passed values through verbatim: `{"scheduled_at":
// "next Tuesday"}` was stored as-is, and regenerateReminders — which had
// already DELETEd the pending rows by then — then failed its time.Parse
// and logged a warning, leaving the webinar with a garbage scheduled_at
// and zero reminders.
func buildWebinarPatch(patch map[string]any, w *Webinar) (webinarPatch, error) {
	out := webinarPatch{}
	// Deterministic order so the generated SQL is stable across runs.
	for _, key := range []string{
		"title", "host_name", "description", "kind",
		"scheduled_at", "duration_minutes", "status",
	} {
		raw, present := patch[key]
		if !present {
			continue
		}
		switch key {
		case "title":
			s, ok := patchString(raw)
			if !ok || strings.TrimSpace(s) == "" {
				return out, errors.New("title must be a non-empty string")
			}
			out.sets = append(out.sets, "title = ?")
			out.args = append(out.args, strings.TrimSpace(s))

		case "host_name", "description":
			s, ok := patchString(raw)
			if !ok {
				return out, fmt.Errorf("%s must be a string", key)
			}
			out.sets = append(out.sets, key+" = ?")
			out.args = append(out.args, nullStr(strings.TrimSpace(s)))

		case "kind":
			s, _ := patchString(raw)
			s = strings.TrimSpace(s)
			if s != "live" && s != "scheduled" && s != "replay" {
				return out, fmt.Errorf("kind must be live|scheduled|replay, got %q", s)
			}
			out.sets = append(out.sets, "kind = ?")
			out.args = append(out.args, s)

		case "scheduled_at":
			s, _ := patchString(raw)
			s = strings.TrimSpace(s)
			if s == "" {
				out.sets = append(out.sets, "scheduled_at = NULL")
			} else {
				norm, err := normalizeRFC3339(s)
				if err != nil {
					return out, fmt.Errorf("scheduled_at must be RFC3339: %w", err)
				}
				out.sets = append(out.sets, "scheduled_at = ?")
				out.args = append(out.args, norm)
			}
			out.scheduledChanged = true

		case "duration_minutes":
			n := intArg(patch, "duration_minutes", 0)
			if n <= 0 {
				return out, errors.New("duration_minutes must be a positive integer")
			}
			out.sets = append(out.sets, "duration_minutes = ?")
			out.args = append(out.args, n)
			out.durationChanged = true

		case "status":
			// Cancellation is the ONLY status transition update accepts.
			// webinars_close owns ended; live is driven by the stream
			// lifecycle; draft/scheduled follow scheduled_at.
			s, _ := patchString(raw)
			if strings.TrimSpace(s) != "cancelled" {
				return out, errors.New(
					`status may only be patched to "cancelled" — use webinars_close to end a webinar`)
			}
			if w.Status == "cancelled" {
				continue
			}
			out.sets = append(out.sets, "status = ?", "ended_at = ?")
			out.args = append(out.args, "cancelled", nowRFC3339())
			out.cancel = true
		}
	}

	// Mirror webinars_create's rule: a scheduled_at makes a draft
	// `scheduled`, and clearing it puts it back. Without this, a draft
	// given a valid time through update stayed status='draft' and
	// `webinars_list status=scheduled` never saw it.
	if out.scheduledChanged && !out.cancel && (w.Status == "draft" || w.Status == "scheduled") {
		next := "scheduled"
		if s, _ := patchString(patch["scheduled_at"]); strings.TrimSpace(s) == "" {
			next = "draft"
		}
		if next != w.Status {
			out.sets = append(out.sets, "status = ?")
			out.args = append(out.args, next)
		}
	}
	return out, nil
}

// patchString coerces a JSON patch value to a string. nil means "clear".
func patchString(v any) (string, bool) {
	switch t := v.(type) {
	case nil:
		return "", true
	case string:
		return t, true
	}
	return "", false
}

// cancelWebinar performs the side effects of a cancellation: stop the
// pipe, stand down every pending reminder, and close the slots so the
// registration page stops offering them.
func (a *App) cancelWebinar(ctx *sdk.AppCtx, pid string, w *Webinar) error {
	if w.StreamID != 0 {
		_ = a.streamingCaller.StopStream(w.StreamID)
	}
	if _, err := ctx.AppDB().Exec(
		`UPDATE webinar_reminders
		 SET status = 'skipped', sent_at = ?, error = 'webinar cancelled'
		 WHERE project_id = ? AND webinar_id = ? AND status = 'pending'`,
		nowRFC3339(), pid, w.ID); err != nil {
		return err
	}
	_, err := ctx.AppDB().Exec(
		`UPDATE webinar_slots SET status = 'cancelled', updated_at = ?
		 WHERE project_id = ? AND webinar_id = ?
		   AND status IN ('scheduled','open','live')`,
		nowRFC3339(), pid, w.ID)
	return err
}

// syncSingleModeSlot keeps the slot materialized from scheduled_at in
// step with the webinars row. Single-mode only — multi-slot webinars
// carry their times on the slots themselves and must not be rewritten
// from the webinar-level scheduled_at.
func (a *App) syncSingleModeSlot(ctx *sdk.AppCtx, pid string, webinarID int64) error {
	w, err := a.dbGet(ctx, pid, webinarID)
	if err != nil || w == nil {
		return err
	}
	if w.SchedulingMode != "" && w.SchedulingMode != "single" {
		return nil
	}

	duration := w.SlotDurationMin
	if duration <= 0 {
		duration = w.DurationMinutes
	}
	if duration <= 0 {
		duration = 60
	}

	var slotID int64
	var slotStatus string
	err = ctx.AppDB().QueryRow(
		`SELECT id, status FROM webinar_slots
		 WHERE project_id = ? AND webinar_id = ? AND source = 'scheduled_at'
		 ORDER BY id ASC LIMIT 1`, pid, webinarID).Scan(&slotID, &slotStatus)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	missing := errors.Is(err, sql.ErrNoRows)

	if w.ScheduledAt == "" {
		if missing {
			return nil
		}
		// The webinar lost its time — the slot it produced can't stay
		// registrable.
		_, uErr := ctx.AppDB().Exec(
			`UPDATE webinar_slots SET status = 'cancelled', updated_at = ?
			 WHERE id = ? AND status IN ('scheduled','open','live')`,
			nowRFC3339(), slotID)
		return uErr
	}

	start, pErr := parseDBTime(w.ScheduledAt)
	if pErr != nil {
		return pErr
	}
	startsAt := formatRFC3339(start)
	endsAt := formatRFC3339(start.Add(time.Duration(duration) * time.Minute))

	if missing {
		_, cErr := a.createSlot(ctx, pid, webinarID, startsAt, endsAt,
			w.Timezone, duration, 0, "scheduled", "scheduled_at", "")
		return cErr
	}

	// A slot the sweep already retired is legitimate again once the
	// webinar moves into the future.
	nextStatus := slotStatus
	if start.After(nowUTC()) && (slotStatus == "ended" || slotStatus == "cancelled") {
		nextStatus = "scheduled"
	}
	_, uErr := ctx.AppDB().Exec(
		`UPDATE webinar_slots
		 SET starts_at = ?, ends_at = ?, timezone = ?, status = ?, updated_at = ?
		 WHERE id = ?`,
		startsAt, endsAt, nullStr(w.Timezone), nextStatus, nowRFC3339(), slotID)
	return uErr
}

// ─── webinars_delete ─────────────────────────────────────────────

func (a *App) toolDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	w, err := a.dbGet(ctx, pid, id)
	if err != nil {
		return nil, err
	}
	if w == nil {
		return map[string]any{"deleted": true}, nil
	}

	if w.StreamID != 0 {
		_ = a.streamingCaller.DeleteStream(w.StreamID)
	}
	if _, err := ctx.AppDB().Exec(
		`DELETE FROM webinars WHERE id = ? AND project_id = ?`, id, pid); err != nil {
		return nil, err
	}
	ctx.Emit("webinar.deleted", map[string]any{"id": id})
	return map[string]any{"deleted": true}, nil
}

// ─── webinars_register ───────────────────────────────────────────

func (a *App) toolRegister(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	wid := int64Arg(args, "webinar_id")
	if wid == 0 {
		return nil, errors.New("webinar_id required")
	}
	// Strict, server-side validation. Registration is unauthenticated by
	// design and whatever lands here is later handed to messaging as the
	// literal `to` address for three reminders plus the we're-live
	// blast, so an unvalidated field is an email/SMS amplifier pointed
	// at the owner's credits and sender reputation.
	email, phone, err := NormalizeRegistrationContact(
		strArg(args, "email"), strArg(args, "phone"))
	if err != nil {
		return nil, err
	}

	w, err := a.dbGet(ctx, pid, wid)
	if err != nil {
		return nil, err
	}
	if w == nil {
		return nil, errors.New("webinar not found")
	}
	if w.Status == "cancelled" {
		return nil, errors.New("webinar is cancelled")
	}

	displayName := sanitizeDisplayName(strArg(args, "display_name"))
	source := strArg(args, "source")
	if source == "" {
		source = "agent"
	}

	// Per-webinar abuse budget. Enforced here rather than in the HTTP
	// handler so the MCP path is covered too. `source=import` is exempt:
	// it's the explicit bulk-load signal and is only reachable from an
	// authenticated MCP caller — the public form hardcodes source=form.
	if source != "import" {
		a.ensureState()
		if !a.registrations.allow(wid, a.registrationsPerMinute(ctx), nowUTC()) {
			return nil, errRegistrationRateLimited
		}
	}

	slotID := int64Arg(args, "slot_id")
	slot, err := a.resolveRegistrationSlot(ctx, pid, w, slotID)
	if err != nil {
		return nil, err
	}

	registrantID, created, err := a.upsertRegistrant(
		ctx, pid, wid, email, phone, displayName, source, slot)
	if err != nil {
		return nil, err
	}

	// CRM contact upsert + activity log (no-op when not bound).
	var contactID int64
	if email != "" {
		resp, err := a.crmCaller.UpsertContactByChannel(CRMUpsertReq{
			Kind:  "email",
			Value: email,
			Defaults: map[string]any{
				"display_name": displayName,
				"source":       "webinars:registration",
			},
			Source:    "webinars:registration",
			ProjectID: pid,
		})
		if err == nil && resp.Contact.ID != 0 {
			contactID = resp.Contact.ID
		}
	}
	if contactID == 0 && phone != "" {
		resp, err := a.crmCaller.UpsertContactByChannel(CRMUpsertReq{
			Kind:  "phone",
			Value: phone,
			Defaults: map[string]any{
				"display_name": displayName,
				"source":       "webinars:registration",
			},
			Source:    "webinars:registration",
			ProjectID: pid,
		})
		if err == nil && resp.Contact.ID != 0 {
			contactID = resp.Contact.ID
		}
	}
	if contactID != 0 {
		// project_id in the WHERE is belt-and-braces: registrantID now
		// comes from RowsAffected-verified insert or an explicit lookup,
		// but this statement is the one that used to corrupt an
		// unrelated registrant when LastInsertId lied.
		_, _ = ctx.AppDB().Exec(
			`UPDATE webinar_registrants SET contact_id = ?
			 WHERE id = ? AND project_id = ? AND webinar_id = ?`,
			contactID, registrantID, pid, wid)
		if created {
			_ = a.crmCaller.LogActivity(CRMLogActivityReq{
				ContactID: contactID,
				Kind:      "note",
				Body:      fmt.Sprintf("Registered for webinar %q", w.Title),
				Source:    "webinars",
				ProjectID: pid,
			})
		}
	}

	// Schedule reminders against the time this registrant actually
	// signed up for — their slot when they have one, the webinar's
	// scheduled_at otherwise. Idempotent: ux_reminder_slot makes a
	// repeat submit a no-op instead of a second full set of sends.
	reminderWebinar := *w
	if slot != nil {
		reminderWebinar.ScheduledAt = slot.StartsAt
	}
	if err := a.scheduleRemindersForRegistrant(ctx, pid, &reminderWebinar, registrantID, email != "", phone != ""); err != nil {
		ctx.Logger().Warn("schedule reminders", "registrant", registrantID, "err", err)
	}

	r, _ := a.dbGetRegistrant(ctx, pid, registrantID)
	a.materializeRegistrant(ctx, w, r)
	ctx.Emit("webinar.registered", map[string]any{
		"webinar_id":    wid,
		"registrant_id": registrantID,
	})
	return map[string]any{"registrant": r}, nil
}

// upsertRegistrant inserts a registrant, or finds the existing one when
// this (webinar, email) or (webinar, phone) already registered. Returns
// the row id and whether THIS call created it.
//
// Two bugs are fixed here, both of which produced silent data
// corruption rather than an error:
//
//   - The old code decided "did the insert happen?" from
//     res.LastInsertId(). In modernc.org/sqlite LastInsertId reports the
//     last successful insert on the pooled connection FROM ANY TABLE, so
//     when ON CONFLICT DO NOTHING fired it handed back the rowid of a
//     chat message or a reminder. The `registrantID == 0` guard only
//     held on a cold connection — and the SDK pins the pool to one
//     connection, so it's warm approximately always. Downstream, `UPDATE
//     webinar_registrants SET contact_id = ? WHERE id = <wrong id>`
//     rewrote an unrelated registrant, reminders were scheduled against
//     the wrong row, and dbGetRegistrant returning nil nil-dereffed the
//     public handler. RowsAffected is the correct signal.
//   - The conflict target only covered email, and the fallback lookup
//     (`WHERE webinar_id = ? AND email = ?` with an empty email) matched
//     nothing because the column stores NULL. A phone-only double submit
//     therefore minted a second registrant, a second join token and a
//     second full set of SMS reminders. ux_reg_phone (003) plus the
//     phone conflict target below close that.
func (a *App) upsertRegistrant(ctx *sdk.AppCtx, pid string, wid int64, email, phone, displayName, source string, slot *WebinarSlot) (int64, bool, error) {
	const insertPrefix = `INSERT INTO webinar_registrants
			(project_id, webinar_id, email, phone, display_name,
			 join_token, source, slot_id, registered_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		 `
	conflict := `ON CONFLICT(webinar_id, email) WHERE email IS NOT NULL AND email <> ''
		 DO NOTHING`
	if email == "" {
		conflict = `ON CONFLICT(webinar_id, phone)
		 WHERE phone IS NOT NULL AND phone <> '' AND (email IS NULL OR email = '')
		 DO NOTHING`
	}

	res, err := ctx.AppDB().Exec(insertPrefix+conflict,
		pid, wid, nullStr(email), nullStr(phone), nullStr(displayName),
		randomToken(), source, nullableSlotID(slot), nowRFC3339())
	if err != nil {
		return 0, false, fmt.Errorf("insert registrant: %w", err)
	}
	n, err := res.RowsAffected()
	if err != nil {
		return 0, false, fmt.Errorf("insert registrant: %w", err)
	}
	if n > 0 {
		id, err := res.LastInsertId()
		if err != nil {
			return 0, false, fmt.Errorf("insert registrant: %w", err)
		}
		return id, true, nil
	}

	// The conflict fired — this contact already registered.
	var id int64
	lookup := `SELECT id FROM webinar_registrants
		 WHERE project_id = ? AND webinar_id = ? AND email = ?`
	key := email
	if email == "" {
		lookup = `SELECT id FROM webinar_registrants
		 WHERE project_id = ? AND webinar_id = ? AND phone = ?
		   AND (email IS NULL OR email = '')`
		key = phone
	}
	if err := ctx.AppDB().QueryRow(lookup, pid, wid, key).Scan(&id); err != nil {
		return 0, false, fmt.Errorf("resolve existing registrant: %w", err)
	}
	return id, false, nil
}

// ─── webinars_list_registrants ───────────────────────────────────

func (a *App) toolListRegistrants(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	wid := int64Arg(args, "webinar_id")
	if wid == 0 {
		return nil, errors.New("webinar_id required")
	}
	limit := intArg(args, "limit", 100)
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	w, err := a.dbGet(ctx, pid, wid)
	if err != nil || w == nil {
		return nil, errors.New("webinar not found")
	}

	where := []string{"project_id = ?", "webinar_id = ?"}
	qargs := []any{pid, wid}
	if v, ok := args["attended"].(bool); ok {
		if v {
			where = append(where, "(attended_live = 1 OR attended_replay = 1)")
		} else {
			where = append(where, "attended_live = 0 AND attended_replay = 0")
		}
	}
	qargs = append(qargs, limit)

	// Single SELECT — the id-list-then-dbGet-per-id shape cost up to
	// 1001 sequential round-trips for a 500-registrant page on a pool
	// the SDK caps at one connection.
	rows, err := ctx.AppDB().Query(
		`SELECT `+registrantColumns+` FROM webinar_registrants WHERE `+strings.Join(where, " AND ")+
			` ORDER BY registered_at DESC LIMIT ?`, qargs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	out := []*Registrant{}
	for rows.Next() {
		r, err := scanRegistrant(rows)
		if err != nil {
			return nil, err
		}
		a.materializeRegistrant(ctx, w, r)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return map[string]any{"registrants": out, "count": len(out)}, nil
}

// ─── webinars_send_reminder ──────────────────────────────────────

func (a *App) toolSendReminder(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	w, err := a.dbGet(ctx, pid, id)
	if err != nil || w == nil {
		return nil, errors.New("webinar not found")
	}
	channel := strArg(args, "channel")
	if channel == "" {
		channel = "all"
	}
	audience := strArg(args, "audience")
	if audience == "" {
		audience = "registered"
	}
	bodyOverride := strArg(args, "body")

	// Build the recipient set.
	var where []string
	qargs := []any{pid, id}
	where = append(where, "project_id = ?", "webinar_id = ?")
	switch audience {
	case "joined":
		where = append(where, "attended_live = 1")
	case "no_show":
		where = append(where, "attended_live = 0")
	case "all", "registered":
		// no extra filter
	default:
		return nil, fmt.Errorf("audience must be all|registered|joined|no_show, got %q", audience)
	}
	rows, err := ctx.AppDB().Query(
		`SELECT id, COALESCE(email,''), COALESCE(phone,''), COALESCE(display_name,''), join_token
		 FROM webinar_registrants WHERE `+strings.Join(where, " AND "), qargs...)
	if err != nil {
		return nil, err
	}
	type rec struct {
		ID                                   int64
		Email, Phone, DisplayName, JoinToken string
	}
	recs := []rec{}
	for rows.Next() {
		var r rec
		_ = rows.Scan(&r.ID, &r.Email, &r.Phone, &r.DisplayName, &r.JoinToken)
		recs = append(recs, r)
	}
	rows.Close()

	body := bodyOverride
	if body == "" {
		body = defaultReminderBody(w, "now")
	}

	// One generation stamp per invocation. Without it every manual send
	// reused the key "webinar:N:reg:M:lead:manual:ch:email", so the
	// SECOND manual blast (and the we're-live blast, which routes
	// through here) was de-duplicated away by messaging and silently
	// delivered to nobody.
	gen := a.nextSequence(ctx, id, "manual_send")

	type job struct {
		regID   int64
		channel string
		to      string
	}
	jobs := []job{}
	for _, r := range recs {
		if (channel == "all" || channel == "email") && r.Email != "" {
			jobs = append(jobs, job{r.ID, "email", r.Email})
		}
		if (channel == "all" || channel == "sms") && r.Phone != "" {
			jobs = append(jobs, job{r.ID, "sms", r.Phone})
		}
	}

	// Bounded fan-out. Each job is a messaging call plus a CRM call; run
	// serially, a 5000-registrant blast is up to 10k sequential
	// cross-app round-trips.
	var mu sync.Mutex
	sent, skipped, failed := 0, 0, 0
	runBounded(jobs, a.reminderConcurrency(ctx), func(j job) {
		_, err := a.dispatchOneReminder(ctx, pid, w, reminderDispatch{
			RegistrantID: j.regID,
			Channel:      j.channel,
			To:           j.to,
			Lead:         "manual",
			Body:         body,
			IdemSuffix:   fmt.Sprintf("gen:%d", gen),
		})
		mu.Lock()
		defer mu.Unlock()
		switch {
		case err == nil:
			sent++
		case errors.Is(err, errMessagingNotBound):
			skipped++
		default:
			failed++
		}
	})
	return map[string]any{
		"sent":    sent,
		"skipped": skipped,
		"failed":  failed,
	}, nil
}

// ─── webinars_define_offer / webinars_post_offer ─────────────────

func (a *App) toolDefineOffer(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	offset := intArg(args, "offset_seconds", -1)
	headline := strArg(args, "headline")
	cta := strArg(args, "cta_label")
	url := strArg(args, "cta_url")
	if id == 0 || offset < 0 || headline == "" || cta == "" || url == "" {
		return nil, errors.New("id, offset_seconds, headline, cta_label, cta_url required")
	}
	// The live room hands cta_url to window.open(), which executes
	// `javascript:` URLs. Offers are agent-scriptable, so without an
	// allowlist a prompt-injected agent gets script execution in every
	// attendee's browser.
	url, err = validateCTAURL(url)
	if err != nil {
		return nil, err
	}
	dur := intArg(args, "duration_seconds", 30)
	w, err := a.dbGet(ctx, pid, id)
	if err != nil || w == nil {
		return nil, errors.New("webinar not found")
	}

	res, err := ctx.AppDB().Exec(
		`INSERT INTO webinar_offers
			(project_id, webinar_id, offset_seconds, headline, body,
			 cta_label, cta_url, duration_seconds)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, id, offset, headline,
		nullStr(strArg(args, "body")), cta, url, dur)
	if err != nil {
		return nil, err
	}
	offerID, _ := res.LastInsertId()
	return map[string]any{"offer_id": offerID}, nil
}

func (a *App) toolPostOffer(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	headline := strArg(args, "headline")
	cta := strArg(args, "cta_label")
	url := strArg(args, "cta_url")
	if id == 0 || headline == "" || cta == "" || url == "" {
		return nil, errors.New("id, headline, cta_label, cta_url required")
	}
	url, err = validateCTAURL(url)
	if err != nil {
		return nil, err
	}
	dur := intArg(args, "duration_seconds", 30)
	w, err := a.dbGet(ctx, pid, id)
	if err != nil || w == nil {
		return nil, errors.New("webinar not found")
	}

	seq := a.nextWebinarSequence(ctx, id)
	res, err := ctx.AppDB().Exec(
		`INSERT INTO webinar_offers
			(project_id, webinar_id, offset_seconds, headline, body,
			 cta_label, cta_url, duration_seconds, shown_at, sequence)
		 VALUES (?, ?, NULL, ?, ?, ?, ?, ?, ?, ?)`,
		pid, id, headline, nullStr(strArg(args, "body")),
		cta, url, dur, nowRFC3339(), seq)
	if err != nil {
		return nil, err
	}
	offerID, _ := res.LastInsertId()
	ctx.Emit("webinar.offer.shown", map[string]any{
		"webinar_id": id,
		"offer_id":   offerID,
		"sequence":   seq,
	})
	return map[string]any{"offer_id": offerID, "sequence": seq}, nil
}

// ─── webinars_push_poll ──────────────────────────────────────────

func (a *App) toolPushPoll(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	question := strArg(args, "question")
	choicesRaw, _ := args["choices"].([]any)
	if id == 0 || question == "" || len(choicesRaw) < 2 {
		return nil, errors.New("id, question, and >= 2 choices required")
	}
	dur := intArg(args, "duration_seconds", 60)

	choices := []string{}
	for _, c := range choicesRaw {
		if s, ok := c.(string); ok && s != "" {
			choices = append(choices, s)
		}
	}
	if len(choices) < 2 {
		return nil, errors.New("at least 2 valid string choices required")
	}
	choicesJSON, _ := json.Marshal(choices)
	closesAt := time.Now().UTC().Add(time.Duration(dur) * time.Second).Format(time.RFC3339)
	seq := a.nextWebinarSequence(ctx, id)

	res, err := ctx.AppDB().Exec(
		`INSERT INTO webinar_polls
			(project_id, webinar_id, question, choices,
			 duration_seconds, closes_at, sequence)
		 VALUES (?, ?, ?, ?, ?, ?, ?)`,
		pid, id, question, string(choicesJSON), dur, closesAt, seq)
	if err != nil {
		return nil, err
	}
	pollID, _ := res.LastInsertId()
	ctx.Emit("webinar.poll.opened", map[string]any{
		"webinar_id": id,
		"poll_id":    pollID,
	})
	return map[string]any{"poll_id": pollID, "sequence": seq, "closes_at": closesAt}, nil
}

// ─── webinars_publish_replay ─────────────────────────────────────

func (a *App) toolPublishReplay(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	w, err := a.dbGet(ctx, pid, id)
	if err != nil || w == nil {
		return nil, errors.New("webinar not found")
	}
	if w.Status != "ended" {
		return nil, fmt.Errorf("webinar is %s; replay can only be published after ended", w.Status)
	}
	expiresAt := strings.TrimSpace(strArg(args, "expires_at"))
	if expiresAt != "" {
		norm, err := normalizeRFC3339(expiresAt)
		if err != nil {
			return nil, fmt.Errorf("expires_at must be RFC3339: %w", err)
		}
		expiresAt = norm
	}

	token := w.ReplayToken
	if token == "" {
		token = randomToken()
	}
	if _, err := ctx.AppDB().Exec(
		`UPDATE webinars
		 SET recording_published = 1, replay_token = ?, replay_expires_at = ?
		 WHERE id = ? AND project_id = ?`,
		token, nullStr(expiresAt), id, pid); err != nil {
		return nil, err
	}

	w, _ = a.dbGet(ctx, pid, id)
	// Publishing is the moment the recording becomes reachable by
	// people outside the live room, so make sure the stream only
	// answers to signed URLs before we hand out a replay link.
	a.EnforceSignedPlayback(ctx, w)
	a.materialize(ctx, w, nil)
	ctx.Emit("webinar.replay_published", map[string]any{"id": id})
	return map[string]any{
		"replay_url":        w.ReplayURL,
		"replay_expires_at": w.ReplayExpiresAt,
	}, nil
}

// ─── webinars_get_engagement ─────────────────────────────────────

func (a *App) toolGetEngagement(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	w, err := a.dbGet(ctx, pid, id)
	if err != nil || w == nil {
		return nil, errors.New("webinar not found")
	}

	out := map[string]any{
		"webinar_id": id,
		"slug":       w.Slug,
	}
	var n int

	_ = ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM webinar_registrants WHERE webinar_id = ?`, id).Scan(&n)
	out["registrations"] = n

	_ = ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM webinar_registrants
		 WHERE webinar_id = ? AND attended_live = 1`, id).Scan(&n)
	out["joined_live"] = n

	_ = ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM webinar_registrants
		 WHERE webinar_id = ? AND attended_replay = 1`, id).Scan(&n)
	out["joined_replay"] = n

	// Average watch %: avg(watch_seconds / (duration_minutes*60)) for live.
	durSec := w.DurationMinutes * 60
	if durSec <= 0 {
		durSec = 60 * 60
	}
	var sumWatch sql.NullFloat64
	_ = ctx.AppDB().QueryRow(
		`SELECT COALESCE(AVG(MIN(watch_seconds, ?)), 0) FROM webinar_attendance
		 WHERE webinar_id = ? AND source = 'live'`, durSec, id).Scan(&sumWatch)
	avgWatch := sumWatch.Float64
	out["avg_watch_seconds"] = int(avgWatch)
	out["avg_watch_pct"] = int(avgWatch / float64(durSec) * 100)

	// Peak concurrent — read from streaming.
	if w.StreamID != 0 {
		if m, err := a.streamingCaller.GetMetrics(w.StreamID); err == nil {
			out["peak_concurrent"] = m.PeakViewers
			out["total_viewer_seconds"] = m.TotalViewerSeconds
		}
	}

	// Offer CTR.
	//
	// The old formula was raw clicks / offers shown, which counted the
	// same attendee's repeat clicks and routinely reported >100%. CTR is
	// now the share of registrants who clicked at least one offer —
	// distinct clickers over registrations, which is bounded by
	// construction. Raw volume is still reported separately.
	var offers, clicks, uniqueClickers int
	_ = ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM webinar_offers WHERE webinar_id = ? AND shown_at IS NOT NULL`, id).Scan(&offers)
	_ = ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM webinar_offer_clicks
		 WHERE offer_id IN (SELECT id FROM webinar_offers WHERE webinar_id = ?)`, id).Scan(&clicks)
	_ = ctx.AppDB().QueryRow(
		`SELECT COUNT(DISTINCT registrant_id) FROM webinar_offer_clicks
		 WHERE registrant_id IS NOT NULL
		   AND offer_id IN (SELECT id FROM webinar_offers WHERE webinar_id = ?)`, id).Scan(&uniqueClickers)
	out["offers_shown"] = offers
	out["offer_clicks"] = clicks
	out["unique_offer_clickers"] = uniqueClickers
	registrations, _ := out["registrations"].(int)
	if offers > 0 && registrations > 0 {
		out["offer_click_through_pct"] = int(float64(uniqueClickers) / float64(registrations) * 100)
	}

	// Poll response rate — responses / (polls_opened * registrants).
	var polls, responses int
	_ = ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM webinar_polls WHERE webinar_id = ?`, id).Scan(&polls)
	_ = ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM webinar_poll_responses
		 WHERE poll_id IN (SELECT id FROM webinar_polls WHERE webinar_id = ?)`, id).Scan(&responses)
	out["polls_opened"] = polls
	out["poll_responses"] = responses

	return out, nil
}

// ─── webinars_close ──────────────────────────────────────────────

func (a *App) toolClose(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	id := int64Arg(args, "id")
	if id == 0 {
		return nil, errors.New("id required")
	}
	w, err := a.dbGet(ctx, pid, id)
	if err != nil || w == nil {
		return nil, errors.New("webinar not found")
	}
	if w.Status == "ended" || w.Status == "cancelled" {
		return map[string]any{"webinar": w, "noop": true}, nil
	}

	if w.StreamID != 0 {
		_ = a.streamingCaller.StopStream(w.StreamID)
	}

	if _, err := ctx.AppDB().Exec(
		`UPDATE webinars SET status='ended', ended_at = ?
		 WHERE id = ? AND project_id = ?`, nowRFC3339(), id, pid); err != nil {
		return nil, err
	}
	// Pending reminders for a webinar that's over are noise at best and
	// a "starts in 15 minutes" message after the fact at worst.
	if _, err := ctx.AppDB().Exec(
		`UPDATE webinar_reminders
		 SET status = 'skipped', sent_at = ?, error = 'webinar ended'
		 WHERE project_id = ? AND webinar_id = ? AND status = 'pending'`,
		nowRFC3339(), pid, id); err != nil {
		ctx.Logger().Warn("stand down pending reminders", "id", id, "err", err)
	}

	w, _ = a.dbGet(ctx, pid, id)
	a.materialize(ctx, w, nil)
	ctx.Emit("webinar.ended", map[string]any{"id": id})
	return map[string]any{"webinar": w}, nil
}

// ─── DB helpers ──────────────────────────────────────────────────

// rowScanner covers both *sql.Row and *sql.Rows so one scan function
// serves the single-row get and the list query — the list queries used
// to SELECT ids and then re-fetch each row one at a time.
type rowScanner interface {
	Scan(dest ...any) error
}

const webinarColumns = `id, project_id, slug, title,
		COALESCE(host_name,''), COALESCE(description,''),
		kind, COALESCE(scheduled_at,''), duration_minutes,
		COALESCE(scheduling_mode,'single'), COALESCE(timezone,'UTC'),
		COALESCE(slot_duration_minutes,0), COALESCE(registration_policy,''),
		status, COALESCE(stream_id, 0),
		recording_published, COALESCE(replay_token,''),
		COALESCE(replay_expires_at,''),
		created_at, COALESCE(started_at,''), COALESCE(ended_at,'')`

func scanWebinar(row rowScanner) (*Webinar, error) {
	w := &Webinar{}
	var published int
	if err := row.Scan(
		&w.ID, &w.ProjectID, &w.Slug, &w.Title,
		&w.HostName, &w.Description,
		&w.Kind, &w.ScheduledAt, &w.DurationMinutes,
		&w.SchedulingMode, &w.Timezone, &w.SlotDurationMin, &w.RegistrationPolicy,
		&w.Status, &w.StreamID,
		&published, &w.ReplayToken,
		&w.ReplayExpiresAt,
		&w.CreatedAt, &w.StartedAt, &w.EndedAt,
	); err != nil {
		return nil, err
	}
	w.RecordingPublished = published != 0
	return w, nil
}

func (a *App) dbGet(ctx *sdk.AppCtx, pid string, id int64) (*Webinar, error) {
	w, err := scanWebinar(ctx.AppDB().QueryRow(
		`SELECT `+webinarColumns+` FROM webinars WHERE id = ? AND project_id = ?`, id, pid))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return w, err
}

const registrantColumns = `id, webinar_id, COALESCE(slot_id,0), contact_id,
		COALESCE(email,''), COALESCE(phone,''),
		COALESCE(display_name,''), join_token,
		registered_at, COALESCE(source,''),
		attended_live, attended_replay`

func scanRegistrant(row rowScanner) (*Registrant, error) {
	r := &Registrant{}
	var contactID sql.NullInt64
	var live, replay int
	if err := row.Scan(
		&r.ID, &r.WebinarID, &r.SlotID, &contactID,
		&r.Email, &r.Phone, &r.DisplayName, &r.JoinToken,
		&r.RegisteredAt, &r.Source,
		&live, &replay,
	); err != nil {
		return nil, err
	}
	if contactID.Valid {
		v := contactID.Int64
		r.ContactID = &v
	}
	r.AttendedLive = live != 0
	r.AttendedReplay = replay != 0
	return r, nil
}

func (a *App) dbGetRegistrant(ctx *sdk.AppCtx, pid string, id int64) (*Registrant, error) {
	r, err := scanRegistrant(ctx.AppDB().QueryRow(
		`SELECT `+registrantColumns+
			` FROM webinar_registrants WHERE id = ? AND project_id = ?`, id, pid))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return r, err
}

func (a *App) createSlot(ctx *sdk.AppCtx, pid string, webinarID int64, startsAt, endsAt, timezone string, durationMinutes, capacity int, status, source, generationKey string) (*WebinarSlot, error) {
	// Slot times are stored as UTC RFC3339 regardless of the layout or
	// offset the caller supplied. The status sweep and the availability
	// checks compare these values lexically in SQL, which is only a
	// total order when every row shares one layout and one zone.
	start, err := parseDBTime(startsAt)
	if err != nil {
		return nil, fmt.Errorf("starts_at must be RFC3339: %w", err)
	}
	startsAt = formatRFC3339(start)
	if endsAt != "" {
		end, err := parseDBTime(endsAt)
		if err != nil {
			return nil, fmt.Errorf("ends_at must be RFC3339: %w", err)
		}
		endsAt = formatRFC3339(end)
	} else if durationMinutes > 0 {
		endsAt = formatRFC3339(start.Add(time.Duration(durationMinutes) * time.Minute))
	}
	if timezone == "" {
		timezone = "UTC"
	}
	if status == "" {
		status = "scheduled"
	}
	if !validSlotStatus(status) {
		return nil, fmt.Errorf("invalid slot status %q", status)
	}
	if source == "" {
		source = "manual"
	}
	res, err := ctx.AppDB().Exec(
		`INSERT INTO webinar_slots
			(project_id, webinar_id, starts_at, ends_at, timezone,
			 capacity, status, source, generation_key, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, webinarID, startsAt, nullStr(endsAt), nullStr(timezone),
		nullablePositiveInt(capacity), status, source, nullStr(generationKey), nowRFC3339())
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return a.dbGetSlot(ctx, pid, id)
}

func (a *App) dbGetSlot(ctx *sdk.AppCtx, pid string, id int64) (*WebinarSlot, error) {
	row := ctx.AppDB().QueryRow(
		`SELECT s.id, s.project_id, s.webinar_id, s.starts_at,
				COALESCE(s.ends_at,''), COALESCE(s.timezone,''),
				COALESCE(s.capacity,0), s.status, s.source,
				COALESCE(s.generation_key,''), s.created_at,
				COUNT(r.id)
		 FROM webinar_slots s
		 LEFT JOIN webinar_registrants r ON r.slot_id = s.id
		 WHERE s.id = ? AND s.project_id = ?
		 GROUP BY s.id`, id, pid)
	slot := &WebinarSlot{}
	if err := row.Scan(
		&slot.ID, &slot.ProjectID, &slot.WebinarID, &slot.StartsAt,
		&slot.EndsAt, &slot.Timezone, &slot.Capacity, &slot.Status,
		&slot.Source, &slot.GenerationKey, &slot.CreatedAt, &slot.Registered,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	slot.Available = slotIsAvailable(slot)
	return slot, nil
}

func (a *App) dbListSlots(ctx *sdk.AppCtx, pid string, webinarID int64, from, to string, availableOnly bool) ([]*WebinarSlot, error) {
	where := []string{"s.project_id = ?", "s.webinar_id = ?"}
	args := []any{pid, webinarID}
	if from != "" {
		where = append(where, "s.starts_at >= ?")
		args = append(args, from)
	}
	if to != "" {
		where = append(where, "s.starts_at <= ?")
		args = append(args, to)
	}
	rows, err := ctx.AppDB().Query(
		`SELECT s.id, s.project_id, s.webinar_id, s.starts_at,
				COALESCE(s.ends_at,''), COALESCE(s.timezone,''),
				COALESCE(s.capacity,0), s.status, s.source,
				COALESCE(s.generation_key,''), s.created_at,
				COUNT(r.id)
		 FROM webinar_slots s
		 LEFT JOIN webinar_registrants r ON r.slot_id = s.id
		 WHERE `+strings.Join(where, " AND ")+`
		 GROUP BY s.id
		 ORDER BY s.starts_at ASC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*WebinarSlot{}
	for rows.Next() {
		slot := &WebinarSlot{}
		if err := rows.Scan(
			&slot.ID, &slot.ProjectID, &slot.WebinarID, &slot.StartsAt,
			&slot.EndsAt, &slot.Timezone, &slot.Capacity, &slot.Status,
			&slot.Source, &slot.GenerationKey, &slot.CreatedAt, &slot.Registered,
		); err != nil {
			return nil, err
		}
		slot.Available = slotIsAvailable(slot)
		if availableOnly && !slot.Available {
			continue
		}
		out = append(out, slot)
	}
	return out, nil
}

func (a *App) resolveRegistrationSlot(ctx *sdk.AppCtx, pid string, w *Webinar, requestedSlotID int64) (*WebinarSlot, error) {
	if requestedSlotID != 0 {
		slot, err := a.dbGetSlot(ctx, pid, requestedSlotID)
		if err != nil {
			return nil, err
		}
		if slot == nil || slot.WebinarID != w.ID {
			return nil, errors.New("slot not found")
		}
		if !slot.Available {
			return nil, errors.New("slot is not available")
		}
		return slot, nil
	}
	slots, err := a.dbListSlots(ctx, pid, w.ID, "", "", true)
	if err != nil {
		return nil, err
	}
	switch len(slots) {
	case 1:
		return slots[0], nil
	case 0:
		// Distinguish "this webinar doesn't use slots" (draft, evergreen,
		// replay — register freely) from "every slot has elapsed or
		// filled up", which used to silently produce a registrant with
		// zero reminders for a date that already happened.
		all, err := a.dbListSlots(ctx, pid, w.ID, "", "", false)
		if err != nil {
			return nil, err
		}
		if len(all) == 0 || w.Status == "ended" {
			return nil, nil
		}
		return nil, errors.New("no available slots — every slot has elapsed or is full")
	default:
		return nil, errors.New("slot_id required when multiple slots are available")
	}
}

// slotIsAvailable reports whether a slot can still be registered for.
//
// Status alone was never enough: a slot whose time has passed keeps
// status='scheduled' forever (nothing transitioned it), so yesterday's
// slot stayed "available", got auto-assigned by resolveRegistrationSlot,
// and then had every reminder lead skipped as past.
func slotIsAvailable(slot *WebinarSlot) bool {
	return slotAvailableAt(slot, nowUTC())
}

func slotAvailableAt(slot *WebinarSlot, now time.Time) bool {
	if slot == nil {
		return false
	}
	switch slot.Status {
	case "scheduled", "open", "live":
	default:
		return false
	}
	if end, ok := slotEndTime(slot); ok && !end.After(now) {
		return false
	}
	return slot.Capacity <= 0 || slot.Registered < slot.Capacity
}

// slotEndTime resolves the moment a slot stops being joinable: its
// ends_at, or its starts_at when no end was recorded.
func slotEndTime(slot *WebinarSlot) (time.Time, bool) {
	if slot == nil {
		return time.Time{}, false
	}
	if slot.EndsAt != "" {
		if t, err := parseDBTime(slot.EndsAt); err == nil {
			return t, true
		}
	}
	if slot.StartsAt != "" {
		if t, err := parseDBTime(slot.StartsAt); err == nil {
			return t, true
		}
	}
	return time.Time{}, false
}

func validSlotStatus(status string) bool {
	switch status {
	case "scheduled", "open", "live", "ended", "cancelled":
		return true
	}
	return false
}

func nullableSlotID(slot *WebinarSlot) any {
	if slot == nil || slot.ID == 0 {
		return nil
	}
	return slot.ID
}

func nullablePositiveInt(v int) any {
	if v <= 0 {
		return nil
	}
	return v
}

// dbGetWebinarByStreamID lets the lifecycle EventHandler find the
// webinar that owns a given streaming stream.
func (a *App) dbGetWebinarByStreamID(ctx *sdk.AppCtx, projectID string, streamID int64) (*Webinar, error) {
	var id int64
	err := ctx.AppDB().QueryRow(
		`SELECT id FROM webinars WHERE project_id = ? AND stream_id = ?`,
		projectID, streamID).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return a.dbGet(ctx, projectID, id)
}

// nextWebinarSequence now lives in engagement.go — it's an atomic
// counter bump rather than a MAX+1 scan.

// ─── URL materialization ─────────────────────────────────────────

func (a *App) materialize(ctx *sdk.AppCtx, w *Webinar, snap *StreamSnapshot) {
	if w == nil {
		return
	}
	base := a.publicAppPath(ctx)
	prefix := strings.TrimSuffix(suppressNonEmptyOr(ctx.Config().Get("registration_url_prefix"), "/r"), "/")
	w.RegistrationURL = base + prefix + "/" + w.Slug

	if snap != nil {
		w.IngestURL = snap.IngestURL
		w.StreamKey = snap.StreamKey
		w.PlaybackURL = snap.PlaybackURL
	}
	if w.RecordingPublished && w.ReplayToken != "" {
		replayPrefix := strings.TrimSuffix(suppressNonEmptyOr(ctx.Config().Get("replay_url_prefix"), "/replay"), "/")
		w.ReplayURL = base + replayPrefix + "/" + w.Slug + "?t=" + w.ReplayToken
	}
}

func (a *App) materializeRegistrant(ctx *sdk.AppCtx, w *Webinar, r *Registrant) {
	if r == nil {
		return
	}
	base := a.publicAppPath(ctx)
	prefix := strings.TrimSuffix(suppressNonEmptyOr(ctx.Config().Get("live_room_url_prefix"), "/live"), "/")
	r.JoinURL = base + prefix + "/" + r.JoinToken
}
