package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const (
	a2aMinimumVersion = "0.4.0"
	a2aManifestURL    = "https://raw.githubusercontent.com/apteva/apps/a2a/v0.4.0/mcp/a2a/apteva.yaml"
	a2aReconcileEvery = "@every 10m"
)

type fleetA2ANode struct {
	NodeID    string `json:"node_id"`
	Name      string `json:"name"`
	PublicURL string `json:"public_url"`
}

type fleetA2APeer struct {
	ID             string   `json:"id"`
	Name           string   `json:"name"`
	BaseURL        string   `json:"base_url"`
	Token          string   `json:"token"`
	DiscoverAgents []string `json:"discover_agents,omitempty"`
	InvokeAgents   []string `json:"invoke_agents,omitempty"`
}

type tenantAppInstall struct {
	InstallID           int64  `json:"install_id"`
	Name                string `json:"name"`
	Version             string `json:"version"`
	ProjectID           string `json:"project_id"`
	Status              string `json:"status"`
	ErrorMessage        string `json:"error_message"`
	DefaultForNewAgents bool   `json:"default_for_new_agents"`
}

type tenantMCPServer struct {
	ID         int64  `json:"id"`
	Source     string `json:"source"`
	UpstreamID string `json:"upstream_id"`
}

type tenantAgent struct {
	ID int64 `json:"id"`
}

type a2aPairResult struct {
	Status               string `json:"status"`
	TenantInstallID      int64  `json:"tenant_install_id,omitempty"`
	TenantAgentsAttached int    `json:"tenant_agents_attached,omitempty"`
	MainNodeID           string `json:"main_node_id,omitempty"`
	MainPeerID           string `json:"main_peer_id,omitempty"`
	TenantPeerID         string `json:"tenant_peer_id,omitempty"`
	TenantURL            string `json:"tenant_url,omitempty"`
	Changed              bool   `json:"changed,omitempty"`
	Reason               string `json:"reason,omitempty"`
}

// reconcileTenantA2ABestEffort makes A2A an optional Fleet capability: a
// pairing failure is visible in the response and event log, but never turns a
// successfully provisioned or started tenant into a failed tenant.
func (a *App) reconcileTenantA2ABestEffort(ctx *sdk.AppCtx, tenantID, actor string) a2aPairResult {
	result := a.reconcileTenantA2A(ctx, tenantID)
	if result.Status == "error" {
		_ = a.store.recordEvent(tenantID, "a2a_pair_failed", actor, result)
		if ctx != nil {
			ctx.Logger().Warn("fleet: A2A reconciliation failed", "tenant", tenantID, "err", result.Reason)
		}
	}
	return result
}

func (a *App) mainA2ANode(ctx *sdk.AppCtx) (*fleetA2ANode, bool, error) {
	if ctx == nil || ctx.PlatformAPI() == nil {
		return nil, false, nil
	}
	identity, err := ctx.PlatformAPI().WhoAmI()
	if err != nil {
		return nil, false, fmt.Errorf("inspect Fleet app bindings: %w", err)
	}
	if identity == nil || !selectedAppBinding(identity.Bindings["a2a"]) {
		return nil, false, nil
	}
	var node fleetA2ANode
	if err := ctx.PlatformAPI().CallAppResult("a2a", "node_info", map[string]any{}, &node); err != nil {
		return nil, true, fmt.Errorf("read parent A2A node: %w", err)
	}
	node.NodeID = strings.TrimSpace(node.NodeID)
	node.Name = strings.TrimSpace(node.Name)
	node.PublicURL = strings.TrimRight(strings.TrimSpace(node.PublicURL), "/")
	if node.NodeID == "" || node.PublicURL == "" {
		return nil, true, errors.New("parent A2A node_info returned no node_id or public_url")
	}
	if node.Name == "" {
		node.Name = "Parent"
	}
	return &node, true, nil
}

func selectedAppBinding(raw any) bool {
	switch value := raw.(type) {
	case int:
		return value > 0
	case int64:
		return value > 0
	case float64:
		return value > 0
	case json.Number:
		n, _ := value.Int64()
		return n > 0
	case string:
		n, _ := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
		return n > 0
	default:
		return false
	}
}

func (a *App) reconcileTenantA2A(ctx *sdk.AppCtx, tenantID string) a2aPairResult {
	node, enabled, err := a.mainA2ANode(ctx)
	if !enabled {
		return a2aPairResult{Status: "disabled", Reason: "parent Fleet install has no optional A2A binding"}
	}
	if err != nil {
		return a2aPairResult{Status: "error", Reason: err.Error()}
	}
	return a.reconcileTenantA2AWithNode(ctx, tenantID, node)
}

