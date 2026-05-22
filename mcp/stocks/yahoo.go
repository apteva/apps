package main

// Yahoo Finance public client — free, no-auth equity / etf quotes, OHLCV
// bars, and dividend history. Backed by Yahoo's /v8/finance/chart
// endpoint, the same source the `yfinance` Python lib uses. One request
// carries the meta-style quote, the timestamp + OHLCV arrays, AND (with
// events=div) the dividend payment history — so a single call covers
// every facet this app surfaces for a symbol.
//
// We deliberately use /chart rather than the more obviously-named
// /v7/finance/quote or /v10/finance/quoteSummary endpoints: those have
// had cookie / crumb requirements bolted on over the years that break
// unauthenticated callers, while /chart has stayed stable and key-free.
// The trade-off is that fundamentals only available via quoteSummary
// (market cap, P/E, payout ratio) aren't here in v0.1; dividend yield is
// computed from the dividend events + current price instead, which keeps
// the whole app on the one reliable endpoint.
//
// v0.2 will route this through the platform's yahoo-finance integration
// (ExecuteIntegrationTool) so richer fundamentals can come from a bound
// provider; v0.1 hits the public endpoint directly so a fresh install
// shows a real AAPL price with zero operator setup.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

const yahooBase = "https://query1.finance.yahoo.com"

type yahooClient struct {
	base   string
	client *http.Client
	// Concurrency cap on parallel symbol fetches. Yahoo's unofficial
	// rate limit is ~2k/hour observed; capping at 4 in-flight keeps the
	// universe warm-up well under that.
	sem chan struct{}
}

func newYahoo() *yahooClient {
	return &yahooClient{
		base:   yahooBase,
		client: &http.Client{Timeout: 8 * time.Second},
		sem:    make(chan struct{}, 4),
	}
}

// Bar is one OHLCV candle. Short JSON keys keep the chart payload small
// over the wire to the panel.
type Bar struct {
	T int64   `json:"t"` // unix seconds
	O float64 `json:"o"`
	H float64 `json:"h"`
	L float64 `json:"l"`
	C float64 `json:"c"`
	V float64 `json:"v"`
}

// Dividend is one per-share cash dividend payment.
type Dividend struct {
	ExDate int64   `json:"ex_date"` // unix seconds
	Amount float64 `json:"amount"`
}

// quoteMeta is the normalized slice of chart.result[0].meta this app
// reads. Yahoo packs many more fields there; we pull only what the get /
// list surfaces need.
type quoteMeta struct {
	Symbol           string  `json:"symbol"`
	Currency         string  `json:"currency"`
	Exchange         string  `json:"exchange"`
	InstrumentType   string  `json:"instrument_type"`
	Name             string  `json:"name"`
	Price            float64 `json:"price"`
	PreviousClose    float64 `json:"previous_close"`
	DayHigh          float64 `json:"day_high"`
	DayLow           float64 `json:"day_low"`
	Volume           float64 `json:"volume"`
	FiftyTwoWeekHigh float64 `json:"fifty_two_week_high"`
	FiftyTwoWeekLow  float64 `json:"fifty_two_week_low"`
}

// chartResult is one fully-parsed /chart response.
type chartResult struct {
	Meta      quoteMeta
	Bars      []Bar
	Dividends []Dividend
}

