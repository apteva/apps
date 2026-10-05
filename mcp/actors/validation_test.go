package main

import (
	"strings"
	"testing"
)

func TestAssertValuesReconcilesPayoutBeforeCommit(t *testing.T) {
	e := &actorExecution{lastValues: map[string]any{
		"gross": 68.81, "fee": 1.32, "net": 67.49,
		"currency": "USD", "destination": "Bank transfer (Revolut **** 5600)",
	}}
	step := actorStep{Action: "assert_values", Assertions: map[string]actorAssertion{
		"gross":       {Equals: 68.81},
		"currency":    {Equals: "USD"},
		"destination": {Contains: "Revolut"},
		"net":         {DifferenceOf: []string{"gross", "fee"}},
	}}
	if err := e.assertValues(step); err != nil {
		t.Fatalf("valid payout rejected: %v", err)
	}

	e.lastValues["net"] = 67.50
	if err := e.assertValues(step); err == nil || !strings.Contains(err.Error(), "net") {
		t.Fatalf("arithmetic mismatch was accepted: %v", err)
	}
}

func TestAssertValuesRejectsSymbolOnlyCurrency(t *testing.T) {
	e := &actorExecution{lastValues: map[string]any{"currency": "$"}}
	if err := e.assertValues(actorStep{Action: "assert_values", Assertions: map[string]actorAssertion{
		"currency": {Equals: "USD"},
	}}); err == nil {
		t.Fatal("symbol-only currency passed an ISO currency assertion")
	}
}

func TestValidateActorDefinitionSupportsCardinalityAndVerifiedClick(t *testing.T) {
	def := actorDefinition{
		SchemaVersion: 1,
		AllowedHosts:  []string{"example.com"},
		Steps: []actorStep{
			{Action: "extract", Items: "button", MinItems: 1, MaxItems: 1, Fields: map[string]actorField{"control": {Type: "text", Required: true}}},
			{Action: "assert_values", Assertions: map[string]actorAssertion{"control": {Contains: "Withdraw"}}},
			{Action: "click_verified", VerifiedField: "control", ExpectedEffect: "financial_action", ConfirmConsequence: "financial_action", OnceKey: "{{request_id}}"},
		},
	}
	if err := validateActorDefinition(def); err != nil {
		t.Fatalf("new generic validation contract rejected: %v", err)
	}
	def.Steps[0].MaxItems = 0
	def.Steps[0].MinItems = -1
	if err := validateActorDefinition(def); err == nil {
		t.Fatal("negative extraction cardinality accepted")
	}
}
