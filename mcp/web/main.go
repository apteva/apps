// Web v0.2.4 - browser-backed web intelligence and reusable extractors.
//
// The app requires computer for session lifecycle, rendered extraction, and
// screenshots. It opens a browser before search/extract/crawl/map/research page
// visits, prefers live browser DOM extraction when available, and falls back to
// HTTP retrieval when the active backend cannot expose rendered content.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	_ "embed"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/draw"
	_ "image/jpeg"
	"image/png"
	"io"
	"math"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"os"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	sdk "github.com/apteva/app-sdk"
	"golang.org/x/net/html"
	"golang.org/x/net/publicsuffix"
	_ "modernc.org/sqlite"
)

//go:embed apteva.yaml
var manifestYAML []byte

const (
	defaultHTTPTimeout      = 20 * time.Second
	defaultMaxChars         = 20000
	maxFetchBytes           = 5 * 1024 * 1024
	maxCachedResponseBytes  = 4 * 1024 * 1024
	maxCrawlPages           = 50
	defaultSearchLimit      = 10
	maxSearchLimit          = 25
	maxResearchQueries      = 8
	defaultHistoryRetention = 90
	defaultSearchMaxAge     = 15 * time.Minute
	defaultSearchTTL        = time.Hour
	defaultExtractMaxAge    = 24 * time.Hour
	defaultExtractTTL       = 24 * time.Hour
	defaultResearchMaxAge   = time.Hour
	defaultResearchTTL      = time.Hour
)

var blockedNetworkPrefixes = []netip.Prefix{
	netip.MustParsePrefix("0.0.0.0/8"),
	netip.MustParsePrefix("10.0.0.0/8"),
	netip.MustParsePrefix("100.64.0.0/10"),
	netip.MustParsePrefix("127.0.0.0/8"),
	netip.MustParsePrefix("169.254.0.0/16"),
	netip.MustParsePrefix("172.16.0.0/12"),
	netip.MustParsePrefix("192.0.0.0/24"),
	netip.MustParsePrefix("192.0.2.0/24"),
	netip.MustParsePrefix("192.168.0.0/16"),
	netip.MustParsePrefix("198.18.0.0/15"),
	netip.MustParsePrefix("198.51.100.0/24"),
	netip.MustParsePrefix("203.0.113.0/24"),
	netip.MustParsePrefix("224.0.0.0/4"),
	netip.MustParsePrefix("240.0.0.0/4"),
	netip.MustParsePrefix("::/128"),
	netip.MustParsePrefix("::1/128"),
	netip.MustParsePrefix("fc00::/7"),
	netip.MustParsePrefix("fe80::/10"),
	netip.MustParsePrefix("ff00::/8"),
	netip.MustParsePrefix("2001:db8::/32"),
}

type App struct{}

var globalCtx *sdk.AppCtx

func (a *App) Manifest() sdk.Manifest {
	m, err := sdk.ParseManifest(manifestYAML)
	if err != nil {
		panic("invalid embedded manifest: " + err.Error())
	}
	return *m
}

func (a *App) OnMount(ctx *sdk.AppCtx) error {
	if ctx.AppDB() == nil {
		return errors.New("web requires a db block")
	}
	globalCtx = ctx
	if err := recoverInterruptedRuns(ctx); err != nil {
		ctx.Logger().Warn("web interrupted-run recovery failed", "err", err.Error())
	}
	if err := pruneCache(ctx); err != nil {
		ctx.Logger().Warn("web cache prune failed", "err", err.Error())
	}
	if err := pruneHistory(ctx); err != nil {
		ctx.Logger().Warn("web history prune failed", "err", err.Error())
	}
	ctx.Logger().Info("web mounted", "scope_project_id", os.Getenv("APTEVA_PROJECT_ID"))
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error    { return nil }
func (a *App) Channels() []sdk.ChannelFactory { return nil }
func (a *App) Workers() []sdk.Worker {
	return []sdk.Worker{{
		Name:     "extractor-runner",
		Schedule: "@every 2s",
		Run:      a.runExtractorWorker,
	}}
}
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Method: http.MethodGet, Pattern: "/runs", Handler: a.handleRuns},
		{Method: http.MethodGet, Pattern: "/runs/{id}", Handler: a.handleRunItem},
		{Method: http.MethodPost, Pattern: "/runs/{id}/cancel", Handler: a.handleRunCancel},
		{Method: http.MethodPost, Pattern: "/runs/{id}/retry", Handler: a.handleRunRetry},
		{Method: http.MethodGet, Pattern: "/extractors", Handler: a.handleExtractors},
		{Method: http.MethodPost, Pattern: "/extractors", Handler: a.handleExtractorSave},
		{Method: http.MethodDelete, Pattern: "/extractors/{id}", Handler: a.handleExtractorDelete},
		{Method: http.MethodPost, Pattern: "/extractors/run", Handler: a.handleExtractorRun},
		{Method: http.MethodGet, Pattern: "/schedules", Handler: a.handleExtractorSchedules},
		{Method: http.MethodPost, Pattern: "/schedules", Handler: a.handleExtractorSchedule},
		{Method: http.MethodPost, Pattern: "/schedules/{id}/run", Handler: a.handleExtractorScheduleRunNow},
		{Method: http.MethodPost, Pattern: "/schedules/{id}/cancel", Handler: a.handleExtractorUnschedule},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	tools := []sdk.Tool{
		{
			Name:        "web_search",
			Description: "Browser-backed web search. Args: query, limit?, engine? (google|duckduckgo, default google), backend?, viewport?, visit_top? bool. Returns normalized JSON results.",
			InputSchema: schemaObject(map[string]any{
				"query":     map[string]any{"type": "string"},
				"limit":     map[string]any{"type": "integer"},
				"engine":    map[string]any{"type": "string", "enum": []string{"google", "duckduckgo"}},
				"backend":   map[string]any{"type": "string"},
				"viewport":  viewportSchema(),
				"visit_top": map[string]any{"type": "boolean"},
				"store":     map[string]any{"type": "boolean"},
				"cache":     cacheModeSchema(),
				"max_age":   cacheSecondsSchema("Maximum accepted cached result age in seconds. Default 900 for search."),
				"cache_ttl": cacheSecondsSchema("How long to retain newly fetched results in seconds. Default 3600 for search."),
			}, []string{"query"}),
			Handler: a.toolSearch,
		},
		{
			Name:        "web_extract",
			Description: "Open a URL in a browser session, extract readable text, markdown, metadata, structured_data, links, images, and optional artifact storage. Args: url, formats?, backend?, viewport?, max_chars?, store?, snapshot?, visibility? (private|signed|public; applies to stored snapshots and defaults to private).",
			InputSchema: schemaObject(map[string]any{
				"url":        map[string]any{"type": "string"},
				"formats":    map[string]any{"type": "array", "items": map[string]any{"type": "string", "enum": []string{"text", "markdown", "html", "metadata", "structured_data", "json", "links", "images"}}},
				"backend":    map[string]any{"type": "string"},
				"viewport":   viewportSchema(),
				"max_chars":  map[string]any{"type": "integer"},
				"store":      map[string]any{"type": "boolean"},
				"snapshot":   map[string]any{"type": "boolean"},
				"visibility": snapshotVisibilitySchema(),
				"cache":      cacheModeSchema(),
				"max_age":    cacheSecondsSchema("Maximum accepted cached page age in seconds. Default 86400 for extraction."),
				"cache_ttl":  cacheSecondsSchema("How long to retain newly extracted page data in seconds. Default 86400."),
			}, []string{"url"}),
			Handler: a.toolExtract,
		},
		{
			Name:        "web_crawl",
			Description: "Browser-backed bounded crawl from one or more seed URLs. Args: url or urls, max_pages?, max_depth?, same_host?, backend?, store?.",
			InputSchema: schemaObject(map[string]any{
				"url":       map[string]any{"type": "string"},
				"urls":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"max_pages": map[string]any{"type": "integer"},
				"max_depth": map[string]any{"type": "integer"},
				"same_host": map[string]any{"type": "boolean"},
				"backend":   map[string]any{"type": "string"},
				"viewport":  viewportSchema(),
				"store":     map[string]any{"type": "boolean"},
				"max_chars": map[string]any{"type": "integer"},
				"cache":     cacheModeSchema(),
				"max_age":   cacheSecondsSchema("Maximum accepted cached crawl age in seconds. Default 86400."),
				"cache_ttl": cacheSecondsSchema("How long to retain newly crawled data in seconds. Default 86400."),
			}, nil),
			Handler: a.toolCrawl,
		},
		{
			Name:        "web_map",
			Description: "Fast site map discovery from one or more seed URLs. Args: url or urls, max_pages?, max_depth?, same_host?, backend?, store?.",
			InputSchema: schemaObject(map[string]any{
				"url":       map[string]any{"type": "string"},
				"urls":      map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"max_pages": map[string]any{"type": "integer"},
				"max_depth": map[string]any{"type": "integer"},
				"same_host": map[string]any{"type": "boolean"},
				"backend":   map[string]any{"type": "string"},
				"viewport":  viewportSchema(),
				"store":     map[string]any{"type": "boolean"},
				"cache":     cacheModeSchema(),
				"max_age":   cacheSecondsSchema("Maximum accepted cached map age in seconds. Default 86400."),
				"cache_ttl": cacheSecondsSchema("How long to retain newly mapped data in seconds. Default 86400."),
			}, nil),
			Handler: a.toolMap,
		},
		{
			Name:        "web_research",
			Description: "Multi-step browser-backed research. Args: question, queries?, max_results?, max_sources?, backend?, snapshots?, store?, visibility? (private|signed|public; applies to stored snapshots and defaults to private). Returns extractive report JSON with citations and artifacts.",
			InputSchema: schemaObject(map[string]any{
				"question":    map[string]any{"type": "string"},
				"queries":     map[string]any{"type": "array", "maxItems": maxResearchQueries, "items": map[string]any{"type": "string"}},
				"max_results": map[string]any{"type": "integer"},
				"max_sources": map[string]any{"type": "integer"},
				"backend":     map[string]any{"type": "string"},
				"viewport":    viewportSchema(),
				"snapshots":   map[string]any{"type": "boolean"},
				"store":       map[string]any{"type": "boolean"},
				"visibility":  snapshotVisibilitySchema(),
				"cache":       cacheModeSchema(),
				"max_age":     cacheSecondsSchema("Maximum accepted cached research age in seconds. Default 3600."),
				"cache_ttl":   cacheSecondsSchema("How long to retain newly generated research in seconds. Default 3600."),
			}, []string{"question"}),
			Handler: a.toolResearch,
		},
		{
			Name:        "web_snapshot",
			Description: "Capture visual evidence for a URL or existing computer session. Args: url? or session_id?, backend?, viewport?, label?, store?, visibility? (private|signed|public; default private), mode? (viewport|smart), query?, max_shots?, crop?, cookie_handling? (auto|off).",
			InputSchema: schemaObject(map[string]any{
				"url":             map[string]any{"type": "string"},
				"session_id":      map[string]any{"type": "string"},
				"backend":         map[string]any{"type": "string"},
				"viewport":        viewportSchema(),
				"label":           map[string]any{"type": "string"},
				"mode":            map[string]any{"type": "string", "enum": []string{"viewport", "smart"}},
				"query":           map[string]any{"type": "string"},
				"max_shots":       map[string]any{"type": "integer"},
				"crop":            map[string]any{"type": "boolean"},
				"cookie_handling": map[string]any{"type": "string", "enum": []string{"auto", "off"}},
				"store":           map[string]any{"type": "boolean"},
				"visibility":      snapshotVisibilitySchema(),
				"cache":           cacheModeSchema(),
				"max_age":         cacheSecondsSchema("Maximum accepted cached snapshot age in seconds. Snapshot caching is disabled unless max_age is set."),
				"cache_ttl":       cacheSecondsSchema("How long to retain newly captured snapshots in seconds. Defaults to max_age when set."),
			}, nil),
			Handler: a.toolSnapshot,
		},
	}
	return append(tools, a.extractorTools()...)
}

type browserSession struct {
	SessionID  string            `json:"session_id"`
	Backend    string            `json:"backend"`
	CurrentURL string            `json:"current_url"`
	DebugURL   string            `json:"debug_url,omitempty"`
	StreamURL  string            `json:"stream_url,omitempty"`
	Width      int               `json:"width"`
	Height     int               `json:"height"`
	Proxy      browserProxyState `json:"proxy"`
}

type browserProxyState struct {
	Mode        string `json:"mode"`
	Provider    string `json:"provider,omitempty"`
	ProfileID   string `json:"profile_id,omitempty"`
	ProfileName string `json:"profile_name,omitempty"`
	Country     string `json:"country,omitempty"`
	StickyScope string `json:"sticky_scope,omitempty"`
}

type browserExtractResult struct {
	SessionID         string          `json:"session_id"`
	Backend           string          `json:"backend"`
	CurrentURL        string          `json:"current_url"`
	URL               string          `json:"url"`
	Title             string          `json:"title"`
	Description       string          `json:"description"`
	Text              string          `json:"text"`
	Markdown          string          `json:"markdown"`
	HTML              string          `json:"html"`
	Links             []linkInfo      `json:"links"`
	Images            []string        `json:"images"`
	Regions           []browserRegion `json:"regions"`
	Metadata          map[string]any  `json:"metadata"`
	StructuredData    map[string]any  `json:"structured_data"`
	Rendered          bool            `json:"rendered"`
	ExtractionBackend string          `json:"extraction_backend"`
	Width             int             `json:"width"`
	Height            int             `json:"height"`
}

type browserRect struct {
	X      float64 `json:"x"`
	Y      float64 `json:"y"`
	Width  float64 `json:"width"`
	Height float64 `json:"height"`
}

type browserRegion struct {
	ID              string      `json:"id"`
	Tag             string      `json:"tag,omitempty"`
	Role            string      `json:"role,omitempty"`
	Selector        string      `json:"selector,omitempty"`
	Heading         string      `json:"heading,omitempty"`
	Text            string      `json:"text,omitempty"`
	Rect            browserRect `json:"rect"`
	ViewportRect    browserRect `json:"viewport_rect"`
	CoordinateFrame string      `json:"coordinate_frame"`
	Visible         bool        `json:"visible"`
	LinkCount       int         `json:"link_count,omitempty"`
	ImageCount      int         `json:"image_count,omitempty"`
}

