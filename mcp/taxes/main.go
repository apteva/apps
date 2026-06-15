package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: taxes
display_name: Taxes
version: 1.0.1
description: Business tax profiles, estimates, statutory obligations, social contributions, filings, and payment tracking.
author: Apteva
scopes: [project, global]
requires:
  permissions:
    - db.write.app
    - platform.apps.call
  apps:
    - name: billing
      optional: true
    - name: bills
      optional: true
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: tax_profiles_create, description: "Create a business tax profile." }
    - { name: tax_profiles_update, description: "Update a business tax profile." }
    - { name: tax_profiles_get, description: "Fetch one business tax profile." }
    - { name: tax_profiles_list, description: "List business tax profiles." }
    - { name: tax_rules_list, description: "List active tax rule versions." }
    - { name: tax_rules_get, description: "Fetch one tax rule." }
    - { name: tax_periods_open, description: "Open a filing/calculation period." }
    - { name: tax_periods_list, description: "List tax periods." }
    - { name: tax_periods_get, description: "Fetch one tax period." }
    - { name: tax_periods_close, description: "Close a tax period." }
    - { name: tax_estimate_vat, description: "Estimate VAT/IVA/TVA payable or refundable." }
    - { name: tax_estimate_income_tax, description: "Estimate personal/business income tax." }
    - { name: tax_estimate_corporate_tax, description: "Estimate corporate income tax." }
    - { name: tax_estimate_social_contributions, description: "Estimate statutory social contributions." }
    - { name: tax_estimate_all, description: "Run all relevant estimates for a profile and period." }
    - { name: tax_obligations_create, description: "Create a tax obligation." }
    - { name: tax_obligations_update, description: "Update a tax obligation." }
    - { name: tax_obligations_list, description: "List tax obligations." }
    - { name: tax_obligations_get, description: "Fetch one tax obligation." }
    - { name: tax_obligations_mark_filed, description: "Mark a tax obligation as filed." }
    - { name: tax_obligations_mark_waived, description: "Mark a tax obligation as waived." }
    - { name: tax_payments_record, description: "Record a tax payment." }
    - { name: tax_payments_list, description: "List tax payments." }
    - { name: tax_payments_link_bill, description: "Link a bills bill/payment to a tax obligation." }
    - { name: tax_payments_create_bill, description: "Create a payable bill for a tax obligation." }
    - { name: tax_adjustments_create, description: "Create a manual tax adjustment." }
    - { name: tax_adjustments_list, description: "List tax adjustments." }
    - { name: tax_adjustments_delete, description: "Delete a tax adjustment." }
    - { name: tax_report_generate, description: "Generate an auditable tax report." }
    - { name: tax_documents_list, description: "List tax documents." }
    - { name: tax_documents_get, description: "Fetch one tax document." }
    - { name: tax_sync_from_billing, description: "Sync source summary from billing." }
    - { name: tax_sync_from_bills, description: "Sync source summary from bills." }
    - { name: tax_sync_all, description: "Sync source summaries from billing and bills." }
  ui_panels:
    - slot: project.page
      label: Taxes
      icon: receipt
      entry: /ui/TaxesPanel.mjs
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/taxes
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/taxes.db
  migrations: migrations/
