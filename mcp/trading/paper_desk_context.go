package main

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func paperEntryClass(pf *Portfolio, class string) error {
	if !oneOfString(class, "equity", "etf", "crypto", "polymarket") {
		return fmt.Errorf("%s is unsupported in the paper desk", class)
	}
	if !contains(pf.AllowedClasses, class) {
		return fmt.Errorf("asset class %s is not allowed by this portfolio", class)
	}
	return nil
}

func (a *App) toolPaperUniverse(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pf, _, err := paperScope(ctx, args)
	if err != nil {
		return nil, err
	}
	class := strings.ToLower(strArg(args, "asset_class"))
	if class != "" && !oneOfString(class, "equity", "etf", "crypto", "polymarket") {
		return nil, errors.New("unknown asset class")
	}
	limit := intArg(args, "limit", 50)
	if limit < 1 || limit > 200 {
		return nil, errors.New("limit must be 1..200")
	}
	after := canonicalSymbol(strArg(args, "after_symbol"))
	db := ctx.AppDB()
	policy, err := dbGetPortfolioUniversePolicy(db, pf)
	if err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT symbol,asset_class,source,volume_24h,marked_at FROM marks
		WHERE symbol>? AND (?='' OR asset_class=?) ORDER BY symbol LIMIT ?`, after, class, class, limit+1)
	if err != nil {
		return nil, err
	}
	type instrument struct {
		Symbol             string   `json:"symbol"`
		AssetClass         string   `json:"asset_class"`
		Source             string   `json:"source"`
		Volume             *float64 `json:"volume_24h,omitempty"`
		MarkedAt           string   `json:"marked_at"`
		EntryPolicyAllowed bool     `json:"entry_policy_allowed"`
		EntryPolicyReason  string   `json:"entry_policy_reason,omitempty"`
	}
	result := make([]instrument, 0, limit+1)
	for rows.Next() {
		var i instrument
		if err = rows.Scan(&i.Symbol, &i.AssetClass, &i.Source, &i.Volume, &i.MarkedAt); err != nil {
			rows.Close()
			return nil, err
		}
		result = append(result, i)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	next := ""
	if len(result) > limit {
		result = result[:limit]
		next = result[limit-1].Symbol
	}
	for index := range result {
		i := &result[index]
		if err := paperEntryClass(pf, i.AssetClass); err != nil {
			i.EntryPolicyReason = err.Error()
			continue
		}
		violation, err := universeViolation(db, policy, i.Symbol)
		if err != nil {
			return nil, err
		}
		i.EntryPolicyAllowed = violation == nil
		if violation != nil {
			i.EntryPolicyReason = violation.Detail
		}
	}
	return map[string]any{"instruments": result, "next_after_symbol": next}, nil
}

func (a *App) toolPaperHealth(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pf, _, err := paperScope(ctx, args)
	if err != nil {
		return nil, err
	}
	positionResult, err := a.toolPositionsList(ctx, map[string]any{"_project_id": pf.ProjectID, "portfolio_id": pf.ID})
	if err != nil {
		return nil, err
	}
	positions := positionResult.(map[string]any)["positions"]
	snapshot, err := snapshotPortfolio(ctx.AppDB(), pf)
	if err != nil {
		return nil, err
	}
	orders, err := workingPortfolioOrders(ctx.AppDB(), pf.ID)
	if err != nil {
		return nil, err
	}
	if orders == nil {
		orders = []*Order{}
	}
	var planErrors int
	if err = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM paper_decisions WHERE portfolio_id=? AND status IN ('submitted','active','exiting') AND last_error<>''`, pf.ID).Scan(&planErrors); err != nil {
		return nil, err
	}
	db := ctx.AppDB()
	policy, err := dbGetPortfolioUniversePolicy(db, pf)
	if err != nil {
		return nil, err
	}
	marks, err := dbListMarks(db)
	if err != nil {
		return nil, err
	}
	symbols := map[string]any{}
	for _, symbol := range policy.IncludeSymbols {
		symbols[symbol] = map[string]any{"ready": false, "reason": "eligible quote unavailable"}
	}
	now := time.Now().UTC()
	ready := 0
	for _, m := range marks {
		if paperEntryClass(pf, m.AssetClass) != nil {
			continue
		}
		violation, err := universeViolation(db, policy, m.Symbol)
		if err != nil {
			return nil, err
		}
		if violation != nil {
			continue
		}
		instrument, err := dbGetInstrument(db, m.Symbol)
		if err != nil {
			symbols[m.Symbol] = map[string]any{"ready": false, "reason": "instrument metadata unavailable"}
			continue
		}
		m.Instrument = instrument
		profile := resolveVenueProfile(db, pf, m.Symbol, m.AssetClass)
		session, err := marketSessionAt(profile.Calendar, now)
		open := err == nil && session.IsOpen
		outcome := ""
		if m.AssetClass == "polymarket" {
			outcome = "yes"
		}
		_, markErr := paperTradeMark(m, m.Symbol, outcome)
		fresh := markFresh(m, now) && m.Source != "" && m.Source != "mock" && m.Source != "backtest"
		// Use a valid minimum size here to report venue/instrument readiness;
		// actual order size and costs are checked independently at preflight.
		qty := max(profile.MinQty, profile.QtyStep, 1)
		if m.Price > 0 {
			qty = max(qty, profile.MinNotional/m.Price)
		}
		if profile.QtyStep > 0 {
			qty = math.Ceil(qty/profile.QtyStep) * profile.QtyStep
		}
		check := validateExecutionOrderAt(profile, instrument, m, qty, m.Price, now)
		valid := markErr == nil && open && check == nil
		if valid {
			ready++
		}
		state := map[string]any{"ready": valid, "fresh": fresh, "session_open": open, "source": m.Source, "marked_at": m.MarkedAt}
		if markErr != nil {
			state["reason"] = markErr.Error()
		} else if check != nil {
			state["reason"] = check.Detail
		}
		symbols[m.Symbol] = state
	}
	return map[string]any{"portfolio_id": pf.ID, "status": pf.Status, "mode": pf.Mode, "cash": pf.Cash, "equity": snapshot.Equity, "open_pnl": snapshot.OpenPnL, "total_pnl": snapshot.TotalPnL, "fees_paid": snapshot.FeesPaid, "positions": positions, "working_orders": orders, "symbols": symbols, "exit_plan_errors": planErrors, "ready_for_entries": pf.Status == "active" && ready > 0 && planErrors == 0, "checked_at": paperNow()}, nil
}

