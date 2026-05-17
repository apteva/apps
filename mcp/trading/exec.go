package main

// Paper-execution engine. Two periodic loops:
//
//   markTick   — refresh marks from the Provider, recompute equity,
//                check daily-loss halt, try to fill working orders.
//   alertTick  — re-evaluate every active alert; on match, fire a
//                SendEvent to the bound instances + journal entry.
//
// Both are registered as Workers via main.go's Workers() so the SDK
// supervises them; we don't manage goroutines directly.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// engine bundles everything the tick loops need. Instantiated once
// in OnMount and stashed in a package var so workers can reach it.
type engine struct {
	db       *sql.DB
	provider Provider
	logger   sdk.Logger
	platform sdk.PlatformClient

	// Metrics surfaced via /healthz/details. Used by tests too.
	mu                   sync.Mutex
	lastTickAt           time.Time
	ticks                int64
	fillsThisRun         int64
	lastWorkingSeen      int64
	lastFillsThisTick    int64
	lastMarksRefreshed   int64

	// significantMarkDeltas state — the last price emitted per symbol,
	// so we send only meaningful changes on the `tick` event. Separate
	// mutex from `mu` to avoid stalling tick metrics while the
	// emit-payload computation runs.
	deltaMu     sync.Mutex
	lastEmitted map[string]float64
}

func (e *engine) snapshotMetrics() map[string]any {
	e.mu.Lock()
	defer e.mu.Unlock()
	return map[string]any{
		"last_tick_at":         e.lastTickAt,
		"ticks":                e.ticks,
		"fills_this_run":       e.fillsThisRun,
		"last_working_seen":    e.lastWorkingSeen,
		"last_fills_this_tick": e.lastFillsThisTick,
		"last_marks_refreshed": e.lastMarksRefreshed,
	}
}

var globalEngine *engine

const (
	slippageBps      = 1.0     // 1 bp default; sells fill below mark, buys above
	feePerOrder      = 0.0     // paper — no fees in v0.1
	defaultLossHalt  = -5.0    // %
	priceTolerance   = 1e-9
)

