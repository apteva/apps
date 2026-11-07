package main

import (
	"context"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"time"
)

type callStatisticsFilter struct {
	From, Until                                 time.Time
	Location                                    *time.Location
	Provider, Adviser, Number, Direction, Focus string
	SegmentFrom, SegmentUntil                   int64
	Threshold                                   float64
	Cursor                                      string
}
type statisticsCursor struct {
	At int64  `json:"at"`
	ID string `json:"id"`
}

func parseCallStatisticsFilter(r *http.Request, now time.Time) (callStatisticsFilter, error) {
	q := r.URL.Query()
	f := callStatisticsFilter{From: now.Add(-24 * time.Hour).Truncate(time.Second), Until: now.Truncate(time.Second), Location: time.UTC, Provider: q.Get("provider"), Adviser: q.Get("adviser"), Number: q.Get("number"), Direction: q.Get("direction"), Focus: q.Get("focus"), Threshold: 200, Cursor: q.Get("cursor")}
	for k, v := range map[string]*time.Time{"from": &f.From, "until": &f.Until} {
		if q.Get(k) != "" {
			t, e := time.Parse(time.RFC3339, q.Get(k))
			if e != nil {
				return f, fmt.Errorf("invalid %s", k)
			}
			*v = t.Truncate(time.Second)
		}
	}
	if !f.From.Before(f.Until) || f.Until.Sub(f.From) > 31*24*time.Hour {
		return f, errors.New("choose a time range of at most 31 days")
	}
	if q.Get("timezone") != "" {
		loc, e := time.LoadLocation(q.Get("timezone"))
		if e != nil {
			return f, errors.New("unknown timezone")
		}
		f.Location = loc
	}
	if s := q.Get("threshold_ms"); s != "" {
		switch s {
		case "20":
			f.Threshold = 20
		case "200":
			f.Threshold = 200
		case "1000":
			f.Threshold = 1000
		default:
			return f, errors.New("threshold_ms must be 20, 200 or 1000")
		}
	}
	if f.Direction != "" && f.Direction != "inbound" && f.Direction != "outbound" {
		return f, errors.New("unknown direction")
	}
	if f.Focus != "" && f.Focus != "problems" && f.Focus != "unobserved" && f.Focus != "failed" && f.Focus != "missed" {
		return f, errors.New("unknown call focus")
	}
	if len(f.Provider) > 64 || len(f.Adviser) > 1024 || len(f.Number) > 64 || len(f.Cursor) > 2048 {
		return f, errors.New("filter too long")
	}
	f.SegmentFrom, f.SegmentUntil = f.From.Unix(), f.Until.Unix()
	for k, v := range map[string]*int64{"segment_from": &f.SegmentFrom, "segment_until": &f.SegmentUntil} {
		if q.Get(k) != "" {
			t, e := time.Parse(time.RFC3339, q.Get(k))
			if e != nil {
				return f, errors.New("invalid chart segment")
			}
			*v = t.Unix()
		}
	}
	if f.SegmentFrom < f.From.Unix() || f.SegmentUntil > f.Until.Unix() || f.SegmentFrom >= f.SegmentUntil {
		return f, errors.New("chart segment outside selected period")
	}
	if f.Cursor != "" {
		var c statisticsCursor
		raw, e := base64.RawURLEncoding.DecodeString(f.Cursor)
		if e != nil || json.Unmarshal(raw, &c) != nil || c.ID == "" || len(c.ID) > 128 || c.At < f.SegmentFrom || c.At >= f.SegmentUntil {
			return f, errors.New("invalid statistics cursor")
		}
	}
	return f, nil
}

