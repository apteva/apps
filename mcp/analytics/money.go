package main

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"
)

type FXRate struct {
	ID            int64   `json:"id"`
	ProjectID     string  `json:"project_id,omitempty"`
	BaseCurrency  string  `json:"base_currency"`
	QuoteCurrency string  `json:"quote_currency"`
	AsOf          int64   `json:"as_of"`
	Rate          float64 `json:"rate"`
	Source        string  `json:"source"`
	CreatedAt     int64   `json:"created_at,omitempty"`
	UpdatedAt     int64   `json:"updated_at,omitempty"`
}

type MoneyQuery struct {
	Value             string `json:"value"`
	CurrencyField     string `json:"currency_field"`
	ReportingCurrency string `json:"reporting_currency"`
	AmountUnit        string `json:"amount_unit"`
	RateDateField     string `json:"rate_date_field,omitempty"`
}

type moneyBucket struct {
	ValueMinor int64
	Count      int64
	Breakdown  map[string]*moneyBreakdown
	Rates      map[string]moneyRateUse
}

type moneyBreakdown struct {
	Currency       string  `json:"currency"`
	OriginalValue  float64 `json:"original_value"`
	ConvertedValue float64 `json:"converted_value"`
	Count          int64   `json:"count"`
}

type moneyRateUse struct {
	BaseCurrency  string  `json:"base_currency"`
	QuoteCurrency string  `json:"quote_currency"`
	AsOf          int64   `json:"as_of"`
	Rate          float64 `json:"rate"`
	Source        string  `json:"source"`
}

type fxRateIndex struct {
	direct map[string][]FXRate
}

func moneyQueryFromConfig(cfg map[string]any) (MoneyQuery, error) {
	q := MoneyQuery{
		Value:             strings.TrimSpace(stringConfig(cfg, "value", "")),
		CurrencyField:     strings.TrimSpace(stringConfig(cfg, "currency_field", "")),
		ReportingCurrency: strings.ToUpper(strings.TrimSpace(stringConfig(cfg, "reporting_currency", ""))),
		AmountUnit:        strings.ToLower(strings.TrimSpace(stringConfig(cfg, "amount_unit", ""))),
		RateDateField:     strings.TrimSpace(stringConfig(cfg, "rate_date_field", "")),
	}
	if _, _, ok := numericValueExtract(q.Value); !ok {
		return MoneyQuery{}, errors.New("sum_money value must be a numeric event field or props.X")
	}
	if _, ok := propsExtract(q.CurrencyField); !ok {
		return MoneyQuery{}, errors.New("sum_money currency_field must be props.X")
	}
	if !validCurrencyCode(q.ReportingCurrency) {
		return MoneyQuery{}, errors.New("sum_money reporting_currency must be a three-letter ISO currency code")
	}
	if q.AmountUnit != "minor" && q.AmountUnit != "major" {
		return MoneyQuery{}, errors.New("sum_money amount_unit must be minor or major")
	}
	if q.RateDateField != "" {
		if _, ok := propsExtract(q.RateDateField); !ok {
			return MoneyQuery{}, errors.New("sum_money rate_date_field must be props.X when set")
		}
	}
	return q, nil
}

func validCurrencyCode(value string) bool {
	if len(value) != 3 {
		return false
	}
	for _, r := range value {
		if r < 'A' || r > 'Z' {
			return false
		}
	}
	return true
}

func normalizeFXRate(rate *FXRate) error {
	rate.BaseCurrency = strings.ToUpper(strings.TrimSpace(rate.BaseCurrency))
	rate.QuoteCurrency = strings.ToUpper(strings.TrimSpace(rate.QuoteCurrency))
	rate.Source = strings.TrimSpace(rate.Source)
	if rate.Source == "" {
		rate.Source = "manual"
	}
	if !validCurrencyCode(rate.BaseCurrency) || !validCurrencyCode(rate.QuoteCurrency) {
		return errors.New("base_currency and quote_currency must be three-letter ISO currency codes")
	}
	if rate.BaseCurrency == rate.QuoteCurrency {
		return errors.New("base_currency and quote_currency must differ")
	}
	if rate.AsOf <= 0 {
		return errors.New("as_of must be positive Unix milliseconds")
	}
	if rate.Rate <= 0 || math.IsNaN(rate.Rate) || math.IsInf(rate.Rate, 0) {
		return errors.New("rate must be a finite number greater than zero")
	}
	if len(rate.Source) > 120 {
		return errors.New("source is too long")
	}
	return nil
}

