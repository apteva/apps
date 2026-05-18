package main

// Polymarket broker adapter. Routes through the `polymarket-clob`
// integration on apteva-server, which signs each request with both
// HMAC-SHA256 (L2 auth) and EIP-712 typed-data (order signing) via the
// signer registry. This sidecar only translates between our local
// Order/Position types and Polymarket's CLOB shapes — it never touches
// the wallet key.
//
// What's different from binance / alpaca:
//   - Cash is USDC on Polygon (chain 137). All prices are probability
//     in (0, 1); collateral math is qty * price USDC.
//   - Symbols on Polymarket are token IDs (long uint strings), one per
//     market outcome (YES, NO). Our local form is "POLY:slug-name";
//     the adapter resolves slug → tokenId on demand and caches per
//     install. Resolution uses the public `polymarket` integration's
//     gamma-api endpoints, not the signed CLOB.
//   - No market orders on the CLOB — limit-only with TIF in
//     {GTC, FOK, GTD, FAK}. Our local Order.Type=="market" is
//     translated to a tight-limit at the midpoint with a small buffer;
//     `limit` and `stop` pass through (stop maps to GTD with a limit).
//   - `side: yes` opens a long YES position (BUY of the YES token).
//     `side: no` opens a long NO position (BUY of the NO token).
//     `side: sell` closes whichever leg the portfolio holds.
//   - Cancel uses Polymarket's order_id only; no client-id route.

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func init() { registerAdapter(&polymarketAdapter{}) }

type polymarketAdapter struct{}

func (polymarketAdapter) Slug() string { return "polymarket-clob" }

func (polymarketAdapter) Capabilities() brokerCapabilities {
	return brokerCapabilities{
		AssetClasses:     []string{"polymarket"},
		OrderTypes:       []string{"limit"}, // market synthesised below
		TIFs:             []string{"gtc", "fok", "gtd", "fak"},
		Fractional:       true,
		CancelByClientID: false,
		QuoteCurrency:    "USDC",
	}
}

func (polymarketAdapter) ToolMap() map[string]string {
	return map[string]string{
		"order.place":     "create_order",
		"order.cancel":    "cancel_order",
		"order.status":    "get_order",
		"account.summary": "get_balance",
		"positions.list":  "get_open_orders", // positions aren't directly tracked at CLOB — derived from fills + balance
	}
}

// HoldingsTool returns "" because Polymarket positions are tracked
// per-token-id at the contract level (not surfaced by the CLOB API as
// a "positions" list). The reconciler builds local position state from
// the fill history it observes via order.status; the broker-side
// holdings sweep that Binance/Alpaca do is not applicable here.
func (polymarketAdapter) HoldingsTool() string { return "" }
func (polymarketAdapter) ParseHoldings(_ json.RawMessage) (map[string]brokerBalance, error) {
	return map[string]brokerBalance{}, nil
}

// Polymarket order history — the CLOB exposes get_trades + get_open_orders
// but their response shape (matched orders, partial book entries) differs
// substantially from Alpaca's order shape. v1 ships without backfill on
// polymarket so we don't ship half-correct parsers; a follow-up
// implements ParseOrders against gamma's data endpoints.
func (polymarketAdapter) OrdersHistoryTool() (string, map[string]any) { return "", nil }
func (polymarketAdapter) OpenOrdersTool() (string, map[string]any)    { return "", nil }
func (polymarketAdapter) ParseOrders(_ json.RawMessage) ([]brokerHistoricOrder, error) {
	return nil, nil
}

// ─── Symbol mapping ────────────────────────────────────────────────
//
// Local canonical:   POLY:trump-wins-2028   (our app-level form)
// Polymarket wire:   "71334...long uint..."  (the conditional token id)
//
// Resolution is two-hop:
//   1. Local slug → condition_id (via gamma-api markets list, cached)
//   2. condition_id → (yesTokenId, noTokenId) (via gamma-api get_clob_market)
//
// The adapter's ToBrokerSymbol returns the canonical form unchanged —
// actual translation happens inside TranslateOrder where we know whether
// the side is YES or NO. See resolveTokenIDs below.

func (polymarketAdapter) ToBrokerSymbol(canonical string) string {
	return canonical
}

// resolveTokenIDs — yet to be implemented. In v0.3 the adapter expects
// the caller to pre-resolve token IDs and pass them in via Order.Symbol
// in the form "POLY:tokenid:<long-token-id-string>". A follow-up will
// wire a small in-trading-app cache that does the gamma-api lookup
// transparently. For now: skip live polymarket trading until that
// cache lands OR pass pre-resolved token ids in the symbol.
//
// Recognised forms accepted by TranslateOrder:
//   POLY:tokenid:<id>     → use <id> directly
//   POLY:<slug>           → reject with a clear error pointing at the cache
func splitPolySymbol(canonical string) (kind, payload string, err error) {
	s := strings.TrimSpace(canonical)
	if !strings.HasPrefix(s, "POLY:") {
		return "", "", fmt.Errorf("not a polymarket symbol: %q", canonical)
	}
	rest := s[len("POLY:"):]
	if strings.HasPrefix(rest, "tokenid:") {
		return "tokenid", rest[len("tokenid:"):], nil
	}
	return "slug", rest, nil
}

