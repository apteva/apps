package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func (a *App) MCPTools() []sdk.Tool {
	selectionProps := rateSelectionProps()
	convertProps := cloneProps(selectionProps)
	convertProps["amount_minor"] = map[string]any{"description": "Signed source amount in minor units; integer or decimal string.", "anyOf": []any{map[string]any{"type": "integer"}, map[string]any{"type": "string", "pattern": "^-?[0-9]+$"}}}
	convertProps["from"] = map[string]any{"type": "string"}
	convertProps["to"] = map[string]any{"type": "string"}
	delete(convertProps, "base")
	delete(convertProps, "quote")
	convertProps["rounding"] = map[string]any{"type": "string", "enum": []string{"half_even", "half_up", "down", "up"}}
	return []sdk.Tool{
		{Name: "currencies_list", Description: "List seeded ISO 4217 currency definitions. Args: q?, kind?, active?, limit?.", InputSchema: schemaObject(map[string]any{
			"q": typ("string"), "kind": enum("fiat", "fund", "metal", "special"), "active": typ("boolean"), "limit": typ("integer"),
		}, nil), Handler: a.toolCurrenciesList},
		{Name: "currencies_get", Description: "Get one ISO 4217 currency definition and minor-unit exponent. Args: code.", InputSchema: schemaObject(map[string]any{"code": typ("string")}, []string{"code"}), Handler: a.toolCurrenciesGet},
		{Name: "currencies_rate_get", Description: "Get a current or historical direct, inverse, or two-edge triangulated FX rate with provenance.", InputSchema: schemaObject(selectionProps, []string{"base", "quote"}), Handler: a.toolRateGet},
		{Name: "currencies_convert", Description: "Convert a signed integer minor-unit amount using exact arithmetic, explicit rounding, and a reproducible rate snapshot.", InputSchema: schemaObject(convertProps, []string{"amount_minor", "from", "to"}), Handler: a.toolConvert},
		{Name: "currencies_rates_history", Description: "List immutable normalized observations for a pair and date range.", InputSchema: schemaObject(map[string]any{
			"base": typ("string"), "quote": typ("string"), "from": typ("string"), "to": typ("string"),
			"providers": arrayStrings(), "rate_kinds": arrayStrings(), "limit": typ("integer"),
		}, []string{"base", "quote"}), Handler: a.toolRatesHistory},
		{Name: "currencies_rate_set_manual", Description: "Append an auditable manual exchange-rate observation. Existing observations are never overwritten.", InputSchema: schemaObject(map[string]any{
			"base": typ("string"), "quote": typ("string"), "rate": map[string]any{"description": "Positive plain decimal string preferred.", "anyOf": []any{typ("string"), typ("number")}},
			"effective_at": typ("string"), "reason": typ("string"), "source_ref": typ("string"), "supersedes_rate_id": typ("integer"),
		}, []string{"base", "quote", "rate", "reason"}), Handler: a.toolRateSetManual},
		{Name: "currencies_sources_status", Description: "List bound FX providers, health, tracked pairs, and latest observations.", InputSchema: schemaObject(map[string]any{}, nil), Handler: a.toolSourcesStatus},
		{Name: "currencies_sync_now", Description: "Fetch and ingest provider observations for one pair or every tracked pair.", InputSchema: schemaObject(map[string]any{
			"base": typ("string"), "quote": typ("string"), "as_of": typ("string"), "provider": typ("string"),
		}, nil), Handler: a.toolSyncNow},
	}
}

func rateSelectionProps() map[string]any {
	return map[string]any{
		"base": typ("string"), "quote": typ("string"), "as_of": typ("string"),
		"selection":  enum("latest_on_or_before", "exact_date"),
		"rate_kinds": arrayStrings(), "providers": arrayStrings(), "max_age_seconds": typ("integer"),
		"allow_inverse": typ("boolean"), "allow_triangulation": typ("boolean"),
		"allow_stale": typ("boolean"), "fetch": typ("boolean"),
	}
}

func (a *App) toolCurrenciesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	var active *bool
	if _, ok := args["active"]; ok {
		v := boolArg(args, "active", true)
		active = &v
	}
	rows, err := listCurrencies(ctx.AppDB(), stringArg(args, "q"), stringArg(args, "kind"), active, intArg(args, "limit", 250))
	if err != nil {
		return nil, err
	}
	return map[string]any{"currencies": rows, "count": len(rows)}, nil
}

func (a *App) toolCurrenciesGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	c, err := getCurrency(ctx.AppDB(), stringArg(args, "code"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"currency": c}, nil
}

