package main

// Scheduled connectors. Each one turns an external source into recorded
// evidence, questions, prices or state values, and reports what it did in
// an ingest run. Connectors never delete: evidence is append-only.

import (
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
)

type ingestEnv struct {
	db  *sql.DB
	pid string
	sc  sourceClient
	llm llmClient
	now time.Time
}

type connectorFunc func(env *ingestEnv, src IntelSource) (ingestStats, error)

var connectors = map[string]connectorFunc{
	"prediction_markets": runPredictionMarkets,
	"news":               runNews,
	"bls":                runBLS,
	"fred":               runFRED,
}

// defaultSources are created on first mount. Operators change enablement,
// interval and config through source_configure; existing rows are never
// overwritten.
func defaultSources() []IntelSource {
	return []IntelSource{
		{Slug: "prediction-markets", Connector: "prediction_markets", Enabled: true, IntervalSec: 900,
			PermittedUse: "Read-only public market metadata, prices and resolutions; no trading.",
			Licence:      "Polymarket, Kalshi and Manifold public APIs (venue terms apply)",
			Config: map[string]any{"venues": []string{"polymarket", "kalshi", "manifold"}, "min_horizon_hours": 12, "max_horizon_days": 10,
				"min_volume_polymarket": 20000, "min_volume_kalshi": 500, "min_volume_manifold": 1000, "max_new_per_run": 25, "max_open": 200,
				"min_price": 0.03, "max_price": 0.97, "max_per_topic": 2, "max_per_event": 2, "max_per_template": 2, "exclude_keywords": defaultExcludedQuestionKeywords}},
		{Slug: "news", Connector: "news", Enabled: true, IntervalSec: 1800,
			PermittedUse: "Public news metadata (headline, outlet, link, date) for evidence; full text is not stored.",
			Licence:      "GDELT open data with attribution; bound news APIs under their own terms",
			Config:       map[string]any{"questions_per_run": 8, "timespan": "3d", "watch_topics": []string{}}},
		{Slug: "bls", Connector: "bls", Enabled: true, IntervalSec: 21600,
			PermittedUse: "Public statistical series.", Licence: "US Bureau of Labor Statistics, public domain",
			Config: map[string]any{"series": []map[string]string{
				{"id": "CUUR0000SA0", "name": "US CPI-U all items", "unit": "index 1982-84=100"},
				{"id": "LNS14000000", "name": "US unemployment rate", "unit": "percent"},
				{"id": "CES0000000001", "name": "US nonfarm payrolls", "unit": "thousands"},
			}}},
		{Slug: "fred", Connector: "fred", Enabled: false, IntervalSec: 21600,
			PermittedUse: "Public economic series via the operator's bound FRED connection (bind FRED, then enable).", Licence: "FRED terms of use",
			Config: map[string]any{"series": []map[string]string{
				{"id": "DFF", "name": "US effective federal funds rate", "unit": "percent"},
				{"id": "DGS10", "name": "US 10-year Treasury yield", "unit": "percent"},
			}}},
	}
}

// defaultExcludedQuestionKeywords keeps market questions about private
// lives and individual conduct out of the question set (posting volume,
// relationships, health, arrests). Matching is case-insensitive substring.
var defaultExcludedQuestionKeywords = []string{"tweet", "post ", "posts ", "affair", "divorce", "pregnan", "dating", "girlfriend", "boyfriend",
	"wedding", "engaged", "baby", "arrest", "indict", "sentenced", "prison", "jail", "die ", "dies", "death of", "health", "hospital", "onlyfans"}

// walletAddress catches markets about individual crypto wallets.
var walletAddress = regexp.MustCompile(`0x[0-9a-fA-F]{6,}`)

func excludedQuestion(text string, keywords []string) bool {
	if walletAddress.MatchString(text) {
		return true
	}
	t := " " + strings.ToLower(text) + " "
	for _, k := range keywords {
		if k != "" && strings.Contains(t, strings.ToLower(k)) {
			return true
		}
	}
	return false
}

// countingClient counts upstream calls for the source's cost meter.
type countingClient struct {
	inner sourceClient
	calls int
}

func (c *countingClient) call(slug, tool string, args map[string]any) (json.RawMessage, bool) {
	c.calls++
	return c.inner.call(slug, tool, args)
}
func (c *countingClient) boundSlugs(candidates []string) map[string]bool {
	return c.inner.boundSlugs(candidates)
}