type browserScreenshot struct {
	PNGB64     string `json:"png_b64"`
	CurrentURL string `json:"current_url"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
}

type computerSOMScreenshot struct {
	CurrentURL string            `json:"current_url"`
	Width      int               `json:"width"`
	Height     int               `json:"height"`
	SOM        []setOfMarkTarget `json:"som"`
}

type setOfMarkTarget struct {
	Label int    `json:"label"`
	X     int    `json:"x"`
	Y     int    `json:"y"`
	W     int    `json:"w"`
	H     int    `json:"h"`
	Tag   string `json:"tag"`
	Role  string `json:"role"`
	Text  string `json:"text"`
	Type  string `json:"type"`
}

type searchResult struct {
	Title      string `json:"title"`
	URL        string `json:"url"`
	Snippet    string `json:"snippet,omitempty"`
	Source     string `json:"source"`
	Rank       int    `json:"rank"`
	FetchedAt  string `json:"fetched_at"`
	Confidence string `json:"confidence"`
}

type pageDoc struct {
	URL               string           `json:"url"`
	FinalURL          string           `json:"final_url"`
	Title             string           `json:"title,omitempty"`
	Description       string           `json:"description,omitempty"`
	Text              string           `json:"text,omitempty"`
	Markdown          string           `json:"markdown,omitempty"`
	HTML              string           `json:"html,omitempty"`
	Links             []linkInfo       `json:"links,omitempty"`
	Images            []string         `json:"images,omitempty"`
	Metadata          map[string]any   `json:"metadata,omitempty"`
	StructuredData    map[string]any   `json:"structured_data,omitempty"`
	Status            int              `json:"status"`
	ContentType       string           `json:"content_type,omitempty"`
	Bytes             int              `json:"bytes"`
	Truncated         bool             `json:"truncated"`
	ExtractionBackend string           `json:"extraction_backend"`
	Browser           *browserSession  `json:"browser,omitempty"`
	Artifact          *artifactSummary `json:"artifact,omitempty"`
	Snapshot          any              `json:"snapshot,omitempty"`
	Error             string           `json:"error,omitempty"`
}

type linkInfo struct {
	URL  string `json:"url"`
	Text string `json:"text,omitempty"`
}

type artifactSummary struct {
	ID        int64  `json:"id"`
	StorageID int64  `json:"storage_id,omitempty"`
	URL       string `json:"url,omitempty"`
}

type crawlNode struct {
	URL   string
	Depth int
}

type crawlEdge struct {
	From string `json:"from"`
	To   string `json:"to"`
	Text string `json:"text,omitempty"`
}

type cachePolicy struct {
	Kind           string
	Mode           string
	Key            string
	RequestJSON    string
	MaxAge         time.Duration
	TTL            time.Duration
	Read           bool
	Write          bool
	ForceOnly      bool
	ExplicitMaxAge bool
	Reason         string
}

type cacheInfo struct {
	Hit             bool   `json:"hit"`
	Mode            string `json:"mode"`
	CacheKey        string `json:"cache_key"`
	AgeSeconds      int64  `json:"age_seconds,omitempty"`
	MaxAgeSeconds   int64  `json:"max_age_seconds,omitempty"`
	CacheTTLSeconds int64  `json:"cache_ttl_seconds,omitempty"`
	CachedAt        string `json:"cached_at,omitempty"`
	ExpiresAt       string `json:"expires_at,omitempty"`
	Stored          bool   `json:"stored,omitempty"`
	Reason          string `json:"reason,omitempty"`
}

func (a *App) toolSearch(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	query := strings.TrimSpace(stringArg(args, "query"))
	if query == "" {
		return nil, errors.New("query required")
	}
	engine := firstNonEmpty(stringArg(args, "engine"), configString(ctx, "default_search_engine"), "google")
	if engine != "google" && engine != "duckduckgo" {
		return nil, fmt.Errorf("unsupported engine %q", engine)
	}
	var runID int64
	if !boolArg(args, "_internal") {
		var err error
		runID, err = startRun(ctx, "search", args)
		if err != nil {
			return nil, fmt.Errorf("start search run: %w", err)
		}
	}
	defer failRunOnPanic(ctx, runID)

	limit := boundedInt(intArg(args, "limit"), defaultSearchLimit, 1, maxSearchLimit)
	policy, err := newCachePolicy("search", args)
	if err != nil {
		completeRun(ctx, runID, "failed", nil, err)
		return nil, err
	}
	if cached, ok, err := loadCachedResponse(ctx, policy); ok {
		stripCachedTransientFields(cached)
		if err := a.applyResponseEffects(ctx, runID, "search", args, cached); err != nil {
			completeRun(ctx, runID, "failed", cached, err)
			return cached, err
		}
		completeRun(ctx, runID, "completed", cached, nil)
		return cached, nil
	} else if err != nil {
		completeRun(ctx, runID, "failed", nil, err)
		return nil, err
	}
	searchURL := searchEngineURL(engine, query)
	browser, browserErr := a.openBrowser(ctx, searchURL, args)
	if browserErr != nil {
		completeRun(ctx, runID, "failed", nil, browserErr)
		return nil, browserErr
	}
	defer a.closeBrowser(ctx, browser.SessionID)

	extracted, err := a.extractBrowserDOM(ctx, browser.SessionID, mapMerge(args, map[string]any{
		"formats": []string{"text", "html", "links", "metadata"},
	}), true)
	if err != nil {
		completeRun(ctx, runID, "failed", nil, err)
		return nil, err
	}
	results := parseSearchResults(engine, extracted, limit)
	now := time.Now().UTC().Format(time.RFC3339)
	for i := range results {
		results[i].FetchedAt = now
	}

	out := map[string]any{
		"query":              query,
		"engine":             engine,
		"search_url":         searchURL,
		"browser":            browser,
		"results":            results,
		"count":              len(results),
		"extraction_backend": firstNonEmpty(extracted.ExtractionBackend, "browser_dom"),
		"page": map[string]any{
			"current_url": extracted.CurrentURL,
			"title":       extracted.Title,
			"text":        truncateString(extracted.Text, 1200),
			"links_count": len(extracted.Links),
		},
	}
	if searchBlocked := detectSearchBlocked(engine, extracted); searchBlocked != "" {
		out["blocked"] = true
		out["error"] = searchBlocked
	}
	if boolArg(args, "visit_top") && len(results) > 0 && out["blocked"] != true {
		topArgs := mapMerge(args, map[string]any{"store": false, "snapshot": false})
		topPage := a.extractURL(ctx, runID, results[0].URL, topArgs, true)
		out["top_page"] = topPage
		if topPage.Description != "" {
			results[0].Snippet = truncateString(topPage.Description, 500)
		} else if topPage.Text != "" {
			results[0].Snippet = truncateString(topPage.Text, 500)
		}
		out["results"] = results
	}
	applyCacheAfterFetch(ctx, policy, out)
	if err := a.applyResponseEffects(ctx, runID, "search", args, out); err != nil {
		completeRun(ctx, runID, "failed", out, err)
		return out, err
	}
	completeRun(ctx, runID, "completed", out, nil)
	return out, nil
}

func (a *App) toolExtract(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	target := strings.TrimSpace(stringArg(args, "url"))
	if target == "" {
		return nil, errors.New("url required")
	}
	if boolArg(args, "snapshot") {
		if _, err := snapshotVisibility(args); err != nil {
			return nil, err
		}
	}
	runID, err := startRun(ctx, "extract", args)
	if err != nil {
		return nil, fmt.Errorf("start extract run: %w", err)
	}
	defer failRunOnPanic(ctx, runID)

	policy, err := newCachePolicy("extract", args)
	if err != nil {
		completeRun(ctx, runID, "failed", nil, err)
		return nil, err
	}
	if cached, ok, err := loadCachedResponse(ctx, policy); ok {
		stripCachedTransientFields(cached)
		if err := a.applyResponseEffects(ctx, runID, "extract", args, cached); err != nil {
			completeRun(ctx, runID, "failed", cached, err)
			return cached, err
		}
		completeRun(ctx, runID, "completed", cached, nil)
		return cached, nil
	} else if err != nil {
		completeRun(ctx, runID, "failed", nil, err)
		return nil, err
	}
	doc := a.extractURL(ctx, runID, target, mapMerge(args, map[string]any{
		"store":           false,
		"_snapshot_store": boolArgDefault(args, "store", storeDefault(ctx)),
	}), true)
	out := map[string]any{"page": doc}
	applyCacheAfterFetch(ctx, policy, out)
	if doc.Error != "" {
		err := errors.New(doc.Error)
		completeRun(ctx, runID, "failed", out, err)
		return out, err
	}
	if err := a.applyResponseEffects(ctx, runID, "extract", args, out); err != nil {
		completeRun(ctx, runID, "failed", out, err)
		return out, err
	}
	completeRun(ctx, runID, "completed", out, nil)
	return out, nil
}

func (a *App) toolCrawl(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	seeds := seedURLs(args)
	if len(seeds) == 0 {
		return nil, errors.New("url or urls required")
	}
	if err := validateSeedURLs(seeds); err != nil {
		return nil, err
	}
	runID, err := startRun(ctx, "crawl", args)
	if err != nil {
		return nil, fmt.Errorf("start crawl run: %w", err)
	}
	defer failRunOnPanic(ctx, runID)

	policy, err := newCachePolicy("crawl", args)
	if err != nil {
		completeRun(ctx, runID, "failed", nil, err)
		return nil, err
	}
	if cached, ok, err := loadCachedResponse(ctx, policy); ok {
		stripCachedTransientFields(cached)
		if err := a.applyResponseEffects(ctx, runID, "crawl", args, cached); err != nil {
			completeRun(ctx, runID, "failed", cached, err)
			return cached, err
		}
		completeRun(ctx, runID, "completed", cached, nil)
		return cached, nil
	} else if err != nil {
		completeRun(ctx, runID, "failed", nil, err)
		return nil, err
	}
	configuredMaxPages := boundedInt(configInt(ctx, "max_pages"), 25, 1, maxCrawlPages)
	maxPages := boundedInt(intArg(args, "max_pages"), configuredMaxPages, 1, configuredMaxPages)
	maxDepth := boundedInt(intArg(args, "max_depth"), 1, 0, 5)
	sameHost := boolArgDefault(args, "same_host", true)
	pages, edges := a.crawl(ctx, runID, seeds, args, maxPages, maxDepth, sameHost, true)
	out := map[string]any{
		"seeds":              seeds,
		"pages":              pages,
		"edges":              edges,
		"count":              len(pages),
		"extraction_backend": aggregateExtractionBackend(pages),
	}
	applyCacheAfterFetch(ctx, policy, out)
	if err := a.applyResponseEffects(ctx, runID, "crawl", args, out); err != nil {
		completeRun(ctx, runID, "failed", out, err)
		return out, err
	}
	completeRun(ctx, runID, "completed", out, nil)
	return out, nil
}

func (a *App) toolMap(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	seeds := seedURLs(args)
	if len(seeds) == 0 {
		return nil, errors.New("url or urls required")
	}
	if err := validateSeedURLs(seeds); err != nil {
		return nil, err
	}
	runID, err := startRun(ctx, "map", args)
	if err != nil {
		return nil, fmt.Errorf("start map run: %w", err)
	}
	defer failRunOnPanic(ctx, runID)

	policy, err := newCachePolicy("map", args)
	if err != nil {
		completeRun(ctx, runID, "failed", nil, err)
		return nil, err
	}
	if cached, ok, err := loadCachedResponse(ctx, policy); ok {
		stripCachedTransientFields(cached)
		if err := a.applyResponseEffects(ctx, runID, "map", args, cached); err != nil {
			completeRun(ctx, runID, "failed", cached, err)
			return cached, err
		}
		completeRun(ctx, runID, "completed", cached, nil)
		return cached, nil
	} else if err != nil {
		completeRun(ctx, runID, "failed", nil, err)
		return nil, err
	}
	configuredMaxPages := boundedInt(configInt(ctx, "max_pages"), 25, 1, maxCrawlPages)
	maxPages := boundedInt(intArg(args, "max_pages"), configuredMaxPages, 1, configuredMaxPages)
	maxDepth := boundedInt(intArg(args, "max_depth"), 2, 0, 5)
	sameHost := boolArgDefault(args, "same_host", true)
	pages, edges := a.crawl(ctx, runID, seeds, args, maxPages, maxDepth, sameHost, false)
	mapPages := make([]map[string]any, 0, len(pages))
	for _, p := range pages {
		mapPages = append(mapPages, map[string]any{
			"url":        p.URL,
			"final_url":  p.FinalURL,
			"title":      p.Title,
			"status":     p.Status,
			"link_count": len(p.Links),
			"browser":    p.Browser,
			"error":      p.Error,
		})
	}
	out := map[string]any{"seeds": seeds, "pages": mapPages, "edges": edges, "count": len(mapPages), "extraction_backend": aggregateExtractionBackend(pages)}
	applyCacheAfterFetch(ctx, policy, out)
	if err := a.applyResponseEffects(ctx, runID, "map", args, out); err != nil {
		completeRun(ctx, runID, "failed", out, err)
		return out, err
	}
	completeRun(ctx, runID, "completed", out, nil)
	return out, nil
}

func (a *App) toolResearch(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	question := strings.TrimSpace(stringArg(args, "question"))
	if question == "" {
		return nil, errors.New("question required")
	}
	if boolArg(args, "snapshots") {
		if _, err := snapshotVisibility(args); err != nil {
			return nil, err
		}
	}
	runID, err := startRun(ctx, "research", args)
	if err != nil {
		return nil, fmt.Errorf("start research run: %w", err)
	}
	defer failRunOnPanic(ctx, runID)

	policy, err := newCachePolicy("research", args)
	if err != nil {
		completeRun(ctx, runID, "failed", nil, err)
		return nil, err
	}
	if cached, ok, err := loadCachedResponse(ctx, policy); ok {
		stripCachedTransientFields(cached)
		if err := a.applyResponseEffects(ctx, runID, "research", args, cached); err != nil {
			completeRun(ctx, runID, "failed", cached, err)
			return cached, err
		}
		completeRun(ctx, runID, "completed", cached, nil)
		return cached, nil
	} else if err != nil {
		completeRun(ctx, runID, "failed", nil, err)
		return nil, err
	}
	queries := dedupeStrings(stringSliceArg(args, "queries"), maxResearchQueries)
	if len(queries) == 0 {
		queries = []string{question}
	}
	maxResults := boundedInt(intArg(args, "max_results"), 8, 1, maxSearchLimit)
	maxSources := boundedInt(intArg(args, "max_sources"), 5, 1, 12)

	searchBatches := make([][]searchResult, len(queries))
	searchJobs := make(chan int, len(queries))
	var searchWG sync.WaitGroup
	workerCount := minInt(3, len(queries))
	for range workerCount {
		searchWG.Add(1)
		go func() {
			defer searchWG.Done()
			for i := range searchJobs {
				q := queries[i]
				searchArgs := map[string]any{
					"query":     q,
					"limit":     maxResults,
					"store":     false,
					"cache":     nestedSearchCacheMode(args),
					"visit_top": false,
					"_internal": true,
				}
				for _, key := range []string{"engine", "backend", "viewport", "context_id", "persist", "timeout", "proxy", "proxy_country"} {
					if value, ok := args[key]; ok {
						searchArgs[key] = value
					}
				}
				searchOut, searchErr := a.toolSearch(ctx, searchArgs)
				if searchErr != nil {
					ctx.Logger().Warn("research search failed", "query", q, "err", searchErr.Error())
					continue
				}
				searchBatches[i] = searchResultsFromOutput(searchOut)
			}
		}()
	}
	for i := range queries {
		searchJobs <- i
	}
	close(searchJobs)
	searchWG.Wait()

	allResults := make([]searchResult, 0, len(queries)*maxResults)
	seen := map[string]bool{}
	for _, batch := range searchBatches {
		for _, r := range batch {
			if !seen[r.URL] {
				seen[r.URL] = true
				allResults = append(allResults, r)
			}
		}
	}
	if len(allResults) > maxSources {
		allResults = allResults[:maxSources]
	}

	sources := make([]pageDoc, len(allResults))
	sourceSem := make(chan struct{}, 3)
	var sourceWG sync.WaitGroup
	for i, r := range allResults {
		sourceWG.Add(1)
		go func(i int, r searchResult) {
			defer sourceWG.Done()
			sourceSem <- struct{}{}
			defer func() { <-sourceSem }()
			extractArgs := mapMerge(args, map[string]any{
				"store":           false,
				"_snapshot_store": boolArgDefault(args, "store", storeDefault(ctx)),
				"snapshot":        boolArg(args, "snapshots"),
				"label":           r.Title,
			})
			sources[i] = a.extractURL(ctx, runID, r.URL, extractArgs, true)
		}(i, r)
	}
	sourceWG.Wait()

	citations := make([]map[string]any, 0, len(allResults))
	for i, r := range allResults {
		doc := sources[i]
		excerpt := bestExcerpt(doc.Text, question)
		citations = append(citations, map[string]any{
			"id":      i + 1,
			"url":     r.URL,
			"title":   firstNonEmpty(doc.Title, r.Title),
			"excerpt": excerpt,
		})
	}

	answer := synthesizeExtractiveAnswer(question, citations)
	out := map[string]any{
		"question":           question,
		"queries":            queries,
		"answer":             answer,
		"citations":          citations,
		"sources":            sources,
		"search_results":     allResults,
		"confidence":         confidenceForSources(sources),
		"open_questions":     researchGaps(sources),
		"extraction_backend": aggregateExtractionBackend(sources),
	}
	applyCacheAfterFetch(ctx, policy, out)
	if err := a.applyResponseEffects(ctx, runID, "research", args, out); err != nil {
		completeRun(ctx, runID, "failed", out, err)
		return out, err
	}
	completeRun(ctx, runID, "completed", out, nil)
	return out, nil
}

func (a *App) toolSnapshot(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if _, err := snapshotVisibility(args); err != nil {
		return nil, err
	}
	runID, runErr := startRun(ctx, "snapshot", args)
	if runErr != nil {
		return nil, fmt.Errorf("start snapshot run: %w", runErr)
	}
	defer failRunOnPanic(ctx, runID)
	policy, policyErr := newCachePolicy("snapshot", args)
	if policyErr != nil {
		completeRun(ctx, runID, "failed", nil, policyErr)
		return nil, policyErr
	}
	if cached, ok, err := loadCachedResponse(ctx, policy); ok {
		completeRun(ctx, runID, "completed", cached, nil)
		return cached, nil
	} else if err != nil {
		completeRun(ctx, runID, "failed", nil, err)
		return nil, err
	}
	out, err := a.snapshot(ctx, runID, args)
	if out != nil {
		applyCacheAfterFetch(ctx, policy, out)
	}
	status := "completed"
	if err != nil {
		status = "failed"
	}
	completeRun(ctx, runID, status, out, err)
	return out, err
}

func (a *App) extractURL(ctx *sdk.AppCtx, runID int64, target string, args map[string]any, includeText bool) pageDoc {
	doc := pageDoc{URL: target, FinalURL: target, ExtractionBackend: "browser_dom"}
	if err := validateHTTPURL(target); err != nil {
		doc.Error = err.Error()
		return doc
	}
	browser, err := a.openBrowser(ctx, target, args)
	if err != nil {
		doc.Error = "computer.browser_open: " + err.Error()
		return doc
	}
	doc.Browser = browser
	defer a.closeBrowser(ctx, browser.SessionID)

	if extracted, err := a.extractBrowserDOM(ctx, browser.SessionID, args, includeText); err == nil {
		doc.FinalURL = firstNonEmpty(extracted.CurrentURL, extracted.URL, browser.CurrentURL, target)
		doc.Title = extracted.Title
		doc.Description = extracted.Description
		doc.Links = extracted.Links
		doc.Images = extracted.Images
		doc.Metadata = extracted.Metadata
		doc.StructuredData = extracted.StructuredData
		doc.Status = http.StatusOK
		doc.ContentType = "text/html"
		doc.ExtractionBackend = firstNonEmpty(extracted.ExtractionBackend, "browser_dom")
		doc.Bytes = len(extracted.Text) + len(extracted.Markdown) + len(extracted.HTML)
		if includeText {
			maxChars := boundedInt(intArg(args, "max_chars"), defaultMaxChars, 1000, 200000)
			doc.Truncated = len(extracted.Text) > maxChars || len(extracted.Markdown) > maxChars || len(extracted.HTML) > maxChars
			doc.Text = truncateString(extracted.Text, maxChars)
			doc.Markdown = truncateString(extracted.Markdown, maxChars)
			doc.HTML = truncateString(extracted.HTML, maxChars)
		}
		if boolArgDefault(args, "store", storeDefault(ctx)) && includeText {
			if art, err := storePageArtifact(ctx, runID, &doc); err == nil {
				doc.Artifact = art
			} else {
				ctx.Logger().Warn("store page artifact failed", "url", target, "err", err.Error())
			}
		}
		if boolArg(args, "snapshot") {
			shot, err := a.snapshot(ctx, runID, mapMerge(args, map[string]any{
				"session_id": browser.SessionID,
				"label":      firstNonEmpty(doc.Title, target),
				"store":      snapshotStore(ctx, args),
			}))
			if err == nil {
				doc.Snapshot = shot
			} else {
				ctx.Logger().Warn("snapshot failed", "url", target, "err", err.Error())
			}
		}
		return doc
	} else {
		ctx.Logger().Warn("computer.browser_extract failed; falling back to HTTP extraction", "url", target, "err", err.Error())
	}

	doc.ExtractionBackend = "http_after_browser_open"
	body, meta, err := fetchURL(ctx, target)
	doc.Status = intFromMap(meta, "status")
	doc.ContentType = stringFromMap(meta, "content_type")
	doc.Bytes = intFromMap(meta, "bytes")
	doc.Truncated = boolFromMap(meta, "truncated")
	if err != nil {
		doc.Error = err.Error()
		return doc
	}
	parsed := parseHTMLDoc(body, target)
	doc.FinalURL = firstNonEmpty(browser.CurrentURL, target)
	doc.Title = parsed.Title
	doc.Description = parsed.Description
	doc.Links = parsed.Links
	doc.Images = parsed.Images
	doc.Metadata = parsed.Metadata
	if includeText {
		maxChars := boundedInt(intArg(args, "max_chars"), defaultMaxChars, 1000, 200000)
		doc.Text = truncateString(parsed.Text, maxChars)
		doc.Markdown = truncateString(parsed.Markdown, maxChars)
	}
	if boolArgDefault(args, "store", storeDefault(ctx)) && includeText {
		if art, err := storePageArtifact(ctx, runID, &doc); err == nil {
			doc.Artifact = art
		} else {
			ctx.Logger().Warn("store page artifact failed", "url", target, "err", err.Error())
		}
	}
	if boolArg(args, "snapshot") {
		shot, err := a.snapshot(ctx, runID, mapMerge(args, map[string]any{
			"session_id": browser.SessionID,
			"label":      firstNonEmpty(doc.Title, target),
			"store":      snapshotStore(ctx, args),
		}))
		if err == nil {
			doc.Snapshot = shot
		} else {
			ctx.Logger().Warn("snapshot failed", "url", target, "err", err.Error())
		}
	}
	return doc
}

func (a *App) crawl(ctx *sdk.AppCtx, runID int64, seeds []string, args map[string]any, maxPages, maxDepth int, sameHost, includeText bool) ([]pageDoc, []crawlEdge) {
	queue := make([]crawlNode, 0, len(seeds))
	seedHosts := map[string]bool{}
	queued := map[string]bool{}
	for _, s := range seeds {
		canon := canonicalURL(s)
		if canon == "" || queued[canon] {
			continue
		}
		queue = append(queue, crawlNode{URL: s})
		queued[canon] = true
		if u, err := url.Parse(s); err == nil {
			seedHosts[strings.ToLower(u.Hostname())] = true
		}
	}
	visited := map[string]bool{}
	pages := make([]pageDoc, 0, maxPages)
	edges := make([]crawlEdge, 0)

	for len(queue) > 0 && len(pages) < maxPages {
		node := queue[0]
		queue = queue[1:]
		canon := canonicalURL(node.URL)
		delete(queued, canon)
		if visited[canon] || node.Depth > maxDepth {
			continue
		}
		visited[canon] = true
		doc := a.extractURL(ctx, runID, node.URL, mapMerge(args, map[string]any{"store": false}), includeText)
		pages = append(pages, doc)
		if doc.Error != "" || node.Depth >= maxDepth {
			continue
		}
		for _, l := range doc.Links {
			if len(edges) < maxPages*20 {
				edges = append(edges, crawlEdge{From: doc.URL, To: l.URL, Text: l.Text})
			}
			linkCanon := canonicalURL(l.URL)
			if len(queue)+len(pages) < maxPages && linkCanon != "" && !visited[linkCanon] && !queued[linkCanon] && shouldFollow(l.URL, seedHosts, sameHost) {
				queue = append(queue, crawlNode{URL: l.URL, Depth: node.Depth + 1})
				queued[linkCanon] = true
			}
		}
	}
	return pages, edges
}

func (a *App) snapshot(ctx *sdk.AppCtx, runID int64, args map[string]any) (map[string]any, error) {
	visibility, err := snapshotVisibility(args)
	if err != nil {
		return nil, err
	}
	sessionID := stringArg(args, "session_id")
	opened := false
	var browser *browserSession
	err = nil
	if sessionID == "" {
		target := stringArg(args, "url")
		if target == "" {
			return nil, errors.New("url or session_id required")
		}
		browser, err = a.openBrowser(ctx, target, args)
		if err != nil {
			return nil, err
		}
		sessionID = browser.SessionID
		opened = true
		defer a.closeBrowser(ctx, sessionID)
	}

	if wantsSmartSnapshot(args) {
		return a.smartSnapshot(ctx, runID, sessionID, browser, opened, args)
	}

	shot, err := a.captureBrowserScreenshot(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("computer.browser_screenshot: %w", err)
	}
	out := map[string]any{
		"session_id":  sessionID,
		"current_url": shot.CurrentURL,
		"width":       shot.Width,
		"height":      shot.Height,
		"stored":      false,
	}
	if browser != nil {
		out["browser"] = browser
	}
	if !boolArgDefault(args, "store", true) {
		out["png_b64"] = shot.PNGB64
		return out, nil
	}
	folder := "/.web/snapshots/" + time.Now().UTC().Format("2006-01")
	var up struct {
		ID  int64  `json:"id"`
		URL string `json:"url"`
	}
	name := randName() + ".png"
	if label := safeFilename(stringArg(args, "label")); label != "" {
		name = label + "-" + name
	}
	upArgs := withProjectID(ctx, map[string]any{
		"name":           name,
		"content_base64": shot.PNGB64,
		"folder":         folder,
		"content_type":   "image/png",
		"source":         "web:snapshot",
		"visibility":     visibility,
	})
	if err := ctx.PlatformAPI().CallAppResult("storage", "files_upload", upArgs, &up); err != nil {
		return nil, fmt.Errorf("storage.files_upload: %w", err)
	}
	size := encodedBase64Size(shot.PNGB64)
	art, err := insertArtifact(ctx, runID, "snapshot", shot.CurrentURL, stringArg(args, "label"), up.ID, up.URL, "image/png", size, compactArtifactMetadata("snapshot", shot.CurrentURL, stringArg(args, "label"), "image/png", size))
	if err != nil {
		rollbackStoredFile(ctx, up.ID)
		return nil, fmt.Errorf("record snapshot artifact: %w", err)
	}
	out["stored"] = true
	out["storage_id"] = up.ID
	out["url"] = up.URL
	out["artifact_id"] = art.ID
	out["visibility"] = visibility
	out["opened_session"] = opened
	return out, nil
}

func wantsSmartSnapshot(args map[string]any) bool {
	mode := strings.ToLower(strings.TrimSpace(stringArg(args, "mode")))
	return mode == "smart" || strings.TrimSpace(stringArg(args, "query")) != ""
}

func (a *App) captureBrowserScreenshot(ctx *sdk.AppCtx, sessionID string) (*browserScreenshot, error) {
	var shot browserScreenshot
	if err := ctx.PlatformAPI().CallAppResult("computer", "browser_screenshot", withProjectID(ctx, map[string]any{"session_id": sessionID}), &shot); err != nil {
		return nil, err
	}
	return &shot, nil
}

func cookieHandlingMode(args map[string]any) string {
	mode := strings.ToLower(strings.TrimSpace(firstNonEmpty(stringArg(args, "cookie_handling"), "auto")))
	switch mode {
	case "auto", "off":
		return mode
	default:
		return "auto"
	}
}

func (a *App) dismissCookieBanner(ctx *sdk.AppCtx, sessionID string, args map[string]any) (map[string]any, error) {
	mode := cookieHandlingMode(args)
	out := map[string]any{"mode": mode, "attempted": false, "dismissed": false}
	if mode == "off" {
		return out, nil
	}
	extracted, err := a.extractBrowserDOM(ctx, sessionID, mapMerge(args, map[string]any{
		"formats":   []string{"regions", "text", "html"},
		"max_chars": 50000,
	}), false)
	if err != nil {
		return out, err
	}
	region, strategy, ok := findCookieBannerRegion(extracted.Regions)
	if !ok {
		if a.dismissCookieBannerWithSOM(ctx, sessionID, args, extracted, nil, out) {
			return out, nil
		}
		out["reason"] = "no_cookie_banner_detected"
		return out, nil
	}
	if a.dismissCookieBannerWithSOM(ctx, sessionID, args, extracted, &region, out) {
		return out, nil
	}
	x, y := cookieAcceptCoordinate(region, strategy)
	out["attempted"] = true
	out["strategy"] = strategy + "_coordinate_fallback"
	out["region_id"] = region.ID
	out["selector"] = region.Selector
	out["heading"] = region.Heading
	out["coordinate"] = fmt.Sprintf("%d,%d", x, y)
	var clickOut map[string]any
	if err := ctx.PlatformAPI().CallAppResult("computer", "computer_use", withProjectID(ctx, map[string]any{
		"session_id": sessionID,
		"action":     "click",
		"coordinate": out["coordinate"],
	}), &clickOut); err != nil {
		return out, fmt.Errorf("computer.computer_use cookie click: %w", err)
	}
	var waitOut map[string]any
	_ = ctx.PlatformAPI().CallAppResult("computer", "computer_use", withProjectID(ctx, map[string]any{
		"session_id": sessionID,
		"action":     "wait",
		"duration":   800,
	}), &waitOut)
	verified, _ := a.extractBrowserDOM(ctx, sessionID, mapMerge(args, map[string]any{
		"formats":   []string{"regions"},
		"max_chars": 5000,
	}), false)
	if verified != nil {
		_, _, stillPresent := findCookieBannerRegion(verified.Regions)
		out["dismissed"] = !stillPresent
		if stillPresent {
			out["reason"] = "banner_still_detected_after_click"
		}
		return out, nil
	}
	out["dismissed"] = true
	out["reason"] = "clicked_not_verified"
	return out, nil
}

func findCookieBannerRegion(regions []browserRegion) (browserRegion, string, bool) {
	for _, r := range regions {
		selector := strings.ToLower(r.Selector)
		text := strings.ToLower(strings.Join([]string{r.Heading, r.Text, selector}, " "))
		if strings.Contains(selector, "onetrust") || strings.Contains(selector, "#onetrust-banner-sdk") {
			return r, "onetrust", true
		}
		if strings.Contains(selector, "cybot") || strings.Contains(text, "cookiebot") {
			return r, "cookiebot", true
		}
		if strings.Contains(selector, "didomi") || strings.Contains(text, "didomi") {
			return r, "didomi", true
		}
		if strings.Contains(selector, "usercentrics") || strings.Contains(selector, "uc-") || strings.Contains(text, "usercentrics") {
			return r, "usercentrics", true
		}
		if isGenericCookieBanner(r, text) {
			return r, "generic_accept_all", true
		}
	}
	return browserRegion{}, "", false
}

func (a *App) dismissCookieBannerWithSOM(ctx *sdk.AppCtx, sessionID string, args map[string]any, extracted *browserExtractResult, region *browserRegion, out map[string]any) bool {
	pageText := strings.Join([]string{extracted.Text, extracted.HTML}, " ")
	if region == nil && !hasGenericCookieBannerText(pageText) {
		return false
	}
	var shot computerSOMScreenshot
	if err := ctx.PlatformAPI().CallAppResult("computer", "computer_use", withProjectID(ctx, map[string]any{
		"session_id":  sessionID,
		"action":      "screenshot",
		"som":         true,
		"include_som": true,
	}), &shot); err != nil {
		if region != nil {
			out["som_fallback_reason"] = "structured targets unavailable: " + err.Error()
			return false
		}
		out["reason"] = "cookie_banner_text_detected_but_som_unavailable"
		out["error"] = err.Error()
		return true
	}
	target, ok := findCookieAcceptTargetForRegion(shot.SOM, region)
	if !ok {
		if region != nil {
			out["som_fallback_reason"] = "no accept target inside detected banner"
			out["som_targets"] = len(shot.SOM)
			return false
		}
		out["reason"] = "cookie_banner_text_detected_but_no_som_accept_target"
		out["som_targets"] = len(shot.SOM)
		return true
	}
	out["attempted"] = true
	out["strategy"] = "som_accept_button"
	out["label"] = target.Label
	out["target_text"] = target.Text
	out["target_tag"] = target.Tag
	out["target_role"] = target.Role
	var clickOut map[string]any
	if err := ctx.PlatformAPI().CallAppResult("computer", "computer_use", withProjectID(ctx, map[string]any{
		"session_id": sessionID,
		"action":     "click",
		"label":      target.Label,
	}), &clickOut); err != nil {
		out["reason"] = "som_cookie_click_failed"
		out["error"] = err.Error()
		return true
	}
	var waitOut map[string]any
	_ = ctx.PlatformAPI().CallAppResult("computer", "computer_use", withProjectID(ctx, map[string]any{
		"session_id": sessionID,
		"action":     "wait",
		"duration":   800,
	}), &waitOut)
	var verifyShot computerSOMScreenshot
	if err := ctx.PlatformAPI().CallAppResult("computer", "computer_use", withProjectID(ctx, map[string]any{
		"session_id":  sessionID,
		"action":      "screenshot",
		"som":         true,
		"include_som": true,
	}), &verifyShot); err == nil {
		stillVisible := hasVisibleCookieAcceptTarget(verifyShot.SOM, region)
		out["dismissed"] = !stillVisible
		if stillVisible {
			out["reason"] = "som_accept_target_still_visible_after_click"
		}
		return true
	}
	verified, _ := a.extractBrowserDOM(ctx, sessionID, mapMerge(args, map[string]any{
		"formats":   []string{"text", "html"},
		"max_chars": 50000,
	}), false)
	if verified != nil {
		stillPresent := hasGenericCookieBannerText(strings.Join([]string{verified.Text, verified.HTML}, " "))
		out["dismissed"] = !stillPresent
		if stillPresent {
			out["reason"] = "banner_still_detected_after_som_click"
		}
		return true
	}
	out["dismissed"] = true
	out["reason"] = "som_clicked_not_verified"
	return true
}

func hasGenericCookieBannerText(text string) bool {
	low := strings.ToLower(text)
	hasCookie := strings.Contains(low, "cookie") || strings.Contains(low, "consent")
	hasNotice := strings.Contains(low, "we use cookies") ||
		strings.Contains(low, "similar technologies") ||
		strings.Contains(low, "personalize content") ||
		strings.Contains(low, "provide a better experience") ||
		(strings.Contains(low, "privacy notice") && strings.Contains(low, "cookies policy"))
	hasAccept := strings.Contains(low, "i accept") ||
		strings.Contains(low, "accept all") ||
		strings.Contains(low, "accept cookies") ||
		strings.Contains(low, "allow all") ||
		strings.Contains(low, "agree") ||
		strings.Contains(low, "got it")
	return hasCookie && hasNotice && hasAccept
}

func findCookieAcceptTarget(targets []setOfMarkTarget) (setOfMarkTarget, bool) {
	return findCookieAcceptTargetForRegion(targets, nil)
}

func findCookieAcceptTargetForRegion(targets []setOfMarkTarget, region *browserRegion) (setOfMarkTarget, bool) {
	bestScore := 0
	var best setOfMarkTarget
	for _, t := range targets {
		if region != nil && !targetInsideRegion(t, *region) {
			continue
		}
		if score := cookieAcceptTargetScore(t); score > bestScore {
			bestScore = score
			best = t
		}
	}
	return best, bestScore > 0
}

func isCookieAcceptTarget(t setOfMarkTarget) bool {
	return cookieAcceptTargetScore(t) > 0
}

func cookieAcceptTargetScore(t setOfMarkTarget) int {
	text := strings.ToLower(strings.TrimSpace(t.Text))
	if text == "" {
		return 0
	}
	buttonish := strings.EqualFold(t.Tag, "button") ||
		strings.EqualFold(t.Role, "button") ||
		strings.EqualFold(t.Type, "button") ||
		strings.EqualFold(t.Type, "submit")
	if !buttonish {
		return 0
	}
	for _, reject := range []string{"reject", "decline", "deny", "settings", "preferences", "customize", "manage", "not accept"} {
		if strings.Contains(text, reject) {
			return 0
		}
	}
	switch text {
	case "accept all":
		return 100
	case "allow all":
		return 95
	case "accept cookies":
		return 90
	case "i accept":
		return 85
	case "accept":
		return 80
	case "agree":
		return 70
	case "got it":
		return 60
	case "ok":
		return 20
	}
	switch {
	case strings.Contains(text, "accept all"):
		return 95
	case strings.Contains(text, "allow all"):
		return 90
	case strings.Contains(text, "accept cookies"):
		return 85
	case strings.Contains(text, "i accept"):
		return 80
	default:
		return 0
	}
}

func targetInsideRegion(t setOfMarkTarget, region browserRegion) bool {
	rect := region.ViewportRect
	if rect.Width <= 0 || rect.Height <= 0 {
		return true
	}
	cx := float64(t.X) + float64(t.W)/2
	cy := float64(t.Y) + float64(t.H)/2
	const tolerance = 12.0
	return cx >= rect.X-tolerance && cx <= rect.X+rect.Width+tolerance &&
		cy >= rect.Y-tolerance && cy <= rect.Y+rect.Height+tolerance
}

func hasVisibleCookieAcceptTarget(targets []setOfMarkTarget, region *browserRegion) bool {
	_, ok := findCookieAcceptTargetForRegion(targets, region)
	return ok
}

func isGenericCookieBanner(r browserRegion, text string) bool {
	if r.Rect.Width <= 0 || r.Rect.Height <= 0 {
		return false
	}
	if r.Rect.Width*r.Rect.Height > 1200000 || r.Rect.Height > 750 {
		return false
	}
	hasCookie := strings.Contains(text, "cookie") || strings.Contains(text, "consent") || strings.Contains(text, "privacy preferences")
	hasAccept := strings.Contains(text, "accept all") || strings.Contains(text, "allow all") || strings.Contains(text, "agree")
	return hasCookie && hasAccept
}

func cookieAcceptCoordinate(r browserRegion, strategy string) (int, int) {
	rect := r.ViewportRect
	if rect.Width <= 0 || rect.Height <= 0 {
		rect = r.Rect
	}
	xRatio, yRatio := 0.84, 0.8
	switch strategy {
	case "onetrust":
		xRatio, yRatio = 0.85, 0.79
	case "cookiebot", "didomi", "usercentrics":
		xRatio, yRatio = 0.78, 0.82
	}
	x := int(math.Round(rect.X + rect.Width*xRatio))
	y := int(math.Round(rect.Y + rect.Height*yRatio))
	if x < 1 {
		x = 1
	}
	if y < 1 {
		y = 1
	}
	return x, y
}

func (a *App) smartSnapshot(ctx *sdk.AppCtx, runID int64, sessionID string, browser *browserSession, opened bool, args map[string]any) (map[string]any, error) {
	query := strings.TrimSpace(stringArg(args, "query"))
	if query == "" {
		return nil, errors.New("query required for smart snapshot")
	}
	visibility, err := snapshotVisibility(args)
	if err != nil {
		return nil, err
	}
	cookieResult, err := a.dismissCookieBanner(ctx, sessionID, args)
	if err != nil {
		ctx.Logger().Warn("cookie banner dismissal failed", "err", err.Error())
		cookieResult = map[string]any{
			"mode":  cookieHandlingMode(args),
			"error": err.Error(),
		}
	}
	extracted, err := a.extractBrowserDOM(ctx, sessionID, mapMerge(args, map[string]any{
		"formats":   []string{"regions"},
		"max_chars": 5000,
	}), false)
	if err != nil {
		return nil, err
	}
	maxShots := boundedInt(intArg(args, "max_shots"), 3, 1, 5)
	candidates := rankRegions(extracted.Regions, query, maxShots)
	out := map[string]any{
		"session_id":         sessionID,
		"current_url":        firstNonEmpty(extracted.CurrentURL, extracted.URL),
		"mode":               "smart",
		"query":              query,
		"stored":             false,
		"opened_session":     opened,
		"extraction_backend": firstNonEmpty(extracted.ExtractionBackend, "browser_dom"),
		"candidate_regions":  len(extracted.Regions),
		"coordinate_frame":   "viewport_css_px",
		"matching_algorithm": "lexical_region_score_v2",
		"cookie_handling":    cookieResult,
	}
	if browser != nil {
		out["browser"] = browser
	}
	if len(candidates) == 0 {
		shot, err := a.captureBrowserScreenshot(ctx, sessionID)
		if err != nil {
			return nil, fmt.Errorf("computer.browser_screenshot: %w", err)
		}
		out["current_url"] = firstNonEmpty(shot.CurrentURL, stringFromAny(out["current_url"]))
		out["width"] = shot.Width
		out["height"] = shot.Height
		out["shots"] = []map[string]any{}
		out["fallback"] = "no_matching_regions"
		if !boolArgDefault(args, "store", true) {
			out["png_b64"] = shot.PNGB64
			return out, nil
		}
		if err := a.storeSnapshotImage(ctx, runID, out, shot.PNGB64, "smart-fallback", stringArg(args, "label"), 0, visibility); err != nil {
			return nil, err
		}
		return out, nil
	}

	shots := make([]map[string]any, 0, len(candidates))
	startScroll := candidates[0].Region.Rect.Y - candidates[0].Region.ViewportRect.Y
	if startScroll < 0 {
		startScroll = 0
	}
	currentScroll := startScroll
	store := boolArgDefault(args, "store", true)
	crop := boolArgDefault(args, "crop", true)
	for i, ranked := range candidates {
		region := ranked.Region
		positionErr := ""
		targetScroll := math.Max(0, region.Rect.Y-120)
		if diff := targetScroll - currentScroll; math.Abs(diff) >= 20 {
			direction := "down"
			amount := int(math.Round(diff))
			if amount < 0 {
				direction = "up"
				amount = -amount
			}
			var actionOut map[string]any
			if err := ctx.PlatformAPI().CallAppResult("computer", "computer_use", withProjectID(ctx, map[string]any{
				"session_id": sessionID,
				"action":     "scroll",
				"direction":  direction,
				"amount":     amount,
			}), &actionOut); err != nil {
				return nil, fmt.Errorf("computer.computer_use scroll: %w", err)
			}
			currentScroll = targetScroll
			if refreshed, actualScroll, err := a.locateRegionAfterScroll(ctx, sessionID, args, region); err == nil {
				region = refreshed
				currentScroll = actualScroll
			} else {
				positionErr = err.Error()
			}
		}
		shot, err := a.captureBrowserScreenshot(ctx, sessionID)
		if err != nil {
			return nil, fmt.Errorf("computer.browser_screenshot: %w", err)
		}
		viewportRect := browserRect{
			X:      region.Rect.X,
			Y:      region.Rect.Y - currentScroll,
			Width:  region.Rect.Width,
			Height: region.Rect.Height,
		}
		contentB64 := shot.PNGB64
		width, height := shot.Width, shot.Height
		cropErr := ""
		if crop {
			if cropped, cw, ch, err := cropScreenshotPNG(shot.PNGB64, viewportRect, shot.Width, shot.Height, 24); err == nil {
				contentB64, width, height = cropped, cw, ch
			} else {
				cropErr = err.Error()
			}
		}
		item := map[string]any{
			"rank":          i + 1,
			"region_id":     region.ID,
			"score":         ranked.Score,
			"confidence":    confidenceLabel(ranked.Score),
			"reason":        ranked.Reason,
			"nearby_text":   truncateString(firstNonEmpty(region.Text, region.Heading), 700),
			"heading":       region.Heading,
			"selector":      region.Selector,
			"coordinates":   viewportRect,
			"source_region": region,
			"current_url":   shot.CurrentURL,
			"width":         width,
			"height":        height,
			"stored":        false,
		}
		if cropErr != "" {
			item["crop_error"] = cropErr
		}
		if positionErr != "" {
			item["position_warning"] = positionErr
		}
		if store {
			label := safeFilename(firstNonEmpty(stringArg(args, "label"), region.Heading, "smart-snapshot"))
			if err := a.storeSnapshotImage(ctx, runID, item, contentB64, "smart", label, 0, visibility); err != nil {
				return nil, err
			}
		} else {
			item["png_b64"] = contentB64
		}
		if i == 0 {
			out["current_url"] = shot.CurrentURL
			out["width"] = width
			out["height"] = height
			for _, key := range []string{"storage_id", "url", "artifact_id", "visibility", "png_b64"} {
				if v, ok := item[key]; ok {
					out[key] = v
				}
			}
			out["stored"] = item["stored"]
		}
		shots = append(shots, item)
	}
	out["shots"] = shots
	return out, nil
}

func (a *App) locateRegionAfterScroll(ctx *sdk.AppCtx, sessionID string, args map[string]any, original browserRegion) (browserRegion, float64, error) {
	extracted, err := a.extractBrowserDOM(ctx, sessionID, mapMerge(args, map[string]any{
		"formats":   []string{"regions"},
		"max_chars": 5000,
	}), false)
	if err != nil {
		return original, 0, err
	}
	bestIndex := -1
	bestDistance := math.MaxFloat64
	for i, candidate := range extracted.Regions {
		if original.Selector != "" && candidate.Selector == original.Selector {
			bestIndex = i
			break
		}
		if original.ID != "" && candidate.ID == original.ID {
			bestIndex = i
			bestDistance = 0
			continue
		}
		if original.Tag != candidate.Tag || normalizeRegionText(original.Heading) != normalizeRegionText(candidate.Heading) {
			continue
		}
		distance := math.Abs(original.Rect.X-candidate.Rect.X) + math.Abs(original.Rect.Y-candidate.Rect.Y)
		if distance < bestDistance {
			bestIndex = i
			bestDistance = distance
		}
	}
	if bestIndex < 0 {
		return original, 0, errors.New("rendered region could not be relocated after scrolling")
	}
	region := extracted.Regions[bestIndex]
	scrollY := region.Rect.Y - region.ViewportRect.Y
	if scrollY < 0 {
		scrollY = 0
	}
	return region, scrollY, nil
}

func (a *App) storeSnapshotImage(ctx *sdk.AppCtx, runID int64, out map[string]any, pngB64, prefix, label string, size int, visibility string) error {
	folder := "/.web/snapshots/" + time.Now().UTC().Format("2006-01")
	var up struct {
		ID  int64  `json:"id"`
		URL string `json:"url"`
	}
	name := randName() + ".png"
	if safe := safeFilename(firstNonEmpty(label, prefix)); safe != "" {
		name = safe + "-" + name
	}
	upArgs := withProjectID(ctx, map[string]any{
		"name":           name,
		"content_base64": pngB64,
		"folder":         folder,
		"content_type":   "image/png",
		"source":         "web:snapshot",
		"visibility":     visibility,
	})
	if err := ctx.PlatformAPI().CallAppResult("storage", "files_upload", upArgs, &up); err != nil {
		return fmt.Errorf("storage.files_upload: %w", err)
	}
	if size <= 0 {
		size = encodedBase64Size(pngB64)
	}
	art, err := insertArtifact(ctx, runID, "snapshot", stringFromAny(out["current_url"]), stringFromAny(out["heading"]), up.ID, up.URL, "image/png", size, compactArtifactMetadata("snapshot", stringFromAny(out["current_url"]), stringFromAny(out["heading"]), "image/png", size))
	if err != nil {
		rollbackStoredFile(ctx, up.ID)
		return fmt.Errorf("record snapshot artifact: %w", err)
	}
	out["stored"] = true
	out["storage_id"] = up.ID
	out["url"] = up.URL
	out["artifact_id"] = art.ID
	out["visibility"] = visibility
	return nil
}

type rankedRegion struct {
	Region browserRegion
	Score  float64
	Reason string
}

func rankRegions(regions []browserRegion, query string, limit int) []rankedRegion {
	tokens := tokenizeQuery(query)
	if len(tokens) == 0 {
		return nil
	}
	ranked := make([]rankedRegion, 0, len(regions))
	for _, r := range regions {
		text := strings.ToLower(strings.Join([]string{r.Heading, r.Text, r.Tag, r.Role, r.Selector}, " "))
		if strings.TrimSpace(text) == "" || r.Rect.Width <= 0 || r.Rect.Height <= 0 {
			continue
		}
		if isSmallLeafTextRegion(r) && !(querySuggestsContact(tokens) && strings.Contains(text, "@")) {
			continue
		}
		score := 0.0
		var hits []string
		for _, tok := range tokens {
			if strings.Contains(text, tok) {
				score += 2
				hits = append(hits, tok)
				if strings.Contains(strings.ToLower(r.Heading), tok) {
					score += 1.5
				}
			}
		}
		if strings.Contains(text, "@") && querySuggestsContact(tokens) {
			score += 3
			hits = append(hits, "email")
		}
		if strings.Contains(text, "contact") || strings.Contains(text, "partner") || strings.Contains(text, "affiliate") {
			score += 0.75
		}
		switch r.Tag {
		case "form", "table", "section", "article":
			score += 0.75
		case "footer", "nav":
			score -= 3
		case "main":
			score -= 1.5
		}
		if r.Visible {
			score += 0.25
		}
		area := r.Rect.Width * r.Rect.Height
		if area > 25000 && area < 900000 {
			score += 0.25
		}
		score += regionSpecificityScore(r)
		if score <= 0 {
			continue
		}
		reason := "Matched query terms in rendered page region."
		if len(hits) > 0 {
			reason = "Matched " + strings.Join(uniqueStrings(hits), ", ") + " in rendered page region."
		}
		ranked = append(ranked, rankedRegion{Region: r, Score: math.Round(score*100) / 100, Reason: reason})
	}
	ranked = dedupeRankedRegions(ranked)
	sort.SliceStable(ranked, func(i, j int) bool {
		if ranked[i].Score == ranked[j].Score {
			return ranked[i].Region.Rect.Y < ranked[j].Region.Rect.Y
		}
		return ranked[i].Score > ranked[j].Score
	})
	if limit > 0 && len(ranked) > limit {
		ranked = ranked[:limit]
	}
	return ranked
}

func regionSpecificityScore(r browserRegion) float64 {
	area := r.Rect.Width * r.Rect.Height
	height := r.Rect.Height
	score := 0.0
	switch {
	case area > 6000000 || height > 3000:
		score -= 7
	case area > 2500000 || height > 1800:
		score -= 4
	case area > 1200000 || height > 1200:
		score -= 2
	}
	if area > 0 && area < 700000 && height >= 80 && height <= 900 {
		score += 1.25
	}
	if isLeafTextRegion(r) {
		score -= 4
		if r.Rect.Width < 720 || r.Rect.Height < 180 {
			score -= 1
		}
	}
	selector := strings.ToLower(r.Selector)
	if strings.Contains(selector, "card") || strings.Contains(selector, "tile") || strings.Contains(selector, "product") || strings.Contains(selector, "pricing") {
		score += 0.5
	}
	if strings.Contains(selector, "footer") || strings.Contains(selector, "nav") {
		score -= 2
	}
	return score
}

func isLeafTextRegion(r browserRegion) bool {
	switch strings.ToLower(r.Tag) {
	case "h1", "h2", "h3", "h4", "h5", "h6", "p", "span", "strong", "em", "small", "a":
		return true
	default:
		return false
	}
}

func isSmallLeafTextRegion(r browserRegion) bool {
	return isLeafTextRegion(r) && (r.Rect.Width < 720 || r.Rect.Height < 180)
}

func dedupeRankedRegions(in []rankedRegion) []rankedRegion {
	if len(in) <= 1 {
		return in
	}
	suppressed := make([]bool, len(in))
	for i := range in {
		if suppressed[i] {
			continue
		}
		for j := i + 1; j < len(in); j++ {
			if suppressed[j] || !duplicateRegion(in[i].Region, in[j].Region) {
				continue
			}
			if in[j].Score > in[i].Score {
				suppressed[i] = true
				break
			}
			suppressed[j] = true
		}
	}
	for i := range in {
		for j := range in {
			if i == j || suppressed[i] {
				continue
			}
			a, b := in[i], in[j]
			if regionContains(a.Region, b.Region, 24) && regionArea(a.Region) > regionArea(b.Region)*2.25 && b.Score >= a.Score-2 {
				suppressed[i] = true
				continue
			}
			if overlapRatio(a.Region, b.Region) > 0.86 && regionArea(a.Region) > regionArea(b.Region)*1.35 && b.Score >= a.Score-1 {
				suppressed[i] = true
			}
		}
	}
	out := make([]rankedRegion, 0, len(in))
	for i, r := range in {
		if !suppressed[i] {
			out = append(out, r)
		}
	}
	return out
}

func duplicateRegion(a, b browserRegion) bool {
	if strings.TrimSpace(a.Selector) != "" && a.Selector == b.Selector {
		return rectsNearEqual(a.Rect, b.Rect, 2)
	}
	if overlapRatio(a, b) > 0.94 &&
		normalizeRegionText(a.Heading) != "" &&
		normalizeRegionText(a.Heading) == normalizeRegionText(b.Heading) {
		return true
	}
	if !rectsNearEqual(a.Rect, b.Rect, 2) {
		return false
	}
	return normalizeRegionText(a.Heading) == normalizeRegionText(b.Heading) &&
		normalizeRegionText(a.Text) == normalizeRegionText(b.Text)
}

func rectsNearEqual(a, b browserRect, tolerance float64) bool {
	return math.Abs(a.X-b.X) <= tolerance &&
		math.Abs(a.Y-b.Y) <= tolerance &&
		math.Abs(a.Width-b.Width) <= tolerance &&
		math.Abs(a.Height-b.Height) <= tolerance
}

func normalizeRegionText(s string) string {
	return strings.Join(strings.Fields(strings.ToLower(strings.TrimSpace(s))), " ")
}

func regionArea(r browserRegion) float64 {
	if r.Rect.Width <= 0 || r.Rect.Height <= 0 {
		return 0
	}
	return r.Rect.Width * r.Rect.Height
}

func regionContains(parent, child browserRegion, tolerance float64) bool {
	if regionArea(parent) <= 0 || regionArea(child) <= 0 {
		return false
	}
	return child.Rect.X >= parent.Rect.X-tolerance &&
		child.Rect.Y >= parent.Rect.Y-tolerance &&
		child.Rect.X+child.Rect.Width <= parent.Rect.X+parent.Rect.Width+tolerance &&
		child.Rect.Y+child.Rect.Height <= parent.Rect.Y+parent.Rect.Height+tolerance
}

func overlapRatio(a, b browserRegion) float64 {
	ax0, ay0 := a.Rect.X, a.Rect.Y
	ax1, ay1 := a.Rect.X+a.Rect.Width, a.Rect.Y+a.Rect.Height
	bx0, by0 := b.Rect.X, b.Rect.Y
	bx1, by1 := b.Rect.X+b.Rect.Width, b.Rect.Y+b.Rect.Height
	x0, y0 := math.Max(ax0, bx0), math.Max(ay0, by0)
	x1, y1 := math.Min(ax1, bx1), math.Min(ay1, by1)
	if x1 <= x0 || y1 <= y0 {
		return 0
	}
	intersection := (x1 - x0) * (y1 - y0)
	smaller := math.Min(regionArea(a), regionArea(b))
	if smaller <= 0 {
		return 0
	}
	return intersection / smaller
}

func tokenizeQuery(query string) []string {
	fields := strings.FieldsFunc(strings.ToLower(query), func(r rune) bool {
		return !(unicode.IsLetter(r) || unicode.IsNumber(r) || r == '@')
	})
	out := make([]string, 0, len(fields))
	for _, f := range fields {
		f = strings.TrimSpace(f)
		if len(f) < 3 {
			continue
		}
		switch f {
		case "the", "and", "for", "with", "from", "that", "this", "page", "site":
			continue
		}
		out = append(out, f)
	}
	return uniqueStrings(out)
}

func querySuggestsContact(tokens []string) bool {
	for _, t := range tokens {
		switch t {
		case "email", "contact", "affiliate", "partner", "partnership", "support", "sales":
			return true
		}
	}
	return false
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
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

func confidenceLabel(score float64) string {
	switch {
	case score >= 7:
		return "high"
	case score >= 3:
		return "medium"
	default:
		return "low"
	}
}

func cropScreenshotPNG(pngB64 string, rect browserRect, shotWidth, shotHeight, padding int) (string, int, int, error) {
	raw, err := base64.StdEncoding.DecodeString(pngB64)
	if err != nil {
		return "", 0, 0, err
	}
	src, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		return "", 0, 0, err
	}
	bounds := src.Bounds()
	imgW, imgH := bounds.Dx(), bounds.Dy()
	if imgW <= 0 || imgH <= 0 {
		return "", 0, 0, errors.New("empty screenshot")
	}
	if shotWidth <= 0 {
		shotWidth = imgW
	}
	if shotHeight <= 0 {
		shotHeight = imgH
	}
	scaleX := float64(imgW) / float64(shotWidth)
	scaleY := float64(imgH) / float64(shotHeight)
	padX := float64(padding) * scaleX
	padY := float64(padding) * scaleY
	x0 := clampInt(int(math.Floor(rect.X*scaleX-padX)), 0, imgW)
	y0 := clampInt(int(math.Floor(rect.Y*scaleY-padY)), 0, imgH)
	x1 := clampInt(int(math.Ceil((rect.X+rect.Width)*scaleX+padX)), 0, imgW)
	y1 := clampInt(int(math.Ceil((rect.Y+rect.Height)*scaleY+padY)), 0, imgH)
	if x1 <= x0 || y1 <= y0 || x1-x0 < 8 || y1-y0 < 8 {
		return "", 0, 0, fmt.Errorf("invalid crop rect %.1f,%.1f %.1fx%.1f", rect.X, rect.Y, rect.Width, rect.Height)
	}
	dst := image.NewRGBA(image.Rect(0, 0, x1-x0, y1-y0))
	draw.Draw(dst, dst.Bounds(), src, image.Point{X: bounds.Min.X + x0, Y: bounds.Min.Y + y0}, draw.Src)
	var buf bytes.Buffer
	if err := png.Encode(&buf, dst); err != nil {
		return "", 0, 0, err
	}
	return base64.StdEncoding.EncodeToString(buf.Bytes()), dst.Bounds().Dx(), dst.Bounds().Dy(), nil
}

func clampInt(v, min, max int) int {
	if v < min {
		return min
	}
	if v > max {
		return max
	}
	return v
}

func (a *App) openBrowser(ctx *sdk.AppCtx, target string, args map[string]any) (*browserSession, error) {
	if err := validateBrowserTarget(ctx, target); err != nil {
		return nil, err
	}
	openArgs := map[string]any{"url": target}
	if b := firstNonEmpty(stringArg(args, "backend"), configString(ctx, "default_backend")); b != "" {
		openArgs["backend"] = b
	}
	for _, key := range []string{"viewport", "environment", "context_id", "persist", "timeout", "proxy", "proxy_mode", "proxy_profile", "proxy_country", "proxy_sticky"} {
		if v, ok := args[key]; ok {
			openArgs[key] = v
		}
	}
	var out browserSession
	if err := ctx.PlatformAPI().CallAppResult("computer", "browser_open", withProjectID(ctx, openArgs), &out); err != nil {
		return nil, fmt.Errorf("computer.browser_open: %w", err)
	}
	if out.SessionID == "" {
		return nil, errors.New("computer.browser_open returned empty session_id")
	}
	return &out, nil
}

func (a *App) closeBrowser(ctx *sdk.AppCtx, sessionID string) {
	if sessionID == "" {
		return
	}
	var out struct{}
	if err := ctx.PlatformAPI().CallAppResult("computer", "browser_close", withProjectID(ctx, map[string]any{"session_id": sessionID}), &out); err != nil {
		ctx.Logger().Warn("computer.browser_close failed", "session_id", sessionID, "err", err.Error())
	}
}

func (a *App) extractBrowserDOM(ctx *sdk.AppCtx, sessionID string, args map[string]any, includeText bool) (*browserExtractResult, error) {
	formats := stringSliceArg(args, "formats")
	if len(formats) == 0 {
		formats = []string{"metadata", "structured_data", "links", "images"}
		if includeText {
			formats = append([]string{"text", "markdown"}, formats...)
		}
	}
	extractArgs := withProjectID(ctx, map[string]any{
		"session_id":  sessionID,
		"formats":     formats,
		"max_chars":   boundedInt(intArg(args, "max_chars"), defaultMaxChars, 1000, 200000),
		"readability": true,
	})
	if waitMS := intArg(args, "wait_ms"); waitMS > 0 {
		extractArgs["wait_ms"] = waitMS
	}
	var out browserExtractResult
	if err := ctx.PlatformAPI().CallAppResult("computer", "browser_extract", extractArgs, &out); err != nil {
		return nil, fmt.Errorf("computer.browser_extract: %w", err)
	}
	if out.ExtractionBackend == "" {
		out.ExtractionBackend = "browser_dom"
	}
	return &out, nil
}

// ─── Response cache ───────────────────────────────────────────────

func newCachePolicy(kind string, args map[string]any) (cachePolicy, error) {
	mode := strings.ToLower(strings.TrimSpace(firstNonEmpty(stringArg(args, "cache"), "auto")))
	switch mode {
	case "auto", "bypass", "refresh", "force":
	default:
		return cachePolicy{}, fmt.Errorf("cache must be one of auto, bypass, refresh, force")
	}

	maxAge, explicitMaxAge := cacheDurationArg(args, "max_age", defaultMaxAge(kind))
	ttl, explicitTTL := cacheDurationArg(args, "cache_ttl", defaultCacheTTL(kind))
	if kind == "snapshot" && explicitMaxAge && !explicitTTL {
		ttl = maxAge
	}
	if maxAge < 0 || ttl < 0 {
		return cachePolicy{}, errors.New("max_age and cache_ttl must be >= 0")
	}

	requestJSON, key, err := cacheKey(kind, args)
	if err != nil {
		return cachePolicy{}, err
	}
	p := cachePolicy{
		Kind:           kind,
		Mode:           mode,
		Key:            key,
		RequestJSON:    requestJSON,
		MaxAge:         maxAge,
		TTL:            ttl,
		ExplicitMaxAge: explicitMaxAge,
	}

	switch mode {
	case "bypass":
		p.Read = false
		p.Write = false
		p.Reason = "bypass requested"
	case "refresh":
		p.Read = false
		p.Write = ttl > 0
	case "force":
		p.Read = true
		p.Write = false
		p.ForceOnly = true
	case "auto":
		p.Read = maxAge > 0
		p.Write = ttl > 0
		if maxAge == 0 {
			p.Reason = "max_age=0"
		}
	}

	if kind == "snapshot" && !explicitMaxAge {
		p.Read = false
		p.Write = false
		if p.Reason == "" {
			p.Reason = "snapshot cache requires max_age"
		}
	}
	if kind == "snapshot" && !boolArgDefault(args, "store", true) {
		p.Read = false
		p.Write = false
		p.Reason = "snapshot cache requires stored output"
	}
	if kind == "extract" && boolArg(args, "snapshot") && !explicitMaxAge {
		p.Read = false
		p.Write = false
		if p.Reason == "" {
			p.Reason = "snapshot requested"
		}
	}
	if p.ForceOnly && !p.Read {
		return cachePolicy{}, fmt.Errorf("cache force is unavailable for this %s request", kind)
	}
	return p, nil
}

func loadCachedResponse(ctx *sdk.AppCtx, p cachePolicy) (map[string]any, bool, error) {
	if !p.Read {
		return nil, false, nil
	}
	var responseJSON string
	var createdAt time.Time
	var expiresAt sql.NullTime
	err := ctx.AppDB().QueryRow(
		`SELECT response_json, created_at, expires_at
		   FROM web_cache
		  WHERE project_id=? AND kind=? AND cache_key=?`,
		projectID(ctx), p.Kind, p.Key,
	).Scan(&responseJSON, &createdAt, &expiresAt)
	if errors.Is(err, sql.ErrNoRows) {
		if p.ForceOnly {
			return nil, false, fmt.Errorf("cache miss for %s", p.Kind)
		}
		return nil, false, nil
	}
	if err != nil {
		return nil, false, err
	}
	now := time.Now().UTC()
	age := now.Sub(createdAt)
	if !p.ForceOnly {
		if p.MaxAge <= 0 || age > p.MaxAge {
			return nil, false, nil
		}
		if expiresAt.Valid && now.After(expiresAt.Time) {
			return nil, false, nil
		}
	}
	var out map[string]any
	if err := json.Unmarshal([]byte(responseJSON), &out); err != nil {
		return nil, false, err
	}
	_, _ = ctx.AppDB().Exec(
		`UPDATE web_cache SET last_accessed_at=?, hit_count=hit_count+1 WHERE project_id=? AND kind=? AND cache_key=?`,
		now, projectID(ctx), p.Kind, p.Key,
	)
	info := cacheInfo{
		Hit:             true,
		Mode:            p.Mode,
		CacheKey:        p.Key,
		AgeSeconds:      int64(age.Seconds()),
		MaxAgeSeconds:   int64(p.MaxAge.Seconds()),
		CacheTTLSeconds: int64(p.TTL.Seconds()),
		CachedAt:        createdAt.UTC().Format(time.RFC3339),
	}
	if expiresAt.Valid {
		info.ExpiresAt = expiresAt.Time.UTC().Format(time.RFC3339)
	}
	out["cache"] = info
	return out, true, nil
}

func applyCacheAfterFetch(ctx *sdk.AppCtx, p cachePolicy, out map[string]any) {
	info := cacheInfo{
		Hit:             false,
		Mode:            p.Mode,
		CacheKey:        p.Key,
		MaxAgeSeconds:   int64(p.MaxAge.Seconds()),
		CacheTTLSeconds: int64(p.TTL.Seconds()),
		Reason:          p.Reason,
	}
	if p.Write && !responseHasError(out) {
		if err := storeCachedResponse(ctx, p, out); err != nil {
			info.Reason = "cache store failed: " + err.Error()
		} else {
			info.Stored = true
		}
	}
	out["cache"] = info
}

func storeCachedResponse(ctx *sdk.AppCtx, p cachePolicy, out map[string]any) error {
	payload, err := cloneResponseForCache(out)
	if err != nil {
		return err
	}
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	if len(b) > maxCachedResponseBytes {
		return fmt.Errorf("cache response too large: %d bytes exceeds %d", len(b), maxCachedResponseBytes)
	}
	cacheURL, title := cacheResponseURLTitle(out)
	now := time.Now().UTC()
	var expires any
	if p.TTL > 0 {
		expires = now.Add(p.TTL)
	}
	_, err = ctx.AppDB().Exec(
		`INSERT INTO web_cache
			(project_id, kind, cache_key, request_json, response_json, url, title, created_at, expires_at, last_accessed_at, hit_count)
		 VALUES (?,?,?,?,?,?,?,?,?,?,0)
		 ON CONFLICT(project_id, kind, cache_key) DO UPDATE SET
			request_json=excluded.request_json,
			response_json=excluded.response_json,
			url=excluded.url,
			title=excluded.title,
			created_at=excluded.created_at,
			expires_at=excluded.expires_at,
			last_accessed_at=excluded.last_accessed_at,
			hit_count=0`,
		projectID(ctx), p.Kind, p.Key, p.RequestJSON, string(b), nullIfEmpty(cacheURL), nullIfEmpty(title), now, expires, now,
	)
	if err != nil {
		return err
	}
	return pruneCache(ctx)
}

func cacheKey(kind string, args map[string]any) (string, string, error) {
	cleaned := map[string]any{"kind": kind}
	for k, v := range args {
		if strings.HasPrefix(k, "_") {
			continue
		}
		switch k {
		case "cache", "max_age", "cache_ttl":
			continue
		case "store":
			if kind != "snapshot" {
				continue
			}
		}
		cleaned[k] = normalizeCacheValue(k, v)
	}
	if kind == "snapshot" {
		cleaned["store"] = boolArgDefault(args, "store", true)
		visibility, err := snapshotVisibility(args)
		if err != nil {
			return "", "", err
		}
		cleaned["visibility"] = visibility
	} else if (kind == "extract" && boolArg(args, "snapshot")) || (kind == "research" && boolArg(args, "snapshots")) {
		visibility, err := snapshotVisibility(args)
		if err != nil {
			return "", "", err
		}
		cleaned["visibility"] = visibility
	}
	b, err := json.Marshal(cleaned)
	if err != nil {
		return "", "", err
	}
	sum := sha256.Sum256(b)
	return string(b), hex.EncodeToString(sum[:]), nil
}

func normalizeCacheValue(key string, v any) any {
	switch x := v.(type) {
	case []string:
		cp := append([]string(nil), x...)
		if key == "formats" {
			sort.Strings(cp)
		}
		return cp
	case []any:
		cp := append([]any(nil), x...)
		if key == "formats" {
			ss := make([]string, 0, len(cp))
			for _, item := range cp {
				ss = append(ss, fmt.Sprint(item))
			}
			sort.Strings(ss)
			return ss
		}
		return cp
	case map[string]any:
		out := make(map[string]any, len(x))
		for k, v := range x {
			out[k] = normalizeCacheValue(k, v)
		}
		return out
	default:
		return v
	}
}

func cloneResponseForCache(out map[string]any) (map[string]any, error) {
	b, err := json.Marshal(out)
	if err != nil {
		return nil, err
	}
	var cp map[string]any
	if err := json.Unmarshal(b, &cp); err != nil {
		return nil, err
	}
	delete(cp, "cache")
	stripCachedTransientFields(cp)
	return cp, nil
}

func stripCachedTransientFields(v any) {
	switch x := v.(type) {
	case map[string]any:
		delete(x, "artifact")
		delete(x, "browser")
		delete(x, "snapshot")
		delete(x, "session_id")
		delete(x, "opened_session")
		delete(x, "png_b64")
		for key, child := range x {
			if key == "metadata" || key == "structured_data" {
				continue
			}
			stripCachedTransientFields(child)
		}
	case []any:
		for _, child := range x {
			stripCachedTransientFields(child)
		}
	}
}

func responseHasError(out map[string]any) bool {
	if errText, _ := out["error"].(string); errText != "" {
		return true
	}
	if page, ok := out["page"].(pageDoc); ok && page.Error != "" {
		return true
	}
	if page, ok := out["page"].(map[string]any); ok {
		if errText, _ := page["error"].(string); errText != "" {
			return true
		}
	}
	if confidence, _ := out["confidence"].(string); confidence == "none" {
		return true
	}
	return false
}

func (a *App) applyResponseEffects(ctx *sdk.AppCtx, runID int64, kind string, args, out map[string]any) error {
	delete(out, "artifact")
	switch kind {
	case "search":
		if !boolArg(args, "store") {
			return nil
		}
		query := stringFromAny(out["query"])
		art, err := storeJSONArtifact(ctx, runID, "search", stringFromAny(out["search_url"]), "search: "+query, out)
		if err != nil {
			return fmt.Errorf("store search artifact: %w", err)
		}
		out["artifact"] = art
		return nil

	case "extract":
		doc, err := pageDocFromAny(out["page"])
		if err != nil {
			return err
		}
		if boolArg(args, "snapshot") && doc.Error == "" && doc.Snapshot == nil {
			shotArgs := mapMerge(args, map[string]any{
				"url":        firstNonEmpty(doc.FinalURL, doc.URL),
				"session_id": "",
				"label":      firstNonEmpty(doc.Title, doc.URL),
				"store":      boolArgDefault(args, "store", storeDefault(ctx)),
			})
			shot, shotErr := a.snapshot(ctx, runID, shotArgs)
			if shotErr != nil {
				return fmt.Errorf("capture extraction snapshot: %w", shotErr)
			}
			doc.Snapshot = shot
		}
		if boolArgDefault(args, "store", storeDefault(ctx)) {
			art, storeErr := storePageArtifact(ctx, runID, &doc)
			if storeErr != nil {
				return fmt.Errorf("store page artifact: %w", storeErr)
			}
			doc.Artifact = art
		}
		out["page"] = doc
		return nil

	case "crawl", "map", "research":
		if kind == "research" && boolArg(args, "snapshots") {
			docs, err := pageDocsFromAny(out["sources"])
			if err != nil {
				return err
			}
			for i := range docs {
				if docs[i].Error != "" || docs[i].Snapshot != nil {
					continue
				}
				shotArgs := mapMerge(args, map[string]any{
					"url":        firstNonEmpty(docs[i].FinalURL, docs[i].URL),
					"session_id": "",
					"label":      firstNonEmpty(docs[i].Title, docs[i].URL),
					"store":      boolArgDefault(args, "store", storeDefault(ctx)),
				})
				shot, shotErr := a.snapshot(ctx, runID, shotArgs)
				if shotErr != nil {
					return fmt.Errorf("capture research snapshot for %s: %w", docs[i].URL, shotErr)
				}
				docs[i].Snapshot = shot
			}
			out["sources"] = docs
		}
		if !boolArgDefault(args, "store", storeDefault(ctx)) {
			return nil
		}
		artifactURL := ""
		title := kind
		if seeds := stringSliceFromAny(out["seeds"]); len(seeds) > 0 {
			artifactURL = seeds[0]
		}
		if kind == "research" {
			title = "research: " + stringFromAny(out["question"])
		}
		art, err := storeJSONArtifact(ctx, runID, kind, artifactURL, title, out)
		if err != nil {
			return fmt.Errorf("store %s artifact: %w", kind, err)
		}
		out["artifact"] = art
	}
	return nil
}

func pageDocFromAny(v any) (pageDoc, error) {
	if doc, ok := v.(pageDoc); ok {
		return doc, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return pageDoc{}, fmt.Errorf("encode cached page: %w", err)
	}
	var doc pageDoc
	if err := json.Unmarshal(b, &doc); err != nil {
		return pageDoc{}, fmt.Errorf("decode cached page: %w", err)
	}
	if doc.URL == "" {
		return pageDoc{}, errors.New("cached response has no page URL")
	}
	return doc, nil
}

func pageDocsFromAny(v any) ([]pageDoc, error) {
	if docs, ok := v.([]pageDoc); ok {
		return docs, nil
	}
	b, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encode cached pages: %w", err)
	}
	var docs []pageDoc
	if err := json.Unmarshal(b, &docs); err != nil {
		return nil, fmt.Errorf("decode cached pages: %w", err)
	}
	return docs, nil
}

func stringSliceFromAny(v any) []string {
	switch x := v.(type) {
	case []string:
		return x
	case []any:
		out := make([]string, 0, len(x))
		for _, item := range x {
			if s := stringFromAny(item); s != "" {
				out = append(out, s)
			}
		}
		return out
	default:
		return nil
	}
}

func pruneCache(ctx *sdk.AppCtx) error {
	if ctx == nil || ctx.AppDB() == nil {
		return nil
	}
	now := time.Now().UTC()
	if _, err := ctx.AppDB().Exec(`DELETE FROM web_cache WHERE expires_at IS NOT NULL AND expires_at < ?`, now); err != nil {
		return err
	}
	_, err := ctx.AppDB().Exec(
		`DELETE FROM web_cache
		  WHERE project_id=?
		    AND id NOT IN (
			SELECT id FROM web_cache WHERE project_id=?
			 ORDER BY last_accessed_at DESC, id DESC LIMIT 1000
		  )`,
		projectID(ctx), projectID(ctx),
	)
	return err
}

func cacheResponseURLTitle(out map[string]any) (string, string) {
	if page, ok := out["page"].(pageDoc); ok {
		return firstNonEmpty(page.FinalURL, page.URL), page.Title
	}
	if page, ok := out["page"].(map[string]any); ok {
		return stringFromAny(firstNonEmptyAny(page["final_url"], page["url"])), stringFromAny(page["title"])
	}
	if u := stringFromAny(out["url"]); u != "" {
		return u, stringFromAny(out["title"])
	}
	if q := stringFromAny(out["query"]); q != "" {
		return "", q
	}
	if q := stringFromAny(out["question"]); q != "" {
		return "", q
	}
	return "", ""
}

func defaultMaxAge(kind string) time.Duration {
	switch kind {
	case "search":
		return defaultSearchMaxAge
	case "extract", "crawl", "map":
		return defaultExtractMaxAge
	case "research":
		return defaultResearchMaxAge
	default:
		return 0
	}
}

func defaultCacheTTL(kind string) time.Duration {
	switch kind {
	case "search":
		return defaultSearchTTL
	case "extract", "crawl", "map":
		return defaultExtractTTL
	case "research":
		return defaultResearchTTL
	default:
		return 0
	}
}

func cacheDurationArg(args map[string]any, key string, fallback time.Duration) (time.Duration, bool) {
	if _, ok := args[key]; !ok {
		return fallback, false
	}
	return time.Duration(intArg(args, key)) * time.Second, true
}

// ─── Retrieval and parsing ────────────────────────────────────────

type fetchInfo map[string]any

func fetchURL(ctx *sdk.AppCtx, target string) ([]byte, fetchInfo, error) {
	if err := validateBrowserTarget(ctx, target); err != nil {
		return nil, nil, err
	}
	reqCtx, cancel := context.WithTimeout(context.Background(), defaultHTTPTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodGet, target, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", "Apteva-Web/0.1 (+https://github.com/apteva/apps)")
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,text/plain;q=0.8,*/*;q=0.5")
	start := time.Now()
	client := outboundHTTPClient(ctx)
	defer client.CloseIdleConnections()
	resp, err := client.Do(req)
	if err != nil {
		return nil, fetchInfo{"duration_ms": time.Since(start).Milliseconds()}, err
	}
	defer resp.Body.Close()
	limited := io.LimitReader(resp.Body, maxFetchBytes+1)
	body, err := io.ReadAll(limited)
	if err != nil {
		return nil, nil, err
	}
	truncated := len(body) > maxFetchBytes
	if truncated {
		body = body[:maxFetchBytes]
	}
	meta := fetchInfo{
		"status":       resp.StatusCode,
		"content_type": resp.Header.Get("Content-Type"),
		"bytes":        len(body),
		"truncated":    truncated,
		"duration_ms":  time.Since(start).Milliseconds(),
		"final_url":    resp.Request.URL.String(),
	}
	if resp.StatusCode >= 400 {
		return body, meta, fmt.Errorf("HTTP %d fetching %s", resp.StatusCode, target)
	}
	return body, meta, nil
}

type parsedHTML struct {
	Title       string
	Description string
	Text        string
	Markdown    string
	Links       []linkInfo
	Images      []string
	Metadata    map[string]any
}

func parseHTMLDoc(body []byte, base string) parsedHTML {
	root, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		text := cleanText(string(body))
		return parsedHTML{Text: text, Markdown: text, Metadata: map[string]any{"parse_error": err.Error()}}
	}
	var out parsedHTML
	out.Metadata = map[string]any{}
	var textParts []string
	var mdParts []string
	var links []linkInfo
	var images []string

	var walk func(*html.Node, bool)
	walk = func(n *html.Node, skip bool) {
		if n.Type == html.ElementNode {
			switch strings.ToLower(n.Data) {
			case "script", "style", "noscript", "svg":
				skip = true
			case "title":
				out.Title = cleanText(nodeText(n))
			case "meta":
				name := strings.ToLower(firstNonEmpty(attr(n, "name"), attr(n, "property")))
				content := strings.TrimSpace(attr(n, "content"))
				if content != "" && name != "" {
					out.Metadata[name] = content
					if name == "description" || name == "og:description" {
						out.Description = firstNonEmpty(out.Description, content)
					}
					if name == "og:title" {
						out.Title = firstNonEmpty(out.Title, content)
					}
				}
			case "a":
				href := absoluteURL(base, attr(n, "href"))
				label := cleanText(nodeText(n))
				if href != "" && strings.HasPrefix(href, "http") {
					links = append(links, linkInfo{URL: href, Text: truncateString(label, 160)})
				}
			case "img":
				src := absoluteURL(base, attr(n, "src"))
				if src != "" {
					images = append(images, src)
				}
			case "h1", "h2", "h3":
				t := cleanText(nodeText(n))
				if t != "" {
					mdParts = append(mdParts, strings.Repeat("#", headingLevel(n.Data))+" "+t)
				}
			case "p", "li":
				t := cleanText(nodeText(n))
				if t != "" {
					mdParts = append(mdParts, t)
				}
			}
		}
		if n.Type == html.TextNode && !skip {
			t := cleanText(n.Data)
			if t != "" {
				textParts = append(textParts, t)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c, skip)
		}
	}
	walk(root, false)
	out.Text = cleanText(strings.Join(textParts, " "))
	out.Markdown = strings.Join(dedupeStrings(mdParts, 400), "\n\n")
	out.Links = dedupeLinks(links, 500)
	out.Images = dedupeStrings(images, 100)
	return out
}

func parseDuckDuckGo(body []byte, limit int) []searchResult {
	root, err := html.Parse(bytes.NewReader(body))
	if err != nil {
		return nil
	}
	var results []searchResult
	seen := map[string]bool{}
	var walk func(*html.Node)
	walk = func(n *html.Node) {
		if len(results) >= limit {
			return
		}
		if n.Type == html.ElementNode && n.Data == "a" {
			href := decodeDuckURL(attr(n, "href"))
			title := cleanText(nodeText(n))
			if href != "" && title != "" && !seen[href] && isLikelyResultURL(href) {
				seen[href] = true
				results = append(results, searchResult{
					Title:      truncateString(title, 240),
					URL:        href,
					Source:     "duckduckgo",
					Rank:       len(results) + 1,
					Confidence: "medium",
				})
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(root)
	return results
}

func parseDuckDuckGoSearch(extracted *browserExtractResult, limit int) []searchResult {
	if extracted == nil {
		return []searchResult{}
	}
	if strings.TrimSpace(extracted.HTML) != "" {
		if results := parseDuckDuckGo([]byte(extracted.HTML), limit); len(results) > 0 {
			return results
		}
	}
	results := make([]searchResult, 0, limit)
	seen := map[string]bool{}
	for _, l := range extracted.Links {
		if len(results) >= limit {
			break
		}
		href := decodeDuckURL(l.URL)
		title := cleanText(l.Text)
		if href == "" || title == "" || seen[href] || !isLikelyResultURL(href) {
			continue
		}
		seen[href] = true
		results = append(results, searchResult{
			Title:      truncateString(title, 240),
			URL:        href,
			Source:     "duckduckgo",
			Rank:       len(results) + 1,
			Confidence: "medium",
		})
	}
	if results == nil {
		return []searchResult{}
	}
	return results
}

func parseSearchResults(engine string, extracted *browserExtractResult, limit int) []searchResult {
	switch engine {
	case "google":
		return parseGoogleSearch(extracted, limit)
	default:
		return parseDuckDuckGoSearch(extracted, limit)
	}
}

func parseGoogleSearch(extracted *browserExtractResult, limit int) []searchResult {
	if extracted == nil {
		return []searchResult{}
	}
	results := make([]searchResult, 0, limit)
	seen := map[string]bool{}
	for _, l := range extracted.Links {
		if len(results) >= limit {
			break
		}
		href := decodeGoogleURL(l.URL)
		title := cleanGoogleResultTitle(l.Text, href)
		if href == "" || title == "" || seen[href] || !isLikelyGoogleResultURL(href) {
			continue
		}
		seen[href] = true
		results = append(results, searchResult{
			Title:      truncateString(title, 240),
			URL:        href,
			Source:     "google",
			Rank:       len(results) + 1,
			Confidence: "medium",
		})
	}
	if results == nil {
		return []searchResult{}
	}
	return results
}

func detectSearchBlocked(engine string, extracted *browserExtractResult) string {
	if extracted == nil {
		return ""
	}
	haystack := strings.ToLower(strings.Join([]string{
		extracted.Title,
		extracted.Description,
		extracted.Text,
		extracted.HTML,
		fmt.Sprint(extracted.Metadata),
	}, "\n"))
	switch engine {
	case "google":
		switch {
		case strings.Contains(haystack, "our systems have detected unusual traffic"):
			return "search_blocked: google returned an anti-bot challenge"
		case strings.Contains(haystack, "/sorry/") || strings.Contains(haystack, "google.com/sorry"):
			return "search_blocked: google returned an anti-bot challenge"
		case strings.Contains(haystack, "to continue, please type the characters below"):
			return "search_blocked: google returned an anti-bot challenge"
		}
	default:
		switch {
		case strings.Contains(haystack, "unfortunately, bots use duckduckgo too"):
			return "search_blocked: duckduckgo returned an anti-bot challenge"
		case strings.Contains(haystack, "error-lite@duckduckgo.com"):
			return "search_blocked: duckduckgo returned an anti-bot challenge"
		case strings.Contains(haystack, "anomaly-modal") || strings.Contains(haystack, "anomaly.js"):
			return "search_blocked: duckduckgo returned an anti-bot challenge"
		}
	}
	return ""
}

func searchResultsFromOutput(out any) []searchResult {
	m, ok := out.(map[string]any)
	if !ok {
		return nil
	}
	switch results := m["results"].(type) {
	case []searchResult:
		return results
	case []any:
		out := make([]searchResult, 0, len(results))
		for _, item := range results {
			switch r := item.(type) {
			case searchResult:
				out = append(out, r)
			case map[string]any:
				u := stringFromAny(r["url"])
				if u == "" {
					continue
				}
				out = append(out, searchResult{
					Title:      stringFromAny(r["title"]),
					URL:        u,
					Snippet:    stringFromAny(r["snippet"]),
					Source:     firstNonEmpty(stringFromAny(r["source"]), "duckduckgo"),
					Rank:       intFromAny(r["rank"]),
					FetchedAt:  stringFromAny(r["fetched_at"]),
					Confidence: stringFromAny(r["confidence"]),
				})
			}
		}
		return out
	default:
		return nil
	}
}

// ─── Storage and DB ───────────────────────────────────────────────

func recoverInterruptedRuns(ctx *sdk.AppCtx) error {
	_, err := ctx.AppDB().Exec(
		`UPDATE web_runs
		    SET status='failed', error='interrupted by app restart', completed_at=?
		  WHERE status='running'`,
		time.Now().UTC(),
	)
	return err
}

func pruneHistory(ctx *sdk.AppCtx) error {
	days := defaultHistoryRetention
	if raw := configString(ctx, "history_retention_days"); raw != "" {
		days = configInt(ctx, "history_retention_days")
	}
	if days <= 0 {
		return nil
	}
	cutoff := time.Now().UTC().Add(-time.Duration(days) * 24 * time.Hour)
	if _, err := ctx.AppDB().Exec(`DELETE FROM web_artifacts WHERE created_at < ?`, cutoff); err != nil {
		return err
	}
	_, err := ctx.AppDB().Exec(`DELETE FROM web_runs WHERE created_at < ?`, cutoff)
	return err
}

func startRun(ctx *sdk.AppCtx, kind string, input any) (int64, error) {
	b, _ := json.Marshal(input)
	res, err := ctx.AppDB().Exec(
		`INSERT INTO web_runs (project_id, kind, input_json, status) VALUES (?,?,?,'running')`,
		projectID(ctx), kind, string(b),
	)
	if err != nil {
		return 0, err
	}
	id, _ := res.LastInsertId()
	return id, nil
}

func completeRun(ctx *sdk.AppCtx, runID int64, status string, output any, runErr error) {
	if runID <= 0 {
		return
	}
	var outText string
	if output != nil {
		if b, err := json.Marshal(compactRunOutput(output)); err == nil {
			outText = string(b)
		}
	}
	summary := runSummaryFromOutput(output)
	errText := ""
	if runErr != nil {
		errText = runErr.Error()
	}
	_, _ = ctx.AppDB().Exec(
		`UPDATE web_runs SET status=?, output_json=?, summary=?, error=?, completed_at=? WHERE id=?`,
		status, nullIfEmpty(outText), nullIfEmpty(summary), nullIfEmpty(errText), time.Now().UTC(), runID,
	)
}

func failRunOnPanic(ctx *sdk.AppCtx, runID int64) {
	if r := recover(); r != nil {
		completeRun(ctx, runID, "failed", nil, fmt.Errorf("panic: %v", r))
		panic(r)
	}
}

func storePageArtifact(ctx *sdk.AppCtx, runID int64, doc *pageDoc) (*artifactSummary, error) {
	payload := map[string]any{
		"url":             doc.URL,
		"final_url":       doc.FinalURL,
		"title":           doc.Title,
		"description":     doc.Description,
		"text":            doc.Text,
		"markdown":        doc.Markdown,
		"html":            doc.HTML,
		"links":           doc.Links,
		"metadata":        doc.Metadata,
		"structured_data": doc.StructuredData,
	}
	return storeArtifact(ctx, runID, "page", doc.URL, doc.Title, "application/json", payload)
}

func storeJSONArtifact(ctx *sdk.AppCtx, runID int64, kind, artifactURL, title string, payload any) (*artifactSummary, error) {
	return storeArtifact(ctx, runID, kind, artifactURL, title, "application/json", payload)
}

func storeArtifact(ctx *sdk.AppCtx, runID int64, kind, artifactURL, title, contentType string, payload any) (*artifactSummary, error) {
	b, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	folder := "/.web/" + kind + "/" + time.Now().UTC().Format("2006-01")
	var up struct {
		ID  int64  `json:"id"`
		URL string `json:"url"`
	}
	name := safeFilename(firstNonEmpty(title, kind)) + "-" + randName() + ".json"
	args := withProjectID(ctx, map[string]any{
		"name":           name,
		"content_base64": base64.StdEncoding.EncodeToString(b),
		"folder":         folder,
		"content_type":   contentType,
		"source":         "web:" + kind,
	})
	if err := ctx.PlatformAPI().CallAppResult("storage", "files_upload", args, &up); err != nil {
		return nil, fmt.Errorf("storage.files_upload: %w", err)
	}
	art, err := insertArtifact(ctx, runID, kind, artifactURL, title, up.ID, up.URL, contentType, len(b), compactArtifactMetadata(kind, artifactURL, title, contentType, len(b)))
	if err != nil {
		rollbackStoredFile(ctx, up.ID)
		return nil, fmt.Errorf("record %s artifact: %w", kind, err)
	}
	return art, nil
}

func rollbackStoredFile(ctx *sdk.AppCtx, storageID int64) {
	if ctx == nil || storageID <= 0 {
		return
	}
	var out map[string]any
	if err := ctx.PlatformAPI().CallAppResult("storage", "files_delete", withProjectID(ctx, map[string]any{"id": storageID}), &out); err != nil {
		ctx.Logger().Warn("rollback uploaded Web artifact failed", "storage_id", storageID, "err", err.Error())
	}
}

func compactArtifactMetadata(kind, artifactURL, title, contentType string, size int) map[string]any {
	return map[string]any{
		"kind":         kind,
		"url":          artifactURL,
		"title":        title,
		"content_type": contentType,
		"bytes":        size,
	}
}

func insertArtifact(ctx *sdk.AppCtx, runID int64, kind, artifactURL, title string, storageID int64, storageURL, contentType string, size int, metadata any) (*artifactSummary, error) {
	metaBytes, _ := json.Marshal(metadata)
	res, err := ctx.AppDB().Exec(
		`INSERT INTO web_artifacts (project_id, run_id, kind, url, title, storage_id, storage_url, content_type, bytes, metadata_json)
		 VALUES (?,?,?,?,?,?,?,?,?,?)`,
		projectID(ctx), nullInt64(runID), kind, nullIfEmpty(artifactURL), nullIfEmpty(title), nullInt64(storageID), nullIfEmpty(storageURL), contentType, size, string(metaBytes),
	)
	if err != nil {
		return nil, err
	}
	id, _ := res.LastInsertId()
	return &artifactSummary{ID: id, StorageID: storageID, URL: storageURL}, nil
}

func listRuns(ctx *sdk.AppCtx, limit, offset int) ([]map[string]any, error) {
	rows, err := ctx.AppDB().Query(
		`SELECT id, kind, status, COALESCE(error,''), COALESCE(summary,''),
		        created_at, completed_at, output_json, COALESCE(extractor_id,0),
		        COALESCE(extractor_revision,0), cancel_requested_at
		 FROM web_runs
		 WHERE project_id = ?
		 ORDER BY created_at DESC
		 LIMIT ? OFFSET ?`,
		projectID(ctx), limit, offset,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id int64
		var kind, status, errText, summary string
		var created time.Time
		var completed sql.NullTime
		var outputJSON sql.NullString
		var extractorID, extractorRevision int64
		var cancelRequested sql.NullTime
		if err := rows.Scan(&id, &kind, &status, &errText, &summary, &created, &completed, &outputJSON, &extractorID, &extractorRevision, &cancelRequested); err != nil {
			return nil, err
		}
		run := map[string]any{
			"id":         id,
			"kind":       kind,
			"status":     status,
			"error":      errText,
			"summary":    summary,
			"created_at": created.Format(time.RFC3339),
		}
		if completed.Valid {
			run["completed_at"] = completed.Time.UTC().Format(time.RFC3339)
			run["duration_ms"] = maxInt64(0, completed.Time.Sub(created).Milliseconds())
		}
		if extractorID > 0 {
			run["extractor_id"] = extractorID
			run["extractor_revision"] = extractorRevision
		}
		if cancelRequested.Valid {
			run["cancel_requested_at"] = cancelRequested.Time.UTC().Format(time.RFC3339)
		}
		if outputJSON.Valid && outputJSON.String != "" {
			var details map[string]any
			if json.Unmarshal([]byte(outputJSON.String), &details) == nil {
				run["details"] = details
			}
		}
		out = append(out, run)
	}
	return out, rows.Err()
}

func countRuns(ctx *sdk.AppCtx) (int, error) {
	var count int
	err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM web_runs WHERE project_id=?`, projectID(ctx)).Scan(&count)
	return count, err
}

