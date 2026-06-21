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
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: seo
display_name: SEO
version: 0.3.7
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
    - { name: locations_list, description: "List active SEO provider locations." }
    - { name: domains_add,    description: "Add a domain (hostname) to track; accepts location_id or country_iso+language_code for the default locale." }
    - { name: domains_list,   description: "List tracked domains in this scope." }
    - { name: domains_get,    description: "Read one domain plus latest metrics." }
    - { name: domains_remove, description: "Remove a domain (cascades to children)." }
    - { name: keywords_add,    description: "Add a keyword to track; requires location_id or country_iso+language_code." }
    - { name: keywords_list,   description: "List keywords in this scope." }
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
			Description: "List keywords in this project scope. Args: country_iso? (filter), limit? (default 200).",
			InputSchema: schemaObject(map[string]any{
				"country_iso": map[string]any{"type": "string"},
				"limit":       map[string]any{"type": "integer"},
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
	ID          int64  `json:"id"`
	ProjectID   string `json:"project_id"`
	Text        string `json:"text"`
	LocationID  int64  `json:"location_id"`
	CountryISO  string `json:"country_iso"`
	LanguageISO string `json:"language_iso"`
	CreatedAt   string `json:"created_at"`
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
		`SELECT id, project_id, text, location_id, country_iso, language_iso, created_at
		   FROM keywords WHERE id = ? AND project_id = ?`, id, pid,
	).Scan(&k.ID, &k.ProjectID, &k.Text, &k.LocationID, &k.CountryISO, &k.LanguageISO, &k.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("keyword %d not found", id)
	}
	if err != nil {
		return nil, err
	}
	return &k, nil
}

func listKeywords(db *sql.DB, pid, countryISO string, limit int) ([]Keyword, error) {
	if limit <= 0 {
		limit = 200
	}
	var (
		rows *sql.Rows
		err  error
	)
	if countryISO == "" {
		rows, err = db.Query(
			`SELECT id, project_id, text, location_id, country_iso, language_iso, created_at
			   FROM keywords WHERE project_id = ?
			   ORDER BY text LIMIT ?`, pid, limit)
	} else {
		rows, err = db.Query(
			`SELECT id, project_id, text, location_id, country_iso, language_iso, created_at
			   FROM keywords WHERE project_id = ? AND country_iso = ?
			   ORDER BY text LIMIT ?`, pid, strings.ToUpper(countryISO), limit)
	}
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Keyword{}
	for rows.Next() {
		var k Keyword
		if err := rows.Scan(&k.ID, &k.ProjectID, &k.Text, &k.LocationID, &k.CountryISO, &k.LanguageISO, &k.CreatedAt); err != nil {
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
	country := strArg(args, "country_iso", "")
	return listKeywords(ctx.AppDB(), projectScopeFromArgs(ctx, args), country, limit)
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
