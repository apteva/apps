package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

func (a *App) sendCommand(d Device, operation, target string, arguments map[string]any, wait bool, timeoutMS int, key string) (Command, error) {
	a.mutationMu.Lock()
	c, err := a.submitCommand(d, operation, target, arguments, timeoutMS, key)
	a.mutationMu.Unlock()
	if err != nil {
		return c, err
	}
	a.wakeOutbox()
	c, err = getCommand(a.ctx.AppDB(), c.ID)
	if err != nil {
		return c, err
	}
	return a.waitForExistingCommand(c, wait)
}
func (a *App) submitCommand(d Device, operation, target string, arguments map[string]any, timeoutMS int, key string) (Command, error) {
	current, err := getDevice(a.ctx.AppDB(), a.projectID, d.ID, false)
	if err != nil {
		return Command{}, err
	}
	d = current
	// Retry identity describes caller input, before defaults, schema changes,
	// or native adapters normalize arguments. Replays survive policy changes.
	originalArgs, err := json.Marshal(arguments)
	if err != nil {
		return Command{}, err
	}
	if len(key) > 128 {
		return Command{}, errors.New("idempotency_key exceeds 128 characters")
	}
	hash := fingerprint(map[string]any{"operation": operation, "target": target, "arguments": json.RawMessage(originalArgs), "timeout_ms": timeoutMS})
	if key != "" {
		var id, oldHash string
		e := a.ctx.AppDB().QueryRow(`SELECT id,fingerprint FROM device_commands WHERE device_id=? AND idempotency_key=?`, d.ID, key).Scan(&id, &oldHash)
		if e == nil {
			if oldHash != hash {
				return Command{}, ErrConflict
			}
			return getCommand(a.ctx.AppDB(), id)
		}
		if !errors.Is(e, sql.ErrNoRows) {
			return Command{}, e
		}
	}
	if timeoutMS == 0 {
		timeoutMS = a.limit("command_timeout_ms", 10000)
	}
	if timeoutMS < 500 || timeoutMS > 86400000 {
		return Command{}, errors.New("timeout_ms must be between 500 and 86400000")
	}
	adapter, err := adapterFor(d.Protocol)
	if err != nil {
		return Command{}, err
	}
	if err = adapter.Validate(d, operation, target, arguments, key); err != nil {
		return Command{}, err
	}
	raw, err := json.Marshal(arguments)
	if err != nil {
		return Command{}, err
	}
	if len(raw) > a.limit("max_device_payload_bytes", 131072) {
		return Command{}, ErrLimit
	}
	tx, err := a.ctx.AppDB().Begin()
	if err != nil {
		return Command{}, err
	}
	defer tx.Rollback()

	if !d.Enabled {
		return Command{}, errors.New("device is disabled")
	}
	var scopeActive, deviceActive int
	if err = tx.QueryRow(`SELECT COUNT(*) FROM device_commands WHERE status IN ('queued','sent') AND device_id IN (SELECT id FROM devices WHERE project_id=?)`, a.projectID).Scan(&scopeActive); err != nil {
		return Command{}, err
	}
	if err = tx.QueryRow(`SELECT COUNT(*) FROM device_commands WHERE status IN ('queued','sent') AND device_id=?`, d.ID).Scan(&deviceActive); err != nil {
		return Command{}, err
	}
	if scopeActive >= a.limit("max_pending_commands", 10000) || deviceActive >= a.limit("max_pending_commands_per_device", 100) {
		return Command{}, ErrLimit
	}
	now := time.Now()
	id := newID("cmd_")
	deadline := formatTime(now.Add(time.Duration(timeoutMS) * time.Millisecond))
	request := adapter.Encode(id, operation, target, arguments, timeoutMS)
	request["expires_at"] = deadline
	request["expires_unix_ms"] = now.Add(time.Duration(timeoutMS) * time.Millisecond).UnixMilli()
	c := Command{ID: id, DeviceID: d.ID, Operation: operation, Target: target, Arguments: arguments, Request: request, Status: "queued", IdempotencyKey: key, TimeoutMS: timeoutMS, CreatedAt: formatTime(now), DeadlineAt: deadline}
	wire, _ := json.Marshal(request)
	outputSchema := map[string]any{}
	if d.Protocol == "apteva.devices/v2" {
		ops, _ := d.Manifest["operations"].([]any)
		for _, v := range ops {
			op, _ := v.(map[string]any)
			if str(op, "name") == operation {
				if schema, ok := op["output_schema"].(map[string]any); ok {
					outputSchema = schema
				}
			}
		}
	}
	outputRaw, _ := json.Marshal(outputSchema)
	_, err = tx.Exec(`INSERT INTO device_commands(id,device_id,operation,target,arguments_json,request_json,status,idempotency_key,timeout_ms,created_at,deadline_at,fingerprint,output_schema_json)VALUES(?,?,?,?,?,?,'queued',?,?,?,?,?,?)`, id, d.ID, operation, target, string(raw), string(wire), nullableString(key), timeoutMS, c.CreatedAt, deadline, hash, string(outputRaw))
	if err != nil {
		return c, err
	}
	if err = a.event(tx, d.ID, "devices.command.submitted", map[string]any{"command_id": id, "operation": operation, "deadline_at": deadline}); err != nil {
		return c, err
	}
	if err = a.usage(tx, d.ID, "commands.submitted", id, 1); err != nil {
		return c, err
	}
	return c, tx.Commit()
}
func (a *App) dispatchCommands() error {
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	if _, err := a.expireCommandsLocked(time.Now()); err != nil {
		return err
	}
	rows, err := a.ctx.AppDB().Query(`SELECT id FROM device_commands WHERE device_id IN (SELECT id FROM devices WHERE project_id=? AND enabled=1) AND status IN ('queued','sent') AND (next_attempt_at IS NULL OR next_attempt_at<=?) ORDER BY created_at,id LIMIT 100`, a.projectID, nowText())
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
		c, e := getCommand(a.ctx.AppDB(), id)
		if e != nil {
			return e
		}
		if c.DeadlineAt <= nowText() {
			continue
		}
		if c.Status == "sent" {
			d, e := getDevice(a.ctx.AppDB(), a.projectID, c.DeviceID, false)
			if e != nil {
				return e
			}
			if d.Protocol == "apteva.devices/v1" && str(d.Manifest, "command_contract") != "apteva.commands/v1" {
				continue
			}
		}

		if c.Status == "queued" {
			var active, deviceActive int
			if e = a.ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM device_commands WHERE status='sent' AND device_id IN (SELECT id FROM devices WHERE project_id=?)`, a.projectID).Scan(&active); e != nil {
				return e
			}
			if e = a.ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM device_commands WHERE status='sent' AND device_id=?`, c.DeviceID).Scan(&deviceActive); e != nil {
				return e
			}
			if active >= a.limit("max_concurrent_commands", 100) || deviceActive >= a.limit("max_concurrent_commands_per_device", 1) {
				continue
			}
		}
		// Claim before publishing; a crash or ambiguous response retries this same
		// command ID. Device clients must durably suppress repeated execution.
		next := formatTime(time.Now().Add(5 * time.Second))
		if _, e = a.ctx.AppDB().Exec(`UPDATE device_commands SET status='sent',sent_at=COALESCE(sent_at,?),next_attempt_at=?,attempts=attempts+1 WHERE id=? AND status IN ('queued','sent')`, nowText(), next, id); e != nil {
			return e
		}
		e = a.mqttPublish(a.topicRoot()+"devices/"+c.DeviceID+"/commands", c.Request, false, 1)
		if e != nil {
			_, err = a.ctx.AppDB().Exec(`UPDATE device_commands SET error=? WHERE id=? AND status='sent'`, e.Error(), id)
			if err != nil {
				return err
			}
		}
	}
	return nil
}
func (a *App) expireCommands(now time.Time) ([]string, error) {
	a.mutationMu.Lock()
	defer a.mutationMu.Unlock()
	return a.expireCommandsLocked(now)
}
func (a *App) expireCommandsLocked(now time.Time) ([]string, error) {
	tx, err := a.ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	rows, err := tx.Query(`SELECT id,device_id FROM device_commands WHERE status IN ('queued','sent') AND deadline_at<=? AND device_id IN(SELECT id FROM devices WHERE project_id=?)`, formatTime(now), a.projectID)
	if err != nil {
		return nil, err
	}
	type item struct{ id, device string }
	items := []item{}
	for rows.Next() {
		var x item
		if err = rows.Scan(&x.id, &x.device); err != nil {
			rows.Close()
			return nil, err
		}
		items = append(items, x)
	}
	rows.Close()
	ids := []string{}
	for _, x := range items {
		if _, err = tx.Exec(`UPDATE device_commands SET status='timed_out',error='execution unconfirmed: response deadline passed',completed_at=? WHERE id=? AND status IN ('queued','sent')`, formatTime(now), x.id); err != nil {
			return nil, err
		}
		if err = a.commandTerminalEvent(tx, x.device, x.id, "timed_out"); err != nil {
			return nil, err
		}
		ids = append(ids, x.id)
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	for _, id := range ids {
		a.notifyWaiters(id)
	}
	return ids, nil
}
func (a *App) commandTerminalEvent(tx *sql.Tx, device, id, status string) error {
	topic := "devices.command." + status
	if status == "succeeded" {
		topic = "devices.command.completed"
	}
	if err := a.event(tx, device, topic, map[string]any{"command_id": id, "status": status}); err != nil {
		return err
	}
	return a.usage(tx, device, "commands."+status, id, 1)
}
func (a *App) cancelDeviceCommands(tx *sql.Tx, device, at string) error {
	rows, err := tx.Query(`SELECT id FROM device_commands WHERE device_id=? AND status IN ('queued','sent')`, device)
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
		if _, err = tx.Exec(`UPDATE device_commands SET status='cancelled',error='device disabled or deleted; execution unconfirmed',completed_at=? WHERE id=?`, at, id); err != nil {
			return err
		}
		if err = a.commandTerminalEvent(tx, device, id, "cancelled"); err != nil {
			return err
		}
	}
	return nil
}
func (a *App) processResponse(d Device, payload, at string) error {
	a.mutationMu.Lock()
	err := a.responseLocked(d, payload, at)
	a.mutationMu.Unlock()
	if err == nil {
		a.wakeOutbox()
	}
	return err
}
func (a *App) responseLocked(d Device, payload, at string) error {
	current, err := getDevice(a.ctx.AppDB(), a.projectID, d.ID, false)
	if err != nil {
		return err
	}
	if !current.Enabled {
		return ErrDeviceAuth
	}
	var response map[string]any
	if err = decodeJSON(payload, &response); err != nil {
		return invalidData(err)
	}
	id := firstString(response, "command_id", "id")
	if id == "" {
		return invalidData(errors.New("command_id required"))
	}
	success, valid := response["success"].(bool)
	if !valid {
		switch str(response, "status") {
		case "ok", "success", "succeeded":
			success = true
		case "failed", "error":
			success = false
		default:
			return invalidData(errors.New("terminal response status required"))
		}
	}
	c, err := getCommand(a.ctx.AppDB(), id)
	if err != nil {
		return err
	}
	if c.DeviceID != d.ID {
		return invalidData(errors.New("command does not belong to device"))
	}
	tx, err := a.ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	disposition := "accepted"
	if commandTerminal(c.Status) || c.DeadlineAt <= at {
		disposition = "late"
	}
	responseHash := fingerprint(response)
	result, err := tx.Exec(`INSERT INTO device_responses(project_id,device_id,command_id,fingerprint,response_json,disposition,created_at)VALUES(?,?,?,?,?,?,?) ON CONFLICT DO NOTHING`, a.projectID, d.ID, id, responseHash, payload, disposition, at)
	if err != nil {
		return err
	}
	inserted, _ := result.RowsAffected()
	if inserted == 0 {
		return a.commitIngestion(tx, d.ID)
	}
	if commandTerminal(c.Status) {
		if err = a.event(tx, d.ID, "devices.command.late_response", map[string]any{"command_id": id, "response": response}); err != nil {
			return err
		}
		return a.commitIngestion(tx, d.ID)
	}
	status := "failed"
	if success {
		status = "succeeded"
	}
	message := firstString(response, "error", "message")
	if c.DeadlineAt <= at {
		status = "timed_out"
		message = "execution unconfirmed: response arrived after deadline"
	}
	if success && status == "succeeded" && d.Protocol == "apteva.devices/v2" {
		var schemaRaw string
		if err = tx.QueryRow(`SELECT output_schema_json FROM device_commands WHERE id=?`, id).Scan(&schemaRaw); err != nil {
			return err
		}
		schema := jsonObject(schemaRaw)
		if len(schema) > 0 {
			if err = validateJSONSchema(schema, response["result"]); err != nil {
				return invalidData(fmt.Errorf("invalid command result: %w", err))
			}
		}
	}

	raw, _ := json.Marshal(response["result"])
	_, err = tx.Exec(`UPDATE device_commands SET status=?,result_json=?,error=?,completed_at=? WHERE id=? AND device_id=? AND status IN ('queued','sent')`, status, string(raw), message, at, id, d.ID)
	if err != nil {
		return err
	}
	if err = a.commandTerminalEvent(tx, d.ID, id, status); err != nil {
		return err
	}
	if status == "timed_out" {
		if err = a.event(tx, d.ID, "devices.command.late_response", map[string]any{"command_id": id, "response": response}); err != nil {
			return err
		}
	}
	if err = a.commitIngestion(tx, d.ID); err != nil {
		return err
	}
	a.notifyWaiters(id)
	return nil
}
