package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

func insertDevice(db *sql.DB, d Device) error {
	manifest, _ := json.Marshal(d.Manifest)
	metadata, _ := json.Marshal(d.Metadata)
	_, err := db.Exec(`INSERT INTO devices
		(id, project_id, name, description, protocol, model, manufacturer, firmware,
		 mqtt_username, mqtt_client_id, enabled, status, availability, manifest_json, metadata_json,
		 credential_version, created_at, updated_at)
		VALUES (?,?,?,?,?,?,?,?,?,'',1,'provisioned','unknown',?,?,1,?,?)`,
		d.ID, d.ProjectID, d.Name, d.Description, d.Protocol, d.Model, d.Manufacturer,
		d.Firmware, d.MQTTUsername, string(manifest), string(metadata), d.CreatedAt, d.UpdatedAt)
	return err
}

func scanDevice(scanner interface{ Scan(...any) error }) (Device, error) {
	var d Device
	var enabled int
	var manifest, metadata string
	err := scanner.Scan(&d.ID, &d.ProjectID, &d.Name, &d.Description, &d.Protocol,
		&d.Model, &d.Manufacturer, &d.Firmware, &d.MQTTUsername, &d.MQTTClientID, &enabled,
		&d.Status, &d.Availability, &manifest, &metadata, &d.CredentialVersion,
		&d.LastSeen, &d.ConnectedAt, &d.DisconnectedAt, &d.CreatedAt, &d.UpdatedAt)
	if err != nil {
		return Device{}, err
	}
	d.Enabled = enabled != 0
	d.Manifest = jsonObject(manifest)
	d.Metadata = jsonObject(metadata)
	return d, nil
}

const deviceColumns = `id, project_id, name, description, protocol, model, manufacturer,
	firmware, mqtt_username, mqtt_client_id, enabled, status, availability, manifest_json, metadata_json,
	credential_version, last_seen, connected_at, disconnected_at, created_at, updated_at`