upgrade_policy: auto-patch
`

var globalCtx *sdk.AppCtx

type App struct{}

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("taxes requires a db block")
	}
	globalCtx = ctx
	if err := seedTaxRules(ctx.AppDB()); err != nil {
		return err
	}
	ctx.Logger().Info("taxes mounted", "version", "1.0.1", "scope_project_id", os.Getenv("APTEVA_PROJECT_ID"))
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/tools/", Handler: a.handleToolHTTP},
		{Pattern: "/profiles", Handler: a.handleProfiles},
		{Pattern: "/rules", Handler: a.handleRules},
		{Pattern: "/periods", Handler: a.handlePeriods},
		{Pattern: "/obligations", Handler: a.handleObligations},
		{Pattern: "/documents", Handler: a.handleDocuments},
	}
}

func (a *App) handleToolHTTP(w http.ResponseWriter, r *http.Request) {
	if globalCtx == nil {
		httpErr(w, http.StatusServiceUnavailable, "app not mounted")
		return
	}
	name := strings.Trim(strings.TrimPrefix(r.URL.Path, "/tools/"), "/")
	if name == "" {
		if r.Method != http.MethodGet {
			httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		tools := []map[string]any{}
		for _, tool := range a.MCPTools() {
			tools = append(tools, map[string]any{
				"name":         tool.Name,
				"description":  tool.Description,
				"input_schema": tool.InputSchema,
			})
		}
		writeJSON(w, map[string]any{"tools": tools})
		return
	}
	if r.Method != http.MethodPost {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	args := map[string]any{}
	if r.Body != nil {
		if err := json.NewDecoder(r.Body).Decode(&args); err != nil && err.Error() != "EOF" {
			httpErr(w, http.StatusBadRequest, "invalid json body: "+err.Error())
			return
		}
	}
	for k, vals := range r.URL.Query() {
		if len(vals) > 0 {
			args[k] = vals[0]
		}
	}
	for _, tool := range a.MCPTools() {
		if tool.Name != name {
			continue
		}
		out, err := tool.Handler(globalCtx, args)
		if err != nil {
			httpErr(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, out)
		return
	}
	httpErr(w, http.StatusNotFound, "unknown tool: "+name)
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{Name: "tax_profiles_create", Description: "Create a business tax profile. Args: name, country, structure, region?, vat_registered?, filing_cadence?, accounting_basis?, fiscal_year_start?, fiscal_year_end?, currency?, config?.", InputSchema: schemaObject(profileProps(), []string{"name", "country", "structure"}), Handler: a.toolProfilesCreate},
		{Name: "tax_profiles_update", Description: "Update a business tax profile. Args: id plus any profile field.", InputSchema: schemaObject(withID(profileProps()), []string{"id"}), Handler: a.toolProfilesUpdate},
		{Name: "tax_profiles_get", Description: "Fetch one tax profile by id.", InputSchema: schemaObject(map[string]any{"id": intSchema()}, []string{"id"}), Handler: a.toolProfilesGet},
		{Name: "tax_profiles_list", Description: "List tax profiles. Args: archived? (default false), country?, structure?.", InputSchema: schemaObject(map[string]any{"archived": boolSchema(), "country": strSchema(), "structure": strSchema()}, nil), Handler: a.toolProfilesList},
		{Name: "tax_rules_list", Description: "List tax rules. Args: country?, structure?, tax_type?, year?, active?.", InputSchema: schemaObject(ruleFilterProps(), nil), Handler: a.toolRulesList},
		{Name: "tax_rules_get", Description: "Fetch a tax rule by id, or by country+structure+tax_type+year.", InputSchema: schemaObject(withID(ruleFilterProps()), nil), Handler: a.toolRulesGet},
		{Name: "tax_periods_open", Description: "Open a tax period. Args: profile_id, tax_type, period_start, period_end, due_date?, metadata?.", InputSchema: schemaObject(periodProps(), []string{"profile_id", "tax_type", "period_start", "period_end"}), Handler: a.toolPeriodsOpen},
		{Name: "tax_periods_list", Description: "List tax periods. Args: profile_id?, tax_type?, status?, period_start?, period_end?.", InputSchema: schemaObject(map[string]any{"profile_id": intSchema(), "tax_type": strSchema(), "status": strSchema(), "period_start": strSchema(), "period_end": strSchema()}, nil), Handler: a.toolPeriodsList},
		{Name: "tax_periods_get", Description: "Fetch one tax period by id.", InputSchema: schemaObject(map[string]any{"id": intSchema()}, []string{"id"}), Handler: a.toolPeriodsGet},
		{Name: "tax_periods_close", Description: "Close a period. Args: id, status? (default closed), filed_at?, filing_ref?.", InputSchema: schemaObject(map[string]any{"id": intSchema(), "status": strSchema(), "filed_at": strSchema(), "filing_ref": strSchema()}, []string{"id"}), Handler: a.toolPeriodsClose},
		{Name: "tax_estimate_vat", Description: "Estimate VAT/IVA/TVA. Args: profile_id, period_start, period_end, period_id?, output_tax_cents?, input_tax_cents?, revenue_cents?, expenses_cents?, create_obligation?.", InputSchema: schemaObject(estimateProps(), []string{"profile_id", "period_start", "period_end"}), Handler: a.toolEstimateVAT},
		{Name: "tax_estimate_income_tax", Description: "Estimate income tax. Args: profile_id, period_start, period_end, revenue_cents?, expenses_cents?, taxable_profit_cents?, create_obligation?.", InputSchema: schemaObject(estimateProps(), []string{"profile_id", "period_start", "period_end"}), Handler: a.toolEstimateIncome},
		{Name: "tax_estimate_corporate_tax", Description: "Estimate corporate tax. Args: profile_id, period_start, period_end, revenue_cents?, expenses_cents?, taxable_profit_cents?, create_obligation?.", InputSchema: schemaObject(estimateProps(), []string{"profile_id", "period_start", "period_end"}), Handler: a.toolEstimateCorporate},
		{Name: "tax_estimate_social_contributions", Description: "Estimate social contributions. Args: profile_id, period_start, period_end, months?, social_contribution_cents?, create_obligation?.", InputSchema: schemaObject(estimateProps(), []string{"profile_id", "period_start", "period_end"}), Handler: a.toolEstimateSocial},
		{Name: "tax_estimate_all", Description: "Run the relevant estimates for the profile structure. Args: profile_id, period_start, period_end, period_id?, create_obligation?.", InputSchema: schemaObject(estimateProps(), []string{"profile_id", "period_start", "period_end"}), Handler: a.toolEstimateAll},
		{Name: "tax_obligations_create", Description: "Create a tax/statutory obligation. Args: profile_id, tax_type, title, amount_cents, currency?, due_date?, authority?, period_id?, metadata?.", InputSchema: schemaObject(obligationProps(), []string{"profile_id", "tax_type", "title", "amount_cents"}), Handler: a.toolObligationsCreate},
		{Name: "tax_obligations_update", Description: "Update an obligation. Args: id plus amount_cents?, status?, due_date?, title?, authority?, metadata?.", InputSchema: schemaObject(withID(obligationProps()), []string{"id"}), Handler: a.toolObligationsUpdate},
		{Name: "tax_obligations_list", Description: "List obligations. Args: profile_id?, period_id?, tax_type?, status?, due_before?, authority?.", InputSchema: schemaObject(map[string]any{"profile_id": intSchema(), "period_id": intSchema(), "tax_type": strSchema(), "status": strSchema(), "due_before": strSchema(), "authority": strSchema()}, nil), Handler: a.toolObligationsList},
		{Name: "tax_obligations_get", Description: "Fetch one obligation by id.", InputSchema: schemaObject(map[string]any{"id": intSchema()}, []string{"id"}), Handler: a.toolObligationsGet},
		{Name: "tax_obligations_mark_filed", Description: "Mark an obligation filed. Args: id, filed_at?, filing_ref?.", InputSchema: schemaObject(map[string]any{"id": intSchema(), "filed_at": strSchema(), "filing_ref": strSchema()}, []string{"id"}), Handler: a.toolObligationsMarkFiled},
		{Name: "tax_obligations_mark_waived", Description: "Mark an obligation waived. Args: id, reason.", InputSchema: schemaObject(map[string]any{"id": intSchema(), "reason": strSchema()}, []string{"id", "reason"}), Handler: a.toolObligationsMarkWaived},
		{Name: "tax_payments_record", Description: "Record a tax payment. Args: obligation_id, amount_cents, paid_at?, method?, reference?, notes?, bills_bill_id?, bills_payment_id?.", InputSchema: schemaObject(paymentProps(), []string{"obligation_id", "amount_cents"}), Handler: a.toolPaymentsRecord},
		{Name: "tax_payments_list", Description: "List tax payments. Args: obligation_id?, profile_id?.", InputSchema: schemaObject(map[string]any{"obligation_id": intSchema(), "profile_id": intSchema()}, nil), Handler: a.toolPaymentsList},
		{Name: "tax_payments_link_bill", Description: "Link an existing bills bill/payment to a tax obligation. Args: obligation_id, bills_bill_id?, bills_payment_id?, amount_cents?, paid_at?.", InputSchema: schemaObject(map[string]any{"obligation_id": intSchema(), "bills_bill_id": intSchema(), "bills_payment_id": intSchema(), "amount_cents": intSchema(), "paid_at": strSchema()}, []string{"obligation_id"}), Handler: a.toolPaymentsLinkBill},
		{Name: "tax_payments_create_bill", Description: "Create a payable tax authority bill through the bills app and link it to the obligation. Args: obligation_id, vendor_name?, vendor_email?, due_date?.", InputSchema: schemaObject(map[string]any{"obligation_id": intSchema(), "vendor_name": strSchema(), "vendor_email": strSchema(), "due_date": strSchema()}, []string{"obligation_id"}), Handler: a.toolPaymentsCreateBill},
		{Name: "tax_adjustments_create", Description: "Create a manual adjustment. Args: profile_id, tax_type, kind, amount_cents, reason, period_id?, currency?, metadata?.", InputSchema: schemaObject(adjustmentProps(), []string{"profile_id", "tax_type", "kind", "amount_cents", "reason"}), Handler: a.toolAdjustmentsCreate},
		{Name: "tax_adjustments_list", Description: "List adjustments. Args: profile_id?, period_id?, tax_type?, status?.", InputSchema: schemaObject(map[string]any{"profile_id": intSchema(), "period_id": intSchema(), "tax_type": strSchema(), "status": strSchema()}, nil), Handler: a.toolAdjustmentsList},
		{Name: "tax_adjustments_delete", Description: "Delete an adjustment by id.", InputSchema: schemaObject(map[string]any{"id": intSchema()}, []string{"id"}), Handler: a.toolAdjustmentsDelete},
		{Name: "tax_report_generate", Description: "Generate an auditable period report. Args: profile_id, period_id?, period_start?, period_end?, tax_types?.", InputSchema: schemaObject(map[string]any{"profile_id": intSchema(), "period_id": intSchema(), "period_start": strSchema(), "period_end": strSchema(), "tax_types": arraySchema()}, []string{"profile_id"}), Handler: a.toolReportGenerate},
		{Name: "tax_documents_list", Description: "List generated tax documents. Args: profile_id?, period_id?, document_type?.", InputSchema: schemaObject(map[string]any{"profile_id": intSchema(), "period_id": intSchema(), "document_type": strSchema()}, nil), Handler: a.toolDocumentsList},
		{Name: "tax_documents_get", Description: "Fetch one tax document by id.", InputSchema: schemaObject(map[string]any{"id": intSchema()}, []string{"id"}), Handler: a.toolDocumentsGet},
		{Name: "tax_sync_from_billing", Description: "Pull billing source summary. Args: profile_id, period_start, period_end.", InputSchema: schemaObject(syncProps(), []string{"profile_id", "period_start", "period_end"}), Handler: a.toolSyncBilling},
		{Name: "tax_sync_from_bills", Description: "Pull bills source summary. Args: profile_id, period_start, period_end.", InputSchema: schemaObject(syncProps(), []string{"profile_id", "period_start", "period_end"}), Handler: a.toolSyncBills},
		{Name: "tax_sync_all", Description: "Pull billing and bills source summaries. Args: profile_id, period_start, period_end.", InputSchema: schemaObject(syncProps(), []string{"profile_id", "period_start", "period_end"}), Handler: a.toolSyncAll},
	}
}

type Profile struct {
	ID              int64          `json:"id"`
	ProjectID       string         `json:"project_id"`
	Name            string         `json:"name"`
	Country         string         `json:"country"`
	Structure       string         `json:"structure"`
	Region          string         `json:"region"`
	FiscalYearStart string         `json:"fiscal_year_start"`
	FiscalYearEnd   string         `json:"fiscal_year_end"`
	VATRegistered   bool           `json:"vat_registered"`
	FilingCadence   string         `json:"filing_cadence"`
	AccountingBasis string         `json:"accounting_basis"`
	Currency        string         `json:"currency"`
	Config          map[string]any `json:"config"`
	Archived        bool           `json:"archived"`
}

type Rule struct {
	ID            int64          `json:"id"`
	Country       string         `json:"country"`
	Structure     string         `json:"structure"`
	TaxType       string         `json:"tax_type"`
	Year          int            `json:"year"`
	Version       string         `json:"version"`
	EffectiveFrom string         `json:"effective_from"`
	EffectiveTo   string         `json:"effective_to"`
	SourceURL     string         `json:"source_url"`
	Rules         map[string]any `json:"rules"`
	Active        bool           `json:"active"`
}

type Obligation struct {
	ID            int64          `json:"id"`
	ProjectID     string         `json:"project_id"`
	ProfileID     int64          `json:"profile_id"`
	PeriodID      int64          `json:"period_id,omitempty"`
	CalculationID int64          `json:"calculation_id,omitempty"`
	TaxType       string         `json:"tax_type"`
	Authority     string         `json:"authority"`
	Title         string         `json:"title"`
	AmountCents   int64          `json:"amount_cents"`
	Currency      string         `json:"currency"`
	DueDate       string         `json:"due_date"`
	Status        string         `json:"status"`
	FiledAt       string         `json:"filed_at"`
	FilingRef     string         `json:"filing_ref"`
	WaivedReason  string         `json:"waived_reason"`
	Metadata      map[string]any `json:"metadata"`
}

type sourceSummary struct {
	RevenueCents   int64          `json:"revenue_cents"`
	ExpensesCents  int64          `json:"expenses_cents"`
	OutputTaxCents int64          `json:"output_tax_cents"`
	InputTaxCents  int64          `json:"input_tax_cents"`
	PaymentCents   int64          `json:"payment_cents"`
	Items          int            `json:"items"`
	Unavailable    bool           `json:"unavailable"`
	Warnings       []string       `json:"warnings"`
	Raw            map[string]any `json:"raw,omitempty"`
}

func (a *App) handleProfiles(w http.ResponseWriter, r *http.Request) {
	serveReadOnlyList(w, r, func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
		return a.toolProfilesList(ctx, args)
	})
}

func (a *App) handleRules(w http.ResponseWriter, r *http.Request) {
	serveReadOnlyList(w, r, func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
		return a.toolRulesList(ctx, args)
	})
}

func (a *App) handlePeriods(w http.ResponseWriter, r *http.Request) {
	serveReadOnlyList(w, r, func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
		return a.toolPeriodsList(ctx, args)
	})
}

func (a *App) handleObligations(w http.ResponseWriter, r *http.Request) {
	serveReadOnlyList(w, r, func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
		return a.toolObligationsList(ctx, args)
	})
}

func (a *App) handleDocuments(w http.ResponseWriter, r *http.Request) {
	serveReadOnlyList(w, r, func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
		return a.toolDocumentsList(ctx, args)
	})
}

func serveReadOnlyList(w http.ResponseWriter, r *http.Request, fn func(*sdk.AppCtx, map[string]any) (any, error)) {
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	if globalCtx == nil {
		httpErr(w, http.StatusServiceUnavailable, "app not mounted")
		return
	}
	args := map[string]any{}
	for k, vals := range r.URL.Query() {
		if len(vals) > 0 {
			args[k] = vals[0]
		}
	}
	out, err := fn(globalCtx, args)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	writeJSON(w, out)
}

func (a *App) toolProfilesCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	p := Profile{
		ProjectID:       projectID(ctx, args),
		Name:            strings.TrimSpace(stringArg(args, "name", "")),
		Country:         strings.ToUpper(strings.TrimSpace(stringArg(args, "country", ""))),
		Structure:       strings.ToUpper(strings.TrimSpace(stringArg(args, "structure", ""))),
		Region:          strings.TrimSpace(stringArg(args, "region", "")),
		FiscalYearStart: stringArg(args, "fiscal_year_start", "01-01"),
		FiscalYearEnd:   stringArg(args, "fiscal_year_end", "12-31"),
		VATRegistered:   boolArg(args, "vat_registered", true),
		FilingCadence:   stringArg(args, "filing_cadence", "quarterly"),
		AccountingBasis: stringArg(args, "accounting_basis", "accrual"),
		Currency:        strings.ToUpper(stringArg(args, "currency", "EUR")),
		Config:          mapArg(args, "config"),
	}
	if p.Name == "" || p.Country == "" || p.Structure == "" {
		return nil, errors.New("name, country, and structure are required")
	}
	res, err := ctx.AppDB().Exec(`INSERT INTO tax_profiles
		(project_id,name,country,structure,region,fiscal_year_start,fiscal_year_end,vat_registered,filing_cadence,accounting_basis,currency,config_json)
		VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`,
		p.ProjectID, p.Name, p.Country, p.Structure, p.Region, p.FiscalYearStart, p.FiscalYearEnd,
		boolInt(p.VATRegistered), p.FilingCadence, p.AccountingBasis, p.Currency, mustJSON(p.Config))
	if err != nil {
		return nil, err
	}
	p.ID, _ = res.LastInsertId()
	audit(ctx.AppDB(), p.ProjectID, "tax_profile", p.ID, "created", p.Name, nil)
	return map[string]any{"profile": p}, nil
}

func (a *App) toolProfilesUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64Arg(args, "id", 0)
	p, err := getProfile(ctx.AppDB(), projectID(ctx, args), id)
	if err != nil {
		return nil, err
	}
	if hasArg(args, "name") {
		p.Name = stringArg(args, "name", p.Name)
	}
	if hasArg(args, "country") {
		p.Country = strings.ToUpper(stringArg(args, "country", p.Country))
	}
	if hasArg(args, "structure") {
		p.Structure = strings.ToUpper(stringArg(args, "structure", p.Structure))
	}
	if hasArg(args, "region") {
		p.Region = stringArg(args, "region", p.Region)
	}
	if hasArg(args, "fiscal_year_start") {
		p.FiscalYearStart = stringArg(args, "fiscal_year_start", p.FiscalYearStart)
	}
	if hasArg(args, "fiscal_year_end") {
		p.FiscalYearEnd = stringArg(args, "fiscal_year_end", p.FiscalYearEnd)
	}
	if hasArg(args, "vat_registered") {
		p.VATRegistered = boolArg(args, "vat_registered", p.VATRegistered)
	}
	if hasArg(args, "filing_cadence") {
		p.FilingCadence = stringArg(args, "filing_cadence", p.FilingCadence)
	}
	if hasArg(args, "accounting_basis") {
		p.AccountingBasis = stringArg(args, "accounting_basis", p.AccountingBasis)
	}
	if hasArg(args, "currency") {
		p.Currency = strings.ToUpper(stringArg(args, "currency", p.Currency))
	}
	if hasArg(args, "config") {
		p.Config = mapArg(args, "config")
	}
	if hasArg(args, "archived") {
		p.Archived = boolArg(args, "archived", p.Archived)
	}
	_, err = ctx.AppDB().Exec(`UPDATE tax_profiles SET
		name=?, country=?, structure=?, region=?, fiscal_year_start=?, fiscal_year_end=?, vat_registered=?,
		filing_cadence=?, accounting_basis=?, currency=?, config_json=?, archived=?, updated_at=CURRENT_TIMESTAMP
		WHERE project_id=? AND id=?`,
		p.Name, p.Country, p.Structure, p.Region, p.FiscalYearStart, p.FiscalYearEnd, boolInt(p.VATRegistered),
		p.FilingCadence, p.AccountingBasis, p.Currency, mustJSON(p.Config), boolInt(p.Archived), p.ProjectID, p.ID)
	if err != nil {
		return nil, err
	}
	audit(ctx.AppDB(), p.ProjectID, "tax_profile", p.ID, "updated", p.Name, nil)
	return map[string]any{"profile": p}, nil
}

func (a *App) toolProfilesGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	p, err := getProfile(ctx.AppDB(), projectID(ctx, args), int64Arg(args, "id", 0))
	if err != nil {
		return nil, err
	}
	return map[string]any{"profile": p}, nil
}

func (a *App) toolProfilesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	where := []string{"project_id=?"}
	vals := []any{projectID(ctx, args)}
	if !boolArg(args, "archived", false) {
		where = append(where, "archived=0")
	}
	if v := strings.ToUpper(stringArg(args, "country", "")); v != "" {
		where = append(where, "country=?")
		vals = append(vals, v)
	}
	if v := strings.ToUpper(stringArg(args, "structure", "")); v != "" {
		where = append(where, "structure=?")
		vals = append(vals, v)
	}
	rows, err := ctx.AppDB().Query(`SELECT id,project_id,name,country,structure,region,fiscal_year_start,fiscal_year_end,vat_registered,filing_cadence,accounting_basis,currency,config_json,archived FROM tax_profiles WHERE `+strings.Join(where, " AND ")+` ORDER BY name`, vals...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Profile
	for rows.Next() {
		p, err := scanProfile(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return map[string]any{"profiles": out}, rows.Err()
}

func (a *App) toolRulesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if err := seedTaxRules(ctx.AppDB()); err != nil {
		return nil, err
	}
	rules, err := listRules(ctx.AppDB(), args)
	if err != nil {
		return nil, err
	}
	return map[string]any{"rules": rules}, nil
}

func (a *App) toolRulesGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if err := seedTaxRules(ctx.AppDB()); err != nil {
		return nil, err
	}
	var r Rule
	var err error
	if id := int64Arg(args, "id", 0); id > 0 {
		r, err = getRuleByID(ctx.AppDB(), id)
	} else {
		r, err = findRule(ctx.AppDB(), strings.ToUpper(stringArg(args, "country", "")), strings.ToUpper(stringArg(args, "structure", "")), stringArg(args, "tax_type", ""), intArg(args, "year", currentYear()))
	}
	if err != nil {
		return nil, err
	}
	return map[string]any{"rule": r}, nil
}

func (a *App) toolPeriodsOpen(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid := int64Arg(args, "profile_id", 0)
	profile, err := getProfile(ctx.AppDB(), projectID(ctx, args), pid)
	if err != nil {
		return nil, err
	}
	taxType := normalizeTaxType(stringArg(args, "tax_type", ""))
	if taxType == "" {
		return nil, errors.New("tax_type is required")
	}
	start, end := stringArg(args, "period_start", ""), stringArg(args, "period_end", "")
	if start == "" || end == "" {
		return nil, errors.New("period_start and period_end are required")
	}
	res, err := ctx.AppDB().Exec(`INSERT INTO tax_periods
		(project_id,profile_id,tax_type,period_start,period_end,due_date,metadata_json)
		VALUES (?,?,?,?,?,?,?)
		ON CONFLICT(project_id, profile_id, tax_type, period_start, period_end)
		DO UPDATE SET due_date=excluded.due_date, metadata_json=excluded.metadata_json, updated_at=CURRENT_TIMESTAMP`,
		profile.ProjectID, profile.ID, taxType, start, end, stringArg(args, "due_date", ""), mustJSON(mapArg(args, "metadata")))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	if id == 0 {
		row := ctx.AppDB().QueryRow(`SELECT id FROM tax_periods WHERE project_id=? AND profile_id=? AND tax_type=? AND period_start=? AND period_end=?`, profile.ProjectID, profile.ID, taxType, start, end)
		_ = row.Scan(&id)
	}
	audit(ctx.AppDB(), profile.ProjectID, "tax_period", id, "opened", taxType, nil)
	period, err := getPeriod(ctx.AppDB(), profile.ProjectID, id)
	return map[string]any{"period": period}, err
}

func (a *App) toolPeriodsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	where := []string{"project_id=?"}
	vals := []any{projectID(ctx, args)}
	addIntFilter(&where, &vals, "profile_id", int64Arg(args, "profile_id", 0))
	addStringFilter(&where, &vals, "tax_type", normalizeTaxType(stringArg(args, "tax_type", "")))
	addStringFilter(&where, &vals, "status", stringArg(args, "status", ""))
	rows, err := ctx.AppDB().Query(`SELECT id,project_id,profile_id,tax_type,period_start,period_end,due_date,status,filed_at,filing_ref,metadata_json FROM tax_periods WHERE `+strings.Join(where, " AND ")+` ORDER BY period_start DESC, tax_type`, vals...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := scanRows(rows)
	return map[string]any{"periods": items}, err
}

func (a *App) toolPeriodsGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	p, err := getPeriod(ctx.AppDB(), projectID(ctx, args), int64Arg(args, "id", 0))
	return map[string]any{"period": p}, err
}

func (a *App) toolPeriodsClose(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64Arg(args, "id", 0)
	status := stringArg(args, "status", "closed")
	_, err := ctx.AppDB().Exec(`UPDATE tax_periods SET status=?, filed_at=?, filing_ref=?, updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`,
		status, stringArg(args, "filed_at", ""), stringArg(args, "filing_ref", ""), projectID(ctx, args), id)
	if err != nil {
		return nil, err
	}
	audit(ctx.AppDB(), projectID(ctx, args), "tax_period", id, "closed", status, nil)
	p, err := getPeriod(ctx.AppDB(), projectID(ctx, args), id)
	return map[string]any{"period": p}, err
}

func (a *App) toolEstimateVAT(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return a.calculate(ctx, args, "vat")
}

func (a *App) toolEstimateIncome(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return a.calculate(ctx, args, "income_tax")
}

func (a *App) toolEstimateCorporate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return a.calculate(ctx, args, "corporate_tax")
}

func (a *App) toolEstimateSocial(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return a.calculate(ctx, args, "social_contributions")
}

func (a *App) toolEstimateAll(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	profile, err := getProfile(ctx.AppDB(), projectID(ctx, args), int64Arg(args, "profile_id", 0))
	if err != nil {
		return nil, err
	}
	types := []string{"vat"}
	switch profile.Structure {
	case "ES_AUTONOMO":
		types = append(types, "income_tax", "social_contributions")
	case "ES_SL", "FR_SAS", "FR_SASU", "FR_SARL", "FR_EURL":
		types = append(types, "corporate_tax")
	default:
		types = append(types, "income_tax")
	}
	var estimates []any
	for _, typ := range types {
		out, err := a.calculate(ctx, cloneMap(args), typ)
		if err != nil {
			return nil, err
		}
		estimates = append(estimates, out)
	}
	return map[string]any{"profile": profile, "estimates": estimates}, nil
}

func (a *App) calculate(ctx *sdk.AppCtx, args map[string]any, taxType string) (any, error) {
	profile, err := getProfile(ctx.AppDB(), projectID(ctx, args), int64Arg(args, "profile_id", 0))
	if err != nil {
		return nil, err
	}
	start, end := stringArg(args, "period_start", ""), stringArg(args, "period_end", "")
	if start == "" || end == "" {
		return nil, errors.New("period_start and period_end are required")
	}
	year := yearFromDate(start)
	rule, err := findRule(ctx.AppDB(), profile.Country, profile.Structure, taxType, year)
	if err != nil {
		return nil, err
	}
	inputs, sources, warnings := a.collectInputs(ctx, args, profile, start, end)
	outputs := calculateOutputs(taxType, rule, inputs, start, end)
	for _, adj := range adjustmentsFor(ctx.AppDB(), profile.ProjectID, profile.ID, int64Arg(args, "period_id", 0), taxType) {
		outputs["adjustments_cents"] = int64FromAny(outputs["adjustments_cents"]) + int64FromAny(adj["amount_cents"])
		outputs["estimated_payable_cents"] = int64FromAny(outputs["estimated_payable_cents"]) + int64FromAny(adj["amount_cents"])
	}
	if sources["billing_unavailable"] == true || sources["bills_unavailable"] == true {
		warnings = append(warnings, "cross-app source data was unavailable; estimate may rely on caller-supplied amounts or zero defaults")
	}
	periodID := int64Arg(args, "period_id", 0)
	calcID, err := insertCalculation(ctx.AppDB(), profile, periodID, taxType, rule, start, end, inputs, outputs, sources, warnings)
	if err != nil {
		return nil, err
	}
	var obligation *Obligation
	if boolArg(args, "create_obligation", true) {
		obligation, err = upsertEstimatedObligation(ctx.AppDB(), profile, periodID, calcID, taxType, outputs, stringArg(args, "due_date", ""))
		if err != nil {
			return nil, err
		}
	}
	result := map[string]any{
		"calculation_id": calcID,
		"profile":        profile,
		"tax_type":       taxType,
		"rule":           rule,
		"period_start":   start,
		"period_end":     end,
		"inputs":         inputs,
		"outputs":        outputs,
		"sources":        sources,
		"warnings":       warnings,
		"confidence":     confidence(warnings),
	}
	if obligation != nil {
		result["obligation"] = obligation
	}
	return result, nil
}

func (a *App) collectInputs(ctx *sdk.AppCtx, args map[string]any, profile Profile, start, end string) (map[string]any, map[string]any, []string) {
	inputs := map[string]any{
		"revenue_cents":             int64Arg(args, "revenue_cents", 0),
		"expenses_cents":            int64Arg(args, "expenses_cents", 0),
		"output_tax_cents":          int64Arg(args, "output_tax_cents", 0),
		"input_tax_cents":           int64Arg(args, "input_tax_cents", 0),
		"taxable_profit_cents":      int64Arg(args, "taxable_profit_cents", 0),
		"social_contribution_cents": int64Arg(args, "social_contribution_cents", 0),
		"months":                    intArg(args, "months", monthsBetween(start, end)),
	}
	sources := map[string]any{}
	warnings := []string{}
	if !boolArg(args, "sync_sources", true) {
		return inputs, sources, warnings
	}
	billing := syncBilling(ctx, profile, start, end)
	bills := syncBills(ctx, profile, start, end)
	sources["billing"] = billing
	sources["bills"] = bills
	if billing.Unavailable {
		sources["billing_unavailable"] = true
	} else {
		inputs["revenue_cents"] = int64FromAny(inputs["revenue_cents"]) + billing.RevenueCents
		inputs["output_tax_cents"] = int64FromAny(inputs["output_tax_cents"]) + billing.OutputTaxCents
	}
	if bills.Unavailable {
		sources["bills_unavailable"] = true
	} else {
		inputs["expenses_cents"] = int64FromAny(inputs["expenses_cents"]) + bills.ExpensesCents
		inputs["input_tax_cents"] = int64FromAny(inputs["input_tax_cents"]) + bills.InputTaxCents
	}
	warnings = append(warnings, billing.Warnings...)
	warnings = append(warnings, bills.Warnings...)
	return inputs, sources, warnings
}

func (a *App) toolObligationsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	profile, err := getProfile(ctx.AppDB(), projectID(ctx, args), int64Arg(args, "profile_id", 0))
	if err != nil {
		return nil, err
	}
	o, err := insertObligation(ctx.AppDB(), profile, int64Arg(args, "period_id", 0), 0, normalizeTaxType(stringArg(args, "tax_type", "")), stringArg(args, "title", ""), int64Arg(args, "amount_cents", 0), strings.ToUpper(stringArg(args, "currency", profile.Currency)), stringArg(args, "due_date", ""), stringArg(args, "authority", defaultAuthority(profile, normalizeTaxType(stringArg(args, "tax_type", "")))), "estimated", mapArg(args, "metadata"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"obligation": o}, nil
}

func (a *App) toolObligationsUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	o, err := getObligation(ctx.AppDB(), projectID(ctx, args), int64Arg(args, "id", 0))
	if err != nil {
		return nil, err
	}
	if hasArg(args, "tax_type") {
		o.TaxType = normalizeTaxType(stringArg(args, "tax_type", o.TaxType))
	}
	if hasArg(args, "title") {
		o.Title = stringArg(args, "title", o.Title)
	}
	if hasArg(args, "amount_cents") {
		o.AmountCents = int64Arg(args, "amount_cents", o.AmountCents)
	}
	if hasArg(args, "currency") {
		o.Currency = strings.ToUpper(stringArg(args, "currency", o.Currency))
	}
	if hasArg(args, "due_date") {
		o.DueDate = stringArg(args, "due_date", o.DueDate)
	}
	if hasArg(args, "authority") {
		o.Authority = stringArg(args, "authority", o.Authority)
	}
	if hasArg(args, "status") {
		o.Status = stringArg(args, "status", o.Status)
	}
	if hasArg(args, "metadata") {
		o.Metadata = mapArg(args, "metadata")
	}
	_, err = ctx.AppDB().Exec(`UPDATE tax_obligations SET tax_type=?, title=?, amount_cents=?, currency=?, due_date=?, authority=?, status=?, metadata_json=?, updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`,
		o.TaxType, o.Title, o.AmountCents, o.Currency, o.DueDate, o.Authority, o.Status, mustJSON(o.Metadata), o.ProjectID, o.ID)
	if err != nil {
		return nil, err
	}
	audit(ctx.AppDB(), o.ProjectID, "tax_obligation", o.ID, "updated", o.Title, nil)
	o, err = getObligation(ctx.AppDB(), o.ProjectID, o.ID)
	return map[string]any{"obligation": o}, err
}

func (a *App) toolObligationsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	where := []string{"project_id=?"}
	vals := []any{projectID(ctx, args)}
	addIntFilter(&where, &vals, "profile_id", int64Arg(args, "profile_id", 0))
	addIntFilter(&where, &vals, "period_id", int64Arg(args, "period_id", 0))
	addStringFilter(&where, &vals, "tax_type", normalizeTaxType(stringArg(args, "tax_type", "")))
	addStringFilter(&where, &vals, "status", stringArg(args, "status", ""))
	addStringFilter(&where, &vals, "authority", stringArg(args, "authority", ""))
	if due := stringArg(args, "due_before", ""); due != "" {
		where = append(where, "due_date <= ?")
		vals = append(vals, due)
	}
	rows, err := ctx.AppDB().Query(`SELECT id,project_id,profile_id,COALESCE(period_id,0),COALESCE(calculation_id,0),tax_type,authority,title,amount_cents,currency,due_date,status,filed_at,filing_ref,waived_reason,metadata_json FROM tax_obligations WHERE `+strings.Join(where, " AND ")+` ORDER BY due_date, id`, vals...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Obligation{}
	for rows.Next() {
		o, err := scanObligation(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, o)
	}
	return map[string]any{"obligations": out}, rows.Err()
}

func (a *App) toolObligationsGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	o, err := getObligation(ctx.AppDB(), projectID(ctx, args), int64Arg(args, "id", 0))
	return map[string]any{"obligation": o}, err
}

func (a *App) toolObligationsMarkFiled(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64Arg(args, "id", 0)
	filedAt := stringArg(args, "filed_at", time.Now().UTC().Format(time.RFC3339))
	_, err := ctx.AppDB().Exec(`UPDATE tax_obligations SET status='filed', filed_at=?, filing_ref=?, updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, filedAt, stringArg(args, "filing_ref", ""), projectID(ctx, args), id)
	if err != nil {
		return nil, err
	}
	audit(ctx.AppDB(), projectID(ctx, args), "tax_obligation", id, "filed", "", nil)
	o, err := getObligation(ctx.AppDB(), projectID(ctx, args), id)
	return map[string]any{"obligation": o}, err
}

