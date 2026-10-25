package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

var accountIssueMessages = map[string]string{
	"insufficient_credit":   "The carrier reports insufficient credit.",
	"account_suspended":     "The carrier reports that the account is suspended.",
	"billing_blocked":       "The carrier reports a billing restriction.",
	"authentication_failed": "The carrier rejected authentication.",
	"rate_limited":          "The carrier is temporarily limiting requests.",
	"provider_unavailable":  "The carrier service is temporarily unavailable.",
}

// Match explicit evidence only, never arbitrary text anywhere in a response.
// In particular, a bare 403 or a normal SIP digest challenge says nothing about balance.
func accountReasonText(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.NewReplacer("_", " ", "-", " ").Replace(value)
	value = strings.TrimSuffix(value, ".")
	switch value {
	case "insufficient account balance", "account balance not sufficient", "insufficient funds", "insufficient balance", "insufficient credit", "insufficient credits", "account balance is insufficient", "not enough credit", "not enough credits":
		return "insufficient_credit"
	case "account suspended", "carrier account suspended", "account is suspended":
		return "account_suspended"
	case "billing blocked", "payment required", "billing restriction":
		return "billing_blocked"
	}
	return ""
}

func accountAPIReason(status int, body json.RawMessage) string {
	if len(body) <= 64*1024 {
		var doc map[string]json.RawMessage
		if json.Unmarshal(body, &doc) == nil {
			candidates := []map[string]json.RawMessage{doc}
			var entries []map[string]json.RawMessage
			if json.Unmarshal(doc["errors"], &entries) == nil && len(entries) <= 32 {
				candidates = append(candidates, entries...)
			}
			var entry map[string]json.RawMessage
			if json.Unmarshal(doc["error"], &entry) == nil && entry != nil {
				candidates = append(candidates, entry)
			}
			for _, entry := range candidates {
				for _, field := range []string{"code", "reason", "title", "detail", "message", "error"} {
					var value string
					if json.Unmarshal(entry[field], &value) == nil {
						if reason := accountReasonText(value); reason != "" {
							return reason
						}
					}
				}
			}
		}
	}
	switch status {
	case 401:
		return "authentication_failed"
	case 402:
		return "billing_blocked"
	case 429:
		return "rate_limited"
	case 500, 502, 503, 504:
		return "provider_unavailable"
	}
	return ""
}

func accountBillingReason(reason string) bool {
	return reason == "insufficient_credit" || reason == "billing_blocked" || reason == "account_suspended"
}
func accountToolOperation(tool string) string {
	switch tool {
	case "create_call", "dial_call":
		return "outbound_calls"
	case "list_phone_numbers", "get_phone_number", "list_connections", "get_connection":
		return "inventory"
	case "hangup_call", "update_call", "send_dtmf", "transfer_call":
		return "call_control"
	default:
		return "carrier_api"
	}
}

type accountObservation struct {
	Project, Provider, Scope, Operation, Reason, Source, CallID string
	Connection                                                  int64
	At, Since                                                   time.Time
	Success, OutboundConfirmed                                  bool
}
type accountIssue struct {
	ID, Provider, Scope, Operation, Reason string
	Active                                 bool
	Revision, Observed                     int64
}

