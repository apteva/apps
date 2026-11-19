package main

// Branch deletion for Git-backed repositories: the local branch, the branch
// on origin, or both. A branch whose commits are not contained in the
// checked-out branch or origin's default branch is kept unless force is set,
// and the result always returns the deleted tips so they can be recreated.
// Origin's default branch and the checked-out branch are never deleted, and
// the remote deletion is leased on the tip that was checked: if someone
// pushes to the branch in between, nothing is deleted.

import (
	"errors"
	"fmt"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// GitBranchDeleteInput selects what repos_git_branch_delete removes.
type GitBranchDeleteInput struct {
	Name  string
	Where string // "local" (default), "remote" or "both"
	Force bool
	Actor string
}

// GitDeletedBranch is one deleted ref. MergedInto names the branch that
// still contains its commits; empty means force deleted unmerged work.
type GitDeletedBranch struct {
	SHA        string `json:"sha"`
	MergedInto string `json:"merged_into,omitempty"`
}

type GitBranchDeleteResult struct {
	Name   string            `json:"name"`
	Where  string            `json:"where"`
	Local  *GitDeletedBranch `json:"local,omitempty"`
	Remote *GitDeletedBranch `json:"remote,omitempty"`
	Status *GitStatus        `json:"status"`
}

var errGitBranchLeaseLost = errors.New("origin's branch changed after it was checked; nothing was deleted, list branches and try again")

// remoteBranchSHA returns the tip of refs/heads/name on the remote, or "".
func (g *gitService) remoteBranchSHA(workTree, gitDir, remoteName, name string, auth *gitAuth) (string, error) {
	out, err := g.engine.run(g.context(), workTree, gitDir, auth, "ls-remote", "--heads", remoteName, "refs/heads/"+name)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) == 2 && fields[1] == "refs/heads/"+name {
			return fields[0], nil
		}
	}
	return "", nil
}

// mergedInto returns the label of the first base that contains sha.
func (g *gitService) mergedInto(workTree, gitDir, sha string, bases [][2]string) string {
	if g.engine.refCommit(g.context(), workTree, gitDir, sha) == "" {
		return "" // never fetched: nothing local contains it
	}
	for _, base := range bases {
		if base[1] == "" {
			continue
		}
		if _, err := g.engine.run(g.context(), workTree, gitDir, nil, "merge-base", "--is-ancestor", sha, base[1]); err == nil {
			return base[0]
		}
	}
	return ""
}

