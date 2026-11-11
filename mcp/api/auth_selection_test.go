package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestAlternativeAuthPolicyValidation(t *testing.T) {
	const policy = `{"kind":"any_of","function_ids":[12],"methods":[{"kind":"auth_jwt","tenant_id":"tenant-a","claims":["roles"]},{"kind":"api_key","required_scopes":["records:read"]}]}`
	inherited, err := effectiveAuthPolicy(policy, `{}`)
	if err != nil || inherited.Kind != "any_of" || len(inherited.Methods) != 2 || len(inherited.FunctionIDs) != 1 {
		t.Fatalf("inherited alternatives: %+v %v", inherited, err)
	}
	replaced, err := effectiveAuthPolicy(policy, `{"kind":"api_key"}`)
	if err != nil || replaced.Kind != "api_key" || len(replaced.Methods) != 0 || len(replaced.FunctionIDs) != 0 {
		t.Fatalf("route replacement retained alternatives: %+v %v", replaced, err)
	}
	for _, methods := range []string{
		`[]`, `null`, `[{}]`,
		`[{"kind":"public"},{"kind":"api_key"}]`,
		`[{"kind":"any_of","methods":[]},{"kind":"api_key"}]`,
		`[{"kind":"auth_jwt"},{"kind":"auth_jwt"}]`,
		`[{"kind":"api_key"},{"kind":"api_key"}]`,
		`[{"kind":"auth_jwt","function_ids":[12]},{"kind":"api_key"}]`,
		`[{"kind":"auth_jwt"},{"kind":"api_key","required_scopes":null}]`,
		`[{"kind":"auth_jwt","methods":[]},{"kind":"api_key"}]`,
		`[{"kind":"authorizer","provider":"app","app":"auth","tool":"auth_verify_session"},{"kind":"api_key"}]`,
	} {
		if err := validateAuthPolicy(`{"kind":"any_of","methods":` + methods + `}`); err == nil {
			t.Errorf("accepted invalid methods %s", methods)
		}
	}
	if err := validateAuthPolicy(`{"kind":"any_of","claims":["roles"],"methods":[{"kind":"auth_jwt"},{"kind":"api_key"}]}`); err == nil {
		t.Fatal("accepted ambiguous outer verifier settings")
	}
}

