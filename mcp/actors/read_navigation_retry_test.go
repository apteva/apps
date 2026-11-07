package main

import (
	"context"
	"strings"
	"testing"
)

func TestReadNavigationRetryDoesNotReplayUploadOrCommit(t *testing.T) {
	for _, tc := range []struct {
		name                         string
		failures, retries, wantReads int
		message                      string
		wantFailed                   bool
	}{
		{"transient_recovers", 1, 1, 2, "", false},
		{"bounded_exhaustion", 5, 2, 3, "", true},
		{"explicit_zero", 1, 0, 1, "", true},
		{"nontransient_stops", 1, 2, 1, "authentication denied", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p := newFakePlatform()
			p.mediaSOM = true
			p.failNavigateRemaining = tc.failures
			p.failNavigationURL = "https://example.com/metadata"
			p.navigateError = tc.message
			ctx, app := newTestCtx(t, p)
			d := map[string]any{
				"schema_version": 1, "allowed_hosts": []string{"example.com"},
				"browser": map[string]any{"context_id": "saved-creator", "backend": "local", "persist": true},
				"limits":  map[string]any{"step_retries": 0},
				"steps": []any{
					map[string]any{"action": "goto", "url": "https://example.com/draft"},
					map[string]any{"action": "upload_file", "locator": map[string]any{"text": "Add image", "role": "button", "exact": true, "som_only": true}, "base64": "aGVsbG8=", "filename": "original.png", "mime_type": "image/png"},
					map[string]any{"action": "goto", "url": "https://example.com/metadata", "read_only": true, "read_retries": tc.retries},
					map[string]any{"action": "click", "locator": map[string]any{"selector": "button.send"}, "once_key": "original-publish", "expected_effect": "immediate_external_commit", "confirm_consequence": "immediate_external_commit"},
				},
			}
			rec := saveFixtureActor(t, ctx, app, d)
			if _, err := app.toolActorRun(ctx, map[string]any{"actor_id": rec.ID}); err != nil {
				t.Fatal(err)
			}
			queued, err := claimActorRun(ctx)
			if err != nil {
				t.Fatal(err)
			}
			_ = app.executeActorRun(context.Background(), ctx, queued)
			run, err := getActorRun(ctx, queued.ID)
			if err != nil {
				t.Fatal(err)
			}
			if (run["status"] == "failed") != tc.wantFailed {
				t.Fatalf("status=%v error=%v", run["status"], run["error"])
			}
			reads, uploads, commits := 0, 0, 0
			for _, call := range p.callsSnapshot() {
				if call.app != "computer" {
					continue
				}
				if call.args["url"] == "https://example.com/metadata" && (call.tool == "browser_open" || call.tool == "computer_use" && call.args["action"] == "navigate") {
					reads++
				}
				if call.tool == "browser_open" && call.args["context_id"] != "saved-creator" {
					t.Fatal("creator context changed", call.args)
				}
				if call.tool == "computer_use" && call.args["action"] == "upload_file" {
					uploads++
				}
				if call.tool == "computer_use" && call.args["action"] == "click" {
					commits++
				}
			}
			wantCommits := 1
			if tc.wantFailed {
				wantCommits = 0
			}
			if reads != tc.wantReads || uploads != 1 || commits != wantCommits {
				t.Fatalf("reads=%d uploads=%d commits=%d", reads, uploads, commits)
			}
			out := mapFromAny(run["output"])
			if mapFromAny(out["execution_progress"])["commit_attempted"] != !tc.wantFailed {
				t.Fatal(out["execution_progress"])
			}
		})
	}
}

func TestReadNavigationRetryRejectsWritesAndUnsafeConfiguration(t *testing.T) {
	for _, action := range []string{"upload_file", "click", "click_verified", "fill", "key", "goto_secret", "observe_json"} {
		n := 1
		if validateReadRetries(actorStep{Action: action, ReadOnly: true, ReadRetries: &n}) == nil {
			t.Fatal("accepted", action)
		}
	}
	for _, s := range []actorStep{
		{Action: "goto", ReadRetries: intPointer(1)},
		{Action: "goto", ReadOnly: true, ReadRetries: intPointer(3)},
		{Action: "goto", ReadOnly: true, ReadRetries: intPointer(-1)},
		{Action: "goto", ReadOnly: true, ReadRetries: intPointer(1), OnceKey: "write"},
		{Action: "goto", ReadOnly: true, ReadRetries: intPointer(1), Optional: true},
		{Action: "goto", ReadOnly: true, ReadRetries: intPointer(1), ExpectedEffect: "financial_action"},
	} {
		if validateReadRetries(s) == nil {
			t.Fatal("unsafe configuration accepted", s)
		}
	}
	ctx, app := newTestCtx(t, newFakePlatform())
	d := testActorDefinition()
	d["steps"].([]any)[0].(map[string]any)["read_retries"] = 1
	if _, err := app.toolActorSave(ctx, map[string]any{"name": "invalid", "definition": d}); err == nil || !strings.Contains(err.Error(), "read_only goto") {
		t.Fatal(err)
	}
}

func intPointer(n int) *int { return &n }
