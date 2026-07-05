package main

import (
	"strings"
	"testing"
)

func TestApplyUnifiedPatch_DryRunDoesNotWrite(t *testing.T) {
	store := newMemFileStore()
	store.CreateRepo("r")
	store.Write("r", "a.txt", []byte("one\ntwo\nthree\n"))
	patch := `--- a/a.txt
+++ b/a.txt
@@ -1,3 +1,3 @@
 one
-two
+TWO
 three
`
	res, err := applyUnifiedPatch(store, "r", patch, true)
	if err != nil {
		t.Fatal(err)
	}
	if !res.DryRun || res.Applied || len(res.ChangedFiles) != 1 {
		t.Fatalf("unexpected dry-run result: %+v", res)
	}
	if res.PatchID == "" || !strings.Contains(res.Hint, "patch_id") {
		t.Fatalf("dry-run should return reusable patch_id and hint: %+v", res)
	}
	got, _ := store.Read("r", "a.txt")
	if string(got) != "one\ntwo\nthree\n" {
		t.Errorf("dry run wrote file: %q", got)
	}
}

func TestPatchPreviewIDCanApplyDryRunPatch(t *testing.T) {
	store := newMemFileStore()
	store.CreateRepo("r")
	store.Write("r", "a.txt", []byte("one\ntwo\nthree\n"))
	patch := `--- a/a.txt
+++ b/a.txt
@@ -1,3 +1,3 @@
 one
-two
+TWO
 three
`
	dry, err := applyUnifiedPatch(store, "r", patch, true)
	if err != nil {
		t.Fatal(err)
	}
	previewPatch, err := loadPatchPreview(dry.PatchID, "r")
	if err != nil {
		t.Fatal(err)
	}
	applied, err := applyUnifiedPatch(store, "r", previewPatch, false)
	if err != nil {
		t.Fatal(err)
	}
	if !applied.Applied || len(applied.ChangedFiles) != 1 {
		t.Fatalf("unexpected apply result: %+v", applied)
	}
	got, _ := store.Read("r", "a.txt")
	if string(got) != "one\nTWO\nthree\n" {
		t.Errorf("patch_id apply wrote %q", got)
	}
}

func TestApplyUnifiedPatch_NoHunksExplainsFormat(t *testing.T) {
	store := newMemFileStore()
	store.CreateRepo("r")
	_, err := applyUnifiedPatch(store, "r", "replace foo with bar", true)
	if err == nil {
		t.Fatal("want error")
	}
	if !strings.Contains(err.Error(), "unified diff format") || !strings.Contains(err.Error(), "--- a/path") {
		t.Fatalf("error should explain expected format, got %v", err)
	}
}

