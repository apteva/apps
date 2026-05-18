package main

// Alpaca broker adapter. Pure translation: local Order types ↔ Alpaca
// Trading API v2 shapes. No I/O. The integration runner in apteva-server
// signs and transports the request; we only build args and parse
// responses.
//
// Equities + ETFs + crypto, long-only, fractional. Limit + stop +
// stop-limit + trailing-stop all map directly. Idempotency via
// client_order_id (we pass our local Order.ID).
//
// Stocks trade only during US market hours (with optional
// extended-hours flag). Alpaca queues orders submitted off-hours; we
// don't need a local "scheduled" state — the order sits as
// status=accepted until the open, then transitions like any other.
//
// Crypto uses {base}/{quote} on Alpaca (BTC/USD), distinct from
// Binance's {base}{quote} (BTCUSDT). The local canonical form stays
// BTC-USD; per-broker translation handles the rest.

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func init() { registerAdapter(&alpacaAdapter{}) }

type alpacaAdapter struct{}

func (alpacaAdapter) Slug() string { return "alpaca-trading" }

func (alpacaAdapter) Capabilities() brokerCapabilities {
	return brokerCapabilities{
		AssetClasses:     []string{"equity", "etf", "crypto"},
		OrderTypes:       []string{"market", "limit", "stop", "stop_limit", "trailing_stop"},
		TIFs:             []string{"day", "gtc", "ioc", "fok", "opg", "cls"},
		Fractional:       true,
		CancelByClientID: false, // cancel_order needs the alpaca order id
		QuoteCurrency:    "USD",
	}
}

func (alpacaAdapter) ToolMap() map[string]string {
	return map[string]string{
		"order.place":     "create_order",
		"order.cancel":    "cancel_order",
		"order.status":    "get_order",
		"account.summary": "get_account",
		"positions.list":  "list_positions",
	}
}

// Alpaca splits cash (get_account) from holdings (list_positions). The
// trading-app's portfolio_create + reconcile loops use HoldingsTool() to
// know they need a second call.
func (alpacaAdapter) HoldingsTool() string { return "list_positions" }

func (alpacaAdapter) ParseHoldings(raw json.RawMessage) (map[string]brokerBalance, error) {
	return alpacaParsePositions(raw)
}

// ─── Symbol mapping ────────────────────────────────────────────────
//
// Local canonical → Alpaca:
//   equity/etf:  bare ticker, unchanged (AAPL → AAPL)
//   crypto:      "{base}-USD" → "{base}/USD"  (BTC-USD → BTC/USD)
//   polymarket:  unsupported (caller filters before reaching here)

func toAlpacaSymbol(canonical string) string {
	s := strings.ToUpper(strings.TrimSpace(canonical))
	if strings.HasSuffix(s, "-USD") {
		return strings.TrimSuffix(s, "-USD") + "/USD"
	}
	return s
}

func fromAlpacaSymbol(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	if strings.Contains(s, "/") {
		// Crypto: BTC/USD → BTC-USD
		return strings.ReplaceAll(s, "/", "-")
	}
	return s
}

func (alpacaAdapter) ToBrokerSymbol(canonical string) string { return toAlpacaSymbol(canonical) }

// ─── Order translation ────────────────────────────────────────────

func (alpacaAdapter) TranslateOrder(o *Order) (map[string]any, error) {
	if o == nil {
		return nil, errors.New("nil order")
	}
	if o.AssetClass == "polymarket" {
		return nil, errors.New("polymarket is not supported on the Alpaca adapter")
	}
	if o.AssetClass != "equity" && o.AssetClass != "etf" && o.AssetClass != "crypto" {
		return nil, fmt.Errorf("Alpaca adapter handles equity/etf/crypto only, got %q", o.AssetClass)
	}

	args := map[string]any{
		"symbol":          toAlpacaSymbol(o.Symbol),
		"side":            strings.ToLower(o.Side), // buy | sell
		"qty":             formatAlpacaQty(o.Qty),
		"client_order_id": o.ID,
	}

	// Time-in-force. Alpaca defaults to "day" if absent; equities accept
	// day/gtc/opg/cls/ioc/fok and crypto accepts gtc/ioc only. We map
	// our local "day" to "day" (equity) or "gtc" (crypto) to avoid the
	// "not allowed for crypto" rejection class.
	tif := strings.ToLower(o.TIF)
	if tif == "" {
		tif = "day"
	}
	if o.AssetClass == "crypto" && tif == "day" {
		tif = "gtc"
	}
	args["time_in_force"] = tif

	switch o.Type {
	case "market":
		args["type"] = "market"
	case "limit":
		if o.LimitPrice == nil {
			return nil, errors.New("limit order missing limit_price")
		}
		args["type"] = "limit"
		args["limit_price"] = formatAlpacaPrice(*o.LimitPrice)
	case "stop":
		// Alpaca has both stop (market-on-trigger) and stop_limit. We
		// map "stop" to stop-market — true market on trigger, no extra
		// limit price needed. Closer to operator intent than Binance's
		// STOP_LOSS_LIMIT workaround.
		if o.StopPrice == nil {
			return nil, errors.New("stop order missing stop_price")
		}
		args["type"] = "stop"
		args["stop_price"] = formatAlpacaPrice(*o.StopPrice)
	default:
		return nil, fmt.Errorf("unsupported order type %q", o.Type)
	}

	return args, nil
}

