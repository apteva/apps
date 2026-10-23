package main

import (
	"encoding/json"
	"fmt"
	"reflect"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func TestBatchAvailabilityKeepsOrderErrorsAndUnknownPrices(t *testing.T) {
	p := newSpaceshipStub(nil)
	var active, maximum atomic.Int32
	p.executeFn = func(_ int64, tool string, in map[string]any) (*sdk.ExecuteResult, error) {
		n := active.Add(1)
		defer active.Add(-1)
		for old := maximum.Load(); n > old && !maximum.CompareAndSwap(old, n); old = maximum.Load() {
		}
		time.Sleep(time.Millisecond)
		return successJSON(map[string]any{"domain": in["domain"], "result": "available"}), nil
	}
	ctx := newTestCtx(t, p)
	out, err := (&App{}).toolDomainAvailabilityCheck(ctx, map[string]any{"domains": []any{" Alpha.example. ", "bad name", "beta.example", "alpha.example", "gamma.example", "delta.example", "epsilon.example"}})
	if err != nil {
		t.Fatal(err)
	}
	rows := out.(map[string]any)["results"].([]map[string]any)
	if len(rows) != 6 || rows[0]["domain"] != "alpha.example" || rows[1]["error"] == nil || rows[2]["domain"] != "beta.example" {
		t.Fatalf("batch results: %+v", rows)
	}
	if rows[0]["checked_at"] == nil {
		t.Fatal("missing check time")
	}
	a := rows[0]["availability"].(*DomainAvailability)
	if !a.Known || !a.Available || a.Price != "" || a.Currency != "" {
		t.Fatalf("invented pricing: %+v", a)
	}
	if len(p.callsFor("check_single_domain_availability")) != 5 || maximum.Load() > 4 {
		t.Fatal("duplicate/unbounded calls", maximum.Load(), p.calls)
	}
	for _, args := range []map[string]any{{"domains": []any{}}, {"domains": []any{"alpha.example", 123}}, {"domains": make([]string, 51)}} {
		before := len(p.calls)
		if _, e := (&App{}).toolDomainAvailabilityCheck(ctx, args); e == nil {
			t.Fatal("accepted malformed batch", args)
		}
		if len(p.calls) != before {
			t.Fatal("invalid batch called provider")
		}
	}
}

func TestBatchAvailabilityRetainsProviderPrices(t *testing.T) {
	p := &stubPlatform{replyByTool: map[string]*sdk.ExecuteResult{"check_availability": {Success: true, Status: 200, Data: json.RawMessage(porkbunAvailableJSON)}}}
	out, err := (&App{}).toolDomainAvailabilityCheck(newTestCtx(t, p), map[string]any{"domains": []string{"freshexample.com"}})
	if err != nil {
		t.Fatal(err)
	}
	a := out.(map[string]any)["results"].([]map[string]any)[0]["availability"].(*DomainAvailability)
	if a.Price != "11.06" || a.Currency != "USD" {
		t.Fatal(a)
	}
}

func seedExplorationValuation(t *testing.T, ctx *sdk.AppCtx, pid, domain, status string, conn int64) {
	t.Helper()
	_, err := ctx.AppDB().Exec(`INSERT INTO domain_valuations(project_id,domain,provider_slug,status,prediction_id,connection_id,marketplace) VALUES(?,?,'replicate',?,'saved-prediction',?,145)`, pid, domain, status, conn)
	if err != nil {
		t.Fatal(err)
	}
}
func explorationValuationPlatform() *stubPlatform {
	p := &stubPlatform{connSlug: "replicate", bindingsOverride: map[string]any{"valuation_provider": float64(4)}}
	var n atomic.Int64
	p.executeFn = func(_ int64, tool string, _ map[string]any) (*sdk.ExecuteResult, error) {
		if tool != "appraise_domains" {
			return nil, fmt.Errorf("unexpected tool %s", tool)
		}
		return successJSON(map[string]any{"id": fmt.Sprintf("paid-%d", n.Add(1)), "status": "starting"}), nil
	}
	return p
}
func TestEstimatesReuseCompletedPendingAndOnlyPayForMissingNames(t *testing.T) {
	p := explorationValuationPlatform()
	ctx := newTestCtx(t, p)
	app := &App{}
	seedExplorationValuation(t, ctx, "test-proj", "alpha.example", "succeeded", 4)
	seedExplorationValuation(t, ctx, "test-proj", "beta.example", "processing", 4)
	args := map[string]any{"domains": []any{"ALPHA.example", "beta.example", "gamma.example", "alpha.example"}}
	out, err := app.toolDomainEstimateStart(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	r := out.(map[string]any)
	if len(r["reused"].([]map[string]any)) != 2 || !reflect.DeepEqual(r["started_domains"], []string{"gamma.example"}) {
		t.Fatal(r)
	}
	calls := p.callsFor("appraise_domains")
	if len(calls) != 1 || calls[0].Input["domains"] != "gamma.example" {
		t.Fatal(calls)
	}
	// Existing CSV callers receive the same cache behavior.
	out, err = app.toolDomainEstimateStart(ctx, map[string]any{"domains": "alpha.example,beta.example,gamma.example"})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["cached"] != true || len(p.calls) != 1 {
		t.Fatal("repeat bought another estimate", out, p.calls)
	}
	if _, err = app.toolDomainEstimateStart(ctx, map[string]any{"domain": "alpha.example", "force_new": true}); err != nil {
		t.Fatal(err)
	}
	if len(p.callsFor("appraise_domains")) != 2 {
		t.Fatal("explicit new estimate not started")
	}
	var count int
	if err = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM domain_valuations`).Scan(&count); err != nil || count != 4 {
		t.Fatal("old history lost", count, err)
	}
	if _, err = app.toolDomainEstimateStart(ctx, map[string]any{"domain": "alpha.example", "force_new": "yes"}); err == nil {
		t.Fatal("accepted malformed force_new")
	}
}
func TestEstimateCacheIsProjectAndConnectionScoped(t *testing.T) {
	p := explorationValuationPlatform()
	ctx := newTestCtx(t, p)
	seedExplorationValuation(t, ctx, "another-project", "alpha.example", "succeeded", 4)
	seedExplorationValuation(t, ctx, "test-proj", "alpha.example", "succeeded", 5)
	if _, err := (&App{}).toolDomainEstimateStart(ctx, map[string]any{"domain": "alpha.example"}); err != nil {
		t.Fatal(err)
	}
	if len(p.callsFor("appraise_domains")) != 1 {
		t.Fatal("reused another project's/account's result")
	}
}
func TestConcurrentDefaultEstimateStartsShareSavedPrediction(t *testing.T) {
	p := explorationValuationPlatform()
	ctx := newTestCtx(t, p)
	var wg sync.WaitGroup
	errs := make(chan error, 8)
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := (&App{}).toolDomainEstimateStart(ctx, map[string]any{"domains": []string{"alpha.example"}})
			errs <- err
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	if len(p.callsFor("appraise_domains")) != 1 {
		t.Fatal("concurrent calls purchased duplicate estimates", p.calls)
	}
}
func TestListingPreviewsValidateExactPayloadWithoutChangingState(t *testing.T) {
	for _, provider := range []string{"spaceship", "dynadot"} {
		t.Run(provider, func(t *testing.T) {
			p := &stubPlatform{connSlug: provider}
			ctx := newTestCtx(t, p)
			app := &App{}
			args := map[string]any{"domain": "ALPHA.example", "price": "145", "minimum_offer": "100.50", "listing_type": "buy_now_and_make_offer", "description": "A domain", "dry_run": true}
			out, err := app.toolDomainSalePublish(ctx, args)
			if err != nil {
				t.Fatal(err)
			}
			r := out.(map[string]any)
			if r["published"] != false || r["dry_run"] != true || r["ownership_verified"] != false {
				t.Fatal(r)
			}
			if len(p.calls) != 0 {
				t.Fatal("preview executed provider", p.calls)
			}
			for _, table := range []string{"domains", "domain_listing_snapshots", "domain_listing_history", "domain_lifecycle_events"} {
				var n int
				if err = ctx.AppDB().QueryRow("SELECT COUNT(*) FROM " + table).Scan(&n); err != nil || n != 0 {
					t.Fatal("preview wrote state", table, n, err)
				}
			}
			for _, bad := range []map[string]any{{"price": "0"}, {"price": "NaN"}, {"price": "1.001"}, {"price": "145", "minimum_offer": "146"}, {"listing_type": "bad"}, {"dry_run": "true"}} {
				testArgs := map[string]any{"domain": "alpha.example", "price": "145", "dry_run": true}
				for k, v := range bad {
					testArgs[k] = v
				}
				if _, err = app.toolDomainSalePublish(ctx, testArgs); err == nil {
					t.Fatal("invalid preview accepted", testArgs)
				}
			}
			if len(p.calls) != 0 {
				t.Fatal("invalid preview called provider")
			}
			p.executeFn = func(_ int64, tool string, in map[string]any) (*sdk.ExecuteResult, error) {
				if !reflect.DeepEqual(in, r["payload"]) {
					return nil, fmt.Errorf("publication payload differs from preview")
				}
				return successJSON(map[string]any{"name": "alpha.example", "status": "verifying"}), nil
			}
			args["dry_run"] = false
			if _, err = app.toolDomainSalePublish(ctx, args); err != nil {
				t.Fatal(err)
			}
			if len(p.calls) != 1 || p.calls[0].Tool != r["provider_tool"] {
				t.Fatal("publication not preserved", p.calls)
			}
		})
	}
}
