package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"
)

func coinbaseFixtureProduct(symbol string) string {
	return fmt.Sprintf(`{"id":%q,"base_currency":%q,"quote_currency":"USD","quote_increment":"0.01","base_increment":"0.00000001","min_market_funds":"1","status":"online"}`, symbol, strings.TrimSuffix(symbol, "-USD"))
}

func coinbaseFixtureTicker() string {
	return fmt.Sprintf(`{"price":"100.5","bid":"100","ask":"101","size":"2","volume":"2","time":%q}`, time.Now().UTC().Format(time.RFC3339Nano))
}

func TestCoinbaseUSDQuotesPreserveIdentityAndVolumeUnits(t *testing.T) {
	var paths []string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		paths = append(paths, r.URL.Path)
		if strings.Contains(r.URL.Path, "USDT") || r.Header.Get("Authorization") != "" {
			t.Errorf("unexpected stablecoin pair or authentication: %s", r.URL.Path)
		}
		if strings.HasSuffix(r.URL.Path, "/ticker") {
			fmt.Fprint(w, coinbaseFixtureTicker())
		} else {
			fmt.Fprint(w, coinbaseFixtureProduct(strings.TrimPrefix(r.URL.Path, "/products/")))
		}
	}))
	defer srv.Close()
	c := &coinbasePublic{base: srv.URL, client: srv.Client()}
	for _, symbol := range []string{"BTC-USD", "ETH-USD", "BTC-USD"} {
		mark, err := c.Quote(symbol)
		if err != nil {
			t.Fatal(err)
		}
		if mark.Symbol != symbol || mark.Source != coinbasePublicSource || mark.Price != 100.5 ||
			mark.Instrument.ProviderSymbol != symbol || mark.Instrument.QuoteCurrency != "USD" ||
			mark.Instrument.Exchange != "COINBASE" || mark.Instrument.TickSize != 0.01 || mark.Instrument.LotSize != 1e-8 || mark.Instrument.MinQty != 1e-8 {
			t.Fatalf("incorrect USD quote: %#v instrument=%#v", mark, mark.Instrument)
		}
		if mark.VolumeUnit != "base_currency" || *mark.Volume24h != 2 || *mark.LastTradeSize != 2 ||
			*mark.BidPrice != 100 || *mark.AskPrice != 101 || mark.QuoteAt == "" || mark.TimestampKind != "exchange" {
			t.Fatalf("incorrect ticker semantics: %#v", mark)
		}
	}
	if len(paths) != 5 { // two product metadata requests and three tickers
		t.Fatalf("metadata cache not used: %v", paths)
	}
}

func TestCoinbaseRejectsNonUSDOrUnavailableProducts(t *testing.T) {
	for _, fixture := range []string{
		`{"id":"BTC-USD","base_currency":"BTC","quote_currency":"USDT","status":"online"}`,
		`{"id":"ETH-USD","base_currency":"ETH","quote_currency":"USD","status":"online"}`,
		`{"id":"BTC-USD","base_currency":"BTC","quote_currency":"USD","status":"offline"}`,
		`{"id":"BTC-USD","base_currency":"BTC","quote_currency":"USD","status":"online","trading_disabled":true}`,
	} {
		t.Run(fixture, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path != "/products/BTC-USD" {
					t.Errorf("unvalidated product used for data: %s", r.URL.Path)
				}
				fmt.Fprint(w, fixture)
			}))
			defer srv.Close()
			c := &coinbasePublic{base: srv.URL, client: srv.Client()}
			if m, err := c.Quote("BTC-USD"); err == nil || m != nil {
				t.Fatalf("invalid product produced quote: %#v, %v", m, err)
			}
			if bars, err := c.BacktestBars("BTC-USD", "1h", time.Now().Add(-time.Hour), time.Now(), 2); err == nil || bars != nil {
				t.Fatalf("invalid product produced history: %#v, %v", bars, err)
			}
		})
	}
}

