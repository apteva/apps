// Apteva SEO app — generic SEO research workbench.
//
// v0.1 surface:
//   - domains: hostname identity, project-scoped, dedup'd via UNIQUE
//     (project_id, host) where host is normalised (lowercase, no
//     scheme, no leading 'www.', no path).
//   - keywords: (text, country_iso, language_iso) identity, also
//     project-scoped. Text is normalised (trimmed, lowercased).
//
// v0.3 adds locale-aware tracking and UI/HTTP-driven refreshes; scheduled
// refresh remains a future jobs-app extension.
//
// project_id comes from APTEVA_PROJECT_ID at runtime; ” = global
// scope. Children (pages, *_metrics, rankings, backlinks) inherit
// scope via FK rather than carrying their own column.
package main

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: seo
display_name: SEO
version: 0.4.3
description: Generic SEO research workbench — locale-aware domains, keywords, rankings, backlinks behind one pluggable provider integration.
author: Apteva
scopes: [project, global]
requires:
  permissions: [db.write.app, net.egress, platform.connections.execute]
  integrations:
    - role: seo_data_provider
      kind: integration
      compatible_slugs: [dataforseo, ahrefs, moz]
      capabilities: []
      required: false
      label: "SEO data provider (optional)"
      hint: "Bind DataForSEO/Ahrefs/Moz to populate metrics & backlinks."
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - { name: search_engines_list, description: "List supported search engines and generic SEO capabilities. Google is the default." }
    - { name: entities_add, description: "Add a tracked search entity. Args: search_engine? (google default, youtube), entity_type, identifier, label?, url?, location_id? or country_iso+language_code?." }
    - { name: entities_list, description: "List tracked search entities. Args: search_engine?, entity_type?, limit?." }
    - { name: entities_get, description: "Read one tracked search entity. Args: id." }
    - { name: entities_remove, description: "Remove one tracked search entity. Args: id." }
    - { name: serp_search, description: "Run a paid provider SERP search and cache ranked results. Args: search_engine? (google default or youtube), keyword or keyword_id, location_id or country_iso+language_code, depth?." }
    - { name: keyword_ideas, description: "Find keyword/content ideas. Args: search_engine? (google default or youtube), seed_keywords or keywords, location_id or country_iso+language_code, limit?, refresh?." }
    - { name: rankings_for_entity, description: "List cached ranking rows for a generic search entity. Args: entity_id, since?, limit?." }
    - { name: content_opportunities, description: "Summarize cached SERP snapshots into content opportunities. Args: search_engine? (youtube default), limit?." }
    - { name: locations_list, description: "List active SEO provider locations." }
    - { name: domains_add,    description: "Add a domain (hostname) to track; accepts location_id or country_iso+language_code for the default locale." }
    - { name: domains_list,   description: "List tracked domains in this scope." }
    - { name: domains_get,    description: "Read one domain plus latest metrics." }
    - { name: domains_remove, description: "Remove a domain (cascades to children)." }
    - { name: keywords_add,    description: "Add a keyword to track; requires location_id or country_iso+language_code." }
    - { name: keywords_list,   description: "List keywords in this scope. Args: search_engine?, country_iso?, limit?." }
    - { name: keywords_get,    description: "Read one keyword plus latest metrics." }
    - { name: keywords_remove, description: "Remove a keyword (cascades to children)." }
    - { name: rankings_for_domain,    description: "Cached current rankings for a domain; pass history=true for daily observations." }
    - { name: rankings_for_keyword,   description: "Cached current rankings for a keyword; pass history=true for daily observations." }
    - { name: backlinks_list,         description: "Cached backlinks pointing at a domain." }
    - { name: keyword_volume_history, description: "Monthly search-volume series (cached)." }
  ui_panels:
    - slot: project.page
      label: SEO
      icon: trending-up
      entry: /ui/SeoPanel.mjs
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/seo
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/seo.db
  migrations: migrations/
