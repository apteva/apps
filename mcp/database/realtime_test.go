package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"github.com/apteva/apps/mcp/database/engine"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"testing"
	"time"
)

func TestRealtimeHTTPAndPermissions(t *testing.T) {
	a := testApp(t)
	caller := &sdk.Caller{ProjectID: "p", AgentID: 1, DefaultEffect: "allow"}
	must := func(op string, args map[string]any) any {
		t.Helper()
		v, e := invoke(a, caller, op, args)
		if e != nil {
			t.Fatal(op, e)
		}
		return v
	}
	must("database_create", map[string]any{"database": "store", "adapter": "aptevadb"})
	must("collection_create", map[string]any{"database": "store", "collection": "orders", "fields": []any{map[string]any{"name": "n", "type": "integer"}}})
	must("realtime_configure", map[string]any{"database": "store", "enabled": true})
	deny := &sdk.Caller{ProjectID: "p", AgentID: 2, DefaultEffect: "allow", Grants: []sdk.Grant{{Effect: "deny", Permission: "database.read", Resource: "*"}}}
	_, e := invoke(a, deny, "changes", map[string]any{"database": "store", "collections": []any{"orders"}})
	if e == nil {
		t.Fatal("watch without read allowed")
	}
	for _, h := range []string{"X-Apteva-Caller-Agent", "X-Apteva-Subject-ID", sdk.HeaderBoundCallerInstallID} {
		req := httptest.NewRequest("POST", "/realtime/watch", strings.NewReader(`{"database":"store","collections":["orders"]}`))
		req.Header.Set("X-Apteva-Project-ID", "p")
		req.Header.Set(h, "1")
		w := httptest.NewRecorder()
		a.handleWatch(w, req)
		if w.Code != 403 {
			t.Fatal(h, w.Code)
		}
	}
	server := httptest.NewServer(http.HandlerFunc(a.handleWatch))
	defer server.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	body := []byte(`{"database":"store","collections":["orders"],"initial":{"op":"find","args":{"collection":"orders"}}}`)
	req, e := http.NewRequestWithContext(ctx, "POST", server.URL, bytes.NewReader(body))
	if e != nil {
		t.Fatal(e)
	}
	req.Header.Set("X-Apteva-Project-ID", "p")
	response, e := http.DefaultClient.Do(req)
	if e != nil {
		t.Fatal(e)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatal(response.Status)
	}
	scan := bufio.NewScanner(response.Body)
	next := func() (string, map[string]any) {
		t.Helper()
		event := ""
		var data map[string]any
		for scan.Scan() {
			line := scan.Text()
			if strings.HasPrefix(line, "event: ") {
				event = strings.TrimPrefix(line, "event: ")
			}
			if strings.HasPrefix(line, "data: ") {
				if e = json.Unmarshal([]byte(strings.TrimPrefix(line, "data: ")), &data); e != nil {
					t.Fatal(e)
				}
			}
			if line == "" && event != "" {
				return event, data
			}
		}
		t.Fatal("stream ended", scan.Err())
		return "", nil
	}
	if event, _ := next(); event != "snapshot" {
		t.Fatal(event)
	}
	event, ready := next()
	if event != "ready" || ready["cursor"] == "" {
		t.Fatal(event, ready)
	}
	must("insert", map[string]any{"database": "store", "collection": "orders", "records": []any{map[string]any{"id": "o1", "n": "1"}}})
	event, data := next()
	if event != "invalidate" || data["cursor"] == ready["cursor"] {
		t.Fatal(event, data)
	}
	// MCP resumes the same delivery token, waits until timeout, then releases its watcher.
	result := must("changes", map[string]any{"database": "store", "collections": []any{"orders"}, "after": data["cursor"], "waitMs": 10}).(map[string]any)
	if result["cursor"] != data["cursor"] {
		t.Fatal(result)
	}
	cancel()
	response.Body.Close()
	// Malformed tokens cannot be interpreted as arbitrary native collection watches.
	_, e = invoke(a, caller, "changes", map[string]any{"database": "store", "collections": []any{"orders"}, "after": "forged"})
	if known, ok := e.(*engine.Error); !ok || known.Code != "resume_invalid" {
		t.Fatal(e)
	}
}

