package main

// This harness is deliberately source-compatible with Telephony 0.11.2.
// Copy this exact file to the control checkout to compare identical workloads.
import (
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"os"
	"runtime"
	"runtime/pprof"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

type queryPerformanceResult struct {
	Name        string  `json:"name"`
	Iterations  int     `json:"iterations"`
	WallMS      float64 `json:"wall_ms_per_operation"`
	CPUMS       float64 `json:"cpu_ms_per_operation"`
	Bytes       uint64  `json:"allocated_bytes_per_operation"`
	Allocations uint64  `json:"allocations_per_operation"`
}

func queryProcessCPU() float64 {
	var r syscall.Rusage
	_ = syscall.Getrusage(syscall.RUSAGE_SELF, &r)
	return float64(r.Utime.Sec+r.Stime.Sec)*1000 + float64(r.Utime.Usec+r.Stime.Usec)/1000
}
func queryPerformanceSeed(t *testing.T, a *App) {
	t.Helper()
	phoneTestPolicy(t, a)
	tx, e := a.db().db.Begin()
	if e != nil {
		t.Fatal(e)
	}
	defer tx.Rollback()
	insert, e := tx.Prepare(`INSERT INTO calls(id,thread_id,to_number,from_number,directive,voice,audio_bridge_url,status,placed_at,ended_at,project_id,direction,peer_kind,routing_destination_id,browser_audio_diagnostics,carrier_audio_diagnostics,carrier_signaling_json) VALUES(?,?,'+13502231050','+13334445555','','','','completed',?,?,'project-a','inbound','human',?,?,?,?)`)
	if e != nil {
		t.Fatal(e)
	}
	defer insert.Close()
	owner, e := tx.Prepare(`INSERT INTO telephony_call_owners(call_id,project_id,principal,destination_id) VALUES(?,'project-a',?,'sales')`)
	if e != nil {
		t.Fatal(e)
	}
	defer owner.Close()
	rec, e := tx.Prepare(`INSERT INTO recordings(id,call_id,project_id,provider,carrier_connection_id,provider_recording_id,storage_status,created_at) VALUES(?,?,'project-a','twilio',1,?,'stored','2026-01-01T00:00:00Z')`)
	if e != nil {
		t.Fatal(e)
	}
	defer rec.Close()
	diagnostics := `{"details":"` + strings.Repeat("x", 4096) + `"}`
	for i := 0; i < 6000; i++ {
		id := fmt.Sprintf("history-%04d", i)
		dest := "support"
		if i >= 5900 {
			dest = "sales"
		}
		placed := time.Date(2026, 1, 1, 0, 0, i, 0, time.UTC).Format(time.RFC3339)
		if _, e = insert.Exec(id, "thread-"+id, placed, placed, dest, diagnostics, diagnostics, diagnostics); e != nil {
			t.Fatal(e)
		}
		if i >= 5900 {
			if _, e = owner.Exec(id, phoneTestIdentity("alice").key()); e != nil {
				t.Fatal(e)
			}
		}
		if i < 5906 {
			if _, e = rec.Exec("rec-"+id, id, id); e != nil {
				t.Fatal(e)
			}
		}
	}
	if e = tx.Commit(); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 10; i++ {
		phoneTestRingOfferCall(t, a, fmt.Sprintf("live-%02d", i))
	}
	if _, e = a.db().db.Exec(`UPDATE call_offers SET expires_at='2099-01-01T00:00:00.000000000Z' WHERE status='offered'`); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 10; i++ {
		id := fmt.Sprintf("occupied-%02d", i)
		phoneTestCall(t, a, id, "in-progress")
		if _, e = a.db().db.Exec(`INSERT INTO phone_capacity(call_id,project_id,principal,destination_id,expires_at) VALUES(?,'project-a',?,'sales','')`, id, fmt.Sprint(i)); e != nil {
			t.Fatal(e)
		}
	}
}
func TestQueryEfficiencyPerformance(t *testing.T) {
	if os.Getenv("TELEPHONY_QUERY_BENCHMARK") != "1" {
		t.Skip("opt-in local database performance comparison")
	}
	softphoneTestCtx(t)
	a := &App{installID: 42}
	queryPerformanceSeed(t, a)
	p, e := a.phonePrincipal("project-a", phoneTestIdentity("alice"))
	if e != nil {
		t.Fatal(e)
	}
	request := withPhonePrincipal(httptest.NewRequest("GET", "/calls", nil), p)
	iterations := 150
	if n, e := strconv.Atoi(os.Getenv("TELEPHONY_QUERY_ITERATIONS")); e == nil && n > 0 {
		iterations = n
	}
	var results []queryPerformanceResult
	profile := os.Getenv("TELEPHONY_QUERY_CPU_PROFILE")
	if profile != "" {
		f, e := os.Create(profile)
		if e != nil {
			t.Fatal(e)
		}
		if e = pprof.StartCPUProfile(f); e != nil {
			t.Fatal(e)
		}
		defer f.Close()
		defer pprof.StopCPUProfile()
	}
	run := func(name string, op func(i int) error) {
		t.Helper()
		if e := op(0); e != nil {
			t.Fatal(e)
		}
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		cpu := queryProcessCPU()
		start := time.Now()
		count := iterations
		if strings.HasSuffix(name, "_write") {
			count *= 10
		}
		for i := 0; i < count; i++ {
			if e := op(i); e != nil {
				t.Fatal(e)
			}
		}
		wall := time.Since(start)
		cpu = queryProcessCPU() - cpu
		runtime.ReadMemStats(&after)
		result := queryPerformanceResult{name, count, float64(wall.Nanoseconds()) / 1e6 / float64(count), cpu / float64(count), (after.TotalAlloc - before.TotalAlloc) / uint64(count), (after.Mallocs - before.Mallocs) / uint64(count)}
		results = append(results, result)
		raw, _ := json.Marshal(result)
		t.Log(string(raw))
	}
	change := func(i int) error {
		_, e := a.db().db.Exec(`UPDATE calls SET media_error_message=? WHERE id='live-00'`, fmt.Sprint(i))
		return e
	}
	run("call_list_changing", func(i int) error {
		if e := change(i); e != nil {
			return e
		}
		w := httptest.NewRecorder()
		a.handleListCalls(w, request)
		if w.Code != 200 {
			return fmt.Errorf("list %d %s", w.Code, w.Body)
		}
		return nil
	})
	run("call_list_repeated", func(i int) error {
		w := httptest.NewRecorder()
		a.handleListCalls(w, request)
		if w.Code != 200 {
			return fmt.Errorf("list %d", w.Code)
		}
		return nil
	})
	run("single_call_recording_summary", func(i int) error {
		return a.db().attachRecordingSummaries("project-a", []callRow{{ID: "history-5905"}})
	})
	run("sse_changing", func(i int) error {
		if e := change(i); e != nil {
			return e
		}
		_, e := a.callNotificationSnapshot(request, "project-a")
		return e
	})
	run("sse_heartbeat", func(i int) error { _, e := a.callNotificationSnapshot(request, "project-a"); return e })
	run("capacity_cleanup", func(i int) error {
		tx, e := a.db().db.Begin()
		if e != nil {
			return e
		}
		if e = cleanupCapacityTx(tx, time.Now()); e != nil {
			tx.Rollback()
			return e
		}
		return tx.Commit()
	})
	run("call_state_write", func(i int) error { return change(i) })
	run("audio_diagnostics_write", func(i int) error {
		_, e := a.db().db.Exec(`UPDATE calls SET browser_audio_diagnostics=? WHERE id='live-00'`, fmt.Sprintf(`{"sequence":%d}`, i))
		return e
	})
	output := os.Getenv("TELEPHONY_QUERY_OUTPUT")
	if output != "" {
		raw, e := json.MarshalIndent(struct {
			GoVersion                        string `json:"go_version"`
			Calls, Recordings, CapacitySlots int
			Results                          []queryPerformanceResult `json:"results"`
		}{runtime.Version(), 6020, 5906, 10, results}, "", "  ")
		if e != nil {
			t.Fatal(e)
		}
		if e = os.WriteFile(output, raw, 0600); e != nil {
			t.Fatal(e)
		}
	}
}
