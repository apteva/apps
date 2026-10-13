package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestCurrentPageSnapshotVerifiesWithoutReloading(t *testing.T) {
	for _, mode := range []string{"valid", "prepared_query", "wrong_query", "no_session"} {
		t.Run(mode, func(t *testing.T) {
			p := newFakePlatform()
			opens, navigates := 0, 0
			p.readViewsResponse = func(tool string, args map[string]any) map[string]any {
				if tool == "computer.browser_open" {
					opens++
				}
				if tool == "computer.computer_use" && args["action"] == "set_text" {
					return map[string]any{"current_url": "https://example.com/library"}
				}
				if tool == "computer.computer_use" && args["action"] == "navigate" {
					navigates++
				}
				if tool == "computer.browser_extract" {
					value := "Ashley"
					if mode == "wrong_query" {
						value = "Other"
					}
					h := strings.Replace(readFixtureHTML(1, 2), "</body>", `<input id="query" value="`+value+`"></body>`, 1)
					return map[string]any{"html": h, "current_url": "https://example.com/library", "rendered": true}
				}
				return nil
			}
			ctx, app := newTestCtx(t, p)
			c := fixtureReadViews()
			c.Views = c.Views[:1]
			c.UseCurrentPage = true
			c.AllowPartial = true
			c.Views[0].UseEntryPage = true
			c.Views[0].URLPattern = `^https://example.com/library$`
			c.Views[0].Pagination = actorReadPagination{Mode: "snapshot", MaxPages: 1, StableRounds: 2, SettleMS: 500}
			c.Views[0].Verification = &actorReadVerification{Fields: map[string]actorField{"query": {Selector: "#query", Attribute: "value", Required: true}}, Assertions: map[string]actorAssertion{"query": {EqualsExact: "Ashley"}}}
			b, _ := json.Marshal(c)
			var conf map[string]any
			json.Unmarshal(b, &conf)
			steps := []any{map[string]any{"action": "goto", "url": "https://example.com/library"}, map[string]any{"action": "inspect_views", "read_views": conf}}
			if mode == "no_session" {
				steps = steps[1:]
			}
			if mode == "prepared_query" {
				steps = []any{steps[0], map[string]any{"action": "set_text", "locator": map[string]any{"selector": "#query"}, "text": "Ashley"}, map[string]any{"action": "inspect_views", "read_only": true, "read_views": conf}}
			}
			rec := saveFixtureActor(t, ctx, app, map[string]any{"schema_version": 1, "read_only": mode != "prepared_query", "allowed_hosts": []any{"example.com"}, "limits": map[string]any{"step_retries": 0}, "steps": steps})
			if _, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID}); err != nil {
				t.Fatal(err)
			}
			q, err := claimActorRun(ctx)
			if err != nil {
				t.Fatal(err)
			}
			app.executeActorRun(context.Background(), ctx, q)
			r, _ := getActorRun(ctx, q.ID)
			o := mapFromAny(r["output"])
			if mode == "valid" || mode == "prepared_query" {
				if r["status"] != "completed" || intFromAny(o["item_count"]) != 2 || mapFromAny(o["coverage"])["inspection_complete"] != false || navigates != 0 || opens != 1 {
					t.Fatalf("reload or false exhaustive result: %#v, opens %d navigates %d", r, opens, navigates)
				}
			} else if r["status"] != "failed" || intFromAny(o["item_count"]) != 0 {
				t.Fatalf("unverified snapshot: %#v", r)
			}
		})
	}
	c := fixtureReadViews()
	c.UseCurrentPage = true
	if validateReadViews(&c) == nil {
		t.Fatal("multiple current-page views accepted")
	}
	c.UseCurrentPage = false
	c.Views = c.Views[:1]
	c.Views[0].Pagination.Mode = "snapshot"
	if validateReadViews(&c) == nil {
		t.Fatal("exhaustive snapshot accepted")
	}
}
