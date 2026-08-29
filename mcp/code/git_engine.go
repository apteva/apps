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
	"strconv"
	"strings"
	"time"
)

const (
	gitCommandTimeout = 15 * time.Minute
	gitOutputLimit    = 2 << 20
)

type gitEngine struct {
	binary   string
	dataDir  string
	hooksDir string
}

func newGitEngine(dataDir string) (*gitEngine, error) {
	binary, err := exec.LookPath("git")
	if err != nil {
		return nil, errors.New("git executable not found")
	}
	hooksDir := filepath.Join(dataDir, "git-hooks-disabled")
	if err := os.MkdirAll(hooksDir, 0o700); err != nil {
		return nil, err
	}
	return &gitEngine{binary: binary, dataDir: dataDir, hooksDir: hooksDir}, nil
}

type cappedBuffer struct {
	buf bytes.Buffer
	max int
}

func (w *cappedBuffer) Write(p []byte) (int, error) {
	n := len(p)
	if w.buf.Len() < w.max {
		keep := w.max - w.buf.Len()
		if keep > len(p) {
			keep = len(p)
		}
		_, _ = w.buf.Write(p[:keep])
	}
	return n, nil
}

func (w *cappedBuffer) String() string { return w.buf.String() }

func (g *gitEngine) run(ctx context.Context, workTree, gitDir string, auth *gitAuth, args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, gitCommandTimeout)
	defer cancel()
	fullArgs := []string{}
	if gitDir != "" {
		fullArgs = append(fullArgs, "--git-dir="+gitDir)
	}
	if workTree != "" {
		fullArgs = append(fullArgs, "--work-tree="+workTree)
	}
	fullArgs = append(fullArgs, args...)
	cmd := exec.CommandContext(ctx, g.binary, fullArgs...)
	askPass, err := newGitAskPass(g.dataDir, auth)
	if err != nil {
		return "", err
	}
	defer askPass.close()
	cmd.Env = append(safeGitEnvironment(), askPass.env()...)
	var output cappedBuffer
	output.max = gitOutputLimit
	cmd.Stdout = &output
	cmd.Stderr = &output
	err = cmd.Run()
	text := strings.TrimSpace(output.String())
	if auth != nil && auth.Password != "" {
		text = strings.ReplaceAll(text, auth.Password, "[REDACTED]")
	}
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return text, errors.New("git operation timed out")
		}
		if text == "" {
			return text, fmt.Errorf("git %s: %w", firstArg(args), err)
		}
		return text, fmt.Errorf("git %s: %s", firstArg(args), text)
	}
	return text, nil
}

func safeGitEnvironment() []string {
	allowed := []string{"PATH=", "TMPDIR=", "TMP=", "TEMP=", "LANG=", "LC_", "SSL_CERT_FILE=", "SSL_CERT_DIR=", "HTTP_PROXY=", "HTTPS_PROXY=", "NO_PROXY="}
	env := []string{}
	for _, item := range os.Environ() {
		for _, prefix := range allowed {
			if strings.HasPrefix(item, prefix) {
				env = append(env, item)
				break
			}
		}
	}
	return append(env,
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL="+nullDevice(),
		"GIT_LFS_SKIP_SMUDGE=1",
		"GIT_TERMINAL_PROMPT=0",
		"GIT_PROTOCOL_FROM_USER=0",
	)
}

func nullDevice() string {
	if filepath.Separator == '\\' {
		return "NUL"
	}
	return "/dev/null"
}

func firstArg(args []string) string {
	for _, arg := range args {
		if !strings.HasPrefix(arg, "-") {
			return arg
		}
	}
	return "command"
}

