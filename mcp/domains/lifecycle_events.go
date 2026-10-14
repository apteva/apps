package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"sync"
	"time"
)

var lifecycleDeliveryMu sync.Mutex

// State and its event enter SQLite together. Bus delivery is at least once;
// consumers deduplicate the stable event_id if a transport acknowledgment is lost.
func queueDomainEvent(tx *sql.Tx, pid, domain, topic, key string, data map[string]any) error {
	id, err := randomToken(16)
	if err != nil {
		return err
	}
	payload := map[string]any{"schema_version": 1, "event_id": id, "project_id": pid, "domain": domain, "occurred_at": time.Now().UTC().Format(time.RFC3339Nano)}
	for k, v := range data {
		payload[k] = v
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	_, err = tx.Exec(`INSERT OR IGNORE INTO domain_lifecycle_events(event_id,project_id,domain,topic,dedupe_key,payload_json,created_at) VALUES(?,?,?,?,?,?,?)`, id, pid, domain, topic, key, string(raw), payload["occurred_at"])
	return err
}

func drainDomainEvents(parent context.Context, ctx *sdk.AppCtx, pid string) error {
	if !lifecycleDeliveryMu.TryLock() {
		return nil
	}
	defer lifecycleDeliveryMu.Unlock()
	deadline, cancel := context.WithTimeout(parent, 3*time.Second)
	defer cancel()
	rows, err := ctx.AppDB().Query(`SELECT id,topic,payload_json FROM domain_lifecycle_events WHERE project_id=? AND delivery_status='pending' ORDER BY id LIMIT 50`, pid)
	if err != nil {
		return err
	}
	type event struct {
		id         int64
		topic, raw string
	}
	events := []event{}
	for rows.Next() {
		var e event
		if err = rows.Scan(&e.id, &e.topic, &e.raw); err != nil {
			break
		}
		events = append(events, e)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, e := range events {
		if deadline.Err() != nil {
			return deadline.Err()
		}
		var data map[string]any
		if err = json.Unmarshal([]byte(e.raw), &data); err != nil {
			return err
		}
		err = ctx.EmitWithProjectAck(deadline, e.topic, pid, data)
		if err != nil {
			_, _ = ctx.AppDB().Exec(`UPDATE domain_lifecycle_events SET attempts=attempts+1,delivery_error=? WHERE id=?`, truncate(err.Error(), 400), e.id)
			return err
		}
		if _, err = ctx.AppDB().Exec(`UPDATE domain_lifecycle_events SET delivery_status='sent',attempts=attempts+1,delivery_error='',delivered_at=? WHERE id=?`, time.Now().UTC().Format(time.RFC3339Nano), e.id); err != nil {
			return err
		}
	}
	return nil
}

func deliverDomainEvents(ctx *sdk.AppCtx, pid string) {
	if err := drainDomainEvents(context.Background(), ctx, pid); err != nil {
		ctx.Logger().Warn("domain events retained for retry", "project", pid, "err", err)
	}
}

func (a *App) toolDomainEventsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	domain := strArg(args, "domain")
	if domain != "" {
		domain, err = normaliseDomainName(domain)
		if err != nil {
			return nil, err
		}
	}
	rows, err := ctx.AppDB().Query(`SELECT event_id,topic,payload_json,delivery_status,created_at FROM domain_lifecycle_events WHERE project_id=? AND (?='' OR domain=?) ORDER BY id DESC LIMIT 100`, pid, domain, domain)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var id, topic, raw, status, at string
		if err = rows.Scan(&id, &topic, &raw, &status, &at); err != nil {
			return nil, err
		}
		var data any
		if json.Unmarshal([]byte(raw), &data) != nil {
			return nil, fmt.Errorf("stored event is invalid")
		}
		out = append(out, map[string]any{"event_id": id, "topic": topic, "data": data, "delivery_status": status, "created_at": at})
	}
	return map[string]any{"events": out}, rows.Err()
}
