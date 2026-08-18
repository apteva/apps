package main

import (
	"errors"
	"strings"
	"time"
)

// Timestamp discipline.
//
// Every timestamp this app writes is RFC3339 in UTC ("2026-08-18T09:06:00Z").
// That matters for three reasons:
//
//   - time.Parse(time.RFC3339, …) is what the readers use. SQLite's
//     CURRENT_TIMESTAMP renders "2026-08-18 09:06:00", which that parse
//     rejects — the offer broadcaster silently skipped every live
//     webinar because started_at was written that way.
//   - Several hot paths compare timestamps *lexically* in SQL (slot
//     elapse sweeps, reminder due scans, retention pruning). Lexical
//     compare is only a total order when every value shares one layout
//     and one zone, hence "UTC, always, with the Z suffix".
//   - Mixed layouts break ORDER BY across rows written by different
//     versions.
//
// Reads stay tolerant: parseDBTime accepts the legacy SQLite layout so
// rows written before migrations/003_hardening.sql normalized them keep
// working even if the migration is somehow skipped.

// dbTimeLayouts are tried in order by parseDBTime. RFC3339 first because
// that's what everything written from v0.2 on looks like.
var dbTimeLayouts = []string{
	time.RFC3339Nano,
	time.RFC3339,
	"2006-01-02T15:04:05",
	"2006-01-02 15:04:05.999999999-07:00",
	"2006-01-02 15:04:05.999999999",
	"2006-01-02 15:04:05",
	"2006-01-02",
}

var errEmptyTime = errors.New("empty timestamp")

// nowUTC is the single clock reference for comparisons.
func nowUTC() time.Time { return time.Now().UTC() }

// nowRFC3339 is the single source of "now" for anything written to the
// DB. Use it instead of SQL CURRENT_TIMESTAMP.
func nowRFC3339() string { return nowUTC().Format(time.RFC3339) }

// formatRFC3339 renders t in the storage layout.
func formatRFC3339(t time.Time) string { return t.UTC().Format(time.RFC3339) }

// parseDBTime parses a timestamp column tolerantly — RFC3339 first, then
// the SQLite CURRENT_TIMESTAMP layout legacy rows may still carry. A
// value with no zone is read as UTC, which is what SQLite's
// CURRENT_TIMESTAMP always produced.
func parseDBTime(s string) (time.Time, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, errEmptyTime
	}
	for _, layout := range dbTimeLayouts {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), nil
		}
	}
	// Zone-less layouts parse in UTC via time.Parse already; the explicit
	// ParseInLocation pass covers layouts time.Parse would treat as local.
	for _, layout := range dbTimeLayouts {
		if t, err := time.ParseInLocation(layout, s, time.UTC); err == nil {
			return t.UTC(), nil
		}
	}
	return time.Time{}, errors.New("unrecognized timestamp layout: " + s)
}

// normalizeRFC3339 re-renders any accepted timestamp layout as UTC
// RFC3339. Used at every write boundary that takes a caller-supplied
// time (webinars_create, webinars_update, webinars_create_slot) so
// stored values are directly lexically comparable.
func normalizeRFC3339(s string) (string, error) {
	t, err := parseDBTime(s)
	if err != nil {
		return "", err
	}
	return formatRFC3339(t), nil
}
