// Web v0.1.1 — browser-backed web intelligence.
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
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	"golang.org/x/net/html"
	_ "modernc.org/sqlite"
)

const manifestYAML = `schema: apteva-app/v1
name: web
display_name: Web
version: 0.1.1
description: Browser-native web intelligence for agents.
author: Apteva
scopes: [project, global]
requires:
  permissions:
    - db.write.app
    - net.egress
    - platform.apps.call
  apps:
    - name: computer
    - name: storage
provides:
  http_routes:
    - prefix: /
  mcp_tools:
    - name: web_search
      description: "Browser-backed web search. Args: query, limit?, engine?, backend?, viewport?, visit_top?."
    - name: web_extract
      description: "Open a URL in a browser session and extract readable text, metadata, and links."
    - name: web_crawl
      description: "Browser-backed bounded crawl from seed URLs."
    - name: web_map
      description: "Fast site map discovery from seed URLs."
    - name: web_research
      description: "Multi-step browser-backed research with citations and artifacts."
    - name: web_snapshot
      description: "Capture visual evidence for a URL or existing computer session."
  ui_panels:
    - slot: project.page
      label: Web
      icon: globe
      entry: /ui/WebPanel.mjs
  ui_components:
    - name: web-result-card
      entry: /ui/WebResultCard.mjs
      slots: [chat.message_attachment]
runtime:
  kind: source
  source:
    repo: github.com/apteva/apps
    ref: main
    entry: mcp/web
  port: 8080
  health_check: /health
db:
  driver: sqlite
  path: /data/web.db
  migrations: migrations/
upgrade_policy: auto-patch
`

const (
	defaultHTTPTimeout = 20 * time.Second
	defaultMaxChars    = 20000
	maxFetchBytes      = 5 * 1024 * 1024
	defaultCrawlPages  = 10
	maxCrawlPages      = 50
	defaultSearchLimit = 10
	maxSearchLimit     = 25
)

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
		return errors.New("web requires a db block")
	}
	globalCtx = ctx
	ctx.Logger().Info("web mounted", "scope_project_id", os.Getenv("APTEVA_PROJECT_ID"))
	return nil
}

func (a *App) OnUnmount(*sdk.AppCtx) error       { return nil }
func (a *App) Channels() []sdk.ChannelFactory    { return nil }
func (a *App) Workers() []sdk.Worker             { return nil }
func (a *App) EventHandlers() []sdk.EventHandler { return nil }

func (a *App) HTTPRoutes() []sdk.Route {
	return []sdk.Route{
		{Method: http.MethodGet, Pattern: "/runs", Handler: a.handleRuns},
	}
}

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{
			Name:        "web_search",
			Description: "Browser-backed web search. Args: query, limit?, engine? (duckduckgo), backend?, viewport?, visit_top? bool. Returns normalized JSON results.",
			InputSchema: schemaObject(map[string]any{
				"query":     map[string]any{"type": "string"},
				"limit":     map[string]any{"type": "integer"},
				"engine":    map[string]any{"type": "string", "enum": []string{"duckduckgo"}},
				"backend":   map[string]any{"type": "string"},
				"viewport":  viewportSchema(),
				"visit_top": map[string]any{"type": "boolean"},
				"store":     map[string]any{"type": "boolean"},
			}, []string{"query"}),
			Handler: a.toolSearch,
		},
		{
			Name:        "web_extract",
			Description: "Open a URL in a browser session, extract readable text, metadata, links, images, and optional artifact storage. Args: url, backend?, viewport?, max_chars?, store?, snapshot?.",
			InputSchema: schemaObject(map[string]any{
				"url":       map[string]any{"type": "string"},
				"backend":   map[string]any{"type": "string"},
				"viewport":  viewportSchema(),
				"max_chars": map[string]any{"type": "integer"},
				"store":     map[string]any{"type": "boolean"},
				"snapshot":  map[string]any{"type": "boolean"},
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
			}, nil),
			Handler: a.toolMap,
		},
		{
			Name:        "web_research",
			Description: "Multi-step browser-backed research. Args: question, queries?, max_results?, max_sources?, backend?, snapshots?, store?. Returns extractive report JSON with citations and artifacts.",
			InputSchema: schemaObject(map[string]any{
				"question":    map[string]any{"type": "string"},
				"queries":     map[string]any{"type": "array", "items": map[string]any{"type": "string"}},
				"max_results": map[string]any{"type": "integer"},
				"max_sources": map[string]any{"type": "integer"},
				"backend":     map[string]any{"type": "string"},
				"viewport":    viewportSchema(),
				"snapshots":   map[string]any{"type": "boolean"},
				"store":       map[string]any{"type": "boolean"},
			}, []string{"question"}),
			Handler: a.toolResearch,
		},
		{
			Name:        "web_snapshot",
			Description: "Capture visual evidence for a URL or existing computer session. Args: url? or session_id?, backend?, viewport?, label?, store?.",
			InputSchema: schemaObject(map[string]any{
				"url":        map[string]any{"type": "string"},
				"session_id": map[string]any{"type": "string"},
				"backend":    map[string]any{"type": "string"},
				"viewport":   viewportSchema(),
				"label":      map[string]any{"type": "string"},
				"store":      map[string]any{"type": "boolean"},
			}, nil),
			Handler: a.toolSnapshot,
		},
	}
}

