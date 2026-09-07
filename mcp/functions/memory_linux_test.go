//go:build linux

package main

import (
	"context"
	tk "github.com/apteva/app-sdk/testkit"
	"os"
	"testing"
)

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
