package main

import (
	"strings"
	"testing"
)

const narrowedParams = `"params": {
    "user": {"from": "$identity.subject", "type": "string", "required": true},
    "chosen": {"from": "$args.chosen", "type": "exact_string_list"},
    "chosenList": {"from": "$args.chosenList", "type": "string_list"},
    "from": {"from": "$args.from", "type": "string"},
    "q": {"from": "$args.q", "type": "string"}
  }`

// centres can't be narrowed. chosen can, by the client's own list, and derived
// inherits that.
const narrowedAccess = `"access": {"values": {
    "centres": {"otherwise": {"collection": "profiles", "field": "centre_id", "where": {"field": "user_id", "op": "eq", "value": "$param.user"}}},
    "chosen": {"type": "exact_string_list", "optional": true, "requested": "$param.chosen",
      "otherwise": {"collection": "grants", "field": "offer_ids", "where": {"field": "user_id", "op": "eq", "value": "$param.user"}}},
    "fixed": {"type": "exact_string_list", "optional": true,
      "otherwise": {"collection": "grants", "field": "offer_ids", "where": {"field": "user_id", "op": "eq", "value": "$param.user"}}},
    "derived": {"type": "exact_string_list", "expr": {"op": "if", "condition": {"op": "gt", "args": [{"op": "length", "args": ["$scope.centres"]}, {"const": 0}]}, "then": {"input": "$scope.chosen"}, "else": {"const": []}}}
  }}`

func narrowedPlan(step string, result ...string) string {
	out := "$steps.s"
	if len(result) > 0 {
		out = result[0]
	}
	return `{"database": "crm", ` + narrowedParams + `, ` + narrowedAccess + `, "steps": [` + step + `], "result": ` + quoteUnlessObject(out) + `}`
}

func quoteUnlessObject(text string) string {
	if strings.HasPrefix(text, "{") {
		return text
	}
	return `"` + text + `"`
}

func joinedWhere(where string) string {
	return `{"id": "s", "op": "joined_page", "max_rows": 5, "args": {"collection": "calls", "where": ` + where + `, "select": ["root.id"]}}`
}

func findWhere(where string) string {
	return `{"id": "s", "op": "find", "max_rows": 5, "args": {"collection": "calls", "where": ` + where + `, "select": ["id"]}}`
}

const centreTerm = `{"field": "centre_id", "op": "in", "value": "$scope.centres"}`

func all(terms ...string) string {
	return `{"and": [` + strings.Join(terms, ", ") + `]}`
}

func refusedPlan(t *testing.T, name, plan, want string) {
	t.Helper()
	_, err := validateDatabasePipeline(decodeConfig(t, plan))
	if err == nil {
		t.Errorf("%s: accepted", name)
		return
	}
	if !strings.Contains(err.Error(), want) {
		t.Errorf("%s: wrong refusal: %s", name, err)
	}
}

func acceptedPlan(t *testing.T, name, plan string) {
	t.Helper()
	if _, err := validateDatabasePipeline(decodeConfig(t, plan)); err != nil {
		t.Errorf("%s: %s", name, err)
	}
}

