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
	Mode           string      `json:"mode,omitempty"`
	Voice          *VoiceCase  `json:"voice,omitempty"`
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

type VoiceCase struct {
	CallerName           string `json:"caller_name,omitempty"`
	CallerPersona        string `json:"caller_persona,omitempty"`
	CallerGoal           string `json:"caller_goal"`
	CallerBehavior       string `json:"caller_behavior,omitempty"`
	Provider             string `json:"provider,omitempty"`
	Voice                string `json:"voice,omitempty"`
	CallerProvider       string `json:"caller_provider,omitempty"`
	CallerVoice          string `json:"caller_voice,omitempty"`
	Greeting             string `json:"greeting,omitempty"`
	MaxFirstResponseMS   int64  `json:"max_first_response_ms,omitempty"`
	MaxAverageResponseMS int64  `json:"max_average_response_ms,omitempty"`
	Transport            string `json:"transport,omitempty"`
	ProtocolFixture      string `json:"protocol_fixture,omitempty"`
}

type Assertion struct {
	Name       string         `json:"name"`
	Type       string         `json:"type"`
	App        string         `json:"app,omitempty"`
	MCP        string         `json:"mcp,omitempty"`
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
	Gating  bool   `json:"gating,omitempty"`
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
	Stage            string                     `json:"stage,omitempty"`
	CaseSnapshot     Case                       `json:"case"`
	TargetSnapshot   Target                     `json:"target"`
	EnvironmentRunID string                     `json:"environment_run_id,omitempty"`
	Execution        *sdk.RuntimeAgentExecution `json:"execution,omitempty"`
	VoiceCall        *EnvironmentVoiceCall      `json:"voice_call,omitempty"`
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

type EnvironmentVoiceCall struct {
	ID              string                     `json:"id"`
	Status          string                     `json:"status"`
	Error           string                     `json:"error,omitempty"`
	Validity        VoiceCallValidity          `json:"validity"`
	Transcript      []VoiceTranscriptTurn      `json:"transcript"`
	Metrics         VoiceCallMetrics           `json:"metrics"`
	TargetRecording string                     `json:"target_recording,omitempty"`
	CallerRecording string                     `json:"caller_recording,omitempty"`
	Execution       *sdk.RuntimeAgentExecution `json:"execution,omitempty"`
	ProtocolEvents  []map[string]any           `json:"protocol_events,omitempty"`
}

type VoiceCallValidity struct {
	Status  string   `json:"status"`
	Reasons []string `json:"reasons,omitempty"`
}

type VoiceTranscriptTurn struct {
	Speaker string    `json:"speaker"`
	Text    string    `json:"text"`
	Time    time.Time `json:"time"`
	AtMS    int64     `json:"at_ms"`
}

type VoiceCallMetrics struct {
	DurationMS                 int64   `json:"duration_ms"`
	FirstResponseMS            int64   `json:"first_response_ms,omitempty"`
	AverageResponseMS          int64   `json:"average_response_ms,omitempty"`
	ReceptionistAudioS         float64 `json:"receptionist_audio_seconds"`
	CallerAudioS               float64 `json:"caller_audio_seconds"`
	Interruptions              int     `json:"interruptions"`
	ToolCalls                  int     `json:"tool_calls"`
	RealtimeErrors             int     `json:"realtime_errors"`
	ReceptionistRealtimeErrors int     `json:"receptionist_realtime_errors"`
	CallerRealtimeErrors       int     `json:"caller_realtime_errors"`
	DroppedAudioEvents         int     `json:"dropped_audio_events"`
	EndedBy                    string  `json:"ended_by"`
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
	Goal   string   `json:"goal"`
	Score  *float64 `json:"score,omitempty"`
	Passed bool     `json:"passed"`
	Why    string   `json:"why"`
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
