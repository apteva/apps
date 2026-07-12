package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const tenantControlMaxBody = 10 << 20

func (a *App) toolTenantInventory(appCtx *sdk.AppCtx, args map[string]any) (any, error) {
	tenantID := getStr(args, "tenant_id")
	if tenantID == "" {
		return nil, errors.New("tenant_id is required")
	}
	t, enc, err := a.store.get(tenantID)
	if err != nil {
		return nil, err
	}
	out := map[string]any{
		"tenant": a.publicTenantView(t),
	}
	if grants, err := a.store.listDomainGrants(tenantID); err == nil {
		out["domain_grants"] = grants
	}
	if grants, err := a.store.listProviderGrants(tenantID); err == nil {
		out["provider_grants"] = grants
	}
	if t.Status == StatusSetupPending {
		out["remote_skipped"] = "tenant is setup_pending; attach an api_key before reading tenant platform inventory"
		return out, nil
	}
	key, err := a.keys.open(enc)
	if err != nil {
		return nil, fmt.Errorf("decrypt tenant api_key: %w", err)
	}
	baseURL, err := a.internalTenantBaseURL(appCtx, t)
	if err != nil {
		return nil, fmt.Errorf("open tenant control channel: %w", err)
	}
	projectID := strings.TrimSpace(getStr(args, "project_id"))
	includeUsers := boolArg(args, "include_users")
	includeCatalog := boolArg(args, "include_catalog")

	remote := map[string]any{}
	errs := map[string]string{}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	fetch := func(name, path string) {
		var v any
		if err := tenantJSON(ctx, baseURL, string(key), http.MethodGet, path, nil, &v); err != nil {
			errs[name] = err.Error()
			return
		}
		remote[name] = v
	}
	fetch("projects", "/api/projects")
	fetch("apps", tenantPathWithQuery("/api/apps", map[string]string{"project_id": projectID}))
	fetch("agents", tenantPathWithQuery("/api/instances", map[string]string{"project_id": projectID}))
	fetch("connections", tenantPathWithQuery("/api/connections", map[string]string{
		"project_id":        projectID,
		"include_app_owned": "1",
	}))
	fetch("mcp_servers", tenantPathWithQuery("/api/mcp-servers", map[string]string{
		"project_id":        projectID,
		"include_app_owned": "1",
	}))
	if includeUsers {
		fetch("users", "/api/users")
	}
	if includeCatalog {
		fetch("integrations", "/api/integrations/catalog")
	}
	out["remote"] = remote
	if len(errs) > 0 {
		out["errors"] = errs
	}
	return out, nil
}

func (a *App) toolTenantAppTools(appCtx *sdk.AppCtx, args map[string]any) (any, error) {
	tenantID := getStr(args, "tenant_id")
	if tenantID == "" {
		return nil, errors.New("tenant_id is required")
	}
	installID := int64Arg(args, "install_id")
	appName := strings.TrimSpace(getStr(args, "app"))
	projectID := strings.TrimSpace(getStr(args, "project_id"))
	t, key, err := a.tenantControlAuth(tenantID)
	if err != nil {
		return nil, err
	}
	if installID > 0 && appName == "" {
		var tools any
		path := fmt.Sprintf("/api/apps/installs/%d/tools", installID)
		baseURL, err := a.internalTenantBaseURL(appCtx, t)
		if err != nil {
			return nil, err
		}
		if err := tenantJSON(context.Background(), baseURL, key, http.MethodGet, path, nil, &tools); err != nil {
			return nil, err
		}
		return map[string]any{"tenant_id": tenantID, "install_id": installID, "tools": tools}, nil
	}
	if appName == "" {
		return nil, errors.New("app is required unless install_id is provided")
	}
	result, err := a.tenantAppRPC(appCtx, t, key, appName, installID, projectID, "tools/list", map[string]any{})
	if err != nil {
		return nil, err
	}
	if m, ok := result.(map[string]any); ok {
		if tools, ok := m["tools"]; ok {
			return map[string]any{
				"tenant_id":  tenantID,
				"app":        appName,
				"install_id": installID,
				"project_id": projectID,
				"tools":      tools,
			}, nil
		}
	}
	return result, nil
}

