package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

// context.call(app, tool, input, {install_id}) pins one installation of the
// target app. Without it the platform resolves the app name to the project's
// default installation.
func TestContextCallTargetsInstallation(t *testing.T) {
	requireBin(t, "node")
	var mu sync.Mutex
	var bodies []map[string]any
	gw := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/apps/callback/apps/telephony/call" {
			http.NotFound(w, r)
			return
		}
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		mu.Lock()
		bodies = append(bodies, body)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"jsonrpc":"2.0","result":{"content":[{"type":"text","text":"{\"ok\":true}"}]}}`)
	}))
	defer gw.Close()
	t.Setenv("APTEVA_GATEWAY_URL", gw.URL)
	t.Setenv("APTEVA_APP_TOKEN", "test-token")

	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithPlatform(&tk.BasePlatformClient{}))
	app := mountApp(t, ctx)
	fn := createFn(t, app, ctx, map[string]any{
		"name": "pinned-call",
		"source": `export default async (event, context) => {
			const a = await context.call("telephony", "telephony_call_get", { call_id: "c1" }, { install_id: 124 });
			const b = await context.call("telephony", "telephony_call_get", { call_id: "c2" });
			let rejected = "";
			try { await context.call("telephony", "telephony_call_get", {}, { install_id: -3 }); }
			catch (err) { rejected = String(err.message || err); }
			return { a, b, rejected };
		};`,
	})
	res, err := invokeFunction(ctx, context.Background(), fn, map[string]any{}, "manual")
	if err != nil {
		t.Fatalf("invoke: %v", err)
	}
	if res.Status != "ok" {
		t.Fatalf("status = %q (err=%q stderr=%q)", res.Status, res.Error, res.Stderr)
	}
	if !strings.Contains(res.Response, "install_id must be a positive integer") {
		t.Fatalf("invalid install_id not rejected: %s", res.Response)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(bodies) != 2 {
		t.Fatalf("gateway calls = %d, want 2 (%v)", len(bodies), bodies)
	}
	if got := bodies[0]["target_install_id"]; got != float64(124) {
		t.Fatalf("pinned call target_install_id = %v, want 124", got)
	}
	if _, ok := bodies[1]["target_install_id"]; ok {
		t.Fatalf("unpinned call carried target_install_id: %v", bodies[1])
	}
	if in, _ := bodies[0]["input"].(map[string]any); in["call_id"] != "c1" || in["_project_id"] != testProj {
		t.Fatalf("pinned call input = %v", bodies[0]["input"])
	}
}

func TestContextCallInstallationRules(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj), tk.WithPlatform(&stubPlatform{result: json.RawMessage(`{}`)}))
	for _, tc := range []struct {
		name string
		msg  wireResponse
		want string
	}{
		{"negative", wireResponse{Type: "call", App: "telephony", Tool: "x", InstallID: -1}, "positive"},
		{"tables", wireResponse{Type: "call", App: "tables", Tool: "rows_list", InstallID: 5}, "not supported for tables"},
		{"functions", wireResponse{Type: "call", App: "functions", Tool: "functions_invoke", InstallID: 5}, "not supported for functions"},
		// Without a context-aware platform client or gateway credentials the
		// selection cannot be honoured, so it must fail instead of silently
		// reaching the default installation.
		{"no gateway", wireResponse{Type: "call", App: "telephony", Tool: "x", InstallID: 5}, "not available"},
	} {
		ans := dispatchPlatformFrame(context.Background(), ctx, tc.msg)
		if ans.OK || !strings.Contains(ans.Error, tc.want) {
			t.Fatalf("%s: ans = %+v, want error containing %q", tc.name, ans, tc.want)
		}
	}
}
