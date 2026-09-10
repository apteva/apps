package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	sdk "github.com/apteva/app-sdk"
)

const autoSyncDebounce = 10 * time.Second
const autoSyncCheckpoint = time.Minute
const autoSyncFetchInterval = time.Minute

type AutoSyncState struct {
	RepoID       int64  `json:"repo_id"`
	Enabled      bool   `json:"enabled"`
	Branch       string `json:"branch"`
	RemoteBranch string `json:"remote_branch"`
	Status       string `json:"status"`
	Fingerprint  string `json:"-"`
	DirtySince   int64  `json:"-"`
	LastChangeAt int64  `json:"-"`
	LastCheckAt  int64  `json:"-"`
	LastSyncAt   int64  `json:"last_sync_at"`
	RetryAt      int64  `json:"retry_at"`
	Attempts     int    `json:"attempts"`
	LastError    string `json:"last_error,omitempty"`
}

const autoSyncColumns = `repo_id, enabled, branch, remote_branch, status, fingerprint, dirty_since, last_change_at, last_check_at, last_sync_at, retry_at, attempts, last_error`

func loadAutoSync(db *sql.DB, id int64) (*AutoSyncState, error) {
	s := &AutoSyncState{RepoID: id, Status: "paused"}
	err := db.QueryRow("SELECT "+autoSyncColumns+" FROM repo_auto_sync WHERE repo_id=?", id).Scan(&s.RepoID, &s.Enabled, &s.Branch, &s.RemoteBranch, &s.Status, &s.Fingerprint, &s.DirtySince, &s.LastChangeAt, &s.LastCheckAt, &s.LastSyncAt, &s.RetryAt, &s.Attempts, &s.LastError)
	if errors.Is(err, sql.ErrNoRows) {
		return s, nil
	}
	return s, err
}
func saveAutoSync(db *sql.DB, s *AutoSyncState) error {
	_, err := db.Exec(`INSERT INTO repo_auto_sync (`+autoSyncColumns+`) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(repo_id) DO UPDATE SET
 enabled=excluded.enabled,branch=excluded.branch,remote_branch=excluded.remote_branch,status=excluded.status,fingerprint=excluded.fingerprint,dirty_since=excluded.dirty_since,last_change_at=excluded.last_change_at,last_check_at=excluded.last_check_at,last_sync_at=excluded.last_sync_at,retry_at=excluded.retry_at,attempts=excluded.attempts,last_error=excluded.last_error`, s.RepoID, s.Enabled, s.Branch, s.RemoteBranch, s.Status, s.Fingerprint, s.DirtySince, s.LastChangeAt, s.LastCheckAt, s.LastSyncAt, s.RetryAt, s.Attempts, s.LastError)
	return err
}

// The supervisor polls Git as well as Code writes, so edits from dev processes
// are detected too. Each repository has at most one task and a shared file lock.
type autoSyncSupervisor struct {
	git     *gitService
	ctx     context.Context
	cancel  context.CancelFunc
	wg      sync.WaitGroup
	mu      sync.Mutex
	running map[int64]bool
	slots   chan struct{}
}