func (g *gitEngine) clone(ctx context.Context, remoteURL, ref, workTree, gitDir string, auth *gitAuth) error {
	args := []string{
		"-c", "protocol.file.allow=never",
		"-c", "protocol.ext.allow=never",
		"-c", "core.symlinks=false",
		"-c", "core.hooksPath=" + g.hooksDir,
		"clone", "--no-recurse-submodules", "--separate-git-dir=" + gitDir,
	}
	if ref != "" {
		args = append(args, "--branch", ref)
	}
	args = append(args, "--", remoteURL, workTree)
	if _, err := g.run(ctx, "", "", auth, args...); err != nil {
		return err
	}
	return g.configure(ctx, workTree, gitDir)
}

func (g *gitEngine) configure(ctx context.Context, workTree, gitDir string) error {
	settings := [][2]string{
		{"core.worktree", workTree},
		{"core.hooksPath", g.hooksDir},
		{"core.symlinks", "false"},
		{"protocol.file.allow", "never"},
		{"protocol.ext.allow", "never"},
		{"submodule.recurse", "false"},
	}
	for _, setting := range settings {
		if _, err := g.run(ctx, workTree, gitDir, nil, "config", "--local", setting[0], setting[1]); err != nil {
			return err
		}
	}
	return nil
}

type GitChange struct {
	Path     string `json:"path"`
	Original string `json:"original_path,omitempty"`
	Index    string `json:"index"`
	WorkTree string `json:"worktree"`
}

type GitStatus struct {
	GitBacked  bool        `json:"git_backed"`
	Branch     string      `json:"branch,omitempty"`
	Detached   bool        `json:"detached,omitempty"`
	HeadSHA    string      `json:"head_sha,omitempty"`
	Upstream   string      `json:"upstream,omitempty"`
	Ahead      int         `json:"ahead"`
	Behind     int         `json:"behind"`
	Dirty      bool        `json:"dirty"`
	Conflicted bool        `json:"conflicted"`
	Changes    []GitChange `json:"changes"`
	Remote     *GitRemote  `json:"remote,omitempty"`
}

func (g *gitEngine) status(ctx context.Context, workTree, gitDir string) (*GitStatus, error) {
	status := &GitStatus{GitBacked: true, Changes: []GitChange{}}
	branch, branchErr := g.run(ctx, workTree, gitDir, nil, "symbolic-ref", "--quiet", "--short", "HEAD")
	if branchErr != nil {
		status.Detached = true
	} else {
		status.Branch = strings.TrimSpace(branch)
	}
	if head, err := g.run(ctx, workTree, gitDir, nil, "rev-parse", "--verify", "HEAD"); err == nil {
		status.HeadSHA = strings.TrimSpace(head)
	}
	if upstream, err := g.run(ctx, workTree, gitDir, nil, "rev-parse", "--abbrev-ref", "--symbolic-full-name", "@{upstream}"); err == nil {
		status.Upstream = strings.TrimSpace(upstream)
		if counts, err := g.run(ctx, workTree, gitDir, nil, "rev-list", "--left-right", "--count", "HEAD...@{upstream}"); err == nil {
			parts := strings.Fields(counts)
			if len(parts) == 2 {
				status.Ahead, _ = strconv.Atoi(parts[0])
				status.Behind, _ = strconv.Atoi(parts[1])
			}
		}
	}
	out, err := g.run(ctx, workTree, gitDir, nil, "status", "--porcelain=v1", "-z", "--untracked-files=all")
	if err != nil {
		return nil, err
	}
	status.Changes = parseGitPorcelain([]byte(out))
	status.Dirty = len(status.Changes) > 0
	for _, change := range status.Changes {
		xy := change.Index + change.WorkTree
		if strings.Contains(xy, "U") || xy == "AA" || xy == "DD" {
			status.Conflicted = true
			break
		}
	}
	return status, nil
}

func parseGitPorcelain(body []byte) []GitChange {
	records := bytes.Split(body, []byte{0})
	out := []GitChange{}
	for i := 0; i < len(records); i++ {
		record := records[i]
		if len(record) < 4 {
			continue
		}
		change := GitChange{Index: string(record[0]), WorkTree: string(record[1]), Path: string(record[3:])}
		if (record[0] == 'R' || record[0] == 'C') && i+1 < len(records) {
			change.Original = string(records[i+1])
			i++
		}
		out = append(out, change)
	}
	return out
}

