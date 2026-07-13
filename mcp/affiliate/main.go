// Apteva Affiliate app — publisher-side affiliate manager.
//
// The app owns a small normalized model: networks, offers, links, and
// daily stats. Provider-specific connectors stay as integrations; this
// app executes their tools through bound integration roles.
package main

import (
	"context"
	"database/sql"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const legacyManifestYAML = `schema: apteva-app/v1
name: affiliate
display_name: Affiliate
version: 0.1.10
description: Publisher-side affiliate manager.
author: Apteva
scopes: [project, global]
requires:
  permissions:
    - db.write.app
    - platform.apps.call
    - platform.connections.execute
  apps:
    - name: redirects
      optional: true
      reason: Creates branded short links for affiliate URLs.
      events:
        - rule.hit
  integrations:
    - role: target-circle
      kind: integration
      required: false
      compatible_slugs: [target-circle]
      label: Target Circle / Circlewise
    - role: impact
      kind: integration
      required: false
      compatible_slugs: [impact]
      label: Impact
    - role: awin
      kind: integration
      required: false
      compatible_slugs: [awin]
      label: Awin
    - role: cj-affiliate
      kind: integration
      required: false
      compatible_slugs: [cj-affiliate]
      label: CJ Affiliate
    - role: amazon-associates
      kind: integration
      required: false
      compatible_slugs: [amazon-associates]
      label: Amazon Associates
    - role: skimlinks
      kind: integration
      required: false
      compatible_slugs: [skimlinks]
      label: Skimlinks
    - role: sovrn
      kind: integration
      required: false
      compatible_slugs: [sovrn]
      label: Sovrn
    - role: partnerstack
      kind: integration
      required: false
      compatible_slugs: [partnerstack]
      label: PartnerStack
    - role: shareasale
      kind: integration
      required: false
      compatible_slugs: [shareasale]
      label: ShareASale
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - name: affiliate_networks
      description: List configured affiliate networks.
    - name: affiliate_refresh
      description: Refresh offers and/or stats from a network dependency.
    - name: affiliate_offers
      description: Search offers.
    - name: affiliate_link_create
      description: Create a monetized link.
    - name: affiliate_links
      description: Search managed links.
    - name: affiliate_stats
      description: Read normalized affiliate stats with one unified clicks metric. Date ranges accept YYYY-MM-DD or RFC3339 values.
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/affiliate
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/affiliate.db
  migrations: migrations/
config_schema:
  - name: default_short_hostname
    type: text
    default: ""
    label: Default short-link hostname
  - name: awin_publisher_id
    type: text
    default: ""
    label: Awin publisher ID
  - name: cj_requestor_cid
    type: text
    default: ""
    label: CJ requestor CID
  - name: cj_website_id
    type: text
    default: ""
    label: CJ website ID
  - name: amazon_partner_tag
    type: text
    default: ""
    label: Amazon partner tag
  - name: amazon_marketplace
    type: text
    default: "www.amazon.com"
    label: Amazon marketplace
  - name: amazon_keywords
    type: text
    default: ""
    label: Amazon refresh keywords
  - name: amazon_search_index
    type: text
    default: "All"
    label: Amazon search index
  - name: awin_relationship
    type: text
    default: "joined"
    label: Awin relationship
  - name: awin_date_type
    type: text
    default: "transaction"
    label: Awin date type
  - name: cj_advertiser_ids
    type: text
    default: "joined"
    label: CJ advertiser IDs
  - name: impact_insertion_order_status
    type: text
    default: "Active"
    label: Impact contract status
  - name: sovrn_campaign_id
    type: text
    default: ""
    label: Sovrn campaign ID
  - name: target_circle_ad_inventory_sid
    type: text
    default: ""
    label: Target Circle ad inventory SID
upgrade_policy: auto-patch
`

//go:embed apteva.yaml
var manifestYAML []byte

const (
	automaticStatsRefreshSchedule  = "@every 6h"
	automaticOffersRefreshSchedule = "@every 24h"
	automaticStatsWindowDays       = 7
)

type App struct {
	refreshMu sync.Mutex
}

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest(manifestYAML)
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("affiliate app requires a db block")
	}
	ctx.Logger().Info("affiliate app mounted")
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error { return nil }

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/networks", Handler: a.handleNetworks},
		{Pattern: "/refresh", Handler: a.handleRefresh},
		{Pattern: "/offers", Handler: a.handleOffers},
		{Pattern: "/links", Handler: a.handleLinks},
		{Pattern: "/stats", Handler: a.handleStats},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "affiliate_networks",
			Description: "List configured affiliate networks. Args: enabled? (bool filter).",
			InputSchema: schemaObject(map[string]any{
				"enabled": map[string]any{"type": "boolean"},
			}, nil),
			Handler: a.toolNetworks,
		},
		{
			Name: "affiliate_refresh",
			Description: "Refresh offers and/or stats from a bound provider integration. " +
				"Args: network (req: target-circle|impact|awin|cj-affiliate|amazon-associates|skimlinks|sovrn|partnerstack|shareasale), " +
				"kind? (offers|stats|all, default all), from?, to?, plus provider IDs such as publisherId, requestor-cid, website-id, partnerTag, campaignId, keywords.",
			InputSchema: schemaObject(map[string]any{
				"network":       map[string]any{"type": "string"},
				"kind":          map[string]any{"type": "string", "enum": []string{"offers", "stats", "all"}},
				"from":          map[string]any{"type": "string"},
				"to":            map[string]any{"type": "string"},
				"publisherId":   map[string]any{"type": "string"},
				"requestor-cid": map[string]any{"type": "string"},
				"website-id":    map[string]any{"type": "string"},
				"partnerTag":    map[string]any{"type": "string"},
				"campaignId":    map[string]any{"type": "string"},
				"keywords":      map[string]any{"type": "string"},
			}, []string{"network"}),
			Handler: a.toolRefresh,
		},
		{
			Name:        "affiliate_offers",
			Description: "Search normalized offers with total-count pagination. Args: q?, network?, status?, limit? (default 50), offset?.",
			InputSchema: schemaObject(map[string]any{
				"q":       map[string]any{"type": "string"},
				"network": map[string]any{"type": "string"},
				"status":  map[string]any{"type": "string"},
				"limit":   map[string]any{"type": "integer"},
				"offset":  map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolOffers,
		},
		{
			Name: "affiliate_link_create",
			Description: "Create a monetized link. If affiliate_url is absent, calls the configured provider integration. " +
				"If shorten=true, calls redirects.redirect_add with short_hostname/default_short_hostname and short_path. " +
				"Args: url (req), network?, offer_id?, affiliate_url?, campaign?, subid?, shorten?, short_hostname?, short_path?, plus provider IDs such as publisherId, website-id, partnerTag, adInventorySid.",
			InputSchema: schemaObject(map[string]any{
				"url":            map[string]any{"type": "string"},
				"network":        map[string]any{"type": "string"},
				"offer_id":       map[string]any{"type": "integer"},
				"affiliate_url":  map[string]any{"type": "string"},
				"campaign":       map[string]any{"type": "string"},
				"subid":          map[string]any{"type": "string"},
				"shorten":        map[string]any{"type": "boolean"},
				"short_hostname": map[string]any{"type": "string"},
				"short_path":     map[string]any{"type": "string"},
				"publisherId":    map[string]any{"type": "string"},
				"website-id":     map[string]any{"type": "string"},
				"partnerTag":     map[string]any{"type": "string"},
				"marketplace":    map[string]any{"type": "string"},
				"adInventorySid": map[string]any{"type": "string"},
				"asin":           map[string]any{"type": "string"},
			}, []string{"url"}),
			Handler: a.toolLinkCreate,
		},
		{
			Name:        "affiliate_links",
			Description: "Search managed links with total-count pagination. Args: q?, network?, offer_id?, status?, limit? (default 50), offset?.",
			InputSchema: schemaObject(map[string]any{
				"q":        map[string]any{"type": "string"},
				"network":  map[string]any{"type": "string"},
				"offer_id": map[string]any{"type": "integer"},
				"status":   map[string]any{"type": "string"},
				"limit":    map[string]any{"type": "integer"},
				"offset":   map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolLinks,
		},
		{
			Name: "affiliate_stats",
			Description: "Read normalized affiliate stats with one unified clicks metric. Args: from?, to? (YYYY-MM-DD or RFC3339), network?, offer_id?, link_id?, " +
				"group_by? (network|offer|link|day, default day).",
			InputSchema: schemaObject(map[string]any{
				"from":     map[string]any{"type": "string"},
				"to":       map[string]any{"type": "string"},
				"network":  map[string]any{"type": "string"},
				"offer_id": map[string]any{"type": "integer"},
				"link_id":  map[string]any{"type": "integer"},
				"group_by": map[string]any{"type": "string", "enum": []string{"network", "offer", "link", "day"}},
			}, nil),
			Handler: a.toolStats,
		},
	}
}

func (a *App) Channels() []sdk.ChannelFactory { return nil }
func (a *App) EventHandlers() []sdk.EventHandler {
	return []sdk.EventHandler{
		{Event: "rule.hit", Handler: a.handleRedirectHit},
	}
}

func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{
		{
			Name:     "affiliate-stats-refresh",
			Schedule: automaticStatsRefreshSchedule,
			Run: func(ctx context.Context, app *sdk.AppCtx) error {
				from, to := automaticStatsWindow(time.Now().UTC())
				providerErr := a.runAutomaticRefresh(ctx, app, "stats", from, to)
				redirectErr := a.reconcileRedirectClicks(ctx, app, from, to)
				return errors.Join(providerErr, redirectErr)
			},
		},
		{
			Name:     "affiliate-offers-refresh",
			Schedule: automaticOffersRefreshSchedule,
			Run: func(ctx context.Context, app *sdk.AppCtx) error {
				return a.runAutomaticRefresh(ctx, app, "offers", "", "")
			},
		},
	}
}

func automaticStatsWindow(now time.Time) (string, string) {
	today := now.UTC()
	from := today.AddDate(0, 0, -(automaticStatsWindowDays - 1))
	return from.Format("2006-01-02"), today.Format("2006-01-02")
}

func (a *App) runAutomaticRefresh(ctx context.Context, app *sdk.AppCtx, kind, from, to string) error {
	if app == nil || app.AppDB() == nil {
		return errors.New("affiliate automatic refresh requires an app database")
	}
	stored, err := dbListNetworks(app.AppDB(), nil)
	if err != nil {
		return err
	}
	byKey := make(map[string]Network, len(stored))
	for _, network := range stored {
		byKey[network.Key] = network
	}

	var refreshErrors []error
	for _, capability := range providerCapabilities {
		if (kind == "stats" && !capability.Stats) || (kind == "offers" && !capability.Offers) {
			continue
		}
		if network, exists := byKey[capability.Key]; exists && !network.Enabled {
			continue
		}
		binding := app.IntegrationFor(capability.Key)
		if binding == nil || binding.ConnectionID == 0 {
			continue
		}
		if err := ctx.Err(); err != nil {
			return errors.Join(append(refreshErrors, err)...)
		}

		summary, err := a.refreshNetwork(app, capability.Key, kind, from, to, map[string]any{})
		if err != nil {
			app.Logger().Warn("automatic affiliate refresh failed", "network", capability.Key, "kind", kind, "err", err)
			refreshErrors = append(refreshErrors, fmt.Errorf("%s: %w", capability.Key, err))
			continue
		}
		app.Logger().Info("automatic affiliate refresh completed",
			"network", capability.Key,
			"kind", kind,
			"offers", summary.OffersUpserted,
			"links", summary.LinksUpserted,
			"stat_rows", summary.StatsDaysUpserted)
	}
	return errors.Join(refreshErrors...)
}

func (a *App) handleRedirectHit(ctx *sdk.AppCtx, event sdk.Event) error {
	if event.SourceApp != "" && event.SourceApp != "redirects" {
		return fmt.Errorf("rule.hit source %q is not redirects", event.SourceApp)
	}
	hit, err := redirectHitFromEvent(event)
	if err != nil {
		return err
	}
	link, matchedBy, err := dbFindLinkForRedirectHit(ctx.AppDB(), hit)
	if err != nil {
		return err
	}
	if link == nil {
		ctx.Logger().Info("redirect hit did not match an affiliate link", "rule_id", hit.RuleID)
		return nil
	}
	if link.RedirectRuleID == 0 {
		if err := dbAttachRedirectRule(ctx.AppDB(), link.ID, hit.RuleID); err != nil {
			return err
		}
	}
	if err := dbUpsertRedirectClick(ctx.AppDB(), link.ID, hit, matchedBy); err != nil {
		return err
	}
	ctx.Logger().Info("affiliate redirect click recorded", "link_id", link.ID, "rule_id", hit.RuleID, "matched_by", matchedBy)
	return nil
}

func redirectHitFromEvent(event sdk.Event) (RedirectHit, error) {
	data := event.Data
	ruleID := toInt64(firstAny(data, "rule_id", "redirect_rule_id", "id"))
	if ruleID == 0 {
		return RedirectHit{}, errors.New("redirect rule.hit missing rule_id")
	}
	at := firstNonEmpty(firstString(data, "at", "occurred_at"), nowUTC())
	date := normalizeStatDate(firstNonEmpty(firstString(data, "date", "day"), at))
	dayValue := firstAny(data, "day_hits")
	return RedirectHit{
		RuleID:         ruleID,
		Destination:    firstString(data, "destination"),
		Target:         firstString(data, "target"),
		Date:           date,
		At:             at,
		DayHits:        toInt64(dayValue),
		HitsTotal:      toInt64(firstAny(data, "hits_total", "total_hits")),
		HasDaySnapshot: dayValue != nil,
		RawJSON:        mustJSON(redactSecrets(data)),
	}, nil
}

func (a *App) reconcileRedirectClicks(ctx context.Context, app *sdk.AppCtx, from, to string) error {
	if app == nil || app.AppDB() == nil || app.PlatformAPI() == nil {
		return errors.New("redirect click reconciliation requires the platform API and app database")
	}
	rules, err := dbRedirectRuleLinks(app.AppDB())
	if err != nil || len(rules) == 0 {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	const pageSize = 250
	for offset := 0; ; offset += pageSize {
		out := map[string]any{}
		if err := app.PlatformAPI().CallAppResult("redirects", "redirect_stats", map[string]any{
			"from": from, "to": to, "limit": pageSize, "offset": offset,
		}, &out); err != nil {
			return fmt.Errorf("redirect_stats: %w", err)
		}
		rows := collectMaps(out, "stats", "data", "items", "results")
		for _, row := range rows {
			ruleID := toInt64(firstAny(row, "rule_id", "redirect_rule_id", "id"))
			linkID := rules[ruleID]
			if linkID == 0 {
				continue
			}
			dayValue := firstAny(row, "day_hits", "hits", "clicks")
			hit := RedirectHit{
				RuleID:         ruleID,
				Date:           normalizeStatDate(firstString(row, "date", "day")),
				At:             firstNonEmpty(firstString(row, "updated_at", "at"), nowUTC()),
				DayHits:        toInt64(dayValue),
				HitsTotal:      toInt64(firstAny(row, "hits_total", "total_hits")),
				HasDaySnapshot: dayValue != nil,
				RawJSON:        mustJSON(redactSecrets(row)),
			}
			if hit.Date == "" || !hit.HasDaySnapshot {
				continue
			}
			if err := dbUpsertRedirectClick(app.AppDB(), linkID, hit, "reconcile"); err != nil {
				return err
			}
		}
		total := int(toInt64(firstAny(out, "total")))
		if len(rows) < pageSize || total > 0 && offset+len(rows) >= total {
			break
		}
	}
	return nil
}

type Network struct {
	ID              int64  `json:"id"`
	Key             string `json:"key"`
	Name            string `json:"name"`
	Enabled         bool   `json:"enabled"`
	Bound           bool   `json:"bound"`
	SupportsOffers  bool   `json:"supports_offers"`
	SupportsStats   bool   `json:"supports_stats"`
	SupportsLinks   bool   `json:"supports_links"`
	SupportsClicks  bool   `json:"supports_clicks"`
	StatsNeedsDates bool   `json:"stats_needs_dates"`
	StatsMode       string `json:"stats_mode,omitempty"`
	LinkMode        string `json:"link_mode,omitempty"`
	ConnectionRef   string `json:"connection_ref,omitempty"`
	LastRefreshedAt string `json:"last_refreshed_at,omitempty"`
	MetadataJSON    string `json:"metadata_json,omitempty"`
	CreatedAt       string `json:"created_at,omitempty"`
	UpdatedAt       string `json:"updated_at,omitempty"`
}

type providerCapability struct {
	Key             string
	Offers          bool
	Stats           bool
	Links           bool
	Clicks          bool
	StatsNeedsDates bool
	StatsMode       string
	LinkMode        string
}

var providerCapabilities = []providerCapability{
	{Key: "target-circle", Offers: true, Stats: true, Links: true, StatsMode: "range", LinkMode: "provider"},
	{Key: "impact", Offers: true, Stats: true, Links: true, StatsMode: "range", LinkMode: "provider"},
	{Key: "awin", Offers: true, Stats: true, Links: true, StatsNeedsDates: true, StatsMode: "range", LinkMode: "provider"},
	{Key: "cj-affiliate", Offers: true, Stats: true, Links: true, StatsMode: "range", LinkMode: "manual"},
	{Key: "amazon-associates", Offers: true, Links: true, LinkMode: "provider"},
	{Key: "skimlinks", Links: true, LinkMode: "provider"},
	{Key: "sovrn", Offers: true, Stats: true, Links: true, StatsMode: "daily", LinkMode: "provider"},
	{Key: "partnerstack", Offers: true, Stats: true, Links: true, StatsMode: "range", LinkMode: "manual"},
	{Key: "shareasale", Offers: true, Stats: true, Links: true, StatsMode: "current-day", LinkMode: "manual"},
}

func capabilityFor(network string) (providerCapability, bool) {
	network = canonicalNetworkKey(network)
	for _, capability := range providerCapabilities {
		if capability.Key == network {
			return capability, true
		}
	}
	return providerCapability{}, false
}

func enrichNetwork(ctx *sdk.AppCtx, network *Network, capability providerCapability) {
	if network == nil {
		return
	}
	network.SupportsOffers = capability.Offers
	network.SupportsStats = capability.Stats
	network.SupportsLinks = capability.Links
	network.SupportsClicks = capability.Clicks
	network.StatsNeedsDates = capability.StatsNeedsDates
	network.StatsMode = capability.StatsMode
	network.LinkMode = capability.LinkMode
	if ctx != nil {
		if bound := ctx.IntegrationFor(capability.Key); bound != nil && bound.ConnectionID != 0 {
			network.Bound = true
			network.ConnectionRef = strconv.FormatInt(bound.ConnectionID, 10)
		}
	}
}

type Offer struct {
	ID                int64  `json:"id"`
	NetworkKey        string `json:"network_key"`
	ExternalID        string `json:"external_id"`
	MerchantName      string `json:"merchant_name"`
	OfferName         string `json:"offer_name"`
	Status            string `json:"status"`
	Category          string `json:"category,omitempty"`
	Vertical          string `json:"vertical,omitempty"`
	CountriesJSON     string `json:"countries_json,omitempty"`
	CommissionSummary string `json:"commission_summary,omitempty"`
	CookieWindow      string `json:"cookie_window,omitempty"`
	TrackingDeepLink  bool   `json:"tracking_deeplink"`
	RawJSON           string `json:"raw_json,omitempty"`
	LastRefreshedAt   string `json:"last_refreshed_at,omitempty"`
	CreatedAt         string `json:"created_at,omitempty"`
	UpdatedAt         string `json:"updated_at,omitempty"`
}

type Link struct {
	ID             int64  `json:"id"`
	NetworkKey     string `json:"network_key"`
	OfferID        int64  `json:"offer_id,omitempty"`
	MerchantName   string `json:"merchant_name,omitempty"`
	OfferName      string `json:"offer_name,omitempty"`
	DestinationURL string `json:"destination_url"`
	AffiliateURL   string `json:"affiliate_url"`
	ShortURL       string `json:"short_url,omitempty"`
	RedirectRuleID int64  `json:"redirect_rule_id,omitempty"`
	Campaign       string `json:"campaign,omitempty"`
	SubID          string `json:"subid,omitempty"`
	Status         string `json:"status"`
	RawJSON        string `json:"raw_json,omitempty"`
	LastCheckedAt  string `json:"last_checked_at,omitempty"`
	CreatedAt      string `json:"created_at,omitempty"`
	UpdatedAt      string `json:"updated_at,omitempty"`
}

type StatRow struct {
	Date            string `json:"date,omitempty"`
	NetworkKey      string `json:"network_key,omitempty"`
	OfferID         int64  `json:"offer_id,omitempty"`
	LinkID          int64  `json:"link_id,omitempty"`
	Clicks          int64  `json:"clicks"`
	RedirectClicks  int64  `json:"redirect_clicks"`
	Conversions     int64  `json:"conversions"`
	RevenueCents    int64  `json:"revenue_cents"`
	CommissionCents int64  `json:"commission_cents"`
	Currency        string `json:"currency"`
}

// UnifiedStatRow is the MCP-facing analytics contract. Source-specific click
// counters stay internal so callers have exactly one click metric to consume.
type UnifiedStatRow struct {
	Date            string `json:"date,omitempty"`
	NetworkKey      string `json:"network_key,omitempty"`
	OfferID         int64  `json:"offer_id,omitempty"`
	LinkID          int64  `json:"link_id,omitempty"`
	Clicks          int64  `json:"clicks"`
	Conversions     int64  `json:"conversions"`
	RevenueCents    int64  `json:"revenue_cents"`
	CommissionCents int64  `json:"commission_cents"`
	Currency        string `json:"currency"`
}

type RedirectHit struct {
	RuleID         int64
	Destination    string
	Target         string
	Date           string
	At             string
	DayHits        int64
	HitsTotal      int64
	HasDaySnapshot bool
	RawJSON        string
}

type RefreshSummary struct {
	Network           string `json:"network"`
	Kind              string `json:"kind"`
	OffersUpserted    int    `json:"offers_upserted"`
	LinksUpserted     int    `json:"links_upserted"`
	StatsDaysUpserted int    `json:"stats_days_upserted"`
	RefreshedAt       string `json:"refreshed_at"`
}

// --- DB helpers -------------------------------------------------------------

func dbListNetworks(db *sql.DB, enabled *bool) ([]Network, error) {
	q := `SELECT id, key, name, enabled, connection_ref, last_refreshed_at, metadata_json, created_at, updated_at FROM networks`
	args := []any{}
	if enabled != nil {
		q += ` WHERE enabled = ?`
		args = append(args, boolInt(*enabled))
	}
	q += ` ORDER BY name`
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Network
	for rows.Next() {
		n, err := scanNetwork(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *n)
	}
	return out, rows.Err()
}

func dbUpsertNetwork(db *sql.DB, key, name string, metadata any) (*Network, error) {
	key = strings.TrimSpace(key)
	if key == "" {
		return nil, errors.New("network required")
	}
	if name == "" {
		name = displayNetworkName(key)
	}
	now := nowUTC()
	meta := mustJSON(redactSecrets(metadata))
	_, err := db.Exec(`
		INSERT INTO networks (key, name, enabled, last_refreshed_at, metadata_json, created_at, updated_at)
		VALUES (?, ?, 1, ?, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
			name = excluded.name,
			metadata_json = excluded.metadata_json,
			last_refreshed_at = excluded.updated_at,
			updated_at = excluded.updated_at`,
		key, name, now, meta, now, now)
	if err != nil {
		return nil, err
	}
	return dbGetNetwork(db, key)
}

func dbGetNetwork(db *sql.DB, key string) (*Network, error) {
	row := db.QueryRow(`SELECT id, key, name, enabled, connection_ref, last_refreshed_at, metadata_json, created_at, updated_at FROM networks WHERE key = ?`, key)
	n, err := scanNetwork(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return n, err
}

func scanNetwork(s rowScanner) (*Network, error) {
	var n Network
	var enabled int
	if err := s.Scan(&n.ID, &n.Key, &n.Name, &enabled, &n.ConnectionRef, &n.LastRefreshedAt, &n.MetadataJSON, &n.CreatedAt, &n.UpdatedAt); err != nil {
		return nil, err
	}
	n.Enabled = enabled != 0
	return &n, nil
}

type OfferInput struct {
	NetworkKey        string
	ExternalID        string
	MerchantName      string
	OfferName         string
	Status            string
	Category          string
	Vertical          string
	CountriesJSON     string
	CommissionSummary string
	CookieWindow      string
	TrackingDeepLink  bool
	RawJSON           string
}

func dbUpsertOffer(db *sql.DB, in OfferInput) (*Offer, error) {
	if in.NetworkKey == "" {
		return nil, errors.New("network required")
	}
	if in.ExternalID == "" {
		return nil, errors.New("external_id required")
	}
	if in.MerchantName == "" {
		return nil, errors.New("merchant_name required")
	}
	if in.CountriesJSON == "" {
		in.CountriesJSON = "[]"
	}
	if in.RawJSON == "" {
		in.RawJSON = "{}"
	}
	now := nowUTC()
	_, err := db.Exec(`
		INSERT INTO offers (
			network_key, external_id, merchant_name, offer_name, status,
			category, vertical, countries_json, commission_summary, cookie_window,
			tracking_deeplink, raw_json, last_refreshed_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(network_key, external_id) DO UPDATE SET
			merchant_name = excluded.merchant_name,
			offer_name = excluded.offer_name,
			status = excluded.status,
			category = excluded.category,
			vertical = excluded.vertical,
			countries_json = excluded.countries_json,
			commission_summary = excluded.commission_summary,
			cookie_window = excluded.cookie_window,
			tracking_deeplink = excluded.tracking_deeplink,
			raw_json = excluded.raw_json,
			last_refreshed_at = excluded.last_refreshed_at,
			updated_at = excluded.updated_at`,
		in.NetworkKey, in.ExternalID, in.MerchantName, in.OfferName, in.Status,
		in.Category, in.Vertical, in.CountriesJSON, in.CommissionSummary, in.CookieWindow,
		boolInt(in.TrackingDeepLink), in.RawJSON, now, now, now)
	if err != nil {
		return nil, err
	}
	return dbGetOfferByExternal(db, in.NetworkKey, in.ExternalID)
}

func dbGetOffer(db *sql.DB, id int64) (*Offer, error) {
	row := db.QueryRow(`SELECT `+offerCols+` FROM offers WHERE id = ?`, id)
	o, err := scanOffer(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return o, err
}

func dbGetOfferByExternal(db *sql.DB, network, externalID string) (*Offer, error) {
	row := db.QueryRow(`SELECT `+offerCols+` FROM offers WHERE network_key = ? AND external_id = ?`, network, externalID)
	o, err := scanOffer(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return o, err
}

const offerCols = `id, network_key, external_id, merchant_name, offer_name, status,
	category, vertical, countries_json, commission_summary, cookie_window,
	tracking_deeplink, raw_json, last_refreshed_at, created_at, updated_at`

func dbListOffers(db *sql.DB, q, network, status string, limit, offset int) ([]Offer, int, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	where := []string{"1=1"}
	args := []any{}
	if network != "" {
		where = append(where, "network_key = ?")
		args = append(args, network)
	}
	if status != "" {
		where = append(where, "status = ?")
		args = append(args, status)
	}
	if q != "" {
		like := "%" + q + "%"
		where = append(where, "(merchant_name LIKE ? OR offer_name LIKE ? OR category LIKE ? OR vertical LIKE ?)")
		args = append(args, like, like, like, like)
	}
	if offset < 0 {
		offset = 0
	}
	var total int
	if err := db.QueryRow(`SELECT COUNT(*) FROM offers WHERE `+strings.Join(where, " AND "), args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, limit, offset)
	rows, err := db.Query(`SELECT `+offerCols+` FROM offers WHERE `+strings.Join(where, " AND ")+` ORDER BY merchant_name, offer_name LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Offer
	for rows.Next() {
		o, err := scanOffer(rows)
		if err != nil {
			return nil, 0, err
		}
		out = append(out, *o)
	}
	return out, total, rows.Err()
}

func scanOffer(s rowScanner) (*Offer, error) {
	var o Offer
	var deep int
	if err := s.Scan(&o.ID, &o.NetworkKey, &o.ExternalID, &o.MerchantName, &o.OfferName, &o.Status,
		&o.Category, &o.Vertical, &o.CountriesJSON, &o.CommissionSummary, &o.CookieWindow,
		&deep, &o.RawJSON, &o.LastRefreshedAt, &o.CreatedAt, &o.UpdatedAt); err != nil {
		return nil, err
	}
	o.TrackingDeepLink = deep != 0
	return &o, nil
}

type LinkInput struct {
	NetworkKey     string
	OfferID        int64
	DestinationURL string
	AffiliateURL   string
	ShortURL       string
	RedirectRuleID int64
	Campaign       string
	SubID          string
	Status         string
	RawJSON        string
}

func dbInsertLink(db *sql.DB, in LinkInput) (*Link, error) {
	if err := validateAbsoluteURL(in.DestinationURL); err != nil {
		return nil, fmt.Errorf("destination url: %w", err)
	}
	if err := validateAbsoluteURL(in.AffiliateURL); err != nil {
		return nil, fmt.Errorf("affiliate url: %w", err)
	}
	if in.NetworkKey == "" {
		return nil, errors.New("network required")
	}
	if in.Status == "" {
		in.Status = "active"
	}
	if in.RawJSON == "" {
		in.RawJSON = "{}"
	}
	now := nowUTC()
	res, err := db.Exec(`
		INSERT INTO links (
			network_key, offer_id, destination_url, affiliate_url, short_url,
			redirect_rule_id, campaign, subid, status, raw_json, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.NetworkKey, nullInt(in.OfferID), in.DestinationURL, in.AffiliateURL, in.ShortURL,
		in.RedirectRuleID, in.Campaign, in.SubID, in.Status, in.RawJSON, now, now)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return dbGetLink(db, id)
}

func dbUpsertDefaultLink(db *sql.DB, in LinkInput) (*Link, bool, error) {
	if err := validateAbsoluteURL(in.DestinationURL); err != nil {
		return nil, false, fmt.Errorf("destination url: %w", err)
	}
	if err := validateAbsoluteURL(in.AffiliateURL); err != nil {
		return nil, false, fmt.Errorf("affiliate url: %w", err)
	}
	if in.NetworkKey == "" {
		return nil, false, errors.New("network required")
	}
	if in.Status == "" {
		in.Status = "active"
	}
	if in.RawJSON == "" {
		in.RawJSON = "{}"
	}
	var id int64
	err := db.QueryRow(`
		SELECT id FROM links
		WHERE network_key = ? AND COALESCE(offer_id, 0) = ? AND campaign = '' AND subid = ''
		ORDER BY id DESC LIMIT 1`, in.NetworkKey, in.OfferID).Scan(&id)
	if err == nil {
		_, err = db.Exec(`
			UPDATE links
			SET destination_url = ?, affiliate_url = ?, status = ?, raw_json = ?, updated_at = ?
			WHERE id = ?`, in.DestinationURL, in.AffiliateURL, in.Status, in.RawJSON, nowUTC(), id)
		if err != nil {
			return nil, false, err
		}
		link, err := dbGetLink(db, id)
		return link, false, err
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return nil, false, err
	}
	link, err := dbInsertLink(db, in)
	return link, true, err
}

func dbUpdateLinkShortURL(db *sql.DB, id, redirectRuleID int64, shortURL string) (*Link, error) {
	_, err := db.Exec(`UPDATE links SET short_url = ?, redirect_rule_id = ?, updated_at = ? WHERE id = ?`,
		shortURL, redirectRuleID, nowUTC(), id)
	if err != nil {
		return nil, err
	}
	return dbGetLink(db, id)
}

func dbFindLinkForRedirectHit(db *sql.DB, hit RedirectHit) (*Link, string, error) {
	rows, err := db.Query(`SELECT `+linkCols+` FROM links WHERE redirect_rule_id = ? ORDER BY id`, hit.RuleID)
	if err != nil {
		return nil, "", err
	}
	linked, err := scanLinks(rows)
	if err != nil {
		return nil, "", err
	}
	if len(linked) > 1 {
		return nil, "", fmt.Errorf("redirect rule %d is attached to multiple affiliate links", hit.RuleID)
	}
	if len(linked) == 1 {
		return &linked[0], "rule_id", nil
	}

	for _, candidate := range []struct {
		value           string
		method          string
		allowExtraQuery bool
	}{{hit.Destination, "destination", false}, {hit.Target, "target", true}} {
		if strings.TrimSpace(candidate.value) == "" {
			continue
		}
		matches, err := dbLinksMatchingAffiliateURL(db, candidate.value, candidate.allowExtraQuery)
		if err != nil {
			return nil, "", err
		}
		if len(matches) > 1 {
			return nil, "", fmt.Errorf("redirect rule %d %s matches multiple affiliate links", hit.RuleID, candidate.method)
		}
		if len(matches) == 1 {
			if matches[0].RedirectRuleID != 0 && matches[0].RedirectRuleID != hit.RuleID {
				return nil, "", fmt.Errorf("affiliate link %d is already attached to redirect rule %d", matches[0].ID, matches[0].RedirectRuleID)
			}
			return &matches[0], candidate.method, nil
		}
	}
	return nil, "", nil
}

func dbLinksMatchingAffiliateURL(db *sql.DB, rawURL string, allowExtraQuery bool) ([]Link, error) {
	rows, err := db.Query(`SELECT ` + linkCols + ` FROM links WHERE status = 'active' ORDER BY id`)
	if err != nil {
		return nil, err
	}
	links, err := scanLinks(rows)
	if err != nil {
		return nil, err
	}
	matches := make([]Link, 0, 1)
	for _, link := range links {
		if trackingURLMatches(link.AffiliateURL, rawURL, allowExtraQuery) {
			matches = append(matches, link)
		}
	}
	return matches, nil
}

func trackingURLMatches(affiliateURL, observedURL string, allowExtraQuery bool) bool {
	affiliateCanonical, err := canonicalTrackingURL(affiliateURL)
	if err != nil {
		return false
	}
	observedCanonical, err := canonicalTrackingURL(observedURL)
	if err != nil {
		return false
	}
	if affiliateCanonical == observedCanonical {
		return true
	}
	if !allowExtraQuery {
		return false
	}
	affiliate, _ := url.Parse(affiliateCanonical)
	observed, _ := url.Parse(observedCanonical)
	if affiliate.Scheme != observed.Scheme || affiliate.Host != observed.Host || affiliate.Path != observed.Path {
		return false
	}
	observedQuery := observed.Query()
	for key, requiredValues := range affiliate.Query() {
		available := append([]string(nil), observedQuery[key]...)
		for _, required := range requiredValues {
			matched := false
			for i, value := range available {
				if value == required {
					available = append(available[:i], available[i+1:]...)
					matched = true
					break
				}
			}
			if !matched {
				return false
			}
		}
	}
	return true
}

func scanLinks(rows *sql.Rows) ([]Link, error) {
	defer rows.Close()
	var links []Link
	for rows.Next() {
		link, err := scanLink(rows)
		if err != nil {
			return nil, err
		}
		links = append(links, *link)
	}
	return links, rows.Err()
}

func canonicalTrackingURL(raw string) (string, error) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Scheme == "" || u.Host == "" {
		return "", errors.New("invalid absolute URL")
	}
	u.Scheme = strings.ToLower(u.Scheme)
	host := strings.ToLower(u.Hostname())
	if port := u.Port(); port != "" && !((u.Scheme == "https" && port == "443") || (u.Scheme == "http" && port == "80")) {
		host += ":" + port
	}
	u.Host = host
	u.Fragment = ""
	if u.Path == "" {
		u.Path = "/"
	}
	u.RawQuery = u.Query().Encode()
	return u.String(), nil
}

func dbAttachRedirectRule(db *sql.DB, linkID, ruleID int64) error {
	if linkID == 0 || ruleID == 0 {
		return errors.New("link_id and redirect_rule_id required")
	}
	var attachedLinkID int64
	err := db.QueryRow(`SELECT id FROM links WHERE redirect_rule_id = ? AND id != ? LIMIT 1`, ruleID, linkID).Scan(&attachedLinkID)
	if err == nil {
		return fmt.Errorf("redirect rule %d is already attached to affiliate link %d", ruleID, attachedLinkID)
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return err
	}
	res, err := db.Exec(`UPDATE links SET redirect_rule_id = ?, updated_at = ? WHERE id = ? AND redirect_rule_id IN (0, ?)`, ruleID, nowUTC(), linkID, ruleID)
	if err != nil {
		return err
	}
	updated, _ := res.RowsAffected()
	if updated != 1 {
		return fmt.Errorf("affiliate link %d could not be attached to redirect rule %d", linkID, ruleID)
	}
	return nil
}

func dbUpsertRedirectClick(db *sql.DB, linkID int64, hit RedirectHit, matchedBy string) error {
	if linkID == 0 || hit.RuleID == 0 || hit.Date == "" {
		return errors.New("redirect click requires link_id, rule_id, and date")
	}
	if hit.HasDaySnapshot {
		_, err := db.Exec(`
			INSERT INTO redirect_clicks_daily (
				date, redirect_rule_id, link_id, clicks, hits_total, matched_by,
				last_event_at, raw_json, updated_at
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
			ON CONFLICT(date, redirect_rule_id) DO UPDATE SET
				link_id = excluded.link_id,
				clicks = MAX(redirect_clicks_daily.clicks, excluded.clicks),
				hits_total = MAX(redirect_clicks_daily.hits_total, excluded.hits_total),
				matched_by = excluded.matched_by,
				last_event_at = excluded.last_event_at,
				raw_json = excluded.raw_json,
				updated_at = excluded.updated_at`,
			hit.Date, hit.RuleID, linkID, hit.DayHits, hit.HitsTotal, matchedBy,
			hit.At, hit.RawJSON, nowUTC())
		return err
	}
	_, err := db.Exec(`
		INSERT INTO redirect_clicks_daily (
			date, redirect_rule_id, link_id, clicks, hits_total, matched_by,
			last_event_at, raw_json, updated_at
		) VALUES (?, ?, ?, 1, ?, ?, ?, ?, ?)
		ON CONFLICT(date, redirect_rule_id) DO UPDATE SET
			link_id = excluded.link_id,
			clicks = redirect_clicks_daily.clicks + 1,
			hits_total = MAX(redirect_clicks_daily.hits_total, excluded.hits_total),
			matched_by = excluded.matched_by,
			last_event_at = excluded.last_event_at,
			raw_json = excluded.raw_json,
			updated_at = excluded.updated_at`,
		hit.Date, hit.RuleID, linkID, hit.HitsTotal, matchedBy, hit.At, hit.RawJSON, nowUTC())
	return err
}

func dbRedirectRuleLinks(db *sql.DB) (map[int64]int64, error) {
	rows, err := db.Query(`SELECT redirect_rule_id, id FROM links WHERE redirect_rule_id != 0 ORDER BY redirect_rule_id, id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := map[int64]int64{}
	for rows.Next() {
		var ruleID, linkID int64
		if err := rows.Scan(&ruleID, &linkID); err != nil {
			return nil, err
		}
		if existing := out[ruleID]; existing != 0 && existing != linkID {
			return nil, fmt.Errorf("redirect rule %d is attached to multiple affiliate links", ruleID)
		}
		out[ruleID] = linkID
	}
	return out, rows.Err()
}

func dbGetLink(db *sql.DB, id int64) (*Link, error) {
	row := db.QueryRow(`SELECT `+linkCols+` FROM links WHERE id = ?`, id)
	l, err := scanLink(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return l, err
}

const linkCols = `id, network_key, COALESCE(offer_id,0), destination_url, affiliate_url,
	short_url, redirect_rule_id, campaign, subid, status, raw_json, last_checked_at,
	created_at, updated_at`

const linkColsQualified = `l.id, l.network_key, COALESCE(l.offer_id,0), l.destination_url, l.affiliate_url,
	l.short_url, l.redirect_rule_id, l.campaign, l.subid, l.status, l.raw_json, l.last_checked_at,
	l.created_at, l.updated_at`

func dbListLinks(db *sql.DB, q, network string, offerID int64, status string, limit, offset int) ([]Link, int, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	where := []string{"1=1"}
	args := []any{}
	if network != "" {
		where = append(where, "l.network_key = ?")
		args = append(args, network)
	}
	if offerID != 0 {
		where = append(where, "l.offer_id = ?")
		args = append(args, offerID)
	}
	if status != "" {
		where = append(where, "l.status = ?")
		args = append(args, status)
	}
	if q != "" {
		like := "%" + q + "%"
		where = append(where, `(l.destination_url LIKE ? OR l.affiliate_url LIKE ? OR l.short_url LIKE ?
			OR l.campaign LIKE ? OR l.subid LIKE ? OR o.merchant_name LIKE ? OR o.offer_name LIKE ?)`)
		args = append(args, like, like, like, like, like, like, like)
	}
	if offset < 0 {
		offset = 0
	}
	var total int
	if err := db.QueryRow(`SELECT COUNT(*) FROM links l LEFT JOIN offers o ON o.id = l.offer_id WHERE `+strings.Join(where, " AND "), args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, limit, offset)
	rows, err := db.Query(`SELECT `+linkColsQualified+`, COALESCE(o.merchant_name,''), COALESCE(o.offer_name,'')
		FROM links l
		LEFT JOIN offers o ON o.id = l.offer_id
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY l.id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Link
	for rows.Next() {
		var l Link
		if err := rows.Scan(&l.ID, &l.NetworkKey, &l.OfferID, &l.DestinationURL, &l.AffiliateURL,
			&l.ShortURL, &l.RedirectRuleID, &l.Campaign, &l.SubID, &l.Status, &l.RawJSON,
			&l.LastCheckedAt, &l.CreatedAt, &l.UpdatedAt, &l.MerchantName, &l.OfferName); err != nil {
			return nil, 0, err
		}
		out = append(out, l)
	}
	return out, total, rows.Err()
}

func dbFindLinkForStat(db *sql.DB, network string, offerID int64, raw map[string]any) (int64, error) {
	references := []string{}
	for _, key := range []string{
		"subid", "subId", "clickref", "clickRef", "SubId1", "SubId2", "sourceId", "source_id", "clickId", "click_id",
		"reference.ref1", "reference.ref2", "reference.ref3", "reference.ref4", "reference.ref5",
	} {
		if value := firstString(raw, key); value != "" {
			references = append(references, value)
		}
	}
	if len(references) == 0 {
		return 0, nil
	}
	where := []string{"network_key = ?", "status = 'active'"}
	args := []any{network}
	if offerID != 0 {
		where = append(where, "COALESCE(offer_id, 0) = ?")
		args = append(args, offerID)
	}
	refs := make([]string, 0, len(references)*2)
	for _, reference := range references {
		refs = append(refs, "campaign = ?", "subid = ?")
		args = append(args, reference, reference)
	}
	where = append(where, "("+strings.Join(refs, " OR ")+")")
	var id int64
	err := db.QueryRow(`SELECT id FROM links WHERE `+strings.Join(where, " AND ")+` ORDER BY id DESC LIMIT 1`, args...).Scan(&id)
	if errors.Is(err, sql.ErrNoRows) {
		return 0, nil
	}
	return id, err
}

func scanLink(s rowScanner) (*Link, error) {
	var l Link
	if err := s.Scan(&l.ID, &l.NetworkKey, &l.OfferID, &l.DestinationURL, &l.AffiliateURL,
		&l.ShortURL, &l.RedirectRuleID, &l.Campaign, &l.SubID, &l.Status, &l.RawJSON,
		&l.LastCheckedAt, &l.CreatedAt, &l.UpdatedAt); err != nil {
		return nil, err
	}
	return &l, nil
}

type StatInput struct {
	Date            string
	NetworkKey      string
	OfferID         int64
	LinkID          int64
	Clicks          int64
	Conversions     int64
	RevenueCents    int64
	CommissionCents int64
	Currency        string
	RawJSON         string
}

func dbUpsertStat(db *sql.DB, in StatInput) error {
	if in.Date == "" {
		return errors.New("date required")
	}
	if in.NetworkKey == "" {
		return errors.New("network required")
	}
	if in.Currency == "" {
		in.Currency = "USD"
	}
	if in.RawJSON == "" {
		in.RawJSON = "{}"
	}
	now := nowUTC()
	_, err := db.Exec(`
		INSERT INTO stats_daily (
			date, network_key, offer_id, link_id, clicks, conversions,
			revenue_cents, commission_cents, currency, raw_json, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(date, network_key, offer_id, link_id, currency) DO UPDATE SET
			clicks = excluded.clicks,
			conversions = excluded.conversions,
			revenue_cents = excluded.revenue_cents,
			commission_cents = excluded.commission_cents,
			raw_json = excluded.raw_json,
			updated_at = excluded.updated_at`,
		in.Date, in.NetworkKey, in.OfferID, in.LinkID, in.Clicks, in.Conversions,
		in.RevenueCents, in.CommissionCents, in.Currency, in.RawJSON, now)
	return err
}

func dbStats(db *sql.DB, from, to, network string, offerID, linkID int64, groupBy string) ([]StatRow, error) {
	if groupBy == "" {
		groupBy = "day"
	}
	selectCols := "date, '' AS network_key, 0 AS offer_id, 0 AS link_id"
	groupCols := "date"
	switch groupBy {
	case "network":
		selectCols = "'' AS date, network_key, 0 AS offer_id, 0 AS link_id"
		groupCols = "network_key"
	case "offer":
		selectCols = "'' AS date, '' AS network_key, COALESCE(offer_id,0) AS offer_id, 0 AS link_id"
		groupCols = "offer_id"
	case "link":
		selectCols = "'' AS date, '' AS network_key, 0 AS offer_id, COALESCE(link_id,0) AS link_id"
		groupCols = "link_id"
	case "day":
	default:
		return nil, fmt.Errorf("invalid group_by %q", groupBy)
	}
	where := []string{"1=1"}
	args := []any{}
	if from != "" {
		where = append(where, "date >= ?")
		args = append(args, from)
	}
	if to != "" {
		where = append(where, "date <= ?")
		args = append(args, to)
	}
	if network != "" {
		where = append(where, "network_key = ?")
		args = append(args, network)
	}
	if offerID != 0 {
		where = append(where, "offer_id = ?")
		args = append(args, offerID)
	}
	if linkID != 0 {
		where = append(where, "link_id = ?")
		args = append(args, linkID)
	}
	q := fmt.Sprintf(`SELECT %s, SUM(clicks), SUM(conversions), SUM(revenue_cents), SUM(commission_cents), currency
		FROM stats_daily WHERE %s GROUP BY %s, currency ORDER BY %s`,
		selectCols, strings.Join(where, " AND "), groupCols, groupCols)
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StatRow
	for rows.Next() {
		var s StatRow
		if err := rows.Scan(&s.Date, &s.NetworkKey, &s.OfferID, &s.LinkID, &s.Clicks, &s.Conversions, &s.RevenueCents, &s.CommissionCents, &s.Currency); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	redirectSelect := "r.date, '' AS network_key, 0 AS offer_id, 0 AS link_id"
	redirectGroup := "r.date"
	switch groupBy {
	case "network":
		redirectSelect = "'' AS date, l.network_key, 0 AS offer_id, 0 AS link_id"
		redirectGroup = "l.network_key"
	case "offer":
		redirectSelect = "'' AS date, '' AS network_key, COALESCE(l.offer_id,0) AS offer_id, 0 AS link_id"
		redirectGroup = "l.offer_id"
	case "link":
		redirectSelect = "'' AS date, '' AS network_key, 0 AS offer_id, r.link_id"
		redirectGroup = "r.link_id"
	}
	redirectWhere := []string{"1=1"}
	redirectArgs := []any{}
	if from != "" {
		redirectWhere = append(redirectWhere, "r.date >= ?")
		redirectArgs = append(redirectArgs, from)
	}
	if to != "" {
		redirectWhere = append(redirectWhere, "r.date <= ?")
		redirectArgs = append(redirectArgs, to)
	}
	if network != "" {
		redirectWhere = append(redirectWhere, "l.network_key = ?")
		redirectArgs = append(redirectArgs, network)
	}
	if offerID != 0 {
		redirectWhere = append(redirectWhere, "COALESCE(l.offer_id,0) = ?")
		redirectArgs = append(redirectArgs, offerID)
	}
	if linkID != 0 {
		redirectWhere = append(redirectWhere, "r.link_id = ?")
		redirectArgs = append(redirectArgs, linkID)
	}
	redirectRows, err := db.Query(fmt.Sprintf(`
		SELECT %s, SUM(r.clicks)
		FROM redirect_clicks_daily r
		JOIN links l ON l.id = r.link_id
		WHERE %s
		GROUP BY %s
		ORDER BY %s`, redirectSelect, strings.Join(redirectWhere, " AND "), redirectGroup, redirectGroup), redirectArgs...)
	if err != nil {
		return nil, err
	}
	defer redirectRows.Close()
	byGroup := map[string]int{}
	for i := range out {
		key := statRowGroupKey(out[i], groupBy)
		if _, exists := byGroup[key]; !exists {
			byGroup[key] = i
		}
	}
	for redirectRows.Next() {
		var redirect StatRow
		if err := redirectRows.Scan(&redirect.Date, &redirect.NetworkKey, &redirect.OfferID, &redirect.LinkID, &redirect.RedirectClicks); err != nil {
			return nil, err
		}
		key := statRowGroupKey(redirect, groupBy)
		if index, exists := byGroup[key]; exists {
			out[index].RedirectClicks += redirect.RedirectClicks
			continue
		}
		byGroup[key] = len(out)
		out = append(out, redirect)
	}
	if err := redirectRows.Err(); err != nil {
		return nil, err
	}
	sort.SliceStable(out, func(i, j int) bool {
		left := fmt.Sprintf("%s\x00%s\x00%020d\x00%020d\x00%s", out[i].Date, out[i].NetworkKey, out[i].OfferID, out[i].LinkID, out[i].Currency)
		right := fmt.Sprintf("%s\x00%s\x00%020d\x00%020d\x00%s", out[j].Date, out[j].NetworkKey, out[j].OfferID, out[j].LinkID, out[j].Currency)
		return left < right
	})
	return out, nil
}

func statRowGroupKey(row StatRow, groupBy string) string {
	switch groupBy {
	case "network":
		return row.NetworkKey
	case "offer":
		return strconv.FormatInt(row.OfferID, 10)
	case "link":
		return strconv.FormatInt(row.LinkID, 10)
	default:
		return row.Date
	}
}

func dbRedirectTrackingAvailable(db *sql.DB, network string) (bool, error) {
	q := `SELECT EXISTS(SELECT 1 FROM links WHERE redirect_rule_id != 0`
	args := []any{}
	if network != "" {
		q += ` AND network_key = ?`
		args = append(args, network)
	}
	q += `)`
	var available bool
	if err := db.QueryRow(q, args...).Scan(&available); err != nil {
		return false, err
	}
	return available, nil
}

// --- Tool handlers ---------------------------------------------------------

func (a *App) toolNetworks(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	enabled := optionalBool(args, "enabled")
	rows, err := dbListNetworks(ctx.AppDB(), enabled)
	if err != nil {
		return nil, err
	}
	byKey := make(map[string]*Network, len(rows))
	for i := range rows {
		byKey[rows[i].Key] = &rows[i]
	}
	result := make([]Network, 0, len(providerCapabilities))
	for _, capability := range providerCapabilities {
		network := byKey[capability.Key]
		if network == nil {
			network = &Network{Key: capability.Key, Name: displayNetworkName(capability.Key)}
		}
		enrichNetwork(ctx, network, capability)
		if enabled == nil || network.Enabled == *enabled {
			result = append(result, *network)
		}
	}
	return map[string]any{"networks": result, "count": len(result)}, nil
}

func (a *App) toolRefresh(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	network := strArg(args, "network")
	if network == "" {
		return nil, errors.New("network required")
	}
	network = canonicalNetworkKey(network)
	kind := strArg(args, "kind")
	if kind == "" {
		kind = "all"
	}
	if kind != "offers" && kind != "stats" && kind != "all" {
		return nil, fmt.Errorf("invalid kind %q", kind)
	}
	capability, ok := capabilityFor(network)
	if !ok {
		return nil, fmt.Errorf("unsupported affiliate network %q", network)
	}
	if kind == "offers" && !capability.Offers {
		return nil, fmt.Errorf("%s does not expose offer refresh", network)
	}
	if kind == "stats" && !capability.Stats {
		return nil, fmt.Errorf("%s does not expose stats refresh", network)
	}
	if kind == "all" && !capability.Offers && !capability.Stats {
		return nil, fmt.Errorf("%s has no refreshable offer or stats data", network)
	}
	if (kind == "stats" || kind == "all") && capability.Stats && capability.StatsNeedsDates && (strArg(args, "from") == "" || strArg(args, "to") == "") {
		return nil, fmt.Errorf("%s stats require from and to dates", network)
	}
	return a.refreshNetwork(ctx, network, kind, strArg(args, "from"), strArg(args, "to"), args)
}

func (a *App) toolOffers(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	network := strArg(args, "network")
	if network != "" {
		network = canonicalNetworkKey(network)
	}
	rows, total, err := dbListOffers(ctx.AppDB(), strArg(args, "q"), network, strArg(args, "status"), intArg(args, "limit", 50), intArg(args, "offset", 0))
	if err != nil {
		return nil, err
	}
	return map[string]any{"offers": rows, "count": len(rows), "total": total}, nil
}

func (a *App) toolLinkCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	destination := strArg(args, "url")
	if destination == "" {
		return nil, errors.New("url required")
	}
	if err := validateAbsoluteURL(destination); err != nil {
		return nil, err
	}
	offerID := toInt64(args["offer_id"])
	network := strArg(args, "network")
	if network != "" {
		network = canonicalNetworkKey(network)
	}
	var offer *Offer
	if offerID != 0 {
		var err error
		offer, err = dbGetOffer(ctx.AppDB(), offerID)
		if err != nil {
			return nil, err
		}
		if offer == nil {
			return nil, fmt.Errorf("offer_id %d not found", offerID)
		}
		if network == "" {
			network = offer.NetworkKey
		} else if network != offer.NetworkKey {
			return nil, fmt.Errorf("offer_id %d belongs to %s, not %s", offerID, offer.NetworkKey, network)
		}
	}
	if network == "" {
		return nil, errors.New("network required when offer_id is absent")
	}
	affiliateURL := strArg(args, "affiliate_url")
	raw := map[string]any{}
	if affiliateURL == "" {
		out, err := a.createProviderLink(ctx, network, destination, offer, args)
		if err != nil {
			return nil, err
		}
		raw = out
		affiliateURL = providerAffiliateURL(out)
		if affiliateURL == "" {
			return nil, fmt.Errorf("%s link provider did not return an affiliate URL", network)
		}
	}
	link, err := dbInsertLink(ctx.AppDB(), LinkInput{
		NetworkKey:     network,
		OfferID:        offerID,
		DestinationURL: destination,
		AffiliateURL:   affiliateURL,
		Campaign:       strArg(args, "campaign"),
		SubID:          strArg(args, "subid"),
		Status:         "active",
		RawJSON:        mustJSON(redactSecrets(raw)),
	})
	if err != nil {
		return nil, err
	}
	warning := ""
	if boolArg(args, "shorten", false) {
		shortened, err := a.createRedirect(ctx, link, args, offer)
		if err != nil {
			warning = err.Error()
		} else {
			link = shortened
		}
	}
	return map[string]any{"link": link, "warning": warning}, nil
}

func (a *App) toolLinks(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	network := strArg(args, "network")
	if network != "" {
		network = canonicalNetworkKey(network)
	}
	rows, total, err := dbListLinks(ctx.AppDB(), strArg(args, "q"), network, toInt64(args["offer_id"]), strArg(args, "status"), intArg(args, "limit", 50), intArg(args, "offset", 0))
	if err != nil {
		return nil, err
	}
	return map[string]any{"links": rows, "count": len(rows), "total": total}, nil
}

func normalizedStatsRange(args map[string]any) (string, string, error) {
	from, err := normalizeStatsDate("from", strArg(args, "from"))
	if err != nil {
		return "", "", err
	}
	to, err := normalizeStatsDate("to", strArg(args, "to"))
	if err != nil {
		return "", "", err
	}
	if from != "" && to != "" && from > to {
		return "", "", errors.New("from must be on or before to")
	}
	return from, to, nil
}

func normalizeStatsDate(name, value string) (string, error) {
	value = strings.TrimSpace(value)
	if value == "" {
		return "", nil
	}
	if len(value) == len("2006-01-02") {
		if _, err := time.Parse("2006-01-02", value); err == nil {
			return value, nil
		}
	}
	parsed, err := time.Parse(time.RFC3339, value)
	if err != nil {
		return "", fmt.Errorf("%s must be YYYY-MM-DD or RFC3339", name)
	}
	return parsed.UTC().Format("2006-01-02"), nil
}

func (a *App) toolStats(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	network := strArg(args, "network")
	if network != "" {
		network = canonicalNetworkKey(network)
	}
	from, to, err := normalizedStatsRange(args)
	if err != nil {
		return nil, err
	}
	rows, err := dbStats(ctx.AppDB(), from, to, network, toInt64(args["offer_id"]), toInt64(args["link_id"]), strArg(args, "group_by"))
	if err != nil {
		return nil, err
	}
	unified := make([]UnifiedStatRow, 0, len(rows))
	for _, row := range rows {
		rowNetwork := network
		if rowNetwork == "" {
			rowNetwork = row.NetworkKey
		}
		clicks := row.RedirectClicks
		if capability, ok := capabilityFor(rowNetwork); ok && capability.Clicks {
			clicks = row.Clicks
		}
		unified = append(unified, UnifiedStatRow{
			Date: row.Date, NetworkKey: row.NetworkKey, OfferID: row.OfferID, LinkID: row.LinkID,
			Clicks: clicks, Conversions: row.Conversions, RevenueCents: row.RevenueCents,
			CommissionCents: row.CommissionCents, Currency: row.Currency,
		})
	}
	return map[string]any{"stats": unified, "count": len(unified)}, nil
}

func (a *App) detailedStats(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	network := strArg(args, "network")
	if network != "" {
		network = canonicalNetworkKey(network)
	}
	from, to, err := normalizedStatsRange(args)
	if err != nil {
		return nil, err
	}
	rows, err := dbStats(ctx.AppDB(), from, to, network, toInt64(args["offer_id"]), toInt64(args["link_id"]), strArg(args, "group_by"))
	if err != nil {
		return nil, err
	}
	providerClicksAvailable := false
	if network != "" {
		if capability, ok := capabilityFor(network); ok {
			providerClicksAvailable = capability.Clicks
		}
	}
	redirectClicksAvailable, err := dbRedirectTrackingAvailable(ctx.AppDB(), network)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"stats":                     rows,
		"count":                     len(rows),
		"clicks_available":          providerClicksAvailable || redirectClicksAvailable,
		"provider_clicks_available": providerClicksAvailable,
		"redirect_clicks_available": redirectClicksAvailable,
	}, nil
}

func (a *App) refreshNetwork(ctx *sdk.AppCtx, network, kind, from, to string, args map[string]any) (*RefreshSummary, error) {
	a.refreshMu.Lock()
	defer a.refreshMu.Unlock()

	if ctx.PlatformAPI() == nil {
		return nil, errors.New("platform API unavailable")
	}
	network = canonicalNetworkKey(network)
	capability, ok := capabilityFor(network)
	if !ok {
		return nil, fmt.Errorf("unsupported affiliate network %q", network)
	}
	summary := &RefreshSummary{Network: network, Kind: kind, RefreshedAt: nowUTC()}
	if capability.Offers && (kind == "offers" || kind == "all") {
		calls, err := providerOfferCalls(network, args)
		if err != nil {
			return nil, err
		}
		for _, call := range calls {
			pages, err := executeProviderPages(ctx, call, []string{"offers", "Campaigns", "campaigns", "programs", "partnerships", "advertisers", "data", "items", "results"})
			if err != nil {
				return nil, err
			}
			for _, page := range pages {
				for _, m := range page.Records {
					in := offerInputFromMap(network, m)
					if in.ExternalID == "" || in.MerchantName == "" {
						continue
					}
					offer, err := dbUpsertOffer(ctx.AppDB(), in)
					if err != nil {
						return nil, err
					}
					summary.OffersUpserted++
					if upserted, err := upsertDefaultLinkFromOffer(ctx.AppDB(), network, offer, m); err != nil {
						return nil, err
					} else if upserted {
						summary.LinksUpserted++
					}
				}
				if _, err := dbUpsertNetwork(ctx.AppDB(), network, displayNetworkName(network), page.Output); err != nil {
					return nil, err
				}
			}
		}
	}
	if capability.Stats && (kind == "stats" || kind == "all") {
		calls, err := providerStatCalls(network, from, to, args)
		if err != nil {
			return nil, err
		}
		aggregated := map[string]StatInput{}
		for _, call := range calls {
			pages, err := executeProviderPages(ctx, call, []string{"stats", "Actions", "actions", "transactions", "Transactions", "rewards", "records", "data", "items", "results"})
			if err != nil {
				return nil, err
			}
			for _, page := range pages {
				for _, m := range page.Records {
					row := statInputFromMap(network, m)
					if row.Date == "" {
						continue
					}
					if ext := firstString(m, "offer_external_id", "offerSid", "offerId", "offer_id", "external_id", "offer.sid", "offer.slug", "CampaignId", "campaignId", "advertiserId", "advertiser.id", "merchantGroupId", "company_key"); ext != "" && row.OfferID == 0 {
						if o, err := dbGetOfferByExternal(ctx.AppDB(), network, ext); err == nil && o != nil {
							row.OfferID = o.ID
						}
					}
					if row.LinkID == 0 {
						row.LinkID, err = dbFindLinkForStat(ctx.AppDB(), network, row.OfferID, m)
						if err != nil {
							return nil, err
						}
					}
					key := fmt.Sprintf("%s\x00%s\x00%d\x00%d\x00%s", row.Date, row.NetworkKey, row.OfferID, row.LinkID, row.Currency)
					if existing, ok := aggregated[key]; ok {
						existing.Clicks += row.Clicks
						existing.Conversions += row.Conversions
						existing.RevenueCents += row.RevenueCents
						existing.CommissionCents += row.CommissionCents
						existing.RawJSON = row.RawJSON
						aggregated[key] = existing
						continue
					}
					aggregated[key] = row
				}
				if _, err := dbUpsertNetwork(ctx.AppDB(), network, displayNetworkName(network), page.Output); err != nil {
					return nil, err
				}
			}
		}
		for _, row := range aggregated {
			if err := dbUpsertStat(ctx.AppDB(), row); err != nil {
				return nil, err
			}
			summary.StatsDaysUpserted++
		}
	}
	return summary, nil
}

func upsertDefaultLinkFromOffer(db *sql.DB, network string, offer *Offer, raw map[string]any) (bool, error) {
	if offer == nil {
		return false, nil
	}
	affiliateURL := firstString(raw, "defaultTrackingUrl", "defaultClickTrackingUrl", "clickTrackingUrl", "clickUrl", "trackingLink")
	if affiliateURL == "" {
		return false, nil
	}
	destinationURL := firstString(raw, "targetUrl", "defaultTargetUrl", "destinationUrl", "url")
	if destinationURL == "" {
		destinationURL = affiliateURL
	}
	link, inserted, err := dbUpsertDefaultLink(db, LinkInput{
		NetworkKey:     network,
		OfferID:        offer.ID,
		DestinationURL: destinationURL,
		AffiliateURL:   affiliateURL,
		Status:         "active",
		RawJSON:        mustJSON(redactSecrets(raw)),
	})
	if err != nil || link == nil {
		return false, err
	}
	return inserted, nil
}

func (a *App) createProviderLink(ctx *sdk.AppCtx, network, destination string, offer *Offer, args map[string]any) (map[string]any, error) {
	if ctx.PlatformAPI() == nil {
		return nil, errors.New("platform API unavailable")
	}
	network = canonicalNetworkKey(network)
	call, err := providerLinkCall(network, destination, offer, args)
	if err != nil {
		return nil, err
	}
	out := map[string]any{}
	if err := executeProviderCall(ctx, call, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func executeProviderCall(ctx *sdk.AppCtx, call providerCall, out any) error {
	bound := ctx.IntegrationFor(call.Role)
	if bound == nil || bound.ConnectionID == 0 {
		return fmt.Errorf("%s integration is not bound", call.Role)
	}
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bound.ConnectionID, call.Tool, call.Input)
	if err != nil {
		return fmt.Errorf("%s.%s: %w", call.Role, call.Tool, err)
	}
	if res == nil {
		return fmt.Errorf("%s.%s: empty integration response", call.Role, call.Tool)
	}
	if !res.Success {
		body := string(res.Data)
		if len(body) > 400 {
			body = body[:400]
		}
		return fmt.Errorf("%s.%s failed with status %d: %s", call.Role, call.Tool, res.Status, body)
	}
	if out == nil || len(res.Data) == 0 {
		return nil
	}
	if err := json.Unmarshal(res.Data, out); err != nil {
		return fmt.Errorf("%s.%s decode: %w", call.Role, call.Tool, err)
	}
	return nil
}

func (a *App) createRedirect(ctx *sdk.AppCtx, link *Link, args map[string]any, offer *Offer) (*Link, error) {
	if ctx.PlatformAPI() == nil {
		return nil, errors.New("platform API unavailable")
	}
	hostname := strArg(args, "short_hostname")
	if hostname == "" {
		hostname = configValue("default_short_hostname")
	}
	if hostname == "" {
		return nil, errors.New("short_hostname required when shorten=true and default_short_hostname is not configured")
	}
	path := strArg(args, "short_path")
	if path == "" {
		path = "/" + slugForLink(link, offer)
	}
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	var out struct {
		Redirect struct {
			ID int64 `json:"id"`
		} `json:"redirect"`
	}
	err := ctx.PlatformAPI().CallAppResult("redirects", "redirect_add", map[string]any{
		"hostname":       hostname,
		"path":           path,
		"destination":    link.AffiliateURL,
		"status_code":    302,
		"preserve_query": true,
		"notes":          fmt.Sprintf("affiliate link #%d", link.ID),
	}, &out)
	if err != nil {
		return nil, fmt.Errorf("redirect_add: %w", err)
	}
	shortURL := "https://" + hostname + path
	return dbUpdateLinkShortURL(ctx.AppDB(), link.ID, out.Redirect.ID, shortURL)
}

// --- HTTP handlers ---------------------------------------------------------

func (a *App) handleNetworks(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET", http.StatusMethodNotAllowed)
		return
	}
	args := map[string]any{}
	if r.URL.Query().Has("enabled") {
		args["enabled"] = r.URL.Query().Get("enabled") == "true"
	}
	out, err := a.toolNetworks(globalCtx, args)
	writeToolResponse(w, out, err)
}

func (a *App) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "POST", http.StatusMethodNotAllowed)
		return
	}
	var args map[string]any
	if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
		http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return
	}
	out, err := a.toolRefresh(globalCtx, args)
	writeToolResponse(w, out, err)
}

func (a *App) handleOffers(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	out, err := a.toolOffers(globalCtx, map[string]any{
		"q":       q.Get("q"),
		"network": q.Get("network"),
		"status":  q.Get("status"),
		"limit":   q.Get("limit"),
		"offset":  q.Get("offset"),
	})
	writeToolResponse(w, out, err)
}

func (a *App) handleLinks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		q := r.URL.Query()
		out, err := a.toolLinks(globalCtx, map[string]any{
			"q":        q.Get("q"),
			"network":  q.Get("network"),
			"offer_id": q.Get("offer_id"),
			"status":   q.Get("status"),
			"limit":    q.Get("limit"),
			"offset":   q.Get("offset"),
		})
		writeToolResponse(w, out, err)
	case http.MethodPost:
		var args map[string]any
		if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
			http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
			return
		}
		out, err := a.toolLinkCreate(globalCtx, args)
		writeToolResponse(w, out, err)
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleStats(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "GET", http.StatusMethodNotAllowed)
		return
	}
	q := r.URL.Query()
	out, err := a.detailedStats(globalCtx, map[string]any{
		"from":     q.Get("from"),
		"to":       q.Get("to"),
		"network":  q.Get("network"),
		"offer_id": q.Get("offer_id"),
		"link_id":  q.Get("link_id"),
		"group_by": q.Get("group_by"),
	})
	writeToolResponse(w, out, err)
}

