package main

// MCP tool surface. Every tool listed in apteva.yaml has a handler
// here; manifest_test.go enforces parity. Handlers do argument parsing,
// project resolution, and call into store.go — no business logic
// inline beyond the pre-trade checks in toolOrderPlace.

import (
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/google/uuid"
)

func (a *App) MCPTools() []sdk.Tool {
	return []sdk.Tool{
		// ─── Lifecycle ────────────────────────────────────────────
		{Name: "portfolio_create", Description: "Create a new portfolio. Args: name, mandate, allowed_classes, starting_cash (paper only — live pulls from broker), mode (paper|live; default paper), broker_slug (live only — e.g. binance-trading, alpaca-trading; optional if exactly one broker is bound). Use brokers_list first to see what's available.",
			InputSchema: schemaObject(map[string]any{
				"name":            map[string]any{"type": "string"},
				"mandate":         map[string]any{"type": "string"},
				"allowed_classes": map[string]any{"type": "array"},
				"starting_cash":   map[string]any{"type": "number"},
				"mode":            map[string]any{"type": "string", "enum": []string{"paper", "live"}},
				"broker_slug":     map[string]any{"type": "string"},
			}, []string{"name"}),
			Handler: a.toolPortfolioCreate},

		{Name: "brokers_list", Description: "List broker adapters registered in this build and their currently-bound connections. Use before portfolio_create with mode=live to pick a broker_slug.",
			InputSchema: schemaObject(nil, nil),
			Handler: a.toolBrokersList},

		// ─── Reads ────────────────────────────────────────────────
		{Name: "portfolio_list", Description: "List portfolios visible in this project.",
			InputSchema: schemaObject(nil, nil),
			Handler: a.toolPortfolioList},

		{Name: "portfolio_get", Description: "Snapshot of one portfolio (mandate, equity, cash, status, watchlist).",
			InputSchema: schemaObject(map[string]any{
				"portfolio_id": map[string]any{"type": "integer"},
			}, []string{"portfolio_id"}),
			Handler: a.toolPortfolioGet},

		{Name: "account_summary", Description: "Equity, cash, buying power, day + open P&L for one portfolio.",
			InputSchema: schemaObject(map[string]any{
				"portfolio_id": map[string]any{"type": "integer"},
			}, []string{"portfolio_id"}),
			Handler: a.toolAccountSummary},

		{Name: "positions_list", Description: "Open positions for one portfolio.",
			InputSchema: schemaObject(map[string]any{
				"portfolio_id": map[string]any{"type": "integer"},
				"asset_class":  map[string]any{"type": "string"},
			}, []string{"portfolio_id"}),
			Handler: a.toolPositionsList},

		{Name: "orders_list", Description: "Working / filled / cancelled orders for one portfolio. Default status filter = working.",
			InputSchema: schemaObject(map[string]any{
				"portfolio_id": map[string]any{"type": "integer"},
				"status":       map[string]any{"type": "string"},
				"limit":        map[string]any{"type": "integer"},
			}, []string{"portfolio_id"}),
			Handler: a.toolOrdersList},

		{Name: "market_quote", Description: "Latest mark for a symbol — equity/crypto USD price or polymarket YES/NO probabilities.",
			InputSchema: schemaObject(map[string]any{
				"symbol": map[string]any{"type": "string"},
			}, []string{"symbol"}),
			Handler: a.toolMarketQuote},

		{Name: "market_history", Description: "OHLCV bars / probability history for a symbol. range: 1D | 5D | 1M | 3M | 1Y | ALL.",
			InputSchema: schemaObject(map[string]any{
				"symbol": map[string]any{"type": "string"},
				"range":  map[string]any{"type": "string"},
			}, []string{"symbol"}),
			Handler: a.toolMarketHistory},

		{Name: "market_source", Description: "Report the live data source per asset class — name (binance-public / polymarket-public / mock), last_ok_at, errors_60s, stale. Lets the agent self-report whether it's reasoning on real prices or the offline mock.",
			InputSchema: schemaObject(nil, nil),
			Handler: a.toolMarketSource},

		{Name: "journal_read", Description: "Read recent journal entries for a portfolio.",
			InputSchema: schemaObject(map[string]any{
				"portfolio_id": map[string]any{"type": "integer"},
				"kind":         map[string]any{"type": "string"},
				"since":        map[string]any{"type": "string"},
				"limit":        map[string]any{"type": "integer"},
			}, []string{"portfolio_id"}),
			Handler: a.toolJournalRead},

		// ─── Writes ───────────────────────────────────────────────
		{Name: "order_place", Description: "Place a paper order. Required rationale ≥ 30 chars; subject to mandate + size cap + daily-loss halt.",
			InputSchema: schemaObject(map[string]any{
				"portfolio_id": map[string]any{"type": "integer"},
				"symbol":       map[string]any{"type": "string"},
				"side":         map[string]any{"type": "string", "enum": []string{"buy", "sell", "yes", "no"}},
				"type":         map[string]any{"type": "string", "enum": []string{"market", "limit", "stop"}},
				"qty":          map[string]any{"type": "number"},
				"limit_price":  map[string]any{"type": "number"},
				"stop_price":   map[string]any{"type": "number"},
				"tif":          map[string]any{"type": "string"},
				"rationale":    map[string]any{"type": "string"},
			}, []string{"portfolio_id", "symbol", "side", "type", "qty", "rationale"}),
			Handler: a.toolOrderPlace},

		{Name: "order_cancel", Description: "Cancel a working order. No-op on already-resolved orders.",
			InputSchema: schemaObject(map[string]any{
				"order_id": map[string]any{"type": "string"},
				"reason":   map[string]any{"type": "string"},
			}, []string{"order_id"}),
			Handler: a.toolOrderCancel},

		{Name: "journal_write", Description: "Append a thesis / rationale / note to a portfolio's journal.",
			InputSchema: schemaObject(map[string]any{
				"portfolio_id": map[string]any{"type": "integer"},
				"kind":         map[string]any{"type": "string"},
				"body":         map[string]any{"type": "string"},
				"metadata":     map[string]any{"type": "object"},
			}, []string{"portfolio_id", "kind", "body"}),
			Handler: a.toolJournalWrite},

		{Name: "watchlist_add", Description: "Track a symbol on a portfolio.",
			InputSchema: schemaObject(map[string]any{
				"portfolio_id": map[string]any{"type": "integer"},
				"symbol":       map[string]any{"type": "string"},
			}, []string{"portfolio_id", "symbol"}),
			Handler: a.toolWatchlistAdd},

		{Name: "watchlist_remove", Description: "Stop tracking a symbol.",
			InputSchema: schemaObject(map[string]any{
				"portfolio_id": map[string]any{"type": "integer"},
				"symbol":       map[string]any{"type": "string"},
			}, []string{"portfolio_id", "symbol"}),
			Handler: a.toolWatchlistRemove},

		// ─── Governance ───────────────────────────────────────────
		{Name: "alert_create", Description: "Create a price/probability/PNL alert. Fires SendEvent to the bound instances on match.",
			InputSchema: schemaObject(map[string]any{
				"portfolio_id": map[string]any{"type": "integer"},
				"symbol":       map[string]any{"type": "string"},
				"rule":         map[string]any{"type": "string", "enum": []string{"mark_above", "mark_below", "yes_above", "yes_below", "day_pnl_below"}},
				"threshold":    map[string]any{"type": "number"},
				"expires_at":   map[string]any{"type": "string"},
			}, []string{"portfolio_id", "symbol", "rule", "threshold"}),
			Handler: a.toolAlertCreate},

		{Name: "portfolio_pause", Description: "Pause this portfolio (no new orders accepted). Self-only — resume requires the dashboard.",
			InputSchema: schemaObject(map[string]any{
				"portfolio_id": map[string]any{"type": "integer"},
				"reason":       map[string]any{"type": "string"},
			}, []string{"portfolio_id", "reason"}),
			Handler: a.toolPortfolioPause},
	}
}

