package main

import (
	"encoding/json"
	"fmt"

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
	return a.toolTenantAppCall(nil, args)
}

// unwrapMCP strips the JSON-RPC result envelope. The convention is
// {"result":{"content":[{"text":"<inner JSON>"}]}} — same shape
// CallAppResult handles internally. If the body is already unwrapped
// we return it verbatim.
func unwrapMCP(raw []byte) (any, error) {
	var env map[string]any
	if err := json.Unmarshal(raw, &env); err == nil {
		if errVal, ok := env["error"]; ok && errVal != nil {
			return nil, fmt.Errorf("tenant MCP error: %v", errVal)
		}
		if result, ok := env["result"]; ok {
			if m, ok := result.(map[string]any); ok {
				if failed, _ := m["isError"].(bool); failed {
					return nil, fmt.Errorf("tenant MCP tool returned an error")
				}
				if content, ok := m["content"].([]any); ok && len(content) > 0 {
					if first, ok := content[0].(map[string]any); ok {
						if text, ok := first["text"].(string); ok && text != "" {
							var inner any
							if err := json.Unmarshal([]byte(text), &inner); err == nil {
								return inner, nil
							}
							return text, nil
						}
					}
				}
			}
			return result, nil
		}
	}
	// Already unwrapped or unknown shape — return parsed body.
	var any_ any
	if err := json.Unmarshal(raw, &any_); err != nil {
		return nil, err
	}
	return any_, nil
}
