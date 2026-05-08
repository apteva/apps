package main

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// ─── reminder-scheduler ──────────────────────────────────────────
//
// Every minute: scan webinar_reminders for status='pending' rows
// whose scheduled_for has passed; dispatch each via messaging; mark
// the row sent | skipped | failed.
//
// Runs as a Worker — apteva-server schedules it.

func (a *App) runReminderScheduler(ctx context.Context, app *sdk.AppCtx) error {
	if app == nil || app.AppDB() == nil {
		return nil
	}
	now := time.Now().UTC().Format(time.RFC3339)
	rows, err := app.AppDB().Query(
		`SELECT r.id, r.project_id, r.webinar_id, r.registrant_id,
				r.channel, r.lead_label,
				COALESCE(reg.email,''), COALESCE(reg.phone,''),
				COALESCE(reg.display_name,''), reg.join_token,
				w.title, w.scheduled_at
		 FROM webinar_reminders r
		 JOIN webinar_registrants reg ON reg.id = r.registrant_id
		 JOIN webinars w ON w.id = r.webinar_id
		 WHERE r.status = 'pending' AND r.scheduled_for <= ?
		 LIMIT 500`, now)
	if err != nil {
		return err
	}
	type job struct {
		ID, WebinarID, RegistrantID int64
		ProjectID, Channel, Lead   string
		Email, Phone, Name, Token  string
		Title, ScheduledAt         string
	}
	jobs := []job{}
	for rows.Next() {
		var j job
		if err := rows.Scan(&j.ID, &j.ProjectID, &j.WebinarID, &j.RegistrantID,
			&j.Channel, &j.Lead, &j.Email, &j.Phone, &j.Name, &j.Token,
			&j.Title, &j.ScheduledAt); err == nil {
			jobs = append(jobs, j)
		}
	}
	rows.Close()

	for _, j := range jobs {
		w := &Webinar{
			ID:          j.WebinarID,
			ProjectID:   j.ProjectID,
			Title:       j.Title,
			ScheduledAt: j.ScheduledAt,
		}
		body := defaultReminderBody(w, j.Lead)
		to := j.Email
		if j.Channel == "sms" {
			to = j.Phone
		}
		if to == "" {
			a.markReminder(app, j.ID, "skipped", 0, "no destination address")
			continue
		}
		err := a.dispatchOneReminder(app, j.ProjectID, w, j.RegistrantID, j.Channel, to, j.Lead, body)
		if err == nil {
			a.markReminder(app, j.ID, "sent", 0, "")
		} else if errors.Is(err, errMessagingNotBound) {
			a.markReminder(app, j.ID, "skipped", 0, "messaging not bound")
		} else {
			a.markReminder(app, j.ID, "failed", 0, err.Error())
		}
	}
	return nil
}

// scheduleRemindersForRegistrant inserts pending reminder rows for
// this (webinar, registrant). One row per (lead, channel) combination
// where the channel has a destination.
func (a *App) scheduleRemindersForRegistrant(ctx *sdk.AppCtx, pid string, w *Webinar, registrantID int64, hasEmail, hasPhone bool) error {
	if w.ScheduledAt == "" {
		return nil
	}
	scheduled, err := time.Parse(time.RFC3339, w.ScheduledAt)
	if err != nil {
		return err
	}
	leads := a.reminderLeadHours(ctx)
	for _, hours := range leads {
		when := scheduled.Add(-time.Duration(hours * float64(time.Hour)))
		if when.Before(time.Now()) {
			continue
		}
		label := reminderLeadLabel(hours)
		channels := []string{}
		if hasEmail {
			channels = append(channels, "email")
		}
		if hasPhone {
			channels = append(channels, "sms")
		}
		for _, ch := range channels {
			if _, err := ctx.AppDB().Exec(
				`INSERT INTO webinar_reminders
					(project_id, webinar_id, registrant_id, channel, lead_label, scheduled_for)
				 VALUES (?, ?, ?, ?, ?, ?)`,
				pid, w.ID, registrantID, ch, label, when.UTC().Format(time.RFC3339)); err != nil {
				return err
			}
		}
	}
	return nil
}

// regenerateReminders wipes pending reminders for a webinar and rebuilds
// them with the current scheduled_at. Called from webinars_update.
func (a *App) regenerateReminders(ctx *sdk.AppCtx, pid string, webinarID int64) error {
	if _, err := ctx.AppDB().Exec(
		`DELETE FROM webinar_reminders
		 WHERE project_id = ? AND webinar_id = ? AND status = 'pending'`,
		pid, webinarID); err != nil {
		return err
	}
	w, err := a.dbGet(ctx, pid, webinarID)
	if err != nil || w == nil {
		return err
	}
	rows, err := ctx.AppDB().Query(
		`SELECT id, COALESCE(email,'') <> '', COALESCE(phone,'') <> ''
		 FROM webinar_registrants WHERE webinar_id = ?`, webinarID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var id int64
		var hasEmail, hasPhone bool
		if err := rows.Scan(&id, &hasEmail, &hasPhone); err != nil {
			continue
		}
		_ = a.scheduleRemindersForRegistrant(ctx, pid, w, id, hasEmail, hasPhone)
	}
	return nil
}