// --- provider routing + normalization --------------------------------------

type providerCall struct {
	Role       string
	Tool       string
	Input      map[string]any
	Pagination *providerPagination
}

type providerPagination struct {
	Mode       string
	Param      string
	PageSize   int
	MaxPages   int
	Start      int
	CursorPath string
}

type providerPage struct {
	Output  map[string]any
	Records []map[string]any
}

func executeProviderPages(ctx *sdk.AppCtx, call providerCall, recordKeys []string) ([]providerPage, error) {
	maxPages := 1
	if call.Pagination != nil {
		maxPages = call.Pagination.MaxPages
		if maxPages <= 0 {
			maxPages = 100
		}
	}
	input := cloneMap(call.Input)
	pages := make([]providerPage, 0, min(maxPages, 8))
	hasMore := false
	for pageIndex := 0; pageIndex < maxPages; pageIndex++ {
		out := map[string]any{}
		pageCall := call
		pageCall.Input = input
		if err := executeProviderCall(ctx, pageCall, &out); err != nil {
			return nil, err
		}
		records := collectMaps(out, recordKeys...)
		pages = append(pages, providerPage{Output: out, Records: records})
		hasMore = responseHasNext(out)
		if call.Pagination == nil || len(records) == 0 {
			break
		}
		pagination := call.Pagination
		switch pagination.Mode {
		case "offset":
			if !hasMore && len(records) < pagination.PageSize {
				return pages, nil
			}
			input = cloneMap(input)
			input[pagination.Param] = pagination.Start + (pageIndex+1)*pagination.PageSize
		case "page":
			if !hasMore && len(records) < pagination.PageSize {
				return pages, nil
			}
			input = cloneMap(input)
			input[pagination.Param] = pagination.Start + pageIndex + 1
		case "cursor":
			cursor := firstString(out, pagination.CursorPath, "next_cursor", "nextCursor", "meta.next_cursor", "pagination.next_cursor")
			if cursor == "" {
				return pages, nil
			}
			input = cloneMap(input)
			input[pagination.Param] = cursor
		default:
			return nil, fmt.Errorf("unsupported pagination mode %q", pagination.Mode)
		}
	}
	if call.Pagination != nil && hasMore {
		return nil, fmt.Errorf("%s.%s exceeded the %d-page safety limit", call.Role, call.Tool, maxPages)
	}
	return pages, nil
}

