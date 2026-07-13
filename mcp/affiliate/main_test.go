package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type recordingPlatform struct {
	tk.BasePlatformClient
	mu        sync.Mutex
	calls     []callAppCall
	responses map[string]json.RawMessage
	queues    map[string][]json.RawMessage
	bindings  map[string]int64
}

type callAppCall struct {
	App   string
	Tool  string
	Input map[string]any
}

func newRecordingPlatform() *recordingPlatform {
	return &recordingPlatform{
		responses: map[string]json.RawMessage{},
		queues:    map[string][]json.RawMessage{},
		bindings: map[string]int64{
			"target-circle":     101,
			"impact":            102,
			"awin":              103,
			"cj-affiliate":      104,
			"amazon-associates": 105,
			"skimlinks":         106,
			"sovrn":             107,
			"partnerstack":      108,
			"shareasale":        109,
		},
	}
}

func (p *recordingPlatform) response(key string) (json.RawMessage, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if queue := p.queues[key]; len(queue) > 0 {
		raw := queue[0]
		p.queues[key] = queue[1:]
		return raw, true
	}
	raw, ok := p.responses[key]
	return raw, ok
}

func (p *recordingPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	bindings := map[string]any{}
	for role, id := range p.bindings {
		bindings[role] = float64(id)
	}
	return &sdk.InstallIdentity{
		AppName:   "affiliate",
		ProjectID: "test-project",
		Bindings:  bindings,
	}, nil
}

func (p *recordingPlatform) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	for role, connID := range p.bindings {
		if connID == id {
			return &sdk.PlatformConnection{ID: id, AppSlug: role, Status: "active"}, nil
		}
	}
	return nil, fmt.Errorf("unknown connection %d", id)
}

func (p *recordingPlatform) ExecuteIntegrationTool(connID int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	role := ""
	for r, id := range p.bindings {
		if id == connID {
			role = r
			break
		}
	}
	if role == "" {
		return nil, fmt.Errorf("unknown connection %d", connID)
	}
	p.mu.Lock()
	p.calls = append(p.calls, callAppCall{App: role, Tool: tool, Input: input})
	p.mu.Unlock()
	if raw, ok := p.response(role + ":" + tool); ok {
		return &sdk.ExecuteResult{Success: true, Status: 200, Data: raw}, nil
	}
	return nil, fmt.Errorf("no stub response for %s:%s", role, tool)
}

func (p *recordingPlatform) CallApp(app, tool string, input map[string]any) (json.RawMessage, error) {
	p.mu.Lock()
	p.calls = append(p.calls, callAppCall{App: app, Tool: tool, Input: input})
	p.mu.Unlock()
	if raw, ok := p.response(app + ":" + tool); ok {
		return raw, nil
	}
	return nil, fmt.Errorf("no stub response for %s:%s", app, tool)
}

func (p *recordingPlatform) CallAppResult(app, tool string, input map[string]any, out any) error {
	raw, err := p.CallApp(app, tool, input)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, out)
}

func newTestCtx(t *testing.T, platform sdk.PlatformClient, cfg map[string]string) *sdk.AppCtx {
	t.Helper()
	opts := []tk.Option{tk.WithProjectID("test-project")}
	if platform != nil {
		opts = append(opts, tk.WithPlatform(platform))
	}
	if cfg != nil {
		opts = append(opts, tk.WithConfig(cfg))
	}
	ctx := tk.NewAppCtx(t, "apteva.yaml", opts...)
	globalCtx = ctx
	return ctx
}

