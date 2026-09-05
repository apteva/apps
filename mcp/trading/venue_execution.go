package main

// Generic execution rules shared by simulation and broker-backed portfolios.
// Adapters still translate wire formats; this file owns the portable rules
// that must be applied consistently before an order reaches any venue.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sort"
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

const venueCircuitThreshold = 5

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

func defaultVenueProfiles() []VenueExecutionProfile {
	seen := map[string]bool{}
	var out []VenueExecutionProfile
	add := func(venue, class string) {
		key := venue + "\x00" + class
		if seen[key] {
			return
		}
		seen[key] = true
		out = append(out, defaultVenueProfile(venue, class))
	}
	for _, class := range []string{"equity", "etf", "crypto", "polymarket"} {
		add("simulation", class)
	}
	for _, adapter := range allAdapters() {
		for _, class := range adapter.Capabilities().AssetClasses {
			add(adapter.Slug(), class)
		}
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].VenueSlug == out[j].VenueSlug {
			return out[i].AssetClass < out[j].AssetClass
		}
		return out[i].VenueSlug < out[j].VenueSlug
	})
	return out
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

func stringIn(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func firstError(values ...error) error {
	for _, value := range values {
		if value != nil {
			return value
		}
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

func dbStoredVenueProfiles(db *sql.DB) ([]VenueExecutionProfile, error) {
	rows, err := db.Query(`SELECT ` + venueProfileColumns + ` FROM venue_execution_profiles ORDER BY venue_slug,asset_class,symbol`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []VenueExecutionProfile
	for rows.Next() {
		p, err := scanVenueProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *p)
	}
	return out, rows.Err()
}

func effectiveVenueProfiles(db *sql.DB) ([]VenueExecutionProfile, error) {
	defaults := defaultVenueProfiles()
	stored, err := dbStoredVenueProfiles(db)
	if err != nil {
		return nil, err
	}
	byKey := map[string]VenueExecutionProfile{}
	for _, p := range defaults {
		byKey[p.VenueSlug+"\x00"+p.AssetClass+"\x00"+p.Symbol] = p
	}
	for _, p := range stored {
		byKey[p.VenueSlug+"\x00"+p.AssetClass+"\x00"+p.Symbol] = p
	}
	out := make([]VenueExecutionProfile, 0, len(byKey))
	for _, p := range byKey {
		runtime := venueRuntimeSnapshot(p.VenueSlug)
		p.Runtime = &runtime
		out = append(out, p)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].VenueSlug != out[j].VenueSlug {
			return out[i].VenueSlug < out[j].VenueSlug
		}
		if out[i].AssetClass != out[j].AssetClass {
			return out[i].AssetClass < out[j].AssetClass
		}
		return out[i].Symbol < out[j].Symbol
	})
	return out, nil
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
	// Compatibility: explicit portfolio settings remain the most specific
	// override and apply to both maker and taker until separately configured.
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

func validateExecutionOrder(profile VenueExecutionProfile, instrument *Instrument, mark *Mark, qty, price float64) *executionViolation {
	return validateExecutionOrderAt(profile, instrument, mark, qty, price, time.Now().UTC())
}

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
	// Mock/replay marks deliberately bypass wall-clock session enforcement;
	// historical engines align bars to sessions independently.
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

func validatePriceTick(profile VenueExecutionProfile, value float64, field string) *executionViolation {
	if value > 0 && profile.PriceTick > 0 && !multipleOf(value, profile.PriceTick) {
		return &executionViolation{Code: "invalid_price_tick", Detail: fmt.Sprintf("%s %.12g must be a multiple of %.12g", field, value, profile.PriceTick)}
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

func orderLiquidityAtPlacement(mark *Mark, side, orderType string, limit *float64) string {
	if orderType != "limit" || limit == nil {
		return "taker"
	}
	if mark == nil {
		return "maker"
	}
	price := mark.Price
	if isBuySide(side) {
		if mark.AskPrice != nil && *mark.AskPrice > 0 {
			price = *mark.AskPrice
		}
		if *limit >= price {
			return "taker"
		}
	} else {
		if mark.BidPrice != nil && *mark.BidPrice > 0 {
			price = *mark.BidPrice
		}
		if *limit <= price {
			return "taker"
		}
	}
	return "maker"
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
	// A resting maker order does not cross the spread or consume taker
	// liquidity. Its eventual limit price is applied by tryFill once the
	// opposite quote reaches it, so the generic estimate only models costs
	// that are inherent to the liquidity role here.
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

func noteVenueCall(venue string, err error) {
	venue = strings.ToLower(strings.TrimSpace(venue))
	if venue == "" || venue == "simulation" {
		return
	}
	now := time.Now().UTC()
	venueRuntime.Lock()
	previous, existed := venueRuntime.bySlug[venue]
	h := previous
	if err == nil {
		h.Status, h.ConsecutiveFailures, h.LastError, h.RetryAt = "healthy", 0, "", ""
		h.LastOKAt = now.Format(time.RFC3339)
	} else {
		h.ConsecutiveFailures++
		h.LastError, h.LastErrorAt = err.Error(), now.Format(time.RFC3339)
		h.Status = "degraded"
		if h.ConsecutiveFailures >= venueCircuitThreshold {
			h.Status = "outage"
			exponent := h.ConsecutiveFailures - venueCircuitThreshold
			if exponent > 4 {
				exponent = 4
			}
			retry := now.Add(time.Duration(30*(1<<exponent)) * time.Second)
			h.RetryAt = retry.Format(time.RFC3339)
		}
	}
	venueRuntime.bySlug[venue] = h
	venueRuntime.Unlock()
	if !existed || previous.Status != h.Status || previous.ConsecutiveFailures != h.ConsecutiveFailures {
		emit("venue.health.changed", map[string]any{"venue_slug": venue, "runtime": h})
	}
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

func dbExecutionCosts(db *sql.DB, portfolioID int64, limit int) ([]ExecutionCost, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := db.Query(`SELECT id,portfolio_id,COALESCE(order_id,''),fill_id,venue_slug,symbol,kind,amount,currency,
		rate_bps,COALESCE(liquidity_role,''),COALESCE(provider_event_id,''),metadata,occurred_at
		FROM execution_costs WHERE portfolio_id=? ORDER BY occurred_at DESC,id DESC LIMIT ?`, portfolioID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []ExecutionCost
	for rows.Next() {
		var c ExecutionCost
		var fillID sql.NullInt64
		var rate sql.NullFloat64
		var meta string
		if err := rows.Scan(&c.ID, &c.PortfolioID, &c.OrderID, &fillID, &c.VenueSlug, &c.Symbol, &c.Kind, &c.Amount,
			&c.Currency, &rate, &c.LiquidityRole, &c.ProviderEventID, &meta, &c.OccurredAt); err != nil {
			return nil, err
		}
		if fillID.Valid {
			v := fillID.Int64
			c.FillID = &v
		}
		if rate.Valid {
			v := rate.Float64
			c.RateBps = &v
		}
		_ = json.Unmarshal([]byte(meta), &c.Metadata)
		out = append(out, c)
	}
	return out, rows.Err()
}

func dbFundingPaid(db *sql.DB, portfolioID int64) float64 {
	var amount float64
	_ = db.QueryRow(`SELECT COALESCE(SUM(amount),0) FROM execution_costs WHERE portfolio_id=? AND kind='funding'`, portfolioID).Scan(&amount)
	return amount
}

func convertCommissionToQuote(db *sql.DB, o *Order, amount float64, asset, quote string, fillPrice float64) (float64, bool) {
	asset, quote = strings.ToUpper(strings.TrimSpace(asset)), strings.ToUpper(strings.TrimSpace(quote))
	if amount == 0 {
		return 0, true
	}
	if asset == "" || asset == quote || (asset == "USDT" && quote == "USD") || (asset == "USDC" && quote == "USD") || (asset == "USD" && (quote == "USDT" || quote == "USDC")) {
		return amount, true
	}
	base := strings.ToUpper(strings.TrimSuffix(o.Symbol, "-USD"))
	if asset == base && fillPrice > 0 {
		return amount * fillPrice, true
	}
	if mark, err := dbGetMark(db, asset+"-USD"); err == nil && mark.Price > 0 && markFresh(mark, time.Now()) {
		return amount * mark.Price, true
	}
	return 0, false
}
