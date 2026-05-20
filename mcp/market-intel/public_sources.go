package main

// Direct-HTTP clients for the PUBLIC, no-auth data sources. These need
// no operator binding and no API key — market-intel hits them straight
// over the wire (covered by the net.egress permission), exactly the way
// the trading app's binance_public.go / polymarket_public.go do.
//
// Keyed sources (the-odds-api, fred, finnhub, newsapi, …) still go
// through a bound integration connection via ExecuteIntegrationTool —
// they genuinely need credentials. The sourceClient tries this public
// path first; only if a slug isn't public does it fall back to the
// bound-connection path.

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

// publicSlugs — sources answerable with zero auth + zero binding.
var publicSlugs = map[string]bool{
	"polymarket":       true, // gamma-api (prices/metadata)
	"polymarket-data":  true, // data-api (holders/OI/positions)
	"gdelt":            true,
	"wikipedia":        true,
	"kalshi":           true, // public market reads
	"manifold-markets": true,
}

func isPublicSource(slug string) bool { return publicSlugs[slug] }

var publicHTTP = &http.Client{Timeout: 6 * time.Second}

// directPublicCall dispatches a (slug, tool, args) to the right public
// HTTP endpoint. Returns (data, true) on success; (nil, false) if the
// slug isn't public or the call failed (caller then tries the bound
// path / next spec).
func directPublicCall(slug, tool string, args map[string]any) (json.RawMessage, bool) {
	if !publicSlugs[slug] {
		return nil, false
	}
	switch slug {
	case "polymarket":
		return polymarketGamma(tool, args)
	case "polymarket-data":
		return polymarketDataAPI(tool, args)
	case "gdelt":
		return gdeltCall(tool, args)
	case "wikipedia":
		return wikipediaCall(tool, args)
	case "kalshi":
		return kalshiCall(tool, args)
	case "manifold-markets":
		return manifoldCall(tool, args)
	}
	return nil, false
}

// ─── Polymarket gamma-api (prices / metadata) ──────────────────────

func polymarketGamma(tool string, args map[string]any) (json.RawMessage, bool) {
	base := "https://gamma-api.polymarket.com"
	switch tool {
	case "markets", "market_price":
		q := url.Values{}
		// Accept either a bare slug or our POLY:<slug> canonical form.
		slug := strings.TrimPrefix(strArgM(args, "slug"), "POLY:")
		if slug == "" {
			slug = strings.TrimPrefix(strArgM(args, "market"), "POLY:")
		}
		if slug != "" {
			q.Set("slug", slug)
		} else {
			q.Set("closed", "false")
			q.Set("limit", "20")
		}
		return getJSON(base + "/markets?" + q.Encode())
	case "search":
		q := url.Values{}
		q.Set("limit", "20")
		if t := strArgM(args, "topic"); t != "" {
			// gamma has no full-text market search; closest is the
			// public /markets list filtered client-side. Return the
			// active set; the gateway/agent filters by question text.
			q.Set("closed", "false")
		}
		return getJSON(base + "/markets?" + q.Encode())
	}
	return nil, false
}

// ─── Polymarket data-api (holders / OI / positions) ────────────────

func polymarketDataAPI(tool string, args map[string]any) (json.RawMessage, bool) {
	base := "https://data-api.polymarket.com"
	q := url.Values{}
	for _, k := range []string{"user", "market", "limit", "type", "side", "interval"} {
		if v := strArgM(args, k); v != "" {
			q.Set(k, v)
		}
	}
	path := ""
	switch tool {
	case "get_positions":
		path = "/positions"
	case "get_holders", "get_market_holders":
		path = "/holders"
	case "get_open_interest":
		path = "/open-interest"
	case "get_leaderboard":
		path = "/leaderboard"
	case "get_trades":
		path = "/trades"
	default:
		return nil, false
	}
	return getJSON(base + path + "?" + q.Encode())
}

// ─── GDELT (public news/events) ────────────────────────────────────

func gdeltCall(tool string, args map[string]any) (json.RawMessage, bool) {
	base := "https://api.gdeltproject.org/api/v2"
	q := url.Values{}
	q.Set("format", "json")
	if query := strArgM(args, "query"); query != "" {
		q.Set("query", query)
	} else if t := strArgM(args, "topic"); t != "" {
		q.Set("query", t)
	}
	switch tool {
	case "doc_search":
		q.Set("mode", "ArtList")
		q.Set("maxrecords", orStr(args["maxrecords"], "8"))
		return getJSON(base + "/doc/doc?" + q.Encode())
	case "doc_timeline_volume":
		q.Set("mode", "TimelineVol")
		q.Set("timespan", orStr(args["timespan"], "30d"))
		return getJSON(base + "/doc/doc?" + q.Encode())
	}
	return nil, false
}

// ─── Wikipedia ─────────────────────────────────────────────────────

func wikipediaCall(tool string, args map[string]any) (json.RawMessage, bool) {
	switch tool {
	case "page_summary":
		title := url.PathEscape(strings.ReplaceAll(strArgM(args, "title"), " ", "_"))
		return getJSON("https://en.wikipedia.org/api/rest_v1/page/summary/" + title)
	case "search":
		q := url.Values{}
		q.Set("action", "query")
		q.Set("list", "search")
		q.Set("format", "json")
		q.Set("srsearch", orStr(args["srsearch"], strArgM(args, "topic")))
		return getJSON("https://en.wikipedia.org/w/api.php?" + q.Encode())
	}
	return nil, false
}

// ─── Kalshi (public market reads) ──────────────────────────────────

func kalshiCall(tool string, args map[string]any) (json.RawMessage, bool) {
	base := "https://api.elections.kalshi.com/trade-api/v2"
	q := url.Values{}
	for _, k := range []string{"series_ticker", "event_ticker", "ticker", "status", "limit", "cursor"} {
		if v := strArgM(args, k); v != "" {
			q.Set(k, v)
		}
	}
	switch tool {
	case "list_markets":
		return getJSON(base + "/markets?" + q.Encode())
	case "list_events":
		return getJSON(base + "/events?" + q.Encode())
	case "get_market":
		t := strArgM(args, "ticker")
		if t == "" {
			return nil, false
		}
		return getJSON(base + "/markets/" + url.PathEscape(t))
	case "exchange_status":
		return getJSON(base + "/exchange/status")
	}
	return nil, false
}

// ─── Manifold (public market reads) ────────────────────────────────

func manifoldCall(tool string, args map[string]any) (json.RawMessage, bool) {
	base := "https://api.manifold.markets/v0"
	switch tool {
	case "search_markets":
		q := url.Values{}
		q.Set("term", orStr(args["term"], strArgM(args, "topic")))
		q.Set("limit", orStr(args["limit"], "20"))
		return getJSON(base + "/search-markets?" + q.Encode())
	case "list_markets":
		q := url.Values{}
		q.Set("limit", orStr(args["limit"], "50"))
		return getJSON(base + "/markets?" + q.Encode())
	case "get_market":
		id := strArgM(args, "id")
		if id == "" {
			return nil, false
		}
		return getJSON(base + "/market/" + url.PathEscape(id))
	}
	return nil, false
}

// ─── HTTP helper ───────────────────────────────────────────────────

func getJSON(u string) (json.RawMessage, bool) {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return nil, false
	}
	req.Header.Set("User-Agent", "Apteva-MarketIntel/0.1 (https://apteva.com)")
	req.Header.Set("Accept", "application/json")
	resp, err := publicHTTP.Do(req)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, false
	}
	body, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil || len(body) == 0 {
		return nil, false
	}
	return json.RawMessage(body), true
}
