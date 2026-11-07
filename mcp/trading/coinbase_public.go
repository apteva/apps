package main

// Public Coinbase Exchange market data uses actual BASE-USD products. Never
// substitute stablecoin pairs: the portfolio's accounting currency is USD.
import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"
)

const coinbasePublicSource = "coinbase-public"

type coinbaseProduct struct {
	ID              string `json:"id"`
	BaseCurrency    string `json:"base_currency"`
	QuoteCurrency   string `json:"quote_currency"`
	QuoteIncrement  string `json:"quote_increment"`
	BaseIncrement   string `json:"base_increment"`
	MinMarketFunds  string `json:"min_market_funds"`
	Status          string `json:"status"`
	TradingDisabled bool   `json:"trading_disabled"`
	CancelOnly      bool   `json:"cancel_only"`
	AuctionMode     bool   `json:"auction_mode"`
}

type cachedCoinbaseProduct struct {
	product coinbaseProduct
	at      time.Time
}

type coinbasePublic struct {
	base     string
	client   *http.Client
	mu       sync.Mutex
	products map[string]cachedCoinbaseProduct
}

func newCoinbasePublic() *coinbasePublic {
	return &coinbasePublic{
		base: "https://api.exchange.coinbase.com", client: &http.Client{Timeout: 4 * time.Second},
	}
}

func coinbaseUSDProduct(symbol string) (string, error) {
	symbol = canonicalSymbol(symbol)
	base := strings.TrimSuffix(symbol, "-USD")
	if base == symbol || base == "" || strings.ContainsAny(base, ":/ -?&#%") {
		return "", fmt.Errorf("coinbasePublic: expected BASE-USD product, got %q", symbol)
	}
	return symbol, nil
}

func (c *coinbasePublic) fetch(ctx context.Context, path string) ([]byte, error) {
	return publicGET(ctx, c.client, "coinbasePublic", c.base+path, 1<<20, map[string]string{
		"Accept": "application/json", "User-Agent": "apteva-trading/0.16",
	})
}