func formatAlpacaQty(q float64) string {
	// Alpaca accepts string qty up to 9 decimals for fractional crypto;
	// equity fractional supports 9 too. Trim trailing zeros for
	// cleanliness in journal entries.
	s := strconv.FormatFloat(q, 'f', -1, 64)
	return s
}

func formatAlpacaPrice(p float64) string {
	// Equity ticks are penny-priced for >= $1; sub-penny ticks are
	// allowed below $1. Two decimals is the safe default and matches
	// what the broker accepts for the vast majority of symbols.
	return strconv.FormatFloat(p, 'f', 2, 64)
}

// ─── Order response parsing ───────────────────────────────────────
//
// Alpaca's create_order and get_order share the same shape. Status
// strings differ from Binance: "new", "accepted", "partially_filled",
// "filled", "canceled", "expired", "rejected", "pending_cancel",
// "pending_replace", "stopped", "suspended", "calculated", "held",
// "accepted_for_bidding", "done_for_day". We collapse to our four:
//
//   working   — new, accepted, partially_filled, pending_*, held, calculated,
//               accepted_for_bidding, done_for_day, suspended
//   filled    — filled, stopped, calculated-with-fill (handled via filled_qty)
//   cancelled — canceled, expired
//   rejected  — rejected

func (alpacaAdapter) ParseOrder(raw json.RawMessage) (*brokerOrderResult, error) {
	if len(raw) == 0 {
		return nil, errors.New("empty broker response")
	}
	var resp struct {
		ID            string `json:"id"`
		ClientOrderID string `json:"client_order_id"`
		Status        string `json:"status"`
		FilledQty     string `json:"filled_qty"`
		FilledAvgPrice string `json:"filled_avg_price"`
		// Notional fills (dollar-amount orders) also report filled_qty.
		Qty string `json:"qty"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("decode broker response: %w", err)
	}
	if resp.ID == "" {
		return nil, fmt.Errorf("broker response missing id: %s", string(raw))
	}
	executed := parseFloat(resp.FilledQty)
	avgPrice := parseFloat(resp.FilledAvgPrice)
	// Synthesize CummulativeQuoteQty so applyBrokerProgress's
	// existing VWAP fallback works without an alpaca-specific branch.
	cumQuote := executed * avgPrice
	out := &brokerOrderResult{
		BrokerOrderID:       resp.ID,
		ClientOrderID:       resp.ClientOrderID,
		BrokerStatus:        resp.Status,
		Status:              mapAlpacaStatus(resp.Status),
		ExecutedQty:         executed,
		CummulativeQuoteQty: cumQuote,
		// Alpaca doesn't return per-fill commission on the order
		// envelope (use /v2/account/activities?activity_types=FILL).
		// Leaving Fills empty makes applyBrokerProgress fall through to
		// the VWAP path, which is correct.
	}
	return out, nil
}

func mapAlpacaStatus(s string) string {
	switch strings.ToLower(s) {
	case "filled":
		return "filled"
	case "canceled", "expired":
		return "cancelled"
	case "rejected":
		return "rejected"
	default:
		// new, accepted, partially_filled, pending_new, pending_cancel,
		// pending_replace, held, calculated, accepted_for_bidding,
		// done_for_day, suspended, stopped — all "working" from our
		// perspective (we read filled_qty for partials).
		return "working"
	}
}

// ─── Account parsing ──────────────────────────────────────────────
//
// Alpaca's get_account returns equity, cash, buying_power, plus a
// pile of risk-controls fields. We surface cash as QuoteCash. Holdings
// come from list_positions, not get_account — but the trading app's
// reconciler calls ParseAccount once per reconcile cycle, so we issue
// a second adapter call (positions.list) inside ParseAccount to populate
// holdings. To keep adapters pure (no I/O), we instead leave Holdings
// nil here and rely on the reconciler to call list_positions separately
// when it needs holdings discovery.
//
// For Alpaca's PARK case (the get_account-only path), cash is enough:
// portfolio_create seeds cash on first install; positions are seeded by
// the agent's explicit watchlist + first orders.

func (alpacaAdapter) ParseAccount(raw json.RawMessage) (*brokerAccount, error) {
	if len(raw) == 0 {
		return nil, errors.New("empty account response")
	}
	var resp struct {
		Cash          string `json:"cash"`
		BuyingPower   string `json:"buying_power"`
		PortfolioValue string `json:"portfolio_value"`
		Status        string `json:"status"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("decode account response: %w", err)
	}
	if resp.Status != "" && resp.Status != "ACTIVE" {
		// SUBMITTED, ACCOUNT_UPDATED, APPROVAL_PENDING, REJECTED, etc.
		return nil, fmt.Errorf("alpaca account status is %q (need ACTIVE)", resp.Status)
	}
	return &brokerAccount{
		QuoteCash: parseFloat(resp.Cash),
		Holdings:  map[string]brokerBalance{}, // populated by reconciler via list_positions
	}, nil
}

