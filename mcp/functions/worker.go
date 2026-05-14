package main

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	sdk "github.com/apteva/app-sdk"
)

// coldStartTimeout bounds how long we wait for a freshly-spawned
// worker to import its handler module and send the ready frame.
const coldStartTimeout = 15 * time.Second

// maxFrame caps a single protocol frame — generous for an event
// payload or a handler result, small enough to reject a runaway.
const maxFrame = 8 << 20 // 8 MiB

// worker is one warm runtime process. It imports the function's
// handler once at boot, then serves invocations one at a time over a
// socketpair until the pool reaps it or it dies. One in-flight call
// at a time — the pool provides concurrency by running several
// workers per function.
type worker struct {
	fnID      int64
	fnName    string
	versionID int64
	cmd       *exec.Cmd
	conn      net.Conn
	stderr    *capBuffer

	mu       sync.Mutex // serialises call(); guards dead + lastUsed + seq
	dead     bool
	lastUsed time.Time
	seq      int64
}

// wireRequest is an invocation request sent to the worker.
type wireRequest struct {
	ID    int64 `json:"id"`
	Event any   `json:"event"`
}

// wireResponse is a frame received from the worker. Which fields are
// populated depends on Type:
//   - ready handshake:    Type=="ready", OK, Error
//   - cross-app call:     Type=="call", CallID, App, Tool, Input
//   - invocation result:  ID, OK, Result, Error, Logs
type wireResponse struct {
	Type   string          `json:"type"`
	ID     int64           `json:"id"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result"`
	Error  string          `json:"error"`
	Logs   []string        `json:"logs"`
	CallID int64           `json:"callId"`
	App    string          `json:"app"`
	Tool   string          `json:"tool"`
	Input  json.RawMessage `json:"input"`
}

// callResult answers a worker's cross-app call request.
type callResult struct {
	Type   string          `json:"type"` // always "call_result"
	CallID int64           `json:"callId"`
	OK     bool            `json:"ok"`
	Result json.RawMessage `json:"result,omitempty"`
	Error  string          `json:"error,omitempty"`
}

// startWorker spawns a runtime process for fn against version
// versionID, hands it one end of a socketpair as fd 3, and blocks
// until it reports ready (or the cold-start budget elapses).
func startWorker(spec runtimeSpec, harnessPath, stageDir, entryPath string, fn *Function, versionID int64) (*worker, error) {
	fds, err := syscall.Socketpair(syscall.AF_UNIX, syscall.SOCK_STREAM, 0)
	if err != nil {
		return nil, fmt.Errorf("socketpair: %w", err)
	}
	parentFile := os.NewFile(uintptr(fds[0]), "fn-sock-parent")
	childFile := os.NewFile(uintptr(fds[1]), "fn-sock-child")

	bin, args := spec.WorkerArgv(harnessPath)
	resolvedBin, err := exec.LookPath(bin)
	if err != nil {
		_ = parentFile.Close()
		_ = childFile.Close()
		return nil, fmt.Errorf("runtime binary %q not in PATH", bin)
	}

	stderr := newCapBuffer(stderrCap)
	cmd := exec.Command(resolvedBin, args...)
	cmd.Dir = stageDir
	cmd.Env = workerEnv(fn, entryPath)
	cmd.ExtraFiles = []*os.File{childFile} // becomes fd 3 in the child
	cmd.Stdout = stderr
	cmd.Stderr = stderr
	// New process group so a kill takes down anything the worker
	// spawned, not just the leader.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	if err := cmd.Start(); err != nil {
		_ = parentFile.Close()
		_ = childFile.Close()
		return nil, fmt.Errorf("start worker: %w", err)
	}
	_ = childFile.Close() // the child owns its copy now

	conn, err := net.FileConn(parentFile)
	_ = parentFile.Close() // net.FileConn dups the fd
	if err != nil {
		_ = killGroup(cmd)
		return nil, fmt.Errorf("socket conn: %w", err)
	}

	w := &worker{
		fnID: fn.ID, fnName: fn.Name, versionID: versionID,
		cmd: cmd, conn: conn, stderr: stderr, lastUsed: time.Now(),
	}

	// Wait for the ready frame — handler module imported successfully.
	_ = conn.SetReadDeadline(time.Now().Add(coldStartTimeout))
	raw, err := readFrame(conn)
	if err != nil {
		w.shutdown()
		return nil, fmt.Errorf("worker never reported ready (%v); logs: %s", err, stderr.String())
	}
	var ready wireResponse
	if err := json.Unmarshal(raw, &ready); err != nil {
		w.shutdown()
		return nil, fmt.Errorf("bad ready frame: %w", err)
	}
	if ready.Type != "ready" || !ready.OK {
		w.shutdown()
		msg := ready.Error
		if msg == "" {
			msg = "unknown cold-start error"
		}
		return nil, fmt.Errorf("worker boot failed: %s", msg)
	}
	_ = conn.SetReadDeadline(time.Time{})
	return w, nil
}

