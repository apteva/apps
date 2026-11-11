package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

func validateGatewayPath(path string) error {
	if path == "" {
		return nil
	}
	if !strings.HasPrefix(path, "/") || strings.HasPrefix(path, "//") ||
		strings.ContainsAny(path, "\\?#%\r\n\t") {
		return errors.New("invalid Gateway-relative path")
	}
	for _, segment := range pathSegments(path) {
		if segment == "." || segment == ".." {
			return errors.New("invalid Gateway path traversal")
		}
	}
	return nil
}

func expand(text string, values map[string]any, escape func(string) string) (string, error) {
	var missing string
	out := variables.ReplaceAllStringFunc(text, func(reference string) string {
		name := reference[2 : len(reference)-2]
		v, ok := values[name]
		if !ok {
			missing = name
			return ""
		}
		value, ok := v.(string)
		if !ok {
			value = encode(v)
		}
		if escape != nil {
			return escape(value)
		}
		return value
	})
	if missing != "" {
		return "", fmt.Errorf("required capture %q is missing", missing)
	}
	return out, nil
}

func expandValue(value any, values map[string]any) (any, error) {
	switch v := value.(type) {
	case string:
		if wholeVariable(v) {
			name := v[2 : len(v)-2]
			if got, ok := values[name]; ok {
				return got, nil
			}
		}
		return expand(v, values, nil)
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, item := range v {
			resolved, err := expandValue(item, values)
			if err != nil {
				return nil, err
			}
			out[k] = resolved
		}
		return out, nil
	case []any:
		out := make([]any, len(v))
		for i, item := range v {
			resolved, err := expandValue(item, values)
			if err != nil {
				return nil, err
			}
			out[i] = resolved
		}
		return out, nil
	default:
		return value, nil
	}
}

func scenarioURL(template string, values map[string]any, session *releaseSession) (*url.URL, string, error) {
	path, query, hasQuery := strings.Cut(template, "?")
	var escape func(string) string
	if session != nil && !wholeVariable(template) {
		escape = url.PathEscape
	}
	target, err := expand(path, values, escape)
	if err != nil {
		return nil, "", err
	}
	if hasQuery {
		// Capture values can populate a query parameter without adding new parameters.
		expanded, err := expand(query, values, url.QueryEscape)
		if err != nil {
			return nil, "", err
		}
		target += "?" + expanded
	}
	u, err := url.Parse(target)
	if err != nil || u.User != nil || u.Fragment != "" {
		return nil, "", errors.New("invalid scenario URL")
	}
	if session == nil {
		if (u.Scheme != "http" && u.Scheme != "https") || u.Hostname() == "" {
			return nil, "", errors.New("standalone scenario requires an absolute HTTP(S) URL")
		}
		return u, u.Path, nil
	}
	base := session.gateway
	if u.IsAbs() || u.Host != "" {
		if u.Scheme != base.Scheme || u.Host != base.Host {
			return nil, "", errors.New("captured URL is outside the bound Gateway")
		}
	}
	relative := u.Path
	inGateway := relative == base.Path || strings.HasPrefix(relative, base.Path+"/")
	if inGateway {
		relative = strings.TrimPrefix(relative, base.Path)
		if relative == "" {
			relative = "/"
		}
	} else if u.IsAbs() {
		return nil, "", errors.New("captured URL is outside the bound Gateway")
	}
	escaped := strings.ToLower(u.EscapedPath())
	if relative == "" || validateGatewayPath(relative) != nil ||
		strings.Contains(escaped, "%2f") || strings.Contains(escaped, "%5c") {
		return nil, "", errors.New("invalid Gateway-relative request path")
	}
	out := *base
	out.Path += relative
	out.RawQuery = u.RawQuery
	return &out, relative, nil
}

