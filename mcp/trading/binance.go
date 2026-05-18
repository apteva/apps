package main

// Binance broker adapter. Pure translation: local Order types ↔ Binance
// REST shapes. No I/O. The integration runner in apteva-server signs and
// transports the request; we only build args and parse responses.
//
// Spot only, long-only — matches the paper engine's semantics. Stop
// orders translate to STOP_LOSS_LIMIT (Binance spot doesn't ship a
// pure stop-market). Polymarket is paper-only on this adapter.
//
// The package-level functions stay for backwards-compatibility with
// tests + callers that want raw access; the brokerAdapter implementation
// at the bottom of this file is what tools.go and exec.go dispatch
// through. Adding a sibling broker = a new file mirroring the same
// structure plus a single registerAdapter() call in init().

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func init() { registerAdapter(&binanceAdapter{}) }

type binanceAdapter struct{}

func (binanceAdapter) Slug() string { return "binance-trading" }

func (binanceAdapter) Capabilities() brokerCapabilities {
	return brokerCapabilities{
		AssetClasses:     []string{"crypto"},
		OrderTypes:       []string{"market", "limit", "stop"},
		TIFs:             []string{"day", "gtc", "ioc", "fok"},
		Fractional:       true,
		CancelByClientID: true, // origClientOrderId
		QuoteCurrency:    "USDT",
	}
}

func (binanceAdapter) ToolMap() map[string]string {
	return map[string]string{
		"order.place":     "create_order",
		"order.cancel":    "cancel_order",
		"order.status":    "get_order",
		"account.summary": "get_account",
	}
}

func (binanceAdapter) ToBrokerSymbol(canonical string) string { return toBinanceSymbol(canonical) }
func (binanceAdapter) TranslateOrder(o *Order) (map[string]any, error) {
	return translateOrder(o)
}
func (binanceAdapter) ParseOrder(raw json.RawMessage) (*brokerOrderResult, error) {
	return parseBinanceOrder(raw)
}
func (binanceAdapter) ParseAccount(raw json.RawMessage) (*brokerAccount, error) {
	return parseBinanceAccount(raw)
}

// Binance's get_account already includes holdings — no second call.
func (binanceAdapter) HoldingsTool() string { return "" }
func (binanceAdapter) ParseHoldings(raw json.RawMessage) (map[string]brokerBalance, error) {
	return map[string]brokerBalance{}, nil
}

// History backfill — Binance's all_orders / open_orders both require
// a per-symbol query. The portfolio-create backfill path doesn't know
// which symbols to iterate (we'd need to enumerate every pair the
// account ever touched), so we skip backfill for Binance in v1.
// Adopting this later means walking holdings + watchlist for symbols
// and issuing one all_orders call per pair — doable but rate-limit
// sensitive.
func (binanceAdapter) OrdersHistoryTool() (string, map[string]any) { return "", nil }
func (binanceAdapter) OpenOrdersTool() (string, map[string]any)    { return "", nil }
func (binanceAdapter) ParseOrders(raw json.RawMessage) ([]brokerHistoricOrder, error) {
	return nil, nil
}

func (binanceAdapter) CancelArgs(o *Order, brokerOrderID string) map[string]any {
	// origClientOrderId is stable across orderId reuse — prefer it.
	return map[string]any{
		"symbol":            toBinanceSymbol(o.Symbol),
		"origClientOrderId": o.ID,
	}
}

func (binanceAdapter) StatusArgs(o *Order, brokerOrderID string) map[string]any {
	return map[string]any{
		"symbol":            toBinanceSymbol(o.Symbol),
		"origClientOrderId": o.ID,
	}
}

func (binanceAdapter) IsUnknownOrderError(code, detail string) bool {
	return code == "binance_-2013" || strings.Contains(detail, "does not exist")
}

func (binanceAdapter) ErrText(res *sdk.ExecuteResult, err error) (code, detail string) {
	return brokerErrText(res, err)
}

// ─── Symbol mapping ────────────────────────────────────────────────
//
// Mock universe uses "{base}-USD"; Binance uses "{base}USDT". The
// generic strip-and-rewrite handles every coin in mockUniverse plus
// anything else the agent might add to a watchlist. Reverse direction
// is symmetric.
//
// We deliberately keep this *table-free*: a static map of 5 symbols
// would diverge from mockUniverse the moment someone adds a coin
// there. Generic transform stays correct without coupling.

func toBinanceSymbol(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	if strings.HasSuffix(s, "-USD") {
		return strings.TrimSuffix(s, "-USD") + "USDT"
	}
	if strings.HasSuffix(s, "USDT") {
		return s
	}
	return s
}

