package main

import (
	"fmt"
	"strings"
	"time"
)

type MarketSession struct {
	Calendar    string `json:"calendar"`
	Timezone    string `json:"timezone"`
	EvaluatedAt string `json:"evaluated_at"`
	OpenDay     bool   `json:"open_day"`
	IsOpen      bool   `json:"is_open"`
	OpenAt      string `json:"open_at,omitempty"`
	CloseAt     string `json:"close_at,omitempty"`
	NextOpenAt  string `json:"next_open_at,omitempty"`
	Reason      string `json:"reason"`
}

func marketSessionAt(calendar string, at time.Time) (*MarketSession, error) {
	if at.IsZero() {
		at = time.Now().UTC()
	}
	at = at.UTC()
	switch strings.ToUpper(strings.TrimSpace(calendar)) {
	case calendarAlwaysOpen:
		open := time.Date(at.Year(), at.Month(), at.Day(), 0, 0, 0, 0, time.UTC)
		return &MarketSession{
			Calendar: calendarAlwaysOpen, Timezone: "UTC", EvaluatedAt: at.Format(time.RFC3339Nano),
			OpenDay: true, IsOpen: true, OpenAt: open.Format(time.RFC3339),
			CloseAt: open.Add(24 * time.Hour).Format(time.RFC3339), Reason: "continuous_session",
		}, nil
	case calendarUSEquity:
		session := usEquitySessionAt(at)
		out := &MarketSession{
			Calendar: calendarUSEquity, Timezone: "America/New_York",
			EvaluatedAt: at.Format(time.RFC3339Nano), OpenDay: session.OpenDay, Reason: session.Reason,
		}
		if session.OpenDay {
			out.OpenAt = session.Open.UTC().Format(time.RFC3339)
			out.CloseAt = session.Close.UTC().Format(time.RFC3339)
			out.IsOpen = !at.Before(session.Open) && at.Before(session.Close)
		}
		if !out.IsOpen {
			if next := nextUSEquityOpen(at); !next.IsZero() {
				out.NextOpenAt = next.UTC().Format(time.RFC3339)
			}
		}
		return out, nil
	default:
		return nil, fmt.Errorf("unsupported exchange calendar %q", calendar)
	}
}

type usEquitySession struct {
	Open    time.Time
	Close   time.Time
	Reason  string
	OpenDay bool
}

func usEquitySessionAt(now time.Time) usEquitySession {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		return usEquitySession{Reason: "exchange_timezone_unavailable"}
	}
	local := now.In(location)
	day := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location)
	if local.Weekday() == time.Saturday || local.Weekday() == time.Sunday {
		return usEquitySession{Reason: "weekend"}
	}
	if holiday, ok := usEquityHoliday(day); ok {
		return usEquitySession{Reason: "market_holiday: " + holiday}
	}
	closeHour := 16
	if usEquityEarlyClose(day) {
		closeHour = 13
	}
	return usEquitySession{
		Open:    time.Date(day.Year(), day.Month(), day.Day(), 9, 30, 0, 0, location),
		Close:   time.Date(day.Year(), day.Month(), day.Day(), closeHour, 0, 0, 0, location),
		Reason:  "regular_session",
		OpenDay: true,
	}
}

func nextUSEquityOpen(after time.Time) time.Time {
	location, err := time.LoadLocation("America/New_York")
	if err != nil {
		return time.Time{}
	}
	local := after.In(location)
	for offset := 0; offset < 370; offset++ {
		candidate := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, location).AddDate(0, 0, offset)
		session := usEquitySessionAt(candidate.Add(12 * time.Hour))
		if !session.OpenDay {
			continue
		}
		if after.Before(session.Open) {
			return session.Open.UTC()
		}
		if after.Before(session.Close) {
			return after.UTC()
		}
	}
	return time.Time{}
}

