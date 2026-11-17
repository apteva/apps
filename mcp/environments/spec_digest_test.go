package main

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type seedCapturePlatform struct {
	*auditPlatform
	inputs []map[string]any
}

func (p *seedCapturePlatform) CallRuntimeAppResult(_, _, _ string, input map[string]any, _ any) error {
	p.inputs = append(p.inputs, input)
	return p.seedErr
}

func pinnedTestApp(t *testing.T) (*App, *seedCapturePlatform) {
	t.Helper()
	s, base := auditService(t)
	p := &seedCapturePlatform{auditPlatform: base}
	s.ctx = sdk.NewAppCtxForTest(nil, s.db.db, nil, p, nil).WithProject("audit")
	return &App{svc: s}, p
}

// jsonArgs decodes like an MCP request: numbers arrive as float64.
func jsonArgs(t *testing.T, raw string) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal([]byte(raw), &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func toJSONMap(t *testing.T, v any) map[string]any {
	t.Helper()
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestSpecDigestIsCanonical(t *testing.T) {
	sum := sha256.Sum256([]byte(`{"a":{"x":1,"y":[2,"z"]},"b":10000000000000000000001}`))
	want := hex.EncodeToString(sum[:])
	for _, raw := range []string{
		`{"b":10000000000000000000001,"a":{"y":[2,"z"],"x":1}}`,
		"{ \"a\" : { \"x\" : 1 , \"y\" : [ 2 , \"z\" ] } ,\n \"b\" : 10000000000000000000001 }",
	} {
		got, err := specDigest([]byte(raw))
		if err != nil || got != want {
			t.Fatalf("digest(%s)=%s err=%v, want %s", raw, got, err, want)
		}
	}
	// UseNumber keeps number text exactly; it never rounds through float64.
	other, _ := specDigest([]byte(`{"a":{"x":1,"y":[2,"z"]},"b":10000000000000000000000}`))
	if other == want {
		t.Fatal("large integers collapsed through float64")
	}
	if _, err := specDigest([]byte(`{"a":1} {}`)); err == nil {
		t.Fatal("accepted trailing data")
	}
}

func TestDefinitionRepliesReportStableSpecDigest(t *testing.T) {
	app, _ := pinnedTestApp(t)
	// The same spec with different key order and number spelling.
	one, err := app.toolSave(false)(nil, jsonArgs(t, `{"id":"env_one","name":"One","spec":{"ttl_seconds":3600,"seeds":[{"app":"shop","tool":"seed","input":{"n":1.0,"m":1e2,"nested":{"b":true,"a":"x"}}}]}}`))
	if err != nil {
		t.Fatal(err)
	}
	two, err := app.toolSave(false)(nil, jsonArgs(t, `{"spec":{"seeds":[{"input":{"nested":{"a":"x","b":true},"m":100,"n":1},"tool":"seed","app":"shop"}],"ttl_seconds":3.6e3},"name":"Two","id":"env_two"}`))
	if err != nil {
		t.Fatal(err)
	}
	d1, d2 := one.(*Definition), two.(*Definition)
	if !specDigestPattern.MatchString(d1.SpecDigest) || d1.SpecDigest != d2.SpecDigest {
		t.Fatalf("create digests differ: %q %q", d1.SpecDigest, d2.SpecDigest)
	}
	if reply := toJSONMap(t, d1); reply["spec_digest"] != d1.SpecDigest {
		t.Fatalf("create reply=%v", reply)
	}
	var stored string
	if err := app.svc.db.db.QueryRow(`SELECT spec_json FROM environment_definitions WHERE id='env_one'`).Scan(&stored); err != nil {
		t.Fatal(err)
	}
	if want, _ := specDigest([]byte(stored)); want != d1.SpecDigest {
		t.Fatalf("digest %s does not describe stored spec (%s)", d1.SpecDigest, want)
	}
	got, err := app.toolGet(nil, map[string]any{"id": "env_one"})
	if err != nil || got.(*Definition).SpecDigest != d1.SpecDigest {
		t.Fatalf("get=%+v err=%v", got, err)
	}
	page, err := app.toolList(nil, map[string]any{})
	if err != nil {
		t.Fatal(err)
	}
	items := page.(*EnvironmentPage).Items
	if len(items) != 2 || items[0].SpecDigest != d1.SpecDigest || items[1].SpecDigest != d1.SpecDigest {
		t.Fatalf("list items=%+v", items)
	}
	// Renaming leaves the spec and its digest alone; a spec edit changes it.
	renamed, err := app.toolSave(true)(nil, map[string]any{"id": "env_one", "name": "Renamed", "spec_digest": "ignored"})
	if err != nil || renamed.(*Definition).SpecDigest != d1.SpecDigest {
		t.Fatalf("rename=%+v err=%v", renamed, err)
	}
	updated, err := app.toolSave(true)(nil, map[string]any{"id": "env_one", "spec": map[string]any{"ttl_seconds": 7200.0}})
	if err != nil {
		t.Fatal(err)
	}
	if digest := updated.(*Definition).SpecDigest; digest == d1.SpecDigest || !specDigestPattern.MatchString(digest) {
		t.Fatalf("update digest=%q", digest)
	}
}

func TestSpecDigestBackfillForLegacyDefinitions(t *testing.T) {
	s := testStore(t)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	raw := `{"version":1,"ttl_seconds":600}`
	if _, err := s.db.Exec(`INSERT INTO environment_definitions(id,name,spec_json,created_at,updated_at) VALUES('legacy','Legacy',?,?,?)`, raw, now, now); err != nil {
		t.Fatal(err)
	}
	svc := &service{db: s}
	page, err := svc.listDefinitions(ListOptions{})
	if err != nil || len(page.Items) != 1 || page.Items[0].SpecDigest != "" {
		t.Fatalf("pre-backfill page=%+v err=%v", page, err)
	}
	if filled, err := s.backfillSpecDigests(); err != nil || filled != 1 {
		t.Fatalf("filled=%d err=%v", filled, err)
	}
	want, _ := specDigest([]byte(raw))
	page, err = svc.listDefinitions(ListOptions{})
	if err != nil || page.Items[0].SpecDigest != want {
		t.Fatalf("post-backfill page=%+v err=%v", page, err)
	}
	if filled, err := s.backfillSpecDigests(); err != nil || filled != 0 {
		t.Fatalf("second backfill filled=%d err=%v", filled, err)
	}
}

func savePinnedDefinition(t *testing.T, app *App, seeds []SeedStep) *Definition {
	t.Helper()
	saved, err := app.svc.saveDefinition(&Definition{ID: "env_x", Name: "X", SpecVersion: 3, Spec: EnvironmentSpec{Version: 1, TTLSeconds: 600, Seeds: seeds}})
	if err != nil {
		t.Fatal(err)
	}
	return saved
}

func TestPinnedRunMismatchMessages(t *testing.T) {
	app, p := pinnedTestApp(t)
	d := savePinnedDefinition(t, app, nil)
	stale := strings.Repeat("ab", 32)
	for name, tc := range map[string]struct {
		version int
		digest  string
		want    string
	}{
		"version": {2, d.SpecDigest, fmt.Sprintf("environment env_x: pinned v2/%s, found v3/%s", d.SpecDigest[:12], d.SpecDigest[:12])},
		"digest":  {3, stale, fmt.Sprintf("environment env_x: pinned v3/abababababab, found v3/%s", d.SpecDigest[:12])},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := app.toolRunCreate(nil, map[string]any{"kind": "test", "environment_id": "env_x", "spec_version": float64(tc.version), "spec_digest": tc.digest})
			if err == nil || err.Error() != tc.want {
				t.Fatalf("error=%v, want %s", err, tc.want)
			}
		})
	}
	_, err := app.toolRunCreate(nil, map[string]any{"environment_id": "env_missing", "spec_version": 1.0, "spec_digest": stale})
	if err == nil || err.Error() != "environment env_missing not found" {
		t.Fatalf("missing error=%v", err)
	}
	if p.created != 0 {
		t.Fatalf("mismatched pins created %d runtimes", p.created)
	}
}

