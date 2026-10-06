package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

type cryptoEnvironmentPlatform struct {
	*hardeningPlatform
	fields map[string]string
	err    error
}

func (p *cryptoEnvironmentPlatform) GetConnectionCredentials(int64) (*sdk.ConnectionCredentials, error) {
	if p.err != nil {
		return nil, p.err
	}
	return &sdk.ConnectionCredentials{Fields: p.fields}, nil
}

func TestCryptoPaperEnvironmentNeverUsesLiveTransport(t *testing.T) {
	for _, slug := range []string{"binance-trading", "kraken", "coinbase", "bybit", "bitstamp"} {
		t.Run(slug, func(t *testing.T) {
			calls := 0
			p := &hardeningPlatform{connections: []sdk.PlatformConnection{{ID: 7, AppSlug: slug, Status: "active"}}, execute: func(int64, string, map[string]any) (*sdk.ExecuteResult, error) {
				calls++
				return nil, errors.New("must not contact live broker")
			}}
			ctx := newHardeningCtx(t, p)
			out, err := (&App{}).toolPortfolioCreate(ctx, map[string]any{"name": "paper", "broker_slug": slug, "execution_environment": "broker_paper", "allowed_classes": []any{"crypto"}})
			if err != nil {
				t.Fatal(err)
			}
			raw, _ := json.Marshal(out)
			if !strings.Contains(string(raw), "broker_environment_mismatch") || calls != 0 {
				t.Fatalf("paper reached live transport: %s calls=%d", raw, calls)
			}
			id, err := dbCreatePortfolio(ctx.AppDB(), &Portfolio{ProjectID: "test-proj", Name: "old mislabeled account", Mode: "live", ExecutionEnvironment: "broker_paper", BrokerSlug: slug, StartingCash: 1000})
			if err != nil {
				t.Fatal(err)
			}
			if _, err := brokerFor(ctx, mustPortfolio(t, ctx, id)); err == nil {
				t.Fatal("existing mislabeled paper portfolio resolved")
			}
			if _, err := (&App{}).toolPortfolioBrokerBind(ctx, map[string]any{"portfolio_id": id, "connection_id": 7, "confirmation": "BIND BROKER ACCOUNT"}); err == nil {
				t.Fatal("binding bypassed environment verification")
			}
		})
	}
}

func TestOKXEnvironmentCheckedOnEveryResolution(t *testing.T) {
	p := &cryptoEnvironmentPlatform{hardeningPlatform: &hardeningPlatform{connections: []sdk.PlatformConnection{{ID: 7, AppSlug: "okx", Status: "active"}}}, fields: map[string]string{"simulated": "true"}}
	ctx := newHardeningCtx(t, p)
	id, err := dbCreatePortfolio(ctx.AppDB(), &Portfolio{ProjectID: "test-proj", Name: "demo", Mode: "live", ExecutionEnvironment: "broker_paper", BrokerSlug: "okx", StartingCash: 1000})
	if err != nil {
		t.Fatal(err)
	}
	pf := mustPortfolio(t, ctx, id)
	if _, err := brokerFor(ctx, pf); err != nil {
		t.Fatal(err)
	}
	p.fields["simulated"] = "false"
	if _, err := brokerFor(ctx, pf); err == nil {
		t.Fatal("same connection changed to live")
	}
	p.fields["demo"] = "y"
	if _, err := brokerFor(ctx, pf); err != nil {
		t.Fatal(err)
	}
	p.err = errors.New("credentials unavailable")
	if _, err := brokerFor(ctx, pf); err == nil {
		t.Fatal("unverified environment accepted")
	}
	for _, value := range []string{"tru", "paper", "maybe"} {
		if _, ok := okxExecutionEnvironment(map[string]string{"simulated": value}); ok {
			t.Fatal("invalid flag accepted")
		}
	}
}