type statisticsTotals struct {
	MetricCalls        map[string]int `json:"metric_calls"`
	Calls              int            `json:"calls"`
	Answered           int            `json:"answered"`
	Missed             int            `json:"missed"`
	Failed             int            `json:"failed"`
	Active             int            `json:"active"`
	Observed           int            `json:"observed"`
	Problems           int            `json:"problems"`
	Pending            int            `json:"pending"`
	TalkSeconds        float64        `json:"talk_seconds"`
	DurationCalls      int            `json:"duration_calls"`
	AverageSeconds     *float64       `json:"average_seconds"`
	ProblemPercent     *float64       `json:"problem_percent"`
	CoveragePercent    *float64       `json:"coverage_percent"`
	SIPDiscardMS       float64        `json:"sip_discard_ms"`
	SIPConcealedMS     float64        `json:"sip_concealed_ms"`
	BrowserDiscardMS   float64        `json:"browser_discard_ms"`
	BrowserConcealedMS float64        `json:"browser_concealed_ms"`
	CaptureDiscardMS   float64        `json:"capture_discard_ms"`
	IngressDiscardMS   float64        `json:"ingress_discard_ms"`
	PlaybackGapMS      float64        `json:"playback_gap_ms"`
	SendDiscardMS      float64        `json:"send_discard_ms"`
	SIPMissingPackets  float64        `json:"sip_missing_packets"`
	BrowserLostPackets float64        `json:"browser_lost_packets"`
	SendDurationMS     float64        `json:"send_duration_ms"`
	SendRateDiscardMS  float64        `json:"send_rate_discard_ms"`
	SendRateCalls      int            `json:"send_rate_calls"`
	SendDiscardPercent *float64       `json:"send_discard_percent"`
}

func statisticsRatio(n, d float64) *float64 {
	if d <= 0 {
		return nil
	}
	v := 100 * n / d
	return &v
}
func (t *statisticsTotals) finish() {
	t.ProblemPercent = statisticsRatio(float64(t.Problems), float64(t.Observed))
	t.CoveragePercent = statisticsRatio(float64(t.Observed), float64(t.Calls))
	t.SendDiscardPercent = statisticsRatio(t.SendRateDiscardMS, t.SendDurationMS)
	if t.DurationCalls > 0 {
		v := t.TalkSeconds / float64(t.DurationCalls)
		t.AverageSeconds = &v
	}
}

type statisticsBucket struct {
	From  string `json:"from"`
	Until string `json:"until"`
	statisticsTotals
	start, end int64
}
type statisticsGroup struct {
	Key   string `json:"key"`
	Label string `json:"label"`
	statisticsTotals
}
type statisticsCall struct {
	ID                 string  `json:"call_id"`
	Status             string  `json:"status"`
	Direction          string  `json:"direction"`
	Provider           string  `json:"provider"`
	From               string  `json:"from"`
	To                 string  `json:"to"`
	StartedAt          string  `json:"started_at"`
	Adviser            string  `json:"adviser"`
	Observed           bool    `json:"observed"`
	Problem            bool    `json:"problem"`
	TalkSeconds        float64 `json:"talk_seconds"`
	start, answer, end int64
	owner, number      string
	pending            bool
	durationKnown      bool
	metrics            []float64
	coverage           int
}

func (t *statisticsTotals) add(c statisticsCall) {
	t.Calls++
	if c.answer > 0 {
		t.Answered++
	}
	if c.durationKnown {
		t.DurationCalls++
		t.TalkSeconds += c.TalkSeconds
	}
	if !isTerminalStatus(c.Status) && c.end == 0 {
		t.Active++
	}
	if c.Status == "failed" {
		t.Failed++
	}
	if c.Direction == "inbound" && c.answer == 0 && isTerminalStatus(c.Status) {
		t.Missed++
	}
	if c.Observed {
		t.Observed++
		if c.Problem {
			t.Problems++
		}
	}
	if c.pending {
		t.Pending++
	}
	if !c.Observed {
		return
	}
	v := c.metrics
	if t.MetricCalls == nil {
		t.MetricCalls = map[string]int{}
	}
	for bit, key := range []string{"sip", "browser", "webrtc", "capture", "ingress", "send", "playback_gap"} {
		if c.coverage&(1<<bit) != 0 {
			t.MetricCalls[key]++
		}
	}
	t.IngressDiscardMS += v[11]
	t.PlaybackGapMS += v[12]
	t.SIPDiscardMS += v[0]
	t.SIPConcealedMS += v[1]
	t.BrowserDiscardMS += v[2]
	t.BrowserConcealedMS += v[3]
	t.CaptureDiscardMS += v[4]
	t.SendDiscardMS += v[5]
	t.SIPMissingPackets += v[6]
	t.BrowserLostPackets += v[7]
	// The denominator is offered audio at this SAME send pacer: sent + dropped.
	// Legacy reports without both counters contribute durations, never a fake rate.
	if v[9] >= 0 && v[10] >= 0 && v[9]+v[10] > 0 {
		t.SendRateCalls++
		t.SendRateDiscardMS += v[10]
		t.SendDurationMS += v[9] + v[10]
	}
}
func statisticsBuckets(f callStatisticsFilter) []statisticsBucket {
	result := []statisticsBucket{}
	day := f.Until.Sub(f.From) > 72*time.Hour
	for start := f.From; start.Before(f.Until); {
		local := start.In(f.Location)
		var next time.Time
		if day {
			next = time.Date(local.Year(), local.Month(), local.Day()+1, 0, 0, 0, 0, f.Location)
		} else {
			next = start.Add(time.Hour)
		}
		if next.After(f.Until) {
			next = f.Until
		}
		result = append(result, statisticsBucket{From: start.UTC().Format(time.RFC3339), Until: next.UTC().Format(time.RFC3339), start: start.Unix(), end: next.Unix()})
		start = next
	}
	return result
}

