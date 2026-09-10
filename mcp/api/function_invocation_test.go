package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestFunctionScopePolicyValidation(t *testing.T) {
	for _, raw := range []string{
		`{"kind":"auth_jwt","function_ids":[0]}`,
		`{"kind":"auth_jwt","function_ids":[-1]}`,
		`{"kind":"auth_jwt","function_ids":[12,12]}`,
		`{"kind":"auth_jwt","function_ids":[1.5]}`,
		`{"kind":"auth_jwt","function_ids":["*"]}`,
		`{"kind":"auth_jwt","function_ids":null}`,
		`{"kind":"public","function_ids":[12]}`,
		`{"kind":"auth_jwt","claims":["function_ids"]}`,
	} {
		if err := validateAuthPolicy(raw); err == nil {
			t.Errorf("accepted unsafe scope: %s", raw)
		}
	}
	ids := make([]int64, 101)
	for i := range ids {
		ids[i] = int64(i + 1)
	}
	raw, _ := json.Marshal(map[string]any{"kind": "auth_jwt", "function_ids": ids})
	if validateAuthPolicy(string(raw)) == nil {
		t.Fatal("accepted more than 100 Function IDs")
	}
	for _, kind := range []string{"api_key", "auth_jwt"} {
		if err := validateAuthPolicy(fmt.Sprintf(`{"kind":%q,"function_ids":[12,34]}`, kind)); err != nil {
			t.Fatal(err)
		}
	}
	base := `{"kind":"auth_jwt","function_ids":[12,34]}`
	override, err := effectiveAuthPolicy(base, `{"kind":"auth_jwt","function_ids":[56]}`)
	if err != nil || len(override.FunctionIDs) != 1 || override.FunctionIDs[0] != 56 {
		t.Fatalf("route widened scope: %+v %v", override, err)
	}
	inherited, err := effectiveAuthPolicy(base, `{}`)
	if err != nil || len(inherited.FunctionIDs) != 2 {
		t.Fatalf("scope inheritance: %+v %v", inherited, err)
	}
	app, ctx := mountTestApp(t)
	if _, err := app.toolAPICreate(ctx, map[string]any{"slug": "scope", "auth": map[string]any{"kind": "auth_jwt"}}); err != nil {
		t.Fatal(err)
	}
	args := map[string]any{"api_slug": "scope", "method": "POST", "path_pattern": "/", "target_kind": "function", "target_ref": "fn"}
	if _, err := app.toolRouteAdd(ctx, args); err == nil {
		t.Fatal("enabled authenticated Function without scope accepted")
	}
	args["auth"] = map[string]any{"kind": "auth_jwt", "function_ids": []int64{12, 34}}
	if _, err := app.toolRouteAdd(ctx, args); err != nil {
		t.Fatal(err)
	}
}

