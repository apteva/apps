package main

// Tier 1 — pure-translation tests for the Binance adapter. No I/O, no
// broker. Live broker calls (Tier 2) live in binance_live_test.go and
// only run when BINANCE_TESTNET_KEY/SECRET are in env. The integration
// runner itself is exercised by integration_test.go.

import (
	"encoding/json"
	"strings"
	"testing"
)

func TestToBinanceSymbol(t *testing.T) {
	cases := map[string]string{
		"BTC-USD":      "BTCUSDT",
		"eth-usd":      "ETHUSDT",
		"SOL-USD":      "SOLUSDT",
		"AVAX-USD":     "AVAXUSDT",
		"DOGE-USD":     "DOGEUSDT",
		"  ETH-USD  ":  "ETHUSDT", // whitespace-tolerant
		"BTCUSDT":      "BTCUSDT", // already-binance shape passes through
		"unknown-spot": "UNKNOWN-SPOT",
	}
	for in, want := range cases {
		if got := toBinanceSymbol(in); got != want {
			t.Errorf("toBinanceSymbol(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestFromBinanceSymbol(t *testing.T) {
	cases := map[string]string{
		"BTCUSDT": "BTC-USD",
		"ethusdt": "ETH-USD",
		"BTC-USD": "BTC-USD", // already-local shape passes through
	}
	for in, want := range cases {
		if got := fromBinanceSymbol(in); got != want {
			t.Errorf("fromBinanceSymbol(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestTranslateOrderMarket(t *testing.T) {
	o := &Order{
		ID: "o-abc123def456", PortfolioID: 1, Symbol: "BTC-USD",
		AssetClass: "crypto", Side: "buy", Type: "market", Qty: 0.001,
	}
	args, err := translateOrder(o)
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	mustEq(t, args, "symbol", "BTCUSDT")
	mustEq(t, args, "side", "BUY")
	mustEq(t, args, "type", "MARKET")
	mustEq(t, args, "newClientOrderId", "o-abc123def456")
	mustEq(t, args, "newOrderRespType", "FULL")
	if _, present := args["price"]; present {
		t.Errorf("market order should not include price, got %v", args["price"])
	}
	if _, present := args["timeInForce"]; present {
		t.Errorf("market order should not include timeInForce, got %v", args["timeInForce"])
	}
}

func TestTranslateOrderLimit(t *testing.T) {
	lp := 65_000.0
	o := &Order{
		ID: "o-limit", Symbol: "ETH-USD", AssetClass: "crypto",
		Side: "sell", Type: "limit", Qty: 0.5, LimitPrice: &lp, TIF: "gtc",
	}
	args, err := translateOrder(o)
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	mustEq(t, args, "symbol", "ETHUSDT")
	mustEq(t, args, "side", "SELL")
	mustEq(t, args, "type", "LIMIT")
	mustEq(t, args, "price", "65000.00")
	mustEq(t, args, "timeInForce", "GTC")
	mustEq(t, args, "quantity", "0.50000000")
}

func TestTranslateOrderLimitDayMapsToGTC(t *testing.T) {
	// Binance has no DAY TIF; we map "day" → "GTC" as the closest
	// equivalent for a paper-style order. The agent doesn't have to
	// know about Binance TIF semantics.
	lp := 100.0
	o := &Order{
		ID: "o-day", Symbol: "BTC-USD", AssetClass: "crypto",
		Side: "buy", Type: "limit", Qty: 0.01, LimitPrice: &lp, TIF: "day",
	}
	args, _ := translateOrder(o)
	mustEq(t, args, "timeInForce", "GTC")
}

func TestTranslateOrderStop(t *testing.T) {
	sp := 60_000.0
	o := &Order{
		ID: "o-stop", Symbol: "BTC-USD", AssetClass: "crypto",
		Side: "sell", Type: "stop", Qty: 0.001, StopPrice: &sp,
	}
	args, err := translateOrder(o)
	if err != nil {
		t.Fatalf("translate: %v", err)
	}
	mustEq(t, args, "type", "STOP_LOSS_LIMIT")
	mustEq(t, args, "stopPrice", "60000.00")
	mustEq(t, args, "price", "60000.00")
	mustEq(t, args, "timeInForce", "GTC")
}

func TestTranslateOrderRejectsPolymarket(t *testing.T) {
	o := &Order{Symbol: "POLY:btc-100k-2026", AssetClass: "polymarket", Side: "yes", Type: "market", Qty: 100}
	if _, err := translateOrder(o); err == nil {
		t.Error("expected polymarket to be rejected on Binance adapter")
	}
}

func TestTranslateOrderRejectsLimitWithoutPrice(t *testing.T) {
	o := &Order{Symbol: "BTC-USD", AssetClass: "crypto", Side: "buy", Type: "limit", Qty: 0.001}
	if _, err := translateOrder(o); err == nil {
		t.Error("expected limit-without-price to be rejected")
	}
}

// ─── Response parsing ──────────────────────────────────────────────

func TestParseBinanceOrderFilledMarket(t *testing.T) {
	// Realistic create_order MARKET response with newOrderRespType=FULL.
	raw := json.RawMessage(`{
		"symbol": "BTCUSDT",
		"orderId": 28,
		"orderListId": -1,
		"clientOrderId": "o-abc123def456",
		"transactTime": 1507725176595,
		"price": "0.00000000",
		"origQty": "0.00100000",
		"executedQty": "0.00100000",
		"cummulativeQuoteQty": "67.43210000",
		"status": "FILLED",
		"timeInForce": "GTC",
		"type": "MARKET",
		"side": "BUY",
		"fills": [
			{"price":"67432.10","qty":"0.00100000","commission":"0.00000100","commissionAsset":"BTC","tradeId":1}
		]
	}`)
	br, err := parseBinanceOrder(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if br.BrokerOrderID != "28" {
		t.Errorf("broker id = %q, want %q", br.BrokerOrderID, "28")
	}
	if br.ClientOrderID != "o-abc123def456" {
		t.Errorf("client id = %q", br.ClientOrderID)
	}
	if br.Status != "filled" {
		t.Errorf("status = %q, want filled", br.Status)
	}
	if br.BrokerStatus != "FILLED" {
		t.Errorf("broker_status = %q", br.BrokerStatus)
	}
	if br.ExecutedQty != 0.001 {
		t.Errorf("executed_qty = %v", br.ExecutedQty)
	}
	if br.CummulativeQuoteQty != 67.4321 {
		t.Errorf("cummulative_quote_qty = %v", br.CummulativeQuoteQty)
	}
	if len(br.Fills) != 1 {
		t.Fatalf("expected 1 fill, got %d", len(br.Fills))
	}
	f := br.Fills[0]
	if f.Price != 67432.10 || f.Qty != 0.001 {
		t.Errorf("fill = %+v", f)
	}
}

func TestParseBinanceOrderNewLimit(t *testing.T) {
	raw := json.RawMessage(`{
		"symbol": "BTCUSDT", "orderId": 99, "clientOrderId": "o-limit",
		"status": "NEW", "executedQty": "0.00000000", "cummulativeQuoteQty": "0.00000000",
		"fills": []
	}`)
	br, err := parseBinanceOrder(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if br.Status != "working" {
		t.Errorf("NEW should map to working, got %q", br.Status)
	}
	if br.ExecutedQty != 0 {
		t.Errorf("executed_qty = %v", br.ExecutedQty)
	}
}

func TestParseBinanceOrderCancelled(t *testing.T) {
	raw := json.RawMessage(`{
		"symbol": "BTCUSDT", "orderId": 5, "clientOrderId": "o-x",
		"status": "CANCELED", "executedQty": "0.00000000", "cummulativeQuoteQty": "0.00000000"
	}`)
	br, _ := parseBinanceOrder(raw)
	if br.Status != "cancelled" {
		t.Errorf("CANCELED should map to cancelled, got %q", br.Status)
	}
}

func TestParseBinanceOrderRejected(t *testing.T) {
	raw := json.RawMessage(`{
		"symbol": "BTCUSDT", "orderId": 5, "clientOrderId": "o-x",
		"status": "REJECTED", "executedQty": "0.00000000", "cummulativeQuoteQty": "0.00000000"
	}`)
	br, _ := parseBinanceOrder(raw)
	if br.Status != "rejected" {
		t.Errorf("REJECTED should map to rejected, got %q", br.Status)
	}
}

func TestParseBinanceOrderMissingOrderID(t *testing.T) {
	raw := json.RawMessage(`{"status": "FILLED", "executedQty": "1"}`)
	if _, err := parseBinanceOrder(raw); err == nil {
		t.Error("expected error on missing orderId")
	}
}

// ─── Account parsing ──────────────────────────────────────────────

func TestParseBinanceAccount(t *testing.T) {
	raw := json.RawMessage(`{
		"makerCommission": 10,
		"balances": [
			{"asset": "BTC",  "free": "0.05000000", "locked": "0.00000000"},
			{"asset": "ETH",  "free": "1.20000000", "locked": "0.00000000"},
			{"asset": "USDT", "free": "12400.13000000", "locked": "0.00000000"},
			{"asset": "DOGE", "free": "0.00000000", "locked": "0.00000000"}
		]
	}`)
	acct, err := parseBinanceAccount(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if acct.QuoteCash != 12400.13 {
		t.Errorf("quote_cash = %v", acct.QuoteCash)
	}
	if len(acct.Holdings) != 2 {
		t.Errorf("expected 2 non-zero non-USDT holdings, got %d (%+v)", len(acct.Holdings), acct.Holdings)
	}
	if btc, ok := acct.Holdings["BTC"]; !ok || btc.Free != 0.05 {
		t.Errorf("BTC holding wrong: %+v", btc)
	}
}

func TestMapBinanceStatus(t *testing.T) {
	cases := map[string]string{
		"NEW":              "working",
		"PARTIALLY_FILLED": "working",
		"PENDING_CANCEL":   "working",
		"FILLED":           "filled",
		"CANCELED":         "cancelled",
		"EXPIRED":          "cancelled",
		"REJECTED":         "rejected",
		"":                 "working",
	}
	for in, want := range cases {
		if got := mapBinanceStatus(in); got != want {
			t.Errorf("mapBinanceStatus(%q) = %q, want %q", in, got, want)
		}
	}
}

// ─── Helpers ──────────────────────────────────────────────────────

func mustEq(t *testing.T, m map[string]any, key, want string) {
	t.Helper()
	got, _ := m[key].(string)
	if got != want {
		// Whitespace-tolerant compare for stringified numbers.
		if strings.TrimSpace(got) != strings.TrimSpace(want) {
			t.Errorf("args[%q] = %q, want %q", key, got, want)
		}
	}
}
