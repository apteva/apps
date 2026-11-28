package main

import (
	"context"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func TestResponseStorageMatrix(t *testing.T) {
	body := `{"user":"u1","access_token":"abc.def.ghi-123456"}`
	cases := []struct {
		mode          string
		authenticated bool
		ok            bool
		days          int
		stored        bool
		omitted       string
	}{
		{storeResponsesAuto, false, true, 7, true, ""},
		{storeResponsesAuto, true, true, 7, false, "authenticated"},
		{storeResponsesAuto, true, false, 7, false, "authenticated"},
		{storeResponsesNever, false, false, 7, false, "policy_never"},
		{storeResponsesErrorsOnly, true, true, 7, false, "errors_only"},
		{storeResponsesErrorsOnly, true, false, 7, true, ""},
		{storeResponsesAlways, true, true, 7, true, ""},
		{storeResponsesAlways, false, true, 0, false, "success_details_disabled"},
		{storeResponsesAlways, false, false, 0, true, ""},
	}
	for _, c := range cases {
		got, omitted := responseStorage(c.mode, c.authenticated, c.ok, c.days, body)
		if (got != "") != c.stored || omitted != c.omitted {
			t.Fatalf("%+v: stored=%q omitted=%q", c, got, omitted)
		}
		redacted := c.authenticated || c.mode == storeResponsesErrorsOnly
		if c.stored && redacted && strings.Contains(got, "abc.def") {
			t.Fatalf("%+v: token kept: %s", c, got)
		}
		if c.stored && !redacted && got != body {
			t.Fatalf("%+v: unauthenticated body changed: %s", c, got)
		}
	}
	if got, omitted := responseStorage(storeResponsesAuto, true, true, 7, ""); got != "" || omitted != "" {
		t.Fatal("empty body reported as omitted")
	}
	if validateStoreResponses("sometimes") == nil || validateStoreResponses(storeResponsesErrorsOnly) != nil {
		t.Fatal("store_responses validation")
	}
}

func TestRedactTokens(t *testing.T) {
	secrets := []string{
		`{"token":"opaque-value-1"}`,
		`{"api_key": "opaque-value-2"}`,
		`{"nested":{"client_secret":"opaque-value-3"}}`,
		`refresh_token=opaque-value-4&x=1`,
		`Authorization: Bearer opaque-value-5abcdef`,
		`key sk-ant-abcdefghijklmnopqrstu in text`,
		`stripe sk_live_abcdefghijklmn`,
		`gh ghp_abcdefghijklmnopqrstuvwxyz0123`,
		`jwt eyJhbGciOiJIUzI1NiJ9.eyJzdWIiOiIxMjM0NTY3ODkwIn0.dozjgNryP4J3jVmNHl0w5N_XgL0n3I9PlFUP0THsR8U`,
		`aws AKIAABCDEFGHIJKLMNOP`,
		`xoxb-1234567890-abcdefghij`,
	}
	for _, s := range secrets {
		out := redactTokens(s)
		if !strings.Contains(out, "[redacted]") {
			t.Fatalf("not redacted: %s -> %s", s, out)
		}
		for _, v := range []string{"opaque-value", "abcdefghijklmnopqrstu", "abcdefghijklmn", "dozjgNryP4J3", "AKIAABCDEFGH", "xoxb-1234"} {
			if strings.Contains(out, v) {
				t.Fatalf("%s survived: %s", v, out)
			}
		}
	}
	// Ordinary data and JSON shape survive.
	keep := `{"user":"u1","count":3,"items":[{"name":"a"}],"token":{"type":"x"}}`
	if out := redactTokens(keep); !strings.Contains(out, `"user":"u1"`) || !strings.Contains(out, `"items":[{"name":"a"}]`) || !strings.Contains(out, `"token":{"type":"x"}`) {
		t.Fatalf("ordinary data changed: %s", out)
	}
}

func TestAuthenticatedResponseNotStored(t *testing.T) {
	requireBin(t, "node")
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	fn := createFn(t, app, ctx, map[string]any{"name": "auth-response", "source": `export default async (e,c)=>({result:"returned-value", access_token:"tok-123456789"});`, "invocation_policy": principalPolicy()})
	if fn.StoreResponses != storeResponsesAuto {
		t.Fatalf("default store_responses = %q", fn.StoreResponses)
	}
	result := authInvoke(t, app, ctx, principalArgs(fn))
	if !strings.Contains(result["response"].(string), "returned-value") {
		t.Fatalf("caller response changed: %v", result)
	}
	inv, err := dbGetInvocation(ctx.AppDB(), testProj, toInt64(result["invocation_id"]))
	if err != nil || inv == nil {
		t.Fatal(err)
	}
	if inv.ResponseBody != "" || inv.ResponseOmitted != "authenticated" || inv.ResponseBytes == 0 || inv.Status != "ok" || inv.DurationMS < 0 {
		t.Fatalf("authenticated record: %+v", inv)
	}

	// always: stored, with the token stripped.
	if _, err = app.toolUpdate(ctx, map[string]any{"id": fn.ID, "store_responses": storeResponsesAlways}); err != nil {
		t.Fatal(err)
	}
	result = authInvoke(t, app, ctx, principalArgs(fn))
	inv, _ = dbGetInvocation(ctx.AppDB(), testProj, toInt64(result["invocation_id"]))
	if !strings.Contains(inv.ResponseBody, "returned-value") || strings.Contains(inv.ResponseBody, "tok-123456789") || inv.ResponseOmitted != "" {
		t.Fatalf("always record: %+v", inv)
	}

	// Unauthenticated calls keep their body under auto.
	plain := createFn(t, app, ctx, map[string]any{"name": "plain-response", "source": echoHandler})
	res, err := invokeFunction(ctx, context.Background(), plain, map[string]any{"v": "kept"}, "manual")
	if err != nil {
		t.Fatal(err)
	}
	inv, _ = dbGetInvocation(ctx.AppDB(), testProj, res.InvocationID)
	if !strings.Contains(inv.ResponseBody, "kept") || inv.ResponseBytes != int64(len(res.Response)) {
		t.Fatalf("unauthenticated record: %+v", inv)
	}
	if _, err = app.toolUpdate(ctx, map[string]any{"id": plain.ID, "store_responses": "sometimes"}); err == nil {
		t.Fatal("invalid store_responses accepted")
	}
}

func TestZeroDaySuccessDetails(t *testing.T) {
	requireBin(t, "node")
	t.Setenv("APTEVA_FUNCTIONS_SUCCESS_DETAILS_DAYS", "0")
	if invocationStoragePolicy().SuccessDays != 0 {
		t.Fatal("0 rejected")
	}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	app := mountApp(t, ctx)
	ok := createFn(t, app, ctx, map[string]any{"name": "zero-ok", "source": `export default async (e,c)=>{c.log("hello");return e}`})
	res, err := invokeFunction(ctx, context.Background(), ok, map[string]any{"v": "x"}, "manual")
	if err != nil || res.Status != "ok" {
		t.Fatalf("%v %+v", err, res)
	}
	inv, _ := dbGetInvocation(ctx.AppDB(), testProj, res.InvocationID)
	if !inv.DetailsExpired || inv.EventJSON != "" || inv.ResponseBody != "" || inv.Stderr != "" || inv.ResponseBytes == 0 || inv.Status != "ok" {
		t.Fatalf("successful call kept details: %+v", inv)
	}
	if string(inv.Identity) != "null" {
		t.Fatalf("identity kept: %s", inv.Identity)
	}
	bad := createFn(t, app, ctx, map[string]any{"name": "zero-bad", "source": `export default async ()=>{throw new Error("boom")}`})
	res, _ = invokeFunction(ctx, context.Background(), bad, map[string]any{}, "manual")
	inv, _ = dbGetInvocation(ctx.AppDB(), testProj, res.InvocationID)
	if inv.DetailsExpired || !strings.Contains(inv.Error, "boom") {
		t.Fatalf("failed call lost details: %+v", inv)
	}
	if got := retentionCutoff(time.Date(2026, 11, 28, 10, 0, 0, 0, time.UTC), 0); got != "2026-11-28T10:00:00Z" {
		t.Fatalf("zero-day cutoff %q", got)
	}
}

func TestLogsPermissionDeclared(t *testing.T) {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		t.Fatal(err)
	}
	declared := false
	for _, p := range m.Provides.ProvidedPermissions {
		declared = declared || p.Name == "logs.read"
	}
	if !declared {
		t.Fatal("logs.read not declared")
	}
	for _, tool := range m.Provides.MCPTools {
		switch tool.Name {
		case "functions_logs":
			if tool.Requires != "logs.read" {
				t.Fatalf("functions_logs requires %q", tool.Requires)
			}
		case "functions_invocations", "functions_slow_invocations", "functions_performance":
			if tool.Requires != "" {
				t.Fatalf("%s should stay summary-level, requires %q", tool.Name, tool.Requires)
			}
		}
	}
	// The SDK enforces requires against the caller's grants.
	deny := &sdk.Caller{AgentID: 7, Grants: []sdk.Grant{{Effect: "deny", Permission: "logs.read"}}}
	if deny.Allows("logs.read", "") {
		t.Fatal("deny grant ignored")
	}
}