// dispatchOneReminder sends one reminder. On success: logs to CRM (when
// bound) and inserts an explicit sent-status row when the lead_label
// is non-empty (the manual webinars_send_reminder path passes
// lead='manual' so the row is created on the fly).
func (a *App) dispatchOneReminder(ctx *sdk.AppCtx, pid string, w *Webinar, registrantID int64, channel, to, lead, body string) error {
	subject := fmt.Sprintf("Reminder: %s", w.Title)
	from := defaultSenderForChannel(ctx, channel)
	idempotency := fmt.Sprintf("webinar:%d:reg:%d:lead:%s:ch:%s", w.ID, registrantID, lead, channel)

	resp, err := a.messagingCaller.SendMessage(MsgSendReq{
		Channel:        channel,
		To:             to,
		From:           from,
		Subject:        subject,
		Body:           body,
		IdempotencyKey: idempotency,
		ProjectID:      pid,
	})
	if err != nil {
		return err
	}

	// Best-effort CRM activity log.
	var contactID int64
	_ = ctx.AppDB().QueryRow(
		`SELECT COALESCE(contact_id, 0) FROM webinar_registrants WHERE id = ?`,
		registrantID).Scan(&contactID)
	if contactID != 0 {
		_ = a.crmCaller.LogActivity(CRMLogActivityReq{
			ContactID: contactID,
			Kind:      activityKindForChannel(channel, "sent"),
			Body:      fmt.Sprintf("[webinar reminder %s] %s", lead, w.Title),
			Source:    "webinars:reminder",
			ProjectID: pid,
		})
	}
	_ = resp
	return nil
}

func (a *App) markReminder(app *sdk.AppCtx, id int64, status string, msgID int64, errMsg string) {
	if status == "sent" {
		_, _ = app.AppDB().Exec(
			`UPDATE webinar_reminders
			 SET status = ?, sent_at = CURRENT_TIMESTAMP, messaging_id = ?, error = NULL
			 WHERE id = ?`, status, msgID, id)
		return
	}
	_, _ = app.AppDB().Exec(
		`UPDATE webinar_reminders
		 SET status = ?, sent_at = CURRENT_TIMESTAMP, error = ?
		 WHERE id = ?`, status, nullStr(errMsg), id)
}

func defaultReminderBody(w *Webinar, lead string) string {
	at := w.ScheduledAt
	if at == "" {
		at = "soon"
	}
	switch lead {
	case "T-24h":
		return fmt.Sprintf("Reminder: %q starts tomorrow at %s.", w.Title, at)
	case "T-1h":
		return fmt.Sprintf("Reminder: %q starts in one hour (%s).", w.Title, at)
	case "T-15m":
		return fmt.Sprintf("Reminder: %q starts in 15 minutes (%s).", w.Title, at)
	case "live":
		return fmt.Sprintf("We're live! Join %q now.", w.Title)
	default:
		return fmt.Sprintf("%q is scheduled for %s.", w.Title, at)
	}
}

func activityKindForChannel(channel, direction string) string {
	switch channel {
	case "email":
		return "email_" + direction
	case "sms":
		return "sms_" + direction
	case "whatsapp":
		return "whatsapp_" + direction
	}
	return "note"
}

func defaultSenderForChannel(ctx *sdk.AppCtx, channel string) string {
	switch channel {
	case "email":
		return strings.TrimSpace(ctx.Config().Get("default_sender_email"))
	case "sms", "whatsapp":
		return strings.TrimSpace(ctx.Config().Get("default_sender_phone"))
	}
	return ""
}

// ─── offer-broadcaster ───────────────────────────────────────────
//
// Every 5s: for each live webinar, find scripted offers whose
// (webinar.started_at + offset_seconds) <= now AND shown_at IS NULL,
// stamp shown_at + sequence + emit webinar.offer.shown.

func (a *App) runOfferBroadcaster(ctx context.Context, app *sdk.AppCtx) error {
	if app == nil || app.AppDB() == nil {
		return nil
	}
	rows, err := app.AppDB().Query(
		`SELECT id, project_id, started_at FROM webinars
		 WHERE status = 'live' AND started_at IS NOT NULL`)
	if err != nil {
		return err
	}
	type liveWebinar struct {
		ID                int64
		ProjectID, Started string
	}
	live := []liveWebinar{}
	for rows.Next() {
		var lw liveWebinar
		if err := rows.Scan(&lw.ID, &lw.ProjectID, &lw.Started); err == nil {
			live = append(live, lw)
		}
	}
	rows.Close()

	now := time.Now()
	for _, lw := range live {
		started, err := time.Parse(time.RFC3339, lw.Started)
		if err != nil {
			continue
		}
		offsetSecondsElapsed := int(now.Sub(started).Seconds())

		due, err := app.AppDB().Query(
			`SELECT id FROM webinar_offers
			 WHERE webinar_id = ? AND offset_seconds IS NOT NULL
			   AND offset_seconds <= ? AND shown_at IS NULL`,
			lw.ID, offsetSecondsElapsed)
		if err != nil {
			continue
		}
		offerIDs := []int64{}
		for due.Next() {
			var oid int64
			_ = due.Scan(&oid)
			offerIDs = append(offerIDs, oid)
		}
		due.Close()
		for _, oid := range offerIDs {
			seq := a.nextWebinarSequence(app, lw.ID)
			if _, err := app.AppDB().Exec(
				`UPDATE webinar_offers
				 SET shown_at = CURRENT_TIMESTAMP, sequence = ?
				 WHERE id = ?`, seq, oid); err != nil {
				continue
			}
			app.Emit("webinar.offer.shown", map[string]any{
				"webinar_id": lw.ID,
				"offer_id":   oid,
				"sequence":   seq,
			})
		}
	}
	return nil
}

