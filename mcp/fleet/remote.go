package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	sdk "github.com/apteva/app-sdk"
)

// toolRunRemote proxies an MCP tool call to the named app on the
// tenant's gateway. Authenticated as super-admin via the stored
// api_key.
//
// Args: {tenant_id, app, tool, input}. `app` is the tenant-side app
// name (e.g. "tasks"); `tool` is one of that app's MCP tools.
//
// Returns the parsed JSON the tenant tool emitted, with the MCP
// JSON-RPC envelope stripped — same shape CallAppResult uses inside
// the platform.
func (a *App) toolRunRemote(_ *sdk.AppCtx, args map[string]any) (any, error) {
	id := getStr(args, "tenant_id")
	appName := getStr(args, "app")
	tool := getStr(args, "tool")
	if id == "" || appName == "" || tool == "" {
		return nil, errors.New("tenant_id, app, tool are required")
	}
	input, _ := args["input"].(map[string]any)
	if input == nil {
		input = map[string]any{}
	}
	t, enc, err := a.store.get(id)
	if err != nil {
		return nil, err
	}
	key, err := a.keys.open(enc)
	if err != nil {
		return nil, fmt.Errorf("decrypt tenant api_key: %w", err)
	}
	payload, _ := json.Marshal(map[string]any{
		"jsonrpc": "2.0",
		"id":      1,
		"method":  "tools/call",
		"params": map[string]any{
			"name":      tool,
			"arguments": input,
		},
	})
	url := fmt.Sprintf("%s/api/apps/%s/mcp", t.BaseURL, appName)
	req, err := http.NewRequestWithContext(context.Background(), http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Authorization", "Bearer "+string(key))
	req.Header.Set("Content-Type", "application/json")
	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(resp.Body)
	if resp.StatusCode >= 300 {
		return nil, fmt.Errorf("tenant returned %d: %s", resp.StatusCode, string(raw))
	}
	_ = a.store.recordEvent(id, "remote_call", "user", map[string]any{"app": appName, "tool": tool})
	return unwrapMCP(raw)
}

// unwrapMCP strips the JSON-RPC result envelope. The convention is
// {"result":{"content":[{"text":"<inner JSON>"}]}} — same shape
// CallAppResult handles internally. If the body is already unwrapped
// we return it verbatim.
func unwrapMCP(raw []byte) (any, error) {
	var env struct {
		Result struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
		Error any `json:"error"`
	}
	if err := json.Unmarshal(raw, &env); err == nil && len(env.Result.Content) > 0 && env.Result.Content[0].Text != "" {
		var inner any
		if err := json.Unmarshal([]byte(env.Result.Content[0].Text), &inner); err == nil {
			return inner, nil
		}
		// Non-JSON text — return as-is so the caller can decide.
		return env.Result.Content[0].Text, nil
	}
	if env.Error != nil {
		return nil, fmt.Errorf("tenant MCP error: %v", env.Error)
	}
	// Already unwrapped or unknown shape — return parsed body.
	var any_ any
	if err := json.Unmarshal(raw, &any_); err != nil {
		return nil, err
	}
	return any_, nil
}