func fromBinanceSymbol(s string) string {
	s = strings.ToUpper(strings.TrimSpace(s))
	if strings.HasSuffix(s, "USDT") {
		return strings.TrimSuffix(s, "USDT") + "-USD"
	}
	return s
}

// ─── Request translation ───────────────────────────────────────────
//
// translateOrder builds the args map the integration runner forwards
// to Binance /order. Field names match binance-trading.json's
// input_schema for create_order.
//
// Idempotency: newClientOrderId = our local Order.ID (uuid12 prefixed
// with "o-"). On a network retry the second create_order returns the
// existing order rather than placing a duplicate.
//
// Precision: v0.2 ships lenient formatting — eight decimals on qty,
// two on price. Binance rejects with LOT_SIZE / PRICE_FILTER if the
// pair's exchange filter is stricter. v0.3 will cache /exchangeInfo
// per symbol and round to its actual stepSize / tickSize.

func translateOrder(o *Order) (map[string]any, error) {
	if o == nil {
		return nil, errors.New("nil order")
	}
	if o.AssetClass == "polymarket" {
		return nil, errors.New("polymarket is not supported on the Binance adapter")
	}
	if o.AssetClass != "crypto" {
		return nil, fmt.Errorf("Binance adapter handles crypto only, got %q", o.AssetClass)
	}

	args := map[string]any{
		"symbol":           toBinanceSymbol(o.Symbol),
		"side":             strings.ToUpper(o.Side), // BUY | SELL
		"quantity":         formatBinanceQty(o.Qty),
		"newClientOrderId": o.ID,
		"newOrderRespType": "FULL", // ensures fills array on inline-fill responses
	}

	switch o.Type {
	case "market":
		args["type"] = "MARKET"
	case "limit":
		if o.LimitPrice == nil {
			return nil, errors.New("limit order missing limit_price")
		}
		args["type"] = "LIMIT"
		args["price"] = formatBinancePrice(*o.LimitPrice)
		tif := strings.ToUpper(o.TIF)
		if tif == "" || tif == "DAY" {
			tif = "GTC" // Binance has no DAY; nearest equivalent is GTC
		}
		args["timeInForce"] = tif
	case "stop":
		// Spot Binance: STOP_LOSS_LIMIT — needs both stopPrice and a
		// limit price. We use stopPrice both as trigger and as the
		// limit cap, which mimics a market-on-trigger closely enough
		// for v0.2. Operators wanting tighter control can place a real
		// LIMIT order off-platform.
		if o.StopPrice == nil {
			return nil, errors.New("stop order missing stop_price")
		}
		args["type"] = "STOP_LOSS_LIMIT"
		args["stopPrice"] = formatBinancePrice(*o.StopPrice)
		args["price"] = formatBinancePrice(*o.StopPrice)
		args["timeInForce"] = "GTC"
	default:
		return nil, fmt.Errorf("unsupported order type %q", o.Type)
	}

	return args, nil
}

func formatBinanceQty(q float64) string {
	// 8 decimals covers BTC-level precision. Binance accepts trailing
	// zeros as long as the value clears LOT_SIZE.minQty.
	return strconv.FormatFloat(q, 'f', 8, 64)
}

func formatBinancePrice(p float64) string {
	return strconv.FormatFloat(p, 'f', 2, 64)
}

// ─── Response parsing ──────────────────────────────────────────────

// brokerOrderResult — normalized view of a create_order or get_order
// response. Status maps to {working, filled, cancelled, rejected};
// fills (if any) are ready for dbInsertFill / dbApplyFill.
type brokerOrderResult struct {
	BrokerOrderID       string  // stringified Binance orderId
	ClientOrderID       string  // echoed newClientOrderId — our o.ID
	Status              string  // local: working | filled | cancelled | rejected
	BrokerStatus        string  // raw upstream status (NEW, PARTIALLY_FILLED, FILLED, …)
	ExecutedQty         float64 // cumulative qty already filled
	CummulativeQuoteQty float64 // cumulative quote-asset spent (USDT) — used to derive VWAP when fills[] is absent (polled get_order)
	Fills               []brokerFill
}

type brokerFill struct {
	Price           float64
	Qty             float64
	Commission      float64
	CommissionAsset string
}

