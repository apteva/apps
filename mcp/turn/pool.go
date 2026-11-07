package main

import (
	"errors"
	"net"
	"os"
	"strconv"
	"sync"
	"time"
)

// Instances' native backend verifies that every declared port is bound by the
// execution PID, including while standby/drained and between allocations. A
// permanent socket owns each relay port; allocations lease its datagram stream.
type relayPort struct {
	socket net.PacketConn
	number int
	mu     sync.Mutex
	lease  *poolLease
	app    *App
}

type datagram struct {
	payload []byte
	address net.Addr
}

func (a *App) openPool() error {
	var ports []*relayPort
	for number := relayPortMin; number <= relayPortMax; number++ {
		socket, err := net.ListenPacket("udp4", net.JoinHostPort(a.cfg.bindIP, strconv.Itoa(number)))
		if err != nil {
			for _, p := range ports {
				_ = p.socket.Close()
			}
			return errors.New("could not bind the complete reserved UDP relay pool")
		}
		ports = append(ports, &relayPort{socket: socket, number: number, app: a})
	}
	a.pool = ports
	for _, p := range ports {
		a.poolReaders.Add(1)
		go func() { defer a.poolReaders.Done(); p.read() }()
	}
	return nil
}

func (p *relayPort) read() {
	buffer := make([]byte, 65535)
	for {
		n, addr, err := p.socket.ReadFrom(buffer)
		if err != nil {
			return
		}
		p.mu.Lock()
		lease := p.lease
		p.mu.Unlock()
		if lease == nil {
			continue
		}
		// Bound queued memory even under a stream of oversized UDP datagrams.
		if n > 16384 {
			p.app.stats.dropped.Add(uint64(n))
			continue
		}
		packet := datagram{payload: append([]byte(nil), buffer[:n]...), address: addr}
		select {
		case <-lease.closed:
		case lease.incoming <- packet:
		default:
			p.app.stats.dropped.Add(uint64(n))
		}
	}
}

func (p *relayPort) acquire() *poolLease {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.lease != nil {
		return nil
	}
	lease := &poolLease{port: p, incoming: make(chan datagram, 32), closed: make(chan struct{}), deadlineChanged: make(chan struct{}, 1)}
	p.lease = lease
	return lease
}

type poolLease struct {
	port                        *relayPort
	incoming                    chan datagram
	closed                      chan struct{}
	once                        sync.Once
	mu                          sync.Mutex
	readDeadline, writeDeadline time.Time
	deadlineChanged             chan struct{}
}

func (c *poolLease) ReadFrom(buffer []byte) (int, net.Addr, error) {
	for {
		c.mu.Lock()
		deadline := c.readDeadline
		c.mu.Unlock()
		var timeout <-chan time.Time
		var timer *time.Timer
		if !deadline.IsZero() {
			timer = time.NewTimer(time.Until(deadline))
			timeout = timer.C
		}
		stopTimer := func() {
			if timer != nil {
				timer.Stop()
			}
		}
		select {
		case <-c.closed:
			stopTimer()
			return 0, nil, net.ErrClosed
		case <-c.deadlineChanged:
			stopTimer()
			continue
		case <-timeout:
			return 0, nil, os.ErrDeadlineExceeded
		case packet := <-c.incoming:
			stopTimer()
			n := copy(buffer, packet.payload)
			return n, packet.address, nil
		}
	}
}

func (c *poolLease) WriteTo(buffer []byte, addr net.Addr) (int, error) {
	c.mu.Lock()
	deadline := c.writeDeadline
	c.mu.Unlock()
	if deadline.IsZero() {
		deadline = time.Now().Add(5 * time.Second)
	}
	c.port.mu.Lock()
	defer c.port.mu.Unlock()
	if c.port.lease != c {
		return 0, net.ErrClosed
	}
	if err := c.port.socket.SetWriteDeadline(deadline); err != nil {
		return 0, err
	}
	return c.port.socket.WriteTo(buffer, addr)
}

func (c *poolLease) Close() error {
	c.once.Do(func() {
		c.port.mu.Lock()
		if c.port.lease == c {
			c.port.lease = nil
		}
		close(c.closed)
		c.port.mu.Unlock()
	})
	return nil
}

func (c *poolLease) LocalAddr() net.Addr { return c.port.socket.LocalAddr() }
func (c *poolLease) SetDeadline(t time.Time) error {
	_ = c.SetWriteDeadline(t)
	return c.SetReadDeadline(t)
}
func (c *poolLease) SetWriteDeadline(t time.Time) error {
	c.mu.Lock()
	c.writeDeadline = t
	c.mu.Unlock()
	return nil
}
func (c *poolLease) SetReadDeadline(t time.Time) error {
	c.mu.Lock()
	c.readDeadline = t
	c.mu.Unlock()
	select {
	case c.deadlineChanged <- struct{}{}:
	default:
	}
	return nil
}
