package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestPayoutCanonicalNoCommitPreflight(t *testing.T) {
	cases := []struct {
		name, method, token, symbol, gross, fee, net, control string
		fail                                                  bool
	}{
		{"bank_alias", "Bank transfer", "5600", "€", "19.57", "0.50", "19.07", "Withdraw €19.07", false},
		{"same_label", "Local bank transfer", "5600", "€", "19.57", "0.50", "19.07", "Withdraw €19.07", false},
		{"wrong_provider", "PayPal", "5600", "€", "19.57", "0.50", "19.07", "Withdraw €19.07", true},
		{"wrong_account", "Bank transfer", "1234", "€", "19.57", "0.50", "19.07", "Withdraw €19.07", true},
		{"wrong_currency", "Bank transfer", "5600", "$", "19.57", "0.50", "19.07", "Withdraw $19.07", true},
		{"wrong_gross", "Bank transfer", "5600", "€", "29.57", "0.50", "29.07", "Withdraw €29.07", true},
		{"bad_arithmetic", "Bank transfer", "5600", "€", "19.57", "0.50", "19.57", "Withdraw €19.57", true},
		{"negative_net", "Bank transfer", "5600", "€", "19.57", "20.50", "0.00", "Withdraw €0.00", true},
		{"wrong_button_amount", "Bank transfer", "5600", "€", "19.57", "0.50", "19.07", "Withdraw €19.57", true},
		{"wrong_button_currency", "Bank transfer", "5600", "€", "19.57", "0.50", "19.07", "Withdraw $19.07", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			raw, err := os.ReadFile("testdata/payout_preflight.json")
			if err != nil {
				t.Fatal(err)
			}
			var def map[string]any
			json.Unmarshal(raw, &def)
			plat := newFakePlatform()
			plat.openBackendOverride = "browserbase"
			plat.waitOutput = map[string]any{"matched": true}
			page := ""
			opened := false
			plat.readViewsResponse = func(tool string, args map[string]any) map[string]any {
				if tool == "computer.browser_open" {
					page = stringFromAny(args["url"])
				}
				if tool == "computer.computer_use" {
					if args["action"] == "navigate" {
						page = stringFromAny(args["url"])
						opened = false
					}
					if args["action"] == "click" {
						opened = true
					}
					if args["action"] == "screenshot" && args["include_som"] == true {
						return map[string]any{"current_url": page, "som_revision": "fresh", "som": []map[string]any{{"label": 1, "role": "button", "text": "Withdraw"}}}
					}
				}
				if tool != "computer.browser_extract" {
					return nil
				}
				body := `Your currency Euro EUR € Local bank transfer xxxx-5600`
				if strings.HasSuffix(page, "/payouts") {
					body = `Available to withdraw €19.57 Bank transfer Withdraw`
				}
				if opened {
					body += fmt.Sprintf(`<div role="dialog">Withdraw earnings Available %s%s %s xxxx-%s Transfer fee -%s%s You'll receive %s%s <button>%s</button></div>`, tc.symbol, tc.gross, tc.method, tc.token, tc.symbol, tc.fee, tc.symbol, tc.net, tc.control)
				}
				return map[string]any{"current_url": page, "text": body, "html": "<html><body>" + body + "</body></html>", "extraction_backend": "browser_dom", "rendered": true}
			}
			ctx, app := newTestCtx(t, plat)
			rec := saveFixtureActor(t, ctx, app, def)
			_, err = app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID, "input": map[string]any{"request_id": tc.name, "expected_amount": "19.57", "expected_currency": "EUR", "expected_destination_provider": "Local bank transfer", "expected_destination_token": "5600"}})
			if err != nil {
				t.Fatal(err)
			}
			queued, err := claimActorRun(ctx)
			if err != nil {
				t.Fatal(err)
			}
			_ = app.executeActorRun(context.Background(), ctx, queued)
			result, err := getActorRun(ctx, queued.ID)
			if err != nil {
				t.Fatal(err)
			}
			if (result["status"] == "failed") != tc.fail {
				t.Fatalf("status=%v error=%v", result["status"], result["error"])
			}
			out := mapFromAny(result["output"])
			progress := mapFromAny(out["execution_progress"])
			if progress["commit_attempted"] != false || progress["preparation_attempted"] != true {
				t.Fatal(progress)
			}
			clicks := 0
			for _, call := range plat.callsSnapshot() {
				if call.tool == "computer_use" && call.args["action"] == "click" {
					clicks++
					if call.args["expected_effect"] != "open_configuration" {
						t.Fatal("dispatched a final commit", call.args)
					}
				}
			}
			if clicks != 1 {
				t.Fatal("expected only the modal opener", clicks)
			}
			if !tc.fail {
				evidence, _ := out["assertion_evidence"].([]any)
				if len(evidence) != 2 {
					t.Fatal("missing raw/canonical method evidence", out)
				}
				last := mapFromAny(evidence[1])
				if last["raw_actual"] != "Local bank transfer" || last["raw_expected"] != tc.method || last["canonical_actual"] != "bank_transfer" || last["canonical_expected"] != "bank_transfer" {
					t.Fatal(last)
				}
			}
		})
	}
}