func (a *App) toolObligationsMarkWaived(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64Arg(args, "id", 0)
	_, err := ctx.AppDB().Exec(`UPDATE tax_obligations SET status='waived', waived_reason=?, updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, stringArg(args, "reason", ""), projectID(ctx, args), id)
	if err != nil {
		return nil, err
	}
	audit(ctx.AppDB(), projectID(ctx, args), "tax_obligation", id, "waived", stringArg(args, "reason", ""), nil)
	o, err := getObligation(ctx.AppDB(), projectID(ctx, args), id)
	return map[string]any{"obligation": o}, err
}

func (a *App) toolPaymentsRecord(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return a.recordPayment(ctx, args, int64Arg(args, "bills_bill_id", 0), int64Arg(args, "bills_payment_id", 0))
}

func (a *App) toolPaymentsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	where := []string{"p.project_id=?"}
	vals := []any{projectID(ctx, args)}
	addIntFilter(&where, &vals, "p.obligation_id", int64Arg(args, "obligation_id", 0))
	if profileID := int64Arg(args, "profile_id", 0); profileID > 0 {
		where = append(where, "o.profile_id=?")
		vals = append(vals, profileID)
	}
	rows, err := ctx.AppDB().Query(`SELECT p.id,p.project_id,p.obligation_id,p.amount_cents,p.currency,p.paid_at,p.method,p.reference,COALESCE(p.bills_bill_id,0),COALESCE(p.bills_payment_id,0),p.notes,p.metadata_json
		FROM tax_payments p JOIN tax_obligations o ON o.id=p.obligation_id
		WHERE `+strings.Join(where, " AND ")+` ORDER BY p.paid_at DESC, p.id DESC`, vals...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := scanRows(rows)
	return map[string]any{"payments": items}, err
}

func (a *App) toolPaymentsLinkBill(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return a.recordPayment(ctx, args, int64Arg(args, "bills_bill_id", 0), int64Arg(args, "bills_payment_id", 0))
}

func (a *App) recordPayment(ctx *sdk.AppCtx, args map[string]any, billID, paymentID int64) (any, error) {
	o, err := getObligation(ctx.AppDB(), projectID(ctx, args), int64Arg(args, "obligation_id", 0))
	if err != nil {
		return nil, err
	}
	amount := int64Arg(args, "amount_cents", o.AmountCents)
	if amount == 0 {
		return nil, errors.New("amount_cents is required")
	}
	paidAt := stringArg(args, "paid_at", time.Now().UTC().Format(time.RFC3339))
	res, err := ctx.AppDB().Exec(`INSERT INTO tax_payments (project_id,obligation_id,amount_cents,currency,paid_at,method,reference,bills_bill_id,bills_payment_id,notes,metadata_json) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		o.ProjectID, o.ID, amount, stringArg(args, "currency", o.Currency), paidAt, stringArg(args, "method", ""), stringArg(args, "reference", ""), nullInt(billID), nullInt(paymentID), stringArg(args, "notes", ""), mustJSON(mapArg(args, "metadata")))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	_, _ = ctx.AppDB().Exec(`UPDATE tax_obligations SET status='paid', updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=? AND (SELECT COALESCE(SUM(amount_cents),0) FROM tax_payments WHERE obligation_id=?) >= amount_cents`, o.ProjectID, o.ID, o.ID)
	audit(ctx.AppDB(), o.ProjectID, "tax_obligation", o.ID, "payment_recorded", "", map[string]any{"payment_id": id, "amount_cents": amount})
	return map[string]any{"payment_id": id, "obligation_id": o.ID}, nil
}

