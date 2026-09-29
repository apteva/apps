package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"math"
)

// ScoringVersion names the frozen scoring contract. Scores are only ever
// comparable within one scoring version — the leaderboard joins on it, and
// changing any weight or curve below requires minting a new version string
// rather than editing this one.
const ScoringVersion = "2026-07.verified-v1"

// ScoreWeights is the verified-v1 allocation, ported unchanged from the
// standalone runner: pass/fail dominates, efficiency fills the remainder.
// Tool-call count is deliberately absent — it is reported but never rewarded
// or penalized, because fewer tool calls is not by itself better work.
var ScoreWeights = struct {
	Success    float64
	DurationMS float64
	CostTokens float64
	Turns      float64
	ToolErrors float64
}{Success: 70, DurationMS: 10, CostTokens: 10, Turns: 5, ToolErrors: 5}

const ScoringFormula = "Verified-v1: successful runs earn 70 points for deterministic task success, " +
	"plus budgeted efficiency: 10 duration, 10 actual cost (or tokens when cost is unavailable), " +
	"5 turns, and 5 zero tool-error points. Failed runs score 0. " +
	"Tool-call count is reported but not rewarded or penalized."

type Score struct {
	Score           float64             `json:"score"`
	SuccessPoints   float64             `json:"success_points"`
	DurationPoints  float64             `json:"duration_points"`
	CostPoints      float64             `json:"cost_points"`
	TurnPoints      float64             `json:"turn_points"`
	ToolErrorPoints float64             `json:"tool_error_points"`
	MaxScore        float64             `json:"max_score"`
	CostBasis       string              `json:"cost_basis"`
	Formula         string              `json:"formula"`
	Ratios          map[string]*float64 `json:"ratios"`
	Weights         map[string]float64  `json:"weights"`
}

// efficiencyPoints awards full points at or under budget, then decays linearly
// to zero at twice budget. A non-positive or non-finite actual means the metric
// was never reported, which cannot be held against the target.
func efficiencyPoints(actual, budget, maxPoints float64) float64 {
	if math.IsNaN(actual) || math.IsInf(actual, 0) || actual <= 0 {
		return maxPoints
	}
	if budget <= 0 {
		return maxPoints
	}
	if actual <= budget {
		return maxPoints
	}
	return maxPoints * clamp(1-(actual-budget)/budget, 0, 1)
}

func clamp(value, low, high float64) float64 {
	return math.Min(math.Max(value, low), high)
}

func round1(value float64) float64 { return math.Round(value*10) / 10 }
func round3(value float64) float64 { return math.Round(value*1000) / 1000 }

func metricRatio(actual, budget float64) *float64 {
	if budget <= 0 || math.IsNaN(actual) || math.IsInf(actual, 0) {
		return nil
	}
	ratio := round3(actual / budget)
	return &ratio
}

// computeScore applies the verified-v1 contract. A failed run scores zero
// outright: efficiency is only meaningful on work that actually succeeded.
func computeScore(passed bool, m Metrics, budget Budget) Score {
	weights := map[string]float64{
		"success":     ScoreWeights.Success,
		"duration_ms": ScoreWeights.DurationMS,
		"cost_tokens": ScoreWeights.CostTokens,
		"turns":       ScoreWeights.Turns,
		"tool_errors": ScoreWeights.ToolErrors,
	}
	ratios := map[string]*float64{
		"duration":     metricRatio(float64(m.DurationMS), float64(budget.DurationMS)),
		"turns":        metricRatio(float64(m.TurnsUsed), float64(budget.Turns)),
		"tokens_total": metricRatio(float64(m.TokensTotal), float64(budget.TokensTotal)),
		"cost_usd":     metricRatio(m.CostUSD, budget.CostUSD),
	}

	score := Score{
		MaxScore: ScoreWeights.Success + ScoreWeights.DurationMS + ScoreWeights.CostTokens +
			ScoreWeights.Turns + ScoreWeights.ToolErrors,
		Formula: ScoringFormula,
		Ratios:  ratios,
		Weights: weights,
	}

	// Cost is the preferred efficiency basis, but providers do not all report
	// it. Falling back to tokens keeps the run scoreable; the basis is recorded
	// so a leaderboard can flag cross-provider comparisons made on mixed bases.
	score.CostBasis = "cost_usd"
	if m.CostUSD <= 0 || budget.CostUSD <= 0 {
		score.CostBasis = "tokens_total"
	}

	if !passed {
		return score
	}

	score.SuccessPoints = ScoreWeights.Success
	score.DurationPoints = efficiencyPoints(float64(m.DurationMS), float64(budget.DurationMS), ScoreWeights.DurationMS)
	if score.CostBasis == "cost_usd" {
		score.CostPoints = efficiencyPoints(m.CostUSD, budget.CostUSD, ScoreWeights.CostTokens)
	} else {
		score.CostPoints = efficiencyPoints(float64(m.TokensTotal), float64(budget.TokensTotal), ScoreWeights.CostTokens)
	}
	score.TurnPoints = efficiencyPoints(float64(m.TurnsUsed), float64(budget.Turns), ScoreWeights.Turns)
	if m.Errors == 0 {
		score.ToolErrorPoints = ScoreWeights.ToolErrors
	}
	score.Score = round1(score.SuccessPoints + score.DurationPoints + score.CostPoints +
		score.TurnPoints + score.ToolErrorPoints)
	return score
}

// canonicalDigest hashes a value through its canonical JSON encoding. Go's
// encoder emits struct fields in declaration order and map keys sorted, so the
// same definition always produces the same digest.
func canonicalDigest(value any) (string, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}
