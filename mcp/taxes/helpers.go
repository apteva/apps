package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func seedTaxRules(db *sql.DB) error {
	rules := []Rule{
		{Country: "ES", Structure: "ES_AUTONOMO", TaxType: "vat", Year: 2026, Version: "es-2026-v1", EffectiveFrom: "2026-01-01", SourceURL: "https://sede.agenciatributaria.gob.es/", Rules: map[string]any{"vat_standard_bps": 2100, "reduced_bps": []int{1000, 400, 0}, "authority": "Agencia Tributaria"}},
		{Country: "ES", Structure: "ES_AUTONOMO", TaxType: "income_tax", Year: 2026, Version: "es-autonomo-2026-v1", EffectiveFrom: "2026-01-01", SourceURL: "https://sede.agenciatributaria.gob.es/", Rules: map[string]any{"income_tax_bps": 2000, "authority": "Agencia Tributaria", "warning": "Flat estimate; actual IRPF is progressive and depends on personal circumstances."}},
		{Country: "ES", Structure: "ES_AUTONOMO", TaxType: "social_contributions", Year: 2026, Version: "es-autonomo-social-2026-v1", EffectiveFrom: "2026-01-01", SourceURL: "https://www.seg-social.es/", Rules: map[string]any{"monthly_cents": 30000, "authority": "Tesoreria General de la Seguridad Social", "warning": "Default monthly quota placeholder; configure the actual base/quota per profile."}},
		{Country: "ES", Structure: "ES_SL", TaxType: "vat", Year: 2026, Version: "es-sl-vat-2026-v1", EffectiveFrom: "2026-01-01", SourceURL: "https://sede.agenciatributaria.gob.es/", Rules: map[string]any{"vat_standard_bps": 2100, "reduced_bps": []int{1000, 400, 0}, "authority": "Agencia Tributaria"}},
		{Country: "ES", Structure: "ES_SL", TaxType: "corporate_tax", Year: 2026, Version: "es-sl-is-2026-v1", EffectiveFrom: "2026-01-01", SourceURL: "https://sede.agenciatributaria.gob.es/", Rules: map[string]any{"corporate_tax_bps": 2500, "authority": "Agencia Tributaria", "warning": "General-rate estimate; reduced rates and special regimes require profile config/adjustments."}},
		{Country: "FR", Structure: "FR_SAS", TaxType: "vat", Year: 2026, Version: "fr-vat-2026-v1", EffectiveFrom: "2026-01-01", SourceURL: "https://www.impots.gouv.fr/", Rules: map[string]any{"vat_standard_bps": 2000, "reduced_bps": []int{1000, 550, 210}, "authority": "Direction generale des Finances publiques"}},
		{Country: "FR", Structure: "FR_SAS", TaxType: "corporate_tax", Year: 2026, Version: "fr-sas-is-2026-v1", EffectiveFrom: "2026-01-01", SourceURL: "https://www.impots.gouv.fr/", Rules: map[string]any{"corporate_tax_bps": 2500, "authority": "Direction generale des Finances publiques"}},
		{Country: "FR", Structure: "FR_SASU", TaxType: "vat", Year: 2026, Version: "fr-vat-2026-v1", EffectiveFrom: "2026-01-01", SourceURL: "https://www.impots.gouv.fr/", Rules: map[string]any{"vat_standard_bps": 2000, "reduced_bps": []int{1000, 550, 210}, "authority": "Direction generale des Finances publiques"}},
		{Country: "FR", Structure: "FR_SASU", TaxType: "corporate_tax", Year: 2026, Version: "fr-sasu-is-2026-v1", EffectiveFrom: "2026-01-01", SourceURL: "https://www.impots.gouv.fr/", Rules: map[string]any{"corporate_tax_bps": 2500, "authority": "Direction generale des Finances publiques"}},
		{Country: "FR", Structure: "FR_SARL", TaxType: "vat", Year: 2026, Version: "fr-vat-2026-v1", EffectiveFrom: "2026-01-01", SourceURL: "https://www.impots.gouv.fr/", Rules: map[string]any{"vat_standard_bps": 2000, "reduced_bps": []int{1000, 550, 210}, "authority": "Direction generale des Finances publiques"}},
		{Country: "FR", Structure: "FR_SARL", TaxType: "corporate_tax", Year: 2026, Version: "fr-sarl-is-2026-v1", EffectiveFrom: "2026-01-01", SourceURL: "https://www.impots.gouv.fr/", Rules: map[string]any{"corporate_tax_bps": 2500, "authority": "Direction generale des Finances publiques"}},
		{Country: "FR", Structure: "FR_EURL", TaxType: "income_tax", Year: 2026, Version: "fr-eurl-ir-2026-v1", EffectiveFrom: "2026-01-01", SourceURL: "https://www.impots.gouv.fr/", Rules: map[string]any{"income_tax_bps": 2500, "authority": "Direction generale des Finances publiques", "warning": "Flat estimate; actual French income tax is progressive and household-dependent."}},
	}
	for _, r := range rules {
		if _, err := db.Exec(`INSERT OR IGNORE INTO tax_rules (country,structure,tax_type,year,version,effective_from,source_url,rules_json,active) VALUES (?,?,?,?,?,?,?,?,1)`,
			r.Country, r.Structure, r.TaxType, r.Year, r.Version, r.EffectiveFrom, r.SourceURL, mustJSON(r.Rules)); err != nil {
			return err
		}
	}
	return nil
}