func TestPinnedRunCreatesFromStoredSpecWithSeedVars(t *testing.T) {
	app, p := pinnedTestApp(t)
	d := savePinnedDefinition(t, app, []SeedStep{{App: "shop", Tool: "seed", Input: map[string]any{
		"digest":    "{{candidate_digest}}",
		"label":     "build {{candidate_digest}} on {{branch}}",
		"unknown":   "{{other}}",
		"mixed":     "{{other}}-{{branch}}",
		"list":      []any{"{{branch}}", 7.0, map[string]any{"{{branch}}": "{{branch}}"}},
		"untouched": 42.0,
	}}})
	reply, err := app.toolRunCreate(nil, map[string]any{
		"kind": "test", "request_id": "run_pinned_one", "environment_id": "env_x",
		"spec_version": 3.0, "spec_digest": d.SpecDigest,
		"seed_vars": map[string]any{"candidate_digest": "sha256:" + strings.Repeat("0", 64), "branch": "main"},
	})
	if err != nil {
		t.Fatal(err)
	}
	run := reply.(*Run)
	if run.ID != "run_pinned_one" || run.Status != "running" || run.EnvironmentID != "" || run.PinnedEnvironmentID != "env_x" {
		t.Fatalf("run=%+v", run)
	}
	want := map[string]any{
		"digest":    "sha256:" + strings.Repeat("0", 64),
		"label":     "build sha256:" + strings.Repeat("0", 64) + " on main",
		"unknown":   "{{other}}",
		"mixed":     "{{other}}-main",
		"list":      []any{"main", 7.0, map[string]any{"{{branch}}": "main"}},
		"untouched": 42.0,
	}
	if len(p.inputs) != 1 || !reflect.DeepEqual(p.inputs[0], want) {
		t.Fatalf("seed inputs=%#v", p.inputs)
	}
	// The stored definition keeps its placeholders.
	stored, _ := app.svc.db.getDefinition("env_x")
	if stored.Spec.Seeds[0].Input["digest"] != "{{candidate_digest}}" || stored.SpecDigest != d.SpecDigest {
		t.Fatalf("stored definition mutated: %+v", stored.Spec.Seeds[0].Input)
	}
	created := toJSONMap(t, run)
	if created["environment_id"] != "env_x" || created["spec_version"] != 3.0 || created["spec_digest"] != d.SpecDigest {
		t.Fatalf("create reply=%v", created)
	}
	got, err := app.toolRunGet(nil, map[string]any{"id": "run_pinned_one"})
	if err != nil {
		t.Fatal(err)
	}
	fetched := toJSONMap(t, got)["run"].(map[string]any)
	if fetched["environment_id"] != "env_x" || fetched["spec_version"] != 3.0 || fetched["spec_digest"] != d.SpecDigest {
		t.Fatalf("get reply run=%v", fetched)
	}
	// A pinned run is not the definition's lifecycle run.
	if stored.ActiveRun != nil {
		t.Fatalf("pinned run became active run: %+v", stored.ActiveRun)
	}
	if err := app.svc.stopDefinition("env_x"); err != nil {
		t.Fatal(err)
	}
	if again, _ := app.svc.db.getRun("run_pinned_one"); again.Status != "running" {
		t.Fatalf("definition stop touched pinned run: %+v", again)
	}
}

