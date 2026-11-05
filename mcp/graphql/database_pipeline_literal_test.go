package main

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
	"testing"

	tk "github.com/apteva/app-sdk/testkit"
)

const (
	literalQGiven    = `{"op":"neq","args":["$param.q",{"const":""}]}`
	literalPhoneLong = `{"op":"gte","args":[{"op":"length","args":["$param.phone"]},{"const":6}]}`
	literalQ         = `{"field":"label","op":"contains_ascii","value":"$param.q"}`
	literalPhone     = `{"field":"digits","op":"contains_ascii","value":"$param.phone"}`
	literalOn        = `"on":[{"localField":"sale_ref","foreignField":"id"}]`
	literalCentre    = `{"field":"centre_id","op":"in","value":"$scope.allowed"}`
	literalOffers    = `{"field":"offer_id","op":"in","value":"$scope.offers"}`
	literalRestrict  = `{"access_when":{"op":"gt","args":[{"op":"length","args":["$scope.offers"]},{"const":0}]},"filter":` + literalOffers + `}`
)

func literalExists(where string) string {
	return `{"exists":{"collection":"sales",` + literalOn + `,"where":` + where + `}}`
}

func literalNotExists(where string) string {
	return `{"not_exists":{"collection":"sales",` + literalOn + `,"where":` + where + `}}`
}

func literalWhen(condition, filter string) string {
	return `{"when":` + condition + `,"filter":` + filter + `}`
}

// literalGroup is the optional search: skipped when q is blank, and always beside
// the mandatory scope, never in place of it.
func literalGroup(filter string) string {
	return `{"and":[` + authorisedFilter + `,` + literalWhen(literalQGiven, filter) + `]}`
}

// literalPlan is a scoped page with a total. Its params cover every kind of source
// a search value could be pointed at.
func literalPlan(t *testing.T, where string) map[string]any {
	t.Helper()
	plan := decodeConfig(t, `{
	  "params": {
	    "user": {"from": "$identity.subject", "required": true},
	    "q": {"from": "$args.q", "type": "string", "required": true},
	    "phone": {"from": "$args.phone", "type": "string"},
	    "number": {"from": "$args.number", "type": "number"},
	    "list": {"from": "$args.list", "type": "exact_string_list"},
	    "trimmed": {"from": "$args.trimmed", "type": "string_list"},
	    "nested": {"from": "$args.input.q", "type": "string"},
	    "parent": {"from": "$parent.q", "type": "string"},
	    "fixed": {"value": "vert"},
	    "blank": {"value": ""}
	  },
	  "access": {"values": {
	    "allowed": {"optional": true, "otherwise": {"collection": "profiles", "field": "centre_id", "where": {"field": "user_id", "op": "eq", "value": "$param.user"}}},
	    "offers": {"optional": true, "otherwise": {"collection": "grants", "field": "offer_id", "where": {"field": "user_id", "op": "eq", "value": "$param.user"}}}}},
	  "steps": [
	    {"id": "rows", "op": "find", "max_rows": 100, "args": {"collection": "leads", "where": `+where+`, "orderBy": [{"field": "id"}]}},
	    {"id": "total", "op": "count", "count_of": "rows"}],
	  "result": {"rows": "$steps.rows", "total": "$steps.total"}}`)
	plan["database"] = "crm"
	plan["_project_id"] = "p1"
	return plan
}

