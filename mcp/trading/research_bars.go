package main

import (
	"errors"
	"time"
)

func strategyCadenceDuration(cadence string) (time.Duration, bool) {
	switch cadence {
	case "1m":
		return time.Minute, true
	case "5m":
		return 5 * time.Minute, true
	case "15m":
		return 15 * time.Minute, true
	case "1h":
		return time.Hour, true
	case "4h":
		return 4 * time.Hour, true
	case "1d":
		return 24 * time.Hour, true
	case "1w":
		return 7 * 24 * time.Hour, true
	default:
		return 0, false
	}
}

type strategyBarsProvider interface {
	StrategyBars(symbol, interval string, limit int) ([]Bar, error)
}

func loadLiveStrategyBars(provider Provider, symbol, interval string, limit int) ([]Bar, error) {
	if provider == nil {
		return nil, errors.New("provider not ready")
	}
	if limit <= 0 {
		limit = 1
	}
	if limit > 1000 {
		limit = 1000
	}
	if sp, ok := provider.(strategyBarsProvider); ok {
		return sp.StrategyBars(symbol, interval, limit)
	}
	return provider.Bars(symbol, "3M")
}

// ─── Strategy MCP tools ────────────────────────────────────────────

// ─── Strategy REST handlers ────────────────────────────────────────

// ─── Strategy backtest simulator ───────────────────────────────────
