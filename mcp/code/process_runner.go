package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const processStopGrace = 2 * time.Second
const maxProcessLogBytes int64 = 16 << 20

type cappedWriter struct {
	w         io.Writer
	remaining int64
	truncated bool
	mu        sync.Mutex
}

func newCappedWriter(w io.Writer, limit int64) *cappedWriter {
	return &cappedWriter{w: w, remaining: limit}
}

func (w *cappedWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	original := len(p)
	overflow := int64(len(p)) > w.remaining
	if w.remaining > 0 {
		write := int64(len(p))
		if write > w.remaining {
			write = w.remaining
		}
		if _, err := w.w.Write(p[:write]); err != nil {
			return 0, err
		}
		w.remaining -= write
	}
	if overflow && !w.truncated {
		w.truncated = true
		_, _ = io.WriteString(w.w, "\n=== log truncated at configured limit ===\n")
	}
	return original, nil
}

type commandCoordinator struct {
	mu        sync.Mutex
	repoSlots map[int64]chan struct{}
	global    chan struct{}
}

func (c *commandCoordinator) acquire(ctx context.Context, repoID int64) (func(), error) {
	c.mu.Lock()
	if c.repoSlots == nil {
		c.repoSlots = map[int64]chan struct{}{}
	}
	if c.global == nil {
		limit := 4
		if raw := os.Getenv("CODE_MAX_COMMANDS"); raw != "" {
			if parsed, err := strconv.Atoi(raw); err == nil && parsed > 0 && parsed <= 64 {
				limit = parsed
			}
		}
		c.global = make(chan struct{}, limit)
	}
	repoSlot := c.repoSlots[repoID]
	if repoSlot == nil {
		repoSlot = make(chan struct{}, 1)
		c.repoSlots[repoID] = repoSlot
	}
	global := c.global
	c.mu.Unlock()

	select {
	case global <- struct{}{}:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	select {
	case repoSlot <- struct{}{}:
		return func() {
			<-repoSlot
			<-global
		}, nil
	case <-ctx.Done():
		<-global
		return nil, ctx.Err()
	}
}

// runProcessGroup gives the whole child process group a graceful termination
// window and then force-kills it. Calling Wait only after cancellation has
// been handled avoids descendants holding inherited output descriptors open.
func runProcessGroup(ctx context.Context, cmd *exec.Cmd) error {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		return err
	case <-ctx.Done():
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		select {
		case <-done:
		case <-time.After(processStopGrace):
			_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
			<-done
		}
		return ctx.Err()
	}
}

func commandEnvironment(dataDir, envJSON string) ([]string, error) {
	runnerHome := filepath.Join(dataDir, "runner-home")
	runnerTmp := filepath.Join(dataDir, "runner-tmp")
	if err := os.MkdirAll(runnerHome, 0o700); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(runnerTmp, 0o700); err != nil {
		return nil, err
	}
	values := map[string]string{
		"HOME":     runnerHome,
		"TMPDIR":   runnerTmp,
		"CI":       "1",
		"NO_COLOR": "1",
	}
	// Preserve only runtime/toolchain configuration that does not grant app
	// authority. Credentials must be supplied explicitly through env_json.
	for _, key := range []string{
		"PATH", "LANG", "LC_ALL", "LC_CTYPE", "TZ", "TERM",
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY",
		"SSL_CERT_FILE", "SSL_CERT_DIR", "GIT_SSL_CAINFO",
	} {
		if value, ok := os.LookupEnv(key); ok {
			values[key] = value
		}
	}
	if strings.TrimSpace(envJSON) != "" {
		var supplied map[string]string
		if err := json.Unmarshal([]byte(envJSON), &supplied); err != nil {
			return nil, fmt.Errorf("env_json is not valid JSON: %w", err)
		}
		for key, value := range supplied {
			if strings.ContainsAny(key, "=\x00") || key == "" {
				return nil, fmt.Errorf("invalid environment variable name %q", key)
			}
			if strings.ContainsRune(value, '\x00') {
				return nil, fmt.Errorf("environment variable %q contains NUL", key)
			}
			values[key] = value
		}
	}
	keys := make([]string, 0, len(values))
	for key := range values {
		keys = append(keys, key)
	}
	sortStrings(keys)
	out := make([]string, 0, len(keys))
	for _, key := range keys {
		out = append(out, key+"="+values[key])
	}
	return out, nil
}

func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}

func isContextDeadline(err error) bool {
	return errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled)
}
