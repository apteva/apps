package main

import (
	"database/sql"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Trading owns account state. Strategies reads it through these app-only
// Trading tools when it needs it and never stores it, except for the inputs
// a backtest freezes into its own run spec.

// TradingState is Trading's portfolio_state: the account view one live
// decision is sized from. It carries no order or fill history.
type TradingState struct {
	Version           int                              `json:"version"`
	SourceInstallID   int64                            `json:"source_install_id"`
	Revision          int64                            `json:"revision"`
	ObservedAt        string                           `json:"observed_at"`
	Portfolio         *Portfolio                       `json:"portfolio"`
	Config            map[string]any                   `json:"config"`
	ExecutionSettings PortfolioExecutionSettings       `json:"execution_settings"`
	Equity            float64                          `json:"equity"`
	DayBaseline       *float64                         `json:"day_baseline,omitempty"`
	Positions         []*Position                      `json:"positions"`
	WorkingOrders     []*Order                         `json:"working_orders"`
	Marks             map[string]*Mark                 `json:"marks"`
	Instruments       map[string]*Instrument           `json:"instruments"`
	Profiles          map[string]VenueExecutionProfile `json:"profiles"`
	RiskPolicy        *PortfolioRiskPolicy             `json:"risk_policy"`
	UniversePolicy    *PortfolioUniversePolicy         `json:"universe_policy"`
	Violations        map[string]*UniverseViolation    `json:"violations"`
}

func (s *TradingState) Mark(symbol string) *Mark {
	if s == nil {
		return nil
	}
	return s.Marks[canonicalSymbol(symbol)]
}

// Profile returns Trading's resolved venue profile, or the venue default
// when Trading did not resolve one for the symbol.
func (s *TradingState) Profile(symbol string) VenueExecutionProfile {
	if p, ok := s.Profiles[canonicalSymbol(symbol)]; ok {
		return p
	}
	return defaultVenueProfile(executionVenue(s.Portfolio), inferAssetClass(symbol))
}

func (s *TradingState) UsesBacktestPricing() bool {
	cfg := s.Config
	return fmt.Sprint(cfg["source_override"]) == "backtest" || fmt.Sprint(cfg["pricing_mode"]) == "backtest" || fmt.Sprint(cfg["source"]) == "backtest"
}

// tradingPortfolio reads one portfolio from Trading, scoped to the project.
func tradingPortfolio(ctx *sdk.AppCtx, pid string, id int64) (*Portfolio, error) {
	if id <= 0 {
		return nil, errors.New("portfolio_id required")
	}
	state, err := newTradingReader(ctx).State(pid, id, nil)
	if err != nil {
		return nil, err
	}
	return state.Portfolio, nil
}

// checkTradingBinding pins a project to the Trading installation it first
// read. Portfolio ids, strategy books and backtests refer to that
// installation; another one has different portfolios under the same ids.
func checkTradingBinding(db *sql.DB, pid string, install int64) error {
	if install <= 0 {
		return nil
	}
	if _, err := db.Exec(`INSERT INTO trading_binding(project_id,source_install_id) VALUES(?,?) ON CONFLICT DO NOTHING`, pid, install); err != nil {
		return err
	}
	var bound int64
	if err := db.QueryRow(`SELECT source_install_id FROM trading_binding WHERE project_id=?`, pid).Scan(&bound); err != nil {
		return err
	}
	if bound != install {
		return errors.New("Trading binding changed; use a new Strategies installation to preserve research portfolio identities")
	}
	return nil
}

// TradingReference is research_reference, all pages merged.
type TradingReference struct {
	Start       string                      `json:"start"`
	End         string                      `json:"end"`
	Adjustment  string                      `json:"adjustment"`
	Status      map[string]any              `json:"status"`
	SecurityIDs map[string]string           `json:"security_ids"`
	Tables      map[string][]map[string]any `json:"tables"`
	NextCursor  string                      `json:"next_cursor"`
}

// TradingFill is one fills_feed row: a fill of an order Strategies placed.
type TradingFill struct {
	ID                      int64   `json:"id"`
	OrderID                 string  `json:"order_id"`
	PortfolioID             int64   `json:"portfolio_id"`
	Symbol                  string  `json:"symbol"`
	AssetClass              string  `json:"asset_class"`
	Side                    string  `json:"side"`
	Outcome                 string  `json:"outcome"`
	Qty                     float64 `json:"qty"`
	Price                   float64 `json:"price"`
	Fee                     float64 `json:"fee"`
	SpreadCost              float64 `json:"spread_cost"`
	SlippageCost            float64 `json:"slippage_cost"`
	FilledAt                string  `json:"filled_at"`
	DecisionRef             string  `json:"decision_ref"`
	ExternalStrategyID      int64   `json:"external_strategy_id"`
	ExternalStrategyVersion int64   `json:"external_strategy_version"`
}

type tradingReader interface {
	State(pid string, portfolioID int64, symbols []string) (*TradingState, error)
	PolicyCheck(pid string, portfolioID int64, symbols []string, asOf time.Time) (map[string]*UniverseViolation, error)
	Reference(pid string, symbols []string, start, end, adjustment string) (*TradingReference, error)
	FillsSince(pid string, afterID int64, limit int, includeLegacy bool) (fills []TradingFill, next int64, more bool, err error)
}

// newTradingReader is the seam tests replace when they need a reader that is
// not backed by the platform's app calls.
var newTradingReader = func(ctx *sdk.AppCtx) tradingReader { return platformTradingReader{ctx: ctx} }

type platformTradingReader struct{ ctx *sdk.AppCtx }

func sortedSymbols(symbols []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(symbols))
	for _, symbol := range symbols {
		if symbol = canonicalSymbol(symbol); symbol != "" && !seen[symbol] {
			seen[symbol] = true
			out = append(out, symbol)
		}
	}
	sort.Strings(out)
	return out
}

