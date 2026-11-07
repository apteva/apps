package main

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

func TestResultsMergePreservesLedgerAndRejectsStaleRevision(t *testing.T) {
	a, c := testApp(t, nil)
	i := create(t, a, c, map[string]any{"title": "History"})
	v, e := a.dispatch(c, "releases_create", map[string]any{"item_id": i.ID, "channel": "Example", "results": map[string]any{"history": []any{map[string]any{"original": "exact\nparagraph"}}, "execution": map[string]any{"accepted_id": 123, "blocker": "old", "nested": map[string]any{"retained": true}}}})
	if e != nil {
		t.Fatal(e)
	}
	r := v.(Release)
	patch := map[string]any{"execution": map[string]any{"blocker": nil, "nested": map[string]any{"new": true}, "operations": map[string]any{"124": map[string]any{"state": "accepted"}}}}
	v, e = a.dispatch(c, "releases_merge_results", map[string]any{"id": r.ID, "revision": r.Revision, "merge": patch})
	if e != nil {
		t.Fatal(e)
	}
	ack := v.(map[string]any)
	if _, ok := ack["results"]; ok {
		t.Fatal("unbounded results returned")
	}
	if ack["revision"] != int64(2) {
		t.Fatal(ack)
	}
	got, e := readRelease(c.AppDB(), "alpha", r.ID)
	if e != nil {
		t.Fatal(e)
	}
	ex := got.Results["execution"].(map[string]any)
	if ex["accepted_id"] != float64(123) || ex["blocker"] != nil || ex["nested"].(map[string]any)["retained"] != true {
		t.Fatal(ex)
	}
	raw, _ := json.Marshal(got.Results["history"])
	if !strings.Contains(string(raw), `exact\nparagraph`) {
		t.Fatal(string(raw))
	}
	if _, e = a.dispatch(c, "releases_merge_results", map[string]any{"id": r.ID, "revision": r.Revision, "merge": map[string]any{"lost": "update"}}); !errors.Is(e, errConflict) {
		t.Fatal("stale merge", e)
	}
	if _, e = mergeReleaseResults(c.AppDB(), "other", r.ID, got.Revision, patch); e == nil {
		t.Fatal("cross project read succeeded")
	}
	if _, e = mergeReleaseResults(c.AppDB(), "alpha", r.ID, got.Revision, map[string]any{"large": strings.Repeat("x", 65536)}); e == nil {
		t.Fatal("oversized patch accepted")
	}
}
func TestCompactReadsExcludeHistoryAndProjectOnlyRequestedKeys(t *testing.T) {
	a, c := testApp(t, nil)
	i := create(t, a, c, map[string]any{"title": "Large history", "fields": map[string]any{"source": "asset1", "large": strings.Repeat("x", 120000)}})
	v, e := a.dispatch(c, "releases_create", map[string]any{"item_id": i.ID, "channel": "Example", "results": map[string]any{"history": strings.Repeat("x", 120000), "execution": map[string]any{"accepted_id": 123}}})
	if e != nil {
		t.Fatal(e)
	}
	r := v.(Release)
	v, e = a.dispatch(c, "items_get", map[string]any{"id": i.ID, "include_history": false, "include_results": false, "field_keys": []any{"source"}})
	if e != nil {
		t.Fatal(e)
	}
	b, _ := json.Marshal(v)
	if len(b) > 2000 || strings.Contains(string(b), strings.Repeat("x", 100)) {
		t.Fatal("unbounded read", len(b))
	}
	m := v.(map[string]any)
	if len(m["history"].([]map[string]any)) != 0 {
		t.Fatal("history included")
	}
	if m["item"].(map[string]any)["fields"].(map[string]any)["source"] != "asset1" {
		t.Fatal(m)
	}
	v, e = a.dispatch(c, "releases_get", map[string]any{"id": r.ID, "results_keys": []any{"execution"}})
	if e != nil {
		t.Fatal(e)
	}
	b, _ = json.Marshal(v)
	if len(b) > 1000 || strings.Contains(string(b), "history") {
		t.Fatal(string(b))
	}
	if v.(map[string]any)["results"].(map[string]any)["execution"] == nil {
		t.Fatal(v)
	}
	v, e = a.dispatch(c, "releases_get", map[string]any{"id": r.ID})
	if e != nil {
		t.Fatal(e)
	}
	if _, ok := v.(map[string]any)["results"]; ok {
		t.Fatal("results not opt-in")
	}
	v, e = a.dispatch(c, "items_get", map[string]any{"id": i.ID})
	if e != nil {
		t.Fatal(e)
	}
	if len(v.(map[string]any)["history"].([]map[string]any)) == 0 {
		t.Fatal("legacy history changed")
	}
}
func TestMergeNullArraysAndLegacyResultsReplacement(t *testing.T) {
	old := map[string]any{"obj": map[string]any{"keep": true, "drop": "a"}, "list": []any{1, 2}}
	got := mergeResultObject(old, map[string]any{"obj": map[string]any{"drop": nil}, "list": []any{3}})
	if old["obj"].(map[string]any)["drop"] != "a" {
		t.Fatal("input mutated")
	}
	if got["obj"].(map[string]any)["keep"] != true || got["obj"].(map[string]any)["drop"] != nil || len(got["list"].([]any)) != 1 {
		t.Fatal(got)
	}
}
func TestReadProjectionRejectsInvalidOptions(t *testing.T) {
	for _, a := range []map[string]any{{"include_history": "maybe"}, {"field_keys": "source"}, {"results_keys": []any{123}}} {
		if e := validateReadOptions(a); e == nil {
			t.Fatal("invalid projection accepted", a)
		}
	}
}
