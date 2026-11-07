package main

import (
	"context"
	"strings"
	"testing"
)

func TestExactLabelSets(t *testing.T) {
	for _, tc := range []struct {
		name             string
		actual, expected any
		valid            bool
	}{
		{"different creator prices", "$25/month\nSilver", []any{"Silver", "$25/month"}, true},
		{"no paid tiers", "", []any{}, true},
		{"missing", "Silver", []any{"Silver", "Gold"}, false},
		{"extra", "Silver\nLegacy", []any{"Silver"}, false},
		{"substring", "Elite Member", []any{"Elite"}, false},
		{"duplicates extracted", "Gold\nGold", []any{"Gold", "Silver"}, false},
		{"duplicates requested", "Gold", []any{"Gold", "Gold"}, false},
		{"invalid input", "Gold", "Gold", false},
		{"multiline name", "Gold\nSilver", []any{"Gold\nSilver"}, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := actorAssertLabelSet(tc.actual, tc.expected)
			if (err == nil) != tc.valid {
				t.Fatalf("error=%v, valid=%t", err, tc.valid)
			}
		})
	}
}

func TestLabelListAdmissionAndRendering(t *testing.T) {
	def := actorDefinition{SchemaVersion: 1, AllowedHosts: []string{"example.com"}, Steps: []actorStep{
		{Action: "set_checked", Locator: actorLocator{Role: "checkbox", Exact: true, SOMOnly: true}, Labels: "{{labels}}", Checked: true},
		{Action: "assert_values", Assertions: map[string]actorAssertion{"selected": {EqualsSet: "{{labels}}"}}},
	}}
	if err := validateActorDefinition(def); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []any{"Gold", []any{""}, []any{"Gold", "Gold"}, []any{1}, []any{" Gold"}, []any{"Gold\nSilver"}, make([]string, 51)} {
		if _, err := renderActorDefinition(def, map[string]any{"labels": bad}); err == nil {
			t.Fatalf("accepted invalid labels %#v", bad)
		}
	}
	if _, err := renderActorDefinition(def, map[string]any{"labels": []any{"Gold", "$1/month"}}); err != nil {
		t.Fatal(err)
	}
	def.Steps[0].Locator.Exact = false
	if err := validateActorDefinition(def); err == nil {
		t.Fatal("inexact list locator accepted")
	}
}

func TestLabelListUsesFreshSemanticObservation(t *testing.T) {
	plat := newFakePlatform()
	plat.formSOM = true
	ctx, app := newTestCtx(t, plat)
	rec := saveFixtureActor(t, ctx, app, map[string]any{
		"schema_version": 1, "allowed_hosts": []any{"example.com"},
		"steps": []any{
			map[string]any{"action": "goto", "url": "https://example.com"},
			map[string]any{"action": "set_checked", "locator": map[string]any{"exact": true, "som_only": true, "text_suffix_pattern": `(?:\s+[0-9]+\s+members?)?`}, "labels": "{{labels}}", "checked": true},
		}, "output_schema": map[string]any{},
	})
	if _, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID, "input": map[string]any{"labels": []any{"Notifications", "Tier"}}}); err != nil {
		t.Fatal(err)
	}
	run, err := claimActorRun(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err := app.executeActorRun(context.Background(), ctx, run); err != nil {
		t.Fatal(err)
	}
	observes, actions := 0, 0
	for _, call := range plat.callsSnapshot() {
		if call.args["action"] == "screenshot" && call.args["include_som"] == true {
			observes++
		}
		if call.args["action"] == "set_checked" {
			actions++
			if call.args["expected_name"] != nil {
				t.Fatal("metadata locator forwarded an unstable accessible name")
			}
			if call.args["target_id"] == nil || call.args["som_revision"] != "form-1" || call.args["coordinate"] != nil || call.args["selector"] != nil {
				t.Fatalf("unguarded action %#v", call.args)
			}
		}
	}
	if observes < 2 || actions != 2 {
		t.Fatalf("observations=%d actions=%d", observes, actions)
	}
}

func TestLabelMismatchStopsBeforeCommit(t *testing.T) {
	e := &actorExecution{lastValues: map[string]any{"selected": "Elite Member"}}
	err := e.assertValues(actorStep{Action: "assert_values", Assertions: map[string]actorAssertion{"selected": {EqualsSet: []any{"Elite"}}}})
	if err == nil || !strings.Contains(err.Error(), "missing") {
		t.Fatalf("mismatched tier accepted: %v", err)
	}
}

func TestExactNamesAllowOnlyConfiguredMetadataSuffix(t *testing.T) {
	loc := actorLocator{Text: "Elite", Role: "menuitemcheckbox", Exact: true, SOMOnly: true, TextSuffixPattern: `(?:\s+[0-9]+\s+members?)?`}
	for _, tc := range []struct {
		text  string
		valid bool
	}{{"Elite", true}, {"Elite 42 members", true}, {"Elite 1 member", true}, {"Elite Member", false}, {"Elite Member 42 members", false}, {"Elite anything", false}, {"Legacy Elite 42 members", false}} {
		if got := somTargetMatches(loc, setOfMarkTarget{AccessibleName: tc.text, Role: "menuitemcheckbox"}); got != tc.valid {
			t.Fatalf("name %q match=%t want=%t", tc.text, got, tc.valid)
		}
	}
	loc.Text = "$1/month"
	if !somTargetMatches(loc, setOfMarkTarget{AccessibleName: "$1/month 0 members", Role: "menuitemcheckbox"}) {
		t.Fatal("literal price label treated as regex")
	}
}
