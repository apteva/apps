package main

import (
	"encoding/json"
	"math"
	"testing"
)

func TestStrategyInverseVolatilitySizing(t *testing.T) {
	market := strategyMarket{history: map[string][]float64{"A": {100, 100 * math.Exp(.01), 100}, "B": {100, 100 * math.Exp(.02), 100}, "FLAT": {100, 100, 100}}, prices: map[string]float64{"A": 100, "B": 100, "FLAT": 100}}
	rank := StrategyRank{Symbols: []string{"A", "B"}, By: "price", Top: 2, Budget: .6, Weight: "inverse_volatility", VolatilityPeriod: 2, VolatilityFloor: .005}
	got, _, err := evalStrategyRank(rank, market)
	if err != nil {
		t.Fatal(err)
	}
	if math.Abs(got[0].Weight-.4) > 1e-12 || math.Abs(got[1].Weight-.2) > 1e-12 {
		t.Fatalf("2:1 inverse risk weights: %+v", got)
	}
	rank.Symbols = []string{"FLAT", "A"}
	got, _, err = evalStrategyRank(rank, market)
	if err != nil || math.Abs(got[0].Weight-.4) > 1e-12 || math.Abs(got[1].Weight-.2) > 1e-12 {
		t.Fatalf("flat series uses floor: %+v %v", got, err)
	}
	rank.Symbols = []string{"A", "B"}
	def := StrategyDefinition{Universe: rank.Symbols, Cadence: "1d", Risk: StrategyRisk{MaxPositionWeight: .3}, Rules: []StrategyRule{{Rank: &rank}}}
	data, _ := json.Marshal(def)
	var raw map[string]any
	json.Unmarshal(data, &raw)
	evaluation, err := evaluateStrategy(&Strategy{Definition: raw}, market)
	if err != nil {
		t.Fatal(err)
	}
	if len(evaluation.TargetAllocations) != 2 || math.Abs(evaluation.TargetAllocations[0].Weight-.3) > 1e-12 || math.Abs(evaluation.TargetAllocations[1].Weight-.2) > 1e-12 {
		t.Fatalf("caps preserve cash: %+v", evaluation)
	}
	rank.VolatilityPeriod = 40
	if got := strategyRequiredBars(&def); got != 41 {
		t.Fatalf("sizing warmup = %d", got)
	}
	if _, _, err = evalStrategyRank(rank, market); err == nil {
		t.Fatal("missing volatility history accepted")
	}
}

func TestStrategyInverseVolatilityValidation(t *testing.T) {
	for _, tc := range []struct {
		period int
		floor  float64
		weight string
		valid  bool
	}{
		{20, .005, "inverse_volatility", true}, {1, .005, "inverse_volatility", false}, {1000, .005, "inverse_volatility", false}, {20, 0, "inverse_volatility", false}, {20, 1e-300, "inverse_volatility", false}, {20, 1.1, "inverse_volatility", false}, {20, .005, "equal_weight", false}, {0, 0, "equal_weight", true},
	} {
		def := map[string]any{"universe": []string{"A"}, "cadence": "1d", "rules": []any{map[string]any{"rank": map[string]any{"symbols": []string{"A"}, "by": "price", "top": 1, "weight": tc.weight, "volatility_period": tc.period, "volatility_floor": tc.floor}}}}
		_, _, err := validateStrategyDefinition(def)
		if (err == nil) != tc.valid {
			t.Fatalf("%+v: %v", tc, err)
		}
	}
}