func getDevice(db *sql.DB, projectID, id string, withState bool) (Device, error) {
	d, err := scanDevice(db.QueryRow(`SELECT `+deviceColumns+` FROM devices WHERE project_id=? AND id=?`, projectID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, fmt.Errorf("device %q not found", id)
	}
	if err != nil {
		return Device{}, err
	}
	if withState {
		d.State, err = listState(db, id, "")
	}
	return d, err
}

func getDeviceByUsername(db *sql.DB, projectID, username string) (Device, error) {
	d, err := scanDevice(db.QueryRow(`SELECT `+deviceColumns+` FROM devices WHERE project_id=? AND mqtt_username=?`, projectID, username))
	return d, err
}

func listDevices(db *sql.DB, projectID, status, q string, limit int) ([]Device, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := `SELECT ` + deviceColumns + ` FROM devices WHERE project_id=?`
	args := []any{projectID}
	if status != "" {
		query += ` AND status=?`
		args = append(args, status)
	}
	if q != "" {
		query += ` AND (LOWER(id) LIKE ? OR LOWER(name) LIKE ? OR LOWER(model) LIKE ?)`
		like := "%" + strings.ToLower(q) + "%"
		args = append(args, like, like, like)
	}
	query += ` ORDER BY name, id LIMIT ?`
	args = append(args, limit)
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Device{}
	for rows.Next() {
		d, err := scanDevice(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

func updateDeviceFields(db *sql.DB, projectID, id string, patch map[string]any) (Device, error) {
	allowed := map[string]bool{"name": true, "description": true, "model": true, "manufacturer": true, "firmware": true}
	sets, args := []string{}, []any{}
	for _, key := range []string{"name", "description", "model", "manufacturer", "firmware"} {
		if value, ok := patch[key]; ok && allowed[key] {
			text, ok := value.(string)
			if !ok {
				return Device{}, fmt.Errorf("%s must be a string", key)
			}
			if key == "name" && strings.TrimSpace(text) == "" {
				return Device{}, errors.New("name cannot be empty")
			}
			sets = append(sets, key+"=?")
			args = append(args, strings.TrimSpace(text))
		}
	}
	if value, ok := patch["metadata"]; ok {
		obj, ok := value.(map[string]any)
		if !ok {
			return Device{}, errors.New("metadata must be an object")
		}
		raw, _ := json.Marshal(obj)
		sets = append(sets, "metadata_json=?")
		args = append(args, string(raw))
	}
	if len(sets) == 0 {
		return getDevice(db, projectID, id, true)
	}
	sets = append(sets, "updated_at=?")
	args = append(args, nowText(), projectID, id)
	res, err := db.Exec(`UPDATE devices SET `+strings.Join(sets, ",")+` WHERE project_id=? AND id=?`, args...)
	if err != nil {
		return Device{}, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return Device{}, fmt.Errorf("device %q not found", id)
	}
	return getDevice(db, projectID, id, true)
}

func setDeviceEnabled(db *sql.DB, projectID, id string, enabled bool) error {
	status := "offline"
	availability := "offline"
	if enabled {
		status, availability = "provisioned", "unknown"
	}
	res, err := db.Exec(`UPDATE devices SET enabled=?, status=?, availability=?, mqtt_client_id=CASE WHEN ?=0 THEN '' ELSE mqtt_client_id END, updated_at=? WHERE project_id=? AND id=?`,
		boolInt(enabled), status, availability, boolInt(enabled), nowText(), projectID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("device %q not found", id)
	}
	return nil
}

func setDeviceConnection(db *sql.DB, projectID, username, clientID string, connected bool, at string) (Device, bool, error) {
	d, err := getDeviceByUsername(db, projectID, username)
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, false, nil
	}
	if err != nil {
		return Device{}, false, err
	}
	if !d.Enabled {
		return d, false, nil
	}
	previous := d.Status
	if connected {
		_, err = db.Exec(`UPDATE devices SET status='online', availability='online', mqtt_client_id=?, connected_at=?, last_seen=?, updated_at=? WHERE id=?`, clientID, at, at, at, d.ID)
	} else {
		// A broker may establish a replacement session before delivering the old
		// session's disconnect event. Do not let that stale event mark the new
		// connection offline.
		if d.MQTTClientID != "" && clientID != "" && d.MQTTClientID != clientID {
			return d, false, nil
		}
		_, err = db.Exec(`UPDATE devices SET status='offline', availability='offline', mqtt_client_id='', disconnected_at=?, updated_at=? WHERE id=?`, at, at, d.ID)
	}
	if err != nil {
		return Device{}, false, err
	}
	d, err = getDevice(db, projectID, d.ID, false)
	return d, previous != d.Status, err
}

func setAvailability(db *sql.DB, projectID, id, availability, at string) (bool, error) {
	var old string
	if err := db.QueryRow(`SELECT availability FROM devices WHERE project_id=? AND id=?`, projectID, id).Scan(&old); err != nil {
		return false, err
	}
	status := "offline"
	if availability == "online" {
		status = "online"
	}
	_, err := db.Exec(`UPDATE devices SET availability=?, status=?, last_seen=?, updated_at=? WHERE project_id=? AND id=?`, availability, status, at, at, projectID, id)
	return old != availability, err
}

func touchDevice(db *sql.DB, projectID, id, at string) error {
	_, err := db.Exec(`UPDATE devices SET last_seen=?, updated_at=? WHERE project_id=? AND id=?`, at, at, projectID, id)
	return err
}

func saveManifest(db *sql.DB, projectID, id string, manifest map[string]any, at string) error {
	raw, _ := json.Marshal(manifest)
	name, _ := manifest["name"].(string)
	model, _ := manifest["model"].(string)
	manufacturer, _ := manifest["manufacturer"].(string)
	firmware, _ := manifest["firmware"].(string)
	_, err := db.Exec(`UPDATE devices SET manifest_json=?,
		name=CASE WHEN ?='' THEN name ELSE ? END,
		model=CASE WHEN ?='' THEN model ELSE ? END,
		manufacturer=CASE WHEN ?='' THEN manufacturer ELSE ? END,
		firmware=CASE WHEN ?='' THEN firmware ELSE ? END,
		last_seen=?, updated_at=? WHERE project_id=? AND id=?`,
		string(raw), name, name, model, model, manufacturer, manufacturer, firmware, firmware, at, at, projectID, id)
	return err
}

func upsertState(db *sql.DB, deviceID, source string, values map[string]any, units map[string]string, at string) ([]string, error) {
	changed := []string{}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	for key, value := range values {
		if key == "" || len(key) > 128 {
			continue
		}
		raw, err := json.Marshal(value)
		if err != nil || len(raw) > 65536 {
			continue
		}
		var previous string
		err = tx.QueryRow(`SELECT value_json FROM device_state WHERE device_id=? AND key=?`, deviceID, key).Scan(&previous)
		if errors.Is(err, sql.ErrNoRows) || (err == nil && previous != string(raw)) {
			changed = append(changed, key)
		} else if err != nil {
			return nil, err
		}
		_, err = tx.Exec(`INSERT INTO device_state(device_id,key,value_json,value_type,unit,source,updated_at)
			VALUES(?,?,?,?,?,?,?) ON CONFLICT(device_id,key) DO UPDATE SET
			value_json=excluded.value_json,value_type=excluded.value_type,unit=excluded.unit,
			source=excluded.source,updated_at=excluded.updated_at`,
			deviceID, key, string(raw), valueType(value), units[key], source, at)
		if err != nil {
			return nil, err
		}
	}
	return changed, tx.Commit()
}

func listState(db *sql.DB, deviceID, key string) ([]StateValue, error) {
	query := `SELECT key,value_json,value_type,unit,source,updated_at FROM device_state WHERE device_id=?`
	args := []any{deviceID}
	if key != "" {
		query += ` AND key=?`
		args = append(args, key)
	}
	query += ` ORDER BY key`
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []StateValue{}
	for rows.Next() {
		var s StateValue
		var raw string
		if err := rows.Scan(&s.Key, &raw, &s.ValueType, &s.Unit, &s.Source, &s.UpdatedAt); err != nil {
			return nil, err
		}
		var value any
		_ = json.Unmarshal([]byte(raw), &value)
		s.Value = value
		out = append(out, s)
	}
	return out, rows.Err()
}

func insertTelemetry(db *sql.DB, deviceID string, payload map[string]any, at string) error {
	raw, _ := json.Marshal(payload)
	_, err := db.Exec(`INSERT INTO device_telemetry(device_id,payload_json,received_at) VALUES(?,?,?)`, deviceID, string(raw), at)
	return err
}

func insertEvent(db *sql.DB, deviceID, kind string, data any) {
	raw, _ := json.Marshal(data)
	_, _ = db.Exec(`INSERT INTO device_events(device_id,kind,data_json,created_at) VALUES(?,?,?,?)`, nullableString(deviceID), kind, string(raw), nowText())
}

func insertCommand(db *sql.DB, c Command) error {
	argsRaw, _ := json.Marshal(c.Arguments)
	requestRaw, _ := json.Marshal(c.Request)
	_, err := db.Exec(`INSERT INTO device_commands
		(id,device_id,operation,target,arguments_json,request_json,status,idempotency_key,
		timeout_ms,created_at,deadline_at) VALUES(?,?,?,?,?,?,'queued',?,?,?,?)`,
		c.ID, c.DeviceID, c.Operation, c.Target, string(argsRaw), string(requestRaw),
		nullableString(c.IdempotencyKey), c.TimeoutMS, c.CreatedAt, c.DeadlineAt)
	return err
}

func scanCommand(scanner interface{ Scan(...any) error }) (Command, error) {
	var c Command
	var argsRaw, requestRaw string
	var resultRaw, idempotency *string
	err := scanner.Scan(&c.ID, &c.DeviceID, &c.Operation, &c.Target, &argsRaw, &requestRaw,
		&c.Status, &resultRaw, &c.Error, &idempotency, &c.TimeoutMS, &c.CreatedAt,
		&c.SentAt, &c.DeadlineAt, &c.CompletedAt)
	if err != nil {
		return Command{}, err
	}
	c.Arguments, c.Request = jsonObject(argsRaw), jsonObject(requestRaw)
	c.Result = jsonValue(resultRaw)
	if idempotency != nil {
		c.IdempotencyKey = *idempotency
	}
	return c, nil
}

const commandColumns = `id,device_id,operation,target,arguments_json,request_json,status,
	result_json,error,idempotency_key,timeout_ms,created_at,sent_at,deadline_at,completed_at`

func getCommand(db *sql.DB, id string) (Command, error) {
	c, err := scanCommand(db.QueryRow(`SELECT `+commandColumns+` FROM device_commands WHERE id=?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Command{}, fmt.Errorf("command %q not found", id)
	}
	return c, err
}

func getCommandByIdempotency(db *sql.DB, deviceID, key string) (Command, error) {
	return scanCommand(db.QueryRow(`SELECT `+commandColumns+` FROM device_commands WHERE device_id=? AND idempotency_key=?`, deviceID, key))
}

func markCommandSent(db *sql.DB, id, at string) error {
	_, err := db.Exec(`UPDATE device_commands SET status='sent',sent_at=? WHERE id=? AND status='queued'`, at, id)
	return err
}

func markCommandPublishFailed(db *sql.DB, id string, publishErr error) error {
	_, err := db.Exec(`UPDATE device_commands SET status='failed',error=?,completed_at=? WHERE id=? AND status='queued'`, publishErr.Error(), nowText(), id)
	return err
}

func completeCommand(db *sql.DB, deviceID, id string, succeeded bool, result any, message string, at string) (Command, bool, error) {
	status := "failed"
	if succeeded {
		status = "succeeded"
	}
	raw, _ := json.Marshal(result)
	res, err := db.Exec(`UPDATE device_commands SET status=?,result_json=?,error=?,completed_at=?
		WHERE id=? AND device_id=? AND status IN ('queued','sent')`, status, string(raw), message, at, id, deviceID)
	if err != nil {
		return Command{}, false, err
	}
	changed, _ := res.RowsAffected()
	if changed == 0 {
		return Command{}, false, nil
	}
	c, err := getCommand(db, id)
	return c, true, err
}

func markTimedOutCommands(db *sql.DB, now time.Time) ([]string, error) {
	rows, err := db.Query(`SELECT id FROM device_commands WHERE status IN ('queued','sent') AND deadline_at<=?`, formatTime(now))
	if err != nil {
		return nil, err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			rows.Close()
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	updated := make([]string, 0, len(ids))
	for _, id := range ids {
		var result sql.Result
		result, err = db.Exec(`UPDATE device_commands SET status='timed_out',error='device response timeout',completed_at=? WHERE id=? AND status IN ('queued','sent')`, formatTime(now), id)
		if err != nil {
			return nil, err
		}
		if changed, _ := result.RowsAffected(); changed == 1 {
			updated = append(updated, id)
		}
	}
	return updated, nil
}

func listCommands(db *sql.DB, deviceID, status string, limit int) ([]Command, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	query := `SELECT ` + commandColumns + ` FROM device_commands WHERE 1=1`
	args := []any{}
	if deviceID != "" {
		query += ` AND device_id=?`
		args = append(args, deviceID)
	}
	if status != "" {
		query += ` AND status=?`
		args = append(args, status)
	}
	query += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Command{}
	for rows.Next() {
		c, err := scanCommand(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

func deleteDeviceRow(db *sql.DB, projectID, id string) error {
	res, err := db.Exec(`DELETE FROM devices WHERE project_id=? AND id=?`, projectID, id)
	if err != nil {
		return err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return fmt.Errorf("device %q not found", id)
	}
	return nil
}

func pruneTelemetry(db *sql.DB, before time.Time, maxRows int) error {
	if _, err := db.Exec(`DELETE FROM device_telemetry WHERE received_at<?`, formatTime(before)); err != nil {
		return err
	}
	_, err := db.Exec(`DELETE FROM device_telemetry WHERE id IN (
		SELECT id FROM (
			SELECT id, ROW_NUMBER() OVER (PARTITION BY device_id ORDER BY received_at DESC) AS rn
			FROM device_telemetry
		) WHERE rn>?
	)`, maxRows)
	return err
}

func pruneHistory(db *sql.DB, before time.Time) error {
	cutoff := formatTime(before)
	if _, err := db.Exec(`DELETE FROM device_commands WHERE completed_at IS NOT NULL AND completed_at<?`, cutoff); err != nil {
		return err
	}
	_, err := db.Exec(`DELETE FROM device_events WHERE created_at<?`, cutoff)
	return err
}

func valueType(value any) string {
	switch value.(type) {
	case nil:
		return "null"
	case bool:
		return "boolean"
	case float64, float32, int, int64, json.Number:
		return "number"
	case string:
		return "string"
	case []any:
		return "array"
	case map[string]any:
		return "object"
	default:
		return "unknown"
	}
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func nullableString(v string) any {
	if strings.TrimSpace(v) == "" {
		return nil
	}
	return v
}