func (a *App) toolRateGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	req, err := selectionRequest(ctx, args, "base", "quote")
	if err != nil {
		return nil, err
	}
	return a.selectRate(ctx, req)
}

func (a *App) toolConvert(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	amount, err := int64Arg(args, "amount_minor")
	if err != nil {
		return nil, err
	}
	convertedArgs := cloneArgs(args)
	convertedArgs["base"] = stringArg(args, "from")
	convertedArgs["quote"] = stringArg(args, "to")
	req, err := selectionRequest(ctx, convertedArgs, "base", "quote")
	if err != nil {
		return nil, err
	}
	q, err := a.selectRate(ctx, req)
	if err != nil {
		return nil, err
	}
	rounding := strings.ToLower(strings.TrimSpace(stringArg(args, "rounding")))
	if rounding == "" {
		rounding = "half_even"
	}
	return convertWithQuote(ctx.AppDB(), amount, req.Base, req.Quote, rounding, q)
}

func (a *App) toolRatesHistory(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	base, err := normalizeCode(stringArg(args, "base"))
	if err != nil {
		return nil, err
	}
	quote, err := normalizeCode(stringArg(args, "quote"))
	if err != nil {
		return nil, err
	}
	if _, err := getCurrency(ctx.AppDB(), base); err != nil {
		return nil, err
	}
	if _, err := getCurrency(ctx.AppDB(), quote); err != nil {
		return nil, err
	}
	from, err := optionalBoundary(stringArg(args, "from"), false)
	if err != nil {
		return nil, fmt.Errorf("from: %w", err)
	}
	to, err := optionalBoundary(stringArg(args, "to"), true)
	if err != nil {
		return nil, fmt.Errorf("to: %w", err)
	}
	rows, err := historyObservations(ctx.AppDB(), scopeKey(ctx, args), base, quote, from, to,
		lowerStrings(stringSliceArg(args, "providers")), lowerStrings(stringSliceArg(args, "rate_kinds")), intArg(args, "limit", 500))
	if err != nil {
		return nil, err
	}
	return map[string]any{"base": base, "quote": quote, "observations": rows, "count": len(rows)}, nil
}

func (a *App) toolRateSetManual(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	base, err := normalizeCode(stringArg(args, "base"))
	if err != nil {
		return nil, err
	}
	quote, err := normalizeCode(stringArg(args, "quote"))
	if err != nil {
		return nil, err
	}
	if _, err := getCurrency(ctx.AppDB(), base); err != nil {
		return nil, err
	}
	if _, err := getCurrency(ctx.AppDB(), quote); err != nil {
		return nil, err
	}
	reason := strings.TrimSpace(stringArg(args, "reason"))
	if reason == "" {
		return nil, errors.New("reason is required for a manual rate")
	}
	rate, err := decimalArg(args, "rate")
	if err != nil {
		return nil, err
	}
	effective := time.Now().UTC()
	if raw := strings.TrimSpace(stringArg(args, "effective_at")); raw != "" {
		effective, err = parseFlexibleTime(raw)
		if err != nil {
			return nil, fmt.Errorf("effective_at: %w", err)
		}
	}
	projectID := scopeKey(ctx, args)
	supersedesID := int64(intArg(args, "supersedes_rate_id", 0))
	if supersedesID != 0 {
		old, err := getObservation(ctx.AppDB(), projectID, supersedesID)
		if err != nil {
			return nil, fmt.Errorf("supersedes_rate_id: %w", err)
		}
		if old.Base != base || old.Quote != quote {
			return nil, errors.New("superseded observation must use the same base and quote currencies")
		}
	}
	obs, created, err := insertObservation(ctx.AppDB(), projectID, ObservationInput{
		Base: base, Quote: quote, Rate: rate, RateKind: "manual", EffectiveAt: effective,
		EffectiveDate: effective.UTC().Format("2006-01-02"), Granularity: "instant",
		ObservedAt: time.Now().UTC(), ProviderSlug: "manual", ProviderRef: stringArg(args, "source_ref"),
		OriginalBase: base, OriginalQuote: quote, AdapterVersion: "manual-v1", QualityFlags: []string{"manual"},
	})
	if err != nil {
		return nil, err
	}
	if created {
		if supersedesID != 0 {
			if _, err := ctx.AppDB().Exec(`UPDATE rate_observations SET supersedes_rate_id=? WHERE project_id=? AND id=?`, supersedesID, projectID, obs.ID); err != nil {
				_, _ = ctx.AppDB().Exec(`DELETE FROM rate_observations WHERE project_id=? AND id=?`, projectID, obs.ID)
				return nil, err
			}
			obs.SupersedesRateID = &supersedesID
		}
		_, err = ctx.AppDB().Exec(`INSERT INTO manual_rate_audit(project_id,rate_id,reason,source_ref) VALUES(?,?,?,?)`,
			projectID, obs.ID, reason, stringArg(args, "source_ref"))
		if err != nil {
			_, _ = ctx.AppDB().Exec(`DELETE FROM rate_observations WHERE project_id=? AND id=?`, projectID, obs.ID)
			return nil, err
		}
		ctx.Emit("currencies.manual_rate.created", map[string]any{"rate_id": obs.ID, "base": base, "quote": quote})
	}
	_ = trackPair(ctx.AppDB(), projectID, base, quote)
	return map[string]any{"observation": obs, "created": created}, nil
}

