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
	ErrorCode                                  string         `json:"error_code,omitempty"`
	Resources                                  *CallResources `json:"resources,omitempty"`
	BuildMS, QueueMS, ColdStartMS, ExecutionMS int64
	InvocationID                               int64
	Status                                     string // ok | error | timeout
	ExitCode                                   int
	DurationMS                                 int64
	Response                                   string
	Stderr                                     string
	Error                                      string
	Streamed                                   bool
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
	if ctx.CurrentProject() != fn.ProjectID {
		ctx = ctx.WithProject(fn.ProjectID)
	}
	parent, security, err := invocationAdmission(parent, fn, triggerKind)
	if err != nil {
		return nil, err
	}
	event, err = trustedEvent(event, security)
	if err != nil {
		return nil, err
	}
	p := currentPool()
	if p == nil {
		return nil, errors.New("function worker pool not initialised")
	}
	done, err := p.beginWork()
	if err != nil {
		return nil, err
	}
	defer done()
	parent = context.WithValue(parent, poolContextKey{}, p)
	if caller := sdk.CallerFrom(parent); caller != nil && (caller.AppName == "jobs" || caller.ThreadRole == "worker") {
		parent = context.WithValue(parent, admissionClassKey{}, "background")
	}
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
	if security.Principal != nil {
		// Audit identity separately; do not persist opaque claims or request bodies.
		eventLog = "[authenticated event omitted]"
	}
	if triggerKind == "function_url" && fn.FunctionURL != nil {
		eventLog = strings.ReplaceAll(eventLog, fn.FunctionURL.Token, "[redacted]")
	}
	eventLog = redactSecrets(eventLog, fn.Env)
	// Nested calls brokered by this invocation inherit its stage name.
	parent = context.WithValue(parent, stageContextKey{}, fn.stage)
	invokeCtx = context.WithValue(invokeCtx, stageContextKey{}, fn.stage)
	inv := &Invocation{FunctionID: fn.ID, VersionID: fn.ActiveVersionID, Stage: fn.stage, ConfigHash: configHash(fn), StartedAt: started.Format(time.RFC3339Nano), Status: "running", TriggerKind: triggerKind, EventJSON: truncate(eventLog, eventJSONCap), Truncated: len(eventLog) > eventJSONCap}
	identityJSON, err := json.Marshal(security.Identity)
	if err != nil {
		return nil, err
	}
	inv.Identity = identityJSON
	id, err := dbInsertInvocation(ctx.AppDB(), fn.ProjectID, inv, p.owner.id)
	if err != nil {
		return nil, fmt.Errorf("record invocation: %w", err)
	}
	ctx.EmitWithProject("invocation.started", fn.ProjectID, map[string]any{
		"id": id, "function_id": fn.ID, "function_name": fn.Name,
	})
	trace := p.newTrace(parent, fn, id)
	invokeCtx = context.WithValue(invokeCtx, traceKey{}, trace)
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
			res.ExitCode = -1
			switch {
			case parent.Err() == context.Canceled:
				res.Status = "canceled"
				res.Error = "Caller canceled the invocation"
			case parent.Err() == context.DeadlineExceeded:
				res.Status = "upstream_timeout"
				res.Error = "Upstream request deadline exceeded"
			case p.life.Err() != nil:
				res.Status = "canceled"
				res.Error = "Functions runtime stopped"
			default:
				res.Status = "timeout"
				res.Error = "Function execution deadline exceeded"
			}
		}
		if res.ErrorCode == "" {
			res.ErrorCode = deadlineErrorCode(retErr)
		}
		switch res.Status {
		case "timeout":
			res.ErrorCode = "invocation_timeout"
		case "canceled":
			res.ErrorCode = "caller_canceled"
		case "upstream_timeout":
			res.ErrorCode = "upstream_timeout"
		}
		if res.ErrorCode == "" {
			res.ErrorCode = extractErrorCode(res.Error)
		}
		p.finishTrace(trace, retErr, res)
		snapshot := trace.snapshot()
		res.Resources = &snapshot
		res.BuildMS = timings.build.Milliseconds()
		res.QueueMS = timings.queue.Milliseconds()
		res.ColdStartMS = timings.cold.Milliseconds()
		res.ExecutionMS = timings.execution.Milliseconds()

		res.InvocationID = id
		if trace.RequestID != "" {
			ctx.Logger().Info("function invocation completed", "request_id", trace.RequestID, "invocation_id", id, "status", res.Status, "error_code", res.ErrorCode, "queue_ms", res.QueueMS, "execution_ms", res.ExecutionMS)
		}
		res.DurationMS = time.Since(started).Milliseconds()
		res.Stderr = redactSecrets(res.Stderr, fn.Env)
		res.Error = redactSecrets(res.Error, fn.Env)
		// Stored copies only; the caller's result above is never altered.
		authenticated := security.Principal != nil
		ok := res.Status == "ok"
		logs, storedError := res.Stderr, res.Error
		if authenticated {
			logs, storedError = redactTokens(logs), redactTokens(storedError)
		}
		successDays := invocationStoragePolicy().SuccessDays
		storedResponse, omitted := responseStorage(fn.StoreResponses, authenticated, ok, successDays, redactSecrets(res.Response, fn.Env))
		err := finishInvocationRecord(ctx.AppDB(), invocationFinish{
			ID: id, ProjectID: fn.ProjectID, StartedAt: inv.StartedAt,
			FinishedAt: time.Now().UTC().Format(time.RFC3339Nano), DurationMS: res.DurationMS,
			Status: res.Status, ExitCode: res.ExitCode,
			Response: truncate(storedResponse, stdoutCap), ResponseBytes: int64(len(res.Response)), ResponseOmitted: omitted,
			Stderr: truncate(logs, stderrCap), Error: truncate(storedError, stderrCap),
			Truncated: inv.Truncated || len(storedResponse) > stdoutCap,
			BuildMS:   timings.build.Milliseconds(), QueueMS: timings.queue.Milliseconds(), ColdStartMS: timings.cold.Milliseconds(), ExecutionMS: timings.execution.Milliseconds(),
			// success_details_days=0: a successful call keeps its summary only.
			ExpireDetails: ok && successDays == 0,
		})
		if err != nil {
			ctx.Logger().Warn("finalize invocation", "id", id, "err", err)
		}
		if err == nil {
			ctx.EmitWithProject("invocation.completed", fn.ProjectID, map[string]any{
				"id": id, "function_id": fn.ID, "function_name": fn.Name,
				"status": res.Status, "error_code": res.ErrorCode,
			})
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
		if fn.poolKey != "" {
			return nil, errors.New("stage version unavailable")
		}
		return nil, errors.New("active version unavailable")
	}
	dir := versionDir(p.buildBase, v)
	releaseVersion, err := p.leaseVersion(dir)
	if err != nil {
		return nil, err
	}
	defer releaseVersion()
	// Preparation is owned by the runtime, not the customer request. Concurrent
	// callers share one bounded job; cancellation only stops this caller's wait.
	prepStart := time.Now()
	if fn.poolKey != "" {
		// Stage / pinned-version targets bypass the active-version
		// preparation queue: build the artifact on demand if it is missing.
		if err := p.ensureVersionArtifact(invokeCtx, ctx, fn, v, spec); err != nil {
			return nil, err
		}
	} else if err := p.awaitPreparation(invokeCtx, fn); err != nil {
		return nil, err
	}
	timings.build = time.Since(prepStart)

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
