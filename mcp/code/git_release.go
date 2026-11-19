package main

// Release plumbing for Git-backed repositories (Code 0.19): annotated tags,
// pushing a local branch to a differently named remote branch (fast-forward
// only), and the pieces shared with pull requests. Nothing here can move or
// delete anything on the remote except by a fast-forward push or by adding a
// new tag: force pushes, tag moves and remote tag deletion are not exposed.

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

// GitTag is one tag as listed by repos_git_tag.
type GitTag struct {
	Name      string `json:"name"`
	SHA       string `json:"sha"` // the commit the tag points at
	Annotated bool   `json:"annotated"`
	Message   string `json:"message,omitempty"`
	Tagger    string `json:"tagger,omitempty"`
	CreatedAt string `json:"created_at,omitempty"`
	// OnRemote is set only when the caller asked to compare with origin:
	// "same", "different" (origin has the name on another commit) or "absent".
	OnRemote string `json:"on_remote,omitempty"`
}

// GitPushInfo describes what a push sent; it rides on GitStatus.
type GitPushInfo struct {
	Source       string `json:"source"`
	RemoteBranch string `json:"remote_branch"`
	SHA          string `json:"sha"`
}

var (
	errGitNonFastForward = errors.New("the remote branch has commits that the pushed branch does not contain; fetch, integrate them and push again (force push is not available)")
	errGitTagExists      = errors.New("a tag with this name already exists on origin; tags are never moved, choose a new version")
)

func (g *gitEngine) validTagName(ctx context.Context, name string) error {
	name = strings.TrimSpace(name)
	if name == "" {
		return errors.New("tag name required")
	}
	if strings.HasPrefix(name, "-") {
		return errors.New("invalid tag name")
	}
	if _, err := g.run(ctx, "", "", nil, "check-ref-format", "refs/tags/"+name); err != nil {
		return errors.New("invalid tag name")
	}
	return nil
}

func (g *gitEngine) validBranchName(ctx context.Context, name string) error {
	if strings.TrimSpace(name) == "" || strings.HasPrefix(name, "-") {
		return errors.New("invalid branch name")
	}
	if _, err := g.run(ctx, "", "", nil, "check-ref-format", "--branch", name); err != nil {
		return errors.New("invalid branch name")
	}
	return nil
}

