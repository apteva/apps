package main

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net"
	"net/netip"
	"strconv"
	"sync"
	"time"

	"github.com/pion/logging"
	"github.com/pion/turn/v5"
	"golang.org/x/time/rate"
)

func (a *App) startNetwork() error {
	address := func(port int) string { return net.JoinHostPort(a.cfg.bindIP, strconv.Itoa(port)) }
	tlsAddresses := make([]string, 0, len(a.cfg.tlsPorts))
	for _, port := range a.cfg.tlsPorts {
		tlsAddresses = append(tlsAddresses, address(port))
	}
	return a.listenListeners(address(a.cfg.udpPort), address(a.cfg.tcpPort), tlsAddresses)
}

// Tests use ephemeral listeners without changing the advertised configuration.
func (a *App) listenNetwork(address string) error {
	tlsAddress := net.JoinHostPort(a.cfg.bindIP, strconv.Itoa(a.cfg.tlsPorts[0]))
	if _, port, _ := net.SplitHostPort(address); port == "0" {
		tlsAddress = net.JoinHostPort(a.cfg.bindIP, "0")
	}
	return a.listenListeners(address, address, []string{tlsAddress})
}

// Every configured listener and the media pool must bind before readiness.
// Roll back every socket on failure, including a later TLS listener conflict.
func (a *App) listenListeners(udpAddress, tcpAddress string, tlsAddresses []string) (err error) {
	udp, err := net.ListenPacket("udp4", udpAddress)
	if err != nil {
		return fmt.Errorf("could not bind TURN UDP %s: %w", udpAddress, err)
	}
	var listeners []*clientListener
	defer func() {
		if err == nil {
			return
		}
		_ = udp.Close()
		for _, listener := range listeners {
			_ = listener.Close()
		}
		for _, port := range a.pool {
			_ = port.socket.Close()
		}
		a.poolReaders.Wait()
	}()
	tcp, err := net.Listen("tcp4", tcpAddress)
	if err != nil {
		return fmt.Errorf("could not bind TURN TCP %s: %w", tcpAddress, err)
	}
	a.udp, a.tcp = udp, newClientListener(tcp, a, nil)
	listeners = append(listeners, a.tcp)
	for _, address := range tlsAddresses {
		socket, bindErr := net.Listen("tcp4", address)
		if bindErr != nil {
			return fmt.Errorf("could not bind TURN TLS %s: %w", address, bindErr)
		}
		listener := newClientListener(socket, a, a.liveTLSConfig())
		listeners = append(listeners, listener)
		a.tlsListeners = append(a.tlsListeners, listener)
	}
	if len(a.tlsListeners) > 0 {
		a.tls = a.tlsListeners[0]
	}
	if err = a.openPool(); err != nil {
		return err
	}
	generator := &relayGenerator{app: a}
	permission := func(_ net.Addr, ip net.IP) bool { return a.allowPeer(ip) }
	factory := logging.NewDefaultLoggerFactory()
	factory.DefaultLogLevel = logging.LogLevelDisabled
	configs := make([]turn.ListenerConfig, 0, len(listeners))
	for _, listener := range listeners {
		configs = append(configs, turn.ListenerConfig{Listener: listener, RelayAddressGenerator: generator, PermissionHandler: permission})
	}
	server, err := turn.NewServer(turn.ServerConfig{
		Realm: a.cfg.realm, AuthHandler: a.authenticate, LoggerFactory: factory,
		QuotaHandler: func(user, _ string, _ net.Addr) bool {
			a.mu.Lock()
			defer a.mu.Unlock()
			return a.admittingLocked(time.Now()) && len(a.relays) < a.cfg.maxAllocations && a.users[user] < a.cfg.perCredential
		},
		AllocationLifetime: 10 * time.Minute,
		PacketConnConfigs:  []turn.PacketConnConfig{{PacketConn: &admissionPacketConn{PacketConn: udp, app: a}, RelayAddressGenerator: generator, PermissionHandler: permission}},
		ListenerConfigs:    configs,
	})
	if err != nil {
		return errors.New("TURN server initialization failed")
	}
	a.mu.Lock()
	a.server = server
	a.mu.Unlock()
	return nil
}

func (a *App) allowPeer(ip net.IP) bool {
	if a.peerFilter != nil {
		return a.peerFilter(ip)
	}
	addr, ok := netip.AddrFromSlice(ip)
	return ok && addr.Unmap() != a.cfg.publicIP && a.cfg.peerAllowed(addr)
}