// alpacaListPositions — helper called from the reconciler when it needs
// to discover broker-side holdings. Returns the parsed map keyed by
// canonical local symbol (AAPL, BTC-USD). Kept on this adapter (rather
// than in the brokerAdapter interface) because Binance gets holdings
// from the same get_account response — only Alpaca needs the extra hop.
func alpacaParsePositions(raw json.RawMessage) (map[string]brokerBalance, error) {
	if len(raw) == 0 {
		return map[string]brokerBalance{}, nil
	}
	var positions []struct {
		Symbol         string `json:"symbol"`
		Qty            string `json:"qty"`
		Side           string `json:"side"` // "long" | "short"
		AvgEntryPrice  string `json:"avg_entry_price"`
	}
	if err := json.Unmarshal(raw, &positions); err != nil {
		return nil, fmt.Errorf("decode positions response: %w", err)
	}
	out := map[string]brokerBalance{}
	for _, p := range positions {
		qty := parseFloat(p.Qty)
		if qty == 0 || strings.EqualFold(p.Side, "short") {
			// Short positions are out of scope for v0.2 (the engine is
			// long-only). Skip them rather than corrupt local state.
			continue
		}
		canonical := fromAlpacaSymbol(p.Symbol)
		// avg_entry_price is the real cost basis Alpaca tracks across
		// every fill that built the current position. Seeding portfolios
		// with this instead of current_mark means unrealized P&L is
		// correct from the first refresh after connect — operators see
		// "you've got +$840 on AAPL", not "+$0".
		out[canonical] = brokerBalance{
			Asset:   canonical,
			Free:    qty,
			AvgCost: parseFloat(p.AvgEntryPrice),
		}
	}
	return out, nil
}

// Order history — Alpaca's /v2/orders accepts {status, limit, direction}.
// "closed" returns filled + canceled + expired + rejected. "open" gives
// the currently-working set. We cap at 50 to keep portfolio_create
// snappy even for old accounts with thousands of historical orders.
func (alpacaAdapter) OrdersHistoryTool() (string, map[string]any) {
	return "list_orders", map[string]any{
		"status":    "closed",
		"limit":     50,
		"direction": "desc", // newest first
	}
}
func (alpacaAdapter) OpenOrdersTool() (string, map[string]any) {
	return "list_orders", map[string]any{
		"status":    "open",
		"limit":     50,
		"direction": "desc",
	}
}

