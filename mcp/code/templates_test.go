package main

import (
	"testing"
)

// applyTemplate uses the embedded templatesFS, not the FileStore's
// internals — so this test exercises the full create-from-template
// path through whichever store we hand it.

func TestApplyTemplate_Nextjs(t *testing.T) {
	store := newMemFileStore()
	if err := store.CreateRepo("r"); err != nil {
		t.Fatal(err)
	}
	count, err := applyTemplate(store, "r", "nextjs")
	if err != nil {
		t.Fatal(err)
	}
	if count == 0 {
		t.Fatal("expected at least one file from nextjs template")
	}
	// The skeleton must include the Next.js entry points and the
	// .apteva sidecar metadata that the future deploy app reads.
	mustExist := []string{
		"package.json",
		"next.config.js",
		"app/page.tsx",
		"app/layout.tsx",
		".apteva/repo.json",
		".apteva/Dockerfile",
	}
	for _, p := range mustExist {
		if _, err := store.Read("r", p); err != nil {
			t.Errorf("template missing %q: %v", p, err)
		}
	}
}

func TestApplyTemplate_Blank(t *testing.T) {
	store := newMemFileStore()
	store.CreateRepo("r")
	count, err := applyTemplate(store, "r", "blank")
	if err != nil {
		t.Fatal(err)
	}
	// Blank framework writes nothing: the function returns early so
	// users start with a truly empty tree.
	if count != 0 {
		t.Errorf("blank framework should write 0 files, wrote %d", count)
	}
}

func TestApplyTemplate_UnknownFrameworkSilent(t *testing.T) {
	store := newMemFileStore()
	store.CreateRepo("r")
	count, err := applyTemplate(store, "r", "fortran")
	if err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Errorf("unknown framework should write 0 files, wrote %d", count)
	}
}