// markTick — runs every tick_seconds. Refreshes marks, then attempts
// fills against the new marks, then evaluates daily-loss halts. One
// pass; deterministic.
func markTick(ctx context.Context, app *sdk.AppCtx) error {
	e := globalEngine
	if e == nil {
		return errors.New("engine not initialised")
	}
	tickStart := time.Now().UTC()

	// 1. Refresh marks. One transaction per tick so we hold the
	// writer lock once instead of N times. A single bad row gets
	// logged but does NOT poison the whole batch — we keep going
	// and commit the rows that did succeed. This is the difference
	// between "one weird symbol stalls the engine" and "engine
	// ticks reliably regardless of provider hiccups".
	marks := e.provider.Universe()
	marksOK := 0
	if tx, err := e.db.Begin(); err == nil {
		for _, m := range marks {
			if _, err := tx.Exec(`
				INSERT INTO marks (symbol, asset_class, price, no_price, prev_close, volume_24h, marked_at)
				VALUES (?, ?, ?, ?, ?, ?, ?)
				ON CONFLICT(symbol) DO UPDATE SET
					asset_class = excluded.asset_class,
					price       = excluded.price,
					no_price    = excluded.no_price,
					prev_close  = excluded.prev_close,
					volume_24h  = excluded.volume_24h,
					marked_at   = excluded.marked_at`,
				m.Symbol, m.AssetClass, m.Price, nullable(m.NoPrice), nullable(m.PrevClose),
				nullable(m.Volume24h), m.MarkedAt); err != nil {
				e.logger.Warn("upsert mark failed", "symbol", m.Symbol, "err", err)
				continue
			}
			marksOK++
		}
		if err := tx.Commit(); err != nil {
			e.logger.Warn("mark batch commit failed", "err", err)
			marksOK = 0
		}
	} else {
		e.logger.Warn("mark batch begin failed", "err", err)
	}

	// 2. Working orders — dispatch per portfolio mode.
	//    paper → in-process tryFill against the marks we just refreshed.
	//    live  → tryReconcile polls the broker for state and mirrors locally.
	working, err := dbWorkingOrders(e.db)
	if err != nil {
		e.logger.Warn("query working orders failed", "err", err)
		return nil
	}
	fillsThisTick := 0
	for _, o := range working {
		pf, perr := dbGetPortfolioAnyProject(e.db, o.PortfolioID)
		if perr != nil {
			e.logger.Warn("portfolio lookup failed for order", "order_id", o.ID, "err", perr)
			continue
		}
		switch pf.Mode {
		case "live":
			if err := tryReconcile(e, pf, o); err != nil {
				e.logger.Warn("reconcile failed", "order_id", o.ID, "err", err)
			}
		default: // "paper" | ""
			if err := tryFill(e, o); err != nil {
				e.logger.Warn("fill attempt failed", "order_id", o.ID, "err", err)
				continue
			}
		}
	}
	fillsThisTick = e.takeFillCounter()

	// 2.5 Periodic account reconcile for live portfolios — every 12 ticks
	//     (60s @ default 5s tick). Catches cash drift + positions placed
	//     outside our app (broker UI, mobile) so the agent doesn't reason
	//     on stale numbers.
	if e.ticks > 0 && e.ticks%12 == 0 {
		reconcileLiveAccounts(e)
	}

	// 3. Daily-loss halt sweep.
	pfs, err := dbAllPortfolios(e.db)
	if err != nil {
		return nil
	}
	for _, p := range pfs {
		if p.Status == "halted" {
			continue
		}
		eq, err := computeEquity(e.db, p)
		if err != nil {
			continue
		}
		day := utcDay(time.Now())
		baseline, ok, _ := dbGetDayBaseline(e.db, p.ID, day)
		if !ok {
			_ = dbSetDayBaseline(e.db, p.ID, day, eq)
			continue
		}
		if baseline <= 0 {
			continue
		}
		dayPctMove := (eq - baseline) / baseline * 100
		halt := portfolioLossHaltPct(p)
		if dayPctMove < -halt {
			// For live portfolios, cancel working broker orders BEFORE
			// flipping status — turns the halt from a paper concept into
			// a real circuit-breaker. Best-effort: failures don't stall
			// the local status flip (the next reconcile tick catches it).
			if p.Mode == "live" {
				cancelLiveWorkingOrders(e, p, "daily_loss_halt")
			}
			_ = dbSetPortfolioStatus(e.db, p.ID, "halted")
			emit("portfolio.status.changed", map[string]any{
				"id": p.ID, "status": "halted", "reason": "daily_loss_halt",
				"day_pct": dayPctMove, "threshold": -halt, "mode": p.Mode,
			})
			body := fmt.Sprintf("Daily-loss halt fired — equity %.2f vs baseline %.2f (%.2f%%, threshold -%.1f%%).",
				eq, baseline, dayPctMove, halt)
			if entryID, jerr := dbInsertJournal(e.db, p.ProjectID, p.ID, "alert",
				body,
				map[string]any{"rule": "daily_loss_halt", "day_pct": dayPctMove, "threshold": -halt}); jerr == nil {
				emit("journal.appended", map[string]any{
					"id": entryID, "portfolio_id": p.ID, "kind": "alert", "body": body,
				})
			}
			notifyInstances(e, p, fmt.Sprintf("HALT %s — daily-loss halt fired (%.2f%%).", p.Name, dayPctMove))
		}
	}

	// 4. Record tick metrics + emit one-line summary so a sidecar log
	// tail tells you whether the engine is actually working.
	e.mu.Lock()
	e.lastTickAt = tickStart
	e.ticks++
	e.lastWorkingSeen = int64(len(working))
	e.lastFillsThisTick = int64(fillsThisTick)
	e.lastMarksRefreshed = int64(marksOK)
	tickN := e.ticks
	e.mu.Unlock()
	e.logger.Info("tick",
		"n", tickN, "marks", marksOK, "working", len(working),
		"fills_this_tick", fillsThisTick, "fills_total", e.fillsThisRun)

	// 5. App-event: one slim payload per tick. Carries the providers
	// snapshot (UI's data-source pill reads this) + a marks delta the
	// desk can apply directly to its in-memory universe without
	// re-fetching /universe. Empty marks list still emits — it's a
	// heartbeat the UI uses to confirm liveness.
	delta := significantMarkDeltas(e, marks)
	emit("tick", map[string]any{
		"n":              tickN,
		"providers":      providerHealthSnapshot(),
		"marks":          delta,
		"working":        len(working),
		"fills_this_tick": fillsThisTick,
	})
	return nil
}

// significantMarkDeltas filters the universe to symbols whose mark
// moved enough to bother sending. Threshold per asset class:
//   crypto/equity/etf — 0.1% relative move
//   polymarket        — 0.5 cent (0.005) absolute move on YES
// On the very first tick (no last-emitted yet) we send everything so
// fresh subscribers don't have to wait for movement.
func significantMarkDeltas(e *engine, marks []*Mark) []*Mark {
	e.deltaMu.Lock()
	defer e.deltaMu.Unlock()
	if e.lastEmitted == nil {
		e.lastEmitted = map[string]float64{}
	}
	out := make([]*Mark, 0, len(marks))
	first := len(e.lastEmitted) == 0
	for _, m := range marks {
		prev, ok := e.lastEmitted[m.Symbol]
		send := first || !ok
		if !send {
			if m.AssetClass == "polymarket" {
				if abs(m.Price-prev) >= 0.005 {
					send = true
				}
			} else if prev > 0 && abs((m.Price-prev)/prev) >= 0.001 {
				send = true
			}
		}
		if send {
			out = append(out, m)
			e.lastEmitted[m.Symbol] = m.Price
		}
	}
	return out
}

