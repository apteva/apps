// Apteva Affiliate app — publisher-side affiliate manager.
//
// The app owns a small normalized model: networks, offers, products, links, and
// daily stats. Provider-specific connectors stay as integrations; this
// app executes their tools through bound integration roles.
package main

import (
	"context"
	"crypto/rand"
	"database/sql"
	_ "embed"
	"encoding/csv"
	"encoding/hex"
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
version: 0.2.0
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
    - name: affiliate_products
      description: Search normalized physical products.
    - name: affiliate_product_create
      description: Create or update a manual affiliate product.
    - name: affiliate_product_get
      description: Get one affiliate product by ID.
    - name: affiliate_product_update
      description: Update a manual affiliate product.
    - name: affiliate_product_archive
      description: Archive a manual affiliate product.
    - name: affiliate_link_create
      description: Create a monetized link.
    - name: affiliate_links
      description: Search managed links.
    - name: affiliate_stats
      description: Read normalized affiliate stats. Top-level clicks is the total; stats contains the grouped breakdown. Date ranges accept YYYY-MM-DD or RFC3339 values.
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
  - name: awin_region
    type: text
    default: ""
    label: Awin advertiser region
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
	automaticStatsRefreshSchedule    = "@every 6h"
	automaticOffersRefreshSchedule   = "@every 24h"
	automaticProductsRefreshSchedule = "@every 24h"
	automaticStatsWindowDays         = 7
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
		{Pattern: "/products", Handler: a.handleProducts},
		{Pattern: "/products/", Handler: a.handleProduct},
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
			Description: "Refresh offers, products, and/or stats from a bound provider integration. " +
				"Args: network (req), kind? (offers|products|stats|all, default all), from?, to?, plus provider-specific search and account fields.",
			InputSchema: schemaObject(map[string]any{
				"network":       map[string]any{"type": "string"},
				"kind":          map[string]any{"type": "string", "enum": []string{"offers", "products", "stats", "all"}},
				"from":          map[string]any{"type": "string"},
				"to":            map[string]any{"type": "string"},
				"publisherId":   map[string]any{"type": "string"},
				"requestor-cid": map[string]any{"type": "string"},
				"website-id":    map[string]any{"type": "string"},
				"partnerTag":    map[string]any{"type": "string"},
				"campaignId":    map[string]any{"type": "string"},
				"keywords":      map[string]any{"type": "string"},
				"q":             map[string]any{"type": "string"},
				"feedId":        map[string]any{"type": "integer"},
				"feedIds":       map[string]any{"type": "string"},
				"language":      map[string]any{"type": "string"},
				"category_ids":  map[string]any{"type": "string"},
				"gtin":          map[string]any{"type": "string"},
				"epid":          map[string]any{"type": "string"},
				"mid":           map[string]any{"type": "integer"},
				"cat":           map[string]any{"type": "string"},
				"brand":         map[string]any{"type": "string"},
				"browseNodeId":  map[string]any{"type": "string"},
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
			Name:        "affiliate_products",
			Description: "Search normalized physical products with total-count pagination. Archived products are hidden by default. Args: q?, network?, merchant?, category?, availability?, source?, status? (active|archived|all), limit?, offset?.",
			InputSchema: schemaObject(map[string]any{
				"q":            map[string]any{"type": "string"},
				"network":      map[string]any{"type": "string"},
				"merchant":     map[string]any{"type": "string"},
				"category":     map[string]any{"type": "string"},
				"availability": map[string]any{"type": "string"},
				"source":       map[string]any{"type": "string", "enum": []string{"provider", "manual"}},
				"status":       map[string]any{"type": "string", "enum": []string{"active", "archived", "all"}},
				"limit":        map[string]any{"type": "integer"},
				"offset":       map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolProducts,
		},
		{
			Name:        "affiliate_product_create",
			Description: "Create or update a manual affiliate product. Amazon products can be created from an ASIN or Amazon URL and partner tag without Creators API access. Other networks require destination_url and affiliate_url.",
			InputSchema: manualProductSchema([]string{"network", "name"}),
			Handler:     a.toolProductCreate,
		},
		{
			Name:        "affiliate_product_get",
			Description: "Get one affiliate product by ID, including archived products.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolProductGet,
		},
		{
			Name:        "affiliate_product_update",
			Description: "Update an existing manual affiliate product. Provider-synced products are read-only.",
			InputSchema: manualProductSchema([]string{"id"}),
			Handler:     a.toolProductUpdate,
		},
		{
			Name:        "affiliate_product_archive",
			Description: "Archive a manual affiliate product. Existing managed links and stats remain intact.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolProductArchive,
		},
		{
			Name: "affiliate_link_create",
			Description: "Create a monetized link from product_id, or from a URL plus an offer/network. If affiliate_url is absent, uses the product catalog URL or calls the configured provider integration. " +
				"If shorten=true, calls redirects.redirect_add with short_hostname/default_short_hostname and short_path. " +
				"Args: product_id?, url?, network?, offer_id?, affiliate_url?, campaign?, subid?, shorten?, short_hostname?, short_path?, plus provider-specific IDs.",
			InputSchema: schemaObject(map[string]any{
				"url":            map[string]any{"type": "string"},
				"network":        map[string]any{"type": "string"},
				"offer_id":       map[string]any{"type": "integer"},
				"product_id":     map[string]any{"type": "integer"},
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
			}, nil),
			Handler: a.toolLinkCreate,
		},
		{
			Name:        "affiliate_links",
			Description: "Search managed links with total-count pagination. Args: q?, network?, offer_id?, status?, limit? (default 50), offset?.",
			InputSchema: schemaObject(map[string]any{
				"q":          map[string]any{"type": "string"},
				"network":    map[string]any{"type": "string"},
				"offer_id":   map[string]any{"type": "integer"},
				"product_id": map[string]any{"type": "integer"},
				"status":     map[string]any{"type": "string"},
				"limit":      map[string]any{"type": "integer"},
				"offset":     map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolLinks,
		},
		{
			Name: "affiliate_stats",
			Description: "Read normalized affiliate stats. Top-level clicks is the total; stats contains the grouped breakdown. Args: from?, to? (YYYY-MM-DD or RFC3339), network?, offer_id?, product_id?, link_id?, " +
				"group_by? (network|offer|product|link|day, default day).",
			InputSchema: schemaObject(map[string]any{
				"from":       map[string]any{"type": "string"},
				"to":         map[string]any{"type": "string"},
				"network":    map[string]any{"type": "string"},
				"offer_id":   map[string]any{"type": "integer"},
				"product_id": map[string]any{"type": "integer"},
				"link_id":    map[string]any{"type": "integer"},
				"group_by":   map[string]any{"type": "string", "enum": []string{"network", "offer", "product", "link", "day"}},
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
		{
			Name:     "affiliate-products-refresh",
			Schedule: automaticProductsRefreshSchedule,
			Run: func(ctx context.Context, app *sdk.AppCtx) error {
				return a.runAutomaticRefresh(ctx, app, "products", "", "")
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
		if (kind == "stats" && !capability.Stats) || (kind == "offers" && !capability.Offers) || (kind == "products" && !capability.Products) {
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
			"products", summary.ProductsUpserted,
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
	ID               int64  `json:"id"`
	Key              string `json:"key"`
	Name             string `json:"name"`
	Enabled          bool   `json:"enabled"`
	Bound            bool   `json:"bound"`
	SupportsOffers   bool   `json:"supports_offers"`
	SupportsProducts bool   `json:"supports_products"`
	SupportsStats    bool   `json:"supports_stats"`
	SupportsLinks    bool   `json:"supports_links"`
	SupportsClicks   bool   `json:"supports_clicks"`
	StatsNeedsDates  bool   `json:"stats_needs_dates"`
	StatsMode        string `json:"stats_mode,omitempty"`
	LinkMode         string `json:"link_mode,omitempty"`
	ConnectionRef    string `json:"connection_ref,omitempty"`
	LastRefreshedAt  string `json:"last_refreshed_at,omitempty"`
	MetadataJSON     string `json:"metadata_json,omitempty"`
	CreatedAt        string `json:"created_at,omitempty"`
	UpdatedAt        string `json:"updated_at,omitempty"`
}

type providerCapability struct {
	Key             string
	Offers          bool
	Products        bool
	Stats           bool
	Links           bool
	Clicks          bool
	StatsNeedsDates bool
	StatsMode       string
	LinkMode        string
}

var providerCapabilities = []providerCapability{
	{Key: "target-circle", Offers: true, Stats: true, Links: true, StatsMode: "range", LinkMode: "provider"},
	{Key: "impact", Offers: true, Stats: true, Links: true, StatsNeedsDates: true, StatsMode: "range", LinkMode: "provider"},
	{Key: "awin", Offers: true, Products: true, Stats: true, Links: true, Clicks: true, StatsNeedsDates: true, StatsMode: "range", LinkMode: "provider"},
	{Key: "cj-affiliate", Offers: true, Products: true, Stats: true, Links: true, StatsMode: "range", LinkMode: "manual"},
	{Key: "amazon-associates", Products: true, Links: true, LinkMode: "provider"},
	{Key: "ebay-partner-network", Products: true, Links: true, LinkMode: "catalog"},
	{Key: "rakuten-advertising", Offers: true, Products: true, Stats: true, Links: true, StatsNeedsDates: true, StatsMode: "range", LinkMode: "provider"},
	{Key: "skimlinks", Links: true, LinkMode: "provider"},
	{Key: "sovrn", Offers: true, Stats: true, Links: true, Clicks: true, StatsNeedsDates: true, StatsMode: "range", LinkMode: "provider"},
	{Key: "partnerstack", Offers: true, Stats: true, Links: true, StatsNeedsDates: true, StatsMode: "range", LinkMode: "manual"},
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
	network.SupportsProducts = capability.Products
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

type Product struct {
	ID              int64  `json:"id"`
	NetworkKey      string `json:"network_key"`
	ExternalID      string `json:"external_id"`
	Source          string `json:"source"`
	Status          string `json:"status"`
	OfferID         int64  `json:"offer_id,omitempty"`
	MerchantID      string `json:"merchant_id,omitempty"`
	MerchantName    string `json:"merchant_name,omitempty"`
	Name            string `json:"name"`
	Description     string `json:"description,omitempty"`
	Category        string `json:"category,omitempty"`
	Brand           string `json:"brand,omitempty"`
	SKU             string `json:"sku,omitempty"`
	GTIN            string `json:"gtin,omitempty"`
	Currency        string `json:"currency,omitempty"`
	PriceCents      int64  `json:"price_cents"`
	SalePriceCents  int64  `json:"sale_price_cents"`
	ImageURL        string `json:"image_url,omitempty"`
	DestinationURL  string `json:"destination_url,omitempty"`
	AffiliateURL    string `json:"affiliate_url,omitempty"`
	Availability    string `json:"availability,omitempty"`
	RawJSON         string `json:"raw_json,omitempty"`
	LastRefreshedAt string `json:"last_refreshed_at,omitempty"`
	CreatedAt       string `json:"created_at,omitempty"`
	UpdatedAt       string `json:"updated_at,omitempty"`
}

type Link struct {
	ID             int64  `json:"id"`
	NetworkKey     string `json:"network_key"`
	OfferID        int64  `json:"offer_id,omitempty"`
	ProductID      int64  `json:"product_id,omitempty"`
	MerchantName   string `json:"merchant_name,omitempty"`
	OfferName      string `json:"offer_name,omitempty"`
	ProductName    string `json:"product_name,omitempty"`
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
	ProductID       int64  `json:"product_id,omitempty"`
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
	ProductID       int64  `json:"product_id,omitempty"`
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
	ProductsUpserted  int    `json:"products_upserted"`
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

type ProductInput struct {
	NetworkKey     string
	ExternalID     string
	Source         string
	Status         string
	OfferID        int64
	MerchantID     string
	MerchantName   string
	Name           string
	Description    string
	Category       string
	Brand          string
	SKU            string
	GTIN           string
	Currency       string
	PriceCents     int64
	SalePriceCents int64
	ImageURL       string
	DestinationURL string
	AffiliateURL   string
	Availability   string
	RawJSON        string
}

func dbUpsertProduct(db *sql.DB, in ProductInput) (*Product, error) {
	if in.NetworkKey == "" {
		return nil, errors.New("network required")
	}
	if in.ExternalID == "" {
		return nil, errors.New("external_id required")
	}
	if in.Name == "" {
		return nil, errors.New("product name required")
	}
	if in.Source == "" {
		in.Source = "provider"
	}
	if in.Source != "provider" && in.Source != "manual" {
		return nil, fmt.Errorf("invalid product source %q", in.Source)
	}
	if in.Status == "" {
		in.Status = "active"
	}
	if in.Status != "active" && in.Status != "archived" {
		return nil, fmt.Errorf("invalid product status %q", in.Status)
	}
	if in.RawJSON == "" {
		in.RawJSON = "{}"
	}
	now := nowUTC()
	_, err := db.Exec(`
		INSERT INTO products (
			network_key, external_id, source, status, offer_id, merchant_id, merchant_name, name,
			description, category, brand, sku, gtin, currency, price_cents,
			sale_price_cents, image_url, destination_url, affiliate_url,
			availability, raw_json, last_refreshed_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT(network_key, external_id) DO UPDATE SET
			source = CASE WHEN excluded.source = 'provider' THEN 'provider' ELSE products.source END,
			status = excluded.status,
			offer_id = COALESCE(excluded.offer_id, products.offer_id),
			merchant_id = excluded.merchant_id,
			merchant_name = excluded.merchant_name,
			name = excluded.name,
			description = excluded.description,
			category = excluded.category,
			brand = excluded.brand,
			sku = excluded.sku,
			gtin = excluded.gtin,
			currency = excluded.currency,
			price_cents = excluded.price_cents,
			sale_price_cents = excluded.sale_price_cents,
			image_url = excluded.image_url,
			destination_url = excluded.destination_url,
			affiliate_url = excluded.affiliate_url,
			availability = excluded.availability,
			raw_json = excluded.raw_json,
			last_refreshed_at = excluded.last_refreshed_at,
			updated_at = excluded.updated_at`,
		in.NetworkKey, in.ExternalID, in.Source, in.Status, nullInt(in.OfferID), in.MerchantID, in.MerchantName, in.Name,
		in.Description, in.Category, in.Brand, in.SKU, in.GTIN, in.Currency, in.PriceCents,
		in.SalePriceCents, in.ImageURL, in.DestinationURL, in.AffiliateURL,
		in.Availability, in.RawJSON, now, now, now)
	if err != nil {
		return nil, err
	}
	return dbGetProductByExternal(db, in.NetworkKey, in.ExternalID)
}

const productCols = `id, network_key, external_id, source, status, COALESCE(offer_id,0), merchant_id,
	merchant_name, name, description, category, brand, sku, gtin, currency,
	price_cents, sale_price_cents, image_url, destination_url, affiliate_url,
	availability, raw_json, last_refreshed_at, created_at, updated_at`

func dbGetProduct(db *sql.DB, id int64) (*Product, error) {
	product, err := scanProduct(db.QueryRow(`SELECT `+productCols+` FROM products WHERE id = ?`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return product, err
}

func dbGetProductByExternal(db *sql.DB, network, externalID string) (*Product, error) {
	product, err := scanProduct(db.QueryRow(`SELECT `+productCols+` FROM products WHERE network_key = ? AND external_id = ?`, network, externalID))
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return product, err
}

func dbListProducts(db *sql.DB, q, network, merchant, category, availability, source, status string, limit, offset int) ([]Product, int, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	if offset < 0 {
		offset = 0
	}
	where := []string{"1=1"}
	args := []any{}
	for _, filter := range []struct{ column, value string }{
		{"network_key", network}, {"merchant_name", merchant}, {"category", category},
		{"availability", availability}, {"source", source},
	} {
		if filter.value != "" {
			where = append(where, filter.column+" = ?")
			args = append(args, filter.value)
		}
	}
	if status == "" {
		status = "active"
	}
	if status != "all" {
		where = append(where, "status = ?")
		args = append(args, status)
	}
	if q != "" {
		like := "%" + q + "%"
		where = append(where, "(name LIKE ? OR merchant_name LIKE ? OR description LIKE ? OR category LIKE ? OR brand LIKE ? OR sku LIKE ? OR gtin LIKE ?)")
		args = append(args, like, like, like, like, like, like, like)
	}
	var total int
	if err := db.QueryRow(`SELECT COUNT(*) FROM products WHERE `+strings.Join(where, " AND "), args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, limit, offset)
	rows, err := db.Query(`SELECT `+productCols+` FROM products WHERE `+strings.Join(where, " AND ")+` ORDER BY updated_at DESC, id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	products := []Product{}
	for rows.Next() {
		product, err := scanProduct(rows)
		if err != nil {
			return nil, 0, err
		}
		products = append(products, *product)
	}
	return products, total, rows.Err()
}

func scanProduct(s rowScanner) (*Product, error) {
	var p Product
	if err := s.Scan(&p.ID, &p.NetworkKey, &p.ExternalID, &p.Source, &p.Status, &p.OfferID, &p.MerchantID,
		&p.MerchantName, &p.Name, &p.Description, &p.Category, &p.Brand, &p.SKU,
		&p.GTIN, &p.Currency, &p.PriceCents, &p.SalePriceCents, &p.ImageURL,
		&p.DestinationURL, &p.AffiliateURL, &p.Availability, &p.RawJSON,
		&p.LastRefreshedAt, &p.CreatedAt, &p.UpdatedAt); err != nil {
		return nil, err
	}
	return &p, nil
}

func dbUpdateManualProduct(db *sql.DB, id int64, in ProductInput) (*Product, error) {
	current, err := dbGetProduct(db, id)
	if err != nil {
		return nil, err
	}
	if current == nil {
		return nil, fmt.Errorf("product_id %d not found", id)
	}
	if current.Source != "manual" {
		return nil, errors.New("provider-synced products are read-only")
	}
	if in.Name == "" {
		return nil, errors.New("product name required")
	}
	_, err = db.Exec(`UPDATE products SET
		offer_id = ?, merchant_id = ?, merchant_name = ?, name = ?, description = ?,
		category = ?, brand = ?, sku = ?, gtin = ?, currency = ?, price_cents = ?,
		sale_price_cents = ?, image_url = ?, destination_url = ?, affiliate_url = ?,
		availability = ?, raw_json = ?, updated_at = ?
		WHERE id = ? AND source = 'manual'`,
		nullInt(in.OfferID), in.MerchantID, in.MerchantName, in.Name, in.Description,
		in.Category, in.Brand, in.SKU, in.GTIN, in.Currency, in.PriceCents,
		in.SalePriceCents, in.ImageURL, in.DestinationURL, in.AffiliateURL,
		in.Availability, in.RawJSON, nowUTC(), id)
	if err != nil {
		return nil, err
	}
	return dbGetProduct(db, id)
}

func dbArchiveManualProduct(db *sql.DB, id int64) (*Product, error) {
	current, err := dbGetProduct(db, id)
	if err != nil {
		return nil, err
	}
	if current == nil {
		return nil, fmt.Errorf("product_id %d not found", id)
	}
	if current.Source != "manual" {
		return nil, errors.New("provider-synced products are read-only")
	}
	if _, err := db.Exec(`UPDATE products SET status = 'archived', updated_at = ? WHERE id = ?`, nowUTC(), id); err != nil {
		return nil, err
	}
	return dbGetProduct(db, id)
}

type LinkInput struct {
	NetworkKey     string
	OfferID        int64
	ProductID      int64
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
			network_key, offer_id, product_id, destination_url, affiliate_url, short_url,
			redirect_rule_id, campaign, subid, status, raw_json, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
		in.NetworkKey, nullInt(in.OfferID), nullInt(in.ProductID), in.DestinationURL, in.AffiliateURL, in.ShortURL,
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
		WHERE network_key = ? AND COALESCE(offer_id, 0) = ? AND COALESCE(product_id, 0) = ? AND campaign = '' AND subid = ''
		ORDER BY id DESC LIMIT 1`, in.NetworkKey, in.OfferID, in.ProductID).Scan(&id)
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

const linkCols = `id, network_key, COALESCE(offer_id,0), COALESCE(product_id,0), destination_url, affiliate_url,
	short_url, redirect_rule_id, campaign, subid, status, raw_json, last_checked_at,
	created_at, updated_at`

const linkColsQualified = `l.id, l.network_key, COALESCE(l.offer_id,0), COALESCE(l.product_id,0), l.destination_url, l.affiliate_url,
	l.short_url, l.redirect_rule_id, l.campaign, l.subid, l.status, l.raw_json, l.last_checked_at,
	l.created_at, l.updated_at`

func dbListLinks(db *sql.DB, q, network string, offerID, productID int64, status string, limit, offset int) ([]Link, int, error) {
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
	if productID != 0 {
		where = append(where, "l.product_id = ?")
		args = append(args, productID)
	}
	if status != "" {
		where = append(where, "l.status = ?")
		args = append(args, status)
	}
	if q != "" {
		like := "%" + q + "%"
		where = append(where, `(l.destination_url LIKE ? OR l.affiliate_url LIKE ? OR l.short_url LIKE ?
			OR l.campaign LIKE ? OR l.subid LIKE ? OR o.merchant_name LIKE ? OR o.offer_name LIKE ? OR p.merchant_name LIKE ? OR p.name LIKE ?)`)
		args = append(args, like, like, like, like, like, like, like, like, like)
	}
	if offset < 0 {
		offset = 0
	}
	var total int
	if err := db.QueryRow(`SELECT COUNT(*) FROM links l LEFT JOIN offers o ON o.id = l.offer_id LEFT JOIN products p ON p.id = l.product_id WHERE `+strings.Join(where, " AND "), args...).Scan(&total); err != nil {
		return nil, 0, err
	}
	args = append(args, limit, offset)
	rows, err := db.Query(`SELECT `+linkColsQualified+`, COALESCE(NULLIF(p.merchant_name,''),o.merchant_name,''), COALESCE(o.offer_name,''), COALESCE(p.name,'')
		FROM links l
		LEFT JOIN offers o ON o.id = l.offer_id
		LEFT JOIN products p ON p.id = l.product_id
		WHERE `+strings.Join(where, " AND ")+`
		ORDER BY l.id DESC LIMIT ? OFFSET ?`, args...)
	if err != nil {
		return nil, 0, err
	}
	defer rows.Close()
	var out []Link
	for rows.Next() {
		var l Link
		if err := rows.Scan(&l.ID, &l.NetworkKey, &l.OfferID, &l.ProductID, &l.DestinationURL, &l.AffiliateURL,
			&l.ShortURL, &l.RedirectRuleID, &l.Campaign, &l.SubID, &l.Status, &l.RawJSON,
			&l.LastCheckedAt, &l.CreatedAt, &l.UpdatedAt, &l.MerchantName, &l.OfferName, &l.ProductName); err != nil {
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
	if err := s.Scan(&l.ID, &l.NetworkKey, &l.OfferID, &l.ProductID, &l.DestinationURL, &l.AffiliateURL,
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

func dbStats(db *sql.DB, from, to, network string, offerID, productID, linkID int64, groupBy string) ([]StatRow, error) {
	if groupBy == "" {
		groupBy = "day"
	}
	selectCols := "s.date, s.network_key, 0 AS offer_id, 0 AS product_id, 0 AS link_id"
	groupCols := "s.date, s.network_key"
	switch groupBy {
	case "network":
		selectCols = "'' AS date, s.network_key, 0 AS offer_id, 0 AS product_id, 0 AS link_id"
		groupCols = "s.network_key"
	case "offer":
		selectCols = "'' AS date, s.network_key, COALESCE(s.offer_id,0) AS offer_id, 0 AS product_id, 0 AS link_id"
		groupCols = "s.network_key, s.offer_id"
	case "product":
		selectCols = "'' AS date, s.network_key, 0 AS offer_id, COALESCE(l.product_id,0) AS product_id, 0 AS link_id"
		groupCols = "s.network_key, l.product_id"
	case "link":
		selectCols = "'' AS date, s.network_key, 0 AS offer_id, 0 AS product_id, COALESCE(s.link_id,0) AS link_id"
		groupCols = "s.network_key, s.link_id"
	case "day":
	default:
		return nil, fmt.Errorf("invalid group_by %q", groupBy)
	}
	where := []string{"1=1"}
	args := []any{}
	if from != "" {
		where = append(where, "s.date >= ?")
		args = append(args, from)
	}
	if to != "" {
		where = append(where, "s.date <= ?")
		args = append(args, to)
	}
	if network != "" {
		where = append(where, "s.network_key = ?")
		args = append(args, network)
	}
	if offerID != 0 {
		where = append(where, "s.offer_id = ?")
		args = append(args, offerID)
	}
	if productID != 0 {
		where = append(where, "COALESCE(l.product_id,0) = ?")
		args = append(args, productID)
	}
	if linkID != 0 {
		where = append(where, "s.link_id = ?")
		args = append(args, linkID)
	}
	q := fmt.Sprintf(`SELECT %s, SUM(s.clicks), SUM(s.conversions), SUM(s.revenue_cents), SUM(s.commission_cents), s.currency
		FROM stats_daily s LEFT JOIN links l ON l.id = s.link_id
		WHERE %s GROUP BY %s, s.currency ORDER BY %s`,
		selectCols, strings.Join(where, " AND "), groupCols, groupCols)
	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StatRow
	for rows.Next() {
		var s StatRow
		if err := rows.Scan(&s.Date, &s.NetworkKey, &s.OfferID, &s.ProductID, &s.LinkID, &s.Clicks, &s.Conversions, &s.RevenueCents, &s.CommissionCents, &s.Currency); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	redirectSelect := "r.date, l.network_key, 0 AS offer_id, 0 AS product_id, 0 AS link_id"
	redirectGroup := "r.date, l.network_key"
	switch groupBy {
	case "network":
		redirectSelect = "'' AS date, l.network_key, 0 AS offer_id, 0 AS product_id, 0 AS link_id"
		redirectGroup = "l.network_key"
	case "offer":
		redirectSelect = "'' AS date, l.network_key, COALESCE(l.offer_id,0) AS offer_id, 0 AS product_id, 0 AS link_id"
		redirectGroup = "l.network_key, l.offer_id"
	case "product":
		redirectSelect = "'' AS date, l.network_key, 0 AS offer_id, COALESCE(l.product_id,0) AS product_id, 0 AS link_id"
		redirectGroup = "l.network_key, l.product_id"
	case "link":
		redirectSelect = "'' AS date, l.network_key, 0 AS offer_id, 0 AS product_id, r.link_id"
		redirectGroup = "l.network_key, r.link_id"
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
	if productID != 0 {
		redirectWhere = append(redirectWhere, "COALESCE(l.product_id,0) = ?")
		redirectArgs = append(redirectArgs, productID)
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
		if err := redirectRows.Scan(&redirect.Date, &redirect.NetworkKey, &redirect.OfferID, &redirect.ProductID, &redirect.LinkID, &redirect.RedirectClicks); err != nil {
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
		left := fmt.Sprintf("%s\x00%s\x00%020d\x00%020d\x00%020d\x00%s", out[i].Date, out[i].NetworkKey, out[i].OfferID, out[i].ProductID, out[i].LinkID, out[i].Currency)
		right := fmt.Sprintf("%s\x00%s\x00%020d\x00%020d\x00%020d\x00%s", out[j].Date, out[j].NetworkKey, out[j].OfferID, out[j].ProductID, out[j].LinkID, out[j].Currency)
		return left < right
	})
	return out, nil
}

func statRowGroupKey(row StatRow, groupBy string) string {
	switch groupBy {
	case "network":
		return row.NetworkKey
	case "offer":
		return row.NetworkKey + "\x00" + strconv.FormatInt(row.OfferID, 10)
	case "product":
		return row.NetworkKey + "\x00" + strconv.FormatInt(row.ProductID, 10)
	case "link":
		return row.NetworkKey + "\x00" + strconv.FormatInt(row.LinkID, 10)
	default:
		return row.NetworkKey + "\x00" + row.Date
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
	if kind != "offers" && kind != "products" && kind != "stats" && kind != "all" {
		return nil, fmt.Errorf("invalid kind %q", kind)
	}
	capability, ok := capabilityFor(network)
	if !ok {
		return nil, fmt.Errorf("unsupported affiliate network %q", network)
	}
	if kind == "offers" && !capability.Offers {
		return nil, fmt.Errorf("%s does not expose offer refresh", network)
	}
	if kind == "products" && !capability.Products {
		return nil, fmt.Errorf("%s does not expose product refresh", network)
	}
	if kind == "stats" && !capability.Stats {
		return nil, fmt.Errorf("%s does not expose stats refresh", network)
	}
	if kind == "all" && !capability.Offers && !capability.Products && !capability.Stats {
		return nil, fmt.Errorf("%s has no refreshable offer, product, or stats data", network)
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

func (a *App) toolProducts(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	network := strArg(args, "network")
	if network != "" {
		network = canonicalNetworkKey(network)
	}
	status := strArg(args, "status")
	if status != "" && status != "active" && status != "archived" && status != "all" {
		return nil, fmt.Errorf("invalid product status %q", status)
	}
	source := strArg(args, "source")
	if source != "" && source != "provider" && source != "manual" {
		return nil, fmt.Errorf("invalid product source %q", source)
	}
	rows, total, err := dbListProducts(ctx.AppDB(), strArg(args, "q"), network,
		strArg(args, "merchant"), strArg(args, "category"), strArg(args, "availability"),
		source, status,
		intArg(args, "limit", 50), intArg(args, "offset", 0))
	if err != nil {
		return nil, err
	}
	return map[string]any{"products": rows, "count": len(rows), "total": total}, nil
}

func (a *App) toolProductCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	in, err := normalizeManualProductInput(ctx.AppDB(), args)
	if err != nil {
		return nil, err
	}
	existing, err := dbGetProductByExternal(ctx.AppDB(), in.NetworkKey, in.ExternalID)
	if err != nil {
		return nil, err
	}
	if existing != nil && existing.Source != "manual" {
		return nil, fmt.Errorf("product %s/%s already exists from provider sync", in.NetworkKey, in.ExternalID)
	}
	product, err := dbUpsertProduct(ctx.AppDB(), in)
	if err != nil {
		return nil, err
	}
	return map[string]any{"product": product}, nil
}

func (a *App) toolProductGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := toInt64(args["id"])
	if id <= 0 {
		return nil, errors.New("id required")
	}
	product, err := dbGetProduct(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	if product == nil {
		return nil, fmt.Errorf("product_id %d not found", id)
	}
	return map[string]any{"product": product}, nil
}

func (a *App) toolProductUpdate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := toInt64(args["id"])
	if id <= 0 {
		return nil, errors.New("id required")
	}
	current, err := dbGetProduct(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	if current == nil {
		return nil, fmt.Errorf("product_id %d not found", id)
	}
	if current.Source != "manual" {
		return nil, errors.New("provider-synced products are read-only")
	}
	merged := manualProductArgs(current)
	for key, value := range args {
		if key != "id" {
			merged[key] = value
		}
	}
	in, err := normalizeManualProductInput(ctx.AppDB(), merged)
	if err != nil {
		return nil, err
	}
	if in.NetworkKey != current.NetworkKey || in.ExternalID != current.ExternalID {
		return nil, errors.New("network and external_id cannot be changed")
	}
	product, err := dbUpdateManualProduct(ctx.AppDB(), id, in)
	if err != nil {
		return nil, err
	}
	return map[string]any{"product": product}, nil
}

func (a *App) toolProductArchive(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := toInt64(args["id"])
	if id <= 0 {
		return nil, errors.New("id required")
	}
	product, err := dbArchiveManualProduct(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"product": product}, nil
}

func (a *App) toolLinkCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	destination := strArg(args, "url")
	offerID := toInt64(args["offer_id"])
	productID := toInt64(args["product_id"])
	network := strArg(args, "network")
	if network != "" {
		network = canonicalNetworkKey(network)
	}
	var product *Product
	if productID != 0 {
		var err error
		product, err = dbGetProduct(ctx.AppDB(), productID)
		if err != nil {
			return nil, err
		}
		if product == nil {
			return nil, fmt.Errorf("product_id %d not found", productID)
		}
		if product.Status == "archived" {
			return nil, fmt.Errorf("product_id %d is archived", productID)
		}
		if network == "" {
			network = product.NetworkKey
		} else if network != product.NetworkKey {
			return nil, fmt.Errorf("product_id %d belongs to %s, not %s", productID, product.NetworkKey, network)
		}
		if offerID == 0 {
			offerID = product.OfferID
		}
		if destination == "" {
			destination = product.DestinationURL
		}
	}
	if destination == "" {
		return nil, errors.New("url required when product_id has no destination URL")
	}
	if err := validateAbsoluteURL(destination); err != nil {
		return nil, err
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
	if affiliateURL == "" && product != nil {
		affiliateURL = product.AffiliateURL
	}
	if product != nil && affiliateURL == product.AffiliateURL && product.DestinationURL != "" && destination != product.DestinationURL {
		return nil, errors.New("a catalog product's stored affiliate URL can only be used with its original destination URL")
	}
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
		ProductID:      productID,
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
		shortened, err := a.createRedirect(ctx, link, args, offer, product)
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
	rows, total, err := dbListLinks(ctx.AppDB(), strArg(args, "q"), network, toInt64(args["offer_id"]), toInt64(args["product_id"]), strArg(args, "status"), intArg(args, "limit", 50), intArg(args, "offset", 0))
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
	rows, err := dbStats(ctx.AppDB(), from, to, network, toInt64(args["offer_id"]), toInt64(args["product_id"]), toInt64(args["link_id"]), strArg(args, "group_by"))
	if err != nil {
		return nil, err
	}
	unified := unifyStatRows(rows, strArg(args, "group_by"), network)
	var totalClicks int64
	for _, row := range unified {
		totalClicks += row.Clicks
	}
	return map[string]any{"clicks": totalClicks, "stats": unified}, nil
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
	rows, err := dbStats(ctx.AppDB(), from, to, network, toInt64(args["offer_id"]), toInt64(args["product_id"]), toInt64(args["link_id"]), strArg(args, "group_by"))
	if err != nil {
		return nil, err
	}
	unified := unifyStatRows(rows, strArg(args, "group_by"), network)
	var totalClicks int64
	for _, row := range unified {
		totalClicks += row.Clicks
	}
	providerClicksAvailable := boundProviderClicksAvailable(ctx, network)
	redirectClicksAvailable, err := dbRedirectTrackingAvailable(ctx.AppDB(), network)
	if err != nil {
		return nil, err
	}
	return map[string]any{
		"stats":                     unified,
		"count":                     len(unified),
		"clicks":                    totalClicks,
		"clicks_available":          providerClicksAvailable || redirectClicksAvailable,
		"provider_clicks_available": providerClicksAvailable,
		"redirect_clicks_available": redirectClicksAvailable,
	}, nil
}

func boundProviderClicksAvailable(ctx *sdk.AppCtx, selectedNetwork string) bool {
	for _, capability := range providerCapabilities {
		if !capability.Clicks || selectedNetwork != "" && capability.Key != selectedNetwork {
			continue
		}
		binding := ctx.IntegrationFor(capability.Key)
		if binding != nil && binding.ConnectionID != 0 {
			return true
		}
	}
	return false
}

func effectiveClicks(row StatRow, selectedNetwork string) int64 {
	rowNetwork := canonicalNetworkKey(firstNonEmpty(row.NetworkKey, selectedNetwork))
	if capability, ok := capabilityFor(rowNetwork); ok && capability.Clicks {
		return row.Clicks
	}
	return row.RedirectClicks
}

func unifyStatRows(rows []StatRow, groupBy, selectedNetwork string) []UnifiedStatRow {
	if groupBy == "" {
		groupBy = "day"
	}
	byKey := map[string]int{}
	out := make([]UnifiedStatRow, 0, len(rows))
	for _, row := range rows {
		unified := UnifiedStatRow{
			Date: row.Date, NetworkKey: row.NetworkKey, OfferID: row.OfferID, ProductID: row.ProductID, LinkID: row.LinkID,
			Clicks: effectiveClicks(row, selectedNetwork), Conversions: row.Conversions,
			RevenueCents: row.RevenueCents, CommissionCents: row.CommissionCents, Currency: row.Currency,
		}
		key := unified.Currency
		switch groupBy {
		case "network":
			key += "\x00" + unified.NetworkKey
		case "offer":
			key += "\x00" + strconv.FormatInt(unified.OfferID, 10)
			unified.NetworkKey = ""
		case "product":
			key += "\x00" + strconv.FormatInt(unified.ProductID, 10)
			unified.NetworkKey = ""
		case "link":
			key += "\x00" + strconv.FormatInt(unified.LinkID, 10)
			unified.NetworkKey = ""
		default:
			key += "\x00" + unified.Date
			unified.NetworkKey = ""
		}
		if index, ok := byKey[key]; ok {
			out[index].Clicks += unified.Clicks
			out[index].Conversions += unified.Conversions
			out[index].RevenueCents += unified.RevenueCents
			out[index].CommissionCents += unified.CommissionCents
			continue
		}
		byKey[key] = len(out)
		out = append(out, unified)
	}
	return out
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
			recordKeys := call.RecordKeys
			if len(recordKeys) == 0 {
				recordKeys = []string{"offers", "Campaigns", "campaigns", "programs", "partnerships", "advertisers", "body", "data", "items", "results"}
			}
			pages, err := executeProviderPages(ctx, call, recordKeys)
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
	if capability.Products && (kind == "products" || kind == "all") {
		productArgs := args
		if network == "awin" && len(commaSeparated(firstNonEmpty(strArg(args, "feedIds"), firstString(args, "feedId"), configValue("awin_product_feed_ids")))) == 0 {
			discovered, discoverErr := a.withDiscoveredAwinFeedIDs(ctx, args)
			if discoverErr != nil {
				return nil, discoverErr
			}
			productArgs = discovered
		}
		calls, err := providerProductCalls(network, productArgs)
		if err != nil {
			return nil, err
		}
		for _, call := range calls {
			recordKeys := call.RecordKeys
			if len(recordKeys) == 0 {
				recordKeys = []string{"itemSummaries", "Items", "items", "products", "product", "data", "results", "result", "body"}
			}
			pages, err := executeProviderPages(ctx, call, recordKeys)
			if err != nil {
				return nil, err
			}
			for _, page := range pages {
				records, err := productRecordsFromPage(network, page)
				if err != nil {
					return nil, fmt.Errorf("%s.%s products: %w", call.Role, call.Tool, err)
				}
				for _, record := range records {
					in := productInputFromMap(network, record)
					if in.ExternalID == "" || in.Name == "" {
						continue
					}
					if in.OfferID == 0 && in.MerchantID != "" {
						if offer, err := dbGetOfferByExternal(ctx.AppDB(), network, in.MerchantID); err == nil && offer != nil {
							in.OfferID = offer.ID
						}
					}
					_, err := dbUpsertProduct(ctx.AppDB(), in)
					if err != nil {
						return nil, err
					}
					summary.ProductsUpserted++
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
			pages, err := executeProviderPages(ctx, call, []string{"stats", "Actions", "actions", "transactions", "Transactions", "rewards", "merchants", "records", "body", "data", "items", "result", "results"})
			if err != nil {
				return nil, err
			}
			for _, page := range pages {
				for _, m := range page.Records {
					row := statInputFromMap(network, m)
					if row.Date == "" {
						continue
					}
					if ext := firstString(m, "offer_external_id", "offerSid", "offerId", "offer_id", "external_id", "offer.sid", "offer.slug", "CampaignId", "campaignId", "advertiserId", "advertiser.id", "merchantGroupId", "company.key", "company_key"); ext != "" && row.OfferID == 0 {
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

func (a *App) withDiscoveredAwinFeedIDs(ctx *sdk.AppCtx, args map[string]any) (map[string]any, error) {
	var decoded any
	if err := executeProviderCall(ctx, providerCall{Role: "awin", Tool: "product_feeds_list", Input: map[string]any{}}, &decoded); err != nil {
		return nil, fmt.Errorf("discover Awin product feeds: %w", err)
	}
	raw, ok := decoded.(string)
	if !ok || strings.TrimSpace(raw) == "" {
		return nil, errors.New("Awin product feed list returned no CSV data")
	}
	records, err := parseCSVRecords(raw)
	if err != nil {
		return nil, fmt.Errorf("parse Awin product feed list: %w", err)
	}
	feedIDs := make([]string, 0, len(records))
	for _, record := range records {
		if !strings.EqualFold(firstString(record, "Membership Status", "membership_status"), "Joined") {
			continue
		}
		if feedID := firstString(record, "Feed ID", "feed_id"); feedID != "" {
			feedIDs = append(feedIDs, feedID)
		}
	}
	if len(feedIDs) == 0 {
		return nil, errors.New("Awin returned no joined product feeds")
	}
	out := cloneMap(args)
	out["feedIds"] = strings.Join(feedIDs, ",")
	return out, nil
}

func upsertDefaultLinkFromOffer(db *sql.DB, network string, offer *Offer, raw map[string]any) (bool, error) {
	if offer == nil {
		return false, nil
	}
	affiliateURL := firstString(raw, "defaultTrackingUrl", "defaultClickTrackingUrl", "clickTrackingUrl", "clickUrl", "clickThroughUrl", "TrackingLink", "trackingLink", "referral_link", "referralLink")
	if affiliateURL == "" {
		return false, nil
	}
	destinationURL := firstString(raw, "targetUrl", "defaultTargetUrl", "destinationUrl", "AdvertiserUrl", "displayUrl", "url")
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

func (a *App) createRedirect(ctx *sdk.AppCtx, link *Link, args map[string]any, offer *Offer, product *Product) (*Link, error) {
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
		path = "/" + slugForLink(link, offer, product)
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

func (a *App) handleProducts(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		q := r.URL.Query()
		out, err := a.toolProducts(globalCtx, map[string]any{
			"q": q.Get("q"), "network": q.Get("network"), "merchant": q.Get("merchant"),
			"category": q.Get("category"), "availability": q.Get("availability"),
			"source": q.Get("source"), "status": q.Get("status"),
			"limit": q.Get("limit"), "offset": q.Get("offset"),
		})
		writeToolResponse(w, out, err)
	case http.MethodPost:
		var args map[string]any
		if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
			http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
			return
		}
		out, err := a.toolProductCreate(globalCtx, args)
		writeToolResponse(w, out, err)
	default:
		http.Error(w, "GET or POST", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleProduct(w http.ResponseWriter, r *http.Request) {
	id, err := strconv.ParseInt(strings.TrimPrefix(r.URL.Path, "/products/"), 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "valid product id required", http.StatusBadRequest)
		return
	}
	switch r.Method {
	case http.MethodGet:
		out, err := a.toolProductGet(globalCtx, map[string]any{"id": id})
		writeToolResponse(w, out, err)
	case http.MethodPatch:
		args := map[string]any{"id": id}
		if err := json.NewDecoder(r.Body).Decode(&args); err != nil {
			http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
			return
		}
		args["id"] = id
		out, err := a.toolProductUpdate(globalCtx, args)
		writeToolResponse(w, out, err)
	case http.MethodDelete:
		out, err := a.toolProductArchive(globalCtx, map[string]any{"id": id})
		writeToolResponse(w, out, err)
	default:
		http.Error(w, "GET, PATCH, or DELETE", http.StatusMethodNotAllowed)
	}
}

func (a *App) handleLinks(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		q := r.URL.Query()
		out, err := a.toolLinks(globalCtx, map[string]any{
			"q":          q.Get("q"),
			"network":    q.Get("network"),
			"offer_id":   q.Get("offer_id"),
			"product_id": q.Get("product_id"),
			"status":     q.Get("status"),
			"limit":      q.Get("limit"),
			"offset":     q.Get("offset"),
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
		"from":       q.Get("from"),
		"to":         q.Get("to"),
		"network":    q.Get("network"),
		"offer_id":   q.Get("offer_id"),
		"product_id": q.Get("product_id"),
		"link_id":    q.Get("link_id"),
		"group_by":   q.Get("group_by"),
	})
	writeToolResponse(w, out, err)
}

// --- provider routing + normalization --------------------------------------

type providerCall struct {
	Role       string
	Tool       string
	Input      map[string]any
	RecordKeys []string
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
		var decoded any
		pageCall := call
		pageCall.Input = input
		if err := executeProviderCall(ctx, pageCall, &decoded); err != nil {
			return nil, err
		}
		out, ok := decoded.(map[string]any)
		if !ok {
			out = map[string]any{"data": decoded}
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
			currentPage := pagination.Start + pageIndex
			if totalPages := toInt64(firstAny(out, "TotalPages", "totalPages", "total_pages", "metadata.total_pages", "_metadata.total_pages")); totalPages > 0 && int64(currentPage) >= totalPages {
				return pages, nil
			}
			if !hasMore && len(records) < pagination.PageSize {
				return pages, nil
			}
			input = cloneMap(input)
			input[pagination.Param] = pagination.Start + pageIndex + 1
		case "cursor":
			cursor := firstString(out, pagination.CursorPath, "next_cursor", "nextCursor", "meta.next_cursor", "pagination.next_cursor")
			if cursor == "" && hasMore && len(records) > 0 {
				cursor = firstString(records[len(records)-1], "key")
			}
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
	v := firstAny(out, "next", "links.next", "meta.next", "pagination.next", "data.has_more", "data.hasMore", "has_more", "hasMore")
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
			"limit":            boundedLimit(args, 250, 250),
		}), RecordKeys: []string{"partnerships", "body", "data", "items", "results"}, Pagination: &providerPagination{Mode: "cursor", Param: "starting_after", PageSize: boundedLimit(args, 250, 250), MaxPages: maxPageArg(args), CursorPath: "next"}}}, nil
	case "shareasale":
		return []providerCall{{Role: "shareasale", Tool: "merchants_search", Input: compactMap(map[string]any{
			"keyword":   strArg(args, "keyword"),
			"category":  strArg(args, "category"),
			"XMLFormat": 0,
		})}}, nil
	case "rakuten-advertising":
		limit := boundedLimit(args, 50, 100)
		return []providerCall{{Role: "rakuten-advertising", Tool: "partnerships_list", Input: compactMap(map[string]any{
			"partner_status": firstNonEmpty(strArg(args, "partner_status"), strArg(args, "status"), "active"),
			"network":        nullInt(toInt64(firstAny(args, "network_id"))), "category": strArg(args, "category"),
			"limit": limit, "page": 1,
		}), RecordKeys: []string{"partnerships", "advertisers", "data", "items", "results"}, Pagination: &providerPagination{Mode: "page", Param: "page", PageSize: limit, MaxPages: maxPageArg(args), Start: 1}}}, nil
	case "skimlinks":
		return nil, errors.New("skimlinks merchant/program refresh is not exposed by the current integration; use skimlinks for link_wrapper_url link creation")
	default:
		return nil, fmt.Errorf("unsupported affiliate network %q", network)
	}
}

func providerProductCalls(network string, args map[string]any) ([]providerCall, error) {
	network = canonicalNetworkKey(network)
	switch network {
	case "awin":
		feedIDs := commaSeparated(firstNonEmpty(strArg(args, "feedIds"), firstString(args, "feedId"), configValue("awin_product_feed_ids")))
		if len(feedIDs) == 0 {
			return nil, errors.New("awin product refresh requires feedId/feedIds or awin_product_feed_ids; use awin.product_feeds_list to discover feed IDs")
		}
		calls := make([]providerCall, 0, len(feedIDs))
		for _, feedID := range feedIDs {
			id, err := strconv.ParseInt(feedID, 10, 64)
			if err != nil || id <= 0 {
				return nil, fmt.Errorf("invalid Awin feed ID %q", feedID)
			}
			calls = append(calls, providerCall{Role: "awin", Tool: "product_feed_download", Input: map[string]any{
				"feedId": id, "language": argOrConfig(args, "language", "awin_feed_language", "en"),
				"adultContent": intArg(args, "adultContent", 0),
			}})
		}
		return calls, nil
	case "cj-affiliate":
		websiteID := requiredArgOrConfig(args, "website-id", "cj_website_id")
		if websiteID == "" {
			return nil, errors.New("cj-affiliate product refresh requires website-id or cj_website_id")
		}
		limit := boundedLimit(args, 100, 100)
		return []providerCall{{Role: "cj-affiliate", Tool: "products_search", Input: compactMap(map[string]any{
			"website-id": websiteID, "advertiser-ids": argOrConfig(args, "advertiser-ids", "cj_advertiser_ids", "joined"),
			"keywords": strArg(args, "keywords"), "serviceable-area": strArg(args, "serviceable-area"),
			"low-price": firstAny(args, "low-price"), "high-price": firstAny(args, "high-price"),
			"currency": strArg(args, "currency"), "records-per-page": limit, "page-number": 1,
		}), RecordKeys: []string{"products", "product", "body", "data", "items", "results"}, Pagination: &providerPagination{Mode: "page", Param: "page-number", PageSize: limit, MaxPages: maxPageArg(args), Start: 1}}}, nil
	case "amazon-associates":
		partnerTag := requiredArgOrConfig(args, "partnerTag", "amazon_partner_tag")
		keywords := requiredArgOrConfig(args, "keywords", "amazon_keywords")
		if partnerTag == "" || keywords == "" {
			return nil, errors.New("amazon-associates product refresh requires partnerTag and keywords; pass them or set amazon_partner_tag and amazon_keywords")
		}
		limit := boundedLimit(args, 10, 10)
		return []providerCall{{Role: "amazon-associates", Tool: "items_search", Input: compactMap(map[string]any{
			"partnerTag": partnerTag, "marketplace": argOrConfig(args, "marketplace", "amazon_marketplace", "www.amazon.com"),
			"keywords": keywords, "searchIndex": argOrConfig(args, "searchIndex", "amazon_search_index", "All"),
			"brand": strArg(args, "brand"), "browseNodeId": strArg(args, "browseNodeId"),
			"itemCount": limit, "itemPage": 1, "resources": defaultAmazonResources(),
		}), RecordKeys: []string{"SearchResult", "Items", "items"}, Pagination: &providerPagination{Mode: "page", Param: "itemPage", PageSize: limit, MaxPages: maxPageArg(args), Start: 1}}}, nil
	case "ebay-partner-network":
		keywords := firstNonEmpty(strArg(args, "q"), strArg(args, "keywords"), configValue("ebay_keywords"))
		categoryID := strArg(args, "category_ids")
		if keywords == "" && categoryID == "" && strArg(args, "gtin") == "" && strArg(args, "epid") == "" {
			return nil, errors.New("ebay product refresh requires q/keywords, category_ids, gtin, or epid")
		}
		limit := boundedLimit(args, 50, 200)
		return []providerCall{{Role: "ebay-partner-network", Tool: "products_search", Input: compactMap(map[string]any{
			"q": keywords, "category_ids": categoryID, "gtin": strArg(args, "gtin"), "epid": strArg(args, "epid"),
			"aspect_filter": strArg(args, "aspect_filter"), "limit": limit, "offset": 0, "fieldgroups": "MATCHING_ITEMS",
		}), RecordKeys: []string{"itemSummaries"}, Pagination: &providerPagination{Mode: "offset", Param: "offset", PageSize: limit, MaxPages: maxPageArg(args), Start: 0}}}, nil
	case "rakuten-advertising":
		keyword := firstNonEmpty(strArg(args, "keyword"), strArg(args, "keywords"), configValue("rakuten_keywords"))
		mid := toInt64(firstAny(args, "mid", "advertiser_id"))
		category := strArg(args, "cat")
		if keyword == "" && mid == 0 && category == "" {
			return nil, errors.New("rakuten product refresh requires keyword/keywords, mid/advertiser_id, or cat")
		}
		limit := boundedLimit(args, 50, 100)
		return []providerCall{{Role: "rakuten-advertising", Tool: "products_search", Input: compactMap(map[string]any{
			"keyword": keyword, "mid": nullInt(mid), "cat": category,
			"language": firstNonEmpty(strArg(args, "language"), "en_US"), "max": limit, "pagenumber": 1,
		}), RecordKeys: []string{"item", "items", "result"}, Pagination: &providerPagination{Mode: "page", Param: "pagenumber", PageSize: limit, MaxPages: maxPageArg(args), Start: 1}}}, nil
	default:
		return nil, fmt.Errorf("%s does not expose product refresh", network)
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
		region := requiredArgOrConfig(args, "region", "awin_region")
		if publisherID == "" || region == "" || from == "" || to == "" {
			return nil, errors.New("awin stats require publisherId, region, from, and to; set awin_publisher_id and awin_region or pass both")
		}
		return []providerCall{{Role: "awin", Tool: "campaign_performance_report", Input: compactMap(map[string]any{
			"publisherId":                   publisherID,
			"startDate":                     from,
			"endDate":                       to,
			"region":                        strings.ToUpper(region),
			"advertiserIds":                 strArg(args, "advertiserIds"),
			"campaign":                      strArg(args, "campaign"),
			"includeNumbersWithoutCampaign": true,
			"interval":                      "day",
			"timezone":                      firstNonEmpty(strArg(args, "timezone"), "UTC"),
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
		if from == "" || to == "" {
			return nil, errors.New("sovrn stats require from and to")
		}
		if days, err := inclusiveDates(from, to); err != nil {
			return nil, err
		} else if len(days) > 31 {
			return nil, errors.New("sovrn stats support a maximum 31-day range per refresh")
		}
		return []providerCall{{Role: "sovrn", Tool: "merchants_by_date_report", Input: compactMap(map[string]any{
			"clickDateStart":   from,
			"clickDateEnd":     exclusiveEndDate(to),
			"campaignIds":      strArg(args, "campaignIds"),
			"subIds":           strArg(args, "subIds"),
			"merchantGroupIds": strArg(args, "merchantGroupIds"),
			"cuids":            strArg(args, "cuids"),
			"pageUtmSource":    strArg(args, "pageUtmSource"),
			"pageUtmMedium":    strArg(args, "pageUtmMedium"),
			"pageUtmCampaign":  strArg(args, "pageUtmCampaign"),
			"pageUtmTerm":      strArg(args, "pageUtmTerm"),
			"pageUtmContent":   strArg(args, "pageUtmContent"),
			"linkUtmSource":    strArg(args, "linkUtmSource"),
			"linkUtmMedium":    strArg(args, "linkUtmMedium"),
			"linkUtmCampaign":  strArg(args, "linkUtmCampaign"),
			"linkUtmTerm":      strArg(args, "linkUtmTerm"),
			"linkUtmContent":   strArg(args, "linkUtmContent"),
			"programType":      strArg(args, "programType"),
			"sovrnProduct":     strArg(args, "sovrnProduct"),
			"deviceType":       strArg(args, "deviceType"),
			"country":          strings.ToUpper(strArg(args, "country")),
		})}}, nil
	case "partnerstack":
		minCreated := toInt64(firstAny(args, "min_created"))
		maxCreated := toInt64(firstAny(args, "max_created"))
		if minCreated == 0 {
			minCreated = unixDateBoundary(from, false)
		}
		if maxCreated == 0 {
			maxCreated = unixDateBoundary(to, true)
		}
		limit := boundedLimit(args, 250, 250)
		common := compactMap(map[string]any{
			"min_created": minCreated,
			"max_created": maxCreated,
			"limit":       limit,
		})
		return []providerCall{
			{Role: "partnerstack", Tool: "transactions_list", Input: common, Pagination: &providerPagination{Mode: "cursor", Param: "starting_after", PageSize: limit, MaxPages: maxPageArg(args), CursorPath: "next"}},
			{Role: "partnerstack", Tool: "rewards_list", Input: cloneMap(common), Pagination: &providerPagination{Mode: "cursor", Param: "starting_after", PageSize: limit, MaxPages: maxPageArg(args), CursorPath: "next"}},
		}, nil
	case "shareasale":
		return []providerCall{{Role: "shareasale", Tool: "daily_activity", Input: compactMap(map[string]any{
			"XMLFormat": 0,
		})}}, nil
	case "rakuten-advertising":
		if from == "" || to == "" {
			return nil, errors.New("rakuten stats require from and to")
		}
		limit := boundedLimit(args, 100, 1000)
		return []providerCall{{Role: "rakuten-advertising", Tool: "transactions_list", Input: compactMap(map[string]any{
			"transaction_date_start": from + " 00:00:00",
			"transaction_date_end":   exclusiveEndDate(to) + " 00:00:00",
			"limit":                  limit, "page": 1, "currency": strArg(args, "currency"), "type": strArg(args, "type"),
		}), RecordKeys: []string{"transactions", "data", "items", "results"}, Pagination: &providerPagination{Mode: "page", Param: "page", PageSize: limit, MaxPages: maxPageArg(args), Start: 1}}}, nil
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
			"Type":       "Regular",
			"subId1":     strArg(args, "campaign"),
			"subId2":     strArg(args, "subid"),
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
	case "ebay-partner-network":
		return providerCall{}, errors.New("eBay links must be created from a synced eBay product because Browse returns the commissionable itemAffiliateWebUrl")
	case "rakuten-advertising":
		advertiserID := firstNonEmpty(strArg(args, "advertiser_id"), offerExternalID)
		if advertiserID == "" {
			return providerCall{}, errors.New("rakuten link creation requires advertiser_id or offer_id")
		}
		return providerCall{Role: "rakuten-advertising", Tool: "deep_link_generate", Input: compactMap(map[string]any{
			"url": destination, "advertiser_id": toInt64(advertiserID), "u1": firstNonEmpty(strArg(args, "subid"), strArg(args, "campaign")),
		})}, nil
	default:
		return providerCall{}, fmt.Errorf("unsupported affiliate network %q", network)
	}
}

func offerInputFromMap(network string, m map[string]any) OfferInput {
	network = canonicalNetworkKey(network)
	merchant := firstString(m, "merchant_name", "merchant", "advertiser.name", "advertiserName", "advertiser_name", "AdvertiserName", "advertiser-name", "merchantGroupName", "company.name", "company_name", "programmeInfo.name")
	name := firstString(m, "offer_name", "offerName", "name", "title", "campaign_name", "CampaignName", "programName", "display_name", "company.name", "programmeInfo.name")
	if merchant == "" {
		merchant = name
	}
	if name == "" {
		name = merchant
	}
	externalID := firstString(m, "external_id", "id", "slug", "sid", "key", "company_key", "asin", "offerSid", "offer_id", "offerId", "campaign_id", "campaignId", "CampaignId", "advertiserId", "advertiser-id", "advertiser.id", "merchantGroupId", "programmeInfo.id")
	if network == "partnerstack" {
		externalID = firstNonEmpty(firstString(m, "company.key", "company_key"), externalID)
	}
	return OfferInput{
		NetworkKey:        network,
		ExternalID:        externalID,
		MerchantName:      merchant,
		OfferName:         name,
		Status:            strings.ToLower(firstString(m, "status", "relationship_status", "relationship-status", "approval_status", "approvalStatus", "approved_status", "ContractStatus", "membershipStatus", "programmeInfo.membershipStatus")),
		Category:          firstString(m, "category", "primaryCategory", "Category", "primarySector", "advertiser.categories", "programmeInfo.primarySector"),
		Vertical:          firstString(m, "vertical"),
		CountriesJSON:     mustJSON(firstAny(m, "countries", "country", "targetedCountries", "allowed_countries", "ShippingRegions", "primaryRegion", "programmeInfo.primaryRegion")),
		CommissionSummary: firstNonEmpty(firstString(m, "commission_summary", "commission", "payout", "payout_summary", "defaultCommissionRate", "rate"), pricingSummaryFromMap(m)),
		CookieWindow:      firstString(m, "cookie_window", "cookieWindow", "cookie_expiration", "cookieExpiration", "cookieDuration", "tracking.cookieExpiration", "ReferralPeriod"),
		TrackingDeepLink:  firstBool(m, "tracking_deeplink", "deeplinking", "tracking.deeplinking", "deepLinking", "deeplink", "AllowsDeeplinking", "deeplinkEnabled", "programmeInfo.deeplinkEnabled"),
		RawJSON:           mustJSON(redactSecrets(m)),
	}
}

func productRecordsFromPage(network string, page providerPage) ([]map[string]any, error) {
	if canonicalNetworkKey(network) != "awin" {
		return page.Records, nil
	}
	raw, _ := firstAny(page.Output, "data").(string)
	if strings.TrimSpace(raw) == "" {
		return nil, errors.New("product feed returned no CSV data")
	}
	return parseCSVRecords(raw)
}

func parseCSVRecords(raw string) ([]map[string]any, error) {
	reader := csv.NewReader(strings.NewReader(raw))
	firstLine := raw
	if newline := strings.IndexByte(firstLine, '\n'); newline >= 0 {
		firstLine = firstLine[:newline]
	}
	if strings.Count(firstLine, "\t") > strings.Count(firstLine, ",") {
		reader.Comma = '\t'
	}
	reader.FieldsPerRecord = -1
	rows, err := reader.ReadAll()
	if err != nil {
		return nil, err
	}
	if len(rows) < 2 {
		return nil, nil
	}
	headers := rows[0]
	if len(headers) > 0 {
		headers[0] = strings.TrimPrefix(headers[0], "\ufeff")
	}
	records := make([]map[string]any, 0, len(rows)-1)
	for _, row := range rows[1:] {
		record := make(map[string]any, len(headers))
		for index, header := range headers {
			if index < len(row) {
				record[header] = row[index]
			}
		}
		records = append(records, record)
	}
	return records, nil
}

func productInputFromMap(network string, m map[string]any) ProductInput {
	network = canonicalNetworkKey(network)
	merchantID := firstString(m, "merchant_id", "merchantId", "advertiser-id", "advertiser_id", "advertiserId", "mid")
	merchantName := firstString(m, "merchant_name", "merchantName", "advertiser-name", "advertiser_name", "advertiserName", "seller.username")
	name := firstString(m, "product_name", "productname", "name", "title", "ItemInfo.Title.DisplayValue")
	externalID := firstString(m, "aw_product_id", "itemId", "ASIN", "asin", "linkid", "linkId", "product-id", "product_id", "id", "sku")
	sku := firstString(m, "merchant_product_id", "sku", "SKU", "legacyItemId")
	if network == "cj-affiliate" && merchantID != "" && sku != "" {
		externalID = merchantID + ":" + sku
	}
	brand := firstString(m, "brand_name", "brand", "manufacturer-name", "manufacturer_name", "ItemInfo.ByLineInfo.Brand.DisplayValue", "ItemInfo.ByLineInfo.Manufacturer.DisplayValue")
	if brand == "" && network == "ebay-partner-network" {
		brand = aspectValue(m, "Brand")
	}
	category := firstString(m, "category_name", "merchant_category", "category.primary", "primary-category", "primary_category", "ItemInfo.Classifications.ProductGroup.DisplayValue")
	if category == "" && network == "ebay-partner-network" {
		category = firstArrayString(m, "categories", "categoryName")
	}
	priceValue := firstAny(m, "search_price", "price", "retail-price", "retail_price")
	saleValue := firstAny(m, "store_price", "saleprice", "sale-price", "sale_price")
	currency := firstNonEmpty(currencyFromAny(priceValue), strings.ToUpper(firstString(m, "currency", "currencyCode")))
	priceCents := productMoneyCents(priceValue)
	salePriceCents := productMoneyCents(saleValue)

	switch network {
	case "amazon-associates":
		listing := firstArrayMap(m, "OffersV2.Listings")
		if len(listing) == 0 {
			listing = firstArrayMap(m, "Offers.Listings")
		}
		price := firstArrayMap(listing, "Price")
		current := firstAny(price, "Money")
		if current == nil {
			current = firstAny(listing, "Price")
		}
		original := firstAny(price, "SavingBasis.Money")
		if original == nil {
			original = firstAny(listing, "SavingBasis")
		}
		priceCents = productMoneyCents(original)
		salePriceCents = productMoneyCents(current)
		if priceCents == 0 {
			priceCents, salePriceCents = salePriceCents, 0
		}
		currency = firstNonEmpty(currencyFromAny(current), currencyFromAny(original))
		if merchantName == "" {
			merchantName = firstNonEmpty(firstString(listing, "MerchantInfo.Name"), "Amazon")
		}
	case "ebay-partner-network":
		current := firstAny(m, "price", "currentBidPrice")
		original := firstAny(m, "marketingPrice.originalPrice")
		priceCents = productMoneyCents(original)
		salePriceCents = productMoneyCents(current)
		if priceCents == 0 {
			priceCents, salePriceCents = salePriceCents, 0
		}
		currency = firstNonEmpty(currencyFromAny(current), currencyFromAny(original))
	}
	if salePriceCents >= priceCents && priceCents > 0 {
		salePriceCents = 0
	}
	affiliateURL := firstString(m, "aw_deep_link", "itemAffiliateWebUrl", "DetailPageURL", "detailPageURL", "buy-url", "buy_url", "linkurl", "linkUrl")
	destinationURL := firstString(m, "merchant_deep_link", "itemWebUrl", "destinationUrl", "destination_url", "url")
	if destinationURL == "" {
		destinationURL = affiliateURL
	}
	availability := strings.ToLower(firstString(m, "stock_status", "availability", "estimatedAvailabilityStatus", "in-stock", "in_stock", "OffersV2.Listings.Availability.Message", "OffersV2.Listings.Availability.Type"))
	if availability == "" && firstAny(m, "Offers.Listings", "OffersV2.Listings") != nil {
		availability = "available"
	}
	return ProductInput{
		NetworkKey: network, ExternalID: externalID, MerchantID: merchantID, MerchantName: merchantName,
		Name: name, Description: firstString(m, "description", "description.long", "description.short", "product_short_description", "ItemInfo.Features.DisplayValues"),
		Category: category, Brand: brand, SKU: sku,
		GTIN:     firstString(m, "product_GTIN", "gtin", "ean", "upc", "upccode", "upc-code", "ItemInfo.ExternalIds.EANs.DisplayValues", "ItemInfo.ExternalIds.UPCs.DisplayValues"),
		Currency: currency, PriceCents: priceCents, SalePriceCents: salePriceCents,
		ImageURL:       firstString(m, "merchant_image_url", "image-url", "image_url", "imageurl", "image.imageUrl", "Images.Primary.Large.URL", "Images.Primary.Medium.URL"),
		DestinationURL: destinationURL, AffiliateURL: affiliateURL, Availability: availability,
		RawJSON: mustJSON(redactSecrets(m)),
	}
}

func productMoneyCents(value any) int64 {
	if object, ok := value.(map[string]any); ok {
		return moneyCentsFromAny(firstAny(object, "value", "Value", "amount", "Amount", "#text", "DisplayAmount"))
	}
	return moneyCentsFromAny(value)
}

func currencyFromAny(value any) string {
	if object, ok := value.(map[string]any); ok {
		return strings.ToUpper(firstString(object, "currency", "Currency", "currencyCode", "@currency"))
	}
	return ""
}

func firstArrayMap(m map[string]any, path string) map[string]any {
	value := firstAny(m, path)
	if object, ok := value.(map[string]any); ok {
		return object
	}
	if values, ok := value.([]any); ok {
		for _, value := range values {
			if object, ok := value.(map[string]any); ok {
				return object
			}
		}
	}
	return map[string]any{}
}

func firstArrayString(m map[string]any, path, key string) string {
	value := firstAny(m, path)
	if values, ok := value.([]any); ok {
		for _, value := range values {
			if object, ok := value.(map[string]any); ok {
				if text := firstString(object, key); text != "" {
					return text
				}
			}
		}
	}
	return firstString(firstArrayMap(m, path), key)
}

func aspectValue(m map[string]any, name string) string {
	value := firstAny(m, "localizedAspects")
	values, _ := value.([]any)
	for _, value := range values {
		object, _ := value.(map[string]any)
		if strings.EqualFold(firstString(object, "name"), name) {
			return firstString(object, "value")
		}
	}
	return ""
}

func commaSeparated(value string) []string {
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part = strings.TrimSpace(part); part != "" {
			out = append(out, part)
		}
	}
	return out
}

func pricingSummaryFromMap(m map[string]any) string {
	if terms, ok := firstAny(m, "PayoutTermsList").([]any); ok {
		parts := make([]string, 0, len(terms))
		for _, item := range terms {
			term, ok := item.(map[string]any)
			if !ok {
				continue
			}
			label := firstString(term, "TrackerName", "TrackerType")
			if amount := firstString(term, "PayoutAmount", "PayoutAmountLowerLimit"); amount != "" {
				parts = append(parts, strings.TrimSpace(fmt.Sprintf("%s %s %s", amount, firstString(term, "PayoutCurrency"), label)))
			} else if percentage := firstString(term, "PayoutPercentage", "PayoutPercentageLowerLimit"); percentage != "" {
				parts = append(parts, strings.TrimSpace(fmt.Sprintf("%s%% %s", percentage, label)))
			}
		}
		return joinPricingParts(parts)
	}
	if ranges, ok := firstAny(m, "commissionRange").([]any); ok {
		parts := make([]string, 0, len(ranges))
		currency := firstString(m, "currencyCode", "programmeInfo.currencyCode")
		for _, item := range ranges {
			rate, ok := item.(map[string]any)
			if !ok {
				continue
			}
			minValue := firstString(rate, "min")
			maxValue := firstString(rate, "max")
			if minValue == "" {
				continue
			}
			value := minValue
			if maxValue != "" && maxValue != minValue {
				value += "-" + maxValue
			}
			if strings.EqualFold(firstString(rate, "type"), "percentage") {
				value += "%"
			} else if currency != "" {
				value += " " + currency
			}
			parts = append(parts, value)
		}
		return joinPricingParts(parts)
	}
	raw := firstAny(m, "pricings", "priceCombinations", "offers")
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
	return joinPricingParts(parts)
}

func joinPricingParts(parts []string) string {
	if len(parts) > 3 {
		parts = parts[:3]
	}
	return strings.Join(parts, "; ")
}

func statInputFromMap(network string, m map[string]any) StatInput {
	network = canonicalNetworkKey(network)
	switch network {
	case "impact":
		return StatInput{
			Date:            normalizeStatDate(firstString(m, "EventDate", "CreationDate", "ActionDate")),
			NetworkKey:      network,
			Conversions:     boolCount(firstString(m, "Id", "ActionId") != ""),
			RevenueCents:    moneyCentsFromAny(firstAny(m, "Amount")),
			CommissionCents: moneyCentsFromAny(firstAny(m, "Payout")),
			Currency:        firstNonEmpty(firstString(m, "Currency"), "USD"),
			RawJSON:         mustJSON(redactSecrets(m)),
		}
	case "awin":
		if firstAny(m, "quantity") != nil {
			return StatInput{
				Date:            normalizeStatDate(firstString(m, "date")),
				NetworkKey:      network,
				Clicks:          toInt64(firstAny(m, "quantity.clicks", "clicks")),
				Conversions:     toInt64(firstAny(m, "quantity.total", "totalNo", "transactions")),
				RevenueCents:    moneyCentsFromAny(firstAny(m, "saleAmount.total", "totalValue")),
				CommissionCents: moneyCentsFromAny(firstAny(m, "commissionAmount.total", "totalComm")),
				Currency:        firstNonEmpty(firstString(m, "currency"), "USD"),
				RawJSON:         mustJSON(redactSecrets(m)),
			}
		}
		return StatInput{
			Date:            normalizeStatDate(firstString(m, "transactionDate", "validationDate")),
			NetworkKey:      network,
			Conversions:     boolCount(firstString(m, "id") != ""),
			RevenueCents:    moneyCentsFromAny(firstAny(m, "saleAmount.amount")),
			CommissionCents: moneyCentsFromAny(firstAny(m, "commissionAmount.amount")),
			Currency:        firstNonEmpty(firstString(m, "commissionAmount.currency", "saleAmount.currency"), "USD"),
			RawJSON:         mustJSON(redactSecrets(m)),
		}
	case "partnerstack":
		key := firstString(m, "key")
		isReward := strings.HasPrefix(key, "rew") || strings.HasPrefix(key, "rwrd") || firstAny(m, "reward_status", "source") != nil
		row := StatInput{
			Date:       normalizeStatDate(firstString(m, "created_at")),
			NetworkKey: network,
			Currency:   firstNonEmpty(firstString(m, "currency"), "USD"),
			RawJSON:    mustJSON(redactSecrets(m)),
		}
		if isReward {
			row.CommissionCents = centsFromAny(firstAny(m, "amount"))
		} else {
			row.Conversions = boolCount(firstString(m, "key") != "" && !firstBool(m, "archived"))
			row.RevenueCents = centsFromAny(firstAny(m, "amount"))
		}
		return row
	case "sovrn":
		if firstAny(m, "clicks", "Clicks", "sales", "Sales", "actions", "Actions") != nil {
			return StatInput{
				Date:            normalizeStatDate(firstString(m, "date", "clickDate")),
				NetworkKey:      network,
				Clicks:          toInt64(firstAny(m, "clicks", "Clicks")),
				Conversions:     toInt64(firstAny(m, "actions", "Actions", "sales", "Sales")),
				CommissionCents: moneyCentsFromAny(firstAny(m, "revenue", "Revenue", "publisherRevenue", "publisherNetRevenue")),
				Currency:        firstNonEmpty(firstString(m, "currency", "Currency"), "USD"),
				RawJSON:         mustJSON(redactSecrets(m)),
			}
		}
	case "rakuten-advertising":
		return StatInput{
			Date:            normalizeStatDate(firstString(m, "transaction_date", "process_date")),
			NetworkKey:      network,
			Conversions:     boolCount(firstString(m, "etransaction_id", "order_id") != ""),
			RevenueCents:    moneyCentsFromAny(firstAny(m, "sale_amount")),
			CommissionCents: moneyCentsFromAny(firstAny(m, "commissions")),
			Currency:        firstNonEmpty(firstString(m, "currency"), "USD"),
			RawJSON:         mustJSON(redactSecrets(m)),
		}
	}
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
	if n, err := strconv.ParseInt(s, 10, 64); err == nil {
		if n > 10_000_000_000 {
			n /= 1000
		}
		return time.Unix(n, 0).UTC().Format("2006-01-02")
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
	return firstString(m, "external_id", "id", "Id", "slug", "sid", "key", "asin", "ASIN", "itemId", "linkid", "product_id", "product-id", "sku", "offerSid", "offer_id", "offerId", "CampaignId", "campaignId", "advertiserId", "merchantGroupId", "company_key", "date", "day", "clickDate", "ActionDate", "EventDate", "eventDate", "transactionDate", "created_at") != ""
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

func manualProductSchema(required []string) map[string]any {
	return schemaObject(map[string]any{
		"id":               map[string]any{"type": "integer"},
		"network":          map[string]any{"type": "string"},
		"external_id":      map[string]any{"type": "string"},
		"offer_id":         map[string]any{"type": "integer"},
		"name":             map[string]any{"type": "string"},
		"description":      map[string]any{"type": "string"},
		"merchant_id":      map[string]any{"type": "string"},
		"merchant_name":    map[string]any{"type": "string"},
		"category":         map[string]any{"type": "string"},
		"brand":            map[string]any{"type": "string"},
		"sku":              map[string]any{"type": "string"},
		"gtin":             map[string]any{"type": "string"},
		"currency":         map[string]any{"type": "string"},
		"price_cents":      map[string]any{"type": "integer"},
		"sale_price_cents": map[string]any{"type": "integer"},
		"image_url":        map[string]any{"type": "string"},
		"destination_url":  map[string]any{"type": "string"},
		"affiliate_url":    map[string]any{"type": "string"},
		"availability":     map[string]any{"type": "string"},
		"asin":             map[string]any{"type": "string"},
		"marketplace":      map[string]any{"type": "string"},
		"partner_tag":      map[string]any{"type": "string"},
		"partnerTag":       map[string]any{"type": "string"},
	}, required)
}

func normalizeManualProductInput(db *sql.DB, args map[string]any) (ProductInput, error) {
	network := canonicalNetworkKey(strArg(args, "network"))
	name := strArg(args, "name")
	if network == "" {
		return ProductInput{}, errors.New("network required")
	}
	if name == "" {
		return ProductInput{}, errors.New("name required")
	}
	offerID := toInt64(args["offer_id"])
	if offerID < 0 {
		return ProductInput{}, errors.New("offer_id must be positive")
	}
	if offerID > 0 {
		offer, err := dbGetOffer(db, offerID)
		if err != nil {
			return ProductInput{}, err
		}
		if offer == nil {
			return ProductInput{}, fmt.Errorf("offer_id %d not found", offerID)
		}
		if offer.NetworkKey != network {
			return ProductInput{}, fmt.Errorf("offer_id %d belongs to %s, not %s", offerID, offer.NetworkKey, network)
		}
	}

	destination := firstNonEmpty(strArg(args, "destination_url"), strArg(args, "url"))
	affiliateURL := strArg(args, "affiliate_url")
	externalID := strArg(args, "external_id")
	if network == "amazon-associates" {
		marketplace, err := normalizeAmazonMarketplace(argOrConfig(args, "marketplace", "amazon_marketplace", "www.amazon.com"))
		if err != nil {
			return ProductInput{}, err
		}
		asin := normalizeASIN(firstNonEmpty(strArg(args, "asin"), externalID, asinFromURL(destination)))
		if asin == "" {
			return ProductInput{}, errors.New("valid 10-character Amazon ASIN or Amazon product URL required")
		}
		externalID = asin
		if destination == "" {
			destination = amazonProductURL(marketplace, asin, "")
		} else if err := validateAmazonDestination(destination, marketplace); err != nil {
			return ProductInput{}, err
		}
		if affiliateURL == "" {
			partnerTag := firstNonEmpty(strArg(args, "partner_tag"), argOrConfig(args, "partnerTag", "amazon_partner_tag", ""))
			if partnerTag == "" {
				return ProductInput{}, errors.New("partner_tag required when affiliate_url is not supplied")
			}
			affiliateURL = amazonProductURL(marketplace, asin, partnerTag)
		}
	} else {
		if externalID == "" {
			var err error
			externalID, err = newManualExternalID()
			if err != nil {
				return ProductInput{}, err
			}
		}
		if destination == "" || affiliateURL == "" {
			return ProductInput{}, errors.New("destination_url and affiliate_url required for non-Amazon manual products")
		}
	}
	if err := validateAbsoluteURL(destination); err != nil {
		return ProductInput{}, fmt.Errorf("destination_url: %w", err)
	}
	if err := validateAbsoluteURL(affiliateURL); err != nil {
		return ProductInput{}, fmt.Errorf("affiliate_url: %w", err)
	}
	if imageURL := strArg(args, "image_url"); imageURL != "" {
		if err := validateAbsoluteURL(imageURL); err != nil {
			return ProductInput{}, fmt.Errorf("image_url: %w", err)
		}
	}
	currency := strings.ToUpper(strArg(args, "currency"))
	if currency != "" && len(currency) != 3 {
		return ProductInput{}, errors.New("currency must be a three-letter code")
	}
	priceCents := toInt64(args["price_cents"])
	salePriceCents := toInt64(args["sale_price_cents"])
	if priceCents < 0 || salePriceCents < 0 {
		return ProductInput{}, errors.New("prices cannot be negative")
	}
	availability := strArg(args, "availability")
	if availability == "" {
		availability = "available"
	}
	return ProductInput{
		NetworkKey: network, ExternalID: externalID, Source: "manual", Status: "active",
		OfferID: offerID, MerchantID: strArg(args, "merchant_id"), MerchantName: strArg(args, "merchant_name"),
		Name: name, Description: strArg(args, "description"), Category: strArg(args, "category"),
		Brand: strArg(args, "brand"), SKU: strArg(args, "sku"), GTIN: strArg(args, "gtin"),
		Currency: currency, PriceCents: priceCents, SalePriceCents: salePriceCents,
		ImageURL: strArg(args, "image_url"), DestinationURL: destination, AffiliateURL: affiliateURL,
		Availability: availability, RawJSON: mustJSON(redactSecrets(args)),
	}, nil
}

func manualProductArgs(product *Product) map[string]any {
	args := map[string]any{
		"network": product.NetworkKey, "external_id": product.ExternalID, "offer_id": product.OfferID,
		"name": product.Name, "description": product.Description, "merchant_id": product.MerchantID,
		"merchant_name": product.MerchantName, "category": product.Category, "brand": product.Brand,
		"sku": product.SKU, "gtin": product.GTIN, "currency": product.Currency,
		"price_cents": product.PriceCents, "sale_price_cents": product.SalePriceCents,
		"image_url": product.ImageURL, "destination_url": product.DestinationURL,
		"affiliate_url": product.AffiliateURL, "availability": product.Availability,
	}
	if product.NetworkKey == "amazon-associates" {
		if destination, err := url.Parse(product.DestinationURL); err == nil {
			args["marketplace"] = destination.Hostname()
		}
	}
	return args
}

func normalizeASIN(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	if len(value) != 10 {
		return ""
	}
	for _, r := range value {
		if !(r >= 'A' && r <= 'Z') && !(r >= '0' && r <= '9') {
			return ""
		}
	}
	return value
}

func normalizeAmazonMarketplace(value string) (string, error) {
	value = strings.TrimSpace(strings.ToLower(value))
	if value == "" {
		value = "www.amazon.com"
	}
	if !strings.Contains(value, "://") {
		value = "https://" + value
	}
	u, err := url.Parse(value)
	if err != nil || u.Hostname() == "" || (u.Path != "" && u.Path != "/") || u.RawQuery != "" || u.Fragment != "" {
		return "", errors.New("marketplace must be an Amazon hostname such as www.amazon.com")
	}
	host := strings.ToLower(u.Hostname())
	base := strings.TrimPrefix(host, "www.")
	if !strings.HasPrefix(base, "amazon.") {
		return "", errors.New("marketplace must be an Amazon hostname such as www.amazon.com")
	}
	return host, nil
}

func validateAmazonDestination(raw, marketplace string) error {
	if err := validateAbsoluteURL(raw); err != nil {
		return fmt.Errorf("destination_url: %w", err)
	}
	u, _ := url.Parse(raw)
	destinationHost := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
	marketplaceHost := strings.TrimPrefix(strings.ToLower(marketplace), "www.")
	if destinationHost != marketplaceHost {
		return fmt.Errorf("destination_url host %q does not match marketplace %q", u.Hostname(), marketplace)
	}
	return nil
}

func amazonProductURL(marketplace, asin, partnerTag string) string {
	u := &url.URL{Scheme: "https", Host: marketplace, Path: "/dp/" + asin + "/ref=nosim"}
	if partnerTag != "" {
		query := u.Query()
		query.Set("tag", partnerTag)
		u.RawQuery = query.Encode()
	}
	return u.String()
}

func newManualExternalID() (string, error) {
	value := make([]byte, 8)
	if _, err := rand.Read(value); err != nil {
		return "", fmt.Errorf("generate manual product id: %w", err)
	}
	return "manual:" + hex.EncodeToString(value), nil
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

func boolCount(v bool) int64 {
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
		date = date.AddDate(0, 0, 1).Add(-time.Millisecond)
	}
	return date.UnixMilli()
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
		"itemInfo.classifications",
		"itemInfo.externalIds",
		"offersV2.listings.availability",
		"offersV2.listings.merchantInfo",
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
		if part == "dp" && i+1 < len(parts) {
			return normalizeASIN(parts[i+1])
		}
		if part == "gp" && i+2 < len(parts) && parts[i+1] == "product" {
			return normalizeASIN(parts[i+2])
		}
		if part == "product" && i+1 < len(parts) {
			return normalizeASIN(parts[i+1])
		}
		if asin := normalizeASIN(part); asin != "" {
			return asin
		}
	}
	return ""
}

func providerAffiliateURL(out map[string]any) string {
	for _, key := range []string{
		"affiliate_url", "clickUrl", "click_url", "trackingLink", "TrackingLink",
		"TrackingURL", "optimized", "url", "detailPageURL", "searchURL", "shortUrl",
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
		for _, key := range []string{"affiliate_url", "clickUrl", "click_url", "trackingLink", "TrackingLink", "TrackingURL", "optimized", "detailPageURL", "searchURL", "url"} {
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
	case []any:
		for _, item := range x {
			if text := scalarString(item); text != "" {
				return text
			}
		}
	case map[string]any:
		return firstString(x, "#text", "value", "Value", "DisplayValue")
	}
	return ""
}

func scalarString(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case float64:
		return strconv.FormatFloat(x, 'f', -1, 64)
	case int:
		return strconv.Itoa(x)
	case int64:
		return strconv.FormatInt(x, 10)
	case json.Number:
		return x.String()
	case map[string]any:
		return firstString(x, "#text", "value", "Value", "DisplayValue")
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
	case "ebay-partner-network":
		return "eBay Partner Network"
	case "rakuten-advertising":
		return "Rakuten Advertising"
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

func slugForLink(link *Link, offer *Offer, product *Product) string {
	base := link.Campaign
	if base == "" && offer != nil {
		base = offer.MerchantName
	}
	if base == "" && product != nil {
		base = firstNonEmpty(product.MerchantName, product.Name)
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