// ─── Lifecycle handlers ───────────────────────────────────────────

func (a *App) toolPortfolioCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil { return nil, err }
	name := strArg(args, "name")
	if name == "" { return nil, errors.New("name required") }
	mandate := strArg(args, "mandate")

	mode := strings.ToLower(strings.TrimSpace(strArg(args, "mode")))
	if mode == "" {
		mode = "paper"
	}
	if mode != "paper" && mode != "live" {
		return rejectStruct("invalid_mode", fmt.Sprintf("mode must be paper|live, got %q", mode)), nil
	}

	classesAny, _ := args["allowed_classes"].([]any)
	classes := make([]string, 0, len(classesAny))
	for _, c := range classesAny {
		if s, ok := c.(string); ok { classes = append(classes, s) }
	}
	if len(classes) == 0 {
		if mode == "live" {
			classes = []string{"crypto"}
		} else {
			classes = []string{"equity", "etf"}
		}
	}

	// ─── Live: broker required, cash + holdings come from the broker ──
	if mode == "live" {
		brokerSlug := strings.TrimSpace(strArg(args, "broker_slug"))
		if brokerSlug == "" {
			// Single-broker-bound installs can default. Multi-broker
			// installs surface an error so the agent picks explicitly.
			defaulted, err := defaultBrokerSlug(ctx)
			if err != nil {
				return rejectStruct("broker_slug_required",
					"mode=live requires broker_slug; "+err.Error()), nil
			}
			brokerSlug = defaulted
		}
		adapter := adapterBySlug(brokerSlug)
		if adapter == nil {
			return rejectStruct("unsupported_broker",
				fmt.Sprintf("no adapter registered for %q (known: %v)", brokerSlug, registeredSlugs())), nil
		}
		// Validate every requested class against the adapter's capabilities.
		for _, c := range classes {
			if !adapterSupportsClass(adapter, c) {
				return rejectStruct("class_unsupported_by_broker",
					fmt.Sprintf("broker %s supports %v; cannot trade class %q",
						brokerSlug, adapter.Capabilities().AssetClasses, c)), nil
			}
		}
		// Resolve a live connection of this slug + pull initial account.
		// brokerFor needs a Portfolio to know the slug, so we build a
		// throwaway and reuse the same lookup at runtime later.
		probe := &Portfolio{Mode: "live", BrokerSlug: brokerSlug}
		bb, ferr := brokerFor(ctx, probe)
		if ferr != nil {
			return rejectStruct("broker_unbound",
				fmt.Sprintf("broker %s has no active connection bound; bind one in app settings", brokerSlug)), nil
		}
		acctRaw, callErr := ctx.PlatformAPI().ExecuteIntegrationTool(
			bb.ConnectionID, bb.toolFor("account.summary"), map[string]any{},
		)
		if callErr != nil || acctRaw == nil || !acctRaw.Success {
			code, detail := adapter.ErrText(acctRaw, callErr)
			return rejectStruct("broker_error",
				fmt.Sprintf("could not read broker account (%s): %s", code, detail)), nil
		}
		acct, parseErr := adapter.ParseAccount(acctRaw.Data)
		if parseErr != nil {
			return rejectStruct("broker_error",
				"could not parse broker account: "+parseErr.Error()), nil
		}
		// Adapters with a separate holdings call (Alpaca) — issue the
		// second tool now and merge.
		if tool := adapter.HoldingsTool(); tool != "" {
			posRaw, perr := ctx.PlatformAPI().ExecuteIntegrationTool(
				bb.ConnectionID, tool, map[string]any{},
			)
			if perr == nil && posRaw != nil && posRaw.Success {
				if holdings, herr := adapter.ParseHoldings(posRaw.Data); herr == nil {
					if acct.Holdings == nil {
						acct.Holdings = map[string]brokerBalance{}
					}
					for k, v := range holdings {
						acct.Holdings[k] = v
					}
				}
			}
		}
		cash := acct.QuoteCash
		id, err := dbCreatePortfolio(ctx.AppDB(), &Portfolio{
			ProjectID: pid, Name: name, Mandate: mandate,
			AllowedClasses: classes, StartingCash: cash,
			Mode: "live", BrokerSlug: brokerSlug,
		})
		if err != nil { return nil, err }

		// Seed positions from broker holdings. Symbols arrive in the
		// adapter's canonical form (BTC-USD for binance, AAPL / BTC-USD
		// for alpaca via fromAlpacaSymbol). For Alpaca, bal.AvgCost
		// carries the broker's real cost basis (avg_entry_price); for
		// Binance, bal.AvgCost is 0 (not published by get_account) and
		// we fall back to the current mark so unrealized P&L is 0 at
		// seed time instead of bogus.
		seeded := 0
		seededWithCost := 0
		for canonical, bal := range acct.Holdings {
			cls := inferAssetClass(canonical)
			cost := bal.AvgCost
			if cost <= 0 {
				if mark, _ := dbGetMark(ctx.AppDB(), canonical); mark != nil {
					cost = mark.Price
				}
			} else {
				seededWithCost++
			}
			_ = dbInsertPositionRaw(ctx.AppDB(), pid, id, canonical, cls, "", bal.Free, cost)
			seeded++
		}
		if seeded > 0 {
			body := ""
			if seededWithCost == seeded {
				body = fmt.Sprintf("Seeded %d position(s) from %s holdings on portfolio create with broker-reported avg_entry_price.", seeded, brokerSlug)
			} else if seededWithCost > 0 {
				body = fmt.Sprintf("Seeded %d position(s) from %s holdings on portfolio create. %d had broker-reported cost basis; the rest defaulted to current mark.", seeded, brokerSlug, seededWithCost)
			} else {
				body = fmt.Sprintf("Seeded %d position(s) from %s holdings on portfolio create. %s doesn't publish cost basis — seeded to current mark; sell-side P&L will be off by the actual cost basis until you reset it.", seeded, brokerSlug, brokerSlug)
			}
			if entryID, jerr := dbInsertJournal(ctx.AppDB(), pid, id, "note", body, map[string]any{
				"source": "broker_reconcile", "kind": "create_seed",
				"broker_slug": brokerSlug, "broker_connection_id": bb.ConnectionID,
				"seeded": seeded, "seeded_with_cost": seededWithCost,
			}); jerr == nil {
				emit("journal.appended", map[string]any{
					"id": entryID, "portfolio_id": id, "kind": "note", "body": body,
				})
			}
		}
		// Backfill order + fill history from the broker. Best-effort
		// — failures here don't block portfolio creation; the next
		// reconcile cycle will eventually fill the gap. Adapters that
		// don't support history backfill (Binance, Polymarket today)
		// return ("", nil) and we silently skip.
		backfilled, openSynced := backfillBrokerHistory(ctx, pid, id, bb)
		if backfilled+openSynced > 0 {
			body := fmt.Sprintf("Backfilled %d historical order(s) and %d open order(s) from %s.", backfilled, openSynced, brokerSlug)
			if entryID, jerr := dbInsertJournal(ctx.AppDB(), pid, id, "note", body, map[string]any{
				"source":              "broker_backfill",
				"kind":                "history_backfill",
				"broker_slug":         brokerSlug,
				"broker_connection_id": bb.ConnectionID,
				"historical_orders":   backfilled,
				"open_orders_synced":  openSynced,
			}); jerr == nil {
				emit("journal.appended", map[string]any{
					"id": entryID, "portfolio_id": id, "kind": "note", "body": body,
				})
			}
		}

		emit("portfolio.created", map[string]any{
			"id": id, "name": name, "mandate": mandate,
			"allowed_classes": classes, "starting_cash": cash,
			"mode": "live", "project_id": pid,
			"broker_slug": brokerSlug, "broker_connection_id": bb.ConnectionID,
		})
		return map[string]any{
			"portfolio_id":     id,
			"name":             name,
			"starting_cash":    cash,
			"mode":             "live",
			"broker_slug":      brokerSlug,
			"seeded_positions": seeded,
		}, nil
	}

	// ─── Paper (default) ─────────────────────────────────────────────
	cash := floatArg(args, "starting_cash", 0)
	if cash <= 0 {
		def := ctx.Config().Get("starting_cash")
		if v, parseErr := strconv.ParseFloat(def, 64); parseErr == nil && v > 0 {
			cash = v
		} else {
			cash = 100_000
		}
	}
	id, err := dbCreatePortfolio(ctx.AppDB(), &Portfolio{
		ProjectID: pid, Name: name, Mandate: mandate,
		AllowedClasses: classes, StartingCash: cash, Mode: "paper",
	})
	if err != nil { return nil, err }
	emit("portfolio.created", map[string]any{
		"id": id, "name": name, "mandate": mandate,
		"allowed_classes": classes, "starting_cash": cash,
		"mode": "paper", "project_id": pid,
	})
	return map[string]any{"portfolio_id": id, "name": name, "starting_cash": cash, "mode": "paper"}, nil
}