func abs(x float64) float64 {
	if x < 0 {
		return -x
	}
	return x
}

// takeFillCounter — atomically returns how many fills happened since
// last call. tryFill increments fillsThisRun + a per-tick counter.
var fillsThisTickCounter int64

func (e *engine) bumpFillCounter() {
	e.mu.Lock()
	e.fillsThisRun++
	fillsThisTickCounter++
	e.mu.Unlock()
}

func (e *engine) takeFillCounter() int {
	e.mu.Lock()
	n := fillsThisTickCounter
	fillsThisTickCounter = 0
	e.mu.Unlock()
	return int(n)
}

// portfolioLossHaltPct — pulls per-portfolio override from config_json
// or falls back to the install-wide default from APTEVA_APP_CONFIG.
func portfolioLossHaltPct(p *Portfolio) float64 {
	// Per-portfolio override (config_json column) — TODO when we expose it.
	cfgRaw := globalCtx.Config().Get("daily_loss_halt_pct")
	if cfgRaw != "" {
		if v, err := strconv.ParseFloat(cfgRaw, 64); err == nil {
			return v
		}
	}
	return -defaultLossHalt
}

// tryFill — given a working order and the fresh marks, decide whether
// to fill. Single-pass, single-tick.
func tryFill(e *engine, o *Order) error {
	mark, err := dbGetMark(e.db, o.Symbol)
	if err != nil {
		return nil // no mark yet — skip
	}
	pf, err := dbGetPortfolioAnyProject(e.db, o.PortfolioID)
	if err != nil {
		return err
	}
	if pf.Status != "active" {
		// Working orders on a paused/halted portfolio just sit. They'll
		// resume when the portfolio is resumed; or be cancelled by the operator.
		return nil
	}

	// Mark used for the rule decision: YES vs NO for polymarket.
	outcome := strings.ToUpper(o.Side)
	mp := mark.Price
	if o.AssetClass == "polymarket" {
		if outcome == "NO" && mark.NoPrice != nil {
			mp = *mark.NoPrice
		}
	}

	// Decide fill price by order type.
	var fillPrice float64
	switch o.Type {
	case "market":
		fillPrice = applySlippage(mp, o.Side)
	case "limit":
		if o.LimitPrice == nil {
			return nil
		}
		ok := false
		switch o.Side {
		case "buy":
			ok = mp <= *o.LimitPrice+priceTolerance
		case "sell":
			ok = mp >= *o.LimitPrice-priceTolerance
		case "yes", "no":
			// polymarket — buyer of YES/NO is willing to pay at most limit
			ok = mp <= *o.LimitPrice+priceTolerance
		}
		if !ok {
			return nil
		}
		fillPrice = mp
	case "stop":
		if o.StopPrice == nil {
			return nil
		}
		// Stop fires when mark crosses; turns into market.
		ok := false
		switch o.Side {
		case "buy":  ok = mp >= *o.StopPrice
		case "sell": ok = mp <= *o.StopPrice
		default:     return nil // no stops on polymarket in v0.1
		}
		if !ok {
			return nil
		}
		fillPrice = applySlippage(mp, o.Side)
	default:
		return nil
	}

	// Validate post-trade against cash (buys) / position (sells).
	if isBuySide(o.Side) {
		needed := o.Qty * fillPrice
		if pf.Cash < needed-1e-6 {
			detail := fmt.Sprintf("need %.2f, have %.2f", needed, pf.Cash)
			_ = dbRejectOrder(e.db, o.ID, "insufficient_cash", detail)
			emit("order.rejected", map[string]any{
				"order_id": o.ID, "portfolio_id": pf.ID,
				"code": "insufficient_cash", "detail": detail,
			})
			notifyInstances(e, pf, fmt.Sprintf("REJECTED %s — insufficient cash for %s", o.ID, o.Symbol))
			return nil
		}
	} else {
		current, _ := dbGetPosition(e.db, pf.ID, o.Symbol, "")
		if current == nil || current.Qty < o.Qty-1e-9 {
			have := 0.0
			if current != nil {
				have = current.Qty
			}
			detail := fmt.Sprintf("need %v, have %v", o.Qty, have)
			_ = dbRejectOrder(e.db, o.ID, "insufficient_position", detail)
			emit("order.rejected", map[string]any{
				"order_id": o.ID, "portfolio_id": pf.ID,
				"code": "insufficient_position", "detail": detail,
			})
			notifyInstances(e, pf, fmt.Sprintf("REJECTED %s — insufficient position for %s sell", o.ID, o.Symbol))
			return nil
		}
	}

	// Apply fill atomically.
	tx, err := e.db.Begin()
	if err != nil {
		return err
	}
	if err := dbInsertFill(tx, pf.ProjectID, o.ID, pf.ID, o.Qty, fillPrice, feePerOrder); err != nil {
		_ = tx.Rollback(); return err
	}
	if err := dbMarkOrderFilled(tx, o.ID, o.Qty, fillPrice); err != nil {
		_ = tx.Rollback(); return err
	}
	if err := dbApplyFill(tx, pf.ID, pf.ProjectID, o, o.Qty, fillPrice); err != nil {
		_ = tx.Rollback(); return err
	}
	body := fmt.Sprintf("%s %s %v @ %s — %s",
		strings.ToUpper(o.Symbol), strings.ToUpper(o.Side), o.Qty, formatPrice(fillPrice, o.AssetClass), o.ID)
	if err := dbInsertJournalTx(tx, pf.ProjectID, pf.ID, "fill", body, map[string]any{
		"order_id": o.ID, "qty": o.Qty, "price": fillPrice, "side": o.Side, "symbol": o.Symbol,
	}); err != nil {
		_ = tx.Rollback(); return err
	}
	if err := tx.Commit(); err != nil {
		return err
	}
	e.bumpFillCounter()

	// App-events: one fill = three logical things changed (the order
	// resolved, a position mutated, the journal got a row). Emit each
	// so UI subscribers can do narrow cache-invalidation rather than
	// re-fetching the whole portfolio.
	emit("order.filled", map[string]any{
		"order_id": o.ID, "portfolio_id": pf.ID, "symbol": o.Symbol,
		"side": o.Side, "qty": o.Qty, "price": fillPrice,
	})
	if newPos, _ := dbGetPosition(e.db, pf.ID, o.Symbol, polyOutcome(o)); newPos != nil {
		emit("position.changed", map[string]any{
			"portfolio_id": pf.ID, "symbol": newPos.Symbol,
			"asset_class":  newPos.AssetClass, "outcome": newPos.Outcome,
			"qty":          newPos.Qty, "avg_cost": newPos.AvgCost,
			"realized_pnl": newPos.RealizedPnL,
		})
	} else {
		// Position closed entirely (sell flat). Surface it explicitly.
		emit("position.changed", map[string]any{
			"portfolio_id": pf.ID, "symbol": o.Symbol, "qty": 0.0, "closed": true,
		})
	}
	emit("journal.appended", map[string]any{
		"portfolio_id": pf.ID, "kind": "fill", "body": body,
	})
	notifyInstances(e, pf, "FILL "+body)
	return nil
}

