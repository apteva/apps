package clickguard

import (
	"strings"
	"testing"
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
