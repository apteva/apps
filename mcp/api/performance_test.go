package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"testing"
	"time"
)

// This exact harness also runs against api/v0.5.0. It includes real HTTP on
// both sides of the gateway. Enable explicitly to avoid noisy timing checks
// in CI; correctness tests do not depend on a particular machine's speed.
func TestGatewayHTTPPerformance(t *testing.T) {
	if os.Getenv("API_PERF") != "1" {
		t.Skip("set API_PERF=1 to measure local HTTP latency")
	}
	origin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"ok":true}`) }))
	defer origin.Close()
	for _, count := range []int{10, 100, 1000} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			app, ctx := mountTestApp(t)
			api, err := dbCreateAPI(ctx.AppDB(), apiInput{ProjectID: testProject, Slug: "perf"})
			if err != nil {
				t.Fatal(err)
			}
			tx, err := ctx.AppDB().Begin()
			if err != nil {
				t.Fatal(err)
			}
			for i := 0; i < count; i++ {
				if _, err = tx.Exec(`INSERT INTO api_routes(project_id,api_id,method,path_pattern,target_kind,target_ref) VALUES(?,?,?,?,?,?)`, testProject, api.ID, "GET", fmt.Sprintf("/r/%04d", i), "http", origin.URL); err != nil {
					tx.Rollback()
					t.Fatal(err)
				}
			}
			if err = tx.Commit(); err != nil {
				t.Fatal(err)
			}
			gateway := httptest.NewServer(http.HandlerFunc(app.handleGateway))
			defer gateway.Close()
			client := &http.Client{Timeout: 30 * time.Second}
			request := func() {
				t.Helper()
				resp, err := client.Get(fmt.Sprintf("%s/gw/perf/r/%04d?project_id=%s", gateway.URL, count-1, testProject))
				if err != nil {
					t.Fatal(err)
				}
				body, err := io.ReadAll(resp.Body)
				resp.Body.Close()
				if err != nil || resp.StatusCode != 200 || string(body) != `{"ok":true}` {
					t.Fatalf("response: %d %s %v", resp.StatusCode, body, err)
				}
			}
			cold := time.Now()
			request()
			coldDuration := time.Since(cold)
			const samples = 100
			latency := make([]time.Duration, samples)
			start := time.Now()
			for i := range latency {
				begin := time.Now()
				request()
				latency[i] = time.Since(begin)
			}
			if _, err := dbListLogs(ctx.AppDB(), testProject, api.ID, 1); err != nil {
				t.Fatal(err)
			}
			elapsed := time.Since(start)
			sort.Slice(latency, func(i, j int) bool { return latency[i] < latency[j] })
			t.Logf("PERF routes=%d requests=%d cold_ms=%.3f p50_ms=%.3f p95_ms=%.3f requests_per_second=%.2f total_with_log_drain_ms=%.3f", count, samples, float64(coldDuration)/1e6, float64(latency[49])/1e6, float64(latency[94])/1e6, samples/elapsed.Seconds(), float64(elapsed)/1e6)
		})
	}
}
