package main

// Polymarket gamma-api public client. The gamma-api is read-only and
// requires no auth, so we can pull live YES/NO prices for prediction
// markets without asking the operator for credentials. The CLOB
// (writable, place-order) endpoints DO need auth — those are out of
// scope here; the paper engine simulates fills internally.

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"
)

const polymarketGammaBase = "https://gamma-api.polymarket.com"

type polymarketPublic struct {
	base   string
	client *http.Client
}

func newPolymarketPublic() *polymarketPublic {
	return &polymarketPublic{
		base:   polymarketGammaBase,
		client: &http.Client{Timeout: 5 * time.Second},
	}
}

// Quote returns one Mark for an internal POLY:<slug> symbol.
func (p *polymarketPublic) Quote(symbol string) (*Mark, error) {
	slug := stripPolyPrefix(symbol)
	if slug == symbol {
		return nil, fmt.Errorf("polymarketPublic: not a POLY: symbol — %q", symbol)
	}
	q := url.Values{}
	q.Set("slug", slug)
	q.Set("limit", "1")
	raw, err := p.fetch(p.base + "/markets?" + q.Encode())
	if err != nil {
		return nil, err
	}
	var rows []gammaMarket
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("polymarketPublic: decode market: %w", err)
	}
	if len(rows) == 0 {
		return nil, fmt.Errorf("polymarketPublic: no market for slug %q", slug)
	}
	return rows[0].toMark(symbol)
}

// UniverseBatch fetches up to N markets in one call. The gamma-api
// supports comma-separated `slugs[]` style queries via repeated `slug`
// params — we issue them in parallel-friendly batches of 50 to stay
// polite. v0.1 watchlists are small so we batch all in one call.
func (p *polymarketPublic) UniverseBatch(symbols []string) ([]*Mark, error) {
	if len(symbols) == 0 {
		return nil, nil
	}
	q := url.Values{}
	q.Set("limit", strconv.Itoa(max2(len(symbols)*2, 50)))
	for _, s := range symbols {
		q.Add("slug", stripPolyPrefix(s))
	}
	raw, err := p.fetch(p.base + "/markets?" + q.Encode())
	if err != nil {
		return nil, err
	}
	var rows []gammaMarket
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("polymarketPublic: decode batch: %w", err)
	}
	out := make([]*Mark, 0, len(rows))
	for _, m := range rows {
		mk, err := m.toMark("POLY:" + m.Slug)
		if err != nil {
			continue
		}
		out = append(out, mk)
	}
	return out, nil
}

func (p *polymarketPublic) fetch(u string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("User-Agent", "apteva-trading/0.2")
	resp, err := p.client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1024))
		return nil, fmt.Errorf("polymarketPublic: HTTP %d: %s", resp.StatusCode, string(body))
	}
	return io.ReadAll(io.LimitReader(resp.Body, 1<<20))
}

// gammaMarket — the subset of gamma-api's market object we read.
// Polymarket ships outcomes + outcomePrices as JSON-encoded strings
// inside the response (the gamma-api is opinionated that way), so we
// parse them at our boundary.
type gammaMarket struct {
	Slug           string `json:"slug"`
	Question       string `json:"question"`
	Outcomes       string `json:"outcomes"`        // e.g. "[\"Yes\",\"No\"]"
	OutcomePrices  string `json:"outcomePrices"`   // e.g. "[\"0.78\",\"0.22\"]"
	Volume24Hr     string `json:"volume24hr"`
	Closed         bool   `json:"closed"`
	EndDate        string `json:"endDate"`
}

func (m gammaMarket) toMark(internalSymbol string) (*Mark, error) {
	var outcomes []string
	var prices []string
	if err := json.Unmarshal([]byte(m.Outcomes), &outcomes); err != nil {
		return nil, fmt.Errorf("polymarketPublic: outcomes: %w", err)
	}
	if err := json.Unmarshal([]byte(m.OutcomePrices), &prices); err != nil {
		return nil, fmt.Errorf("polymarketPublic: prices: %w", err)
	}
	yesIdx, noIdx := indexOfCaseFold(outcomes, "Yes"), indexOfCaseFold(outcomes, "No")
	if yesIdx < 0 || noIdx < 0 {
		return nil, fmt.Errorf("polymarketPublic: market %q is not a binary YES/NO market", m.Slug)
	}
	yes, err := strconv.ParseFloat(prices[yesIdx], 64)
	if err != nil {
		return nil, fmt.Errorf("polymarketPublic: yes price: %w", err)
	}
	no, err := strconv.ParseFloat(prices[noIdx], 64)
	if err != nil {
		return nil, fmt.Errorf("polymarketPublic: no price: %w", err)
	}
	mk := &Mark{
		Symbol:     internalSymbol,
		AssetClass: "polymarket",
		Price:      yes,
		NoPrice:    &no,
		MarkedAt:   time.Now().UTC().Format(time.RFC3339),
	}
	if v, err := strconv.ParseFloat(m.Volume24Hr, 64); err == nil && v > 0 {
		mk.Volume24h = &v
	}
	return mk, nil
}

func indexOfCaseFold(xs []string, needle string) int {
	for i, x := range xs {
		if strings.EqualFold(x, needle) {
			return i
		}
	}
	return -1
}

func max2(a, b int) int {
	if a > b {
		return a
	}
	return b
}
