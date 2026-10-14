package main

import (
	"context"
	"encoding/json"
	sdk "github.com/apteva/app-sdk"
	"os"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestRuntimeOptimizationPerformance(t *testing.T) {
	output := os.Getenv("TELEPHONY_RUNTIME_OPT_RESULTS")
	if output == "" {
		t.Skip("opt-in isolated read/CPU/allocation measurements")
	}
	a, ctx := withTelephonyTestContext(t, &answerPlatform{})
	phoneTestPolicy(t, a)
	row := phoneTestCall(t, a, "perf-history", "answered")
	now := time.Now()
	for i := 0; i < 256; i++ {
		record := carrierTelemetryRecord{ID: newCallID(), ProjectID: row.ProjectID, CallID: row.ID, At: ringTime(now), Kind: "event"}
		raw, _ := json.Marshal(record)
		raw = append(raw[:len(raw)-1], []byte(`,"extra":"`+strings.Repeat("x", 1200)+`"}`)...)
		if _, e := ctx.AppDB().Exec(`INSERT INTO telephony_carrier_transport_history VALUES(?,?,?,?,?,?,?)`, record.ID, row.ProjectID, row.ID, "event", record.At, ringTime(now.Add(time.Hour)), string(raw)); e != nil {
			t.Fatal(e)
		}
	}
	page, e := a.db().carrierHistoryPage(t.Context(), row.ProjectID, row.ID, "")
	if e != nil {
		t.Fatal(e)
	}
	var results []queryPerformanceResult
	measure := func(name string, fn func() error) {
		t.Helper()
		if e := fn(); e != nil {
			t.Fatal(e)
		}
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		cpu := queryProcessCPU()
		start := time.Now()
		const n = 200
		for i := 0; i < n; i++ {
			if e := fn(); e != nil {
				t.Fatal(e)
			}
		}
		elapsed := time.Since(start)
		cost := queryProcessCPU() - cpu
		runtime.ReadMemStats(&after)
		results = append(results, queryPerformanceResult{Name: name, Iterations: n, WallMS: float64(elapsed) / float64(time.Millisecond) / n, CPUMS: cost / n, Bytes: (after.TotalAlloc - before.TotalAlloc) / n, Allocations: (after.Mallocs - before.Mallocs) / n})
	}
	measure("history_full", func() error { _, _, e := a.db().carrierTransportEvidence(t.Context(), row.ProjectID, row.ID); return e })
	measure("history_incremental_unchanged", func() error {
		_, e := a.db().carrierHistoryPage(t.Context(), row.ProjectID, row.ID, page.Cursor)
		return e
	})
	policy, _ := a.phonePolicy(row.ProjectID)
	for i := 0; i < 80; i++ {
		u := policy.Users[0]
		u.Identity.SubjectID = strings.Repeat("u", 32)
		policy.Users = append(policy.Users, u)
	}
	raw, _ := json.Marshal(policy)
	if _, e = ctx.AppDB().Exec(`UPDATE telephony_access_policies SET policy_json=?,revision=revision+1 WHERE project_id=?`, string(raw), row.ProjectID); e != nil {
		t.Fatal(e)
	}
	measure("policy_double_parse", func() error {
		for i := 0; i < 2; i++ {
			if _, e := a.phonePolicy(row.ProjectID); e != nil {
				return e
			}
		}
		return nil
	})
	measure("policy_double_cached", func() error {
		for i := 0; i < 2; i++ {
			if _, e := a.cachedPhonePolicy(row.ProjectID); e != nil {
				return e
			}
		}
		return nil
	})
	if e = a.db().refreshAudioDashboard(t.Context()); e != nil {
		t.Fatal(e)
	}
	f, _ := parseAudioDashboardFilterForBenchmark(now)
	measure("dashboard_full", func() error { _, e := a.db().audioDashboard(t.Context(), row.ProjectID, f, now); return e })
	light := f
	light.OmitFacets = true
	light.OmitTotals = true
	measure("dashboard_light", func() error { _, e := a.db().audioDashboard(t.Context(), row.ProjectID, light, now); return e })
	measure("dashboard_shared_extras", func() error {
		_, e := a.db().audioDashboard(t.Context(), row.ProjectID, f, now, &a.dashboardExtras)
		return e
	})
	jobs := []struct {
		name string
		run  func(context.Context, *sdk.AppCtx) error
	}{{"ai-call-policies", a.runAICallPolicies}, {"carrier-media-recovery", a.runCarrierMediaRecovery}, {"carrier-activations", a.runCarrierActivations}, {"ai-handoffs", a.runAIHandoffs}}
	measure("empty_workers_original", func() error {
		for _, j := range jobs {
			if e := j.run(t.Context(), ctx); e != nil {
				return e
			}
		}
		return nil
	})
	measure("empty_workers_gated", func() error {
		for _, j := range jobs {
			if e := a.runIdleGated(j.name, j.run, t.Context(), ctx); e != nil {
				return e
			}
		}
		return nil
	})
	data, _ := json.MarshalIndent(map[string]any{"results": results, "policy_bytes": len(raw), "history_records": 256, "history_record_extra_bytes": 1200, "go_version": runtime.Version()}, "", "  ")
	if e = os.WriteFile(output, data, 0600); e != nil {
		t.Fatal(e)
	}
	t.Log(string(data))
}
func parseAudioDashboardFilterForBenchmark(now time.Time) (audioDashboardFilter, error) {
	return audioDashboardFilter{From: now.Add(-time.Hour).UnixMilli(), Until: now.Add(time.Second).UnixMilli(), Limit: 50}, nil
}
