package main

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestMetricsCache_DeduplicatesConcurrentCollection(t *testing.T) {
	inst := &Instance{ID: 991, Provider: "manual", Status: "ready"}
	clearMetricsCache(inst.ID)
	var calls atomic.Int32
	started := make(chan struct{})
	release := make(chan struct{})
	collector := func() (*Metrics, error) {
		if calls.Add(1) == 1 {
			close(started)
		}
		<-release
		return &Metrics{Timestamp: nowUTC()}, nil
	}
	var wg sync.WaitGroup
	for range 20 {
		wg.Add(1)
		go func() { defer wg.Done(); _, _ = collectMetricsCached(inst, collector) }()
	}
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("collector did not start")
	}
	time.Sleep(20 * time.Millisecond)
	close(release)
	wg.Wait()
	if got := calls.Load(); got != 1 {
		t.Fatalf("collector calls=%d, want 1", got)
	}
}