func TestPinnedRunUsesInlineValidation(t *testing.T) {
	app, p := pinnedTestApp(t)
	// A legacy row that is no longer valid must fail exactly as inline would.
	raw := `{"version":1,"ttl_seconds":30}`
	digest, _ := specDigest([]byte(raw))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	if _, err := app.svc.db.db.Exec(`INSERT INTO environment_definitions(id,name,spec_version,spec_json,spec_digest,created_at,updated_at) VALUES('env_bad','Bad',1,?,?,?,?)`, raw, digest, now, now); err != nil {
		t.Fatal(err)
	}
	_, inlineErr := app.toolRunCreate(nil, map[string]any{"spec": map[string]any{"ttl_seconds": 30.0}})
	_, pinnedErr := app.toolRunCreate(nil, map[string]any{"environment_id": "env_bad", "spec_version": 1.0, "spec_digest": digest})
	if inlineErr == nil || pinnedErr == nil || inlineErr.Error() != pinnedErr.Error() {
		t.Fatalf("inline=%v pinned=%v", inlineErr, pinnedErr)
	}
	if p.created != 0 {
		t.Fatalf("invalid spec created %d runtimes", p.created)
	}
}

func TestSeedVarsValidation(t *testing.T) {
	app, p := pinnedTestApp(t)
	d := savePinnedDefinition(t, app, nil)
	tooMany := map[string]any{}
	for i := 0; i <= maxSeedVars; i++ {
		tooMany[fmt.Sprintf("v%d", i)] = "x"
	}
	for name, tc := range map[string]struct {
		vars any
		want string
	}{
		"too many":    {tooMany, "at most 16"},
		"upper name":  {map[string]any{"Branch": "x"}, "must match"},
		"digit first": {map[string]any{"1x": "x"}, "must match"},
		"long name":   {map[string]any{"a" + strings.Repeat("b", 64): "x"}, "must match"},
		"non-string":  {map[string]any{"n": 1.0}, "must be a string"},
		"too long":    {map[string]any{"v": strings.Repeat("x", 4097)}, "exceeds 4096 bytes"},
		"newline":     {map[string]any{"v": "a\nb"}, "CR or LF"},
		"return":      {map[string]any{"v": "a\rb"}, "CR or LF"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := app.toolRunCreate(nil, map[string]any{"environment_id": "env_x", "spec_version": 3.0, "spec_digest": d.SpecDigest, "seed_vars": tc.vars})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want %q", err, tc.want)
			}
		})
	}
	limits := map[string]any{}
	for i := 0; i < maxSeedVars; i++ {
		limits[fmt.Sprintf("v%d", i)] = strings.Repeat("x", maxSeedVarValueBytes)
	}
	limits["v0"] = "z" + strings.Repeat("_", 62)
	if _, err := app.toolRunCreate(nil, map[string]any{"environment_id": "env_x", "spec_version": 3.0, "spec_digest": d.SpecDigest, "seed_vars": limits}); err != nil {
		t.Fatalf("limits rejected: %v", err)
	}
	if p.created != 1 {
		t.Fatalf("created=%d", p.created)
	}
}

