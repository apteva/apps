package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

func withCaptureConfig(t *testing.T, global captureConfig, projects map[string]captureConfig) {
	t.Helper()
	captureMu.Lock()
	oldCfg, oldProjects := captureCfg, captureProjects
	captureCfg, captureProjects = global, projects
	captureMu.Unlock()
	t.Cleanup(func() {
		captureMu.Lock()
		captureCfg, captureProjects = oldCfg, oldProjects
		captureMu.Unlock()
	})
}

func firehose(t *testing.T, lines ...string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		for _, line := range lines {
			fmt.Fprintln(w, "data: "+line)
		}
	}))
	t.Cleanup(server.Close)
	return server
}

func scalarInt(t *testing.T, db *sql.DB, query string, args ...any) int64 {
	t.Helper()
	var n int64
	if err := db.QueryRow(query, args...).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

func insertInboxRow(t *testing.T, db *sql.DB, id, payload string, received int64, processed any) {
	t.Helper()
	if _, err := db.Exec(`INSERT INTO capture_inbox(identity,payload,received_at,processed_at) VALUES(?,?,?,?)`, id, payload, received, processed); err != nil {
		t.Fatal(err)
	}
}

// Events the install will not record must not be written to the inbox, but
// the cursor still moves past them.
func TestCaptureIgnoredEventsAreNotStored(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"))
	db := ctx.AppDB()
	withCaptureConfig(t, captureConfig{}, map[string]captureConfig{
		"p1":  {Enabled: true, Mode: "allowlist", Topics: []string{"order.*"}, SampleRate: 1},
		"off": {Enabled: false, Mode: "all", SampleRate: 1},
		"few": {Enabled: true, Mode: "all", SampleRate: 0},
	})
	server := firehose(t,
		`{"seq":1,"app":"shop","project_id":"p1","topic":"order.paid","time":"2026-01-01T00:00:00Z","data":{}}`,
		`{"seq":2,"app":"trading","project_id":"p1","topic":"portfolio.performance.updated","data":{}}`,
		`{"seq":3,"app":"analytics","project_id":"p1","topic":"order.progress","data":{}}`,
		`{"seq":4,"app":"shop","project_id":"off","topic":"order.paid","data":{}}`,
		`{"seq":5,"app":"shop","project_id":"few","topic":"order.paid","data":{}}`,
		`{"seq":6,"app":"deploy","project_id":"","topic":"deploy.release.live","data":{}}`,
	)
	var cursor uint64
	_ = streamFirehose(ctx, server.URL, "test", &cursor)
	if cursor != 6 {
		t.Fatalf("cursor=%d, want 6 (ignored events still advance it)", cursor)
	}
	if n := scalarInt(t, db, `SELECT COUNT(*) FROM capture_inbox`); n != 1 {
		t.Fatalf("inbox rows=%d, want only the accepted event", n)
	}
	if n, err := countEvents(db, Filter{ProjectID: "p1"}); err != nil || n != 1 {
		t.Fatalf("events=%d err=%v, want 1", n, err)
	}
}

// Inbox cleanup ignores the payload: processed rows past the cutoff expire
// whatever their project (or none, or unreadable JSON); unprocessed and
// recent rows stay.
func TestPruneCaptureInboxExpiresProjectlessAndKeepsPending(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"))
	db := ctx.AppDB()
	cutoff := int64(1_000_000)
	insertInboxRow(t, db, "old-empty", `{"project_id":"","app":"deploy"}`, 10, cutoff-5)
	insertInboxRow(t, db, "old-none", `{"app":"deploy"}`, 11, cutoff-4)
	insertInboxRow(t, db, "old-gone", `{"project_id":"removed"}`, 12, cutoff-3)
	insertInboxRow(t, db, "old-bad", `not json`, 13, cutoff-2)
	insertInboxRow(t, db, "old-p1", `{"project_id":"p1"}`, 14, cutoff-1)
	insertInboxRow(t, db, "recent", `{"project_id":"p1"}`, 15, cutoff+1)
	insertInboxRow(t, db, "pending-old", `{"project_id":""}`, 1, nil)
	n, err := pruneCaptureInbox(context.Background(), db, cutoff, 1000, time.Second)
	if err != nil || n != 5 {
		t.Fatalf("pruned=%d err=%v, want 5", n, err)
	}
	rows, err := db.Query(`SELECT identity FROM capture_inbox ORDER BY identity`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var left []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			t.Fatal(err)
		}
		left = append(left, id)
	}
	if strings.Join(left, ",") != "pending-old,recent" {
		t.Fatalf("left=%v, want pending-old and recent", left)
	}
}