func (s *gitService) DeleteBranch(ctx *sdk.AppCtx, repo *Repo, in GitBranchDeleteInput) (*GitBranchDeleteResult, error) {
	name := strings.TrimSpace(in.Name)
	where := strings.TrimSpace(in.Where)
	if where == "" {
		where = "local"
	}
	if where != "local" && where != "remote" && where != "both" {
		return nil, errors.New("where must be local, remote or both")
	}
	if err := s.engine.validBranchName(s.context(), name); err != nil {
		return nil, err
	}
	defer s.locks.lock(repoStoreKey(repo))()
	workTree, gitDir, err := s.paths(repo)
	if err != nil {
		return nil, err
	}
	status, err := s.engine.status(s.context(), workTree, gitDir)
	if err != nil {
		return nil, err
	}
	result := &GitBranchDeleteResult{Name: name, Where: where}
	wantLocal, wantRemote := where != "remote", where != "local"

	localSHA := ""
	if wantLocal {
		if status.Branch == name {
			return nil, fmt.Errorf("cannot delete the checked-out branch %s; switch to another branch first", name)
		}
		localSHA = s.engine.refCommit(s.context(), workTree, gitDir, "refs/heads/"+name)
		if localSHA == "" && where == "local" {
			return nil, fmt.Errorf("local branch %s does not exist", name)
		}
	}

	var remote *GitRemote
	var pushAuth *gitAuth
	remoteSHA, defaultBranch := "", ""
	if wantRemote || wantLocal {
		remote, _ = dbGetGitRemote(ctx.AppDB(), repo.ID, "origin")
	}
	if remote != nil {
		defaultBranch = remote.DefaultBranch
	}
	if wantRemote {
		if remote == nil {
			return nil, errors.New("origin remote is not configured")
		}
		fetchAuth, err := gitAuthForRemote(ctx, remote.FetchURL, remote.ConnectionID)
		if err != nil {
			return nil, err
		}
		if defaultBranch == "" {
			defaultBranch, _ = s.engine.remoteDefaultBranch(s.context(), remote.FetchURL, fetchAuth)
		}
		if name == defaultBranch || (defaultBranch == "" && (name == "main" || name == "master")) {
			return nil, fmt.Errorf("%s is origin's default branch and cannot be deleted", name)
		}
		remoteSHA, err = s.remoteBranchSHA(workTree, gitDir, remote.Name, name, fetchAuth)
		if err != nil {
			dbMarkGitRemoteResult(ctx.AppDB(), repo.ID, remote.Name, "branch_delete", err)
			return nil, err
		}
		if remoteSHA == "" && where == "remote" {
			return nil, fmt.Errorf("origin has no branch %s", name)
		}
		if remoteSHA != "" {
			pushAuth, err = gitAuthForRemote(ctx, firstNonEmpty(remote.PushURL, remote.FetchURL), remote.ConnectionID)
			if err != nil {
				return nil, err
			}
		}
	}
	if localSHA == "" && remoteSHA == "" {
		return nil, fmt.Errorf("branch %s exists neither locally nor on origin", name)
	}

	// Decide everything before deleting anything.
	bases := [][2]string{}
	if status.Branch != "" {
		bases = append(bases, [2]string{status.Branch, status.HeadSHA})
	}
	if remote != nil && defaultBranch != "" && defaultBranch != status.Branch {
		ref := remote.Name + "/" + defaultBranch
		bases = append(bases, [2]string{ref, s.engine.refCommit(s.context(), workTree, gitDir, "refs/remotes/"+ref)})
	}
	unmerged := []string{}
	if localSHA != "" {
		localBases := bases
		if remote != nil && remoteSHA == "" && !wantRemote {
			// Deleting only the local copy is safe when origin still has the commits.
			ref := remote.Name + "/" + name
			localBases = append(append([][2]string{}, bases...), [2]string{ref, s.engine.refCommit(s.context(), workTree, gitDir, "refs/remotes/"+ref)})
		}
		result.Local = &GitDeletedBranch{SHA: localSHA, MergedInto: s.mergedInto(workTree, gitDir, localSHA, localBases)}
		if result.Local.MergedInto == "" {
			unmerged = append(unmerged, "the local branch")
		}
	}
	if remoteSHA != "" {
		result.Remote = &GitDeletedBranch{SHA: remoteSHA, MergedInto: s.mergedInto(workTree, gitDir, remoteSHA, bases)}
		if result.Remote.MergedInto == "" {
			unmerged = append(unmerged, "origin's branch")
		}
	}
	if len(unmerged) > 0 && !in.Force {
		into := []string{}
		for _, base := range bases {
			if base[1] != "" {
				into = append(into, base[0])
			}
		}
		target := "any other branch"
		if len(into) > 0 {
			target = strings.Join(into, " or ")
		}
		verb := "has"
		if len(unmerged) > 1 {
			verb = "have"
		}
		return nil, fmt.Errorf("%s of %s %s commits not merged into %s; pass force=true to delete it anyway (the result returns the tip so it can be recreated)",
			strings.Join(unmerged, " and "), name, verb, target)
	}

	// Remote first: if origin refuses, the local branch is untouched.
	if remoteSHA != "" {
		_, opErr := s.engine.run(s.context(), workTree, gitDir, pushAuth, "push",
			"--force-with-lease=refs/heads/"+name+":"+remoteSHA, remote.Name, ":refs/heads/"+name)
		if opErr != nil && (strings.Contains(opErr.Error(), "stale info") || strings.Contains(opErr.Error(), "[rejected]")) {
			opErr = errGitBranchLeaseLost
		}
		dbMarkGitRemoteResult(ctx.AppDB(), repo.ID, remote.Name, "branch_delete", opErr)
		if opErr != nil {
			dbRecordGitOperation(ctx.AppDB(), repo.ID, "branch_delete", in.Actor, status.HeadSHA, status.HeadSHA, opErr)
			return nil, opErr
		}
		_, _ = s.engine.run(s.context(), workTree, gitDir, nil, "update-ref", "-d", "refs/remotes/"+remote.Name+"/"+name)
	}
	if localSHA != "" {
		if _, opErr := s.engine.run(s.context(), workTree, gitDir, nil, "branch", "-D", name); opErr != nil {
			dbRecordGitOperation(ctx.AppDB(), repo.ID, "branch_delete", in.Actor, status.HeadSHA, status.HeadSHA, opErr)
			return nil, opErr
		}
	}
	dbRecordGitOperation(ctx.AppDB(), repo.ID, "branch_delete", in.Actor, status.HeadSHA, status.HeadSHA, nil)

	result.Status, err = s.engine.status(s.context(), workTree, gitDir)
	if err != nil {
		return nil, err
	}
	result.Status.Remote, _ = dbGetGitRemote(ctx.AppDB(), repo.ID, "origin")
	event := map[string]any{"id": repo.ID, "slug": repo.Slug, "branch": name, "where": where}
	if result.Local != nil {
		event["local_sha"] = result.Local.SHA
	}
	if result.Remote != nil {
		event["remote_sha"] = result.Remote.SHA
	}
	ctx.Emit("repo.git.branch_deleted", event)
	return result, nil
}
