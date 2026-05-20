package main

// Source registry + the source-call layer.
//
// The gateway never hardcodes "for tennis H2H call api-sports". Instead
// it asks the registry for the ordered list of source specs that answer
// a (domain, queryType) and walks them until one succeeds. Each spec
// names a slug + upstream tool + an arg builder. Adding a new backing
// source for a query type = one registry entry, no gateway changes.
//
// sourceClient is the indirection that makes all of this testable: in
// production it resolves a slug to a bound connection and calls
// ExecuteIntegrationTool; in tests it returns canned JSON per
// (slug, tool).

import (
	"encoding/json"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// queryType enumerates the normalized things the gateway can ask for.
type queryType string

const (
	qOdds        queryType = "odds"          // sportsbook odds for an event → de-vig → fair prob
	qH2H         queryType = "h2h"           // head-to-head record between two entities
	qRanking     queryType = "ranking"       // current ranking / standing for an entity
	qForm        queryType = "form"          // recent form / results for an entity
	qIndicator   queryType = "indicator"     // macro indicator time series
	qMarketPrice queryType = "market_price"  // current price of a prediction market on a venue
	qNews        queryType = "news"          // news + sentiment for a topic/entity
	qCryptoPrice queryType = "crypto_price"  // spot price for a crypto asset
)

// sourceSpec — one way to answer a query type. argFn turns the gateway's
// generic params into the upstream tool's arg map.
type sourceSpec struct {
	slug  string
	tool  string
	argFn func(params map[string]any) map[string]any
}

// registry — (domain, queryType) → ordered specs. The gateway tries
// them top to bottom. domain "*" matches any domain (used for news,
// which is domain-agnostic).
var registry = map[string]map[queryType][]sourceSpec{
	"tennis": {
		qH2H: {
			{slug: "tennis-abstract", tool: "h2h", argFn: func(p map[string]any) map[string]any {
				return map[string]any{"player_a": p["entity_a"], "player_b": p["entity_b"]}
			}},
			{slug: "api-sports", tool: "tennis_h2h", argFn: func(p map[string]any) map[string]any {
				return map[string]any{"h2h": str(p["entity_a"]) + "-" + str(p["entity_b"])}
			}},
		},
		qRanking: {
			{slug: "api-sports", tool: "tennis_rankings", argFn: func(p map[string]any) map[string]any { return map[string]any{} }},
			{slug: "tennis-abstract", tool: "ranking", argFn: func(p map[string]any) map[string]any {
				return map[string]any{"player": p["entity"]}
			}},
		},
		qForm: {
			{slug: "tennis-abstract", tool: "recent_form", argFn: func(p map[string]any) map[string]any {
				return map[string]any{"player": p["entity"], "surface": p["surface"], "last": 20}
			}},
		},
		qOdds: {
			{slug: "the-odds-api", tool: "get_odds", argFn: func(p map[string]any) map[string]any {
				return map[string]any{"sport": orStr(p["sport"], "tennis_atp"), "markets": "h2h", "oddsFormat": "decimal"}
			}},
		},
	},
	"sports": {
		qOdds: {
			{slug: "the-odds-api", tool: "get_odds", argFn: func(p map[string]any) map[string]any {
				return map[string]any{"sport": p["sport"], "markets": orStr(p["markets"], "h2h"), "oddsFormat": "decimal"}
			}},
		},
		qH2H: {
			{slug: "api-sports", tool: "h2h", argFn: func(p map[string]any) map[string]any {
				return map[string]any{"h2h": str(p["entity_a"]) + "-" + str(p["entity_b"])}
			}},
		},
		qRanking: {
			{slug: "api-sports", tool: "standings", argFn: func(p map[string]any) map[string]any {
				return map[string]any{"league": p["league"], "season": p["season"]}
			}},
		},
	},
	"macro": {
		qIndicator: {
			{slug: "fred", tool: "series_observations", argFn: func(p map[string]any) map[string]any {
				return map[string]any{"series_id": p["series_id"], "sort_order": "desc", "limit": 12}
			}},
			{slug: "bls", tool: "get_series_single", argFn: func(p map[string]any) map[string]any {
				return map[string]any{"seriesid": p["series_id"]}
			}},
		},
	},
	"crypto": {
		qCryptoPrice: {
			{slug: "coingecko", tool: "simple_price", argFn: func(p map[string]any) map[string]any {
				return map[string]any{"ids": p["asset"], "vs_currencies": "usd"}
			}},
		},
	},
	"*": {
		qNews: {
			{slug: "tavily", tool: "search", argFn: func(p map[string]any) map[string]any {
				return map[string]any{"query": p["topic"], "max_results": 8}
			}},
			{slug: "newsapi", tool: "everything", argFn: func(p map[string]any) map[string]any {
				return map[string]any{"q": p["topic"], "pageSize": 8, "sortBy": "publishedAt"}
			}},
			{slug: "gnews", tool: "search", argFn: func(p map[string]any) map[string]any {
				return map[string]any{"q": p["topic"], "max": 8}
			}},
			{slug: "gdelt", tool: "doc_search", argFn: func(p map[string]any) map[string]any {
				return map[string]any{"query": p["topic"], "maxrecords": 8}
			}},
		},
	},
}

// specsFor returns the ordered specs for a (domain, queryType),
// transparently falling back to the "*" wildcard domain.
func specsFor(domain string, qt queryType) []sourceSpec {
	if d, ok := registry[domain]; ok {
		if specs, ok := d[qt]; ok {
			return specs
		}
	}
	if d, ok := registry["*"]; ok {
		if specs, ok := d[qt]; ok {
			return specs
		}
	}
	return nil
}

// ─── Source client (the testable indirection) ─────────────────────

// sourceResult is what a single source call yields.
type sourceResult struct {
	Slug string          `json:"slug"`
	Data json.RawMessage `json:"data"`
}

// sourceClient executes a tool against a bound integration connection.
// ok=false means the slug isn't bound, the call failed, or returned
// non-success — callers treat that as "this source can't answer, try
// the next spec".
type sourceClient interface {
	call(slug, tool string, args map[string]any) (json.RawMessage, bool)
	// boundSlugs reports which of the given candidate slugs currently
	// have an active connection. Used by sources_status + to skip
	// specs whose source isn't bound without a wasted call.
	boundSlugs(candidates []string) map[string]bool
}

// platformSourceClient is the production sourceClient — resolves slugs
// to connections via ListConnections (cached briefly) and dispatches
// through ExecuteIntegrationTool.
type platformSourceClient struct {
	ctx *sdk.AppCtx

	mu       sync.Mutex
	connByID map[string]int64 // slug → connection id
	cachedAt time.Time
	cacheTTL time.Duration
}

func newPlatformSourceClient(ctx *sdk.AppCtx) *platformSourceClient {
	return &platformSourceClient{ctx: ctx, connByID: map[string]int64{}, cacheTTL: 60 * time.Second}
}

func (p *platformSourceClient) connFor(slug string) (int64, bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if time.Since(p.cachedAt) > p.cacheTTL {
		p.connByID = map[string]int64{}
		p.cachedAt = time.Now()
	}
	if id, ok := p.connByID[slug]; ok {
		return id, id != 0
	}
	id := int64(0)
	if p.ctx != nil && p.ctx.PlatformAPI() != nil {
		conns, err := p.ctx.PlatformAPI().ListConnections(sdk.ConnectionFilter{AppSlug: slug})
		if err == nil {
			for _, c := range conns {
				if c.Status == "" || c.Status == "active" || c.Status == "connected" {
					id = c.ID
					break
				}
			}
		}
	}
	p.connByID[slug] = id
	return id, id != 0
}

func (p *platformSourceClient) call(slug, tool string, args map[string]any) (json.RawMessage, bool) {
	id, ok := p.connFor(slug)
	if !ok {
		return nil, false
	}
	res, err := p.ctx.PlatformAPI().ExecuteIntegrationTool(id, tool, args)
	if err != nil || res == nil || !res.Success {
		return nil, false
	}
	return res.Data, true
}

func (p *platformSourceClient) boundSlugs(candidates []string) map[string]bool {
	out := map[string]bool{}
	for _, s := range candidates {
		_, ok := p.connFor(s)
		out[s] = ok
	}
	return out
}

// ─── small param helpers ───────────────────────────────────────────

func str(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
func orStr(v any, def string) string {
	if s := str(v); s != "" {
		return s
	}
	return def
}