func responseHasNext(out map[string]any) bool {
	v := firstAny(out, "next", "links.next", "meta.next", "pagination.next", "has_more", "hasMore")
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return strings.TrimSpace(x) != ""
	case map[string]any:
		return len(x) > 0
	default:
		return v != nil
	}
}

func cloneMap(in map[string]any) map[string]any {
	out := make(map[string]any, len(in))
	for key, value := range in {
		out[key] = value
	}
	return out
}

func providerOfferCalls(network string, args map[string]any) ([]providerCall, error) {
	network = canonicalNetworkKey(network)
	switch network {
	case "target-circle":
		limit := intArg(args, "limit", 50)
		if limit <= 0 || limit > 50 {
			limit = 50
		}
		return []providerCall{{Role: "target-circle", Tool: "offers_list", Input: compactMap(map[string]any{
			"limit": limit, "offset": 0, "status": strArg(args, "status"),
		}), Pagination: &providerPagination{Mode: "offset", Param: "offset", PageSize: limit, MaxPages: maxPageArg(args), Start: 0}}}, nil
	case "impact":
		return []providerCall{{Role: "impact", Tool: "programs_list", Input: compactMap(map[string]any{
			"InsertionOrderStatus": argOrConfig(args, "InsertionOrderStatus", "impact_insertion_order_status", "Active"),
			"PageSize":             boundedLimit(args, 100, 1000),
			"Page":                 1,
		}), Pagination: &providerPagination{Mode: "page", Param: "Page", PageSize: boundedLimit(args, 100, 1000), MaxPages: maxPageArg(args), Start: 1}}}, nil
	case "awin":
		publisherID := requiredArgOrConfig(args, "publisherId", "awin_publisher_id")
		if publisherID == "" {
			return nil, errors.New("awin publisherId required; pass publisherId or set awin_publisher_id")
		}
		return []providerCall{{Role: "awin", Tool: "programs_list", Input: compactMap(map[string]any{
			"publisherId":  publisherID,
			"relationship": argOrConfig(args, "relationship", "awin_relationship", "joined"),
			"countryCode":  strArg(args, "countryCode"),
		})}}, nil
	case "cj-affiliate":
		requestorCID := requiredArgOrConfig(args, "requestor-cid", "cj_requestor_cid")
		if requestorCID == "" {
			return nil, errors.New("cj-affiliate requestor-cid required; pass requestor-cid or set cj_requestor_cid")
		}
		return []providerCall{{Role: "cj-affiliate", Tool: "advertisers_lookup", Input: compactMap(map[string]any{
			"requestor-cid":    requestorCID,
			"advertiser-ids":   argOrConfig(args, "advertiser-ids", "cj_advertiser_ids", "joined"),
			"keywords":         strArg(args, "keywords"),
			"records-per-page": boundedLimit(args, 100, 100),
			"page-number":      1,
		}), Pagination: &providerPagination{Mode: "page", Param: "page-number", PageSize: boundedLimit(args, 100, 100), MaxPages: maxPageArg(args), Start: 1}}}, nil
	case "amazon-associates":
		partnerTag := requiredArgOrConfig(args, "partnerTag", "amazon_partner_tag")
		keywords := requiredArgOrConfig(args, "keywords", "amazon_keywords")
		if partnerTag == "" || keywords == "" {
			return nil, errors.New("amazon-associates partnerTag and keywords required; pass them or set amazon_partner_tag and amazon_keywords")
		}
		return []providerCall{{Role: "amazon-associates", Tool: "items_search", Input: compactMap(map[string]any{
			"partnerTag":  partnerTag,
			"marketplace": argOrConfig(args, "marketplace", "amazon_marketplace", "www.amazon.com"),
			"keywords":    keywords,
			"searchIndex": argOrConfig(args, "searchIndex", "amazon_search_index", "All"),
			"itemCount":   boundedLimit(args, 10, 10),
			"itemPage":    1,
			"resources":   defaultAmazonResources(),
		}), Pagination: &providerPagination{Mode: "page", Param: "itemPage", PageSize: boundedLimit(args, 10, 10), MaxPages: maxPageArg(args), Start: 1}}}, nil
	case "sovrn":
		campaignID := requiredArgOrConfig(args, "campaignId", "sovrn_campaign_id")
		if campaignID == "" {
			return nil, errors.New("sovrn campaignId required; pass campaignId or set sovrn_campaign_id")
		}
		return []providerCall{{Role: "sovrn", Tool: "merchants_approved", Input: compactMap(map[string]any{
			"campaignId": toInt64(campaignID),
			"page":       1,
			"pageSize":   boundedLimit(args, 1000, 1000),
			"filters":    firstAny(args, "filters"),
		}), Pagination: &providerPagination{Mode: "page", Param: "page", PageSize: boundedLimit(args, 1000, 1000), MaxPages: maxPageArg(args), Start: 1}}}, nil
	case "partnerstack":
		return []providerCall{{Role: "partnerstack", Tool: "partnerships_list", Input: compactMap(map[string]any{
			"include_offers":   true,
			"include_archived": boolArg(args, "include_archived", false),
			"limit":            boundedLimit(args, 100, 100),
		}), Pagination: &providerPagination{Mode: "cursor", Param: "starting_after", PageSize: boundedLimit(args, 100, 100), MaxPages: maxPageArg(args), CursorPath: "next"}}}, nil
	case "shareasale":
		return []providerCall{{Role: "shareasale", Tool: "merchants_search", Input: compactMap(map[string]any{
			"keyword":   strArg(args, "keyword"),
			"category":  strArg(args, "category"),
			"XMLFormat": 0,
		})}}, nil
	case "skimlinks":
		return nil, errors.New("skimlinks merchant/program refresh is not exposed by the current integration; use skimlinks for link_wrapper_url link creation")
	default:
		return nil, fmt.Errorf("unsupported affiliate network %q", network)
	}
}