func upsertFXRate(db *sql.DB, projectID string, rate FXRate) (*FXRate, error) {
	rate.ProjectID = projectID
	if err := normalizeFXRate(&rate); err != nil {
		return nil, err
	}
	now := time.Now().UnixMilli()
	_, err := db.Exec(`INSERT INTO fx_rates
		(project_id, base_currency, quote_currency, as_of, rate, source, created_at, updated_at)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(project_id, base_currency, quote_currency, as_of) DO UPDATE SET
			rate=excluded.rate, source=excluded.source, updated_at=excluded.updated_at`,
		projectID, rate.BaseCurrency, rate.QuoteCurrency, rate.AsOf, rate.Rate, rate.Source, now, now)
	if err != nil {
		return nil, err
	}
	if err := db.QueryRow(`SELECT id, project_id, base_currency, quote_currency, as_of, rate, source, created_at, updated_at
		FROM fx_rates WHERE project_id=? AND base_currency=? AND quote_currency=? AND as_of=?`,
		projectID, rate.BaseCurrency, rate.QuoteCurrency, rate.AsOf).Scan(
		&rate.ID, &rate.ProjectID, &rate.BaseCurrency, &rate.QuoteCurrency, &rate.AsOf,
		&rate.Rate, &rate.Source, &rate.CreatedAt, &rate.UpdatedAt); err != nil {
		return nil, err
	}
	return &rate, nil
}

