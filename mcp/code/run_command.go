package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
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

	logPath := repoCommandLogPath(a.dataDir, repo.ID)
	if err := os.MkdirAll(filepath.Dir(logPath), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir command log dir: %w", err)
	}
	logF, err := os.OpenFile(logPath, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, fmt.Errorf("open command log: %w", err)
	}
	defer logF.Close()

	fmt.Fprintf(logF, "=== command for %s/%d (%s) at %s ===\n",
		repo.ProjectID, repo.ID, repo.Slug, time.Now().UTC().Format(time.RFC3339))

	res := &repoCommandResult{
		Status:   "failed",
		Command:  command,
		ExitCode: -1,
		LogPath:  logPath,
	}

	if plan, err := nodeDepsInstallPlan(srcDir); err != nil {
		res.Error = err.Error()
		res.LogTail, _ = tailFile(logPath, tailLines)
		return res, nil
	} else if plan.Needed {
		res.DependencyInstallRan = true
		res.DependencyInstallNote = plan.Reason
		if err := installNodeDeps(srcDir, logF, plan); err != nil {
			res.Error = err.Error()
			res.DurationMS = 0
			res.LogTail, _ = tailFile(logPath, tailLines)
			return res, nil
		}
	}

	env, err := mergeCommandEnv(in.EnvJSON)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	stdoutTail := &tailBytesWriter{Limit: 64 * 1024}
	stderrTail := &tailBytesWriter{Limit: 64 * 1024}
	cmd := exec.CommandContext(ctx, "sh", "-c", command)
	cmd.Dir = srcDir
	cmd.Env = env
	cmd.Stdout = io.MultiWriter(logF, stdoutTail)
	cmd.Stderr = io.MultiWriter(logF, stderrTail)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}

	start := time.Now()
	fmt.Fprintf(logF, "+ %s (cwd=%s, timeout=%s)\n", command, srcDir, timeout)
	err = cmd.Start()
	if err != nil {
		res.Error = fmt.Sprintf("exec command: %v", err)
		res.LogTail, _ = tailFile(logPath, tailLines)
		return res, nil
	}
	err = cmd.Wait()
	res.DurationMS = time.Since(start).Milliseconds()
	res.StdoutTail = stdoutTail.String()
	res.StderrTail = stderrTail.String()
	if ctx.Err() == context.DeadlineExceeded {
		res.TimedOut = true
		res.Error = fmt.Sprintf("command timed out after %s", timeout)
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	} else if err != nil {
		if ee, ok := err.(*exec.ExitError); ok {
			res.ExitCode = ee.ExitCode()
		}
		res.Error = err.Error()
	} else {
		res.ExitCode = 0
		res.Status = "success"
	}
	fmt.Fprintf(logF, "=== command exited at %s (exit=%d, timeout=%v, err=%v) ===\n",
		time.Now().UTC().Format(time.RFC3339), res.ExitCode, res.TimedOut, err)
	res.LogTail, _ = tailFile(logPath, tailLines)
	return res, nil
}

func repoCommandLogPath(dataDir string, repoID int64) string {
	return filepath.Join(dataDir, "command-logs", fmt.Sprintf("%d-%d.log", repoID, time.Now().UnixNano()))
}

func mergeCommandEnv(envJSON string) ([]string, error) {
	out := append([]string{}, os.Environ()...)
	if strings.TrimSpace(envJSON) == "" {
		return out, nil
	}
	var m map[string]string
	if err := json.Unmarshal([]byte(envJSON), &m); err != nil {
		return nil, fmt.Errorf("env_json is not valid JSON: %w", err)
	}
	for k, v := range m {
		out = append(out, k+"="+v)
	}
	return out, nil
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
