package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"reflect"
	"strconv"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func pointer(v any, path string) (any, bool) {
	if path == "" {
		return v, true
	}
	for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
		part = strings.ReplaceAll(strings.ReplaceAll(part, "~1", "/"), "~0", "~")
		switch obj := v.(type) {
		case map[string]any:
			var ok bool
			v, ok = obj[part]
			if !ok {
				return nil, false
			}
		case []any:
			i, err := strconv.Atoi(part)
			if err != nil || i < 0 || i >= len(obj) || strconv.Itoa(i) != part {
				return nil, false
			}
			v = obj[i]
		default:
			return nil, false
		}
	}
	return v, true
}
func evaluate(output any, assertions []Assertion) []AssertionResult {
	results := make([]AssertionResult, 0, len(assertions))
	for _, a := range assertions {
		actual, exists := pointer(output, a.Path)
		passed := false
		if a.Op == "exists" {
			passed = exists
		} else if exists {
			switch a.Op {
			case "equals":
				passed = reflect.DeepEqual(actual, a.Value)
			case "not_equals":
				passed = !reflect.DeepEqual(actual, a.Value)
			case "contains":
				str, ok := actual.(string)
				expected, _ := a.Value.(string)
				passed = ok && strings.Contains(str, expected)
			case "lt", "lte", "gt", "gte":
				x, ok := actual.(float64)
				y, valid := a.Value.(float64)
				if ok && valid {
					switch a.Op {
					case "lt":
						passed = x < y
					case "lte":
						passed = x <= y
					case "gt":
						passed = x > y
					case "gte":
						passed = x >= y
					}
				}
			}
		}
		// Compare the full value, but avoid duplicating a large response in
		// every assertion's evidence. The full output is stored once.
		evidence := actual
		if raw, err := json.Marshal(actual); err == nil && len(raw) > 8192 {
			evidence = map[string]any{"truncated": true, "preview": string(raw[:8192])}
		}
		results = append(results, AssertionResult{Assertion: a, Passed: passed, Actual: evidence, Exists: exists})
	}
	return results
}

// Resolve and dial the same address to avoid DNS rebinding. Private targets
// require an explicit operator opt-in; no sidecar credentials are forwarded.
func httpClient(allowPrivate bool) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.Proxy = nil
	transport.DialContext = func(ctx context.Context, network, address string) (net.Conn, error) {
		host, port, err := net.SplitHostPort(address)
		if err != nil {
			return nil, err
		}
		ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
		if err != nil {
			return nil, err
		}
		if len(ips) == 0 {
			return nil, errors.New("target has no IP addresses")
		}
		for _, ip := range ips {
			if !allowPrivate && (!ip.IP.IsGlobalUnicast() || ip.IP.IsPrivate() || ip.IP.IsLoopback() || ip.IP.IsLinkLocalUnicast() || (ip.IP.To4() != nil && ip.IP.To4()[0] == 100 && ip.IP.To4()[1] >= 64 && ip.IP.To4()[1] <= 127)) {
				return nil, errors.New("private network target blocked; operator may set APTEVA_TESTS_ALLOW_PRIVATE_NETWORK=true")
			}
		}
		dialer := net.Dialer{Timeout: 10 * time.Second}
		return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
	}
	return &http.Client{Transport: transport, Timeout: 30 * time.Second, CheckRedirect: func(req *http.Request, via []*http.Request) error { return http.ErrUseLastResponse }}
}

func (a *App) execute(ctx context.Context, app *sdk.AppCtx, c Check) (result Result) {
	result = Result{CheckID: c.ID, Name: c.Name, Kind: c.Kind, Status: "error", Assertions: []AssertionResult{}}
	start := time.Now()
	defer func() {
		result.DurationMS = time.Since(start).Milliseconds()
		if p := recover(); p != nil {
			result.Status = "error"
			result.Error = "runner panic"
		}
	}()
	d := c.Definition
	runCtx, cancel := context.WithTimeout(ctx, time.Duration(d.TimeoutMS)*time.Millisecond)
	defer cancel()
	var output any
	var err error
	switch c.Kind {
	case "function":
		if app.PlatformAPI() == nil {
			err = errors.New("Functions app is not connected")
			break
		}
		var response map[string]any
		response, err = a.invoke(runCtx, app, d)
		if raw, ok := response["response"].(string); ok {
			var decoded any
			if json.Unmarshal([]byte(raw), &decoded) == nil {
				response["response"] = decoded
			}
		}
		output = response
		if err == nil && response["status"] != "ok" {
			err = fmt.Errorf("function invocation did not succeed (status %v)", response["status"])
		}
	case "http":
		var req *http.Request
		req, err = http.NewRequestWithContext(runCtx, d.Method, d.URL, strings.NewReader(d.Body))
		if err != nil {
			break
		}
		for k, v := range d.Headers {
			req.Header.Set(k, v)
		}
		var res *http.Response
		res, err = a.client.Do(req)
		if err != nil {
			break
		}
		var body []byte
		body, err = io.ReadAll(io.LimitReader(res.Body, maxPayload+1))
		res.Body.Close()
		if err != nil {
			break
		}
		if len(body) > maxPayload {
			err = errors.New("HTTP response exceeds 256 KiB")
			break
		}
		var decoded any
		_ = json.Unmarshal(body, &decoded)
		output = map[string]any{"status_code": res.StatusCode, "body": string(body), "json": decoded}
	default:
		err = errors.New("unknown check kind")
	}
	if err == nil && runCtx.Err() != nil {
		err = runCtx.Err()
	}
	// Normalize numbers and reject oversized evidence before persisting it.
	if output != nil {
		raw, e := json.Marshal(output)
		if e != nil {
			err = e
		} else if len(raw) > maxPayload {
			err = errors.New("runner output exceeds 256 KiB")
		} else {
			_ = json.Unmarshal(raw, &result.Output)
		}
	}
	if err != nil {
		result.Error = err.Error()
		return
	}
	result.Status = "passed"
	result.Assertions = evaluate(result.Output, d.Assertions)
	for _, v := range result.Assertions {
		if !v.Passed {
			result.Status = "failed"
		}
	}
	return
}

func (a *App) work(ctx context.Context, app *sdk.AppCtx) error {
	if app.CurrentProject() == "" {
		return nil
	}
	s := store{app.AppDB(), app.CurrentProject()}
	r, err := s.claim()
	if errors.Is(err, sql.ErrNoRows) {
		return nil
	}
	if err != nil {
		return err
	}
	runCtx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	var message string
	for _, c := range r.Checks {
		if runCtx.Err() != nil {
			message = runCtx.Err().Error()
			break
		}
		result := a.execute(runCtx, app, c)
		if err = s.saveResult(r.ID, result); err != nil {
			message = "could not persist result: " + err.Error()
			break
		}
	}
	return s.finish(r.ID, message)
}

func (a *App) invoke(ctx context.Context, app *sdk.AppCtx, d Definition) (map[string]any, error) {
	// CallAppResult currently has no context argument. Bound outstanding calls
	// even after a local deadline; timed-out downstream functions may still run.
	select {
	case a.functionSlots <- struct{}{}:
	default:
		return nil, errors.New("function runner busy; earlier invocations are still finishing")
	}
	type reply struct {
		output map[string]any
		err    error
	}
	done := make(chan reply, 1)
	go func() {
		defer func() { <-a.functionSlots }()
		var out map[string]any
		err := app.PlatformAPI().CallAppResult("functions", "functions_invoke", map[string]any{"name": d.Function, "event": d.Event}, &out)
		done <- reply{out, err}
	}()
	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case r := <-done:
		return r.output, r.err
	}
}
