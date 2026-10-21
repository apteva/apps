package sdk

import (
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"
)

// RuntimeClient is the privileged, app-facing control surface for ephemeral
// isolated execution groups. Obtain it from AppCtx.RuntimeAPI(). It is
// intentionally separate from PlatformClient so ordinary apps and their test
// stubs do not inherit this large control-plane contract.
type RuntimeClient interface {
	ListRuntimes() ([]RuntimeSummary, error)
	CreateRuntime(req RuntimeCreateRequest) (*RuntimeSummary, error)
	GetRuntime(id string) (*RuntimeSummary, error)
	DestroyRuntime(id string) error

	ListRuntimeAppTools(runtimeID, appName string) ([]RuntimeMCPTool, error)
	GetRuntimeAppEndpoint(runtimeID, appName string) (*RuntimeAppEndpoint, error)
	CallRuntimeApp(runtimeID, appName, tool string, input map[string]any) (json.RawMessage, error)
	CallRuntimeAppResult(runtimeID, appName, tool string, input map[string]any, out any) error
	CallRuntimeAppAsAgent(runtimeID, appName, agentAlias, tool string, input map[string]any) (json.RawMessage, error)
	CallRuntimeAppAsAgentResult(runtimeID, appName, agentAlias, tool string, input map[string]any, out any) error
	ListRuntimeManagedMCPTools(runtimeID, mcpName string) ([]RuntimeMCPTool, error)
	CallRuntimeManagedMCP(runtimeID, mcpName, tool string, input map[string]any) (json.RawMessage, error)
	CallRuntimeManagedMCPResult(runtimeID, mcpName, tool string, input map[string]any, out any) error

	AttachRuntimeMCP(runtimeID string, req RuntimeMCPAttachmentRequest) (*RuntimeMCPAttachment, error)
	ListRuntimeMCPAttachments(runtimeID string) ([]RuntimeMCPAttachment, error)

	ListRuntimeAgents(runtimeID string) ([]RuntimeAgent, error)
	SpawnRuntimeAgent(runtimeID string, req RuntimeAgentSpawnRequest) (*RuntimeAgent, error)
	StopRuntimeAgent(runtimeID, agentOrAlias string) error
	SendRuntimeAgentEvent(runtimeID, agentOrAlias string, req RuntimeAgentEventRequest) error
	ControlRuntimeAgent(runtimeID, agentOrAlias, action string) error
	WaitRuntimeAgent(runtimeID, agentOrAlias string, req RuntimeAgentWaitRequest) (*RuntimeAgentExecution, error)
	ListRuntimeAgentThreads(runtimeID, agentOrAlias string) (json.RawMessage, error)
	GetRuntimeAgentThread(runtimeID, agentOrAlias, threadID string) (json.RawMessage, error)
	ListRuntimeAgentTelemetry(runtimeID, agentOrAlias string, since time.Time, limit int) ([]RuntimeTelemetryEvent, error)
	SpawnRuntimeRealtimeThread(runtimeID, agentOrAlias string, req RuntimeRealtimeSpawnRequest) (*RealtimeSpawnResult, error)
	RenewRuntimeRealtimeAudioBridge(runtimeID, agentOrAlias, threadID string) (*RealtimeSpawnResult, error)
	StopRuntimeRealtimeThread(runtimeID, agentOrAlias, threadID string) error

	ListRuntimeEdgeCalls(runtimeID string) ([]RuntimeEdgeCall, error)
	GetRuntimeCassette(runtimeID string) (json.RawMessage, error)
	SnapshotRuntime(runtimeID string, req RuntimeSnapshotRequest) (*RuntimeSnapshot, error)
	ListRuntimeSnapshots() ([]RuntimeSnapshot, error)
	DeleteRuntimeSnapshot(id string) error

	ListRuntimeCatalogApps(projectID string) ([]RuntimeCatalogApp, error)
	ListRuntimeCatalogAppTools(installID int64) ([]RuntimeMCPTool, error)
	ListRuntimeCatalogManagedMCPServers(projectID string) ([]RuntimeCatalogManagedMCPServer, error)
	ListRuntimeCatalogIntegrations() ([]RuntimeCatalogIntegration, error)
	ListRuntimeCatalogIntegrationTools(slug string) ([]RuntimeCatalogIntegrationTool, error)
	ListRuntimeRealtimeProviders(projectID string) ([]RuntimeRealtimeProvider, error)
	ListRuntimeCatalogAgents(projectID string) ([]RuntimeCatalogAgent, error)
	GetRuntimeAgentCapabilities(agentID int64) ([]RuntimeAgentCapability, error)
	UpdateAgentDirective(agentID int64, req AgentDirectiveUpdateRequest) (*RuntimeCatalogAgent, error)
}

