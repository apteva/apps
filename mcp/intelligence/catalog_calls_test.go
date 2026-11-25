package main

import (
	"context"
	"encoding/json"
	"reflect"
	"testing"

	sdk "github.com/apteva/app-sdk"
	"github.com/apteva/app-sdk/testkit"
)

// publicPlatform answers keyless integration calls and records them.
type publicPlatform struct {
	testkit.BasePlatformClient
	conns []sdk.PlatformConnection
	calls []string
	args  []map[string]any
	reply func(slug, tool string) *sdk.ExecuteResult
}

func (p *publicPlatform) ListConnections(f sdk.ConnectionFilter) ([]sdk.PlatformConnection, error) {
	var out []sdk.PlatformConnection
	for _, c := range p.conns {
		if c.AppSlug == f.AppSlug {
			out = append(out, c)
		}
	}
	return out, nil
}

func (p *publicPlatform) ExecutePublicIntegrationToolContext(_ context.Context, slug, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	p.calls = append(p.calls, "public:"+slug+"."+tool)
	p.args = append(p.args, input)
	return p.reply(slug, tool), nil
}

func (p *publicPlatform) ExecuteIntegrationToolContext(_ context.Context, id int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	p.calls = append(p.calls, "conn:"+tool)
	p.args = append(p.args, input)
	return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"via":"connection"}`)}, nil
}

func TestCatalogCallMapsAppCallsOntoCatalogTools(t *testing.T) {
	for _, tc := range []struct {
		slug, tool string
		args       map[string]any
		wantTool   string
		want       map[string]any
	}{
		{"polymarket", "market_price", map[string]any{"market": "POLY:fed-cut"}, "list_markets", map[string]any{"slug": "fed-cut"}},
		{"polymarket", "markets_window", map[string]any{"end_date_min": "a", "end_date_max": "b", "limit": "200"}, "list_markets",
			map[string]any{"end_date_min": "a", "end_date_max": "b", "closed": false, "active": true, "order": "volumeNum", "ascending": false, "limit": "200"}},
		{"polymarket-data", "get_holders", map[string]any{"market": "m", "junk": "x"}, "get_market_holders", map[string]any{"market": "m"}},
		{"gdelt", "doc_search", map[string]any{"query": "fuel", "maxrecords": "250", "startdatetime": "2026-11-20T09:00:00Z"}, "doc_search",
			map[string]any{"format": "json", "query": "fuel", "mode": "ArtList", "sort": "DateDesc", "maxrecords": "250", "startdatetime": "20261120090000"}},
		{"kalshi", "get_event", map[string]any{"event_ticker": "KX-1", "other": 1}, "get_event", map[string]any{"event_ticker": "KX-1"}},
		{"manifold-markets", "search_markets", map[string]any{"topic": "fed"}, "search_markets", map[string]any{"term": "fed", "limit": "20"}},
		{"wikipedia", "page_summary", map[string]any{"title": "Brent Crude"}, "page_summary", map[string]any{"title": "Brent_Crude"}},
		{"yahoo-finance", "stock_search", map[string]any{"query": "shell"}, "stock_search", map[string]any{"q": "shell"}},
		{"bls", "get_series_single", map[string]any{"seriesid": "CUUR0000SA0"}, "get_series_single", map[string]any{"seriesid": "CUUR0000SA0"}},
		{"fred", "series_observations", map[string]any{"series_id": "X"}, "series_observations", map[string]any{"series_id": "X"}},
	} {
		tool, args := catalogCall(tc.slug, tc.tool, tc.args)
		if tool != tc.wantTool || !reflect.DeepEqual(args, tc.want) {
			t.Errorf("%s.%s -> %s %v, want %s %v", tc.slug, tc.tool, tool, args, tc.wantTool, tc.want)
		}
	}
}

func TestSourceClientRoutesThroughIntegrations(t *testing.T) {
	plat := &publicPlatform{
		conns: []sdk.PlatformConnection{{ID: 7, AppSlug: "bls", Status: "active"}, {ID: 8, AppSlug: "fred", Status: "active"}},
		reply: func(slug, tool string) *sdk.ExecuteResult {
			if slug == "gdelt" {
				return &sdk.ExecuteResult{Success: false, Status: 429, Data: json.RawMessage(`"Please limit requests"`)}
			}
			return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"ok":true}`)}
		},
	}
	ctx := testkit.NewAppCtx(t, "apteva.yaml", testkit.WithPlatform(plat))
	sc := newPlatformSourceClient(ctx)

	if raw, ok := sc.call("kalshi", "get_event", map[string]any{"event_ticker": "KX-1"}); !ok || string(raw) != `{"ok":true}` {
		t.Fatalf("keyless call = %s %v", raw, ok)
	}
	// A bound connection wins over the keyless route.
	if raw, ok := sc.call("bls", "get_series_single", map[string]any{"seriesid": "S"}); !ok || string(raw) != `{"via":"connection"}` {
		t.Fatalf("bound call = %s %v", raw, ok)
	}
	if _, ok := sc.call("fred", "series_observations", nil); !ok {
		t.Fatal("keyed source with a connection failed")
	}
	// A keyed source without a connection is not attempted.
	if _, ok := sc.call("newsapi", "everything", nil); ok {
		t.Fatal("unbound keyed source answered")
	}
	// Upstream failures surface their cause for connector status.
	if _, ok := sc.call("gdelt", "doc_search", map[string]any{"query": "fuel"}); ok {
		t.Fatal("429 treated as success")
	}
	if got := lastSourceError("gdelt"); got != "rate limited (HTTP 429)" {
		t.Fatalf("gdelt error = %q", got)
	}
	want := []string{"public:kalshi.get_event", "conn:get_series_single", "conn:series_observations", "public:gdelt.doc_search"}
	if !reflect.DeepEqual(plat.calls, want) {
		t.Fatalf("calls = %v, want %v", plat.calls, want)
	}
}