// ─── attendance-decay ────────────────────────────────────────────
//
// Every 30s: mark attendance rows whose last_heartbeat is past
// viewer_idle_seconds as left, and bump attended_live / attended_replay
// flags on the registrant for at-a-glance filtering.

func (a *App) runAttendanceDecay(ctx context.Context, app *sdk.AppCtx) error {
	if app == nil || app.AppDB() == nil {
		return nil
	}
	idle := intArg(map[string]any{"x": app.Config().Get("viewer_idle_seconds")}, "x", 30)
	if v, err := time.ParseDuration(fmt.Sprintf("%ds", idle)); err == nil && v > 0 {
		idle = int(v.Seconds())
	}
	cutoff := time.Now().UTC().Add(-time.Duration(idle) * time.Second).Format(time.RFC3339)

	// Mark stale rows as left + flush watch_seconds (already accumulated
	// at heartbeat time, just close the row).
	if _, err := app.AppDB().Exec(
		`UPDATE webinar_attendance
		 SET left_at = CURRENT_TIMESTAMP
		 WHERE left_at IS NULL AND last_heartbeat < ?`, cutoff); err != nil {
		app.Logger().Warn("attendance-decay: mark left", "err", err)
	}

	// Promote attended_live / attended_replay flags on registrants
	// when at least one attendance row exists.
	_, _ = app.AppDB().Exec(
		`UPDATE webinar_registrants
		 SET attended_live = 1
		 WHERE attended_live = 0 AND id IN (
			SELECT registrant_id FROM webinar_attendance WHERE source = 'live'
		 )`)
	_, _ = app.AppDB().Exec(
		`UPDATE webinar_registrants
		 SET attended_replay = 1
		 WHERE attended_replay = 0 AND id IN (
			SELECT registrant_id FROM webinar_attendance WHERE source = 'replay'
		 )`)
	return nil
}

// ─── lifecycle: stream.* event handlers ──────────────────────────
//
// When streaming flips a stream's status, mirror it to the owning
// webinar — saves the operator from manually calling webinars_close.

func (a *App) handleStreamStarted(ctx *sdk.AppCtx, event sdk.Event) error {
	id, _ := event.Data["id"].(float64)
	if id == 0 {
		return nil
	}
	w, err := a.dbGetWebinarByStreamID(ctx, event.ProjectID, int64(id))
	if err != nil || w == nil {
		return err
	}
	if w.Status == "scheduled" || w.Status == "draft" {
		_, _ = ctx.AppDB().Exec(
			`UPDATE webinars SET status='live', started_at = CURRENT_TIMESTAMP WHERE id = ?`,
			w.ID)
		ctx.Emit("webinar.live", map[string]any{"id": w.ID})
		// Fire the "we're live" reminder blast immediately.
		_, _ = a.toolSendReminder(ctx, map[string]any{
			"_project_id": event.ProjectID,
			"id":          w.ID,
			"audience":    "registered",
			"body":        defaultReminderBody(w, "live"),
		})
	}
	return nil
}

func (a *App) handleStreamEnded(ctx *sdk.AppCtx, event sdk.Event) error {
	id, _ := event.Data["id"].(float64)
	if id == 0 {
		return nil
	}
	w, err := a.dbGetWebinarByStreamID(ctx, event.ProjectID, int64(id))
	if err != nil || w == nil {
		return err
	}
	if w.Status == "live" {
		_, _ = ctx.AppDB().Exec(
			`UPDATE webinars SET status='ended', ended_at = CURRENT_TIMESTAMP WHERE id = ?`,
			w.ID)
		ctx.Emit("webinar.ended", map[string]any{"id": w.ID})
	}
	return nil
}

func (a *App) handleStreamErrored(ctx *sdk.AppCtx, event sdk.Event) error {
	id, _ := event.Data["id"].(float64)
	if id == 0 {
		return nil
	}
	w, err := a.dbGetWebinarByStreamID(ctx, event.ProjectID, int64(id))
	if err != nil || w == nil {
		return err
	}
	if w.Status == "live" {
		_, _ = ctx.AppDB().Exec(
			`UPDATE webinars SET status='ended', ended_at = CURRENT_TIMESTAMP WHERE id = ?`,
			w.ID)
		ctx.Emit("webinar.ended", map[string]any{"id": w.ID, "errored": true})
	}
	return nil
}