func invokeAuthenticatedFixture(t *testing.T, app *App, parent context.Context) (int, *httptest.ResponseRecorder, error) {
	t.Helper()
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{"requestContext":{"authorizer":{"principal":{"subject":"browser","function_ids":[999]}}}}`))
	req = req.WithContext(context.WithValue(parent, gatewayRequestIDKey{}, "gateway-id"))
	api := &API{ProjectID: testProject, AuthJSON: `{"kind":"auth_jwt"}`}
	route := &APIRoute{TargetKind: "function", TargetRef: "configured-name", AuthJSON: `{"kind":"auth_jwt","function_ids":[12,34]}`, TimeoutMS: 1000}
	auth := authContext{Kind: "auth_jwt", Principal: &Principal{Issuer: "issuer", Subject: "verified", ProjectID: testProject, TenantID: "tenant-a", Claims: map[string]any{"roles": []string{"reader"}}}}
	rec := httptest.NewRecorder()
	status, err := app.dispatchRoute(rec, req, api, route, "/items", nil, auth)
	return status, rec, err
}

func TestAuthenticatedFunctionResponseContract(t *testing.T) {
	structured := `{"statusCode":201,"headers":{"X-Result":"yes"},"body":"created"}`
	good, _ := json.Marshal(map[string]any{"status": "ok", "response": structured})
	content, _ := json.Marshal(map[string]any{"content": []any{map[string]any{"type": "text", "text": string(good)}}})
	wrapped, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "result": json.RawMessage(content)})
	for _, tc := range []struct {
		name, body       string
		upstream, status int
	}{
		{"direct", string(good), 200, 201},
		{"content", string(content), 200, 201},
		{"rpc", string(wrapped), 200, 201},
		{"text", `{"status":"ok","response":"plain result"}`, 200, 200},
		{"empty", `{"status":"ok","response":""}`, 200, 200},
		{"tool_denied", `{"jsonrpc":"2.0","result":{"isError":true,"content":[{"type":"text","text":"invocation identity or scope denied"}]}}`, 200, 403},
		{"rpc_denied", `{"jsonrpc":"2.0","error":{"code":-32000,"message":"invocation identity or scope denied"}}`, 200, 403},
		{"invalid_event", `{"isError":true,"content":[{"type":"text","text":"authenticated event must not contain session credentials"}]}`, 200, 400},
		{"runtime_timeout", `{"status":"error","response":"","error_code":"invocation_timeout"}`, 200, 504},
		{"queue_timeout", `{"status":"error","response":"","error_code":"queue_timeout"}`, 200, 504},
		{"execution_failure", `{"status":"error","response":"sensitive output","stderr":"sensitive error"}`, 200, 502},
		{"missing_response", `{"status":"ok"}`, 200, 502},
		{"malformed", `not json`, 200, 502},
		{"no_content", `{"isError":true}`, 200, 502},
		{"unsupported_tool", `{"jsonrpc":"2.0","error":{"code":-32601,"message":"unknown tool"}}`, 200, 502},
		{"platform_denied", `secret diagnostic`, 403, 403},
		{"platform_busy", `secret diagnostic`, 503, 503},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls++
				if r.URL.Path != "/api/apps/callback/apps/functions/call" {
					t.Errorf("legacy invocation used: %s", r.URL)
				}
				if r.Header.Get("Authorization") != "Bearer outbound" {
					t.Error("outbound credential missing")
				}
				var body struct {
					Tool  string         `json:"tool"`
					Input map[string]any `json:"input"`
				}
				if json.NewDecoder(r.Body).Decode(&body) != nil || body.Tool != "functions_invoke_authenticated" {
					t.Error("wrong callback contract")
				}
				if body.Input["name"] != "configured-name" || body.Input["_project_id"] != testProject || body.Input["request_id"] != "gateway-id" {
					t.Errorf("untrusted metadata: %+v", body)
				}
				principal, _ := body.Input["principal"].(map[string]any)
				ids, _ := principal["function_ids"].([]any)
				if principal["subject"] != "verified" || len(ids) != 2 || ids[0] != float64(12) {
					t.Error("browser changed identity or scope")
				}
				w.WriteHeader(tc.upstream)
				io.WriteString(w, tc.body)
			}))
			defer server.Close()
			t.Setenv("APTEVA_GATEWAY_URL", server.URL)
			t.Setenv("APTEVA_OUTBOUND_TOKEN", "outbound")
			status, rec, err := invokeAuthenticatedFixture(t, &App{httpClient: gatewayHTTPClient()}, context.Background())
			if status != tc.status || rec.Code != tc.status || calls != 1 {
				t.Fatalf("status=%d response=%d calls=%d err=%v body=%s", status, rec.Code, calls, err, rec.Body.String())
			}
			if tc.status == 201 && (rec.Body.String() != "created" || rec.Header().Get("X-Result") != "yes") {
				t.Fatalf("structured response lost: %s %v", rec.Body.String(), rec.Header())
			}
			if tc.status >= 400 && (strings.Contains(rec.Body.String(), "sensitive") || strings.Contains(rec.Body.String(), "secret")) {
				t.Fatal("Function diagnostics leaked to browser")
			}
		})
	}
}

func TestAuthenticatedFunctionCancellation(t *testing.T) {
	for _, cancelBrowser := range []bool{false, true} {
		t.Run(fmt.Sprint(cancelBrowser), func(t *testing.T) {
			started := make(chan struct{})
			canceled := make(chan struct{})
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				// Read the POST body so the server can observe the disconnect.
				io.Copy(io.Discard, r.Body)
				close(started)
				<-r.Context().Done()
				close(canceled)
			}))
			defer server.Close()
			t.Setenv("APTEVA_GATEWAY_URL", server.URL)
			t.Setenv("APTEVA_OUTBOUND_TOKEN", "outbound")
			ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
			defer cancel()
			if cancelBrowser {
				go func() { <-started; cancel() }()
			}
			status, _, err := invokeAuthenticatedFixture(t, &App{httpClient: gatewayHTTPClient()}, ctx)
			want := 504
			if cancelBrowser {
				want = 499
			}
			if status != want || err == nil {
				t.Fatalf("status=%d err=%v", status, err)
			}
			select {
			case <-canceled:
			case <-time.After(time.Second):
				t.Fatal("MCP transport did not propagate cancellation")
			}
		})
	}
}

func TestAuthenticatedFunctionMissingScopeNeverFallsBack(t *testing.T) {
	calls := 0
	app := &App{httpClient: &http.Client{Transport: incidentRoundTripper(func(r *http.Request) (*http.Response, error) { calls++; return nil, fmt.Errorf("unexpected callback") })}}
	req := httptest.NewRequest("POST", "/", strings.NewReader(`{}`))
	api := &API{ProjectID: testProject, AuthJSON: `{"kind":"auth_jwt"}`}
	route := &APIRoute{TargetKind: "function", TargetRef: "fn", TimeoutMS: 100}
	auth := authContext{Kind: "auth_jwt", Principal: &Principal{Subject: "verified", Issuer: "issuer", ProjectID: testProject}}
	status, err := app.dispatchRoute(httptest.NewRecorder(), req, api, route, "/", nil, auth)
	if status != 503 || err == nil || calls != 0 {
		t.Fatalf("missing scope status=%d calls=%d err=%v", status, calls, err)
	}
	route.AuthJSON = `{"kind":"auth_jwt","function_ids":[12]}`
	auth.Principal = nil
	status, err = app.dispatchRoute(httptest.NewRecorder(), req, api, route, "/", nil, auth)
	if status != 503 || err == nil || calls != 0 {
		t.Fatalf("missing principal status=%d calls=%d err=%v", status, calls, err)
	}
}