// ─── HTTP handlers ────────────────────────────────────────────────

func (a *App) handleRuns(w http.ResponseWriter, r *http.Request) {
	if globalCtx == nil {
		httpErr(w, http.StatusServiceUnavailable, "web app is not mounted")
		return
	}
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	offset := 0
	if v := r.URL.Query().Get("offset"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n >= 0 {
			offset = n
		}
	}
	ctx := globalCtx
	if pid := strings.TrimSpace(r.URL.Query().Get("project_id")); pid != "" {
		ctx = globalCtx.WithProject(pid)
	}
	runs, err := listRuns(ctx, limit, offset)
	if err != nil {
		writeJSON(w, nil, err)
		return
	}
	total, err := countRuns(ctx)
	writeJSON(w, map[string]any{"runs": runs, "total": total, "limit": limit, "offset": offset}, err)
}

// ─── Utility ──────────────────────────────────────────────────────

func schemaObject(props map[string]any, req []string) map[string]any {
	m := map[string]any{"type": "object", "properties": props}
	if len(req) > 0 {
		m["required"] = req
	}
	return m
}

func viewportSchema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"width":  map[string]any{"type": "integer"},
			"height": map[string]any{"type": "integer"},
		},
	}
}

func cacheModeSchema() map[string]any {
	return map[string]any{
		"type":        "string",
		"enum":        []string{"auto", "bypass", "refresh", "force"},
		"description": "auto uses a fresh-enough cached response when available; bypass ignores and does not write cache; refresh fetches live and updates cache; force returns only cached data and errors on miss.",
	}
}

