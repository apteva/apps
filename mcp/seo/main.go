// Apteva SEO app — generic SEO research workbench.
//
// v0.1 surface:
//   * domains: hostname identity, project-scoped, dedup'd via UNIQUE
//     (project_id, host) where host is normalised (lowercase, no
//     scheme, no leading 'www.', no path).
//   * keywords: (text, country_iso, language_iso) identity, also
//     project-scoped. Text is normalised (trimmed, lowercased).
//
// Pages, rankings, backlinks, panel UI, and a stub seo_data_provider
// land in v0.2; scheduled refresh via the jobs app lands in v0.3.
//
// project_id comes from APTEVA_PROJECT_ID at runtime; '' = global
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
version: 0.2.0
description: Generic SEO research workbench — domains, keywords, rankings, backlinks behind one pluggable provider integration.
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
    - { name: domains_add,    description: "Add a domain (hostname) to track." }
    - { name: domains_list,   description: "List tracked domains in this scope." }
    - { name: domains_get,    description: "Read one domain plus latest metrics." }
    - { name: domains_remove, description: "Remove a domain (cascades to children)." }
    - { name: keywords_add,    description: "Add a keyword (text + country + language) to track." }
    - { name: keywords_list,   description: "List keywords in this scope." }
    - { name: keywords_get,    description: "Read one keyword plus latest metrics." }
    - { name: keywords_remove, description: "Remove a keyword (cascades to children)." }
    - { name: rankings_for_domain,    description: "Cached rankings history for a domain." }
    - { name: rankings_for_keyword,   description: "Cached rankings of a keyword across tracked domains." }
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
	}
}

