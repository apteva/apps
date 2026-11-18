package main

// Native merge engine (Code 0.16): merge, squash, cherry-pick and revert as
// three-way merges with structured conflicts; auto-stash on branch switch;
// branch delete and compare; line-level diff, per-path history and blame.
//
// A merge that cannot finish writes MERGE.json next to the repository's
// objects. While it exists only repos_merge may record a revision, and branch
// switches, undo and restore-with-revision are refused, so the conflicted
// working tree can never be captured by accident.

import (
	"container/heap"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

var nativeRevisionID = regexp.MustCompile(`^[0-9a-f]{64}$`)

// ─── Ref resolution and ancestry ────────────────────────────────────────

// resolveRefLocked accepts a revision ID, a branch name, a tag name, or ""
// / "HEAD" for the checked-out branch's head.
func (n *nativeVCS) resolveRefLocked(repo *Repo, ref string) (string, error) {
	ref = strings.TrimSpace(ref)
	if ref == "" || ref == "HEAD" {
		branch, err := n.currentBranch(repo)
		if err != nil {
			return "", err
		}
		head := n.readRef(repo, branch, false)
		if head == "" {
			return "", fmt.Errorf("branch %s has no revisions yet", branch)
		}
		return head, nil
	}
	if nativeRevisionID.MatchString(ref) {
		if _, err := n.loadRevision(repo, ref); err != nil {
			return "", fmt.Errorf("revision %s not found", ref)
		}
		return ref, nil
	}
	if _, err := normaliseRefName(ref); err != nil {
		return "", fmt.Errorf("%q is not a revision, branch or tag", ref)
	}
	if id := n.readRef(repo, ref, false); id != "" {
		return id, nil
	}
	if id := n.readRef(repo, ref, true); id != "" {
		return id, nil
	}
	return "", fmt.Errorf("%q is not a revision, branch or tag", ref)
}

const nativeMaxWalk = 100000

func (n *nativeVCS) ancestorsLocked(repo *Repo, id string) (map[string]bool, error) {
	seen := map[string]bool{}
	stack := nonEmpty(id)
	for len(stack) > 0 {
		cur := stack[len(stack)-1]
		stack = stack[:len(stack)-1]
		if seen[cur] {
			continue
		}
		if len(seen) >= nativeMaxWalk {
			return nil, errors.New("history too large to walk")
		}
		rev, err := n.loadRevision(repo, cur)
		if err != nil {
			return nil, err
		}
		seen[cur] = true
		stack = append(stack, rev.Parents...)
	}
	return seen, nil
}

// mergeBaseLocked returns the best common ancestor of a and b ("" when the
// histories are unrelated). Among several best candidates (criss-cross
// merges) the most recent one wins.
func (n *nativeVCS) mergeBaseLocked(repo *Repo, a, b string) (string, error) {
	ancA, err := n.ancestorsLocked(repo, a)
	if err != nil {
		return "", err
	}
	candidates := []string{}
	seen := map[string]bool{}
	queue := nonEmpty(b)
	for len(queue) > 0 {
		cur := queue[0]
		queue = queue[1:]
		if seen[cur] {
			continue
		}
		seen[cur] = true
		if ancA[cur] {
			candidates = append(candidates, cur)
			continue // its ancestors are dominated by it
		}
		rev, err := n.loadRevision(repo, cur)
		if err != nil {
			return "", err
		}
		queue = append(queue, rev.Parents...)
	}
	best := []*nativeRevision{}
	for _, c := range candidates {
		dominated := false
		for _, other := range candidates {
			if other == c {
				continue
			}
			anc, err := n.ancestorsLocked(repo, other)
			if err != nil {
				return "", err
			}
			if anc[c] {
				dominated = true
				break
			}
		}
		if !dominated {
			rev, err := n.loadRevision(repo, c)
			if err != nil {
				return "", err
			}
			best = append(best, rev)
		}
	}
	if len(best) == 0 {
		return "", nil
	}
	sort.Slice(best, func(i, j int) bool {
		if best[i].CreatedAt != best[j].CreatedAt {
			return best[i].CreatedAt > best[j].CreatedAt
		}
		return best[i].ID < best[j].ID
	})
	return best[0].ID, nil
}

func (n *nativeVCS) entriesForLocked(repo *Repo, id string) (map[string]nativeEntry, error) {
	if id == "" {
		return map[string]nativeEntry{}, nil
	}
	rev, err := n.loadRevision(repo, id)
	if err != nil {
		return nil, err
	}
	return n.revisionEntries(repo, rev)
}

func (n *nativeVCS) readBlob(repo *Repo, blob string) ([]byte, error) {
	return os.ReadFile(filepath.Join(n.blobsDir(repo), blob))
}

// ─── Three-way tree merge ───────────────────────────────────────────────

type nativeConflict struct {
	Path       string       `json:"path"`
	Reason     string       `json:"reason"` // content, add_add, modify_delete, binary, too_large
	Base       *nativeEntry `json:"base,omitempty"`
	Ours       *nativeEntry `json:"ours,omitempty"`
	Theirs     *nativeEntry `json:"theirs,omitempty"`
	Markers    string       `json:"markers_blob,omitempty"` // diff3-marked text, when available
	Resolved   bool         `json:"resolved"`
	Resolution string       `json:"resolution,omitempty"`
}

func entryPtr(e nativeEntry, ok bool) *nativeEntry {
	if !ok {
		return nil
	}
	return &e
}

func pickMode(base, ours, theirs uint32, hasBase bool) uint32 {
	if hasBase && base == ours {
		return theirs
	}
	return ours
}

// threeWayLocked merges theirs into ours against base. Clean results
// (including line-level merges, whose blobs it stores) are returned in
// result; conflicting paths keep ours in result and are listed in conflicts.
func (n *nativeVCS) threeWayLocked(repo *Repo, base, ours, theirs map[string]nativeEntry, labels merge3Labels) (map[string]nativeEntry, []nativeConflict, []string, error) {
	result := map[string]nativeEntry{}
	for p, e := range ours {
		result[p] = e
	}
	paths := map[string]bool{}
	for _, m := range []map[string]nativeEntry{base, ours, theirs} {
		for p := range m {
			paths[p] = true
		}
	}
	sorted := make([]string, 0, len(paths))
	for p := range paths {
		sorted = append(sorted, p)
	}
	sort.Strings(sorted)
	conflicts := []nativeConflict{}
	merged := []string{}
	for _, p := range sorted {
		b, hb := base[p]
		o, ho := ours[p]
		t, ht := theirs[p]
		switch {
		case ho == ht && o == t:
			continue
		case hb == ho && b == o:
			if ht {
				result[p] = t
			} else {
				delete(result, p)
			}
			continue
		case hb == ht && b == t:
			continue
		}
		c := nativeConflict{Path: p, Base: entryPtr(b, hb), Ours: entryPtr(o, ho), Theirs: entryPtr(t, ht)}
		if !ho || !ht {
			c.Reason = "modify_delete"
			conflicts = append(conflicts, c)
			continue
		}
		if o.Blob == t.Blob { // same content, only modes differ
			result[p] = nativeEntry{Blob: o.Blob, Mode: o.Mode}
			continue
		}
		var baseBody []byte
		if hb {
			body, err := n.readBlob(repo, b.Blob)
			if err != nil {
				return nil, nil, nil, err
			}
			baseBody = body
		}
		oursBody, err := n.readBlob(repo, o.Blob)
		if err != nil {
			return nil, nil, nil, err
		}
		theirsBody, err := n.readBlob(repo, t.Blob)
		if err != nil {
			return nil, nil, nil, err
		}
		if len(oursBody) > nativeMergeMaxBytes || len(theirsBody) > nativeMergeMaxBytes || len(baseBody) > nativeMergeMaxBytes {
			c.Reason = "too_large"
			conflicts = append(conflicts, c)
			continue
		}
		if !isText(oursBody) || !isText(theirsBody) || !isText(baseBody) {
			c.Reason = "binary"
			conflicts = append(conflicts, c)
			continue
		}
		res := merge3(splitLines(baseBody), splitLines(oursBody), splitLines(theirsBody), labels)
		body := []byte(strings.Join(res.Lines, ""))
		blob := hashBytes(body)
		if err := n.writeBlob(repo, blob, body); err != nil {
			return nil, nil, nil, err
		}
		if res.Conflicts == 0 {
			result[p] = nativeEntry{Blob: blob, Mode: pickMode(b.Mode, o.Mode, t.Mode, hb)}
			merged = append(merged, p)
			continue
		}
		c.Reason = "content"
		if !hb {
			c.Reason = "add_add"
		}
		c.Markers = blob
		conflicts = append(conflicts, c)
	}
	return result, conflicts, merged, nil
}

// ─── Merge state ────────────────────────────────────────────────────────

type nativeMergeState struct {
	Mode      string           `json:"mode"`
	Branch    string           `json:"branch"`
	Source    string           `json:"source"`
	Base      string           `json:"base,omitempty"`
	Ours      string           `json:"ours"`
	Theirs    string           `json:"theirs"`
	Message   string           `json:"message"`
	Actor     string           `json:"actor,omitempty"`
	StartedAt string           `json:"started_at"`
	Conflicts []nativeConflict `json:"conflicts"`
}

func (n *nativeVCS) mergePath(repo *Repo) string { return filepath.Join(n.repoDir(repo), "MERGE.json") }

func (n *nativeVCS) mergeInProgress(repo *Repo) bool { return fileExists(n.mergePath(repo)) }

func (n *nativeVCS) loadMergeLocked(repo *Repo) (*nativeMergeState, error) {
	body, err := os.ReadFile(n.mergePath(repo))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var st nativeMergeState
	if err := json.Unmarshal(body, &st); err != nil {
		return nil, fmt.Errorf("merge state is damaged: %w", err)
	}
	return &st, nil
}

func (n *nativeVCS) saveMergeLocked(repo *Repo, st *nativeMergeState) error {
	body, err := json.Marshal(st)
	if err != nil {
		return err
	}
	return atomicWrite(n.mergePath(repo), body, 0o600)
}

// ─── Merge operations ───────────────────────────────────────────────────

type nativeMergeOptions struct {
	Source  string
	Mode    string // merge (default), squash, cherry_pick, revert
	Message string
	Actor   string
	Markers bool // write diff3 markers into conflicted working files
	NoFF    bool // record a merge revision even when a fast-forward is possible
}

type nativeConflictView struct {
	Path       string  `json:"path"`
	Reason     string  `json:"reason"`
	Resolved   bool    `json:"resolved"`
	Resolution string  `json:"resolution,omitempty"`
	Base       *string `json:"base,omitempty"`
	Ours       *string `json:"ours,omitempty"`
	Theirs     *string `json:"theirs,omitempty"`
	Markers    *string `json:"merged_with_markers,omitempty"`
	Omitted    string  `json:"content_omitted,omitempty"`
}

type nativeMergeResult struct {
	Status      string               `json:"status"` // up_to_date, fast_forward, merged, no_changes, conflicts, in_progress, aborted
	Mode        string               `json:"mode,omitempty"`
	Branch      string               `json:"branch,omitempty"`
	Source      string               `json:"source,omitempty"`
	Base        string               `json:"base,omitempty"`
	Ours        string               `json:"ours,omitempty"`
	Theirs      string               `json:"theirs,omitempty"`
	Revision    *nativeRevision      `json:"revision,omitempty"`
	MergedLines []string             `json:"line_merged_paths,omitempty"`
	Conflicts   []nativeConflictView `json:"conflicts,omitempty"`
	Unresolved  int                  `json:"unresolved"`
	Next        string               `json:"next,omitempty"`
}

func (n *nativeVCS) conflictViewsLocked(repo *Repo, conflicts []nativeConflict, withContent bool) []nativeConflictView {
	out := make([]nativeConflictView, 0, len(conflicts))
	budget := 4 * nativeInlineMaxBytes
	inline := func(e *nativeEntry, view *nativeConflictView) *string {
		if e == nil || !withContent {
			return nil
		}
		body, err := n.readBlob(repo, e.Blob)
		if err != nil || !isText(body) {
			view.Omitted = "binary"
			return nil
		}
		if len(body) > nativeInlineMaxBytes || len(body) > budget {
			view.Omitted = "too_large; read the file or use repos_diff"
			return nil
		}
		budget -= len(body)
		s := string(body)
		return &s
	}
	for _, c := range conflicts {
		v := nativeConflictView{Path: c.Path, Reason: c.Reason, Resolved: c.Resolved, Resolution: c.Resolution}
		if !c.Resolved {
			v.Base = inline(c.Base, &v)
			v.Ours = inline(c.Ours, &v)
			v.Theirs = inline(c.Theirs, &v)
			if c.Markers != "" {
				v.Markers = inline(&nativeEntry{Blob: c.Markers}, &v)
			}
		}
		out = append(out, v)
	}
	return out
}

func unresolvedCount(cs []nativeConflict) int {
	count := 0
	for _, c := range cs {
		if !c.Resolved {
			count++
		}
	}
	return count
}

func shortID(id string) string {
	if len(id) > 12 {
		return id[:12]
	}
	return id
}

func (n *nativeVCS) mergeStart(repo *Repo, opts nativeMergeOptions) (*nativeMergeResult, error) {
	if err := n.ensureRepo(repo); err != nil {
		return nil, err
	}
	mode := strings.TrimSpace(opts.Mode)
	if mode == "" {
		mode = "merge"
	}
	switch mode {
	case "merge", "squash", "cherry_pick", "revert":
	default:
		return nil, fmt.Errorf("mode must be merge, squash, cherry_pick or revert (got %q)", mode)
	}
	if strings.TrimSpace(opts.Source) == "" {
		return nil, errors.New("start needs source: the branch, tag or revision to merge (or to cherry-pick / revert)")
	}
	defer n.lock(repo)()
	if n.mergeInProgress(repo) {
		return nil, errNativeMergeInProgress
	}
	branch, err := n.currentBranch(repo)
	if err != nil {
		return nil, err
	}
	ours := n.readRef(repo, branch, false)
	if ours == "" {
		return nil, fmt.Errorf("branch %s has no revisions yet; checkpoint first", branch)
	}
	status, err := n.statusLocked(repo)
	if err != nil {
		return nil, err
	}
	if status.Dirty {
		return nil, errors.New("working tree has changes that are not in a revision; checkpoint them before merging")
	}
	source, err := n.resolveRefLocked(repo, opts.Source)
	if err != nil {
		return nil, err
	}
	srcRev, err := n.loadRevision(repo, source)
	if err != nil {
		return nil, err
	}
	res := &nativeMergeResult{Mode: mode, Branch: branch, Source: strings.TrimSpace(opts.Source), Ours: ours}
	var base, theirs, message string
	switch mode {
	case "merge", "squash":
		theirs = source
		if base, err = n.mergeBaseLocked(repo, ours, theirs); err != nil {
			return nil, err
		}
		if base == theirs {
			res.Status, res.Base, res.Theirs = "up_to_date", base, theirs
			return res, nil
		}
		if mode == "merge" {
			message = fmt.Sprintf("Merge %s into %s", res.Source, branch)
		} else {
			message = fmt.Sprintf("Squash merge %s into %s", res.Source, branch)
		}
		if mode == "merge" && base == ours && !opts.NoFF {
			if err := n.restoreRevisionLocked(repo, theirs, nil); err != nil {
				return nil, err
			}
			entry := nativeRefLogEntry{Actor: strings.TrimSpace(opts.Actor), Action: "merge.fast_forward", Message: message}
			if err := n.moveRefLocked(repo, branch, false, true, ours, theirs, entry); err != nil {
				return nil, err
			}
			res.Status, res.Base, res.Theirs, res.Revision = "fast_forward", base, theirs, srcRev
			return res, nil
		}
	case "cherry_pick":
		if len(srcRev.Parents) > 0 {
			base = srcRev.Parents[0]
		}
		theirs = source
		message = fmt.Sprintf("%s\n\n(cherry picked from revision %s)", srcRev.Message, source)
	case "revert":
		base = source
		if len(srcRev.Parents) > 0 {
			theirs = srcRev.Parents[0]
		}
		message = fmt.Sprintf("Revert %q\n\nThis reverts revision %s.", srcRev.Message, source)
	}
	if m := strings.TrimSpace(opts.Message); m != "" {
		message = m
	}
	res.Base, res.Theirs = base, theirs
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
	labels := merge3Labels{Ours: branch, Base: "base " + shortID(base), Theirs: res.Source}
	result, conflicts, merged, err := n.threeWayLocked(repo, baseT, oursT, theirsT, labels)
	if err != nil {
		return nil, err
	}
	res.MergedLines = merged
	if opts.Markers {
		for _, c := range conflicts {
			if c.Markers != "" {
				result[c.Path] = nativeEntry{Blob: c.Markers, Mode: c.Ours.Mode}
			}
		}
	}
	if err := n.applyEntriesLocked(repo, result, nil); err != nil {
		return nil, err
	}
	if len(conflicts) > 0 {
		st := &nativeMergeState{Mode: mode, Branch: branch, Source: res.Source, Base: base, Ours: ours, Theirs: theirs, Message: message, Actor: strings.TrimSpace(opts.Actor), StartedAt: n.timestamp(), Conflicts: conflicts}
		if err := n.saveMergeLocked(repo, st); err != nil {
			return nil, err
		}
		res.Status = "conflicts"
		res.Conflicts = n.conflictViewsLocked(repo, conflicts, true)
		res.Unresolved = len(conflicts)
		res.Next = "resolve each path with action=resolve (choice ours|theirs|base|delete|working, or content), then action=continue; or action=abort"
		return res, nil
	}
	rev, err := n.finishMergeLocked(repo, mode, ours, theirs, message, opts.Actor)
	if err != nil {
		return nil, err
	}
	if rev == nil || rev.ID == ours {
		res.Status = "no_changes"
		return res, nil
	}
	res.Status, res.Revision = "merged", rev
	return res, nil
}

func (n *nativeVCS) finishMergeLocked(repo *Repo, mode, ours, theirs, message, actor string) (*nativeRevision, error) {
	opts := nativeCommit{Message: message, Actor: actor, ExpectedHead: ours, Action: mode, InMerge: true}
	if mode == "merge" {
		opts.ExtraParents = []string{theirs}
		opts.AllowEmpty = true
	}
	return n.commitLocked(repo, opts)
}

func (n *nativeVCS) mergeStatus(repo *Repo) (*nativeMergeResult, error) {
	if err := n.ensureRepo(repo); err != nil {
		return nil, err
	}
	defer n.rlock(repo)()
	st, err := n.loadMergeLocked(repo)
	if err != nil || st == nil {
		if st == nil && err == nil {
			return &nativeMergeResult{Status: "none"}, nil
		}
		return nil, err
	}
	return n.mergeView(repo, st, true), nil
}

func (n *nativeVCS) mergeView(repo *Repo, st *nativeMergeState, content bool) *nativeMergeResult {
	res := &nativeMergeResult{Status: "in_progress", Mode: st.Mode, Branch: st.Branch, Source: st.Source, Base: st.Base, Ours: st.Ours, Theirs: st.Theirs, Conflicts: n.conflictViewsLocked(repo, st.Conflicts, content), Unresolved: unresolvedCount(st.Conflicts)}
	if res.Unresolved == 0 {
		res.Next = "every conflict is resolved; call action=continue to record the result"
	} else {
		res.Next = "resolve each path with action=resolve, then action=continue; or action=abort"
	}
	return res
}

// mergeResolve settles one conflicted path. choice is ours, theirs, base,
// delete or working (keep the file as it is now in the working tree); content
// writes the given text instead.
func (n *nativeVCS) mergeResolve(repo *Repo, path, choice string, content *string) (*nativeMergeResult, error) {
	if err := n.ensureRepo(repo); err != nil {
		return nil, err
	}
	clean, err := normalisePath(path)
	if err != nil {
		return nil, fmt.Errorf("resolve needs path: %w", err)
	}
	choice = strings.TrimSpace(choice)
	if content != nil {
		if choice != "" && choice != "content" {
			return nil, errors.New("pass either choice or content, not both")
		}
		choice = "content"
	}
	defer n.lock(repo)()
	st, err := n.loadMergeLocked(repo)
	if err != nil {
		return nil, err
	}
	if st == nil {
		return nil, errors.New("no merge in progress")
	}
	idx := -1
	for i := range st.Conflicts {
		if st.Conflicts[i].Path == clean {
			idx = i
		}
	}
	if idx < 0 {
		return nil, fmt.Errorf("%s is not a conflicted path in this merge", clean)
	}
	c := &st.Conflicts[idx]
	mode := os.FileMode(0o644)
	for _, e := range []*nativeEntry{c.Ours, c.Theirs, c.Base} {
		if e != nil && e.Mode != 0 {
			mode = os.FileMode(e.Mode)
			break
		}
	}
	var change *fileMutation
	pick := func(e *nativeEntry) error {
		if e == nil {
			change = &fileMutation{Path: clean, Delete: true}
			return nil
		}
		body, err := n.readBlob(repo, e.Blob)
		if err != nil {
			return err
		}
		change = &fileMutation{Path: clean, Body: body, Mode: os.FileMode(e.Mode)}
		return nil
	}
	switch choice {
	case "ours":
		err = pick(c.Ours)
	case "theirs":
		err = pick(c.Theirs)
	case "base":
		err = pick(c.Base)
	case "delete":
		change = &fileMutation{Path: clean, Delete: true}
	case "working":
		if body, readErr := n.rawStore().Read(nativeStoreKey(repo), clean); readErr == nil && hasConflictMarkers(body) {
			return nil, fmt.Errorf("%s still contains conflict markers; edit them out before resolving", clean)
		}
	case "content":
		if hasConflictMarkers([]byte(*content)) {
			return nil, fmt.Errorf("content for %s still contains conflict markers", clean)
		}
		change = &fileMutation{Path: clean, Body: []byte(*content), Mode: mode}
	case "":
		return nil, errors.New("resolve needs choice (ours, theirs, base, delete, working) or content")
	default:
		return nil, fmt.Errorf("unknown choice %q: use ours, theirs, base, delete, working, or pass content", choice)
	}
	if err != nil {
		return nil, err
	}
	if change != nil {
		if change.Delete {
			if _, statErr := n.rawStore().Read(nativeStoreKey(repo), clean); statErr != nil {
				change = nil // already absent
			}
		}
		if change != nil {
			if err := applyFileMutations(n.rawStore(), nativeStoreKey(repo), []fileMutation{*change}); err != nil {
				return nil, err
			}
		}
	}
	c.Resolved, c.Resolution = true, choice
	if err := n.saveMergeLocked(repo, st); err != nil {
		return nil, err
	}
	return n.mergeView(repo, st, true), nil
}

// hasConflictMarkers reports a complete diff3 or merge marker block.
func hasConflictMarkers(body []byte) bool {
	start, mid := false, false
	for _, line := range splitLines(body) {
		line = strings.TrimRight(line, "\r\n")
		switch {
		case strings.HasPrefix(line, "<<<<<<<"):
			start, mid = true, false
		case start && line == "=======":
			mid = true
		case start && mid && strings.HasPrefix(line, ">>>>>>>"):
			return true
		}
	}
	return false
}

func (n *nativeVCS) mergeContinue(repo *Repo, message, actor string) (*nativeMergeResult, error) {
	if err := n.ensureRepo(repo); err != nil {
		return nil, err
	}
	defer n.lock(repo)()
	st, err := n.loadMergeLocked(repo)
	if err != nil {
		return nil, err
	}
	if st == nil {
		return nil, errors.New("no merge in progress")
	}
	if left := unresolvedCount(st.Conflicts); left > 0 {
		paths := []string{}
		for _, c := range st.Conflicts {
			if !c.Resolved {
				paths = append(paths, c.Path)
			}
		}
		return nil, fmt.Errorf("%d conflict(s) still unresolved: %s", left, strings.Join(paths, ", "))
	}
	if m := strings.TrimSpace(message); m != "" {
		st.Message = m
	}
	if strings.TrimSpace(actor) == "" {
		actor = st.Actor
	}
	rev, err := n.finishMergeLocked(repo, st.Mode, st.Ours, st.Theirs, st.Message, actor)
	if err != nil {
		return nil, err
	}
	if err := os.Remove(n.mergePath(repo)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	res := &nativeMergeResult{Mode: st.Mode, Branch: st.Branch, Source: st.Source, Base: st.Base, Ours: st.Ours, Theirs: st.Theirs, Status: "merged", Revision: rev}
	if rev == nil || rev.ID == st.Ours {
		res.Status, res.Revision = "no_changes", nil
	}
	return res, nil
}

func (n *nativeVCS) mergeAbort(repo *Repo) (*nativeMergeResult, error) {
	if err := n.ensureRepo(repo); err != nil {
		return nil, err
	}
	defer n.lock(repo)()
	st, err := n.loadMergeLocked(repo)
	if err != nil {
		return nil, err
	}
	if st == nil {
		return nil, errors.New("no merge in progress")
	}
	if err := n.restoreRevisionLocked(repo, st.Ours, nil); err != nil {
		return nil, err
	}
	if err := os.Remove(n.mergePath(repo)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	return &nativeMergeResult{Status: "aborted", Mode: st.Mode, Branch: st.Branch, Source: st.Source, Ours: st.Ours}, nil
}

// ─── Auto-stash on branch switch ────────────────────────────────────────

type nativeStash struct {
	Revision string `json:"revision"`
	Head     string `json:"head,omitempty"` // branch head the changes were made on
	At       string `json:"at"`
}

type nativeSwitchResult struct {
	Branch         string   `json:"branch"`
	Stashed        string   `json:"stashed_revision,omitempty"`
	StashRestored  bool     `json:"stash_restored"`
	StashConflicts []string `json:"stash_conflicts,omitempty"`
}

func (n *nativeVCS) stashPath(repo *Repo, branch string) string {
	return filepath.Join(n.repoDir(repo), "stash", branch+".json")
}

func (n *nativeVCS) loadStashLocked(repo *Repo, branch string) ([]nativeStash, error) {
	body, err := os.ReadFile(n.stashPath(repo, branch))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var list []nativeStash
	if err := json.Unmarshal(body, &list); err != nil {
		return nil, fmt.Errorf("stash for %s is damaged: %w", branch, err)
	}
	return list, nil
}

func (n *nativeVCS) saveStashLocked(repo *Repo, branch string, list []nativeStash) error {
	if len(list) == 0 {
		err := os.Remove(n.stashPath(repo, branch))
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if err := os.MkdirAll(filepath.Dir(n.stashPath(repo, branch)), 0o700); err != nil {
		return err
	}
	body, _ := json.Marshal(list)
	return atomicWrite(n.stashPath(repo, branch), body, 0o600)
}

func (n *nativeVCS) switchBranchLocked(repo *Repo, name, actor string) (*nativeSwitchResult, error) {
	if n.mergeInProgress(repo) {
		return nil, errNativeMergeInProgress
	}
	if !fileExists(n.refPath(repo, name, false)) {
		return nil, errors.New("branch not found")
	}
	current, err := n.currentBranch(repo)
	if err != nil {
		return nil, err
	}
	out := &nativeSwitchResult{Branch: name}
	if current == name {
		return out, nil
	}
	status, err := n.statusLocked(repo)
	if err != nil {
		return nil, err
	}
	if status.Dirty {
		snap, err := n.snapshotLocked(repo, true)
		if err != nil {
			return nil, err
		}
		rev, err := n.writeRevisionLocked(repo, snap, nonEmpty(status.Head), "Auto-stash on "+current, actor)
		if err != nil {
			return nil, err
		}
		list, err := n.loadStashLocked(repo, current)
		if err != nil {
			return nil, err
		}
		list = append(list, nativeStash{Revision: rev.ID, Head: status.Head, At: n.timestamp()})
		if err := n.saveStashLocked(repo, current, list); err != nil {
			return nil, err
		}
		out.Stashed = rev.ID
	}
	target := n.readRef(repo, name, false)
	if err := n.restoreRevisionLocked(repo, target, nil); err != nil {
		return nil, err
	}
	if err := atomicWrite(n.headPath(repo), []byte(name+"\n"), 0o600); err != nil {
		return nil, err
	}
	list, err := n.loadStashLocked(repo, name)
	if err != nil || len(list) == 0 {
		return out, err
	}
	top := list[len(list)-1]
	var apply map[string]nativeEntry
	if top.Head == target {
		if apply, err = n.entriesForLocked(repo, top.Revision); err != nil {
			return nil, err
		}
	} else {
		baseT, err := n.entriesForLocked(repo, top.Head)
		if err != nil {
			return nil, err
		}
		oursT, err := n.entriesForLocked(repo, target)
		if err != nil {
			return nil, err
		}
		theirsT, err := n.entriesForLocked(repo, top.Revision)
		if err != nil {
			return nil, err
		}
		result, conflicts, _, err := n.threeWayLocked(repo, baseT, oursT, theirsT, merge3Labels{})
		if err != nil {
			return nil, err
		}
		if len(conflicts) > 0 {
			for _, c := range conflicts {
				out.StashConflicts = append(out.StashConflicts, c.Path)
			}
			return out, nil // keep the stash; the branch moved underneath it
		}
		apply = result
	}
	if err := n.applyEntriesLocked(repo, apply, nil); err != nil {
		return nil, err
	}
	out.StashRestored = true
	return out, n.saveStashLocked(repo, name, list[:len(list)-1])
}

// extraRootsLocked lists revisions that must survive garbage collection
// besides refs and live ref logs: an in-progress merge, auto-stashes, and the
// logs of deleted branches (so a deleted branch can be recreated).
func (n *nativeVCS) extraRootsLocked(repo *Repo) ([]string, error) {
	out := []string{}
	st, err := n.loadMergeLocked(repo)
	if err != nil {
		return nil, err
	}
	if st != nil {
		out = append(out, st.Base, st.Ours, st.Theirs)
	}
	stashes, err := os.ReadDir(filepath.Join(n.repoDir(repo), "stash"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, e := range stashes {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") || strings.HasPrefix(e.Name(), ".tmp-") {
			continue
		}
		list, err := n.loadStashLocked(repo, strings.TrimSuffix(e.Name(), ".json"))
		if err != nil {
			return nil, err
		}
		for _, s := range list {
			out = append(out, s.Revision, s.Head)
		}
	}
	logs, err := os.ReadDir(filepath.Join(n.logsDir(repo), "heads"))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return nil, err
	}
	for _, e := range logs {
		name := strings.TrimSuffix(e.Name(), ".jsonl")
		if e.IsDir() || name == e.Name() || fileExists(n.refPath(repo, name, false)) {
			continue
		}
		log, err := n.readRefLog(repo, name, false)
		if err != nil {
			return nil, err
		}
		for _, entry := range log {
			out = append(out, entry.Old, entry.New)
		}
	}
	return out, nil
}

// ─── Branch list, create, switch, delete, compare ───────────────────────

// createBranchRef creates a branch at start (revision, branch or tag; default
// the checked-out head).
func (n *nativeVCS) createBranchRef(repo *Repo, name, start, actor string) error {
	if err := n.ensureRepo(repo); err != nil {
		return err
	}
	if _, err := normaliseRefName(name); err != nil {
		return err
	}
	defer n.lock(repo)()
	id, err := n.resolveRefLocked(repo, start)
	if err != nil {
		return err
	}
	if fileExists(n.refPath(repo, name, false)) {
		return errors.New("branch already exists")
	}
	return n.moveRefLocked(repo, name, false, true, "", id, nativeRefLogEntry{Actor: strings.TrimSpace(actor), Action: "branch.create"})
}

func (n *nativeVCS) switchBranchActor(repo *Repo, name, actor string) (*nativeSwitchResult, error) {
	if err := n.ensureRepo(repo); err != nil {
		return nil, err
	}
	if _, err := normaliseRefName(name); err != nil {
		return nil, err
	}
	defer n.lock(repo)()
	return n.switchBranchLocked(repo, name, actor)
}

// resolvePair resolves diff endpoints; to "" or "working" means the working
// tree and is returned as "".
func (n *nativeVCS) resolvePair(repo *Repo, from, to string) (string, string, error) {
	if err := n.ensureRepo(repo); err != nil {
		return "", "", err
	}
	defer n.rlock(repo)()
	a, err := n.resolveRefLocked(repo, from)
	if err != nil {
		return "", "", err
	}
	to = strings.TrimSpace(to)
	if to == "" || to == "working" || to == "working-tree" {
		return a, "", nil
	}
	b, err := n.resolveRefLocked(repo, to)
	return a, b, err
}

// historyRef walks first parents from ref (default: the checked-out branch).
func (n *nativeVCS) historyRef(repo *Repo, ref string, limit int) ([]nativeRevision, error) {
	if strings.TrimSpace(ref) == "" {
		return n.history(repo, limit)
	}
	if err := n.ensureRepo(repo); err != nil {
		return nil, err
	}
	defer n.rlock(repo)()
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	id, err := n.resolveRefLocked(repo, ref)
	if err != nil {
		return nil, err
	}
	out := []nativeRevision{}
	for id != "" && len(out) < limit {
		rev, err := n.loadRevision(repo, id)
		if err != nil {
			return nil, err
		}
		out = append(out, *rev)
		if len(rev.Parents) == 0 {
			break
		}
		id = rev.Parents[0]
	}
	return out, nil
}

// ─── Branch list, delete, compare ───────────────────────────────────────

type nativeBranchInfo struct {
	Name    string `json:"name"`
	Head    string `json:"head"`
	Current bool   `json:"current"`
	Stashes int    `json:"stashes,omitempty"`
}

func (n *nativeVCS) branchList(repo *Repo) ([]nativeBranchInfo, error) {
	if err := n.ensureRepo(repo); err != nil {
		return nil, err
	}
	defer n.rlock(repo)()
	refs, err := n.listRefs(repo, false)
	if err != nil {
		return nil, err
	}
	current, _ := n.currentBranch(repo)
	out := []nativeBranchInfo{}
	for name, head := range refs {
		list, _ := n.loadStashLocked(repo, name)
		out = append(out, nativeBranchInfo{Name: name, Head: head, Current: name == current, Stashes: len(list)})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out, nil
}

// deleteBranch removes a branch ref. Unless force, the branch must already be
// merged into the checked-out branch and carry no auto-stash. Its log is kept,
// so it can be recreated from repos_undo's history.
func (n *nativeVCS) deleteBranch(repo *Repo, name string, force bool, actor string) (string, error) {
	if err := n.ensureRepo(repo); err != nil {
		return "", err
	}
	if _, err := normaliseRefName(name); err != nil {
		return "", err
	}
	defer n.lock(repo)()
	head := n.readRef(repo, name, false)
	if head == "" {
		return "", errors.New("branch not found")
	}
	current, err := n.currentBranch(repo)
	if err != nil {
		return "", err
	}
	if current == name {
		return "", errors.New("cannot delete the checked-out branch; switch to another branch first")
	}
	if !force {
		anc, err := n.ancestorsLocked(repo, n.readRef(repo, current, false))
		if err != nil {
			return "", err
		}
		if !anc[head] {
			return "", fmt.Errorf("branch %s has revisions not merged into %s; merge it or pass force=true", name, current)
		}
		if list, _ := n.loadStashLocked(repo, name); len(list) > 0 {
			return "", fmt.Errorf("branch %s has an auto-stash of unsaved changes; switch to it to restore them, or pass force=true", name)
		}
	}
	if err := os.Remove(n.refPath(repo, name, false)); err != nil {
		return "", err
	}
	entry := nativeRefLogEntry{Old: head, New: "", Actor: strings.TrimSpace(actor), Action: "branch.delete", At: n.timestamp()}
	if err := appendJSONLine(n.logPath(repo, name, false), entry); err != nil {
		return "", err
	}
	return head, nil
}

type nativeCompare struct {
	From          string   `json:"from"`
	To            string   `json:"to"`
	FromRevision  string   `json:"from_revision"`
	ToRevision    string   `json:"to_revision"`
	MergeBase     string   `json:"merge_base,omitempty"`
	Ahead         int      `json:"ahead"`  // revisions in from that to lacks
	Behind        int      `json:"behind"` // revisions in to that from lacks
	FastForward   bool     `json:"fast_forward_possible"`
	ChangedPaths  []string `json:"changed_paths"`
	ChangedCount  int      `json:"changed_count"`
	AheadMessages []string `json:"ahead_messages,omitempty"`
}

// compareRefs reports how from relates to to (default: the checked-out
// branch): ahead/behind counts and the paths from changed since the base.
func (n *nativeVCS) compareRefs(repo *Repo, from, to string) (*nativeCompare, error) {
	if err := n.ensureRepo(repo); err != nil {
		return nil, err
	}
	defer n.rlock(repo)()
	a, err := n.resolveRefLocked(repo, from)
	if err != nil {
		return nil, err
	}
	b, err := n.resolveRefLocked(repo, to)
	if err != nil {
		return nil, err
	}
	if strings.TrimSpace(to) == "" {
		to, _ = n.currentBranch(repo)
	}
	ancA, err := n.ancestorsLocked(repo, a)
	if err != nil {
		return nil, err
	}
	ancB, err := n.ancestorsLocked(repo, b)
	if err != nil {
		return nil, err
	}
	base, err := n.mergeBaseLocked(repo, a, b)
	if err != nil {
		return nil, err
	}
	out := &nativeCompare{From: from, To: to, FromRevision: a, ToRevision: b, MergeBase: base, ChangedPaths: []string{}}
	aheadRevs := []*nativeRevision{}
	for id := range ancA {
		if !ancB[id] {
			out.Ahead++
			if rev, err := n.loadRevision(repo, id); err == nil {
				aheadRevs = append(aheadRevs, rev)
			}
		}
	}
	for id := range ancB {
		if !ancA[id] {
			out.Behind++
		}
	}
	sort.Slice(aheadRevs, func(i, j int) bool { return aheadRevs[i].CreatedAt > aheadRevs[j].CreatedAt })
	for i, rev := range aheadRevs {
		if i >= 20 {
			break
		}
		out.AheadMessages = append(out.AheadMessages, rev.Message)
	}
	out.FastForward = base == b
	baseT, err := n.entriesForLocked(repo, base)
	if err != nil {
		return nil, err
	}
	aT, err := n.entriesForLocked(repo, a)
	if err != nil {
		return nil, err
	}
	for p, e := range aT {
		if other, ok := baseT[p]; !ok || other != e {
			out.ChangedPaths = append(out.ChangedPaths, p)
		}
	}
	for p := range baseT {
		if _, ok := aT[p]; !ok {
			out.ChangedPaths = append(out.ChangedPaths, p)
		}
	}
	sort.Strings(out.ChangedPaths)
	out.ChangedCount = len(out.ChangedPaths)
	return out, nil
}

// ─── Line diff ──────────────────────────────────────────────────────────

type nativeFileDiff struct {
	Path    string `json:"path"`
	Status  string `json:"status"` // added, deleted, modified
	Added   int    `json:"added"`
	Removed int    `json:"removed"`
	Binary  bool   `json:"binary,omitempty"`
	Patch   string `json:"patch,omitempty"`
	Omitted string `json:"omitted,omitempty"`
}

const (
	nativeDiffMaxFiles     = 100
	nativeDiffMaxPatchSize = 512 << 10
)

// lineDiff resolves from/to (revision, branch, tag; "" for the checked-out
// head; to may be "working" for the working tree) and returns per-file
// unified patches.
func (n *nativeVCS) lineDiff(repo *Repo, from, to string, only []string) (string, string, []nativeFileDiff, error) {
	if err := n.ensureRepo(repo); err != nil {
		return "", "", nil, err
	}
	defer n.rlock(repo)()
	a, err := n.resolveRefLocked(repo, from)
	if err != nil {
		return "", "", nil, err
	}
	aT, err := n.entriesForLocked(repo, a)
	if err != nil {
		return "", "", nil, err
	}
	var bT map[string]nativeEntry
	b := strings.TrimSpace(to)
	readB := func(e nativeEntry) ([]byte, error) { return n.readBlob(repo, e.Blob) }
	if b == "" || b == "working" || b == "working-tree" {
		snap, err := n.snapshotLocked(repo, false)
		if err != nil {
			return "", "", nil, err
		}
		bT, b = snap.Entries, "working-tree"
		paths := map[string]string{}
		for p, e := range bT {
			paths[e.Blob] = p
		}
		readB = func(e nativeEntry) ([]byte, error) {
			if body, err := n.readBlob(repo, e.Blob); err == nil {
				return body, nil
			}
			return n.rawStore().Read(nativeStoreKey(repo), paths[e.Blob])
		}
	} else {
		if b, err = n.resolveRefLocked(repo, b); err != nil {
			return "", "", nil, err
		}
		if bT, err = n.entriesForLocked(repo, b); err != nil {
			return "", "", nil, err
		}
	}
	filter := map[string]bool{}
	for _, p := range only {
		if clean, err := normalisePath(p); err == nil {
			filter[clean] = true
		}
	}
	matches := func(p string) bool {
		if len(filter) == 0 {
			return true
		}
		for f := range filter {
			if p == f || strings.HasPrefix(p, f+"/") {
				return true
			}
		}
		return false
	}
	changed := []string{}
	for p, e := range aT {
		if other, ok := bT[p]; (!ok || other != e) && matches(p) {
			changed = append(changed, p)
		}
	}
	for p := range bT {
		if _, ok := aT[p]; !ok && matches(p) {
			changed = append(changed, p)
		}
	}
	sort.Strings(changed)
	out := []nativeFileDiff{}
	budget := nativeDiffMaxPatchSize
	for i, p := range changed {
		ea, hasA := aT[p]
		eb, hasB := bT[p]
		d := nativeFileDiff{Path: p, Status: "modified"}
		if !hasA {
			d.Status = "added"
		} else if !hasB {
			d.Status = "deleted"
		}
		if i >= nativeDiffMaxFiles {
			d.Omitted = "file limit reached"
			out = append(out, d)
			continue
		}
		var bodyA, bodyB []byte
		if hasA {
			if bodyA, err = n.readBlob(repo, ea.Blob); err != nil {
				return "", "", nil, err
			}
		}
		if hasB {
			if bodyB, err = readB(eb); err != nil {
				return "", "", nil, err
			}
		}
		if !isText(bodyA) || !isText(bodyB) {
			d.Binary = true
			out = append(out, d)
			continue
		}
		if len(bodyA) > nativeMergeMaxBytes || len(bodyB) > nativeMergeMaxBytes {
			d.Omitted = "file too large for a line diff"
			out = append(out, d)
			continue
		}
		patch, stats := unifiedDiff(p, splitLines(bodyA), splitLines(bodyB))
		d.Added, d.Removed = stats.Added, stats.Removed
		if len(patch) > budget {
			d.Omitted = "patch size limit reached"
		} else {
			d.Patch = patch
			budget -= len(patch)
		}
		out = append(out, d)
	}
	return a, b, out, nil
}

// ─── Path history and blame ─────────────────────────────────────────────

type revQueue []*nativeRevision

func (q revQueue) Len() int { return len(q) }
func (q revQueue) Less(i, j int) bool {
	if q[i].CreatedAt != q[j].CreatedAt {
		return q[i].CreatedAt > q[j].CreatedAt
	}
	return q[i].ID > q[j].ID
}
func (q revQueue) Swap(i, j int) { q[i], q[j] = q[j], q[i] }
func (q *revQueue) Push(x any)   { *q = append(*q, x.(*nativeRevision)) }
func (q *revQueue) Pop() any {
	old := *q
	item := old[len(old)-1]
	*q = old[:len(old)-1]
	return item
}

// pathKey identifies a file or folder's content in a revision ("" = absent).
func pathKey(entries map[string]nativeEntry, path string) string {
	if e, ok := entries[path]; ok {
		return fmt.Sprintf("f:%s:%o", e.Blob, e.Mode)
	}
	prefix := path + "/"
	keys := []string{}
	for p, e := range entries {
		if strings.HasPrefix(p, prefix) {
			keys = append(keys, fmt.Sprintf("%s\x00%s\x00%o", p, e.Blob, e.Mode))
		}
	}
	if len(keys) == 0 {
		return ""
	}
	sort.Strings(keys)
	return "d:" + hashBytes([]byte(strings.Join(keys, "\n")))
}

// pathHistory lists revisions reachable from ref that changed path (a file or
// folder), newest first. A merge that kept one parent's version follows only
// that parent, as Git's default history simplification does.
func (n *nativeVCS) pathHistory(repo *Repo, ref, path string, limit int) (string, []nativeRevision, error) {
	if err := n.ensureRepo(repo); err != nil {
		return "", nil, err
	}
	clean, err := normalisePath(path)
	if err != nil {
		return "", nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	defer n.rlock(repo)()
	head, err := n.resolveRefLocked(repo, ref)
	if err != nil {
		return "", nil, err
	}
	keys := map[string]string{}
	keyOf := func(id string) (string, error) {
		if k, ok := keys[id]; ok {
			return k, nil
		}
		entries, err := n.entriesForLocked(repo, id)
		if err != nil {
			return "", err
		}
		keys[id] = pathKey(entries, clean)
		return keys[id], nil
	}
	start, err := n.loadRevision(repo, head)
	if err != nil {
		return "", nil, err
	}
	q := &revQueue{start}
	seen := map[string]bool{head: true}
	out := []nativeRevision{}
	walked := 0
	for q.Len() > 0 && len(out) < limit && walked < nativeMaxWalk {
		rev := heap.Pop(q).(*nativeRevision)
		walked++
		key, err := keyOf(rev.ID)
		if err != nil {
			return "", nil, err
		}
		follow := rev.Parents
		same := false
		for _, p := range rev.Parents {
			pk, err := keyOf(p)
			if err != nil {
				return "", nil, err
			}
			if pk == key {
				follow, same = []string{p}, true
				break
			}
		}
		if !same && (len(rev.Parents) > 0 || key != "") {
			out = append(out, *rev)
		}
		for _, p := range follow {
			if seen[p] {
				continue
			}
			seen[p] = true
			prev, err := n.loadRevision(repo, p)
			if err != nil {
				return "", nil, err
			}
			heap.Push(q, prev)
		}
	}
	return head, out, nil
}

type nativeBlameLine struct {
	Line     int    `json:"line"`
	Revision string `json:"revision"`
	Actor    string `json:"actor,omitempty"`
	At       string `json:"at"`
	Message  string `json:"message,omitempty"`
	Text     string `json:"text"`
}

const nativeBlameMaxRevisions = 2000

// blame attributes every line of path at ref to the revision that last
// changed it. Lines are traced through every parent of a merge, preferring
// the first parent that already had the line.
func (n *nativeVCS) blame(repo *Repo, ref, path string) (string, []nativeBlameLine, bool, error) {
	if err := n.ensureRepo(repo); err != nil {
		return "", nil, false, err
	}
	clean, err := normalisePath(path)
	if err != nil {
		return "", nil, false, err
	}
	defer n.rlock(repo)()
	head, err := n.resolveRefLocked(repo, ref)
	if err != nil {
		return "", nil, false, err
	}
	fileAt := func(id string) ([]string, bool, error) {
		entries, err := n.entriesForLocked(repo, id)
		if err != nil {
			return nil, false, err
		}
		e, ok := entries[clean]
		if !ok {
			return nil, false, nil
		}
		body, err := n.readBlob(repo, e.Blob)
		if err != nil {
			return nil, false, err
		}
		if !isText(body) {
			return nil, true, errors.New("blame works on text files only")
		}
		if len(body) > nativeMergeMaxBytes {
			return nil, true, errors.New("file too large to blame")
		}
		return splitLines(body), true, nil
	}
	lines, ok, err := fileAt(head)
	if err != nil {
		return "", nil, false, err
	}
	if !ok {
		return "", nil, false, fmt.Errorf("%s does not exist at %s", clean, shortID(head))
	}
	owner := make([]string, len(lines))
	// pending[rev] maps a line index in rev's version of the file to the
	// original line indexes it stands for.
	pending := map[string]map[int][]int{head: {}}
	for i := range lines {
		pending[head][i] = []int{i}
	}
	revs := map[string]*nativeRevision{}
	files := map[string][]string{head: lines}
	start, err := n.loadRevision(repo, head)
	if err != nil {
		return "", nil, false, err
	}
	revs[head] = start
	q := &revQueue{start}
	processed := 0
	truncated := false
	for q.Len() > 0 {
		rev := heap.Pop(q).(*nativeRevision)
		todo := pending[rev.ID]
		delete(pending, rev.ID)
		if len(todo) == 0 {
			continue
		}
		processed++
		if processed > nativeBlameMaxRevisions {
			truncated = true
			for _, origs := range todo {
				for _, o := range origs {
					owner[o] = rev.ID
				}
			}
			continue
		}
		cur := files[rev.ID]
		for _, p := range rev.Parents {
			if len(todo) == 0 {
				break
			}
			plines, exists, err := fileAt(p)
			if err != nil {
				return "", nil, false, err
			}
			if !exists {
				continue
			}
			passed := map[int][]int{}
			for _, m := range diffMatches(plines, cur) {
				if origs, ok := todo[m[1]]; ok {
					passed[m[0]] = append(passed[m[0]], origs...)
					delete(todo, m[1])
				}
			}
			if len(passed) == 0 {
				continue
			}
			if pending[p] == nil {
				pending[p] = map[int][]int{}
				prev, err := n.loadRevision(repo, p)
				if err != nil {
					return "", nil, false, err
				}
				revs[p] = prev
				files[p] = plines
				heap.Push(q, prev)
			}
			for k, v := range passed {
				pending[p][k] = append(pending[p][k], v...)
			}
		}
		for _, origs := range todo {
			for _, o := range origs {
				owner[o] = rev.ID
			}
		}
	}
	out := make([]nativeBlameLine, len(lines))
	for i, l := range lines {
		r := revs[owner[i]]
		out[i] = nativeBlameLine{Line: i + 1, Revision: owner[i], Text: strings.TrimSuffix(l, "\n")}
		if r != nil {
			out[i].Actor, out[i].At, out[i].Message = r.Actor, r.CreatedAt, firstLine(r.Message)
		}
	}
	return head, out, truncated, nil
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