func (a *App) reconcileTenantA2AWithNode(ctx *sdk.AppCtx, tenantID string, node *fleetA2ANode) a2aPairResult {
	lock := a.a2aTenantLock(tenantID)
	lock.Lock()
	defer lock.Unlock()
	t, encryptedKey, err := a.store.get(tenantID)
	if err != nil {
		return a2aPairResult{Status: "error", Reason: err.Error()}
	}
	if t.Kind != KindLocal {
		return a2aPairResult{Status: "skipped", Reason: "external tenant_connect instances are not modified automatically"}
	}
	if t.Status == StatusSetupPending || t.Status == StatusStarting {
		return a2aPairResult{Status: "pending", Reason: "tenant admin API key is not ready"}
	}
	key, err := a.keys.open(encryptedKey)
	if err != nil {
		return a2aPairResult{Status: "error", Reason: "decrypt tenant api_key: " + err.Error()}
	}
	baseURL, err := a.internalTenantBaseURL(ctx, t)
	if err != nil {
		return a2aPairResult{Status: "error", Reason: "open tenant control channel: " + err.Error()}
	}
	tenantA2AURL := strings.TrimRight(baseURL, "/") + "/api/apps/a2a"
	mainRules, err := fleetA2ARules(ctx, "a2a_main_agents_json")
	if err != nil {
		return a2aPairResult{Status: "error", Reason: err.Error()}
	}
	tenantRules, err := fleetA2ARules(ctx, "a2a_tenant_agents_json")
	if err != nil {
		return a2aPairResult{Status: "error", Reason: err.Error()}
	}
	token := a.keys.deriveToken("a2a-peer:" + t.ID)
	fingerprintBytes, _ := json.Marshal([]any{node.NodeID, node.PublicURL, tenantA2AURL, mainRules, tenantRules, token})
	fingerprint := string(fingerprintBytes)
	if a.a2aFingerprint(t.ID) == fingerprint {
		return a2aPairResult{Status: "paired", TenantURL: tenantA2AURL, Reason: "already reconciled"}
	}

	tenantPeerID := "fleet-main:" + node.NodeID
	tenantPeer := fleetA2APeer{
		ID: tenantPeerID, Name: node.Name, BaseURL: node.PublicURL, Token: token,
		DiscoverAgents: tenantRules, InvokeAgents: tenantRules,
	}
	install, changed, err := a.ensureTenantA2A(ctx, t, string(key), tenantA2AURL, tenantPeer)
	if err != nil {
		return a2aPairResult{Status: "error", Reason: err.Error(), TenantURL: tenantA2AURL}
	}
	attached, attachChanged, err := a.attachTenantA2A(ctx, t, string(key), install)
	if err != nil {
		return a2aPairResult{Status: "error", Reason: err.Error(), TenantInstallID: install.InstallID, TenantURL: tenantA2AURL}
	}

	mainPeerID := "fleet:" + t.ID
	var peerResult map[string]any
	if err := ctx.PlatformAPI().CallAppResult("a2a", "peer_upsert", map[string]any{
		"id": mainPeerID, "name": t.Slug, "base_url": tenantA2AURL, "token": token,
		"discover_agents": mainRules, "invoke_agents": mainRules,
	}, &peerResult); err != nil {
		return a2aPairResult{Status: "error", Reason: "register tenant on parent A2A: " + err.Error(), TenantInstallID: install.InstallID, TenantURL: tenantA2AURL}
	}
	a.setA2AFingerprint(t.ID, fingerprint)
	result := a2aPairResult{
		Status: "paired", TenantInstallID: install.InstallID, TenantAgentsAttached: attached,
		MainNodeID: node.NodeID, MainPeerID: mainPeerID, TenantPeerID: tenantPeerID,
		TenantURL: tenantA2AURL, Changed: changed || attachChanged,
	}
	_ = a.store.recordEvent(t.ID, "a2a_paired", "fleet:a2a_reconcile", result)
	return result
}

func fleetA2ARules(ctx *sdk.AppCtx, key string) ([]string, error) {
	raw := ""
	if ctx != nil {
		raw = strings.TrimSpace(ctx.Config().Get(key))
	}
	if raw == "" {
		raw = `["*"]`
	}
	var rules []string
	if err := json.Unmarshal([]byte(raw), &rules); err != nil {
		return nil, fmt.Errorf("%s must be a JSON string array: %w", key, err)
	}
	out := make([]string, 0, len(rules))
	seen := map[string]bool{}
	for _, rule := range rules {
		rule = strings.TrimSpace(rule)
		if rule != "" && !seen[rule] {
			out = append(out, rule)
			seen[rule] = true
		}
	}
	return out, nil
}

