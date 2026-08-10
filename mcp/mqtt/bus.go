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
	"encoding/base64"
	"fmt"
	"unicode/utf8"

	sdk "github.com/apteva/app-sdk"
	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/packets"
)

// ─── busHook — capture-only, no auth ────────────────────────────────

type busHook struct {
	mqtt.HookBase
	app *App
}

type messageRecord struct {
	topic       string
	payload     []byte
	payloadSize int
	truncated   bool
	qos         byte
	retain      bool
	clientID    string
	username    string
	printable   bool
}

type aclRecord struct {
	username string
	action   string
	topic    string
	allowed  bool
	reason   string
}

func (h *busHook) ID() string { return "apteva-bus" }
func (h *busHook) Provides(b byte) bool {
	return b == mqtt.OnPublished
}

// OnPublished fires after a publish has been routed to subscribers.
// Cheap to do here — it's already a hot path the broker amortises.
func (h *busHook) OnPublished(cl *mqtt.Client, pk packets.Packet) {
	clientID, username := h.app.mqttIdentity(cl, pk)
	h.recordMessage(clientID, username, pk)
}

func (h *busHook) recordMessage(clientID, username string, pk packets.Packet) {
	maxLogPayload := clampConfigInt(h.app.ctx, "max_log_payload_bytes", 65536, 0, 268435455)
	payload := pk.Payload
	truncated := len(payload) > maxLogPayload
	if truncated {
		payload = payload[:maxLogPayload]
	}
	record := messageRecord{
		topic: pk.TopicName, payload: append([]byte(nil), payload...),
		payloadSize: len(pk.Payload), truncated: truncated,
		qos: pk.FixedHeader.Qos, retain: pk.FixedHeader.Retain,
		clientID: clientID, username: username, printable: isPrintableUTF8(payload),
	}
	select {
	case h.app.messageLogCh <- record:
	default:
		h.app.droppedLogs.Add(1)
	}
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}

// runPersistence moves broker audit writes off Mochi's packet-processing
// goroutines. The bounded queues keep a traffic burst from consuming
// unbounded memory; dropped audit rows are exposed by mqtt_status.
func (a *App) runPersistence(ctx context.Context) error {
	for {
		messages := make([]messageRecord, 0, 100)
		acls := make([]aclRecord, 0, 100)
		select {
		case <-ctx.Done():
			return nil
		case rec := <-a.messageLogCh:
			messages = append(messages, rec)
		case rec := <-a.aclLogCh:
			acls = append(acls, rec)
		}
		for len(messages)+len(acls) < 100 {
			select {
			case rec := <-a.messageLogCh:
				messages = append(messages, rec)
			case rec := <-a.aclLogCh:
				acls = append(acls, rec)
			default:
				goto persist
			}
		}
	persist:
		if err := a.persistRecords(messages, acls); err != nil {
			a.droppedLogs.Add(uint64(len(messages) + len(acls)))
			a.ctx.Logger().Warn("persist mqtt audit batch", "err", err.Error())
		}
	}
}

func (a *App) persistRecords(messages []messageRecord, acls []aclRecord) error {
	tx, err := a.ctx.AppDB().Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, rec := range messages {
		if _, err := tx.Exec(
			`INSERT INTO mqtt_message_log(topic, payload, payload_size, payload_truncated, qos, retain, client_id, username, is_printable)
			 VALUES (?,?,?,?,?,?,?,?,?)`,
			rec.topic, rec.payload, rec.payloadSize, boolToInt(rec.truncated), rec.qos, boolToInt(rec.retain), rec.clientID, rec.username, boolToInt(rec.printable)); err != nil {
			return err
		}
	}
	for _, rec := range acls {
		if _, err := tx.Exec(
			`INSERT INTO mqtt_acl_log(username, action, topic, allowed, reason) VALUES (?,?,?,?,?)`,
			rec.username, rec.action, rec.topic, boolToInt(rec.allowed), rec.reason); err != nil {
			return err
		}
	}
	return tx.Commit()
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
		a.emitBusMessage("message", cl, pk)
	}); err != nil {
		return err
	}

	subs, err := listAllBusSubscriptions(a.ctx.AppDB())
	if err != nil {
		return err
	}
	for _, s := range subs {
		s := s
		_ = b.Subscribe(s.TopicPattern, int(100+s.ID), func(cl *mqtt.Client, sub packets.Subscription, pk packets.Packet) {
			a.emitBusMessageForProject(s.BusTopic, s.ProjectID, cl, pk)
		})
	}
	return nil
}

