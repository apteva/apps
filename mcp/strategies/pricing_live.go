package main

import (
	"strings"

	"time"
)

const (
	cacheTTL           = 30 * time.Second
	healthRecentWindow = 60 * time.Second
	staleAfter         = 90 * time.Second
	polyDiscoveryTTL   = 30 * time.Second
	polyMissingTTL     = 15 * time.Minute
	polyMaxBackoff     = 15 * time.Minute
)

func completedYahooStrategyBars(bars []Bar, interval string, now time.Time) []Bar {
	now = now.UTC()
	out := make([]Bar, 0, len(bars))
	if interval == "1d" {
		location, err := time.LoadLocation("America/New_York")
		if err != nil {
			location = time.UTC
		}
		nowDate := now.In(location).Format("2006-01-02")
		for _, bar := range bars {
			if time.Unix(bar.T, 0).In(location).Format("2006-01-02") < nowDate {
				out = append(out, bar)
			}
		}
		return out
	}
	dur, ok := strategyCadenceDuration(interval)
	if !ok {
		dur = 24 * time.Hour
	}
	for _, bar := range bars {
		if !time.Unix(bar.T, 0).UTC().Add(dur).After(now) {
			out = append(out, bar)
		}
	}
	return out
}

func strategyClosedCandleBoundary(now time.Time, interval string) time.Time {
	now = now.UTC()
	if strings.EqualFold(strings.TrimSpace(interval), "1w") {
		day := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
		daysSinceMonday := (int(day.Weekday()) + 6) % 7
		return day.AddDate(0, 0, -daysSinceMonday)
	}
	dur, ok := strategyCadenceDuration(strings.ToLower(strings.TrimSpace(interval)))
	if !ok || dur <= 0 {
		dur = time.Hour
	}
	return now.Truncate(dur)
}
