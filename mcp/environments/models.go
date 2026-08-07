package main

import (
	"encoding/json"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type EnvironmentSpec struct {
	Version             int                             `json:"version"`
	TTLSeconds          int                             `json:"ttl_seconds,omitempty"`
	AppInstallIDs       []int64                         `json:"app_install_ids,omitempty"`
	ConnectionIDs       []int64                         `json:"connection_ids,omitempty"`
	MCPServerIDs        []int64                         `json:"mcp_server_ids,omitempty"`
	NetworkMode         sdk.RuntimeNetworkMode          `json:"network_mode,omitempty"`
	IntegrationMode     string                          `json:"integration_mode,omitempty"`
	AllowHostSuffixes   []string                        `json:"allow_host_suffixes,omitempty"`
	HTTPMocks           []sdk.RuntimeHTTPMock           `json:"http_mocks,omitempty"`
	IntegrationFixtures []sdk.RuntimeIntegrationMock    `json:"integration_fixtures,omitempty"`
	IntegrationBindings []sdk.RuntimeIntegrationBinding `json:"integration_bindings,omitempty"`
	Subscriptions       []sdk.RuntimeSubscription       `json:"subscriptions,omitempty"`
	Seeds               []SeedStep                      `json:"seeds,omitempty"`
	Agents              []AgentSpec                     `json:"agents,omitempty"`
	SnapshotID          string                          `json:"snapshot_id,omitempty"`
	WebFixtures         []WebFixtureSpec                `json:"web_fixtures,omitempty"`
	ProtocolFixtures    []ProtocolFixtureSpec           `json:"protocol_fixtures,omitempty"`
	VoiceFixtures       []VoiceFixtureSpec              `json:"voice_fixtures,omitempty"`
}

type WebFixtureSpec struct {
	ID       string         `json:"id"`
	Pack     string         `json:"pack"`
	Version  string         `json:"version,omitempty"`
	Scenario string         `json:"scenario,omitempty"`
	Strict   bool           `json:"strict,omitempty"`
	Seed     map[string]any `json:"seed,omitempty"`
}

type WebFixtureCatalogItem struct {
	ID          string                `json:"id"`
	Name        string                `json:"name"`
	Description string                `json:"description"`
	Version     string                `json:"version"`
	Scenarios   []WebFixtureScenario  `json:"scenarios"`
	SeedFields  []WebFixtureSeedField `json:"seed_fields,omitempty"`
}

type WebFixtureScenario struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
}

type WebFixtureSeedField struct {
	Name        string `json:"name"`
	Label       string `json:"label"`
	Description string `json:"description,omitempty"`
	Default     any    `json:"default,omitempty"`
	Required    bool   `json:"required,omitempty"`
}

type WebFixtureInstance struct {
	RunID        string         `json:"run_id"`
	ID           string         `json:"id"`
	Pack         string         `json:"pack"`
	Version      string         `json:"version"`
	Scenario     string         `json:"scenario"`
	Status       string         `json:"status"`
	Seed         map[string]any `json:"seed,omitempty"`
	State        map[string]any `json:"state,omitempty"`
	PreviewPath  string         `json:"preview_path,omitempty"`
	TestURL      string         `json:"test_url,omitempty"`
	CreatedAt    time.Time      `json:"created_at"`
	UpdatedAt    time.Time      `json:"updated_at"`
	Token        string         `json:"-"`
	InitialState map[string]any `json:"-"`
}

