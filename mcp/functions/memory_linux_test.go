//go:build linux

package main

import (
	"context"
	"fmt"
	tk "github.com/apteva/app-sdk/testkit"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestAvailableMemoryUsesPhysicalAndEnclosingLimits(t *testing.T) {
	outer, inner := t.TempDir(), t.TempDir()
	write := func(dir, name, value string) {
		t.Helper()
		if e := os.WriteFile(filepath.Join(dir, name), []byte(value), 0600); e != nil {
			t.Fatal(e)
		}
	}
	write(outer, "memory.max", "1073741824")
	write(outer, "memory.current", "805306368")
	write(inner, "memory.max", "max")
	write(inner, "memory.current", "0")
	if n := availableMemoryMB("MemAvailable: 8388608 kB", []string{outer, inner}); n != 256 {
		t.Fatalf("container headroom: %d", n)
	}
	if n := availableMemoryMB("MemAvailable: 65536 kB", []string{outer, inner}); n != 64 {
		t.Fatalf("physical headroom: %d", n)
	}
	write(outer, "memory.current", "invalid")
	if n := availableMemoryMB("MemAvailable: 8388608 kB", []string{outer}); n != 0 {
		t.Fatalf("missing bounded-group measurement: %d", n)
	}
	if n := availableMemoryMB("", nil); n != -1 {
		t.Fatalf("missing measurements invented availability: %d", n)
	}
}

// Real sandboxed processes, cgroup measurements and handlers. The same low
// worker-memory target admits four sleeping 256 MiB workers only in soft mode.
func TestLinuxSoftMemoryRealWorkers(t *testing.T) {
	if os.Getenv("RUN_FUNCTIONS_CGROUP_TESTS") != "1" {
		t.Skip("requires delegated writable cgroup v2")
	}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	fn := createFn(t, app, ctx, map[string]any{"name": "soft-real", "runtime": "go", "max_memory_mb": 256, "timeout_ms": 5000, "limits": map[string]any{"queue_timeout_ms": 100}, "source": `package main
import("encoding/json";"time")
func Handle(e json.RawMessage,c *Context)(any,error){time.Sleep(600*time.Millisecond);return true,nil}`})
	p := currentPool()
	p.mu.Lock()
	p.capacity.TotalMemoryMB = 512
	p.capacity.NestedMemoryMB = 0
	p.capacity.InteractiveMemoryMB = 0
	p.capacity.HostHeadroomMB = 64
	p.mu.Unlock()
	// Stop a preparation worker before each run so both modes start cold.
	for _, mode := range []string{"strict", "soft"} {
		for p.evictIdle() {
		}
		p.mu.Lock()
		p.capacity.MemoryMode = mode
		p.mu.Unlock()
		var wg sync.WaitGroup
		results := make(chan error, 4)
		for i := 0; i < 4; i++ {
			wg.Add(1)
			go func() {
				defer wg.Done()
				r, e := invokeFunction(ctx, context.Background(), fn, nil, "test")
				if e == nil && r.Status != "ok" {
					e = fmt.Errorf("%s: %s", r.ErrorCode, r.Error)
				}
				results <- e
			}()
		}
		// Capture while handlers sleep, after the bounded queue deadline.
		time.Sleep(300 * time.Millisecond)
		snap := p.capacitySnapshot(testProj)
		wg.Wait()
		close(results)
		ok, failed := 0, 0
		for e := range results {
			if e == nil {
				ok++
			} else {
				failed++
				t.Logf("%s: %v", mode, e)
			}
		}
		m := snap["memory_admission"].(admissionMemory)
		g := snap["global"].(map[string]any)
		t.Logf("%s: successful=%d rejected=%d combined_worker_limits=%v MiB measured=%v bytes admission=%d MiB", mode, ok, failed, g["reserved_memory_mb"], g["actual_worker_memory_bytes"], m.AccountedMB)
		if mode == "strict" && (ok != 2 || failed != 2) {
			t.Fatalf("strict comparison: %d/%d", ok, failed)
		}
		if mode == "soft" && (ok != 4 || failed != 0 || g["reserved_memory_mb"].(int) != 1024 || m.FallbackWorkers != 0) {
			t.Fatalf("soft did not use measured headroom: %+v", snap)
		}
		if len(p.globalQueue) != 0 || len(p.downstream) != 0 {
			t.Fatal("slots leaked")
		}
	}
}

func TestLinuxCallMemoryAndOOM(t *testing.T) {
	if os.Getenv("RUN_FUNCTIONS_CGROUP_TESTS") != "1" {
		t.Skip("requires delegated writable cgroup v2")
	}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	fn := createFn(t, app, ctx, map[string]any{"name": "measured", "runtime": "go", "max_memory_mb": 256, "source": `package main
import("encoding/json";"time";"runtime")
func Handle(e json.RawMessage,c *Context)(any,error){b:=make([]byte,48*1024*1024);for i:=range b{b[i]=1};time.Sleep(180*time.Millisecond);runtime.KeepAlive(b);return len(b),nil}`})
	r, e := invokeFunction(ctx, context.Background(), fn, nil, "test")
	if e != nil || r.Status != "ok" {
		t.Fatalf("memory call: %v %+v", e, r)
	}
	m := r.Resources
	if m.MemorySource != "cgroup_v2" || m.MemoryPeak == nil || m.MemoryStart == nil || *m.MemoryPeak <= *m.MemoryStart+32*1024*1024 {
		t.Fatalf("measured peak not captured: %+v", m)
	}
	t.Logf("worker memory start=%d sampled_peak=%d allowance=%d MiB", *m.MemoryStart, *m.MemoryPeak, m.ReservedMB)
	oom := createFn(t, app, ctx, map[string]any{"name": "oom", "runtime": "go", "max_memory_mb": 64, "source": `package main
import("encoding/json";"time";"runtime")
func Handle(e json.RawMessage,c *Context)(any,error){b:=make([]byte,256*1024*1024);for i:=range b{b[i]=1};time.Sleep(100*time.Millisecond);runtime.KeepAlive(b);return len(b),nil}`})
	r, e = invokeFunction(ctx, context.Background(), oom, nil, "test")
	if r == nil || r.ErrorCode != "worker_oom" {
		t.Fatalf("OOM was not distinguished: %v %+v", e, r)
	}
	p := currentPool()
	if len(p.downstream) != 0 || len(p.globalQueue) != 0 {
		t.Fatal("OOM leaked admission/downstream slots")
	}
}