type RuntimeCreateRequest struct {
	ID                  string                      `json:"id,omitempty"`
	ProjectID           string                      `json:"project_id,omitempty"`
	TTLSeconds          int                         `json:"ttl_seconds,omitempty"`
	AppInstallIDs       []int64                     `json:"app_install_ids,omitempty"`
	ConnectionIDs       []int64                     `json:"connection_ids,omitempty"`
	MCPServerIDs        []int64                     `json:"mcp_server_ids,omitempty"`
	NetworkMode         RuntimeNetworkMode          `json:"network_mode,omitempty"`
	IntegrationMode     string                      `json:"integration_mode,omitempty"`
	AllowHostSuffixes   []string                    `json:"allow_host_suffixes,omitempty"`
	HTTPMocks           []RuntimeHTTPMock           `json:"http_mocks,omitempty"`
	IntegrationFixtures []RuntimeIntegrationMock    `json:"integration_fixtures,omitempty"`
	IntegrationBindings []RuntimeIntegrationBinding `json:"integration_bindings,omitempty"`
	ConnectionBindings  []RuntimeConnectionBinding  `json:"connection_bindings,omitempty"`
	Subscriptions       []RuntimeSubscription       `json:"subscriptions,omitempty"`
	SnapshotID          string                      `json:"snapshot_id,omitempty"`
}

type RuntimeNetworkMode string

const (
	RuntimeNetworkBlock       RuntimeNetworkMode = "block"
	RuntimeNetworkPassthrough RuntimeNetworkMode = "passthrough"
	RuntimeNetworkRecord      RuntimeNetworkMode = "record"
	RuntimeNetworkReplay      RuntimeNetworkMode = "replay"
)

type RuntimeSummary struct {
	ID              string                 `json:"id"`
	ProjectID       string                 `json:"project_id"`
	Status          string                 `json:"status"`
	NetworkMode     RuntimeNetworkMode     `json:"network_mode"`
	IntegrationMode string                 `json:"integration_mode"`
	Apps            []RuntimeApp           `json:"apps"`
	Agents          []RuntimeAgent         `json:"agents"`
	ManagedMCPs     []RuntimeManagedMCP    `json:"managed_mcps"`
	MCPAttachments  []RuntimeMCPAttachment `json:"mcp_attachments"`
	CreatedAt       time.Time              `json:"created_at"`
	ExpiresAt       time.Time              `json:"expires_at"`
}

type RuntimeApp struct {
	Name      string `json:"name"`
	InstallID int64  `json:"install_id,omitempty"`
	Kind      string `json:"kind"`
	Status    string `json:"status"`
}

// RuntimeAppEndpoint is a runtime-scoped URL for exercising an app's regular
// HTTP and WebSocket routes. PlatformURL is the base URL apps in that runtime
// receive from WhoAmI; AppURL addresses this specific temporary install.
type RuntimeAppEndpoint struct {
	PlatformURL string `json:"platform_url"`
	GatewayURL  string `json:"gateway_url"`
	AppURL      string `json:"app_url"`
}

type RuntimeMCPTool struct {
	Name        string         `json:"name"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema"`
}

type RuntimeManagedMCP struct {
	SourceID    int64  `json:"source_id"`
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Revision    string `json:"revision,omitempty"`
	Status      string `json:"status"`
	ToolCount   int    `json:"tool_count"`
}

type RuntimeMCPAttachmentRequest struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

type RuntimeMCPAttachment struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Path string `json:"path,omitempty"`
}

type RuntimeAgentDraft struct {
	Name      string `json:"name"`
	Directive string `json:"directive"`
	Mode      string `json:"mode,omitempty"`
	Config    string `json:"config,omitempty"`
}

type RuntimeAgentSpawnRequest struct {
	SourceAgentID int64              `json:"source_agent_id,omitempty"`
	Draft         *RuntimeAgentDraft `json:"draft,omitempty"`
	Directive     string             `json:"directive,omitempty"`
	Alias         string             `json:"alias,omitempty"`
	StartPaused   bool               `json:"start_paused,omitempty"`
	Provider      string             `json:"provider,omitempty"`
	Model         string             `json:"model,omitempty"`
}

type RuntimeAgent struct {
	ID            int64     `json:"id"`
	SourceAgentID int64     `json:"source_agent_id,omitempty"`
	SourceName    string    `json:"source_name,omitempty"`
	Alias         string    `json:"alias"`
	Status        string    `json:"status"`
	Provider      string    `json:"provider,omitempty"`
	Model         string    `json:"model,omitempty"`
	CreatedAt     time.Time `json:"created_at"`
}

type RuntimeAgentEventRequest struct {
	Message  string `json:"message"`
	ThreadID string `json:"thread_id,omitempty"`
}

// RuntimeRealtimeSpawnRequest starts a voice/audio sub-thread inside a runtime
// agent. The runtime and target agent are selected by the method arguments, so
// this request never accepts a global agent id.
type RuntimeRealtimeSpawnRequest struct {
	ThreadID                   string                 `json:"thread_id"`
	Directive                  string                 `json:"directive"`
	Voice                      string                 `json:"voice,omitempty"`
	Provider                   string                 `json:"provider,omitempty"`
	CapabilityMode             RealtimeCapabilityMode `json:"capability_mode,omitempty"`
	Tools                      []string               `json:"tools,omitempty"`
	MCP                        []string               `json:"mcp,omitempty"`
	CallContext                *RealtimeCallContext   `json:"call_context,omitempty"`
	TurnDetection              *RealtimeTurnDetection `json:"turn_detection,omitempty"`
	Ephemeral                  bool                   `json:"ephemeral,omitempty"`
	InitialMessage             string                 `json:"initial_message,omitempty"`
	BridgeDisconnectTTLSeconds int                    `json:"bridge_disconnect_ttl_seconds,omitempty"`
}