// fetchChart makes one call to /v8/finance/chart for a symbol. rng +
// interval are Yahoo's own enums (range: 1mo|6mo|1y|5y|max; interval:
// 1d|1wk|1mo). withDivs adds events=div so the response carries the
// dividend history.
func (y *yahooClient) fetchChart(symbol, rng, interval string, withDivs bool) (*chartResult, error) {
	q := url.Values{}
	q.Set("range", rng)
	q.Set("interval", interval)
	if withDivs {
		q.Set("events", "div")
	}
	u := y.base + "/v8/finance/chart/" + url.PathEscape(strings.ToUpper(symbol)) + "?" + q.Encode()

	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	// Yahoo silently drops requests carrying the Go default UA.
	req.Header.Set("User-Agent", "Mozilla/5.0 (compatible; Apteva-Stocks/0.1)")
	req.Header.Set("Accept", "application/json")

	resp, err := y.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		return nil, fmt.Errorf("yahoo HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return nil, err
	}

	var doc struct {
		Chart struct {
			Result []struct {
				Meta struct {
					Symbol               string  `json:"symbol"`
					Currency             string  `json:"currency"`
					ExchangeName         string  `json:"exchangeName"`
					InstrumentType       string  `json:"instrumentType"`
					LongName             string  `json:"longName"`
					ShortName            string  `json:"shortName"`
					RegularMarketPrice   float64 `json:"regularMarketPrice"`
					PreviousClose        float64 `json:"previousClose"`
					ChartPreviousClose   float64 `json:"chartPreviousClose"`
					RegularMarketDayHigh float64 `json:"regularMarketDayHigh"`
					RegularMarketDayLow  float64 `json:"regularMarketDayLow"`
					RegularMarketVolume  float64 `json:"regularMarketVolume"`
					FiftyTwoWeekHigh     float64 `json:"fiftyTwoWeekHigh"`
					FiftyTwoWeekLow      float64 `json:"fiftyTwoWeekLow"`
				} `json:"meta"`
				Timestamp  []int64 `json:"timestamp"`
				Indicators struct {
					Quote []struct {
						Open   []float64 `json:"open"`
						High   []float64 `json:"high"`
						Low    []float64 `json:"low"`
						Close  []float64 `json:"close"`
						Volume []float64 `json:"volume"`
					} `json:"quote"`
				} `json:"indicators"`
				Events struct {
					Dividends map[string]struct {
						Amount float64 `json:"amount"`
						Date   int64   `json:"date"`
					} `json:"dividends"`
				} `json:"events"`
			} `json:"result"`
			Error any `json:"error"`
		} `json:"chart"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("yahoo decode: %w", err)
	}
	if doc.Chart.Error != nil {
		return nil, fmt.Errorf("yahoo error envelope: %v", doc.Chart.Error)
	}
	if len(doc.Chart.Result) == 0 {
		return nil, fmt.Errorf("yahoo: no data for %s", symbol)
	}
	r := doc.Chart.Result[0]

	out := &chartResult{Meta: quoteMeta{
		Symbol:           orStr(r.Meta.Symbol, strings.ToUpper(symbol)),
		Currency:         r.Meta.Currency,
		Exchange:         r.Meta.ExchangeName,
		InstrumentType:   r.Meta.InstrumentType,
		Name:             orStr(r.Meta.LongName, r.Meta.ShortName),
		Price:            r.Meta.RegularMarketPrice,
		PreviousClose:    orFloat(r.Meta.PreviousClose, r.Meta.ChartPreviousClose),
		DayHigh:          r.Meta.RegularMarketDayHigh,
		DayLow:           r.Meta.RegularMarketDayLow,
		Volume:           r.Meta.RegularMarketVolume,
		FiftyTwoWeekHigh: r.Meta.FiftyTwoWeekHigh,
		FiftyTwoWeekLow:  r.Meta.FiftyTwoWeekLow,
	}}

	if len(r.Indicators.Quote) > 0 {
		qd := r.Indicators.Quote[0]
		out.Bars = make([]Bar, 0, len(r.Timestamp))
		for i, t := range r.Timestamp {
			// Yahoo emits null inside OHLC arrays for holidays / gaps;
			// the decoder turns those into 0. Skip zero-close rows so
			// the chart doesn't plot flat-zero dips.
			if i >= len(qd.Close) || qd.Close[i] == 0 {
				continue
			}
			b := Bar{T: t, C: qd.Close[i]}
			if i < len(qd.Open) {
				b.O = qd.Open[i]
			}
			if i < len(qd.High) {
				b.H = qd.High[i]
			}
			if i < len(qd.Low) {
				b.L = qd.Low[i]
			}
			if i < len(qd.Volume) {
				b.V = qd.Volume[i]
			}
			out.Bars = append(out.Bars, b)
		}
	}

	for _, d := range r.Events.Dividends {
		if d.Amount <= 0 || d.Date == 0 {
			continue
		}
		out.Dividends = append(out.Dividends, Dividend{ExDate: d.Date, Amount: d.Amount})
	}
	sort.Slice(out.Dividends, func(i, j int) bool { return out.Dividends[i].ExDate < out.Dividends[j].ExDate })

	return out, nil
}
