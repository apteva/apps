package main

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// ─── reminder-scheduler ──────────────────────────────────────────
//
// Every minute: retire elapsed slots, then scan webinar_reminders for
// status='pending' rows whose scheduled_for has passed and dispatch each
// via messaging, marking the row sent | skipped | failed.
//
// Runs as a Worker — apteva-server schedules it.

// projectFilter renders an optional `AND <col> = ?` for a worker query.
//
// apteva-server dispatches every Worker once PER PROJECT each tick
// (see the SDK's runWorker/dispatchProjects). Every worker in this file
// was written as if it ran once globally — project-agnostic scans,
// project-agnostic UPDATEs — so a 50-project install repeated the same
// full scan 50 times per tick against a pool the SDK pins to ONE
// connection. Sequential dispatch kept it correct; it was purely
// wasted writer time, on the one resource everything else here is
// tuned around.
//
// An empty project (single-project install, or a fresh one with none
// created yet) means "no filter" and restores the old behaviour.
func projectFilter(app *sdk.AppCtx, col string) (string, []any) {
	pid := app.CurrentProject()
	if pid == "" {
		return "", nil
	}
	return " AND " + col + " = ?", []any{pid}
}

// reminderTickBudget caps how long one scheduler tick will keep pulling
// batches, so a large wave drains across several batches inside one tick
// instead of trickling out at one batch per minute — but never overruns
// its own schedule.
const reminderTickBudget = 45 * time.Second

func (a *App) runReminderScheduler(ctx context.Context, app *sdk.AppCtx) error {
	if app == nil || app.AppDB() == nil {
		return nil
	}
	// Slot lifecycle rides along on this tick: it's the same cadence and
	// it keeps the number of scheduled workers down.
	if err := a.sweepSlotStatuses(app); err != nil {
		app.Logger().Warn("slot status sweep", "err", err)
	}

	batchSize := configInt(app, "reminder_batch_size", 500)
	if batchSize <= 0 || batchSize > 5000 {
		batchSize = 500
	}
	concurrency := a.reminderConcurrency(app)
	deadline := time.Now().Add(reminderTickBudget)

	for {
		jobs, err := a.dueReminders(app, batchSize)
		if err != nil {
			return err
		}
		if len(jobs) == 0 {
			return nil
		}
		stuck := a.dispatchReminderBatch(app, jobs, concurrency)
		if stuck > 0 {
			// Those rows are still 'pending' and the next dueReminders
			// would hand them straight back, re-sending each one for the
			// rest of the tick budget. Give up this tick instead.
			app.Logger().Warn("reminder rows could not be closed out; ending tick early",
				"stuck", stuck, "batch", len(jobs))
			return nil
		}
		if len(jobs) < batchSize || time.Now().After(deadline) {
			return nil
		}
		select {
		case <-ctx.Done():
			return nil
		default:
		}
	}
}

// reminderJob is one pending row joined to everything the dispatch needs.
type reminderJob struct {
	ID, WebinarID, RegistrantID int64
	ProjectID, Channel, Lead    string
	ScheduledFor                string
	Email, Phone, Name, Token   string
	Title, StartsAt, Timezone   string
}

