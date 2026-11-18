package main

// Tier 1 coverage for Code 0.17: branch protection, working-tree-free merges,
// work branches that carry unsaved changes, and change requests with
// reviews, stale approvals, blockers and merge.

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"
)

func crFixture(t *testing.T) (*changeRequestService, *nativeVCS, *Repo, *LocalFileStore) {
	t.Helper()
	db := openTestDB(t)
	repo, err := dbCreateRepo(db, "p", CreateRepoInput{Name: "Review Demo", Framework: "blank"})
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	store := NewLocalFileStore(filepath.Join(root, "repos"))
	if err := store.CreateRepo(repoStoreKey(repo)); err != nil {
		t.Fatal(err)
	}
	n := newNativeVCS(filepath.Join(root, "data"), store, newRepoLockSet())
	put(t, store, repo, "app.txt", "title=Demo\ncolor=blue\nsize=10\n")
	mustCheckpoint(t, n, repo, "base")
	return &changeRequestService{db: db, n: n}, n, repo, store
}

func TestNativeProtection_GuardsEveryDirectWrite(t *testing.T) {
	_, n, repo, store := crFixture(t)
	if _, err := n.setProtection(repo, "main", 1, "owner"); err != nil {
		t.Fatal(err)
	}
	put(t, store, repo, "app.txt", "edited on main\n")
	if _, err := n.checkpoint(repo, "direct", "agent"); !errors.Is(err, errNativeProtected) {
		t.Fatalf("checkpoint on protected main: %v", err)
	}
	head := n.readRef(repo, "main", false)
	if _, err := n.restore(repo, head, nil, false, "agent"); !errors.Is(err, errNativeProtected) {
		t.Fatalf("restore with revision: %v", err)
	}
	if _, _, err := n.undo(repo, "main", head, "agent"); !errors.Is(err, errNativeProtected) {
		t.Fatalf("undo: %v", err)
	}
	if err := n.createBranchRef(repo, "other", "", "t"); err != nil {
		t.Fatal(err)
	}
	if _, err := n.mergeStart(repo, nativeMergeOptions{Source: "other"}); !errors.Is(err, errNativeProtected) {
		t.Fatalf("merge onto protected main: %v", err)
	}
	// Git auto-sync (an internal writer) may still record on a protected branch.
	func() {
		defer n.lock(repo)()
		if _, err := n.checkpointHeld(repo, "Auto-save", "auto-sync"); err != nil {
			t.Fatalf("auto-sync on protected branch: %v", err)
		}
	}()
	head = n.readRef(repo, "main", false)
	put(t, store, repo, "app.txt", "edited on main again\n")
	// Work branch carries the unsaved edit away from main.
	if err := n.createAndSwitch(repo, "work", "agent"); err != nil {
		t.Fatal(err)
	}
	st, _ := n.status(repo)
	if st.Branch != "work" || !st.Dirty || readWorking(t, store, repo, "app.txt") != "edited on main again\n" {
		t.Fatalf("work branch %+v", st)
	}
	mustCheckpoint(t, n, repo, "edit on work")
	if n.readRef(repo, "main", false) != head {
		t.Fatal("main moved")
	}
	if _, err := n.deleteBranch(repo, "main", true, "t"); err == nil {
		t.Fatal("deleted a protected branch")
	}
	branches, _ := n.branchList(repo)
	for _, b := range branches {
		if b.Name == "main" && (!b.Protected || b.RequiredApprovals != 1) {
			t.Fatalf("list %+v", b)
		}
	}
	if _, err := n.setProtection(repo, "main", -1, "owner"); err != nil {
		t.Fatal(err)
	}
	if _, err := n.switchBranch(repo, "main"); err != nil {
		t.Fatal(err)
	}
	put(t, store, repo, "app.txt", "now allowed\n")
	mustCheckpoint(t, n, repo, "after unprotect")
}