// polyOutcome — small helper so dbGetPosition can find a polymarket
// position via its YES/NO leg. Empty string for non-poly orders.
func polyOutcome(o *Order) string {
	if o.AssetClass == "polymarket" {
		return strings.ToUpper(o.Side)
	}
	return ""
}

// applySlippage — sells fill below mark, buys above. The trader always
// pays the spread.
func applySlippage(mark float64, side string) float64 {
	bp := slippageBps / 10_000.0
	switch side {
	case "buy", "yes", "no":
		return mark + mark*bp
	case "sell":
		return mark - mark*bp
	}
	return mark
}

func isBuySide(side string) bool {
	return side == "buy" || side == "yes" || side == "no"
}

func formatPrice(p float64, class string) string {
	if class == "polymarket" {
		return fmt.Sprintf("%.2f¢", p*100)
	}
	return fmt.Sprintf("$%.2f", p)
}

// notifyInstances fans a short text message to every Apteva instance
// bound to this portfolio. Best-effort — failures are logged but don't
// break the engine.
func notifyInstances(e *engine, p *Portfolio, msg string) {
	rows, err := e.db.Query(`SELECT instance_id FROM portfolio_bindings WHERE portfolio_id = ?`, p.ID)
	if err != nil {
		return
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if rows.Scan(&id) == nil {
			ids = append(ids, id)
		}
	}
	for _, id := range ids {
		if err := e.platform.SendEvent(id, msg); err != nil {
			e.logger.Warn("send_event failed", "instance_id", id, "err", err)
		}
	}
}

// ─── Alert engine ──────────────────────────────────────────────────

func alertTick(ctx context.Context, app *sdk.AppCtx) error {
	e := globalEngine
	if e == nil {
		return nil
	}
	alerts, err := dbActiveAlerts(e.db)
	if err != nil {
		return nil
	}
	for _, a := range alerts {
		if a.ExpiresAt != "" {
			if t, err := time.Parse(time.RFC3339, a.ExpiresAt); err == nil && time.Now().After(t) {
				_, _ = e.db.Exec(`UPDATE alerts SET status='expired' WHERE id = ?`, a.ID)
				continue
			}
		}
		match, value := evaluateAlert(e, a)
		if !match {
			continue
		}
		_ = dbFireAlert(e.db, a.ID)
		pf, _ := dbGetPortfolioAnyProject(e.db, a.PortfolioID)
		if pf == nil {
			continue
		}
		body := fmt.Sprintf("ALERT %s — %s %s threshold (%v ↔ %v)",
			a.Symbol, a.Rule, "matched", value, a.Threshold)
		emit("alert.fired", map[string]any{
			"alert_id": a.ID, "portfolio_id": pf.ID,
			"symbol": a.Symbol, "rule": a.Rule,
			"threshold": a.Threshold, "value": value,
		})
		if entryID, jerr := dbInsertJournal(e.db, pf.ProjectID, pf.ID, "alert", body, map[string]any{
			"alert_id": a.ID, "rule": a.Rule, "threshold": a.Threshold, "value": value, "symbol": a.Symbol,
		}); jerr == nil {
			emit("journal.appended", map[string]any{
				"id": entryID, "portfolio_id": pf.ID, "kind": "alert", "body": body,
			})
		}
		notifyInstances(e, pf, body)
	}
	return nil
}