func listFXRates(db *sql.DB, projectID, base, quote string, since, until int64, limit int) ([]FXRate, error) {
	base = strings.ToUpper(strings.TrimSpace(base))
	quote = strings.ToUpper(strings.TrimSpace(quote))
	if base != "" && !validCurrencyCode(base) {
		return nil, errors.New("base_currency must be a three-letter ISO currency code")
	}
	if quote != "" && !validCurrencyCode(quote) {
		return nil, errors.New("quote_currency must be a three-letter ISO currency code")
	}
	if limit <= 0 {
		limit = 200
	}
	if limit > 1000 {
		limit = 1000
	}
	where := []string{"project_id=?"}
	args := []any{projectID}
	if base != "" {
		where, args = append(where, "base_currency=?"), append(args, base)
	}
	if quote != "" {
		where, args = append(where, "quote_currency=?"), append(args, quote)
	}
	if since > 0 {
		where, args = append(where, "as_of>=?"), append(args, since)
	}
	if until > 0 {
		where, args = append(where, "as_of<?"), append(args, until)
	}
	args = append(args, limit)
	rows, err := db.Query(`SELECT id, project_id, base_currency, quote_currency, as_of, rate, source, created_at, updated_at
		FROM fx_rates WHERE `+strings.Join(where, " AND ")+` ORDER BY as_of DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []FXRate{}
	for rows.Next() {
		var rate FXRate
		if err := rows.Scan(&rate.ID, &rate.ProjectID, &rate.BaseCurrency, &rate.QuoteCurrency,
			&rate.AsOf, &rate.Rate, &rate.Source, &rate.CreatedAt, &rate.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, rate)
	}
	return out, rows.Err()
}

func loadFXRateIndex(db *sql.DB, projectID string) (*fxRateIndex, error) {
	rows, err := db.Query(`SELECT id, project_id, base_currency, quote_currency, as_of, rate, source, created_at, updated_at
		FROM fx_rates WHERE project_id=? ORDER BY as_of, id`, projectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var rates []FXRate
	for rows.Next() {
		var rate FXRate
		if err := rows.Scan(&rate.ID, &rate.ProjectID, &rate.BaseCurrency, &rate.QuoteCurrency,
			&rate.AsOf, &rate.Rate, &rate.Source, &rate.CreatedAt, &rate.UpdatedAt); err != nil {
			return nil, err
		}
		rates = append(rates, rate)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	idx := &fxRateIndex{direct: map[string][]FXRate{}}
	for _, rate := range rates {
		key := rate.BaseCurrency + "/" + rate.QuoteCurrency
		idx.direct[key] = append(idx.direct[key], rate)
	}
	for key := range idx.direct {
		sort.Slice(idx.direct[key], func(i, j int) bool { return idx.direct[key][i].AsOf < idx.direct[key][j].AsOf })
	}
	return idx, nil
}

func latestRateAt(rates []FXRate, at int64) (FXRate, bool) {
	i := sort.Search(len(rates), func(i int) bool { return rates[i].AsOf > at })
	if i == 0 {
		return FXRate{}, false
	}
	return rates[i-1], true
}

func (idx *fxRateIndex) resolve(base, quote string, at int64) (float64, moneyRateUse, error) {
	if base == quote {
		return 1, moneyRateUse{BaseCurrency: base, QuoteCurrency: quote, Rate: 1, Source: "identity"}, nil
	}
	if rate, ok := latestRateAt(idx.direct[base+"/"+quote], at); ok {
		return rate.Rate, moneyRateUse{BaseCurrency: base, QuoteCurrency: quote, AsOf: rate.AsOf, Rate: rate.Rate, Source: rate.Source}, nil
	}
	if rate, ok := latestRateAt(idx.direct[quote+"/"+base], at); ok {
		return 1 / rate.Rate, moneyRateUse{BaseCurrency: base, QuoteCurrency: quote, AsOf: rate.AsOf, Rate: 1 / rate.Rate, Source: rate.Source + " (inverse)"}, nil
	}
	return 0, moneyRateUse{}, fmt.Errorf("missing FX rate %s/%s at %s", base, quote, time.UnixMilli(at).UTC().Format("2006-01-02"))
}

func parseMoneyRateDate(raw string, eventTS int64) (int64, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return eventTS, nil
	}
	if n, err := strconv.ParseInt(raw, 10, 64); err == nil {
		if n > 0 && n < 100_000_000_000 {
			n *= 1000
		}
		if n > 0 {
			return n, nil
		}
	}
	for _, layout := range []string{time.RFC3339Nano, time.RFC3339, "2006-01-02"} {
		if parsed, err := time.Parse(layout, raw); err == nil {
			return parsed.UnixMilli(), nil
		}
	}
	return 0, fmt.Errorf("invalid rate date %q", raw)
}

func currencyMinorDigits(currency string) int {
	// ISO 4217 currencies whose minor unit differs from the common two digits.
	if oneOf(currency, "BIF", "CLP", "DJF", "GNF", "ISK", "JPY", "KMF", "KRW", "PYG", "RWF", "UGX", "UYI", "VND", "VUV", "XAF", "XOF", "XPF") {
		return 0
	}
	if oneOf(currency, "BHD", "IQD", "JOD", "KWD", "LYD", "OMR", "TND") {
		return 3
	}
	return 2
}

func moneyAggregate(db *sql.DB, f Filter, q MoneyQuery, bucketExpr string) (map[string]*moneyBucket, error) {
	valueExpr, numericPredicate, _ := numericValueExtract(q.Value)
	currencyExpr, _ := propsExtract(q.CurrencyField)
	dateExpr := "CAST(ts AS TEXT)"
	if q.RateDateField != "" {
		dateExpr, _ = propsExtract(q.RateDateField)
		dateExpr = "COALESCE(CAST(" + dateExpr + " AS TEXT), '')"
	}
	where, args := f.buildWhere()
	conditions := []string{valueExpr + " IS NOT NULL", currencyExpr + " IS NOT NULL"}
	if where != "" {
		conditions = append([]string{where}, conditions...)
	}
	selectBucket := "''"
	if bucketExpr != "" {
		selectBucket = bucketExpr
	}
	query := `SELECT id, ts, ` + selectBucket + `, CAST(` + valueExpr + ` AS REAL),
		CASE WHEN ` + numericPredicate + ` THEN 1 ELSE 0 END,
		CAST(` + currencyExpr + ` AS TEXT), ` + dateExpr + `
		FROM events WHERE ` + strings.Join(conditions, " AND ") + ` ORDER BY ts, id`
	idx, err := loadFXRateIndex(db, f.ProjectID)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(query, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	buckets := map[string]*moneyBucket{}
	targetScale := math.Pow10(currencyMinorDigits(q.ReportingCurrency))
	for rows.Next() {
		var id, eventTS int64
		var bucket, currency, rawRateDate string
		var amount float64
		var numeric int
		if err := rows.Scan(&id, &eventTS, &bucket, &amount, &numeric, &currency, &rawRateDate); err != nil {
			return nil, err
		}
		if numeric == 0 || math.IsNaN(amount) || math.IsInf(amount, 0) {
			return nil, fmt.Errorf("sum_money value %q contains a non-numeric row at event %d", q.Value, id)
		}
		currency = strings.ToUpper(strings.TrimSpace(currency))
		if !validCurrencyCode(currency) {
			return nil, fmt.Errorf("sum_money currency_field %q contains invalid currency %q at event %d", q.CurrencyField, currency, id)
		}
		rateDate, err := parseMoneyRateDate(rawRateDate, eventTS)
		if err != nil {
			return nil, fmt.Errorf("sum_money rate_date_field %q at event %d: %w", q.RateDateField, id, err)
		}
		rate, used, err := idx.resolve(currency, q.ReportingCurrency, rateDate)
		if err != nil {
			return nil, err
		}
		sourceMajor := amount
		if q.AmountUnit == "minor" {
			sourceMajor /= math.Pow10(currencyMinorDigits(currency))
		}
		convertedMinorFloat := math.Round(sourceMajor * rate * targetScale)
		if math.Abs(convertedMinorFloat) > math.MaxInt64 {
			return nil, fmt.Errorf("sum_money converted value overflows at event %d", id)
		}
		convertedMinor := int64(convertedMinorFloat)
		entry := buckets[bucket]
		if entry == nil {
			entry = &moneyBucket{Breakdown: map[string]*moneyBreakdown{}, Rates: map[string]moneyRateUse{}}
			buckets[bucket] = entry
		}
		if (convertedMinor > 0 && entry.ValueMinor > math.MaxInt64-convertedMinor) || (convertedMinor < 0 && entry.ValueMinor < math.MinInt64-convertedMinor) {
			return nil, errors.New("sum_money total overflows")
		}
		entry.ValueMinor += convertedMinor
		entry.Count++
		line := entry.Breakdown[currency]
		if line == nil {
			line = &moneyBreakdown{Currency: currency}
			entry.Breakdown[currency] = line
		}
		line.OriginalValue += sourceMajor
		line.ConvertedValue += float64(convertedMinor) / targetScale
		line.Count++
		rateKey := fmt.Sprintf("%s/%s/%d/%.12g", used.BaseCurrency, used.QuoteCurrency, used.AsOf, used.Rate)
		entry.Rates[rateKey] = used
	}
	return buckets, rows.Err()
}

func moneyBucketResult(bucket *moneyBucket, q MoneyQuery) map[string]any {
	if bucket == nil {
		bucket = &moneyBucket{Breakdown: map[string]*moneyBreakdown{}, Rates: map[string]moneyRateUse{}}
	}
	targetScale := math.Pow10(currencyMinorDigits(q.ReportingCurrency))
	breakdown := make([]moneyBreakdown, 0, len(bucket.Breakdown))
	for _, line := range bucket.Breakdown {
		line.OriginalValue = math.Round(line.OriginalValue*1e6) / 1e6
		line.ConvertedValue = math.Round(line.ConvertedValue*targetScale) / targetScale
		breakdown = append(breakdown, *line)
	}
	sort.Slice(breakdown, func(i, j int) bool { return breakdown[i].Currency < breakdown[j].Currency })
	rates := make([]moneyRateUse, 0, len(bucket.Rates))
	for _, rate := range bucket.Rates {
		rates = append(rates, rate)
	}
	sort.Slice(rates, func(i, j int) bool {
		if rates[i].BaseCurrency != rates[j].BaseCurrency {
			return rates[i].BaseCurrency < rates[j].BaseCurrency
		}
		return rates[i].AsOf < rates[j].AsOf
	})
	return map[string]any{
		"value":             float64(bucket.ValueMinor) / targetScale,
		"count":             bucket.Count,
		"aggregation":       "sum_money",
		"metric":            "sum_money",
		"field":             q.Value,
		"currency":          q.ReportingCurrency,
		"amount_unit":       "major",
		"input_amount_unit": q.AmountUnit,
		"currency_field":    q.CurrencyField,
		"rate_date_field":   q.RateDateField,
		"breakdown":         breakdown,
		"fx":                map[string]any{"policy": "at_or_before_event_date", "coverage": 1, "rates_used": rates},
	}
}

func moneyScalarForWidget(db *sql.DB, f Filter, cfg map[string]any) (map[string]any, error) {
	q, err := moneyQueryFromConfig(cfg)
	if err != nil {
		return nil, err
	}
	buckets, err := moneyAggregate(db, f, q, "")
	if err != nil {
		return nil, err
	}
	return moneyBucketResult(buckets[""], q), nil
}

func moneySeriesForWidget(db *sql.DB, f Filter, interval string, cfg map[string]any) ([]map[string]any, map[string]any, error) {
	q, err := moneyQueryFromConfig(cfg)
	if err != nil {
		return nil, nil, err
	}
	buckets, err := moneyAggregate(db, f, q, dashboardBucketExpr(interval))
	if err != nil {
		return nil, nil, err
	}
	keys := make([]string, 0, len(buckets))
	for key := range buckets {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	rows := make([]map[string]any, 0, len(keys))
	all := &moneyBucket{Breakdown: map[string]*moneyBreakdown{}, Rates: map[string]moneyRateUse{}}
	for _, key := range keys {
		row := moneyBucketResult(buckets[key], q)
		row["bucket"] = key
		rows = append(rows, row)
		all.ValueMinor += buckets[key].ValueMinor
		all.Count += buckets[key].Count
		for currency, line := range buckets[key].Breakdown {
			combined := all.Breakdown[currency]
			if combined == nil {
				combined = &moneyBreakdown{Currency: currency}
				all.Breakdown[currency] = combined
			}
			combined.OriginalValue += line.OriginalValue
			combined.ConvertedValue += line.ConvertedValue
			combined.Count += line.Count
		}
		for rateKey, rate := range buckets[key].Rates {
			all.Rates[rateKey] = rate
		}
	}
	return rows, moneyBucketResult(all, q), nil
}
