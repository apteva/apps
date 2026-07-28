package main

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func TestEmbeddedManifestVersionMatchesAppManifest(t *testing.T) {
	raw, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatal(err)
	}
	fileManifest, err := sdk.ParseManifest(raw)
	if err != nil {
		t.Fatal(err)
	}
	if embedded := (&App{}).Manifest(); embedded.Version != fileManifest.Version {
		t.Fatalf("embedded manifest version %q != apteva.yaml version %q", embedded.Version, fileManifest.Version)
	}
}

func TestTwitterInboxDMPermissionFailureIsReportedAndBackedOff(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["get_me"] = &sdk.ExecuteResult{
		Success: true,
		Status:  http.StatusOK,
		Data:    json.RawMessage(`{"data":{"id":"owner-1","username":"owner"}}`),
	}
	pf.executeResponses["get_user_mentions"] = &sdk.ExecuteResult{
		Success: true,
		Status:  http.StatusOK,
		Data:    json.RawMessage(`{"data":[]}`),
	}
	pf.executeResponses["get_dm_events"] = &sdk.ExecuteResult{
		Success: false,
		Status:  http.StatusForbidden,
		Data:    json.RawMessage(`{"title":"Forbidden"}`),
	}
	ctx := newSocialCtx(t, pf)
	result, err := ctx.AppDB().Exec(
		`INSERT INTO social_accounts
		   (project_id, platform, connection_id, display_name, status)
		 VALUES ('test-proj', 'twitter', 42, '@owner', 'active')`,
	)
	if err != nil {
		t.Fatal(err)
	}
	accountID, _ := result.LastInsertId()

	for attempt := 0; attempt < 2; attempt++ {
		report, err := syncTwitterAccount(ctx, "test-proj", accountID)
		if err != nil {
			t.Fatal(err)
		}
		if len(report.Warnings) != 1 || !strings.Contains(report.Warnings[0], "reconnect") {
			t.Fatalf("attempt %d warnings = %#v", attempt+1, report.Warnings)
		}
	}

	dmCalls := 0
	for _, call := range pf.executeCalls {
		if call.Tool == "get_dm_events" {
			dmCalls++
		}
	}
	if dmCalls != 1 {
		t.Fatalf("get_dm_events calls = %d, want one call followed by backoff", dmCalls)
	}
	var lastError string
	if err := ctx.AppDB().QueryRow(
		`SELECT COALESCE(last_error,'') FROM inbox_cursors
		  WHERE social_account_id=? AND kind=?`,
		accountID, twitterDMCursorKind,
	).Scan(&lastError); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(lastError, twitterDMPermissionPrefix) {
		t.Fatalf("DM cursor error = %q", lastError)
	}

	clearTwitterDMSyncError(ctx.AppDB(), accountID)
	pf.executeResponses["get_dm_events"] = &sdk.ExecuteResult{
		Success: true,
		Status:  http.StatusOK,
		Data:    json.RawMessage(`{"data":[]}`),
	}
	report, err := syncTwitterAccount(ctx, "test-proj", accountID)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Warnings) != 0 {
		t.Fatalf("warnings after permission reset = %#v", report.Warnings)
	}
}

