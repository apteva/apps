package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

type repoCommandInput struct {
	Command        string
	EnvJSON        string
	TimeoutSeconds int
	TailLines      int
}

type repoCommandResult struct {
	Status                string `json:"status"`
	Command               string `json:"command"`
	ExitCode              int    `json:"exit_code"`
	DurationMS            int64  `json:"duration_ms"`
	TimedOut              bool   `json:"timed_out"`
	DependencyInstallRan  bool   `json:"dependency_install_ran"`
	DependencyInstallNote string `json:"dependency_install_note,omitempty"`
	LogPath               string `json:"log_path"`
	LogTail               string `json:"log_tail"`
	StdoutTail            string `json:"stdout_tail"`
	StderrTail            string `json:"stderr_tail"`
	Error                 string `json:"error,omitempty"`
}

func (a *App) runRepoCommand(repo *Repo, srcDir string, in repoCommandInput) (*repoCommandResult, error) {
	command := strings.TrimSpace(in.Command)
	if command == "" {
		return nil, errors.New("command required")
	}
	timeout := time.Duration(in.TimeoutSeconds) * time.Second
	if timeout <= 0 {
		timeout = 5 * time.Minute
	}
	if timeout > 30*time.Minute {
		timeout = 30 * time.Minute
	}
	tailLines := in.TailLines
	if tailLines <= 0 {
		tailLines = 200
	}
	if tailLines > 2000 {
		tailLines = 2000
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	release, err := a.commands.acquire(ctx, repo.ID)
	if err != nil {
		return &repoCommandResult{Status: "failed", Command: command, ExitCode: -1, TimedOut: isContextDeadline(err), Error: err.Error()}, nil
	}
	defer release()
	started := time.Now()

	logPath := repoCommandLogPath(a.dataDir, repo.ID)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir command log dir: %w", err)
	}
	logF, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o600)
	if err != nil {
		return nil, fmt.Errorf("open command log: %w", err)
	}
	defer logF.Close()
	logOut := newCappedWriter(logF, maxProcessLogBytes)

	fmt.Fprintf(logOut, "=== command for %s/%d (%s) at %s ===\n",
		repo.ProjectID, repo.ID, repo.Slug, time.Now().UTC().Format(time.RFC3339))

	res := &repoCommandResult{
		Status:   "failed",
		Command:  command,
		ExitCode: -1,
		LogPath:  logPath,
	}
	env, err := commandEnvironment(a.dataDir, in.EnvJSON)
	if err != nil {
		return nil, err
	}
	if repo.ProjectID != "" {
		env = append(env, "APTEVA_PROJECT_ID="+repo.ProjectID)
	}

	if plan, err := nodeDepsInstallPlan(srcDir); err != nil {
		res.Error = err.Error()
		res.LogTail, _ = tailFile(logPath, tailLines)
		return res, nil
	} else if plan.Needed {
		res.DependencyInstallRan = true
		res.DependencyInstallNote = plan.Reason
		if err := installNodeDeps(ctx, srcDir, logOut, plan, env); err != nil {
			res.Error = err.Error()
			res.DurationMS = time.Since(started).Milliseconds()
			res.TimedOut = isContextDeadline(err)
			res.LogTail, _ = tailFile(logPath, tailLines)
			return res, nil
		}
	}

	stdoutTail := &tailBytesWriter{Limit: 64 * 1024}
	stderrTail := &tailBytesWriter{Limit: 64 * 1024}
	cmd := exec.Command("sh", "-c", command)
	cmd.Dir = srcDir
	cmd.Env = env
	cmd.Stdout = io.MultiWriter(logOut, stdoutTail)
	cmd.Stderr = io.MultiWriter(logOut, stderrTail)
	fmt.Fprintf(logOut, "+ %s (cwd=%s, timeout=%s)\n", command, srcDir, timeout)
	err = runProcessGroup(ctx, cmd)
	if err != nil && cmd.Process == nil {
		res.Error = fmt.Sprintf("exec command: %v", err)
		res.DurationMS = time.Since(started).Milliseconds()
		res.LogTail, _ = tailFile(logPath, tailLines)
		return res, nil
	}
	res.DurationMS = time.Since(started).Milliseconds()
	res.StdoutTail = stdoutTail.String()
	res.StderrTail = stderrTail.String()
	if isContextDeadline(err) {
		res.TimedOut = true
		res.Error = fmt.Sprintf("command timed out after %s", timeout)
	} else if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			res.ExitCode = ee.ExitCode()
		}
		res.Error = err.Error()
	} else {
		res.ExitCode = 0
		res.Status = "success"
	}
	fmt.Fprintf(logOut, "=== command exited at %s (exit=%d, timeout=%v, err=%v) ===\n",
		time.Now().UTC().Format(time.RFC3339), res.ExitCode, res.TimedOut, err)
	res.LogTail, _ = tailFile(logPath, tailLines)
	return res, nil
}

func repoCommandLogPath(dataDir string, repoID int64) string {
	dir := filepath.Join(dataDir, "command-logs")
	cleanupCommandLogs(dir, repoID)
	return filepath.Join(dir, fmt.Sprintf("%d-%d.log", repoID, time.Now().UnixNano()))
}

func cleanupCommandLogs(dir string, repoID int64) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	prefix := fmt.Sprintf("%d-", repoID)
	type candidate struct {
		path string
		mod  time.Time
	}
	var logs []candidate
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasPrefix(entry.Name(), prefix) || !strings.HasSuffix(entry.Name(), ".log") {
			continue
		}
		if info, statErr := entry.Info(); statErr == nil {
			logs = append(logs, candidate{path: filepath.Join(dir, entry.Name()), mod: info.ModTime()})
		}
	}
	sort.Slice(logs, func(i, j int) bool { return logs[i].mod.After(logs[j].mod) })
	cutoff := time.Now().Add(-7 * 24 * time.Hour)
	for i, log := range logs {
		if i >= 50 || log.mod.Before(cutoff) {
			_ = os.Remove(log.path)
		}
	}
}

type tailBytesWriter struct {
	Limit int
	buf   bytes.Buffer
}

func (w *tailBytesWriter) Write(p []byte) (int, error) {
	n := len(p)
	_, _ = w.buf.Write(p)
	if w.Limit > 0 && w.buf.Len() > w.Limit {
		b := w.buf.Bytes()
		keep := append([]byte(nil), b[len(b)-w.Limit:]...)
		w.buf.Reset()
		_, _ = w.buf.Write(keep)
	}
	return n, nil
}

func (w *tailBytesWriter) String() string {
	return w.buf.String()
}
