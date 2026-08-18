package main

import (
	"database/sql"
	"encoding/json"
	"errors"

	sdk "github.com/apteva/app-sdk"
)

// Live-room write path: sequence allocation plus the ownership-checked
// entry points the public handlers use for poll responses and offer
// clicks.

// ─── Atomic per-webinar sequence ──────────────────────────────────

// nextWebinarSequence allocates the next monotonic sequence number for a
// webinar's live-room event stream (chat + offers + polls share one
// space so /events can merge them into a single ordered feed).
//
// This used to be MAX(sequence)+1 across a three-table UNION: a
// read-then-insert with no atomicity, so two concurrent chat posts both
// computed 7, both inserted 7, and the viewer's `sequence > since`
// cursor stepped past 7 after delivering only one of them — silent
// message loss under exactly the load the live room is built for. It
// also scanned webinar_polls, which had no (webinar_id, sequence)
// index.
//
// The counter now lives in its own row and is bumped with a single
// atomic upsert. Callers must use the returned value verbatim.
func (a *App) nextWebinarSequence(ctx *sdk.AppCtx, webinarID int64) int {
	return a.nextSequence(ctx, webinarID, "event")
}

// nextSequence bumps one named counter for a webinar and returns the new
// value. `kind` partitions counters that must not share a space —
// "event" for the live-room feed, "manual_send" for manual reminder
// generations.
func (a *App) nextSequence(ctx *sdk.AppCtx, webinarID int64, kind string) int {
	if ctx == nil || ctx.AppDB() == nil {
		return 0
	}
	var seq int
	err := ctx.AppDB().QueryRow(
		`INSERT INTO webinar_sequences (webinar_id, kind, value)
		 VALUES (?, ?, 1)
		 ON CONFLICT(webinar_id, kind) DO UPDATE SET value = value + 1
		 RETURNING value`, webinarID, kind).Scan(&seq)
	if err != nil || seq < 1 {
		// A counter row can only fail to materialize if the DB is in
		// trouble; fall back to the old scan so the live room degrades
		// rather than stops.
		ctx.Logger().Warn("sequence allocation fell back to scan",
			"webinar_id", webinarID, "kind", kind, "err", err)
		return a.legacyMaxSequence(ctx, webinarID)
	}
	return seq
}

