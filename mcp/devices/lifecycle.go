package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

type lifecycleOperation struct {
	ID          string  `json:"operation_id"`
	DeviceID    string  `json:"device_id"`
	Kind        string  `json:"kind"`
	Status      string  `json:"status"`
	Error       string  `json:"error,omitempty"`
	CreatedAt   string  `json:"created_at"`
	CompletedAt *string `json:"completed_at,omitempty"`
	args        map[string]any
	secret      string
	expires     *string
}

func (a *App) getOperation(id string) (lifecycleOperation, error) {
	var op lifecycleOperation
	var raw string
	err := a.ctx.AppDB().QueryRow(`SELECT id,device_id,kind,status,error,created_at,completed_at,arguments_json,secret_cipher,secret_expires_at FROM device_operations WHERE project_id=? AND id=?`, a.projectID, id).Scan(&op.ID, &op.DeviceID, &op.Kind, &op.Status, &op.Error, &op.CreatedAt, &op.CompletedAt, &raw, &op.secret, &op.expires)
	op.args = jsonObject(raw)
	if errors.Is(err, sql.ErrNoRows) {
		err = errors.New("operation not found")
	}
	return op, err
}
func (a *App) lifecycle(kind string, input map[string]any) (map[string]any, error) {
	a.mutationMu.Lock()
	result, err := a.lifecycleLocked(kind, input)
	a.mutationMu.Unlock()
	if err == nil {
		a.wakeOutbox()
	}
	return result, err
}
func (a *App) lifecycleLocked(kind string, input map[string]any) (map[string]any, error) {
	for _, field := range []string{"metadata", "capabilities"} {
		if v, ok := input[field]; ok {
			if _, valid := v.(map[string]any); !valid {
				return nil, fmt.Errorf("%s must be a JSON object", field)
			}
		}
	}
	args := cloneMap(input)
	delete(args, "_project_id")
	delete(args, "scope_id")
	key := strings.TrimSpace(str(args, "idempotency_key"))
	delete(args, "idempotency_key")
	delete(args, "confirm")
	if len(key) > 128 {
		return nil, errors.New("idempotency_key exceeds 128 characters")
	}
	id := strings.TrimSpace(str(args, "device_id"))
	if kind == "provision" {
		name := strings.TrimSpace(str(args, "name"))
		if name == "" || len(name) > 128 {
			return nil, errors.New("name must contain 1 to 128 characters")
		}
		if id == "" {
			id = slugDeviceID(name)
		}
		protocol := firstNonEmpty(str(args, "protocol"), "apteva.devices/v1")
		if _, err := adapterFor(protocol); err != nil {
			return nil, err
		}
		manifest := objectOrEmpty(args, "capabilities")
		manifest["protocol"] = protocol
		if err := validateManifest(manifest); err != nil {
			return nil, err
		}
		args["name"] = name
		args["protocol"] = protocol
		args["capabilities"] = manifest
	}
	if !validDeviceID(id) {
		return nil, errors.New("invalid device_id")
	}
	args["device_id"] = id
	if key == "" {
		if kind == "rotate" || kind == "enable" || kind == "disable" {
			key = newID(kind + "_")
		} else {
			key = id
		}
	}
	if str(input, "idempotency_key") == "" && (kind == "enable" || kind == "disable") {
		var pendingID string
		e := a.ctx.AppDB().QueryRow(`SELECT id FROM device_operations WHERE project_id=? AND device_id=? AND kind=? AND status='pending'`, a.projectID, id, kind).Scan(&pendingID)
		if e == nil {
			op, err := a.getOperation(pendingID)
			if err != nil {
				return nil, err
			}
			_ = a.executeOperation(op)
			return a.operationResult(op.ID)
		}
		d, e := getDevice(a.ctx.AppDB(), a.projectID, id, false)
		if e != nil {
			return nil, e
		}
		if d.Enabled == (kind == "enable") && d.Status != "disable_pending" && d.Status != "delete_pending" {
			return map[string]any{"status": "completed", "device_id": id, "enabled": d.Enabled}, nil
		}
	}

	hash := fingerprint(args)
	var existingID, oldHash string
	err := a.ctx.AppDB().QueryRow(`SELECT id,fingerprint FROM device_operations WHERE project_id=? AND kind=? AND idempotency_key=?`, a.projectID, kind, key).Scan(&existingID, &oldHash)
	if err == nil {
		if oldHash != hash {
			return nil, ErrConflict
		}
		op, e := a.getOperation(existingID)
		if e != nil {
			return nil, e
		}
		if op.Status == "pending" {
			_ = a.executeOperation(op)
		}
		return a.operationResult(op.ID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	if kind == "provision" {
		var count int
		if err = a.ctx.AppDB().QueryRow(`SELECT (SELECT COUNT(*) FROM devices WHERE project_id=?) + (SELECT COUNT(*) FROM device_operations WHERE project_id=? AND kind='provision' AND status='pending')`, a.projectID, a.projectID).Scan(&count); err != nil {
			return nil, err
		}
		if count >= a.limit("max_devices", 10000) {
			return nil, ErrLimit
		}
		var tomb int
		_ = a.ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM device_tombstones WHERE project_id=? AND device_id=?`, a.projectID, id).Scan(&tomb)
		if tomb > 0 {
			return nil, fmt.Errorf("%w: device id was retired; use a new id", ErrConflict)
		}
		if _, err = getDevice(a.ctx.AppDB(), a.projectID, id, false); err == nil {
			return nil, fmt.Errorf("%w: device already exists", ErrConflict)
		} else if !errors.Is(err, ErrNotFound) {
			return nil, err
		}
	} else {
		if kind == "delete" {
			var retired int
			if e := a.ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM device_tombstones WHERE project_id=? AND device_id=?`, a.projectID, id).Scan(&retired); e != nil {
				return nil, e
			}
			if retired > 0 {
				return map[string]any{"status": "completed", "device_id": id, "deleted": true}, nil
			}
		}
		if _, err = getDevice(a.ctx.AppDB(), a.projectID, id, false); err != nil {
			return nil, err
		}
	}
	op := lifecycleOperation{ID: newID("op_"), DeviceID: id, Kind: kind, Status: "pending", args: args}
	if kind == "provision" {
		args["credential_name"] = "dev_" + op.ID
	}
	cipherText := ""
	if kind == "provision" || kind == "rotate" {
		secret, e := generateSecret()
		if e != nil {
			return nil, e
		}
		cipherText, e = a.cryptSecret(op.ID, secret, true)
		if e != nil {
			return nil, e
		}
	}
	raw, e := json.Marshal(args)
	if e != nil {
		return nil, e
	}
	if len(raw) > 262144 {
		return nil, ErrLimit
	}
	tx, err := a.ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var pending int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM device_operations WHERE project_id=? AND device_id=? AND status='pending'`, a.projectID, id).Scan(&pending); err != nil {
		return nil, err
	}
	if pending > 0 {
		return nil, fmt.Errorf("%w: lifecycle operation pending", ErrConflict)
	}
	_, err = tx.Exec(`INSERT INTO device_operations(id,project_id,device_id,kind,idempotency_key,fingerprint,arguments_json,secret_cipher,created_at)VALUES(?,?,?,?,?,?,?,?,?)`, op.ID, a.projectID, id, kind, key, hash, string(raw), cipherText, nowText())
	if err != nil {
		return nil, err
	}
	if kind == "disable" || kind == "delete" {
		if _, err = tx.Exec(`UPDATE devices SET enabled=0,status=?,updated_at=? WHERE project_id=? AND id=?`, kind+"_pending", nowText(), a.projectID, id); err != nil {
			return nil, err
		}
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	op, _ = a.getOperation(op.ID)
	_ = a.executeOperation(op)
	return a.operationResult(op.ID)
}
func (a *App) executeOperation(op lifecycleOperation) error {
	if op.Status != "pending" {
		return nil
	}
	err := a.applyOperation(op)
	if err != nil {
		_, _ = a.ctx.AppDB().Exec(`UPDATE device_operations SET error=? WHERE id=? AND project_id=? AND status='pending'`, err.Error(), op.ID, a.projectID)
	}
	return err
}
func (a *App) ensureCredential(id, name, secret string) error {
	var users []struct {
		Username string `json:"username"`
	}
	if err := a.callMQTT("mqtt_users_list", map[string]any{}, &users); err != nil {
		return err
	}
	for _, u := range users {
		if u.Username == a.wireUsername(name) {
			return a.mqttRotatePassword(u.Username, secret)
		}
	}
	var result map[string]any
	base := a.topicRoot() + "devices/" + id
	err := a.callMQTT("mqtt_users_create", map[string]any{"username": name, "password": secret, "allow_publish": []string{base + "/response", base + "/telemetry", base + "/state", base + "/manifest", base + "/availability"}, "allow_subscribe": []string{base + "/commands", base + "/receipts"}}, &result)
	if err != nil {
		return err
	}
	if username := str(result, "username"); username != "" && username != a.wireUsername(name) {
		return errors.New("MQTT returned an unexpected credential identity")
	}
	return nil
}
func (a *App) applyOperation(op lifecycleOperation) error {
	id := op.DeviceID
	var d Device
	var err error
	secret := ""
	if op.secret != "" {
		secret, err = a.cryptSecret(op.ID, op.secret, false)
		if err != nil {
			return err
		}
	}
	if op.Kind != "provision" {
		d, err = getDevice(a.ctx.AppDB(), a.projectID, id, false)
		if err != nil {
			return err
		}
	}
	switch op.Kind {
	case "provision":
		err = a.ensureCredential(id, str(op.args, "credential_name"), secret)
	case "rotate":
		err = a.mqttRotatePassword(d.MQTTUsername, secret)
	case "enable":
		err = a.mqttSetEnabled(d.MQTTUsername, true)
	case "disable":
		err = a.mqttSetEnabled(d.MQTTUsername, false)
	case "delete":
		var users []struct {
			Username string `json:"username"`
		}
		err = a.callMQTT("mqtt_users_list", map[string]any{}, &users)
		if err == nil {
			for _, u := range users {
				if u.Username == d.MQTTUsername {
					err = a.mqttDeleteUser(d.MQTTUsername)
					break
				}
			}
		}
		if err == nil {
			for _, kind := range []string{"state", "manifest", "availability"} {
				var out map[string]any
				topic := a.topicRoot() + "devices/" + id + "/" + kind
				var retained []map[string]any
				err = a.callMQTT("mqtt_retained_list", map[string]any{"topic_pattern": topic, "limit": 1}, &retained)
				if err == nil && len(retained) > 0 {
					err = a.callMQTT("mqtt_retained_delete", map[string]any{"topic": topic}, &out)
				}
				if err != nil {
					break
				}
			}
		}
	default:
		return errors.New("unknown lifecycle operation")
	}
	if err != nil {
		return err
	}
	tx, err := a.ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	at := nowText()
	topic := "devices.device.status_changed"
	switch op.Kind {
	case "provision":
		args := op.args
		manifest, _ := json.Marshal(args["capabilities"])
		metadata, _ := json.Marshal(objectOrEmpty(args, "metadata"))
		_, err = tx.Exec(`INSERT INTO devices(id,project_id,name,protocol,model,manufacturer,mqtt_username,topic_root,manifest_json,metadata_json,created_at,updated_at)VALUES(?,?,?,?,?,?,?,?,?,?,?,?)`, id, a.projectID, str(args, "name"), str(args, "protocol"), str(args, "model"), str(args, "manufacturer"), a.wireUsername(str(args, "credential_name")), a.topicRoot(), string(manifest), string(metadata), at, at)
		topic = "devices.device.created"
	case "rotate":
		_, err = tx.Exec(`UPDATE devices SET credential_version=credential_version+1,status='offline',availability='offline',updated_at=? WHERE id=? AND project_id=?`, at, id, a.projectID)
		topic = "devices.device.updated"
	case "enable", "disable":
		enabled := op.Kind == "enable"
		status := "offline"
		if enabled {
			status = "provisioned"
		}
		_, err = tx.Exec(`UPDATE devices SET enabled=?,status=?,availability='unknown',mqtt_client_id='',updated_at=? WHERE id=? AND project_id=?`, boolInt(enabled), status, at, id, a.projectID)
	case "delete":
		_, err = tx.Exec(`INSERT INTO device_tombstones VALUES(?,?,?) ON CONFLICT DO NOTHING`, a.projectID, id, at)
		if err == nil {
			err = a.cancelDeviceCommands(tx, id, at)
		}
		for _, table := range []string{"device_state", "device_telemetry", "device_commands", "device_events", "device_messages"} {
			if err == nil {
				_, err = tx.Exec(`DELETE FROM `+table+` WHERE device_id=?`, id)
			}
		}
		if err == nil {
			_, err = tx.Exec(`DELETE FROM devices WHERE id=? AND project_id=?`, id, a.projectID)
		}
		topic = "devices.device.deleted"
	}
	if err != nil {
		return err
	}
	if op.Kind == "disable" {
		if err = a.cancelDeviceCommands(tx, id, at); err != nil {
			return err
		}
	}
	if err = a.event(tx, id, topic, map[string]any{"operation_id": op.ID, "action": op.Kind}); err != nil {
		return err
	}
	if err = a.usage(tx, id, "lifecycle."+op.Kind, op.ID, 1); err != nil {
		return err
	}
	expires := formatTime(time.Now().Add(time.Duration(a.limit("secret_recovery_hours", 24)) * time.Hour))
	_, err = tx.Exec(`UPDATE device_operations SET status='completed',error='',completed_at=?,secret_expires_at=? WHERE id=? AND project_id=?`, at, expires, op.ID, a.projectID)
	if err != nil {
		return err
	}
	if err = tx.Commit(); err != nil {
		return err
	}
	a.notifyTerminalWaiters()
	return nil
}
func (a *App) operationResult(id string) (map[string]any, error) {
	op, err := a.getOperation(id)
	if err != nil {
		return nil, err
	}
	result := map[string]any{"operation_id": id, "status": op.Status, "operation": op}
	if op.Status != "completed" {
		return result, nil
	}
	if op.Kind == "provision" || op.Kind == "rotate" {
		if op.secret == "" || op.expires == nil || *op.expires < nowText() {
			result["credential_recovery_expired"] = true
			result["recovery_action"] = "rotate credentials using a new idempotency key"
			return result, nil
		}
		secret, e := a.cryptSecret(op.ID, op.secret, false)
		if e != nil {
			return nil, e
		}
		d, e := getDevice(a.ctx.AppDB(), a.projectID, op.DeviceID, false)
		if e != nil {
			if !errors.Is(e, ErrNotFound) {
				return nil, e
			}
			result["resource_deleted"] = true
			return result, nil
		}
		var newer int
		e = a.ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM device_operations WHERE project_id=? AND device_id=? AND kind='rotate' AND rowid>(SELECT rowid FROM device_operations WHERE id=?)`, a.projectID, op.DeviceID, id).Scan(&newer)
		if e != nil {
			return nil, e
		}
		if newer > 0 {
			result["credential_superseded"] = true
			return result, nil
		}
		if op.Kind == "provision" {
			for k, v := range a.enrollmentResponse(d, secret) {
				result[k] = v
			}
		} else {
			result["device_id"] = d.ID
			result["username"] = d.MQTTUsername
			result["password"] = secret
			result["credential_version"] = d.CredentialVersion
		}
		result["password_shown_once"] = false
		result["secret_recoverable_until"] = op.expires
	}
	return result, nil
}
func (a *App) reconcileOperations() error {
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	rows, err := a.ctx.AppDB().Query(`SELECT id FROM device_operations WHERE project_id=? AND status='pending' ORDER BY created_at LIMIT 100`, a.projectID)
	if err != nil {
		return err
	}
	ids := []string{}
	for rows.Next() {
		var id string
		if err = rows.Scan(&id); err != nil {
			rows.Close()
			return err
		}
		ids = append(ids, id)
	}
	rows.Close()
	for _, id := range ids {
		op, e := a.getOperation(id)
		if e != nil {
			return e
		}
		_ = a.executeOperation(op)
	}
	_, err = a.ctx.AppDB().Exec(`UPDATE device_operations SET secret_cipher='' WHERE project_id=? AND status='completed' AND secret_expires_at<? AND secret_cipher<>''`, a.projectID, nowText())
	return err
}
