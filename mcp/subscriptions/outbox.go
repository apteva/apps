package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

func queueAuditEvent(tx *sql.Tx, pid string, id int64, action string, details map[string]any) error {
	topics := []string{}
	sub, err := requiredSub(tx, pid, id, false)
	if err != nil {
		return err
	}
	switch action {
	case "subscription.created", "subscription.cancelled", "subscription.cancellation_scheduled", "subscription.resumed":
		topics = append(topics, action)
	case "subscription.status_updated":
		topics = append(topics, "subscription.updated", "subscription."+sub.Status)
	case "subscription.trial_ended", "subscription.period_ended", "subscription.unpaid_grace_expired":
		topics = append(topics, "subscription."+sub.Status)
	case "subscription.metadata_updated", "subscription.item_created", "subscription.item_updated", "subscription.change_applied":
		topics = append(topics, "subscription.updated")
	case "subscription.cycle_due":
		topics = append(topics, action)
	}
	for _, topic := range topics {
		payload := subscriptionEventPayload(tx, sub)
		payload["cancel_at"] = sub.CancelAt
		if topic == "subscription.cycle_due" {
			cycle, err := dbCycleGet(tx, pid, int64Arg(details, "cycle_id"))
			if err != nil {
				return err
			}
			if cycle == nil {
				return errors.New("due event cycle missing")
			}
			for k, v := range cycleDueDetails(sub, cycle) {
				payload[k] = v
			}
		}
		data, err := json.Marshal(payload)
		if err != nil {
			return err
		}
		if _, err = tx.Exec(`INSERT INTO subscription_outbox(project_id,subscription_id,topic,payload) VALUES(?,?,?,?)`, pid, id, topic, string(data)); err != nil {
			return err
		}
	}
	return nil
}

func emitDomainEvent(ctx *sdk.AppCtx, topic string, data map[string]any) {
	// Keep SDK test/dev emitters observable; queued rows are never acknowledged
	// by this fallback. Configured sidecars exclusively use durable delivery.
	if os.Getenv("APTEVA_GATEWAY_URL") == "" || os.Getenv("APTEVA_APP_TOKEN") == "" {
		ctx.EmitWithProject(topic, ctx.CurrentProject(), data)
		return
	}
	publishEvents(ctx)
}

var eventDispatchMu sync.Mutex

// SDK v0.73 emits asynchronously without an acknowledgment. Use its documented
// gateway transport directly so an outbox row is retained on delivery failure.
func dispatchEvents(runCtx context.Context, ctx *sdk.AppCtx) error {
	if !eventDispatchMu.TryLock() {
		return nil
	}
	defer eventDispatchMu.Unlock()
	rows, e := ctx.AppDB().Query(`SELECT id,project_id,topic,payload FROM subscription_outbox WHERE delivered_at IS NULL ORDER BY id LIMIT 100`)
	if e != nil {
		return e
	}
	type event struct {
		id                  int64
		pid, topic, payload string
	}
	pending := []event{}
	for rows.Next() {
		var v event
		if e = rows.Scan(&v.id, &v.pid, &v.topic, &v.payload); e != nil {
			rows.Close()
			return e
		}
		pending = append(pending, v)
	}
	e = rows.Err()
	rows.Close()
	if e != nil {
		return e
	}
	if len(pending) == 0 {
		return nil
	}
	gateway, token := strings.TrimRight(os.Getenv("APTEVA_GATEWAY_URL"), "/"), os.Getenv("APTEVA_APP_TOKEN")
	if gateway == "" || token == "" {
		return errors.New("event gateway is not configured; events remain queued")
	}
	client := &http.Client{Timeout: 5 * time.Second}
	for _, v := range pending {
		data := map[string]any{}
		if e = json.Unmarshal([]byte(v.payload), &data); e != nil {
			return e
		}
		data["event_id"] = fmt.Sprintf("subscriptions:%s:%d", v.pid, v.id)
		body, e := json.Marshal(map[string]any{"topic": v.topic, "project_id": v.pid, "data": data})
		if e != nil {
			return e
		}
		req, e := http.NewRequestWithContext(runCtx, http.MethodPost, gateway+"/api/app-events/internal/emit", bytes.NewReader(body))
		if e != nil {
			return e
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		resp, e := client.Do(req)
		if e != nil {
			return e
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 65536))
		resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return fmt.Errorf("event gateway returned HTTP %d", resp.StatusCode)
		}
		if _, e = ctx.AppDB().Exec(`UPDATE subscription_outbox SET delivered_at=CURRENT_TIMESTAMP WHERE id=? AND delivered_at IS NULL`, v.id); e != nil {
			return e
		}
	}
	// Keep a short delivery history, with AUTOINCREMENT preserving stable IDs.
	_, e = ctx.AppDB().Exec(`DELETE FROM subscription_outbox WHERE delivered_at < datetime('now','-7 days')`)
	return e
}

func publishEvents(ctx *sdk.AppCtx) {
	if os.Getenv("APTEVA_GATEWAY_URL") == "" || os.Getenv("APTEVA_APP_TOKEN") == "" {
		return
	}
	c, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	if e := dispatchEvents(c, ctx); e != nil {
		ctx.Logger().Warn("subscription events remain queued", "error", e)
	}
}
