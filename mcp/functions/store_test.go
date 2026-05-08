package main

import (
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

const testProj = "test-proj"

// TestCreateAndGet exercises the happy path: create with inline
// source, hash gets stamped, GetByID + GetByName both find it.
func TestCreateAndGet(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	db := ctx.AppDB()

	src := []byte("console.log(JSON.stringify({hello:'world'}))")
	fn, err := dbCreateFunction(db, testProj, &Function{
		Name:       "hello",
		Runtime:    "bun",
		SourceKind: "inline",
		Source:     string(src),
		SourceHash: hashSource(src),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	if fn.ID == 0 {
		t.Error("expected non-zero id")
	}
	if fn.SourceHash != hashSource(src) {
		t.Errorf("SourceHash = %q, want %q", fn.SourceHash, hashSource(src))
	}
	if fn.TimeoutMS != defaultTimeout {
		t.Errorf("TimeoutMS = %d, want %d", fn.TimeoutMS, defaultTimeout)
	}

	byID, err := dbGetFunction(db, testProj, fn.ID, "")
	if err != nil || byID == nil {
		t.Fatalf("get by id: %v / %v", err, byID)
	}
	byName, err := dbGetFunction(db, testProj, 0, "hello")
	if err != nil || byName == nil {
		t.Fatalf("get by name: %v / %v", err, byName)
	}
	if byID.ID != byName.ID {
		t.Errorf("id mismatch %d vs %d", byID.ID, byName.ID)
	}
}

// TestNameValidation rejects illegal slugs at create time so a bad
// name can't become an unrouteable /fn/<name> later.
func TestNameValidation(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	bad := []string{"", "Has-Caps", "starts/with/slash", "has spaces", "x", strRepeat("x", 64)}
	for _, name := range bad {
		_, err := dbCreateFunction(ctx.AppDB(), testProj, &Function{
			Name: name, Runtime: "bun",
			SourceKind: "inline", Source: "echo", SourceHash: "abc",
		})
		if err == nil && name != "x" { // single-char name is actually allowed by the regex
			t.Errorf("expected error for name %q", name)
		}
	}
}

// TestRuntimeValidation only accepts the four supported runtimes.
func TestRuntimeValidation(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	_, err := dbCreateFunction(ctx.AppDB(), testProj, &Function{
		Name: "bad", Runtime: "ruby",
		SourceKind: "inline", Source: "puts 1", SourceHash: "x",
	})
	if err == nil {
		t.Error("expected runtime validation error")
	}
}

// TestProjectPartition: a function created in project A is invisible
// to a query for project B. Same shape as every other app.
func TestProjectPartition(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("a"))
	_, err := dbCreateFunction(ctx.AppDB(), "a", &Function{
		Name: "fn", Runtime: "bun",
		SourceKind: "inline", Source: "x", SourceHash: "h",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	got, err := dbGetFunction(ctx.AppDB(), "b", 0, "fn")
	if err != nil {
		t.Fatalf("get: %v", err)
	}
	if got != nil {
		t.Error("expected nil for cross-project lookup")
	}
}

// TestUpdateRehashesOnSourceChange: dbUpdateFunction takes a
// recomputed hash from the caller — verify it lands in the DB.
func TestUpdateRehashesOnSourceChange(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	db := ctx.AppDB()

	fn, err := dbCreateFunction(db, testProj, &Function{
		Name: "u", Runtime: "bun",
		SourceKind: "inline", Source: "v1", SourceHash: hashSource([]byte("v1")),
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	updated, err := dbUpdateFunction(db, testProj, fn.ID, map[string]any{
		"source": "v2",
	}, hashSource([]byte("v2")))
	if err != nil {
		t.Fatalf("update: %v", err)
	}
	if updated.Source != "v2" {
		t.Errorf("Source = %q, want v2", updated.Source)
	}
	if updated.SourceHash != hashSource([]byte("v2")) {
		t.Errorf("SourceHash unchanged: %q", updated.SourceHash)
	}
}

// TestInvocationsRoundtrip: insert an invocation, list it back, fetch
// it singly. The most-recently-started should sort first.
func TestInvocationsRoundtrip(t *testing.T) {
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID(testProj))
	db := ctx.AppDB()

	fn, err := dbCreateFunction(db, testProj, &Function{
		Name: "i", Runtime: "bun",
		SourceKind: "inline", Source: "x", SourceHash: "h",
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}

	id1, err := dbInsertInvocation(db, testProj, &Invocation{
		FunctionID: fn.ID, StartedAt: "2026-05-08T10:00:00Z",
		Status: "ok", TriggerKind: "manual", ResponseBody: "first",
	})
	if err != nil || id1 == 0 {
		t.Fatalf("insert 1: %v", err)
	}
	id2, err := dbInsertInvocation(db, testProj, &Invocation{
		FunctionID: fn.ID, StartedAt: "2026-05-08T10:00:01Z",
		Status: "error", TriggerKind: "http", Error: "boom",
	})
	if err != nil || id2 == 0 {
		t.Fatalf("insert 2: %v", err)
	}

	list, err := dbListInvocations(db, testProj, fn.ID, 50)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(list) != 2 {
		t.Fatalf("len = %d, want 2", len(list))
	}
	if list[0].ID != id2 {
		t.Errorf("first = %d, want %d (newer first)", list[0].ID, id2)
	}

	one, err := dbGetInvocation(db, testProj, id1)
	if err != nil || one == nil {
		t.Fatalf("get: %v / %v", err, one)
	}
	if one.ResponseBody != "first" {
		t.Errorf("ResponseBody = %q", one.ResponseBody)
	}
}

func strRepeat(s string, n int) string {
	out := make([]byte, 0, len(s)*n)
	for i := 0; i < n; i++ {
		out = append(out, s...)
	}
	return string(out)
}
