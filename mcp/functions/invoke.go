package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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

func invokeFunctionWithStream(ctx *sdk.AppCtx, parent context.Context, fn *Function, event any, triggerKind string, stream invocationStream) (*invokeResult, error) {
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

	ver := globalPool.cachedVersion(*fn.ActiveVersionID)
	if ver == nil {
		ver, err = dbGetVersion(ctx.AppDB(), fn.ProjectID, *fn.ActiveVersionID)
		if err != nil {
			return nil, err
		}
		globalPool.cacheVersion(ver)
	}
	if ver == nil {
		return nil, fmt.Errorf("active version %d missing", *fn.ActiveVersionID)
	}
	if ver.BuildStatus != "ready" {
		return nil, fmt.Errorf("active version v%d build_status=%s", ver.Version, ver.BuildStatus)
	}

	base, err := poolBuildBase()
	if err != nil {
		return nil, err
	}
	dir := versionDir(base, ver)
	marker, markerErr := os.ReadFile(filepath.Join(dir, ".ready"))
	if markerErr != nil || string(marker) != ver.SourceHash {
		src, resolveErr := resolveVersionSource(ctx, ver)
		if resolveErr != nil {
			return nil, fmt.Errorf("resolve source: %w", resolveErr)
		}
		dir, err = ensureBuilt(base, ver, spec, src)
		if err != nil {
			return nil, fmt.Errorf("build: %w", err)
		}
	}

	timeout := time.Duration(fn.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = defaultTimeout * time.Millisecond
	}

	started := time.Now().UTC()
	invokeCtx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()
	res, err := globalPool.invoke(ctx, invokeCtx, fn, ver, spec, dir, event, timeout, stream)
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
