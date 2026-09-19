package main

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type trustedPlatform struct {
	sdk.PlatformClient
	sdk.AppContextClient
	calls    atomic.Int64
	input    map[string]any
	response string
	err      error
}

func (p *trustedPlatform) CallAppResultContext(ctx context.Context, app, tool string, input map[string]any, out any) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	return p.CallAppResult(app, tool, input, out)
}

func (p *trustedPlatform) CallAppResult(app, tool string, input map[string]any, out any) error {
	p.calls.Add(1)
	if app != "functions" || tool != "functions_invoke_authenticated" {
		return fmt.Errorf("unexpected legacy dispatch")
	}
	p.input = input
	if p.err != nil {
		return p.err
	}
	b, _ := json.Marshal(map[string]any{"status": "ok", "response": p.response})
	return json.Unmarshal(b, out)
}
func secureTestApp(t *testing.T, p sdk.PlatformClient) *App {
	t.Helper()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"), tk.WithPlatform(p))
	a := &App{}
	if err := a.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { a.OnUnmount(ctx) })
	return a
}
func testSecurityPolicy() map[string]any {
	return map[string]any{"mode": "auth", "tenant_id": "default", "environment": "production", "claims": []string{"roles", "permissions", "authorization_version"}}
}
func testIdentityContext(t *testing.T) context.Context {
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*10)
	t.Cleanup(cancel)
	return context.WithValue(ctx, identityKey{}, &requestIdentity{Subject: "42", Issuer: "apteva:auth:default", Project: "p1", API: "default", Tenant: "default", Claims: map[string]any{"roles": []any{"reader"}}, Permissions: []string{"workspace:read"}, Expires: time.Now().Add(time.Minute), RequestID: "test-request"})
}
func trustedConfig() map[string]any {
	return map[string]any{"_project_id": "p1", "authenticated": true, "function_id": 95, "function_ids": []int{95, 96}, "contract": "http"}
}

func TestSecurityConfigurationRejectsUnsafePolicies(t *testing.T) {
	for _, raw := range []string{`{}`, `{"mode":"public"}`, `{"mode":"auth","tenant_id":"default"}`, `{"mode":"platform","claims":["roles"]}`, `{"mode":"auth","tenant_id":"default","environment":"production","claims":["session_token"]}`, `{"mode":"auth","tenant_id":"default","environment":"production","claims":["principal"]}`, `{"mode":"auth","tenant_id":"default","environment":"production","allow_all":true}`, `{"mode":"auth","tenant_id":"default","environment":"production","fields":{"bad key":["read"]}}`} {
		t.Run(raw, func(t *testing.T) {
			if _, err := parseSecurity(json.RawMessage(raw)); err == nil {
				t.Fatal("unsafe policy accepted")
			}
		})
	}
	for _, raw := range []string{`{"authenticated":true}`, `{"authenticated":true,"function_id":95,"function_ids":[96]}`, `{"authenticated":true,"function_id":95,"function_ids":[95,95]}`, `{"authenticated":"true"}`, `{"authenticated":true,"function_id":95,"function_ids":[95,-1]}`, `{"contract":"custom"}`} {
		var config map[string]any
		json.Unmarshal([]byte(raw), &config)
		if _, err := functionSecurity(config); err == nil {
			t.Fatalf("unsafe Function config accepted: %s", raw)
		}
	}
}

func TestTrustedFunctionIdentityAndHTTPMapping(t *testing.T) {
	p := &trustedPlatform{response: `{"statusCode":200,"headers":{"Set-Cookie":"secret"},"body":"{\"ok\":true}"}`}
	a := secureTestApp(t, p)
	args := map[string]any{"commercial_id": "7", "principal": map[string]any{"subject": "attacker", "function_ids": []int{999}}, "deadline": "2099-01-01", "function_id": 999}
	result, err := a.callFunction(testIdentityContext(t), trustedConfig(), args)
	if err != nil || result.(map[string]any)["ok"] != true {
		t.Fatalf("result=%v err=%v", result, err)
	}
	principal := p.input["principal"].(map[string]any)
	if principal["subject"] != "42" || principal["issuer"] != "apteva:auth:default" || principal["project_id"] != "p1" || p.input["id"] != int64(95) {
		t.Fatalf("identity/scope overridden: %v", p.input)
	}
	ids := principal["function_ids"].([]int64)
	if len(ids) != 2 || ids[0] != 95 || ids[1] != 96 {
		t.Fatalf("scope=%v", ids)
	}
	event := p.input["event"].(map[string]any)
	if event["requestContext"] != nil || event["principal"] != nil || event["headers"] != nil || event["body"].(map[string]any)["commercial_id"] != "7" {
		t.Fatalf("event=%v", event)
	}
	if principal["claims"].(map[string]any)["tenant_id"] != "default" {
		t.Fatal("tenant lost")
	}
	deadline, _ := time.Parse(time.RFC3339Nano, p.input["deadline"].(string))
	if time.Until(deadline) > 10*time.Second {
		t.Fatal("deadline extended")
	}
}