func TestLiteralSearchAccepted(t *testing.T) {
	clause := `{"and":[` + literalCentre + `,` + literalRestrict + `,{"or":[` + literalQ + `,{"field":"contract","op":"contains_ascii","value":"$param.q"}]}]}`
	for name, where := range map[string]string{
		"alone":                 literalQ,
		"beside the scope":      `{"and":[` + authorisedFilter + `,` + literalQ + `]}`,
		"literal value":         `{"field":"label","op":"contains_ascii","value":"vert"}`,
		"fixed param":           `{"field":"label","op":"contains_ascii","value":"$param.fixed"}`,
		"under a when":          literalGroup(literalQ),
		"or under a when":       literalGroup(`{"or":[` + literalQ + `,{"field":"name","op":"contains_ascii","value":"$param.q"}]}`),
		"exists under a when":   literalGroup(literalExists(literalQ)),
		"checks on the related": literalGroup(`{"or":[` + literalQ + `,` + literalExists(clause) + `]}`),
		"a chain":               literalGroup(literalExists(`{"and":[` + literalCentre + `,` + literalExists(clause) + `]}`)),
		"phone branch":          literalGroup(`{"or":[` + literalQ + `,` + literalWhen(literalPhoneLong, literalPhone) + `]}`),
		"phone branch, related": literalGroup(literalExists(`{"and":[` + literalCentre + `,{"or":[` + literalQ + `,` + literalWhen(literalPhoneLong, literalPhone) + `]}]}`)),
		"trusted not_exists in a clause": literalGroup(literalExists(`{"and":[` + literalQ + `,` +
			literalNotExists(`{"field":"offer_id","op":"not_in","value":"$scope.offers"}`) + `]}`)),
		"policy exists in a clause": literalGroup(literalExists(`{"and":[` + literalQ + `,` + literalExists(literalOffers) + `]}`)),
		"selected policy check":     literalGroup(literalExists(`{"and":[` + literalQ + `,{"access_when":{"const":true},"filter":` + literalExists(literalOffers) + `}]}`)),
		"literal in a policy or":    `{"and":[` + authorisedFilter + `,{"or":[{"field":"label","op":"contains_ascii","value":"vert"},{"field":"status","op":"eq","value":"open"}]}]}`,
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := validateDatabasePipeline(literalPlan(t, where)); err != nil {
				t.Error(err)
			}
		})
	}
	metric := decodeConfig(t, `{"params":{"q":{"from":"$args.q","type":"string"}},"steps":[{"id":"s","op":"aggregate","args":{"collection":"c","metrics":[{"name":"n","op":"count","where":`+literalQ+`}]}}],"result":"$steps.s"}`)
	if _, err := validateDatabasePipeline(metric); err != nil {
		t.Errorf("metric: %v", err)
	}
}

