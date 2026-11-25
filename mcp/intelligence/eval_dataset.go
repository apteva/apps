package main

// Evaluation datasets: resolved binary questions frozen with their full
// trade-by-trade price history, so a backtest can be re-run later on
// exactly the same inputs.
//
// Leakage rules applied here:
//   - only YES/NO resolutions; cancelled, partial and N/A outcomes are skipped
//   - prices are individual trades with their own trade times, never a
//     venue's "current" or final price
//   - a question must have been open for min_open_days before it closed, so
//     a forecast horizon fits inside its life
// Within the window, questions are a seeded random sample of those that
// pass the filters, so they spread over the whole period rather than
// bunching at its end.
// Selection still uses information from after the forecast time (a market
// had to resolve, and trader counts are final counts). Reports state this.

import (
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math/rand"
	"sort"
	"strconv"
	"strings"
	"time"
)

type evalItem struct {
	Idx        int          `json:"idx"`
	Venue      string       `json:"venue"`
	ExternalID string       `json:"external_id"`
	Cluster    string       `json:"cluster"`
	Text       string       `json:"text"`
	URL        string       `json:"url,omitempty"`
	OpenedAt   time.Time    `json:"opened_at"`
	ClosesAt   time.Time    `json:"closes_at"`
	ResolvedAt time.Time    `json:"resolved_at"`
	Outcome    int          `json:"outcome"`
	Prices     [][2]float64 `json:"prices"` // [unix_ms, probability] ascending
}

// end is when the outcome stopped being uncertain: the earlier of close
// and resolution (questions can resolve early when the event happens).
func (it evalItem) end() time.Time {
	if it.ResolvedAt.Before(it.ClosesAt) {
		return it.ResolvedAt
	}
	return it.ClosesAt
}

// priceAt is the last traded probability at or before t, with the trade's
// time. ok is false when nothing traded by then.
func (it evalItem) priceAt(t time.Time) (p float64, at time.Time, ok bool) {
	ms := float64(t.UnixMilli())
	i := sort.Search(len(it.Prices), func(i int) bool { return it.Prices[i][0] > ms })
	if i == 0 {
		return 0, time.Time{}, false
	}
	last := it.Prices[i-1]
	return last[1], time.UnixMilli(int64(last[0])).UTC(), true
}

type datasetParams struct {
	Name        string   `json:"name"`
	Venues      []string `json:"venues"`
	SinceDays   int      `json:"since_days"`
	MaxPerVenue int      `json:"max_per_venue"`
	MinTraders  int      `json:"min_traders"`
	MinTrades   int      `json:"min_trades"`
	MinOpenDays float64  `json:"min_open_days"`
	MaxPerEvent int      `json:"max_per_event"`
	Seed        int64    `json:"seed"`
}

func parseDatasetParams(args map[string]any) datasetParams {
	p := datasetParams{Name: strArg(args, "name"), Venues: []string{"manifold", "kalshi"}, SinceDays: 30, MaxPerVenue: 150,
		MinTraders: 15, MinTrades: 10, MinOpenDays: 8, MaxPerEvent: 2, Seed: 1}
	if v := strArg(args, "venues"); v != "" {
		p.Venues = nil
		for _, s := range strings.Split(v, ",") {
			if s = strings.TrimSpace(strings.ToLower(s)); s == "manifold" || s == "kalshi" {
				p.Venues = append(p.Venues, s)
			}
		}
	}
	if n := intArgAny(args, "since_days"); n > 0 {
		p.SinceDays = int(min(n, 365))
	}
	if n := intArgAny(args, "max_per_venue"); n > 0 {
		p.MaxPerVenue = int(min(n, 500))
	}
	if n := intArgAny(args, "min_traders"); n > 0 {
		p.MinTraders = int(n)
	}
	if n := intArgAny(args, "min_trades"); n > 0 {
		p.MinTrades = int(n)
	}
	if v, ok := floatArgAny(args, "min_open_days"); ok && v > 0 {
		p.MinOpenDays = v
	}
	if n := intArgAny(args, "max_per_event"); n > 0 {
		p.MaxPerEvent = int(n)
	}
	if n := intArgAny(args, "seed"); n != 0 {
		p.Seed = n
	}
	if p.Name == "" {
		p.Name = fmt.Sprintf("%s resolved in last %dd", strings.Join(p.Venues, "+"), p.SinceDays)
	}
	return p
}

type skipCounter map[string]int

func (s skipCounter) add(reason string) { s[reason]++ }

