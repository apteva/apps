package main

import (
	"encoding/json"
	"time"
)

// Freshness is separate from values and inventory coverage. A zero can be
// ready; an absent observation cannot establish either a zero or readiness.
type analyticsFreshness struct {
	ObservedAt       string   `json:"observed_at"`
	ProcessedThrough string   `json:"processed_through,omitempty"`
	PendingDates     []string `json:"pending_dates"`
	EstimatedDates   []string `json:"estimated_dates"`
	UnavailableDates []string `json:"unavailable_dates"`
}

func mergeMetricStatus(a, b string) string {
	rank := map[string]int{"": 0, "ready": 0, "estimated": 1, "unavailable": 2, "unknown": 2, "pending": 3}
	if rank[b] > rank[a] {
		return b
	}
	if a == "" {
		return "ready"
	}
	return a
}
func (p insightPoint) MarshalJSON() ([]byte, error) {
	type plain insightPoint
	if p.DataStatus != "pending" && p.DataStatus != "unavailable" {
		return json.Marshal(plain(p))
	}
	return json.Marshal(struct {
		plain
		Value any `json:"value"`
	}{plain(p), nil})
}

// Preserve numeric adapter arithmetic while exposing unreported values as null.
func marshalAvailableMetrics(value any, available []string, info map[string]analyticsMetricInfo, names []string) ([]byte, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return nil, err
	}
	var fields map[string]json.RawMessage
	if err = json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}
	known := map[string]bool{}
	for _, name := range available {
		known[name] = true
	}
	if known["outbound_clicks"] {
		known["clicks"] = true
	}
	for name, m := range info {
		if m.AliasOf != "" && known[m.AliasOf] {
			known[name] = true
		}
	}
	for _, name := range names {
		if !known[name] {
			fields[name] = json.RawMessage(`null`)
		} else if _, ok := fields[name]; !ok {
			fields[name] = json.RawMessage(`0`)
		}
	}
	return json.Marshal(fields)
}
func (m normalizedMetrics) MarshalJSON() ([]byte, error) {
	type plain normalizedMetrics
	if m.Available == nil {
		return json.Marshal(plain(m))
	}
	return marshalAvailableMetrics(plain(m), m.Available, nil, []string{"impressions", "video_views", "pin_clicks", "outbound_clicks", "engagements", "views", "reach", "likes", "comments", "shares", "saves", "clicks"})
}
func (m accountMetricsResult) MarshalJSON() ([]byte, error) {
	type plain accountMetricsResult
	if m.Report == nil {
		return json.Marshal(plain(m))
	}
	return marshalAvailableMetrics(plain(m), m.Available, m.Report.MetricInfo, reportMetricNames)
}
func seriesFreshness(series insightSeries, names []string, q analyticsQuery) *analyticsFreshness {
	start, end := q.dates()
	s, _ := time.Parse("2006-01-02", start)
	e, _ := time.Parse("2006-01-02", end)
	f := &analyticsFreshness{ObservedAt: time.Now().UTC().Format(time.RFC3339), PendingDates: []string{}, EstimatedDates: []string{}, UnavailableDates: []string{}}
	days := map[string]map[string]string{}
	estimated := map[string]bool{}
	for _, name := range names {
		days[name] = map[string]string{}
		for _, p := range series[name] {
			status := p.DataStatus
			if status == "" {
				if p.Partial {
					status = "unavailable"
				} else {
					status = "ready"
				}
			}
			days[name][p.Time] = status
			if p.Estimated || status == "estimated" {
				estimated[p.Time] = true
			}
		}
	}
	contiguous := true
	for day := s; !day.After(e); day = day.AddDate(0, 0, 1) {
		date := day.Format("2006-01-02")
		status := "ready"
		pending, missing := false, false
		for _, name := range names {
			state, ok := days[name][date]
			if !ok {
				state = "unavailable"
			}
			pending = pending || state == "pending"
			missing = missing || state == "unavailable" || state == "unknown"
			status = mergeMetricStatus(status, state)
		}
		if pending {
			f.PendingDates = append(f.PendingDates, date)
		}
		if missing {
			f.UnavailableDates = append(f.UnavailableDates, date)
		}
		if estimated[date] {
			f.EstimatedDates = append(f.EstimatedDates, date)
		}
		if status != "ready" {
			contiguous = false
		}
		if contiguous {
			f.ProcessedThrough = date
		}
	}
	return f
}