func TestNativeMergeRefs_WithoutWorkingTree(t *testing.T) {
	_, n, repo, store := crFixture(t)
	if err := n.createAndSwitch(repo, "feature", "a"); err != nil {
		t.Fatal(err)
	}
	put(t, store, repo, "app.txt", "title=Demo\ncolor=green\nsize=10\n")
	feat := mustCheckpoint(t, n, repo, "green")
	// target not checked out: working tree (feature) untouched
	res, err := n.mergeRefs(repo, "main", "feature", "merge", "Land feature", "a", false, "")
	if err != nil || res.Status != "merged" || len(res.Revision.Parents) != 2 || res.Revision.Parents[1] != feat.ID {
		t.Fatalf("mergeRefs %+v %v", res, err)
	}
	if st, _ := n.status(repo); st.Branch != "feature" || st.Dirty {
		t.Fatalf("working tree touched: %+v", st)
	}
	main, _ := n.entriesForLocked(repo, n.readRef(repo, "main", false))
	featT, _ := n.entriesForLocked(repo, feat.ID)
	if main["app.txt"] != featT["app.txt"] {
		t.Fatal("main does not have the feature content")
	}
	if res, _ := n.mergeRefs(repo, "main", "feature", "merge", "", "a", true, ""); res.Status != "up_to_date" {
		t.Fatalf("second merge %+v", res)
	}
	// Checked-out dirty target is refused; clean one is updated.
	if _, err := n.switchBranch(repo, "main"); err != nil {
		t.Fatal(err)
	}
	if err := n.createBranchRef(repo, "f2", "", "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := n.switchBranch(repo, "f2"); err != nil {
		t.Fatal(err)
	}
	put(t, store, repo, "new.txt", "new\n")
	mustCheckpoint(t, n, repo, "new file")
	if _, err := n.switchBranch(repo, "main"); err != nil {
		t.Fatal(err)
	}
	put(t, store, repo, "app.txt", "dirty\n")
	if _, err := n.mergeRefs(repo, "main", "f2", "squash", "sq", "a", false, ""); err == nil || !strings.Contains(err.Error(), "not in a revision") {
		t.Fatalf("dirty checked-out target: %v", err)
	}
	if _, err := n.restore(repo, n.readRef(repo, "main", false), nil, true, "a"); err != nil {
		t.Fatal(err)
	}
	res, err = n.mergeRefs(repo, "main", "f2", "squash", "sq", "a", false, "")
	if err != nil || len(res.Revision.Parents) != 1 || readWorking(t, store, repo, "new.txt") != "new\n" {
		t.Fatalf("squash into checked-out main %+v %v", res, err)
	}
	if _, err := n.mergeRefs(repo, "main", "f2", "merge", "", "a", false, "0000000000000000000000000000000000000000000000000000000000000000"); !errors.Is(err, errNativeRefMoved) {
		t.Fatalf("expect source mismatch: %v", err)
	}
}

func TestChangeRequests_ReviewApproveMerge(t *testing.T) {
	svc, n, repo, store := crFixture(t)
	if _, err := n.setProtection(repo, "main", 1, "owner"); err != nil {
		t.Fatal(err)
	}
	if err := n.createAndSwitch(repo, "green", "agent:1"); err != nil {
		t.Fatal(err)
	}
	put(t, store, repo, "app.txt", "title=Demo\ncolor=green\nsize=10\n")
	mustCheckpoint(t, n, repo, "green")

	if _, err := svc.create(repo, changeRequestCreate{Source: "green", Author: "agent:1"}); err == nil {
		t.Fatal("create without title")
	}
	if _, err := svc.create(repo, changeRequestCreate{Title: "x", Source: "nope", Author: "agent:1"}); err == nil {
		t.Fatal("create with unknown source")
	}
	cr, err := svc.create(repo, changeRequestCreate{Title: "Make it green", Body: "Why: brand", Source: "green", Issues: []int{3}, Author: "agent:1"})
	if err != nil || cr.Number != 1 || cr.Target != "main" || cr.State != "open" || len(cr.Issues) != 1 {
		t.Fatalf("create %+v %v", cr, err)
	}
	if _, err := svc.create(repo, changeRequestCreate{Title: "dup", Source: "green", Author: "agent:1"}); err == nil {
		t.Fatal("duplicate open request accepted")
	}
	d, err := svc.detail(repo, 1, true)
	if err != nil || d.CanMerge || d.Mergeable != "mergeable" || d.Reviews.Required != 1 || len(d.Files) != 1 || !strings.Contains(d.Files[0].Patch, "+color=green") {
		t.Fatalf("detail %+v %v", d, err)
	}
	if !strings.Contains(strings.Join(d.Blockers, ";"), "needs 1 approval") {
		t.Fatalf("blockers %v", d.Blockers)
	}
	if _, _, err := svc.merge(repo, 1, "agent:1", "", "", false); err == nil {
		t.Fatal("merged without approval")
	}
	if _, err := svc.review(repo, 1, "agent:1", "approve", "lgtm"); err == nil {
		t.Fatal("author approved own request")
	}
	if _, err := svc.review(repo, 1, "user:2", "maybe", ""); err == nil {
		t.Fatal("unknown verdict accepted")
	}
	if d, err = svc.review(repo, 1, "user:2", "request_changes", "use a darker green"); err != nil || len(d.Reviews.ChangesRequested) != 1 {
		t.Fatalf("request changes %+v %v", d, err)
	}
	if _, err := svc.comment(repo, 1, "user:2", "this line", "app.txt", 2); err != nil {
		t.Fatal(err)
	}
	// Author pushes a fix; reviewer approves the new head.
	put(t, store, repo, "app.txt", "title=Demo\ncolor=darkgreen\nsize=10\n")
	mustCheckpoint(t, n, repo, "darker")
	if d, err = svc.review(repo, 1, "user:2", "approve", ""); err != nil || len(d.Reviews.Approvals) != 1 || len(d.Reviews.ChangesRequested) != 0 || !d.CanMerge {
		t.Fatalf("approve %+v %v", d.Reviews, err)
	}
	// A new revision after approval makes it stale.
	put(t, store, repo, "extra.txt", "late\n")
	mustCheckpoint(t, n, repo, "late change")
	if d, _ = svc.detail(repo, 1, false); d.CanMerge || len(d.Reviews.StaleApprovals) != 1 {
		t.Fatalf("stale approval %+v", d.Reviews)
	}
	if _, err := svc.review(repo, 1, "user:2", "approve", "again"); err != nil {
		t.Fatal(err)
	}
	d, res, err := svc.merge(repo, 1, "user:2", "squash", "", true)
	if err != nil || d.State != "merged" || res.Revision == nil || len(res.Revision.Parents) != 1 || d.MergeRevision != res.Revision.ID || d.MergedBy != "user:2" {
		t.Fatalf("merge %+v %+v %v", d, res, err)
	}
	mainT, _ := n.entriesForLocked(repo, n.readRef(repo, "main", false))
	if _, ok := mainT["extra.txt"]; !ok {
		t.Fatal("main lacks the merged files")
	}
	// The source was checked out, so it is kept (with a note) instead of deleted.
	if bs, _ := n.branches(repo); bs["green"] == "" {
		t.Fatal("checked-out source branch deleted")
	}
	notes := strings.Builder{}
	for _, nt := range d.Notes {
		notes.WriteString(nt.Kind + ":" + nt.Body + ";")
	}
	for _, want := range []string{"event:opened", "review:use a darker green", "comment:this line", "event:merged", "source branch kept"} {
		if !strings.Contains(notes.String(), want) {
			t.Fatalf("notes %q lack %q", notes.String(), want)
		}
	}
	if _, err := svc.review(repo, 1, "user:3", "approve", ""); err == nil {
		t.Fatal("review on a merged request")
	}
	if _, err := svc.setState(repo, 1, "user:2", "closed"); err == nil {
		t.Fatal("closed a merged request")
	}
	list, _ := svc.list(repo, "merged", 0)
	if len(list) != 1 {
		t.Fatalf("merged list %d", len(list))
	}
}

