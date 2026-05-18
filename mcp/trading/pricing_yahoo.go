package main

// Yahoo Finance public client — free, no-auth equity / etf marks + bars.
// Mirrors binance_public.go's shape so liveProvider can swap providers
// per asset class without any structural change. Backed by Yahoo's
// /v8/finance/chart endpoint (same source the `yfinance` Python lib
// uses); the response carries both the meta-style quote and the
// timestamps + OHLCV arrays we need for the chart pane, so one HTTP
// call covers both Quote() and Bars() per symbol.
//
// Used as the equity/etf fallback in liveProvider when no
// alpaca-market-data connection is bound. Always preferred over the
// mock walk — the only downside vs Alpaca is no SLA + ~200–400ms
// latency vs ~150ms, both acceptable for the default-fallback role.
//
// A proper apteva integration (integrations/src/apps/yahoo-finance.json)
// exists in parallel so other apps (finance, billing, analytics) can
// reach Yahoo through ExecuteIntegrationTool. The trading sidecar
// uses this direct client instead because it ships data without any
// operator binding step — fresh install, real AAPL price, no clicks.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

const yahooDefaultBase = "https://query1.finance.yahoo.com"

type yahooPublic struct {
	base   string
	client *http.Client
	// Concurrency cap on parallel symbol fetches. Yahoo's unofficial
	// rate limit is ~2k/hour observed; capping at 4 in-flight keeps
	// us well under that even on tick-heavy UI sessions.
	sem chan struct{}
}

func newYahooPublic() *yahooPublic {
	return &yahooPublic{
		base:   yahooDefaultBase,
		client: &http.Client{Timeout: 5 * time.Second},
		sem:    make(chan struct{}, 4),
	}
}

// Quote returns a Mark for the given symbol. Hits the chart endpoint
// with 1d/5m and pulls meta.regularMarketPrice — using the chart
// endpoint instead of the (more obviously-named) /v7/finance/quote
// endpoint because the latter has had cookie / crumb requirements
// added over the years that broke unauthenticated callers. /chart
// has been stable for the same period.
func (y *yahooPublic) Quote(symbol string) (*Mark, error) {
	bars, meta, err := y.fetchChart(symbol, "1d", "5m")
	if err != nil {
		return nil, err
	}
	_ = bars
	if meta == nil || meta.RegularMarketPrice <= 0 {
		return nil, fmt.Errorf("yahoo: no regularMarketPrice for %s", symbol)
	}
	mk := &Mark{
		Symbol:     strings.ToUpper(symbol),
		AssetClass: inferAssetClass(symbol),
		Price:      meta.RegularMarketPrice,
		MarkedAt:   time.Now().UTC().Format(time.RFC3339),
	}
	if meta.PreviousClose > 0 {
		pc := meta.PreviousClose
		mk.PrevClose = &pc
	}
	if meta.RegularMarketVolume > 0 {
		v := meta.RegularMarketVolume
		mk.Volume24h = &v
	}
	return mk, nil
}

// Bars returns OHLCV history for the given symbol + range. Maps our
// range chips to Yahoo's (range, interval) tuple. The chart endpoint
// returns both meta + bars; we ignore meta here and let Quote() own
// that responsibility.
func (y *yahooPublic) Bars(symbol, rng string) ([]Bar, error) {
	yRange, yInterval := yahooRangeFor(rng)
	bars, _, err := y.fetchChart(symbol, yRange, yInterval)
	return bars, err
}

// UniverseBatch fetches multiple symbols in parallel (semaphore-limited).
// Yahoo doesn't expose a true batched quote in the chart API, so this
// is N concurrent calls — but with our sem=4 cap, even 50 symbols
// finishes in ~3-5 seconds and the typical equity universe is 7-10
// tickers (sub-second batch).
func (y *yahooPublic) UniverseBatch(symbols []string) ([]*Mark, error) {
	if len(symbols) == 0 {
		return nil, nil
	}
	var (
		mu   sync.Mutex
		wg   sync.WaitGroup
		out  = make([]*Mark, 0, len(symbols))
	)
	for _, sym := range symbols {
		wg.Add(1)
		go func(s string) {
			defer wg.Done()
			y.sem <- struct{}{}
			defer func() { <-y.sem }()
			m, err := y.Quote(s)
			if err != nil || m == nil {
				return
			}
			mu.Lock()
			out = append(out, m)
			mu.Unlock()
		}(sym)
	}
	wg.Wait()
	return out, nil
}

// ─── HTTP + response parsing ───────────────────────────────────────