func (c *coinbasePublic) product(ctx context.Context, symbol string) (coinbaseProduct, error) {
	id, err := coinbaseUSDProduct(symbol)
	if err != nil {
		return coinbaseProduct{}, err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if cached, ok := c.products[id]; ok && time.Since(cached.at) < 5*time.Minute {
		return cached.product, nil
	}
	raw, err := c.fetch(ctx, "/products/"+url.PathEscape(id))
	if err != nil {
		return coinbaseProduct{}, err
	}
	var p coinbaseProduct
	if err := json.Unmarshal(raw, &p); err != nil {
		return p, fmt.Errorf("coinbasePublic: decode product: %w", err)
	}
	if p.ID != id || p.BaseCurrency != strings.TrimSuffix(id, "-USD") || p.QuoteCurrency != "USD" {
		return p, fmt.Errorf("coinbasePublic: product %s must identify the exact base asset and USD quote currency", id)
	}
	if p.Status != "online" || p.TradingDisabled || p.CancelOnly || p.AuctionMode {
		return p, fmt.Errorf("coinbasePublic: product %s is unavailable for continuous trading", id)
	}
	if c.products == nil {
		c.products = map[string]cachedCoinbaseProduct{}
	}
	c.products[id] = cachedCoinbaseProduct{product: p, at: time.Now()}
	return p, nil
}

func (c *coinbasePublic) Quote(symbol string) (*Mark, error) {
	p, err := c.product(context.Background(), symbol)
	if err != nil {
		return nil, err
	}
	raw, err := c.fetch(context.Background(), "/products/"+url.PathEscape(p.ID)+"/ticker")
	if err != nil {
		return nil, err
	}
	var t struct {
		Price  string `json:"price"`
		Bid    string `json:"bid"`
		Ask    string `json:"ask"`
		Size   string `json:"size"`
		Volume string `json:"volume"`
		Time   string `json:"time"`
	}
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, fmt.Errorf("coinbasePublic: decode ticker: %w", err)
	}
	price, err := strconv.ParseFloat(t.Price, 64)
	if err != nil || !finite(price) || price <= 0 {
		return nil, fmt.Errorf("coinbasePublic: invalid price %q", t.Price)
	}
	at, err := time.Parse(time.RFC3339Nano, t.Time)
	if err != nil {
		return nil, fmt.Errorf("coinbasePublic: invalid trade timestamp %q", t.Time)
	}
	receivedAt := time.Now().UTC()
	m := &Mark{
		Symbol: p.ID, AssetClass: "crypto", Price: price, Source: coinbasePublicSource,
		MarkedAt: at.UTC().Format(time.RFC3339Nano), TimestampKind: "exchange",
		ReceivedAt: receivedAt.Format(time.RFC3339Nano), VolumeUnit: "base_currency",
		LastTradePrice: ptr(price),
	}
	// The ticker's time is the last trade, not the book event time. Its
	// bid/ask is a REST snapshot, so use receipt time for the quote timestamp.
	for _, field := range []struct {
		value string
		dest  **float64
	}{{t.Bid, &m.BidPrice}, {t.Ask, &m.AskPrice}, {t.Size, &m.LastTradeSize}, {t.Volume, &m.Volume24h}} {
		n, err := strconv.ParseFloat(field.value, 64)
		if err != nil || !finite(n) || n < 0 {
			return nil, fmt.Errorf("coinbasePublic: invalid ticker value %q", field.value)
		}
		*field.dest = ptr(n)
	}
	if *m.BidPrice <= 0 || *m.AskPrice <= 0 || *m.BidPrice > *m.AskPrice {
		return nil, errors.New("coinbasePublic: invalid bid/ask snapshot")
	}
	m.QuoteAt = receivedAt.Format(time.RFC3339Nano)
	m.Instrument = defaultInstrument(p.ID, "crypto", coinbasePublicSource, receivedAt)
	m.Instrument.Exchange = "COINBASE"
	m.Instrument.ProviderSymbol = p.ID
	m.Instrument.QuoteCurrency = p.QuoteCurrency
	m.Instrument.BaseCurrency = p.BaseCurrency
	m.Instrument.VolumeUnit = m.VolumeUnit
	m.Instrument.TickSize = parseFloat(p.QuoteIncrement)
	m.Instrument.LotSize = parseFloat(p.BaseIncrement)
	m.Instrument.MinQty = m.Instrument.LotSize
	m.Instrument.MinNotional = parseFloat(p.MinMarketFunds)
	return normalizeMark(coinbasePublicSource, m, receivedAt)
}

func (c *coinbasePublic) UniverseBatch(symbols []string) ([]*Mark, error) {
	var out []*Mark
	var lastErr error
	// Bounded sequential calls stay below the public endpoint's burst limit.
	for _, symbol := range symbols {
		m, err := c.Quote(symbol)
		if err != nil {
			lastErr = err
			continue
		}
		out = append(out, m)
	}
	if len(out) == 0 {
		return nil, lastErr
	}
	return out, nil
}

func (c *coinbasePublic) Bars(symbol, rng string) ([]Bar, error) {
	interval, count := "5m", 288
	switch strings.ToUpper(rng) {
	case "5D":
		interval, count = "1h", 120
	case "1M":
		interval, count = "6h", 120
	case "3M":
		interval, count = "1d", 90
	case "1Y":
		interval, count = "1d", 365
	case "ALL":
		interval, count = "1d", 1825
	}
	dur, _ := coinbaseInterval(interval)
	end := time.Now().UTC()
	start := end.Truncate(dur).Add(-dur * time.Duration(count-1))
	return c.BacktestBars(symbol, interval, start, end, count)
}

func coinbaseInterval(interval string) (time.Duration, error) {
	switch interval {
	case "1m":
		return time.Minute, nil
	case "5m":
		return 5 * time.Minute, nil
	case "15m":
		return 15 * time.Minute, nil
	case "1h":
		return time.Hour, nil
	case "6h":
		return 6 * time.Hour, nil
	case "1d":
		return 24 * time.Hour, nil
	default:
		return 0, fmt.Errorf("coinbasePublic: unsupported candle interval %q", interval)
	}
}

