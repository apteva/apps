package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"
)

var structuresByCountry = map[string]map[string]bool{
	"ES": {
		"ES_AUTONOMO": true,
		"ES_SL":       true,
	},
	"FR": {
		"FR_SAS":  true,
		"FR_SASU": true,
		"FR_SARL": true,
		"FR_EURL": true,
	},
}

var validTaxTypes = map[string]bool{
	"vat":                  true,
	"income_tax":           true,
	"corporate_tax":        true,
	"social_contributions": true,
}

var validObligationStatuses = map[string]bool{
	"estimated": true,
	"draft":     true,
	"filed":     true,
	"paid":      true,
	"waived":    true,
}

func validateProfile(profile Profile) error {
	if strings.TrimSpace(profile.ProjectID) == "" {
		return errors.New("project context is required")
	}
	if strings.TrimSpace(profile.Name) == "" {
		return errors.New("profile name is required")
	}
	allowed, ok := structuresByCountry[profile.Country]
	if !ok || !allowed[profile.Structure] {
		return fmt.Errorf("structure %q is not valid for country %q", profile.Structure, profile.Country)
	}
	switch strings.ToLower(profile.FilingCadence) {
	case "monthly", "quarterly", "annual":
	default:
		return fmt.Errorf("unsupported filing cadence %q", profile.FilingCadence)
	}
	switch strings.ToLower(profile.AccountingBasis) {
	case "cash", "accrual":
	default:
		return fmt.Errorf("unsupported accounting basis %q", profile.AccountingBasis)
	}
	if len(profile.Currency) != 3 {
		return errors.New("currency must be a three-letter ISO code")
	}
	if _, err := time.Parse("01-02", profile.FiscalYearStart); err != nil {
		return errors.New("fiscal_year_start must use MM-DD")
	}
	if _, err := time.Parse("01-02", profile.FiscalYearEnd); err != nil {
		return errors.New("fiscal_year_end must use MM-DD")
	}
	if profile.Structure == "FR_EURL" {
		regime := strings.ToLower(stringFromAny(profile.Config["tax_regime"]))
		if regime != "" && regime != "income_tax" && regime != "corporate_tax" {
			return errors.New("FR_EURL config.tax_regime must be income_tax or corporate_tax")
		}
	}
	if monthly := int64FromAny(profile.Config["monthly_social_contribution_cents"]); monthly < 0 {
		return errors.New("config.monthly_social_contribution_cents cannot be negative")
	}
	return nil
}

func validateTaxType(taxType string) error {
	if !validTaxTypes[taxType] {
		return fmt.Errorf("unsupported tax_type %q", taxType)
	}
	return nil
}

func validateObligationInput(profile Profile, taxType, direction string, amount int64, currency, status string) error {
	if err := validateTaxType(taxType); err != nil {
		return err
	}
	if direction != "payable" && direction != "receivable" {
		return errors.New("direction must be payable or receivable")
	}
	if amount < 0 {
		return errors.New("amount_cents cannot be negative; use direction=receivable for refunds")
	}
	if strings.ToUpper(currency) != profile.Currency {
		return fmt.Errorf("currency %s does not match profile currency %s", currency, profile.Currency)
	}
	if !validObligationStatuses[status] {
		return fmt.Errorf("unsupported obligation status %q", status)
	}
	return nil
}

func stringSliceArg(args map[string]any, key string) []string {
	raw, ok := args[key].([]any)
	if !ok {
		if typed, ok := args[key].([]string); ok {
			return typed
		}
		return nil
	}
	out := make([]string, 0, len(raw))
	for _, item := range raw {
		if value := normalizeTaxType(stringFromAny(item)); value != "" {
			out = append(out, value)
		}
	}
	return out
}

func containsString(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}

