package monitor

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
)

func reading(at int64, pct, core float64) Metrics {
	return Metrics{Timestamp: time.UnixMilli(at).UTC().Format(time.RFC3339Nano), ResourceTimestamp: time.UnixMilli(at).UTC().Format(time.RFC3339Nano), CPU: CPUMetrics{TotalPct: pct, BusiestCorePct: core, IntervalMS: 250, PeakAt: at}, Mem: MemMetrics{UsedBytes: 20, TotalBytes: 100, AvailableBytes: 80}, Disk: []DiskMetrics{}, Net: []NetMetrics{}}
}
func TestCPUDeltaGuestCountersAndSingleCore(t *testing.T) {
	previous := make([]cpu.TimesStat, 4)
	current := make([]cpu.TimesStat, 4)
	current[0] = cpu.TimesStat{User: 20, System: 5, Guest: 10}
	for i := 1; i < 4; i++ {
		current[i].Idle = 25
	}
	got, valid := CPUDelta(previous, current, 250*time.Millisecond)
	if !valid || got.TotalPct != 25 || got.BusiestCorePct != 100 || got.UserPct != 20 {
		t.Fatalf("invalid per-core or guest accounting: %+v %v", got, valid)
	}
	current[0].User = -1
	if _, valid := CPUDelta(previous, current, time.Second); valid {
		t.Fatal("counter reset accepted")
	}
}

func TestIncidentBeforeAfterRecoveryAndSpool(t *testing.T) {
	e := NewEngine()
	base := time.Now().Add(-3*time.Minute).UnixMilli() / 1000 * 1000
	for i := 1; i <= 480; i++ {
		e.Add(reading(base+int64(i)*250, 10, 10))
	}
	id := e.Add(reading(base+120250, 100, 100))
	if id == "" {
		t.Fatal("spike not detected")
	}
	for i := 1; i <= 1202; i++ {
		e.Add(reading(base+120250+int64(i)*250, 10, 10))
	}
	b := e.Export(0)
	if len(b.Incidents) != 1 {
		t.Fatalf("expected one incident, got %d", len(b.Incidents))
	}
	ev := b.Incidents[0]
	if ev.End == 0 || ev.Peak != 100 || ev.Recordings[0].Time > ev.Start-119000 || len(ev.Recordings) < 1600 {
		t.Fatalf("missing pre/post recording %+v", ev)
	}
	path := filepath.Join(t.TempDir(), "spool.json.gz")
	if err := e.Save(path); err != nil {
		t.Fatal(err)
	}
	restored, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	if got := restored.Export(0); len(got.Incidents) != 1 || got.Incidents[0].Peak != 100 {
		t.Fatal("restart lost incident")
	}
}
func TestSustainedCorePressureIsBounded(t *testing.T) {
	e := NewEngine()
	base := time.Now().Add(-time.Hour).UnixMilli() / 1000 * 1000
	for i := 1; i <= 7000; i++ {
		e.Add(reading(base+int64(i)*250, 25, 100))
	}
	b := e.Export(0)
	if len(b.Incidents) < 2 || b.Incidents[0].End == 0 {
		t.Fatal("continuous overload did not close bounded windows")
	}
	for _, ev := range b.Incidents {
		if len(ev.Recordings) > 3000 {
			t.Fatal("unbounded recording")
		}
		if ev.Reason != "Core ≥95% for 1s" {
			t.Fatalf("wrong trigger %q", ev.Reason)
		}
	}
	if len(b.Points) > 120 {
		t.Fatal("unbounded export")
	}
}
func TestCoverageUsesObservedTimeAndMetricAvailability(t *testing.T) {
	p := Point{Time: 1000, Step: 1000}
	p.Merge(readingPoint(reading(1250, 10, 10), 1000, 250))
	m := reading(1750, 100, 100)
	m.ResourceError = "memory unavailable"
	m.ResourceTimestamp = ""
	p.Merge(readingPoint(m, 1500, 250))
	if p.ObservedMS != 500 || p.Values["cpu"].Average(p.ObservedMS) != 55 || p.Values["memory"].Average(p.ObservedMS) != 20 {
		t.Fatalf("incorrect coverage: %+v", p)
	}
}

func ptr(m Metrics) *Metrics { return &m }
func TestUnixSocketAndCancellation(t *testing.T) {
	dir, err := os.MkdirTemp("/tmp", "im-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(dir)
	e := NewEngine()
	e.Add(reading(time.Now().UnixMilli(), 5, 5))
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, dir, e) }()
	var data []byte
	for i := 0; i < 50; i++ {
		data, err = ExportSocket(dir, 0)
		if err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	cancel()
	if serveErr := <-done; serveErr != nil {
		t.Fatal(serveErr)
	}
	if err != nil || len(data) == 0 {
		t.Fatalf("export %s %v", data, err)
	}
}
func TestWeightedAggregation(t *testing.T) {
	p := Point{}
	p.Merge(Point{Samples: 1, ObservedMS: 100, Values: map[string]Aggregate{"cpu": {ObservedMS: 100, Sum: 10000, Min: 100, Max: 100, PeakAt: 1}}})
	p.Merge(Point{Samples: 1, ObservedMS: 900, Values: map[string]Aggregate{"cpu": {ObservedMS: 900, Sum: 9000, Min: 10, Max: 10, PeakAt: 2}}})
	if math.Abs(p.Values["cpu"].Average(p.ObservedMS)-19) > 0.001 || p.Values["cpu"].PeakAt != 1 {
		t.Fatal(p)
	}
}

func TestRestartClosesInterruptedRecordingWithoutChangingReadOnlyExports(t *testing.T) {
	e := NewEngine()
	at := time.Now().UnixMilli()
	e.Add(reading(at, 100, 100))
	path := filepath.Join(t.TempDir(), "spool.json.gz")
	if err := e.Save(path); err != nil {
		t.Fatal(err)
	}
	restored, err := Load(path)
	if err != nil {
		t.Fatal(err)
	}
	before := restored.Export(0)
	if len(before.Incidents) != 1 || before.Incidents[0].End != 0 {
		t.Fatal("read-only export closed an active recording")
	}
	restored.Recover(at + 1000)
	after := restored.Export(at)
	if len(after.Incidents) != 1 || after.Incidents[0].End != at || after.Incidents[0].Updated != at+1000 {
		t.Fatal("restart did not publish closure")
	}
}
