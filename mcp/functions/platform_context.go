package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// The SDK's current CallAppResult has no context argument. This narrow adapter
// uses the same public callback API with cancellation and bounded bodies. Custom
// clients can implement the optional context methods without any HTTP adapter.
var callbackHTTP = &http.Client{Transport: &http.Transport{Proxy: http.ProxyFromEnvironment, MaxIdleConns: 128, MaxIdleConnsPerHost: 64, MaxConnsPerHost: 128, IdleConnTimeout: 90 * time.Second, ResponseHeaderTimeout: 30 * time.Second}}

func callbackRequest(ctx context.Context, method, path string, body any, out any) error {
	base := strings.TrimRight(os.Getenv("APTEVA_GATEWAY_URL"), "/")
	if base == "" {
		base = "http://127.0.0.1:5280"
	}
	b, err := json.Marshal(body)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, method, base+path, bytes.NewReader(b))
	if err != nil {
		return err
	}
	req.Header.Set("Authorization", "Bearer "+os.Getenv("APTEVA_APP_TOKEN"))
	req.Header.Set("Content-Type", "application/json")
	resp, err := callbackHTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, maxFrame+1))
	if err != nil {
		return err
	}
	if len(data) > maxFrame {
		return fmt.Errorf("downstream response exceeds 8 MiB; use storage references")
	}
	if resp.StatusCode/100 != 2 {
		return fmt.Errorf("platform callback status %d: %s", resp.StatusCode, truncate(string(data), 2048))
	}
	return json.Unmarshal(data, out)
}
func dispatchPlatformFrame(parent context.Context, ctx *sdk.AppCtx, msg wireResponse) callResult {
	ans := callResult{Type: "call_result", CallID: msg.CallID}
	if err := parent.Err(); err != nil {
		ans.Error = err.Error()
		return ans
	}
	input := map[string]any{}
	if len(msg.Input) > 0 {
		if err := json.Unmarshal(msg.Input, &input); err != nil {
			ans.Error = "input must be a JSON object"
			return ans
		}
	}
	if input == nil {
		input = map[string]any{}
	}
	input["_project_id"] = ctx.CurrentProject()
	var err error
	if msg.Type == "call" {
		if msg.App == "" || msg.Tool == "" || strings.ContainsAny(msg.App, "/\\?#") {
			ans.Error = "invalid app or tool"
			return ans
		}
		if api, ok := ctx.PlatformAPI().(interface {
			CallAppResultContext(context.Context, string, string, map[string]any, any) error
		}); ok {
			err = api.CallAppResultContext(parent, msg.App, msg.Tool, input, &ans.Result)
		} else if os.Getenv("APTEVA_APP_TOKEN") != "" && os.Getenv("APTEVA_GATEWAY_URL") != "" {
			var raw json.RawMessage
			err = callbackRequest(parent, "POST", "/api/apps/callback/apps/"+url.PathEscape(msg.App)+"/call", map[string]any{"tool": msg.Tool, "input": input}, &raw)
			if err == nil {
				ans.Result, err = unwrapCallback(raw)
			}
		} else {
			return servicePlatformCall(ctx, msg)
		}
	} else {
		var id int64
		var msgErr string
		var slug string
		if json.Unmarshal(msg.Conn, &slug) == nil && parseInt64(slug) == 0 {
			id, msgErr = resolveConnSlugContext(parent, ctx, strings.TrimSpace(slug))
		} else {
			id, msgErr = resolveConnRef(ctx, msg.Conn)
		}
		if msgErr != "" {
			ans.Error = msgErr
			return ans
		}
		var result *sdk.ExecuteResult
		if api, ok := ctx.PlatformAPI().(interface {
			ExecuteIntegrationToolContext(context.Context, int64, string, map[string]any) (*sdk.ExecuteResult, error)
		}); ok {
			result, err = api.ExecuteIntegrationToolContext(parent, id, msg.Tool, input)
		} else if os.Getenv("APTEVA_APP_TOKEN") != "" && os.Getenv("APTEVA_GATEWAY_URL") != "" {
			err = callbackRequest(parent, "POST", fmt.Sprintf("/api/apps/callback/integrations/%d/execute", id), map[string]any{"tool": msg.Tool, "input": input}, &result)
		} else {
			return servicePlatformIntegration(ctx, msg)
		}
		if err == nil && result != nil {
			if !result.Success {
				err = fmt.Errorf("integration failed (status=%d): %s", result.Status, truncate(string(result.Data), 2048))
			} else {
				ans.Result = result.Data
			}
		}
	}
	if err != nil {
		ans.Error = err.Error()
		return ans
	}
	if len(ans.Result) == 0 {
		ans.Result = json.RawMessage("null")
	}
	ans.OK = true
	return ans
}
func unwrapCallback(raw json.RawMessage) (json.RawMessage, error) {
	var env struct {
		JSONRPC string          `json:"jsonrpc"`
		Result  json.RawMessage `json:"result"`
		Error   *struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &env) == nil && env.JSONRPC != "" {
		if env.Error != nil {
			return nil, fmt.Errorf("platform tool: %s", env.Error.Message)
		}
		raw = env.Result
	}
	var content struct {
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		IsError bool `json:"isError"`
	}
	if json.Unmarshal(raw, &content) == nil && content.Content != nil {
		if content.IsError {
			return nil, fmt.Errorf("platform tool error: %s", truncate(string(raw), 2048))
		}
		for _, part := range content.Content {
			if part.Type == "text" {
				if !json.Valid([]byte(part.Text)) {
					return nil, fmt.Errorf("platform tool returned non-JSON text")
				}
				return json.RawMessage(part.Text), nil
			}
		}
		return json.RawMessage("null"), nil
	}
	if !json.Valid(raw) {
		return nil, fmt.Errorf("invalid platform response")
	}
	return raw, nil
}
