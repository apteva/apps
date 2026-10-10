package main

import (
	"context"
	"encoding/json"
	"fmt"
	"golang.org/x/net/html"
	"strings"
	"testing"
)

func fixtureReadViews() actorReadViews {
	v := actorReadView{Name: "posts", Covers: []string{"published", "scheduled"}, LinkSelector: `a[href="/posts"]`, URLPattern: `^https://example.com/(?:library|posts)$`, ReadySelector: "#ready", EmptySelector: "#empty", LoadingSelector: "#loading", ErrorSelector: "#error", Items: "article", KeyField: "id", Fields: map[string]actorField{"id": {Attribute: "data-id", Required: true}, "title": {Selector: "h2", Required: true}, "status": {Attribute: "data-status", Required: true}, "edit_url": {Selector: "a", Type: "url", Attribute: "href", Required: true}}, Pagination: actorReadPagination{Mode: "scroll", ScrollTargetName: "Document", MaxPages: 20, StableRounds: 2, SettleMS: 500}}
	d := v
	d.Name = "drafts"
	d.Covers = []string{"draft"}
	d.LinkSelector = `a[href="/drafts"]`
	d.URLPattern = `^https://example.com/(?:library|drafts)$`
	return actorReadViews{EntryURL: "https://example.com/library", IdentityURL: "https://example.com/creator", Identity: actorField{Selector: "a#identity", Attribute: "href", Required: true}, Views: []actorReadView{v, d}, TimeoutMS: 1000}
}
func readFixtureHTML(ids ...int) string {
	s := `<body><a id="identity" href="https://example.com/creator">Creator</a><div id="ready"></div><a href="/posts">Posts</a><a href="/drafts">Drafts</a>`
	for _, id := range ids {
		status := "published"
		if id == 2 {
			status = "scheduled"
		}
		if id == 5 {
			status = "draft"
		}
		s += fmt.Sprintf(`<article data-id="%d" data-status="%s"><h2>Post %d</h2><a href="/posts/%d/edit">Edit</a></article>`, id, status, id, id)
	}
	return s + "</body>"
}
func TestReadViewsCoverage(t *testing.T) {
	for _, mode := range []string{"complete", "inaccessible", "limit", "identity", "truncated", "empty_unknown", "empty_verified", "wrong_scroll", "changed"} {
		t.Run(mode, func(t *testing.T) {
			plat := newFakePlatform()
			url := ""
			frame := 0
			scrolls := 0
			plat.readViewsResponse = func(tool string, in map[string]any) map[string]any {
				switch tool {
				case "computer.browser_open":
					url = stringFromAny(in["url"])
					frame = 0
				case "computer.computer_use":
					switch in["action"] {
					case "navigate":
						url = stringFromAny(in["url"])
						frame = 0
						return map[string]any{"current_url": url}
					case "scroll":
						frame = minInt(frame+1, 2)
						scrolls++
						r := map[string]any{"actual_target_id": "scroll_document"}
						if mode == "wrong_scroll" {
							r["actual_target_id"] = "sidebar"
						}
						return map[string]any{"current_url": url, "scroll": r}
					case "screenshot":
						return map[string]any{"som_revision": scrolls + 1, "scroll_regions": []any{map[string]any{"id": "scroll_document", "name": "Document", "role": "document", "scroll_top": frame * 600, "max_scroll_y": 1200}}}
					}
				case "computer.browser_extract":
					h := readFixtureHTML()
					if strings.HasSuffix(url, "/posts") {
						switch frame {
						case 0:
							h = readFixtureHTML(1, 2)
						case 1:
							h = readFixtureHTML(2, 3)
						case 2:
							h = readFixtureHTML(3, 4)
						}
					}
					if strings.HasSuffix(url, "/drafts") {
						h = readFixtureHTML(5)
						if mode == "inaccessible" {
							h = strings.Replace(h, `id="ready"`, `id="error"`, 1)
						}
					}
					if mode == "identity" {
						h = strings.Replace(h, "/creator", "/other", 1)
					}
					if mode == "changed" && frame == 1 {
						h = strings.Replace(h, "Post 2", "Changed 2", 1)
					}
					if mode == "empty_unknown" || mode == "empty_verified" {
						h = readFixtureHTML()
						if mode == "empty_verified" {
							h = strings.Replace(h, "</body>", `<div id="empty"></div></body>`, 1)
						}
					}
					return map[string]any{"html": h, "rendered": true, "current_url": url, "truncated": mode == "truncated"}
				}
				return nil
			}
			ctx, app := newTestCtx(t, plat)
			c := fixtureReadViews()
			if mode == "limit" {
				c.Views[0].Pagination.MaxPages = 1
			}
			b, _ := json.Marshal(c)
			var conf map[string]any
			json.Unmarshal(b, &conf)
			rec := saveFixtureActor(t, ctx, app, map[string]any{"schema_version": 1, "read_only": true, "allowed_hosts": []any{"example.com"}, "limits": map[string]any{"max_pages": 40, "max_items": 100, "max_duration_seconds": 60, "step_retries": 0}, "steps": []any{map[string]any{"action": "inspect_views", "read_views": conf}}, "output_schema": map[string]any{}})
			queued, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID})
			if err != nil {
				t.Fatal(err)
			}
			run, err := claimActorRun(ctx)
			if err != nil {
				t.Fatal(err)
			}
			if err := app.executeActorRun(context.Background(), ctx, run); err != nil {
				t.Fatal(err)
			}
			r, err := getActorRun(ctx, queued.(map[string]any)["run_id"].(int64))
			if err != nil {
				t.Fatal(err)
			}
			out := r["output"].(map[string]any)
			cov := out["coverage"].(map[string]any)
			want := mode == "complete" || mode == "empty_verified"
			if cov["inspection_complete"] != want || (r["status"] == "completed") != want {
				t.Fatalf("status=%v coverage=%#v error=%v", r["status"], cov, r["error"])
			}
			if want && mode == "complete" && intFromAny(out["item_count"]) != 5 {
				t.Fatalf("dedup/beyond-first-page=%#v", out)
			}
			if !want && cov["more_results_remaining"] != true {
				t.Fatalf("lost incomplete coverage: %#v", cov)
			}
			if mode == "inaccessible" && intFromAny(out["item_count"]) != 4 {
				t.Fatal("partial dataset lost")
			}
			for _, call := range plat.callsSnapshot() {
				if call.tool == "browser_extract" && call.args["readability"] != false {
					t.Fatal("readability enabled")
				}
				if call.tool == "computer_use" {
					a := stringFromAny(call.args["action"])
					if a != "navigate" && a != "screenshot" && a != "scroll" {
						t.Fatalf("write or unexpected action %s", a)
					}
					if call.args["coordinate"] != nil {
						t.Fatal("coordinate interaction")
					}
				}
			}
		})
	}
}
func TestReadOnlyRejectsWritesAndOptionalCoverage(t *testing.T) {
	for _, action := range []string{"fill", "click", "key", "upload_file", "set_checked", "paginate"} {
		def := actorDefinition{SchemaVersion: 1, ReadOnly: true, AllowedHosts: []string{"example.com"}, Steps: []actorStep{{Action: action}}}
		if err := validateActorDefinition(def); err == nil {
			t.Fatalf("write %s accepted", action)
		}
	}
	def := actorDefinition{SchemaVersion: 1, ReadOnly: true, AllowedHosts: []string{"example.com"}, Steps: []actorStep{{Action: "inspect_views", ReadViews: ptrReadViews(fixtureReadViews()), Optional: true}}}
	if err := validateActorDefinition(def); err == nil {
		t.Fatal("optional coverage accepted")
	}
	def.Operations = map[string]actorOperation{"read": {ReadOnly: true, Steps: []actorStep{{Action: "click"}}}}
	def.Steps = nil
	def.ReadOnly = false
	if err := validateActorDefinition(def); err == nil {
		t.Fatal("named read-only write accepted")
	}
}
func ptrReadViews(c actorReadViews) *actorReadViews { return &c }

