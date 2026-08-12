package main

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAppEventsConfigFiltersAndProjectsStaticOutput(t *testing.T) {
	cfg, err := parseAppEventsConfig(`{
		"topics":["row.*"],
		"match":{"data.table":"ventes"},
		"output":{"type":"invalidate","resource":"ventes"},
		"coalesce_ms":25
	}`)
	if err != nil {
		t.Fatal(err)
	}
	if !eventMatches(cfg, appBusEvent{Topic: "row.updated", Data: []byte(`{"table":"ventes","secret":"never-forward"}`)}) {
		t.Fatal("expected matching row event")
	}
	if eventMatches(cfg, appBusEvent{Topic: "row.updated", Data: []byte(`{"table":"customers"}`)}) {
		t.Fatal("event from another table must not match")
	}
	if eventMatches(cfg, appBusEvent{Topic: "table.altered", Data: []byte(`{"table":"ventes"}`)}) {
		t.Fatal("event from another topic must not match")
	}
}

func TestAppEventsConfigMatchesListAndProjectsConstrainedField(t *testing.T) {
	cfg, err := parseAppEventsConfig(`{
		"topics":["row.*"],
		"match":{"data.table":{"in":["appels","ventes","prospects"]}},
		"output":{"type":"invalidate","table":"$data.table"},
		"coalesce_ms":25
	}`)
	if err != nil {
		t.Fatal(err)
	}
	output, ok := projectAppEvent(cfg, appBusEvent{
		Topic: "row.updated",
		Data:  []byte(`{"table":"ventes","id":99,"secret":"never-forward"}`),
	})
	if !ok {
		t.Fatal("expected matching row event")
	}
	if got, want := string(output), `{"table":"ventes","type":"invalidate"}`; got != want {
		t.Fatalf("projected output = %s, want %s", got, want)
	}
	if _, ok := projectAppEvent(cfg, appBusEvent{Topic: "row.updated", Data: []byte(`{"table":"customers"}`)}); ok {
		t.Fatal("event outside the allowlist must not match")
	}
}

func TestAppEventsConfigRejectsUnsafeShape(t *testing.T) {
	bad := []string{
		`{"topics":[],"output":{"type":"invalidate"}}`,
		`{"topics":["row.*"],"match":{"project_id":"other"},"output":{"type":"invalidate"}}`,
		`{"topics":["row.*"],"match":{"data.table":{"$ne":"x"}},"output":{"type":"invalidate"}}`,
		`{"topics":["row.*"],"match":{"data.table":{"in":[]}},"output":{"type":"invalidate"}}`,
		`{"topics":["row.*"],"match":{"data.table":{"in":[{"secret":true}]}},"output":{"type":"invalidate"}}`,
		`{"topics":["row.*"],"match":{"data.table":{"in":["ventes"]}},"output":{"type":"invalidate","id":"$data.id"}}`,
		`{"topics":["row.*"],"match":{"data.type":{"in":["invalidate"]}},"output":{"type":"$data.type"}}`,
		`{"topics":["row.*"],"output":{}}`,
	}
	for _, raw := range bad {
		if _, err := parseAppEventsConfig(raw); err == nil {
			t.Fatalf("config should be rejected: %s", raw)
		}
	}
}

func TestAppEventsConfigBoundsInMatcher(t *testing.T) {
	values := make([]any, maxEventMatchValues+1)
	for i := range values {
		values[i] = fmt.Sprintf("table_%d", i)
	}
	raw, err := json.Marshal(map[string]any{
		"topics": []any{"row.*"},
		"match": map[string]any{
			"data.table": map[string]any{"in": values},
		},
		"output": map[string]any{"type": "invalidate", "table": "$data.table"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := parseAppEventsConfig(string(raw)); err == nil || !strings.Contains(err.Error(), "1 to 64") {
		t.Fatalf("oversized in matcher error = %v", err)
	}
}

func TestAppEventHubSharesOneUpstreamConnection(t *testing.T) {
	var connections atomic.Int32
	upstreamCanceled := make(chan struct{}, 1)
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		connections.Add(1)
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		upstreamCanceled <- struct{}{}
	}))
	t.Cleanup(platform.Close)

	manager := newAppEventHubManager(platform.URL, "token", http.DefaultClient)
	t.Cleanup(manager.close)
	ctx := context.Background()
	_, _, cancelOne, err := manager.subscribe(ctx, testProject, "tables", 0)
	if err != nil {
		t.Fatal(err)
	}
	_, _, cancelTwo, err := manager.subscribe(ctx, testProject, "tables", 0)
	if err != nil {
		t.Fatal(err)
	}
	if got := connections.Load(); got != 1 {
		t.Fatalf("upstream connections = %d, want 1", got)
	}
	cancelOne()
	if got := connections.Load(); got != 1 {
		t.Fatalf("first unsubscribe closed shared upstream; connections=%d", got)
	}
	cancelTwo()
	select {
	case <-upstreamCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("last unsubscribe did not cancel upstream")
	}
}

