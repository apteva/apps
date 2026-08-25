package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

var compatibleProviderSlugs = []string{"alpaca-market-data", "saltedge", "alpha-vantage"}

func (a *App) fetchPair(ctx *sdk.AppCtx, projectID, base, quote string, asOf time.Time, requestedProvider string) ([]RateObservation, error) {
	connections, err := providerConnections(ctx, projectID)
	if err != nil {
		return nil, err
	}
	if requestedProvider != "" {
		requestedProvider = strings.ToLower(strings.TrimSpace(requestedProvider))
		filtered := connections[:0]
		for _, c := range connections {
			if c.AppSlug == requestedProvider {
				filtered = append(filtered, c)
			}
		}
		connections = filtered
	}
	if len(connections) == 0 {
		return nil, errors.New("no active FX-provider connection is bound")
	}

	var failures []string
	for _, conn := range connections {
		runID := startSyncRun(ctx.AppDB(), projectID, conn, base, quote)
		inputs, err := fetchProviderObservations(ctx, conn, base, quote, asOf)
		if err != nil {
			finishSyncRun(ctx.AppDB(), runID, "failed", 0, err.Error())
			updateProviderHealth(ctx.AppDB(), projectID, conn, false, err)
			failures = append(failures, conn.AppSlug+": "+err.Error())
			continue
		}
		created := []RateObservation{}
		for _, in := range inputs {
			o, wasCreated, insertErr := insertObservation(ctx.AppDB(), projectID, in)
			if insertErr != nil {
				err = insertErr
				break
			}
			if wasCreated {
				created = append(created, o)
			}
		}
		if err != nil {
			finishSyncRun(ctx.AppDB(), runID, "failed", len(created), err.Error())
			updateProviderHealth(ctx.AppDB(), projectID, conn, false, err)
			failures = append(failures, conn.AppSlug+": "+err.Error())
			continue
		}
		finishSyncRun(ctx.AppDB(), runID, "completed", len(created), "")
		updateProviderHealth(ctx.AppDB(), projectID, conn, true, nil)
		_, _ = ctx.AppDB().Exec(`UPDATE tracked_pairs SET last_refresh_at=CURRENT_TIMESTAMP,last_error=''
            WHERE project_id=? AND base=? AND quote=?`, projectID, base, quote)
		ctx.Emit("currencies.sync.completed", map[string]any{
			"provider": conn.AppSlug, "base": base, "quote": quote, "observations": len(created),
		})
		// A successful primary answers the request. Fallbacks are for failure,
		// not for silently blending provider observations.
		if len(inputs) > 0 {
			return created, nil
		}
	}
	err = fmt.Errorf("all FX providers failed: %s", strings.Join(failures, "; "))
	_, _ = ctx.AppDB().Exec(`UPDATE tracked_pairs SET last_refresh_at=CURRENT_TIMESTAMP,last_error=?
        WHERE project_id=? AND base=? AND quote=?`, err.Error(), projectID, base, quote)
	ctx.Emit("currencies.sync.failed", map[string]any{"base": base, "quote": quote, "error": err.Error()})
	return nil, err
}

func providerConnections(ctx *sdk.AppCtx, projectID string) ([]sdk.PlatformConnection, error) {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return nil, errors.New("platform connections are unavailable")
	}
	priority := providerPriority(ctx)
	out := []sdk.PlatformConnection{}
	for _, slug := range compatibleProviderSlugs {
		rows, err := ctx.PlatformAPI().ListConnections(sdk.ConnectionFilter{ProjectID: projectID, AppSlug: slug})
		if err != nil {
			continue
		}
		for _, c := range rows {
			if c.Status == "" || c.Status == "active" || c.Status == "connected" {
				out = append(out, c)
			}
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		pi, ok := priority[out[i].AppSlug]
		if !ok {
			pi = 1000
		}
		pj, ok := priority[out[j].AppSlug]
		if !ok {
			pj = 1000
		}
		if pi != pj {
			return pi < pj
		}
		return out[i].ID < out[j].ID
	})
	return out, nil
}

