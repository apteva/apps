// Partner Program v0.1.0 — merchant-side referral and affiliate MVP.
//
// This app is intentionally small: five tables, manual payout tracking,
// and a commission ledger derived from recorded referrals. Later versions
// can subscribe to checkout/billing/subscription events and automate
// branded redirects or payouts without changing the core model.
package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: partner-program
display_name: Partner Program
version: 0.1.0
description: Merchant-side referral and affiliate program.
author: Apteva
scopes: [project, global]
requires:
  permissions:
    - db.write.app
    - platform.apps.call
  apps:
    - name: crm
      optional: true
      reason: Partners and referred customers can link back to CRM contacts.
    - name: redirects
      optional: true
      reason: Future versions can mint branded short links automatically.
    - name: billing
      optional: true
      reason: Future versions can create commissions from paid invoices.
    - name: checkout
      optional: true
      reason: Future versions can attribute checkout sessions automatically.
    - name: subscriptions
      optional: true
      reason: Future versions can create recurring commissions from subscription renewals.
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: partners_create, description: "Create a referral/affiliate partner." }
    - { name: partners_search, description: "Search partners." }
    - { name: partners_get, description: "Fetch one partner." }
    - { name: partners_update, description: "Partial-patch a partner." }
    - { name: campaigns_create, description: "Create a campaign." }
    - { name: campaigns_search, description: "Search campaigns." }
    - { name: referral_link_create, description: "Create a referral link/code." }
    - { name: referral_links_search, description: "Search referral links." }
    - { name: referrals_record, description: "Record a lead or conversion." }
    - { name: referrals_search, description: "Search referrals." }
    - { name: commissions_search, description: "Search commissions." }
    - { name: commissions_update, description: "Update commission status/payment fields." }
    - { name: program_stats, description: "Return program counts and totals." }
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/partner-program
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/partner-program.db
  migrations: migrations/
