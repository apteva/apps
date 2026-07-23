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
		{Country: "ES", Structure: "ES_AUTONOMO", TaxType: "vat", Year: 2026, Version: "es-2026-v2", EffectiveFrom: "2026-01-01", SourceURL: "https://sede.agenciatributaria.gob.es/Sede/iva/presentar-declaracion-iva-modelo-303/plazo-presentacion-modelo-303.html", Rules: map[string]any{"vat_standard_bps": 2100, "reduced_bps": []int{1000, 400, 0}, "authority": "Agencia Tributaria"}},
		{Country: "ES", Structure: "ES_AUTONOMO", TaxType: "income_tax", Year: 2026, Version: "es-autonomo-2026-v2", EffectiveFrom: "2026-01-01", SourceURL: "https://sede.agenciatributaria.gob.es/", Rules: map[string]any{"income_tax_bps": 2000, "authority": "Agencia Tributaria", "warning": "Planning estimate only. Actual IRPF is progressive and depends on deductions and personal circumstances."}},
		{Country: "ES", Structure: "ES_AUTONOMO", TaxType: "social_contributions", Year: 2026, Version: "es-autonomo-social-2026-v2", EffectiveFrom: "2026-01-01", SourceURL: "https://www.seg-social.es/", Rules: map[string]any{"monthly_cents": 0, "authority": "Tesoreria General de la Seguridad Social", "warning": "Set the actual monthly contribution from the profile or estimate input; the contribution depends on the applicable income band and elections."}},
		{Country: "ES", Structure: "ES_SL", TaxType: "vat", Year: 2026, Version: "es-sl-vat-2026-v1", EffectiveFrom: "2026-01-01", SourceURL: "https://sede.agenciatributaria.gob.es/", Rules: map[string]any{"vat_standard_bps": 2100, "reduced_bps": []int{1000, 400, 0}, "authority": "Agencia Tributaria"}},
		{Country: "ES", Structure: "ES_SL", TaxType: "corporate_tax", Year: 2026, Version: "es-sl-is-2026-v1", EffectiveFrom: "2026-01-01", SourceURL: "https://sede.agenciatributaria.gob.es/", Rules: map[string]any{"corporate_tax_bps": 2500, "authority": "Agencia Tributaria", "warning": "General-rate estimate; reduced rates and special regimes require profile config/adjustments."}},
		{Country: "FR", Structure: "FR_SAS", TaxType: "vat", Year: 2026, Version: "fr-vat-2026-v1", EffectiveFrom: "2026-01-01", SourceURL: "https://www.impots.gouv.fr/", Rules: map[string]any{"vat_standard_bps": 2000, "reduced_bps": []int{1000, 550, 210}, "authority": "Direction generale des Finances publiques"}},
		{Country: "FR", Structure: "FR_SAS", TaxType: "corporate_tax", Year: 2026, Version: "fr-sas-is-2026-v1", EffectiveFrom: "2026-01-01", SourceURL: "https://www.impots.gouv.fr/", Rules: map[string]any{"corporate_tax_bps": 2500, "authority": "Direction generale des Finances publiques"}},
		{Country: "FR", Structure: "FR_SASU", TaxType: "vat", Year: 2026, Version: "fr-vat-2026-v1", EffectiveFrom: "2026-01-01", SourceURL: "https://www.impots.gouv.fr/", Rules: map[string]any{"vat_standard_bps": 2000, "reduced_bps": []int{1000, 550, 210}, "authority": "Direction generale des Finances publiques"}},
		{Country: "FR", Structure: "FR_SASU", TaxType: "corporate_tax", Year: 2026, Version: "fr-sasu-is-2026-v1", EffectiveFrom: "2026-01-01", SourceURL: "https://www.impots.gouv.fr/", Rules: map[string]any{"corporate_tax_bps": 2500, "authority": "Direction generale des Finances publiques"}},
		{Country: "FR", Structure: "FR_SARL", TaxType: "vat", Year: 2026, Version: "fr-vat-2026-v1", EffectiveFrom: "2026-01-01", SourceURL: "https://www.impots.gouv.fr/", Rules: map[string]any{"vat_standard_bps": 2000, "reduced_bps": []int{1000, 550, 210}, "authority": "Direction generale des Finances publiques"}},
		{Country: "FR", Structure: "FR_SARL", TaxType: "corporate_tax", Year: 2026, Version: "fr-sarl-is-2026-v1", EffectiveFrom: "2026-01-01", SourceURL: "https://www.impots.gouv.fr/", Rules: map[string]any{"corporate_tax_bps": 2500, "authority": "Direction generale des Finances publiques"}},
		{Country: "FR", Structure: "FR_EURL", TaxType: "vat", Year: 2026, Version: "fr-eurl-vat-2026-v1", EffectiveFrom: "2026-01-01", SourceURL: "https://www.impots.gouv.fr/professionnel/tva", Rules: map[string]any{"vat_standard_bps": 2000, "reduced_bps": []int{1000, 550, 210}, "authority": "Direction generale des Finances publiques"}},
		{Country: "FR", Structure: "FR_EURL", TaxType: "income_tax", Year: 2026, Version: "fr-eurl-ir-2026-v1", EffectiveFrom: "2026-01-01", SourceURL: "https://www.impots.gouv.fr/", Rules: map[string]any{"income_tax_bps": 2500, "authority": "Direction generale des Finances publiques", "warning": "Flat estimate; actual French income tax is progressive and household-dependent."}},
		{Country: "FR", Structure: "FR_EURL", TaxType: "corporate_tax", Year: 2026, Version: "fr-eurl-is-2026-v1", EffectiveFrom: "2026-01-01", SourceURL: "https://www.impots.gouv.fr/", Rules: map[string]any{"corporate_tax_bps": 2500, "authority": "Direction generale des Finances publiques", "warning": "Used only when the profile config selects the corporate-tax regime."}},
		{Country: "FR", Structure: "FR_EURL", TaxType: "social_contributions", Year: 2026, Version: "fr-eurl-social-2026-v1", EffectiveFrom: "2026-01-01", SourceURL: "https://www.urssaf.fr/", Rules: map[string]any{"monthly_cents": 0, "authority": "URSSAF", "warning": "Set the actual monthly contribution. Manager contributions depend on remuneration, profit, ownership, and social status."}},
	}
	for _, r := range rules {
		if _, err := db.Exec(`INSERT INTO tax_rules (country,structure,tax_type,year,version,effective_from,source_url,rules_json,active)
			VALUES (?,?,?,?,?,?,?,?,1)
			ON CONFLICT(country,structure,tax_type,year,version)
			DO UPDATE SET effective_from=excluded.effective_from, source_url=excluded.source_url, rules_json=excluded.rules_json, active=1`,
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
	TaxType       string
	PeriodStart   string
	PeriodEnd     string
	DueDate       string
	DeadlineState string
	SourceURL     string
}

func generatePeriodsForProfile(db *sql.DB, profile Profile, year int) ([]map[string]any, error) {
	inferred := inferPeriods(profile, year)
	out := []map[string]any{}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`UPDATE tax_periods
		SET status='superseded', updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND profile_id=? AND status='open'
		  AND period_start>=? AND period_start<?
		  AND json_extract(metadata_json,'$.generated')=1`,
		profile.ProjectID, profile.ID, fmt.Sprintf("%04d-01-01", year), fmt.Sprintf("%04d-01-01", year+1)); err != nil {
		return nil, err
	}
	for _, p := range inferred {
		if _, err := tx.Exec(`INSERT INTO tax_periods
			(project_id,profile_id,tax_type,period_start,period_end,due_date,metadata_json)
			VALUES (?,?,?,?,?,?,?)
			ON CONFLICT(project_id, profile_id, tax_type, period_start, period_end)
			DO UPDATE SET due_date=excluded.due_date,
				metadata_json=excluded.metadata_json,
				status=CASE WHEN tax_periods.status='superseded' THEN 'open' ELSE tax_periods.status END,
				updated_at=CURRENT_TIMESTAMP`,
			profile.ProjectID, profile.ID, p.TaxType, p.PeriodStart, p.PeriodEnd, p.DueDate,
			mustJSON(map[string]any{
				"generated":       true,
				"source":          "tax_profile",
				"deadline_state":  p.DeadlineState,
				"deadline_source": p.SourceURL,
			})); err != nil {
			return nil, err
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	rows, err := db.Query(`SELECT id,project_id,profile_id,tax_type,period_start,period_end,due_date,status,filed_at,filing_ref,metadata_json
		FROM tax_periods
		WHERE project_id=? AND profile_id=? AND period_start>=? AND period_start<?
		ORDER BY period_start, tax_type`, profile.ProjectID, profile.ID, fmt.Sprintf("%04d-01-01", year), fmt.Sprintf("%04d-01-01", year+1))
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
			out = append(out, annualPeriodForProfile(profile, year, taxType))
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
	for i := range out {
		if configured := configuredDueDate(profile, out[i]); configured != "" {
			out[i].DueDate = configured
			out[i].DeadlineState = "profile_configured"
		}
	}
	return out
}

func inferredTaxTypes(profile Profile) []string {
	out := []string{}
	if profile.VATRegistered {
		out = append(out, "vat")
	}
	switch profile.Structure {
	case "ES_AUTONOMO":
		return append(out, "income_tax", "social_contributions")
	case "ES_SL":
		return append(out, "corporate_tax")
	case "FR_SAS", "FR_SASU", "FR_SARL":
		return append(out, "corporate_tax")
	case "FR_EURL":
		if strings.EqualFold(stringFromAny(profile.Config["tax_regime"]), "corporate_tax") {
			return append(out, "corporate_tax", "social_contributions")
		}
		return append(out, "income_tax", "social_contributions")
	default:
		return out
	}
}

func quarterlyPeriods(year int, taxType, country string) []inferredPeriod {
	quarters := [][2]string{{"01-01", "03-31"}, {"04-01", "06-30"}, {"07-01", "09-30"}, {"10-01", "12-31"}}
	out := []inferredPeriod{}
	for i, q := range quarters {
		out = append(out, inferredPeriod{
			TaxType:       taxType,
			PeriodStart:   fmt.Sprintf("%04d-%s", year, q[0]),
			PeriodEnd:     fmt.Sprintf("%04d-%s", year, q[1]),
			DueDate:       dueDateFor(country, taxType, year, i+1, "quarterly"),
			DeadlineState: deadlineState(country),
			SourceURL:     deadlineSource(country, taxType),
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
			TaxType:       taxType,
			PeriodStart:   start.Format("2006-01-02"),
			PeriodEnd:     end.Format("2006-01-02"),
			DueDate:       dueDateFor(country, taxType, year, month, "monthly"),
			DeadlineState: deadlineState(country),
			SourceURL:     deadlineSource(country, taxType),
		})
	}
	return out
}

func annualPeriod(year int, taxType, country string) inferredPeriod {
	return inferredPeriod{
		TaxType:       taxType,
		PeriodStart:   fmt.Sprintf("%04d-01-01", year),
		PeriodEnd:     fmt.Sprintf("%04d-12-31", year),
		DueDate:       dueDateFor(country, taxType, year, 0, "annual"),
		DeadlineState: deadlineState(country),
		SourceURL:     deadlineSource(country, taxType),
	}
}

func annualPeriodForProfile(profile Profile, year int, taxType string) inferredPeriod {
	startSuffix := normalizeMonthDay(profile.FiscalYearStart, "01-01")
	endSuffix := normalizeMonthDay(profile.FiscalYearEnd, "12-31")
	start, _ := time.Parse("2006-01-02", fmt.Sprintf("%04d-%s", year, startSuffix))
	endYear := year
	if endSuffix < startSuffix {
		endYear++
	}
	end, _ := time.Parse("2006-01-02", fmt.Sprintf("%04d-%s", endYear, endSuffix))
	due := ""
	state := deadlineState(profile.Country)
	if profile.Country == "ES" && taxType == "corporate_tax" {
		sixMonthsAfter := addMonthsClamped(end, 6)
		due = nextWeekday(sixMonthsAfter.AddDate(0, 0, 25)).Format("2006-01-02")
	}
	return inferredPeriod{
		TaxType:       taxType,
		PeriodStart:   start.Format("2006-01-02"),
		PeriodEnd:     end.Format("2006-01-02"),
		DueDate:       due,
		DeadlineState: state,
		SourceURL:     deadlineSource(profile.Country, taxType),
	}
}

func normalizeMonthDay(v, fallback string) string {
	v = strings.TrimSpace(v)
	if _, err := time.Parse("01-02", v); err != nil {
		return fallback
	}
	return v
}

func configuredDueDate(profile Profile, period inferredPeriod) string {
	raw, ok := profile.Config["due_dates"].(map[string]any)
	if !ok {
		return ""
	}
	keys := []string{
		period.TaxType + ":" + period.PeriodEnd,
		period.TaxType,
	}
	for _, key := range keys {
		if v := stringFromAny(raw[key]); validISODate(v) {
			return v
		}
	}
	return ""
}

func validISODate(v string) bool {
	_, err := time.Parse("2006-01-02", v)
	return err == nil
}

func addMonthsClamped(date time.Time, months int) time.Time {
	targetMonth := time.Date(date.Year(), date.Month()+time.Month(months), 1, 0, 0, 0, 0, time.UTC)
	lastDay := targetMonth.AddDate(0, 1, -1).Day()
	day := date.Day()
	if day > lastDay {
		day = lastDay
	}
	return time.Date(targetMonth.Year(), targetMonth.Month(), day, 0, 0, 0, 0, time.UTC)
}

func nextWeekday(date time.Time) time.Time {
	for date.Weekday() == time.Saturday || date.Weekday() == time.Sunday {
		date = date.AddDate(0, 0, 1)
	}
	return date
}

func dueDateFor(country, taxType string, year, ordinal int, cadence string) string {
	switch country {
	case "ES":
		if taxType == "corporate_tax" {
			return nextWeekday(time.Date(year+1, time.July, 25, 0, 0, 0, 0, time.UTC)).Format("2006-01-02")
		}
		if taxType == "social_contributions" && cadence == "monthly" {
			return time.Date(year, time.Month(ordinal)+1, 0, 0, 0, 0, 0, time.UTC).Format("2006-01-02")
		}
		if cadence == "monthly" {
			next := time.Date(year, time.Month(ordinal)+1, 1, 0, 0, 0, 0, time.UTC)
			last := next.AddDate(0, 1, -1).Day()
			day := 30
			if last < day {
				day = last
			}
			return nextWeekday(time.Date(next.Year(), next.Month(), day, 0, 0, 0, 0, time.UTC)).Format("2006-01-02")
		}
		if cadence == "quarterly" {
			due := []time.Time{
				time.Date(year, time.April, 20, 0, 0, 0, 0, time.UTC),
				time.Date(year, time.July, 20, 0, 0, 0, 0, time.UTC),
				time.Date(year, time.October, 20, 0, 0, 0, 0, time.UTC),
				time.Date(year+1, time.January, 30, 0, 0, 0, 0, time.UTC),
			}
			if ordinal >= 1 && ordinal <= len(due) {
				return nextWeekday(due[ordinal-1]).Format("2006-01-02")
			}
		}
	case "FR":
		// French filing dates vary by regime and the date assigned in the
		// professional account. A false exact date is worse than an explicit
		// confirmation requirement.
		return ""
	}
	return ""
}

func deadlineState(country string) string {
	if country == "ES" {
		return "statutory_estimate"
	}
	return "requires_confirmation"
}

func deadlineSource(country, taxType string) string {
	if country == "ES" && taxType == "social_contributions" {
		return "https://www.seg-social.es/"
	}
	if country == "ES" {
		return "https://sede.agenciatributaria.gob.es/Sede/ayuda/calendario-contribuyente/calendario-contribuyente-2026.html"
	}
	if country == "FR" {
		return "https://www.impots.gouv.fr/professionnel/calendrier-fiscal"
	}
	return ""
}

func calculateOutputs(taxType string, rule Rule, inputs map[string]any, start, end string) map[string]any {
	revenue := int64FromAny(inputs["revenue_cents"])
	expenses := int64FromAny(inputs["expenses_cents"])
	outputTax := int64FromAny(inputs["output_tax_cents"])
	inputTax := int64FromAny(inputs["input_tax_cents"])
	profit, profitProvided := int64MapValue(inputs, "taxable_profit_cents")
	if !profitProvided {
		profit = revenue - expenses
	}
	if profit < 0 {
		profit = 0
	}
	out := map[string]any{"tax_type": taxType, "period_start": start, "period_end": end}
	switch taxType {
	case "vat":
		rate := int64FromAny(rule.Rules["vat_standard_bps"])
		_, outputProvided := inputs["output_tax_cents"]
		_, inputProvided := inputs["input_tax_cents"]
		if !outputProvided && revenue > 0 {
			outputTax = revenue * rate / 10000
		}
		if !inputProvided && expenses > 0 {
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
		monthly, monthlyProvided := int64MapValue(inputs, "social_contribution_cents")
		if !monthlyProvided {
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
	direction := "payable"
	if amount < 0 {
		direction = "receivable"
		amount = -amount
		title = fmt.Sprintf("%s %s refund estimate", profile.Name, strings.ReplaceAll(taxType, "_", " "))
	}
	existing := int64(0)
	if periodID > 0 {
		_ = db.QueryRow(`SELECT id FROM tax_obligations WHERE project_id=? AND profile_id=? AND period_id=? AND tax_type=? AND status IN ('estimated','draft') ORDER BY id DESC LIMIT 1`, profile.ProjectID, profile.ID, periodID, taxType).Scan(&existing)
	}
	if existing > 0 {
		_, err := db.Exec(`UPDATE tax_obligations SET calculation_id=?, title=?, amount_cents=?, currency=?, due_date=?, authority=?, direction=?, updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`,
			calcID, title, amount, profile.Currency, dueDate, defaultAuthority(profile, taxType), direction, profile.ProjectID, existing)
		if err != nil {
			return nil, err
		}
		o, err := getObligation(db, profile.ProjectID, existing)
		return &o, err
	}
	return insertObligation(db, profile, periodID, calcID, taxType, title, amount, profile.Currency, dueDate, defaultAuthority(profile, taxType), direction, "estimated", nil)
}

func insertObligation(db *sql.DB, profile Profile, periodID, calcID int64, taxType, title string, amount int64, currency, dueDate, authority, direction, status string, metadata map[string]any) (*Obligation, error) {
	if title == "" {
		title = strings.ReplaceAll(taxType, "_", " ") + " obligation"
	}
	if currency == "" {
		currency = profile.Currency
	}
	if status == "" {
		status = "estimated"
	}
	if direction == "" {
		direction = "payable"
	}
	if err := validateObligationInput(profile, taxType, direction, amount, currency, status); err != nil {
		return nil, err
	}
	if periodID > 0 {
		period, err := getPeriod(db, profile.ProjectID, periodID)
		if err != nil {
			return nil, err
		}
		if int64FromAny(period["profile_id"]) != profile.ID || normalizeTaxType(stringFromAny(period["tax_type"])) != taxType {
			return nil, errors.New("period does not belong to this profile and tax type")
		}
	}
	res, err := db.Exec(`INSERT INTO tax_obligations (project_id,profile_id,period_id,calculation_id,tax_type,authority,title,amount_cents,currency,due_date,direction,status,metadata_json) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?)`,
		profile.ProjectID, profile.ID, nullInt(periodID), nullInt(calcID), taxType, authority, title, amount, currency, dueDate, direction, status, mustJSON(metadata))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	audit(db, profile.ProjectID, "tax_obligation", id, "created", title, nil)
	o, err := getObligation(db, profile.ProjectID, id)
	return &o, err
}

func getObligation(db *sql.DB, projectID string, id int64) (Obligation, error) {
	row := db.QueryRow(`SELECT id,project_id,profile_id,COALESCE(period_id,0),COALESCE(calculation_id,0),tax_type,authority,title,amount_cents,currency,due_date,direction,status,filed_at,filing_ref,waived_reason,metadata_json FROM tax_obligations WHERE project_id=? AND id=?`, projectID, id)
	return scanObligation(row)
}

type obligationScanner interface {
	Scan(dest ...any) error
}

func scanObligation(row obligationScanner) (Obligation, error) {
	var o Obligation
	var meta string
	if err := row.Scan(&o.ID, &o.ProjectID, &o.ProfileID, &o.PeriodID, &o.CalculationID, &o.TaxType, &o.Authority, &o.Title, &o.AmountCents, &o.Currency, &o.DueDate, &o.Direction, &o.Status, &o.FiledAt, &o.FilingRef, &o.WaivedReason, &meta); err != nil {
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
	statuses := []string{"open", "paid"}
	if strings.EqualFold(profile.AccountingBasis, "cash") {
		statuses = []string{"paid"}
	}
	rawPages := map[string]any{}
	for _, status := range statuses {
		var page map[string]any
		err := ctx.PlatformAPI().CallAppResult("billing", "invoices_search", map[string]any{
			"_project_id": profile.ProjectID,
			"since":       start,
			"until":       exclusiveDateEnd(end),
			"status":      status,
			"currency":    profile.Currency,
			"limit":       200,
		}, &page)
		if err != nil {
			out.Unavailable = true
			out.Warnings = append(out.Warnings, "billing sync failed: "+err.Error())
			return out
		}
		rows := objectRows(page["invoices"])
		for _, row := range rows {
			out.RevenueCents += int64FromAny(row["subtotal_cents"])
			out.OutputTaxCents += int64FromAny(row["tax_cents"])
		}
		out.Items += len(rows)
		if len(rows) == 200 {
			out.Warnings = append(out.Warnings, "billing returned the 200-row maximum for status "+status+"; narrow the period before relying on this estimate")
		}
		rawPages[status] = page
	}
	out.Raw = rawPages
	if out.OutputTaxCents == 0 && out.RevenueCents > 0 && profile.VATRegistered {
		out.Warnings = append(out.Warnings, "billing returned revenue with zero recorded output tax; zero is preserved and should be reviewed for exempt or zero-rated sales")
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
	statuses := []string{"received", "approved", "scheduled", "paid"}
	if strings.EqualFold(profile.AccountingBasis, "cash") {
		statuses = []string{"paid"}
	}
	rawPages := map[string]any{}
	for _, status := range statuses {
		offset := 0
		statusPages := []any{}
		for {
			var page map[string]any
			err := ctx.PlatformAPI().CallAppResult("bills", "bills_search", map[string]any{
				"_project_id": profile.ProjectID,
				"since":       start,
				"until":       exclusiveDateEnd(end),
				"status":      status,
				"currency":    profile.Currency,
				"limit":       200,
				"offset":      offset,
			}, &page)
			if err != nil {
				out.Unavailable = true
				out.Warnings = append(out.Warnings, "bills sync failed: "+err.Error())
				return out
			}
			rows := objectRows(page["bills"])
			for _, row := range rows {
				out.ExpensesCents += int64FromAny(row["subtotal_cents"])
				out.InputTaxCents += int64FromAny(row["tax_cents"])
			}
			out.Items += len(rows)
			statusPages = append(statusPages, page)
			if !boolFromAny(page["has_more"]) || len(rows) == 0 {
				break
			}
			next := intFromAny(page["next_offset"])
			if next <= offset {
				next = offset + len(rows)
			}
			offset = next
		}
		rawPages[status] = statusPages
	}
	out.Raw = rawPages
	if out.InputTaxCents == 0 && out.ExpensesCents > 0 && profile.VATRegistered {
		out.Warnings = append(out.Warnings, "bills returned expenses with zero recorded input tax; zero is preserved and should be reviewed for recoverability")
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
	if ctx != nil {
		if v := strings.TrimSpace(ctx.CurrentProject()); v != "" {
			return v
		}
	}
	if v := strings.TrimSpace(stringArg(args, "_project_id", "")); v != "" {
		return v
	}
	if v := os.Getenv("APTEVA_PROJECT_ID"); v != "" {
		return v
	}
	return ""
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
	if p.Country == "FR" && taxType == "social_contributions" {
		return "URSSAF"
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

func objectRows(v any) []map[string]any {
	raw, ok := v.([]any)
	if !ok {
		return nil
	}
	out := make([]map[string]any, 0, len(raw))
	for _, item := range raw {
		if row, ok := item.(map[string]any); ok {
			out = append(out, row)
		}
	}
	return out
}

func exclusiveDateEnd(end string) string {
	parsed, err := time.Parse("2006-01-02", end)
	if err != nil {
		return end
	}
	return parsed.AddDate(0, 0, 1).Format("2006-01-02")
}

func int64MapValue(m map[string]any, key string) (int64, bool) {
	v, ok := m[key]
	if !ok {
		return 0, false
	}
	return int64FromAny(v), true
}

func stringFromAny(v any) string {
	if v == nil {
		return ""
	}
	if s, ok := v.(string); ok {
		return s
	}
	return fmt.Sprint(v)
}

func boolFromAny(v any) bool {
	switch x := v.(type) {
	case bool:
		return x
	case string:
		b, _ := strconv.ParseBool(x)
		return b
	case float64:
		return x != 0
	case int:
		return x != 0
	case int64:
		return x != 0
	}
	return false
}

func intFromAny(v any) int {
	return int(int64FromAny(v))
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
		"config": map[string]any{"type": "object"}, "archived": boolSchema(), "auto_open_periods": boolSchema(),
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
	return map[string]any{"profile_id": intSchema(), "period_id": intSchema(), "tax_type": strSchema(), "title": strSchema(), "amount_cents": intSchema(), "currency": strSchema(), "due_date": strSchema(), "authority": strSchema(), "direction": strSchema(), "status": strSchema(), "metadata": map[string]any{"type": "object"}}
}

func paymentProps() map[string]any {
	return map[string]any{"obligation_id": intSchema(), "amount_cents": intSchema(), "currency": strSchema(), "paid_at": strSchema(), "method": strSchema(), "reference": strSchema(), "notes": strSchema(), "metadata": map[string]any{"type": "object"}}
}

func adjustmentProps() map[string]any {
	return map[string]any{"profile_id": intSchema(), "period_id": intSchema(), "tax_type": strSchema(), "kind": strSchema(), "amount_cents": intSchema(), "currency": strSchema(), "reason": strSchema(), "metadata": map[string]any{"type": "object"}}
}
