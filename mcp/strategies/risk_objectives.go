package main

import (
	"database/sql"
	"errors"
)

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

func legacyRiskPolicy(portfolio *Portfolio) *PortfolioRiskPolicy {
	daily := configuredDailyLossHaltPct()
	return &PortfolioRiskPolicy{RiskLevel: "custom", MaxDailyLossPct: daily, MaxDrawdownPct: 100, MaxPositionPct: 100, MaxGrossExposurePct: 100, MaxOrderPct: 100}
}

func dbGetPortfolioRiskPolicy(db *sql.DB, portfolio *Portfolio) (*PortfolioRiskPolicy, error) {
	if portfolio == nil {
		return nil, errors.New("portfolio required")
	}
	p := legacyRiskPolicy(portfolio)
	p.PortfolioID, p.ProjectID = portfolio.ID, portfolio.ProjectID
	err := db.QueryRow(`SELECT risk_level,max_daily_loss_pct,max_drawdown_pct,max_position_pct,max_gross_exposure_pct,max_order_pct,updated_at
		FROM portfolio_risk_policies WHERE portfolio_id=? AND project_id=?`, portfolio.ID, portfolio.ProjectID).
		Scan(&p.RiskLevel, &p.MaxDailyLossPct, &p.MaxDrawdownPct, &p.MaxPositionPct, &p.MaxGrossExposurePct, &p.MaxOrderPct, &p.UpdatedAt)
	if err == sql.ErrNoRows {
		return p, nil
	}
	return p, err
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