upgrade_policy: auto-patch
`

type App struct{}

var globalCtx *sdk.AppCtx

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest([]byte(manifestYAML))
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("partner-program requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("partner-program mounted", "version", "0.1.0")
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func main() { sdk.Run(&App{}) }

// ─── HTTP ───────────────────────────────────────────────────────────

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/partners", Handler: a.handlePartners},
		{Pattern: "/partners/", Handler: a.handlePartnerItem},
		{Pattern: "/campaigns", Handler: a.handleCampaigns},
		{Pattern: "/referral-links", Handler: a.handleReferralLinks},
		{Pattern: "/referrals", Handler: a.handleReferrals},
		{Pattern: "/commissions", Handler: a.handleCommissions},
		{Pattern: "/commissions/", Handler: a.handleCommissionItem},
		{Pattern: "/stats", Handler: a.handleStats},
	}
}

func (a *App) handlePartners(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := projectFromRequest(r, ctx)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		rows, err := dbPartnersSearch(ctx.AppDB(), pid, filtersFromRequest(r))
		writeHTTPResult(w, map[string]any{"partners": rows, "count": len(rows)}, err)
	case http.MethodPost:
		var body map[string]any
		if !decodeBody(w, r, &body) {
			return
		}
		row, err := dbPartnerCreate(ctx.AppDB(), pid, body)
		writeHTTPResult(w, map[string]any{"partner": row}, err)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handlePartnerItem(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := projectFromRequest(r, ctx)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	id := pathID(r.URL.Path, "/partners/")
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	switch r.Method {
	case http.MethodGet:
		row, err := dbPartnerGet(ctx.AppDB(), pid, id)
		if err == nil && row == nil {
			httpErr(w, http.StatusNotFound, "partner not found")
			return
		}
		writeHTTPResult(w, map[string]any{"partner": row}, err)
	case http.MethodPatch:
		var patch map[string]any
		if !decodeBody(w, r, &patch) {
			return
		}
		row, err := dbPartnerUpdate(ctx.AppDB(), pid, id, patch)
		writeHTTPResult(w, map[string]any{"partner": row}, err)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleCampaigns(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := projectFromRequest(r, ctx)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		rows, err := dbCampaignsSearch(ctx.AppDB(), pid, filtersFromRequest(r))
		writeHTTPResult(w, map[string]any{"campaigns": rows, "count": len(rows)}, err)
	case http.MethodPost:
		var body map[string]any
		if !decodeBody(w, r, &body) {
			return
		}
		row, err := dbCampaignCreate(ctx.AppDB(), pid, body)
		writeHTTPResult(w, map[string]any{"campaign": row}, err)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleReferralLinks(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := projectFromRequest(r, ctx)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		rows, err := dbReferralLinksSearch(ctx.AppDB(), pid, filtersFromRequest(r))
		writeHTTPResult(w, map[string]any{"referral_links": rows, "count": len(rows)}, err)
	case http.MethodPost:
		var body map[string]any
		if !decodeBody(w, r, &body) {
			return
		}
		row, err := dbReferralLinkCreate(ctx.AppDB(), pid, body)
		writeHTTPResult(w, map[string]any{"referral_link": row}, err)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleReferrals(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := projectFromRequest(r, ctx)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	switch r.Method {
	case http.MethodGet:
		rows, err := dbReferralsSearch(ctx.AppDB(), pid, filtersFromRequest(r))
		writeHTTPResult(w, map[string]any{"referrals": rows, "count": len(rows)}, err)
	case http.MethodPost:
		var body map[string]any
		if !decodeBody(w, r, &body) {
			return
		}
		row, commission, err := dbReferralRecord(ctx.AppDB(), pid, body)
		writeHTTPResult(w, map[string]any{"referral": row, "commission": commission}, err)
	default:
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (a *App) handleCommissions(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := projectFromRequest(r, ctx)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if r.Method != http.MethodGet {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	rows, err := dbCommissionsSearch(ctx.AppDB(), pid, filtersFromRequest(r))
	writeHTTPResult(w, map[string]any{"commissions": rows, "count": len(rows)}, err)
}

func (a *App) handleCommissionItem(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := projectFromRequest(r, ctx)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if r.Method != http.MethodPatch {
		httpErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	id := pathID(r.URL.Path, "/commissions/")
	if id == 0 {
		httpErr(w, http.StatusBadRequest, "id required")
		return
	}
	var patch map[string]any
	if !decodeBody(w, r, &patch) {
		return
	}
	row, err := dbCommissionUpdate(ctx.AppDB(), pid, id, patch)
	writeHTTPResult(w, map[string]any{"commission": row}, err)
}

func (a *App) handleStats(w http.ResponseWriter, r *http.Request) {
	ctx := getAppCtx(r)
	pid, err := projectFromRequest(r, ctx)
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	stats, err := dbProgramStats(ctx.AppDB(), pid)
	writeHTTPResult(w, stats, err)
}

// ─── MCP tools ──────────────────────────────────────────────────────

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "partners_create",
			Description: "Create a partner. Args: name, email?, type? (customer|affiliate|agency|reseller|internal), status?, crm_contact_id?, payout_email?, metadata?.",
			InputSchema: schemaObject(map[string]any{
				"name":           map[string]any{"type": "string"},
				"email":          map[string]any{"type": "string"},
				"type":           map[string]any{"type": "string"},
				"status":         map[string]any{"type": "string"},
				"crm_contact_id": map[string]any{"type": "integer"},
				"payout_email":   map[string]any{"type": "string"},
				"metadata":       map[string]any{"type": "object"},
			}, []string{"name"}),
			Handler: func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
				pid, err := projectFromArgs(ctx, args)
				if err != nil {
					return nil, err
				}
				row, err := dbPartnerCreate(ctx.AppDB(), pid, args)
				return map[string]any{"partner": row}, err
			},
		},
		{
			Name:        "partners_search",
			Description: "Search partners. Args: q?, type?, status?, limit? (default 50, max 200).",
			InputSchema: schemaObject(filterSchema("type", "status"), nil),
			Handler: func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
				pid, err := projectFromArgs(ctx, args)
				if err != nil {
					return nil, err
				}
				rows, err := dbPartnersSearch(ctx.AppDB(), pid, filtersFromArgs(args))
				return map[string]any{"partners": rows, "count": len(rows)}, err
			},
		},
		{
			Name:        "partners_get",
			Description: "Fetch one partner. Args: id.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler: func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
				pid, err := projectFromArgs(ctx, args)
				if err != nil {
					return nil, err
				}
				row, err := dbPartnerGet(ctx.AppDB(), pid, int64Arg(args, "id"))
				if err == nil && row == nil {
					err = errors.New("partner not found")
				}
				return map[string]any{"partner": row}, err
			},
		},
		{
			Name:        "partners_update",
			Description: "Partial-patch a partner. Args: id, patch.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}, "patch": map[string]any{"type": "object"}}, []string{"id", "patch"}),
			Handler: func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
				pid, err := projectFromArgs(ctx, args)
				if err != nil {
					return nil, err
				}
				patch, _ := args["patch"].(map[string]any)
				row, err := dbPartnerUpdate(ctx.AppDB(), pid, int64Arg(args, "id"), patch)
				return map[string]any{"partner": row}, err
			},
		},
		{
			Name:        "campaigns_create",
			Description: "Create a campaign. Args: name, slug?, destination_url?, status?, default_commission_type? (none|fixed|percent), default_commission_value?, cookie_days?, metadata?.",
			InputSchema: schemaObject(map[string]any{
				"name":                     map[string]any{"type": "string"},
				"slug":                     map[string]any{"type": "string"},
				"destination_url":          map[string]any{"type": "string"},
				"status":                   map[string]any{"type": "string"},
				"default_commission_type":  map[string]any{"type": "string"},
				"default_commission_value": map[string]any{"type": "number"},
				"cookie_days":              map[string]any{"type": "integer"},
				"metadata":                 map[string]any{"type": "object"},
			}, []string{"name"}),
			Handler: func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
				pid, err := projectFromArgs(ctx, args)
				if err != nil {
					return nil, err
				}
				row, err := dbCampaignCreate(ctx.AppDB(), pid, args)
				return map[string]any{"campaign": row}, err
			},
		},
		{
			Name:        "campaigns_search",
			Description: "Search campaigns. Args: q?, status?, limit?.",
			InputSchema: schemaObject(filterSchema("status"), nil),
			Handler: func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
				pid, err := projectFromArgs(ctx, args)
				if err != nil {
					return nil, err
				}
				rows, err := dbCampaignsSearch(ctx.AppDB(), pid, filtersFromArgs(args))
				return map[string]any{"campaigns": rows, "count": len(rows)}, err
			},
		},
		{
			Name:        "referral_link_create",
			Description: "Create a referral link/code. Args: partner_id, campaign_id, code?, short_url?, destination_url?, status?, metadata?.",
			InputSchema: schemaObject(map[string]any{
				"partner_id":      map[string]any{"type": "integer"},
				"campaign_id":     map[string]any{"type": "integer"},
				"code":            map[string]any{"type": "string"},
				"short_url":       map[string]any{"type": "string"},
				"destination_url": map[string]any{"type": "string"},
				"status":          map[string]any{"type": "string"},
				"metadata":        map[string]any{"type": "object"},
			}, []string{"partner_id", "campaign_id"}),
			Handler: func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
				pid, err := projectFromArgs(ctx, args)
				if err != nil {
					return nil, err
				}
				row, err := dbReferralLinkCreate(ctx.AppDB(), pid, args)
				return map[string]any{"referral_link": row}, err
			},
		},
		{
			Name:        "referral_links_search",
			Description: "Search referral links. Args: q?, partner_id?, campaign_id?, status?, limit?.",
			InputSchema: schemaObject(filterSchema("partner_id", "campaign_id", "status"), nil),
			Handler: func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
				pid, err := projectFromArgs(ctx, args)
				if err != nil {
					return nil, err
				}
				rows, err := dbReferralLinksSearch(ctx.AppDB(), pid, filtersFromArgs(args))
				return map[string]any{"referral_links": rows, "count": len(rows)}, err
			},
		},
		{
			Name:        "referrals_record",
			Description: "Record a lead or conversion. Args: partner_id? or referral_link_id/code, campaign_id?, customer_email?, status? (lead|converted|...), amount_cents?, currency?, create_commission? (default true for converted amount), source_event?, metadata?.",
			InputSchema: schemaObject(map[string]any{
				"partner_id":               map[string]any{"type": "integer"},
				"campaign_id":              map[string]any{"type": "integer"},
				"referral_link_id":         map[string]any{"type": "integer"},
				"code":                     map[string]any{"type": "string"},
				"crm_contact_id":           map[string]any{"type": "integer"},
				"customer_email":           map[string]any{"type": "string"},
				"external_customer_id":     map[string]any{"type": "string"},
				"external_order_id":        map[string]any{"type": "string"},
				"external_subscription_id": map[string]any{"type": "string"},
				"status":                   map[string]any{"type": "string"},
				"amount_cents":             map[string]any{"type": "integer"},
				"currency":                 map[string]any{"type": "string"},
				"create_commission":        map[string]any{"type": "boolean"},
				"source_event":             map[string]any{"type": "object"},
				"metadata":                 map[string]any{"type": "object"},
			}, nil),
			Handler: func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
				pid, err := projectFromArgs(ctx, args)
				if err != nil {
					return nil, err
				}
				row, commission, err := dbReferralRecord(ctx.AppDB(), pid, args)
				return map[string]any{"referral": row, "commission": commission}, err
			},
		},
		{
			Name:        "referrals_search",
			Description: "Search referrals. Args: q?, partner_id?, campaign_id?, status?, limit?.",
			InputSchema: schemaObject(filterSchema("partner_id", "campaign_id", "status"), nil),
			Handler: func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
				pid, err := projectFromArgs(ctx, args)
				if err != nil {
					return nil, err
				}
				rows, err := dbReferralsSearch(ctx.AppDB(), pid, filtersFromArgs(args))
				return map[string]any{"referrals": rows, "count": len(rows)}, err
			},
		},
		{
			Name:        "commissions_search",
			Description: "Search commissions. Args: partner_id?, status?, payout_batch?, limit?.",
			InputSchema: schemaObject(filterSchema("partner_id", "status", "payout_batch"), nil),
			Handler: func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
				pid, err := projectFromArgs(ctx, args)
				if err != nil {
					return nil, err
				}
				rows, err := dbCommissionsSearch(ctx.AppDB(), pid, filtersFromArgs(args))
				return map[string]any{"commissions": rows, "count": len(rows)}, err
			},
		},
		{
			Name:        "commissions_update",
			Description: "Update commission status/payment fields. Args: id, status? (pending|approved|rejected|void|paid), payout_batch?, reason?, metadata?.",
			InputSchema: schemaObject(map[string]any{
				"id":           map[string]any{"type": "integer"},
				"status":       map[string]any{"type": "string"},
				"payout_batch": map[string]any{"type": "string"},
				"reason":       map[string]any{"type": "string"},
				"metadata":     map[string]any{"type": "object"},
			}, []string{"id"}),
			Handler: func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
				pid, err := projectFromArgs(ctx, args)
				if err != nil {
					return nil, err
				}
				row, err := dbCommissionUpdate(ctx.AppDB(), pid, int64Arg(args, "id"), args)
				return map[string]any{"commission": row}, err
			},
		},
		{
			Name:        "program_stats",
			Description: "Return high-level partner program counts and commission totals.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Handler: func(ctx *sdk.AppCtx, args map[string]any) (any, error) {
				pid, err := projectFromArgs(ctx, args)
				if err != nil {
					return nil, err
				}
				return dbProgramStats(ctx.AppDB(), pid)
			},
		},
	}
}

// ─── Models ────────────────────────────────────────────────────────

type Partner struct {
	ID           int64           `json:"id"`
	ProjectID    string          `json:"project_id"`
	CRMContactID int64           `json:"crm_contact_id"`
	Name         string          `json:"name"`
	Email        string          `json:"email"`
	Type         string          `json:"type"`
	Status       string          `json:"status"`
	PayoutEmail  string          `json:"payout_email"`
	Metadata     json.RawMessage `json:"metadata"`
	CreatedAt    string          `json:"created_at"`
	UpdatedAt    string          `json:"updated_at"`
}

type Campaign struct {
	ID                     int64           `json:"id"`
	ProjectID              string          `json:"project_id"`
	Name                   string          `json:"name"`
	Slug                   string          `json:"slug"`
	DestinationURL         string          `json:"destination_url"`
	Status                 string          `json:"status"`
	DefaultCommissionType  string          `json:"default_commission_type"`
	DefaultCommissionValue float64         `json:"default_commission_value"`
	CookieDays             int             `json:"cookie_days"`
	Metadata               json.RawMessage `json:"metadata"`
	CreatedAt              string          `json:"created_at"`
	UpdatedAt              string          `json:"updated_at"`
}

type ReferralLink struct {
	ID             int64           `json:"id"`
	ProjectID      string          `json:"project_id"`
	PartnerID      int64           `json:"partner_id"`
	CampaignID     int64           `json:"campaign_id"`
	Code           string          `json:"code"`
	ShortURL       string          `json:"short_url"`
	DestinationURL string          `json:"destination_url"`
	Status         string          `json:"status"`
	Clicks         int64           `json:"clicks"`
	Conversions    int64           `json:"conversions"`
	Metadata       json.RawMessage `json:"metadata"`
	CreatedAt      string          `json:"created_at"`
	UpdatedAt      string          `json:"updated_at"`
}

type Referral struct {
	ID                     int64           `json:"id"`
	ProjectID              string          `json:"project_id"`
	PartnerID              int64           `json:"partner_id"`
	CampaignID             int64           `json:"campaign_id"`
	ReferralLinkID         int64           `json:"referral_link_id,omitempty"`
	CRMContactID           int64           `json:"crm_contact_id"`
	CustomerEmail          string          `json:"customer_email"`
	ExternalCustomerID     string          `json:"external_customer_id"`
	ExternalOrderID        string          `json:"external_order_id"`
	ExternalSubscriptionID string          `json:"external_subscription_id"`
	Status                 string          `json:"status"`
	AmountCents            int64           `json:"amount_cents"`
	Currency               string          `json:"currency"`
	SourceEvent            json.RawMessage `json:"source_event"`
	Metadata               json.RawMessage `json:"metadata"`
	CreatedAt              string          `json:"created_at"`
	ConvertedAt            string          `json:"converted_at"`
	UpdatedAt              string          `json:"updated_at"`
}

type Commission struct {
	ID          int64           `json:"id"`
	ProjectID   string          `json:"project_id"`
	PartnerID   int64           `json:"partner_id"`
	ReferralID  int64           `json:"referral_id"`
	Status      string          `json:"status"`
	AmountCents int64           `json:"amount_cents"`
	Currency    string          `json:"currency"`
	Reason      string          `json:"reason"`
	EligibleAt  string          `json:"eligible_at"`
	ApprovedAt  string          `json:"approved_at"`
	PaidAt      string          `json:"paid_at"`
	PayoutBatch string          `json:"payout_batch"`
	Metadata    json.RawMessage `json:"metadata"`
	CreatedAt   string          `json:"created_at"`
	UpdatedAt   string          `json:"updated_at"`
}

type filters struct {
	Query       string
	Type        string
	Status      string
	PayoutBatch string
	PartnerID   int64
	CampaignID  int64
	Limit       int
}

// ─── DB: partners ──────────────────────────────────────────────────

func dbPartnerCreate(db *sql.DB, pid string, args map[string]any) (*Partner, error) {
	name := strings.TrimSpace(strArg(args, "name"))
	if name == "" {
		return nil, errors.New("name required")
	}
	typ := defaultString(strArg(args, "type"), "affiliate")
	if !validPartnerTypes[typ] {
		return nil, fmt.Errorf("type must be one of customer, affiliate, agency, reseller, internal")
	}
	status := defaultString(strArg(args, "status"), "pending")
	if !validPartnerStatuses[status] {
		return nil, fmt.Errorf("status must be one of pending, approved, rejected, suspended")
	}
	meta := jsonOrEmpty(args["metadata"], "{}")
	res, err := db.Exec(
		`INSERT INTO partners
		   (project_id, crm_contact_id, name, email, type, status, payout_email, metadata)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, int64Arg(args, "crm_contact_id"), name, strings.ToLower(strings.TrimSpace(strArg(args, "email"))),
		typ, status, strings.ToLower(strings.TrimSpace(strArg(args, "payout_email"))), meta)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return dbPartnerGet(db, pid, id)
}

