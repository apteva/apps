package main

import (
	"fmt"
	"testing"
	"time"
)

func TestStrategyReplayRetainsRealizedPnLThroughCloseAndResume(t *testing.T) {
	ctx := newTestCtx(t)
	pid := mustCreatePortfolio(t, ctx, "PnL", []string{"crypto"})
	run := &BacktestRun{ProjectID: "test-proj", PortfolioID: pid, Symbols: []string{"BTC-USD"}, StartingCash: 1000, Interval: "1d", TotalSteps: 4}
	id, err := dbCreateBacktestRun(ctx.AppDB(), run)
	if err != nil {
		t.Fatal(err)
	}
	run.ID, run.Summary = id, nil // Exercise accounting without venue costs or risk limits.
	state := &strategyBacktestState{Cash: 1000, Positions: map[string]*Position{}}
	prices := func(price float64) []map[string]any {
		return []map[string]any{{"symbol": "BTC-USD", "price": price}}
	}
	applyStrategyTargets(run, state, []StrategyAllocation{{Symbol: "BTC-USD", Weight: 1}}, prices(100))
	// Close half, persist, and reload while a position still exists.
	applyStrategyTargets(run, state, []StrategyAllocation{{Symbol: "BTC-USD", Weight: 0.5}}, prices(120))
	partial := strategySnapshot(run, 1, state, prices(120), nil)
	assertClose(t, "partial realized", partial.RealizedPnL, 100)
	if err := dbUpsertBacktestSnapshot(ctx.AppDB(), partial); err != nil {
		t.Fatal(err)
	}
	state, err = loadStrategyBacktestState(run)
	if err != nil {
		t.Fatal(err)
	}
	applyStrategyTargets(run, state, nil, prices(120))
	closed := strategySnapshot(run, 2, state, prices(120), nil)
	assertClose(t, "closed cash", closed.Cash, 1200)
	assertClose(t, "closed realized", closed.RealizedPnL, 200)
	if len(closed.Positions) != 0 {
		t.Fatal("position should be closed")
	}
	if err := dbUpsertBacktestSnapshot(ctx.AppDB(), closed); err != nil {
		t.Fatal(err)
	}
	state, err = loadStrategyBacktestState(run)
	if err != nil {
		t.Fatal(err)
	}
	// Reopen and lose $100; the earlier $200 profit must still be counted.
	applyStrategyTargets(run, state, []StrategyAllocation{{Symbol: "BTC-USD", Weight: 1}}, prices(120))
	applyStrategyTargets(run, state, nil, prices(110))
	final := strategySnapshot(run, 3, state, prices(110), nil)
	assertClose(t, "final realized", final.RealizedPnL, 100)
	assertClose(t, "final equity", final.Equity, 1100)
}

func TestStrategyReplayUsesCompletedDailyCandles(t *testing.T) {
	for _, symbol := range []string{"BTC-USD", "AAPL"} {
		t.Run(symbol, func(t *testing.T) {
			def := &StrategyDefinition{Universe: []string{symbol}, Cadence: "1d"}
			run := &BacktestRun{Symbols: def.Universe, Interval: "1h"}
			market := strategyMarket{history: map[string][]float64{}, prices: map[string]float64{}}
			step := 0
			for day, date := range []string{"2026-11-25", "2026-11-27", "2026-11-30"} {
				at, _ := time.Parse(time.RFC3339, date+"T12:00:00Z")
				start, end := at.Truncate(24*time.Hour), at.Truncate(24*time.Hour).Add(24*time.Hour)
				if symbol == "AAPL" {
					session := usEquitySessionAt(at)
					start, end = session.Open, session.Close
				}
				for at = start; at.Before(end); at = at.Add(time.Hour) {
					step++
					last := !at.Add(time.Hour).Before(end)
					price := 9999.0 // Forming candles must not reach the indicators.
					if last {
						price = 100 + float64(day)*20
					}
					appendStrategyReplayBar(run, def, &market, &BacktestMarketBar{Step: step, Symbol: symbol, T: at.Unix(), C: price})
					want := day
					if last {
						want++
					}
					if len(market.history[symbol]) != want || strategyReplaySignalDue(def, step, market) != last {
						t.Fatalf("at %s: history=%v signalStep=%d", at, market.history[symbol], market.signalStep)
					}
					slower := *def
					slower.RebalanceEvery = 2
					if strategyReplaySignalDue(&slower, step, market) != (last && day%2 == 0) {
						t.Fatalf("rebalance_every counted hourly bars at %s", at)
					}
					if last && day == 1 {
						sma, err := strategyMetric(symbol, "sma_2", market)
						if err != nil {
							t.Fatal(err)
						}
						assertClose(t, "daily SMA on hourly replay", sma, 110)
					}
				}
			}
		})
	}
}