type WebFixtureEvent struct {
	ID        int64          `json:"id"`
	RunID     string         `json:"run_id"`
	FixtureID string         `json:"fixture_id"`
	Type      string         `json:"type"`
	Data      map[string]any `json:"data,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

type VoiceFixtureSpec struct {
	ID               string                `json:"id,omitempty"`
	Name             string                `json:"name,omitempty"`
	CallerName       string                `json:"caller_name,omitempty"`
	CallerPersona    string                `json:"caller_persona,omitempty"`
	CallerGoal       string                `json:"caller_goal"`
	CallerBehavior   string                `json:"caller_behavior,omitempty"`
	Provider         string                `json:"provider,omitempty"`
	Voice            string                `json:"voice,omitempty"`
	CallerProvider   string                `json:"caller_provider,omitempty"`
	CallerVoice      string                `json:"caller_voice,omitempty"`
	TimeoutSeconds   int                   `json:"timeout_seconds,omitempty"`
	Greeting         string                `json:"greeting,omitempty"`
	TargetAgent      string                `json:"target_agent,omitempty"`
	TargetDirective  string                `json:"target_directive,omitempty"`
	DisconnectOnDone bool                  `json:"disconnect_on_done,omitempty"`
	Transport        string                `json:"transport,omitempty"`
	ProtocolFixture  string                `json:"protocol_fixture,omitempty"`
	AudioConditions  *VoiceAudioConditions `json:"audio_conditions,omitempty"`
}

type VoiceAudioConditions struct {
	Preset    string `json:"preset,omitempty"`
	Intensity string `json:"intensity,omitempty"`
	Codec     string `json:"codec,omitempty"`
	Seed      int64  `json:"seed,omitempty"`
}

type ProtocolFixtureSpec struct {
	ID        string         `json:"id"`
	Pack      string         `json:"pack"`
	Version   string         `json:"version,omitempty"`
	TargetApp string         `json:"target_app,omitempty"`
	Config    map[string]any `json:"config,omitempty"`
}

type ProtocolFixtureCatalogItem struct {
	ID          string `json:"id"`
	Name        string `json:"name"`
	Description string `json:"description"`
	Version     string `json:"version"`
	Protocol    string `json:"protocol"`
	TargetApp   string `json:"target_app"`
}

type ProtocolFixtureInstance struct {
	RunID     string         `json:"run_id"`
	ID        string         `json:"id"`
	Pack      string         `json:"pack"`
	Version   string         `json:"version"`
	Protocol  string         `json:"protocol"`
	TargetApp string         `json:"target_app"`
	Status    string         `json:"status"`
	Config    map[string]any `json:"config,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
	UpdatedAt time.Time      `json:"updated_at"`
}

type ProtocolFixtureEvent struct {
	ID        int64          `json:"id"`
	RunID     string         `json:"run_id"`
	FixtureID string         `json:"fixture_id"`
	CallID    string         `json:"call_id,omitempty"`
	Type      string         `json:"type"`
	Direction string         `json:"direction,omitempty"`
	Data      map[string]any `json:"data,omitempty"`
	CreatedAt time.Time      `json:"created_at"`
}

type VoiceCall struct {
	ID                       string                      `json:"id"`
	RunID                    string                      `json:"run_id"`
	Status                   string                      `json:"status"`
	Error                    string                      `json:"error,omitempty"`
	Validity                 VoiceCallValidity           `json:"validity"`
	Spec                     VoiceFixtureSpec            `json:"spec"`
	TargetThreadID           string                      `json:"target_thread_id"`
	CallerThreadID           string                      `json:"caller_thread_id"`
	CallerAgentAlias         string                      `json:"caller_agent_alias"`
	Transcript               []VoiceTranscriptTurn       `json:"transcript"`
	Metrics                  VoiceCallMetrics            `json:"metrics"`
	TargetRecording          string                      `json:"target_recording,omitempty"`
	CallerRecording          string                      `json:"caller_recording,omitempty"`
	DeliveredCallerRecording string                      `json:"delivered_caller_recording,omitempty"`
	StartedAt                time.Time                   `json:"started_at"`
	FinishedAt               *time.Time                  `json:"finished_at,omitempty"`
	TargetTelemetry          []sdk.RuntimeTelemetryEvent `json:"target_telemetry,omitempty"`
	CallerTelemetry          []sdk.RuntimeTelemetryEvent `json:"caller_telemetry,omitempty"`
	Execution                *sdk.RuntimeAgentExecution  `json:"execution,omitempty"`
	ProtocolEvents           []ProtocolFixtureEvent      `json:"protocol_events,omitempty"`
	BridgeExits              []VoiceBridgeExitResult     `json:"bridge_exits,omitempty"`
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
	DurationMS                 int64                       `json:"duration_ms"`
	FirstResponseMS            int64                       `json:"first_response_ms,omitempty"`
	AverageResponseMS          int64                       `json:"average_response_ms,omitempty"`
	ReceptionistAudioS         float64                     `json:"receptionist_audio_seconds"`
	CallerAudioS               float64                     `json:"caller_audio_seconds"`
	DeliveredCallerAudioS      float64                     `json:"delivered_caller_audio_seconds,omitempty"`
	Interruptions              int                         `json:"interruptions"`
	ToolCalls                  int                         `json:"tool_calls"`
	RealtimeErrors             int                         `json:"realtime_errors"`
	ReceptionistRealtimeErrors int                         `json:"receptionist_realtime_errors"`
	CallerRealtimeErrors       int                         `json:"caller_realtime_errors"`
	DroppedAudioEvents         int                         `json:"dropped_audio_events"`
	EndedBy                    string                      `json:"ended_by"`
	ReceptionistSourceTurns    int                         `json:"receptionist_source_turns"`
	CallerReceivedTurns        int                         `json:"caller_received_turns"`
	PendingReceptionistTurns   int                         `json:"pending_receptionist_turns"`
	CallerSourceTurns          int                         `json:"caller_source_turns"`
	ReceptionistReceivedTurns  int                         `json:"receptionist_received_turns"`
	PendingCallerTurns         int                         `json:"pending_caller_turns"`
	CallerResponseUndelivered  bool                        `json:"caller_response_generated_not_delivered"`
	AudioConditions            *VoiceAudioConditionMetrics `json:"audio_conditions,omitempty"`
}