func dbPartnersSearch(db *sql.DB, pid string, f filters) ([]*Partner, error) {
	limit := clampLimit(f.Limit, 50, 200)
	where := []string{"project_id = ?"}
	args := []any{pid}
	if f.Query != "" {
		where = append(where, "(name LIKE ? OR email LIKE ?)")
		like := "%" + f.Query + "%"
		args = append(args, like, like)
	}
	if f.Type != "" {
		where = append(where, "type = ?")
		args = append(args, f.Type)
	}
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	args = append(args, limit)
	rows, err := db.Query(
		`SELECT id, project_id, crm_contact_id, name, email, type, status, payout_email, metadata, created_at, updated_at
		 FROM partners WHERE `+strings.Join(where, " AND ")+` ORDER BY updated_at DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Partner
	for rows.Next() {
		row, err := scanPartner(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func dbPartnerGet(db *sql.DB, pid string, id int64) (*Partner, error) {
	row := db.QueryRow(
		`SELECT id, project_id, crm_contact_id, name, email, type, status, payout_email, metadata, created_at, updated_at
		 FROM partners WHERE project_id = ? AND id = ?`, pid, id)
	p, err := scanPartner(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return p, err
}

func dbPartnerUpdate(db *sql.DB, pid string, id int64, patch map[string]any) (*Partner, error) {
	if id == 0 {
		return nil, errors.New("id required")
	}
	sets, args, err := buildPatch(patch, map[string]patchField{
		"crm_contact_id": {Column: "crm_contact_id", Kind: "int"},
		"name":           {Column: "name", Kind: "string_nonempty"},
		"email":          {Column: "email", Kind: "lower"},
		"type":           {Column: "type", Kind: "enum_partner_type"},
		"status":         {Column: "status", Kind: "enum_partner_status"},
		"payout_email":   {Column: "payout_email", Kind: "lower"},
		"metadata":       {Column: "metadata", Kind: "json"},
	})
	if err != nil {
		return nil, err
	}
	if len(sets) > 0 {
		sets = append(sets, "updated_at = CURRENT_TIMESTAMP")
		args = append(args, pid, id)
		if _, err := db.Exec(`UPDATE partners SET `+strings.Join(sets, ", ")+` WHERE project_id = ? AND id = ?`, args...); err != nil {
			return nil, err
		}
	}
	return dbPartnerGet(db, pid, id)
}

func scanPartner(s rowScanner) (*Partner, error) {
	var p Partner
	var meta string
	if err := s.Scan(&p.ID, &p.ProjectID, &p.CRMContactID, &p.Name, &p.Email, &p.Type, &p.Status, &p.PayoutEmail, &meta, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	p.Metadata = rawJSON(meta)
	return &p, nil
}

// ─── DB: campaigns ─────────────────────────────────────────────────

func dbCampaignCreate(db *sql.DB, pid string, args map[string]any) (*Campaign, error) {
	name := strings.TrimSpace(strArg(args, "name"))
	if name == "" {
		return nil, errors.New("name required")
	}
	slug := slugify(defaultString(strArg(args, "slug"), name))
	status := defaultString(strArg(args, "status"), "active")
	if !validCampaignStatuses[status] {
		return nil, fmt.Errorf("status must be one of draft, active, paused, archived")
	}
	commissionType := defaultString(strArg(args, "default_commission_type"), "percent")
	if !validCommissionTypes[commissionType] {
		return nil, fmt.Errorf("default_commission_type must be one of none, fixed, percent")
	}
	commissionValue := floatArg(args, "default_commission_value", 20)
	if commissionValue < 0 {
		return nil, errors.New("default_commission_value must be >= 0")
	}
	cookieDays := intArg(args, "cookie_days", 60)
	if cookieDays < 0 {
		return nil, errors.New("cookie_days must be >= 0")
	}
	res, err := db.Exec(
		`INSERT INTO campaigns
		   (project_id, name, slug, destination_url, status, default_commission_type, default_commission_value, cookie_days, metadata)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, name, slug, strArg(args, "destination_url"), status, commissionType, commissionValue, cookieDays, jsonOrEmpty(args["metadata"], "{}"))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return dbCampaignGet(db, pid, id)
}