func cacheSecondsSchema(description string) map[string]any {
	return map[string]any{
		"type":        "integer",
		"minimum":     0,
		"description": description,
	}
}

func duckDuckGoURL(query string) string {
	return "https://duckduckgo.com/html/?" + url.Values{"q": []string{query}}.Encode()
}

func googleURL(query string) string {
	return "https://www.google.com/search?" + url.Values{"q": []string{query}}.Encode()
}

func searchEngineURL(engine, query string) string {
	switch engine {
	case "google":
		return googleURL(query)
	default:
		return duckDuckGoURL(query)
	}
}

func decodeDuckURL(href string) string {
	if href == "" {
		return ""
	}
	if strings.HasPrefix(href, "//") {
		href = "https:" + href
	}
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if hostIsOrSubdomain(u.Hostname(), "duckduckgo.com") {
		if uddg := u.Query().Get("uddg"); uddg != "" {
			return uddg
		}
		return ""
	}
	if u.Scheme == "http" || u.Scheme == "https" {
		return u.String()
	}
	return ""
}

func decodeGoogleURL(href string) string {
	if href == "" {
		return ""
	}
	if strings.HasPrefix(href, "//") {
		href = "https:" + href
	}
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return ""
	}
	host := strings.ToLower(u.Hostname())
	if isGoogleOwnedHost(host) {
		for _, key := range []string{"q", "url"} {
			if raw := u.Query().Get(key); raw != "" {
				return raw
			}
		}
		return ""
	}
	return u.String()
}

func isLikelyResultURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host != "" && !hostIsOrSubdomain(host, "duckduckgo.com")
}

func isLikelyGoogleResultURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if host == "" {
		return false
	}
	return !isGoogleOwnedHost(host)
}

func hostIsOrSubdomain(host, domain string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	domain = strings.TrimSuffix(strings.ToLower(domain), ".")
	return host == domain || strings.HasSuffix(host, "."+domain)
}

func isGoogleOwnedHost(host string) bool {
	host = strings.TrimSuffix(strings.ToLower(host), ".")
	root, err := publicsuffix.EffectiveTLDPlusOne(host)
	if err != nil {
		return false
	}
	label := strings.SplitN(root, ".", 2)[0]
	switch label {
	case "google", "gstatic", "googleusercontent":
		return true
	default:
		return false
	}
}

func cleanGoogleResultTitle(text, href string) string {
	text = cleanText(text)
	if text == "" {
		return ""
	}
	if strings.HasPrefix(strings.ToLower(text), "read more") {
		return ""
	}
	if strings.Contains(text, " - View related links") {
		text = strings.TrimSpace(strings.Split(text, " - View related links")[0])
	}
	if strings.Contains(text, " Opens in new tab.") {
		text = strings.TrimSpace(strings.ReplaceAll(text, " Opens in new tab.", ""))
	}
	if u, err := url.Parse(href); err == nil {
		host := strings.TrimPrefix(strings.ToLower(u.Hostname()), "www.")
		if host != "" {
			for _, sep := range []string{" https://" + host, " http://" + host, host + " https://", host + " http://"} {
				if idx := strings.Index(strings.ToLower(text), sep); idx > 0 {
					text = strings.TrimSpace(text[:idx])
					break
				}
			}
		}
	}
	return text
}

func validateHTTPURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil {
		return err
	}
	if u.Scheme != "http" && u.Scheme != "https" {
		return fmt.Errorf("unsupported URL scheme %q", u.Scheme)
	}
	if u.Host == "" {
		return errors.New("url host required")
	}
	if u.User != nil {
		return errors.New("url credentials are not allowed")
	}
	return nil
}

func validateBrowserTarget(ctx *sdk.AppCtx, raw string) error {
	if err := validateHTTPURL(raw); err != nil {
		return err
	}
	if allowPrivateNetworks(ctx) {
		return nil
	}
	u, _ := url.Parse(raw)
	host := strings.TrimSuffix(strings.ToLower(u.Hostname()), ".")
	if host == "localhost" || strings.HasSuffix(host, ".localhost") {
		return fmt.Errorf("private network URL blocked: %s", host)
	}
	if addr, err := netip.ParseAddr(host); err == nil && isBlockedAddress(addr) {
		return fmt.Errorf("private network URL blocked: %s", host)
	}
	return nil
}

func allowPrivateNetworks(ctx *sdk.AppCtx) bool {
	return boolArgDefault(map[string]any{"enabled": configString(ctx, "allow_private_networks")}, "enabled", false)
}

func isBlockedAddress(addr netip.Addr) bool {
	if !addr.IsValid() {
		return true
	}
	addr = addr.WithZone("").Unmap()
	if !addr.IsGlobalUnicast() {
		return true
	}
	for _, prefix := range blockedNetworkPrefixes {
		if prefix.Contains(addr) {
			return true
		}
	}
	return false
}

