package main

// Tier 1 coverage for the Code 0.16 merge engine: line diff and diff3,
// merge / squash / cherry-pick / revert, structured conflicts and their
// resolution, auto-stash on branch switch, branch delete and compare, path
// history, blame, and garbage-collection roots.

import (
	"errors"
	"math/rand"
	"strings"
	"testing"
	"time"
)

func put(t *testing.T, store *LocalFileStore, repo *Repo, path, body string) {
	t.Helper()
	if _, err := store.Write(repoStoreKey(repo), path, []byte(body)); err != nil {
		t.Fatal(err)
	}
}

func lcsLen(a, b []string) int {
	dp := make([][]int, len(a)+1)
	for i := range dp {
		dp[i] = make([]int, len(b)+1)
	}
	for i := len(a) - 1; i >= 0; i-- {
		for j := len(b) - 1; j >= 0; j-- {
			if a[i] == b[j] {
				dp[i][j] = dp[i+1][j+1] + 1
			} else if dp[i+1][j] > dp[i][j+1] {
				dp[i][j] = dp[i+1][j]
			} else {
				dp[i][j] = dp[i][j+1]
			}
		}
	}
	return dp[0][0]
}

func TestNativeDiff_MatchesAreAnOptimalCommonSubsequence(t *testing.T) {
	rng := rand.New(rand.NewSource(7))
	alphabet := []string{"a\n", "b\n", "c\n", "d\n", "e"}
	gen := func() []string {
		out := make([]string, rng.Intn(30))
		for i := range out {
			out[i] = alphabet[rng.Intn(len(alphabet))]
		}
		return out
	}
	for iter := 0; iter < 2000; iter++ {
		a, b := gen(), gen()
		m := diffMatches(a, b)
		pi, pj := -1, -1
		for _, p := range m {
			if p[0] <= pi || p[1] <= pj || a[p[0]] != b[p[1]] {
				t.Fatalf("invalid match %v in %q / %q", p, a, b)
			}
			pi, pj = p[0], p[1]
		}
		if want := lcsLen(a, b); len(m) != want {
			t.Fatalf("matches %d, LCS %d for %q / %q", len(m), want, a, b)
		}
	}
}

func TestNativeDiff_UnifiedPatch(t *testing.T) {
	a := splitLines([]byte("one\ntwo\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\n"))
	b := splitLines([]byte("one\nTWO\nthree\nfour\nfive\nsix\nseven\neight\nnine\nten\neleven"))
	patch, stats := unifiedDiff("f.txt", a, b)
	want := "--- a/f.txt\n+++ b/f.txt\n@@ -1,5 +1,5 @@\n one\n-two\n+TWO\n three\n four\n five\n@@ -8,3 +8,4 @@\n eight\n nine\n ten\n+eleven\n\\ No newline at end of file\n"
	if patch != want {
		t.Fatalf("patch:\n%s\nwant:\n%s", patch, want)
	}
	if stats.Added != 2 || stats.Removed != 1 {
		t.Fatalf("stats %+v", stats)
	}
	if p, _ := unifiedDiff("f", a, a); p != "" {
		t.Fatalf("identical files produced a patch: %q", p)
	}
}