// A client narrowing a list may only ever see fewer rows. A smaller list can return
// more where it switches off a condition, shrinks what a negative rules out or lifts
// a mask, so those places are refused for every plan, joined or not.
func TestClientNarrowedAccessOnlyReachesPositiveFilters(t *testing.T) {
	when := func(scope string) string {
		return `{"access_when": {"op": "gt", "args": [{"op": "length", "args": ["` + scope + `"]}, {"const": 0}]}, "filter": {"field": "offer_id", "op": "in", "value": "$scope.centres"}}`
	}
	notExists := func(scope string) string {
		return `{"not_exists": {"collection": "exclusions", "on": [{"localField": "id", "foreignField": "call_id"}], "where": {"field": "offer_id", "op": "in", "value": "` + scope + `"}}}`
	}
	notIn := func(op, scope string) string {
		return `{"field": "offer_id", "op": "` + op + `", "value": "` + scope + `"}`
	}
	for _, build := range []struct {
		name string
		step func(string) string
	}{{"find", findWhere}, {"joined", joinedWhere}} {
		for name, c := range map[string]struct{ where, want string }{
			"condition":             {all(centreTerm, when("$scope.chosen")), "narrow"},
			"derived condition":     {all(centreTerm, when("$scope.derived")), "narrow"},
			"not_exists":            {all(centreTerm, notExists("$scope.chosen")), "not_exists"},
			"derived not_exists":    {all(centreTerm, notExists("$scope.derived")), "not_exists"},
			"not_in":                {all(centreTerm, notIn("not_in", "$scope.chosen")), "not_in"},
			"ne":                    {all(centreTerm, notIn("ne", "$scope.chosen")), "ne"},
			"filter of a condition": {all(centreTerm, `{"access_when": {"op": "gt", "args": [{"op": "length", "args": ["$scope.fixed"]}, {"const": 0}]}, "filter": {"field": "offer_id", "op": "in", "value": "$scope.chosen"}}`), "narrow"},
		} {
			refusedPlan(t, build.name+" "+name, narrowedPlan(build.step(c.where)), c.want)
		}
		// The ordinary use stays allowed: a shorter list returns fewer rows.
		acceptedPlan(t, build.name+" positive filter", narrowedPlan(build.step(all(centreTerm, `{"field": "offer_id", "op": "in", "value": "$scope.chosen"}`))))
		acceptedPlan(t, build.name+" unnarrowed negative", narrowedPlan(build.step(all(centreTerm, `{"field": "offer_id", "op": "not_in", "value": "$scope.centres"}`))))
	}

	// An access value can't decide on one either, though it can pass one through.
	refusedPlan(t, "expr condition", strings.Replace(narrowedPlan(findWhere(centreTerm)), `"condition": {"op": "gt", "args": [{"op": "length", "args": ["$scope.centres"]}`, `"condition": {"op": "gt", "args": [{"op": "length", "args": ["$scope.chosen"]}`, 1), "decides on $scope.chosen")
	refusedPlan(t, "expr reads it", strings.Replace(narrowedPlan(findWhere(centreTerm)), `"then": {"input": "$scope.chosen"}`, `"then": {"op": "coalesce", "args": ["$scope.chosen", {"const": []}]}`, 1), "decides on $scope.chosen")
	refusedPlan(t, "access read", strings.Replace(narrowedPlan(findWhere(centreTerm)), `"derived":`, `"read": {"optional": true, "otherwise": {"collection": "x", "field": "y", "where": {"field": "z", "op": "not_in", "value": "$scope.chosen"}}}, "derived":`, 1), "not_in")

	// Nor can it reach what a viewer is shown.
	plan := &databasePipelinePlan{accessIndex: map[string]bool{"chosen": true}, narrowedBy: map[string][]string{"chosen": {"chosen"}}}
	if err := plan.validateCombineInput("$scope.chosen", nil); err == nil || !strings.Contains(err.Error(), "narrowed") {
		t.Errorf("combine input: %v", err)
	}

	// A result template can't decide on it.
	refusedPlan(t, "$if", narrowedPlan(findWhere(centreTerm), `{"rows": {"$if": {"op": "gt", "args": [{"op": "length", "args": ["$scope.chosen"]}, {"const": 0}]}, "then": "$steps.s", "else": {"$const": []}}}`), "narrow")
}

// A condition on a normal list would read a blank grant as no grant, because a normal
// list drops blanks, and a "no grant means unrestricted" rule would lift the limit.
func TestJoinedConditionsNeedAnExactList(t *testing.T) {
	access := strings.Replace(narrowedAccess, `"requested": "$param.chosen",`, ``, 1)
	plan := func(where string) string {
		return `{"database": "crm", ` + narrowedParams + `, ` + access + `, "steps": [` + joinedWhere(where) + `], "result": "$steps.s"}`
	}
	when := func(scope string) string {
		return `{"access_when": {"op": "gt", "args": [{"op": "length", "args": ["` + scope + `"]}, {"const": 0}]}, "filter": {"field": "offer_id", "op": "in", "value": "` + scope + `"}}`
	}
	acceptedPlan(t, "exact list", plan(all(centreTerm, when("$scope.chosen"))))
	refusedPlan(t, "normal list", plan(all(centreTerm, when("$scope.centres"))), "exact_string_list")
	refusedPlan(t, "$if on a normal list", `{"database": "crm", `+narrowedParams+`, `+access+`, "steps": [`+joinedWhere(centreTerm)+`],
	  "result": {"rows": {"$if": {"op": "gt", "args": [{"op": "length", "args": ["$scope.centres"]}, {"const": 0}]}, "then": "$steps.s", "else": {"$const": []}}}}`, "exact_string_list")
}

