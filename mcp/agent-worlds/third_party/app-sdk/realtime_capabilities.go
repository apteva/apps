package sdk

import (
	"errors"
	"net/url"
	"strconv"
	"strings"
)

// RealtimeCapabilities separates stored access grants from registry/connection
// state and the configuration acknowledged by the active realtime provider.
// Nil lists mean unknown; non-nil empty lists mean known empty. Core owns this
// versioned snapshot; server must not infer presentation from grants.
type RealtimeCapabilities struct {
	Version                  int      `json:"version"`
	ThreadID                 string   `json:"id"`
	Status                   string   `json:"status"` // unknown, pending, ready, failed
	GrantsVerified           bool     `json:"grants_verified"`
	GrantedTools             []string `json:"granted_tools"`
	GrantedMCP               []string `json:"granted_mcp"`
	ConnectedMCP             []string `json:"connected_mcp"`
	RegisteredTools          []string `json:"registered_tools"`
	PresentedTools           []string `json:"presented_tools"`
	PresentationVerified     bool     `json:"presentation_verified"`
	SessionGeneration        int64    `json:"session_generation"`
	AppliedSessionGeneration int64    `json:"applied_session_generation"`
	ConfigurationRevision    int64    `json:"configuration_revision"`
	AppliedRevision          int64    `json:"applied_revision"`
	Error                    string   `json:"error,omitempty"`
}

// Verified requires evidence for the current session and configuration. It
// confirms tool presentation, not that an app's required tools are present.
func (c *RealtimeCapabilities) Verified() bool {
	return c != nil && c.Version == 1 && c.ThreadID != "" && c.Status == "ready" && c.Error == "" &&
		c.GrantsVerified && c.GrantedTools != nil && c.GrantedMCP != nil &&
		c.ConnectedMCP != nil && c.RegisteredTools != nil && c.PresentedTools != nil &&
		c.PresentationVerified && c.SessionGeneration > 0 && c.AppliedSessionGeneration == c.SessionGeneration && c.ConfigurationRevision > 0 &&
		c.AppliedRevision == c.ConfigurationRevision
}

// MissingPresentedTools checks exact provider-visible names (including any MCP
// namespace). Unknown/pending evidence returns an error, never a healthy empty
// missing list. Apps decide which tools are required for their own workflows.
func (c *RealtimeCapabilities) MissingPresentedTools(required ...string) ([]string, error) {
	if !c.Verified() {
		return nil, errors.New("realtime tool presentation is not verified")
	}
	present := make(map[string]bool, len(c.PresentedTools))
	for _, name := range c.PresentedTools {
		present[name] = true
	}
	missing := []string{}
	seen := map[string]bool{}
	for _, name := range required {
		if strings.TrimSpace(name) == "" {
			return nil, errors.New("required tool name cannot be empty")
		}
		if !present[name] && !seen[name] {
			missing = append(missing, name)
			seen[name] = true
		}
	}
	return missing, nil
}

// Optional extension: existing PlatformClient implementations remain source
// compatible. Use GetRealtimeThreadCapabilities with ctx.PlatformAPI().
type RealtimeCapabilitiesClient interface {
	GetRealtimeThreadCapabilities(agentID int64, threadID string) (*RealtimeCapabilities, error)
}

func GetRealtimeThreadCapabilities(client PlatformClient, agentID int64, threadID string) (*RealtimeCapabilities, error) {
	reader, ok := client.(RealtimeCapabilitiesClient)
	if !ok {
		return nil, errors.New("realtime capability API unavailable")
	}
	return reader.GetRealtimeThreadCapabilities(agentID, threadID)
}

func (c *httpPlatformClient) GetRealtimeThreadCapabilities(agentID int64, threadID string) (*RealtimeCapabilities, error) {
	if agentID <= 0 || strings.TrimSpace(threadID) == "" || strings.Contains(threadID, "/") {
		return nil, errors.New("valid agent_id and thread_id required")
	}
	var out RealtimeCapabilities
	path := "/api/apps/callback/threads/" + url.PathEscape(threadID) + "/capabilities?agent_id=" + strconv.FormatInt(agentID, 10)
	if err := c.get(path, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (p *projectScopedClient) GetRealtimeThreadCapabilities(agentID int64, threadID string) (*RealtimeCapabilities, error) {
	return GetRealtimeThreadCapabilities(p.inner, agentID, threadID)
}

// RuntimeRealtimeCapabilitiesClient is the optional runtime-scoped read API.
type RuntimeRealtimeCapabilitiesClient interface {
	GetRuntimeRealtimeThreadCapabilities(runtimeID, agentOrAlias, threadID string) (*RealtimeCapabilities, error)
}

func (c *httpPlatformClient) GetRuntimeRealtimeThreadCapabilities(runtimeID, agentOrAlias, threadID string) (*RealtimeCapabilities, error) {
	if strings.TrimSpace(runtimeID) == "" || strings.TrimSpace(agentOrAlias) == "" || strings.TrimSpace(threadID) == "" || strings.Contains(threadID, "/") {
		return nil, errors.New("runtime, agent and thread required")
	}
	var out RealtimeCapabilities
	path := "/api/apps/callback/runtimes/" + url.PathEscape(runtimeID) + "/agents/" + url.PathEscape(agentOrAlias) + "/realtime/" + url.PathEscape(threadID) + "/capabilities"
	if err := c.get(path, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (p *projectScopedClient) GetRuntimeRealtimeThreadCapabilities(runtimeID, agentOrAlias, threadID string) (*RealtimeCapabilities, error) {
	reader, ok := p.inner.(RuntimeRealtimeCapabilitiesClient)
	if !ok {
		return nil, errors.New("runtime realtime capability API unavailable")
	}
	return reader.GetRuntimeRealtimeThreadCapabilities(runtimeID, agentOrAlias, threadID)
}