func providerStatCalls(network, from, to string, args map[string]any) ([]providerCall, error) {
	network = canonicalNetworkKey(network)
	switch network {
	case "target-circle":
		limit := intArg(args, "limit", 25)
		if limit <= 0 || limit > 25 {
			limit = 25
		}
		return []providerCall{{Role: "target-circle", Tool: "transactions_list", Input: compactMap(map[string]any{
			"savedFrom": firstNonEmpty(from, strArg(args, "savedFrom")),
			"savedTo":   exclusiveEndDate(firstNonEmpty(to, strArg(args, "savedTo"))),
			"offset":    0,
			"limit":     limit,
		}), Pagination: &providerPagination{Mode: "offset", Param: "offset", PageSize: limit, MaxPages: maxPageArg(args), Start: 0}}}, nil
	case "impact":
		return []providerCall{{Role: "impact", Tool: "actions_list", Input: compactMap(map[string]any{
			"ActionDateStart": firstNonEmpty(from, strArg(args, "ActionDateStart")),
			"ActionDateEnd":   firstNonEmpty(to, strArg(args, "ActionDateEnd")),
			"CampaignId":      strArg(args, "CampaignId"),
			"State":           strArg(args, "State"),
			"PageSize":        boundedLimit(args, 100, 1000),
			"Page":            1,
		}), Pagination: &providerPagination{Mode: "page", Param: "Page", PageSize: boundedLimit(args, 100, 1000), MaxPages: maxPageArg(args), Start: 1}}}, nil
	case "awin":
		publisherID := requiredArgOrConfig(args, "publisherId", "awin_publisher_id")
		if publisherID == "" || from == "" || to == "" {
			return nil, errors.New("awin stats require publisherId, from, and to")
		}
		return []providerCall{{Role: "awin", Tool: "transactions_list", Input: compactMap(map[string]any{
			"publisherId": publisherID,
			"startDate":   from,
			"endDate":     to,
			"dateType":    argOrConfig(args, "dateType", "awin_date_type", "transaction"),
		})}}, nil
	case "cj-affiliate":
		query := strArg(args, "query")
		if query == "" {
			query = `query PublisherCommissions($since: DateTime, $before: DateTime) { publisherCommissions(since: $since, before: $before) { count records { actionTrackerName advertiserName commissionId eventDate postingDate saleAmountUsd pubCommissionAmountUsd } } }`
		}
		return []providerCall{{Role: "cj-affiliate", Tool: "commission_query", Input: compactMap(map[string]any{
			"query": query,
			"variables": map[string]any{
				"since":  from,
				"before": to,
			},
		})}}, nil
	case "sovrn":
		dates, err := inclusiveDates(firstNonEmpty(strArg(args, "clickDate"), from, time.Now().UTC().Format("2006-01-02")), firstNonEmpty(to, from))
		if err != nil {
			return nil, err
		}
		calls := make([]providerCall, 0, len(dates))
		for _, date := range dates {
			calls = append(calls, providerCall{Role: "sovrn", Tool: "transactions_report", Input: compactMap(map[string]any{
				"clickDate": date, "campaignIds": strArg(args, "campaignIds"), "merchantGroupIds": strArg(args, "merchantGroupIds"), "programType": strArg(args, "programType"),
			})})
		}
		return calls, nil
	case "partnerstack":
		minCreated := toInt64(firstAny(args, "min_created"))
		maxCreated := toInt64(firstAny(args, "max_created"))
		if minCreated == 0 {
			minCreated = unixDateBoundary(from, false)
		}
		if maxCreated == 0 {
			maxCreated = unixDateBoundary(to, true)
		}
		return []providerCall{{Role: "partnerstack", Tool: "transactions_list", Input: compactMap(map[string]any{
			"min_created": minCreated,
			"max_created": maxCreated,
			"limit":       boundedLimit(args, 100, 100),
		}), Pagination: &providerPagination{Mode: "cursor", Param: "starting_after", PageSize: boundedLimit(args, 100, 100), MaxPages: maxPageArg(args), CursorPath: "next"}}}, nil
	case "shareasale":
		return []providerCall{{Role: "shareasale", Tool: "daily_activity", Input: compactMap(map[string]any{
			"XMLFormat": 0,
		})}}, nil
	case "amazon-associates", "skimlinks":
		return nil, fmt.Errorf("%s does not expose stats through the current integration", network)
	default:
		return nil, fmt.Errorf("unsupported affiliate network %q", network)
	}
}

