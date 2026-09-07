package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type studioFake struct {
	*fakeAuth
	bindings                map[string]any
	repoID, deployID, envID int64
	source                  string
	fail                    bool
	callsMu                 sync.Mutex
	dispatches              int
	analyticsIn             []map[string]any
	facts                   map[string]map[string]any
	providerCall            func(string, map[string]any) (any, error)
}

func (f *studioFake) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{InstallID: 1, ProjectID: "test-proj", Bindings: f.bindings}, nil
}
func (f *studioFake) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	slug := "admob"
	if id == 8 {
		slug = "google-analytics"
	}
	if id == 9 {
		slug = "app-store-connect"
	}
	return &sdk.PlatformConnection{ID: id, AppSlug: slug, ProjectID: "test-proj"}, nil
}
func (f *studioFake) ExecuteIntegrationTool(id int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	if f.providerCall == nil {
		return nil, fmt.Errorf("unexpected provider call %s", tool)
	}
	out, e := f.providerCall(tool, input)
	if e != nil {
		return nil, e
	}
	b, _ := json.Marshal(out)
	return &sdk.ExecuteResult{Success: true, Status: 200, Data: b}, nil
}
func (f *studioFake) CallAppResult(app, tool string, in map[string]any, out any) error {
	if app == "auth" {
		return f.fakeAuth.CallAppResult(app, tool, in, out)
	}
	f.callsMu.Lock()
	defer f.callsMu.Unlock()
	var value any
	if in["_project_id"] != "test-proj" {
		return errors.New("missing trusted project scope")
	}
	switch app + "." + tool {
	case "code.repos_get":
		value = map[string]any{"repository": map[string]any{"id": f.repoID, "slug": "moon", "name": "Moon", "project_id": "test-proj"}}
	case "deploy.deploy_get":
		value = map[string]any{"deployment": map[string]any{"id": f.deployID, "project_id": "test-proj", "environment": "production", "name": "Moon Android", "target_kind": "android", "source_kind": "code", "source_ref": f.source}, "environments": []any{map[string]any{"id": f.envID, "name": "production"}}, "builds": []any{map[string]any{"id": 12, "deployment_id": f.deployID, "environment_id": f.envID, "status": "succeeded"}}, "releases": []any{map[string]any{"id": 13, "build_id": 12, "deployment_id": f.deployID, "environment_id": f.envID, "status": "pending"}}}
	case "deploy.deploy_build", "deploy.deploy_release", "deploy.deploy_promote", "deploy.deploy_halt", "deploy.deploy_rollout":
		f.dispatches++
		if f.fail {
			return errors.New("connection lost after dispatch")
		}
		value = map[string]any{"build": map[string]any{"id": 12}, "release": map[string]any{"id": 13}}
	case "analytics.analytics_track":
		f.analyticsIn = append(f.analyticsIn, in)
		f.facts[txt(in["upsert_key"])] = object(in["props"])
		if f.fail {
			return errors.New("ack lost after analytics insert")
		}
		value = map[string]any{"id": 1}
	case "analytics.analytics_query":
		f.analyticsIn = append(f.analyticsIn, in)
		value = map[string]any{"events": []any{}}
	default:
		return fmt.Errorf("unexpected %s.%s", app, tool)
	}
	b, _ := json.Marshal(value)
	return json.Unmarshal(b, out)
}
func newStudioFixture(t *testing.T) (*sdk.AppCtx, *studioFake, GameScope) {
	t.Helper()
	f := &studioFake{fakeAuth: newFakeAuth(t), bindings: map[string]any{"code": int64(2), "deploy": int64(3), "analytics": int64(4), "reporting": map[string]any{"ids": []any{int64(7), int64(8), int64(9)}}}, repoID: 1, deployID: 2, envID: 3, source: "moon", facts: map[string]map[string]any{}}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("test-proj"), tk.WithPlatform(f), tk.WithConfig(map[string]string{"analytics_enabled": "true"}))
	if e := initializeGames(ctx); e != nil {
		t.Fatal(e)
	}
	if e := initializeStudio(ctx); e != nil {
		t.Fatal(e)
	}
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = nil })
	out, e := gameAction(ctx, "create", map[string]any{"slug": "moon", "name": "Moon"})
	if e != nil {
		t.Fatal(e)
	}
	return ctx, f, out.(map[string]any)["game"].(*Game).Scope()
}
func linkStudio(t *testing.T, ctx *sdk.AppCtx, s GameScope) map[string]any {
	t.Helper()
	source, e := studioSourceSet(ctx, s, map[string]any{"repo_slug": "moon"})
	if e != nil {
		t.Fatal(e)
	}
	target, e := studioTargetSet(ctx, s, map[string]any{"source_id": source.(map[string]any)["id"], "deployment_id": int64(2), "platform": "android"})
	if e != nil {
		t.Fatal(e)
	}
	return target.(map[string]any)
}
func TestStudioScopeIdentityAndOwnership(t *testing.T) {
	ctx, f, s := newStudioFixture(t)
	target := linkStudio(t, ctx, s)
	other, e := gameAction(ctx, "create", map[string]any{"slug": "other", "name": "Other"})
	if e != nil {
		t.Fatal(e)
	}
	b := other.(map[string]any)["game"].(*Game).Scope()
	if _, e = studioSourceSet(ctx, b, map[string]any{"repo_slug": "moon"}); e != nil {
		t.Fatal("shared repository rejected", e)
	}
	if _, e = studioTargetSet(ctx, b, map[string]any{"source_id": "1", "deployment_id": int64(2), "platform": "android"}); e == nil {
		t.Fatal("target shared across games")
	}
	if _, _, e = studioTarget(ctx, b, txt(target["id"])); e == nil {
		t.Fatal("foreign game target accessible")
	}
	if _, e = linkGet(ctx, GameScope{"elsewhere", s.GameID}, "source", "1"); e == nil {
		t.Fatal("foreign project accessible")
	}
	f.repoID = 9
	if _, _, e = studioTarget(ctx, s, txt(target["id"])); e == nil {
		t.Fatal("repo recreation undetected")
	}
	f.repoID = 1
	f.source = "other"
	if _, _, e = studioTarget(ctx, s, txt(target["id"])); e == nil {
		t.Fatal("source drift undetected")
	}
	f.source = "moon"
	f.bindings["deploy"] = int64(99)
	if _, _, e = studioTarget(ctx, s, txt(target["id"])); e == nil {
		t.Fatal("installation replacement undetected")
	}
}
func TestStudioAmbiguousDispatchIsNeverRepeated(t *testing.T) {
	ctx, f, s := newStudioFixture(t)
	target := linkStudio(t, ctx, s)
	f.fail = true
	args := map[string]any{"target_id": target["id"], "request_key": "once"}
	result, e := studioDispatch(ctx, s, "build", args)
	if e != nil || result.(map[string]any)["status"] != "unknown" {
		t.Fatal(result, e)
	}
	f.fail = false
	for i := 0; i < 3; i++ {
		if _, e = studioDispatch(ctx, s, "build", args); e != nil {
			t.Fatal(e)
		}
	}
	if f.dispatches != 1 {
		t.Fatal("duplicate remote dispatch")
	}
	if _, e = studioDispatch(ctx, s, "build", map[string]any{"target_id": target["id"], "request_key": "different"}); e == nil {
		t.Fatal("unresolved request bypassed")
	}
	if _, e = studioReconcile(ctx, s, map[string]any{"request_key": "once", "build_id": 99, "confirm": true}); e == nil {
		t.Fatal("unrelated build adopted")
	}
	if _, e = studioReconcile(ctx, s, map[string]any{"request_key": "once", "build_id": 12, "confirm": true}); e != nil {
		t.Fatal(e)
	}
	if _, e = studioDispatch(ctx, s, "release", map[string]any{"target_id": target["id"], "request_key": "publish", "build_id": 99, "channel": "internal"}); e == nil {
		t.Fatal("foreign build released")
	}
	if _, e = studioDispatch(ctx, s, "release", map[string]any{"target_id": target["id"], "request_key": "publish", "build_id": 12, "channel": "internal"}); e != nil {
		t.Fatal(e)
	}
	if _, e = studioDispatch(ctx, s, "release", map[string]any{"target_id": target["id"], "request_key": "publish", "build_id": 12, "channel": "production"}); e == nil {
		t.Fatal("request key accepted changed channel")
	}
}
func TestStudioConcurrentDispatchAndArchive(t *testing.T) {
	ctx, f, s := newStudioFixture(t)
	target := linkStudio(t, ctx, s)
	var wg sync.WaitGroup
	for i := 0; i < 10; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, _ = studioDispatch(ctx, s, "build", map[string]any{"target_id": target["id"], "request_key": "race"})
		}()
	}
	wg.Wait()
	if f.dispatches != 1 {
		t.Fatalf("dispatches %d", f.dispatches)
	}
	if _, e := gameAction(ctx, "archive", map[string]any{"game_id": s.GameID}); e != nil {
		t.Fatal(e)
	}
	if _, e := studioAction(ctx, "build", map[string]any{"game_id": s.GameID, "target_id": target["id"], "request_key": "archived"}); e == nil {
		t.Fatal("archived game built")
	}
	var n int
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM game_delivery_requests`).Scan(&n)
	if n != 1 {
		t.Fatal("history changed")
	}
}
func TestAdMobCompletenessAndPrecision(t *testing.T) {
	source := map[string]any{"external_id": "ca-app-pub-test", "timezone": "UTC"}
	report := func() any {
		return []any{map[string]any{"header": map[string]any{"localizationSettings": map[string]any{"currencyCode": "USD"}}}, map[string]any{"row": map[string]any{"dimensionValues": map[string]any{"APP": map[string]any{"value": "ca-app-pub-test"}, "DATE": map[string]any{"value": "20260901"}}, "metricValues": map[string]any{"ESTIMATED_EARNINGS": map[string]any{"microsValue": "9007199254740993"}, "IMPRESSIONS": map[string]any{"integerValue": "9"}, "CLICKS": map[string]any{"integerValue": "1"}}}}, map[string]any{"footer": map[string]any{"matchingRowCount": "1"}}}
	}
	rows, e := parseAdMob(report(), source, "2026-09-01")
	if e != nil || rows[0]["earnings_micros"] != "9007199254740993" {
		t.Fatal(rows, e)
	}
	for _, test := range []string{"missing_footer", "truncated", "wrong_app", "warnings"} {
		t.Run(test, func(t *testing.T) {
			r := report().([]any)
			switch test {
			case "missing_footer":
				r = r[:2]
			case "truncated":
				object(object(r[2])["footer"])["matchingRowCount"] = "2"
			case "warnings":
				object(object(r[2])["footer"])["warnings"] = []any{map[string]any{"type": "OTHER"}}
			case "wrong_app":
				object(object(object(object(r[1])["row"])["dimensionValues"])["APP"])["value"] = "other"
			}
			if _, e := parseAdMob(r, source, "2026-09-01"); e == nil {
				t.Fatal("bad report accepted")
			}
		})
	}
}
func TestAppleSalesCurrenciesAndCorrections(t *testing.T) {
	raw := "Apple Identifier\tUnits\tDeveloper Proceeds\tCurrency of Proceeds\tBegin Date\tEnd Date\n123\t2\t1.25\tUSD\t09/01/2026\t09/01/2026\n123\t-1\t1.25\tUSD\t09/01/2026\t09/01/2026\n123\t1\t2.50\tEUR\t09/01/2026\t09/01/2026\n999\t9\t99\tUSD\t09/01/2026\t09/01/2026\n"
	facts, e := parseAppleSales([]byte(raw), map[string]any{"external_id": "123"}, "2026-09-01")
	if e != nil || len(facts) != 2 {
		t.Fatal(facts, e)
	}
	for _, f := range facts {
		if f["currency"] == "USD" && (f["units"] != "1" || f["proceeds_micros"] != "1250000") {
			t.Fatal(f)
		}
	}
	if _, e = parseAppleSales([]byte(raw), map[string]any{"external_id": "123"}, "2026-09-02"); e == nil {
		t.Fatal("wrong date accepted")
	}
}
func TestMetricSyncDedupLeaseAndScopedQuery(t *testing.T) {
	ctx, f, s := newStudioFixture(t)
	source := map[string]any{"id": "report", "provider": "google-analytics", "connection_id": 8, "external_id": "123", "stream_id": "456", "timezone": "UTC", "family": "engagement"}
	if e := linkPut(ctx, s, "metric", "report", source); e != nil {
		t.Fatal(e)
	}
	_, _ = ctx.AppDB().Exec(`INSERT INTO game_metric_syncs(project_id,game_id,source_id) VALUES(?,?,'report')`, s.ProjectID, s.GameID)
	f.providerCall = func(tool string, in map[string]any) (any, error) {
		if tool != "run_report" {
			return nil, errors.New("unexpected report")
		}
		if object(object(in["dimensionFilter"])["filter"])["fieldName"] != "streamId" {
			t.Fatal("missing app filter")
		}
		return map[string]any{"rowCount": 0, "metadata": map[string]any{"timeZone": "UTC"}}, nil
	}
	f.fail = true
	if _, e := syncMetricSource(ctx, s, "report", 1); e == nil {
		t.Fatal("lost acknowledgement was ignored")
	}
	f.fail = false
	if _, e := syncMetricSource(ctx, s, "report", 1); e != nil {
		t.Fatal(e)
	}
	if len(f.facts) != 1 {
		t.Fatal("duplicated daily metric")
	}
	_, _ = ctx.AppDB().Exec(`UPDATE game_metric_syncs SET lease_until=?`, time.Now().Add(time.Hour).UTC().Format(time.RFC3339))
	if _, e := syncMetricSource(ctx, s, "report", 1); e == nil {
		t.Fatal("sync lease ignored")
	}
	if _, e := gameMetricsQuery(ctx, s, map[string]any{"where": map[string]any{"props.game_id": "other"}}); e != nil {
		t.Fatal(e)
	}
	last := f.analyticsIn[len(f.analyticsIn)-1]
	if object(last["where"])["props.game_id"] != s.GameID {
		t.Fatal("game filter overridden")
	}
	f.bindings["reporting"] = []any{}
	if _, e := metricExecute(ctx, s, source, "run_report", nil); e == nil {
		t.Fatal("revoked connection used")
	}
}
func TestTelemetryAtomicReplayAndNoRewards(t *testing.T) {
	ctx, f, s := newStudioFixture(t)
	ev := PlayEvent{ID: "event-1", Name: "run_completed", SessionID: "session", Release: "1.0", Environment: "test", Time: time.Now().UTC().Format(time.RFC3339), Props: map[string]any{"score": float64(10)}}
	if n, e := recordPlayEvents(ctx, s, 42, []PlayEvent{ev}); e != nil || n != 1 {
		t.Fatal(n, e)
	}
	if n, e := recordPlayEvents(ctx, s, 42, []PlayEvent{ev}); e != nil || n != 0 {
		t.Fatal(n, e)
	}
	changed := ev
	changed.Release = "changed"
	if _, e := recordPlayEvents(ctx, s, 42, []PlayEvent{changed}); e == nil {
		t.Fatal("changed event accepted")
	}
	f.fail = true
	if e := drainOutbox(ctx); e != nil {
		t.Fatal(e)
	}
	f.fail = false
	_, _ = ctx.AppDB().Exec(`UPDATE game_outbox SET next_attempt=''`)
	if e := drainOutbox(ctx); e != nil {
		t.Fatal(e)
	}
	if len(f.facts) != 1 {
		t.Fatalf("duplicate analytics rows: %d", len(f.facts))
	}
	var n int
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM player_stats WHERE game_id=?`, s.GameID).Scan(&n)
	if n != 0 {
		t.Fatal("telemetry changed player stats")
	}
	bad := ev
	bad.ID = "second"
	bad.Name = "grant_coins"
	if _, e := recordPlayEvents(ctx, s, 42, []PlayEvent{ev, bad}); e == nil {
		t.Fatal("unknown telemetry event accepted")
	}
}
func TestMissingAnalyticsDoesNotExhaustRetries(t *testing.T) {
	ctx, f, s := newStudioFixture(t)
	delete(f.bindings, "analytics")
	if e := queueEvent(ctx.AppDB(), s, "play.session_started", map[string]any{"player_id": 1}, true); e != nil {
		t.Fatal(e)
	}
	if e := drainOutbox(ctx); e != nil {
		t.Fatal(e)
	}
	var attempts int
	var detail string
	_ = ctx.AppDB().QueryRow(`SELECT attempts,last_error FROM game_outbox WHERE analytics=1`).Scan(&attempts, &detail)
	if attempts != 0 || !strings.Contains(detail, "not connected") {
		t.Fatal(attempts, detail)
	}
}