// call sends one event to the worker and waits for the response,
// bounded by timeout. Cross-app call frames the handler emits
// mid-flight are serviced inline (via ctx's PlatformAPI) and answered
// over the same socket. A read timeout leaves the worker in an
// unknown state — call kills it, and the pool discards it.
func (w *worker) call(ctx *sdk.AppCtx, parent context.Context, event any, timeout time.Duration) (*invokeResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.dead {
		return nil, errors.New("worker is dead")
	}

	w.seq++
	id := w.seq
	reqBytes, err := json.Marshal(wireRequest{ID: id, Event: event})
	if err != nil {
		return nil, fmt.Errorf("encode event: %w", err)
	}

	deadline := time.Now().Add(timeout)
	if d, ok := parent.Deadline(); ok && d.Before(deadline) {
		deadline = d
	}

	started := time.Now().UTC()
	_ = w.conn.SetWriteDeadline(deadline)
	if err := writeFrame(w.conn, reqBytes); err != nil {
		w.killLocked()
		return nil, fmt.Errorf("write request: %w", err)
	}

	for {
		_ = w.conn.SetReadDeadline(deadline)
		raw, err := readFrame(w.conn)
		if err != nil {
			finished := time.Now().UTC()
			w.killLocked()
			durMS := finished.Sub(started).Milliseconds()
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				return &invokeResult{
					Status: "timeout", ExitCode: -1, DurationMS: durMS,
					Error: "deadline exceeded", Stderr: w.stderr.String(),
				}, nil
			}
			return &invokeResult{
				Status: "error", ExitCode: -1, DurationMS: durMS,
				Error: fmt.Sprintf("worker read: %v", err), Stderr: w.stderr.String(),
			}, nil
		}

		var msg wireResponse
		if err := json.Unmarshal(raw, &msg); err != nil {
			w.killLocked()
			return &invokeResult{
				Status: "error", ExitCode: -1,
				DurationMS: time.Since(started).Milliseconds(),
				Error:      fmt.Sprintf("bad response frame: %v", err),
			}, nil
		}

		// Cross-app call from inside the handler — service it and
		// keep reading; the invocation result is still to come.
		if msg.Type == "call" {
			ans := servicePlatformCall(ctx, msg)
			ansBytes, _ := json.Marshal(ans)
			_ = w.conn.SetWriteDeadline(deadline)
			if err := writeFrame(w.conn, ansBytes); err != nil {
				w.killLocked()
				return &invokeResult{
					Status: "error", ExitCode: -1,
					DurationMS: time.Since(started).Milliseconds(),
					Error:      fmt.Sprintf("write call_result: %v", err),
				}, nil
			}
			continue
		}

		// Invocation result.
		finished := time.Now().UTC()
		w.lastUsed = time.Now()
		res := &invokeResult{
			DurationMS: finished.Sub(started).Milliseconds(),
			Stderr:     truncate(strings.Join(msg.Logs, "\n"), stderrCap),
		}
		if msg.OK {
			res.Status = "ok"
			res.ExitCode = 0
			res.Response = truncate(string(msg.Result), stdoutCap)
		} else {
			res.Status = "error"
			res.ExitCode = 1
			res.Error = msg.Error
		}
		return res, nil
	}
}

// servicePlatformCall executes a worker's cross-app call request
// against the sidecar's PlatformAPI — the worker never holds a
// platform token itself, so every cross-app call funnels through
// here.
func servicePlatformCall(ctx *sdk.AppCtx, msg wireResponse) callResult {
	ans := callResult{Type: "call_result", CallID: msg.CallID}
	if msg.App == "" || msg.Tool == "" {
		ans.Error = "context.call needs both an app and a tool name"
		return ans
	}
	if ctx == nil || ctx.PlatformAPI() == nil {
		ans.Error = "platform API not available in this context"
		return ans
	}
	input := map[string]any{}
	if len(msg.Input) > 0 {
		if err := json.Unmarshal(msg.Input, &input); err != nil {
			ans.Error = fmt.Sprintf("context.call input must be a JSON object: %v", err)
			return ans
		}
	}
	var out json.RawMessage
	if err := ctx.PlatformAPI().CallAppResult(msg.App, msg.Tool, input, &out); err != nil {
		ans.Error = err.Error()
		return ans
	}
	if len(out) == 0 {
		out = json.RawMessage("null")
	}
	ans.OK = true
	ans.Result = out
	return ans
}

