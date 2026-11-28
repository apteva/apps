package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// PortfolioUniversePolicy is the portfolio-level mandate boundary. Market and
// reference universes describe what data exists; this policy describes what
// the portfolio is actually permitted to trade.
type PortfolioUniversePolicy struct {
	PortfolioID          int64    `json:"portfolio_id"`
	ProjectID            string   `json:"project_id,omitempty"`
	SelectionMode        string   `json:"selection_mode"`
	IncludeSymbols       []string `json:"include_symbols"`
	ExcludeSymbols       []string `json:"exclude_symbols"`
	ReferenceUniverseID  string   `json:"reference_universe_id,omitempty"`
	RequireActiveListing bool     `json:"require_active_listing"`
	EnforcementEnabled   bool     `json:"enforcement_enabled"`
	UpdatedAt            string   `json:"updated_at,omitempty"`
}

type UniverseViolation struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

func defaultUniversePolicy(portfolio *Portfolio) *PortfolioUniversePolicy {
	p := &PortfolioUniversePolicy{SelectionMode: "all_allowed_classes", IncludeSymbols: []string{}, ExcludeSymbols: []string{}, EnforcementEnabled: true}
	if portfolio != nil {
		p.PortfolioID, p.ProjectID = portfolio.ID, portfolio.ProjectID
	}
	return p
}

func validateUniversePolicy(p *PortfolioUniversePolicy) error {
	if p == nil || p.PortfolioID <= 0 || strings.TrimSpace(p.ProjectID) == "" {
		return errors.New("portfolio universe policy requires portfolio and project")
	}
	p.SelectionMode = strings.ToLower(strings.TrimSpace(p.SelectionMode))
	if !oneOfString(p.SelectionMode, "all_allowed_classes", "symbol_allowlist", "reference_universe") {
		return errors.New("selection_mode must be all_allowed_classes, symbol_allowlist or reference_universe")
	}
	p.IncludeSymbols = cleanSymbols(p.IncludeSymbols)
	p.ExcludeSymbols = cleanSymbols(p.ExcludeSymbols)
	p.ReferenceUniverseID = strings.TrimSpace(p.ReferenceUniverseID)
	if p.SelectionMode == "symbol_allowlist" && len(p.IncludeSymbols) == 0 {
		return errors.New("symbol_allowlist requires at least one include_symbols entry")
	}
	if p.SelectionMode == "reference_universe" && p.ReferenceUniverseID == "" {
		return errors.New("reference_universe requires reference_universe_id")
	}
	excluded := map[string]bool{}
	for _, symbol := range p.ExcludeSymbols {
		excluded[symbol] = true
	}
	for _, symbol := range p.IncludeSymbols {
		if excluded[symbol] {
			return fmt.Errorf("symbol %s cannot be both included and excluded", symbol)
		}
	}
	return nil
}

func dbGetPortfolioUniversePolicy(db *sql.DB, portfolio *Portfolio) (*PortfolioUniversePolicy, error) {
	p := defaultUniversePolicy(portfolio)
	var includes, excludes string
	var requireActive, enabled int
	err := db.QueryRow(`SELECT selection_mode,include_symbols,exclude_symbols,reference_universe_id,require_active_listing,enforcement_enabled,updated_at
		FROM portfolio_universe_policies WHERE portfolio_id=? AND project_id=?`, portfolio.ID, portfolio.ProjectID).
		Scan(&p.SelectionMode, &includes, &excludes, &p.ReferenceUniverseID, &requireActive, &enabled, &p.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return p, nil
	}
	if err != nil {
		return nil, err
	}
	_ = json.Unmarshal([]byte(includes), &p.IncludeSymbols)
	_ = json.Unmarshal([]byte(excludes), &p.ExcludeSymbols)
	p.RequireActiveListing, p.EnforcementEnabled = requireActive != 0, enabled != 0
	return p, nil
}

