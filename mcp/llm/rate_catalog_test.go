package main

import (
	"encoding/json"
	"sort"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

func TestAnthropicCatalogCoversDiscoveredModels(t *testing.T) {
	want := []string{
		"claude-fable-5",
		"claude-haiku-4-5-20251001",
		"claude-opus-4-1-20250805",
		"claude-opus-4-5-20251101",
		"claude-opus-4-6",
		"claude-opus-4-7",
		"claude-opus-4-8",
		"claude-sonnet-4-5-20250929",
		"claude-sonnet-4-6",
		"claude-sonnet-5",
	}
	seen := map[string]bool{}
	for _, rate := range builtinProviderRateCatalog {
		if rate.Provider == "anthropic" {
			seen[rate.ModelID] = true
		}
	}
	got := make([]string, 0, len(seen))
	for model := range seen {
		got = append(got, model)
	}
	sort.Strings(got)
	if len(got) != len(want) {
		t.Fatalf("catalog models=%v", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("catalog models=%v", got)
		}
	}
}

func TestAnthropicCatalogRefreshPricesEveryDiscoveredModel(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"))
	app := &App{}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	models := []ProviderModel{}
	seen := map[string]bool{}
	for _, rate := range builtinProviderRateCatalog {
		if rate.Provider != "anthropic" || seen[rate.ModelID] {
			continue
		}
		seen[rate.ModelID] = true
		models = append(models, ProviderModel{Provider: "anthropic", ModelID: rate.ModelID, Raw: json.RawMessage(`{"type":"model"}`)})
	}
	if err := dbProviderModelsReplace(ctx.AppDB(), "proj-test", "anthropic", models); err != nil {
		t.Fatal(err)
	}
	updated, err := app.refreshProviderRates(ctx.AppDB(), "proj-test", "anthropic")
	if err != nil || updated != 11 {
		t.Fatalf("updated=%d err=%v", updated, err)
	}
	current, err := dbProviderRatesList(ctx.AppDB(), "proj-test", "anthropic", "", false)
	if err != nil || len(current) != 10 {
		t.Fatalf("current rates=%d err=%v", len(current), err)
	}
	for _, rate := range current {
		if rate.Source != "builtin_catalog" || rate.SourceReference == "" {
			t.Fatalf("rate=%+v", rate)
		}
		var extra map[string]any
		if json.Unmarshal(rate.ExtraRates, &extra) != nil || extra["catalog_version"] != builtinRateCatalogVersion {
			t.Fatalf("catalog metadata=%s", rate.ExtraRates)
		}
	}
}

func TestBuiltinCatalogMaterializesFallbackAndPreservesManualRate(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"))
	app := &App{}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	if err := dbProviderModelsReplace(ctx.AppDB(), "proj-test", "anthropic", []ProviderModel{
		{Provider: "anthropic", ModelID: "claude-haiku-4-5-20251001", Raw: json.RawMessage(`{"id":"claude-haiku-4-5-20251001"}`)},
		{Provider: "anthropic", ModelID: "claude-opus-4-8", Raw: json.RawMessage(`{"id":"claude-opus-4-8"}`)},
	}); err != nil {
		t.Fatal(err)
	}
	manual, _, err := dbProviderRateUpsert(ctx.AppDB(), "proj-test", map[string]any{
		"provider": "anthropic", "model_id": "claude-haiku-4-5-20251001",
		"input_microunits_per_million": 123, "output_microunits_per_million": 456,
	}, false)
	if err != nil {
		t.Fatal(err)
	}
	updated, err := app.refreshProviderRates(ctx.AppDB(), "proj-test", "anthropic")
	if err != nil || updated != 1 {
		t.Fatalf("updated=%d err=%v", updated, err)
	}
	haiku, err := resolveProviderRate(ctx.AppDB(), "proj-test", "anthropic", "claude-haiku-4-5-20251001")
	if err != nil || haiku == nil || haiku.ID != manual.ID || haiku.Source != "manual" {
		t.Fatalf("haiku=%+v err=%v", haiku, err)
	}
	opus, err := resolveProviderRate(ctx.AppDB(), "proj-test", "anthropic", "claude-opus-4-8")
	if err != nil || opus == nil || opus.Source != "builtin_catalog" || opus.InputMicrounitsPerMillion != 5_000_000 || opus.OutputMicrounitsPerMillion != 25_000_000 {
		t.Fatalf("opus=%+v err=%v", opus, err)
	}
	if got := extraRateInt(opus, "cache_write_1h_microunits_per_million"); got != 10_000_000 {
		t.Fatalf("1h cache rate=%d", got)
	}
}

func TestBuiltinCatalogSupportsScheduledRates(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"))
	app := &App{}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	updated, err := materializeBuiltinCatalogRates(ctx.AppDB(), "proj-test", "anthropic", "claude-sonnet-5")
	if err != nil || updated != 2 {
		t.Fatalf("updated=%d err=%v", updated, err)
	}
	intro, err := resolveProviderRateAt(ctx.AppDB(), "proj-test", "anthropic", "claude-sonnet-5", "2026-08-01 00:00:00")
	if err != nil || intro == nil || intro.InputMicrounitsPerMillion != 2_000_000 || intro.OutputMicrounitsPerMillion != 10_000_000 {
		t.Fatalf("intro=%+v err=%v", intro, err)
	}
	standard, err := resolveProviderRateAt(ctx.AppDB(), "proj-test", "anthropic", "claude-sonnet-5", "2026-09-01 00:00:00")
	if err != nil || standard == nil || standard.InputMicrounitsPerMillion != 3_000_000 || standard.OutputMicrounitsPerMillion != 15_000_000 {
		t.Fatalf("standard=%+v err=%v", standard, err)
	}
	updated, err = materializeBuiltinCatalogRates(ctx.AppDB(), "proj-test", "anthropic", "claude-sonnet-5")
	if err != nil || updated != 0 {
		t.Fatalf("idempotent updated=%d err=%v", updated, err)
	}
}

func TestCatalogCacheDimensionsAndPartialDetection(t *testing.T) {
	rateSpec := builtinCatalogRatesFor("anthropic", "claude-haiku-4-5-20251001")[0]
	extra := map[string]any{}
	if err := json.Unmarshal([]byte(rateSpec.extraRatesJSON()), &extra); err != nil {
		t.Fatal(err)
	}
	extra["standard_context_max_tokens"] = 200_000
	extraJSON, _ := json.Marshal(extra)
	rate := &ProviderRate{
		InputMicrounitsPerMillion:       rateSpec.InputMicrounitsPerMillion,
		OutputMicrounitsPerMillion:      rateSpec.OutputMicrounitsPerMillion,
		CachedInputMicrounitsPerMillion: rateSpec.CachedInputMicrounitsPerMillion,
		CacheWriteMicrounitsPerMillion:  rateSpec.CacheWriteMicrounitsPerMillion,
		ExtraRates:                      extraJSON,
	}
	metering := parseProviderMetering([]byte(`{"usage":{"input_tokens":100,"output_tokens":10,"cache_read_input_tokens":20,"cache_creation_input_tokens":9,"cache_creation":{"ephemeral_5m_input_tokens":4,"ephemeral_1h_input_tokens":5}}}`))
	if metering.CacheWriteTokens != 9 || metering.CacheWrite1hTokens != 5 {
		t.Fatalf("metering=%+v", metering)
	}
	if got := calculateProviderCostDetailed(rate, 100, 10, 20, 9, 5); got != 167 {
		t.Fatalf("cost=%d", got)
	}
	if providerCostCalculationPartial(rate, 200_000, 5) {
		t.Fatal("catalog covers standard context and 1h cache writes")
	}
	if !providerCostCalculationPartial(rate, 200_001, 5) {
		t.Fatal("long-context usage should be marked partial")
	}
}

func TestProviderAPIRateOverridesBuiltinCatalog(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("proj-test"))
	app := &App{}
	if err := app.OnMount(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := materializeBuiltinCatalogRates(ctx.AppDB(), "proj-test", "anthropic", "claude-opus-4-8"); err != nil {
		t.Fatal(err)
	}
	providerRate, _, err := dbProviderRateUpsert(ctx.AppDB(), "proj-test", map[string]any{
		"provider": "anthropic", "model_id": "claude-opus-4-8", "source": "provider_api",
		"input_microunits_per_million": 6_000_000, "output_microunits_per_million": 30_000_000,
	}, true)
	if err != nil {
		t.Fatal(err)
	}
	resolved, err := resolveProviderRate(ctx.AppDB(), "proj-test", "anthropic", "claude-opus-4-8")
	if err != nil || resolved == nil || resolved.ID != providerRate.ID || resolved.Source != "provider_api" {
		t.Fatalf("resolved=%+v err=%v", resolved, err)
	}
	if updated, err := materializeBuiltinCatalogRates(ctx.AppDB(), "proj-test", "anthropic", "claude-opus-4-8"); err != nil || updated != 0 {
		t.Fatalf("catalog should not replace provider rate: updated=%d err=%v", updated, err)
	}
}