func publicAddresses(ctx context.Context, host string) ([]netip.Addr, error) {
	if addr, err := netip.ParseAddr(strings.Trim(host, "[]")); err == nil {
		if isBlockedAddress(addr) {
			return nil, fmt.Errorf("private network address blocked: %s", host)
		}
		return []netip.Addr{addr.Unmap()}, nil
	}
	addresses, err := net.DefaultResolver.LookupNetIP(ctx, "ip", host)
	if err != nil {
		return nil, fmt.Errorf("resolve %s: %w", host, err)
	}
	if len(addresses) == 0 {
		return nil, fmt.Errorf("resolve %s: no addresses", host)
	}
	for _, addr := range addresses {
		if isBlockedAddress(addr) {
			return nil, fmt.Errorf("private network address blocked for %s: %s", host, addr)
		}
	}
	return addresses, nil
}

func outboundHTTPClient(appCtx *sdk.AppCtx) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	dialer := &net.Dialer{Timeout: defaultHTTPTimeout, KeepAlive: 30 * time.Second}
	if allowPrivateNetworks(appCtx) {
		transport.DialContext = dialer.DialContext
	} else {
		transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
			host, port, err := net.SplitHostPort(address)
			if err != nil {
				return nil, err
			}
			addresses, err := publicAddresses(ctx, host)
			if err != nil {
				return nil, err
			}
			var dialErrs []error
			for _, addr := range addresses {
				if strings.HasSuffix(network, "4") && !addr.Is4() || strings.HasSuffix(network, "6") && !addr.Is6() {
					continue
				}
				conn, dialErr := dialer.DialContext(ctx, network, net.JoinHostPort(addr.String(), port))
				if dialErr == nil {
					return conn, nil
				}
				dialErrs = append(dialErrs, dialErr)
			}
			if len(dialErrs) == 0 {
				return nil, fmt.Errorf("no %s addresses available for %s", network, host)
			}
			return nil, errors.Join(dialErrs...)
		}
	}
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(req *http.Request, via []*http.Request) error {
			if len(via) >= 10 {
				return errors.New("stopped after 10 redirects")
			}
			return validateBrowserTarget(appCtx, req.URL.String())
		},
	}
}