func (r platformTradingReader) State(pid string, portfolioID int64, symbols []string) (*TradingState, error) {
	var state TradingState
	if err := callTrading(r.ctx, "portfolio_state", map[string]any{"_project_id": pid, "portfolio_id": portfolioID, "symbols": sortedSymbols(symbols)}, &state); err != nil {
		return nil, err
	}
	if state.Portfolio == nil || state.Portfolio.ID != portfolioID {
		return nil, errors.New("Trading returned no portfolio state")
	}
	if err := checkTradingBinding(r.ctx.AppDB(), pid, state.SourceInstallID); err != nil {
		return nil, err
	}
	if state.Marks == nil {
		state.Marks = map[string]*Mark{}
	}
	if state.Violations == nil {
		state.Violations = map[string]*UniverseViolation{}
	}
	if state.Config == nil {
		state.Config = map[string]any{}
	}
	return &state, nil
}

func (r platformTradingReader) PolicyCheck(pid string, portfolioID int64, symbols []string, asOf time.Time) (map[string]*UniverseViolation, error) {
	var out struct {
		Violations map[string]*UniverseViolation `json:"violations"`
	}
	args := map[string]any{"_project_id": pid, "portfolio_id": portfolioID, "symbols": sortedSymbols(symbols)}
	if !asOf.IsZero() {
		args["as_of"] = asOf.UTC().Format(time.RFC3339)
	}
	if err := callTrading(r.ctx, "portfolio_policy_check", args, &out); err != nil {
		return nil, err
	}
	if out.Violations == nil {
		out.Violations = map[string]*UniverseViolation{}
	}
	return out.Violations, nil
}

const maxReferencePages = 200

func (r platformTradingReader) Reference(pid string, symbols []string, start, end, adjustment string) (*TradingReference, error) {
	merged := &TradingReference{Start: start, End: end, Adjustment: adjustment, Tables: map[string][]map[string]any{}}
	cursor := ""
	for page := 0; page < maxReferencePages; page++ {
		var out TradingReference
		args := map[string]any{"_project_id": pid, "symbols": sortedSymbols(symbols), "start": start, "end": end, "adjustment": adjustment}
		if cursor != "" {
			args["cursor"] = cursor
		}
		if err := callTrading(r.ctx, "research_reference", args, &out); err != nil {
			return nil, err
		}
		if page == 0 {
			merged.Status, merged.SecurityIDs = out.Status, out.SecurityIDs
		}
		for table, rows := range out.Tables {
			merged.Tables[table] = append(merged.Tables[table], rows...)
		}
		if out.NextCursor == "" {
			if merged.SecurityIDs == nil {
				merged.SecurityIDs = map[string]string{}
			}
			rememberExchangeSessions(merged.Tables["exchange_sessions"])
			return merged, nil
		}
		if out.NextCursor == cursor {
			return nil, errors.New("Trading research_reference cursor did not advance")
		}
		cursor = out.NextCursor
	}
	return nil, errors.New("Trading research_reference exceeded the page limit")
}

