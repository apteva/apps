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

type polymarketMarketNotFoundError struct{ slug string }

func (e *polymarketMarketNotFoundError) Error() string {
	return fmt.Sprintf("polymarketPublic: no active market for slug %q", e.slug)
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
		return nil, &polymarketMarketNotFoundError{slug: slug}
	}
	return rows[0].toMark(symbol)
}

// ActiveMarkets discovers current liquid markets instead of relying on
// expiring slugs compiled into the app.
func (p *polymarketPublic) ActiveMarkets(limit int) ([]*Mark, error) {
	if limit <= 0 || limit > 100 {
		limit = 25
	}
	q := url.Values{}
	q.Set("active", "true")
	q.Set("closed", "false")
	q.Set("order", "volume24hr")
	q.Set("ascending", "false")
	q.Set("limit", strconv.Itoa(limit))
	raw, err := p.fetch(p.base + "/markets?" + q.Encode())
	if err != nil {
		return nil, err
	}
	var rows []gammaMarket
	if err := json.Unmarshal(raw, &rows); err != nil {
		return nil, fmt.Errorf("polymarketPublic: decode active markets: %w", err)
	}
	out := make([]*Mark, 0, len(rows))
	for _, market := range rows {
		if market.Closed {
			continue
		}
		mark, err := market.toMark("POLY:" + market.Slug)
		if err == nil {
			out = append(out, mark)
		}
	}
	return out, nil
}

func (p *polymarketPublic) fetch(u string) ([]byte, error) {
	return publicGET(context.Background(), p.client, "polymarketPublic", u, 1<<20, map[string]string{
		"Accept": "application/json", "User-Agent": "apteva-trading/0.4",
	})
}

// gammaMarket — the subset of gamma-api's market object we read.
// Polymarket ships outcomes + outcomePrices as JSON-encoded strings
// inside the response (the gamma-api is opinionated that way), so we
// parse them at our boundary.
type gammaMarket struct {
	Slug          string          `json:"slug"`
	Question      string          `json:"question"`
	Outcomes      string          `json:"outcomes"`      // e.g. "[\"Yes\",\"No\"]"
	OutcomePrices string          `json:"outcomePrices"` // e.g. "[\"0.78\",\"0.22\"]"
	Volume24Hr    json.RawMessage `json:"volume24hr"`
	Active        bool            `json:"active"`
	Closed        bool            `json:"closed"`
	EndDate       string          `json:"endDate"`
	UpdatedAt     string          `json:"updatedAt"`
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
	receivedAt := time.Now().UTC()
	markedAt := receivedAt
	timestampKind := "received"
	if updatedAt, err := time.Parse(time.RFC3339Nano, m.UpdatedAt); err == nil {
		markedAt = updatedAt.UTC()
		timestampKind = "exchange"
	}
	mk := &Mark{
		Symbol:        internalSymbol,
		AssetClass:    "polymarket",
		Price:         yes,
		NoPrice:       &no,
		MarkedAt:      markedAt.Format(time.RFC3339Nano),
		TimestampKind: timestampKind,
		Source:        "polymarket-public",
		VolumeUnit:    "quote_currency",
	}
	mk.Instrument = defaultInstrument(internalSymbol, "polymarket", "polymarket-public", receivedAt)
	mk.Instrument.ProviderSymbol = m.Slug
	mk.Instrument.Name = m.Question
	mk.Instrument.Active = m.Active && !m.Closed
	mk.Instrument.ExpiresAt = m.EndDate
	mk.Instrument.QuoteCurrency = "USDC"
	if v, err := parseGammaNumber(m.Volume24Hr); err == nil && v > 0 {
		mk.Volume24h = &v
	}
	return mk, nil
}

func parseGammaNumber(raw json.RawMessage) (float64, error) {
	if len(raw) == 0 || string(raw) == "null" {
		return 0, nil
	}
	var number float64
	if err := json.Unmarshal(raw, &number); err == nil {
		return number, nil
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return 0, err
	}
	return strconv.ParseFloat(text, 64)
}

func indexOfCaseFold(xs []string, needle string) int {
	for i, x := range xs {
		if strings.EqualFold(x, needle) {
			return i
		}
	}
	return -1
}