// emitBusMessage shapes an MQTT packet into a platform event payload.
// Strings stay strings (UTF-8 cheap to inspect), binary becomes a
// length hint instead of bloating the bus with base64.
func (a *App) emitBusMessage(busTopic string, cl *mqtt.Client, pk packets.Packet) {
	a.emitBusMessageForProject(busTopic, projectScope(a.ctx), cl, pk)
}

func (a *App) emitBusMessageForProject(busTopic, projectID string, cl *mqtt.Client, pk packets.Packet) {
	if a.eventLimiter != nil && !a.eventLimiter.allow() {
		a.droppedEvents.Add(1)
		return
	}
	maxEventPayload := clampConfigInt(a.ctx, "max_event_payload_bytes", 131072, 0, 268435455)
	visiblePayload := pk.Payload
	truncated := len(visiblePayload) > maxEventPayload
	if truncated {
		visiblePayload = visiblePayload[:maxEventPayload]
	}
	payloadField := any(string(visiblePayload))
	if !isPrintableUTF8(visiblePayload) {
		binary := map[string]any{
			"binary":     true,
			"size_bytes": len(pk.Payload),
		}
		if len(visiblePayload) > 0 {
			binary["base64"] = base64.StdEncoding.EncodeToString(visiblePayload)
		} else {
			binary["omitted"] = true
		}
		if truncated {
			binary["truncated"] = true
		}
		payloadField = binary
	} else if truncated {
		payloadField = map[string]any{
			"text_preview": string(visiblePayload),
			"size_bytes":   len(pk.Payload),
			"truncated":    true,
		}
	}
	clientID, username := a.mqttIdentity(cl, pk)
	a.ctx.EmitWithProject("mqtt."+busTopic, projectID, map[string]any{
		"topic":     pk.TopicName,
		"payload":   payloadField,
		"qos":       int(pk.FixedHeader.Qos),
		"retain":    pk.FixedHeader.Retain,
		"client_id": clientID,
		"username":  username,
	})
	a.eventsEmitted.Add(1)
}

func (a *App) mqttIdentity(cl *mqtt.Client, pk packets.Packet) (clientID, username string) {
	clientID = pk.Origin
	if cl != nil && !cl.Net.Inline {
		cl.RLock()
		defer cl.RUnlock()
		if cl.ID != "" {
			clientID = cl.ID
		}
		return clientID, string(cl.Properties.Username)
	}
	if identity, ok := a.clientIdentities.Load(clientID); ok {
		return clientID, identity.(*connectedClientIdentity).username
	}
	return clientID, ""
}

// ─── outbound platform→MQTT bridge ──────────────────────────────────

// EventHandlers returns subscriptions on platform-bus topics this
// app cares about. mqtt.publish_request lets sibling apps publish
// to MQTT without speaking the protocol themselves: emit
//
//	ctx.Emit("mqtt.publish_request", {topic, payload, retain?, qos?})
//
// and we forward into the broker.
//
// (The SDK calls this method during framework wiring; the handler
// runs on the framework's dispatcher goroutine.)
//
// (See main.go where EventHandlers() is overridden to surface this.)
func (a *App) handleOutboundPublishRequest(_ *sdk.AppCtx, evt map[string]any) error {
	if a.broker == nil {
		return fmt.Errorf("broker not running yet")
	}
	topic, _ := evt["topic"].(string)
	p, hasPayload := evt["payload"]
	payload, err := encodePayload(p, hasPayload)
	if err != nil {
		return fmt.Errorf("encode payload: %w", err)
	}
	retain, _ := evt["retain"].(bool)
	qos := intArg(evt, "qos", 0)
	if err := validatePublish(topic, qos); err != nil {
		return err
	}
	if err := a.validatePayloadSize(payload); err != nil {
		return err
	}
	return a.broker.Publish(topic, payload, retain, byte(qos))
}