// RuntimeAgentWaitRequest controls completion detection for one execution.
// The runtime remains alive after this call so callers can inspect app state.
type RuntimeAgentWaitRequest struct {
	ThreadID            string `json:"thread_id,omitempty"`
	TimeoutSeconds      int    `json:"timeout_seconds,omitempty"`
	IdleSeconds         int    `json:"idle_seconds,omitempty"`
	PostToolIdleSeconds int    `json:"post_tool_idle_seconds,omitempty"`
	MaxTurns            int    `json:"max_turns,omitempty"`
	RequireActivity     bool   `json:"require_activity,omitempty"`
}

type RuntimeAgentExecution struct {
	Status     string              `json:"status"`
	Reason     string              `json:"reason,omitempty"`
	ThreadID   string              `json:"thread_id"`
	Turns      int                 `json:"turns"`
	StartedAt  time.Time           `json:"started_at"`
	FinishedAt time.Time           `json:"finished_at"`
	Trace      []RuntimeTraceEvent `json:"trace"`
	Metrics    RuntimeAgentMetrics `json:"metrics"`
}

type RuntimeTraceEvent struct {
	Index    int              `json:"index"`
	ThreadID string           `json:"thread_id"`
	Role     string           `json:"role"`
	Content  string           `json:"content,omitempty"`
	ToolCall *RuntimeToolCall `json:"tool_call,omitempty"`
}

type RuntimeToolCall struct {
	ID      string          `json:"id,omitempty"`
	Name    string          `json:"name"`
	Input   json.RawMessage `json:"input,omitempty"`
	Output  string          `json:"output,omitempty"`
	IsError bool            `json:"is_error,omitempty"`
}

type RuntimeAgentMetrics struct {
	Provider      string  `json:"provider,omitempty"`
	Model         string  `json:"model,omitempty"`
	LLMCalls      int     `json:"llm_calls"`
	TokensIn      int     `json:"tokens_in"`
	TokensOut     int     `json:"tokens_out"`
	TokensCached  int     `json:"tokens_cached"`
	CostUSD       float64 `json:"cost_usd"`
	LLMDurationMS int     `json:"llm_duration_ms"`
	ToolCalls     int     `json:"tool_calls"`
	Errors        int     `json:"errors"`
}

type RuntimeTelemetryEvent = TelemetryStreamEvent

type RuntimeEdgeCall struct {
	Method   string `json:"method"`
	Host     string `json:"host"`
	Path     string `json:"path"`
	Status   int    `json:"status"`
	Allowed  bool   `json:"allowed,omitempty"`
	Mocked   bool   `json:"mocked,omitempty"`
	Blocked  bool   `json:"blocked,omitempty"`
	Recorded bool   `json:"recorded,omitempty"`
}

