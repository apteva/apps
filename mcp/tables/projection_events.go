package main

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type projectionEventOutboxItem struct {
	ID, ProjectID, Topic, Payload string
	ProjectionID                  int64
	Attempts                      int
}

type projectionReadyBatch struct {
	IDs         []string
	ProjectID   string
	Payload     map[string]any
	Attempts    map[string]int
	Scopes      map[string]struct{}
	Generations map[string]struct{}
}

// deliverProjectionEvents drains a bounded amount of the durable outbox. It
// deliberately acknowledges outside the write transaction: a slow platform
// cannot hold the Tables writer, while a failed delivery remains retryable.
func (a *App) deliverProjectionEvents(parent context.Context, app *sdk.AppCtx) error {
	deadline := time.Now().Add(projectionEventBudget)
	rows, err := app.AppReadDB().QueryContext(parent, `SELECT event_id,project_id,projection_id,topic,payload,attempts FROM projection_event_outbox WHERE next_attempt_ms<=? ORDER BY next_attempt_ms,created_at_ms LIMIT ?`, time.Now().UnixMilli(), projectionEventBatch)
	if err != nil {
		return err
	}
	defer rows.Close()
	groups := map[string]*projectionReadyBatch{}
	for rows.Next() {
		var item projectionEventOutboxItem
		if err := rows.Scan(&item.ID, &item.ProjectID, &item.ProjectionID, &item.Topic, &item.Payload, &item.Attempts); err != nil {
			return err
		}
		var payload map[string]any
		if err := json.Unmarshal([]byte(item.Payload), &payload); err != nil {
			_ = a.failProjectionEvent(context.Background(), app, item, err)
			continue
		}
		key := fmt.Sprintf("%s:%d:%v", item.ProjectID, item.ProjectionID, payload["version"])
		batch := groups[key]
		if batch == nil {
			batch = &projectionReadyBatch{ProjectID: item.ProjectID, Payload: payload, Attempts: map[string]int{}, Scopes: map[string]struct{}{}, Generations: map[string]struct{}{}}
			groups[key] = batch
		}
		batch.IDs = append(batch.IDs, item.ID)
		batch.Attempts[item.ID] = item.Attempts
		mergeProjectionReadyMetadata(batch.Payload, payload)
		if scope, ok := payload["scope_key"].(string); ok && scope != "" {
			batch.Scopes[scope] = struct{}{}
		}
		if values, ok := payload["scope_keys"].([]any); ok {
			for _, value := range values {
				if scope, ok := value.(string); ok {
					batch.Scopes[scope] = struct{}{}
				}
			}
		}
		if gen, ok := payload["generation"].(string); ok {
			batch.Generations[gen] = struct{}{}
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	for _, batch := range groups {
		if time.Now().After(deadline) {
			break
		}
		scopes := make([]string, 0, len(batch.Scopes))
		for scope := range batch.Scopes {
			scopes = append(scopes, scope)
		}
		sort.Strings(scopes)
		batch.Payload["scope_keys"] = scopes
		batch.Payload["scope_count"] = len(scopes)
		if len(scopes) == 1 {
			batch.Payload["scope_key"] = scopes[0]
		} else {
			delete(batch.Payload, "scope_key")
		}
		if len(batch.Generations) > 1 {
			generations := make([]string, 0, len(batch.Generations))
			for generation := range batch.Generations {
				generations = append(generations, generation)
			}
			sort.Strings(generations)
			batch.Payload["generations"] = generations
		}
		if err := a.sendProjectionEvent(parent, app, batch); err != nil {
			for _, id := range batch.IDs {
				_ = a.failProjectionEvent(context.Background(), app, projectionEventOutboxItem{ID: id, ProjectID: batch.ProjectID, Topic: topicProjectionReady, Attempts: batch.Attempts[id]}, err)
			}
		} else {
			for _, id := range batch.IDs {
				_, _ = app.AppDB().ExecContext(parent, `DELETE FROM projection_event_outbox WHERE event_id=?`, id)
			}
		}
	}
	return nil
}

// mergeProjectionReadyMetadata keeps the event useful when a worker coalesces
// several publications for one projection. Scope keys are merged separately;
// this helper makes the scalar snapshot metadata describe the newest result.
func mergeProjectionReadyMetadata(dst, src map[string]any) {
	srcWatermark, ok := projectionEventNumber(src["watermark"])
	if !ok {
		return
	}
	dstWatermark, exists := projectionEventNumber(dst["watermark"])
	srcPublished, _ := src["published_at"].(string)
	dstPublished, _ := dst["published_at"].(string)
	if !exists || srcWatermark > dstWatermark || (srcWatermark == dstWatermark && srcPublished > dstPublished) {
		dst["watermark"] = src["watermark"]
		dst["published_at"] = src["published_at"]
		dst["generation"] = src["generation"]
	}
}

func projectionEventNumber(value any) (float64, bool) {
	switch n := value.(type) {
	case float64:
		return n, true
	case float32:
		return float64(n), true
	case int:
		return float64(n), true
	case int64:
		return float64(n), true
	case json.Number:
		f, err := n.Float64()
		return f, err == nil
	default:
		return 0, false
	}
}

func (a *App) sendProjectionEvent(parent context.Context, app *sdk.AppCtx, batch *projectionReadyBatch) error {
	ctx, cancel := context.WithTimeout(parent, projectionEventBudget)
	defer cancel()
	if eventID, ok := batch.Payload["event_id"].(string); ok && eventID != "" {
		if bus := app.EventBusAPI(); bus != nil {
			// PublishAppEvent carries the stable event ID at the platform level,
			// so an ambiguous timeout can be retried without duplicate delivery.
			result := make(chan error, 1)
			go func() { result <- bus.PublishAppEvent(batch.ProjectID, eventID, topicProjectionReady, batch.Payload) }()
			select {
			case err := <-result:
				return err
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	return app.EmitWithProjectAck(ctx, topicProjectionReady, batch.ProjectID, batch.Payload)
}

func (a *App) failProjectionEvent(ctx context.Context, app *sdk.AppCtx, item projectionEventOutboxItem, deliveryErr error) error {
	delay := int64(1000)
	for i := 1; i < item.Attempts && delay < 60000; i++ {
		delay *= 2
	}
	if delay > 60000 {
		delay = 60000
	}
	message := deliveryErr.Error()
	if len(message) > 2048 {
		message = message[:2048]
	}
	_, err := app.AppDB().ExecContext(ctx, `UPDATE projection_event_outbox SET attempts=attempts+1,last_error=?,next_attempt_ms=? WHERE event_id=?`, message, time.Now().UnixMilli()+delay, item.ID)
	return err
}
