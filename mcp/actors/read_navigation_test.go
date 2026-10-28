package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

func TestReadViewsNativeTabNavigation(t *testing.T) {
	for _, mode := range []string{"complete", "badge", "bad_badge", "stream_once", "stream_twice", "dangerous", "wrong_effect", "ambiguous", "loading", "wrong_url", "wrong_identity", "rejected"} {
		t.Run(mode, func(t *testing.T) {
			plat := newFakePlatform()
			if mode == "stream_once" || mode == "stream_twice" {
				plat.actionErrors = map[string][]error{"navigate": {errors.New("net::ERR_HTTP2_SERVER_REFUSED_STREAM")}}
				if mode == "stream_twice" {
					plat.actionErrors["navigate"] = append(plat.actionErrors["navigate"], errors.New("net::ERR_HTTP2_SERVER_REFUSED_STREAM"))
				}
			}
			current := "https://example.com/library"
			clicks := 0
			plat.readViewsResponse = func(tool string, in map[string]any) map[string]any {
				switch tool {
				case "computer.browser_open":
					current = stringFromAny(in["url"])
				case "computer.browser_extract":
					h := readFixtureHTML(5)
					if clicks > 0 && mode == "wrong_identity" {
						h = `<body><a id="identity" href="https://example.com/other"></a><div id="ready"></div><div id="empty"></div></body>`
					}
					return map[string]any{"html": h, "current_url": current, "rendered": true}
				case "computer.computer_use":
					switch in["action"] {
					case "navigate":
						current = stringFromAny(in["url"])
						return map[string]any{"current_url": current}
					case "wait":
						return map[string]any{"current_url": current}
					case "screenshot":
						target := map[string]any{"id": "drafts-tab", "tag": "button", "role": "tab", "accessible_name": "Drafts", "effect": "navigation_only"}
						if mode == "badge" {
							target["accessible_name"] = "Drafts3"
						}
						if mode == "bad_badge" {
							target["accessible_name"] = "Drafts All"
						}
						if mode == "dangerous" {
							target["dangerous"] = true
						}
						if mode == "wrong_effect" {
							target["effect"] = "immediate_external_commit"
						}
						if mode == "loading" {
							target["loading"] = true
						}
						som := []any{target}
						if mode == "ambiguous" {
							som = append(som, target)
						}
						return map[string]any{"current_url": current, "som_revision": 17, "som": som}
					case "click":
						if in["target_id"] != "drafts-tab" || intFromAny(in["som_revision"]) != 17 || in["expected_effect"] != "navigation_only" || in["coordinate"] != nil {
							t.Fatal("unguarded native tab click", in)
						}
						if mode == "badge" && (in["expected_text"] != nil || in["expected_role"] != "tab") {
							t.Fatal("metadata locator lost fresh target/role guards", in)
						}
						clicks++
						current = "https://example.com/drafts"
						if mode == "wrong_url" {
							current = "https://example.com/other"
						}
						return map[string]any{"current_url": current, "success": mode != "rejected"}
					}
				}
				return nil
			}
			ctx, app := newTestCtx(t, plat)
			c := fixtureReadViews()
			v := c.Views[1]
			v.LinkSelector = ""
			v.Navigation = []actorLocator{{Text: "Drafts", Role: "tab", Exact: true, SOMOnly: true}}
			if mode == "badge" || mode == "bad_badge" {
				v.Navigation[0].TextSuffixPattern = `(?:\s*[0-9]+)?`
			}
			v.URLPattern = `^https://example.com/drafts$`
			v.Pagination = actorReadPagination{Mode: "next", Next: actorLocator{Text: "Next", Selector: "#next", Exact: true, SOMOnly: true}, EndWhenNextAbsent: true, StableRounds: 2, SettleMS: 500, MaxPages: 4}
			c.Views = []actorReadView{v}
			raw, _ := json.Marshal(c)
			var conf map[string]any
			json.Unmarshal(raw, &conf)
			rec := saveFixtureActor(t, ctx, app, map[string]any{"schema_version": 1, "read_only": true, "allowed_hosts": []any{"example.com"}, "limits": map[string]any{"max_pages": 10, "step_retries": 0}, "steps": []any{map[string]any{"action": "goto", "url": "https://example.com/library"}, map[string]any{"action": "inspect_views", "read_views": conf}}})
			q, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID})
			if err != nil {
				t.Fatal(err)
			}
			run, _ := claimActorRun(ctx)
			app.executeActorRun(context.Background(), ctx, run)
			r, _ := getActorRun(ctx, q.(map[string]any)["run_id"].(int64))
			cov := r["output"].(map[string]any)["coverage"].(map[string]any)
			want := mode == "complete" || mode == "stream_once" || mode == "badge"
			if (r["status"] == "completed") != want || cov["inspection_complete"] != want {
				t.Fatalf("status %v coverage %v error %v", r["status"], cov, r["error"])
			}
			if !want && cov["more_results_remaining"] != true {
				t.Fatal("lost incomplete coverage")
			}
			if (mode == "dangerous" || mode == "wrong_effect" || mode == "ambiguous" || mode == "loading") && clicks != 0 {
				t.Fatal("unsafe navigation clicked")
			}
		})
	}
}
func TestReadViewsNativeTabDefinitionGuards(t *testing.T) {
	c := fixtureReadViews()
	c.Views = c.Views[1:]
	c.Views[0].LinkSelector = ""
	c.Views[0].Navigation = []actorLocator{{Text: "Drafts", Exact: true, SOMOnly: true}}
	if err := validateReadViews(&c); err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"selector", "inexact", "fallback", "anchor", "entry", "unbounded"} {
		bad := c
		bad.Views = append([]actorReadView{}, c.Views...)
		bad.Views[0].Navigation = append([]actorLocator{}, c.Views[0].Navigation...)
		switch mode {
		case "selector":
			bad.Views[0].Navigation[0].Selector = "button"
		case "inexact":
			bad.Views[0].Navigation[0].Exact = false
		case "fallback":
			bad.Views[0].Navigation[0].SOMOnly = false
		case "anchor":
			bad.Views[0].LinkSelector = "a"
		case "entry":
			bad.Views[0].UseEntryPage = true
		case "unbounded":
			bad.Views[0].Navigation = make([]actorLocator, 6)
		}
		if validateReadViews(&bad) == nil {
			t.Fatal("invalid navigation accepted", mode)
		}
	}
}

