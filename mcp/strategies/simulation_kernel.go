package main

// Pure simulation kernel: the policy, cost and symbol rules that decide a
// backtest result. This file is part of simulationSourceHash; the live
// data-access files that call Trading are not.

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
)

// applySlippage — sells fill below mark, buys above. The trader always
// pays the spread.
func applySlippage(mark float64, side string, bps float64) float64 {
	bp := bps / 10_000.0
	switch side {
	case "buy", "yes", "no":
		return mark + mark*bp
	case "sell":
		return mark - mark*bp
	}
	return mark
}

func fillFee(qty, price, bps float64) float64 {
	if qty <= 0 || price <= 0 || bps == 0 || !finite(bps) {
		return 0
	}
	return qty * price * bps / 10_000.0
}

func isBuySide(side string) bool {
	return side == "buy" || side == "yes" || side == "no"
}

type replayExecutionPolicy struct {
	Risk        *PortfolioRiskPolicy             `json:"risk"`
	Allowed     map[string]bool                  `json:"allowed"`
	Profiles    map[string]VenueExecutionProfile `json:"profiles"`
	Instruments map[string]*Instrument           `json:"instruments"`
}

func decodeReplayPolicy(run *BacktestRun) *replayExecutionPolicy {
	raw, err := json.Marshal(run.Summary["execution_policy"])
	if err != nil {
		return nil
	}
	var p replayExecutionPolicy
	if json.Unmarshal(raw, &p) != nil || p.Risk == nil {
		return nil
	}
	return &p
}

func exposureBreach(policy *PortfolioRiskPolicy, equity, orderNotional, symbolValue, gross float64) *RiskBreach {
	if equity <= 0 {
		return &RiskBreach{Code: "risk_no_equity", Detail: "positive equity required"}
	}
	checks := []struct {
		actual, limit float64
		code          string
	}{{orderNotional / equity * 100, policy.MaxOrderPct, "risk_max_order"}, {(symbolValue + orderNotional) / equity * 100, policy.MaxPositionPct, "risk_max_position"}, {(gross + orderNotional) / equity * 100, policy.MaxGrossExposurePct, "risk_max_gross_exposure"}}
	for _, c := range checks {
		if c.actual > c.limit+1e-9 {
			return &RiskBreach{Code: c.code, Detail: fmt.Sprintf("projected %.2f%% exceeds %.2f%% maximum", c.actual, c.limit), ActualPct: c.actual, LimitPct: c.limit}
		}
	}
	return nil
}

func replayPolicyHash(policy *replayExecutionPolicy) string {
	raw, _ := json.Marshal(policy)
	var value any
	_ = json.Unmarshal(raw, &value)
	var strip func(any)
	strip = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			for _, k := range []string{"updated_at", "created_at", "received_at", "runtime"} {
				delete(x, k)
			}
			for _, child := range x {
				strip(child)
			}
		case []any:
			for _, child := range x {
				strip(child)
			}
		}
	}
	strip(value)
	raw, _ = json.Marshal(value)
	return fmt.Sprintf("%x", sha256.Sum256(raw))
}

func round4(v float64) float64 {
	return float64(int(v*10000+0.5)) / 10000
}

// inferAssetClass — used by tools to validate a symbol's class without
// consulting the provider. Symbol prefixes carry the type; bare symbols
// default to equity.
func inferAssetClass(symbol string) string {
	s := strings.ToUpper(symbol)
	if strings.HasPrefix(s, "POLY:") {
		return "polymarket"
	}
	if strings.HasSuffix(s, "-USD") {
		return "crypto"
	}
	return "equity"
}

func canonicalSymbol(symbol string) string {
	symbol = strings.TrimSpace(symbol)
	upper := strings.ToUpper(symbol)
	if strings.HasPrefix(upper, "POLY:") {
		payload := strings.TrimSpace(symbol[len("POLY:"):])
		if strings.HasPrefix(strings.ToLower(payload), "tokenid:") {
			return "POLY:tokenid:" + strings.TrimSpace(payload[len("tokenid:"):])
		}
		return "POLY:" + strings.ToLower(payload)
	}
	return upper
}