func TestTrustedFunctionFailsClosedAndSanitizesErrors(t *testing.T) {
	p := &trustedPlatform{response: `{"statusCode":200,"body":{}}`}
	a := secureTestApp(t, p)
	if _, err := a.callFunction(context.Background(), trustedConfig(), nil); errorCode(err) != "unauthenticated" || p.calls.Load() != 0 {
		t.Fatal("unauthenticated invocation dispatched")
	}
	cfg := trustedConfig()
	cfg["_project_id"] = "victim"
	if _, err := a.callFunction(testIdentityContext(t), cfg, nil); err == nil || p.calls.Load() != 0 {
		t.Fatal("cross-project dispatched")
	}
	p.err = fmt.Errorf("secret upstream token")
	if _, err := a.callFunction(testIdentityContext(t), trustedConfig(), nil); errorCode(err) != "permission_denied" || strings.Contains(err.Error(), "secret") {
		t.Fatalf("err=%v", err)
	}
	if p.calls.Load() != 1 {
		t.Fatal("fallback dispatched")
	}
	p.err = nil
	for _, status := range []int{401, 403, 500} {
		p.response = fmt.Sprintf(`{"statusCode":%d,"body":{"secret":"private"}}`, status)
		if _, err := a.callFunction(testIdentityContext(t), trustedConfig(), nil); err == nil || strings.Contains(err.Error(), "private") {
			t.Fatal("Function error became data")
		}
	}
	cfg = trustedConfig()
	cfg["contract"] = "graphql"
	p.response = `{"answer":42}`
	out, err := a.callFunction(testIdentityContext(t), cfg, map[string]any{"question": "hello"})
	if err != nil || out.(map[string]any)["answer"] != float64(42) {
		t.Fatal("native result", out, err)
	}
	if p.input["event"].(map[string]any)["arguments"] == nil {
		t.Fatal("native arguments missing")
	}
}

func TestAuthVerificationUsesManagedClaimsOnly(t *testing.T) {
	var code atomic.Int64
	code.Store(200)
	auth := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/apps/auth/me" || r.URL.Query().Get("project_id") != "p1" {
			t.Error("wrong Auth scope")
		}
		w.WriteHeader(int(code.Load()))
		fmt.Fprint(w, `{"org":"default","user":{"id":42,"metadata":{"roles":["admin"],"session_token":"secret"}},"authorization":{"roles":["reader"],"permissions":["workspace:read"],"authorization_version":3}}`)
	}))
	defer auth.Close()
	t.Setenv("APTEVA_GATEWAY_URL", auth.URL)
	a := secureTestApp(t, &trustedPlatform{})
	policy, _ := parseSecurity(testSecurityPolicy())
	token := "e30." + base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf(`{"exp":%d}`, time.Now().Add(time.Minute).Unix()))) + ".signature"
	r := httptest.NewRequest("POST", "/", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	i, err := a.authenticateGraphQL(r, "p1", "default", policy)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(i)
	if strings.Contains(string(raw), "admin") || strings.Contains(string(raw), "secret") || i.Subject != "42" || i.Issuer != "apteva:auth:default" {
		t.Fatalf("unsafe identity: %s", raw)
	}
	if !strings.Contains(string(raw), "reader") {
		t.Fatal("managed claims lost")
	}
	policy.TenantID = "other"
	if _, err := a.authenticateGraphQL(r, "p1", "default", policy); errorCode(err) != "permission_denied" {
		t.Fatal("wrong tenant admitted")
	}
	policy.TenantID = "default"
	code.Store(401)
	if _, err := a.authenticateGraphQL(r, "p1", "default", policy); errorCode(err) != "unauthenticated" {
		t.Fatal("revoked token admitted")
	}
	code.Store(200)
	r.Header.Set("Authorization", "Bearer e30."+base64.RawURLEncoding.EncodeToString([]byte(`{"exp":1}`))+".signature")
	if _, err := a.authenticateGraphQL(r, "p1", "default", policy); err == nil {
		t.Fatal("expired token admitted")
	}
}

