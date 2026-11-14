package main

import (
	"strings"
	"testing"

	"golang.org/x/net/html"
)

func TestClips4SalePayoutHistoryPreservesAdjustmentSign(t *testing.T) {
	def := realPerformerDefinition(t)
	operation := def.Operations["get_payout_history"]
	if !operation.ReadOnly {
		t.Fatal("payout history must be read-only")
	}
	view := operation.Steps[len(operation.Steps)-2].ReadViews.Views[0]
	root, err := html.Parse(strings.NewReader(`<table><tbody><tr><td>Nov 7, 2026</td><td>Adjustment</td><td>SEPA</td><td>- $15.10</td></tr></tbody></table>`))
	if err != nil {
		t.Fatal(err)
	}
	item, err := extractNodeItem(root, view.Fields, "https://workspace.clips4sale.com/payout-history")
	if err != nil || item["amount_display"] != "- $15.10" || item["currency"] != "$" {
		t.Fatalf("unexpected payout history evidence: %v, %v", item, err)
	}
}

func TestClips4SalePayoutMinimumRequiresAuthorizationAndReadback(t *testing.T) {
	def := realPerformerDefinition(t)
	operation := def.Operations["set_payout_minimum"]
	steps := operation.Steps
	if steps[0].Assertions["payout_minimum_change_authorized"].EqualsExact != true {
		t.Fatal("the first step must require explicit payout change authorization")
	}
	checkedCurrent := false
	commits := 0
	for _, step := range steps {
		if step.Assertions["before_minimum_usd"].Equals == "{{expected_current_minimum_usd}}" {
			checkedCurrent = true
		}
		if step.Action == "click" {
			if !checkedCurrent || step.ExpectedText != "Save" || step.OnceKey == "" || step.OnceScope != "context" {
				t.Fatalf("unguarded payout threshold save: %+v", step)
			}
			commits++
		}
	}
	if commits != 1 || steps[len(steps)-1].Assertions["saved_minimum_usd"].Equals != "{{new_minimum_usd}}" {
		t.Fatal("a threshold write requires exactly one save and a persisted value check")
	}
}

func TestClips4SaleInitialPayoutStopsAtVerification(t *testing.T) {
	def := realPerformerDefinition(t)
	for _, name := range []string{"set_payout_account_type", "set_payout_sole_trader_account_type", "set_payout_method"} {
		t.Run(name, func(t *testing.T) {
			steps := def.Operations[name].Steps
			if steps[0].Assertions["payout_setup_authorized"].EqualsExact != true {
				t.Fatal("initial setup must start with an explicit authorization check")
			}
			commit := -1
			for i, step := range steps {
				if step.Action != "click" || step.ExpectedText != "Save" {
					continue
				}
				if commit != -1 || step.OnceScope != "context" || step.OnceKey == "" {
					t.Fatal("initial setup must have one guarded save")
				}
				commit = i
			}
			if commit < 0 || len(steps) < commit+5 {
				t.Fatal("only read-only verification may follow the save")
			}
			for _, step := range steps[commit+1:] {
				if !step.ReadOnly {
					t.Fatal("unexpected write after save")
				}
			}
			extract, guard := steps[commit+2], steps[commit+3]
			for _, tc := range []struct {
				body string
				stop bool
			}{
				{body: "<body>Once saved, all changes will send you either a phone validation code or an email.</body>"},
				{body: `<body><input type="tel" aria-label="Please enter OTP character 1"></body>`, stop: true},
				{body: "<body>Enter the 6-digit code sent to you.</body>", stop: true},
			} {
				root, err := html.Parse(strings.NewReader(tc.body))
				if err != nil {
					t.Fatal(err)
				}
				item, err := extractNodeItem(root, extract.Fields, "https://workspace.clips4sale.com")
				if err != nil {
					t.Fatal(err)
				}
				exec := actorExecution{lastValues: item}
				if err := exec.assertValues(guard); (err != nil) != tc.stop {
					t.Fatalf("unexpected verification decision for %s: %v", tc.body, err)
				}
			}
		})
	}
}