func TestLiteralSearchRefused(t *testing.T) {
	value := func(raw string) string { return `{"field":"label","op":"contains_ascii","value":` + raw + `}` }
	guardedClause := func(where string) string { return literalGroup(literalExists(where)) }
	for name, c := range map[string]struct{ where, want string }{
		// The value is a scalar string; the resolver owns the field and operator.
		"number param":       {value(`"$param.number"`), "must be a string param"},
		"list param":         {value(`"$param.list"`), "must be a string param"},
		"trimmed list param": {value(`"$param.trimmed"`), "must be a string param"},
		"identity value":     {value(`"$param.user"`), "must come from a top-level"},
		"nested argument":    {value(`"$param.nested"`), "must come from a top-level"},
		"parent value":       {value(`"$param.parent"`), "must come from a top-level"},
		"access value":       {value(`"$scope.allowed"`), "must be a string or a string $param"},
		"unknown param":      {value(`"$param.nope"`), "unknown $param.nope"},
		"number value":       {value(`1`), "requires a string value"},
		"list value":         {value(`["a"]`), "requires a string value"},
		"null value":         {value(`null`), "requires a string value"},
		"no value":           {`{"field":"label","op":"contains_ascii"}`, "requires a string value"},
		"long literal":       {value(`"` + strings.Repeat("x", 1025) + `"`), "limited to 1024 bytes"},
		"client field":       {`{"field":"$param.q","op":"contains_ascii","value":"$param.q"}`, "requires a literal field"},
		"no field":           {`{"op":"contains_ascii","value":"x"}`, "requires a literal field"},
		"several fields":     {`{"fields":["a","b"],"op":"contains_ascii","value":"x"}`, "unknown key"},
		"field and fields":   {`{"field":"a","fields":["b"],"op":"contains_ascii","value":"x"}`, "unknown key"},
		"extra key":          {`{"field":"a","op":"contains_ascii","value":"x","mode":"y"}`, "unknown key"},

		// A blank value matches every record, so it must not reach an or or an exists.
		"unguarded or":        {`{"and":[` + authorisedFilter + `,{"or":[` + literalQ + `,{"field":"status","op":"eq","value":"open"}]}]}`, "must sit in a when"},
		"unguarded exists":    {`{"and":[` + authorisedFilter + `,` + literalExists(literalQ) + `]}`, "must sit in a when"},
		"guard on another":    {`{"and":[` + authorisedFilter + `,` + literalWhen(literalPhoneLong, `{"or":[`+literalQ+`,`+literalPhone+`]}`) + `]}`, "must sit in a when"},
		"phone guarded by q":  {literalGroup(`{"or":[` + literalQ + `,` + literalPhone + `]}`), "must sit in a when"},
		"empty literal":       {literalGroup(`{"or":[` + literalQ + `,` + value(`""`) + `]}`), "matches every record"},
		"blank fixed param":   {literalGroup(`{"or":[` + literalQ + `,` + value(`"$param.blank"`) + `]}`), "matches every record"},
		"empty in a relation": {`{"and":[` + authorisedFilter + `,` + literalExists(value(`""`)) + `]}`, "matches every record"},

		// A search must never be an alternative to authorization.
		"scope alternative":           {`{"or":[` + authorisedFilter + `,` + literalQ + `]}`, "must not be an alternative to authorization"},
		"identity alternative":        {`{"or":[{"field":"owner","op":"eq","value":"$param.user"},` + literalQ + `]}`, "must not be an alternative to authorization"},
		"scope beside a related hit":  {`{"or":[` + authorisedFilter + `,` + literalExists(`{"field":"label","op":"contains_ascii","value":"vert"}`) + `]}`, "must not be an alternative to authorization"},
		"scope alternative, related":  {guardedClause(`{"or":[` + literalQ + `,` + literalOffers + `]}`), "must not be an alternative to authorization"},
		"nested scope alternative":    {guardedClause(`{"or":[{"and":[` + literalQ + `,` + literalOffers + `]},` + literalCentre + `]}`), "must not be an alternative to authorization"},
		"policy alternative, related": {guardedClause(`{"or":[` + literalQ + `,` + literalExists(literalOffers) + `]}`), "must not be an alternative to a policy"},
		"scope under the when":        {literalGroup(`{"or":[` + literalQ + `,` + authorisedFilter + `]}`), "must stay unconditional"},
		"scope and search in a when":  {literalGroup(`{"and":[` + literalQ + `,` + authorisedFilter + `]}`), "must stay unconditional"},
		"identity under the when":     {literalGroup(`{"and":[` + literalQ + `,{"field":"owner","op":"eq","value":"$param.user"}]}`), "identity value"},

		// Relationships stay policy, unless they search.
		"policy exists under a when":  {literalGroup(literalExists(literalOffers)), "cannot hold exists"},
		"policy exists beside search": {literalGroup(`{"and":[` + literalQ + `,` + literalExists(literalOffers) + `]}`), "cannot hold exists"},
		"not_exists under a when":     {literalGroup(literalNotExists(`{"field":"label","op":"contains_ascii","value":"vert"}`)), "cannot hold exists"},
		"search only selected":        {literalGroup(literalExists(`{"and":[{"access_when":{"const":true},"filter":{"field":"label","op":"contains_ascii","value":"vert"}}]}`)), "cannot hold exists"},
		"search only excluded":        {literalGroup(literalExists(literalNotExists(`{"field":"label","op":"contains_ascii","value":"vert"}`))), "cannot hold exists"},

		// Client input stays out of a negation, at any depth.
		"client search in not_exists":  {`{"and":[` + authorisedFilter + `,` + literalNotExists(literalQ) + `]}`, "comes from client input"},
		"client value, nested not":     {guardedClause(`{"and":[` + literalQ + `,` + literalNotExists(`{"field":"status","op":"eq","value":"$param.phone"}`) + `]}`), "comes from client input"},
		"client search, nested not":    {guardedClause(`{"and":[` + literalQ + `,` + literalNotExists(literalQ) + `]}`), "comes from client input"},
		"client search under not":      {`{"and":[` + authorisedFilter + `,` + literalNotExists(literalExists(literalQ)) + `]}`, "comes from client input"},
		"client when in a not_exists":  {guardedClause(`{"and":[` + literalQ + `,` + literalNotExists(`{"or":[{"field":"a","op":"eq","value":"x"},`+literalWhen(literalPhoneLong, `{"field":"b","op":"eq","value":"y"}`)+`]}`) + `]}`), "cannot hold a client when"},
		"client when in a policy":      {`{"and":[` + authorisedFilter + `,` + literalExists(`{"or":[{"field":"a","op":"eq","value":"x"},`+literalWhen(literalPhoneLong, literalPhone)+`]}`) + `]}`, "cannot hold a client when"},
		"client search in access_when": {`{"and":[` + authorisedFilter + `,{"access_when":{"const":true},"filter":` + literalQ + `}]}`, "comes from client input"},
		"client search in a selection": {guardedClause(`{"and":[` + literalQ + `,{"access_when":{"const":true},"filter":` + literalQ + `}]}`), "comes from client input"},

		// A condition only ever selects a branch of an or inside the search group.
		"when beside a related search": {guardedClause(`{"and":[` + literalQ + `,` + literalWhen(literalPhoneLong, literalPhone) + `]}`), "cannot contain another conditional"},
		"when beside a search":         {literalGroup(`{"and":[` + literalQ + `,` + literalWhen(literalPhoneLong, literalPhone) + `]}`), "cannot contain another conditional"},
		"when directly in a when":      {literalGroup(literalWhen(literalPhoneLong, literalPhone)), "cannot contain another conditional"},
		"when in an and in an or":      {literalGroup(`{"or":[` + literalQ + `,{"and":[` + literalWhen(literalPhoneLong, literalPhone) + `]}]}`), "inside or"},
		"every branch conditional":     {literalGroup(`{"or":[` + literalWhen(literalPhoneLong, literalPhone) + `,` + literalWhen(literalPhoneLong, literalPhone) + `]}`), "needs a branch without a condition"},
		"when in an or, no group":      {`{"and":[` + authorisedFilter + `,{"or":[{"field":"a","op":"eq","value":"x"},` + literalWhen(literalPhoneLong, literalPhone) + `]}]}`, "inside or"},
		"access_when in the group":     {literalGroup(`{"and":[` + literalQ + `,{"access_when":{"const":true},"filter":{"field":"a","op":"eq","value":"x"}}]}`), "cannot contain another conditional"},
		"access_when directly in or":   {`{"or":[{"field":"label","op":"contains_ascii","value":"vert"},{"access_when":{"const":true},"filter":{"field":"a","op":"eq","value":"x"}}]}`, "inside or"},
		"access_when, policy or":       {`{"or":[{"field":"a","op":"eq","value":"x"},` + literalExists(`{"and":[{"access_when":{"const":true},"filter":`+literalOffers+`}]}`) + `]}`, "inside or"},

		// Wildcard search keeps its old limits.
		"like in a relationship": {guardedClause(`{"and":[` + literalQ + `,{"fields":["a"],"op":"like_contains_ascii","value":"$param.q"}]}`), "cannot hold like_contains_ascii"},
		"like in the group or":   {literalGroup(`{"or":[` + literalQ + `,{"fields":["a"],"op":"like_contains_ascii","value":"$param.q"}]}`), "cannot be inside or"},
	} {
		t.Run(name, func(t *testing.T) {
			_, err := validateDatabasePipeline(literalPlan(t, c.where))
			if err == nil {
				t.Fatal("was accepted")
			}
			if !strings.Contains(err.Error(), c.want) {
				t.Errorf("got %q, want it to contain %q", err, c.want)
			}
		})
	}

	access := literalPlan(t, authorisedFilter)
	access["access"].(map[string]any)["values"].(map[string]any)["searched"] = decodeConfig(t, `{"optional":true,"otherwise":{"collection":"labels","field":"id","where":`+literalQ+`}}`)
	if _, err := validateDatabasePipeline(access); err == nil || !strings.Contains(err.Error(), "never decides authorization") {
		t.Errorf("a search in a scope lookup: %v", err)
	}
	metric := decodeConfig(t, `{"params":{"q":{"from":"$args.q","type":"string"}},"steps":[{"id":"s","op":"aggregate","args":{"collection":"c","metrics":[{"name":"n","op":"count","where":{"or":[`+literalQ+`,{"field":"a","op":"eq","value":"x"}]}}]}}],"result":"$steps.s"}`)
	if _, err := validateDatabasePipeline(metric); err == nil || !strings.Contains(err.Error(), "must sit in a when") {
		t.Errorf("an unguarded metric or: %v", err)
	}
}