func (a *App) toolPaymentsCreateBill(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	o, err := getObligation(ctx.AppDB(), projectID(ctx, args), int64Arg(args, "obligation_id", 0))
	if err != nil {
		return nil, err
	}
	vendorName := stringArg(args, "vendor_name", o.Authority)
	if vendorName == "" {
		vendorName = "Tax authority"
	}
	var vendorOut map[string]any
	if err := ctx.PlatformAPI().CallAppResult("bills", "vendors_upsert_by_email", map[string]any{
		"email": stringArg(args, "vendor_email", "tax-authority@example.invalid"),
		"defaults": map[string]any{
			"name": vendorName,
		},
	}, &vendorOut); err != nil {
		return nil, fmt.Errorf("create/link vendor in bills: %w", err)
	}
	vendorID := findNestedInt(vendorOut, "vendor", "id")
	var billOut map[string]any
	if err := ctx.PlatformAPI().CallAppResult("bills", "bills_create", map[string]any{
		"vendor_id":             vendorID,
		"vendor_invoice_number": fmt.Sprintf("tax-%d", o.ID),
		"currency":              o.Currency,
		"due_date":              stringArg(args, "due_date", o.DueDate),
		"notes":                 o.Title,
		"line_items": []map[string]any{{
			"description":      o.Title,
			"quantity":         1,
			"unit_price_cents": o.AmountCents,
		}},
	}, &billOut); err != nil {
		return nil, fmt.Errorf("create bill in bills: %w", err)
	}
	billID := findNestedInt(billOut, "bill", "id")
	_, _ = ctx.AppDB().Exec(`UPDATE tax_obligations SET metadata_json=json_set(COALESCE(NULLIF(metadata_json,''),'{}'), '$.bills_bill_id', ?) WHERE project_id=? AND id=?`, billID, o.ProjectID, o.ID)
	audit(ctx.AppDB(), o.ProjectID, "tax_obligation", o.ID, "bill_created", "", map[string]any{"bills_bill_id": billID})
	return map[string]any{"obligation": o, "bills_bill_id": billID, "bills_response": billOut}, nil
}

