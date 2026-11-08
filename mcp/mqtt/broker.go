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
	"sync"
	"time"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/listeners"
	"github.com/mochi-mqtt/server/v2/packets"
)

type Broker struct {
	server       *mqtt.Server
	port         int
	address      string
	app          *App
	transport    transportConfig
	certificates *certificateService
	closeOnce    sync.Once
	closeErr     error
}

// NewBroker constructs a server with hooks wired up and the TCP
// listener bound. Doesn't start serving yet — that's the worker.
func NewBroker(a *App) (*Broker, error) {
	transport, err := parseTransport(a.ctx)
	if err != nil {
		return nil, err
	}
	if err := a.initAdmission(); err != nil {
		return nil, err
	}
	capabilities := mqtt.NewDefaultServerCapabilities()
	capabilities.MaximumClients = int64(clampConfigInt(a.ctx, "max_clients", 1000, 1, 1000000))
	capabilities.MaximumPacketSize = uint32(clampConfigInt(a.ctx, "max_payload_bytes", 1048576, 1024, 268435455))
	capabilities.ReceiveMaximum = uint16(clampConfigInt(a.ctx, "max_inflight_per_client", 128, 1, 65535))
	capabilities.MaximumInflight = capabilities.ReceiveMaximum
	capabilities.MaximumClientWritesPending = int32(clampConfigInt(a.ctx, "max_pending_writes_per_client", 1024, 1, 1048576))
	server := mqtt.New(&mqtt.Options{
		InlineClient: true, // needed for the bus loopback subscriber.
		Capabilities: capabilities,
	})

	br := &Broker{server: server, app: a, transport: transport}

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
	if err := server.AddHook(&lifecycleHook{app: a}, nil); err != nil {
		return nil, fmt.Errorf("lifecycle hook: %w", err)
	}
	if err := server.AddHook(newPublishGuardHook(a), nil); err != nil {
		return nil, fmt.Errorf("publish guard hook: %w", err)
	}
	if err := server.AddHook(&retainedHook{app: a}, nil); err != nil {
		return nil, fmt.Errorf("retained storage hook: %w", err)
	}

	if transport.mode == "tls" {
		certificates, tlsConfig, err := newCertificateService(transport)
		if err != nil {
			return nil, err
		}
		br.certificates = certificates
		listener, err := net.Listen("tcp", net.JoinHostPort(transport.bind, strconv.Itoa(transport.port)))
		if err != nil {
			return nil, err
		}
		secure := &mqttTLSListener{listener: listener, config: tlsConfig, timeout: time.Duration(transport.handshakeSeconds) * time.Second, slots: make(chan struct{}, transport.maxHandshakes), conns: map[net.Conn]struct{}{}}
		if err := server.AddListener(secure); err != nil {
			listener.Close()
			return nil, err
		}
		br.address = secure.Address()
		_, actual, _ := net.SplitHostPort(br.address)
		br.port, _ = strconv.Atoi(actual)
	} else {
		br.port, br.address, err = attachTCPListener(server, a, transport.port)
		if err != nil {
			_ = server.Close()
			return nil, err
		}
	}
	if br.transport.publicPort == 0 {
		br.transport.publicPort = br.port
	}
	return br, nil
}

func attachTCPListener(server *mqtt.Server, a *App, port int) (int, string, error) {
	cfg := listeners.Config{Type: "tcp", ID: "tcp1", Address: bindAddress(a, port)}
	tcpListener := listeners.NewTCP(cfg)
	if err := server.AddListener(tcpListener); err != nil {
		return 0, "", fmt.Errorf("add listener %s: %w", cfg.Address, err)
	}
	_, actualPort, err := net.SplitHostPort(tcpListener.Address())
	if err != nil {
		return 0, "", fmt.Errorf("resolve listener address %s: %w", tcpListener.Address(), err)
	}
	resolved, err := strconv.Atoi(actualPort)
	if err != nil {
		return 0, "", fmt.Errorf("resolve listener port %s: %w", actualPort, err)
	}
	return resolved, tcpListener.Address(), nil
}

func (b *Broker) Port() int       { return b.port }
func (b *Broker) Address() string { return b.address }

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

	// Mochi's Serve starts goroutines and returns. Keep this supervised worker
	// alive until cancellation so all sockets and pending TLS work are closed.
	if err := b.server.Serve(); err != nil {
		_ = b.Close()
		return err
	}
	<-ctx.Done()
	return b.Close()
}

func (b *Broker) Close() error {
	b.closeOnce.Do(func() {
		if b.certificates != nil {
			b.certificates.cancel()
		}
		if b.server != nil {
			b.closeErr = b.server.Close()
		}
	})
	return b.closeErr
}

// Publish — used by MCP tool + HTTP handler + inbound bus bridge.
func (b *Broker) Publish(topic string, payload []byte, retain bool, qos byte) error {
	if err := b.app.validatePayloadSize(payload); err != nil {
		return err
	}
	if err := b.app.admitScope(topicScope(topic)); err != nil {
		return err
	}
	return b.server.Publish(topic, payload, retain, qos)
}

// Subscribe — wraps the inline subscription. Filter id is caller-managed;
// pass distinct ints to register multiple subs against the same filter.
func (b *Broker) Subscribe(filter string, subID int, fn func(cl *mqtt.Client, sub packets.Subscription, pk packets.Packet)) error {
	return b.server.Subscribe(filter, subID, fn)
}

func (b *Broker) Unsubscribe(filter string, subID int) error {
	return b.server.Unsubscribe(filter, subID)
}

func (b *Broker) Clients() []*mqtt.Client {
	if b == nil || b.server == nil {
		return nil
	}
	out := make([]*mqtt.Client, 0, b.server.Clients.Len())
	for _, client := range b.server.Clients.GetAll() {
		if client.Net.Inline || client.Closed() {
			continue
		}
		out = append(out, client)
	}
	return out
}

func (b *Broker) DeleteRetained(topic string) error {
	if b == nil || b.server == nil {
		return nil
	}
	// MQTT's retained-delete operation is a retained publish with an empty
	// payload. Going through Publish notifies subscribers and the storage hook,
	// keeping clients, memory, and SQLite in sync.
	return b.Publish(topic, nil, true, 0)
}

func bindAddress(a *App, port int) string {
	iface := configString(a.ctx, "bind_interface", "127.0.0.1")
	// parseTransport has validated the runtime configuration before binding.
	if ip := net.ParseIP(iface); ip != nil {
		return net.JoinHostPort(iface, strconv.Itoa(port))
	}
	return net.JoinHostPort("127.0.0.1", strconv.Itoa(port))
}
