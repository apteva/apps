package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func occupancyExec(t testing.TB, db *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

const occupancyInsertCall = `INSERT INTO calls(id,thread_id,to_number,from_number,directive,voice,audio_bridge_url,status,placed_at,project_id,peer_kind,direction)
 VALUES(?,?,'','','','','',?,'2026-10-22T08:00:00Z',?,?,?)`

func occupancyCall(t testing.TB, db *sql.DB, id, project, direction, status, peer string, owner *phoneIdentity) {
	t.Helper()
	occupancyExec(t, db, occupancyInsertCall, id, "thread-"+id, status, project, peer, direction)
	if owner != nil {
		occupancyExec(t, db, `INSERT INTO telephony_call_owners(call_id,project_id,principal) VALUES(?,?,?)`, id, project, owner.key())
	}
}

func TestLiveOccupancyLifecycleAndIdentity(t *testing.T) {
	db := testCallsDB(t).db
	alice := phoneTestIdentity("145")
	alice.IssuerInstallID = "18"
	alice.OrganizationID = "1"
	other := alice
	other.IssuerInstallID = "19" // Same subject ID, different identity.
	active := []string{"pending", "answering", "initiated", "ringing", "answered", "in-progress", "dialing"}
	for i, status := range active {
		direction := "outbound"
		if i%2 == 0 {
			direction = "inbound"
		}
		occupancyCall(t, db, status, "project-a", direction, status, peerKindHuman, &alice)
	}
	occupancyCall(t, db, "other-identity", "project-a", "outbound", "answered", peerKindHuman, &other)
	for _, status := range []string{"completed", "failed", "busy", "no-answer", "canceled"} {
		occupancyCall(t, db, status, "project-a", "outbound", status, peerKindHuman, &alice)
	}
	occupancyCall(t, db, "ai", "project-a", "outbound", "answered", "realtime", &alice)
	occupancyCall(t, db, "unowned", "project-a", "inbound", "pending", peerKindHuman, nil)
	occupancyCall(t, db, "released", "project-a", "inbound", "answering", peerKindHuman, &alice)
	// Actual release transition removes ownership through the existing trigger.
	occupancyExec(t, db, `UPDATE calls SET status='pending' WHERE id='released'`)
	occupancyCall(t, db, "other-project", "project-b", "outbound", "answered", peerKindHuman, &alice)
	occupancyCall(t, db, "mismatched-project", "project-a", "outbound", "answered", peerKindHuman, &alice)
	occupancyExec(t, db, `UPDATE telephony_call_owners SET project_id='project-b' WHERE call_id='mismatched-project'`)
	// Reservations and offers are separate from existing call ownership.
	occupancyExec(t, db, `INSERT INTO phone_capacity(project_id,principal,call_id,destination_id,expires_at) VALUES('project-a',?,'unowned','sales','2099-01-01T00:00:00Z')`, alice.key())

	start := time.Now()
	snapshot, err := readLiveOccupancy(t.Context(), db, "project-a", "")
	if err != nil {
		t.Fatal(err)
	}
	if !snapshot.Complete || len(snapshot.Calls) != len(active)+1 {
		t.Fatalf("incomplete/wrong occupancy: %+v", snapshot)
	}
	at, err := time.Parse(time.RFC3339Nano, snapshot.SnapshotAt)
	if err != nil || at.Before(start) || at.After(time.Now()) || !strings.HasSuffix(snapshot.SnapshotAt, "Z") {
		t.Fatalf("invalid UTC observation timestamp %q: %v", snapshot.SnapshotAt, err)
	}
	for _, call := range snapshot.Calls {
		want := alice
		if call.CallID == "other-identity" {
			want = other
		}
		if call.OwnerIdentity != want || isTerminalStatus(call.Status) {
			t.Fatalf("wrong identity/lifecycle: %+v", call)
		}
	}
	for _, direction := range []string{"inbound", "outbound"} {
		filtered, err := readLiveOccupancy(t.Context(), db, "project-a", direction)
		if err != nil || !filtered.Complete {
			t.Fatal(filtered, err)
		}
		count := 0
		for _, call := range snapshot.Calls {
			if call.Direction == direction {
				count++
			}
		}
		if len(filtered.Calls) != count {
			t.Fatalf("wrong %s count: %+v", direction, filtered)
		}
		for _, call := range filtered.Calls {
			if call.Direction != direction {
				t.Fatal("direction filter leaked", call)
			}
		}
	}
	var reservations int
	if err := db.QueryRow(`SELECT COUNT(*) FROM phone_capacity`).Scan(&reservations); err != nil || reservations != 1 {
		t.Fatal("read mutated reservations", reservations, err)
	}
}

func TestLiveOccupancyCompleteEmptyAndUnpaginated(t *testing.T) {
	db := testCallsDB(t).db
	snapshot, err := readLiveOccupancy(t.Context(), db, "project-a", "")
	if err != nil || !snapshot.Complete || snapshot.Calls == nil || len(snapshot.Calls) != 0 {
		t.Fatal(snapshot, err)
	}
	raw, _ := json.Marshal(snapshot)
	if !strings.Contains(string(raw), `"calls":[]`) {
		t.Fatal(string(raw))
	}
	owner := phoneTestIdentity("alice")
	owner.OrganizationID = "" // Optional in the existing identity contract.
	for i := 0; i < 305; i++ {
		occupancyCall(t, db, fmt.Sprintf("call-%03d", i), "project-a", "outbound", "ringing", peerKindHuman, &owner)
	}
	snapshot, err = readLiveOccupancy(t.Context(), db, "project-a", "outbound")
	if err != nil || !snapshot.Complete || len(snapshot.Calls) != 305 {
		t.Fatal("snapshot truncated", snapshot, err)
	}
}

func TestLiveOccupancyErrorsNeverReturnPartialSuccess(t *testing.T) {
	for _, principal := range []string{"not-json", `null`, `{}`, `{"issuer_app":"auth","issuer_install_id":"18","subject_type":"agent","subject_id":"145"}`} {
		t.Run(principal, func(t *testing.T) {
			db := testCallsDB(t).db
			owner := phoneTestIdentity("alice")
			occupancyCall(t, db, "a-valid", "project-a", "outbound", "answered", peerKindHuman, &owner)
			occupancyCall(t, db, "z-invalid", "project-a", "outbound", "answered", peerKindHuman, &owner)
			occupancyExec(t, db, `UPDATE telephony_call_owners SET principal=? WHERE call_id='z-invalid'`, principal)
			got, err := readLiveOccupancy(t.Context(), db, "project-a", "")
			if err == nil || got != nil {
				t.Fatal("invalid owner returned successful/partial snapshot", got, err)
			}
		})
	}
	t.Run("database", func(t *testing.T) {
		db := testCallsDB(t).db
		occupancyExec(t, db, `DROP TABLE telephony_call_owners`)
		if got, err := readLiveOccupancy(t.Context(), db, "project-a", ""); got != nil || err == nil {
			t.Fatal("read failure treated as empty", got, err)
		}
		if err := db.Close(); err != nil {
			t.Fatal(err)
		}
		if got, err := readLiveOccupancy(t.Context(), db, "project-a", ""); got != nil || err == nil {
			t.Fatal("closed DB treated as empty", got, err)
		}
	})
	t.Run("cancellation", func(t *testing.T) {
		db := testCallsDB(t).db
		conn, err := db.Conn(t.Context()) // Hold the only pool connection.
		if err != nil {
			t.Fatal(err)
		}
		defer conn.Close()
		ctx, cancel := context.WithCancel(t.Context())
		cancel()
		if got, err := readLiveOccupancy(ctx, db, "project-a", ""); got != nil || !errors.Is(err, context.Canceled) {
			t.Fatal("canceled read treated as empty", got, err)
		}
		waiting, stop := context.WithTimeout(t.Context(), 20*time.Millisecond)
		defer stop()
		if got, err := readLiveOccupancy(waiting, db, "project-a", ""); got != nil || !errors.Is(err, context.DeadlineExceeded) {
			t.Fatal("waiting read ignored deadline or returned empty success", got, err)
		}
	})
}

func TestLiveOccupancyToolPermissionsProjectAndDrain(t *testing.T) {
	ctx := softphoneTestCtx(t)
	a := &App{installID: 42, runtime: runtimeTestActive()}
	a.runtime.state = "draining"
	owner := phoneTestIdentity("alice")
	occupancyCall(t, ctx.AppDB(), "a", "project-a", "outbound", "answered", peerKindHuman, &owner)
	occupancyCall(t, ctx.AppDB(), "b", "project-b", "outbound", "answered", peerKindHuman, &owner)
	for _, tool := range a.MCPTools() {
		if tool.Name != "telephony_live_occupancy" {
			continue
		}
		if tool.Annotations["readOnlyHint"] != true || tool.Annotations["openWorldHint"] != false {
			t.Fatal("missing read-only/local tool annotation", tool.Annotations)
		}
		got, err := tool.HandlerCtx(t.Context(), ctx, map[string]any{"project_id": "project-b", "_project_id": "project-b"})
		if err != nil {
			t.Fatal("read blocked during drain", err)
		}
		snapshot, ok := got.(*liveOccupancySnapshot)
		if !ok || !snapshot.Complete || len(snapshot.Calls) != 1 || snapshot.Calls[0].CallID != "a" {
			t.Fatal("untrusted project override", got)
		}
		got, err = tool.HandlerCtx(t.Context(), ctx.WithProject("project-b"), nil)
		if err != nil || got.(*liveOccupancySnapshot).Calls[0].CallID != "b" {
			t.Fatal("trusted project not used", got, err)
		}
		for _, args := range []map[string]any{{"direction": 1}, {"direction": "sideways"}, {"direction": ""}, {"limit": 1}, {"cursor": "x"}, {"offset": 20}} {
			got, err := tool.HandlerCtx(t.Context(), ctx, args)
			if err == nil || got != nil {
				t.Fatal("invalid argument accepted", args, got, err)
			}
		}
		previousProject := os.Getenv("APTEVA_PROJECT_ID")
		t.Setenv("APTEVA_PROJECT_ID", "")
		got, err = tool.HandlerCtx(t.Context(), ctx.WithProject(""), nil)
		if err == nil || got != nil {
			t.Fatal("missing project accepted", got, err)
		}
		t.Setenv("APTEVA_PROJECT_ID", previousProject)
		_, err = tool.HandlerCtx(sdk.WithCaller(t.Context(), &sdk.Caller{SubjectType: "user", SubjectID: "alice"}), ctx, nil)
		if err == nil {
			t.Fatal("application user bypassed operator tool permissions")
		}
		occupancyExec(t, ctx.AppDB(), `DROP TABLE telephony_call_owners`)
		got, err = tool.HandlerCtx(t.Context(), ctx, nil)
		if err == nil || got != nil {
			t.Fatal("tool swallowed read failure", got, err)
		}
		return
	}
	t.Fatal("tool not registered")
}

// File-backed WAL allows a writer to commit while a reader consumes its prior
// snapshot. Each transaction exposes exactly one valid status/identity pair.
func occupancyWALDB(t testing.TB) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:"+filepath.ToSlash(filepath.Join(t.TempDir(), "occupancy.db"))+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(2)
	t.Cleanup(func() { db.Close() })
	files, err := filepath.Glob("migrations/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, file := range files {
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		occupancyExec(t, db, string(raw))
	}
	return db
}

