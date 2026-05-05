package main

// Tier 1 — pure-function unit tests for the normalisation helpers.
// No DB, no SDK, no I/O. The whole file should run in <10ms.

import (
	"strings"
	"testing"
)

func TestNormaliseEmail(t *testing.T) {
	cases := map[string]string{
		"alice@example.com":     "alice@example.com",
		"ALICE@EXAMPLE.COM":     "alice@example.com",
		"  Alice@Example.com  ": "alice@example.com",
		"":                      "",
		"   ":                   "",
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
		{0, 0},
		{0.49, 0},
		{0.5, 1},
		{1.4, 1},
		{1.5, 2},
		{1499.5, 1500}, // round-half-up at the cent boundary
		{-0.5, -1},     // negative half rounds away from zero
		{-1.4, -1},
		{-1.5, -2},
	}
	for _, c := range cases {
		if got := roundCents(c.in); got != c.want {
			t.Errorf("roundCents(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestLooksLikeISO4217(t *testing.T) {
	good := []string{"USD", "EUR", "GBP", "JPY", "CAD", "XYZ"}
	for _, s := range good {
		if !looksLikeISO4217(s) {
			t.Errorf("expected %q to be valid", s)
		}
	}
	bad := []string{"", "us", "USDS", "us d", "U5D", "usd", "Us-D"}
	for _, s := range bad {
		if looksLikeISO4217(s) {
			t.Errorf("expected %q to be invalid", s)
		}
	}
}

func TestRenderSeqToken(t *testing.T) {
	cases := []struct {
		format string
		seq    int64
		want   string
	}{
		{"INV-{seq}", 1, "INV-1"},
		{"INV-{seq}", 42, "INV-42"},
		{"INV-{seq:04}", 1, "INV-0001"},
		{"INV-{seq:04}", 42, "INV-0042"},
		{"INV-{seq:04}", 12345, "INV-12345"}, // overflow widens, not truncates
		{"INV-{seq:2}", 7, "INV-07"},
		{"{seq:6}-suffix", 9, "000009-suffix"},
		{"no-token-here", 5, "no-token-here"},
		{"{seq}-{seq}", 3, "3-3"}, // every token resolves
	}
	for _, c := range cases {
		got := renderSeqToken(c.format, c.seq)
		if got != c.want {
			t.Errorf("renderSeqToken(%q, %d) = %q, want %q", c.format, c.seq, got, c.want)
		}
	}
}

func TestComputeTotals_NoTax(t *testing.T) {
	items := []LineItem{
		{AmountCents: 1000, TaxRateBps: 0},
		{AmountCents: 2500, TaxRateBps: 0},
	}
	sub, tax, total := computeTotals(items)
	if sub != 3500 || tax != 0 || total != 3500 {
		t.Errorf("got sub=%d tax=%d total=%d, want 3500/0/3500", sub, tax, total)
	}
}

func TestComputeTotals_WithTax(t *testing.T) {
	// Two lines, one taxed at 20% (2000bps), one untaxed.
	items := []LineItem{
		{AmountCents: 1000, TaxRateBps: 2000}, // 200 tax
		{AmountCents: 500, TaxRateBps: 0},     // no tax
	}
	sub, tax, total := computeTotals(items)
	if sub != 1500 || tax != 200 || total != 1700 {
		t.Errorf("got sub=%d tax=%d total=%d, want 1500/200/1700", sub, tax, total)
	}
}

func TestComputeTotals_TaxRoundsDownPerLine(t *testing.T) {
	// 7.25% tax on $13.37 = 96.93 cents. Per-line integer division
	// truncates to 96, not 97. This is the "rounds down per line"
	// invariant the panel relies on for displayed-total consistency.
	items := []LineItem{
		{AmountCents: 1337, TaxRateBps: 725},
	}
	_, tax, _ := computeTotals(items)
	if tax != 96 {
		t.Errorf("got tax=%d, want 96 (1337*725/10000 truncated)", tax)
	}
}

func TestNormaliseLineItems_HappyPath(t *testing.T) {
	raw := []any{
		map[string]any{
			"description":      "Consulting",
			"quantity":         10.0,
			"unit_price_cents": 15000.0,
			"tax_rate_bps":     2000.0,
		},
		map[string]any{
			"description":      "Travel",
			"unit_price_cents": 8500.0, // quantity defaults to 1
		},
	}
	items, err := normaliseLineItems(raw, 0)
	if err != nil {
		t.Fatalf("normaliseLineItems: %v", err)
	}
	if len(items) != 2 {
		t.Fatalf("got %d items, want 2", len(items))
	}
	if items[0].AmountCents != 150000 {
		t.Errorf("items[0].AmountCents = %d, want 150000", items[0].AmountCents)
	}
	if items[0].TaxRateBps != 2000 {
		t.Errorf("items[0].TaxRateBps = %d, want 2000", items[0].TaxRateBps)
	}
	if items[1].Quantity != 1 {
		t.Errorf("items[1].Quantity = %v, want 1 (default)", items[1].Quantity)
	}
	if items[1].AmountCents != 8500 {
		t.Errorf("items[1].AmountCents = %d, want 8500", items[1].AmountCents)
	}
	if items[0].Position != 0 || items[1].Position != 1 {
		t.Errorf("positions = %d, %d, want 0, 1", items[0].Position, items[1].Position)
	}
}

func TestNormaliseLineItems_AppliesDefaultBps(t *testing.T) {
	raw := []any{
		map[string]any{
			"description":      "Service",
			"quantity":         1.0,
			"unit_price_cents": 1000.0,
			// tax_rate_bps omitted — should pick up the install default.
		},
	}
	items, err := normaliseLineItems(raw, 1500)
	if err != nil {
		t.Fatal(err)
	}
	if items[0].TaxRateBps != 1500 {
		t.Errorf("got bps=%d, want 1500 (install default)", items[0].TaxRateBps)
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
			"negative quantity",
			[]any{map[string]any{"description": "x", "quantity": -1.0, "unit_price_cents": 100.0}},
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
		{
			"not an object",
			[]any{"not-a-map"},
			"is not an object",
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
