//go:build integration

package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// A small gateway supplies the same authenticated app identity as Server.
// Both applications run their production binaries with separate SQLite files.
func TestSeparateAppsStrategyExecutesThroughTrading(t *testing.T) {
	trading := tk.SpawnSidecar(t, "../trading", tk.WithProjectID("test-proj"), tk.WithEnv("APTEVA_INSTALL_ID", "11"), tk.WithConfig(map[string]string{"pricing_provider": "mock", "bootstrap_demo": "false", "tick_seconds": "1"}))
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
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	defer gateway.Close()
	strategies := tk.SpawnSidecar(t, ".", tk.WithProjectID("test-proj"), tk.WithEnv("APTEVA_INSTALL_ID", "10"), tk.WithEnv("APTEVA_GATEWAY_URL", gateway.URL))
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
