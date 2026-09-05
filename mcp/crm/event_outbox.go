package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"
)

var outboxDeliveryMu sync.Mutex
var outboxClient = &http.Client{Timeout: 5 * time.Second}

// At-least-once delivery to the gateway. Consumers must deduplicate event_id:
// a crash after gateway acknowledgment but before the local update can replay it.
func flushCRMEvents(cancel context.Context, ctx *sdk.AppCtx) error {
	outboxDeliveryMu.Lock()
	defer outboxDeliveryMu.Unlock()
	rows, err := ctx.AppDB().Query(`SELECT id,project_id,topic,payload FROM crm_event_outbox WHERE delivered_at IS NULL ORDER BY id LIMIT 100`)
	if err != nil {
		return err
	}
	type pending struct {
		id                   int64
		project, topic, body string
	}
	batch := []pending{}
	for rows.Next() {
		var e pending
		if err = rows.Scan(&e.id, &e.project, &e.topic, &e.body); err != nil {
			rows.Close()
			return err
		}
		batch = append(batch, e)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return err
	}
	gateway := strings.TrimRight(os.Getenv("APTEVA_GATEWAY_URL"), "/")
	token := os.Getenv("APTEVA_OUTBOUND_TOKEN")
	if token == "" {
		token = os.Getenv("APTEVA_APP_TOKEN")
	}
	for _, e := range batch {
		if err := cancel.Err(); err != nil {
			return err
		}
		var payload map[string]any
		if err = json.Unmarshal([]byte(e.body), &payload); err != nil {
			return err
		}
		payload["event_id"] = fmt.Sprintf("crm:%s:%d", os.Getenv("APTEVA_INSTALL_ID"), e.id)
		if gateway == "" || token == "" {
			// Embedded/test contexts have a synchronous supplied emitter.
			ctx.EmitWithProject(e.topic, e.project, payload)
		} else {
			body, err := json.Marshal(map[string]any{"topic": e.topic, "project_id": e.project, "data": payload})
			if err != nil {
				return err
			}
			req, err := http.NewRequestWithContext(cancel, http.MethodPost, gateway+"/api/app-events/internal/emit", bytes.NewReader(body))
			if err != nil {
				return err
			}
			req.Header.Set("Authorization", "Bearer "+token)
			req.Header.Set("Content-Type", "application/json")
			res, err := outboxClient.Do(req)
			if err == nil {
				io.Copy(io.Discard, io.LimitReader(res.Body, 4096))
				res.Body.Close()
				if res.StatusCode < 200 || res.StatusCode >= 300 {
					err = fmt.Errorf("event gateway returned %d", res.StatusCode)
				}
			}
			if err != nil {
				_, _ = ctx.AppDB().Exec(`UPDATE crm_event_outbox SET attempts=attempts+1,last_error=? WHERE id=?`, err.Error(), e.id)
				return err
			}
		}
		if _, err = ctx.AppDB().Exec(`UPDATE crm_event_outbox SET delivered_at=strftime('%Y-%m-%dT%H:%M:%fZ','now'),attempts=attempts+1,last_error=NULL WHERE id=?`, e.id); err != nil {
			return err
		}
	}

	if len(batch) > 0 {
		var pending int
		if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM crm_event_outbox WHERE delivered_at IS NULL`).Scan(&pending); err != nil {
			return err
		}
		ctx.Logger().Info("crm event delivery", "delivered", len(batch), "pending", pending)
	}
	return nil
}

func pruneCRMEvents(_ context.Context, ctx *sdk.AppCtx) error {
	// Retain acknowledgments for seven days; pending events are never discarded.
	_, err := ctx.AppDB().Exec(`DELETE FROM crm_event_outbox WHERE id IN (SELECT id FROM crm_event_outbox WHERE delivered_at < strftime('%Y-%m-%dT%H:%M:%fZ','now','-7 days') LIMIT 1000)`)
	return err
}

func crmOutboxWorker() sdk.Worker {
	return sdk.Worker{Name: "crm-event-outbox", Schedule: "@every 1s", Run: flushCRMEvents}
}

// Production delivery is handled by the bounded worker; request handlers never
// wait on a slow event gateway. Embedded contexts use their synchronous emitter.
func deliverQueuedCRMEvents(ctx *sdk.AppCtx) error {
	if os.Getenv("APTEVA_GATEWAY_URL") != "" {
		return nil
	}
	return flushCRMEvents(context.Background(), ctx)
}