// defaultBrokerSlug — when portfolio_create's `broker_slug` arg is
// absent, infer it from what's actually bound. If exactly one broker
// slug has an active connection, use it. Otherwise the agent must pick
// explicitly to avoid binding crypto to an equity-only broker by
// accident.
func defaultBrokerSlug(ctx *sdk.AppCtx) (string, error) {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return "", errors.New("platform unavailable")
	}
	seen := map[string]bool{}
	for _, a := range allAdapters() {
		conns, err := ctx.PlatformAPI().ListConnections(sdk.ConnectionFilter{AppSlug: a.Slug()})
		if err != nil {
			continue
		}
		for _, c := range conns {
			if c.Status != "" && c.Status != "active" && c.Status != "connected" {
				continue
			}
			seen[a.Slug()] = true
		}
	}
	switch len(seen) {
	case 0:
		return "", fmt.Errorf("no broker connection bound (known adapters: %v)", registeredSlugs())
	case 1:
		for slug := range seen {
			return slug, nil
		}
	}
	// Multiple bound: ambiguous, force the agent to choose.
	bound := make([]string, 0, len(seen))
	for slug := range seen {
		bound = append(bound, slug)
	}
	return "", fmt.Errorf("multiple brokers bound (%v) — pass broker_slug to disambiguate", bound)
}

// backfillBrokerHistory — best-effort sync of historical closed orders
// + currently-open orders from the broker into the local DB. Called
// once at portfolio_create. Returns (historicalCount, openCount) for
// the audit journal line.
//
// Failures are swallowed — backfill is a quality-of-life surface, not
// a correctness one. The next reconcile tick still picks up open
// orders via their broker_order_id; missing history just means the
// orders/fills tables start sparse.
func backfillBrokerHistory(ctx *sdk.AppCtx, projectID string, portfolioID int64, bb *boundBroker) (int, int) {
	historical := 0
	open := 0

	// ─── Historical (closed) orders ─────────────────────────────
	if tool, args := bb.Adapter.OrdersHistoryTool(); tool != "" {
		historical = importBrokerOrders(ctx, projectID, portfolioID, bb, tool, args, "backfill")
	}

	// ─── Currently-open broker orders ───────────────────────────
	if tool, args := bb.Adapter.OpenOrdersTool(); tool != "" {
		open = importBrokerOrders(ctx, projectID, portfolioID, bb, tool, args, "open_sync")
	}

	return historical, open
}

