package history

import (
	"github.com/apteva/apps/mcp/instances/internal/monitor"
	"path/filepath"
	"testing"
	"time"
)

type Metrics = monitor.Metrics
type CPUMetrics = monitor.CPUMetrics
type MemMetrics = monitor.MemMetrics
type DiskMetrics = monitor.DiskMetrics
type NetMetrics = monitor.NetMetrics
type Batch = monitor.Batch

var NewEngine = monitor.NewEngine
var Tiers = monitor.Tiers

const CollectorVersion = monitor.CollectorVersion

func reading(at int64, pct, core float64) Metrics {
	return Metrics{Timestamp: time.UnixMilli(at).UTC().Format(time.RFC3339Nano), ResourceTimestamp: time.UnixMilli(at).UTC().Format(time.RFC3339Nano), CPU: CPUMetrics{TotalPct: pct, BusiestCorePct: core, IntervalMS: 250, PeakAt: at}, Mem: MemMetrics{UsedBytes: 20, TotalBytes: 100, AvailableBytes: 80}, Disk: []DiskMetrics{}, Net: []NetMetrics{}}
}
func ptr(m Metrics) *Metrics { return &m }
func readingPoint(m Metrics, at, duration int64) monitor.Point {
	return monitor.Point{Time: at, Step: 1000, ObservedMS: duration, Samples: 4, Values: map[string]monitor.Aggregate{"cpu": {Sum: m.CPU.TotalPct * float64(duration), ObservedMS: duration, Min: m.CPU.TotalPct, Max: m.CPU.TotalPct, PeakAt: at}}}
}
func TestPeaksSurviveEveryTierAndReplay(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "monitor.db"), DefaultBudget)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	e := NewEngine()
	base := time.Now().Add(-5*time.Second).UnixMilli() / 1000 * 1000
	for i := 1; i <= 5; i++ {
		pct := float64(10)
		if i == 3 {
			pct = 100
		}
		e.Add(reading(base+int64(i)*250, pct, pct))
	}
	b := e.Export(0)
	if len(b.Points) != 1 {
		t.Fatalf("points %+v", b.Points)
	}
	if b.Points[0].Values["cpu"].Max != 100 || b.Points[0].Above95MS != 250 || b.Points[0].Values["cpu"].Average(1000) != 32.5 {
		t.Fatalf("lost spike: %+v", b.Points[0])
	}
	if err := s.Ingest(7, b); err != nil {
		t.Fatal(err)
	}
	if err := s.Ingest(7, b); err != nil {
		t.Fatal(err)
	}
	for _, tier := range Tiers {
		var data string
		if err := s.DB.QueryRow(`SELECT data FROM points WHERE host=7 AND step=?`, tier.Step).Scan(&data); err != nil {
			t.Fatal(err)
		}
		h, err := s.History(7, base, base+1000, tier.Name, 20)
		if err != nil || len(h.Points) != 1 {
			t.Fatalf("%s history: %+v %v", tier.Name, h, err)
		}
		p := h.Points[0]
		if p.ObservedMS != 1000 || p.Values["cpu"].Max != 100 || p.Values["cpu"].Average(p.ObservedMS) != 32.5 {
			t.Fatalf("%s replay changed values: %+v", tier.Name, p)
		}
	}
	// Expire raw data; coarser history must still contain the original peak.
	if err := s.Prune(time.UnixMilli(base).Add(2 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	h, err := s.History(7, base, base+1000, "1m", 20)
	if err != nil || len(h.Points) != 1 || h.Points[0].Values["cpu"].Max != 100 {
		t.Fatalf("rollup lost peak: %+v %v", h, err)
	}
}
func TestHistoryValidationAndLimits(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "monitor.db"), DefaultBudget)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UnixMilli()
	if _, err := s.History(1, now-3600000, now, "1s", 600); err == nil {
		t.Fatal("oversized response accepted")
	}
	h, err := s.History(1, now-3600000, now, "auto", 600)
	if err != nil || h.Resolution != "1m" {
		t.Fatalf("auto resolution %+v %v", h, err)
	}
	if _, err := s.History(1, now, now-1, "auto", 600); err == nil {
		t.Fatal("reversed range accepted")
	}
	if _, err := s.ListIncidents(1, 201); err == nil {
		t.Fatal("unbounded incident list accepted")
	}
}
func TestRetentionAndPhysicalBudget(t *testing.T) {
	budget := int64(4 << 20)
	s, err := OpenStore(filepath.Join(t.TempDir(), "monitor.db"), budget)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	base := time.Now().Add(-time.Hour).UnixMilli() / 1000 * 1000
	for host := int64(1); host <= 15; host++ {
		for offset := int64(0); offset < 1200; offset += 120 {
			b := Batch{Version: CollectorVersion, Latest: ptr(reading(base+offset*1000, 10, 10))}
			for j := int64(0); j < 120; j++ {
				b.Points = append(b.Points, readingPoint(reading(base+(offset+j)*1000, 10, 10), base+(offset+j)*1000, 1000))
			}
			if err := s.Ingest(host, b); err != nil {
				t.Fatal(err)
			}
		}
	}
	var pages, size int64
	s.DB.QueryRow("PRAGMA page_count").Scan(&pages)
	s.DB.QueryRow("PRAGMA page_size").Scan(&size)
	if pages*size > budget {
		t.Fatalf("budget exceeded: %d", pages*size)
	}
	if err := s.Prune(time.Now().Add(366 * 24 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	var n int
	s.DB.QueryRow("SELECT COUNT(*) FROM points").Scan(&n)
	if n != 0 {
		t.Fatalf("expired points=%d", n)
	}
}

func TestReconnectAdvancesPastExpiredRawButPreservesRetainedRollups(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "monitor.db"), DefaultBudget)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	base := time.Now().Add(-3*time.Hour).UnixMilli() / 1000 * 1000
	b := Batch{Version: CollectorVersion, Latest: ptr(reading(time.Now().UnixMilli(), 5, 5)), Points: []monitor.Point{readingPoint(reading(base, 100, 100), base, 1000)}}
	if err := s.Ingest(1, b); err != nil {
		t.Fatal(err)
	}
	status, err := s.Status(1)
	if err != nil || status.LastPoint != base {
		t.Fatalf("expired cursor stalled: %+v %v", status, err)
	}
	h, err := s.History(1, base, base+1000, "1m", 10)
	if err != nil || len(h.Points) != 1 || h.Points[0].Values["cpu"].Max != 100 {
		t.Fatalf("retained peak missing: %+v %v", h, err)
	}
	var n int
	s.DB.QueryRow(`SELECT count(*) FROM points WHERE host=1 AND step=1000`).Scan(&n)
	if n != 0 {
		t.Fatal("expired raw persisted")
	}
}
