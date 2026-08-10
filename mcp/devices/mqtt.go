package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) callMQTT(tool string, input map[string]any, out any) error {
	if a.ctx == nil || a.ctx.PlatformAPI() == nil {
		return errors.New("MQTT platform client unavailable")
	}
	return a.ctx.PlatformAPI().CallAppResult("mqtt", tool, input, out)
}

func (a *App) refreshBrokerStatus() error {
	var status brokerStatus
	if err := a.callMQTT("mqtt_status", map[string]any{}, &status); err != nil {
		return err
	}
	if strings.TrimSpace(status.Endpoint) == "" {
		return errors.New("MQTT broker did not advertise a client endpoint")
	}
	a.setBroker(status)
	return nil
}

func (a *App) mqttCreateUser(username, password string) error {
	var out map[string]any
	base := "devices/" + username
	return a.callMQTT("mqtt_users_create", map[string]any{
		"username": username,
		"password": password,
		"allow_publish": []string{
			base + "/response", base + "/telemetry", base + "/state",
			base + "/manifest", base + "/availability",
		},
		"allow_subscribe": []string{base + "/commands"},
	}, &out)
}

func (a *App) mqttDeleteUser(username string) error {
	var out map[string]any
	return a.callMQTT("mqtt_users_delete", map[string]any{"username": username}, &out)
}

func (a *App) mqttSetEnabled(username string, enabled bool) error {
	var out map[string]any
	return a.callMQTT("mqtt_users_set_enabled", map[string]any{"username": username, "enabled": enabled}, &out)
}

func (a *App) mqttRotatePassword(username, password string) error {
	var out map[string]any
	return a.callMQTT("mqtt_users_rotate_password", map[string]any{"username": username, "password": password}, &out)
}

func (a *App) mqttPublish(topic string, payload any, retain bool, qos int) error {
	var out map[string]any
	return a.callMQTT("mqtt_publish", map[string]any{
		"topic": topic, "payload": payload, "retain": retain, "qos": qos,
	}, &out)
}

func (a *App) reconcileConnectedClients() error {
	var clients []map[string]any
	if err := a.callMQTT("mqtt_clients_list", map[string]any{}, &clients); err != nil {
		return err
	}
	at := nowText()
	for _, client := range clients {
		username, _ := client["username"].(string)
		clientID, _ := client["client_id"].(string)
		if username == "" {
			continue
		}
		_, _, _ = setDeviceConnection(a.ctx.AppDB(), a.projectID, username, clientID, true, at)
	}
	return nil
}

func (a *App) reconcileRetained() error {
	var retained []struct {
		Topic   string `json:"topic"`
		Payload any    `json:"payload"`
		Updated string `json:"updated_at"`
	}
	if err := a.callMQTT("mqtt_retained_list", map[string]any{"topic_pattern": "devices/#", "limit": 1000}, &retained); err != nil {
		return err
	}
	for _, item := range retained {
		payload, ok := item.Payload.(string)
		if !ok {
			continue
		}
		parts, ok := parseDeviceTopic(item.Topic)
		if !ok {
			continue
		}
		d, err := getDevice(a.ctx.AppDB(), a.projectID, parts.deviceID, false)
		if err != nil {
			continue
		}
		at := item.Updated
		if at == "" {
			at = nowText()
		}
		_ = a.processDevicePayload(d, parts.kind, payload, at)
	}
	return nil
}

type deviceTopic struct {
	deviceID string
	kind     string
}

func parseDeviceTopic(topic string) (deviceTopic, bool) {
	parts := strings.Split(topic, "/")
	if len(parts) != 3 || parts[0] != "devices" || !validDeviceID(parts[1]) {
		return deviceTopic{}, false
	}
	switch parts[2] {
	case "response", "telemetry", "state", "manifest", "availability":
		return deviceTopic{deviceID: parts[1], kind: parts[2]}, true
	default:
		return deviceTopic{}, false
	}
}