func (g *gitEngine) head(ctx context.Context, workTree, gitDir string) string {
	head, err := g.run(ctx, workTree, gitDir, nil, "rev-parse", "--verify", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(head)
}

func (g *gitEngine) fetch(ctx context.Context, workTree, gitDir, remoteURL, remoteName string, auth *gitAuth) error {
	if remoteName == "" {
		remoteName = "origin"
	}
	_, err := g.run(ctx, workTree, gitDir, auth, "fetch", "--prune", "--tags", remoteURL,
		"+refs/heads/*:refs/remotes/"+remoteName+"/*")
	return err
}

func (g *gitEngine) fastForward(ctx context.Context, workTree, gitDir, remoteName, branch string) error {
	if branch == "" {
		return errors.New("current branch has no upstream")
	}
	if remoteName == "" {
		remoteName = "origin"
	}
	_, err := g.run(ctx, workTree, gitDir, nil, "merge", "--ff-only", "--no-edit", remoteName+"/"+branch)
	return err
}

func (g *gitEngine) remoteDefaultBranch(ctx context.Context, remoteURL string, auth *gitAuth) (string, error) {
	out, err := g.run(ctx, "", "", auth, "ls-remote", "--symref", remoteURL, "HEAD")
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 3 && fields[0] == "ref:" && fields[2] == "HEAD" {
			return strings.TrimPrefix(fields[1], "refs/heads/"), nil
		}
	}
	return "", errors.New("remote default branch could not be determined; pass branch explicitly")
}

func (g *gitEngine) commit(ctx context.Context, workTree, gitDir, message string, paths []string, authorName, authorEmail string) (string, error) {
	if strings.TrimSpace(message) == "" {
		return "", errors.New("commit message required")
	}
	if authorName == "" {
		authorName = "Apteva Code"
	}
	if authorEmail == "" {
		authorEmail = "code@apteva.local"
	}
	if len(paths) == 0 {
		if _, err := g.run(ctx, workTree, gitDir, nil, "add", "--all"); err != nil {
			return "", err
		}
	} else {
		args := []string{"--literal-pathspecs", "add", "--"}
		args = append(args, paths...)
		if _, err := g.run(ctx, workTree, gitDir, nil, args...); err != nil {
			return "", err
		}
	}
	_, err := g.run(ctx, workTree, gitDir, nil,
		"-c", "user.name="+authorName,
		"-c", "user.email="+authorEmail,
		"commit", "-m", message)
	if err != nil {
		return "", err
	}
	return g.head(ctx, workTree, gitDir), nil
}

func (g *gitEngine) push(ctx context.Context, workTree, gitDir, remoteName, branch string, setUpstream bool, auth *gitAuth) error {
	if branch == "" {
		return errors.New("cannot push a detached HEAD")
	}
	if remoteName == "" {
		remoteName = "origin"
	}
	args := []string{"push"}
	if setUpstream {
		args = append(args, "--set-upstream")
	}
	args = append(args, remoteName, "HEAD:refs/heads/"+branch)
	_, err := g.run(ctx, workTree, gitDir, auth, args...)
	return err
}

func (g *gitEngine) updateRemoteTracking(ctx context.Context, workTree, gitDir, remoteName, branch string) {
	if remoteName == "" {
		remoteName = "origin"
	}
	_, _ = g.run(ctx, workTree, gitDir, nil, "update-ref", "refs/remotes/"+remoteName+"/"+branch, "HEAD")
}

