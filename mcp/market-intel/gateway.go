package main

// The gateway — unified, normalized query functions. Each takes a
// sourceClient (the testable indirection) so the logic is exercised in
// unit tests with canned source responses, no live integrations needed.
//
// v0.1 implements: probability (sports odds de-vig), stats (entity
// profile fan-out), context (news fan-out + merge), enrich (the
// combined blob), sources_status. Cross-venue + macro-implied
// probability methods are stubbed to degrade gracefully until v0.2.

import (
	"encoding/json"
	"sort"
	"strings"
)

// ─── probability ───────────────────────────────────────────────────

type ProbabilityResult struct {
	FairProb float64  `json:"fair_prob"`
	Method   string   `json:"method"`             // odds_devig | cross_venue | macro_implied | unavailable
	Books    int      `json:"books,omitempty"`    // for odds_devig: how many bookmakers contributed
	Sources  []string `json:"sources"`
	Detail   string   `json:"detail,omitempty"`
}

// gwProbability computes the best ground-truth probability for an event.
// v0.1: sports via odds de-vig. params needs {domain, sport, entity}
// (entity = the outcome we want the prob of, e.g. "Carlos Alcaraz").
func gwProbability(sc sourceClient, params map[string]any) ProbabilityResult {
	domain := strArgM(params, "domain")
	target := strArgM(params, "entity")

	// Sports / tennis → odds de-vig.
	for _, spec := range specsFor(orStr(domain, "sports"), qOdds) {
		raw, ok := sc.call(spec.slug, spec.tool, spec.argFn(params))
		if !ok {
			continue
		}
		fair, books := fairProbFromOddsAPI(raw, target)
		if books > 0 {
			return ProbabilityResult{
				FairProb: fair, Method: "odds_devig", Books: books,
				Sources: []string{spec.slug},
			}
		}
	}

	return ProbabilityResult{
		Method:  "unavailable",
		Sources: []string{},
		Detail:  "no bound ground-truth source could answer (sports needs the-odds-api; macro/cross-venue methods land in v0.2)",
	}
}

// fairProbFromOddsAPI parses a the-odds-api /odds response and returns
// the de-vigged consensus probability of `target` across all
// bookmakers. Matches outcomes case-insensitively + by substring so
// "Alcaraz" matches "Carlos Alcaraz".
func fairProbFromOddsAPI(raw json.RawMessage, target string) (float64, int) {
	if len(raw) == 0 || strings.TrimSpace(target) == "" {
		return 0, 0
	}
	var events []struct {
		HomeTeam   string `json:"home_team"`
		AwayTeam   string `json:"away_team"`
		Bookmakers []struct {
			Key     string `json:"key"`
			Markets []struct {
				Key      string `json:"key"`
				Outcomes []struct {
					Name  string  `json:"name"`
					Price float64 `json:"price"`
				} `json:"outcomes"`
			} `json:"markets"`
		} `json:"bookmakers"`
	}
	if err := json.Unmarshal(raw, &events); err != nil {
		return 0, 0
	}
	tl := strings.ToLower(strings.TrimSpace(target))

	// Pick the event that mentions the target team.
	var bookOutcomes [][]float64
	targetIdxAcross := -1
	for _, ev := range events {
		if !strings.Contains(strings.ToLower(ev.HomeTeam), tl) &&
			!strings.Contains(strings.ToLower(ev.AwayTeam), tl) {
			continue
		}
		for _, bk := range ev.Bookmakers {
			for _, mk := range bk.Markets {
				if mk.Key != "h2h" {
					continue
				}
				prices := make([]float64, 0, len(mk.Outcomes))
				idx := -1
				for i, o := range mk.Outcomes {
					prices = append(prices, o.Price)
					if strings.Contains(strings.ToLower(o.Name), tl) {
						idx = i
					}
				}
				if idx >= 0 && len(prices) >= 2 {
					bookOutcomes = append(bookOutcomes, prices)
					targetIdxAcross = idx
				}
			}
		}
		break // matched event found; one event is enough
	}
	if targetIdxAcross < 0 || len(bookOutcomes) == 0 {
		return 0, 0
	}
	return consensusFairProb(bookOutcomes, targetIdxAcross)
}

// ─── market price (prediction-market venues) ──────────────────────

