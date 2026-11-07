package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"

	"strings"

	"time"
)

type FillCostDetails struct {
	VenueSlug     string
	FeeCurrency   string
	FeeSource     string
	LiquidityRole string
	FeeBps        float64
	SpreadCost    float64
	SlippageCost  float64
	Metadata      map[string]any
}

func validateVenueProfile(p *VenueExecutionProfile) error {
	if p == nil {
		return errors.New("venue profile required")
	}
	p.VenueSlug = strings.ToLower(strings.TrimSpace(p.VenueSlug))
	p.AssetClass = strings.ToLower(strings.TrimSpace(p.AssetClass))
	p.Symbol = canonicalSymbol(p.Symbol)
	if p.Symbol == "" {
		p.Symbol = "*"
	}
	if p.VenueSlug == "" || p.AssetClass == "" {
		return errors.New("venue_slug and asset_class are required")
	}
	p.Status = strings.ToLower(strings.TrimSpace(p.Status))
	if !stringIn(p.Status, "active", "degraded", "maintenance", "outage") {
		return fmt.Errorf("invalid venue status %q", p.Status)
	}
	p.Calendar = strings.ToUpper(strings.TrimSpace(p.Calendar))
	if p.Calendar == "" {
		p.Calendar = calendarAlwaysOpen
	}
	p.SessionPolicy = strings.ToLower(strings.TrimSpace(p.SessionPolicy))
	if !stringIn(p.SessionPolicy, "continuous", "regular_only", "venue_managed") {
		return fmt.Errorf("invalid session_policy %q", p.SessionPolicy)
	}
	p.SpreadModel = strings.ToLower(strings.TrimSpace(p.SpreadModel))
	if !stringIn(p.SpreadModel, "quote", "fixed_bps", "none") {
		return fmt.Errorf("invalid spread_model %q", p.SpreadModel)
	}
	p.SlippageModel = strings.ToLower(strings.TrimSpace(p.SlippageModel))
	if !stringIn(p.SlippageModel, "fixed_bps", "none") {
		return fmt.Errorf("invalid slippage_model %q", p.SlippageModel)
	}
	p.FeeCurrency = strings.ToUpper(strings.TrimSpace(p.FeeCurrency))
	if p.FeeCurrency == "" {
		p.FeeCurrency = "USD"
	}
	for name, value := range map[string]float64{
		"fallback_spread_bps": p.FallbackSpreadBps, "slippage_bps": p.SlippageBps,
		"min_qty": p.MinQty, "min_notional": p.MinNotional, "qty_step": p.QtyStep,
		"price_tick": p.PriceTick,
	} {
		if !finite(value) || value < 0 {
			return fmt.Errorf("%s must be finite and non-negative", name)
		}
	}
	if !finite(p.MakerFeeBps) || !finite(p.TakerFeeBps) || math.Abs(p.MakerFeeBps) > 10_000 || math.Abs(p.TakerFeeBps) > 10_000 {
		return errors.New("maker_fee_bps and taker_fee_bps must be finite and between -10000 and 10000")
	}
	if !finite(p.FundingRateBps) {
		return errors.New("funding_rate_bps must be finite")
	}
	if p.FundingIntervalHours < 0 {
		return errors.New("funding_interval_hours must be non-negative")
	}
	if strings.TrimSpace(p.Source) == "" {
		p.Source = "operator"
	}
	return nil
}

func dbUpsertVenueProfile(db *sql.DB, p *VenueExecutionProfile) error {
	if err := validateVenueProfile(p); err != nil {
		return err
	}
	_, err := db.Exec(`INSERT INTO venue_execution_profiles (
		venue_slug,asset_class,symbol,status,calendar,session_policy,maker_fee_bps,taker_fee_bps,
		fee_currency,spread_model,fallback_spread_bps,slippage_model,slippage_bps,min_qty,
		min_notional,qty_step,price_tick,funding_rate_bps,funding_interval_hours,
		supports_post_only,supports_reduce_only,source,updated_at
	) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,CURRENT_TIMESTAMP)
	ON CONFLICT(venue_slug,asset_class,symbol) DO UPDATE SET
		status=excluded.status,calendar=excluded.calendar,session_policy=excluded.session_policy,
		maker_fee_bps=excluded.maker_fee_bps,taker_fee_bps=excluded.taker_fee_bps,
		fee_currency=excluded.fee_currency,spread_model=excluded.spread_model,
		fallback_spread_bps=excluded.fallback_spread_bps,slippage_model=excluded.slippage_model,
		slippage_bps=excluded.slippage_bps,min_qty=excluded.min_qty,min_notional=excluded.min_notional,
		qty_step=excluded.qty_step,price_tick=excluded.price_tick,funding_rate_bps=excluded.funding_rate_bps,
		funding_interval_hours=excluded.funding_interval_hours,supports_post_only=excluded.supports_post_only,
		supports_reduce_only=excluded.supports_reduce_only,source=excluded.source,updated_at=CURRENT_TIMESTAMP`,
		p.VenueSlug, p.AssetClass, p.Symbol, p.Status, p.Calendar, p.SessionPolicy,
		p.MakerFeeBps, p.TakerFeeBps, p.FeeCurrency, p.SpreadModel, p.FallbackSpreadBps,
		p.SlippageModel, p.SlippageBps, p.MinQty, p.MinNotional, p.QtyStep, p.PriceTick,
		p.FundingRateBps, p.FundingIntervalHours, boolInt(p.SupportsPostOnly),
		boolInt(p.SupportsReduceOnly), p.Source)
	return err
}

func validateExecutionOrder(profile VenueExecutionProfile, instrument *Instrument, mark *Mark, qty, price float64) *executionViolation {
	return validateExecutionOrderAt(profile, instrument, mark, qty, price, time.Now().UTC())
}

func dbInsertExecutionCostTx(tx *sql.Tx, projectID string, portfolioID int64, orderID string, fillID *int64,
	venue, symbol, kind string, amount float64, currency string, rateBps *float64, role, eventID string,
	metadata map[string]any, occurredAt string) (int64, bool, error) {
	if strings.TrimSpace(symbol) == "" && strings.TrimSpace(orderID) != "" {
		_ = tx.QueryRow(`SELECT symbol FROM orders WHERE id=?`, orderID).Scan(&symbol)
	}
	meta, _ := json.Marshal(metadata)
	if len(meta) == 0 {
		meta = []byte("{}")
	}
	var at any
	if strings.TrimSpace(occurredAt) != "" {
		at = occurredAt
	}
	res, err := tx.Exec(`INSERT INTO execution_costs
		(project_id,portfolio_id,order_id,fill_id,venue_slug,symbol,kind,amount,currency,rate_bps,liquidity_role,provider_event_id,metadata,occurred_at)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,COALESCE(?,CURRENT_TIMESTAMP))
		ON CONFLICT(portfolio_id,venue_slug,kind,provider_event_id) WHERE provider_event_id IS NOT NULL AND provider_event_id != '' DO NOTHING`,
		projectID, portfolioID, nullableString(orderID), nullableInt64(fillID), venue, canonicalSymbol(symbol), kind,
		amount, strings.ToUpper(currency), nullable(rateBps), nullableString(role), nullableString(eventID), string(meta), at)
	if err != nil {
		return 0, false, err
	}
	affected, _ := res.RowsAffected()
	id, _ := res.LastInsertId()
	return id, affected > 0, nil
}

func nullableInt64(v *int64) any {
	if v == nil {
		return nil
	}
	return *v
}
