package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestJSONSavedOrderedReferences(t *testing.T) {
	d := &actorJSONDocument{Selector: "pre", Fields: map[string]actorJSONField{
		"id": {Path: "/data/id", Required: true}, "ids": {Path: "/data/images", Select: "/id", Unique: true, Required: true},
		"names": {Path: "/data/images", Select: "/attributes/name", Resolve: &actorJSONResolve{Pool: "/included", Keys: []string{"/id", "/type"}}, Required: true},
		"draft": {Path: "/data/published", IsNull: true, Required: true}}}
	raw := `{"data":{"id":"42","published":null,"images":[{"id":"a","type":"image"},{"id":"b","type":"image"}]},"included":[{"id":"b","type":"image","attributes":{"name":"second.png"}},{"id":"a","type":"image","attributes":{"name":"first.png"}}]}`
	for _, mode := range []string{"good", "reverse", "extra", "duplicate", "missing_reference", "ambiguous_reference", "invalid", "missing", "null_array"} {
		t.Run(mode, func(t *testing.T) {
			s := raw
			switch mode {
			case "reverse":
				s = strings.Replace(s, `[{"id":"a","type":"image"},{"id":"b","type":"image"}]`, `[{"id":"b","type":"image"},{"id":"a","type":"image"}]`, 1)
			case "extra":
				s = strings.Replace(s, `"images":[`, `"images":[{"id":"b","type":"image"},`, 1)
			case "duplicate":
				s = strings.Replace(s, `{"id":"b","type":"image"}]`, `{"id":"a","type":"image"}]`, 1)
			case "missing_reference":
				s = strings.Replace(s, `"id":"b","type":"image","attributes"`, `"id":"c","type":"image","attributes"`, 1)
			case "ambiguous_reference":
				s = strings.Replace(s, `"id":"b","type":"image","attributes"`, `"id":"a","type":"image","attributes"`, 1)
			case "invalid":
				s = s[:len(s)-3]
			case "missing":
				s = strings.Replace(s, `"images":`, `"other":`, 1)
			case "null_array":
				s = strings.Replace(s, `[{"id":"a","type":"image"},{"id":"b","type":"image"}]`, `null`, 1)
			}
			out, err := extractJSONDocument(d, s)
			if mode == "good" || mode == "reverse" {
				if err != nil {
					t.Fatal(err)
				}
				e := actorExecution{lastValues: out}
				err = e.assertValues(actorStep{Assertions: map[string]actorAssertion{"ids": {EqualsExact: []any{"a", "b"}}, "names": {EqualsExact: []any{"first.png", "second.png"}}, "draft": {EqualsExact: true}}})
				if (err == nil) != (mode == "good") {
					t.Fatal(mode, err, out)
				}
			} else if err == nil {
				t.Fatal("malformed evidence accepted", mode, out)
			}
			if mode == "missing_reference" && len(out["ids"].([]any)) != 2 {
				t.Fatal("partial identity evidence lost")
			}
		})
	}
}
func TestBrowserJSONDocumentReadOnly(t *testing.T) {
	p := newFakePlatform()
	current := ""
	p.readViewsResponse = func(tool string, in map[string]any) map[string]any {
		switch tool {
		case "computer.browser_open":
			current = stringFromAny(in["url"])
		case "computer.computer_use":
			if in["action"] == "navigate" {
				current = stringFromAny(in["url"])
			}
			return map[string]any{"current_url": current}
		case "computer.browser_extract":
			return map[string]any{"html": `<html><body><pre>{"id":"42","images":[{"id":"a"},{"id":"b"}]}</pre></body></html>`, "current_url": current, "rendered": true}
		}
		return nil
	}
	ctx, a := newTestCtx(t, p)
	r := saveFixtureActor(t, ctx, a, map[string]any{"schema_version": 1, "read_only": true, "allowed_hosts": []any{"example.com"}, "steps": []any{map[string]any{"action": "goto", "url": "https://example.com/api/42"}, map[string]any{"action": "observe_json", "json_document": map[string]any{"selector": "pre", "fields": map[string]any{"id": map[string]any{"path": "/id", "required": true}, "ids": map[string]any{"path": "/images", "select": "/id", "unique": true, "required": true}}}}, map[string]any{"action": "assert_values", "assertions": map[string]any{"ids": map[string]any{"equals_exact": []any{"a", "b"}}}}}})
	q, err := a.toolActorRun(ctx, map[string]any{"actor_id": r.ID})
	if err != nil {
		t.Fatal(err)
	}
	run, _ := claimActorRun(ctx)
	if err = a.executeActorRun(context.Background(), ctx, run); err != nil {
		t.Fatal(err)
	}
	result, _ := getActorRun(ctx, q.(map[string]any)["run_id"].(int64))
	if result["status"] != "completed" {
		t.Fatal(result["error"])
	}
	b, _ := json.Marshal(mapFromAny(result["output"])["document_evidence"])
	if !strings.Contains(string(b), `"ids":["a","b"]`) {
		t.Fatal(string(b))
	}
}