// With only conditional terms, a viewer none of them applies to reads every record.
func TestJoinedRootNeedsATermThatAlwaysApplies(t *testing.T) {
	access := strings.Replace(narrowedAccess, `"requested": "$param.chosen",`, ``, 1)
	plan := func(step string) string {
		return `{"database": "crm", ` + narrowedParams + `, ` + access + `, "steps": [` + step + `], "result": "$steps.s"}`
	}
	when := `{"access_when": {"op": "gt", "args": [{"op": "length", "args": ["$scope.chosen"]}, {"const": 0}]}, "filter": {"field": "offer_id", "op": "in", "value": "$scope.chosen"}}`
	client := `{"when": {"op": "neq", "args": ["$param.q", {"const": ""}]}, "filter": {"field": "phone", "op": "eq", "value": "$param.q"}}`
	const want = "conditional restrictions"
	refusedPlan(t, "alone", plan(joinedWhere(when)), want)
	refusedPlan(t, "in an and", plan(joinedWhere(all(when))), want)
	refusedPlan(t, "beside a client when", plan(joinedWhere(all(when, client))), want)
	refusedPlan(t, "one or arm is conditional", plan(joinedWhere(`{"or": [`+when+`, `+centreTerm+`]}`)), want)
	refusedPlan(t, "a branch", plan(`{"id": "s", "op": "joined_aggregate", "args": {"branches": [
	  {"collection": "a", "where": `+centreTerm+`, "select": ["root.id"]},
	  {"collection": "b", "where": `+when+`, "select": ["root.id"]}],
	  "metrics": [{"name": "n", "op": "count"}]}}`), want)
	refusedPlan(t, "beside only a client value", plan(joinedWhere(all(`{"field": "phone", "op": "eq", "value": "$param.q"}`, when))), want)
	withFilter := func(where, filter string) string {
		return plan(`{"id": "s", "op": "joined_page", "max_rows": 5, "args": {"collection": "calls", "where": ` + where + `, "filter": ` + filter + `, "select": ["root.id"]}}`)
	}
	filterWhen := strings.Replace(when, `"field": "offer_id"`, `"field": "root.offer_id"`, 1)
	refusedPlan(t, "only in the joined-row filter", plan(`{"id": "s", "op": "joined_page", "max_rows": 5, "args": {"collection": "calls", "filter": `+filterWhen+`, "select": ["root.id"]}}`), want)
	acceptedPlan(t, "where stands beside a conditional filter", withFilter(centreTerm, all(filterWhen)))
	acceptedPlan(t, "filter stands beside a conditional where", withFilter(all(when), `{"field": "root.state", "op": "eq", "value": "open"}`))
	refusedPlan(t, "a filter over all branches", plan(`{"id": "s", "op": "joined_aggregate", "args": {"branches": [
	  {"collection": "a", "select": ["root.id"]}], "filter": `+filterWhen+`,
	  "metrics": [{"name": "n", "op": "count"}]}}`), want)
	acceptedPlan(t, "a branch stands beside a conditional filter over all", plan(`{"id": "s", "op": "joined_aggregate", "args": {"branches": [
	  {"collection": "a", "where": `+centreTerm+`, "select": ["root.id"]}], "filter": `+all(filterWhen)+`,
	  "metrics": [{"name": "n", "op": "count"}]}}`))
	acceptedPlan(t, "beside a fixed scope", plan(joinedWhere(all(centreTerm, when))))
	acceptedPlan(t, "beside a literal", plan(joinedWhere(all(`{"field": "state", "op": "eq", "value": "open"}`, when))))
	acceptedPlan(t, "no restriction at all", plan(joinedWhere(`{"field": "state", "op": "eq", "value": "open"}`)))

	checker := &databasePipelinePlan{Params: map[string]databasePipelineParam{"q": {From: "$args.q"}, "user": {From: "$identity.subject"}}}
	for raw, want := range map[string]bool{
		`{"field": "phone", "op": "eq", "value": "$param.q"}`:                                                                  false,
		`{"when": {"op": "neq", "args": [{"const": "a"}, {"const": ""}]}, "filter": {"field": "x", "op": "eq", "value": "y"}}`: false,
		`{"field": "owner", "op": "eq", "value": "$param.user"}`:                                                               true,
		centreTerm:            true,
		when:                  false,
		client:                false,
		`{"and": []}`:         false,
		`{"or": []}`:          false,
		all(when, centreTerm): true,
		`{"or": [` + when + `, ` + centreTerm + `]}`:       false,
		`{"or": [` + centreTerm + `, ` + centreTerm + `]}`: true,
	} {
		if got := checker.filterAlwaysApplies(decodeConfig(t, raw)); got != want {
			t.Errorf("%s: %v", raw, got)
		}
	}
}