// buildEvalDataset fetches, filters and freezes a dataset. now is the fetch
// time; it is recorded so readers know what the venues knew at that point.
func buildEvalDataset(sc sourceClient, p datasetParams, now time.Time) ([]evalItem, skipCounter, error) {
	skipped := skipCounter{}
	since := now.AddDate(0, 0, -p.SinceDays)
	var items []evalItem
	var failures []string
	for _, v := range p.Venues {
		var got []evalItem
		var err error
		switch v {
		case "manifold":
			got, err = fetchManifoldResolved(sc, p, since, skipped)
		case "kalshi":
			got, err = fetchKalshiSettled(sc, p, since, now, skipped)
		}
		if err != nil {
			failures = append(failures, v+": "+err.Error())
			continue
		}
		items = append(items, got...)
	}
	if len(items) == 0 {
		if len(failures) > 0 {
			return nil, skipped, errors.New(strings.Join(failures, "; "))
		}
		return nil, skipped, errors.New("no question passed the filters")
	}
	for _, f := range failures {
		skipped.add("venue failed: " + f)
	}
	sort.SliceStable(items, func(i, j int) bool {
		if !items[i].ResolvedAt.Equal(items[j].ResolvedAt) {
			return items[i].ResolvedAt.Before(items[j].ResolvedAt)
		}
		return items[i].Venue+items[i].ExternalID < items[j].Venue+items[j].ExternalID
	})
	for i := range items {
		items[i].Idx = i
		// Stored at millisecond precision; truncate now so the content hash
		// is the same before and after a round trip through the database.
		items[i].OpenedAt = items[i].OpenedAt.Truncate(time.Millisecond)
		items[i].ClosesAt = items[i].ClosesAt.Truncate(time.Millisecond)
		items[i].ResolvedAt = items[i].ResolvedAt.Truncate(time.Millisecond)
	}
	return items, skipped, nil
}

func fetchManifoldResolved(sc sourceClient, p datasetParams, since time.Time, skipped skipCounter) ([]evalItem, error) {
	type mmarket struct {
		ID             string  `json:"id"`
		Question       string  `json:"question"`
		URL            string  `json:"url"`
		Resolution     string  `json:"resolution"`
		CreatedTime    float64 `json:"createdTime"`
		CloseTime      float64 `json:"closeTime"`
		ResolutionTime float64 `json:"resolutionTime"`
		Bettors        int     `json:"uniqueBettorCount"`
	}
	// Page back through resolutions (newest first) until the window is
	// covered.
	var markets []mmarket
	for page := 0; page < 20; page++ {
		raw, ok := sc.call("manifold-markets", "search_markets", map[string]any{
			"filter": "resolved", "sort": "resolve-date", "contractType": "BINARY", "limit": "1000", "offset": strconv.Itoa(page * 1000)})
		if !ok {
			if page == 0 {
				return nil, fmt.Errorf("market search failed (%s)", orStr(lastSourceError("manifold-markets"), "unavailable"))
			}
			skipped.add("manifold: search paging stopped early")
			break
		}
		var batch []mmarket
		if err := json.Unmarshal(raw, &batch); err != nil {
			return nil, fmt.Errorf("market search: %w", err)
		}
		markets = append(markets, batch...)
		if len(batch) < 1000 || msTime(batch[len(batch)-1].ResolutionTime).Before(since) {
			break
		}
	}
	type cand struct {
		m  mmarket
		it evalItem
	}
	cands := []cand{}
	seen := map[string]bool{}
	for _, m := range markets {
		if seen[m.ID] {
			continue
		}
		seen[m.ID] = true
		if m.Resolution != "YES" && m.Resolution != "NO" {
			skipped.add("manifold: not a YES/NO resolution")
			continue
		}
		it := evalItem{Venue: "manifold", ExternalID: m.ID, Cluster: "manifold:" + m.ID, Text: m.Question, URL: m.URL,
			OpenedAt: msTime(m.CreatedTime), ClosesAt: msTime(m.CloseTime), ResolvedAt: msTime(m.ResolutionTime)}
		if m.CloseTime == 0 {
			it.ClosesAt = it.ResolvedAt
		}
		if m.Resolution == "YES" {
			it.Outcome = 1
		}
		switch {
		case it.ResolvedAt.Before(since):
			skipped.add("resolved before the window")
			continue
		case m.Bettors < p.MinTraders:
			skipped.add("too few traders")
			continue
		case it.end().Sub(it.OpenedAt).Hours() < p.MinOpenDays*24:
			skipped.add("open too briefly")
			continue
		}
		cands = append(cands, cand{m, it})
	}
	rand.New(rand.NewSource(p.Seed)).Shuffle(len(cands), func(i, j int) { cands[i], cands[j] = cands[j], cands[i] })
	out := []evalItem{}
	for _, c := range cands {
		if len(out) >= p.MaxPerVenue {
			break
		}
		m, it := c.m, c.it
		prices, err := manifoldPrices(sc, m.ID, it.end())
		if err != nil {
			skipped.add("manifold: price history failed")
			continue
		}
		if len(prices) < p.MinTrades {
			skipped.add("too few trades")
			continue
		}
		it.Prices = prices
		out = append(out, it)
	}
	return out, nil
}

