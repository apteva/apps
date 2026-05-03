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
		{Name: "portfolio_create", Description: "Create a new portfolio. Args: name, mandate, allowed_classes, starting_cash (paper only — live pulls from broker), mode (paper|live; default paper). Live requires a broker integration bound to the install.",
			InputSchema: schemaObject(map[string]any{
				"name":            map[string]any{"type": "string"},
				"mandate":         map[string]any{"type": "string"},
				"allowed_classes": map[string]any{"type": "array"},
				"starting_cash":   map[string]any{"type": "number"},
				"mode":            map[string]any{"type": "string", "enum": []string{"paper", "live"}},
			}, []string{"name"}),
			Handler: a.toolPortfolioCreate},

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
		bound := ctx.IntegrationFor("broker")
		if bound == nil {
			return rejectStruct("broker_unbound",
				"mode=live requires a broker connection bound to the 'broker' role; bind one in app settings or use mode=paper"), nil
		}
		// Live portfolios are crypto-only on the Binance adapter.
		// Reject mandates that try to mix in equity/etf/polymarket.
		for _, c := range classes {
			if c != "crypto" {
				return rejectStruct("invalid_args",
					fmt.Sprintf("live mode (binance adapter) supports allowed_classes=[crypto] only; got %v", classes)), nil
			}
		}
		acct, summaryErr := callBrokerAccount(ctx, bound)
		if summaryErr != nil {
			return rejectStruct("broker_error",
				"could not read broker account: "+summaryErr.Error()), nil
		}
		cash := acct.QuoteCash // USDT free
		id, err := dbCreatePortfolio(ctx.AppDB(), &Portfolio{
			ProjectID: pid, Name: name, Mandate: mandate,
			AllowedClasses: classes, StartingCash: cash, Mode: "live",
		})
		if err != nil { return nil, err }

		// Seed positions from the broker's holdings — anything non-zero
		// becomes a position with source=broker_reconcile semantics.
		// avg_cost is unknown (broker doesn't ship it); we mark at the
		// current price the engine knows about, falling back to 0.
		seeded := 0
		for asset, bal := range acct.Holdings {
			sym := asset + "-USD"
			price := 0.0
			if mark, _ := dbGetMark(ctx.AppDB(), sym); mark != nil {
				price = mark.Price
			}
			_ = dbInsertPositionRaw(ctx.AppDB(), pid, id, sym, "crypto", "", bal.Free, price)
			seeded++
		}
		if seeded > 0 {
			body := fmt.Sprintf("Seeded %d position(s) from broker holdings on portfolio create. avg_cost is unknown — reconciler tracks future trades from here.", seeded)
			if entryID, jerr := dbInsertJournal(ctx.AppDB(), pid, id, "note", body, map[string]any{
				"source": "broker_reconcile", "kind": "create_seed", "broker_connection_id": bound.ConnectionID,
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
			"broker_connection_id": bound.ConnectionID,
		})
		return map[string]any{
			"portfolio_id":  id,
			"name":          name,
			"starting_cash": cash,
			"mode":          "live",
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

// callBrokerAccount — wraps ExecuteIntegrationTool + parseBinanceAccount.
// Lives here (not binance.go) because future brokers will switch on
// bound.AppSlug; the parse step itself is delegated to the adapter.
func callBrokerAccount(ctx *sdk.AppCtx, bound *sdk.BoundIntegration) (*brokerAccount, error) {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(
		bound.ConnectionID, bound.ToolFor("account.summary"), map[string]any{},
	)
	if err != nil {
		return nil, err
	}
	if res == nil || !res.Success {
		code, detail := brokerErrText(res, nil)
		return nil, fmt.Errorf("%s: %s", code, detail)
	}
	switch bound.AppSlug {
	case "binance-trading", "":
		return parseBinanceAccount(res.Data)
	default:
		return nil, fmt.Errorf("broker %q not supported in v0.2 (binance only)", bound.AppSlug)
	}
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
		bound := ctx.IntegrationFor("broker")
		if bound == nil {
			// Unbound mid-flight (operator unbound after portfolio create).
			// Local order is rolled to rejected; agent gets a clear code.
			_ = dbRejectOrder(ctx.AppDB(), o.ID, "broker_unbound",
				"portfolio is mode=live but no broker is bound; rebind to resume")
			emit("order.rejected", map[string]any{
				"order_id": o.ID, "portfolio_id": pf.ID,
				"code": "broker_unbound", "detail": "broker integration is not bound",
			})
			return rejectStruct("broker_unbound",
				"portfolio is mode=live but no broker is bound"), nil
		}
		// Polymarket / equity / etf are not supported on the Binance adapter.
		// Pre-trade caught this via allowed_classes, but be explicit.
		if class != "crypto" {
			_ = dbRejectOrder(ctx.AppDB(), o.ID, "broker_unsupported_class",
				fmt.Sprintf("Binance adapter handles crypto only, got %q", class))
			emit("order.rejected", map[string]any{"order_id": o.ID, "code": "broker_unsupported_class"})
			return rejectStruct("broker_unsupported_class",
				fmt.Sprintf("Binance adapter handles crypto only, got %q", class)), nil
		}
		brokerArgs, terr := translateOrder(o)
		if terr != nil {
			_ = dbRejectOrder(ctx.AppDB(), o.ID, "translate_failed", terr.Error())
			emit("order.rejected", map[string]any{"order_id": o.ID, "code": "translate_failed", "detail": terr.Error()})
			return rejectStruct("translate_failed", terr.Error()), nil
		}
		res, callErr := ctx.PlatformAPI().ExecuteIntegrationTool(
			bound.ConnectionID, bound.ToolFor("order.place"), brokerArgs,
		)
		if callErr != nil || res == nil || !res.Success {
			code, detail := brokerErrText(res, callErr)
			_ = dbRejectOrder(ctx.AppDB(), o.ID, code, detail)
			emit("order.rejected", map[string]any{
				"order_id": o.ID, "portfolio_id": pf.ID,
				"code": code, "detail": detail,
			})
			return rejectStruct(code, detail), nil
		}
		br, perr := parseBinanceOrder(res.Data)
		if perr != nil {
			_ = dbRejectOrder(ctx.AppDB(), o.ID, "broker_parse_failed", perr.Error())
			emit("order.rejected", map[string]any{"order_id": o.ID, "code": "broker_parse_failed", "detail": perr.Error()})
			return rejectStruct("broker_parse_failed", perr.Error()), nil
		}

		// Persist broker linkage in the rationale journal row before any
		// fill row lands, so an audit can join order → broker_order_id by
		// kind='rationale' alone.
		if entryID, jerr := dbInsertJournal(ctx.AppDB(), pid, pf.ID, "rationale", rationale, map[string]any{
			"order_id": o.ID, "symbol": symbol, "side": side, "qty": qty, "type": otype,
			"broker_connection_id": bound.ConnectionID,
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
			"broker_order_id":  br.BrokerOrderID,
		})

		// Apply any inline fills (Binance returns these synchronously for
		// market orders) + reflect terminal status.
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
		bound := ctx.IntegrationFor("broker")
		if bound == nil {
			return rejectStruct("broker_unbound", "live cancel needs broker bound"), nil
		}
		brokerOrderID, _ := dbBrokerOrderIDFor(ctx.AppDB(), id)
		if brokerOrderID == "" {
			// No broker id stored — order was placed pre-binding or the
			// rationale journal is missing. Flip locally only and warn.
			ctx.Logger().Warn("live order missing broker_order_id; local-cancel only", "order_id", id)
		} else {
			cancelArgs := map[string]any{
				"symbol":            toBinanceSymbol(o.Symbol),
				"origClientOrderId": id, // our newClientOrderId — stable across orderId reuse
			}
			res, cerr := ctx.PlatformAPI().ExecuteIntegrationTool(
				bound.ConnectionID, bound.ToolFor("order.cancel"), cancelArgs,
			)
			if cerr != nil || res == nil || !res.Success {
				code, detail := brokerErrText(res, cerr)
				// Common case: -2011 "Unknown order sent" → already
				// resolved upstream. Treat as success and let the
				// reconciler reflect actual state.
				if !strings.Contains(detail, "Unknown order") && code != "binance_-2011" {
					return rejectStruct("broker_cancel_failed", code+": "+detail), nil
				}
				ctx.Logger().Info("broker reports order unknown — proceeding with local cancel", "order_id", id, "broker_order_id", brokerOrderID)
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