func (a *App) toolTenantAppCall(appCtx *sdk.AppCtx, args map[string]any) (any, error) {
	tenantID := getStr(args, "tenant_id")
	appName := strings.TrimSpace(getStr(args, "app"))
	tool := strings.TrimSpace(getStr(args, "tool"))
	if tenantID == "" || appName == "" || tool == "" {
		return nil, errors.New("tenant_id, app, tool are required")
	}
	input := mapArg(args, "arguments")
	if input == nil {
		input = mapArg(args, "input")
	}
	if input == nil {
		input = map[string]any{}
	}
	installID := int64Arg(args, "install_id")
	projectID := strings.TrimSpace(getStr(args, "project_id"))
	t, key, err := a.tenantControlAuth(tenantID)
	if err != nil {
		return nil, err
	}
	result, err := a.tenantAppRPC(appCtx, t, key, appName, installID, projectID, "tools/call", map[string]any{
		"name":      tool,
		"arguments": input,
	})
	if err != nil {
		return nil, err
	}
	_ = a.store.recordEvent(tenantID, "remote_call", "user", map[string]any{
		"app":        appName,
		"tool":       tool,
		"install_id": installID,
		"project_id": projectID,
	})
	return result, nil
}

func (a *App) toolTenantPlatformCall(appCtx *sdk.AppCtx, args map[string]any) (any, error) {
	tenantID := getStr(args, "tenant_id")
	resource := normalizeControlToken(getStr(args, "resource"))
	action := normalizeControlToken(getStr(args, "action"))
	if tenantID == "" || resource == "" || action == "" {
		return nil, errors.New("tenant_id, resource, action are required")
	}
	callArgs := mapArg(args, "arguments")
	if callArgs == nil {
		callArgs = map[string]any{}
	}
	method, path, body, err := tenantPlatformRoute(resource, action, callArgs)
	if err != nil {
		return nil, err
	}
	t, key, err := a.tenantControlAuth(tenantID)
	if err != nil {
		return nil, err
	}
	var result any
	baseURL, err := a.internalTenantBaseURL(appCtx, t)
	if err != nil {
		return nil, err
	}
	if err := tenantJSON(context.Background(), baseURL, key, method, path, body, &result); err != nil {
		return nil, err
	}
	_ = a.store.recordEvent(tenantID, "platform_call", "user", map[string]any{
		"resource": resource,
		"action":   action,
		"method":   method,
		"path":     pathForAudit(path),
	})
	return map[string]any{
		"tenant_id": tenantID,
		"resource":  resource,
		"action":    action,
		"result":    result,
	}, nil
}

func (a *App) tenantControlAuth(tenantID string) (*Tenant, string, error) {
	t, enc, err := a.store.get(tenantID)
	if err != nil {
		return nil, "", err
	}
	if t.Status == StatusSetupPending {
		return nil, "", errors.New("tenant is in setup_pending; call tenant_attach_key before tenant control calls")
	}
	key, err := a.keys.open(enc)
	if err != nil {
		return nil, "", fmt.Errorf("decrypt tenant api_key: %w", err)
	}
	return t, string(key), nil
}

func (a *App) tenantAppRPC(appCtx *sdk.AppCtx, t *Tenant, key, appName string, installID int64, projectID, method string, params map[string]any) (any, error) {
	if params == nil {
		params = map[string]any{}
	}
	q := url.Values{}
	if installID > 0 {
		q.Set("install_id", strconv.FormatInt(installID, 10))
	}
	if strings.TrimSpace(projectID) != "" {
		q.Set("project_id", strings.TrimSpace(projectID))
	}
	path := "/api/apps/" + url.PathEscape(appName) + "/mcp"
	if encoded := q.Encode(); encoded != "" {
		path += "?" + encoded
	}
	payload, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  method,
		"params":  params,
	})
	baseURL, err := a.internalTenantBaseURL(appCtx, t)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, baseURL+path, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+key)
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, tenantControlMaxBody))
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("tenant returned %d: %s", resp.StatusCode, string(raw))
	}
	return unwrapMCP(raw)
}

