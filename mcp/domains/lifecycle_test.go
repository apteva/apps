package main

import (
	"context"
	"encoding/json"
	"errors"
	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"strings"
	"testing"
	"time"
)

type ackRecorder struct {
	*tk.EmitRecorder
	fail bool
}

func (r *ackRecorder) EmitWithProjectAck(c context.Context, topic, pid string, data any) error {
	if c.Err() != nil {
		return c.Err()
	}
	r.EmitWithProject(topic, pid, data)
	if r.fail {
		return errors.New("lost acknowledgment")
	}
	return nil
}
func lifecycleCtx(t *testing.T, plat *stubPlatform) (*sdk.AppCtx, *ackRecorder) {
	ctx := newTestCtx(t, plat).WithProject("test-proj")
	rec := &ackRecorder{EmitRecorder: tk.NewEmitRecorder()}
	ctx.SetEmitter(rec)
	return ctx, rec
}
func listingFixture(state string) map[string]any {
	return map[string]any{"name": "example.com", "status": state, "binPriceEnabled": true, "binPrice": map[string]any{"amount": "145", "currency": "USD"}}
}
func saleFixture() map[string]any {
	return map[string]any{"name": "example.com", "saleDateTime": time.Now().UTC().Format(time.RFC3339Nano), "salePrice": map[string]any{"amount": "145", "currency": "USD"}, "payout": map[string]any{"amount": "130.50", "currency": "USD"}, "source": "forSalePage"}
}
func TestMissingListingIsNotASaleAndErrorsPreserveState(t *testing.T) {
	plat := newSpaceshipStub(map[string]*sdk.ExecuteResult{"get_sellerhub_domain": successJSON(listingFixture("onSale"))})
	ctx, rec := lifecycleCtx(t, plat)
	b, _ := saleConnection(ctx, map[string]any{})
	if _, err := refreshListing(ctx, "test-proj", "example.com", b, "worker"); err != nil {
		t.Fatal(err)
	}
	plat.replyByTool["get_sellerhub_domain"] = &sdk.ExecuteResult{Success: false, Status: 429, Data: json.RawMessage(`{"error":"rate_limit"}`)}
	if _, err := refreshListing(ctx, "test-proj", "example.com", b, "worker"); err == nil {
		t.Fatal("429 accepted")
	}
	old, _ := listingGet(ctx.AppDB(), "test-proj", "example.com", 1)
	if old.State != "on_sale" || old.Error == "" {
		t.Fatal(old)
	}
	if old.NextCheckAt <= time.Now().Unix() {
		t.Fatal("no backoff")
	}
	plat.replyByTool["get_sellerhub_domain"] = &sdk.ExecuteResult{Success: false, Status: 404, Data: json.RawMessage(`{}`)}
	if _, err := refreshListing(ctx, "test-proj", "example.com", b, "worker"); err != nil {
		t.Fatal(err)
	}
	old, _ = listingGet(ctx.AppDB(), "test-proj", "example.com", 1)
	if old.State != "missing" {
		t.Fatal(old)
	}
	if len(rec.EventsByTopic("domain.sold")) != 0 {
		t.Fatal("disappearance marked sold")
	}
}
func TestSalesReportPersistsEvidenceAndDeduplicatesTransitions(t *testing.T) {
	sale := saleFixture()
	plat := newSpaceshipStub(map[string]*sdk.ExecuteResult{"get_sellerhub_domain": successJSON(listingFixture("onSale")), "list_sellerhub_sold_domains": successJSON(map[string]any{"items": []any{sale}})})
	ctx, rec := lifecycleCtx(t, plat)
	b, _ := saleConnection(ctx, map[string]any{})
	if _, err := refreshListing(ctx, "test-proj", "example.com", b, "worker"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if err := refreshSoldReport(context.Background(), ctx, "test-proj", b, true); err != nil {
			t.Fatal(err)
		}
	}
	snapshot, _ := listingGet(ctx.AppDB(), "test-proj", "example.com", 1)
	if snapshot.State != "sold" {
		t.Fatal(snapshot)
	}
	if len(rec.EventsByTopic("domain.sold")) != 1 {
		t.Fatal(rec.Events())
	}
	history, err := (&App{}).toolDomainSaleHistory(ctx, map[string]any{"domain": "example.com"})
	if err != nil {
		t.Fatal(err)
	}
	sales := history.(map[string]any)["sales"].([]map[string]any)
	if len(sales) != 1 || sales[0]["sale_price"] != "145.00" || sales[0]["payout"] != "130.50" {
		t.Fatal(sales)
	}
	var changes int
	if err = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM domain_listing_history`).Scan(&changes); err != nil || changes != 2 {
		t.Fatalf("changes=%d err=%v", changes, err)
	}
	// A stale listing response cannot erase confirmed sale evidence.
	if _, err = refreshListing(ctx, "test-proj", "example.com", b, "worker"); err != nil {
		t.Fatal(err)
	}
	snapshot, _ = listingGet(ctx.AppDB(), "test-proj", "example.com", 1)
	if snapshot.State != "sold" {
		t.Fatal(snapshot)
	}
}
func TestSoldStatusCanBeEnrichedWithoutAnotherSoldEvent(t *testing.T) {
	plat := newSpaceshipStub(map[string]*sdk.ExecuteResult{"get_sellerhub_domain": successJSON(listingFixture("sold")), "list_sellerhub_sold_domains": successJSON(map[string]any{"items": []any{saleFixture()}})})
	ctx, rec := lifecycleCtx(t, plat)
	b, _ := saleConnection(ctx, map[string]any{})
	if _, err := refreshListing(ctx, "test-proj", "example.com", b, "worker"); err != nil {
		t.Fatal(err)
	}
	if err := refreshSoldReport(context.Background(), ctx, "test-proj", b, true); err != nil {
		t.Fatal(err)
	}
	if len(rec.EventsByTopic("domain.sold")) != 1 {
		t.Fatal("duplicate sold event")
	}
	var payout string
	if err := ctx.AppDB().QueryRow(`SELECT payout FROM domain_sales`).Scan(&payout); err != nil || payout != "130.50" {
		t.Fatal(payout, err)
	}
}
func TestDurableOutboxRetriesStableIDsAndSurvivesContextRestart(t *testing.T) {
	plat := newSpaceshipStub(nil)
	ctx, rec := lifecycleCtx(t, plat)
	rec.fail = true
	b, _ := saleConnection(ctx, map[string]any{})
	if _, err := saveListing(ctx, "test-proj", "example.com", b, "on_sale", "onSale", listingFixture("onSale"), "worker"); err != nil {
		t.Fatal(err)
	}
	first := rec.Events()[0].Data.(map[string]any)["event_id"]
	var pending int
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM domain_lifecycle_events WHERE delivery_status='pending'`).Scan(&pending)
	if pending != 1 {
		t.Fatal(pending)
	}
	restarted := sdk.NewAppCtxForTest(&sdk.Manifest{}, ctx.AppDB(), sdk.Config{}, plat, ctx.Logger()).WithProject("test-proj")
	restarted.SetEmitter(rec)
	rec.fail = false
	if err := drainDomainEvents(context.Background(), restarted, "test-proj"); err != nil {
		t.Fatal(err)
	}
	if rec.Events()[1].Data.(map[string]any)["event_id"] != first {
		t.Fatal("event identity changed")
	}
	if err := drainDomainEvents(context.Background(), restarted, "test-proj"); err != nil {
		t.Fatal(err)
	}
	if len(rec.Events()) != 2 {
		t.Fatal("delivered event emitted again")
	}
}
func TestWorkerOnlyReadsAndUsesDispatchedProject(t *testing.T) {
	plat := newSpaceshipStub(map[string]*sdk.ExecuteResult{"get_sellerhub_domain": successJSON(listingFixture("onSale")), "list_sellerhub_sold_domains": successJSON(map[string]any{"items": []any{}})})
	ctx, rec := lifecycleCtx(t, plat)
	t.Setenv("APTEVA_PROJECT_ID", "")
	if _, err := upsertDomainInventory(ctx, "test-proj", "example.com", "spaceship", "spaceship", "", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := upsertDomainInventory(ctx, "other-project", "other.com", "spaceship", "spaceship", "", 1); err != nil {
		t.Fatal(err)
	}
	app := &App{}
	workers := app.Workers()
	if len(workers) != 2 || workers[0].Schedule != "@every 30s" {
		t.Fatal(workers)
	}
	if err := workers[0].Run(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	before := len(plat.calls)
	if err := workers[0].Run(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if len(plat.calls) != before {
		t.Fatal("worker ignored due times")
	}
	for _, call := range plat.calls {
		if call.Tool != "get_sellerhub_domain" && call.Tool != "list_sellerhub_sold_domains" {
			t.Fatal("worker made mutation", call)
		}
	}
	for _, event := range rec.Events() {
		if event.ProjectID != "test-proj" {
			t.Fatal(event)
		}
	}
	var n int
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM domain_listing_snapshots WHERE project_id='other-project'`).Scan(&n)
	if n != 0 {
		t.Fatal("cross-project refresh")
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := app.runLifecycleRefresh(cancelled, ctx, true); err != nil {
		t.Fatal(err)
	}
	if len(plat.calls) != before {
		t.Fatal("canceled worker called provider")
	}
}
func TestSalesCursorResumesBoundedPages(t *testing.T) {
	plat := newSpaceshipStub(nil)
	page := 0
	plat.executeFn = func(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
		if _, err := time.Parse("2006-01-02T15:04:05.000Z", strArg(input, "saleDateTimeTo")); err != nil || len(strArg(input, "saleDateTimeTo")) != 24 {
			t.Fatal("invalid provider date format", input)
		}
		if tool != "list_sellerhub_sold_domains" {
			return nil, errors.New("unexpected tool")
		}
		page++
		out := map[string]any{"items": []any{}}
		if page < 4 {
			out["cursor"] = string(rune('a' + page))
		}
		if page == 4 && input["cursor"] != "d" {
			t.Fatal(input)
		}
		return successJSON(out), nil
	}
	ctx, _ := lifecycleCtx(t, plat)
	b, _ := saleConnection(ctx, map[string]any{})
	if err := refreshSoldReport(context.Background(), ctx, "test-proj", b, true); err != nil {
		t.Fatal(err)
	}
	if page != 3 {
		t.Fatal(page)
	}
	if err := refreshSoldReport(context.Background(), ctx, "test-proj", b, true); err != nil {
		t.Fatal(err)
	}
	if page != 4 {
		t.Fatal(page)
	}
}
func TestRegistrationEventsExcludePurchaseSecretsAndCompletionReplays(t *testing.T) {
	plat := newSpaceshipPurchaseStub()
	ctx, rec := lifecycleCtx(t, plat)
	app := &App{}
	prepared, err := app.toolDomainRegistrationPrepare(ctx, spaceshipPrepareArgs())
	if err != nil {
		t.Fatal(err)
	}
	token := prepared.(map[string]any)["confirmation_token"].(string)
	if _, err = app.toolDomainRegister(ctx, map[string]any{"confirmation_token": token}); err != nil {
		t.Fatal(err)
	}
	plat.replyByTool["get_async_operation"].Data = json.RawMessage(`{"status":"success","type":"domains_Register","details":{"name":"example.com"}}`)
	for range 2 {
		if _, err = app.toolRegistrationStatus(ctx, map[string]any{"confirmation_token": token}); err != nil {
			t.Fatal(err)
		}
	}
	if len(rec.EventsByTopic("domain.registration.submitted")) != 1 || len(rec.EventsByTopic("domain.registered")) != 1 {
		t.Fatal(rec.Events())
	}
	for _, event := range rec.Events() {
		raw, _ := json.Marshal(event.Data)
		for _, secret := range []string{token, contactIDFixture} {
			if strings.Contains(string(raw), secret) {
				t.Fatal("purchase secret leaked to bus")
			}
		}
	}
}
func TestValuationLifecycleEmitsOnceAfterPersistedPrices(t *testing.T) {
	plat := &stubPlatform{connSlug: "replicate", bindingsOverride: map[string]any{"valuation_provider": float64(2)}, replyByTool: map[string]*sdk.ExecuteResult{
		"appraise_domains": successJSON(map[string]any{"id": "pred", "status": "starting"}),
		"get_prediction":   successJSON(map[string]any{"id": "pred", "status": "succeeded", "output": map[string]any{"valuations": []any{map[string]any{"domain": "example.com", "auction": 0, "marketplace": 145, "brokerage": 457}}}}),
	}}
	ctx, rec := lifecycleCtx(t, plat)
	app := &App{}
	if _, err := app.toolDomainEstimateStart(ctx, map[string]any{"domain": "example.com"}); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := app.toolDomainEstimateList(ctx, map[string]any{"refresh": true}); err != nil {
			t.Fatal(err)
		}
	}
	if len(rec.EventsByTopic("domain.valuation.started")) != 1 || len(rec.EventsByTopic("domain.valuation.completed")) != 1 {
		t.Fatal(rec.Events())
	}
	if len(plat.callsFor("appraise_domains")) != 1 {
		t.Fatal("refresh bought another estimate")
	}
	var price float64
	if err := ctx.AppDB().QueryRow(`SELECT marketplace FROM domain_valuations`).Scan(&price); err != nil || price != 145 {
		t.Fatal(price, err)
	}
}
func TestOldOrForeignSalesDoNotSellCurrentHolding(t *testing.T) {
	old := saleFixture()
	old["saleDateTime"] = "2000-01-01T00:00:00Z"
	foreign := saleFixture()
	foreign["name"] = "foreign.com"
	plat := newSpaceshipStub(map[string]*sdk.ExecuteResult{"get_sellerhub_domain": successJSON(listingFixture("onSale")), "list_sellerhub_sold_domains": successJSON(map[string]any{"items": []any{old, foreign}})})
	ctx, rec := lifecycleCtx(t, plat)
	b, _ := saleConnection(ctx, map[string]any{})
	if _, err := upsertDomainInventory(ctx, "test-proj", "example.com", "spaceship", "spaceship", "", 1); err != nil {
		t.Fatal(err)
	}
	if _, err := refreshListing(ctx, "test-proj", "example.com", b, "worker"); err != nil {
		t.Fatal(err)
	}
	if err := refreshSoldReport(context.Background(), ctx, "test-proj", b, true); err != nil {
		t.Fatal(err)
	}
	if len(rec.EventsByTopic("domain.sold")) != 0 {
		t.Fatal("old or unrelated holding marked sold")
	}
}

type blockingRefreshPlatform struct{ *stubPlatform }

func (p *blockingRefreshPlatform) ExecuteIntegrationToolContext(c context.Context, _ int64, _ string, _ map[string]any) (*sdk.ExecuteResult, error) {
	<-c.Done()
	return nil, c.Err()
}
func TestRefreshPropagatesDeadlineToProvider(t *testing.T) {
	base := newSpaceshipStub(nil)
	ctx, _ := lifecycleCtx(t, base)
	p := &blockingRefreshPlatform{stubPlatform: base}
	scoped := sdk.NewAppCtxForTest(&sdk.Manifest{}, ctx.AppDB(), sdk.Config{}, p, ctx.Logger()).WithProject("test-proj")
	b := &sdk.BoundIntegration{AppSlug: "spaceship", ConnectionID: 1}
	deadline, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	started := time.Now()
	if _, err := refreshListing(scoped, "test-proj", "example.com", b, "worker", deadline); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatal(err)
	}
	if time.Since(started) > time.Second {
		t.Fatal("provider request ignored worker deadline")
	}
}
