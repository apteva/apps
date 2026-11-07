package history

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/apteva/apps/mcp/instances/internal/monitor"
)

func TestRoutineCleanupSharedCadenceAndBoundedBacklog(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "monitor.db"), DefaultBudget)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	base := time.Now().Add(-3*time.Hour).UnixMilli() / 1000 * 1000
	tx, err := s.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := int64(0); i < 2501; i++ {
		if _, err = tx.Exec(`INSERT INTO points VALUES(1,1000,?,'{}')`, base+i*1000); err != nil {
			t.Fatal(err)
		}
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	ingest := func(host int64) {
		t.Helper()
		if err := s.Ingest(host, Batch{Version: CollectorVersion, Latest: ptr(reading(time.Now().UnixMilli(), 10, 10))}); err != nil {
			t.Fatal(err)
		}
	}
	expired := func() int {
		t.Helper()
		var n int
		if err := s.DB.QueryRow(`SELECT count(*) FROM points WHERE step=1000`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	ingest(1)
	if n := expired(); n != 1501 {
		t.Fatalf("first maintenance did not bound backlog: %d", n)
	}
	for _, host := range []int64{0, 1, 2, 6} {
		ingest(host)
	}
	if n := expired(); n != 1501 {
		t.Fatalf("other host imports repeated cleanup: %d", n)
	}
	s.lastPrune = time.Now().Add(-pruneInterval)
	ingest(6)
	if n := expired(); n != 501 {
		t.Fatalf("scheduled maintenance did not resume: %d", n)
	}
	if err = s.Prune(time.Now()); err != nil {
		t.Fatal(err)
	}
	if n := expired(); n != 0 {
		t.Fatalf("explicit maintenance did not drain backlog: %d", n)
	}
}

func TestIncidentCapEnforcedBetweenScheduledSweeps(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "monitor.db"), DefaultBudget)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Now().UnixMilli()
	for batch := 0; batch < 6; batch++ {
		b := Batch{Version: CollectorVersion, Latest: ptr(reading(now, 10, 10))}
		for i := 0; i < 50; i++ {
			n := batch*50 + i
			b.Incidents = append(b.Incidents, monitor.Incident{ID: fmt.Sprintf("event-%03d", n), Start: now + int64(n), Updated: now + int64(n)})
		}
		if err = s.Ingest(1, b); err != nil {
			t.Fatal(err)
		}
		var n int
		if err = s.DB.QueryRow(`SELECT count(*) FROM incidents WHERE host=1`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		if n > 200 {
			t.Fatalf("incident cap exceeded between sweeps: %d", n)
		}
	}
	var oldest string
	if err = s.DB.QueryRow(`SELECT id FROM incidents WHERE host=1 ORDER BY start LIMIT 1`).Scan(&oldest); err != nil {
		t.Fatal(err)
	}
	if oldest != "event-100" {
		t.Fatalf("newest incident summaries were not retained: %s", oldest)
	}
}

func TestCleanupUsesCoveringIndexAndExactKeyDelete(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "monitor.db"), DefaultBudget)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, q := range []struct {
		sql, want string
		args      []any
	}{
		{`SELECT host,step,ts FROM points WHERE step=? AND ts<? ORDER BY ts LIMIT 1000`, "COVERING INDEX points_age", []any{1000, 0}},
		{`DELETE FROM points WHERE host=? AND step=? AND ts=?`, "PRIMARY KEY (host=? AND step=? AND ts=?)", []any{1, 1000, 0}},
		{`SELECT host,id,length(detail) FROM incidents INDEXED BY incidents_detail_budget WHERE detail IS NOT NULL ORDER BY updated DESC,host`, "INDEX incidents_detail_budget", nil},
		{`SELECT host,id FROM (SELECT host,id,ROW_NUMBER() OVER(PARTITION BY host ORDER BY start DESC,id) AS n FROM incidents) WHERE n>200 LIMIT 16`, "COVERING INDEX incidents_host_start", nil},
	} {
		rows, err := s.DB.Query("EXPLAIN QUERY PLAN "+q.sql, q.args...)
		if err != nil {
			t.Fatal(err)
		}
		var plan strings.Builder
		for rows.Next() {
			var a, b, c int
			var detail string
			if err = rows.Scan(&a, &b, &c, &detail); err != nil {
				t.Fatal(err)
			}
			plan.WriteString(detail + "\n")
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(plan.String(), q.want) || strings.Contains(plan.String(), "SCAN points") {
			t.Fatalf("cleanup lost indexed access: %s", plan.String())
		}
	}
}