// Client values before a pick or rollup don't only narrow: they change which row
// wins, or make a mixed record look single. A report's date window is a real use, so
// a plan can name what it means to allow.
func TestClientValuesBeforeAPickOrRollupAreNamed(t *testing.T) {
	window := `{"field": "day", "op": "gte", "value": "$param.from"}`
	chosen := `{"field": "offer_id", "op": "in", "value": "$scope.chosen"}`
	optional := `{"when": {"op": "neq", "args": ["$param.from", {"const": ""}]}, "filter": ` + window + `}`
	pick := func(where, extra string) string {
		return `{"id": "s", "op": "joined_page", "max_rows": 5` + extra + `, "args": {"collection": "calls", "where": ` + where + `,
		  "pick": {"by": ["root.interaction_id"], "orderBy": [{"field": "root.id", "direction": "asc"}]}, "select": ["root.id"]}}`
	}
	rollup := func(field, where, extra string) string {
		return `{"id": "s", "op": "joined_aggregate"` + extra + `, "args": {"collection": "calls", "` + field + `": ` + where + `,
		  "rollup": {"by": ["root.sale_id"], "metrics": [{"name": "mixed", "op": "max", "field": "root.mixed"}]},
		  "metrics": [{"name": "n", "op": "count"}]}}`
	}
	const want = "before a pick, rollup or distinct"
	refusedPlan(t, "where before a pick", narrowedPlan(pick(all(centreTerm, window), "")), want)
	refusedPlan(t, "optional before a pick", narrowedPlan(pick(all(centreTerm, optional), "")), want)
	refusedPlan(t, "where before a rollup", narrowedPlan(rollup("where", all(centreTerm, window), "")), want)
	refusedPlan(t, "narrowed scope before a rollup", narrowedPlan(rollup("where", all(centreTerm, chosen), "")), want)
	acceptedPlan(t, "named window", narrowedPlan(pick(all(centreTerm, window), `, "stage_params": ["from"]`)))
	acceptedPlan(t, "named optional window", narrowedPlan(rollup("where", all(centreTerm, optional), `, "stage_params": ["from"]`)))
	acceptedPlan(t, "named requested list", narrowedPlan(rollup("where", all(centreTerm, chosen), `, "stage_params": ["chosen"]`)))
	acceptedPlan(t, "no client value", narrowedPlan(pick(centreTerm, "")))

	refusedPlan(t, "a branch's pick", narrowedPlan(`{"id": "s", "op": "joined_aggregate", "args": {"branches": [
	  {"collection": "a", "where": `+all(centreTerm, window)+`, "pick": {"by": ["root.k"], "orderBy": [{"field": "root.id", "direction": "asc"}]}, "select": ["root.id"]}],
	  "metrics": [{"name": "n", "op": "count"}]}}`), want)
	refusedPlan(t, "every branch feeds a top-level rollup", narrowedPlan(`{"id": "s", "op": "joined_aggregate", "args": {"branches": [
	  {"collection": "a", "where": `+all(centreTerm, window)+`, "select": ["root.id"]}],
	  "rollup": {"by": ["root.k"], "metrics": [{"name": "m", "op": "max", "field": "root.v"}]},
	  "metrics": [{"name": "n", "op": "count"}]}}`), want)

	// A distinct by field keeps a winner. A distinct by record only collapses joins.
	distinct := func(names string) string {
		return `{"id": "s", "op": "joined_page", "max_rows": 5, "args": {"collection": "calls", "where": ` + all(centreTerm, window) + `,
		  "joins": [{"as": "s", "collection": "sales", "on": [{"local": "root.sale_id", "foreign": "id"}]}],
		  "distinct": ` + names + `, "select": ["root.id"]}}`
	}
	refusedPlan(t, "distinct by field", narrowedPlan(distinct(`["root.interaction_id"]`)), want)
	acceptedPlan(t, "distinct by record", narrowedPlan(distinct(`["root"]`)))
	acceptedPlan(t, "distinct by every record", narrowedPlan(distinct(`["root", "s"]`)))
	refusedPlan(t, "a top-level distinct reads selected names", narrowedPlan(`{"id": "s", "op": "joined_aggregate", "args": {"branches": [
	  {"collection": "a", "where": `+all(centreTerm, window)+`, "select": ["root.id"]}],
	  "distinct": ["id"], "metrics": [{"name": "n", "op": "count"}]}}`), want)

	refusedPlan(t, "naming an identity param", narrowedPlan(pick(centreTerm, `, "stage_params": ["user"]`)), "not a client param")
	refusedPlan(t, "naming nothing declared", narrowedPlan(pick(centreTerm, `, "stage_params": ["nope"]`)), "not a client param")
	refusedPlan(t, "no stage to feed", narrowedPlan(`{"id": "s", "op": "joined_page", "max_rows": 5, "stage_params": ["from"], "args": {"collection": "calls", "where": `+centreTerm+`, "select": ["root.id"]}}`), "needs a pick, rollup or distinct")
	refusedPlan(t, "not a joined read", narrowedPlan(`{"id": "s", "op": "find", "max_rows": 5, "stage_params": ["from"], "args": {"collection": "calls", "where": `+centreTerm+`, "select": ["id"]}}`), "joined read")
}