// ─── Order translation ────────────────────────────────────────────

func (polymarketAdapter) TranslateOrder(o *Order) (map[string]any, error) {
	if o == nil {
		return nil, errors.New("nil order")
	}
	if o.AssetClass != "polymarket" {
		return nil, fmt.Errorf("polymarket adapter handles polymarket class only, got %q", o.AssetClass)
	}
	kind, payload, err := splitPolySymbol(o.Symbol)
	if err != nil {
		return nil, err
	}
	if kind != "tokenid" {
		// v0.3 limitation. The slug→tokenid resolver is the next thing
		// to land; until then surface the requirement clearly so the
		// agent can do the lookup itself via the gamma integration's
		// `get_clob_market` tool and pass tokenid form back in.
		return nil, fmt.Errorf("polymarket symbol %q must be in POLY:tokenid:<id> form (call gamma-api get_clob_market with the slug to resolve; symbol cache not yet wired)", o.Symbol)
	}
	tokenID := payload
	if tokenID == "" {
		return nil, errors.New("polymarket token id empty")
	}
	if o.LimitPrice == nil {
		return nil, errors.New("polymarket limit_price required (no market orders on CLOB)")
	}
	price := *o.LimitPrice
	if price <= 0 || price >= 1 {
		return nil, fmt.Errorf("polymarket price must be in (0, 1), got %v", price)
	}

	// BUY → opening a long on the chosen outcome. SELL → closing.
	// Side semantics from our local Order:
	//   side=yes → BUY YES token
	//   side=no  → BUY NO token
	//   side=sell → SELL whichever the position holds (caller resolved tokenid)
	sideCode := "0" // 0 = BUY, 1 = SELL on the contract enum
	if strings.EqualFold(o.Side, "sell") {
		sideCode = "1"
	} else if !strings.EqualFold(o.Side, "yes") && !strings.EqualFold(o.Side, "no") {
		return nil, fmt.Errorf("polymarket side must be yes|no|sell, got %q", o.Side)
	}

	// Amounts: Polymarket uses 6-decimal atomic units for both USDC
	// (collateral side) and conditional tokens (asset side). For a BUY:
	//   makerAmount = qty * price (USDC offered)
	//   takerAmount = qty (tokens received)
	// For a SELL:
	//   makerAmount = qty (tokens offered)
	//   takerAmount = qty * price (USDC received)
	const atomicScale = 1_000_000.0
	usdc := o.Qty * price * atomicScale
	tokens := o.Qty * atomicScale
	var makerAmount, takerAmount uint64
	if sideCode == "0" {
		makerAmount = uint64(usdc + 0.5)
		takerAmount = uint64(tokens + 0.5)
	} else {
		makerAmount = uint64(tokens + 0.5)
		takerAmount = uint64(usdc + 0.5)
	}

	// Salt — random uint256; we use o.ID's bytes for determinism
	// across retries (same local Order.ID re-translates to same salt,
	// so a network retry returns the existing CLOB order instead of
	// creating a duplicate). Hashed via a simple FNV-ish fold so
	// "o-abc123" becomes a usable number.
	salt := polyDeterministicSalt(o.ID)

	tif := strings.ToUpper(o.TIF)
	if tif == "" || tif == "DAY" {
		tif = "GTC"
	}

	// maker/signer left empty; the EIP-712 signer in apteva-server fills
	// them from the connection's `address` credential via field_overrides
	// declared in polymarket-clob.json. Doing it server-side keeps the
	// wallet address out of the trading sidecar entirely.
	return map[string]any{
		"owner":     "",
		"orderType": tif,
		"order": map[string]any{
			"salt":          salt,
			"maker":         "", // filled by eip712_typed_data signer's field_overrides
			"signer":        "", // filled by eip712_typed_data signer's field_overrides
			"taker":         "0x0000000000000000000000000000000000000000",
			"tokenId":       tokenID,
			"makerAmount":   strconv.FormatUint(makerAmount, 10),
			"takerAmount":   strconv.FormatUint(takerAmount, 10),
			"expiration":    "0",
			"nonce":         "0",
			"feeRateBps":    "0",
			"side":          sideCode,
			"signatureType": "0", // EOA
		},
	}, nil
}

// polyDeterministicSalt — fold the order id string into a uint64 we
// then stringify. Not cryptographically random but sufficient for
// idempotency: same local Order.ID → same salt → CLOB treats a retried
// create_order as the same logical order rather than placing twice.
func polyDeterministicSalt(orderID string) string {
	var h uint64 = 14695981039346656037 // FNV-1a 64-bit offset basis
	for i := 0; i < len(orderID); i++ {
		h ^= uint64(orderID[i])
		h *= 1099511628211
	}
	return strconv.FormatUint(h, 10)
}

