package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	sdk "github.com/apteva/app-sdk"
	gql "github.com/graphql-go/graphql"
)

// failingDatabase answers every Database call with one app-call error, in the
// exact text form the SDK produces.
type failingDatabase struct {
	sdk.PlatformClient
	sdk.AppContextClient
	err   error
	input map[string]any
}

func (p *failingDatabase) CallAppResult(app, tool string, input map[string]any, out any) error {
	return p.CallAppResultContext(context.Background(), app, tool, input, out)
}

func (p *failingDatabase) CallAppResultContext(_ context.Context, app, tool string, input map[string]any, _ any) error {
	p.input = input
	return p.err
}

func TestDatabaseErrorCodesReachGraphQLClients(t *testing.T) {
	for _, c := range []struct {
		err           error
		code, message string
	}{
		{errors.New("database.db_update: version_conflict: record version changed (code=-32000)"), "version_conflict", "record version changed"},
		{errors.New("database.db_update: version_conflict: record is absent (code=-32000)"), "version_conflict", "record is absent"},
		{errors.New("database.db_update: invalid_argument: use an exact key or a filter (code=-32000)"), "invalid_argument", "use an exact key or a filter"},
		{errors.New("database.db_insert: unique_conflict: duplicate key (code=-32000)"), "unique_conflict", "duplicate key"},
		// Unknown classes and transport failures stay generic.
		{errors.New("database.db_update: storage_error: disk (code=-32000)"), "execution_error", "database.db_update: storage_error: disk (code=-32000)"},
		{errors.New("dial tcp: connection refused"), "execution_error", "dial tcp: connection refused"},
	} {
		p := &failingDatabase{err: c.err}
		_, schema, ctx := standardApp(t, databaseSDL, p, databaseBindings())
		result := runStandard(schema, gql.Params{Context: ctx, RequestString: `mutation { patchOrder(key:{id:"1"}, set:{amount:7}, ifVersion:3) { affected } }`})
		if len(result.Errors) != 1 {
			t.Fatalf("%v: errors %v", c.err, result.Errors)
		}
		// Served responses take extensions.code from errorCode, keyed by field
		// path (asserted over HTTP in TestRealDatabaseGuardedEdits).
		if code := errorCode(databaseCallError(c.err)); code != c.code || result.Errors[0].Message != c.message {
			t.Fatalf("%v: code=%v message=%q", c.err, code, result.Errors[0].Message)
		}
	}
}

func TestDatabasePipelineKeepsDatabaseCodes(t *testing.T) {
	err := databasePipelineError(context.Background(), errors.New("database.db_batch: permission_denied: app graphql has no read access (code=-32000)"))
	if errorCode(err) != "permission_denied" || err.Error() != "app graphql has no read access" {
		t.Fatalf("%s: %v", errorCode(err), err)
	}
}

// Mutation inputs: the resolver's guard always reaches Database, a client
// filter can only narrow it, and write bounds cannot be raised.
func TestDatabaseMutationGuardCannotBeWeakened(t *testing.T) {
	guard := map[string]any{"field": "centre_id", "op": "eq", "value": "centre_a"}
	sdl := `input Filter { field: String op: String value: String or: [Filter!] }
input Key { id: ID! }
input Patch { notes: String }
type WriteResult { affected: Int! }
type Query { ping: Int }
type Mutation {
 editRecord(key: Key!, set: Patch!, ifVersion: Int, where: Filter, maxAffected: Int, all: Boolean, scope: String, database: String, collection: String): WriteResult!
 bulkEdit(set: Patch!, where: Filter, maxAffected: Int): WriteResult!
 removeRecord(key: Key!, ifVersion: Int, maxAffected: Int): WriteResult!
}`
	bindings := &executionBindings{sources: map[int64]sourceRecord{
		1: {Kind: "database", Config: map[string]any{"database": "crm", "collection": "records"}},
	}, resolvers: map[string]resolverRecord{
		"Mutation.editRecord":   {SourceID: 1, Operation: "update", Config: map[string]any{"where": guard}},
		"Mutation.bulkEdit":     {SourceID: 1, Operation: "update", Config: map[string]any{"where": guard, "maxAffected": 20}},
		"Mutation.removeRecord": {SourceID: 1, Operation: "delete", Config: map[string]any{"where": guard}},
	}}
	run := func(query string) map[string]any {
		t.Helper()
		p := &failingDatabase{err: errors.New("database.db_update: version_conflict: record version changed (code=-32000)")}
		_, schema, ctx := standardApp(t, sdl, p, bindings)
		runStandard(schema, gql.Params{Context: ctx, RequestString: query})
		raw, _ := json.Marshal(p.input)
		var input map[string]any
		_ = json.Unmarshal(raw, &input)
		return input
	}

	// A client may try to widen the guard, address another collection or
	// database, switch scope, lift the bound or ask for a whole collection.
	input := run(`mutation { editRecord(key:{id:"r1"}, set:{notes:"x"}, ifVersion:4,
		where:{or:[{field:"centre_id", op:"eq", value:"centre_b"}, {field:"centre_id", op:"ne", value:"centre_a"}]},
		maxAffected:1000, all:true, scope:"app", database:"other", collection:"secrets") { affected } }`)
	want := fmt.Sprint(map[string]any{"and": []any{guard, map[string]any{"or": []any{
		map[string]any{"field": "centre_id", "op": "eq", "value": "centre_b"},
		map[string]any{"field": "centre_id", "op": "ne", "value": "centre_a"},
	}}}})
	if fmt.Sprint(input["where"]) != want {
		t.Fatalf("guard was not ANDed with the client filter: %v", input["where"])
	}
	if input["maxAffected"] != float64(1) || input["all"] != nil || input["scope"] != "project" || input["database"] != "crm" || input["collection"] != "records" {
		t.Fatalf("client changed server policy: %v", input)
	}
	if fmt.Sprint(input["key"]) != "map[id:r1]" || input["ifVersion"] != float64(4) {
		t.Fatalf("key and revision must be forwarded: %v", input)
	}

	// Without a client filter the guard is sent alone.
	if input := run(`mutation { editRecord(key:{id:"r1"}, set:{notes:"x"}) { affected } }`); fmt.Sprint(input["where"]) != fmt.Sprint(guard) {
		t.Fatalf("guard missing: %v", input["where"])
	}
	// A configured bound can be lowered but not raised.
	if input := run(`mutation { bulkEdit(set:{notes:"x"}, maxAffected:500) { affected } }`); input["maxAffected"] != float64(20) {
		t.Fatalf("raised bound: %v", input["maxAffected"])
	}
	if input := run(`mutation { bulkEdit(set:{notes:"x"}, maxAffected:3) { affected } }`); input["maxAffected"] != float64(3) {
		t.Fatalf("lowered bound ignored: %v", input["maxAffected"])
	}
	// Deletes follow the same rules.
	if input := run(`mutation { removeRecord(key:{id:"r1"}, ifVersion:2, maxAffected:50) { affected } }`); fmt.Sprint(input["where"]) != fmt.Sprint(guard) || input["maxAffected"] != float64(1) {
		t.Fatalf("delete policy: %v", input)
	}
}
