package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
)

const (
	relationOn      = `"on":[{"localField":"id","foreignField":"call_id"}]`
	relatedAllowed  = `{"exists":{"collection":"sales",` + relationOn + `,"where":{"field":"offer","op":"in","value":"$scope.allowed"}}}`
	relatedOther    = `{"not_exists":{"collection":"sales",` + relationOn + `,"where":{"field":"offer","op":"not_in","value":"$scope.allowed"}}}`
	grantsListed    = `{"op":"gt","args":[{"op":"length","args":["$scope.allowed"]},{"const":0}]}`
	restrictedByUse = `{"access_when":` + grantsListed + `,"filter":{"and":[` + relatedAllowed + `,` + relatedOther + `]}}`
)

func relationPlanWith(t *testing.T, where string) map[string]any {
	t.Helper()
	return conditionalPlan(t, where)
}

func TestRelationalFilterValidationRefuses(t *testing.T) {
	relation := func(clause string) string { return `{"exists":{"collection":"sales",` + clause + `}}` }
	pairs := func(n int) string {
		list := []string{}
		for i := 0; i < n; i++ {
			list = append(list, `{"localField":"a","foreignField":"b"}`)
		}
		return `[` + strings.Join(list, ",") + `]`
	}
	alternatives := func(n int) string {
		list := []string{}
		for i := 0; i < n; i++ {
			list = append(list, `{`+relationOn+`}`)
		}
		return `[` + strings.Join(list, ",") + `]`
	}
	nine := []string{}
	for i := 0; i < maxRelationNodes+1; i++ {
		nine = append(nine, relatedAllowed)
	}
	for name, c := range map[string]struct{ where, want string }{
		"beside another key":     {`{"exists":{"collection":"sales",` + relationOn + `},"field":"a"}`, "only key"},
		"not an object":          {`{"exists":"sales"}`, "must be an object"},
		"unknown key":            {relation(relationOn + `,"limit":1`), "limit"},
		"no collection":          {`{"exists":{` + relationOn + `}}`, "literal collection"},
		"placeholder collection": {`{"exists":{"collection":"$param.status",` + relationOn + `}}`, "literal collection"},
		"neither on nor anyOf":   {`{"exists":{"collection":"sales"}}`, "exactly one"},
		"both on and anyOf":      {relation(relationOn + `,"anyOf":[{` + relationOn + `}]`), "exactly one"},
		"empty on":               {relation(`"on":[]`), "1 to 8"},
		"too many pairs":         {relation(`"on":` + pairs(9)), "1 to 8"},
		"empty anyOf":            {relation(`"anyOf":[]`), "1 to 16"},
		"too many alternatives":  {relation(`"anyOf":` + alternatives(17)), "1 to 16"},
		"alternative key":        {relation(`"anyOf":[{` + relationOn + `,"where":{}}]`), "where"},
		"pair key":               {relation(`"on":[{"localField":"a","foreignField":"b","x":1}]`), "x"},
		"placeholder field":      {relation(`"on":[{"localField":"$param.status","foreignField":"b"}]`), "literal localField"},
		"placeholder related":    {relation(`"on":[{"localField":"a","foreignField":"$scope.allowed"}]`), "literal localField"},
		"comparison":             {relation(`"on":[{"localField":"a","foreignField":"b","comparison":"loose"}]`), "comparison"},
		"nested":                 {relation(relationOn + `,"where":` + relatedOther), "another exists"},
		"nested in a group":      {relation(relationOn + `,"where":{"and":[` + relatedOther + `]}`), "another exists"},
		"client when in where":   {relation(relationOn + `,"where":{"and":[` + selectedCentres + `]}`), "client when"},
		"search in where":        {relation(relationOn + `,"where":{"fields":["a"],"op":"like_contains_ascii","value":"$param.status"}`), "like_contains_ascii"},
		"bad operator in where":  {relation(relationOn + `,"where":{"field":"a","op":"contains","value":"x"}`), "unsupported op"},
		"too many":               {`{"and":[` + strings.Join(nine, ",") + `]}`, "at most 8"},
		"client value negated":   {`{"not_exists":{"collection":"sales",` + relationOn + `,"where":{"field":"offer","op":"eq","value":"$param.status"}}}`, "client input"},
		"client list negated":    {`{"not_exists":{"collection":"sales",` + relationOn + `,"where":{"field":"offer","op":"not_in","value":"$param.centres"}}}`, "client input"},
		"client value in group":  {`{"not_exists":{"collection":"sales",` + relationOn + `,"where":{"and":[{"or":[{"field":"offer","op":"in","value":["$param.status"]}]}]}}}`, "client input"},
		"inside a client when":   {`{"and":[{"when":` + selectedWhen + `,"filter":` + relatedAllowed + `}]}`, "relationships are policy"},
		// Selection by trusted values.
		"selection at the root":   {restrictedByUse, "child of an and"},
		"selection under or":      {`{"or":[` + authorisedFilter + `,` + restrictedByUse + `]}`, "inside or"},
		"selection under and-or":  {`{"or":[` + authorisedFilter + `,{"and":[` + restrictedByUse + `]}]}`, "inside or"},
		"selection in a related":  {`{"or":[{"exists":{"collection":"sales",` + relationOn + `,"where":{"and":[` + restrictedByUse + `]}}}]}`, "inside or"},
		"selection key":           {`{"and":[{"access_when":` + conditionTrue + `,"filter":` + authorisedFilter + `,"also":1}]}`, "also"},
		"selection, no filter":    {`{"and":[{"access_when":` + conditionTrue + `}]}`, "requires both"},
		"selection, no condition": {`{"and":[{"access_when":null,"filter":` + authorisedFilter + `}]}`, "requires both"},
		"client param selects":    {`{"and":[{"access_when":{"op":"eq","args":["$param.status",{"const":"all"}]},"filter":` + relatedAllowed + `}]}`, "client input"},
		"client list selects":     {`{"and":[{"access_when":{"op":"gt","args":[{"op":"length","args":["$param.centres"]},{"const":0}]},"filter":` + relatedAllowed + `}]}`, "client input"},
		"client flag selects":     {`{"and":[{"access_when":"$param.flag","filter":` + relatedAllowed + `}]}`, "client input"},
		"direct argument":         {`{"and":[{"access_when":{"op":"eq","args":["$args.status",{"const":"all"}]},"filter":` + relatedAllowed + `}]}`, "$param"},
		"not Boolean":             {`{"and":[{"access_when":{"op":"length","args":["$scope.allowed"]},"filter":` + relatedAllowed + `}]}`, "must be Boolean"},
		"list as Boolean":         {`{"and":[{"access_when":"$scope.allowed","filter":` + relatedAllowed + `}]}`, "must be Boolean"},
		"unknown access value":    {`{"and":[{"access_when":{"op":"gt","args":[{"op":"length","args":["$scope.nope"]},{"const":0}]},"filter":` + relatedAllowed + `}]}`, "unknown $scope.nope"},
		"undeclared param":        {`{"and":[{"access_when":{"op":"eq","args":["$param.nope",{"const":"a"}]},"filter":` + relatedAllowed + `}]}`, "undeclared"},
		"unsupported op":          {`{"and":[{"access_when":{"op":"if","args":[{"const":true}]},"filter":` + relatedAllowed + `}]}`, "not allowed"},
		"in over text":            {`{"and":[{"access_when":{"op":"in","args":[{"const":"a"},{"const":"abc"}]},"filter":` + relatedAllowed + `}]}`, "in requires"},
		"in in a client when":     {`{"and":[{"when":{"op":"in","args":[{"const":"a"},"$param.centres"]},"filter":` + selectedFilter + `}]}`, "not allowed"},
		"selection nested":        {`{"and":[{"access_when":` + conditionTrue + `,"filter":{"and":[` + restrictedByUse + `]}}]}`, "another when"},
		"client when inside":      {`{"and":[{"access_when":` + conditionTrue + `,"filter":{"and":[` + selectedCentres + `]}}]}`, "another when"},
		"client value inside":     {`{"and":[{"access_when":` + conditionTrue + `,"filter":{"field":"a","op":"eq","value":"$param.status"}}]}`, "client input"},
		"client value related":    {`{"and":[{"access_when":` + conditionTrue + `,"filter":{"exists":{"collection":"sales",` + relationOn + `,"where":{"field":"a","op":"eq","value":"$param.status"}}}}]}`, "client input"},
	} {
		_, err := validateDatabasePipeline(relationPlanWith(t, c.where))
		if err == nil {
			t.Errorf("%s was accepted", name)
		} else if !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: want %q in: %v", name, c.want, err)
		}
	}

	// Access reads run before filters are selected.
	for name, where := range map[string]string{"relation": relatedAllowed, "selection": `{"and":[` + restrictedByUse + `]}`} {
		plan := conditionalPlan(t, authorisedFilter)
		otherwise := plan["access"].(map[string]any)["values"].(map[string]any)["allowed"].(map[string]any)["otherwise"].(map[string]any)
		otherwise["where"] = decodeConfig(t, `{"w":`+where+`}`)["w"]
		if _, err := validateDatabasePipeline(plan); err == nil || !strings.Contains(err.Error(), "access allowed") {
			t.Errorf("access read with a %s: %v", name, err)
		}
	}
	// Database refuses relationships in metric filters.
	plan := conditionalPlan(t, authorisedFilter)
	plan["steps"] = decodeConfig(t, `{"s":[{"id":"rows","op":"aggregate","args":{"collection":"leads","groupBy":["status"],"metrics":[
	  {"name":"n","op":"count","where":`+relatedAllowed+`}]}}]}`)["s"]
	plan["result"] = "$steps.rows"
	if _, err := validateDatabasePipeline(plan); err == nil || !strings.Contains(err.Error(), "metric filter") {
		t.Errorf("metric: %v", err)
	}
	// Plain resolvers, whatever the letter case.
	for _, where := range []string{relatedAllowed, relatedOther, `{"and":[{"EXISTS":{"collection":"s"}}]}`, `{"or":[{"Not_Exists":{"collection":"s"}}]}`, `{"and":[` + restrictedByUse + `]}`} {
		if err := validateDatabaseResolverOptions("find", decodeConfig(t, `{"collection":"leads","where":`+where+`}`)); err == nil || !strings.Contains(err.Error(), "database_pipeline") {
			t.Errorf("plain resolver %s: %v", where, err)
		}
	}
}

