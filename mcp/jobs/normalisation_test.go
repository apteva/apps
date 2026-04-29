package main

import (
	"testing"
	"time"
)

// Cron parsing: smallest set of expressions a v0.1 user actually
// types. We don't aim for full vixie compatibility — just enough to
// cover daily/weekly/N-minute schedules.

func TestParseCron_StarFields(t *testing.T) {
	c, err := parseCron("* * * * *")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	from := time.Date(2026, 4, 29, 9, 0, 30, 0, time.UTC)
	got := c.next(from)
	want := time.Date(2026, 4, 29, 9, 1, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("next * * * * * after %v = %v, want %v", from, got, want)
	}
}

func TestParseCron_DailyAt9(t *testing.T) {
	c, err := parseCron("0 9 * * *")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	from := time.Date(2026, 4, 29, 9, 30, 0, 0, time.UTC)
	got := c.next(from)
	want := time.Date(2026, 4, 30, 9, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("daily-at-9 next after %v = %v, want %v", from, got, want)
	}
}

func TestParseCron_StepEveryFiveMinutes(t *testing.T) {
	c, err := parseCron("*/5 * * * *")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	from := time.Date(2026, 4, 29, 9, 12, 0, 0, time.UTC)
	got := c.next(from)
	want := time.Date(2026, 4, 29, 9, 15, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("*/5 next after %v = %v, want %v", from, got, want)
	}
}

func TestParseCron_DOWMonday(t *testing.T) {
	// 2026-04-29 is a Wednesday; next Monday at 9:00 is 2026-05-04.
	c, err := parseCron("0 9 * * 1")
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	from := time.Date(2026, 4, 29, 12, 0, 0, 0, time.UTC)
	got := c.next(from)
	want := time.Date(2026, 5, 4, 9, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Errorf("monday-9 next = %v, want %v", got, want)
	}
}

func TestParseCron_Invalid(t *testing.T) {
	cases := []string{
		"",                // no fields
		"* * *",           // wrong arity
		"60 * * * *",      // minute out of range
		"* 24 * * *",      // hour out of range
		"* * 0 * *",       // dom out of range
		"foo bar baz q r", // not numbers
	}
	for _, expr := range cases {
		if _, err := parseCron(expr); err == nil {
			t.Errorf("parseCron(%q) should have failed", expr)
		}
	}
}

// computeNextRun for "every" intervals is straightforward addition;
// pin it down so a regression doesn't quietly drift.
func TestComputeNextRun_Every(t *testing.T) {
	now := time.Date(2026, 4, 29, 9, 0, 0, 0, time.UTC)
	secs := int64(300) // 5 minutes
	got := computeNextRun("every", time.Time{}, &secs, "", time.UTC, now)
	want := now.Add(5 * time.Minute)
	if !got.Equal(want) {
		t.Errorf("got %v, want %v", got, want)
	}
}

func TestComputeNextRun_Once(t *testing.T) {
	runAt := time.Date(2026, 5, 1, 12, 0, 0, 0, time.UTC)
	got := computeNextRun("once", runAt, nil, "", time.UTC, time.Now())
	if !got.Equal(runAt) {
		t.Errorf("once should return run_at unchanged; got %v", got)
	}
}

// validateTarget enforces the kind enum + per-kind required fields.
func TestValidateTarget(t *testing.T) {
	good := []map[string]any{
		{"kind": "http", "url": "https://example.com/x"},
		{"kind": "http", "app": "crm", "path": "/cron/x"},
		{"kind": "event", "instance_id": float64(7), "message": "hi"},
	}
	for _, g := range good {
		if err := validateTarget(g); err != nil {
			t.Errorf("validateTarget(%v) = %v, want ok", g, err)
		}
	}
	bad := []map[string]any{
		{"kind": "http"},              // no url, no app/path
		{"kind": "http", "app": "x"},  // app without path
		{"kind": "event"},             // no instance / message
		{"kind": "event", "instance_id": float64(7)},
		{"kind": "smtp", "url": "x"},  // unknown kind
	}
	for _, b := range bad {
		if err := validateTarget(b); err == nil {
			t.Errorf("validateTarget(%v) should have errored", b)
		}
	}
}