func providerLinkCall(network, destination string, offer *Offer, args map[string]any) (providerCall, error) {
	network = canonicalNetworkKey(network)
	offerExternalID := ""
	if offer != nil {
		offerExternalID = offer.ExternalID
	}
	switch network {
	case "target-circle":
		offerSID := firstNonEmpty(strArg(args, "offerSid"), offerExternalID)
		if offerSID == "" {
			return providerCall{}, errors.New("target-circle link creation requires offerSid or offer_id")
		}
		return providerCall{Role: "target-circle", Tool: "codes_list", Input: compactMap(map[string]any{
			"offerSid":             offerSID,
			"adInventorySid":       argOrConfig(args, "adInventorySid", "target_circle_ad_inventory_sid", ""),
			"deeplink":             destination,
			"parameters[ref1]":     firstNonEmpty(strArg(args, "campaign"), "apteva"),
			"parameters[click_id]": strArg(args, "subid"),
		})}, nil
	case "impact":
		programID := firstNonEmpty(strArg(args, "program_id"), offerExternalID)
		if programID == "" {
			return providerCall{}, errors.New("impact link creation requires program_id or offer_id")
		}
		return providerCall{Role: "impact", Tool: "tracking_link_create", Input: compactMap(map[string]any{
			"program_id": programID,
			"DeepLink":   destination,
			"SubId1":     strArg(args, "campaign"),
			"SubId2":     strArg(args, "subid"),
		})}, nil
	case "awin":
		publisherID := requiredArgOrConfig(args, "publisherId", "awin_publisher_id")
		if publisherID == "" || offerExternalID == "" {
			return providerCall{}, errors.New("awin link creation requires publisherId and offer_id/advertiserId")
		}
		return providerCall{Role: "awin", Tool: "tracking_link_generate", Input: compactMap(map[string]any{
			"publisherId":    publisherID,
			"advertiserId":   toInt64(offerExternalID),
			"destinationUrl": destination,
			"shorten":        boolArg(args, "provider_shorten", false),
			"parameters": map[string]any{
				"campaign": strArg(args, "campaign"),
				"clickref": strArg(args, "subid"),
			},
		})}, nil
	case "cj-affiliate":
		return providerCall{}, errors.New("cj-affiliate link creation requires manual affiliate_url because links_search cannot generate a deeplink for the requested destination")
	case "amazon-associates":
		partnerTag := requiredArgOrConfig(args, "partnerTag", "amazon_partner_tag")
		if partnerTag == "" {
			return providerCall{}, errors.New("amazon-associates link creation requires partnerTag or amazon_partner_tag")
		}
		asin := firstNonEmpty(strArg(args, "asin"), offerExternalID)
		if asin == "" {
			asin = asinFromURL(destination)
		}
		if asin == "" {
			return providerCall{}, errors.New("amazon-associates link creation requires asin, an Amazon /dp/<ASIN> URL, or an offer with ASIN external_id")
		}
		return providerCall{Role: "amazon-associates", Tool: "items_get", Input: map[string]any{
			"partnerTag":  partnerTag,
			"marketplace": argOrConfig(args, "marketplace", "amazon_marketplace", "www.amazon.com"),
			"itemIds":     []string{asin},
			"itemIdType":  "ASIN",
			"resources":   defaultAmazonResources(),
		}}, nil
	case "skimlinks":
		return providerCall{Role: "skimlinks", Tool: "link_wrapper_url", Input: compactMap(map[string]any{
			"url":   destination,
			"xs":    1,
			"xcust": firstNonEmpty(strArg(args, "subid"), strArg(args, "campaign")),
		})}, nil
	case "sovrn":
		return providerCall{Role: "sovrn", Tool: "affiliate_link_url", Input: compactMap(map[string]any{
			"u":    destination,
			"cuid": firstNonEmpty(strArg(args, "subid"), strArg(args, "campaign")),
		})}, nil
	case "partnerstack", "shareasale":
		return providerCall{}, fmt.Errorf("%s link creation requires manual affiliate_url with the current integration", network)
	default:
		return providerCall{}, fmt.Errorf("unsupported affiliate network %q", network)
	}
}

