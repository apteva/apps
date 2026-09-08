package main

import (
	"context"
	sdk "github.com/apteva/app-sdk"
	"testing"
)

func TestTableCorrelationDoesNotChangeToolArguments(t *testing.T) {
	ctx := newTestCtx(t)
	args := map[string]any{"_request_id": "gateway-123", "sql": "select 1"}
	result, err := traceTableCall(context.Background(), ctx, "tables_query", args, func(_ *sdk.AppCtx, input map[string]any) (any, error) {
		if _, ok := input["_request_id"]; ok {
			t.Fatal("diagnostic metadata leaked into operation")
		}
		if input["sql"] != "select 1" {
			t.Fatal(input)
		}
		return "ok", nil
	})
	if err != nil || result != "ok" {
		t.Fatalf("%v %v", result, err)
	}
}