func TestSecurityScopesAndPreflightCoverNestedFields(t *testing.T) {
	p, _ := parseSecurity(testSecurityPolicy())
	ctx := testIdentityContext(t)
	if err := authorizeIdentity(ctx, "p1", "default", p, "production"); err != nil {
		t.Fatal(err)
	}
	for _, scope := range [][3]string{{"other", "default", "production"}, {"p1", "other", "production"}, {"p1", "default", "development"}} {
		if authorizeIdentity(ctx, scope[0], scope[1], p, scope[2]) == nil {
			t.Fatal("wrong scope admitted", scope)
		}
	}
	schema, _ := validateSDL(`type Query { rows: [Row!]! } type Row { id: ID! secret: String }`)
	p.Fields = map[string][]string{"Row.secret": {"sensitive:read"}}
	for _, query := range []string{`{ rows { alias: secret } }`, `{ rows { ...Bits } } fragment Bits on Row { secret }`, `{ rows { ... on Row { secret } } }`} {
		doc, errs := parseAndValidateQuery(schema, query)
		if len(errs) > 0 {
			t.Fatal(errs)
		}
		if err := authorizeSelection(ctx, p, doc.Operations[0].SelectionSet); errorCode(err) != "permission_denied" {
			t.Fatalf("preflight bypass: %s %v", query, err)
		}
	}
}

func TestSecurityIsolationAndNoInternalOrRealtimeBypass(t *testing.T) {
	p := &trustedPlatform{}
	a := secureTestApp(t, p)
	if _, err := setSecurity(a.ctx.AppDB(), "p1", "default", testSecurityPolicy()); err != nil {
		t.Fatal(err)
	}
	other, err := getSecurity(a.ctx.AppReadDB(), "other", "default")
	if err != nil || other.Mode != "platform" {
		t.Fatal("policy leaked across projects")
	}
	other, err = getSecurity(a.ctx.AppReadDB(), "p1", "other")
	if err != nil || other.Mode != "platform" {
		t.Fatal("policy leaked across APIs")
	}
	if _, err := a.execute(context.Background(), "p1", "default", "production", graphqlRequest{Query: "{test}"}); errorCode(err) != "unauthenticated" {
		t.Fatal("internal execution bypass", err)
	}
	w := httptest.NewRecorder()
	a.handleRealtime(w, httptest.NewRequest("GET", "/realtime", nil))
	if w.Code != 403 {
		t.Fatal("protected realtime accepted")
	}
	w = httptest.NewRecorder()
	a.handlePublicGraphQL(w, httptest.NewRequest("POST", "/public/graphql/default", strings.NewReader(`{"query":"{test}"}`)))
	if w.Code != 401 {
		t.Fatal("public missing token accepted", w.Code)
	}
	if p.calls.Load() != 0 {
		t.Fatal("denied requests dispatched")
	}
}

func TestAuthenticatedOperationPreflightBeforeMutation(t *testing.T) {
	p := &trustedPlatform{response: `{"answer":42}`}
	a := secureTestApp(t, p)
	policy := testSecurityPolicy()
	policy["fields"] = map[string][]string{"Mutation.denied": {"admin"}}
	if _, err := setSecurity(a.ctx.AppDB(), "p1", "default", policy); err != nil {
		t.Fatal(err)
	}
	schema, _, err := createSchemaForAPI(a.ctx.AppDB(), "p1", "default", "production", `type Query { value: Int } type Mutation { allowed: Int denied: Int }`, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = publishSchemaForAPI(a.ctx.AppDB(), "p1", "default", "production", schema.Version); err != nil {
		t.Fatal(err)
	}
	result, err := a.execute(testIdentityContext(t), "p1", "default", "production", graphqlRequest{Query: `mutation { allowed denied }`})
	if errorCode(err) != "permission_denied" || p.calls.Load() != 0 || result.Data != nil {
		t.Fatal("mutation preflight bypass", result, err)
	}
}
