package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestNormalizeMarkAddsExplicitTimestampAndVolumeSemantics(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	volume := 42.0
	mark, err := normalizeMark("test-feed", &Mark{
		Symbol: " btc-usd ", AssetClass: "crypto", Price: 100, Volume24h: &volume,
	}, now)
	if err != nil {
		t.Fatal(err)
	}
	if mark.Symbol != "BTC-USD" || mark.TimestampKind != "received" || mark.MarkedAt != now.Format(time.RFC3339Nano) {
		t.Fatalf("normalized mark = %#v", mark)
	}
	if mark.Source != "test-feed" || mark.VolumeUnit != "quote_currency" || mark.Instrument == nil {
		t.Fatalf("missing provenance/metadata: %#v", mark)
	}
	if mark.Instrument.Calendar != calendarAlwaysOpen || mark.Instrument.ExchangeTimezone != "UTC" {
		t.Fatalf("instrument = %#v", mark.Instrument)
	}
}

func TestNormalizeMarkRejectsInvalidOrFutureData(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	if _, err := normalizeMark("bad", &Mark{Symbol: "AAPL", AssetClass: "equity", Price: -1}, now); err == nil {
		t.Fatal("negative price passed quality gate")
	}
	if _, err := normalizeMark("bad", &Mark{
		Symbol: "AAPL", AssetClass: "equity", Price: 100,
		MarkedAt: now.Add(2 * time.Minute).Format(time.RFC3339),
	}, now); err == nil {
		t.Fatal("future timestamp passed quality gate")
	}
}

func TestNormalizeBarsDeduplicatesAndRejectsContradictoryCandles(t *testing.T) {
	got, err := normalizeBars("AAPL", "test", []Bar{
		{T: 2, O: 10, H: 12, L: 9, C: 11, V: 4},
		{T: 1, C: 9},
		{T: 2, O: 10, H: 13, L: 9, C: 12, V: 5},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[0].T != 1 || got[0].O != 9 || got[1].C != 12 {
		t.Fatalf("normalized bars = %#v", got)
	}
	if _, err := normalizeBars("AAPL", "test", []Bar{{T: 1, O: 10, H: 9, L: 8, C: 11}}); err == nil {
		t.Fatal("contradictory OHLC candle passed quality gate")
	}
}

func TestPublicGETRetriesTransientFailuresAndNotPermanentOnes(t *testing.T) {
	var transientRequests atomic.Int32
	transient := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if transientRequests.Add(1) < 3 {
			http.Error(w, "temporary", http.StatusServiceUnavailable)
			return
		}
		_, _ = w.Write([]byte(`{"ok":true}`))
	}))
	defer transient.Close()
	raw, err := publicGET(context.Background(), transient.Client(), "test", transient.URL, 1024, nil)
	if err != nil || string(raw) != `{"ok":true}` || transientRequests.Load() != 3 {
		t.Fatalf("raw=%s requests=%d err=%v", raw, transientRequests.Load(), err)
	}

	var permanentRequests atomic.Int32
	permanent := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		permanentRequests.Add(1)
		http.Error(w, "bad symbol", http.StatusBadRequest)
	}))
	defer permanent.Close()
	if _, err := publicGET(context.Background(), permanent.Client(), "test", permanent.URL, 1024, nil); err == nil {
		t.Fatal("permanent HTTP failure unexpectedly succeeded")
	}
	if permanentRequests.Load() != 1 {
		t.Fatalf("permanent failure requests = %d, want 1", permanentRequests.Load())
	}
}

func TestInstrumentPersistsWithMarkAndCalendarIsQueryable(t *testing.T) {
	ctx := newTestCtx(t)
	mark := &Mark{
		Symbol: "QQQ", AssetClass: "etf", Price: 500,
		MarkedAt: time.Date(2026, 8, 3, 15, 0, 0, 0, time.UTC).Format(time.RFC3339),
		Source:   "test-feed", Instrument: &Instrument{
			Symbol: "QQQ", AssetClass: "etf", Exchange: "NASDAQ", ExchangeTimezone: "America/New_York",
			Calendar: calendarUSEquity, QuoteCurrency: "USD", VolumeUnit: "shares", Active: true,
		},
	}
	if err := dbUpsertMark(ctx.AppDB(), mark); err != nil {
		t.Fatal(err)
	}
	stored, err := dbGetInstrument(ctx.AppDB(), "QQQ")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Exchange != "NASDAQ" || stored.Calendar != calendarUSEquity || stored.VolumeUnit != "shares" {
		t.Fatalf("stored instrument = %#v", stored)
	}
	out, err := (&App{}).toolMarketCalendar(ctx, map[string]any{
		"symbol": "QQQ", "at": "2026-08-03T15:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	session := out.(map[string]any)["session"].(*MarketSession)
	if !session.OpenDay || !session.IsOpen || session.Timezone != "America/New_York" {
		t.Fatalf("session = %#v", session)
	}
}

func TestRealPublicMarketDataSmoke(t *testing.T) {
	if os.Getenv("RUN_TRADING_PROVIDER_TESTS") != "1" {
		t.Skip("set RUN_TRADING_PROVIDER_TESTS=1 for real public-provider smoke tests")
	}

	t.Run("binance", func(t *testing.T) {
		mark, err := newBinancePublic().Quote("BTC-USD")
		if err != nil {
			t.Fatal(err)
		}
		mark, err = normalizeMark("binance-public", mark, time.Now().UTC())
		if err != nil || mark.Price <= 0 || mark.Volume24h == nil || mark.Instrument.ProviderSymbol != "BTCUSDT" {
			t.Fatalf("mark=%#v err=%v", mark, err)
		}
		t.Logf("BTC-USD price=%v 24h_quote_volume=%v", mark.Price, *mark.Volume24h)
	})

	t.Run("yahoo", func(t *testing.T) {
		mark, err := newYahooPublic().Quote("AAPL")
		if err != nil {
			t.Fatal(err)
		}
		mark, err = normalizeMark("yahoo-finance", mark, time.Now().UTC())
		if err != nil || mark.Price <= 0 || mark.Instrument == nil || mark.Instrument.Calendar != calendarUSEquity {
			t.Fatalf("mark=%#v err=%v", mark, err)
		}
		if strings.TrimSpace(mark.Instrument.Exchange) == "" || strings.TrimSpace(mark.Instrument.QuoteCurrency) == "" {
			t.Fatalf("incomplete Yahoo metadata: %#v", mark.Instrument)
		}
		t.Logf("AAPL price=%v exchange=%s timezone=%s", mark.Price, mark.Instrument.Exchange, mark.Instrument.ExchangeTimezone)
	})

	t.Run("polymarket", func(t *testing.T) {
		marks, err := newPolymarketPublic().ActiveMarkets(3)
		if err != nil {
			t.Fatal(err)
		}
		if len(marks) == 0 {
			t.Fatal("public Polymarket discovery returned no active markets")
		}
		mark, err := normalizeMark("polymarket-public", marks[0], time.Now().UTC())
		if err != nil || mark.Instrument == nil || mark.Instrument.Name == "" {
			encoded, _ := json.Marshal(mark)
			t.Fatalf("mark=%s err=%v", encoded, err)
		}
		t.Logf("market=%s yes=%v", mark.Instrument.Name, mark.Price)
	})
}
