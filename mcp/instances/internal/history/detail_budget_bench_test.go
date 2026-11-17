package history

import (
	"fmt"
	"path/filepath"
	"testing"
	"time"

	"github.com/apteva/apps/mcp/instances/internal/monitor"
)

// Many hosts, each with retained incident detail and one open incident whose
// summary is re-exported on every poll: the steady state of a busy fleet. One
// iteration is one flush interval's worth of collector batches.
func BenchmarkFlushOpenIncidentsManyHosts(b *testing.B) {
	const hosts, retained = 50, 200
	s, err := OpenStore(filepath.Join(b.TempDir(), "monitor.db"), DefaultBudget)
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UnixMilli()
	tx, err := s.DB.Begin()
	if err != nil {
		b.Fatal(err)
	}
	for h := int64(1); h <= hosts; h++ {
		if _, err = tx.Exec(`INSERT INTO hosts(id) VALUES(?)`, h); err != nil {
			b.Fatal(err)
		}
		for i := 0; i < retained; i++ {
			at := now - int64(retained-i)*1000
			if _, err = tx.Exec(`INSERT INTO incidents VALUES(?,?,?,?,'{}',zeroblob(2048))`, h, fmt.Sprint(i), at, at); err != nil {
				b.Fatal(err)
			}
		}
	}
	if err = tx.Commit(); err != nil {
		b.Fatal(err)
	}
	s.lastPrune = time.Now()
	b.ResetTimer()
	for n := 0; n < b.N; n++ {
		for h := int64(1); h <= hosts; h++ {
			ev := monitor.Incident{ID: "open", Start: now, Updated: now + int64(n) + 1}
			if err := s.Ingest(h, Batch{Version: CollectorVersion, Latest: ptr(reading(now, 90, 90)), Incidents: []monitor.Incident{ev}}); err != nil {
				b.Fatal(err)
			}
		}
		if err := s.Flush(); err != nil {
			b.Fatal(err)
		}
	}
}