type VoiceBridgeExitResult struct {
	Leg                       string `json:"leg"`
	Endpoint                  string `json:"endpoint"`
	Operation                 string `json:"operation"`
	CloseCode                 int    `json:"close_code,omitempty"`
	Reason                    string `json:"reason,omitempty"`
	Error                     string `json:"error,omitempty"`
	ElapsedMS                 int64  `json:"elapsed_ms"`
	NormalClosure             bool   `json:"normal_closure"`
	TransportFailure          bool   `json:"transport_failure"`
	CallerResponseUndelivered bool   `json:"caller_response_generated_not_delivered"`
}

type VoiceAudioConditionMetrics struct {
	Preset             string  `json:"preset"`
	Intensity          string  `json:"intensity"`
	Codec              string  `json:"codec"`
	Seed               int64   `json:"seed"`
	TargetSNRDB        float64 `json:"target_snr_db,omitempty"`
	ProcessedFrames    int64   `json:"processed_frames"`
	ClippedSamples     int64   `json:"clipped_samples"`
	VADCommitSilenceMS int     `json:"vad_commit_silence_ms,omitempty"`
}

type SeedStep struct {
	App   string         `json:"app"`
	Tool  string         `json:"tool"`
	Input map[string]any `json:"input,omitempty"`
}

type AgentSpec struct {
	SourceAgentID int64                  `json:"source_agent_id,omitempty"`
	Draft         *sdk.RuntimeAgentDraft `json:"draft,omitempty"`
	Directive     string                 `json:"directive,omitempty"`
	Alias         string                 `json:"alias,omitempty"`
	StartPaused   bool                   `json:"start_paused,omitempty"`
	Provider      string                 `json:"provider,omitempty"`
	Model         string                 `json:"model,omitempty"`
}

type Definition struct {
	ID           string              `json:"id"`
	Name         string              `json:"name"`
	Description  string              `json:"description,omitempty"`
	DesiredState string              `json:"desired_state"`
	SpecVersion  int                 `json:"spec_version"`
	Spec         EnvironmentSpec     `json:"spec"`
	CreatedAt    time.Time           `json:"created_at"`
	UpdatedAt    time.Time           `json:"updated_at"`
	ActiveRun    *Run                `json:"active_run,omitempty"`
	Runtime      *sdk.RuntimeSummary `json:"runtime,omitempty"`
}

type Run struct {
	ID               string                    `json:"id"`
	EnvironmentID    string                    `json:"environment_id,omitempty"`
	RuntimeID        string                    `json:"runtime_id"`
	Kind             string                    `json:"kind"`
	Status           string                    `json:"status"`
	Error            string                    `json:"error,omitempty"`
	StartedAt        time.Time                 `json:"started_at"`
	StoppedAt        *time.Time                `json:"stopped_at,omitempty"`
	WebFixtures      []WebFixtureInstance      `json:"web_fixtures,omitempty"`
	ProtocolFixtures []ProtocolFixtureInstance `json:"protocol_fixtures,omitempty"`
}

type Snapshot struct {
	ID            string    `json:"id"`
	EnvironmentID string    `json:"environment_id,omitempty"`
	Description   string    `json:"description,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

type Assertion struct {
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
	Passed  bool   `json:"passed"`
	Actual  any    `json:"actual,omitempty"`
	Message string `json:"message,omitempty"`
}

func rawSpec(spec EnvironmentSpec) (string, error) {
	if spec.Version == 0 {
		spec.Version = 1
	}
	b, err := json.Marshal(spec)
	return string(b), err
}