func TestEmbeddedManifestMatchesToolSurface(t *testing.T) {
	app := &App{}
	manifest := app.Manifest()
	if manifest.Name != "affiliate" || manifest.Version == "" || manifest.DB == nil {
		t.Fatalf("invalid embedded manifest: %+v", manifest)
	}
	declared := map[string]bool{}
	for _, tool := range manifest.Provides.MCPTools {
		declared[tool.Name] = true
	}
	for _, tool := range app.MCPTools() {
		if !declared[tool.Name] {
			t.Errorf("implemented tool %q is absent from apteva.yaml", tool.Name)
		}
		delete(declared, tool.Name)
	}
	for name := range declared {
		t.Errorf("apteva.yaml declares %q without a handler", name)
	}
}

func TestWorkersMatchManifestAndSchedules(t *testing.T) {
	app := &App{}
	manifest := app.Manifest()
	declared := map[string]string{}
	for _, worker := range manifest.Provides.Workers {
		declared[worker.Name] = worker.Schedule
	}
	workers := app.Workers()
	if len(workers) != 2 {
		t.Fatalf("workers=%d want 2", len(workers))
	}
	for _, worker := range workers {
		if got := declared[worker.Name]; got != worker.Schedule {
			t.Errorf("worker %q schedule=%q, manifest=%q", worker.Name, worker.Schedule, got)
		}
		delete(declared, worker.Name)
	}
	if len(declared) != 0 {
		t.Fatalf("manifest has workers without implementations: %v", declared)
	}
}

func TestRedirectHitSubscriptionMatchesManifest(t *testing.T) {
	app := &App{}
	manifest := app.Manifest()
	var subscribed bool
	for _, dep := range manifest.Requires.Apps {
		if dep.Name == "redirects" && len(dep.Events) == 1 && dep.Events[0] == "rule.hit" {
			subscribed = true
		}
	}
	if !subscribed {
		t.Fatal("manifest does not subscribe to redirects rule.hit")
	}
	handlers := app.EventHandlers()
	if len(handlers) != 1 || handlers[0].Event != "rule.hit" {
		t.Fatalf("event handlers=%+v", handlers)
	}
}