// One request can use a selected policy and its search together, so they count
// together at publish.
func TestLiteralSearchCountsTowardsTheRelationshipLimit(t *testing.T) {
	policy := func(count int) string {
		parts := []string{authorisedFilter}
		for range count {
			parts = append(parts, literalExists(literalOffers))
		}
		return strings.Join(parts, ",")
	}
	if _, err := validateDatabasePipeline(literalPlan(t, `{"and":[`+policy(maxRelationNodes-1)+`,`+literalWhen(literalQGiven, literalExists(literalQ))+`]}`)); err != nil {
		t.Errorf("at the limit: %v", err)
	}
	_, err := validateDatabasePipeline(literalPlan(t, `{"and":[`+policy(maxRelationNodes)+`,`+literalWhen(literalQGiven, literalExists(literalQ))+`]}`))
	if err == nil || !strings.Contains(err.Error(), "holds 25 exists or not_exists filters; Database takes at most 24") {
		t.Errorf("past the limit: %v", err)
	}
}

// Publishing asks Database about all of a step's relationships together only past
// the older limit, so plans that already published send the same probes as before.
func TestRelationshipCountProbe(t *testing.T) {
	publish := func(count int) (separate, whole int, err error) {
		t.Helper()
		parts := []string{authorisedFilter}
		for range count {
			parts = append(parts, literalExists(literalOffers))
		}
		plan, failure := validateDatabasePipeline(literalPlan(t, `{"and":[`+strings.Join(parts, ",")+`]}`))
		if failure != nil {
			t.Fatal(failure)
		}
		recorder := &explainRecorder{}
		a := &App{ctx: tk.NewAppCtx(t, "apteva.yaml", tk.WithPlatform(recorder))}
		run := &databasePipelineRun{app: a, plan: plan, project: "p1"}
		err = run.verifyRelations(context.Background())
		for _, input := range recorder.inputs {
			if _, grouped := input["where"].(map[string]any)["and"]; grouped {
				whole++
			} else {
				separate++
			}
		}
		return separate, whole, err
	}
	// The page and its total are each planned.
	if separate, whole, err := publish(olderRelationNodes); err != nil || separate != 2*olderRelationNodes || whole != 0 {
		t.Errorf("within the older limit: %d separate, %d together, %v", separate, whole, err)
	}
	if separate, whole, err := publish(olderRelationNodes + 1); err != nil || separate != 2*(olderRelationNodes+1) || whole != 2 {
		t.Errorf("past the older limit: %d separate, %d together, %v", separate, whole, err)
	}
}

