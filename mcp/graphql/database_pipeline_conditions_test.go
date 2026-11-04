package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

const (
	conditionTrue    = `{"const":true}`
	selectedWhen     = `{"op":"gt","args":[{"op":"length","args":["$param.centres"]},{"const":0}]}`
	selectedFilter   = `{"field":"centre_id","op":"in","value":"$param.centres"}`
	selectedCentres  = `{"when":` + selectedWhen + `,"filter":` + selectedFilter + `}`
	authorisedFilter = `{"field":"centre_id","op":"in","value":"$scope.allowed"}`
	statusFilter     = `{"when":{"op":"neq","args":["$param.status",{"const":""}]},"filter":{"field":"status","op":"eq","value":"$param.status"}}`
)

// conditionalPlan is a scoped page with a total, filtered by where. Its params
// cover every kind of source a condition could be pointed at.
func conditionalPlan(t *testing.T, where string) map[string]any {
	t.Helper()
	plan := decodeConfig(t, `{
	  "params": {
	    "user": {"from": "$identity.subject", "required": true},
	    "perms": {"from": "$identity.permissions", "type": "string_list"},
	    "centres": {"from": "$args.centres", "type": "exact_string_list"},
	    "normalised": {"from": "$args.normalised", "type": "string_list"},
	    "nested": {"from": "$args.input.centres", "type": "exact_string_list"},
	    "status": {"from": "$args.status"},
	    "min": {"from": "$args.min", "type": "number"},
	    "flag": {"from": "$args.flag", "type": "boolean"},
	    "blob": {"from": "$args.blob", "type": "json"},
	    "since": {"from": "$args.since", "type": "datetime"},
	    "fixed": {"value": "open"}
	  },
	  "access": {"values": {"allowed": {"optional": true, "otherwise": {"collection": "profiles", "field": "centre_id",
	    "where": {"field": "user_id", "op": "eq", "value": "$param.user"}}}}},
	  "steps": [
	    {"id": "rows", "op": "find", "max_rows": 100, "args": {"collection": "leads", "where": `+where+`, "orderBy": [{"field": "id"}]}},
	    {"id": "total", "op": "count", "count_of": "rows"}],
	  "result": {"rows": "$steps.rows", "total": "$steps.total"}}`)
	plan["database"] = "crm"
	plan["_project_id"] = "p1"
	return plan
}