func TestRelationalFilterValidationAccepts(t *testing.T) {
	both := `{"and":[` + authorisedFilter + `,` + relatedAllowed + `,` + relatedOther + `]}`
	for name, where := range map[string]string{
		"exists and not_exists": both,
		"at the root":           relatedAllowed,
		"inside or":             `{"or":[` + authorisedFilter + `,` + relatedAllowed + `]}`,
		"without where":         `{"exists":{"collection":"sales",` + relationOn + `}}`,
		"text comparison":       `{"exists":{"collection":"sales","on":[{"localField":"sale","foreignField":"id","comparison":"text"}]}}`,
		"composite key":         `{"exists":{"collection":"sales","on":[{"localField":"a","foreignField":"a"},{"localField":"b","foreignField":"b"}]}}`,
		"alternatives":          `{"exists":{"collection":"sales","anyOf":[{` + relationOn + `},{"on":[{"localField":"a","foreignField":"b"}]}]}}`,
		"client value narrows":  `{"exists":{"collection":"sales",` + relationOn + `,"where":{"field":"a","op":"eq","value":"$param.status"}}}`,
		"identity in negation":  `{"not_exists":{"collection":"sales",` + relationOn + `,"where":{"field":"a","op":"eq","value":"$param.user"}}}`,
		"fixed in negation":     `{"not_exists":{"collection":"sales",` + relationOn + `,"where":{"field":"a","op":"eq","value":"$param.fixed"}}}`,
		"selection":             `{"and":[` + authorisedFilter + `,` + restrictedByUse + `]}`,
		"selection in related":  `{"exists":{"collection":"sales",` + relationOn + `,"where":{"and":[{"access_when":` + grantsListed + `,"filter":` + authorisedFilter + `}]}}}`,
		"both kinds":            `{"and":[` + authorisedFilter + `,` + restrictedByUse + `,` + selectedCentres + `]}`,
		"identity selects":      `{"and":[{"access_when":{"op":"not","args":[{"op":"in","args":[{"const":"all"},"$param.perms"]}]},"filter":` + relatedAllowed + `}]}`,
		"identity text selects": `{"and":[{"access_when":{"op":"neq","args":["$param.user",{"const":"7"}]},"filter":` + relatedAllowed + `}]}`,
		"not_in":                `{"field":"a","op":"not_in","value":["x","y"]}`,
		"client not_in":         `{"and":[{"when":` + selectedWhen + `,"filter":{"field":"a","op":"not_in","value":"$param.centres"}}]}`,
	} {
		if _, err := validateDatabasePipeline(relationPlanWith(t, where)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
	plan, err := validateDatabasePipeline(relationPlanWith(t, `{"and":[`+authorisedFilter+`,`+restrictedByUse+`,{"exists":{"collection":"visits",`+relationOn+`}}]}`))
	if err != nil {
		t.Fatal(err)
	}
	if got := fmt.Sprint(plan.Collections()); got != "[leads profiles sales visits]" {
		t.Fatalf("related collections must be watched, including behind a selection and on the count: %s", got)
	}
}

func TestRelationalFilterBinding(t *testing.T) {
	bound := func(where string, identity []string, args map[string]any, allowed ...any) ([]string, *dbPipelinePlatform, any, error) {
		t.Helper()
		plan := conditionalPlan(t, where)
		original, _ := json.Marshal(plan)
		plan["graphql_args"] = args
		a, p := pipelineApp(t, conditionalResponder(allowed...))
		out, err := a.callDatabasePipeline(pipelineIdentity("7", identity...), plan)
		delete(plan, "graphql_args")
		if after, _ := json.Marshal(plan); string(after) != string(original) {
			t.Fatalf("binding changed the stored plan: %s", after)
		}
		if err != nil {
			return nil, p, out, err
		}
		reads := conditionalReads(t, p)
		for _, read := range reads {
			if strings.Contains(read, "access_when") || strings.Contains(read, "$scope") || strings.Contains(read, "$param") {
				t.Fatalf("an unresolved filter reached Database: %s", read)
			}
		}
		return reads, p, out, nil
	}
	where := `{"and":[` + authorisedFilter + `,` + restrictedByUse + `]}`
	scope := `{"field":"centre_id","op":"in","value":["a"]}`
	positive := `{"exists":{"collection":"sales","on":[{"foreignField":"call_id","localField":"id"}],"where":{"field":"offer","op":"in","value":["a"]}}}`
	negative := `{"not_exists":{"collection":"sales","on":[{"foreignField":"call_id","localField":"id"}],"where":{"field":"offer","op":"not_in","value":["a"]}}}`

	// Trusted values bind into the relationship, for the page and its total, in
	// one batch.
	reads, p, _, err := bound(where, nil, map[string]any{}, "a")
	if err != nil {
		t.Fatal(err)
	}
	want := `{"and":[` + scope + `,{"and":[` + positive + `,` + negative + `]}]}`
	if len(reads) != 2 || reads[0] != want || reads[1] != want {
		t.Fatalf("restricted: %v\nwant %s", reads, want)
	}
	if len(p.batches) != 2 {
		t.Fatalf("access then one batch for page and total, got %d", len(p.batches))
	}

	// An empty trusted set selects nothing here, so the relationship isn't sent
	// and the scope still applies: nothing is read.
	reads, _, out, err := bound(where, nil, map[string]any{})
	if err != nil || len(reads) != 0 || fmt.Sprint(out.(map[string]any)["total"]) != "0" {
		t.Fatalf("empty selection: %v %v %v", reads, out, err)
	}
	// The selection alone can leave no filter.
	reads, _, _, err = bound(`{"and":[`+restrictedByUse+`]}`, nil, map[string]any{})
	if err != nil || len(reads) != 2 || reads[0] != "null" {
		t.Fatalf("nothing left: %v %v", reads, err)
	}

	// A client's arguments can't change whether the policy applies.
	for _, args := range []map[string]any{{"status": "all"}, {"flag": true}, {"centres": []any{}}, {"allowed": []any{}}, {"access_when": false}} {
		reads, _, _, err := bound(where, nil, args, "a")
		if err != nil || reads[0] != want {
			t.Fatalf("%v changed the policy: %v %v", args, reads, err)
		}
	}

	// Identity selects: a bypass permission removes it, and only that does.
	bypass := `{"and":[` + authorisedFilter + `,{"access_when":{"op":"not","args":[{"op":"in","args":[{"const":"all"},"$param.perms"]}]},"filter":` + relatedAllowed + `}]}`
	reads, _, _, err = bound(bypass, []string{"all"}, map[string]any{}, "a")
	if err != nil || strings.Contains(reads[0], "exists") {
		t.Fatalf("bypass: %v %v", reads, err)
	}
	for _, permissions := range [][]string{nil, {}, {"other"}} {
		reads, _, _, err = bound(bypass, permissions, map[string]any{}, "a")
		if err != nil || !strings.Contains(reads[0], "exists") {
			t.Fatalf("no bypass with %v: %v %v", permissions, reads, err)
		}
	}

	// A selection inside a relationship can leave it with no where.
	inner := `{"exists":{"collection":"sales",` + relationOn + `,"where":{"and":[{"access_when":` + grantsListed + `,"filter":` + authorisedFilter + `}]}}}`
	reads, _, _, err = bound(inner, nil, map[string]any{})
	if err != nil || strings.Contains(reads[0], "where") {
		t.Fatalf("emptied related filter: %v %v", reads, err)
	}
	reads, _, _, err = bound(inner, nil, map[string]any{}, "a")
	if err != nil || !strings.Contains(reads[0], `"where":{"and":[`+scope) {
		t.Fatalf("kept related filter: %v %v", reads, err)
	}

	// exists over an empty list can't hold: nothing is read. not_exists over an
	// empty list always holds: Database gets a filter matching nothing.
	emptyList := func(key string) string {
		return `{"` + key + `":{"collection":"sales",` + relationOn + `,"where":{"field":"offer","op":"in","value":"$scope.allowed"}}}`
	}
	reads, _, out, err = bound(emptyList("exists"), nil, map[string]any{})
	if err != nil || len(reads) != 0 || fmt.Sprint(out.(map[string]any)["total"]) != "0" {
		t.Fatalf("exists over nothing: %v %v %v", reads, out, err)
	}
	reads, _, _, err = bound(emptyList("not_exists"), nil, map[string]any{})
	if err != nil || len(reads) != 2 || !strings.Contains(reads[0], `"op":"is_null"`) {
		t.Fatalf("not_exists over nothing: %v %v", reads, err)
	}

	// not_in over nothing still refuses nulls, and long lists are ANDed chunks.
	negated := `{"field":"offer","op":"not_in","value":"$scope.allowed"}`
	reads, _, _, err = bound(negated, nil, map[string]any{})
	if err != nil || reads[0] != `{"field":"offer","op":"is_not_null"}` {
		t.Fatalf("empty not_in: %v %v", reads, err)
	}
	long := []any{}
	for i := 0; i < 250; i++ {
		long = append(long, fmt.Sprintf("o%03d", i))
	}
	reads, _, _, err = bound(negated, nil, map[string]any{}, long...)
	if err != nil {
		t.Fatal(err)
	}
	var chunks struct {
		And []struct {
			Op    string `json:"op"`
			Value []any  `json:"value"`
		} `json:"and"`
	}
	if err := json.Unmarshal([]byte(reads[0]), &chunks); err != nil || len(chunks.And) != 3 || chunks.And[0].Op != "not_in" || len(chunks.And[0].Value) != 100 || len(chunks.And[2].Value) != 50 {
		t.Fatalf("long not_in: %.200s %v", reads[0], err)
	}
	reads, _, _, err = bound(relatedAllowed, nil, map[string]any{}, long...)
	if err != nil || !strings.Contains(reads[0], `"where":{"or":[`) {
		t.Fatalf("long in inside a relationship: %.200v %v", reads, err)
	}

	// A condition that can't be evaluated fails the request, before any step.
	missing := `{"and":[{"access_when":{"op":"eq","args":["$param.perms",{"const":[]}]},"filter":` + relatedAllowed + `}]}`
	if _, err := validateDatabasePipeline(conditionalPlan(t, missing)); err == nil {
		t.Fatal("comparing lists was accepted")
	}
}

func TestConditionEvaluationWithAccessValues(t *testing.T) {
	scope := map[string][]string{"filled": {"a", "b"}, "empty": {}}
	params := map[string]any{"perms": []any{"read"}, "none": nil, "n": int64(1)}
	for expression, want := range map[string]bool{
		`{"op":"gt","args":[{"op":"length","args":["$scope.filled"]},{"const":0}]}`:  true,
		`{"op":"gt","args":[{"op":"length","args":["$scope.empty"]},{"const":0}]}`:   false,
		`{"op":"eq","args":[{"op":"length","args":["$scope.absent"]},{"const":0}]}`:  true,
		`{"op":"in","args":[{"const":"read"},"$param.perms"]}`:                       true,
		`{"op":"in","args":[{"const":"write"},"$param.perms"]}`:                      false,
		`{"op":"in","args":[{"const":"b"},"$scope.filled"]}`:                         true,
		`{"op":"in","args":[{"const":"read"},"$scope.empty"]}`:                       false,
		`{"op":"in","args":["$param.n",{"const":["1"]}]}`:                            true,
		`{"op":"not","args":[{"op":"in","args":[{"const":"read"},"$param.perms"]}]}`: false,
	} {
		var raw any
		if err := json.Unmarshal([]byte(expression), &raw); err != nil {
			t.Fatal(err)
		}
		got, err := evalCondition(raw, params, scope)
		if err != nil || got != want {
			t.Errorf("%s: got %v (%v), want %v", expression, got, err, want)
		}
	}
	// A missing identity list fails closed instead of reading as "not listed".
	var raw any
	if err := json.Unmarshal([]byte(`{"op":"not","args":[{"op":"in","args":[{"const":"all"},"$param.none"]}]}`), &raw); err != nil {
		t.Fatal(err)
	}
	if got, err := evalCondition(raw, params, scope); pipelineErrorCode(err) != "invalid_request" {
		t.Errorf("missing list: %v %v", got, err)
	}
}

func TestClientFilterRelationDetection(t *testing.T) {
	for _, where := range []string{
		`{"exists":{}}`, `{"not_exists":{}}`, `{"EXISTS":{}}`, `{"Not_Exists":{}}`, `{"and":[{"exists":{}}]}`, `{"AND":[{"or":[{"Exists":{}}]}]}`,
	} {
		var raw any
		if err := json.Unmarshal([]byte(where), &raw); err != nil {
			t.Fatal(err)
		}
		if !untrustedFilterHasRelation(raw) {
			t.Errorf("%s not found", where)
		}
	}
	for _, where := range []string{`{"field":"exists","op":"eq","value":"not_exists"}`, `{"and":[{"field":"a","op":"in","value":["exists"]}]}`, `null`} {
		var raw any
		if err := json.Unmarshal([]byte(where), &raw); err != nil {
			t.Fatal(err)
		}
		if untrustedFilterHasRelation(raw) {
			t.Errorf("%s reported", where)
		}
	}
	// Refused before Database is called.
	a, p := pipelineApp(t, conditionalResponder())
	config := map[string]any{"_project_id": "p1", "database": "crm", "collection": "leads",
		"graphql_args": map[string]any{"where": map[string]any{"and": []any{map[string]any{"Exists": map[string]any{"collection": "private"}}}}}}
	if _, err := a.callDatabase(pipelineIdentity("7"), "find", config); pipelineErrorCode(err) != "invalid_request" || len(p.batches) != 0 {
		t.Fatalf("client relationship: %v", err)
	}
}

func TestProbeRelationDropsValues(t *testing.T) {
	node := decodeConfig(t, `{"exists":{"collection":"sales","anyOf":[{`+relationOn+`}],"where":{"and":[
	  {"field":"offer","op":"in","value":"$scope.allowed"},
	  {"or":[{"field":"a","op":"not_in","value":["x"]},{"field":"b","op":"is_null"}]},
	  {"access_when":`+conditionTrue+`,"filter":{"field":"c","op":"eq","value":"$scope.allowed"}}]}}}`)
	encoded, _ := json.Marshal(probeRelation(node))
	got := string(encoded)
	want := `{"exists":{"anyOf":[{"on":[{"foreignField":"call_id","localField":"id"}]}],"collection":"sales","where":{"and":[{"field":"offer","op":"is_null"},{"or":[{"field":"a","op":"is_null"},{"field":"b","op":"is_null"}]},{"field":"c","op":"is_null"}]}}}`
	if got != want {
		t.Fatalf("probe:\n%s\nwant\n%s", got, want)
	}
	if _, clause := relationClause(node); !strings.Contains(fmt.Sprint(clause["where"]), "$scope.allowed") {
		t.Fatal("probing changed the stored node")
	}
}