func (a *App) handleMQTTMessage(_ *sdk.AppCtx, event sdk.Event) error {
	if event.ProjectID != "" && event.ProjectID != a.projectID {
		return nil
	}
	topic, _ := event.Data["topic"].(string)
	parts, ok := parseDeviceTopic(topic)
	if !ok {
		return nil
	}
	username, _ := event.Data["username"].(string)
	if username == "" || username != parts.deviceID {
		insertEvent(a.ctx.AppDB(), parts.deviceID, "security.topic_rejected", map[string]any{"topic": topic, "username": username})
		return nil
	}
	d, err := getDevice(a.ctx.AppDB(), a.projectID, parts.deviceID, false)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) || strings.Contains(err.Error(), "not found") {
			return nil
		}
		return err
	}
	if !d.Enabled || d.MQTTUsername != username {
		insertEvent(a.ctx.AppDB(), d.ID, "security.identity_rejected", map[string]any{"topic": topic, "username": username})
		return nil
	}
	payload, ok := event.Data["payload"].(string)
	if !ok {
		insertEvent(a.ctx.AppDB(), d.ID, "payload.rejected", map[string]any{"topic": topic, "reason": "binary or truncated payload"})
		return nil
	}
	if len(payload) > 131072 {
		return errors.New("device payload exceeds 128 KiB")
	}
	if err := a.processDevicePayload(d, parts.kind, payload, nowText()); err != nil {
		insertEvent(a.ctx.AppDB(), d.ID, "payload.rejected", map[string]any{"topic": topic, "reason": err.Error()})
		a.ctx.Logger().Warn("devices reject MQTT payload", "device_id", d.ID, "topic", topic, "err", err.Error())
	}
	return nil
}

func (a *App) processDevicePayload(d Device, kind, payload, at string) error {
	if err := touchDevice(a.ctx.AppDB(), a.projectID, d.ID, at); err != nil {
		return err
	}
	switch kind {
	case "availability":
		availability := parseAvailability(payload)
		changed, err := setAvailability(a.ctx.AppDB(), a.projectID, d.ID, availability, at)
		if err != nil {
			return err
		}
		if changed {
			a.ctx.Emit("devices.device.status_changed", map[string]any{"device_id": d.ID, "status": availability})
		}
		insertEvent(a.ctx.AppDB(), d.ID, "availability", map[string]any{"status": availability})
		return nil
	case "manifest":
		var manifest map[string]any
		if err := json.Unmarshal([]byte(payload), &manifest); err != nil {
			return fmt.Errorf("invalid device manifest: %w", err)
		}
		if err := validateManifest(manifest); err != nil {
			return err
		}
		if err := saveManifest(a.ctx.AppDB(), a.projectID, d.ID, manifest, at); err != nil {
			return err
		}
		a.ctx.Emit("devices.device.updated", map[string]any{"device_id": d.ID, "changed": "manifest"})
		return nil
	case "response":
		return a.processResponse(d, payload, at)
	case "state", "telemetry":
		var values map[string]any
		if err := json.Unmarshal([]byte(payload), &values); err != nil {
			return fmt.Errorf("invalid %s payload: %w", kind, err)
		}
		if variables, ok := values["variables"].(map[string]any); ok {
			values = variables
		}
		values = withoutEnvelopeFields(values)
		changed, err := upsertState(a.ctx.AppDB(), d.ID, kind, values, manifestUnits(d.Manifest), at)
		if err != nil {
			return err
		}
		if len(changed) > 0 {
			a.ctx.Emit("devices.state.changed", map[string]any{"device_id": d.ID, "keys": changed, "source": kind})
		}
		if kind == "telemetry" {
			if err := insertTelemetry(a.ctx.AppDB(), d.ID, values, at); err != nil {
				return err
			}
			a.ctx.Emit("devices.telemetry.received", map[string]any{"device_id": d.ID, "keys": mapKeys(values), "received_at": at})
		}
	}
	return nil
}