func usEquityHoliday(day time.Time) (string, bool) {
	location := day.Location()
	year := day.Year()
	type holiday struct {
		date time.Time
		name string
	}
	holidays := []holiday{
		{observedNewYearsDay(year, location), "New Year's Day"},
		{nthWeekday(year, time.January, time.Monday, 3, location), "Martin Luther King Jr. Day"},
		{nthWeekday(year, time.February, time.Monday, 3, location), "Washington's Birthday"},
		{easterSunday(year, location).AddDate(0, 0, -2), "Good Friday"},
		{lastWeekday(year, time.May, time.Monday, location), "Memorial Day"},
		{observedFixedHoliday(year, time.July, 4, location), "Independence Day"},
		{nthWeekday(year, time.September, time.Monday, 1, location), "Labor Day"},
		{nthWeekday(year, time.November, time.Thursday, 4, location), "Thanksgiving Day"},
		{observedFixedHoliday(year, time.December, 25, location), "Christmas Day"},
	}
	if year >= 2022 {
		holidays = append(holidays, holiday{observedFixedHoliday(year, time.June, 19, location), "Juneteenth National Independence Day"})
	}
	for _, holiday := range holidays {
		if sameLocalDate(day, holiday.date) {
			return holiday.name, true
		}
	}
	return "", false
}

func usEquityEarlyClose(day time.Time) bool {
	year := day.Year()
	location := day.Location()
	thanksgiving := nthWeekday(year, time.November, time.Thursday, 4, location)
	if sameLocalDate(day, thanksgiving.AddDate(0, 0, 1)) {
		return true
	}
	// NYSE schedules 13:00 closes on July 3 and December 24 when those
	// dates are otherwise trading days. It does not generically shorten the
	// business day before an observed holiday.
	if day.Month() == time.July && day.Day() == 3 {
		return true
	}
	if day.Month() == time.December && day.Day() == 24 {
		return true
	}
	return false
}

func observedNewYearsDay(year int, location *time.Location) time.Time {
	date := time.Date(year, time.January, 1, 0, 0, 0, 0, location)
	if date.Weekday() == time.Sunday {
		return date.AddDate(0, 0, 1)
	}
	if date.Weekday() == time.Saturday {
		return time.Time{}
	}
	return date
}

func observedFixedHoliday(year int, month time.Month, day int, location *time.Location) time.Time {
	date := time.Date(year, month, day, 0, 0, 0, 0, location)
	switch date.Weekday() {
	case time.Saturday:
		return date.AddDate(0, 0, -1)
	case time.Sunday:
		return date.AddDate(0, 0, 1)
	default:
		return date
	}
}

func nthWeekday(year int, month time.Month, weekday time.Weekday, n int, location *time.Location) time.Time {
	date := time.Date(year, month, 1, 0, 0, 0, 0, location)
	offset := (int(weekday) - int(date.Weekday()) + 7) % 7
	return date.AddDate(0, 0, offset+(n-1)*7)
}

func lastWeekday(year int, month time.Month, weekday time.Weekday, location *time.Location) time.Time {
	date := time.Date(year, month+1, 0, 0, 0, 0, 0, location)
	offset := (int(date.Weekday()) - int(weekday) + 7) % 7
	return date.AddDate(0, 0, -offset)
}

// Anonymous Gregorian algorithm, sufficient for NYSE Good Friday dates.
func easterSunday(year int, location *time.Location) time.Time {
	a := year % 19
	b := year / 100
	c := year % 100
	d := b / 4
	e := b % 4
	f := (b + 8) / 25
	g := (b - f + 1) / 3
	h := (19*a + b - d - g + 15) % 30
	i := c / 4
	k := c % 4
	l := (32 + 2*e + 2*i - h - k) % 7
	m := (a + 11*h + 22*l) / 451
	month := time.Month((h + l - 7*m + 114) / 31)
	day := (h+l-7*m+114)%31 + 1
	return time.Date(year, month, day, 0, 0, 0, 0, location)
}

func sameLocalDate(a, b time.Time) bool {
	aYear, aMonth, aDay := a.Date()
	bYear, bMonth, bDay := b.In(a.Location()).Date()
	return aYear == bYear && aMonth == bMonth && aDay == bDay
}
