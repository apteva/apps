package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

func memoryTestPool(mode string) *pool {
	s := defaultCapacity()
	s.MemoryMode = mode
	s.TotalMemoryMB = 1024
	s.MaxWorkers = 16
	s.InteractiveMemoryMB, s.NestedMemoryMB = 256, 256
	s.InteractiveWorkers, s.NestedWorkers = 2, 2
	p := &pool{capacity: s, byFn: map[int64]*fnPool{}, all: map[*worker]*fnPool{}, globalSem: make(chan struct{}, 32), stop: make(chan struct{}), wake: make(chan struct{}, 1), deleted: map[string]bool{}}
	p.memoryReader = func(*worker) (*int64, string, int64) { n := int64(40 << 20); return &n, "cgroup_v2", 0 }
	p.hostMemoryReader = func() int64 { return 8192 }
	return p
}

// Simulates a completed process startup; measurements are injected while all
// reservation, concurrency, queueing and classification logic remains real.
func attachMeasuredWorker(p *pool, id int64, memory int, class string) *worker {
	w := &worker{fnID: id, fnName: fmt.Sprintf("worker-%d", id), projectID: testProj, memoryMB: memory, capacityClass: class, cmd: &exec.Cmd{Process: &os.Process{Pid: int(id) + 10000}}}
	w.capacityState.Store("running")
	p.mu.Lock()
	p.all[w] = p.poolForLocked(id)
	p.mu.Unlock()
	p.signal()
	return w
}
func releaseMeasuredWorker(p *pool, w *worker) {
	p.mu.Lock()
	delete(p.all, w)
	delete(p.memorySamples, w)
	// Match discard's atomic removal: a removed measured worker must not
	// temporarily look like a pending cold start with its full allowance.
	p.liveMB -= w.memoryMB
	u := p.classReservations[w.capacityClass]
	p.classReservations[w.capacityClass] = [2]int{u[0] - w.memoryMB, u[1] - 1}
	p.mu.Unlock()
	<-p.globalSem
	p.signal()
}

func TestSoftMemoryDefaultAndStrictComparison(t *testing.T) {
	t.Setenv("APTEVA_FUNCTIONS_MEMORY_MODE", "")
	if defaultCapacity().MemoryMode != "soft" {
		t.Fatal("new installs must default to soft")
	}
	for _, mode := range []string{"soft", "strict"} {
		t.Run(mode, func(t *testing.T) {
			p := memoryTestPool(mode)
			var workers []*worker
			defer func() {
				for _, w := range workers {
					releaseMeasuredWorker(p, w)
				}
			}()
			admitted := 0
			for i := 0; i < 8; i++ {
				memory := 128
				if i%2 == 0 {
					memory = 256
				}
				fn := &Function{MaxMemoryMB: memory, Limits: RuntimePolicy{QueueMS: 20}}
				class, err := p.reserveWorker(context.Background(), fn)
				if err != nil {
					if mode == "soft" || errorCode(err) != "memory_budget_exhausted" {
						t.Fatalf("request %d: %v", i, err)
					}
					break
				}
				workers = append(workers, attachMeasuredWorker(p, int64(i+1), memory, class))
				admitted++
			}
			want := 8
			if mode == "strict" {
				want = 4
			}
			if admitted != want {
				t.Fatalf("admitted %d, want %d", admitted, want)
			}
			p.mu.Lock()
			m := p.admissionMemoryLocked()
			p.mu.Unlock()
			t.Logf("%s: %d simultaneous mixed 128/256 MiB workers, combined limits %d MiB, admission %d MiB", mode, admitted, p.liveMB, m.AccountedMB)
			if mode == "soft" && (p.liveMB != 1536 || m.AccountedMB != 448) {
				t.Fatalf("unexpected accounting: %+v", m)
			}
		})
	}
}

