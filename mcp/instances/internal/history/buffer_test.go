package history

import (
	"path/filepath"
	"testing"
	"time"

	"github.com/apteva/apps/mcp/instances/internal/monitor"
)

func pointRows(t *testing.T, s *Store, host int64) int {
	t.Helper()
	var n int
	if err := s.DB.QueryRow(`SELECT count(*) FROM points WHERE host=? AND step=1000`, host).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestIngestBuffersUntilFlushIntervalButReadsSeeData(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "monitor.db"), DefaultBudget)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	var mode string
	if err = s.DB.QueryRow("PRAGMA journal_mode").Scan(&mode); err != nil || mode != "wal" {
		t.Fatalf("journal mode %q %v", mode, err)
	}
	base := time.Now().Add(-10*time.Second).UnixMilli() / 1000 * 1000
	for i := int64(0); i < 4; i++ {
		at := base + i*1000
		b := Batch{Version: CollectorVersion, Latest: ptr(reading(at, 40, 50)), Points: []monitor.Point{readingPoint(reading(at, 40, 50), at, 1000)}}
		if err = s.Ingest(3, b); err != nil {
			t.Fatal(err)
		}
	}
	if n := pointRows(t, s, 3); n != 0 {
		t.Fatalf("batches were committed before the flush interval: %d rows", n)
	}
	status, err := s.Status(3)
	if err != nil || status.State != "running" || status.LastPoint != base+3000 {
		t.Fatalf("status does not reflect pending data: %+v %v", status, err)
	}
	latest, err := s.Latest(3)
	if err != nil || latest == nil || latest.Timestamp != reading(base+3000, 40, 50).Timestamp {
		t.Fatalf("latest does not reflect pending data: %+v %v", latest, err)
	}
	h, err := s.History(3, base, base+4000, "1s", 10)
	if err != nil || len(h.Points) != 4 {
		t.Fatalf("history read did not flush pending data: %d %v", len(h.Points), err)
	}
	if n := pointRows(t, s, 3); n != 4 {
		t.Fatalf("rows after flush: %d", n)
	}

	at := base + 5000
	if err = s.Ingest(3, Batch{Version: CollectorVersion, Latest: ptr(reading(at, 40, 50)), Points: []monitor.Point{readingPoint(reading(at, 40, 50), at, 1000)}}); err != nil {
		t.Fatal(err)
	}
	s.lastFlush = time.Now().Add(-flushInterval)
	at += 1000
	if err = s.Ingest(3, Batch{Version: CollectorVersion, Latest: ptr(reading(at, 40, 50)), Points: []monitor.Point{readingPoint(reading(at, 40, 50), at, 1000)}}); err != nil {
		t.Fatal(err)
	}
	if n := pointRows(t, s, 3); n != 6 {
		t.Fatalf("elapsed flush interval did not commit: %d rows", n)
	}
	var last int64
	if err = s.DB.QueryRow(`SELECT last_point FROM hosts WHERE id=3`).Scan(&last); err != nil || last != at {
		t.Fatalf("last_point %d %v", last, err)
	}
}

func TestDisabledHostDropsPendingData(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "monitor.db"), DefaultBudget)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	at := time.Now().Add(-5*time.Second).UnixMilli() / 1000 * 1000
	if err = s.Ingest(4, Batch{Version: CollectorVersion, Latest: ptr(reading(at, 1, 1)), Points: []monitor.Point{readingPoint(reading(at, 1, 1), at, 1000)}}); err != nil {
		t.Fatal(err)
	}
	if err = s.SetEnabled(4, false); err != nil {
		t.Fatal(err)
	}
	if err = s.Flush(); err != nil {
		t.Fatal(err)
	}
	if n := pointRows(t, s, 4); n != 0 {
		t.Fatalf("disabled host committed pending data: %d", n)
	}
	if status, _ := s.Status(4); status.State != "disabled" {
		t.Fatalf("pending data overrode disabled state: %+v", status)
	}
}

func TestOpenIncidentDetailRewriteIsThrottled(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "monitor.db"), DefaultBudget)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UnixMilli()
	recordings := func(n int) []monitor.Reading {
		out := make([]monitor.Reading, n)
		for i := range out {
			out[i] = monitor.Reading{Time: now + int64(i), CPU: 99, Core: 99, IntervalMS: 250}
		}
		return out
	}
	ingest := func(updated int64, n int, end int64) {
		t.Helper()
		ev := monitor.Incident{ID: "spike", Start: now, Updated: updated, End: end, Recordings: recordings(n)}
		if err := s.Ingest(5, Batch{Version: CollectorVersion, Latest: ptr(reading(now, 99, 99)), Incidents: []monitor.Incident{ev}}); err != nil {
			t.Fatal(err)
		}
		if err := s.Flush(); err != nil {
			t.Fatal(err)
		}
	}
	stored := func() (int, int64) {
		t.Helper()
		ev, err := s.Incident(5, "spike")
		if err != nil {
			t.Fatal(err)
		}
		return len(ev.Recordings), ev.Updated
	}
	ingest(now+1, 10, 0)
	if n, _ := stored(); n != 10 {
		t.Fatalf("first detail not written: %d", n)
	}
	ingest(now+2, 20, 0)
	if n, updated := stored(); n != 10 || updated != now+2 {
		t.Fatalf("open incident: detail=%d updated=%d (summary must follow, detail must not)", n, updated)
	}
	ingest(now+3, 30, now+3)
	if n, _ := stored(); n != 30 {
		t.Fatalf("closed incident detail not written: %d", n)
	}
	// Pending detail is served without waiting for the throttle.
	if err := s.Ingest(5, Batch{Version: CollectorVersion, Latest: ptr(reading(now, 99, 99)), Incidents: []monitor.Incident{{ID: "spike", Start: now, Updated: now + 4, End: now + 3, Recordings: recordings(31)}}}); err != nil {
		t.Fatal(err)
	}
	if n, _ := stored(); n != 31 {
		t.Fatalf("pending detail not visible: %d", n)
	}
}

func TestCloseFlushesPendingData(t *testing.T) {
	path := filepath.Join(t.TempDir(), "monitor.db")
	s, err := OpenStore(path, DefaultBudget)
	if err != nil {
		t.Fatal(err)
	}
	at := time.Now().Add(-5*time.Second).UnixMilli() / 1000 * 1000
	if err = s.Ingest(8, Batch{Version: CollectorVersion, Latest: ptr(reading(at, 1, 1)), Points: []monitor.Point{readingPoint(reading(at, 1, 1), at, 1000)}}); err != nil {
		t.Fatal(err)
	}
	if err = s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = OpenStore(path, DefaultBudget)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if n := pointRows(t, s, 8); n != 1 {
		t.Fatalf("close lost pending data: %d rows", n)
	}
}
