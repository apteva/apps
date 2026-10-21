package sdk

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
)

// AgentKind identifies a server-owned special agent target. Ordinary agents
// are addressed by AgentID; special agent kinds stay explicit and closed so an
// app cannot turn an arbitrary database kind into an attachment target.
type AgentKind string

const (
	AgentKindPlatformHelper AgentKind = "platform_helper"
)

// EnsureAppToolsRequest asks the platform to add this app's MCP tools, plus a
// selected subset of its declared requires.apps dependencies, to one agent.
// Exactly one of AgentID or AgentKind must be set.
type EnsureAppToolsRequest struct {
	AgentID             int64     `json:"agent_id,omitempty"`
	AgentKind           AgentKind `json:"agent_kind,omitempty"`
	IncludeRequiredApps []string  `json:"include_required_apps,omitempty"`
}

// EnsureAppToolsResult describes the canonical attachment state after an
// idempotent ensure operation. Applied means the desired state is available to
// the running agent now, or was durably persisted for its next start.
type EnsureAppToolsResult struct {
	AgentID            int64   `json:"agent_id"`
	AttachedInstallIDs []int64 `json:"attached_install_ids"`
	MCPServerIDs       []int64 `json:"mcp_server_ids"`
	Changed            bool    `json:"changed"`
	Applied            bool    `json:"applied"`
	AgentRunning       bool    `json:"agent_running"`
	ResetThreads       int     `json:"reset_threads,omitempty"`
}

// AgentToolsClient is the optional app-to-agent MCP attachment capability.
// The server always includes the calling install and accepts dependency names
// only when they are declared in, and concretely bound through, requires.apps.
type AgentToolsClient interface {
	EnsureAppToolsAttached(req EnsureAppToolsRequest) (*EnsureAppToolsResult, error)
}

// AgentToolsError is a structured platform rejection. Code is stable for app
// control flow; Message remains human-readable and may change.
type AgentToolsError struct {
	StatusCode int       `json:"-"`
	Code       string    `json:"code"`
	Message    string    `json:"error"`
	AgentKind  AgentKind `json:"agent_kind,omitempty"`
}

func (e *AgentToolsError) Error() string {
	if e == nil {
		return ""
	}
	if e.Message != "" {
		return e.Message
	}
	if e.Code != "" {
		return e.Code
	}
	return fmt.Sprintf("agent tools request failed with HTTP %d", e.StatusCode)
}

// IsAgentToolsErrorCode reports whether err is an AgentToolsError with code.
func IsAgentToolsErrorCode(err error, code string) bool {
	var target *AgentToolsError
	return errors.As(err, &target) && target.Code == code
}

func validateEnsureAppToolsRequest(req EnsureAppToolsRequest) error {
	hasID := req.AgentID > 0
	hasKind := strings.TrimSpace(string(req.AgentKind)) != ""
	if hasID == hasKind {
		return errors.New("exactly one of agent_id or agent_kind is required")
	}
	if req.AgentID < 0 {
		return errors.New("agent_id must be positive")
	}
	if hasKind && req.AgentKind != AgentKindPlatformHelper {
		return fmt.Errorf("unsupported agent_kind %q", req.AgentKind)
	}
	seen := map[string]bool{}
	for _, name := range req.IncludeRequiredApps {
		name = strings.TrimSpace(name)
		if name == "" {
			return errors.New("include_required_apps cannot contain an empty name")
		}
		if seen[name] {
			return fmt.Errorf("include_required_apps contains duplicate %q", name)
		}
		seen[name] = true
	}
	return nil
}

func (c *httpPlatformClient) EnsureAppToolsAttached(req EnsureAppToolsRequest) (*EnsureAppToolsResult, error) {
	if err := validateEnsureAppToolsRequest(req); err != nil {
		return nil, err
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequest(http.MethodPost,
		c.baseURL+"/api/apps/callback/agent-tools/ensure-attached", bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	c.addAuth(httpReq)
	resp, err := c.client.Do(httpReq)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		var platformErr AgentToolsError
		if json.Unmarshal(raw, &platformErr) != nil {
			platformErr.Message = strings.TrimSpace(string(raw))
		}
		platformErr.StatusCode = resp.StatusCode
		return nil, &platformErr
	}
	var out EnsureAppToolsResult
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, err
	}
	if out.AttachedInstallIDs == nil {
		out.AttachedInstallIDs = []int64{}
	}
	if out.MCPServerIDs == nil {
		out.MCPServerIDs = []int64{}
	}
	return &out, nil
}
