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
	for _, mode := range []string{"complete", "stalled", "limit", "wrong_identity", "commit_control", "empty_unknown", "partial", "partial_identity"} {
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
					if mode == "wrong_identity" || mode == "partial_identity" {
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
			if mode == "partial" || mode == "partial_identity" {
				c.AllowPartial = true
			}
			if mode == "limit" || mode == "partial" {
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
			if (r["status"] == "completed") != (mode == "complete" || mode == "partial") {
				t.Fatalf("%s: %v", mode, r["error"])
			}
			u, _ := url.Parse(current)
			if u.Query().Get("q") != "name & other=#value" || len(u.Query()) != 1 {
				t.Fatal("query values were not safely encoded")
			}
			if mode == "partial" {
				cov := r["output"].(map[string]any)["coverage"].(map[string]any)
				if cov["inspection_complete"] != false || cov["more_results_remaining"] != true || clicks != 0 {
					t.Fatal("partial coverage was misreported")
				}
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
func TestReadNavigationRevealsControlAboveLongHistory(t *testing.T) {
	plat := newFakePlatform()
	top := 60000
	clicks := 0
	scrolls := 0
	plat.readViewsResponse = func(tool string, in map[string]any) map[string]any {
		if tool != "computer.computer_use" {
			return nil
		}
		switch in["action"] {
		case "screenshot":
			targets := []any{}
			if top == 0 {
				targets = append(targets, map[string]any{"id": "older", "tag": "div", "text": "Load Older Messages"})
			}
			return map[string]any{"current_url": "https://example.com/thread", "som_revision": scrolls + 1, "som": targets, "scroll_regions": []any{map[string]any{"id": "document", "name": "Document", "role": "document", "scroll_top": top, "max_scroll_y": 60000}}}
		case "scroll":
			if in["target_id"] != "document" || in["expected_name"] != "Document" || in["direction"] != "up" {
				t.Fatal("unverified reveal")
			}
			top = max(0, top-intArg(in, "amount"))
			scrolls++
			return map[string]any{"scroll": map[string]any{"actual_target_id": "document"}}
		case "click":
			clicks++
			if in["target_id"] != "older" || in["expected_effect"] != "navigation_only" || in["coordinate"] != nil {
				t.Fatal("unverified click")
			}
			return map[string]any{"current_url": "https://example.com/thread"}
		}
		return nil
	}
	ctx, app := newTestCtx(t, plat)
	e := &actorExecution{app: app, ctx: ctx, workerCtx: context.Background(), currentURL: "https://example.com/thread", session: &browserSession{SessionID: "long-thread"}, lastValues: map[string]any{}, definition: actorDefinition{AllowedHosts: []string{"example.com"}}}
	if err := e.clickReadNavigation(actorLocator{Text: "Load Older Messages", Exact: true, SOMOnly: true}); err != nil {
		t.Fatal(err)
	}
	if clicks != 1 || scrolls != 6 {
		t.Fatalf("clicks %d scrolls %d", clicks, scrolls)
	}
}
func TestPartialCoverageCannotPrecedeWrites(t *testing.T) {
	c := fixtureReadViews()
	c.AllowPartial = true
	def := actorDefinition{SchemaVersion: 1, AllowedHosts: []string{"example.com"}, Steps: []actorStep{{Action: "inspect_views", ReadOnly: true, ReadViews: &c}, {Action: "click", Locator: actorLocator{Selector: "button"}}}}
	if validateActorDefinition(def) == nil {
		t.Fatal("partial inspection may not authorize writes")
	}
}
func TestNewlinePolicyValidation(t *testing.T) {
	for _, mode := range []string{"preserve", "compact", "paragraph"} {
		def := actorDefinition{SchemaVersion: 1, AllowedHosts: []string{"example.com"}, Steps: []actorStep{{Action: "set_text", Locator: actorLocator{Selector: "textarea"}, Text: "Hello", NewlineMode: mode}}}
		if (validateActorDefinition(def) == nil) != (mode != "paragraph") {
			t.Fatal(mode)
		}
	}
}
