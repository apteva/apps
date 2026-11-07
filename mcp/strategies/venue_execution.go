package main

import (
	"database/sql"

	"errors"
	"fmt"
	"math"

	"strings"
	"sync"
	"time"
)

type VenueExecutionProfile struct {
	VenueSlug            string              `json:"venue_slug"`
	AssetClass           string              `json:"asset_class"`
	Symbol               string              `json:"symbol"`
	Status               string              `json:"status"`
	Calendar             string              `json:"calendar"`
	SessionPolicy        string              `json:"session_policy"`
	MakerFeeBps          float64             `json:"maker_fee_bps"`
	TakerFeeBps          float64             `json:"taker_fee_bps"`
	FeeCurrency          string              `json:"fee_currency"`
	SpreadModel          string              `json:"spread_model"`
	FallbackSpreadBps    float64             `json:"fallback_spread_bps"`
	SlippageModel        string              `json:"slippage_model"`
	SlippageBps          float64             `json:"slippage_bps"`
	MinQty               float64             `json:"min_qty"`
	MinNotional          float64             `json:"min_notional"`
	QtyStep              float64             `json:"qty_step"`
	PriceTick            float64             `json:"price_tick"`
	FundingRateBps       float64             `json:"funding_rate_bps"`
	FundingIntervalHours int                 `json:"funding_interval_hours"`
	SupportsPostOnly     bool                `json:"supports_post_only"`
	SupportsReduceOnly   bool                `json:"supports_reduce_only"`
	Source               string              `json:"source"`
	UpdatedAt            string              `json:"updated_at,omitempty"`
	Runtime              *VenueRuntimeHealth `json:"runtime,omitempty"`
}

type VenueRuntimeHealth struct {
	Status              string `json:"status"`
	ConsecutiveFailures int    `json:"consecutive_failures"`
	LastOKAt            string `json:"last_ok_at,omitempty"`
	LastErrorAt         string `json:"last_error_at,omitempty"`
	LastError           string `json:"last_error,omitempty"`
	RetryAt             string `json:"retry_at,omitempty"`
}

type ExecutionCost struct {
	ID              int64          `json:"id"`
	PortfolioID     int64          `json:"portfolio_id"`
	OrderID         string         `json:"order_id,omitempty"`
	FillID          *int64         `json:"fill_id,omitempty"`
	VenueSlug       string         `json:"venue_slug"`
	Symbol          string         `json:"symbol"`
	Kind            string         `json:"kind"`
	Amount          float64        `json:"amount"`
	Currency        string         `json:"currency"`
	RateBps         *float64       `json:"rate_bps,omitempty"`
	LiquidityRole   string         `json:"liquidity_role,omitempty"`
	ProviderEventID string         `json:"provider_event_id,omitempty"`
	Metadata        map[string]any `json:"metadata,omitempty"`
	OccurredAt      string         `json:"occurred_at"`
}

type executionViolation struct {
	Code   string
	Detail string
}

type simulationFill struct {
	Price          float64
	ReferencePrice float64
	TouchPrice     float64
	Fee            float64
	FeeBps         float64
	FeeCurrency    string
	LiquidityRole  string
	SpreadCost     float64
	SlippageCost   float64
}

var venueRuntime = struct {
	sync.RWMutex
	bySlug map[string]VenueRuntimeHealth
}{bySlug: map[string]VenueRuntimeHealth{}}

func executionVenue(pf *Portfolio) string {
	if pf != nil && strings.TrimSpace(pf.BrokerSlug) != "" {
		return strings.ToLower(strings.TrimSpace(pf.BrokerSlug))
	}
	return "simulation"
}