func TestTwitterReconnectClearsInboxCapabilityBackoff(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["get_me"] = &sdk.ExecuteResult{
		Success: true,
		Status:  http.StatusOK,
		Data:    json.RawMessage(`{"data":{"id":"owner-1","username":"owner"}}`),
	}
	ctx := newSocialCtx(t, pf)
	accountResult, err := ctx.AppDB().Exec(
		`INSERT INTO social_accounts
		   (project_id, platform, connection_id, display_name, status)
		 VALUES ('test-proj', 'twitter', 42, '@owner', 'needs_reauth')`,
	)
	if err != nil {
		t.Fatal(err)
	}
	accountID, _ := accountResult.LastInsertId()
	recordTwitterDMPermissionFailure(ctx.AppDB(), accountID)
	pendingResult, err := ctx.AppDB().Exec(
		`INSERT INTO pending_accounts
		   (project_id, platform, integration_slug, connection_id, status, expires_at)
		 VALUES ('test-proj', 'twitter', 'twitter-api', 42, 'ready', ?)`,
		pendingExpiry(time.Now().UTC().Add(time.Hour)),
	)
	if err != nil {
		t.Fatal(err)
	}
	pendingID, _ := pendingResult.LastInsertId()

	out, err := (&App{}).toolAccountFinalize(ctx, map[string]any{
		"pending_account_id": pendingID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["reconnected"] != true {
		t.Fatalf("finalize result = %+v", out)
	}
	var cursorCount int
	if err := ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM inbox_cursors WHERE social_account_id=?`,
		accountID,
	).Scan(&cursorCount); err != nil {
		t.Fatal(err)
	}
	if cursorCount != 0 {
		t.Fatalf("inbox cursor count after reconnect = %d", cursorCount)
	}
}

func TestTwitterPostMetricsUsesStandardEndpointFirst(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["get_tweet_analytics"] = &sdk.ExecuteResult{
		Success: true,
		Status:  http.StatusOK,
		Data: json.RawMessage(`{"data":{
			"public_metrics":{"impression_count":18,"like_count":2,"reply_count":1,"retweet_count":1,"quote_count":1,"bookmark_count":3},
			"organic_metrics":{"impression_count":20}
		}}`),
	}
	pf.executeResponses["get_post_analytics"] = &sdk.ExecuteResult{
		Success: false,
		Status:  http.StatusForbidden,
		Data:    json.RawMessage(`{"reason":"client-not-enrolled"}`),
	}
	ctx := newSocialCtx(t, pf)
	out := (&App{}).getTwitterPostMetrics(ctx, targetMetricsOutcome{
		Platform:       "twitter",
		PlatformPostID: "2078177956510212137",
	}, 42)
	if out.Status != "ok" || out.Metrics == nil {
		t.Fatalf("outcome = %+v", out)
	}
	if out.Metrics.Views != 20 || out.Metrics.Likes != 2 || out.Metrics.Comments != 1 || out.Metrics.Shares != 2 || out.Metrics.Saves != 3 {
		t.Fatalf("normalized metrics = %+v", out.Metrics)
	}
	if len(pf.executeCalls) != 1 || pf.executeCalls[0].Tool != "get_tweet_analytics" {
		t.Fatalf("unexpected analytics calls = %+v", pf.executeCalls)
	}
}

func TestTwitterPublishRejectsOversizeBeforeUpstreamCalls(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newSocialCtx(t, pf)
	_, _, err := (&App{}).publishTwitter(ctx, platforms["twitter"], publishJob{
		connID: 42,
		body:   strings.Repeat("a", 281),
	})
	if err == nil || !strings.Contains(err.Error(), "standard limit is 280") {
		t.Fatalf("error = %v", err)
	}
	if len(pf.executeCalls) != 0 {
		t.Fatalf("oversize post made upstream calls: %+v", pf.executeCalls)
	}
}

func TestTwitterPublishAllowsExplicitLongform(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["post_tweet"] = &sdk.ExecuteResult{
		Success: true,
		Status:  http.StatusCreated,
		Data:    json.RawMessage(`{"data":{"id":"long-1"}}`),
	}
	ctx := newSocialCtx(t, pf)
	id, _, err := (&App{}).publishTwitter(ctx, platforms["twitter"], publishJob{
		connID:  42,
		body:    strings.Repeat("a", 281),
		options: map[string]any{"allow_longform": true},
	})
	if err != nil {
		t.Fatal(err)
	}
	if id != "long-1" || len(pf.executeCalls) != 1 || pf.executeCalls[0].Tool != "post_tweet" {
		t.Fatalf("id=%q calls=%+v", id, pf.executeCalls)
	}
}

func TestTwitterWeightedTextLengthUsesTransformedURLLength(t *testing.T) {
	url := "https://example.com/" + strings.Repeat("a", 100)
	if got := twitterWeightedTextLength("look " + url); got != 28 {
		t.Fatalf("weighted length = %d, want 28", got)
	}
}