type browserSession struct {
	SessionID  string `json:"session_id"`
	Backend    string `json:"backend"`
	CurrentURL string `json:"current_url"`
	DebugURL   string `json:"debug_url,omitempty"`
	StreamURL  string `json:"stream_url,omitempty"`
	Width      int    `json:"width"`
	Height     int    `json:"height"`
}

type browserExtractResult struct {
	SessionID         string         `json:"session_id"`
	Backend           string         `json:"backend"`
	CurrentURL        string         `json:"current_url"`
	URL               string         `json:"url"`
	Title             string         `json:"title"`
	Description       string         `json:"description"`
	Text              string         `json:"text"`
	Markdown          string         `json:"markdown"`
	Links             []linkInfo     `json:"links"`
	Images            []string       `json:"images"`
	Metadata          map[string]any `json:"metadata"`
	Rendered          bool           `json:"rendered"`
	ExtractionBackend string         `json:"extraction_backend"`
	Width             int            `json:"width"`
	Height            int            `json:"height"`
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
	Links             []linkInfo       `json:"links,omitempty"`
	Images            []string         `json:"images,omitempty"`
	Metadata          map[string]any   `json:"metadata,omitempty"`
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

func (a *App) toolSearch(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	query := strings.TrimSpace(stringArg(args, "query"))
	if query == "" {
		return nil, errors.New("query required")
	}
	runID, _ := startRun(ctx, "search", args)
	defer failRunOnPanic(ctx, runID)

	limit := boundedInt(intArg(args, "limit"), defaultSearchLimit, 1, maxSearchLimit)
	engine := firstNonEmpty(stringArg(args, "engine"), configString(ctx, "default_search_engine"), "duckduckgo")
	if engine != "duckduckgo" {
		return nil, fmt.Errorf("unsupported engine %q", engine)
	}
	searchURL := duckDuckGoURL(query)
	browser, browserErr := a.openBrowser(ctx, searchURL, args)
	if browserErr != nil {
		completeRun(ctx, runID, "failed", nil, browserErr)
		return nil, browserErr
	}
	defer a.closeBrowser(ctx, browser.SessionID)

	body, fetchMeta, err := fetchURL(ctx, searchURL)
	if err != nil {
		completeRun(ctx, runID, "failed", nil, err)
		return nil, err
	}
	results := parseDuckDuckGo(body, limit)
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
		"extraction_backend": "http_after_browser_open",
		"fetch":              fetchMeta,
	}
	if boolArg(args, "store") {
		if art, err := storeJSONArtifact(ctx, runID, "search", searchURL, "search: "+query, out); err == nil {
			out["artifact"] = art
		} else {
			ctx.Logger().Warn("store search artifact failed", "err", err.Error())
		}
	}
	completeRun(ctx, runID, "completed", out, nil)
	return out, nil
}

