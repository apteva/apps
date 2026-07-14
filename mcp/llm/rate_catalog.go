package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"strings"
)

const builtinRateCatalogVersion = "2026-07-14"

type builtinProviderRate struct {
	Provider                         string
	ModelID                          string
	Currency                         string
	InputMicrounitsPerMillion        int64
	OutputMicrounitsPerMillion       int64
	CachedInputMicrounitsPerMillion  int64
	CacheWriteMicrounitsPerMillion   int64
	CacheWrite1hMicrounitsPerMillion int64
	RequestMicrounits                int64
	EffectiveFrom                    string
	EffectiveTo                      string
	SourceReference                  string
	StandardContextMaxTokens         int64
}

func anthropicCatalogRate(model string, input, output, cachedInput, cacheWrite5m, cacheWrite1h int64, source string) builtinProviderRate {
	return builtinProviderRate{
		Provider: "anthropic", ModelID: model, Currency: "USD",
		InputMicrounitsPerMillion: input, OutputMicrounitsPerMillion: output,
		CachedInputMicrounitsPerMillion: cachedInput, CacheWriteMicrounitsPerMillion: cacheWrite5m,
		CacheWrite1hMicrounitsPerMillion: cacheWrite1h,
		EffectiveFrom:                    "2026-07-14 00:00:00", SourceReference: source,
	}
}

var builtinProviderRateCatalog = func() []builtinProviderRate {
	const (
		anthropicPrices = "https://platform.claude.com/docs/en/about-claude/pricing"
		fablePrices     = "https://www.anthropic.com/claude/fable"
		haikuPrices     = "https://www.anthropic.com/claude/haiku"
		opusPrices      = "https://www.anthropic.com/claude/opus"
		sonnetPrices    = "https://www.anthropic.com/claude/sonnet"
	)
	rates := []builtinProviderRate{
		anthropicCatalogRate("claude-fable-5", 10_000_000, 50_000_000, 1_000_000, 12_500_000, 20_000_000, fablePrices),
		anthropicCatalogRate("claude-haiku-4-5-20251001", 1_000_000, 5_000_000, 100_000, 1_250_000, 2_000_000, haikuPrices),
		anthropicCatalogRate("claude-opus-4-1-20250805", 15_000_000, 75_000_000, 1_500_000, 18_750_000, 30_000_000, anthropicPrices),
		anthropicCatalogRate("claude-opus-4-5-20251101", 5_000_000, 25_000_000, 500_000, 6_250_000, 10_000_000, opusPrices),
		anthropicCatalogRate("claude-opus-4-6", 5_000_000, 25_000_000, 500_000, 6_250_000, 10_000_000, opusPrices),
		anthropicCatalogRate("claude-opus-4-7", 5_000_000, 25_000_000, 500_000, 6_250_000, 10_000_000, opusPrices),
		anthropicCatalogRate("claude-opus-4-8", 5_000_000, 25_000_000, 500_000, 6_250_000, 10_000_000, opusPrices),
		anthropicCatalogRate("claude-sonnet-4-5-20250929", 3_000_000, 15_000_000, 300_000, 3_750_000, 6_000_000, sonnetPrices),
		anthropicCatalogRate("claude-sonnet-4-6", 3_000_000, 15_000_000, 300_000, 3_750_000, 6_000_000, sonnetPrices),
	}
	intro := anthropicCatalogRate("claude-sonnet-5", 2_000_000, 10_000_000, 200_000, 2_500_000, 4_000_000, sonnetPrices)
	intro.EffectiveTo = "2026-09-01 00:00:00"
	standard := anthropicCatalogRate("claude-sonnet-5", 3_000_000, 15_000_000, 300_000, 3_750_000, 6_000_000, sonnetPrices)
	standard.EffectiveFrom = "2026-09-01 00:00:00"
	return append(rates, intro, standard)
}()

func builtinCatalogRatesFor(provider, modelID string) []builtinProviderRate {
	provider = normalizeProvider(provider)
	modelID = upstreamModel(provider, modelID)
	out := []builtinProviderRate{}
	for _, rate := range builtinProviderRateCatalog {
		if rate.Provider == provider && rate.ModelID == modelID {
			out = append(out, rate)
		}
	}
	return out
}