func TestNoRecordRequiresCompleteCoverage(t *testing.T) {
	for _, mode := range []string{"absent", "duplicate", "incomplete", "more", "not_read_only", "missing"} {
		e := &actorExecution{items: []map[string]any{{"title": "New"}}, collectionItems: []map[string]any{{"title": "Existing"}}, coverage: &actorReadCoverage{Complete: true, ReadOnly: true}}
		value := "New"
		switch mode {
		case "duplicate":
			value = "Existing"
		case "incomplete":
			e.coverage.Complete = false
		case "more":
			e.coverage.MoreRemaining = true
		case "not_read_only":
			e.coverage.ReadOnly = false
		case "missing":
			e.coverage = nil
		}
		err := e.assertNoRecord(actorStep{VerifiedField: "title", Value: value})
		if (err == nil) != (mode == "absent") {
			t.Fatal(mode, err)
		}
	}
}

func TestInputAssertionsDoNotReplaceSavedEvidence(t *testing.T) {
	for _, mode := range []string{"accepted", "wrong_class", "missing", "wrong_price"} {
		vars := map[string]any{"asset_class": "no_real_person", "price": 6.99}
		if mode == "wrong_class" {
			vars["asset_class"] = "unknown"
		}
		if mode == "missing" {
			delete(vars, "asset_class")
		}
		if mode == "wrong_price" {
			vars["price"] = -1
		}
		e := &actorExecution{variables: vars, lastValues: map[string]any{"saved_title": "Keep"}}
		err := e.runStep(actorStep{Action: "assert_inputs", Assertions: map[string]actorAssertion{"asset_class": {EqualsExact: "no_real_person"}, "price": {GreaterThan: 0}}})
		if (err == nil) != (mode == "accepted") {
			t.Fatal(mode, err)
		}
		if e.lastValues["saved_title"] != "Keep" {
			t.Fatal("input guard replaced saved evidence")
		}
	}
}

func TestGotoRecordRequiresVerifiedURLAndCoverage(t *testing.T) {
	for _, mode := range []string{"complete", "incomplete", "remaining", "missing", "relative", "outside_host", "uncollected"} {
		t.Run(mode, func(t *testing.T) {
			plat := newFakePlatform()
			ctx, app := newTestCtx(t, plat)
			cov := &actorReadCoverage{Complete: true, ReadOnly: true}
			target := "https://example.com/clip/42"
			if mode == "incomplete" {
				cov.Complete = false
			}
			if mode == "remaining" {
				cov.MoreRemaining = true
			}
			if mode == "missing" {
				target = ""
			}
			if mode == "relative" {
				target = "/clip/42"
			}
			if mode == "outside_host" {
				target = "https://other.example/clip/42"
			}
			e := &actorExecution{run: &actorQueuedRun{ID: 1}, app: app, ctx: ctx, workerCtx: context.Background(), session: &browserSession{SessionID: "fixture"}, definition: actorDefinition{AllowedHosts: []string{"example.com"}}, coverage: cov, collectionItems: []map[string]any{{"edit_url": target}}, lastValues: map[string]any{"edit_url": target}}
			if mode == "uncollected" {
				e.collectionItems = nil
			}
			err := e.gotoRecord(actorStep{VerifiedField: "edit_url"})
			if (err == nil) != (mode == "complete") {
				t.Fatal(mode, err)
			}
			calls := plat.callsSnapshot()
			if mode != "complete" && len(calls) > 0 {
				t.Fatal("unverified navigation dispatched", calls)
			}
		})
	}
	for _, mode := range []string{"mutable", "optional", "missing"} {
		step := actorStep{Action: "goto_record", ReadOnly: true, VerifiedField: "edit_url"}
		if mode == "mutable" {
			step.ReadOnly = false
		}
		if mode == "optional" {
			step.Optional = true
		}
		if mode == "missing" {
			step.VerifiedField = ""
		}
		if validateActorDefinition(actorDefinition{SchemaVersion: 1, AllowedHosts: []string{"example.com"}, Steps: []actorStep{step}}) == nil {
			t.Fatal("invalid goto_record accepted", mode)
		}
	}
}