// stale reports whether the worker was started against a version
// that is no longer the function's active one.
func (w *worker) stale(activeVersionID int64) bool {
	return w.versionID != activeVersionID
}

func (w *worker) alive() bool {
	w.mu.Lock()
	defer w.mu.Unlock()
	return !w.dead
}

func (w *worker) idleSince() time.Time {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.lastUsed
}

// killLocked terminates the worker; caller holds w.mu.
func (w *worker) killLocked() {
	if w.dead {
		return
	}
	w.dead = true
	_ = w.conn.Close()
	_ = killGroup(w.cmd)
}

func (w *worker) shutdown() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.killLocked()
}

// ── framing ───────────────────────────────────────────────────────

func writeFrame(w io.Writer, payload []byte) error {
	var hdr [4]byte
	binary.BigEndian.PutUint32(hdr[:], uint32(len(payload)))
	if _, err := w.Write(hdr[:]); err != nil {
		return err
	}
	_, err := w.Write(payload)
	return err
}

func readFrame(r io.Reader) ([]byte, error) {
	var hdr [4]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	n := binary.BigEndian.Uint32(hdr[:])
	if n > maxFrame {
		return nil, fmt.Errorf("frame too large: %d bytes", n)
	}
	buf := make([]byte, n)
	if _, err := io.ReadFull(r, buf); err != nil {
		return nil, err
	}
	return buf, nil
}

// killGroup SIGKILLs the worker's process group so children die with
// it, then reaps the process. Best-effort.
func killGroup(cmd *exec.Cmd) error {
	if cmd == nil || cmd.Process == nil {
		return nil
	}
	_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	err := cmd.Process.Kill()
	go func() { _ = cmd.Wait() }() // reap, don't leak a zombie
	return err
}

// envPassthrough is the allowlist of host env vars a worker inherits.
// Everything else — notably APTEVA_APP_TOKEN and APTEVA_GATEWAY_URL —
// is withheld: handler code is untrusted and reaches the platform
// only through context.call, which the sidecar mediates.
var envPassthrough = []string{"PATH", "HOME", "TMPDIR", "TMP", "TEMP", "LANG", "LC_ALL", "TZ"}

// workerEnv builds the worker's scrubbed environment: the allowlisted
// host vars, the function's own env map, the entry path, and the
// per-function hints.
func workerEnv(fn *Function, entryPath string) []string {
	out := make([]string, 0, len(envPassthrough)+len(fn.Env)+4)
	for _, k := range envPassthrough {
		if v, ok := os.LookupEnv(k); ok {
			out = append(out, k+"="+v)
		}
	}
	for k, v := range fn.Env {
		out = append(out, k+"="+v)
	}
	out = append(out,
		"APTEVA_FN_ENTRY="+entryPath,
		"APTEVA_FUNCTION_NAME="+fn.Name,
		"APTEVA_FUNCTION_ID="+fmt.Sprintf("%d", fn.ID),
		"APTEVA_FUNCTION_RUNTIME="+fn.Runtime,
	)
	return out
}

// ── capped output buffer ──────────────────────────────────────────

// capBuffer drops bytes past the cap silently. Safe for concurrent
// writers — exec wires a worker's stdout and stderr to one of these,
// and call() reads it while those copy goroutines are still writing.
type capBuffer struct {
	cap     int
	mu      sync.Mutex
	written int
	buf     bytes.Buffer
}

func newCapBuffer(cap int) *capBuffer { return &capBuffer{cap: cap} }

func (c *capBuffer) Write(p []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.written >= c.cap {
		return len(p), nil // pretend we took it all
	}
	take := p
	if room := c.cap - c.written; len(take) > room {
		take = take[:room]
	}
	c.buf.Write(take)
	c.written += len(take)
	return len(p), nil
}

func (c *capBuffer) String() string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.buf.String()
}
