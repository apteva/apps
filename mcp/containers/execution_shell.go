package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/creack/pty"
)

const shellRuntimePrefix = "pty_"

var persistentShells = newPersistentShellManager()

type persistentShellManager struct {
	mu         sync.Mutex
	sessions   map[string]*persistentShellSession
	executions map[string]*persistentShellExecution
	starting   map[string]chan struct{}
}

type persistentShellSession struct {
	manager       *persistentShellManager
	key           string
	containerName string
	sessionKey    string
	user          string
	cmd           *exec.Cmd
	terminal      *os.File
	readyMarker   string
	ready         chan struct{}
	readyOnce     sync.Once
	closed        chan struct{}
	closedOnce    sync.Once
	writeMu       sync.Mutex
	mu            sync.Mutex
	current       *persistentShellExecution
	pending       []byte
	lastUsed      time.Time
	controlDir    string
	closing       bool
}

type persistentShellExecution struct {
	mu          sync.Mutex
	id          string
	runtimeID   string
	endPrefix   []byte
	session     *persistentShellSession
	output      []byte
	totalBytes  int
	outputLimit int
	running     bool
	exitCode    int
	done        chan struct{}
	doneOnce    sync.Once
}

func newPersistentShellManager() *persistentShellManager {
	return &persistentShellManager{
		sessions:   make(map[string]*persistentShellSession),
		starting:   make(map[string]chan struct{}),
		executions: make(map[string]*persistentShellExecution),
	}
}

func shellRuntimeControlDir(runtimeID string) string {
	if !persistentShellRuntime(runtimeID) {
		return ""
	}
	generation, _, ok := strings.Cut(strings.TrimPrefix(runtimeID, shellRuntimePrefix), ".")
	if !ok || len(generation) != 16 || strings.Trim(generation, "0123456789abcdef") != "" {
		return ""
	}
	return "/tmp/.apteva-sessions/exe_" + generation
}

func persistentShellRuntime(runtimeID string) bool {
	return strings.HasPrefix(runtimeID, shellRuntimePrefix)
}

func (m *persistentShellManager) Start(ctx context.Context, spec executionRuntimeSpec) (string, error) {
	if spec.ContainerName == "" || spec.ExecutionID == "" || spec.SessionKey == "" || len(spec.Argv) == 0 {
		return "", errors.New("persistent shell execution requires a container, execution id, session key, and command")
	}
	key := spec.ContainerName + "\x00" + spec.SessionKey
	var session *persistentShellSession
	for {
		m.mu.Lock()
		session = m.sessions[key]
		if session != nil && session.isClosed() {
			delete(m.sessions, key)
			session = nil
		}
		if session != nil {
			m.mu.Unlock()
			break
		}
		if ready := m.starting[key]; ready != nil {
			m.mu.Unlock()
			select {
			case <-ready:
				continue
			case <-ctx.Done():
				return "", ctx.Err()
			}
		}
		if len(m.sessions)+len(m.starting) >= 64 {
			m.mu.Unlock()
			return "", errors.New("persistent session limit reached; close an idle session")
		}
		ready := make(chan struct{})
		m.starting[key] = ready
		m.mu.Unlock()
		var err error
		session, err = startPersistentShell(ctx, m, key, spec)
		m.mu.Lock()
		delete(m.starting, key)
		if err == nil {
			m.sessions[key] = session
		}
		close(ready)
		m.mu.Unlock()
		if err != nil {
			return "", err
		}
		break
	}

	runtimeID := shellRuntimePrefix + strings.TrimPrefix(strings.TrimPrefix(session.controlDir, "/tmp/.apteva-sessions/"), "exe_") + "." + strings.TrimPrefix(newExecutionID(), "exe_")
	execution := &persistentShellExecution{
		id: spec.ExecutionID, runtimeID: runtimeID,
		endPrefix: []byte("\x1eAPTEVA_END_" + runtimeID + ":"),
		session:   session, running: true, exitCode: -1, done: make(chan struct{}), outputLimit: executionOutputLimit(spec.MaxOutputBytes),
	}
	session.mu.Lock()
	if session.isClosed() || session.closing {
		session.mu.Unlock()
		return "", errors.New("session is closed; retry execution")
	}
	if session.user != spec.User {
		session.mu.Unlock()
		return "", errors.New("session user does not match workload user")
	}
	if session.current != nil && session.current.isRunning() {
		session.mu.Unlock()
		return "", fmt.Errorf("persistent shell session %q is already executing a command", spec.SessionKey)
	}
	session.current = execution
	session.lastUsed = time.Now()
	session.mu.Unlock()
	m.mu.Lock()
	m.executions[spec.ExecutionID] = execution
	m.mu.Unlock()

	// Stage source over a pipe, never through canonical terminal input.
	commandPath := session.controlDir + "/" + spec.ExecutionID + ".sh"
	args := []string{"exec", "-i"}
	if spec.User != "" {
		args = append(args, "--user", spec.User)
	}
	args = append(args, spec.ContainerName, "/bin/sh", "-c", `umask 077; mkdir -p "$1"; cat > "$2"`, "sh", session.controlDir, commandPath)
	if _, err := dockerWithInput(ctx, []byte(persistentShellCommand(spec, runtimeID)), args...); err != nil {
		execution.complete(125)
		session.mu.Lock()
		if session.current == execution {
			session.current = nil
		}
		session.mu.Unlock()
		m.Remove(&Execution{ID: spec.ExecutionID})
		return "", err
	}
	if spec.RuntimeReady != nil {
		if err := spec.RuntimeReady(runtimeID); err != nil {
			execution.complete(125)
			session.close()
			return "", err
		}
	}
	control := ". " + shellSingleQuote(commandPath) + " </dev/null; _apteva_status=$?; /bin/rm -f " + shellSingleQuote(commandPath) + "; printf '\\036APTEVA_END_" + runtimeID + ":%s\\037' \"$_apteva_status\" >&3\n"
	session.writeMu.Lock()
	_, err := session.terminal.Write([]byte(control))
	session.writeMu.Unlock()
	if err != nil {
		execution.complete(125)
		session.close()
		return "", fmt.Errorf("write persistent shell command: %w", err)
	}
	return runtimeID, nil
}