func TestRecoveryCapabilitiesUseRegisteredSlugs(t *testing.T) {
	for _, slug := range []string{"alpaca-trading", "binance-trading", "okx", "bybit", "bitstamp"} {
		a := adapterBySlug(slug)
		if a == nil || !recoverableByClientID(slug) {
			t.Fatalf("%s not recoverable", slug)
		}
		o := &Order{ID: "o-recover", Symbol: "BTC-USD", AssetClass: "crypto", Side: "buy", Type: "limit", Qty: 1}
		price := 100.0
		o.LimitPrice = &price
		placed, err := a.TranslateOrder(o)
		if err != nil {
			t.Fatal(err)
		}
		status := a.StatusArgs(o, "")
		keys := map[string][2]string{"alpaca-trading": {"client_order_id", "client_order_id"}, "binance-trading": {"newClientOrderId", "origClientOrderId"}, "okx": {"clOrdId", "clOrdId"}, "bybit": {"orderLinkId", "orderLinkId"}, "bitstamp": {"client_order_id", "client_order_id"}}
		k := keys[slug]
		if placed[k[0]] != status[k[1]] {
			t.Fatalf("%s client ID mismatch: %v %v", slug, placed, status)
		}
	}
	for _, slug := range []string{"coinbase", "kraken", "okx-trading"} {
		if recoverableByClientID(slug) {
			t.Fatalf("unimplemented recovery claimed for %s", slug)
		}
	}
}

func TestAlpacaAdvertisedOrderTypesAreTranslatable(t *testing.T) {
	a := alpacaAdapter{}
	price := 100.0
	for _, kind := range a.Capabilities().OrderTypes {
		if _, err := a.TranslateOrder(&Order{ID: "o", Symbol: "AAPL", AssetClass: "equity", Side: "buy", Type: kind, Qty: 1, LimitPrice: &price, StopPrice: &price}); err != nil {
			t.Fatalf("advertised %s: %v", kind, err)
		}
	}
}

func TestCryptoOrderHistoryNormalization(t *testing.T) {
	fixtures := map[string]string{
		"binance-trading": `[{"orderId":9007199254740993,"symbol":"BTCUSDT","clientOrderId":"client","side":"BUY","type":"LIMIT","origQty":"2","executedQty":"1","cummulativeQuoteQty":"100","price":"100","status":"PARTIALLY_FILLED","time":1700000000000}]`,
		"kraken":          `{"error":[],"result":{"open":{"K-1":{"status":"open","vol":"2","vol_exec":"1","price":"100","descr":{"pair":"XBTUSD","type":"buy","ordertype":"limit","price":"100"}}}}}`,
		"coinbase":        `{"orders":[{"order_id":"C-1","product_id":"BTC-USD","side":"BUY","order_type":"LIMIT","status":"OPEN","filled_size":"1","average_filled_price":"100","order_configuration":{"limit_limit_gtc":{"base_size":"2","limit_price":"100"}}}]}`,
		"okx":             `{"code":"0","data":[{"ordId":"O-1","instId":"BTC-USDT","side":"buy","ordType":"limit","state":"partially_filled","sz":"2","accFillSz":"1","avgPx":"100","px":"100"}]}`,
		"bybit":           `{"retCode":0,"result":{"list":[{"orderId":"B-1","symbol":"BTCUSDT","side":"Buy","orderType":"Limit","orderStatus":"PartiallyFilled","qty":"2","cumExecQty":"1","avgPrice":"100","price":"100"}]}}`,
		"bitstamp":        `[{"id":"S-1","currency_pair":"BTC/USD","type":"0","amount":"2","price":"100"}]`,
	}
	for slug, raw := range fixtures {
		t.Run(slug, func(t *testing.T) {
			rows, err := adapterBySlug(slug).ParseOrders(json.RawMessage(raw))
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) != 1 || rows[0].Symbol != "BTC-USD" || rows[0].Qty != 2 || rows[0].Status != "working" {
				t.Fatalf("bad history: %+v", rows)
			}
			if slug != "bitstamp" && (rows[0].FilledQty != 1 || rows[0].AvgFillPrice != 100) {
				t.Fatalf("lost partial fill: %+v", rows)
			}
			if slug == "binance-trading" && rows[0].BrokerOrderID != "9007199254740993" {
				t.Fatal("ID lost precision")
			}
			if _, err := adapterBySlug(slug).ParseOrders(json.RawMessage(`{}`)); err == nil {
				t.Fatal("malformed list silently accepted")
			}
		})
	}
}