// dueReminders pulls the next batch of dispatchable rows.
//
// The webinar-status filter is the important part: the query used to
// join webinars without looking at status at all, so a webinar that
// started early at 14:30 and ended at 14:50 still fired its "starts in
// 15 minutes" reminder at 14:45, and every remaining pending row kept
// going out after the webinar was over.
//
// The start time comes from the registrant's own slot when they have
// one, so multi-slot webinars quote the time that registrant actually
// signed up for rather than the webinar-level scheduled_at.
func (a *App) dueReminders(app *sdk.AppCtx, limit int) ([]reminderJob, error) {
	scope, scopeArgs := projectFilter(app, "r.project_id")
	args := append([]any{nowRFC3339()}, scopeArgs...)
	args = append(args, limit)
	rows, err := app.AppDB().Query(
		`SELECT r.id, r.project_id, r.webinar_id, r.registrant_id,
				r.channel, r.lead_label, r.scheduled_for,
				COALESCE(reg.email,''), COALESCE(reg.phone,''),
				COALESCE(reg.display_name,''), reg.join_token,
				w.title, COALESCE(s.starts_at, w.scheduled_at, ''),
				COALESCE(s.timezone, w.timezone, 'UTC')
		 FROM webinar_reminders r
		 JOIN webinar_registrants reg ON reg.id = r.registrant_id
		 JOIN webinars w ON w.id = r.webinar_id
		 LEFT JOIN webinar_slots s ON s.id = reg.slot_id
		 WHERE r.status = 'pending' AND r.scheduled_for <= ?
		   AND w.status IN ('draft','scheduled','live')`+scope+`
		 ORDER BY r.scheduled_for ASC
		 LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	jobs := []reminderJob{}
	for rows.Next() {
		var j reminderJob
		if err := rows.Scan(&j.ID, &j.ProjectID, &j.WebinarID, &j.RegistrantID,
			&j.Channel, &j.Lead, &j.ScheduledFor, &j.Email, &j.Phone, &j.Name,
			&j.Token, &j.Title, &j.StartsAt, &j.Timezone); err == nil {
			jobs = append(jobs, j)
		}
	}
	return jobs, rows.Err()
}

// dispatchReminderBatch fans the batch out across a bounded pool. Each
// job is a messaging call plus a CRM call at ~100ms apiece; serially,
// a 500-row batch took 50–100s and overran its own one-minute tick,
// capping throughput at ~500/min — so the T-15m wave for a big webinar
// landed 20+ minutes late.
//
// Returns the number of rows that were dispatched but could not be
// marked, so the caller can stop re-reading them.
func (a *App) dispatchReminderBatch(app *sdk.AppCtx, jobs []reminderJob, concurrency int) int {
	var stuck atomic.Int64
	runBounded(jobs, concurrency, guarded(app, "reminder-dispatch", func(j reminderJob) {
		w := &Webinar{
			ID:          j.WebinarID,
			ProjectID:   j.ProjectID,
			Title:       j.Title,
			ScheduledAt: j.StartsAt,
			Timezone:    j.Timezone,
		}
		to := j.Email
		if j.Channel == "sms" {
			to = j.Phone
		}
		if to == "" {
			a.markReminder(app, j.ID, "skipped", 0, "no destination address")
			return
		}
		msgID, err := a.dispatchOneReminder(app, j.ProjectID, w, reminderDispatch{
			RegistrantID: j.RegistrantID,
			Channel:      j.Channel,
			To:           to,
			Lead:         j.Lead,
			Body:         a.reminderBody(app, w, j.Lead, a.joinURL(app, j.Token)),
			IdemSuffix:   "at:" + j.ScheduledFor,
		})
		var markErr error
		switch {
		case err == nil:
			markErr = a.markReminder(app, j.ID, "sent", msgID, "")
		case errors.Is(err, errMessagingNotBound):
			markErr = a.markReminder(app, j.ID, "skipped", 0, "messaging not bound")
		default:
			markErr = a.markReminder(app, j.ID, "failed", 0, err.Error())
		}
		if markErr != nil {
			stuck.Add(1)
		}
	}))
	return int(stuck.Load())
}

// reminderConcurrency — how many reminder dispatches run in flight.
func (a *App) reminderConcurrency(ctx *sdk.AppCtx) int {
	n := configInt(ctx, "reminder_concurrency", 8)
	if n < 1 {
		n = 1
	}
	if n > 64 {
		n = 64
	}
	return n
}

// runBounded applies fn to every item with at most `concurrency` in
// flight. fn must be safe to call concurrently.
//
// Wrap fn in guarded() when it can panic — these pools run inside
// scheduled workers, where an unrecovered panic in a goroutine takes
// the whole sidecar down, live room and all.
func runBounded[T any](items []T, concurrency int, fn func(T)) {
	if len(items) == 0 {
		return
	}
	if concurrency < 1 {
		concurrency = 1
	}
	if concurrency > len(items) {
		concurrency = len(items)
	}
	if concurrency == 1 {
		for _, it := range items {
			fn(it)
		}
		return
	}
	ch := make(chan T)
	var wg sync.WaitGroup
	for i := 0; i < concurrency; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for it := range ch {
				fn(it)
			}
		}()
	}
	for _, it := range items {
		ch <- it
	}
	close(ch)
	wg.Wait()
}

// ─── Reminder scheduling ─────────────────────────────────────────

// guarded contains a panic to the one item that caused it, logging it
// through the app's own logger rather than taking the process with it.
func guarded[T any](app *sdk.AppCtx, label string, fn func(T)) func(T) {
	return func(it T) {
		defer func() {
			if r := recover(); r != nil {
				app.Logger().Error("recovered panic", "where", label, "panic", fmt.Sprint(r))
			}
		}()
		fn(it)
	}
}

// plannedReminder is one row scheduleReminders is about to write.
type plannedReminder struct {
	RegistrantID int64
	Channel      string
	Lead         string
	ScheduledFor string
}

// planReminders computes the (lead × channel) rows for one registrant
// against a specific start time. Leads already in the past are dropped.
func (a *App) planReminders(ctx *sdk.AppCtx, startsAt string, registrantID int64, hasEmail, hasPhone bool) ([]plannedReminder, error) {
	if startsAt == "" {
		return nil, nil
	}
	scheduled, err := parseDBTime(startsAt)
	if err != nil {
		return nil, err
	}
	channels := []string{}
	if hasEmail {
		channels = append(channels, "email")
	}
	if hasPhone {
		channels = append(channels, "sms")
	}
	now := nowUTC()
	out := []plannedReminder{}
	for _, hours := range a.reminderLeadHours(ctx) {
		when := scheduled.Add(-time.Duration(hours * float64(time.Hour)))
		if when.Before(now) {
			continue
		}
		label := reminderLeadLabel(hours)
		for _, ch := range channels {
			out = append(out, plannedReminder{
				RegistrantID: registrantID,
				Channel:      ch,
				Lead:         label,
				ScheduledFor: formatRFC3339(when),
			})
		}
	}
	return out, nil
}

// insertReminders writes a planned set in ONE transaction.
//
// Two problems this closes: the old code issued up to six autocommit
// INSERTs per registrant (regenerateReminders looped every registrant,
// so a 5000-registrant reschedule was ~30k individual WAL commits on
// the single connection, stalling chat and heartbeats for any
// concurrently-live webinar), and it had no conflict handling at all —
// so a duplicate registration inserted a second full set of pending
// rows and only messaging-side de-duplication hid the double send.
func insertReminders(tx sqlExecer, pid string, webinarID int64, rows []plannedReminder) error {
	for _, r := range rows {
		if _, err := tx.Exec(
			`INSERT INTO webinar_reminders
				(project_id, webinar_id, registrant_id, channel, lead_label, scheduled_for)
			 VALUES (?, ?, ?, ?, ?, ?)
			 ON CONFLICT(registrant_id, channel, lead_label, scheduled_for) DO NOTHING`,
			pid, webinarID, r.RegistrantID, r.Channel, r.Lead, r.ScheduledFor); err != nil {
			return err
		}
	}
	return nil
}

// scheduleRemindersForRegistrant inserts pending reminder rows for this
// (webinar, registrant): one per (lead, channel) where the channel has a
// destination. Idempotent — safe to re-run on a duplicate submit.
func (a *App) scheduleRemindersForRegistrant(ctx *sdk.AppCtx, pid string, w *Webinar, registrantID int64, hasEmail, hasPhone bool) error {
	rows, err := a.planReminders(ctx, w.ScheduledAt, registrantID, hasEmail, hasPhone)
	if err != nil || len(rows) == 0 {
		return err
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := insertReminders(tx, pid, w.ID, rows); err != nil {
		return err
	}
	return tx.Commit()
}

// regenerateReminders rebuilds every registrant's pending reminders
// after a reschedule.
//
// Fail-safe by construction: the whole replacement set is computed
// FIRST, and only then does one transaction delete and re-insert. The
// old version deleted first and rebuilt after, so an unparseable
// scheduled_at (which webinars_update let through unvalidated) left the
// webinar with zero reminders and a logged warning.
//
// Each registrant is rebuilt from THEIR OWN slot's time when they have
// one. The old version rebuilt everyone from the webinar-level
// scheduled_at, which flattened every per-slot time in a multi-slot
// webinar onto one date.
func (a *App) regenerateReminders(ctx *sdk.AppCtx, pid string, webinarID int64) error {
	w, err := a.dbGet(ctx, pid, webinarID)
	if err != nil || w == nil {
		return err
	}

	rows, err := ctx.AppDB().Query(
		`SELECT reg.id,
				COALESCE(reg.email,'') <> '', COALESCE(reg.phone,'') <> '',
				COALESCE(s.starts_at, ?)
		 FROM webinar_registrants reg
		 LEFT JOIN webinar_slots s ON s.id = reg.slot_id
		 WHERE reg.project_id = ? AND reg.webinar_id = ?`,
		w.ScheduledAt, pid, webinarID)
	if err != nil {
		return err
	}
	planned := []plannedReminder{}
	var scanErr error
	for rows.Next() {
		var id int64
		var hasEmail, hasPhone bool
		var startsAt string
		if err := rows.Scan(&id, &hasEmail, &hasPhone, &startsAt); err != nil {
			scanErr = err
			break
		}
		set, err := a.planReminders(ctx, startsAt, id, hasEmail, hasPhone)
		if err != nil {
			// An unparseable start time is a data problem, not a reason
			// to destroy this webinar's whole reminder schedule.
			scanErr = fmt.Errorf("registrant %d: %w", id, err)
			break
		}
		planned = append(planned, set...)
	}
	rows.Close()
	if scanErr != nil {
		return scanErr
	}
	if err := rows.Err(); err != nil {
		return err
	}

	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if _, err := tx.Exec(
		`DELETE FROM webinar_reminders
		 WHERE project_id = ? AND webinar_id = ? AND status = 'pending'`,
		pid, webinarID); err != nil {
		return err
	}
	if err := insertReminders(tx, pid, webinarID, planned); err != nil {
		return err
	}
	return tx.Commit()
}

// enqueueLiveBlast queues the "we're live" message for every registrant
// as ordinary pending reminder rows, for the scheduler to fan out on its
// next tick.
//
// This used to be a synchronous toolSendReminder call inside the
// stream.started event handler: it loaded the whole recipient set and
// did a messaging call plus a CRM call per registrant per channel, so
// 5000 registrants meant up to 10k serial cross-app calls blocking the
// handler for minutes — at exactly the moment live-room load spikes.
// Enqueuing also means the blast finally leaves an audit trail; the
// direct path wrote no reminder rows at all.
func (a *App) enqueueLiveBlast(ctx *sdk.AppCtx, pid string, w *Webinar) error {
	rows, err := ctx.AppDB().Query(
		`SELECT id, COALESCE(email,'') <> '', COALESCE(phone,'') <> ''
		 FROM webinar_registrants WHERE project_id = ? AND webinar_id = ?`,
		pid, w.ID)
	if err != nil {
		return err
	}
	at := nowRFC3339()
	planned := []plannedReminder{}
	for rows.Next() {
		var id int64
		var hasEmail, hasPhone bool
		if err := rows.Scan(&id, &hasEmail, &hasPhone); err != nil {
			continue
		}
		if hasEmail {
			planned = append(planned, plannedReminder{id, "email", "live", at})
		}
		if hasPhone {
			planned = append(planned, plannedReminder{id, "sms", "live", at})
		}
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return err
	}
	if len(planned) == 0 {
		return nil
	}

	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	if err := insertReminders(tx, pid, w.ID, planned); err != nil {
		return err
	}
	return tx.Commit()
}

// ─── Dispatch ────────────────────────────────────────────────────

// reminderDispatch is one send.
type reminderDispatch struct {
	RegistrantID int64
	Channel      string
	To           string
	Lead         string
	Body         string
	// IdemSuffix disambiguates repeat sends that share the same
	// (webinar, registrant, lead, channel): the reminder row's
	// scheduled_for for scheduled sends, a per-invocation generation
	// stamp for manual ones.
	IdemSuffix string
}

// dispatchOneReminder sends one reminder and returns messaging's record
// id. On success it also logs a CRM activity when CRM is bound.
//
// The idempotency key carries IdemSuffix because the old key —
// "webinar:N:reg:M:lead:L:ch:C" — was identical across genuinely
// distinct sends. Move a webinar a week later after the T-24h email
// already went out, and the regenerated T-24h reminder dispatched with
// the SAME key: messaging de-duplicated it, the registrant never heard
// about the new date, and the row was still marked `sent`. Same defect
// silently swallowed every repeat manual send and every we're-live
// blast after the first.
func (a *App) dispatchOneReminder(ctx *sdk.AppCtx, pid string, w *Webinar, d reminderDispatch) (int64, error) {
	subject := fmt.Sprintf("Reminder: %s", w.Title)
	from := defaultSenderForChannel(ctx, d.Channel)
	idempotency := fmt.Sprintf("webinar:%d:reg:%d:lead:%s:ch:%s",
		w.ID, d.RegistrantID, d.Lead, d.Channel)
	if d.IdemSuffix != "" {
		idempotency += ":" + d.IdemSuffix
	}

	resp, err := a.messagingCaller.SendMessage(MsgSendReq{
		Channel:        d.Channel,
		To:             d.To,
		From:           from,
		Subject:        subject,
		Body:           d.Body,
		IdempotencyKey: idempotency,
		ProjectID:      pid,
	})
	if err != nil {
		return 0, err
	}

	// Best-effort CRM activity log.
	var contactID int64
	_ = ctx.AppDB().QueryRow(
		`SELECT COALESCE(contact_id, 0) FROM webinar_registrants WHERE id = ?`,
		d.RegistrantID).Scan(&contactID)
	if contactID != 0 {
		_ = a.crmCaller.LogActivity(CRMLogActivityReq{
			ContactID: contactID,
			Kind:      activityKindForChannel(d.Channel, "sent"),
			Body:      fmt.Sprintf("[webinar reminder %s] %s", d.Lead, w.Title),
			Source:    "webinars:reminder",
			ProjectID: pid,
		})
	}
	return resp.ID, nil
}

// markReminder closes out one reminder row. msgID is messaging's record
// id — the audit column existed from day one but every row was written
// with 0, so "did registrant 9 get the T-1h SMS?" had no answer.
// markReminder closes out one reminder row and reports whether the row
// actually moved.
//
// It used to discard both the error and the row count. A reminder whose
// UPDATE kept failing stayed 'pending', so the scheduler's batch loop
// re-fetched the same rows and re-sent them for the rest of its 45s
// budget — the failure was invisible and the cost was a send storm.
func (a *App) markReminder(app *sdk.AppCtx, id int64, status string, msgID int64, errMsg string) error {
	var res sql.Result
	var err error
	if status == "sent" {
		res, err = app.AppDB().Exec(
			`UPDATE webinar_reminders
			 SET status = ?, sent_at = ?, messaging_id = ?, error = NULL
			 WHERE id = ?`, status, nowRFC3339(), nullablePositiveInt64(msgID), id)
	} else {
		res, err = app.AppDB().Exec(
			`UPDATE webinar_reminders
			 SET status = ?, sent_at = ?, error = ?
			 WHERE id = ?`, status, nowRFC3339(), nullStr(errMsg), id)
	}
	if err != nil {
		app.Logger().Warn("mark reminder", "reminder_id", id, "status", status, "err", err)
		return err
	}
	if n, rErr := res.RowsAffected(); rErr == nil && n == 0 {
		return fmt.Errorf("reminder %d not updated", id)
	}
	return nil
}

// nullablePositiveInt64 keeps messaging_id NULL rather than 0 when the
// provider gave us nothing to record.
func nullablePositiveInt64(v int64) any {
	if v <= 0 {
		return nil
	}
	return v
}

// reminderBody renders the message a registrant actually receives.
//
// Two things were wrong with the version this replaces, and both made
// the reminder useless rather than merely ugly:
//
//   - No join link. Ever. The join token was selected on both dispatch
//     paths and then dropped on the floor, so "We're live! Join now."
//     arrived with nothing to click. The only time a registrant ever
//     saw their room URL was the redirect immediately after they
//     registered — close that tab and the funnel was over.
//   - Raw storage timestamps. "starts tomorrow at 2026-09-20T15:00:00Z"
//     is what the recipient read, while webinars.timezone (captured at
//     create time precisely for this) went unused.
func (a *App) reminderBody(ctx *sdk.AppCtx, w *Webinar, lead, joinURL string) string {
	at := formatWebinarTime(w.ScheduledAt, w.Timezone)
	var head string
	switch {
	case lead == "live":
		head = fmt.Sprintf("We're live! %s has started.", w.Title)
	case at == "":
		head = fmt.Sprintf("%s is starting soon.", w.Title)
	case lead == "T-24h":
		head = fmt.Sprintf("Reminder: %s starts tomorrow, %s.", w.Title, at)
	case lead == "T-1h":
		head = fmt.Sprintf("Reminder: %s starts in one hour, at %s.", w.Title, at)
	case lead == "T-15m":
		head = fmt.Sprintf("Reminder: %s starts in 15 minutes, at %s.", w.Title, at)
	default:
		head = fmt.Sprintf("%s is scheduled for %s.", w.Title, at)
	}
	return appendJoinLink(head, joinURL)
}

// appendJoinLink adds the registrant's room URL to a message body,
// unless the caller already put one there (a custom body from
// webinars_send_reminder may well have).
func appendJoinLink(body, joinURL string) string {
	if joinURL == "" || strings.Contains(body, joinURL) {
		return body
	}
	return body + "\n\nJoin here: " + joinURL
}

// formatWebinarTime renders a stored UTC timestamp for a human, in the
// webinar's own timezone. Returns "" when there is no usable time.
//
// time/tzdata is imported in main.go so LoadLocation works on a scratch
// container with no /usr/share/zoneinfo.
func formatWebinarTime(iso, tz string) string {
	t, err := parseDBTime(iso)
	if err != nil {
		return ""
	}
	loc := time.UTC
	if tz = strings.TrimSpace(tz); tz != "" {
		if l, lerr := time.LoadLocation(tz); lerr == nil {
			loc = l
		}
	}
	return t.In(loc).Format("Mon 2 Jan 2006 at 15:04 MST")
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

// ─── Slot lifecycle ──────────────────────────────────────────────
//
// webinar_slots.status was written once at creation and never
// transitioned, so yesterday's slot stayed 'scheduled' — and therefore
// "available" — forever. The sweep is deterministic and time-driven:
// an active slot whose end has passed becomes 'ended'; a scheduled slot
// that has started but not ended becomes 'live'.
//
// Lexical comparison is sound here because every slot time is stored as
// UTC RFC3339 (see timeutil.go + migration 003).

func (a *App) sweepSlotStatuses(app *sdk.AppCtx) error {
	now := nowRFC3339()
	scope, scopeArgs := projectFilter(app, "project_id")
	if _, err := app.AppDB().Exec(
		`UPDATE webinar_slots SET status = 'ended', updated_at = ?
		 WHERE status IN ('scheduled','open','live')
		   AND COALESCE(NULLIF(ends_at,''), starts_at) <= ?`+scope,
		append([]any{now, now}, scopeArgs...)...); err != nil {
		return err
	}
	_, err := app.AppDB().Exec(
		`UPDATE webinar_slots SET status = 'live', updated_at = ?
		 WHERE status IN ('scheduled','open')
		   AND starts_at <= ?
		   AND COALESCE(NULLIF(ends_at,''), starts_at) > ?`+scope,
		append([]any{now, now, now}, scopeArgs...)...)
	return err
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
	scope, scopeArgs := projectFilter(app, "project_id")
	rows, err := app.AppDB().Query(
		`SELECT id, project_id, started_at FROM webinars
		 WHERE status = 'live' AND started_at IS NOT NULL`+scope, scopeArgs...)
	if err != nil {
		return err
	}
	type liveWebinar struct {
		ID                 int64
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

	now := nowUTC()
	for _, lw := range live {
		// Tolerant parse. This is where scripted offers died: started_at
		// was written with SQL CURRENT_TIMESTAMP ("2026-08-18 09:06:00"),
		// time.Parse(time.RFC3339, …) rejected it, and the `continue`
		// skipped every live webinar on every tick — so no scripted offer
		// ever fired in production. Writes are RFC3339 now; this stays
		// tolerant for rows that predate the migration.
		started, err := parseDBTime(lw.Started)
		if err != nil {
			app.Logger().Warn("offer-broadcaster: unparseable started_at",
				"webinar_id", lw.ID, "started_at", lw.Started)
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
			// `AND shown_at IS NULL` + RowsAffected, so the stamp is the
			// thing that decides whether this tick owns the broadcast.
			// Without it any re-entrant or duplicated tick would re-stamp
			// an already-shown offer with a fresh sequence and emit it to
			// the room a second time.
			res, err := app.AppDB().Exec(
				`UPDATE webinar_offers
				 SET shown_at = ?, sequence = ?
				 WHERE id = ? AND shown_at IS NULL`, nowRFC3339(), seq, oid)
			if err != nil {
				continue
			}
			if n, _ := res.RowsAffected(); n == 0 {
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
// Every 30s: close attendance rows whose last_heartbeat is past
// viewer_idle_seconds, and backstop the attended_live / attended_replay
// promotion the flush worker normally handles.
//
// Both promote statements used to be unscoped — they subselected every
// 'live' attendance row that ever existed and scanned
// webinar_registrants WHERE attended_live = 0 with no index, as WRITE
// statements holding the single writer, every 30 seconds, on tables
// nothing ever pruned. They're now bounded to rows touched since the
// previous sweep.

func (a *App) runAttendanceDecay(ctx context.Context, app *sdk.AppCtx) error {
	if app == nil || app.AppDB() == nil {
		return nil
	}
	idle := a.viewerIdleSeconds(app)
	now := nowUTC()
	cutoff := formatRFC3339(now.Add(-time.Duration(idle) * time.Second))
	scope, scopeArgs := projectFilter(app, "project_id")

	if _, err := app.AppDB().Exec(
		`UPDATE webinar_attendance
		 SET left_at = ?
		 WHERE left_at IS NULL AND last_heartbeat < ?`+scope,
		append([]any{nowRFC3339(), cutoff}, scopeArgs...)...); err != nil {
		app.Logger().Warn("attendance-decay: mark left", "err", err)
	}

	// Backstop promotion for rows the flush worker didn't cover.
	// Placeholders run in source order: the outer project filter, the
	// watermark, then the subquery's project filter.
	since := a.promoteWatermark(now)
	promote := func(column, source string) {
		args := make([]any, 0, len(scopeArgs)*2+1)
		args = append(args, scopeArgs...)
		args = append(args, since)
		args = append(args, scopeArgs...)
		if _, err := app.AppDB().Exec(
			`UPDATE webinar_registrants
			 SET `+column+` = 1
			 WHERE `+column+` = 0`+scope+` AND id IN (
				SELECT registrant_id FROM webinar_attendance
				 WHERE source = ? AND last_heartbeat >= ?`+scope+`
			 )`, insertSourceArg(args, source, len(scopeArgs))...); err != nil {
			app.Logger().Warn("attendance-decay: promote "+source, "err", err)
		}
	}
	promote("attended_live", "live")
	promote("attended_replay", "replay")
	return nil
}

// insertSourceArg splices the attendance source in ahead of the
// watermark, so the subquery binds (source, last_heartbeat) rather than
// interpolating the value into the SQL text.
func insertSourceArg(args []any, source string, at int) []any {
	out := make([]any, 0, len(args)+1)
	out = append(out, args[:at]...)
	out = append(out, source)
	out = append(out, args[at:]...)
	return out
}

// promoteWatermark returns the lower bound for this sweep's promotion
// scan and advances it. The first sweep after a restart looks back an
// hour so nothing written while the sidecar was down is missed.
func (a *App) promoteWatermark(now time.Time) string {
	a.sweepMu.Lock()
	defer a.sweepMu.Unlock()
	since := a.lastPromoteSweep
	if since.IsZero() {
		since = now.Add(-time.Hour)
	}
	a.lastPromoteSweep = now
	// Overlap by one window so a row written between the query and the
	// watermark update isn't skipped.
	return formatRFC3339(since.Add(-time.Minute))
}

// ─── lifecycle: stream.* event handlers ──────────────────────────
//
// When streaming flips a stream's status, mirror it to the owning
// webinar — saves the operator from manually calling webinars_close.

// eventInt64 extracts a numeric event field across every shape a JSON
// bus can hand back. The handlers used to assert `.(float64)` only,
// which yields 0 — and therefore an early return — the moment the bus
// delivers an int64 or a json.Number, silently stopping all
// stream-lifecycle mirroring.
func eventInt64(data map[string]any, key string) int64 {
	if data == nil {
		return 0
	}
	return int64Arg(data, key)
}

func (a *App) handleStreamStarted(ctx *sdk.AppCtx, event sdk.Event) error {
	id := eventInt64(event.Data, "id")
	if id == 0 {
		return nil
	}
	w, err := a.dbGetWebinarByStreamID(ctx, event.ProjectID, id)
	if err != nil || w == nil {
		return err
	}
	if w.Status == "scheduled" || w.Status == "draft" {
		if _, err := ctx.AppDB().Exec(
			`UPDATE webinars SET status='live', started_at = ? WHERE id = ?`,
			nowRFC3339(), w.ID); err != nil {
			return err
		}
		ctx.Emit("webinar.live", map[string]any{"id": w.ID})
		// Queue the "we're live" blast; the reminder scheduler drains it
		// on its next tick rather than blocking this handler.
		if err := a.enqueueLiveBlast(ctx, event.ProjectID, w); err != nil {
			ctx.Logger().Warn("enqueue live blast", "id", w.ID, "err", err)
		}
	}
	return nil
}

func (a *App) handleStreamEnded(ctx *sdk.AppCtx, event sdk.Event) error {
	return a.endWebinarForStream(ctx, event, false)
}

func (a *App) handleStreamErrored(ctx *sdk.AppCtx, event sdk.Event) error {
	return a.endWebinarForStream(ctx, event, true)
}

func (a *App) endWebinarForStream(ctx *sdk.AppCtx, event sdk.Event, errored bool) error {
	id := eventInt64(event.Data, "id")
	if id == 0 {
		return nil
	}
	w, err := a.dbGetWebinarByStreamID(ctx, event.ProjectID, id)
	if err != nil || w == nil {
		return err
	}
	if w.Status != "live" {
		return nil
	}
	if _, err := ctx.AppDB().Exec(
		`UPDATE webinars SET status='ended', ended_at = ? WHERE id = ?`,
		nowRFC3339(), w.ID); err != nil {
		return err
	}
	// Anything still pending is for a webinar that's over.
	if _, err := ctx.AppDB().Exec(
		`UPDATE webinar_reminders
		 SET status = 'skipped', sent_at = ?, error = 'webinar ended'
		 WHERE project_id = ? AND webinar_id = ? AND status = 'pending'`,
		nowRFC3339(), event.ProjectID, w.ID); err != nil {
		ctx.Logger().Warn("stand down pending reminders", "id", w.ID, "err", err)
	}
	data := map[string]any{"id": w.ID}
	if errored {
		data["errored"] = true
	}
	ctx.Emit("webinar.ended", data)
	return nil
}