// manifoldPrices pages back through a market's bets (newest first) and
// keeps filled trades up to the question's end, as [time, prob after].
func manifoldPrices(sc sourceClient, id string, end time.Time) ([][2]float64, error) {
	var prices [][2]float64
	before := ""
	for page := 0; page < 8; page++ {
		args := map[string]any{"contractId": id, "limit": "1000"}
		if before != "" {
			args["before"] = before
		}
		raw, ok := sc.call("manifold-markets", "list_bets", args)
		if !ok {
			return nil, errors.New("bets unavailable")
		}
		var bets []struct {
			ID          string   `json:"id"`
			CreatedTime float64  `json:"createdTime"`
			ProbAfter   *float64 `json:"probAfter"`
			IsFilled    *bool    `json:"isFilled"`
			IsCancelled bool     `json:"isCancelled"`
			Amount      float64  `json:"amount"`
		}
		if err := json.Unmarshal(raw, &bets); err != nil {
			return nil, err
		}
		for _, b := range bets {
			if b.ProbAfter == nil || b.IsCancelled || (b.IsFilled != nil && !*b.IsFilled) || b.Amount == 0 {
				continue
			}
			if msTime(b.CreatedTime).After(end) {
				continue
			}
			prices = append(prices, [2]float64{b.CreatedTime, *b.ProbAfter})
		}
		if len(bets) < 1000 {
			break
		}
		before = bets[len(bets)-1].ID
	}
	sort.Slice(prices, func(i, j int) bool { return prices[i][0] < prices[j][0] })
	return prices, nil
}