func offerInputFromMap(network string, m map[string]any) OfferInput {
	merchant := firstString(m, "merchant_name", "merchant", "advertiser.name", "advertiserName", "advertiser_name", "AdvertiserName", "advertiser-name", "merchantGroupName", "company_name")
	name := firstString(m, "offer_name", "offerName", "name", "title", "campaign_name", "CampaignName", "programName", "display_name")
	if merchant == "" {
		merchant = name
	}
	if name == "" {
		name = merchant
	}
	return OfferInput{
		NetworkKey:        network,
		ExternalID:        firstString(m, "external_id", "id", "slug", "sid", "key", "company_key", "asin", "offerSid", "offer_id", "offerId", "campaign_id", "campaignId", "CampaignId", "advertiserId", "advertiser-id", "merchantGroupId"),
		MerchantName:      merchant,
		OfferName:         name,
		Status:            strings.ToLower(firstString(m, "status", "relationship_status", "relationship-status", "approval_status", "approvalStatus", "ContractStatus")),
		Category:          firstString(m, "category", "primaryCategory", "Category"),
		Vertical:          firstString(m, "vertical"),
		CountriesJSON:     mustJSON(firstAny(m, "countries", "country", "targetedCountries", "allowed_countries", "ShippingRegions")),
		CommissionSummary: firstNonEmpty(firstString(m, "commission_summary", "commission", "payout", "payout_summary", "commissionRange", "defaultCommissionRate", "rate"), pricingSummaryFromMap(m)),
		CookieWindow:      firstString(m, "cookie_window", "cookieWindow", "cookie_expiration", "cookieExpiration", "cookieDuration", "tracking.cookieExpiration"),
		TrackingDeepLink:  firstBool(m, "tracking_deeplink", "deeplinking", "tracking.deeplinking", "deepLinking", "deeplink", "AllowsDeeplinking"),
		RawJSON:           mustJSON(redactSecrets(m)),
	}
}

