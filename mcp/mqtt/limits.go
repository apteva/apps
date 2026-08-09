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
	if burst < rate {
		burst = rate
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
	return b == mqtt.OnPublish || b == mqtt.OnDisconnect
}

func (h *publishGuardHook) OnPublish(cl *mqtt.Client, pk packets.Packet) (packets.Packet, error) {
	if cl == nil || cl.Net.Inline || h.rate <= 0 {
		return pk, nil
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
		// CodeSuccessIgnore keeps the connection healthy and lets Mochi send
		// the normal QoS acknowledgement while skipping retain/routing hooks.
		// Returning the MQTT-5-only rate error would not suppress QoS 0 or
		// MQTT 3.1.1 publishes in Mochi's processing path.
		return pk, packets.CodeSuccessIgnore
	}
	return pk, nil
}

func (h *publishGuardHook) OnDisconnect(cl *mqtt.Client, _ error, _ bool) {
	h.mu.Lock()
	delete(h.clients, cl)
	h.mu.Unlock()
}