func evaluateAlert(e *engine, a *Alert) (bool, float64) {
	switch a.Rule {
	case "mark_above", "mark_below", "yes_above", "yes_below":
		mark, err := dbGetMark(e.db, a.Symbol)
		if err != nil {
			return false, 0
		}
		mp := mark.Price
		if a.Rule == "yes_above" || a.Rule == "yes_below" {
			// 'yes' rules already use mark.Price (which is YES probability for polymarkets)
		}
		switch a.Rule {
		case "mark_above", "yes_above":
			return mp > a.Threshold, mp
		case "mark_below", "yes_below":
			return mp < a.Threshold, mp
		}
	case "day_pnl_below":
		pf, err := dbGetPortfolioAnyProject(e.db, a.PortfolioID)
		if err != nil {
			return false, 0
		}
		eq, _ := computeEquity(e.db, pf)
		baseline, ok, _ := dbGetDayBaseline(e.db, pf.ID, utcDay(time.Now()))
		if !ok || baseline <= 0 {
			return false, 0
		}
		pct := (eq - baseline) / baseline * 100
		return pct < a.Threshold, pct
	}
	return false, 0
}

// ─── Live broker integration ──────────────────────────────────────
//
// tryReconcile: poll the broker for a working live order's current
// state and mirror progress (fills, status flips) into local tables.
// Soft-fails on transient broker errors — the order stays working and
// the next tick retries. The agent doesn't see flapping.
//
// All three callers (this, the inline-fill path in toolOrderPlace, and
// halt-cancels) go through applyBrokerProgress for the "what changed,
// what to write, what to emit" rules. One source of truth.

func tryReconcile(e *engine, pf *Portfolio, o *Order) error {
	if globalCtx == nil {
		return errors.New("no app ctx — engine not fully mounted")
	}
	bb, ferr := brokerFor(globalCtx, pf)
	if ferr != nil {
		// Operator unbound the broker (or the slug isn't registered).
		// Don't reject — the order may resume when rebound. Log once per
		// tick. The agent sees the order stay 'working' which is the
		// truthful state given we can't poll.
		e.logger.Warn("live order has no broker bound; staying working",
			"order_id", o.ID, "broker_slug", pf.BrokerSlug, "err", ferr)
		return nil
	}
	brokerOrderID, _ := dbBrokerOrderIDFor(e.db, o.ID)
	args := bb.Adapter.StatusArgs(o, brokerOrderID)
	res, err := globalCtx.PlatformAPI().ExecuteIntegrationTool(
		bb.ConnectionID, bb.toolFor("order.status"), args,
	)
	if err != nil {
		// Transient — retry next tick.
		return err
	}
	if res == nil || !res.Success {
		code, detail := bb.Adapter.ErrText(res, nil)
		// Broker confirms the order doesn't exist on its side — likely
		// the placement itself failed silently. Reject locally so the
		// agent stops waiting on a phantom.
		if bb.Adapter.IsUnknownOrderError(code, detail) {
			_ = dbRejectOrder(e.db, o.ID, "broker_unknown_order",
				"broker reports order does not exist; treating as failed-to-place")
			emit("order.rejected", map[string]any{
				"order_id": o.ID, "code": "broker_unknown_order", "detail": detail,
			})
			return nil
		}
		return fmt.Errorf("broker get_order: %s: %s", code, detail)
	}
	br, perr := bb.Adapter.ParseOrder(res.Data)
	if perr != nil {
		return perr
	}
	if _, aerr := applyBrokerProgress(e.db, pf.ProjectID, pf, o, br); aerr != nil {
		return aerr
	}
	return nil
}

