// search.go — multi-indexer search aggregator.
//
// Each row in the `indexers` table is one upstream search source. We
// fan out a query to every enabled indexer in parallel, deduplicate
// by infohash, sort by seeders descending, and return a normalised
// list. Per-indexer timeout is short (~8s default) so a single dead
// source doesn't stall the agent.
//
// Wire shape per kind:
//
//   * jackett  — GET {base_url}/api/v2.0/indexers/all/results?apikey=K&Query=Q&Category=C
//                 Returns JSON: { Results: [ { Title, MagnetUri, InfoHash, Size,
//                                              Seeders, Peers, PublishDate, CategoryDesc,
//                                              Tracker, ... }, ... ] }
//   * prowlarr — GET {base_url}/api/v1/search?query=Q&type=search&apikey=K
//                 Returns JSON: array of { title, magnetUrl, infoHash, size,
//                                          seeders, leechers, publishDate, ... }
//   * rss      — GET {base_url} returning Torznab/RSS XML; we parse the minimum
//                 needed for {title, magnet, infohash, size, seeders, leechers}.
//
// Categories are normalised to a small enum: movie | tv | music | book | software.
// Each kind has its own mapping back to its native category id space.
package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"encoding/xml"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"
)

// SearchResult is the agent-facing normalised shape.
type SearchResult struct {
	Name        string `json:"name"`
	Infohash    string `json:"infohash"`
	Magnet      string `json:"magnet"`
	TorrentURL  string `json:"torrent_url,omitempty"`
	SizeBytes   int64  `json:"size_bytes"`
	Seeders     int    `json:"seeders"`
	Leechers    int    `json:"leechers"`
	PublishedAt string `json:"published_at,omitempty"`
	Category    string `json:"category,omitempty"`
	Indexer     string `json:"indexer"`
	Tracker     string `json:"tracker,omitempty"`
}

// Search is the top-level aggregator. minSeeders=0 disables the
// filter; "" category disables the category filter; sort=""
// defaults to seeders-desc.
func (a *App) searchIndexers(ctx context.Context, query, category string, minSeeders int, sortBy string) ([]SearchResult, error) {
	indexers, err := listIndexers(a.ctx.AppDB(), projectScope(), true)
	if err != nil {
		return nil, err
	}
	if len(indexers) == 0 {
		return nil, fmt.Errorf("no enabled indexers — add at least one in the panel's Indexers tab")
	}

	timeout := time.Duration(configInt(a.ctx, "indexer_query_timeout_seconds", 8)) * time.Second
	cctx, cancel := context.WithTimeout(ctx, timeout+2*time.Second)
	defer cancel()

	type indexerOutcome struct {
		results []SearchResult
		err     error
		name    string
	}
	resCh := make(chan indexerOutcome, len(indexers))

	for _, ix := range indexers {
		ix := ix
		go func() {
			rs, err := a.queryIndexer(cctx, ix, query, category, timeout)
			if err != nil {
				a.markIndexerError(ix.ID, err.Error())
			} else {
				a.markIndexerOK(ix.ID)
			}
			resCh <- indexerOutcome{results: rs, err: err, name: ix.Name}
		}()
	}

	merged := []SearchResult{}
	for i := 0; i < len(indexers); i++ {
		out := <-resCh
		if out.err != nil {
			a.ctx.Logger().Warn("indexer", "name", out.name, "err", out.err.Error())
			continue
		}
		merged = append(merged, out.results...)
	}

	merged = dedupe(merged)
	if minSeeders > 0 {
		filtered := merged[:0]
		for _, r := range merged {
			if r.Seeders >= minSeeders {
				filtered = append(filtered, r)
			}
		}
		merged = filtered
	}

	switch strings.ToLower(sortBy) {
	case "size", "size_desc":
		sort.Slice(merged, func(i, j int) bool { return merged[i].SizeBytes > merged[j].SizeBytes })
	case "newest", "recent":
		sort.Slice(merged, func(i, j int) bool { return merged[i].PublishedAt > merged[j].PublishedAt })
	default: // seeders desc
		sort.Slice(merged, func(i, j int) bool { return merged[i].Seeders > merged[j].Seeders })
	}
	return merged, nil
}