func TestStudioRecoveryWithoutRemoteOperationAndReadDuringCodeOutage(t *testing.T) {
	ctx, f, s := newStudioFixture(t)
	target := linkStudio(t, ctx, s)
	f.fail = true
	if _, e := studioDispatch(ctx, s, "build", map[string]any{"target_id": target["id"], "request_key": "no-receipt"}); e != nil {
		t.Fatal(e)
	}
	if _, e := studioReconcile(ctx, s, map[string]any{"request_key": "no-receipt", "resolution": "not_created", "confirm": true}); e == nil {
		t.Fatal("unverified resolution accepted")
	}
	if _, e := studioReconcile(ctx, s, map[string]any{"request_key": "no-receipt", "resolution": "not_created", "confirm": true, "reason": "Verified in Deploy: rejected before creating any build"}); e != nil {
		t.Fatal(e)
	}
	delete(f.bindings, "code")
	if _, e := studioAction(ctx, "release_status", map[string]any{"game_id": s.GameID, "target_id": target["id"]}); e != nil {
		t.Fatal("Code outage blocked existing release visibility", e)
	}
	if _, e := studioAction(ctx, "build", map[string]any{"game_id": s.GameID, "target_id": target["id"], "request_key": "must-fail"}); e == nil {
		t.Fatal("build without Code accepted")
	}
	if _, e := gameAction(ctx, "archive", map[string]any{"game_id": s.GameID}); e != nil {
		t.Fatal(e)
	}
	if _, e := studioAction(ctx, "history", map[string]any{"game_id": s.GameID}); e != nil {
		t.Fatal("archived history unreadable", e)
	}
}

