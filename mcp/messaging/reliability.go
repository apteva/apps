package main

import (
	"crypto/rand"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"net/url"
	"time"
)

func mustJSON(v any) []byte { b, _ := json.Marshal(v); return b }

// Insertion and its resumable source commit together. Raw source is internal only
// and removed after successful processing; it is never returned or routed.
func persistInbound(ctx *sdk.AppCtx, pid, kind string, source any, query string, args ...any) (sql.Result, error) {
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(query, args...)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n > 0 {
		id, _ := res.LastInsertId()
		if _, err = tx.Exec(`INSERT INTO inbound_jobs(message_id,project_id,source_kind,source) VALUES(?,?,?,?)`, id, pid, kind, string(mustJSON(source))); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return res, nil
}

func processInboundJob(ctx *sdk.AppCtx, pid string, id int64, force bool) error {
	now := time.Now().Unix()
	if force {
		if _, err := ctx.AppDB().Exec(`UPDATE inbound_jobs SET status='pending',attempts=0,next_attempt=0 WHERE message_id=? AND project_id=? AND lease_until<=?`, id, pid, now); err != nil {
			return err
		}
	}
	leaseToken := rand.Text()
	res, err := ctx.AppDB().Exec(`UPDATE inbound_jobs SET lease_token=?,status='running', attempts=attempts+1,lease_until=? WHERE message_id=? AND project_id=? AND status IN ('pending','running') AND lease_until<=? AND next_attempt<=?`, leaseToken, now+600, id, pid, now, now)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil
	}
	stopHeartbeat := make(chan struct{})
	heartbeatDone := make(chan struct{})
	go func() {
		defer close(heartbeatDone)
		ticker := time.NewTicker(30 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-stopHeartbeat:
				return
			case <-ticker.C:
				_, _ = ctx.AppDB().Exec(`UPDATE inbound_jobs SET lease_until=? WHERE message_id=? AND project_id=? AND status='running' AND lease_token=?`, time.Now().Unix()+600, id, pid, leaseToken)
			}
		}
	}()
	defer func() { close(stopHeartbeat); <-heartbeatDone }()
	var kind, source string
	var attempts int
	err = ctx.AppDB().QueryRow(`SELECT source_kind,source,attempts FROM inbound_jobs WHERE message_id=?`, id).Scan(&kind, &source, &attempts)
	if err == nil {
		err = runInboundJob(ctx, pid, id, kind, source)
	}
	status, lastError, next := "done", "", int64(0)
	if err != nil {
		status = "pending"
		lastError = truncate(err.Error(), 500)
		next = now + int64(30*(1<<min(attempts, 10)))
		if attempts >= 8 {
			status = "failed"
		}
	}
	_, finishErr := ctx.AppDB().Exec(`UPDATE inbound_jobs SET status=?,last_error=?,next_attempt=?,lease_until=0,source=CASE WHEN ?='done' THEN '{}' ELSE source END WHERE message_id=? AND project_id=? AND lease_token=?`, status, lastError, next, status, id, pid, leaseToken)
	if err != nil {
		_, _ = ctx.AppDB().Exec(`UPDATE messages SET route_status=CASE WHEN ?='failed' THEN 'failed' ELSE 'target_failed' END,route_error=? WHERE id=? AND project_id=?`, status, lastError, id, pid)
		return err
	}
	if finishErr == nil {
		emitMessagingEvent(ctx, pid, "message.processed", map[string]any{"id": id, "channel": "inbound"})
	}
	return finishErr
}