func TestNativeDiff_Merge3(t *testing.T) {
	base := splitLines([]byte("a\nb\nc\nd\ne\n"))
	ours := splitLines([]byte("A\nb\nc\nd\ne\n"))
	theirs := splitLines([]byte("a\nb\nc\nd\nE\nf"))
	res := merge3(base, ours, theirs, merge3Labels{})
	if res.Conflicts != 0 || strings.Join(res.Lines, "") != "A\nb\nc\nd\nE\nf" {
		t.Fatalf("clean merge: %d %q", res.Conflicts, strings.Join(res.Lines, ""))
	}
	res = merge3(splitLines([]byte("x\ny\nz\n")), splitLines([]byte("x\nours\nz\n")), splitLines([]byte("x\ntheirs\nz\n")), merge3Labels{Ours: "main", Base: "base", Theirs: "feature"})
	want := "x\n<<<<<<< main\nours\n||||||| base\ny\n=======\ntheirs\n>>>>>>> feature\nz\n"
	if res.Conflicts != 1 || strings.Join(res.Lines, "") != want {
		t.Fatalf("conflict: %d\n%s", res.Conflicts, strings.Join(res.Lines, ""))
	}
	if !hasConflictMarkers([]byte(want)) || hasConflictMarkers([]byte("<<<<<<< only an opener\n")) {
		t.Fatal("marker detection")
	}
	// Both sides making the same change is not a conflict.
	res = merge3(splitLines([]byte("1\n2\n")), splitLines([]byte("1\nsame\n")), splitLines([]byte("1\nsame\n")), merge3Labels{})
	if res.Conflicts != 0 || strings.Join(res.Lines, "") != "1\nsame\n" {
		t.Fatalf("identical change: %q", strings.Join(res.Lines, ""))
	}
	// Insertions at the same spot conflict; at different spots they merge.
	res = merge3(splitLines([]byte("1\n2\n3\n")), splitLines([]byte("0\n1\n2\n3\n")), splitLines([]byte("1\n2\n3\n4\n")), merge3Labels{})
	if res.Conflicts != 0 || strings.Join(res.Lines, "") != "0\n1\n2\n3\n4\n" {
		t.Fatalf("separate inserts: %q", strings.Join(res.Lines, ""))
	}
}

// branchFixture: main has a.txt and b.txt; feature branches from it.
func branchFixture(t *testing.T) (*nativeVCS, *Repo, *LocalFileStore, *nativeRevision) {
	t.Helper()
	n, repo, store := nativeFixture(t)
	put(t, store, repo, "a.txt", "line1\nline2\nline3\nline4\nline5\n")
	put(t, store, repo, "b.txt", "bee\n")
	base := mustCheckpoint(t, n, repo, "base")
	if err := n.createBranchRef(repo, "feature", "", "test"); err != nil {
		t.Fatal(err)
	}
	return n, repo, store, base
}

