package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"github.com/gorilla/websocket"
)

// databaseBridge stands in for apteva-server's app callback bridge in front of
// a Database sidecar: it binds the GraphQL install as caller, forwards the
// negotiated direct JSON result format, and counts calls by tool.
type databaseBridge struct {
	URL   string
	mu    sync.Mutex
	calls map[string]int
	conns int // TCP connections accepted from the GraphQL sidecar
}

func newDatabaseBridge(t *testing.T, database *tk.Sidecar, project string) *databaseBridge {
	t.Helper()
	bridge := &databaseBridge{calls: map[string]int{}}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var call struct {
			Tool  string         `json:"tool"`
			Input map[string]any `json:"input"`
		}
		if r.URL.Path != "/api/apps/callback/apps/database/call" || json.NewDecoder(r.Body).Decode(&call) != nil {
			http.Error(w, "invalid callback", http.StatusBadRequest)
			return
		}
		bridge.mu.Lock()
		bridge.calls[call.Tool]++
		bridge.mu.Unlock()
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": call.Tool, "arguments": call.Input}})
		req, _ := http.NewRequestWithContext(r.Context(), http.MethodPost, database.URL()+"/mcp", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+database.Token())
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Apteva-Project-ID", project)
		req.Header.Set(sdk.HeaderBoundCallerInstallID, "1")
		req.Header.Set(sdk.HeaderBoundCallerAppName, "graphql")
		direct := r.Header.Get(sdk.HeaderAppResultFormat) == sdk.AppResultJSON
		if direct {
			req.Header.Set(sdk.HeaderAppResultFormat, sdk.AppResultJSON)
		}
		resp, err := bridgeClient.Do(req)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadGateway)
			return
		}
		defer resp.Body.Close()
		if direct && resp.Header.Get(sdk.HeaderAppResultFormat) == sdk.AppResultJSON {
			w.Header().Set(sdk.HeaderAppResultFormat, sdk.AppResultJSON)
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	server.Config.ConnState = func(_ net.Conn, state http.ConnState) {
		if state == http.StateNew {
			bridge.mu.Lock()
			bridge.conns++
			bridge.mu.Unlock()
		}
	}
	t.Cleanup(server.Close)
	bridge.URL = server.URL
	return bridge
}

// bridgeClient pools connections to sidecars so the harness itself does not
// churn ephemeral ports under load; app-side clients are left as shipped.
var bridgeClient = &http.Client{Transport: &http.Transport{MaxIdleConns: 512, MaxIdleConnsPerHost: 256, IdleConnTimeout: 30 * time.Second}}

// takeCalls returns and resets the per-tool call counts and new connections.
func (b *databaseBridge) takeCalls() (map[string]int, int) {
	b.mu.Lock()
	defer b.mu.Unlock()
	calls, conns := b.calls, b.conns
	b.calls, b.conns = map[string]int{}, 0
	return calls, conns
}

// spawnGraph starts the GraphQL sidecar. GRAPHQL_BENCH_SIDECAR_GOFLAGS applies
// only to its build (for example -modfile=... pointing at an SDK branch), so
// the Database sidecar keeps its own pinned dependencies.
func spawnGraph(t *testing.T, opts ...tk.Option) *tk.Sidecar {
	t.Helper()
	if flags := os.Getenv("GRAPHQL_BENCH_SIDECAR_GOFLAGS"); flags != "" {
		previous, had := os.LookupEnv("GOFLAGS")
		os.Setenv("GOFLAGS", flags)
		defer func() {
			if had {
				os.Setenv("GOFLAGS", previous)
			} else {
				os.Unsetenv("GOFLAGS")
			}
		}()
	}
	return tk.SpawnSidecar(t, ".", opts...)
}

// dialRealtime opens an initialized graphql-transport-ws connection.
func dialRealtime(t *testing.T, graph *tk.Sidecar, project string) *websocket.Conn {
	t.Helper()
	header := http.Header{}
	header.Set("Authorization", "Bearer "+graph.Token())
	header.Set("X-Apteva-Project-ID", project)
	header.Set("Sec-WebSocket-Protocol", "graphql-transport-ws")
	conn, resp, err := websocket.DefaultDialer.Dial("ws"+strings.TrimPrefix(graph.URL(), "http")+"/realtime/default", header)
	if err != nil {
		status := 0
		if resp != nil {
			status = resp.StatusCode
		}
		t.Fatalf("dial realtime: status=%d err=%v", status, err)
	}
	if err := conn.WriteJSON(map[string]any{"type": "connection_init"}); err != nil {
		t.Fatal(err)
	}
	var ack wsMessage
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	if err := conn.ReadJSON(&ack); err != nil || ack.Type != "connection_ack" {
		t.Fatalf("realtime init: %v %v", ack, err)
	}
	_ = conn.SetReadDeadline(time.Time{})
	return conn
}