// applyBrokerProgress mirrors a parsed broker response into local
// tables. Used by:
//   - toolOrderPlace (inline-fill path) when create_order returns FILLED
//     synchronously with a fills array.
//   - tryReconcile (every tick) when a polled get_order shows progress.
//   - cancelLiveWorkingOrders (halt path) when the broker confirms
//     a cancel.
//
// Mutates `o` to reflect new filled_qty / avg_fill_price / status so
// the caller can return current state without re-reading. Emits all
// the same SSE events as the paper engine — UI stays mode-agnostic.
//
// Returns (changed, error). changed=true means at least one of fills
// or status flipped; callers can use this to decide whether to bump
// metrics.
func applyBrokerProgress(db *sql.DB, projectID string, pf *Portfolio, o *Order, br *brokerOrderResult) (bool, error) {
	deltaQty := br.ExecutedQty - o.FilledQty
	changed := false

	if deltaQty > 1e-9 {
		// VWAP for the new fill chunk. Prefer the synchronous fills
		// array (create_order with newOrderRespType=FULL); fall back to
		// whole-order VWAP via cumulative quote qty (polled get_order
		// doesn't carry per-fill detail).
		var deltaPrice, fee float64
		if len(br.Fills) > 0 {
			var qSum, pvSum float64
			for _, f := range br.Fills {
				qSum += f.Qty
				pvSum += f.Qty * f.Price
				fee += f.Commission
			}
			if qSum > 0 {
				deltaPrice = pvSum / qSum
			}
		}
		if deltaPrice == 0 && br.CummulativeQuoteQty > 0 && br.ExecutedQty > 0 {
			deltaPrice = br.CummulativeQuoteQty / br.ExecutedQty
		}
		if deltaPrice <= 0 {
			return false, fmt.Errorf("cannot resolve fill price for order %s (executed_qty=%v, fills=%d)",
				o.ID, br.ExecutedQty, len(br.Fills))
		}

		tx, err := db.Begin()
		if err != nil {
			return false, err
		}
		// CAS guard: claim the delta by updating filled_qty conditioned on
		// the value we read into `o`. Two overlapping ticks (or an
		// order_place inline-apply racing with a tryReconcile) can both
		// observe the same stale FilledQty and otherwise both apply the
		// same delta. The conditional UPDATE serializes them: the second
		// arrival sees rows_affected = 0 and bails before touching fills,
		// positions, or cash.
		cumAvg := deltaPrice
		if br.CummulativeQuoteQty > 0 && br.ExecutedQty > 0 {
			cumAvg = br.CummulativeQuoteQty / br.ExecutedQty
		}
		casRes, err := tx.Exec(`UPDATE orders SET filled_qty = ?, avg_fill_price = ?
			WHERE id = ? AND ABS(filled_qty - ?) < 1e-9`,
			br.ExecutedQty, cumAvg, o.ID, o.FilledQty)
		if err != nil {
			_ = tx.Rollback()
			return false, err
		}
		if n, _ := casRes.RowsAffected(); n == 0 {
			_ = tx.Rollback()
			return false, nil // another tick already applied this fill
		}
		if err := dbInsertFill(tx, projectID, o.ID, pf.ID, deltaQty, deltaPrice, fee); err != nil {
			_ = tx.Rollback()
			return false, err
		}
		if err := dbApplyFill(tx, pf.ID, projectID, o, deltaQty, deltaPrice); err != nil {
			_ = tx.Rollback()
			return false, err
		}
		body := fmt.Sprintf("%s %s %v @ %s — %s (broker %s)",
			strings.ToUpper(o.Symbol), strings.ToUpper(o.Side), deltaQty,
			formatPrice(deltaPrice, o.AssetClass), o.ID, br.BrokerOrderID)
		if err := dbInsertJournalTx(tx, projectID, pf.ID, "fill", body, map[string]any{
			"order_id": o.ID, "qty": deltaQty, "price": deltaPrice, "fee": fee,
			"side": o.Side, "symbol": o.Symbol,
			"source": "broker", "broker_order_id": br.BrokerOrderID,
		}); err != nil {
			_ = tx.Rollback()
			return false, err
		}
		if err := tx.Commit(); err != nil {
			return false, err
		}

		o.FilledQty = br.ExecutedQty
		o.AvgFillPrice = cumAvg
		changed = true

		emit("order.filled", map[string]any{
			"order_id": o.ID, "portfolio_id": pf.ID, "symbol": o.Symbol,
			"side": o.Side, "qty": deltaQty, "price": deltaPrice,
			"fee": fee, "broker_order_id": br.BrokerOrderID,
		})
		if newPos, _ := dbGetPosition(db, pf.ID, o.Symbol, polyOutcome(o)); newPos != nil {
			emit("position.changed", map[string]any{
				"portfolio_id": pf.ID, "symbol": newPos.Symbol,
				"asset_class": newPos.AssetClass, "qty": newPos.Qty,
				"avg_cost": newPos.AvgCost, "realized_pnl": newPos.RealizedPnL,
			})
		} else {
			emit("position.changed", map[string]any{
				"portfolio_id": pf.ID, "symbol": o.Symbol, "qty": 0.0, "closed": true,
			})
		}
		emit("journal.appended", map[string]any{
			"portfolio_id": pf.ID, "kind": "fill", "body": body,
		})
	}

	// Terminal status flips. Status moves are independent of fills —
	// PARTIALLY_FILLED stays "working", FILLED resolves, CANCELED /
	// REJECTED close out. UPDATEs are conditional on the row still being
	// 'working' so racing reconciles don't re-emit the terminal event.
	switch br.Status {
	case "filled":
		if o.Status != "filled" {
			res, err := db.Exec(`UPDATE orders SET status='filled', resolved_at=CURRENT_TIMESTAMP
				WHERE id = ? AND status = 'working'`, o.ID)
			if err != nil {
				return changed, err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				o.Status = "filled"
				changed = true
			}
		}
	case "cancelled":
		if o.Status != "cancelled" {
			res, err := db.Exec(`UPDATE orders SET status='cancelled', resolved_at=CURRENT_TIMESTAMP, rejection_detail=?
				WHERE id = ? AND status = 'working'`,
				"broker_"+br.BrokerStatus, o.ID)
			if err != nil {
				return changed, err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				o.Status = "cancelled"
				emit("order.cancelled", map[string]any{
					"order_id": o.ID, "broker_status": br.BrokerStatus,
				})
				changed = true
			}
		}
	case "rejected":
		if o.Status != "rejected" {
			res, err := db.Exec(`UPDATE orders SET status='rejected', rejection_code=?, rejection_detail=?,
				resolved_at=CURRENT_TIMESTAMP WHERE id = ? AND status = 'working'`,
				"broker_rejected", br.BrokerStatus, o.ID)
			if err != nil {
				return changed, err
			}
			if n, _ := res.RowsAffected(); n > 0 {
				o.Status = "rejected"
				emit("order.rejected", map[string]any{
					"order_id": o.ID, "code": "broker_rejected", "detail": br.BrokerStatus,
				})
				changed = true
			}
		}
	}

	return changed, nil
}

