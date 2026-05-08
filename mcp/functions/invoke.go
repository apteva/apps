package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// Invocation output ceilings. Function authors that exceed them lose
// the overflow silently — documented in README.
const (
	stdoutCap     = 64 * 1024
	stderrCap     = 16 * 1024
	eventJSONCap  = 4 * 1024
	defaultConcur = 8
)

// invokeResult is the return shape from invokeFunction. Mirrors the
// columns we record on function_invocations + the bits the MCP /
// HTTP handlers want to surface to callers.
type invokeResult struct {
	InvocationID int64
	Status       string // ok | error | timeout
	ExitCode     int
	DurationMS   int64
	Response     string
	Stderr       string
	Error        string
}

// invokeFunction runs one function with the given event, captures
// output, records an invocation row, and returns the result. Caller
// supplies trigger_kind so the audit log distinguishes http /
// manual / event-routed invocations.
func invokeFunction(ctx *sdk.AppCtx, parent context.Context, fn *Function, event any, triggerKind string) (*invokeResult, error) {
	if fn.Status != "active" {
		return nil, fmt.Errorf("function %q is %s, refusing to invoke", fn.Name, fn.Status)
	}

	src, err := resolveSource(ctx, fn)
	if err != nil {
		return nil, fmt.Errorf("resolve source: %w", err)
	}

	spec, err := resolveRuntime(fn.Runtime)
	if err != nil {
		return nil, err
	}

	// Per-function concurrency cap. Stops a runaway agent from
	// fork-bombing the host by hammering one function in parallel.
	sem := semFor(fn.ID)
	select {
	case sem <- struct{}{}:
	case <-parent.Done():
		return nil, parent.Err()
	}
	defer func() { <-sem }()

	// Stage source in a per-invocation temp dir. Cwd here means
	// relative paths inside the function resolve under the temp dir,
	// not whatever the sidecar's cwd is — kept out of /data so a
	// crashed function can't pollute the persistent volume.
	dir, err := os.MkdirTemp("", "fn-"+fn.Name+"-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(dir)

	entryPath := filepath.Join(dir, spec.EntryFile)
	if err := os.WriteFile(entryPath, src, 0o600); err != nil {
		return nil, err
	}

	bin, args := spec.Argv(entryPath)
	resolvedBin, err := exec.LookPath(bin)
	if err != nil {
		// resolveRuntime already probed; this is belt-and-braces in
		// case PATH changed between probe and spawn.
		return nil, fmt.Errorf("runtime %q binary %q not in PATH", fn.Runtime, bin)
	}

	timeout := time.Duration(fn.TimeoutMS) * time.Millisecond
	if timeout <= 0 {
		timeout = defaultTimeout * time.Millisecond
	}
	cmdCtx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	cmd := exec.CommandContext(cmdCtx, resolvedBin, args...)
	cmd.Dir = dir
	cmd.Env = mergeEnv(os.Environ(), fn.Env, fn)

	// Encode event as JSON for stdin. Pass null for nil event so
	// `JSON.parse(stdin)` in the function works uniformly.
	eventBytes, err := json.Marshal(event)
	if err != nil {
		return nil, fmt.Errorf("encode event: %w", err)
	}
	cmd.Stdin = bytes.NewReader(eventBytes)

	stdoutBuf := newCapBuffer(stdoutCap)
	stderrBuf := newCapBuffer(stderrCap)
	cmd.Stdout = stdoutBuf
	cmd.Stderr = stderrBuf

	// New process group so the timeout-kill takes down any children
	// the function spawned. Without Setpgid, killing cmd leaves
	// detached subprocesses running until the OS reaps them.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	started := time.Now().UTC()
	runErr := cmd.Run()
	finished := time.Now().UTC()

	// On timeout, ensure we kill the *group* even though
	// CommandContext already sent SIGKILL to the leader. cmd.Process
	// can be nil if Start failed before fork; guard accordingly.
	if errors.Is(cmdCtx.Err(), context.DeadlineExceeded) && cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}

	status, exitCode, runErrStr := classifyRun(cmdCtx, runErr)
	result := &invokeResult{
		Status:     status,
		ExitCode:   exitCode,
		DurationMS: finished.Sub(started).Milliseconds(),
		Response:   stdoutBuf.String(),
		Stderr:     stderrBuf.String(),
		Error:      runErrStr,
	}

	// Record the invocation row regardless of outcome. Best-effort —
	// if the DB write fails, log and surface the result anyway.
	id, dbErr := dbInsertInvocation(ctx.AppDB(), fn.ProjectID, &Invocation{
		FunctionID:   fn.ID,
		StartedAt:    started.Format(time.RFC3339Nano),
		FinishedAt:   finished.Format(time.RFC3339Nano),
		DurationMS:   result.DurationMS,
		Status:       result.Status,
		ExitCode:     result.ExitCode,
		TriggerKind:  triggerKind,
		EventJSON:    truncate(string(eventBytes), eventJSONCap),
		ResponseBody: result.Response,
		Stderr:       result.Stderr,
		Error:        result.Error,
	})
	if dbErr != nil {
		ctx.Logger().Warn("record invocation", "function_id", fn.ID, "err", dbErr)
	}
	result.InvocationID = id
	return result, nil
}

