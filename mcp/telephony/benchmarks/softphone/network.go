// Package softphonebench is test infrastructure, not part of the app runtime.
package softphonebench

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math/rand"
	"net"
	"os"
	"sync"
	"time"
)

type Link struct {
	Kbps                float64 `json:"kbps"`
	LatencyMS           float64 `json:"latency_ms"`
	JitterMS            float64 `json:"jitter_ms"`
	RecoveryProbability float64 `json:"recovery_probability"`
	RecoveryMS          float64 `json:"recovery_ms"`
	OutageAtMS          float64 `json:"outage_at_ms"`
	OutageMS            float64 `json:"outage_ms"`
}
type Profile struct {
	Name          string  `json:"name"`
	Expectation   string  `json:"expectation"`
	Down          Link    `json:"down"`
	Up            Link    `json:"up"`
	MaxP95MS      float64 `json:"max_p95_ms"`
	MaxMissingPct float64 `json:"max_missing_pct"`
}

func Profiles(path string) ([]Profile, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var profiles []Profile
	if err = json.Unmarshal(raw, &profiles); err != nil {
		return nil, err
	}
	for _, p := range profiles {
		for _, l := range []Link{p.Down, p.Up} {
			if l.Kbps <= 0 || l.LatencyMS < 0 || l.JitterMS < 0 || l.RecoveryProbability < 0 || l.RecoveryProbability > 1 || l.RecoveryMS < 0 || l.OutageMS < 0 {
				return nil, fmt.Errorf("invalid profile %s", p.Name)
			}
		}
	}
	return profiles, nil
}

type LinkStats struct {
	BytesRead           int64   `json:"bytes_read"`
	BytesDelivered      int64   `json:"bytes_delivered"`
	Chunks              int64   `json:"chunks"`
	RecoveryEvents      int64   `json:"recovery_events"`
	MaxScheduledDelayMS float64 `json:"max_scheduled_delay_ms"`
}
type Schedule struct {
	Link                       Link
	RNG                        *rand.Rand
	SerialFree, LastDue, Epoch time.Time
}

func (s *Schedule) Due(now time.Time, bytes int) (time.Time, bool) {
	if s.SerialFree.Before(now) {
		s.SerialFree = now
	}
	s.SerialFree = s.SerialFree.Add(time.Duration(float64(bytes) * 8 / s.Link.Kbps * float64(time.Millisecond)))
	jitter := s.RNG.Float64() * s.Link.JitterMS
	due := s.SerialFree.Add(time.Duration((s.Link.LatencyMS + jitter) * float64(time.Millisecond)))
	if due.Before(s.LastDue) {
		due = s.LastDue
	}
	recovery := s.Link.RecoveryProbability > 0 && s.RNG.Float64() < s.Link.RecoveryProbability
	if recovery {
		due = due.Add(time.Duration(s.Link.RecoveryMS * float64(time.Millisecond)))
	}
	if !s.Epoch.IsZero() && s.Link.OutageMS > 0 {
		start := s.Epoch.Add(time.Duration(s.Link.OutageAtMS * float64(time.Millisecond)))
		end := start.Add(time.Duration(s.Link.OutageMS * float64(time.Millisecond)))
		if !due.Before(start) && due.Before(end) {
			due = end
		}
	}
	s.LastDue = due
	return due, recovery
}

type Proxy struct {
	listener    net.Listener
	cancel      context.CancelFunc
	wg          sync.WaitGroup
	mu          sync.Mutex
	connections map[net.Conn]bool
	up, down    LinkStats
	epoch       time.Time
}