func (a *App) queryIndexer(ctx context.Context, ix Indexer, query, category string, timeout time.Duration) ([]SearchResult, error) {
	apiKey, err := decryptSecret(ix.APIKeyEnc)
	if err != nil {
		return nil, fmt.Errorf("api key: %w", err)
	}
	httpc := &http.Client{Timeout: timeout}
	switch ix.Kind {
	case "jackett":
		return queryJackett(ctx, httpc, ix.BaseURL, apiKey, query, category, ix.Name)
	case "prowlarr":
		return queryProwlarr(ctx, httpc, ix.BaseURL, apiKey, query, category, ix.Name)
	case "rss":
		return queryTorznabRSS(ctx, httpc, ix.BaseURL, apiKey, query, category, ix.Name)
	case "apibay":
		return queryApibay(ctx, httpc, ix.BaseURL, query, ix.Name)
	default:
		return nil, fmt.Errorf("unknown indexer kind: %s", ix.Kind)
	}
}

// ─── Apibay (TPB public JSON) ───────────────────────────────────────
//
// apibay.org/q.php is The Pirate Bay's public JSON search frontend.
// No API key, no self-hosting — used as the zero-config default so a
// fresh torrent install can search out of the box. The tradeoff is a
// single point of failure: if apibay is down or blocked on the user's
// network, search returns nothing until they add a Jackett/Prowlarr.

type apibayItem struct {
	Name     string `json:"name"`
	InfoHash string `json:"info_hash"`
	Seeders  string `json:"seeders"`
	Leechers string `json:"leechers"`
	NumFiles string `json:"num_files"`
	Size     string `json:"size"`
	Added    string `json:"added"`
	Category string `json:"category"`
	Username string `json:"username"`
}

// publicTrackers — UDP tracker list grafted onto apibay magnets.
// apibay returns infohashes only; without trackers the swarm is hard
// to find. Same set the legacy code-old/dlna project used.
var publicTrackers = []string{
	"udp://tracker.opentrackr.org:1337/announce",
	"udp://open.stealth.si:80/announce",
	"udp://tracker.openbittorrent.com:6969/announce",
	"udp://exodus.desync.com:6969/announce",
	"udp://tracker.torrent.eu.org:451/announce",
	"udp://open.demonii.com:1337/announce",
}

func queryApibay(ctx context.Context, httpc *http.Client, baseURL, query, indexer string) ([]SearchResult, error) {
	if baseURL == "" {
		baseURL = "https://apibay.org"
	}
	u := strings.TrimRight(baseURL, "/") + "/q.php?cat=0&q=" + url.QueryEscape(query)
	req, _ := http.NewRequestWithContext(ctx, "GET", u, nil)
	resp, err := httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return nil, fmt.Errorf("apibay status %d", resp.StatusCode)
	}
	var items []apibayItem
	if err := json.NewDecoder(resp.Body).Decode(&items); err != nil {
		return nil, err
	}
	out := make([]SearchResult, 0, len(items))
	for _, it := range items {
		// apibay returns one synthetic row when there are no matches.
		if it.Name == "No results returned" || it.InfoHash == "" || it.InfoHash == "0000000000000000000000000000000000000000" {
			break
		}
		seeders, _ := strconv.Atoi(it.Seeders)
		leechers, _ := strconv.Atoi(it.Leechers)
		size, _ := strconv.ParseInt(it.Size, 10, 64)
		published := ""
		if ts, err := strconv.ParseInt(it.Added, 10, 64); err == nil && ts > 0 {
			published = time.Unix(ts, 0).UTC().Format(time.RFC3339)
		}
		out = append(out, SearchResult{
			Name:        it.Name,
			Infohash:    it.InfoHash,
			Magnet:      buildApibayMagnet(it.InfoHash, it.Name),
			SizeBytes:   size,
			Seeders:     seeders,
			Leechers:    leechers,
			PublishedAt: published,
			Category:    apibayCategory(it.Category),
			Indexer:     indexer,
		})
	}
	return out, nil
}

func buildApibayMagnet(infoHash, name string) string {
	m := "magnet:?xt=urn:btih:" + infoHash + "&dn=" + url.QueryEscape(name)
	for _, tr := range publicTrackers {
		m += "&tr=" + url.QueryEscape(tr)
	}
	return m
}

// apibayCategory normalises TPB's numeric categories into the app's
// shared category enum. First digit determines the bucket; subcats
// are ignored. 100=audio, 200=video, 300=apps, 400=games, 600=other.
func apibayCategory(cat string) string {
	if cat == "" {
		return ""
	}
	switch cat[0] {
	case '1':
		return "music"
	case '2':
		return "video"
	case '3', '4':
		return "software"
	default:
		return ""
	}
}