// This opt-in test runs released Database (AptevaDB change capture) behind the
// GraphQL sidecar and checks that a GraphQL subscription re-runs when another
// caller commits to the watched collection.
func TestRealDatabaseSubscriptionFollowsChangeFeed(t *testing.T) {
	databaseDir := os.Getenv("GRAPHQL_TEST_DATABASE_DIR")
	if testing.Short() || databaseDir == "" {
		t.Skip("set GRAPHQL_TEST_DATABASE_DIR to released Database source")
	}
	const project = "graphql-database-realtime-test"
	database := tk.SpawnSidecar(t, databaseDir, tk.WithProjectID(project), tk.WithEnv("APTEVA_GATEWAY_URL", ""), tk.WithEnv("APTEVA_OUTBOUND_TOKEN", ""))
	operator := func(tool string, args map[string]any) map[string]any {
		return database.MCPAs(tool, args, 1, "", project)
	}
	fields := []any{map[string]any{"name": "amount", "type": "number"}}
	for _, setup := range []struct{ name, adapter string }{{"live", "aptevadb"}, {"plain", "sqlite"}} {
		operator("db_database_create", map[string]any{"database": setup.name, "adapter": setup.adapter})
		operator("db_collection_create", map[string]any{"database": setup.name, "collection": "orders", "fields": fields})
		operator("db_access_grant", map[string]any{"database": setup.name, "app": "graphql", "access": "read"})
	}
	operator("db_realtime_configure", map[string]any{"database": "live", "enabled": true})

	proxy := newDatabaseBridge(t, database, project)

	graph := tk.SpawnSidecar(t, ".", tk.WithProjectID(project), tk.WithEnv("APTEVA_GATEWAY_URL", proxy.URL), tk.WithEnv("APTEVA_OUTBOUND_TOKEN", "database-realtime-test"))
	for _, name := range []string{"live", "plain"} {
		graph.MCP("graphql_source_add", map[string]any{"name": name, "kind": "database", "config": map[string]any{"database": name, "collection": "orders"}})
		graph.MCP("graphql_resolver_set", map[string]any{"parent_type": "Subscription", "field_name": name + "Count", "source": name, "operation": "count"})
	}
	schema := graph.MCP("graphql_schema_create", map[string]any{"environment": "development", "sdl": `type Query { ping: Int } type Subscription { liveCount: Int! plainCount: Int! }`})
	graph.MCP("graphql_release_publish", map[string]any{"environment": "development", "schema_version": schema["schema"].(map[string]any)["version"]})

	conn := dialRealtime(t, graph, project)
	defer conn.Close()
	send := func(message map[string]any) {
		if err := conn.WriteJSON(message); err != nil {
			t.Fatal(err)
		}
	}
	// Messages for different operations interleave; buffer the ones not awaited.
	pending := []wsMessage{}
	await := func(id, kind string) wsMessage {
		t.Helper()
		for index, message := range pending {
			if message.ID == id && message.Type == kind {
				pending = append(pending[:index], pending[index+1:]...)
				return message
			}
		}
		_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
		for {
			var message wsMessage
			if err := conn.ReadJSON(&message); err != nil {
				t.Fatalf("waiting for %s %s: %v", id, kind, err)
			}
			if message.ID == id && message.Type == kind {
				return message
			}
			if message.ID == id && message.Type == "error" {
				t.Fatalf("operation %s failed: %s", id, message.Payload)
			}
			pending = append(pending, message)
		}
	}
	count := func(message wsMessage, field string) int {
		var payload struct {
			Data   map[string]int `json:"data"`
			Errors []any          `json:"errors"`
		}
		if err := json.Unmarshal(message.Payload, &payload); err != nil || len(payload.Errors) > 0 {
			t.Fatalf("subscription payload: %s", message.Payload)
		}
		return payload.Data[field]
	}

	send(map[string]any{"id": "a", "type": "subscribe", "payload": map[string]any{"query": "subscription { liveCount }"}})
	send(map[string]any{"id": "b", "type": "subscribe", "payload": map[string]any{"query": "subscription { liveCount }"}})
	if count(await("a", "next"), "liveCount") != 0 || count(await("b", "next"), "liveCount") != 0 {
		t.Fatal("initial counts")
	}
	// Both subscribers share one native watcher (Database allows 8 per app).
	if stats, _ := operator("db_realtime_describe", map[string]any{"database": "live"})["stats"].(map[string]any); stats["Watchers"] != float64(1) {
		t.Fatalf("subscribers did not share one watcher: %v", stats)
	}

	// Each commit by another caller must reach both subscribers.
	latencies := []time.Duration{}
	for want := 1; want <= 10; want++ {
		started := time.Now()
		operator("db_insert", map[string]any{"database": "live", "collection": "orders", "records": []any{map[string]any{"amount": float64(want)}}})
		for _, id := range []string{"a", "b"} {
			for got := -1; got != want; {
				got = count(await(id, "next"), "liveCount")
				if got > want {
					t.Fatalf("%s saw %d before commit %d", id, got, want)
				}
			}
		}
		latencies = append(latencies, time.Since(started))
	}
	sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
	t.Logf("commit -> both subscribers updated: p50=%s max=%s (10 single-record commits, insert call included)", latencies[len(latencies)/2].Round(100*time.Microsecond), latencies[len(latencies)-1].Round(100*time.Microsecond))

	// Without native capture the subscription fails up front, not silently.
	send(map[string]any{"id": "c", "type": "subscribe", "payload": map[string]any{"query": "subscription { plainCount }"}})
	failure := await("c", "error")
	if !strings.Contains(string(failure.Payload), "aptevadb") {
		t.Fatalf("unsupported adapter error: %s", failure.Payload)
	}
	send(map[string]any{"id": "a", "type": "complete"})
	send(map[string]any{"id": "b", "type": "complete"})
}
