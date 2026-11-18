package main

// Branch protection and working-tree-free merges (Code 0.17).
//
// Protection lives in protection.json next to the native objects, so it is
// read under the repository lock and leaves with the repository. A protected
// branch refuses checkpoints, revision-recording restores, merges started on
// it, undo and deletion; it only moves when a change request is merged
// (mergeRefs). Internal writers such as Git auto-sync pass AllowProtected.

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type nativeProtection struct {
	RequiredApprovals int    `json:"required_approvals"`
	By                string `json:"by,omitempty"`
	At                string `json:"at"`
}

func (n *nativeVCS) protectionPath(repo *Repo) string {
	return filepath.Join(n.repoDir(repo), "protection.json")
}

func (n *nativeVCS) loadProtectionLocked(repo *Repo) (map[string]*nativeProtection, error) {
	body, err := os.ReadFile(n.protectionPath(repo))
	if errors.Is(err, os.ErrNotExist) {
		return map[string]*nativeProtection{}, nil
	}
	if err != nil {
		return nil, err
	}
	out := map[string]*nativeProtection{}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("branch protection settings are damaged: %w", err)
	}
	return out, nil
}

func (n *nativeVCS) guardProtectedLocked(repo *Repo, branch string) error {
	prot, err := n.loadProtectionLocked(repo)
	if err != nil {
		return err
	}
	if prot[branch] != nil {
		return fmt.Errorf("%w: %s only changes through a merged change request; work on another branch (repos_branch action=create switch=true) and open one with change_requests", errNativeProtected, branch)
	}
	return nil
}

func (n *nativeVCS) protection(repo *Repo, branch string) (*nativeProtection, error) {
	if err := n.ensureRepo(repo); err != nil {
		return nil, err
	}
	defer n.rlock(repo)()
	prot, err := n.loadProtectionLocked(repo)
	if err != nil {
		return nil, err
	}
	return prot[branch], nil
}

// setProtection protects (approvals >= 0) or unprotects (approvals < 0) a
// branch.
func (n *nativeVCS) setProtection(repo *Repo, branch string, approvals int, actor string) (*nativeProtection, error) {
	if err := n.ensureRepo(repo); err != nil {
		return nil, err
	}
	if _, err := normaliseRefName(branch); err != nil {
		return nil, err
	}
	if approvals > 10 {
		return nil, errors.New("required_approvals must be between 0 and 10")
	}
	defer n.lock(repo)()
	if !fileExists(n.refPath(repo, branch, false)) {
		return nil, errors.New("branch not found")
	}
	prot, err := n.loadProtectionLocked(repo)
	if err != nil {
		return nil, err
	}
	var out *nativeProtection
	if approvals < 0 {
		delete(prot, branch)
	} else {
		out = &nativeProtection{RequiredApprovals: approvals, By: strings.TrimSpace(actor), At: n.timestamp()}
		prot[branch] = out
	}
	body, _ := json.Marshal(prot)
	return out, atomicWrite(n.protectionPath(repo), body, 0o600)
}

// createAndSwitch starts a work branch at the checked-out head and checks it
// out without touching the working tree, so unsaved changes carry over to it.
func (n *nativeVCS) createAndSwitch(repo *Repo, name, actor string) error {
	if err := n.ensureRepo(repo); err != nil {
		return err
	}
	if _, err := normaliseRefName(name); err != nil {
		return err
	}
	defer n.lock(repo)()
	if n.mergeInProgress(repo) {
		return errNativeMergeInProgress
	}
	current, err := n.currentBranch(repo)
	if err != nil {
		return err
	}
	head := n.readRef(repo, current, false)
	if head == "" {
		return fmt.Errorf("branch %s has no revisions yet", current)
	}
	if fileExists(n.refPath(repo, name, false)) {
		return errors.New("branch already exists")
	}
	entry := nativeRefLogEntry{Actor: strings.TrimSpace(actor), Action: "branch.create", Message: "Work branch from " + current}
	if err := n.moveRefLocked(repo, name, false, true, "", head, entry); err != nil {
		return err
	}
	return atomicWrite(n.headPath(repo), []byte(name+"\n"), 0o600)
}

