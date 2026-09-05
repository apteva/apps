package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	tk "github.com/apteva/app-sdk/testkit"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"sort"
	"testing"
	"time"
)

type benchmarkPlatform struct {
	tk.BasePlatformClient
	client *http.Client
	url    string
}

func (p *benchmarkPlatform) CallAppResult(app, tool string, input map[string]any, out any) error {
	b, err := json.Marshal(input)
	if err != nil {
		return err
	}
	resp, err := p.client.Post(p.url, "application/json", bytes.NewReader(b))
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(out)
}

// Run on the same host and source revision, without other test workloads.
// The app endpoint is a real local HTTP server with fixed 2ms service latency.
func TestPerformance(t *testing.T) {
	if os.Getenv("FUNCTIONS_PERFORMANCE") != "1" {
		t.Skip("opt-in latency comparison")
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		time.Sleep(2 * time.Millisecond)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer server.Close()
	platform := &benchmarkPlatform{client: server.Client(), url: server.URL}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithPlatform(platform))
	app := mountApp(t, ctx)
	cases := []struct {
		name, src string
		n         int
	}{
		{"warm-http", `export default async e=>e`, 300},
		{"app-call", `export default async (e,c)=>await c.call("tables","rows_list",{})`, 150},
		{"apps-sequential-8", `export default async (e,c)=>{for(let i=0;i<8;i++)await c.call("tables","rows_list",{});return true}`, 40},
		{"apps-parallel-8", `export default async (e,c)=>await Promise.all(Array.from({length:8},()=>c.call("tables","rows_list",{})))`, 80},
	}
	for _, tc := range cases {
		deployStart := time.Now()
		fn := createFn(t, app, ctx, map[string]any{"name": tc.name, "source": tc.src})
		deployTime := time.Since(deployStart)
		failures := 0
		run := func() time.Duration {
			req := httptest.NewRequest("POST", "/fn/"+fn.Name+"?project_id="+testProj, bytes.NewBufferString(`{"x":1}`))
			rr := httptest.NewRecorder()
			start := time.Now()
			app.handleHTTPInvokeByName(rr, req)
			d := time.Since(start)
			if rr.Code != 200 {
				failures++
				if failures <= 3 {
					t.Logf("REQUEST_FAILURE %s: %s", tc.name, rr.Body.String())
				}
			}
			return d
		}
		first := run()
		for i := 0; i < 10; i++ {
			run()
		}
		runtime.GC()
		var before, after runtime.MemStats
		runtime.ReadMemStats(&before)
		times := make([]float64, tc.n)
		for i := range times {
			times[i] = float64(run().Microseconds()) / 1000
		}
		runtime.ReadMemStats(&after)
		sort.Float64s(times)
		t.Logf("PERF %s first_ms=%.3f p50_ms=%.3f p95_ms=%.3f bytes/op=%d samples=%d failures=%d deploy_ms=%.3f", tc.name, float64(first.Microseconds())/1000, times[len(times)/2], times[len(times)*95/100], (after.TotalAlloc-before.TotalAlloc)/uint64(tc.n), tc.n, failures, float64(deployTime.Microseconds())/1000)
		if failures > 0 && os.Getenv("FUNCTIONS_ALLOW_ERRORS") != "1" {
			t.Errorf("%s: %d failed requests", tc.name, failures)
		}
	}
}

func BenchmarkEventRead(b *testing.B) {
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		r := httptest.NewRequest("POST", "/fn/test", bytes.NewBufferString(`{"x":1}`))
		decodeEventBody(r)
	}
}

// Paired timing fixture: run old and new binaries concurrently and alternate
// requests to reduce bias from changing load on a shared development host.
func TestPerformanceServer(t *testing.T) {
	if os.Getenv("FUNCTIONS_PERFORMANCE_SERVER") != "1" {
		t.Skip("paired benchmark fixture")
	}
	downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.Copy(io.Discard, r.Body)
		time.Sleep(2 * time.Millisecond)
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer downstream.Close()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithPlatform(&benchmarkPlatform{client: downstream.Client(), url: downstream.URL}))
	app := mountApp(t, ctx)
	for name, src := range map[string]string{
		"warm-http":         `export default async e=>e`,
		"app-call":          `export default async (e,c)=>c.call("tables","rows_list",{})`,
		"apps-sequential-8": `export default async (e,c)=>{for(let i=0;i<8;i++)await c.call("tables","rows_list",{});return true}`,
		"apps-parallel-8":   `export default async(e,c)=>Promise.all(Array.from({length:8},()=>c.call("tables","rows_list",{})))`,
	} {
		fn := createFn(t, app, ctx, map[string]any{"name": name, "source": src})
		r := httptest.NewRequest("POST", "/fn/"+fn.Name+"?project_id="+testProj, bytes.NewBufferString(`{}`))
		rr := httptest.NewRecorder()
		app.handleHTTPInvokeByName(rr, r)
		if rr.Code != 200 {
			t.Fatal(rr.Body.String())
		}
	}
	done := make(chan struct{})
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/shutdown" {
			close(done)
			w.WriteHeader(200)
			return
		}
		app.handleHTTPInvokeByName(w, r)
	}))
	defer server.Close()
	t.Logf("PERF_SERVER %s", server.URL)
	<-done
}

func TestGoBuildPerformance(t *testing.T) {
	if os.Getenv("FUNCTIONS_GO_PERFORMANCE") != "1" {
		t.Skip("opt-in Go build comparison")
	}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	for i := 0; i < 3; i++ {
		start := time.Now()
		fn := createFn(t, app, ctx, map[string]any{"name": fmt.Sprintf("go-build-%d", i), "runtime": "go", "source": readExample(t, "hello.go.txt") + fmt.Sprintf("\n// version %d\n", i)})
		res, err := invokeFunction(ctx, context.Background(), fn, nil, "manual")
		if err != nil || res.Status != "ok" {
			t.Fatalf("build/invoke: %v %+v", err, res)
		}
		t.Logf("GO_BUILD iteration=%d deploy_and_first_invoke_ms=%.3f", i+1, float64(time.Since(start).Microseconds())/1000)
	}
}
