package main

// DataForSEO-specific call wrappers + response normalisers.
//
// The integration runner does the HTTP plumbing (Basic auth from
// the bound connection's `login`/`password` fields, base_url
// resolution, request body assembly per the input_schema). Apps
// only call PlatformAPI().ExecuteIntegrationTool and parse the
// `data` payload — which is the raw DataForSEO response envelope.
//
// DataForSEO's response envelope:
//
//   {
//     "version": "...",
//     "status_code": 20000,
//     "tasks": [{
//       "id": "...",
//       "status_code": 20000,        // 20000 = task ok
//       "result": [{ <actual data> }]
//     }]
//   }
//
// Each tool below: build args → ExecuteIntegrationTool → unwrap to
// tasks[0].result[0] (or [...] for list endpoints) → translate to
// our row shape → write to DB → return summary.
//
// The whole tasks[0] payload is preserved on each snapshot row's
// raw_json so post-hoc analyses (or future typed columns) don't
// require re-spending API calls.

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// dfsEnvelope mirrors the top-level shape of every /v3/.../live
// response we use. Endpoint-specific result shapes get decoded
// against tasks[0].result via json.RawMessage indirection.
type dfsEnvelope struct {
	StatusCode int       `json:"status_code"`
	StatusMsg  string    `json:"status_message"`
	Tasks      []dfsTask `json:"tasks"`
}

type dfsTask struct {
	ID         string            `json:"id"`
	StatusCode int               `json:"status_code"`
	StatusMsg  string            `json:"status_message"`
	Result     []json.RawMessage `json:"result"`
}

// callDfs wraps ExecuteIntegrationTool + envelope sanity-check. It
// returns the first task's first result row as raw JSON, plus the
// whole task[0] payload for raw_json archival.
func callDfs(ctx *sdk.AppCtx, connID int64, tool string, input map[string]any) (resultRow []byte, taskRaw []byte, err error) {
	res, err := ctx.PlatformAPI().ExecuteIntegrationTool(connID, tool, input)
	if err != nil {
		return nil, nil, fmt.Errorf("dataforseo: ExecuteIntegrationTool(%s): %w", tool, err)
	}
	if !res.Success || res.Status >= 400 {
		return nil, nil, fmt.Errorf("dataforseo: %s returned HTTP %d", tool, res.Status)
	}
	var env dfsEnvelope
	if err := json.Unmarshal(res.Data, &env); err != nil {
		return nil, nil, fmt.Errorf("dataforseo: parse envelope: %w", err)
	}
	if env.StatusCode != 20000 {
		return nil, nil, fmt.Errorf("dataforseo: status %d: %s", env.StatusCode, env.StatusMsg)
	}
	if len(env.Tasks) == 0 {
		return nil, nil, fmt.Errorf("dataforseo: %s returned zero tasks", tool)
	}
	t := env.Tasks[0]
	if t.StatusCode != 20000 {
		return nil, nil, fmt.Errorf("dataforseo: task status %d: %s", t.StatusCode, t.StatusMsg)
	}
	taskRaw, _ = json.Marshal(t)
	if len(t.Result) == 0 {
		// Some endpoints legitimately return zero result rows
		// (e.g. backlinks_list when nothing matches the filter).
		// Surface as nil resultRow + nil err so callers can decide.
		return nil, taskRaw, nil
	}
	return t.Result[0], taskRaw, nil
}

// ─── Domain rank overview ────────────────────────────────────────

// dfsDomainRankResult mirrors dataforseo_labs/google/domain_rank_overview/live.
// Only the columns we surface are typed; `raw_json` carries the rest.
type dfsDomainRankResult struct {
	Items []struct {
		SETopType    string `json:"se_type"`
		LocationCode int    `json:"location_code"`
		Metrics      struct {
			Organic struct {
				Count                  int64   `json:"count"`
				ETV                    float64 `json:"etv"`
				EstimatedPaidTraffic   float64 `json:"estimated_paid_traffic_cost"`
			} `json:"organic"`
			Paid struct {
				Count int64   `json:"count"`
				ETV   float64 `json:"etv"`
			} `json:"paid"`
		} `json:"metrics"`
		// DataForSEO's domain_rank_overview doesn't ship DR/DA itself —
		// those come from the dataforseo_labs/.../bulk_domain_ranks
		// endpoint. authority_score stays null until v0.2.1 wires that.
	} `json:"items"`
}

