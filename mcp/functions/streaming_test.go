package main

import (
	"bufio"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

type streamGatePlatform struct {
	tk.BasePlatformClient
	entered     chan struct{}
	release     chan struct{}
	once        sync.Once
	releaseOnce sync.Once
}

func (p *streamGatePlatform) CallAppResult(_ string, _ string, _ map[string]any, out any) error {
	p.once.Do(func() { close(p.entered) })
	<-p.release
	return json.Unmarshal([]byte(`{"released":true}`), out)
}

func (p *streamGatePlatform) unblock() {
	p.releaseOnce.Do(func() { close(p.release) })
}

func TestHTTPInvocationStreamsBeforeHandlerCompletes(t *testing.T) {
	requireBin(t, "node")
	requireBin(t, "go")

	tests := []struct {
		name    string
		runtime string
		source  string
	}{
		{
			name:    "node",
			runtime: "node",
			source: `export default async (_event, context) => {
  await context.sse.send("one", {event: "tick"});
  await context.call("gate", "wait", {});
  await context.sse.send({value: "two"}, {event: "tick"});
  return null;
};`,
		},
		{
			name:    "go",
			runtime: "go",
			source: `package main

import "encoding/json"

func Handle(_ json.RawMessage, ctx *Context) (any, error) {
	if err := ctx.SSE("tick", "one"); err != nil { return nil, err }
	if _, err := ctx.Call("gate", "wait", map[string]any{}); err != nil { return nil, err }
	if err := ctx.SSE("tick", map[string]any{"value": "two"}); err != nil { return nil, err }
	return nil, nil
}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gate := &streamGatePlatform{entered: make(chan struct{}), release: make(chan struct{})}
			t.Cleanup(gate.unblock)
			ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithPlatform(gate))
			app := mountApp(t, ctx)
			fn := createFn(t, app, ctx, map[string]any{
				"name": "stream-" + tt.name, "runtime": tt.runtime, "source": tt.source,
			})

			server := httptest.NewServer(http.HandlerFunc(app.handleHTTPInvokeByName))
			t.Cleanup(server.Close)
			resp, err := http.Post(server.URL+"/fn/"+fn.Name+"?project_id="+testProj, "application/json", strings.NewReader(`{}`))
			if err != nil {
				t.Fatalf("invoke: %v", err)
			}
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				body, _ := io.ReadAll(resp.Body)
				t.Fatalf("status = %d body=%s", resp.StatusCode, body)
			}
			if got := resp.Header.Get("Content-Type"); !strings.HasPrefix(got, "text/event-stream") {
				t.Fatalf("Content-Type = %q", got)
			}
			if got := resp.Header.Get("X-Apteva-Function-Stream"); got != "true" {
				t.Fatalf("X-Apteva-Function-Stream = %q", got)
			}

			select {
			case <-gate.entered:
			case <-time.After(5 * time.Second):
				t.Fatal("handler did not reach blocking downstream call")
			}

			firstEvent := make(chan string, 1)
			reader := bufio.NewReader(resp.Body)
			go func() {
				var event strings.Builder
				for {
					line, readErr := reader.ReadString('\n')
					event.WriteString(line)
					if readErr != nil || line == "\n" {
						firstEvent <- event.String()
						return
					}
				}
			}()
			select {
			case event := <-firstEvent:
				if !strings.Contains(event, "event: tick") || !strings.Contains(event, "data: one") {
					t.Fatalf("first event = %q", event)
				}
			case <-time.After(2 * time.Second):
				t.Fatal("first SSE event was buffered while handler was blocked")
			}

			gate.unblock()
			rest, err := io.ReadAll(reader)
			if err != nil {
				t.Fatalf("read remaining stream: %v", err)
			}
			if !strings.Contains(string(rest), `data: {"value":"two"}`) {
				t.Fatalf("remaining stream = %q", rest)
			}

			invocations, err := dbListInvocations(ctx.AppDB(), testProj, fn.ID, 10)
			if err != nil || len(invocations) != 1 {
				t.Fatalf("invocations = %d err=%v", len(invocations), err)
			}
			if !strings.Contains(invocations[0].ResponseBody, "data: one") || !strings.Contains(invocations[0].ResponseBody, "value") {
				t.Fatalf("stored stream preview = %q", invocations[0].ResponseBody)
			}
		})
	}
}

func TestManualInvokeKeepsUnaryContractForStreamingHandler(t *testing.T) {
	requireBin(t, "node")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	fn := createFn(t, app, ctx, map[string]any{
		"name": "manual-stream",
		"source": `export default async (_event, context) => {
  await context.sse.send("one");
  await context.sse.send("two");
};`,
	})

	out, err := app.toolInvoke(ctx, map[string]any{"id": fn.ID, "event": nil})
	if err != nil {
		t.Fatalf("toolInvoke: %v", err)
	}
	result := out.(map[string]any)
	if result["status"] != "ok" || !strings.Contains(result["response"].(string), "data: one") {
		t.Fatalf("unary result = %#v", result)
	}
}

func TestHTTPStreamIsDeliveredBeyondStoredPreviewCap(t *testing.T) {
	requireBin(t, "node")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	fn := createFn(t, app, ctx, map[string]any{
		"name": "large-stream",
		"source": `export default async (_event, context) => {
  await context.stream.start({headers: {"Content-Type": "text/plain"}});
  await context.stream.write("x".repeat(70000));
};`,
	})

	req := httptest.NewRequest(http.MethodPost, "/fn/large-stream?project_id="+testProj, strings.NewReader(`{}`))
	rr := httptest.NewRecorder()
	app.handleHTTPInvokeByName(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d body=%s", rr.Code, rr.Body.String())
	}
	if rr.Body.Len() != 70000 {
		t.Fatalf("streamed body length = %d, want 70000", rr.Body.Len())
	}
	invocations, err := dbListInvocations(ctx.AppDB(), testProj, fn.ID, 10)
	if err != nil || len(invocations) != 1 {
		t.Fatalf("invocations = %d err=%v", len(invocations), err)
	}
	if got := len(invocations[0].ResponseBody); got != stdoutCap {
		t.Fatalf("stored preview length = %d, want %d", got, stdoutCap)
	}
}