const statisticsJoin = ` FROM calls c LEFT JOIN telephony_audio_reports a ON a.call_id=c.id AND a.project_id=c.project_id LEFT JOIN telephony_call_owners o ON o.call_id=c.id AND o.project_id=c.project_id LEFT JOIN telephony_audio_pending p ON p.call_id=c.id `

func (f callStatisticsFilter) where(project string) (string, []any) {
	w := `c.project_id=? AND unixepoch(c.placed_at)>=? AND unixepoch(c.placed_at)<?`
	args := []any{project, f.From.Add(-f.Until.Sub(f.From)).Unix(), f.Until.Unix()}
	for _, x := range [][2]string{{"c.carrier_slug", f.Provider}, {"c.direction", f.Direction}, {"COALESCE(o.principal,'')", f.Adviser}, {"CASE WHEN c.direction='inbound' THEN c.to_number ELSE c.from_number END", f.Number}} {
		if x[1] != "" {
			w += " AND " + x[0] + "=?"
			value := x[1]
			if value == "__unassigned__" {
				value = ""
			}
			args = append(args, value)
		}
	}
	return w, args
}

// Only compact summary metrics enter this query. Never load recording data,
// RTP history, credentials or raw browser diagnostics for a statistics page.
const statisticsBrowserObserved = `(COALESCE(json_extract(a.summary_json,'$.metrics.browser_observed'),0)=1 OR COALESCE(json_extract(a.summary_json,'$.browser_state'),'')<>'' OR COALESCE(json_extract(a.summary_json,'$.context_state'),'')<>'' OR json_type(a.summary_json,'$.metrics.webrtc_packets_lost') IS NOT NULL)`
const statisticsCoverage = `(CASE WHEN json_type(a.summary_json,'$.metrics.sip_packets_received') IS NOT NULL THEN 1 ELSE 0 END
 + CASE WHEN ` + statisticsBrowserObserved + ` THEN 2 ELSE 0 END
 + CASE WHEN json_type(a.summary_json,'$.metrics.webrtc_packets_lost') IS NOT NULL THEN 4 ELSE 0 END
 + CASE WHEN ` + statisticsBrowserObserved + ` AND json_type(a.summary_json,'$.metrics.capture_dropped_ms') IS NOT NULL THEN 8 ELSE 0 END
 + CASE WHEN ` + statisticsBrowserObserved + ` AND json_type(a.summary_json,'$.metrics.server_webrtc_ingress_discarded_ms') IS NOT NULL THEN 16 ELSE 0 END
 + CASE WHEN COALESCE(json_extract(a.summary_json,'$.metrics.carrier_observed'),0)=1 OR json_type(a.summary_json,'$.metrics.sip_pacer_dropped_ms') IS NOT NULL THEN 32 ELSE 0 END
 + CASE WHEN ` + statisticsBrowserObserved + ` AND json_type(a.summary_json,'$.metrics.playback_underrun_ms') IS NOT NULL THEN 64 ELSE 0 END)`