func TestRealtimeSlowAndIdleWatchersDoNotHoldDatabaseLocks(t *testing.T) {
	a := testApp(t)
	c := &sdk.Caller{ProjectID: "p", AgentID: 1, DefaultEffect: "allow"}
	for _, item := range []struct {
		op   string
		args map[string]any
	}{
		{"database_create", map[string]any{"database": "native", "adapter": "aptevadb"}},
		{"collection_create", map[string]any{"database": "native", "collection": "items", "fields": []any{map[string]any{"name": "n", "type": "integer"}}}},
		{"realtime_configure", map[string]any{"database": "native", "enabled": true}},
	} {
		if _, e := invoke(a, c, item.op, item.args); e != nil {
			t.Fatal(e)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	watch, e := a.manager.OpenWatch(ctx, scope("p", nil), engine.Request{Database: "native", Collections: []string{"items"}})
	if e != nil {
		t.Fatal(e)
	}
	defer watch.Close()
	// Deliberately never call Next; ordinary CRUD must still complete.
	for i := 0; i < 20; i++ {
		if _, e := invoke(a, c, "insert", map[string]any{"database": "native", "collection": "items", "records": []any{map[string]any{"n": "1"}}}); e != nil {
			t.Fatal(e)
		}
	}
	if _, e := invoke(a, c, "count", map[string]any{"database": "native", "collection": "items"}); e != nil {
		t.Fatal(e)
	}
}

// Opt-in transport timing. Real HTTP streaming, with the application operation
// route as writer. This does not include the platform proxy or browser rendering.
func TestRealtimeHTTPPerformance(t *testing.T) {
	path := os.Getenv("DB_REALTIME_HTTP_PERF_OUTPUT")
	if path == "" {
		t.Skip("set DB_REALTIME_HTTP_PERF_OUTPUT")
	}
	var results []map[string]any
	for _, mode := range []string{"os", "full"} {
		for _, count := range []int{1, 8} {
			t.Run(fmt.Sprintf("%s_%d", mode, count), func(t *testing.T) {
				options := engine.DefaultAptevaOptions()
				options.SyncMode = mode
				m, e := engine.OpenWithConfig(t.TempDir(), engine.ManagerOptions{Apteva: &options})
				if e != nil {
					t.Fatal(e)
				}
				defer m.Close()
				a := &App{manager: m}
				caller := &sdk.Caller{ProjectID: "p", AgentID: 1, DefaultEffect: "allow"}
				for _, item := range []struct {
					op   string
					args map[string]any
				}{
					{"database_create", map[string]any{"database": "native", "adapter": "aptevadb"}},
					{"collection_create", map[string]any{"database": "native", "collection": "items", "fields": []any{map[string]any{"name": "n", "type": "integer"}}}},
					{"realtime_configure", map[string]any{"database": "native", "enabled": true}},
				} {
					if _, e := invoke(a, caller, item.op, item.args); e != nil {
						t.Fatal(e)
					}
				}
				mux := http.NewServeMux()
				mux.HandleFunc("/realtime/watch", a.handleWatch)
				mux.HandleFunc("/operations/", a.handleHTTP)
				server := httptest.NewServer(mux)
				defer server.Close()
				ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
				defer cancel()
				clients := make([]chan time.Time, count)
				fail := make(chan error, count)
				for i := range clients {
					req, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/realtime/watch", strings.NewReader(`{"database":"native","collections":["items"]}`))
					req.Header.Set("X-Apteva-Project-ID", "p")
					res, e := http.DefaultClient.Do(req)
					if e != nil {
						t.Fatal(e)
					}
					if res.StatusCode != 200 {
						t.Fatal(res.Status)
					}
					defer res.Body.Close()
					ready := make(chan struct{})
					clients[i] = make(chan time.Time, 1)
					go func(i int) {
						scan := bufio.NewScanner(res.Body)
						event := ""
						for scan.Scan() {
							line := scan.Text()
							if strings.HasPrefix(line, "event: ") {
								event = strings.TrimPrefix(line, "event: ")
							}
							if line == "" && event == "ready" {
								close(ready)
								event = ""
							}
							if line == "" && event == "invalidate" {
								event = ""
								select {
								case clients[i] <- time.Now():
								case <-ctx.Done():
									return
								}
							}
						}
						if e := scan.Err(); e != nil {
							select {
							case fail <- e:
							default:
							}
						}
					}(i)
					select {
					case <-ready:
					case e := <-fail:
						t.Fatal(e)
					case <-ctx.Done():
						t.Fatal(ctx.Err())
					}
				}
				var latencies []float64
				for i := 0; i < 100; i++ {
					body := fmt.Sprintf(`{"database":"native","collection":"items","records":[{"id":"r%d","n":"1"}]}`, i)
					req, _ := http.NewRequestWithContext(ctx, "POST", server.URL+"/operations/insert", strings.NewReader(body))
					req.Header.Set("X-Apteva-Project-ID", "p")
					start := time.Now()
					res, e := http.DefaultClient.Do(req)
					if e != nil {
						t.Fatal(e)
					}
					io.Copy(io.Discard, res.Body)
					res.Body.Close()
					if res.StatusCode != 200 {
						t.Fatal(res.Status)
					}
					for _, ch := range clients {
						select {
						case at := <-ch:
							latencies = append(latencies, float64(at.Sub(start).Nanoseconds())/1000)
						case e := <-fail:
							t.Fatal(e)
						case <-ctx.Done():
							t.Fatal("missed streamed event", ctx.Err())
						}
					}
				}
				sort.Float64s(latencies)
				results = append(results, map[string]any{"sync_mode": mode, "watchers": count, "samples": len(latencies), "p50_us": latencies[len(latencies)/2], "p95_us": latencies[int(float64(len(latencies))*.95)], "times_us": latencies})
			})
		}
	}
	data, e := json.MarshalIndent(map[string]any{"results": results, "passed": !t.Failed()}, "", "  ")
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(path, data, 0644); e != nil {
		t.Fatal(e)
	}
}