func dbCampaignsSearch(db *sql.DB, pid string, f filters) ([]*Campaign, error) {
	limit := clampLimit(f.Limit, 50, 200)
	where := []string{"project_id = ?"}
	args := []any{pid}
	if f.Query != "" {
		where = append(where, "(name LIKE ? OR slug LIKE ?)")
		like := "%" + f.Query + "%"
		args = append(args, like, like)
	}
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	args = append(args, limit)
	rows, err := db.Query(
		`SELECT id, project_id, name, slug, destination_url, status, default_commission_type,
		        default_commission_value, cookie_days, metadata, created_at, updated_at
		 FROM campaigns WHERE `+strings.Join(where, " AND ")+` ORDER BY updated_at DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Campaign
	for rows.Next() {
		row, err := scanCampaign(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func dbCampaignGet(db *sql.DB, pid string, id int64) (*Campaign, error) {
	row := db.QueryRow(
		`SELECT id, project_id, name, slug, destination_url, status, default_commission_type,
		        default_commission_value, cookie_days, metadata, created_at, updated_at
		 FROM campaigns WHERE project_id = ? AND id = ?`, pid, id)
	c, err := scanCampaign(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

func scanCampaign(s rowScanner) (*Campaign, error) {
	var c Campaign
	var meta string
	if err := s.Scan(&c.ID, &c.ProjectID, &c.Name, &c.Slug, &c.DestinationURL, &c.Status, &c.DefaultCommissionType, &c.DefaultCommissionValue, &c.CookieDays, &meta, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	c.Metadata = rawJSON(meta)
	return &c, nil
}

// ─── DB: referral links ────────────────────────────────────────────

func dbReferralLinkCreate(db *sql.DB, pid string, args map[string]any) (*ReferralLink, error) {
	partnerID := int64Arg(args, "partner_id")
	campaignID := int64Arg(args, "campaign_id")
	if partnerID == 0 || campaignID == 0 {
		return nil, errors.New("partner_id and campaign_id required")
	}
	partner, err := dbPartnerGet(db, pid, partnerID)
	if err != nil {
		return nil, err
	}
	if partner == nil {
		return nil, errors.New("partner not found")
	}
	campaign, err := dbCampaignGet(db, pid, campaignID)
	if err != nil {
		return nil, err
	}
	if campaign == nil {
		return nil, errors.New("campaign not found")
	}
	status := defaultString(strArg(args, "status"), "active")
	if !validLinkStatuses[status] {
		return nil, fmt.Errorf("status must be one of active, paused, archived")
	}
	code := slugify(defaultString(strArg(args, "code"), partner.Name+"-"+campaign.Slug))
	destination := defaultString(strArg(args, "destination_url"), campaign.DestinationURL)
	shortURL := strArg(args, "short_url")
	res, err := db.Exec(
		`INSERT INTO referral_links
		   (project_id, partner_id, campaign_id, code, short_url, destination_url, status, metadata)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?)`,
		pid, partnerID, campaignID, code, shortURL, destination, status, jsonOrEmpty(args["metadata"], "{}"))
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return dbReferralLinkGet(db, pid, id)
}

func dbReferralLinksSearch(db *sql.DB, pid string, f filters) ([]*ReferralLink, error) {
	limit := clampLimit(f.Limit, 50, 200)
	where := []string{"project_id = ?"}
	args := []any{pid}
	if f.Query != "" {
		where = append(where, "(code LIKE ? OR short_url LIKE ? OR destination_url LIKE ?)")
		like := "%" + f.Query + "%"
		args = append(args, like, like, like)
	}
	if f.PartnerID != 0 {
		where = append(where, "partner_id = ?")
		args = append(args, f.PartnerID)
	}
	if f.CampaignID != 0 {
		where = append(where, "campaign_id = ?")
		args = append(args, f.CampaignID)
	}
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	args = append(args, limit)
	rows, err := db.Query(
		`SELECT id, project_id, partner_id, campaign_id, code, short_url, destination_url, status,
		        clicks, conversions, metadata, created_at, updated_at
		 FROM referral_links WHERE `+strings.Join(where, " AND ")+` ORDER BY updated_at DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*ReferralLink
	for rows.Next() {
		row, err := scanReferralLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func dbReferralLinkGet(db *sql.DB, pid string, id int64) (*ReferralLink, error) {
	row := db.QueryRow(
		`SELECT id, project_id, partner_id, campaign_id, code, short_url, destination_url, status,
		        clicks, conversions, metadata, created_at, updated_at
		 FROM referral_links WHERE project_id = ? AND id = ?`, pid, id)
	link, err := scanReferralLink(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return link, err
}

func dbReferralLinkGetByCode(db *sql.DB, pid, code string) (*ReferralLink, error) {
	row := db.QueryRow(
		`SELECT id, project_id, partner_id, campaign_id, code, short_url, destination_url, status,
		        clicks, conversions, metadata, created_at, updated_at
		 FROM referral_links WHERE project_id = ? AND code = ?`, pid, code)
	link, err := scanReferralLink(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return link, err
}

func scanReferralLink(s rowScanner) (*ReferralLink, error) {
	var link ReferralLink
	var meta string
	if err := s.Scan(&link.ID, &link.ProjectID, &link.PartnerID, &link.CampaignID, &link.Code, &link.ShortURL, &link.DestinationURL, &link.Status, &link.Clicks, &link.Conversions, &meta, &link.CreatedAt, &link.UpdatedAt); err != nil {
		return nil, err
	}
	link.Metadata = rawJSON(meta)
	return &link, nil
}

// ─── DB: referrals and commissions ─────────────────────────────────

func dbReferralRecord(db *sql.DB, pid string, args map[string]any) (*Referral, *Commission, error) {
	partnerID := int64Arg(args, "partner_id")
	campaignID := int64Arg(args, "campaign_id")
	linkID := int64Arg(args, "referral_link_id")
	var link *ReferralLink
	var err error
	if linkID != 0 {
		link, err = dbReferralLinkGet(db, pid, linkID)
	} else if code := strings.TrimSpace(strArg(args, "code")); code != "" {
		link, err = dbReferralLinkGetByCode(db, pid, slugify(code))
	}
	if err != nil {
		return nil, nil, err
	}
	if link != nil {
		linkID = link.ID
		partnerID = link.PartnerID
		campaignID = link.CampaignID
	}
	if partnerID == 0 || campaignID == 0 {
		return nil, nil, errors.New("partner_id and campaign_id required unless referral_link_id/code is supplied")
	}
	if partner, err := dbPartnerGet(db, pid, partnerID); err != nil {
		return nil, nil, err
	} else if partner == nil {
		return nil, nil, errors.New("partner not found")
	}
	campaign, err := dbCampaignGet(db, pid, campaignID)
	if err != nil {
		return nil, nil, err
	}
	if campaign == nil {
		return nil, nil, errors.New("campaign not found")
	}
	status := defaultString(strArg(args, "status"), "lead")
	if !validReferralStatuses[status] {
		return nil, nil, fmt.Errorf("status must be one of lead, converted, rejected, refunded, cancelled")
	}
	amount := int64Arg(args, "amount_cents")
	currency := strings.ToUpper(defaultString(strArg(args, "currency"), "USD"))
	convertedAt := ""
	if status == "converted" {
		convertedAt = nowExpr
	}
	res, err := db.Exec(
		`INSERT INTO referrals
		   (project_id, partner_id, campaign_id, referral_link_id, crm_contact_id, customer_email,
		    external_customer_id, external_order_id, external_subscription_id, status, amount_cents,
		    currency, source_event, metadata, converted_at)
		 VALUES (?, ?, ?, NULLIF(?, 0), ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, `+sqlNullIfNow(convertedAt)+`)`,
		pid, partnerID, campaignID, linkID, int64Arg(args, "crm_contact_id"), strings.ToLower(strings.TrimSpace(strArg(args, "customer_email"))),
		strArg(args, "external_customer_id"), strArg(args, "external_order_id"), strArg(args, "external_subscription_id"),
		status, amount, currency, jsonOrEmpty(args["source_event"], "{}"), jsonOrEmpty(args["metadata"], "{}"))
	if err != nil {
		return nil, nil, err
	}
	referralID, _ := res.LastInsertId()
	if linkID != 0 && status == "converted" {
		_, _ = db.Exec(`UPDATE referral_links SET conversions = conversions + 1, updated_at = CURRENT_TIMESTAMP WHERE project_id = ? AND id = ?`, pid, linkID)
	}
	referral, err := dbReferralGet(db, pid, referralID)
	if err != nil {
		return nil, nil, err
	}
	createCommission := boolArg(args, "create_commission", status == "converted" && amount > 0)
	if !createCommission {
		return referral, nil, nil
	}
	commission, err := dbCommissionCreateFromReferral(db, pid, referral, campaign)
	return referral, commission, err
}

func dbReferralGet(db *sql.DB, pid string, id int64) (*Referral, error) {
	row := db.QueryRow(
		`SELECT id, project_id, partner_id, campaign_id, COALESCE(referral_link_id, 0), crm_contact_id,
		        customer_email, external_customer_id, external_order_id, external_subscription_id,
		        status, amount_cents, currency, source_event, metadata, created_at, converted_at, updated_at
		 FROM referrals WHERE project_id = ? AND id = ?`, pid, id)
	ref, err := scanReferral(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return ref, err
}

func dbReferralsSearch(db *sql.DB, pid string, f filters) ([]*Referral, error) {
	limit := clampLimit(f.Limit, 50, 200)
	where := []string{"project_id = ?"}
	args := []any{pid}
	if f.Query != "" {
		where = append(where, "(customer_email LIKE ? OR external_customer_id LIKE ? OR external_order_id LIKE ? OR external_subscription_id LIKE ?)")
		like := "%" + f.Query + "%"
		args = append(args, like, like, like, like)
	}
	if f.PartnerID != 0 {
		where = append(where, "partner_id = ?")
		args = append(args, f.PartnerID)
	}
	if f.CampaignID != 0 {
		where = append(where, "campaign_id = ?")
		args = append(args, f.CampaignID)
	}
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	args = append(args, limit)
	rows, err := db.Query(
		`SELECT id, project_id, partner_id, campaign_id, COALESCE(referral_link_id, 0), crm_contact_id,
		        customer_email, external_customer_id, external_order_id, external_subscription_id,
		        status, amount_cents, currency, source_event, metadata, created_at, converted_at, updated_at
		 FROM referrals WHERE `+strings.Join(where, " AND ")+` ORDER BY created_at DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Referral
	for rows.Next() {
		row, err := scanReferral(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func dbCommissionCreateFromReferral(db *sql.DB, pid string, referral *Referral, campaign *Campaign) (*Commission, error) {
	if referral == nil || campaign == nil {
		return nil, errors.New("referral and campaign required")
	}
	if campaign.DefaultCommissionType == "none" {
		return nil, nil
	}
	amount := int64(0)
	switch campaign.DefaultCommissionType {
	case "fixed":
		amount = int64(math.Round(campaign.DefaultCommissionValue))
	case "percent":
		amount = int64(math.Round(float64(referral.AmountCents) * campaign.DefaultCommissionValue / 100))
	default:
		return nil, fmt.Errorf("unknown commission type %q", campaign.DefaultCommissionType)
	}
	if amount <= 0 {
		return nil, nil
	}
	reason := fmt.Sprintf("%s commission from campaign %s", campaign.DefaultCommissionType, campaign.Slug)
	res, err := db.Exec(
		`INSERT INTO commissions
		   (project_id, partner_id, referral_id, status, amount_cents, currency, reason, eligible_at)
		 VALUES (?, ?, ?, 'pending', ?, ?, ?, CURRENT_TIMESTAMP)`,
		pid, referral.PartnerID, referral.ID, amount, referral.Currency, reason)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return dbCommissionGet(db, pid, id)
}

func dbCommissionGet(db *sql.DB, pid string, id int64) (*Commission, error) {
	row := db.QueryRow(
		`SELECT id, project_id, partner_id, referral_id, status, amount_cents, currency, reason,
		        eligible_at, approved_at, paid_at, payout_batch, metadata, created_at, updated_at
		 FROM commissions WHERE project_id = ? AND id = ?`, pid, id)
	c, err := scanCommission(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return c, err
}

func dbCommissionsSearch(db *sql.DB, pid string, f filters) ([]*Commission, error) {
	limit := clampLimit(f.Limit, 50, 200)
	where := []string{"project_id = ?"}
	args := []any{pid}
	if f.PartnerID != 0 {
		where = append(where, "partner_id = ?")
		args = append(args, f.PartnerID)
	}
	if f.Status != "" {
		where = append(where, "status = ?")
		args = append(args, f.Status)
	}
	if f.PayoutBatch != "" {
		where = append(where, "payout_batch = ?")
		args = append(args, f.PayoutBatch)
	}
	args = append(args, limit)
	rows, err := db.Query(
		`SELECT id, project_id, partner_id, referral_id, status, amount_cents, currency, reason,
		        eligible_at, approved_at, paid_at, payout_batch, metadata, created_at, updated_at
		 FROM commissions WHERE `+strings.Join(where, " AND ")+` ORDER BY created_at DESC, id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []*Commission
	for rows.Next() {
		row, err := scanCommission(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func dbCommissionUpdate(db *sql.DB, pid string, id int64, patch map[string]any) (*Commission, error) {
	if id == 0 {
		return nil, errors.New("id required")
	}
	sets, args, err := buildPatch(patch, map[string]patchField{
		"status":       {Column: "status", Kind: "enum_commission_status"},
		"payout_batch": {Column: "payout_batch", Kind: "string"},
		"reason":       {Column: "reason", Kind: "string"},
		"metadata":     {Column: "metadata", Kind: "json"},
	})
	if err != nil {
		return nil, err
	}
	status := strArg(patch, "status")
	if status == "approved" {
		sets = append(sets, "approved_at = CASE WHEN approved_at = '' THEN CURRENT_TIMESTAMP ELSE approved_at END")
	}
	if status == "paid" {
		sets = append(sets, "paid_at = CASE WHEN paid_at = '' THEN CURRENT_TIMESTAMP ELSE paid_at END")
		sets = append(sets, "approved_at = CASE WHEN approved_at = '' THEN CURRENT_TIMESTAMP ELSE approved_at END")
	}
	if len(sets) > 0 {
		sets = append(sets, "updated_at = CURRENT_TIMESTAMP")
		args = append(args, pid, id)
		if _, err := db.Exec(`UPDATE commissions SET `+strings.Join(sets, ", ")+` WHERE project_id = ? AND id = ?`, args...); err != nil {
			return nil, err
		}
	}
	return dbCommissionGet(db, pid, id)
}

func scanReferral(s rowScanner) (*Referral, error) {
	var r Referral
	var source, meta string
	if err := s.Scan(&r.ID, &r.ProjectID, &r.PartnerID, &r.CampaignID, &r.ReferralLinkID, &r.CRMContactID,
		&r.CustomerEmail, &r.ExternalCustomerID, &r.ExternalOrderID, &r.ExternalSubscriptionID,
		&r.Status, &r.AmountCents, &r.Currency, &source, &meta, &r.CreatedAt, &r.ConvertedAt, &r.UpdatedAt); err != nil {
		return nil, err
	}
	r.SourceEvent = rawJSON(source)
	r.Metadata = rawJSON(meta)
	return &r, nil
}

func scanCommission(s rowScanner) (*Commission, error) {
	var c Commission
	var meta string
	if err := s.Scan(&c.ID, &c.ProjectID, &c.PartnerID, &c.ReferralID, &c.Status, &c.AmountCents, &c.Currency, &c.Reason,
		&c.EligibleAt, &c.ApprovedAt, &c.PaidAt, &c.PayoutBatch, &meta, &c.CreatedAt, &c.UpdatedAt); err != nil {
		return nil, err
	}
	c.Metadata = rawJSON(meta)
	return &c, nil
}

func dbProgramStats(db *sql.DB, pid string) (map[string]any, error) {
	out := map[string]any{}
	for key, query := range map[string]string{
		"partners":         `SELECT COUNT(*) FROM partners WHERE project_id = ?`,
		"active_campaigns": `SELECT COUNT(*) FROM campaigns WHERE project_id = ? AND status = 'active'`,
		"referral_links":   `SELECT COUNT(*) FROM referral_links WHERE project_id = ?`,
		"referrals":        `SELECT COUNT(*) FROM referrals WHERE project_id = ?`,
		"conversions":      `SELECT COUNT(*) FROM referrals WHERE project_id = ? AND status = 'converted'`,
	} {
		var n int64
		if err := db.QueryRow(query, pid).Scan(&n); err != nil {
			return nil, err
		}
		out[key] = n
	}
	rows, err := db.Query(
		`SELECT status, currency, COUNT(*), COALESCE(SUM(amount_cents), 0)
		 FROM commissions WHERE project_id = ? GROUP BY status, currency ORDER BY status, currency`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var commissions []map[string]any
	for rows.Next() {
		var status, currency string
		var count, amount int64
		if err := rows.Scan(&status, &currency, &count, &amount); err != nil {
			return nil, err
		}
		commissions = append(commissions, map[string]any{
			"status": status, "currency": currency, "count": count, "amount_cents": amount,
		})
	}
	out["commissions"] = commissions
	return out, rows.Err()
}

// ─── Helpers ───────────────────────────────────────────────────────

const nowExpr = "CURRENT_TIMESTAMP"

var (
	validPartnerTypes       = set("customer", "affiliate", "agency", "reseller", "internal")
	validPartnerStatuses    = set("pending", "approved", "rejected", "suspended")
	validCampaignStatuses   = set("draft", "active", "paused", "archived")
	validCommissionTypes    = set("none", "fixed", "percent")
	validLinkStatuses       = set("active", "paused", "archived")
	validReferralStatuses   = set("lead", "converted", "rejected", "refunded", "cancelled")
	validCommissionStatuses = set("pending", "approved", "rejected", "void", "paid")
)

type rowScanner interface {
	Scan(dest ...any) error
}

type patchField struct {
	Column string
	Kind   string
}

func set(values ...string) map[string]bool {
	out := map[string]bool{}
	for _, v := range values {
		out[v] = true
	}
	return out
}

func projectFromArgs(ctx *sdk.AppCtx, args map[string]any) (string, error) {
	if raw := strings.TrimSpace(strArg(args, "_project_id")); raw != "" {
		return raw, nil
	}
	if ctx != nil && strings.TrimSpace(ctx.CurrentProject()) != "" {
		return strings.TrimSpace(ctx.CurrentProject()), nil
	}
	return "", errors.New("_project_id required for global installs")
}

func projectFromRequest(r *http.Request, ctx *sdk.AppCtx) (string, error) {
	if v := strings.TrimSpace(r.URL.Query().Get("project_id")); v != "" {
		return v, nil
	}
	if ctx != nil && strings.TrimSpace(ctx.CurrentProject()) != "" {
		return strings.TrimSpace(ctx.CurrentProject()), nil
	}
	return "", errors.New("project_id required for global installs")
}

func getAppCtx(r *http.Request) *sdk.AppCtx {
	return globalCtx
}

func strArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	if v, ok := args[key].(string); ok {
		return strings.TrimSpace(v)
	}
	return ""
}

func int64Arg(args map[string]any, key string) int64 {
	if args == nil {
		return 0
	}
	switch v := args[key].(type) {
	case int:
		return int64(v)
	case int64:
		return v
	case float64:
		return int64(v)
	case json.Number:
		n, _ := v.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(v), 10, 64)
		return n
	default:
		return 0
	}
}

func intArg(args map[string]any, key string, def int) int {
	if v := int64Arg(args, key); v != 0 {
		return int(v)
	}
	return def
}

func floatArg(args map[string]any, key string, def float64) float64 {
	if args == nil {
		return def
	}
	switch v := args[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case int64:
		return float64(v)
	case json.Number:
		n, _ := v.Float64()
		return n
	case string:
		n, err := strconv.ParseFloat(strings.TrimSpace(v), 64)
		if err == nil {
			return n
		}
	}
	return def
}

func boolArg(args map[string]any, key string, def bool) bool {
	if args == nil {
		return def
	}
	v, ok := args[key]
	if !ok {
		return def
	}
	b, ok := v.(bool)
	if !ok {
		return def
	}
	return b
}

func defaultString(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return strings.TrimSpace(v)
}

func jsonOrEmpty(v any, def string) string {
	if v == nil {
		return def
	}
	b, err := json.Marshal(v)
	if err != nil || !json.Valid(b) {
		return def
	}
	return string(b)
}

func rawJSON(s string) json.RawMessage {
	if strings.TrimSpace(s) == "" {
		return json.RawMessage("{}")
	}
	if !json.Valid([]byte(s)) {
		return json.RawMessage("{}")
	}
	return json.RawMessage(s)
}

func slugify(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "ref"
	}
	return out
}

func clampLimit(n, def, max int) int {
	if n <= 0 {
		n = def
	}
	if n > max {
		n = max
	}
	return n
}

func buildPatch(patch map[string]any, allowed map[string]patchField) ([]string, []any, error) {
	if patch == nil {
		return nil, nil, nil
	}
	var sets []string
	var args []any
	for key, value := range patch {
		field, ok := allowed[key]
		if !ok {
			continue
		}
		converted, err := patchValue(field.Kind, value)
		if err != nil {
			return nil, nil, err
		}
		sets = append(sets, field.Column+" = ?")
		args = append(args, converted)
	}
	return sets, args, nil
}

func patchValue(kind string, value any) (any, error) {
	switch kind {
	case "string":
		s, _ := value.(string)
		return strings.TrimSpace(s), nil
	case "string_nonempty":
		s, _ := value.(string)
		s = strings.TrimSpace(s)
		if s == "" {
			return nil, errors.New("name cannot be empty")
		}
		return s, nil
	case "lower":
		s, _ := value.(string)
		return strings.ToLower(strings.TrimSpace(s)), nil
	case "int":
		return int64Arg(map[string]any{"v": value}, "v"), nil
	case "json":
		return jsonOrEmpty(value, "{}"), nil
	case "enum_partner_type":
		s, _ := value.(string)
		if !validPartnerTypes[s] {
			return nil, errors.New("invalid partner type")
		}
		return s, nil
	case "enum_partner_status":
		s, _ := value.(string)
		if !validPartnerStatuses[s] {
			return nil, errors.New("invalid partner status")
		}
		return s, nil
	case "enum_commission_status":
		s, _ := value.(string)
		if !validCommissionStatuses[s] {
			return nil, errors.New("invalid commission status")
		}
		return s, nil
	default:
		return value, nil
	}
}

func filtersFromArgs(args map[string]any) filters {
	return filters{
		Query:       strArg(args, "q"),
		Type:        strArg(args, "type"),
		Status:      strArg(args, "status"),
		PayoutBatch: strArg(args, "payout_batch"),
		PartnerID:   int64Arg(args, "partner_id"),
		CampaignID:  int64Arg(args, "campaign_id"),
		Limit:       intArg(args, "limit", 50),
	}
}

func filtersFromRequest(r *http.Request) filters {
	q := r.URL.Query()
	return filters{
		Query:       strings.TrimSpace(q.Get("q")),
		Type:        strings.TrimSpace(q.Get("type")),
		Status:      strings.TrimSpace(q.Get("status")),
		PayoutBatch: strings.TrimSpace(q.Get("payout_batch")),
		PartnerID:   atoi64(q.Get("partner_id")),
		CampaignID:  atoi64(q.Get("campaign_id")),
		Limit:       int(atoi64(q.Get("limit"))),
	}
}

func atoi64(s string) int64 {
	n, _ := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	return n
}

func filterSchema(extra ...string) map[string]any {
	props := map[string]any{
		"q":     map[string]any{"type": "string"},
		"limit": map[string]any{"type": "integer"},
	}
	for _, key := range extra {
		typ := "string"
		if strings.HasSuffix(key, "_id") {
			typ = "integer"
		}
		props[key] = map[string]any{"type": typ}
	}
	return props
}

func schemaObject(props map[string]any, required []string) map[string]any {
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func decodeBody(w http.ResponseWriter, r *http.Request, out any) bool {
	dec := json.NewDecoder(r.Body)
	dec.UseNumber()
	if err := dec.Decode(out); err != nil {
		httpErr(w, http.StatusBadRequest, "invalid json")
		return false
	}
	return true
}

func writeHTTPResult(w http.ResponseWriter, v any, err error) {
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	httpJSON(w, v)
}

func httpJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": msg})
}

func pathID(path, prefix string) int64 {
	rest := strings.Trim(strings.TrimPrefix(path, prefix), "/")
	if rest == "" {
		return 0
	}
	part := strings.Split(rest, "/")[0]
	return atoi64(part)
}

func sqlNullIfNow(value string) string {
	if value == nowExpr {
		return "CURRENT_TIMESTAMP"
	}
	return "''"
}