func (c *coinbasePublic) BacktestBars(symbol, interval string, start, end time.Time, limit int) ([]Bar, error) {
	return c.BacktestBarsContext(context.Background(), symbol, interval, start, end, limit)
}

func (c *coinbasePublic) BacktestBarsContext(ctx context.Context, symbol, interval string, start, end time.Time, limit int) ([]Bar, error) {
	// Preserve legacy callers' composite intervals using real USD candles.
	// Coinbase supports neither four-hour nor weekly native candles.
	if interval == "4h" || interval == "1w" {
		if start.IsZero() || end.IsZero() || end.Before(start) || limit <= 0 {
			return nil, errors.New("coinbasePublic: valid time range and positive bar limit required")
		}
		native := "1h"
		if interval == "1w" {
			native = "1d"
		}
		dur, _ := coinbaseInterval(native)
		bars, err := c.BacktestBarsContext(ctx, symbol, native, start, end, int(end.Sub(start)/dur)+1)
		if err != nil {
			return nil, err
		}
		var out []Bar
		for _, bar := range bars {
			bucket := strategyClosedCandleBoundary(time.Unix(bar.T, 0), interval).Unix()
			if time.Unix(bucket, 0).Before(start) {
				continue
			}
			if len(out) == 0 || out[len(out)-1].T != bucket {
				bar.T = bucket
				out = append(out, bar)
				continue
			}
			last := &out[len(out)-1]
			last.C = bar.C
			last.H = max(last.H, bar.H)
			last.L = min(last.L, bar.L)
			last.V += bar.V
		}
		if len(out) > limit {
			out = out[:limit]
		}
		return out, nil
	}
	dur, err := coinbaseInterval(interval)
	if err != nil {
		return nil, err
	}
	if start.IsZero() || end.IsZero() || end.Before(start) || limit <= 0 {
		return nil, errors.New("coinbasePublic: valid time range and positive bar limit required")
	}
	p, err := c.product(ctx, symbol)
	if err != nil {
		return nil, err
	}
	byTime := map[int64]Bar{}
	// Coinbase returns at most 300 candles and may include rows before start.
	// Page by requested windows, filter precisely, deduplicate, then sort.
	for cursor := start.UTC(); !cursor.After(end) && len(byTime) < limit; {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		pageEnd := cursor.Add(299 * dur)
		if pageEnd.After(end) {
			pageEnd = end
		}
		q := url.Values{
			"granularity": {strconv.FormatInt(int64(dur/time.Second), 10)},
			"start":       {cursor.Format(time.RFC3339Nano)}, "end": {pageEnd.Format(time.RFC3339Nano)},
		}
		raw, err := c.fetch(ctx, "/products/"+url.PathEscape(p.ID)+"/candles?"+q.Encode())
		if err != nil {
			return nil, err
		}
		var rows [][]float64
		if err := json.Unmarshal(raw, &rows); err != nil {
			return nil, fmt.Errorf("coinbasePublic: decode candles: %w", err)
		}
		for _, row := range rows {
			if len(row) != 6 || !finite(row[0]) || row[0] <= 0 || row[0] != float64(int64(row[0])) {
				return nil, errors.New("coinbasePublic: malformed candle")
			}
			at := time.Unix(int64(row[0]), 0).UTC()
			if at.Before(cursor) || at.After(pageEnd) {
				continue
			}
			byTime[at.Unix()] = Bar{T: at.Unix(), L: row[1], H: row[2], O: row[3], C: row[4], V: row[5]}
		}
		cursor = pageEnd.Truncate(dur).Add(dur)
	}
	bars := make([]Bar, 0, len(byTime))
	for _, bar := range byTime {
		bars = append(bars, bar)
	}
	sort.Slice(bars, func(i, j int) bool { return bars[i].T < bars[j].T })
	if len(bars) > limit {
		bars = bars[:limit]
	}
	return normalizeBars(symbol, coinbasePublicSource, bars)
}