func switchTo(t *testing.T, n *nativeVCS, repo *Repo, name string) *nativeSwitchResult {
	t.Helper()
	res, err := n.switchBranch(repo, name)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestNativeMerge_FastForwardUpToDateAndNoFF(t *testing.T) {
	n, repo, store, base := branchFixture(t)
	switchTo(t, n, repo, "feature")
	put(t, store, repo, "c.txt", "new\n")
	feat := mustCheckpoint(t, n, repo, "feature work")
	switchTo(t, n, repo, "main")

	res, err := n.mergeStart(repo, nativeMergeOptions{Source: "feature", Actor: "test"})
	if err != nil || res.Status != "fast_forward" || n.readRef(repo, "main", false) != feat.ID {
		t.Fatalf("fast-forward: %+v %v", res, err)
	}
	if readWorking(t, store, repo, "c.txt") != "new\n" {
		t.Fatal("fast-forward did not update the working tree")
	}
	if res, _ = n.mergeStart(repo, nativeMergeOptions{Source: "feature"}); res.Status != "up_to_date" {
		t.Fatalf("second merge: %+v", res)
	}
	_, moves, _ := n.refMoves(repo, "main", 1)
	if moves[0].Action != "merge.fast_forward" {
		t.Fatalf("log action %q", moves[0].Action)
	}

	// no_ff records a merge revision with two parents even when FF is possible.
	if _, _, err := n.undo(repo, "main", base.ID, "test"); err != nil {
		t.Fatal(err)
	}
	res, err = n.mergeStart(repo, nativeMergeOptions{Source: "feature", NoFF: true})
	if err != nil || res.Status != "merged" || len(res.Revision.Parents) != 2 || res.Revision.Parents[1] != feat.ID {
		t.Fatalf("no_ff: %+v %v", res, err)
	}
}

func TestNativeMerge_ThreeWayLineMergeRecordsTwoParents(t *testing.T) {
	n, repo, store, _ := branchFixture(t)
	switchTo(t, n, repo, "feature")
	put(t, store, repo, "a.txt", "line1\nline2\nline3\nline4\nFEATURE5\n")
	put(t, store, repo, "f.txt", "feature only\n")
	feat := mustCheckpoint(t, n, repo, "feature edits")
	switchTo(t, n, repo, "main")
	put(t, store, repo, "a.txt", "MAIN1\nline2\nline3\nline4\nline5\n")
	put(t, store, repo, "b.txt", "bee main\n")
	ours := mustCheckpoint(t, n, repo, "main edits")

	res, err := n.mergeStart(repo, nativeMergeOptions{Source: "feature", Actor: "test"})
	if err != nil || res.Status != "merged" {
		t.Fatalf("merge: %+v %v", res, err)
	}
	if got := res.Revision.Parents; len(got) != 2 || got[0] != ours.ID || got[1] != feat.ID {
		t.Fatalf("parents %v", got)
	}
	if got := readWorking(t, store, repo, "a.txt"); got != "MAIN1\nline2\nline3\nline4\nFEATURE5\n" {
		t.Fatalf("line merge: %q", got)
	}
	if readWorking(t, store, repo, "b.txt") != "bee main\n" || readWorking(t, store, repo, "f.txt") != "feature only\n" {
		t.Fatal("file-level merge lost a side")
	}
	if len(res.MergedLines) != 1 || res.MergedLines[0] != "a.txt" {
		t.Fatalf("line-merged paths %v", res.MergedLines)
	}
	if st, _ := n.status(repo); st.Dirty || st.Head != res.Revision.ID {
		t.Fatalf("status after merge %+v", st)
	}
	cmp, err := n.compareRefs(repo, "feature", "main")
	if err != nil || cmp.Ahead != 0 || cmp.Behind != 2 || cmp.FastForward {
		t.Fatalf("compare after merge %+v %v", cmp, err)
	}
}

func conflictFixture(t *testing.T) (*nativeVCS, *Repo, *LocalFileStore, *nativeMergeResult) {
	t.Helper()
	n, repo, store, _ := branchFixture(t)
	switchTo(t, n, repo, "feature")
	put(t, store, repo, "a.txt", "line1\nFEATURE\nline3\nline4\nline5\n")
	put(t, store, repo, "b.txt", "bee feature\n")
	mustCheckpoint(t, n, repo, "feature")
	switchTo(t, n, repo, "main")
	put(t, store, repo, "a.txt", "line1\nMAIN\nline3\nline4\nline5\n")
	if err := store.Delete(repoStoreKey(repo), "b.txt"); err != nil {
		t.Fatal(err)
	}
	mustCheckpoint(t, n, repo, "main")
	res, err := n.mergeStart(repo, nativeMergeOptions{Source: "feature", Actor: "test"})
	if err != nil || res.Status != "conflicts" {
		t.Fatalf("expected conflicts: %+v %v", res, err)
	}
	return n, repo, store, res
}

func TestNativeMerge_ConflictsAreDataAndBlockOtherWrites(t *testing.T) {
	n, repo, store, res := conflictFixture(t)
	if len(res.Conflicts) != 2 || res.Unresolved != 2 {
		t.Fatalf("conflicts %+v", res.Conflicts)
	}
	byPath := map[string]nativeConflictView{}
	for _, c := range res.Conflicts {
		byPath[c.Path] = c
	}
	a := byPath["a.txt"]
	if a.Reason != "content" || a.Ours == nil || !strings.Contains(*a.Ours, "MAIN") || !strings.Contains(*a.Theirs, "FEATURE") || !strings.Contains(*a.Base, "line2") {
		t.Fatalf("a.txt conflict view %+v", a)
	}
	if a.Markers == nil || !strings.Contains(*a.Markers, "<<<<<<< main") {
		t.Fatalf("markers preview missing: %+v", a)
	}
	if b := byPath["b.txt"]; b.Reason != "modify_delete" || b.Ours != nil || b.Theirs == nil {
		t.Fatalf("b.txt conflict view %+v", b)
	}
	// Without markers=true the working file keeps our side.
	if got := readWorking(t, store, repo, "a.txt"); strings.Contains(got, "<<<<<<<") || !strings.Contains(got, "MAIN") {
		t.Fatalf("working a.txt %q", got)
	}
	if _, err := n.checkpoint(repo, "sneaky", "test"); !errors.Is(err, errNativeMergeInProgress) {
		t.Fatalf("checkpoint during merge: %v", err)
	}
	if _, err := n.switchBranch(repo, "feature"); !errors.Is(err, errNativeMergeInProgress) {
		t.Fatalf("switch during merge: %v", err)
	}
	if _, _, err := n.undo(repo, "main", "x", "test"); !errors.Is(err, errNativeMergeInProgress) {
		t.Fatalf("undo during merge: %v", err)
	}
	if _, err := n.restore(repo, n.readRef(repo, "main", false), nil, false, "t"); !errors.Is(err, errNativeMergeInProgress) {
		t.Fatalf("restore during merge: %v", err)
	}
	if _, err := n.mergeStart(repo, nativeMergeOptions{Source: "feature"}); !errors.Is(err, errNativeMergeInProgress) {
		t.Fatalf("second start: %v", err)
	}
	if _, err := n.mergeContinue(repo, "", "t"); err == nil || !strings.Contains(err.Error(), "unresolved") {
		t.Fatalf("continue with unresolved: %v", err)
	}
	st, err := n.mergeStatus(repo)
	if err != nil || st.Status != "in_progress" || st.Unresolved != 2 {
		t.Fatalf("status %+v %v", st, err)
	}
}

func TestNativeMerge_ResolveContinueRecordsMerge(t *testing.T) {
	n, repo, store, res := conflictFixture(t)
	if _, err := n.mergeResolve(repo, "a.txt", "", nil); err == nil {
		t.Fatal("resolve without choice accepted")
	}
	if _, err := n.mergeResolve(repo, "nope.txt", "ours", nil); err == nil {
		t.Fatal("resolve of a non-conflicted path accepted")
	}
	bad := "<<<<<<< x\na\n=======\nb\n>>>>>>> y\n"
	if _, err := n.mergeResolve(repo, "a.txt", "", &bad); err == nil {
		t.Fatal("content with markers accepted")
	}
	// Edit the file directly, then resolve with "working".
	put(t, store, repo, "a.txt", "line1\nMAIN+FEATURE\nline3\nline4\nline5\n")
	if _, err := n.mergeResolve(repo, "a.txt", "working", nil); err != nil {
		t.Fatal(err)
	}
	st, err := n.mergeResolve(repo, "b.txt", "theirs", nil)
	if err != nil || st.Unresolved != 0 {
		t.Fatalf("resolve b: %+v %v", st, err)
	}
	if readWorking(t, store, repo, "b.txt") != "bee feature\n" {
		t.Fatal("theirs not written")
	}
	done, err := n.mergeContinue(repo, "Merge feature by hand", "test")
	if err != nil || done.Status != "merged" || len(done.Revision.Parents) != 2 || done.Revision.Parents[0] != res.Ours || done.Revision.Parents[1] != res.Theirs {
		t.Fatalf("continue %+v %v", done, err)
	}
	if n.mergeInProgress(repo) {
		t.Fatal("merge state left behind")
	}
	if s, _ := n.status(repo); s.Dirty || s.Head != done.Revision.ID {
		t.Fatalf("status %+v", s)
	}
	mustCheckpoint(t, n, repo, "checkpoints work again")
}

func TestNativeMerge_ResolveChoicesAndAbort(t *testing.T) {
	n, repo, store, res := conflictFixture(t)
	content := "resolved by content\n"
	if _, err := n.mergeResolve(repo, "a.txt", "", &content); err != nil {
		t.Fatal(err)
	}
	if readWorking(t, store, repo, "a.txt") != content {
		t.Fatal("content not written")
	}
	if _, err := n.mergeResolve(repo, "b.txt", "delete", nil); err != nil {
		t.Fatal(err)
	}
	// Changing your mind is allowed before continue.
	if _, err := n.mergeResolve(repo, "a.txt", "base", nil); err != nil {
		t.Fatal(err)
	}
	if readWorking(t, store, repo, "a.txt") != "line1\nline2\nline3\nline4\nline5\n" {
		t.Fatal("base not written")
	}
	ab, err := n.mergeAbort(repo)
	if err != nil || ab.Status != "aborted" {
		t.Fatalf("abort %+v %v", ab, err)
	}
	if n.mergeInProgress(repo) || n.readRef(repo, "main", false) != res.Ours {
		t.Fatal("abort left state or moved the branch")
	}
	if s, _ := n.status(repo); s.Dirty {
		t.Fatal("abort did not restore the working tree")
	}
	if got := readWorking(t, store, repo, "a.txt"); !strings.Contains(got, "MAIN") {
		t.Fatalf("a.txt after abort %q", got)
	}
}

func TestNativeMerge_MarkersOptionWritesMarkers(t *testing.T) {
	n, repo, store, _ := branchFixture(t)
	switchTo(t, n, repo, "feature")
	put(t, store, repo, "a.txt", "line1\nFEATURE\nline3\nline4\nline5\n")
	mustCheckpoint(t, n, repo, "feature")
	switchTo(t, n, repo, "main")
	put(t, store, repo, "a.txt", "line1\nMAIN\nline3\nline4\nline5\n")
	mustCheckpoint(t, n, repo, "main")
	if _, err := n.mergeStart(repo, nativeMergeOptions{Source: "feature", Markers: true}); err != nil {
		t.Fatal(err)
	}
	got := readWorking(t, store, repo, "a.txt")
	if !strings.Contains(got, "<<<<<<< main") || !strings.Contains(got, ">>>>>>> feature") {
		t.Fatalf("markers missing: %q", got)
	}
	if _, err := n.mergeResolve(repo, "a.txt", "working", nil); err == nil {
		t.Fatal("working file with markers accepted")
	}
}

func TestNativeMerge_BinaryAndAddAddConflicts(t *testing.T) {
	n, repo, store, _ := branchFixture(t)
	switchTo(t, n, repo, "feature")
	put(t, store, repo, "img.bin", "\x00\x01feature")
	put(t, store, repo, "new.txt", "from feature\n")
	mustCheckpoint(t, n, repo, "feature")
	switchTo(t, n, repo, "main")
	put(t, store, repo, "img.bin", "\x00\x01main")
	put(t, store, repo, "new.txt", "from main\n")
	mustCheckpoint(t, n, repo, "main")
	res, err := n.mergeStart(repo, nativeMergeOptions{Source: "feature"})
	if err != nil || res.Status != "conflicts" {
		t.Fatalf("%+v %v", res, err)
	}
	reasons := map[string]string{}
	for _, c := range res.Conflicts {
		reasons[c.Path] = c.Reason
		if c.Path == "img.bin" && (c.Ours != nil || c.Omitted != "binary") {
			t.Fatalf("binary content inlined: %+v", c)
		}
	}
	if reasons["img.bin"] != "binary" || reasons["new.txt"] != "add_add" {
		t.Fatalf("reasons %v", reasons)
	}
}

func TestNativeMerge_SquashCherryPickRevert(t *testing.T) {
	n, repo, store, _ := branchFixture(t)
	switchTo(t, n, repo, "feature")
	put(t, store, repo, "x.txt", "x1\n")
	pick := mustCheckpoint(t, n, repo, "add x")
	put(t, store, repo, "y.txt", "y1\n")
	mustCheckpoint(t, n, repo, "add y")
	switchTo(t, n, repo, "main")

	// cherry-pick one revision
	res, err := n.mergeStart(repo, nativeMergeOptions{Source: pick.ID, Mode: "cherry_pick"})
	if err != nil || res.Status != "merged" || len(res.Revision.Parents) != 1 || !strings.Contains(res.Revision.Message, "cherry picked from revision "+pick.ID) {
		t.Fatalf("cherry-pick %+v %v", res, err)
	}
	if readWorking(t, store, repo, "x.txt") != "x1\n" {
		t.Fatal("cherry-pick content")
	}
	if _, err := store.Read(repoStoreKey(repo), "y.txt"); err == nil {
		t.Fatal("cherry-pick brought a later revision")
	}
	// picking it again changes nothing
	if res, _ = n.mergeStart(repo, nativeMergeOptions{Source: pick.ID, Mode: "cherry_pick"}); res.Status != "no_changes" {
		t.Fatalf("repeat cherry-pick %+v", res)
	}
	// revert it
	res, err = n.mergeStart(repo, nativeMergeOptions{Source: n.readRef(repo, "main", false), Mode: "revert"})
	if err != nil || res.Status != "merged" || !strings.HasPrefix(res.Revision.Message, "Revert ") {
		t.Fatalf("revert %+v %v", res, err)
	}
	if _, err := store.Read(repoStoreKey(repo), "x.txt"); err == nil {
		t.Fatal("revert left x.txt")
	}
	// squash the whole branch into one single-parent revision
	res, err = n.mergeStart(repo, nativeMergeOptions{Source: "feature", Mode: "squash", Message: "Feature, squashed"})
	if err != nil || res.Status != "merged" || len(res.Revision.Parents) != 1 || res.Revision.Message != "Feature, squashed" {
		t.Fatalf("squash %+v %v", res, err)
	}
	if readWorking(t, store, repo, "x.txt") != "x1\n" || readWorking(t, store, repo, "y.txt") != "y1\n" {
		t.Fatal("squash content")
	}
	if _, err := n.mergeStart(repo, nativeMergeOptions{Source: "feature", Mode: "rebase"}); err == nil {
		t.Fatal("unknown mode accepted")
	}
}

func TestNativeMerge_RefusesDirtyTreeAndUnknownSource(t *testing.T) {
	n, repo, store, _ := branchFixture(t)
	put(t, store, repo, "a.txt", "dirty\n")
	if _, err := n.mergeStart(repo, nativeMergeOptions{Source: "feature"}); err == nil || !strings.Contains(err.Error(), "checkpoint") {
		t.Fatalf("dirty merge: %v", err)
	}
	mustCheckpoint(t, n, repo, "clean")
	if _, err := n.mergeStart(repo, nativeMergeOptions{Source: "no-such-branch"}); err == nil {
		t.Fatal("unknown source accepted")
	}
	if _, err := n.mergeStart(repo, nativeMergeOptions{}); err == nil {
		t.Fatal("empty source accepted")
	}
}

func TestNativeMerge_UnrelatedHistories(t *testing.T) {
	n, repo, store := nativeFixture(t)
	put(t, store, repo, "same.txt", "main\n")
	mustCheckpoint(t, n, repo, "main root")
	// An orphan branch: its root revision has no parent.
	snap := func() *nativeRevision {
		defer n.lock(repo)()
		put(t, store, repo, "same.txt", "orphan\n")
		s, err := n.snapshotLocked(repo, true)
		if err != nil {
			t.Fatal(err)
		}
		rev, err := n.writeRevisionLocked(repo, s, nil, "orphan root", "t")
		if err != nil {
			t.Fatal(err)
		}
		return rev
	}()
	if _, err := n.restore(repo, n.readRef(repo, "main", false), nil, true, "t"); err != nil {
		t.Fatal(err)
	}
	if err := n.createBranchRef(repo, "orphan", snap.ID, "t"); err != nil {
		t.Fatal(err)
	}
	res, err := n.mergeStart(repo, nativeMergeOptions{Source: "orphan"})
	if err != nil || res.Status != "conflicts" || res.Base != "" || res.Conflicts[0].Reason != "add_add" {
		t.Fatalf("unrelated merge %+v %v", res, err)
	}
}

func TestNativeStash_TravelsWithBranch(t *testing.T) {
	n, repo, store, _ := branchFixture(t)
	put(t, store, repo, "a.txt", "unsaved on main\n")
	put(t, store, repo, "draft.txt", "draft\n")
	res := switchTo(t, n, repo, "feature")
	if res.Stashed == "" || res.StashRestored {
		t.Fatalf("switch away %+v", res)
	}
	if s, _ := n.status(repo); s.Dirty || s.Branch != "feature" {
		t.Fatalf("feature should be clean: %+v", s)
	}
	if _, err := store.Read(repoStoreKey(repo), "draft.txt"); err == nil {
		t.Fatal("main's draft leaked onto feature")
	}
	branches, _ := n.branchList(repo)
	for _, b := range branches {
		if b.Name == "main" && b.Stashes != 1 {
			t.Fatalf("branch list %+v", branches)
		}
	}
	// The stash is a GC root.
	if _, err := n.gc(repo, 0); err != nil {
		t.Fatal(err)
	}
	back := switchTo(t, n, repo, "main")
	if !back.StashRestored {
		t.Fatalf("switch back %+v", back)
	}
	if readWorking(t, store, repo, "a.txt") != "unsaved on main\n" || readWorking(t, store, repo, "draft.txt") != "draft\n" {
		t.Fatal("stash not restored")
	}
	if s, _ := n.status(repo); !s.Dirty {
		t.Fatal("restored changes should be unsaved again")
	}
	if list, _ := n.loadStashLocked(repo, "main"); len(list) != 0 {
		t.Fatal("stash not popped")
	}
}

func moveBranch(t *testing.T, n *nativeVCS, repo *Repo, branch, to string) {
	t.Helper()
	defer n.lock(repo)()
	if err := n.moveRefLocked(repo, branch, false, false, "", to, nativeRefLogEntry{Action: "test"}); err != nil {
		t.Fatal(err)
	}
}

func TestNativeStash_BranchMovedUnderneath(t *testing.T) {
	n, repo, store, _ := branchFixture(t)
	// Clean case: main moves to a revision that does not touch the stashed file.
	put(t, store, repo, "b.txt", "bee unsaved\n")
	switchTo(t, n, repo, "feature")
	put(t, store, repo, "a.txt", "feature a\n")
	f1 := mustCheckpoint(t, n, repo, "feature a")
	moveBranch(t, n, repo, "main", f1.ID) // e.g. another agent merged feature
	res := switchTo(t, n, repo, "main")
	if !res.StashRestored || len(res.StashConflicts) != 0 {
		t.Fatalf("clean re-apply %+v", res)
	}
	if readWorking(t, store, repo, "a.txt") != "feature a\n" || readWorking(t, store, repo, "b.txt") != "bee unsaved\n" {
		t.Fatal("re-applied stash content")
	}

	// Conflicting case: main moves to a revision that changes the stashed file.
	put(t, store, repo, "b.txt", "main draft\n")
	switchTo(t, n, repo, "feature")
	put(t, store, repo, "b.txt", "feature bee\n")
	f2 := mustCheckpoint(t, n, repo, "feature b")
	moveBranch(t, n, repo, "main", f2.ID)
	res = switchTo(t, n, repo, "main")
	if res.StashRestored || len(res.StashConflicts) != 1 || res.StashConflicts[0] != "b.txt" {
		t.Fatalf("conflicting stash %+v", res)
	}
	if list, _ := n.loadStashLocked(repo, "main"); len(list) != 1 {
		t.Fatal("a conflicting stash must be kept")
	}
	if s, _ := n.status(repo); s.Dirty || readWorking(t, store, repo, "b.txt") != "feature bee\n" {
		t.Fatalf("working tree should be the clean branch head: %+v", s)
	}
}

func TestNativeBranch_DeleteCompareRecreate(t *testing.T) {
	n, repo, store, base := branchFixture(t)
	switchTo(t, n, repo, "feature")
	put(t, store, repo, "f.txt", "f\n")
	feat := mustCheckpoint(t, n, repo, "feature work")
	if _, err := n.deleteBranch(repo, "feature", false, "t"); err == nil {
		t.Fatal("deleted the checked-out branch")
	}
	switchTo(t, n, repo, "main")
	cmp, err := n.compareRefs(repo, "feature", "")
	if err != nil || cmp.Ahead != 1 || cmp.Behind != 0 || cmp.MergeBase != base.ID || !cmp.FastForward || len(cmp.ChangedPaths) != 1 || cmp.To != "main" {
		t.Fatalf("compare %+v %v", cmp, err)
	}
	if _, err := n.deleteBranch(repo, "feature", false, "t"); err == nil || !strings.Contains(err.Error(), "not merged") {
		t.Fatalf("unmerged delete: %v", err)
	}
	head, err := n.deleteBranch(repo, "feature", true, "t")
	if err != nil || head != feat.ID {
		t.Fatalf("force delete %s %v", head, err)
	}
	if bs, _ := n.branches(repo); bs["feature"] != "" {
		t.Fatal("branch still listed")
	}
	// The deleted branch's head survives GC (its log is a root) and can be recreated.
	n.now = func() time.Time { return time.Now().Add(30 * 24 * time.Hour) }
	if _, err := n.gc(repo, nativeGCGrace); err != nil {
		t.Fatal(err)
	}
	n.now = time.Now
	if err := n.createBranchRef(repo, "feature", head, "t"); err != nil {
		t.Fatal(err)
	}
	if _, err := n.mergeStart(repo, nativeMergeOptions{Source: "feature"}); err != nil {
		t.Fatal(err)
	}
	if readWorking(t, store, repo, "f.txt") != "f\n" {
		t.Fatal("recreated branch lost content")
	}
	// A merged branch deletes without force.
	if _, err := n.deleteBranch(repo, "feature", false, "t"); err != nil {
		t.Fatal(err)
	}
}

func TestNativeHistory_PathAndBlameAcrossMerge(t *testing.T) {
	n, repo, store, base := branchFixture(t)
	put(t, store, repo, "b.txt", "bee2\n")
	mustCheckpoint(t, n, repo, "only b")
	switchTo(t, n, repo, "feature")
	put(t, store, repo, "a.txt", "line1\nline2\nline3\nline4\nFEATURE\n")
	featRev := mustCheckpoint(t, n, repo, "feature line 5")
	switchTo(t, n, repo, "main")
	put(t, store, repo, "a.txt", "MAIN\nline2\nline3\nline4\nline5\n")
	mainRev, err := n.checkpointExpect(repo, "main line 1", "alice", "")
	if err != nil {
		t.Fatal(err)
	}
	merge, err := n.mergeStart(repo, nativeMergeOptions{Source: "feature", Actor: "bob"})
	if err != nil || merge.Status != "merged" {
		t.Fatal(merge, err)
	}

	_, hist, err := n.pathHistory(repo, "", "a.txt", 0)
	if err != nil {
		t.Fatal(err)
	}
	msgs := []string{}
	for _, r := range hist {
		msgs = append(msgs, r.Message)
	}
	got := strings.Join(msgs, "|")
	for _, want := range []string{"Merge feature into main", "main line 1", "feature line 5", base.Message} {
		if !strings.Contains(got, want) {
			t.Fatalf("path history %q lacks %q", got, want)
		}
	}
	if strings.Contains(got, "only b") {
		t.Fatalf("path history includes an unrelated revision: %q", got)
	}

	_, lines, truncated, err := n.blame(repo, "", "a.txt")
	if err != nil || truncated || len(lines) != 5 {
		t.Fatalf("blame %v %v %d", err, truncated, len(lines))
	}
	if lines[0].Revision != mainRev.ID || lines[0].Actor != "alice" || lines[0].Text != "MAIN" {
		t.Fatalf("line 1 %+v", lines[0])
	}
	if lines[4].Revision != featRev.ID || lines[4].Text != "FEATURE" {
		t.Fatalf("line 5 %+v", lines[4])
	}
	if lines[2].Revision != base.ID {
		t.Fatalf("line 3 %+v", lines[2])
	}
	if _, _, _, err := n.blame(repo, "", "missing.txt"); err == nil {
		t.Fatal("blame of a missing file")
	}

	// Line diff between branch names, and against the working tree.
	put(t, store, repo, "a.txt", "MAIN\nline2\nline3 edited\nline4\nFEATURE\n")
	from, to, files, err := n.lineDiff(repo, "", "", nil)
	if err != nil || to != "working-tree" || from != merge.Revision.ID || len(files) != 1 || files[0].Added != 1 || files[0].Removed != 1 || !strings.Contains(files[0].Patch, "+line3 edited") {
		t.Fatalf("working diff %s %s %+v %v", from, to, files, err)
	}
	_, _, files, err = n.lineDiff(repo, "feature", "main", []string{"a.txt"})
	if err != nil || len(files) != 1 || !strings.Contains(files[0].Patch, "-line1\n+MAIN") {
		t.Fatalf("branch diff %+v %v", files, err)
	}
}
