package main

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
)

type PortfolioRiskState struct {
	PortfolioID        int64   `json:"portfolio_id"`
	HighWaterEquity    float64 `json:"high_water_equity"`
	CurrentDrawdownPct float64 `json:"current_drawdown_pct"`
	HighWaterAt        string  `json:"high_water_at,omitempty"`
	UpdatedAt          string  `json:"updated_at,omitempty"`
}

func dbGetPortfolioRiskState(db *sql.DB, portfolioID int64) (*PortfolioRiskState, error) {
	s := &PortfolioRiskState{PortfolioID: portfolioID}
	err := db.QueryRow(`SELECT high_water_equity,current_drawdown_pct,high_water_at,updated_at FROM portfolio_risk_state WHERE portfolio_id=?`, portfolioID).
		Scan(&s.HighWaterEquity, &s.CurrentDrawdownPct, &s.HighWaterAt, &s.UpdatedAt)
	return s, err
}

func preTradeRiskCheckExcluding(db *sql.DB, portfolio *Portfolio, symbol, side string, qty, price float64, excludeOrder string) (*RiskBreach, error) {
	if portfolio == nil || !isBuySide(side) || qty <= 0 || price <= 0 {
		return nil, nil
	}
	policy, err := dbGetPortfolioRiskPolicy(db, portfolio)
	if err != nil {
		return nil, err
	}
	equity, err := computeEquity(db, portfolio)
	if err != nil {
		return nil, err
	}
	if breach, err := currentLossBreach(db, portfolio, policy, equity); err != nil || breach != nil {
		return breach, err
	}
	orderNotional := qty * price
	orderPct := orderNotional / equity * 100
	if orderPct > policy.MaxOrderPct+1e-9 {
		return &RiskBreach{Code: "risk_max_order", Detail: fmt.Sprintf("order %.2f%% exceeds %.2f%% maximum", orderPct, policy.MaxOrderPct), ActualPct: orderPct, LimitPct: policy.MaxOrderPct}, nil
	}
	positions, err := dbListPositions(db, portfolio.ID)
	if err != nil {
		return nil, err
	}
	gross, symbolValue := 0.0, 0.0
	for _, position := range positions {
		mark, _ := dbGetMark(db, position.Symbol)
		if mark == nil || mark.Price <= 0 {
			return &RiskBreach{Code: "risk_unpriced_position", Detail: "cannot authorize more risk while a holding has no price"}, nil
		}
		marketPrice := position.AvgCost
		if mark != nil && mark.Price > 0 {
			marketPrice = markPriceForSide(mark, position.Outcome)
		}
		value := math.Abs(position.Qty * marketPrice)
		gross += value
		if canonicalSymbol(position.Symbol) == canonicalSymbol(symbol) {
			symbolValue += value
		}
	}
	working, err := workingPortfolioOrders(db, portfolio.ID)
	if err != nil {
		return nil, err
	}
	for _, order := range working {
		if order.ID == excludeOrder || order.PortfolioID != portfolio.ID || !isBuySide(order.Side) {
			continue
		}
		workingPrice := orderRiskPrice(db, order)
		value := math.Max(0, order.Qty-order.FilledQty) * workingPrice
		gross += value
		if canonicalSymbol(order.Symbol) == canonicalSymbol(symbol) {
			symbolValue += value
		}
	}
	return exposureBreach(policy, equity, orderNotional, symbolValue, gross), nil
}

func currentLossBreach(db *sql.DB, pf *Portfolio, policy *PortfolioRiskPolicy, equity float64) (*RiskBreach, error) {
	if equity <= 0 {
		return &RiskBreach{Code: "risk_no_equity", Detail: "positive portfolio equity required"}, nil
	}
	baseline, ok, err := dbGetDayBaseline(db, pf.ID, utcDay(executionTime(db, pf.ID)))
	if err != nil {
		return nil, err
	}
	if ok && baseline > 0 && (equity/baseline-1)*100 < -policy.MaxDailyLossPct {
		return &RiskBreach{Code: "daily_loss_halt", Detail: "daily loss limit reached"}, nil
	}
	state, err := dbGetPortfolioRiskState(db, pf.ID)
	if err != nil && !errors.Is(err, sql.ErrNoRows) {
		return nil, err
	}
	peak := pf.StartingCash
	if state != nil {
		peak = math.Max(peak, state.HighWaterEquity)
	}
	if peak > 0 && (equity/peak-1)*100 < -policy.MaxDrawdownPct {
		return &RiskBreach{Code: "max_drawdown_halt", Detail: "drawdown limit reached"}, nil
	}
	return nil, nil
}

func orderRiskPrice(db *sql.DB, order *Order) float64 {
	if order == nil {
		return 0
	}
	if order.LimitPrice != nil && *order.LimitPrice > 0 {
		return *order.LimitPrice
	}
	if order.StopPrice != nil && *order.StopPrice > 0 {
		return *order.StopPrice
	}
	if mark, _ := dbGetMark(db, order.Symbol); mark != nil {
		return markPriceForSide(mark, order.Outcome)
	}
	return 0
}
