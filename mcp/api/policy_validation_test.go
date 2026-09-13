package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRepairLegacyFunctionRoutesIndependently(t *testing.T) {
	app, ctx := mountTestAppWithPlatform(t, newRecordingCORSPlatform())
	api, err := dbCreateAPI(ctx.AppDB(), apiInput{ProjectID: testProject, Slug: "migration", AuthJSON: `{"kind":"auth_jwt"}`})
	if err != nil {
		t.Fatal(err)
	}
	// Model an upgrade: one route inherits auth, another overrides it, and
	// neither pre-existing route has the newly required Function scope.
	var legacy *APIRoute
	for _, path := range []string{"/first", "/second"} {
		policy := "{}"
		if path == "/second" {
			policy = `{"kind":"auth_jwt"}`
		}
		route, _, err := dbUpsertRoute(ctx.AppDB(), routeInput{ProjectID: testProject, APIID: api.ID, Method: "POST", PathPattern: path, TargetKind: "function", TargetRef: "handler", AuthJSON: policy, Enabled: true})
		if err != nil {
			t.Fatal(err)
		}
		if path == "/second" {
			legacy = route
		}
	}
	repair := map[string]any{"api_slug": "migration", "method": "POST", "path_pattern": "/first", "target_kind": "function", "target_ref": "handler", "auth": map[string]any{"kind": "auth_jwt", "function_ids": []int64{12}}}
	if _, err := app.toolRouteAdd(ctx, repair); err != nil {
		t.Fatalf("another legacy route blocked repair: %v", err)
	}
	unchanged, err := dbGetRouteByID(ctx.AppDB(), testProject, legacy.ID)
	if err != nil || unchanged == nil || *unchanged != *legacy {
		t.Fatalf("repair changed sibling: %+v %v", unchanged, err)
	}
	t.Setenv("APTEVA_GATEWAY_URL", "http://platform.invalid")
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "test-outbound")
	functionCalls := 0
	app.httpClient = &http.Client{Transport: incidentRoundTripper(func(r *http.Request) (*http.Response, error) {
		response := `{"user":{"id":1},"org":"example"}`
		if r.URL.Path == "/api/apps/callback/apps/functions/call" {
			functionCalls++
			var call struct {
				Tool  string `json:"tool"`
				Input struct {
					Principal functionPrincipal `json:"principal"`
				} `json:"input"`
			}
			if json.NewDecoder(r.Body).Decode(&call) != nil || call.Tool != "functions_invoke_authenticated" || len(call.Input.Principal.FunctionIDs) != 1 || call.Input.Principal.FunctionIDs[0] != 12 {
				t.Error("repaired route lost its configured admission scope")
			}
			response = `{"status":"ok","response":"repaired"}`
		} else if r.URL.Path != "/api/apps/auth/me" {
			t.Errorf("unexpected callback or legacy fallback: %s", r.URL)
		}
		return &http.Response{StatusCode: 200, Header: make(http.Header), Body: io.NopCloser(strings.NewReader(response))}, nil
	})}
	for _, test := range []struct {
		path          string
		status, calls int
	}{{"/second", 503, 0}, {"/first", 200, 1}} {
		req := httptest.NewRequest("POST", "/gw/migration"+test.path+"?project_id="+testProject, strings.NewReader(`{}`))
		req.Header.Set("Authorization", "Bearer test-user-token")
		rec := httptest.NewRecorder()
		app.handleGateway(rec, req)
		if rec.Code != test.status || functionCalls != test.calls {
			t.Fatalf("%s status=%d calls=%d body=%s", test.path, rec.Code, functionCalls, rec.Body.String())
		}
	}
	// The route being edited (and any new route) must still satisfy the rule.
	repair["auth"] = map[string]any{"kind": "auth_jwt"}
	for _, path := range []string{"/second", "/new"} {
		repair["path_pattern"] = path
		if _, err := app.toolRouteAdd(ctx, repair); err == nil || !strings.Contains(err.Error(), "auth.function_ids") {
			t.Fatalf("invalid candidate %s accepted: %v", path, err)
		}
	}
	repair["path_pattern"] = "/second"
	repair["auth"] = map[string]any{"kind": "auth_jwt", "function_ids": []int64{34}}
	if _, err := app.toolRouteAdd(ctx, repair); err != nil {
		t.Fatalf("second repair failed: %v", err)
	}
}

func TestLegacyRouteRepairPreservesSharedCORSValidation(t *testing.T) {
	app, ctx := mountTestAppWithPlatform(t, newRecordingCORSPlatform())
	api, err := dbCreateAPI(ctx.AppDB(), apiInput{ProjectID: testProject, Slug: "cors-migration", AuthJSON: `{"kind":"auth_jwt"}`})
	if err != nil {
		t.Fatal(err)
	}
	origins := make([]string, 100)
	for i := range origins {
		origins[i] = fmt.Sprintf("https://origin-%d.example", i)
	}
	cors, _ := json.Marshal(map[string]any{"enabled": true, "origins": origins})
	for _, path := range []string{"/legacy", "/repair"} {
		if _, _, err := dbUpsertRoute(ctx.AppDB(), routeInput{ProjectID: testProject, APIID: api.ID, Method: "POST", PathPattern: path, TargetKind: "function", TargetRef: "handler", CORSJSON: string(cors), Enabled: true}); err != nil {
			t.Fatal(err)
		}
	}
	repair := map[string]any{"api_slug": "cors-migration", "method": "POST", "path_pattern": "/repair", "target_kind": "function", "target_ref": "handler", "auth": map[string]any{"kind": "auth_jwt", "function_ids": []int64{12}}, "cors": map[string]any{"enabled": true, "origins": []string{"https://additional.example"}}}
	if _, err := app.toolRouteAdd(ctx, repair); err == nil || !strings.Contains(err.Error(), "exceeds 100 origins") {
		t.Fatalf("shared origin limit lost: %v", err)
	}
	repair["cors"] = map[string]any{"enabled": true, "origins": []string{origins[0]}}
	if _, err := app.toolRouteAdd(ctx, repair); err != nil {
		t.Fatalf("valid deduplicated origins rejected: %v", err)
	}
	// Invalid CORS on a historical sibling must still be detected; only its
	// absent Function scope is exempt from blocking a different route's edit.
	if _, err := ctx.AppDB().Exec(`UPDATE api_routes SET cors_json=? WHERE api_id=? AND path_pattern='/legacy'`, `{"enabled":true,"origins":["*"]}`, api.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolRouteAdd(ctx, repair); err == nil {
		t.Fatal("invalid sibling CORS was ignored")
	}
}

func TestAPIWideAuthChangesStillValidateEveryRoute(t *testing.T) {
	app, ctx := mountTestApp(t)
	api, err := dbCreateAPI(ctx.AppDB(), apiInput{ProjectID: testProject, Slug: "global-policy", AuthJSON: `{"kind":"public"}`})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := dbUpsertRoute(ctx.AppDB(), routeInput{ProjectID: testProject, APIID: api.ID, Method: "POST", PathPattern: "/", TargetKind: "function", TargetRef: "handler", Enabled: true}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolAPIUpdate(ctx, map[string]any{"slug": "global-policy", "auth": map[string]any{"kind": "auth_jwt"}}); err == nil || !strings.Contains(err.Error(), "auth.function_ids") {
		t.Fatalf("API-wide auth change escaped validation: %v", err)
	}
}