func (a *App) ensureTenantA2A(ctx *sdk.AppCtx, t *Tenant, key, publicURL string, mainPeer fleetA2APeer) (*tenantAppInstall, bool, error) {
	apps, err := listTenantApps(ctx, a, t, key)
	if err != nil {
		return nil, false, fmt.Errorf("list tenant apps: %w", err)
	}
	install := findGlobalTenantApp(apps, "a2a")
	changed := false
	peerJSON, _ := json.Marshal([]fleetA2APeer{mainPeer})
	if install == nil {
		var created struct {
			InstallID int64  `json:"install_id"`
			Status    string `json:"status"`
			Error     string `json:"error"`
		}
		baseURL, err := a.internalTenantBaseURL(ctx, t)
		if err != nil {
			return nil, false, err
		}
		body := map[string]any{
			"manifest_url": a2aManifestURL,
			"repo":         "github.com/apteva/apps",
			"ref":          "a2a/v" + a2aMinimumVersion,
			"project_id":   "",
			"config": map[string]string{
				"node_name": t.Slug, "public_url": publicURL, "peers_json": string(peerJSON),
			},
		}
		if err := tenantJSON(context.Background(), baseURL, key, http.MethodPost, "/api/apps/install", body, &created); err != nil {
			return nil, false, fmt.Errorf("install tenant A2A: %w", err)
		}
		if created.InstallID <= 0 {
			return nil, false, fmt.Errorf("install tenant A2A returned no install_id: %s", created.Error)
		}
		install = &tenantAppInstall{InstallID: created.InstallID, Name: "a2a", Version: a2aMinimumVersion, Status: created.Status}
		changed = true
	} else if !versionAtLeast(install.Version, a2aMinimumVersion) {
		baseURL, err := a.internalTenantBaseURL(ctx, t)
		if err != nil {
			return nil, false, err
		}
		if err := tenantJSON(context.Background(), baseURL, key, http.MethodPost,
			fmt.Sprintf("/api/apps/installs/%d/upgrade", install.InstallID), map[string]any{"approve_new_permissions": true}, nil); err != nil {
			return nil, false, fmt.Errorf("upgrade tenant A2A from %s: %w", install.Version, err)
		}
		changed = true
	}

	install, err = a.waitForTenantA2A(ctx, t, key, install.InstallID, 2*time.Minute)
	if err != nil {
		return nil, false, err
	}
	configChanged, err := a.reconcileTenantA2AConfig(ctx, t, key, install.InstallID, publicURL, mainPeer)
	if err != nil {
		return nil, false, err
	}
	return install, changed || configChanged, nil
}

func listTenantApps(ctx *sdk.AppCtx, a *App, t *Tenant, key string) ([]tenantAppInstall, error) {
	baseURL, err := a.internalTenantBaseURL(ctx, t)
	if err != nil {
		return nil, err
	}
	var apps []tenantAppInstall
	if err := tenantJSON(context.Background(), baseURL, key, http.MethodGet, "/api/apps", nil, &apps); err != nil {
		return nil, err
	}
	return apps, nil
}

func findGlobalTenantApp(apps []tenantAppInstall, name string) *tenantAppInstall {
	for i := range apps {
		if strings.EqualFold(apps[i].Name, name) && strings.TrimSpace(apps[i].ProjectID) == "" {
			copy := apps[i]
			return &copy
		}
	}
	return nil
}

func (a *App) waitForTenantA2A(ctx *sdk.AppCtx, t *Tenant, key string, installID int64, timeout time.Duration) (*tenantAppInstall, error) {
	deadline := time.Now().Add(timeout)
	for {
		apps, err := listTenantApps(ctx, a, t, key)
		if err == nil {
			if install := findGlobalTenantApp(apps, "a2a"); install != nil && install.InstallID == installID {
				switch install.Status {
				case "running":
					if !versionAtLeast(install.Version, a2aMinimumVersion) {
						return nil, fmt.Errorf("tenant A2A version %s is below %s", install.Version, a2aMinimumVersion)
					}
					return install, nil
				case "error":
					return nil, fmt.Errorf("tenant A2A install failed: %s", install.ErrorMessage)
				}
			}
		}
		if time.Now().After(deadline) {
			if err != nil {
				return nil, fmt.Errorf("wait for tenant A2A: %w", err)
			}
			return nil, errors.New("timeout waiting for tenant A2A to run")
		}
		time.Sleep(500 * time.Millisecond)
	}
}

