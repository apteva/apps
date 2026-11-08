package main

import (
	"sync"
	"time"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/packets"
)

// tokenBucket is a small in-process limiter used to protect the broker hooks
// and the platform event bus. A zero rate disables the limit.
type tokenBucket struct {
	mu     sync.Mutex
	rate   float64
	burst  float64
	tokens float64
	last   time.Time
}

func newTokenBucket(rate, burst int) *tokenBucket {
	if rate <= 0 {
		return &tokenBucket{}
	}
	if burst < 1 {
		burst = 1
	}
	now := time.Now()
	return &tokenBucket{rate: float64(rate), burst: float64(burst), tokens: float64(burst), last: now}
}

func (b *tokenBucket) allow() bool {
	if b == nil || b.rate <= 0 {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	now := time.Now()
	b.tokens += now.Sub(b.last).Seconds() * b.rate
	if b.tokens > b.burst {
		b.tokens = b.burst
	}
	b.last = now
	if b.tokens < 1 {
		return false
	}
	b.tokens--
	return true
}

type publishGuardHook struct {
	mqtt.HookBase
	app *App

	mu      sync.Mutex
	clients map[*mqtt.Client]*tokenBucket
	rate    int
	burst   int
}

func newPublishGuardHook(app *App) *publishGuardHook {
	rate := clampConfigInt(app.ctx, "max_publish_per_second", 100, 0, 1000000)
	burst := clampConfigInt(app.ctx, "max_publish_burst", 200, 1, 10000000)
	return &publishGuardHook{app: app, clients: map[*mqtt.Client]*tokenBucket{}, rate: rate, burst: burst}
}

func (h *publishGuardHook) ID() string { return "apteva-publish-guard" }

func (h *publishGuardHook) Provides(b byte) bool {
	return b == mqtt.OnPacketRead || b == mqtt.OnDisconnect || b == mqtt.OnPublishDropped
}

func (h *publishGuardHook) OnPacketRead(cl *mqtt.Client, pk packets.Packet) (packets.Packet, error) {
	if cl == nil || cl.Net.Inline || pk.FixedHeader.Type != packets.Publish {
		return pk, nil
	}
	identity := h.app.clientIdentity(cl)
	if identity == nil {
		return pk, packets.ErrRejectPacket
	}
	h.mu.Lock()
	limiter := h.clients[cl]
	if limiter == nil {
		limiter = newTokenBucket(h.rate, h.burst)
		h.clients[cl] = limiter
	}
	h.mu.Unlock()
	if !limiter.allow() {
		h.app.rateLimitedMessages.Add(1)
		h.app.counters(identity.scope).rateLimited.Add(1)
		h.reject(cl)
		return pk, packets.ErrRejectPacket
	}
	if err := h.app.admitScope(identity.scope); err != nil {
		h.reject(cl)
		return pk, packets.ErrRejectPacket
	}
	return pk, nil
}

func (h *publishGuardHook) reject(cl *mqtt.Client) {
	// A read rejection terminates processing before retain, routing and PUBACK /
	// PUBREC for every QoS/protocol combination. Ordinary hook errors are ignored
	// by Mochi. MQTT 3.1.1 has no negative PUBACK or server DISCONNECT packet.
	if cl.Properties.ProtocolVersion == 5 && cl.Net.Conn != nil {
		_ = cl.Net.Conn.SetWriteDeadline(time.Now().Add(overloadWriteTimeout))
		_ = cl.WritePacket(packets.Packet{FixedHeader: packets.FixedHeader{Type: packets.Disconnect}, ReasonCode: packets.ErrQuotaExceeded.Code, Properties: packets.Properties{ReasonString: "Publish capacity exhausted; reconnect with backoff and retry unacknowledged work"}})
	}
	cl.Stop(packets.ErrQuotaExceeded)
}

// Make outbound queue exhaustion visible to the affected subscriber. This does
// not make publisher acceptance a durable subscriber-delivery acknowledgment.
func (h *publishGuardHook) OnPublishDropped(cl *mqtt.Client, _ packets.Packet) {
	if cl != nil && !cl.Net.Inline {
		h.reject(cl)
	}
}

func (h *publishGuardHook) OnDisconnect(cl *mqtt.Client, _ error, _ bool) {
	h.mu.Lock()
	delete(h.clients, cl)
	h.mu.Unlock()
}