func TestCryptoHistoryPaginationAndAccountScopedImport(t *testing.T) {
	calls := 0
	p := &hardeningPlatform{execute: func(_ int64, tool string, args map[string]any) (*sdk.ExecuteResult, error) {
		calls++
		if tool != "list_orders" {
			t.Fatalf("unexpected %s", tool)
		}
		id, cursor, more := "first", "next", true
		if args["cursor"] == "next" {
			id, cursor, more = "second", "", false
		}
		raw, _ := json.Marshal(map[string]any{"has_next": more, "cursor": cursor, "orders": []any{map[string]any{"order_id": id, "product_id": "BTC-USD", "side": "BUY", "order_type": "LIMIT", "status": "FILLED", "filled_size": "1", "average_filled_price": "100", "order_configuration": map[string]any{"limit_limit_gtc": map[string]any{"base_size": "1", "limit_price": "100"}}}}})
		return &sdk.ExecuteResult{Success: true, Data: raw}, nil
	}}
	ctx := newHardeningCtx(t, p)
	a := mustCreatePortfolio(t, ctx, "first account", []string{"crypto"})
	b := mustCreatePortfolio(t, ctx, "second account", []string{"crypto"})
	bb := &boundBroker{Adapter: coinbaseAdapter{}, ConnectionID: 7}
	for _, id := range []int64{a, b} {
		before := mustPortfolio(t, ctx, id).Cash
		if n := importBrokerOrders(ctx, "test-proj", id, bb, "list_orders", nil, "backfill"); n != 2 {
			t.Fatalf("imported %d", n)
		}
		if n := importBrokerOrders(ctx, "test-proj", id, bb, "list_orders", nil, "backfill"); n != 0 {
			t.Fatalf("duplicate import %d", n)
		}
		if mustPortfolio(t, ctx, id).Cash != before {
			t.Fatal("history debited cash again")
		}
	}
	if calls != 8 {
		t.Fatalf("pagination calls=%d", calls)
	}
	var count int
	ctx.AppDB().QueryRow(`SELECT count(*) FROM fills`).Scan(&count)
	if count != 4 {
		t.Fatalf("fills=%d", count)
	}
	o := &Order{ID: "o-imported", Symbol: "BTC-USD"}
	args := (binanceAdapter{}).CancelArgs(o, "9007199254740993")
	if args["orderId"] != json.Number("9007199254740993") || args["origClientOrderId"] != nil {
		t.Fatalf("wrong imported cancellation: %v", args)
	}
}

func TestBybitLostSubmissionRecoversFromHistory(t *testing.T) {
	calls := []string{}
	p := &hardeningPlatform{connections: []sdk.PlatformConnection{{ID: 7, AppSlug: "bybit", Status: "active"}}, execute: func(_ int64, tool string, args map[string]any) (*sdk.ExecuteResult, error) {
		calls = append(calls, tool)
		if tool == "list_orders" {
			return &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"retCode":0,"result":{"list":[]}}`)}, nil
		}
		if tool != "list_order_history" || args["orderLinkId"] != "o-lost" {
			t.Fatalf("unexpected recovery call %s %v", tool, args)
		}
		return &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"retCode":0,"result":{"list":[{"orderId":"B-recovered","orderLinkId":"o-lost","orderStatus":"New","cumExecQty":"0"}]}}`)}, nil
	}}
	ctx := newHardeningCtx(t, p)
	id, err := dbCreatePortfolio(ctx.AppDB(), &Portfolio{ProjectID: "test-proj", Name: "recover", Mode: "live", ExecutionEnvironment: "broker_live", BrokerSlug: "bybit", AllowedClasses: []string{"crypto"}, StartingCash: 1000})
	if err != nil {
		t.Fatal(err)
	}
	o := auditOrder(t, ctx, id, "o-lost", "buy", 0.001)
	if err := tryReconcile(globalEngine, mustPortfolio(t, ctx, id), o); err != nil {
		t.Fatal(err)
	}
	got, err := dbBrokerOrderIDFor(ctx.AppDB(), o.ID)
	if err != nil || got != "B-recovered" {
		t.Fatalf("recovery ID=%s %v", got, err)
	}
	if len(calls) != 2 {
		t.Fatalf("calls=%v", calls)
	}
}

