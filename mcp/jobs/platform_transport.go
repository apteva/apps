package main

// The SDK's current app/event methods have no context parameter. Runtime
// dispatch uses the same public callback protocol with a request context so
// shutdown, lease loss and deadlines cancel the network I/O itself.
import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

var platformHTTPClient = &http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return errors.New("platform redirects are not allowed") }}

func platformRequest(ctx context.Context, method, path string, input, out any) error {
	base := strings.TrimRight(os.Getenv("APTEVA_GATEWAY_URL"), "/")
	if base == "" {
		base = "http://127.0.0.1:5280"
	}
	var body io.Reader
	if input != nil {
		b, err := json.Marshal(input)
		if err != nil {
			return err
		}
		body = bytes.NewReader(b)
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, body)
	if err != nil {
		return err
	}
	token := os.Getenv("APTEVA_OUTBOUND_TOKEN")
	if token == "" {
		token = os.Getenv("APTEVA_APP_TOKEN")
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := platformHTTPClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("platform callback failed: HTTP %d", resp.StatusCode)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, (1<<20)+1))
	if err != nil {
		return err
	}
	if len(b) > 1<<20 {
		return errors.New("platform response exceeds 1 MiB")
	}
	if out != nil {
		return json.Unmarshal(b, out)
	}
	return nil
}
func runtimeDispatch(ctx context.Context) bool { return ctx.Value(runtimeDispatchKey{}) == true }
func callAppContext(ctx context.Context, app *sdk.AppCtx, name, tool string, input map[string]any, out *any) error {
	if !validIdentifier(name) || !validToolName(tool) {
		return errors.New("invalid app or tool identifier")
	}
	if !runtimeDispatch(ctx) {
		return app.PlatformAPI().CallAppResult(name, tool, input, out)
	}
	var raw any
	if err := platformRequest(ctx, "POST", "/api/apps/callback/apps/"+name+"/call", map[string]any{"tool": tool, "input": input}, &raw); err != nil {
		return err
	}
	if m, ok := raw.(map[string]any); ok {
		if m["jsonrpc"] != nil {
			if m["error"] != nil {
				return errors.New("app tool RPC error")
			}
			raw = m["result"]
		}
	}
	if m, ok := raw.(map[string]any); ok {
		if m["isError"] == true {
			return errors.New("app tool returned an error")
		}
		if structured, ok := m["structuredContent"]; ok {
			raw = structured
		} else if content, ok := m["content"].([]any); ok {
			if len(content) == 0 {
				raw = nil
			} else {
				block, ok := content[0].(map[string]any)
				if !ok {
					return errors.New("invalid app tool content")
				}
				text, ok := block["text"].(string)
				if !ok {
					return errors.New("unsupported app tool content")
				}
				if err := json.Unmarshal([]byte(text), &raw); err != nil {
					raw = text
				}
			}
		}
	}
	*out = raw
	return nil
}
func getInstanceContext(ctx context.Context, app *sdk.AppCtx, id int64) (*sdk.PlatformInstance, error) {
	if app == nil || app.PlatformAPI() == nil {
		return nil, errors.New("platform API required")
	}
	if !runtimeDispatch(ctx) {
		return app.PlatformAPI().GetInstance(id)
	}
	var out sdk.PlatformInstance
	err := platformRequest(ctx, "GET", "/api/apps/callback/agents/"+strconv.FormatInt(id, 10), nil, &out)
	return &out, err
}
func getGrantsContext(ctx context.Context, app *sdk.AppCtx, id int64) (*sdk.GrantsResponse, error) {
	if app == nil || app.PlatformAPI() == nil {
		return nil, errors.New("platform API required")
	}
	if !runtimeDispatch(ctx) {
		return app.PlatformAPI().GetGrants(id)
	}
	var out sdk.GrantsResponse
	err := platformRequest(ctx, "GET", "/api/apps/callback/grants?instance_id="+strconv.FormatInt(id, 10), nil, &out)
	return &out, err
}
func sendEventContext(ctx context.Context, app *sdk.AppCtx, id int64, message string) error {
	if !runtimeDispatch(ctx) {
		return app.PlatformAPI().SendEvent(id, message)
	}
	return platformRequest(ctx, "POST", "/api/apps/callback/agents/"+strconv.FormatInt(id, 10)+"/event", map[string]any{"message": message}, nil)
}