func TestGatewayAppEventsRequiresAuthentication(t *testing.T) {
	app, ctx := mountTestApp(t)
	if _, err := app.toolAPICreate(ctx, map[string]any{"slug": "public-events", "auth": map[string]any{"kind": "public"}}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolRouteAdd(ctx, map[string]any{
		"api_slug": "public-events", "method": "GET", "path_pattern": "/changes",
		"target_kind": "app_events", "target_ref": "tables",
		"events": map[string]any{
			"topics": []any{"row.*"},
			"output": map[string]any{"type": "invalidate", "resource": "ventes"},
		},
	}); err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/gw/public-events/changes?project_id="+testProject, nil)
	app.handleGateway(rr, req)
	if rr.Code != http.StatusUnauthorized || !strings.Contains(rr.Body.String(), "require api_key or auth_jwt") {
		t.Fatalf("response = %d %s", rr.Code, rr.Body.String())
	}
}

func TestGatewayProjectsFilteredCoalescedAppEvents(t *testing.T) {
	upstreamCanceled := make(chan struct{}, 1)
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/app-events/tables" || r.URL.Query().Get("project_id") != testProject {
			t.Errorf("upstream URL = %s", r.URL.String())
		}
		if r.Header.Get("Authorization") != "Bearer outbound-token" {
			t.Errorf("authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		w.(http.Flusher).Flush()
		for _, frame := range []string{
			appBusFrame(1, "row.updated", `{"table":"ventes","secret":"never-forward"}`),
			appBusFrame(2, "row.inserted", `{"table":"ventes","ids":[99]}`),
			appBusFrame(3, "row.updated", `{"table":"appels","id":41}`),
			appBusFrame(4, "row.updated", `{"table":"customers"}`),
		} {
			_, _ = io.WriteString(w, frame)
		}
		w.(http.Flusher).Flush()
		<-r.Context().Done()
		upstreamCanceled <- struct{}{}
	}))
	t.Cleanup(platform.Close)
	t.Setenv("APTEVA_GATEWAY_URL", platform.URL)
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "outbound-token")

	app, ctx := mountTestApp(t)
	if _, err := app.toolAPICreate(ctx, map[string]any{"slug": "sales", "auth": map[string]any{"kind": "api_key"}}); err != nil {
		t.Fatal(err)
	}
	api, err := dbGetAPIBySlug(ctx.AppDB(), testProject, "sales")
	if err != nil || api == nil {
		t.Fatalf("api = %+v err=%v", api, err)
	}
	_, secret, err := dbCreateAPIKey(ctx.AppDB(), testProject, api.ID, "browser")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolRouteAdd(ctx, map[string]any{
		"api_slug": "sales", "method": "GET", "path_pattern": "/events",
		"target_kind": "app_events", "target_ref": "tables",
		"events": map[string]any{
			"topics": []any{"row.inserted", "row.updated"},
			"match": map[string]any{
				"data.table": map[string]any{"in": []any{"appels", "ventes"}},
			},
			"output":      map[string]any{"type": "invalidate", "table": "$data.table"},
			"coalesce_ms": 25,
		},
	}); err != nil {
		t.Fatal(err)
	}

	gateway := httptest.NewServer(http.HandlerFunc(app.handleGateway))
	t.Cleanup(gateway.Close)
	requestCtx, cancel := context.WithCancel(context.Background())
	req, err := http.NewRequestWithContext(requestCtx, http.MethodGet,
		gateway.URL+"/gw/sales/events?project_id="+testProject+"&api_key="+secret, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(resp.Body)
	ready := readSSEEvent(reader)
	if !strings.Contains(ready, "event: ready") || !strings.Contains(ready, `"revalidate":true`) {
		t.Fatalf("ready event = %q", ready)
	}
	ventes := readSSEEvent(reader)
	if !strings.Contains(ventes, "id: 2") || !strings.Contains(ventes, "event: invalidate") ||
		!strings.Contains(ventes, `{"table":"ventes","type":"invalidate"}`) {
		t.Fatalf("ventes event = %q", ventes)
	}
	appels := readSSEEvent(reader)
	if !strings.Contains(appels, "id: 3") || !strings.Contains(appels, `{"table":"appels","type":"invalidate"}`) {
		t.Fatalf("appels event = %q", appels)
	}
	if projected := ventes + appels; strings.Contains(projected, "secret") || strings.Contains(projected, "ids") ||
		strings.Contains(projected, "install_id") || strings.Contains(projected, "customers") {
		t.Fatalf("internal payload leaked: %q", projected)
	}
	cancel()
	_ = resp.Body.Close()
	select {
	case <-upstreamCanceled:
	case <-time.After(2 * time.Second):
		t.Fatal("client cancellation did not close AppBus subscription")
	}
}

func appBusFrame(seq uint64, topic, data string) string {
	return fmt.Sprintf("id: %d\ndata: {\"topic\":%q,\"app\":\"tables\",\"project_id\":%q,\"install_id\":17,\"seq\":%d,\"time\":\"2026-08-12T00:00:00Z\",\"data\":%s}\n\n",
		seq, topic, testProject, seq, data)
}