// yahooMeta — the bits of `chart.result[0].meta` we read. Yahoo packs
// many more fields in there (52-week range, exchange timezone, market
// state, etc.); we only pull what feeds Mark.
type yahooMeta struct {
	Symbol              string  `json:"symbol"`
	RegularMarketPrice  float64 `json:"regularMarketPrice"`
	PreviousClose       float64 `json:"previousClose"`
	ChartPreviousClose  float64 `json:"chartPreviousClose"`
	RegularMarketVolume float64 `json:"regularMarketVolume"`
}

// fetchChart — single HTTP call, returns (bars, meta) split. The chart
// endpoint is the workhorse: one request gets everything for a symbol
// + range. Errors are mapped to nil pointers so callers can decide
// whether the partial data is usable.
func (y *yahooPublic) fetchChart(symbol, rng, interval string) ([]Bar, *yahooMeta, error) {
	q := url.Values{}
	q.Set("range", rng)
	q.Set("interval", interval)
	// Yahoo silently drops calls with the Go default UA; the integration
	// catalog declares a browser-ish UA + the client sets the same here
	// for the direct-call path. If Yahoo tightens auth further (cookie
	// + crumb), the parse will fail with a clean error and the caller
	// falls back to mock — no silent corruption.
	u := y.base + "/v8/finance/chart/" + url.PathEscape(strings.ToUpper(symbol)) + "?" + q.Encode()

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Apteva-Trading/0.4)")
	req.Header.Set("Accept", "application/json")

	resp, err := y.client.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, nil, fmt.Errorf("yahoo HTTP %d: %s", resp.StatusCode, string(body))
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, nil, err
	}

	// Response shape:
	//   { "chart": { "result": [{ "meta": {...}, "timestamp": [...],
	//     "indicators": { "quote": [{ "open": [...], "close": [...], ... }] } }],
	//     "error": null }
	//   }
	var resp1 struct {
		Chart struct {
			Result []struct {
				Meta       yahooMeta `json:"meta"`
				Timestamp  []int64   `json:"timestamp"`
				Indicators struct {
					Quote []struct {
						Open   []float64 `json:"open"`
						High   []float64 `json:"high"`
						Low    []float64 `json:"low"`
						Close  []float64 `json:"close"`
						Volume []float64 `json:"volume"`
					} `json:"quote"`
				} `json:"indicators"`
			} `json:"result"`
			Error any `json:"error"`
		} `json:"chart"`
	}
	if err := json.Unmarshal(raw, &resp1); err != nil {
		return nil, nil, fmt.Errorf("yahoo decode: %w", err)
	}
	if resp1.Chart.Error != nil {
		return nil, nil, fmt.Errorf("yahoo error envelope: %v", resp1.Chart.Error)
	}
	if len(resp1.Chart.Result) == 0 {
		return nil, nil, fmt.Errorf("yahoo: empty result for %s", symbol)
	}
	r := resp1.Chart.Result[0]
	meta := r.Meta

	bars := make([]Bar, 0, len(r.Timestamp))
	if len(r.Indicators.Quote) > 0 {
		q := r.Indicators.Quote[0]
		// Yahoo can return null values inside the OHLC arrays for
		// holidays / market gaps. The JSON decoder turns those into
		// 0; skip rows where close is 0 to avoid plotting flat-zero
		// dips on the chart.
		for i, t := range r.Timestamp {
			if i >= len(q.Close) || q.Close[i] == 0 {
				continue
			}
			bar := Bar{T: t, C: q.Close[i]}
			if i < len(q.Open) {
				bar.O = q.Open[i]
			}
			if i < len(q.High) {
				bar.H = q.High[i]
			}
			if i < len(q.Low) {
				bar.L = q.Low[i]
			}
			if i < len(q.Volume) {
				bar.V = q.Volume[i]
			}
			bars = append(bars, bar)
		}
	}
	return bars, &meta, nil
}

// yahooRangeFor — maps our local ChartRange to Yahoo's (range, interval)
// tuple. Yahoo's range is a separate enum from ours — keep the bar
// count roughly aligned with bucketsForRange so the chart resolution
// matches whether you're on Yahoo, Alpaca, or mock.
func yahooRangeFor(rng string) (string, string) {
	switch strings.ToUpper(rng) {
	case "1D":  return "1d",  "5m"
	case "5D":  return "5d",  "30m"
	case "1M":  return "1mo", "1h"
	case "3M":  return "3mo", "1d"
	case "1Y":  return "1y",  "1d"
	case "ALL": return "5y",  "1wk"
	default:    return "1d",  "5m"
	}
}