func refreshDomainViaDataForSEO(ctx *sdk.AppCtx, d *Domain) (any, error) {
	_, connID, err := boundProvider(ctx)
	if err != nil {
		return nil, err
	}
	rowRaw, taskRaw, err := callDfs(ctx, connID, "domain_rank_overview", map[string]any{
		"target":        d.Host,
		"location_code": 2840, // US — TODO v0.3 honour per-domain locale
		"language_code": "en",
	})
	if err != nil {
		return nil, err
	}
	if rowRaw == nil {
		return nil, fmt.Errorf("dataforseo returned no rows for %s", d.Host)
	}
	var parsed dfsDomainRankResult
	if err := json.Unmarshal(rowRaw, &parsed); err != nil {
		return nil, fmt.Errorf("parse domain_rank_overview: %w", err)
	}
	if len(parsed.Items) == 0 {
		return nil, fmt.Errorf("domain_rank_overview: zero items for %s", d.Host)
	}
	m := parsed.Items[0].Metrics
	now := time.Now().Unix()
	res, err := ctx.AppDB().Exec(
		`INSERT INTO domain_metrics
		    (domain_id, provider, ts, country_iso, organic_traffic,
		     organic_keywords, paid_traffic, paid_keywords, raw_json)
		 VALUES (?, 'dataforseo', ?, 'US', ?, ?, ?, ?, ?)`,
		d.ID, now,
		int64(m.Organic.ETV), m.Organic.Count,
		int64(m.Paid.ETV), m.Paid.Count,
		string(taskRaw),
	)
	if err != nil {
		return nil, fmt.Errorf("insert domain_metrics: %w", err)
	}
	id, _ := res.LastInsertId()
	return map[string]any{
		"domain_id":     d.ID,
		"snapshot_id":   id,
		"provider":      "dataforseo",
		"fetched_at":    now,
		"organic_kw":    m.Organic.Count,
		"organic_etv":   int64(m.Organic.ETV),
		"paid_kw":       m.Paid.Count,
		"paid_etv":      int64(m.Paid.ETV),
	}, nil
}

// ─── Keyword search-volume + monthly history ─────────────────────

// dfsKeywordVolumeItem is the per-keyword row inside the
// keywords_data/google_ads/search_volume/live result. We send one
// keyword per call; the monthly_searches array is the inline history
// we unfold into keyword_volume_history.
type dfsKeywordVolumeItem struct {
	Keyword         string `json:"keyword"`
	LocationCode    int    `json:"location_code"`
	LanguageCode    string `json:"language_code"`
	SearchVolume    *int64 `json:"search_volume"`
	CompetitionIdx  *int64 `json:"competition_index"`
	CPC             *float64 `json:"cpc"`
	LowTopOfPageBid *float64 `json:"low_top_of_page_bid"`
	HighTopOfPageBid *float64 `json:"high_top_of_page_bid"`
	MonthlySearches []struct {
		Year   int   `json:"year"`
		Month  int   `json:"month"`
		Volume int64 `json:"search_volume"`
	} `json:"monthly_searches"`
}

