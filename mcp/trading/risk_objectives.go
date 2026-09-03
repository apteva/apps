package main

import (
	"database/sql"
	"errors"
	"fmt"
	"math"
	"strings"
	"time"
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

type PortfolioRiskState struct {
	PortfolioID        int64   `json:"portfolio_id"`
	HighWaterEquity    float64 `json:"high_water_equity"`
	CurrentDrawdownPct float64 `json:"current_drawdown_pct"`
	HighWaterAt        string  `json:"high_water_at,omitempty"`
	UpdatedAt          string  `json:"updated_at,omitempty"`
}

type PortfolioObjective struct {
	ID             int64    `json:"id"`
	ProjectID      string   `json:"project_id,omitempty"`
	PortfolioID    int64    `json:"portfolio_id"`
	Name           string   `json:"name"`
	Metric         string   `json:"metric"`
	TargetPct      float64  `json:"target_pct"`
	Direction      string   `json:"direction"`
	StartsAt       string   `json:"starts_at"`
	DeadlineAt     string   `json:"deadline_at,omitempty"`
	BaselineEquity *float64 `json:"baseline_equity,omitempty"`
	Status         string   `json:"status"`
	ActualPct      *float64 `json:"actual_pct,omitempty"`
	ProgressPct    *float64 `json:"progress_pct,omitempty"`
	Achieved       bool     `json:"achieved"`
	PeriodState    string   `json:"period_state"`
	CreatedAt      string   `json:"created_at,omitempty"`
	UpdatedAt      string   `json:"updated_at,omitempty"`
}

var riskPresets = map[string]PortfolioRiskPolicy{
	"conservative": {RiskLevel: "conservative", MaxDailyLossPct: 1.5, MaxDrawdownPct: 8, MaxPositionPct: 15, MaxGrossExposurePct: 60, MaxOrderPct: 10},
	"balanced":     {RiskLevel: "balanced", MaxDailyLossPct: 3, MaxDrawdownPct: 15, MaxPositionPct: 25, MaxGrossExposurePct: 100, MaxOrderPct: 20},
	"aggressive":   {RiskLevel: "aggressive", MaxDailyLossPct: 5, MaxDrawdownPct: 30, MaxPositionPct: 50, MaxGrossExposurePct: 100, MaxOrderPct: 40},
}

func riskProfiles() []PortfolioRiskPolicy {
	return []PortfolioRiskPolicy{riskPresets["conservative"], riskPresets["balanced"], riskPresets["aggressive"]}
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

func validateRiskPolicy(p *PortfolioRiskPolicy) error {
	if p == nil {
		return errors.New("risk policy required")
	}
	p.RiskLevel = strings.ToLower(strings.TrimSpace(p.RiskLevel))
	if _, preset := riskPresets[p.RiskLevel]; !preset && p.RiskLevel != "custom" {
		return errors.New("risk_level must be conservative, balanced, aggressive or custom")
	}
	for name, value := range map[string]float64{
		"max_daily_loss_pct": p.MaxDailyLossPct, "max_drawdown_pct": p.MaxDrawdownPct,
		"max_position_pct": p.MaxPositionPct, "max_gross_exposure_pct": p.MaxGrossExposurePct, "max_order_pct": p.MaxOrderPct,
	} {
		if value <= 0 || math.IsNaN(value) || math.IsInf(value, 0) {
			return fmt.Errorf("%s must be a finite number greater than zero", name)
		}
	}
	if p.MaxPositionPct > p.MaxGrossExposurePct {
		return errors.New("max_position_pct cannot exceed max_gross_exposure_pct")
	}
	return nil
}

func dbUpsertPortfolioRiskPolicy(db *sql.DB, portfolio *Portfolio, requested PortfolioRiskPolicy) (*PortfolioRiskPolicy, error) {
	if portfolio == nil {
		return nil, errors.New("portfolio required")
	}
	level := strings.ToLower(strings.TrimSpace(requested.RiskLevel))
	if preset, ok := riskPresets[level]; ok {
		requested = preset
	} else if level == "custom" {
		requested.RiskLevel = level
	} else {
		return nil, errors.New("risk_level must be conservative, balanced, aggressive or custom")
	}
	requested.PortfolioID, requested.ProjectID = portfolio.ID, portfolio.ProjectID
	if err := validateRiskPolicy(&requested); err != nil {
		return nil, err
	}
	_, err := db.Exec(`INSERT INTO portfolio_risk_policies
		(portfolio_id,project_id,risk_level,max_daily_loss_pct,max_drawdown_pct,max_position_pct,max_gross_exposure_pct,max_order_pct)
		VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(portfolio_id) DO UPDATE SET project_id=excluded.project_id,risk_level=excluded.risk_level,
		max_daily_loss_pct=excluded.max_daily_loss_pct,max_drawdown_pct=excluded.max_drawdown_pct,max_position_pct=excluded.max_position_pct,
		max_gross_exposure_pct=excluded.max_gross_exposure_pct,max_order_pct=excluded.max_order_pct,updated_at=CURRENT_TIMESTAMP`,
		requested.PortfolioID, requested.ProjectID, requested.RiskLevel, requested.MaxDailyLossPct, requested.MaxDrawdownPct,
		requested.MaxPositionPct, requested.MaxGrossExposurePct, requested.MaxOrderPct)
	if err != nil {
		return nil, err
	}
	return dbGetPortfolioRiskPolicy(db, portfolio)
}

func dbUpdatePortfolioRiskState(db *sql.DB, portfolio *Portfolio, equity float64) (*PortfolioRiskState, error) {
	if portfolio == nil || equity < 0 {
		return nil, errors.New("valid portfolio and equity required")
	}
	_, err := db.Exec(`INSERT INTO portfolio_risk_state(portfolio_id,project_id,high_water_equity,current_drawdown_pct)
		VALUES(?,?,?,0) ON CONFLICT(portfolio_id) DO UPDATE SET
		high_water_equity=CASE WHEN excluded.high_water_equity>portfolio_risk_state.high_water_equity THEN excluded.high_water_equity ELSE portfolio_risk_state.high_water_equity END,
		high_water_at=CASE WHEN excluded.high_water_equity>portfolio_risk_state.high_water_equity THEN CURRENT_TIMESTAMP ELSE portfolio_risk_state.high_water_at END,
		current_drawdown_pct=CASE WHEN MAX(portfolio_risk_state.high_water_equity,excluded.high_water_equity)>0
		THEN (excluded.high_water_equity/MAX(portfolio_risk_state.high_water_equity,excluded.high_water_equity)-1)*100 ELSE 0 END,
		updated_at=CURRENT_TIMESTAMP`, portfolio.ID, portfolio.ProjectID, equity)
	if err != nil {
		return nil, err
	}
	return dbGetPortfolioRiskState(db, portfolio.ID)
}

func dbGetPortfolioRiskState(db *sql.DB, portfolioID int64) (*PortfolioRiskState, error) {
	s := &PortfolioRiskState{PortfolioID: portfolioID}
	err := db.QueryRow(`SELECT high_water_equity,current_drawdown_pct,high_water_at,updated_at FROM portfolio_risk_state WHERE portfolio_id=?`, portfolioID).
		Scan(&s.HighWaterEquity, &s.CurrentDrawdownPct, &s.HighWaterAt, &s.UpdatedAt)
	return s, err
}

type RiskBreach struct {
	Code      string  `json:"code"`
	Detail    string  `json:"detail"`
	ActualPct float64 `json:"actual_pct"`
	LimitPct  float64 `json:"limit_pct"`
}

func preTradeRiskCheck(db *sql.DB, portfolio *Portfolio, symbol, side string, qty, price float64) (*RiskBreach, error) {
	if portfolio == nil || !isBuySide(side) || qty <= 0 || price <= 0 {
		return nil, nil
	}
	policy, err := dbGetPortfolioRiskPolicy(db, portfolio)
	if err != nil {
		return nil, err
	}
	equity, err := computeEquity(db, portfolio)
	if err != nil || equity <= 0 {
		return nil, err
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
	working, err := dbWorkingOrders(db)
	if err != nil {
		return nil, err
	}
	for _, order := range working {
		if order.PortfolioID != portfolio.ID || !isBuySide(order.Side) {
			continue
		}
		workingPrice := orderRiskPrice(db, order)
		value := math.Max(0, order.Qty-order.FilledQty) * workingPrice
		gross += value
		if canonicalSymbol(order.Symbol) == canonicalSymbol(symbol) {
			symbolValue += value
		}
	}
	positionPct := (symbolValue + orderNotional) / equity * 100
	if positionPct > policy.MaxPositionPct+1e-9 {
		return &RiskBreach{Code: "risk_max_position", Detail: fmt.Sprintf("projected %s weight %.2f%% exceeds %.2f%% maximum", canonicalSymbol(symbol), positionPct, policy.MaxPositionPct), ActualPct: positionPct, LimitPct: policy.MaxPositionPct}, nil
	}
	grossPct := (gross + orderNotional) / equity * 100
	if grossPct > policy.MaxGrossExposurePct+1e-9 {
		return &RiskBreach{Code: "risk_max_gross_exposure", Detail: fmt.Sprintf("projected gross exposure %.2f%% exceeds %.2f%% maximum", grossPct, policy.MaxGrossExposurePct), ActualPct: grossPct, LimitPct: policy.MaxGrossExposurePct}, nil
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

func validateObjective(o *PortfolioObjective) error {
	if o == nil || strings.TrimSpace(o.Name) == "" {
		return errors.New("objective name required")
	}
	o.Name = strings.TrimSpace(o.Name)
	o.Metric = strings.ToLower(strings.TrimSpace(o.Metric))
	o.Direction = strings.ToLower(strings.TrimSpace(o.Direction))
	o.Status = strings.ToLower(strings.TrimSpace(o.Status))
	if o.Status == "" {
		o.Status = "active"
	}
	if o.Direction == "" {
		if o.Metric == "drawdown_pct" {
			o.Direction = "at_most"
		} else {
			o.Direction = "at_least"
		}
	}
	if !oneOfString(o.Metric, "period_return_pct", "total_return_pct", "day_return_pct", "drawdown_pct") {
		return errors.New("metric must be period_return_pct, total_return_pct, day_return_pct or drawdown_pct")
	}
	if !oneOfString(o.Direction, "at_least", "at_most") {
		return errors.New("direction must be at_least or at_most")
	}
	if !oneOfString(o.Status, "draft", "active", "paused", "achieved", "expired", "archived") {
		return errors.New("invalid objective status")
	}
	if math.IsNaN(o.TargetPct) || math.IsInf(o.TargetPct, 0) {
		return errors.New("target_pct must be finite")
	}
	if o.Metric == "drawdown_pct" && (o.TargetPct < 0 || o.Direction != "at_most") {
		return errors.New("drawdown_pct objectives require a non-negative target_pct and direction=at_most")
	}
	start, err := time.Parse(time.RFC3339, o.StartsAt)
	if err != nil {
		return errors.New("starts_at must be RFC3339")
	}
	if o.DeadlineAt != "" {
		deadline, err := time.Parse(time.RFC3339, o.DeadlineAt)
		if err != nil || !deadline.After(start) {
			return errors.New("deadline_at must be RFC3339 and after starts_at")
		}
	}
	return nil
}

func oneOfString(value string, allowed ...string) bool {
	for _, candidate := range allowed {
		if value == candidate {
			return true
		}
	}
	return false
}

func dbCreatePortfolioObjective(db *sql.DB, portfolio *Portfolio, objective PortfolioObjective, equity float64) (*PortfolioObjective, error) {
	objective.ProjectID, objective.PortfolioID = portfolio.ProjectID, portfolio.ID
	if objective.StartsAt == "" {
		objective.StartsAt = time.Now().UTC().Format(time.RFC3339)
	}
	if err := validateObjective(&objective); err != nil {
		return nil, err
	}
	start, _ := time.Parse(time.RFC3339, objective.StartsAt)
	if objective.Metric == "period_return_pct" && !start.After(time.Now().UTC()) {
		objective.BaselineEquity = &equity
	}
	res, err := db.Exec(`INSERT INTO portfolio_objectives(project_id,portfolio_id,name,metric,target_pct,direction,starts_at,deadline_at,baseline_equity,status)
		VALUES(?,?,?,?,?,?,?,?,?,?)`, objective.ProjectID, objective.PortfolioID, objective.Name, objective.Metric, objective.TargetPct,
		objective.Direction, objective.StartsAt, nullableText(objective.DeadlineAt), objective.BaselineEquity, objective.Status)
	if err != nil {
		return nil, err
	}
	objective.ID, _ = res.LastInsertId()
	return dbGetPortfolioObjective(db, portfolio, objective.ID)
}

func dbGetPortfolioObjective(db *sql.DB, portfolio *Portfolio, id int64) (*PortfolioObjective, error) {
	o := &PortfolioObjective{}
	err := db.QueryRow(`SELECT id,project_id,portfolio_id,name,metric,target_pct,direction,starts_at,COALESCE(deadline_at,''),baseline_equity,status,created_at,updated_at
		FROM portfolio_objectives WHERE id=? AND portfolio_id=? AND project_id=?`, id, portfolio.ID, portfolio.ProjectID).
		Scan(&o.ID, &o.ProjectID, &o.PortfolioID, &o.Name, &o.Metric, &o.TargetPct, &o.Direction, &o.StartsAt, &o.DeadlineAt, &o.BaselineEquity, &o.Status, &o.CreatedAt, &o.UpdatedAt)
	return o, err
}

func dbListPortfolioObjectives(db *sql.DB, portfolio *Portfolio, includeArchived bool) ([]*PortfolioObjective, error) {
	query := `SELECT id,project_id,portfolio_id,name,metric,target_pct,direction,starts_at,COALESCE(deadline_at,''),baseline_equity,status,created_at,updated_at
		FROM portfolio_objectives WHERE portfolio_id=? AND project_id=?`
	if !includeArchived {
		query += ` AND status!='archived'`
	}
	query += ` ORDER BY status='active' DESC,created_at DESC,id DESC`
	rows, err := db.Query(query, portfolio.ID, portfolio.ProjectID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*PortfolioObjective{}
	for rows.Next() {
		o := &PortfolioObjective{}
		if err := rows.Scan(&o.ID, &o.ProjectID, &o.PortfolioID, &o.Name, &o.Metric, &o.TargetPct, &o.Direction, &o.StartsAt, &o.DeadlineAt, &o.BaselineEquity, &o.Status, &o.CreatedAt, &o.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return out, rows.Err()
}

func dbUpdatePortfolioObjective(db *sql.DB, portfolio *Portfolio, objective PortfolioObjective) (*PortfolioObjective, error) {
	if objective.ID <= 0 {
		return nil, errors.New("objective id required")
	}
	if err := validateObjective(&objective); err != nil {
		return nil, err
	}
	res, err := db.Exec(`UPDATE portfolio_objectives SET name=?,metric=?,target_pct=?,direction=?,starts_at=?,deadline_at=?,baseline_equity=?,status=?,updated_at=CURRENT_TIMESTAMP
		WHERE id=? AND portfolio_id=? AND project_id=?`, objective.Name, objective.Metric, objective.TargetPct, objective.Direction,
		objective.StartsAt, nullableText(objective.DeadlineAt), objective.BaselineEquity, objective.Status, objective.ID, portfolio.ID, portfolio.ProjectID)
	if err != nil {
		return nil, err
	}
	if n, _ := res.RowsAffected(); n == 0 {
		return nil, sql.ErrNoRows
	}
	return dbGetPortfolioObjective(db, portfolio, objective.ID)
}

func objectivesWithProgress(db *sql.DB, portfolio *Portfolio, includeArchived bool) ([]*PortfolioObjective, error) {
	equity, err := computeEquity(db, portfolio)
	if err != nil {
		return nil, err
	}
	_ = initializeDueObjectiveBaselines(db, portfolio, equity, time.Now().UTC())
	_, _ = dbUpdatePortfolioRiskState(db, portfolio, equity)
	snap, err := snapshotPortfolio(db, portfolio)
	if err != nil {
		return nil, err
	}
	objectives, err := dbListPortfolioObjectives(db, portfolio, includeArchived)
	if err != nil {
		return nil, err
	}
	for _, objective := range objectives {
		objectiveProgress(db, portfolio, objective, snap)
	}
	return objectives, nil
}

func initializeDueObjectiveBaselines(db *sql.DB, portfolio *Portfolio, equity float64, now time.Time) error {
	_, err := db.Exec(`UPDATE portfolio_objectives SET baseline_equity=?,updated_at=CURRENT_TIMESTAMP
		WHERE portfolio_id=? AND project_id=? AND metric='period_return_pct' AND baseline_equity IS NULL
		AND status='active' AND starts_at<=?`, equity, portfolio.ID, portfolio.ProjectID, now.UTC().Format(time.RFC3339))
	return err
}

func objectiveProgress(db *sql.DB, portfolio *Portfolio, objective *PortfolioObjective, snap *Portfolio) {
	if objective == nil || snap == nil {
		return
	}
	now := time.Now().UTC()
	start, _ := time.Parse(time.RFC3339, objective.StartsAt)
	if now.Before(start) {
		objective.PeriodState = "pending"
		return
	}
	objective.PeriodState = "active"
	if objective.DeadlineAt != "" {
		deadline, _ := time.Parse(time.RFC3339, objective.DeadlineAt)
		if !now.Before(deadline) {
			objective.PeriodState = "ended"
		}
	}
	actual := 0.0
	switch objective.Metric {
	case "period_return_pct":
		if objective.BaselineEquity == nil || *objective.BaselineEquity <= 0 {
			return
		}
		actual = (snap.Equity/(*objective.BaselineEquity) - 1) * 100
	case "total_return_pct":
		actual = snap.TotalPnLPct
	case "day_return_pct":
		actual = snap.DayPnLPct
	case "drawdown_pct":
		state, err := dbGetPortfolioRiskState(db, portfolio.ID)
		if err != nil {
			return
		}
		actual = math.Abs(state.CurrentDrawdownPct)
	}
	objective.ActualPct = &actual
	objective.Achieved = (objective.Direction == "at_least" && actual >= objective.TargetPct) || (objective.Direction == "at_most" && actual <= objective.TargetPct)
	if objective.TargetPct != 0 {
		progress := actual / objective.TargetPct * 100
		objective.ProgressPct = &progress
	}
}