func (a *App) toolAdjustmentsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	profile, err := getProfile(ctx.AppDB(), projectID(ctx, args), int64Arg(args, "profile_id", 0))
	if err != nil {
		return nil, err
	}
	res, err := ctx.AppDB().Exec(`INSERT INTO tax_adjustments (project_id,profile_id,period_id,tax_type,kind,amount_cents,currency,reason,metadata_json) VALUES (?,?,?,?,?,?,?,?,?)`,
		profile.ProjectID, profile.ID, nullInt(int64Arg(args, "period_id", 0)), normalizeTaxType(stringArg(args, "tax_type", "")), stringArg(args, "kind", ""), int64Arg(args, "amount_cents", 0), stringArg(args, "currency", profile.Currency), stringArg(args, "reason", ""), mustJSON(mapArg(args, "metadata")))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	audit(ctx.AppDB(), profile.ProjectID, "tax_adjustment", id, "created", stringArg(args, "reason", ""), nil)
	return map[string]any{"adjustment_id": id}, nil
}

func (a *App) toolAdjustmentsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	where := []string{"project_id=?"}
	vals := []any{projectID(ctx, args)}
	addIntFilter(&where, &vals, "profile_id", int64Arg(args, "profile_id", 0))
	addIntFilter(&where, &vals, "period_id", int64Arg(args, "period_id", 0))
	addStringFilter(&where, &vals, "tax_type", normalizeTaxType(stringArg(args, "tax_type", "")))
	addStringFilter(&where, &vals, "status", stringArg(args, "status", ""))
	rows, err := ctx.AppDB().Query(`SELECT id,project_id,profile_id,COALESCE(period_id,0),tax_type,kind,amount_cents,currency,reason,status,metadata_json FROM tax_adjustments WHERE `+strings.Join(where, " AND ")+` ORDER BY id DESC`, vals...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := scanRows(rows)
	return map[string]any{"adjustments": items}, err
}