type RuntimeHTTPMock struct {
	Host    string            `json:"host"`
	Path    string            `json:"path"`
	Method  string            `json:"method"`
	Status  int               `json:"status,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    json.RawMessage   `json:"body,omitempty"`
}

type RuntimeIntegrationMock struct {
	App    string `json:"app"`
	Tool   string `json:"tool"`
	Status int    `json:"status,omitempty"`
	Data   any    `json:"data,omitempty"`
}

// RuntimeIntegrationBinding creates a runtime-scoped fake connection and
// binds it to one cloned app role. Credentials never affect the source install
// and the connection row is deleted with the runtime.
type RuntimeIntegrationBinding struct {
	App            string            `json:"app"`
	Role           string            `json:"role"`
	Slug           string            `json:"slug"`
	AppName        string            `json:"app_name,omitempty"`
	Name           string            `json:"name,omitempty"`
	AuthType       string            `json:"auth_type,omitempty"`
	Credentials    map[string]string `json:"credentials,omitempty"`
	ExposeToAgents bool              `json:"expose_to_agents,omitempty"`
}

// RuntimeConnectionBinding binds an existing project connection to one role
// on a cloned runtime app. Only the connection ID crosses the runtime API;
// credentials remain in the platform connection store.
type RuntimeConnectionBinding struct {
	App          string `json:"app"`
	Role         string `json:"role"`
	ConnectionID int64  `json:"connection_id"`
}

type RuntimeSubscription struct {
	ID               string `json:"id,omitempty"`
	Source           string `json:"source,omitempty"`
	App              string `json:"app"`
	Topic            string `json:"topic"`
	TargetAgentAlias string `json:"target_agent_alias,omitempty"`
	ThreadID         string `json:"thread_id,omitempty"`
	Name             string `json:"name,omitempty"`
	Description      string `json:"description,omitempty"`
	Enabled          bool   `json:"enabled"`
}

type RuntimeSnapshotRequest struct {
	ID          string `json:"id,omitempty"`
	Description string `json:"description,omitempty"`
}

type RuntimeSnapshot struct {
	ID          string              `json:"id"`
	ProjectID   string              `json:"project_id"`
	Description string              `json:"description,omitempty"`
	Apps        []string            `json:"apps"`
	ManagedMCPs []RuntimeManagedMCP `json:"managed_mcps,omitempty"`
	HasAgent    bool                `json:"has_agent"`
	HasCassette bool                `json:"has_cassette"`
	CreatedAt   time.Time           `json:"created_at"`
}

type RuntimeCatalogManagedMCPServer struct {
	ID           int64    `json:"id"`
	Name         string   `json:"name"`
	Description  string   `json:"description,omitempty"`
	Status       string   `json:"status"`
	ToolCount    int      `json:"tool_count"`
	AllowedTools []string `json:"allowed_tools,omitempty"`
	Revision     string   `json:"revision,omitempty"`
}

type RuntimeCatalogApp struct {
	InstallID        int64            `json:"install_id"`
	Name             string           `json:"name"`
	DisplayName      string           `json:"display_name,omitempty"`
	Description      string           `json:"description,omitempty"`
	Icon             string           `json:"icon,omitempty"`
	IconStyle        string           `json:"icon_style,omitempty"`
	ProjectID        string           `json:"project_id,omitempty"`
	Status           string           `json:"status"`
	IntegrationRoles []IntegrationDep `json:"integration_roles,omitempty"`
	Publishes        []EventDecl      `json:"publishes,omitempty"`
}

type RuntimeCatalogIntegration struct {
	Slug        string   `json:"slug"`
	Name        string   `json:"name"`
	Description string   `json:"description,omitempty"`
	Logo        string   `json:"logo,omitempty"`
	Categories  []string `json:"categories,omitempty"`
	ToolCount   int      `json:"tool_count"`
	Kind        string   `json:"kind,omitempty"`
}

type RuntimeCatalogIntegrationTool struct {
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	InputSchema  map[string]any  `json:"inputSchema"`
	MockResponse json.RawMessage `json:"mock_response,omitempty"`
}

type RuntimeRealtimeProvider struct {
	Name         string            `json:"name"`
	Models       map[string]string `json:"models"`
	DefaultVoice string            `json:"default_voice,omitempty"`
}

type RuntimeCatalogAgent struct {
	ID            int64  `json:"id"`
	Name          string `json:"name"`
	Directive     string `json:"directive,omitempty"`
	DirectiveETag string `json:"directive_etag,omitempty"`
	Mode          string `json:"mode"`
	Status        string `json:"status"`
	ProjectID     string `json:"project_id"`
}

type RuntimeAgentCapability struct {
	AppName   string           `json:"app_name"`
	InstallID int64            `json:"install_id"`
	Tools     []RuntimeMCPTool `json:"tools"`
}

type AgentDirectiveUpdateRequest struct {
	Directive    string `json:"directive"`
	ExpectedETag string `json:"expected_etag"`
	Reason       string `json:"reason,omitempty"`
}

func (c *httpPlatformClient) ListRuntimes() ([]RuntimeSummary, error) {
	var out []RuntimeSummary
	err := c.get("/api/apps/callback/runtimes", &out)
	return out, err
}

func (c *httpPlatformClient) CreateRuntime(req RuntimeCreateRequest) (*RuntimeSummary, error) {
	var out RuntimeSummary
	if err := c.postWith(c.slowClient, "/api/apps/callback/runtimes", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpPlatformClient) GetRuntime(id string) (*RuntimeSummary, error) {
	var out RuntimeSummary
	if err := c.get(runtimePath(id), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpPlatformClient) DestroyRuntime(id string) error {
	return c.delete(runtimePath(id), nil)
}

func (c *httpPlatformClient) ListRuntimeAppTools(runtimeID, appName string) ([]RuntimeMCPTool, error) {
	var out []RuntimeMCPTool
	err := c.get(runtimePath(runtimeID)+"/apps/"+url.PathEscape(appName)+"/tools", &out)
	return out, err
}

func (c *httpPlatformClient) GetRuntimeAppEndpoint(runtimeID, appName string) (*RuntimeAppEndpoint, error) {
	var out RuntimeAppEndpoint
	if err := c.get(runtimePath(runtimeID)+"/apps/"+url.PathEscape(appName)+"/endpoint", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpPlatformClient) CallRuntimeApp(runtimeID, appName, tool string, input map[string]any) (json.RawMessage, error) {
	return c.callRuntimeApp(runtimeID, appName, "", tool, input)
}

func (c *httpPlatformClient) CallRuntimeAppAsAgent(runtimeID, appName, agentAlias, tool string, input map[string]any) (json.RawMessage, error) {
	return c.callRuntimeApp(runtimeID, appName, agentAlias, tool, input)
}

func (c *httpPlatformClient) callRuntimeApp(runtimeID, appName, agentAlias, tool string, input map[string]any) (json.RawMessage, error) {
	var out struct {
		Result json.RawMessage `json:"result"`
	}
	body := map[string]any{"tool": tool, "input": input}
	if strings.TrimSpace(agentAlias) != "" {
		body["agent_alias"] = agentAlias
	}
	err := c.postWith(c.slowClient, runtimePath(runtimeID)+"/apps/"+url.PathEscape(appName)+"/call", body, &out)
	return out.Result, err
}

func (c *httpPlatformClient) CallRuntimeAppResult(runtimeID, appName, tool string, input map[string]any, out any) error {
	raw, err := c.CallRuntimeApp(runtimeID, appName, tool, input)
	if err != nil {
		return err
	}
	return decodeMCPEnvelope(raw, appName, tool, out)
}

func (c *httpPlatformClient) CallRuntimeAppAsAgentResult(runtimeID, appName, agentAlias, tool string, input map[string]any, out any) error {
	raw, err := c.CallRuntimeAppAsAgent(runtimeID, appName, agentAlias, tool, input)
	if err != nil {
		return err
	}
	return decodeMCPEnvelope(raw, appName, tool, out)
}

func (c *httpPlatformClient) ListRuntimeManagedMCPTools(runtimeID, mcpName string) ([]RuntimeMCPTool, error) {
	var out []RuntimeMCPTool
	err := c.get(runtimePath(runtimeID)+"/managed-mcps/"+url.PathEscape(mcpName)+"/tools", &out)
	return out, err
}

func (c *httpPlatformClient) CallRuntimeManagedMCP(runtimeID, mcpName, tool string, input map[string]any) (json.RawMessage, error) {
	var out struct {
		Result json.RawMessage `json:"result"`
	}
	err := c.postWith(c.slowClient, runtimePath(runtimeID)+"/managed-mcps/"+url.PathEscape(mcpName)+"/call", map[string]any{"tool": tool, "input": input}, &out)
	return out.Result, err
}

func (c *httpPlatformClient) CallRuntimeManagedMCPResult(runtimeID, mcpName, tool string, input map[string]any, out any) error {
	raw, err := c.CallRuntimeManagedMCP(runtimeID, mcpName, tool, input)
	if err != nil {
		return err
	}
	return decodeMCPEnvelope(raw, mcpName, tool, out)
}

func (c *httpPlatformClient) AttachRuntimeMCP(runtimeID string, req RuntimeMCPAttachmentRequest) (*RuntimeMCPAttachment, error) {
	var out RuntimeMCPAttachment
	if err := c.post(runtimePath(runtimeID)+"/mcp-attachments", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpPlatformClient) ListRuntimeMCPAttachments(runtimeID string) ([]RuntimeMCPAttachment, error) {
	var out []RuntimeMCPAttachment
	err := c.get(runtimePath(runtimeID)+"/mcp-attachments", &out)
	return out, err
}

func (c *httpPlatformClient) ListRuntimeAgents(runtimeID string) ([]RuntimeAgent, error) {
	var out []RuntimeAgent
	err := c.get(runtimePath(runtimeID)+"/agents", &out)
	return out, err
}

func (c *httpPlatformClient) SpawnRuntimeAgent(runtimeID string, req RuntimeAgentSpawnRequest) (*RuntimeAgent, error) {
	var out RuntimeAgent
	if err := c.postWith(c.slowClient, runtimePath(runtimeID)+"/agents", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpPlatformClient) StopRuntimeAgent(runtimeID, agentOrAlias string) error {
	return c.delete(runtimePath(runtimeID)+"/agents/"+url.PathEscape(agentOrAlias), nil)
}

func (c *httpPlatformClient) SendRuntimeAgentEvent(runtimeID, agentOrAlias string, req RuntimeAgentEventRequest) error {
	return c.post(runtimeAgentPath(runtimeID, agentOrAlias)+"/event", req, nil)
}

func (c *httpPlatformClient) ControlRuntimeAgent(runtimeID, agentOrAlias, action string) error {
	return c.post(runtimeAgentPath(runtimeID, agentOrAlias)+"/control", map[string]string{"action": action}, nil)
}

func (c *httpPlatformClient) WaitRuntimeAgent(runtimeID, agentOrAlias string, req RuntimeAgentWaitRequest) (*RuntimeAgentExecution, error) {
	var out RuntimeAgentExecution
	if err := c.postWith(c.slowClient, runtimeAgentPath(runtimeID, agentOrAlias)+"/wait", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpPlatformClient) ListRuntimeAgentThreads(runtimeID, agentOrAlias string) (json.RawMessage, error) {
	return c.getRaw(runtimeAgentPath(runtimeID, agentOrAlias) + "/threads")
}

func (c *httpPlatformClient) GetRuntimeAgentThread(runtimeID, agentOrAlias, threadID string) (json.RawMessage, error) {
	return c.getRaw(runtimeAgentPath(runtimeID, agentOrAlias) + "/threads/" + url.PathEscape(threadID))
}

func (c *httpPlatformClient) ListRuntimeAgentTelemetry(runtimeID, agentOrAlias string, since time.Time, limit int) ([]RuntimeTelemetryEvent, error) {
	q := "?limit=" + strconv.Itoa(limit)
	if !since.IsZero() {
		q += "&since=" + url.QueryEscape(since.UTC().Format(time.RFC3339Nano))
	}
	var out []RuntimeTelemetryEvent
	err := c.get(runtimeAgentPath(runtimeID, agentOrAlias)+"/telemetry"+q, &out)
	return out, err
}

func (c *httpPlatformClient) SpawnRuntimeRealtimeThread(runtimeID, agentOrAlias string, req RuntimeRealtimeSpawnRequest) (*RealtimeSpawnResult, error) {
	if err := validateRealtimeCapabilityMode(req.CapabilityMode, req.Tools, req.MCP); err != nil {
		return nil, fmt.Errorf("SpawnRuntimeRealtimeThread: %w", err)
	}
	var out RealtimeSpawnResult
	if err := c.postWith(c.slowClient, runtimeAgentPath(runtimeID, agentOrAlias)+"/realtime", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpPlatformClient) RenewRuntimeRealtimeAudioBridge(runtimeID, agentOrAlias, threadID string) (*RealtimeSpawnResult, error) {
	var out RealtimeSpawnResult
	path := runtimeAgentPath(runtimeID, agentOrAlias) + "/realtime/" + url.PathEscape(strings.TrimSpace(threadID)) + "/audio-token"
	if err := c.post(path, map[string]any{}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpPlatformClient) StopRuntimeRealtimeThread(runtimeID, agentOrAlias, threadID string) error {
	path := runtimeAgentPath(runtimeID, agentOrAlias) + "/realtime/" + url.PathEscape(strings.TrimSpace(threadID))
	return c.delete(path, nil)
}

func (c *httpPlatformClient) ListRuntimeEdgeCalls(runtimeID string) ([]RuntimeEdgeCall, error) {
	var out []RuntimeEdgeCall
	err := c.get(runtimePath(runtimeID)+"/edge/calls", &out)
	return out, err
}

func (c *httpPlatformClient) GetRuntimeCassette(runtimeID string) (json.RawMessage, error) {
	return c.getRaw(runtimePath(runtimeID) + "/edge/cassette")
}

func (c *httpPlatformClient) SnapshotRuntime(runtimeID string, req RuntimeSnapshotRequest) (*RuntimeSnapshot, error) {
	var out RuntimeSnapshot
	if err := c.postWith(c.slowClient, runtimePath(runtimeID)+"/snapshots", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpPlatformClient) ListRuntimeSnapshots() ([]RuntimeSnapshot, error) {
	var out []RuntimeSnapshot
	err := c.get("/api/apps/callback/runtimes/artifacts/snapshots", &out)
	return out, err
}

func (c *httpPlatformClient) DeleteRuntimeSnapshot(id string) error {
	return c.delete("/api/apps/callback/runtimes/artifacts/snapshots/"+url.PathEscape(strings.TrimSpace(id)), nil)
}

func (c *httpPlatformClient) ListRuntimeCatalogApps(projectID string) ([]RuntimeCatalogApp, error) {
	var out []RuntimeCatalogApp
	err := c.get("/api/apps/callback/runtimes/catalog/apps?project_id="+url.QueryEscape(projectID), &out)
	return out, err
}

func (c *httpPlatformClient) ListRuntimeCatalogAppTools(installID int64) ([]RuntimeMCPTool, error) {
	var out []RuntimeMCPTool
	err := c.get("/api/apps/callback/runtimes/catalog/apps/"+strconv.FormatInt(installID, 10)+"/tools", &out)
	return out, err
}

func (c *httpPlatformClient) ListRuntimeCatalogManagedMCPServers(projectID string) ([]RuntimeCatalogManagedMCPServer, error) {
	var out []RuntimeCatalogManagedMCPServer
	err := c.get("/api/apps/callback/runtimes/catalog/managed-mcps?project_id="+url.QueryEscape(projectID), &out)
	return out, err
}

func (c *httpPlatformClient) ListRuntimeCatalogIntegrations() ([]RuntimeCatalogIntegration, error) {
	var out []RuntimeCatalogIntegration
	err := c.get("/api/apps/callback/runtimes/catalog/integrations", &out)
	return out, err
}

func (c *httpPlatformClient) ListRuntimeCatalogIntegrationTools(slug string) ([]RuntimeCatalogIntegrationTool, error) {
	var out []RuntimeCatalogIntegrationTool
	err := c.get("/api/apps/callback/runtimes/catalog/integrations/"+url.PathEscape(strings.TrimSpace(slug))+"/tools", &out)
	return out, err
}

func (c *httpPlatformClient) ListRuntimeRealtimeProviders(projectID string) ([]RuntimeRealtimeProvider, error) {
	var out []RuntimeRealtimeProvider
	err := c.get("/api/apps/callback/runtimes/catalog/realtime-providers?project_id="+url.QueryEscape(projectID), &out)
	return out, err
}

func (c *httpPlatformClient) ListRuntimeCatalogAgents(projectID string) ([]RuntimeCatalogAgent, error) {
	var out []RuntimeCatalogAgent
	err := c.get("/api/apps/callback/runtimes/catalog/agents?project_id="+url.QueryEscape(projectID), &out)
	return out, err
}

func (c *httpPlatformClient) GetRuntimeAgentCapabilities(agentID int64) ([]RuntimeAgentCapability, error) {
	var out []RuntimeAgentCapability
	err := c.get("/api/apps/callback/runtimes/catalog/agents/"+strconv.FormatInt(agentID, 10)+"/capabilities", &out)
	return out, err
}

func (c *httpPlatformClient) UpdateAgentDirective(agentID int64, req AgentDirectiveUpdateRequest) (*RuntimeCatalogAgent, error) {
	var out RuntimeCatalogAgent
	if err := c.put("/api/apps/callback/runtimes/catalog/agents/"+strconv.FormatInt(agentID, 10)+"/directive", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func runtimePath(id string) string {
	return "/api/apps/callback/runtimes/" + url.PathEscape(strings.TrimSpace(id))
}

func runtimeAgentPath(runtimeID, agent string) string {
	return runtimePath(runtimeID) + "/agents/" + url.PathEscape(strings.TrimSpace(agent))
}

func (c *httpPlatformClient) getRaw(path string) (json.RawMessage, error) {
	var out json.RawMessage
	if err := c.get(path, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (p *projectScopedClient) runtime() RuntimeClient {
	r, _ := p.inner.(RuntimeClient)
	return r
}

func (p *projectScopedClient) ListRuntimes() ([]RuntimeSummary, error) {
	return p.runtime().ListRuntimes()
}
func (p *projectScopedClient) CreateRuntime(req RuntimeCreateRequest) (*RuntimeSummary, error) {
	if req.ProjectID == "" {
		req.ProjectID = p.projectID
	}
	return p.runtime().CreateRuntime(req)
}
func (p *projectScopedClient) GetRuntime(id string) (*RuntimeSummary, error) {
	return p.runtime().GetRuntime(id)
}
func (p *projectScopedClient) DestroyRuntime(id string) error { return p.runtime().DestroyRuntime(id) }
func (p *projectScopedClient) ListRuntimeAppTools(r, a string) ([]RuntimeMCPTool, error) {
	return p.runtime().ListRuntimeAppTools(r, a)
}
func (p *projectScopedClient) GetRuntimeAppEndpoint(r, a string) (*RuntimeAppEndpoint, error) {
	return p.runtime().GetRuntimeAppEndpoint(r, a)
}
func (p *projectScopedClient) CallRuntimeApp(r, a, t string, in map[string]any) (json.RawMessage, error) {
	return p.runtime().CallRuntimeApp(r, a, t, in)
}
func (p *projectScopedClient) CallRuntimeAppResult(r, a, t string, in map[string]any, out any) error {
	return p.runtime().CallRuntimeAppResult(r, a, t, in, out)
}
func (p *projectScopedClient) CallRuntimeAppAsAgent(r, a, alias, t string, in map[string]any) (json.RawMessage, error) {
	return p.runtime().CallRuntimeAppAsAgent(r, a, alias, t, in)
}
func (p *projectScopedClient) CallRuntimeAppAsAgentResult(r, a, alias, t string, in map[string]any, out any) error {
	return p.runtime().CallRuntimeAppAsAgentResult(r, a, alias, t, in, out)
}
func (p *projectScopedClient) ListRuntimeManagedMCPTools(r, m string) ([]RuntimeMCPTool, error) {
	return p.runtime().ListRuntimeManagedMCPTools(r, m)
}
func (p *projectScopedClient) CallRuntimeManagedMCP(r, m, t string, in map[string]any) (json.RawMessage, error) {
	return p.runtime().CallRuntimeManagedMCP(r, m, t, in)
}
func (p *projectScopedClient) CallRuntimeManagedMCPResult(r, m, t string, in map[string]any, out any) error {
	return p.runtime().CallRuntimeManagedMCPResult(r, m, t, in, out)
}
func (p *projectScopedClient) AttachRuntimeMCP(r string, req RuntimeMCPAttachmentRequest) (*RuntimeMCPAttachment, error) {
	return p.runtime().AttachRuntimeMCP(r, req)
}
func (p *projectScopedClient) ListRuntimeMCPAttachments(r string) ([]RuntimeMCPAttachment, error) {
	return p.runtime().ListRuntimeMCPAttachments(r)
}
func (p *projectScopedClient) ListRuntimeAgents(r string) ([]RuntimeAgent, error) {
	return p.runtime().ListRuntimeAgents(r)
}
func (p *projectScopedClient) SpawnRuntimeAgent(r string, req RuntimeAgentSpawnRequest) (*RuntimeAgent, error) {
	return p.runtime().SpawnRuntimeAgent(r, req)
}
func (p *projectScopedClient) StopRuntimeAgent(r, a string) error {
	return p.runtime().StopRuntimeAgent(r, a)
}
func (p *projectScopedClient) SendRuntimeAgentEvent(r, a string, req RuntimeAgentEventRequest) error {
	return p.runtime().SendRuntimeAgentEvent(r, a, req)
}
func (p *projectScopedClient) ControlRuntimeAgent(r, a, action string) error {
	return p.runtime().ControlRuntimeAgent(r, a, action)
}
func (p *projectScopedClient) WaitRuntimeAgent(r, a string, req RuntimeAgentWaitRequest) (*RuntimeAgentExecution, error) {
	return p.runtime().WaitRuntimeAgent(r, a, req)
}
func (p *projectScopedClient) ListRuntimeAgentThreads(r, a string) (json.RawMessage, error) {
	return p.runtime().ListRuntimeAgentThreads(r, a)
}
func (p *projectScopedClient) GetRuntimeAgentThread(r, a, t string) (json.RawMessage, error) {
	return p.runtime().GetRuntimeAgentThread(r, a, t)
}
func (p *projectScopedClient) ListRuntimeAgentTelemetry(r, a string, since time.Time, limit int) ([]RuntimeTelemetryEvent, error) {
	return p.runtime().ListRuntimeAgentTelemetry(r, a, since, limit)
}
func (p *projectScopedClient) SpawnRuntimeRealtimeThread(r, a string, req RuntimeRealtimeSpawnRequest) (*RealtimeSpawnResult, error) {
	return p.runtime().SpawnRuntimeRealtimeThread(r, a, req)
}
func (p *projectScopedClient) RenewRuntimeRealtimeAudioBridge(r, a, t string) (*RealtimeSpawnResult, error) {
	return p.runtime().RenewRuntimeRealtimeAudioBridge(r, a, t)
}
func (p *projectScopedClient) StopRuntimeRealtimeThread(r, a, t string) error {
	return p.runtime().StopRuntimeRealtimeThread(r, a, t)
}
func (p *projectScopedClient) ListRuntimeEdgeCalls(r string) ([]RuntimeEdgeCall, error) {
	return p.runtime().ListRuntimeEdgeCalls(r)
}
func (p *projectScopedClient) GetRuntimeCassette(r string) (json.RawMessage, error) {
	return p.runtime().GetRuntimeCassette(r)
}
func (p *projectScopedClient) SnapshotRuntime(r string, req RuntimeSnapshotRequest) (*RuntimeSnapshot, error) {
	return p.runtime().SnapshotRuntime(r, req)
}
func (p *projectScopedClient) ListRuntimeSnapshots() ([]RuntimeSnapshot, error) {
	return p.runtime().ListRuntimeSnapshots()
}
func (p *projectScopedClient) DeleteRuntimeSnapshot(id string) error {
	return p.runtime().DeleteRuntimeSnapshot(id)
}
func (p *projectScopedClient) ListRuntimeCatalogApps(projectID string) ([]RuntimeCatalogApp, error) {
	if projectID == "" {
		projectID = p.projectID
	}
	return p.runtime().ListRuntimeCatalogApps(projectID)
}
func (p *projectScopedClient) ListRuntimeCatalogAppTools(installID int64) ([]RuntimeMCPTool, error) {
	return p.runtime().ListRuntimeCatalogAppTools(installID)
}
func (p *projectScopedClient) ListRuntimeCatalogManagedMCPServers(projectID string) ([]RuntimeCatalogManagedMCPServer, error) {
	if projectID == "" {
		projectID = p.projectID
	}
	return p.runtime().ListRuntimeCatalogManagedMCPServers(projectID)
}
func (p *projectScopedClient) ListRuntimeCatalogIntegrations() ([]RuntimeCatalogIntegration, error) {
	return p.runtime().ListRuntimeCatalogIntegrations()
}
func (p *projectScopedClient) ListRuntimeCatalogIntegrationTools(slug string) ([]RuntimeCatalogIntegrationTool, error) {
	return p.runtime().ListRuntimeCatalogIntegrationTools(slug)
}
func (p *projectScopedClient) ListRuntimeRealtimeProviders(projectID string) ([]RuntimeRealtimeProvider, error) {
	if projectID == "" {
		projectID = p.projectID
	}
	return p.runtime().ListRuntimeRealtimeProviders(projectID)
}
func (p *projectScopedClient) ListRuntimeCatalogAgents(projectID string) ([]RuntimeCatalogAgent, error) {
	if projectID == "" {
		projectID = p.projectID
	}
	return p.runtime().ListRuntimeCatalogAgents(projectID)
}
func (p *projectScopedClient) GetRuntimeAgentCapabilities(id int64) ([]RuntimeAgentCapability, error) {
	return p.runtime().GetRuntimeAgentCapabilities(id)
}
func (p *projectScopedClient) UpdateAgentDirective(id int64, req AgentDirectiveUpdateRequest) (*RuntimeCatalogAgent, error) {
	return p.runtime().UpdateAgentDirective(id, req)
}

var _ RuntimeClient = (*httpPlatformClient)(nil)
var _ RuntimeClient = (*projectScopedClient)(nil)
