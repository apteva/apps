package main

// Binance public REST client. No auth — public market-data endpoints
// only. Exists so a freshly-installed trading app shows real BTC/ETH
// prices the moment it boots, without asking the operator for any
// credentials. The integration JSON at integrations/src/apps/binance-
// trading.json catalogs the same endpoints + shapes; this is the
// subset we hit on the read path.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

const binanceDefaultBase = "https://api.binance.com/api/v3"

// USD↔USDT mapping. Internal symbols use the dash form (BTC-USD); the
// wire form on Binance is BTCUSDT. We translate at the boundary and
// the rest of the app stays oblivious.
var binanceUSDPairs = map[string]string{
	"BTC-USD":   "BTCUSDT",
	"ETH-USD":   "ETHUSDT",
	"SOL-USD":   "SOLUSDT",
	"AVAX-USD":  "AVAXUSDT",
	"DOGE-USD":  "DOGEUSDT",
	"MATIC-USD": "MATICUSDT",
}

type binancePublic struct {
	base   string
	client *http.Client
}

func newBinancePublic() *binancePublic {
	return &binancePublic{
		base:   binanceDefaultBase,
		client: &http.Client{Timeout: 4 * time.Second},
	}
}

// Quote returns one Mark for the given internal symbol. Returns an
// error on HTTP / decode failure; the caller records the provider error
// and leaves the previous persisted mark untouched.
func (b *binancePublic) Quote(symbol string) (*Mark, error) {
	wire, err := binanceWireSymbol(symbol)
	if err != nil {
		return nil, err
	}
	q := url.Values{}
	q.Set("symbol", wire)
	raw, err := b.fetch(b.base + "/ticker/24hr?" + q.Encode())
	if err != nil {
		return nil, err
	}
	var t binanceTicker
	if err := json.Unmarshal(raw, &t); err != nil {
		return nil, fmt.Errorf("binancePublic: decode ticker: %w", err)
	}
	return t.toMark(symbol)
}

// UniverseBatch fetches all requested internal symbols in one HTTP
// call using the array form (`?symbols=["BTCUSDT",...]`). Symbols
// outside our internal table are skipped with a warning rather than
// failing the whole batch.
func (b *binancePublic) UniverseBatch(symbols []string) ([]*Mark, error) {
	if len(symbols) == 0 {
		return nil, nil
	}
	wireSyms := make([]string, 0, len(symbols))
	canonicalByWire := make(map[string]string, len(symbols))
	for _, s := range symbols {
		w, err := binanceWireSymbol(s)
		if err != nil {
			continue
		}
		wireSyms = append(wireSyms, w)
		canonicalByWire[w] = strings.ToUpper(strings.TrimSpace(s))
	}
	if len(wireSyms) == 0 {
		return nil, nil
	}
	// Binance's `symbols` query expects a JSON array literal —
	// e.g. ?symbols=["BTCUSDT","ETHUSDT"]. URL-encode the bracket form.
	arr, _ := json.Marshal(wireSyms)
	u := b.base + "/ticker/24hr?symbols=" + url.QueryEscape(string(arr))
	raw, err := b.fetch(u)
	if err != nil {
		return nil, err
	}
	var arrOut []binanceTicker
	if err := json.Unmarshal(raw, &arrOut); err != nil {
		return nil, fmt.Errorf("binancePublic: decode batch ticker: %w", err)
	}
	out := make([]*Mark, 0, len(arrOut))
	for _, t := range arrOut {
		internal, ok := canonicalByWire[t.Symbol]
		if !ok {
			continue
		}
		m, err := t.toMark(internal)
		if err != nil {
			continue
		}
		out = append(out, m)
	}
	return out, nil
}

// Bars returns OHLCV history via /api/v3/klines — Binance's
// candlestick endpoint, free + no auth. Maps our ChartRange to
// Binance's interval string + the bar count that matches the
// engine's bucketsForRange.
func (b *binancePublic) Bars(symbol, rng string) ([]Bar, error) {
	wire, err := binanceWireSymbol(symbol)
	if err != nil {
		return nil, err
	}
	interval, limit := binanceIntervalForRange(rng)
	return b.klines(wire, interval, limit, time.Time{}, time.Time{})
}

func (b *binancePublic) BacktestBars(symbol, interval string, start, end time.Time, limit int) ([]Bar, error) {
	return b.BacktestBarsContext(context.Background(), symbol, interval, start, end, limit)
}

