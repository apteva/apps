package main

import (
	"context"
	"reflect"
	"testing"
	"time"
)

func TestGraphQLTypedWhereLowersToNativePredicates(t *testing.T) {
	raw := map[string]any{
		"and": []any{
			map[string]any{"status": map[string]any{"in": []any{"OPEN", "PENDING"}}},
			map[string]any{"amount": map[string]any{"gte": 10, "lt": 50}},
		},
		"or": []any{
			map[string]any{"ownerId": map[string]any{"eq": "7"}},
			map[string]any{"ownerId": map[string]any{"eq": "8"}},
		},
		"deletedAt": map[string]any{"isNull": true},
	}
	got, err := graphqlWhere(raw, map[string]string{"ownerId": "owner_id", "deletedAt": "deleted_at"})
	if err != nil {
		t.Fatal(err)
	}
	want := []any{
		map[string]any{"col": "status", "op": "in", "value": []any{"OPEN", "PENDING"}},
		map[string]any{"col": "amount", "op": "gte", "value": 10},
		map[string]any{"col": "amount", "op": "lt", "value": 50},
		map[string]any{"col": "deleted_at", "op": "is_null"},
		map[string]any{"col": "owner_id", "op": "in", "value": []any{"7", "8"}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("typed where\n got: %#v\nwant: %#v", got, want)
	}
	if _, err := graphqlWhere(map[string]any{"or": []any{map[string]any{"a": 1}, map[string]any{"b": 2}}}, nil); err == nil {
		t.Fatal("accepted a disjunction that Tables cannot execute without broadening")
	}
}

func TestMappedTablesInputCombinesScopeAndGraphQLConventions(t *testing.T) {
	config := map[string]any{
		"table":          "prospects",
		"where":          []any{map[string]any{"col": "tenant", "op": "eq", "value": "fixed"}},
		"identity_where": []any{map[string]any{"col": "owner_id", "op": "eq", "value": "42"}},
		"filter_columns": map[string]any{"createdAt": "created_at"},
	}
	input, err := mappedTablesInput(config, map[string]any{
		"where": map[string]any{"createdAt": map[string]any{"gte": "2026-01-01"}},
		"first": 25, "after": "cursor-1", "includeTotal": true,
	})
	if err != nil {
		t.Fatal(err)
	}
	where := input["where"].([]any)
	if len(where) != 3 || where[0].(map[string]any)["value"] != "fixed" || where[1].(map[string]any)["value"] != "42" || where[2].(map[string]any)["col"] != "created_at" {
		t.Fatalf("mandatory filters were not preserved: %#v", where)
	}
	if input["limit"] != 25 || input["cursor"] != "cursor-1" || input["include_total"] != true {
		t.Fatalf("GraphQL pagination aliases: %#v", input)
	}
}

func TestIdentityRowFiltersAreTrustedAndTyped(t *testing.T) {
	raw := map[string]any{
		"mode": "auth", "tenant_id": "default", "environment": "production",
		"claims": []any{"centre_ids"},
		"row_filters": map[string]any{
			"Query.prospects": []any{
				map[string]any{"column": "commercial_id", "identity": "subject", "value_type": "string"},
				map[string]any{"column": "centre_id", "op": "in", "identity": "claim.centre_ids", "value_type": "string"},
			},
		},
	}
	policy, err := parseSecurity(raw)
	if err != nil {
		t.Fatal(err)
	}
	identity := &requestIdentity{Subject: "42", Tenant: "default", Claims: map[string]any{"centre_ids": []any{"a", "b"}}, Expires: time.Now().Add(time.Minute)}
	ctx := context.WithValue(context.Background(), identityKey{}, identity)
	where, err := identityWhere(ctx, policy, "Query.prospects")
	if err != nil {
		t.Fatal(err)
	}
	if len(where) != 2 || where[0].(map[string]any)["value"] != "42" || where[1].(map[string]any)["op"] != "in" {
		t.Fatalf("identity predicates: %#v", where)
	}
	raw["claims"] = []any{}
	if _, err := parseSecurity(raw); err == nil {
		t.Fatal("accepted a row policy using an untrusted/unrequested claim")
	}
}
