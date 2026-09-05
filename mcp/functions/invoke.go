package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Output ceilings recorded on the invocation row. The serialised
// handler result and captured logs past these are truncated —
// documented in the README.
const (
	stdoutCap    = 64 * 1024
	stderrCap    = 16 * 1024
	eventJSONCap = 4 * 1024
)

// invokeResult is the return shape from invokeFunction. Mirrors the
// columns recorded on function_invocations plus the bits the MCP /
// HTTP handlers surface to callers.
type invokeResult struct {
	InvocationID int64
	Status       string // ok | error | timeout
	ExitCode     int
	DurationMS   int64
	Response     string
	Stderr       string
	Error        string
	Streamed     bool
}

// invocationStream receives response metadata and body chunks while a worker
// is still running. HTTP invocations provide a live sink; MCP, jobs, and
// manual invocations pass nil and retain the existing unary result contract.
type invocationStream interface {
	Start(statusCode int, headers map[string]string) error
	Write(chunk []byte) error
}

// invokeFunction runs one event against a function's active version
// through the warm worker pool, records an invocation row, and
// returns the result. triggerKind distinguishes http / manual /
// event-routed invocations in the log.
func invokeFunction(ctx *sdk.AppCtx, parent context.Context, fn *Function, event any, triggerKind string) (*invokeResult, error) {
	return invokeFunctionWithStream(ctx, parent, fn, event, triggerKind, nil)
}

func invokeFunctionWithStream(ctx *sdk.AppCtx, parent context.Context, fn *Function, event any, triggerKind string, stream invocationStream) (res *invokeResult, retErr error) {
	p := currentPool()
	if p == nil {
		return nil, errors.New("function worker pool not initialised")
	}
	parent = context.WithValue(parent, poolContextKey{}, p)
	started := time.Now().UTC()
	timeout := time.Duration(fn.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = defaultTimeout * time.Millisecond
	}
	timings := &invocationTimings{}
	parent = context.WithValue(parent, timingKey{}, timings)
	invokeCtx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	stop := context.AfterFunc(p.life, cancel)
	defer stop()
	eventBytes, err := json.Marshal(event)
	if err != nil {
		return nil, err
	}
	if len(eventBytes) > maxFrame {
		return nil, errors.New("event exceeds 8 MiB")
	}
	eventLog := string(eventBytes)
	if triggerKind == "function_url" && fn.FunctionURL != nil {
		eventLog = strings.ReplaceAll(eventLog, fn.FunctionURL.Token, "[redacted]")
	}
	eventLog = redactSecrets(eventLog, fn.Env)
	inv := &Invocation{FunctionID: fn.ID, VersionID: fn.ActiveVersionID, ConfigHash: configHash(fn), StartedAt: started.Format(time.RFC3339Nano), Status: "running", TriggerKind: triggerKind, EventJSON: truncate(eventLog, eventJSONCap), Truncated: len(eventLog) > eventJSONCap}
	id, err := dbInsertInvocation(ctx.AppDB(), fn.ProjectID, inv)
	if err != nil {
		return nil, fmt.Errorf("record invocation: %w", err)
	}
	if httpStream, ok := stream.(*httpInvocationStream); ok {
		httpStream.invocationID = id
		httpStream.ctx = invokeCtx
		httpStream.w.Header().Set("X-Apteva-Function-Invocation", fmt.Sprint(id))
	}
	defer func() {
		if res == nil {
			res = &invokeResult{Status: "error", ExitCode: -1}
			if retErr != nil {
				res.Error = retErr.Error()
			}
		}
		deadline, hasDeadline := invokeCtx.Deadline()
		if invokeCtx.Err() != nil || hasDeadline && !time.Now().Before(deadline) {
			res.Status = "timeout"
			res.ExitCode = -1
			if invokeCtx.Err() != nil {
				res.Error = invokeCtx.Err().Error()
			} else {
				res.Error = context.DeadlineExceeded.Error()
			}
		}
		res.InvocationID = id
		res.DurationMS = time.Since(started).Milliseconds()
		res.Stderr = redactSecrets(res.Stderr, fn.Env)
		res.Error = redactSecrets(res.Error, fn.Env)
		logs := res.Stderr
		_, err := ctx.AppDB().Exec(`UPDATE function_invocations SET finished_at=?,duration_ms=?,status=?,exit_code=?,response_body=?,stderr=?,error=?,truncated=?,build_ms=?,queue_ms=?,cold_start_ms=?,execution_ms=? WHERE id=? AND project_id=? AND started_at=?`, time.Now().UTC().Format(time.RFC3339Nano), res.DurationMS, res.Status, res.ExitCode, truncate(redactSecrets(res.Response, fn.Env), stdoutCap), truncate(logs, stderrCap), truncate(res.Error, stderrCap), inv.Truncated || len(res.Response) > stdoutCap, timings.build.Milliseconds(), timings.queue.Milliseconds(), timings.cold.Milliseconds(), timings.execution.Milliseconds(), id, fn.ProjectID, inv.StartedAt)
		if err != nil {
			ctx.Logger().Warn("finalize invocation", "id", id, "err", err)
		}
	}()
	if fn.Status != "active" {
		return nil, fmt.Errorf("function %q is %s", fn.Name, fn.Status)
	}
	if fn.ActiveVersionID == nil {
		return nil, errors.New("function has no active version")
	}
	spec, ok := runtimes[fn.Runtime]
	if !ok {
		return nil, fmt.Errorf("unknown runtime %q", fn.Runtime)
	}
	v := p.cachedVersion(*fn.ActiveVersionID)
	if v == nil {
		v, err = dbGetVersion(ctx.AppDB(), fn.ProjectID, *fn.ActiveVersionID)
		if err != nil {
			return nil, err
		}
		p.cacheVersion(v)
	}
	if v == nil || v.ArtifactKey != fn.InstanceKey || v.BuildStatus != "ready" {
		return nil, errors.New("active version unavailable")
	}
	dir := versionDir(p.buildBase, v)
	releaseVersion, err := p.leaseVersion(dir)
	if err != nil {
		return nil, err
	}
	defer releaseVersion()
	if _, built := p.artifacts.Load(dir); !built {
		buildStart := time.Now()

		src, err := resolveVersionSourceContext(invokeCtx, ctx, v)
		if err != nil {
			return nil, err
		}
		dir, err = ensureBuiltContext(invokeCtx, p.buildBase, v, spec, src)
		timings.build = time.Since(buildStart)
		if err != nil {
			return nil, err
		}
	}
	return p.invoke(ctx, invokeCtx, fn, v, spec, dir, json.RawMessage(eventBytes), timeout, stream)
}

type timingKey struct{}
type invocationTimings struct{ build, queue, cold, execution time.Duration }

func timingsFrom(ctx context.Context) *invocationTimings {
	if t, ok := ctx.Value(timingKey{}).(*invocationTimings); ok {
		return t
	}
	return &invocationTimings{}
}

func redactSecrets(text string, env map[string]string) string {
	for _, value := range env {
		if len(value) >= 4 {
			text = strings.ReplaceAll(text, value, "[redacted]")
		}
	}
	return text
}
