// bus.go — bidirectional bridge between the MQTT broker and the
// platform event bus.
//
// Inbound (MQTT → platform):
//
//   * busHook captures every published message into mqtt_message_log
//     (bounded ring) so the panel's Live tab can fetch the last N.
//   * bridgeBusLoopback subscribes (inline) to "#" and emits each
//     message as `mqtt.message`. Keeps the per-app event topic
//     namespace flat so subscribers can filter on data fields.
//   * Each row in mqtt_subscriptions adds a second inline sub on its
//     pattern that re-emits as `mqtt.<bus_topic>` — gives the operator
//     a way to register a stable bus topic an automation app can
//     subscribe to without speaking MQTT.
//
// Outbound (platform → MQTT):
//
//   * Sibling apps that want to publish without depending on this
//     app at link time emit `mqtt.publish_request` with
//     {topic, payload, retain?, qos?}. We register an EventHandler
//     for that topic and forward it to the broker.

package main

import (
	"context"
	"fmt"
	"unicode/utf8"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/packets"
)

// ─── busHook — capture-only, no auth ────────────────────────────────

type busHook struct {
	mqtt.HookBase
	app *App
}

func (h *busHook) ID() string { return "apteva-bus" }
func (h *busHook) Provides(b byte) bool {
	return b == mqtt.OnPublished
}

// OnPublished fires after a publish has been routed to subscribers.
// Cheap to do here — it's already a hot path the broker amortises.
func (h *busHook) OnPublished(cl *mqtt.Client, pk packets.Packet) {
	h.recordMessage(string(cl.ID), pk)
}

func (h *busHook) recordMessage(clientID string, pk packets.Packet) {
	printable := isPrintableUTF8(pk.Payload)
	retain := 0
	if pk.FixedHeader.Retain {
		retain = 1
	}
	_, _ = h.app.ctx.AppDB().Exec(
		`INSERT INTO mqtt_message_log(topic, payload, qos, retain, client_id, is_printable)
		 VALUES (?,?,?,?,?,?)`,
		pk.TopicName, pk.Payload, pk.FixedHeader.Qos, retain, clientID, boolToInt(printable))
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// isPrintableUTF8 is the panel's "should I show this as a string or
// a base64 blob" heuristic. Conservative: if any non-printable byte
// shows up (other than \n, \r, \t), call it binary.
func isPrintableUTF8(p []byte) bool {
	if !utf8.Valid(p) {
		return false
	}
	for _, b := range p {
		if b < 0x20 && b != '\n' && b != '\r' && b != '\t' {
			return false
		}
	}
	return true
}

// ─── inline bus loopback ────────────────────────────────────────────

// bridgeBusLoopback wires the wildcard subscription that mirrors
// every MQTT message onto the platform event bus as `mqtt.message`.
// Also adds one inline sub per row in mqtt_subscriptions that
// re-emits matches under the operator-chosen bus topic.
func (a *App) bridgeBusLoopback(b *Broker) error {
	// Wildcard catch-all. Subscription identifier 1 — distinct from
	// any per-pattern bus subscriptions (which start at 100).
	if err := b.Subscribe("#", 1, func(cl *mqtt.Client, sub packets.Subscription, pk packets.Packet) {
		a.emitBusMessage("message", pk)
	}); err != nil {
		return err
	}

	subs, err := listBusSubscriptions(a.ctx.AppDB())
	if err != nil {
		return err
	}
	for _, s := range subs {
		s := s
		_ = b.Subscribe(s.TopicPattern, int(100+s.ID), func(cl *mqtt.Client, sub packets.Subscription, pk packets.Packet) {
			a.emitBusMessage(s.BusTopic, pk)
		})
	}
	return nil
}

// emitBusMessage shapes an MQTT packet into a platform event payload.
// Strings stay strings (UTF-8 cheap to inspect), binary becomes a
// length hint instead of bloating the bus with base64.
func (a *App) emitBusMessage(busTopic string, pk packets.Packet) {
	payloadField := any(string(pk.Payload))
	if !isPrintableUTF8(pk.Payload) {
		payloadField = map[string]any{
			"binary":     true,
			"size_bytes": len(pk.Payload),
		}
	}
	a.ctx.Emit("mqtt."+busTopic, map[string]any{
		"topic":   pk.TopicName,
		"payload": payloadField,
		"qos":     int(pk.FixedHeader.Qos),
		"retain":  pk.FixedHeader.Retain,
	})
}

// ─── outbound platform→MQTT bridge ──────────────────────────────────

// EventHandlers returns subscriptions on platform-bus topics this
// app cares about. mqtt.publish_request lets sibling apps publish
// to MQTT without speaking the protocol themselves: emit
//
//   ctx.Emit("mqtt.publish_request", {topic, payload, retain?, qos?})
//
// and we forward into the broker.
//
// (The SDK calls this method during framework wiring; the handler
// runs on the framework's dispatcher goroutine.)
//
// (See main.go where EventHandlers() is overridden to surface this.)
func (a *App) eventHandlers() []func(ctx context.Context, evt map[string]any) error {
	return []func(ctx context.Context, evt map[string]any) error{
		a.handleOutboundPublishRequest,
	}
}

func (a *App) handleOutboundPublishRequest(ctx context.Context, evt map[string]any) error {
	if a.broker == nil {
		return fmt.Errorf("broker not running yet")
	}
	topic, _ := evt["topic"].(string)
	if topic == "" {
		return fmt.Errorf("topic required")
	}
	var payload []byte
	switch p := evt["payload"].(type) {
	case string:
		payload = []byte(p)
	case []byte:
		payload = p
	}
	retain, _ := evt["retain"].(bool)
	qos := byte(0)
	if v, ok := evt["qos"].(float64); ok {
		qos = byte(v)
	}
	return a.broker.Publish(topic, payload, retain, qos)
}
