package main

import (
	"encoding/json"
	"fmt"
	"regexp"
	"strings"
	"testing"
)

const (
	exactGrantRead = `"otherwise": {"collection": "grants", "field": "ids"}`
	exactType      = `"type": "exact_string_list", `
)

var exactTypeKey = regexp.MustCompile(`"type": "exact_string_list", `)

// exactPlan resolves values and returns the named ones as $scope, so a test sees
// exactly what a stage produced.
func exactPlan(t *testing.T, values string, show ...string) map[string]any {
	t.Helper()
	result := []string{`"rows": "$steps.rows"`}
	for _, name := range show {
		result = append(result, fmt.Sprintf(`%q: "$scope.%s"`, name, name))
	}
	return decodeConfig(t, `{"database": "crm", "_project_id": "p1",
	  "params": {"exact": {"from": "$args.exact", "type": "exact_string_list"}, "normal": {"from": "$args.normal", "type": "string_list"}},
	  "access": {"values": {`+values+`}},
	  "steps": [{"id": "rows", "op": "find", "max_rows": 100, "args": {"collection": "reports"}}],
	  "result": {`+strings.Join(result, ", ")+`}}`)
}

// exactResponder answers access reads from fixed records, by collection.
func exactResponder(tables map[string]string) func([]any) (any, error) {
	return func(operations []any) (any, error) {
		out := []any{}
		for _, raw := range operations {
			operation := raw.(map[string]any)
			collection, _ := operation["args"].(map[string]any)["collection"].(string)
			var rows []any
			if text, ok := tables[collection]; ok {
				if err := json.Unmarshal([]byte(text), &rows); err != nil {
					return nil, err
				}
			}
			if rows == nil {
				rows = []any{}
			}
			if operation["op"] == "aggregate" {
				out = append(out, map[string]any{"rows": rows, "truncated": false})
				continue
			}
			out = append(out, map[string]any{"records": rows, "hasMore": false})
		}
		return map[string]any{"results": out}, nil
	}
}

func exactScope(t *testing.T, tables map[string]string, plan map[string]any, name string, permissions ...string) ([]string, error) {
	t.Helper()
	a, _ := pipelineApp(t, exactResponder(tables))
	value, err := a.callDatabasePipeline(pipelineIdentity("42", permissions...), plan)
	if err != nil {
		return nil, err
	}
	list, ok := value.(map[string]any)[name].([]string)
	if !ok {
		t.Fatalf("%s: %T", name, value.(map[string]any)[name])
	}
	return list, nil
}

// bothModes runs one set of values as exact and, with the type removed, as the
// default, so what each mode does is stated side by side.
func bothModes(t *testing.T, values string, tables map[string]string, name, wantExact, wantNormal string) {
	t.Helper()
	for mode, c := range map[string]struct{ values, want string }{
		"exact":  {values, wantExact},
		"normal": {exactTypeKey.ReplaceAllString(values, ""), wantNormal},
	} {
		got, err := exactScope(t, tables, exactPlan(t, c.values, name), name)
		if err != nil {
			t.Errorf("%s: %v", mode, err)
			continue
		}
		if text := fmt.Sprintf("%q", got); text != c.want {
			t.Errorf("%s: got %s, want %s", mode, text, c.want)
		}
	}
}