func getProfile(db *sql.DB, projectID string, id int64) (Profile, error) {
	if id <= 0 {
		return Profile{}, errors.New("profile id is required")
	}
	row := db.QueryRow(`SELECT id,project_id,name,country,structure,region,fiscal_year_start,fiscal_year_end,vat_registered,filing_cadence,accounting_basis,currency,config_json,archived FROM tax_profiles WHERE project_id=? AND id=?`, projectID, id)
	return scanProfile(row)
}

type profileScanner interface {
	Scan(dest ...any) error
}

func scanProfile(row profileScanner) (Profile, error) {
	var p Profile
	var vat, archived int
	var cfg string
	if err := row.Scan(&p.ID, &p.ProjectID, &p.Name, &p.Country, &p.Structure, &p.Region, &p.FiscalYearStart, &p.FiscalYearEnd, &vat, &p.FilingCadence, &p.AccountingBasis, &p.Currency, &cfg, &archived); err != nil {
		return Profile{}, err
	}
	p.VATRegistered = vat != 0
	p.Archived = archived != 0
	p.Config = parseJSONMap(cfg)
	return p, nil
}

func listRules(db *sql.DB, args map[string]any) ([]Rule, error) {
	where := []string{"1=1"}
	vals := []any{}
	addStringFilter(&where, &vals, "country", strings.ToUpper(stringArg(args, "country", "")))
	addStringFilter(&where, &vals, "structure", strings.ToUpper(stringArg(args, "structure", "")))
	addStringFilter(&where, &vals, "tax_type", normalizeTaxType(stringArg(args, "tax_type", "")))
	if year := intArg(args, "year", 0); year > 0 {
		where = append(where, "year=?")
		vals = append(vals, year)
	}
	if !hasArg(args, "active") || boolArg(args, "active", true) {
		where = append(where, "active=1")
	}
	rows, err := db.Query(`SELECT id,country,structure,tax_type,year,version,effective_from,effective_to,source_url,rules_json,active FROM tax_rules WHERE `+strings.Join(where, " AND ")+` ORDER BY country,structure,tax_type,year DESC`, vals...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Rule
	for rows.Next() {
		r, err := scanRule(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

type ruleScanner interface {
	Scan(dest ...any) error
}

func scanRule(row ruleScanner) (Rule, error) {
	var r Rule
	var raw string
	var active int
	if err := row.Scan(&r.ID, &r.Country, &r.Structure, &r.TaxType, &r.Year, &r.Version, &r.EffectiveFrom, &r.EffectiveTo, &r.SourceURL, &raw, &active); err != nil {
		return Rule{}, err
	}
	r.Rules = parseJSONMap(raw)
	r.Active = active != 0
	return r, nil
}

func getRuleByID(db *sql.DB, id int64) (Rule, error) {
	row := db.QueryRow(`SELECT id,country,structure,tax_type,year,version,effective_from,effective_to,source_url,rules_json,active FROM tax_rules WHERE id=?`, id)
	return scanRule(row)
}

func findRule(db *sql.DB, country, structure, taxType string, year int) (Rule, error) {
	row := db.QueryRow(`SELECT id,country,structure,tax_type,year,version,effective_from,effective_to,source_url,rules_json,active
		FROM tax_rules
		WHERE country=? AND structure=? AND tax_type=? AND year<=? AND active=1
		ORDER BY year DESC, id DESC LIMIT 1`, country, structure, normalizeTaxType(taxType), year)
	r, err := scanRule(row)
	if err == nil {
		return r, nil
	}
	if errors.Is(err, sql.ErrNoRows) {
		row = db.QueryRow(`SELECT id,country,structure,tax_type,year,version,effective_from,effective_to,source_url,rules_json,active
			FROM tax_rules
			WHERE country=? AND structure='DEFAULT' AND tax_type=? AND year<=? AND active=1
			ORDER BY year DESC, id DESC LIMIT 1`, country, normalizeTaxType(taxType), year)
		return scanRule(row)
	}
	return Rule{}, err
}

func getPeriod(db *sql.DB, projectID string, id int64) (map[string]any, error) {
	rows, err := db.Query(`SELECT id,project_id,profile_id,tax_type,period_start,period_end,due_date,status,filed_at,filing_ref,metadata_json FROM tax_periods WHERE project_id=? AND id=?`, projectID, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := scanRows(rows)
	if err != nil {
		return nil, err
	}
	if len(items) == 0 {
		return nil, sql.ErrNoRows
	}
	return items[0], nil
}

type inferredPeriod struct {
	TaxType     string
	PeriodStart string
	PeriodEnd   string
	DueDate     string
}

func generatePeriodsForProfile(db *sql.DB, profile Profile, year int) ([]map[string]any, error) {
	inferred := inferPeriods(profile, year)
	out := []map[string]any{}
	for _, p := range inferred {
		if _, err := db.Exec(`INSERT INTO tax_periods
			(project_id,profile_id,tax_type,period_start,period_end,due_date,metadata_json)
			VALUES (?,?,?,?,?,?,?)
			ON CONFLICT(project_id, profile_id, tax_type, period_start, period_end)
			DO UPDATE SET due_date=excluded.due_date, updated_at=CURRENT_TIMESTAMP`,
			profile.ProjectID, profile.ID, p.TaxType, p.PeriodStart, p.PeriodEnd, p.DueDate,
			mustJSON(map[string]any{"generated": true, "source": "tax_profile"})); err != nil {
			return nil, err
		}
	}
	rows, err := db.Query(`SELECT id,project_id,profile_id,tax_type,period_start,period_end,due_date,status,filed_at,filing_ref,metadata_json
		FROM tax_periods
		WHERE project_id=? AND profile_id=? AND period_start>=? AND period_end<=?
		ORDER BY period_start, tax_type`, profile.ProjectID, profile.ID, fmt.Sprintf("%04d-01-01", year), fmt.Sprintf("%04d-12-31", year))
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out, err = scanRows(rows)
	return out, err
}

func inferPeriods(profile Profile, year int) []inferredPeriod {
	taxTypes := inferredTaxTypes(profile)
	out := []inferredPeriod{}
	for _, taxType := range taxTypes {
		switch taxType {
		case "social_contributions":
			out = append(out, monthlyPeriods(year, taxType, profile.Country)...)
		case "income_tax":
			if profile.Country == "ES" && profile.Structure == "ES_AUTONOMO" {
				out = append(out, quarterlyPeriods(year, taxType, profile.Country)...)
			} else {
				out = append(out, annualPeriod(year, taxType, profile.Country))
			}
		case "corporate_tax":
			out = append(out, annualPeriod(year, taxType, profile.Country))
		case "vat":
			switch strings.ToLower(profile.FilingCadence) {
			case "monthly":
				out = append(out, monthlyPeriods(year, taxType, profile.Country)...)
			case "annual":
				out = append(out, annualPeriod(year, taxType, profile.Country))
			default:
				out = append(out, quarterlyPeriods(year, taxType, profile.Country)...)
			}
		}
	}
	return out
}

func inferredTaxTypes(profile Profile) []string {
	switch profile.Structure {
	case "ES_AUTONOMO":
		return []string{"vat", "income_tax", "social_contributions"}
	case "ES_SL":
		return []string{"vat", "corporate_tax"}
	case "FR_SAS", "FR_SASU", "FR_SARL":
		return []string{"vat", "corporate_tax"}
	case "FR_EURL":
		return []string{"vat", "income_tax"}
	default:
		return []string{"vat"}
	}
}

func quarterlyPeriods(year int, taxType, country string) []inferredPeriod {
	quarters := [][2]string{{"01-01", "03-31"}, {"04-01", "06-30"}, {"07-01", "09-30"}, {"10-01", "12-31"}}
	out := []inferredPeriod{}
	for i, q := range quarters {
		out = append(out, inferredPeriod{
			TaxType:     taxType,
			PeriodStart: fmt.Sprintf("%04d-%s", year, q[0]),
			PeriodEnd:   fmt.Sprintf("%04d-%s", year, q[1]),
			DueDate:     dueDateFor(country, taxType, year, i+1, "quarterly"),
		})
	}
	return out
}

func monthlyPeriods(year int, taxType, country string) []inferredPeriod {
	out := []inferredPeriod{}
	for month := 1; month <= 12; month++ {
		start := time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC)
		end := start.AddDate(0, 1, -1)
		out = append(out, inferredPeriod{
			TaxType:     taxType,
			PeriodStart: start.Format("2006-01-02"),
			PeriodEnd:   end.Format("2006-01-02"),
			DueDate:     dueDateFor(country, taxType, year, month, "monthly"),
		})
	}
	return out
}

func annualPeriod(year int, taxType, country string) inferredPeriod {
	return inferredPeriod{
		TaxType:     taxType,
		PeriodStart: fmt.Sprintf("%04d-01-01", year),
		PeriodEnd:   fmt.Sprintf("%04d-12-31", year),
		DueDate:     dueDateFor(country, taxType, year, 0, "annual"),
	}
}

func dueDateFor(country, taxType string, year, ordinal int, cadence string) string {
	switch country {
	case "ES":
		if taxType == "corporate_tax" {
			return fmt.Sprintf("%04d-07-25", year+1)
		}
		if cadence == "monthly" {
			month := ordinal + 1
			dueYear := year
			if month == 13 {
				month = 1
				dueYear = year + 1
			}
			return fmt.Sprintf("%04d-%02d-20", dueYear, month)
		}
		if cadence == "quarterly" {
			due := []string{
				fmt.Sprintf("%04d-04-20", year),
				fmt.Sprintf("%04d-07-20", year),
				fmt.Sprintf("%04d-10-20", year),
				fmt.Sprintf("%04d-01-20", year+1),
			}
			if ordinal >= 1 && ordinal <= len(due) {
				return due[ordinal-1]
			}
		}
	case "FR":
		if taxType == "corporate_tax" {
			return fmt.Sprintf("%04d-05-15", year+1)
		}
		if cadence == "monthly" {
			month := ordinal + 1
			dueYear := year
			if month == 13 {
				month = 1
				dueYear = year + 1
			}
			return fmt.Sprintf("%04d-%02d-24", dueYear, month)
		}
		if cadence == "quarterly" {
			due := []string{
				fmt.Sprintf("%04d-04-24", year),
				fmt.Sprintf("%04d-07-24", year),
				fmt.Sprintf("%04d-10-24", year),
				fmt.Sprintf("%04d-01-24", year+1),
			}
			if ordinal >= 1 && ordinal <= len(due) {
				return due[ordinal-1]
			}
		}
	}
	return ""
}

func calculateOutputs(taxType string, rule Rule, inputs map[string]any, start, end string) map[string]any {
	revenue := int64FromAny(inputs["revenue_cents"])
	expenses := int64FromAny(inputs["expenses_cents"])
	outputTax := int64FromAny(inputs["output_tax_cents"])
	inputTax := int64FromAny(inputs["input_tax_cents"])
	profit := int64FromAny(inputs["taxable_profit_cents"])
	if profit == 0 {
		profit = revenue - expenses
	}
	if profit < 0 {
		profit = 0
	}
	out := map[string]any{"tax_type": taxType, "period_start": start, "period_end": end}
	switch taxType {
	case "vat":
		rate := int64FromAny(rule.Rules["vat_standard_bps"])
		if outputTax == 0 && revenue > 0 {
			outputTax = revenue * rate / 10000
		}
		if inputTax == 0 && expenses > 0 {
			inputTax = expenses * rate / 10000
		}
		out["output_tax_cents"] = outputTax
		out["input_tax_cents"] = inputTax
		out["estimated_payable_cents"] = outputTax - inputTax
		out["rate_bps"] = rate
	case "income_tax":
		rate := int64FromAny(rule.Rules["income_tax_bps"])
		out["taxable_profit_cents"] = profit
		out["estimated_payable_cents"] = profit * rate / 10000
		out["rate_bps"] = rate
	case "corporate_tax":
		rate := int64FromAny(rule.Rules["corporate_tax_bps"])
		out["taxable_profit_cents"] = profit
		out["estimated_payable_cents"] = profit * rate / 10000
		out["rate_bps"] = rate
	case "social_contributions":
		monthly := int64FromAny(inputs["social_contribution_cents"])
		if monthly == 0 {
			monthly = int64FromAny(rule.Rules["monthly_cents"])
		}
		months := int64FromAny(inputs["months"])
		if months <= 0 {
			months = int64(monthsBetween(start, end))
		}
		out["monthly_cents"] = monthly
		out["months"] = months
		out["estimated_payable_cents"] = monthly * months
	default:
		out["estimated_payable_cents"] = int64(0)
	}
	if msg, ok := rule.Rules["warning"].(string); ok && msg != "" {
		out["rule_warning"] = msg
	}
	return out
}

func insertCalculation(db *sql.DB, profile Profile, periodID int64, taxType string, rule Rule, start, end string, inputs, outputs, sources map[string]any, warnings []string) (int64, error) {
	conf := confidence(warnings)
	res, err := db.Exec(`INSERT INTO tax_calculations (project_id,profile_id,period_id,tax_type,rule_id,rule_version,period_start,period_end,inputs_json,outputs_json,sources_json,warnings_json,confidence) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		profile.ProjectID, profile.ID, nullInt(periodID), taxType, rule.ID, rule.Version, start, end, mustJSON(inputs), mustJSON(outputs), mustJSON(sources), mustJSON(warnings), conf)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	audit(db, profile.ProjectID, "tax_calculation", id, "created", taxType, map[string]any{"confidence": conf})
	return id, nil
}

func upsertEstimatedObligation(db *sql.DB, profile Profile, periodID, calcID int64, taxType string, outputs map[string]any, dueDate string) (*Obligation, error) {
	title := fmt.Sprintf("%s %s estimate", profile.Name, strings.ReplaceAll(taxType, "_", " "))
	amount := int64FromAny(outputs["estimated_payable_cents"])
	existing := int64(0)
	if periodID > 0 {
		_ = db.QueryRow(`SELECT id FROM tax_obligations WHERE project_id=? AND profile_id=? AND period_id=? AND tax_type=? AND status IN ('estimated','draft') ORDER BY id DESC LIMIT 1`, profile.ProjectID, profile.ID, periodID, taxType).Scan(&existing)
	}
	if existing > 0 {
		_, err := db.Exec(`UPDATE tax_obligations SET calculation_id=?, title=?, amount_cents=?, currency=?, due_date=?, authority=?, updated_at=CURRENT_TIMESTAMP WHERE id=?`,
			calcID, title, amount, profile.Currency, dueDate, defaultAuthority(profile, taxType), existing)
		if err != nil {
			return nil, err
		}
		o, err := getObligation(db, profile.ProjectID, existing)
		return &o, err
	}
	return insertObligation(db, profile, periodID, calcID, taxType, title, amount, profile.Currency, dueDate, defaultAuthority(profile, taxType), "estimated", nil)
}

func insertObligation(db *sql.DB, profile Profile, periodID, calcID int64, taxType, title string, amount int64, currency, dueDate, authority, status string, metadata map[string]any) (*Obligation, error) {
	if taxType == "" {
		return nil, errors.New("tax_type is required")
	}
	if title == "" {
		title = strings.ReplaceAll(taxType, "_", " ") + " obligation"
	}
	if currency == "" {
		currency = profile.Currency
	}
	if status == "" {
		status = "estimated"
	}
	res, err := db.Exec(`INSERT INTO tax_obligations (project_id,profile_id,period_id,calculation_id,tax_type,authority,title,amount_cents,currency,due_date,status,metadata_json) VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		profile.ProjectID, profile.ID, nullInt(periodID), nullInt(calcID), taxType, authority, title, amount, currency, dueDate, status, mustJSON(metadata))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	audit(db, profile.ProjectID, "tax_obligation", id, "created", title, nil)
	o, err := getObligation(db, profile.ProjectID, id)
	return &o, err
}

func getObligation(db *sql.DB, projectID string, id int64) (Obligation, error) {
	row := db.QueryRow(`SELECT id,project_id,profile_id,COALESCE(period_id,0),COALESCE(calculation_id,0),tax_type,authority,title,amount_cents,currency,due_date,status,filed_at,filing_ref,waived_reason,metadata_json FROM tax_obligations WHERE project_id=? AND id=?`, projectID, id)
	return scanObligation(row)
}

type obligationScanner interface {
	Scan(dest ...any) error
}

func scanObligation(row obligationScanner) (Obligation, error) {
	var o Obligation
	var meta string
	if err := row.Scan(&o.ID, &o.ProjectID, &o.ProfileID, &o.PeriodID, &o.CalculationID, &o.TaxType, &o.Authority, &o.Title, &o.AmountCents, &o.Currency, &o.DueDate, &o.Status, &o.FiledAt, &o.FilingRef, &o.WaivedReason, &meta); err != nil {
		return Obligation{}, err
	}
	o.Metadata = parseJSONMap(meta)
	return o, nil
}

func adjustmentsFor(db *sql.DB, projectID string, profileID, periodID int64, taxType string) []map[string]any {
	where := []string{"project_id=?", "profile_id=?", "tax_type=?", "status='active'"}
	vals := []any{projectID, profileID, taxType}
	if periodID > 0 {
		where = append(where, "(period_id=? OR period_id IS NULL)")
		vals = append(vals, periodID)
	}
	rows, err := db.Query(`SELECT id,kind,amount_cents,currency,reason FROM tax_adjustments WHERE `+strings.Join(where, " AND "), vals...)
	if err != nil {
		return nil
	}
	defer rows.Close()
	items, _ := scanRows(rows)
	return items
}

func syncBilling(ctx *sdk.AppCtx, profile Profile, start, end string) sourceSummary {
	out := sourceSummary{}
	if ctx == nil || ctx.PlatformAPI() == nil {
		out.Unavailable = true
		out.Warnings = append(out.Warnings, "billing platform API unavailable")
		return out
	}
	var raw map[string]any
	err := ctx.PlatformAPI().CallAppResult("billing", "invoices_search", map[string]any{
		"date_from": start,
		"date_to":   end,
		"status":    "open,paid",
	}, &raw)
	if err != nil {
		out.Unavailable = true
		out.Warnings = append(out.Warnings, "billing sync failed: "+err.Error())
		return out
	}
	out.Raw = raw
	out.RevenueCents = sumNested(raw, "total_cents", "subtotal_cents", "amount_cents")
	out.OutputTaxCents = sumNested(raw, "tax_cents", "vat_cents", "output_tax_cents")
	out.Items = countLikelyItems(raw)
	if out.OutputTaxCents == 0 && out.RevenueCents > 0 && profile.VATRegistered {
		out.Warnings = append(out.Warnings, "billing returned revenue without explicit output tax; estimator may infer standard-rate tax")
	}
	return out
}

func syncBills(ctx *sdk.AppCtx, profile Profile, start, end string) sourceSummary {
	out := sourceSummary{}
	if ctx == nil || ctx.PlatformAPI() == nil {
		out.Unavailable = true
		out.Warnings = append(out.Warnings, "bills platform API unavailable")
		return out
	}
	var raw map[string]any
	err := ctx.PlatformAPI().CallAppResult("bills", "bills_search", map[string]any{
		"date_from": start,
		"date_to":   end,
	}, &raw)
	if err != nil {
		out.Unavailable = true
		out.Warnings = append(out.Warnings, "bills sync failed: "+err.Error())
		return out
	}
	out.Raw = raw
	out.ExpensesCents = sumNested(raw, "total_cents", "subtotal_cents", "amount_cents")
	out.InputTaxCents = sumNested(raw, "tax_cents", "vat_cents", "input_tax_cents")
	out.Items = countLikelyItems(raw)
	if out.InputTaxCents == 0 && out.ExpensesCents > 0 && profile.VATRegistered {
		out.Warnings = append(out.Warnings, "bills returned expenses without explicit input tax; estimator may infer standard-rate recoverable tax")
	}
	return out
}

func scanRows(rows *sql.Rows) ([]map[string]any, error) {
	cols, err := rows.Columns()
	if err != nil {
		return nil, err
	}
	var out []map[string]any
	for rows.Next() {
		vals := make([]any, len(cols))
		ptrs := make([]any, len(cols))
		for i := range vals {
			ptrs[i] = &vals[i]
		}
		if err := rows.Scan(ptrs...); err != nil {
			return nil, err
		}
		m := map[string]any{}
		for i, c := range cols {
			switch v := vals[i].(type) {
			case []byte:
				s := string(v)
				if (strings.HasSuffix(c, "_json") || c == "content_json") && json.Valid(v) {
					var decoded any
					_ = json.Unmarshal(v, &decoded)
					m[strings.TrimSuffix(c, "_json")] = decoded
				} else {
					m[c] = s
				}
			default:
				m[c] = v
			}
		}
		out = append(out, m)
	}
	return out, rows.Err()
}

func audit(db *sql.DB, projectID, entityType string, entityID int64, action, message string, metadata map[string]any) {
	if db == nil {
		return
	}
	_, _ = db.Exec(`INSERT INTO tax_audit_log (project_id,entity_type,entity_id,action,message,metadata_json) VALUES (?,?,?,?,?,?)`, projectID, entityType, entityID, action, message, mustJSON(metadata))
}

func projectID(ctx *sdk.AppCtx, args map[string]any) string {
	if v := stringArg(args, "project_id", ""); v != "" {
		return v
	}
	if v := os.Getenv("APTEVA_PROJECT_ID"); v != "" {
		return v
	}
	return "default"
}

func normalizeTaxType(v string) string {
	v = strings.ToLower(strings.TrimSpace(v))
	v = strings.ReplaceAll(v, "-", "_")
	switch v {
	case "iva", "tva", "vat":
		return "vat"
	case "irpf", "income", "income_tax":
		return "income_tax"
	case "is", "corporate", "corporate_tax":
		return "corporate_tax"
	case "social", "social_security", "social_contribution", "social_contributions":
		return "social_contributions"
	}
	return v
}

func defaultAuthority(p Profile, taxType string) string {
	if p.Country == "ES" && taxType == "social_contributions" {
		return "Tesoreria General de la Seguridad Social"
	}
	if p.Country == "ES" {
		return "Agencia Tributaria"
	}
	if p.Country == "FR" {
		return "Direction generale des Finances publiques"
	}
	return "Tax authority"
}

func confidence(warnings []string) string {
	if len(warnings) == 0 {
		return "medium"
	}
	if len(warnings) > 2 {
		return "low"
	}
	return "medium_with_warnings"
}

func monthsBetween(start, end string) int {
	s, err1 := time.Parse("2006-01-02", start)
	e, err2 := time.Parse("2006-01-02", end)
	if err1 != nil || err2 != nil || e.Before(s) {
		return 1
	}
	months := int(e.Year()-s.Year())*12 + int(e.Month()-s.Month()) + 1
	if months < 1 {
		return 1
	}
	return months
}

func yearFromDate(v string) int {
	if len(v) >= 4 {
		if y, err := strconv.Atoi(v[:4]); err == nil {
			return y
		}
	}
	return currentYear()
}

func currentYear() int { return time.Now().Year() }

func addStringFilter(where *[]string, vals *[]any, col, value string) {
	if value == "" {
		return
	}
	*where = append(*where, col+"=?")
	*vals = append(*vals, value)
}

func addIntFilter(where *[]string, vals *[]any, col string, value int64) {
	if value <= 0 {
		return
	}
	*where = append(*where, col+"=?")
	*vals = append(*vals, value)
}

func mustJSON(v any) string {
	if v == nil {
		return "{}"
	}
	b, err := json.Marshal(v)
	if err != nil {
		return "{}"
	}
	return string(b)
}

func parseJSONMap(raw string) map[string]any {
	if raw == "" {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		return map[string]any{}
	}
	return out
}

func mapArg(args map[string]any, key string) map[string]any {
	v, ok := args[key]
	if !ok || v == nil {
		return map[string]any{}
	}
	if m, ok := v.(map[string]any); ok {
		return m
	}
	return map[string]any{}
}

func hasArg(args map[string]any, key string) bool {
	_, ok := args[key]
	return ok
}

func stringArg(args map[string]any, key, def string) string {
	v, ok := args[key]
	if !ok || v == nil {
		return def
	}
	switch x := v.(type) {
	case string:
		return x
	case fmt.Stringer:
		return x.String()
	default:
		return fmt.Sprint(x)
	}
}

func intArg(args map[string]any, key string, def int) int {
	return int(int64Arg(args, key, int64(def)))
}

func int64Arg(args map[string]any, key string, def int64) int64 {
	v, ok := args[key]
	if !ok || v == nil {
		return def
	}
	return int64FromAny(v)
}

func int64FromAny(v any) int64 {
	switch x := v.(type) {
	case int:
		return int64(x)
	case int64:
		return x
	case int32:
		return int64(x)
	case float64:
		return int64(math.Round(x))
	case float32:
		return int64(math.Round(float64(x)))
	case json.Number:
		i, _ := x.Int64()
		return i
	case string:
		i, _ := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		return i
	default:
		return 0
	}
}

func boolArg(args map[string]any, key string, def bool) bool {
	v, ok := args[key]
	if !ok || v == nil {
		return def
	}
	switch x := v.(type) {
	case bool:
		return x
	case string:
		b, err := strconv.ParseBool(x)
		if err == nil {
			return b
		}
	case int:
		return x != 0
	case int64:
		return x != 0
	case float64:
		return x != 0
	}
	return def
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func nullInt(v int64) any {
	if v <= 0 {
		return nil
	}
	return v
}

func cloneMap(in map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range in {
		out[k] = v
	}
	return out
}

func sumNested(v any, keys ...string) int64 {
	total := int64(0)
	switch x := v.(type) {
	case map[string]any:
		for k, val := range x {
			for _, want := range keys {
				if k == want {
					total += int64FromAny(val)
				}
			}
			total += sumNested(val, keys...)
		}
	case []any:
		for _, item := range x {
			total += sumNested(item, keys...)
		}
	}
	return total
}

func countLikelyItems(v any) int {
	switch x := v.(type) {
	case map[string]any:
		for _, key := range []string{"invoices", "bills", "items", "results"} {
			if arr, ok := x[key].([]any); ok {
				return len(arr)
			}
		}
	case []any:
		return len(x)
	}
	return 0
}

func findNestedInt(m map[string]any, path ...string) int64 {
	var cur any = m
	for _, p := range path {
		obj, ok := cur.(map[string]any)
		if !ok {
			return 0
		}
		cur = obj[p]
	}
	return int64FromAny(cur)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": msg})
}

func schemaObject(props map[string]any, required []string) map[string]any {
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func strSchema() map[string]any   { return map[string]any{"type": "string"} }
func intSchema() map[string]any   { return map[string]any{"type": "integer"} }
func boolSchema() map[string]any  { return map[string]any{"type": "boolean"} }
func arraySchema() map[string]any { return map[string]any{"type": "array"} }

func withID(props map[string]any) map[string]any {
	out := map[string]any{"id": intSchema()}
	for k, v := range props {
		out[k] = v
	}
	return out
}

func profileProps() map[string]any {
	return map[string]any{
		"name": strSchema(), "country": strSchema(), "structure": strSchema(), "region": strSchema(),
		"fiscal_year_start": strSchema(), "fiscal_year_end": strSchema(), "vat_registered": boolSchema(),
		"filing_cadence": strSchema(), "accounting_basis": strSchema(), "currency": strSchema(),
		"config": map[string]any{"type": "object"}, "archived": boolSchema(),
	}
}

func ruleFilterProps() map[string]any {
	return map[string]any{"country": strSchema(), "structure": strSchema(), "tax_type": strSchema(), "year": intSchema(), "active": boolSchema()}
}

func periodProps() map[string]any {
	return map[string]any{"profile_id": intSchema(), "tax_type": strSchema(), "period_start": strSchema(), "period_end": strSchema(), "due_date": strSchema(), "metadata": map[string]any{"type": "object"}}
}

func syncProps() map[string]any {
	return map[string]any{"profile_id": intSchema(), "period_start": strSchema(), "period_end": strSchema()}
}

func estimateProps() map[string]any {
	return map[string]any{
		"profile_id": intSchema(), "period_id": intSchema(), "period_start": strSchema(), "period_end": strSchema(),
		"due_date": strSchema(), "revenue_cents": intSchema(), "expenses_cents": intSchema(),
		"output_tax_cents": intSchema(), "input_tax_cents": intSchema(), "taxable_profit_cents": intSchema(),
		"social_contribution_cents": intSchema(), "months": intSchema(), "sync_sources": boolSchema(), "create_obligation": boolSchema(),
	}
}

func obligationProps() map[string]any {
	return map[string]any{"profile_id": intSchema(), "period_id": intSchema(), "tax_type": strSchema(), "title": strSchema(), "amount_cents": intSchema(), "currency": strSchema(), "due_date": strSchema(), "authority": strSchema(), "status": strSchema(), "metadata": map[string]any{"type": "object"}}
}

func paymentProps() map[string]any {
	return map[string]any{"obligation_id": intSchema(), "amount_cents": intSchema(), "currency": strSchema(), "paid_at": strSchema(), "method": strSchema(), "reference": strSchema(), "notes": strSchema(), "bills_bill_id": intSchema(), "bills_payment_id": intSchema(), "metadata": map[string]any{"type": "object"}}
}

func adjustmentProps() map[string]any {
	return map[string]any{"profile_id": intSchema(), "period_id": intSchema(), "tax_type": strSchema(), "kind": strSchema(), "amount_cents": intSchema(), "currency": strSchema(), "reason": strSchema(), "metadata": map[string]any{"type": "object"}}
}