func TestCoinbaseRejectsInvalidTickerAndSymbols(t *testing.T) {
	for _, symbol := range []string{"BTC-USDT", "BTCUSD", "-USD", "BTC/ETH-USD", "BTC?x-USD"} {
		if _, err := coinbaseUSDProduct(symbol); err == nil {
			t.Fatalf("invalid symbol accepted: %s", symbol)
		}
	}
	for _, replacement := range []string{`"price":"NaN"`, `"price":"0"`, `"price":"Inf"`} {
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if strings.HasSuffix(r.URL.Path, "/ticker") {
				fmt.Fprint(w, strings.Replace(coinbaseFixtureTicker(), `"price":"100.5"`, replacement, 1))
			} else {
				fmt.Fprint(w, coinbaseFixtureProduct("BTC-USD"))
			}
		}))
		c := &coinbasePublic{base: srv.URL, client: srv.Client()}
		if m, err := c.Quote("BTC-USD"); err == nil || m != nil {
			t.Fatalf("invalid ticker accepted: %#v %v", m, err)
		}
		srv.Close()
	}
}

func TestCoinbaseHistoryPaginatesSortsAndFiltersUSDData(t *testing.T) {
	start := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	end := start.Add(300 * time.Hour)
	pages := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/products/BTC-USD" {
			fmt.Fprint(w, coinbaseFixtureProduct("BTC-USD"))
			return
		}
		if r.URL.Path != "/products/BTC-USD/candles" || r.URL.Query().Get("granularity") != "3600" {
			t.Errorf("wrong candle request: %s", r.URL)
		}
		pages++
		from, _ := time.Parse(time.RFC3339Nano, r.URL.Query().Get("start"))
		to, _ := time.Parse(time.RFC3339Nano, r.URL.Query().Get("end"))
		if to.Sub(from) > 299*time.Hour {
			t.Errorf("request exceeds Coinbase candle limit: %s", r.URL)
		}
		var rows [][]float64
		for at := to; !at.Before(from.Add(-time.Hour)); at = at.Add(-time.Hour) {
			rows = append(rows, []float64{float64(at.Unix()), 99, 102, 100, 101, 2})
		}
		json.NewEncoder(w).Encode(rows)
	}))
	defer srv.Close()
	c := &coinbasePublic{base: srv.URL, client: srv.Client()}
	bars, err := c.BacktestBars("BTC-USD", "1h", start, end, 301)
	if err != nil || len(bars) != 301 || pages != 2 {
		t.Fatalf("bars=%d pages=%d err=%v", len(bars), pages, err)
	}
	if bars[0].T != start.Unix() || bars[300].T != end.Unix() || bars[0].O != 100 || bars[0].L != 99 || bars[0].V != 2 {
		t.Fatalf("wrong candle range/shape: first=%#v last=%#v", bars[0], bars[300])
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.BacktestBarsContext(ctx, "BTC-USD", "1h", start, end, 301); err == nil {
		t.Fatal("cancelled history request succeeded")
	}
}

func TestLiveProviderUSDStrategyBarsUseOnlyClosedCandles(t *testing.T) {
	fixedNow := time.Date(2026, 10, 27, 10, 2, 0, 0, time.UTC)
	boundary := fixedNow.Truncate(time.Hour)
	start := boundary.Add(-121 * time.Hour)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/products/BTC-USD" {
			fmt.Fprint(w, coinbaseFixtureProduct("BTC-USD"))
			return
		}
		from, _ := time.Parse(time.RFC3339Nano, r.URL.Query().Get("start"))
		to, _ := time.Parse(time.RFC3339Nano, r.URL.Query().Get("end"))
		if !from.Equal(start) || !to.Equal(boundary.Add(-time.Millisecond)) {
			t.Errorf("unexpected closed candle bounds: %s to %s", from, to)
		}
		var rows [][]float64
		for at := start; !at.After(boundary); at = at.Add(time.Hour) {
			rows = append(rows, []float64{float64(at.Unix()), 99, 102, 100, 101, 2})
		}
		json.NewEncoder(w).Encode(rows)
	}))
	defer srv.Close()
	p := newLiveProvider()
	p.crypto = &coinbasePublic{base: srv.URL, client: srv.Client()}
	p.now = func() time.Time { return fixedNow }
	bars, err := p.StrategyBars("BTC-USD", "1h", 121)
	if err != nil || len(bars) != 121 || bars[120].T != boundary.Add(-time.Hour).Unix() {
		t.Fatalf("closed candles=%d err=%v", len(bars), err)
	}
}