func TestChangeRequests_ConflictsBlockAndCloseReopen(t *testing.T) {
	svc, n, repo, store := crFixture(t)
	if err := n.createAndSwitch(repo, "a", "x"); err != nil {
		t.Fatal(err)
	}
	put(t, store, repo, "app.txt", "title=Demo\ncolor=A\nsize=10\n")
	mustCheckpoint(t, n, repo, "A")
	if _, err := n.switchBranch(repo, "main"); err != nil {
		t.Fatal(err)
	}
	put(t, store, repo, "app.txt", "title=Demo\ncolor=MAIN\nsize=10\n")
	mustCheckpoint(t, n, repo, "main moves")
	if _, err := svc.create(repo, changeRequestCreate{Title: "A", Source: "a", Author: "x"}); err != nil {
		t.Fatal(err)
	}
	d, err := svc.detail(repo, 1, false)
	if err != nil || d.Mergeable != "conflicts" || d.CanMerge || len(d.Conflicts) != 1 || !strings.Contains(d.Blockers[0], "repos_merge") {
		t.Fatalf("conflict detail %+v %v", d, err)
	}
	if _, _, err := svc.merge(repo, 1, "x", "", "", false); err == nil {
		t.Fatal("merged with conflicts")
	}
	if d, err = svc.setState(repo, 1, "x", "closed"); err != nil || d.State != "closed" {
		t.Fatalf("close %+v %v", d, err)
	}
	if list, _ := svc.list(repo, "", 0); len(list) != 0 {
		t.Fatal("closed request listed as open")
	}
	if d, err = svc.setState(repo, 1, "x", "open"); err != nil || d.State != "open" {
		t.Fatalf("reopen %+v %v", d, err)
	}
	// Resolve the conflict on the source branch, then it merges (no protection).
	if _, err := n.switchBranch(repo, "a"); err != nil {
		t.Fatal(err)
	}
	if _, err := n.mergeStart(repo, nativeMergeOptions{Source: "main"}); err != nil {
		t.Fatal(err)
	}
	if _, err := n.mergeResolve(repo, "app.txt", "ours", nil); err != nil {
		t.Fatal(err)
	}
	if _, err := n.mergeContinue(repo, "", "x"); err != nil {
		t.Fatal(err)
	}
	d, res, err := svc.merge(repo, 1, "x", "", "", true)
	if err != nil || d.State != "merged" || len(res.Revision.Parents) != 2 {
		t.Fatalf("merge after resolving %+v %v", d, err)
	}
	if bs, _ := n.branches(repo); bs["a"] == "" {
		t.Fatal("checked-out source deleted")
	}
}