func fetchProviderObservations(ctx *sdk.AppCtx, conn sdk.PlatformConnection, base, quote string, asOf time.Time) ([]ObservationInput, error) {
	var tool string
	args := map[string]any{}
	switch conn.AppSlug {
	case "alpaca-market-data":
		if time.Since(asOf) < 24*time.Hour {
			tool = "forex_latest_rates"
			args["currency_pairs"] = base + quote
		} else {
			tool = "forex_rates"
			args = map[string]any{
				"currency_pairs": base + quote, "timeframe": "1Day",
				"end": asOf.UTC().Format(time.RFC3339), "limit": 1000, "sort": "desc",
			}
		}
	case "saltedge":
		tool = "list_rates"
		args["date"] = asOf.UTC().Format("2006-01-02")
	case "alpha-vantage":
		tool = "fx_daily"
		outputSize := "compact"
		if time.Since(asOf) > 90*24*time.Hour {
			outputSize = "full"
		}
		args = map[string]any{"from_symbol": base, "to_symbol": quote, "outputsize": outputSize}
	default:
		return nil, fmt.Errorf("unsupported FX provider %q", conn.AppSlug)
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(conn.ID, tool, args)
	if err != nil {
		return nil, err
	}
	if res == nil {
		return nil, errors.New("empty integration response")
	}
	if !res.Success {
		return nil, fmt.Errorf("%s failed: HTTP %d %s", tool, res.Status, strings.TrimSpace(string(res.Data)))
	}
	if len(res.Data) == 0 {
		return nil, errors.New("provider returned an empty body")
	}
	payloadHash := sha256.Sum256(res.Data)
	hash := hex.EncodeToString(payloadHash[:])
	switch conn.AppSlug {
	case "alpha-vantage":
		return parseAlphaVantage(res.Data, conn, base, quote, asOf, hash)
	case "alpaca-market-data":
		return parseAlpaca(res.Data, conn, base, quote, asOf, hash)
	case "saltedge":
		return parseSaltEdge(res.Data, conn, base, quote, asOf, hash)
	}
	return nil, errors.New("unreachable provider adapter")
}

func parseAlphaVantage(raw []byte, conn sdk.PlatformConnection, base, quote string, asOf time.Time, hash string) ([]ObservationInput, error) {
	var root map[string]any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, err
	}
	for k, v := range root {
		if strings.Contains(strings.ToLower(k), "error") || strings.Contains(strings.ToLower(k), "note") {
			if s, ok := v.(string); ok && s != "" {
				return nil, errors.New(s)
			}
		}
	}
	var series map[string]any
	for k, v := range root {
		if strings.Contains(strings.ToLower(k), "time series") {
			series, _ = v.(map[string]any)
			break
		}
	}
	if len(series) == 0 {
		return nil, errors.New("Alpha Vantage response has no FX time series")
	}
	keys := map[string]string{"1. open": "open", "2. high": "high", "3. low": "low", "4. close": "close"}
	out := []ObservationInput{}
	for date, rawBar := range series {
		day, err := time.Parse("2006-01-02", date)
		if err != nil || day.After(asOf) {
			continue
		}
		bar, _ := rawBar.(map[string]any)
		for providerKey, kind := range keys {
			rate, ok := decimalValue(bar[providerKey])
			if !ok {
				continue
			}
			out = append(out, providerObservation(conn, base, quote, rate, kind, day, date, "day", date+":"+kind, hash))
		}
	}
	if len(out) == 0 {
		return nil, errors.New("Alpha Vantage returned no eligible FX bars")
	}
	return out, nil
}

func parseAlpaca(raw []byte, conn sdk.PlatformConnection, base, quote string, asOf time.Time, hash string) ([]ObservationInput, error) {
	var root any
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, err
	}
	node := findPairNode(root, base+quote)
	if node == nil {
		return nil, errors.New("Alpaca response has no requested currency pair")
	}
	entries := flattenRateEntries(node)
	out := []ObservationInput{}
	for _, entry := range entries {
		rate, ok := rateFromMap(entry)
		if !ok {
			continue
		}
		effective := timeFromMap(entry, asOf)
		if effective.After(asOf.Add(time.Minute)) {
			continue
		}
		kind := "spot"
		granularity := "instant"
		if _, ok := entry["c"]; ok {
			kind, granularity = "close", "day"
		}
		out = append(out, providerObservation(conn, base, quote, rate, kind, effective,
			effective.UTC().Format("2006-01-02"), granularity, effective.UTC().Format(time.RFC3339Nano), hash))
	}
	if len(out) == 0 {
		return nil, errors.New("Alpaca returned no parseable FX rate")
	}
	return out, nil
}