func TestInvocationSummariesOmitIdentity(t *testing.T) {
	page := invocationPage([]*Invocation{{ID: 1, Identity: StoredResources(`{"subject":"user-1"}`), ResponseBody: "x", ResponseBytes: 1}}, 50)
	row := page["invocations"].([]*Invocation)[0]
	if row.Identity != nil || row.ResponseBody != "" || row.ResponseBytes != 1 {
		t.Fatalf("summary row: %+v", row)
	}
}

func TestStorageCleanupFailingAlert(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	p := &pool{ctx: ctx}
	policy := invocationStoragePolicy()
	p.recordStorageRun(storageWork{}, context.DeadlineExceeded, policy)
	if _, ok := p.storageAlerts["cleanup_failing"]; ok {
		t.Fatal("alert raised before the grace period")
	}
	p.mu.Lock()
	p.storageLastComplete = time.Now().Add(-storageFailingAfter - time.Second)
	p.lastStorageReport = time.Now()
	p.mu.Unlock()
	p.recordStorageRun(storageWork{BudgetExhausted: stepSummaries}, context.DeadlineExceeded, policy)
	if _, ok := p.storageAlerts["cleanup_failing"]; !ok {
		t.Fatal("cleanup_failing not raised")
	}
	var report storageReport
	p.addStorageRuntimeStatus(&report)
	if report.FailedRuns != 2 || len(report.Alerts) != 1 || report.Alerts[0].Code != "cleanup_failing" {
		t.Fatalf("report: %+v", report)
	}
	p.recordStorageRun(storageWork{}, nil, policy)
	if _, ok := p.storageAlerts["cleanup_failing"]; ok {
		t.Fatal("alert not cleared after a complete run")
	}
}