// reconcileLiveAccounts — periodic sweep that pulls broker account state
// for every live portfolio and reconciles cash + holdings against local.
// Best-effort: errors get logged, the next sweep retries.
//
// Catches:
//   - cash drift (commissions in non-USDT, dust the order path missed)
//   - positions placed outside our app (broker UI / mobile / another bot)
//
// Does NOT modify avg_cost — that's the local cost-basis source of
// truth. New positions discovered here are seeded with avg_cost = current
// mark (best-effort) and journaled with source=broker_reconcile so the
// audit trail is honest about provenance.
func reconcileLiveAccounts(e *engine) {
	if globalCtx == nil {
		return
	}
	pfs, err := dbAllPortfolios(e.db)
	if err != nil {
		return
	}
	for _, p := range pfs {
		if p.Mode != "live" || p.Status == "halted" {
			continue
		}
		bb, ferr := brokerFor(globalCtx, p)
		if ferr != nil {
			// Broker not bound for this portfolio's slug — skip silently;
			// next reconcile after rebind will catch up.
			continue
		}
		// Stamp BEFORE the broker call so we can detect any fill that
		// landed while we were waiting on the response. A snapshot taken
		// pre-fill but applied post-fill would otherwise wipe out the
		// fill's cash debit and re-introduce phantom buying power.
		snapshotBefore := time.Now().UTC()
		res, err := globalCtx.PlatformAPI().ExecuteIntegrationTool(
			bb.ConnectionID, bb.toolFor("account.summary"), map[string]any{},
		)
		if err != nil || res == nil || !res.Success {
			e.logger.Warn("account reconcile failed", "portfolio_id", p.ID, "broker", bb.Adapter.Slug())
			continue
		}
		acct, perr := bb.Adapter.ParseAccount(res.Data)
		if perr != nil {
			e.logger.Warn("account parse failed", "portfolio_id", p.ID, "err", perr)
			continue
		}
		// Adapters with a separate holdings call (Alpaca) — fetch +
		// merge here so the downstream discovery logic sees a single
		// unified acct view.
		if tool := bb.Adapter.HoldingsTool(); tool != "" {
			posRaw, herr := globalCtx.PlatformAPI().ExecuteIntegrationTool(
				bb.ConnectionID, tool, map[string]any{},
			)
			if herr == nil && posRaw != nil && posRaw.Success {
				if holdings, perr2 := bb.Adapter.ParseHoldings(posRaw.Data); perr2 == nil {
					if acct.Holdings == nil {
						acct.Holdings = map[string]brokerBalance{}
					}
					for k, v := range holdings {
						acct.Holdings[k] = v
					}
				}
			}
		}
		// Did anything fill on this portfolio after the snapshot was
		// captured? If yes, the broker's reported balances pre-date local
		// state — skip cash + position writes this round and let the next
		// reconcile (after fills settle) catch up.
		var fillsSince int
		_ = e.db.QueryRow(`SELECT COUNT(*) FROM fills WHERE portfolio_id = ? AND filled_at > ?`,
			p.ID, snapshotBefore.Format("2006-01-02 15:04:05")).Scan(&fillsSince)
		if fillsSince > 0 {
			e.logger.Info("reconcile: skipping write — fill(s) landed during broker snapshot",
				"portfolio_id", p.ID, "fills_since_snapshot", fillsSince)
			continue
		}
		// Cash drift.
		if abs(acct.QuoteCash-p.Cash) > 0.01 {
			delta := acct.QuoteCash - p.Cash
			body := fmt.Sprintf("Cash reconcile: local %.2f → broker %.2f (Δ %+.2f). Likely commission / dust.", p.Cash, acct.QuoteCash, delta)
			_, _ = dbInsertJournal(e.db, p.ProjectID, p.ID, "note", body, map[string]any{
				"source": "broker_reconcile", "kind": "cash_drift",
				"local": p.Cash, "broker": acct.QuoteCash, "delta": delta,
			})
			_, _ = e.db.Exec(`UPDATE portfolios SET cash = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ?`, acct.QuoteCash, p.ID)
		}
		// Position drift — discover-only in v0.2. Reducing positions by
		// reconcile is risky (might reflect an in-flight tx); we leave
		// over-sized local positions alone and surface them via a
		// journal note. New holdings get inserted with avg_cost = 0 so
		// downstream P&L is honest about cost basis being unknown rather
		// than fabricating one from the current mark (which understates
		// realized P&L on subsequent sells and lies on the panel).
		// Also: skip symbols with a working live order so we don't race
		// an in-flight order_place that's about to write the position.
		positions, _ := dbListPositions(e.db, p.ID)
		known := map[string]bool{}
		for _, q := range positions {
			known[strings.ToUpper(q.Symbol)] = true
		}
		workingBySymbol := map[string]bool{}
		if wo, werr := dbListOrders(e.db, p.ID, "working", 200); werr == nil {
			for _, w := range wo {
				workingBySymbol[strings.ToUpper(w.Symbol)] = true
			}
		}
		for canonical, bal := range acct.Holdings {
			key := strings.ToUpper(canonical)
			if known[key] {
				continue
			}
			if workingBySymbol[key] {
				continue
			}
			cls := inferAssetClass(canonical)
			if err := dbInsertPositionRaw(e.db, p.ProjectID, p.ID, canonical, cls, "", bal.Free, 0); err == nil {
				body := fmt.Sprintf("Discovered %v %s on broker (no prior local position). avg_cost = 0 — true cost basis unknown; sells will overstate realized P&L until operator sets it.", bal.Free, canonical)
				_, _ = dbInsertJournal(e.db, p.ProjectID, p.ID, "note", body, map[string]any{
					"source": "broker_reconcile", "kind": "discovered_position",
					"broker_slug": bb.Adapter.Slug(),
					"symbol":      canonical, "qty": bal.Free, "avg_cost": 0,
				})
				emit("position.changed", map[string]any{
					"portfolio_id": p.ID, "symbol": canonical, "qty": bal.Free,
					"avg_cost": 0.0, "discovered": true,
				})
			}
		}
	}
}