func parseSaltEdge(raw []byte, conn sdk.PlatformConnection, base, quote string, asOf time.Time, hash string) ([]ObservationInput, error) {
	var root struct {
		Data []map[string]any `json:"data"`
		Meta struct {
			IssuedOn string `json:"issued_on"`
		} `json:"meta"`
	}
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, err
	}
	if len(root.Data) == 0 {
		return nil, errors.New("Salt Edge response has no rates")
	}
	effective, err := parseFlexibleTime(root.Meta.IssuedOn)
	if err != nil {
		return nil, fmt.Errorf("Salt Edge meta.issued_on: %w", err)
	}
	if effective.After(asOf) {
		return nil, errors.New("Salt Edge returned rates newer than the requested as_of")
	}

	type usdRate struct {
		rate   *big.Rat
		failed bool
	}
	rates := map[string]usdRate{"USD": {rate: new(big.Rat).SetInt64(1)}}
	for _, entry := range root.Data {
		code := strings.ToUpper(firstMapString(entry, "currency_code"))
		if code == "" {
			continue
		}
		rateText, ok := decimalValue(entry["rate"])
		if !ok {
			continue
		}
		rate, _, err := parsePositiveDecimal(rateText)
		if err != nil {
			continue
		}
		failed, _ := entry["fail"].(bool)
		rates[code] = usdRate{rate: rate, failed: failed}
	}
	baseUSD, baseOK := rates[base]
	quoteUSD, quoteOK := rates[quote]
	if !baseOK || !quoteOK {
		missing := []string{}
		if !baseOK {
			missing = append(missing, base)
		}
		if !quoteOK {
			missing = append(missing, quote)
		}
		return nil, fmt.Errorf("Salt Edge response has no USD reference rate for %s", strings.Join(missing, ", "))
	}

	// Salt Edge defines every entry as one unit of currency expressed in USD.
	// Dividing the two USD reference rates yields quote units per base unit.
	rate := ratDecimal(new(big.Rat).Quo(baseUSD.rate, quoteUSD.rate))
	observation := providerObservation(conn, base, quote, rate, "reference", effective,
		effective.UTC().Format("2006-01-02"), "day", root.Meta.IssuedOn+":via-USD", hash)
	observation.AdapterVersion = "saltedge-v2"
	observation.QualityFlags = []string{"normalized_from_usd_reference_rates"}
	if baseUSD.failed || quoteUSD.failed {
		observation.QualityFlags = append(observation.QualityFlags, "provider_previous_available_date")
	}
	return []ObservationInput{observation}, nil
}

func providerObservation(conn sdk.PlatformConnection, base, quote, rate, kind string, effective time.Time, date, granularity, ref, hash string) ObservationInput {
	return ObservationInput{
		Base: base, Quote: quote, Rate: rate, RateKind: kind, EffectiveAt: effective.UTC(), EffectiveDate: date,
		Granularity: granularity, ObservedAt: time.Now().UTC(), ProviderSlug: conn.AppSlug,
		ConnectionID: conn.ID, ProviderRef: ref, OriginalBase: base, OriginalQuote: quote,
		PayloadHash: hash, AdapterVersion: "v1", QualityFlags: []string{},
	}
}

func findPairNode(v any, pair string) any {
	pair = normalizePairKey(pair)
	switch x := v.(type) {
	case map[string]any:
		for k, child := range x {
			if normalizePairKey(k) == pair {
				return child
			}
		}
		for _, preferred := range []string{"rates", "data", "bars", "quotes"} {
			if child, ok := x[preferred]; ok {
				if found := findPairNode(child, pair); found != nil {
					return found
				}
			}
		}
	case []any:
		for _, child := range x {
			if found := findPairNode(child, pair); found != nil {
				return found
			}
		}
	}
	return nil
}

func normalizePairKey(v string) string {
	v = strings.ToUpper(v)
	return strings.NewReplacer("/", "", "-", "", "_", "", " ", "").Replace(v)
}

func flattenRateEntries(v any) []map[string]any {
	switch x := v.(type) {
	case []any:
		out := []map[string]any{}
		for _, item := range x {
			if m, ok := item.(map[string]any); ok {
				out = append(out, m)
			}
		}
		return out
	case map[string]any:
		if _, ok := rateFromMap(x); ok {
			return []map[string]any{x}
		}
		out := []map[string]any{}
		for key, item := range x {
			if m, ok := item.(map[string]any); ok {
				if _, exists := m["timestamp"]; !exists {
					m["timestamp"] = key
				}
				out = append(out, m)
			}
		}
		return out
	default:
		if rate, ok := decimalValue(x); ok {
			return []map[string]any{{"rate": rate}}
		}
	}
	return nil
}

func collectRateMaps(v any, out *[]map[string]any) {
	switch x := v.(type) {
	case map[string]any:
		if _, ok := rateFromMap(x); ok {
			*out = append(*out, x)
			return
		}
		for _, child := range x {
			collectRateMaps(child, out)
		}
	case []any:
		for _, child := range x {
			collectRateMaps(child, out)
		}
	}
}

func rateFromMap(m map[string]any) (string, bool) {
	for _, key := range []string{"rate", "mid", "mid_price", "mp", "price", "value", "c", "close", "4. close"} {
		if v, ok := decimalValue(m[key]); ok {
			return v, true
		}
	}
	bid, bidOK := decimalValue(firstMapValue(m, "bid", "bid_price", "bp"))
	ask, askOK := decimalValue(firstMapValue(m, "ask", "ask_price", "ap"))
	if bidOK && askOK {
		br, _, _ := parsePositiveDecimal(bid)
		ar, _, _ := parsePositiveDecimal(ask)
		return ratDecimal(new(big.Rat).Quo(new(big.Rat).Add(br, ar), new(big.Rat).SetInt64(2))), true
	}
	return "", false
}

