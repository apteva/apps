package main

// Every data source goes through its integrations-catalog entry.
//
// Keyless sources (GDELT, Kalshi and Manifold reads, Polymarket market
// listings, BLS, Wikipedia, Yahoo charts) run through the platform's
// public integration route: no connection, no key, and the catalog
// tool's own pacing, timeout and retries apply. Keyed sources need a
// bound connection. Either way the app never speaks HTTP to an upstream
// itself.
//
// Callers keep using short, app-level call names ("markets_window",
// "doc_search" with RFC3339 dates, ...). catalogCall maps each one onto
// the catalog tool and its exact arguments.

import (
	"fmt"
	"strings"
	"sync"
)

// publicSlugs are the sources whose catalog tools we use run without
// credentials (the integration is keyless, or the tools are marked
// public). They need no binding.
var publicSlugs = map[string]bool{
	"polymarket":       true, // gamma market listings (trading stays keyed)
	"polymarket-data":  true,
	"gdelt":            true,
	"wikipedia":        true,
	"kalshi":           true,
	"manifold-markets": true,
	"bls":              true,
	"yahoo-finance":    true,
}

func isPublicSource(slug string) bool { return publicSlugs[slug] }

// catalogCall maps an app-level call onto the catalog tool and arguments.
// Calls without a mapping pass through unchanged (keyed sources already
// use catalog names).
func catalogCall(slug, tool string, args map[string]any) (string, map[string]any) {
	if args == nil {
		args = map[string]any{}
	}
	pick := func(keys ...string) map[string]any {
		out := map[string]any{}
		for _, k := range keys {
			if v, ok := args[k]; ok && v != nil && v != "" {
				out[k] = v
			}
		}
		return out
	}
	switch slug {
	case "polymarket":
		switch tool {
		case "markets", "market_price":
			// Accept either a bare slug or our POLY:<slug> canonical form.
			s := strings.TrimPrefix(strArgM(args, "slug"), "POLY:")
			if s == "" {
				s = strings.TrimPrefix(strArgM(args, "market"), "POLY:")
			}
			if s != "" {
				return "list_markets", map[string]any{"slug": s}
			}
			return "list_markets", map[string]any{"closed": false, "limit": 20}
		case "markets_window":
			// Open markets closing inside [end_date_min, end_date_max],
			// most traded first. Used by the question importer.
			out := pick("end_date_min", "end_date_max", "offset")
			out["closed"], out["active"] = false, true
			out["order"], out["ascending"] = "volumeNum", false
			out["limit"] = orStr(args["limit"], "100")
			return "list_markets", out
		case "search":
			// Gamma has no full-text market search: return the open set
			// and let the caller filter by question text.
			return "list_markets", map[string]any{"closed": false, "limit": 20}
		}
	case "polymarket-data":
		out := pick("user", "market", "limit", "type", "side", "interval")
		if tool == "get_holders" {
			tool = "get_market_holders"
		}
		return tool, out
	case "gdelt":
		out := map[string]any{"format": "json"}
		if q := strArgM(args, "query"); q != "" {
			out["query"] = q
		} else if t := strArgM(args, "topic"); t != "" {
			out["query"] = t
		}
		switch tool {
		case "doc_search":
			out["mode"], out["sort"] = "ArtList", "DateDesc"
			out["maxrecords"] = orStr(args["maxrecords"], "8")
			if v := strArgM(args, "timespan"); v != "" {
				out["timespan"] = v
			}
			// GDELT wants YYYYMMDDHHMMSS; accept RFC3339 / YYYY-MM-DD too.
			for _, k := range []string{"startdatetime", "enddatetime"} {
				if v := strArgM(args, k); v != "" {
					if t, err := parseOptionalTime(v); err == nil && t != nil {
						v = t.UTC().Format("20060102150405")
					}
					out[k] = v
				}
			}
		case "doc_timeline_volume":
			out["mode"] = "TimelineVol"
			out["timespan"] = orStr(args["timespan"], "30d")
		}
		return tool, out
	case "wikipedia":
		switch tool {
		case "page_summary":
			return tool, map[string]any{"title": strings.ReplaceAll(strArgM(args, "title"), " ", "_")}
		case "search":
			return tool, map[string]any{"action": "query", "list": "search", "format": "json",
				"srsearch": orStr(args["srsearch"], strArgM(args, "topic"))}
		}
	case "kalshi":
		return tool, pick("series_ticker", "event_ticker", "ticker", "status", "limit", "cursor", "min_close_ts", "max_close_ts", "mve_filter", "min_ts", "max_ts")
	case "manifold-markets":
		switch tool {
		case "search_markets":
			out := pick("filter", "sort", "contractType")
			out["term"] = orStr(args["term"], strArgM(args, "topic"))
			out["limit"] = orStr(args["limit"], "20")
			return tool, out
		case "list_markets":
			return tool, map[string]any{"limit": orStr(args["limit"], "50")}
		case "get_market":
			return tool, pick("id")
		}
	case "yahoo-finance":
		switch tool {
		case "stock_search":
			return tool, map[string]any{"q": strArgM(args, "query")}
		case "stock_chart":
			return tool, map[string]any{"symbol": strArgM(args, "symbol"),
				"range": orStr(args["range"], "10y"), "interval": orStr(args["interval"], "1d")}
		}
	}
	return tool, args
}

// sourceErrors keeps the latest failure reason per source, so connector
// status can say "rate limited (HTTP 429)" rather than just "failed".
var sourceErrors sync.Map // slug -> string

func setSourceError(slug, reason string) {
	if reason == "" {
		sourceErrors.Delete(slug)
		return
	}
	sourceErrors.Store(slug, reason)
}

func lastSourceError(slug string) string {
	v, _ := sourceErrors.Load(slug)
	s, _ := v.(string)
	return s
}

// failureReason turns an integration result into an operator-facing cause.
func failureReason(status int, data []byte, err error) string {
	switch {
	case err != nil:
		return err.Error()
	case status == 429:
		return "rate limited (HTTP 429)"
	case status >= 400:
		return fmt.Sprintf("HTTP %d", status)
	}
	if msg := strings.TrimSpace(string(data)); msg != "" {
		return truncate(msg, 160)
	}
	return "failed"
}
