package main

import (
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Pack states. A draft is freely editable and may be run only after sealing.
// Sealing copies the draft into a new immutable row carrying a content digest;
// sealed rows are never mutated, so a score always names the exact definition
// that produced it.
const (
	PackStateDraft  = "draft"
	PackStateSealed = "sealed"
)

// Admission decides whether a result is evidence about a target or noise about
// the harness. A provider or environment failure before the agent ever ran says
// nothing about the model and must never become a zero-score row.
const (
	AdmissionVerified   = "verified"   // the agent ran and deterministic checks were evaluated
	AdmissionDiagnostic = "diagnostic" // the agent ran, but the scenario declares no checks
	AdmissionInvalid    = "invalid"    // harness failure; withheld from the record
)

const (
	RunStatusQueued    = "queued"
	RunStatusRunning   = "running"
	RunStatusCompleted = "completed"
	RunStatusFailed    = "failed"
	RunStatusCancelled = "cancelled"
)

type Pack struct {
	ID             string `json:"id"`
	Name           string `json:"name"`
	Description    string `json:"description"`
	State          string `json:"state"`
	Version        string `json:"version,omitempty"`
	Digest         string `json:"digest,omitempty"`
	ScoringVersion string `json:"scoring_version,omitempty"`
	// ProfileDigest pins the scoring contract this pack is scored under. A
	// sealed pack carries one so its results always mean the same thing.
	ProfileDigest string     `json:"profile_digest,omitempty"`
	SourcePackID  string     `json:"source_pack_id,omitempty"`
	Scenarios     []Scenario `json:"scenarios"`
	Revision      int        `json:"revision"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

func (p *Pack) scenario(id string) *Scenario {
	for i := range p.Scenarios {
		if p.Scenarios[i].ID == id {
			return &p.Scenarios[i]
		}
	}
	return nil
}

// Scenario is one benchmark task: a seeded world, a prompt, deterministic
// final-state checks, and the budgets efficiency is scored against.
type Scenario struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	Prompt         string   `json:"prompt"`
	Goals          []string `json:"goals,omitempty"`
	EnvironmentID  string   `json:"environment_id,omitempty"`
	SnapshotID     string   `json:"snapshot_id,omitempty"`
	Checks         []Check  `json:"checks"`
	Budget         Budget   `json:"budget"`
	TimeoutSeconds int      `json:"timeout_seconds,omitempty"`
	MaxTurns       int      `json:"max_turns,omitempty"`
	Weight         float64  `json:"weight,omitempty"`
}

// Check mirrors an Evals assertion. One check is one deterministic fact about
// final state, read back through an app tool.
type Check struct {
	Name   string         `json:"name"`
	Type   string         `json:"type,omitempty"` // app_state (default), mcp_state, telemetry, edge_call…
	App    string         `json:"app,omitempty"`
	MCP    string         `json:"mcp,omitempty"`
	Tool   string         `json:"tool,omitempty"`
	Input  map[string]any `json:"input,omitempty"`
	Path   string         `json:"path,omitempty"`
	Equals any            `json:"equals,omitempty"`
}

// Budget is the per-scenario efficiency allowance. Hitting budget earns full
// efficiency points; points decay linearly to zero at twice budget.
type Budget struct {
	DurationMS  int64   `json:"duration_ms"`
	CostUSD     float64 `json:"cost_usd"`
	TokensTotal int64   `json:"tokens_total"`
	Turns       int     `json:"turns"`
}

// Target is one thing being benchmarked. Shape matches the Evals target so it
// passes through untouched.
type Target struct {
	AgentID   int64  `json:"agent_id"`
	AgentName string `json:"agent_name,omitempty"`
	Provider  string `json:"provider,omitempty"`
	Model     string `json:"model,omitempty"`
	Directive string `json:"directive,omitempty"`
}

func (t Target) label() string {
	switch {
	case t.Provider != "" && t.Model != "":
		return t.Provider + "/" + t.Model
	case t.Model != "":
		return t.Model
	case t.Provider != "":
		return t.Provider
	case t.AgentName != "":
		return t.AgentName
	}
	return "target"
}

// Provenance is what makes a number reproducible by someone else. Snapshot
// content addressing is not yet available from Environments, so runs record
// the snapshot ids they pinned and callers must treat world-identity as
// asserted rather than proven until that lands.
type Provenance struct {
	ScoringVersion    string            `json:"scoring_version"`
	PackDigest        string            `json:"pack_digest"`
	PackVersion       string            `json:"pack_version"`
	ScenarioDigests   map[string]string `json:"scenario_digests"`
	SnapshotIDs       map[string]string `json:"snapshot_ids,omitempty"`
	EnvironmentIDs    map[string]string `json:"environment_ids,omitempty"`
	PlatformVersion   string            `json:"platform_version,omitempty"`
	SnapshotsVerified bool              `json:"snapshots_verified"`
	CapturedAt        time.Time         `json:"captured_at"`
}

type Run struct {
	ID             string `json:"id"`
	PackID         string `json:"pack_id"`
	PackName       string `json:"pack_name"`
	PackVersion    string `json:"pack_version"`
	PackDigest     string `json:"pack_digest"`
	ScoringVersion string `json:"scoring_version"`
	// ScoringProfileDigest is the contract this run was actually scored under.
	// The leaderboard joins on it, so a run scored under a different profile is
	// never averaged with this one.
	ScoringProfileDigest string     `json:"scoring_profile_digest"`
	Name                 string     `json:"name"`
	Targets              []Target   `json:"targets"`
	Trials               int        `json:"trials"`
	SuiteID              string     `json:"suite_id,omitempty"`
	ExperimentID         string     `json:"experiment_id,omitempty"`
	Status               string     `json:"status"`
	Provenance           Provenance `json:"provenance"`
	Summary              Summary    `json:"summary"`
	Results              []Result   `json:"results,omitempty"`
	Error                string     `json:"error,omitempty"`
	CreatedAt            time.Time  `json:"created_at"`
	StartedAt            *time.Time `json:"started_at,omitempty"`
	FinishedAt           *time.Time `json:"finished_at,omitempty"`
}

type Result struct {
	ID            string    `json:"id"`
	BenchRunID    string    `json:"bench_run_id"`
	ScenarioID    string    `json:"scenario_id"`
	ScenarioName  string    `json:"scenario_name"`
	TargetIndex   int       `json:"target_index"`
	Target        Target    `json:"target"`
	Trial         int       `json:"trial"`
	EvalRunID     string    `json:"eval_run_id,omitempty"`
	Admission     string    `json:"admission"`
	InvalidReason string    `json:"invalid_reason,omitempty"`
	Passed        bool      `json:"passed"`
	Score         Score     `json:"score"`
	Metrics       Metrics   `json:"metrics"`
	Error         string    `json:"error,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

type Metrics struct {
	Provider    string  `json:"provider,omitempty"`
	Model       string  `json:"model,omitempty"`
	DurationMS  int64   `json:"duration_ms"`
	TurnsUsed   int     `json:"turns_used"`
	TokensIn    int     `json:"tokens_in"`
	TokensOut   int     `json:"tokens_out"`
	TokensTotal int     `json:"tokens_total"`
	CostUSD     float64 `json:"cost_usd"`
	LLMCalls    int     `json:"llm_calls"`
	ToolCalls   int     `json:"tool_calls"`
	Errors      int     `json:"errors"`
}

// Summary aggregates one bench run. PassRate is the headline; AverageScore is
// secondary and computed over admitted results only.
type Summary struct {
	Total        int             `json:"total"`
	Verified     int             `json:"verified"`
	Invalid      int             `json:"invalid"`
	Passed       int             `json:"passed"`
	PassRate     float64         `json:"pass_rate"`
	AverageScore float64         `json:"average_score"`
	Targets      []TargetSummary `json:"targets"`
}

type TargetSummary struct {
	TargetIndex       int     `json:"target_index"`
	Target            Target  `json:"target"`
	Label             string  `json:"label"`
	Runs              int     `json:"runs"`
	Verified          int     `json:"verified"`
	Invalid           int     `json:"invalid"`
	Passed            int     `json:"passed"`
	PassRate          float64 `json:"pass_rate"`
	AverageScore      float64 `json:"average_score"`
	AverageDurationMS float64 `json:"average_duration_ms"`
	AverageTokens     float64 `json:"average_tokens"`
	AverageCostUSD    float64 `json:"average_cost_usd"`
}

type Baseline struct {
	ID          string    `json:"id"`
	PackDigest  string    `json:"pack_digest"`
	ScenarioID  string    `json:"scenario_id"`
	Label       string    `json:"label"`
	Target      Target    `json:"target"`
	Score       float64   `json:"score"`
	PassRate    float64   `json:"pass_rate"`
	Metrics     Metrics   `json:"metrics"`
	SourceRunID string    `json:"source_run_id,omitempty"`
	CreatedAt   time.Time `json:"created_at"`
}

// evalRun is the subset of the Evals run shape bench reads back.
type evalRun struct {
	ID           string                     `json:"id"`
	ExperimentID string                     `json:"experiment_id"`
	CaseID       string                     `json:"case_id"`
	TargetIndex  int                        `json:"target_index"`
	Repetition   int                        `json:"repetition"`
	Status       string                     `json:"status"`
	TargetSnap   Target                     `json:"target"`
	Execution    *sdk.RuntimeAgentExecution `json:"execution,omitempty"`
	Error        string                     `json:"error,omitempty"`
	StartedAt    *time.Time                 `json:"started_at,omitempty"`
	FinishedAt   *time.Time                 `json:"finished_at,omitempty"`
}

type evalExperiment struct {
	ID      string    `json:"id"`
	SuiteID string    `json:"suite_id"`
	Status  string    `json:"status"`
	Error   string    `json:"error,omitempty"`
	Runs    []evalRun `json:"runs,omitempty"`
}

type evalSuite struct {
	ID   string `json:"id"`
	Name string `json:"name"`
}

type evalCase struct {
	ID      string `json:"id"`
	SuiteID string `json:"suite_id"`
	Name    string `json:"name"`
}
