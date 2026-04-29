package main

import (
	"strings"
	"testing"
)

// ─── applyEdit ─────────────────────────────────────────────────────

func TestApplyEdit_Unique(t *testing.T) {
	body := "hello world\n"
	got, n, err := applyEdit(body, "world", "there", false)
	if err != nil {
		t.Fatal(err)
	}
	if n != 1 {
		t.Errorf("replacements = %d, want 1", n)
	}
	if got != "hello there\n" {
		t.Errorf("got %q", got)
	}
}

func TestApplyEdit_NotFound(t *testing.T) {
	_, _, err := applyEdit("hello world", "nope", "x", false)
	if err == nil {
		t.Fatal("want error for not-found old_string")
	}
	if !strings.Contains(err.Error(), "not found") {
		t.Errorf("unexpected error: %v", err)
	}
}

func TestApplyEdit_NonUniqueWithoutReplaceAll(t *testing.T) {
	body := "foo\nfoo\nbar\nfoo\n"
	_, _, err := applyEdit(body, "foo", "qux", false)
	if err == nil {
		t.Fatal("want error for non-unique old_string")
	}
	if !strings.Contains(err.Error(), "not unique") {
		t.Errorf("expected uniqueness error, got: %v", err)
	}
	// Sample line numbers should be embedded so the agent can find
	// the right occurrence.
	if !strings.Contains(err.Error(), "1") || !strings.Contains(err.Error(), "2") {
		t.Errorf("expected line numbers in error, got: %v", err)
	}
}

func TestApplyEdit_ReplaceAll(t *testing.T) {
	body := "foo\nfoo\nbar\nfoo\n"
	got, n, err := applyEdit(body, "foo", "qux", true)
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("replacements = %d, want 3", n)
	}
	if got != "qux\nqux\nbar\nqux\n" {
		t.Errorf("got %q", got)
	}
}

func TestApplyEdit_NoChange(t *testing.T) {
	_, _, err := applyEdit("foo", "foo", "foo", false)
	if err == nil {
		t.Fatal("want error when old == new")
	}
}

func TestApplyEdit_EmptyOld(t *testing.T) {
	_, _, err := applyEdit("foo", "", "x", false)
	if err == nil {
		t.Fatal("want error for empty old_string")
	}
}

// ─── multiEditFile (atomicity) ─────────────────────────────────────

func TestMultiEdit_Atomic_AllOrNothing(t *testing.T) {
	store := newMemFileStore()
	if err := store.CreateRepo("r"); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Write("r", "a.txt", []byte("apple\nbanana\ncherry\n")); err != nil {
		t.Fatal(err)
	}
	// First edit succeeds, second one fails (target absent). The whole
	// batch must abort and the file must look unchanged.
	ops := []EditOp{
		{OldString: "apple", NewString: "APPLE"},
		{OldString: "missing", NewString: "x"},
	}
	_, err := multiEditFile(store, "r", "a.txt", ops)
	if err == nil {
		t.Fatal("want error from second edit")
	}
	if !strings.Contains(err.Error(), "edit #2") {
		t.Errorf("error should name the failing op, got %v", err)
	}
	got, _ := store.Read("r", "a.txt")
	if string(got) != "apple\nbanana\ncherry\n" {
		t.Errorf("file was modified despite atomic failure: %q", got)
	}
}

func TestMultiEdit_Sequential(t *testing.T) {
	store := newMemFileStore()
	if err := store.CreateRepo("r"); err != nil {
		t.Fatal(err)
	}
	// Each edit applies to the result of the previous one.
	if _, err := store.Write("r", "a.txt", []byte("foo")); err != nil {
		t.Fatal(err)
	}
	ops := []EditOp{
		{OldString: "foo", NewString: "bar"},
		{OldString: "bar", NewString: "baz"},
	}
	res, err := multiEditFile(store, "r", "a.txt", ops)
	if err != nil {
		t.Fatal(err)
	}
	if res.OperationCount != 2 || res.Replacements != 2 {
		t.Errorf("unexpected result: %+v", res)
	}
	got, _ := store.Read("r", "a.txt")
	if string(got) != "baz" {
		t.Errorf("got %q, want baz", got)
	}
}

// ─── Read with line numbers ────────────────────────────────────────

func TestReadWithLineNumbers_Basic(t *testing.T) {
	store := newMemFileStore()
	store.CreateRepo("r")
	store.Write("r", "f.txt", []byte("alpha\nbeta\ngamma\n"))
	res, err := readWithLineNumbers(store, "r", "f.txt", 0, 0)
	if err != nil {
		t.Fatal(err)
	}
	if res.TotalLines != 3 {
		t.Errorf("total_lines = %d, want 3", res.TotalLines)
	}
	if !strings.Contains(res.Content, "alpha") || !strings.Contains(res.Content, "gamma") {
		t.Errorf("missing content: %q", res.Content)
	}
	// The format should be cat -n style: padded line number + tab.
	if !strings.Contains(res.Content, "\talpha") {
		t.Errorf("expected tab-separated line numbers: %q", res.Content)
	}
	if res.Truncated {
		t.Error("3-line file shouldn't be truncated with default limit")
	}
}