// commitTreeLocked records a revision for an explicit set of entries (every
// blob already stored) without reading the working tree.
func (n *nativeVCS) commitTreeLocked(repo *Repo, entries map[string]nativeEntry, parents []string, message, actor string) (*nativeRevision, error) {
	snap := &nativeSnapshot{Entries: entries, Tree: flatTreeHash(entries)}
	flat := filepath.Join(n.revisionsDir(repo), "tree-"+snap.Tree+".json")
	if !fileExists(flat) {
		body, err := json.Marshal(entries)
		if err != nil {
			return nil, err
		}
		if err := atomicWrite(flat, body, 0o600); err != nil {
			return nil, err
		}
	}
	root, err := n.writeFolderTrees(repo, entries)
	if err != nil {
		return nil, err
	}
	snap.Root = root
	return n.writeRevisionLocked(repo, snap, parents, message, actor)
}

type nativeRefMerge struct {
	Status    string          `json:"status"` // merged, up_to_date, conflicts
	Base      string          `json:"base,omitempty"`
	Target    string          `json:"target_revision"`
	Source    string          `json:"source_revision"`
	Revision  *nativeRevision `json:"revision,omitempty"`
	Conflicts []string        `json:"conflicts,omitempty"`
}

// mergeRefs merges source into target without a working tree (dryRun only
// reports whether it would succeed). mode is merge (always a two-parent
// revision) or squash. If target is checked out, its working tree must be
// clean and is updated to the result.
func (n *nativeVCS) mergeRefs(repo *Repo, target, source, mode, message, actor string, dryRun bool, expectSource string) (*nativeRefMerge, error) {
	if err := n.ensureRepo(repo); err != nil {
		return nil, err
	}
	if mode == "" {
		mode = "merge"
	}
	if mode != "merge" && mode != "squash" {
		return nil, fmt.Errorf("mode must be merge or squash (got %q)", mode)
	}
	defer n.lock(repo)() // even a dry run stores line-merged blobs
	ours := n.readRef(repo, target, false)
	theirs := n.readRef(repo, source, false)
	if ours == "" {
		return nil, fmt.Errorf("target branch %s not found", target)
	}
	if theirs == "" {
		return nil, fmt.Errorf("source branch %s not found", source)
	}
	if expectSource != "" && theirs != expectSource {
		return nil, fmt.Errorf("%w: source branch %s moved to %s since it was reviewed", errNativeRefMoved, source, shortID(theirs))
	}
	base, err := n.mergeBaseLocked(repo, ours, theirs)
	if err != nil {
		return nil, err
	}
	out := &nativeRefMerge{Base: base, Target: ours, Source: theirs}
	if base == theirs {
		out.Status = "up_to_date"
		return out, nil
	}
	baseT, err := n.entriesForLocked(repo, base)
	if err != nil {
		return nil, err
	}
	oursT, err := n.entriesForLocked(repo, ours)
	if err != nil {
		return nil, err
	}
	theirsT, err := n.entriesForLocked(repo, theirs)
	if err != nil {
		return nil, err
	}
	result, conflicts, _, err := n.threeWayLocked(repo, baseT, oursT, theirsT, merge3Labels{})
	if err != nil {
		return nil, err
	}
	if len(conflicts) > 0 {
		out.Status = "conflicts"
		for _, c := range conflicts {
			out.Conflicts = append(out.Conflicts, c.Path)
		}
		return out, nil
	}
	if dryRun {
		out.Status = "mergeable"
		return out, nil
	}
	current, err := n.currentBranch(repo)
	if err != nil {
		return nil, err
	}
	checkedOut := current == target
	if checkedOut {
		if n.mergeInProgress(repo) {
			return nil, errNativeMergeInProgress
		}
		status, err := n.statusLocked(repo)
		if err != nil {
			return nil, err
		}
		if status.Dirty {
			return nil, fmt.Errorf("%s is checked out with changes that are not in a revision; switch away or discard them before merging", target)
		}
	}
	parents := []string{ours}
	if mode == "merge" {
		parents = append(parents, theirs)
	}
	rev, err := n.commitTreeLocked(repo, result, parents, message, actor)
	if err != nil {
		return nil, err
	}
	if checkedOut {
		if err := n.applyEntriesLocked(repo, result, nil); err != nil {
			return nil, err
		}
	}
	entry := nativeRefLogEntry{Actor: strings.TrimSpace(actor), Action: "change_request." + mode, Message: firstLine(message)}
	if err := n.moveRefLocked(repo, target, false, true, ours, rev.ID, entry); err != nil {
		return nil, err
	}
	out.Status, out.Revision = "merged", rev
	return out, nil
}
