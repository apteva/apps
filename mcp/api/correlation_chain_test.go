package main

import (
	"bytes"
	"encoding/json"
	tk "github.com/apteva/app-sdk/testkit"
	"io"
	"net/http"
	"net/http/httptest"
	"net/http/httputil"
	"net/url"
	"strings"
	"testing"
)

func TestGatewayFunctionsTablesCorrelationChain(t *testing.T) {
	if testing.Short() {
		t.Skip("builds Functions and Tables SDK sidecars")
	}
	tables := tk.SpawnSidecar(t, "../tables", tk.WithProjectID(testProject), tk.WithEnv("APTEVA_GATEWAY_URL", ""), tk.WithEnv("APTEVA_OUTBOUND_TOKEN", ""))
	tableIDs := make(chan string, 1)
	tableBridge := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/tables/call") {
			io.WriteString(w, `{}`)
			return
		}
		var payload struct {
			Tool  string         `json:"tool"`
			Input map[string]any `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&payload); err != nil {
			t.Error(err)
			http.Error(w, "bad input", 400)
			return
		}
		id, _ := payload.Input["_request_id"].(string)
		tableIDs <- id
		if id == "" || r.Header.Get("X-Request-ID") != id {
			t.Error("callback header/argument correlation missing")
		}
		raw, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": payload.Tool, "arguments": payload.Input}})
		req, _ := http.NewRequestWithContext(r.Context(), "POST", tables.URL()+"/mcp", bytes.NewReader(raw))
		req.Header.Set("Authorization", "Bearer "+tables.Token())
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("X-Apteva-Project-ID", testProject)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		io.Copy(w, resp.Body)
	}))
	defer tableBridge.Close()
	fn := tk.SpawnSidecar(t, "../functions", tk.WithProjectID(testProject), tk.WithEnv("APTEVA_GATEWAY_URL", tableBridge.URL), tk.WithEnv("APTEVA_OUTBOUND_TOKEN", ""))
	fn.MCP("functions_create", map[string]any{"name": "trace-chain", "runtime": "node", "source": `export default async(e,c)=>({request_id:e.request_id,tables:await c.call("tables","tables_list",{})});`})
	u, _ := url.Parse(fn.URL())
	proxy := httputil.NewSingleHostReverseProxy(u)
	original := proxy.Director
	proxy.Director = func(r *http.Request) {
		original(r)
		r.URL.Path = strings.TrimPrefix(r.URL.Path, "/api/apps/callback/apps/functions/proxy")
		r.Header.Set("Authorization", "Bearer "+fn.Token())
	}
	fnBridge := httptest.NewServer(proxy)
	defer fnBridge.Close()
	t.Setenv("APTEVA_GATEWAY_URL", fnBridge.URL)
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "synthetic")
	app, ctx := mountTestApp(t)
	api, err := dbCreateAPI(ctx.AppDB(), apiInput{ProjectID: testProject, Slug: "trace"})
	if err != nil {
		t.Fatal(err)
	}
	_, _, err = dbUpsertRoute(ctx.AppDB(), routeInput{ProjectID: testProject, APIID: api.ID, Method: "GET", PathPattern: "/data", TargetKind: "function", TargetRef: "trace-chain", Enabled: true, TimeoutMS: 30000})
	if err != nil {
		t.Fatal(err)
	}
	rr := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/gw/trace/data?project_id="+testProject, nil)
	req.Header.Set("X-Request-ID", "client-spoof")
	app.handleGateway(rr, req)
	id := rr.Header().Get("X-Request-ID")
	if rr.Code != 200 || id == "" || id == "client-spoof" || !strings.Contains(rr.Body.String(), id) {
		t.Fatalf("status=%d id=%s body=%s", rr.Code, id, rr.Body.String())
	}
	select {
	case tableID := <-tableIDs:
		if tableID != id {
			t.Fatalf("Gateway=%s Tables=%s", id, tableID)
		}
	default:
		t.Fatal("Tables was not called")
	}
}
