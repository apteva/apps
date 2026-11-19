package main

// MCP tool surface. Thin handlers: resolve project, build a
// platformSourceClient from the live ctx, delegate to the gateway
// functions in gateway.go (which are themselves source-client-agnostic
// for testability).

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"strconv"
	"time"
)

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		{Name: "search", Description: "Generic bitemporal intelligence search across recorded evidence. Args: query?, kind?, source?, entity?, event_from?, event_to?, published_from?, published_to?, as_of?, limit?. Dates accept RFC3339 or YYYY-MM-DD; as_of returns only facts known and valid at that time.",
			InputSchema: schemaObject(map[string]any{
				"query": map[string]any{"type": "string"}, "kind": map[string]any{"type": "string"}, "source": map[string]any{"type": "string"}, "entity": map[string]any{"type": "string"},
				"event_from": map[string]any{"type": "string"}, "event_to": map[string]any{"type": "string"}, "published_from": map[string]any{"type": "string"}, "published_to": map[string]any{"type": "string"}, "as_of": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer"},
			}, nil), Handler: a.toolSearch},
		{Name: "evidence_record", Description: "Record an immutable, source-attributed evidence item for later search, timelines, monitoring, and point-in-time replay. Duplicate content is idempotent.",
			InputSchema: schemaObject(map[string]any{
				"kind": map[string]any{"type": "string"}, "title": map[string]any{"type": "string"}, "body": map[string]any{"type": "string"}, "payload": map[string]any{"type": "object"}, "entity_refs": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, "source": map[string]any{"type": "string"}, "source_ref": map[string]any{"type": "string"}, "event_time": map[string]any{"type": "string"}, "published_time": map[string]any{"type": "string"}, "observed_at": map[string]any{"type": "string"}, "valid_from": map[string]any{"type": "string"}, "valid_to": map[string]any{"type": "string"}, "supersedes_id": map[string]any{"type": "integer"},
			}, []string{"source"}), Handler: a.toolEvidenceRecord},
		{Name: "timeline", Description: "Return a chronological evidence timeline for a topic or entity, with optional point-in-time cutoff. Alias for search ordered by temporal evidence.",
			InputSchema: schemaObject(map[string]any{"query": map[string]any{"type": "string"}, "entity": map[string]any{"type": "string"}, "from": map[string]any{"type": "string"}, "to": map[string]any{"type": "string"}, "as_of": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer"}}, nil), Handler: a.toolTimeline},
		{Name: "replay", Description: "Build a deterministic point-in-time evidence snapshot for analysis or backtesting. Returns the matched records plus the cutoff and stable snapshot hash.",
			InputSchema: schemaObject(map[string]any{"query": map[string]any{"type": "string"}, "entity": map[string]any{"type": "string"}, "as_of": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer"}}, []string{"as_of"}), Handler: a.toolReplay},
		{Name: "enrich", Description: "Everything relevant to a prediction market in one normalized blob: cross-venue prices, ground-truth probability, entities, their stats + H2H, news, computed edge. Args: market (id/slug), entity_a + entity_b (optional — the outcome principals, e.g. two players), domain (optional — tennis/sports/macro/crypto), sport (optional — the-odds-api sport_key), topic (optional — news query override).",
			InputSchema: schemaObject(map[string]any{
				"market":   map[string]any{"type": "string"},
				"entity_a": map[string]any{"type": "string"},
				"entity_b": map[string]any{"type": "string"},
				"domain":   map[string]any{"type": "string"},
				"sport":    map[string]any{"type": "string"},
				"topic":    map[string]any{"type": "string"},
			}, []string{"market"}),
			Handler: a.toolEnrich},

		{Name: "probability", Description: "Best ground-truth probability for an event. Args: entity (the outcome you want the prob of), domain (sports/tennis), sport (the-odds-api sport_key). Returns fair_prob + method + book count.",
			InputSchema: schemaObject(map[string]any{
				"entity": map[string]any{"type": "string"},
				"domain": map[string]any{"type": "string"},
				"sport":  map[string]any{"type": "string"},
			}, []string{"entity"}),
			Handler: a.toolProbability},

		{Name: "stats", Description: "Normalized profile for an entity (player/team/ticker/crypto) fanned across the right sources. Args: entity, domain.",
			InputSchema: schemaObject(map[string]any{
				"entity": map[string]any{"type": "string"},
				"domain": map[string]any{"type": "string"},
			}, []string{"entity", "domain"}),
			Handler: a.toolStats},

		{Name: "history", Description: "Head-to-head between two entities (or one entity's series). Args: entity_a, entity_b (optional), domain.",
			InputSchema: schemaObject(map[string]any{
				"entity_a": map[string]any{"type": "string"},
				"entity_b": map[string]any{"type": "string"},
				"domain":   map[string]any{"type": "string"},
			}, []string{"entity_a", "domain"}),
			Handler: a.toolHistory},

		{Name: "context", Description: "Deduped news + event coverage for a topic or entity, merged across every bound news source. Args: topic.",
			InputSchema: schemaObject(map[string]any{
				"topic": map[string]any{"type": "string"},
			}, []string{"topic"}),
			Handler: a.toolContext},

		{Name: "resolve_entity", Description: "Resolve a name to a canonical entity record (cached cross-source ids). Args: name, domain.",
			InputSchema: schemaObject(map[string]any{
				"name":   map[string]any{"type": "string"},
				"domain": map[string]any{"type": "string"},
			}, []string{"name"}),
			Handler: a.toolResolveEntity},

		{Name: "markets", Description: "Live active markets across the public prediction-market venues (Polymarket, Kalshi, Manifold), volume-ranked. No setup needed — all public. Args: limit (default 30).",
			InputSchema: schemaObject(map[string]any{
				"limit": map[string]any{"type": "integer"},
			}, nil),
			Handler: a.toolMarkets},

		{Name: "sources_status", Description: "Which data sources are bound right now (by slug). Use to see what the gateway can answer.",
			InputSchema: schemaObject(nil, nil),
			Handler:     a.toolSourcesStatus},

		{Name: "signals_list", Description: "List auditable trading signals. Args: feed?, status? (open/evaluated/all), limit?.",
			InputSchema: schemaObject(map[string]any{"feed": map[string]any{"type": "string"}, "status": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer"}}, nil), Handler: a.toolSignalsList},
		{Name: "signal_feeds", Description: "List configured signal products and quality gates.", InputSchema: schemaObject(nil, nil), Handler: a.toolSignalFeeds},
		{Name: "signal_metrics", Description: "Audited performance for evaluated signals. Args: feed?.", InputSchema: schemaObject(map[string]any{"feed": map[string]any{"type": "string"}}, nil), Handler: a.toolSignalMetrics},
		{Name: "signal_scan_now", Description: "Run provider discovery and signal generation now.", InputSchema: schemaObject(nil, nil), Handler: a.toolSignalScan},
		{Name: "signal_backtest", Description: "Walk-forward replay with no look-ahead. Args: symbol (Binance form, e.g. BTCUSDT), interval?, strategy?, limit?.",
			InputSchema: schemaObject(map[string]any{"symbol": map[string]any{"type": "string"}, "interval": map[string]any{"type": "string"}, "strategy": map[string]any{"type": "string"}, "provider": map[string]any{"type": "string"}, "limit": map[string]any{"type": "integer"}}, []string{"symbol"}), Handler: a.toolSignalBacktest},
	}
}

func evidenceQueryFromArgs(args map[string]any, projectID string) (EvidenceQuery, error) {
	q := EvidenceQuery{ProjectID: projectID, Text: strArg(args, "query"), Kind: strArg(args, "kind"), Source: strArg(args, "source"), Entity: strArg(args, "entity"), Limit: 50}
	if raw, ok := args["offset"]; ok {
		n, err := strconv.Atoi(fmt.Sprint(raw))
		if err != nil || n < 0 {
			return q, errors.New("offset must be a nonnegative integer")
		}
		q.Offset = n
	}
	if v := strArg(args, "limit"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			q.Limit = n
		}
	} else if n, ok := args["limit"].(float64); ok {
		q.Limit = int(n)
	}
	var err error
	for key, dst := range map[string]**time.Time{"event_from": &q.EventFrom, "event_to": &q.EventTo, "published_from": &q.PublishedFrom, "published_to": &q.PublishedTo, "as_of": &q.AsOf} {
		if v := strArg(args, key); v != "" {
			*dst, err = parseOptionalTime(v)
			if err != nil {
				return q, fmt.Errorf("%s: %w", key, err)
			}
		}
	}
	return q, nil
}

func (a *App) toolSearch(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	q, err := evidenceQueryFromArgs(args, pid)
	if err != nil {
		return nil, err
	}
	rows, err := searchEvidence(ctx.AppDB(), q)
	if err != nil {
		return nil, err
	}
	return map[string]any{"results": rows, "count": len(rows), "query": q.Text, "as_of": timeString(q.AsOf)}, nil
}

func (a *App) toolTimeline(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	if v := strArg(args, "from"); v != "" {
		args["event_from"] = v
	}
	if v := strArg(args, "to"); v != "" {
		args["event_to"] = v
	}
	q, err := evidenceQueryFromArgs(args, pid)
	if err != nil {
		return nil, err
	}
	rows, err := searchEvidence(ctx.AppDB(), q)
	if err != nil {
		return nil, err
	}
	return map[string]any{"timeline": rows, "count": len(rows), "as_of": timeString(q.AsOf)}, nil
}

func (a *App) toolReplay(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	q, err := evidenceQueryFromArgs(args, pid)
	if err != nil {
		return nil, err
	}
	if q.AsOf == nil {
		return nil, errors.New("as_of is required")
	}
	rows, err := searchEvidence(ctx.AppDB(), q)
	if err != nil {
		return nil, err
	}
	b, _ := json.Marshal(rows)
	sum := sha256.Sum256(b)
	return map[string]any{"snapshot": hex.EncodeToString(sum[:]), "as_of": q.AsOf.UTC().Format(time.RFC3339), "records": rows, "count": len(rows), "lookahead_safe": true}, nil
}

func (a *App) toolEvidenceRecord(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	e := Evidence{Kind: strArg(args, "kind"), Title: strArg(args, "title"), Body: strArg(args, "body"), Source: strArg(args, "source"), SourceRef: strArg(args, "source_ref")}
	if raw, ok := args["payload"]; ok {
		b, e2 := json.Marshal(raw)
		if e2 != nil {
			return nil, e2
		}
		e.Payload = b
	}
	if refs, ok := args["entity_refs"].([]any); ok {
		for _, r := range refs {
			if s, ok := r.(string); ok {
				e.EntityRefs = append(e.EntityRefs, s)
			}
		}
	}
	if refs, ok := args["entity_refs"].([]string); ok {
		e.EntityRefs = append(e.EntityRefs, refs...)
	}
	for key, dst := range map[string]**time.Time{"event_time": &e.EventTime, "published_time": &e.PublishedTime, "observed_at": &e.ObservedAt, "valid_from": &e.ValidFrom, "valid_to": &e.ValidTo} {
		if v := strArg(args, key); v != "" {
			*dst, err = parseOptionalTime(v)
			if err != nil {
				return nil, fmt.Errorf("%s: %w", key, err)
			}
		}
	}
	if n, ok := args["supersedes_id"].(float64); ok {
		id := int64(n)
		e.SupersedesID = &id
	}
	return recordEvidence(ctx.AppDB(), e, pid)
}

func (a *App) toolSignalsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	limit := 50
	switch v := args["limit"].(type) {
	case float64:
		limit = int(v)
	case int:
		limit = v
	case string:
		if n, e := strconv.Atoi(v); e == nil {
			limit = n
		}
	}
	rows, err := listSignals(ctx.AppDB(), pid, strArg(args, "feed"), strArg(args, "status"), limit)
	if err != nil {
		return nil, err
	}
	return map[string]any{"signals": rows, "count": len(rows)}, nil
}
func (a *App) toolSignalFeeds(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	if _, err = ensureDefaultFeed(ctx.AppDB(), pid); err != nil {
		return nil, err
	}
	rows, err := listFeeds(ctx.AppDB(), pid)
	return map[string]any{"feeds": rows}, err
}
func (a *App) toolSignalMetrics(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	return signalMetrics(ctx.AppDB(), pid, strArg(args, "feed"))
}
func (a *App) toolSignalScan(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	summaries, err := a.runSignalScan(context.Background(), ctx, pid)
	return map[string]any{"runs": summaries}, err
}
func (a *App) toolSignalBacktest(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	symbol := strArg(args, "symbol")
	if symbol == "" {
		return nil, errors.New("symbol required")
	}
	interval := strArg(args, "interval")
	if interval == "" {
		interval = "1h"
	}
	strategy := strArg(args, "strategy")
	limit := 500
	if v, ok := args["limit"].(float64); ok {
		limit = int(v)
	}
	if limit < 70 {
		limit = 70
	}
	if limit > 1000 {
		limit = 1000
	}
	providerName := strArg(args, "provider")
	var provider MarketDataProvider
	for _, p := range a.signalProviders(ctx) {
		if providerName == "" || p.Name() == providerName {
			provider = p
			break
		}
	}
	if provider == nil {
		return nil, errors.New("provider unavailable")
	}
	cost := configFloat(ctx, "round_trip_fee_bps", 20, 0, 500) + configFloat(ctx, "slippage_bps", 5, 0, 500)
	return a.runBacktest(context.Background(), provider, symbol, interval, strategy, limit, cost)
}

func (a *App) toolMarkets(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	limit := 30
	if v, ok := args["limit"].(float64); ok && v > 0 {
		limit = int(v)
	}
	rows := gwListMarkets(a.client(), limit)
	return map[string]any{"markets": rows, "count": len(rows)}, nil
}

func (a *App) client() sourceClient { return newPlatformSourceClient(globalCtx) }

func (a *App) toolEnrich(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	sc := a.client()
	market := strArg(args, "market")
	domain := strArg(args, "domain")
	entityA := strArg(args, "entity_a")
	entityB := strArg(args, "entity_b")
	sport := strArg(args, "sport")
	topic := strArg(args, "topic")
	if topic == "" {
		topic = market
		if entityA != "" && entityB != "" {
			topic = entityA + " vs " + entityB
		}
	}

	blob := map[string]any{"market": market, "sources": []string{}}
	srcSet := map[string]bool{}
	addSrc := func(ss []string) {
		for _, s := range ss {
			srcSet[s] = true
		}
	}

	// Prediction-market price (public, no binding). The anchor.
	var pmYes *float64
	if mp := gwMarketPrice(sc, market); mp != nil {
		blob["market_price"] = mp
		pmYes = mp.YesPrice
		addSrc([]string{mp.Source})
	}

	// Ground-truth probability (sports). entity_a is the principal
	// outcome we price.
	var fair *float64
	if entityA != "" {
		prob := gwProbability(sc, map[string]any{"entity": entityA, "domain": domain, "sport": sport})
		blob["ground_truth"] = prob
		addSrc(prob.Sources)
		if prob.Method != "unavailable" {
			f := prob.FairProb
			fair = &f
		}
	}

	// Edge: PM YES vs ground-truth fair. Positive = PM overprices YES
	// (a SELL_YES signal). Note: ground truth may be surface-blind etc;
	// the agent enriches with stats below before trusting it.
	if pmYes != nil && fair != nil {
		bps := edgeBps(*pmYes, *fair)
		dir := "BUY_YES"
		if bps > 0 {
			dir = "SELL_YES"
		}
		blob["edge"] = map[string]any{
			"pm_yes":    *pmYes,
			"fair":      *fair,
			"edge_bps":  bps,
			"direction": dir,
			"note":      "ground truth may be naive (e.g. surface-blind odds); validate with the stats/h2h fields before trading",
		}
	}

	// Stats for both principals.
	if entityA != "" && domain != "" {
		sa := gwStats(sc, entityA, domain)
		blob["stats_a"] = sa
		addSrc(sa.Sources)
	}
	if entityB != "" && domain != "" {
		sb := gwStats(sc, entityB, domain)
		blob["stats_b"] = sb
		addSrc(sb.Sources)
	}

	// Head-to-head.
	if entityA != "" && entityB != "" && domain != "" {
		h2h, hs := gwHistory(sc, entityA, entityB, domain)
		if len(h2h) > 0 {
			blob["h2h"] = h2h
			addSrc(hs)
		}
	}

	// News context.
	ctxRes := gwContext(sc, topic)
	if len(ctxRes.Items) > 0 {
		blob["news"] = ctxRes
		addSrc(ctxRes.Sources)
	}

	// Provenance.
	srcs := make([]string, 0, len(srcSet))
	for s := range srcSet {
		srcs = append(srcs, s)
	}
	blob["sources"] = srcs
	_ = pid
	return blob, nil
}

func (a *App) toolProbability(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if _, err := resolveProjectFromArgs(args); err != nil {
		return nil, err
	}
	return gwProbability(a.client(), map[string]any{
		"entity": strArg(args, "entity"),
		"domain": strArg(args, "domain"),
		"sport":  strArg(args, "sport"),
	}), nil
}

func (a *App) toolStats(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if _, err := resolveProjectFromArgs(args); err != nil {
		return nil, err
	}
	return gwStats(a.client(), strArg(args, "entity"), strArg(args, "domain")), nil
}

func (a *App) toolHistory(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if _, err := resolveProjectFromArgs(args); err != nil {
		return nil, err
	}
	h2h, sources := gwHistory(a.client(), strArg(args, "entity_a"), strArg(args, "entity_b"), strArg(args, "domain"))
	return map[string]any{"history": h2h, "sources": sources}, nil
}

func (a *App) toolContext(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if _, err := resolveProjectFromArgs(args); err != nil {
		return nil, err
	}
	return gwContextParams(a.client(), args), nil
}

func (a *App) toolResolveEntity(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil {
		return nil, err
	}
	name := strArg(args, "name")
	domain := strArg(args, "domain")
	// Check the resolution cache first.
	if e, _ := dbSearchEntity(ctx.AppDB(), pid, domain, name); e != nil {
		return map[string]any{"entity": e, "cached": true}, nil
	}
	// Cache miss — create a stub record so future calls are stable.
	// (LLM-assisted external-id resolution is a v0.2 enrichment.)
	e := &Entity{Canonical: name, Domain: domain, ExternalIDs: map[string]string{}, Aliases: []string{}}
	if domain != "" {
		if id, err := dbUpsertEntity(ctx.AppDB(), pid, e); err == nil {
			e.ID = id
		}
	}
	return map[string]any{"entity": e, "cached": false,
		"note": "stub record — cross-source ids resolved lazily in v0.2 via LLM"}, nil
}

func (a *App) toolSourcesStatus(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	return gwSourcesStatus(a.client()), nil
}
