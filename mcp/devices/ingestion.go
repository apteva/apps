package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

func (a *App) ingest(d Device, kind, payload, received string) error {
	if d.ProjectID != a.projectID {
		return errors.New("ownership scope mismatch")
	}
	if kind == "response" {
		return a.processResponse(d, payload, received)
	}
	a.mutationMu.Lock()
	err := a.ingestLocked(d, kind, payload, received)
	a.mutationMu.Unlock()
	if err == nil {
		if d.Protocol == "apteva.devices/v2" && (kind == "state" || kind == "telemetry") {
			var envelope struct {
				MessageID string `json:"message_id"`
			}
			_ = json.Unmarshal([]byte(payload), &envelope)
			// Application acknowledgment follows the durable commit. A lost receipt
			// causes a client resend and the duplicate path emits the receipt again.
			_ = a.mqttPublish(a.topicRoot()+"devices/"+d.ID+"/receipts", map[string]any{"message_id": envelope.MessageID, "status": "accepted"}, false, 1)
		}
		a.wakeOutbox()
	}
	return err
}
func (a *App) ingestLocked(d Device, kind, payload, received string) error {
	// Recheck authorization under the mutation lock, after lifecycle changes.
	current, err := getDevice(a.ctx.AppDB(), a.projectID, d.ID, false)
	if err != nil {
		return err
	}
	if !current.Enabled {
		return errors.New("device is disabled")
	}
	d = current
	if len(payload) > a.limit("max_device_payload_bytes", 131072) {
		return ErrLimit
	}
	if kind == "manifest" || kind == "availability" {
		tx, e := a.ctx.AppDB().Begin()
		if e != nil {
			return e
		}
		defer tx.Rollback()
		topic := "devices.device.updated"
		data := map[string]any{"changed": "manifest"}
		if kind == "manifest" {
			var m map[string]any
			if e = decodeJSON(payload, &m); e != nil {
				return e
			}
			if e = validateManifest(m); e != nil {
				return e
			}
			if p := str(m, "protocol"); p != "" && p != d.Protocol {
				return errors.New("manifest cannot change the enrolled protocol")
			}
			raw, _ := json.Marshal(m)
			_, e = tx.Exec(`UPDATE devices SET manifest_json=?,last_seen=?,updated_at=? WHERE project_id=? AND id=?`, string(raw), received, received, a.projectID, d.ID)
		} else {
			availability := parseAvailability(payload)
			if availability == d.Availability {
				return nil
			}
			_, e = tx.Exec(`UPDATE devices SET availability=?,status=?,last_seen=?,updated_at=? WHERE project_id=? AND id=?`, availability, availability, received, received, a.projectID, d.ID)
			topic = "devices.device.status_changed"
			data = map[string]any{"status": availability}
		}
		if e != nil {
			return e
		}
		if e = a.event(tx, d.ID, topic, data); e != nil {
			return e
		}
		return tx.Commit()
	}
	if kind != "state" && kind != "telemetry" {
		return errors.New("unknown device data kind")
	}
	adapter, err := adapterFor(d.Protocol)
	if err != nil {
		return err
	}
	m, err := adapter.Decode(kind, payload)
	if err != nil {
		return err
	}
	if m.ObservedAt == "" {
		m.ObservedAt = received
	}
	observed, err := time.Parse(time.RFC3339Nano, m.ObservedAt)
	if err != nil {
		return errors.New("observed_at must be RFC3339")
	}
	m.ObservedAt = formatTime(observed)
	receivedTime, err := time.Parse(time.RFC3339Nano, received)
	if err != nil {
		return err
	}
	received = formatTime(receivedTime)
	if observed.After(receivedTime.Add(time.Duration(a.limit("max_clock_skew_seconds", 300)) * time.Second)) {
		return errors.New("observed_at exceeds allowed clock skew")
	}
	if m.MessageID == "" {
		m.MessageID = newID("legacy_")
	}
	raw, err := json.Marshal(m.Data)
	if err != nil {
		return err
	}
	metadata, _ := json.Marshal(m.Metadata)
	hash := fingerprint(map[string]any{"kind": kind, "message": m})
	tx, err := a.ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	var oldHash string
	err = tx.QueryRow(`SELECT fingerprint FROM device_messages WHERE project_id=? AND device_id=? AND message_id=?`, a.projectID, d.ID, m.MessageID).Scan(&oldHash)
	if err == nil {
		if oldHash != hash {
			return ErrConflict
		}
		return nil
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	var pending int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM device_outbox WHERE project_id=? AND delivered_at IS NULL`, a.projectID).Scan(&pending); err != nil {
		return err
	}
	if pending >= a.limit("max_pending_events", 50000) {
		return ErrLimit
	}
	var rows, bytes int
	if err = tx.QueryRow(`SELECT COUNT(*),COALESCE(SUM(length(CAST(data_json AS BLOB))+length(CAST(metadata_json AS BLOB))),0) FROM device_messages WHERE project_id=? AND device_id=?`, a.projectID, d.ID).Scan(&rows, &bytes); err != nil {
		return err
	}
	if rows >= a.limit("telemetry_max_rows_per_device", 10000) || bytes+len(raw)+len(metadata) > a.limit("max_history_bytes_per_device", 67108864) {
		return ErrLimit
	}
	_, err = tx.Exec(`INSERT INTO device_messages(project_id,device_id,message_id,fingerprint,kind,data_json,metadata_json,observed_at,received_at,session_id,sequence)VALUES(?,?,?,?,?,?,?,?,?,?,?)`, a.projectID, d.ID, m.MessageID, hash, kind, string(raw), string(metadata), m.ObservedAt, received, m.SessionID, m.Sequence)
	if err != nil {
		return err
	}
	applied := false
	changed := []string{}
	if m.UpdateState {
		var at, mid, session string
		var seq sql.NullInt64
		e := tx.QueryRow(`SELECT observed_at,message_id,session_id,sequence FROM device_streams WHERE device_id=?`, d.ID).Scan(&at, &mid, &session, &seq)
		newer := errors.Is(e, sql.ErrNoRows) || m.ObservedAt > at || (m.ObservedAt == at && m.MessageID > mid)
		if e != nil && !errors.Is(e, sql.ErrNoRows) {
			return e
		}
		if session == m.SessionID && session != "" && seq.Valid && m.Sequence != nil {
			newer = *m.Sequence > seq.Int64
		}
		if newer {
			values, ok := m.Data.(map[string]any)
			if !ok {
				values = map[string]any{"$": m.Data}
				m.Mode = "snapshot"
			}
			if len(values) > a.limit("max_state_keys_per_device", 1024) {
				return ErrLimit
			}
			if m.Mode == "snapshot" {
				existing, e := tx.Query(`SELECT key FROM device_state WHERE device_id=?`, d.ID)
				if e != nil {
					return e
				}
				for existing.Next() {
					var key string
					if e = existing.Scan(&key); e != nil {
						existing.Close()
						return e
					}
					if _, ok := values[key]; !ok {
						changed = append(changed, key)
					}
				}
				existing.Close()
				if _, err = tx.Exec(`DELETE FROM device_state WHERE device_id=?`, d.ID); err != nil {
					return err
				}
			}
			for key, value := range values {
				if key == "" || len(key) > 128 {
					return errors.New("state key must contain 1 to 128 characters")
				}
				v, _ := json.Marshal(value)
				var previous string
				e := tx.QueryRow(`SELECT value_json FROM device_state WHERE device_id=? AND key=?`, d.ID, key).Scan(&previous)
				if e != nil && !errors.Is(e, sql.ErrNoRows) {
					return e
				}
				if previous != string(v) {
					changed = append(changed, key)
				}
				_, err = tx.Exec(`INSERT INTO device_state(device_id,key,value_json,value_type,unit,source,updated_at,observed_at,message_id,session_id,sequence)VALUES(?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(device_id,key)DO UPDATE SET value_json=excluded.value_json,value_type=excluded.value_type,unit=excluded.unit,source=excluded.source,updated_at=excluded.updated_at,observed_at=excluded.observed_at,message_id=excluded.message_id,session_id=excluded.session_id,sequence=excluded.sequence`, d.ID, key, string(v), valueType(value), manifestUnits(d.Manifest)[key], kind, received, m.ObservedAt, m.MessageID, m.SessionID, m.Sequence)
				if err != nil {
					return err
				}
			}
			var count, stateBytes int
			if err = tx.QueryRow(`SELECT COUNT(*),COALESCE(SUM(length(CAST(value_json AS BLOB))),0) FROM device_state WHERE device_id=?`, d.ID).Scan(&count, &stateBytes); err != nil {
				return err
			}
			if count > a.limit("max_state_keys_per_device", 1024) || stateBytes > a.limit("max_state_bytes_per_device", 1048576) {
				return ErrLimit
			}
			_, err = tx.Exec(`INSERT INTO device_streams VALUES(?,?,?,?,?) ON CONFLICT(device_id)DO UPDATE SET observed_at=excluded.observed_at,message_id=excluded.message_id,session_id=excluded.session_id,sequence=excluded.sequence`, d.ID, m.ObservedAt, m.MessageID, m.SessionID, m.Sequence)
			if err != nil {
				return err
			}
			applied = true
		}
	}
	if _, err = tx.Exec(`UPDATE devices SET last_seen=?,updated_at=? WHERE project_id=? AND id=?`, received, received, a.projectID, d.ID); err != nil {
		return err
	}
	if len(changed) > 0 {
		if err = a.event(tx, d.ID, "devices.state.changed", map[string]any{"message_id": m.MessageID, "keys": changed, "source": kind}); err != nil {
			return err
		}
	}
	if err = a.event(tx, d.ID, "devices.data.received", map[string]any{"message_id": m.MessageID, "kind": kind, "observed_at": m.ObservedAt, "received_at": received, "state_applied": applied}); err != nil {
		return err
	}
	if kind == "telemetry" {
		if err = a.event(tx, d.ID, "devices.telemetry.received", map[string]any{"message_id": m.MessageID, "received_at": received}); err != nil {
			return err
		}
	}
	for measurement, quantity := range map[string]int{"data.messages": 1, "data.bytes": len(payload)} {
		if err = a.usage(tx, d.ID, measurement, m.MessageID, quantity); err != nil {
			return err
		}
	}
	if err = tx.Commit(); err != nil {
		return fmt.Errorf("persist device data: %w", err)
	}
	return nil
}