func pricingSummaryFromMap(m map[string]any) string {
	raw := firstAny(m, "pricings", "priceCombinations")
	items, ok := raw.([]any)
	if !ok || len(items) == 0 {
		return ""
	}
	parts := []string{}
	for _, item := range items {
		pm, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if payout, ok := firstAny(pm, "payout").(map[string]any); ok {
			value := firstString(payout, "value")
			if value == "" {
				continue
			}
			typ := firstString(payout, "type")
			currency := firstString(payout, "currency")
			transactionType := firstString(pm, "transactionType")
			if typ == "percentage" {
				parts = append(parts, strings.TrimSpace(fmt.Sprintf("%s%% %s", value, transactionType)))
			} else {
				parts = append(parts, strings.TrimSpace(fmt.Sprintf("%s %s %s", value, currency, transactionType)))
			}
			continue
		}
		value := firstString(pm, "payout", "value")
		if value == "" {
			continue
		}
		commissionType := firstString(pm, "commissionType")
		transactionType := firstString(pm, "transactionType")
		currency := firstString(pm, "currency")
		parts = append(parts, strings.TrimSpace(fmt.Sprintf("%s %s %s %s", commissionType, transactionType, value, currency)))
	}
	if len(parts) > 3 {
		parts = parts[:3]
	}
	return strings.Join(parts, "; ")
}

func statInputFromMap(network string, m map[string]any) StatInput {
	date := normalizeStatDate(firstString(m, "date", "day", "ActionDate", "eventDate", "postingDate", "transactionDate", "saved", "clickSaved", "updatedAt", "validationDate", "click.clickDate", "commission.commissionDate", "commission.updateDate", "created_at"))
	transactionType := strings.ToLower(firstString(m, "transactionType", "transaction_type", "type"))
	clicks := toInt64(firstAny(m, "clicks", "Clicks"))
	conversions := toInt64(firstAny(m, "conversions", "sales", "transactions", "Actions"))
	if transactionType == "click" || transactionType == "4" {
		if clicks == 0 {
			clicks = 1
		}
	} else if conversions == 0 && firstString(m, "transactionId", "transactionHash", "commissionId") != "" {
		conversions = 1
	}
	revenueCents := centsFromAny(firstAny(m, "revenue_cents"))
	if revenueCents == 0 {
		revenueCents = moneyCentsFromAny(firstAny(m, "revenue", "order_value", "orderValue", "transactionAmount", "saleAmountUsd", "SaleAmount", "commission.orderValue"))
	}
	commissionCents := centsFromAny(firstAny(m, "commission_cents"))
	if commissionCents == 0 {
		commissionCents = moneyCentsFromAny(firstAny(m, "commission", "payout", "Payout", "pubCommissionAmountUsd", "publisherNetRevenue", "commission.publisherNetRevenue"))
	}
	return StatInput{
		Date:            date,
		NetworkKey:      network,
		OfferID:         toInt64(firstAny(m, "offer_id")),
		LinkID:          toInt64(firstAny(m, "link_id")),
		Clicks:          clicks,
		Conversions:     conversions,
		RevenueCents:    revenueCents,
		CommissionCents: commissionCents,
		Currency:        firstNonEmpty(firstString(m, "currency", "Currency"), "USD"),
		RawJSON:         mustJSON(redactSecrets(m)),
	}
}

func normalizeStatDate(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	if len(s) >= 10 {
		return s[:10]
	}
	return s
}