// importBrokerOrders — shared body for both backfill paths. Fetches +
// parses + inserts; returns how many rows it actually wrote (skipping
// dupes when the broker's order_id is already in our journal).
//
// `kind` flavors the local Order.ID prefix and the journal source so
// audit queries can tell history-fill from open-order-sync apart:
//
//	kind=backfill  → o-bf-<broker_id_short>, source=broker_backfill
//	kind=open_sync → o-os-<broker_id_short>, source=broker_open_sync
func importBrokerOrders(ctx *sdk.AppCtx, projectID string, portfolioID int64, bb *boundBroker, tool string, args map[string]any, kind string) int {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(bb.ConnectionID, tool, args)
	if err != nil || res == nil || !res.Success {
		return 0
	}
	rows, err := bb.Adapter.ParseOrders(res.Data)
	if err != nil || len(rows) == 0 {
		return 0
	}

	prefix := "o-bf-"
	source := "broker_backfill:live"
	if kind == "open_sync" {
		prefix = "o-os-"
		source = "broker_open_sync:live"
	}

	wrote := 0
	for _, r := range rows {
		// Idempotency: skip if we already imported this broker_order_id
		// (re-runs of portfolio_create against the same connection
		// shouldn't double-insert).
		if existing, _ := dbOrderIDByBrokerID(ctx.AppDB(), r.BrokerOrderID); existing != "" {
			continue
		}
		// Skip ClientOrderIDs that match our local format (orders
		// originally placed via this app). If they're already in
		// `orders` we'd hit primary-key conflicts; if they're not
		// (orphaned), they likely belong to a different install — not
		// our story to tell.
		if r.ClientOrderID != "" && strings.HasPrefix(r.ClientOrderID, "o-") {
			if existing, _ := dbGetOrder(ctx.AppDB(), projectID, r.ClientOrderID); existing != nil {
				continue
			}
		}

		localID := prefix + safeBrokerIDShort(r.BrokerOrderID)
		rationale := "Imported from broker on portfolio_create — no operator rationale captured. Trust the broker's record of intent."

		if err := dbInsertBackfilledOrder(
			ctx.AppDB(), projectID, portfolioID, localID,
			r.Symbol, r.AssetClass, r.Side, r.Type,
			r.Qty, r.FilledQty, r.AvgFillPrice,
			r.LimitPrice, r.StopPrice, r.TIF,
			r.Status, rationale, source,
			r.PlacedAt, r.ResolvedAt,
		); err != nil {
			ctx.Logger().Warn("backfill: insert order failed",
				"broker_order_id", r.BrokerOrderID, "err", err)
			continue
		}

		// Fill row for filled qty. Use the broker's resolved_at as the
		// fill timestamp when available so equity curves line up.
		if r.FilledQty > 0 && r.AvgFillPrice > 0 {
			fillAt := r.ResolvedAt
			if fillAt == "" {
				fillAt = r.PlacedAt
			}
			_ = dbInsertBackfilledFill(ctx.AppDB(), projectID, localID, portfolioID,
				r.FilledQty, r.AvgFillPrice, 0 /* fee unknown */, fillAt)
		}

		// Rationale journal row carrying the broker_order_id so the
		// existing cancel + status-poll paths can resolve it. Without
		// this row, dbBrokerOrderIDFor would return "" and live
		// cancel against an open-sync'd order would fall to local-only.
		_, _ = dbInsertJournal(ctx.AppDB(), projectID, portfolioID, "rationale", rationale, map[string]any{
			"order_id":              localID,
			"symbol":                r.Symbol,
			"side":                  r.Side,
			"qty":                   r.Qty,
			"type":                  r.Type,
			"broker_slug":           bb.Adapter.Slug(),
			"broker_connection_id":  bb.ConnectionID,
			"broker_order_id":       r.BrokerOrderID,
			"client_order_id":       r.ClientOrderID,
			"source":                source,
			"backfill_status":       r.BrokerStatus,
		})

		wrote++
	}
	return wrote
}

// safeBrokerIDShort — crops a long broker id (Alpaca UUIDs are 36 chars)
// to something the orders.id column can carry alongside our prefix.
// Strip non-alphanumeric so the resulting id is URL-safe for the
// /orders/{id}/cancel route.
func safeBrokerIDShort(s string) string {
	var b strings.Builder
	for _, c := range s {
		if (c >= '0' && c <= '9') || (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') {
			b.WriteRune(c)
			if b.Len() >= 16 {
				break
			}
		}
	}
	if b.Len() == 0 {
		return "unknown"
	}
	return b.String()
}

func registeredSlugs() []string {
	as := allAdapters()
	out := make([]string, 0, len(as))
	for _, a := range as {
		out = append(out, a.Slug())
	}
	return out
}

// toolBrokersList — enumerate registered adapters + bound connections,
// so the agent can decide which slug to pass to portfolio_create. Pure
// read; safe to call without a portfolio.
func (a *App) toolBrokersList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	out := make([]map[string]any, 0)
	for _, ad := range allAdapters() {
		caps := ad.Capabilities()
		row := map[string]any{
			"slug":          ad.Slug(),
			"asset_classes": caps.AssetClasses,
			"order_types":   caps.OrderTypes,
			"tifs":          caps.TIFs,
			"fractional":    caps.Fractional,
			"quote":         caps.QuoteCurrency,
			"connections":   []map[string]any{},
		}
		if ctx != nil && ctx.PlatformAPI() != nil {
			conns, err := ctx.PlatformAPI().ListConnections(sdk.ConnectionFilter{AppSlug: ad.Slug()})
			if err == nil {
				rows := make([]map[string]any, 0, len(conns))
				for _, c := range conns {
					rows = append(rows, map[string]any{
						"id": c.ID, "name": c.Name, "status": c.Status,
					})
				}
				row["connections"] = rows
				row["bound"] = len(rows) > 0
			}
		}
		out = append(out, row)
	}
	return map[string]any{"brokers": out}, nil
}

// ─── Read handlers ────────────────────────────────────────────────

func (a *App) toolPortfolioList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil { return nil, err }
	pfs, err := dbListPortfolios(ctx.AppDB(), pid)
	if err != nil { return nil, err }
	out := make([]map[string]any, 0, len(pfs))
	for _, p := range pfs {
		snap, _ := snapshotPortfolio(ctx.AppDB(), p)
		out = append(out, map[string]any{
			"id": snap.ID, "name": snap.Name, "mandate": snap.Mandate,
			"allowed_classes": snap.AllowedClasses, "status": snap.Status,
			"equity": snap.Equity, "cash": snap.Cash,
			"day_pnl": snap.DayPnL, "day_pnl_pct": snap.DayPnLPct,
		})
	}
	return map[string]any{"portfolios": out}, nil
}