func TestLiteralSearchBinding(t *testing.T) {
	restricted := literalExists(`{"and":[` + literalCentre + `,` + literalRestrict + `,{"or":[` + literalQ + `,` + literalWhen(literalPhoneLong, literalPhone) + `]}]}`)
	where := literalGroup(`{"or":[{"field":"source","op":"contains_ascii","value":"$param.q"},` + restricted + `,` + literalWhen(literalPhoneLong, `{"field":"own_digits","op":"contains_ascii","value":"$param.phone"}`) + `]}`)
	run := func(q, phone string, offers ...any) ([]string, *dbPipelinePlatform, error) {
		t.Helper()
		plan := literalPlan(t, where)
		plan["graphql_args"] = map[string]any{"q": q, "phone": phone}
		a, p := pipelineApp(t, func(operations []any) (any, error) {
			out := []any{}
			for _, raw := range operations {
				operation := raw.(map[string]any)
				args := operation["args"].(map[string]any)
				switch {
				case args["collection"] == "profiles":
					out = append(out, map[string]any{"records": []any{map[string]any{"centre_id": "a"}}, "hasMore": false})
				case args["collection"] == "grants":
					records := []any{}
					for _, offer := range offers {
						records = append(records, map[string]any{"offer_id": offer})
					}
					out = append(out, map[string]any{"records": records, "hasMore": false})
				case operation["op"] == "count":
					out = append(out, map[string]any{"count": "1"})
				default:
					out = append(out, map[string]any{"records": []any{}, "hasMore": false})
				}
			}
			return map[string]any{"results": out}, nil
		})
		if _, err := a.callDatabasePipeline(pipelineIdentity("7"), plan); err != nil {
			return nil, p, err
		}
		reads := []string{}
		for _, batch := range p.batches {
			for _, raw := range batch["operations"].([]any) {
				args := raw.(map[string]any)["args"].(map[string]any)
				if args["collection"] == "profiles" || args["collection"] == "grants" {
					continue
				}
				encoded, err := json.Marshal(args["where"])
				if err != nil {
					t.Fatal(err)
				}
				if strings.Contains(string(encoded), `"when"`) || strings.Contains(string(encoded), `"access_when"`) || strings.Contains(string(encoded), `$`) {
					t.Fatalf("an unresolved node reached Database: %s", encoded)
				}
				reads = append(reads, string(encoded))
			}
		}
		if len(reads) != 2 || reads[0] != reads[1] {
			t.Fatalf("the page and its total must use one predicate: %v", reads)
		}
		return reads, p, nil
	}
	const scope = `{"field":"centre_id","op":"in","value":["a"]}`
	related := func(q string, branches string, checks string) string {
		return `{"exists":{"collection":"sales","on":[{"foreignField":"id","localField":"sale_ref"}],"where":{"and":[` + scope + checks + `,{"or":[{"field":"label","op":"contains_ascii","value":` + q + `}` + branches + `]}]}}}`
	}
	normalise := func(text string) string {
		var value any
		if err := json.Unmarshal([]byte(text), &value); err != nil {
			t.Fatalf("%s: %v", text, err)
		}
		encoded, _ := json.Marshal(value)
		return string(encoded)
	}

	// Blank q bypasses the whole group, whatever the phone says, so a report with no
	// display context stays visible under the mandatory scope alone.
	for _, phone := range []string{"", "0612345678"} {
		reads, _, err := run("", phone)
		if err != nil {
			t.Fatal(err)
		}
		if want := `{"and":[` + scope + `]}`; reads[0] != want {
			t.Errorf("blank q, phone %q: %s", phone, reads[0])
		}
	}

	// q reaches every field verbatim: spaces, case and wildcards included.
	for _, q := range []string{"100%", "A_B", `back\slash`, "  padded  ", " ", "é", `"quoted"`} {
		encoded, _ := json.Marshal(q)
		reads, _, err := run(q, "")
		if err != nil {
			t.Fatal(err)
		}
		want := normalise(`{"and":[` + scope + `,{"or":[{"field":"source","op":"contains_ascii","value":` + string(encoded) + `},` + related(string(encoded), "", "") + `]}]}`)
		if reads[0] != want {
			t.Errorf("q %q:\n got %s\nwant %s", q, reads[0], want)
		}
	}

	// A short phone drops only the phone branches, on the query and on the related
	// records alike; a long one adds them.
	reads, _, err := run("vert", "12345")
	if err != nil {
		t.Fatal(err)
	}
	if want := normalise(`{"and":[` + scope + `,{"or":[{"field":"source","op":"contains_ascii","value":"vert"},` + related(`"vert"`, "", "") + `]}]}`); reads[0] != want {
		t.Errorf("short phone:\n got %s\nwant %s", reads[0], want)
	}
	reads, _, err = run("vert", "123456")
	if err != nil {
		t.Fatal(err)
	}
	digits := `,{"field":"digits","op":"contains_ascii","value":"123456"}`
	if want := normalise(`{"and":[` + scope + `,{"or":[{"field":"source","op":"contains_ascii","value":"vert"},` + related(`"vert"`, digits, "") + `,{"field":"own_digits","op":"contains_ascii","value":"123456"}]}]}`); reads[0] != want {
		t.Errorf("long phone:\n got %s\nwant %s", reads[0], want)
	}

	// Trusted values select the offer check on the records being searched, and the
	// client can't turn it off.
	reads, _, err = run("vert", "", "offer-1")
	if err != nil {
		t.Fatal(err)
	}
	offer := `,{"field":"offer_id","op":"in","value":["offer-1"]}`
	if want := normalise(`{"and":[` + scope + `,{"or":[{"field":"source","op":"contains_ascii","value":"vert"},` + related(`"vert"`, "", offer) + `]}]}`); reads[0] != want {
		t.Errorf("offer check:\n got %s\nwant %s", reads[0], want)
	}
}

