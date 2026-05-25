// Apteva Affiliate app — publisher-side affiliate manager.
//
// The app owns a small normalized model: networks, offers, links, and
// daily stats. Provider-specific apps stay as thin adapters; this app
// calls them through PlatformAPI.CallAppResult when a refresh or link
// generation needs external data.
package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: affiliate
display_name: Affiliate
version: 0.1.0
description: Publisher-side affiliate manager.
author: Apteva
scopes: [project, global]
requires:
  permissions:
    - db.write.app
    - platform.apps.call
  apps:
    - name: redirects
      optional: true
      reason: Creates branded short links for affiliate URLs.
    - name: target-circle
      optional: true
    - name: impact
      optional: true
    - name: awin
      optional: true
    - name: cj-affiliate
      optional: true
    - name: amazon-associates
      optional: true
    - name: skimlinks
      optional: true
    - name: sovrn
      optional: true
    - name: partnerstack
      optional: true
    - name: shareasale
      optional: true
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
      description: Read normalized stats.
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
			Description: "Refresh offers and/or stats from a provider app dependency. " +
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
			Description: "Search normalized offers. Args: q?, network?, status?, limit? (default 50).",
			InputSchema: schemaObject(map[string]any{
				"q":       map[string]any{"type": "string"},
				"network": map[string]any{"type": "string"},
				"status":  map[string]any{"type": "string"},
				"limit":   map[string]any{"type": "integer"},
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
			Description: "Search managed links. Args: q?, network?, offer_id?, status?, limit? (default 50).",
			InputSchema: schemaObject(map[string]any{
				"q":        map[string]any{"type": "string"},
				"network":  map[string]any{"type": "string"},
				"offer_id": map[string]any{"type": "integer"},
				"status":   map[string]any{"type": "string"},
				"limit":    map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolLinks,
		},
		{
			Name: "affiliate_stats",
			Description: "Read normalized stats. Args: from?, to?, network?, offer_id?, link_id?, " +
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

func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

type Network struct {
	ID              int64  `json:"id"`
	Key             string `json:"key"`
	Name            string `json:"name"`
	Enabled         bool   `json:"enabled"`
	ConnectionRef   string `json:"connection_ref,omitempty"`
	LastRefreshedAt string `json:"last_refreshed_at,omitempty"`
	MetadataJSON    string `json:"metadata_json,omitempty"`
	CreatedAt       string `json:"created_at,omitempty"`
	UpdatedAt       string `json:"updated_at,omitempty"`
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
	Conversions     int64  `json:"conversions"`
	RevenueCents    int64  `json:"revenue_cents"`
	CommissionCents int64  `json:"commission_cents"`
	Currency        string `json:"currency"`
}

type RefreshSummary struct {
	Network           string `json:"network"`
	Kind              string `json:"kind"`
	OffersUpserted    int    `json:"offers_upserted"`
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
	meta := mustJSON(metadata)
	_, err := db.Exec(`
		INSERT INTO networks (key, name, enabled, metadata_json, created_at, updated_at)
		VALUES (?, ?, 1, ?, ?, ?)
		ON CONFLICT(key) DO UPDATE SET
			name = excluded.name,
			metadata_json = excluded.metadata_json,
			last_refreshed_at = excluded.updated_at,
			updated_at = excluded.updated_at`,
		key, name, meta, now, now)
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

func dbListOffers(db *sql.DB, q, network, status string, limit int) ([]Offer, error) {
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
	args = append(args, limit)
	rows, err := db.Query(`SELECT `+offerCols+` FROM offers WHERE `+strings.Join(where, " AND ")+` ORDER BY merchant_name, offer_name LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Offer
	for rows.Next() {
		o, err := scanOffer(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *o)
	}
	return out, rows.Err()
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

func dbUpdateLinkShortURL(db *sql.DB, id, redirectRuleID int64, shortURL string) (*Link, error) {
	_, err := db.Exec(`UPDATE links SET short_url = ?, redirect_rule_id = ?, updated_at = ? WHERE id = ?`,
		shortURL, redirectRuleID, nowUTC(), id)
	if err != nil {
		return nil, err
	}
	return dbGetLink(db, id)
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

func dbListLinks(db *sql.DB, q, network string, offerID int64, status string, limit int) ([]Link, error) {
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	where := []string{"1=1"}
	args := []any{}
	if network != "" {
		where = append(where, "network_key = ?")
		args = append(args, network)
	}
	if offerID != 0 {
		where = append(where, "offer_id = ?")
		args = append(args, offerID)
	}
	if status != "" {
		where = append(where, "status = ?")
		args = append(args, status)
	}
	if q != "" {
		like := "%" + q + "%"
		where = append(where, "(destination_url LIKE ? OR affiliate_url LIKE ? OR short_url LIKE ? OR campaign LIKE ? OR subid LIKE ?)")
		args = append(args, like, like, like, like, like)
	}
	args = append(args, limit)
	rows, err := db.Query(`SELECT `+linkCols+` FROM links WHERE `+strings.Join(where, " AND ")+` ORDER BY id DESC LIMIT ?`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Link
	for rows.Next() {
		l, err := scanLink(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *l)
	}
	return out, rows.Err()
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
	return out, rows.Err()
}

// --- Tool handlers ---------------------------------------------------------

func (a *App) toolNetworks(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	enabled := optionalBool(args, "enabled")
	rows, err := dbListNetworks(ctx.AppDB(), enabled)
	if err != nil {
		return nil, err
	}
	return map[string]any{"networks": rows, "count": len(rows)}, nil
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
	return a.refreshNetwork(ctx, network, kind, strArg(args, "from"), strArg(args, "to"), args)
}

func (a *App) toolOffers(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	network := strArg(args, "network")
	if network != "" {
		network = canonicalNetworkKey(network)
	}
	rows, err := dbListOffers(ctx.AppDB(), strArg(args, "q"), network, strArg(args, "status"), intArg(args, "limit", 50))
	if err != nil {
		return nil, err
	}
	return map[string]any{"offers": rows, "count": len(rows)}, nil
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
		RawJSON:        mustJSON(raw),
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
	rows, err := dbListLinks(ctx.AppDB(), strArg(args, "q"), network, toInt64(args["offer_id"]), strArg(args, "status"), intArg(args, "limit", 50))
	if err != nil {
		return nil, err
	}
	return map[string]any{"links": rows, "count": len(rows)}, nil
}

func (a *App) toolStats(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	network := strArg(args, "network")
	if network != "" {
		network = canonicalNetworkKey(network)
	}
	rows, err := dbStats(ctx.AppDB(), strArg(args, "from"), strArg(args, "to"), network, toInt64(args["offer_id"]), toInt64(args["link_id"]), strArg(args, "group_by"))
	if err != nil {
		return nil, err
	}
	return map[string]any{"stats": rows, "count": len(rows)}, nil
}

func (a *App) refreshNetwork(ctx *sdk.AppCtx, network, kind, from, to string, args map[string]any) (*RefreshSummary, error) {
	if ctx.PlatformAPI() == nil {
		return nil, errors.New("platform API unavailable")
	}
	network = canonicalNetworkKey(network)
	summary := &RefreshSummary{Network: network, Kind: kind, RefreshedAt: nowUTC()}
	if kind == "offers" || kind == "all" {
		calls, err := providerOfferCalls(network, args)
		if err != nil {
			return nil, err
		}
		for _, call := range calls {
			out := map[string]any{}
			if err := ctx.PlatformAPI().CallAppResult(call.App, call.Tool, call.Input, &out); err != nil {
				return nil, fmt.Errorf("%s.%s: %w", call.App, call.Tool, err)
			}
			offers := collectMaps(out, "offers", "Campaigns", "campaigns", "programs", "partnerships", "advertisers", "data", "items", "results")
			for _, m := range offers {
				in := offerInputFromMap(network, m)
				if in.ExternalID == "" || in.MerchantName == "" {
					continue
				}
				if _, err := dbUpsertOffer(ctx.AppDB(), in); err != nil {
					return nil, err
				}
				summary.OffersUpserted++
			}
			_, _ = dbUpsertNetwork(ctx.AppDB(), network, displayNetworkName(network), out)
		}
	}
	if kind == "stats" || kind == "all" {
		calls, err := providerStatCalls(network, from, to, args)
		if err != nil {
			return nil, err
		}
		for _, call := range calls {
			out := map[string]any{}
			if err := ctx.PlatformAPI().CallAppResult(call.App, call.Tool, call.Input, &out); err != nil {
				return nil, fmt.Errorf("%s.%s: %w", call.App, call.Tool, err)
			}
			stats := collectMaps(out, "stats", "Actions", "actions", "transactions", "Transactions", "rewards", "records", "data", "items", "results")
			for _, m := range stats {
				row := statInputFromMap(network, m)
				if row.Date == "" {
					continue
				}
				if ext := firstString(m, "offer_external_id", "offerId", "offer_id", "external_id", "CampaignId", "campaignId", "advertiserId", "merchantGroupId", "company_key"); ext != "" && row.OfferID == 0 {
					if o, err := dbGetOfferByExternal(ctx.AppDB(), network, ext); err == nil && o != nil {
						row.OfferID = o.ID
					}
				}
				if err := dbUpsertStat(ctx.AppDB(), row); err != nil {
					return nil, err
				}
				summary.StatsDaysUpserted++
			}
			_, _ = dbUpsertNetwork(ctx.AppDB(), network, displayNetworkName(network), out)
		}
	}
	return summary, nil
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
	if err := ctx.PlatformAPI().CallAppResult(call.App, call.Tool, call.Input, &out); err != nil {
		return nil, fmt.Errorf("%s.%s: %w", call.App, call.Tool, err)
	}
	return out, nil
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
	_ = json.NewDecoder(r.Body).Decode(&args)
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
		})
		writeToolResponse(w, out, err)
	case http.MethodPost:
		var args map[string]any
		_ = json.NewDecoder(r.Body).Decode(&args)
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
	out, err := a.toolStats(globalCtx, map[string]any{
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
	App   string
	Tool  string
	Input map[string]any
}

func providerOfferCalls(network string, args map[string]any) ([]providerCall, error) {
	network = canonicalNetworkKey(network)
	switch network {
	case "target-circle":
		return []providerCall{{App: "target-circle", Tool: "offers_list", Input: compactMap(map[string]any{
			"limit":  intArg(args, "limit", 100),
			"status": strArg(args, "status"),
		})}}, nil
	case "impact":
		return []providerCall{{App: "impact", Tool: "programs_list", Input: compactMap(map[string]any{
			"InsertionOrderStatus": argOrConfig(args, "InsertionOrderStatus", "impact_insertion_order_status", "Active"),
			"PageSize":             intArg(args, "limit", 100),
		})}}, nil
	case "awin":
		publisherID := requiredArgOrConfig(args, "publisherId", "awin_publisher_id")
		if publisherID == "" {
			return nil, errors.New("awin publisherId required; pass publisherId or set awin_publisher_id")
		}
		return []providerCall{{App: "awin", Tool: "programs_list", Input: compactMap(map[string]any{
			"publisherId":  publisherID,
			"relationship": argOrConfig(args, "relationship", "awin_relationship", "joined"),
			"countryCode":  strArg(args, "countryCode"),
		})}}, nil
	case "cj-affiliate":
		requestorCID := requiredArgOrConfig(args, "requestor-cid", "cj_requestor_cid")
		if requestorCID == "" {
			return nil, errors.New("cj-affiliate requestor-cid required; pass requestor-cid or set cj_requestor_cid")
		}
		return []providerCall{{App: "cj-affiliate", Tool: "advertisers_lookup", Input: compactMap(map[string]any{
			"requestor-cid":    requestorCID,
			"advertiser-ids":   argOrConfig(args, "advertiser-ids", "cj_advertiser_ids", "joined"),
			"keywords":         strArg(args, "keywords"),
			"records-per-page": intArg(args, "limit", 100),
		})}}, nil
	case "amazon-associates":
		partnerTag := requiredArgOrConfig(args, "partnerTag", "amazon_partner_tag")
		keywords := requiredArgOrConfig(args, "keywords", "amazon_keywords")
		if partnerTag == "" || keywords == "" {
			return nil, errors.New("amazon-associates partnerTag and keywords required; pass them or set amazon_partner_tag and amazon_keywords")
		}
		return []providerCall{{App: "amazon-associates", Tool: "items_search", Input: compactMap(map[string]any{
			"partnerTag":  partnerTag,
			"marketplace": argOrConfig(args, "marketplace", "amazon_marketplace", "www.amazon.com"),
			"keywords":    keywords,
			"searchIndex": argOrConfig(args, "searchIndex", "amazon_search_index", "All"),
			"itemCount":   intArg(args, "limit", 10),
			"resources":   defaultAmazonResources(),
		})}}, nil
	case "sovrn":
		campaignID := requiredArgOrConfig(args, "campaignId", "sovrn_campaign_id")
		if campaignID == "" {
			return nil, errors.New("sovrn campaignId required; pass campaignId or set sovrn_campaign_id")
		}
		return []providerCall{{App: "sovrn", Tool: "merchants_approved", Input: compactMap(map[string]any{
			"campaignId": toInt64(campaignID),
			"page":       intArg(args, "page", 1),
			"pageSize":   intArg(args, "limit", 1000),
			"filters":    firstAny(args, "filters"),
		})}}, nil
	case "partnerstack":
		return []providerCall{{App: "partnerstack", Tool: "partnerships_list", Input: compactMap(map[string]any{
			"include_offers":   true,
			"include_archived": boolArg(args, "include_archived", false),
			"limit":            intArg(args, "limit", 100),
		})}}, nil
	case "shareasale":
		return []providerCall{{App: "shareasale", Tool: "merchants_search", Input: compactMap(map[string]any{
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
		return []providerCall{{App: "target-circle", Tool: "transactions_list", Input: compactMap(map[string]any{
			"from":  from,
			"to":    to,
			"limit": intArg(args, "limit", 100),
		})}}, nil
	case "impact":
		return []providerCall{{App: "impact", Tool: "actions_list", Input: compactMap(map[string]any{
			"ActionDateStart": firstNonEmpty(from, strArg(args, "ActionDateStart")),
			"ActionDateEnd":   firstNonEmpty(to, strArg(args, "ActionDateEnd")),
			"CampaignId":      strArg(args, "CampaignId"),
			"State":           strArg(args, "State"),
			"PageSize":        intArg(args, "limit", 100),
		})}}, nil
	case "awin":
		publisherID := requiredArgOrConfig(args, "publisherId", "awin_publisher_id")
		if publisherID == "" || from == "" || to == "" {
			return nil, errors.New("awin stats require publisherId, from, and to")
		}
		return []providerCall{{App: "awin", Tool: "transactions_list", Input: compactMap(map[string]any{
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
		return []providerCall{{App: "cj-affiliate", Tool: "commission_query", Input: compactMap(map[string]any{
			"query": query,
			"variables": map[string]any{
				"since":  from,
				"before": to,
			},
		})}}, nil
	case "sovrn":
		date := firstNonEmpty(strArg(args, "clickDate"), from, time.Now().UTC().Format("2006-01-02"))
		return []providerCall{{App: "sovrn", Tool: "transactions_report", Input: compactMap(map[string]any{
			"clickDate":        date,
			"campaignIds":      strArg(args, "campaignIds"),
			"merchantGroupIds": strArg(args, "merchantGroupIds"),
			"programType":      strArg(args, "programType"),
		})}}, nil
	case "partnerstack":
		return []providerCall{{App: "partnerstack", Tool: "transactions_list", Input: compactMap(map[string]any{
			"min_created": toInt64(firstAny(args, "min_created")),
			"max_created": toInt64(firstAny(args, "max_created")),
			"limit":       intArg(args, "limit", 100),
		})}}, nil
	case "shareasale":
		return []providerCall{{App: "shareasale", Tool: "daily_activity", Input: compactMap(map[string]any{
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
		return providerCall{App: "target-circle", Tool: "codes_list", Input: compactMap(map[string]any{
			"offerSid":             offerSID,
			"adInventorySid":       argOrConfig(args, "adInventorySid", "target_circle_ad_inventory_sid", ""),
			"deeplink":             destination,
			"parameters[ref1]":     strArg(args, "campaign"),
			"parameters[click_id]": strArg(args, "subid"),
		})}, nil
	case "impact":
		programID := firstNonEmpty(strArg(args, "program_id"), offerExternalID)
		if programID == "" {
			return providerCall{}, errors.New("impact link creation requires program_id or offer_id")
		}
		return providerCall{App: "impact", Tool: "tracking_link_create", Input: compactMap(map[string]any{
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
		return providerCall{App: "awin", Tool: "tracking_link_generate", Input: compactMap(map[string]any{
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
		websiteID := requiredArgOrConfig(args, "website-id", "cj_website_id")
		if websiteID == "" {
			return providerCall{}, errors.New("cj-affiliate link creation requires website-id or cj_website_id")
		}
		return providerCall{App: "cj-affiliate", Tool: "links_search", Input: compactMap(map[string]any{
			"website-id":           websiteID,
			"advertiser-ids":       firstNonEmpty(strArg(args, "advertiser-ids"), offerExternalID, "joined"),
			"keywords":             firstNonEmpty(strArg(args, "keywords"), strArg(args, "campaign")),
			"allow-deep-linking":   "true",
			"records-per-page":     1,
			"promotion-end-date":   strArg(args, "promotion-end-date"),
			"promotion-start-date": strArg(args, "promotion-start-date"),
		})}, nil
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
		return providerCall{App: "amazon-associates", Tool: "items_get", Input: map[string]any{
			"partnerTag":  partnerTag,
			"marketplace": argOrConfig(args, "marketplace", "amazon_marketplace", "www.amazon.com"),
			"itemIds":     []string{asin},
			"itemIdType":  "ASIN",
			"resources":   defaultAmazonResources(),
		}}, nil
	case "skimlinks":
		return providerCall{App: "skimlinks", Tool: "link_wrapper_url", Input: compactMap(map[string]any{
			"url":   destination,
			"xs":    1,
			"xcust": firstNonEmpty(strArg(args, "subid"), strArg(args, "campaign")),
		})}, nil
	case "sovrn":
		return providerCall{App: "sovrn", Tool: "affiliate_link_url", Input: compactMap(map[string]any{
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
		ExternalID:        firstString(m, "external_id", "id", "slug", "key", "company_key", "asin", "offer_id", "offerId", "campaign_id", "campaignId", "CampaignId", "advertiserId", "advertiser-id", "merchantGroupId"),
		MerchantName:      merchant,
		OfferName:         name,
		Status:            strings.ToLower(firstString(m, "status", "relationship_status", "relationship-status", "approval_status", "approvalStatus", "ContractStatus")),
		Category:          firstString(m, "category", "primaryCategory", "Category"),
		Vertical:          firstString(m, "vertical"),
		CountriesJSON:     mustJSON(firstAny(m, "countries", "country", "allowed_countries", "ShippingRegions")),
		CommissionSummary: firstString(m, "commission_summary", "commission", "payout", "payout_summary", "commissionRange", "defaultCommissionRate", "rate"),
		CookieWindow:      firstString(m, "cookie_window", "cookieWindow", "cookie_expiration", "cookieExpiration", "cookieDuration"),
		TrackingDeepLink:  firstBool(m, "tracking_deeplink", "deeplinking", "deepLinking", "deeplink", "AllowsDeeplinking"),
		RawJSON:           mustJSON(m),
	}
}

func statInputFromMap(network string, m map[string]any) StatInput {
	return StatInput{
		Date:            firstString(m, "date", "day", "ActionDate", "eventDate", "postingDate", "transactionDate", "click.clickDate", "commission.commissionDate", "commission.updateDate", "created_at"),
		NetworkKey:      network,
		OfferID:         toInt64(firstAny(m, "offer_id")),
		LinkID:          toInt64(firstAny(m, "link_id")),
		Clicks:          toInt64(firstAny(m, "clicks", "Clicks")),
		Conversions:     toInt64(firstAny(m, "conversions", "sales", "transactions", "Actions")),
		RevenueCents:    centsFromAny(firstAny(m, "revenue_cents", "revenue", "order_value", "orderValue", "saleAmountUsd", "SaleAmount", "commission.orderValue")),
		CommissionCents: centsFromAny(firstAny(m, "commission_cents", "commission", "payout", "Payout", "pubCommissionAmountUsd", "publisherNetRevenue", "commission.publisherNetRevenue")),
		Currency:        firstNonEmpty(firstString(m, "currency", "Currency"), "USD"),
		RawJSON:         mustJSON(m),
	}
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
	return firstString(m, "external_id", "id", "slug", "key", "asin", "offer_id", "offerId", "CampaignId", "campaignId", "advertiserId", "merchantGroupId", "company_key", "date", "day", "ActionDate", "eventDate", "transactionDate") != ""
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
		for _, item := range x {
			if s := findURL(item); s != "" {
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
			return int64(f * 100)
		}
		return toInt64(s)
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
