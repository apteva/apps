package main

import (
	"encoding/json"
	"testing"
)

func TestCoinbaseAdapter(t *testing.T) {
	lp := 70000.0
	args, err := (coinbaseAdapter{}).TranslateOrder(&Order{ID: "o-coin", Symbol: "BTC-USD", AssetClass: "crypto", Side: "buy", Type: "limit", Qty: 0.1, LimitPrice: &lp})
	if err != nil {
		t.Fatalf("TranslateOrder: %v", err)
	}
	mustEq(t, args, "product_id", "BTC-USD")
	mustEq(t, args, "side", "BUY")
	cfg := args["order_configuration"].(map[string]any)
	if _, ok := cfg["limit_limit_gtc"]; !ok {
		t.Fatalf("expected Coinbase limit_limit_gtc config, got %+v", cfg)
	}

	br, err := (coinbaseAdapter{}).ParseOrder(json.RawMessage(`{"success":true,"success_response":{"order_id":"cb-1","client_order_id":"o-coin"}}`))
	if err != nil {
		t.Fatalf("ParseOrder create: %v", err)
	}
	if br.BrokerOrderID != "cb-1" || br.Status != "working" {
		t.Fatalf("bad create parse: %+v", br)
	}
	br, err = (coinbaseAdapter{}).ParseOrder(json.RawMessage(`{"order":{"order_id":"cb-1","status":"FILLED","filled_size":"0.1","average_filled_price":"70000"}}`))
	if err != nil {
		t.Fatalf("ParseOrder get: %v", err)
	}
	if br.Status != "filled" || br.CummulativeQuoteQty != 7000 {
		t.Fatalf("bad get parse: %+v", br)
	}
	acct, err := (coinbaseAdapter{}).ParseAccount(json.RawMessage(`{"accounts":[{"currency":"USD","available_balance":{"value":"1000"}},{"currency":"BTC","available_balance":{"value":"0.2"}}]}`))
	if err != nil {
		t.Fatalf("ParseAccount: %v", err)
	}
	if acct.QuoteCash != 1000 || acct.Holdings["BTC-USD"].Free != 0.2 {
		t.Fatalf("bad account: %+v", acct)
	}
}

func TestOKXAdapter(t *testing.T) {
	lp := 3500.0
	args, err := (okxAdapter{}).TranslateOrder(&Order{ID: "o-okx", Symbol: "ETH-USD", AssetClass: "crypto", Side: "sell", Type: "limit", Qty: 1.5, LimitPrice: &lp, TIF: "ioc"})
	if err != nil {
		t.Fatalf("TranslateOrder: %v", err)
	}
	mustEq(t, args, "instId", "ETH-USDT")
	mustEq(t, args, "ordType", "ioc")
	mustEq(t, args, "px", "3500")

	br, err := (okxAdapter{}).ParseOrder(json.RawMessage(`{"code":"0","data":[{"ordId":"okx-1","clOrdId":"ookx","state":"filled","accFillSz":"1.5","avgPx":"3500"}]}`))
	if err != nil {
		t.Fatalf("ParseOrder: %v", err)
	}
	if br.Status != "filled" || br.CummulativeQuoteQty != 5250 {
		t.Fatalf("bad order: %+v", br)
	}
	acct, err := (okxAdapter{}).ParseAccount(json.RawMessage(`{"code":"0","data":[{"details":[{"ccy":"USDT","availBal":"500"},{"ccy":"ETH","availBal":"2"}]}]}`))
	if err != nil {
		t.Fatalf("ParseAccount: %v", err)
	}
	if acct.QuoteCash != 500 || acct.Holdings["ETH-USD"].Free != 2 {
		t.Fatalf("bad account: %+v", acct)
	}
}

func TestBybitAdapter(t *testing.T) {
	sp := 69000.0
	args, err := (bybitAdapter{}).TranslateOrder(&Order{ID: "o-bybit", Symbol: "BTC-USD", AssetClass: "crypto", Side: "sell", Type: "stop", Qty: 0.05, StopPrice: &sp})
	if err != nil {
		t.Fatalf("TranslateOrder: %v", err)
	}
	mustEq(t, args, "symbol", "BTCUSDT")
	mustEq(t, args, "orderType", "Market")
	mustEq(t, args, "triggerPrice", "69000")
	if args["triggerDirection"] != 2 {
		t.Fatalf("triggerDirection = %v", args["triggerDirection"])
	}

	br, err := (bybitAdapter{}).ParseOrder(json.RawMessage(`{"retCode":0,"retMsg":"OK","result":{"list":[{"orderId":"by-1","orderLinkId":"o-bybit","orderStatus":"Filled","cumExecQty":"0.05","cumExecValue":"3450"}]}}`))
	if err != nil {
		t.Fatalf("ParseOrder: %v", err)
	}
	if br.Status != "filled" || br.CummulativeQuoteQty != 3450 {
		t.Fatalf("bad order: %+v", br)
	}
	acct, err := (bybitAdapter{}).ParseAccount(json.RawMessage(`{"retCode":0,"result":{"list":[{"coin":[{"coin":"USDT","walletBalance":"900"},{"coin":"BTC","walletBalance":"0.5"}]}]}}`))
	if err != nil {
		t.Fatalf("ParseAccount: %v", err)
	}
	if acct.QuoteCash != 900 || acct.Holdings["BTC-USD"].Free != 0.5 {
		t.Fatalf("bad account: %+v", acct)
	}
}

func TestBitstampAdapter(t *testing.T) {
	lp := 70000.0
	o := &Order{ID: "o-bitstamp", Symbol: "BTC-USD", AssetClass: "crypto", Side: "buy", Type: "limit", Qty: 0.1, LimitPrice: &lp, TIF: "ioc"}
	if tool := (bitstampAdapter{}).PlaceOrderTool(o); tool != "buy_limit_order" {
		t.Fatalf("PlaceOrderTool = %q", tool)
	}
	args, err := (bitstampAdapter{}).TranslateOrder(o)
	if err != nil {
		t.Fatalf("TranslateOrder: %v", err)
	}
	mustEq(t, args, "market_symbol", "btcusd")
	mustEq(t, args, "price", "70000")
	if args["ioc_order"] != true {
		t.Fatalf("ioc_order not set: %+v", args)
	}
	br, err := (bitstampAdapter{}).ParseOrder(json.RawMessage(`{"id":"bs-1","status":"Finished","amount":"0.1","price":"70000","transactions":[{"amount":"0.1","price":"70000","fee":"1.0"}]}`))
	if err != nil {
		t.Fatalf("ParseOrder: %v", err)
	}
	if br.Status != "filled" || br.CummulativeQuoteQty != 7000 || len(br.Fills) != 1 {
		t.Fatalf("bad order: %+v", br)
	}
	acct, err := (bitstampAdapter{}).ParseAccount(json.RawMessage(`{"usd_available":"1000","btc_available":"0.2","eth_available":"0"}`))
	if err != nil {
		t.Fatalf("ParseAccount: %v", err)
	}
	if acct.QuoteCash != 1000 || acct.Holdings["BTC-USD"].Free != 0.2 {
		t.Fatalf("bad account: %+v", acct)
	}
}

func TestGeminiSlugIsNotExchangeAdapter(t *testing.T) {
	if adapterBySlug("gemini") != nil {
		t.Fatal("gemini slug is Google Gemini in the integration catalog; do not register it as an exchange broker")
	}
}
