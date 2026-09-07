package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"net"
	"net/http"
	"strings"
	"time"
)

type downstreamKindKey struct{}

func callbackClient() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.MaxIdleConns = 128
	tr.MaxIdleConnsPerHost = 64
	tr.MaxConnsPerHost = 128
	tr.IdleConnTimeout = 90 * time.Second
	tr.ResponseHeaderTimeout = 0
	tr.DialContext = (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	tr.TLSHandshakeTimeout = 10 * time.Second
	return &http.Client{Transport: tr}
}

var integrationHTTP = callbackClient()

func downstreamContext(parent context.Context, ctx *sdk.AppCtx, id int64, msg wireResponse) (context.Context, context.CancelFunc) {
	fn, _ := dbGetFunction(ctx.AppDB(), ctx.CurrentProject(), id, "")
	r := RuntimePolicy{AppMS: 30000, IntegrationMS: 300000}
	if fn != nil {
		r = policy(fn)
		if p := currentPool(); p != nil {
			settings := p.settings()
			if fn.Limits.AppMS == 0 {
				r.AppMS = settings.AppTimeoutMS
			}
			if fn.Limits.IntegrationMS == 0 {
				r.IntegrationMS = settings.IntegrationTimeoutMS
			}
		}
	}
	ms := r.AppMS
	if msg.Type == "integration" {
		ms = r.IntegrationMS
	}
	if msg.App == "functions" && msg.Tool == "functions_invoke" {
		ms = maxTimeoutMS
	}
	return context.WithTimeout(context.WithValue(parent, downstreamKindKey{}, msg.Type), time.Duration(ms)*time.Millisecond)
}
func classifyDownstreamError(parent, child context.Context, err error, kind string) string {
	if parent.Err() == context.Canceled {
		return "caller_canceled"
	}
	if parent.Err() == context.DeadlineExceeded {
		return "invocation_timeout"
	}
	if code := errorCode(err); code != "" {
		return code
	}
	var ne net.Error
	if child.Err() == context.DeadlineExceeded || errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout()) {
		if kind == "integration" {
			return "integration_timeout"
		}
		return "app_call_timeout"
	}
	return "downstream_error"
}
func extractErrorCode(message string) string {
	for _, code := range []string{"worker_oom", "worker_memory_limit", "protocol_memory_limit", "integration_timeout", "app_call_timeout", "invocation_timeout", "caller_canceled", "upstream_timeout", "nested_capacity_exhausted", "nested_cycle", "nested_depth_limit", "memory_budget_exhausted", "worker_limit", "function_worker_limit", "queue_limit", "function_queue_limit", "nested_downstream_limit"} {
		if strings.Contains(message, "["+code+"]") {
			return code
		}
	}
	return ""
}
func (t *callTrace) beginDownstream(msg wireResponse, ctx context.Context) int {
	if t == nil {
		return -1
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.Downstream) >= 128 {
		t.DownstreamDropped++
		return -1
	}
	target := msg.App + "." + msg.Tool
	if msg.Type == "integration" {
		target = string(msg.Conn) + "." + msg.Tool
	}
	ms := int64(0)
	if d, ok := ctx.Deadline(); ok {
		ms = time.Until(d).Milliseconds()
	}
	t.Downstream = append(t.Downstream, DownstreamRecord{ID: msg.CallID, Kind: msg.Type, Target: truncate(target, 256), State: "running", StartedAt: time.Now().UTC(), TimeoutMS: ms})
	return len(t.Downstream) - 1
}
func (t *callTrace) endDownstream(i int, ans callResult) {
	if t == nil || i < 0 {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	d := &t.Downstream[i]
	d.DurationMS = time.Since(d.StartedAt).Milliseconds()
	d.State = "ok"
	if !ans.OK {
		d.State = "error"
		d.ErrorCode = ans.ErrorCode
	}
}

// Local nested calls keep trusted ancestry/deadlines without transmitting user-
// supplied priority headers. Function access checks run before this dispatcher.
func dispatchNested(parent context.Context, ctx *sdk.AppCtx, input map[string]any) callResult {
	ans := callResult{Type: "call_result"}
	p := currentPool()
	if p == nil {
		ans.Error = "runtime unavailable"
		return ans
	}
	fn, err := executionFunction(ctx, ctx.CurrentProject(), int64Arg(input, "id"), strArg(input, "name"))
	if err != nil || fn == nil {
		ans.Error = "nested function not found"
		return ans
	}
	if t := traceFrom(parent); t != nil {
		for _, id := range t.chain {
			if id == fn.ID {
				ans.ErrorCode = "nested_cycle"
				ans.Error = "nested function cycle rejected"
				return ans
			}
		}
		if t.Depth+1 > p.settings().MaxNestedDepth {
			ans.ErrorCode = "nested_depth_limit"
			ans.Error = "nested call depth exceeded"
			return ans
		}
	}
	res, err := invokeFunction(ctx, parent, fn, input["event"], "nested")
	if err != nil {
		ans.Error = err.Error()
		ans.ErrorCode = errorCode(err)
		return ans
	}
	if res.Status != "ok" {
		ans.Error = res.Error
		ans.ErrorCode = res.ErrorCode
		return ans
	}
	// Same result contract as functions_invoke MCP.
	result := map[string]any{"invocation_id": res.InvocationID, "status": res.Status, "duration_ms": res.DurationMS, "response": res.Response, "resources": res.Resources}
	ans.Result, err = json.Marshal(result)
	if err != nil {
		ans.Error = fmt.Sprint(err)
		return ans
	}
	ans.OK = true
	return ans
}