func TestConditionalFilterValidationRefuses(t *testing.T) {
	when := func(condition string) string {
		return `{"and":[` + authorisedFilter + `,{"when":` + condition + `,"filter":{"field":"a","op":"eq","value":"x"}}]}`
	}
	filter := func(nested string) string {
		return `{"and":[` + authorisedFilter + `,{"when":` + conditionTrue + `,"filter":` + nested + `}]}`
	}
	deep := conditionTrue
	for i := 0; i < maxConditionDepth+2; i++ {
		deep = `{"op":"not","args":[` + deep + `]}`
	}
	for name, c := range map[string]struct{ where, want string }{
		"at the root":             {selectedCentres, "child of an and"},
		"inside or":               {`{"or":[` + authorisedFilter + `,` + selectedCentres + `]}`, "inside or"},
		"inside and inside or":    {`{"or":[` + authorisedFilter + `,{"and":[` + selectedCentres + `]}]}`, "inside or"},
		"inside another":          {filter(selectedCentres), "another conditional"},
		"inside its own filter":   {filter(`{"and":[` + selectedCentres + `]}`), "another conditional"},
		"extra key":               {`{"and":[{"when":` + conditionTrue + `,"filter":` + selectedFilter + `,"also":1}]}`, "also"},
		"no filter":               {`{"and":[{"when":` + conditionTrue + `}]}`, "requires both"},
		"no condition":            {`{"and":[{"filter":` + selectedFilter + `}]}`, "requires both"},
		"null condition":          {`{"and":[{"when":null,"filter":` + selectedFilter + `}]}`, "requires both"},
		"scope in filter":         {filter(authorisedFilter), "$scope.allowed"},
		"scope in filter list":    {filter(`{"field":"a","op":"in","value":["$scope.allowed"]}`), "$scope.allowed"},
		"scope in condition":      {when(`{"op":"gt","args":[{"op":"length","args":["$scope.allowed"]},{"const":0}]}`), "$scope.allowed"},
		"identity in filter":      {filter(`{"field":"a","op":"eq","value":"$param.user"}`), "identity"},
		"identity in filter list": {filter(`{"field":"a","op":"in","value":["$param.user"]}`), "identity"},
		"identity in condition":   {when(`{"op":"eq","args":["$param.user",{"const":"7"}]}`), "identity"},
		"permissions":             {when(`{"op":"gt","args":[{"op":"length","args":["$param.perms"]},{"const":0}]}`), "identity"},
		"direct identity":         {when(`{"op":"eq","args":["$identity.subject",{"const":"7"}]}`), "$param"},
		"nested argument":         {when(`{"op":"gt","args":[{"op":"length","args":["$param.nested"]},{"const":0}]}`), "top-level"},
		"undeclared param":        {when(`{"op":"eq","args":["$param.nope",{"const":"7"}]}`), "undeclared"},
		"not Boolean":             {when(`{"op":"length","args":["$param.centres"]}`), "must be Boolean"},
		"string as Boolean":       {when(`"$param.status"`), "must be Boolean"},
		"constant as Boolean":     {when(`{"const":"false"}`), "must be Boolean"},
		"json param":              {when(`{"op":"gt","args":[{"op":"length","args":["$param.blob"]},{"const":0}]}`), "cannot use"},
		"datetime param":          {when(`{"op":"gt","args":["$param.since",{"const":"2026-01-01T00:00:00Z"}]}`), "cannot use"},
		"string_list condition":   {when(`{"op":"gt","args":[{"op":"length","args":["$param.normalised"]},{"const":0}]}`), "exact_string_list"},
		"string_list filter":      {filter(`{"field":"a","op":"in","value":"$param.normalised"}`), "exact_string_list"},
		"unknown op":              {when(`{"op":"if","args":[{"const":true}]}`), "not allowed"},
		"coalesce of text":        {when(`{"op":"coalesce","args":["$param.status",{"const":1}]}`), "mixes"},
		"bare string":             {when(`"yes"`), "not a $param"},
		"bare number":             {when(`1`), "a condition is"},
		"bare Boolean":            {when(`true`), "a condition is"},
		"null const":              {when(`{"const":null}`), "cannot be null"},
		"const with op":           {when(`{"const":true,"op":"not"}`), "stand alone"},
		"object const":            {when(`{"const":{"a":1}}`), "const must be"},
		"mixed list const":        {when(`{"op":"gt","args":[{"op":"length","args":[{"const":["a",1]}]},{"const":0}]}`), "only strings"},
		"op with extra key":       {when(`{"op":"not","args":[{"const":true}],"x":1}`), "x"},
		"no args":                 {when(`{"op":"not"}`), "args"},
		"mixed comparison":        {when(`{"op":"eq","args":["$param.status",{"const":1}]}`), "same type"},
		"list comparison":         {when(`{"op":"eq","args":["$param.centres",{"const":[]}]}`), "cannot compare"},
		"Boolean ordering":        {when(`{"op":"gt","args":["$param.flag",{"const":false}]}`), "cannot compare"},
		"and of text":             {when(`{"op":"and","args":["$param.status"]}`), "requires Booleans"},
		"empty and":               {when(`{"op":"and","args":[]}`), "requires Booleans"},
		"not of text":             {when(`{"op":"not","args":["$param.status"]}`), "not requires"},
		"length of number":        {when(`{"op":"gt","args":[{"op":"length","args":["$param.min"]},{"const":0}]}`), "length requires"},
		"too deep":                {when(deep), "too deep"},
		"in with one value":       {filter(`{"field":"a","op":"in","value":"$param.status"}`), "in requires"},
		"eq with a list":          {filter(`{"field":"a","op":"eq","value":"$param.centres"}`), "does not take a list"},
		"list param in a list":    {filter(`{"field":"a","op":"in","value":["$param.centres"]}`), "list item"},
		"object value":            {filter(`{"field":"a","op":"eq","value":{"x":"$param.user"}}`), "scalar"},
		"nested list value":       {filter(`{"field":"a","op":"in","value":[["$param.user"]]}`), "scalars"},
		"bad filter when false":   {`{"and":[{"when":{"const":false},"filter":{"field":"a","op":"contains","value":"x"}}]}`, "unsupported op"},
		"no field when false":     {`{"and":[{"when":{"const":false},"filter":{"op":"eq","value":"x"}}]}`, "literal field"},
		"search inside or":        {filter(`{"or":[{"fields":["a"],"op":"like_contains_ascii","value":"$param.status"},{"field":"b","op":"eq","value":"x"}]}`), "cannot be inside or"},
		"bad search":              {filter(`{"fields":[],"op":"like_contains_ascii","value":"$param.status"}`), "like_contains_ascii"},
		"list search":             {filter(`{"fields":["a"],"op":"like_contains_ascii","value":"$param.centres"}`), "list"},
	} {
		_, err := validateDatabasePipeline(conditionalPlan(t, c.where))
		if err == nil {
			t.Errorf("%s was accepted", name)
		} else if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want %q in: %v", name, c.want, err)
		}
	}

	// Access reads decide authorization, so client input has no place there.
	plan := conditionalPlan(t, authorisedFilter)
	otherwise := plan["access"].(map[string]any)["values"].(map[string]any)["allowed"].(map[string]any)["otherwise"].(map[string]any)
	otherwise["where"] = decodeConfig(t, `{"and":[`+selectedCentres+`]}`)
	if _, err := validateDatabasePipeline(plan); err == nil || !strings.Contains(err.Error(), "cannot use a conditional") {
		t.Errorf("access read: %v", err)
	}
	// So do metric filters, under the same rules as step filters.
	plan = conditionalPlan(t, authorisedFilter)
	plan["steps"] = decodeConfig(t, `{"s":[{"id":"rows","op":"aggregate","args":{"collection":"leads","groupBy":["status"],"metrics":[
	  {"name":"n","op":"count","where":{"or":[`+selectedCentres+`]}}]}}]}`)["s"]
	plan["result"] = "$steps.rows"
	if _, err := validateDatabasePipeline(plan); err == nil || !strings.Contains(err.Error(), "inside or") {
		t.Errorf("metric under or: %v", err)
	}
	// Plain resolvers take no conditional filters.
	if err := validateDatabaseResolverOptions("find", decodeConfig(t, `{"collection":"leads","where":{"and":[`+selectedCentres+`]}}`)); err == nil || !strings.Contains(err.Error(), "database_pipeline") {
		t.Errorf("plain resolver: %v", err)
	}
}