func TestStrategyReplaySignalCalendar(t *testing.T) {
	for _, tc := range []struct {
		name, symbol, cadence, source, at string
		complete                          bool
	}{
		{"regular close", "AAPL", "1d", "1h", "2026-07-09T19:30:00Z", true},
		{"forming day", "AAPL", "1d", "1h", "2026-07-09T18:30:00Z", false},
		{"early Friday close", "AAPL", "1w", "1h", "2026-11-27T17:30:00Z", true},
		{"holiday week Thursday close", "AAPL", "1w", "1d", "2026-07-02T13:30:00Z", true},
		{"forming week", "AAPL", "1w", "1d", "2026-07-01T13:30:00Z", false},
		{"crypto Sunday close", "BTC-USD", "1w", "1h", "2026-07-05T23:00:00Z", true},
		{"crypto forming week", "BTC-USD", "1w", "1h", "2026-07-04T23:00:00Z", false},
		{"before DST", "AAPL", "1d", "1h", "2026-03-06T20:30:00Z", true},
		{"after DST", "AAPL", "1d", "1h", "2026-03-09T19:30:00Z", true},
		{"four hour equity close", "AAPL", "4h", "1h", "2026-07-09T16:30:00Z", true},
		{"short final four hour candle", "AAPL", "4h", "1h", "2026-07-09T19:30:00Z", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			at, err := time.Parse(time.RFC3339, tc.at)
			if err != nil {
				t.Fatal(err)
			}
			_, complete := completedReplaySignalBar(&StrategyDefinition{Universe: []string{tc.symbol}, Cadence: tc.cadence}, tc.source, at)
			if complete != tc.complete {
				t.Fatalf("completed=%v, want %v", complete, tc.complete)
			}
		})
	}
}

func TestStrategyReplayIntervalValidation(t *testing.T) {
	def := &StrategyDefinition{Cadence: "1h"}
	if interval, err := normalizeStrategyReplayInterval(def, ""); err != nil || interval != "1h" {
		t.Fatalf("default interval=%q err=%v", interval, err)
	}
	for _, interval := range []string{"1d", "4h", "1w"} {
		if _, err := normalizeStrategyReplayInterval(def, interval); err == nil {
			t.Fatalf("accepted %s bars for an hourly strategy", interval)
		}
	}
}

func TestStrategyReplayWarmupAndReloadUseDailyHistory(t *testing.T) {
	ctx := newTestCtx(t)
	pid := mustCreatePortfolio(t, ctx, "Daily history", []string{"crypto"})
	def := &StrategyDefinition{Universe: []string{"BTC-USD", "ETH-USD"}, Cadence: "1d", Rules: []StrategyRule{{When: &StrategyCondition{Indicator: "sma_2"}}}}
	run := &BacktestRun{ProjectID: "test-proj", PortfolioID: pid, Symbols: def.Universe, Interval: "1h", StartingCash: 1000, TotalSteps: 24}
	id, err := dbCreateBacktestRun(ctx.AppDB(), run)
	if err != nil {
		t.Fatal(err)
	}
	run.ID = id
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	var bars []*BacktestMarketBar
	for step := 1; step <= 72; step++ {
		for _, symbol := range def.Universe {
			bars = append(bars, &BacktestMarketBar{Step: step, Symbol: symbol, AssetClass: "crypto", T: start.Add(time.Duration(step-1) * time.Hour).Unix(), O: float64(step), H: float64(step), L: float64(step), C: float64(step), Source: "fixture"})
		}
	}
	bars = reindexValidationMarketBars(bars, 49, 72, strategyReplayWarmupSteps(def, run.Interval))
	if err := dbReplaceBacktestMarketBars(ctx.AppDB(), id, bars); err != nil {
		t.Fatal(err)
	}
	market, err := backtestStrategyMarket(run, def, 0)
	if err != nil {
		t.Fatal(err)
	}
	if market.signalCount != 0 {
		t.Fatal("warmup bars must not consume rebalance slots")
	}
	for _, symbol := range def.Universe {
		sma, err := strategyMetric(symbol, "sma_2", market)
		if err != nil {
			t.Fatal(err)
		}
		assertClose(t, "warmup daily SMA", sma, 36)
	}
	for step := 1; step <= 24; step++ {
		if _, err := advanceBacktestStrategyMarket(run, def, step, &market); err != nil {
			t.Fatal(err)
		}
	}
	reloaded, err := backtestStrategyMarket(run, def, 24)
	if err != nil {
		t.Fatal(err)
	}
	for _, symbol := range def.Universe {
		for _, m := range []strategyMarket{market, reloaded} {
			sma, err := strategyMetric(symbol, "sma_2", m)
			if err != nil {
				t.Fatal(err)
			}
			assertClose(t, "resumed daily SMA", sma, 60)
			if m.signalCount != 1 || !strategyReplaySignalDue(def, 24, m) {
				t.Fatalf("signal state differs after reload: count=%d step=%d", m.signalCount, m.signalStep)
			}
		}
	}
}

