package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func principalPolicy() map[string]any {
	return map[string]any{"authenticated_callers": []any{map[string]any{"installation_id": 42, "issuers": []string{"https://issuer.example"}}}}
}
func principalArgs(fn *Function, ids ...int64) map[string]any {
	if len(ids) == 0 {
		ids = []int64{fn.ID}
	}
	return map[string]any{"id": fn.ID, "_project_id": fn.ProjectID, "event": map[string]any{}, "deadline": time.Now().Add(20 * time.Second).UTC().Format(time.RFC3339Nano), "request_id": "trusted-request-1", "principal": map[string]any{
		"subject": "user-123", "issuer": "https://issuer.example", "project_id": fn.ProjectID, "function_ids": ids, "claims": map[string]any{"roles": []string{"arbitrary-role"}, "tenant": map[string]any{"plan": "custom"}},
	}}
}
func trustedCaller() context.Context {
	return sdk.WithCaller(context.Background(), &sdk.Caller{AppInstallID: 42, AppName: "generic-entry", ProjectID: testProj})
}
func authInvoke(t *testing.T, app *App, ctx *sdk.AppCtx, args map[string]any) map[string]any {
	t.Helper()
	out, err := app.toolInvokeAuthenticated(trustedCaller(), ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	if result["status"] != "ok" {
		t.Fatalf("invoke: %v", result)
	}
	return result
}
func TestTrustedAdmissionRejectsUnverifiedOrOutOfScope(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	fn, err := dbCreateFunction(ctx.AppDB(), testProj, &Function{Name: "guard", Runtime: "node", SourceKind: "inline", Source: echoHandler, SourceHash: "test", InvocationPolicy: &InvocationPolicy{AuthenticatedCallers: []PrincipalCaller{{InstallationID: 42, Issuers: []string{"https://issuer.example"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name   string
		caller *sdk.Caller
		change func(map[string]any)
	}{
		{"missing caller", nil, nil},
		{"agent", &sdk.Caller{AgentID: 42}, nil},
		{"unconfigured installation", &sdk.Caller{AppInstallID: 43, AppName: "generic-entry"}, nil},
		{"same name cannot impersonate installation", &sdk.Caller{AppInstallID: 99, AppName: "generic-entry"}, nil},
		{"wrong caller project", &sdk.Caller{AppInstallID: 42, AppName: "generic-entry", ProjectID: "other"}, nil},
		{"wrong principal project", nil, func(a map[string]any) { a["principal"].(map[string]any)["project_id"] = "other" }},
		{"wrong route project", nil, func(a map[string]any) { a["_project_id"] = "other" }},
		{"wrong issuer", nil, func(a map[string]any) { a["principal"].(map[string]any)["issuer"] = "different" }},
		{"wrong function", nil, func(a map[string]any) { a["principal"].(map[string]any)["function_ids"] = []int64{fn.ID + 1} }},
		{"wildcard scope", nil, func(a map[string]any) { a["principal"].(map[string]any)["function_ids"] = []string{"*"} }},
		{"missing subject", nil, func(a map[string]any) { delete(a["principal"].(map[string]any), "subject") }},
		{"expired deadline", nil, func(a map[string]any) { a["deadline"] = time.Now().Add(-time.Second).Format(time.RFC3339Nano) }},
		{"missing deadline", nil, func(a map[string]any) { delete(a, "deadline") }},
		{"credential claims", nil, func(a map[string]any) {
			a["principal"].(map[string]any)["claims"] = map[string]any{"nested": []any{map[string]any{"refresh_token": "secret"}}}
		}},
		{"unknown principal credential", nil, func(a map[string]any) { a["principal"].(map[string]any)["session_token"] = "secret" }},
		{"password in identity", nil, func(a map[string]any) {
			a["principal"].(map[string]any)["claims"] = map[string]any{"password": "caller-secret"}
		}},
		{"nested caller credential", nil, func(a map[string]any) {
			a["principal"].(map[string]any)["claims"] = map[string]any{"nested": []any{map[string]any{"Authorization": "Bearer caller-secret"}}}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			args := principalArgs(fn)
			parent := trustedCaller()
			if tc.change != nil {
				tc.change(args)
			} else {
				parent = sdk.WithCaller(context.Background(), tc.caller)
			}
			if _, err := app.toolInvokeAuthenticated(parent, ctx, args); !errors.Is(err, errInvocationDenied) {
				t.Fatalf("expected admission denial, got %v", err)
			}
		})
	}
	invs, err := dbListInvocations(ctx.AppDB(), testProj, fn.ID, 50)
	if err != nil || len(invs) != 0 {
		t.Fatalf("rejected requests reached execution: %v %v", invs, err)
	}
}

func TestAuthorizerIsReservedOnEveryPayloadShape(t *testing.T) {
	security := &invocationSecurity{}
	for _, event := range []any{
		map[string]any{"requestContext": map[string]any{"authorizer": map[string]any{"subject": "forged"}, "path": "/ok"}},
		json.RawMessage(`{"requestContext":{"authorizer":{"subject":"forged"},"path":"/ok"}}`),
	} {
		clean, err := trustedEvent(event, security)
		if err != nil {
			t.Fatal(err)
		}
		b, _ := json.Marshal(clean)
		if strings.Contains(string(b), "forged") || !strings.Contains(string(b), "/ok") {
			t.Fatalf("unsanitized event: %s", b)
		}
	}
	for _, event := range []any{nil, 42, []any{"legacy"}, "string", json.RawMessage(`9007199254740993`)} {
		clean, err := trustedEvent(event, security)
		if err != nil {
			t.Fatal(err)
		}
		before, _ := json.Marshal(event)
		after, _ := json.Marshal(clean)
		if string(before) != string(after) {
			t.Fatalf("legacy payload changed: %s -> %s", before, after)
		}
	}
}

func TestTrustedNodePrincipalNestedIntegrityAndAudit(t *testing.T) {
	requireBin(t, "node")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	child := createFn(t, app, ctx, map[string]any{"name": "child", "source": `export default async (e,c)=>({event:e,identity:c.invocation});`, "invocation_policy": principalPolicy()})
	parent := createFn(t, app, ctx, map[string]any{"name": "parent", "source": fmt.Sprintf(`export default async (e,c)=>{
 e.requestContext.authorizer.principal.subject="forged";
 e.requestContext.authorizer.claims.roles=["admin"];
 c.invocation.subject="forged";
 return c.call("functions","functions_invoke",{id:%d,_project_id:"other",principal:{subject:"forged"},deadline:"2099-01-01T00:00:00Z",event:{requestContext:{authorizer:{principal:{subject:"forged"}}}}});
 };`, child.ID), "invocation_policy": principalPolicy(), "access": map[string]any{"apps": []string{"functions.functions_invoke"}, "integrations": []string{}}})
	args := principalArgs(parent, parent.ID, child.ID)
	result := authInvoke(t, app, ctx, args)
	response := result["response"].(string)
	if strings.Contains(response, "forged") || strings.Contains(response, `admin`) || !strings.Contains(response, "user-123") || !strings.Contains(response, "arbitrary-role") {
		t.Fatalf("identity replacement: %s", response)
	}
	children, err := dbListInvocations(ctx.AppDB(), testProj, child.ID, 10)
	if err != nil || len(children) != 1 {
		t.Fatalf("child audit: %v %v", children, err)
	}
	var identity InvocationIdentity
	if err = json.Unmarshal(children[0].Identity, &identity); err != nil {
		t.Fatal(err)
	}
	if identity.Subject != "user-123" || identity.Issuer != "https://issuer.example" || identity.ProjectID != testProj || identity.ParentInvocationID != result["invocation_id"] || identity.RequestID != "trusted-request-1" {
		t.Fatalf("audit: %+v", identity)
	}
	if strings.Contains(children[0].EventJSON, "roles") {
		t.Fatal("opaque claims persisted as event")
	}
	// Scope is fixed at admission, even when caller arguments ask for a target.
	out, err := app.toolInvokeAuthenticated(trustedCaller(), ctx, principalArgs(parent, parent.ID))
	if err == nil && out.(map[string]any)["status"] == "ok" {
		t.Fatal("nested call widened function scope")
	}
	// Target policy is checked separately from root admission.
	if _, err = app.toolUpdate(ctx, map[string]any{"id": child.ID, "invocation_policy": nil}); err != nil {
		t.Fatal(err)
	}
	out, err = app.toolInvokeAuthenticated(trustedCaller(), ctx, args)
	if err == nil && out.(map[string]any)["status"] == "ok" {
		t.Fatal("nested call bypassed target policy")
	}
}

func TestAuthenticatedWorkerRetirementAndLegacyReuse(t *testing.T) {
	requireBin(t, "node")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	fn := createFn(t, app, ctx, map[string]any{"name": "reuse", "source": `let saved=null;let count=0;export default async(e,c)=>{let previous=saved;saved=e.requestContext?.authorizer;count++;if(e.fail)throw Error("failed");if(e.wait)await new Promise(r=>setTimeout(r,10000));return {previous,count,identity:c.invocation,authorizer:saved};};`, "invocation_policy": principalPolicy()})
	for _, mode := range []string{"ok", "fail", "cancel"} {
		args := principalArgs(fn)
		if mode == "fail" {
			args["event"] = map[string]any{"fail": true}
		}
		if mode == "cancel" {
			args["event"] = map[string]any{"wait": true}
			args["deadline"] = time.Now().Add(150 * time.Millisecond).Format(time.RFC3339Nano)
		}
		_, _ = app.toolInvokeAuthenticated(trustedCaller(), ctx, args)
		res, err := invokeFunction(ctx, context.Background(), fn, map[string]any{"requestContext": map[string]any{"authorizer": map[string]any{"subject": "spoof"}}}, "manual")
		if err != nil || res.Status != "ok" {
			t.Fatalf("legacy invoke: %v %v", res, err)
		}
		if strings.Contains(res.Response, "user-123") || strings.Contains(res.Response, "spoof") || !strings.Contains(res.Response, `"count":1`) {
			t.Fatalf("identity survived %s: %s", mode, res.Response)
		}
	}
	res, err := invokeFunction(ctx, context.Background(), fn, map[string]any{}, "manual")
	if err != nil || !strings.Contains(res.Response, `"count":2`) {
		t.Fatalf("legacy worker was not reused: %v %v", res, err)
	}
}

func TestRequireAuthenticatedPreservesOptInAndBlocksHTTP(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	fn, err := dbCreateFunction(ctx.AppDB(), testProj, &Function{Name: "private", Runtime: "node", SourceKind: "inline", Source: echoHandler, SourceHash: "test", InvocationPolicy: &InvocationPolicy{RequireAuthenticated: true}})
	if err != nil {
		t.Fatal(err)
	}
	for _, trigger := range []string{"manual", "http", "function_url", "nested"} {
		if _, err := invokeFunction(ctx, context.Background(), fn, map[string]any{}, trigger); !errors.Is(err, errInvocationDenied) {
			t.Fatalf("%s allowed: %v", trigger, err)
		}
	}
	req := httptest.NewRequest("POST", "/fn/private", strings.NewReader(`{"principal":{"subject":"fake"}}`))
	w := httptest.NewRecorder()
	app.handleHTTPInvokeByName(w, req)
	if w.Code != 403 {
		t.Fatalf("status=%d body=%s", w.Code, w.Body.String())
	}
	if _, err := app.toolUpdate(ctx, map[string]any{"id": fn.ID, "invocation_policy": nil}); err != nil {
		t.Fatal(err)
	}
	updated, err := dbGetFunction(ctx.AppDB(), testProj, fn.ID, "")
	if err != nil || updated.InvocationPolicy != nil {
		t.Fatalf("could not restore legacy admission: %v %v", updated, err)
	}
}

func TestOtherInvocationIdentities(t *testing.T) {
	fn := &Function{ID: 1, ProjectID: testProj}
	for _, tc := range []struct {
		name                   string
		caller                 *sdk.Caller
		trigger, kind, subject string
	}{
		{"anonymous", nil, "http", "anonymous", ""},
		{"schedule", &sdk.Caller{AppInstallID: 7, AppName: "jobs"}, "http", "service", "7"},
		{"service", &sdk.Caller{AppInstallID: 8, AppName: "custom-service"}, "manual", "service", "8"},
		{"agent", &sdk.Caller{AgentID: 9}, "manual", "agent", "9"},
		{"url", nil, "function_url", "function_url", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, s, err := invocationAdmission(sdk.WithCaller(context.Background(), tc.caller), fn, tc.trigger)
			if err != nil || s.Identity.Kind != tc.kind || s.Identity.Subject != tc.subject || s.Principal != nil || s.Identity.RequestID == "" {
				t.Fatalf("identity: %+v %v", s, err)
			}
		})
	}
}

func TestTrustedGoPrincipal(t *testing.T) {
	requireBin(t, "go")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	fn := createFn(t, app, ctx, map[string]any{"name": "go-principal", "runtime": "go", "source": `package main
import "encoding/json"
func Handle(event json.RawMessage,c *Context)(any,error){return map[string]any{"event":event,"identity":c.Invocation},nil}`, "invocation_policy": principalPolicy()})
	out := authInvoke(t, app, ctx, principalArgs(fn))
	if !strings.Contains(out["response"].(string), "user-123") {
		t.Fatal(out)
	}
}

func TestInvocationPolicyValidation(t *testing.T) {
	for _, raw := range []any{
		"invalid", []any{}, map[string]any{"typo": true},
		map[string]any{"authenticated_callers": []any{map[string]any{"installation_id": 42, "issuers": []string{}}}},
		map[string]any{"authenticated_callers": []any{map[string]any{"installation_id": -1, "issuers": []string{"issuer"}}}},
		map[string]any{"authenticated_callers": []any{map[string]any{"installation_id": 42, "issuers": []string{"issuer"}}, map[string]any{"installation_id": 42, "issuers": []string{"issuer"}}}},
	} {
		if _, err := parseInvocationPolicy(raw); err == nil {
			t.Fatalf("invalid policy accepted: %v", raw)
		}
	}
}

func TestTrustedNestedAccessAndOriginalDeadline(t *testing.T) {
	requireBin(t, "node")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	child := createFn(t, app, ctx, map[string]any{"name": "bounded-child", "source": `export default async(e)=>{await new Promise(r=>setTimeout(r,500));return "too late";};`, "invocation_policy": principalPolicy()})
	root := createFn(t, app, ctx, map[string]any{"name": "bounded-root", "source": fmt.Sprintf(`export default async(e,c)=>c.call("functions","functions_invoke",{id:%d,deadline:"2099-01-01T00:00:00Z",event:{}});`, child.ID), "invocation_policy": principalPolicy(), "access": map[string]any{"apps": []string{}, "integrations": []string{}}})
	out, err := app.toolInvokeAuthenticated(trustedCaller(), ctx, principalArgs(root, root.ID, child.ID))
	if err == nil && out.(map[string]any)["status"] == "ok" {
		t.Fatal("outbound denial ignored")
	}
	children, err := dbListInvocations(ctx.AppDB(), testProj, child.ID, 10)
	if err != nil || len(children) != 0 {
		t.Fatalf("denied nested call ran: %v %v", children, err)
	}
	if _, err = app.toolUpdate(ctx, map[string]any{"id": root.ID, "access": map[string]any{"apps": []string{"functions.functions_invoke"}, "integrations": []string{}}}); err != nil {
		t.Fatal(err)
	}
	args := principalArgs(root, root.ID, child.ID)
	args["deadline"] = time.Now().Add(200 * time.Millisecond).Format(time.RFC3339Nano)
	started := time.Now()
	out, err = app.toolInvokeAuthenticated(trustedCaller(), ctx, args)
	if time.Since(started) > 2*time.Second {
		t.Fatal("nested deadline was extended")
	}
	if err == nil && out.(map[string]any)["status"] == "ok" {
		t.Fatal("deadline replacement succeeded")
	}
}

func TestAuthenticatedEntryExposureOverMCP(t *testing.T) {
	requireBin(t, "node")
	sc := tk.SpawnSidecar(t, ".", tk.WithProjectID(testProj))
	listed, err := sc.MCPRaw("tools/list", map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(listed)
	if strings.Contains(string(b), "functions_invoke_authenticated") {
		t.Fatal("private entry exposed to ordinary agents")
	}
	var created struct {
		Function Function `json:"function"`
	}
	response := sc.POST("/functions", map[string]any{"name": "mcp-guard", "runtime": "node", "source": echoHandler, "invocation_policy": principalPolicy()}, &created)
	if response.Status >= 300 {
		t.Fatalf("create failed: %+v", response)
	}
	rpc := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "functions_invoke_authenticated", "arguments": principalArgs(&created.Function)}}
	for _, bound := range []bool{false, true} {
		headers := map[string]string{}
		if bound {
			headers[sdk.HeaderBoundCallerInstallID] = "42"
			headers[sdk.HeaderBoundCallerAppName] = "generic-entry"
		}
		var result map[string]any
		sc.RequestWithHeaders("POST", "/mcp", rpc, &result, headers)
		b, _ := json.Marshal(result)
		success := strings.Contains(string(b), "user-123")
		if success != bound {
			t.Fatalf("bound=%v response=%s", bound, b)
		}
	}
}

func TestTrustedBusinessPayloadIsOpaque(t *testing.T) {
	business := map[string]any{
		"password": "new-user-password",
		"body":     map[string]any{"users": []any{map[string]any{"password": "nested-password", "credentials": map[string]any{"token": "business-token"}, "role": "custom-role", "centre": "centre-123", "crm": map[string]any{"rule": "application-owned"}}}},
	}
	for _, authenticated := range []bool{false, true} {
		t.Run(fmt.Sprint("authenticated=", authenticated), func(t *testing.T) {
			s := &invocationSecurity{}
			if authenticated {
				s.Principal = &Principal{Subject: "verified-subject", Claims: map[string]any{"roles": []string{"opaque-role"}, "centres": []string{"opaque-centre"}}}
			}
			clean, err := trustedEvent(business, s)
			if err != nil {
				t.Fatal(err)
			}
			obj := clean.(map[string]any)
			if authenticated {
				rc := obj["requestContext"].(map[string]any)
				authorizer, _ := json.Marshal(rc["authorizer"])
				if strings.Contains(string(authorizer), "password") || !strings.Contains(string(authorizer), "verified-subject") {
					t.Fatalf("business data entered identity: %s", authorizer)
				}
				delete(obj, "requestContext")
			}
			before, _ := json.Marshal(business)
			after, _ := json.Marshal(obj)
			if string(before) != string(after) {
				t.Fatalf("business payload changed: %s -> %s", before, after)
			}
		})
	}
}

func TestBusinessPasswordThroughNestedExecutionIsNotStoredAsInput(t *testing.T) {
	for _, runtime := range []string{"node", "go"} {
		t.Run(runtime, func(t *testing.T) {
			requireBin(t, runtime)
			ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
			app := mountApp(t, ctx)
			source := `export default async(e)=>{
    if(e.body.user.password!=="business-only-secret" || e.body.user.role!=="new-custom-role" || e.body.user.centre!=="centre-123")throw Error("business input changed");
    if(e.fail)throw Error("business handler failure");
    return {accepted:true,subject:e.requestContext.authorizer.principal.subject};
   };`
			if runtime == "go" {
				source = `package main
import("encoding/json";"fmt")
func Handle(event json.RawMessage,c *Context)(any,error){
 var e struct{Body struct{User struct{Password,Role,Centre string}};Fail bool;RequestContext struct{Authorizer struct{Principal struct{Subject string}}}}
 if err:=json.Unmarshal(event,&e);err!=nil{return nil,err}
 if e.Body.User.Password!="business-only-secret"||e.Body.User.Role!="new-custom-role"||e.Body.User.Centre!="centre-123"{return nil,fmt.Errorf("business input changed")}
 if e.Fail{return nil,fmt.Errorf("business handler failure")}
 return map[string]any{"accepted":true,"subject":e.RequestContext.Authorizer.Principal.Subject},nil
}`
			}
			child := createFn(t, app, ctx, map[string]any{"name": "business-child", "runtime": runtime, "source": source, "invocation_policy": principalPolicy()})
			requireBin(t, "node")
			root := createFn(t, app, ctx, map[string]any{"name": "business-root", "source": fmt.Sprintf(`export default async(e,c)=>c.call("functions","functions_invoke",{id:%d,event:e});`, child.ID), "invocation_policy": principalPolicy()})
			for _, fail := range []bool{false, true} {
				args := principalArgs(root, root.ID, child.ID)
				args["event"] = map[string]any{"body": map[string]any{"user": map[string]any{"password": "business-only-secret", "role": "new-custom-role", "centre": "centre-123"}}, "fail": fail}
				out, err := app.toolInvokeAuthenticated(trustedCaller(), ctx, args)
				if err != nil {
					t.Fatal(err)
				}
				result := out.(map[string]any)
				if !fail && (result["status"] != "ok" || !strings.Contains(result["response"].(string), "user-123")) {
					t.Fatalf("business input rejected: %v", result)
				}
				if fail && (result["status"] != "error" || !strings.Contains(fmt.Sprint(result["error"]), "business handler failure")) {
					t.Fatalf("failure path: %v", result)
				}
			}
			for _, fn := range []*Function{root, child} {
				invs, err := dbListInvocations(ctx.AppDB(), testProj, fn.ID, 10)
				if err != nil || len(invs) != 2 {
					t.Fatalf("history: %v %v", invs, err)
				}
				for _, inv := range invs {
					if inv.EventJSON != "[authenticated event omitted]" {
						t.Fatalf("business body stored: %s", inv.EventJSON)
					}
					detail, err := dbGetInvocation(ctx.AppDB(), testProj, inv.ID)
					if err != nil {
						t.Fatal(err)
					}
					logs, err := app.toolLogs(ctx, map[string]any{"invocation_id": inv.ID})
					if err != nil {
						t.Fatal(err)
					}
					b, _ := json.Marshal([]any{inv, detail, logs})
					if strings.Contains(string(b), "business-only-secret") || strings.Contains(string(b), "new-custom-role") || strings.Contains(string(b), "centre-123") {
						t.Fatalf("business body leaked into history: %s", b)
					}
					var identity InvocationIdentity
					if err := json.Unmarshal(inv.Identity, &identity); err != nil {
						t.Fatal(err)
					}
					if identity.Subject != "user-123" || identity.CallerInstallationID != 42 || (fn.ID == child.ID && identity.ParentInvocationID == 0) {
						t.Fatalf("lost trusted identity: %+v", identity)
					}
				}
			}
		})
	}
}