func TestRedirectHitUsesRuleIDAndAbsoluteDailyCount(t *testing.T) {
	ctx := newTestCtx(t, nil, nil)
	link, err := dbInsertLink(ctx.AppDB(), LinkInput{
		NetworkKey: "target-circle", DestinationURL: "https://merchant.example/product",
		AffiliateURL: "https://track.example/click?a=one", RedirectRuleID: 318,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := dbUpsertStat(ctx.AppDB(), StatInput{
		Date: "2026-07-13", NetworkKey: "target-circle", LinkID: link.ID,
		Clicks: 9, Conversions: 1, Currency: "EUR",
	}); err != nil {
		t.Fatal(err)
	}
	app := &App{}
	for _, count := range []int64{5, 5, 7} {
		err := app.handleRedirectHit(ctx, sdk.Event{SourceApp: "redirects", Data: map[string]any{
			"rule_id": 318, "date": "2026-07-13", "day_hits": count, "hits_total": 100 + count,
		}})
		if err != nil {
			t.Fatal(err)
		}
	}
	stats, err := dbStats(ctx.AppDB(), "2026-07-13", "2026-07-13", "target-circle", 0, link.ID, "day")
	if err != nil || len(stats) != 1 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
	if stats[0].Clicks != 9 || stats[0].RedirectClicks != 7 {
		t.Fatalf("provider and redirect clicks were not kept separate: %+v", stats[0])
	}
	out, err := app.toolStats(ctx, map[string]any{
		"network": "target-circle", "from": "2026-07-13T00:00:00Z", "to": "2026-07-13T23:59:59Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	unified := result["stats"].([]UnifiedStatRow)
	if len(unified) != 1 || unified[0].Clicks != 7 {
		t.Fatalf("unified MCP clicks=%+v", unified)
	}
	payload, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	for _, internal := range []string{"redirect_clicks", "provider_clicks", "clicks_available"} {
		if strings.Contains(string(payload), internal) {
			t.Fatalf("MCP response leaked internal field %q: %s", internal, payload)
		}
	}
	detailed, err := app.detailedStats(ctx, map[string]any{"network": "target-circle", "from": "2026-07-13", "to": "2026-07-13"})
	if err != nil {
		t.Fatal(err)
	}
	detailedResult := detailed.(map[string]any)
	if detailedResult["provider_clicks_available"] != false || detailedResult["redirect_clicks_available"] != true {
		t.Fatalf("unexpected internal click availability: %+v", detailedResult)
	}
}

func TestStatsDateRangeValidation(t *testing.T) {
	ctx := newTestCtx(t, nil, nil)
	app := &App{}
	if _, err := app.toolStats(ctx, map[string]any{"from": "July 13", "to": "2026-07-13"}); err == nil {
		t.Fatal("invalid from date was accepted")
	}
	if _, err := app.toolStats(ctx, map[string]any{"from": "2026-07-14", "to": "2026-07-13"}); err == nil {
		t.Fatal("reversed date range was accepted")
	}
	from, to, err := normalizedStatsRange(map[string]any{
		"from": "2026-07-13T23:30:00-07:00",
		"to":   "2026-07-14T23:00:00-07:00",
	})
	if err != nil || from != "2026-07-14" || to != "2026-07-15" {
		t.Fatalf("UTC-normalized range=%s..%s err=%v", from, to, err)
	}
}

func TestRedirectHitAutoMatchesCanonicalDestination(t *testing.T) {
	ctx := newTestCtx(t, nil, nil)
	link, err := dbInsertLink(ctx.AppDB(), LinkInput{
		NetworkKey: "target-circle", DestinationURL: "https://merchant.example/product",
		AffiliateURL: "https://TRACK.example:443/click?b=two&a=one#ignored",
	})
	if err != nil {
		t.Fatal(err)
	}
	app := &App{}
	err = app.handleRedirectHit(ctx, sdk.Event{SourceApp: "redirects", Data: map[string]any{
		"rule_id": 900, "destination": "https://track.example/click?a=one&b=two",
		"target": "https://track.example/click?a=one&b=two&inbound=yes",
		"date":   "2026-07-13", "day_hits": 3,
	}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := dbGetLink(ctx.AppDB(), link.ID)
	if err != nil || got.RedirectRuleID != 900 {
		t.Fatalf("auto match was not persisted: link=%+v err=%v", got, err)
	}
}

func TestRedirectHitAutoMatchesTargetWithPreservedQuery(t *testing.T) {
	ctx := newTestCtx(t, nil, nil)
	link, err := dbInsertLink(ctx.AppDB(), LinkInput{
		NetworkKey: "target-circle", DestinationURL: "https://merchant.example/product",
		AffiliateURL: "https://track.example/click?a=campaign&i=inventory",
	})
	if err != nil {
		t.Fatal(err)
	}
	err = (&App{}).handleRedirectHit(ctx, sdk.Event{SourceApp: "redirects", Data: map[string]any{
		"id":     904,
		"target": "https://track.example/click?utm_source=article&i=inventory&a=campaign",
		"at":     "2026-07-13T12:00:00Z",
	}})
	if err != nil {
		t.Fatal(err)
	}
	got, err := dbGetLink(ctx.AppDB(), link.ID)
	if err != nil || got.RedirectRuleID != 904 {
		t.Fatalf("target fallback was not persisted: link=%+v err=%v", got, err)
	}
}

func TestRedirectHitRejectsAmbiguousURLMatch(t *testing.T) {
	ctx := newTestCtx(t, nil, nil)
	for i := 0; i < 2; i++ {
		if _, err := dbInsertLink(ctx.AppDB(), LinkInput{
			NetworkKey: "target-circle", DestinationURL: fmt.Sprintf("https://merchant.example/%d", i),
			AffiliateURL: "https://track.example/shared",
		}); err != nil {
			t.Fatal(err)
		}
	}
	err := (&App{}).handleRedirectHit(ctx, sdk.Event{SourceApp: "redirects", Data: map[string]any{
		"rule_id": 901, "destination": "https://track.example/shared", "date": "2026-07-13", "day_hits": 1,
	}})
	if err == nil {
		t.Fatal("expected ambiguous redirect target error")
	}
	var count int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM redirect_clicks_daily`).Scan(&count); err != nil || count != 0 {
		t.Fatalf("ambiguous hit was persisted: count=%d err=%v", count, err)
	}
}

func TestRedirectHitWithoutSnapshotCountsEveryEvent(t *testing.T) {
	ctx := newTestCtx(t, nil, nil)
	link, err := dbInsertLink(ctx.AppDB(), LinkInput{
		NetworkKey: "target-circle", DestinationURL: "https://merchant.example/product",
		AffiliateURL: "https://track.example/click", RedirectRuleID: 902,
	})
	if err != nil {
		t.Fatal(err)
	}
	app := &App{}
	for i := 0; i < 2; i++ {
		if err := app.handleRedirectHit(ctx, sdk.Event{SourceApp: "redirects", Data: map[string]any{
			"rule_id": 902, "at": "2026-07-13T12:00:00Z",
		}}); err != nil {
			t.Fatal(err)
		}
	}
	stats, err := dbStats(ctx.AppDB(), "", "", "target-circle", 0, link.ID, "day")
	if err != nil || len(stats) != 1 || stats[0].RedirectClicks != 2 {
		t.Fatalf("per-event clicks=%+v err=%v", stats, err)
	}
}

func TestRedirectStatsReconciliationUsesAbsoluteCounts(t *testing.T) {
	platform := newRecordingPlatform()
	platform.responses["redirects:redirect_stats"] = json.RawMessage(`{
		"stats":[{"rule_id":903,"date":"2026-07-12","hits":8,"hits_total":40}]
	}`)
	ctx := newTestCtx(t, platform, nil)
	link, err := dbInsertLink(ctx.AppDB(), LinkInput{
		NetworkKey: "target-circle", DestinationURL: "https://merchant.example/product",
		AffiliateURL: "https://track.example/click", RedirectRuleID: 903,
	})
	if err != nil {
		t.Fatal(err)
	}
	app := &App{}
	if err := app.reconcileRedirectClicks(context.Background(), ctx, "2026-07-07", "2026-07-13"); err != nil {
		t.Fatal(err)
	}
	stats, err := dbStats(ctx.AppDB(), "", "", "target-circle", 0, link.ID, "day")
	if err != nil || len(stats) != 1 || stats[0].RedirectClicks != 8 {
		t.Fatalf("reconciled clicks=%+v err=%v", stats, err)
	}
	if len(platform.calls) != 1 || platform.calls[0].Tool != "redirect_stats" {
		t.Fatalf("calls=%+v", platform.calls)
	}
	if platform.calls[0].Input["limit"] != 250 || platform.calls[0].Input["offset"] != 0 {
		t.Fatalf("redirect_stats pagination=%+v", platform.calls[0].Input)
	}
}

func TestAutomaticStatsWindowUsesSevenUTCDays(t *testing.T) {
	from, to := automaticStatsWindow(time.Date(2026, time.July, 13, 23, 45, 0, 0, time.FixedZone("west", -7*60*60)))
	if from != "2026-07-08" || to != "2026-07-14" {
		t.Fatalf("window=%s..%s want 2026-07-08..2026-07-14", from, to)
	}
}

func TestAutomaticStatsRefreshUsesBoundProviderAndRollingDates(t *testing.T) {
	platform := newRecordingPlatform()
	platform.bindings = map[string]int64{"target-circle": 101}
	platform.responses["target-circle:transactions_list"] = json.RawMessage(`{"transactions":[]}`)
	ctx := newTestCtx(t, platform, nil)
	app := &App{}

	if err := app.runAutomaticRefresh(context.Background(), ctx, "stats", "2026-07-07", "2026-07-13"); err != nil {
		t.Fatal(err)
	}
	if len(platform.calls) != 1 {
		t.Fatalf("provider calls=%d want 1: %+v", len(platform.calls), platform.calls)
	}
	call := platform.calls[0]
	if call.App != "target-circle" || call.Tool != "transactions_list" {
		t.Fatalf("unexpected provider call: %+v", call)
	}
	if call.Input["savedFrom"] != "2026-07-07" || call.Input["savedTo"] != "2026-07-14" {
		t.Fatalf("provider dates=%v..%v", call.Input["savedFrom"], call.Input["savedTo"])
	}
}

func TestAutomaticRefreshSkipsDisabledNetwork(t *testing.T) {
	platform := newRecordingPlatform()
	platform.bindings = map[string]int64{"target-circle": 101}
	ctx := newTestCtx(t, platform, nil)
	app := &App{}
	if _, err := dbUpsertNetwork(ctx.AppDB(), "target-circle", "Target Circle", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE networks SET enabled = 0 WHERE key = 'target-circle'`); err != nil {
		t.Fatal(err)
	}

	if err := app.runAutomaticRefresh(context.Background(), ctx, "stats", "2026-07-07", "2026-07-13"); err != nil {
		t.Fatal(err)
	}
	if len(platform.calls) != 0 {
		t.Fatalf("disabled network made provider calls: %+v", platform.calls)
	}
}

func TestAutomaticRefreshContinuesAfterProviderFailure(t *testing.T) {
	platform := newRecordingPlatform()
	platform.bindings = map[string]int64{"target-circle": 101, "impact": 102}
	platform.responses["impact:actions_list"] = json.RawMessage(`{"Actions":[]}`)
	ctx := newTestCtx(t, platform, nil)
	app := &App{}

	err := app.runAutomaticRefresh(context.Background(), ctx, "stats", "2026-07-07", "2026-07-13")
	if err == nil {
		t.Fatal("expected aggregate error from target-circle")
	}
	if len(platform.calls) != 2 || platform.calls[1].App != "impact" {
		t.Fatalf("later provider was not refreshed after failure: %+v", platform.calls)
	}
}

func TestManualLinkCreate(t *testing.T) {
	ctx := newTestCtx(t, nil, nil)
	app := &App{}

	out, err := app.toolLinkCreate(ctx, map[string]any{
		"url":           "https://example.com/product",
		"network":       "target-circle",
		"affiliate_url": "https://track.example/click",
		"campaign":      "guide",
	})
	if err != nil {
		t.Fatal(err)
	}
	link := out.(map[string]any)["link"].(*Link)
	if link.NetworkKey != "target-circle" || link.Campaign != "guide" {
		t.Fatalf("unexpected link: %+v", link)
	}
	if link.ShortURL != "" {
		t.Fatalf("manual link should not be shortened by default: %+v", link)
	}
}

func TestRefreshOffersAndStatsFromProvider(t *testing.T) {
	platform := newRecordingPlatform()
	platform.responses["target-circle:offers_list"] = json.RawMessage(`{
		"data": [{
			"adInventorySid": "site-main",
			"name": "Publisher Site",
			"offers": [{
				"offerSid": "mintos",
				"advertiser": "Mintos",
				"name": "Mintos Investment Marketplace",
				"category": "Fintech",
				"pricings": [{"commissionType": "Fixed", "transactionType": "lead", "payout": 5, "currency": "EUR"}],
				"tracking": {"deeplinking": true, "cookieExpiration": 2592000}
			}]
		}]
	}`)
	platform.responses["target-circle:transactions_list"] = json.RawMessage(`{
		"transactions": [{
			"date": "2026-05-25",
			"offer_external_id": "mintos",
			"clicks": 10,
			"conversions": 2,
			"commission_cents": 1000,
			"currency": "EUR"
		}]
	}`)
	ctx := newTestCtx(t, platform, nil)
	app := &App{}

	out, err := app.toolRefresh(ctx, map[string]any{"network": "target-circle", "kind": "all", "pages": 1})
	if err != nil {
		t.Fatal(err)
	}
	if got := platform.calls[0].Input["limit"]; got != 50 {
		t.Fatalf("target-circle offers limit = %v, want 50", got)
	}
	summary := out.(*RefreshSummary)
	if summary.OffersUpserted != 1 || summary.StatsDaysUpserted != 1 {
		t.Fatalf("bad summary: %+v", summary)
	}
	offers, _, err := dbListOffers(ctx.AppDB(), "mintos", "", "", 10, 0)
	if err != nil || len(offers) != 1 {
		t.Fatalf("offers len=%d err=%v", len(offers), err)
	}
	if offers[0].CommissionSummary != "Fixed lead 5 EUR" || !offers[0].TrackingDeepLink {
		t.Fatalf("offer normalization failed: %+v", offers[0])
	}
	stats, err := dbStats(ctx.AppDB(), "", "", "target-circle", offers[0].ID, 0, "day")
	if err != nil || len(stats) != 1 || stats[0].CommissionCents != 1000 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
}

func TestTargetCircleRefreshCreatesLinksAndStats(t *testing.T) {
	platform := newRecordingPlatform()
	platform.responses["target-circle:offers_list"] = json.RawMessage(`{
		"data": [{
			"adInventorySid": "obcsig",
			"offers": [{
				"offerSid": "mintos",
				"name": "Mintos",
				"advertiser": "Mintos",
				"defaultTrackingUrl": "https://c.trackmytarget.com/?a=v80f9f&i=obcsig"
			}]
		}]
	}`)
	platform.responses["target-circle:transactions_list"] = json.RawMessage(`{
		"data": [{
			"saved": "2026-04-30 01:00:25",
			"offerSid": "mintos",
			"transactionId": "tx-1",
			"transactionAmount": 88.50,
			"payout": 0.885,
			"currency": "EUR"
		}, {
			"saved": "2026-04-30 03:00:00",
			"offerSid": "mintos",
			"transactionId": "tx-2",
			"transactionAmount": 10.00,
			"payout": 1.00,
			"currency": "EUR"
		}]
	}`)
	ctx := newTestCtx(t, platform, nil)
	app := &App{}

	out, err := app.toolRefresh(ctx, map[string]any{"network": "target-circle", "kind": "all", "pages": 1})
	if err != nil {
		t.Fatal(err)
	}
	summary := out.(*RefreshSummary)
	if summary.OffersUpserted != 1 || summary.LinksUpserted != 1 || summary.StatsDaysUpserted != 1 {
		t.Fatalf("bad summary: %+v", summary)
	}
	links, _, err := dbListLinks(ctx.AppDB(), "", "target-circle", 0, "", 10, 0)
	if err != nil || len(links) != 1 {
		t.Fatalf("links len=%d err=%v", len(links), err)
	}
	stats, err := dbStats(ctx.AppDB(), "", "", "target-circle", 0, 0, "day")
	if err != nil || len(stats) != 1 {
		t.Fatalf("stats len=%d err=%v", len(stats), err)
	}
	if stats[0].Date != "2026-04-30" || stats[0].Conversions != 2 || stats[0].RevenueCents != 9850 || stats[0].CommissionCents != 189 {
		t.Fatalf("unexpected stats: %+v", stats[0])
	}
}

func TestLinkCreateCallsProviderAndRedirects(t *testing.T) {
	platform := newRecordingPlatform()
	platform.responses["target-circle:codes_list"] = json.RawMessage(`{
		"clickUrl": "https://c.trackmytarget.com/abc123"
	}`)
	platform.responses["redirects:redirect_add"] = json.RawMessage(`{
		"redirect": {"id": 318}
	}`)
	ctx := newTestCtx(t, platform, map[string]string{"default_short_hostname": "go.example.com"})
	app := &App{}

	offer, err := dbUpsertOffer(ctx.AppDB(), OfferInput{
		NetworkKey:   "target-circle",
		ExternalID:   "mintos",
		MerchantName: "Mintos",
		OfferName:    "Mintos Investment Marketplace",
	})
	if err != nil {
		t.Fatal(err)
	}
	out, err := app.toolLinkCreate(ctx, map[string]any{
		"url":      "https://www.mintos.com/en/",
		"offer_id": offer.ID,
		"campaign": "p2p-guide",
		"shorten":  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	link := out.(map[string]any)["link"].(*Link)
	if link.AffiliateURL != "https://c.trackmytarget.com/abc123" {
		t.Fatalf("affiliate_url = %q", link.AffiliateURL)
	}
	if link.ShortURL != "https://go.example.com/p2p-guide" || link.RedirectRuleID != 318 {
		t.Fatalf("short link not recorded: %+v", link)
	}
	if len(platform.calls) != 2 {
		t.Fatalf("calls=%+v", platform.calls)
	}
	if platform.calls[0].App != "target-circle" || platform.calls[0].Tool != "codes_list" {
		t.Fatalf("provider call wrong: %+v", platform.calls[0])
	}
	if got := platform.calls[0].Input["parameters[ref1]"]; got != "p2p-guide" {
		t.Fatalf("target-circle ref1 = %v", got)
	}
	if platform.calls[1].App != "redirects" || platform.calls[1].Tool != "redirect_add" {
		t.Fatalf("redirect call wrong: %+v", platform.calls[1])
	}
}

func TestMoneyConversionRoundsToNearestCent(t *testing.T) {
	for _, value := range []any{153.85, "153.85", json.Number("153.85")} {
		if got := moneyCentsFromAny(value); got != 15385 {
			t.Fatalf("moneyCentsFromAny(%v) = %d, want 15385", value, got)
		}
	}
}

func TestTargetCircleClickRecordIsNotAConversion(t *testing.T) {
	row := statInputFromMap("target-circle", map[string]any{
		"saved": "2026-05-24 01:00:00", "transactionId": "click-1", "transactionType": "click",
	})
	if row.Clicks != 1 || row.Conversions != 0 {
		t.Fatalf("click record normalized as %+v", row)
	}
}

func TestTargetCircleUsesExclusiveSavedToBoundary(t *testing.T) {
	calls, err := providerStatCalls("target-circle", "2026-05-18", "2026-05-24", nil)
	if err != nil {
		t.Fatal(err)
	}
	if got := calls[0].Input["savedTo"]; got != "2026-05-25" {
		t.Fatalf("savedTo = %v, want 2026-05-25", got)
	}
}

func TestPaginatedStatsAggregateBeforeUpsert(t *testing.T) {
	platform := newRecordingPlatform()
	platform.queues["target-circle:transactions_list"] = []json.RawMessage{
		json.RawMessage(`{"next":"https://api.targetcircle.com/page/2","data":[{"saved":"2026-05-24 01:00:00","offerSid":"mintos","transactionId":"one","transactionAmount":100,"payout":1,"currency":"EUR"}]}`),
		json.RawMessage(`{"next":null,"data":[{"saved":"2026-05-24 02:00:00","offerSid":"mintos","transactionId":"two","transactionAmount":200,"payout":2,"currency":"EUR"}]}`),
	}
	ctx := newTestCtx(t, platform, nil)
	if _, err := dbUpsertOffer(ctx.AppDB(), OfferInput{NetworkKey: "target-circle", ExternalID: "mintos", MerchantName: "Mintos"}); err != nil {
		t.Fatal(err)
	}
	app := &App{}
	if _, err := app.toolRefresh(ctx, map[string]any{"network": "target-circle", "kind": "stats"}); err != nil {
		t.Fatal(err)
	}
	stats, err := dbStats(ctx.AppDB(), "", "", "target-circle", 0, 0, "day")
	if err != nil || len(stats) != 1 {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
	if stats[0].Conversions != 2 || stats[0].RevenueCents != 30000 || stats[0].CommissionCents != 300 {
		t.Fatalf("paginated values were not aggregated: %+v", stats[0])
	}
}

func TestStatReferenceAttributesManagedLink(t *testing.T) {
	platform := newRecordingPlatform()
	platform.responses["target-circle:transactions_list"] = json.RawMessage(`{"data":[{"saved":"2026-05-24 01:00:00","offerSid":"mintos","transactionId":"one","transactionAmount":10,"payout":1,"currency":"EUR","reference":{"ref1":"guide","ref2":"article-7"}}]}`)
	ctx := newTestCtx(t, platform, nil)
	offer, err := dbUpsertOffer(ctx.AppDB(), OfferInput{NetworkKey: "target-circle", ExternalID: "mintos", MerchantName: "Mintos"})
	if err != nil {
		t.Fatal(err)
	}
	link, err := dbInsertLink(ctx.AppDB(), LinkInput{NetworkKey: "target-circle", OfferID: offer.ID, DestinationURL: "https://mintos.com", AffiliateURL: "https://track.example/mintos", Campaign: "guide", SubID: "article-7"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (&App{}).toolRefresh(ctx, map[string]any{"network": "target-circle", "kind": "stats"}); err != nil {
		t.Fatal(err)
	}
	stats, err := dbStats(ctx.AppDB(), "", "", "target-circle", 0, link.ID, "link")
	if err != nil || len(stats) != 1 || stats[0].LinkID != link.ID {
		t.Fatalf("stats=%+v err=%v", stats, err)
	}
}

func TestRefreshCapabilitiesPreventPartialOrUnsupportedSync(t *testing.T) {
	platform := newRecordingPlatform()
	ctx := newTestCtx(t, platform, nil)
	app := &App{}
	if _, err := app.toolRefresh(ctx, map[string]any{"network": "awin", "kind": "all"}); err == nil {
		t.Fatal("expected Awin date validation error")
	}
	if _, err := app.toolRefresh(ctx, map[string]any{"network": "skimlinks", "kind": "all"}); err == nil {
		t.Fatal("expected Skimlinks refresh capability error")
	}
	if len(platform.calls) != 0 {
		t.Fatalf("validation must happen before provider writes/calls: %+v", platform.calls)
	}
}

func TestLinkRejectsOfferNetworkMismatch(t *testing.T) {
	ctx := newTestCtx(t, nil, nil)
	offer, err := dbUpsertOffer(ctx.AppDB(), OfferInput{NetworkKey: "target-circle", ExternalID: "mintos", MerchantName: "Mintos"})
	if err != nil {
		t.Fatal(err)
	}
	_, err = (&App{}).toolLinkCreate(ctx, map[string]any{"url": "https://mintos.com", "affiliate_url": "https://track.example", "offer_id": offer.ID, "network": "impact"})
	if err == nil {
		t.Fatal("expected offer/network mismatch error")
	}
}

func TestNetworkMetadataRedactsProviderTokens(t *testing.T) {
	ctx := newTestCtx(t, nil, nil)
	network, err := dbUpsertNetwork(ctx.AppDB(), "target-circle", "", map[string]any{"token": "secret", "nested": map[string]any{"api_key": "also-secret", "value": "ok"}})
	if err != nil {
		t.Fatal(err)
	}
	if network.LastRefreshedAt == "" || network.MetadataJSON != `{"nested":{"api_key":"[REDACTED]","value":"ok"},"token":"[REDACTED]"}` {
		t.Fatalf("network metadata was not safely stored: %+v", network)
	}
}
