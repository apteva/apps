package main

import (
	"math"
	"testing"
)

func almost(a, b, tol float64) bool { return math.Abs(a-b) <= tol }

func TestAmericanToImplied(t *testing.T) {
	cases := []struct {
		odds float64
		want float64
	}{
		{+100, 0.5},
		{+150, 0.40},
		{-200, 0.6667},
		{-118, 0.5413},
		{+280, 0.2632},
	}
	for _, c := range cases {
		got := americanToImplied(c.odds)
		if !almost(got, c.want, 0.001) {
			t.Errorf("americanToImplied(%v) = %.4f, want %.4f", c.odds, got, c.want)
		}
	}
}

func TestDecimalToImplied(t *testing.T) {
	if got := decimalToImplied(2.50); !almost(got, 0.40, 0.001) {
		t.Errorf("decimal 2.50 → %.4f, want 0.40", got)
	}
	if got := decimalToImplied(1.50); !almost(got, 0.6667, 0.001) {
		t.Errorf("decimal 1.50 → %.4f, want 0.6667", got)
	}
	if got := decimalToImplied(1.0); got != 0 {
		t.Errorf("decimal 1.0 should yield 0 (no payout), got %.4f", got)
	}
}

func TestDevigRemovesOverround(t *testing.T) {
	// Two-way market priced -118 / -104.
	raw := []float64{americanToImplied(-118), americanToImplied(-104)}
	fair, ov := devig(raw)
	// Fair probs must sum to 1.
	if !almost(fair[0]+fair[1], 1.0, 1e-9) {
		t.Errorf("fair probs don't sum to 1: %v", fair)
	}
	// Overround should be positive (~5%).
	if ov <= 0 || ov > 0.10 {
		t.Errorf("overround out of expected range: %.4f", ov)
	}
	// The favorite (-118) should have higher fair prob than the dog.
	if fair[0] <= fair[1] {
		t.Errorf("favorite should have higher fair prob: %v", fair)
	}
}

func TestDevigTwoWayAmerican(t *testing.T) {
	// Even-ish market: -110 / -110 → both fair ≈ 0.50 after de-vig.
	fairA, ov := devigTwoWayAmerican(-110, -110)
	if !almost(fairA, 0.50, 0.001) {
		t.Errorf("symmetric -110/-110 should de-vig to 0.50, got %.4f", fairA)
	}
	if ov <= 0 {
		t.Errorf("expected positive overround, got %.4f", ov)
	}
}

func TestConsensusFairProbMedian(t *testing.T) {
	// Three books, two-way decimal odds, target = outcome 0 (the favorite).
	// Book A: 1.85 / 2.05 ; Book B: 1.83 / 2.08 ; Book C (stale): 1.70 / 2.30
	books := [][]float64{
		{1.85, 2.05},
		{1.83, 2.08},
		{1.70, 2.30},
	}
	median, n := consensusFairProb(books, 0)
	if n != 3 {
		t.Fatalf("expected 3 books contributing, got %d", n)
	}
	// Each book's fair-prob for outcome 0 is roughly 0.52-0.57; median
	// should land in that band and NOT be dragged to the stale book's
	// extreme.
	if median < 0.50 || median > 0.60 {
		t.Errorf("consensus median out of band: %.4f", median)
	}
}

func TestEdgeBps(t *testing.T) {
	// PM YES 0.55 vs fair 0.48 → +700 bps (overpriced).
	if got := edgeBps(0.55, 0.48); got != 700 {
		t.Errorf("edgeBps(0.55, 0.48) = %v, want 700", got)
	}
	// Underpriced case.
	if got := edgeBps(0.40, 0.48); got != -800 {
		t.Errorf("edgeBps(0.40, 0.48) = %v, want -800", got)
	}
}
