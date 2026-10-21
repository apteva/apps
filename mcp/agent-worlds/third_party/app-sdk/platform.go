package sdk

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// httpPlatformClient is the SDK's default PlatformClient — talks to
// apteva-server's /api/apps/callback/* surface using the per-install
// APTEVA_APP_TOKEN. The platform validates the token + checks the
// caller's declared permissions on every call.
//
// Two HTTP clients: `client` for fast metadata endpoints (whoami,
// connection lookup, event send) at 30s; `slowClient` for callbacks
// that proxy upstream API calls or other apps' tools, where the wire
// time is bounded by the platform's own per-tool timeout — generous
// here, restrictive at the next hop.
type httpPlatformClient struct {
	baseURL     string
	token       string
	userSession string
	client      *http.Client
	slowClient  *http.Client
	// streamClient has no whole-request timeout. Long transfers are bounded by
	// the context supplied by the app, while the transport still enforces its
	// normal connection and response-header deadlines.
	streamClient *http.Client

	// platform_info cache. Sidecars used to read public_url etc. via
	// the APTEVA_PUBLIC_URL env var captured at spawn time; that meant
	// operators changing public_url in settings had to restart every
	// sidecar. PlatformInfo() now fetches a small JSON blob from the
	// platform and caches it for piCacheTTL, so apps get hot updates
	// within a minute of an operator change without DOSing the
	// server from hot loops.
	piMu       sync.Mutex
	piCached   *PlatformInfo
	piCachedAt time.Time
}

const piCacheTTL = 60 * time.Second

func platformWithUserSession(client PlatformClient, session string) PlatformClient {
	switch c := client.(type) {
	case *httpPlatformClient:
		// Share transports, but never copy the cache mutex or share user state.
		return &httpPlatformClient{
			baseURL: c.baseURL, token: c.token, userSession: session,
			client: c.client, slowClient: c.slowClient, streamClient: c.streamClient,
		}
	case *projectScopedClient:
		return &projectScopedClient{inner: platformWithUserSession(c.inner, session), projectID: c.projectID}
	default:
		return client
	}
}

func newHTTPPlatformClient(baseURL, token string) PlatformClient {
	if baseURL == "" {
		baseURL = "http://127.0.0.1:5280"
	}
	return &httpPlatformClient{
		baseURL: baseURL, token: token,
		client:       &http.Client{Timeout: 30 * time.Second},
		slowClient:   &http.Client{Timeout: 10 * time.Minute},
		streamClient: &http.Client{},
	}
}

const platformBackupErrorLimit = 64 << 10

func (c *httpPlatformClient) OpenPlatformSnapshot(ctx context.Context) (io.ReadCloser, error) {
	return c.OpenPlatformSnapshotWithPassphrase(ctx, "")
}

// OpenPlatformSnapshotWithPassphrase wraps the server recovery key inside the
// snapshot. The passphrase is sent only in an authenticated request header.
func (c *httpPlatformClient) OpenPlatformSnapshotWithPassphrase(ctx context.Context, passphrase string) (io.ReadCloser, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/api/apps/callback/platform/snapshot", nil)
	if err != nil {
		return nil, err
	}
	if passphrase != "" {
		req.Header.Set("X-Backup-Passphrase", passphrase)
	}
	c.addAuth(req)
	resp, err := c.platformStreamClient().Do(req)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		defer resp.Body.Close()
		return nil, boundedPlatformError(resp, platformBackupErrorLimit)
	}
	return resp.Body, nil
}

func (c *httpPlatformClient) RestorePlatformSnapshot(ctx context.Context, body io.Reader, size int64) (map[string]any, error) {
	return c.RestorePlatformSnapshotWithPassphrase(ctx, body, size, "")
}

func (c *httpPlatformClient) RestorePlatformSnapshotWithPassphrase(ctx context.Context, body io.Reader, size int64, passphrase string) (map[string]any, error) {
	if body == nil {
		return nil, errors.New("platform restore body is required")
	}
	if size < -1 {
		return nil, errors.New("platform restore size must be -1 or non-negative")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/apps/callback/platform/restore", body)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/gzip")
	req.Header.Set("X-Confirm-Restore", "yes")
	if size >= 0 {
		req.ContentLength = size
	} else {
		// Override net/http's reader-size inference so -1 has the promised
		// chunked-transfer meaning even for bytes.Buffer/bytes.Reader inputs.
		req.ContentLength = -1
	}
	if passphrase != "" {
		req.Header.Set("X-Backup-Passphrase", passphrase)
	}
	c.addAuth(req)
	resp, err := c.platformStreamClient().Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, boundedPlatformError(resp, platformBackupErrorLimit)
	}
	limited := io.LimitReader(resp.Body, platformBackupErrorLimit+1)
	raw, err := io.ReadAll(limited)
	if err != nil {
		return nil, fmt.Errorf("read platform restore response: %w", err)
	}
	if len(raw) > platformBackupErrorLimit {
		return nil, errors.New("platform restore response exceeds limit")
	}
	var report map[string]any
	if err := json.Unmarshal(raw, &report); err != nil {
		return nil, fmt.Errorf("decode platform restore response: %w", err)
	}
	return report, nil
}

func (c *httpPlatformClient) platformStreamClient() *http.Client {
	if c.streamClient != nil {
		return c.streamClient
	}
	if c.slowClient != nil {
		return c.slowClient
	}
	return http.DefaultClient
}

func boundedPlatformError(resp *http.Response, limit int64) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, limit))
	return fmt.Errorf("platform %s: http %d: %s", resp.Request.URL.Path, resp.StatusCode, strings.TrimSpace(string(body)))
}

// PlatformInfo fetches the platform-level facts bag (public_url +
// version). 60-second in-memory cache so apps can call from hot loops.
// Older platforms that don't yet expose /api/apps/callback/platform-info
// return a 404; we degrade gracefully by reading APTEVA_PUBLIC_URL from
// the env (the legacy spawn-time variable) so apps that switch to this
// helper keep working against pre-v0.17.6 servers.
func (c *httpPlatformClient) PlatformInfo() (*PlatformInfo, error) {
	c.piMu.Lock()
	if c.piCached != nil && time.Since(c.piCachedAt) < piCacheTTL {
		out := *c.piCached
		c.piMu.Unlock()
		return &out, nil
	}
	c.piMu.Unlock()

	var fresh PlatformInfo
	err := c.get("/api/apps/callback/platform-info", &fresh)
	if err != nil {
		// Back-compat: fall back to the env. Don't cache the env-fallback
		// (the platform might come online with the endpoint between calls
		// — let the next call retry the real fetch).
		if envURL := strings.TrimSpace(os.Getenv("APTEVA_PUBLIC_URL")); envURL != "" {
			return &PlatformInfo{PublicURL: envURL}, nil
		}
		return nil, err
	}

	c.piMu.Lock()
	c.piCached = &fresh
	c.piCachedAt = time.Now()
	c.piMu.Unlock()
	return &fresh, nil
}

