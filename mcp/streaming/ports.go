package main

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
)

// portAllocator manages a fixed range of TCP ports for RTMP listeners.
// One port per active stream. Allocation is in-memory only — on
// sidecar restart the allocator starts fresh; the OnMount reconciler
// flips any orphaned status=live row to errored, so previously-leaked
// ports are implicitly returned to the pool.
type portAllocator struct {
	mu       sync.Mutex
	low      int
	high     int
	used     map[int]bool
}

func newPortAllocator(spec string) (*portAllocator, error) {
	low, high, err := parsePortRange(spec)
	if err != nil {
		return nil, err
	}
	return &portAllocator{
		low:  low,
		high: high,
		used: map[int]bool{},
	}, nil
}

// allocate returns the lowest free port in the range, or an error if
// the range is exhausted. Idempotent across restarts because the
// in-memory free-list is rebuilt fresh each boot.
func (p *portAllocator) allocate() (int, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for port := p.low; port <= p.high; port++ {
		if !p.used[port] {
			p.used[port] = true
			return port, nil
		}
	}
	return 0, fmt.Errorf("rtmp port range %d-%d exhausted", p.low, p.high)
}

// release returns a port to the pool. No-op if the port wasn't
// tracked — keeps cleanup paths simple.
func (p *portAllocator) release(port int) {
	if port == 0 {
		return
	}
	p.mu.Lock()
	delete(p.used, port)
	p.mu.Unlock()
}

// markUsed marks a port as used without going through allocate. Used
// during reconciliation if v0.2 ever wants to recover live runners
// across restart. Currently unused.
func (p *portAllocator) markUsed(port int) {
	if port < p.low || port > p.high {
		return
	}
	p.mu.Lock()
	p.used[port] = true
	p.mu.Unlock()
}

// size returns the total port-range capacity.
func (p *portAllocator) size() int {
	return p.high - p.low + 1
}

// parsePortRange accepts "1935-1965" (inclusive). Single port "1935"
// is also valid (range of one).
func parsePortRange(spec string) (int, int, error) {
	spec = strings.TrimSpace(spec)
	if spec == "" {
		return 0, 0, errors.New("empty range")
	}
	parts := strings.SplitN(spec, "-", 2)
	low, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil || low < 1 || low > 65535 {
		return 0, 0, fmt.Errorf("invalid lower port %q", parts[0])
	}
	high := low
	if len(parts) == 2 {
		high, err = strconv.Atoi(strings.TrimSpace(parts[1]))
		if err != nil || high < 1 || high > 65535 {
			return 0, 0, fmt.Errorf("invalid upper port %q", parts[1])
		}
	}
	if high < low {
		return 0, 0, fmt.Errorf("upper %d < lower %d", high, low)
	}
	return low, high, nil
}