func TestExactAccessValueReads(t *testing.T) {
	read := `"a": {` + exactType + `"optional": true, ` + exactGrantRead + `}`
	for name, c := range map[string]struct{ records, exact, normal string }{
		"array field": {`[{"ids": [" a ", "a", "", "   ", "a", 7, null]}]`, `[" a " "a" "" "   " "7"]`, `["a" "7"]`},
		"scalars":     {`[{"ids": " a "}, {"ids": ""}, {"ids": null}, {"ids": " a "}, {"ids": 7}]`, `[" a " "" "7"]`, `["a" "7"]`},
		"across rows": {`[{"ids": ["a"]}, {"ids": ["a", " a"]}]`, `["a" " a"]`, `["a"]`},
		"truly empty": {`[{"ids": []}]`, `[]`, `[]`},
		"no record":   {`[]`, `[]`, `[]`},
		"blank":       {`[{"ids": [""]}]`, `[""]`, `[]`},
		"spaces":      {`[{"ids": ["   "]}]`, `["   "]`, `[]`},
		"unknown":     {`[{"ids": ["no-such-id"]}]`, `["no-such-id"]`, `["no-such-id"]`},
	} {
		t.Run(name, func(t *testing.T) {
			bothModes(t, read, map[string]string{"grants": c.records}, "a", c.exact, c.normal)
		})
	}

	t.Run("distinct read", func(t *testing.T) {
		values := `"a": {` + exactType + `"optional": true, "otherwise": {"collection": "grants", "field": "ids", "distinct": true}}`
		bothModes(t, values, map[string]string{"grants": `[{"ids": " a "}, {"ids": "a"}, {"ids": ""}, {"ids": 7}]`}, "a", `[" a " "a" "" "7"]`, `["a" "7"]`)
	})
	t.Run("an empty value still refuses unless optional", func(t *testing.T) {
		values := `"a": {` + exactType + exactGrantRead + `}`
		if _, err := exactScope(t, map[string]string{"grants": `[{"ids": []}]`}, exactPlan(t, values, "a"), "a"); err == nil || !strings.Contains(err.Error(), "no accessible") {
			t.Errorf("exact []: %v", err)
		}
		if got, err := exactScope(t, map[string]string{"grants": `[{"ids": [""]}]`}, exactPlan(t, values, "a"), "a"); err != nil || fmt.Sprintf("%q", got) != `[""]` {
			t.Errorf(`exact [""]: %q %v`, got, err)
		}
		normal := exactTypeKey.ReplaceAllString(values, "")
		if _, err := exactScope(t, map[string]string{"grants": `[{"ids": [""]}]`}, exactPlan(t, normal, "a"), "a"); err == nil || !strings.Contains(err.Error(), "no accessible") {
			t.Errorf(`normal [""]: %v`, err)
		}
	})
	t.Run("single counts identifiers as stored", func(t *testing.T) {
		values := `"a": {` + exactType + `"single": true, ` + exactGrantRead + `}`
		two := map[string]string{"grants": `[{"ids": [" a ", "a"]}]`}
		if _, err := exactScope(t, two, exactPlan(t, values, "a"), "a"); err == nil || !strings.Contains(err.Error(), "exactly one") {
			t.Errorf("exact: %v", err)
		}
		if got, err := exactScope(t, map[string]string{"grants": `[{"ids": [""]}]`}, exactPlan(t, values, "a"), "a"); err != nil || fmt.Sprintf("%q", got) != `[""]` {
			t.Errorf("exact blank: %q %v", got, err)
		}
		if got, err := exactScope(t, two, exactPlan(t, exactTypeKey.ReplaceAllString(values, ""), "a"), "a"); err != nil || fmt.Sprintf("%q", got) != `["a"]` {
			t.Errorf("normal: %q %v", got, err)
		}
	})
}

func TestExactAccessValuesSurviveLaterStages(t *testing.T) {
	const reads = `"a": {` + exactType + `"optional": true, "otherwise": {"collection": "grants", "field": "ids"}},
	  "b": {` + exactType + `"optional": true, "otherwise": {"collection": "others", "field": "ids"}}`
	tables := map[string]string{"grants": `[{"ids": ["x", " x ", ""]}]`, "others": `[{"ids": [" x ", "", "y"]}]`}
	withValue := func(name, spec string) string {
		return reads + `, "` + name + `": {` + exactType + `"optional": true, ` + spec + `}`
	}
	for name, c := range map[string]struct{ spec, exact, normal string }{
		"union":       {`"union": ["a", "b"]`, `["x" " x " "" "y"]`, `["x" "y"]`},
		"intersect":   {`"intersect": ["a", "b"]`, `[" x " ""]`, `["x"]`},
		"expr input":  {`"expr": "$scope.a"`, `["x" " x " ""]`, `["x"]`},
		"expr branch": {`"expr": {"op": "if", "condition": {"op": "gt", "args": [{"op": "length", "args": ["$scope.a"]}, {"const": 0}]}, "then": "$scope.b", "else": {"const": []}}`, `[" x " "" "y"]`, `["x" "y"]`},
		"expr const":  {`"expr": {"const": [" k ", "", null, 7, 7, " k "]}`, `[" k " "" "7"]`, `["k" "7"]`},
	} {
		t.Run(name, func(t *testing.T) {
			bothModes(t, withValue("c", c.spec), tables, "c", c.exact, c.normal)
		})
	}
	t.Run("expr result stays refused when not scalar", func(t *testing.T) {
		values := `"c": {` + exactType + `"optional": true, "expr": {"const": [{"a": 1}]}}`
		if _, err := exactScope(t, nil, exactPlan(t, values, "c"), "c"); err == nil || !strings.Contains(err.Error(), "scalar") {
			t.Errorf("got %v", err)
		}
	})
	t.Run("a chain of stages", func(t *testing.T) {
		values := withValue("c", `"intersect": ["a", "b"]`) +
			`, "d": {` + exactType + `"optional": true, "expr": {"const": [" k ", ""]}}` +
			`, "e": {` + exactType + `"optional": true, "union": ["c", "d"]}` +
			`, "f": {` + exactType + `"optional": true, "expr": "$scope.e"}`
		bothModes(t, values, tables, "f", `[" x " "" " k "]`, `["x" "k"]`)
	})
	t.Run("a normalised value beside exact ones keeps its normalisation", func(t *testing.T) {
		values := `"home": {"optional": true, "otherwise": {"collection": "profiles", "field": "centre"}},
		  "a": {` + exactType + `"optional": true, ` + exactGrantRead + `},
		  "c": {` + exactType + `"optional": true, "union": ["home", "a"]}`
		got, err := exactScope(t, map[string]string{"profiles": `[{"centre": "  home "}]`, "grants": `[{"ids": [" x "]}]`}, exactPlan(t, values, "c"), "c")
		if err != nil || fmt.Sprintf("%q", got) != `["home" " x "]` {
			t.Errorf("%q %v", got, err)
		}
	})
}