func fetchKalshiSettled(sc sourceClient, p datasetParams, since, now time.Time, skipped skipCounter) ([]evalItem, error) {
	type kmarket struct {
		Ticker      string `json:"ticker"`
		EventTicker string `json:"event_ticker"`
		Title       string `json:"title"`
		YesSubTitle string `json:"yes_sub_title"`
		Result      string `json:"result"`
		MarketType  string `json:"market_type"`
		OpenTime    string `json:"open_time"`
		CloseTime   string `json:"close_time"`
		Settlement  string `json:"settlement_ts"`
	}
	// Kalshi settles thousands of short-lived markets a day, so one page
	// covers hours. Take one page from each of ten slices of the window.
	var cands []kmarket
	const slices = 10
	span := now.Sub(since) / slices
	failed := 0
	for k := 0; k < slices; k++ {
		lo, hi := since.Add(time.Duration(k)*span), since.Add(time.Duration(k+1)*span)
		raw, ok := sc.call("kalshi", "list_markets", map[string]any{"status": "settled", "mve_filter": "exclude", "limit": "1000",
			"min_close_ts": strconv.FormatInt(lo.Unix(), 10), "max_close_ts": strconv.FormatInt(hi.Unix(), 10)})
		if !ok {
			failed++
			continue
		}
		var resp struct {
			Markets []kmarket `json:"markets"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			return nil, err
		}
		cands = append(cands, resp.Markets...)
	}
	if failed == slices {
		return nil, fmt.Errorf("market list failed (%s)", orStr(lastSourceError("kalshi"), "unavailable"))
	}
	if failed > 0 {
		skipped["kalshi: window slices unavailable"] += failed
	}
	rand.New(rand.NewSource(p.Seed+1)).Shuffle(len(cands), func(i, j int) { cands[i], cands[j] = cands[j], cands[i] })
	perEvent, perSeries := map[string]int{}, map[string]int{}
	out := []evalItem{}
	for _, m := range cands {
		if len(out) >= p.MaxPerVenue {
			break
		}
		if m.MarketType != "" && m.MarketType != "binary" {
			skipped.add("kalshi: not binary")
			continue
		}
		if m.Result != "yes" && m.Result != "no" {
			skipped.add("kalshi: not a yes/no settlement")
			continue
		}
		opened, closed, settled := parseTS(m.OpenTime), parseTS(m.CloseTime), parseTS(m.Settlement)
		if opened == nil || closed == nil {
			skipped.add("kalshi: missing times")
			continue
		}
		if settled == nil {
			settled = closed
		}
		it := evalItem{Venue: "kalshi", ExternalID: m.Ticker, Cluster: "kalshi:" + kalshiSeries(m.EventTicker), Text: kalshiText(m.Title, m.YesSubTitle),
			URL: "https://kalshi.com/markets/" + strings.ToLower(m.EventTicker), OpenedAt: *opened, ClosesAt: *closed, ResolvedAt: *settled}
		if m.Result == "yes" {
			it.Outcome = 1
		}
		if it.end().Sub(it.OpenedAt).Hours() < p.MinOpenDays*24 {
			skipped.add("open too briefly")
			continue
		}
		if perEvent[m.EventTicker] >= p.MaxPerEvent {
			skipped.add("kalshi: event already represented")
			continue
		}
		if perSeries[kalshiSeries(m.EventTicker)] >= kalshiMaxPerSeries {
			skipped.add("kalshi: series already represented")
			continue
		}
		prices, err := kalshiPrices(sc, m.Ticker, it.OpenedAt, it.end())
		if err != nil {
			skipped.add("kalshi: trade history failed")
			continue
		}
		if len(prices) < p.MinTrades {
			skipped.add("too few trades")
			continue
		}
		it.Prices = prices
		perEvent[m.EventTicker]++
		perSeries[kalshiSeries(m.EventTicker)]++
		out = append(out, it)
	}
	return out, nil
}

// kalshiMaxPerSeries keeps a few recurring daily series from filling the
// sample.
const kalshiMaxPerSeries = 5

// kalshiSeries is the series part of an event ticker (KXHIGHNY-26NOV24 →
// KXHIGHNY). Daily and weekly markets in one series move together, so they
// share a bootstrap cluster.
func kalshiSeries(eventTicker string) string {
	if i := strings.Index(eventTicker, "-"); i > 0 {
		return eventTicker[:i]
	}
	return eventTicker
}

func kalshiText(title, sub string) string {
	if sub != "" && !strings.Contains(title, sub) {
		return title + " — " + sub
	}
	return title
}

func kalshiPrices(sc sourceClient, ticker string, from, to time.Time) ([][2]float64, error) {
	var prices [][2]float64
	cursor := ""
	for page := 0; page < 5; page++ {
		args := map[string]any{"ticker": ticker, "limit": "1000", "min_ts": strconv.FormatInt(from.Unix(), 10), "max_ts": strconv.FormatInt(to.Unix(), 10)}
		if cursor != "" {
			args["cursor"] = cursor
		}
		raw, ok := sc.call("kalshi", "get_market_trades", args)
		if !ok {
			return nil, errors.New("trades unavailable")
		}
		var resp struct {
			Trades []struct {
				CreatedTime string `json:"created_time"`
				YesPrice    string `json:"yes_price_dollars"`
			} `json:"trades"`
			Cursor string `json:"cursor"`
		}
		if err := json.Unmarshal(raw, &resp); err != nil {
			return nil, err
		}
		for _, t := range resp.Trades {
			at := parseTS(t.CreatedTime)
			p, err := strconv.ParseFloat(t.YesPrice, 64)
			if at == nil || err != nil || at.After(to) {
				continue
			}
			prices = append(prices, [2]float64{float64(at.UnixMilli()), p})
		}
		if resp.Cursor == "" || len(resp.Trades) == 0 {
			break
		}
		cursor = resp.Cursor
	}
	sort.Slice(prices, func(i, j int) bool { return prices[i][0] < prices[j][0] })
	return prices, nil
}

func msTime(ms float64) time.Time { return time.UnixMilli(int64(ms)).UTC() }

// datasetHash fingerprints the frozen content, so a report can prove which
// inputs it scored.
func datasetHash(items []evalItem) string {
	h := sha256.New()
	for _, it := range items {
		b, _ := json.Marshal(it)
		h.Write(b)
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil))
}

func createEvalDataset(db *sql.DB, pid string, p datasetParams, now time.Time) (int64, error) {
	var id int64
	err := db.QueryRow(`INSERT INTO intel_eval_datasets (project_id, name, status, params, created_at) VALUES (?,?,?,?,?) RETURNING id`,
		pid, p.Name, "building", mustJSON(p), ts(now)).Scan(&id)
	return id, err
}

func finishEvalDataset(db *sql.DB, id int64, items []evalItem, skipped skipCounter, fetchedAt time.Time, buildErr error) error {
	if buildErr != nil {
		_, err := db.Exec(`UPDATE intel_eval_datasets SET status = 'failed', error = ?, skipped = ?, fetched_at = ? WHERE id = ?`,
			buildErr.Error(), mustJSON(skipped), ts(fetchedAt), id)
		return err
	}
	tx, err := db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()
	for _, it := range items {
		if _, err := tx.Exec(`INSERT INTO intel_eval_items (dataset_id, idx, venue, external_id, cluster, text, url, opened_at, closes_at, resolved_at, outcome, prices)
			VALUES (?,?,?,?,?,?,?,?,?,?,?,?)`, id, it.Idx, it.Venue, it.ExternalID, it.Cluster, it.Text, it.URL,
			tsMilli(it.OpenedAt), tsMilli(it.ClosesAt), tsMilli(it.ResolvedAt), it.Outcome, mustJSON(it.Prices)); err != nil {
			return err
		}
	}
	if _, err := tx.Exec(`UPDATE intel_eval_datasets SET status = 'ready', content_hash = ?, n_items = ?, skipped = ?, fetched_at = ? WHERE id = ?`,
		datasetHash(items), len(items), mustJSON(skipped), ts(fetchedAt), id); err != nil {
		return err
	}
	return tx.Commit()
}

// tsMilli keeps millisecond precision: cutoffs and trades are compared at
// that resolution, so rounding to seconds could move a trade across one.
func tsMilli(t time.Time) string { return t.UTC().Format("2006-01-02T15:04:05.000Z07:00") }

type evalDataset struct {
	ID          int64          `json:"id"`
	Name        string         `json:"name"`
	Status      string         `json:"status"`
	Params      map[string]any `json:"params"`
	ContentHash string         `json:"content_hash"`
	NItems      int            `json:"n_items"`
	Skipped     map[string]int `json:"skipped"`
	Error       string         `json:"error,omitempty"`
	CreatedAt   string         `json:"created_at"`
	FetchedAt   string         `json:"fetched_at,omitempty"`
}

func listEvalDatasets(db *sql.DB, pid string, id int64) ([]evalDataset, error) {
	q := `SELECT id, name, status, params, content_hash, n_items, skipped, error, created_at, COALESCE(fetched_at,'') FROM intel_eval_datasets WHERE project_id = ?`
	args := []any{pid}
	if id > 0 {
		q += ` AND id = ?`
		args = append(args, id)
	}
	rows, err := db.Query(q+` ORDER BY id DESC LIMIT 50`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []evalDataset{}
	for rows.Next() {
		var d evalDataset
		var params, skipped string
		if err := rows.Scan(&d.ID, &d.Name, &d.Status, &params, &d.ContentHash, &d.NItems, &skipped, &d.Error, &d.CreatedAt, &d.FetchedAt); err != nil {
			return nil, err
		}
		_ = json.Unmarshal([]byte(params), &d.Params)
		_ = json.Unmarshal([]byte(skipped), &d.Skipped)
		out = append(out, d)
	}
	return out, rows.Err()
}

func loadEvalItems(db *sql.DB, datasetID int64) ([]evalItem, error) {
	rows, err := db.Query(`SELECT idx, venue, external_id, cluster, text, url, opened_at, closes_at, resolved_at, outcome, prices
		FROM intel_eval_items WHERE dataset_id = ? ORDER BY idx`, datasetID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []evalItem{}
	for rows.Next() {
		var it evalItem
		var opened, closes, resolved, prices string
		if err := rows.Scan(&it.Idx, &it.Venue, &it.ExternalID, &it.Cluster, &it.Text, &it.URL, &opened, &closes, &resolved, &it.Outcome, &prices); err != nil {
			return nil, err
		}
		for _, pair := range []struct {
			dst *time.Time
			src string
		}{{&it.OpenedAt, opened}, {&it.ClosesAt, closes}, {&it.ResolvedAt, resolved}} {
			t, err := time.Parse(time.RFC3339Nano, pair.src)
			if err != nil {
				return nil, fmt.Errorf("item %d: %w", it.Idx, err)
			}
			*pair.dst = t.UTC()
		}
		if err := json.Unmarshal([]byte(prices), &it.Prices); err != nil {
			return nil, fmt.Errorf("item %d prices: %w", it.Idx, err)
		}
		out = append(out, it)
	}
	return out, rows.Err()
}