func TestLiveOccupancyConcurrentLifecycleSnapshot(t *testing.T) {
	db := occupancyWALDB(t)
	alice, bob := phoneTestIdentity("alice"), phoneTestIdentity("bob")
	occupancyCall(t, db, "changing", "project-a", "outbound", "pending", peerKindHuman, nil)
	occupancyCall(t, db, "stable", "project-a", "outbound", "answered", peerKindHuman, &alice)
	start, done := make(chan struct{}), make(chan error, 1)
	go func() {
		<-start
		for i := 0; i < 200; i++ {
			status, owner := "answering", alice.key()
			switch i % 4 {
			case 1:
				status, owner = "answered", bob.key()
			case 2:
				status, owner = "completed", bob.key()
			case 3:
				status, owner = "pending", ""
			}
			tx, err := db.Begin()
			if err == nil {
				_, err = tx.Exec(`UPDATE calls SET status=? WHERE id='changing'`, status)
				if err == nil {
					_, err = tx.Exec(`DELETE FROM telephony_call_owners WHERE call_id='changing'`)
				}
				if err == nil && owner != "" {
					_, err = tx.Exec(`INSERT INTO telephony_call_owners(call_id,project_id,principal) VALUES('changing','project-a',?)`, owner)
				}
				if err == nil {
					err = tx.Commit()
				} else {
					tx.Rollback()
				}
			}
			if err != nil {
				done <- err
				return
			}
		}
		done <- nil
	}()
	close(start)
	var readErr error
	for i := 0; i < 300; i++ {
		snapshot, err := readLiveOccupancy(t.Context(), db, "project-a", "")
		if err != nil {
			readErr = err
			break
		}
		if !snapshot.Complete || len(snapshot.Calls) < 1 || len(snapshot.Calls) > 2 {
			readErr = fmt.Errorf("incomplete snapshot: %+v", snapshot)
			break
		}
		for _, call := range snapshot.Calls {
			if call.CallID == "stable" && call.OwnerIdentity == alice {
				continue
			}
			if call.CallID == "changing" && ((call.Status == "answering" && call.OwnerIdentity == alice) || (call.Status == "answered" && call.OwnerIdentity == bob)) {
				continue
			}
			readErr = fmt.Errorf("mismatched status/owner snapshot: %+v", call)
		}
	}
	if err := <-done; err != nil {
		t.Fatal("writer", err)
	}
	if readErr != nil {
		t.Fatal(readErr)
	}
}

