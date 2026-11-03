package main

import (
	"fmt"
	"reflect"
	"strings"
	"testing"

	gql "github.com/graphql-go/graphql"
)

// Options that would be silently ignored are configuration errors: a "latest
// grant" read with an ignored orderBy/limit would quietly widen access.
func TestDatabasePipelineRejectsUnknownOrMistypedConfig(t *testing.T) {
	read := `{"collection":"grants","field":"id","where":{"field":"user","op":"eq","value":"$param.u"}%s}`
	access := func(value string) string {
		return `{"params":{"u":{"from":"$identity.subject","required":true}},"access":{"values":{"g":` + value + `}},` +
			`"steps":[{"id":"s","op":"count","args":{"collection":"c","where":{"field":"g","op":"in","value":"$scope.g"}}}],"result":{"n":"$steps.s"}}`
	}
	step := func(spec string) string {
		return `{"steps":[` + spec + `],"result":{"r":"$steps.s"}}`
	}
	cases := map[string]string{
		"access read sort":       access(`{"otherwise":` + fmt.Sprintf(read, `,"sort":[{"field":"created_at"}]`) + `}`),
		"access orderBy dir":     access(`{"otherwise":` + fmt.Sprintf(read, `,"orderBy":[{"field":"created_at","direction":"newest"}]`) + `}`),
		"access limit unordered": access(`{"otherwise":` + fmt.Sprintf(read, `,"limit":1`) + `}`),
		"access read fractional": access(`{"otherwise":` + fmt.Sprintf(read, `,"max":2.5`) + `}`),
		"access value typo":      access(`{"otherwise":` + fmt.Sprintf(read, ``) + `,"singel":true}`),
		"single not Boolean":     access(`{"otherwise":` + fmt.Sprintf(read, ``) + `,"single":"yes"}`),
		"permission not string":  access(`{"otherwise":` + fmt.Sprintf(read, ``) + `,"unrestricted_if_permission":true}`),
		"access unknown key":     strings.Replace(access(`{"otherwise":`+fmt.Sprintf(read, ``)+`}`), `"access":{`, `"access":{"mode":"strict",`, 1),
		"param unknown key":      strings.Replace(access(`{"otherwise":`+fmt.Sprintf(read, ``)+`}`), `"required":true`, `"required":true,"default":"x"`, 1),
		"param from not string":  step(`{"id":"s","op":"count","args":{"collection":"c"}}`)[:1] + `"params":{"p":{"from":5}},` + step(`{"id":"s","op":"count","args":{"collection":"c"}}`)[1:],
		"step unknown key":       step(`{"id":"s","op":"find","pages":{"size":10},"args":{"collection":"c"}}`),
		"max_rows string":        step(`{"id":"s","op":"find","max_rows":"10","args":{"collection":"c"}}`),
		"metric unknown key":     step(`{"id":"s","op":"aggregate","args":{"collection":"c","metrics":[{"name":"n","op":"count","distinct":true}]}}`),
		"metric unknown op":      step(`{"id":"s","op":"aggregate","args":{"collection":"c","metrics":[{"name":"n","op":"median","field":"x"}]}}`),
		"lookup unknown key":     step(`{"id":"s","op":"find","args":{"collection":"c","lookups":[{"as":"a","collection":"agents","localField":"agent_id","foreignField":"id"}]}}`),
		"lookup missing as":      step(`{"id":"s","op":"find","args":{"collection":"c","lookups":[{"collection":"agents","localField":"agent_id"}]}}`),
		"bucket unit":            step(`{"id":"s","op":"aggregate","args":{"collection":"c","buckets":[{"name":"d","field":"at","unit":"quarter"}],"groupBy":["d"],"metrics":[{"name":"n","op":"count"}]}}`),
		"combine unknown key":    `{"steps":[{"id":"s","op":"aggregate","args":{"collection":"c","groupBy":["k"],"metrics":[{"name":"n","op":"count"}]}}],"combine":{"j":{"join":["s"],"on":["k"],"sort":"n"}},"result":{"r":"$combine.j"}}`,
		"total unknown key":      `{"steps":[{"id":"s","op":"aggregate","args":{"collection":"c","groupBy":["k"],"metrics":[{"name":"n","op":"count"}]}},{"id":"t","op":"aggregate","args":{"collection":"c","metrics":[{"name":"n","op":"count"}]}}],"combine":{"j":{"join":["s"],"on":["k"],"total":{"key":"all","steps":{"s":"t"},"label":"x"}}},"result":{"r":"$combine.j"}}`,
		"timeout fractional":     `{"timeout_ms":1.5,"steps":[{"id":"s","op":"count","args":{"collection":"c"}}],"result":{"r":"$steps.s"}}`,
		"root result unknown":    `{"steps":[{"id":"s","op":"count","args":{"collection":"c"}}],"result":"$steps.x"}`,
	}
	for name, text := range cases {
		if _, err := validateDatabasePipeline(decodeConfig(t, text)); err == nil {
			t.Errorf("%s was accepted", name)
		} else if pipelineErrorCode(err) != "invalid_request" {
			t.Errorf("%s: %v", name, err)
		}
	}
	if _, err := validateDatabasePipeline(decodeConfig(t, access(`{"otherwise":`+fmt.Sprintf(read, `,"max":20`)+`,"single":true}`))); err != nil {
		t.Fatalf("valid access rejected: %v", err)
	}
}