func (g *gitEngine) diff(ctx context.Context, workTree, gitDir, base string, maxBytes int) (string, bool, error) {
	if maxBytes <= 0 || maxBytes > gitOutputLimit {
		maxBytes = 256 << 10
	}
	args := []string{"diff", "--no-ext-diff", "--no-color"}
	if base != "" {
		args = append(args, base)
	}
	out, err := g.run(ctx, workTree, gitDir, nil, args...)
	if err != nil {
		return "", false, err
	}
	truncated := len(out) > maxBytes
	if truncated {
		out = out[:maxBytes]
	}
	return out, truncated, nil
}

type GitCommit struct {
	SHA        string `json:"sha"`
	Author     string `json:"author"`
	AuthoredAt string `json:"authored_at"`
	Subject    string `json:"subject"`
}

type GitBranch struct {
	Name     string `json:"name"`
	Current  bool   `json:"current"`
	Remote   bool   `json:"remote"`
	Upstream string `json:"upstream,omitempty"`
	SHA      string `json:"sha"`
}

func (g *gitEngine) branches(ctx context.Context, workTree, gitDir string) ([]GitBranch, error) {
	format := "%(refname)%00%(HEAD)%00%(upstream:short)%00%(objectname)"
	out, err := g.run(ctx, workTree, gitDir, nil, "for-each-ref", "--format="+format, "refs/heads", "refs/remotes")
	if err != nil {
		return nil, err
	}
	branches := []GitBranch{}
	for _, record := range strings.Split(out, "\n") {
		parts := strings.Split(record, "\x00")
		if len(parts) != 4 {
			continue
		}
		name := strings.TrimPrefix(parts[0], "refs/heads/")
		remote := strings.HasPrefix(parts[0], "refs/remotes/")
		if remote {
			name = strings.TrimPrefix(parts[0], "refs/remotes/")
			if strings.HasSuffix(name, "/HEAD") {
				continue
			}
		}
		branches = append(branches, GitBranch{Name: name, Current: strings.TrimSpace(parts[1]) == "*", Remote: remote, Upstream: parts[2], SHA: parts[3]})
	}
	return branches, nil
}

func (g *gitEngine) createBranch(ctx context.Context, workTree, gitDir, name, startPoint string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("branch name required")
	}
	if _, err := g.run(ctx, workTree, gitDir, nil, "check-ref-format", "--branch", name); err != nil {
		return errors.New("invalid branch name")
	}
	args := []string{"branch", name}
	if startPoint != "" {
		args = append(args, startPoint)
	}
	_, err := g.run(ctx, workTree, gitDir, nil, args...)
	return err
}

func (g *gitEngine) switchBranch(ctx context.Context, workTree, gitDir, name string) error {
	if strings.TrimSpace(name) == "" {
		return errors.New("branch name required")
	}
	if _, err := g.run(ctx, workTree, gitDir, nil, "check-ref-format", "--branch", name); err != nil {
		return errors.New("invalid branch name")
	}
	_, err := g.run(ctx, workTree, gitDir, nil, "switch", name)
	return err
}

func (g *gitEngine) log(ctx context.Context, workTree, gitDir string, limit int) ([]GitCommit, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	format := "%H%x1f%an%x1f%aI%x1f%s%x1e"
	out, err := g.run(ctx, workTree, gitDir, nil, "log", "-n", strconv.Itoa(limit), "--format="+format)
	if err != nil {
		return nil, err
	}
	commits := []GitCommit{}
	for _, record := range strings.Split(out, "\x1e") {
		parts := strings.Split(strings.TrimSpace(record), "\x1f")
		if len(parts) != 4 {
			continue
		}
		commits = append(commits, GitCommit{SHA: parts[0], Author: parts[1], AuthoredAt: parts[2], Subject: parts[3]})
	}
	return commits, nil
}

func copyFile(dst string, src io.Reader, mode os.FileMode) error {
	f, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, mode)
	if err != nil {
		return err
	}
	_, copyErr := io.Copy(f, src)
	closeErr := f.Close()
	if copyErr != nil {
		return copyErr
	}
	return closeErr
}
