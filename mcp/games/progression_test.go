package main

import (
	"strings"
	"testing"
	"time"
)

func TestAggregate(t *testing.T) {
	cases := []struct {
		agg      string
		existing bool
		old, v   float64
		want     float64
	}{
		{"last", true, 5, 3, 3}, {"last", false, 0, 3, 3},
		{"max", true, 5, 3, 5}, {"max", true, 5, 9, 9}, {"max", false, 0, -2, -2},
		{"min", true, 5, 3, 3}, {"min", true, 5, 9, 5}, {"min", false, 0, 7, 7},
		{"sum", true, 5, 3, 8}, {"sum", false, 0, 3, 3}, {"sum", true, 5, -2, 3},
	}
	for _, c := range cases {
		if got := aggregate(c.agg, c.existing, c.old, c.v); got != c.want {
			t.Errorf("aggregate(%s, %v, %v, %v) = %v, want %v", c.agg, c.existing, c.old, c.v, got, c.want)
		}
	}
}

func TestPeriodKey(t *testing.T) {
	now := time.Date(2026, 9, 3, 10, 0, 0, 0, time.UTC)
	if got := periodKey(&Leaderboard{Reset: "none"}, now); got != "all" {
		t.Errorf("none = %q", got)
	}
	if got := periodKey(&Leaderboard{Reset: "daily"}, now); got != "2026-09-03" {
		t.Errorf("daily = %q", got)
	}
	if got := periodKey(&Leaderboard{Reset: "weekly"}, now); got != "2026-W36" {
		t.Errorf("weekly = %q", got)
	}
	if got := periodKey(&Leaderboard{Reset: "monthly"}, now); got != "2026-09" {
		t.Errorf("monthly = %q", got)
	}
	started := now.Add(-31 * 24 * time.Hour).Format(time.RFC3339)
	lb := &Leaderboard{Reset: "season", SeasonDays: 30, CurrentPeriod: "season-2", PeriodStartedAt: started}
	if got := periodKey(lb, now); got != "season-3" {
		t.Errorf("season after 31 days = %q, want season-3", got)
	}
	lb.PeriodStartedAt = now.Add(-5 * 24 * time.Hour).Format(time.RFC3339)
	if got := periodKey(lb, now); got != "season-2" {
		t.Errorf("season within window = %q, want season-2", got)
	}
	lb.CurrentPeriod = "season-2-rabc"
	if got := periodKey(lb, now); got != "season-2" {
		t.Errorf("manual season period = %q, want season-2", got)
	}
	if got := periodKey(&Leaderboard{Reset: "season"}, now); got != "season-1" {
		t.Errorf("fresh season = %q", got)
	}
}

func TestAchievementMet(t *testing.T) {
	d := AchievementDef{Threshold: 10}
	for _, c := range []struct {
		op   string
		v    float64
		want bool
	}{
		{"gte", 10, true}, {"gte", 9, false}, {"gt", 10, false}, {"gt", 11, true},
		{"lte", 10, true}, {"lt", 10, false}, {"eq", 10, true}, {"eq", 11, false}, {"", 12, true},
	} {
		d.Op = c.op
		if got := achievementMet(d, c.v); got != c.want {
			t.Errorf("op %q value %v = %v, want %v", c.op, c.v, got, c.want)
		}
	}
}

func TestNewLeaderboardValidation(t *testing.T) {
	now := time.Now()
	if _, err := newLeaderboard("Bad Name", "", "score", "", "", 0, now); err == nil {
		t.Error("space in name should fail")
	}
	if _, err := newLeaderboard("ok", "", "score", "sideways", "", 0, now); err == nil {
		t.Error("bad sort should fail")
	}
	if _, err := newLeaderboard("ok", "", "score", "", "yearly", 0, now); err == nil {
		t.Error("bad reset should fail")
	}
	lb, err := newLeaderboard("Season-Board", "Season", "score", "", "season", 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if lb.Name != "season-board" || lb.SeasonDays != 30 || lb.CurrentPeriod != "season-1" || lb.Sort != "desc" {
		t.Errorf("season defaults = %+v", lb)
	}
	lb, _ = newLeaderboard("daily", "", "score", "asc", "daily", 99, now)
	if lb.SeasonDays != 0 || !strings.HasPrefix(lb.CurrentPeriod, now.UTC().Format("2006-")) {
		t.Errorf("daily = %+v", lb)
	}
}

func TestIdentitySubjectIsStableAndHashed(t *testing.T) {
	a, b := identitySubject("device-abc"), identitySubject("  device-abc ")
	if a != b || len(a) != 64 || a == "device-abc" {
		t.Errorf("subject = %q / %q", a, b)
	}
}
