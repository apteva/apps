package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const providerRateColumns = `id, project_id, provider, model_id, currency,
	input_microunits_per_million, output_microunits_per_million,
	cached_input_microunits_per_million, cache_write_microunits_per_million,
	request_microunits, extra_rates_json, source, source_reference,
	COALESCE(effective_from,''), COALESCE(effective_to,''), COALESCE(created_at,''), COALESCE(updated_at,'')`

type ProviderRate struct {
	ID                              int64           `json:"id"`
	ProjectID                       string          `json:"project_id"`
	Provider                        string          `json:"provider"`
	ModelID                         string          `json:"model_id"`
	Currency                        string          `json:"currency"`
	InputMicrounitsPerMillion       int64           `json:"input_microunits_per_million"`
	OutputMicrounitsPerMillion      int64           `json:"output_microunits_per_million"`
	CachedInputMicrounitsPerMillion int64           `json:"cached_input_microunits_per_million"`
	CacheWriteMicrounitsPerMillion  int64           `json:"cache_write_microunits_per_million"`
	RequestMicrounits               int64           `json:"request_microunits"`
	ExtraRates                      json.RawMessage `json:"extra_rates,omitempty"`
	Source                          string          `json:"source"`
	SourceReference                 string          `json:"source_reference,omitempty"`
	EffectiveFrom                   string          `json:"effective_from"`
	EffectiveTo                     string          `json:"effective_to,omitempty"`
	CreatedAt                       string          `json:"created_at,omitempty"`
	UpdatedAt                       string          `json:"updated_at,omitempty"`
}

func (r *ProviderRate) hasMeteredRate() bool {
	return r != nil
}

type ProviderMetering struct {
	InputTokens        int64           `json:"input_tokens"`
	OutputTokens       int64           `json:"output_tokens"`
	CachedInputTokens  int64           `json:"cached_input_tokens,omitempty"`
	CacheWriteTokens   int64           `json:"cache_write_tokens,omitempty"`
	CacheWrite1hTokens int64           `json:"cache_write_1h_tokens,omitempty"`
	CostMicrounits     int64           `json:"provider_cost_microunits,omitempty"`
	CostCurrency       string          `json:"provider_cost_currency,omitempty"`
	CostReported       bool            `json:"-"`
	RawUsage           json.RawMessage `json:"raw_usage,omitempty"`
}

func ensureProviderCostSchema(tx *sql.Tx) error {
	if _, err := tx.Exec(`CREATE TABLE IF NOT EXISTS provider_rates (
		id INTEGER PRIMARY KEY,
		project_id TEXT NOT NULL DEFAULT '',
		provider TEXT NOT NULL,
		model_id TEXT NOT NULL,
		currency TEXT NOT NULL DEFAULT 'USD',
		input_microunits_per_million INTEGER NOT NULL DEFAULT 0,
		output_microunits_per_million INTEGER NOT NULL DEFAULT 0,
		cached_input_microunits_per_million INTEGER NOT NULL DEFAULT 0,
		cache_write_microunits_per_million INTEGER NOT NULL DEFAULT 0,
		request_microunits INTEGER NOT NULL DEFAULT 0,
		extra_rates_json TEXT NOT NULL DEFAULT '{}',
		source TEXT NOT NULL DEFAULT 'manual',
		source_reference TEXT NOT NULL DEFAULT '',
		effective_from TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		effective_to TIMESTAMP,
		created_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP,
		updated_at TIMESTAMP NOT NULL DEFAULT CURRENT_TIMESTAMP
	)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE INDEX IF NOT EXISTS ix_provider_rates_lookup
		ON provider_rates(project_id, provider, model_id, effective_from DESC)`); err != nil {
		return err
	}
	if _, err := tx.Exec(`CREATE UNIQUE INDEX IF NOT EXISTS ux_provider_rates_active
		ON provider_rates(project_id, provider, model_id) WHERE effective_to IS NULL`); err != nil {
		return err
	}
	for _, col := range []struct{ name, decl string }{
		{"provider_cost_microunits", `INTEGER NOT NULL DEFAULT 0`},
		{"provider_cost_currency", `TEXT NOT NULL DEFAULT 'USD'`},
		{"provider_cost_status", `TEXT NOT NULL DEFAULT 'unpriced'`},
		{"provider_cost_source", `TEXT NOT NULL DEFAULT ''`},
		{"provider_rate_id", `INTEGER NOT NULL DEFAULT 0`},
		{"usage_details_json", `TEXT NOT NULL DEFAULT '{}'`},
	} {
		has, err := txTableHasColumn(tx, "usage_events", col.name)
		if err != nil {
			return err
		}
		if !has {
			if _, err := tx.Exec(`ALTER TABLE usage_events ADD COLUMN ` + col.name + ` ` + col.decl); err != nil {
				return err
			}
		}
	}
	_, err := tx.Exec(`UPDATE usage_events
		SET provider_cost_microunits = estimated_cost_cents * 10000,
			provider_cost_status = CASE WHEN estimated_cost_cents > 0 THEN 'calculated' ELSE provider_cost_status END,
			provider_cost_source = CASE WHEN estimated_cost_cents > 0 THEN 'legacy' ELSE provider_cost_source END
		WHERE provider_cost_microunits = 0 AND estimated_cost_cents > 0`)
	return err
}

