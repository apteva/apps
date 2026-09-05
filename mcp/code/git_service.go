package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

type gitService struct {
	engine  *gitEngine
	store   *LocalFileStore
	locks   *repoLockSet
	dataDir string
}

func newGitService(dataDir string, store *LocalFileStore, locks *repoLockSet) (*gitService, error) {
	engine, err := newGitEngine(dataDir)
	if err != nil {
		return nil, err
	}
	gitRoot := filepath.Join(dataDir, "git")
	if err := os.MkdirAll(gitRoot, 0o700); err != nil {
		return nil, err
	}
	return &gitService{engine: engine, store: store, locks: locks, dataDir: dataDir}, nil
}

func (s *gitService) gitDir(repoID int64) string {
	return filepath.Join(s.dataDir, "git", fmt.Sprintf("repo-%d.git", repoID))
}

func (s *gitService) paths(repo *Repo) (string, string, error) {
	if repo == nil {
		return "", "", errors.New("repository required")
	}
	workTree := s.store.RepoPath(repoStoreKey(repo))
	gitDir := s.gitDir(repo.ID)
	if workTree == "" {
		return "", "", errors.New("Git requires a local repository workspace")
	}
	if _, err := os.Stat(gitDir); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", "", errors.New("repository is not Git-backed")
		}
		return "", "", err
	}
	return workTree, gitDir, nil
}

type GitImportInput struct {
	RemoteURL    string
	Ref          string
	Name         string
	Slug         string
	Description  string
	Framework    string
	ProjectID    string
	ConnectionID int64
}

type GitImportResult struct {
	Repository *Repo      `json:"repository"`
	Remote     *GitRemote `json:"remote"`
	Status     *GitStatus `json:"status"`
	FileCount  int        `json:"file_count"`
}

