package main

import (
	"context"
	"encoding/json"
	"fmt"
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
}

// invokeFunction runs one event against a function's active version
// through the warm worker pool, records an invocation row, and
// returns the result. triggerKind distinguishes http / manual /
// event-routed invocations in the log.
func invokeFunction(ctx *sdk.AppCtx, parent context.Context, fn *Function, event any, triggerKind string) (*invokeResult, error) {
	if fn.Status != "active" {
		return nil, fmt.Errorf("function %q is %s, refusing to invoke", fn.Name, fn.Status)
	}
	if globalPool == nil {
		return nil, fmt.Errorf("function worker pool not initialised")
	}
	if fn.ActiveVersionID == nil {
		return nil, fmt.Errorf("function %q has no active version — deploy it first", fn.Name)
	}

	spec, err := resolveRuntime(fn.Runtime)
	if err != nil {
		return nil, err
	}

	ver, err := dbGetVersion(ctx.AppDB(), fn.ProjectID, *fn.ActiveVersionID)
	if err != nil {
		return nil, err
	}
	if ver == nil {
		return nil, fmt.Errorf("active version %d missing", *fn.ActiveVersionID)
	}
	if ver.BuildStatus != "ready" {
		return nil, fmt.Errorf("active version v%d build_status=%s", ver.Version, ver.BuildStatus)
	}

	// Resolve + ensure the artifact dir. Cheap when the version is
	// already built (a stat of the .ready marker); rebuilds lazily if
	// an ephemeral build base was cleared by a restart.
	src, err := resolveVersionSource(ctx, ver)
	if err != nil {
		return nil, fmt.Errorf("resolve source: %w", err)
	}
	base, err := poolBuildBase()
	if err != nil {
		return nil, err
	}
	dir, err := ensureBuilt(base, ver, spec, src)
	if err != nil {
		return nil, fmt.Errorf("build: %w", err)
	}

	timeout := time.Duration(fn.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = defaultTimeout * time.Millisecond
	}

	started := time.Now().UTC()
	res, err := globalPool.invoke(ctx, parent, fn, ver, spec, dir, event, timeout)
	if err != nil {
		return nil, err
	}
	finished := started.Add(time.Duration(res.DurationMS) * time.Millisecond)

	eventBytes, _ := json.Marshal(event)

	id, dbErr := dbInsertInvocation(ctx.AppDB(), fn.ProjectID, &Invocation{
		FunctionID:   fn.ID,
		StartedAt:    started.Format(time.RFC3339Nano),
		FinishedAt:   finished.Format(time.RFC3339Nano),
		DurationMS:   res.DurationMS,
		Status:       res.Status,
		ExitCode:     res.ExitCode,
		TriggerKind:  triggerKind,
		EventJSON:    truncate(string(eventBytes), eventJSONCap),
		ResponseBody: res.Response,
		Stderr:       res.Stderr,
		Error:        res.Error,
	})
	if dbErr != nil {
		ctx.Logger().Warn("record invocation", "function_id", fn.ID, "err", dbErr)
	}
	res.InvocationID = id
	return res, nil
}