func TestMarketToolsReplacePersistedUSDTMetadata(t *testing.T) {
	ctx := newTestCtx(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/products/BTC-USD":
			fmt.Fprint(w, coinbaseFixtureProduct("BTC-USD"))
		case "/products/BTC-USD/ticker":
			fmt.Fprint(w, coinbaseFixtureTicker())
		case "/products/BTC-USD/candles":
			at := time.Now().UTC().Truncate(time.Hour).Unix()
			fmt.Fprintf(w, `[[%d,99,102,100,101,2]]`, at)
		default:
			t.Errorf("unexpected endpoint: %s", r.URL)
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	p := newLiveProvider()
	p.crypto = &coinbasePublic{base: srv.URL, client: srv.Client()}
	old := globalEngine
	globalEngine = &engine{db: ctx.AppDB(), provider: p}
	t.Cleanup(func() { globalEngine = old })
	i := defaultInstrument("BTC-USD", "crypto", "binance-public", time.Now())
	i.QuoteCurrency = "USDT"
	i.ProviderSymbol = "BTCUSDT"
	if err := dbUpsertInstrumentExec(ctx.AppDB(), i); err != nil {
		t.Fatal(err)
	}
	if err := dbUpsertMark(ctx.AppDB(), &Mark{Symbol: "BTC-USD", AssetClass: "crypto", Price: 99, Source: "binance-public", Instrument: i, MarkedAt: time.Now().Add(10 * time.Second).UTC().Format(time.RFC3339Nano)}); err != nil {
		t.Fatal(err)
	}
	a := &App{}
	quote, err := a.toolMarketQuote(ctx, map[string]any{"symbol": "BTC-USD"})
	if err != nil {
		t.Fatal(err)
	}
	q := quote.(map[string]any)
	if q["source"] != coinbasePublicSource || q["instrument"].(*Instrument).QuoteCurrency != "USD" || q["bid_price"] != 100.0 || q["ask_price"] != 101.0 {
		t.Fatalf("legacy quote retained: %#v", q)
	}
	stored, err := dbGetMark(ctx.AppDB(), "BTC-USD")
	if err != nil || stored.Source != coinbasePublicSource || stored.Instrument.QuoteCurrency != "USD" {
		t.Fatalf("persisted legacy quote retained: %#v err=%v", stored, err)
	}
	history, err := a.toolMarketHistory(ctx, map[string]any{"symbol": "BTC-USD", "range": "5D"})
	if err != nil {
		t.Fatal(err)
	}
	h := history.(map[string]any)
	if h["source"] != coinbasePublicSource || h["volume_unit"] != "base_currency" || h["instrument"].(*Instrument).ProviderSymbol != "BTC-USD" {
		t.Fatalf("incorrect history provenance: %#v", h)
	}
}

func TestSimulationRejectsUSDTPriceForUSDSymbol(t *testing.T) {
	i := defaultInstrument("BTC-USD", "crypto", "binance-public", time.Now())
	i.QuoteCurrency = "USDT"
	v := validateExecutionOrder(defaultVenueProfile("simulation", "crypto"), i, &Mark{Price: 100}, 1, 100)
	if v == nil || v.Code != "quote_currency_mismatch" {
		t.Fatalf("USDT quote accepted for USD simulation: %#v", v)
	}
}

func TestRealCoinbaseUSDFeed(t *testing.T) {
	if os.Getenv("RUN_TRADING_PROVIDER_TESTS") != "1" {
		t.Skip("set RUN_TRADING_PROVIDER_TESTS=1 for Coinbase USD network smoke test")
	}
	c := newCoinbasePublic()
	for _, symbol := range []string{"BTC-USD", "ETH-USD"} {
		m, err := c.Quote(symbol)
		if err != nil {
			t.Fatal(err)
		}
		bars, err := c.Bars(symbol, "5D")
		if err != nil || len(bars) < 100 || m.Instrument.QuoteCurrency != "USD" || m.Instrument.ProviderSymbol != symbol || !markFresh(m, time.Now()) {
			t.Fatalf("USD feed smoke: mark=%#v bars=%d err=%v", m, len(bars), err)
		}
		t.Logf("%s source=%s price=%v bid=%v ask=%v bars=%d", symbol, m.Source, m.Price, *m.BidPrice, *m.AskPrice, len(bars))
	}
}