func seedOccupancyHistory(t testing.TB, db *sql.DB, history int) {
	t.Helper()
	// Bulk seed realistic retained owner rows without the full call loader.
	occupancyExec(t, db, `WITH RECURSIVE n(x) AS (SELECT 1 UNION ALL SELECT x+1 FROM n WHERE x<?)
 INSERT INTO calls(id,thread_id,to_number,from_number,directive,voice,audio_bridge_url,status,placed_at,project_id,peer_kind,direction)
 SELECT 'history-'||x,'history-thread-'||x,'','','','','','completed','2026-10-01T00:00:00Z','project-a','human','outbound' FROM n`, history)
	occupancyExec(t, db, `INSERT INTO telephony_call_owners(call_id,project_id,principal) SELECT id,project_id,? FROM calls WHERE id LIKE 'history-%'`, phoneTestIdentity("historical").key())
}

func TestLiveOccupancyHistoryQueryPlan(t *testing.T) {
	db := testCallsDB(t).db
	seedOccupancyHistory(t, db, 50000)
	owner := phoneTestIdentity("live")
	for i := 0; i < 12; i++ {
		occupancyCall(t, db, fmt.Sprintf("live-%02d", i), "project-a", "outbound", "answered", peerKindHuman, &owner)
	}
	// Corrupt, large optional diagnostics must not enter this narrow read path.
	occupancyExec(t, db, `UPDATE calls SET browser_audio_diagnostics=?,carrier_audio_diagnostics=? WHERE id LIKE 'live-%'`, strings.Repeat("x", 100000), strings.Repeat("x", 100000))
	for _, analyze := range []bool{false, true} {
		if analyze {
			occupancyExec(t, db, `ANALYZE`)
		}
		for _, direction := range []string{"", "outbound"} {
			args := []any{"project-a"}
			if direction != "" {
				args = append(args, direction)
			}
			rows, err := db.Query("EXPLAIN QUERY PLAN "+liveOccupancyQuery(direction), args...)
			if err != nil {
				t.Fatal(err)
			}
			var plan []string
			for rows.Next() {
				var a, b, c int
				var detail string
				if err := rows.Scan(&a, &b, &c, &detail); err != nil {
					t.Fatal(err)
				}
				plan = append(plan, detail)
			}
			if err := rows.Err(); err != nil {
				t.Fatal(err)
			}
			rows.Close()
			joined := strings.Join(plan, "\n")
			t.Logf("analyzed=%v direction=%q: %s", analyze, direction, joined)
			if len(plan) < 2 || !strings.Contains(plan[0], "SEARCH c USING COVERING INDEX idx_calls_live_occupancy") || !strings.Contains(plan[1], "SEARCH o USING INDEX sqlite_autoindex_telephony_call_owners_1") || strings.Contains(joined, "TEMP B-TREE") {
				t.Fatal("query scales with history or sorts unnecessarily:", joined)
			}
			snapshot, err := readLiveOccupancy(t.Context(), db, "project-a", direction)
			if err != nil || !snapshot.Complete || len(snapshot.Calls) != 12 {
				t.Fatal("large history changed result", snapshot, err)
			}
		}
	}
}

func BenchmarkLiveOccupancy(b *testing.B) {
	for _, history := range []int{0, 50000} {
		b.Run(fmt.Sprintf("history_%d", history), func(b *testing.B) {
			db := occupancyWALDB(b)
			if history > 0 {
				seedOccupancyHistory(b, db, history)
			}
			owner := phoneTestIdentity("live")
			for i := 0; i < 12; i++ {
				occupancyCall(b, db, fmt.Sprintf("live-%02d", i), "project-a", "outbound", "answered", peerKindHuman, &owner)
			}
			occupancyExec(b, db, `ANALYZE`)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				snapshot, err := readLiveOccupancy(b.Context(), db, "project-a", "outbound")
				if err != nil || len(snapshot.Calls) != 12 || !snapshot.Complete {
					b.Fatal(snapshot, err)
				}
			}
		})
	}
}
