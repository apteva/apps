package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
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
	scheduledAt := strArg(args, "scheduled_at")
	if scheduledAt != "" {
		if _, err := time.Parse(time.RFC3339, scheduledAt); err != nil {
			return nil, fmt.Errorf("scheduled_at must be RFC3339: %w", err)
		}
	}
	durationMinutes := intArg(args, "duration_minutes", 60)
	if durationMinutes <= 0 {
		durationMinutes = 60
	}

	slug := uniqueSlug(ctx, pid, slugify(title))

	res, err := ctx.AppDB().Exec(
		`INSERT INTO webinars
			(project_id, slug, title, host_name, description, kind,
			 scheduled_at, duration_minutes, status)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, 'draft')`,
		pid, slug, title,
		nullStr(strArg(args, "host_name")),
		nullStr(strArg(args, "description")),
		kind,
		nullStr(scheduledAt),
		durationMinutes)
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

	w, _ := a.dbGet(ctx, pid, id)
	a.materialize(ctx, w, &created.Stream)
	ctx.Emit("webinar.created", map[string]any{"id": id, "slug": slug})
	return map[string]any{"webinar": w}, nil
}

// uniqueSlug ensures the slug is unique per project. Appends -2, -3 …
// until INSERT would succeed. SQLite-side uniqueness still enforces it.
func uniqueSlug(ctx *sdk.AppCtx, pid, base string) string {
	if base == "" {
		base = randomToken()[:8]
	}
	candidate := base
	for n := 2; n < 100; n++ {
		var exists int
		_ = ctx.AppDB().QueryRow(
			`SELECT COUNT(*) FROM webinars WHERE project_id = ? AND slug = ?`,
			pid, candidate).Scan(&exists)
		if exists == 0 {
			return candidate
		}
		candidate = fmt.Sprintf("%s-%d", base, n)
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
		"webinar":            w,
		"found":              true,
		"registrant_count":   regCount,
		"attended_live_count": attendedLive,
		"stream":             snap,
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

	rows, err := ctx.AppDB().Query(
		`SELECT id FROM webinars WHERE `+strings.Join(where, " AND ")+
			` ORDER BY COALESCE(scheduled_at, created_at) DESC LIMIT ?`, qargs...)
	if err != nil {
		return nil, err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err == nil {
			ids = append(ids, id)
		}
	}
	rows.Close()

	out := []*Webinar{}
	for _, id := range ids {
		w, err := a.dbGet(ctx, pid, id)
		if err != nil || w == nil {
			continue
		}
		a.materialize(ctx, w, nil)
		out = append(out, w)
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

	allowed := map[string]bool{
		"title":            true,
		"host_name":        true,
		"description":      true,
		"kind":             true,
		"scheduled_at":     true,
		"duration_minutes": true,
	}
	sets := []string{}
	qargs := []any{}
	scheduledChanged := false
	for k, v := range patch {
		if !allowed[k] {
			continue
		}
		if k == "scheduled_at" {
			scheduledChanged = true
		}
		sets = append(sets, k+" = ?")
		qargs = append(qargs, v)
	}
	if len(sets) == 0 {
		return map[string]any{"webinar": w, "noop": true}, nil
	}
	qargs = append(qargs, id, pid)
	if _, err := ctx.AppDB().Exec(
		`UPDATE webinars SET `+strings.Join(sets, ", ")+
			` WHERE id = ? AND project_id = ?`, qargs...); err != nil {
		return nil, err
	}

	// If scheduled_at moved, regenerate pending reminders.
	if scheduledChanged {
		if err := a.regenerateReminders(ctx, pid, id); err != nil {
			ctx.Logger().Warn("regenerate reminders", "id", id, "err", err)
		}
	}

	w, _ = a.dbGet(ctx, pid, id)
	a.materialize(ctx, w, nil)
	ctx.Emit("webinar.updated", map[string]any{"id": id})
	return map[string]any{"webinar": w}, nil
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
	email := strings.TrimSpace(strArg(args, "email"))
	phone := strings.TrimSpace(strArg(args, "phone"))
	if email == "" && phone == "" {
		return nil, errors.New("email or phone required")
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

	displayName := strArg(args, "display_name")
	source := strArg(args, "source")
	if source == "" {
		source = "agent"
	}

	// Idempotent on (webinar_id, email) — INSERT OR IGNORE then SELECT.
	joinToken := randomToken()
	res, err := ctx.AppDB().Exec(
		`INSERT INTO webinar_registrants
			(project_id, webinar_id, email, phone, display_name,
			 join_token, source)
		 VALUES (?, ?, ?, ?, ?, ?, ?)
		 ON CONFLICT(webinar_id, email) WHERE email IS NOT NULL AND email <> ''
		 DO NOTHING`,
		pid, wid, nullStr(email), nullStr(phone),
		nullStr(displayName), joinToken, source)
	var registrantID int64
	if err == nil {
		registrantID, _ = res.LastInsertId()
	}
	if registrantID == 0 {
		// Either the conflict path fired or ON CONFLICT isn't supported
		// here. Look up the existing row.
		err = ctx.AppDB().QueryRow(
			`SELECT id, join_token FROM webinar_registrants
			 WHERE webinar_id = ? AND email = ?`,
			wid, email).Scan(&registrantID, &joinToken)
		if err != nil {
			// No email-key conflict possible (phone-only or first row);
			// re-attempt insert without ON CONFLICT.
			res, err = ctx.AppDB().Exec(
				`INSERT INTO webinar_registrants
					(project_id, webinar_id, email, phone, display_name,
					 join_token, source)
				 VALUES (?, ?, ?, ?, ?, ?, ?)`,
				pid, wid, nullStr(email), nullStr(phone),
				nullStr(displayName), joinToken, source)
			if err != nil {
				return nil, fmt.Errorf("insert registrant: %w", err)
			}
			registrantID, _ = res.LastInsertId()
		}
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
		_, _ = ctx.AppDB().Exec(
			`UPDATE webinar_registrants SET contact_id = ? WHERE id = ?`,
			contactID, registrantID)
		_ = a.crmCaller.LogActivity(CRMLogActivityReq{
			ContactID: contactID,
			Kind:      "note",
			Body:      fmt.Sprintf("Registered for webinar %q", w.Title),
			Source:    "webinars",
			ProjectID: pid,
		})
	}

	// Schedule reminders.
	if err := a.scheduleRemindersForRegistrant(ctx, pid, w, registrantID, email != "", phone != ""); err != nil {
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

	rows, err := ctx.AppDB().Query(
		`SELECT id FROM webinar_registrants WHERE `+strings.Join(where, " AND ")+
			` ORDER BY registered_at DESC LIMIT ?`, qargs...)
	if err != nil {
		return nil, err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		_ = rows.Scan(&id)
		ids = append(ids, id)
	}
	rows.Close()

	out := []*Registrant{}
	for _, id := range ids {
		r, err := a.dbGetRegistrant(ctx, pid, id)
		if err != nil || r == nil {
			continue
		}
		a.materializeRegistrant(ctx, w, r)
		out = append(out, r)
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
		ID                                  int64
		Email, Phone, DisplayName, JoinToken string
	}
	recs := []rec{}
	for rows.Next() {
		var r rec
		_ = rows.Scan(&r.ID, &r.Email, &r.Phone, &r.DisplayName, &r.JoinToken)
		recs = append(recs, r)
	}
	rows.Close()

	sent, skipped, failed := 0, 0, 0
	body := bodyOverride
	if body == "" {
		body = defaultReminderBody(w, "now")
	}

	for _, r := range recs {
		if (channel == "all" || channel == "email") && r.Email != "" {
			if err := a.dispatchOneReminder(ctx, pid, w, r.ID, "email", r.Email, "manual", body); err != nil {
				if errors.Is(err, errMessagingNotBound) {
					skipped++
				} else {
					failed++
				}
			} else {
				sent++
			}
		}
		if (channel == "all" || channel == "sms") && r.Phone != "" {
			if err := a.dispatchOneReminder(ctx, pid, w, r.ID, "sms", r.Phone, "manual", body); err != nil {
				if errors.Is(err, errMessagingNotBound) {
					skipped++
				} else {
					failed++
				}
			} else {
				sent++
			}
		}
	}
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
		 VALUES (?, ?, NULL, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP, ?)`,
		pid, id, headline, nullStr(strArg(args, "body")),
		cta, url, dur, seq)
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
	expiresAt := strArg(args, "expires_at")
	if expiresAt != "" {
		if _, err := time.Parse(time.RFC3339, expiresAt); err != nil {
			return nil, fmt.Errorf("expires_at must be RFC3339: %w", err)
		}
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
	var offers, clicks int
	_ = ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM webinar_offers WHERE webinar_id = ? AND shown_at IS NOT NULL`, id).Scan(&offers)
	_ = ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM webinar_offer_clicks
		 WHERE offer_id IN (SELECT id FROM webinar_offers WHERE webinar_id = ?)`, id).Scan(&clicks)
	out["offers_shown"] = offers
	out["offer_clicks"] = clicks
	if offers > 0 {
		out["offer_click_through_pct"] = int(float64(clicks) / float64(offers) * 100)
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
		`UPDATE webinars SET status='ended', ended_at = CURRENT_TIMESTAMP
		 WHERE id = ? AND project_id = ?`, id, pid); err != nil {
		return nil, err
	}

	w, _ = a.dbGet(ctx, pid, id)
	a.materialize(ctx, w, nil)
	ctx.Emit("webinar.ended", map[string]any{"id": id})
	return map[string]any{"webinar": w}, nil
}

// ─── DB helpers ──────────────────────────────────────────────────

func (a *App) dbGet(ctx *sdk.AppCtx, pid string, id int64) (*Webinar, error) {
	row := ctx.AppDB().QueryRow(
		`SELECT id, project_id, slug, title,
				COALESCE(host_name,''), COALESCE(description,''),
				kind, COALESCE(scheduled_at,''), duration_minutes,
				status, COALESCE(stream_id, 0),
				recording_published, COALESCE(replay_token,''),
				COALESCE(replay_expires_at,''),
				created_at, COALESCE(started_at,''), COALESCE(ended_at,'')
		 FROM webinars WHERE id = ? AND project_id = ?`, id, pid)
	w := &Webinar{}
	var published int
	if err := row.Scan(
		&w.ID, &w.ProjectID, &w.Slug, &w.Title,
		&w.HostName, &w.Description,
		&w.Kind, &w.ScheduledAt, &w.DurationMinutes,
		&w.Status, &w.StreamID,
		&published, &w.ReplayToken,
		&w.ReplayExpiresAt,
		&w.CreatedAt, &w.StartedAt, &w.EndedAt,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	w.RecordingPublished = published != 0
	return w, nil
}

func (a *App) dbGetRegistrant(ctx *sdk.AppCtx, pid string, id int64) (*Registrant, error) {
	row := ctx.AppDB().QueryRow(
		`SELECT id, webinar_id, contact_id,
				COALESCE(email,''), COALESCE(phone,''),
				COALESCE(display_name,''), join_token,
				registered_at, COALESCE(source,''),
				attended_live, attended_replay
		 FROM webinar_registrants WHERE id = ? AND project_id = ?`, id, pid)
	r := &Registrant{}
	var contactID sql.NullInt64
	var live, replay int
	if err := row.Scan(
		&r.ID, &r.WebinarID, &contactID,
		&r.Email, &r.Phone, &r.DisplayName, &r.JoinToken,
		&r.RegisteredAt, &r.Source,
		&live, &replay,
	); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
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

// nextWebinarSequence returns a monotonic sequence number for the
// live-room polling endpoint. Computed from MAX(sequence) + 1 across
// chat + offers + polls so events on different topics can be merged
// into a single ordered stream.
func (a *App) nextWebinarSequence(ctx *sdk.AppCtx, webinarID int64) int {
	var seq int
	_ = ctx.AppDB().QueryRow(
		`SELECT COALESCE(MAX(seq), 0) + 1 FROM (
			SELECT MAX(sequence) AS seq FROM webinar_chat   WHERE webinar_id = ?
			UNION ALL
			SELECT MAX(sequence) AS seq FROM webinar_offers WHERE webinar_id = ?
			UNION ALL
			SELECT MAX(sequence) AS seq FROM webinar_polls  WHERE webinar_id = ?
		)`, webinarID, webinarID, webinarID).Scan(&seq)
	if seq < 1 {
		seq = 1
	}
	return seq
}

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