func (a *App) toolExtract(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	target := strings.TrimSpace(stringArg(args, "url"))
	if target == "" {
		return nil, errors.New("url required")
	}
	runID, _ := startRun(ctx, "extract", args)
	defer failRunOnPanic(ctx, runID)

	doc := a.extractURL(ctx, runID, target, args, true)
	out := map[string]any{"page": doc}
	if doc.Error != "" {
		err := errors.New(doc.Error)
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
	runID, _ := startRun(ctx, "crawl", args)
	defer failRunOnPanic(ctx, runID)

	maxPages := boundedInt(intArg(args, "max_pages"), defaultCrawlPages, 1, maxCrawlPages)
	maxDepth := boundedInt(intArg(args, "max_depth"), 1, 0, 5)
	sameHost := boolArgDefault(args, "same_host", true)
	pages, edges := a.crawl(ctx, runID, seeds, args, maxPages, maxDepth, sameHost, true)
	out := map[string]any{
		"seeds":              seeds,
		"pages":              pages,
		"edges":              edges,
		"count":              len(pages),
		"extraction_backend": "http_after_browser_open",
	}
	if boolArgDefault(args, "store", storeDefault(ctx)) {
		if art, err := storeJSONArtifact(ctx, runID, "crawl", seeds[0], "crawl", out); err == nil {
			out["artifact"] = art
		}
	}
	completeRun(ctx, runID, "completed", out, nil)
	return out, nil
}

func (a *App) toolMap(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	seeds := seedURLs(args)
	if len(seeds) == 0 {
		return nil, errors.New("url or urls required")
	}
	runID, _ := startRun(ctx, "map", args)
	defer failRunOnPanic(ctx, runID)

	maxPages := boundedInt(intArg(args, "max_pages"), defaultCrawlPages, 1, maxCrawlPages)
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
	out := map[string]any{"seeds": seeds, "pages": mapPages, "edges": edges, "count": len(mapPages)}
	if boolArgDefault(args, "store", storeDefault(ctx)) {
		if art, err := storeJSONArtifact(ctx, runID, "map", seeds[0], "map", out); err == nil {
			out["artifact"] = art
		}
	}
	completeRun(ctx, runID, "completed", out, nil)
	return out, nil
}

func (a *App) toolResearch(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	question := strings.TrimSpace(stringArg(args, "question"))
	if question == "" {
		return nil, errors.New("question required")
	}
	runID, _ := startRun(ctx, "research", args)
	defer failRunOnPanic(ctx, runID)

	queries := stringSliceArg(args, "queries")
	if len(queries) == 0 {
		queries = []string{question}
	}
	maxResults := boundedInt(intArg(args, "max_results"), 8, 1, maxSearchLimit)
	maxSources := boundedInt(intArg(args, "max_sources"), 5, 1, 12)

	allResults := make([]searchResult, 0, len(queries)*maxResults)
	seen := map[string]bool{}
	for _, q := range queries {
		searchOut, err := a.toolSearch(ctx, mapMerge(args, map[string]any{
			"query": q,
			"limit": maxResults,
			"store": false,
		}))
		if err != nil {
			ctx.Logger().Warn("research search failed", "query", q, "err", err.Error())
			continue
		}
		for _, r := range searchOut.(map[string]any)["results"].([]searchResult) {
			if !seen[r.URL] {
				seen[r.URL] = true
				allResults = append(allResults, r)
			}
		}
	}
	if len(allResults) > maxSources {
		allResults = allResults[:maxSources]
	}

	sources := make([]pageDoc, 0, len(allResults))
	citations := make([]map[string]any, 0, len(allResults))
	for i, r := range allResults {
		doc := a.extractURL(ctx, runID, r.URL, args, true)
		if boolArg(args, "snapshots") && doc.Error == "" {
			shot, err := a.snapshot(ctx, runID, mapMerge(args, map[string]any{"url": r.URL, "label": r.Title, "store": true}))
			if err == nil {
				doc.Snapshot = shot
			}
		}
		sources = append(sources, doc)
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
		"extraction_backend": "http_after_browser_open",
	}
	if boolArgDefault(args, "store", storeDefault(ctx)) {
		if art, err := storeJSONArtifact(ctx, runID, "research", "", "research: "+question, out); err == nil {
			out["artifact"] = art
		}
	}
	completeRun(ctx, runID, "completed", out, nil)
	return out, nil
}

func (a *App) toolSnapshot(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	runID, _ := startRun(ctx, "snapshot", args)
	defer failRunOnPanic(ctx, runID)
	out, err := a.snapshot(ctx, runID, args)
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
		doc.ExtractionBackend = firstNonEmpty(extracted.ExtractionBackend, "browser_dom")
		doc.Bytes = len(extracted.Text) + len(extracted.Markdown)
		doc.Truncated = false
		if includeText {
			maxChars := boundedInt(intArg(args, "max_chars"), defaultMaxChars, 1000, 200000)
			doc.Text = truncateString(extracted.Text, maxChars)
			doc.Markdown = truncateString(extracted.Markdown, maxChars)
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
				"store":      true,
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
			"store":      true,
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
	for _, s := range seeds {
		if err := validateHTTPURL(s); err == nil {
			queue = append(queue, crawlNode{URL: s})
			if u, err := url.Parse(s); err == nil {
				seedHosts[strings.ToLower(u.Hostname())] = true
			}
		}
	}
	visited := map[string]bool{}
	pages := make([]pageDoc, 0, maxPages)
	edges := make([]crawlEdge, 0)

	for len(queue) > 0 && len(pages) < maxPages {
		node := queue[0]
		queue = queue[1:]
		canon := canonicalURL(node.URL)
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
			if len(queue)+len(pages) >= maxPages {
				continue
			}
			if shouldFollow(l.URL, seedHosts, sameHost) {
				queue = append(queue, crawlNode{URL: l.URL, Depth: node.Depth + 1})
			}
		}
	}
	return pages, edges
}

func (a *App) snapshot(ctx *sdk.AppCtx, runID int64, args map[string]any) (map[string]any, error) {
	sessionID := stringArg(args, "session_id")
	opened := false
	var browser *browserSession
	var err error
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

	var shot struct {
		PNGB64     string `json:"png_b64"`
		CurrentURL string `json:"current_url"`
		Width      int    `json:"width"`
		Height     int    `json:"height"`
	}
	if err := ctx.PlatformAPI().CallAppResult("computer", "browser_screenshot", withProjectID(ctx, map[string]any{"session_id": sessionID}), &shot); err != nil {
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
	})
	if err := ctx.PlatformAPI().CallAppResult("storage", "files_upload", upArgs, &up); err != nil {
		return nil, fmt.Errorf("storage.files_upload: %w", err)
	}
	art, _ := insertArtifact(ctx, runID, "snapshot", shot.CurrentURL, stringArg(args, "label"), up.ID, up.URL, "image/png", 0, out)
	out["stored"] = true
	out["storage_id"] = up.ID
	out["url"] = up.URL
	out["artifact_id"] = art.ID
	out["opened_session"] = opened
	return out, nil
}