func collectMaps(root map[string]any, keys ...string) []map[string]any {
	var out []map[string]any
	var walk func(any)
	walk = func(v any) {
		switch x := v.(type) {
		case []any:
			for _, item := range x {
				walk(item)
			}
		case map[string]any:
			for _, key := range keys {
				if child, ok := x[key]; ok {
					walk(child)
				}
			}
			if looksLikeRecord(x) {
				out = append(out, x)
			}
		}
	}
	for _, key := range keys {
		if v, ok := root[key]; ok {
			walk(v)
		}
	}
	if len(out) == 0 && looksLikeRecord(root) {
		out = append(out, root)
	}
	return out
}

func looksLikeRecord(m map[string]any) bool {
	return firstString(m, "external_id", "id", "slug", "sid", "key", "asin", "offerSid", "offer_id", "offerId", "CampaignId", "campaignId", "advertiserId", "merchantGroupId", "company_key", "date", "day", "ActionDate", "eventDate", "transactionDate") != ""
}

// --- helpers ---------------------------------------------------------------

type rowScanner interface {
	Scan(dest ...any) error
}

func schemaObject(props map[string]any, required []string) map[string]any {
	out := map[string]any{"type": "object", "properties": props}
	if len(required) > 0 {
		out["required"] = required
	}
	return out
}

func writeToolResponse(w http.ResponseWriter, out any, err error) {
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	writeJSON(w, out)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func validateAbsoluteURL(s string) error {
	u, err := url.Parse(s)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return errors.New("url must use http or https")
	}
	if u.Host == "" {
		return errors.New("url must include a host")
	}
	return nil
}

func nowUTC() string {
	return time.Now().UTC().Format(time.RFC3339)
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}

func nullInt(v int64) any {
	if v == 0 {
		return nil
	}
	return v
}

func strArg(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	if v, ok := args[key]; ok {
		switch x := v.(type) {
		case string:
			return strings.TrimSpace(x)
		case fmt.Stringer:
			return strings.TrimSpace(x.String())
		}
	}
	return ""
}

func intArg(args map[string]any, key string, def int) int {
	if args == nil {
		return def
	}
	if v, ok := args[key]; ok {
		n := toInt64(v)
		if n != 0 {
			return int(n)
		}
	}
	return def
}

func boundedLimit(args map[string]any, def, maxValue int) int {
	value := intArg(args, "limit", def)
	if value <= 0 || value > maxValue {
		return def
	}
	return value
}

func maxPageArg(args map[string]any) int {
	value := intArg(args, "pages", 100)
	if value <= 0 || value > 500 {
		return 100
	}
	return value
}

func inclusiveDates(from, to string) ([]string, error) {
	if to == "" {
		to = from
	}
	start, err := time.Parse("2006-01-02", from)
	if err != nil {
		return nil, fmt.Errorf("invalid from date %q", from)
	}
	end, err := time.Parse("2006-01-02", to)
	if err != nil {
		return nil, fmt.Errorf("invalid to date %q", to)
	}
	if end.Before(start) {
		return nil, errors.New("to date must not be before from date")
	}
	if end.Sub(start) > 366*24*time.Hour {
		return nil, errors.New("date range must not exceed 366 days")
	}
	result := []string{}
	for date := start; !date.After(end); date = date.AddDate(0, 0, 1) {
		result = append(result, date.Format("2006-01-02"))
	}
	return result, nil
}

func exclusiveEndDate(value string) string {
	if value == "" {
		return ""
	}
	date, err := time.Parse("2006-01-02", value)
	if err != nil {
		return value
	}
	return date.AddDate(0, 0, 1).Format("2006-01-02")
}

func unixDateBoundary(value string, endOfDay bool) int64 {
	if value == "" {
		return 0
	}
	date, err := time.Parse("2006-01-02", value)
	if err != nil {
		return 0
	}
	if endOfDay {
		date = date.AddDate(0, 0, 1).Add(-time.Second)
	}
	return date.Unix()
}

func boolArg(args map[string]any, key string, def bool) bool {
	if args == nil {
		return def
	}
	if v, ok := args[key]; ok {
		if b, ok := v.(bool); ok {
			return b
		}
		if s, ok := v.(string); ok {
			return s == "true" || s == "1"
		}
	}
	return def
}

func canonicalNetworkKey(key string) string {
	switch strings.ToLower(strings.TrimSpace(key)) {
	case "circlewise", "targetcircle", "target-circle":
		return "target-circle"
	case "cj", "commission-junction", "cj-affiliate":
		return "cj-affiliate"
	default:
		return strings.ToLower(strings.TrimSpace(key))
	}
}

func compactMap(in map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range in {
		if v == nil {
			continue
		}
		switch x := v.(type) {
		case string:
			if strings.TrimSpace(x) == "" {
				continue
			}
		case int:
			if x == 0 && (strings.EqualFold(k, "limit") || strings.EqualFold(k, "PageSize") || strings.EqualFold(k, "pageSize")) {
				continue
			}
		case int64:
			if x == 0 && (strings.EqualFold(k, "campaignId") || strings.EqualFold(k, "min_created") || strings.EqualFold(k, "max_created")) {
				continue
			}
		}
		out[k] = v
	}
	return out
}

func argOrConfig(args map[string]any, argName, configName, def string) string {
	if v := strArg(args, argName); v != "" {
		return v
	}
	if v := configValue(configName); v != "" {
		return v
	}
	return def
}

func requiredArgOrConfig(args map[string]any, argName, configName string) string {
	return argOrConfig(args, argName, configName, "")
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func defaultAmazonResources() []string {
	return []string{
		"images.primary.medium",
		"itemInfo.title",
		"itemInfo.byLineInfo",
		"offersV2.listings.price",
	}
}

func asinFromURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return ""
	}
	parts := strings.Split(strings.Trim(u.Path, "/"), "/")
	for i, part := range parts {
		if (part == "dp" || part == "gp" || part == "product") && i+1 < len(parts) {
			return strings.TrimSpace(parts[i+1])
		}
		if len(part) == 10 && strings.HasPrefix(part, "B") {
			return part
		}
	}
	return ""
}

func providerAffiliateURL(out map[string]any) string {
	for _, key := range []string{
		"affiliate_url", "clickUrl", "click_url", "trackingLink", "TrackingLink",
		"optimized", "url", "detailPageURL", "searchURL", "shortUrl",
	} {
		if v := firstString(out, key); strings.HasPrefix(v, "http://") || strings.HasPrefix(v, "https://") {
			return v
		}
	}
	if v := findURL(out); v != "" {
		return v
	}
	return ""
}

func findURL(v any) string {
	switch x := v.(type) {
	case string:
		if strings.HasPrefix(x, "http://") || strings.HasPrefix(x, "https://") {
			return x
		}
	case []any:
		for _, item := range x {
			if s := findURL(item); s != "" {
				return s
			}
		}
	case map[string]any:
		for _, key := range []string{"affiliate_url", "clickUrl", "click_url", "trackingLink", "TrackingLink", "optimized", "detailPageURL", "searchURL", "url"} {
			if s := firstString(x, key); strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://") {
				return s
			}
		}
		keys := make([]string, 0, len(x))
		for key := range x {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if s := findURL(x[key]); s != "" {
				return s
			}
		}
	}
	return ""
}

func optionalBool(args map[string]any, key string) *bool {
	if args == nil {
		return nil
	}
	if _, ok := args[key]; !ok {
		return nil
	}
	v := boolArg(args, key, false)
	return &v
}

func toInt64(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case float64:
		return int64(x)
	case json.Number:
		n, _ := x.Int64()
		return n
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(x), 10, 64)
		return n
	}
	return 0
}

func centsFromAny(v any) int64 {
	switch x := v.(type) {
	case int64, int, float64, json.Number:
		return toInt64(x)
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return 0
		}
		if strings.Contains(s, ".") {
			f, _ := strconv.ParseFloat(s, 64)
			return int64(math.Round(f * 100))
		}
		return toInt64(s)
	}
	return 0
}

func moneyCentsFromAny(v any) int64 {
	switch x := v.(type) {
	case float64:
		return int64(math.Round(x * 100))
	case int:
		return int64(x * 100)
	case int64:
		return x * 100
	case json.Number:
		f, _ := strconv.ParseFloat(x.String(), 64)
		return int64(math.Round(f * 100))
	case string:
		s := strings.TrimSpace(x)
		if s == "" {
			return 0
		}
		f, _ := strconv.ParseFloat(s, 64)
		return int64(math.Round(f * 100))
	}
	return 0
}

func firstAny(m map[string]any, keys ...string) any {
	for _, key := range keys {
		if strings.Contains(key, ".") {
			if v, ok := lookupPath(m, key); ok && v != nil {
				return v
			}
			continue
		}
		if v, ok := m[key]; ok && v != nil {
			return v
		}
		norm := normalizeLookupKey(key)
		for mk, mv := range m {
			if normalizeLookupKey(mk) == norm && mv != nil {
				return mv
			}
		}
	}
	return nil
}

func lookupPath(m map[string]any, path string) (any, bool) {
	var cur any = m
	for _, part := range strings.Split(path, ".") {
		obj, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if v, ok := obj[part]; ok {
			cur = v
			continue
		}
		norm := normalizeLookupKey(part)
		found := false
		for mk, mv := range obj {
			if normalizeLookupKey(mk) == norm {
				cur = mv
				found = true
				break
			}
		}
		if !found {
			return nil, false
		}
	}
	return cur, true
}

func normalizeLookupKey(key string) string {
	key = strings.ToLower(key)
	key = strings.ReplaceAll(key, "_", "")
	key = strings.ReplaceAll(key, "-", "")
	key = strings.ReplaceAll(key, " ", "")
	return key
}

func firstString(m map[string]any, keys ...string) string {
	v := firstAny(m, keys...)
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case float64:
		return strconv.FormatInt(int64(x), 10)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case json.Number:
		return x.String()
	}
	return ""
}

func firstBool(m map[string]any, keys ...string) bool {
	v := firstAny(m, keys...)
	switch x := v.(type) {
	case bool:
		return x
	case string:
		return x == "true" || x == "1" || x == "yes"
	case float64:
		return x != 0
	case int:
		return x != 0
	}
	return false
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

func redactSecrets(v any) any {
	switch value := v.(type) {
	case map[string]any:
		out := make(map[string]any, len(value))
		for key, child := range value {
			normalized := normalizeLookupKey(key)
			if normalized == "token" || normalized == "apitoken" || normalized == "apikey" || normalized == "accesstoken" || normalized == "refreshtoken" || normalized == "authorization" {
				out[key] = "[REDACTED]"
				continue
			}
			out[key] = redactSecrets(child)
		}
		return out
	case []any:
		out := make([]any, len(value))
		for i, child := range value {
			out[i] = redactSecrets(child)
		}
		return out
	default:
		return v
	}
}

func displayNetworkName(key string) string {
	switch canonicalNetworkKey(key) {
	case "cj-affiliate":
		return "CJ Affiliate"
	case "awin":
		return "Awin"
	case "impact":
		return "Impact"
	case "target-circle":
		return "Target Circle (Circlewise)"
	case "amazon-associates":
		return "Amazon Associates"
	case "skimlinks":
		return "Skimlinks"
	case "sovrn":
		return "Sovrn"
	case "partnerstack":
		return "PartnerStack"
	case "shareasale":
		return "ShareASale"
	default:
		return strings.Title(strings.ReplaceAll(key, "-", " "))
	}
}

func slugForLink(link *Link, offer *Offer) string {
	base := link.Campaign
	if base == "" && offer != nil {
		base = offer.MerchantName
	}
	if base == "" {
		u, _ := url.Parse(link.DestinationURL)
		base = u.Hostname()
	}
	base = strings.ToLower(base)
	var b strings.Builder
	lastDash := false
	for _, r := range base {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	s := strings.Trim(b.String(), "-")
	if s == "" {
		s = fmt.Sprintf("link-%d", link.ID)
	}
	return s
}

func configValue(name string) string {
	if globalCtx == nil {
		return ""
	}
	cfg := globalCtx.Config()
	if cfg == nil {
		return ""
	}
	return strings.TrimSpace(cfg.Get(name))
}

var globalCtx *sdk.AppCtx

type wrapApp struct{ app *App }

func (w *wrapApp) Manifest() sdk.Manifest            { return w.app.Manifest() }
func (w *wrapApp) OnMount(ctx *sdk.AppCtx) error     { globalCtx = ctx; return w.app.OnMount(ctx) }
func (w *wrapApp) OnUnmount(c *sdk.AppCtx) error     { return w.app.OnUnmount(c) }
func (w *wrapApp) HTTPRoutes() []sdk.Route           { return w.app.HTTPRoutes() }
func (w *wrapApp) MCPTools() []sdk.Tool              { return w.app.MCPTools() }
func (w *wrapApp) Channels() []sdk.ChannelFactory    { return w.app.Channels() }
func (w *wrapApp) Workers() []sdk.Worker             { return w.app.Workers() }
func (w *wrapApp) EventHandlers() []sdk.EventHandler { return w.app.EventHandlers() }

func main() { sdk.Run(&wrapApp{app: &App{}}) }