func (a *App) toolAdjustmentsDelete(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64Arg(args, "id", 0)
	res, err := ctx.AppDB().Exec(`DELETE FROM tax_adjustments WHERE project_id=? AND id=?`, projectID(ctx, args), id)
	if err != nil {
		return nil, err
	}
	n, _ := res.RowsAffected()
	return map[string]any{"deleted": n > 0}, nil
}

func (a *App) toolReportGenerate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	profile, err := getProfile(ctx.AppDB(), projectID(ctx, args), int64Arg(args, "profile_id", 0))
	if err != nil {
		return nil, err
	}
	report := map[string]any{"profile": profile, "generated_at": time.Now().UTC().Format(time.RFC3339)}
	if periodID := int64Arg(args, "period_id", 0); periodID > 0 {
		period, err := getPeriod(ctx.AppDB(), profile.ProjectID, periodID)
		if err != nil {
			return nil, err
		}
		report["period"] = period
	}
	obligations, _ := a.toolObligationsList(ctx, map[string]any{"project_id": profile.ProjectID, "profile_id": profile.ID})
	adjustments, _ := a.toolAdjustmentsList(ctx, map[string]any{"project_id": profile.ProjectID, "profile_id": profile.ID})
	report["obligations"] = obligations
	report["adjustments"] = adjustments
	res, err := ctx.AppDB().Exec(`INSERT INTO tax_documents (project_id,profile_id,period_id,document_type,title,content_json) VALUES (?,?,?,?,?,?)`,
		profile.ProjectID, profile.ID, nullInt(int64Arg(args, "period_id", 0)), "report", "Tax report - "+profile.Name, mustJSON(report))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return map[string]any{"document_id": id, "report": report}, nil
}