func (a *App) openBrowser(ctx *sdk.AppCtx, target string, args map[string]any) (*browserSession, error) {
	openArgs := map[string]any{"url": target}
	if b := firstNonEmpty(stringArg(args, "backend"), configString(ctx, "default_backend")); b != "" {
		openArgs["backend"] = b
	}
	for _, key := range []string{"viewport", "context_id", "persist", "timeout", "proxy", "proxy_country"} {
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
	formats := []string{"metadata", "links", "images"}
	if includeText {
		formats = append([]string{"text", "markdown"}, formats...)
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

// ─── Retrieval and parsing ────────────────────────────────────────

type fetchInfo map[string]any

func fetchURL(ctx *sdk.AppCtx, target string) ([]byte, fetchInfo, error) {
	if err := validateHTTPURL(target); err != nil {
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
	resp, err := http.DefaultClient.Do(req)
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

// ─── Storage and DB ───────────────────────────────────────────────

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
		if b, err := json.Marshal(output); err == nil {
			outText = string(b)
		}
	}
	errText := ""
	if runErr != nil {
		errText = runErr.Error()
	}
	_, _ = ctx.AppDB().Exec(
		`UPDATE web_runs SET status=?, output_json=?, error=?, completed_at=? WHERE id=?`,
		status, nullIfEmpty(outText), nullIfEmpty(errText), time.Now().UTC(), runID,
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
		"url":         doc.URL,
		"final_url":   doc.FinalURL,
		"title":       doc.Title,
		"description": doc.Description,
		"text":        doc.Text,
		"markdown":    doc.Markdown,
		"links":       doc.Links,
		"metadata":    doc.Metadata,
	}
	return storeArtifact(ctx, runID, "page", doc.URL, doc.Title, "application/json", payload)
}

func storeJSONArtifact(ctx *sdk.AppCtx, runID int64, kind, artifactURL, title string, payload any) (*artifactSummary, error) {
	return storeArtifact(ctx, runID, kind, artifactURL, title, "application/json", payload)
}

func storeArtifact(ctx *sdk.AppCtx, runID int64, kind, artifactURL, title, contentType string, payload any) (*artifactSummary, error) {
	b, err := json.MarshalIndent(payload, "", "  ")
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
	return insertArtifact(ctx, runID, kind, artifactURL, title, up.ID, up.URL, contentType, len(b), payload)
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

func listRuns(ctx *sdk.AppCtx, limit int) ([]map[string]any, error) {
	rows, err := ctx.AppDB().Query(
		`SELECT id, kind, status, COALESCE(error,''), COALESCE(output_json,''), created_at
		 FROM web_runs
		 WHERE project_id = ?
		 ORDER BY created_at DESC
		 LIMIT ?`,
		projectID(ctx), limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []map[string]any
	for rows.Next() {
		var id int64
		var kind, status, errText, outputText string
		var created time.Time
		if err := rows.Scan(&id, &kind, &status, &errText, &outputText, &created); err != nil {
			return nil, err
		}
		out = append(out, map[string]any{
			"id":         id,
			"kind":       kind,
			"status":     status,
			"error":      errText,
			"summary":    runSummary(outputText),
			"created_at": created.Format(time.RFC3339),
		})
	}
	return out, rows.Err()
}

// ─── HTTP handlers ────────────────────────────────────────────────

func (a *App) handleRuns(w http.ResponseWriter, r *http.Request) {
	limit := 50
	if v := r.URL.Query().Get("limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil && n > 0 && n <= 200 {
			limit = n
		}
	}
	runs, err := listRuns(globalCtx, limit)
	writeJSON(w, map[string]any{"runs": runs}, err)
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

func duckDuckGoURL(query string) string {
	return "https://duckduckgo.com/html/?" + url.Values{"q": []string{query}}.Encode()
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
	if strings.Contains(u.Hostname(), "duckduckgo.com") {
		if uddg := u.Query().Get("uddg"); uddg != "" {
			if parsed, err := url.QueryUnescape(uddg); err == nil {
				return parsed
			}
			return uddg
		}
		return ""
	}
	if u.Scheme == "http" || u.Scheme == "https" {
		return u.String()
	}
	return ""
}

func isLikelyResultURL(raw string) bool {
	u, err := url.Parse(raw)
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return false
	}
	host := strings.ToLower(u.Hostname())
	return host != "" && !strings.Contains(host, "duckduckgo.com")
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
	return nil
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
	b.WriteString("Key evidence is available in the citations array; v0.1.1 returns an extractive report rather than an LLM-written conclusion.")
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

func firstNonEmpty(vals ...string) string {
	for _, v := range vals {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

func truncateString(s string, max int) string {
	if max <= 0 || len(s) <= max {
		return s
	}
	return s[:max]
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

func storeDefault(ctx *sdk.AppCtx) bool {
	v := configString(ctx, "store_artifacts_by_default")
	if v == "" {
		return true
	}
	return boolArgDefault(map[string]any{"v": v}, "v", true)
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

func runSummary(outputText string) string {
	if outputText == "" {
		return ""
	}
	var m map[string]any
	if err := json.Unmarshal([]byte(outputText), &m); err != nil {
		return ""
	}
	for _, key := range []string{"query", "question"} {
		if s, ok := m[key].(string); ok {
			return s
		}
	}
	if c, ok := m["count"].(float64); ok {
		return fmt.Sprintf("%.0f item(s)", c)
	}
	return ""
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
