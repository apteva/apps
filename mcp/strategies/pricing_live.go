package main

import (
	"strings"

	"time"
)

const (
	staleAfter = 90 * time.Second
)

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
