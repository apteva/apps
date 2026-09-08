package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

type nestedProtocolPlatform struct {
	tk.BasePlatformClient
	gate    chan struct{}
	entered chan struct{}
	active  atomic.Int32
}

// HTTP project scoping wraps the SDK interface; the fast chain mock also
// implements its ordinary method. Cancellation tests use the context adapter.
func (p *nestedProtocolPlatform) CallAppResult(app, tool string, input map[string]any, out any) error {
	return p.CallAppResultContext(context.Background(), app, tool, input, out)
}

func (p *nestedProtocolPlatform) CallAppResultContext(ctx context.Context, _, _ string, _ map[string]any, out any) error {
	p.active.Add(1)
	defer p.active.Add(-1)
	if p.entered != nil {
		p.entered <- struct{}{}
	}
	if p.gate != nil {
		select {
		case <-p.gate:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return json.Unmarshal([]byte(`{"ok":true}`), out)
}
func assertProtocolDrained(t *testing.T, p *pool) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		p.mu.Lock()
		used := int64(0)
		for _, n := range p.protocolClasses {
			used += n
		}
		p.mu.Unlock()
		if used == 0 && protocolBytes.Load() == 0 && len(p.downstream) == 0 && len(p.globalQueue) == 0 {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("protocol/downstream reservations leaked: bytes=%d downstream=%d", protocolBytes.Load(), len(p.downstream))
}

func TestNestedProtocolHTTPChainBeyondProtectedReserve(t *testing.T) {
	platform := &nestedProtocolPlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithPlatform(platform))
	app := mountApp(t, ctx)
	for i := 3; i >= 0; i-- {
		source := `export default async(e,c)=>c.call("tables","list",{})`
		if i < 3 {
			source = fmt.Sprintf(`export default async(e,c)=>JSON.parse((await c.call("functions","functions_invoke",{name:"protocol-chain-%d",event:{}})).response)`, i+1)
		}
		createFn(t, app, ctx, map[string]any{"name": fmt.Sprintf("protocol-chain-%d", i), "max_memory_mb": 128, "source": source})
	}
	for i := 0; i < 3; i++ {
		r := httptest.NewRequest("POST", "/fn/protocol-chain-0?project_id="+testProj, strings.NewReader(`{}`))
		w := httptest.NewRecorder()
		app.handleHTTPInvokeByName(w, r)
		if w.Code != 200 || !strings.Contains(w.Body.String(), `"ok":true`) {
			t.Fatalf("nested HTTP chain: status=%d body=%s", w.Code, w.Body.String())
		}
		assertProtocolDrained(t, currentPool())
	}
}

func TestNestedProtocolFanoutAndCancellation(t *testing.T) {
	for _, cancelCall := range []bool{false, true} {
		t.Run(fmt.Sprint("cancel=", cancelCall), func(t *testing.T) {
			platform := &nestedProtocolPlatform{gate: make(chan struct{}), entered: make(chan struct{}, 8)}
			defer close(platform.gate)
			ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithPlatform(platform))
			app := mountApp(t, ctx)
			// Exercise the underlying deadline/protocol mechanism independently of adaptive admission.
			currentPool().auto = nil
			currentPool().autoDownstream = nil
			createFn(t, app, ctx, map[string]any{"name": "protocol-child", "max_memory_mb": 128, "source": `export default async(e,c)=>c.call("tables","list",{})`})
			fn := createFn(t, app, ctx, map[string]any{"name": "protocol-fanout", "max_memory_mb": 128, "source": `export default async(e,c)=>Promise.all([1,2,3].map(()=>c.call("functions","functions_invoke",{name:"protocol-child",event:{}})))`})
			parent, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan *invokeResult, 1)
			go func() { r, _ := invokeFunction(ctx, parent, fn, nil, "test"); done <- r }()
			for i := 0; i < 3; i++ {
				select {
				case <-platform.entered:
				case r := <-done:
					t.Fatalf("fanout ended before all children entered: %+v", r)
				case <-time.After(3 * time.Second):
					t.Fatal("fanout did not reach three downstream calls")
				}
			}
			p := currentPool()
			p.mu.Lock()
			nested := p.protocolClasses["nested"]
			p.mu.Unlock()
			if nested != 3*maxFrame {
				t.Fatalf("expected 24 MiB nested reservations, got %d", nested)
			}
			if cancelCall {
				cancel()
			} else {
				for i := 0; i < 3; i++ {
					platform.gate <- struct{}{}
				}
			}
			select {
			case r := <-done:
				if r == nil || (!cancelCall && r.Status != "ok") || (cancelCall && r.ErrorCode != "caller_canceled") {
					t.Fatalf("fanout result: %+v", r)
				}
			case <-time.After(3 * time.Second):
				t.Fatal("fanout did not terminate")
			}
			assertProtocolDrained(t, p)
			if platform.active.Load() != 0 {
				t.Fatal("downstream execution leaked")
			}
		})
	}
}

func TestNestedProtocolBorrowsSharedButNeverExceedsTotal(t *testing.T) {
	t.Setenv("APTEVA_FUNCTIONS_PROTOCOL_MEMORY_MB", "128")
	p := &pool{stop: make(chan struct{}), wake: make(chan struct{}, 1)}
	var releases []func()
	defer func() {
		for _, r := range releases {
			r()
		}
	}()
	for i := 0; i < 3; i++ {
		release, e := p.acquireProtocol(context.Background(), "nested", maxFrame)
		if e != nil {
			t.Fatalf("shared capacity inaccessible for nested request %d: %v", i, e)
		}
		releases = append(releases, release)
	}
	pc := p.capacitySnapshot(testProj)["protocol_capacity"].(map[string]any)
	if pc["nested_protected_bytes"] != int64(16<<20) || pc["nested_reserved_bytes"] != int64(24<<20) || pc["nested_borrowed_bytes"] != int64(8<<20) || pc["nested_can_borrow_shared"] != true {
		t.Fatalf("API borrowing telemetry: %+v", pc)
	}
	remaining := (128 << 20) - protocolBytes.Load()
	if !reserveProtocol(remaining) {
		t.Fatal("could not occupy remaining budget")
	}
	defer protocolBytes.Add(-remaining)
	start := time.Now()
	for i := 0; i < 100; i++ {
		if _, e := p.acquireProtocol(context.Background(), "nested", maxFrame); errorCode(e) != "protocol_memory_limit" {
			t.Fatalf("hard cap bypassed: %v", e)
		}
	}
	if time.Since(start) > time.Second {
		t.Fatal("nested pressure waited behind parents")
	}
	if protocolBytes.Load() != 128<<20 {
		t.Fatal("failed acquisition changed reservations")
	}
}