func TestApplyUnifiedPatch_AppliesModifyAndCreate(t *testing.T) {
	store := newMemFileStore()
	store.CreateRepo("r")
	store.Write("r", "a.txt", []byte("one\ntwo\nthree\n"))
	patch := `--- a/a.txt
+++ b/a.txt
@@ -1,3 +1,3 @@
 one
-two
+TWO
 three
--- /dev/null
+++ b/new.txt
@@ -0,0 +1,2 @@
+alpha
+beta
`
	res, err := applyUnifiedPatch(store, "r", patch, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Applied || len(res.ChangedFiles) != 2 {
		t.Fatalf("unexpected result: %+v", res)
	}
	got, _ := store.Read("r", "a.txt")
	if string(got) != "one\nTWO\nthree\n" {
		t.Errorf("modified file = %q", got)
	}
	created, _ := store.Read("r", "new.txt")
	if string(created) != "alpha\nbeta\n" {
		t.Errorf("created file = %q", created)
	}
}

func TestApplyUnifiedPatch_RelocatesHunkWhenLineNumbersAreStale(t *testing.T) {
	store := newMemFileStore()
	store.CreateRepo("r")
	store.Write("r", "a.txt", []byte("intro\none\ntwo\nthree\n"))
	patch := `--- a/a.txt
+++ b/a.txt
@@ -1,3 +1,3 @@
 one
-two
+TWO
 three
`
	res, err := applyUnifiedPatch(store, "r", patch, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Applied || len(res.ChangedFiles) != 1 {
		t.Fatalf("unexpected result: %+v", res)
	}
	got, _ := store.Read("r", "a.txt")
	if string(got) != "intro\none\nTWO\nthree\n" {
		t.Errorf("relocated patch wrote %q", got)
	}
}

func TestApplyUnifiedPatch_ToleratesStaleContextWhenRemovalAnchorMatches(t *testing.T) {
	store := newMemFileStore()
	store.CreateRepo("r")
	store.Write("r", "App.jsx", []byte("const selectedIssue = issueData[0];\n<span className=\"status-chip\">{selectedIssue.status}</span>\n<h2>{selectedIssue.title}</h2>\n"))
	patch := `--- a/App.jsx
+++ b/App.jsx
@@ -1,3 +1,6 @@
 const selected = issueData[0];
-<span className="status-chip">{selectedIssue.status}</span>
+<div className="detail-header">
+  <span className="status-chip">{selectedIssue.status}</span>
+</div>
 <h2>{selected.title}</h2>
`
	res, err := applyUnifiedPatch(store, "r", patch, false)
	if err != nil {
		t.Fatal(err)
	}
	if !res.Applied || len(res.RejectedHunks) != 0 {
		t.Fatalf("unexpected result: %+v", res)
	}
	got, _ := store.Read("r", "App.jsx")
	want := "const selectedIssue = issueData[0];\n<div className=\"detail-header\">\n  <span className=\"status-chip\">{selectedIssue.status}</span>\n</div>\n<h2>{selectedIssue.title}</h2>\n"
	if string(got) != want {
		t.Errorf("context-drift patch wrote:\n%q\nwant:\n%q", got, want)
	}
}

func TestApplyUnifiedPatch_RejectsMismatchWithoutWriting(t *testing.T) {
	store := newMemFileStore()
	store.CreateRepo("r")
	store.Write("r", "a.txt", []byte("one\ntwo\nthree\n"))
	patch := `--- a/a.txt
+++ b/a.txt
@@ -1,3 +1,3 @@
 one
-missing
+TWO
 three
`
	res, err := applyUnifiedPatch(store, "r", patch, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Applied || len(res.RejectedHunks) == 0 || !strings.Contains(res.RejectedHunks[0], "removal mismatch") {
		t.Fatalf("unexpected reject result: %+v", res)
	}
	if len(res.RejectedContext) != 1 || res.RejectedContext[0].StartLine != 2 || !strings.Contains(res.RejectedContext[0].Excerpt, "\tthree") {
		t.Fatalf("reject should include nearby context: %+v", res.RejectedContext)
	}
	got, _ := store.Read("r", "a.txt")
	if string(got) != "one\ntwo\nthree\n" {
		t.Errorf("rejected patch wrote file: %q", got)
	}
}

func TestApplyUnifiedPatch_RejectsSecondFileWithoutWritingFirst(t *testing.T) {
	store := newMemFileStore()
	store.CreateRepo("r")
	store.Write("r", "a.txt", []byte("one\ntwo\n"))
	store.Write("r", "b.txt", []byte("alpha\nbeta\n"))
	patch := `--- a/a.txt
+++ b/a.txt
@@ -1,2 +1,2 @@
 one
-two
+TWO
--- a/b.txt
+++ b/b.txt
@@ -1,2 +1,2 @@
 alpha
-missing
+BETA
`
	res, err := applyUnifiedPatch(store, "r", patch, false)
	if err != nil {
		t.Fatal(err)
	}
	if res.Applied || len(res.RejectedHunks) == 0 {
		t.Fatalf("unexpected result: %+v", res)
	}
	got, _ := store.Read("r", "a.txt")
	if string(got) != "one\ntwo\n" {
		t.Errorf("first file changed despite second-file rejection: %q", got)
	}
}
