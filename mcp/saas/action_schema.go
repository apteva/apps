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
	"os"
	"strings"
	"sync"
	"time"
)

// SaaS v0.13.2 -- send idempotency_key to an app action only when the target
// tool declares it.
//
// Every fulfillment run has a deterministic idempotency_key. SaaS used to add
// it to every app action's input, but tools that reject unknown fields (for
// example containers_run) then failed every call. SaaS now reads the target
// app's tool list (MCP tools/list through the platform's bound app proxy) and
// passes the key only to tools whose input schema lists idempotency_key. When
// the list cannot be read, the key is left out: SaaS reserves every run before
// calling the target, so a retry stays safe without it.

const (
	actionSchemaTTL        = 10 * time.Minute
	actionSchemaFailureTTL = time.Minute
	actionSchemaTimeout    = 10 * time.Second
	actionSchemaMaxBytes   = 4 << 20
)

// appToolSchemas maps a tool name to its input schema.
type appToolSchemas map[string]map[string]any

type actionSchemaEntry struct {
	tools   appToolSchemas
	err     error
	expires time.Time
}

var (
	actionSchemaMu    sync.Mutex
	actionSchemaCache = map[string]actionSchemaEntry{}
	// lookupAppToolSchemas is replaced in tests.
	lookupAppToolSchemas = fetchAppToolSchemas
)

// actionAcceptsIdempotencyKey reports whether appName's tool declares
// idempotency_key in its input schema.
func actionAcceptsIdempotencyKey(pid, appName, tool string) bool {
	tools, err := cachedAppToolSchemas(pid, appName)
	if err != nil {
		return false
	}
	return schemaDeclaresProperty(tools[tool], "idempotency_key")
}

func cachedAppToolSchemas(pid, appName string) (appToolSchemas, error) {
	key := pid + "\x00" + appName
	now := time.Now()
	actionSchemaMu.Lock()
	entry, ok := actionSchemaCache[key]
	actionSchemaMu.Unlock()
	if ok && now.Before(entry.expires) {
		return entry.tools, entry.err
	}
	tools, err := lookupAppToolSchemas(pid, appName)
	ttl := actionSchemaTTL
	if err != nil {
		ttl = actionSchemaFailureTTL
	}
	actionSchemaMu.Lock()
	actionSchemaCache[key] = actionSchemaEntry{tools: tools, err: err, expires: now.Add(ttl)}
	actionSchemaMu.Unlock()
	return tools, err
}

func resetActionSchemaCache() {
	actionSchemaMu.Lock()
	actionSchemaCache = map[string]actionSchemaEntry{}
	actionSchemaMu.Unlock()
}

func schemaDeclaresProperty(schema map[string]any, name string) bool {
	props := mapFromAny(schema["properties"])
	_, ok := props[name]
	return ok
}

// fetchAppToolSchemas asks the bound app for its tool list through the
// platform's app proxy, the same route other apps use to reach a bound app.
func fetchAppToolSchemas(pid, appName string) (appToolSchemas, error) {
	base := strings.TrimRight(os.Getenv("APTEVA_GATEWAY_URL"), "/")
	token := strings.TrimSpace(os.Getenv("APTEVA_APP_TOKEN"))
	if base == "" || token == "" {
		return nil, errors.New("APTEVA_GATEWAY_URL and APTEVA_APP_TOKEN are required to read tool schemas")
	}
	target := base + "/api/apps/callback/apps/" + url.PathEscape(appName) + "/proxy/mcp"
	if pid != "" {
		target += "?" + url.Values{"project_id": []string{pid}}.Encode()
	}
	body, _ := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": map[string]any{}})
	reqCtx, cancel := context.WithTimeout(context.Background(), actionSchemaTimeout)
	defer cancel()
	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, target, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("X-Apteva-App-Install-ID", os.Getenv("APTEVA_INSTALL_ID"))
	if pid != "" {
		req.Header.Set("X-Apteva-Project-ID", pid)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, actionSchemaMaxBytes))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode/100 != 2 {
		return nil, fmt.Errorf("tools/list %s: status %d", appName, resp.StatusCode)
	}
	return parseToolsList(raw)
}

func parseToolsList(raw []byte) (appToolSchemas, error) {
	var rpc struct {
		Result struct {
			Tools []struct {
				Name        string         `json:"name"`
				InputSchema map[string]any `json:"inputSchema"`
			} `json:"tools"`
		} `json:"result"`
		Error *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if err := json.Unmarshal(raw, &rpc); err != nil {
		return nil, fmt.Errorf("tools/list: %w", err)
	}
	if rpc.Error != nil {
		return nil, fmt.Errorf("tools/list: %s", rpc.Error.Message)
	}
	out := appToolSchemas{}
	for _, t := range rpc.Result.Tools {
		if t.Name != "" {
			out[t.Name] = t.InputSchema
		}
	}
	return out, nil
}

// appActionInput returns the input sent to an app action: the stored args,
// without idempotency_key unless the target tool declares it.
func appActionInput(pid string, action PlanAction, args map[string]any) map[string]any {
	if _, ok := args["idempotency_key"]; !ok {
		return args
	}
	if actionAcceptsIdempotencyKey(pid, action.AppName, action.ToolName) {
		return args
	}
	input := copyMap(args)
	delete(input, "idempotency_key")
	return input
}