func TestReadWithLineNumbers_OffsetLimit(t *testing.T) {
	store := newMemFileStore()
	store.CreateRepo("r")
	store.Write("r", "f.txt", []byte("a\nb\nc\nd\ne\n"))
	res, err := readWithLineNumbers(store, "r", "f.txt", 2, 2)
	if err != nil {
		t.Fatal(err)
	}
	if res.StartLine != 2 || res.EndLine != 3 {
		t.Errorf("range = %d-%d, want 2-3", res.StartLine, res.EndLine)
	}
	if !strings.Contains(res.Content, "b") || !strings.Contains(res.Content, "c") {
		t.Errorf("expected b,c in window, got %q", res.Content)
	}
	if strings.Contains(res.Content, "\ta\n") || strings.Contains(res.Content, "\td\n") {
		t.Errorf("window should not include outside lines, got %q", res.Content)
	}
	if !res.Truncated {
		t.Error("partial read should set truncated=true")
	}
}

// ─── Glob ──────────────────────────────────────────────────────────

func TestGlobRepo(t *testing.T) {
	store := newMemFileStore()
	store.CreateRepo("r")
	store.Write("r", "app/page.tsx", []byte("a"))
	store.Write("r", "app/layout.tsx", []byte("b"))
	store.Write("r", "lib/util.ts", []byte("c"))
	store.Write("r", "README.md", []byte("d"))

	got, err := globRepo(store, "r", "**/*.tsx")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("**/*.tsx -> %v, want 2 files", got)
	}

	got, err = globRepo(store, "r", "app/*.tsx")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 {
		t.Errorf("app/*.tsx -> %v, want 2 files", got)
	}

	got, err = globRepo(store, "r", "**/util.ts")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || got[0] != "lib/util.ts" {
		t.Errorf("**/util.ts -> %v, want lib/util.ts", got)
	}
}

// ─── Grep ──────────────────────────────────────────────────────────

func TestGrepRepo_Literal(t *testing.T) {
	store := newMemFileStore()
	store.CreateRepo("r")
	store.Write("r", "a.go", []byte("package main\n\nfunc Hello() {}\n"))
	store.Write("r", "b.go", []byte("package main\n\nfunc World() {}\n"))

	matches, err := grepRepo(store, "r", GrepOptions{Pattern: "Hello"})
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatalf("matches: %+v", matches)
	}
	if matches[0].Path != "a.go" || matches[0].Line != 3 {
		t.Errorf("unexpected match: %+v", matches[0])
	}
}

func TestGrepRepo_Regex(t *testing.T) {
	store := newMemFileStore()
	store.CreateRepo("r")
	store.Write("r", "a.go", []byte("func Hello()\nfunc World()\n"))
	matches, err := grepRepo(store, "r", GrepOptions{Pattern: `^func \w+\(`, Regex: true})
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 2 {
		t.Errorf("matches: %+v", matches)
	}
}

func TestGrepRepo_FilePattern(t *testing.T) {
	store := newMemFileStore()
	store.CreateRepo("r")
	store.Write("r", "a.go", []byte("hello\n"))
	store.Write("r", "a.txt", []byte("hello\n"))
	matches, err := grepRepo(store, "r", GrepOptions{Pattern: "hello", FilePattern: "**/*.go"})
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 || matches[0].Path != "a.go" {
		t.Errorf("expected only a.go, got %+v", matches)
	}
}

func TestGrepRepo_Context(t *testing.T) {
	store := newMemFileStore()
	store.CreateRepo("r")
	store.Write("r", "a.txt", []byte("one\ntwo\nNEEDLE\nfour\nfive\n"))
	matches, err := grepRepo(store, "r", GrepOptions{Pattern: "NEEDLE", Context: 1})
	if err != nil {
		t.Fatal(err)
	}
	if len(matches) != 1 {
		t.Fatal(matches)
	}
	m := matches[0]
	if len(m.Before) != 1 || m.Before[0] != "two" {
		t.Errorf("before = %v", m.Before)
	}
	if len(m.After) != 1 || m.After[0] != "four" {
		t.Errorf("after = %v", m.After)
	}
}

func TestGrepRepo_SkipsBinary(t *testing.T) {
	store := newMemFileStore()
	store.CreateRepo("r")
	store.Write("r", "blob.bin", []byte{0x00, 0xff, 0xab, 0x00})
	store.Write("r", "code.go", []byte("hello"))
	matches, err := grepRepo(store, "r", GrepOptions{Pattern: "h"})
	if err != nil {
		t.Fatal(err)
	}
	for _, m := range matches {
		if m.Path == "blob.bin" {
			t.Errorf("binary file should be skipped: %+v", m)
		}
	}
}