func startPersistentShell(ctx context.Context, manager *persistentShellManager, key string, spec executionRuntimeSpec) (*persistentShellSession, error) {
	readyMarker := "\x1eAPTEVA_READY_" + strings.TrimPrefix(newExecutionID(), "exe_") + "\x1f"
	args := []string{"exec", "-it", "-e", "PS1=", "-e", "PS2=", "-e", "TERM=xterm-256color"}
	if spec.User != "" {
		args = append(args, "--user", spec.User)
	}
	args = append(args, spec.ContainerName, "/bin/sh")
	cmd := exec.Command("docker", args...)
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 40, Cols: 160})
	if err != nil {
		return nil, fmt.Errorf("start persistent Docker shell: %w", err)
	}
	session := &persistentShellSession{
		manager: manager, key: key, containerName: spec.ContainerName,
		sessionKey: spec.SessionKey, user: spec.User, cmd: cmd, terminal: terminal,
		readyMarker: readyMarker, ready: make(chan struct{}), closed: make(chan struct{}), lastUsed: time.Now(), controlDir: "/tmp/.apteva-sessions/" + newExecutionID(),
	}
	go session.readLoop()
	initCommand := "exec 3>&1; stty -echo 2>/dev/null || true; umask 077; mkdir -p " + shellSingleQuote(session.controlDir) + "; echo $$ > " + shellSingleQuote(session.controlDir+"/pid") + "; printf '\\036" + strings.Trim(readyMarker, "\x1e\x1f") + "\\037' >&3\n"
	if _, err := terminal.Write([]byte(initCommand)); err != nil {
		session.close()
		return nil, fmt.Errorf("initialize persistent Docker shell: %w", err)
	}
	select {
	case <-session.ready:
		return session, nil
	case <-session.closed:
		return nil, errors.New("persistent Docker shell exited during initialization")
	case <-ctx.Done():
		killCtx, killCancel := context.WithTimeout(context.Background(), 10*time.Second)
		killErr := containerTreeKill(killCtx, session.containerName, session.controlDir)
		killCancel()
		if killErr != nil {
			session.close()
			return nil, killErr
		}
		session.close()
		return nil, ctx.Err()
	}
}