// A client value beside a rule the plan fixed could add rows the rule leaves out.
func TestJoinedOrOnlyMixesClientValuesWithEachOther(t *testing.T) {
	literal := `{"field": "state", "op": "eq", "value": "open"}`
	phone := `{"field": "phone", "op": "eq", "value": "$param.q"}`
	name := `{"field": "name", "op": "eq", "value": "$param.q"}`
	refusedPlan(t, "client beside a literal", narrowedPlan(joinedWhere(all(centreTerm, `{"or": [`+literal+`, `+phone+`]}`))), "only some alternatives")
	refusedPlan(t, "client beside authority", narrowedPlan(joinedWhere(all(`{"or": [`+centreTerm+`, `+phone+`]}`))), "client values and authorization")
	chosen := `{"field": "offer_id", "op": "in", "value": "$scope.chosen"}`
	refusedPlan(t, "client beside a narrowable list", narrowedPlan(joinedWhere(all(centreTerm, `{"or": [`+chosen+`, `+phone+`]}`))), "client values and authorization")
	acceptedPlan(t, "a narrowable list beside a fixed one", narrowedPlan(joinedWhere(all(`{"or": [`+chosen+`, `+centreTerm+`]}`))))
	acceptedPlan(t, "all client", narrowedPlan(joinedWhere(all(centreTerm, `{"or": [`+phone+`, `+name+`]}`))))
	acceptedPlan(t, "all literal", narrowedPlan(joinedWhere(all(centreTerm, `{"or": [`+literal+`, `+literal+`]}`))))
	// A client when is optional as a whole, so it stays a narrowing term.
	acceptedPlan(t, "inside a when", narrowedPlan(joinedWhere(all(centreTerm, `{"when": {"op": "neq", "args": ["$param.q", {"const": ""}]}, "filter": {"or": [`+phone+`, `+name+`]}}`))))
}

func TestJoinedComputedConditionCannotHoldAccessWhen(t *testing.T) {
	refusedPlan(t, "access_when in a computed if", narrowedPlan(`{"id": "s", "op": "joined_page", "max_rows": 5, "args": {"collection": "calls", "where": `+centreTerm+`,
	  "computed": [{"name": "c", "expr": {"if": {"access_when": {"op": "gt", "args": [{"op": "length", "args": ["$scope.fixed"]}, {"const": 0}]}, "filter": {"field": "root.state", "op": "eq", "value": "open"}}, "then": {"const": 1}, "else": {"const": 0}}}],
	  "select": ["root.id", "c"]}}`), "cannot hold access_when")
}