func (a *App) reconcileTenantA2AConfig(ctx *sdk.AppCtx, t *Tenant, key string, installID int64, publicURL string, desired fleetA2APeer) (bool, error) {
	baseURL, err := a.internalTenantBaseURL(ctx, t)
	if err != nil {
		return false, err
	}
	var got struct {
		Config map[string]any `json:"config"`
	}
	path := fmt.Sprintf("/api/apps/installs/%d/config", installID)
	if err := tenantJSON(context.Background(), baseURL, key, http.MethodGet, path, nil, &got); err != nil {
		return false, fmt.Errorf("read tenant A2A config: %w", err)
	}
	raw, _ := got.Config["peers_json"].(string)
	peers, err := parseFleetA2APeers(raw)
	if err != nil {
		return false, fmt.Errorf("read tenant A2A peers_json: %w", err)
	}
	wantedPrefix := "fleet-main:"
	merged := make([]fleetA2APeer, 0, len(peers)+1)
	for _, peer := range peers {
		if !strings.HasPrefix(peer.ID, wantedPrefix) {
			merged = append(merged, peer)
		}
	}
	merged = append(merged, desired)
	mergedJSON, _ := json.Marshal(merged)
	currentPublic, _ := got.Config["public_url"].(string)
	if strings.TrimSpace(currentPublic) == publicURL && fleetA2APeersEqual(peers, merged) {
		return false, nil
	}
	body := map[string]any{"config": map[string]any{"public_url": publicURL, "peers_json": string(mergedJSON)}}
	if err := tenantJSON(context.Background(), baseURL, key, http.MethodPut, path, body, nil); err != nil {
		return false, fmt.Errorf("update tenant A2A config: %w", err)
	}
	return true, nil
}

func parseFleetA2APeers(raw string) ([]fleetA2APeer, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, nil
	}
	var peers []fleetA2APeer
	if err := json.Unmarshal([]byte(raw), &peers); err == nil {
		return peers, nil
	}
	var wrapper struct {
		Peers []fleetA2APeer `json:"peers"`
	}
	if err := json.Unmarshal([]byte(raw), &wrapper); err != nil {
		return nil, err
	}
	return wrapper.Peers, nil
}

func fleetA2APeersEqual(a, b []fleetA2APeer) bool {
	left, _ := json.Marshal(a)
	right, _ := json.Marshal(b)
	return string(left) == string(right)
}

func (a *App) attachTenantA2A(ctx *sdk.AppCtx, t *Tenant, key string, install *tenantAppInstall) (int, bool, error) {
	baseURL, err := a.internalTenantBaseURL(ctx, t)
	if err != nil {
		return 0, false, err
	}
	changed := false
	if !install.DefaultForNewAgents {
		path := fmt.Sprintf("/api/apps/installs/%d/agent-default", install.InstallID)
		if err := tenantJSON(context.Background(), baseURL, key, http.MethodPatch, path,
			map[string]any{"default_for_new_agents": true}, nil); err != nil {
			return 0, false, fmt.Errorf("make tenant A2A default for new agents: %w", err)
		}
		changed = true
	}
	var servers []tenantMCPServer
	if err := tenantJSON(context.Background(), baseURL, key, http.MethodGet,
		"/api/mcp-servers?include_app_owned=1", nil, &servers); err != nil {
		return 0, false, fmt.Errorf("list tenant MCP servers: %w", err)
	}
	wantUpstream := "app:" + strconv.FormatInt(install.InstallID, 10)
	var serverID int64
	for _, server := range servers {
		if server.Source == "app" && server.UpstreamID == wantUpstream {
			serverID = server.ID
			break
		}
	}
	if serverID == 0 {
		return 0, false, errors.New("tenant A2A MCP bridge is not registered")
	}
	var agents []tenantAgent
	if err := tenantJSON(context.Background(), baseURL, key, http.MethodGet, "/api/agents", nil, &agents); err != nil {
		return 0, false, fmt.Errorf("list tenant agents: %w", err)
	}
	for _, agent := range agents {
		if agent.ID <= 0 {
			continue
		}
		path := fmt.Sprintf("/api/agents/%d/mcp-servers", agent.ID)
		if err := tenantJSON(context.Background(), baseURL, key, http.MethodPost, path,
			map[string]any{"action": "add", "mcp_server_ids": []int64{serverID}}, nil); err != nil {
			return 0, changed, fmt.Errorf("attach tenant A2A to agent %d: %w", agent.ID, err)
		}
	}
	return len(agents), changed || len(agents) > 0, nil
}