func TestAlternativeCredentialsReachSameFunction(t *testing.T) {
	var authCalls, functionCalls atomic.Int32
	platform := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer gateway-outbound" {
			t.Error("callback did not authenticate as Gateway")
		}
		switch r.URL.Path {
		case "/api/apps/callback/apps/identity/proxy/authorize":
			authCalls.Add(1)
			var input struct {
				Project    string            `json:"project_id"`
				Credential map[string]string `json:"credential"`
			}
			if err := json.NewDecoder(r.Body).Decode(&input); err != nil {
				t.Error(err)
			}
			if input.Project != testProject || r.URL.Query().Get("project_id") != testProject {
				t.Error("incorrect verifier project")
			}
			if input.Credential["token"] != "session-token" {
				fmt.Fprint(w, `{"authenticated":false}`)
				return
			}
			fmt.Fprintf(w, `{"authenticated":true,"expires_at":%q,"principal":{"issuer":"identity","subject":"user-42","project_id":%q,"tenant_id":"tenant-a","claims":{"permissions":["records:read"],"private":"hidden"}}}`, time.Now().Add(time.Minute).Format(time.RFC3339), testProject)
		case "/api/apps/callback/apps/functions/call":
			functionCalls.Add(1)
			var call struct {
				Tool  string `json:"tool"`
				Input struct {
					Name      string            `json:"name"`
					Project   string            `json:"_project_id"`
					Principal functionPrincipal `json:"principal"`
				} `json:"input"`
			}
			if err := json.NewDecoder(r.Body).Decode(&call); err != nil {
				t.Error(err)
			}
			if call.Tool != "functions_invoke_authenticated" || call.Input.Name != "read-records" || call.Input.Project != testProject || len(call.Input.Principal.FunctionIDs) != 1 || call.Input.Principal.FunctionIDs[0] != 12 {
				t.Errorf("incorrect Function admission: %+v", call)
			}
			principal, _ := json.Marshal(call.Input.Principal)
			json.NewEncoder(w).Encode(map[string]any{"status": "ok", "response": string(principal)})
		default:
			http.Error(w, "unexpected callback", 500)
		}
	}))
	defer platform.Close()
	t.Setenv("APTEVA_GATEWAY_URL", platform.URL)
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "gateway-outbound")
	t.Setenv("APTEVA_APP_TOKEN", "gateway-internal")
	app, ctx := mountTestApp(t)
	api, err := dbCreateAPI(ctx.AppDB(), apiInput{ProjectID: testProject, Slug: "alternatives", AuthJSON: `{"kind":"any_of","function_ids":[12],"methods":[{"kind":"api_key","required_scopes":["records:read"]},{"kind":"authorizer","provider":"app","app":"identity","path":"/authorize","issuer":"identity","tenant_id":"tenant-a","claims":["permissions"]}]}`})
	if err != nil {
		t.Fatal(err)
	}
	route, _, err := dbUpsertRoute(ctx.AppDB(), routeInput{ProjectID: testProject, APIID: api.ID, Method: "GET", PathPattern: "/records", TargetKind: "function", TargetRef: "read-records", Enabled: true})
	if err != nil {
		t.Fatal(err)
	}
	if err := validateEffectivePolicies(ctx.AppDB(), api, route, 0); err != nil {
		t.Fatal(err)
	}
	key, secret, _, err := dbCreateAPIKey(ctx.AppDB(), apiKeyInput{ProjectID: testProject, APIID: api.ID, Name: "client", ClaimsJSON: `{"tenant_id":"tenant-a"}`, ScopesJSON: `["records:read"]`, MetadataJSON: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	_, unscoped, _, err := dbCreateAPIKey(ctx.AppDB(), apiKeyInput{ProjectID: testProject, APIID: api.ID, Name: "no-scope", ClaimsJSON: `{}`, ScopesJSON: `[]`, MetadataJSON: `{}`})
	if err != nil {
		t.Fatal(err)
	}
	for _, test := range []struct {
		name, authorization, key, query, subject string
		status, verifierCalls                    int
	}{
		{"session", "Bearer session-token", "", "", "user-42", 200, 1},
		{"case-insensitive bearer", "bearer session-token", "", "", "user-42", 200, 1},
		{"key header", "", secret, "", "api_key:" + formatID(key.ID), 200, 0},
		{"key query with platform token", "Bearer gateway-internal", "", "&api_key=" + url.QueryEscape(secret), "api_key:" + formatID(key.ID), 200, 0},
		{"missing", "", "", "", "", 401, 0},
		{"platform token alone", "Bearer gateway-internal", "", "", "", 401, 0},
		{"invalid session", "Bearer invalid", "", "", "", 401, 1},
		{"key in bearer never falls back", "Bearer " + secret, "", "", "", 401, 1},
		{"invalid key", "", "invalid", "", "", 401, 0},
		{"insufficient key scopes", "", unscoped, "", "", 403, 0},
		{"both credentials", "Bearer session-token", secret, "", "", 400, 0},
		{"invalid session and valid key", "Bearer invalid", secret, "", "", 400, 0},
		{"valid session and invalid key", "Bearer session-token", "invalid", "", "", 400, 0},
		{"duplicate key carriers", "", secret, "&api_key=" + url.QueryEscape(secret), "", 400, 0},
		{"duplicate query keys", "", "", "&api_key=one&api_key=two", "", 400, 0},
		{"malformed bearer", "Basic invalid", "", "", "", 401, 0},
		{"empty query key", "", "", "&api_key=", "", 401, 0},
		{"malformed query", "Bearer session-token", "", "&api_key=%ZZ", "", 400, 0},
	} {
		t.Run(test.name, func(t *testing.T) {
			beforeAuth, beforeFunction := authCalls.Load(), functionCalls.Load()
			request := httptest.NewRequest("GET", "/gw/alternatives/records?project_id="+testProject+test.query, nil)
			if test.authorization != "" {
				request.Header.Set("Authorization", test.authorization)
			}
			if test.key != "" {
				request.Header.Set("X-API-Key", test.key)
			}
			request.Header.Set("X-Apteva-App-Token", "spoofed")
			rec := httptest.NewRecorder()
			app.handleGateway(rec, request)
			if rec.Code != test.status || int(authCalls.Load()-beforeAuth) != test.verifierCalls {
				t.Fatalf("status=%d want=%d verifier calls=%d body=%s", rec.Code, test.status, authCalls.Load()-beforeAuth, rec.Body.String())
			}
			expectedDispatches := int32(0)
			if test.status == 200 {
				expectedDispatches = 1
				var principal functionPrincipal
				if err := json.Unmarshal(rec.Body.Bytes(), &principal); err != nil {
					t.Fatal(err)
				}
				if principal.Subject != test.subject || principal.ProjectID != testProject || principal.Claims["tenant_id"] != "tenant-a" || strings.Contains(rec.Body.String(), "hidden") {
					t.Fatalf("incorrect verified principal: %s", rec.Body.String())
				}
			}
			if functionCalls.Load()-beforeFunction != expectedDispatches {
				t.Fatal("request dispatched without successful authentication")
			}
		})
	}
	planResult, err := app.toolUsagePlanCreate(ctx, map[string]any{"api_id": api.ID, "name": "limited", "rate_limit": 1, "interval_seconds": 60, "burst": 1})
	if err != nil {
		t.Fatal(err)
	}
	plan := planResult.(map[string]any)["usage_plan"].(*APIUsagePlan)
	if _, err := app.toolUsagePlanAttachKey(ctx, map[string]any{"key_id": key.ID, "usage_plan_id": plan.ID}); err != nil {
		t.Fatal(err)
	}
	for _, status := range []int{200, 429} {
		request := httptest.NewRequest("GET", "/gw/alternatives/records?project_id="+testProject, nil)
		request.Header.Set("X-API-Key", secret)
		rec := httptest.NewRecorder()
		app.handleGateway(rec, request)
		if rec.Code != status {
			t.Fatalf("API-key usage plan: status=%d want=%d body=%s", rec.Code, status, rec.Body.String())
		}
	}
	if _, err := app.toolKeyRevoke(ctx, map[string]any{"id": key.ID}); err != nil {
		t.Fatal(err)
	}
	request := httptest.NewRequest("GET", "/gw/alternatives/records?project_id="+testProject, nil)
	request.Header.Set("X-API-Key", secret)
	rec := httptest.NewRecorder()
	app.handleGateway(rec, request)
	if rec.Code != 401 {
		t.Fatalf("revoked alternative key: %d %s", rec.Code, rec.Body.String())
	}
}

func TestAlternativeAuthenticationRejectsRepeatedHeaders(t *testing.T) {
	policy, err := parseAuthPolicy(`{"kind":"any_of","methods":[{"kind":"auth_jwt"},{"kind":"api_key"}]}`)
	if err != nil {
		t.Fatal(err)
	}
	for _, header := range []string{"Authorization", "X-API-Key"} {
		request := httptest.NewRequest("GET", "/", nil)
		request.Header.Add(header, "one")
		request.Header.Add(header, "two")
		_, _, err := selectAuthMethod(request, policy)
		failure, ok := err.(*authorizationError)
		if !ok || failure.status != http.StatusBadRequest {
			t.Fatalf("repeated %s was accepted: %v", header, err)
		}
	}
}