func absoluteURL(base, href string) string {
	href = strings.TrimSpace(href)
	if href == "" || strings.HasPrefix(href, "#") || strings.HasPrefix(strings.ToLower(href), "javascript:") || strings.HasPrefix(strings.ToLower(href), "mailto:") {
		return ""
	}
	b, err := url.Parse(base)
	if err != nil {
		return ""
	}
	u, err := url.Parse(href)
	if err != nil {
		return ""
	}
	return b.ResolveReference(u).String()
}

func canonicalURL(raw string) string {
	u, err := url.Parse(raw)
	if err != nil {
		return raw
	}
	u.Fragment = ""
	return strings.TrimRight(u.String(), "/")
}

func shouldFollow(raw string, seedHosts map[string]bool, sameHost bool) bool {
	if err := validateHTTPURL(raw); err != nil {
		return false
	}
	if !sameHost {
		return true
	}
	u, _ := url.Parse(raw)
	return seedHosts[strings.ToLower(u.Hostname())]
}

func seedURLs(args map[string]any) []string {
	var out []string
	if u := strings.TrimSpace(stringArg(args, "url")); u != "" {
		out = append(out, u)
	}
	out = append(out, stringSliceArg(args, "urls")...)
	return dedupeStrings(out, 20)
}

func validateSeedURLs(seeds []string) error {
	for _, seed := range seeds {
		if err := validateHTTPURL(seed); err != nil {
			return fmt.Errorf("invalid seed URL %q: %w", seed, err)
		}
	}
	return nil
}

func aggregateExtractionBackend(pages []pageDoc) string {
	seen := map[string]bool{}
	for _, page := range pages {
		if page.Error == "" && page.ExtractionBackend != "" {
			seen[page.ExtractionBackend] = true
		}
	}
	if len(seen) == 0 {
		return "none"
	}
	if len(seen) > 1 {
		return "mixed"
	}
	for backend := range seen {
		return backend
	}
	return "none"
}

func nodeText(n *html.Node) string {
	var parts []string
	var walk func(*html.Node)
	walk = func(x *html.Node) {
		if x.Type == html.TextNode {
			parts = append(parts, x.Data)
		}
		for c := x.FirstChild; c != nil; c = c.NextSibling {
			walk(c)
		}
	}
	walk(n)
	return strings.Join(parts, " ")
}

func attr(n *html.Node, key string) string {
	for _, a := range n.Attr {
		if strings.EqualFold(a.Key, key) {
			return a.Val
		}
	}
	return ""
}

func headingLevel(tag string) int {
	switch strings.ToLower(tag) {
	case "h1":
		return 1
	case "h2":
		return 2
	default:
		return 3
	}
}

func cleanText(s string) string {
	return strings.Join(strings.Fields(strings.ReplaceAll(s, "\u00a0", " ")), " ")
}

func bestExcerpt(text, question string) string {
	text = cleanText(text)
	if text == "" {
		return ""
	}
	terms := strings.Fields(strings.ToLower(question))
	sentences := strings.FieldsFunc(text, func(r rune) bool { return r == '.' || r == '!' || r == '?' || r == '\n' })
	best := ""
	bestScore := -1
	for _, s := range sentences {
		low := strings.ToLower(s)
		score := 0
		for _, t := range terms {
			if len(t) > 3 && strings.Contains(low, t) {
				score++
			}
		}
		if score > bestScore {
			bestScore = score
			best = s
		}
	}
	return truncateString(cleanText(best), 500)
}

func synthesizeExtractiveAnswer(question string, citations []map[string]any) string {
	if len(citations) == 0 {
		return "No usable browser-backed sources were extracted for this question."
	}
	var b strings.Builder
	fmt.Fprintf(&b, "Browser-backed research for %q reviewed %d source(s). ", question, len(citations))
	b.WriteString("Key evidence is available in the citations array. The report is extractive rather than an LLM-written conclusion.")
	return b.String()
}

func confidenceForSources(sources []pageDoc) string {
	ok := 0
	for _, s := range sources {
		if s.Error == "" && s.Text != "" {
			ok++
		}
	}
	switch {
	case ok >= 4:
		return "medium"
	case ok >= 1:
		return "low"
	default:
		return "none"
	}
}

func researchGaps(sources []pageDoc) []string {
	var gaps []string
	for _, s := range sources {
		if s.Error != "" {
			gaps = append(gaps, "Could not extract "+s.URL+": "+s.Error)
		}
	}
	if len(gaps) == 0 {
		return nil
	}
	return gaps
}

func stringArg(m map[string]any, k string) string {
	if v, ok := m[k]; ok {
		switch x := v.(type) {
		case string:
			return x
		case fmt.Stringer:
			return x.String()
		}
	}
	return ""
}

func intArg(m map[string]any, k string) int {
	v, ok := m[k]
	if !ok {
		return 0
	}
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	case json.Number:
		n, _ := x.Int64()
		return int(n)
	case string:
		n, _ := strconv.Atoi(x)
		return n
	default:
		return 0
	}
}

func boolArg(m map[string]any, k string) bool {
	return boolArgDefault(m, k, false)
}

func boolArgDefault(m map[string]any, k string, def bool) bool {
	v, ok := m[k]
	if !ok {
		return def
	}
	switch x := v.(type) {
	case bool:
		return x
	case string:
		switch strings.ToLower(strings.TrimSpace(x)) {
		case "1", "true", "yes", "on":
			return true
		case "0", "false", "no", "off":
			return false
		}
	}
	return def
}

func stringSliceArg(m map[string]any, k string) []string {
	v, ok := m[k]
	if !ok {
		return nil
	}
	var out []string
	switch x := v.(type) {
	case []string:
		out = append(out, x...)
	case []any:
		for _, item := range x {
			if s, ok := item.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
	}
	return out
}

func boundedInt(got, def, min, max int) int {
	if got == 0 {
		got = def
	}
	if got < min {
		return min
	}
	if got > max {
		return max
	}
	return got
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func maxInt64(a, b int64) int64 {
	if a > b {
		return a
	}
	return b
}

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func firstNonEmptyAny(vals ...any) any {
	for _, v := range vals {
		if stringFromAny(v) != "" {
			return v
		}
	}
	return nil
}

func stringFromAny(v any) string {
	switch x := v.(type) {
	case string:
		return strings.TrimSpace(x)
	case fmt.Stringer:
		return strings.TrimSpace(x.String())
	default:
		return ""
	}
}

func intFromAny(v any) int {
	switch x := v.(type) {
	case int:
		return x
	case int64:
		return int(x)
	case float64:
		return int(x)
	case json.Number:
		n, _ := strconv.Atoi(x.String())
		return n
	default:
		return 0
	}
}

func truncateString(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	end := max
	for end > 0 && !utf8.RuneStart(s[end]) {
		end--
	}
	return s[:end]
}

func dedupeStrings(in []string, limit int) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(in))
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func dedupeLinks(in []linkInfo, limit int) []linkInfo {
	seen := map[string]bool{}
	out := make([]linkInfo, 0, len(in))
	for _, l := range in {
		if l.URL == "" || seen[l.URL] {
			continue
		}
		seen[l.URL] = true
		out = append(out, l)
		if limit > 0 && len(out) >= limit {
			break
		}
	}
	return out
}

func mapMerge(a, b map[string]any) map[string]any {
	out := make(map[string]any, len(a)+len(b))
	for k, v := range a {
		out[k] = v
	}
	for k, v := range b {
		out[k] = v
	}
	return out
}

func withProjectID(ctx *sdk.AppCtx, args map[string]any) map[string]any {
	out := make(map[string]any, len(args)+1)
	for k, v := range args {
		out[k] = v
	}
	if pid := ctx.CurrentProject(); pid != "" {
		out["_project_id"] = pid
	}
	return out
}

func projectID(ctx *sdk.AppCtx) string {
	if pid := ctx.CurrentProject(); pid != "" {
		return pid
	}
	return "global"
}

func configString(ctx *sdk.AppCtx, key string) string {
	if ctx == nil || ctx.Config() == nil {
		return ""
	}
	return strings.TrimSpace(ctx.Config().Get(key))
}

func configInt(ctx *sdk.AppCtx, key string) int {
	v := configString(ctx, key)
	if v == "" {
		return 0
	}
	n, _ := strconv.Atoi(v)
	return n
}

func storeDefault(ctx *sdk.AppCtx) bool {
	v := configString(ctx, "store_artifacts_by_default")
	if v == "" {
		return true
	}
	return boolArgDefault(map[string]any{"v": v}, "v", true)
}

func snapshotStore(ctx *sdk.AppCtx, args map[string]any) bool {
	if _, ok := args["_snapshot_store"]; ok {
		return boolArgDefault(args, "_snapshot_store", false)
	}
	return boolArgDefault(args, "store", storeDefault(ctx))
}

func snapshotVisibilitySchema() map[string]any {
	return map[string]any{
		"type":        "string",
		"enum":        []string{"private", "signed", "public"},
		"description": "Visibility for screenshots stored by this request. Defaults to private.",
	}
}

func snapshotVisibility(args map[string]any) (string, error) {
	visibility := strings.ToLower(strings.TrimSpace(stringArg(args, "visibility")))
	if visibility == "" {
		return "private", nil
	}
	switch visibility {
	case "private", "signed", "public":
		return visibility, nil
	default:
		return "", errors.New("visibility must be one of: private, signed, public")
	}
}

func nestedSearchCacheMode(args map[string]any) string {
	switch strings.ToLower(strings.TrimSpace(stringArg(args, "cache"))) {
	case "bypass":
		return "bypass"
	case "refresh":
		return "refresh"
	default:
		return "auto"
	}
}

func encodedBase64Size(value string) int {
	size := len(value) * 3 / 4
	if strings.HasSuffix(value, "==") {
		return maxInt(0, size-2)
	}
	if strings.HasSuffix(value, "=") {
		return maxInt(0, size-1)
	}
	return size
}

func maxInt(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func nullIfEmpty(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullInt64(n int64) any {
	if n <= 0 {
		return nil
	}
	return n
}

func intFromMap(m map[string]any, k string) int {
	return intArg(m, k)
}

func stringFromMap(m map[string]any, k string) string {
	return stringArg(m, k)
}

func boolFromMap(m map[string]any, k string) bool {
	return boolArg(m, k)
}

func randName() string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		return strconv.FormatInt(time.Now().UnixNano(), 36)
	}
	return hex.EncodeToString(b[:])
}

func safeFilename(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == '-' || r == '_':
			b.WriteRune(r)
		case r == ' ' || r == '.' || r == '/':
			b.WriteByte('-')
		}
		if b.Len() >= 48 {
			break
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "web"
	}
	return out
}

func runSummaryFromOutput(output any) string {
	m := mapFromAny(output)
	for _, key := range []string{"query", "question"} {
		if s := stringFromAny(m[key]); s != "" {
			return truncateString(s, 500)
		}
	}
	switch page := m["page"].(type) {
	case pageDoc:
		if page.Title != "" {
			return truncateString(page.Title, 500)
		}
		if rawURL := firstNonEmpty(page.FinalURL, page.URL); rawURL != "" {
			return truncateString(rawURL, 500)
		}
	case map[string]any:
		if title := stringFromAny(page["title"]); title != "" {
			return truncateString(title, 500)
		}
		if rawURL := stringFromAny(firstNonEmptyAny(page["final_url"], page["url"])); rawURL != "" {
			return truncateString(rawURL, 500)
		}
	}
	if count := intFromAny(m["count"]); count > 0 {
		return fmt.Sprintf("%d item(s)", count)
	}
	if rawURL := stringFromAny(m["current_url"]); rawURL != "" {
		return truncateString(rawURL, 500)
	}
	return ""
}

func compactRunOutput(output any) map[string]any {
	m := mapFromAny(output)
	out := make(map[string]any)
	for _, key := range []string{
		"query", "question", "count", "current_url", "mode", "blocked",
		"extraction_backend", "fallback", "url", "storage_id", "artifact_id",
		"width", "height", "stored",
	} {
		if v, ok := m[key]; ok {
			out[key] = v
		}
	}
	switch page := m["page"].(type) {
	case pageDoc:
		out["page"] = map[string]any{
			"url":                page.URL,
			"final_url":          page.FinalURL,
			"title":              page.Title,
			"status":             page.Status,
			"extraction_backend": page.ExtractionBackend,
		}
	case map[string]any:
		out["page"] = map[string]any{
			"url":                page["url"],
			"final_url":          page["final_url"],
			"title":              page["title"],
			"status":             page["status"],
			"extraction_backend": page["extraction_backend"],
		}
	}
	if cache, ok := m["cache"]; ok {
		out["cache"] = cache
	}
	if artifact, ok := m["artifact"]; ok {
		out["artifact"] = artifact
	}
	return out
}

func mapFromAny(v any) map[string]any {
	if m, ok := v.(map[string]any); ok {
		return m
	}
	b, err := json.Marshal(v)
	if err != nil {
		return map[string]any{}
	}
	var out map[string]any
	if err := json.Unmarshal(b, &out); err != nil {
		return map[string]any{}
	}
	return out
}

func writeJSON(w http.ResponseWriter, payload any, err error) {
	if err != nil {
		httpErr(w, http.StatusInternalServerError, err.Error())
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload)
}

func httpErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func main() { sdk.Run(&App{}) }
