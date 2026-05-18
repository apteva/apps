package main

// Tier 1 tests — every MCP tool handler exercised against an
// in-memory SQLite. Fast (whole suite <1s), runs on every commit.

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// ─── Helpers ────────────────────────────────────────────────────────

// newTestCtx returns a fresh *sdk.AppCtx with the manifest loaded,
// migrations applied, and APTEVA_PROJECT_ID="test-proj" set. Tests
// that need a different project_id pass extra options.
func newTestCtx(t *testing.T, opts ...tk.Option) *sdk.AppCtx {
	t.Helper()
	full := append([]tk.Option{tk.WithProjectID("test-proj")}, opts...)
	return tk.NewAppCtx(t, "apteva.yaml", full...)
}

func mustCreate(t *testing.T, ctx *sdk.AppCtx, args map[string]any) *Contact {
	t.Helper()
	app := &App{}
	out, err := app.toolCreate(ctx, args)
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	return out.(map[string]any)["contact"].(*Contact)
}

// ─── Upsert / find-or-create ────────────────────────────────────────

func TestUpsertByChannel_CreatesThenReuses(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}

	out1, err := app.toolUpsertByChannel(ctx, map[string]any{
		"kind":  "email",
		"value": "alice@example.com",
		"defaults": map[string]any{
			"first_name": "Alice",
			"last_name":  "Cooper",
		},
		"source": "agent:1",
	})
	if err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	res1 := out1.(map[string]any)
	if res1["was_created"] != true {
		t.Fatalf("expected was_created=true on first call, got %#v", res1["was_created"])
	}
	c1 := res1["contact"].(*Contact)
	if c1.PrimaryEmail != "alice@example.com" {
		t.Errorf("primary_email=%q, want alice@example.com", c1.PrimaryEmail)
	}

	// Second call with case-folded value should hit the same row.
	out2, err := app.toolUpsertByChannel(ctx, map[string]any{
		"kind":  "email",
		"value": "ALICE@example.com",
	})
	if err != nil {
		t.Fatalf("second upsert: %v", err)
	}
	res2 := out2.(map[string]any)
	if res2["was_created"] != false {
		t.Errorf("expected was_created=false on second call, got %#v", res2["was_created"])
	}
	c2 := res2["contact"].(*Contact)
	if c2.ID != c1.ID {
		t.Errorf("expected same contact id (%d), got %d", c1.ID, c2.ID)
	}
}

// ─── Project-scope safety ───────────────────────────────────────────

func TestCreate_RejectsWithoutProjectID_GlobalScope(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "")
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	app := &App{}
	_, err := app.toolCreate(ctx, map[string]any{
		"first_name": "Bob",
	})
	if err == nil {
		t.Fatal("expected error when project_id is missing in global scope")
	}
	if !strings.Contains(err.Error(), "project_id") {
		t.Errorf("error %q should mention project_id", err.Error())
	}
}

// ─── Search ─────────────────────────────────────────────────────────

func TestSearch_FreeTextMatchesNameAndEmail(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}

	mustCreate(t, ctx, map[string]any{
		"first_name": "Alice", "last_name": "Cooper",
		"channels": []any{map[string]any{"kind": "email", "value": "alice@acme.com", "is_primary": true}},
	})
	mustCreate(t, ctx, map[string]any{
		"first_name": "Bob", "last_name": "Dylan",
		"channels": []any{map[string]any{"kind": "email", "value": "bob@example.org", "is_primary": true}},
	})
	mustCreate(t, ctx, map[string]any{
		"first_name": "Charlie", "last_name": "Parker",
		"company":    "Acme",
	})

	cases := []struct {
		q    string
		want int
	}{
		{"alice", 1},
		{"acme", 2}, // matches Alice's email + Charlie's company
		{"dylan", 1},
		{"nonexistent", 0},
	}
	for _, c := range cases {
		t.Run(c.q, func(t *testing.T) {
			out, err := app.toolSearch(ctx, map[string]any{"q": c.q})
			if err != nil {
				t.Fatalf("search: %v", err)
			}
			got := out.(map[string]any)["count"].(int)
			if got != c.want {
				t.Errorf("q=%q got %d, want %d", c.q, got, c.want)
			}
		})
	}
}

