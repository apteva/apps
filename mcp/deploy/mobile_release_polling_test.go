package main

import (
	"context"
	"encoding/json"
	"testing"
	"time"
)

func pollingRelease(t *testing.T, c *googlePlayCoordinator, provider, status, externalStatus string) *Release {
	t.Helper()
	d, err := dbGetDeploymentByName(c.db, "quota-test", "polling")
	if err != nil {
		t.Fatal(err)
	}
	if d == nil {
		d, err = dbCreateDeployment(c.db, "quota-test", CreateDeploymentInput{
			Name: "polling", TargetKind: "android", Framework: "android", SourceKind: "local", SourceRef: "/src",
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	b, err := dbCreateBuild(c.db, d.ID, "android", "")
	if err != nil {
		t.Fatal(err)
	}
	rel, err := dbCreateRelease(c.db, d.ID, b.ID)
	if err != nil {
		t.Fatal(err)
	}
	meta := mobileReleaseMeta{Platform: "android", PackageName: "com.example.app", VersionCode: "42", Connections: map[string]int64{"play_store": 88}}
	if err := dbUpdateRelease(c.db, rel.ID, map[string]any{
		"provider": provider, "channel": "internal", "status": status, "external_status": externalStatus,
		"release_meta_json": mustJSON(meta),
	}); err != nil {
		t.Fatal(err)
	}
	// Reproduce existing releases whose workflow never left publishing.
	if err := saveWorkflow(rel, d, workflowState{Phase: "publishing", ArtifactSHA256: "retained-digest", Values: map[string]any{"receipt": "keep"}}); err != nil {
		t.Fatal(err)
	}
	rel, err = dbGetRelease(c.db, rel.ID)
	if err != nil {
		t.Fatal(err)
	}
	return rel
}

func TestFinishedMobileReleasesDoNotStarvePendingReleases(t *testing.T) {
	_, c, _ := setupQuotaPlatform(t)
	for i := 0; i < 105; i++ {
		pollingRelease(t, c, "google_play", "live", "completed")
	}
	pending := pollingRelease(t, c, "google_play", "starting", "publishing")
	releases, err := dbListPendingMobileReleases(c.db, 1)
	if err != nil {
		t.Fatal(err)
	}
	if len(releases) != 1 || releases[0].ID != pending.ID {
		t.Fatalf("finished history displaced pending release: %+v", releases)
	}
}

func TestMobileWorkerReconcilesFinishedWorkflowsWithoutProviderCalls(t *testing.T) {
	p, c, _ := setupQuotaPlatform(t)
	tests := []struct {
		provider, status, externalStatus, wantPhase string
	}{
		{"google_play", "live", "completed", "completed"},
		{"google_play", "live", "COMPLETED", "completed"},
		{"app_store_connect", "live", "testflight_available", "completed"},
		{"app_store_connect", "live", "in_beta_testing", "completed"},
		{"app_store_connect", "live", "ready_for_sale", "completed"},
		{"app_store_connect", "live", "ready_for_distribution", "completed"},
		{"google_play", "failed", "failed", "failed"},
		{"google_play", "stopped", "halted", "stopped"},
	}
	releases := make([]*Release, len(tests))
	for i, tt := range tests {
		releases[i] = pollingRelease(t, c, tt.provider, tt.status, tt.externalStatus)
	}
	// Local reconciliation must work even while the account is in cooldown.
	if err := c.recordBackoff(88, 403, nil, json.RawMessage(dailyEditQuotaResponse)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err := (&App{}).syncPendingMobileReleases(context.Background()); err != nil {
			t.Fatal(err)
		}
		googlePlayCoordinators.Delete(c.db)
	}
	if len(p.calls) != 0 {
		t.Fatalf("completed releases contacted provider: %+v", p.calls)
	}
	for i, rel := range releases {
		var raw, config string
		if err := c.db.QueryRow(`SELECT state_json,config_json FROM release_workflows WHERE release_id=?`, rel.ID).Scan(&raw, &config); err != nil {
			t.Fatal(err)
		}
		var state workflowState
		if err := json.Unmarshal([]byte(raw), &state); err != nil {
			t.Fatal(err)
		}
		if state.Phase != tests[i].wantPhase || state.ArtifactSHA256 != "retained-digest" || state.Values["receipt"] != "keep" || config == "" {
			t.Fatalf("workflow not reconciled without losing evidence: %+v", state)
		}
		fresh, err := dbGetRelease(c.db, rel.ID)
		if err != nil || fresh.Status != rel.Status || fresh.ExternalStatus != rel.ExternalStatus {
			t.Fatalf("reconciliation changed provider evidence: %+v, %v", fresh, err)
		}
	}
	if err := googlePlayCoordinatorForContext().before(88); err == nil {
		t.Fatal("reconciliation cleared account cooldown")
	}
}

func TestPendingMobileSelectionKeepsUnfinishedWork(t *testing.T) {
	_, c, _ := setupQuotaPlatform(t)
	for _, tt := range []struct{ provider, status, external string }{
		{"google_play", "live", "inProgress"},
		{"google_play", "live", "in_progress"},
		{"google_play", "live", "sync_error"},
		{"google_play", "starting", "publishing"},
		{"google_play", "starting", "draft"},
		{"app_store_connect", "starting", "processing"},
		{"app_store_connect", "starting", "waiting_for_review"},
		{"app_store_connect", "live", "in_review"},
		{"custom_publisher", "live", "published"},
	} {
		pollingRelease(t, c, tt.provider, tt.status, tt.external)
	}
	if err := dbReconcileFinishedMobileWorkflows(c.db, 0); err != nil {
		t.Fatal(err)
	}
	releases, err := dbListPendingMobileReleases(c.db, 100)
	if err != nil || len(releases) != 9 {
		t.Fatalf("unfinished releases lost: %+v, %v", releases, err)
	}
	for _, rel := range releases {
		var phase string
		if err := c.db.QueryRow(`SELECT json_extract(state_json,'$.phase') FROM release_workflows WHERE release_id=?`, rel.ID).Scan(&phase); err != nil || phase != "publishing" {
			t.Fatalf("unfinished workflow was marked finished: %q, %v", phase, err)
		}
	}
}

func TestTestFlightCompletionReconcilesOnlyItsWorkflow(t *testing.T) {
	p, c, _ := setupQuotaPlatform(t)
	ready := pollingRelease(t, c, "app_store_connect", "starting", "processing")
	pending := pollingRelease(t, c, "app_store_connect", "starting", "processing")
	if err := markTestFlightAvailable(ready, "build-1", "group-1", &mobileReleaseMeta{Platform: "ios", BuildProcessed: true}); err != nil {
		t.Fatal(err)
	}
	for _, tt := range []struct {
		id    int64
		phase string
	}{{ready.ID, "completed"}, {pending.ID, "publishing"}} {
		var phase string
		if err := c.db.QueryRow(`SELECT json_extract(state_json,'$.phase') FROM release_workflows WHERE release_id=?`, tt.id).Scan(&phase); err != nil || phase != tt.phase {
			t.Fatalf("release %d phase=%q, want=%q, err=%v", tt.id, phase, tt.phase, err)
		}
	}
	if len(p.calls) != 0 {
		t.Fatal("completion reconciliation contacted provider")
	}
}

func TestGoogleWorkerStopsAfterCompletionButExplicitSyncStillWorks(t *testing.T) {
	p, c, _ := setupQuotaPlatform(t)
	now := time.Now().UTC()
	c.now = func() time.Time { return now }
	rel := pollingRelease(t, c, "google_play", "starting", "publishing")
	a := &App{}
	if err := a.syncPendingMobileReleases(context.Background()); err != nil {
		t.Fatal(err)
	}
	if got := countIntegrationCalls(p.calls, "create_edit"); got != 1 {
		t.Fatalf("pending release was not checked: %d", got)
	}
	// A new process with expired observations must still avoid terminal work.
	googlePlayCoordinators.Delete(c.db)
	c = googlePlayCoordinatorForContext()
	now = now.Add(24 * time.Hour)
	c.now = func() time.Time { return now }
	for i := 0; i < 5; i++ {
		if err := a.syncPendingMobileReleases(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if got := countIntegrationCalls(p.calls, "create_edit"); got != 1 {
		t.Fatalf("completed beta kept opening edits after restart: %d", got)
	}
	fresh, err := dbGetRelease(c.db, rel.ID)
	if err != nil || fresh.Status != "live" || fresh.ExternalStatus != "completed" {
		t.Fatalf("completion was not retained: %+v, %v", fresh, err)
	}
	if err := a.syncMobileReleaseState(fresh); err != nil {
		t.Fatal(err)
	}
	if got := countIntegrationCalls(p.calls, "create_edit"); got != 2 {
		t.Fatalf("explicit refresh no longer available: %d", got)
	}
}

func TestGoogleSavedCompletionReconcilesSyncErrorWithoutLosingWarnings(t *testing.T) {
	for _, staleObservation := range []bool{false, true} {
		t.Run(map[bool]string{false: "legacy", true: "stale_observation"}[staleObservation], func(t *testing.T) {
			p, c, _ := setupQuotaPlatform(t)
			rel := pollingRelease(t, c, "google_play", "live", "sync_error")
			var meta mobileReleaseMeta
			if err := json.Unmarshal([]byte(rel.ReleaseMetaJSON), &meta); err != nil {
				t.Fatal(err)
			}
			meta.Availability = &availabilityObservation{
				Publication: "completed", State: "unconfirmed", Audience: "internal",
				CheckedAt: "2026-10-09T10:00:00Z", PublishedAt: "2026-10-09T10:00:00Z",
			}
			if staleObservation {
				meta.Observation = &providerObservation{Stale: true, CheckedAt: meta.Availability.CheckedAt, NextRetryAt: "2026-10-28T15:37:00Z", Reason: "Daily edit creation quota exceeded."}
			} else {
				meta.Availability.Audience = "" // Older evidence may omit audience.
			}
			warning := "Google Play refresh paused: Daily edit creation quota exceeded."
			savedMeta := mustJSON(meta)
			if err := dbUpdateRelease(c.db, rel.ID, map[string]any{"release_meta_json": savedMeta, "error": warning}); err != nil {
				t.Fatal(err)
			}
			if err := c.recordBackoff(88, 403, nil, json.RawMessage(dailyEditQuotaResponse)); err != nil {
				t.Fatal(err)
			}
			var retryBefore string
			if err := c.db.QueryRow(`SELECT next_retry_at FROM google_play_backoff WHERE connection_id=88`).Scan(&retryBefore); err != nil {
				t.Fatal(err)
			}
			for i := 0; i < 3; i++ {
				if err := (&App{}).syncPendingMobileReleases(context.Background()); err != nil {
					t.Fatal(err)
				}
				googlePlayCoordinators.Delete(c.db)
				c = googlePlayCoordinatorForContext()
				// Repair must remain terminal after restart and cooldown expiry.
				c.now = func() time.Time { return time.Now().Add(48 * time.Hour) }
			}
			var phase string
			if err := c.db.QueryRow(`SELECT json_extract(state_json,'$.phase') FROM release_workflows WHERE release_id=?`, rel.ID).Scan(&phase); err != nil || phase != "completed" {
				t.Fatalf("saved completion did not close workflow: phase=%q, err=%v", phase, err)
			}
			fresh, err := dbGetRelease(c.db, rel.ID)
			if err != nil || fresh.Status != "live" || fresh.ExternalStatus != "sync_error" || fresh.Error != warning || fresh.ReleaseMetaJSON != savedMeta {
				t.Fatalf("repair rewrote stale-check warning or provider evidence: %+v, %v", fresh, err)
			}
			if len(p.calls) != 0 {
				t.Fatalf("saved completion opened provider requests: %+v", p.calls)
			}
			var retryAfter string
			if err := c.db.QueryRow(`SELECT next_retry_at FROM google_play_backoff WHERE connection_id=88`).Scan(&retryAfter); err != nil || retryAfter != retryBefore {
				t.Fatalf("repair changed quota cooldown: %q, %v", retryAfter, err)
			}
		})
	}
}

func TestGoogleSavedCompletionDoesNotFinishUncertainOrActiveReleases(t *testing.T) {
	_, c, _ := setupQuotaPlatform(t)
	for _, tt := range []struct {
		name, status, external, meta string
	}{
		{"active_rollout", "live", "sync_error", `{"rollout_fraction":0.25,"availability":{"publication":"completed"}}`},
		{"newer_rollout", "live", "inProgress", `{"availability":{"publication":"completed"}}`},
		{"pending_upload", "starting", "sync_error", `{"availability":{"publication":"completed"}}`},
		{"unconfirmed", "live", "sync_error", `{"availability":{"publication":"unconfirmed"}}`},
		{"missing", "live", "sync_error", `{}`},
		{"malformed", "live", "sync_error", `{broken`},
		{"other_channel", "live", "sync_error", `{"availability":{"publication":"completed","audience":"production"}}`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			rel := pollingRelease(t, c, "google_play", tt.status, tt.external)
			if err := dbUpdateRelease(c.db, rel.ID, map[string]any{"release_meta_json": tt.meta}); err != nil {
				t.Fatal(err)
			}
			if err := dbReconcileFinishedMobileWorkflows(c.db, rel.ID); err != nil {
				t.Fatal(err)
			}
			releases, err := dbListPendingMobileReleases(c.db, 100)
			if err != nil {
				t.Fatal(err)
			}
			found := false
			for _, pending := range releases {
				found = found || pending.ID == rel.ID
			}
			if !found {
				t.Fatal("uncertain or active release was excluded from reconciliation")
			}
		})
	}
}
