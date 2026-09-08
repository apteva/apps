package main

import (
	"context"
	"encoding/json"
	"sort"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestParallelDefaultRunsBeyondEightWithoutCPUTelemetry(t *testing.T) {
	requireBin(t, "node")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	fn := createFn(t, app, ctx, map[string]any{"name": "parallel-twelve", "max_memory_mb": 128, "source": `export default async e=>{const start=Date.now();await new Promise(r=>setTimeout(r,1500));return {id:e.id,start,end:Date.now()}}`})
	if err := currentPool().awaitPreparation(context.Background(), fn); err != nil {
		t.Fatal(err)
	}
	type result struct {
		r *invokeResult
		e error
	}
	done := make(chan result, 12)
	for i := 0; i < 12; i++ {
		go func(i int) {
			r, e := invokeFunction(ctx, context.Background(), fn, map[string]any{"id": i}, "test")
			done <- result{r, e}
		}(i)
	}
	type point struct {
		at    int64
		delta int
	}
	points := []point{}
	seen := map[int]bool{}
	for i := 0; i < 12; i++ {
		out := <-done
		if out.e != nil || out.r == nil || out.r.Status != "ok" {
			t.Errorf("invocation: %+v %v", out.r, out.e)
			continue
		}
		var body struct {
			ID         int
			Start, End int64
		}
		if err := json.Unmarshal([]byte(out.r.Response), &body); err != nil {
			t.Fatal(err)
		}
		seen[body.ID] = true
		points = append(points, point{body.Start, 1}, point{body.End, -1})
		if out.r.Resources.AutomaticWaitMS != 0 {
			t.Error("automatic gate queued invocation")
		}
	}
	sort.Slice(points, func(i, j int) bool {
		if points[i].at == points[j].at {
			return points[i].delta < points[j].delta
		}
		return points[i].at < points[j].at
	})
	active, peak := 0, 0
	for _, p := range points {
		active += p.delta
		peak = max(peak, active)
	}
	if peak <= 8 || len(seen) != 12 {
		t.Fatalf("peak=%d distinct results=%d", peak, len(seen))
	}
	t.Logf("peak simultaneous workers=%d; 12 distinct results; no automatic wait", peak)
}

func TestParallelDefaultPreservesExplicitPolicy(t *testing.T) {
	if policy(&Function{}).Concurrency != 0 {
		t.Fatal("implicit per-function limit")
	}
	if policy(&Function{Limits: RuntimePolicy{Concurrency: 2}}).Concurrency != 2 {
		t.Fatal("explicit limit lost")
	}
}
