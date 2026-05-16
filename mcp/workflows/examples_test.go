package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestExamplesParse: every workflow file under examples/ must parse
// and pass Validate(). Catches docs drifting from the schema.
func TestExamplesParse(t *testing.T) {
	entries, err := os.ReadDir("examples")
	if err != nil {
		t.Fatalf("read examples/: %v", err)
	}
	var found int
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".yaml") {
			continue
		}
		found++
		t.Run(e.Name(), func(t *testing.T) {
			src, err := os.ReadFile(filepath.Join("examples", e.Name()))
			if err != nil {
				t.Fatalf("read: %v", err)
			}
			def, err := ParseDefinition(src)
			if err != nil {
				t.Fatalf("ParseDefinition: %v", err)
			}
			if err := def.Validate(); err != nil {
				t.Fatalf("Validate: %v", err)
			}
		})
	}
	if found == 0 {
		t.Fatal("no examples/*.yaml found")
	}
}

// TestExampleTablesInsert_RunsAgainstStub: the simplest example
// actually executes end-to-end — drives its app step against the
// stub platform and confirms the templated email reached
// tables/rows_insert verbatim.
func TestExampleTablesInsert_RunsAgainstStub(t *testing.T) {
	src, err := os.ReadFile("examples/tables-insert.yaml")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	plat := &stubPlatform{
		results: map[string]any{
			"tables:rows_insert": map[string]any{"ids": []any{int64(1)}, "inserted": 1},
		},
	}
	ctx := newRunCtx(t, plat)
	wf := mustCreateWorkflow(t, ctx, string(src))

	run, err := RunWorkflow(context.Background(), ctx, testProj, wf,
		map[string]any{"email": "marco@example.com"},
		runOptions{triggerKind: "manual"})
	if err != nil {
		t.Fatalf("RunWorkflow: %v", err)
	}
	if run.Status != "completed" {
		t.Fatalf("status = %q (steps=%+v)", run.Status, run.Steps)
	}
	if len(plat.calls) != 1 {
		t.Fatalf("calls = %d, want 1", len(plat.calls))
	}
	call := plat.calls[0]
	if call.app != "tables" || call.tool != "rows_insert" {
		t.Errorf("call = %s/%s, want tables/rows_insert", call.app, call.tool)
	}
	rows, _ := call.input["rows"].([]any)
	if len(rows) != 1 {
		t.Fatalf("rows = %v", call.input["rows"])
	}
	row, _ := rows[0].(map[string]any)
	if row["email"] != "marco@example.com" {
		t.Errorf("templated email did not flow through; row = %v", row)
	}
}

// TestExampleLeadCapture_SkipsExisting: the branch step ends the run
// early when the count step reports a duplicate, so the insert step
// is never called.
func TestExampleLeadCapture_SkipsExisting(t *testing.T) {
	src, err := os.ReadFile("examples/lead-capture.yaml")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	plat := &stubPlatform{
		results: map[string]any{
			"tables:rows_count": map[string]any{"count": 1}, // already exists
		},
	}
	ctx := newRunCtx(t, plat)
	wf := mustCreateWorkflow(t, ctx, string(src))

	run, err := RunWorkflow(context.Background(), ctx, testProj, wf,
		map[string]any{"email": "marco@example.com", "source": "homepage"},
		runOptions{triggerKind: "manual"})
	if err != nil {
		t.Fatalf("RunWorkflow: %v", err)
	}
	if run.Status != "completed" {
		t.Fatalf("status = %q (steps=%+v)", run.Status, run.Steps)
	}
	// Only the count step should have been called.
	if len(plat.calls) != 1 || plat.calls[0].tool != "rows_count" {
		t.Errorf("expected only rows_count to be called; got %+v", plat.calls)
	}
}

// TestExampleLeadCapture_InsertsWhenAbsent: count returns 0 → branch
// falls through → insert fires with the templated source.
func TestExampleLeadCapture_InsertsWhenAbsent(t *testing.T) {
	src, err := os.ReadFile("examples/lead-capture.yaml")
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	plat := &stubPlatform{
		results: map[string]any{
			"tables:rows_count":  map[string]any{"count": 0},
			"tables:rows_insert": map[string]any{"ids": []any{int64(7)}, "inserted": 1},
		},
	}
	ctx := newRunCtx(t, plat)
	wf := mustCreateWorkflow(t, ctx, string(src))

	run, err := RunWorkflow(context.Background(), ctx, testProj, wf,
		map[string]any{"email": "new@example.com", "source": "homepage"},
		runOptions{triggerKind: "manual"})
	if err != nil {
		t.Fatalf("RunWorkflow: %v", err)
	}
	if run.Status != "completed" {
		t.Fatalf("status = %q (steps=%+v)", run.Status, run.Steps)
	}
	// rows_count then rows_insert.
	if len(plat.calls) != 2 {
		t.Fatalf("calls = %d, want 2 (%+v)", len(plat.calls), plat.calls)
	}
	if plat.calls[1].tool != "rows_insert" {
		t.Errorf("second call = %s, want rows_insert", plat.calls[1].tool)
	}
	rows, _ := plat.calls[1].input["rows"].([]any)
	row, _ := rows[0].(map[string]any)
	if row["source"] != "homepage" {
		t.Errorf("templated source did not flow through; row = %v", row)
	}
}
