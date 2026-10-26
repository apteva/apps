package main

import (
	"context"
	"encoding/json"
	"os"
	"testing"
	"time"
)

func TestRevealControlShortViewport(t *testing.T) {
	for _, mode := range []string{"success", "middle", "missing", "ambiguous", "disabled", "loading", "wrong_scroll", "changed_page", "limit"} {
		t.Run(mode, func(t *testing.T) {
			p := newFakePlatform()
			top, clicks, scrolls := 0, 0, 0
			p.readViewsResponse = func(tool string, in map[string]any) map[string]any {
				if tool != "computer.computer_use" {
					return nil
				}
				switch in["action"] {
				case "screenshot":
					targets := []any{}
					y := 1170
					if mode == "middle" {
						y = 450
					}
					if y >= top && y+35 <= top+400 && mode != "missing" {
						target := map[string]any{"id": "send", "text": "Send!", "role": "button", "effect": "message_send", "disabled": mode == "disabled", "loading": mode == "loading"}
						targets = append(targets, target)
						if mode == "ambiguous" {
							targets = append(targets, target)
						}
					}
					u := "https://example.com/thread"
					if mode == "changed_page" {
						u = "https://example.com/other"
					}
					return map[string]any{"current_url": u, "som_revision": top + 1, "som": targets, "scroll_regions": []any{map[string]any{"id": "doc", "name": "Document", "role": "document", "h": 400, "scroll_top": top, "max_scroll_y": 1800}}}
				case "scroll":
					if in["target_id"] != "doc" || in["som_revision"] != float64(top+1) || in["expected_name"] != "Document" || in["expected_role"] != "document" || intArg(in, "amount") >= 400 {
						t.Fatal("unverified or nonoverlapping scroll", in)
					}
					scrolls++
					if in["direction"] == "down" {
						top = min(1800, top+intArg(in, "amount"))
					} else {
						top = max(0, top-intArg(in, "amount"))
					}
					id := "doc"
					if mode == "wrong_scroll" {
						id = "other"
					}
					return map[string]any{"scroll": map[string]any{"actual_target_id": id}}
				case "click":
					clicks++
				}
				return nil
			}
			ctx, app := newTestCtx(t, p)
			rec := saveFixtureActor(t, ctx, app, map[string]any{"schema_version": 1, "allowed_hosts": []string{"example.com"}, "steps": []any{map[string]any{"action": "goto", "url": "https://example.com/thread"}}})
			app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID})
			run, _ := claimActorRun(ctx)
			e := &actorExecution{app: app, ctx: ctx, run: run, workerCtx: context.Background(), deadline: time.Now().Add(time.Minute), currentURL: "https://example.com/thread", session: &browserSession{SessionID: "short"}, definition: actorDefinition{AllowedHosts: []string{"example.com"}}}
			s := actorStep{Action: "reveal_control", Locator: actorLocator{Text: "Send!", Exact: true, SOMOnly: true}, Reveal: &actorReveal{ScrollTargetName: "Document", Direction: "down", MaxScrolls: 40}}
			if mode == "limit" {
				s.Reveal.MaxScrolls = 1
			}
			err := e.revealControl(s)
			if (err == nil) != (mode == "success" || mode == "middle") {
				t.Fatal(mode, err)
			}
			if clicks != 0 || scrolls > s.Reveal.MaxScrolls {
				t.Fatal("reveal clicked or exceeded bounds")
			}
		})
	}
}

func TestMissingSemanticSendIsNotDispatched(t *testing.T) {
	p := newFakePlatform()
	p.readViewsResponse = func(tool string, in map[string]any) map[string]any {
		if tool == "computer.computer_use" && in["action"] == "screenshot" {
			return map[string]any{"som": []any{}, "som_revision": 1}
		}
		return nil
	}
	ctx, app := newTestCtx(t, p)
	r := saveFixtureActor(t, ctx, app, map[string]any{"schema_version": 1, "allowed_hosts": []string{"example.com"}, "limits": map[string]any{"step_retries": 0}, "steps": []any{map[string]any{"action": "goto", "url": "https://example.com"}, map[string]any{"action": "click", "locator": map[string]any{"text": "Send!", "exact": true, "som_only": true}, "expected_effect": "message_send", "confirm_consequence": "message_send", "once_key": "logical-message"}}})
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := app.toolActorRun(ctx, map[string]any{"actor_id": r.ID}); err != nil {
			t.Fatal(err)
		}
		run, _ := claimActorRun(ctx)
		app.executeActorRun(context.Background(), ctx, run)
		out, _ := getActorRun(ctx, run.ID)
		if out["status"] != "failed" || mapFromAny(mapFromAny(out["output"])["execution_progress"])["commit_attempted"] != false {
			t.Fatal(out)
		}
	}
	var state string
	ctx.AppDB().QueryRow(`SELECT state FROM actors_effect_guards WHERE actor_id=?`, r.ID).Scan(&state)
	if state != "not_dispatched" {
		t.Fatal(state)
	}
	for _, call := range p.callsSnapshot() {
		if call.args["action"] == "click" {
			t.Fatal("missing control dispatched")
		}
	}
}

func TestAdultfolioEverywhereAndSendConfiguration(t *testing.T) {
	raw, _ := os.ReadFile("examples/adultfolio-control.json")
	var d actorDefinition
	if err := json.Unmarshal(raw, &d); err != nil {
		t.Fatal(err)
	}
	a := d.Operations["search_unmessaged_models_by_radius_and_age"].Steps[0].ReadViews.Views[0].Verification.Assertions["radius_miles"]
	a.Equals = nil
	for _, tc := range []struct {
		value string
		valid bool
	}{{"50", true}, {"9000", true}, {"9999", true}, {"9001", false}, {"10000", false}, {"0", false}, {"-1", false}, {"unknown", false}} {
		e := &actorExecution{lastValues: map[string]any{"radius": tc.value}}
		if (e.assertValues(actorStep{Assertions: map[string]actorAssertion{"radius": a}}) == nil) != tc.valid {
			t.Fatal(tc.value)
		}
	}
	for _, name := range []string{"reply", "initial_send", "prepare_reply", "prepare_initial_send"} {
		steps := d.Operations[name].Steps
		revealed, asserted := false, false
		for _, s := range steps {
			if s.Action == "reveal_control" {
				if err := validateActorReveal(s); err != nil {
					t.Fatal(err)
				}
				revealed = true
			}
			if revealed && s.Action == "assert_values" {
				asserted = true
			}
			if s.ExpectedText == "Send!" && (!revealed || !asserted || s.OnceKey == "" || !s.Locator.SOMOnly) {
				t.Fatal("send without reveal, fresh assertions and guard", name)
			}
		}
		if !revealed || !asserted {
			t.Fatal("missing preflight", name)
		}
	}
}
