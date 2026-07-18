package main

import (
	"encoding/json"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type Suite struct {
	ID                string     `json:"id"`
	Name              string     `json:"name"`
	Description       string     `json:"description,omitempty"`
	EnvironmentID     string     `json:"environment_id,omitempty"`
	JudgeModel        string     `json:"judge_model,omitempty"`
	ContinuousTargets []Target   `json:"continuous_targets,omitempty"`
	ScheduleMinutes   int        `json:"schedule_minutes,omitempty"`
	RequiredPassRate  float64    `json:"required_pass_rate"`
	Enabled           bool       `json:"enabled"`
	Revision          int        `json:"revision"`
	NextRunAt         *time.Time `json:"next_run_at,omitempty"`
	CreatedAt         time.Time  `json:"created_at"`
	UpdatedAt         time.Time  `json:"updated_at"`
	Cases             []Case     `json:"cases,omitempty"`
}

type Case struct {
	ID             string      `json:"id"`
	SuiteID        string      `json:"suite_id"`
	Name           string      `json:"name"`
	Prompt         string      `json:"prompt"`
	Goals          []string    `json:"goals"`
	Assertions     []Assertion `json:"assertions"`
	EnvironmentID  string      `json:"environment_id,omitempty"`
	Weight         float64     `json:"weight"`
	TimeoutSeconds int         `json:"timeout_seconds"`
	MaxTurns       int         `json:"max_turns"`
	Enabled        bool        `json:"enabled"`
	Revision       int         `json:"revision"`
	CreatedAt      time.Time   `json:"created_at"`
	UpdatedAt      time.Time   `json:"updated_at"`
}

type Assertion struct {
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	App        string         `json:"app,omitempty"`
	Tool       string         `json:"tool,omitempty"`
	Input      map[string]any `json:"input,omitempty"`
	Path       string         `json:"path,omitempty"`
	Equals     any            `json:"equals,omitempty"`
	Method     string         `json:"method,omitempty"`
	Host       string         `json:"host,omitempty"`
	MinCalls   int            `json:"min_calls,omitempty"`
	AgentAlias string         `json:"agent_alias,omitempty"`
	EventType  string         `json:"event_type,omitempty"`
	Fixture    string         `json:"fixture,omitempty"`
}

type AssertionResult struct {
	Name    string `json:"name"`
	Passed  bool   `json:"passed"`
	Actual  any    `json:"actual,omitempty"`
	Message string `json:"message,omitempty"`
}

type Target struct {
	AgentID       int64  `json:"agent_id"`
	AgentName     string `json:"agent_name,omitempty"`
	Provider      string `json:"provider,omitempty"`
	Model         string `json:"model,omitempty"`
	Directive     string `json:"directive,omitempty"`
	DirectiveETag string `json:"directive_etag,omitempty"`
}

type Experiment struct {
	ID             string     `json:"id"`
	SuiteID        string     `json:"suite_id"`
	SuiteRevision  int        `json:"suite_revision"`
	Name           string     `json:"name"`
	TriggerType    string     `json:"trigger_type"`
	Status         string     `json:"status"`
	Targets        []Target   `json:"targets"`
	Repetitions    int        `json:"repetitions"`
	JudgeModel     string     `json:"judge_model,omitempty"`
	BaselineTarget int        `json:"baseline_target"`
	CreatedAt      time.Time  `json:"created_at"`
	StartedAt      *time.Time `json:"started_at,omitempty"`
	FinishedAt     *time.Time `json:"finished_at,omitempty"`
	Error          string     `json:"error,omitempty"`
	Runs           []Run      `json:"runs,omitempty"`
	Summary        *Summary   `json:"summary,omitempty"`
}

type Run struct {
	ID               string                     `json:"id"`
	ExperimentID     string                     `json:"experiment_id"`
	CaseID           string                     `json:"case_id"`
	CaseRevision     int                        `json:"case_revision"`
	TargetIndex      int                        `json:"target_index"`
	Repetition       int                        `json:"repetition"`
	Status           string                     `json:"status"`
	CaseSnapshot     Case                       `json:"case"`
	TargetSnapshot   Target                     `json:"target"`
	EnvironmentRunID string                     `json:"environment_run_id,omitempty"`
	Execution        *sdk.RuntimeAgentExecution `json:"execution,omitempty"`
	Assertions       []AssertionResult          `json:"assertions"`
	Judge            *JudgeVerdict              `json:"judge,omitempty"`
	CorrectnessScore *float64                   `json:"correctness_score,omitempty"`
	JudgeScore       *float64                   `json:"judge_score,omitempty"`
	OverallScore     *float64                   `json:"overall_score,omitempty"`
	StartedAt        *time.Time                 `json:"started_at,omitempty"`
	FinishedAt       *time.Time                 `json:"finished_at,omitempty"`
	Error            string                     `json:"error,omitempty"`
	CreatedAt        time.Time                  `json:"created_at"`
	Suggestions      []Suggestion               `json:"suggestions,omitempty"`
}

type JudgeVerdict struct {
	Passed              bool                 `json:"passed"`
	Score               float64              `json:"score"`
	Reasoning           string               `json:"reasoning"`
	PerGoal             []GoalVerdict        `json:"per_goal"`
	DirectiveSuggestion *DirectiveSuggestion `json:"directive_suggestion,omitempty"`
	Model               string               `json:"model,omitempty"`
	Usage               map[string]any       `json:"usage,omitempty"`
}

type GoalVerdict struct {
	Goal   string `json:"goal"`
	Passed bool   `json:"passed"`
	Why    string `json:"why"`
}

type DirectiveSuggestion struct {
	Directive string `json:"directive"`
	Reason    string `json:"reason"`
}

type Suggestion struct {
	ID           string     `json:"id"`
	RunID        string     `json:"run_id"`
	AgentID      int64      `json:"agent_id"`
	Directive    string     `json:"directive"`
	ExpectedETag string     `json:"expected_etag"`
	Reason       string     `json:"reason"`
	Status       string     `json:"status"`
	CreatedAt    time.Time  `json:"created_at"`
	AppliedAt    *time.Time `json:"applied_at,omitempty"`
}

type Summary struct {
	Total        int             `json:"total"`
	Queued       int             `json:"queued"`
	Running      int             `json:"running"`
	Passed       int             `json:"passed"`
	Failed       int             `json:"failed"`
	Errors       int             `json:"errors"`
	PassRate     float64         `json:"pass_rate"`
	AverageScore float64         `json:"average_score"`
	Targets      []TargetSummary `json:"targets"`
}

type TargetSummary struct {
	TargetIndex   int     `json:"target_index"`
	Target        Target  `json:"target"`
	Runs          int     `json:"runs"`
	Passed        int     `json:"passed"`
	PassRate      float64 `json:"pass_rate"`
	AverageScore  float64 `json:"average_score"`
	AverageCost   float64 `json:"average_cost_usd"`
	AverageTokens float64 `json:"average_tokens"`
}

type EnvironmentDefinition struct {
	ID   string         `json:"id"`
	Name string         `json:"name"`
	Spec map[string]any `json:"spec"`
}

type EnvironmentRun struct {
	ID          string                  `json:"id"`
	RuntimeID   string                  `json:"runtime_id"`
	Status      string                  `json:"status"`
	WebFixtures []EnvironmentWebFixture `json:"web_fixtures,omitempty"`
}

type EnvironmentWebFixture struct {
	ID      string `json:"id"`
	Pack    string `json:"pack"`
	TestURL string `json:"test_url"`
}

type LLMModels struct {
	Models []map[string]any `json:"models"`
}

func encodeJSON(value any) string {
	raw, _ := json.Marshal(value)
	return string(raw)
}