// runDueSources runs each enabled source whose interval has elapsed (or
// all of them when force is set, optionally limited to one slug).
func runDueSources(env *ingestEnv, force bool, only string) ([]map[string]any, error) {
	sources, err := listSources(env.db, env.pid)
	if err != nil {
		return nil, err
	}
	// Markets first: news and extraction work from the questions it creates.
	priority := map[string]int{"prediction_markets": 0, "news": 2}
	sort.SliceStable(sources, func(i, j int) bool {
		pi, ok := priority[sources[i].Connector]
		if !ok {
			pi = 1
		}
		pj, ok := priority[sources[j].Connector]
		if !ok {
			pj = 1
		}
		return pi < pj
	})
	out := []map[string]any{}
	for _, src := range sources {
		if only != "" && src.Slug != only {
			continue
		}
		if src.Connector == "extraction" || (!src.Enabled && only == "") {
			continue // extraction runs as its own cycle step
		}
		if !force && src.LastRunAt != nil && env.now.Sub(*src.LastRunAt) < time.Duration(src.IntervalSec)*time.Second {
			continue
		}
		fn := connectors[src.Connector]
		if fn == nil {
			out = append(out, map[string]any{"source": src.Slug, "error": "unknown connector " + src.Connector})
			continue
		}
		runID, err := startIngestRun(env.db, env.pid, src.Slug)
		if err != nil {
			return out, err
		}
		cc := &countingClient{inner: env.sc}
		sub := *env
		sub.sc = cc
		st, runErr := fn(&sub, src)
		st.Calls = cc.calls
		finishIngestRun(env.db, env.pid, src.Slug, runID, st, runErr)
		row := map[string]any{"source": src.Slug, "fetched": st.Fetched, "inserted": st.Inserted, "duplicates": st.Duplicates, "calls": st.Calls}
		if runErr != nil {
			row["error"] = runErr.Error()
		}
		out = append(out, row)
	}
	return out, nil
}

// ─── Prediction markets: questions, prices, resolutions ────────────

type venueMarket struct {
	Venue, ID, Question, Rule, URL, Category string
	Event, EventTitle                        string // venue event grouping (Polymarket event slug, Kalshi event_ticker)
	Template                                 string // questionTemplate of the venue's own question text
	Close                                    time.Time
	Yes                                      *float64
	Volume                                   float64
	Resolved                                 bool
	Outcome                                  *int // 1 yes, 0 no; nil with Resolved = void/cancelled
}

func cfgFloat(cfg map[string]any, key string, def float64) float64 {
	switch v := cfg[key].(type) {
	case float64:
		return v
	case int:
		return float64(v)
	case string:
		if f, err := strconv.ParseFloat(v, 64); err == nil {
			return f
		}
	}
	return def
}

