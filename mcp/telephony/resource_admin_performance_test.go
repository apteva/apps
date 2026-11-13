package main

import (
	"context"
	"fmt"
	"os"
	"runtime"
	"sort"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type resourceTimingLogger struct {
	sdk.Logger
	mu         sync.Mutex
	databaseMS []float64
	queueMS    []float64
}

func (l *resourceTimingLogger) Info(message string, fields ...any) {
	if message != "Telephony resource administration timing" {
		return
	}
	values := map[string]float64{}
	for i := 0; i < len(fields); i += 2 {
		if value, ok := fields[i+1].(float64); ok {
			values[fields[i].(string)] = value
		}
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.databaseMS = append(l.databaseMS, values["database_ms"])
	l.queueMS = append(l.queueMS, values["queue_ms"])
}
func resourcePercentile(values []float64, percent int) float64 {
	copy := append([]float64{}, values...)
	sort.Float64s(copy)
	if len(copy) == 0 {
		return 0
	}
	return copy[(len(copy)-1)*percent/100]
}

// Explicitly invoked so the routine test suite does not become a benchmark.
func TestPhoneResourceAdminPerformance(t *testing.T) {
	if os.Getenv("RUN_TELEPHONY_RESOURCE_BENCH") != "1" {
		t.Skip("opt-in local administration benchmark")
	}
	f := newPhoneAdminFixture(t)
	f.provision(t, "desk-benchmark", "alice", "call.read", "call.answer", "call.attach")
	logger := &resourceTimingLogger{Logger: f.ctx.Logger()}
	base := sdk.NewAppCtxForTest(f.ctx.Manifest(), f.ctx.AppDB(), f.ctx.Config(), f.ctx.PlatformAPI(), logger)
	f.ctx = base.WithProject("project-a")
	globalCtx = base
	const requests = 32
	for _, operation := range []string{"grants_get", "grants_upsert"} {
		for _, warm := range []bool{false, true} {
			if warm {
				for range 8 {
					f.app.resourceAdmin(sdk.WithCaller(context.Background(), f.caller), f.ctx, "grants_get", map[string]any{"id": "grant-desk-benchmark"})
				}
			}
			logger.mu.Lock()
			logger.databaseMS = nil
			logger.queueMS = nil
			logger.mu.Unlock()
			var before, after runtime.MemStats
			runtime.ReadMemStats(&before)
			statsBefore := f.ctx.AppDB().Stats()
			var mu sync.Mutex
			var durations []float64
			failures := 0
			jobs := make(chan int)
			var workers sync.WaitGroup
			for range 8 {
				workers.Add(1)
				go func() {
					defer workers.Done()
					for i := range jobs {
						args := map[string]any{"id": "grant-desk-benchmark"}
						if operation == "grants_upsert" {
							suffix := fmt.Sprintf("%t-%d", warm, i)
							args = adminGrantArgs("benchmark-"+suffix, "desk-benchmark", "alice", "call.read")
						}
						start := time.Now()
						response := f.app.resourceAdmin(sdk.WithCaller(context.Background(), f.caller), f.ctx, operation, args)
						elapsed := float64(time.Since(start)) / float64(time.Millisecond)
						mu.Lock()
						durations = append(durations, elapsed)
						if response["ok"] != true {
							failures++
						}
						mu.Unlock()
					}
				}()
			}
			for i := range requests {
				jobs <- i
			}
			close(jobs)
			workers.Wait()
			runtime.ReadMemStats(&after)
			statsAfter := f.ctx.AppDB().Stats()
			logger.mu.Lock()
			queue, database := append([]float64{}, logger.queueMS...), append([]float64{}, logger.databaseMS...)
			logger.mu.Unlock()
			t.Logf("operation=%s warm=%t requests=%d concurrency=8 p50_ms=%.3f p95_ms=%.3f errors=%d db_pool_wait_ms=%.3f transaction_queue_p95_ms=%.3f database_transaction_p95_ms=%.3f allocated_bytes=%d heap_bytes=%d",
				operation, warm, requests, resourcePercentile(durations, 50), resourcePercentile(durations, 95), failures, float64(statsAfter.WaitDuration-statsBefore.WaitDuration)/float64(time.Millisecond), resourcePercentile(queue, 95), resourcePercentile(database, 95), after.TotalAlloc-before.TotalAlloc, after.HeapAlloc)
			if failures != 0 {
				t.Fatal("administration benchmark failed")
			}
		}
	}
}
