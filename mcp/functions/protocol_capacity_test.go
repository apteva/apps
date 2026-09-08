package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	tk "github.com/apteva/app-sdk/testkit"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func protocolTestPool(t *testing.T, mode string, free int64) *pool {
	t.Helper()
	if protocolBytes.Load() != 0 {
		t.Fatal("prior test leaked protocol reservations")
	}
	s := defaultCapacity()
	s.ProtocolMode = mode
	s.ProtocolTargetMB = 128
	s.ProtocolHardMB = 256
	s.ProtocolWaitMS = 100
	p := &pool{capacity: s, stop: make(chan struct{}), wake: make(chan struct{}, 1)}
	b := newProtocolBudget(s)
	b.readHost = func() int64 { return free }
	p.protocolBudget.Store(b)
	t.Cleanup(func() {
		if protocolBytes.Load() != 0 {
			t.Errorf("reservations leaked: %d", protocolBytes.Load())
		}
	})
	return p
}
func TestProtocolSoftConcurrentBurstAndHardCeiling(t *testing.T) {
	p := protocolTestPool(t, "soft", 8192)
	var wg sync.WaitGroup
	var successes atomic.Int64
	var mu sync.Mutex
	var releases []func()
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			r, e := p.acquireProtocol(context.Background(), "nested", maxFrame)
			if e == nil {
				successes.Add(1)
				mu.Lock()
				releases = append(releases, r)
				mu.Unlock()
			} else if errorCode(e) != "protocol_memory_limit" {
				t.Errorf("unexpected error %v", e)
			}
		}()
	}
	wg.Wait()
	defer func() {
		for _, r := range releases {
			r()
		}
	}()
	if successes.Load() != 32 || protocolBytes.Load() != 256<<20 {
		t.Fatalf("success=%d reserved=%d", successes.Load(), protocolBytes.Load())
	}
	pc := p.protocolCapacitySnapshot(nil)
	if pc["over_target_bytes"] != int64(128<<20) || pc["burst_admissions"].(int64) < 16 {
		t.Fatalf("missing burst telemetry: %+v", pc)
	}
	if reason := reserveProtocolBudget(p.protocolConfig(), 1, false); reason != "protocol_memory_limit" {
		t.Fatalf("hard cap not enforced: %s", reason)
	}
}
func TestProtocolStrictAndHostPressure(t *testing.T) {
	for _, tc := range []struct {
		name, mode string
		free       int64
		reason     string
	}{{"strict", "strict", 8192, "protocol_memory_limit"}, {"low-memory", "soft", 515, "protocol_host_memory_pressure"}, {"unavailable", "soft", -1, "protocol_host_memory_pressure"}} {
		t.Run(tc.name, func(t *testing.T) {
			p := protocolTestPool(t, tc.mode, tc.free)
			b := p.protocolConfig()
			if reason := reserveProtocolBudget(b, 128<<20, false); reason != "" {
				t.Fatal(reason)
			}
			defer protocolBytes.Add(-(128 << 20))
			started := time.Now()
			if _, err := p.acquireProtocol(context.Background(), "nested", maxFrame); errorCode(err) != tc.reason {
				t.Fatalf("want %s got %v", tc.reason, err)
			}
			if time.Since(started) > 50*time.Millisecond {
				t.Fatal("nested request waited behind parent")
			}
		})
	}
}
func TestProtocolBriefPressureWaitAndCancellation(t *testing.T) {
	p := protocolTestPool(t, "strict", 8192)
	p.capacity.ProtocolWaitMS = 500
	b := newProtocolBudget(p.capacity)
	p.protocolBudget.Store(b)
	if reason := reserveProtocolBudget(b, 128<<20, false); reason != "" {
		t.Fatal(reason)
	}
	done := make(chan error, 1)
	go func() {
		r, err := p.acquireProtocol(context.Background(), "interactive", maxFrame)
		if err == nil {
			r()
		}
		done <- err
	}()
	deadline := time.Now().Add(time.Second)
	for p.protocolWaiters.Load() != 1 {
		if time.Now().After(deadline) {
			t.Fatal("request did not queue")
		}
		time.Sleep(time.Millisecond)
	}
	time.Sleep(25 * time.Millisecond)
	protocolBytes.Add(-(128 << 20))
	p.signal()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if p.protocolWaiters.Load() != 0 {
		t.Fatal("waiter leaked")
	}
	if reason := reserveProtocolBudget(b, 128<<20, false); reason != "" {
		t.Fatal(reason)
	}
	defer protocolBytes.Add(-(128 << 20))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := p.acquireProtocol(ctx, "interactive", maxFrame); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	timeout, stop := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer stop()
	if _, err := p.acquireProtocol(timeout, "interactive", maxFrame); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if p.protocolWaiters.Load() != 0 {
		t.Fatal("canceled waiter leaked")
	}
}
func TestProtocolWaitDeadlineIsDistinct(t *testing.T) {
	p := protocolTestPool(t, "strict", 8192)
	if reason := reserveProtocolBudget(p.protocolConfig(), 128<<20, false); reason != "" {
		t.Fatal(reason)
	}
	defer protocolBytes.Add(-(128 << 20))
	start := time.Now()
	if _, err := p.acquireProtocol(context.Background(), "interactive", maxFrame); errorCode(err) != "protocol_memory_limit" {
		t.Fatal(err)
	}
	if elapsed := time.Since(start); elapsed < 80*time.Millisecond || elapsed > time.Second {
		t.Fatalf("unbounded or absent wait: %s", elapsed)
	}
	if p.protocolWaiters.Load() != 0 {
		t.Fatal("waiter leaked")
	}
}
func TestProtocolSoftDefaultsAndLegacyCeiling(t *testing.T) {
	t.Setenv("APTEVA_FUNCTIONS_PROTOCOL_MEMORY_MB", "")
	t.Setenv("APTEVA_FUNCTIONS_PROTOCOL_MEMORY_MODE", "")
	s := defaultCapacity()
	if s.ProtocolMode != "soft" || s.ProtocolTargetMB != 128 || s.ProtocolHardMB != 256 || s.ProtocolWaitMS != 1000 {
		t.Fatalf("defaults: %+v", s)
	}
	t.Setenv("APTEVA_FUNCTIONS_PROTOCOL_MEMORY_MB", "192")
	s = defaultCapacity()
	if s.ProtocolTargetMB != 192 || s.ProtocolHardMB != 192 {
		t.Fatal("legacy hard ceiling lost")
	}
	t.Setenv("APTEVA_FUNCTIONS_PROTOCOL_HARD_LIMIT_MB", "384")
	if s = defaultCapacity(); s.ProtocolHardMB != 384 {
		t.Fatal("explicit new ceiling ignored")
	}
}
func TestProtocolSettingsAPIValidationAndPersistence(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	app := mountApp(t, ctx)
	p := currentPool()
	put := func(s CapacitySettings) *httptest.ResponseRecorder {
		raw, _ := json.Marshal(s)
		w := httptest.NewRecorder()
		app.handleHTTPCapacitySettings(w, httptest.NewRequest("PUT", "/capacity/settings", strings.NewReader(string(raw))))
		return w
	}
	s := p.settings()
	s.TotalMemoryMB = 256
	s.MaxWorkerMemoryMB = 128
	s.InteractiveMemoryMB = 64
	s.NestedMemoryMB = 32
	s.HostHeadroomMB = 0
	// Host validation includes build resources, so these test-specific budgets also
	// fit inside bounded Linux test containers.
	t.Setenv("APTEVA_FUNCTIONS_MAX_BUILDS", "1")
	t.Setenv("APTEVA_FUNCTIONS_BUILD_MEMORY_MB", "64")
	s.ProtocolTargetMB = 32
	s.ProtocolHardMB = 64
	s.ProtocolWaitMS = 75
	s.ProtocolMode = "soft"
	if w := put(s); w.Code != 200 {
		t.Fatalf("save %d %s", w.Code, w.Body.String())
	}
	var saved string
	ctx.AppDB().QueryRow("SELECT settings_json FROM function_capacity_settings WHERE id=1").Scan(&saved)
	if !strings.Contains(saved, `"protocol_hard_limit_mb":64`) {
		t.Fatal(saved)
	}
	app.OnUnmount(ctx)
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	p = currentPool()
	if p.protocolConfig().hard != 64<<20 || p.settings().ProtocolWaitMS != 75 {
		t.Fatal("settings not restored")
	}
	invalid := s
	invalid.ProtocolTargetMB = 65
	if w := put(invalid); w.Code != 400 {
		t.Fatalf("invalid target accepted: %d", w.Code)
	}
	invalid = s
	invalid.ProtocolMode = "unlimited"
	if w := put(invalid); w.Code != 400 {
		t.Fatal("invalid mode accepted")
	}
	if reason := reserveProtocolBudget(p.protocolConfig(), 32<<20, false); reason != "" {
		t.Fatal(reason)
	}
	defer protocolBytes.Add(-(32 << 20))
	s.ProtocolMode = "strict"
	s.ProtocolTargetMB = 16
	s.ProtocolHardMB = 16
	if w := put(s); w.Code != 409 {
		t.Fatalf("lowered ceiling below use: %d %s", w.Code, w.Body.String())
	}
}
func TestProtocolPendingCallbacksStrictVsSoft(t *testing.T) {
	// All handlers remain pending together; no real integration or business data.
	for _, mode := range []string{"strict", "soft"} {
		t.Run(mode, func(t *testing.T) {
			p := protocolTestPool(t, mode, 8192)
			var active, peak atomic.Int64
			entered := make(chan struct{}, 24)
			release := make(chan struct{})
			downstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := active.Add(1)
				defer active.Add(-1)
				for old := peak.Load(); n > old && !peak.CompareAndSwap(old, n); old = peak.Load() {
				}
				entered <- struct{}{}
				select {
				case <-release:
					fmt.Fprint(w, "ok")
				case <-r.Context().Done():
				}
			}))
			defer downstream.Close()
			var wg sync.WaitGroup
			var admitted sync.WaitGroup
			admitted.Add(24)
			var failed atomic.Int64
			for i := 0; i < 24; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					r, err := p.acquireProtocol(context.Background(), "nested", maxFrame)
					admitted.Done()
					if err != nil {
						failed.Add(1)
						return
					}
					defer r()
					resp, err := http.Get(downstream.URL)
					if err != nil {
						t.Error(err)
						return
					}
					resp.Body.Close()
				}()
			}
			expected := 16
			if mode == "soft" {
				expected = 24
			}
			for i := 0; i < expected; i++ {
				select {
				case <-entered:
				case <-time.After(2 * time.Second):
					t.Fatal("callbacks did not reach mock")
				}
			}
			admitted.Wait()
			close(release)
			wg.Wait()
			wantFailures := int64(24 - expected)
			if failed.Load() != wantFailures || peak.Load() != int64(expected) {
				t.Fatalf("mode=%s failures=%d peak=%d", mode, failed.Load(), peak.Load())
			}
			t.Logf("%s: %d simultaneous delayed callbacks, %d rejections", mode, peak.Load(), failed.Load())
		})
	}
}

func TestProtocolQueueDoesNotAdmitAfterDeadline(t *testing.T) {
	p := protocolTestPool(t, "strict", 8192)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Millisecond)
	defer cancel()
	p.mu.Lock()
	done := make(chan error, 1)
	go func() {
		r, err := p.acquireProtocol(ctx, "interactive", maxFrame)
		if err == nil {
			r()
		}
		done <- err
	}()
	time.Sleep(20 * time.Millisecond)
	p.mu.Unlock()
	if err := <-done; !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("admitted canceled request: %v", err)
	}
}
func TestProtocolBudgetCountsFramesTogetherWithCallbacks(t *testing.T) {
	p := protocolTestPool(t, "soft", 8192)
	b := p.protocolConfig()
	if reason := reserveProtocolBudget(b, 250<<20, false); reason != "" {
		t.Fatal(reason)
	}
	defer protocolBytes.Add(-(250 << 20))
	if _, err := p.acquireProtocol(context.Background(), "nested", maxFrame); errorCode(err) != "protocol_memory_limit" {
		t.Fatalf("callback ignored existing frame bytes: %v", err)
	}
}