func cfgStrings(cfg map[string]any, key string) []string {
	out := []string{}
	switch v := cfg[key].(type) {
	case []any:
		for _, x := range v {
			if s, ok := x.(string); ok && strings.TrimSpace(s) != "" {
				out = append(out, strings.TrimSpace(s))
			}
		}
	case []string:
		out = append(out, v...)
	case string:
		for _, s := range strings.Split(v, ",") {
			if s = strings.TrimSpace(s); s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

func runPredictionMarkets(env *ingestEnv, src IntelSource) (ingestStats, error) {
	st := ingestStats{}
	cfg := src.Config
	venues := cfgStrings(cfg, "venues")
	if len(venues) == 0 {
		venues = []string{"polymarket", "kalshi", "manifold"}
	}
	minClose := env.now.Add(time.Duration(cfgFloat(cfg, "min_horizon_hours", 12)) * time.Hour)
	maxClose := env.now.Add(time.Duration(cfgFloat(cfg, "max_horizon_days", 10)*24) * time.Hour)
	maxNew := int(cfgFloat(cfg, "max_new_per_run", 25))
	maxOpen := int(cfgFloat(cfg, "max_open", 200))

	open, err := listQuestions(env.db, env.pid, "open", 5000)
	if err != nil {
		return st, err
	}
	listed := []venueMarket{}
	for _, v := range venues {
		var rows []venueMarket
		switch v {
		case "polymarket":
			raw, ok := env.sc.call("polymarket", "markets_window", map[string]any{"end_date_min": ts(minClose), "end_date_max": ts(maxClose), "limit": "200"})
			if ok {
				rows = parsePolymarketWindow(raw)
			}
		case "kalshi":
			raw, ok := env.sc.call("kalshi", "list_markets", map[string]any{"status": "open", "limit": "500", "mve_filter": "exclude",
				"min_close_ts": strconv.FormatInt(minClose.Unix(), 10), "max_close_ts": strconv.FormatInt(maxClose.Unix(), 10)})
			if ok {
				rows = parseKalshiMarkets(raw)
			}
		case "manifold":
			raw, ok := env.sc.call("manifold-markets", "search_markets", map[string]any{"term": "", "filter": "open", "sort": "24-hour-vol", "contractType": "BINARY", "limit": "500"})
			if ok {
				rows = parseManifoldMarkets(raw)
			}
		}
		min := cfgFloat(cfg, "min_volume_"+v, 0)
		for _, m := range rows {
			if m.Close.Before(minClose) || m.Close.After(maxClose) || m.Volume < min || m.Yes == nil {
				continue
			}
			listed = append(listed, m)
		}
	}
	exclude := cfgStrings(cfg, "exclude_keywords")
	if _, set := cfg["exclude_keywords"]; !set {
		exclude = defaultExcludedQuestionKeywords
	}
	minPrice, maxPrice := cfgFloat(cfg, "min_price", 0.03), cfgFloat(cfg, "max_price", 0.97)
	perTopic := int(cfgFloat(cfg, "max_per_topic", 2))
	perEvent := int(cfgFloat(cfg, "max_per_event", 2))
	perTemplate := int(cfgFloat(cfg, "max_per_template", 2))
	topicCount := map[string]int{}
	eventCount := map[string]int{}
	templateCount := map[string]int{}
	for _, q := range open {
		if len(q.Topics) > 0 {
			topicCount[strings.ToLower(q.Topics[0])]++
		}
		if q.EventKey != "" {
			eventCount[q.Venue+"\x00"+q.EventKey]++
		}
		if q.Template != "" {
			templateCount[q.Template]++
		}
	}
	// eligibleNew applies the new-question filters; existing questions keep
	// being priced and resolved whatever their current price.
	eligibleNew := func(m venueMarket) bool {
		if excludedQuestion(m.Question, exclude) || *m.Yes < minPrice || *m.Yes > maxPrice {
			return false
		}
		ek := m.Venue + "\x00" + m.Event
		if m.Event != "" && perEvent > 0 && eventCount[ek] >= perEvent {
			return false
		}
		if m.Template != "" && perTemplate > 0 && templateCount[m.Template] >= perTemplate {
			return false
		}
		if t := questionTopics(m.Question); len(t) > 0 && perTopic > 0 {
			key := strings.ToLower(t[0])
			if topicCount[key] >= perTopic {
				return false
			}
			topicCount[key]++
		}
		if m.Event != "" {
			eventCount[ek]++
		}
		if m.Template != "" {
			templateCount[m.Template]++
		}
		return true
	}
	st.Fetched = len(listed)
	sort.SliceStable(listed, func(i, j int) bool { return listed[i].Volume > listed[j].Volume })

	byKey := map[string]Question{}
	for _, q := range open {
		byKey[q.Venue+"\x00"+q.ExternalID] = q
	}
	created := 0
	priced := map[int64]bool{}
	eventTitles := map[string]string{}
	for _, m := range listed {
		q, known := byKey[m.Venue+"\x00"+m.ID]
		if !known {
			if created >= maxNew || len(byKey) >= maxOpen {
				continue
			}
			if m.Venue == "kalshi" {
				m = withKalshiEvent(env.sc, m, eventTitles)
			}
			if !eligibleNew(m) {
				continue
			}
			nq, isNew, err := insertQuestion(env.db, env.pid, Question{Origin: "market", Venue: m.Venue, ExternalID: m.ID, URL: m.URL, Text: m.Question,
				ResolutionRule: m.Rule, ResolutionSource: m.Venue + " market resolution (" + m.URL + ")", Category: m.Category, Deadline: m.Close, EventKey: m.Event, Template: m.Template})
			if err != nil {
				return st, err
			}
			if !isNew {
				continue
			}
			created++
			st.Inserted++
			q = *nq
			byKey[m.Venue+"\x00"+m.ID] = q
		}
		if err := addQuestionPrice(env.db, q.ID, *m.Yes, env.now, m.Venue); err != nil {
			return st, err
		}
		priced[q.ID] = true
	}

	// Refresh prices and check resolution for open market questions the
	// listing did not cover (closing soon, past deadline, or low volume now).
	refreshed := 0
	for _, q := range open {
		if q.Origin != "market" || priced[q.ID] || refreshed >= 60 {
			continue
		}
		refreshed++
		m, ok := fetchVenueMarket(env.sc, q.Venue, q.ExternalID)
		if !ok {
			continue
		}
		if m.Resolved {
			status, note := "resolved", fmt.Sprintf("%s reported final outcome", q.Venue)
			if m.Outcome == nil {
				status, note = "void", q.Venue+" cancelled or resolved non-binary"
			}
			if err := resolveQuestionRow(env.db, env.pid, q.ID, status, m.Outcome, note, env.now); err == nil {
				st.Inserted++
			}
			continue
		}
		if m.Yes != nil {
			_ = addQuestionPrice(env.db, q.ID, *m.Yes, env.now, q.Venue)
		}
	}
	return st, nil
}

// Question topics are search terms only. They are deliberately not turned
// into entities: capitalized runs are often people's names, and the world
// model admits only typed, non-person entities (from series, operators, or
// the extractor, which is instructed to model people only as public roles).

func containsID(ids []int64, id int64) bool {
	for _, x := range ids {
		if x == id {
			return true
		}
	}
	return false
}

func parseTimeLoose(v string) time.Time {
	if t := parseTS(v); t != nil {
		return *t
	}
	if t, err := time.Parse("2006-01-02", v); err == nil {
		return t
	}
	return time.Time{}
}

// withEventTitle adds a market's event title when the question alone is
// ambiguous ("Exact Score: 2-1?", "Lewis Hamilton wins"). Questions that
// already restate most of the event are left alone; a shared "X:" lead
// ("… - Exact Score" + "Exact Score: 2-1?") is merged rather than doubled.
func withEventTitle(question, event string) string {
	event = strings.TrimSpace(event)
	if event == "" {
		return question
	}
	if i := strings.Index(question, ":"); i > 0 && i < 40 {
		lead := strings.TrimSpace(question[:i])
		if strings.Contains(strings.ToLower(event), strings.ToLower(lead)) {
			return event + ": " + strings.TrimSpace(question[i+1:])
		}
	}
	lq := strings.ToLower(question)
	words, shared := 0, 0
	for _, w := range strings.Fields(strings.ToLower(event)) {
		w = strings.Trim(w, ".,:;?!()-")
		if len(w) < 3 {
			continue
		}
		words++
		if strings.Contains(lq, w) {
			shared++
		}
	}
	// Very short questions ("Chicago wins") always get their event.
	if words == 0 || (shared*2 >= words && len(strings.Fields(question)) > 4) {
		return question
	}
	return event + ": " + question
}

var templateDigits = regexp.MustCompile(`[0-9]+`)

// questionTemplate collapses numbers so ladder markets ("Bitcoin above
// $68,000 / $70,000") and repeated props ("Exact Score: Any Other Score?"
// across many matches) share one key.
func questionTemplate(raw string) string {
	return templateDigits.ReplaceAllString(strings.ToLower(strings.TrimSpace(raw)), "#")
}

// withKalshiEvent fetches (once per run) the event title for a Kalshi market.
func withKalshiEvent(sc sourceClient, m venueMarket, cache map[string]string) venueMarket {
	if m.Event == "" {
		return m
	}
	title, ok := cache[m.Event]
	if !ok {
		if raw, ok := sc.call("kalshi", "get_event", map[string]any{"event_ticker": m.Event}); ok {
			var r struct {
				Event struct {
					Title string `json:"title"`
				} `json:"event"`
			}
			if json.Unmarshal(raw, &r) == nil {
				title = r.Event.Title
			}
		}
		cache[m.Event] = title
	}
	m.EventTitle = title
	m.Question = withEventTitle(m.Question, title)
	return m
}

func parsePolymarketWindow(raw json.RawMessage) []venueMarket {
	var arr []json.RawMessage
	if json.Unmarshal(raw, &arr) != nil {
		return nil
	}
	out := []venueMarket{}
	for _, item := range arr {
		if m, ok := parsePolymarketMarket(item); ok {
			out = append(out, m)
		}
	}
	return out
}

func parsePolymarketMarket(raw json.RawMessage) (venueMarket, bool) {
	var m struct {
		Question      string  `json:"question"`
		Slug          string  `json:"slug"`
		Description   string  `json:"description"`
		Outcomes      string  `json:"outcomes"`
		OutcomePrices string  `json:"outcomePrices"`
		VolumeNum     float64 `json:"volumeNum"`
		EndDate       string  `json:"endDate"`
		Closed        bool    `json:"closed"`
		UMAStatus     string  `json:"umaResolutionStatus"`
		Category      string  `json:"category"`
		Events        []struct {
			Slug  string `json:"slug"`
			Title string `json:"title"`
		} `json:"events"`
	}
	if json.Unmarshal(raw, &m) != nil || m.Slug == "" || m.Question == "" {
		return venueMarket{}, false
	}
	var outcomes, prices []string
	_ = json.Unmarshal([]byte(m.Outcomes), &outcomes)
	_ = json.Unmarshal([]byte(m.OutcomePrices), &prices)
	if len(outcomes) != 2 || !strings.EqualFold(outcomes[0], "yes") || len(prices) != 2 {
		return venueMarket{}, false // binary Yes/No markets only
	}
	vm := venueMarket{Venue: "polymarket", ID: m.Slug, Question: m.Question, Rule: truncate(m.Description, 4000),
		URL: "https://polymarket.com/market/" + m.Slug, Category: strings.ToLower(m.Category), Close: parseTimeLoose(m.EndDate), Volume: m.VolumeNum}
	vm.Template = questionTemplate(m.Question)
	if len(m.Events) > 0 {
		vm.Event, vm.EventTitle = m.Events[0].Slug, m.Events[0].Title
		vm.Question = withEventTitle(m.Question, m.Events[0].Title)
	}
	yes := parseFloatStr(prices[0])
	if m.Closed {
		if strings.EqualFold(m.UMAStatus, "resolved") || yes == 1 || yes == 0 {
			vm.Resolved = true
			switch {
			case yes >= 0.99:
				o := 1
				vm.Outcome = &o
			case yes <= 0.01:
				o := 0
				vm.Outcome = &o
			}
		}
		return vm, true
	}
	vm.Yes = &yes
	return vm, true
}

func parseKalshiMarkets(raw json.RawMessage) []venueMarket {
	var resp struct {
		Markets []json.RawMessage `json:"markets"`
	}
	if json.Unmarshal(raw, &resp) != nil {
		return nil
	}
	out := []venueMarket{}
	for _, item := range resp.Markets {
		if m, ok := parseKalshiMarket(item); ok {
			out = append(out, m)
		}
	}
	return out
}

func kalshiPrice(cents float64, dollars string) float64 {
	if cents > 0 {
		return cents / 100
	}
	return parseFloatStr(dollars)
}

func parseKalshiMarket(raw json.RawMessage) (venueMarket, bool) {
	var m struct {
		Ticker        string  `json:"ticker"`
		EventTicker   string  `json:"event_ticker"`
		Title         string  `json:"title"`
		YesSubTitle   string  `json:"yes_sub_title"`
		Rules         string  `json:"rules_primary"`
		MarketType    string  `json:"market_type"`
		YesBid        float64 `json:"yes_bid"`
		YesAsk        float64 `json:"yes_ask"`
		YesBidDollars string  `json:"yes_bid_dollars"`
		YesAskDollars string  `json:"yes_ask_dollars"`
		Volume        float64 `json:"volume"`
		VolumeFP      string  `json:"volume_fp"`
		Status        string  `json:"status"`
		Result        string  `json:"result"`
		CloseTime     string  `json:"close_time"`
		Category      string  `json:"category"`
	}
	if json.Unmarshal(raw, &m) != nil || m.Ticker == "" || m.Title == "" {
		return venueMarket{}, false
	}
	if m.MarketType != "" && m.MarketType != "binary" {
		return venueMarket{}, false
	}
	text := m.Title
	if m.YesSubTitle != "" && !strings.Contains(strings.ToLower(text), strings.ToLower(m.YesSubTitle)) {
		text += " — " + m.YesSubTitle
	}
	vm := venueMarket{Venue: "kalshi", ID: m.Ticker, Question: text, Rule: truncate(m.Rules, 4000), Category: strings.ToLower(m.Category),
		URL: "https://kalshi.com/markets/" + strings.ToLower(m.Ticker), Close: parseTimeLoose(m.CloseTime), Volume: m.Volume, Event: m.EventTicker, Template: questionTemplate(text)}
	if vm.Volume == 0 {
		vm.Volume = parseFloatStr(m.VolumeFP)
	}
	switch m.Status {
	case "settled", "finalized", "determined":
		vm.Resolved = true
		switch strings.ToLower(m.Result) {
		case "yes":
			o := 1
			vm.Outcome = &o
		case "no":
			o := 0
			vm.Outcome = &o
		}
		return vm, true
	}
	bid, ask := kalshiPrice(m.YesBid, m.YesBidDollars), kalshiPrice(m.YesAsk, m.YesAskDollars)
	if bid > 0 && ask > 0 && ask >= bid {
		mid := math.Round((bid+ask)/2*1000) / 1000
		vm.Yes = &mid
	}
	return vm, true
}

func parseManifoldMarkets(raw json.RawMessage) []venueMarket {
	var arr []json.RawMessage
	if json.Unmarshal(raw, &arr) != nil {
		return nil
	}
	out := []venueMarket{}
	for _, item := range arr {
		if m, ok := parseManifoldMarket(item); ok {
			out = append(out, m)
		}
	}
	return out
}

func parseManifoldMarket(raw json.RawMessage) (venueMarket, bool) {
	var m struct {
		ID              string  `json:"id"`
		Question        string  `json:"question"`
		URL             string  `json:"url"`
		Probability     float64 `json:"probability"`
		Volume          float64 `json:"volume"`
		OutcomeType     string  `json:"outcomeType"`
		IsResolved      bool    `json:"isResolved"`
		Resolution      string  `json:"resolution"`
		CloseTime       int64   `json:"closeTime"`
		TextDescription string  `json:"textDescription"`
	}
	if json.Unmarshal(raw, &m) != nil || m.ID == "" || m.Question == "" || m.OutcomeType != "BINARY" {
		return venueMarket{}, false
	}
	vm := venueMarket{Venue: "manifold", ID: m.ID, Question: m.Question, URL: m.URL, Rule: truncate(m.TextDescription, 4000),
		Close: time.UnixMilli(m.CloseTime).UTC(), Volume: m.Volume, Template: questionTemplate(m.Question)}
	if m.IsResolved {
		vm.Resolved = true
		switch m.Resolution {
		case "YES":
			o := 1
			vm.Outcome = &o
		case "NO":
			o := 0
			vm.Outcome = &o
		}
		return vm, true
	}
	p := m.Probability
	vm.Yes = &p
	return vm, true
}

func fetchVenueMarket(sc sourceClient, venue, id string) (venueMarket, bool) {
	switch venue {
	case "polymarket":
		raw, ok := sc.call("polymarket", "market_price", map[string]any{"slug": id})
		if !ok {
			return venueMarket{}, false
		}
		var arr []json.RawMessage
		if json.Unmarshal(raw, &arr) != nil || len(arr) == 0 {
			return venueMarket{}, false
		}
		return parsePolymarketMarket(arr[0])
	case "kalshi":
		raw, ok := sc.call("kalshi", "get_market", map[string]any{"ticker": id})
		if !ok {
			return venueMarket{}, false
		}
		var wrap struct {
			Market json.RawMessage `json:"market"`
		}
		if json.Unmarshal(raw, &wrap) != nil || len(wrap.Market) == 0 {
			return venueMarket{}, false
		}
		return parseKalshiMarket(wrap.Market)
	case "manifold":
		raw, ok := sc.call("manifold-markets", "get_market", map[string]any{"id": id})
		if !ok {
			return venueMarket{}, false
		}
		return parseManifoldMarket(raw)
	}
	return venueMarket{}, false
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}

// ─── News: evidence for open questions and watch topics ────────────

func runNews(env *ingestEnv, src IntelSource) (ingestStats, error) {
	st := ingestStats{}
	perRun := int(cfgFloat(src.Config, "questions_per_run", 24))
	timespan := fmt.Sprint(orStr(src.Config["timespan"], "3d"))
	open, err := listQuestions(env.db, env.pid, "open", 5000)
	if err != nil {
		return st, err
	}
	// Rotate through open questions using the cursor (last question id).
	cursor, _ := strconv.ParseInt(src.Cursor, 10, 64)
	sort.Slice(open, func(i, j int) bool { return open[i].ID < open[j].ID })
	batch := []Question{}
	for _, pass := range []bool{true, false} {
		for _, q := range open {
			if (q.ID > cursor) == pass && len(batch) < perRun && q.Deadline.After(env.now) && len(q.Topics) > 0 {
				batch = append(batch, q)
			}
		}
	}
	var failures []string
	record := func(it NewsItem, query string, qs []Question) {
		matched := []Question{}
		for _, q := range qs {
			if topicRelevance(it.Title, q.Topics) > 0.2 {
				matched = append(matched, q)
			}
		}
		if len(matched) == 0 && len(qs) > 1 {
			return // batched query hit, but the headline names none of the questions
		}
		st.Fetched++
		refs := []string{}
		for _, q := range matched {
			refs = append(refs, q.Topics...)
		}
		ev, inserted, err := recordNewsItem(env, it, query, refs)
		if err != nil {
			return
		}
		if inserted {
			st.Inserted++
		} else {
			st.Duplicates++
		}
		for _, q := range matched {
			_, _ = linkQuestionEvidence(env.db, q.ID, ev, topicRelevance(it.Title, q.Topics))
		}
	}

	// GDELT: one OR-query per chunk of questions instead of one call each;
	// the public API allows a single request every five seconds.
	for i := 0; i < len(batch); i += 8 {
		chunk := batch[i:min(i+8, len(batch))]
		terms := []string{}
		for _, q := range chunk {
			if t := newsTerm(q.Topics); t != "" && !containsFold(terms, t) {
				terms = append(terms, t)
			}
		}
		if len(terms) == 0 {
			continue
		}
		query := "(" + strings.Join(terms, " OR ") + ") sourcelang:english"
		if len(terms) == 1 {
			query = terms[0] + " sourcelang:english"
		}
		raw, ok := env.sc.call("gdelt", "doc_search", map[string]any{"query": query, "maxrecords": "250", "timespan": timespan})
		if !ok {
			reason := lastSourceError("gdelt")
			failures = append(failures, "gdelt: "+orStr(reason, "unavailable"))
			break // don't hammer a limiting API; the next run retries
		}
		for _, it := range parseNews(raw, "gdelt") {
			record(it, query, chunk)
		}
	}

	// Bound news APIs (NewsAPI, GNews, Tavily…): per question, only when connected.
	bound := env.sc.boundSlugs([]string{"tavily", "newsapi", "gnews"})
	for _, spec := range specsFor("*", qNews) {
		if !bound[spec.slug] {
			continue
		}
		for _, q := range batch {
			query := newsQuery(q.Topics)
			raw, ok := env.sc.call(spec.slug, spec.tool, spec.argFn(map[string]any{"topic": query}))
			if !ok {
				failures = append(failures, spec.slug+" call failed")
				break
			}
			for _, it := range parseNews(raw, spec.slug) {
				record(it, query, []Question{q})
			}
		}
	}

	for _, topic := range cfgStrings(src.Config, "watch_topics") {
		raw, ok := env.sc.call("gdelt", "doc_search", map[string]any{"query": topic + " sourcelang:english", "maxrecords": "100", "timespan": timespan})
		if !ok {
			failures = append(failures, "gdelt watch topic "+topic)
			continue
		}
		for _, it := range parseNews(raw, "gdelt") {
			record(it, topic, nil)
		}
	}
	if len(batch) > 0 {
		st.Cursor = strconv.FormatInt(batch[len(batch)-1].ID, 10)
	}
	if len(failures) > 0 && st.Fetched == 0 {
		return st, errors.New(strings.Join(failures, "; "))
	}
	return st, nil
}

// newsTerm is one search term for a question: its first topic as a phrase,
// trimmed to three words ("Democratic Progressive Party").
func newsTerm(topics []string) string {
	if len(topics) == 0 {
		return ""
	}
	t := strings.TrimSpace(topics[0])
	words := strings.Fields(t)
	switch {
	case len(words) == 0:
		return ""
	case len(words) == 1:
		if len(t) < 3 {
			return ""
		}
		return t
	case len(words) <= 3:
		return `"` + t + `"`
	}
	return `"` + strings.Join(words[:3], " ") + `"`
}

// newsQuery quotes multi-word topics for per-question APIs.
func newsQuery(topics []string) string {
	parts := []string{}
	for i, t := range topics {
		if i >= 2 {
			break
		}
		if strings.Contains(t, " ") {
			t = `"` + t + `"`
		}
		parts = append(parts, t)
	}
	return strings.Join(parts, " ")
}

func topicRelevance(title string, topics []string) float64 {
	if len(topics) == 0 {
		return 0
	}
	t := strings.ToLower(title)
	hit := 0
	for _, x := range topics {
		if strings.Contains(t, strings.ToLower(x)) {
			hit++
		}
	}
	if hit == 0 {
		return 0
	}
	return math.Round(float64(hit)/float64(len(topics))*100) / 100
}

func recordNewsItem(env *ingestEnv, it NewsItem, query string, refs []string) (int64, bool, error) {
	if strings.TrimSpace(it.Title) == "" {
		return 0, false, errors.New("empty headline")
	}
	payload, _ := json.Marshal(map[string]any{"connector": "news", "query": query, "outlet": it.Source, "url": it.URL})
	e := Evidence{Kind: "news", Title: it.Title, Source: fallback(it.Source, "news"), SourceRef: it.URL, Payload: payload, EntityRefs: refs}
	if it.Date != "" {
		if t := parseTS(it.Date); t != nil {
			e.PublishedTime = t
		}
	}
	// Dedup news on outlet + URL (or title) regardless of which query found it.
	e.ContentHash = evidenceHash(env.pid, Evidence{Kind: "news", Title: it.Title, Source: e.Source, SourceRef: it.URL})
	row, inserted, err := recordEvidenceDetailed(env.db, e, env.pid)
	if err != nil {
		return 0, false, err
	}
	id, _ := row["id"].(int64)
	return id, inserted, nil
}

// ─── Statistical series (BLS public, FRED via binding) ─────────────

type seriesSpec struct{ ID, Name, Unit string }

func cfgSeries(cfg map[string]any) []seriesSpec {
	out := []seriesSpec{}
	raw, _ := json.Marshal(cfg["series"])
	var rows []map[string]string
	_ = json.Unmarshal(raw, &rows)
	for _, r := range rows {
		if r["id"] != "" {
			out = append(out, seriesSpec{ID: r["id"], Name: fallback(r["name"], r["id"]), Unit: r["unit"]})
		}
	}
	return out
}

type seriesPoint struct {
	At    time.Time
	Value float64
}

func runBLS(env *ingestEnv, src IntelSource) (ingestStats, error) {
	return runSeries(env, src, "bls", func(s seriesSpec) ([]seriesPoint, bool) {
		raw, ok := env.sc.call("bls", "get_series_single", map[string]any{"seriesid": s.ID})
		if !ok {
			return nil, false
		}
		return parseBLSSeries(raw), true
	})
}

func runFRED(env *ingestEnv, src IntelSource) (ingestStats, error) {
	if !env.sc.boundSlugs([]string{"fred"})["fred"] {
		return ingestStats{}, errors.New("fred is not bound: connect a FRED integration or disable this source")
	}
	return runSeries(env, src, "fred", func(s seriesSpec) ([]seriesPoint, bool) {
		raw, ok := env.sc.call("fred", "series_observations", map[string]any{"series_id": s.ID, "sort_order": "desc", "limit": 24})
		if !ok {
			return nil, false
		}
		return parseFREDSeries(raw), true
	})
}

func runSeries(env *ingestEnv, src IntelSource, slug string, fetch func(seriesSpec) ([]seriesPoint, bool)) (ingestStats, error) {
	st := ingestStats{}
	specs := cfgSeries(src.Config)
	failed := 0
	for _, s := range specs {
		points, ok := fetch(s)
		if !ok {
			failed++
			continue
		}
		ent, _, err := upsertWorldEntity(env.db, env.pid, WorldEntity{Name: s.Name, Kind: "indicator", ExternalIDs: map[string]string{slug: s.ID}})
		if err != nil {
			return st, err
		}
		for _, p := range points {
			st.Fetched++
			payload, _ := json.Marshal(map[string]any{"connector": slug, "series": s.ID, "value": p.Value, "unit": s.Unit, "period": ts(p.At)})
			at := p.At
			row, inserted, err := recordEvidenceDetailed(env.db, Evidence{Kind: "measurement", Title: fmt.Sprintf("%s: %g %s", s.Name, p.Value, s.Unit),
				Source: slug, SourceRef: s.ID + "@" + p.At.Format("2006-01"), Payload: payload, EntityRefs: []string{s.Name}, EventTime: &at, ValidFrom: &at}, env.pid)
			if err != nil {
				return st, err
			}
			evID, _ := row["id"].(int64)
			if _, err := putStateValue(env.db, env.pid, StateValue{EntityID: ent.ID, Variable: "value", Value: p.Value, Unit: s.Unit, ValidAt: p.At, EvidenceID: &evID, Source: slug}); err != nil {
				return st, err
			}
			if inserted {
				st.Inserted++
			} else {
				st.Duplicates++
			}
		}
	}
	if failed > 0 && failed == len(specs) {
		return st, fmt.Errorf("%s: all %d series requests failed", slug, failed)
	}
	return st, nil
}

func parseBLSSeries(raw json.RawMessage) []seriesPoint {
	var resp struct {
		Results struct {
			Series []struct {
				Data []struct {
					Year   string `json:"year"`
					Period string `json:"period"`
					Value  string `json:"value"`
				} `json:"data"`
			} `json:"series"`
		} `json:"Results"`
	}
	if json.Unmarshal(raw, &resp) != nil || len(resp.Results.Series) == 0 {
		return nil
	}
	out := []seriesPoint{}
	for _, d := range resp.Results.Series[0].Data {
		if !strings.HasPrefix(d.Period, "M") || d.Period == "M13" {
			continue
		}
		month, err1 := strconv.Atoi(strings.TrimPrefix(d.Period, "M"))
		year, err2 := strconv.Atoi(d.Year)
		v, err3 := strconv.ParseFloat(strings.ReplaceAll(d.Value, ",", ""), 64)
		if err1 != nil || err2 != nil || err3 != nil {
			continue
		}
		out = append(out, seriesPoint{At: time.Date(year, time.Month(month), 1, 0, 0, 0, 0, time.UTC), Value: v})
	}
	return out
}

func parseFREDSeries(raw json.RawMessage) []seriesPoint {
	var resp struct {
		Observations []struct {
			Date  string `json:"date"`
			Value string `json:"value"`
		} `json:"observations"`
	}
	if json.Unmarshal(raw, &resp) != nil {
		return nil
	}
	out := []seriesPoint{}
	for _, o := range resp.Observations {
		t, err := time.Parse("2006-01-02", o.Date)
		v, err2 := strconv.ParseFloat(o.Value, 64)
		if err != nil || err2 != nil {
			continue // FRED uses "." for missing values
		}
		out = append(out, seriesPoint{At: t, Value: v})
	}
	return out
}