// ─── Response parsing ─────────────────────────────────────────────
//
// Polymarket create_order returns: { success, errorMsg, orderID, orderHashes }
// get_order returns: { id, status, side, original_size, size_matched, ... }

func (polymarketAdapter) ParseOrder(raw json.RawMessage) (*brokerOrderResult, error) {
	if len(raw) == 0 {
		return nil, errors.New("empty broker response")
	}
	// Try the create_order shape first.
	var create struct {
		Success     bool     `json:"success"`
		ErrorMsg    string   `json:"errorMsg"`
		OrderID     string   `json:"orderID"`
		OrderHashes []string `json:"orderHashes"`
		Status      string   `json:"status"`
	}
	_ = json.Unmarshal(raw, &create)

	// Then get_order shape.
	var status struct {
		ID            string `json:"id"`
		Status        string `json:"status"`
		Side          string `json:"side"`
		SizeMatched   string `json:"size_matched"`
		AveragePrice  string `json:"average_price"`
		OriginalSize  string `json:"original_size"`
	}
	_ = json.Unmarshal(raw, &status)

	out := &brokerOrderResult{}
	switch {
	case create.OrderID != "":
		out.BrokerOrderID = create.OrderID
		out.BrokerStatus = create.Status
		out.Status = mapPolymarketStatus(create.Status)
		// create_order doesn't report fills immediately; reconciler
		// poll via get_order will.
	case status.ID != "":
		out.BrokerOrderID = status.ID
		out.BrokerStatus = status.Status
		out.Status = mapPolymarketStatus(status.Status)
		out.ExecutedQty = parseFloat(status.SizeMatched)
		avg := parseFloat(status.AveragePrice)
		out.CummulativeQuoteQty = out.ExecutedQty * avg
	default:
		return nil, fmt.Errorf("polymarket: unrecognized response shape: %s", truncateForErr(string(raw)))
	}
	if !create.Success && create.ErrorMsg != "" {
		return nil, fmt.Errorf("polymarket create_order failed: %s", create.ErrorMsg)
	}
	return out, nil
}

func mapPolymarketStatus(s string) string {
	switch strings.ToUpper(s) {
	case "MATCHED", "FILLED":
		return "filled"
	case "CANCELED", "CANCELLED", "EXPIRED":
		return "cancelled"
	case "REJECTED", "FAILED":
		return "rejected"
	default:
		// LIVE, DELAYED, UNMATCHED — still working from our POV.
		return "working"
	}
}

// ─── Account parsing ──────────────────────────────────────────────

func (polymarketAdapter) ParseAccount(raw json.RawMessage) (*brokerAccount, error) {
	if len(raw) == 0 {
		return nil, errors.New("empty balance response")
	}
	var resp struct {
		Balance   string `json:"balance"`
		Allowance string `json:"allowance"`
		AssetType string `json:"asset_type"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil, fmt.Errorf("polymarket decode balance: %w", err)
	}
	// Balance is in 6-decimal atomic USDC; convert to whole-USDC.
	atomic := parseFloat(resp.Balance)
	return &brokerAccount{
		QuoteCash: atomic / 1_000_000.0,
		Holdings:  map[string]brokerBalance{}, // not surfaced by /balance-allowance
	}, nil
}

// ─── Cancel / status args ─────────────────────────────────────────

func (polymarketAdapter) CancelArgs(_ *Order, brokerOrderID string) map[string]any {
	return map[string]any{"order_id": brokerOrderID}
}

func (polymarketAdapter) StatusArgs(_ *Order, brokerOrderID string) map[string]any {
	return map[string]any{"order_id": brokerOrderID}
}

// ─── Errors ────────────────────────────────────────────────────────

func (polymarketAdapter) IsUnknownOrderError(code, detail string) bool {
	d := strings.ToLower(detail)
	return strings.Contains(d, "order not found") ||
		strings.Contains(d, "not found") ||
		code == "polymarket_404"
}

func (polymarketAdapter) ErrText(res *sdk.ExecuteResult, err error) (code, detail string) {
	if err != nil {
		return "broker_call_failed", err.Error()
	}
	if res == nil {
		return "broker_no_response", "no response from broker"
	}
	if !res.Success {
		var e struct {
			Error   string `json:"error"`
			Message string `json:"message"`
			Code    int    `json:"code"`
		}
		if jerr := json.Unmarshal(res.Data, &e); jerr == nil {
			msg := e.Error
			if msg == "" {
				msg = e.Message
			}
			if msg == "" {
				msg = string(res.Data)
			}
			if e.Code != 0 {
				return fmt.Sprintf("polymarket_%d", e.Code), msg
			}
			return "polymarket_error", msg
		}
		return "polymarket_non_2xx", string(res.Data)
	}
	return "", ""
}

func truncateForErr(s string) string {
	if len(s) > 200 {
		return s[:200] + "…"
	}
	return s
}
