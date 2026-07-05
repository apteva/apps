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
	got, _ := store.Read("r", "a.txt")
	if string(got) != "one\ntwo\nthree\n" {
		t.Errorf("dry run wrote file: %q", got)
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