func TestConditionalFilterValidationAccepts(t *testing.T) {
	for name, where := range map[string]string{
		"selection":      `{"and":[` + authorisedFilter + `,` + selectedCentres + `]}`,
		"status":         `{"and":[` + authorisedFilter + `,` + statusFilter + `]}`,
		"nested and":     `{"and":[` + authorisedFilter + `,{"and":[` + selectedCentres + `,` + statusFilter + `]}]}`,
		"or in filter":   `{"and":[` + authorisedFilter + `,{"when":` + conditionTrue + `,"filter":{"or":[{"field":"a","op":"is_null"},{"field":"a","op":"eq","value":"$param.status"}]}}]}`,
		"or beside":      `{"and":[{"or":[` + authorisedFilter + `,{"field":"a","op":"eq","value":"x"}]},` + statusFilter + `]}`,
		"search":         `{"and":[` + authorisedFilter + `,{"when":{"op":"neq","args":["$param.status",{"const":""}]},"filter":{"fields":["a","b"],"op":"like_contains_ascii","value":"$param.status"}}]}`,
		"coalesce":       `{"and":[{"when":{"op":"gt","args":[{"op":"length","args":[{"op":"coalesce","args":["$param.centres",{"const":[]}]}]},{"const":0}]},"filter":` + selectedFilter + `}]}`,
		"Booleans":       `{"and":[{"when":{"op":"and","args":["$param.flag",{"op":"not","args":[{"op":"or","args":["$param.flag",{"const":false}]}]}]},"filter":` + selectedFilter + `}]}`,
		"number":         `{"and":[{"when":{"op":"gte","args":["$param.min",{"const":1.5}]},"filter":{"field":"a","op":"gte","value":"$param.min"}}]}`,
		"fixed value":    `{"and":[{"when":{"op":"eq","args":["$param.fixed",{"const":"open"}]},"filter":{"field":"a","op":"eq","value":"$param.fixed"}}]}`,
		"literal list":   `{"and":[{"when":` + conditionTrue + `,"filter":{"field":"a","op":"in","value":["x","y"]}}]}`,
		"date as string": `{"and":[{"when":{"op":"neq","args":["$param.status",{"const":""}]},"filter":{"field":"at","op":"gte","value":"$param.status"}}]}`,
	} {
		if _, err := validateDatabasePipeline(conditionalPlan(t, where)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	// An ordinary filter may still use normalised lists and identity values.
	if _, err := validateDatabasePipeline(conditionalPlan(t, `{"and":[{"field":"a","op":"in","value":"$param.normalised"},{"field":"b","op":"eq","value":"$param.user"}]}`)); err != nil {
		t.Errorf("ordinary filter: %v", err)
	}
}

func TestConditionalSearchIsFound(t *testing.T) {
	plan, err := validateDatabasePipeline(conditionalPlan(t, `{"and":[`+authorisedFilter+`,{"when":{"op":"neq","args":["$param.status",{"const":""}]},"filter":{"fields":["a"],"op":"like_contains_ascii","value":"$param.status"}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(plan.searchArguments()); got != "[status]" {
		t.Fatalf("search inside a conditional filter: %s", got)
	}
	if got := fmt.Sprint(plan.conditionalArguments()); got != "map[status:false]" {
		t.Fatalf("conditional arguments: %s", got)
	}
}

func TestConditionEvaluation(t *testing.T) {
	params := map[string]any{"list": []any{"a"}, "empty": []any{}, "text": "x", "blank": "", "n": int64(3), "yes": true, "no": false, "missing": nil, "false": "false"}
	for expression, want := range map[string]any{
		`"$param.yes"`:                               true,
		`{"const":"x"}`:                              "x",
		`{"op":"length","args":["$param.list"]}`:     int64(1),
		`{"op":"length","args":["$param.empty"]}`:    int64(0),
		`{"op":"length","args":[{"const":"héllo"}]}`: int64(5),
		`{"op":"gt","args":[{"op":"length","args":["$param.list"]},{"const":0}]}`:                                            true,
		`{"op":"gt","args":[{"op":"length","args":["$param.empty"]},{"const":0}]}`:                                           false,
		`{"op":"eq","args":["$param.text",{"const":"x"}]}`:                                                                   true,
		`{"op":"neq","args":["$param.blank",{"const":""}]}`:                                                                  false,
		`{"op":"lt","args":["$param.n",{"const":10}]}`:                                                                       true,
		`{"op":"lte","args":["$param.n",{"const":3.0}]}`:                                                                     true,
		`{"op":"gte","args":["$param.n",{"const":4}]}`:                                                                       false,
		`{"op":"eq","args":["$param.yes",{"const":true}]}`:                                                                   true,
		`{"op":"neq","args":["$param.yes",{"const":false}]}`:                                                                 true,
		`{"op":"and","args":["$param.yes","$param.no"]}`:                                                                     false,
		`{"op":"or","args":["$param.yes","$param.no"]}`:                                                                      true,
		`{"op":"not","args":["$param.no"]}`:                                                                                  true,
		`{"op":"coalesce","args":["$param.missing",{"const":"d"}]}`:                                                          "d",
		`{"op":"coalesce","args":["$param.blank",{"const":"d"}]}`:                                                            "",
		`{"op":"gt","args":[{"op":"length","args":[{"op":"coalesce","args":["$param.missing",{"const":[]}]}]},{"const":0}]}`: false,
	} {
		var raw any
		decoder := json.NewDecoder(strings.NewReader(expression))
		decoder.UseNumber()
		if err := decoder.Decode(&raw); err != nil {
			t.Fatal(err)
		}
		got, err := evalCondition(raw, params)
		if err != nil || fmt.Sprint(got) != fmt.Sprint(want) {
			t.Errorf("%s: got %v (%v), want %v", expression, got, err, want)
		}
	}
	// Nothing is decided by truthiness, and a missing operand always fails,
	// whichever way the other operands came out.
	for _, expression := range []string{
		`{"op":"and","args":["$param.false"]}`,
		`{"op":"not","args":["$param.false"]}`,
		`{"op":"gt","args":["$param.false",{"const":0}]}`,
		`{"op":"eq","args":["$param.yes",{"const":"true"}]}`,
		`{"op":"eq","args":["$param.n",{"const":"3"}]}`,
		`{"op":"length","args":["$param.n"]}`,
		`{"op":"length","args":["$param.missing"]}`,
		`{"op":"eq","args":["$param.missing",{"const":""}]}`,
		`{"op":"and","args":["$param.no",{"op":"gt","args":["$param.missing",{"const":0}]}]}`,
		`{"op":"or","args":["$param.yes",{"op":"not","args":["$param.missing"]}]}`,
		`{"op":"not","args":[{"op":"coalesce","args":["$param.missing","$param.missing"]}]}`,
	} {
		var raw any
		if err := json.Unmarshal([]byte(expression), &raw); err != nil {
			t.Fatal(err)
		}
		if got, err := evalCondition(raw, params); pipelineErrorCode(err) != "invalid_request" {
			t.Errorf("%s: got %v (%v), want an error", expression, got, err)
		}
	}
}

// conditionalResponder answers access reads with the given centres and records
// every other operation.
func conditionalResponder(allowed ...any) func([]any) (any, error) {
	return func(operations []any) (any, error) {
		out := []any{}
		for _, raw := range operations {
			operation := raw.(map[string]any)
			args := operation["args"].(map[string]any)
			switch {
			case args["collection"] == "profiles":
				records := []any{}
				for _, centre := range allowed {
					records = append(records, map[string]any{"centre_id": centre})
				}
				out = append(out, map[string]any{"records": records, "hasMore": false})
			case operation["op"] == "count":
				out = append(out, map[string]any{"count": "1"})
			case operation["op"] == "aggregate":
				out = append(out, map[string]any{"rows": []any{}, "truncated": false})
			default:
				out = append(out, map[string]any{"records": []any{}, "hasMore": false})
			}
		}
		return map[string]any{"results": out}, nil
	}
}

// conditionalReads returns the where of each step read, as sent to Database.
func conditionalReads(t *testing.T, p *dbPipelinePlatform) []string {
	t.Helper()
	reads := []string{}
	for _, batch := range p.batches {
		for _, raw := range batch["operations"].([]any) {
			args := raw.(map[string]any)["args"].(map[string]any)
			if args["collection"] == "profiles" {
				continue
			}
			encoded, err := json.Marshal(args["where"])
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(string(encoded), `"when"`) {
				t.Fatalf("a conditional node reached Database: %s", encoded)
			}
			reads = append(reads, string(encoded))
		}
	}
	return reads
}

func TestConditionalFilterBinding(t *testing.T) {
	where := `{"and":[` + authorisedFilter + `,` + selectedCentres + `,` + statusFilter + `]}`
	run := func(where string, args map[string]any, allowed ...any) ([]string, *dbPipelinePlatform, any, error) {
		t.Helper()
		plan := conditionalPlan(t, where)
		plan["graphql_args"] = args
		a, p := pipelineApp(t, conditionalResponder(allowed...))
		out, err := a.callDatabasePipeline(pipelineIdentity("7"), plan)
		if err != nil {
			return nil, p, out, err
		}
		return conditionalReads(t, p), p, out, nil
	}
	authorised := `{"field":"centre_id","op":"in","value":["a"]}`

	// Everything optional is off: only the authorised predicate is sent, to the
	// page and its total, in one batch.
	reads, p, _, err := run(where, map[string]any{"centres": []any{}, "status": ""}, "a")
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"and":[` + authorised + `]}`; len(reads) != 2 || reads[0] != want || reads[1] != want {
		t.Fatalf("empty selection: %v", reads)
	}
	if len(p.batches) != 2 {
		t.Fatalf("access then one batch for page and total, got %d batches", len(p.batches))
	}

	// A selection and a status are ANDed with it.
	reads, _, _, err = run(where, map[string]any{"centres": []any{"a", "b"}, "status": "open"}, "a")
	if err != nil {
		t.Fatal(err)
	}
	want := `{"and":[` + authorised + `,{"field":"centre_id","op":"in","value":["a","b"]},{"field":"status","op":"eq","value":"open"}]}`
	if len(reads) != 2 || reads[0] != want || reads[1] != want {
		t.Fatalf("selection: %v", reads)
	}

	// A selection never widens an empty authorisation, and nothing is read.
	for _, centres := range [][]any{{}, {"a"}} {
		reads, _, out, err := run(where, map[string]any{"centres": centres, "status": ""})
		if err != nil || len(reads) != 0 || fmt.Sprint(out.(map[string]any)["total"]) != "0" {
			t.Fatalf("empty authorisation with %v: %v %v %v", centres, reads, out, err)
		}
	}

	// A true condition over an empty list still means "match nothing".
	reads, _, out, err := run(`{"and":[{"when":`+conditionTrue+`,"filter":`+selectedFilter+`}]}`, map[string]any{"centres": []any{}}, "a")
	if err != nil || len(reads) != 0 || fmt.Sprint(out.(map[string]any)["total"]) != "0" {
		t.Fatalf("empty in: %v %v %v", reads, out, err)
	}

	// With nothing left there is no where at all; Database refuses empty groups.
	reads, _, _, err = run(`{"and":[`+selectedCentres+`,{"and":[`+statusFilter+`]}]}`, map[string]any{"centres": []any{}, "status": ""}, "a")
	if err != nil || len(reads) != 2 || reads[0] != "null" || reads[1] != "null" {
		t.Fatalf("nothing left: %v %v", reads, err)
	}

	// A group written empty in the config is not widened to no filter.
	reads, _, _, err = run(`{"and":[]}`, map[string]any{}, "a")
	if err != nil || len(reads) != 2 || reads[0] != `{"and":[]}` {
		t.Fatalf("stored empty group: %v %v", reads, err)
	}

	// Identifiers reach Database as sent: blanks and spaces can't turn the
	// selection off or change what it selects.
	reads, _, _, err = run(where, map[string]any{"centres": []any{"", " a ", "a", " a "}, "status": ""}, "a")
	if err != nil || !strings.Contains(reads[0], `"value":[""," a ","a"]`) {
		t.Fatalf("exact list: %v %v", reads, err)
	}
	// A single blank selection is a selection, not "no selection".
	reads, _, _, err = run(where, map[string]any{"centres": []any{""}, "status": ""}, "a")
	if err != nil || !strings.Contains(reads[0], `"value":[""]`) {
		t.Fatalf("blank selection: %v %v", reads, err)
	}
	// string_list keeps normalising, because permissions depend on it.
	reads, _, _, err = run(`{"field":"a","op":"in","value":"$param.normalised"}`, map[string]any{"normalised": []any{"", " a "}}, "a")
	if err != nil || !strings.Contains(reads[0], `"value":["a"]`) {
		t.Fatalf("string_list: %v %v", reads, err)
	}

	// Lists are validated like string_list.
	for _, args := range []map[string]any{{"centres": "a"}, {"centres": []any{1}}, {"centres": make([]any, 1001)}} {
		if _, _, _, err := run(where, args, "a"); pipelineErrorCode(err) != "invalid_request" {
			t.Fatalf("%.40v: %v", args, err)
		}
	}

	// A value the condition needs fails the request, after no step read.
	for name, args := range map[string]map[string]any{
		"omitted":       {"status": ""},
		"explicit null": {"centres": nil, "status": ""},
		"omitted other": {"centres": []any{"a"}},
	} {
		_, p, _, err := run(where, args, "a")
		if pipelineErrorCode(err) != "invalid_request" {
			t.Fatalf("%s: %v", name, err)
		}
		if reads := conditionalReads(t, p); len(reads) != 0 {
			t.Fatalf("%s reached Database: %v", name, reads)
		}
	}

	// Over 100 selected values are chunked like any other list.
	long := []any{}
	for i := 0; i < 250; i++ {
		long = append(long, fmt.Sprintf("c%03d", i))
	}
	reads, _, _, err = run(where, map[string]any{"centres": long, "status": ""}, "a")
	if err != nil || !strings.Contains(reads[0], `"or"`) {
		t.Fatalf("long selection: %.200v %v", reads, err)
	}

	// Metric filters: a false condition leaves the metric counting every row.
	aggregate := func(args map[string]any) string {
		t.Helper()
		plan := conditionalPlan(t, authorisedFilter)
		plan["steps"] = decodeConfig(t, `{"s":[{"id":"rows","op":"aggregate","args":{"collection":"leads","groupBy":["status"],
		  "where":`+authorisedFilter+`,
		  "metrics":[{"name":"n","op":"count","where":{"and":[`+selectedCentres+`]}}]}}]}`)["s"]
		plan["result"] = "$steps.rows"
		plan["graphql_args"] = args
		a, p := pipelineApp(t, conditionalResponder("a"))
		if _, err := a.callDatabasePipeline(pipelineIdentity("7"), plan); err != nil {
			t.Fatal(err)
		}
		batch := p.batches[len(p.batches)-1]["operations"].([]any)
		encoded, _ := json.Marshal(batch[0].(map[string]any)["args"].(map[string]any)["metrics"])
		return string(encoded)
	}
	if got := aggregate(map[string]any{"centres": []any{}}); strings.Contains(got, "where") {
		t.Fatalf("false metric condition: %s", got)
	}
	if got := aggregate(map[string]any{"centres": []any{"a"}}); !strings.Contains(got, `"where":{"and":[{"field":"centre_id","op":"in","value":["a"]}]}`) {
		t.Fatalf("true metric condition: %s", got)
	}
}