func (r builtinProviderRate) extraRatesJSON() string {
	extra := map[string]any{
		"catalog_version": builtinRateCatalogVersion,
		"verified_at":     builtinRateCatalogVersion,
	}
	if r.StandardContextMaxTokens > 0 {
		extra["standard_context_max_tokens"] = r.StandardContextMaxTokens
	}
	if r.CacheWrite1hMicrounitsPerMillion > 0 {
		extra["cache_write_1h_microunits_per_million"] = r.CacheWrite1hMicrounitsPerMillion
	}
	b, _ := json.Marshal(extra)
	return string(b)
}

func materializeBuiltinCatalogRates(db *sql.DB, projectID, provider, modelID string) (int, error) {
	rates := builtinCatalogRatesFor(provider, modelID)
	if len(rates) == 0 {
		return 0, nil
	}
	var higherPriority int
	err := db.QueryRow(`SELECT COUNT(*) FROM provider_rates
		WHERE project_id IN ('', ?) AND provider=? AND model_id=?
		  AND source IN ('manual','provider_api') AND effective_to IS NULL`,
		projectID, rates[0].Provider, rates[0].ModelID).Scan(&higherPriority)
	if err != nil || higherPriority > 0 {
		return 0, err
	}

	tx, err := db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()
	updated := 0
	for _, rate := range rates {
		extra := rate.extraRatesJSON()
		var existingID int64
		err := tx.QueryRow(`SELECT id FROM provider_rates
			WHERE project_id=? AND provider=? AND model_id=? AND currency=?
			  AND input_microunits_per_million=? AND output_microunits_per_million=?
			  AND cached_input_microunits_per_million=? AND cache_write_microunits_per_million=?
			  AND request_microunits=? AND extra_rates_json=? AND source='builtin_catalog'
			  AND source_reference=? AND effective_from=? AND COALESCE(effective_to,'')=? LIMIT 1`,
			projectID, rate.Provider, rate.ModelID, rate.Currency,
			rate.InputMicrounitsPerMillion, rate.OutputMicrounitsPerMillion,
			rate.CachedInputMicrounitsPerMillion, rate.CacheWriteMicrounitsPerMillion,
			rate.RequestMicrounits, extra, rate.SourceReference, rate.EffectiveFrom, rate.EffectiveTo).Scan(&existingID)
		if err == nil {
			continue
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return updated, err
		}
		if rate.EffectiveTo == "" {
			if _, err := tx.Exec(`UPDATE provider_rates
				SET effective_to=CASE WHEN effective_from > CURRENT_TIMESTAMP THEN effective_from ELSE CURRENT_TIMESTAMP END,
				    updated_at=CURRENT_TIMESTAMP
				WHERE project_id=? AND provider=? AND model_id=? AND source='builtin_catalog' AND effective_to IS NULL`,
				projectID, rate.Provider, rate.ModelID); err != nil {
				return updated, err
			}
		}
		_, err = tx.Exec(`INSERT INTO provider_rates (
			project_id, provider, model_id, currency, input_microunits_per_million,
			output_microunits_per_million, cached_input_microunits_per_million,
			cache_write_microunits_per_million, request_microunits, extra_rates_json,
			source, source_reference, effective_from, effective_to)
			VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, 'builtin_catalog', ?, ?, NULLIF(?,''))`,
			projectID, rate.Provider, rate.ModelID, rate.Currency,
			rate.InputMicrounitsPerMillion, rate.OutputMicrounitsPerMillion,
			rate.CachedInputMicrounitsPerMillion, rate.CacheWriteMicrounitsPerMillion,
			rate.RequestMicrounits, extra, rate.SourceReference, rate.EffectiveFrom, rate.EffectiveTo)
		if err != nil {
			return updated, err
		}
		updated++
	}
	if err := tx.Commit(); err != nil {
		return updated, err
	}
	return updated, nil
}

func extraRateInt(rate *ProviderRate, key string) int64 {
	if rate == nil || len(rate.ExtraRates) == 0 {
		return 0
	}
	var extra map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(rate.ExtraRates)))
	decoder.UseNumber()
	if decoder.Decode(&extra) != nil {
		return 0
	}
	return int64FromDecimal(extra[key])
}
