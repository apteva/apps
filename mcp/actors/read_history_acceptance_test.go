package main

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

// The native older-page control can remain visible after an empty AJAX batch.
// Only a verified empty response marker terminates the read; a no-op cannot.
func TestReadHistoryEmptyBatchAndRealOlderPage(t *testing.T) {
	for _, mode := range []string{"empty_batch", "older_then_empty", "no_response"} {
		t.Run(mode, func(t *testing.T) {
			p := newFakePlatform()
			clicks := 0
			url := ""
			p.readViewsResponse = func(tool string, args map[string]any) map[string]any {
				if tool == "computer.browser_open" {
					url = stringFromAny(args["url"])
				}
				if tool == "computer.computer_use" && args["action"] == "click" {
					clicks++
					return map[string]any{"current_url": url}
				}
				if tool == "computer.browser_extract" {
					h := readFixtureHTML(2, 3)
					if mode == "older_then_empty" && clicks >= 1 {
						h = readFixtureHTML(1, 2, 3)
					}
					h = strings.Replace(h, "</body>", `<div id="older" onclick="readOlder()">Load Older Messages</div></body>`, 1)
					if (mode == "empty_batch" && clicks >= 1) || (mode == "older_then_empty" && clicks >= 2) {
						h = strings.Replace(h, "</body>", `<span id="1791877466388"> </span><hr></body>`, 1)
					}
					return map[string]any{"current_url": url, "html": h, "rendered": true}
				}
				return nil
			}
			ctx, app := newTestCtx(t, p)
			c := fixtureReadViews()
			c.Views = c.Views[:1]
			v := &c.Views[0]
			v.UseEntryPage = true
			v.URLPattern = `^https://example.com/library$`
			v.Pagination = actorReadPagination{Mode: "next", Next: actorLocator{Text: "Load Older Messages", Selector: "#older", Exact: true, SOMOnly: true}, ClickMode: "selector", EndSelector: `body > span[id]:matchesOwn(^\s*$):not(:has(*)) + hr`, MaxPages: 5, AdvanceTimeoutMS: 1000, SettleMS: 500, StableRounds: 2, OnStall: "partial"}
			c.AllowPartial = true
			b, _ := json.Marshal(c)
			var conf map[string]any
			json.Unmarshal(b, &conf)
			rec := saveFixtureActor(t, ctx, app, map[string]any{"schema_version": 1, "read_only": true, "allowed_hosts": []any{"example.com"}, "limits": map[string]any{"step_retries": 0}, "steps": []any{map[string]any{"action": "inspect_views", "read_views": conf}}})
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
			cov := mapFromAny(o["coverage"])
			if r["status"] != "completed" {
				t.Fatal(r["error"])
			}
			if (cov["inspection_complete"] == true) != (mode != "no_response") {
				t.Fatalf("false completion: %#v", cov)
			}
			want := 2
			if mode == "older_then_empty" {
				want = 3
			}
			if intFromAny(o["item_count"]) != want {
				t.Fatalf("missing history records: %#v", o)
			}
			if mode == "older_then_empty" && clicks != 2 {
				t.Fatalf("expected real older page then verified empty batch, got %d clicks", clicks)
			}
		})
	}
}
