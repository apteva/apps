package main

// Sportsbook de-vigging — converting bookmaker odds into a fair
// probability you can compare against a prediction-market price.
//
// Books price every outcome with a margin baked in ("the vig" / "the
// juice"). The raw implied probabilities of all outcomes sum to >100%;
// the excess is the book's edge. To get a fair probability you remove
// it. We use the multiplicative (proportional) method: divide each
// outcome's raw implied prob by the overround. It's the standard, it's
// unbiased toward favorites/longshots at the level of accuracy we need,
// and it's what most quant-betting references default to.
//
// This is the reusable value market-intel centralizes — every trading
// agent would otherwise reimplement it and get the overround handling
// subtly wrong.

import "math"

// americanToImplied converts American odds to a raw implied probability.
//   +150 → 100/(150+100) = 0.40
//   -200 → 200/(200+100) = 0.667
func americanToImplied(odds float64) float64 {
	if odds == 0 {
		return 0
	}
	if odds > 0 {
		return 100.0 / (odds + 100.0)
	}
	return -odds / (-odds + 100.0)
}

// decimalToImplied converts decimal odds to a raw implied probability.
//   2.50 → 1/2.50 = 0.40
//   1.50 → 1/1.50 = 0.667
func decimalToImplied(odds float64) float64 {
	if odds <= 1.0 {
		return 0
	}
	return 1.0 / odds
}

// devig takes the raw implied probabilities for every outcome of an
// event and returns the fair (vig-free) probabilities, proportionally
// normalized so they sum to 1. The overround (book margin) is
// returned alongside for transparency.
//
// Example: a two-way market priced -118 / -104:
//   raw = [0.541, 0.510]  sum = 1.051  (5.1% overround)
//   fair = [0.541/1.051, 0.510/1.051] = [0.515, 0.485]
func devig(rawImplied []float64) (fair []float64, overround float64) {
	sum := 0.0
	for _, p := range rawImplied {
		sum += p
	}
	if sum <= 0 {
		return rawImplied, 0
	}
	fair = make([]float64, len(rawImplied))
	for i, p := range rawImplied {
		fair[i] = p / sum
	}
	return fair, sum - 1.0
}

// devigTwoWayAmerican is the common case: a head-to-head market with
// two outcomes priced in American odds. Returns the fair probability of
// the FIRST outcome (the one the agent cares about) + the overround.
func devigTwoWayAmerican(oddsA, oddsB float64) (fairA, overround float64) {
	raw := []float64{americanToImplied(oddsA), americanToImplied(oddsB)}
	fair, ov := devig(raw)
	if len(fair) == 0 {
		return 0, 0
	}
	return fair[0], ov
}

// consensusFairProb computes the de-vigged fair probability of a target
// outcome across MANY bookmakers, then takes the median (robust to one
// book posting a stale/wrong line). Each book contributes its own
// two-way (or n-way) de-vig; we median the per-book fair probs.
//
// bookOutcomes[i] is one book's slice of (decimal) odds for every
// outcome; targetIdx is which outcome we want the fair prob of.
func consensusFairProb(bookOutcomes [][]float64, targetIdx int) (median float64, books int) {
	fairs := make([]float64, 0, len(bookOutcomes))
	for _, outcomes := range bookOutcomes {
		if targetIdx >= len(outcomes) {
			continue
		}
		raw := make([]float64, len(outcomes))
		for i, dec := range outcomes {
			raw[i] = decimalToImplied(dec)
		}
		fair, _ := devig(raw)
		if targetIdx < len(fair) && fair[targetIdx] > 0 {
			fairs = append(fairs, fair[targetIdx])
		}
	}
	if len(fairs) == 0 {
		return 0, 0
	}
	return medianOf(fairs), len(fairs)
}

func medianOf(xs []float64) float64 {
	// Simple insertion sort — book counts are small (<100).
	cp := append([]float64(nil), xs...)
	for i := 1; i < len(cp); i++ {
		for j := i; j > 0 && cp[j-1] > cp[j]; j-- {
			cp[j-1], cp[j] = cp[j], cp[j-1]
		}
	}
	n := len(cp)
	if n%2 == 1 {
		return cp[n/2]
	}
	return (cp[n/2-1] + cp[n/2]) / 2
}

// edgeBps computes the edge in basis points between a prediction-market
// YES price and a fair probability. Positive = the YES is OVERpriced
// vs fair (a SELL_YES signal); negative = underpriced (BUY_YES).
//   pmYes=0.55, fair=0.48 → +700 bps (overpriced → sell)
func edgeBps(pmYes, fair float64) float64 {
	return math.Round((pmYes - fair) * 10000)
}