func (a *App) toolPortfolioGet(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil { return nil, err }
	id := int64Arg(args, "portfolio_id", 0)
	if id == 0 { return nil, errors.New("portfolio_id required") }
	p, err := dbGetPortfolio(ctx.AppDB(), pid, id)
	if err != nil { return nil, fmt.Errorf("portfolio %d not found in project", id) }
	snap, _ := snapshotPortfolio(ctx.AppDB(), p)
	return map[string]any{"portfolio": snap}, nil
}

func (a *App) toolAccountSummary(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil { return nil, err }
	id := int64Arg(args, "portfolio_id", 0)
	p, err := dbGetPortfolio(ctx.AppDB(), pid, id)
	if err != nil { return nil, fmt.Errorf("portfolio %d not found", id) }
	snap, _ := snapshotPortfolio(ctx.AppDB(), p)
	return map[string]any{
		"equity": snap.Equity, "cash": snap.Cash, "buying_power": snap.BuyingPower,
		"day_pnl": snap.DayPnL, "day_pnl_pct": snap.DayPnLPct,
		"open_pnl": snap.OpenPnL, "open_pnl_pct": snap.OpenPnLPct,
		"status": snap.Status, "mode": snap.Mode,
	}, nil
}

func (a *App) toolPositionsList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil { return nil, err }
	id := int64Arg(args, "portfolio_id", 0)
	p, err := dbGetPortfolio(ctx.AppDB(), pid, id)
	if err != nil { return nil, fmt.Errorf("portfolio %d not found", id) }
	snap, _ := snapshotPortfolio(ctx.AppDB(), p) // computes weights + marks
	pos, _ := dbListPositions(ctx.AppDB(), id)
	classFilter, _ := args["asset_class"].(string)
	out := make([]*Position, 0, len(pos))
	for _, q := range pos {
		// Re-merge the snapshot's computed fields (weight, market_value, etc.)
		// onto the bare row.
		mark, _ := dbGetMark(ctx.AppDB(), q.Symbol)
		if mark != nil {
			q.MarketPrice = markPriceForSide(mark, q.Outcome)
		} else {
			q.MarketPrice = q.AvgCost
		}
		q.MarketValue = q.MarketPrice * q.Qty
		q.UnrealizedPnL = (q.MarketPrice - q.AvgCost) * q.Qty
		if q.AvgCost > 0 && q.Qty > 0 {
			q.UnrealizedPnLPct = (q.MarketPrice/q.AvgCost - 1) * 100
		}
		if snap.Equity > 0 {
			q.WeightPct = q.MarketValue / snap.Equity * 100
		}
		if classFilter != "" && q.AssetClass != classFilter {
			continue
		}
		out = append(out, q)
	}
	return map[string]any{"positions": out}, nil
}

func (a *App) toolOrdersList(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	_, err := resolveProjectFromArgs(args)
	if err != nil { return nil, err }
	id := int64Arg(args, "portfolio_id", 0)
	status, _ := args["status"].(string)
	if status == "" { status = "working" }
	limit := intArg(args, "limit", 50)
	rows, err := dbListOrders(ctx.AppDB(), id, status, limit)
	if err != nil { return nil, err }
	if rows == nil {
		rows = []*Order{}
	}
	return map[string]any{"orders": rows, "count": len(rows)}, nil
}

func (a *App) toolMarketQuote(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	symbol, _ := args["symbol"].(string)
	if symbol == "" { return nil, errors.New("symbol required") }
	mark, err := dbGetMark(ctx.AppDB(), symbol)
	if err != nil {
		// Fall back to live provider if engine hasn't ticked yet.
		if globalEngine != nil {
			mark, err = globalEngine.provider.Quote(symbol)
		}
		if err != nil { return nil, err }
	}
	out := map[string]any{
		"symbol": mark.Symbol, "asset_class": mark.AssetClass,
		"marked_at": mark.MarkedAt,
	}
	if mark.AssetClass == "polymarket" {
		out["yes_price"] = mark.Price
		if mark.NoPrice != nil { out["no_price"] = *mark.NoPrice }
	} else {
		out["price"] = mark.Price
		if mark.PrevClose != nil {
			out["prev_close"] = *mark.PrevClose
			out["change_pct_24h"] = (mark.Price/(*mark.PrevClose) - 1) * 100
		}
	}
	if mark.Volume24h != nil { out["volume_24h"] = *mark.Volume24h }
	return out, nil
}

func (a *App) toolMarketHistory(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	if globalEngine == nil { return nil, errors.New("engine not ready") }
	symbol, _ := args["symbol"].(string)
	rng, _ := args["range"].(string)
	if rng == "" { rng = "1D" }
	bars, err := globalEngine.provider.Bars(symbol, rng)
	if err != nil { return nil, err }
	return map[string]any{"symbol": symbol, "range": rng, "bars": bars}, nil
}

func (a *App) toolMarketSource(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	out := map[string]any{
		"providers": providerHealthSnapshot(),
	}
	if globalEngine != nil {
		out["last_tick_at"] = globalEngine.lastTickAt
		out["ticks"] = globalEngine.ticks
	}
	return out, nil
}

func (a *App) toolJournalRead(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	_, err := resolveProjectFromArgs(args)
	if err != nil { return nil, err }
	id := int64Arg(args, "portfolio_id", 0)
	kind, _ := args["kind"].(string)
	since, _ := args["since"].(string)
	limit := intArg(args, "limit", 50)
	entries, err := dbReadJournal(ctx.AppDB(), id, kind, since, limit)
	if err != nil { return nil, err }
	if entries == nil { entries = []*JournalEntry{} }
	return map[string]any{"entries": entries}, nil
}

// ─── Write handlers ───────────────────────────────────────────────

const minRationaleLen = 30

