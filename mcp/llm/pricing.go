package main

import (
	"database/sql"
	"errors"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// ModelPrice is the sell-side rate for a public alias. It is deliberately keyed
// on the alias rather than the upstream model: margin should survive a backend
// swap, and customers are quoted the alias.
//
// PlanID is reserved for per-plan price books. Resolution currently matches the
// empty plan, so setting it has no effect until plans exist.
type ModelPrice struct {
	ID                         int64  `json:"id,omitempty"`
	ProjectID                  string `json:"project_id"`
	PlanID                     string `json:"plan_id,omitempty"`
	Alias                      string `json:"alias"`
	Currency                   string `json:"currency"`
	InputMicrounitsPerMillion  int64  `json:"input_microunits_per_million"`
	OutputMicrounitsPerMillion int64  `json:"output_microunits_per_million"`
	RequestMicrounits          int64  `json:"request_microunits,omitempty"`
	MinimumChargeMicrounits    int64  `json:"minimum_charge_microunits,omitempty"`
	PriceVersion               int64  `json:"price_version"`
	EffectiveFrom              string `json:"effective_from,omitempty"`
	EffectiveTo                string `json:"effective_to,omitempty"`
}

const modelPriceColumns = `id, project_id, plan_id, alias, currency, input_microunits_per_million,
	output_microunits_per_million, request_microunits, minimum_charge_microunits, price_version,
	COALESCE(effective_from,''), COALESCE(effective_to,'')`

func scanModelPrice(scan func(dest ...any) error) (*ModelPrice, error) {
	var out ModelPrice
	if err := scan(&out.ID, &out.ProjectID, &out.PlanID, &out.Alias, &out.Currency,
		&out.InputMicrounitsPerMillion, &out.OutputMicrounitsPerMillion, &out.RequestMicrounits,
		&out.MinimumChargeMicrounits, &out.PriceVersion, &out.EffectiveFrom, &out.EffectiveTo); err != nil {
		return nil, err
	}
	return &out, nil
}

// dbModelPriceResolve prefers a project-specific price and falls back to the
// global price book, mirroring how aliases resolve.
func dbModelPriceResolve(db *sql.DB, projectID, alias string) (*ModelPrice, error) {
	alias = normalizeAlias(alias)
	if alias == "" {
		return nil, nil
	}
	for _, scope := range []string{strings.TrimSpace(projectID), ""} {
		row := db.QueryRow(`SELECT `+modelPriceColumns+` FROM model_prices
			WHERE project_id = ? AND plan_id = '' AND alias = ? AND effective_to IS NULL`, scope, alias)
		price, err := scanModelPrice(row.Scan)
		if errors.Is(err, sql.ErrNoRows) {
			if strings.TrimSpace(projectID) == "" {
				break
			}
			continue
		}
		if err != nil {
			return nil, err
		}
		return price, nil
	}
	return nil, nil
}

func dbModelPricesList(db *sql.DB, projectID string, includeHistory bool) ([]*ModelPrice, error) {
	query := `SELECT ` + modelPriceColumns + ` FROM model_prices WHERE (project_id = ? OR project_id = '')`
	if !includeHistory {
		query += ` AND effective_to IS NULL`
	}
	query += ` ORDER BY alias ASC, effective_from DESC`
	rows, err := db.Query(query, strings.TrimSpace(projectID))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []*ModelPrice{}
	for rows.Next() {
		price, err := scanModelPrice(rows.Scan)
		if err != nil {
			return nil, err
		}
		out = append(out, price)
	}
	return out, rows.Err()
}

// dbModelPriceUpsert closes the active price and opens a new version, so
// historical invoices stay reproducible after a price change.
func dbModelPriceUpsert(db *sql.DB, projectID string, args map[string]any) (*ModelPrice, error) {
	alias := normalizeAlias(strArg(args, "alias"))
	if alias == "" {
		return nil, userError("alias is required")
	}
	currency := strings.ToUpper(firstNonEmpty(strings.TrimSpace(strArg(args, "currency")), "USD"))
	input := int64Arg(args, "input_microunits_per_million")
	output := int64Arg(args, "output_microunits_per_million")
	request := int64Arg(args, "request_microunits")
	minimum := int64Arg(args, "minimum_charge_microunits")
	if input < 0 || output < 0 || request < 0 || minimum < 0 {
		return nil, userError("prices must not be negative")
	}
	if input == 0 && output == 0 && request == 0 {
		return nil, userError("at least one of input, output, or request price must be set")
	}
	scope := strings.TrimSpace(projectID)

	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var version int64
	if err := tx.QueryRow(`SELECT COALESCE(MAX(price_version),0) FROM model_prices
		WHERE project_id = ? AND plan_id = '' AND alias = ?`, scope, alias).Scan(&version); err != nil {
		return nil, err
	}
	if _, err := tx.Exec(`UPDATE model_prices SET effective_to = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		WHERE project_id = ? AND plan_id = '' AND alias = ? AND effective_to IS NULL`, scope, alias); err != nil {
		return nil, err
	}
	res, err := tx.Exec(`INSERT INTO model_prices
		(project_id, plan_id, alias, currency, input_microunits_per_million, output_microunits_per_million,
		 request_microunits, minimum_charge_microunits, price_version, updated_at)
		VALUES (?, '', ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)`,
		scope, alias, currency, input, output, request, minimum, version+1)
	if err != nil {
		return nil, err
	}
	id, err := res.LastInsertId()
	if err != nil {
		return nil, err
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	row := db.QueryRow(`SELECT `+modelPriceColumns+` FROM model_prices WHERE id = ?`, id)
	return scanModelPrice(row.Scan)
}

func dbModelPriceDelete(db *sql.DB, projectID, alias string) error {
	alias = normalizeAlias(alias)
	if alias == "" {
		return userError("alias is required")
	}
	res, err := db.Exec(`UPDATE model_prices SET effective_to = CURRENT_TIMESTAMP, updated_at = CURRENT_TIMESTAMP
		WHERE project_id = ? AND plan_id = '' AND alias = ? AND effective_to IS NULL`,
		strings.TrimSpace(projectID), alias)
	if err != nil {
		return err
	}
	if n, err := res.RowsAffected(); err == nil && n == 0 {
		return userError("no active price for that alias")
	}
	return nil
}

// calculateBilledAmount returns what the customer owes, in microunits.
func calculateBilledAmount(price *ModelPrice, input, output int64) int64 {
	if price == nil {
		return 0
	}
	if input < 0 {
		input = 0
	}
	if output < 0 {
		output = 0
	}
	amount := price.RequestMicrounits
	amount += mulDivRoundHalfUp(input, price.InputMicrounitsPerMillion, 1_000_000)
	amount += mulDivRoundHalfUp(output, price.OutputMicrounitsPerMillion, 1_000_000)
	if price.MinimumChargeMicrounits > amount {
		amount = price.MinimumChargeMicrounits
	}
	return amount
}

// mulDivRoundHalfUp computes tokens*rate/scale with half-up rounding, keeping
// sub-microunit amounts from silently rounding to zero revenue.
func mulDivRoundHalfUp(tokens, rate, scale int64) int64 {
	if tokens <= 0 || rate <= 0 || scale <= 0 {
		return 0
	}
	return (tokens*rate + scale/2) / scale
}

// applyBilling prices a committed usage event against the alias the caller
// used. Unpriced aliases record zero, which keeps metering working before a
// price book exists.
func applyBilling(db *sql.DB, ev *UsageEvent, alias string) error {
	if ev == nil || ev.ID == 0 || strings.TrimSpace(alias) == "" {
		return nil
	}
	price, err := dbModelPriceResolve(db, ev.ProjectID, alias)
	if err != nil || price == nil {
		return err
	}
	amount := calculateBilledAmount(price, ev.RequestTokens, ev.ResponseTokens)
	if _, err := db.Exec(`UPDATE usage_events
		SET billed_amount_microunits = ?, billed_currency = ?, price_version = ?
		WHERE id = ?`, amount, price.Currency, price.PriceVersion, ev.ID); err != nil {
		return err
	}
	ev.Alias = alias
	ev.BilledAmountMicrounits = amount
	ev.BilledCurrency = price.Currency
	ev.PriceVersion = price.PriceVersion
	return nil
}

func (a *App) toolPricesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	rows, err := dbModelPricesList(ctx.AppDB(), projectFromArgs(args), boolArg(args, "include_history"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"prices": rows}, nil
}

func (a *App) toolPriceUpsert(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	price, err := dbModelPriceUpsert(ctx.AppDB(), projectFromArgs(args), args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"price": price}, nil
}

func (a *App) toolPriceDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if err := dbModelPriceDelete(ctx.AppDB(), projectFromArgs(args), strArg(args, "alias")); err != nil {
		return nil, err
	}
	return map[string]any{"status": "closed"}, nil
}