// A payload-heavy retained database exposes SQLite's tuple-IN DELETE cost even
// with zero expired keys. Setup is outside the timing; both cases preserve rows.
func BenchmarkHistoryCleanupNoExpiredRows(b *testing.B) {
	s, err := OpenStore(filepath.Join(b.TempDir(), "monitor.db"), DefaultBudget)
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	tx, err := s.DB.Begin()
	if err != nil {
		b.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO points VALUES(?,?,?,?)`)
	if err != nil {
		b.Fatal(err)
	}
	data := strings.Repeat("x", 2048)
	for i := 0; i < 32768; i++ {
		if _, err = stmt.Exec(i%4, 1000, i/4*1000, data); err != nil {
			b.Fatal(err)
		}
	}
	stmt.Close()
	if err = tx.Commit(); err != nil {
		b.Fatal(err)
	}
	b.Run("legacy-tuple-delete", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if _, err := s.DB.Exec(`DELETE FROM points WHERE (host,step,ts) IN (SELECT host,step,ts FROM points WHERE step=? AND ts<? LIMIT 1000)`, 1000, 0); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("indexed-key-selection", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if n, err := s.deletePoints(`SELECT host,step,ts FROM points WHERE step=? AND ts<? ORDER BY ts LIMIT 1000`, 1000, 0); err != nil || n != 0 {
				b.Fatalf("deleted=%d: %v", n, err)
			}
		}
	})
}

func BenchmarkIncidentCleanupRetainedDetails(b *testing.B) {
	s, err := OpenStore(filepath.Join(b.TempDir(), "monitor.db"), DefaultBudget)
	if err != nil {
		b.Fatal(err)
	}
	defer s.Close()
	tx, err := s.DB.Begin()
	if err != nil {
		b.Fatal(err)
	}
	stmt, err := tx.Prepare(`INSERT INTO incidents VALUES(?,?,?,?,?,?)`)
	if err != nil {
		b.Fatal(err)
	}
	payload := []byte(strings.Repeat("x", 128<<10))
	for host := 0; host < 4; host++ {
		for i := 0; i < 200; i++ {
			if _, err = stmt.Exec(host, fmt.Sprint(i), i, i, "{}", payload); err != nil {
				b.Fatal(err)
			}
		}
	}
	stmt.Close()
	if err = tx.Commit(); err != nil {
		b.Fatal(err)
	}
	q := `SELECT host,id FROM (SELECT host,id,ROW_NUMBER() OVER(PARTITION BY host ORDER BY start DESC) AS n FROM incidents) WHERE n>200 LIMIT 16`
	if _, err = s.DB.Exec(`DROP INDEX incidents_host_start`); err != nil {
		b.Fatal(err)
	}
	b.Run("legacy-tuple-delete", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			result, err := s.DB.Exec(`DELETE FROM incidents WHERE (host,id) IN (` + q + `)`)
			if err != nil {
				b.Fatal(err)
			}
			if n, _ := result.RowsAffected(); n != 0 {
				b.Fatalf("unexpected deleted rows: %d", n)
			}
		}
	})
	if _, err = s.DB.Exec(`CREATE INDEX incidents_host_start ON incidents(host,start DESC,id)`); err != nil {
		b.Fatal(err)
	}
	b.Run("indexed-key-selection", func(b *testing.B) {
		for i := 0; i < b.N; i++ {
			if n, err := s.deleteIncidents(q); err != nil || n != 0 {
				b.Fatalf("deleted=%d: %v", n, err)
			}
		}
	})
}

func TestDetailBudgetEnforcedWithoutWaitingForMaintenance(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "monitor.db"), DefaultBudget)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	if err = s.EnsureHost(1); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	for i := 0; i < 5; i++ {
		if _, err = s.DB.Exec(`INSERT INTO incidents VALUES(1,?,?,?,'{}',zeroblob(?))`, fmt.Sprint(i), now+int64(i), now+int64(i), 4<<20); err != nil {
			t.Fatal(err)
		}
	}
	s.lastPrune = time.Now()
	b := Batch{Version: CollectorVersion, Latest: ptr(reading(now, 10, 10)), Incidents: []monitor.Incident{{ID: "4", Start: now + 4, Updated: now + 5}}}
	if err = s.Ingest(1, b); err != nil {
		t.Fatal(err)
	}
	var bytes int64
	var summaries int
	if err = s.DB.QueryRow(`SELECT coalesce(sum(length(detail)),0),count(*) FROM incidents WHERE host=1`).Scan(&bytes, &summaries); err != nil {
		t.Fatal(err)
	}
	if bytes > monitor.IncidentBudgetBytes || summaries != 5 {
		t.Fatalf("bytes=%d summaries=%d", bytes, summaries)
	}
	var oldestNull bool
	if err = s.DB.QueryRow(`SELECT detail IS NULL FROM incidents WHERE host=1 AND id='0'`).Scan(&oldestNull); err != nil {
		t.Fatal(err)
	}
	if !oldestNull {
		t.Fatal("oldest detail was not evicted")
	}
}