func TestStrategyReplayHourlyStocksMatchStepAndResume(t *testing.T) {
	ctx := newTestCtx(t)
	pid := mustCreatePortfolio(t, ctx, "Daily on hourly", []string{"equity"})
	sid := mustCreateFixedStrategy(t, ctx, "Daily stock", "AAPL", 0.1, "1d")
	var bars []*BacktestMarketBar
	for _, date := range []string{"2026-11-25", "2026-11-27", "2026-11-30"} {
		noon, _ := time.Parse(time.RFC3339, date+"T12:00:00Z")
		session := usEquitySessionAt(noon)
		for at := session.Open; at.Before(session.Close); at = at.Add(time.Hour) {
			price := 100 + float64(len(bars))*2
			bars = append(bars, &BacktestMarketBar{Step: len(bars) + 1, Symbol: "AAPL", AssetClass: "equity", T: at.Unix(), O: price, H: price + 1, L: price, C: price + 1, Source: "fixture"})
		}
	}
	var reference []*BacktestSnapshot
	for _, mode := range []string{"batch", "manual", "resume"} {
		t.Run(mode, func(t *testing.T) {
			id, err := dbCreateBacktestRun(ctx.AppDB(), &BacktestRun{ProjectID: "test-proj", PortfolioID: pid, StrategyID: sid, StrategyVersion: 1, RunKind: "strategy", Name: mode, Symbols: []string{"AAPL"}, Interval: "1h", StartingCash: 100000, SlippageBps: 5, TotalSteps: len(bars)})
			if err != nil {
				t.Fatal(err)
			}
			if err := dbReplaceBacktestMarketBars(ctx.AppDB(), id, bars); err != nil {
				t.Fatal(err)
			}
			run, err := dbGetBacktestRun(ctx.AppDB(), "test-proj", id)
			if err != nil {
				t.Fatal(err)
			}
			if mode != "batch" {
				if err := initializeStrategyBacktestRun(run); err != nil {
					t.Fatal(err)
				}
				steps := len(bars)
				if mode == "resume" {
					steps = 7
				} // Pause with a completed daily signal pending.
				for i := 0; i < steps; i++ {
					if _, err := stepStrategyBacktestRun(run); err != nil {
						t.Fatal(err)
					}
				}
				if mode == "resume" {
					if err := dbSetBacktestStatus(ctx.AppDB(), id, "paused", ""); err != nil {
						t.Fatal(err)
					}
					run, err = dbGetBacktestRun(ctx.AppDB(), "test-proj", id)
					if err != nil {
						t.Fatal(err)
					}
				}
			}
			if mode != "manual" {
				if _, err := runStrategyBacktestToEnd(run); err != nil {
					t.Fatal(err)
				}
			}
			snaps, err := dbListBacktestSnapshots(ctx.AppDB(), id)
			if err != nil {
				t.Fatal(err)
			}
			if len(snaps[8].Orders) != 1 {
				t.Fatal("daily signal must execute at the next session open, step 8")
			}
			for _, snap := range snaps {
				if snap.Step != 8 && snap.Step != 12 && len(snap.Orders) != 0 {
					t.Fatalf("unexpected orders at step %d", snap.Step)
				}
			}
			assertClose(t, "next open fill", snaps[8].Orders[0].AvgFillPrice, bars[7].O*1.0005)
			if reference == nil {
				reference = snaps
				return
			}
			for i, snap := range snaps {
				assertClose(t, fmt.Sprintf("step %d equity", i), snap.Equity, reference[i].Equity)
				assertClose(t, fmt.Sprintf("step %d realized", i), snap.RealizedPnL, reference[i].RealizedPnL)
				if len(snap.Orders) != len(reference[i].Orders) {
					t.Fatalf("different order count at step %d", i)
				}
			}
		})
	}
}