func completedPaperContext(bars []Bar, now time.Time) (map[string]any, error) {
	if len(bars) < 3 {
		return nil, errors.New("insufficient history")
	}
	intervals := map[int64]int{}
	step, count := int64(0), 0
	for i := 1; i < len(bars); i++ {
		gap := bars[i].T - bars[i-1].T
		if gap > 0 {
			intervals[gap]++
			if intervals[gap] > count {
				step, count = gap, intervals[gap]
			}
		}
	}
	if step <= 0 {
		return nil, errors.New("cannot identify candle interval")
	}
	var closed []Bar
	for _, bar := range bars {
		if bar.T+step <= now.Unix() {
			closed = append(closed, bar)
		}
	}
	if len(closed) < 51 {
		return nil, errors.New("at least 51 completed source candles required for context")
	}
	sma := func(n int) float64 {
		sum := 0.0
		for _, b := range closed[len(closed)-n:] {
			sum += b.C
		}
		return sum / float64(n)
	}
	last, prior := closed[len(closed)-1], closed[len(closed)-2]
	previous := closed[len(closed)-21 : len(closed)-1]
	hi, lo, volume := previous[0].H, previous[0].L, 0.0
	for _, b := range previous {
		hi = max(hi, b.H)
		lo = min(lo, b.L)
		volume += b.V
	}
	position := 0.5
	if hi > lo {
		position = (last.L - lo) / (hi - lo)
	}
	return map[string]any{"bar_seconds": step, "completed_bars": len(closed), "latest": last, "prior": prior, "sma20": sma(20), "sma50": sma(50), "previous20_high": hi, "previous20_low": lo, "previous20_average_volume": volume / 20, "latest_low_range_fraction": position, "latest_closed_at": time.Unix(last.T+step, 0).UTC().Format(time.RFC3339), "forming_bars_excluded": len(bars) - len(closed)}, nil
}

func (a *App) toolPaperMarketContext(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if _, _, err := paperScope(ctx, args); err != nil {
		return nil, err
	}
	symbol := canonicalSymbol(strArg(args, "symbol"))
	if symbol == "" {
		return nil, errors.New("symbol required")
	}
	rangeName := strings.ToUpper(strings.TrimSpace(strArg(args, "range")))
	if rangeName == "" {
		rangeName = "5D"
	}
	if !oneOfString(rangeName, "1D", "5D", "1M", "3M", "1Y", "ALL") {
		return nil, errors.New("range must be 1D, 5D, 1M, 3M, 1Y or ALL")
	}
	if inferAssetClass(symbol) == "polymarket" {
		return nil, errors.New("use market_quote and market_history for prediction-market research")
	}
	if globalEngine == nil || globalEngine.provider == nil {
		return nil, errors.New("market engine unavailable")
	}
	bars, err := globalEngine.provider.Bars(symbol, rangeName)
	if err != nil {
		return nil, err
	}
	bars, err = normalizeBars(symbol, "desk-context", bars)
	if err != nil {
		return nil, err
	}
	sort.Slice(bars, func(i, j int) bool { return bars[i].T < bars[j].T })
	out, err := completedPaperContext(bars, time.Now().UTC())
	if err != nil {
		return nil, err
	}
	out["symbol"] = symbol
	out["range"] = rangeName
	out["retrieved_at"] = paperNow()
	out["predictive_edge_validated"] = false
	if m, err := dbGetMark(ctx.AppDB(), symbol); err == nil {
		out["source"] = m.Source
		out["instrument"] = m.Instrument
	}
	return out, nil
}