func (s *gitService) Import(ctx *sdk.AppCtx, in GitImportInput) (*GitImportResult, error) {
	remote, err := validateGitRemoteURL(in.RemoteURL)
	if err != nil {
		return nil, err
	}
	in.RemoteURL = remote.String()
	auth, err := gitAuthForRemote(ctx, in.RemoteURL, in.ConnectionID)
	if err != nil {
		return nil, err
	}
	if in.Slug == "" {
		base := strings.TrimSuffix(filepath.Base(remote.Path), ".git")
		in.Slug = base
	}
	in.Slug = slugify(in.Slug)
	if in.Name == "" {
		in.Name = strings.TrimSuffix(strings.TrimPrefix(remote.Path, "/"), ".git")
	}
	if in.Description == "" {
		in.Description = "Cloned from " + in.RemoteURL
	}
	if in.Framework != "" && !validFramework(in.Framework) {
		return nil, fmt.Errorf("framework %q not supported", in.Framework)
	}

	stage, err := os.MkdirTemp(s.dataDir, "git-import-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)
	stageWork := filepath.Join(stage, "worktree")
	stageGit := filepath.Join(stage, "gitdir")
	if err := s.engine.clone(context.Background(), in.RemoteURL, in.Ref, stageWork, stageGit, auth); err != nil {
		return nil, err
	}
	if in.Framework == "" {
		in.Framework = detectFrameworkFromDir(stageWork)
		if in.Framework == "" {
			in.Framework = "blank"
		}
	}
	fileCount, err := countSourceFiles(stageWork)
	if err != nil {
		return nil, err
	}

	repo, err := dbCreateRepo(ctx.AppDB(), in.ProjectID, CreateRepoInput{
		Name: in.Name, Slug: in.Slug, Description: in.Description, Framework: in.Framework,
	})
	if err != nil {
		return nil, err
	}
	key := repoStoreKey(repo)
	defer s.locks.lock(key)()
	rollback := true
	defer func() {
		if rollback {
			_ = dbHardDeleteRepo(ctx.AppDB(), in.ProjectID, repo.Slug)
			_ = s.store.DropRepo(key)
			_ = os.RemoveAll(s.gitDir(repo.ID))
		}
	}()

	finalWork := s.store.RepoPath(key)
	finalGit := s.gitDir(repo.ID)
	if err := os.MkdirAll(filepath.Dir(finalWork), 0o755); err != nil {
		return nil, err
	}
	if err := os.MkdirAll(filepath.Dir(finalGit), 0o700); err != nil {
		return nil, err
	}
	if err := moveTree(stageGit, finalGit); err != nil {
		return nil, fmt.Errorf("install Git metadata: %w", err)
	}
	if err := moveTree(stageWork, finalWork); err != nil {
		return nil, fmt.Errorf("install working tree: %w", err)
	}
	if err := os.WriteFile(filepath.Join(finalWork, ".git"), []byte("gitdir: "+finalGit+"\n"), 0o600); err != nil {
		return nil, err
	}
	if err := s.engine.configure(context.Background(), finalWork, finalGit); err != nil {
		return nil, err
	}
	branch := in.Ref
	status, err := s.engine.status(context.Background(), finalWork, finalGit)
	if err != nil {
		return nil, err
	}
	if branch == "" {
		branch = status.Branch
	}
	remoteRow, err := dbUpsertGitRemote(ctx.AppDB(), GitRemote{
		RepoID: repo.ID, Name: "origin", FetchURL: in.RemoteURL,
		PushURL: in.RemoteURL, ConnectionID: auth.ConnectionID,
		ProviderSlug: auth.ProviderSlug, DefaultBranch: branch,
	})
	if err != nil {
		return nil, err
	}
	_ = dbRecordImport(ctx.AppDB(), repo.ID, "git:"+in.RemoteURL)
	status.Remote = remoteRow
	rollback = false
	dbMarkGitRemoteResult(ctx.AppDB(), repo.ID, "origin", "clone", nil)
	dbRecordGitOperation(ctx.AppDB(), repo.ID, "clone", "", "", status.HeadSHA, nil)
	ctx.Emit("repo.added", map[string]any{"id": repo.ID, "slug": repo.Slug, "name": repo.Name, "framework": repo.Framework, "imported_from": "git"})
	ctx.Emit("repo.git.connected", map[string]any{"id": repo.ID, "slug": repo.Slug, "branch": status.Branch, "head_sha": status.HeadSHA})
	return &GitImportResult{Repository: repo, Remote: remoteRow, Status: status, FileCount: fileCount}, nil
}

type GitConnectInput struct {
	RemoteURL    string
	Branch       string
	ConnectionID int64
}

type GitConnectResult struct {
	Remote                 *GitRemote `json:"remote"`
	Status                 *GitStatus `json:"status"`
	ReconciliationRequired bool       `json:"reconciliation_required"`
	SafetyBranch           string     `json:"safety_branch,omitempty"`
}

func (s *gitService) Connect(ctx *sdk.AppCtx, repo *Repo, in GitConnectInput) (*GitConnectResult, error) {
	remoteURL, err := validateGitRemoteURL(in.RemoteURL)
	if err != nil {
		return nil, err
	}
	auth, err := gitAuthForRemote(ctx, remoteURL.String(), in.ConnectionID)
	if err != nil {
		return nil, err
	}
	branch := strings.TrimSpace(in.Branch)
	if branch == "" {
		branch, err = s.engine.remoteDefaultBranch(context.Background(), remoteURL.String(), auth)
		if err != nil {
			return nil, err
		}
	}
	defer s.locks.lock(repoStoreKey(repo))()
	workTree := s.store.RepoPath(repoStoreKey(repo))
	gitDir := s.gitDir(repo.ID)
	if _, err := os.Stat(gitDir); err == nil {
		return nil, errors.New("repository is already Git-backed")
	}
	if _, err := s.engine.run(context.Background(), "", "", nil,
		"init", "--separate-git-dir="+gitDir, "--initial-branch=apteva-local", workTree); err != nil {
		return nil, err
	}
	cleanup := true
	defer func() {
		if cleanup {
			_ = os.RemoveAll(gitDir)
			_ = os.Remove(filepath.Join(workTree, ".git"))
		}
	}()
	if err := s.engine.configure(context.Background(), workTree, gitDir); err != nil {
		return nil, err
	}
	if _, err := s.engine.run(context.Background(), workTree, gitDir, nil, "remote", "add", "origin", remoteURL.String()); err != nil {
		return nil, err
	}
	if err := s.engine.fetch(context.Background(), workTree, gitDir, remoteURL.String(), "origin", auth); err != nil {
		return nil, err
	}
	if _, err := s.engine.run(context.Background(), workTree, gitDir, nil, "add", "--all", "--force"); err != nil {
		return nil, err
	}
	localTree, err := s.engine.run(context.Background(), workTree, gitDir, nil, "write-tree")
	if err != nil {
		return nil, err
	}
	remoteTree, err := s.engine.run(context.Background(), workTree, gitDir, nil, "rev-parse", "refs/remotes/origin/"+branch+"^{tree}")
	if err != nil {
		return nil, fmt.Errorf("remote branch %q not found: %w", branch, err)
	}
	reconcile := strings.TrimSpace(localTree) != strings.TrimSpace(remoteTree)
	safetyBranch := ""
	if reconcile {
		safetyBranch = "apteva/local-before-connect"
		if _, err := s.engine.run(context.Background(), workTree, gitDir, nil, "checkout", "-B", safetyBranch); err != nil {
			return nil, err
		}
		if _, err := s.engine.run(context.Background(), workTree, gitDir, nil,
			"-c", "user.name=Apteva Code", "-c", "user.email=code@apteva.local",
			"commit", "--allow-empty", "-m", "Preserve local files before connecting remote"); err != nil {
			return nil, err
		}
	} else {
		if _, err := s.engine.run(context.Background(), workTree, gitDir, nil, "checkout", "-B", branch, "origin/"+branch); err != nil {
			return nil, err
		}
		_, _ = s.engine.run(context.Background(), workTree, gitDir, nil, "branch", "--set-upstream-to=origin/"+branch, branch)
	}
	remoteRow, err := dbUpsertGitRemote(ctx.AppDB(), GitRemote{
		RepoID: repo.ID, Name: "origin", FetchURL: remoteURL.String(), PushURL: remoteURL.String(),
		ConnectionID: auth.ConnectionID, ProviderSlug: auth.ProviderSlug, DefaultBranch: branch,
	})
	if err != nil {
		return nil, err
	}
	status, err := s.engine.status(context.Background(), workTree, gitDir)
	if err != nil {
		return nil, err
	}
	status.Remote = remoteRow
	cleanup = false
	dbMarkGitRemoteResult(ctx.AppDB(), repo.ID, "origin", "connect", nil)
	dbRecordGitOperation(ctx.AppDB(), repo.ID, "connect", "", "", status.HeadSHA, nil)
	ctx.Emit("repo.git.connected", map[string]any{"id": repo.ID, "slug": repo.Slug, "branch": status.Branch, "reconciliation_required": reconcile})
	return &GitConnectResult{Remote: remoteRow, Status: status, ReconciliationRequired: reconcile, SafetyBranch: safetyBranch}, nil
}

func (s *gitService) Status(ctx *sdk.AppCtx, repo *Repo) (*GitStatus, error) {
	defer s.locks.rlock(repoStoreKey(repo))()
	workTree, gitDir, err := s.paths(repo)
	if err != nil {
		if strings.Contains(err.Error(), "not Git-backed") {
			return &GitStatus{GitBacked: false, Changes: []GitChange{}}, nil
		}
		return nil, err
	}
	status, err := s.engine.status(context.Background(), workTree, gitDir)
	if err != nil {
		return nil, err
	}
	status.Remote, _ = dbGetGitRemote(ctx.AppDB(), repo.ID, "origin")
	return status, nil
}

func (s *gitService) Fetch(ctx *sdk.AppCtx, repo *Repo, actor string) (*GitStatus, error) {
	return s.networkOperation(ctx, repo, "fetch", actor, func(workTree, gitDir string, remote *GitRemote, auth *gitAuth) error {
		return s.engine.fetch(context.Background(), workTree, gitDir, remote.FetchURL, remote.Name, auth)
	})
}

func (s *gitService) Pull(ctx *sdk.AppCtx, repo *Repo, actor string) (*GitStatus, error) {
	return s.networkOperation(ctx, repo, "pull", actor, func(workTree, gitDir string, remote *GitRemote, auth *gitAuth) error {
		status, err := s.engine.status(context.Background(), workTree, gitDir)
		if err != nil {
			return err
		}
		if status.Dirty {
			return errors.New("working tree has uncommitted changes; commit them before pulling")
		}
		if status.Conflicted {
			return errors.New("repository has unresolved conflicts")
		}
		branch := status.Branch
		if branch == "" {
			return errors.New("cannot pull a detached HEAD")
		}
		if err := s.engine.fetch(context.Background(), workTree, gitDir, remote.FetchURL, remote.Name, auth); err != nil {
			return err
		}
		return s.engine.fastForward(context.Background(), workTree, gitDir, remote.Name, branch)
	})
}

func (s *gitService) Push(ctx *sdk.AppCtx, repo *Repo, actor string, setUpstream bool) (*GitStatus, error) {
	return s.networkOperation(ctx, repo, "push", actor, func(workTree, gitDir string, remote *GitRemote, auth *gitAuth) error {
		status, err := s.engine.status(context.Background(), workTree, gitDir)
		if err != nil {
			return err
		}
		if status.Conflicted {
			return errors.New("repository has unresolved conflicts")
		}
		pushURL := firstNonEmpty(remote.PushURL, remote.FetchURL)
		pushAuth, err := gitAuthForRemote(ctx, pushURL, remote.ConnectionID)
		if err != nil {
			return err
		}
		if err := s.engine.push(context.Background(), workTree, gitDir, remote.Name, status.Branch, setUpstream || status.Upstream == "", pushAuth); err != nil {
			return err
		}
		s.engine.updateRemoteTracking(context.Background(), workTree, gitDir, remote.Name, status.Branch)
		return nil
	})
}

func (s *gitService) networkOperation(ctx *sdk.AppCtx, repo *Repo, operation, actor string, fn func(string, string, *GitRemote, *gitAuth) error) (*GitStatus, error) {
	defer s.locks.lock(repoStoreKey(repo))()
	workTree, gitDir, err := s.paths(repo)
	if err != nil {
		return nil, err
	}
	remote, err := dbGetGitRemote(ctx.AppDB(), repo.ID, "origin")
	if err != nil {
		return nil, err
	}
	if remote == nil {
		return nil, errors.New("origin remote is not configured")
	}
	auth, err := gitAuthForRemote(ctx, remote.FetchURL, remote.ConnectionID)
	if err != nil {
		return nil, err
	}
	before := s.engine.head(context.Background(), workTree, gitDir)
	opErr := fn(workTree, gitDir, remote, auth)
	after := s.engine.head(context.Background(), workTree, gitDir)
	dbMarkGitRemoteResult(ctx.AppDB(), repo.ID, remote.Name, operation, opErr)
	dbRecordGitOperation(ctx.AppDB(), repo.ID, operation, actor, before, after, opErr)
	if opErr != nil {
		return nil, opErr
	}
	status, err := s.engine.status(context.Background(), workTree, gitDir)
	if err != nil {
		return nil, err
	}
	remote, _ = dbGetGitRemote(ctx.AppDB(), repo.ID, remote.Name)
	status.Remote = remote
	ctx.Emit("repo.git."+operation+"ed", map[string]any{"id": repo.ID, "slug": repo.Slug, "branch": status.Branch, "head_sha": status.HeadSHA})
	return status, nil
}

func (s *gitService) Commit(ctx *sdk.AppCtx, repo *Repo, message string, paths []string, authorName, authorEmail, actor string) (*GitStatus, error) {
	cleanPaths := []string{}
	for _, path := range paths {
		clean, err := normalisePath(path)
		if err != nil {
			return nil, err
		}
		cleanPaths = append(cleanPaths, clean)
	}
	defer s.locks.lock(repoStoreKey(repo))()
	workTree, gitDir, err := s.paths(repo)
	if err != nil {
		return nil, err
	}
	before := s.engine.head(context.Background(), workTree, gitDir)
	_, opErr := s.engine.commit(context.Background(), workTree, gitDir, message, cleanPaths, authorName, authorEmail)
	after := s.engine.head(context.Background(), workTree, gitDir)
	dbRecordGitOperation(ctx.AppDB(), repo.ID, "commit", actor, before, after, opErr)
	if opErr != nil {
		return nil, opErr
	}
	status, err := s.engine.status(context.Background(), workTree, gitDir)
	if err != nil {
		return nil, err
	}
	status.Remote, _ = dbGetGitRemote(ctx.AppDB(), repo.ID, "origin")
	ctx.Emit("repo.git.committed", map[string]any{"id": repo.ID, "slug": repo.Slug, "branch": status.Branch, "head_sha": status.HeadSHA})
	return status, nil
}

func (s *gitService) Diff(ctx *sdk.AppCtx, repo *Repo, base string, maxBytes int) (string, bool, error) {
	defer s.locks.rlock(repoStoreKey(repo))()
	workTree, gitDir, err := s.paths(repo)
	if err != nil {
		return "", false, err
	}
	return s.engine.diff(context.Background(), workTree, gitDir, base, maxBytes)
}

func (s *gitService) Log(ctx *sdk.AppCtx, repo *Repo, limit int) ([]GitCommit, error) {
	defer s.locks.rlock(repoStoreKey(repo))()
	workTree, gitDir, err := s.paths(repo)
	if err != nil {
		return nil, err
	}
	return s.engine.log(context.Background(), workTree, gitDir, limit)
}

func (s *gitService) Branches(ctx *sdk.AppCtx, repo *Repo) ([]GitBranch, error) {
	defer s.locks.rlock(repoStoreKey(repo))()
	workTree, gitDir, err := s.paths(repo)
	if err != nil {
		return nil, err
	}
	return s.engine.branches(context.Background(), workTree, gitDir)
}

func (s *gitService) CreateBranch(ctx *sdk.AppCtx, repo *Repo, name, startPoint, actor string) (*GitStatus, error) {
	defer s.locks.lock(repoStoreKey(repo))()
	workTree, gitDir, err := s.paths(repo)
	if err != nil {
		return nil, err
	}
	before := s.engine.head(context.Background(), workTree, gitDir)
	opErr := s.engine.createBranch(context.Background(), workTree, gitDir, name, startPoint)
	dbRecordGitOperation(ctx.AppDB(), repo.ID, "branch_create", actor, before, before, opErr)
	if opErr != nil {
		return nil, opErr
	}
	status, err := s.engine.status(context.Background(), workTree, gitDir)
	if err == nil {
		status.Remote, _ = dbGetGitRemote(ctx.AppDB(), repo.ID, "origin")
	}
	return status, err
}

func (s *gitService) Switch(ctx *sdk.AppCtx, repo *Repo, name, actor string) (*GitStatus, error) {
	defer s.locks.lock(repoStoreKey(repo))()
	workTree, gitDir, err := s.paths(repo)
	if err != nil {
		return nil, err
	}
	status, err := s.engine.status(context.Background(), workTree, gitDir)
	if err != nil {
		return nil, err
	}
	if status.Dirty || status.Conflicted {
		return nil, errors.New("working tree must be clean before switching branches")
	}
	before := status.HeadSHA
	opErr := s.engine.switchBranch(context.Background(), workTree, gitDir, name)
	after := s.engine.head(context.Background(), workTree, gitDir)
	dbRecordGitOperation(ctx.AppDB(), repo.ID, "switch", actor, before, after, opErr)
	if opErr != nil {
		return nil, opErr
	}
	status, err = s.engine.status(context.Background(), workTree, gitDir)
	if err != nil {
		return nil, err
	}
	status.Remote, _ = dbGetGitRemote(ctx.AppDB(), repo.ID, "origin")
	ctx.Emit("repo.git.switched", map[string]any{"id": repo.ID, "slug": repo.Slug, "branch": status.Branch, "head_sha": status.HeadSHA})
	return status, nil
}

func detectFrameworkFromDir(root string) string {
	files := map[string][]byte{}
	for _, name := range []string{"go.mod", "package.json", "requirements.txt", "pyproject.toml", "index.html"} {
		body, err := os.ReadFile(filepath.Join(root, name))
		if err == nil {
			files[name] = body
		}
	}
	return detectImportFramework(files)
}

func countSourceFiles(root string) (int, error) {
	count := 0
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if strings.EqualFold(entry.Name(), ".git") {
			if entry.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}
		if !entry.IsDir() {
			count++
		}
		return nil
	})
	return count, err
}

func moveTree(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	info, err := os.Stat(src)
	if err != nil {
		return err
	}
	if !info.IsDir() {
		return errors.New("source tree is not a directory")
	}
	if err := os.MkdirAll(dst, info.Mode().Perm()); err != nil {
		return err
	}
	err = filepath.WalkDir(src, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		rel, err := filepath.Rel(src, path)
		if err != nil || rel == "." {
			return err
		}
		target := filepath.Join(dst, rel)
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return os.MkdirAll(target, info.Mode().Perm())
		}
		if info.Mode()&os.ModeSymlink != 0 {
			return fmt.Errorf("symlink %q is not supported", rel)
		}
		in, err := os.Open(path)
		if err != nil {
			return err
		}
		copyErr := copyFile(target, in, info.Mode().Perm())
		closeErr := in.Close()
		if copyErr != nil {
			return copyErr
		}
		return closeErr
	})
	if err != nil {
		_ = os.RemoveAll(dst)
		return err
	}
	return os.RemoveAll(src)
}