func (b *binancePublic) BacktestBarsContext(ctx context.Context, symbol, interval string, start, end time.Time, limit int) ([]Bar, error) {
	wire, err := binanceWireSymbol(symbol)
	if err != nil {
		return nil, err
	}
	binanceInterval, err := binanceIntervalForBacktest(interval)
	if err != nil {
		return nil, err
	}
	if start.IsZero() || end.IsZero() || end.Before(start) {
		return nil, errors.New("binancePublic: valid backtest start and end required")
	}
	if limit <= 0 {
		return nil, errors.New("binancePublic: positive backtest bar limit required")
	}

	pageSize := 1000
	cursor := start.UTC()
	effectiveEnd := end.UTC()
	byTime := make(map[int64]Bar, min(limit, pageSize))
	for len(byTime) < limit && !cursor.After(effectiveEnd) {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		remaining := limit - len(byTime)
		if remaining < pageSize {
			pageSize = remaining
		}
		page, err := retryBacktestBars(ctx, func() ([]Bar, error) {
			return b.klinesContext(ctx, wire, binanceInterval, pageSize, cursor, effectiveEnd)
		})
		if err != nil {
			return nil, err
		}
		if len(page) == 0 {
			break
		}
		lastT := int64(0)
		for _, bar := range page {
			at := time.Unix(bar.T, 0).UTC()
			if at.Before(start.UTC()) || at.After(effectiveEnd) || bar.C <= 0 {
				continue
			}
			byTime[bar.T] = bar
			if bar.T > lastT {
				lastT = bar.T
			}
		}
		if lastT <= 0 {
			return nil, errors.New("binancePublic: kline page contained no usable timestamps")
		}
		next := time.Unix(lastT, 0).UTC().Add(backtestIntervalDuration(binanceInterval))
		if !next.After(cursor) {
			return nil, errors.New("binancePublic: kline pagination made no progress")
		}
		cursor = next
		if len(page) < pageSize {
			break
		}
	}

	out := make([]Bar, 0, len(byTime))
	for _, bar := range byTime {
		out = append(out, bar)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].T < out[j].T })
	return out, nil
}

func binanceWireSymbol(symbol string) (string, error) {
	canonical := strings.ToUpper(strings.TrimSpace(symbol))
	if wire, ok := binanceUSDPairs[canonical]; ok {
		return wire, nil
	}
	if !strings.HasSuffix(canonical, "-USD") {
		return "", fmt.Errorf("binancePublic: crypto symbol must use BASE-USD form, got %q", symbol)
	}
	base := strings.TrimSuffix(canonical, "-USD")
	if base == "" || strings.ContainsAny(base, ":/ -") {
		return "", fmt.Errorf("binancePublic: invalid crypto symbol %q", symbol)
	}
	return base + "USDT", nil
}

func (b *binancePublic) klines(wire, interval string, limit int, start, end time.Time) ([]Bar, error) {
	return b.klinesContext(context.Background(), wire, interval, limit, start, end)
}

func (b *binancePublic) klinesContext(ctx context.Context, wire, interval string, limit int, start, end time.Time) ([]Bar, error) {
	q := url.Values{}
	q.Set("symbol", wire)
	q.Set("interval", interval)
	q.Set("limit", strconv.Itoa(limit))
	if !start.IsZero() {
		q.Set("startTime", strconv.FormatInt(start.UTC().UnixMilli(), 10))
	}
	if !end.IsZero() {
		q.Set("endTime", strconv.FormatInt(end.UTC().UnixMilli(), 10))
	}
	raw, err := b.fetchContext(ctx, b.base+"/klines?"+q.Encode())
	if err != nil {
		return nil, err
	}
	// Klines come back as an array of arrays; each inner array's
	// elements are positional: [openTime, open, high, low, close,
	// volume, closeTime, quoteVolume, trades, ...]. We unmarshal
	// into []any then pluck by index — sturdy against trailing
	// fields Binance might add.
	var rows [][]any
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("binancePublic: decode klines: %w", err)
	}
	out := make([]Bar, 0, len(rows))
	for _, row := range rows {
		if len(row) < 6 {
			continue
		}
		// openTime is ms-since-epoch as a JSON number → float64.
		openMS, _ := row[0].(float64)
		o := parseKlineFloat(row[1])
		h := parseKlineFloat(row[2])
		l := parseKlineFloat(row[3])
		c := parseKlineFloat(row[4])
		v := parseKlineFloat(row[5])
		if c <= 0 {
			continue
		}
		out = append(out, Bar{
			T: int64(openMS / 1000),
			O: o, H: h, L: l, C: c, V: v,
		})
	}
	return out, nil
}