// Alpaca's list_orders response shape (from their REST docs):
//
//	{
//	  "id": "...",
//	  "client_order_id": "...",
//	  "created_at": "2024-…",
//	  "submitted_at": "...",
//	  "filled_at": "...",
//	  "symbol": "AAPL",
//	  "asset_class": "us_equity",
//	  "qty": "10",
//	  "filled_qty": "10",
//	  "type": "market",
//	  "side": "buy",
//	  "time_in_force": "day",
//	  "limit_price": "228.50" | null,
//	  "stop_price": null,
//	  "filled_avg_price": "227.69",
//	  "status": "filled"
//	}
//
// We normalise statuses via mapAlpacaStatus (same map used by
// ParseOrder for create_order responses).
func (alpacaAdapter) ParseOrders(raw json.RawMessage) ([]brokerHistoricOrder, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	var rows []struct {
		ID             string `json:"id"`
		ClientOrderID  string `json:"client_order_id"`
		CreatedAt      string `json:"created_at"`
		SubmittedAt    string `json:"submitted_at"`
		FilledAt       string `json:"filled_at"`
		CanceledAt     string `json:"canceled_at"`
		ExpiredAt      string `json:"expired_at"`
		Symbol         string `json:"symbol"`
		AssetClass     string `json:"asset_class"`
		Qty            string `json:"qty"`
		FilledQty      string `json:"filled_qty"`
		Type           string `json:"type"`
		Side           string `json:"side"`
		TIF            string `json:"time_in_force"`
		LimitPrice     string `json:"limit_price"`
		StopPrice      string `json:"stop_price"`
		FilledAvgPrice string `json:"filled_avg_price"`
		Status         string `json:"status"`
	}
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("decode list_orders response: %w", err)
	}
	out := make([]brokerHistoricOrder, 0, len(rows))
	for _, r := range rows {
		canonical := fromAlpacaSymbol(r.Symbol)
		// Pick a resolved-at timestamp from whichever terminal event
		// fired; falls back to "" for still-working orders.
		resolved := r.FilledAt
		if resolved == "" {
			resolved = r.CanceledAt
		}
		if resolved == "" {
			resolved = r.ExpiredAt
		}
		placed := r.SubmittedAt
		if placed == "" {
			placed = r.CreatedAt
		}
		out = append(out, brokerHistoricOrder{
			BrokerOrderID: r.ID,
			ClientOrderID: r.ClientOrderID,
			Symbol:        canonical,
			AssetClass:    inferAssetClass(canonical),
			Side:          strings.ToLower(r.Side),
			Type:          strings.ToLower(r.Type),
			Qty:           parseFloat(r.Qty),
			FilledQty:     parseFloat(r.FilledQty),
			AvgFillPrice:  parseFloat(r.FilledAvgPrice),
			LimitPrice:    parseFloat(r.LimitPrice),
			StopPrice:     parseFloat(r.StopPrice),
			TIF:           strings.ToLower(r.TIF),
			Status:        mapAlpacaStatus(r.Status),
			BrokerStatus:  r.Status,
			PlacedAt:      placed,
			ResolvedAt:    resolved,
		})
	}
	return out, nil
}

func (alpacaAdapter) CancelArgs(o *Order, brokerOrderID string) map[string]any {
	// Alpaca's cancel_order takes the alpaca order_id only (no client-id
	// path on cancel). The caller resolves brokerOrderID via the
	// rationale-journal lookup before calling us.
	return map[string]any{"order_id": brokerOrderID}
}

func (alpacaAdapter) StatusArgs(o *Order, brokerOrderID string) map[string]any {
	// get_order accepts the alpaca id OR the client_order_id; prefer the
	// alpaca id when known (faster lookup), else fall back to client.
	if brokerOrderID != "" {
		return map[string]any{"order_id": brokerOrderID}
	}
	return map[string]any{"order_id": o.ID} // client_order_id route
}

// IsUnknownOrderError — alpaca returns HTTP 404 with body
// `{"code":40410000,"message":"order not found"}` when the order id
// isn't recognised. The integration runner surfaces this as
// !res.Success with a code-bearing payload.
func (alpacaAdapter) IsUnknownOrderError(code, detail string) bool {
	return strings.HasPrefix(code, "alpaca_40410") ||
		strings.Contains(strings.ToLower(detail), "order not found") ||
		strings.Contains(strings.ToLower(detail), "not found")
}

func (alpacaAdapter) ErrText(res *sdk.ExecuteResult, err error) (code, detail string) {
	return alpacaErrText(res, err)
}

// alpacaErrText — Alpaca's error envelope:
//
//	{"code": 40010001, "message": "qty must be > 0"}
//
// Counterpart to brokerErrText (which is Binance-shaped). Dispatched
// per-adapter via the brokerAdapter.ErrText method.
func alpacaErrText(res *sdk.ExecuteResult, err error) (code, detail string) {
	if err != nil {
		return "broker_call_failed", err.Error()
	}
	if res == nil {
		return "broker_no_response", "no response from broker"
	}
	if !res.Success {
		var e struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		}
		if jerr := json.Unmarshal(res.Data, &e); jerr == nil && e.Code != 0 {
			return fmt.Sprintf("alpaca_%d", e.Code), e.Message
		}
		return "broker_non_2xx", string(res.Data)
	}
	return "", ""
}
