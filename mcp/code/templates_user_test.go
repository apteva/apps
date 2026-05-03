package main

import (
	"testing"
)

// User-template + fork suite. Embedded-template behaviour stays in
// templates_test.go; this file covers everything migration 002 added.

func TestSetTemplate_DefaultsToPrivate(t *testing.T) {
	db := openTestDB(t)
	r, err := dbCreateRepo(db, "p1", CreateRepoInput{Name: "Starter"})
	if err != nil {
		t.Fatal(err)
	}
	got, err := dbSetTemplate(db, "p1", r.Slug, true, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if !got.IsTemplate || got.TemplateScope != "private" {
		t.Errorf("expected (is_template=true, scope=private), got (%v, %q)", got.IsTemplate, got.TemplateScope)
	}
}

func TestSetTemplate_RejectsBadScope(t *testing.T) {
	db := openTestDB(t)
	r, _ := dbCreateRepo(db, "p1", CreateRepoInput{Name: "x"})
	if _, err := dbSetTemplate(db, "p1", r.Slug, true, "company", "", ""); err == nil {
		t.Fatal("want error for unknown scope")
	}
}

func TestUnmarkTemplate_ClearsAllFields(t *testing.T) {
	db := openTestDB(t)
	r, _ := dbCreateRepo(db, "p1", CreateRepoInput{Name: "x"})
	_, _ = dbSetTemplate(db, "p1", r.Slug, true, "global", "tag", "🚀")
	got, err := dbSetTemplate(db, "p1", r.Slug, false, "", "", "")
	if err != nil {
		t.Fatal(err)
	}
	if got.IsTemplate || got.TemplateScope != "" || got.TemplateTagline != "" || got.TemplateIcon != "" {
		t.Errorf("unmark should reset every template field, got %+v", got)
	}
}

func TestListUserTemplates_ScopeVisibility(t *testing.T) {
	// alice has a private + a project template; bob has a global one.
	// alice should see her own two + bob's global = 3.
	// bob should see his own + nothing of alice's = 1.
	db := openTestDB(t)
	a1, _ := dbCreateRepo(db, "alice", CreateRepoInput{Name: "a1"})
	a2, _ := dbCreateRepo(db, "alice", CreateRepoInput{Name: "a2"})
	b1, _ := dbCreateRepo(db, "bob", CreateRepoInput{Name: "b1"})
	_, _ = dbSetTemplate(db, "alice", a1.Slug, true, "private", "", "")
	_, _ = dbSetTemplate(db, "alice", a2.Slug, true, "project", "", "")
	_, _ = dbSetTemplate(db, "bob", b1.Slug, true, "global", "", "")

	got, err := dbListUserTemplates(db, "alice")
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 3 {
		t.Errorf("alice should see 3 templates (own private+project + bob's global), got %d", len(got))
	}
	got, _ = dbListUserTemplates(db, "bob")
	if len(got) != 1 || got[0].Slug != b1.Slug {
		t.Errorf("bob should only see his own global template, got %d entries", len(got))
	}
}

func TestListUserTemplates_ExcludesArchived(t *testing.T) {
	db := openTestDB(t)
	r, _ := dbCreateRepo(db, "p", CreateRepoInput{Name: "x"})
	_, _ = dbSetTemplate(db, "p", r.Slug, true, "private", "", "")
	_ = dbArchiveRepo(db, "p", r.Slug)
	got, _ := dbListUserTemplates(db, "p")
	if len(got) != 0 {
		t.Errorf("archived templates should be hidden, got %d", len(got))
	}
}

func TestRecordFork_RoundTrip(t *testing.T) {
	db := openTestDB(t)
	r, _ := dbCreateRepo(db, "p", CreateRepoInput{Name: "child"})
	if err := dbRecordFork(db, r.ID, "parent-slug", "user"); err != nil {
		t.Fatal(err)
	}
	got, err := dbGetFork(db, r.ID)
	if err != nil || got == nil {
		t.Fatalf("expected fork row, got %v err=%v", got, err)
	}
	if got.ParentSlug != "parent-slug" || got.ParentKind != "user" {
		t.Errorf("fork = %+v", got)
	}
}

func TestRecordFork_UpsertOverwrites(t *testing.T) {
	db := openTestDB(t)
	r, _ := dbCreateRepo(db, "p", CreateRepoInput{Name: "child"})
	_ = dbRecordFork(db, r.ID, "first", "user")
	if err := dbRecordFork(db, r.ID, "second", "embedded"); err != nil {
		t.Fatal(err)
	}
	got, _ := dbGetFork(db, r.ID)
	if got.ParentSlug != "second" || got.ParentKind != "embedded" {
		t.Errorf("upsert should overwrite, got %+v", got)
	}
}

// fork() copies every file from a source treeReader into a destination
// FileStore. This pins the contract that user-template forks and
// embedded-template materialisation walk the same code path.
func TestFork_FromUserRepoCopiesEveryFile(t *testing.T) {
	store := newMemFileStore()
	_ = store.CreateRepo("src")
	_ = store.CreateRepo("dst")
	_, _ = store.Write("src", "package.json", []byte(`{"name":"src"}`))
	_, _ = store.Write("src", "src/index.ts", []byte("export const x = 1\n"))
	_, _ = store.Write("src", "deep/nested/file.txt", []byte("hi"))

	n, err := fork(storeReader{s: store}, "src", store, "dst")
	if err != nil {
		t.Fatal(err)
	}
	if n != 3 {
		t.Errorf("expected 3 files copied, got %d", n)
	}
	for _, p := range []string{"package.json", "src/index.ts", "deep/nested/file.txt"} {
		body, err := store.Read("dst", p)
		if err != nil {
			t.Errorf("dst missing %q: %v", p, err)
		}
		src, _ := store.Read("src", p)
		if string(body) != string(src) {
			t.Errorf("contents differ for %q", p)
		}
	}
}

func TestFork_FromEmbeddedTemplateMatchesApplyTemplate(t *testing.T) {
	store := newMemFileStore()
	_ = store.CreateRepo("a")
	_ = store.CreateRepo("b")

	viaApply, err := applyTemplate(store, "a", "nextjs")
	if err != nil {
		t.Fatal(err)
	}
	viaFork, err := fork(embeddedReader{}, "nextjs", store, "b")
	if err != nil {
		t.Fatal(err)
	}
	if viaApply != viaFork || viaApply == 0 {
		t.Errorf("applyTemplate=%d vs fork=%d (both should match and be >0)", viaApply, viaFork)
	}
	// Spot-check one file's contents are identical.
	pkgA, _ := store.Read("a", "package.json")
	pkgB, _ := store.Read("b", "package.json")
	if string(pkgA) != string(pkgB) || len(pkgA) == 0 {
		t.Error("package.json differs between applyTemplate and fork — they should walk the same FS")
	}
}

func TestEmbeddedTemplateNames_IncludesNextjs(t *testing.T) {
	names := embeddedTemplateNames()
	found := false
	for _, n := range names {
		if n == "nextjs" {
			found = true
		}
	}
	if !found {
		t.Errorf("embeddedTemplateNames should include nextjs, got %v", names)
	}
}
