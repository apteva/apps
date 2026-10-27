package main

import (
	"errors"
	"sort"
	"time"

	sdk "github.com/apteva/app-sdk"
)

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
	symbols := map[string]any{}
	ready := 0
	for _, symbol := range []string{"SPY", "QQQ", "BTC-USD", "ETH-USD"} {
		m, err := dbGetMark(ctx.AppDB(), symbol)
		if err != nil {
			symbols[symbol] = map[string]any{"ready": false, "reason": "quote unavailable"}
			continue
		}
		open := true
		if m.Instrument != nil {
			session, err := marketSessionAt(m.Instrument.Calendar, time.Now().UTC())
			open = err == nil && session.IsOpen
		}
		fresh := markFresh(m, time.Now()) && m.Source != "mock"
		valid := fresh && open
		if inferAssetClass(symbol) == "crypto" {
			valid = valid && m.Instrument != nil && m.Instrument.QuoteCurrency == "USD"
		}
		if valid {
			ready++
		}
		symbols[symbol] = map[string]any{"ready": valid, "fresh": fresh, "session_open": open, "source": m.Source, "marked_at": m.MarkedAt}
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
	if !oneOfString(symbol, "SPY", "QQQ", "BTC-USD", "ETH-USD") {
		return nil, errors.New("symbol outside paper universe")
	}
	if globalEngine == nil {
		return nil, errors.New("market engine unavailable")
	}
	bars, err := globalEngine.provider.Bars(symbol, "5D")
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
	out["range"] = "5D"
	out["retrieved_at"] = paperNow()
	out["predictive_edge_validated"] = false
	if m, err := dbGetMark(ctx.AppDB(), symbol); err == nil {
		out["source"] = m.Source
		out["instrument"] = m.Instrument
	}
	return out, nil
}
