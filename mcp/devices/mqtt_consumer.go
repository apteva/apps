package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"strings"
	"time"
)

var ErrInvalidData = errors.New("invalid device data")

func invalidData(err error) error { return fmt.Errorf("%w: %v", ErrInvalidData, err) }

const deviceConsumerName = "devices-ingestion-v1"

type MQTTRecord struct {
	ID         string `json:"message_id"`
	Sequence   string `json:"sequence"`
	Topic      string `json:"topic"`
	Payload    string `json:"payload_base64"`
	Username   string `json:"username"`
	AcceptedAt int64  `json:"accepted_at"`
}
type consumerBatch struct {
	Messages []MQTTRecord `json:"messages"`
	Lease    string       `json:"lease_token"`
}

func (a *App) consumerCall(ctx context.Context, args map[string]any, out any) error {
	call, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()
	return sdk.CallAppResultContext(call, a.ctx.PlatformAPI(), "mqtt", "mqtt_consumer", args, out)
}
func (a *App) ensureConsumer(ctx context.Context) error {
	var result map[string]any
	err := a.consumerCall(ctx, map[string]any{"action": "status", "name": deviceConsumerName}, &result)
	if err == nil {
		return nil
	}
	if !strings.Contains(strings.ToLower(err.Error()), "not found") {
		return err
	}
	return a.consumerCall(ctx, map[string]any{"action": "create", "name": deviceConsumerName, "topic_filter": a.topicRoot() + "devices/+/+", "start": "earliest"}, &result)
}
func (a *App) runMQTTConsumer(ctx context.Context, _ *sdk.AppCtx) error {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	for {
		if err := a.consumeMQTTBatch(ctx); err != nil {
			a.consumerStateMu.Lock()
			changed := a.consumerError != err.Error()
			a.consumerError = err.Error()
			a.consumerStateMu.Unlock()
			if changed {
				a.ctx.Logger().Warn("devices MQTT durable consumer", "err", err.Error())
			}
		} else {
			a.consumerStateMu.Lock()
			a.consumerError = ""
			a.consumerLastPoll = nowText()
			a.consumerStateMu.Unlock()
		}
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
		}
	}
}
func (a *App) consumeMQTTBatch(ctx context.Context) error {
	a.consumerMu.Lock()
	defer a.consumerMu.Unlock()
	var batch consumerBatch
	if err := a.consumerCall(ctx, map[string]any{"action": "claim", "name": deviceConsumerName, "limit": 25, "lease_seconds": 60}, &batch); err != nil {
		return err
	}
	if len(batch.Messages) == 0 {
		return nil
	}
	if batch.Lease == "" {
		return errors.New("MQTT consumer returned messages without a lease")
	}
	for _, record := range batch.Messages {
		if err := a.processMQTTRecord(record); err != nil {
			var result map[string]any
			_ = a.consumerCall(ctx, map[string]any{"action": "retry", "name": deviceConsumerName, "lease_token": batch.Lease, "retry_seconds": 2}, &result)
			return err
		}
	}
	var result map[string]any
	if err := a.consumerCall(ctx, map[string]any{"action": "ack", "name": deviceConsumerName, "lease_token": batch.Lease}, &result); err != nil {
		return err
	}
	// Only acknowledged receipts may be retention-pruned. A failed checkpoint
	// write is conservative: the small local receipt remains until recovery.
	a.mutationMu.Lock()
	tx, err := a.ctx.AppDB().Begin()
	if err == nil {
		for _, record := range batch.Messages {
			_, err = tx.Exec(`UPDATE device_transport_receipts SET acknowledged_at=? WHERE project_id=? AND message_id=?`, nowText(), a.projectID, record.ID)
			if err != nil {
				break
			}
		}
		if err == nil {
			_, err = tx.Exec(`DELETE FROM device_ingest_rejections WHERE rowid IN(SELECT r.rowid FROM device_ingest_rejections r LEFT JOIN device_transport_receipts t ON t.project_id=r.project_id AND t.message_id=r.message_id WHERE r.project_id=? AND (t.acknowledged_at IS NOT NULL OR t.message_id IS NULL) ORDER BY r.created_at DESC,r.rowid DESC LIMIT -1 OFFSET ?)`, a.projectID, a.limit("max_ingest_rejections", 10000))
		}
		if err == nil {
			err = tx.Commit()
		} else {
			_ = tx.Rollback()
		}
	}
	a.mutationMu.Unlock()
	if err != nil {
		return err
	}
	for _, record := range batch.Messages {
		parts, ok := parseDeviceTopic(strings.TrimPrefix(record.Topic, a.topicRoot()))
		if !ok || (parts.kind != "state" && parts.kind != "telemetry") {
			continue
		}
		d, e := getDevice(a.ctx.AppDB(), a.projectID, parts.deviceID, false)
		if e != nil || d.Protocol != "apteva.devices/v2" || d.MQTTUsername != record.Username {
			continue
		}
		var outcome string
		e = a.ctx.AppDB().QueryRow(`SELECT outcome FROM device_transport_receipts WHERE project_id=? AND message_id=?`, a.projectID, record.ID).Scan(&outcome)
		if e != nil || outcome != "accepted" {
			continue
		}
		payload, e := base64.StdEncoding.DecodeString(record.Payload)
		if e != nil {
			continue
		}
		var envelope struct {
			MessageID string `json:"message_id"`
		}
		_ = json.Unmarshal(payload, &envelope)
		if envelope.MessageID != "" {
			_ = a.mqttPublish(a.topicRoot()+"devices/"+d.ID+"/receipts", map[string]any{"message_id": envelope.MessageID, "status": "accepted"}, false, 1)
		}
	}
	a.wakeOutbox()
	return nil
}
func (a *App) processMQTTRecord(record MQTTRecord) error {
	if record.ID == "" || len(record.ID) > 256 {
		return errors.New("invalid durable MQTT message identity")
	}
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	var existing int
	if err := a.ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM device_transport_receipts WHERE project_id=? AND message_id=?`, a.projectID, record.ID).Scan(&existing); err != nil {
		return err
	}
	if existing > 0 {
		return nil
	}
	a.activeMQTTMessageID = record.ID
	defer func() { a.activeMQTTMessageID = "" }()
	parts, ok := parseDeviceTopic(strings.TrimPrefix(record.Topic, a.topicRoot()))
	if !strings.HasPrefix(record.Topic, a.topicRoot()) || !ok {
		return a.ignoreMQTTRecord("", "ignored", "")
	}
	d, err := getDevice(a.ctx.AppDB(), a.projectID, parts.deviceID, false)
	if errors.Is(err, ErrNotFound) {
		return a.ignoreMQTTRecord(parts.deviceID, "rejected", "unknown device")
	}
	if err != nil {
		return err
	}
	if !d.Enabled || record.Username == "" || d.MQTTUsername != record.Username {
		return a.ignoreMQTTRecord(d.ID, "rejected", "device credential unavailable or identity mismatch")
	}
	raw, err := base64.StdEncoding.DecodeString(record.Payload)
	if err != nil {
		return a.ignoreMQTTRecord(d.ID, "rejected", "invalid payload encoding")
	}
	at := formatTime(time.Unix(record.AcceptedAt, 0))
	if record.AcceptedAt <= 0 {
		at = nowText()
	}
	if parts.kind == "response" {
		err = a.responseLocked(d, string(raw), nowText())
	} else {
		err = a.ingestLocked(d, parts.kind, string(raw), at)
	}
	if err == nil {
		return nil
	}
	if errors.Is(err, ErrInvalidData) || errors.Is(err, ErrConflict) || errors.Is(err, ErrDeviceAuth) || errors.Is(err, ErrNotFound) {
		reason := "invalid device data"
		if errors.Is(err, ErrConflict) {
			reason = "logical message idempotency conflict"
		}
		if errors.Is(err, ErrDeviceAuth) {
			reason = "device credential unavailable"
		}
		if errors.Is(err, ErrNotFound) {
			reason = "resource no longer exists"
		}
		return a.ignoreMQTTRecord(d.ID, "rejected", reason)
	}
	// Capacity and storage failures keep the broker cursor unacknowledged.
	return err
}
func (a *App) ignoreMQTTRecord(device, outcome, reason string) error {
	if outcome == "ignored" {
		return nil
	}
	tx, err := a.ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if outcome == "rejected" {
		var pending int
		if err = tx.QueryRow(`SELECT COUNT(*) FROM device_outbox WHERE project_id=? AND delivered_at IS NULL`, a.projectID).Scan(&pending); err != nil {
			return err
		}
		if pending >= a.limit("max_pending_events", 50000) {
			return ErrLimit
		}
		if len(reason) > 512 {
			reason = reason[:512]
		}
		_, err = tx.Exec(`INSERT INTO device_ingest_rejections VALUES(?,?,?,?,?) ON CONFLICT DO NOTHING`, a.projectID, a.activeMQTTMessageID, device, reason, nowText())
		if err != nil {
			return err
		}
		if err = a.event(tx, device, "devices.data.rejected", map[string]any{"transport": "mqtt", "transport_message_id": a.activeMQTTMessageID, "reason": reason}); err != nil {
			return err
		}
	}
	if err = a.recordMQTTReceipt(tx, device, outcome); err != nil {
		return err
	}
	return tx.Commit()
}
func (a *App) recordMQTTReceipt(tx *sql.Tx, device, outcome string) error {
	if a.activeMQTTMessageID == "" {
		return nil
	}
	var total int
	if err := tx.QueryRow(`SELECT COUNT(*) FROM device_transport_receipts WHERE project_id=?`, a.projectID).Scan(&total); err != nil {
		return err
	}
	if total >= a.limit("max_transport_receipts", 1000000) {
		return ErrLimit
	}
	_, err := tx.Exec(`INSERT INTO device_transport_receipts(project_id,message_id,device_id,outcome,created_at)VALUES(?,?,?,?,?) ON CONFLICT DO NOTHING`, a.projectID, a.activeMQTTMessageID, device, outcome, nowText())
	return err
}
func (a *App) commitIngestion(tx *sql.Tx, device string) error {
	if err := a.recordMQTTReceipt(tx, device, "accepted"); err != nil {
		return err
	}
	return tx.Commit()
}
func (a *App) transportStatus() map[string]any {
	a.consumerStateMu.RLock()
	defer a.consumerStateMu.RUnlock()
	return map[string]any{"consumer": deviceConsumerName, "delivery": "durable-at-least-once", "last_poll_at": a.consumerLastPoll, "error": a.consumerError, "ready": a.consumerLastPoll != "" && a.consumerError == ""}
}
