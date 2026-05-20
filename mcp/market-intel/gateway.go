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
		rows = append(rows, map[string]any{"slug": s, "bound": bound[s]})
	}
	return map[string]any{"sources": rows}
}

func strArgM(m map[string]any, k string) string {
	if s, ok := m[k].(string); ok {
		return strings.TrimSpace(s)
	}
	return ""
}