// ─── MCP tools ───────────────────────────────────────────────────

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{Name: "domains_add",
			Description: "Add a domain (hostname) to track. Host is normalised: lowercased, scheme stripped, leading 'www.' stripped, no trailing slash. Args: host (required), label?.",
			InputSchema: schemaObject(map[string]any{
				"host":  map[string]any{"type": "string"},
				"label": map[string]any{"type": "string"},
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
			Description: "Add a keyword to track. Args: text (required), country_iso? (default 'US'), language_iso? (default 'en'). Identity = (text, country_iso, language_iso); duplicates upsert.",
			InputSchema: schemaObject(map[string]any{
				"text":         map[string]any{"type": "string"},
				"country_iso":  map[string]any{"type": "string"},
				"language_iso": map[string]any{"type": "string"},
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
			Description: "List a domain's ranking history (cached; no provider call). Args: domain_id (required), since? (unix seconds), limit? (default 200).",
			InputSchema: schemaObject(map[string]any{
				"domain_id": map[string]any{"type": "integer"},
				"since":     map[string]any{"type": "integer"},
				"limit":     map[string]any{"type": "integer"},
			}, []string{"domain_id"}),
			Handler: a.toolRankingsForDomain},
		{Name: "rankings_for_keyword",
			Description: "List which tracked domains rank for a keyword (cached). Args: keyword_id (required), since? (unix seconds), limit? (default 200).",
			InputSchema: schemaObject(map[string]any{
				"keyword_id": map[string]any{"type": "integer"},
				"since":      map[string]any{"type": "integer"},
				"limit":      map[string]any{"type": "integer"},
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
	}
}

// ─── Models ──────────────────────────────────────────────────────

type Domain struct {
	ID        int64  `json:"id"`
	ProjectID string `json:"project_id"`
	Host      string `json:"host"`
	Label     string `json:"label,omitempty"`
	CreatedAt string `json:"created_at"`
}

type Keyword struct {
	ID          int64  `json:"id"`
	ProjectID   string `json:"project_id"`
	Text        string `json:"text"`
	CountryISO  string `json:"country_iso"`
	LanguageISO string `json:"language_iso"`
	CreatedAt   string `json:"created_at"`
}

type DomainMetrics struct {
	ID                     int64    `json:"id"`
	DomainID               int64    `json:"domain_id"`
	Provider               string   `json:"provider"`
	TS                     int64    `json:"ts"`
	CountryISO             *string  `json:"country_iso,omitempty"`
	AuthorityScore         *int64   `json:"authority_score,omitempty"`
	SpamScore              *float64 `json:"spam_score,omitempty"`
	OrganicTraffic         *int64   `json:"organic_traffic,omitempty"`
	OrganicKeywords        *int64   `json:"organic_keywords,omitempty"`
	PaidTraffic            *int64   `json:"paid_traffic,omitempty"`
	PaidKeywords           *int64   `json:"paid_keywords,omitempty"`
	BacklinksCount         *int64   `json:"backlinks_count,omitempty"`
	ReferringDomainsCount  *int64   `json:"referring_domains_count,omitempty"`
}

type KeywordMetrics struct {
	ID            int64    `json:"id"`
	KeywordID     int64    `json:"keyword_id"`
	Provider      string   `json:"provider"`
	TS            int64    `json:"ts"`
	Volume        *int64   `json:"volume,omitempty"`
	Difficulty    *int64   `json:"difficulty,omitempty"`
	CPCUSD        *float64 `json:"cpc_usd,omitempty"`
	Clicks        *int64   `json:"clicks,omitempty"`
	OrganicCTR    *float64 `json:"organic_ctr,omitempty"`
	IntentJSON    string   `json:"intent_json"`
	SerpFeatJSON  string   `json:"serp_features_json"`
}

// ─── Scope ───────────────────────────────────────────────────────

func projectScope() string {
	return os.Getenv("APTEVA_PROJECT_ID") // '' = global
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

func getDomain(db *sql.DB, pid string, id int64) (*Domain, error) {
	var d Domain
	err := db.QueryRow(
		`SELECT id, project_id, host, label, created_at
		   FROM domains WHERE id = ? AND project_id = ?`, id, pid,
	).Scan(&d.ID, &d.ProjectID, &d.Host, &d.Label, &d.CreatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, fmt.Errorf("domain %d not found", id)
	}
	if err != nil {
		return nil, err
	}
	return &d, nil
}

func listDomains(db *sql.DB, pid string) ([]Domain, error) {
	rows, err := db.Query(
		`SELECT id, project_id, host, label, created_at
		   FROM domains WHERE project_id = ? ORDER BY host`, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Domain{}
	for rows.Next() {
		var d Domain
		if err := rows.Scan(&d.ID, &d.ProjectID, &d.Host, &d.Label, &d.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// latestDomainMetrics returns the most recent domain_metrics row across
// any provider for the given domain. Nil + no error if there are none.
func latestDomainMetrics(db *sql.DB, domainID int64) (*DomainMetrics, error) {
	row := db.QueryRow(
		`SELECT id, domain_id, provider, ts, country_iso,
		        authority_score, spam_score, organic_traffic,
		        organic_keywords, paid_traffic, paid_keywords,
		        backlinks_count, referring_domains_count
		   FROM domain_metrics WHERE domain_id = ?
		   ORDER BY ts DESC LIMIT 1`, domainID)
	var m DomainMetrics
	err := row.Scan(&m.ID, &m.DomainID, &m.Provider, &m.TS, &m.CountryISO,
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
		`SELECT id, project_id, text, country_iso, language_iso, created_at
		   FROM keywords WHERE id = ? AND project_id = ?`, id, pid,
	).Scan(&k.ID, &k.ProjectID, &k.Text, &k.CountryISO, &k.LanguageISO, &k.CreatedAt)
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
			`SELECT id, project_id, text, country_iso, language_iso, created_at
			   FROM keywords WHERE project_id = ?
			   ORDER BY text LIMIT ?`, pid, limit)
	} else {
		rows, err = db.Query(
			`SELECT id, project_id, text, country_iso, language_iso, created_at
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
		if err := rows.Scan(&k.ID, &k.ProjectID, &k.Text, &k.CountryISO, &k.LanguageISO, &k.CreatedAt); err != nil {
			return nil, err
		}
		out = append(out, k)
	}
	return out, rows.Err()
}

func latestKeywordMetrics(db *sql.DB, keywordID int64) (*KeywordMetrics, error) {
	row := db.QueryRow(
		`SELECT id, keyword_id, provider, ts, volume, difficulty,
		        cpc_usd, clicks, organic_ctr, intent_json, serp_features_json
		   FROM keyword_metrics WHERE keyword_id = ?
		   ORDER BY ts DESC LIMIT 1`, keywordID)
	var m KeywordMetrics
	err := row.Scan(&m.ID, &m.KeywordID, &m.Provider, &m.TS,
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
	pid := projectScope()
	db := ctx.AppDB()
	res, err := db.Exec(
		`INSERT INTO domains (project_id, host, label) VALUES (?, ?, ?)
		   ON CONFLICT(project_id, host) DO UPDATE SET label = excluded.label
		   WHERE excluded.label != ''`,
		pid, host, label)
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

func (a *App) toolDomainsList(ctx *sdk.AppCtx, _ map[string]any) (any, error) {
	return listDomains(ctx.AppDB(), projectScope())
}

func (a *App) toolDomainsGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := toInt64(args["id"])
	if id == 0 {
		return nil, errors.New("id required")
	}
	d, err := getDomain(ctx.AppDB(), projectScope(), id)
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
	pid := projectScope()
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
	country := strings.ToUpper(strArg(args, "country_iso", "US"))
	lang := strings.ToLower(strArg(args, "language_iso", "en"))
	pid := projectScope()
	db := ctx.AppDB()
	res, err := db.Exec(
		`INSERT INTO keywords (project_id, text, country_iso, language_iso)
		   VALUES (?, ?, ?, ?)
		   ON CONFLICT(project_id, text, country_iso, language_iso) DO NOTHING`,
		pid, text, country, lang)
	if err != nil {
		return nil, fmt.Errorf("insert keyword: %w", err)
	}
	id, _ := res.LastInsertId()
	if id == 0 {
		row := db.QueryRow(
			`SELECT id FROM keywords
			   WHERE project_id = ? AND text = ? AND country_iso = ? AND language_iso = ?`,
			pid, text, country, lang)
		_ = row.Scan(&id)
	}
	return getKeyword(db, pid, id)
}

func (a *App) toolKeywordsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	limit := int(toInt64(args["limit"]))
	country := strArg(args, "country_iso", "")
	return listKeywords(ctx.AppDB(), projectScope(), country, limit)
}

func (a *App) toolKeywordsGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := toInt64(args["id"])
	if id == 0 {
		return nil, errors.New("id required")
	}
	k, err := getKeyword(ctx.AppDB(), projectScope(), id)
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
	pid := projectScope()
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
	ID                int64  `json:"id"`
	DomainID          int64  `json:"domain_id"`
	KeywordID         int64  `json:"keyword_id"`
	Provider          string `json:"provider"`
	TS                int64  `json:"ts"`
	Rank              *int64 `json:"rank,omitempty"`
	RankURL           string `json:"rank_url,omitempty"`
	Device            string `json:"device"`
	SerpFeaturesJSON  string `json:"serp_features_json"`
}

type Backlink struct {
	ID              int64   `json:"id"`
	DomainID        int64   `json:"domain_id"`
	Provider        string  `json:"provider"`
	SourceURL       string  `json:"source_url"`
	DestURL         string  `json:"dest_url"`
	Anchor          string  `json:"anchor"`
	IsDofollow      *int64  `json:"is_dofollow,omitempty"`
	IsNofollow      *int64  `json:"is_nofollow,omitempty"`
	IsUGC           *int64  `json:"is_ugc,omitempty"`
	IsSponsored     *int64  `json:"is_sponsored,omitempty"`
	SourceAuthority *int64  `json:"source_authority,omitempty"`
	FirstSeen       *int64  `json:"first_seen,omitempty"`
	LastSeen        *int64  `json:"last_seen,omitempty"`
	IsLost          int64   `json:"is_lost"`
}

type VolumeHistoryRow struct {
	Provider string `json:"provider"`
	Year     int    `json:"year"`
	Month    int    `json:"month"`
	Volume   int64  `json:"volume"`
}

func (a *App) toolRankingsForDomain(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	id := toInt64(args["domain_id"])
	if id == 0 {
		return nil, errors.New("domain_id required")
	}
	if _, err := getDomain(ctx.AppDB(), projectScope(), id); err != nil {
		return nil, err
	}
	since := toInt64(args["since"])
	limit := int(toInt64(args["limit"]))
	if limit <= 0 {
		limit = 200
	}
	rows, err := ctx.AppDB().Query(
		`SELECT id, domain_id, keyword_id, provider, ts, rank, rank_url,
		        device, serp_features_json
		   FROM rankings
		   WHERE domain_id = ? AND ts >= ?
		   ORDER BY ts DESC LIMIT ?`, id, since, limit)
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
	if _, err := getKeyword(ctx.AppDB(), projectScope(), id); err != nil {
		return nil, err
	}
	since := toInt64(args["since"])
	limit := int(toInt64(args["limit"]))
	if limit <= 0 {
		limit = 200
	}
	rows, err := ctx.AppDB().Query(
		`SELECT id, domain_id, keyword_id, provider, ts, rank, rank_url,
		        device, serp_features_json
		   FROM rankings
		   WHERE keyword_id = ? AND ts >= ?
		   ORDER BY ts DESC LIMIT ?`, id, since, limit)
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
		if err := rows.Scan(&r.ID, &r.DomainID, &r.KeywordID, &r.Provider, &r.TS,
			&r.Rank, &r.RankURL, &r.Device, &r.SerpFeaturesJSON); err != nil {
			return nil, err
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
	if _, err := getDomain(ctx.AppDB(), projectScope(), id); err != nil {
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
	if _, err := getKeyword(ctx.AppDB(), projectScope(), id); err != nil {
		return nil, err
	}
	rows, err := ctx.AppDB().Query(
		`SELECT provider, year, month, volume
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
		if err := rows.Scan(&v.Provider, &v.Year, &v.Month, &v.Volume); err != nil {
			return nil, err
		}
		out = append(out, v)
	}
	return out, rows.Err()
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
	switch {
	case len(parts) == 2 && parts[1] == "refresh":
		out, err := refreshDomain(ctx, id)
		writeJSONOrErr(w, out, err)
	case len(parts) == 3 && parts[1] == "backlinks" && parts[2] == "refresh":
		out, err := refreshBacklinks(ctx, id)
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
	out, err := refreshKeyword(mustCtx(r), id)
	writeJSONOrErr(w, out, err)
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

func refreshDomain(ctx *sdk.AppCtx, domainID int64) (any, error) {
	d, err := getDomain(ctx.AppDB(), projectScope(), domainID)
	if err != nil {
		return nil, err
	}
	slug, _, err := boundProvider(ctx)
	if err != nil {
		return nil, err
	}
	switch slug {
	case "dataforseo":
		return refreshDomainViaDataForSEO(ctx, d)
	default:
		return nil, fmt.Errorf("provider %q not yet wired (v0.2 supports dataforseo only)", slug)
	}
}

func refreshKeyword(ctx *sdk.AppCtx, keywordID int64) (any, error) {
	k, err := getKeyword(ctx.AppDB(), projectScope(), keywordID)
	if err != nil {
		return nil, err
	}
	slug, _, err := boundProvider(ctx)
	if err != nil {
		return nil, err
	}
	switch slug {
	case "dataforseo":
		return refreshKeywordViaDataForSEO(ctx, k)
	default:
		return nil, fmt.Errorf("provider %q not yet wired (v0.2 supports dataforseo only)", slug)
	}
}

func refreshBacklinks(ctx *sdk.AppCtx, domainID int64) (any, error) {
	d, err := getDomain(ctx.AppDB(), projectScope(), domainID)
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