func TestObservePageReturnsVisibleNavigationAndExactURL(t *testing.T) {
	plat := newFakePlatform()
	plat.readViewsResponse = func(tool string, in map[string]any) map[string]any {
		if tool == "computer.browser_extract" {
			return map[string]any{"html": `<body><h1>Creator</h1><a href="/drafts">Drafts</a><a href="/hidden">Hidden</a><button aria-label="Filter">Filter</button></body>`, "current_url": "https://example.com/library", "title": "Library", "rendered": true}
		}
		if tool == "computer.computer_use" && in["action"] == "screenshot" {
			return map[string]any{"som_revision": 1, "som": []any{map[string]any{"id": "a1", "tag": "a", "accessible_name": "Drafts"}, map[string]any{"id": "b1", "tag": "button", "accessible_name": "Filter"}}}
		}
		return nil
	}
	ctx, app := newTestCtx(t, plat)
	rec := saveFixtureActor(t, ctx, app, map[string]any{"schema_version": 1, "read_only": true, "allowed_hosts": []any{"example.com"}, "steps": []any{map[string]any{"action": "goto", "url": "https://example.com/library"}, map[string]any{"action": "observe_page", "items": "body", "readability": false, "fields": map[string]any{"creator_name": map[string]any{"selector": "h1", "required": true}}}}, "output_schema": map[string]any{}})
	queued, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID})
	if err != nil {
		t.Fatal(err)
	}
	run, _ := claimActorRun(ctx)
	app.executeActorRun(context.Background(), ctx, run)
	r, _ := getActorRun(ctx, queued.(map[string]any)["run_id"].(int64))
	if r["status"] != "completed" {
		t.Fatal(r["error"])
	}
	out := r["output"].(map[string]any)
	item := out["items"].([]any)[0].(map[string]any)
	nav := item["visible_navigation"].([]any)
	if item["current_url"] != "https://example.com/library" || item["creator_name"] != "Creator" || len(nav) != 1 || nav[0].(map[string]any)["url"] != "https://example.com/drafts" {
		t.Fatalf("observation=%#v", item)
	}
}