func tenantPlatformRoute(resource, action string, args map[string]any) (string, string, any, error) {
	switch resource {
	case "app", "apps":
		return tenantAppsRoute(action, args)
	case "agent", "agents", "instance", "instances":
		return tenantAgentsRoute(action, args)
	case "project", "projects":
		return tenantProjectsRoute(action, args)
	case "user", "users":
		return tenantUsersRoute(action, args)
	case "integration", "integrations":
		return tenantIntegrationsRoute(action, args)
	case "connection", "connections":
		return tenantConnectionsRoute(action, args)
	case "mcp_server", "mcp_servers":
		return tenantMCPServersRoute(action, args)
	default:
		return "", "", nil, fmt.Errorf("unsupported tenant platform resource %q", resource)
	}
}

func tenantAppsRoute(action string, args map[string]any) (string, string, any, error) {
	switch action {
	case "list":
		return http.MethodGet, tenantPathWithQuery("/api/apps", pickQuery(args, "project_id")), nil, nil
	case "marketplace":
		return http.MethodGet, tenantPathWithQuery("/api/apps/marketplace", pickQuery(args, "project_id", "registry_url")), nil, nil
	case "install":
		return http.MethodPost, "/api/apps/install", bodyWithout(args), nil
	case "preflight":
		return http.MethodPost, "/api/apps/install/preflight", bodyWithout(args), nil
	case "upgrade":
		id, err := requiredID(args, "install_id", "id")
		if err != nil {
			return "", "", nil, err
		}
		return http.MethodPost, "/api/apps/installs/" + id + "/upgrade", bodyWithout(args, "install_id", "id"), nil
	case "uninstall", "delete":
		id, err := requiredID(args, "install_id", "id")
		if err != nil {
			return "", "", nil, err
		}
		if !boolArg(args, "confirm") {
			return "", "", nil, errors.New("confirm=true is required for app uninstall")
		}
		return http.MethodDelete, tenantPathWithQuery("/api/apps/installs/"+id, pickQuery(args, "force")), nil, nil
	case "tools":
		id, err := requiredID(args, "install_id", "id")
		if err != nil {
			return "", "", nil, err
		}
		return http.MethodGet, "/api/apps/installs/" + id + "/tools", nil, nil
	case "config_get", "get_config":
		id, err := requiredID(args, "install_id", "id")
		if err != nil {
			return "", "", nil, err
		}
		return http.MethodGet, "/api/apps/installs/" + id + "/config", nil, nil
	case "config_set", "set_config", "update_config":
		id, err := requiredID(args, "install_id", "id")
		if err != nil {
			return "", "", nil, err
		}
		body := mapArg(args, "config")
		if body == nil {
			body = bodyWithout(args, "install_id", "id")
		}
		return http.MethodPut, "/api/apps/installs/" + id + "/config", body, nil
	case "bindings_set", "set_bindings", "bind":
		id, err := requiredID(args, "install_id", "id")
		if err != nil {
			return "", "", nil, err
		}
		body := mapArg(args, "bindings")
		if body == nil {
			body = bodyWithout(args, "install_id", "id")
		}
		return http.MethodPut, "/api/apps/installs/" + id + "/bindings", body, nil
	case "scope_set", "set_scope":
		id, err := requiredID(args, "install_id", "id")
		if err != nil {
			return "", "", nil, err
		}
		return http.MethodPatch, "/api/apps/installs/" + id + "/scope", bodyWithout(args, "install_id", "id"), nil
	default:
		return "", "", nil, fmt.Errorf("unsupported apps action %q", action)
	}
}

