package clickguard

import (
	"errors"
	"strings"
	"testing"

	computer "github.com/apteva/apps/mcp/computer/internal/browser/api"
)

func TestValidateRejectsLoadingAndDisabledTargets(t *testing.T) {
	for _, target := range []Target{
		{Tag: "button", AccessibleName: "Publish", Loading: true},
		{Tag: "button", AccessibleName: "Publish", Disabled: true},
	} {
		if err := Validate(target, Options{}); err == nil {
			t.Fatalf("expected rejection for %+v", target)
		}
	}
}

func TestConsequenceGuardRequiresMatchingIntentAndAcknowledgement(t *testing.T) {
	target := Target{Tag: "button", AccessibleName: "Publish", Dangerous: true, DestructiveEffect: "immediate_publish"}
	tests := []struct {
		name    string
		options Options
		code    string
	}{
		{name: "missing intent", options: Options{ExpectedText: "Publish", EnforceConsequence: true}, code: "consequence_confirmation_required"},
		{name: "navigation intent", options: Options{ExpectedText: "Publish", ExpectedEffect: "open_configuration", EnforceConsequence: true}, code: "semantic_intent_mismatch"},
		{name: "missing acknowledgement", options: Options{ExpectedText: "Publish", ExpectedEffect: "immediate_external_commit", EnforceConsequence: true}, code: "consequence_confirmation_required"},
		{name: "wrong acknowledgement", options: Options{ExpectedText: "Publish", ExpectedEffect: "immediate_external_commit", ConfirmConsequence: "message_send", EnforceConsequence: true}, code: "consequence_confirmation_required"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := Validate(target, test.options)
			var rejection *ConsequenceError
			if !errors.As(err, &rejection) || rejection.Code != test.code || rejection.DetectedEffect != "immediate_external_commit" {
				t.Fatalf("rejection=%T %v", err, err)
			}
		})
	}
	confirmed := Options{ExpectedText: "Publish", ExpectedEffect: "immediate_external_commit", ConfirmConsequence: "immediate_external_commit", EnforceConsequence: true}
	if err := Validate(target, confirmed); err != nil {
		t.Fatalf("confirmed consequence rejected: %v", err)
	}
	var result computer.ClickResult
	StoreResult(&result, target, confirmed, true)
	if result.DetectedEffect != "immediate_external_commit" || !result.Confirmed || !result.ActionDispatched {
		t.Fatalf("click result=%+v", result)
	}
}

func TestConsequenceGuardKeepsOrdinaryConfigurationClicksSimple(t *testing.T) {
	target := Target{Tag: "button", AccessibleName: "Set publish date"}
	if err := Validate(target, Options{ExpectedText: "Set publish date", ExpectedEffect: "open_configuration", EnforceConsequence: true}); err != nil {
		t.Fatalf("ordinary configuration click rejected: %v", err)
	}
	if err := Validate(target, Options{ExpectedText: "Set publish date", ExpectedEffect: "navigation_only", ConfirmConsequence: "immediate_external_commit", EnforceConsequence: true}); err != nil {
		t.Fatalf("mechanically populated optional consequence fields blocked an ordinary target: %v", err)
	}
	if got := CanonicalEffect("schedule_publish"); got != "scheduled_external_commit" {
		t.Fatalf("canonical schedule effect=%q", got)
	}
}

func TestValidateRequiresExactExpectationForDangerousCoordinate(t *testing.T) {
	target := Target{Tag: "button", AccessibleName: "Publish", Dangerous: true, DestructiveEffect: "immediate_publish"}
	if err := Validate(target, Options{RequireExpectedIfDangerous: true}); err == nil || !strings.Contains(err.Error(), "expected_text") {
		t.Fatalf("missing expectation error = %v", err)
	}
	if err := Validate(target, Options{ExpectedText: "Schedule", RequireExpectedIfDangerous: true}); err == nil || !strings.Contains(err.Error(), `expected target "Schedule"`) {
		t.Fatalf("mismatch error = %v", err)
	}
	if err := Validate(target, Options{ExpectedText: "Publish", RequireExpectedIfDangerous: true}); err != nil {
		t.Fatalf("exact expected text rejected: %v", err)
	}
}

func TestValidatePreservesLabelClicksButRejectsCoordinatesInOpaqueFrames(t *testing.T) {
	target := Target{Tag: "iframe", OpaqueFrame: true}
	if err := Validate(target, Options{ExpectedText: "Continue"}); err != nil {
		t.Fatalf("current cross-frame label rejected: %v", err)
	}
	if err := Validate(target, Options{ExpectedText: "Continue", RequireExpectedIfDangerous: true}); err == nil || !strings.Contains(err.Error(), "cross-origin frame") {
		t.Fatalf("opaque coordinate error = %v", err)
	}
}
