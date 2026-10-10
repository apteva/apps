package main

import (
	"context"
	"encoding/json"
	"fmt"
	"golang.org/x/net/html"
	"net/url"
	"strings"
	"testing"
)

func TestDirectCollectionPaging(t *testing.T) {
	for _, mode := range []string{"complete", "stalled", "limit", "wrong_identity", "commit_control", "empty_unknown"} {
		t.Run(mode, func(t *testing.T) {
			plat := newFakePlatform()
			page := 0
			clicks := 0
			current := ""
			plat.readViewsResponse = func(tool string, in map[string]any) map[string]any {
				switch tool {
				case "computer.browser_open":
					current = stringFromAny(in["url"])
				case "computer.browser_extract":
					h := readFixtureHTML(1, 2)
					if page > 0 {
						h = readFixtureHTML(1, 2, 3)
					}
					if page == 0 {
						h = strings.Replace(h, "</body>", `<div id="more">Load Older Items</div></body>`, 1)
					}
					if mode == "wrong_identity" {
						h = strings.Replace(h, "/creator", "/other", 1)
					}
					if mode == "empty_unknown" {
						h = readFixtureHTML()
					}
					return map[string]any{"html": h, "current_url": current, "rendered": true}
				case "computer.computer_use":
					switch in["action"] {
					case "navigate":
						current = stringFromAny(in["url"])
						return map[string]any{"current_url": current}
					case "screenshot":
						effect := ""
						if mode == "commit_control" {
							effect = "message_send"
						}
						return map[string]any{"current_url": current, "som_revision": 1, "som": []any{map[string]any{"id": "more", "tag": "div", "accessible_name": "Load Older Items", "effect": effect}}}
					case "click":
						if in["expected_effect"] != "navigation_only" || in["target_id"] != "more" || in["coordinate"] != nil {
							t.Fatal("unverified pagination")
						}
						clicks++
						if mode != "stalled" {
							page++
						}
						return map[string]any{"current_url": current}
					}
				}
				return nil
			}
			ctx, app := newTestCtx(t, plat)
			c := fixtureReadViews()
			c.Views = c.Views[:1]
			v := &c.Views[0]
			v.UseEntryPage = true
			v.LinkSelector = ""
			v.Pagination = actorReadPagination{Mode: "next", Next: actorLocator{Text: "Load Older Items", Exact: true, SOMOnly: true, Selector: "#more"}, EndWhenNextAbsent: true, MaxPages: 8, StableRounds: 2, SettleMS: 500}
			c.EntryQuery = map[string]string{"q": "name & other=#value"}
			v.URLPattern = `^https://example.com/library\?`
			if mode == "limit" {
				v.Pagination.MaxPages = 1
			}
			if err := validateReadViews(&c); err != nil {
				t.Fatal(err)
			}
			b, _ := json.Marshal(c)
			var conf map[string]any
			json.Unmarshal(b, &conf)
			rec := saveFixtureActor(t, ctx, app, map[string]any{"schema_version": 1, "read_only": true, "allowed_hosts": []any{"example.com"}, "limits": map[string]any{"max_pages": 20, "max_items": 100, "max_duration_seconds": 60, "step_retries": 0}, "steps": []any{map[string]any{"action": "inspect_views", "read_views": conf}}})
			queued, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID})
			if err != nil {
				t.Fatal(err)
			}
			run, _ := claimActorRun(ctx)
			app.executeActorRun(context.Background(), ctx, run)
			r, _ := getActorRun(ctx, queued.(map[string]any)["run_id"].(int64))
			if (r["status"] == "completed") != (mode == "complete") {
				t.Fatalf("%s: %v", mode, r["error"])
			}
			u, _ := url.Parse(current)
			if u.Query().Get("q") != "name & other=#value" || len(u.Query()) != 1 {
				t.Fatal("query values were not safely encoded")
			}
			if mode == "complete" && clicks != 1 {
				t.Fatalf("clicks=%d", clicks)
			}
			if (mode == "wrong_identity" || mode == "commit_control" || mode == "empty_unknown") && clicks != 0 {
				t.Fatal("unsafe click")
			}
		})
	}
}
func TestExactMessageAndNewRecordAssertions(t *testing.T) {
	for _, tt := range []struct {
		body string
		id   any
		good bool
	}{{"Hello\nWorld", 101, true}, {"hello\nWorld", 101, false}, {"Hello World", 101, false}, {"Hello\nWorld", 100, false}, {"Hello\nWorld", "invalid", false}} {
		e := &actorExecution{lastValues: map[string]any{"body": tt.body, "id": tt.id, "baseline": 100}}
		err := e.assertValues(actorStep{Assertions: map[string]actorAssertion{"body": {EqualsExact: "Hello\nWorld"}, "id": {GreaterThanField: "baseline"}}})
		if (err == nil) != tt.good {
			t.Fatal(fmt.Sprint(tt, err))
		}
	}
}
func TestManyFieldsPreserveAttachmentsAndOptionOrder(t *testing.T) {
	root, _ := html.Parse(strings.NewReader(`<body><select><option value="">Any</option><option value="model">Models</option></select><a href="/a">One</a><a href="/b">Two</a></body>`))
	item, err := extractNodeItem(root, map[string]actorField{"values": {Selector: "option", Attribute: "value", Many: true}, "labels": {Selector: "option", Many: true}, "attachments": {Selector: "a", Attribute: "href", Type: "url", Many: true}}, "https://example.com/thread")
	if err != nil {
		t.Fatal(err)
	}
	b, _ := json.Marshal(item)
	want := `{"attachments":["https://example.com/a","https://example.com/b"],"labels":["Any","Models"],"values":["","model"]}`
	if string(b) != want {
		t.Fatal(string(b))
	}
}