func tenantAgentsRoute(action string, args map[string]any) (string, string, any, error) {
	switch action {
	case "list":
		return http.MethodGet, tenantPathWithQuery("/api/instances", pickQuery(args, "project_id")), nil, nil
	case "create":
		return http.MethodPost, "/api/instances", bodyWithout(args), nil
	case "get":
		id, err := requiredID(args, "agent_id", "instance_id", "id")
		if err != nil {
			return "", "", nil, err
		}
		return http.MethodGet, "/api/instances/" + id, nil, nil
	case "config_get", "get_config":
		id, err := requiredID(args, "agent_id", "instance_id", "id")
		if err != nil {
			return "", "", nil, err
		}
		return http.MethodGet, "/api/instances/" + id + "/config", nil, nil
	case "update", "config_set", "set_config":
		id, err := requiredID(args, "agent_id", "instance_id", "id")
		if err != nil {
			return "", "", nil, err
		}
		body := mapArg(args, "config")
		if body == nil {
			body = bodyWithout(args, "agent_id", "instance_id", "id")
		}
		return http.MethodPut, "/api/instances/" + id + "/config", body, nil
	case "start", "stop", "restart":
		id, err := requiredID(args, "agent_id", "instance_id", "id")
		if err != nil {
			return "", "", nil, err
		}
		return http.MethodPost, "/api/instances/" + id + "/" + action, bodyWithout(args, "agent_id", "instance_id", "id"), nil
	case "delete":
		id, err := requiredID(args, "agent_id", "instance_id", "id")
		if err != nil {
			return "", "", nil, err
		}
		if !boolArg(args, "confirm") {
			return "", "", nil, errors.New("confirm=true is required for agent delete")
		}
		return http.MethodDelete, "/api/instances/" + id, nil, nil
	default:
		return "", "", nil, fmt.Errorf("unsupported agents action %q", action)
	}
}

func tenantProjectsRoute(action string, args map[string]any) (string, string, any, error) {
	switch action {
	case "list":
		return http.MethodGet, "/api/projects", nil, nil
	case "create":
		return http.MethodPost, "/api/projects", bodyWithout(args), nil
	case "get":
		id, err := requiredID(args, "project_id", "id")
		if err != nil {
			return "", "", nil, err
		}
		return http.MethodGet, "/api/projects/" + url.PathEscape(id), nil, nil
	case "update":
		id, err := requiredID(args, "project_id", "id")
		if err != nil {
			return "", "", nil, err
		}
		return http.MethodPut, "/api/projects/" + url.PathEscape(id), bodyWithout(args, "project_id", "id"), nil
	case "delete":
		id, err := requiredID(args, "project_id", "id")
		if err != nil {
			return "", "", nil, err
		}
		if !boolArg(args, "confirm") {
			return "", "", nil, errors.New("confirm=true is required for project delete")
		}
		return http.MethodDelete, "/api/projects/" + url.PathEscape(id), nil, nil
	default:
		return "", "", nil, fmt.Errorf("unsupported projects action %q", action)
	}
}

func tenantUsersRoute(action string, args map[string]any) (string, string, any, error) {
	switch action {
	case "list":
		return http.MethodGet, "/api/users", nil, nil
	case "create":
		return http.MethodPost, "/api/users", bodyWithout(args), nil
	case "get":
		id, err := requiredID(args, "user_id", "id")
		if err != nil {
			return "", "", nil, err
		}
		return http.MethodGet, "/api/users/" + id, nil, nil
	case "reset_password", "password_reset":
		id, err := requiredID(args, "user_id", "id")
		if err != nil {
			return "", "", nil, err
		}
		body := bodyWithout(args, "user_id", "id")
		if _, ok := body["new_password"]; !ok {
			if password := getStr(args, "password"); password != "" {
				body["new_password"] = password
			}
		}
		return http.MethodPatch, "/api/users/" + id + "/password", body, nil
	case "delete":
		id, err := requiredID(args, "user_id", "id")
		if err != nil {
			return "", "", nil, err
		}
		if !boolArg(args, "confirm") {
			return "", "", nil, errors.New("confirm=true is required for user delete")
		}
		return http.MethodDelete, "/api/users/" + id, nil, nil
	default:
		return "", "", nil, fmt.Errorf("unsupported users action %q", action)
	}
}

