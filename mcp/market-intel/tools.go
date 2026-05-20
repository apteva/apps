package main

// MCP tool surface. Thin handlers: resolve project, build a
// platformSourceClient from the live ctx, delegate to the gateway
// functions in gateway.go (which are themselves source-client-agnostic
// for testability).

import (
	sdk "github.com/apteva/app-sdk"
)

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
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
			Handler: a.toolSourcesStatus},
	}
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
	return gwContext(a.client(), strArg(args, "topic")), nil
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
