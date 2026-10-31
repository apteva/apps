package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Meta's response for the production failure: Ads API "too many API calls".
func metaTooManyCalls() *sdk.ExecuteResult {
	return &sdk.ExecuteResult{Success: false, Status: 400, Data: json.RawMessage(
		`{"error":{"code":17,"error_subcode":2446079,"type":"OAuthException","message":"User request limit reached","error_user_title":"Ad Account Has Too Many API Calls"}}`,
	)}
}

func seedSelectedProjectAccount(t *testing.T, ctx *sdk.AppCtx, projectID string, connectionID int, nativeID string) int64 {
	t.Helper()
	result, err := ctx.AppDB().Exec(
		`INSERT INTO ad_accounts (project_id, platform, connection_id, native_account_id, display_name, management_mode)
		 VALUES (?, 'meta', ?, ?, 'Shared account', 'selected')`,
		projectID, connectionID, nativeID,
	)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	return id
}

func TestCollectorHierarchyRateLimitBacksOffWithoutFurtherCalls(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	clock := time.Now().UTC()
	app := &App{clock: func() time.Time { return clock }}
	id := seedSelectedAccount(t, ctx, "act_limited")
	acct, _, _ := app.resolveAdAccount(ctx, map[string]any{"ad_account_id": id})
	if err := app.upsertManagedCampaign(ctx, acct, map[string]any{"id": "c1"}, "imported"); err != nil {
		t.Fatal(err)
	}
	pf.executeResponder = func(_ int64, tool string, _ map[string]any) (*sdk.ExecuteResult, error) {
		if tool == "adset_list" {
			return metaTooManyCalls(), nil
		}
		return executeJSON(`{"data":[]}`), nil
	}
	if err := app.runPerformanceCollector(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if countCalls(pf, "adset_list") != 1 {
		t.Fatalf("hierarchy calls on first tick: %d", countCalls(pf, "adset_list"))
	}
	var status string
	var failures int
	var next string
	if err := ctx.AppDB().QueryRow(
		`SELECT last_status, failure_count, next_attempt_at FROM ad_sync_state WHERE ad_account_id=? AND level='ad_group'`, id,
	).Scan(&status, &failures, &next); err != nil || status != "failed" || failures != 1 || next == "" {
		t.Fatalf("hierarchy failure not recorded: status=%q failures=%d next=%q err=%v", status, failures, next, err)
	}
	before := len(pf.executeCalls)
	for minute := 1; minute <= 4; minute++ {
		clock = clock.Add(time.Minute)
		if err := app.runPerformanceCollector(context.Background(), ctx); err != nil {
			t.Fatal(err)
		}
	}
	if len(pf.executeCalls) != before {
		t.Fatalf("collector called the provider during cooldown: %#v", pf.executeCalls[before:])
	}
}

func TestProviderCooldownIsSharedAcrossProjectBindings(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	clock := time.Now().UTC()
	app := &App{clock: func() time.Time { return clock }}
	first := seedSelectedProjectAccount(t, ctx, "test-proj", 82, "act_shared")
	second := seedSelectedProjectAccount(t, ctx, "proj-b", 82, "act_shared")
	other := seedSelectedProjectAccount(t, ctx, "proj-c", 82, "act_other")
	pf.executeResponder = func(int64, string, map[string]any) (*sdk.ExecuteResult, error) {
		return metaTooManyCalls(), nil
	}
	a := accountRow(t, ctx, first)
	if _, errOut := app.execIntegrationTool(ctx, a, "campaign_list", map[string]any{}); errOut == nil || errOut["code"] != "provider_rate_limited" {
		t.Fatalf("rate limit not classified: %#v", errOut)
	}
	pf.executeResponder = func(int64, string, map[string]any) (*sdk.ExecuteResult, error) {
		return executeJSON(`{"data":[]}`), nil
	}
	b := accountRow(t, ctx, second)
	_, errOut := app.execIntegrationTool(ctx, b, "campaign_list", map[string]any{})
	if errOut == nil || errOut["provider_cooldown"] != true || errOut["retry_at"] == nil {
		t.Fatalf("second binding was not gated: %#v", errOut)
	}
	if len(pf.executeCalls) != 1 {
		t.Fatalf("gated call reached the provider: %d calls", len(pf.executeCalls))
	}
	// A different physical account on the same connection is unaffected.
	c := accountRow(t, ctx, other)
	if _, errOut := app.execIntegrationTool(ctx, c, "campaign_list", map[string]any{}); errOut != nil {
		t.Fatalf("unrelated account gated: %#v", errOut)
	}
	// The cooldown survives a restart and reopens once it ends.
	restarted := &App{clock: func() time.Time { return clock }}
	if restarted.providerCooldownUntil(ctx, b).IsZero() {
		t.Fatal("cooldown not persisted")
	}
	clock = clock.Add(providerCooldownMin + time.Second)
	if _, errOut := restarted.execIntegrationTool(ctx, b, "campaign_list", map[string]any{}); errOut != nil {
		t.Fatalf("gate did not reopen: %#v", errOut)
	}
	var rows int
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM ad_provider_cooldowns`).Scan(&rows)
	if rows != 0 {
		t.Fatalf("successful probe did not clear the cooldown: %d rows", rows)
	}
}

func TestRateLimitedUpdateIsNotRetried(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponder = func(int64, string, map[string]any) (*sdk.ExecuteResult, error) {
		return metaTooManyCalls(), nil
	}
	ctx := newAdsCtx(t, pf)
	app := &App{retryDelay: func(int) time.Duration { return 0 }}
	id := seedSelectedAccount(t, ctx, "act_update")
	acct, _, _ := app.resolveAdAccount(ctx, map[string]any{"ad_account_id": id})
	_, errOut := app.execIdempotentUpdate(ctx, acct, "campaign_update", map[string]any{"campaignId": "1", "status": "PAUSED"})
	if errOut == nil || errOut["code"] != "provider_rate_limited" || len(pf.executeCalls) != 1 {
		t.Fatalf("rate-limited update retried: calls=%d err=%#v", len(pf.executeCalls), errOut)
	}
}

func TestProviderRetryHintExtendsCooldown(t *testing.T) {
	res := metaTooManyCalls()
	res.Headers = map[string]string{
		"x-business-use-case-usage": `{"123":[{"type":"ads_management","call_count":100,"estimated_time_to_regain_access":42}]}`,
	}
	if got := providerRetryHint(res); got != 42*time.Minute {
		t.Fatalf("hint=%v", got)
	}
}

func TestHierarchySnapshotIsSharedAcrossProjectsAndFilteredPerProject(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}
	first := seedSelectedProjectAccount(t, ctx, "test-proj", 82, "act_shared")
	second := seedSelectedProjectAccount(t, ctx, "proj-b", 82, "act_shared")
	a := accountRow(t, ctx, first)
	metaDef := platforms["meta"]
	def := &metaDef
	b := accountRow(t, ctx, second)
	if err := app.upsertManagedCampaign(ctx, a, map[string]any{"id": "c1"}, "imported"); err != nil {
		t.Fatal(err)
	}
	if err := app.upsertManagedCampaign(ctx, b, map[string]any{"id": "c2"}, "imported"); err != nil {
		t.Fatal(err)
	}
	pf.executeResponder = func(_ int64, tool string, _ map[string]any) (*sdk.ExecuteResult, error) {
		if tool == "adset_list" {
			return executeJSON(`{"data":[{"id":"g1","campaign_id":"c1"},{"id":"g2","campaign_id":"c2"}]}`), nil
		}
		return executeJSON(`{"data":[]}`), nil
	}
	if failure := app.refreshManagedHierarchy(ctx, a, def); failure != nil {
		t.Fatal(failure)
	}
	if failure := app.refreshManagedHierarchy(ctx, b, def); failure != nil {
		t.Fatal(failure)
	}
	if len(pf.executeCalls) != 2 {
		t.Fatalf("projects sharing an account scanned separately: %d calls", len(pf.executeCalls))
	}
	for pid, want := range map[string]string{"test-proj": "g1", "proj-b": "g2"} {
		var got string
		var count int
		_ = ctx.AppDB().QueryRow(`SELECT COUNT(*), MAX(native_entity_id) FROM ad_entities WHERE project_id=? AND level='ad_group'`, pid).Scan(&count, &got)
		if count != 1 || got != want {
			t.Fatalf("project %s sees %d ad sets (%s), want only %s", pid, count, got, want)
		}
	}
	// A mutation invalidates the snapshot so the next read rescans.
	if _, errOut := app.execIntegrationTool(ctx, a, "adset_update", map[string]any{"adsetId": "g1"}); errOut != nil {
		t.Fatal(errOut)
	}
	if failure := app.refreshManagedHierarchy(ctx, a, def); failure != nil {
		t.Fatal(failure)
	}
	if countCalls(pf, "adset_list") != 2 {
		t.Fatalf("mutation did not invalidate the snapshot: %d scans", countCalls(pf, "adset_list"))
	}
}

func TestCachedListsAndCachedPerformanceMakeNoProviderCalls(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}
	id := seedSelectedAccount(t, ctx, "act_cached")
	acct, _, _ := app.resolveAdAccount(ctx, map[string]any{"ad_account_id": id})
	if err := app.upsertManagedCampaign(ctx, acct, map[string]any{"id": "c1"}, "imported"); err != nil {
		t.Fatal(err)
	}
	if err := app.upsertDeliveryEntities(ctx, acct, "ad_group", []map[string]any{{"id": "g1", "campaign_id": "c1", "name": "Group"}}, "c1"); err != nil {
		t.Fatal(err)
	}
	pf.executeResponder = func(_ int64, tool string, _ map[string]any) (*sdk.ExecuteResult, error) {
		t.Fatalf("passive refresh called provider tool %s", tool)
		return nil, nil
	}
	out, err := app.toolAdSetList(ctx, map[string]any{"ad_account_id": id, "campaign_id": "c1", "cached": true})
	sets := mustSucceed(t, out, err)
	if rows := sets["data"].([]map[string]any); len(rows) != 1 || sets["source"] != "cache" {
		t.Fatalf("cached ad sets: %#v", sets)
	}
	out, err = app.toolAdList(ctx, map[string]any{"ad_account_id": id, "adset_id": "g1", "cached": true})
	ads := mustSucceed(t, out, err)
	if ads["cache_empty"] != true {
		t.Fatalf("never-listed ad set should report cache_empty: %#v", ads)
	}
	for _, level := range []string{"campaign", "ad_group", "ad"} {
		out, err := app.toolPerformanceGet(ctx, map[string]any{
			"ad_account_id": id, "level": level, "refresh": false, "date_from": "2026-10-01", "date_to": "2026-10-02",
		})
		mustSucceed(t, out, err)
	}
}

// accountRow loads a binding from any project; the test context is pinned to
// test-proj, so resolveAdAccount cannot reach the others.
func accountRow(t *testing.T, ctx *sdk.AppCtx, id int64) *adAccount {
	t.Helper()
	acct := &adAccount{ID: id}
	if err := ctx.AppDB().QueryRow(
		`SELECT project_id, platform, connection_id, native_account_id, management_mode FROM ad_accounts WHERE id=?`, id,
	).Scan(&acct.ProjectID, &acct.Platform, &acct.ConnectionID, &acct.NativeAccountID, &acct.ManagementMode); err != nil {
		t.Fatal(err)
	}
	return acct
}
