package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

var errConflict = errors.New("workload operation conflict")

type workloadGuard struct {
	token            chan struct{}
	refs             int
	cancel           context.CancelFunc
	destroyRequested bool
}

func (a *App) lockWorkload(ctx context.Context, id string, creating bool) (context.Context, func(), error) {
	a.guardMu.Lock()
	if a.guards == nil {
		a.guards = map[string]*workloadGuard{}
	}
	g := a.guards[id]
	if g == nil {
		g = &workloadGuard{token: make(chan struct{}, 1)}
		a.guards[id] = g
	}
	g.refs++
	a.guardMu.Unlock()
	select {
	case g.token <- struct{}{}:
	case <-ctx.Done():
		a.releaseGuard(id, g)
		return ctx, nil, ctx.Err()
	}
	child, cancel := context.WithCancel(ctx)
	a.guardMu.Lock()
	if creating && g.destroyRequested {
		a.guardMu.Unlock()
		cancel()
		<-g.token
		a.releaseGuard(id, g)
		return ctx, nil, errConflict
	}
	if creating {
		g.cancel = cancel
	}
	a.guardMu.Unlock()
	return child, func() {
		cancel()
		a.guardMu.Lock()
		g.cancel = nil
		a.guardMu.Unlock()
		<-g.token
		a.releaseGuard(id, g)
	}, nil
}
func (a *App) cancelCreation(id string) {
	a.guardMu.Lock()
	defer a.guardMu.Unlock()
	if a.guards == nil {
		a.guards = map[string]*workloadGuard{}
	}
	g := a.guards[id]
	if g == nil {
		g = &workloadGuard{token: make(chan struct{}, 1)}
		a.guards[id] = g
	}
	g.destroyRequested = true
	if g.cancel != nil {
		g.cancel()
	}
}

// tailBuffer bounds allocation while retaining the newest bytes and the full count.
type tailBuffer struct {
	mu           sync.Mutex
	data         []byte
	limit, total int
}

func (b *tailBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	b.total += n
	if b.limit <= 0 {
		return n, nil
	}
	if n >= b.limit {
		b.data = append(b.data[:0], p[n-b.limit:]...)
	} else {
		drop := len(b.data) + n - b.limit
		if drop > 0 {
			copy(b.data, b.data[drop:])
			b.data = b.data[:len(b.data)-drop]
		}
		b.data = append(b.data, p...)
	}
	return n, nil
}
func (b *tailBuffer) String() string { b.mu.Lock(); defer b.mu.Unlock(); return string(b.data) }
func executionOutputLimit(n int) int {
	if n <= 0 {
		return 1048576
	}
	if n > maxDockerOutputBytes {
		return maxDockerOutputBytes
	}
	return n
}
func dockerCombined(ctx context.Context, args ...string) (string, error) {
	b := &tailBuffer{limit: maxDockerOutputBytes}
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Stdout = b
	cmd.Stderr = b
	if err := cmd.Run(); err != nil {
		return b.String(), formatDockerError(args, err.Error()+": "+b.String())
	}
	return b.String(), nil
}

// Helpers have identities and explicit cleanup; killing the docker CLI alone does
// not stop a container already created by the daemon.
func helperContainer(ctx context.Context, input []byte, limit int, args ...string) (output string, resultErr error) {
	name := "containers-helper-" + newWorkloadID()[4:]
	base := []string{"run", "--rm", "--name", name, "--memory", "256m", "--cpus", "1", "--pids-limit", "64"}
	hasNetwork := false
	for _, arg := range args {
		if arg == "--network" {
			hasNetwork = true
		}
	}
	if !hasNetwork {
		base = append(base, "--network", "none")
	}
	base = append(base, args...)
	defer func() {
		c, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := removeHelper(c, name); err != nil {
			resultErr = errors.Join(resultErr, err)
		}
	}()
	child, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(child, "docker", base...)
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	out := &hardLimitBuffer{limit: limit, cancel: cancel}
	stderr := newLimitedBuffer(maxDockerErrorBytes)
	cmd.Stdout = out
	cmd.Stderr = stderr
	err := cmd.Run()
	if out.exceeded {
		return "", errors.New("helper output exceeds byte limit")
	}
	if err != nil {
		return "", fmt.Errorf("helper failed: %w: %s", err, stderr.String())
	}
	return out.buf.String(), nil
}

func containerTreeKill(ctx context.Context, container, dir string) error {
	if dir == "" {
		return nil
	}
	// /proc is available on Linux workloads; snapshot descendants before killing
	// their parents. No signals are sent outside this session's process tree.
	script := `root=$(cat "$1/pid" 2>/dev/null) || exit 0
case "$root" in ''|*[!0-9]*|0|1) exit 1;; esac
pids="$root"
while :; do
 old="$pids"
 for f in /proc/[0-9]*/status; do
  pid=; parent=
  while read -r k v rest; do case "$k" in Pid:) pid="$v";; PPid:) parent="$v";; esac; done < "$f" 2>/dev/null || continue
  case " $pids " in *" $parent "*) case " $pids " in *" $pid "*) ;; *) pids="$pids $pid";; esac;; esac
 done
 [ "$pids" = "$old" ] && break
done
for pid in $pids; do kill -KILL "$pid" 2>/dev/null || true; done`
	_, err := docker(ctx, "exec", container, "/bin/sh", "-c", script, "sh", dir)
	return err
}
func typedInputError(err error) error { return fmt.Errorf("invalid input: %w", err) }
func isInputError(err error) bool {
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "invalid") || strings.Contains(msg, "must ") || strings.Contains(msg, "required") || strings.Contains(msg, "exceeds")
}

type hardLimitBuffer struct {
	buf      bytes.Buffer
	limit    int
	cancel   context.CancelFunc
	exceeded bool
}

func (b *hardLimitBuffer) Write(p []byte) (int, error) {
	if b.buf.Len()+len(p) > b.limit {
		b.exceeded = true
		b.cancel()
		return 0, errors.New("output limit exceeded")
	}
	return b.buf.Write(p)
}

func (a *App) releaseGuard(id string, g *workloadGuard) {
	a.guardMu.Lock()
	defer a.guardMu.Unlock()
	g.refs--
	if g.refs == 0 && a.guards[id] == g {
		delete(a.guards, id)
	}
}

func removeHelper(ctx context.Context, name string) error {
	for {
		out, err := exec.CommandContext(ctx, "docker", "rm", "-f", name).CombinedOutput()
		if err == nil || strings.Contains(strings.ToLower(string(out)), "no such container") {
			return nil
		}
		if !strings.Contains(strings.ToLower(string(out)), "removal") {
			return fmt.Errorf("helper cleanup %s: %w: %s", name, err, out)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}