func (a *App) trafficAllowed() bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	return a.trafficAllowedLocked(time.Now())
}

func (a *App) stopNetwork() {
	a.stopOnce.Do(func() {
		a.mu.Lock()
		server := a.server
		a.server = nil
		relays := make([]*relayConn, 0, len(a.relays))
		for c := range a.relays {
			relays = append(relays, c)
		}
		a.mu.Unlock()
		if server != nil {
			_ = server.Close()
		} else {
			if a.udp != nil {
				_ = a.udp.Close()
			}
			if a.tcp != nil {
				_ = a.tcp.Close()
			}
			for _, listener := range a.tlsListeners {
				_ = listener.Close()
			}
		}
		for _, c := range relays {
			_ = c.Close()
		}
		for _, port := range a.pool {
			_ = port.socket.Close()
		}
		a.poolReaders.Wait()
	})
}

type admissionPacketConn struct {
	net.PacketConn
	app *App
}

func (c *admissionPacketConn) ReadFrom(p []byte) (int, net.Addr, error) {
	for {
		n, addr, err := c.PacketConn.ReadFrom(p)
		if err != nil {
			return n, addr, err
		}
		if c.app.trafficAllowed() {
			return n, addr, nil
		}
	}
}
func (c *admissionPacketConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	if !c.app.trafficAllowed() {
		return len(p), nil
	}
	return c.PacketConn.WriteTo(p, addr)
}

type clientListener struct {
	net.Listener
	app         *App
	mu          sync.Mutex
	closed      bool
	connections map[*clientConn]struct{}
	tlsConfig   *tls.Config
	accepted    chan net.Conn
	stop        chan struct{}
	closeOnce   sync.Once
}

func (l *clientListener) Accept() (net.Conn, error) {
	if l.tlsConfig != nil {
		select {
		case <-l.stop:
			return nil, net.ErrClosed
		case conn := <-l.accepted:
			return conn, nil
		}
	}
	return l.acceptPlain()
}

func newClientListener(listener net.Listener, app *App, cfg *tls.Config) *clientListener {
	l := &clientListener{Listener: listener, app: app, connections: make(map[*clientConn]struct{}), tlsConfig: cfg, accepted: make(chan net.Conn, 16), stop: make(chan struct{})}
	if cfg != nil {
		go l.acceptTLS()
	}
	return l
}

func (l *clientListener) acceptTLS() {
	for {
		conn, err := l.acceptPlain()
		if err != nil {
			return
		}
		go func() {
			secured := tls.Server(conn, l.tlsConfig)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			err := secured.HandshakeContext(ctx)
			cancel()
			if err != nil {
				_ = secured.Close()
				return
			}
			select {
			case <-l.stop:
				_ = secured.Close()
			case l.accepted <- secured:
			}
		}()
	}
}

func (l *clientListener) acceptPlain() (net.Conn, error) {
	for {
		conn, err := l.Listener.Accept()
		if err != nil {
			return nil, err
		}
		if !l.app.trafficAllowed() {
			_ = conn.Close()
			continue
		}
		l.mu.Lock()
		if l.closed || len(l.connections) >= 128 {
			l.mu.Unlock()
			_ = conn.Close()
			continue
		}
		c := &clientConn{Conn: conn, listener: l}
		l.connections[c] = struct{}{}
		l.mu.Unlock()
		return c, nil
	}
}

func (l *clientListener) Close() error {
	var err error
	l.closeOnce.Do(func() {
		close(l.stop)
		err = l.Listener.Close()
		l.mu.Lock()
		l.closed = true
		connections := make([]*clientConn, 0, len(l.connections))
		for c := range l.connections {
			connections = append(connections, c)
		}
		l.mu.Unlock()
		for _, c := range connections {
			_ = c.Close()
		}
	})
	return err
}

type clientConn struct {
	net.Conn
	listener *clientListener
	once     sync.Once
}

func (c *clientConn) Read(p []byte) (int, error) {
	if !c.listener.app.trafficAllowed() {
		_ = c.Close()
		return 0, net.ErrClosed
	}
	_ = c.Conn.SetReadDeadline(time.Now().Add(2 * time.Minute))
	n, err := c.Conn.Read(p)
	if !c.listener.app.trafficAllowed() {
		_ = c.Close()
		return 0, net.ErrClosed
	}
	return n, err
}
func (c *clientConn) Write(p []byte) (int, error) {
	if !c.listener.app.trafficAllowed() {
		return 0, net.ErrClosed
	}
	_ = c.Conn.SetWriteDeadline(time.Now().Add(5 * time.Second))
	return c.Conn.Write(p)
}
func (c *clientConn) Close() error {
	var err error
	c.once.Do(func() {
		err = c.Conn.Close()
		c.listener.mu.Lock()
		delete(c.listener.connections, c)
		c.listener.mu.Unlock()
	})
	return err
}