func tenantIntegrationsRoute(action string, args map[string]any) (string, string, any, error) {
	switch action {
	case "list":
		return http.MethodGet, tenantPathWithQuery("/api/integrations/catalog", pickQuery(args, "query")), nil, nil
	case "get":
		slug := strings.TrimSpace(getStr(args, "slug"))
		if slug == "" {
			return "", "", nil, errors.New("slug is required")
		}
		return http.MethodGet, "/api/integrations/catalog/" + url.PathEscape(slug), nil, nil
	default:
		return "", "", nil, fmt.Errorf("unsupported integrations action %q", action)
	}
}

func tenantConnectionsRoute(action string, args map[string]any) (string, string, any, error) {
	switch action {
	case "list":
		return http.MethodGet, tenantPathWithQuery("/api/connections", pickQuery(args, "project_id", "include_app_owned", "include")), nil, nil
	case "create":
		return http.MethodPost, "/api/connections", bodyWithout(args), nil
	case "get":
		id, err := requiredID(args, "connection_id", "id")
		if err != nil {
			return "", "", nil, err
		}
		return http.MethodGet, "/api/connections/" + id, nil, nil
	case "rename", "update":
		id, err := requiredID(args, "connection_id", "id")
		if err != nil {
			return "", "", nil, err
		}
		return http.MethodPatch, "/api/connections/" + id, bodyWithout(args, "connection_id", "id"), nil
	case "delete":
		id, err := requiredID(args, "connection_id", "id")
		if err != nil {
			return "", "", nil, err
		}
		if !boolArg(args, "confirm") {
			return "", "", nil, errors.New("confirm=true is required for connection delete")
		}
		return http.MethodDelete, "/api/connections/" + id, nil, nil
	case "tools":
		id, err := requiredID(args, "connection_id", "id")
		if err != nil {
			return "", "", nil, err
		}
		return http.MethodGet, "/api/connections/" + id + "/tools", nil, nil
	case "test":
		id, err := requiredID(args, "connection_id", "id")
		if err != nil {
			return "", "", nil, err
		}
		return http.MethodPost, "/api/connections/" + id + "/test", bodyWithout(args, "connection_id", "id"), nil
	case "execute":
		id, err := requiredID(args, "connection_id", "id")
		if err != nil {
			return "", "", nil, err
		}
		return http.MethodPost, "/api/connections/" + id + "/execute", bodyWithout(args, "connection_id", "id"), nil
	case "mcp_create", "create_mcp_server":
		id, err := requiredID(args, "connection_id", "id")
		if err != nil {
			return "", "", nil, err
		}
		return http.MethodPost, "/api/connections/" + id + "/mcp", bodyWithout(args, "connection_id", "id"), nil
	default:
		return "", "", nil, fmt.Errorf("unsupported connections action %q", action)
	}
}