func refreshKeywordViaDataForSEO(ctx *sdk.AppCtx, k *Keyword) (any, error) {
	_, connID, err := boundProvider(ctx)
	if err != nil {
		return nil, err
	}
	loc := dfsLocationCode(k.CountryISO) // US=2840 etc.
	rowRaw, taskRaw, err := callDfs(ctx, connID, "keyword_search_volume", map[string]any{
		"keywords":      []string{k.Text},
		"location_code": loc,
		"language_code": strings.ToLower(k.LanguageISO),
	})
	if err != nil {
		return nil, err
	}
	if rowRaw == nil {
		return nil, fmt.Errorf("dataforseo: zero rows for keyword %q", k.Text)
	}
	// search_volume returns: { items: [<dfsKeywordVolumeItem>, …] }
	var parsed struct {
		Items []dfsKeywordVolumeItem `json:"items"`
	}
	if err := json.Unmarshal(rowRaw, &parsed); err != nil {
		return nil, fmt.Errorf("parse keyword_search_volume: %w", err)
	}
	if len(parsed.Items) == 0 {
		return nil, fmt.Errorf("keyword_search_volume: zero items for %q", k.Text)
	}
	item := parsed.Items[0]
	now := time.Now().Unix()
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(
		`INSERT INTO keyword_metrics
		   (keyword_id, provider, ts, volume, cpc_usd, raw_json)
		 VALUES (?, 'dataforseo', ?, ?, ?, ?)`,
		k.ID, now, item.SearchVolume, item.CPC, string(taskRaw),
	)
	if err != nil {
		return nil, fmt.Errorf("insert keyword_metrics: %w", err)
	}
	snapID, _ := res.LastInsertId()

	// Unfold monthly_searches → keyword_volume_history. Upsert on the
	// (keyword_id, provider, year, month) UNIQUE so re-refreshing
	// doesn't duplicate. ON CONFLICT updates volume to the freshest
	// figure DataForSEO reports for that month.
	for _, mo := range item.MonthlySearches {
		if mo.Year == 0 || mo.Month == 0 {
			continue
		}
		if _, err := tx.Exec(
			`INSERT INTO keyword_volume_history
			   (keyword_id, provider, year, month, volume)
			 VALUES (?, 'dataforseo', ?, ?, ?)
			 ON CONFLICT(keyword_id, provider, year, month)
			 DO UPDATE SET volume = excluded.volume`,
			k.ID, mo.Year, mo.Month, mo.Volume,
		); err != nil {
			return nil, fmt.Errorf("upsert volume history (%d-%02d): %w", mo.Year, mo.Month, err)
		}
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return map[string]any{
		"keyword_id":   k.ID,
		"snapshot_id":  snapID,
		"provider":     "dataforseo",
		"fetched_at":   now,
		"volume":       valOr(item.SearchVolume, 0),
		"history_rows": len(item.MonthlySearches),
	}, nil
}

// ─── Backlinks list ──────────────────────────────────────────────

type dfsBacklinkItem struct {
	URLFrom         string  `json:"url_from"`
	URLTo           string  `json:"url_to"`
	Anchor          string  `json:"anchor"`
	Dofollow        bool    `json:"dofollow"`
	Attributes      []string `json:"attributes"`
	IsLost          bool    `json:"is_lost"`
	DomainFromRank  *int64  `json:"domain_from_rank"`
	FirstSeen       string  `json:"first_seen"`
	LastSeen        string  `json:"last_seen"`
}

func refreshBacklinksViaDataForSEO(ctx *sdk.AppCtx, d *Domain) (any, error) {
	_, connID, err := boundProvider(ctx)
	if err != nil {
		return nil, err
	}
	rowRaw, _, err := callDfs(ctx, connID, "backlinks_list", map[string]any{
		"target": d.Host,
		"mode":   "as_is",
		"limit":  100, // v0.2 cap; v0.3 paginates
		"backlinks_status_type": "all",
	})
	if err != nil {
		return nil, err
	}
	if rowRaw == nil {
		return map[string]any{"domain_id": d.ID, "rows_upserted": 0, "note": "no backlinks reported"}, nil
	}
	var parsed struct {
		Items []dfsBacklinkItem `json:"items"`
	}
	if err := json.Unmarshal(rowRaw, &parsed); err != nil {
		return nil, fmt.Errorf("parse backlinks_list: %w", err)
	}
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()

	upserts := 0
	for _, b := range parsed.Items {
		// Per-row raw_json so post-hoc analyses (anchor diversity,
		// link types) don't require re-fetching.
		raw, _ := json.Marshal(b)
		isUGC := containsAttr(b.Attributes, "ugc")
		isSponsored := containsAttr(b.Attributes, "sponsored")
		isNofollow := !b.Dofollow
		_, err := tx.Exec(
			`INSERT INTO backlinks
			   (domain_id, provider, source_url, dest_url, anchor,
			    is_dofollow, is_nofollow, is_ugc, is_sponsored,
			    source_authority, first_seen, last_seen, is_lost, raw_json)
			 VALUES (?, 'dataforseo', ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
			 ON CONFLICT(domain_id, provider, source_url, dest_url, anchor)
			 DO UPDATE SET
			    last_seen = excluded.last_seen,
			    is_lost   = excluded.is_lost,
			    raw_json  = excluded.raw_json`,
			d.ID, b.URLFrom, b.URLTo, b.Anchor,
			boolToInt(b.Dofollow), boolToInt(isNofollow),
			boolToInt(isUGC), boolToInt(isSponsored),
			b.DomainFromRank,
			parseDfsTime(b.FirstSeen),
			parseDfsTime(b.LastSeen),
			boolToInt(b.IsLost),
			string(raw),
		)
		if err != nil {
			return nil, fmt.Errorf("upsert backlink: %w", err)
		}
		upserts++
	}
	if err := tx.Commit(); err != nil {
		return nil, err
	}
	return map[string]any{
		"domain_id":     d.ID,
		"provider":      "dataforseo",
		"rows_upserted": upserts,
		"capped_at":     100,
	}, nil
}

// ─── Helpers ─────────────────────────────────────────────────────

// dfsLocationCode maps 2-letter ISO → DataForSEO's integer
// location_code. Coverage: the top markets only; everything else
// falls back to US (2840). v0.3 should hydrate this from
// /v3/dataforseo_labs/locations_and_languages on first call and
// cache, so we don't need to compile-in a 200-row table.
func dfsLocationCode(iso string) int {
	switch strings.ToUpper(iso) {
	case "US":
		return 2840
	case "GB", "UK":
		return 2826
	case "CA":
		return 2124
	case "AU":
		return 2036
	case "DE":
		return 2276
	case "FR":
		return 2250
	case "ES":
		return 2724
	case "IT":
		return 2380
	case "BR":
		return 2076
	case "IN":
		return 2356
	case "JP":
		return 2392
	case "MX":
		return 2484
	case "NL":
		return 2528
	default:
		return 2840
	}
}

// parseDfsTime parses DataForSEO's "YYYY-MM-DD HH:MM:SS +00:00"
// timestamps to unix seconds. Returns nil on any parse failure so
// the column lands NULL rather than 0.
func parseDfsTime(s string) any {
	s = strings.TrimSpace(s)
	if s == "" {
		return nil
	}
	for _, layout := range []string{
		"2006-01-02 15:04:05 -07:00",
		"2006-01-02 15:04:05",
		"2006-01-02T15:04:05Z",
		time.RFC3339,
	} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.Unix()
		}
	}
	return nil
}

func containsAttr(xs []string, want string) bool {
	for _, x := range xs {
		if strings.EqualFold(x, want) {
			return true
		}
	}
	return false
}

func valOr[T any](p *T, def T) T {
	if p == nil {
		return def
	}
	return *p
}

// Compile-time safety: ensure we still pull in database/sql even if
// later edits drop other usages. (Build will fail loud if not.)
var _ = sql.ErrNoRows