// The allocator is the authoritative admission/quota check. Unlike a separate
// QuotaHandler it reserves and counts each socket atomically, including across
// UDP and TCP client listeners and allocations racing a deployment drain.
type relayGenerator struct {
	app  *App
	next int
}

func (g *relayGenerator) Validate() error {
	if g.app == nil {
		return errors.New("relay app required")
	}
	return nil
}
func (g *relayGenerator) AllocateListener(turn.AllocateListenerConfig) (net.Listener, net.Addr, error) {
	return nil, nil, errors.New("TCP relay allocations are not supported")
}
func (g *relayGenerator) AllocateConn(turn.AllocateConnConfig) (net.Conn, error) {
	return nil, errors.New("TCP relay allocations are not supported")
}
func (g *relayGenerator) AllocatePacketConn(request turn.AllocateListenerConfig) (net.PacketConn, net.Addr, error) {
	a := g.app
	if request.Network != "udp4" {
		return nil, nil, errors.New("only IPv4 UDP relay allocations are supported")
	}
	if request.RequestedPort != 0 && (request.RequestedPort < relayPortMin || request.RequestedPort > relayPortMax) {
		return nil, nil, errors.New("requested relay port is outside the reserved pool")
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	if !a.admittingLocked(time.Now()) || len(a.relays) >= a.cfg.maxAllocations || a.users[request.UserID] >= a.cfg.perCredential {
		return nil, nil, errNotAdmitting
	}
	for i := 0; i < len(a.pool); i++ {
		index := g.next % len(a.pool)
		g.next++
		if request.RequestedPort != 0 {
			index = request.RequestedPort - relayPortMin
		}
		port := a.pool[index]
		conn := port.acquire()
		if conn == nil {
			if request.RequestedPort != 0 {
				break
			}
			continue
		}
		wrapped := &relayConn{PacketConn: conn, app: a, user: request.UserID, limiter: rate.NewLimiter(rate.Limit(a.cfg.bytesPerSecond), 65536)}
		a.relays[wrapped] = struct{}{}
		a.users[request.UserID]++
		return wrapped, &net.UDPAddr{IP: net.IP(a.cfg.publicIP.AsSlice()), Port: port.number}, nil
	}
	return nil, nil, errors.New("no reserved relay port is available")
}

type relayConn struct {
	net.PacketConn
	app     *App
	user    string
	limiter *rate.Limiter
	once    sync.Once
}

func (c *relayConn) ReadFrom(p []byte) (int, net.Addr, error) {
	for {
		n, addr, err := c.PacketConn.ReadFrom(p)
		if err != nil {
			return n, addr, err
		}
		peer, ok := addr.(*net.UDPAddr)
		if !c.app.trafficAllowed() {
			return 0, nil, net.ErrClosed
		}
		if !ok || !c.app.allowPeer(peer.IP) || !c.limiter.AllowN(time.Now(), n) {
			c.app.stats.dropped.Add(uint64(n))
			continue
		}
		c.app.stats.received.Add(uint64(n))
		return n, addr, nil
	}
}
func (c *relayConn) WriteTo(p []byte, addr net.Addr) (int, error) {
	peer, ok := addr.(*net.UDPAddr)
	if !ok || !c.app.allowPeer(peer.IP) {
		return 0, errors.New("peer destination is not allowed")
	}
	if !c.app.trafficAllowed() {
		return 0, net.ErrClosed
	}
	if !c.limiter.AllowN(time.Now(), len(p)) {
		c.app.stats.dropped.Add(uint64(len(p)))
		return len(p), nil
	}
	n, err := c.PacketConn.WriteTo(p, addr)
	c.app.stats.sent.Add(uint64(n))
	return n, err
}
func (c *relayConn) Close() error {
	var err error
	c.once.Do(func() {
		err = c.PacketConn.Close()
		c.app.mu.Lock()
		delete(c.app.relays, c)
		c.app.users[c.user]--
		if c.app.users[c.user] <= 0 {
			delete(c.app.users, c.user)
		}
		c.app.mu.Unlock()
	})
	return err
}
