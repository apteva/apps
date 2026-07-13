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
	ID            string     `json:"id"`
	EnvironmentID string     `json:"environment_id,omitempty"`
	RuntimeID     string     `json:"runtime_id"`
	Kind          string     `json:"kind"`
	Status        string     `json:"status"`
	Error         string     `json:"error,omitempty"`
	StartedAt     time.Time  `json:"started_at"`
	StoppedAt     *time.Time `json:"stopped_at,omitempty"`
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
	Tool       string         `json:"tool,omitempty"`
	Input      map[string]any `json:"input,omitempty"`
	Path       string         `json:"path,omitempty"`
	Equals     any            `json:"equals,omitempty"`
	Method     string         `json:"method,omitempty"`
	Host       string         `json:"host,omitempty"`
	MinCalls   int            `json:"min_calls,omitempty"`
	AgentAlias string         `json:"agent_alias,omitempty"`
	EventType  string         `json:"event_type,omitempty"`
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