func (a *App) toolDocumentsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	where := []string{"project_id=?"}
	vals := []any{projectID(ctx, args)}
	addIntFilter(&where, &vals, "profile_id", int64Arg(args, "profile_id", 0))
	addIntFilter(&where, &vals, "period_id", int64Arg(args, "period_id", 0))
	addStringFilter(&where, &vals, "document_type", stringArg(args, "document_type", ""))
	rows, err := ctx.AppDB().Query(`SELECT id,project_id,COALESCE(profile_id,0),COALESCE(period_id,0),document_type,title,storage_file_id,content_json,created_at FROM tax_documents WHERE `+strings.Join(where, " AND ")+` ORDER BY id DESC`, vals...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items, err := scanRows(rows)
	return map[string]any{"documents": items}, err
}

func (a *App) toolDocumentsGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	rows, err := ctx.AppDB().Query(`SELECT id,project_id,COALESCE(profile_id,0),COALESCE(period_id,0),document_type,title,storage_file_id,content_json,created_at FROM tax_documents WHERE project_id=? AND id=?`, projectID(ctx, args), int64Arg(args, "id", 0))
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
	return map[string]any{"document": items[0]}, nil
}

func (a *App) toolSyncBilling(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	profile, err := getProfile(ctx.AppDB(), projectID(ctx, args), int64Arg(args, "profile_id", 0))
	if err != nil {
		return nil, err
	}
	return map[string]any{"billing": syncBilling(ctx, profile, stringArg(args, "period_start", ""), stringArg(args, "period_end", ""))}, nil
}

func (a *App) toolSyncBills(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	profile, err := getProfile(ctx.AppDB(), projectID(ctx, args), int64Arg(args, "profile_id", 0))
	if err != nil {
		return nil, err
	}
	return map[string]any{"bills": syncBills(ctx, profile, stringArg(args, "period_start", ""), stringArg(args, "period_end", ""))}, nil
}

func (a *App) toolSyncAll(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	profile, err := getProfile(ctx.AppDB(), projectID(ctx, args), int64Arg(args, "profile_id", 0))
	if err != nil {
		return nil, err
	}
	start, end := stringArg(args, "period_start", ""), stringArg(args, "period_end", "")
	return map[string]any{"billing": syncBilling(ctx, profile, start, end), "bills": syncBills(ctx, profile, start, end)}, nil
}

func main() { sdk.Run(&App{}) }
