package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

func scanDevice(scanner interface{ Scan(...any) error }) (Device, error) {
	var d Device
	var enabled int
	var manifest, metadata string
	err := scanner.Scan(&d.ID, &d.ProjectID, &d.Name, &d.Description, &d.Protocol,
		&d.Model, &d.Manufacturer, &d.Firmware, &d.MQTTUsername, &d.TopicRoot, &d.MQTTClientID, &enabled,
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
	firmware, mqtt_username, topic_root, mqtt_client_id, enabled, status, availability, manifest_json, metadata_json,
	credential_version, last_seen, connected_at, disconnected_at, created_at, updated_at`

func getDevice(db sqlStore, projectID, id string, withState bool) (Device, error) {
	d, err := scanDevice(db.QueryRow(`SELECT `+deviceColumns+` FROM devices WHERE project_id=? AND id=?`, projectID, id))
	if errors.Is(err, sql.ErrNoRows) {
		return Device{}, fmt.Errorf("%w: device %q", ErrNotFound, id)
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

func updateDeviceFields(db sqlStore, projectID, id string, patch map[string]any) (Device, error) {
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
		return Device{}, fmt.Errorf("%w: device %q", ErrNotFound, id)
	}
	return getDevice(db, projectID, id, true)
}

func saveManifest(db sqlStore, projectID, id string, manifest map[string]any, at string) error {
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

func listState(db sqlStore, deviceID, key string) ([]StateValue, error) {
	query := `SELECT key,value_json,value_type,unit,source,updated_at,observed_at,message_id FROM device_state WHERE device_id=?`
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
		if err := rows.Scan(&s.Key, &raw, &s.ValueType, &s.Unit, &s.Source, &s.UpdatedAt, &s.ObservedAt, &s.MessageID); err != nil {
			return nil, err
		}
		var value any
		_ = decodeJSON(raw, &value)
		s.Value = value
		out = append(out, s)
	}
	return out, rows.Err()
}

func insertEvent(db *sql.DB, deviceID, kind string, data any) {
	raw, _ := json.Marshal(data)
	_, _ = db.Exec(`INSERT INTO device_events(device_id,kind,data_json,created_at) VALUES(?,?,?,?)`, nullableString(deviceID), kind, string(raw), nowText())
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
		return Command{}, fmt.Errorf("%w: command %q", ErrNotFound, id)
	}
	return c, err
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
