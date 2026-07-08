package main

import (
	"encoding/json"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestKrakenSymbolMapping(t *testing.T) {
	cases := map[string]string{
		"BTC-USD": "XBTUSD",
		"eth-usd": "ETHUSD",
		"SOL-USD": "SOLUSD",
		"XBTUSD":  "XBTUSD",
		"BTC/USD": "XBTUSD",
	}
	for in, want := range cases {
		if got := toKrakenSymbol(in); got != want {
			t.Errorf("toKrakenSymbol(%q) = %q, want %q", in, got, want)
		}
	}
	reverse := map[string]string{
		"XBTUSD":   "BTC-USD",
		"XXBTZUSD": "BTC-USD",
		"ETHUSD":   "ETH-USD",
		"XETHZUSD": "ETH-USD",
		"SOLUSD":   "SOL-USD",
	}
	for in, want := range reverse {
		if got := fromKrakenSymbol(in); got != want {
			t.Errorf("fromKrakenSymbol(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestKrakenTranslateOrderMarket(t *testing.T) {
	o := &Order{ID: "o-abc123def456", Symbol: "BTC-USD", AssetClass: "crypto", Side: "buy", Type: "market", Qty: 0.01}
	args, err := (krakenAdapter{}).TranslateOrder(o)
	if err != nil {
		t.Fatalf("TranslateOrder: %v", err)
	}
	mustEq(t, args, "pair", "XBTUSD")
	mustEq(t, args, "type", "buy")
	mustEq(t, args, "ordertype", "market")
	mustEq(t, args, "volume", "0.01")
	if _, ok := args["timeinforce"]; ok {
		t.Fatalf("market order should not include timeinforce")
	}
	if _, ok := args["userref"].(int32); !ok {
		t.Fatalf("userref should be int32, got %T", args["userref"])
	}
}

func TestKrakenTranslateOrderLimitAndStop(t *testing.T) {
	lp := 2500.5
	limit := &Order{ID: "o-limit", Symbol: "ETH-USD", AssetClass: "crypto", Side: "sell", Type: "limit", Qty: 0.25, LimitPrice: &lp, TIF: "day"}
	args, err := (krakenAdapter{}).TranslateOrder(limit)
	if err != nil {
		t.Fatalf("limit TranslateOrder: %v", err)
	}
	mustEq(t, args, "pair", "ETHUSD")
	mustEq(t, args, "ordertype", "limit")
	mustEq(t, args, "price", "2500.5")
	mustEq(t, args, "timeinforce", "GTC")

	sp := 2350.0
	stop := &Order{ID: "o-stop", Symbol: "ETH-USD", AssetClass: "crypto", Side: "sell", Type: "stop", Qty: 0.25, StopPrice: &sp, TIF: "ioc"}
	args, err = (krakenAdapter{}).TranslateOrder(stop)
	if err != nil {
		t.Fatalf("stop TranslateOrder: %v", err)
	}
	mustEq(t, args, "ordertype", "stop-loss")
	mustEq(t, args, "price", "2350")
	mustEq(t, args, "timeinforce", "IOC")
}

func TestKrakenParseAddOrder(t *testing.T) {
	raw := json.RawMessage(`{"error":[],"result":{"descr":{"order":"buy 0.01000000 XBTUSD @ market"},"txid":["OABCDEF-GHIJK-LMNOPQ"]}}`)
	br, err := (krakenAdapter{}).ParseOrder(raw)
	if err != nil {
		t.Fatalf("ParseOrder: %v", err)
	}
	if br.BrokerOrderID != "OABCDEF-GHIJK-LMNOPQ" || br.Status != "working" {
		t.Fatalf("bad parsed add order: %+v", br)
	}
}

func TestKrakenParseQueryOrderFilled(t *testing.T) {
	raw := json.RawMessage(`{"error":[],"result":{"OABCDEF-GHIJK-LMNOPQ":{"status":"closed","vol":"0.01000000","vol_exec":"0.01000000","cost":"690.25","price":"69025.0","fee":"1.25","descr":{"pair":"XXBTZUSD","type":"buy","ordertype":"market"}}}}`)
	br, err := (krakenAdapter{}).ParseOrder(raw)
	if err != nil {
		t.Fatalf("ParseOrder: %v", err)
	}
	if br.Status != "filled" || br.ExecutedQty != 0.01 || br.CummulativeQuoteQty != 690.25 {
		t.Fatalf("bad parsed query order: %+v", br)
	}
	if len(br.Fills) != 0 {
		t.Fatalf("query status should not synthesize fills, got %+v", br.Fills)
	}
}

func TestKrakenParseAccount(t *testing.T) {
	raw := json.RawMessage(`{"error":[],"result":{"ZUSD":"1000.50","USDT":"25.00","XXBT":"0.125","XETH":"2.5","SOL":"0"}}`)
	acct, err := (krakenAdapter{}).ParseAccount(raw)
	if err != nil {
		t.Fatalf("ParseAccount: %v", err)
	}
	if acct.QuoteCash != 1025.5 {
		t.Fatalf("quote cash = %v", acct.QuoteCash)
	}
	if btc := acct.Holdings["BTC-USD"]; btc.Free != 0.125 {
		t.Fatalf("BTC holding wrong: %+v", btc)
	}
	if eth := acct.Holdings["ETH-USD"]; eth.Free != 2.5 {
		t.Fatalf("ETH holding wrong: %+v", eth)
	}
}

func TestKrakenErrorText(t *testing.T) {
	res := &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"error":["EOrder:Insufficient funds"]}`)}
	code, detail := (krakenAdapter{}).ErrText(res, nil)
	if code != "kraken_EOrder" || detail != "EOrder:Insufficient funds" {
		t.Fatalf("ErrText = %q, %q", code, detail)
	}
}