func dbUpsertPortfolioUniversePolicy(db *sql.DB, portfolio *Portfolio, p PortfolioUniversePolicy) (*PortfolioUniversePolicy, error) {
	orderPlacementMu.Lock()
	defer orderPlacementMu.Unlock()
	p.PortfolioID, p.ProjectID = portfolio.ID, portfolio.ProjectID
	if err := validateUniversePolicy(&p); err != nil {
		return nil, err
	}
	includes, _ := json.Marshal(p.IncludeSymbols)
	excludes, _ := json.Marshal(p.ExcludeSymbols)
	_, err := db.Exec(`INSERT INTO portfolio_universe_policies
		(portfolio_id,project_id,selection_mode,include_symbols,exclude_symbols,reference_universe_id,require_active_listing,enforcement_enabled)
		VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(portfolio_id) DO UPDATE SET project_id=excluded.project_id,
		selection_mode=excluded.selection_mode,include_symbols=excluded.include_symbols,exclude_symbols=excluded.exclude_symbols,
		reference_universe_id=excluded.reference_universe_id,require_active_listing=excluded.require_active_listing,
		enforcement_enabled=excluded.enforcement_enabled,updated_at=CURRENT_TIMESTAMP`,
		p.PortfolioID, p.ProjectID, p.SelectionMode, string(includes), string(excludes), p.ReferenceUniverseID,
		boolInt(p.RequireActiveListing), boolInt(p.EnforcementEnabled))
	if err != nil {
		return nil, err
	}
	return dbGetPortfolioUniversePolicy(db, portfolio)
}

func portfolioUniverseViolation(db *sql.DB, portfolio *Portfolio, symbol string) (*UniverseViolation, error) {
	policy, err := dbGetPortfolioUniversePolicy(db, portfolio)
	if err != nil {
		return nil, err
	}
	return universeViolation(db, policy, symbol)
}

func universeViolation(db *sql.DB, policy *PortfolioUniversePolicy, symbol string) (*UniverseViolation, error) {
	if !policy.EnforcementEnabled {
		return nil, nil
	}
	symbol = canonicalSymbol(symbol)
	if contains(policy.ExcludeSymbols, symbol) {
		return &UniverseViolation{Code: "universe_symbol_excluded", Detail: fmt.Sprintf("%s is explicitly excluded by the portfolio universe policy", symbol)}, nil
	}
	included := contains(policy.IncludeSymbols, symbol)
	switch policy.SelectionMode {
	case "symbol_allowlist":
		if !included {
			return &UniverseViolation{Code: "universe_not_allowed", Detail: fmt.Sprintf("%s is not in the portfolio symbol allowlist", symbol)}, nil
		}
	case "reference_universe":
		member, err := referenceUniverseContainsSymbol(db, policy.ReferenceUniverseID, symbol, time.Now().UTC())
		if err != nil {
			return nil, err
		}
		if !member && !included {
			return &UniverseViolation{Code: "universe_not_member", Detail: fmt.Sprintf("%s is not an active member of reference universe %s", symbol, policy.ReferenceUniverseID)}, nil
		}
	}
	if policy.RequireActiveListing {
		active, err := activeListingExists(db, symbol, time.Now().UTC())
		if err != nil {
			return nil, err
		}
		if !active {
			return &UniverseViolation{Code: "universe_inactive_listing", Detail: fmt.Sprintf("%s has no active security-master listing", symbol)}, nil
		}
	}
	return nil, nil
}

