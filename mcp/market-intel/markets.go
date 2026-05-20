package main

// Live-markets board — pulls active markets from the public prediction
// venues (Polymarket gamma, Kalshi, Manifold) so the panel has real
// data to show the moment it opens, zero setup. All three are public
// (direct HTTP, no key), so this works on a fresh install.

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"
)

type MarketRow struct {
	Venue     string   `json:"venue"`
	ID        string   `json:"id"`        // slug / ticker
	Question  string   `json:"question"`
	YesPrice  *float64 `json:"yes_price,omitempty"`
	Volume    *float64 `json:"volume,omitempty"`
	CloseTime string   `json:"close_time,omitempty"`
	URL       string   `json:"url,omitempty"`
}

// gwListMarkets fans the public venues + merges into one volume-ranked
// board. limit caps the final list. Each venue that's unreachable is
// just absent — never an error.
func gwListMarkets(sc sourceClient, limit int) []MarketRow {
	if limit <= 0 {
		limit = 30
	}
	rows := []MarketRow{}

	if raw, ok := sc.call("polymarket", "markets", map[string]any{}); ok {
		rows = append(rows, parsePolymarketList(raw)...)
	}
	if raw, ok := sc.call("manifold-markets", "list_markets", map[string]any{"limit": strconv.Itoa(limit)}); ok {
		rows = append(rows, parseManifoldList(raw)...)
	}
	if raw, ok := sc.call("kalshi", "list_markets", map[string]any{"status": "open", "limit": strconv.Itoa(limit)}); ok {
		rows = append(rows, parseKalshiList(raw)...)
	}

	// Rank by volume desc (nil volume sinks to the bottom).
	sort.SliceStable(rows, func(i, j int) bool {
		vi, vj := 0.0, 0.0
		if rows[i].Volume != nil {
			vi = *rows[i].Volume
		}
		if rows[j].Volume != nil {
			vj = *rows[j].Volume
		}
		return vi > vj
	})
	if len(rows) > limit {
		rows = rows[:limit]
	}
	return rows
}

// parsePolymarketList — gamma /markets array. outcomePrices is a
// JSON-encoded string; volumeNum is the numeric 24h-ish volume.
func parsePolymarketList(raw json.RawMessage) []MarketRow {
	var arr []struct {
		Question      string  `json:"question"`
		Slug          string  `json:"slug"`
		OutcomePrices string  `json:"outcomePrices"`
		VolumeNum     float64 `json:"volumeNum"`
		Volume        string  `json:"volume"`
		EndDate       string  `json:"endDate"`
		Closed        bool    `json:"closed"`
	}
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil
	}
	out := make([]MarketRow, 0, len(arr))
	for _, m := range arr {
		if m.Closed || m.Question == "" {
			continue
		}
		row := MarketRow{
			Venue:     "polymarket",
			ID:        m.Slug,
			Question:  m.Question,
			CloseTime: m.EndDate,
			URL:       "https://polymarket.com/event/" + m.Slug,
		}
		var prices []string
		if json.Unmarshal([]byte(m.OutcomePrices), &prices) == nil && len(prices) >= 1 {
			if y := parseFloatStr(prices[0]); y > 0 {
				row.YesPrice = &y
			}
		}
		vol := m.VolumeNum
		if vol == 0 {
			vol = parseFloatStr(m.Volume)
		}
		if vol > 0 {
			row.Volume = &vol
		}
		out = append(out, row)
	}
	return out
}

// parseManifoldList — /markets array. probability is the binary YES
// prob directly (0-1). Only binary, unresolved markets make the board.
func parseManifoldList(raw json.RawMessage) []MarketRow {
	var arr []struct {
		Question    string  `json:"question"`
		Slug        string  `json:"slug"`
		URL         string  `json:"url"`
		Probability float64 `json:"probability"`
		Volume      float64 `json:"volume"`
		OutcomeType string  `json:"outcomeType"`
		IsResolved  bool    `json:"isResolved"`
		CloseTime   int64   `json:"closeTime"`
	}
	if err := json.Unmarshal(raw, &arr); err != nil {
		return nil
	}
	out := make([]MarketRow, 0, len(arr))
	for _, m := range arr {
		if m.IsResolved || m.OutcomeType != "BINARY" || m.Question == "" {
			continue
		}
		row := MarketRow{Venue: "manifold", ID: m.Slug, Question: m.Question, URL: m.URL}
		if m.Probability > 0 {
			p := m.Probability
			row.YesPrice = &p
		}
		if m.Volume > 0 {
			v := m.Volume
			row.Volume = &v
		}
		out = append(out, row)
	}
	return out
}

// parseKalshiList — {markets:[...]}. yes_bid / yes_ask are cents (1-99);
// mid / 100 is the implied YES probability.
func parseKalshiList(raw json.RawMessage) []MarketRow {
	var resp struct {
		Markets []struct {
			Ticker    string  `json:"ticker"`
			Title     string  `json:"title"`
			YesBid    float64 `json:"yes_bid"`
			YesAsk    float64 `json:"yes_ask"`
			Volume    float64 `json:"volume"`
			Status    string  `json:"status"`
			CloseTime string  `json:"close_time"`
		} `json:"markets"`
	}
	if err := json.Unmarshal(raw, &resp); err != nil {
		return nil
	}
	out := make([]MarketRow, 0, len(resp.Markets))
	for _, m := range resp.Markets {
		if m.Title == "" {
			continue
		}
		row := MarketRow{
			Venue: "kalshi", ID: m.Ticker, Question: m.Title, CloseTime: m.CloseTime,
			URL: "https://kalshi.com/markets/" + strings.ToLower(m.Ticker),
		}
		if m.YesBid > 0 || m.YesAsk > 0 {
			mid := (m.YesBid + m.YesAsk) / 2 / 100.0
			row.YesPrice = &mid
		}
		if m.Volume > 0 {
			v := m.Volume
			row.Volume = &v
		}
		out = append(out, row)
	}
	return out
}