func browserOriginRegistrationPath(registrationKey string) (string, error) {
	registrationKey = strings.TrimSpace(registrationKey)
	if registrationKey == "" {
		return "", errors.New("browser-origin registration key is required")
	}
	if strings.ContainsAny(registrationKey, "/\\") {
		return "", errors.New("browser-origin registration key cannot contain slashes")
	}
	return "/api/apps/callback/cors-origins/" + url.PathEscape(registrationKey), nil
}

// ReplaceBrowserOrigins atomically replaces one app-owned client's origin set.
// The app install token is attached by put; server-side changes are visible to
// the next browser preflight without restarting Apteva or the sidecar.
func (c *httpPlatformClient) ReplaceBrowserOrigins(registrationKey string, origins []string) (*BrowserOriginRegistration, error) {
	path, err := browserOriginRegistrationPath(registrationKey)
	if err != nil {
		return nil, err
	}
	// Keep the wire shape deterministic: an empty set is [] rather than null.
	wanted := append([]string{}, origins...)
	var out BrowserOriginRegistration
	if err := c.put(path, map[string]any{"origins": wanted}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// ReplaceBrowserOriginPolicy atomically replaces an app-owned origin set and
// its preflight/credential policy. The response is verified so an older server
// cannot silently ignore policy fields and report a misleading success.
func (c *httpPlatformClient) ReplaceBrowserOriginPolicy(registrationKey string, policy BrowserOriginPolicy) (*BrowserOriginRegistration, error) {
	path, err := browserOriginRegistrationPath(registrationKey)
	if err != nil {
		return nil, err
	}
	if policy.Preflight == "" {
		policy.Preflight = BrowserPreflightPlatform
	}
	if policy.Preflight != BrowserPreflightPlatform && policy.Preflight != BrowserPreflightApp {
		return nil, fmt.Errorf("invalid browser preflight mode %q", policy.Preflight)
	}
	// Keep the wire shape deterministic: an empty set is [] rather than null.
	policy.Origins = append([]string{}, policy.Origins...)
	var out BrowserOriginRegistration
	if err := c.put(path, policy, &out); err != nil {
		return nil, err
	}
	if out.Preflight != policy.Preflight || out.Credentials != policy.Credentials {
		return nil, fmt.Errorf(
			"platform did not apply requested browser-origin policy (wanted preflight=%q credentials=%t, got preflight=%q credentials=%t); upgrade the Apteva server",
			policy.Preflight, policy.Credentials, out.Preflight, out.Credentials,
		)
	}
	return &out, nil
}

func (c *httpPlatformClient) DeleteBrowserOrigins(registrationKey string) error {
	path, err := browserOriginRegistrationPath(registrationKey)
	if err != nil {
		return err
	}
	return c.delete(path, nil)
}

func (c *httpPlatformClient) ListBrowserOriginRegistrations() ([]BrowserOriginRegistration, error) {
	var envelope struct {
		Registrations []BrowserOriginRegistration `json:"registrations"`
	}
	if err := c.get("/api/apps/callback/cors-origins", &envelope); err != nil {
		return nil, err
	}
	if envelope.Registrations == nil {
		envelope.Registrations = []BrowserOriginRegistration{}
	}
	return envelope.Registrations, nil
}

func (c *httpPlatformClient) GetConnection(id int64) (*PlatformConnection, error) {
	var out PlatformConnection
	if err := c.get("/api/apps/callback/connections/"+strconv.FormatInt(id, 10), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpPlatformClient) ListConnections(filter ConnectionFilter) ([]PlatformConnection, error) {
	q := ""
	if filter.ProjectID != "" {
		q = "?project_id=" + filter.ProjectID
	}
	if filter.AppSlug != "" {
		if q == "" {
			q = "?"
		} else {
			q += "&"
		}
		q += "app_slug=" + filter.AppSlug
	}
	var out []PlatformConnection
	if err := c.get("/api/apps/callback/connections"+q, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *httpPlatformClient) GetInstance(id int64) (*PlatformInstance, error) {
	// Hit the canonical /agents/<id> callback path; the server's
	// Phase 2 alias makes /instances/<id> work just as well, but new
	// SDK code uses the new vocabulary.
	var out PlatformInstance
	if err := c.get("/api/apps/callback/agents/"+strconv.FormatInt(id, 10), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetAgent is the agent-vocabulary alias for GetInstance. Both methods
// return the same PlatformInstance / PlatformAgent struct (type alias)
// and hit the same platform endpoint. New code should prefer GetAgent.
func (c *httpPlatformClient) GetAgent(id int64) (*PlatformAgent, error) {
	return c.GetInstance(id)
}

// ListAgents implements the optional AgentDirectoryClient surface.
// Requires the platform.instances.read permission server-side.
func (c *httpPlatformClient) ListAgents(projectID string) ([]PlatformAgent, error) {
	path := "/api/apps/callback/agents"
	if strings.TrimSpace(projectID) != "" {
		path += "?project_id=" + url.QueryEscape(projectID)
	}
	var out []PlatformAgent
	if err := c.get(path, &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *httpPlatformClient) SendEvent(instanceID int64, message string) error {
	return c.post("/api/apps/callback/agents/"+strconv.FormatInt(instanceID, 10)+"/event",
		map[string]any{"message": message}, nil)
}

func (c *httpPlatformClient) SendThreadEvent(target ThreadRef, message any) error {
	if target.AgentID <= 0 {
		return errors.New("SendThreadEvent: agent_id required")
	}
	if strings.TrimSpace(target.ThreadID) == "" {
		return errors.New("SendThreadEvent: thread_id required")
	}
	if message == nil {
		return errors.New("SendThreadEvent: message required")
	}
	return c.post("/api/apps/callback/agents/"+strconv.FormatInt(target.AgentID, 10)+"/event",
		map[string]any{"message": message, "thread_id": strings.TrimSpace(target.ThreadID)}, nil)
}

func (c *httpPlatformClient) SendToChannel(channelName, projectID, message string) error {
	return c.post("/api/apps/callback/channels/send",
		map[string]any{"channel": channelName, "project_id": projectID, "message": message}, nil)
}

func (c *httpPlatformClient) WhoAmI() (*InstallIdentity, error) {
	var out InstallIdentity
	if err := c.get("/api/apps/callback/whoami", &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpPlatformClient) ExecuteIntegrationTool(connID int64, tool string, input map[string]any) (*ExecuteResult, error) {
	return c.ExecuteIntegrationToolContext(context.Background(), connID, tool, input)
}

func (c *httpPlatformClient) ExecuteIntegrationToolContext(ctx context.Context, connID int64, tool string, input map[string]any) (*ExecuteResult, error) {
	if input == nil {
		input = map[string]any{}
	}
	body := map[string]any{"tool": tool, "input": input}
	var out ExecuteResult
	if err := c.postWithContext(ctx, c.slowClient, "/api/apps/callback/integrations/"+strconv.FormatInt(connID, 10)+"/execute", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpPlatformClient) GetIntegrationURLProperty(connID int64, property string) (*IntegrationURLPropertyStatus, error) {
	var out IntegrationURLPropertyStatus
	endpoint := "/api/apps/callback/integrations/" + strconv.FormatInt(connID, 10) + "/url-properties/" + url.PathEscape(property)
	if err := c.get(endpoint, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpPlatformClient) CallApp(appName, tool string, input map[string]any) (json.RawMessage, error) {
	return c.CallAppContext(context.Background(), appName, tool, input)
}

func (c *httpPlatformClient) CallAppContext(ctx context.Context, appName, tool string, input map[string]any) (json.RawMessage, error) {
	raw, _, err := c.callApp(ctx, appName, tool, input, false)
	return raw, err
}

func (c *httpPlatformClient) callApp(ctx context.Context, appName, tool string, input map[string]any, innerResult bool) (json.RawMessage, bool, error) {
	if input == nil {
		input = map[string]any{}
	}
	body := map[string]any{"tool": tool, "input": input}
	rawBody, err := json.Marshal(body)
	if err != nil {
		return nil, false, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/api/apps/callback/apps/"+url.PathEscape(appName)+"/call", bytes.NewReader(rawBody))
	if err != nil {
		return nil, false, err
	}
	req.Header.Set("Content-Type", "application/json")
	if innerResult {
		req.Header.Set(HeaderAppCallResult, "inner")
		req.Header.Set(HeaderAppResultFormat, AppResultJSON)
	}
	setAppCallDeadline(req)
	c.addAuth(req)
	resp, err := c.slowClient.Do(req)
	if err != nil {
		return nil, false, err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return nil, false, c.platformErr(resp)
	}
	out, err := io.ReadAll(io.LimitReader(resp.Body, (16<<20)+1))
	if err != nil {
		return nil, false, err
	}
	if len(out) > 16<<20 {
		return nil, false, errors.New("app response too large")
	}
	return json.RawMessage(out), resp.Header.Get(HeaderAppResultFormat) == AppResultJSON, nil
}

// CallAppResult — CallApp + MCP-envelope unwrap. See PlatformClient
// interface doc for the wire shape and motivation. Pure addition;
// CallApp keeps its raw-envelope contract for the apps that already
// strip the envelope themselves (deploy/domain_link, certs/domain_link).
func (c *httpPlatformClient) CallAppResult(appName, tool string, input map[string]any, out any) error {
	return c.CallAppResultContext(context.Background(), appName, tool, input, out)
}

func (c *httpPlatformClient) CallAppResultContext(ctx context.Context, appName, tool string, input map[string]any, out any) error {
	raw, direct, err := c.callApp(ctx, appName, tool, input, true)
	if err != nil {
		return err
	}
	if direct {
		return json.Unmarshal(raw, out)
	}
	return decodeMCPEnvelope(raw, appName, tool, out)
}

func (c *httpPlatformClient) CallAppBatch(appName string, calls []AppCall) ([]AppCallResult, error) {
	return c.CallAppBatchContext(context.Background(), appName, calls, AppBatchOptions{})
}

func (c *httpPlatformClient) CallAppBatchContext(ctx context.Context, appName string, calls []AppCall, options AppBatchOptions) ([]AppCallResult, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if strings.TrimSpace(appName) == "" {
		return nil, errors.New("CallAppBatch: app name required")
	}
	if len(calls) == 0 {
		return []AppCallResult{}, nil
	}
	if len(calls) > 32 {
		return nil, errors.New("CallAppBatch: maximum 32 calls")
	}
	if options.Execution != "" && options.Execution != "sequential" && options.Execution != ParallelIndependent {
		return nil, errors.New("CallAppBatch: unknown execution mode")
	}
	if options.Concurrency < 0 || options.Concurrency > 8 || (options.Execution != ParallelIndependent && options.Concurrency > 1) {
		return nil, errors.New("CallAppBatch: invalid concurrency")
	}
	if options.ResultMode != "" && options.ResultMode != "json" {
		return nil, errors.New("CallAppBatch: unknown result mode")
	}
	var out struct {
		Results []AppCallResult `json:"results"`
	}
	if err := c.postWithContext(ctx, c.slowClient, "/api/apps/callback/apps/"+url.PathEscape(appName)+"/batch", struct {
		Calls []AppCall `json:"calls"`
		AppBatchOptions
	}{calls, options}, &out, 32<<20); err != nil {
		return nil, err
	}
	if out.Results == nil {
		out.Results = []AppCallResult{}
	}
	return out.Results, nil
}

// decodeMCPEnvelope strips the JSON-RPC + content-array layers and
// json.Unmarshals the inner text into out. Resilient to two
// shapes:
//
//  1. Full envelope: {"jsonrpc":..., "result":{"content":[{"text":"<json>"}]}}
//  2. Bare inner: <json> directly (test mocks, future platform
//     versions that pre-unwrap, etc.)
//
// On RPC-level error (.error.code) returns a descriptive Go error
// without ever touching out.
func decodeMCPEnvelope(raw json.RawMessage, appName, tool string, out any) error {
	if out == nil {
		return errors.New("CallAppResult: out is nil")
	}
	if len(raw) == 0 {
		return fmt.Errorf("%s.%s: empty response", appName, tool)
	}
	var env struct {
		Result json.RawMessage `json:"result"`
		Error  *struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"error,omitempty"`
	}
	// Try the envelope shape; if it doesn't fit (caller is using a
	// pre-unwrapped client or the bytes are already the inner JSON),
	// fall through to direct decode.
	if err := json.Unmarshal(raw, &env); err != nil {
		return json.Unmarshal(raw, out)
	}
	if env.Error != nil {
		return fmt.Errorf("%s.%s: %s (code=%d)", appName, tool, env.Error.Message, env.Error.Code)
	}
	if len(env.Result) > 0 {
		if handled, err := decodeMCPContent(env.Result, appName, tool, out); handled || err != nil {
			return err
		}
	}
	// Environment seed/app-call endpoints return the MCP result object
	// directly ({content:[...]}) rather than the full JSON-RPC envelope.
	if handled, err := decodeMCPContent(raw, appName, tool, out); handled || err != nil {
		return err
	}
	// Either the response was already unwrapped (some test paths) or it
	// had no content. Try direct decode; if that fails we surface the
	// original bytes for diagnosis.
	if jerr := json.Unmarshal(raw, out); jerr != nil {
		return fmt.Errorf("%s.%s: response had no content array and direct decode failed: %w", appName, tool, jerr)
	}
	return nil
}

func decodeMCPContent(raw json.RawMessage, appName, tool string, out any) (bool, error) {
	var result struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError,omitempty"`
	}
	if err := json.Unmarshal(raw, &result); err != nil || len(result.Content) == 0 {
		return false, nil
	}
	inner := result.Content[0].Text
	if inner == "" {
		return true, fmt.Errorf("%s.%s: empty content text", appName, tool)
	}
	if result.IsError {
		return true, fmt.Errorf("%s.%s: tool returned error: %.200s", appName, tool, inner)
	}
	if err := json.Unmarshal([]byte(inner), out); err != nil {
		return true, fmt.Errorf("%s.%s: decode inner JSON: %w (text: %.200s)", appName, tool, err, inner)
	}
	return true, nil
}

func (c *httpPlatformClient) StartOAuth(req OAuthStartRequest) (*OAuthStartResult, error) {
	var out OAuthStartResult
	if err := c.post("/api/apps/callback/oauth/start", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpPlatformClient) DisconnectConnection(connID int64) error {
	return c.post("/api/apps/callback/connections/"+strconv.FormatInt(connID, 10)+"/disconnect", nil, nil)
}

func (c *httpPlatformClient) ListOwnedConnections() ([]PlatformConnection, error) {
	var out []PlatformConnection
	if err := c.get("/api/apps/callback/connections?owned=true", &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *httpPlatformClient) EnsureManagedConnection(req ManagedConnectionRequest) (*PlatformConnection, error) {
	var out PlatformConnection
	if err := c.post("/api/apps/callback/connections/managed/ensure", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpPlatformClient) RotateManagedConnection(id int64, req ManagedConnectionRotation) (*PlatformConnection, error) {
	var out PlatformConnection
	path := "/api/apps/callback/connections/" + strconv.FormatInt(id, 10) + "/managed/credentials"
	if err := c.put(path, req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpPlatformClient) RevokeManagedConnection(id int64) error {
	path := "/api/apps/callback/connections/" + strconv.FormatInt(id, 10) + "/managed"
	return c.delete(path, nil)
}

func (c *httpPlatformClient) CreateManagedTenantEnrollment(req ManagedTenantEnrollmentRequest) (*ManagedTenantEnrollment, error) {
	var out ManagedTenantEnrollment
	if err := c.post("/api/apps/callback/managed-tenants/enrollments", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpPlatformClient) EnsureManagedTenant(req ManagedTenantRequest) (*ManagedTenant, error) {
	var out ManagedTenant
	if err := c.post("/api/apps/callback/managed-tenants/tenants", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpPlatformClient) EnsureManagedConnectionGrant(req ManagedConnectionGrantRequest) (*ManagedConnectionGrant, error) {
	var out ManagedConnectionGrant
	if err := c.post("/api/apps/callback/managed-tenants/grants", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpPlatformClient) GetManagedConnectionGrantDelivery(tenantID, grantID string) (*ManagedConnectionGrantDelivery, error) {
	var out ManagedConnectionGrantDelivery
	path := "/api/apps/callback/managed-tenants/grants/" + url.PathEscape(tenantID) + "/" + url.PathEscape(grantID) + "/delivery"
	if err := c.get(path, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpPlatformClient) RevokeManagedConnectionGrant(tenantID, grantID string) error {
	path := "/api/apps/callback/managed-tenants/grants/" + url.PathEscape(tenantID) + "/" + url.PathEscape(grantID)
	return c.delete(path, nil)
}

func (c *httpPlatformClient) EnsureManagedTenantBundle(req ManagedTenantBundleRequest) (*ManagedTenantBundle, error) {
	var out ManagedTenantBundle
	if err := c.post("/api/apps/callback/managed-tenants/bundles", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpPlatformClient) GetManagedTenantBundle(tenantID, bundleID string) (*ManagedTenantBundle, error) {
	var out ManagedTenantBundle
	path := "/api/apps/callback/managed-tenants/bundles/" + url.PathEscape(tenantID) + "/" + url.PathEscape(bundleID)
	if err := c.get(path, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// GetGrants fetches the policy the operator wrote for (this install,
// instanceID). Returns empty rules + default "allow" when the
// platform endpoint is missing — back-compat with older servers
// that haven't shipped the grants API yet.
func (c *httpPlatformClient) GetGrants(instanceID int64) (*GrantsResponse, error) {
	var out GrantsResponse
	path := "/api/apps/callback/grants?instance_id=" + strconv.FormatInt(instanceID, 10)
	err := c.get(path, &out)
	if err != nil {
		// Older servers return 404 for the endpoint; treat as
		// "default allow, no rules" so existing apps don't 403
		// after upgrading the SDK.
		return &GrantsResponse{DefaultEffect: "allow"}, nil
	}
	if out.DefaultEffect == "" {
		out.DefaultEffect = "allow"
	}
	return &out, nil
}

// GetConnectionCredentials hits /api/apps/callback/connections/:id/credentials.
// Authorization (permission, compatible_slugs, binding) is enforced
// server-side; this is a thin GET wrapper.
func (c *httpPlatformClient) GetConnectionCredentials(id int64) (*ConnectionCredentials, error) {
	var out ConnectionCredentials
	path := "/api/apps/callback/connections/" + strconv.FormatInt(id, 10) + "/credentials"
	if err := c.get(path, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpPlatformClient) GetConnectionPublicConfig(id int64) (*ConnectionPublicConfig, error) {
	var out ConnectionPublicConfig
	path := "/api/apps/callback/connections/" + strconv.FormatInt(id, 10) + "/public-config"
	if err := c.get(path, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpPlatformClient) EnsureIntegrationWebhook(req IntegrationWebhookEnsureRequest) (*IntegrationWebhookStatus, error) {
	var out IntegrationWebhookStatus
	if err := c.post("/api/apps/callback/integration-webhooks/ensure", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpPlatformClient) VerifyIntegrationWebhook(req IntegrationWebhookVerifyRequest) (*IntegrationWebhookVerifyResult, error) {
	var out IntegrationWebhookVerifyResult
	if err := c.post("/api/apps/callback/integration-webhooks/verify", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpPlatformClient) ListProjects() ([]PlatformProject, error) {
	var out []PlatformProject
	if err := c.get("/api/apps/callback/projects", &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *httpPlatformClient) ListProjectTemplates(projectID string, options ProjectTemplateListOptions) ([]ProjectTemplate, error) {
	projectID = strings.TrimSpace(projectID)
	if projectID == "" {
		return nil, errors.New("ListProjectTemplates: project id required")
	}
	query := url.Values{"project_id": []string{projectID}}
	if options.IncludeSystem {
		query.Set("include_system", "true")
	}
	var out struct {
		Templates []ProjectTemplate `json:"templates"`
	}
	if err := c.get("/api/apps/callback/templates?"+query.Encode(), &out); err != nil {
		return nil, err
	}
	if out.Templates == nil {
		out.Templates = []ProjectTemplate{}
	}
	return out.Templates, nil
}

func (c *httpPlatformClient) GetProjectTemplate(projectID, templateID string) (*ProjectTemplate, error) {
	projectID = strings.TrimSpace(projectID)
	templateID = strings.TrimSpace(templateID)
	if projectID == "" {
		return nil, errors.New("GetProjectTemplate: project id required")
	}
	if templateID == "" || strings.ContainsAny(templateID, `/\\`) {
		return nil, errors.New("GetProjectTemplate: valid template id required")
	}
	query := url.Values{"project_id": []string{projectID}}
	var out ProjectTemplate
	if err := c.get("/api/apps/callback/templates/"+url.PathEscape(templateID)+"?"+query.Encode(), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpPlatformClient) ExposeIngress(req IngressExposeRequest) (*IngressRoute, error) {
	var out struct {
		Route IngressRoute `json:"route"`
	}
	if err := c.post("/api/apps/callback/ingress/expose", req, &out); err != nil {
		return nil, err
	}
	return &out.Route, nil
}

func (c *httpPlatformClient) UnexposeIngress(hostname string) error {
	if strings.TrimSpace(hostname) == "" {
		return errors.New("UnexposeIngress: hostname required")
	}
	return c.post("/api/apps/callback/ingress/unexpose", map[string]any{"hostname": hostname}, nil)
}

func (c *httpPlatformClient) ListIngressRoutes() ([]IngressRoute, error) {
	var out struct {
		Routes []IngressRoute `json:"routes"`
	}
	if err := c.get("/api/apps/callback/ingress/routes", &out); err != nil {
		return nil, err
	}
	if out.Routes == nil {
		out.Routes = []IngressRoute{}
	}
	return out.Routes, nil
}

func (c *httpPlatformClient) ListDomainGrants() ([]DomainGrant, error) {
	var out struct {
		Grants []DomainGrant `json:"grants"`
	}
	if err := c.get("/api/apps/callback/dns/grants", &out); err != nil {
		return nil, err
	}
	if out.Grants == nil {
		out.Grants = []DomainGrant{}
	}
	return out.Grants, nil
}

func (c *httpPlatformClient) UpsertDNSRecord(req DNSRecordRequest) (*DNSRecordResult, error) {
	var out DNSRecordResult
	if err := c.post("/api/apps/callback/dns/records", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpPlatformClient) DeleteDNSRecord(req DNSRecordRequest) (*DNSRecordResult, error) {
	var out DNSRecordResult
	if err := c.deleteWithBody("/api/apps/callback/dns/records", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpPlatformClient) ListEnvironments() ([]EnvironmentSummary, error) {
	var out []EnvironmentSummary
	if err := c.get("/api/environments", &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *httpPlatformClient) CreateEnvironment(req EnvironmentCreateRequest) (*EnvironmentSummary, error) {
	var out EnvironmentSummary
	if err := c.postWith(c.slowClient, "/api/environments", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpPlatformClient) GetEnvironment(id string) (*EnvironmentSummary, error) {
	if strings.TrimSpace(id) == "" {
		return nil, errors.New("GetEnvironment: id required")
	}
	var out EnvironmentSummary
	if err := c.get("/api/environments/"+url.PathEscape(id), &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpPlatformClient) DestroyEnvironment(id string) error {
	if strings.TrimSpace(id) == "" {
		return errors.New("DestroyEnvironment: id required")
	}
	return c.delete("/api/environments/"+url.PathEscape(id), nil)
}

func (c *httpPlatformClient) SeedEnvironment(id string, calls []EnvironmentSeedCall, seedBaseDir string) ([]json.RawMessage, error) {
	if strings.TrimSpace(id) == "" {
		return nil, errors.New("SeedEnvironment: id required")
	}
	if len(calls) == 0 {
		return []json.RawMessage{}, nil
	}
	body := map[string]any{"calls": calls}
	if seedBaseDir != "" {
		body["seed_base_dir"] = seedBaseDir
	}
	var out struct {
		Results []json.RawMessage `json:"results"`
	}
	if err := c.postWith(c.slowClient, "/api/environments/"+url.PathEscape(id)+"/seed", body, &out); err != nil {
		return nil, err
	}
	if out.Results == nil {
		out.Results = []json.RawMessage{}
	}
	return out.Results, nil
}

func (c *httpPlatformClient) CallEnvironmentApp(environmentID, appName, tool string, input map[string]any) (json.RawMessage, error) {
	if strings.TrimSpace(appName) == "" {
		return nil, errors.New("CallEnvironmentApp: appName required")
	}
	if strings.TrimSpace(tool) == "" {
		return nil, errors.New("CallEnvironmentApp: tool required")
	}
	results, err := c.SeedEnvironment(environmentID, []EnvironmentSeedCall{{App: appName, Tool: tool, Input: input}}, "")
	if err != nil {
		return nil, err
	}
	if len(results) == 0 {
		return nil, fmt.Errorf("CallEnvironmentApp %s.%s: empty response", appName, tool)
	}
	return results[0], nil
}

func (c *httpPlatformClient) CallEnvironmentAppResult(environmentID, appName, tool string, input map[string]any, out any) error {
	raw, err := c.CallEnvironmentApp(environmentID, appName, tool, input)
	if err != nil {
		return err
	}
	return decodeMCPEnvelope(raw, appName, tool, out)
}

func (c *httpPlatformClient) SnapshotEnvironment(environmentID string, req EnvironmentSnapshotRequest) (*EnvironmentSnapshot, error) {
	if strings.TrimSpace(environmentID) == "" {
		return nil, errors.New("SnapshotEnvironment: environmentID required")
	}
	var out EnvironmentSnapshot
	if err := c.postWith(c.slowClient, "/api/environments/"+url.PathEscape(environmentID)+"/snapshot", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpPlatformClient) ListEnvironmentAgents(environmentID string) ([]EnvironmentAgent, error) {
	if strings.TrimSpace(environmentID) == "" {
		return nil, errors.New("ListEnvironmentAgents: environmentID required")
	}
	var out []EnvironmentAgent
	if err := c.get("/api/environments/"+url.PathEscape(environmentID)+"/agents", &out); err != nil {
		return nil, err
	}
	return out, nil
}

func (c *httpPlatformClient) SpawnEnvironmentAgent(environmentID string, req EnvironmentAgentSpawnRequest) (*EnvironmentAgent, error) {
	if strings.TrimSpace(environmentID) == "" {
		return nil, errors.New("SpawnEnvironmentAgent: environmentID required")
	}
	if req.SourceAgentID == 0 {
		return nil, errors.New("SpawnEnvironmentAgent: source_agent_id required")
	}
	var out EnvironmentAgent
	if err := c.postWith(c.slowClient, "/api/environments/"+url.PathEscape(environmentID)+"/agents", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpPlatformClient) StopEnvironmentAgent(environmentID string, agentOrAlias string) error {
	if strings.TrimSpace(environmentID) == "" {
		return errors.New("StopEnvironmentAgent: environmentID required")
	}
	path := "/api/environments/" + url.PathEscape(environmentID) + "/agent"
	if strings.TrimSpace(agentOrAlias) != "" {
		path = "/api/environments/" + url.PathEscape(environmentID) + "/agents/" + url.PathEscape(agentOrAlias)
	}
	return c.delete(path, nil)
}

// SpawnRealtimeThread hits /api/apps/callback/threads/spawn-realtime.
// Returns the spawn result including audio_bridge_url + audio_token
// the caller uses to dial core's audio WebSocket.
func (c *httpPlatformClient) SpawnRealtimeThread(req RealtimeSpawnRequest) (*RealtimeSpawnResult, error) {
	if req.AgentID == 0 {
		return nil, errors.New("SpawnRealtimeThread: agent_id required (pull from CallerFrom(ctx).AgentID in HandlerCtx)")
	}
	if req.ThreadID == "" {
		return nil, errors.New("SpawnRealtimeThread: thread_id required")
	}
	if req.Directive == "" {
		return nil, errors.New("SpawnRealtimeThread: directive required")
	}
	if err := validateRealtimeCapabilityMode(req.CapabilityMode, req.Tools, req.MCP); err != nil {
		return nil, fmt.Errorf("SpawnRealtimeThread: %w", err)
	}
	var out RealtimeSpawnResult
	if err := c.post("/api/apps/callback/threads/spawn-realtime", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpPlatformClient) SpawnThread(req ThreadSpawnRequest) (*ThreadSpawnResult, error) {
	if req.AgentID <= 0 {
		return nil, errors.New("SpawnThread: agent_id required")
	}
	if strings.TrimSpace(req.ThreadID) == "" {
		return nil, errors.New("SpawnThread: thread_id required")
	}
	var out ThreadSpawnResult
	if err := c.post("/api/apps/callback/threads/spawn", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpPlatformClient) EnsureThread(req ThreadEnsureRequest) (*ThreadEnsureResult, error) {
	if req.AgentID <= 0 {
		return nil, errors.New("EnsureThread: agent_id required")
	}
	if strings.TrimSpace(req.ThreadID) == "" {
		return nil, errors.New("EnsureThread: thread_id required")
	}
	var out ThreadEnsureResult
	if err := c.post("/api/apps/callback/threads/ensure", req, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// KillThread hits /api/apps/callback/threads/{id} with DELETE.
// Idempotent — 404 on unknown id is treated as success because the
// caller's intent (no live thread by this name) is already satisfied.
func (c *httpPlatformClient) RenewRealtimeAudioBridge(agentID int64, threadID string) (*RealtimeSpawnResult, error) {
	if agentID <= 0 {
		return nil, errors.New("RenewRealtimeAudioBridge: agent_id required")
	}
	if strings.TrimSpace(threadID) == "" {
		return nil, errors.New("RenewRealtimeAudioBridge: thread_id required")
	}
	var out RealtimeSpawnResult
	path := "/api/apps/callback/threads/" + url.PathEscape(threadID) + "/audio-token?agent_id=" + strconv.FormatInt(agentID, 10)
	if err := c.post(path, map[string]any{}, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

func (c *httpPlatformClient) KillThread(agentID int64, threadID string) error {
	if agentID <= 0 {
		return errors.New("KillThread: agent_id required")
	}
	if threadID == "" {
		return errors.New("KillThread: thread_id required")
	}
	path := "/api/apps/callback/threads/" + url.PathEscape(threadID) + "?agent_id=" + strconv.FormatInt(agentID, 10)
	req, err := http.NewRequest("DELETE", c.baseURL+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+c.token)
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusNotFound {
		return nil
	}
	if resp.StatusCode >= 400 {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("KillThread %s: %d %s", threadID, resp.StatusCode, string(body))
	}
	return nil
}

// projectScopedClient wraps a PlatformClient so CallApp / CallAppResult
// auto-thread `_project_id` into the input map when the caller hasn't
// supplied one. Used by AppCtx.WithProject — apps' worker code calling
// `ctx.PlatformAPI().CallAppResult("storage", "files_list", args, &out)`
// from a global install thus carries the dispatcher's current project
// without anyone touching the call site.
//
// Most other methods pass straight through. Ordinary thread spawn/ensure also
// inherit this project when the request does not provide one, so a global app
// can preserve trusted project context on app-owned threads.
type projectScopedClient struct {
	inner     PlatformClient
	projectID string
}

func wrapPlatformWithProject(inner PlatformClient, projectID string) PlatformClient {
	if projectID == "" || inner == nil {
		return inner
	}
	// Avoid re-wrapping if the inner client already has the same
	// project pinned — idempotent under repeated WithProject calls.
	if existing, ok := inner.(*projectScopedClient); ok && existing.projectID == projectID {
		return existing
	}
	return &projectScopedClient{inner: inner, projectID: projectID}
}

func (p *projectScopedClient) withProject(input map[string]any) map[string]any {
	if input == nil {
		return map[string]any{"_project_id": p.projectID}
	}
	if _, set := input["_project_id"]; set {
		return input
	}
	cp := make(map[string]any, len(input)+1)
	for k, v := range input {
		cp[k] = v
	}
	cp["_project_id"] = p.projectID
	return cp
}

func (p *projectScopedClient) CallApp(appName, tool string, input map[string]any) (json.RawMessage, error) {
	return p.inner.CallApp(appName, tool, p.withProject(input))
}

func (p *projectScopedClient) CallAppResult(appName, tool string, input map[string]any, out any) error {
	return p.inner.CallAppResult(appName, tool, p.withProject(input), out)
}

func (p *projectScopedClient) CallAppContext(ctx context.Context, app, tool string, input map[string]any) (json.RawMessage, error) {
	return CallAppContext(ctx, p.inner, app, tool, p.withProject(input))
}

func (p *projectScopedClient) CallAppResultContext(ctx context.Context, app, tool string, input map[string]any, out any) error {
	return CallAppResultContext(ctx, p.inner, app, tool, p.withProject(input), out)
}

func (p *projectScopedClient) CallAppBatchContext(ctx context.Context, app string, calls []AppCall, options AppBatchOptions) ([]AppCallResult, error) {
	scoped := append([]AppCall(nil), calls...)
	for i := range scoped {
		scoped[i].Input = p.withProject(scoped[i].Input)
	}
	return CallAppBatchContext(ctx, p.inner, app, scoped, options)
}

func (p *projectScopedClient) CallAppBatch(appName string, calls []AppCall) ([]AppCallResult, error) {
	client, ok := p.inner.(AppBatchClient)
	if !ok {
		return nil, errors.New("app batch API unavailable")
	}
	scoped := make([]AppCall, len(calls))
	for i, call := range calls {
		scoped[i] = call
		scoped[i].Input = p.withProject(call.Input)
	}
	return client.CallAppBatch(appName, scoped)
}

// Pass-throughs for every other method.

func (p *projectScopedClient) GetConnection(id int64) (*PlatformConnection, error) {
	return p.inner.GetConnection(id)
}
func (p *projectScopedClient) ListConnections(f ConnectionFilter) ([]PlatformConnection, error) {
	return p.inner.ListConnections(f)
}
func (p *projectScopedClient) GetInstance(id int64) (*PlatformInstance, error) {
	return p.inner.GetInstance(id)
}
func (p *projectScopedClient) GetAgent(id int64) (*PlatformAgent, error) {
	return p.inner.GetAgent(id)
}
func (p *projectScopedClient) SendEvent(instanceID int64, message string) error {
	return p.inner.SendEvent(instanceID, message)
}
func (p *projectScopedClient) SendThreadEvent(target ThreadRef, message any) error {
	client, ok := p.inner.(ThreadClient)
	if !ok {
		return errors.New("thread API unavailable")
	}
	return client.SendThreadEvent(target, message)
}
func (p *projectScopedClient) ListAgents(projectID string) ([]PlatformAgent, error) {
	client, ok := p.inner.(AgentDirectoryClient)
	if !ok {
		return nil, errors.New("agent directory API unavailable")
	}
	if projectID == "" {
		projectID = p.projectID
	}
	return client.ListAgents(projectID)
}
func (p *projectScopedClient) SendToChannel(channelName, projectID, message string) error {
	if projectID == "" {
		projectID = p.projectID
	}
	return p.inner.SendToChannel(channelName, projectID, message)
}
func (p *projectScopedClient) WhoAmI() (*InstallIdentity, error) {
	id, err := p.inner.WhoAmI()
	if err != nil || id == nil || strings.TrimSpace(p.projectID) == "" {
		return id, err
	}
	scoped := *id
	scoped.ProjectID = p.projectID
	if projects, perr := p.inner.ListProjects(); perr == nil {
		for _, project := range projects {
			if project.ID == p.projectID {
				scoped.ProjectName = project.Name
				scoped.ProjectDescription = project.Description
				break
			}
		}
	}
	return &scoped, nil
}
func (p *projectScopedClient) ExecuteIntegrationTool(connID int64, tool string, input map[string]any) (*ExecuteResult, error) {
	return p.inner.ExecuteIntegrationTool(connID, tool, input)
}
func (p *projectScopedClient) ExecuteIntegrationToolContext(ctx context.Context, connID int64, tool string, input map[string]any) (*ExecuteResult, error) {
	return ExecuteIntegrationToolContext(ctx, p.inner, connID, tool, input)
}
func (p *projectScopedClient) GetIntegrationURLProperty(connID int64, property string) (*IntegrationURLPropertyStatus, error) {
	return GetIntegrationURLProperty(p.inner, connID, property)
}
func (p *projectScopedClient) StartOAuth(req OAuthStartRequest) (*OAuthStartResult, error) {
	if req.ProjectID == "" {
		req.ProjectID = p.projectID
	}
	return p.inner.StartOAuth(req)
}
func (p *projectScopedClient) DisconnectConnection(connID int64) error {
	return p.inner.DisconnectConnection(connID)
}
func (p *projectScopedClient) ListOwnedConnections() ([]PlatformConnection, error) {
	return p.inner.ListOwnedConnections()
}
func (p *projectScopedClient) GetGrants(instanceID int64) (*GrantsResponse, error) {
	return p.inner.GetGrants(instanceID)
}
func (p *projectScopedClient) GetConnectionCredentials(id int64) (*ConnectionCredentials, error) {
	return p.inner.GetConnectionCredentials(id)
}
func (p *projectScopedClient) GetConnectionPublicConfig(id int64) (*ConnectionPublicConfig, error) {
	return GetConnectionPublicConfig(p.inner, id)
}
func (p *projectScopedClient) EnsureManagedConnection(req ManagedConnectionRequest) (*PlatformConnection, error) {
	manager, ok := p.inner.(ManagedConnectionClient)
	if !ok {
		return nil, errors.New("platform does not support managed connections")
	}
	if req.ProjectID == "" {
		req.ProjectID = p.projectID
	}
	return manager.EnsureManagedConnection(req)
}
func (p *projectScopedClient) RotateManagedConnection(id int64, req ManagedConnectionRotation) (*PlatformConnection, error) {
	manager, ok := p.inner.(ManagedConnectionClient)
	if !ok {
		return nil, errors.New("platform does not support managed connections")
	}
	return manager.RotateManagedConnection(id, req)
}
func (p *projectScopedClient) RevokeManagedConnection(id int64) error {
	manager, ok := p.inner.(ManagedConnectionClient)
	if !ok {
		return errors.New("platform does not support managed connections")
	}
	return manager.RevokeManagedConnection(id)
}
func (p *projectScopedClient) CreateManagedTenantEnrollment(req ManagedTenantEnrollmentRequest) (*ManagedTenantEnrollment, error) {
	manager, ok := p.inner.(ManagedTenantClient)
	if !ok {
		return nil, errors.New("platform does not support managed tenants")
	}
	return manager.CreateManagedTenantEnrollment(req)
}
func (p *projectScopedClient) EnsureManagedTenant(req ManagedTenantRequest) (*ManagedTenant, error) {
	manager, ok := p.inner.(ManagedTenantClient)
	if !ok {
		return nil, errors.New("platform does not support managed tenants")
	}
	return manager.EnsureManagedTenant(req)
}
func (p *projectScopedClient) EnsureManagedConnectionGrant(req ManagedConnectionGrantRequest) (*ManagedConnectionGrant, error) {
	manager, ok := p.inner.(ManagedTenantClient)
	if !ok {
		return nil, errors.New("platform does not support managed tenants")
	}
	return manager.EnsureManagedConnectionGrant(req)
}
func (p *projectScopedClient) GetManagedConnectionGrantDelivery(tenantID, grantID string) (*ManagedConnectionGrantDelivery, error) {
	manager, ok := p.inner.(ManagedTenantClient)
	if !ok {
		return nil, errors.New("platform does not support managed tenants")
	}
	return manager.GetManagedConnectionGrantDelivery(tenantID, grantID)
}
func (p *projectScopedClient) RevokeManagedConnectionGrant(tenantID, grantID string) error {
	manager, ok := p.inner.(ManagedTenantClient)
	if !ok {
		return errors.New("platform does not support managed tenants")
	}
	return manager.RevokeManagedConnectionGrant(tenantID, grantID)
}
func (p *projectScopedClient) EnsureManagedTenantBundle(req ManagedTenantBundleRequest) (*ManagedTenantBundle, error) {
	manager, ok := p.inner.(ManagedTenantClient)
	if !ok {
		return nil, errors.New("platform does not support managed tenants")
	}
	return manager.EnsureManagedTenantBundle(req)
}
func (p *projectScopedClient) GetManagedTenantBundle(tenantID, bundleID string) (*ManagedTenantBundle, error) {
	manager, ok := p.inner.(ManagedTenantClient)
	if !ok {
		return nil, errors.New("platform does not support managed tenants")
	}
	return manager.GetManagedTenantBundle(tenantID, bundleID)
}
func (p *projectScopedClient) EnsureIntegrationWebhook(req IntegrationWebhookEnsureRequest) (*IntegrationWebhookStatus, error) {
	return p.inner.EnsureIntegrationWebhook(req)
}
func (p *projectScopedClient) VerifyIntegrationWebhook(req IntegrationWebhookVerifyRequest) (*IntegrationWebhookVerifyResult, error) {
	return p.inner.VerifyIntegrationWebhook(req)
}
func (p *projectScopedClient) ListProjects() ([]PlatformProject, error) {
	return p.inner.ListProjects()
}
func (p *projectScopedClient) ExposeIngress(req IngressExposeRequest) (*IngressRoute, error) {
	if req.ProjectID == "" {
		req.ProjectID = p.projectID
	}
	return p.inner.ExposeIngress(req)
}
func (p *projectScopedClient) UnexposeIngress(hostname string) error {
	return p.inner.UnexposeIngress(hostname)
}
func (p *projectScopedClient) ListIngressRoutes() ([]IngressRoute, error) {
	return p.inner.ListIngressRoutes()
}
func (p *projectScopedClient) ListDomainGrants() ([]DomainGrant, error) {
	return p.inner.ListDomainGrants()
}
func (p *projectScopedClient) UpsertDNSRecord(req DNSRecordRequest) (*DNSRecordResult, error) {
	if req.ProjectID == "" {
		req.ProjectID = p.projectID
	}
	return p.inner.UpsertDNSRecord(req)
}
func (p *projectScopedClient) DeleteDNSRecord(req DNSRecordRequest) (*DNSRecordResult, error) {
	if req.ProjectID == "" {
		req.ProjectID = p.projectID
	}
	return p.inner.DeleteDNSRecord(req)
}
func (p *projectScopedClient) SpawnRealtimeThread(req RealtimeSpawnRequest) (*RealtimeSpawnResult, error) {
	return p.inner.SpawnRealtimeThread(req)
}
func (p *projectScopedClient) SpawnThread(req ThreadSpawnRequest) (*ThreadSpawnResult, error) {
	client, ok := p.inner.(ThreadClient)
	if !ok {
		return nil, errors.New("thread API unavailable")
	}
	if req.ProjectID == "" {
		req.ProjectID = p.projectID
	}
	return client.SpawnThread(req)
}
func (p *projectScopedClient) EnsureThread(req ThreadEnsureRequest) (*ThreadEnsureResult, error) {
	client, ok := p.inner.(ThreadProfileClient)
	if !ok {
		return nil, errors.New("thread profile API unavailable")
	}
	if req.ProjectID == "" {
		req.ProjectID = p.projectID
	}
	return client.EnsureThread(req)
}
func (p *projectScopedClient) RenewRealtimeAudioBridge(agentID int64, threadID string) (*RealtimeSpawnResult, error) {
	return p.inner.RenewRealtimeAudioBridge(agentID, threadID)
}

func (p *projectScopedClient) KillThread(agentID int64, threadID string) error {
	return p.inner.KillThread(agentID, threadID)
}
func (p *projectScopedClient) PlatformInfo() (*PlatformInfo, error) {
	return p.inner.PlatformInfo()
}
func (p *projectScopedClient) ListEnvironments() ([]EnvironmentSummary, error) {
	return p.inner.ListEnvironments()
}
func (p *projectScopedClient) CreateEnvironment(req EnvironmentCreateRequest) (*EnvironmentSummary, error) {
	if req.ProjectID == "" {
		req.ProjectID = p.projectID
	}
	return p.inner.CreateEnvironment(req)
}
func (p *projectScopedClient) GetEnvironment(id string) (*EnvironmentSummary, error) {
	return p.inner.GetEnvironment(id)
}
func (p *projectScopedClient) DestroyEnvironment(id string) error {
	return p.inner.DestroyEnvironment(id)
}
func (p *projectScopedClient) SeedEnvironment(id string, calls []EnvironmentSeedCall, seedBaseDir string) ([]json.RawMessage, error) {
	return p.inner.SeedEnvironment(id, calls, seedBaseDir)
}
func (p *projectScopedClient) CallEnvironmentApp(environmentID, appName, tool string, input map[string]any) (json.RawMessage, error) {
	return p.inner.CallEnvironmentApp(environmentID, appName, tool, input)
}
func (p *projectScopedClient) CallEnvironmentAppResult(environmentID, appName, tool string, input map[string]any, out any) error {
	return p.inner.CallEnvironmentAppResult(environmentID, appName, tool, input, out)
}
func (p *projectScopedClient) SnapshotEnvironment(environmentID string, req EnvironmentSnapshotRequest) (*EnvironmentSnapshot, error) {
	return p.inner.SnapshotEnvironment(environmentID, req)
}
func (p *projectScopedClient) ListEnvironmentAgents(environmentID string) ([]EnvironmentAgent, error) {
	return p.inner.ListEnvironmentAgents(environmentID)
}
func (p *projectScopedClient) SpawnEnvironmentAgent(environmentID string, req EnvironmentAgentSpawnRequest) (*EnvironmentAgent, error) {
	return p.inner.SpawnEnvironmentAgent(environmentID, req)
}
func (p *projectScopedClient) StopEnvironmentAgent(environmentID string, agentOrAlias string) error {
	return p.inner.StopEnvironmentAgent(environmentID, agentOrAlias)
}

// --- low-level helpers -------------------------------------------------------

func (c *httpPlatformClient) get(path string, out any) error {
	req, _ := http.NewRequest(http.MethodGet, c.baseURL+path, nil)
	c.addAuth(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return c.platformErr(resp)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *httpPlatformClient) post(path string, body any, out any) error {
	return c.postWith(c.client, path, body, out)
}

func (c *httpPlatformClient) postWith(client *http.Client, path string, body any, out any) error {
	return c.postWithContext(context.Background(), client, path, body, out)
}

func (c *httpPlatformClient) postWithContext(ctx context.Context, client *http.Client, path string, body any, out any, responseLimit ...int64) error {
	var br io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		br = bytes.NewReader(buf)
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+path, br)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	setAppCallDeadline(req)
	c.addAuth(req)
	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return c.platformErr(resp)
	}
	if out == nil {
		return nil
	}
	if len(responseLimit) > 0 {
		data, err := io.ReadAll(io.LimitReader(resp.Body, responseLimit[0]+1))
		if err != nil {
			return err
		}
		if int64(len(data)) > responseLimit[0] {
			return errors.New("app batch response too large")
		}
		return json.Unmarshal(data, out)
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *httpPlatformClient) put(path string, body any, out any) error {
	var br io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		br = bytes.NewReader(buf)
	}
	req, _ := http.NewRequest(http.MethodPut, c.baseURL+path, br)
	req.Header.Set("Content-Type", "application/json")
	c.addAuth(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return c.platformErr(resp)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *httpPlatformClient) deleteWithBody(path string, body any, out any) error {
	var br io.Reader
	if body != nil {
		buf, err := json.Marshal(body)
		if err != nil {
			return err
		}
		br = bytes.NewReader(buf)
	}
	req, _ := http.NewRequest(http.MethodDelete, c.baseURL+path, br)
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	c.addAuth(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return c.platformErr(resp)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *httpPlatformClient) delete(path string, out any) error {
	req, _ := http.NewRequest(http.MethodDelete, c.baseURL+path, nil)
	c.addAuth(req)
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return c.platformErr(resp)
	}
	if out == nil {
		return nil
	}
	return json.NewDecoder(resp.Body).Decode(out)
}

func (c *httpPlatformClient) addAuth(req *http.Request) {
	if c.token == "" {
		c.token = os.Getenv("APTEVA_APP_TOKEN")
	}
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	if c.userSession != "" && strings.HasPrefix(req.URL.Path, "/api/apps/callback/") {
		req.Header.Set("X-Apteva-User-Session", c.userSession)
	}
	req.Header.Set("X-Apteva-App-Install-ID", os.Getenv("APTEVA_INSTALL_ID"))
	// When this sidecar runs inside a test Environment, forward the
	// environment id so the platform routes its integration calls to that
	// environment's interceptor. Empty/unset in production is a no-op.
	if eid := os.Getenv("APTEVA_ENVIRONMENT_ID"); eid != "" {
		req.Header.Set("X-Apteva-Environment-Id", eid)
	}
}

func (c *httpPlatformClient) platformErr(resp *http.Response) error {
	body, _ := io.ReadAll(resp.Body)
	return fmt.Errorf("platform %s: http %d: %s", resp.Request.URL.Path, resp.StatusCode, string(body))
}
