package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strconv"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type financialFXNeed struct {
	Base, Quote, Day string
	At, LastAt       int64
}
type financialObservation struct {
	RateID         int64  `json:"rate_id"`
	PayloadHash    string `json:"payload_hash,omitempty"`
	AdapterVersion string `json:"adapter_version,omitempty"`
	EffectiveDate  string `json:"effective_date,omitempty"`
	Base           string `json:"base"`
	Quote          string `json:"quote"`
	Rate           string `json:"rate"`
	EffectiveAt    string `json:"effective_at"`
	Provider       string `json:"provider"`
	ProviderRef    string `json:"provider_ref"`
	ObservedAt     string `json:"observed_at"`
}

// CallAppResult currently has no context argument. At most one outstanding
// provider call is allowed, even after a worker deadline. It only fetches data;
// late results cannot write Analytics state or retain a lease.
var financialProviderSlot = make(chan struct{}, 1)
var financialFetch = func(ctx context.Context, app *sdk.AppCtx, need financialFXNeed) ([]financialObservation, error) {
	select {
	case financialProviderSlot <- struct{}{}:
	default:
		return nil, errors.New("Currencies provider is busy; retry queued")
	}
	type response struct {
		Observations []financialObservation `json:"observations"`
	}
	type result struct {
		out response
		err error
	}
	done := make(chan result, 1)
	day, err := time.Parse("2006-01-02", need.Day)
	if err != nil {
		<-financialProviderSlot
		return nil, err
	}
	go func() {
		defer func() { <-financialProviderSlot }()
		var out response
		err := app.PlatformAPI().CallAppResult("currencies", "currencies_sync_now", map[string]any{"provider": "ecb-reference-rates", "base": need.Base, "quote": need.Quote, "from": day.AddDate(0, 0, -14).Format("2006-01-02"), "to": need.Day}, &out)
		done <- result{out, err}
	}()
	select {
	case r := <-done:
		if r.err != nil {
			return nil, fmt.Errorf("optional Currencies provider unavailable: %w", r.err)
		}
		if len(r.out.Observations) == 0 {
			return nil, errors.New("Currencies returned no historical observations; upgrade Currencies or add manual FX rates")
		}
		return r.out.Observations, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func financialNeeds(db sqlRunner, project string, target ObjectiveTarget) ([]financialFXNeed, error) {
	needs := []financialFXNeed{}
	if target.Query.Aggregation != "sum_money" {
		return needs, nil
	}
	f := Filter{ProjectID: project, App: target.Query.App, Topic: target.Query.Topic, Source: target.Query.Source, Where: target.Query.Where, Since: target.PeriodStart, Until: target.PeriodEnd}
	where, args, err := f.buildWhere()
	if err != nil {
		return nil, err
	}
	currency, ok := propsExtract(target.Query.CurrencyField)
	if !ok {
		return nil, errors.New("invalid currency field")
	}
	date := "CAST(ts AS TEXT)"
	if target.Query.RateDateField != "" {
		date, ok = propsExtract(target.Query.RateDateField)
		if !ok {
			return nil, errors.New("invalid rate date field")
		}
	}
	rows, err := db.Query(`SELECT DISTINCT UPPER(TRIM(COALESCE(CAST(`+currency+` AS TEXT),''))),COALESCE(CAST(`+date+` AS TEXT),'') FROM events WHERE `+where+` LIMIT 50001`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	seen := map[string]int{}
	count := 0
	for rows.Next() {
		count++
		if count > 50000 {
			return nil, errors.New("FX requirements exceed 50000 distinct dates; narrow the target period")
		}
		var base, raw string
		if err = rows.Scan(&base, &raw); err != nil {
			return nil, err
		}
		if !validCurrencyCode(base) {
			return nil, errors.New("invalid source currency")
		}
		if base == target.Query.ReportingCurrency {
			continue
		}
		at, e := parseMoneyRateDate(raw, 0)
		if e != nil || at <= 0 {
			return nil, errors.New("missing or invalid FX accounting date")
		}
		day := time.UnixMilli(at).UTC().Format("2006-01-02")
		key := base + "/" + day
		// Retain the earliest intraday timestamp for coverage validation. Discovery
		// imports a full window; the evaluator still selects each event's exact time.
		if index, ok := seen[key]; ok {
			if at > needs[index].LastAt {
				needs[index].LastAt = at
			}
			if at < needs[index].At {
				needs[index].At = at
			}
			continue
		}
		seen[key] = len(needs)
		needs = append(needs, financialFXNeed{Base: base, Quote: target.Query.ReportingCurrency, Day: day, At: at, LastAt: at})
	}
	return needs, rows.Err()
}

// Reference data is published on TARGET business days, at approximately 16:00
// Brussels time. This policy tolerates weekends and TARGET holidays, without
// silently carrying a months-old quote into today's totals.
func expectedECBPublication(at time.Time) time.Time {
	loc, _ := time.LoadLocation("Europe/Brussels")
	at = at.In(loc)
	day := time.Date(at.Year(), at.Month(), at.Day(), 16, 0, 0, 0, loc)
	if day.After(at) {
		day = day.AddDate(0, 0, -1)
	}
	for !ecbBusinessDay(day) {
		day = day.AddDate(0, 0, -1)
	}
	return day
}
func ecbBusinessDay(d time.Time) bool {
	if d.Weekday() == time.Saturday || d.Weekday() == time.Sunday {
		return false
	}
	m, day := d.Month(), d.Day()
	if (m == 1 && day == 1) || (m == 5 && day == 1) || (m == 12 && (day == 25 || day == 26)) {
		return false
	}
	y := d.Year()
	a := y % 19
	b := y / 100
	c := y % 100
	dd := b / 4
	e := b % 4
	f := (b + 8) / 25
	g := (b - f + 1) / 3
	h := (19*a + b - dd - g + 15) % 30
	i := c / 4
	k := c % 4
	l := (32 + 2*e + 2*i - h - k) % 7
	mm := (a + 11*h + 22*l) / 451
	easter := time.Date(y, time.Month((h+l-7*mm+114)/31), (h+l-7*mm+114)%31+1, 16, 0, 0, 0, d.Location())
	date := d.Format("2006-01-02")
	return date != easter.AddDate(0, 0, -2).Format("2006-01-02") && date != easter.AddDate(0, 0, 1).Format("2006-01-02")
}
func financialFXCoverage(db sqlRunner, project string, target ObjectiveTarget) error {
	needs, err := financialNeeds(db, project, target)
	if err != nil {
		return err
	}
	if len(needs) == 0 {
		return nil
	}
	idx, err := loadFXRateIndex(db, project)
	if err != nil {
		return err
	}
	for _, need := range needs {
		for _, at := range []int64{need.At, need.LastAt} {
			need.At = at
			_, used, e := idx.resolve(need.Base, need.Quote, need.At)
			if e != nil {
				return e
			}
			// Existing daily manual rates can use midnight. Compare publication dates,
			// while the ordinary evaluator continues enforcing exact as_of eligibility.
			expected := expectedECBPublication(time.UnixMilli(need.At)).Format("2006-01-02")
			if time.UnixMilli(used.AsOf).UTC().Format("2006-01-02") < expected {
				return fmt.Errorf("stale FX coverage %s/%s on %s; expected publication %s", need.Base, need.Quote, need.Day, expected)
			}
		}
	}
	return nil
}

func maintainFinancialFX(ctx context.Context, app *sdk.AppCtx, project string) error {
	db := contextualDB{db: app.AppDB(), ctx: ctx}
	var enabled bool
	if err := db.QueryRow(`SELECT fx_enabled FROM financial_projects WHERE project_id=?`, project).Scan(&enabled); err != nil {
		return err
	}
	if !enabled {
		return nil
	}
	// Round-robin discovery is bounded and resumes after a restart. A large
	// catalog cannot keep later targets from ever getting their FX requirements.
	var cursor int64
	if err := db.QueryRow(`SELECT fx_cursor FROM financial_projects WHERE project_id=?`, project).Scan(&cursor); err != nil {
		return err
	}
	rows, err := db.Query(`SELECT t.id FROM objective_targets t JOIN objectives o ON o.id=t.objective_id WHERE o.project_id=? AND o.status='active' AND t.retired_at IS NULL AND t.id>? ORDER BY t.id LIMIT 8`, project, cursor)
	if err != nil {
		return err
	}
	ids := []int64{}
	for rows.Next() {
		var id int64
		if err = rows.Scan(&id); err != nil {
			break
		}
		ids = append(ids, id)
	}
	if err == nil {
		err = rows.Err()
	}
	rows.Close()
	if err != nil {
		return err
	}
	for _, id := range ids {
		target, e := financialTarget(db, project, id)
		if e != nil {
			return e
		}
		discovery, cancel := context.WithTimeout(ctx, 500*time.Millisecond)
		needs, e := financialNeeds(contextualDB{db: readPool(app), ctx: discovery}, project, target)
		cancel()
		if e == nil {
			for _, n := range needs {
				if _, err = db.Exec(`INSERT INTO financial_fx_requests(project_id,base,quote,day,required_at) VALUES(?,?,?,?,?) ON CONFLICT(project_id,base,quote,day) DO UPDATE SET required_at=MAX(required_at,excluded.required_at),next_retry=CASE WHEN excluded.required_at>required_at THEN 0 ELSE next_retry END`, project, n.Base, n.Quote, n.Day, n.LastAt); err != nil {
					return err
				}
			}
		}
		cursor = id
	}
	if len(ids) < 8 {
		cursor = 0
	}
	if _, err = db.Exec(`UPDATE financial_projects SET fx_cursor=? WHERE project_id=?`, cursor, project); err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	var need financialFXNeed
	var retries int
	err = db.QueryRow(`SELECT base,quote,day,retry_count,required_at FROM financial_fx_requests WHERE project_id=? AND next_retry<=? ORDER BY last_attempt,day LIMIT 1`, project, now).Scan(&need.Base, &need.Quote, &need.Day, &retries, &need.At)
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	if _, err = db.Exec(`UPDATE financial_fx_requests SET last_attempt=? WHERE project_id=? AND base=? AND quote=? AND day=?`, now, project, need.Base, need.Quote, need.Day); err != nil {
		return err
	}
	netCtx, cancel := context.WithTimeout(ctx, 4*time.Second)
	obs, fetchErr := financialFetch(netCtx, app, need)
	cancel()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if fetchErr == nil {
		fetchErr = importFinancialRates(ctx, app.AppDB(), project, need, obs)
		if fetchErr == nil {
			idx, e := loadFXRateIndex(db, project)
			fetchErr = e
			if e == nil {
				_, used, e := idx.resolve(need.Base, need.Quote, need.At)
				fetchErr = e
				if e == nil && time.UnixMilli(used.AsOf).UTC().Format("2006-01-02") < expectedECBPublication(time.UnixMilli(need.At)).Format("2006-01-02") {
					fetchErr = errors.New("ECB publication missing; FX coverage remains stale")
				}
			}
		}
	}
	next := time.Now().Add(6 * time.Hour).UnixMilli()
	message := ""
	success := now
	if need.Day < time.Now().UTC().AddDate(0, 0, -2).Format("2006-01-02") {
		next = 9223372036854775807
	}
	if fetchErr != nil {
		retries++
		message = fetchErr.Error()
		success = 0
		next = now + financialBackoff(retries).Milliseconds()
	} else {
		retries = 0
	}
	raw, _ := json.Marshal(obs)
	if len(raw) > 200000 {
		raw = []byte(`{}`)
	}
	_, err = db.Exec(`UPDATE financial_fx_requests SET last_success=CASE WHEN ?>0 THEN ? ELSE last_success END,retry_count=?,next_retry=?,last_error=?,provenance=? WHERE project_id=? AND base=? AND quote=? AND day=?`, success, success, retries, next, message, string(raw), project, need.Base, need.Quote, need.Day)
	if err != nil {
		return err
	}
	return fetchErr
}
func importFinancialRates(ctx context.Context, db *sql.DB, project string, need financialFXNeed, obs []financialObservation) error {
	if len(obs) == 0 || len(obs) > 1000 {
		return errors.New("invalid Currencies observations response")
	}
	// Immutable provider history may contain several revisions for the same
	// publication. Select its newest observation, independent of list order.
	latest := map[string]financialObservation{}
	for _, o := range obs {
		if o.Provider != "ecb-reference-rates" || o.Base != "EUR" {
			return errors.New("unexpected ECB observation")
		}
		key := o.Base + "/" + o.Quote + "/" + o.EffectiveAt
		old, exists := latest[key]
		if !exists || o.RateID > old.RateID {
			latest[key] = o
		}
	}
	obs = make([]financialObservation, 0, len(latest))
	for _, o := range latest {
		obs = append(obs, o)
	}
	sort.Slice(obs, func(i, j int) bool {
		if obs[i].EffectiveAt != obs[j].EffectiveAt {
			return obs[i].EffectiveAt < obs[j].EffectiveAt
		}
		return obs[i].Quote < obs[j].Quote
	})
	originals := map[string][]financialObservation{}
	for _, o := range obs {
		originals[o.EffectiveAt] = append(originals[o.EffectiveAt], o)
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if need.Base != "EUR" && need.Quote != "EUR" {
		byTime := map[string]map[string]financialObservation{}
		for _, o := range obs {
			if o.Provider != "ecb-reference-rates" || o.Base != "EUR" {
				return errors.New("unexpected FX provider or pair")
			}
			if byTime[o.EffectiveAt] == nil {
				byTime[o.EffectiveAt] = map[string]financialObservation{}
			}
			byTime[o.EffectiveAt][o.Quote] = o
		}
		derived := []financialObservation{}
		for at, rates := range byTime {
			base, bok := rates[need.Base]
			quote, qok := rates[need.Quote]
			if !bok || !qok {
				continue
			}
			b, ok := new(big.Rat).SetString(base.Rate)
			if !ok || b.Sign() <= 0 {
				return errors.New("invalid base reference rate")
			}
			q, ok := new(big.Rat).SetString(quote.Rate)
			if !ok || q.Sign() <= 0 {
				return errors.New("invalid quote reference rate")
			}
			rate, _ := new(big.Rat).Quo(q, b).Float64()
			derived = append(derived, financialObservation{Base: need.Base, Quote: need.Quote, Rate: strconv.FormatFloat(rate, 'g', -1, 64), EffectiveAt: at, Provider: "ecb-reference-rates"})
		}
		if len(derived) == 0 {
			return errors.New("missing same-publication FX observations for cross rate")
		}
		obs = derived
	}
	for _, o := range obs {
		if o.Provider != "ecb-reference-rates" || !validCurrencyCode(o.Base) || !validCurrencyCode(o.Quote) {
			return errors.New("unexpected FX provider or pair")
		}
		if o.Quote != need.Base && o.Quote != need.Quote {
			continue
		}
		at, e := time.Parse(time.RFC3339Nano, o.EffectiveAt)
		if e != nil {
			return e
		}
		rate, e := strconv.ParseFloat(o.Rate, 64)
		if e != nil {
			return e
		}
		r := FXRate{BaseCurrency: o.Base, QuoteCurrency: o.Quote, AsOf: at.UnixMilli(), Rate: rate, Source: "currencies:ecb"}
		if err = normalizeFXRate(&r); err != nil {
			return err
		}
		now := time.Now().UnixMilli()
		// Only rates owned by this importer may be revised. Manual/provenance data
		// is never overwritten; exact repeats do not create revisions or dirty work.
		result, err := tx.ExecContext(ctx, `INSERT INTO fx_rates(project_id,base_currency,quote_currency,as_of,rate,source,created_at,updated_at) SELECT ?,?,?,?,?,?,?,? WHERE NOT EXISTS (SELECT 1 FROM fx_rates WHERE project_id=? AND base_currency=? AND quote_currency=? AND as_of=? AND source!='currencies:ecb')
 ON CONFLICT(project_id,base_currency,quote_currency,as_of) DO UPDATE SET rate=excluded.rate,updated_at=excluded.updated_at WHERE fx_rates.source='currencies:ecb' AND fx_rates.rate!=excluded.rate`, project, r.BaseCurrency, r.QuoteCurrency, r.AsOf, r.Rate, r.Source, now, now, project, r.QuoteCurrency, r.BaseCurrency, r.AsOf)
		if err != nil {
			return err
		}
		changed, err := result.RowsAffected()
		if err != nil {
			return err
		}
		if changed > 0 {
			raw, err := json.Marshal(originals[o.EffectiveAt])
			if err != nil {
				return err
			}
			_, err = tx.ExecContext(ctx, `INSERT INTO financial_fx_provenance(revision_id,project_id,imported_at,observations) SELECT MAX(id),?,?,? FROM fx_rate_revisions WHERE project_id=? AND base_currency=? AND quote_currency=? AND as_of=?`, project, now, string(raw), project, r.BaseCurrency, r.QuoteCurrency, r.AsOf)
			if err != nil {
				return err
			}
		}

	}
	return tx.Commit()
}