func runInboundJob(ctx *sdk.AppCtx, pid string, id int64, kind, source string) error {
	m, err := dbMessageGet(ctx.AppDB(), pid, id)
	if err != nil {
		return err
	}
	if m == nil {
		return errors.New("message missing")
	}
	// STOP is applied on every attempt, including retries after a failed write.
	if m.Channel != channelEmail && isStopKeyword(m.BodyText) {
		address := canonicalAddrForChannel(m.Channel, m.From)
		existing, err := dbSuppressionGetExact(ctx.AppDB(), pid, m.Channel, "address", address)
		if err != nil {
			return err
		}
		if existing == nil {
			if _, err := upsertSuppressionAndEmit(ctx, pid, m.Channel, "address", address, "stop-keyword", "auto"); err != nil {
				return err
			}
		}
	}

	var verdicts map[string]string
	_ = json.Unmarshal(m.Verdicts, &verdicts)
	if verdicts["virus"] == "FAIL" {
		_, err := ctx.AppDB().Exec(`UPDATE messages SET route_status='quarantined',route_error='SES virus verdict failed' WHERE id=? AND project_id=?`, id, pid)
		return err
	}
	var inputs []providerAttachment
	switch kind {
	case "email":
		if source != "{}" {
			if err := json.Unmarshal([]byte(source), &inputs); err != nil {
				return err
			}
		}
	case "twilio":
		var form url.Values
		if err := json.Unmarshal([]byte(source), &form); err != nil {
			return err
		}
		if len(form) > 0 {
			bound := ctx.IntegrationFor("phone_provider")
			if bound == nil {
				return errors.New("phone provider missing")
			}
			token := lookupConnectionCredential(ctx, bound.ConnectionID, "auth_token")
			if token == "" {
				return errors.New("Twilio credentials unavailable")
			}
			inputs = twilioInboundAttachments(form, m.ProviderMessageID, form.Get("AccountSid"), token)
		}
	}
	ready := map[string]bool{}
	for _, att := range m.Attachments {
		if att.ProcessingStatus == "ready" {
			ready[att.ProviderRef] = true
		}
	}
	pending := []providerAttachment{}
	for _, att := range inputs {
		if !ready[att.ProviderRef] {
			pending = append(pending, att)
		}
	}
	processed := prepareInboundAttachments(ctx, pid, pending)
	if err := dbInsertMessageAttachments(ctx.AppDB(), pid, id, processed); err != nil {
		return err
	}
	for _, att := range processed {
		if att.ProcessingStatus != "ready" {
			return fmt.Errorf("attachment %s: %s", att.Filename, att.ProcessingError)
		}
	}
	m, err = dbMessageGet(ctx.AppDB(), pid, id)
	if err != nil {
		return err
	}
	return dispatchInbound(ctx, pid, m)
}

func (a *App) retryMessagingWork(ctx *sdk.AppCtx) error {
	rows, err := ctx.AppDB().Query(`SELECT message_id,project_id FROM inbound_jobs WHERE status IN ('pending','running') AND next_attempt<=? AND lease_until<=? ORDER BY message_id LIMIT 25`, time.Now().Unix(), time.Now().Unix())
	if err != nil {
		return err
	}
	type job struct {
		id  int64
		pid string
	}
	jobs := []job{}
	for rows.Next() {
		var j job
		if err := rows.Scan(&j.id, &j.pid); err != nil {
			rows.Close()
			return err
		}
		jobs = append(jobs, j)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, j := range jobs {
		if err := processInboundJob(ctx, j.pid, j.id, false); err != nil {
			ctx.Logger().Warn("inbound retry", "message_id", j.id, "err", err)
		}
	}
	return replayProviderEvents(ctx)
}

func queueProviderEvents(ctx *sdk.AppCtx, pid, topic string, events []providerEvent) error {
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, ev := range events {
		if ev.ProviderEventID == "" {
			ev.ProviderEventID = stableProviderEventID(&Message{}, ev)
		}
		if _, err := tx.Exec(`INSERT OR IGNORE INTO unmatched_provider_events(project_id,provider_id,event_id,topic_arn,payload) VALUES(?,?,?,?,?)`, pid, ev.ProviderMessageID, ev.ProviderEventID, topic, string(mustJSON(ev))); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func replayProviderEvents(ctx *sdk.AppCtx) error {
	rows, err := ctx.AppDB().Query(`SELECT id,project_id,topic_arn,payload FROM unmatched_provider_events e WHERE EXISTS(SELECT 1 FROM messages m WHERE m.provider_message_id=e.provider_id AND (e.project_id='' OR m.project_id=e.project_id)) ORDER BY id LIMIT 200`)
	if err != nil {
		return err
	}
	type pendingEvent struct {
		id              int64
		pid, topic, raw string
	}
	pending := []pendingEvent{}
	for rows.Next() {
		var p pendingEvent
		if err := rows.Scan(&p.id, &p.pid, &p.topic, &p.raw); err != nil {
			rows.Close()
			return err
		}
		pending = append(pending, p)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	for _, p := range pending {
		var ev providerEvent
		if err := json.Unmarshal([]byte(p.raw), &ev); err != nil {
			return err
		}
		m, err := dbMessageByProviderID(ctx.AppDB(), p.pid, ev.ProviderMessageID)
		if err != nil {
			return err
		}
		if m == nil {
			continue
		}
		if p.topic != "" && !snsTopicAuthorized(ctx, m.ProjectID, p.topic, "ses_bounce_topic_arn") {
			continue
		}
		if _, err := persistAndEmitProviderEvent(ctx, m, ev); err != nil {
			return err
		}
		if _, err := ctx.AppDB().Exec(`DELETE FROM unmatched_provider_events WHERE id=?`, p.id); err != nil {
			return err
		}
	}
	return nil
}

func scheduleInboundRetry(ctx *sdk.AppCtx, pid string, id int64) error {
	_, err := ctx.AppDB().Exec(`UPDATE inbound_jobs SET next_attempt=0 WHERE message_id=? AND project_id=? AND status='pending'`, id, pid)
	return err
}
