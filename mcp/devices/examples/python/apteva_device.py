#!/usr/bin/env python3
"""Minimal Apteva Devices client for Raspberry Pi or generic Linux."""

import json
import os
import platform
import time
from typing import Any

import paho.mqtt.client as mqtt
from device_runtime import DeviceRuntime

try:
    import RPi.GPIO as GPIO  # type: ignore
except ImportError:
    GPIO = None

DEVICE_ID = os.environ.get("APTEVA_DEVICE_ID", "raspberry-pi-1")
PASSWORD = os.environ.get("APTEVA_DEVICE_PASSWORD", "replace-me")
HOST = os.environ.get("APTEVA_MQTT_HOST", "localhost")
PORT = int(os.environ.get("APTEVA_MQTT_PORT", "1883"))
USERNAME = os.environ["APTEVA_DEVICE_USERNAME"]
BASE = os.environ["APTEVA_DEVICE_TOPIC_BASE"].rstrip("/")
RUNTIME = DeviceRuntime(os.environ.get("APTEVA_DEVICE_JOURNAL", "device-journal.sqlite"))

# Only these pins can be touched. Keep this list narrow for the attached board.
PINS = {
    "status_led": {"number": 17, "type": "digital", "modes": ["input", "output"], "writable": True},
    "button": {"number": 27, "type": "digital", "modes": ["input", "input_pullup"], "writable": False},
}

started_at = time.monotonic()


def publish_json(client: mqtt.Client, suffix: str, value: Any, *, retain: bool = False, qos: int = 1) -> None:
    client.publish(f"{BASE}/{suffix}", json.dumps(value, separators=(",", ":")), qos=qos, retain=retain)


def pin_entry(value: Any) -> dict[str, Any]:
    needle = str(value)
    for name, pin in PINS.items():
        if needle in (name, str(pin["number"])):
            return {"name": name, **pin}
    raise ValueError(f"pin {needle} is not allowlisted")


def set_mode(pin: dict[str, Any], mode: str) -> None:
    if mode not in pin["modes"]:
        raise ValueError(f"mode {mode} is not allowed for {pin['name']}")
    if GPIO is None:
        raise RuntimeError("RPi.GPIO is not installed on this host")
    mapping = {"input": GPIO.IN, "input_pullup": GPIO.IN, "output": GPIO.OUT}
    kwargs = {"pull_up_down": GPIO.PUD_UP} if mode == "input_pullup" else {}
    GPIO.setup(pin["number"], mapping[mode], **kwargs)


def execute(command: dict[str, Any]) -> Any:
    operation = command.get("operation") or command.get("type", "")
    args = command.get("arguments") or command.get("params") or {}
    target = command.get("target", "")

    if operation in ("device.info", "get_info"):
        return {"id": DEVICE_ID, "name": platform.node(), "hardware": platform.machine(), "connected": True,
                "variables": {"cpu_load_1m": os.getloadavg()[0], "uptime_seconds": int(time.monotonic() - started_at)},
                "functions": ["identify"]}
    if operation in ("variable.get", "get_variable"):
        name = target or args.get("name") or args.get("variable")
        values = {"cpu_load_1m": os.getloadavg()[0], "uptime_seconds": int(time.monotonic() - started_at)}
        if name not in values:
            raise ValueError(f"unknown variable {name}")
        return {"variable": name, "value": values[name]}
    if operation in ("function.call", "function"):
        name = target or args.get("name") or args.get("function")
        if name != "identify":
            raise ValueError(f"unknown function {name}")
        return {"executed": "identify", "message": f"Hello from {platform.node()}"}
    if operation in ("pin.read", "digital_read", "analog_read"):
        pin = pin_entry(args.get("pin", target))
        if GPIO is None:
            raise RuntimeError("RPi.GPIO is not installed on this host")
        set_mode(pin, "input")
        return {"pin": pin["number"], "return_value": GPIO.input(pin["number"])}
    if operation in ("pin.write", "digital_write", "analog_write"):
        pin = pin_entry(args.get("pin", target))
        if not pin["writable"]:
            raise ValueError(f"pin {pin['name']} is read-only")
        set_mode(pin, "output")
        value = 1 if args.get("value") in (1, True, "1", "HIGH") else 0
        GPIO.output(pin["number"], value)
        return {"pin": pin["number"], "return_value": value}
    if operation in ("pin.mode", "pin_mode"):
        pin = pin_entry(args.get("pin", target))
        set_mode(pin, str(args.get("mode")))
        return {"pin": pin["number"], "mode": args.get("mode")}
    raise ValueError(f"unsupported operation {operation}")


def on_connect(client: mqtt.Client, _userdata: Any, _flags: Any, reason_code: Any, _properties: Any) -> None:
    if reason_code != 0:
        print(f"MQTT connection failed: {reason_code}")
        return
    client.subscribe(f"{BASE}/commands", qos=1)
    publish_json(client, "availability", "online", retain=True)
    publish_json(client, "manifest", {
        "protocol": "apteva.devices/v1", "command_contract": "apteva.commands/v1", "name": platform.node(), "model": platform.machine(),
        "manufacturer": "Raspberry Pi" if GPIO else "Linux", "firmware": platform.release(),
        "variables": [{"name": "cpu_load_1m", "type": "number", "readable": True},
                      {"name": "uptime_seconds", "type": "number", "unit": "s", "readable": True}],
        "functions": [{"name": "identify"}], "pins": [{"name": name, **pin} for name, pin in PINS.items()],
    }, retain=True)


def on_message(client: mqtt.Client, _userdata: Any, message: mqtt.MQTTMessage) -> None:
    command = json.loads(message.payload)
    response = RUNTIME.command(command, execute)
    publish_json(client, "response", response)


def main() -> None:
    if GPIO is not None:
        GPIO.setmode(GPIO.BCM)
    client = mqtt.Client(mqtt.CallbackAPIVersion.VERSION2, client_id=os.environ.get("APTEVA_MQTT_CLIENT_ID", USERNAME))
    client.username_pw_set(USERNAME, PASSWORD)
    client.will_set(f"{BASE}/availability", payload="offline", qos=1, retain=True)
    client.on_connect = on_connect
    client.on_message = on_message
    client.connect(HOST, PORT, keepalive=30)
    client.loop_start()
    try:
        while True:
            publish_json(client, "telemetry", {"cpu_load_1m": os.getloadavg()[0], "uptime_seconds": int(time.monotonic() - started_at)}, qos=0)
            time.sleep(10)
    finally:
        publish_json(client, "availability", "offline", retain=True)
        client.disconnect()
        if GPIO is not None:
            GPIO.cleanup()


if __name__ == "__main__":
    main()