func TestStudioHTTPRejectsNullAndGetMutation(t *testing.T) {
	_, _, s := newStudioFixture(t)
	rec := doReq((&App{}).handleStudio, "POST", "/admin/games/x/studio/build", json.RawMessage("null"), map[string]string{"game_id": s.GameID, "action": "build"})
	if rec.Code != 400 {
		t.Fatal(rec.Code)
	}
	rec = doReq((&App{}).handleStudio, "GET", "/admin/games/x/studio/build", nil, map[string]string{"game_id": s.GameID, "action": "build"})
	if rec.Code != 405 {
		t.Fatal("GET mutation was not rejected", rec.Code)
	}
}

func TestAdMobInventoryPaginationAndDailyImport(t *testing.T) {
	ctx, f, s := newStudioFixture(t)
	pages := 0
	f.providerCall = func(tool string, in map[string]any) (any, error) {
		switch tool {
		case "get_account":
			return map[string]any{"currencyCode": "USD", "reportingTimeZone": "America/Los_Angeles"}, nil
		case "list_apps":
			pages++
			if in["pageToken"] == "" {
				return map[string]any{"apps": []any{}, "nextPageToken": "next"}, nil
			}
			if in["pageToken"] != "next" {
				t.Fatal("wrong pagination input", in)
			}
			return map[string]any{"apps": []any{map[string]any{"appId": "ca-app-pub-123/456"}}}, nil
		case "generate_network_report":
			spec := object(in["reportSpec"])
			if len(spec["dimensionFilters"].([]any)) != 1 {
				t.Fatal("app filter missing")
			}
			return []any{map[string]any{"header": map[string]any{"localizationSettings": map[string]any{"currencyCode": "USD", "reportingTimeZone": "America/Los_Angeles"}}}, map[string]any{"footer": map[string]any{"matchingRowCount": "0"}}}, nil
		}
		return nil, fmt.Errorf("unexpected AdMob tool: %s", tool)
	}
	source, e := metricSourceSet(ctx, s, map[string]any{"provider": "admob", "connection_id": 7, "external_id": "ca-app-pub-123/456", "account_id": "pub-123", "family": "network"})
	if e != nil {
		t.Fatal(e)
	}
	if pages != 2 {
		t.Fatal("pagination did not complete", pages)
	}
	if _, e = syncMetricSource(ctx, s, txt(source.(map[string]any)["id"]), 1); e != nil {
		t.Fatal(e)
	}
	if len(f.facts) != 1 {
		t.Fatal("missing daily report")
	}
	for _, fact := range f.facts {
		rows := fact["facts"].([]map[string]any)
		if rows[0]["earnings_micros"] != "0" || rows[0]["currency"] != "USD" {
			t.Fatal(fact)
		}
	}
}

func TestRolloutFractionParticipatesInRequestIdentity(t *testing.T) {
	ctx, f, s := newStudioFixture(t)
	target := linkStudio(t, ctx, s)
	args := map[string]any{"target_id": target["id"], "request_key": "rollout", "release_id": 13, "fraction": 0.1}
	if _, e := studioDispatch(ctx, s, "rollout", args); e != nil {
		t.Fatal(e)
	}
	args["fraction"] = 0.5
	if _, e := studioDispatch(ctx, s, "rollout", args); e == nil {
		t.Fatal("changed rollout replay accepted")
	}
	if f.dispatches != 1 {
		t.Fatal("repeated rollout", f.dispatches)
	}
	if _, e := studioAction(ctx, "logs", map[string]any{"game_id": s.GameID, "target_id": target["id"], "build_id": 999}); e == nil {
		t.Fatal("foreign log accessible")
	}
}