// classifyRun maps cmd.Run's error into our (status, exit_code, err)
// tuple. The shape comes from the standard exec package — we just
// translate timeout semantics out of the context error.
func classifyRun(ctx context.Context, runErr error) (string, int, string) {
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		return "timeout", -1, "deadline exceeded"
	}
	if runErr == nil {
		return "ok", 0, ""
	}
	var ee *exec.ExitError
	if errors.As(runErr, &ee) {
		return "error", ee.ExitCode(), runErr.Error()
	}
	return "error", -1, runErr.Error()
}

// mergeEnv builds the spawn environment by overlaying the function's
// env onto the sidecar's environment. We also inject a few runtime
// hints the function can rely on so it doesn't need a config file.
func mergeEnv(parent []string, env map[string]string, fn *Function) []string {
	out := append([]string{}, parent...)
	for k, v := range env {
		out = append(out, k+"="+v)
	}
	out = append(out,
		"APTEVA_FUNCTION_NAME="+fn.Name,
		"APTEVA_FUNCTION_ID="+fmt.Sprintf("%d", fn.ID),
		"APTEVA_FUNCTION_RUNTIME="+fn.Runtime,
	)
	return out
}

// ─── Capped output buffer ──────────────────────────────────────────

// capBuffer drops bytes past the cap silently. Implemented as a
// thin io.Writer so we can hand it to exec.Cmd directly.
type capBuffer struct {
	cap     int
	written int
	buf     bytes.Buffer
}

func newCapBuffer(cap int) *capBuffer { return &capBuffer{cap: cap} }

func (c *capBuffer) Write(p []byte) (int, error) {
	if c.written >= c.cap {
		return len(p), nil // pretend we wrote it all
	}
	room := c.cap - c.written
	take := p
	if len(take) > room {
		take = take[:room]
	}
	c.buf.Write(take)
	c.written += len(take)
	return len(p), nil
}

func (c *capBuffer) String() string { return c.buf.String() }

// ─── Per-function concurrency ──────────────────────────────────────

var (
	semsMu sync.Mutex
	sems   = map[int64]chan struct{}{}
)

// semFor returns the semaphore for one function id, creating it on
// first call. defaultConcur is the cap; we don't currently expose
// it as a per-function knob — yagni until a real workload needs it.
func semFor(id int64) chan struct{} {
	semsMu.Lock()
	defer semsMu.Unlock()
	if s, ok := sems[id]; ok {
		return s
	}
	s := make(chan struct{}, defaultConcur)
	sems[id] = s
	return s
}

// Small utils (truncate, strKey, intArg, …) live in utils.go.