type providerRateScanner interface{ Scan(dest ...any) error }

func scanProviderRate(row providerRateScanner) (*ProviderRate, error) {
	var rate ProviderRate
	var extra string
	if err := row.Scan(&rate.ID, &rate.ProjectID, &rate.Provider, &rate.ModelID, &rate.Currency,
		&rate.InputMicrounitsPerMillion, &rate.OutputMicrounitsPerMillion,
		&rate.CachedInputMicrounitsPerMillion, &rate.CacheWriteMicrounitsPerMillion,
		&rate.RequestMicrounits, &extra, &rate.Source, &rate.SourceReference,
		&rate.EffectiveFrom, &rate.EffectiveTo, &rate.CreatedAt, &rate.UpdatedAt); err != nil {
		return nil, err
	}
	rate.ExtraRates = json.RawMessage(firstNonEmpty(extra, "{}"))
	return &rate, nil
}

func dbProviderRatesList(db *sql.DB, projectID, provider, modelID string, includeHistory bool) ([]ProviderRate, error) {
	conds := []string{"project_id IN ('', ?)"}
	args := []any{projectID}
	if provider = normalizeProvider(provider); provider != "" {
		conds = append(conds, "provider=?")
		args = append(args, provider)
	}
	if modelID = strings.TrimSpace(modelID); modelID != "" {
		conds = append(conds, "model_id=?")
		args = append(args, upstreamModel(provider, modelID))
	}
	if !includeHistory {
		conds = append(conds, "effective_from <= CURRENT_TIMESTAMP", "(effective_to IS NULL OR effective_to > CURRENT_TIMESTAMP)")
	}
	rows, err := db.Query(`SELECT `+providerRateColumns+` FROM provider_rates WHERE `+strings.Join(conds, " AND ")+
		` ORDER BY provider, model_id, CASE WHEN project_id=? THEN 0 ELSE 1 END, effective_from DESC, id DESC`, append(args, projectID)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []ProviderRate{}
	for rows.Next() {
		rate, err := scanProviderRate(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *rate)
	}
	return out, rows.Err()
}

func resolveProviderRate(q queryRower, projectID, provider, model string) (*ProviderRate, error) {
	return resolveProviderRateAt(q, projectID, provider, model, "")
}

func resolveProviderRateAt(q queryRower, projectID, provider, model, at string) (*ProviderRate, error) {
	provider = normalizeProvider(provider)
	modelID := upstreamModel(provider, strings.TrimSpace(model))
	row := q.QueryRow(`SELECT `+providerRateColumns+` FROM provider_rates
		WHERE project_id IN ('', ?) AND provider=? AND model_id=?
		  AND effective_from <= COALESCE(NULLIF(?,''), CURRENT_TIMESTAMP)
		  AND (effective_to IS NULL OR effective_to > COALESCE(NULLIF(?,''), CURRENT_TIMESTAMP))
		ORDER BY CASE WHEN project_id=? THEN 0 ELSE 1 END,
			CASE source WHEN 'manual' THEN 0 WHEN 'provider_api' THEN 1 WHEN 'builtin_catalog' THEN 2 ELSE 3 END,
			effective_from DESC, id DESC LIMIT 1`,
		projectID, provider, modelID, at, at, projectID)
	rate, err := scanProviderRate(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return rate, err
}

func dbProviderRateUpsert(db *sql.DB, projectID string, args map[string]any, automatic bool) (*ProviderRate, bool, error) {
	provider := normalizeProvider(strArg(args, "provider"))
	modelID := upstreamModel(provider, strArg(args, "model_id"))
	if provider == "" || modelID == "" {
		return nil, false, errors.New("provider and model_id are required")
	}
	currency := strings.ToUpper(firstNonEmpty(strArg(args, "currency"), "USD"))
	if len(currency) != 3 {
		return nil, false, errors.New("currency must be a three-letter code")
	}
	source := firstNonEmpty(strArg(args, "source"), "manual")
	extra := jsonFromAny(args["extra_rates"])
	if len(extra) == 0 {
		extra = json.RawMessage(`{}`)
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, false, err
	}
	defer tx.Rollback()
	if automatic {
		current, currentErr := scanProviderRate(tx.QueryRow(`SELECT `+providerRateColumns+` FROM provider_rates
			WHERE project_id=? AND provider=? AND model_id=? AND effective_to IS NULL LIMIT 1`, projectID, provider, modelID))
		if currentErr == nil && current.Source == "manual" {
			return current, false, nil
		}
		if currentErr == nil && providerRateMatchesArgs(current, currency, args, source, strArg(args, "source_reference")) {
			return current, false, nil
		}
		if currentErr != nil && !errors.Is(currentErr, sql.ErrNoRows) {
			return nil, false, currentErr
		}
	}
	if _, err := tx.Exec(`UPDATE provider_rates SET effective_to=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND provider=? AND model_id=? AND effective_to IS NULL`, projectID, provider, modelID); err != nil {
		return nil, false, err
	}
	res, err := tx.Exec(`INSERT INTO provider_rates (
		project_id, provider, model_id, currency, input_microunits_per_million,
		output_microunits_per_million, cached_input_microunits_per_million,
		cache_write_microunits_per_million, request_microunits, extra_rates_json,
		source, source_reference, effective_from)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)`,
		projectID, provider, modelID, currency,
		int64Arg(args, "input_microunits_per_million"), int64Arg(args, "output_microunits_per_million"),
		int64Arg(args, "cached_input_microunits_per_million"), int64Arg(args, "cache_write_microunits_per_million"),
		int64Arg(args, "request_microunits"), string(extra), source, strArg(args, "source_reference"))
	if err != nil {
		return nil, false, err
	}
	id, _ := res.LastInsertId()
	row := tx.QueryRow(`SELECT `+providerRateColumns+` FROM provider_rates WHERE id=?`, id)
	rate, err := scanProviderRate(row)
	if err != nil {
		return nil, false, err
	}
	if err := tx.Commit(); err != nil {
		return nil, false, err
	}
	return rate, true, nil
}

func providerRateMatchesArgs(rate *ProviderRate, currency string, args map[string]any, source, sourceReference string) bool {
	return rate != nil && rate.Currency == currency && rate.Source == source && rate.SourceReference == sourceReference &&
		rate.InputMicrounitsPerMillion == int64Arg(args, "input_microunits_per_million") &&
		rate.OutputMicrounitsPerMillion == int64Arg(args, "output_microunits_per_million") &&
		rate.CachedInputMicrounitsPerMillion == int64Arg(args, "cached_input_microunits_per_million") &&
		rate.CacheWriteMicrounitsPerMillion == int64Arg(args, "cache_write_microunits_per_million") &&
		rate.RequestMicrounits == int64Arg(args, "request_microunits")
}

func dbProviderRateDelete(db *sql.DB, projectID string, id int64) (bool, error) {
	res, err := db.Exec(`UPDATE provider_rates SET effective_to=CURRENT_TIMESTAMP, updated_at=CURRENT_TIMESTAMP
		WHERE id=? AND project_id=? AND effective_to IS NULL`, id, projectID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (a *App) refreshProviderRates(db *sql.DB, projectID, provider string) (int, error) {
	models, err := dbProviderModelsList(db, providerModelFilter{ProjectID: projectID, Provider: normalizeProvider(provider), Status: "active"})
	if err != nil {
		return 0, err
	}
	updated := 0
	for _, model := range models {
		args, ok := rateArgsFromModel(model)
		if ok {
			if _, changed, err := dbProviderRateUpsert(db, projectID, args, true); err != nil {
				return updated, err
			} else if changed {
				updated++
			}
			continue
		}
		count, err := materializeBuiltinCatalogRates(db, projectID, model.Provider, model.ModelID)
		if err != nil {
			return updated, err
		}
		updated += count
	}
	return updated, nil
}

func rateArgsFromModel(model ProviderModel) (map[string]any, bool) {
	var raw map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(model.Raw)))
	decoder.UseNumber()
	if decoder.Decode(&raw) != nil {
		return nil, false
	}
	input, inputOK := firstPrice(raw,
		pricePath{[]string{"pricing", "prompt"}, "per_token"},
		pricePath{[]string{"pricing", "input"}, "per_token"},
		pricePath{[]string{"input_price_per_million"}, "per_million"},
		pricePath{[]string{"input_cost_per_million"}, "per_million"})
	output, outputOK := firstPrice(raw,
		pricePath{[]string{"pricing", "completion"}, "per_token"},
		pricePath{[]string{"pricing", "output"}, "per_token"},
		pricePath{[]string{"output_price_per_million"}, "per_million"},
		pricePath{[]string{"output_cost_per_million"}, "per_million"})
	request, requestOK := firstPrice(raw,
		pricePath{[]string{"pricing", "request"}, "per_request"},
		pricePath{[]string{"request_price"}, "per_request"})
	if !inputOK && !outputOK && !requestOK {
		return nil, false
	}
	return map[string]any{
		"provider": model.Provider, "model_id": model.ModelID, "currency": "USD",
		"input_microunits_per_million": input, "output_microunits_per_million": output,
		"request_microunits": request, "source": "provider_api",
		"source_reference": "models_response",
	}, true
}

type pricePath struct {
	parts []string
	unit  string
}

func firstPrice(raw map[string]any, paths ...pricePath) (int64, bool) {
	for _, path := range paths {
		value, ok := nestedValue(raw, path.parts...)
		if !ok {
			continue
		}
		n, ok := decimalValue(value)
		if !ok || n < 0 {
			continue
		}
		switch path.unit {
		case "per_token":
			return clampRounded(n * 1_000_000 * 1_000_000), true
		case "per_million":
			return clampRounded(n * 1_000_000), true
		default:
			return clampRounded(n * 1_000_000), true
		}
	}
	return 0, false
}

func nestedValue(raw map[string]any, path ...string) (any, bool) {
	var current any = raw
	for _, part := range path {
		m, ok := current.(map[string]any)
		if !ok {
			return nil, false
		}
		current, ok = m[part]
		if !ok {
			return nil, false
		}
	}
	return current, true
}

func decimalValue(value any) (float64, bool) {
	switch v := value.(type) {
	case json.Number:
		n, err := v.Float64()
		return n, err == nil
	case float64:
		return v, true
	case string:
		n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		return n, err == nil
	default:
		return 0, false
	}
}

func clampRounded(value float64) int64 {
	if value <= 0 {
		return 0
	}
	if value >= math.MaxInt64 {
		return math.MaxInt64
	}
	return int64(math.Round(value))
}

func calculateProviderCost(rate *ProviderRate, input, output, cachedInput, cacheWrite int64) int64 {
	return calculateProviderCostDetailed(rate, input, output, cachedInput, cacheWrite, 0)
}

func calculateProviderCostDetailed(rate *ProviderRate, input, output, cachedInput, cacheWrite, cacheWrite1h int64) int64 {
	if !rate.hasMeteredRate() {
		return 0
	}
	if cacheWrite1h < 0 {
		cacheWrite1h = 0
	}
	if cacheWrite1h > cacheWrite {
		cacheWrite1h = cacheWrite
	}
	cacheWrite5m := cacheWrite - cacheWrite1h
	cost := float64(rate.RequestMicrounits)
	cost += float64(input) * float64(rate.InputMicrounitsPerMillion) / 1_000_000
	cost += float64(output) * float64(rate.OutputMicrounitsPerMillion) / 1_000_000
	cost += float64(cachedInput) * float64(rate.CachedInputMicrounitsPerMillion) / 1_000_000
	cost += float64(cacheWrite5m) * float64(rate.CacheWriteMicrounitsPerMillion) / 1_000_000
	cacheWrite1hRate := extraRateInt(rate, "cache_write_1h_microunits_per_million")
	if cacheWrite1hRate == 0 {
		cacheWrite1hRate = rate.CacheWriteMicrounitsPerMillion
	}
	cost += float64(cacheWrite1h) * float64(cacheWrite1hRate) / 1_000_000
	return clampRounded(cost)
}

func parseProviderMetering(raw []byte) ProviderMetering {
	var payload map[string]any
	decoder := json.NewDecoder(strings.NewReader(string(raw)))
	decoder.UseNumber()
	if decoder.Decode(&payload) != nil {
		return ProviderMetering{}
	}
	usage, _ := payload["usage"].(map[string]any)
	m := ProviderMetering{
		InputTokens:  int64FromDecimal(firstNonNil(usage["prompt_tokens"], usage["input_tokens"])),
		OutputTokens: int64FromDecimal(firstNonNil(usage["completion_tokens"], usage["output_tokens"])),
		CostCurrency: "USD",
	}
	if details, ok := usage["prompt_tokens_details"].(map[string]any); ok {
		m.CachedInputTokens = int64FromDecimal(details["cached_tokens"])
	}
	if m.CachedInputTokens == 0 {
		m.CachedInputTokens = int64FromDecimal(firstNonNil(usage["cache_read_input_tokens"], usage["cached_input_tokens"]))
	}
	m.CacheWriteTokens = int64FromDecimal(firstNonNil(usage["cache_creation_input_tokens"], usage["cache_write_tokens"]))
	if cacheCreation, ok := usage["cache_creation"].(map[string]any); ok {
		m.CacheWrite1hTokens = int64FromDecimal(cacheCreation["ephemeral_1h_input_tokens"])
	}
	if b, err := json.Marshal(usage); err == nil && len(usage) > 0 {
		m.RawUsage = b
	}
	for _, candidate := range []any{usage["cost"], usage["total_cost"], payload["cost"]} {
		if dollars, ok := decimalValue(candidate); ok && dollars >= 0 {
			m.CostMicrounits = clampRounded(dollars * 1_000_000)
			m.CostReported = true
			break
		}
	}
	return m
}

func int64FromDecimal(v any) int64 {
	n, ok := decimalValue(v)
	if !ok || n <= 0 {
		return 0
	}
	return clampRounded(n)
}

func (a *App) handleProviderRates(w http.ResponseWriter, r *http.Request) {
	projectID := projectFromRequest(r)
	switch r.Method {
	case http.MethodGet:
		rows, err := dbProviderRatesList(globalCtx.AppDB(), projectID, r.URL.Query().Get("provider"), r.URL.Query().Get("model_id"), r.URL.Query().Get("include_history") == "true")
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"provider_rates": rows})
	case http.MethodPut, http.MethodPost:
		var args map[string]any
		if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
			http.Error(w, "invalid json", http.StatusBadRequest)
			return
		}
		args["source"] = "manual"
		rate, _, err := dbProviderRateUpsert(globalCtx.AppDB(), projectID, args, false)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		writeJSON(w, map[string]any{"provider_rate": rate})
	case http.MethodDelete:
		id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
		if id <= 0 {
			http.Error(w, "id is required", http.StatusBadRequest)
			return
		}
		deleted, err := dbProviderRateDelete(globalCtx.AppDB(), projectID, id)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}
		writeJSON(w, map[string]any{"deleted": deleted})
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleProviderRatesRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var args map[string]any
	_ = json.NewDecoder(r.Body).Decode(&args)
	count, err := a.refreshProviderRates(globalCtx.AppDB(), projectFromRequest(r), strArg(args, "provider"))
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"updated": count})
}

