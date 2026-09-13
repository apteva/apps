package main

import (
	sdk "github.com/apteva/app-sdk"
	"strings"
)

// Classify the environment actually used by the installed transport. A UI label
// or an unused credential flag must never turn a live endpoint into paper.
func brokerConnectionEnvironment(ctx *sdk.AppCtx, slug string, id int64) (string, bool) {
	switch slug {
	case "alpaca-trading":
		return alpacaConnectionEnvironment(ctx, id)
	case "okx":
		if ctx == nil || ctx.PlatformAPI() == nil {
			return "", false
		}
		// The OKX signer reads both fields from credentials on every request.
		// Public metadata can omit either one, so inspect the same source.
		creds, err := ctx.PlatformAPI().GetConnectionCredentials(id)
		if err != nil || creds == nil {
			return "", false
		}
		return okxExecutionEnvironment(creds.Fields)
	case "binance-trading", "kraken", "coinbase", "bybit", "bitstamp", "polymarket-clob":
		// These catalog entries have fixed production URLs. In particular the
		// Binance testnet credential is not wired into its transport URL.
		return "broker_live", true
	default:
		return "", false
	}
}

func okxExecutionEnvironment(fields map[string]string) (string, bool) {
	paper := false
	for _, key := range []string{"simulated", "demo"} {
		switch strings.ToLower(strings.TrimSpace(fields[key])) {
		case "true", "1", "yes", "y", "on":
			paper = true
		case "", "false", "0", "no", "off":
		default:
			return "", false
		}
	}
	if paper {
		return "broker_paper", true
	}
	return "broker_live", true
}