// A list field such as commercials: [Commercial!]! takes the rows directly;
// the object form keeps working.
func TestDatabasePipelineRootResult(t *testing.T) {
	for ref, want := range map[string]string{
		"$combine.byAgent":   "[]interface {}",
		"$steps.sales":       "[]interface {}",
		"$scope.teams":       "[]string",
		"$steps.calls.first": "map[string]interface {}",
	} {
		a, _ := pipelineApp(t, reportResponder(t))
		config := reportConfig(t, map[string]any{"from": "2026-10-01T00:00:00Z", "to": "2026-11-01T00:00:00Z"})
		config["result"] = ref
		value, err := a.callDatabasePipeline(pipelineIdentity("u1", "reports:read"), config)
		if err != nil {
			t.Fatal(err)
		}
		if got := reflect.TypeOf(value).String(); got != want {
			t.Fatalf("%s returned %s", ref, got)
		}
	}
	a, _ := pipelineApp(t, reportResponder(t))
	config := reportConfig(t, map[string]any{"from": "2026-10-01T00:00:00Z", "to": "2026-11-01T00:00:00Z"})
	config["result"] = "$combine.byAgent"
	value, _ := a.callDatabasePipeline(pipelineIdentity("u1", "reports:read"), config)
	if rows := value.([]any); len(rows) != 4 || rows[0].(map[string]any)["agent_id"] != "a1" {
		t.Fatalf("rows: %v", rows)
	}
}

func TestDatabaseResolverForwardsLookupsAndBuckets(t *testing.T) {
	lookups := []any{map[string]any{"as": "agent", "collection": "agents", "localField": "agent_id", "select": []any{"name"}}}
	buckets := []any{map[string]any{"name": "day", "field": "started_at", "unit": "day", "timezone": "Europe/Paris"}}
	sdl := `input Filter { field: String op: String value: String }
type Call { id: ID agent: Agent }
type Agent { name: String }
type Row { day: String n: Int }
type Query {
 calls(where: Filter, lookups: [Filter!]): [Call!]!
 daily(buckets: [Filter!]): [Row!]!
}`
	bindings := &executionBindings{sources: map[int64]sourceRecord{
		1: {Kind: "database", Config: map[string]any{"database": "crm", "collection": "calls"}},
	}, resolvers: map[string]resolverRecord{
		"Query.calls": {SourceID: 1, Operation: "find", Config: map[string]any{"lookups": lookups}},
		"Query.daily": {SourceID: 1, Operation: "aggregate", Config: map[string]any{"buckets": buckets, "groupBy": []any{"day"}, "metrics": []any{map[string]any{"name": "n", "op": "count"}}}},
	}}
	run := func(query string) map[string]any {
		t.Helper()
		p := &failingDatabase{}
		_, schema, ctx := standardApp(t, sdl, p, bindings)
		runStandard(schema, gql.Params{Context: ctx, RequestString: query})
		return p.input
	}
	// Configured lookups are forwarded; a client-supplied one is ignored.
	input := run(`{ calls(lookups:[{field:"secret"}]) { id } }`)
	if !reflect.DeepEqual(input["lookups"], lookups) {
		t.Fatalf("lookups: %v", input["lookups"])
	}
	if input := run(`{ daily(buckets:[{field:"x"}]) { day } }`); !reflect.DeepEqual(input["buckets"], buckets) {
		t.Fatalf("buckets: %v", input["buckets"])
	}

	for name, config := range map[string]struct {
		op     string
		config map[string]any
	}{
		"lookup typo":      {"find", map[string]any{"lookups": []any{map[string]any{"as": "a", "collection": "agents", "localfield": "agent_id"}}}},
		"too many lookups": {"find", map[string]any{"lookups": []any{lookups[0], lookups[0], lookups[0], lookups[0], lookups[0]}}},
		"lookup on get":    {"get", map[string]any{"lookups": lookups}},
		"buckets on find":  {"find", map[string]any{"buckets": buckets}},
		"bucket unit":      {"aggregate", map[string]any{"buckets": []any{map[string]any{"name": "d", "field": "at", "unit": "fortnight"}}}},
	} {
		if err := validateDatabaseResolverOptions(config.op, config.config); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
	source := sourceRecord{Kind: "database", Config: map[string]any{"database": "crm", "collection": "calls"}}
	spec, ok := databaseWatchFor("p1", source, resolverRecord{Operation: "find", Config: map[string]any{"lookups": lookups}})
	if !ok || strings.Join(spec.collections, ",") != "agents,calls" {
		t.Fatalf("watch: %v", spec.collections)
	}
}
