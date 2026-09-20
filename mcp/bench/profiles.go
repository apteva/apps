package main

import (
	"errors"
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// A scoring profile is the contract that turns one run's metrics into a score.
// It is sealed exactly like a pack: editing mints a new version rather than
// changing what existing scores mean, and the leaderboard joins on its digest
// so runs scored under different contracts are never averaged together.
const (
	ProfileStateDraft  = "draft"
	ProfileStateSealed = "sealed"
)

// Component kinds.
const (
	KindGate      = "gate"      // pass/fail; drives on_failure
	KindBudget    = "budget"    // a metric measured against the scenario's budget
	KindThreshold = "threshold" // binary: metric at or below a cutoff
	KindQuality   = "quality"   // a 0-100 quality metric; higher is better
)

// Curves map a metric's ratio-to-budget onto [0,1]. Only budget components use
// them.
const (
	// CurveCliff is verified-v1's shape: full marks anywhere at or under
	// budget, then linear to zero at ZeroAt x budget. It deliberately has no
	// resolution below budget — preserved so existing scores stay reproducible.
	CurveCliff = "cliff"
	// CurveLinear grades below budget too, so beating it earns more.
	CurveLinear = "linear"
	// CurveRatio is budget/actual capped at 1: no cliff, and a run far over
	// budget still scores something rather than falling to a hard zero.
	CurveRatio = "ratio"
	// CurveLog is CurveRatio on a log scale, for metrics that span orders of
	// magnitude — tokens being the obvious one.
	CurveLog = "log"
)

const (
	OnFailureZero       = "zero"       // a failed run scores 0 outright
	OnFailureComponents = "components" // a failed run still earns efficiency credit
)

type ProfileComponent struct {
	Key   string `json:"key"`
	Label string `json:"label,omitempty"`
	Kind  string `json:"kind"`
	// Metric names a field of Metrics. Budget and threshold components require it.
	Metric string `json:"metric,omitempty"`
	// FallbackMetric is used when Metric is absent or zero for a run — cost
	// falling back to tokens, for instance. The result records which was used.
	FallbackMetric string  `json:"fallback_metric,omitempty"`
	Weight         float64 `json:"weight"`
	Curve          string  `json:"curve,omitempty"`
	// ZeroAt is the multiple of budget at which a cliff curve reaches zero.
	ZeroAt float64 `json:"zero_at,omitempty"`
	// At is the threshold cutoff: the component scores its full weight when
	// the metric is at or below it.
	At float64 `json:"at,omitempty"`
}

type Profile struct {
	ID          string             `json:"id"`
	Name        string             `json:"name"`
	Description string             `json:"description,omitempty"`
	State       string             `json:"state"`
	Version     string             `json:"version,omitempty"`
	Digest      string             `json:"digest,omitempty"`
	SourceID    string             `json:"source_profile_id,omitempty"`
	Builtin     bool               `json:"builtin,omitempty"`
	OnFailure   string             `json:"on_failure"`
	Components  []ProfileComponent `json:"components"`
	Revision    int                `json:"revision"`
	CreatedAt   time.Time          `json:"created_at"`
	UpdatedAt   time.Time          `json:"updated_at"`
}

func (p *Profile) maxScore() float64 {
	total := 0.0
	for _, c := range p.Components {
		total += c.Weight
	}
	return round1(total)
}

// metricValue reads one named field off a run's metrics. Keeping this explicit
// rather than reflective means a profile can only reference metrics that exist.
func metricValue(m Metrics, name string) (float64, bool) {
	switch name {
	case "duration_ms":
		return float64(m.DurationMS), true
	case "turns_used":
		return float64(m.TurnsUsed), true
	case "tokens_total":
		return float64(m.TokensTotal), true
	case "tokens_in":
		return float64(m.TokensIn), true
	case "tokens_out":
		return float64(m.TokensOut), true
	case "cost_usd":
		return m.CostUSD, true
	case "llm_calls":
		return float64(m.LLMCalls), true
	case "tool_calls":
		return float64(m.ToolCalls), true
	case "errors":
		return float64(m.Errors), true
	case "judge_score":
		if m.JudgeScore == nil {
			return 0, false
		}
		return *m.JudgeScore, true
	}
	return 0, false
}

// budgetValue reads the matching allowance for a metric off a scenario budget.
func budgetValue(b Budget, name string) (float64, bool) {
	switch name {
	case "duration_ms":
		return float64(b.DurationMS), true
	case "turns_used":
		return float64(b.Turns), true
	case "tokens_total", "tokens_in", "tokens_out":
		return float64(b.TokensTotal), true
	case "cost_usd":
		return b.CostUSD, true
	}
	return 0, false
}

// curveScore maps actual/budget onto [0,1] for the named curve.
func curveScore(curve string, actual, budget, zeroAt float64) float64 {
	if budget <= 0 || math.IsNaN(actual) || math.IsInf(actual, 0) {
		return 1
	}
	if actual <= 0 {
		// The metric was never reported. That is not the target's fault, so it
		// is not held against it — same rule verified-v1 has always applied.
		return 1
	}
	switch curve {
	case CurveLinear:
		return clamp(1-actual/budget, 0, 1)
	case CurveRatio:
		return clamp(budget/actual, 0, 1)
	case CurveLog:
		if actual <= budget {
			return 1
		}
		// One full budget-doubling of overrun costs half the remaining credit.
		return clamp(1/(1+math.Log2(actual/budget)), 0, 1)
	default: // CurveCliff
		if actual <= budget {
			return 1
		}
		if zeroAt <= 1 {
			zeroAt = 2
		}
		return clamp(1-(actual-budget)/(budget*(zeroAt-1)), 0, 1)
	}
}

func validateProfile(p *Profile) error {
	if strings.TrimSpace(p.Name) == "" {
		return errors.New("name is required")
	}
	if len(p.Components) == 0 {
		return errors.New("a profile needs at least one component")
	}
	if p.OnFailure != OnFailureZero && p.OnFailure != OnFailureComponents {
		return fmt.Errorf("on_failure must be %q or %q", OnFailureZero, OnFailureComponents)
	}
	seen := map[string]bool{}
	gates := 0
	for i, c := range p.Components {
		if strings.TrimSpace(c.Key) == "" {
			return fmt.Errorf("component %d needs a key", i)
		}
		if seen[c.Key] {
			return fmt.Errorf("duplicate component key %q", c.Key)
		}
		seen[c.Key] = true
		if c.Weight <= 0 {
			return fmt.Errorf("component %q needs a positive weight", c.Key)
		}
		switch c.Kind {
		case KindGate:
			gates++
		case KindBudget, KindThreshold, KindQuality:
			if strings.TrimSpace(c.Metric) == "" {
				return fmt.Errorf("component %q needs a metric", c.Key)
			}
			if _, ok := metricValue(Metrics{}, c.Metric); !ok {
				return fmt.Errorf("component %q references unknown metric %q", c.Key, c.Metric)
			}
			if c.FallbackMetric != "" {
				if _, ok := metricValue(Metrics{}, c.FallbackMetric); !ok {
					return fmt.Errorf("component %q references unknown fallback metric %q", c.Key, c.FallbackMetric)
				}
			}
			if c.Kind == KindBudget {
				if _, ok := budgetValue(Budget{}, c.Metric); !ok {
					return fmt.Errorf("component %q: metric %q has no budget to measure against", c.Key, c.Metric)
				}
			}
		default:
			return fmt.Errorf("component %q has unknown kind %q", c.Key, c.Kind)
		}
	}
	// Without a gate every run scores its efficiency regardless of whether it
	// did the task, which is not a benchmark.
	if gates == 0 {
		return errors.New("a profile needs a gate component for task success")
	}
	if gates > 1 {
		return errors.New("a profile may only have one gate component")
	}
	return nil
}

// profileDigest hashes the contract itself — never ids, names or timestamps —
// so two identical profiles are one contract.
func profileDigest(p *Profile) (string, error) {
	components := append([]ProfileComponent(nil), p.Components...)
	sort.Slice(components, func(a, b int) bool { return components[a].Key < components[b].Key })
	return canonicalDigest(struct {
		OnFailure  string             `json:"on_failure"`
		Components []ProfileComponent `json:"components"`
	}{p.OnFailure, components})
}

// verifiedV1 is the contract every score in the record so far was computed
// under, expressed in the profile vocabulary. Its digest must stay stable:
// changing it would orphan existing results.
func verifiedV1() *Profile {
	return &Profile{
		Name:        "Verified v1",
		Description: "The original frozen contract: 70 points for deterministic task success, then budgeted efficiency. Full marks at or under budget, zero at twice budget. Failed runs score zero.",
		State:       ProfileStateSealed,
		Version:     ScoringVersion,
		Builtin:     true,
		OnFailure:   OnFailureZero,
		Components: []ProfileComponent{
			{Key: "success", Label: "Task success", Kind: KindGate, Weight: 70},
			{Key: "duration", Label: "Duration", Kind: KindBudget, Metric: "duration_ms", Weight: 10, Curve: CurveCliff, ZeroAt: 2},
			{Key: "cost", Label: "Cost / tokens", Kind: KindBudget, Metric: "cost_usd", FallbackMetric: "tokens_total", Weight: 10, Curve: CurveCliff, ZeroAt: 2},
			{Key: "turns", Label: "Turns", Kind: KindBudget, Metric: "turns_used", Weight: 5, Curve: CurveCliff, ZeroAt: 2},
			{Key: "tool_errors", Label: "No tool errors", Kind: KindThreshold, Metric: "errors", At: 0, Weight: 5},
		},
	}
}

// gradedV1 keeps verified-v1's weights but grades below budget and decays
// gracefully above it, so two targets that both beat every allowance are still
// told apart. Tokens use a log curve because they span orders of magnitude.
func gradedV1() *Profile {
	return &Profile{
		Name:        "Graded v1",
		Description: "Same weights as Verified v1, but efficiency is graded rather than stepped: beating a budget earns more, and exceeding one decays instead of falling to zero.",
		State:       ProfileStateSealed,
		Version:     "2026-09.graded-v1",
		Builtin:     true,
		OnFailure:   OnFailureZero,
		Components: []ProfileComponent{
			{Key: "success", Label: "Task success", Kind: KindGate, Weight: 70},
			{Key: "duration", Label: "Duration", Kind: KindBudget, Metric: "duration_ms", Weight: 10, Curve: CurveRatio},
			{Key: "cost", Label: "Cost / tokens", Kind: KindBudget, Metric: "cost_usd", FallbackMetric: "tokens_total", Weight: 10, Curve: CurveLog},
			{Key: "turns", Label: "Turns", Kind: KindBudget, Metric: "turns_used", Weight: 5, Curve: CurveRatio},
			{Key: "tool_errors", Label: "No tool errors", Kind: KindThreshold, Metric: "errors", At: 0, Weight: 5},
		},
	}
}

// correctnessOnly is for suites where efficiency is noise and only the
// deterministic checks matter.
func correctnessOnly() *Profile {
	return &Profile{
		Name:        "Correctness only",
		Description: "Pass or fail, nothing else. Use when efficiency is not the question.",
		State:       ProfileStateSealed,
		Version:     "2026-09.correctness-only",
		Builtin:     true,
		OnFailure:   OnFailureZero,
		Components:  []ProfileComponent{{Key: "success", Label: "Task success", Kind: KindGate, Weight: 100}},
	}
}

// judgedV1 makes the qualitative verdict visible in Bench's score while
// retaining deterministic success as a hard gate. A failed assertion or judge
// verdict still zeroes the entire score through OnFailureZero.
func judgedV1() *Profile {
	return &Profile{
		Name:        "Judged v1",
		Description: "40 points for gated task success, 30 for the pinned LLM judge score, and 30 for budgeted efficiency. Deterministic and judge failures are hard gates.",
		State:       ProfileStateSealed,
		Version:     "2026-09.judged-v1",
		Builtin:     true,
		OnFailure:   OnFailureZero,
		Components: []ProfileComponent{
			{Key: "success", Label: "Task success", Kind: KindGate, Weight: 40},
			{Key: "judge", Label: "Judge quality", Kind: KindQuality, Metric: "judge_score", Weight: 30},
			{Key: "duration", Label: "Duration", Kind: KindBudget, Metric: "duration_ms", Weight: 10, Curve: CurveCliff, ZeroAt: 2},
			{Key: "cost", Label: "Cost / tokens", Kind: KindBudget, Metric: "cost_usd", FallbackMetric: "tokens_total", Weight: 10, Curve: CurveCliff, ZeroAt: 2},
			{Key: "turns", Label: "Turns", Kind: KindBudget, Metric: "turns_used", Weight: 5, Curve: CurveCliff, ZeroAt: 2},
			{Key: "tool_errors", Label: "No tool errors", Kind: KindThreshold, Metric: "errors", At: 0, Weight: 5},
		},
	}
}

func builtinProfiles() []*Profile {
	return []*Profile{verifiedV1(), judgedV1(), gradedV1(), correctnessOnly()}
}
