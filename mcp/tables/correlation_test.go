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

func TestFunctionCorrelationReachesReadDiagnostics(t *testing.T) {
	ctx, _, logger := diagnosticTestCtx(t)
	args := map[string]any{"sql": "SELECT 1 AS n", "_request_id": "gateway-function-123", "request_id": "caller-supplied"}
	if _, err := invokeObservedRead(&App{}, ctx, context.Background(), args); err != nil {
		t.Fatal(err)
	}
	records := logger.snapshot()
	if len(records) != 1 || records[0]["request_id"] != "gateway-function-123" {
		t.Fatalf("missing originating Function request ID: %+v", records)
	}
	if args["_request_id"] != "gateway-function-123" {
		t.Fatal("caller arguments were mutated")
	}
}
