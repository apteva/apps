package main

import (
	"encoding/json"
	"reflect"
	"testing"

	gql "github.com/graphql-go/graphql"
)

func displayNameModule() resolverModule {
	return resolverModule{
		Name: "common.display_name", Version: 1, Status: "published", OutputType: "String", Deterministic: true,
		Inputs:     map[string]any{"first": "String", "last": "String"},
		Definition: map[string]any{"op": "trim", "args": []any{map[string]any{"op": "concat", "args": []any{map[string]any{"input": "first"}, map[string]any{"const": " "}, map[string]any{"input": "last"}}}}},
	}
}

func TestResolverModuleLifecycleAndDecimalEvaluation(t *testing.T) {
	db := testDB(t)
	module, err := createResolverModuleForAPI(db, "p1", "commerce", "common.price", "Reusable price", "Float", map[string]any{"price": "Decimal", "quantity": "Int"}, map[string]any{
		"op": "round", "places": 2, "value": map[string]any{"op": "multiply", "args": []any{map[string]any{"input": "price"}, map[string]any{"input": "quantity"}}},
	}, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	module, err = publishResolverModuleForAPI(db, "p1", "commerce", module.Name, module.Version)
	if err != nil {
		t.Fatal(err)
	}
	value, err := (moduleRuntime{modules: map[string]resolverModule{moduleKey(module.Name, module.Version): *module}}).evaluate(*module, map[string]any{"price": "19.99", "quantity": 3})
	if err != nil || value != 59.97 {
		t.Fatalf("value=%v err=%v", value, err)
	}
	if _, err := createResolverModuleForAPI(db, "p1", "commerce", module.Name, "changed", "Float", module.Inputs, module.Definition, module.Version, true); err == nil {
		t.Fatal("published module was mutable")
	}
	if rows, err := listResolverModulesForAPI(db, "p1", "analytics"); err != nil || len(rows) != 0 {
		t.Fatalf("module leaked across APIs: %#v %v", rows, err)
	}
}

func TestResolverModuleRejectsUnknownInputsAndCycles(t *testing.T) {
	if problems := validateResolverModuleDefinition(map[string]any{"name": "String"}, map[string]any{"input": "missing"}); len(problems) == 0 {
		t.Fatal("unknown input accepted")
	}
	modules := map[string]resolverModule{}
	a := resolverModule{Name: "a", Version: 1, Status: "published", Inputs: map[string]any{}, Definition: map[string]any{"op": "call", "module": "b", "version": 1, "inputs": map[string]any{}}, Dependencies: []string{"b@1"}}
	b := resolverModule{Name: "b", Version: 1, Status: "published", Inputs: map[string]any{}, Definition: map[string]any{"op": "call", "module": "a", "version": 1, "inputs": map[string]any{}}, Dependencies: []string{"a@1"}}
	modules["a@1"] = a
	modules["b@1"] = b
	if problems := validateModuleGraph(a, modules); len(problems) == 0 {
		t.Fatal("cycle accepted")
	}
}

func TestResolverModuleNullGuardSkipsNumericBranch(t *testing.T) {
	module := resolverModule{Name: "common.percentage", Version: 1, Status: "published", Inputs: map[string]any{"part": "Decimal", "total": "Decimal"}, Definition: map[string]any{
		"op": "if", "condition": map[string]any{"op": "gt", "args": []any{map[string]any{"input": "total"}, map[string]any{"const": 0}}},
		"then": map[string]any{"op": "divide", "args": []any{map[string]any{"input": "part"}, map[string]any{"input": "total"}}}, "else": map[string]any{"const": 0},
	}}
	value, err := (moduleRuntime{modules: map[string]resolverModule{}}).evaluate(module, map[string]any{"part": nil, "total": nil})
	if err != nil || value != 0 {
		t.Fatalf("value=%#v err=%v", value, err)
	}
}

func TestComputedFieldUsesHiddenProjectionDependenciesAndFastPath(t *testing.T) {
	p := &standardTables{}
	module := displayNameModule()
	bindings := &executionBindings{sources: map[int64]sourceRecord{
		1: {Kind: "tables", Config: map[string]any{"table": "customers", "select": []any{"id", "name", "secret"}}},
		2: {Kind: "module", Config: map[string]any{"module": module.Name, "version": 1, "inputs": map[string]any{"first": "$parent.name", "last": map[string]any{"fixed": true}}}},
	}, resolvers: map[string]resolverRecord{
		"Query.customers":       {SourceID: 1, Operation: "find"},
		"Customer.display_name": {SourceID: 2, Operation: "resolve", Config: map[string]any{"inputs": map[string]any{"first": "$parent.name", "last": ""}}},
	}, modules: map[string]resolverModule{moduleKey(module.Name, module.Version): module}}
	_, schema, ctx := standardApp(t, `type Query { customers: [Customer!]! } type Customer { id: ID! name: String display_name: String! }`, p, bindings)
	result := runStandard(schema, gql.Params{Context: ctx, RequestString: `{ customers { display_name } }`})
	if len(result.Errors) > 0 {
		t.Fatal(result.Errors)
	}
	encoded, _ := json.Marshal(result.Data)
	if string(encoded) != `{"customers":[{"display_name":"Ada"},{"display_name":"Grace"}]}` {
		t.Fatalf("data=%s", encoded)
	}
	selectValues, _ := p.inputs[0]["select"].([]any)
	if !reflect.DeepEqual(selectValues, []any{"name"}) {
		t.Fatalf("hidden projection=%#v", selectValues)
	}
	state := ctx.Value(standardRequestKey{}).(*standardRequest)
	if !state.fastProjectionUsed {
		t.Fatal("computed field disabled fast projection")
	}
}