type MarketPrice struct {
	Venue    string   `json:"venue"`
	Question string   `json:"question,omitempty"`
	YesPrice *float64 `json:"yes_price,omitempty"`
	NoPrice  *float64 `json:"no_price,omitempty"`
	Closed   bool     `json:"closed"`
	Source   string   `json:"source"`
}

// gwMarketPrice fetches the current price of a prediction market from
// its venue. v0.1 fully parses Polymarket gamma (public, no key);
// kalshi/manifold return raw for the agent until their parsers land.
func gwMarketPrice(sc sourceClient, market string) *MarketPrice {
	for _, spec := range specsFor("prediction_market", qMarketPrice) {
		raw, ok := sc.call(spec.slug, spec.tool, spec.argFn(map[string]any{"market": market, "topic": market}))
		if !ok {
			continue
		}
		if spec.slug == "polymarket" {
			if mp := parsePolymarketGamma(raw); mp != nil {
				mp.Source = spec.slug
				return mp
			}
		}
		// Other venues: return the raw under a venue tag (parsers v0.2).
		// Skip if empty.
	}
	return nil
}

// parsePolymarketGamma reads gamma's /markets response. outcomePrices +
// outcomes arrive as JSON-encoded STRINGS (gamma is opinionated that
// way), e.g. outcomePrices: "[\"0.78\", \"0.22\"]". For a binary
// market, index 0 is YES.
func parsePolymarketGamma(raw json.RawMessage) *MarketPrice {
	var arr []struct {
		Question      string `json:"question"`
		OutcomePrices string `json:"outcomePrices"`
		Closed        bool   `json:"closed"`
	}
	if err := json.Unmarshal(raw, &arr); err != nil || len(arr) == 0 {
		return nil
	}
	m := arr[0]
	var prices []string
	_ = json.Unmarshal([]byte(m.OutcomePrices), &prices)
	mp := &MarketPrice{Venue: "polymarket", Question: m.Question, Closed: m.Closed}
	if len(prices) >= 1 {
		if y := parseFloatStr(prices[0]); y > 0 {
			mp.YesPrice = &y
		}
	}
	if len(prices) >= 2 {
		if n := parseFloatStr(prices[1]); n > 0 {
			mp.NoPrice = &n
		}
	}
	return mp
}

func parseFloatStr(s string) float64 {
	var f float64
	// json.Unmarshal of a quoted number string fails; parse manually.
	if _, err := jsonNumber(s, &f); err != nil {
		return 0
	}
	return f
}

// jsonNumber parses a decimal string into a float without importing
// strconv at the call site (keeps the parse local + tolerant).
func jsonNumber(s string, out *float64) (bool, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return false, errEmpty
	}
	return true, json.Unmarshal([]byte(s), out)
}

var errEmpty = jsonErr("empty")

type jsonErr string

func (e jsonErr) Error() string { return string(e) }

// ─── stats ─────────────────────────────────────────────────────────

type StatsResult struct {
	Entity  string                     `json:"entity"`
	Domain  string                     `json:"domain"`
	Data    map[string]json.RawMessage `json:"data"`    // queryType → raw normalized payload
	Sources []string                   `json:"sources"`
}

// gwStats fans the relevant query types for a domain and merges raw
// results. v0.1 returns the source payloads keyed by query type +
// records provenance; deeper field-level normalization per query type
// is incremental (each queryType gets its own normalizer over time).
func gwStats(sc sourceClient, entity, domain string) StatsResult {
	out := StatsResult{Entity: entity, Domain: domain, Data: map[string]json.RawMessage{}, Sources: []string{}}
	seen := map[string]bool{}
	for _, qt := range []queryType{qRanking, qForm} {
		for _, spec := range specsFor(domain, qt) {
			params := map[string]any{"entity": entity, "surface": ""}
			raw, ok := sc.call(spec.slug, spec.tool, spec.argFn(params))
			if !ok {
				continue
			}
			out.Data[string(qt)] = raw
			if !seen[spec.slug] {
				out.Sources = append(out.Sources, spec.slug)
				seen[spec.slug] = true
			}
			break // first source that answers this query type wins
		}
	}
	return out
}