func TestSoftMemoryAtomicColdStartsAndMissingMeasurements(t *testing.T) {
	p := memoryTestPool("soft")
	fn := &Function{MaxMemoryMB: 256, Limits: RuntimePolicy{QueueMS: 25}}
	for i := 0; i < 3; i++ {
		if _, e := p.reserveWorker(context.Background(), fn); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := p.reserveWorker(context.Background(), fn); errorCode(e) != "memory_pressure" {
		t.Fatalf("pending starts overcommitted: %v", e)
	}
	p.memoryReader = func(*worker) (*int64, string, int64) { n := int64(1 << 20); return &n, "process_rss_no_descendants", 0 }
	for i := 0; i < 3; i++ {
		attachMeasuredWorker(p, int64(i+1), 256, "interactive")
	}
	if _, e := p.reserveWorker(context.Background(), fn); errorCode(e) != "memory_pressure" {
		t.Fatalf("incomplete measurements overcommitted: %v", e)
	}
	p.mu.Lock()
	m := p.admissionMemoryLocked()
	p.mu.Unlock()
	if m.AccountedMB != 768 || m.FallbackWorkers != 3 {
		t.Fatalf("fallback: %+v", m)
	}
}

func TestSoftMemoryPressureWaitsAndRecoversWithoutWorkerExit(t *testing.T) {
	p := memoryTestPool("soft")
	var used atomic.Int64
	used.Store(256 << 20)
	p.memoryReader = func(*worker) (*int64, string, int64) { n := used.Load(); return &n, "cgroup_v2", 0 }
	fn := &Function{MaxMemoryMB: 256, Limits: RuntimePolicy{QueueMS: 1000}}
	for i := 0; i < 3; i++ {
		class, e := p.reserveWorker(context.Background(), fn)
		if e != nil {
			t.Fatal(e)
		}
		attachMeasuredWorker(p, int64(i+1), 256, class)
	}
	done := make(chan error, 1)
	go func() { _, e := p.reserveWorker(context.Background(), fn); done <- e }()
	select {
	case e := <-done:
		t.Fatalf("did not wait: %v", e)
	case <-time.After(30 * time.Millisecond):
	}
	used.Store(40 << 20) // Existing processes release memory, but stay alive.
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("memory recovery did not wake admission")
	}
	if len(p.globalSem) != 4 {
		t.Fatal("lost worker admission")
	}
}

func TestSoftMemoryHostPressureCancellationAndRecovery(t *testing.T) {
	p := memoryTestPool("soft")
	var available atomic.Int64
	available.Store(600) // 512 MiB retained for host.
	p.hostMemoryReader = available.Load
	fn := &Function{MaxMemoryMB: 128, Limits: RuntimePolicy{QueueMS: 40}}
	if _, e := p.reserveWorker(context.Background(), fn); errorCode(e) != "host_memory_pressure" {
		t.Fatalf("host pressure: %v", e)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { _, e := p.reserveWorker(ctx, fn); done <- e }()
	time.Sleep(10 * time.Millisecond)
	cancel()
	if e := <-done; !errors.Is(e, context.Canceled) {
		t.Fatalf("cancel: %v", e)
	}
	if p.liveMB != 0 || len(p.globalSem) != 0 {
		t.Fatal("canceled wait leaked reservation")
	}
	available.Store(8192)
	fn.Limits.QueueMS = 1000
	class, e := p.reserveWorker(context.Background(), fn)
	if e != nil {
		t.Fatal(e)
	}
	p.releaseReservation(class, 128)
}

func TestSoftMemoryHostAccountsPendingStarts(t *testing.T) {
	p := memoryTestPool("soft")
	p.hostMemoryReader = func() int64 { return 1024 }
	fn := &Function{MaxMemoryMB: 256, Limits: RuntimePolicy{QueueMS: 20}}
	for i := 0; i < 2; i++ {
		if _, e := p.reserveWorker(context.Background(), fn); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := p.reserveWorker(context.Background(), fn); errorCode(e) != "host_memory_pressure" {
		t.Fatalf("pending workers bypassed host headroom: %v", e)
	}
	for i := 0; i < 2; i++ {
		p.releaseReservation("interactive", 256)
	}
}

func TestSoftMemoryConcurrentMixedWorkersKeepHardSlots(t *testing.T) {
	p := memoryTestPool("soft")
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			memory := 128
			if i%2 == 0 {
				memory = 256
			}
			fn := &Function{MaxMemoryMB: memory, Limits: RuntimePolicy{QueueMS: 3000}}
			class, e := p.reserveWorker(context.Background(), fn)
			if e != nil {
				t.Error(e)
				return
			}
			w := attachMeasuredWorker(p, int64(i+1), memory, class)
			p.mu.Lock()
			m := p.admissionMemoryLocked()
			_, count := p.classUsageLocked(class)
			if m.AccountedMB > 768 || count > 14 || len(p.globalSem) > 16 {
				t.Errorf("unsafe admission: %+v slots=%d", m, count)
			}
			p.mu.Unlock()
			time.Sleep(5 * time.Millisecond)
			releaseMeasuredWorker(p, w)
		}(i)
	}
	wg.Wait()
	if p.liveMB != 0 || len(p.globalSem) != 0 || len(p.all) != 0 || len(p.memorySamples) != 0 {
		t.Fatal("worker/reservation/sample leak")
	}
}