upgrade_policy: manual
`

// providerRole is the role name in requires.integrations. Refresh
// dispatch reads ctx.IntegrationFor(providerRole) at call time so a
// connection bound after boot is honoured without a restart.
const providerRole = "seo_data_provider"

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
		return errors.New("seo requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("seo mounted")
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

// ─── HTTP routes (refresh lives here, NOT in MCPTools) ───────────
//
// Refresh costs money — it calls the bound provider (DataForSEO etc.)
// which bills per request. Keeping it off the MCP surface means the
// agent can never trigger a paid action; only the human can, via the
// SeoPanel button or curl. The agent reads cached rows via the
// MCP read-only tools and surfaces last_refreshed_at as a staleness
// signal in its answers.

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Pattern: "/domains/", Handler: a.handleDomainsItem},
		{Pattern: "/keywords/", Handler: a.handleKeywordsItem},
		{Pattern: "/locations", Handler: a.handleLocationsList},
		{Pattern: "/locations/sync", Handler: a.handleLocationsSync},
		{Pattern: "/tools/call", Handler: a.handleToolsCall},
	}
}

// ─── MCP tools ───────────────────────────────────────────────────

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{Name: "search_engines_list",
			Description: "List supported search engines and their generic SEO capabilities. Args: none. Google is the default search_engine.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Handler:     a.toolSearchEnginesList},
		{Name: "entities_add",
			Description: "Add a tracked entity. Args: search_engine? (google default, youtube), entity_type (domain/page/channel/video), identifier (domain, URL, channel id/handle, or video id), label?, url?, location_id? or country_iso+language_code?.",
			InputSchema: schemaObject(map[string]any{
				"search_engine": map[string]any{"type": "string"},
				"entity_type":   map[string]any{"type": "string"},
				"identifier":    map[string]any{"type": "string"},
				"label":         map[string]any{"type": "string"},
				"url":           map[string]any{"type": "string"},
				"location_id":   map[string]any{"type": "integer"},
				"country_iso":   map[string]any{"type": "string"},
				"language_code": map[string]any{"type": "string"},
			}, []string{"entity_type", "identifier"}),
			Handler: a.toolEntitiesAdd},
		{Name: "entities_list",
			Description: "List tracked entities. Args: search_engine? (omitted means all), entity_type?, limit?.",
			InputSchema: schemaObject(map[string]any{
				"search_engine": map[string]any{"type": "string"},
				"entity_type":   map[string]any{"type": "string"},
				"limit":         map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolEntitiesList},
		{Name: "entities_get",
			Description: "Read one tracked entity. Args: id.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolEntitiesGet},
		{Name: "entities_remove",
			Description: "Remove one tracked entity. Args: id.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolEntitiesRemove},
		{Name: "serp_search",
			Description: "Run a paid provider SERP search and cache ranked results. Args: search_engine? (google default or youtube), keyword or keyword_id, location_id or country_iso+language_code, depth?.",
			InputSchema: schemaObject(map[string]any{
				"search_engine": map[string]any{"type": "string"},
				"keyword":       map[string]any{"type": "string"},
				"keyword_id":    map[string]any{"type": "integer"},
				"location_id":   map[string]any{"type": "integer"},
				"country_iso":   map[string]any{"type": "string"},
				"language_code": map[string]any{"type": "string"},
				"depth":         map[string]any{"type": "integer"},
				"device":        map[string]any{"type": "string"},
			}, nil),
			Handler: a.toolSERPSearch},
		{Name: "keyword_ideas",
			Description: "Find keyword/content ideas. Google calls provider keyword ideas; YouTube derives ideas from cached or freshly fetched YouTube SERPs for seed_keywords. Args: search_engine? (google default or youtube), seed_keywords or keywords, location_id or country_iso+language_code, limit?, refresh?.",
			InputSchema: schemaObject(map[string]any{
				"search_engine": map[string]any{"type": "string"},
				"seed_keywords": map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"keywords":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"location_id":   map[string]any{"type": "integer"},
				"country_iso":   map[string]any{"type": "string"},
				"language_code": map[string]any{"type": "string"},
				"limit":         map[string]any{"type": "integer"},
				"refresh":       map[string]any{"type": "boolean"},
			}, nil),
			Handler: a.toolKeywordIdeas},
		{Name: "rankings_for_entity",
			Description: "List cached ranking rows for a generic entity. Args: entity_id, since?, limit?. For YouTube channel entities, matches cached video results by channel id/handle/title when available.",
			InputSchema: schemaObject(map[string]any{
				"entity_id": map[string]any{"type": "integer"},
				"since":     map[string]any{"type": "integer"},
				"limit":     map[string]any{"type": "integer"},
			}, []string{"entity_id"}),
			Handler: a.toolRankingsForEntity},
		{Name: "content_opportunities",
			Description: "Summarize cached SERP snapshots into content opportunities. Args: search_engine? (youtube default for this tool), limit?.",
			InputSchema: schemaObject(map[string]any{
				"search_engine": map[string]any{"type": "string"},
				"limit":         map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolContentOpportunities},
		{Name: "domains_add",
			Description: "Add a domain (hostname) to track. Host is normalised. Args: host (required), label?, location_id? or country_iso+language_code? to set a default locale.",
			InputSchema: schemaObject(map[string]any{
				"host":          map[string]any{"type": "string"},
				"label":         map[string]any{"type": "string"},
				"location_id":   map[string]any{"type": "integer"},
				"country_iso":   map[string]any{"type": "string"},
				"language_code": map[string]any{"type": "string"},
				"search_engine": map[string]any{"type": "string"},
			}, []string{"host"}),
			Handler: a.toolDomainsAdd},
		{Name: "domains_list",
			Description: "List tracked domains in this project scope.",
			InputSchema: schemaObject(map[string]any{}, nil),
			Handler:     a.toolDomainsList},
		{Name: "domains_get",
			Description: "Read one domain plus its latest metrics snapshot (across providers). Args: id.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolDomainsGet},
		{Name: "domains_remove",
			Description: "Remove a domain. Cascades to its pages, metrics, rankings, and backlinks. Args: id.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolDomainsRemove},

		{Name: "keywords_add",
			Description: "Add a keyword to track. Args: text (required), location_id or country_iso+language_code. No implicit default locale is applied.",
			InputSchema: schemaObject(map[string]any{
				"text":          map[string]any{"type": "string"},
				"location_id":   map[string]any{"type": "integer"},
				"country_iso":   map[string]any{"type": "string"},
				"language_iso":  map[string]any{"type": "string"},
				"language_code": map[string]any{"type": "string"},
				"search_engine": map[string]any{"type": "string"},
			}, []string{"text"}),
			Handler: a.toolKeywordsAdd},
		{Name: "keywords_list",
			Description: "List keywords in this project scope. Args: search_engine? (google, youtube), country_iso? (filter), limit? (default 200).",
			InputSchema: schemaObject(map[string]any{
				"search_engine": map[string]any{"type": "string"},
				"country_iso":   map[string]any{"type": "string"},
				"limit":         map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolKeywordsList},
		{Name: "keywords_get",
			Description: "Read one keyword plus its latest metrics snapshot (across providers). Args: id.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolKeywordsGet},
		{Name: "keywords_remove",
			Description: "Remove a keyword. Cascades to its metrics, volume history, and rankings. Args: id.",
			InputSchema: schemaObject(map[string]any{"id": map[string]any{"type": "integer"}}, []string{"id"}),
			Handler:     a.toolKeywordsRemove},

		// ── read-only views (v0.2) ──────────────────────────────
		{Name: "rankings_for_domain",
			Description: "List a domain's current rankings by default, or daily ranking observations with history=true. Args: domain_id (required), since? (unix seconds), limit? (default 200), history? (default false).",
			InputSchema: schemaObject(map[string]any{
				"domain_id": map[string]any{"type": "integer"},
				"since":     map[string]any{"type": "integer"},
				"limit":     map[string]any{"type": "integer"},
				"history":   map[string]any{"type": "boolean"},
			}, []string{"domain_id"}),
			Handler: a.toolRankingsForDomain},
		{Name: "rankings_for_keyword",
			Description: "List current tracked-domain rankings for a keyword by default, or daily observations with history=true. Args: keyword_id (required), since? (unix seconds), limit? (default 200), history? (default false).",
			InputSchema: schemaObject(map[string]any{
				"keyword_id": map[string]any{"type": "integer"},
				"since":      map[string]any{"type": "integer"},
				"limit":      map[string]any{"type": "integer"},
				"history":    map[string]any{"type": "boolean"},
			}, []string{"keyword_id"}),
			Handler: a.toolRankingsForKeyword},
		{Name: "backlinks_list",
			Description: "List backlinks pointing at a domain (cached). Args: domain_id (required), lost? (bool, default false), dofollow? (bool, optional filter), limit? (default 200).",
			InputSchema: schemaObject(map[string]any{
				"domain_id": map[string]any{"type": "integer"},
				"lost":      map[string]any{"type": "boolean"},
				"dofollow":  map[string]any{"type": "boolean"},
				"limit":     map[string]any{"type": "integer"},
			}, []string{"domain_id"}),
			Handler: a.toolBacklinksList},
		{Name: "keyword_volume_history",
			Description: "Monthly search-volume series for a keyword (cached). Args: keyword_id (required).",
			InputSchema: schemaObject(map[string]any{
				"keyword_id": map[string]any{"type": "integer"},
			}, []string{"keyword_id"}),
			Handler: a.toolKeywordVolumeHistory},
		{Name: "locations_list",
			Description: "List active SEO provider locations. Args: provider?, search_engine?, country_iso?, language_code?, q?, limit?. Sync locations from the UI/HTTP route first.",
			InputSchema: schemaObject(map[string]any{
				"provider":      map[string]any{"type": "string"},
				"search_engine": map[string]any{"type": "string"},
				"country_iso":   map[string]any{"type": "string"},
				"language_code": map[string]any{"type": "string"},
				"language_iso":  map[string]any{"type": "string"},
				"q":             map[string]any{"type": "string"},
				"limit":         map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolLocationsList},
	}
}

// ─── Models ──────────────────────────────────────────────────────

type Domain struct {
	ID                int64  `json:"id"`
	ProjectID         string `json:"project_id"`
	Host              string `json:"host"`
	Label             string `json:"label,omitempty"`
	DefaultLocationID *int64 `json:"default_location_id,omitempty"`
	CreatedAt         string `json:"created_at"`
}

type Keyword struct {
	ID           int64  `json:"id"`
	ProjectID    string `json:"project_id"`
	SearchEngine string `json:"search_engine"`
	Text         string `json:"text"`
	LocationID   int64  `json:"location_id"`
	CountryISO   string `json:"country_iso"`
	LanguageISO  string `json:"language_iso"`
	CreatedAt    string `json:"created_at"`
}

type SEOLocation struct {
	ID           int64   `json:"id"`
	Provider     string  `json:"provider"`
	SearchEngine string  `json:"search_engine"`
	LocationCode *int64  `json:"location_code,omitempty"`
	LocationName string  `json:"location_name"`
	CountryISO   *string `json:"country_iso,omitempty"`
	LanguageCode string  `json:"language_code"`
	LanguageName string  `json:"language_name,omitempty"`
	IsActive     int64   `json:"is_active"`
	SyncedAt     *int64  `json:"synced_at,omitempty"`
	CreatedAt    string  `json:"created_at"`
}

type DomainMetrics struct {
	ID                    int64    `json:"id"`
	DomainID              int64    `json:"domain_id"`
	LocationID            int64    `json:"location_id"`
	Provider              string   `json:"provider"`
	TS                    int64    `json:"ts"`
	CountryISO            *string  `json:"country_iso,omitempty"`
	AuthorityScore        *int64   `json:"authority_score,omitempty"`
	SpamScore             *float64 `json:"spam_score,omitempty"`
	OrganicTraffic        *int64   `json:"organic_traffic,omitempty"`
	OrganicKeywords       *int64   `json:"organic_keywords,omitempty"`
	PaidTraffic           *int64   `json:"paid_traffic,omitempty"`
	PaidKeywords          *int64   `json:"paid_keywords,omitempty"`
	BacklinksCount        *int64   `json:"backlinks_count,omitempty"`
	ReferringDomainsCount *int64   `json:"referring_domains_count,omitempty"`
}

type KeywordMetrics struct {
	ID           int64    `json:"id"`
	KeywordID    int64    `json:"keyword_id"`
	LocationID   int64    `json:"location_id"`
	Provider     string   `json:"provider"`
	TS           int64    `json:"ts"`
	Volume       *int64   `json:"volume,omitempty"`
	Difficulty   *int64   `json:"difficulty,omitempty"`
	CPCUSD       *float64 `json:"cpc_usd,omitempty"`
	Clicks       *int64   `json:"clicks,omitempty"`
	OrganicCTR   *float64 `json:"organic_ctr,omitempty"`
	IntentJSON   string   `json:"intent_json"`
	SerpFeatJSON string   `json:"serp_features_json"`
}

type SearchEngineDef struct {
	ID           string   `json:"id"`
	Label        string   `json:"label"`
	Default      bool     `json:"default,omitempty"`
	EntityTypes  []string `json:"entity_types"`
	Capabilities []string `json:"capabilities"`
}

type SearchEntity struct {
	ID                int64  `json:"id"`
	ProjectID         string `json:"project_id"`
	SearchEngine      string `json:"search_engine"`
	EntityType        string `json:"entity_type"`
	Identifier        string `json:"identifier"`
	Label             string `json:"label,omitempty"`
	URL               string `json:"url,omitempty"`
	DefaultLocationID *int64 `json:"default_location_id,omitempty"`
	CreatedAt         string `json:"created_at"`
	UpdatedAt         string `json:"updated_at"`
}

type SearchRanking struct {
	ID                int64  `json:"id"`
	SnapshotID        int64  `json:"snapshot_id"`
	EntityID          *int64 `json:"entity_id,omitempty"`
	SearchEngine      string `json:"search_engine"`
	KeywordID         *int64 `json:"keyword_id,omitempty"`
	KeywordText       string `json:"keyword_text"`
	LocationID        *int64 `json:"location_id,omitempty"`
	Provider          string `json:"provider"`
	TS                int64  `json:"ts"`
	Rank              *int64 `json:"rank,omitempty"`
	ResultType        string `json:"result_type,omitempty"`
	Title             string `json:"title,omitempty"`
	URL               string `json:"url,omitempty"`
	Identifier        string `json:"identifier,omitempty"`
	ChannelIdentifier string `json:"channel_identifier,omitempty"`
	ChannelTitle      string `json:"channel_title,omitempty"`
	Snippet           string `json:"snippet,omitempty"`
	PublishedAt       string `json:"published_at,omitempty"`
}

type keywordIdea struct {
	Keyword          string   `json:"keyword"`
	SourceKeyword    string   `json:"source_keyword"`
	OpportunityScore int64    `json:"opportunity_score"`
	Reason           string   `json:"reason"`
	ExampleTitles    []string `json:"example_titles"`
	TopChannels      []string `json:"top_channels"`
}

// ─── Scope ───────────────────────────────────────────────────────

func projectScope(ctxs ...*sdk.AppCtx) string {
	return strings.TrimSpace(os.Getenv("APTEVA_PROJECT_ID")) // '' = global
}

func projectScopeFromArgs(ctx *sdk.AppCtx, args map[string]any) string {
	if args != nil {
		if pid := strings.TrimSpace(strArg(args, "_project_id", "")); pid != "" {
			return pid
		}
	}
	return projectScope(ctx)
}

// ─── Normalisation ───────────────────────────────────────────────

// normaliseHost takes user input (may include scheme, www., path,
// trailing slash, mixed case) and returns the canonical hostname.
// "https://Www.Nike.com/running-shoes/" → "nike.com"
// "blog.nike.com" → "blog.nike.com"
// Returns "" if the input doesn't yield a usable host.
func normaliseHost(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	// Add scheme so url.Parse extracts host even when user typed bare host.
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" {
		return ""
	}
	host := strings.ToLower(u.Host)
	// Drop port.
	if i := strings.IndexByte(host, ':'); i >= 0 {
		host = host[:i]
	}
	// Drop leading 'www.' so 'www.nike.com' and 'nike.com' collapse to one row.
	host = strings.TrimPrefix(host, "www.")
	return host
}

// normaliseKeyword trims whitespace and lowercases.
func normaliseKeyword(raw string) string {
	return strings.ToLower(strings.TrimSpace(raw))
}

// ─── DB helpers ──────────────────────────────────────────────────

func scanLocation(s interface{ Scan(...any) error }) (*SEOLocation, error) {
	var l SEOLocation
	var code sql.NullInt64
	var country sql.NullString
	var synced sql.NullInt64
	err := s.Scan(&l.ID, &l.Provider, &l.SearchEngine, &code, &l.LocationName,
		&country, &l.LanguageCode, &l.LanguageName, &l.IsActive, &synced, &l.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	if code.Valid {
		l.LocationCode = &code.Int64
	}
	if country.Valid {
		l.CountryISO = &country.String
	}
	if synced.Valid {
		l.SyncedAt = &synced.Int64
	}
	return &l, nil
}

const locationSelectCols = `id, provider, search_engine, location_code, location_name,
       country_iso, language_code, language_name, is_active, synced_at, created_at`

func getLocation(db *sql.DB, id int64) (*SEOLocation, error) {
	if id == 0 {
		return nil, errors.New("location_id required")
	}
	l, err := scanLocation(db.QueryRow(
		`SELECT `+locationSelectCols+` FROM seo_locations WHERE id = ?`, id))
	if err != nil {
		return nil, err
	}
	if l == nil {
		return nil, fmt.Errorf("location %d not found; sync provider locations first", id)
	}
	if l.IsActive == 0 {
		return nil, fmt.Errorf("location %d is inactive", id)
	}
	return l, nil
}

func resolveLocationFromArgs(db *sql.DB, args map[string]any, defaultID *int64) (*SEOLocation, error) {
	if id := toInt64(args["location_id"]); id != 0 {
		return getLocation(db, id)
	}
	if defaultID != nil && *defaultID != 0 {
		return getLocation(db, *defaultID)
	}
	provider := strings.ToLower(strings.TrimSpace(strArg(args, "provider", "dataforseo")))
	searchEngine := strings.ToLower(strings.TrimSpace(strArg(args, "search_engine", "google")))
	country := strings.ToUpper(strings.TrimSpace(strArg(args, "country_iso", "")))
	lang := strings.ToLower(strings.TrimSpace(strArg(args, "language_code", strArg(args, "language_iso", ""))))
	if country == "" || lang == "" {
		return nil, errors.New("location_id or country_iso + language_code are required; sync provider locations first")
	}
	l, err := scanLocation(db.QueryRow(
		`SELECT `+locationSelectCols+`
		   FROM seo_locations
		  WHERE provider = ? AND search_engine = ? AND country_iso = ?
		    AND language_code = ? AND is_active = 1
		  ORDER BY CASE WHEN location_name = country_iso THEN 0 ELSE 1 END, location_name
		  LIMIT 1`,
		provider, searchEngine, country, lang))
	if err != nil {
		return nil, err
	}
	if l == nil {
		return nil, fmt.Errorf("no active %s/%s location for %s/%s; sync locations before creating or refreshing SEO data", provider, searchEngine, country, lang)
	}
	return l, nil
}

func listLocations(db *sql.DB, args map[string]any) ([]SEOLocation, error) {
	provider := strings.ToLower(strings.TrimSpace(strArg(args, "provider", "")))
	searchEngine := strings.ToLower(strings.TrimSpace(strArg(args, "search_engine", "")))
	country := strings.ToUpper(strings.TrimSpace(strArg(args, "country_iso", "")))
	lang := strings.ToLower(strings.TrimSpace(strArg(args, "language_code", strArg(args, "language_iso", ""))))
	q := strings.ToLower(strings.TrimSpace(strArg(args, "q", "")))
	limit := int(toInt64(args["limit"]))
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	sqlText := `SELECT ` + locationSelectCols + ` FROM seo_locations WHERE is_active = 1`
	qargs := []any{}
	if provider != "" {
		sqlText += ` AND provider = ?`
		qargs = append(qargs, provider)
	}
	if searchEngine != "" {
		sqlText += ` AND search_engine = ?`
		qargs = append(qargs, searchEngine)
	}
	if country != "" {
		sqlText += ` AND country_iso = ?`
		qargs = append(qargs, country)
	}
	if lang != "" {
		sqlText += ` AND language_code = ?`
		qargs = append(qargs, lang)
	}
	if q != "" {
		sqlText += ` AND (lower(location_name) LIKE ? OR lower(language_name) LIKE ? OR lower(language_code) LIKE ?)`
		like := "%" + q + "%"
		qargs = append(qargs, like, like, like)
	}
	sqlText += ` ORDER BY provider, search_engine, location_name, language_code LIMIT ?`
	qargs = append(qargs, limit)
	rows, err := db.Query(sqlText, qargs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []SEOLocation{}
	for rows.Next() {
		l, err := scanLocation(rows)
		if err != nil {
			return nil, err
		}
		if l != nil {
			out = append(out, *l)
		}
	}
	return out, rows.Err()
}

func getDomain(db *sql.DB, pid string, id int64) (*Domain, error) {
	var d Domain
	var defaultLoc sql.NullInt64
	err := db.QueryRow(
		`SELECT id, project_id, host, label, default_location_id, created_at
		   FROM domains WHERE id = ? AND project_id = ?`, id, pid,
	).Scan(&d.ID, &d.ProjectID, &d.Host, &d.Label, &defaultLoc, &d.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("domain %d not found", id)
	}
	if err != nil {
		return nil, err
	}
	if defaultLoc.Valid {
		d.DefaultLocationID = &defaultLoc.Int64
	}
	return &d, nil
}

func listDomains(db *sql.DB, pid string) ([]Domain, error) {
	rows, err := db.Query(
		`SELECT id, project_id, host, label, default_location_id, created_at
		   FROM domains WHERE project_id = ? ORDER BY host`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Domain{}
	for rows.Next() {
		var d Domain
		var defaultLoc sql.NullInt64
		if err := rows.Scan(&d.ID, &d.ProjectID, &d.Host, &d.Label, &defaultLoc, &d.CreatedAt); err != nil {
			return nil, err
		}
		if defaultLoc.Valid {
			d.DefaultLocationID = &defaultLoc.Int64
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// latestDomainMetrics returns the most recent domain_metrics row across
// any provider for the given domain. Nil + no error if there are none.
func latestDomainMetrics(db *sql.DB, domainID int64) (*DomainMetrics, error) {
	row := db.QueryRow(
		`SELECT id, domain_id, location_id, provider, ts, country_iso,
		        authority_score, spam_score, organic_traffic,
		        organic_keywords, paid_traffic, paid_keywords,
		        backlinks_count, referring_domains_count
		   FROM domain_metrics WHERE domain_id = ?
		   ORDER BY ts DESC LIMIT 1`, domainID)
	var m DomainMetrics
	err := row.Scan(&m.ID, &m.DomainID, &m.LocationID, &m.Provider, &m.TS, &m.CountryISO,
		&m.AuthorityScore, &m.SpamScore, &m.OrganicTraffic,
		&m.OrganicKeywords, &m.PaidTraffic, &m.PaidKeywords,
		&m.BacklinksCount, &m.ReferringDomainsCount)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

func getKeyword(db *sql.DB, pid string, id int64) (*Keyword, error) {
	var k Keyword
	err := db.QueryRow(
		`SELECT k.id, k.project_id, COALESCE(l.search_engine, 'google') AS search_engine,
		        k.text, k.location_id, k.country_iso, k.language_iso, k.created_at
		   FROM keywords k
		   LEFT JOIN seo_locations l ON l.id = k.location_id
		  WHERE k.id = ? AND k.project_id = ?`, id, pid,
	).Scan(&k.ID, &k.ProjectID, &k.SearchEngine, &k.Text, &k.LocationID, &k.CountryISO, &k.LanguageISO, &k.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("keyword %d not found", id)
	}
	if err != nil {
		return nil, err
	}
	return &k, nil
}

func listKeywords(db *sql.DB, pid, countryISO string, limit int) ([]Keyword, error) {
	return listKeywordsWithSearchEngine(db, pid, "", countryISO, limit)
}

func listKeywordsWithSearchEngine(db *sql.DB, pid, searchEngine, countryISO string, limit int) ([]Keyword, error) {
	if limit <= 0 {
		limit = 200
	}
	sqlText := `SELECT k.id, k.project_id, COALESCE(l.search_engine, 'google') AS search_engine,
	                   k.text, k.location_id, k.country_iso, k.language_iso, k.created_at
	              FROM keywords k
	              LEFT JOIN seo_locations l ON l.id = k.location_id
	             WHERE k.project_id = ?`
	qargs := []any{pid}
	if searchEngine != "" {
		sqlText += ` AND COALESCE(l.search_engine, 'google') = ?`
		qargs = append(qargs, searchEngine)
	}
	if countryISO != "" {
		sqlText += ` AND k.country_iso = ?`
		qargs = append(qargs, strings.ToUpper(countryISO))
	}
	sqlText += ` ORDER BY k.text LIMIT ?`
	qargs = append(qargs, limit)
	rows, err := db.Query(sqlText, qargs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Keyword{}
	for rows.Next() {
		var k Keyword
		if err := rows.Scan(&k.ID, &k.ProjectID, &k.SearchEngine, &k.Text, &k.LocationID, &k.CountryISO, &k.LanguageISO, &k.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func latestKeywordMetrics(db *sql.DB, keywordID int64) (*KeywordMetrics, error) {
	row := db.QueryRow(
		`SELECT id, keyword_id, location_id, provider, ts, volume, difficulty,
		        cpc_usd, clicks, organic_ctr, intent_json, serp_features_json
		   FROM keyword_metrics WHERE keyword_id = ?
		   ORDER BY ts DESC LIMIT 1`, keywordID)
	var m KeywordMetrics
	err := row.Scan(&m.ID, &m.KeywordID, &m.LocationID, &m.Provider, &m.TS,
		&m.Volume, &m.Difficulty, &m.CPCUSD, &m.Clicks, &m.OrganicCTR,
		&m.IntentJSON, &m.SerpFeatJSON)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	return &m, nil
}

// ─── Tool handlers ───────────────────────────────────────────────

func (a *App) toolDomainsAdd(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	host := normaliseHost(strArg(args, "host", ""))
	if host == "" {
		return nil, errors.New("host required (e.g. 'nike.com' or 'https://www.nike.com')")
	}
	label := strings.TrimSpace(strArg(args, "label", ""))
	pid := projectScopeFromArgs(ctx, args)
	db := ctx.AppDB()
	var locID any
	if hasLocationArgs(args) {
		loc, err := resolveLocationFromArgs(db, args, nil)
		if err != nil {
			return nil, err
		}
		locID = loc.ID
	}
	res, err := db.Exec(
		`INSERT INTO domains (project_id, host, label, default_location_id) VALUES (?, ?, ?, ?)
		   ON CONFLICT(project_id, host) DO UPDATE SET
		     label = CASE WHEN excluded.label != '' THEN excluded.label ELSE domains.label END,
		     default_location_id = COALESCE(excluded.default_location_id, domains.default_location_id)`,
		pid, host, label, locID)
	if err != nil {
		return nil, fmt.Errorf("insert domain: %w", err)
	}
	id, _ := res.LastInsertId()
	if id == 0 {
		// ON CONFLICT path: look up the existing row.
		row := db.QueryRow(`SELECT id FROM domains WHERE project_id = ? AND host = ?`, pid, host)
		_ = row.Scan(&id)
	}
	return getDomain(db, pid, id)
}

func (a *App) toolDomainsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return listDomains(ctx.AppDB(), projectScopeFromArgs(ctx, args))
}

func (a *App) toolDomainsGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := toInt64(args["id"])
	if id == 0 {
		return nil, errors.New("id required")
	}
	d, err := getDomain(ctx.AppDB(), projectScopeFromArgs(ctx, args), id)
	if err != nil {
		return nil, err
	}
	m, err := latestDomainMetrics(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"domain": d, "metrics": m}, nil
}

func (a *App) toolDomainsRemove(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := toInt64(args["id"])
	if id == 0 {
		return nil, errors.New("id required")
	}
	pid := projectScopeFromArgs(ctx, args)
	if _, err := getDomain(ctx.AppDB(), pid, id); err != nil {
		return nil, err
	}
	if _, err := ctx.AppDB().Exec(`DELETE FROM domains WHERE id = ? AND project_id = ?`, id, pid); err != nil {
		return nil, err
	}
	return map[string]any{"removed": id}, nil
}

func (a *App) toolKeywordsAdd(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	text := normaliseKeyword(strArg(args, "text", ""))
	if text == "" {
		return nil, errors.New("text required")
	}
	pid := projectScopeFromArgs(ctx, args)
	db := ctx.AppDB()
	loc, err := resolveLocationFromArgs(db, args, nil)
	if err != nil {
		return nil, err
	}
	country := ""
	if loc.CountryISO != nil {
		country = strings.ToUpper(*loc.CountryISO)
	}
	if country == "" {
		return nil, fmt.Errorf("location %d has no country_iso; choose a country-scoped location for keyword metrics", loc.ID)
	}
	lang := strings.ToLower(loc.LanguageCode)
	res, err := db.Exec(
		`INSERT INTO keywords (project_id, text, location_id, country_iso, language_iso)
		   VALUES (?, ?, ?, ?, ?)
		   ON CONFLICT(project_id, text, location_id) DO NOTHING`,
		pid, text, loc.ID, country, lang)
	if err != nil {
		return nil, fmt.Errorf("insert keyword: %w", err)
	}
	id, _ := res.LastInsertId()
	if id == 0 {
		row := db.QueryRow(
			`SELECT id FROM keywords
			   WHERE project_id = ? AND text = ? AND location_id = ?`,
			pid, text, loc.ID)
		_ = row.Scan(&id)
	}
	return getKeyword(db, pid, id)
}

func (a *App) toolKeywordsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	limit := int(toInt64(args["limit"]))
	searchEngine := ""
	if raw := strings.TrimSpace(strArg(args, "search_engine", "")); raw != "" {
		var err error
		searchEngine, err = normalizeSearchEngine(raw)
		if err != nil {
			return nil, err
		}
	}
	country := strArg(args, "country_iso", "")
	return listKeywordsWithSearchEngine(ctx.AppDB(), projectScopeFromArgs(ctx, args), searchEngine, country, limit)
}

func (a *App) toolKeywordsGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := toInt64(args["id"])
	if id == 0 {
		return nil, errors.New("id required")
	}
	k, err := getKeyword(ctx.AppDB(), projectScopeFromArgs(ctx, args), id)
	if err != nil {
		return nil, err
	}
	m, err := latestKeywordMetrics(ctx.AppDB(), id)
	if err != nil {
		return nil, err
	}
	return map[string]any{"keyword": k, "metrics": m}, nil
}

func (a *App) toolKeywordsRemove(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := toInt64(args["id"])
	if id == 0 {
		return nil, errors.New("id required")
	}
	pid := projectScopeFromArgs(ctx, args)
	if _, err := getKeyword(ctx.AppDB(), pid, id); err != nil {
		return nil, err
	}
	if _, err := ctx.AppDB().Exec(`DELETE FROM keywords WHERE id = ? AND project_id = ?`, id, pid); err != nil {
		return nil, err
	}
	return map[string]any{"removed": id}, nil
}

// ─── Read-only tool handlers (v0.2) ─────────────────────────────

type Ranking struct {
	ID               int64  `json:"id"`
	DomainID         int64  `json:"domain_id"`
	KeywordID        int64  `json:"keyword_id"`
	LocationID       int64  `json:"location_id"`
	Provider         string `json:"provider"`
	TS               int64  `json:"ts"`
	ObservedDate     string `json:"observed_date,omitempty"`
	Rank             *int64 `json:"rank,omitempty"`
	RankURL          string `json:"rank_url,omitempty"`
	Device           string `json:"device"`
	SerpFeaturesJSON string `json:"serp_features_json"`
}

type Backlink struct {
	ID              int64  `json:"id"`
	DomainID        int64  `json:"domain_id"`
	Provider        string `json:"provider"`
	SourceURL       string `json:"source_url"`
	DestURL         string `json:"dest_url"`
	Anchor          string `json:"anchor"`
	IsDofollow      *int64 `json:"is_dofollow,omitempty"`
	IsNofollow      *int64 `json:"is_nofollow,omitempty"`
	IsUGC           *int64 `json:"is_ugc,omitempty"`
	IsSponsored     *int64 `json:"is_sponsored,omitempty"`
	SourceAuthority *int64 `json:"source_authority,omitempty"`
	FirstSeen       *int64 `json:"first_seen,omitempty"`
	LastSeen        *int64 `json:"last_seen,omitempty"`
	IsLost          int64  `json:"is_lost"`
}

type VolumeHistoryRow struct {
	Provider   string `json:"provider"`
	LocationID int64  `json:"location_id"`
	Year       int    `json:"year"`
	Month      int    `json:"month"`
	Volume     int64  `json:"volume"`
}

func (a *App) toolRankingsForDomain(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := toInt64(args["domain_id"])
	if id == 0 {
		return nil, errors.New("domain_id required")
	}
	if _, err := getDomain(ctx.AppDB(), projectScopeFromArgs(ctx, args), id); err != nil {
		return nil, err
	}
	since := toInt64(args["since"])
	limit := int(toInt64(args["limit"]))
	if limit <= 0 {
		limit = 200
	}
	var rows *sql.Rows
	var err error
	if boolArg(args, "history", false) {
		rows, err = ctx.AppDB().Query(
			`SELECT id, domain_id, keyword_id, location_id, provider, ts, observed_date,
			        rank, rank_url, device, serp_features_json
			   FROM rankings
			   WHERE domain_id = ? AND ts >= ?
			   ORDER BY ts DESC, rank ASC LIMIT ?`, id, since, limit)
	} else {
		rows, err = ctx.AppDB().Query(
			`WITH current_rankings AS (
		    SELECT id, domain_id, keyword_id, location_id, provider, ts, observed_date, rank, rank_url,
		           device, serp_features_json,
		           ROW_NUMBER() OVER (
		             PARTITION BY domain_id, keyword_id, location_id, provider, rank_url, device
		             ORDER BY ts DESC, id DESC
		           ) AS rn
		      FROM rankings
		     WHERE domain_id = ? AND ts >= ?
		  )
		  SELECT id, domain_id, keyword_id, location_id, provider, ts, observed_date, rank, rank_url,
		         device, serp_features_json
		    FROM current_rankings
		   WHERE rn = 1
		   ORDER BY ts DESC, rank ASC LIMIT ?`, id, since, limit)
	}
	if err != nil {
		return nil, err
	}
	return scanRankings(rows)
}

func (a *App) toolRankingsForKeyword(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := toInt64(args["keyword_id"])
	if id == 0 {
		return nil, errors.New("keyword_id required")
	}
	if _, err := getKeyword(ctx.AppDB(), projectScopeFromArgs(ctx, args), id); err != nil {
		return nil, err
	}
	since := toInt64(args["since"])
	limit := int(toInt64(args["limit"]))
	if limit <= 0 {
		limit = 200
	}
	var rows *sql.Rows
	var err error
	if boolArg(args, "history", false) {
		rows, err = ctx.AppDB().Query(
			`SELECT id, domain_id, keyword_id, location_id, provider, ts, observed_date,
			        rank, rank_url, device, serp_features_json
			   FROM rankings
			   WHERE keyword_id = ? AND ts >= ?
			   ORDER BY ts DESC, rank ASC LIMIT ?`, id, since, limit)
	} else {
		rows, err = ctx.AppDB().Query(
			`WITH current_rankings AS (
		    SELECT id, domain_id, keyword_id, location_id, provider, ts, observed_date, rank, rank_url,
		           device, serp_features_json,
		           ROW_NUMBER() OVER (
		             PARTITION BY domain_id, keyword_id, location_id, provider, rank_url, device
		             ORDER BY ts DESC, id DESC
		           ) AS rn
		      FROM rankings
		     WHERE keyword_id = ? AND ts >= ?
		  )
		  SELECT id, domain_id, keyword_id, location_id, provider, ts, observed_date, rank, rank_url,
		         device, serp_features_json
		    FROM current_rankings
		   WHERE rn = 1
		   ORDER BY ts DESC, rank ASC LIMIT ?`, id, since, limit)
	}
	if err != nil {
		return nil, err
	}
	return scanRankings(rows)
}

func scanRankings(rows *sql.Rows) ([]Ranking, error) {
	defer rows.Close()
	out := []Ranking{}
	for rows.Next() {
		var r Ranking
		var observed sql.NullString
		if err := rows.Scan(&r.ID, &r.DomainID, &r.KeywordID, &r.LocationID, &r.Provider, &r.TS,
			&observed, &r.Rank, &r.RankURL, &r.Device, &r.SerpFeaturesJSON); err != nil {
			return nil, err
		}
		if observed.Valid {
			r.ObservedDate = observed.String
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (a *App) toolBacklinksList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := toInt64(args["domain_id"])
	if id == 0 {
		return nil, errors.New("domain_id required")
	}
	if _, err := getDomain(ctx.AppDB(), projectScopeFromArgs(ctx, args), id); err != nil {
		return nil, err
	}
	limit := int(toInt64(args["limit"]))
	if limit <= 0 {
		limit = 200
	}
	// `lost` defaults to false: callers only see live links unless
	// they ask for the lost set. dofollow nil → no filter.
	wantLost := boolArg(args, "lost", false)
	q := `SELECT id, domain_id, provider, source_url, dest_url, anchor,
	             is_dofollow, is_nofollow, is_ugc, is_sponsored,
	             source_authority, first_seen, last_seen, is_lost
	        FROM backlinks
	       WHERE domain_id = ? AND is_lost = ?`
	qargs := []any{id, boolToInt(wantLost)}
	if v, ok := args["dofollow"].(bool); ok {
		q += ` AND is_dofollow = ?`
		qargs = append(qargs, boolToInt(v))
	}
	q += ` ORDER BY last_seen DESC LIMIT ?`
	qargs = append(qargs, limit)
	rows, err := ctx.AppDB().Query(q, qargs...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Backlink{}
	for rows.Next() {
		var b Backlink
		if err := rows.Scan(&b.ID, &b.DomainID, &b.Provider, &b.SourceURL, &b.DestURL, &b.Anchor,
			&b.IsDofollow, &b.IsNofollow, &b.IsUGC, &b.IsSponsored,
			&b.SourceAuthority, &b.FirstSeen, &b.LastSeen, &b.IsLost); err != nil {
			return nil, err
		}
		out = append(out, b)
	}
	return out, rows.Err()
}

func (a *App) toolKeywordVolumeHistory(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := toInt64(args["keyword_id"])
	if id == 0 {
		return nil, errors.New("keyword_id required")
	}
	if _, err := getKeyword(ctx.AppDB(), projectScopeFromArgs(ctx, args), id); err != nil {
		return nil, err
	}
	rows, err := ctx.AppDB().Query(
		`SELECT provider, location_id, year, month, volume
		   FROM keyword_volume_history
		   WHERE keyword_id = ?
		   ORDER BY year DESC, month DESC`, id)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []VolumeHistoryRow{}
	for rows.Next() {
		var v VolumeHistoryRow
		if err := rows.Scan(&v.Provider, &v.LocationID, &v.Year, &v.Month, &v.Volume); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
}

func (a *App) toolLocationsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return listLocations(ctx.AppDB(), args)
}

func (a *App) toolSearchEnginesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return map[string]any{
		"default":        "google",
		"search_engines": searchEngineDefs(),
	}, nil
}

func searchEngineDefs() []SearchEngineDef {
	return []SearchEngineDef{
		{
			ID:          "google",
			Label:       "Google",
			Default:     true,
			EntityTypes: []string{"domain", "page"},
			Capabilities: []string{
				"locations", "languages", "keyword_metrics", "keyword_ideas",
				"serp_search", "rankings", "backlinks", "trends",
			},
		},
		{
			ID:          "youtube",
			Label:       "YouTube",
			EntityTypes: []string{"channel", "video"},
			Capabilities: []string{
				"locations", "languages", "serp_search", "entity_info",
				"transcripts", "comments", "rankings", "content_opportunities",
			},
		},
	}
}

func normalizeSearchEngine(raw string) (string, error) {
	p := strings.ToLower(strings.TrimSpace(raw))
	if p == "" {
		return "google", nil
	}
	switch p {
	case "google", "youtube":
		return p, nil
	case "yt":
		return "youtube", nil
	default:
		return "", fmt.Errorf("unsupported search_engine %q; supported search engines: google, youtube", raw)
	}
}

func search_engineFromArg(raw string) string {
	p, err := normalizeSearchEngine(raw)
	if err != nil {
		return strings.ToLower(strings.TrimSpace(raw))
	}
	return p
}

func normalizeEntityInput(search_engine, entityType, identifier, rawURL string) (etype string, ident string, urlText string, err error) {
	etype = strings.ToLower(strings.TrimSpace(entityType))
	ident = strings.TrimSpace(identifier)
	urlText = strings.TrimSpace(rawURL)
	if ident == "" {
		return "", "", "", errors.New("identifier required")
	}
	switch search_engine {
	case "google":
		switch etype {
		case "domain":
			ident = normaliseHost(ident)
			if ident == "" {
				return "", "", "", errors.New("domain identifier must be a hostname or URL")
			}
			if urlText == "" {
				urlText = "https://" + ident
			}
		case "page":
			u := strings.TrimSpace(ident)
			if !strings.Contains(u, "://") {
				u = "https://" + u
			}
			parsed, parseErr := url.Parse(u)
			if parseErr != nil || parsed.Host == "" {
				return "", "", "", errors.New("page identifier must be a URL")
			}
			ident = parsed.String()
			if urlText == "" {
				urlText = ident
			}
		default:
			return "", "", "", fmt.Errorf("google entity_type must be domain or page, got %q", entityType)
		}
	case "youtube":
		switch etype {
		case "channel":
			ident = strings.TrimSpace(ident)
		case "video":
			ident = youtubeVideoID(ident)
			if ident == "" {
				return "", "", "", errors.New("video identifier must be a YouTube video id or URL")
			}
			if urlText == "" {
				urlText = "https://www.youtube.com/watch?v=" + ident
			}
		default:
			return "", "", "", fmt.Errorf("youtube entity_type must be channel or video, got %q", entityType)
		}
	default:
		return "", "", "", fmt.Errorf("unsupported search_engine %q", search_engine)
	}
	return etype, ident, urlText, nil
}

func youtubeVideoID(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return ""
	}
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		if err == nil {
			if v := u.Query().Get("v"); v != "" {
				return v
			}
			parts := strings.Split(strings.Trim(u.Path, "/"), "/")
			if len(parts) > 0 {
				return parts[len(parts)-1]
			}
		}
	}
	return s
}

func (a *App) toolEntitiesAdd(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	search_engine, err := normalizeSearchEngine(strArg(args, "search_engine", "google"))
	if err != nil {
		return nil, err
	}
	etype, ident, urlText, err := normalizeEntityInput(search_engine, strArg(args, "entity_type", ""), strArg(args, "identifier", ""), strArg(args, "url", ""))
	if err != nil {
		return nil, err
	}
	label := strings.TrimSpace(strArg(args, "label", ""))
	pid := projectScopeFromArgs(ctx, args)
	var locID any
	if hasLocationArgs(args) {
		locArgs := copyArgs(args)
		locArgs["search_engine"] = search_engine
		loc, err := resolveLocationFromArgs(ctx.AppDB(), locArgs, nil)
		if err != nil {
			return nil, err
		}
		locID = loc.ID
	}
	id, err := upsertSearchEntity(ctx.AppDB(), pid, search_engine, etype, ident, label, urlText, locID, "{}")
	if err != nil {
		return nil, err
	}
	return getSearchEntity(ctx.AppDB(), pid, id)
}

func (a *App) toolEntitiesList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	search_engine := ""
	if raw := strings.TrimSpace(strArg(args, "search_engine", "")); raw != "" {
		var err error
		search_engine, err = normalizeSearchEngine(raw)
		if err != nil {
			return nil, err
		}
	}
	etype := strings.ToLower(strings.TrimSpace(strArg(args, "entity_type", "")))
	limit := int(toInt64(args["limit"]))
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	sqlText := `SELECT id, project_id, search_engine, entity_type, identifier, label, url,
	                   default_location_id, created_at, updated_at
	              FROM search_entities
	             WHERE project_id = ?`
	qargs := []any{projectScopeFromArgs(ctx, args)}
	if search_engine != "" {
		sqlText += ` AND search_engine = ?`
		qargs = append(qargs, search_engine)
	}
	if etype != "" {
		sqlText += ` AND entity_type = ?`
		qargs = append(qargs, etype)
	}
	sqlText += ` ORDER BY search_engine, entity_type, identifier LIMIT ?`
	qargs = append(qargs, limit)
	rows, err := ctx.AppDB().Query(sqlText, qargs...)
	if err != nil {
		return nil, err
	}
	return scanSearchEntities(rows)
}

func (a *App) toolEntitiesGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := toInt64(args["id"])
	if id == 0 {
		return nil, errors.New("id required")
	}
	return getSearchEntity(ctx.AppDB(), projectScopeFromArgs(ctx, args), id)
}

func (a *App) toolEntitiesRemove(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := toInt64(args["id"])
	if id == 0 {
		return nil, errors.New("id required")
	}
	pid := projectScopeFromArgs(ctx, args)
	if _, err := getSearchEntity(ctx.AppDB(), pid, id); err != nil {
		return nil, err
	}
	if _, err := ctx.AppDB().Exec(`DELETE FROM search_entities WHERE id = ? AND project_id = ?`, id, pid); err != nil {
		return nil, err
	}
	return map[string]any{"removed": id}, nil
}

func getSearchEntity(db *sql.DB, pid string, id int64) (*SearchEntity, error) {
	var e SearchEntity
	var defaultLoc sql.NullInt64
	err := db.QueryRow(
		`SELECT id, project_id, search_engine, entity_type, identifier, label, url,
		        default_location_id, created_at, updated_at
		   FROM search_entities
		  WHERE id = ? AND project_id = ?`, id, pid,
	).Scan(&e.ID, &e.ProjectID, &e.SearchEngine, &e.EntityType, &e.Identifier, &e.Label, &e.URL, &defaultLoc, &e.CreatedAt, &e.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("entity %d not found", id)
	}
	if err != nil {
		return nil, err
	}
	if defaultLoc.Valid {
		e.DefaultLocationID = &defaultLoc.Int64
	}
	return &e, nil
}

func scanSearchEntities(rows *sql.Rows) ([]SearchEntity, error) {
	defer rows.Close()
	out := []SearchEntity{}
	for rows.Next() {
		var e SearchEntity
		var defaultLoc sql.NullInt64
		if err := rows.Scan(&e.ID, &e.ProjectID, &e.SearchEngine, &e.EntityType, &e.Identifier, &e.Label, &e.URL, &defaultLoc, &e.CreatedAt, &e.UpdatedAt); err != nil {
			return nil, err
		}
		if defaultLoc.Valid {
			e.DefaultLocationID = &defaultLoc.Int64
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

func upsertSearchEntity(db execer, pid, search_engine, entityType, identifier, label, urlText string, defaultLocationID any, rawJSON string) (int64, error) {
	if rawJSON == "" {
		rawJSON = "{}"
	}
	if _, err := db.Exec(
		`INSERT INTO search_entities
		    (project_id, search_engine, entity_type, identifier, label, url, default_location_id, raw_json, updated_at)
		 VALUES (?, ?, ?, ?, ?, ?, ?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(project_id, search_engine, entity_type, identifier) DO UPDATE SET
		    label = CASE WHEN excluded.label != '' THEN excluded.label ELSE search_entities.label END,
		    url = CASE WHEN excluded.url != '' THEN excluded.url ELSE search_entities.url END,
		    default_location_id = COALESCE(excluded.default_location_id, search_entities.default_location_id),
		    raw_json = CASE WHEN excluded.raw_json != '{}' THEN excluded.raw_json ELSE search_entities.raw_json END,
		    updated_at = CURRENT_TIMESTAMP`,
		pid, search_engine, entityType, identifier, label, urlText, defaultLocationID, rawJSON); err != nil {
		return 0, err
	}
	qr, ok := db.(queryRower)
	if !ok {
		return 0, errors.New("database handle cannot query inserted search_engine entity")
	}
	var id int64
	row := qr.QueryRow(
		`SELECT id FROM search_entities
		  WHERE project_id = ? AND search_engine = ? AND entity_type = ? AND identifier = ?`,
		pid, search_engine, entityType, identifier)
	if err := row.Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

type execer interface {
	Exec(string, ...any) (sql.Result, error)
}

type queryRower interface {
	QueryRow(string, ...any) *sql.Row
}

type normalizedSERPItem struct {
	Rank              *int64
	ResultType        string
	EntityType        string
	Identifier        string
	Title             string
	URL               string
	ChannelIdentifier string
	ChannelTitle      string
	Snippet           string
	PublishedAt       string
	RawJSON           string
}

func (a *App) toolSERPSearch(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	search_engine, err := normalizeSearchEngine(strArg(args, "search_engine", "google"))
	if err != nil {
		return nil, err
	}
	pid := projectScopeFromArgs(ctx, args)
	db := ctx.AppDB()
	var keywordID any
	var keywordText string
	var defaultLoc *int64
	if id := toInt64(args["keyword_id"]); id != 0 {
		k, err := getKeyword(db, pid, id)
		if err != nil {
			return nil, err
		}
		if k.SearchEngine != "" && k.SearchEngine != search_engine {
			return nil, fmt.Errorf("keyword %d belongs to search_engine %s, not %s", id, k.SearchEngine, search_engine)
		}
		keywordID = id
		keywordText = k.Text
		defaultLoc = &k.LocationID
	} else {
		keywordText = normaliseKeyword(strArg(args, "keyword", ""))
	}
	if keywordText == "" {
		return nil, errors.New("keyword or keyword_id required")
	}
	locArgs := copyArgs(args)
	locArgs["search_engine"] = search_engine
	loc, err := resolveLocationFromArgs(db, locArgs, defaultLoc)
	if err != nil {
		return nil, err
	}
	depth := int(toInt64(args["depth"]))
	if depth <= 0 {
		depth = 20
	}
	if depth > 100 {
		depth = 100
	}
	rowRaw, taskRaw, toolName, err := serpSearchViaProvider(ctx, search_engine, keywordText, loc, depth, strArg(args, "device", "desktop"))
	if err != nil {
		return nil, err
	}
	items, err := decodeSERPItems(rowRaw)
	if err != nil {
		return nil, err
	}
	now := time.Now().Unix()
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	res, err := tx.Exec(
		`INSERT INTO search_serp_snapshots
		    (project_id, search_engine, keyword_id, keyword_text, location_id, provider, ts, raw_json)
		 VALUES (?, ?, ?, ?, ?, 'dataforseo', ?, ?)`,
		pid, search_engine, keywordID, keywordText, loc.ID, now, string(taskRaw))
	if err != nil {
		return nil, err
	}
	snapshotID, _ := res.LastInsertId()
	stored := []SearchRanking{}
	for _, raw := range items {
		item := normalizeSERPItem(search_engine, raw)
		if item.Identifier == "" && item.URL == "" && item.Title == "" {
			continue
		}
		var entityID any
		if item.EntityType != "" && item.Identifier != "" {
			id, err := upsertSearchEntity(tx, pid, search_engine, item.EntityType, item.Identifier, item.Title, item.URL, loc.ID, item.RawJSON)
			if err != nil {
				return nil, err
			}
			entityID = id
		}
		rowRes, err := tx.Exec(
			`INSERT INTO search_serp_results
			    (snapshot_id, entity_id, rank, result_type, title, url, identifier,
			     channel_identifier, channel_title, snippet, published_at, raw_json)
			 VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)`,
			snapshotID, entityID, item.Rank, item.ResultType, item.Title, item.URL, item.Identifier,
			item.ChannelIdentifier, item.ChannelTitle, item.Snippet, item.PublishedAt, item.RawJSON)
		if err != nil {
			return nil, err
		}
		resultID, _ := rowRes.LastInsertId()
		var eid *int64
		if v, ok := entityID.(int64); ok {
			eid = &v
		}
		stored = append(stored, SearchRanking{
			ID: resultID, SnapshotID: snapshotID, EntityID: eid, SearchEngine: search_engine,
			KeywordText: keywordText, LocationID: &loc.ID, Provider: "dataforseo", TS: now,
			Rank: item.Rank, ResultType: item.ResultType, Title: item.Title, URL: item.URL,
			Identifier: item.Identifier, ChannelIdentifier: item.ChannelIdentifier,
			ChannelTitle: item.ChannelTitle, Snippet: item.Snippet, PublishedAt: item.PublishedAt,
		})
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return map[string]any{
		"provider":      "dataforseo",
		"tool":          toolName,
		"search_engine": search_engine,
		"keyword":       keywordText,
		"location_id":   loc.ID,
		"snapshot_id":   snapshotID,
		"results":       stored,
		"count":         len(stored),
	}, nil
}

func serpSearchViaProvider(ctx *sdk.AppCtx, search_engine, keyword string, loc *SEOLocation, depth int, device string) ([]byte, []byte, string, error) {
	slug, connID, err := boundProvider(ctx)
	if err != nil {
		return nil, nil, "", err
	}
	if slug == "" {
		slug = "dataforseo"
	}
	if slug != "dataforseo" {
		return nil, nil, "", fmt.Errorf("provider %q not wired for search_engine SERP search", slug)
	}
	if loc == nil || loc.LocationCode == nil {
		return nil, nil, "", fmt.Errorf("dataforseo SERP search requires a location with location_code")
	}
	if device == "" {
		device = "desktop"
	}
	input := map[string]any{
		"keyword":       keyword,
		"location_code": *loc.LocationCode,
		"language_code": strings.ToLower(loc.LanguageCode),
		"depth":         depth,
	}
	toolName := "serp_organic"
	if search_engine == "youtube" {
		toolName = "youtube_organic_serp"
		input["device"] = device
	} else {
		input["device"] = device
	}
	rowRaw, taskRaw, err := callDfs(ctx, connID, toolName, input)
	return rowRaw, taskRaw, toolName, err
}

func decodeSERPItems(rowRaw []byte) ([]map[string]any, error) {
	if rowRaw == nil {
		return nil, errors.New("provider returned zero SERP rows")
	}
	var obj map[string]any
	if err := json.Unmarshal(rowRaw, &obj); err != nil {
		return nil, fmt.Errorf("parse SERP result: %w", err)
	}
	rawItems, _ := obj["items"].([]any)
	out := []map[string]any{}
	for _, raw := range rawItems {
		if m, ok := raw.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out, nil
}

func normalizeSERPItem(search_engine string, raw map[string]any) normalizedSERPItem {
	rawText, _ := json.Marshal(raw)
	item := normalizedSERPItem{
		Rank:        intPtr(firstNumber(raw, "rank_absolute", "rank_group", "rank")),
		ResultType:  strings.ToLower(firstString(raw, "type", "item_type", "se_type")),
		Title:       firstString(raw, "title", "name"),
		URL:         firstString(raw, "url", "link"),
		Snippet:     firstString(raw, "description", "snippet"),
		PublishedAt: firstString(raw, "timestamp", "published_date", "date"),
		RawJSON:     string(rawText),
	}
	switch search_engine {
	case "youtube":
		item.EntityType = "video"
		item.Identifier = firstString(raw, "video_id")
		if item.Identifier == "" {
			item.Identifier = youtubeVideoID(item.URL)
		}
		item.ChannelIdentifier = firstString(raw, "channel_id", "channel_url", "author_url")
		item.ChannelTitle = firstString(raw, "channel_name", "channel_title", "author")
	case "google":
		item.EntityType = "page"
		item.Identifier = item.URL
		if item.Identifier == "" {
			if domain := firstString(raw, "domain"); domain != "" {
				item.EntityType = "domain"
				item.Identifier = normaliseHost(domain)
			}
		}
	}
	return item
}

func (a *App) toolRankingsForEntity(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := toInt64(args["entity_id"])
	if id == 0 {
		return nil, errors.New("entity_id required")
	}
	pid := projectScopeFromArgs(ctx, args)
	e, err := getSearchEntity(ctx.AppDB(), pid, id)
	if err != nil {
		return nil, err
	}
	since := toInt64(args["since"])
	limit := int(toInt64(args["limit"]))
	if limit <= 0 || limit > 500 {
		limit = 200
	}
	q := `SELECT r.id, r.snapshot_id, r.entity_id, s.search_engine, s.keyword_id, s.keyword_text,
	             s.location_id, s.provider, s.ts, r.rank, r.result_type, r.title, r.url,
	             r.identifier, r.channel_identifier, r.channel_title, r.snippet, r.published_at
	        FROM search_serp_results r
	        JOIN search_serp_snapshots s ON s.id = r.snapshot_id
	       WHERE s.project_id = ? AND s.ts >= ? AND (r.entity_id = ?`
	qargs := []any{pid, since, id}
	if e.SearchEngine == "youtube" && e.EntityType == "channel" {
		q += ` OR lower(r.channel_identifier) = lower(?) OR lower(r.channel_title) = lower(?)`
		qargs = append(qargs, e.Identifier, strings.TrimPrefix(e.Identifier, "@"))
	}
	q += `) ORDER BY s.ts DESC, r.rank ASC LIMIT ?`
	qargs = append(qargs, limit)
	rows, err := ctx.AppDB().Query(q, qargs...)
	if err != nil {
		return nil, err
	}
	return scanSearchRankings(rows)
}

func scanSearchRankings(rows *sql.Rows) ([]SearchRanking, error) {
	defer rows.Close()
	out := []SearchRanking{}
	for rows.Next() {
		var r SearchRanking
		if err := rows.Scan(&r.ID, &r.SnapshotID, &r.EntityID, &r.SearchEngine, &r.KeywordID,
			&r.KeywordText, &r.LocationID, &r.Provider, &r.TS, &r.Rank, &r.ResultType,
			&r.Title, &r.URL, &r.Identifier, &r.ChannelIdentifier, &r.ChannelTitle,
			&r.Snippet, &r.PublishedAt); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

func (a *App) toolKeywordIdeas(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	search_engine, err := normalizeSearchEngine(strArg(args, "search_engine", "google"))
	if err != nil {
		return nil, err
	}
	seeds := stringSliceArg(args, "seed_keywords")
	if len(seeds) == 0 {
		seeds = stringSliceArg(args, "keywords")
	}
	if len(seeds) == 0 {
		if s := strings.TrimSpace(strArg(args, "keyword", "")); s != "" {
			seeds = []string{s}
		}
	}
	for i := range seeds {
		seeds[i] = normaliseKeyword(seeds[i])
	}
	seeds = nonEmptyStrings(seeds)
	if len(seeds) == 0 {
		return nil, errors.New("seed_keywords required")
	}
	limit := int(toInt64(args["limit"]))
	if limit <= 0 || limit > 100 {
		limit = 50
	}
	if search_engine == "google" {
		return googleKeywordIdeasViaProvider(ctx, args, seeds, limit)
	}
	if boolArg(args, "refresh", true) {
		for _, seed := range seeds {
			searchArgs := copyArgs(args)
			searchArgs["search_engine"] = "youtube"
			searchArgs["keyword"] = seed
			if _, ok := searchArgs["depth"]; !ok {
				searchArgs["depth"] = int64(20)
			}
			if _, err := a.toolSERPSearch(ctx, searchArgs); err != nil {
				return nil, err
			}
		}
	}
	return youtubeIdeasFromCachedSERPs(ctx.AppDB(), projectScopeFromArgs(ctx, args), seeds, limit)
}

func googleKeywordIdeasViaProvider(ctx *sdk.AppCtx, args map[string]any, seeds []string, limit int) (any, error) {
	slug, connID, err := boundProvider(ctx)
	if err != nil {
		return nil, err
	}
	if slug == "" {
		slug = "dataforseo"
	}
	if slug != "dataforseo" {
		return nil, fmt.Errorf("provider %q not wired for keyword ideas", slug)
	}
	locArgs := copyArgs(args)
	locArgs["search_engine"] = "google"
	loc, err := resolveLocationFromArgs(ctx.AppDB(), locArgs, nil)
	if err != nil {
		return nil, err
	}
	if loc.LocationCode == nil {
		return nil, fmt.Errorf("dataforseo keyword ideas requires a location with location_code")
	}
	rowRaw, taskRaw, err := callDfs(ctx, connID, "keyword_ideas", map[string]any{
		"keywords":             seeds,
		"location_code":        *loc.LocationCode,
		"language_code":        strings.ToLower(loc.LanguageCode),
		"include_seed_keyword": true,
		"limit":                limit,
	})
	if err != nil {
		return nil, err
	}
	items, _ := decodeSERPItems(rowRaw)
	return map[string]any{
		"provider":      "dataforseo",
		"search_engine": "google",
		"capability":    "keyword_ideas",
		"location_id":   loc.ID,
		"items":         items,
		"raw":           json.RawMessage(taskRaw),
	}, nil
}

func youtubeIdeasFromCachedSERPs(db *sql.DB, pid string, seeds []string, limit int) (any, error) {
	rows, err := db.Query(
		`SELECT s.keyword_text, r.title, r.channel_title, r.published_at, r.rank, r.url
		   FROM search_serp_results r
		   JOIN search_serp_snapshots s ON s.id = r.snapshot_id
		  WHERE s.project_id = ? AND s.search_engine = 'youtube'
		    AND s.keyword_text IN (`+placeholders(len(seeds))+`)
		  ORDER BY s.ts DESC, r.rank ASC`,
		append([]any{pid}, stringsToAny(seeds)...)...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	ideas := map[string]*keywordIdea{}
	for rows.Next() {
		var seed, title, channel, published, urlText string
		var rank *int64
		if err := rows.Scan(&seed, &title, &channel, &published, &rank, &urlText); err != nil {
			return nil, err
		}
		for _, kw := range titleIdeas(seed, title) {
			it := ideas[kw]
			if it == nil {
				it = &keywordIdea{Keyword: kw, SourceKeyword: seed, OpportunityScore: 50}
				ideas[kw] = it
			}
			if rank != nil && *rank <= 10 {
				it.OpportunityScore += 3
			}
			if published == "" {
				it.OpportunityScore += 2
			}
			if len(it.ExampleTitles) < 3 && title != "" && !containsString(it.ExampleTitles, title) {
				it.ExampleTitles = append(it.ExampleTitles, title)
			}
			if len(it.TopChannels) < 3 && channel != "" && !containsString(it.TopChannels, channel) {
				it.TopChannels = append(it.TopChannels, channel)
			}
			if it.Reason == "" {
				it.Reason = "Derived from recurring phrases in ranking YouTube videos."
			}
		}
		_ = urlText
	}
	out := []*keywordIdea{}
	for _, it := range ideas {
		if it.OpportunityScore > 100 {
			it.OpportunityScore = 100
		}
		out = append(out, it)
	}
	sortIdeas(out)
	if len(out) > limit {
		out = out[:limit]
	}
	return map[string]any{
		"provider":      "dataforseo",
		"search_engine": "youtube",
		"capability":    "keyword_ideas",
		"items":         out,
		"cached":        true,
	}, rows.Err()
}

func (a *App) toolContentOpportunities(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	search_engine, err := normalizeSearchEngine(strArg(args, "search_engine", "youtube"))
	if err != nil {
		return nil, err
	}
	limit := int(toInt64(args["limit"]))
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	rows, err := ctx.AppDB().Query(
		`SELECT s.keyword_text,
		        COUNT(*) AS result_count,
		        SUM(CASE WHEN r.rank <= 10 THEN 1 ELSE 0 END) AS top10_count,
		        MAX(s.ts) AS latest_ts,
		        GROUP_CONCAT(CASE WHEN r.rank <= 5 THEN r.title ELSE NULL END, ' || ') AS titles
		   FROM search_serp_snapshots s
		   JOIN search_serp_results r ON r.snapshot_id = s.id
		  WHERE s.project_id = ? AND s.search_engine = ?
		  GROUP BY s.keyword_text
		  ORDER BY latest_ts DESC
		  LIMIT ?`,
		projectScopeFromArgs(ctx, args), search_engine, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []map[string]any{}
	for rows.Next() {
		var keyword, titles string
		var titlesNull sql.NullString
		var resultCount, top10Count, latestTS int64
		if err := rows.Scan(&keyword, &resultCount, &top10Count, &latestTS, &titlesNull); err != nil {
			return nil, err
		}
		if titlesNull.Valid {
			titles = titlesNull.String
		}
		score := int64(50)
		if resultCount >= 10 {
			score += 15
		}
		if top10Count >= 5 {
			score += 10
		}
		out = append(out, map[string]any{
			"search_engine":     search_engine,
			"keyword":           keyword,
			"opportunity_score": minInt64(score, 100),
			"result_count":      resultCount,
			"top10_count":       top10Count,
			"latest_ts":         latestTS,
			"example_titles":    splitLimited(titles, " || ", 5),
		})
	}
	return map[string]any{"search_engine": search_engine, "items": out}, rows.Err()
}

func copyArgs(args map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range args {
		out[k] = v
	}
	return out
}

func stringSliceArg(args map[string]any, key string) []string {
	v, ok := args[key]
	if !ok {
		return nil
	}
	switch x := v.(type) {
	case []string:
		return append([]string(nil), x...)
	case []any:
		out := []string{}
		for _, item := range x {
			if s, ok := item.(string); ok {
				out = append(out, s)
			}
		}
		return out
	case string:
		parts := strings.Split(x, ",")
		out := []string{}
		for _, p := range parts {
			if s := strings.TrimSpace(p); s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func nonEmptyStrings(in []string) []string {
	out := []string{}
	seen := map[string]bool{}
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

func stringsToAny(in []string) []any {
	out := make([]any, 0, len(in))
	for _, s := range in {
		out = append(out, s)
	}
	return out
}

func placeholders(n int) string {
	if n <= 0 {
		return "NULL"
	}
	parts := make([]string, n)
	for i := range parts {
		parts[i] = "?"
	}
	return strings.Join(parts, ",")
}

func firstNumber(obj map[string]any, keys ...string) (int64, bool) {
	for _, key := range keys {
		if v, ok := obj[key]; ok {
			switch x := v.(type) {
			case float64:
				return int64(x), true
			case int64:
				return x, true
			case int:
				return int64(x), true
			case json.Number:
				n, err := x.Int64()
				return n, err == nil
			}
		}
	}
	return 0, false
}

func intPtr(v int64, ok bool) *int64 {
	if !ok {
		return nil
	}
	return &v
}

func titleIdeas(seed, title string) []string {
	seed = normaliseKeyword(seed)
	clean := cleanIdeaText(title)
	if clean == "" {
		return []string{seed}
	}
	out := []string{seed}
	if len(clean) <= 90 {
		out = append(out, clean)
	}
	for _, sep := range []string{" - ", " | ", ": ", "? "} {
		if i := strings.Index(clean, sep); i > 0 {
			out = append(out, strings.TrimSpace(clean[:i]))
		}
	}
	words := strings.Fields(clean)
	for size := 3; size <= 5; size++ {
		for i := 0; i+size <= len(words); i++ {
			phrase := strings.Join(words[i:i+size], " ")
			if usefulIdeaPhrase(phrase) {
				out = append(out, phrase)
			}
		}
	}
	return nonEmptyStrings(out)
}

func cleanIdeaText(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	repl := strings.NewReplacer(
		"(", " ", ")", " ", "[", " ", "]", " ", "{", " ", "}", " ",
		"\"", " ", "'", " ", ",", " ", ".", " ", "!", " ", "\n", " ",
		"\t", " ", "#", " ", "*", " ",
	)
	s = repl.Replace(s)
	return strings.Join(strings.Fields(s), " ")
}

func usefulIdeaPhrase(s string) bool {
	if len(s) < 10 || len(s) > 70 {
		return false
	}
	stopStarts := []string{"the ", "and ", "for ", "with ", "from ", "this ", "that "}
	for _, p := range stopStarts {
		if strings.HasPrefix(s, p) {
			return false
		}
	}
	return true
}

func containsString(xs []string, want string) bool {
	for _, x := range xs {
		if x == want {
			return true
		}
	}
	return false
}

func sortIdeas(ideas []*keywordIdea) {
	sort.SliceStable(ideas, func(i, j int) bool {
		if ideas[i].OpportunityScore == ideas[j].OpportunityScore {
			return ideas[i].Keyword < ideas[j].Keyword
		}
		return ideas[i].OpportunityScore > ideas[j].OpportunityScore
	})
}

func minInt64(a, b int64) int64 {
	if a < b {
		return a
	}
	return b
}

func splitLimited(s, sep string, limit int) []string {
	if s == "" || limit <= 0 {
		return nil
	}
	parts := strings.Split(s, sep)
	out := []string{}
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p == "" || containsString(out, p) {
			continue
		}
		out = append(out, p)
		if len(out) >= limit {
			break
		}
	}
	return out
}

// ─── HTTP panel helpers ──────────────────────────────────────────

func (a *App) handleLocationsList(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	args := map[string]any{}
	for _, k := range []string{"provider", "search_engine", "country_iso", "language_code", "language_iso", "q", "limit"} {
		if v := strings.TrimSpace(r.URL.Query().Get(k)); v != "" {
			if k == "limit" {
				if n, err := strconv.ParseInt(v, 10, 64); err == nil {
					args[k] = n
				}
			} else {
				args[k] = v
			}
		}
	}
	out, err := listLocations(mustCtx(r).AppDB(), args)
	writeJSONOrErr(w, map[string]any{"locations": out}, err)
}

func (a *App) handleLocationsSync(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	out, err := syncLocations(mustCtx(r))
	writeJSONOrErr(w, out, err)
}

func (a *App) handleToolsCall(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	var body struct {
		Tool string         `json:"tool"`
		Args map[string]any `json:"args"`
	}
	if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
		http.Error(w, "invalid JSON: "+err.Error(), http.StatusBadRequest)
		return
	}
	if body.Tool == "" {
		http.Error(w, "tool required", http.StatusBadRequest)
		return
	}
	if body.Args == nil {
		body.Args = map[string]any{}
	}
	if _, ok := body.Args["_project_id"]; !ok {
		if pid := strings.TrimSpace(r.URL.Query().Get("project_id")); pid != "" {
			body.Args["_project_id"] = pid
		}
	}
	for _, t := range a.MCPTools() {
		if t.Name == body.Tool {
			out, err := t.Handler(mustCtx(r), body.Args)
			writeJSONOrErr(w, out, err)
			return
		}
	}
	http.Error(w, "unknown tool: "+body.Tool, http.StatusNotFound)
}

// ─── HTTP refresh dispatchers (NOT exposed as MCP) ───────────────
//
// Routes:
//   POST /domains/{id}/refresh             → refreshDomain
//   POST /domains/{id}/backlinks/refresh   → refreshBacklinks
//   POST /keywords/{id}/refresh            → refreshKeyword
//
// Each route is a thin wrapper around an internal Go func. The funcs
// are unexported and never registered as MCP tools — paid actions
// stay off the agent's surface.

func (a *App) handleDomainsItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/domains/")
	parts := strings.Split(rest, "/")
	if len(parts) < 2 {
		http.Error(w, "expected /domains/{id}/refresh or /domains/{id}/backlinks/refresh", http.StatusNotFound)
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid domain id", http.StatusBadRequest)
		return
	}
	ctx := mustCtx(r)
	args := locationArgsFromQuery(r)
	switch {
	case len(parts) == 2 && parts[1] == "refresh":
		out, err := refreshDomain(ctx, id, args)
		writeJSONOrErr(w, out, err)
	case len(parts) == 3 && parts[1] == "backlinks" && parts[2] == "refresh":
		out, err := refreshBacklinks(ctx, id, args)
		writeJSONOrErr(w, out, err)
	default:
		http.Error(w, "unknown subroute", http.StatusNotFound)
	}
}

func (a *App) handleKeywordsItem(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	rest := strings.TrimPrefix(r.URL.Path, "/keywords/")
	parts := strings.Split(rest, "/")
	if len(parts) != 2 || parts[1] != "refresh" {
		http.Error(w, "expected /keywords/{id}/refresh", http.StatusNotFound)
		return
	}
	id, err := strconv.ParseInt(parts[0], 10, 64)
	if err != nil || id <= 0 {
		http.Error(w, "invalid keyword id", http.StatusBadRequest)
		return
	}
	out, err := refreshKeyword(mustCtx(r), id, locationArgsFromQuery(r))
	writeJSONOrErr(w, out, err)
}

func locationArgsFromQuery(r *http.Request) map[string]any {
	args := map[string]any{}
	q := r.URL.Query()
	if v := strings.TrimSpace(q.Get("location_id")); v != "" {
		if id, err := strconv.ParseInt(v, 10, 64); err == nil {
			args["location_id"] = id
		}
	}
	for _, k := range []string{"country_iso", "language_code", "language_iso", "search_engine", "provider"} {
		if v := strings.TrimSpace(q.Get(k)); v != "" {
			args[k] = v
		}
	}
	if pid := strings.TrimSpace(q.Get("project_id")); pid != "" {
		args["_project_id"] = pid
	}
	return args
}

func writeJSONOrErr(w http.ResponseWriter, payload any, err error) {
	if err != nil {
		// 503 reads better than 500 for "no provider bound" — it's a
		// configuration state, not a code fault.
		code := http.StatusInternalServerError
		if errors.Is(err, errProviderUnbound) {
			code = http.StatusServiceUnavailable
		}
		http.Error(w, err.Error(), code)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func mustCtx(_ *http.Request) *sdk.AppCtx { return globalCtx }

// errProviderUnbound is returned from refresh* funcs when no SEO data
// provider integration is bound. Callers translate to HTTP 503.
var errProviderUnbound = errors.New("no SEO data provider is bound — connect DataForSEO/Ahrefs/Moz in Integrations")

// boundProvider returns the bound SEO data provider connection's
// slug + connection id, or errProviderUnbound when nothing is wired.
func boundProvider(ctx *sdk.AppCtx) (slug string, connID int64, err error) {
	bound := ctx.IntegrationFor(providerRole)
	if bound == nil {
		return "", 0, errProviderUnbound
	}
	// AppSlug is filled lazily via GetConnection. If empty, fall back
	// to the integration runner — it'll route by connection id alone.
	return bound.AppSlug, bound.ConnectionID, nil
}

// ─── Internal refresh orchestrators ──────────────────────────────
//
// One func per refreshable entity. Dispatch on the bound provider's
// slug, call the provider-specific normaliser (provider_dataforseo.go
// today; provider_ahrefs.go later), write rows in our schema. DB
// writes happen here; HTTP + credential handling live in the
// integration runner; provider-shape mapping lives in the per-slug
// normaliser file.

func refreshDomain(ctx *sdk.AppCtx, domainID int64, args map[string]any) (any, error) {
	pid := projectScopeFromArgs(ctx, args)
	d, err := getDomain(ctx.AppDB(), pid, domainID)
	if err != nil {
		return nil, err
	}
	loc, err := resolveLocationFromArgs(ctx.AppDB(), args, d.DefaultLocationID)
	if err != nil {
		return nil, err
	}
	slug, _, err := boundProvider(ctx)
	if err != nil {
		return nil, err
	}
	switch slug {
	case "dataforseo":
		return refreshDomainViaDataForSEO(ctx, d, loc)
	default:
		return nil, fmt.Errorf("provider %q not yet wired (v0.2 supports dataforseo only)", slug)
	}
}

func refreshKeyword(ctx *sdk.AppCtx, keywordID int64, args map[string]any) (any, error) {
	pid := projectScopeFromArgs(ctx, args)
	k, err := getKeyword(ctx.AppDB(), pid, keywordID)
	if err != nil {
		return nil, err
	}
	if k.SearchEngine != "" && k.SearchEngine != "google" {
		return nil, fmt.Errorf("%s keyword metrics are not supported; refresh %s SERP data with serp_search or keyword_ideas refresh=true", k.SearchEngine, k.SearchEngine)
	}
	loc, err := resolveLocationFromArgs(ctx.AppDB(), args, &k.LocationID)
	if err != nil {
		return nil, err
	}
	slug, _, err := boundProvider(ctx)
	if err != nil {
		return nil, err
	}
	switch slug {
	case "dataforseo":
		return refreshKeywordViaDataForSEO(ctx, k, loc)
	default:
		return nil, fmt.Errorf("provider %q not yet wired (v0.2 supports dataforseo only)", slug)
	}
}

func refreshBacklinks(ctx *sdk.AppCtx, domainID int64, args map[string]any) (any, error) {
	pid := projectScopeFromArgs(ctx, args)
	d, err := getDomain(ctx.AppDB(), pid, domainID)
	if err != nil {
		return nil, err
	}
	slug, _, err := boundProvider(ctx)
	if err != nil {
		return nil, err
	}
	switch slug {
	case "dataforseo":
		return refreshBacklinksViaDataForSEO(ctx, d)
	default:
		return nil, fmt.Errorf("provider %q not yet wired (v0.2 supports dataforseo only)", slug)
	}
}

// ─── Tiny arg helpers (mirrors the pattern in todo/calendar apps) ─

func schemaObject(props map[string]any, required []string) map[string]any {
	s := map[string]any{
		"type":       "object",
		"properties": props,
	}
	if len(required) > 0 {
		s["required"] = required
	}
	return s
}

func strArg(args map[string]any, key, def string) string {
	if v, ok := args[key].(string); ok {
		return v
	}
	return def
}

func toInt64(v any) int64 {
	switch x := v.(type) {
	case int64:
		return x
	case int:
		return int64(x)
	case float64:
		return int64(x)
	}
	return 0
}

func boolArg(args map[string]any, key string, def bool) bool {
	if v, ok := args[key].(bool); ok {
		return v
	}
	return def
}

func hasLocationArgs(args map[string]any) bool {
	if toInt64(args["location_id"]) != 0 {
		return true
	}
	for _, key := range []string{"country_iso", "language_code", "language_iso"} {
		if strings.TrimSpace(strArg(args, key, "")) != "" {
			return true
		}
	}
	return false
}

func boolToInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// ─── main ────────────────────────────────────────────────────────

func main() {
	sdk.Run(&App{})
}