func (r platformTradingReader) FillsSince(pid string, afterID int64, limit int, includeLegacy bool) ([]TradingFill, int64, bool, error) {
	var out struct {
		Fills      []TradingFill `json:"fills"`
		NextFillID int64         `json:"next_fill_id"`
		HasMore    bool          `json:"has_more"`
	}
	args := map[string]any{"_project_id": pid, "after_fill_id": afterID, "limit": limit}
	if includeLegacy {
		args["include_legacy"] = true
	}
	if err := callTrading(r.ctx, "fills_feed", args, &out); err != nil {
		return nil, afterID, false, err
	}
	if out.NextFillID < afterID {
		return nil, afterID, false, errors.New("Trading fills_feed moved backwards")
	}
	return out.Fills, out.NextFillID, out.HasMore, nil
}

// Provider-published US equity sessions seen in research_reference. Sessions
// are global facts, so one process-wide book serves every simulation; the
// rules calendar remains the fallback for dates Trading has not published.
var exchangeSessionBook = struct {
	sync.RWMutex
	byDate map[string]*MarketSession
}{byDate: map[string]*MarketSession{}}

func rememberExchangeSessions(rows []map[string]any) {
	exchangeSessionBook.Lock()
	defer exchangeSessionBook.Unlock()
	for _, row := range rows {
		if strArg(row, "venue") != calendarUSEquity || strArg(row, "session_type") != "regular" {
			continue
		}
		date := strArg(row, "session_date")
		if current, ok := exchangeSessionBook.byDate[date]; ok && current.Reason == "authoritative_session:alpaca" && strArg(row, "source") != "alpaca" {
			continue
		}
		exchangeSessionBook.byDate[date] = &MarketSession{
			Calendar: calendarUSEquity, Timezone: "America/New_York", OpenDay: strArg(row, "status") == "open",
			OpenAt: strArg(row, "open_at"), CloseAt: strArg(row, "close_at"), Reason: "authoritative_session:" + strings.ToLower(strArg(row, "source")),
		}
	}
}

// storedUSEquitySessionAt prefers provider-published sessions. The rules
// engine remains the fallback for dates Trading has not published.
func storedUSEquitySessionAt(at time.Time) *MarketSession {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		return nil
	}
	exchangeSessionBook.RLock()
	stored := exchangeSessionBook.byDate[at.In(location).Format("2006-01-02")]
	exchangeSessionBook.RUnlock()
	if stored == nil {
		return nil
	}
	out := *stored
	out.EvaluatedAt, out.Reason = at.UTC().Format(time.RFC3339Nano), "authoritative_session"
	open, openErr := time.Parse(time.RFC3339, out.OpenAt)
	closeAt, closeErr := time.Parse(time.RFC3339, out.CloseAt)
	if out.OpenDay && openErr == nil && closeErr == nil {
		out.OpenAt, out.CloseAt = open.UTC().Format(time.RFC3339), closeAt.UTC().Format(time.RFC3339)
		out.IsOpen = !at.Before(open) && at.Before(closeAt)
	} else {
		out.OpenAt, out.CloseAt = "", ""
	}
	if !out.IsOpen {
		if next := usEquityBookNextOpen(at); next != "" {
			out.NextOpenAt = next
		}
	}
	return &out
}

func usEquityBookNextOpen(at time.Time) string {
	exchangeSessionBook.RLock()
	defer exchangeSessionBook.RUnlock()
	best := ""
	for _, session := range exchangeSessionBook.byDate {
		if !session.OpenDay {
			continue
		}
		open, err := time.Parse(time.RFC3339, session.OpenAt)
		if err != nil || !open.After(at) {
			continue
		}
		if formatted := open.UTC().Format(time.RFC3339); best == "" || formatted < best {
			best = formatted
		}
	}
	return best
}

// tradingBackoff spaces out reads of a portfolio after Trading failed, from
// 30 seconds up to 15 minutes. It lives in memory only.
var tradingBackoff = struct {
	sync.Mutex
	next  map[int64]time.Time
	delay map[int64]time.Duration
}{next: map[int64]time.Time{}, delay: map[int64]time.Duration{}}

func tradingReadDeferred(portfolioID int64, now time.Time) bool {
	tradingBackoff.Lock()
	defer tradingBackoff.Unlock()
	return now.Before(tradingBackoff.next[portfolioID])
}

func recordTradingRead(portfolioID int64, now time.Time, err error) {
	tradingBackoff.Lock()
	defer tradingBackoff.Unlock()
	if err == nil {
		delete(tradingBackoff.next, portfolioID)
		delete(tradingBackoff.delay, portfolioID)
		return
	}
	delay := tradingBackoff.delay[portfolioID] * 2
	if delay < 30*time.Second {
		delay = 30 * time.Second
	}
	if delay > 15*time.Minute {
		delay = 15 * time.Minute
	}
	tradingBackoff.delay[portfolioID], tradingBackoff.next[portfolioID] = delay, now.Add(delay)
}