const statisticsMetrics = `json_array(
 COALESCE(json_extract(a.summary_json,'$.metrics.sip_jitter_discarded_ms'),0),
 COALESCE(json_extract(a.summary_json,'$.metrics.sip_concealed_ms'),0),
 COALESCE(json_extract(a.summary_json,'$.metrics.playback_dropped_ms'),0),
 COALESCE(json_extract(a.summary_json,'$.metrics.webrtc_concealed_ms'),0),
 COALESCE(json_extract(a.summary_json,'$.metrics.capture_dropped_ms'),0),
 COALESCE(json_extract(a.summary_json,'$.metrics.carrier_send_dropped_ms'),0),
 COALESCE(json_extract(a.summary_json,'$.metrics.sip_sequence_loss'),0),
 COALESCE(json_extract(a.summary_json,'$.metrics.webrtc_packets_lost'),0),
 COALESCE(json_extract(a.summary_json,'$.metrics.sip_read_errors'),0)+COALESCE(json_extract(a.summary_json,'$.metrics.sip_send_errors'),0)+COALESCE(json_extract(a.summary_json,'$.metrics.sip_decode_errors'),0)+COALESCE(json_extract(a.summary_json,'$.metrics.carrier_decode_errors'),0)+COALESCE(json_extract(a.summary_json,'$.metrics.sip_bye_failures'),0),
 COALESCE(json_extract(a.summary_json,'$.metrics.sip_pacer_sent_ms'),-1),
 COALESCE(json_extract(a.summary_json,'$.metrics.sip_pacer_dropped_ms'),-1),
 COALESCE(json_extract(a.summary_json,'$.metrics.server_webrtc_ingress_discarded_ms'),0),
 COALESCE(json_extract(a.summary_json,'$.metrics.playback_underrun_ms'),0))`