// The clock is stored even on success with no open issue: a slower, older
// request must not reopen an issue after a newer successful observation.
func recordAccountObservation(db *sql.DB, o accountObservation) error {
	if o.Project == "" || (!o.Success && o.Provider == "") || o.Connection <= 0 || o.At.IsZero() || (!o.Success && accountIssueMessages[o.Reason] == "") {
		return nil
	}
	request, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	tx, err := db.BeginTx(request, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	ns := o.At.UnixNano()
	if !o.Since.IsZero() {
		ns = o.Since.UnixNano()
	}
	clockScope := o.Scope
	if accountBillingReason(o.Reason) {
		clockScope += ":billing"
	}
	// First statement takes the SQLite write lock; avoid read-to-write upgrades.
	result, err := tx.ExecContext(request, `INSERT INTO telephony_account_observations(project_id,connection_id,provider,scope,observed_ns) VALUES(?,?,?,?,?)
 ON CONFLICT(project_id,connection_id,provider,scope) DO UPDATE SET observed_ns=excluded.observed_ns WHERE observed_ns<excluded.observed_ns`, o.Project, o.Connection, "", clockScope, ns)
	if err != nil {
		return err
	}
	changed, _ := result.RowsAffected()
	if changed == 0 {
		return nil
	}
	if !o.Success && accountBillingReason(o.Reason) {
		var newer int
		if err = tx.QueryRowContext(request, `SELECT COUNT(*) FROM telephony_account_observations WHERE project_id=? AND connection_id=? AND provider=? AND scope='outbound_confirmed' AND observed_ns>=?`, o.Project, o.Connection, "", ns).Scan(&newer); err != nil {
			return err
		}
		if newer > 0 {
			return tx.Commit()
		}
	}
	rows, err := tx.QueryContext(request, `SELECT issue_id,provider,scope,operation,reason,active,revision,observed_ns FROM telephony_account_issues WHERE project_id=? AND connection_id=? AND (provider=? OR ?='') AND (scope=? OR ?=1)`, o.Project, o.Connection, o.Provider, o.Provider, o.Scope, o.OutboundConfirmed)
	if err != nil {
		return err
	}
	var issues []accountIssue
	for rows.Next() {
		var v accountIssue
		if err = rows.Scan(&v.ID, &v.Provider, &v.Scope, &v.Operation, &v.Reason, &v.Active, &v.Revision, &v.Observed); err != nil {
			rows.Close()
			return err
		}
		issues = append(issues, v)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	if !o.Success {
		found := false
		for _, v := range issues {
			if v.Reason == o.Reason {
				found = true
			}
		}
		if !found {
			issues = append(issues, accountIssue{ID: newCallID(), Provider: o.Provider, Scope: o.Scope, Operation: o.Operation, Reason: o.Reason})
		}
	}
	for _, v := range issues {
		if ns <= v.Observed {
			continue
		}
		if o.Success {
			if !v.Active {
				continue
			}
			if o.OutboundConfirmed {
				if v.Operation != "outbound_calls" || !accountBillingReason(v.Reason) {
					continue
				}
			} else if accountBillingReason(v.Reason) {
				continue
			}
		} else if v.Reason != o.Reason {
			continue
		}
		active := !o.Success
		transition := active != v.Active
		if transition {
			v.Revision++
		}
		_, err = tx.ExecContext(request, `INSERT INTO telephony_account_issues(issue_id,project_id,connection_id,provider,scope,operation,reason,active,revision,observed_ns) VALUES(?,?,?,?,?,?,?,?,?,?)
 ON CONFLICT(issue_id) DO UPDATE SET active=excluded.active,revision=excluded.revision,observed_ns=excluded.observed_ns`, v.ID, o.Project, o.Connection, v.Provider, v.Scope, v.Operation, v.Reason, active, v.Revision, ns)
		if err != nil {
			return err
		}
		if !transition {
			continue
		}
		topic, message, severity := "telephony.account.issue", accountIssueMessages[v.Reason], "error"
		if v.Reason == "rate_limited" || v.Reason == "provider_unavailable" {
			severity = "warning"
		}
		if !active {
			topic = "telephony.account.recovered"
			message = "The affected carrier operation succeeded after the reported issue."
			severity = "info"
		}
		id := newCallID()
		payload := map[string]any{"event_id": id, "issue_id": v.ID, "revision": v.Revision, "provider": v.Provider, "carrier_connection_id": o.Connection, "reason": v.Reason, "severity": severity, "affected_operations": []string{v.Operation}, "message": message, "source": o.Source, "occurred_at": o.At.UTC().Format(time.RFC3339Nano)}
		if o.CallID != "" {
			payload["call_id"] = o.CallID
		}
		raw, _ := json.Marshal(payload)
		if _, err = tx.ExecContext(request, `INSERT INTO telephony_account_event_outbox(event_id,issue_id,revision,project_id,topic,payload_json) VALUES(?,?,?,?,?,?)`, id, v.ID, v.Revision, o.Project, topic, string(raw)); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func observeAccountAPI(ctx *sdk.AppCtx, connection int64, tool string, started time.Time, result *sdk.ExecuteResult, callErr error) {
	if ctx == nil || result == nil {
		return
	}
	success := callErr == nil && result.Success && result.Status >= 200 && result.Status < 300
	reason := ""
	if !success {
		reason = accountAPIReason(result.Status, result.Data)
	}
	if ctx.CurrentProject() == "" || (!success && reason == "") {
		return
	}
	// Successful requests need no extra gateway/carrier lookups. Connection IDs
	// are installation bindings; the platform already authorized the execution.
	provider := ""
	if !success {
		provider = accountProvider(ctx, connection)
		if provider == "" {
			ctx.Logger().Warn("carrier account provider metadata unavailable", "connection", connection)
			return
		}
	}
	o := accountObservation{Project: ctx.CurrentProject(), Provider: provider, Connection: connection, Scope: "api:" + tool, Operation: accountToolOperation(tool), Source: "carrier_api", At: started, Success: success, Reason: reason}
	if err := recordAccountObservation(ctx.AppDB(), o); err != nil {
		ctx.Logger().Warn("carrier account observation could not be persisted", "connection", connection)
	}
}

// GetConnection in the current SDK has a 30-second timeout and no context
// variant. Use its authenticated metadata endpoint with a short budget here;
// a notification must not hold up returning a failed carrier operation.
func accountProvider(ctx *sdk.AppCtx, connection int64) string {
	request, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	var provider string
	// Connection IDs are stable. Reuse already verified metadata on repeated
	// failures without a second network request during a carrier outage.
	err := ctx.AppDB().QueryRowContext(request, `SELECT provider FROM telephony_account_issues WHERE project_id=? AND connection_id=? LIMIT 1`, ctx.CurrentProject(), connection).Scan(&provider)
	if err == nil && provider != "" {
		return provider
	}
	gateway, token := strings.TrimRight(os.Getenv("APTEVA_GATEWAY_URL"), "/"), os.Getenv("APTEVA_APP_TOKEN")
	if gateway == "" || token == "" {
		return ""
	}
	req, err := http.NewRequestWithContext(request, http.MethodGet, gateway+"/api/apps/callback/connections/"+strconv.FormatInt(connection, 10), nil)
	if err != nil {
		return ""
	}
	req.Header.Set("Authorization", "Bearer "+token)
	client := &http.Client{Timeout: 500 * time.Millisecond, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	res, err := client.Do(req)
	if err != nil {
		return ""
	}
	defer res.Body.Close()
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return ""
	}
	var result sdk.PlatformConnection
	if json.NewDecoder(io.LimitReader(res.Body, 64*1024)).Decode(&result) != nil {
		return ""
	}
	return result.AppSlug
}

// Called only for authenticated provider lifecycle evidence, never local errors.
func (c *callsDB) observeAccountCall(row *callRow, status, message string, facts lifecycleFacts) {
	if row == nil || facts.Source != "provider" || facts.Synthesized || row.Direction != "outbound" {
		return
	}
	at, err := time.Parse(time.RFC3339Nano, facts.OccurredAt)
	if err != nil {
		return
	}
	reason := accountReasonText(facts.TerminationCause)
	if reason == "" {
		reason = accountReasonText(message)
	}
	success := status == "in-progress" || status == "answered"
	o := accountObservation{Project: row.ProjectID, Provider: row.CarrierSlug, Connection: row.CarrierConnectionID, Scope: "callback:outbound", Operation: "outbound_calls", Source: "provider_callback", At: at, Reason: reason, Success: success, CallID: row.ID}
	if success {
		// A long-running or delayed callback for an old call cannot prove that a
		// newer billing restriction has cleared. Use the start of this attempt.
		started, e := time.Parse(time.RFC3339Nano, row.PlacedAt)
		if e != nil {
			return
		}
		o.Since = started
		o.Source = "call_answer"
		o.Scope = "outbound_confirmed"
		o.OutboundConfirmed = true
	}
	if err = recordAccountObservation(c.db, o); err != nil && globalCtx != nil {
		globalCtx.Logger().Warn("carrier account call observation could not be persisted", "call", row.ID)
	}
}

// Durable at-least-once publication. A short lease prevents simultaneous tick
// and dispatcher sends; consumers still deduplicate event_id after a lost ACK.
func (a *App) publishAccountEvents(parent context.Context, ctx *sdk.AppCtx) error {
	gateway, token := strings.TrimRight(os.Getenv("APTEVA_GATEWAY_URL"), "/"), os.Getenv("APTEVA_APP_TOKEN")
	if gateway == "" || token == "" || ctx.CurrentProject() == "" {
		return nil
	}
	batch, cancel := context.WithTimeout(parent, 2*time.Second)
	defer cancel()
	now := time.Now().UnixMilli()
	rows, err := ctx.AppDB().QueryContext(batch, `SELECT e.event_id,e.topic,e.payload_json,e.attempts FROM telephony_account_event_outbox e WHERE e.project_id=? AND e.published_ms=0 AND e.due_ms<=?
 AND NOT EXISTS(SELECT 1 FROM telephony_account_event_outbox p WHERE p.issue_id=e.issue_id AND p.revision<e.revision AND p.published_ms=0) ORDER BY e.due_ms,e.event_id LIMIT 20`, ctx.CurrentProject(), now)
	if err != nil {
		return err
	}
	type event struct {
		id, topic, payload string
		attempts           int
	}
	var events []event
	for rows.Next() {
		var e event
		if err = rows.Scan(&e.id, &e.topic, &e.payload, &e.attempts); err != nil {
			rows.Close()
			return err
		}
		events = append(events, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	client := &http.Client{Timeout: 500 * time.Millisecond, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
	for _, e := range events {
		if batch.Err() != nil {
			return batch.Err()
		}
		lease := time.Now().Add(30 * time.Second).UnixMilli()
		result, err := ctx.AppDB().ExecContext(batch, `UPDATE telephony_account_event_outbox SET due_ms=?,attempts=attempts+1 WHERE event_id=? AND published_ms=0 AND due_ms<=?`, lease, e.id, now)
		if err != nil {
			return err
		}
		n, _ := result.RowsAffected()
		if n == 0 {
			continue
		}
		body, _ := json.Marshal(map[string]any{"event_id": e.id, "topic": e.topic, "project_id": ctx.CurrentProject(), "data": json.RawMessage(e.payload)})
		req, err := http.NewRequestWithContext(batch, http.MethodPost, gateway+"/api/app-events/internal/emit", bytes.NewReader(body))
		if err != nil {
			return err
		}
		req.Header.Set("Authorization", "Bearer "+token)
		req.Header.Set("Content-Type", "application/json")
		response, sendErr := client.Do(req)
		ok := false
		if response != nil {
			ok = response.StatusCode >= 200 && response.StatusCode < 300
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
			response.Body.Close()
		}
		if sendErr == nil && ok {
			_, err = ctx.AppDB().ExecContext(batch, `UPDATE telephony_account_event_outbox SET published_ms=? WHERE event_id=? AND due_ms=?`, time.Now().UnixMilli(), e.id, lease)
		} else {
			delay := time.Second * time.Duration(1<<min(e.attempts, 8))
			_, err = ctx.AppDB().ExecContext(batch, `UPDATE telephony_account_event_outbox SET due_ms=? WHERE event_id=? AND due_ms=? AND published_ms=0`, time.Now().Add(delay).UnixMilli(), e.id, lease)
		}
		if err != nil {
			return err
		}
	}
	_, err = ctx.AppDB().ExecContext(batch, `DELETE FROM telephony_account_event_outbox WHERE event_id IN (SELECT event_id FROM telephony_account_event_outbox WHERE published_ms>0 AND published_ms<? LIMIT 100)`, time.Now().Add(-30*24*time.Hour).UnixMilli())
	if err != nil {
		return fmt.Errorf("account event retention: %w", err)
	}
	return nil
}

func accountSIPReason(status int, phrase string) string {
	if status < 400 || status == 401 || status == 407 {
		return ""
	}
	if reason := accountReasonText(phrase); reason != "" {
		return reason
	}
	if status == 402 {
		return "billing_blocked"
	}
	return ""
}
func (c *callsDB) observeAccountSIP(callID string, status int, phrase string) {
	reason := accountSIPReason(status, phrase)
	if reason == "" {
		return
	}
	row, err := c.findCall(callID)
	if err != nil || row == nil || row.Direction != "outbound" {
		return
	}
	o := accountObservation{Project: row.ProjectID, Provider: row.CarrierSlug, Connection: row.CarrierConnectionID, Scope: "sip:outbound", Operation: "outbound_calls", Source: "sip_response", CallID: callID, Reason: reason, At: time.Now()}
	if err = recordAccountObservation(c.db, o); err != nil && globalCtx != nil {
		globalCtx.Logger().Warn("carrier SIP account observation could not be persisted", "call", callID)
	}
}