func (a *App) processResponse(d Device, payload, at string) error {
	var response map[string]any
	if err := json.Unmarshal([]byte(payload), &response); err != nil {
		return fmt.Errorf("invalid command response: %w", err)
	}
	id := firstString(response, "command_id", "id")
	if id == "" {
		return errors.New("command response missing command_id")
	}
	success := false
	if value, ok := response["success"].(bool); ok {
		success = value
	} else {
		status := strings.ToLower(firstString(response, "status"))
		success = status == "ok" || status == "success" || status == "succeeded"
	}
	result := response["result"]
	message := firstString(response, "error", "message")
	if obj, ok := result.(map[string]any); ok && message == "" && !success {
		message = firstString(obj, "error", "message")
	}
	command, changed, err := completeCommand(a.ctx.AppDB(), d.ID, id, success, result, message, at)
	if err != nil || !changed {
		return err
	}
	if success && command.Operation == "device.info" {
		if obj, ok := result.(map[string]any); ok {
			manifest := manifestFromInfo(d, obj)
			_ = saveManifest(a.ctx.AppDB(), a.projectID, d.ID, manifest, at)
			a.ctx.Emit("devices.device.updated", map[string]any{"device_id": d.ID, "changed": "capabilities"})
		}
	}
	eventName := "devices.command.failed"
	if success {
		eventName = "devices.command.completed"
	}
	a.ctx.Emit(eventName, map[string]any{"command_id": id, "device_id": d.ID, "operation": command.Operation, "status": command.Status})
	insertEvent(a.ctx.AppDB(), d.ID, "command."+command.Status, map[string]any{"command_id": id, "operation": command.Operation})
	a.notifyWaiters(id)
	return nil
}

func (a *App) handleMQTTConnected(_ *sdk.AppCtx, event sdk.Event) error {
	if event.ProjectID != "" && event.ProjectID != a.projectID {
		return nil
	}
	username, _ := event.Data["username"].(string)
	at := firstString(event.Data, "connected_at")
	if at == "" {
		at = nowText()
	}
	clientID, _ := event.Data["client_id"].(string)
	d, changed, err := setDeviceConnection(a.ctx.AppDB(), a.projectID, username, clientID, true, at)
	if err != nil || d.ID == "" {
		return err
	}
	a.ctx.Emit("devices.device.connected", map[string]any{"device_id": d.ID, "client_id": event.Data["client_id"], "connected_at": at})
	if changed {
		a.ctx.Emit("devices.device.status_changed", map[string]any{"device_id": d.ID, "status": "online"})
	}
	insertEvent(a.ctx.AppDB(), d.ID, "connected", event.Data)
	// Clients without a retained manifest can report their identity and
	// capabilities after authentication.
	if needsDeviceInfo(d.Manifest) {
		_, _ = a.sendCommand(d, "device.info", "", map[string]any{}, false, 10000, "")
	}
	return nil
}

func (a *App) handleMQTTDisconnected(_ *sdk.AppCtx, event sdk.Event) error {
	if event.ProjectID != "" && event.ProjectID != a.projectID {
		return nil
	}
	username, _ := event.Data["username"].(string)
	at := firstString(event.Data, "disconnected_at")
	if at == "" {
		at = nowText()
	}
	clientID, _ := event.Data["client_id"].(string)
	current, lookupErr := getDeviceByUsername(a.ctx.AppDB(), a.projectID, username)
	if lookupErr == nil && current.MQTTClientID != "" && clientID != "" && current.MQTTClientID != clientID {
		return nil
	}
	d, changed, err := setDeviceConnection(a.ctx.AppDB(), a.projectID, username, clientID, false, at)
	if err != nil || d.ID == "" {
		return err
	}
	a.ctx.Emit("devices.device.disconnected", map[string]any{"device_id": d.ID, "client_id": event.Data["client_id"], "reason": event.Data["reason"], "disconnected_at": at})
	if changed {
		a.ctx.Emit("devices.device.status_changed", map[string]any{"device_id": d.ID, "status": "offline"})
	}
	insertEvent(a.ctx.AppDB(), d.ID, "disconnected", event.Data)
	return nil
}

func parseAvailability(payload string) string {
	text := strings.ToLower(strings.TrimSpace(payload))
	if text == "online" || text == "1" || text == "true" {
		return "online"
	}
	var scalar string
	if json.Unmarshal([]byte(payload), &scalar) == nil && strings.EqualFold(strings.TrimSpace(scalar), "online") {
		return "online"
	}
	var obj map[string]any
	if json.Unmarshal([]byte(payload), &obj) == nil {
		text = strings.ToLower(firstString(obj, "status", "availability"))
		if text == "online" || text == "connected" {
			return "online"
		}
	}
	return "offline"
}

func needsDeviceInfo(manifest map[string]any) bool {
	for _, key := range []string{"variables", "functions"} {
		if items, ok := manifest[key].([]any); ok && len(items) > 0 {
			return false
		}
	}
	return true
}