func newAutoSyncSupervisor(g *gitService) *autoSyncSupervisor {
	ctx, cancel := context.WithCancel(context.Background())
	return &autoSyncSupervisor{git: g, ctx: ctx, cancel: cancel, running: map[int64]bool{}, slots: make(chan struct{}, 4)}
}
func (s *autoSyncSupervisor) start(appCtx *sdk.AppCtx) {
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		ticker := time.NewTicker(2 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-s.ctx.Done():
				return
			case <-ticker.C:
				s.dispatch(appCtx)
			}
		}
	}()
}
func (s *autoSyncSupervisor) stop() { s.cancel(); s.wg.Wait() }
func (s *autoSyncSupervisor) dispatch(appCtx *sdk.AppCtx) {
	rows, err := appCtx.AppDB().Query(`SELECT r.id,r.project_id,r.slug FROM repositories r JOIN repo_auto_sync a ON a.repo_id=r.id WHERE a.enabled=1 AND r.archived_at IS NULL`)
	if err != nil {
		appCtx.Logger().Warn("auto-sync scan failed", "err", err)
		return
	}
	var repos []*Repo
	for rows.Next() {
		r := new(Repo)
		if err = rows.Scan(&r.ID, &r.ProjectID, &r.Slug); err != nil {
			break
		}
		repos = append(repos, r)
	}
	rows.Close()
	for _, r := range repos {
		s.mu.Lock()
		if s.running[r.ID] {
			s.mu.Unlock()
			continue
		}
		select {
		case s.slots <- struct{}{}:
		default:
			s.mu.Unlock()
			return
		}
		s.running[r.ID] = true
		s.mu.Unlock()
		s.wg.Add(1)
		go func(repo *Repo) {
			defer s.wg.Done()
			defer func() { <-s.slots; s.mu.Lock(); delete(s.running, repo.ID); s.mu.Unlock() }()
			if _, err := s.tick(appCtx.WithProject(repo.ProjectID), repo, time.Now(), false); err != nil && s.ctx.Err() == nil {
				appCtx.Logger().Warn("auto-sync failed", "repo", repo.ID, "err", err)
			}
		}(r)
	}
}
func (s *autoSyncSupervisor) configure(ctx *sdk.AppCtx, repo *Repo, enabled bool, expectedBranch ...string) (*AutoSyncState, error) {
	defer s.git.locks.lock(repoStoreKey(repo))()
	state, err := loadAutoSync(ctx.AppDB(), repo.ID)
	if err != nil {
		return nil, err
	}
	if enabled {
		current, err := dbGetRepoByID(ctx.AppDB(), repo.ProjectID, repo.ID)
		if err != nil {
			return nil, err
		}
		if current == nil || current.ArchivedAt != "" {
			return nil, errors.New("auto-sync requires an active repository")
		}
		work, gd, err := s.git.paths(repo)
		if err != nil {
			return nil, err
		}
		status, err := s.git.engine.status(s.ctx, work, gd)
		if err != nil {
			return nil, err
		}
		if len(expectedBranch) > 0 && expectedBranch[0] != "" && status.Branch != expectedBranch[0] {
			return nil, errors.New("branch changed since confirmation; refresh and enable again")
		}
		if status.Detached || status.Conflicted || !strings.HasPrefix(status.Upstream, "origin/") {
			return nil, errors.New("auto-sync requires a conflict-free branch tracking origin; connect and reconcile the repository first")
		}
		remote, err := dbGetGitRemote(ctx.AppDB(), repo.ID, "origin")
		if err != nil {
			return nil, err
		}
		if remote == nil {
			return nil, errors.New("origin remote is not configured")
		}
		state.Branch = status.Branch
		state.RemoteBranch = strings.TrimPrefix(status.Upstream, "origin/")
		state.Status = "pending"
		state.Fingerprint = ""
		state.DirtySince = 0
		state.LastChangeAt = 0
		state.LastCheckAt = 0
		state.RetryAt = 0
		state.Attempts = 0
		state.LastError = ""
	} else {
		state.Status = "paused"
	}
	state.Enabled = enabled
	return s.persist(ctx, repo, state)
}
func (s *autoSyncSupervisor) persist(ctx *sdk.AppCtx, repo *Repo, state *AutoSyncState) (*AutoSyncState, error) {
	if err := saveAutoSync(ctx.AppDB(), state); err != nil {
		return nil, err
	}
	ctx.Emit("repo.git.sync", map[string]any{"slug": repo.Slug, "repo_id": repo.ID, "auto_sync": state})
	return state, nil
}
func (s *autoSyncSupervisor) attention(ctx *sdk.AppCtx, repo *Repo, state *AutoSyncState, err error) (*AutoSyncState, error) {
	state.Status = "needs_attention"
	state.LastError = err.Error()
	state.RetryAt = 0
	return s.persist(ctx, repo, state)
}
func (s *autoSyncSupervisor) networkFailure(ctx *sdk.AppCtx, repo *Repo, state *AutoSyncState, now time.Time, err error) (*AutoSyncState, error) {
	msg := strings.ToLower(err.Error())
	for _, term := range []string{"authentication failed", "could not read username", "could not read password", "access denied", "permission denied", "repository not found", "403", "401", "not bound", "missing git credentials", "non-fast-forward", "fetch first", "[rejected]", "protected branch", "gh006", "gh013", "remote rejected"} {
		if strings.Contains(msg, term) {
			return s.attention(ctx, repo, state, err)
		}
	}
	state.Status = "offline"
	state.LastError = err.Error()
	state.Attempts++
	delay := 15 * time.Second
	for i := 1; i < state.Attempts && delay < 5*time.Minute; i++ {
		delay *= 2
	}
	if delay > 5*time.Minute {
		delay = 5 * time.Minute
	}
	state.RetryAt = now.Add(delay).UnixMilli()
	return s.persist(ctx, repo, state)
}
func autoSyncDue(state *AutoSyncState, now time.Time) bool {
	return state.DirtySince > 0 && (now.UnixMilli()-state.LastChangeAt >= autoSyncDebounce.Milliseconds() || now.UnixMilli()-state.DirtySince >= autoSyncCheckpoint.Milliseconds())
}