func TestSearch_StructuredFilterEq(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	mustCreate(t, ctx, map[string]any{"first_name": "Alice", "company": "Acme"})
	mustCreate(t, ctx, map[string]any{"first_name": "Bob", "company": "Globex"})

	out, err := app.toolSearch(ctx, map[string]any{
		"filters": []any{
			map[string]any{"field": "company", "op": "eq", "value": "Globex"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	got := out.(map[string]any)["count"].(int)
	if got != 1 {
		t.Errorf("got %d, want 1", got)
	}
}

// ─── Update ─────────────────────────────────────────────────────────

func TestUpdate_PartialPatch(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	c := mustCreate(t, ctx, map[string]any{"first_name": "Alice"})

	_, err := app.toolUpdate(ctx, map[string]any{
		"id": c.ID,
		"patch": map[string]any{
			"job_title": "Engineering Manager",
		},
		"source": "human:1",
	})
	if err != nil {
		t.Fatal(err)
	}
	out, _ := app.toolGet(ctx, map[string]any{"id": c.ID})
	got := out.(map[string]any)["contact"].(*Contact)
	if got.JobTitle != "Engineering Manager" {
		t.Errorf("job_title=%q", got.JobTitle)
	}
}

// v0.5.4 regression: contacts_update's patch.attributes was silently
// dropped — the tool returned success but nothing was written. Backfill
// scripts thought 55 attrs landed; zero actually did. Now the patch
// routes attributes through dbSetAttribute, surfacing any missing-def
// errors per item.
func TestUpdate_PatchAttributesWritesValues(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}

	// Attribute defs must exist before write.
	if _, err := app.toolDefineAttribute(ctx, map[string]any{"key": "tier", "label": "Tier", "type": "text"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolDefineAttribute(ctx, map[string]any{"key": "lifecycle", "label": "Lifecycle", "type": "text"}); err != nil {
		t.Fatal(err)
	}
	c := mustCreate(t, ctx, map[string]any{"first_name": "Alice"})

	if _, err := app.toolUpdate(ctx, map[string]any{
		"id": c.ID,
		"patch": map[string]any{
			"job_title":  "Director",
			"attributes": []any{
				map[string]any{"key": "tier", "value": "gold"},
				map[string]any{"key": "lifecycle", "value": "customer"},
			},
		},
		"source": "human:1",
	}); err != nil {
		t.Fatal(err)
	}

	out, _ := app.toolGetContext(ctx, map[string]any{"id": c.ID})
	got := out.(map[string]any)["contact"].(*Contact)
	if got.JobTitle != "Director" {
		t.Errorf("scalar field not updated: job_title=%q", got.JobTitle)
	}
	if len(got.Attributes) != 2 {
		t.Fatalf("expected 2 attributes persisted, got %d", len(got.Attributes))
	}
	seen := map[string]any{}
	for _, a := range got.Attributes {
		seen[a.Key] = a.Value
	}
	if seen["tier"] != "gold" || seen["lifecycle"] != "customer" {
		t.Errorf("attribute values not persisted: %+v", seen)
	}
}

func TestUpdate_PatchAttributesSurfacesMissingDef(t *testing.T) {
	// No define_attribute call: the write should error explicitly,
	// not silently no-op. The reporter's bug class: scripts thought
	// they succeeded because the tool returned no error.
	ctx := newTestCtx(t)
	app := &App{}
	c := mustCreate(t, ctx, map[string]any{"first_name": "Alice"})
	_, err := app.toolUpdate(ctx, map[string]any{
		"id": c.ID,
		"patch": map[string]any{
			"attributes": []any{
				map[string]any{"key": "nonexistent", "value": "x"},
			},
		},
	})
	if err == nil {
		t.Fatal("expected error when writing to undefined attribute")
	}
	if !strings.Contains(err.Error(), "not defined") {
		t.Errorf("error should point at the missing def, got %v", err)
	}
}

func TestUpdate_RejectsUnknownPatchField(t *testing.T) {
	// Catches typos like 'firts_name' before they become silent
	// drops. Pre-v0.5.4 this would have returned success without
	// writing anything.
	ctx := newTestCtx(t)
	app := &App{}
	c := mustCreate(t, ctx, map[string]any{"first_name": "Alice"})
	_, err := app.toolUpdate(ctx, map[string]any{
		"id":    c.ID,
		"patch": map[string]any{"firts_name": "Bob"},
	})
	if err == nil {
		t.Fatal("expected error for unknown patch field")
	}
	if !strings.Contains(err.Error(), "unknown patch field") {
		t.Errorf("expected unknown-field error, got %v", err)
	}
}

func TestCreate_AttributesAreWritten(t *testing.T) {
	// Bug found while fixing the update path: contacts_create's
	// schema advertised attributes as an input but dbCreate dropped
	// the field. Fixed alongside.
	ctx := newTestCtx(t)
	app := &App{}
	if _, err := app.toolDefineAttribute(ctx, map[string]any{"key": "tier", "label": "Tier", "type": "text"}); err != nil {
		t.Fatal(err)
	}
	out, err := app.toolCreate(ctx, map[string]any{
		"first_name": "Alice",
		"attributes": []any{
			map[string]any{"key": "tier", "value": "gold"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	c := out.(map[string]any)["contact"].(*Contact)
	got, _ := app.toolGetContext(ctx, map[string]any{"id": c.ID})
	contact := got.(map[string]any)["contact"].(*Contact)
	if len(contact.Attributes) != 1 || contact.Attributes[0].Value != "gold" {
		t.Errorf("create-time attribute not persisted: %+v", contact.Attributes)
	}
}

func TestHandleHTTPContactItem_AcceptsPUT(t *testing.T) {
	// Bug 2: PUT /contacts/<id> used to 405. Accepts now (same
	// partial-patch handler as PATCH). The HTTP body is the patch
	// directly (not {"patch":{...}}) — same shape handleHTTPUpdate
	// has always expected.
	ctx := newTestCtx(t)
	globalCtx = ctx // getAppCtx(r) reads this; tests don't auto-wire it
	app := &App{}
	c := mustCreate(t, ctx, map[string]any{"first_name": "Alice"})

	body := bytes.NewBufferString(`{"job_title":"VP"}`)
	r := httptest.NewRequest("PUT", "/contacts/"+strconv.FormatInt(c.ID, 10)+"?project_id=test-proj", body)
	w := httptest.NewRecorder()
	app.handleHTTPContactItem(w, r)
	if w.Code != http.StatusOK {
		t.Fatalf("PUT status=%d, body=%s", w.Code, w.Body.String())
	}
	got, _ := app.toolGet(ctx, map[string]any{"id": c.ID})
	if got.(map[string]any)["contact"].(*Contact).JobTitle != "VP" {
		t.Errorf("PUT didn't apply patch")
	}
}

// ─── Activities ─────────────────────────────────────────────────────

func TestLogActivity_BumpsLastContactAt(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	c := mustCreate(t, ctx, map[string]any{"first_name": "Alice"})

	_, err := app.toolLogActivity(ctx, map[string]any{
		"contact_id":  c.ID,
		"kind":        "call",
		"body":        "Discussed Q2 plan",
		"occurred_at": "2026-04-28T10:00:00Z",
		"source":      "human:1",
	})
	if err != nil {
		t.Fatal(err)
	}

	out, _ := app.toolGetContext(ctx, map[string]any{"id": c.ID})
	res := out.(map[string]any)
	acts := res["activities"].([]*Activity)
	if len(acts) != 1 {
		t.Fatalf("got %d activities, want 1", len(acts))
	}
	if acts[0].Kind != "call" {
		t.Errorf("kind=%q", acts[0].Kind)
	}
	got := res["contact"].(*Contact)
	if got.LastContactAt == "" {
		t.Errorf("last_contact_at should have been bumped")
	}
}

// ─── Merge ──────────────────────────────────────────────────────────

func TestMerge_AbsorbsLoserChannels(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}

	loser := mustCreate(t, ctx, map[string]any{
		"first_name": "Alice",
		"channels": []any{map[string]any{"kind": "email", "value": "alice@home.com", "is_primary": true}},
	})
	winner := mustCreate(t, ctx, map[string]any{
		"first_name": "Alice",
		"channels": []any{map[string]any{"kind": "email", "value": "alice@work.com", "is_primary": true}},
	})

	_, err := app.toolMerge(ctx, map[string]any{
		"loser_id":  loser.ID,
		"winner_id": winner.ID,
		"notes":     "duplicate from inbound form",
		"source":    "human:1",
	})
	if err != nil {
		t.Fatalf("merge: %v", err)
	}

	out, _ := app.toolGet(ctx, map[string]any{"id": winner.ID})
	w := out.(map[string]any)["contact"].(*Contact)
	gotEmails := []string{}
	for _, ch := range w.Channels {
		if ch.Kind == "email" {
			gotEmails = append(gotEmails, ch.Value)
		}
	}
	if len(gotEmails) != 2 {
		t.Errorf("winner emails=%v, want 2", gotEmails)
	}

	out2, _ := app.toolGet(ctx, map[string]any{"id": loser.ID})
	l := out2.(map[string]any)["contact"].(*Contact)
	if l.Status != "merged" {
		t.Errorf("loser status=%q, want merged", l.Status)
	}
}

// ─── Custom attributes ──────────────────────────────────────────────

func TestDefineAndSetAttribute(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}

	c := mustCreate(t, ctx, map[string]any{"first_name": "Alice"})

	_, err := app.toolDefineAttribute(ctx, map[string]any{
		"key": "renewal_date", "label": "Renewal date", "type": "date",
	})
	if err != nil {
		t.Fatal(err)
	}
	_, err = app.toolSetAttribute(ctx, map[string]any{
		"contact_id": c.ID,
		"key":        "renewal_date",
		"value":      "2026-12-31",
		"source":     "agent:42",
	})
	if err != nil {
		t.Fatal(err)
	}

	out, _ := app.toolGetContext(ctx, map[string]any{"id": c.ID})
	contact := out.(map[string]any)["contact"].(*Contact)
	if len(contact.Attributes) != 1 {
		t.Fatalf("attributes=%d, want 1", len(contact.Attributes))
	}
	got := contact.Attributes[0]
	if got.Key != "renewal_date" || got.Value != "2026-12-31" {
		t.Errorf("attribute=%+v", got)
	}
	if got.Source != "agent:42" {
		t.Errorf("provenance lost: source=%q", got.Source)
	}
}

func TestSetAttribute_RejectsUndefinedKey(t *testing.T) {
	ctx := newTestCtx(t)
	app := &App{}
	c := mustCreate(t, ctx, map[string]any{"first_name": "Alice"})
	_, err := app.toolSetAttribute(ctx, map[string]any{
		"contact_id": c.ID, "key": "no_such_key", "value": "x",
	})
	if err == nil {
		t.Fatal("expected error for undefined attribute key")
	}
	if !strings.Contains(err.Error(), "not defined") {
		t.Errorf("error %q should mention 'not defined'", err.Error())
	}
}
