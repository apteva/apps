// discovery.go — Home Assistant MQTT discovery convention parser.
//
// HA convention: devices announce themselves by publishing to
//
//   homeassistant/<component>/<object_id>/config
//   homeassistant/<component>/<node_id>/<object_id>/config   (with-node form)
//
// where component is light | switch | sensor | binary_sensor |
// climate | cover | fan | …, and the JSON payload describes the
// device including state_topic, command_topic, name, manufacturer,
// model, etc.
//
// We subscribe inline to homeassistant/+/+/config and homeassistant/
// +/+/+/config, parse on each match, upsert into mqtt_devices, and
// auto-promote state_topic to a tracked subscription so attribute
// updates flow through the bus too.

package main

import (
	"encoding/json"
	"strings"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/packets"
)

type haConfigPayload struct {
	Name             string         `json:"name"`
	UniqueID         string         `json:"unique_id"`
	StateTopic       string         `json:"state_topic"`
	CommandTopic     string         `json:"command_topic"`
	AvailabilityTopic string        `json:"availability_topic"`
	JSONAttrTopic    string         `json:"json_attributes_topic"`
	Device           *struct {
		Manufacturer string   `json:"manufacturer"`
		Model        string   `json:"model"`
		Name         string   `json:"name"`
		Identifiers  []string `json:"identifiers"`
		SwVersion    string   `json:"sw_version"`
	} `json:"device"`
}

// bridgeHADiscovery wires the discovery sub when ha_discovery_enabled.
// Two filters cover the with-node and no-node forms.
func (a *App) bridgeHADiscovery(b *Broker) error {
	if !configFlag(a.ctx, "ha_discovery_enabled", true) {
		return nil
	}
	cb := func(cl *mqtt.Client, sub packets.Subscription, pk packets.Packet) {
		a.handleHAConfig(pk.TopicName, pk.Payload)
	}
	// Distinct subscription IDs so they coexist with the wildcard
	// loopback (id=1) and per-pattern bus subs (id=100+).
	if err := b.Subscribe("homeassistant/+/+/config", 2, cb); err != nil {
		return err
	}
	if err := b.Subscribe("homeassistant/+/+/+/config", 3, cb); err != nil {
		return err
	}
	return nil
}

func (a *App) handleHAConfig(topic string, payload []byte) {
	component, objectID, ok := parseHATopic(topic)
	if !ok {
		return
	}
	if len(payload) == 0 {
		// Empty payload = HA convention for "remove this device".
		_, _ = a.ctx.AppDB().Exec(
			`DELETE FROM mqtt_devices WHERE project_id = ? AND slug = ?`,
			projectScope(), topicWithoutSuffix(topic))
		return
	}
	var hp haConfigPayload
	if err := json.Unmarshal(payload, &hp); err != nil {
		// Don't log every bad payload at warn — discovery is best-effort.
		return
	}
	display := hp.Name
	if display == "" && hp.Device != nil {
		display = hp.Device.Name
	}
	manuf, model := "", ""
	if hp.Device != nil {
		manuf = hp.Device.Manufacturer
		model = hp.Device.Model
	}
	slug := topicWithoutSuffix(topic) // homeassistant/<comp>/<obj>
	_, _ = a.ctx.AppDB().Exec(
		`INSERT INTO mqtt_devices
		   (project_id, slug, component, object_id, display_name,
		    manufacturer, model, state_topic, command_topic, ha_config_json, last_seen)
		 VALUES (?,?,?,?,?,?,?,?,?,?, CURRENT_TIMESTAMP)
		 ON CONFLICT(project_id, slug) DO UPDATE SET
		   display_name = excluded.display_name,
		   manufacturer = excluded.manufacturer,
		   model = excluded.model,
		   state_topic = excluded.state_topic,
		   command_topic = excluded.command_topic,
		   ha_config_json = excluded.ha_config_json,
		   last_seen = CURRENT_TIMESTAMP`,
		projectScope(), slug, component, objectID, display,
		manuf, model, hp.StateTopic, hp.CommandTopic, string(payload),
	)
}

// parseHATopic returns (component, object_id, ok) for either form:
//   homeassistant/<component>/<object_id>/config
//   homeassistant/<component>/<node_id>/<object_id>/config
func parseHATopic(topic string) (string, string, bool) {
	parts := strings.Split(topic, "/")
	if len(parts) < 4 || parts[0] != "homeassistant" || parts[len(parts)-1] != "config" {
		return "", "", false
	}
	component := parts[1]
	objectID := parts[len(parts)-2]
	return component, objectID, true
}

func topicWithoutSuffix(topic string) string {
	return strings.TrimSuffix(topic, "/config")
}
