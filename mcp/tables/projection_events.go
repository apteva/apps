package main

import (
	"bytes"
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
	Sealed                        bool
}

type projectionReadyBatch struct {
	IDs         []string
	ProjectID   string
	Payload     map[string]any
	Attempts    map[string]int
	Scopes      map[string]struct{}
	Generations map[string]struct{}
	AllScopes   bool
}

// deliverProjectionEvents drains a bounded amount of the durable outbox. It
// deliberately acknowledges outside the write transaction: a slow platform
// cannot hold the Tables writer, while a failed delivery remains retryable.
func (a *App) deliverProjectionEvents(parent context.Context, app *sdk.AppCtx) error {
	started := time.Now()
	defer func() {
		a.updateWorkerMetrics(app.CurrentProject(), func(m *projectionWorkerMetrics) { m.EventNs += time.Since(started).Nanoseconds() })
	}()
	deadline := time.Now().Add(projectionEventBudget)
	rows, err := app.AppReadDB().QueryContext(parent, `SELECT event_id,project_id,projection_id,topic,payload,attempts,delivery_sealed FROM projection_event_outbox WHERE next_attempt_ms<=? AND (?='' OR project_id=?) ORDER BY next_attempt_ms,created_at_ms LIMIT ?`, time.Now().UnixMilli(), app.CurrentProject(), app.CurrentProject(), projectionEventBatch)
	if err != nil {
		return err
	}
	defer rows.Close()
	groups := map[string]*projectionReadyBatch{}
	var invalid []projectionEventOutboxItem
	invalidErrors := map[string]error{}
	for rows.Next() {
		var item projectionEventOutboxItem
		if err := rows.Scan(&item.ID, &item.ProjectID, &item.ProjectionID, &item.Topic, &item.Payload, &item.Attempts, &item.Sealed); err != nil {
			return err
		}
		var payload map[string]any
		decoder := json.NewDecoder(bytes.NewBufferString(item.Payload))
		decoder.UseNumber()
		if err := decoder.Decode(&payload); err != nil {
			invalid = append(invalid, item)
			invalidErrors[item.ID] = err
			continue
		}
		if _, err := boundedProjectionReadyPayload(payload); err != nil {
			invalid = append(invalid, item)
			invalidErrors[item.ID] = err
			continue
		}
		key := fmt.Sprintf("%s:%d:%v", item.ProjectID, item.ProjectionID, payload["version"])
		// Never change the snapshot behind an identity that may already have
		// been accepted by the platform (including legacy ambiguous retries).
		if item.Sealed || item.Attempts > 0 {
			key += ":" + item.ID
		}
		batch := groups[key]
		if batch == nil {
			batch = &projectionReadyBatch{ProjectID: item.ProjectID, Payload: payload, Attempts: map[string]int{}, Scopes: map[string]struct{}{}, Generations: map[string]struct{}{}}
			groups[key] = batch
		}
		batch.IDs = append(batch.IDs, item.ID)
		batch.Attempts[item.ID] = item.Attempts
		mergeProjectionReadyMetadata(batch.Payload, payload)
		batch.AllScopes = batch.AllScopes || payload["all_scopes"] == true || payload["scopes_truncated"] == true
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
		// Stop reading more legacy payloads once the CPU budget is spent.
		if time.Now().After(deadline) {
			break
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	if err := rows.Close(); err != nil {
		return err
	}
	for _, item := range invalid {
		if err := a.failProjectionEvent(parent, app, item, invalidErrors[item.ID]); err != nil {
			return err
		}
	}
	delivered := 0
	for _, batch := range groups {
		batchStarted := time.Now()
		// Make progress even when repairing one old payload used the budget.
		if delivered > 0 && time.Now().After(deadline) {
			break
		}
		delivered++
		scopes := make([]string, 0, len(batch.Scopes))
		for scope := range batch.Scopes {
			scopes = append(scopes, scope)
		}
		sort.Strings(scopes)
		batch.Payload["scope_keys"] = scopes
		batch.Payload["scope_count"] = len(scopes)
		if batch.AllScopes {
			compactProjectionReadyScopes(batch.Payload)
		}
		if len(scopes) == 1 && !batch.AllScopes {
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
		bounded, err := boundedProjectionReadyPayload(batch.Payload)
		if err != nil {
			return err
		}
		if err := sealProjectionReadyBatch(parent, app, batch, bounded); err != nil {
			return err
		}
		if err := a.sendProjectionEvent(parent, app, batch); err != nil {
			for _, id := range batch.IDs {
				if err := a.failProjectionEvent(parent, app, projectionEventOutboxItem{ID: id, ProjectID: batch.ProjectID, Topic: topicProjectionReady, Attempts: batch.Attempts[id]}, err); err != nil {
					return err
				}
			}
		} else {
			for _, id := range batch.IDs {
				if _, err := app.AppDB().ExecContext(parent, `DELETE FROM projection_event_outbox WHERE event_id=?`, id); err != nil {
					return err
				}
			}
		}
		if app.CurrentProject() == "" {
			a.updateWorkerMetrics(batch.ProjectID, func(m *projectionWorkerMetrics) { m.EventNs += time.Since(batchStarted).Nanoseconds() })
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
		for _, key := range []string{"coverage_from", "coverage_to", "ready"} {
			if value, ok := src[key]; ok {
				dst[key] = value
			} else {
				delete(dst, key)
			}
		}
	}
}

func projectionEventNumber(value any) (int64, bool) {
	switch n := value.(type) {
	case float64:
		return int64(n), true
	case int:
		return int64(n), true
	case int64:
		return n, true
	case json.Number:
		v, err := n.Int64()
		return v, err == nil
	default:
		return 0, false
	}
}

// Bound the actual JSON bytes, including UTF-8, escaping, and metadata. The
// compact form is a projection-wide invalidation hint, not an empty change set.
const projectionReadyPayloadMaxBytes = 16 * 1024

func compactProjectionReadyScopes(payload map[string]any) {
	payload["all_scopes"] = true
	payload["scopes_truncated"] = true
	payload["scope_keys"] = []string{}
	delete(payload, "scope_key")
	// Once scope details are discarded, overlapping publications cannot be
	// counted exactly. Omit the count instead of claiming zero or summing it.
	delete(payload, "scope_count")
}

func boundedProjectionReadyPayload(payload map[string]any) ([]byte, error) {
	if payload == nil {
		return nil, fmt.Errorf("missing projection-ready payload")
	}
	if payload["all_scopes"] == true || payload["scopes_truncated"] == true {
		compactProjectionReadyScopes(payload)
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if len(encoded) <= projectionReadyPayloadMaxBytes {
		return encoded, nil
	}
	compactProjectionReadyScopes(payload)
	// The newest generation remains authoritative; the optional history must
	// not defeat the cap when several individually small events are combined.
	delete(payload, "generations")
	encoded, err = json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	if len(encoded) > projectionReadyPayloadMaxBytes {
		return nil, fmt.Errorf("projection-ready metadata exceeds %d bytes", projectionReadyPayloadMaxBytes)
	}
	return encoded, nil
}

// Persist the complete delivery snapshot atomically before sending. A crash or
// ambiguous acknowledgement retries exactly this payload with the same ID;
// later publications keep their own IDs and cannot be lost to deduplication.
func sealProjectionReadyBatch(ctx context.Context, app *sdk.AppCtx, batch *projectionReadyBatch, payload []byte) error {
	tx, err := app.AppDB().BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	id := batch.IDs[0]
	if _, err := tx.ExecContext(ctx, `UPDATE projection_event_outbox SET payload=?,delivery_sealed=1 WHERE event_id=?`, string(payload), id); err != nil {
		return err
	}
	for _, mergedID := range batch.IDs[1:] {
		if _, err := tx.ExecContext(ctx, `DELETE FROM projection_event_outbox WHERE event_id=?`, mergedID); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	batch.IDs = []string{id}
	return nil
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