// A condition that lets a blank value through fails the request. It never turns
// into a match on every record.
func TestLiteralSearchBlankFailsClosed(t *testing.T) {
	always := `{"op":"gte","args":[{"op":"length","args":["$param.q"]},{"const":0}]}`
	for name, filter := range map[string]string{
		"or":     `{"or":[` + literalQ + `,{"field":"status","op":"eq","value":"open"}]}`,
		"exists": literalExists(literalQ),
		"nested": literalExists(`{"and":[` + literalCentre + `,{"or":[` + literalQ + `,` + literalExists(literalQ) + `]}]}`),
	} {
		t.Run(name, func(t *testing.T) {
			plan := literalPlan(t, `{"and":[`+authorisedFilter+`,`+literalWhen(always, filter)+`]}`)
			plan["graphql_args"] = map[string]any{"q": ""}
			a, p := pipelineApp(t, conditionalResponder("a"))
			_, err := a.callDatabasePipeline(pipelineIdentity("7"), plan)
			if err == nil || !strings.Contains(err.Error(), "would match every record") {
				t.Fatalf("got %v", err)
			}
			for _, batch := range p.batches {
				for _, raw := range batch["operations"].([]any) {
					if args := raw.(map[string]any)["args"].(map[string]any); args["collection"] == "leads" {
						t.Errorf("the page was read: %v", args["where"])
					}
				}
			}
		})
	}

	// Beside other filters a blank matches what they match, as documented.
	plan := literalPlan(t, `{"and":[`+authorisedFilter+`,`+literalQ+`]}`)
	plan["graphql_args"] = map[string]any{"q": ""}
	a, _ := pipelineApp(t, conditionalResponder("a"))
	if _, err := a.callDatabasePipeline(pipelineIdentity("7"), plan); err != nil {
		t.Errorf("beside the scope: %v", err)
	}
}

