#!/usr/bin/env python3
"""Generic v2 device. Use the exact username/topic base from enrollment."""
import json
import os
import time
import paho.mqtt.client as mqtt
from device_runtime import DeviceRuntime
from mqtt_transport import configure_tls

BASE = os.environ["APTEVA_DEVICE_TOPIC_BASE"].rstrip("/")
USERNAME = os.environ["APTEVA_DEVICE_USERNAME"]
PASSWORD = os.environ["APTEVA_DEVICE_PASSWORD"]
HOST = os.environ["APTEVA_MQTT_HOST"]
PORT = int(os.environ.get("APTEVA_MQTT_PORT", "8883"))
RUNTIME = DeviceRuntime(os.environ.get("APTEVA_DEVICE_JOURNAL", "device-journal.sqlite"))


def execute(command):
    if command["operation"] == "echo":
        return command["arguments"]
    raise ValueError("unsupported operation")


def on_connect(client, _userdata, _flags, reason_code, _properties):
    if reason_code != 0:
        raise RuntimeError(f"MQTT connection failed: {reason_code}")
    client.subscribe([(f"{BASE}/commands", 1), (f"{BASE}/receipts", 1)])
    client.publish(f"{BASE}/availability", "online", qos=1, retain=True)
    client.publish(f"{BASE}/manifest", json.dumps({"protocol": "apteva.devices/v2", "operations": [
        {"name": "echo", "input_schema": {"type": "object"}, "output_schema": {"type": "object"}}
    ]}), qos=1, retain=True)


def on_message(client, _userdata, message):
    if message.topic.endswith("/receipts"):
        RUNTIME.receipt(message.payload.decode())
    elif message.topic.endswith("/commands"):
        response = RUNTIME.command(json.loads(message.payload), execute)
        client.publish(f"{BASE}/response", json.dumps(response), qos=1)


def main():
    client = mqtt.Client(mqtt.CallbackAPIVersion.VERSION2,
                         client_id=os.environ.get("APTEVA_MQTT_CLIENT_ID", USERNAME))
    configure_tls(client)
    client.username_pw_set(USERNAME, PASSWORD)
    client.will_set(f"{BASE}/availability", "offline", qos=1, retain=True)
    client.on_connect, client.on_message = on_connect, on_message
    client.connect(HOST, PORT, 60)
    client.loop_start()
    try:
        next_sample = 0.0
        while True:
            if time.monotonic() >= next_sample:
                RUNTIME.sample("state", {"name": "generic-device", "nested": {"uptime": time.monotonic()}})
                next_sample = time.monotonic() + 10
            RUNTIME.resend(lambda kind, payload: client.publish(f"{BASE}/{kind}", payload, qos=1))
            time.sleep(2)
    finally:
        client.disconnect()
        client.loop_stop()
        RUNTIME.close()


if __name__ == "__main__":
    main()