func setObligationBillLink(db *sql.DB, obligation Obligation, billID int64) error {
	metadata := obligation.Metadata
	if metadata == nil {
		metadata = map[string]any{}
	}
	metadata["bills_bill_id"] = billID
	raw, err := json.Marshal(metadata)
	if err != nil {
		return err
	}
	_, err = db.Exec(`UPDATE tax_obligations SET metadata_json=?, updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, string(raw), obligation.ProjectID, obligation.ID)
	return err
}

func profileCountryForObligation(db *sql.DB, obligation Obligation) string {
	var country string
	_ = db.QueryRow(`SELECT country FROM tax_profiles WHERE project_id=? AND id=?`, obligation.ProjectID, obligation.ProfileID).Scan(&country)
	if country == "" {
		return "tax"
	}
	return country
}

func max64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func reportObligations(db *sql.DB, profile Profile, periodID int64, start, end string, taxTypes []string) ([]Obligation, error) {
	where := []string{"o.project_id=?", "o.profile_id=?"}
	values := []any{profile.ProjectID, profile.ID}
	if periodID > 0 {
		where = append(where, "o.period_id=?")
		values = append(values, periodID)
	} else {
		if start != "" {
			where = append(where, "COALESCE(p.period_end,c.period_end)>=?")
			values = append(values, start)
		}
		if end != "" {
			where = append(where, "COALESCE(p.period_start,c.period_start)<=?")
			values = append(values, end)
		}
	}
	where, values = appendTaxTypeFilter(where, values, "o.tax_type", taxTypes)
	rows, err := db.Query(`SELECT o.id,o.project_id,o.profile_id,COALESCE(o.period_id,0),COALESCE(o.calculation_id,0),
		o.tax_type,o.authority,o.title,o.amount_cents,o.currency,o.due_date,o.direction,o.status,o.filed_at,o.filing_ref,o.waived_reason,o.metadata_json
		FROM tax_obligations o
		LEFT JOIN tax_periods p ON p.id=o.period_id
		LEFT JOIN tax_calculations c ON c.id=o.calculation_id
		WHERE `+strings.Join(where, " AND ")+` ORDER BY o.due_date,o.id`, values...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Obligation{}
	for rows.Next() {
		obligation, err := scanObligation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, obligation)
	}
	return out, rows.Err()
}

func reportAdjustments(db *sql.DB, profile Profile, periodID int64, start, end string, taxTypes []string) ([]map[string]any, error) {
	where := []string{"a.project_id=?", "a.profile_id=?"}
	values := []any{profile.ProjectID, profile.ID}
	if periodID > 0 {
		where = append(where, "a.period_id=?")
		values = append(values, periodID)
	} else {
		if start != "" {
			where = append(where, "(p.period_end>=? OR a.period_id IS NULL)")
			values = append(values, start)
		}
		if end != "" {
			where = append(where, "(p.period_start<=? OR a.period_id IS NULL)")
			values = append(values, end)
		}
	}
	where, values = appendTaxTypeFilter(where, values, "a.tax_type", taxTypes)
	rows, err := db.Query(`SELECT a.id,a.project_id,a.profile_id,COALESCE(a.period_id,0),a.tax_type,a.kind,a.amount_cents,a.currency,a.reason,a.status,a.metadata_json
		FROM tax_adjustments a LEFT JOIN tax_periods p ON p.id=a.period_id
		WHERE `+strings.Join(where, " AND ")+` ORDER BY a.id DESC`, values...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanRows(rows)
}

func appendTaxTypeFilter(where []string, values []any, column string, taxTypes []string) ([]string, []any) {
	if len(taxTypes) == 0 {
		return where, values
	}
	placeholders := make([]string, len(taxTypes))
	for i, taxType := range taxTypes {
		placeholders[i] = "?"
		values = append(values, taxType)
	}
	where = append(where, column+" IN ("+strings.Join(placeholders, ",")+")")
	return where, values
}

func reportTitle(profile Profile, periodID int64, start, end string) string {
	if periodID > 0 {
		return fmt.Sprintf("Tax report - %s - period %d", profile.Name, periodID)
	}
	if start != "" || end != "" {
		return fmt.Sprintf("Tax report - %s - %s to %s", profile.Name, start, end)
	}
	return "Tax report - " + profile.Name
}