func TestRunCreateFormsAreExclusive(t *testing.T) {
	app, _ := pinnedTestApp(t)
	digest := strings.Repeat("a", 64)
	for name, tc := range map[string]struct {
		args map[string]any
		want string
	}{
		"both":            {map[string]any{"spec": map[string]any{}, "environment_id": "env_x", "spec_version": 1.0, "spec_digest": digest}, "mutually exclusive"},
		"spec with vars":  {map[string]any{"spec": map[string]any{}, "seed_vars": map[string]any{"a": "b"}}, "require environment_id"},
		"spec with pins":  {map[string]any{"spec": map[string]any{}, "spec_digest": digest}, "require environment_id"},
		"pins only":       {map[string]any{"spec_version": 1.0, "spec_digest": digest}, "environment_id required"},
		"missing version": {map[string]any{"environment_id": "env_x", "spec_digest": digest}, "spec_version required"},
		"missing digest":  {map[string]any{"environment_id": "env_x", "spec_version": 1.0}, "spec_digest must be"},
		"upper digest":    {map[string]any{"environment_id": "env_x", "spec_version": 1.0, "spec_digest": strings.Repeat("A", 64)}, "spec_digest must be"},
		"bad id":          {map[string]any{"environment_id": "env x", "spec_version": 1.0, "spec_digest": digest}, "environment_id must be"},
		"nested eval id":  {map[string]any{"spec": map[string]any{"environment_id": "env_x"}}, "must be executed through Evals"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := app.toolRunCreate(nil, tc.args); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("error=%v, want %q", err, tc.want)
			}
		})
	}
}

func TestInlineRunReplyIsUnchanged(t *testing.T) {
	app, p := pinnedTestApp(t)
	reply, err := app.toolRunCreate(nil, map[string]any{"kind": "test", "request_id": "run_inline_one", "spec": map[string]any{"ttl_seconds": 600.0, "seeds": []any{map[string]any{"app": "shop", "tool": "seed", "input": map[string]any{"d": "{{candidate_digest}}"}}}}})
	if err != nil {
		t.Fatal(err)
	}
	out := toJSONMap(t, reply)
	for _, field := range []string{"environment_id", "spec_version", "spec_digest"} {
		if _, found := out[field]; found {
			t.Fatalf("inline reply gained %s: %v", field, out)
		}
	}
	if out["id"] != "run_inline_one" || out["status"] != "running" {
		t.Fatalf("reply=%v", out)
	}
	// Inline specs are passed through without seed_vars substitution.
	if len(p.inputs) != 1 || p.inputs[0]["d"] != "{{candidate_digest}}" {
		t.Fatalf("inputs=%v", p.inputs)
	}
}

func TestApplySeedVarsLeavesUnclosedAndUnknownPlaceholders(t *testing.T) {
	vars := map[string]string{"a": "1"}
	for in, want := range map[string]string{
		"{{a}}":        "1",
		"{{a}":         "{{a}",
		"x{{a}}y{{a}}": "x1y1",
		"{{b}}{{a}}":   "{{b}}1",
		"{{{{a}}}}":    "{{1}}",
		"{{a}}}}":      "1}}",
		"no braces":    "no braces",
		"{{ a }}":      "{{ a }}",
	} {
		if got := substituteSeedVars(in, vars); got != want {
			t.Errorf("substitute(%q)=%q, want %q", in, got, want)
		}
	}
}
