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
	"time"
)

var completionWake = make(chan struct{}, 1)

// Completion commits and event creation share a transaction. Delivery is at
// least once: consumers can deduplicate by payload.event_id after a lost ACK.
func startCompletionOutbox(app *sdk.AppCtx) {
	startMediaWorker(app, func() {
		tick := time.NewTicker(10 * time.Second)
		defer tick.Stop()
		for {
			flushCompletionOutbox(app)
			select {
			case <-mediaDone(app):
				return
			case <-completionWake:
			case <-tick.C:
			}
		}
	})
}
func flushCompletionOutbox(app *sdk.AppCtx) {
	gateway := strings.TrimRight(os.Getenv("APTEVA_GATEWAY_URL"), "/")
	token := os.Getenv("APTEVA_APP_TOKEN")
	if token == "" {
		token = os.Getenv("APTEVA_OUTBOUND_TOKEN")
	}
	if gateway == "" || token == "" {
		return
	}
	rows, err := app.AppDB().Query(`SELECT event_id,project_id,topic,payload FROM media_event_outbox ORDER BY created_at LIMIT 20`)
	if err != nil {
		return
	}
	type event struct{ id, project, topic, payload string }
	var events []event
	for rows.Next() {
		var e event
		if rows.Scan(&e.id, &e.project, &e.topic, &e.payload) == nil {
			events = append(events, e)
		}
	}
	rows.Close()
	ctx, cancel := mediaContext(context.Background(), app)
	defer cancel()
	for _, e := range events {
		body, _ := json.Marshal(map[string]any{"topic": e.topic, "project_id": e.project, "data": json.RawMessage(e.payload)})
		cctx, stop := context.WithTimeout(ctx, 2*time.Second)
		req, err := http.NewRequestWithContext(cctx, http.MethodPost, gateway+"/api/app-events/internal/emit", bytes.NewReader(body))
		if err != nil {
			stop()
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+token)
		response, err := http.DefaultClient.Do(req)
		if err == nil {
			_, _ = io.Copy(io.Discard, io.LimitReader(response.Body, 4096))
			response.Body.Close()
			if response.StatusCode/100 != 2 {
				err = fmt.Errorf("emit HTTP %d", response.StatusCode)
			}
		}
		stop()
		if err != nil {
			app.Logger().Warn("completion event retained for retry", "event_id", e.id, "err", err)
			return
		}
		if _, err = app.AppDB().Exec(`DELETE FROM media_event_outbox WHERE event_id=?`, e.id); err != nil {
			return
		}
	}
}
