package main

import (
	"context"
	"errors"
	"math"
	"strconv"
	"strings"
)

type BacktestTrade struct {
	SignalTime int64   `json:"signal_time"`
	Strategy   string  `json:"strategy"`
	Direction  string  `json:"direction"`
	Entry      float64 `json:"entry"`
	Exit       float64 `json:"exit"`
	ReturnBps  float64 `json:"return_bps"`
	Outcome    string  `json:"outcome"`
}
type BacktestResult struct {
	Symbol         string          `json:"symbol"`
	Interval       string          `json:"interval"`
	Provider       string          `json:"provider"`
	Bars           int             `json:"bars"`
	Signals        int             `json:"signals"`
	Wins           int             `json:"wins"`
	Losses         int             `json:"losses"`
	Flat           int             `json:"flat"`
	HitRate        float64         `json:"hit_rate"`
	AvgReturnBps   float64         `json:"avg_return_bps"`
	TotalReturnBps float64         `json:"total_return_bps"`
	ProfitFactor   float64         `json:"profit_factor"`
	MaxDrawdownBps float64         `json:"max_drawdown_bps"`
	Trades         []BacktestTrade `json:"trades"`
	Methodology    []string        `json:"methodology"`
}

func backtestBars(symbol, interval, provider, strategy string, bars []Candle, costBps float64) (BacktestResult, error) {
	closed := make([]Candle, 0, len(bars))
	for _, b := range bars {
		if b.Closed && b.Close > 0 {
			closed = append(closed, b)
		}
	}
	if len(closed) < 70 {
		return BacktestResult{}, errors.New("at least 70 closed candles required")
	}
	r := BacktestResult{Symbol: symbol, Interval: interval, Provider: provider, Bars: len(closed), Trades: []BacktestTrade{}, Methodology: []string{"walk-forward: features use only candles available at signal time", "six-candle horizon; stop wins ties when stop and target occur in one candle", "reported returns include configured round-trip fees and slippage"}}
	seen := map[string]bool{}
	equity, peak := 0.0, 0.0
	grossWin, grossLoss := 0.0, 0.0
	for i := 60; i < len(closed)-6; i++ {
		window := closed[:i+1]
		f, e := extractSignalFeatures(window)
		if e != nil {
			continue
		}
		for _, c := range generateCandidates(f) {
			if strategy != "" && strategy != "all" && c.Strategy != strategy {
				continue
			}
			key := c.Strategy + ":" + c.Direction + ":" + strconv.FormatInt(closed[i].Time, 10)
			if seen[key] {
				continue
			}
			seen[key] = true
			entry := closed[i].Close
			risk := math.Max(f.ATR14*1.5, entry*.004)
			stop, target := entry-risk, entry+risk*1.5
			if c.Direction == "SELL" {
				stop, target = entry+risk, entry-risk*1.5
			}
			exit := closed[i+6].Close
			outcome := "flat"
			for _, future := range closed[i+1 : i+7] {
				if c.Direction == "BUY" {
					if future.Low <= stop {
						exit = stop
						break
					}
					if future.High >= target {
						exit = target
						break
					}
				} else {
					if future.High >= stop {
						exit = stop
						break
					}
					if future.Low <= target {
						exit = target
						break
					}
				}
			}
			ret := (exit/entry - 1) * 10000
			if c.Direction == "SELL" {
				ret = -ret
			}
			ret -= costBps
			if ret > 10 {
				outcome = "win"
				r.Wins++
				grossWin += ret
			} else if ret < -10 {
				outcome = "loss"
				r.Losses++
				grossLoss -= ret
			} else {
				r.Flat++
			}
			r.Signals++
			r.TotalReturnBps += ret
			equity += ret
			if equity > peak {
				peak = equity
			}
			if dd := peak - equity; dd > r.MaxDrawdownBps {
				r.MaxDrawdownBps = dd
			}
			if len(r.Trades) < 100 {
				r.Trades = append(r.Trades, BacktestTrade{SignalTime: closed[i].Time, Strategy: c.Strategy, Direction: c.Direction, Entry: entry, Exit: exit, ReturnBps: ret, Outcome: outcome})
			}
		}
	}
	if r.Signals > 0 {
		r.AvgReturnBps = r.TotalReturnBps / float64(r.Signals)
	}
	if r.Wins+r.Losses > 0 {
		r.HitRate = float64(r.Wins) / float64(r.Wins+r.Losses)
	}
	if grossLoss > 0 {
		r.ProfitFactor = grossWin / grossLoss
	}
	return r, nil
}

func (a *App) runBacktest(ctx context.Context, provider MarketDataProvider, symbol, interval, strategy string, limit int, costBps float64) (BacktestResult, error) {
	bars, err := provider.Bars(ctx, strings.ToUpper(symbol), interval, limit)
	if err != nil {
		return BacktestResult{}, err
	}
	return backtestBars(strings.ToUpper(symbol), interval, provider.Name(), strategy, bars, costBps)
}