func activeListingExists(db *sql.DB, symbol string, asOf time.Time) (bool, error) {
	var one int
	day := asOf.Format("2006-01-02")
	err := db.QueryRow(`SELECT 1 FROM security_listings WHERE symbol=? AND active=1
		AND (valid_from='' OR valid_from<=?) AND (valid_to='' OR valid_to>?) LIMIT 1`, canonicalSymbol(symbol), day, day).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

func referenceUniverseContainsSymbol(db *sql.DB, universeID, symbol string, asOf time.Time) (bool, error) {
	var one int
	day := asOf.Format("2006-01-02")
	err := db.QueryRow(`SELECT 1 FROM universe_memberships u JOIN security_listings l ON l.security_id=u.security_id
		WHERE u.universe_id=? AND l.symbol=? AND l.active=1
		AND u.valid_from<=? AND (u.valid_to='' OR u.valid_to>?)
		AND (l.valid_from='' OR l.valid_from<=?) AND (l.valid_to='' OR l.valid_to>?) LIMIT 1`,
		universeID, canonicalSymbol(symbol), day, day, day, day).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	return err == nil, err
}

// Exclusions must never trap an existing holding, but the exception is only
// for a genuinely risk-reducing sell. Outstanding sells are reserved so a
// broker-live portfolio cannot accidentally open a short via duplicate exits.
func portfolioCanReducePosition(db *sql.DB, portfolioID int64, symbol, outcome string, qty float64) bool {
	outcome = strings.ToUpper(strings.TrimSpace(outcome))
	var held float64
	err := db.QueryRow(`SELECT COALESCE(SUM(qty),0) FROM positions WHERE portfolio_id=? AND symbol=? AND COALESCE(outcome,'')=?`,
		portfolioID, canonicalSymbol(symbol), outcome).Scan(&held)
	if err != nil {
		return false
	}
	var reserved float64
	_ = db.QueryRow(`SELECT COALESCE(SUM(MAX(qty-filled_qty,0)),0) FROM orders
		WHERE portfolio_id=? AND symbol=? AND side='sell' AND status='working' AND COALESCE(outcome,'')=?`,
		portfolioID, canonicalSymbol(symbol), outcome).Scan(&reserved)
	return qty > 0 && held-reserved >= qty-1e-9
}

// MCP handlers live here to keep Trading governance cohesive and independent
// from the generic Analytics app.
func (a *App) toolPortfolioUniverseGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	portfolio, err := dbGetPortfolio(ctx.AppDB(), pid, int64Arg(args, "portfolio_id", 0))
	if err != nil {
		return nil, err
	}
	policy, err := dbGetPortfolioUniversePolicy(ctx.AppDB(), portfolio)
	if err != nil {
		return nil, err
	}
	return map[string]any{"policy": policy, "allowed_classes": portfolio.AllowedClasses}, nil
}

func (a *App) toolPortfolioUniverseUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	portfolio, err := dbGetPortfolio(ctx.AppDB(), pid, int64Arg(args, "portfolio_id", 0))
	if err != nil {
		return nil, err
	}
	current, err := dbGetPortfolioUniversePolicy(ctx.AppDB(), portfolio)
	if err != nil {
		return nil, err
	}
	if value := strings.TrimSpace(strArg(args, "selection_mode")); value != "" {
		current.SelectionMode = value
	}
	if raw, exists := args["include_symbols"]; exists {
		value, ok := stringSliceValue(raw)
		if !ok {
			return nil, errors.New("include_symbols must be an array of symbols")
		}
		current.IncludeSymbols = value
	}
	if raw, exists := args["exclude_symbols"]; exists {
		value, ok := stringSliceValue(raw)
		if !ok {
			return nil, errors.New("exclude_symbols must be an array of symbols")
		}
		current.ExcludeSymbols = value
	}
	if _, ok := args["reference_universe_id"]; ok {
		current.ReferenceUniverseID = strArg(args, "reference_universe_id")
	}
	if value, ok := boolValue(args["require_active_listing"]); ok {
		current.RequireActiveListing = value
	}
	if value, ok := boolValue(args["enforcement_enabled"]); ok {
		current.EnforcementEnabled = value
	}
	saved, err := dbUpsertPortfolioUniversePolicy(ctx.AppDB(), portfolio, *current)
	if err != nil {
		return nil, err
	}
	emit("portfolio.universe.changed", map[string]any{"portfolio_id": portfolio.ID, "policy": saved})
	return map[string]any{"policy": saved, "allowed_classes": portfolio.AllowedClasses}, nil
}

func stringSliceValue(value any) ([]string, bool) {
	if value == nil {
		return nil, false
	}
	buf, err := json.Marshal(value)
	if err != nil {
		return nil, false
	}
	var out []string
	if json.Unmarshal(buf, &out) != nil {
		return nil, false
	}
	return cleanSymbols(out), true
}

func boolValue(value any) (bool, bool) {
	switch v := value.(type) {
	case bool:
		return v, true
	case string:
		switch strings.ToLower(strings.TrimSpace(v)) {
		case "true", "1":
			return true, true
		case "false", "0":
			return false, true
		}
	case float64:
		return v != 0, true
	case int:
		return v != 0, true
	}
	return false, false
}
