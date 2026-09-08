package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
	"github.com/apteva/apps/mcp/functions/internal/admission"
)

func TestAutomaticRealConcurrentCPUInvocations(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	fn := createFn(t, app, ctx, map[string]any{"name": "cpu-concurrency", "source": `export default e=>{const start=Date.now();let n=0;while(Date.now()-start<1200){n=(n+1)%1000003}return {id:e.id,start,end:Date.now(),valid:n>=0}}`})
	p := currentPool()
	p.auto = admission.NewObserver()
	if err := p.awaitPreparation(context.Background(), fn); err != nil {
		t.Fatal(err)
	}
	start := make(chan struct{})
	results := make(chan *invokeResult, 2)
	var wg sync.WaitGroup
	for i := 0; i < 2; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			r, e := invokeFunction(ctx, context.Background(), fn, map[string]any{"id": i}, "test")
			if e != nil {
				t.Error(e)
			}
			results <- r
		}(i)
	}
	close(start)
	wg.Wait()
	close(results)
	var intervals []struct {
		ID         int `json:"id"`
		Start, End int64
		Valid      bool
	}
	maxWait := int64(0)
	for r := range results {
		if r == nil || r.Status != "ok" {
			t.Fatalf("invocation failed: %+v", r)
		}
		var v struct {
			ID         int `json:"id"`
			Start, End int64
			Valid      bool
		}
		if e := json.Unmarshal([]byte(r.Response), &v); e != nil {
			t.Fatal(e)
		}
		if !v.Valid {
			t.Fatal("incorrect calculation")
		}
		intervals = append(intervals, v)
		maxWait = max(maxWait, r.Resources.AutomaticWaitMS)
	}
	a, b := intervals[0], intervals[1]
	if a.Start > b.Start {
		a, b = b, a
	}
	if b.Start >= a.End {
		t.Fatalf("independent handlers were serialized: %+v %+v", a, b)
	}
	if maxWait != 0 {
		t.Fatalf("missing queue measurement: %d", maxWait)
	}
	if a.ID == b.ID {
		t.Fatal("distinct requests were coalesced")
	}
	t.Logf("Two distinct CPU requests: intervals %d ms and %d ms; automatic wait %d ms; concurrent execution", a.End-a.Start, b.End-b.Start, maxWait)
}

func TestAutomaticMixedCPUAndLightweightTraffic(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	p := currentPool()
	p.auto = admission.NewObserver()
	heavy := createFn(t, app, ctx, map[string]any{"name": "mixed-heavy", "source": `export default e=>{const start=Date.now();while(Date.now()-start<120){}return {centre:e.centre,start,end:Date.now()}}`})
	light := createFn(t, app, ctx, map[string]any{"name": "mixed-light", "source": `export default e=>({value:e.value})`})
	if err := p.awaitPreparation(context.Background(), heavy); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 4; i++ {
		r, e := invokeFunction(ctx, context.Background(), light, map[string]any{"value": i}, "test")
		if e != nil || r.Status != "ok" {
			t.Fatal(r, e)
		}
	}
	var wg sync.WaitGroup
	start := make(chan struct{})
	results := make(chan *invokeResult, 6)
	for i := 0; i < 6; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			<-start
			centre := fmt.Sprintf("centre-%d", i)
			if i == 5 {
				centre = "all"
			}
			r, e := invokeFunction(ctx, context.Background(), heavy, map[string]any{"centre": centre}, "test")
			if e != nil {
				t.Error(e)
			}
			results <- r
		}(i)
	}
	close(start)
	latencies := []time.Duration{}
	for i := 0; i < 12; i++ {
		at := time.Now()
		r, e := invokeFunction(ctx, context.Background(), light, map[string]any{"value": i}, "test")
		latencies = append(latencies, time.Since(at))
		if e != nil || r.Status != "ok" || !strings.Contains(r.Response, fmt.Sprintf(`"value":%d`, i)) {
			t.Fatalf("light result: %+v %v", r, e)
		}
		time.Sleep(20 * time.Millisecond)
	}
	wg.Wait()
	close(results)
	seen := map[string]bool{}
	intervals := [][2]int64{}
	for r := range results {
		if r == nil || r.Status != "ok" {
			t.Fatalf("heavy result: %+v", r)
		}
		var body struct {
			Centre     string
			Start, End int64
		}
		if e := json.Unmarshal([]byte(r.Response), &body); e != nil {
			t.Fatal(e)
		}
		seen[body.Centre] = true
		intervals = append(intervals, [2]int64{body.Start, body.End})
	}
	if len(seen) != 6 || !seen["all"] {
		t.Fatal("distinct centre inputs were lost", seen)
	}
	sort.Slice(intervals, func(i, j int) bool { return intervals[i][0] < intervals[j][0] })
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	p95 := latencies[len(latencies)-1]
	if p95 > 200*time.Millisecond {
		t.Fatalf("lightweight responsiveness regressed: %v", latencies)
	}
	t.Logf("6 distinct CPU calculations including all-centres completed independently; 12 lightweight calls remained correct; worst lightweight latency %s", p95)
}

func TestAutomaticHTTPDoesNotQueueBehindExistingInvocation(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	fn := createFn(t, app, ctx, map[string]any{"name": "parallel-http", "source": `export default e=>e`})
	p := currentPool()
	if err := p.awaitPreparation(context.Background(), fn); err != nil {
		t.Fatal(err)
	}
	permit, err := p.acquireAutomatic(context.Background(), fn)
	if err != nil {
		t.Fatal(err)
	}
	defer permit.Finish(admission.Result{})
	r := httptest.NewRequest("POST", "/fn/"+fn.Name+"?project_id="+testProj, strings.NewReader(`{"value":"independent"}`))
	w := httptest.NewRecorder()
	app.handleHTTPInvokeByName(w, r)
	if w.Code != 200 || !strings.Contains(w.Body.String(), "independent") {
		t.Fatal(w.Code, w.Body.String())
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.acquireAutomatic(canceled, fn); err != context.Canceled {
		t.Fatal(err)
	}
	if p.auto.Snapshot()["queued"] != 0 {
		t.Fatal("automatic execution queue remained")
	}
}

func TestAutomaticAdmissionAPIAndGenericDestinations(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	fn := createFn(t, app, ctx, map[string]any{"name": "generic-auto", "source": `export default e=>e`})
	p := currentPool()
	permit, e := p.acquireAutomatic(context.Background(), fn)
	if e != nil {
		t.Fatal(e)
	}
	s := p.capacitySnapshot(testProj)
	a := s["automatic_admission"].(map[string]any)
	if a["mode"] != "parallel" || a["active"].(int) != 1 {
		t.Fatal(a)
	}
	other := p.capacitySnapshot("unrelated-project")["automatic_admission"].(map[string]any)
	if len(other["operations"].([]map[string]any)) != 0 {
		t.Fatal("cross-project operation identities exposed")
	}
	permit.Finish(admission.Result{CPUSeconds: -1})
	for _, target := range []string{"app-a", "app-b", "integration-a"} {
		perm, e := p.autoDownstream.Acquire(context.Background(), admission.Request{Key: target, Operation: "calculate", Caller: fmt.Sprint(fn.ID)})
		if e != nil {
			t.Fatal(e)
		}
		perm.Finish(admission.Result{CPUSeconds: -1})
	}
}