func TestAlpacaLostSubmissionUsesClientLookupEndpoint(t *testing.T) {
	var called string
	p := &integrityEnvironmentPlatform{hardeningPlatform: &hardeningPlatform{connections: []sdk.PlatformConnection{{ID: 7, AppSlug: "alpaca-trading", Status: "active"}}, execute: func(_ int64, tool string, args map[string]any) (*sdk.ExecuteResult, error) {
		called = tool
		if tool != "get_order_by_client_order_id" || args["client_order_id"] != "o-alpaca-lost" || args["order_id"] != nil {
			t.Fatalf("wrong recovery endpoint %s %v", tool, args)
		}
		return &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"id":"alpaca-recovered","client_order_id":"o-alpaca-lost","status":"accepted","filled_qty":"0"}`)}, nil
	}}, host: "paper-api.alpaca.markets"}
	ctx := newHardeningCtx(t, p)
	id, err := dbCreatePortfolio(ctx.AppDB(), &Portfolio{ProjectID: "test-proj", Name: "Alpaca recovery", Mode: "live", ExecutionEnvironment: "broker_paper", BrokerSlug: "alpaca-trading", AllowedClasses: []string{"crypto"}, StartingCash: 1000})
	if err != nil {
		t.Fatal(err)
	}
	o := auditOrder(t, ctx, id, "o-alpaca-lost", "buy", 0.001)
	if err := tryReconcile(globalEngine, mustPortfolio(t, ctx, id), o); err != nil {
		t.Fatal(err)
	}
	got, _ := dbBrokerOrderIDFor(ctx.AppDB(), o.ID)
	if called == "" || got != "alpaca-recovered" {
		t.Fatalf("lookup not saved: %s", got)
	}
}

func TestHistoryImportRollsBackIncompleteFill(t *testing.T) {
	ctx := newTestCtx(t)
	id := mustCreatePortfolio(t, ctx, "atomic import", []string{"crypto"})
	_, err := ctx.AppDB().Exec(`CREATE TRIGGER reject_import_fill BEFORE INSERT ON fills BEGIN SELECT RAISE(ABORT,'test fill failure'); END`)
	if err != nil {
		t.Fatal(err)
	}
	row := brokerHistoricOrder{BrokerOrderID: "external", Symbol: "BTC-USD", AssetClass: "crypto", Side: "buy", Type: "limit", TIF: "gtc", Qty: 1, FilledQty: 1, AvgFillPrice: 100, Status: "filled"}
	err = insertImportedBrokerOrder(ctx.AppDB(), "test-proj", id, "o-import-fail", "history", "broker_backfill:live", &boundBroker{Adapter: coinbaseAdapter{}, ConnectionID: 7}, row)
	if err == nil {
		t.Fatal("fill failure ignored")
	}
	var count int
	ctx.AppDB().QueryRow(`SELECT count(*) FROM orders WHERE id='o-import-fail'`).Scan(&count)
	if count != 0 {
		t.Fatal("partially committed imported order")
	}
}

func TestHistoryPaginationRejectsStalledCursor(t *testing.T) {
	p := &hardeningPlatform{execute: func(int64, string, map[string]any) (*sdk.ExecuteResult, error) {
		return &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"orders":[],"has_next":true,"cursor":"same"}`)}, nil
	}}
	ctx := newHardeningCtx(t, p)
	if _, err := fetchBrokerHistory(ctx, &boundBroker{Adapter: coinbaseAdapter{}, ConnectionID: 7}, "list_orders", nil); err == nil {
		t.Fatal("stalled cursor silently accepted")
	}
}