// legacyMaxSequence is the pre-v0.2 MAX+1 scan, kept only as the
// fallback path for nextSequence.
func (a *App) legacyMaxSequence(ctx *sdk.AppCtx, webinarID int64) int {
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

// ─── Ownership-checked engagement writes ──────────────────────────
//
// poll_id / offer_id arrive from the browser as sequential INTEGER
// PRIMARY KEYs. Writing them without checking who they belong to lets
// any registrant of any webinar enumerate ids and stuff another
// webinar's poll results and offer CTR — and under `scope: global`,
// another tenant's. Both helpers resolve the id back to the caller's
// (project, webinar) before writing, and the poll path additionally
// refuses votes after closes_at.

var (
	// errEngagementNotFound covers "no such id" and "belongs to someone
	// else" — the caller must not be able to tell those apart, or the
	// error itself becomes an id-enumeration oracle. Map to 404.
	errEngagementNotFound = simpleErr("not found")
	// errPollClosed — the poll's closes_at has passed. Map to 409.
	errPollClosed = simpleErr("poll is closed")
	// errInvalidChoice — choice index outside the poll's choices array.
	// Map to 400.
	errInvalidChoice = simpleErr("invalid choice")
)

// RecordPollResponse stores one registrant's answer after verifying the
// poll belongs to this webinar and project, is still open, and that the
// choice index is in range. Re-answering overwrites the previous choice.
//
// Errors are the sentinels above; compare with errors.Is.
func (a *App) RecordPollResponse(ctx *sdk.AppCtx, w *Webinar, reg *Registrant, pollID int64, choiceIndex int) error {
	if ctx == nil || w == nil || reg == nil {
		return errEngagementNotFound
	}
	if pollID <= 0 {
		return errEngagementNotFound
	}
	var closesAt sql.NullString
	var choicesJSON string
	err := ctx.AppDB().QueryRow(
		`SELECT COALESCE(closes_at,''), choices FROM webinar_polls
		 WHERE id = ? AND webinar_id = ? AND project_id = ?`,
		pollID, w.ID, w.ProjectID).Scan(&closesAt, &choicesJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return errEngagementNotFound
	}
	if err != nil {
		return err
	}
	if n := countPollChoices(choicesJSON); choiceIndex < 0 || (n > 0 && choiceIndex >= n) {
		return errInvalidChoice
	}
	if closesAt.Valid && closesAt.String != "" {
		if t, perr := parseDBTime(closesAt.String); perr == nil && !nowUTC().Before(t) {
			return errPollClosed
		}
	}
	_, err = ctx.AppDB().Exec(
		`INSERT INTO webinar_poll_responses
			(poll_id, registrant_id, choice_index, answered_at, project_id, webinar_id)
		 VALUES (?, ?, ?, ?, ?, ?)
		 ON CONFLICT(poll_id, registrant_id) DO UPDATE SET
			choice_index = excluded.choice_index,
			answered_at  = excluded.answered_at`,
		pollID, reg.ID, choiceIndex, nowRFC3339(), w.ProjectID, w.ID)
	return err
}

// RecordOfferClick attributes one click after verifying the offer
// belongs to this webinar and project and has actually been shown.
// Repeat clicks by the same registrant are stored — engagement reporting
// counts distinct clickers, so raw click volume stays available without
// inflating CTR.
func (a *App) RecordOfferClick(ctx *sdk.AppCtx, w *Webinar, reg *Registrant, offerID int64) error {
	if ctx == nil || w == nil || reg == nil {
		return errEngagementNotFound
	}
	if offerID <= 0 {
		return errEngagementNotFound
	}
	var shownAt sql.NullString
	err := ctx.AppDB().QueryRow(
		`SELECT shown_at FROM webinar_offers
		 WHERE id = ? AND webinar_id = ? AND project_id = ?`,
		offerID, w.ID, w.ProjectID).Scan(&shownAt)
	if errors.Is(err, sql.ErrNoRows) {
		return errEngagementNotFound
	}
	if err != nil {
		return err
	}
	if !shownAt.Valid || shownAt.String == "" {
		// Scripted-but-not-yet-broadcast offer: nobody can have seen it,
		// so a click on it is fabricated.
		return errEngagementNotFound
	}
	_, err = ctx.AppDB().Exec(
		`INSERT INTO webinar_offer_clicks
			(project_id, webinar_id, offer_id, registrant_id, clicked_at)
		 VALUES (?, ?, ?, ?, ?)`,
		w.ProjectID, w.ID, offerID, reg.ID, nowRFC3339())
	return err
}

// countPollChoices returns the number of choices in a poll's stored JSON
// array, or 0 when the column can't be decoded (in which case the range
// check is skipped rather than rejecting a legitimate vote).
func countPollChoices(choicesJSON string) int {
	var choices []string
	if err := json.Unmarshal([]byte(choicesJSON), &choices); err != nil {
		return 0
	}
	return len(choices)
}

// InsertChatMessage writes one chat line with an atomically allocated
// sequence and an RFC3339 created_at, and returns (id, sequence).
// Exposed so the live-room handler doesn't have to reproduce the
// sequence + timestamp contract.
func (a *App) InsertChatMessage(ctx *sdk.AppCtx, w *Webinar, reg *Registrant, displayName, body, kind string) (int64, int, error) {
	if ctx == nil || w == nil {
		return 0, 0, errEngagementNotFound
	}
	if kind != "question" && kind != "host" {
		kind = "message"
	}
	seq := a.nextWebinarSequence(ctx, w.ID)
	var registrantID any
	if reg != nil && reg.ID != 0 {
		registrantID = reg.ID
	}
	res, err := ctx.AppDB().Exec(
		`INSERT INTO webinar_chat
			(project_id, webinar_id, registrant_id, display_name, body, kind, sequence, created_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		w.ProjectID, w.ID, registrantID, displayName, body, kind, seq, nowRFC3339())
	if err != nil {
		return 0, seq, err
	}
	id, _ := res.LastInsertId()
	return id, seq, nil
}