func NewProxy(target string, profile Profile, seed int64) (*Proxy, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithCancel(context.Background())
	p := &Proxy{listener: l, cancel: cancel, connections: map[net.Conn]bool{}}
	p.wg.Add(1)
	go func() {
		defer p.wg.Done()
		var index int64
		for {
			client, err := l.Accept()
			if err != nil {
				return
			}
			server, err := net.DialTimeout("tcp", target, 2*time.Second)
			if err != nil {
				client.Close()
				continue
			}
			p.mu.Lock()
			p.connections[client] = true
			p.connections[server] = true
			p.mu.Unlock()
			index++
			p.wg.Add(1)
			go func(client, server net.Conn, index int64) {
				defer p.wg.Done()
				defer client.Close()
				defer server.Close()
				child, stop := context.WithCancel(ctx)
				defer stop()
				var links sync.WaitGroup
				for _, direction := range []struct {
					src, dst net.Conn
					link     Link
					up       bool
					seed     int64
				}{{client, server, profile.Up, true, seed + index*2}, {server, client, profile.Down, false, seed + index*2 + 1}} {
					links.Add(1)
					go func(d struct {
						src, dst net.Conn
						link     Link
						up       bool
						seed     int64
					}) {
						defer links.Done()
						p.pipe(child, d.src, d.dst, d.link, d.up, d.seed)
						stop()
						client.Close()
						server.Close()
					}(direction)
				}
				links.Wait()
				p.mu.Lock()
				delete(p.connections, client)
				delete(p.connections, server)
				p.mu.Unlock()
			}(client, server, index)
		}
	}()
	return p, nil
}
func (p *Proxy) Addr() string     { return p.listener.Addr().String() }
func (p *Proxy) Arm(at time.Time) { p.mu.Lock(); p.epoch = at; p.mu.Unlock() }
func (p *Proxy) Stats() map[string]LinkStats {
	p.mu.Lock()
	defer p.mu.Unlock()
	return map[string]LinkStats{"up": p.up, "down": p.down}
}
func (p *Proxy) Close() {
	p.cancel()
	p.listener.Close()
	p.mu.Lock()
	for c := range p.connections {
		c.Close()
	}
	p.mu.Unlock()
	p.wg.Wait()
}
func (p *Proxy) pipe(ctx context.Context, src, dst net.Conn, link Link, up bool, seed int64) {
	ctx, cancel := context.WithCancel(ctx)
	type chunk struct {
		data []byte
		due  time.Time
	}
	// Bounded network buffer; filling it backpressures the actual TCP sender.
	pending := make(chan chunk, 512)
	readerDone := make(chan struct{})
	go func() {
		defer close(readerDone)
		defer close(pending)
		schedule := Schedule{Link: link, RNG: rand.New(rand.NewSource(seed))}
		for {
			buf := make([]byte, 1460)
			n, err := src.Read(buf)
			if n > 0 {
				now := time.Now()
				p.mu.Lock()
				schedule.Epoch = p.epoch
				p.mu.Unlock()
				due, recovery := schedule.Due(now, n)
				p.mu.Lock()
				stats := &p.down
				if up {
					stats = &p.up
				}
				stats.BytesRead += int64(n)
				stats.Chunks++
				if recovery {
					stats.RecoveryEvents++
				}
				stats.MaxScheduledDelayMS = max(stats.MaxScheduledDelayMS, float64(due.Sub(now))/float64(time.Millisecond))
				p.mu.Unlock()
				select {
				case pending <- chunk{buf[:n], due}:
				case <-ctx.Done():
					return
				}
			}
			if err != nil {
				return
			}
		}
	}()
	defer func() { cancel(); src.Close(); <-readerDone }()
	for {
		select {
		case <-ctx.Done():
			return
		case packet, ok := <-pending:
			if !ok {
				return
			}
			if delay := time.Until(packet.due); delay > 0 {
				timer := time.NewTimer(delay)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
			if err := dst.SetWriteDeadline(time.Now().Add(time.Second)); err != nil {
				return
			}
			n, err := io.Copy(dst, bytesReader(packet.data))
			p.mu.Lock()
			if up {
				p.up.BytesDelivered += n
			} else {
				p.down.BytesDelivered += n
			}
			p.mu.Unlock()
			if err != nil {
				return
			}
		}
	}
}

// A slice reader avoids sharing mutable packet buffers between goroutines.
type sliceReader struct{ b []byte }

func bytesReader(b []byte) *sliceReader { return &sliceReader{b: b} }
func (r *sliceReader) Read(b []byte) (int, error) {
	if len(r.b) == 0 {
		return 0, io.EOF
	}
	n := copy(b, r.b)
	r.b = r.b[n:]
	return n, nil
}
