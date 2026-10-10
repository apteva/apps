package main

import (
	"context"
	"encoding/json"
	"fmt"
	"math"
	"testing"
	"time"
)

func TestPipelineStageLimitTypedIntegers(t *testing.T) {
	for name, value := range map[string]any{
		"int":                   int(2097152),
		"int64":                 int64(2097152),
		"decoded JSON":          float64(2097152),
		"preserved JSON number": json.Number("2097152"),
	} {
		t.Run(name, func(t *testing.T) {
			got, err := pipelineStageLimit(value, 100)
			if err != nil || got != 2097152 {
				t.Fatal(got, err)
			}
		})
	}
	for name, value := range map[string]any{
		"fraction":          2097152.5,
		"negative fraction": -0.5,
		"NaN":               math.NaN(),
		"positive infinity": math.Inf(1),
		"negative infinity": math.Inf(-1),
		"float overflow":    float64(math.MaxInt32) + 1,
		"float underflow":   float64(math.MinInt32) - 1,
		"integer overflow":  int64(math.MaxInt32) + 1,
		"integer underflow": int64(math.MinInt32) - 1,
		"JSON overflow":     json.Number("9223372036854775808"),
		"JSON fraction":     json.Number("2097152.5"),
		"string":            "2097152",
		"Boolean":           true,
	} {
		t.Run(name, func(t *testing.T) {
			if got, err := pipelineStageLimit(value, 100); err == nil {
				t.Fatal("invalid limit accepted", got)
			}
		})
	}
	if got, err := pipelineStageLimit(nil, 100); err != nil || got != 100 {
		t.Fatal(got, err)
	}
	for _, value := range []any{int64(math.MinInt32), float64(math.MinInt32), int64(math.MaxInt32), float64(math.MaxInt32)} {
		if _, err := pipelineStageLimit(value, 100); err != nil {
			t.Fatal(value, err)
		}
	}
}

func TestPipelineStageLimitsLargeJSONBudgetPublishAndExecute(t *testing.T) {
	for _, version := range []int{2, 3} {
		t.Run(fmt.Sprint(version), func(t *testing.T) {
			config := stagedConfig()
			if version == 3 {
				config = snapshotConfig()
			}
			for _, raw := range config["stages"].([]any) {
				raw.(map[string]any)["max_bytes"] = 2097152
			}
			// Configurations round-trip through stored JSON before publication and
			// execution; ordinary JSON decoding produces the problematic float64.
			encoded, err := json.Marshal(config)
			if err != nil {
				t.Fatal(err)
			}
			var decoded map[string]any
			if err := json.Unmarshal(encoded, &decoded); err != nil {
				t.Fatal(err)
			}
			plan := stagedPlan(t, decoded)
			for _, stage := range plan.Stages {
				if stage.MaxBytes != 2097152 {
					t.Fatal(stage)
				}
			}
			if err := plan.validateStageLimits(defaultReleaseLimits()); err != nil {
				t.Fatal(err)
			}
			platform := &stagedPlatform{db: stagedDB(t)}
			app := stagedReleaseFixture(t, platform, decoded, nil)
			ctx := context.WithValue(context.Background(), identityKey{}, &requestIdentity{Tenant: "north", Expires: time.Now().Add(time.Hour)})
			out, err := app.execute(ctx, "p1", "default", "production", graphqlRequest{Query: `{summary{total}}`})
			if err != nil || len(out.Errors) != 0 || fmt.Sprint(out.Data["summary"].(map[string]any)["total"]) != "5" {
				t.Fatal(out, err)
			}
			// Still reject budgets outside the stage contract after typed parsing.
			for _, bad := range []any{0, 4194305, 2097152.5} {
				decoded["stages"].([]any)[0].(map[string]any)["max_bytes"] = bad
				if _, err := validateAggregatePipeline(aggregatePipelineOperation, decoded, pipelineSources(), 1); err == nil {
					t.Fatal("invalid budget accepted", bad)
				}
			}
		})
	}
}
