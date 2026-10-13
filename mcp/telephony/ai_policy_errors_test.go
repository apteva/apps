package main

import (
	"context"
	"errors"
	"os"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func TestAICallPolicyReminderManifestPermission(t *testing.T) {
	raw, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, text := range []string{string(raw), manifestYAML} {
		manifest, err := sdk.ParseManifest([]byte(text))
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, permission := range manifest.Requires.Permissions {
			found = found || permission == sdk.PermThreadsWrite
		}
		if !found {
			t.Fatal("Server rejects non-main-thread reminders without platform.threads.write")
		}
	}
}

func TestAICallPolicyErrorsSafeAndSpecific(t *testing.T) {
	for _, tc := range []struct {
		err    error
		class  string
		status int
	}{
		{errors.New("platform /api/apps/callback/agents/4/event: http 403: missing permission: platform.threads.write"), "missing_thread_permission", 403},
		{errors.New("http 502: downstream https://secret/token?token=secret Authorization: Bearer secret"), "upstream_unavailable", 502},
		{errors.New("http 401: secret"), "authorization_rejected", 401},
		{errors.New("http 429: secret"), "rate_limited", 429},
		{context.DeadlineExceeded, "timeout", 0}, {context.Canceled, "canceled", 0},
		{errors.New(strings.Repeat("secret", 10000)), "control_request_failed", 0},
	} {
		s := &aiPolicyState{}
		s.recordControlError(time.Now(), "reminder_delivery_failed", tc.err)
		event := s.Events[0]
		if event.ErrorClass != tc.class || event.HTTPStatus != tc.status || strings.Contains(s.LastError, "secret") || strings.Contains(event.Detail, "secret") {
			t.Fatalf("unsafe/missing evidence: %+v", s)
		}
	}
}

func TestAICallPolicyReminderFailureRetainedWithoutReplay(t *testing.T) {
	a, _, _, row, r, base := aiPolicyFixture(t)
	p := &aiReminderPlatform{answerPlatform: &answerPlatform{}, fail: true}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("project-a"), tk.WithPlatform(p))
	policyAdvance(t, r, row, base, "")
	policyAdvance(t, r, row, base.Add(30*time.Second), "remind")
	a.dispatchAIReminder(ctx, r, r.state.ReminderToken)
	deadline := time.Now().Add(time.Second)
	for len(a.aiPolicies.commands) > 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if len(a.aiPolicies.commands) != 0 {
		t.Fatal("reminder did not finish")
	}
	r.mu.Lock()
	if r.state.ReminderSent || !r.state.ReminderIssued || !strings.Contains(r.state.LastError, "control_request_failed") {
		t.Fatal(r.state)
	}
	r.advance(row, r.state.ReminderAt.Add(aiReminderDeliveryLimit))
	snapshot := r.state
	r.mu.Unlock()
	if snapshot.Reason != terminationAIPolicyFailure || !strings.Contains(snapshot.LastError, "control_request_failed") {
		t.Fatal(snapshot)
	}
	if err := a.persistAIPolicy(r, snapshot); err != nil {
		t.Fatal(err)
	}
	saved, err := a.aiPolicyDiagnostics(row.ProjectID, row.ID)
	if err != nil {
		t.Fatal(err)
	}
	events := saved.(aiPolicyState).Events
	if events[len(events)-1].ErrorClass != "control_request_failed" {
		t.Fatal(events)
	}
	a.dispatchAIReminder(ctx, r, snapshot.ReminderToken)
	deadline = time.Now().Add(time.Second)
	for len(a.aiPolicies.commands) > 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	if len(p.reminders) != 1 {
		t.Fatal("ambiguous failures must not replay the reminder")
	}
}