// ─── Jackett ────────────────────────────────────────────────────────

type jackettResp struct {
	Results []struct {
		Title        string `json:"Title"`
		MagnetUri    string `json:"MagnetUri"`
		Link         string `json:"Link"`
		InfoHash     string `json:"InfoHash"`
		Size         int64  `json:"Size"`
		Seeders      int    `json:"Seeders"`
		Peers        int    `json:"Peers"`
		PublishDate  string `json:"PublishDate"`
		CategoryDesc string `json:"CategoryDesc"`
		Tracker      string `json:"Tracker"`
	} `json:"Results"`
}

func queryJackett(ctx context.Context, httpc *http.Client, baseURL, apiKey, query, category, indexer string) ([]SearchResult, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return nil, err
	}
	if !strings.Contains(u.Path, "/results") {
		u.Path = strings.TrimRight(u.Path, "/") + "/api/v2.0/indexers/all/results"
	}
	q := u.Query()
	q.Set("apikey", apiKey)
	q.Set("Query", query)
	for _, c := range jackettCategoriesFor(category) {
		q.Add("Category[]", strconv.Itoa(c))
	}
	u.RawQuery = q.Encode()

	body, err := getJSON(ctx, httpc, u.String())
	if err != nil {
		return nil, err
	}
	var jr jackettResp
	if err := json.Unmarshal(body, &jr); err != nil {
		return nil, fmt.Errorf("jackett parse: %w", err)
	}
	out := make([]SearchResult, 0, len(jr.Results))
	for _, r := range jr.Results {
		ih := strings.ToLower(strings.TrimSpace(r.InfoHash))
		if ih == "" && r.MagnetUri != "" {
			ih = infohashFromMagnet(r.MagnetUri)
		}
		out = append(out, SearchResult{
			Name:        r.Title,
			Infohash:    ih,
			Magnet:      r.MagnetUri,
			TorrentURL:  r.Link,
			SizeBytes:   r.Size,
			Seeders:     r.Seeders,
			Leechers:    max0(r.Peers - r.Seeders),
			PublishedAt: r.PublishDate,
			Category:    normaliseCategory(r.CategoryDesc),
			Indexer:     indexer,
			Tracker:     r.Tracker,
		})
	}
	return out, nil
}

// jackettCategoriesFor maps our enum to the Newznab numeric ranges
// Jackett uses internally. Empty input returns no filter.
func jackettCategoriesFor(c string) []int {
	switch strings.ToLower(c) {
	case "movie", "movies":
		return []int{2000} // Movies (parent)
	case "tv", "show", "shows":
		return []int{5000} // TV
	case "music":
		return []int{3000}
	case "book", "books":
		return []int{7000}
	case "software", "apps":
		return []int{4000}
	default:
		return nil
	}
}

// ─── Prowlarr ───────────────────────────────────────────────────────

type prowlarrResult struct {
	Title       string `json:"title"`
	MagnetURL   string `json:"magnetUrl"`
	DownloadURL string `json:"downloadUrl"`
	InfoHash    string `json:"infoHash"`
	Size        int64  `json:"size"`
	Seeders     int    `json:"seeders"`
	Leechers    int    `json:"leechers"`
	PublishDate string `json:"publishDate"`
	Categories  []struct {
		Name string `json:"name"`
	} `json:"categories"`
	Indexer string `json:"indexer"`
}

func queryProwlarr(ctx context.Context, httpc *http.Client, baseURL, apiKey, query, category, indexer string) ([]SearchResult, error) {
	u, err := url.Parse(strings.TrimRight(baseURL, "/"))
	if err != nil {
		return nil, err
	}
	if !strings.Contains(u.Path, "/api/v1") {
		u.Path = strings.TrimRight(u.Path, "/") + "/api/v1/search"
	}
	q := u.Query()
	q.Set("apikey", apiKey)
	q.Set("query", query)
	q.Set("type", "search")
	if cats := prowlarrCategoriesFor(category); len(cats) > 0 {
		q.Set("categories", strings.Join(cats, ","))
	}
	u.RawQuery = q.Encode()

	body, err := getJSON(ctx, httpc, u.String())
	if err != nil {
		return nil, err
	}
	var rs []prowlarrResult
	if err := json.Unmarshal(body, &rs); err != nil {
		return nil, fmt.Errorf("prowlarr parse: %w", err)
	}
	out := make([]SearchResult, 0, len(rs))
	for _, r := range rs {
		ih := strings.ToLower(strings.TrimSpace(r.InfoHash))
		if ih == "" && r.MagnetURL != "" {
			ih = infohashFromMagnet(r.MagnetURL)
		}
		cat := ""
		if len(r.Categories) > 0 {
			cat = normaliseCategory(r.Categories[0].Name)
		}
		out = append(out, SearchResult{
			Name:        r.Title,
			Infohash:    ih,
			Magnet:      r.MagnetURL,
			TorrentURL:  r.DownloadURL,
			SizeBytes:   r.Size,
			Seeders:     r.Seeders,
			Leechers:    r.Leechers,
			PublishedAt: r.PublishDate,
			Category:    cat,
			Indexer:     pickName(r.Indexer, indexer),
		})
	}
	return out, nil
}

