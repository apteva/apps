package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestApplyAlpacaMarketPayloadPersistsQuoteAndClosedBar(t *testing.T) {
	ctx := newTestCtx(t)
	_, _ = ctx.AppDB().Exec(`DELETE FROM marks WHERE symbol='AAPL'`)
	coalescer := &markEventCoalescer{add: make(chan *Mark, 8), cancel: func() {}}
	payload := []byte(`[
		{"T":"q","S":"AAPL","bp":226.10,"ap":226.12,"bs":4,"as":7,"t":"2026-01-03T14:30:00Z"},
		{"T":"t","S":"AAPL","p":226.11,"s":25,"t":"2026-01-03T14:30:00.100Z"},
		{"T":"b","S":"AAPL","o":226,"h":226.2,"l":225.9,"c":226.11,"v":1012,"n":42,"vw":226.07,"t":"2026-01-03T14:30:00Z"}
	]`)
	payload = []byte(strings.Replace(string(payload), "2026-01-03T14:30:00Z", time.Now().Add(-time.Second).UTC().Format(time.RFC3339Nano), 1))
	payload = []byte(strings.Replace(string(payload), "2026-01-03T14:30:00.100Z", time.Now().UTC().Format(time.RFC3339Nano), 1))
	if err := applyAlpacaMarketPayload(ctx, "iex", payload, coalescer); err != nil {
		t.Fatal(err)
	}
	mark, err := dbGetMark(ctx.AppDB(), "AAPL")
	if err != nil {
		t.Fatal(err)
	}
	if mark.Source != alpacaMarketDataSlug || mark.Feed != "iex" || mark.BidPrice == nil || *mark.BidPrice != 226.10 || mark.AskPrice == nil || *mark.AskPrice != 226.12 {
		t.Fatalf("unexpected streamed mark: %#v", mark)
	}
	if mark.LastTradePrice == nil || *mark.LastTradePrice != 226.11 || mark.LastTradeSize == nil || *mark.LastTradeSize != 25 {
		t.Fatalf("trade fields not preserved: %#v", mark)
	}
	var close float64
	var complete bool
	if err := ctx.AppDB().QueryRow(`SELECT close, complete FROM market_bars WHERE symbol = 'AAPL' AND timeframe = '1m'`).Scan(&close, &complete); err != nil {
		t.Fatal(err)
	}
	if close != 226.11 || !complete {
		t.Fatalf("closed bar = close %v complete %v", close, complete)
	}
}

func TestHTTPPortfolioListExposesExecutionSafetyState(t *testing.T) {
	ctx := newTestCtx(t)
	id, err := dbCreatePortfolio(ctx.AppDB(), &Portfolio{ProjectID: "test-proj", Name: "paper broker", AllowedClasses: []string{"equity"}, Mode: "live", ExecutionEnvironment: "broker_paper", BrokerSlug: "alpaca-trading"})
	if err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest("GET", "/portfolios?project_id=test-proj", nil)
	recorder := httptest.NewRecorder()
	(&App{}).httpListPortfolios(recorder, req)
	if recorder.Code != 200 {
		t.Fatalf("status %d: %s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Portfolios []Portfolio `json:"portfolios"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatal(err)
	}
	if len(response.Portfolios) != 1 || response.Portfolios[0].ID != id || response.Portfolios[0].ExecutionEnvironment != "broker_paper" || response.Portfolios[0].LiveArmed {
		t.Fatalf("unexpected portfolio list: %#v", response.Portfolios)
	}
}

func TestBrokerLiveArmingRequiresConfirmation(t *testing.T) {
	ctx := newTestCtx(t)
	id, err := dbCreatePortfolio(ctx.AppDB(), &Portfolio{ProjectID: "test-proj", Name: "live", AllowedClasses: []string{"equity"}, Mode: "live", ExecutionEnvironment: "broker_live", BrokerSlug: "alpaca-trading"})
	if err != nil {
		t.Fatal(err)
	}
	app := &App{}
	out, err := app.toolPortfolioArmLive(ctx, map[string]any{"portfolio_id": float64(id), "armed": true})
	if err != nil || out.(map[string]any)["code"] != "confirmation_required" {
		t.Fatalf("missing confirmation result = %#v, %v", out, err)
	}
	out, err = app.toolPortfolioArmLive(ctx, map[string]any{"portfolio_id": float64(id), "armed": true, "confirmation": "LIVE MONEY"})
	if err != nil || out.(map[string]any)["live_armed"] != true {
		t.Fatalf("confirmed arm result = %#v, %v", out, err)
	}
	portfolio, err := dbGetPortfolio(ctx.AppDB(), "test-proj", id)
	if err != nil || !portfolio.LiveArmed {
		t.Fatalf("portfolio live_armed = %v, %v", portfolio.LiveArmed, err)
	}
}

func TestAlpacaHostEnvironmentIsExactAndFailClosed(t *testing.T) {
	if environment, ok := alpacaHostEnvironment("paper-api.alpaca.markets"); !ok || environment != "broker_paper" {
		t.Fatalf("paper host = %q, %v", environment, ok)
	}
	if environment, ok := alpacaHostEnvironment("api.alpaca.markets"); !ok || environment != "broker_live" {
		t.Fatalf("live host = %q, %v", environment, ok)
	}
	if _, ok := alpacaHostEnvironment("https://api.alpaca.markets"); ok {
		t.Fatal("non-canonical host must not be classified")
	}
}
