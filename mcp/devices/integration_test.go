package main

import (
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

// This exercises the real app-sdk gateway and the real MQTT sidecar. MQTT uses
// an explicit ephemeral development port so the test never competes for the
// managed production port 1883.
func TestSidecarProvisionWithRealMQTT(t *testing.T) {
	if testing.Short() {
		t.Skip("sidecar integration test")
	}
	sc := tk.SpawnSidecar(t, ".",
		tk.WithProjectID("project-integration"),
		tk.WithDependency("mqtt", "../mqtt", tk.DependencyConfig(map[string]string{
			"listen_port": "0", "bind_interface": "127.0.0.1", "ha_discovery_enabled": "false",
		})),
	)

	enrollment := sc.MCP("devices_provision", map[string]any{
		"name": "Integration board", "device_id": "integration-board",
		"capabilities": map[string]any{
			"pins": []any{map[string]any{
				"name": "led", "number": 2, "type": "digital", "writable": true,
				"modes": []any{"input", "output"},
			}},
		},
	})
	mqtt, _ := enrollment["mqtt"].(map[string]any)
	if mqtt["username"] != "integration-board" || mqtt["password"] == "" || mqtt["endpoint"] == "" {
		t.Fatalf("enrollment missing broker credentials: %#v", enrollment)
	}

	device := sc.MCP("devices_get", map[string]any{"device_id": "integration-board"})
	if device["id"] != "integration-board" || device["status"] != "provisioned" {
		t.Fatalf("provisioned device = %#v", device)
	}

	command := sc.MCP("devices_pin_write", map[string]any{
		"device_id": "integration-board", "pin": "led", "value": 1, "wait": false,
	})
	if command["command_id"] == "" {
		t.Fatalf("command submission = %#v", command)
	}
}