func prowlarrCategoriesFor(c string) []string {
	switch strings.ToLower(c) {
	case "movie", "movies":
		return []string{"2000"}
	case "tv":
		return []string{"5000"}
	case "music":
		return []string{"3000"}
	case "book", "books":
		return []string{"7000"}
	case "software":
		return []string{"4000"}
	}
	return nil
}

// ─── Torznab / RSS ──────────────────────────────────────────────────

type torznabFeed struct {
	XMLName xml.Name `xml:"rss"`
	Channel struct {
		Item []torznabItem `xml:"item"`
	} `xml:"channel"`
}

type torznabItem struct {
	Title    string  `xml:"title"`
	Link     string  `xml:"link"`
	GUID     string  `xml:"guid"`
	PubDate  string  `xml:"pubDate"`
	Size     int64   `xml:"size"`
	Enclosure struct {
		URL  string `xml:"url,attr"`
		Type string `xml:"type,attr"`
	} `xml:"enclosure"`
	// Torznab-specific extension attributes are encoded as <torznab:attr name="seeders" value="42"/>
	Attrs []struct {
		Name  string `xml:"name,attr"`
		Value string `xml:"value,attr"`
	} `xml:"http://torznab.com/schemas/2015/feed attr"`
}

func queryTorznabRSS(ctx context.Context, httpc *http.Client, baseURL, apiKey, query, category, indexer string) ([]SearchResult, error) {
	u, err := url.Parse(baseURL)
	if err != nil {
		return nil, err
	}
	q := u.Query()
	if apiKey != "" {
		q.Set("apikey", apiKey)
	}
	if query != "" {
		q.Set("q", query)
	}
	if cat := torznabCategoryFor(category); cat != "" {
		q.Set("cat", cat)
	}
	q.Set("t", "search")
	u.RawQuery = q.Encode()

	body, err := getRaw(ctx, httpc, u.String())
	if err != nil {
		return nil, err
	}
	var feed torznabFeed
	if err := xml.Unmarshal(body, &feed); err != nil {
		return nil, fmt.Errorf("torznab parse: %w", err)
	}
	out := make([]SearchResult, 0, len(feed.Channel.Item))
	for _, it := range feed.Channel.Item {
		seeders, leechers, ih := 0, 0, ""
		for _, a := range it.Attrs {
			switch strings.ToLower(a.Name) {
			case "seeders":
				seeders, _ = strconv.Atoi(a.Value)
			case "leechers", "peers":
				leechers, _ = strconv.Atoi(a.Value)
			case "infohash":
				ih = strings.ToLower(a.Value)
			}
		}
		size := it.Size
		mag := ""
		torURL := it.Link
		if strings.HasPrefix(it.Enclosure.URL, "magnet:") {
			mag = it.Enclosure.URL
		} else if it.Enclosure.URL != "" {
			torURL = it.Enclosure.URL
		}
		if ih == "" && mag != "" {
			ih = infohashFromMagnet(mag)
		}
		out = append(out, SearchResult{
			Name:        it.Title,
			Infohash:    ih,
			Magnet:      mag,
			TorrentURL:  torURL,
			SizeBytes:   size,
			Seeders:     seeders,
			Leechers:    leechers,
			PublishedAt: it.PubDate,
			Indexer:     indexer,
		})
	}
	return out, nil
}

func torznabCategoryFor(c string) string {
	switch strings.ToLower(c) {
	case "movie", "movies":
		return "2000"
	case "tv":
		return "5000"
	case "music":
		return "3000"
	case "book", "books":
		return "7000"
	case "software":
		return "4000"
	}
	return ""
}

// ─── helpers ────────────────────────────────────────────────────────