// cancelLiveWorkingOrders — invoked from the daily-loss halt sweep.
// Best-effort cancels every working broker order on the halted portfolio
// before status flips. Failures are logged; the next reconcile tick
// will catch any that didn't cancel cleanly.
func cancelLiveWorkingOrders(e *engine, p *Portfolio, reason string) {
	if globalCtx == nil {
		return
	}
	bb, ferr := brokerFor(globalCtx, p)
	if ferr != nil {
		return
	}
	working, err := dbListOrders(e.db, p.ID, "working", 200)
	if err != nil {
		return
	}
	for _, o := range working {
		brokerOrderID, _ := dbBrokerOrderIDFor(e.db, o.ID)
		// Adapters without cancel-by-client-id need the broker order
		// id. If we can't find one (rare — see toolOrderCancel), still
		// flip locally; next reconcile will fix any state drift.
		if brokerOrderID == "" && !bb.Adapter.Capabilities().CancelByClientID {
			e.logger.Warn("halt-cancel: missing broker_order_id; local-only",
				"order_id", o.ID, "broker", bb.Adapter.Slug())
		} else {
			args := bb.Adapter.CancelArgs(o, brokerOrderID)
			_, err := globalCtx.PlatformAPI().ExecuteIntegrationTool(
				bb.ConnectionID, bb.toolFor("order.cancel"), args,
			)
			if err != nil {
				e.logger.Warn("halt-cancel broker call failed",
					"order_id", o.ID, "broker", bb.Adapter.Slug(), "err", err)
				continue
			}
		}
		if _, err := e.db.Exec(`UPDATE orders SET status='cancelled', resolved_at=CURRENT_TIMESTAMP, rejection_detail=? WHERE id = ?`,
			"halt_cancel_"+reason, o.ID); err == nil {
			emit("order.cancelled", map[string]any{
				"order_id": o.ID, "reason": reason, "by": "halt",
			})
		}
	}
}
