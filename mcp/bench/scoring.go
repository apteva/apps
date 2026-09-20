package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math"
	"strings"
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

// ScoreComponent is one profile component's contribution to a run's score.
type ScoreComponent struct {
	Key     string   `json:"key"`
	Label   string   `json:"label,omitempty"`
	Kind    string   `json:"kind"`
	Weight  float64  `json:"weight"`
	Earned  float64  `json:"earned"`
	Metric  string   `json:"metric,omitempty"`
	Actual  *float64 `json:"actual,omitempty"`
	Budget  *float64 `json:"budget,omitempty"`
	Ratio   *float64 `json:"ratio,omitempty"`
	Curve   string   `json:"curve,omitempty"`
	Basis   string   `json:"basis,omitempty"`
	Skipped bool     `json:"skipped,omitempty"`
}

type Score struct {
	Score          float64          `json:"score"`
	MaxScore       float64          `json:"max_score"`
	ProfileVersion string           `json:"profile_version,omitempty"`
	ProfileDigest  string           `json:"profile_digest,omitempty"`
	ProfileName    string           `json:"profile_name,omitempty"`
	Components     []ScoreComponent `json:"components"`

	// Legacy mirrors. Results stored before profiles existed carry these, and
	// the panel still reads them, so a profile using the verified-v1 keys keeps
	// populating them rather than breaking every historical row.
	SuccessPoints   float64             `json:"success_points"`
	DurationPoints  float64             `json:"duration_points"`
	CostPoints      float64             `json:"cost_points"`
	TurnPoints      float64             `json:"turn_points"`
	ToolErrorPoints float64             `json:"tool_error_points"`
	JudgePoints     float64             `json:"judge_points,omitempty"`
	CostBasis       string              `json:"cost_basis"`
	Formula         string              `json:"formula"`
	Ratios          map[string]*float64 `json:"ratios"`
	Weights         map[string]float64  `json:"weights"`
}

func ptr(v float64) *float64 { return &v }

func round1(value float64) float64 { return math.Round(value*10) / 10 }
func round3(value float64) float64 { return math.Round(value*1000) / 1000 }

func clamp(value, low, high float64) float64 {
	return math.Min(math.Max(value, low), high)
}

func metricRatio(actual, budget float64) *float64 {
	if budget <= 0 || math.IsNaN(actual) || math.IsInf(actual, 0) {
		return nil
	}
	ratio := round3(actual / budget)
	return &ratio
}

// scoreWithProfile applies one scoring profile to a run. The gate component
// decides whether the rest counts at all: with on_failure=zero a failed run
// earns nothing, because efficiency is only meaningful on work that succeeded.
func scoreWithProfile(passed bool, m Metrics, budget Budget, p *Profile) Score {
	if p == nil {
		p = verifiedV1()
	}
	score := Score{
		MaxScore:       p.maxScore(),
		ProfileVersion: p.Version,
		ProfileDigest:  p.Digest,
		ProfileName:    p.Name,
		Components:     make([]ScoreComponent, 0, len(p.Components)),
		Ratios:         map[string]*float64{},
		Weights:        map[string]float64{},
		Formula:        profileFormula(p),
	}
	gated := !passed && p.OnFailure == OnFailureZero

	total := 0.0
	for _, c := range p.Components {
		out := ScoreComponent{Key: c.Key, Label: c.Label, Kind: c.Kind, Weight: c.Weight, Metric: c.Metric, Curve: c.Curve}
		score.Weights[c.Key] = c.Weight

		switch c.Kind {
		case KindGate:
			if passed {
				out.Earned = c.Weight
			}
		case KindThreshold:
			if gated {
				out.Skipped = true
				break
			}
			actual, _ := metricValue(m, c.Metric)
			out.Actual = ptr(actual)
			if actual <= c.At {
				out.Earned = c.Weight
			}
		case KindQuality:
			if gated {
				out.Skipped = true
				break
			}
			actual, available := metricValue(m, c.Metric)
			if !available {
				out.Skipped = true
				break
			}
			out.Actual, out.Budget = ptr(actual), ptr(100)
			out.Ratio = ptr(round3(clamp(actual/100, 0, 1)))
			out.Earned = round1(c.Weight * clamp(actual/100, 0, 1))
		case KindBudget:
			if gated {
				out.Skipped = true
				break
			}
			metric := c.Metric
			actual, _ := metricValue(m, metric)
			allowance, _ := budgetValue(budget, metric)
			// Not every provider reports cost. Falling back keeps the run
			// scoreable, and the basis is recorded so a leaderboard can flag a
			// comparison made on mixed bases.
			if (actual <= 0 || allowance <= 0) && c.FallbackMetric != "" {
				metric = c.FallbackMetric
				actual, _ = metricValue(m, metric)
				allowance, _ = budgetValue(budget, metric)
			}
			out.Basis, out.Metric = metric, metric
			out.Actual, out.Budget = ptr(actual), ptr(allowance)
			out.Ratio = metricRatio(actual, allowance)
			score.Ratios[c.Key] = out.Ratio
			out.Earned = round1(c.Weight * curveScore(c.Curve, actual, allowance, c.ZeroAt))
		}
		total += out.Earned
		score.Components = append(score.Components, out)
		switch c.Key {
		case "success":
			score.SuccessPoints = out.Earned
		case "duration":
			score.DurationPoints = out.Earned
		case "cost":
			score.CostPoints, score.CostBasis = out.Earned, out.Basis
		case "turns":
			score.TurnPoints = out.Earned
		case "tool_errors":
			score.ToolErrorPoints = out.Earned
		case "judge":
			score.JudgePoints = out.Earned
		}
	}
	// Legacy consumers read ratios by metric name, not component key.
	score.Ratios["duration"] = metricRatio(float64(m.DurationMS), float64(budget.DurationMS))
	score.Ratios["turns"] = metricRatio(float64(m.TurnsUsed), float64(budget.Turns))
	score.Ratios["tokens_total"] = metricRatio(float64(m.TokensTotal), float64(budget.TokensTotal))
	score.Score = round1(total)
	return score
}

func profileFormula(p *Profile) string {
	parts := make([]string, 0, len(p.Components))
	for _, c := range p.Components {
		switch c.Kind {
		case KindGate:
			parts = append(parts, fmt.Sprintf("%g for %s", c.Weight, orKey(c.Label, c.Key)))
		case KindThreshold:
			parts = append(parts, fmt.Sprintf("%g for %s at or below %g", c.Weight, c.Metric, c.At))
		case KindQuality:
			parts = append(parts, fmt.Sprintf("%g for %s scaled from 0-100", c.Weight, c.Metric))
		default:
			parts = append(parts, fmt.Sprintf("%g for %s (%s)", c.Weight, c.Metric, orKey(c.Curve, CurveCliff)))
		}
	}
	tail := "Failed runs score zero."
	if p.OnFailure == OnFailureComponents {
		tail = "Failed runs still earn efficiency credit."
	}
	return fmt.Sprintf("%s: %s. %s", p.Name, strings.Join(parts, ", "), tail)
}

func orKey(value, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

// computeScore scores under the original frozen contract. Kept so callers and
// tests that predate profiles keep the exact behaviour they asserted.
func computeScore(passed bool, m Metrics, budget Budget) Score {
	return scoreWithProfile(passed, m, budget, verifiedV1())
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
