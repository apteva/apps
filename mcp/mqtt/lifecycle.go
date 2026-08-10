package main

import (
	"time"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/packets"
)

type lifecycleHook struct {
	mqtt.HookBase
	app *App
}

type connectedClientIdentity struct {
	client   *mqtt.Client
	username string
}

func (h *lifecycleHook) ID() string { return "apteva-lifecycle" }

func (h *lifecycleHook) Provides(b byte) bool {
	return b == mqtt.OnSessionEstablished || b == mqtt.OnDisconnect
}

func (h *lifecycleHook) OnSessionEstablished(cl *mqtt.Client, _ packets.Packet) {
	if cl == nil || cl.Net.Inline {
		return
	}
	connectedAt := time.Now().UTC()
	h.app.clientConnectedAt.Store(cl, connectedAt)
	payload := clientEventPayload(cl)
	identity := &connectedClientIdentity{client: cl, username: payload["username"].(string)}
	h.app.clientIdentities.Store(cl.ID, identity)
	payload["connected_at"] = connectedAt.Format(time.RFC3339Nano)
	h.app.ctx.Emit("mqtt.client.connected", payload)
	h.app.eventsEmitted.Add(1)
}

func (h *lifecycleHook) OnDisconnect(cl *mqtt.Client, err error, expire bool) {
	if cl == nil || cl.Net.Inline {
		return
	}
	payload := clientEventPayload(cl)
	if identity, ok := h.app.clientIdentities.Load(cl.ID); ok {
		if identity.(*connectedClientIdentity).client == cl {
			h.app.clientIdentities.CompareAndDelete(cl.ID, identity)
		}
	}
	if connectedAt, ok := h.app.clientConnectedAt.LoadAndDelete(cl); ok {
		payload["connected_at"] = connectedAt.(time.Time).Format(time.RFC3339Nano)
	}
	payload["disconnected_at"] = time.Now().UTC().Format(time.RFC3339Nano)
	payload["session_expired"] = expire
	if err != nil {
		payload["reason"] = err.Error()
	} else {
		payload["reason"] = "client disconnected"
	}
	h.app.ctx.Emit("mqtt.client.disconnected", payload)
	h.app.eventsEmitted.Add(1)
}

func clientEventPayload(cl *mqtt.Client) map[string]any {
	cl.RLock()
	defer cl.RUnlock()
	return map[string]any{
		"client_id":        cl.ID,
		"username":         string(cl.Properties.Username),
		"remote":           cl.Net.Remote,
		"listener":         cl.Net.Listener,
		"protocol_version": int(cl.Properties.ProtocolVersion),
		"clean_session":    cl.Properties.Clean,
	}
}
