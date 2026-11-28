package main

import ()

type PortfolioRiskPolicy struct {
	PortfolioID         int64   `json:"portfolio_id"`
	ProjectID           string  `json:"project_id,omitempty"`
	RiskLevel           string  `json:"risk_level"`
	MaxDailyLossPct     float64 `json:"max_daily_loss_pct"`
	MaxDrawdownPct      float64 `json:"max_drawdown_pct"`
	MaxPositionPct      float64 `json:"max_position_pct"`
	MaxGrossExposurePct float64 `json:"max_gross_exposure_pct"`
	MaxOrderPct         float64 `json:"max_order_pct"`
	UpdatedAt           string  `json:"updated_at,omitempty"`
}

type RiskBreach struct {
	Code      string  `json:"code"`
	Detail    string  `json:"detail"`
	ActualPct float64 `json:"actual_pct"`
	LimitPct  float64 `json:"limit_pct"`
}

func oneOfString(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}