// A stored [""] is a grant that matches nothing real, not the absence of a grant.
func TestExactAccessValuesKeepEmptyDistinctFromBlank(t *testing.T) {
	const values = `"grant": {` + exactType + `"optional": true, ` + exactGrantRead + `},
	  "scope": {` + exactType + `"optional": true, "expr": {"op": "if",
	    "condition": {"op": "eq", "args": [{"op": "length", "args": ["$scope.grant"]}, {"const": 0}]},
	    "then": {"const": ["fallback"]}, "else": "$scope.grant"}}`
	for name, c := range map[string]struct{ stored, exact, normal string }{
		"empty":  {`[]`, `["fallback"]`, `["fallback"]`},
		"blank":  {`[""]`, `[""]`, `["fallback"]`},
		"spaces": {`["   "]`, `["   "]`, `["fallback"]`},
		"padded": {`[" a "]`, `[" a "]`, `["a"]`},
	} {
		t.Run(name, func(t *testing.T) {
			bothModes(t, values, map[string]string{"grants": `[{"ids": ` + c.stored + `}]`}, "scope", c.exact, c.normal)
		})
	}

	// The same difference in a trusted condition and in the filter sent.
	filterPlan := func(values string) map[string]any {
		plan := exactPlan(t, values)
		plan["steps"].([]any)[0].(map[string]any)["args"].(map[string]any)["where"] = decodeConfig(t, `{"and": [{
		  "access_when": {"op": "gt", "args": [{"op": "length", "args": ["$scope.grant"]}, {"const": 0}]},
		  "filter": {"field": "centre", "op": "in", "value": "$scope.grant"}}]}`)
		return plan
	}
	const grant = `"grant": {` + exactType + `"optional": true, ` + exactGrantRead + `}`
	for name, c := range map[string]struct{ values, want string }{
		"exact keeps the restriction": {grant, `"centre","op":"in","value":[""]`},
		"normal selects it away":      {exactTypeKey.ReplaceAllString(grant, ""), ``},
	} {
		t.Run(name, func(t *testing.T) {
			a, p := pipelineApp(t, exactResponder(map[string]string{"grants": `[{"ids": [""]}]`}))
			if _, err := a.callDatabasePipeline(pipelineIdentity("42"), filterPlan(c.values)); err != nil {
				t.Fatal(err)
			}
			sent, _ := json.Marshal(p.batches[len(p.batches)-1]["operations"])
			if c.want != "" && !strings.Contains(string(sent), c.want) {
				t.Errorf("sent %s", sent)
			}
			if c.want == "" && strings.Contains(string(sent), `"centre"`) {
				t.Errorf("sent %s", sent)
			}
		})
	}
}

