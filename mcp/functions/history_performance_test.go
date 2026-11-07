package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

func historyTestDB(t *testing.T) *sql.DB {
	t.Helper()
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	db := ctx.AppDB()
	if err := migrateExecutionIdentity(context.Background(), db, nil); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "history.db")
	if _, err := db.Exec("VACUUM INTO ?", path); err != nil {
		t.Fatal(err)
	}
	fileDB, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(on)&_pragma=journal_mode(WAL)&_pragma=busy_timeout(2000)")
	if err != nil {
		t.Fatal(err)
	}
	fileDB.SetMaxOpenConns(1)
	t.Cleanup(func() { fileDB.Close() })
	db = fileDB

	_, err = db.Exec(`INSERT INTO functions(id,project_id,name,runtime,source_hash) VALUES
 (1,'a','first','node','hash'),(2,'a','second','node','hash'),(3,'b','private','node','hash')`)
	if err != nil {
		t.Fatal(err)
	}
	return db
}

func historyPlan(t *testing.T, db *sql.DB, query string, args ...any) string {
	t.Helper()
	rows, err := db.Query("EXPLAIN QUERY PLAN "+query, args...)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var details []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		details = append(details, detail)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	return strings.Join(details, "\n")
}

func TestHistoryPageUsesProjectIDIndex(t *testing.T) {
	db := historyTestDB(t)
	for _, cursor := range []int64{0, 1000} {
		query, args, err := invocationHistoryPageQuery("a", InvocationQuery{Limit: 200, Cursor: cursor})
		if err != nil {
			t.Fatal(err)
		}
		plan := historyPlan(t, db, query, args...)
		if !strings.Contains(plan, "ix_inv_project_id") || strings.Contains(plan, "TEMP B-TREE") {
			t.Fatalf("cursor %d: %s", cursor, plan)
		}
		if strings.Contains(query, "resources_json") || strings.Contains(query, "identity_json") {
			t.Fatal("page selection reads telemetry")
		}
		t.Logf("cursor=%d plan:\n%s", cursor, plan)
	}
	query, args := invocationHistoryDetailsQuery("a", []int64{1000, 999, 998})
	plan := historyPlan(t, db, query, args...)
	if strings.Contains(plan, "TEMP B-TREE") || strings.Contains(plan, "SCAN i") || !strings.Contains(plan, "SEARCH function_invocation_resources USING INTEGER PRIMARY KEY") || !strings.Contains(plan, "SEARCH function_invocation_identities USING INTEGER PRIMARY KEY") {
		t.Fatal(plan)
	}
	t.Logf("details plan:\n%s", plan)
}