// parseBinanceOrder decodes a create_order or get_order response. Both
// endpoints share the same shape; create_order with newOrderRespType=FULL
// includes the fills array for synchronous fills.
func parseBinanceOrder(raw json.RawMessage) (*brokerOrderResult, error) {
	if len(raw) == 0 {
		return nil, errors.New("empty broker response")
	}
	var resp struct {
		Symbol              string `json:"symbol"`
		OrderID             int64  `json:"orderId"`
		ClientOrderID       string `json:"clientOrderId"`
		Status              string `json:"status"`
		ExecutedQty         string `json:"executedQty"`
		CummulativeQuoteQty string `json:"cummulativeQuoteQty"` // sic — Binance's spelling
		Fills               []struct {
			Price           string `json:"price"`
			Qty             string `json:"qty"`
			Commission      string `json:"commission"`
			CommissionAsset string `json:"commissionAsset"`
		} `json:"fills"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("decode broker response: %w", err)
	}
	if resp.OrderID == 0 {
		return nil, fmt.Errorf("broker response missing orderId: %s", string(raw))
	}
	out := &brokerOrderResult{
		BrokerOrderID:       strconv.FormatInt(resp.OrderID, 10),
		ClientOrderID:       resp.ClientOrderID,
		BrokerStatus:        resp.Status,
		Status:              mapBinanceStatus(resp.Status),
		ExecutedQty:         parseFloat(resp.ExecutedQty),
		CummulativeQuoteQty: parseFloat(resp.CummulativeQuoteQty),
	}
	for _, f := range resp.Fills {
		out.Fills = append(out.Fills, brokerFill{
			Price:           parseFloat(f.Price),
			Qty:             parseFloat(f.Qty),
			Commission:      parseFloat(f.Commission),
			CommissionAsset: f.CommissionAsset,
		})
	}
	return out, nil
}

// mapBinanceStatus collapses Binance's order states to our four:
//
//	working   — NEW, PARTIALLY_FILLED, PENDING_CANCEL
//	filled    — FILLED
//	cancelled — CANCELED, EXPIRED
//	rejected  — REJECTED, EXPIRED_IN_MATCH
//
// PARTIALLY_FILLED stays "working" — the engine reads ExecutedQty to
// apply incremental fills as they arrive. Only when status flips to
// FILLED do we mark the local order resolved.
func mapBinanceStatus(s string) string {
	switch strings.ToUpper(s) {
	case "FILLED":
		return "filled"
	case "CANCELED", "EXPIRED":
		return "cancelled"
	case "REJECTED", "EXPIRED_IN_MATCH":
		return "rejected"
	default:
		return "working"
	}
}

// ─── Account parsing ───────────────────────────────────────────────

// brokerAccount — normalized view of get_account.
type brokerAccount struct {
	QuoteCash float64                  // free USDT
	Holdings  map[string]brokerBalance // base-asset symbol → free balance
}

type brokerBalance struct {
	Asset   string  // canonical local form: "BTC-USD", "AAPL", …
	Free    float64 // qty available
	AvgCost float64 // broker-reported cost basis (Alpaca's avg_entry_price);
	                // 0 when the broker doesn't publish one (Binance get_account, polymarket).
}

// parseBinanceAccount decodes the get_account response into a quote
// (USDT) cash figure plus a per-base-asset holdings map. Locked
// balances are ignored — they belong to working orders the reconciler
// already tracks separately.
func parseBinanceAccount(raw json.RawMessage) (*brokerAccount, error) {
	if len(raw) == 0 {
		return nil, errors.New("empty account response")
	}
	var resp struct {
		Balances []struct {
			Asset  string `json:"asset"`
			Free   string `json:"free"`
			Locked string `json:"locked"`
		} `json:"balances"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("decode account response: %w", err)
	}
	out := &brokerAccount{Holdings: map[string]brokerBalance{}}
	for _, b := range resp.Balances {
		free := parseFloat(b.Free)
		if free <= 0 {
			continue
		}
		if strings.EqualFold(b.Asset, "USDT") {
			out.QuoteCash = free
			continue
		}
		// Canonicalize to local form (BTC-USD) so reconcile code can
		// treat every adapter's holdings map the same way.
		canonical := strings.ToUpper(b.Asset) + "-USD"
		out.Holdings[canonical] = brokerBalance{
			Asset: canonical,
			Free:  free,
		}
	}
	return out, nil
}

// ─── Error normalisation ───────────────────────────────────────────
//
// Binance error responses look like {"code":-2010,"msg":"Account has insufficient balance ..."}.
// brokerErrText extracts (code, message) when present so rejection_code
// in the orders table carries something machine-readable.

func brokerErrText(res *sdk.ExecuteResult, err error) (code, detail string) {
	if err != nil {
		return "broker_call_failed", err.Error()
	}
	if res == nil {
		return "broker_no_response", "no response from broker"
	}
	if !res.Success {
		var e struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		}
		if jerr := json.Unmarshal(res.Data, &e); jerr == nil && e.Code != 0 {
			return fmt.Sprintf("binance_%d", e.Code), e.Msg
		}
		return "broker_non_2xx", string(res.Data)
	}
	return "", ""
}

// ─── Helpers ───────────────────────────────────────────────────────

func parseFloat(s string) float64 {
	if s == "" {
		return 0
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0
	}
	return v
}
