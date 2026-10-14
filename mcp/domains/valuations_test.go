package main

import (
	"encoding/json"
	sdk "github.com/apteva/app-sdk"
	"testing"
)

func TestValuationCompletionStoredPerDomain(t *testing.T) {
	plat := &stubPlatform{connSlug: "replicate", bindingsOverride: map[string]any{"valuation_provider": float64(4)}}
	plat.executeFn = func(id int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
		if id != 4 {
			t.Fatalf("wrong connection %d", id)
		}
		if tool == "appraise_domains" {
			return successJSON(map[string]any{"id": "pred-1", "status": "starting"}), nil
		}
		if tool != "get_prediction" {
			t.Fatalf("unexpected %s", tool)
		}
		return successJSON(map[string]any{"id": "pred-1", "status": "succeeded", "output": map[string]any{"valuations": []any{map[string]any{"domain": "vectorlantern.com", "auction": 0, "marketplace": 145, "brokerage": 457}, map[string]any{"domain": "ledgerharvest.com", "auction": 0, "marketplace": 119, "brokerage": 409}}}}), nil
	}
	ctx := newTestCtx(t, plat)
	app := &App{}
	if _, err := app.toolDomainEstimateStart(ctx, map[string]any{"domains": "VectorLantern.com,ledgerharvest.com,vectorlantern.com"}); err != nil {
		t.Fatal(err)
	}
	// Changing the default does not reroute existing predictions to another account.
	plat.bindingsOverride["valuation_provider"] = float64(5)
	out, err := app.toolDomainEstimateList(ctx, map[string]any{"refresh": true})
	if err != nil {
		t.Fatal(err)
	}
	rows := out.(map[string]any)["valuations"].([]map[string]any)
	if len(rows) != 2 {
		t.Fatal(rows)
	}
	for _, r := range rows {
		if r["status"] != "succeeded" || *r["auction"].(*float64) != 0 || r["marketplace"] == nil {
			t.Fatal(r)
		}
	}
	before := len(plat.calls)
	out, err = app.toolDomainEstimateList(ctx, map[string]any{"domain": "vectorlantern.com", "refresh": true})
	if err != nil {
		t.Fatal(err)
	}
	if len(plat.calls) != before {
		t.Fatal("completed history called provider again")
	}
	if len(out.(map[string]any)["valuations"].([]map[string]any)) != 1 {
		t.Fatal(out)
	}
	var stored string
	if err = ctx.AppDB().QueryRow(`SELECT raw_json FROM domain_valuations WHERE domain='vectorlantern.com'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	var env map[string]any
	if json.Unmarshal([]byte(stored), &env) != nil || env["status"] != "succeeded" {
		t.Fatal(stored)
	}
}
func TestValuationFailureAndLegacyPending(t *testing.T) {
	plat := &stubPlatform{connSlug: "replicate", bindingsOverride: map[string]any{"valuation_provider": float64(4)}, replyByTool: map[string]*sdk.ExecuteResult{
		"get_prediction": successJSON(map[string]any{"id": "old-pred", "status": "succeeded", "output": map[string]any{"valuations": []any{map[string]any{"domain": "vectorlantern.com", "error": "cannot appraise"}}}})}}
	ctx := newTestCtx(t, plat)
	_, err := ctx.AppDB().Exec(`INSERT INTO domain_valuations(project_id,domain,provider_slug,status,prediction_id,raw_json) VALUES('test-proj','vectorlantern.com','replicate','starting','old-pred','{}')`)
	if err != nil {
		t.Fatal(err)
	}
	out, err := (&App{}).toolDomainEstimateList(ctx, map[string]any{"refresh": true})
	if err != nil {
		t.Fatal(err)
	}
	r := out.(map[string]any)["valuations"].([]map[string]any)[0]
	if r["status"] != "failed" || r["error"] != "cannot appraise" {
		t.Fatal(r)
	}
	if r["marketplace"].(*float64) != nil {
		t.Fatal(r)
	}
}
func TestValuationRejectsOtherProjectAndInvalidNames(t *testing.T) {
	plat := &stubPlatform{connSlug: "replicate", connProject: "other", bindingsOverride: map[string]any{"valuation_provider": float64(4)}}
	ctx := newTestCtx(t, plat)
	for _, d := range []string{"example.com", "not a domain"} {
		if _, err := (&App{}).toolDomainEstimateStart(ctx, map[string]any{"domain": d}); err == nil {
			t.Fatal(d)
		}
	}
	if len(plat.calls) != 0 {
		t.Fatal("paid prediction started for invalid input")
	}
}