func decimalValue(v any) (string, bool) {
	var s string
	switch x := v.(type) {
	case string:
		s = x
	case json.Number:
		s = x.String()
	case float64:
		s = strconv.FormatFloat(x, 'f', -1, 64)
	case float32:
		s = strconv.FormatFloat(float64(x), 'f', -1, 32)
	case int:
		s = strconv.Itoa(x)
	case int64:
		s = strconv.FormatInt(x, 10)
	default:
		return "", false
	}
	_, canonical, err := parsePositiveDecimal(s)
	return canonical, err == nil
}

func timeFromMap(m map[string]any, fallback time.Time) time.Time {
	for _, key := range []string{"timestamp", "time", "t", "date", "datetime", "effective_at"} {
		v := m[key]
		s, ok := v.(string)
		if ok {
			if t, err := parseFlexibleTime(s); err == nil {
				return t
			}
		}
		if n, ok := v.(float64); ok {
			if n > 1e12 {
				return time.UnixMilli(int64(n)).UTC()
			}
			if n > 1e9 {
				return time.Unix(int64(n), 0).UTC()
			}
		}
	}
	return fallback.UTC()
}

func firstMapString(m map[string]any, keys ...string) string {
	for _, key := range keys {
		if s, ok := m[key].(string); ok && strings.TrimSpace(s) != "" {
			return strings.TrimSpace(s)
		}
	}
	return ""
}

func firstMapValue(m map[string]any, keys ...string) any {
	for _, key := range keys {
		if v, ok := m[key]; ok {
			return v
		}
	}
	return nil
}

func startSyncRun(db *sql.DB, projectID string, conn sdk.PlatformConnection, base, quote string) int64 {
	res, err := db.Exec(`INSERT INTO sync_runs(project_id,provider_slug,connection_id,base,quote,status,started_at)
        VALUES(?,?,?,?,?,'running',?)`, projectID, conn.AppSlug, conn.ID, base, quote, time.Now().UTC().Format(time.RFC3339Nano))
	if err != nil {
		return 0
	}
	id, _ := res.LastInsertId()
	return id
}

func finishSyncRun(db *sql.DB, id int64, status string, observations int, message string) {
	if id == 0 {
		return
	}
	_, _ = db.Exec(`UPDATE sync_runs SET status=?,observations=?,error=?,completed_at=? WHERE id=?`,
		status, observations, message, time.Now().UTC().Format(time.RFC3339Nano), id)
}

func updateProviderHealth(db *sql.DB, projectID string, conn sdk.PlatformConnection, success bool, callErr error) {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if success {
		_, _ = db.Exec(`INSERT INTO provider_health(project_id,connection_id,provider_slug,last_attempt_at,last_success_at,last_error,failure_count)
            VALUES(?,?,?,?,?,'',0) ON CONFLICT(project_id,connection_id) DO UPDATE SET
            provider_slug=excluded.provider_slug,last_attempt_at=excluded.last_attempt_at,
            last_success_at=excluded.last_success_at,last_error='',failure_count=0,updated_at=CURRENT_TIMESTAMP`,
			projectID, conn.ID, conn.AppSlug, now, now)
		return
	}
	message := "provider call failed"
	if callErr != nil {
		message = callErr.Error()
	}
	_, _ = db.Exec(`INSERT INTO provider_health(project_id,connection_id,provider_slug,last_attempt_at,last_error,failure_count)
        VALUES(?,?,?,?,?,1) ON CONFLICT(project_id,connection_id) DO UPDATE SET
        provider_slug=excluded.provider_slug,last_attempt_at=excluded.last_attempt_at,
        last_error=excluded.last_error,failure_count=provider_health.failure_count+1,updated_at=CURRENT_TIMESTAMP`,
		projectID, conn.ID, conn.AppSlug, now, message)
}

func (a *App) refreshTrackedPairs(ctx context.Context, app *sdk.AppCtx) error {
	projectID := scopeKey(app, nil)
	pairs, err := trackedPairs(app.AppDB(), projectID)
	if err != nil {
		return err
	}
	if len(pairs) == 0 {
		return nil
	}
	connections, err := providerConnections(app, projectID)
	if err != nil || len(connections) == 0 {
		return nil // manual/offline mode is healthy
	}
	var failures []string
	for _, pair := range pairs {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		if _, err := a.fetchPair(app, projectID, pair[0], pair[1], time.Now().UTC(), ""); err != nil {
			failures = append(failures, pair[0]+"/"+pair[1]+": "+err.Error())
		}
	}
	if len(failures) > 0 {
		return errors.New(strings.Join(failures, "; "))
	}
	return nil
}
