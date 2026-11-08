package main

import (
	"context"
	"crypto/tls"
	"log/slog"
	"net"
	"sync"
	"time"

	"github.com/mochi-mqtt/server/v2/listeners"
	"golang.org/x/crypto/acme"
)

// Handshake before broker admission, with bounded concurrent handshakes and a
// deadline. A TCP peer cannot hold unlimited pre-authentication TLS goroutines.
type mqttTLSListener struct {
	listener net.Listener
	config   *tls.Config
	timeout  time.Duration
	slots    chan struct{}
	mu       sync.Mutex
	closed   bool
	conns    map[net.Conn]struct{}
	wg       sync.WaitGroup
}

func (l *mqttTLSListener) ID() string              { return "tls1" }
func (l *mqttTLSListener) Address() string         { return l.listener.Addr().String() }
func (l *mqttTLSListener) Protocol() string        { return "tls" }
func (l *mqttTLSListener) Init(*slog.Logger) error { return nil }

func (l *mqttTLSListener) Serve(establish listeners.EstablishFn) {
	for {
		raw, err := l.listener.Accept()
		if err != nil {
			return
		}
		l.mu.Lock()
		if l.closed {
			l.mu.Unlock()
			raw.Close()
			return
		}
		select {
		case l.slots <- struct{}{}:
		default:
			l.mu.Unlock()
			raw.Close()
			continue
		}
		l.conns[raw] = struct{}{}
		l.wg.Add(1)
		l.mu.Unlock()
		go func() {
			defer l.wg.Done()
			defer raw.Close()
			defer func() { l.mu.Lock(); delete(l.conns, raw); l.mu.Unlock() }()
			secure := tls.Server(raw, l.config)
			var released sync.Once
			release := func() { released.Do(func() { <-l.slots }) }
			defer release()
			ctx, cancel := context.WithTimeout(context.Background(), l.timeout)
			_ = raw.SetDeadline(time.Now().Add(l.timeout))
			err := secure.HandshakeContext(ctx)
			cancel()
			if err != nil || secure.ConnectionState().NegotiatedProtocol == acme.ALPNProto {
				return
			}
			// A verified TLS handshake is not MQTT authentication. Bound the CONNECT
			// wait too; the broker refreshes deadlines after parsing a valid CONNECT.
			_ = raw.SetDeadline(time.Now().Add(l.timeout))
			_ = establish(l.ID(), &tlsAdmissionConn{Conn: secure, authenticated: release})
		}()
	}
}

type tlsAdmissionConn struct {
	net.Conn
	authenticated func()
}

func (l *mqttTLSListener) Close(closeClients listeners.CloseFn) {
	l.mu.Lock()
	if l.closed {
		l.mu.Unlock()
		return
	}
	l.closed = true
	_ = l.listener.Close()
	for conn := range l.conns {
		_ = conn.Close()
	}
	l.mu.Unlock()
	closeClients(l.ID())
	l.wg.Wait()
}