func (a *App) toolOrderPlace(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil { return nil, err }
	id := int64Arg(args, "portfolio_id", 0)
	pf, err := dbGetPortfolio(ctx.AppDB(), pid, id)
	if err != nil { return nil, fmt.Errorf("portfolio %d not found", id) }

	// Pre-trade pipeline. Each rejection returns a structured status —
	// the agent should reason about it, not raise.
	rationale, _ := args["rationale"].(string)
	if len(strings.TrimSpace(rationale)) < minRationaleLen {
		return rejectStruct("rationale_required",
			fmt.Sprintf("rationale must be ≥ %d chars; got %d", minRationaleLen, len(rationale))), nil
	}
	if pf.Status != "active" {
		return rejectStruct("portfolio_not_active",
			fmt.Sprintf("portfolio status is %q", pf.Status)), nil
	}
	symbol, _ := args["symbol"].(string)
	side, _ := args["side"].(string)
	otype, _ := args["type"].(string)
	qty := floatArg(args, "qty", 0)
	if symbol == "" || side == "" || otype == "" || qty <= 0 {
		return rejectStruct("invalid_args", "symbol, side, type, qty are required and qty > 0"), nil
	}
	class := inferAssetClass(symbol)
	if !contains(pf.AllowedClasses, class) {
		return rejectStruct("asset_class_blocked",
			fmt.Sprintf("class %q not in allowed_classes %v", class, pf.AllowedClasses)), nil
	}
	// Side / class consistency.
	if class == "polymarket" {
		if side != "yes" && side != "no" {
			return rejectStruct("invalid_side", "polymarket side must be 'yes' or 'no'"), nil
		}
		if otype == "stop" {
			return rejectStruct("invalid_type", "stop orders not supported on polymarket"), nil
		}
	} else {
		if side != "buy" && side != "sell" {
			return rejectStruct("invalid_side", "equity/crypto side must be 'buy' or 'sell'"), nil
		}
	}
	tif, _ := args["tif"].(string)
	if tif == "" { tif = "day" }
	var lp, sp *float64
	if v := floatArg(args, "limit_price", 0); v > 0 { lp = &v }
	if v := floatArg(args, "stop_price", 0); v > 0  { sp = &v }
	if otype == "limit" && lp == nil {
		return rejectStruct("invalid_args", "limit orders require limit_price"), nil
	}
	if otype == "stop" && sp == nil {
		return rejectStruct("invalid_args", "stop orders require stop_price"), nil
	}
	if class == "polymarket" && lp != nil {
		if *lp <= 0 || *lp >= 1 {
			return rejectStruct("invalid_args", "polymarket limit_price must be in (0, 1)"), nil
		}
	}

	// Source — "agent" by default; HTTP path overrides via source_override.
	// Live mode appends ":live" so audit queries can split paper from live
	// without joining onto the portfolio row.
	source := "agent"
	if so, ok := args["source_override"].(string); ok && so != "" {
		source = so
	}
	if pf.Mode == "live" {
		source += ":live"
	}

	// Build + write the order. Status begins working — for paper, the
	// engine picks it up on the next tick; for live, we forward to the
	// broker below and reflect the response inline.
	o := &Order{
		ID:           "o-" + strings.ReplaceAll(uuid.NewString(), "-", "")[:12],
		PortfolioID:  pf.ID,
		Symbol:       symbol,
		AssetClass:   class,
		Side:         side,
		Type:         otype,
		Qty:          qty,
		LimitPrice:   lp,
		StopPrice:    sp,
		TIF:          tif,
		Status:       "working",
		Rationale:    rationale,
		Source:       source,
	}
	if err := dbInsertOrder(ctx.AppDB(), o, pid); err != nil {
		return nil, err
	}

	// ─── Live: forward to broker, apply response inline ─────────────
	if pf.Mode == "live" {
		bb, ferr := brokerFor(ctx, pf)
		if ferr != nil {
			// Unbound mid-flight (operator unbound after portfolio create),
			// or the slug isn't registered. Local order flips rejected; agent
			// gets a clear code so they can stop retrying.
			code := "broker_unbound"
			detail := "portfolio is mode=live but no broker is bound; rebind to resume"
			if errors.Is(ferr, errBrokerUnbound) {
				detail = fmt.Sprintf("broker %s has no active connection bound; rebind in app settings", pf.BrokerSlug)
			} else if ferr != errPaper {
				code = "broker_lookup_failed"
				detail = ferr.Error()
			}
			_ = dbRejectOrder(ctx.AppDB(), o.ID, code, detail)
			emit("order.rejected", map[string]any{
				"order_id": o.ID, "portfolio_id": pf.ID,
				"code": code, "detail": detail,
			})
			return rejectStruct(code, detail), nil
		}
		adapter := bb.Adapter
		// Capability check — defense in depth; portfolio_create already
		// validated allowed_classes against the adapter, but a watchlist
		// add could have widened the agent's reach.
		if !adapterSupportsClass(adapter, class) {
			_ = dbRejectOrder(ctx.AppDB(), o.ID, "broker_unsupported_class",
				fmt.Sprintf("%s adapter does not support %q (supports %v)", adapter.Slug(), class, adapter.Capabilities().AssetClasses))
			emit("order.rejected", map[string]any{"order_id": o.ID, "code": "broker_unsupported_class"})
			return rejectStruct("broker_unsupported_class",
				fmt.Sprintf("%s adapter does not support %q", adapter.Slug(), class)), nil
		}
		// Pre-trade cash check (live). Mirrors the paper-engine check in
		// tryFill. Uses local pf.Cash (broker-reconciled, ≤60s stale) and
		// the best price estimate we have. Broker will reject if we
		// somehow get past this, but burning an API call on an obviously
		// underfunded order isn't worth it.
		if isBuySide(side) {
			estPrice := 0.0
			if lp != nil {
				estPrice = *lp
			} else if mark, mErr := dbGetMark(ctx.AppDB(), symbol); mErr == nil && mark != nil {
				estPrice = mark.Price
			}
			if estPrice > 0 {
				needed := qty * estPrice * 1.005 // 0.5% buffer for slippage + fees
				if pf.Cash < needed {
					detail := fmt.Sprintf("estimated need %.2f, local cash %.2f (broker-synced ≤60s ago)", needed, pf.Cash)
					_ = dbRejectOrder(ctx.AppDB(), o.ID, "insufficient_cash", detail)
					emit("order.rejected", map[string]any{
						"order_id": o.ID, "portfolio_id": pf.ID,
						"code": "insufficient_cash", "detail": detail,
					})
					return rejectStruct("insufficient_cash", detail), nil
				}
			}
		}
		brokerArgs, terr := adapter.TranslateOrder(o)
		if terr != nil {
			_ = dbRejectOrder(ctx.AppDB(), o.ID, "translate_failed", terr.Error())
			emit("order.rejected", map[string]any{"order_id": o.ID, "code": "translate_failed", "detail": terr.Error()})
			return rejectStruct("translate_failed", terr.Error()), nil
		}
		res, callErr := ctx.PlatformAPI().ExecuteIntegrationTool(
			bb.ConnectionID, bb.toolFor("order.place"), brokerArgs,
		)
		// Ambiguous broker outcomes (network error, non-success response,
		// parse failure of a successful response) leave the local order in
		// 'working' state instead of marking it rejected. The broker may
		// have accepted the order even when the response didn't make it
		// back cleanly — rejecting locally while real money is working at
		// the broker is the unrecoverable case. tryReconcile polls by
		// client id next tick; if the broker reports "unknown order" the
		// reconciler rejects locally then (exec.go).
		uncertain := func(code, detail string) (any, error) {
			if _, jerr := dbInsertJournal(ctx.AppDB(), pid, pf.ID, "rationale", rationale, map[string]any{
				"order_id": o.ID, "symbol": symbol, "side": side, "qty": qty, "type": otype,
				"broker_slug":          adapter.Slug(),
				"broker_connection_id": bb.ConnectionID,
				"client_order_id":      o.ID,
				"broker_call_status":   "uncertain",
				"broker_call_code":     code,
				"broker_call_detail":   detail,
			}); jerr == nil {
				emit("journal.appended", map[string]any{
					"portfolio_id": pf.ID, "kind": "rationale", "body": rationale,
				})
			}
			emit("order.placed", map[string]any{
				"order_id": o.ID, "portfolio_id": pf.ID, "symbol": symbol, "asset_class": class,
				"side": side, "type": otype, "qty": qty,
				"limit_price": o.LimitPrice, "stop_price": o.StopPrice,
				"status": "working", "rationale": rationale, "mode": "live",
				"broker_slug": adapter.Slug(),
				"uncertain": true, "broker_call_code": code, "broker_call_detail": detail,
			})
			ctx.Logger().Warn("broker call uncertain — leaving order working for reconciler",
				"order_id", o.ID, "broker", adapter.Slug(), "code", code, "detail", detail)
			return map[string]any{
				"order_id":  o.ID,
				"status":    "working",
				"uncertain": true,
				"code":      code,
				"detail":    detail,
				"note":      "broker response was ambiguous; local order kept working — reconciler will poll by client_order_id",
			}, nil
		}
		if callErr != nil || res == nil || !res.Success {
			code, detail := adapter.ErrText(res, callErr)
			return uncertain(code, detail)
		}
		br, perr := adapter.ParseOrder(res.Data)
		if perr != nil {
			return uncertain("broker_parse_failed", perr.Error())
		}

		// Persist broker linkage in the rationale journal row before any
		// fill row lands, so an audit can join order → broker_order_id by
		// kind='rationale' alone.
		if entryID, jerr := dbInsertJournal(ctx.AppDB(), pid, pf.ID, "rationale", rationale, map[string]any{
			"order_id": o.ID, "symbol": symbol, "side": side, "qty": qty, "type": otype,
			"broker_slug":          adapter.Slug(),
			"broker_connection_id": bb.ConnectionID,
			"broker_order_id":      br.BrokerOrderID,
			"client_order_id":      o.ID,
		}); jerr == nil {
			emit("journal.appended", map[string]any{
				"id": entryID, "portfolio_id": pf.ID, "kind": "rationale", "body": rationale,
			})
		}

		// emit order.placed before applying fills so subscribers see
		// {placed, filled} in order rather than just a sudden FILLED.
		emit("order.placed", map[string]any{
			"order_id":         o.ID,
			"portfolio_id":     pf.ID,
			"symbol":           symbol,
			"asset_class":      class,
			"side":             side,
			"type":             otype,
			"qty":              qty,
			"limit_price":      o.LimitPrice,
			"stop_price":       o.StopPrice,
			"status":           "working",
			"rationale":        rationale,
			"mode":             "live",
			"broker_slug":      adapter.Slug(),
			"broker_order_id":  br.BrokerOrderID,
		})

		// Apply any inline fills (e.g. Binance market orders return them
		// synchronously) + reflect terminal status.
		if _, ferr := applyBrokerProgress(ctx.AppDB(), pid, pf, o, br); ferr != nil {
			ctx.Logger().Warn("apply broker progress failed", "order_id", o.ID, "err", ferr)
		}
		return map[string]any{
			"order_id":        o.ID,
			"broker_order_id": br.BrokerOrderID,
			"status":          o.Status,        // updated by applyBrokerProgress
			"filled_qty":      o.FilledQty,
			"avg_fill_price":  o.AvgFillPrice,
		}, nil
	}

	// ─── Paper (default) — engine fills on next tick ────────────────
	emit("order.placed", map[string]any{
		"order_id":     o.ID,
		"portfolio_id": pf.ID,
		"symbol":       symbol,
		"asset_class":  class,
		"side":         side,
		"type":         otype,
		"qty":          qty,
		"limit_price":  o.LimitPrice,
		"stop_price":   o.StopPrice,
		"status":       "working",
		"rationale":    rationale,
		"mode":         "paper",
	})
	// Auto-attach a rationale row to the journal for audit.
	if entryID, err := dbInsertJournal(ctx.AppDB(), pid, pf.ID, "rationale", rationale, map[string]any{
		"order_id": o.ID, "symbol": symbol, "side": side, "qty": qty, "type": otype,
	}); err == nil {
		emit("journal.appended", map[string]any{
			"id": entryID, "portfolio_id": pf.ID, "kind": "rationale", "body": rationale,
		})
	}
	return map[string]any{"order_id": o.ID, "status": "working"}, nil
}

