package main

// Tier 1 — pure-function unit tests. Pure normalisation logic is
// duplicated from billing per the proposal's three-line rule, so
// these tests are duplicated too. Keep them in sync.

import (
	"strings"
	"testing"
)

func TestNormaliseEmail(t *testing.T) {
	cases := map[string]string{
		"ap@acme.com":            "ap@acme.com",
		"AP@ACME.COM":            "ap@acme.com",
		"  AP@Acme.com  ":        "ap@acme.com",
		"":                       "",
	}
	for in, want := range cases {
		if got := normaliseEmail(in); got != want {
			t.Errorf("normaliseEmail(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRoundCents(t *testing.T) {
	cases := []struct {
		in   float64
		want int64
	}{
		{0, 0}, {0.5, 1}, {1.4, 1}, {1.5, 2},
		{-0.5, -1}, {-1.5, -2},
	}
	for _, c := range cases {
		if got := roundCents(c.in); got != c.want {
			t.Errorf("roundCents(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestLooksLikeISO4217(t *testing.T) {
	for _, s := range []string{"USD", "EUR", "GBP", "JPY", "CAD"} {
		if !looksLikeISO4217(s) {
			t.Errorf("expected %q valid", s)
		}
	}
	for _, s := range []string{"", "us", "USDS", "us d", "U5D", "usd"} {
		if looksLikeISO4217(s) {
			t.Errorf("expected %q invalid", s)
		}
	}
}

func TestComputeTotals(t *testing.T) {
	items := []BillLineItem{
		{AmountCents: 1000, TaxRateBps: 2000}, // 200 input tax
		{AmountCents: 500, TaxRateBps: 0},
	}
	sub, tax, total := computeTotals(items)
	if sub != 1500 || tax != 200 || total != 1700 {
		t.Errorf("got sub=%d tax=%d total=%d, want 1500/200/1700", sub, tax, total)
	}
}

func TestComputeTotals_PerLineRoundsDown(t *testing.T) {
	// 7.25% of $13.37 = 96.93c → truncated to 96.
	items := []BillLineItem{{AmountCents: 1337, TaxRateBps: 725}}
	_, tax, _ := computeTotals(items)
	if tax != 96 {
		t.Errorf("got tax=%d, want 96", tax)
	}
}

func TestNormaliseLineItems_HappyPath(t *testing.T) {
	raw := []any{
		map[string]any{
			"description":      "AWS",
			"quantity":         1.0,
			"unit_price_cents": 5000.0,
			"tax_rate_bps":     0.0,
		},
	}
	items, err := normaliseLineItems(raw, 0)
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if len(items) != 1 || items[0].AmountCents != 5000 {
		t.Errorf("got %#v", items)
	}
}

func TestNormaliseLineItems_RejectsBad(t *testing.T) {
	cases := []struct {
		name  string
		raw   []any
		match string
	}{
		{
			"missing description",
			[]any{map[string]any{"quantity": 1.0, "unit_price_cents": 100.0}},
			"description required",
		},
		{
			"zero quantity",
			[]any{map[string]any{"description": "x", "quantity": 0.0, "unit_price_cents": 100.0}},
			"quantity must be > 0",
		},
		{
			"tax bps out of range",
			[]any{map[string]any{
				"description": "x", "quantity": 1.0, "unit_price_cents": 100.0,
				"tax_rate_bps": 200000.0,
			}},
			"tax_rate_bps out of range",
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			_, err := normaliseLineItems(c.raw, 0)
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), c.match) {
				t.Errorf("error %q should mention %q", err.Error(), c.match)
			}
		})
	}
}

func TestValidScheduleMethod(t *testing.T) {
	for _, m := range []string{"wire", "check", "ach", "card", "other"} {
		if !validScheduleMethod(m) {
			t.Errorf("expected %q valid", m)
		}
	}
	for _, m := range []string{"stripe", "external_rail", "cash", "venmo", ""} {
		if validScheduleMethod(m) {
			t.Errorf("expected %q invalid (cash is valid for record but not schedule)", m)
		}
	}
}
