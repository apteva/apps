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
	for _, mode := range []string{"complete", "delayed", "same_end", "reordered_stall", "stalled", "stalled_partial", "stalled_partial_identity", "limit", "item_limit_partial", "wrong_identity", "commit_control", "empty_unknown", "partial", "partial_identity"} {
		t.Run(mode, func(t *testing.T) {
			plat := newFakePlatform()
			page := 0
			clicks := 0
			current := ""
			readsAfterClick := 0
			plat.readViewsResponse = func(tool string, in map[string]any) map[string]any {
				switch tool {
				case "computer.browser_open":
					current = stringFromAny(in["url"])
				case "computer.browser_extract":
					h := readFixtureHTML(1, 2)
					if page > 0 {
						h = readFixtureHTML(1, 2, 3)
						readsAfterClick++
						if mode == "same_end" || (mode == "delayed" && readsAfterClick <= 2) {
							h = readFixtureHTML(1, 2)
						}
					}
					if page == 0 || (mode == "delayed" && readsAfterClick <= 2) {
						h = strings.Replace(h, "</body>", `<div id="more">Load Older Items</div></body>`, 1)
					}
					if mode == "reordered_stall" && page > 0 {
						h = strings.Replace(readFixtureHTML(2, 1), "</body>", `<div id="more">Load Older Items</div></body>`, 1)
					}
					if mode == "wrong_identity" || mode == "partial_identity" || (mode == "stalled_partial_identity" && clicks > 0) {
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
						if !strings.HasPrefix(mode, "stalled") {
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
			v.Pagination = actorReadPagination{Mode: "next", Next: actorLocator{Text: "Load Older Items", Exact: true, SOMOnly: true, Selector: "#more"}, EndWhenNextAbsent: true, MaxPages: 8, StableRounds: 2, SettleMS: 500, AdvanceTimeoutMS: 500}
			if mode == "delayed" {
				v.Pagination.AdvanceTimeoutMS = 3000
			}
			c.EntryQuery = map[string]string{"q": "name & other=#value"}
			v.URLPattern = `^https://example.com/library\?`
			if mode == "partial" || mode == "partial_identity" || mode == "item_limit_partial" {
				c.AllowPartial = true
			}
			if strings.HasPrefix(mode, "stalled_partial") {
				c.AllowPartial = true
				v.Pagination.OnStall = "partial"
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
			maxItems := 100
			if mode == "item_limit_partial" {
				maxItems = 2
			}
			rec := saveFixtureActor(t, ctx, app, map[string]any{"schema_version": 1, "read_only": true, "allowed_hosts": []any{"example.com"}, "limits": map[string]any{"max_pages": 20, "max_items": maxItems, "max_duration_seconds": 60, "step_retries": 0}, "steps": []any{map[string]any{"action": "inspect_views", "read_views": conf}}})
			queued, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID})
			if err != nil {
				t.Fatal(err)
			}
			run, _ := claimActorRun(ctx)
			app.executeActorRun(context.Background(), ctx, run)
			r, _ := getActorRun(ctx, queued.(map[string]any)["run_id"].(int64))
			if (r["status"] == "completed") != (mode == "complete" || mode == "partial" || mode == "delayed" || mode == "same_end" || mode == "stalled_partial" || mode == "item_limit_partial") {
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
			if mode == "stalled_partial" || mode == "item_limit_partial" {
				cov := r["output"].(map[string]any)["coverage"].(map[string]any)
				if cov["inspection_complete"] != false || cov["more_results_remaining"] != true || clicks != 1 || intFromAny(r["output"].(map[string]any)["item_count"]) != 2 {
					t.Fatal("stalled pagination lost verified records or overstated coverage")
				}
			}
			if (mode == "delayed" || mode == "same_end") && clicks != 1 {
				t.Fatal("pagination was clicked again while waiting")
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

func TestReadNavigationDoesNotSkipShortViewportControls(t *testing.T) {
	plat := newFakePlatform()
	top, clicks := 0, 0
	plat.readViewsResponse = func(tool string, in map[string]any) map[string]any {
		if tool != "computer.computer_use" {
			return nil
		}
		switch in["action"] {
		case "screenshot":
			targets := []any{}
			if 450 >= top && 480 <= top+400 {
				targets = append(targets, map[string]any{"id": "next", "text": "Older", "effect": "navigation_only"})
			}
			return map[string]any{"current_url": "https://example.com/thread", "som_revision": top + 1, "som": targets, "scroll_regions": []any{map[string]any{"id": "doc", "name": "Document", "role": "document", "h": 400, "scroll_top": top, "max_scroll_y": 1000}}}
		case "scroll":
			if in["target_id"] != "doc" || in["direction"] != "down" || intArg(in, "amount") >= 400 {
				t.Fatal("scroll left an unobserved viewport gap")
			}
			top += intArg(in, "amount")
			return map[string]any{"scroll": map[string]any{"actual_target_id": "doc"}}
		case "click":
			if in["target_id"] != "next" || in["expected_effect"] != "navigation_only" || in["coordinate"] != nil {
				t.Fatal("unguarded pagination click")
			}
			clicks++
			return map[string]any{"current_url": "https://example.com/thread"}
		}
		return nil
	}
	ctx, app := newTestCtx(t, plat)
	e := &actorExecution{app: app, ctx: ctx, workerCtx: context.Background(), currentURL: "https://example.com/thread", session: &browserSession{SessionID: "short"}, lastValues: map[string]any{}, definition: actorDefinition{AllowedHosts: []string{"example.com"}}}
	if err := e.clickReadNavigation(actorLocator{Text: "Older", Exact: true, SOMOnly: true}); err != nil {
		t.Fatal(err)
	}
	if clicks != 1 {
		t.Fatal("control was not clicked exactly once")
	}
}

func TestReadNavigationRevealsControlAtDocumentEnd(t *testing.T) {
	plat := newFakePlatform()
	top, clicks, scrolls := 0, 0, 0
	plat.readViewsResponse = func(tool string, in map[string]any) map[string]any {
		if tool != "computer.computer_use" {
			return nil
		}
		switch in["action"] {
		case "screenshot":
			targets := []any{}
			if top == 60000 {
				targets = append(targets, map[string]any{"id": "more", "text": "More"})
			}
			return map[string]any{"current_url": "https://example.com/list", "som_revision": scrolls + 1, "som": targets, "scroll_regions": []any{map[string]any{"id": "doc", "name": "Document", "role": "document", "h": 400, "scroll_top": top, "max_scroll_y": 60000}}}
		case "scroll":
			if in["direction"] != "down" || in["target_id"] != "doc" {
				t.Fatal("wrong document boundary")
			}
			top = min(60000, top+intArg(in, "amount"))
			scrolls++
			return map[string]any{"scroll": map[string]any{"actual_target_id": "doc"}}
		case "click":
			if in["target_id"] != "more" || in["expected_effect"] != "navigation_only" || in["coordinate"] != nil {
				t.Fatal("unguarded click")
			}
			clicks++
			return map[string]any{"current_url": "https://example.com/list"}
		}
		return nil
	}
	ctx, app := newTestCtx(t, plat)
	e := &actorExecution{app: app, ctx: ctx, workerCtx: context.Background(), currentURL: "https://example.com/list", session: &browserSession{SessionID: "end"}, lastValues: map[string]any{}, definition: actorDefinition{AllowedHosts: []string{"example.com"}}}
	if err := e.clickReadNavigation(actorLocator{Text: "More", Exact: true, SOMOnly: true}, "end"); err != nil {
		t.Fatal(err)
	}
	if clicks != 1 || scrolls != 6 {
		t.Fatal("document end was not reached efficiently")
	}
}

func TestPartialStallPolicyRequiresExplicitPartialReads(t *testing.T) {
	c := fixtureReadViews()
	c.Views[0].Pagination.OnStall = "partial"
	if validateReadViews(&c) == nil {
		t.Fatal("partial stall policy accepted without partial read policy")
	}
	c.AllowPartial = true
	if err := validateReadViews(&c); err != nil {
		t.Fatal(err)
	}
	c.Views[0].Pagination.OnStall = "ignore"
	if validateReadViews(&c) == nil {
		t.Fatal("unknown stall policy accepted")
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