// gwHistory — head-to-head between two entities (or a single entity's
// series). v0.1 covers H2H.
func gwHistory(sc sourceClient, entityA, entityB, domain string) (map[string]any, []string) {
	sources := []string{}
	out := map[string]any{}
	if entityB != "" {
		for _, spec := range specsFor(domain, qH2H) {
			raw, ok := sc.call(spec.slug, spec.tool, spec.argFn(map[string]any{
				"entity_a": entityA, "entity_b": entityB,
			}))
			if !ok {
				continue
			}
			out["h2h"] = json.RawMessage(raw)
			sources = append(sources, spec.slug)
			break
		}
	}
	return out, sources
}

// ─── context (news) ────────────────────────────────────────────────

type NewsItem struct {
	Title  string `json:"title"`
	URL    string `json:"url,omitempty"`
	Source string `json:"source,omitempty"`
}

type ContextResult struct {
	Topic   string     `json:"topic"`
	Items   []NewsItem `json:"items"`
	Sources []string   `json:"sources"`
}

// gwContext fans the news query across every bound news source, merges
// + dedups by normalized title. v0.1 dedup is title-prefix based;
// sentiment scoring is v0.2.
func gwContext(sc sourceClient, topic string) ContextResult {
	out := ContextResult{Topic: topic, Items: []NewsItem{}, Sources: []string{}}
	seenTitle := map[string]bool{}
	for _, spec := range specsFor("*", qNews) {
		raw, ok := sc.call(spec.slug, spec.tool, spec.argFn(map[string]any{"topic": topic}))
		if !ok {
			continue
		}
		items := parseNews(raw, spec.slug)
		if len(items) == 0 {
			continue
		}
		out.Sources = append(out.Sources, spec.slug)
		for _, it := range items {
			key := normTitle(it.Title)
			if key == "" || seenTitle[key] {
				continue
			}
			seenTitle[key] = true
			out.Items = append(out.Items, it)
		}
	}
	// Stable-ish ordering: alphabetical by title so output is
	// deterministic for tests.
	sort.SliceStable(out.Items, func(i, j int) bool { return out.Items[i].Title < out.Items[j].Title })
	return out
}

// parseNews extracts a best-effort list of {title,url} from the various
// news-source response shapes. Tavily: {results:[{title,url}]}.
// NewsAPI: {articles:[{title,url,source:{name}}]}. GNews: {articles:[{title,url,source:{name}}]}.
// GDELT: {articles:[{title,url,domain}]}.
func parseNews(raw json.RawMessage, slug string) []NewsItem {
	var generic struct {
		Results []struct {
			Title string `json:"title"`
			URL   string `json:"url"`
		} `json:"results"`
		Articles []struct {
			Title  string `json:"title"`
			URL    string `json:"url"`
			Domain string `json:"domain"`
			Source struct {
				Name string `json:"name"`
			} `json:"source"`
		} `json:"articles"`
	}
	_ = json.Unmarshal(raw, &generic)
	out := []NewsItem{}
	for _, r := range generic.Results { // tavily
		if r.Title != "" {
			out = append(out, NewsItem{Title: r.Title, URL: r.URL, Source: slug})
		}
	}
	for _, a := range generic.Articles { // newsapi / gnews / gdelt
		if a.Title == "" {
			continue
		}
		src := a.Source.Name
		if src == "" {
			src = a.Domain
		}
		if src == "" {
			src = slug
		}
		out = append(out, NewsItem{Title: a.Title, URL: a.URL, Source: src})
	}
	return out
}

func normTitle(t string) string {
	t = strings.ToLower(strings.TrimSpace(t))
	if len(t) > 60 {
		t = t[:60]
	}
	return t
}

// ─── sources_status ────────────────────────────────────────────────

func allRegistrySlugs() []string {
	seen := map[string]bool{}
	out := []string{}
	for _, byQuery := range registry {
		for _, specs := range byQuery {
			for _, s := range specs {
				if !seen[s.slug] {
					seen[s.slug] = true
					out = append(out, s.slug)
				}
			}
		}
	}
	sort.Strings(out)
	return out
}

func gwSourcesStatus(sc sourceClient) map[string]any {
	slugs := allRegistrySlugs()
	bound := sc.boundSlugs(slugs)
	rows := make([]map[string]any, 0, len(slugs))
	for _, s := range slugs {
		pub := isPublicSource(s)
		rows = append(rows, map[string]any{
			"slug":      s,
			"bound":     bound[s],
			"public":    pub,
			"available": pub || bound[s], // public works without binding
		})
	}
	return map[string]any{"sources": rows}
}

func strArgM(m map[string]any, k string) string {
	if s, ok := m[k].(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}