func TestReadPaginationRequiresFreshNavigationOnlyEffect(t *testing.T) {
	for _, effect := range []string{"navigation_only", "immediate_external_commit", ""} {
		t.Run(effect, func(t *testing.T) {
			plat := newFakePlatform()
			clicked := false
			plat.readViewsResponse = func(tool string, in map[string]any) map[string]any {
				if tool == "computer.computer_use" && in["action"] == "screenshot" {
					return map[string]any{"som_revision": 2, "som": []any{map[string]any{"id": "next", "tag": "button", "accessible_name": "Next", "effect": effect}}}
				}
				if tool == "computer.computer_use" && in["action"] == "click" {
					clicked = true
					return map[string]any{}
				}
				return nil
			}
			ctx, app := newTestCtx(t, plat)
			exec := &actorExecution{app: app, ctx: ctx, workerCtx: context.Background(), session: &browserSession{SessionID: "sess_1"}, definition: actorDefinition{AllowedHosts: []string{"example.com"}}, currentURL: "https://example.com"}
			err := exec.clickReadNavigation(actorLocator{Text: "Next", Exact: true, SOMOnly: true})
			want := effect == "navigation_only"
			if clicked != want || (err == nil) != want {
				t.Fatalf("effect=%s clicked=%v err=%v", effect, clicked, err)
			}
		})
	}
}

func TestReadEndMustMatchAuthoritativeTotal(t *testing.T) {
	for _, tc := range []struct {
		footer   string
		count    int
		complete bool
	}{{"1 - 3 of 3", 3, true}, {"1 - 3 of 9", 3, false}, {"", 3, false}, {"1 - 0 of 0", 0, true}} {
		root, err := html.Parse(strings.NewReader(`<body><footer>` + tc.footer + `</footer></body>`))
		if err != nil {
			t.Fatal(err)
		}
		v := fixtureReadViews().Views[0]
		v.Total = &actorField{Selector: "footer", Type: "number", Required: true, Pattern: `of (\d+)$`}
		result := &actorViewCoverage{MoreRemaining: true}
		err = completeReadView(v, result, root, tc.count, "https://example.com", "end")
		if (err == nil) != tc.complete || result.Complete != tc.complete || result.MoreRemaining == tc.complete {
			t.Fatalf("footer=%q count=%d result=%#v err=%v", tc.footer, tc.count, result, err)
		}
	}
}