// Cleanup drains several batches in one run, but stops when the time budget
// is spent and resumes on the next run.
func TestPruneCaptureInboxBatchesWithinBudget(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"))
	db := ctx.AppDB()
	tx, err := db.Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2500; i++ {
		if _, err := tx.Exec(`INSERT INTO capture_inbox(identity,payload,received_at,processed_at) VALUES(?,?,?,?)`, fmt.Sprintf("r%05d", i), `{}`, i, 100+i); err != nil {
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	n, err := pruneCaptureInbox(context.Background(), db, 1_000_000, 1000, 0)
	if err != nil || n != 1000 {
		t.Fatalf("spent budget: pruned=%d err=%v, want one batch of 1000", n, err)
	}
	if first := scalarInt(t, db, `SELECT MIN(processed_at) FROM capture_inbox`); first != 1100 {
		t.Fatalf("oldest left=%d, want the oldest rows deleted first", first)
	}
	n, err = pruneCaptureInbox(context.Background(), db, 1_000_000, 1000, time.Minute)
	if err != nil || n != 1500 {
		t.Fatalf("resume: pruned=%d err=%v, want remaining 1500", n, err)
	}
}

// The cleanup query must walk the processed_at index, never scan the table
// or read payloads.
func TestPruneCaptureInboxUsesIndex(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"))
	rows, err := ctx.AppDB().Query(`EXPLAIN QUERY PLAN `+captureInboxPruneSQL, 1, 1000)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	var plan []string
	for rows.Next() {
		var id, parent, unused int
		var detail string
		if err := rows.Scan(&id, &parent, &unused, &detail); err != nil {
			t.Fatal(err)
		}
		plan = append(plan, detail)
	}
	joined := strings.Join(plan, "\n")
	if !strings.Contains(joined, "USING INDEX ix_capture_pending") || strings.Contains(joined, "SCAN capture_inbox") || strings.Contains(joined, "TEMP B-TREE") {
		t.Fatalf("prune plan does not walk ix_capture_pending:\n%s", joined)
	}
	if strings.Contains(captureInboxPruneSQL, "json_") || strings.Contains(captureInboxPruneSQL, "payload") {
		t.Fatal("prune query reads payload JSON")
	}
}

// The SDK calls workers once per project; inbox cleanup must run once per
// interval for the whole install, including when there is no project.
func TestCaptureInboxWorkerRunsOncePerInterval(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"))
	db := ctx.AppDB()
	captureInboxRun.Lock()
	captureInboxRun.last = time.Time{}
	captureInboxRun.Unlock()
	old := time.Now().Add(-8 * 24 * time.Hour).UnixMilli()
	insertInboxRow(t, db, "a", `{"project_id":""}`, old, old)
	app := &App{}
	if err := app.captureInboxWorker(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if n := scalarInt(t, db, `SELECT COUNT(*) FROM capture_inbox`); n != 0 {
		t.Fatalf("first call left %d rows", n)
	}
	insertInboxRow(t, db, "b", `{"project_id":"p2"}`, old, old)
	if err := app.captureInboxWorker(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if n := scalarInt(t, db, `SELECT COUNT(*) FROM capture_inbox`); n != 1 {
		t.Fatalf("second call in the same interval did work again (rows=%d)", n)
	}
	// The per-project retention worker no longer touches the inbox.
	if err := app.retentionWorker(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if n := scalarInt(t, db, `SELECT COUNT(*) FROM capture_inbox`); n != 1 {
		t.Fatalf("retention worker touched the inbox (rows=%d)", n)
	}
}

// After an accepted event's inbox row expires, a replay of the same delivery
// (stream restart from an older cursor) must not record it twice.
func TestCaptureReplayAfterInboxPruneDoesNotDuplicate(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"))
	db := ctx.AppDB()
	withCaptureConfig(t, captureConfig{Enabled: true, Mode: "all", SampleRate: 1}, nil)
	server := firehose(t, `{"seq":7,"app":"shop","project_id":"p1","topic":"order.paid","time":"2026-01-01T00:00:00Z","data":{}}`)
	var cursor uint64
	_ = streamFirehose(ctx, server.URL, "test", &cursor)
	if _, err := db.Exec(`UPDATE capture_inbox SET processed_at=1`); err != nil {
		t.Fatal(err)
	}
	if n, err := pruneCaptureInbox(context.Background(), db, 1000, 1000, time.Second); err != nil || n != 1 {
		t.Fatalf("pruned=%d err=%v", n, err)
	}
	cursor = 0
	_ = streamFirehose(ctx, server.URL, "test", &cursor)
	if n, err := countEvents(db, Filter{ProjectID: "p1"}); err != nil || n != 1 {
		t.Fatalf("events=%d err=%v after replay, want 1", n, err)
	}
}