func TestExactRequestedScope(t *testing.T) {
	const values = `"a": {` + exactType + `"requested": "$param.exact", "unrestricted_if_permission": "all", "default": {"collection": "catalogue", "field": "ids"}, ` + exactGrantRead + `}`
	ask := func(t *testing.T, stored string, requested []any, permissions ...string) (string, error) {
		t.Helper()
		plan := exactPlan(t, values, "a")
		plan["graphql_args"] = map[string]any{"exact": requested}
		got, err := exactScope(t, map[string]string{"grants": `[{"ids": ` + stored + `}]`, "catalogue": `[{"ids": [" c ", ""]}]`}, plan, "a", permissions...)
		return fmt.Sprintf("%q", got), err
	}
	const granted = `["centre-a", " centre-a ", ""]`
	for name, c := range map[string]struct {
		stored    string
		requested []any
		want      string
	}{
		"nothing requested":    {granted, []any{}, `["centre-a" " centre-a " ""]`},
		"one":                  {granted, []any{"centre-a"}, `["centre-a"]`},
		"padded, when granted": {granted, []any{" centre-a "}, `[" centre-a "]`},
		"blank, when granted":  {granted, []any{""}, `[""]`},
		"duplicates":           {granted, []any{"centre-a", "centre-a", " centre-a "}, `["centre-a" " centre-a "]`},
		"outside the grant":    {granted, []any{"centre-b"}, ``},
		"other padding":        {granted, []any{"  centre-a"}, ``},
		"one outside refuses":  {granted, []any{"centre-a", "centre-b"}, ``},
		"padded, not granted":  {`["centre-a"]`, []any{" centre-a "}, ``},
		"blank, not granted":   {`["centre-a"]`, []any{""}, ``},
		"spaces, not granted":  {`[""]`, []any{"  "}, ``},
		"unknown, empty grant": {`[""]`, []any{"no-such-id"}, ``},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := ask(t, c.stored, c.requested)
			if c.want == "" {
				if err == nil || !strings.Contains(err.Error(), "outside") {
					t.Errorf("got %s, %v", got, err)
				}
				return
			}
			if err != nil || got != c.want {
				t.Errorf("got %s, %v", got, err)
			}
		})
	}
	t.Run("unrestricted gets what it asked for, exactly", func(t *testing.T) {
		if got, err := ask(t, `[]`, []any{" x ", ""}, "all"); err != nil || got != `[" x " ""]` {
			t.Errorf("%s %v", got, err)
		}
		if got, err := ask(t, `[]`, []any{}, "all"); err != nil || got != `[" c " ""]` {
			t.Errorf("default read: %s %v", got, err)
		}
	})
	t.Run("the normalised mode is unchanged", func(t *testing.T) {
		const values = `"a": {"requested": "$param.normal", ` + exactGrantRead + `}`
		for name, c := range map[string]struct {
			stored    string
			requested []any
			want      string
		}{
			"padded grant and request": {`[" centre-a "]`, []any{" centre-a "}, `["centre-a"]`},
			"a blank is no request":    {`["centre-a", "centre-b"]`, []any{""}, `["centre-a" "centre-b"]`},
			"outside":                  {`["centre-a"]`, []any{"centre-b"}, ``},
		} {
			plan := exactPlan(t, values, "a")
			plan["graphql_args"] = map[string]any{"normal": c.requested}
			got, err := exactScope(t, map[string]string{"grants": `[{"ids": ` + c.stored + `}]`}, plan, "a")
			if text := fmt.Sprintf("%q", got); (c.want == "" && err == nil) || (c.want != "" && (err != nil || text != c.want)) {
				t.Errorf("%s: %s, %v", name, text, err)
			}
		}
	})
}

func TestExactAccessValueValidation(t *testing.T) {
	const read = exactGrantRead
	for name, values := range map[string]string{
		"explicit default":            `"a": {"type": "string_list", ` + read + `}`,
		"exact read":                  `"a": {"type": "exact_string_list", ` + read + `}`,
		"exact derived from normal":   `"a": {` + read + `}, "c": {"type": "exact_string_list", "expr": "$scope.a"}`,
		"exact union of normal":       `"a": {` + read + `}, "b": {` + read + `}, "c": {"type": "exact_string_list", "union": ["a", "b"]}`,
		"normal read on exact values": `"a": {"type": "exact_string_list", ` + read + `}, "c": {"otherwise": {"collection": "o", "field": "id", "where": {"field": "ref", "op": "in", "value": "$scope.a"}}}`,
		"exact requested":             `"a": {"type": "exact_string_list", "requested": "$param.exact", ` + read + `}`,
		"normal requested":            `"a": {"requested": "$param.normal", ` + read + `}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := validateDatabasePipeline(exactPlan(t, values)); err != nil {
				t.Fatal(err)
			}
		})
	}
	for name, c := range map[string]struct{ values, want string }{
		"unknown type":                  {`"a": {"type": "string", ` + read + `}`, "type must be"},
		"non-string type":               {`"a": {"type": 3, ` + read + `}`, "type must be"},
		"object type":                   {`"a": {"type": {"x": 1}, ` + read + `}`, "type must be"},
		"placeholder type":              {`"a": {"type": "$args.mode", ` + read + `}`, "type must be"},
		"exact with a normalised param": {`"a": {"type": "exact_string_list", "requested": "$param.normal", ` + read + `}`, "must name a exact_string_list"},
		"normal with an exact param":    {`"a": {"requested": "$param.exact", ` + read + `}`, "must name a string_list"},
		"normal union of exact":         {`"a": {"type": "exact_string_list", ` + read + `}, "b": {` + read + `}, "c": {"union": ["a", "b"]}`, "must also be type exact_string_list"},
		"normal intersect of exact":     {`"a": {"type": "exact_string_list", ` + read + `}, "b": {` + read + `}, "c": {"intersect": ["b", "a"]}`, "must also be type exact_string_list"},
		"normal expr of exact":          {`"a": {"type": "exact_string_list", ` + read + `}, "c": {"expr": "$scope.a"}`, "must also be type exact_string_list"},
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := validateDatabasePipeline(exactPlan(t, c.values)); err == nil || !strings.Contains(err.Error(), c.want) {
				t.Fatalf("got %v, want %q", err, c.want)
			}
		})
	}
}
