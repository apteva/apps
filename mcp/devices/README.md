# Devices

Devices is Apteva's project-scoped hardware control plane. It provisions one
MQTT identity per device and provides agent tools and a UI for capabilities,
live state, telemetry, GPIO, variables, firmware functions, and correlated
commands.

The app requires MQTT `>=0.3.0`. MQTT owns transport and credentials; Devices
owns device semantics. A device can be an Arduino/ESP8266/ESP32, Raspberry Pi,
Linux service, PLC gateway, or anything else with an MQTT client.

## Provisioning

Install MQTT, then install Devices. Provision a device from the project Devices
page or with `devices_provision`. Save the returned password immediately: it is
not persisted by Devices and cannot be revealed later.

Every device gets these topics:

```text
devices/{device_id}/commands       subscribe
devices/{device_id}/response       publish
devices/{device_id}/telemetry      publish
devices/{device_id}/state          publish, retained recommended
devices/{device_id}/manifest       publish, retained recommended
devices/{device_id}/availability   publish, retained/LWT recommended
```

The MQTT username is the device ID. Its ACL can only publish/subscribe the
listed exact topics for that same ID.

## aREST compatibility

Current aREST MQTT firmware uses the same four core topics, the device ID as
the username, and the API key as the password. Configure the provisioned broker
with:

```cpp
client.deviceId = "greenhouse-1";
client.apiKey = "the-one-time-password";
client.setServer("your-apteva-host", 1883);
```

Devices emits aREST-compatible command fields (`command_id`, `type`, and
`params`) alongside the native versioned fields. Supported aREST operations
include `get_info`, `get_variable`, `function`, `digital_read`,
`digital_write`, `analog_read`, and `analog_write`.

aREST's `get_info` discovers variables and functions but does not report a safe
GPIO allowlist. Add exposed pins in Setup or pass `capabilities` while
provisioning:

```json
{
  "protocol": "arest-mqtt/v1",
  "pins": [
    {"name": "status_led", "number": 2, "type": "digital", "modes": ["input", "output"], "writable": true},
    {"name": "light", "number": 34, "type": "analog", "modes": ["input"], "writable": false}
  ]
}
```

## Native command envelope

Commands contain both the native and compatibility representation:

```json
{
  "command_id": "cmd_...",
  "id": "cmd_...",
  "operation": "pin.write",
  "type": "digital_write",
  "target": "status_led",
  "arguments": {"pin": 2, "kind": "digital", "value": 1},
  "params": {"pin": 2, "kind": "digital", "value": 1},
  "timeout_ms": 10000
}
```

The response must echo `command_id` (or `id`) and report either `success` or a
terminal `status`:

```json
{"command_id":"cmd_...","success":true,"result":{"return_value":1}}
```

Unknown commands should fail explicitly. Device clients must never interpret a
command as arbitrary shell or firmware code.

## Examples

- `examples/arduino/AptevaDevices.ino`: ESP32/ESP8266 MQTT, retained manifest,
  telemetry, a function, and allowlisted GPIO.
- `examples/python/apteva_device.py`: Raspberry Pi/Linux client with the same
  contract and optional `RPi.GPIO` support.

Both examples read placeholders that must be replaced with the one-time values
returned during provisioning.