func (s *autoSyncSupervisor) tick(ctx *sdk.AppCtx, repo *Repo, now time.Time, force bool) (*AutoSyncState, error) {
	defer s.git.locks.lock(repoStoreKey(repo))()
	// Revalidate after acquiring the lock; a queued task must not revive an
	// archived/deleted repository or one that the user has paused.
	current, err := dbGetRepoByID(ctx.AppDB(), repo.ProjectID, repo.ID)
	if err != nil {
		return nil, err
	}
	if current == nil {
		return nil, errors.New("repository no longer exists")
	}
	state, err := loadAutoSync(ctx.AppDB(), repo.ID)
	if err != nil {
		return nil, err
	}
	if !state.Enabled || current.ArchivedAt != "" {
		if force {
			return nil, errors.New("enable auto-sync on an active repository first")
		}
		return state, nil
	}
	if state.Status == "needs_attention" && !force {
		return state, nil
	}
	work, gd, err := s.git.paths(repo)
	if err != nil {
		return s.attention(ctx, repo, state, err)
	}
	status, err := s.git.engine.status(s.ctx, work, gd)
	if err != nil {
		return s.attention(ctx, repo, state, err)
	}
	if status.Detached || status.Conflicted || status.Branch != state.Branch || status.Upstream != "origin/"+state.RemoteBranch {
		return s.attention(ctx, repo, state, errors.New("branch or upstream changed, or conflicts exist; resolve and re-enable auto-sync"))
	}
	fingerprint, err := s.fingerprint(work, gd, repoStoreKey(repo), status)
	if err != nil {
		return s.attention(ctx, repo, state, err)
	}
	changed := false
	if status.Dirty {
		if state.DirtySince == 0 {
			state.DirtySince = now.UnixMilli()
			changed = true
		}
		if fingerprint != state.Fingerprint {
			state.Fingerprint = fingerprint
			state.LastChangeAt = now.UnixMilli()
			changed = true
		}
		if state.Status != "offline" {
			state.Status = "pending"
		}
		if !force && !autoSyncDue(state, now) {
			if changed {
				return s.persist(ctx, repo, state)
			}
			return state, nil
		}
	} else {
		if state.DirtySince != 0 {
			state.DirtySince = 0
			state.LastChangeAt = 0
			state.Fingerprint = ""
			changed = true
		}
		if !force && (state.RetryAt > now.UnixMilli() || (state.RetryAt == 0 && now.UnixMilli()-state.LastCheckAt < autoSyncFetchInterval.Milliseconds())) {
			if changed {
				return s.persist(ctx, repo, state)
			}
			return state, nil
		}
	}
	before := status.HeadSHA
	if status.Dirty {
		message := fmt.Sprintf("Auto-save: update %d files", len(status.Changes))
		if len(status.Changes) == 1 {
			message = "Auto-save: update " + status.Changes[0].Path
		}
		_, err = s.git.engine.commit(s.ctx, work, gd, message, nil, "Apteva Code", "code@apteva.local")
		dbRecordGitOperation(ctx.AppDB(), repo.ID, "auto_commit", "auto-sync", before, s.git.engine.head(s.ctx, work, gd), err)
		if err != nil {
			return s.attention(ctx, repo, state, err)
		}
		state.DirtySince = 0
		state.LastChangeAt = 0
		state.Fingerprint = ""
	}
	// Continue local checkpoints during network backoff.
	if !force && state.RetryAt > now.UnixMilli() {
		return s.persist(ctx, repo, state)
	}
	state.Status = "syncing"
	state.LastCheckAt = now.UnixMilli()
	if _, err = s.persist(ctx, repo, state); err != nil {
		return nil, err
	}
	remote, err := dbGetGitRemote(ctx.AppDB(), repo.ID, "origin")
	if err != nil {
		return nil, err
	}
	if remote == nil {
		return s.attention(ctx, repo, state, errors.New("origin remote is not configured"))
	}
	auth, err := gitAuthForRemote(ctx, remote.FetchURL, remote.ConnectionID)
	if err != nil {
		return s.attention(ctx, repo, state, err)
	}
	err = s.git.engine.fetch(s.ctx, work, gd, remote.FetchURL, "origin", auth)
	if err != nil {
		return s.networkFailure(ctx, repo, state, now, err)
	}
	// Verify the exact pinned ref still exists; never recreate a deleted remote branch.
	target := "refs/remotes/origin/" + state.RemoteBranch
	if _, err = s.git.engine.run(s.ctx, work, gd, nil, "rev-parse", "--verify", target); err != nil {
		return s.attention(ctx, repo, state, errors.New("remote branch no longer exists"))
	}
	status, err = s.git.engine.status(s.ctx, work, gd)
	if err != nil {
		return s.attention(ctx, repo, state, err)
	}
	if status.Dirty {
		return s.attention(ctx, repo, state, errors.New("files changed during sync; retry after external writers finish"))
	}
	if status.Ahead > 0 && status.Behind > 0 {
		return s.attention(ctx, repo, state, errors.New("local and remote histories have diverged; reconcile them before retrying"))
	}
	if status.Behind > 0 {
		if err = s.git.engine.fastForward(s.ctx, work, gd, "origin", state.RemoteBranch); err != nil {
			return s.attention(ctx, repo, state, err)
		}
		ctx.Emit("repo.git.pulled", map[string]any{"id": repo.ID, "slug": repo.Slug, "branch": state.Branch, "head_sha": s.git.engine.head(s.ctx, work, gd)})
	}
	if status.Ahead > 0 {
		// Push only to the fetched origin URL, using an explicit refspec. Never force.
		pushURL := firstNonEmpty(remote.PushURL, remote.FetchURL)
		if pushURL != remote.FetchURL {
			return s.attention(ctx, repo, state, errors.New("auto-sync requires matching fetch and push URLs"))
		}
		_, err = s.git.engine.run(s.ctx, work, gd, auth, "push", pushURL, "HEAD:refs/heads/"+state.RemoteBranch)
		if err != nil {
			return s.networkFailure(ctx, repo, state, now, err)
		}
		s.git.engine.updateRemoteTracking(s.ctx, work, gd, "origin", state.RemoteBranch)
	}
	after := s.git.engine.head(s.ctx, work, gd)
	dbRecordGitOperation(ctx.AppDB(), repo.ID, "auto_sync", "auto-sync", before, after, nil)
	state.Status = "synced"
	state.LastSyncAt = now.UnixMilli()
	state.RetryAt = 0
	state.Attempts = 0
	state.LastError = ""
	return s.persist(ctx, repo, state)
}
func (s *autoSyncSupervisor) fingerprint(work, gd, key string, status *GitStatus) (string, error) {
	if !status.Dirty {
		return "", nil
	}
	// Hash contents as a stream rather than a bounded patch, including changes
	// made outside Code. Index hashes distinguish separately staged edits.
	index, err := s.git.engine.run(s.ctx, work, gd, nil, "diff", "--cached", "--raw", "--abbrev=40", "--no-ext-diff", "HEAD", "--")
	if err != nil {
		return "", err
	}
	h := sha256.New()
	h.Write([]byte(index))
	for _, change := range status.Changes {
		fmt.Fprintf(h, "%s\x00%s%s\x00", change.Path, change.Index, change.WorkTree)
		root, err := os.OpenRoot(s.git.store.RepoPath(key))
		if err != nil {
			return "", err
		}
		info, err := root.Lstat(change.Path)
		if os.IsNotExist(err) {
			root.Close()
			h.Write([]byte("deleted\x00"))
			continue
		}
		if err != nil {
			root.Close()
			return "", err
		}
		if info.Mode()&os.ModeSymlink != 0 {
			target, err := root.Readlink(change.Path)
			root.Close()
			if err != nil {
				return "", err
			}
			fmt.Fprintf(h, "symlink\x00%s\x00", target)
			continue
		}
		if !info.Mode().IsRegular() {
			root.Close()
			return "", fmt.Errorf("auto-sync cannot checkpoint non-regular file %q", change.Path)
		}
		f, err := root.Open(change.Path)
		root.Close()
		if err != nil {
			return "", err
		}
		info, err = f.Stat()
		if err == nil && !info.Mode().IsRegular() {
			err = fmt.Errorf("auto-sync cannot checkpoint non-regular file %q", change.Path)
		}
		if err == nil {
			fmt.Fprintf(h, "%o\x00", info.Mode().Perm())
			_, err = io.Copy(h, f)
		}
		f.Close()
		if err != nil {
			return "", err
		}
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}