func TestCanonicalMapFailsClosed(t *testing.T) {
	aliases := map[string]string{"Local bank transfer": "bank_transfer", "Bank transfer": "bank_transfer", "PayPal": "paypal", "Hyperwallet": "hyperwallet", "Revolut": "revolut"}
	e := &actorExecution{}
	for _, pair := range [][2]string{{"PayPal", "Hyperwallet"}, {"Revolut", "Bank transfer"}, {"Wire transfer", "Bank transfer"}, {"bank transfer", "Bank transfer"}} {
		if e.compareAssertionValues("method", pair[0], pair[1], actorAssertion{CanonicalMap: aliases, Equals: pair[1]}) == nil {
			t.Fatal("unapproved equivalence", pair)
		}
	}
	for _, a := range []actorAssertion{{CanonicalMap: aliases}, {CanonicalMap: aliases, Equals: "Bank transfer", Tolerance: 1}, {CanonicalMap: map[string]string{"Bank transfer": "{{input}}"}, Equals: "Bank transfer"}} {
		if validateCanonicalAssertion(a) == nil {
			t.Fatal("invalid canonical map accepted")
		}
	}
}

func TestPreparePhaseCannotHideFinancialCommit(t *testing.T) {
	for _, s := range []actorStep{{Action: "click_verified", EffectPhase: "prepare", ExpectedEffect: "financial_action", ConfirmConsequence: "financial_action", OnceKey: "commit"}, {Action: "click", EffectPhase: "prepare", ExpectedEffect: "open_configuration"}, {Action: "wait", EffectPhase: "prepare"}, {Action: "click", EffectPhase: "unknown"}} {
		if validateEffectPhase(s) == nil {
			t.Fatal("unsafe phase accepted", s)
		}
	}
}

func TestCommitPhaseProgressAndGuardRemainConservative(t *testing.T) {
	for _, phase := range []string{"", "commit", "prepare"} {
		t.Run("phase_"+phase, func(t *testing.T) {
			plat := newFakePlatform()
			plat.failAction = "click"
			ctx, app := newTestCtx(t, plat)
			effect := "financial_action"
			if phase == "prepare" {
				effect = "open_configuration"
			}
			rec := saveFixtureActor(t, ctx, app, map[string]any{"schema_version": 1, "allowed_hosts": []string{"example.com"}, "limits": map[string]any{"step_retries": 0}, "steps": []any{map[string]any{"action": "goto", "url": "https://example.com"}, map[string]any{"action": "click", "locator": map[string]any{"selector": "button"}, "effect_phase": phase, "expected_effect": effect, "confirm_consequence": effect, "once_key": "logical:effect"}}})
			_, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID})
			if err != nil {
				t.Fatal(err)
			}
			queued, err := claimActorRun(ctx)
			if err != nil {
				t.Fatal(err)
			}
			app.executeActorRun(context.Background(), ctx, queued)
			result, err := getActorRun(ctx, queued.ID)
			if err != nil {
				t.Fatal(err)
			}
			out := mapFromAny(result["output"])
			progress := mapFromAny(out["execution_progress"])
			if progress["commit_attempted"] != (phase != "prepare") || progress["preparation_attempted"] != (phase == "prepare") {
				t.Fatal(progress)
			}
			if proveUncommitted(result, rec.Definition) == nil {
				t.Fatal("uncertain guarded effect became recoverable")
			}
			effects, _ := out["effects"].([]any)
			if len(effects) != 1 || mapFromAny(effects[0])["state"] != "uncertain" {
				t.Fatal(effects)
			}
		})
	}
}
