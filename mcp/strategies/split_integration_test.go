//go:build integration

package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A small gateway supplies the same authenticated app identity as Server.
// Both applications run their production binaries with separate SQLite files.
func TestSeparateAppsStrategyExecutesThroughTrading(t *testing.T) {
	tradingDB := filepath.Join(t.TempDir(), "trading.db")
	strategiesDB := filepath.Join(t.TempDir(), "strategies.db")
	var continuations atomic.Int64
	trading := tk.SpawnSidecar(t, "../trading", tk.WithProjectID("test-proj"), tk.WithEnv("APTEVA_INSTALL_ID", "11"), tk.WithEnv("DB_PATH", tradingDB), tk.WithConfig(map[string]string{"pricing_provider": "mock", "bootstrap_demo": "false", "tick_seconds": "1"}))
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/apps/trading/call") {
			http.Error(w, "unexpected callback "+r.URL.Path, 404)
			return
		}
		var body struct {
			Tool  string         `json:"tool"`
			Input map[string]any `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		if body.Tool == tradingSnapshotTool && strArg(body.Input, "revision") != "" {
			continuations.Add(1)
		}
		rpc, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": body.Tool, "arguments": body.Input}})
		req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, trading.URL()+"/mcp", bytes.NewReader(rpc))
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+trading.Token())
		req.Header.Set(sdk.HeaderBoundCallerAppName, "strategies")
		req.Header.Set(sdk.HeaderBoundCallerInstallID, "10")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		defer resp.Body.Close()
		const gatewayCap = 16 << 20
		payload, err := io.ReadAll(io.LimitReader(resp.Body, gatewayCap+1))
		if err != nil || len(payload) > gatewayCap {
			http.Error(w, "app response too large", http.StatusBadGateway)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = w.Write(payload)
	}))
	defer gateway.Close()
	strategies := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"), tk.WithEnv("APTEVA_INSTALL_ID", "10"), tk.WithEnv("APTEVA_GATEWAY_URL", gateway.URL), tk.WithEnv("DB_PATH", strategiesDB))
	portfolio := trading.MCP("portfolio_create", map[string]any{"name": "Team portfolio", "allowed_classes": []string{"crypto"}, "starting_cash": 100000})
	id := portfolio["portfolio_id"].(float64)
	definition := map[string]any{"universe": []string{"BTC-USD"}, "cadence": "1d", "rules": []any{map[string]any{"allocate": []any{map[string]any{"symbol": "BTC-USD", "weight": .01}}}}}
	saved := strategies.MCP("strategy_create", map[string]any{"name": "Boundary test", "status": "active", "definition": definition})
	sid := saved["strategy"].(map[string]any)["id"].(float64)
	strategies.MCP("strategy_assign", map[string]any{"portfolio_id": id, "strategy_id": sid})
	deadline := time.Now().Add(25 * time.Second)
	var orders []any
	for time.Now().Before(deadline) {
		out := trading.MCP("orders_list", map[string]any{"portfolio_id": id, "status": "all"})
		orders, _ = out["orders"].([]any)
		if len(orders) > 0 && orders[0].(map[string]any)["status"] == "filled" {
			break
		}
		time.Sleep(300 * time.Millisecond)
	}
	if len(orders) != 1 {
		t.Fatalf("wanted exactly one order in Trading, got %#v", orders)
	}
	order := orders[0].(map[string]any)
	if order["status"] != "filled" || order["origin_app"] != "strategies" || order["origin_install_id"] != float64(10) || order["external_strategy_id"] != sid || order["decision_ref"] == "" {
		t.Fatalf("missing fill or attribution across apps: %#v", order)
	}
	summary := strategies.MCP("account_summary", map[string]any{"portfolio_id": id})
	authoritative := trading.MCP("account_summary", map[string]any{"portfolio_id": id})
	if summary["cash"] != authoritative["cash"] {
		t.Fatalf("Strategies returned a second balance ledger: %#v / %#v", summary, authoritative)
	}
	var status map[string]any
	if resp := strategies.POST(fmt.Sprintf("/portfolios/%.0f/orders", id), map[string]any{"qty": 100}, &status); resp.Status != 404 {
		t.Fatalf("Strategies exposes an execution HTTP route: %d", resp.Status)
	}
	tools, err := trading.MCPRaw("tools/list", nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range tools["tools"].([]any) {
		if entry.(map[string]any)["name"] == "strategy_create" {
			t.Fatal("Trading advertises strategy authoring")
		}
	}

	// Use a separate portfolio so the scheduled strategy's order cannot make
	// the assertion pass. These three archived rows require multiple pages.
	book := trading.MCP("portfolio_create", map[string]any{"name": "Paged book", "allowed_classes": []string{"crypto"}})
	bookID := int64(book["portfolio_id"].(float64))
	writer, err := sql.Open("sqlite", "file:"+tradingDB+"?_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	defer writer.Close()
	rationale := strings.Repeat("x", 1500000)
	for i := 0; i < 3; i++ {
		if _, err := writer.Exec(`INSERT INTO orders(id,project_id,portfolio_id,symbol,asset_class,side,type,qty,status,rationale,source)
			VALUES (?,'test-proj',?,'BTC-USD','crypto','buy','limit',1,'cancelled',?,'fixture')`, fmt.Sprintf("paged-%d", i), bookID, rationale); err != nil {
			t.Fatal(err)
		}
	}
	if resp := strategies.GET(fmt.Sprintf("/portfolios/%d/backtests", bookID), nil); resp.Status != 200 {
		t.Fatalf("Strategies could not apply the multi-page snapshot: %d %s", resp.Status, resp.Body)
	}
	reader, err := sql.Open("sqlite", "file:"+strategiesDB+"?mode=ro")
	if err != nil {
		t.Fatal(err)
	}
	defer reader.Close()
	var count, size int
	if err := reader.QueryRow(`SELECT COUNT(*), COALESCE(SUM(length(rationale)),0) FROM orders WHERE portfolio_id=?`, bookID).Scan(&count, &size); err != nil {
		t.Fatal(err)
	}
	if continuations.Load() == 0 || count != 3 || size != 3*len(rationale) {
		t.Fatalf("multi-page observation incomplete: continuations=%d orders=%d rationale_bytes=%d", continuations.Load(), count, size)
	}
}

func TestSidecar_ImportsObservedBidAskCSV(t *testing.T) {
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"), tk.WithConfig(map[string]string{}))
	out := sc.MCP("market_data_import", map[string]any{"symbol": "XAUUSD", "streams": []map[string]any{{"kind": "quotes", "source": "synthetic_integration", "csv": "timestamp,bid,ask\n2025-01-02T10:00:00Z,2600,2600.2\n"}}})
	inputs, ok := out["inputs"].([]any)
	if !ok || len(inputs) != 1 || out["input_sha256"] == "" {
		t.Fatal("binary MCP import lacks sourced observations", out)
	}
	data := inputs[0].(map[string]any)["data"].(map[string]any)
	if data["bid"] != float64(2600) || data["ask"] != 2600.2 {
		t.Fatal("binary MCP import changed executable prices", data)
	}
}