func TestSoftMemoryProtectsInteractiveAndNestedWorkers(t *testing.T) {
	p := memoryTestPool("soft")
	bg := &Function{MaxMemoryMB: 128, Limits: RuntimePolicy{Class: "background", QueueMS: 20}}
	for i := 0; i < 7; i++ {
		class, e := p.reserveWorker(context.Background(), bg)
		if e != nil {
			t.Fatal(e)
		}
		attachMeasuredWorker(p, int64(i+1), 128, class)
	}
	if _, e := p.reserveWorker(context.Background(), bg); errorCode(e) != "memory_pressure" {
		t.Fatalf("background used interactive headroom: %v", e)
	}
	for _, class := range []string{"interactive", "nested"} {
		ctx := context.WithValue(context.Background(), admissionClassKey{}, class)
		if _, e := p.reserveWorker(ctx, &Function{MaxMemoryMB: 128}); e != nil {
			t.Fatalf("%s blocked: %v", class, e)
		}
	}
	// A child can fail quickly instead of waiting for a parent holding resources.
	ctx := context.WithValue(context.Background(), admissionClassKey{}, "nested")
	if _, e := p.reserveWorker(ctx, &Function{MaxMemoryMB: 256}); errorCode(e) != "nested_capacity_exhausted" {
		t.Fatalf("nested fanout: %v", e)
	}
}

func TestSoftMemoryWorkerLimitStillEnforced(t *testing.T) {
	p := memoryTestPool("soft")
	p.capacity.MaxWorkers = 4
	p.capacity.NestedWorkers = 1
	p.capacity.InteractiveWorkers = 1
	fn := &Function{MaxMemoryMB: 128, Limits: RuntimePolicy{QueueMS: 20}}
	for i := 0; i < 3; i++ {
		class, e := p.reserveWorker(context.Background(), fn)
		if e != nil {
			t.Fatal(e)
		}
		attachMeasuredWorker(p, int64(i+1), 128, class)
	}
	if _, e := p.reserveWorker(context.Background(), fn); errorCode(e) != "worker_limit" {
		t.Fatalf("soft memory bypassed hard worker slots: %v", e)
	}
}

func TestSoftMemoryReclassificationUsesMeasuredCharge(t *testing.T) {
	for _, mode := range []string{"soft", "strict"} {
		p := memoryTestPool(mode)
		fn := &Function{MaxMemoryMB: 256}
		for i := 0; i < 3; i++ {
			class, e := p.reserveWorker(context.Background(), fn)
			if e != nil {
				t.Fatal(e)
			}
			attachMeasuredWorker(p, int64(i+1), 256, class)
		}
		ctx := context.WithValue(context.Background(), admissionClassKey{}, "nested")
		class, e := p.reserveWorker(ctx, fn)
		if e != nil {
			t.Fatal(e)
		}
		w := attachMeasuredWorker(p, 4, 256, class)
		if got := p.reclassify(w, "interactive"); got != (mode == "soft") {
			t.Fatalf("%s reclassification: %v", mode, got)
		}
		p.mu.Lock()
		reserved := p.classReservations
		total := p.liveMB
		p.mu.Unlock()
		wantRoots := 3
		if mode == "soft" {
			wantRoots = 4
		}
		if reserved["interactive"][1] != wantRoots || reserved["nested"][1] != 4-wantRoots || total != 1024 {
			t.Fatalf("%s changed reservation totals: %v", mode, reserved)
		}
	}
}

