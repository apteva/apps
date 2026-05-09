package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// stepResult captures everything the runner records about one step
// invocation. Output is JSON-serialisable; the runner stuffs it
// into TemplateContext.Steps for downstream lookups.
type stepResult struct {
	Status string // ok | error | timeout | skipped
	Output any
	Error  string
}

// stepHTTPClient is the dispatch client for kind=http steps. Same
// pattern as jobs — package-level so tests can substitute via
// setStepHTTPClient.
var stepHTTPClient = &http.Client{Timeout: 30 * time.Second}

func setStepHTTPClient(c *http.Client) { stepHTTPClient = c }

// runStep dispatches one step by kind. The renderedInput value has
// already been templated; per-kind executors only worry about
// shape, not substitution.
func runStep(ctx context.Context, app *sdk.AppCtx, step *StepDef, renderedInput any) stepResult {
	switch step.Kind {
	case "http":
		return runHTTPStep(ctx, step, renderedInput)
	case "function":
		return runFunctionStep(app, step, renderedInput)
	case "app":
		return runAppStep(app, step, renderedInput)
	case "emit":
		return runEmitStep(app, step, renderedInput)
	case "branch":
		// branch outcomes are decided by the runner via condition.go;
		// runStep should never be called for them.
		return stepResult{Status: "error", Error: "branch step shouldn't reach runStep"}
	default:
		return stepResult{Status: "error", Error: fmt.Sprintf("unknown step kind %q", step.Kind)}
	}
}

// ─── http ──────────────────────────────────────────────────────────

func runHTTPStep(ctx context.Context, step *StepDef, input any) stepResult {
	url, err := resolveHTTPURL(step)
	if err != nil {
		return stepResult{Status: "error", Error: err.Error()}
	}
	method := strings.ToUpper(step.Method)
	if method == "" {
		if input == nil {
			method = "GET"
		} else {
			method = "POST"
		}
	}

	var body io.Reader
	if input != nil {
		bs, err := json.Marshal(input)
		if err != nil {
			return stepResult{Status: "error", Error: "encode input: " + err.Error()}
		}
		body = bytes.NewReader(bs)
	}

	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return stepResult{Status: "error", Error: err.Error()}
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	// Forward the install token so app-relative dispatches reach
	// the target sidecar with the platform's bearer. Same pattern
	// as jobs.runHTTPTarget.
	if t := os.Getenv("APTEVA_APP_TOKEN"); t != "" {
		req.Header.Set("Authorization", "Bearer "+t)
	}

	resp, err := stepHTTPClient.Do(req)
	if err != nil {
		if errors.Is(err, context.DeadlineExceeded) {
			return stepResult{Status: "timeout", Error: err.Error()}
		}
		return stepResult{Status: "error", Error: err.Error()}
	}
	defer resp.Body.Close()
	respBytes, _ := io.ReadAll(io.LimitReader(resp.Body, 256*1024))

	out := map[string]any{
		"status_code": resp.StatusCode,
	}
	// Decode JSON if Content-Type says so OR the body looks JSON-y.
	// Falls back to raw text — keeping the per-step output shape
	// always a JSON object regardless of upstream API.
	var parsed any
	if json.Unmarshal(respBytes, &parsed) == nil {
		out["body"] = parsed
	} else {
		out["body"] = string(respBytes)
	}

	if resp.StatusCode/100 != 2 {
		return stepResult{Status: "error", Output: out, Error: fmt.Sprintf("non-2xx: %d", resp.StatusCode)}
	}
	return stepResult{Status: "ok", Output: out}
}

func resolveHTTPURL(step *StepDef) (string, error) {
	if step.URL != "" {
		return step.URL, nil
	}
	if step.App == "" || step.Path == "" {
		return "", errors.New("http step needs url or {app, path}")
	}
	path := step.Path
	if !strings.HasPrefix(path, "/") {
		path = "/" + path
	}
	gateway := os.Getenv("APTEVA_GATEWAY_URL")
	if gateway == "" {
		return "", errors.New("APTEVA_GATEWAY_URL not set; cannot resolve app-relative target")
	}
	return strings.TrimRight(gateway, "/") + "/api/apps/" + step.App + path, nil
}

// ─── function ──────────────────────────────────────────────────────

// runFunctionStep calls functions_invoke on the functions sidecar
// via PlatformAPI.CallAppResult. Output shape matches what
// functions returns: {invocation_id, status, duration_ms,
// exit_code, response, stderr?, error?}.
func runFunctionStep(app *sdk.AppCtx, step *StepDef, input any) stepResult {
	if app == nil || app.PlatformAPI() == nil {
		return stepResult{Status: "error", Error: "function step requires PlatformAPI"}
	}
	args := map[string]any{
		"name":  step.FunctionName,
		"event": input,
	}
	var out map[string]any
	if err := app.PlatformAPI().CallAppResult("functions", "functions_invoke", args, &out); err != nil {
		return stepResult{Status: "error", Error: err.Error()}
	}
	status, _ := out["status"].(string)
	if status == "" {
		status = "ok"
	}
	if status != "ok" {
		errMsg, _ := out["error"].(string)
		return stepResult{Status: "error", Output: out, Error: errMsg}
	}
	return stepResult{Status: "ok", Output: out}
}

// ─── app ───────────────────────────────────────────────────────────

// runAppStep is the general-purpose "call any sibling app's MCP
// tool" step. Uses CallAppResult so the inner JSON-RPC envelope is
// stripped and the tool's natural shape becomes the step output.
func runAppStep(app *sdk.AppCtx, step *StepDef, input any) stepResult {
	if app == nil || app.PlatformAPI() == nil {
		return stepResult{Status: "error", Error: "app step requires PlatformAPI"}
	}
	args := map[string]any{}
	if m, ok := input.(map[string]any); ok {
		args = m
	} else if input != nil {
		args["input"] = input
	}
	var out any
	if err := app.PlatformAPI().CallAppResult(step.App, step.Tool, args, &out); err != nil {
		return stepResult{Status: "error", Error: err.Error()}
	}
	return stepResult{Status: "ok", Output: out}
}

// ─── emit ──────────────────────────────────────────────────────────

// runEmitStep publishes an event onto the workflows sidecar's app-
// event lane. Topics are bare strings — server stamps the app
// prefix (workflows) when fanning out.
func runEmitStep(app *sdk.AppCtx, step *StepDef, input any) stepResult {
	if app == nil {
		return stepResult{Status: "error", Error: "emit step needs AppCtx"}
	}
	// data resolution: input wins over step.Data when both are set,
	// matching how http/app/function consume input as the payload.
	data := input
	if data == nil {
		data = step.Data
	}
	app.Emit(step.Topic, data)
	return stepResult{Status: "ok", Output: map[string]any{"topic": step.Topic, "data": data}}
}
