package main

import (
	"context"
	"fmt"
	"testing"
)

func TestActorIntegerCounts(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want int64
	}{
		{"0", 0}, {"116", 116}, {"498", 498}, {"1,041", 1041},
		{"1,690", 1690}, {"2,196", 2196}, {"1,234,567", 1234567},
		{"1.234.567", 1234567}, {"1 041", 1041}, {"1\u00a0041", 1041},
		{"1\u202f041", 1041}, {"Send a message to 1,041 members", 1041},
		{"-1,041", -1041}, {"+1,041", 1041},
		{"9007199254740993", 9007199254740993},
		{"9,223,372,036,854,775,807", 9223372036854775807},
		{"-9,223,372,036,854,775,808", -9223372036854775808},
	} {
		t.Run(tc.raw, func(t *testing.T) {
			got, err := coerceActorValue(tc.raw, "integer", "")
			if err != nil || got != tc.want {
				t.Fatalf("integer %q = %#v, %v; want %d", tc.raw, got, err, tc.want)
			}
		})
	}
}

func TestActorIntegerRejectsFractionalMalformedOrOverflowCounts(t *testing.T) {
	for _, raw := range []string{"", "no members", "1.5", "1,5", "1,04", "1,234.56", "1.234,56", "12,34,567", "1,,041", "1,041,", "1 04", ".5", "1e3", "9223372036854775808", "-9223372036854775809"} {
		t.Run(raw, func(t *testing.T) {
			if got, err := coerceActorValue(raw, "integer", ""); err == nil {
				t.Fatalf("unsafe integer %q accepted as %v", raw, got)
			}
		})
	}
}

func TestActorGroupedCountHonorsAudienceCap(t *testing.T) {
	for _, cap := range []int{1, 1040, 1041} {
		t.Run(fmt.Sprint(cap), func(t *testing.T) {
			plat := newFakePlatform()
			plat.readViewsResponse = func(tool string, in map[string]any) map[string]any {
				if tool == "computer.browser_extract" {
					return map[string]any{"html": `<body><h2>Send a message to 1,041 members</h2></body>`, "current_url": "https://example.com/members", "rendered": true}
				}
				return nil
			}
			ctx, app := newTestCtx(t, plat)
			rec := saveFixtureActor(t, ctx, app, map[string]any{
				"schema_version": 1, "allowed_hosts": []any{"example.com"}, "limits": map[string]any{"step_retries": 0},
				"steps": []any{
					map[string]any{"action": "goto", "url": "https://example.com/members"},
					map[string]any{"action": "observe_page", "items": "body", "fields": map[string]any{"recipients": map[string]any{"selector": "h2", "pattern": `to ([0-9,]+) members`, "type": "integer", "required": true}}},
					map[string]any{"action": "assert_values", "assertions": map[string]any{"recipients": map[string]any{"equals": 1041, "less_than_or_equal": cap}}},
				},
			})
			if _, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID}); err != nil {
				t.Fatal(err)
			}
			run, err := claimActorRun(ctx)
			if err != nil {
				t.Fatal(err)
			}
			app.executeActorRun(context.Background(), ctx, run)
			result, err := getActorRun(ctx, run.ID)
			if err != nil {
				t.Fatal(err)
			}
			want := "failed"
			if cap == 1041 {
				want = "completed"
			}
			if result["status"] != want {
				t.Fatalf("cap %d: status=%v error=%v", cap, result["status"], result["error"])
			}
			if mapFromAny(mapFromAny(result["output"])["execution_progress"])["commit_attempted"] != false {
				t.Fatal("count check attempted a commit")
			}
		})
	}
}