// refCommit resolves ref to a commit SHA, or "" when it does not exist.
func (g *gitEngine) refCommit(ctx context.Context, workTree, gitDir, ref string) string {
	out, err := g.run(ctx, workTree, gitDir, nil, "rev-parse", "-q", "--verify", "--end-of-options", ref+"^{commit}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

func (g *gitEngine) createTag(ctx context.Context, workTree, gitDir, name, target, message, taggerName, taggerEmail string) (*GitTag, error) {
	if err := g.validTagName(ctx, name); err != nil {
		return nil, err
	}
	if strings.TrimSpace(target) == "" {
		target = "HEAD"
	}
	if strings.HasPrefix(target, "-") {
		return nil, errors.New("invalid tag target")
	}
	if g.refCommit(ctx, workTree, gitDir, "refs/tags/"+name) != "" {
		return nil, fmt.Errorf("tag %s already exists", name)
	}
	sha := g.refCommit(ctx, workTree, gitDir, target)
	if sha == "" {
		return nil, fmt.Errorf("tag target %q is not a commit", target)
	}
	if strings.TrimSpace(message) == "" {
		message = name
	}
	if taggerName == "" {
		taggerName = "Apteva Code"
	}
	if taggerEmail == "" {
		taggerEmail = "code@apteva.local"
	}
	if _, err := g.run(ctx, workTree, gitDir, nil, "-c", "user.name="+taggerName, "-c", "user.email="+taggerEmail,
		"tag", "-a", "-m", message, "--end-of-options", name, sha); err != nil {
		return nil, err
	}
	tags, err := g.listTags(ctx, workTree, gitDir, name, 1)
	if err != nil || len(tags) == 0 {
		return &GitTag{Name: name, SHA: sha, Annotated: true, Message: message}, err
	}
	return &tags[0], nil
}

// listTags lists local tags, newest first. pattern narrows to one exact name
// when non-empty.
func (g *gitEngine) listTags(ctx context.Context, workTree, gitDir, pattern string, limit int) ([]GitTag, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	ref := "refs/tags"
	if pattern != "" {
		ref = "refs/tags/" + pattern
	}
	format := "%(refname:strip=2)%1f%(objecttype)%1f%(objectname)%1f%(*objectname)%1f%(contents:subject)%1f%(taggername)%1f%(creatordate:iso-strict)%1e"
	out, err := g.run(ctx, workTree, gitDir, nil, "for-each-ref", "--sort=-creatordate", "--count="+strconv.Itoa(limit), "--format="+format, ref)
	if err != nil {
		return nil, err
	}
	tags := []GitTag{}
	for _, record := range strings.Split(out, "\x1e") {
		parts := strings.Split(strings.TrimLeft(record, "\n"), "\x1f")
		if len(parts) != 7 || parts[0] == "" {
			continue
		}
		tag := GitTag{Name: parts[0], SHA: parts[2], Tagger: parts[5], CreatedAt: parts[6]}
		if parts[1] == "tag" {
			tag.Annotated = true
			tag.SHA = parts[3]
			tag.Message = parts[4]
		}
		tags = append(tags, tag)
	}
	return tags, nil
}

// remoteTags returns tag name -> commit SHA on the remote (peeled for
// annotated tags).
func (g *gitEngine) remoteTags(ctx context.Context, workTree, gitDir, remoteName string, auth *gitAuth) (map[string]string, error) {
	out, err := g.run(ctx, workTree, gitDir, auth, "ls-remote", "--tags", remoteName)
	if err != nil {
		return nil, err
	}
	tags := map[string]string{}
	peeled := map[string]bool{}
	for _, line := range strings.Split(out, "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || !strings.HasPrefix(fields[1], "refs/tags/") {
			continue
		}
		name := strings.TrimPrefix(fields[1], "refs/tags/")
		if strings.HasSuffix(name, "^{}") {
			name = strings.TrimSuffix(name, "^{}")
			tags[name], peeled[name] = fields[0], true
			continue
		}
		if !peeled[name] {
			tags[name] = fields[0]
		}
	}
	return tags, nil
}

func (g *gitEngine) pushTag(ctx context.Context, workTree, gitDir, remoteName, name string, auth *gitAuth) error {
	if err := g.validTagName(ctx, name); err != nil {
		return err
	}
	if g.refCommit(ctx, workTree, gitDir, "refs/tags/"+name) == "" {
		return fmt.Errorf("tag %s does not exist locally; create it first", name)
	}
	_, err := g.run(ctx, workTree, gitDir, auth, "push", remoteName, "refs/tags/"+name+":refs/tags/"+name)
	if err != nil && isTagRejection(err) {
		return errGitTagExists
	}
	return err
}

func (g *gitEngine) deleteTag(ctx context.Context, workTree, gitDir, name string) error {
	if err := g.validTagName(ctx, name); err != nil {
		return err
	}
	if g.refCommit(ctx, workTree, gitDir, "refs/tags/"+name) == "" {
		return fmt.Errorf("tag %s does not exist locally", name)
	}
	_, err := g.run(ctx, workTree, gitDir, nil, "tag", "-d", "--end-of-options", name)
	return err
}

// pushBranch pushes local branch source (HEAD when empty) to
// refs/heads/remoteBranch without a "+" refspec, so Git itself refuses
// anything but a fast-forward or a new branch.
func (g *gitEngine) pushBranch(ctx context.Context, workTree, gitDir, remoteName, source, remoteBranch string, setUpstream bool, auth *gitAuth) error {
	if remoteName == "" {
		remoteName = "origin"
	}
	if err := g.validBranchName(ctx, remoteBranch); err != nil {
		return fmt.Errorf("remote_branch: %w", err)
	}
	src := "HEAD"
	if source != "" {
		src = "refs/heads/" + source
	}
	args := []string{"push"}
	if setUpstream {
		args = append(args, "--set-upstream")
	}
	args = append(args, remoteName, src+":refs/heads/"+remoteBranch)
	_, err := g.run(ctx, workTree, gitDir, auth, args...)
	if err != nil && isNonFastForward(err) {
		return errGitNonFastForward
	}
	return err
}

func (g *gitEngine) setRemoteTracking(ctx context.Context, workTree, gitDir, remoteName, branch, sha string) {
	if remoteName == "" {
		remoteName = "origin"
	}
	_, _ = g.run(ctx, workTree, gitDir, nil, "update-ref", "refs/remotes/"+remoteName+"/"+branch, sha)
}

func isNonFastForward(err error) bool {
	text := err.Error()
	return strings.Contains(text, "non-fast-forward") || strings.Contains(text, "fetch first") ||
		(strings.Contains(text, "[rejected]") && !strings.Contains(text, "already exists"))
}

func isTagRejection(err error) bool {
	text := err.Error()
	return strings.Contains(text, "already exists") || strings.Contains(text, "would clobber existing tag") ||
		(strings.Contains(text, "[rejected]") && strings.Contains(text, "refs/tags/"))
}

// GitPushInput selects what repos_git_push sends. Empty Source pushes the
// checked-out branch; empty RemoteBranch keeps the source branch's name.
type GitPushInput struct {
	Actor        string
	SetUpstream  bool
	Source       string
	RemoteBranch string
}

func (s *gitService) PushBranch(ctx *sdk.AppCtx, repo *Repo, in GitPushInput) (*GitStatus, error) {
	var pushed *GitPushInfo
	status, err := s.networkOperation(ctx, repo, "push", in.Actor, func(workTree, gitDir string, remote *GitRemote, auth *gitAuth) error {
		status, err := s.engine.status(s.context(), workTree, gitDir)
		if err != nil {
			return err
		}
		if status.Conflicted {
			return errors.New("repository has unresolved conflicts")
		}
		source := strings.TrimSpace(in.Source)
		if source == "" {
			if status.Branch == "" {
				return errors.New("cannot push a detached HEAD; pass source")
			}
			source = status.Branch
		} else if err := s.engine.validBranchName(s.context(), source); err != nil {
			return fmt.Errorf("source: %w", err)
		}
		sha := s.engine.refCommit(s.context(), workTree, gitDir, "refs/heads/"+source)
		if sha == "" {
			return fmt.Errorf("local branch %s does not exist", source)
		}
		remoteBranch := strings.TrimSpace(in.RemoteBranch)
		if remoteBranch == "" {
			remoteBranch = source
		}
		pushURL := firstNonEmpty(remote.PushURL, remote.FetchURL)
		pushAuth, err := gitAuthForRemote(ctx, pushURL, remote.ConnectionID)
		if err != nil {
			return err
		}
		upstream := source == status.Branch && remoteBranch == source && (in.SetUpstream || status.Upstream == "")
		if err := s.engine.pushBranch(s.context(), workTree, gitDir, remote.Name, source, remoteBranch, upstream, pushAuth); err != nil {
			return err
		}
		s.engine.setRemoteTracking(s.context(), workTree, gitDir, remote.Name, remoteBranch, sha)
		pushed = &GitPushInfo{Source: source, RemoteBranch: remoteBranch, SHA: sha}
		return nil
	})
	if err != nil {
		return nil, err
	}
	status.Pushed = pushed
	return status, nil
}

// GitTagResult is the response of every repos_git_tag action.
type GitTagResult struct {
	Action  string   `json:"action"`
	Tag     *GitTag  `json:"tag,omitempty"`
	Tags    []GitTag `json:"tags,omitempty"`
	Count   int      `json:"count,omitempty"`
	Deleted bool     `json:"deleted,omitempty"`
	Pushed  bool     `json:"pushed,omitempty"`
}

func (s *gitService) CreateTag(ctx *sdk.AppCtx, repo *Repo, name, target, message, actor string) (*GitTagResult, error) {
	defer s.locks.lock(repoStoreKey(repo))()
	workTree, gitDir, err := s.paths(repo)
	if err != nil {
		return nil, err
	}
	head := s.engine.head(s.context(), workTree, gitDir)
	tag, opErr := s.engine.createTag(s.context(), workTree, gitDir, strings.TrimSpace(name), target, message, "", "")
	dbRecordGitOperation(ctx.AppDB(), repo.ID, "tag_create", actor, head, head, opErr)
	if opErr != nil {
		return nil, opErr
	}
	ctx.Emit("repo.git.tagged", map[string]any{"id": repo.ID, "slug": repo.Slug, "tag": tag.Name, "sha": tag.SHA})
	return &GitTagResult{Action: "create", Tag: tag}, nil
}

func (s *gitService) ListTags(ctx *sdk.AppCtx, repo *Repo, limit int, compareRemote bool) (*GitTagResult, error) {
	defer s.locks.rlock(repoStoreKey(repo))()
	workTree, gitDir, err := s.paths(repo)
	if err != nil {
		return nil, err
	}
	tags, err := s.engine.listTags(s.context(), workTree, gitDir, "", limit)
	if err != nil {
		return nil, err
	}
	if compareRemote && len(tags) > 0 {
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
		published, err := s.engine.remoteTags(s.context(), workTree, gitDir, remote.Name, auth)
		if err != nil {
			return nil, err
		}
		for i := range tags {
			switch sha, ok := published[tags[i].Name]; {
			case !ok:
				tags[i].OnRemote = "absent"
			case sha == tags[i].SHA:
				tags[i].OnRemote = "same"
			default:
				tags[i].OnRemote = "different"
			}
		}
	}
	return &GitTagResult{Action: "list", Tags: tags, Count: len(tags)}, nil
}

func (s *gitService) PushTag(ctx *sdk.AppCtx, repo *Repo, name, actor string) (*GitTagResult, error) {
	name = strings.TrimSpace(name)
	var tag *GitTag
	_, err := s.networkOperation(ctx, repo, "tag_push", actor, func(workTree, gitDir string, remote *GitRemote, auth *gitAuth) error {
		tags, err := s.engine.listTags(s.context(), workTree, gitDir, name, 1)
		if err != nil {
			return err
		}
		if len(tags) == 0 || tags[0].Name != name {
			return fmt.Errorf("tag %s does not exist locally; create it first", name)
		}
		tag = &tags[0]
		pushAuth, err := gitAuthForRemote(ctx, firstNonEmpty(remote.PushURL, remote.FetchURL), remote.ConnectionID)
		if err != nil {
			return err
		}
		return s.engine.pushTag(s.context(), workTree, gitDir, remote.Name, name, pushAuth)
	})
	if err != nil {
		return nil, err
	}
	return &GitTagResult{Action: "push", Tag: tag, Pushed: true}, nil
}

func (s *gitService) DeleteTag(ctx *sdk.AppCtx, repo *Repo, name, actor string) (*GitTagResult, error) {
	defer s.locks.lock(repoStoreKey(repo))()
	workTree, gitDir, err := s.paths(repo)
	if err != nil {
		return nil, err
	}
	head := s.engine.head(s.context(), workTree, gitDir)
	opErr := s.engine.deleteTag(s.context(), workTree, gitDir, strings.TrimSpace(name))
	dbRecordGitOperation(ctx.AppDB(), repo.ID, "tag_delete", actor, head, head, opErr)
	if opErr != nil {
		return nil, opErr
	}
	return &GitTagResult{Action: "delete", Tag: &GitTag{Name: strings.TrimSpace(name)}, Deleted: true}, nil
}