// When authorization is empty, the linked records can't match either: nothing is
// read, and the search group can't bring anything back.
func TestLiteralSearchKeepsEmptyAuthorization(t *testing.T) {
	plan := literalPlan(t, literalGroup(`{"or":[`+literalQ+`,`+literalExists(`{"and":[`+literalCentre+`,`+literalQ+`]}`)+`]}`))
	plan["graphql_args"] = map[string]any{"q": "vert"}
	a, p := pipelineApp(t, conditionalResponder())
	out, err := a.callDatabasePipeline(pipelineIdentity("7"), plan)
	if err != nil {
		t.Fatal(err)
	}
	for _, batch := range p.batches {
		for _, raw := range batch["operations"].([]any) {
			if args := raw.(map[string]any)["args"].(map[string]any); args["collection"] == "leads" {
				t.Errorf("the page was read: %v", args["where"])
			}
		}
	}
	if fmt.Sprint(out.(map[string]any)["total"]) != "0" {
		t.Errorf("empty authorisation: %v", out)
	}

	// Only the linked check empty: the branch matches nothing, the rest still runs.
	filter := boundWithEmptyScope(t, `{"or":[`+literalQ+`,`+literalExists(`{"and":[`+literalCentre+`,`+literalQ+`]}`)+`]}`)
	if !filterMatchesNothing(filter["or"].([]any)[1]) || filterMatchesNothing(filter) {
		t.Errorf("empty linked check: %v", filter)
	}
}

