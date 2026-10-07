package softphonebench

import (
	cryptorand "crypto/rand"
	"encoding/hex"
	"math/rand/v2"
	"net"
	"sync"
	"time"

	"github.com/pion/turn/v5"
)

// TURNFixture forces the native browser's encrypted media through a loopback
// relay. Its client-facing UDP socket shapes SRTP, RTCP, TURN encapsulation and
// control, including 28 bytes IPv4/UDP per datagram. Signaling TCP is separate.
// Setup is unrestricted until Arm; media never bypasses this socket.
type TURNFixture struct {
	server             *turn.Server
	conn               *shapedPacketConn
	Username, Password string
	Budgets            *WireBudgets
}

func NewTURNFixture(up, down Link, seed int64) (*TURNFixture, error) {
	c, err := net.ListenPacket("udp4", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	budgets := newWireBudgets(up.Kbps, down.Kbps)
	shaped := newShapedPacketConn(c, up, down, seed, budgets)
	secret := make([]byte, 16)
	if _, err = cryptorand.Read(secret); err != nil {
		shaped.Close()
		return nil, err
	}
	password := hex.EncodeToString(secret)
	const user, realm = "local-benchmark", "telephony-local"
	key := turn.GenerateAuthKey(user, realm, password)
	server, err := turn.NewServer(turn.ServerConfig{
		Realm: realm,
		AuthHandler: func(a *turn.RequestAttributes) (string, []byte, bool) {
			if a.Username == user {
				return user, key, true
			}
			return "", nil, false
		},
		PacketConnConfigs: []turn.PacketConnConfig{{PacketConn: shaped,
			RelayAddressGenerator: &turn.RelayAddressGeneratorStatic{RelayAddress: net.ParseIP("127.0.0.1"), Address: "127.0.0.1"},
			PermissionHandler:     func(_ net.Addr, ip net.IP) bool { return ip.IsLoopback() },
		}},
	})
	if err != nil {
		shaped.Close()
		return nil, err
	}
	return &TURNFixture{server: server, conn: shaped, Username: user, Password: password, Budgets: budgets}, nil
}
func (f *TURNFixture) URL() string { return "turn:" + f.conn.LocalAddr().String() + "?transport=udp" }
func (f *TURNFixture) Arm(at time.Time) {
	f.conn.mu.Lock()
	f.conn.epoch = at
	f.conn.mu.Unlock()
	f.Budgets.Arm(at)
}
func (f *TURNFixture) Stats() map[string]DatagramStats {
	f.conn.mu.Lock()
	defer f.conn.mu.Unlock()
	return map[string]DatagramStats{"up": f.conn.upStats, "down": f.conn.downStats}
}
func (f *TURNFixture) Close() { _ = f.server.Close(); _ = f.conn.Close() }

type DatagramStats struct {
	Packets            int64   `json:"packets"`
	WireBytes          int64   `json:"wire_bytes"`
	DeliveredPackets   int64   `json:"delivered_packets"`
	DeliveredWireBytes int64   `json:"delivered_wire_bytes"`
	DroppedPackets     int64   `json:"dropped_packets"`
	DroppedWireBytes   int64   `json:"dropped_wire_bytes"`
	MaxDelayMS         float64 `json:"max_delay_ms"`
	FirstAtMS          int64   `json:"first_at_ms"`
	LastAtMS           int64   `json:"last_at_ms"`
}
type datagram struct {
	data     []byte
	addr     net.Addr
	due      time.Time
	measured bool
}

// A shallow network queue drops excess datagrams; it cannot accumulate seconds
// of queued speech. UDP does not backpressure the producer like the TCP fixture.
type shapedPacketConn struct {
	net.PacketConn
	mu                        sync.Mutex
	epoch                     time.Time
	upStats, downStats        DatagramStats
	downSchedule              datagramSchedule
	up                        Link
	upRNG                     *rand.Rand
	budgets                   *WireBudgets
	incoming, outgoing, ready chan datagram
	done                      chan struct{}
	once                      sync.Once
	wg                        sync.WaitGroup
}
type datagramSchedule struct {
	free   time.Time
	link   Link
	rng    *rand.Rand
	budget *wireBudget
}

func (s *datagramSchedule) due(now time.Time, bytes int) (time.Time, bool) {
	extra := time.Duration((s.link.LatencyMS + s.rng.Float64()*s.link.JitterMS) * float64(time.Millisecond))
	if extra >= 200*time.Millisecond {
		return now.Add(extra), false
	}
	if s.budget != nil {
		due, accepted := s.budget.reserve(now, bytes+28, 200*time.Millisecond-extra)
		return due.Add(extra), accepted
	}
	free := s.free
	if free.Before(now) {
		free = now
	}
	next := free.Add(time.Duration(float64(bytes+28) * 8 / s.link.Kbps * float64(time.Millisecond)))
	due := next.Add(extra)
	if due.Sub(now) > 200*time.Millisecond {
		return due, false
	}
	s.free = next
	return due, true
}
func newShapedPacketConn(c net.PacketConn, up, down Link, seed int64, budgets ...*WireBudgets) *shapedPacketConn {
	p := &shapedPacketConn{PacketConn: c, up: up, upRNG: rand.New(rand.NewPCG(uint64(seed), 1)), downSchedule: datagramSchedule{link: down, rng: rand.New(rand.NewPCG(uint64(seed), 2))}, incoming: make(chan datagram, 64), outgoing: make(chan datagram, 64), ready: make(chan datagram, 64), done: make(chan struct{})}
	if len(budgets) > 0 {
		p.budgets = budgets[0]
		p.downSchedule.budget = p.budgets.Down
	}
	p.wg.Add(3)
	go p.readPackets()
	go p.deliver(p.incoming, true)
	go p.deliver(p.outgoing, false)
	return p
}
func (p *shapedPacketConn) measured(now time.Time) bool {
	return !p.epoch.IsZero() && !now.Before(p.epoch)
}
func (p *shapedPacketConn) observe(stats *DatagramStats, packet datagram, now time.Time, accepted bool) {
	if !packet.measured {
		return
	}
	stats.Packets++
	stats.WireBytes += int64(len(packet.data) + 28)
	if stats.FirstAtMS == 0 {
		stats.FirstAtMS = now.UnixMilli()
	}
	stats.LastAtMS = now.UnixMilli()
	if accepted {
		stats.MaxDelayMS = max(stats.MaxDelayMS, float64(packet.due.Sub(now))/float64(time.Millisecond))
	} else {
		stats.DroppedPackets++
		stats.DroppedWireBytes += int64(len(packet.data) + 28)
	}
}
func (p *shapedPacketConn) readPackets() {
	defer p.wg.Done()
	s := datagramSchedule{link: p.up, rng: p.upRNG}
	if p.budgets != nil {
		s.budget = p.budgets.Up
	}
	for {
		buf := make([]byte, 65536)
		n, a, err := p.PacketConn.ReadFrom(buf)
		if err != nil {
			return
		}
		now := time.Now()
		packet := datagram{data: buf[:n], addr: a, due: now}
		p.mu.Lock()
		packet.measured = p.measured(now)
		accepted := true
		if packet.measured {
			packet.due, accepted = s.due(now, n)
		}
		if accepted {
			select {
			case p.incoming <- packet:
			default:
				accepted = false
			}
		}
		p.observe(&p.upStats, packet, now, accepted)
		p.mu.Unlock()
	}
}
func (p *shapedPacketConn) WriteTo(data []byte, addr net.Addr) (int, error) {
	select {
	case <-p.done:
		return 0, net.ErrClosed
	default:
	}
	now := time.Now()
	packet := datagram{data: append([]byte(nil), data...), addr: addr, due: now}
	p.mu.Lock()
	packet.measured = p.measured(now)
	accepted := true
	if packet.measured {
		packet.due, accepted = p.downSchedule.due(now, len(data))
	}
	if accepted {
		select {
		case p.outgoing <- packet:
		default:
			accepted = false
		}
	}
	p.observe(&p.downStats, packet, now, accepted)
	p.mu.Unlock()
	return len(data), nil // network loss is not a sender failure
}
func (p *shapedPacketConn) deliver(queue <-chan datagram, up bool) {
	defer p.wg.Done()
	for {
		select {
		case <-p.done:
			return
		case packet := <-queue:
			timer := time.NewTimer(max(0, time.Until(packet.due)))
			select {
			case <-p.done:
				timer.Stop()
				return
			case <-timer.C:
			}
			delivered := false
			if up {
				select {
				case p.ready <- packet:
					delivered = true
				case <-p.done:
					return
				}
			} else {
				_, err := p.PacketConn.WriteTo(packet.data, packet.addr)
				delivered = err == nil
			}
			if packet.measured {
				p.mu.Lock()
				stats := &p.downStats
				if up {
					stats = &p.upStats
				}
				if delivered {
					stats.DeliveredPackets++
					stats.DeliveredWireBytes += int64(len(packet.data) + 28)
				} else {
					stats.DroppedPackets++
					stats.DroppedWireBytes += int64(len(packet.data) + 28)
				}
				p.mu.Unlock()
			}
		}
	}
}
func (p *shapedPacketConn) ReadFrom(data []byte) (int, net.Addr, error) {
	select {
	case <-p.done:
		return 0, nil, net.ErrClosed
	case packet := <-p.ready:
		return copy(data, packet.data), packet.addr, nil
	}
}
func (p *shapedPacketConn) Close() error {
	p.once.Do(func() { close(p.done); _ = p.PacketConn.Close(); p.wg.Wait() })
	return nil
}