func defaultVenueProfile(venue, class string) VenueExecutionProfile {
	venue = strings.ToLower(strings.TrimSpace(venue))
	class = strings.ToLower(strings.TrimSpace(class))
	p := VenueExecutionProfile{
		VenueSlug: venue, AssetClass: class, Symbol: "*", Status: "active",
		Calendar: calendarAlwaysOpen, SessionPolicy: "continuous",
		FeeCurrency: "USD", SpreadModel: "quote", SlippageModel: "fixed_bps",
		SlippageBps: defaultSlippageBps, Source: "builtin",
	}
	if class == "equity" || class == "etf" {
		p.Calendar = calendarUSEquity
		if venue == "simulation" {
			p.SessionPolicy = "regular_only"
		} else {
			p.SessionPolicy = "venue_managed"
		}
	}
	switch venue {
	case "binance-trading", "okx", "bybit":
		p.FeeCurrency = "USDT"
	case "polymarket-clob":
		p.FeeCurrency = "USDC"
	}
	return p
}

func stringIn(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func scanVenueProfile(scanner interface{ Scan(...any) error }) (*VenueExecutionProfile, error) {
	var p VenueExecutionProfile
	var postOnly, reduceOnly int
	err := scanner.Scan(&p.VenueSlug, &p.AssetClass, &p.Symbol, &p.Status, &p.Calendar,
		&p.SessionPolicy, &p.MakerFeeBps, &p.TakerFeeBps, &p.FeeCurrency, &p.SpreadModel,
		&p.FallbackSpreadBps, &p.SlippageModel, &p.SlippageBps, &p.MinQty, &p.MinNotional,
		&p.QtyStep, &p.PriceTick, &p.FundingRateBps, &p.FundingIntervalHours,
		&postOnly, &reduceOnly, &p.Source, &p.UpdatedAt)
	p.SupportsPostOnly, p.SupportsReduceOnly = postOnly != 0, reduceOnly != 0
	return &p, err
}

const venueProfileColumns = `venue_slug,asset_class,symbol,status,calendar,session_policy,
	maker_fee_bps,taker_fee_bps,fee_currency,spread_model,fallback_spread_bps,
	slippage_model,slippage_bps,min_qty,min_notional,qty_step,price_tick,
	funding_rate_bps,funding_interval_hours,supports_post_only,supports_reduce_only,source,updated_at`

func dbVenueProfile(db *sql.DB, venue, class, symbol string) (*VenueExecutionProfile, error) {
	venue, class, symbol = strings.ToLower(strings.TrimSpace(venue)), strings.ToLower(strings.TrimSpace(class)), canonicalSymbol(symbol)
	for _, candidate := range []string{symbol, "*"} {
		if candidate == "" {
			continue
		}
		p, err := scanVenueProfile(db.QueryRow(`SELECT `+venueProfileColumns+` FROM venue_execution_profiles
			WHERE venue_slug=? AND asset_class=? AND symbol=?`, venue, class, candidate))
		if err == nil {
			return p, nil
		}
		if !errors.Is(err, sql.ErrNoRows) {
			return nil, err
		}
	}
	return nil, sql.ErrNoRows
}

func resolveVenueProfile(db *sql.DB, pf *Portfolio, symbol, class string) VenueExecutionProfile {
	venue := executionVenue(pf)
	p := defaultVenueProfile(venue, class)
	if stored, err := dbVenueProfile(db, venue, class, symbol); err == nil {
		p = *stored
	}
	if instrument, err := dbGetInstrument(db, symbol); err == nil && instrumentRulesMatchVenue(instrument, venue) {
		if instrument.Calendar != "" {
			p.Calendar = instrument.Calendar
		}
		if instrument.TickSize > 0 {
			p.PriceTick = instrument.TickSize
		}
		if instrument.LotSize > 0 {
			p.QtyStep = instrument.LotSize
		}
		if instrument.MinQty > 0 {
			p.MinQty = instrument.MinQty
		}
		if instrument.MinNotional > 0 {
			p.MinNotional = instrument.MinNotional
		}
		if instrument.QuoteCurrency != "" {
			p.FeeCurrency = instrument.QuoteCurrency
		}
	}

	settings := dbPortfolioExecutionSettings(db, pf.ID)
	if cfg, err := dbPortfolioConfig(db, pf.ID); err == nil {
		if _, ok := cfg["fee_bps"]; ok {
			p.MakerFeeBps, p.TakerFeeBps = settings.FeeBps, settings.FeeBps
		}
		if _, ok := cfg["slippage_bps"]; ok {
			p.SlippageBps = settings.SlippageBps
		}
	}
	p.Runtime = ptrVenueRuntime(venueRuntimeSnapshot(venue))
	return p
}

func instrumentRulesMatchVenue(instrument *Instrument, venue string) bool {
	if instrument == nil {
		return false
	}
	venue = strings.ToLower(strings.TrimSpace(venue))
	if venue == "simulation" {
		return true
	}
	source, exchange := strings.ToLower(instrument.Source), strings.ToLower(instrument.Exchange)
	switch venue {
	case "binance-trading":
		return strings.Contains(source, "binance") || strings.Contains(exchange, "binance")
	case "alpaca-trading":
		return strings.Contains(source, "alpaca") || instrument.AssetClass == "equity" || instrument.AssetClass == "etf"
	case "polymarket-clob":
		return strings.Contains(source, "polymarket") || strings.Contains(exchange, "polymarket")
	default:
		return strings.Contains(source, venue) || strings.Contains(exchange, venue)
	}
}

func ptrVenueRuntime(v VenueRuntimeHealth) *VenueRuntimeHealth { return &v }

func validateExecutionOrderAt(profile VenueExecutionProfile, instrument *Instrument, mark *Mark, qty, price float64, at time.Time) *executionViolation {
	if profile.Status == "maintenance" || profile.Status == "outage" {
		return &executionViolation{Code: "venue_unavailable", Detail: fmt.Sprintf("%s is %s", profile.VenueSlug, profile.Status)}
	}
	if open, retry := venueCircuitOpen(profile.VenueSlug, at); open {
		return &executionViolation{Code: "venue_circuit_open", Detail: fmt.Sprintf("%s temporarily unavailable after repeated broker errors; retry after %s", profile.VenueSlug, retry.Format(time.RFC3339))}
	}
	if instrument != nil {
		if !instrument.Active {
			return &executionViolation{Code: "instrument_inactive", Detail: fmt.Sprintf("%s is not active", instrument.Symbol)}
		}
		if instrument.ExpiresAt != "" {
			if expires, err := time.Parse(time.RFC3339, instrument.ExpiresAt); err == nil && !at.Before(expires) {
				return &executionViolation{Code: "instrument_expired", Detail: fmt.Sprintf("%s expired at %s", instrument.Symbol, expires.Format(time.RFC3339))}
			}
		}
	}

	if profile.SessionPolicy == "regular_only" && (mark == nil || (mark.Source != "mock" && mark.Source != "backtest")) {
		calendar := profile.Calendar
		if calendar == "" && instrument != nil && instrument.Calendar != "" {
			calendar = instrument.Calendar
		}
		if session, err := marketSessionAt(calendar, at); err != nil {
			return &executionViolation{Code: "session_unknown", Detail: err.Error()}
		} else if !session.IsOpen {
			detail := fmt.Sprintf("%s regular session is closed", calendar)
			if session.NextOpenAt != "" {
				detail += "; next open " + session.NextOpenAt
			}
			return &executionViolation{Code: "market_closed", Detail: detail}
		}
	}
	if profile.MinQty > 0 && qty+numericTolerance(profile.MinQty) < profile.MinQty {
		return &executionViolation{Code: "below_min_qty", Detail: fmt.Sprintf("qty %.12g is below venue minimum %.12g", qty, profile.MinQty)}
	}
	if profile.QtyStep > 0 && !multipleOf(qty, profile.QtyStep) {
		return &executionViolation{Code: "invalid_qty_step", Detail: fmt.Sprintf("qty %.12g must be a multiple of %.12g", qty, profile.QtyStep)}
	}
	if profile.MinNotional > 0 && qty*price+numericTolerance(profile.MinNotional) < profile.MinNotional {
		return &executionViolation{Code: "below_min_notional", Detail: fmt.Sprintf("notional %.8f is below venue minimum %.8f %s", qty*price, profile.MinNotional, profile.FeeCurrency)}
	}
	return nil
}

func numericTolerance(step float64) float64 { return math.Max(1e-9, math.Abs(step)*1e-8) }

func multipleOf(value, step float64) bool {
	if step <= 0 {
		return true
	}
	ratio := value / step
	return math.Abs(ratio-math.Round(ratio)) <= math.Max(1e-8, math.Abs(ratio)*1e-10)
}

func liquidityRole(orderType string) string {
	if orderType == "limit" {
		return "maker"
	}
	return "taker"
}

func profileFeeBps(profile VenueExecutionProfile, role string) float64 {
	if role == "maker" {
		return profile.MakerFeeBps
	}
	return profile.TakerFeeBps
}

func estimateSimulationExecution(mark *Mark, side, orderType string, qty, reference float64, profile VenueExecutionProfile, roles ...string) simulationFill {
	role := liquidityRole(orderType)
	if len(roles) > 0 && stringIn(roles[0], "maker", "taker") {
		role = roles[0]
	}
	result := simulationFill{ReferencePrice: reference, TouchPrice: reference, Price: reference,
		FeeCurrency: profile.FeeCurrency, LiquidityRole: role, FeeBps: profileFeeBps(profile, role)}

	if role == "maker" {
		result.Fee = fillFee(qty, result.Price, result.FeeBps)
		return result
	}
	buy := isBuySide(side)
	if mark != nil {
		mid := reference
		if profile.SpreadModel == "quote" && mark.BidPrice != nil && mark.AskPrice != nil && *mark.BidPrice > 0 && *mark.AskPrice >= *mark.BidPrice {
			mid = (*mark.BidPrice + *mark.AskPrice) / 2
			if buy {
				result.TouchPrice = *mark.AskPrice
			} else {
				result.TouchPrice = *mark.BidPrice
			}
			result.ReferencePrice = mid
		} else if profile.SpreadModel == "fixed_bps" && profile.FallbackSpreadBps > 0 {
			half := reference * profile.FallbackSpreadBps / 20_000
			if buy {
				result.TouchPrice = reference + half
			} else {
				result.TouchPrice = reference - half
			}
		}
	}
	result.Price = result.TouchPrice
	if role == "taker" && profile.SlippageModel == "fixed_bps" {
		result.Price = applySlippage(result.TouchPrice, side, profile.SlippageBps)
	}
	result.SpreadCost = math.Abs(result.TouchPrice-result.ReferencePrice) * qty
	result.SlippageCost = math.Abs(result.Price-result.TouchPrice) * qty
	result.Fee = fillFee(qty, result.Price, result.FeeBps)
	return result
}

func venueRuntimeSnapshot(venue string) VenueRuntimeHealth {
	venueRuntime.RLock()
	h, ok := venueRuntime.bySlug[strings.ToLower(strings.TrimSpace(venue))]
	venueRuntime.RUnlock()
	if !ok {
		h.Status = "healthy"
	}
	return h
}

func venueCircuitOpen(venue string, now time.Time) (bool, time.Time) {
	h := venueRuntimeSnapshot(venue)
	if h.Status != "outage" || h.RetryAt == "" {
		return false, time.Time{}
	}
	retry, err := time.Parse(time.RFC3339, h.RetryAt)
	if err != nil || !now.Before(retry) {
		return false, retry
	}
	return true, retry
}

func dbFundingPaid(db *sql.DB, portfolioID int64) float64 {
	var amount float64
	_ = db.QueryRow(`SELECT COALESCE(SUM(amount),0) FROM execution_costs WHERE portfolio_id=? AND kind='funding'`, portfolioID).Scan(&amount)
	return amount
}