func statisticsProblem(v []float64, threshold float64) bool {
	for _, i := range []int{0, 1, 2, 3, 4, 5, 11, 12} {
		if v[i] >= threshold {
			return true
		}
	}
	return v[6] > 0 || v[7] > 0 || v[8] > 0
}
func statisticsGroups(values map[string]*statisticsGroup) []statisticsGroup {
	out := make([]statisticsGroup, 0, len(values))
	for _, v := range values {
		v.finish()
		out = append(out, *v)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Calls == out[j].Calls {
			return out[i].Key < out[j].Key
		}
		return out[i].Calls > out[j].Calls
	})
	return out
}
func (c *callsDB) callStatistics(ctx context.Context, project string, f callStatisticsFilter, now time.Time) (map[string]any, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	tx, err := c.db.BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	where, args := f.where(project)
	rows, err := tx.QueryContext(ctx, `SELECT c.id,c.status,c.direction,c.carrier_slug,c.from_number,c.to_number,c.placed_at,unixepoch(c.placed_at),COALESCE(unixepoch(c.answered_at),0),COALESCE(unixepoch(c.ended_at),0),COALESCE(o.principal,''),COALESCE(json_extract(a.summary_json,'$.telemetry'),0),p.call_id IS NOT NULL,`+statisticsMetrics+`,`+statisticsCoverage+statisticsJoin+`WHERE `+where+` ORDER BY unixepoch(c.placed_at) DESC,c.id DESC`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	total, previous := statisticsTotals{}, statisticsTotals{}
	buckets := statisticsBuckets(f)
	providers, advisers, numbers := map[string]*statisticsGroup{}, map[string]*statisticsGroup{}, map[string]*statisticsGroup{}
	calls := []statisticsCall{}
	matching := 0
	next := ""
	var cursor statisticsCursor
	if f.Cursor != "" {
		raw, _ := base64.RawURLEncoding.DecodeString(f.Cursor)
		_ = json.Unmarshal(raw, &cursor)
	}
	for rows.Next() {
		var r statisticsCall
		var raw string
		if err = rows.Scan(&r.ID, &r.Status, &r.Direction, &r.Provider, &r.From, &r.To, &r.StartedAt, &r.start, &r.answer, &r.end, &r.owner, &r.Observed, &r.pending, &raw, &r.coverage); err != nil {
			return nil, err
		}
		if err = json.Unmarshal([]byte(raw), &r.metrics); err != nil {
			return nil, err
		}
		if len(r.metrics) != 13 {
			return nil, errors.New("invalid compact statistics metrics")
		}
		// Invalid old values cannot silently invert a total or yield a negative rate.
		for i := range r.metrics {
			if i != 9 && i != 10 {
				r.metrics[i] = max(0, r.metrics[i])
			}
		}
		r.Problem = r.Observed && statisticsProblem(r.metrics, f.Threshold)
		r.Adviser = audioDashboardAdviser(r.owner, "")
		r.number = r.From
		if r.Direction == "inbound" {
			r.number = r.To
		}
		end := r.end
		if end == 0 && !isTerminalStatus(r.Status) {
			end = now.Unix()
		}
		if r.answer > 0 && end >= r.answer {
			r.TalkSeconds = float64(end - r.answer)
			r.durationKnown = true
		}
		if r.start < f.From.Unix() {
			previous.add(r)
			continue
		}
		total.add(r)
		i := sort.Search(len(buckets), func(i int) bool { return buckets[i].end > r.start })
		if i < len(buckets) {
			buckets[i].add(r)
		}
		for _, g := range []struct {
			m          map[string]*statisticsGroup
			key, label string
		}{{providers, r.Provider, r.Provider}, {advisers, r.owner, r.Adviser}, {numbers, r.number, r.number}} {
			if g.m[g.key] == nil {
				g.m[g.key] = &statisticsGroup{Key: g.key, Label: g.label}
			}
			g.m[g.key].add(r)
		}
		matches := r.start >= f.SegmentFrom && r.start < f.SegmentUntil
		switch f.Focus {
		case "problems":
			matches = matches && r.Problem
		case "unobserved":
			matches = matches && !r.Observed
		case "failed":
			matches = matches && r.Status == "failed"
		case "missed":
			matches = matches && r.Direction == "inbound" && r.answer == 0 && isTerminalStatus(r.Status)
		}
		if !matches {
			continue
		}
		matching++
		if f.Cursor != "" && (r.start > cursor.At || r.start == cursor.At && r.ID >= cursor.ID) {
			continue
		}
		if len(calls) < 50 {
			calls = append(calls, r)
		} else if next == "" {
			last := calls[len(calls)-1]
			b, _ := json.Marshal(statisticsCursor{At: last.start, ID: last.ID})
			next = base64.RawURLEncoding.EncodeToString(b)
		}
	}
	if err = rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	total.finish()
	previous.finish()
	for i := range buckets {
		buckets[i].finish()
	}
	// Bound dimension responses explicitly; period totals and call pagination remain complete.
	groups := map[string]any{}
	for k, v := range map[string]map[string]*statisticsGroup{"provider": providers, "adviser": advisers, "number": numbers} {
		all := statisticsGroups(v)
		n := len(all)
		if n > 50 {
			all = all[:50]
		}
		groups[k] = map[string]any{"items": all, "total_groups": n, "omitted_groups": max(0, n-50)}
	}
	return map[string]any{"totals": total, "previous": previous, "buckets": buckets, "breakdowns": groups, "calls": calls, "matching_calls": matching, "next_cursor": next, "from": f.From.UTC().Format(time.RFC3339), "until": f.Until.UTC().Format(time.RFC3339), "previous_from": f.From.Add(-f.Until.Sub(f.From)).UTC().Format(time.RFC3339), "timezone": f.Location.String(), "threshold_ms": f.Threshold, "generated_at": now.UTC().Format(time.RFC3339Nano), "basis": "calls_started_in_period_lifetime_counters", "pending_reports": total.Pending, "complete": true}, nil
}
func (a *App) serveCallStatistics(w http.ResponseWriter, r *http.Request, project string) {
	now := time.Now()
	f, err := parseCallStatisticsFilter(r, now)
	if err != nil {
		http.Error(w, err.Error(), 400)
		return
	}
	started := time.Now()
	v, err := a.db().callStatistics(r.Context(), project, f, now)
	if err != nil {
		http.Error(w, "statistics unavailable; retry shortly", 503)
		return
	}
	v["query_ms"] = float64(time.Since(started).Microseconds()) / 1000
	writeJSON(w, v)
}