// boundWithEmptyScope binds a filter with no access values, as a request with empty scope would.
func boundWithEmptyScope(t *testing.T, where string) map[string]any {
	t.Helper()
	plan, err := validateDatabasePipeline(literalPlan(t, literalGroup(where)))
	if err != nil {
		t.Fatal(err)
	}
	run := &databasePipelineRun{plan: plan, params: map[string]any{"q": "vert", "phone": ""}, scope: map[string][]string{"allowed": {}, "offers": {}}}
	bound, err := run.where(decodeConfig(t, where))
	if err != nil {
		t.Fatal(err)
	}
	return bound.(map[string]any)
}

func TestLiteralSearchDiscovery(t *testing.T) {
	where := `{"and":[` + authorisedFilter + `,` + literalWhen(literalQGiven, `{"or":[{"field":"source","op":"contains_ascii","value":"$param.q"},`+
		literalExists(`{"and":[`+literalCentre+`,`+literalRestrict+`,{"or":[`+literalQ+`,`+literalWhen(literalPhoneLong, literalPhone)+`]}]}`)+`,`+
		literalWhen(literalPhoneLong, `{"field":"own_digits","op":"contains_ascii","value":"$param.phone"}`)+`]}`) + `]}`
	plan, err := validateDatabasePipeline(literalPlan(t, where))
	if err != nil {
		t.Fatal(err)
	}

	// Arguments are found at every depth, so publishing checks they are strings.
	if got := plan.searchArguments(); !reflect.DeepEqual(got, []string{"phone", "q"}) {
		t.Errorf("searchArguments: %v", got)
	}
	if _, ok := plan.conditionalArguments()["phone"]; !ok {
		t.Errorf("conditionalArguments: %v", plan.conditionalArguments())
	}

	// Own-collection searches are probed on the query's collection, and only those.
	step := plan.Steps[0]
	own := []string{}
	for _, search := range step.ownSearches() {
		own = append(own, fmt.Sprint(search["field"]))
	}
	if !reflect.DeepEqual(own, []string{"source", "own_digits"}) {
		t.Errorf("ownSearches: %v", own)
	}
	if len(step.searches()) != 4 {
		t.Errorf("searches: %v", step.searches())
	}
	if fields := step.conditionalFields(); len(fields) != 0 {
		t.Errorf("a related field must not be read from the query's own collection: %v", fields)
	}

	// A relationship is planned with its operator, on the collection of the field,
	// and without any value a request would bind.
	probes := step.ownRelations()
	if len(probes) != 1 {
		t.Fatalf("ownRelations: %v", probes)
	}
	encoded, _ := json.Marshal(probeRelation(probes[0]))
	for _, want := range []string{`{"field":"label","op":"contains_ascii","value":""}`, `{"field":"digits","op":"contains_ascii","value":""}`, `"op":"is_null"`} {
		if !strings.Contains(string(encoded), want) {
			t.Errorf("probe lacks %s: %s", want, encoded)
		}
	}
	for _, refused := range []string{"$param", "$scope", `"when"`, "access_when"} {
		if strings.Contains(string(encoded), refused) {
			t.Errorf("probe holds %s: %s", refused, encoded)
		}
	}

	// Realtime watches the related collection under the when.
	if got := fmt.Sprint(plan.Collections()); !strings.Contains(got, "sales") {
		t.Errorf("Collections: %v", got)
	}
}

// An or without conditions keeps its meaning through resolution, and one a plan
// could not have published fails rather than dropping the restriction.
func TestResolveAlternatives(t *testing.T) {
	run := &databasePipelineRun{params: map[string]any{"phone": "1"}, scope: map[string][]string{}}
	plain := decodeConfig(t, `{"or":[{"field":"a","op":"eq","value":"x"},{"exists":{"collection":"c","on":[{"localField":"a","foreignField":"b"}]}}]}`)
	resolved, err := run.resolveConditions(plain)
	if err != nil || !reflect.DeepEqual(resolved, plain) {
		t.Errorf("plain or: %v %v", resolved, err)
	}
	_, err = run.resolveConditions(decodeConfig(t, `{"or":[`+literalWhen(literalPhoneLong, literalPhone)+`]}`))
	if err == nil || !strings.Contains(err.Error(), "removed every branch") {
		t.Errorf("no branches left: %v", err)
	}
}
