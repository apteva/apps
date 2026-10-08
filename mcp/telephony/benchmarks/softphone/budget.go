package softphonebench

import (
	"sync"
	"time"
)

// WireBudgets gives the relay's UDP and signaling TCP one shared serializer
// per direction. It measures per-call application traffic, not all host traffic.
type WireBudgets struct{ Up, Down *wireBudget }
type WireBudgetStats struct {
	ChargedWireBytes int64   `json:"charged_wire_bytes"`
	WindowMS         float64 `json:"window_ms"`
	MaxBacklogMS     float64 `json:"max_backlog_ms"`
}
type wireBudget struct {
	mu          sync.Mutex
	kbps        float64
	epoch, free time.Time
	stats       WireBudgetStats
}

func newWireBudgets(up, down float64) *WireBudgets {
	return &WireBudgets{Up: &wireBudget{kbps: up}, Down: &wireBudget{kbps: down}}
}
func (b *wireBudget) reserve(now time.Time, bytes int, maxWait time.Duration) (time.Time, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.epoch.IsZero() || now.Before(b.epoch) {
		return now, true
	}
	free := b.free
	if free.Before(now) {
		free = now
	}
	next := free.Add(time.Duration(float64(bytes) * 8 / b.kbps * float64(time.Millisecond)))
	if maxWait > 0 && next.Sub(now) > maxWait {
		return next, false
	}
	b.free = next
	b.stats.ChargedWireBytes += int64(bytes)
	b.stats.MaxBacklogMS = max(b.stats.MaxBacklogMS, float64(next.Sub(now))/float64(time.Millisecond))
	return next, true
}
func (b *WireBudgets) Arm(at time.Time) {
	for _, link := range []*wireBudget{b.Up, b.Down} {
		link.mu.Lock()
		link.epoch = at
		link.mu.Unlock()
	}
}
func (b *WireBudgets) Stats() map[string]WireBudgetStats {
	result := map[string]WireBudgetStats{}
	for direction, link := range map[string]*wireBudget{"up": b.Up, "down": b.Down} {
		link.mu.Lock()
		stats := link.stats
		if !link.epoch.IsZero() {
			stats.WindowMS = max(0, float64(time.Since(link.epoch))/float64(time.Millisecond))
		}
		link.mu.Unlock()
		result[direction] = stats
	}
	return result
}