func TestJoinedRenameCannotCollide(t *testing.T) {
	step := func(shape string) string {
		return `{"id": "s", "op": "joined_page", "max_rows": 5, "page": {"size": 5}, "args": {"collection": "calls", "where": ` + centreTerm + `, "select": ["root.id", "name", "p.name"]}, "shape": ` + shape + `}`
	}
	refusedPlan(t, "onto a kept name", narrowedPlan(step(`{"rename": {"p.name": "name"}}`)), "two values named name")
	refusedPlan(t, "two onto one", narrowedPlan(step(`{"rename": {"p.name": "x", "root.id": "x"}}`)), "unique")
	refusedPlan(t, "total onto a name", narrowedPlan(step(`{"total_as": "name"}`)), "total_as")
	refusedPlan(t, "total onto a renamed name", narrowedPlan(step(`{"rename": {"p.name": "x"}, "total_as": "x"}`)), "total_as")
	acceptedPlan(t, "swapping names", narrowedPlan(step(`{"rename": {"p.name": "name", "name": "client"}}`)))
	acceptedPlan(t, "no collision", narrowedPlan(step(`{"rename": {"p.name": "prospect", "root.id": "id"}, "total_as": "total_count"}`)))
}

// A value the plan fixes is kept in the probe, so Database checks it against the
// field's type at publish. A bound value can't be: a sample could refuse a good plan.
func TestJoinedProbeKeepsFixedValues(t *testing.T) {
	plan, err := validateDatabasePipeline(decodeConfig(t, narrowedPlan(`{"id": "s", "op": "joined_page", "max_rows": 5, "args": {"collection": "calls",
	  "where": {"and": [{"field": "state", "op": "eq", "value": "open"}, `+centreTerm+`, {"field": "phone", "op": "eq", "value": "$param.q"}]},
	  "joins": [{"as": "o", "collection": "offers", "on": [{"local": "root.offer_id", "foreign": "id"}], "where": {"field": "active", "op": "in", "value": [1, 2]}}],
	  "select": ["root.id"]}}`)))
	if err != nil {
		t.Fatal(err)
	}
	probe, err := plan.Steps[0].joinedProbe()
	if err != nil {
		t.Fatal(err)
	}
	args := probe["args"].(map[string]any)
	if !filterMatchesNothing(args["where"]) {
		t.Errorf("the probe can read rows: %v", args["where"])
	}
	sameJSON(t, "where", args["where"], `{"and":[
	  {"and":[{"field":"state","op":"is_null"},{"field":"state","op":"is_not_null"}]},
	  {"and":[{"field":"state","op":"eq","value":"open"},{"field":"centre_id","op":"is_null"},{"field":"phone","op":"is_null"}]}]}`)
	sameJSON(t, "a join's fixed value", args["joins"].([]any)[0].(map[string]any)["where"], `{"field":"active","op":"in","value":[1,2]}`)

	// Older probes stay as they were.
	sameJSON(t, "older probe", probeFilter(decodeConfig(t, `{"field": "state", "op": "eq", "value": "open"}`)), `{"field":"state","op":"is_null"}`)
}

// A selected field might be JSON, which Database won't filter on.
func TestJoinedRootFieldPrefersFieldsThatCanBeFiltered(t *testing.T) {
	for name, c := range map[string]struct{ args, want string }{
		"a where field":     {`{"where": {"field": "day", "op": "eq", "value": "x"}, "select": ["id"]}`, "day"},
		"order before id":   {`{"orderBy": [{"field": "root.day", "direction": "asc"}], "select": ["root.id"]}`, "day"},
		"group before id":   {`{"groupBy": ["root.day"], "select": ["root.id"]}`, "day"},
		"join key":          {`{"joins": [{"as": "s", "collection": "sales", "on": [{"local": "root.sale_id", "foreign": "id"}]}], "select": ["root.payload"]}`, "sale_id"},
		"id before payload": {`{"select": ["root.payload", "root.id"]}`, "id"},
		"bare id":           {`{"relation": {"as": "r"}, "select": ["payload", "id"]}`, "id"},
		"only what's there": {`{"select": ["root.payload"]}`, "payload"},
	} {
		if got := joinedRootField(decodeConfig(t, c.args)); got != c.want {
			t.Errorf("%s: %q", name, got)
		}
	}
}