func (a *App) toolOrderCancel(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil { return nil, err }
	id, _ := args["order_id"].(string)
	reason, _ := args["reason"].(string)
	if id == "" { return nil, errors.New("order_id required") }

	// Look the order up first so we know whether it's a live order that
	// needs a broker cancel before the local row flips.
	o, oerr := dbGetOrder(ctx.AppDB(), pid, id)
	if oerr != nil {
		return nil, fmt.Errorf("order %s not found", id)
	}
	if o.Status != "working" {
		return map[string]any{"status": "already_resolved", "order_id": id, "current_status": o.Status}, nil
	}
	pf, perr := dbGetPortfolio(ctx.AppDB(), pid, o.PortfolioID)
	if perr != nil {
		return nil, fmt.Errorf("portfolio %d not found for order %s", o.PortfolioID, id)
	}

	// Live: cancel upstream first. If the broker says "already filled" or
	// "not found", we still flip locally — the reconciler would have
	// caught it on the next tick anyway.
	if pf.Mode == "live" {
		bb, ferr := brokerFor(ctx, pf)
		if ferr != nil {
			return rejectStruct("broker_unbound",
				fmt.Sprintf("live cancel needs broker bound (slug=%s): %s", pf.BrokerSlug, ferr.Error())), nil
		}
		adapter := bb.Adapter
		brokerOrderID, _ := dbBrokerOrderIDFor(ctx.AppDB(), id)
		caps := adapter.Capabilities()
		if brokerOrderID == "" && !caps.CancelByClientID {
			// Adapter requires the broker id and we don't have one (order
			// placed pre-binding, or rationale journal is missing). Best
			// we can do is flip locally; reconciler will reconcile when
			// it next polls.
			ctx.Logger().Warn("live order missing broker_order_id and adapter requires it; local-cancel only",
				"order_id", id, "broker", adapter.Slug())
		} else {
			cancelArgs := adapter.CancelArgs(o, brokerOrderID)
			res, cerr := ctx.PlatformAPI().ExecuteIntegrationTool(
				bb.ConnectionID, bb.toolFor("order.cancel"), cancelArgs,
			)
			if cerr != nil || res == nil || !res.Success {
				code, detail := adapter.ErrText(res, cerr)
				// "Already gone" cases (Binance -2011 / Alpaca 422 'order
				// is not cancelable') are not failures from the local
				// perspective — the order is already in a terminal state
				// upstream; the reconciler will mirror it.
				if !adapter.IsUnknownOrderError(code, detail) &&
					!strings.Contains(strings.ToLower(detail), "not cancelable") &&
					!strings.Contains(strings.ToLower(detail), "unknown order") {
					return rejectStruct("broker_cancel_failed", code+": "+detail), nil
				}
				ctx.Logger().Info("broker reports order already resolved — proceeding with local cancel",
					"order_id", id, "broker", adapter.Slug(), "broker_order_id", brokerOrderID)
			}
		}
	}

	status, err := dbCancelOrder(ctx.AppDB(), pid, id, reason)
	if err != nil { return nil, err }
	if status == "cancelled" {
		emit("order.cancelled", map[string]any{"order_id": id, "reason": reason, "mode": pf.Mode})
		return map[string]any{"status": "cancelled", "order_id": id}, nil
	}
	return map[string]any{"status": "already_resolved", "order_id": id, "current_status": status}, nil
}