func binanceIntervalForBacktest(interval string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(interval)) {
	case "5m", "15m", "1h", "4h", "1d", "1w":
		return strings.ToLower(strings.TrimSpace(interval)), nil
	default:
		return "", fmt.Errorf("unsupported Binance backtest interval %q", interval)
	}
}

func parseKlineFloat(v any) float64 {
	if s, ok := v.(string); ok {
		if f, err := strconv.ParseFloat(s, 64); err == nil {
			return f
		}
	}
	return 0
}

// binanceIntervalForRange — maps the panel's range chip to a Binance
// interval + bar count. Aligned with engine.bucketsForRange so live
// + mock paths show the same chart resolution.
func binanceIntervalForRange(rng string) (string, int) {
	switch strings.ToUpper(rng) {
	case "1D":
		return "5m", 78
	case "5D":
		return "30m", 130
	case "1M":
		return "4h", 220
	case "3M":
		return "8h", 320
	case "1Y":
		return "1d", 540
	case "ALL":
		return "1d", 720
	default:
		return "5m", 78
	}
}

// fetch wraps the HTTP call with a context deadline + status-code
// check. Body is read in full; callers decode.
func (b *binancePublic) fetch(u string) ([]byte, error) {
	return b.fetchContext(context.Background(), u)
}

func (b *binancePublic) fetchContext(parent context.Context, u string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(parent, 8*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "apteva-trading/0.2")
	resp, err := b.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("binancePublic: HTTP %d: %s", resp.StatusCode, string(body))
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

// binanceTicker mirrors the relevant subset of /ticker/24hr's response
// shape. Numeric fields land as strings on the wire — Binance's API
// is consistent about that — so we parse them ourselves.
type binanceTicker struct {
	Symbol             string `json:"symbol"`
	LastPrice          string `json:"lastPrice"`
	PrevClosePrice     string `json:"prevClosePrice"`
	PriceChange        string `json:"priceChange"`
	PriceChangePercent string `json:"priceChangePercent"`
	Volume             string `json:"volume"`      // base-asset volume
	QuoteVolume        string `json:"quoteVolume"` // USD-side volume; better for our 24h indicator
}

func (t binanceTicker) toMark(internalSymbol string) (*Mark, error) {
	price, err := strconv.ParseFloat(t.LastPrice, 64)
	if err != nil || price <= 0 {
		return nil, fmt.Errorf("binancePublic: bad lastPrice %q", t.LastPrice)
	}
	prev, _ := strconv.ParseFloat(t.PrevClosePrice, 64)
	vol, _ := strconv.ParseFloat(t.QuoteVolume, 64)
	mk := &Mark{
		Symbol:     internalSymbol,
		AssetClass: "crypto",
		Price:      price,
		MarkedAt:   time.Now().UTC().Format(time.RFC3339),
	}
	if prev > 0 {
		mk.PrevClose = &prev
	}
	if vol > 0 {
		mk.Volume24h = &vol
	}
	return mk, nil
}

// cryptoSymbolsKnown is the bootstrap set fetched in one batch. Symbols
// added through quotes, watchlists, alerts, or positions are persisted and
// refreshed separately by the engine.
func cryptoSymbolsKnown() []string {
	out := make([]string, 0, len(binanceUSDPairs))
	for k := range binanceUSDPairs {
		out = append(out, k)
	}
	// Stable order so per-tick HTTP requests are repeatable in tests.
	return sortedStrings(out)
}

func sortedStrings(in []string) []string {
	out := make([]string, len(in))
	copy(out, in)
	for i := 1; i < len(out); i++ {
		for j := i; j > 0 && out[j-1] > out[j]; j-- {
			out[j-1], out[j] = out[j], out[j-1]
		}
	}
	return out
}

// Helper used by polymarket_public.go.
func stripPolyPrefix(symbol string) string {
	return strings.TrimPrefix(symbol, "POLY:")
}