func (a *App) toolProviderRatesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	rows, err := dbProviderRatesList(ctx.AppDB(), projectFromArgs(args), strArg(args, "provider"), strArg(args, "model_id"), boolArg(args, "include_history"))
	return map[string]any{"provider_rates": rows}, err
}

func (a *App) toolProviderRateUpsert(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	args["source"] = "manual"
	rate, _, err := dbProviderRateUpsert(ctx.AppDB(), projectFromArgs(args), args, false)
	return map[string]any{"provider_rate": rate}, err
}

func (a *App) toolProviderRateDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	deleted, err := dbProviderRateDelete(ctx.AppDB(), projectFromArgs(args), int64Arg(args, "id"))
	return map[string]any{"deleted": deleted}, err
}

func (a *App) toolProviderRatesRefresh(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	count, err := a.refreshProviderRates(ctx.AppDB(), projectFromArgs(args), strArg(args, "provider"))
	return map[string]any{"updated": count}, err
}

func providerCostDetails(m ProviderMetering) json.RawMessage {
	b, _ := json.Marshal(map[string]any{
		"input_tokens": m.InputTokens, "output_tokens": m.OutputTokens,
		"cached_input_tokens": m.CachedInputTokens, "cache_write_tokens": m.CacheWriteTokens,
		"cache_write_1h_tokens": m.CacheWrite1hTokens,
		"raw_usage":             rawJSON(string(m.RawUsage)),
	})
	return b
}