func getJSON(ctx context.Context, httpc *http.Client, u string) ([]byte, error) {
	body, err := getRaw(ctx, httpc, u)
	if err != nil {
		return nil, err
	}
	return body, nil
}

func getRaw(ctx context.Context, httpc *http.Client, u string) ([]byte, error) {
	req, err := http.NewRequestWithContext(ctx, "GET", u, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Apteva-torrent/0.1")
	resp, err := httpc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return nil, fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}
	return io.ReadAll(resp.Body)
}

// dedupe collapses results sharing an infohash to the highest-seeded
// instance — that's the one most likely to actually download. Items
// missing an infohash are kept verbatim (they can still be added by
// magnet/url; we just can't cross-reference them).
func dedupe(in []SearchResult) []SearchResult {
	best := map[string]int{}
	out := []SearchResult{}
	for _, r := range in {
		if r.Infohash == "" {
			out = append(out, r)
			continue
		}
		idx, exists := best[r.Infohash]
		if !exists {
			best[r.Infohash] = len(out)
			out = append(out, r)
			continue
		}
		if r.Seeders > out[idx].Seeders {
			out[idx] = r
		}
	}
	return out
}

func infohashFromMagnet(magnet string) string {
	const prefix = "urn:btih:"
	i := strings.Index(magnet, prefix)
	if i < 0 {
		return ""
	}
	rest := magnet[i+len(prefix):]
	end := strings.IndexAny(rest, "&?")
	if end >= 0 {
		rest = rest[:end]
	}
	rest = strings.ToLower(rest)
	// urn:btih: can be 40-char hex (v1) or 32-char base32 (v1) or
	// hex-encoded sha256 (v2). For dedupe we just need a stable key.
	if len(rest) > 50 {
		rest = rest[:50]
	}
	return rest
}

func normaliseCategory(s string) string {
	s = strings.ToLower(s)
	switch {
	case strings.Contains(s, "movie"):
		return "movie"
	case strings.Contains(s, "tv"):
		return "tv"
	case strings.Contains(s, "music") || strings.Contains(s, "audio"):
		return "music"
	case strings.Contains(s, "book"):
		return "book"
	case strings.Contains(s, "pc") || strings.Contains(s, "software"):
		return "software"
	default:
		return ""
	}
}

func pickName(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func max0(n int) int {
	if n < 0 {
		return 0
	}
	return n
}

// ─── indexer DB layer ───────────────────────────────────────────────

type Indexer struct {
	ID            int64    `json:"id"`
	ProjectID     string   `json:"project_id"`
	Name          string   `json:"name"`
	Kind          string   `json:"kind"`
	BaseURL       string   `json:"base_url"`
	APIKeyEnc     string   `json:"-"`
	Categories    []string `json:"categories"`
	Priority      int      `json:"priority"`
	Enabled       bool     `json:"enabled"`
	LastOKAt      string   `json:"last_ok_at,omitempty"`
	LastError     string   `json:"last_error,omitempty"`
}

func listIndexers(db *sql.DB, pid string, onlyEnabled bool) ([]Indexer, error) {
	q := `SELECT id, project_id, name, kind, base_url, api_key_enc,
	             categories_json, priority, enabled, last_ok_at, last_error
	        FROM indexers WHERE project_id = ?`
	if onlyEnabled {
		q += ` AND enabled = 1`
	}
	q += ` ORDER BY priority, id`
	rows, err := db.Query(q, pid)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Indexer{}
	for rows.Next() {
		var ix Indexer
		var catsJSON, lastOK string
		var en int
		if err := rows.Scan(&ix.ID, &ix.ProjectID, &ix.Name, &ix.Kind, &ix.BaseURL,
			&ix.APIKeyEnc, &catsJSON, &ix.Priority, &en, &lastOK, &ix.LastError); err != nil {
			return nil, err
		}
		ix.Enabled = en == 1
		ix.LastOKAt = lastOK
		_ = json.Unmarshal([]byte(catsJSON), &ix.Categories)
		out = append(out, ix)
	}
	return out, nil
}

func (a *App) markIndexerError(id int64, msg string) {
	_, _ = a.ctx.AppDB().Exec(
		`UPDATE indexers SET last_error = ? WHERE id = ?`, msg, id)
}

func (a *App) markIndexerOK(id int64) {
	_, _ = a.ctx.AppDB().Exec(
		`UPDATE indexers SET last_ok_at = ?, last_error = '' WHERE id = ?`,
		time.Now().UTC().Format(time.RFC3339), id)
}

