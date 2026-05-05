// broker.go — mochi-mqtt server setup, hooks, listener, lifecycle.
//
// One Broker per App; created in OnMount, served by the "broker"
// worker, closed in OnUnmount. The hook is the "aclHook" type below
// — it satisfies mochi's HookBase interface and consults users.go
// for connect/publish/subscribe authorisation. Inline-client +
// retained-message + persistent-session features all enabled.

package main

import (
	"context"
	"fmt"
	"net"
	"strconv"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/listeners"
	"github.com/mochi-mqtt/server/v2/packets"
)

type Broker struct {
	server *mqtt.Server
	port   int
	app    *App
}

// NewBroker constructs a server with hooks wired up and the TCP
// listener bound. Doesn't start serving yet — that's the worker.
func NewBroker(a *App) (*Broker, error) {
	server := mqtt.New(&mqtt.Options{
		InlineClient: true, // needed for the bus loopback subscriber.
	})

	br := &Broker{server: server, app: a}

	// AuthHook satisfies the broker's auth/ACL hook interface and
	// delegates to users.go.
	if err := server.AddHook(&aclHook{app: a}, nil); err != nil {
		return nil, fmt.Errorf("auth hook: %w", err)
	}
	// busHook captures every published message for the message log
	// and (in bus.go) the platform event-bus bridge.
	if err := server.AddHook(&busHook{app: a}, nil); err != nil {
		return nil, fmt.Errorf("bus hook: %w", err)
	}

	port, err := pickListenerPort(a)
	if err != nil {
		return nil, err
	}
	br.port = port

	cfg := listeners.Config{
		Type:    "tcp",
		ID:      "tcp1",
		Address: bindAddress(a, port),
	}
	if err := server.AddListener(listeners.NewTCP(cfg)); err != nil {
		return nil, fmt.Errorf("add listener %s: %w", cfg.Address, err)
	}
	return br, nil
}

func (b *Broker) Port() int { return b.port }

// Serve blocks on the broker's main loop. Worker stops it via Close
// when ctx is cancelled — mochi's Server doesn't take a Context, so
// we wire the cancellation translator here.
func (b *Broker) Serve(ctx context.Context) error {
	// Register the bus loopback as soon as we're serving (Subscribe
	// is safe pre-Serve too, but doing it here keeps the dependency
	// order clear: server up → bus bridge → traffic).
	if err := b.app.bridgeBusLoopback(b); err != nil {
		return fmt.Errorf("bus loopback: %w", err)
	}
	if err := b.app.bridgeHADiscovery(b); err != nil {
		b.app.ctx.Logger().Warn("ha discovery bridge", "err", err.Error())
	}

	errCh := make(chan error, 1)
	go func() { errCh <- b.server.Serve() }()
	select {
	case <-ctx.Done():
		_ = b.server.Close()
		return nil
	case err := <-errCh:
		return err
	}
}

func (b *Broker) Close() error {
	if b.server == nil {
		return nil
	}
	return b.server.Close()
}

// Publish — used by MCP tool + HTTP handler + inbound bus bridge.
func (b *Broker) Publish(topic string, payload []byte, retain bool, qos byte) error {
	return b.server.Publish(topic, payload, retain, qos)
}

// Subscribe — wraps the inline subscription. Filter id is caller-managed;
// pass distinct ints to register multiple subs against the same filter.
func (b *Broker) Subscribe(filter string, subID int, fn func(cl *mqtt.Client, sub packets.Subscription, pk packets.Packet)) error {
	return b.server.Subscribe(filter, subID, fn)
}

// pickListenerPort resolves config["listen_port"] to a free TCP port.
// Default 1883; 0 means kernel-assigned; on bind-conflict we fall
// back to a random port (same pattern torrent uses for ListenPort)
// so a stale broker doesn't make the install hang.
func pickListenerPort(a *App) (int, error) {
	want := configInt(a.ctx, "listen_port", 1883)
	if want < 0 {
		want = 1883
	}
	if want == 0 {
		// Caller asked for kernel-assigned. Bind once to find a port.
		l, err := net.Listen("tcp", bindAddress(a, 0))
		if err != nil {
			return 0, fmt.Errorf("free port: %w", err)
		}
		port := l.Addr().(*net.TCPAddr).Port
		_ = l.Close()
		return port, nil
	}
	// Probe whether the requested port is free; otherwise relocate.
	addr := bindAddress(a, want)
	if l, err := net.Listen("tcp", addr); err == nil {
		_ = l.Close()
		return want, nil
	} else {
		a.ctx.Logger().Warn("listen busy — relocating", "want", want, "err", err.Error())
		l2, err2 := net.Listen("tcp", bindAddress(a, 0))
		if err2 != nil {
			return 0, fmt.Errorf("relocate listen: %w", err2)
		}
		port := l2.Addr().(*net.TCPAddr).Port
		_ = l2.Close()
		return port, nil
	}
}

func bindAddress(a *App, port int) string {
	iface := configString(a.ctx, "bind_interface", "")
	if iface == "" {
		return ":" + strconv.Itoa(port)
	}
	// User-supplied interface name. We don't resolve it here — the
	// listener takes "host:port"; if they pass an interface name like
	// "en0" we'd need to lookup its IP. Keep it simple: only bind by
	// IP. Document this in apteva.yaml.
	if ip := net.ParseIP(iface); ip != nil {
		return net.JoinHostPort(iface, strconv.Itoa(port))
	}
	a.ctx.Logger().Warn("bind_interface must be an IP, falling back to all interfaces", "got", iface)
	return ":" + strconv.Itoa(port)
}
