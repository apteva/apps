package history

import (
	"database/sql"
	"fmt"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/apteva/apps/mcp/instances/internal/monitor"
	_ "modernc.org/sqlite"
)

// The trigger-maintained counters must equal a full recount after any change.
func assertDetailCounters(t *testing.T, s *Store) {
	t.Helper()
	rows, err := s.DB.Query(`SELECT id,detail_bytes,(SELECT coalesce(sum(length(detail)),0) FROM incidents WHERE host=hosts.id AND detail IS NOT NULL) FROM hosts`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var h, counted, actual int64
		if err := rows.Scan(&h, &counted, &actual); err != nil {
			t.Fatal(err)
		}
		if counted != actual {
			t.Fatalf("host %d detail_bytes=%d, actual %d", h, counted, actual)
		}
	}
	var counted, actual int64
	if err := s.DB.QueryRow(`SELECT (SELECT value FROM metadata WHERE key='detail_bytes'),(SELECT coalesce(sum(length(detail)),0) FROM incidents WHERE detail IS NOT NULL)`).Scan(&counted, &actual); err != nil {
		t.Fatal(err)
	}
	if counted != actual {
		t.Fatalf("total detail_bytes=%d, actual %d", counted, actual)
	}
}

func TestDetailCountersTrackEveryWritePath(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "monitor.db"), DefaultBudget)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.lastPrune = time.Now()
	now := time.Now().UnixMilli()
	recordings := func(n int) []monitor.Reading {
		out := make([]monitor.Reading, n)
		for i := range out {
			out[i] = monitor.Reading{Time: now + int64(i), CPU: float64(i % 100), Core: 99, IntervalMS: 250}
		}
		return out
	}
	ingest := func(host int64, ev monitor.Incident) {
		t.Helper()
		if err := s.Ingest(host, Batch{Version: CollectorVersion, Latest: ptr(reading(now, 50, 50)), Incidents: []monitor.Incident{ev}}); err != nil {
			t.Fatal(err)
		}
		if err := s.Flush(); err != nil {
			t.Fatal(err)
		}
		assertDetailCounters(t, s)
	}
	// Insert with detail, then replace with larger detail on close.
	ingest(1, monitor.Incident{ID: "a", Start: now, Updated: now + 1, Recordings: recordings(50)})
	ingest(1, monitor.Incident{ID: "a", Start: now, Updated: now + 2, End: now + 2, Recordings: recordings(500)})
	// Summary-only update preserves the existing detail size.
	var before int64
	s.DB.QueryRow(`SELECT detail_bytes FROM hosts WHERE id=1`).Scan(&before)
	ingest(1, monitor.Incident{ID: "a", Start: now, Updated: now + 3, End: now + 2})
	var after int64
	s.DB.QueryRow(`SELECT detail_bytes FROM hosts WHERE id=1`).Scan(&after)
	if before == 0 || before != after {
		t.Fatalf("summary update changed detail size: %d -> %d", before, after)
	}
	// A stale update (the upsert's WHERE rejects it) changes nothing.
	if _, err := s.DB.Exec(`INSERT INTO incidents(host,id,start,updated,data,detail) VALUES(1,'a',?,?,'{}',zeroblob(99999)) ON CONFLICT(host,id) DO UPDATE SET detail=excluded.detail WHERE excluded.updated>incidents.updated`, now, now); err != nil {
		t.Fatal(err)
	}
	assertDetailCounters(t, s)
	// Second host.
	ingest(2, monitor.Incident{ID: "b", Start: now, Updated: now + 1, End: now + 1, Recordings: recordings(200)})
	// Rolled-back transaction leaves counters untouched.
	tx, err := s.DB.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(`INSERT INTO incidents VALUES(2,'rolled',?,?,'{}',zeroblob(4096))`, now, now); err != nil {
		t.Fatal(err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	assertDetailCounters(t, s)
	// Full-database retry path eviction (raw UPDATE outside commitLocked).
	if _, err = s.DB.Exec(`UPDATE incidents SET detail=NULL WHERE host=2 AND id='b'`); err != nil {
		t.Fatal(err)
	}
	assertDetailCounters(t, s)
	// Retention delete via scheduled maintenance.
	if _, err = s.DB.Exec(`INSERT INTO incidents VALUES(2,'old',?,?,'{}',zeroblob(1000))`, now, now-int64(31*24*time.Hour/time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	assertDetailCounters(t, s)
	if err = s.Prune(time.Now()); err != nil {
		t.Fatal(err)
	}
	assertDetailCounters(t, s)
	// Host removal.
	if err = s.RemoveHost(1); err != nil {
		t.Fatal(err)
	}
	assertDetailCounters(t, s)
	var total int64
	s.DB.QueryRow(`SELECT value FROM metadata WHERE key='detail_bytes'`).Scan(&total)
	if total != 0 {
		t.Fatalf("total after removing every detail = %d", total)
	}
}

func TestDetailCountersMigrateAndReconcileOnOpen(t *testing.T) {
	path := filepath.Join(t.TempDir(), "monitor.db")
	// A pre-counter store: no detail_bytes column, no triggers.
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	for _, stmt := range []string{
		`CREATE TABLE hosts(id INTEGER PRIMARY KEY,enabled INTEGER NOT NULL DEFAULT 1,state TEXT NOT NULL DEFAULT 'pending',version TEXT NOT NULL DEFAULT '',error TEXT NOT NULL DEFAULT '',last_seen INTEGER NOT NULL DEFAULT 0,last_point INTEGER NOT NULL DEFAULT 0,evicted INTEGER NOT NULL DEFAULT 0,latest TEXT)`,
		`CREATE TABLE incidents(host INTEGER NOT NULL,id TEXT NOT NULL,start INTEGER NOT NULL,updated INTEGER NOT NULL,data TEXT NOT NULL,detail BLOB,PRIMARY KEY(host,id)) WITHOUT ROWID`,
		`INSERT INTO hosts(id) VALUES(1),(2)`,
		`INSERT INTO incidents VALUES(1,'x',1,1,'{}',zeroblob(1000)),(1,'y',2,2,'{}',zeroblob(500)),(2,'z',3,3,'{}',zeroblob(70)),(2,'n',4,4,'{}',NULL)`,
	} {
		if _, err = db.Exec(stmt); err != nil {
			t.Fatal(err)
		}
	}
	db.Close()
	s, err := OpenStore(path, DefaultBudget)
	if err != nil {
		t.Fatal(err)
	}
	assertDetailCounters(t, s)
	var h1 int64
	s.DB.QueryRow(`SELECT detail_bytes FROM hosts WHERE id=1`).Scan(&h1)
	if h1 != 1500 {
		t.Fatalf("migrated host 1 detail_bytes=%d", h1)
	}
	// Any drift (e.g. a manual edit) is reconciled on the next open.
	if _, err = s.DB.Exec(`UPDATE hosts SET detail_bytes=0; UPDATE metadata SET value=42 WHERE key='detail_bytes'`); err != nil {
		t.Fatal(err)
	}
	s.Close()
	if s, err = OpenStore(path, DefaultBudget); err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	assertDetailCounters(t, s)
}

func TestInstallationDetailBudgetEvictsOldestAcrossHosts(t *testing.T) {
	// 4MiB store: installation-wide detail budget is 1MiB.
	s, err := OpenStore(filepath.Join(t.TempDir(), "monitor.db"), 4<<20)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.lastPrune = time.Now()
	now := time.Now().UnixMilli()
	for h := int64(1); h <= 3; h++ {
		if err = s.EnsureHost(h); err != nil {
			t.Fatal(err)
		}
		for i := 0; i < 2; i++ {
			at := now + h*10 + int64(i)
			if _, err = s.DB.Exec(`INSERT INTO incidents VALUES(?,?,?,?,'{}',zeroblob(?))`, h, fmt.Sprint(i), at, at, 200<<10); err != nil {
				t.Fatal(err)
			}
		}
	}
	// 1.2MiB stored. A summary-only commit on host 3 enforces the global limit.
	if err = s.Ingest(3, Batch{Version: CollectorVersion, Latest: ptr(reading(now, 10, 10)), Incidents: []monitor.Incident{{ID: "1", Start: now + 31, Updated: now + 99}}}); err != nil {
		t.Fatal(err)
	}
	if err = s.Flush(); err != nil {
		t.Fatal(err)
	}
	assertDetailCounters(t, s)
	var total int64
	s.DB.QueryRow(`SELECT value FROM metadata WHERE key='detail_bytes'`).Scan(&total)
	if total > 1<<20 {
		t.Fatalf("installation detail %d over budget", total)
	}
	var evicted []string
	rows, _ := s.DB.Query(`SELECT host||'/'||id FROM incidents WHERE detail IS NULL ORDER BY updated`)
	for rows.Next() {
		var k string
		rows.Scan(&k)
		evicted = append(evicted, k)
	}
	rows.Close()
	// Exactly the single oldest detail goes; summaries all remain.
	if strings.Join(evicted, ",") != "1/0" {
		t.Fatalf("evicted %v", evicted)
	}
	var count, hostEvicted int
	s.DB.QueryRow(`SELECT count(*) FROM incidents`).Scan(&count)
	s.DB.QueryRow(`SELECT evicted FROM hosts WHERE id=1`).Scan(&hostEvicted)
	if count != 6 || hostEvicted != 1 {
		t.Fatalf("summaries=%d host evicted=%d", count, hostEvicted)
	}
}

func TestDetailBudgetQueriesUseIndexWithoutSorting(t *testing.T) {
	s, err := OpenStore(filepath.Join(t.TempDir(), "monitor.db"), DefaultBudget)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	for _, q := range []struct {
		sql, want string
		args      []any
	}{
		{`SELECT host,id,length(detail) FROM incidents INDEXED BY incidents_host_detail WHERE host=? AND detail IS NOT NULL ORDER BY updated,id LIMIT ?`, "INDEX incidents_host_detail (host=?)", []any{1, 16}},
		{`SELECT host,id,length(detail) FROM incidents INDEXED BY incidents_detail_budget WHERE detail IS NOT NULL ORDER BY updated,host DESC,id DESC LIMIT ?`, "INDEX incidents_detail_budget", []any{16}},
		{`SELECT coalesce(sum(length(detail)),0) FROM incidents INDEXED BY incidents_host_detail WHERE host=? AND detail IS NOT NULL`, "INDEX incidents_host_detail (host=?)", []any{1}},
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
		rows.Close()
		if !strings.Contains(plan.String(), q.want) || strings.Contains(plan.String(), "TEMP B-TREE") {
			t.Fatalf("%s\nplan: %s", q.sql, plan.String())
		}
	}
}