func persistentShellCommand(spec executionRuntimeSpec, runtimeID string) string {
	var command strings.Builder
	if spec.WorkingDirectory != "" {
		command.WriteString("cd " + shellSingleQuote(spec.WorkingDirectory) + " || return $?\n")
	}
	for _, key := range envKeys(spec.Env) {
		command.WriteString("export " + key + "=" + shellSingleQuote(spec.Env[key]) + "\n")
	}
	if spec.StatefulCommand && len(spec.Argv) == 3 {
		command.WriteString(spec.Argv[2])
	} else {
		command.WriteString(shellArgv(spec.Argv))
	}
	command.WriteByte('\n')
	return command.String()
}

func shellArgv(argv []string) string {
	parts := make([]string, len(argv))
	for i, arg := range argv {
		parts[i] = shellSingleQuote(arg)
	}
	return strings.Join(parts, " ")
}

func shellSingleQuote(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func (s *persistentShellSession) readLoop() {
	buffer := make([]byte, 32*1024)
	for {
		n, err := s.terminal.Read(buffer)
		if n > 0 {
			s.consume(buffer[:n])
		}
		if err != nil {
			break
		}
	}
	err := s.cmd.Wait()
	code := 0
	if err != nil {
		code = 125
		if s.cmd.ProcessState != nil {
			code = s.cmd.ProcessState.ExitCode()
			if code < 0 {
				code = 125
			}
		}
	}
	_ = s.terminal.Close()
	s.closedOnce.Do(func() { close(s.closed) })
	s.mu.Lock()
	current := s.current
	s.current = nil
	s.mu.Unlock()
	if current != nil {
		current.appendOutput(s.pending)
		current.complete(code)
	}
	s.manager.mu.Lock()
	if s.manager.sessions[s.key] == s {
		delete(s.manager.sessions, s.key)
	}
	s.manager.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	_, _ = docker(ctx, "exec", s.containerName, "rm", "-rf", s.controlDir)
}

func (s *persistentShellSession) consume(chunk []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.pending = append(s.pending, chunk...)
	if !channelClosed(s.ready) {
		marker := []byte(s.readyMarker)
		if index := bytes.Index(s.pending, marker); index >= 0 {
			s.pending = s.pending[index+len(marker):]
			s.readyOnce.Do(func() { close(s.ready) })
		} else if len(s.pending) > len(marker) {
			s.pending = append([]byte(nil), s.pending[len(s.pending)-len(marker):]...)
			return
		} else {
			return
		}
	}
	current := s.current
	if current == nil {
		s.pending = nil
		return
	}
	index := bytes.Index(s.pending, current.endPrefix)
	if index < 0 {
		keep := len(current.endPrefix) - 1
		if len(s.pending) > keep {
			current.appendOutput(s.pending[:len(s.pending)-keep])
			s.pending = append([]byte(nil), s.pending[len(s.pending)-keep:]...)
		}
		return
	}
	current.appendOutput(s.pending[:index])
	rest := s.pending[index+len(current.endPrefix):]
	end := bytes.IndexByte(rest, '\x1f')
	if end < 0 {
		if len(rest) > 32 {
			current.appendOutput(s.pending[index:])
			s.pending = nil
			return
		}
		s.pending = append([]byte(nil), s.pending[index:]...)
		return
	}
	code, err := strconv.Atoi(string(rest[:end]))
	if err != nil {
		code = 125
	}
	s.pending = append([]byte(nil), rest[end+1:]...)
	s.current = nil
	s.lastUsed = time.Now()
	current.complete(code)
}

func (s *persistentShellSession) isClosed() bool {
	return channelClosed(s.closed)
}

func (s *persistentShellSession) close() {
	s.closedOnce.Do(func() {
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		_ = containerTreeKill(ctx, s.containerName, s.controlDir)
		cancel()
		_ = s.terminal.Close()
		if s.cmd.Process != nil {
			_ = s.cmd.Process.Kill()
		}
		close(s.closed)
	})
}

func (e *persistentShellExecution) appendOutput(value []byte) {
	e.mu.Lock()
	e.totalBytes += len(value)
	limit := e.outputLimit
	if limit <= 0 {
		limit = maxDockerOutputBytes
	}
	if len(value) >= limit {
		e.output = append(e.output[:0], value[len(value)-limit:]...)
	} else {
		overflow := len(e.output) + len(value) - limit
		if overflow > 0 {
			copy(e.output, e.output[overflow:])
			e.output = e.output[:len(e.output)-overflow]
		}
		e.output = append(e.output, value...)
	}
	e.mu.Unlock()
}

func (e *persistentShellExecution) complete(code int) {
	e.mu.Lock()
	if !e.running {
		e.mu.Unlock()
		return
	}
	e.running = false
	e.exitCode = code
	e.mu.Unlock()
	e.doneOnce.Do(func() { close(e.done) })
}

func (e *persistentShellExecution) isRunning() bool {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.running
}

func (e *persistentShellExecution) snapshot() (bool, int, string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	output := strings.ReplaceAll(string(e.output), "\r\n", "\n")
	output = strings.ReplaceAll(output, "\r", "")
	return e.running, e.exitCode, output
}

func (m *persistentShellManager) execution(id string) *persistentShellExecution {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.executions[id]
}

func (m *persistentShellManager) Inspect(execution *Execution) *ContainerState {
	state := m.execution(execution.ID)
	if state == nil || state.runtimeID != execution.RuntimeContainerID {
		return &ContainerState{ID: execution.RuntimeContainerID, Status: "exited", ExitCode: 125}
	}
	state.mu.Lock()
	running, code := state.running, state.exitCode
	state.mu.Unlock()
	status := "exited"
	if running {
		status = "running"
	}
	return &ContainerState{ID: state.runtimeID, Status: status, Running: running, ExitCode: code}
}

func (m *persistentShellManager) Logs(execution *Execution, tail int) string {
	state := m.execution(execution.ID)
	if state == nil || state.runtimeID != execution.RuntimeContainerID {
		return ""
	}
	_, _, output := state.snapshot()
	return tailLines(output, tail)
}

func (m *persistentShellManager) Interrupt(ctx context.Context, execution *Execution) error {
	state := m.execution(execution.ID)
	if state == nil || state.runtimeID != execution.RuntimeContainerID || !state.isRunning() {
		return nil
	}
	session := state.session
	session.writeMu.Lock()
	_, err := session.terminal.Write([]byte{3})
	session.writeMu.Unlock()
	if err != nil {
		return fmt.Errorf("interrupt persistent shell command: %w", err)
	}
	timer := time.NewTimer(150 * time.Millisecond)
	select {
	case <-state.done:
		timer.Stop()
		return nil
	case <-timer.C:
	}
	if state.isRunning() {
		marker := "printf '\\036APTEVA_END_" + state.runtimeID + ":130\\037'\n"
		session.writeMu.Lock()
		_, err = session.terminal.Write([]byte(marker))
		session.writeMu.Unlock()
		if err != nil {
			return fmt.Errorf("synchronize interrupted shell command: %w", err)
		}
	}
	select {
	case <-state.done:
		return nil
	case <-ctx.Done():
		killCtx, killCancel := context.WithTimeout(context.Background(), 10*time.Second)
		killErr := containerTreeKill(killCtx, session.containerName, session.controlDir)
		killCancel()
		if killErr != nil {
			return killErr
		}
		session.close()
		state.complete(130)
		return ctx.Err()
	}
}

func (m *persistentShellManager) Remove(execution *Execution) {
	m.mu.Lock()
	delete(m.executions, execution.ID)
	m.mu.Unlock()
}

func (m *persistentShellManager) CloseSession(containerName, key string) error {
	m.mu.Lock()
	s := m.sessions[containerName+"\x00"+key]
	m.mu.Unlock()
	if s == nil {
		return nil
	}
	s.mu.Lock()
	busy := s.current != nil && s.current.isRunning()
	if !busy {
		s.closing = true
	}
	s.mu.Unlock()
	if busy {
		return errors.New("session has an active execution")
	}
	s.close()
	return nil
}
func (m *persistentShellManager) ExpireIdle() {
	m.mu.Lock()
	var idle []*persistentShellSession
	for _, s := range m.sessions {
		s.mu.Lock()
		if s.current == nil && time.Since(s.lastUsed) > 30*time.Minute {
			s.closing = true
			idle = append(idle, s)
		}
		s.mu.Unlock()
	}
	m.mu.Unlock()
	for _, s := range idle {
		s.close()
	}
}
func (m *persistentShellManager) CloseAll() {
	m.mu.Lock()
	sessions := make([]*persistentShellSession, 0, len(m.sessions))
	for _, session := range m.sessions {
		sessions = append(sessions, session)
	}
	m.sessions = make(map[string]*persistentShellSession)
	m.executions = make(map[string]*persistentShellExecution)
	m.mu.Unlock()
	for _, session := range sessions {
		session.close()
	}
}

func channelClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}