func mergeUsageDetails(raw json.RawMessage, attempts []any, failedAttempts int) json.RawMessage {
	details := map[string]any{}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &details)
	}
	if len(attempts) > 0 {
		details["attempts"] = attempts
	}
	if failedAttempts > 0 {
		details["unknown_cost_attempts"] = failedAttempts
	}
	b, _ := json.Marshal(details)
	return b
}

func usageHasUnknownAttemptCost(raw json.RawMessage) bool {
	var details struct {
		UnknownCostAttempts int `json:"unknown_cost_attempts"`
	}
	return json.Unmarshal(raw, &details) == nil && details.UnknownCostAttempts > 0
}

func providerCostCalculationPartial(rate *ProviderRate, input, cacheWrite1h int64) bool {
	if rate == nil {
		return false
	}
	if maxStandardContext := extraRateInt(rate, "standard_context_max_tokens"); maxStandardContext > 0 && input > maxStandardContext {
		return true
	}
	return cacheWrite1h > 0 && extraRateInt(rate, "cache_write_1h_microunits_per_million") == 0
}

func costHeaders(w http.ResponseWriter, ev *UsageEvent) {
	if ev == nil {
		return
	}
	w.Header().Set("X-Apteva-Input-Tokens", strconv.FormatInt(ev.RequestTokens, 10))
	w.Header().Set("X-Apteva-Output-Tokens", strconv.FormatInt(ev.ResponseTokens, 10))
	w.Header().Set("X-Apteva-Provider-Cost-Microunits", strconv.FormatInt(ev.ProviderCostMicrounits, 10))
	w.Header().Set("X-Apteva-Provider-Cost-Currency", ev.ProviderCostCurrency)
	w.Header().Set("X-Apteva-Provider-Cost-Status", ev.ProviderCostStatus)
}

func validateCostCurrency(policies []*Policy) error {
	for _, policy := range policies {
		currency := strings.ToUpper(firstNonEmpty(policy.Limits.ProviderCostCurrency, "USD"))
		if policy.Limits.MonthlyProviderCostLimitMicrounits > 0 && currency != "USD" {
			return fmt.Errorf("provider cost limits currently require USD rates")
		}
	}
	return nil
}

func smallestPolicyOutputReservation(policies []*Policy, requested int64) int64 {
	if requested > 0 {
		return requested
	}
	var out int64
	for _, policy := range policies {
		out = minPositive(out, policy.Limits.MaxTokensPerRequest)
	}
	return out
}

func unpricedAllowed(policies []*Policy) bool {
	for _, policy := range policies {
		if policy.Limits.MonthlyProviderCostLimitMicrounits <= 0 {
			continue
		}
		if !strings.EqualFold(policy.Limits.UnpricedModelBehavior, "allow") {
			return false
		}
	}
	return true
}

func utcNow() string { return time.Now().UTC().Format(time.RFC3339Nano) }