func tenantMCPServersRoute(action string, args map[string]any) (string, string, any, error) {
	switch action {
	case "list":
		return http.MethodGet, tenantPathWithQuery("/api/mcp-servers", pickQuery(args, "project_id", "include_app_owned")), nil, nil
	case "create":
		return http.MethodPost, "/api/mcp-servers", bodyWithout(args), nil
	case "tools":
		id, err := requiredID(args, "mcp_server_id", "server_id", "id")
		if err != nil {
			return "", "", nil, err
		}
		return http.MethodGet, "/api/mcp-servers/" + id + "/tools", nil, nil
	case "update_tools", "tools_set":
		id, err := requiredID(args, "mcp_server_id", "server_id", "id")
		if err != nil {
			return "", "", nil, err
		}
		return http.MethodPut, "/api/mcp-servers/" + id + "/tools", bodyWithout(args, "mcp_server_id", "server_id", "id"), nil
	case "call_tool":
		id, err := requiredID(args, "mcp_server_id", "server_id", "id")
		if err != nil {
			return "", "", nil, err
		}
		return http.MethodPost, "/api/mcp-servers/" + id + "/call-tool", bodyWithout(args, "mcp_server_id", "server_id", "id"), nil
	case "start", "stop":
		id, err := requiredID(args, "mcp_server_id", "server_id", "id")
		if err != nil {
			return "", "", nil, err
		}
		return http.MethodPost, "/api/mcp-servers/" + id + "/" + action, nil, nil
	case "delete":
		id, err := requiredID(args, "mcp_server_id", "server_id", "id")
		if err != nil {
			return "", "", nil, err
		}
		if !boolArg(args, "confirm") {
			return "", "", nil, errors.New("confirm=true is required for MCP server delete")
		}
		return http.MethodDelete, "/api/mcp-servers/" + id, nil, nil
	default:
		return "", "", nil, fmt.Errorf("unsupported mcp_servers action %q", action)
	}
}

func tenantPathWithQuery(path string, vals map[string]string) string {
	q := url.Values{}
	for k, v := range vals {
		v = strings.TrimSpace(v)
		if v == "" {
			continue
		}
		q.Set(k, v)
	}
	if encoded := q.Encode(); encoded != "" {
		return path + "?" + encoded
	}
	return path
}

func pickQuery(args map[string]any, keys ...string) map[string]string {
	out := map[string]string{}
	for _, key := range keys {
		if v := argString(args, key); v != "" {
			out[key] = v
		}
	}
	return out
}

func requiredID(args map[string]any, keys ...string) (string, error) {
	for _, key := range keys {
		if v := argString(args, key); v != "" {
			return url.PathEscape(v), nil
		}
	}
	return "", fmt.Errorf("%s is required", keys[0])
}

func argString(args map[string]any, key string) string {
	if args == nil {
		return ""
	}
	switch v := args[key].(type) {
	case string:
		return strings.TrimSpace(v)
	case float64:
		return strconv.FormatInt(int64(v), 10)
	case int:
		return strconv.Itoa(v)
	case int64:
		return strconv.FormatInt(v, 10)
	case bool:
		if v {
			return "1"
		}
		return "0"
	default:
		return ""
	}
}

func mapArg(args map[string]any, key string) map[string]any {
	if args == nil {
		return nil
	}
	if m, ok := args[key].(map[string]any); ok {
		return m
	}
	return nil
}

func boolArg(args map[string]any, key string) bool {
	if args == nil {
		return false
	}
	switch v := args[key].(type) {
	case bool:
		return v
	case string:
		v = strings.TrimSpace(strings.ToLower(v))
		return v == "true" || v == "1" || v == "yes"
	case float64:
		return v != 0
	case int:
		return v != 0
	case int64:
		return v != 0
	default:
		return false
	}
}

func bodyWithout(args map[string]any, skip ...string) map[string]any {
	if body := mapArg(args, "body"); body != nil {
		return body
	}
	skipped := map[string]bool{"confirm": true}
	for _, k := range skip {
		skipped[k] = true
	}
	out := map[string]any{}
	for k, v := range args {
		if skipped[k] {
			continue
		}
		out[k] = v
	}
	return out
}

func normalizeControlToken(v string) string {
	v = strings.TrimSpace(strings.ToLower(v))
	v = strings.ReplaceAll(v, "-", "_")
	return v
}

func pathForAudit(path string) string {
	if i := strings.Index(path, "?"); i >= 0 {
		return path[:i]
	}
	return path
}