func TestSoftMemoryCapacityAPIAndPersistedUpgrade(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	p := currentPool()
	s := p.settings()
	b, _ := json.Marshal(s)
	var old map[string]any
	json.Unmarshal(b, &old)
	delete(old, "memory_mode")
	b, _ = json.Marshal(old)
	if _, e := ctx.AppDB().Exec("INSERT INTO function_capacity_settings(id,settings_json) VALUES(1,?)", string(b)); e != nil {
		t.Fatal(e)
	}
	reloaded := &pool{ctx: ctx}
	if e := reloaded.initCapacity(); e != nil {
		t.Fatal(e)
	}
	if reloaded.settings().MemoryMode != "soft" {
		t.Fatal("existing configuration did not adopt soft default")
	}
	// An older API client omitting the new key also gets soft, not strict.
	w := httptest.NewRecorder()
	app.handleHTTPCapacitySettings(w, httptest.NewRequest("PUT", "/capacity/settings", strings.NewReader(string(b))))
	if w.Code != 200 || p.settings().MemoryMode != "soft" {
		t.Fatalf("old client: %d %s", w.Code, w.Body.String())
	}
	s.MemoryMode = "strict"
	b, _ = json.Marshal(s)
	w = httptest.NewRecorder()
	app.handleHTTPCapacitySettings(w, httptest.NewRequest("PUT", "/capacity/settings", strings.NewReader(string(b))))
	if w.Code != 200 {
		t.Fatalf("strict opt-in: %d %s", w.Code, w.Body.String())
	}
	reloaded = &pool{ctx: ctx}
	if e := reloaded.initCapacity(); e != nil || reloaded.settings().MemoryMode != "strict" {
		t.Fatalf("strict persistence: %v", e)
	}
	s.MemoryMode = "unlimited"
	b, _ = json.Marshal(s)
	w = httptest.NewRecorder()
	app.handleHTTPCapacitySettings(w, httptest.NewRequest("PUT", "/capacity/settings", strings.NewReader(string(b))))
	if w.Code != 400 {
		t.Fatal("invalid mode accepted")
	}
	// Soft mode accepts combined allowances above the target; switching back
	// to strict requires draining, rather than silently killing existing work.
	p.mu.Lock()
	p.liveMB = s.TotalMemoryMB + 256
	p.mu.Unlock()
	s.MemoryMode = "soft"
	b, _ = json.Marshal(s)
	w = httptest.NewRecorder()
	app.handleHTTPCapacitySettings(w, httptest.NewRequest("PUT", "/capacity/settings", strings.NewReader(string(b))))
	softCode := w.Code
	s.MemoryMode = "strict"
	b, _ = json.Marshal(s)
	w = httptest.NewRecorder()
	app.handleHTTPCapacitySettings(w, httptest.NewRequest("PUT", "/capacity/settings", strings.NewReader(string(b))))
	p.mu.Lock()
	p.liveMB = 0
	p.mu.Unlock()
	if softCode != 200 || w.Code != 409 {
		t.Fatalf("mode transition: soft %d strict %d", softCode, w.Code)
	}

	fixture := memoryTestPool("soft")
	for i := 0; i < 4; i++ {
		class, e := fixture.reserveWorker(context.Background(), &Function{MaxMemoryMB: 256})
		if e != nil {
			t.Fatal(e)
		}
		attachMeasuredWorker(fixture, int64(i+1), 256, class)
	}
	snap := fixture.capacitySnapshot(testProj)
	m := snap["memory_admission"].(admissionMemory)
	if m.AccountedMB != 224 || m.Mode != "soft" || *m.HostAvailableMB != 8192 {
		t.Fatalf("API admission: %+v", m)
	}
	for _, g := range snap["functions"].([]*FunctionCapacity) {
		if g.AdmissionMB != 56 || g.ReservedMB != 256 {
			t.Fatalf("function accounting: %+v", g)
		}
	}
	other := fixture.capacitySnapshot("other")
	if len(other["workers"].([]map[string]any)) != 0 || len(other["functions"].([]*FunctionCapacity)) != 0 {
		t.Fatal("cross-project exposure")
	}
}