func withoutEnvelopeFields(values map[string]any) map[string]any {
	out := make(map[string]any, len(values))
	for key, value := range values {
		switch key {
		case "device_id", "id", "name", "connected", "timestamp", "ts", "uptime":
			continue
		default:
			out[key] = value
		}
	}
	return out
}

func mapKeys(values map[string]any) []string {
	out := make([]string, 0, len(values))
	for key := range values {
		out = append(out, key)
	}
	sort.Strings(out)
	return out
}

func firstString(values map[string]any, keys ...string) string {
	for _, key := range keys {
		if text, ok := values[key].(string); ok && strings.TrimSpace(text) != "" {
			return strings.TrimSpace(text)
		}
	}
	return ""
}

func manifestUnits(manifest map[string]any) map[string]string {
	out := map[string]string{}
	for _, key := range []string{"variables", "sensors"} {
		items, _ := manifest[key].([]any)
		for _, item := range items {
			obj, _ := item.(map[string]any)
			name, unit := firstString(obj, "name"), firstString(obj, "unit", "unit_of_measurement")
			if name != "" {
				out[name] = unit
			}
		}
	}
	return out
}

func manifestFromInfo(d Device, info map[string]any) map[string]any {
	manifest := map[string]any{
		"protocol": d.Protocol, "name": d.Name, "model": d.Model,
		"manufacturer": d.Manufacturer, "firmware": d.Firmware,
	}
	if name := firstString(info, "name"); name != "" {
		manifest["name"] = name
	}
	if hardware := firstString(info, "hardware", "model"); hardware != "" {
		manifest["model"] = hardware
	}
	variables := []any{}
	if values, ok := info["variables"].(map[string]any); ok {
		for name, value := range values {
			variables = append(variables, map[string]any{"name": name, "type": valueType(value), "readable": true})
		}
	}
	manifest["variables"] = variables
	functions := []any{}
	if names, ok := info["functions"].([]any); ok {
		for _, name := range names {
			if text, ok := name.(string); ok {
				functions = append(functions, map[string]any{"name": text})
			}
		}
	}
	manifest["functions"] = functions
	if pins, ok := d.Manifest["pins"]; ok {
		manifest["pins"] = pins
	}
	return manifest
}

func validateManifest(manifest map[string]any) error {
	raw, err := json.Marshal(manifest)
	if err != nil {
		return fmt.Errorf("encode device manifest: %w", err)
	}
	if len(raw) > 131072 {
		return errors.New("device manifest exceeds 128 KiB")
	}
	protocol := firstString(manifest, "protocol")
	if protocol != "" && protocol != "apteva.devices/v1" {
		return fmt.Errorf("unsupported device protocol %q", protocol)
	}
	for _, key := range []string{"variables", "functions", "pins"} {
		if value, ok := manifest[key]; ok {
			items, ok := value.([]any)
			if !ok {
				return fmt.Errorf("manifest %s must be an array", key)
			}
			if len(items) > 256 {
				return fmt.Errorf("manifest %s exceeds 256 entries", key)
			}
		}
	}
	seenPins := map[string]bool{}
	if pins, ok := manifest["pins"].([]any); ok {
		for i, item := range pins {
			pin, ok := item.(map[string]any)
			if !ok {
				return fmt.Errorf("manifest pins[%d] must be an object", i)
			}
			number, name := pinText(pin["number"]), firstString(pin, "name")
			if number == "" {
				return fmt.Errorf("manifest pins[%d].number is required", i)
			}
			if seenPins[number] || name != "" && seenPins[name] {
				return fmt.Errorf("manifest pin %q is duplicated", firstNonEmpty(name, number))
			}
			seenPins[number] = true
			if name != "" {
				seenPins[name] = true
			}
			kind := strings.ToLower(firstString(pin, "type", "kind"))
			if kind != "" && kind != "digital" && kind != "analog" && kind != "pwm" {
				return fmt.Errorf("manifest pin %s has unsupported type %q", number, kind)
			}
			for _, mode := range stringSlice(pin["modes"]) {
				if mode != "input" && mode != "input_pullup" && mode != "output" {
					return fmt.Errorf("manifest pin %s has unsupported mode %q", number, mode)
				}
			}
		}
	}
	return nil
}