func (a *App) scenarioHTTP(ctx context.Context, step Step, values map[string]any, session *releaseSession, result *StepResult) (any, error) {
	u, relative, err := scenarioURL(step.Path, values, session)
	if err != nil {
		return nil, err
	}
	var body io.Reader
	if step.JSON != nil {
		value, err := expandValue(step.JSON, values)
		if err != nil {
			return nil, err
		}
		payload := encode(value)
		if len(payload) > maxPayload {
			return nil, errors.New("HTTP request exceeds 256 KiB")
		}
		body = strings.NewReader(payload)
	}
	req, err := http.NewRequestWithContext(ctx, step.Method, u.String(), body)
	if err != nil {
		return nil, errors.New("could not construct HTTP request")
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	for name, template := range step.Headers {
		value, err := expand(template, values, nil)
		if err != nil {
			return nil, err
		}
		req.Header.Set(name, value)
	}
	client := a.client
	if session != nil {
		client = a.runtimeHTTP
		if step.Identity != "" && step.Identity != "anonymous" {
			req.Header.Set(session.config.Identities[step.Identity].Header, session.identities[step.Identity])
		}
		result.OperationID, err = session.contract.match(step.Method, relative)
		if err != nil {
			return nil, err
		}
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, errors.New("HTTP request failed or timed out")
	}
	defer res.Body.Close()
	if session != nil && session.config.Identities[step.Identity].Header == "Authorization" &&
		res.Header.Get("X-Apteva-Runtime-Public-Auth") != "preserved" {
		return nil, errors.New("runtime Gateway does not support preserving caller Authorization")
	}
	payload, err := io.ReadAll(io.LimitReader(res.Body, maxPayload+1))
	if err != nil || len(payload) > maxPayload {
		return nil, errors.New("HTTP response could not be read within the size limit")
	}
	result.HTTPStatus = res.StatusCode
	requestID := res.Header.Get("X-Request-ID")
	if slugPattern.MatchString(requestID) {
		result.Correlation = requestID
	}
	headers := make(map[string]any, len(res.Header))
	for name, values := range res.Header {
		headers[strings.ToLower(name)] = strings.Join(values, ", ")
	}
	var parsed any
	_ = json.Unmarshal(payload, &parsed)
	return map[string]any{"status_code": float64(res.StatusCode), "headers": headers, "body": string(payload), "json": parsed}, nil
}

func (a *App) scenarioStepOutput(ctx context.Context, step Step, values map[string]any, app *appRunContext, result *StepResult) (any, error) {
	switch step.Kind {
	case "browser":
		return a.executeBrowserStep(ctx, step, values, app, result)
	case "http":
		return a.scenarioHTTP(ctx, step, values, app.release, result)
	case "function":
		input, err := expandValue(step.Event, values)
		if err != nil {
			return nil, err
		}
		var output map[string]any
		if app.release != nil {
			raw, err := app.release.invoke(ctx, "functions", "functions_invoke", map[string]any{"name": step.Function, "event": input})
			if err != nil {
				return nil, err
			}
			output, _ = raw.(map[string]any)
		} else {
			if app.ctx.PlatformAPI() == nil {
				return nil, errors.New("Functions app is not connected")
			}
			output, err = a.invoke(ctx, app.ctx, Definition{Function: step.Function, Event: input})
			if err != nil {
				return nil, errors.New("Function invocation failed")
			}
		}
		if output["status"] != "ok" {
			return nil, errors.New("Function invocation did not succeed")
		}
		if raw, ok := output["response"].(string); ok {
			var parsed any
			if json.Unmarshal([]byte(raw), &parsed) == nil {
				output["response"] = parsed
			}
		}
		return output, nil
	case "app_tool":
		if app.release == nil || !app.release.config.allowsRead(step.App, step.Tool) {
			return nil, errors.New("tool is outside the release read-tool allowlist")
		}
		value, err := expandValue(step.Input, values)
		if err != nil {
			return nil, err
		}
		input, _ := value.(map[string]any)
		return app.release.invoke(ctx, step.App, step.Tool, input)
	default:
		return nil, errors.New("unknown scenario step")
	}
}

func (a *App) runStep(ctx context.Context, step Step, values map[string]any, app *appRunContext) (result StepResult, captures map[string]any) {
	result = StepResult{ID: step.ID, Kind: step.Kind, Status: "error", Method: step.Method, Path: strings.SplitN(step.Path, "?", 2)[0]}
	start := time.Now()
	defer func() {
		result.DurationMS = time.Since(start).Milliseconds()
		if recover() != nil {
			result.Status, result.Error = "error", "scenario runner panic"
			captures = nil
		}
	}()
	ctx, cancel := context.WithTimeout(ctx, time.Duration(step.TimeoutMS)*time.Millisecond)
	defer cancel()
	assertions := append([]Assertion(nil), step.Assertions...)
	for i := range assertions {
		value, err := expandValue(assertions[i].Value, values)
		if err != nil {
			result.Error = err.Error()
			return
		}
		assertions[i].Value = value
	}
	for {
		result.Attempts++
		output, err := a.scenarioStepOutput(ctx, step, values, app, &result)
		if err != nil {
			result.Error = err.Error()
			return
		}
		if len(encode(output)) > maxPayload {
			result.Error = "step output exceeds 256 KiB"
			return
		}
		result.Status, result.Error = "passed", ""
		result.Assertions = evaluate(output, assertions)
		for _, assertion := range result.Assertions {
			if !assertion.Passed {
				result.Status = "failed"
			}
		}
		if result.Status == "passed" && len(step.Schema) > 0 {
			compiled, err := compileSchema(step.Schema)
			value, _ := pointer(output, "/json")
			if err != nil || compiled.Validate(value) != nil {
				result.Status, result.Error = "failed", "response JSON does not satisfy the schema"
			}
		}
		sanitizeStep(&result, output, step.Evidence, app.release)
		if result.Status == "passed" {
			result.coverageQualified = len(step.Schema) > 0
			for _, assertion := range assertions {
				if assertion.Path != "" && assertion.Path != "/status_code" {
					result.coverageQualified = true
				}
			}
			captures = make(map[string]any, len(step.Capture))
			for name, path := range step.Capture {
				v, exists := pointer(output, path)
				if !exists {
					result.Status, result.Error = "failed", fmt.Sprintf("required capture %q is missing", name)
					return result, nil
				}
				captures[name] = v
			}
			return
		}
		if step.PollEveryMS == 0 {
			return
		}
		timer := time.NewTimer(time.Duration(step.PollEveryMS) * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			result.Error = "poll deadline exceeded"
			return
		case <-timer.C:
		}
	}
}

func (a *App) runScenario(ctx context.Context, check Check, app *appRunContext, covered map[string]bool) Result {
	start := time.Now()
	timeout := time.Duration(check.Definition.TimeoutMS) * time.Millisecond
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	result := Result{CheckID: check.ID, Name: check.Name, Kind: check.Kind, Status: "passed", Assertions: []AssertionResult{}}
	app.browser = nil
	if hasBrowser(check) {
		if _, err := a.openBrowserCheck(ctx, check, app); err != nil {
			result.Status, result.Error = "error", safeBrowserError(err)
		}
	}
	values := map[string]any{}
	var steps []StepResult
	for _, step := range check.Definition.Steps {
		if result.Status != "passed" {
			break
		}
		if ctx.Err() != nil {
			result.Status, result.Error = "error", "scenario deadline exceeded"
			break
		}
		var row StepResult
		if len(step.Parallel) == 0 {
			var captures map[string]any
			row, captures = a.runStep(ctx, step, values, app)
			mergeCaptures(values, captures)

		} else {
			row = a.runParallel(ctx, step, values, app)
		}
		steps = append(steps, row)
		if row.Status != "passed" {
			result.Status = row.Status
			break
		}
	}
	var browserEvidence *BrowserEvidence
	if app.browser != nil {
		var status struct {
			Diagnostics BrowserDiagnostics `json:"diagnostics"`
		}
		statusCtx, statusCancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		statusErr := callComputer(statusCtx, app.ctx, "browser_test_session", map[string]any{"action": "status", "id": app.browser.id}, &status)
		statusCancel()
		if statusErr != nil || len(status.Diagnostics.Errors) > 0 || status.Diagnostics.Blocked > 0 || status.Diagnostics.Truncated {
			result.Status, result.Error = "error", "browser diagnostics reported an execution failure"
		}
		if result.Status != "passed" {
			stepID := "startup"
			if len(steps) > 0 {
				stepID = steps[len(steps)-1].ID
			}
			if id, err := a.captureBrowserFailure(ctx, app.browser, stepID); err == nil && len(steps) > 0 {
				steps[len(steps)-1].ArtifactID = id
			}
		}
		evidence, err := a.closeBrowserCheck(ctx, app.browser)
		browserEvidence = &evidence
		if err != nil {
			result.Status, result.Error = "error", safeBrowserError(err)
		}
		app.browser = nil
	}
	if result.Status == "passed" {
		markScenarioCoverage(covered, steps)
	}
	result.DurationMS = time.Since(start).Milliseconds()
	result.Output = map[string]any{"steps": steps}
	if browserEvidence != nil {
		result.Output.(map[string]any)["browser"] = redactValue(browserEvidence, app.release)
	}
	return result
}

func mergeCaptures(values, captures map[string]any) {
	for key, value := range captures {
		values[key] = value
	}
}

func markScenarioCoverage(covered map[string]bool, steps []StepResult) {
	// Credit a response only after the entire scenario passed. For 204s a
	// subsequent assertion can verify the side effect.
	followedByAssertion := false
	for i := len(steps) - 1; i >= 0; i-- {
		row := steps[i]
		group := row.Parallel
		if len(group) == 0 {
			group = []StepResult{row}
		}
		for _, step := range group {
			if step.OperationID != "" && step.HTTPStatus >= 200 && step.HTTPStatus < 300 &&
				(step.coverageQualified || step.HTTPStatus == http.StatusNoContent && followedByAssertion) {
				covered[step.OperationID] = true
			}
		}
		for _, step := range group {
			followedByAssertion = followedByAssertion || step.coverageQualified
		}
	}
}

func (a *App) runParallel(ctx context.Context, step Step, values map[string]any, app *appRunContext) StepResult {
	start := time.Now()
	row := StepResult{ID: step.ID, Kind: "parallel", Status: "passed", Parallel: make([]StepResult, len(step.Parallel))}
	type completion struct {
		index    int
		row      StepResult
		captures map[string]any
	}
	done := make(chan completion, len(step.Parallel))
	for i, child := range step.Parallel {
		go func() {
			row, captures := a.runStep(ctx, child, values, app)
			done <- completion{i, row, captures}
		}()
	}
	pending := map[string]any{}
	for range step.Parallel {
		result := <-done
		row.Parallel[result.index] = result.row
		mergeCaptures(pending, result.captures)
		if result.row.Status == "error" || result.row.Status == "failed" && row.Status == "passed" {
			row.Status = result.row.Status
		}
	}
	mergeCaptures(values, pending)
	row.DurationMS = time.Since(start).Milliseconds()
	return row
}