func (a *App) toolSourcesStatus(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	projectID := scopeKey(ctx, args)
	connections, connErr := providerConnections(ctx, projectID)
	priority := providerPriority(ctx)
	statuses := []ProviderStatus{}
	ecbObservations := 0
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM rate_observations WHERE project_id=? AND provider_slug=?`,
		projectID, ecbProviderSlug).Scan(&ecbObservations)
	if configBool(ctx, "ecb_bootstrap_enabled", true) {
		ecb := ProviderStatus{
			ConnectionID: 0, Provider: ecbProviderSlug, Name: ecbProviderName,
			Status: "public", Priority: priority[ecbProviderSlug], Enabled: true,
		}
		var enabled int
		err := ctx.AppDB().QueryRow(`SELECT priority,enabled,COALESCE(last_attempt_at,''),COALESCE(last_success_at,''),last_error,failure_count
            FROM provider_health WHERE project_id=? AND connection_id=0 AND provider_slug=?`, projectID, ecbProviderSlug).Scan(
			&ecb.Priority, &enabled, &ecb.LastAttemptAt, &ecb.LastSuccessAt, &ecb.LastError, &ecb.FailureCount)
		if err == nil {
			ecb.Enabled = enabled != 0
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		statuses = append(statuses, ecb)
	}
	for _, conn := range connections {
		s := ProviderStatus{ConnectionID: conn.ID, Provider: conn.AppSlug, Name: conn.Name, Status: conn.Status, Priority: priority[conn.AppSlug], Enabled: true}
		var enabled int
		err := ctx.AppDB().QueryRow(`SELECT priority,enabled,COALESCE(last_attempt_at,''),COALESCE(last_success_at,''),last_error,failure_count
            FROM provider_health WHERE project_id=? AND connection_id=?`, projectID, conn.ID).Scan(
			&s.Priority, &enabled, &s.LastAttemptAt, &s.LastSuccessAt, &s.LastError, &s.FailureCount)
		if err == nil {
			s.Enabled = enabled != 0
		} else if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
		statuses = append(statuses, s)
	}
	var tracked, observations int
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM tracked_pairs WHERE project_id=? AND enabled=1`, projectID).Scan(&tracked)
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM rate_observations WHERE project_id=?`, projectID).Scan(&observations)
	var newest string
	_ = ctx.AppDB().QueryRow(`SELECT COALESCE(MAX(effective_at),'') FROM rate_observations WHERE project_id=?`, projectID).Scan(&newest)
	out := map[string]any{
		"providers": statuses, "tracked_pairs": tracked, "observations": observations,
		"newest_effective_at": newest, "offline_manual_mode": len(connections) == 0 && ecbObservations == 0,
		"ecb_observations": ecbObservations,
	}
	if connErr != nil {
		out["connection_error"] = connErr.Error()
	}
	return out, nil
}

func (a *App) toolSyncNow(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	projectID := scopeKey(ctx, args)
	requestedProvider := strings.ToLower(strings.TrimSpace(stringArg(args, "provider")))
	if requestedProvider == ecbProviderSlug {
		syncCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		created, latest, err := a.refreshECBIfDue(syncCtx, ctx, true)
		cancel()
		if err != nil {
			return nil, err
		}
		return map[string]any{
			"results": []map[string]any{{
				"provider": ecbProviderSlug, "base": "EUR", "observations_created": created,
				"latest_effective_date": latest, "success": true,
			}},
			"count": 1,
		}, nil
	}
	asOf := time.Now().UTC()
	var err error
	if raw := strings.TrimSpace(stringArg(args, "as_of")); raw != "" {
		asOf, err = parseFlexibleTime(raw)
		if err != nil {
			return nil, fmt.Errorf("as_of: %w", err)
		}
	}
	baseRaw, quoteRaw := stringArg(args, "base"), stringArg(args, "quote")
	pairs := [][2]string{}
	if baseRaw != "" || quoteRaw != "" {
		if baseRaw == "" || quoteRaw == "" {
			return nil, errors.New("base and quote must be provided together")
		}
		base, err := normalizeCode(baseRaw)
		if err != nil {
			return nil, err
		}
		quote, err := normalizeCode(quoteRaw)
		if err != nil {
			return nil, err
		}
		if _, err := getCurrency(ctx.AppDB(), base); err != nil {
			return nil, err
		}
		if _, err := getCurrency(ctx.AppDB(), quote); err != nil {
			return nil, err
		}
		_ = trackPair(ctx.AppDB(), projectID, base, quote)
		pairs = append(pairs, [2]string{base, quote})
	} else {
		pairs, err = trackedPairs(ctx.AppDB(), projectID)
		if err != nil {
			return nil, err
		}
	}
	results := []map[string]any{}
	for _, pair := range pairs {
		created, fetchErr := a.fetchPair(ctx, projectID, pair[0], pair[1], asOf, requestedProvider)
		row := map[string]any{"base": pair[0], "quote": pair[1], "observations_created": len(created), "success": fetchErr == nil}
		if fetchErr != nil {
			row["error"] = fetchErr.Error()
		}
		results = append(results, row)
	}
	return map[string]any{"results": results, "count": len(results)}, nil
}

func selectionRequest(ctx *sdk.AppCtx, args map[string]any, baseKey, quoteKey string) (SelectionRequest, error) {
	base, err := normalizeCode(stringArg(args, baseKey))
	if err != nil {
		return SelectionRequest{}, err
	}
	quote, err := normalizeCode(stringArg(args, quoteKey))
	if err != nil {
		return SelectionRequest{}, err
	}
	if _, err := getCurrency(ctx.AppDB(), base); err != nil {
		return SelectionRequest{}, err
	}
	if _, err := getCurrency(ctx.AppDB(), quote); err != nil {
		return SelectionRequest{}, err
	}
	asOf := time.Now().UTC()
	if raw := strings.TrimSpace(stringArg(args, "as_of")); raw != "" {
		asOf, err = parseFlexibleTime(raw)
		if err != nil {
			return SelectionRequest{}, fmt.Errorf("as_of: %w", err)
		}
		if len(raw) == 10 {
			asOf = asOf.Add(24*time.Hour - time.Nanosecond)
		}
	}
	selection := strings.ToLower(strings.TrimSpace(stringArg(args, "selection")))
	if selection == "" {
		selection = "latest_on_or_before"
	}
	if selection != "latest_on_or_before" && selection != "exact_date" {
		return SelectionRequest{}, errors.New("selection must be latest_on_or_before or exact_date")
	}
	maxAgeSeconds := intArg(args, "max_age_seconds", configInt(ctx, "default_max_age_seconds", 259200))
	if maxAgeSeconds < 0 {
		return SelectionRequest{}, errors.New("max_age_seconds must be non-negative")
	}
	kinds := lowerStrings(stringSliceArg(args, "rate_kinds"))
	for _, kind := range kinds {
		if !contains([]string{"spot", "reference", "open", "high", "low", "close", "manual"}, kind) {
			return SelectionRequest{}, fmt.Errorf("unsupported rate_kind %q", kind)
		}
	}
	return SelectionRequest{
		ProjectID: scopeKey(ctx, args), Base: base, Quote: quote, AsOf: asOf, Selection: selection,
		RateKinds: kinds, Providers: lowerStrings(stringSliceArg(args, "providers")),
		MaxAge:       time.Duration(maxAgeSeconds) * time.Second,
		AllowInverse: boolArg(args, "allow_inverse", true), AllowTriangulation: boolArg(args, "allow_triangulation", true),
		AllowStale: boolArg(args, "allow_stale", false), Fetch: boolArg(args, "fetch", true),
	}, nil
}

func parseFlexibleTime(v string) (time.Time, error) {
	v = strings.TrimSpace(v)
	if t, err := time.Parse(time.RFC3339Nano, v); err == nil {
		return t.UTC(), nil
	}
	if t, err := time.Parse("2006-01-02", v); err == nil {
		return t.UTC(), nil
	}
	return time.Time{}, errors.New("must be RFC3339 or YYYY-MM-DD")
}

func optionalBoundary(v string, end bool) (string, error) {
	if strings.TrimSpace(v) == "" {
		return "", nil
	}
	t, err := parseFlexibleTime(v)
	if err != nil {
		return "", err
	}
	if len(strings.TrimSpace(v)) == 10 && end {
		t = t.Add(24*time.Hour - time.Nanosecond)
	}
	return t.UTC().Format(time.RFC3339Nano), nil
}

func decimalArg(args map[string]any, key string) (string, error) {
	v, ok := args[key]
	if !ok {
		return "", fmt.Errorf("%s is required", key)
	}
	var s string
	switch x := v.(type) {
	case string:
		s = x
	case float64:
		s = strconv.FormatFloat(x, 'f', -1, 64)
	case int:
		s = strconv.Itoa(x)
	case int64:
		s = strconv.FormatInt(x, 10)
	case json.Number:
		s = x.String()
	default:
		return "", fmt.Errorf("%s must be a decimal string or number", key)
	}
	_, canonical, err := parsePositiveDecimal(s)
	if err != nil {
		return "", fmt.Errorf("%s: %w", key, err)
	}
	return canonical, nil
}

func int64Arg(args map[string]any, key string) (int64, error) {
	v, ok := args[key]
	if !ok {
		return 0, fmt.Errorf("%s is required", key)
	}
	switch x := v.(type) {
	case int:
		return int64(x), nil
	case int64:
		return x, nil
	case float64:
		if math.IsNaN(x) || math.IsInf(x, 0) || math.Trunc(x) != x {
			return 0, fmt.Errorf("%s must be an integer", key)
		}
		// 2^63 is exactly representable as float64 but is one past MaxInt64.
		if x < -9223372036854775808.0 || x >= 9223372036854775808.0 {
			return 0, fmt.Errorf("%s is outside the signed 64-bit integer range", key)
		}
		return int64(x), nil
	case string:
		return strconv.ParseInt(strings.TrimSpace(x), 10, 64)
	case json.Number:
		return x.Int64()
	default:
		return 0, fmt.Errorf("%s must be an integer or decimal integer string", key)
	}
}

func stringArg(args map[string]any, key string) string {
	v, _ := args[key].(string)
	return strings.TrimSpace(v)
}

func stringSliceArg(args map[string]any, key string) []string {
	v, ok := args[key]
	if !ok || v == nil {
		return nil
	}
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := []string{}
		for _, item := range x {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
		return out
	case string:
		return splitCSV(x)
	}
	return nil
}

func lowerStrings(in []string) []string {
	out := make([]string, 0, len(in))
	for _, v := range in {
		if v = strings.ToLower(strings.TrimSpace(v)); v != "" {
			out = append(out, v)
		}
	}
	return out
}

func splitCSV(v string) []string {
	out := []string{}
	for _, item := range strings.Split(v, ",") {
		if item = strings.TrimSpace(item); item != "" {
			out = append(out, item)
		}
	}
	return out
}

func boolArg(args map[string]any, key string, fallback bool) bool {
	v, ok := args[key]
	if !ok {
		return fallback
	}
	switch x := v.(type) {
	case bool:
		return x
	case string:
		b, err := strconv.ParseBool(x)
		if err == nil {
			return b
		}
	}
	return fallback
}

func intArg(args map[string]any, key string, fallback int) int {
	v, ok := args[key]
	if !ok {
		return fallback
	}
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	case string:
		if n, err := strconv.Atoi(x); err == nil {
			return n
		}
	}
	return fallback
}

func configInt(ctx *sdk.AppCtx, key string, fallback int) int {
	if ctx == nil {
		return fallback
	}
	if n, err := strconv.Atoi(strings.TrimSpace(ctx.Config().Get(key))); err == nil && n >= 0 {
		return n
	}
	return fallback
}

func contains(values []string, needle string) bool {
	for _, v := range values {
		if v == needle {
			return true
		}
	}
	return false
}

func cloneArgs(in map[string]any) map[string]any {
	out := make(map[string]any, len(in)+2)
	for k, v := range in {
		out[k] = v
	}
	return out
}

func cloneProps(in map[string]any) map[string]any { return cloneArgs(in) }

func typ(t string) map[string]any          { return map[string]any{"type": t} }
func enum(values ...string) map[string]any { return map[string]any{"type": "string", "enum": values} }
func arrayStrings() map[string]any         { return map[string]any{"type": "array", "items": typ("string")} }
func schemaObject(props map[string]any, required []string) map[string]any {
	s := map[string]any{"type": "object", "properties": props, "additionalProperties": false}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}
