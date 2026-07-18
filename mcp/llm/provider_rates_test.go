package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestProviderRateUpsertPreservesHistoryAndProjectOverride(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"))
	app := &App{}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	first, changed, err := dbProviderRateUpsert(ctx.AppDB(), "proj-test", map[string]any{
		"provider": "openai", "model_id": "model-a", "currency": "USD",
		"input_microunits_per_million": 1_000_000, "output_microunits_per_million": 2_000_000,
	}, false)
	if err != nil || !changed {
		t.Fatalf("first=%+v changed=%v err=%v", first, changed, err)
	}
	second, changed, err := dbProviderRateUpsert(ctx.AppDB(), "proj-test", map[string]any{
		"provider": "openai", "model_id": "openai/model-a", "currency": "USD",
		"input_microunits_per_million": 3_000_000, "output_microunits_per_million": 4_000_000,
	}, false)
	if err != nil || !changed || second.ID == first.ID {
		t.Fatalf("second=%+v changed=%v err=%v", second, changed, err)
	}
	active, err := resolveProviderRate(ctx.AppDB(), "proj-test", "openai", "openai/model-a")
	if err != nil || active == nil || active.ID != second.ID {
		t.Fatalf("active=%+v err=%v", active, err)
	}
	rows, err := dbProviderRatesList(ctx.AppDB(), "proj-test", "openai", "model-a", true)
	if err != nil || len(rows) != 2 || rows[1].EffectiveTo == "" {
		t.Fatalf("history=%+v err=%v", rows, err)
	}
}

func TestProviderModelPricingExtraction(t *testing.T) {
	args, ok := rateArgsFromModel(ProviderModel{
		Provider: "openrouter", ModelID: "vendor/model",
		Raw: json.RawMessage(`{"id":"vendor/model","pricing":{"prompt":"0.000002","completion":"0.000008","request":"0.01"}}`),
	})
	if !ok {
		t.Fatal("expected pricing metadata")
	}
	if got := int64Arg(args, "input_microunits_per_million"); got != 2_000_000 {
		t.Fatalf("input rate=%d", got)
	}
	if got := int64Arg(args, "output_microunits_per_million"); got != 8_000_000 {
		t.Fatalf("output rate=%d", got)
	}
	if got := int64Arg(args, "request_microunits"); got != 10_000 {
		t.Fatalf("request rate=%d", got)
	}
}

func TestCalculatedProviderCostIsRecordedAndReturnedInHeaders(t *testing.T) {
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"id":"priced","choices":[{"message":{"role":"assistant","content":"ok"}}],"usage":{"prompt_tokens":1000,"completion_tokens":500}}`))
	}))
	defer upstream.Close()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"), tk.WithConfig(map[string]string{"openai_api_key": "test"}))
	app := &App{httpClient: upstream.Client()}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	_, _ = dbProviderConfigUpsert(ctx.AppDB(), "proj-test", map[string]any{"provider": "openai", "base_url": upstream.URL})
	_, _, err := dbProviderRateUpsert(ctx.AppDB(), "proj-test", map[string]any{
		"provider": "openai", "model_id": "priced", "input_microunits_per_million": 2_000_000,
		"output_microunits_per_million": 8_000_000, "request_microunits": 10_000,
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	token, _ := createToken(ctx.AppDB(), map[string]any{"project_id": "proj-test", "subject_type": "agent", "subject_id": "a"})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"openai/priced","messages":[{"role":"user","content":"hello"}]}`))
	req.Header.Set("Authorization", "Bearer "+token["token"].(string))
	rec := httptest.NewRecorder()
	app.handleV1(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if got := rec.Header().Get("X-Apteva-Provider-Cost-Microunits"); got != "16000" {
		t.Fatalf("cost header=%q", got)
	}
	events, err := dbUsageEventsList(ctx.AppDB(), usageFilter{ProjectID: "proj-test"}, 10)
	if err != nil || len(events) != 1 {
		t.Fatalf("events=%+v err=%v", events, err)
	}
	if events[0].ProviderCostMicrounits != 16_000 || events[0].ProviderCostStatus != "calculated" || events[0].ProviderRateID == 0 {
		t.Fatalf("event=%+v", events[0])
	}
}

func TestProviderReportedCostOverridesRate(t *testing.T) {
	metering := parseProviderMetering([]byte(`{"usage":{"prompt_tokens":10,"completion_tokens":5,"cost":0.1234}}`))
	if !metering.CostReported || metering.CostMicrounits != 123_400 {
		t.Fatalf("metering=%+v", metering)
	}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"))
	app := &App{}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	ident := &TokenIdentity{ProjectID: "proj-test", SubjectType: "tenant", SubjectID: "a"}
	reservation, _, err := reserveUsage(ctx.AppDB(), ident, "openrouter", "openrouter/vendor/model", 10, 5, "reported", []*Policy{{Limits: Limits{}}}, true)
	if err != nil {
		t.Fatal(err)
	}
	event, err := finishUsageReservationCost(ctx.AppDB(), reservation.ID, "openrouter", "openrouter/vendor/model", 10, 5, "completed", "upstream",
		metering.CostMicrounits, metering.CostCurrency, metering.CostReported, providerCostDetails(metering))
	if err != nil {
		t.Fatal(err)
	}
	if event.ProviderCostMicrounits != 123_400 || event.ProviderCostStatus != "reported" || event.ProviderRateID != 0 {
		t.Fatalf("event=%+v", event)
	}
}

func TestCostLimitRejectsUnpricedAndOverBudgetRequests(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"))
	app := &App{}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	ident := &TokenIdentity{ProjectID: "proj-test", SubjectType: "tenant", SubjectID: "a"}
	policies := []*Policy{{Limits: Limits{MonthlyProviderCostLimitMicrounits: 50, ProviderCostCurrency: "USD"}}}
	if _, _, err := reserveUsage(ctx.AppDB(), ident, "openai", "openai/unpriced", 1, 1, "unpriced", policies, true); err == nil || !strings.Contains(err.Error(), "provider rate is required") {
		t.Fatalf("unpriced error=%v", err)
	}
	_, _, err := dbProviderRateUpsert(ctx.AppDB(), "proj-test", map[string]any{
		"provider": "openai", "model_id": "priced", "request_microunits": 100,
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := reserveUsage(ctx.AppDB(), ident, "openai", "openai/priced", 1, 1, "over-budget", policies, true); err == nil || !strings.Contains(err.Error(), "monthly provider cost limit") {
		t.Fatalf("budget error=%v", err)
	}
	_, _, err = dbProviderRateUpsert(ctx.AppDB(), "proj-test", map[string]any{
		"provider": "openai", "model_id": "free",
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	reservation, _, err := reserveUsage(ctx.AppDB(), ident, "openai", "openai/free", 1, 1, "free", policies, true)
	if err != nil {
		t.Fatalf("configured zero-price model should be allowed: %v", err)
	}
	event, err := finishUsageReservation(ctx.AppDB(), reservation.ID, "openai", "openai/free", 1, 1, "completed", "")
	if err != nil || event.ProviderCostStatus != "calculated" || event.ProviderCostMicrounits != 0 {
		t.Fatalf("free event=%+v err=%v", event, err)
	}
}
