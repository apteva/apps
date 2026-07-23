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
version: 1.0.3
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
    - { name: tax_periods_generate, description: "Generate standard periods inferred from a tax profile." }
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
	ctx.Logger().Info("taxes mounted", "version", "1.0.3", "scope_project_id", os.Getenv("APTEVA_PROJECT_ID"))
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
	if args["_project_id"] == nil {
		args["_project_id"] = r.URL.Query().Get("project_id")
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
		{Name: "tax_periods_generate", Description: "Generate standard periods inferred from a profile's country, structure, and filing cadence. Args: profile_id, year? (default current year).", InputSchema: schemaObject(map[string]any{"profile_id": intSchema(), "year": intSchema()}, []string{"profile_id"}), Handler: a.toolPeriodsGenerate},
		{Name: "tax_periods_list", Description: "List tax periods. Args: profile_id?, tax_type?, status?, period_start?, period_end?.", InputSchema: schemaObject(map[string]any{"profile_id": intSchema(), "tax_type": strSchema(), "status": strSchema(), "period_start": strSchema(), "period_end": strSchema()}, nil), Handler: a.toolPeriodsList},
		{Name: "tax_periods_get", Description: "Fetch one tax period by id.", InputSchema: schemaObject(map[string]any{"id": intSchema()}, []string{"id"}), Handler: a.toolPeriodsGet},
		{Name: "tax_periods_close", Description: "Close a period. Args: id, status? (default closed), filed_at?, filing_ref?.", InputSchema: schemaObject(map[string]any{"id": intSchema(), "status": strSchema(), "filed_at": strSchema(), "filing_ref": strSchema()}, []string{"id"}), Handler: a.toolPeriodsClose},
		{Name: "tax_estimate_vat", Description: "Estimate VAT/IVA/TVA for a generated period_id, or a start/end range when create_obligation=false.", InputSchema: schemaObject(estimateProps(), []string{"profile_id"}), Handler: a.toolEstimateVAT},
		{Name: "tax_estimate_income_tax", Description: "Estimate income tax for a generated period_id, or a start/end range when create_obligation=false.", InputSchema: schemaObject(estimateProps(), []string{"profile_id"}), Handler: a.toolEstimateIncome},
		{Name: "tax_estimate_corporate_tax", Description: "Estimate corporate tax for a generated period_id, or a start/end range when create_obligation=false.", InputSchema: schemaObject(estimateProps(), []string{"profile_id"}), Handler: a.toolEstimateCorporate},
		{Name: "tax_estimate_social_contributions", Description: "Estimate social contributions for a generated period_id, or a start/end range when create_obligation=false.", InputSchema: schemaObject(estimateProps(), []string{"profile_id"}), Handler: a.toolEstimateSocial},
		{Name: "tax_estimate_all", Description: "Estimate every applicable generated period in the requested range, or only period_id when supplied.", InputSchema: schemaObject(estimateProps(), []string{"profile_id"}), Handler: a.toolEstimateAll},
		{Name: "tax_obligations_create", Description: "Create a tax/statutory obligation. Args: profile_id, tax_type, title, amount_cents, currency?, due_date?, authority?, period_id?, metadata?.", InputSchema: schemaObject(obligationProps(), []string{"profile_id", "tax_type", "title", "amount_cents"}), Handler: a.toolObligationsCreate},
		{Name: "tax_obligations_update", Description: "Update an obligation. Args: id plus amount_cents?, status?, due_date?, title?, authority?, metadata?.", InputSchema: schemaObject(withID(obligationProps()), []string{"id"}), Handler: a.toolObligationsUpdate},
		{Name: "tax_obligations_list", Description: "List obligations. Args: profile_id?, period_id?, tax_type?, status?, due_before?, authority?.", InputSchema: schemaObject(map[string]any{"profile_id": intSchema(), "period_id": intSchema(), "tax_type": strSchema(), "status": strSchema(), "due_before": strSchema(), "authority": strSchema()}, nil), Handler: a.toolObligationsList},
		{Name: "tax_obligations_get", Description: "Fetch one obligation by id.", InputSchema: schemaObject(map[string]any{"id": intSchema()}, []string{"id"}), Handler: a.toolObligationsGet},
		{Name: "tax_obligations_mark_filed", Description: "Mark an obligation filed. Args: id, filed_at?, filing_ref?.", InputSchema: schemaObject(map[string]any{"id": intSchema(), "filed_at": strSchema(), "filing_ref": strSchema()}, []string{"id"}), Handler: a.toolObligationsMarkFiled},
		{Name: "tax_obligations_mark_waived", Description: "Mark an obligation waived. Args: id, reason.", InputSchema: schemaObject(map[string]any{"id": intSchema(), "reason": strSchema()}, []string{"id", "reason"}), Handler: a.toolObligationsMarkWaived},
		{Name: "tax_payments_record", Description: "Record a tax payment. Args: obligation_id, amount_cents, paid_at?, method?, reference?, notes?, bills_bill_id?, bills_payment_id?.", InputSchema: schemaObject(paymentProps(), []string{"obligation_id", "amount_cents"}), Handler: a.toolPaymentsRecord},
		{Name: "tax_payments_list", Description: "List tax payments. Args: obligation_id?, profile_id?.", InputSchema: schemaObject(map[string]any{"obligation_id": intSchema(), "profile_id": intSchema()}, nil), Handler: a.toolPaymentsList},
		{Name: "tax_payments_link_bill", Description: "Link a Bills bill to an obligation. A tax payment is recorded only when bills_payment_id is supplied and verified against that bill.", InputSchema: schemaObject(map[string]any{"obligation_id": intSchema(), "bills_bill_id": intSchema(), "bills_payment_id": intSchema()}, []string{"obligation_id", "bills_bill_id"}), Handler: a.toolPaymentsLinkBill},
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
	Direction     string         `json:"direction"`
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
	if args["_project_id"] == nil {
		args["_project_id"] = r.URL.Query().Get("project_id")
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
	if err := validateProfile(p); err != nil {
		return nil, err
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
	out := map[string]any{"profile": p}
	if boolArg(args, "auto_open_periods", true) {
		generated, err := generatePeriodsForProfile(ctx.AppDB(), p, intArg(args, "year", currentYear()))
		if err != nil {
			return nil, err
		}
		out["periods"] = generated
		next, err := generatePeriodsForProfile(ctx.AppDB(), p, intArg(args, "year", currentYear())+1)
		if err != nil {
			return nil, err
		}
		out["next_year_periods"] = next
	}
	return out, nil
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
	if err := validateProfile(p); err != nil {
		return nil, err
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
	out := map[string]any{"profile": p}
	if boolArg(args, "auto_open_periods", true) && !p.Archived {
		periods, err := generatePeriodsForProfile(ctx.AppDB(), p, currentYear())
		if err != nil {
			return nil, err
		}
		next, err := generatePeriodsForProfile(ctx.AppDB(), p, currentYear()+1)
		if err != nil {
			return nil, err
		}
		out["periods"] = periods
		out["next_year_periods"] = next
	}
	return out, nil
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
	if err := validateTaxType(taxType); err != nil {
		return nil, err
	}
	if !containsString(inferredTaxTypes(profile), taxType) {
		return nil, fmt.Errorf("%s is not active for this profile", taxType)
	}
	start, end := stringArg(args, "period_start", ""), stringArg(args, "period_end", "")
	if !validISODate(start) || !validISODate(end) || start > end {
		return nil, errors.New("period_start and period_end must be valid ordered YYYY-MM-DD dates")
	}
	dueDate := stringArg(args, "due_date", "")
	if dueDate != "" && !validISODate(dueDate) {
		return nil, errors.New("due_date must use YYYY-MM-DD")
	}
	res, err := ctx.AppDB().Exec(`INSERT INTO tax_periods
		(project_id,profile_id,tax_type,period_start,period_end,due_date,metadata_json)
		VALUES (?,?,?,?,?,?,?)
		ON CONFLICT(project_id, profile_id, tax_type, period_start, period_end)
		DO UPDATE SET due_date=excluded.due_date, metadata_json=excluded.metadata_json, updated_at=CURRENT_TIMESTAMP`,
		profile.ProjectID, profile.ID, taxType, start, end, dueDate, mustJSON(mapArg(args, "metadata")))
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

func (a *App) toolPeriodsGenerate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	profile, err := getProfile(ctx.AppDB(), projectID(ctx, args), int64Arg(args, "profile_id", 0))
	if err != nil {
		return nil, err
	}
	year := intArg(args, "year", currentYear())
	periods, err := generatePeriodsForProfile(ctx.AppDB(), profile, year)
	if err != nil {
		return nil, err
	}
	audit(ctx.AppDB(), profile.ProjectID, "tax_profile", profile.ID, "periods_generated", fmt.Sprintf("%d", year), map[string]any{"count": len(periods)})
	return map[string]any{"profile": profile, "year": year, "periods": periods}, nil
}

func (a *App) toolPeriodsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	where := []string{"project_id=?"}
	vals := []any{projectID(ctx, args)}
	addIntFilter(&where, &vals, "profile_id", int64Arg(args, "profile_id", 0))
	addStringFilter(&where, &vals, "tax_type", normalizeTaxType(stringArg(args, "tax_type", "")))
	addStringFilter(&where, &vals, "status", stringArg(args, "status", ""))
	if start := stringArg(args, "period_start", ""); start != "" {
		where = append(where, "period_end>=?")
		vals = append(vals, start)
	}
	if end := stringArg(args, "period_end", ""); end != "" {
		where = append(where, "period_start<=?")
		vals = append(vals, end)
	}
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
	if status != "closed" && status != "filed" {
		return nil, errors.New("status must be closed or filed")
	}
	res, err := ctx.AppDB().Exec(`UPDATE tax_periods SET status=?, filed_at=?, filing_ref=?, updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`,
		status, stringArg(args, "filed_at", ""), stringArg(args, "filing_ref", ""), projectID(ctx, args), id)
	if err != nil {
		return nil, err
	}
	affected, err := res.RowsAffected()
	if err != nil {
		return nil, err
	}
	if affected == 0 {
		return nil, sql.ErrNoRows
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
	applicable := inferredTaxTypes(profile)
	if len(applicable) == 0 {
		return nil, errors.New("this profile has no applicable tax types")
	}
	allowed := map[string]bool{}
	for _, taxType := range applicable {
		allowed[taxType] = true
	}

	var estimates []any
	if periodID := int64Arg(args, "period_id", 0); periodID > 0 {
		period, err := getPeriod(ctx.AppDB(), profile.ProjectID, periodID)
		if err != nil {
			return nil, err
		}
		taxType := normalizeTaxType(stringFromAny(period["tax_type"]))
		if int64FromAny(period["profile_id"]) != profile.ID || !allowed[taxType] {
			return nil, errors.New("period does not belong to this profile or its active tax regime")
		}
		callArgs := cloneMap(args)
		callArgs["period_start"] = period["period_start"]
		callArgs["period_end"] = period["period_end"]
		if !hasArg(callArgs, "due_date") || stringArg(callArgs, "due_date", "") == "" {
			callArgs["due_date"] = period["due_date"]
		}
		out, err := a.calculate(ctx, callArgs, taxType)
		if err != nil {
			return nil, err
		}
		estimates = append(estimates, out)
		return map[string]any{"profile": profile, "estimates": estimates}, nil
	}

	start := stringArg(args, "period_start", fmt.Sprintf("%04d-01-01", currentYear()))
	end := stringArg(args, "period_end", fmt.Sprintf("%04d-12-31", currentYear()))
	rows, err := ctx.AppDB().Query(`SELECT id,tax_type,period_start,period_end,due_date
		FROM tax_periods
		WHERE project_id=? AND profile_id=? AND status='open' AND period_start>=? AND period_end<=?
		ORDER BY period_start,tax_type`, profile.ProjectID, profile.ID, start, end)
	if err != nil {
		return nil, err
	}
	type estimatePeriod struct {
		id                     int64
		taxType                string
		periodStart, periodEnd string
		dueDate                string
	}
	var selectedPeriods []estimatePeriod
	for rows.Next() {
		var period estimatePeriod
		if err := rows.Scan(&period.id, &period.taxType, &period.periodStart, &period.periodEnd, &period.dueDate); err != nil {
			rows.Close()
			return nil, err
		}
		if !allowed[period.taxType] {
			continue
		}
		selectedPeriods = append(selectedPeriods, period)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return nil, err
	}
	if err := rows.Close(); err != nil {
		return nil, err
	}
	if boolArg(args, "create_obligation", true) {
		for _, period := range selectedPeriods {
			if period.dueDate == "" {
				return nil, fmt.Errorf("%s period ending %s requires a confirmed due date", period.taxType, period.periodEnd)
			}
			if period.taxType == "social_contributions" &&
				!hasArg(args, "social_contribution_cents") &&
				int64FromAny(profile.Config["monthly_social_contribution_cents"]) <= 0 {
				return nil, errors.New("social_contribution_cents or profile config.monthly_social_contribution_cents is required")
			}
			if _, err := findRule(ctx.AppDB(), profile.Country, profile.Structure, period.taxType, yearFromDate(period.periodStart)); err != nil {
				return nil, err
			}
		}
	}
	for _, period := range selectedPeriods {
		callArgs := cloneMap(args)
		callArgs["period_id"] = period.id
		callArgs["period_start"] = period.periodStart
		callArgs["period_end"] = period.periodEnd
		callArgs["due_date"] = period.dueDate
		out, err := a.calculate(ctx, callArgs, period.taxType)
		if err != nil {
			return nil, err
		}
		estimates = append(estimates, out)
	}
	if len(estimates) == 0 {
		return nil, errors.New("no open generated periods found in this range; generate periods first")
	}
	return map[string]any{"profile": profile, "estimates": estimates}, nil
}

func (a *App) calculate(ctx *sdk.AppCtx, args map[string]any, taxType string) (any, error) {
	profile, err := getProfile(ctx.AppDB(), projectID(ctx, args), int64Arg(args, "profile_id", 0))
	if err != nil {
		return nil, err
	}
	taxType = normalizeTaxType(taxType)
	if !containsString(inferredTaxTypes(profile), taxType) {
		return nil, fmt.Errorf("%s is not active for this profile", taxType)
	}
	if taxType == "vat" && !profile.VATRegistered {
		return nil, errors.New("VAT is disabled for this profile")
	}
	periodID := int64Arg(args, "period_id", 0)
	start, end := stringArg(args, "period_start", ""), stringArg(args, "period_end", "")
	requestedDueDate := stringArg(args, "due_date", "")
	dueDate := requestedDueDate
	if periodID > 0 {
		period, err := getPeriod(ctx.AppDB(), profile.ProjectID, periodID)
		if err != nil {
			return nil, err
		}
		if int64FromAny(period["profile_id"]) != profile.ID || normalizeTaxType(stringFromAny(period["tax_type"])) != taxType {
			return nil, errors.New("period does not belong to this profile and tax type")
		}
		status := stringFromAny(period["status"])
		if status != "open" {
			return nil, fmt.Errorf("period is %s; only open periods can be estimated", status)
		}
		start = stringFromAny(period["period_start"])
		end = stringFromAny(period["period_end"])
		dueDate = stringFromAny(period["due_date"])
		if requestedDueDate != "" {
			dueDate = requestedDueDate
		}
	}
	if start == "" || end == "" {
		return nil, errors.New("period_id or period_start and period_end are required")
	}
	if !validISODate(start) || !validISODate(end) || start > end {
		return nil, errors.New("period_start and period_end must be valid ordered YYYY-MM-DD dates")
	}
	if dueDate != "" && !validISODate(dueDate) {
		return nil, errors.New("due_date must use YYYY-MM-DD")
	}
	if periodID == 0 && boolArg(args, "create_obligation", true) {
		err := ctx.AppDB().QueryRow(`SELECT id,due_date FROM tax_periods
			WHERE project_id=? AND profile_id=? AND tax_type=? AND period_start=? AND period_end=? AND status='open'
			LIMIT 1`, profile.ProjectID, profile.ID, taxType, start, end).Scan(&periodID, &dueDate)
		if errors.Is(err, sql.ErrNoRows) {
			return nil, errors.New("create_obligation requires a matching open generated period")
		}
		if err != nil {
			return nil, err
		}
	}
	year := yearFromDate(start)
	rule, err := findRule(ctx.AppDB(), profile.Country, profile.Structure, taxType, year)
	if err != nil {
		return nil, err
	}
	inputArgs := args
	if taxType == "social_contributions" {
		inputArgs = cloneMap(args)
		inputArgs["sync_sources"] = false
	}
	inputs, sources, warnings := a.collectInputs(ctx, inputArgs, profile, start, end)
	outputs := calculateOutputs(taxType, rule, inputs, start, end)
	if warning := stringFromAny(rule.Rules["warning"]); warning != "" {
		warnings = append(warnings, warning)
	}
	if taxType == "social_contributions" && int64FromAny(outputs["monthly_cents"]) <= 0 {
		warnings = append(warnings, "monthly social contribution is missing; set it explicitly before creating an obligation")
	}
	if dueDate == "" {
		warnings = append(warnings, "filing deadline requires confirmation; no due date was assigned")
	}
	for _, adj := range adjustmentsFor(ctx.AppDB(), profile.ProjectID, profile.ID, periodID, taxType) {
		outputs["adjustments_cents"] = int64FromAny(outputs["adjustments_cents"]) + int64FromAny(adj["amount_cents"])
		outputs["estimated_payable_cents"] = int64FromAny(outputs["estimated_payable_cents"]) + int64FromAny(adj["amount_cents"])
	}
	if sources["billing_unavailable"] == true || sources["bills_unavailable"] == true {
		warnings = append(warnings, "cross-app source data was unavailable; estimate may rely on caller-supplied amounts or zero defaults")
	}
	createObligation := boolArg(args, "create_obligation", true)
	if createObligation {
		if dueDate == "" {
			return nil, errors.New("confirm due_date before creating an obligation for this period")
		}
		if taxType == "social_contributions" && int64FromAny(outputs["monthly_cents"]) <= 0 {
			return nil, errors.New("social_contribution_cents is required before creating a social contribution obligation")
		}
	}
	calcID, err := insertCalculation(ctx.AppDB(), profile, periodID, taxType, rule, start, end, inputs, outputs, sources, warnings)
	if err != nil {
		return nil, err
	}
	var obligation *Obligation
	if createObligation {
		obligation, err = upsertEstimatedObligation(ctx.AppDB(), profile, periodID, calcID, taxType, outputs, dueDate)
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
	inputs := map[string]any{"months": intArg(args, "months", monthsBetween(start, end))}
	manualKeys := []string{
		"revenue_cents",
		"expenses_cents",
		"output_tax_cents",
		"input_tax_cents",
		"taxable_profit_cents",
		"social_contribution_cents",
	}
	for _, key := range manualKeys {
		if hasArg(args, key) {
			inputs[key] = int64Arg(args, key, 0)
		}
	}
	if !hasArg(args, "social_contribution_cents") {
		if configured := int64FromAny(profile.Config["monthly_social_contribution_cents"]); configured > 0 {
			inputs["social_contribution_cents"] = configured
		}
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
		if !hasArg(args, "revenue_cents") {
			inputs["revenue_cents"] = billing.RevenueCents
		}
		if !hasArg(args, "output_tax_cents") {
			inputs["output_tax_cents"] = billing.OutputTaxCents
		}
	}
	if bills.Unavailable {
		sources["bills_unavailable"] = true
	} else {
		if !hasArg(args, "expenses_cents") {
			inputs["expenses_cents"] = bills.ExpensesCents
		}
		if !hasArg(args, "input_tax_cents") {
			inputs["input_tax_cents"] = bills.InputTaxCents
		}
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
	o, err := insertObligation(ctx.AppDB(), profile, int64Arg(args, "period_id", 0), 0, normalizeTaxType(stringArg(args, "tax_type", "")), stringArg(args, "title", ""), int64Arg(args, "amount_cents", 0), strings.ToUpper(stringArg(args, "currency", profile.Currency)), stringArg(args, "due_date", ""), stringArg(args, "authority", defaultAuthority(profile, normalizeTaxType(stringArg(args, "tax_type", "")))), stringArg(args, "direction", "payable"), "estimated", mapArg(args, "metadata"))
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
	if hasArg(args, "direction") {
		o.Direction = stringArg(args, "direction", o.Direction)
	}
	if hasArg(args, "status") {
		o.Status = stringArg(args, "status", o.Status)
	}
	if hasArg(args, "metadata") {
		o.Metadata = mapArg(args, "metadata")
	}
	profile, err := getProfile(ctx.AppDB(), o.ProjectID, o.ProfileID)
	if err != nil {
		return nil, err
	}
	if err := validateObligationInput(profile, o.TaxType, o.Direction, o.AmountCents, o.Currency, o.Status); err != nil {
		return nil, err
	}
	_, err = ctx.AppDB().Exec(`UPDATE tax_obligations SET tax_type=?, title=?, amount_cents=?, currency=?, due_date=?, authority=?, direction=?, status=?, metadata_json=?, updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`,
		o.TaxType, o.Title, o.AmountCents, o.Currency, o.DueDate, o.Authority, o.Direction, o.Status, mustJSON(o.Metadata), o.ProjectID, o.ID)
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
	rows, err := ctx.AppDB().Query(`SELECT id,project_id,profile_id,COALESCE(period_id,0),COALESCE(calculation_id,0),tax_type,authority,title,amount_cents,currency,due_date,direction,status,filed_at,filing_ref,waived_reason,metadata_json FROM tax_obligations WHERE `+strings.Join(where, " AND ")+` ORDER BY due_date, id`, vals...)
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
	res, err := ctx.AppDB().Exec(`UPDATE tax_obligations SET status='filed', filed_at=?, filing_ref=?, updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, filedAt, stringArg(args, "filing_ref", ""), projectID(ctx, args), id)
	if err != nil {
		return nil, err
	}
	if changed, _ := res.RowsAffected(); changed == 0 {
		return nil, sql.ErrNoRows
	}
	audit(ctx.AppDB(), projectID(ctx, args), "tax_obligation", id, "filed", "", nil)
	o, err := getObligation(ctx.AppDB(), projectID(ctx, args), id)
	return map[string]any{"obligation": o}, err
}

func (a *App) toolObligationsMarkWaived(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := int64Arg(args, "id", 0)
	reason := strings.TrimSpace(stringArg(args, "reason", ""))
	if reason == "" {
		return nil, errors.New("waiver reason is required")
	}
	res, err := ctx.AppDB().Exec(`UPDATE tax_obligations SET status='waived', waived_reason=?, updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, reason, projectID(ctx, args), id)
	if err != nil {
		return nil, err
	}
	if changed, _ := res.RowsAffected(); changed == 0 {
		return nil, sql.ErrNoRows
	}
	audit(ctx.AppDB(), projectID(ctx, args), "tax_obligation", id, "waived", reason, nil)
	o, err := getObligation(ctx.AppDB(), projectID(ctx, args), id)
	return map[string]any{"obligation": o}, err
}

func (a *App) toolPaymentsRecord(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return a.recordPayment(ctx, args, 0, 0)
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
	o, err := getObligation(ctx.AppDB(), projectID(ctx, args), int64Arg(args, "obligation_id", 0))
	if err != nil {
		return nil, err
	}
	billID := int64Arg(args, "bills_bill_id", 0)
	if billID <= 0 {
		return nil, errors.New("bills_bill_id is required")
	}
	if ctx.PlatformAPI() == nil {
		return nil, errors.New("bills platform API unavailable")
	}
	var billOut map[string]any
	if err := ctx.PlatformAPI().CallAppResult("bills", "bills_get", map[string]any{
		"_project_id": o.ProjectID,
		"id":          billID,
	}, &billOut); err != nil {
		return nil, fmt.Errorf("verify bill: %w", err)
	}
	bill, ok := billOut["bill"].(map[string]any)
	if !ok || bill == nil || !boolArg(billOut, "found", true) {
		return nil, errors.New("bill not found")
	}
	if strings.ToUpper(stringFromAny(bill["currency"])) != o.Currency {
		return nil, errors.New("bill currency does not match the tax obligation")
	}
	if err := setObligationBillLink(ctx.AppDB(), o, billID); err != nil {
		return nil, err
	}
	paymentID := int64Arg(args, "bills_payment_id", 0)
	if paymentID == 0 {
		return map[string]any{
			"obligation_id":    o.ID,
			"bills_bill_id":    billID,
			"linked":           true,
			"payment_recorded": false,
		}, nil
	}
	var verified map[string]any
	for _, payment := range objectRows(bill["payments"]) {
		if int64FromAny(payment["id"]) == paymentID {
			verified = payment
			break
		}
	}
	if verified == nil {
		return nil, errors.New("bills_payment_id does not belong to the supplied bill")
	}
	verifiedArgs := map[string]any{
		"_project_id":   o.ProjectID,
		"obligation_id": o.ID,
		"amount_cents":  int64FromAny(verified["amount_cents"]),
		"currency":      strings.ToUpper(stringFromAny(verified["currency"])),
		"paid_at":       stringFromAny(verified["sent_at"]),
		"method":        stringFromAny(verified["method"]),
		"reference":     stringFromAny(verified["external_id"]),
		"notes":         "Verified from Bills payment",
	}
	return a.recordPayment(ctx, verifiedArgs, billID, paymentID)
}

func (a *App) recordPayment(ctx *sdk.AppCtx, args map[string]any, billID, paymentID int64) (any, error) {
	o, err := getObligation(ctx.AppDB(), projectID(ctx, args), int64Arg(args, "obligation_id", 0))
	if err != nil {
		return nil, err
	}
	if o.Direction != "payable" {
		return nil, errors.New("payments can only be recorded against payable obligations")
	}
	amount := int64Arg(args, "amount_cents", 0)
	if amount <= 0 {
		return nil, errors.New("amount_cents must be positive")
	}
	currency := strings.ToUpper(stringArg(args, "currency", o.Currency))
	if currency != o.Currency {
		return nil, errors.New("payment currency must match the obligation")
	}
	var alreadyPaid int64
	if err := ctx.AppDB().QueryRow(`SELECT COALESCE(SUM(amount_cents),0) FROM tax_payments WHERE project_id=? AND obligation_id=?`, o.ProjectID, o.ID).Scan(&alreadyPaid); err != nil {
		return nil, err
	}
	remaining := o.AmountCents - alreadyPaid
	if amount > remaining {
		return nil, fmt.Errorf("payment exceeds remaining obligation amount of %d cents", remaining)
	}
	paidAt := stringArg(args, "paid_at", time.Now().UTC().Format(time.RFC3339))
	res, err := ctx.AppDB().Exec(`INSERT INTO tax_payments (project_id,obligation_id,amount_cents,currency,paid_at,method,reference,bills_bill_id,bills_payment_id,notes,metadata_json) VALUES (?,?,?,?,?,?,?,?,?,?,?)`,
		o.ProjectID, o.ID, amount, currency, paidAt, stringArg(args, "method", ""), stringArg(args, "reference", ""), nullInt(billID), nullInt(paymentID), stringArg(args, "notes", ""), mustJSON(mapArg(args, "metadata")))
	if err != nil {
		if paymentID > 0 && strings.Contains(strings.ToLower(err.Error()), "unique") {
			return nil, errors.New("this Bills payment is already linked")
		}
		return nil, err
	}
	id, _ := res.LastInsertId()
	newPaid := alreadyPaid + amount
	if newPaid >= o.AmountCents {
		if _, err := ctx.AppDB().Exec(`UPDATE tax_obligations SET status='paid', updated_at=CURRENT_TIMESTAMP WHERE project_id=? AND id=?`, o.ProjectID, o.ID); err != nil {
			return nil, err
		}
	}
	audit(ctx.AppDB(), o.ProjectID, "tax_obligation", o.ID, "payment_recorded", "", map[string]any{"payment_id": id, "amount_cents": amount})
	return map[string]any{
		"payment_id":      id,
		"obligation_id":   o.ID,
		"paid_cents":      newPaid,
		"remaining_cents": max64(0, o.AmountCents-newPaid),
	}, nil
}

func (a *App) toolPaymentsCreateBill(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	o, err := getObligation(ctx.AppDB(), projectID(ctx, args), int64Arg(args, "obligation_id", 0))
	if err != nil {
		return nil, err
	}
	if o.Direction != "payable" || o.AmountCents <= 0 {
		return nil, errors.New("a Bills payable can only be created for a positive payable obligation")
	}
	if existing := int64FromAny(o.Metadata["bills_bill_id"]); existing > 0 {
		return map[string]any{"obligation": o, "bills_bill_id": existing, "already_linked": true}, nil
	}
	if ctx.PlatformAPI() == nil {
		return nil, errors.New("bills platform API unavailable")
	}
	vendorName := stringArg(args, "vendor_name", o.Authority)
	if vendorName == "" {
		vendorName = "Tax authority"
	}
	var vendorOut map[string]any
	if err := ctx.PlatformAPI().CallAppResult("bills", "vendors_upsert_by_email", map[string]any{
		"_project_id": o.ProjectID,
		"email":       stringArg(args, "vendor_email", fmt.Sprintf("tax-authority-%s-%s@apteva.invalid", strings.ToLower(profileCountryForObligation(ctx.AppDB(), o)), strings.ReplaceAll(o.TaxType, "_", "-"))),
		"defaults": map[string]any{
			"name": vendorName,
		},
	}, &vendorOut); err != nil {
		return nil, fmt.Errorf("create/link vendor in bills: %w", err)
	}
	vendorID := findNestedInt(vendorOut, "vendor", "id")
	if vendorID <= 0 {
		return nil, errors.New("Bills did not return a vendor id")
	}
	var billOut map[string]any
	if err := ctx.PlatformAPI().CallAppResult("bills", "bills_create", map[string]any{
		"_project_id":           o.ProjectID,
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
	if billID <= 0 {
		return nil, errors.New("Bills did not return a bill id")
	}
	if err := setObligationBillLink(ctx.AppDB(), o, billID); err != nil {
		return nil, err
	}
	audit(ctx.AppDB(), o.ProjectID, "tax_obligation", o.ID, "bill_created", "", map[string]any{"bills_bill_id": billID})
	updated, err := getObligation(ctx.AppDB(), o.ProjectID, o.ID)
	if err != nil {
		return nil, err
	}
	return map[string]any{"obligation": updated, "bills_bill_id": billID, "bills_response": billOut}, nil
}

func (a *App) toolAdjustmentsCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	profile, err := getProfile(ctx.AppDB(), projectID(ctx, args), int64Arg(args, "profile_id", 0))
	if err != nil {
		return nil, err
	}
	taxType := normalizeTaxType(stringArg(args, "tax_type", ""))
	if err := validateTaxType(taxType); err != nil {
		return nil, err
	}
	if !containsString(inferredTaxTypes(profile), taxType) {
		return nil, fmt.Errorf("%s is not active for this profile", taxType)
	}
	kind := strings.TrimSpace(stringArg(args, "kind", ""))
	reason := strings.TrimSpace(stringArg(args, "reason", ""))
	amount := int64Arg(args, "amount_cents", 0)
	currency := strings.ToUpper(stringArg(args, "currency", profile.Currency))
	if kind == "" {
		return nil, errors.New("kind is required")
	}
	if reason == "" {
		return nil, errors.New("reason is required")
	}
	if amount == 0 {
		return nil, errors.New("amount_cents cannot be zero")
	}
	if currency != profile.Currency {
		return nil, fmt.Errorf("currency %s does not match profile currency %s", currency, profile.Currency)
	}
	periodID := int64Arg(args, "period_id", 0)
	if periodID > 0 {
		period, err := getPeriod(ctx.AppDB(), profile.ProjectID, periodID)
		if err != nil {
			return nil, err
		}
		if int64FromAny(period["profile_id"]) != profile.ID || normalizeTaxType(stringFromAny(period["tax_type"])) != taxType {
			return nil, errors.New("period does not belong to this profile and tax type")
		}
	}
	res, err := ctx.AppDB().Exec(`INSERT INTO tax_adjustments (project_id,profile_id,period_id,tax_type,kind,amount_cents,currency,reason,metadata_json) VALUES (?,?,?,?,?,?,?,?,?)`,
		profile.ProjectID, profile.ID, nullInt(periodID), taxType, kind, amount, currency, reason, mustJSON(mapArg(args, "metadata")))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	audit(ctx.AppDB(), profile.ProjectID, "tax_adjustment", id, "created", reason, nil)
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
	periodID := int64Arg(args, "period_id", 0)
	start := stringArg(args, "period_start", "")
	end := stringArg(args, "period_end", "")
	taxTypes := stringSliceArg(args, "tax_types")
	for _, taxType := range taxTypes {
		if err := validateTaxType(taxType); err != nil {
			return nil, err
		}
	}
	report := map[string]any{
		"profile":      profile,
		"generated_at": time.Now().UTC().Format(time.RFC3339),
		"scope": map[string]any{
			"period_id":    periodID,
			"period_start": start,
			"period_end":   end,
			"tax_types":    taxTypes,
		},
	}
	if periodID > 0 {
		period, err := getPeriod(ctx.AppDB(), profile.ProjectID, periodID)
		if err != nil {
			return nil, err
		}
		if int64FromAny(period["profile_id"]) != profile.ID {
			return nil, errors.New("period does not belong to this profile")
		}
		start = stringFromAny(period["period_start"])
		end = stringFromAny(period["period_end"])
		report["period"] = period
	}
	obligations, err := reportObligations(ctx.AppDB(), profile, periodID, start, end, taxTypes)
	if err != nil {
		return nil, err
	}
	adjustments, err := reportAdjustments(ctx.AppDB(), profile, periodID, start, end, taxTypes)
	if err != nil {
		return nil, err
	}
	report["obligations"] = obligations
	report["adjustments"] = adjustments
	res, err := ctx.AppDB().Exec(`INSERT INTO tax_documents (project_id,profile_id,period_id,document_type,title,content_json) VALUES (?,?,?,?,?,?)`,
		profile.ProjectID, profile.ID, nullInt(periodID), "report", reportTitle(profile, periodID, start, end), mustJSON(report))
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