func versionAtLeast(got, minimum string) bool {
	parse := func(raw string) ([3]int, bool) {
		var out [3]int
		parts := strings.Split(strings.TrimPrefix(strings.TrimSpace(raw), "v"), ".")
		if len(parts) < 3 {
			return out, false
		}
		for i := range out {
			part := parts[i]
			if cut := strings.IndexAny(part, "-+"); cut >= 0 {
				part = part[:cut]
			}
			n, err := strconv.Atoi(part)
			if err != nil || n < 0 {
				return out, false
			}
			out[i] = n
		}
		return out, true
	}
	left, ok := parse(got)
	if !ok {
		return false
	}
	right, ok := parse(minimum)
	if !ok {
		return false
	}
	for i := range left {
		if left[i] != right[i] {
			return left[i] > right[i]
		}
	}
	return true
}

func (a *App) runA2AReconciler(ctx context.Context, app *sdk.AppCtx) error {
	// Global app workers are dispatched once per project by the SDK. Fleet's
	// tenant registry is global, so coalesce those project dispatches into one
	// sweep instead of reconciling the same tenants N times per tick.
	a.a2aMu.Lock()
	if !a.a2aLastSweep.IsZero() && time.Since(a.a2aLastSweep) < time.Minute {
		a.a2aMu.Unlock()
		return nil
	}
	a.a2aLastSweep = time.Now()
	a.a2aMu.Unlock()
	node, enabled, err := a.mainA2ANode(app)
	if !enabled {
		return nil
	}
	if err != nil {
		return err
	}
	tenants, err := a.store.list(map[string]string{"kind": KindLocal})
	if err != nil {
		return err
	}
	for _, tenant := range tenants {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
		}
		if tenant.Status != StatusActive && tenant.Status != StatusDisconnected {
			continue
		}
		// The fingerprint avoids duplicate work when lifecycle hooks converge in
		// quick succession. The scheduled pass deliberately bypasses it so Fleet
		// also repairs operator drift, missing attachments, and removed peers.
		a.clearA2AFingerprint(tenant.ID)
		result := a.reconcileTenantA2AWithNode(app, tenant.ID, node)
		if result.Status == "error" {
			_ = a.store.recordEvent(tenant.ID, "a2a_pair_failed", "worker:a2a_reconcile", result)
			app.Logger().Warn("fleet: A2A reconciliation failed", "tenant", tenant.ID, "err", result.Reason)
		}
	}
	return nil
}

func (a *App) unpairTenantA2A(ctx *sdk.AppCtx, tenantID string) error {
	node, enabled, err := a.mainA2ANode(ctx)
	if !enabled {
		return nil
	}
	if err != nil {
		return err
	}
	lock := a.a2aTenantLock(tenantID)
	lock.Lock()
	defer lock.Unlock()
	var removed map[string]any
	if err := ctx.PlatformAPI().CallAppResult("a2a", "peer_remove", map[string]any{"id": "fleet:" + tenantID}, &removed); err != nil {
		return fmt.Errorf("remove parent A2A peer: %w", err)
	}
	a.clearA2AFingerprint(tenantID)
	_ = a.store.recordEvent(tenantID, "a2a_unpaired", "fleet:a2a_reconcile", map[string]any{"main_node_id": node.NodeID})
	return nil
}

func (a *App) a2aFingerprint(tenantID string) string {
	a.a2aMu.Lock()
	defer a.a2aMu.Unlock()
	return a.a2aReconciled[tenantID]
}

func (a *App) setA2AFingerprint(tenantID, fingerprint string) {
	a.a2aMu.Lock()
	defer a.a2aMu.Unlock()
	a.a2aReconciled[tenantID] = fingerprint
}

func (a *App) clearA2AFingerprint(tenantID string) {
	a.a2aMu.Lock()
	defer a.a2aMu.Unlock()
	delete(a.a2aReconciled, tenantID)
}

func (a *App) a2aTenantLock(tenantID string) *sync.Mutex {
	a.a2aMu.Lock()
	defer a.a2aMu.Unlock()
	if lock := a.a2aLocks[tenantID]; lock != nil {
		return lock
	}
	lock := &sync.Mutex{}
	a.a2aLocks[tenantID] = lock
	return lock
}