func TestStorageReportCleanupLag(t *testing.T) {
	db := historyTestDB(t)
	now := time.Date(2026, 11, 28, 12, 0, 0, 0, time.UTC)
	policy := storagePolicy{SummaryDays: 30, SuccessDays: 7, FailureDays: 30, Batch: 32}
	// A successful call 10 days old: its details were due 3 days ago.
	seedStorageInvocation(t, db, 1, "a", "ok", now.Add(-10*24*time.Hour).Format(time.RFC3339Nano))
	report, err := readStorageReport(context.Background(), db, "", policy, now)
	if err != nil {
		t.Fatal(err)
	}
	if report.CleanupLagSeconds < int64((2*24*time.Hour).Seconds()) {
		t.Fatalf("lag %d", report.CleanupLagSeconds)
	}
	found := false
	for _, a := range report.Alerts {
		found = found || a.Code == "cleanup_behind"
	}
	if !found {
		t.Fatalf("cleanup_behind missing: %+v", report.Alerts)
	}
}

func TestMaintenanceStepTimings(t *testing.T) {
	db := historyTestDB(t)
	policy := storagePolicy{SummaryDays: 30, SuccessDays: 7, FailureDays: 30, Batch: 8}
	work, err := maintainInvocationStorage(context.Background(), db, policy, time.Now(), false, 0)
	if err != nil {
		t.Fatal(err)
	}
	for _, step := range storageStepNames {
		if _, ok := work.StepMS[step]; !ok {
			t.Fatalf("no timing for %s: %+v", step, work)
		}
	}
	if work.BatchRows != 8 {
		t.Fatalf("batch %d", work.BatchRows)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	work, err = maintainInvocationStorage(ctx, db, policy, time.Now(), false, 2)
	if err == nil || work.BudgetExhausted != stepSummaries || !strings.HasPrefix(err.Error(), stepSummaries+":") {
		t.Fatalf("spent budget: %v %+v", err, work)
	}
}

func toInt64(v any) int64 {
	switch n := v.(type) {
	case int64:
		return n
	case int:
		return int64(n)
	case float64:
		return int64(n)
	}
	return 0
}
