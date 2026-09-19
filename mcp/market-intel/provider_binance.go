package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// BinancePublicProvider uses only documented, unauthenticated spot endpoints.
// It is intentionally read-only and can be replaced by Binance.US or a paid
// feed without changing scanner logic.
type BinancePublicProvider struct {
	BaseURL string
	Client  *http.Client
	Now     func() time.Time
}

func NewBinancePublicProvider() *BinancePublicProvider {
	return &BinancePublicProvider{BaseURL: "https://api.binance.com/api/v3", Client: &http.Client{Timeout: 10 * time.Second}, Now: time.Now}
}
func (b *BinancePublicProvider) Name() string  { return "binance-public" }
func (b *BinancePublicProvider) Venue() string { return "binance" }

func (b *BinancePublicProvider) Universe(ctx context.Context, limit int) ([]Instrument, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	var tickers []struct {
		Symbol      string `json:"symbol"`
		Last        string `json:"lastPrice"`
		Bid         string `json:"bidPrice"`
		Ask         string `json:"askPrice"`
		QuoteVolume string `json:"quoteVolume"`
		CloseTime   int64  `json:"closeTime"`
	}
	if err := b.get(ctx, "/ticker/24hr", nil, &tickers); err != nil {
		return nil, err
	}
	out := make([]Instrument, 0, len(tickers))
	for _, t := range tickers {
		if !eligibleBinanceSymbol(t.Symbol) {
			continue
		}
		last, e1 := strconv.ParseFloat(t.Last, 64)
		bid, e2 := strconv.ParseFloat(t.Bid, 64)
		ask, e3 := strconv.ParseFloat(t.Ask, 64)
		qv, e4 := strconv.ParseFloat(t.QuoteVolume, 64)
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil || last <= 0 || bid <= 0 || ask <= 0 || ask < bid {
			continue
		}
		base := strings.TrimSuffix(t.Symbol, "USDT")
		out = append(out, Instrument{Symbol: t.Symbol, Canonical: base + "-USD", AssetClass: "crypto", Bid: bid, Ask: ask, Last: last, QuoteVolume24h: qv, QuoteTime: t.CloseTime / 1000})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].QuoteVolume24h > out[j].QuoteVolume24h })
	if len(out) > limit {
		out = out[:limit]
	}
	return out, nil
}

func eligibleBinanceSymbol(s string) bool {
	if !strings.HasSuffix(s, "USDT") {
		return false
	}
	base := strings.TrimSuffix(s, "USDT")
	if base == "USDC" || base == "FDUSD" || base == "TUSD" || base == "DAI" || base == "EUR" {
		return false
	}
	for _, suffix := range []string{"UP", "DOWN", "BULL", "BEAR"} {
		if strings.HasSuffix(base, suffix) {
			return false
		}
	}
	return base != ""
}

func (b *BinancePublicProvider) Bars(ctx context.Context, symbol, interval string, limit int) ([]Candle, error) {
	if limit < 60 {
		limit = 60
	}
	if limit > 1000 {
		limit = 1000
	}
	q := url.Values{"symbol": {symbol}, "interval": {interval}, "limit": {strconv.Itoa(limit)}}
	var raw [][]json.RawMessage
	if err := b.get(ctx, "/klines", q, &raw); err != nil {
		return nil, err
	}
	now := b.Now().UnixMilli()
	out := make([]Candle, 0, len(raw))
	for _, r := range raw {
		if len(r) < 7 {
			continue
		}
		var openTime, closeTime int64
		var o, h, l, c, v string
		_ = json.Unmarshal(r[0], &openTime)
		_ = json.Unmarshal(r[1], &o)
		_ = json.Unmarshal(r[2], &h)
		_ = json.Unmarshal(r[3], &l)
		_ = json.Unmarshal(r[4], &c)
		_ = json.Unmarshal(r[5], &v)
		_ = json.Unmarshal(r[6], &closeTime)
		pf := func(s string) float64 { x, _ := strconv.ParseFloat(s, 64); return x }
		bar := Candle{Time: openTime / 1000, Open: pf(o), High: pf(h), Low: pf(l), Close: pf(c), Volume: pf(v), Closed: closeTime < now}
		if bar.Close > 0 {
			out = append(out, bar)
		}
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("binance returned no bars for %s", symbol)
	}
	return out, nil
}

func (b *BinancePublicProvider) get(ctx context.Context, path string, q url.Values, out any) error {
	u := strings.TrimRight(b.BaseURL, "/") + path
	if len(q) > 0 {
		u += "?" + q.Encode()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "Apteva-MarketIntel/0.2")
	resp, err := b.Client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("binance %s: HTTP %d: %s", path, resp.StatusCode, strings.TrimSpace(string(body)))
	}
	if err := json.Unmarshal(body, out); err != nil {
		return fmt.Errorf("binance %s decode: %w", path, err)
	}
	return nil
}
