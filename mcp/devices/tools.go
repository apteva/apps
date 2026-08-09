package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/google/uuid"
)

var deviceIDPattern = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,63}$`)
var operationPattern = regexp.MustCompile(`^[a-z][a-z0-9_.-]{0,63}$`)

func (a *App) mcpTools() []sdk.Tool {
	return []sdk.Tool{
		tool("devices_list", "List managed devices.", schema(map[string]any{"status": stringProp(), "q": stringProp(), "limit": intProp()}, nil), a.toolList),
		tool("devices_get", "Get one device with capabilities and current state.", schema(map[string]any{"device_id": stringProp()}, []string{"device_id"}), a.toolGet),
		tool("devices_provision", "Provision a device and return its one-time MQTT credentials and setup details.", schema(map[string]any{
			"name": stringProp(), "device_id": stringProp(), "protocol": stringProp(), "model": stringProp(),
			"manufacturer": stringProp(), "metadata": objectProp(), "capabilities": objectProp(),
		}, []string{"name"}), a.toolProvision),
		tool("devices_update", "Update device metadata.", schema(map[string]any{"device_id": stringProp(), "patch": objectProp()}, []string{"device_id", "patch"}), a.toolUpdate),
		tool("devices_enable", "Enable a device credential.", deviceOnlySchema(), a.toolEnable),
		tool("devices_disable", "Disable and disconnect a device credential.", deviceOnlySchema(), a.toolDisable),
		tool("devices_rotate_secret", "Rotate a device MQTT password and return it once.", deviceOnlySchema(), a.toolRotateSecret),
		tool("devices_delete", "Delete a device and MQTT credential.", schema(map[string]any{"device_id": stringProp(), "confirm": boolProp()}, []string{"device_id", "confirm"}), a.toolDelete),
		tool("devices_state_get", "Get current device state.", schema(map[string]any{"device_id": stringProp(), "key": stringProp()}, []string{"device_id"}), a.toolStateGet),
		tool("devices_capabilities_set", "Set the allowlisted variables, functions, and pins for a device.", schema(map[string]any{"device_id": stringProp(), "manifest": objectProp()}, []string{"device_id", "manifest"}), a.toolCapabilitiesSet),
		tool("devices_capabilities_refresh", "Ask a device to report identity and capabilities (aREST get_info compatible).", commandHelperSchema(map[string]any{}, nil), a.toolCapabilitiesRefresh),
		tool("devices_variable_get", "Read an exposed firmware variable.", commandHelperSchema(map[string]any{"name": stringProp()}, []string{"name"}), a.toolVariableGet),
		tool("devices_function_call", "Call an exposed firmware function.", commandHelperSchema(map[string]any{"name": stringProp(), "arguments": objectProp()}, []string{"name"}), a.toolFunctionCall),
		tool("devices_pin_read", "Read an allowlisted digital or analog pin.", commandHelperSchema(map[string]any{"pin": map[string]any{}, "kind": stringProp()}, []string{"pin"}), a.toolPinRead),
		tool("devices_pin_write", "Write an allowlisted digital or PWM pin.", commandHelperSchema(map[string]any{"pin": map[string]any{}, "value": map[string]any{}, "kind": stringProp()}, []string{"pin", "value"}), a.toolPinWrite),
		tool("devices_pin_mode", "Change an allowlisted pin mode on a native device client.", commandHelperSchema(map[string]any{"pin": map[string]any{}, "mode": map[string]any{"type": "string", "enum": []string{"input", "input_pullup", "output"}}}, []string{"pin", "mode"}), a.toolPinMode),
		tool("devices_command_send", "Send a correlated structured command.", commandHelperSchema(map[string]any{"operation": stringProp(), "target": stringProp(), "arguments": objectProp()}, []string{"operation"}), a.toolCommandSend),
		tool("devices_command_get", "Get one command and its response.", schema(map[string]any{"command_id": stringProp()}, []string{"command_id"}), a.toolCommandGet),
		tool("devices_commands_list", "List recent commands.", schema(map[string]any{"device_id": stringProp(), "status": stringProp(), "limit": intProp()}, nil), a.toolCommandsList),
	}
}

func tool(name, description string, input map[string]any, handler sdk.ToolHandler) sdk.Tool {
	return sdk.Tool{Name: name, Description: description, InputSchema: input, Handler: handler}
}

func schema(properties map[string]any, required []string) map[string]any {
	out := map[string]any{"type": "object", "properties": properties, "additionalProperties": false}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func stringProp() map[string]any { return map[string]any{"type": "string"} }
func boolProp() map[string]any   { return map[string]any{"type": "boolean"} }
func intProp() map[string]any    { return map[string]any{"type": "integer"} }
func objectProp() map[string]any { return map[string]any{"type": "object"} }

func deviceOnlySchema() map[string]any {
	return schema(map[string]any{"device_id": stringProp()}, []string{"device_id"})
}

func commandHelperSchema(extra map[string]any, required []string) map[string]any {
	props := map[string]any{
		"device_id": stringProp(), "wait": boolProp(), "timeout_ms": intProp(), "idempotency_key": stringProp(),
	}
	for key, value := range extra {
		props[key] = value
	}
	return schema(props, append([]string{"device_id"}, required...))
}

func (a *App) toolList(_ *sdk.AppCtx, args map[string]any) (any, error) {
	return listDevices(a.ctx.AppDB(), a.projectID, str(args, "status"), str(args, "q"), integer(args, "limit", 100))
}

func (a *App) toolGet(_ *sdk.AppCtx, args map[string]any) (any, error) {
	return getDevice(a.ctx.AppDB(), a.projectID, str(args, "device_id"), true)
}

func (a *App) toolProvision(_ *sdk.AppCtx, args map[string]any) (any, error) {
	return a.provision(args)
}

func (a *App) provision(args map[string]any) (map[string]any, error) {
	name := strings.TrimSpace(str(args, "name"))
	if name == "" {
		return nil, errors.New("name required")
	}
	if len(name) > 128 {
		return nil, errors.New("name must be 128 characters or fewer")
	}
	id := strings.TrimSpace(str(args, "device_id"))
	if id == "" {
		id = slugDeviceID(name)
	}
	if !validDeviceID(id) {
		return nil, errors.New("device_id must match [a-z0-9][a-z0-9_-]{0,63}")
	}
	if _, err := getDevice(a.ctx.AppDB(), a.projectID, id, false); err == nil {
		return nil, fmt.Errorf("device %q already exists", id)
	}
	protocol := strings.TrimSpace(str(args, "protocol"))
	if protocol == "" || protocol == "arest" {
		protocol = "arest-mqtt/v1"
	}
	if protocol != "arest-mqtt/v1" && protocol != "apteva.devices/v1" {
		return nil, errors.New("protocol must be arest-mqtt/v1 or apteva.devices/v1")
	}
	secret, err := generateSecret()
	if err != nil {
		return nil, err
	}
	if err := a.mqttCreateUser(id, secret); err != nil {
		return nil, fmt.Errorf("create MQTT device credential: %w", err)
	}
	manifest := object(args, "capabilities")
	if manifest == nil {
		manifest = map[string]any{}
	}
	manifest["protocol"] = protocol
	if err := validateManifest(manifest); err != nil {
		_ = a.mqttDeleteUser(id)
		return nil, err
	}
	d := Device{
		ID: id, ProjectID: a.projectID, Name: name, Protocol: protocol,
		Model: strings.TrimSpace(str(args, "model")), Manufacturer: strings.TrimSpace(str(args, "manufacturer")),
		MQTTUsername: id, Manifest: manifest, Metadata: object(args, "metadata"),
		CreatedAt: nowText(), UpdatedAt: nowText(),
	}
	if d.Metadata == nil {
		d.Metadata = map[string]any{}
	}
	if err := insertDevice(a.ctx.AppDB(), d); err != nil {
		_ = a.mqttDeleteUser(id)
		return nil, err
	}
	a.ctx.Emit("devices.device.created", map[string]any{"device_id": id, "name": name, "protocol": protocol})
	insertEvent(a.ctx.AppDB(), id, "created", map[string]any{"protocol": protocol})
	return a.enrollmentResponse(d, secret), nil
}

func (a *App) enrollmentResponse(d Device, secret string) map[string]any {
	broker := a.currentBroker()
	base := "devices/" + d.ID
	return map[string]any{
		"device": d, "mqtt": map[string]any{
			"endpoint": broker.Endpoint, "host": broker.AdvertisedHost, "port": broker.AdvertisedPort,
			"tls": broker.TLS, "username": d.MQTTUsername, "password": secret,
		},
		"password_shown_once": true,
		"topics": map[string]any{
			"commands": base + "/commands", "response": base + "/response", "telemetry": base + "/telemetry",
			"state": base + "/state", "manifest": base + "/manifest", "availability": base + "/availability",
		},
		"arest": map[string]any{"device_id": d.ID, "api_key": secret, "server": broker.AdvertisedHost, "port": broker.AdvertisedPort},
	}
}

func (a *App) toolUpdate(_ *sdk.AppCtx, args map[string]any) (any, error) {
	d, err := updateDeviceFields(a.ctx.AppDB(), a.projectID, str(args, "device_id"), object(args, "patch"))
	if err == nil {
		a.ctx.Emit("devices.device.updated", map[string]any{"device_id": d.ID, "changed": "metadata"})
	}
	return d, err
}

func (a *App) toolEnable(_ *sdk.AppCtx, args map[string]any) (any, error) {
	return a.setEnabled(str(args, "device_id"), true)
}

func (a *App) toolDisable(_ *sdk.AppCtx, args map[string]any) (any, error) {
	return a.setEnabled(str(args, "device_id"), false)
}

func (a *App) setEnabled(id string, enabled bool) (any, error) {
	d, err := getDevice(a.ctx.AppDB(), a.projectID, id, false)
	if err != nil {
		return nil, err
	}
	if err := a.mqttSetEnabled(d.MQTTUsername, enabled); err != nil {
		return nil, err
	}
	if err := setDeviceEnabled(a.ctx.AppDB(), a.projectID, id, enabled); err != nil {
		return nil, err
	}
	d, err = getDevice(a.ctx.AppDB(), a.projectID, id, true)
	if err == nil {
		a.ctx.Emit("devices.device.status_changed", map[string]any{"device_id": id, "status": d.Status, "enabled": enabled})
	}
	return d, err
}

func (a *App) toolRotateSecret(_ *sdk.AppCtx, args map[string]any) (any, error) {
	id := str(args, "device_id")
	d, err := getDevice(a.ctx.AppDB(), a.projectID, id, false)
	if err != nil {
		return nil, err
	}
	secret, err := generateSecret()
	if err != nil {
		return nil, err
	}
	if err := a.mqttRotatePassword(d.MQTTUsername, secret); err != nil {
		return nil, err
	}
	_, err = a.ctx.AppDB().Exec(`UPDATE devices SET credential_version=credential_version+1,status='offline',availability='offline',updated_at=? WHERE id=?`, nowText(), id)
	if err != nil {
		return nil, err
	}
	d, _ = getDevice(a.ctx.AppDB(), a.projectID, id, false)
	return map[string]any{"device_id": id, "username": d.MQTTUsername, "password": secret, "password_shown_once": true, "credential_version": d.CredentialVersion}, nil
}

func (a *App) toolDelete(_ *sdk.AppCtx, args map[string]any) (any, error) {
	if !boolean(args, "confirm", false) {
		return nil, errors.New("confirm=true required")
	}
	id := str(args, "device_id")
	d, err := getDevice(a.ctx.AppDB(), a.projectID, id, false)
	if err != nil {
		return nil, err
	}
	if err := a.mqttDeleteUser(d.MQTTUsername); err != nil {
		return nil, err
	}
	if err := deleteDeviceRow(a.ctx.AppDB(), a.projectID, id); err != nil {
		return nil, err
	}
	a.ctx.Emit("devices.device.deleted", map[string]any{"device_id": id})
	return map[string]any{"ok": true, "device_id": id}, nil
}

func (a *App) toolStateGet(_ *sdk.AppCtx, args map[string]any) (any, error) {
	id := str(args, "device_id")
	if _, err := getDevice(a.ctx.AppDB(), a.projectID, id, false); err != nil {
		return nil, err
	}
	return listState(a.ctx.AppDB(), id, str(args, "key"))
}

func (a *App) toolCapabilitiesSet(_ *sdk.AppCtx, args map[string]any) (any, error) {
	id := str(args, "device_id")
	d, err := getDevice(a.ctx.AppDB(), a.projectID, id, false)
	if err != nil {
		return nil, err
	}
	manifest := object(args, "manifest")
	if manifest == nil {
		return nil, errors.New("manifest required")
	}
	if _, ok := manifest["protocol"]; !ok {
		manifest["protocol"] = d.Protocol
	}
	if err := validateManifest(manifest); err != nil {
		return nil, err
	}
	if err := saveManifest(a.ctx.AppDB(), a.projectID, id, manifest, nowText()); err != nil {
		return nil, err
	}
	a.ctx.Emit("devices.device.updated", map[string]any{"device_id": id, "changed": "capabilities"})
	return getDevice(a.ctx.AppDB(), a.projectID, id, true)
}

func (a *App) toolCapabilitiesRefresh(_ *sdk.AppCtx, args map[string]any) (any, error) {
	return a.sendStandard(args, "device.info", "", map[string]any{})
}

func (a *App) toolVariableGet(_ *sdk.AppCtx, args map[string]any) (any, error) {
	name := strings.TrimSpace(str(args, "name"))
	if name == "" {
		return nil, errors.New("name required")
	}
	return a.sendStandard(args, "variable.get", name, map[string]any{"name": name})
}

func (a *App) toolFunctionCall(_ *sdk.AppCtx, args map[string]any) (any, error) {
	name := strings.TrimSpace(str(args, "name"))
	if name == "" {
		return nil, errors.New("name required")
	}
	return a.sendStandard(args, "function.call", name, objectOrEmpty(args, "arguments"))
}

func (a *App) toolPinRead(_ *sdk.AppCtx, args map[string]any) (any, error) {
	id, pin := str(args, "device_id"), args["pin"]
	d, err := getDevice(a.ctx.AppDB(), a.projectID, id, false)
	if err != nil {
		return nil, err
	}
	entry, err := allowedPin(d.Manifest, pin, false, "")
	if err != nil {
		return nil, err
	}
	kind := firstNonEmpty(str(args, "kind"), firstString(entry, "type", "kind"), "digital")
	return a.sendStandard(args, "pin.read", pinText(pin), map[string]any{"pin": pin, "kind": kind})
}

func (a *App) toolPinWrite(_ *sdk.AppCtx, args map[string]any) (any, error) {
	id, pin := str(args, "device_id"), args["pin"]
	d, err := getDevice(a.ctx.AppDB(), a.projectID, id, false)
	if err != nil {
		return nil, err
	}
	entry, err := allowedPin(d.Manifest, pin, true, "")
	if err != nil {
		return nil, err
	}
	kind := strings.ToLower(firstNonEmpty(str(args, "kind"), firstString(entry, "type", "kind"), "digital"))
	value := args["value"]
	if err := validatePinValue(kind, value); err != nil {
		return nil, err
	}
	return a.sendStandard(args, "pin.write", pinText(pin), map[string]any{"pin": pin, "value": value, "kind": kind})
}

func (a *App) toolPinMode(_ *sdk.AppCtx, args map[string]any) (any, error) {
	id, pin, mode := str(args, "device_id"), args["pin"], str(args, "mode")
	d, err := getDevice(a.ctx.AppDB(), a.projectID, id, false)
	if err != nil {
		return nil, err
	}
	if _, err := allowedPin(d.Manifest, pin, false, mode); err != nil {
		return nil, err
	}
	return a.sendStandard(args, "pin.mode", pinText(pin), map[string]any{"pin": pin, "mode": mode})
}

func (a *App) toolCommandSend(_ *sdk.AppCtx, args map[string]any) (any, error) {
	operation := strings.TrimSpace(str(args, "operation"))
	if operation == "" {
		return nil, errors.New("operation required")
	}
	return a.sendStandard(args, operation, str(args, "target"), objectOrEmpty(args, "arguments"))
}

func (a *App) sendStandard(args map[string]any, operation, target string, arguments map[string]any) (any, error) {
	d, err := getDevice(a.ctx.AppDB(), a.projectID, str(args, "device_id"), false)
	if err != nil {
		return nil, err
	}
	command, err := a.sendCommand(d, operation, target, arguments, boolean(args, "wait", true), integer(args, "timeout_ms", 0), str(args, "idempotency_key"))
	if err != nil {
		return nil, err
	}
	return commandResult(command), nil
}

func (a *App) sendCommand(d Device, operation, target string, arguments map[string]any, wait bool, timeoutMS int, idempotencyKey string) (Command, error) {
	if !d.Enabled {
		return Command{}, errors.New("device is disabled")
	}
	if err := validateCommand(d, operation, target, arguments, idempotencyKey); err != nil {
		return Command{}, err
	}
	if idempotencyKey != "" {
		if existing, err := getCommandByIdempotency(a.ctx.AppDB(), d.ID, idempotencyKey); err == nil {
			return a.waitForExistingCommand(existing, wait)
		}
	}
	if timeoutMS == 0 {
		timeoutMS = configInt(a.ctx, "command_timeout_ms", 10000, 500, 60000)
	}
	if timeoutMS < 500 || timeoutMS > 60000 {
		return Command{}, errors.New("timeout_ms must be between 500 and 60000")
	}
	id := "cmd_" + strings.ReplaceAll(uuid.NewString(), "-", "")
	now := time.Now().UTC()
	request := buildWireCommand(id, operation, target, arguments, timeoutMS)
	c := Command{
		ID: id, DeviceID: d.ID, Operation: operation, Target: target, Arguments: arguments,
		Request: request, Status: "queued", IdempotencyKey: idempotencyKey, TimeoutMS: timeoutMS,
		CreatedAt: formatTime(now), DeadlineAt: formatTime(now.Add(time.Duration(timeoutMS) * time.Millisecond)),
	}
	if err := insertCommand(a.ctx.AppDB(), c); err != nil {
		return Command{}, err
	}
	var waiter <-chan struct{}
	var unregister func()
	if wait {
		waiter, unregister = a.registerWaiter(id)
		defer unregister()
	}
	if err := a.mqttPublish("devices/"+d.ID+"/commands", request, false, 1); err != nil {
		_ = markCommandPublishFailed(a.ctx.AppDB(), id, err)
		a.ctx.Emit("devices.command.failed", map[string]any{"command_id": id, "device_id": d.ID, "operation": operation, "status": "failed"})
		insertEvent(a.ctx.AppDB(), d.ID, "command.failed", map[string]any{"command_id": id, "operation": operation, "error": err.Error()})
		a.notifyWaiters(id)
		return Command{}, fmt.Errorf("publish device command: %w", err)
	}
	if err := markCommandSent(a.ctx.AppDB(), id, nowText()); err != nil {
		return Command{}, err
	}
	if wait {
		timer := time.NewTimer(time.Duration(timeoutMS) * time.Millisecond)
		defer timer.Stop()
		select {
		case <-waiter:
		case <-timer.C:
			ids, _ := markTimedOutCommands(a.ctx.AppDB(), time.Now().UTC())
			a.emitTimedOutCommands(ids)
		}
	}
	return getCommand(a.ctx.AppDB(), id)
}

func (a *App) waitForExistingCommand(existing Command, wait bool) (Command, error) {
	if !wait || commandTerminal(existing.Status) {
		return existing, nil
	}
	waiter, unregister := a.registerWaiter(existing.ID)
	defer unregister()
	// Close the race between the first lookup and waiter registration.
	current, err := getCommand(a.ctx.AppDB(), existing.ID)
	if err != nil || commandTerminal(current.Status) {
		return current, err
	}
	deadline, err := time.Parse(time.RFC3339Nano, current.DeadlineAt)
	if err != nil {
		return Command{}, err
	}
	remaining := time.Until(deadline)
	if remaining <= 0 {
		ids, _ := markTimedOutCommands(a.ctx.AppDB(), time.Now().UTC())
		a.emitTimedOutCommands(ids)
		return getCommand(a.ctx.AppDB(), existing.ID)
	}
	timer := time.NewTimer(remaining)
	defer timer.Stop()
	select {
	case <-waiter:
	case <-timer.C:
		ids, _ := markTimedOutCommands(a.ctx.AppDB(), time.Now().UTC())
		a.emitTimedOutCommands(ids)
	}
	return getCommand(a.ctx.AppDB(), existing.ID)
}

func commandTerminal(status string) bool {
	switch status {
	case "succeeded", "failed", "timed_out", "cancelled":
		return true
	default:
		return false
	}
}

func validateCommand(d Device, operation, target string, arguments map[string]any, idempotencyKey string) error {
	if !operationPattern.MatchString(operation) {
		return errors.New("operation must match [a-z][a-z0-9_.-]{0,63}")
	}
	if len(target) > 128 {
		return errors.New("target must be 128 characters or fewer")
	}
	if len(idempotencyKey) > 128 {
		return errors.New("idempotency_key must be 128 characters or fewer")
	}
	raw, err := json.Marshal(arguments)
	if err != nil {
		return fmt.Errorf("encode command arguments: %w", err)
	}
	if len(raw) > 65536 {
		return errors.New("command arguments exceed 64 KiB")
	}
	switch operation {
	case "pin.read", "pin.write", "pin.mode":
		pin := arguments["pin"]
		if pin == nil {
			pin = target
			arguments["pin"] = pin
		}
		entry, err := allowedPin(d.Manifest, pin, operation == "pin.write", str(arguments, "mode"))
		if err != nil {
			return err
		}
		if operation == "pin.write" {
			kind := strings.ToLower(firstNonEmpty(str(arguments, "kind"), firstString(entry, "type", "kind"), "digital"))
			arguments["kind"] = kind
			if err := validatePinValue(kind, arguments["value"]); err != nil {
				return err
			}
		}
	}
	return nil
}

func buildWireCommand(id, operation, target string, arguments map[string]any, timeoutMS int) map[string]any {
	params := cloneMap(arguments)
	wireType := operation
	switch operation {
	case "device.info":
		wireType = "get_info"
	case "variable.get":
		wireType = "get_variable"
		params["variable"] = target
		params["name"] = target
	case "function.call":
		wireType = "function"
		params["function"] = target
		params["name"] = target
		if _, exists := params["args"]; !exists {
			if len(arguments) == 1 {
				for _, value := range arguments {
					params["args"] = fmt.Sprint(value)
				}
			} else if len(arguments) > 0 {
				raw, _ := json.Marshal(arguments)
				params["args"] = string(raw)
			}
		}
	case "pin.read":
		if strings.Contains(strings.ToLower(fmt.Sprint(arguments["kind"])), "analog") {
			wireType = "analog_read"
		} else {
			wireType = "digital_read"
		}
	case "pin.write":
		kind := strings.ToLower(fmt.Sprint(arguments["kind"]))
		if kind == "analog" || kind == "pwm" {
			wireType = "analog_write"
		} else {
			wireType = "digital_write"
		}
	case "pin.mode":
		wireType = "pin_mode"
	}
	wire := map[string]any{
		"command_id": id, "id": id, "type": wireType, "operation": operation,
		"target": target, "params": params, "arguments": arguments, "timeout_ms": timeoutMS,
	}
	for _, key := range []string{"pin", "value", "mode", "variable", "function"} {
		if value, ok := params[key]; ok {
			wire[key] = value
		}
	}
	return wire
}

func (a *App) toolCommandGet(_ *sdk.AppCtx, args map[string]any) (any, error) {
	c, err := getCommand(a.ctx.AppDB(), str(args, "command_id"))
	if err != nil {
		return nil, err
	}
	if _, err := getDevice(a.ctx.AppDB(), a.projectID, c.DeviceID, false); err != nil {
		return nil, err
	}
	return commandResult(c), nil
}

func (a *App) toolCommandsList(_ *sdk.AppCtx, args map[string]any) (any, error) {
	id := str(args, "device_id")
	if id != "" {
		if _, err := getDevice(a.ctx.AppDB(), a.projectID, id, false); err != nil {
			return nil, err
		}
	}
	return listCommands(a.ctx.AppDB(), id, str(args, "status"), integer(args, "limit", 100))
}

func commandResult(c Command) map[string]any { return map[string]any{"command_id": c.ID, "command": c} }

func allowedPin(manifest map[string]any, requested any, write bool, mode string) (map[string]any, error) {
	pins, _ := manifest["pins"].([]any)
	needle := pinText(requested)
	for _, item := range pins {
		pin, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if needle != pinText(pin["number"]) && needle != pinText(pin["name"]) {
			continue
		}
		if write {
			if writable, exists := pin["writable"].(bool); exists && !writable {
				return nil, fmt.Errorf("pin %s is read-only", needle)
			}
		}
		if mode != "" {
			allowed := false
			for _, item := range stringSlice(pin["modes"]) {
				if item == mode {
					allowed = true
				}
			}
			if !allowed {
				return nil, fmt.Errorf("pin %s does not allow mode %s", needle, mode)
			}
		}
		return pin, nil
	}
	return nil, fmt.Errorf("pin %s is not allowlisted in the device manifest", needle)
}

func validatePinValue(kind string, value any) error {
	if kind == "analog" || kind == "pwm" {
		n, ok := numeric(value)
		if !ok || n < 0 || n > 255 {
			return errors.New("PWM value must be between 0 and 255")
		}
		return nil
	}
	if _, ok := value.(bool); ok {
		return nil
	}
	n, ok := numeric(value)
	if !ok || (n != 0 && n != 1) {
		return errors.New("digital value must be boolean, 0, or 1")
	}
	return nil
}

func numeric(value any) (float64, bool) {
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
		v, err := n.Float64()
		return v, err == nil
	default:
		return 0, false
	}
}

func generateSecret() (string, error) {
	raw := make([]byte, 32)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func slugDeviceID(name string) string {
	name = strings.ToLower(strings.TrimSpace(name))
	var b strings.Builder
	lastDash := false
	for _, r := range name {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			lastDash = false
		} else if !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	id := strings.Trim(b.String(), "-")
	if id == "" {
		id = "device"
	}
	if len(id) > 54 {
		id = id[:54]
	}
	return id + "-" + strings.ReplaceAll(uuid.NewString()[:8], "-", "")
}

func validDeviceID(id string) bool { return deviceIDPattern.MatchString(id) }
func str(args map[string]any, key string) string {
	value, _ := args[key].(string)
	return strings.TrimSpace(value)
}
func object(args map[string]any, key string) map[string]any {
	value, _ := args[key].(map[string]any)
	return value
}
func objectOrEmpty(args map[string]any, key string) map[string]any {
	if value := object(args, key); value != nil {
		return value
	}
	return map[string]any{}
}
func boolean(args map[string]any, key string, def bool) bool {
	value, ok := args[key].(bool)
	if !ok {
		return def
	}
	return value
}
func integer(args map[string]any, key string, def int) int {
	switch value := args[key].(type) {
	case float64:
		return int(value)
	case int:
		return value
	case int64:
		return int(value)
	case json.Number:
		n, _ := strconv.Atoi(value.String())
		return n
	default:
		return def
	}
}
func pinText(value any) string {
	if value == nil {
		return ""
	}
	switch n := value.(type) {
	case float64:
		return strconv.FormatFloat(n, 'f', -1, 64)
	default:
		return strings.TrimSpace(fmt.Sprint(value))
	}
}
func cloneMap(input map[string]any) map[string]any {
	out := make(map[string]any, len(input)+2)
	for key, value := range input {
		out[key] = value
	}
	return out
}
func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func stringSlice(value any) []string {
	switch items := value.(type) {
	case []string:
		return items
	case []any:
		out := make([]string, 0, len(items))
		for _, item := range items {
			out = append(out, strings.TrimSpace(fmt.Sprint(item)))
		}
		return out
	default:
		return nil
	}
}
