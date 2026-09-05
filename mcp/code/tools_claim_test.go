package main

import (
	"context"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestIssueClaimantUsesAuthenticatedCaller(t *testing.T) {
	t.Run("agent", func(t *testing.T) {
		callCtx := sdk.WithCaller(context.Background(), &sdk.Caller{AgentID: 42})
		owner, label, err := issueClaimant(callCtx, &sdk.AppCtx{})
		if err != nil {
			t.Fatalf("issueClaimant: %v", err)
		}
		if owner != "agent:42" || label != "Agent 42" {
			t.Fatalf("claimant = (%q, %q), want (%q, %q)", owner, label, "agent:42", "Agent 42")
		}
	})

	t.Run("delegated user", func(t *testing.T) {
		callCtx := sdk.WithCaller(context.Background(), &sdk.Caller{
			SubjectType:  "account",
			SubjectID:    "user-7",
			SubjectEmail: "marco@example.com",
		})
		owner, label, err := issueClaimant(callCtx, &sdk.AppCtx{})
		if err != nil {
			t.Fatalf("issueClaimant: %v", err)
		}
		if owner != "user:account:user-7" || label != "marco@example.com" {
			t.Fatalf("claimant = (%q, %q)", owner, label)
		}
	})

	t.Run("missing caller", func(t *testing.T) {
		if _, _, err := issueClaimant(context.Background(), &sdk.AppCtx{}); err == nil {
			t.Fatal("expected missing caller to be rejected")
		}
	})
}