func TestHistoryPaginationScopeFiltersAndTelemetry(t *testing.T) {
	db := historyTestDB(t)
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	for id := 1; id <= 603; id++ {
		function := 1 + (id % 3)
		project := "a"
		if function == 3 {
			project = "b"
		}
		status := "ok"
		if id%5 == 0 {
			status = "timeout"
		}
		if _, err = tx.Exec(`INSERT INTO function_invocations(id,project_id,function_id,status,trigger_kind,response_body,stderr) VALUES(?,?,?,?, 'http','hidden output','hidden stderr')`, id, project, function, status); err != nil {
			t.Fatal(err)
		}
		if _, err = tx.Exec(`INSERT INTO function_invocation_resources VALUES(?,?); INSERT INTO function_invocation_identities VALUES(?,?)`, id, fmt.Sprintf(`{"worker_cpu_seconds":%d}`, id), id, fmt.Sprintf(`{"subject":"caller-%d"}`, id)); err != nil {
			t.Fatal(err)
		}
	}
	// A mismatched function must not consume a page slot or leak its name.
	if _, err = tx.Exec(`INSERT INTO function_invocations(id,project_id,function_id,status,trigger_kind) VALUES(604,'a',3,'ok','http')`); err != nil {
		t.Fatal(err)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	for _, filter := range []InvocationQuery{{Limit: 200}, {Limit: 17, Status: "errors"}, {Limit: 31, FunctionID: 2}, {Limit: 13, FunctionID: 1, Status: "ok"}} {
		seen := map[int64]bool{}
		cursor := int64(0)
		for {
			filter.Cursor = cursor
			page, err := dbRecentInvocationsQuery(db, "a", filter)
			if err != nil {
				t.Fatal(err)
			}
			if len(page) > filter.Limit {
				t.Fatal("page exceeded limit")
			}
			previous := cursor
			if previous == 0 {
				previous = int64(^uint64(0) >> 1)
			}
			for _, row := range page {
				if row == nil || row.ID >= previous || seen[row.ID] || row.FunctionID == 3 {
					t.Fatalf("bad page row: %+v", row)
				}
				if filter.FunctionID > 0 && row.FunctionID != filter.FunctionID {
					t.Fatalf("wrong function: %+v", row)
				}
				if filter.Status == "errors" && row.Status != "timeout" || filter.Status == "ok" && row.Status != "ok" {
					t.Fatalf("wrong status: %+v", row)
				}
				if row.ResponseBody != "" || row.Stderr != "" {
					t.Fatal("summary leaked payload")
				}
				if !json.Valid(row.Resources) || !json.Valid(row.Identity) || !strings.Contains(string(row.Identity), fmt.Sprint(row.ID)) {
					t.Fatal("telemetry lost or mixed")
				}
				seen[row.ID] = true
				previous = row.ID
			}
			if len(page) < filter.Limit {
				break
			}
			cursor = page[len(page)-1].ID
			// Newly inserted calls must not shift subsequent keyset pages.
			if cursor > 0 && len(seen) == filter.Limit {
				if _, err = db.Exec(`INSERT INTO function_invocations(project_id,function_id,status,trigger_kind) VALUES('a',1,'ok','http')`); err != nil {
					t.Fatal(err)
				}
			}
		}
		if _, err = db.Exec("DELETE FROM function_invocations WHERE id > 604"); err != nil {
			t.Fatal(err)
		}
		var expected int
		where := "project_id='a' AND function_id<>3 AND id<=603"
		if filter.FunctionID > 0 {
			where += fmt.Sprintf(" AND function_id=%d", filter.FunctionID)
		}
		if filter.Status == "errors" {
			where += " AND status='timeout'"
		} else if filter.Status == "ok" {
			where += " AND status='ok'"
		}
		if err = db.QueryRow("SELECT count(*) FROM function_invocations WHERE " + where).Scan(&expected); err != nil {
			t.Fatal(err)
		}
		// All original matching calls must be traversed exactly once.
		original := 0
		for id := range seen {
			if id <= 603 {
				original++
			}
		}
		if original != expected {
			t.Fatalf("got %d original calls, want %d", original, expected)
		}
	}
	empty, err := dbRecentInvocationsQuery(db, "not-a-project", InvocationQuery{Limit: 200})
	if err != nil || len(empty) != 0 {
		t.Fatalf("scope leak: %d %v", len(empty), err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := dbRecentInvocationsContext(canceled, db, "a", InvocationQuery{Limit: 200}); err == nil {
		t.Fatal("canceled request succeeded")
	}
}

func TestHistoryIndexMigrationWriteLockAndRollback(t *testing.T) {
	db := historyTestDB(t)
	if _, err := db.Exec("DROP INDEX ix_inv_project_id"); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile("migrations/009_invocation_history_index.sql")
	if err != nil {
		t.Fatal(err)
	}
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	if _, err = tx.Exec(string(body)); err != nil {
		t.Fatal(err)
	}
	// A second writer cannot proceed while index creation's transaction is open.
	// Use the same database on a separate connection with a short busy timeout.
	var databasePath string
	if err = tx.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&databasePath); err != nil {
		t.Fatal(err)
	}
	other, err := sql.Open("sqlite", databasePath+"?_pragma=busy_timeout(1)")
	if err != nil {
		t.Fatal(err)
	}
	defer other.Close()
	_, err = other.Exec(`INSERT INTO function_invocations(project_id,function_id,status,trigger_kind) VALUES('a',1,'ok','http')`)
	if err == nil || !strings.Contains(strings.ToLower(err.Error()), "locked") {
		t.Fatalf("expected index write lock, got %v", err)
	}
	if err = tx.Rollback(); err != nil {
		t.Fatal(err)
	}
	var n int
	if err = db.QueryRow("SELECT count(*) FROM sqlite_master WHERE name='ix_inv_project_id'").Scan(&n); err != nil || n != 0 {
		t.Fatalf("index survived rollback: %d %v", n, err)
	}
	for i := 0; i < 2; i++ {
		if _, err = db.Exec(string(body)); err != nil {
			t.Fatal(err)
		}
	}
	if _, err = other.Exec(`INSERT INTO function_invocations(project_id,function_id,status,trigger_kind) VALUES('a',1,'ok','http')`); err != nil {
		t.Fatal(err)
	}
	var integrity string
	if err = db.QueryRow("PRAGMA integrity_check").Scan(&integrity); err != nil || integrity != "ok" {
		t.Fatalf("integrity %s %v", integrity, err)
	}
}

func TestHistoryMillions(t *testing.T) {
	if os.Getenv("RUN_FUNCTIONS_HISTORY_PERFORMANCE") != "1" {
		t.Skip("opt-in million-row, telemetry-heavy disk fixture")
	}
	db := historyTestDB(t)
	// Create the new index after populating history, as on a real upgrade.
	if _, err := db.Exec("DROP INDEX ix_inv_project_id"); err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	const count = 2000000
	// Set-based insertion avoids millions of driver round trips. Two projects and
	// two functions per target project reproduce the cross-function ordering bug.
	_, err := db.Exec(`WITH RECURSIVE seq(id) AS (SELECT 1 UNION ALL SELECT id+1 FROM seq WHERE id<2000000)
 INSERT INTO function_invocations(id,project_id,function_id,status,trigger_kind)
 SELECT id,CASE WHEN id%3=2 THEN 'b' ELSE 'a' END,1+(id%3),'ok','http' FROM seq`)
	if err != nil {
		t.Fatal(err)
	}
	payload := `{"padding":"` + strings.Repeat("x", 65536) + `"}`
	// 4,000 older rows plus 600 recent rows hold 64 KiB in each telemetry table.
	// The returned 200-row pages alone carry ~25 MiB; off-page telemetry must not
	// affect latency or be loaded. Keep this fixture under available local disk.
	for _, table := range []string{"function_invocation_resources", "function_invocation_identities"} {
		column := "resources_json"
		if strings.Contains(table, "identities") {
			column = "identity_json"
		}
		if _, err = db.Exec("INSERT INTO "+table+"(invocation_id,"+column+") SELECT id,? FROM function_invocations WHERE id<=4000 OR id>?", payload, count-600); err != nil {
			t.Fatal(err)
		}
	}
	t.Logf("fixture populated in %s", time.Since(start))
	query, args, err := invocationHistoryPageQuery("a", InvocationQuery{Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	before := historyPlan(t, db, query, args...)
	if !strings.Contains(before, "TEMP B-TREE") {
		t.Fatalf("fixture did not reproduce old sort:\n%s", before)
	}
	t.Logf("before migration:\n%s", before)
	body, err := os.ReadFile("migrations/009_invocation_history_index.sql")
	if err != nil {
		t.Fatal(err)
	}
	start = time.Now()
	if _, err = db.Exec(string(body)); err != nil {
		t.Fatal(err)
	}
	t.Logf("index creation=%s", time.Since(start))
	timings := []time.Duration{}
	for sample := 0; sample < 8; sample++ {
		cursor := int64(0)
		seen := map[int64]bool{}
		for pageNumber := 0; pageNumber < 2; pageNumber++ {
			q := InvocationQuery{Limit: 200, Cursor: cursor}
			query, args, err := invocationHistoryPageQuery("a", q)
			if err != nil {
				t.Fatal(err)
			}
			plan := historyPlan(t, db, query, args...)
			if strings.Contains(plan, "TEMP B-TREE") || !strings.Contains(plan, "ix_inv_project_id") {
				t.Fatal(plan)
			}
			start = time.Now()
			page, err := dbRecentInvocationsQuery(db, "a", q)
			duration := time.Since(start)
			timings = append(timings, duration)
			if err != nil || len(page) != 200 {
				t.Fatalf("page %d: %d rows %v", pageNumber, len(page), err)
			}
			for _, row := range page {
				if seen[row.ID] || row.FunctionID == 3 || cursor > 0 && row.ID >= cursor {
					t.Fatalf("duplicate, bad cursor, or scope leak: %d", row.ID)
				}
				seen[row.ID] = true
				if len(row.Resources) < 65536 || len(row.Identity) < 65536 {
					t.Fatal("large telemetry missing")
				}
			}
			cursor = page[len(page)-1].ID
		}
	}
	sort.Slice(timings, func(i, j int) bool { return timings[i] < timings[j] })
	var path string
	if err = db.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path); err != nil {
		t.Fatal(err)
	}
	var bytes int64
	for _, suffix := range []string{"", "-wal"} {
		if info, e := os.Stat(filepath.Clean(path) + suffix); e == nil {
			bytes += info.Size()
		}
	}
	t.Logf("rows=%d disk=%.2f GiB page=200 telemetry=64 KiB resources + 64 KiB identity; p50=%s p95=%s max=%s samples=%d", count, float64(bytes)/(1<<30), timings[len(timings)/2], timings[len(timings)*95/100], timings[len(timings)-1], len(timings))
	if timings[len(timings)-1] > 2*time.Second {
		t.Fatal("indexed page exceeded 2-second regression budget")
	}
}

func TestHistoryHTTPUsesReadPoolAndCursor(t *testing.T) {
	db := historyTestDB(t)
	if _, err := db.Exec(`INSERT INTO function_invocations(id,project_id,function_id,status,trigger_kind) VALUES(1,'a',1,'ok','http'),(2,'a',2,'error','http'),(3,'b',3,'ok','http')`); err != nil {
		t.Fatal(err)
	}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("a"))
	restore := ctx.SetAppReadDBForTest(db)
	defer restore()
	saved := globalCtx
	globalCtx = ctx
	defer func() { globalCtx = saved }()
	t.Setenv("APTEVA_PROJECT_ID", "")
	for _, tc := range []struct {
		query  string
		id     int64
		cursor string
	}{{"limit=1", 2, "2"}, {"limit=1&cursor=2", 1, "1"}, {"limit=1&status=errors", 2, "2"}} {
		recorder := httptest.NewRecorder()
		request := httptest.NewRequest("GET", "/invocations?project_id=a&"+tc.query, nil)
		(&App{}).handleHTTPInvocationsCollection(recorder, request)
		if recorder.Code != http.StatusOK {
			t.Fatal(recorder.Body.String())
		}
		var page struct {
			Invocations []*Invocation `json:"invocations"`
			NextCursor  string        `json:"next_cursor"`
		}
		if err := json.Unmarshal(recorder.Body.Bytes(), &page); err != nil {
			t.Fatal(err)
		}
		if len(page.Invocations) != 1 || page.Invocations[0].ID != tc.id || page.NextCursor != tc.cursor {
			t.Fatalf("bad cursor response: %+v", page)
		}
	}
}

func TestHistoryPageSnapshotSurvivesConcurrentDeletion(t *testing.T) {
	db := historyTestDB(t)
	if _, err := db.Exec(`INSERT INTO function_invocations(id,project_id,function_id,status,trigger_kind) VALUES(1,'a',1,'ok','http'),(2,'a',2,'error','http'); INSERT INTO function_invocation_resources VALUES(2,'{"worker_cpu_seconds":2}')`); err != nil {
		t.Fatal(err)
	}
	tx, err := db.BeginTx(context.Background(), &sql.TxOptions{ReadOnly: true})
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	query, args, err := invocationHistoryPageQuery("a", InvocationQuery{Limit: 200})
	if err != nil {
		t.Fatal(err)
	}
	rows, err := tx.Query(query, args...)
	if err != nil {
		t.Fatal(err)
	}
	var ids []int64
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		ids = append(ids, id)
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	var path string
	if err = tx.QueryRow("SELECT file FROM pragma_database_list WHERE name='main'").Scan(&path); err != nil {
		t.Fatal(err)
	}
	writer, err := sql.Open("sqlite", path+"?_pragma=foreign_keys(on)&_pragma=busy_timeout(1000)")
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	// WAL permits a writer to commit while the historical read snapshot is open.
	if _, err = writer.Exec("DELETE FROM functions WHERE id=2"); err != nil {
		t.Fatal(err)
	}
	query, args = invocationHistoryDetailsQuery("a", ids)
	rows, err = tx.Query(query, args...)
	if err != nil {
		t.Fatal(err)
	}
	found := map[int64]*Invocation{}
	for rows.Next() {
		row := &Invocation{}
		if err = scanInvocation(rows, row); err != nil {
			t.Fatal(err)
		}
		found[row.ID] = row
	}
	if err = rows.Err(); err != nil {
		t.Fatal(err)
	}
	rows.Close()
	if len(found) != 2 || string(found[2].Resources) != "{\"worker_cpu_seconds\":2}" {
		t.Fatalf("page changed after concurrent deletion: %+v", found)
	}
	if err = tx.Commit(); err != nil {
		t.Fatal(err)
	}
	next, err := dbRecentInvocationsQuery(db, "a", InvocationQuery{Limit: 200})
	if err != nil || len(next) != 1 || next[0].ID != 1 {
		t.Fatalf("new snapshot didn't see deletion: %+v %v", next, err)
	}
}