func (a *App) toolJournalWrite(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil { return nil, err }
	portfolioID := int64Arg(args, "portfolio_id", 0)
	kind, _ := args["kind"].(string)
	body, _ := args["body"].(string)
	if kind == "" || body == "" {
		return nil, errors.New("kind and body required")
	}
	meta, _ := args["metadata"].(map[string]any)
	entryID, err := dbInsertJournal(ctx.AppDB(), pid, portfolioID, kind, body, meta)
	if err != nil { return nil, err }
	emit("journal.appended", map[string]any{
		"id": entryID, "portfolio_id": portfolioID, "kind": kind, "body": body, "metadata": meta,
	})
	return map[string]any{"entry_id": entryID}, nil
}

func (a *App) toolWatchlistAdd(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil { return nil, err }
	id := int64Arg(args, "portfolio_id", 0)
	symbol, _ := args["symbol"].(string)
	pf, err := dbGetPortfolio(ctx.AppDB(), pid, id)
	if err != nil { return nil, fmt.Errorf("portfolio %d not found", id) }
	class := inferAssetClass(symbol)
	if !contains(pf.AllowedClasses, class) {
		return nil, fmt.Errorf("asset class %q not in portfolio's allowed_classes", class)
	}
	added, err := dbWatchlistAdd(ctx.AppDB(), pid, id, symbol)
	if err != nil { return nil, err }
	wl, _ := dbWatchlist(ctx.AppDB(), id)
	if added {
		emit("watchlist.changed", map[string]any{"portfolio_id": id, "watchlist": wl, "added": symbol})
	}
	return map[string]any{"added": added, "watchlist": wl}, nil
}

func (a *App) toolWatchlistRemove(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	_, err := resolveProjectFromArgs(args)
	if err != nil { return nil, err }
	id := int64Arg(args, "portfolio_id", 0)
	symbol, _ := args["symbol"].(string)
	removed, err := dbWatchlistRemove(ctx.AppDB(), id, symbol)
	if err != nil { return nil, err }
	wl, _ := dbWatchlist(ctx.AppDB(), id)
	if removed {
		emit("watchlist.changed", map[string]any{"portfolio_id": id, "watchlist": wl, "removed": symbol})
	}
	return map[string]any{"removed": removed, "watchlist": wl}, nil
}

func (a *App) toolAlertCreate(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil { return nil, err }
	a2 := &Alert{
		PortfolioID: int64Arg(args, "portfolio_id", 0),
		Symbol:      strArg(args, "symbol"),
		Rule:        strArg(args, "rule"),
		Threshold:   floatArg(args, "threshold", 0),
	}
	if exp, ok := args["expires_at"].(string); ok && exp != "" {
		// Validate RFC3339; if it parses, store it verbatim.
		if _, err := time.Parse(time.RFC3339, exp); err == nil {
			a2.ExpiresAt = exp
		}
	}
	id, err := dbInsertAlert(ctx.AppDB(), pid, a2)
	if err != nil { return nil, err }
	return map[string]any{"alert_id": id}, nil
}

func (a *App) toolPortfolioPause(ctx *sdk.AppCtx, args map[string]any) (any, error) {
	pid, err := resolveProjectFromArgs(args)
	if err != nil { return nil, err }
	id := int64Arg(args, "portfolio_id", 0)
	reason := strArg(args, "reason")
	if reason == "" { return nil, errors.New("reason required") }
	pf, err := dbGetPortfolio(ctx.AppDB(), pid, id)
	if err != nil { return nil, fmt.Errorf("portfolio %d not found", id) }
	if err := dbSetPortfolioStatus(ctx.AppDB(), pf.ID, "paused"); err != nil {
		return nil, err
	}
	emit("portfolio.status.changed", map[string]any{
		"id": pf.ID, "status": "paused", "reason": reason,
	})
	if entryID, jerr := dbInsertJournal(ctx.AppDB(), pid, pf.ID, "alert",
		"Portfolio paused by agent. Reason: "+reason,
		map[string]any{"action": "pause", "reason": reason}); jerr == nil {
		emit("journal.appended", map[string]any{
			"id": entryID, "portfolio_id": pf.ID, "kind": "alert",
			"body": "Portfolio paused by agent. Reason: " + reason,
		})
	}
	return map[string]any{"status": "paused"}, nil
}

// ─── Helpers ───────────────────────────────────────────────────────

func rejectStruct(code, detail string) map[string]any {
	return map[string]any{"status": "rejected", "code": code, "detail": detail}
}

func contains(xs []string, x string) bool {
	for _, y := range xs {
		if y == x { return true }
	}
	return false
}
