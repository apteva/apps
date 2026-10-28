package main

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func TestYouTubeUploadWithReleaseTimeUploadsPrivatelyWithPublishAt(t *testing.T) {
	pf, ctx, acctID := youtubeDisclosureFixture(t)
	release := time.Now().UTC().Add(6 * time.Hour).Truncate(time.Second)
	if _, err := (&App{}).toolPostCreate(ctx, map[string]any{
		"mode":              "publish",
		"body":              "Tonight's video",
		"targets":           []any{map[string]any{"social_account_id": acctID, "visibility": "public", "release_at": release.Format(time.RFC3339)}},
		"media_storage_ids": []any{int64(1)},
	}); err != nil {
		t.Fatal(err)
	}
	status := youtubeInitStatus(t, pf)
	if status["privacyStatus"] != "private" || status["publishAt"] != release.Format(time.RFC3339) {
		t.Fatalf("release time must upload privately with publishAt: %+v", status)
	}
}

func TestYouTubeUploadRejectsPastReleaseTime(t *testing.T) {
	pf, ctx, acctID := youtubeDisclosureFixture(t)
	out, err := (&App{}).toolPostCreate(ctx, map[string]any{
		"mode":              "publish",
		"body":              "Late video",
		"targets":           []any{map[string]any{"social_account_id": acctID, "release_at": time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)}},
		"media_storage_ids": []any{int64(1)},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range pf.executeCalls {
		if c.Tool == "upload_video_init" {
			t.Fatalf("past release time still uploaded: %+v", c.Input)
		}
	}
	var lastErr string
	ctx.AppDB().QueryRow(`SELECT COALESCE(last_error,'') FROM post_targets WHERE post_id=?`, out.(map[string]any)["post_id"]).Scan(&lastErr)
	if !strings.Contains(lastErr, "release time") {
		t.Fatalf("last_error = %q, want release-time rejection", lastErr)
	}
}

func TestYouTubeReleaseStatePrefersObservationOverRequest(t *testing.T) {
	now := time.Date(2026, 10, 28, 12, 0, 0, 0, time.UTC)
	future, past := "2026-10-28T19:00:00Z", "2026-10-28T10:00:00Z"
	cases := []struct {
		name     string
		options  map[string]any
		metadata any
		state    string
		source   string
	}{
		{"default upload is public", map[string]any{}, nil, "public", "requested"},
		{"requested release", map[string]any{"release_at": future}, nil, "scheduled", "requested"},
		{"requested private", map[string]any{"visibility": "private"}, nil, "private", "requested"},
		{"requested unlisted ignores release", map[string]any{"visibility": "unlisted", "release_at": future}, nil, "unlisted", "requested"},
		{"observed schedule set outside Social", map[string]any{"visibility": "private"}, map[string]any{"visibility": "private", "scheduled_publish_at": future}, "scheduled", "observed"},
		{"observed release wins over stale request", map[string]any{"release_at": future}, map[string]any{"visibility": "public"}, "public", "observed"},
		{"release time passed", map[string]any{}, map[string]any{"visibility": "private", "scheduled_publish_at": past}, "release_due", "observed"},
		{"observation without visibility falls back", map[string]any{"visibility": "unlisted"}, map[string]any{"title": "x"}, "unlisted", "requested"},
	}
	for _, c := range cases {
		got := youtubeReleaseState(c.options, c.metadata, "2026-10-28T11:00:00Z", now)
		if got["state"] != c.state || got["source"] != c.source {
			t.Errorf("%s: got %+v, want state %s source %s", c.name, got, c.state, c.source)
		}
	}
}

func TestYouTubeEditSetsAndClearsReleaseTime(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newSocialCtx(t, pf)
	pf.executeResponses["get_video"] = &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"items":[{"snippet":{"title":"Video"},"status":{"privacyStatus":"public"}}]}`)}
	release := time.Now().UTC().Add(3 * time.Hour).Truncate(time.Second).Format(time.RFC3339)
	input, err := (&App{}).youtubeMergedUpdateInput(ctx, 42, "video", map[string]any{"release_at": release}, map[string]any{"release_at": release})
	if err != nil {
		t.Fatal(err)
	}
	if status := input["status"].(map[string]any); status["privacyStatus"] != "private" || status["publishAt"] != release {
		t.Fatalf("setting release did not schedule privately: %+v", status)
	}

	pf.executeResponses["get_video"] = &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"items":[{"snippet":{"title":"Video"},"status":{"privacyStatus":"private","publishAt":"` + release + `"}}]}`)}
	input, err = (&App{}).youtubeMergedUpdateInput(ctx, 42, "video", map[string]any{"release_at": nil}, map[string]any{"release_at": nil})
	if err != nil {
		t.Fatal(err)
	}
	if status := input["status"].(map[string]any); status["publishAt"] != nil || status["privacyStatus"] != "private" {
		t.Fatalf("clearing release should drop publishAt and keep the video private: %+v", status)
	}

	past := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	if _, err := (&App{}).youtubeMergedUpdateInput(ctx, 42, "video", map[string]any{"release_at": past}, map[string]any{"release_at": past}); err == nil {
		t.Fatal("past release time was accepted")
	}
}

// scheduledHistoryFixture adds a YouTube status.publishAt to selected videos.
type scheduledHistoryFixture struct {
	*historyFixturePlatform
	publishAt map[string]string
}

func (p *scheduledHistoryFixture) ExecuteIntegrationTool(conn int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	res, err := p.historyFixturePlatform.ExecuteIntegrationTool(conn, tool, input)
	if err != nil || tool != "get_video" {
		return res, err
	}
	var raw map[string]any
	json.Unmarshal(res.Data, &raw)
	for _, value := range raw["items"].([]any) {
		item := value.(map[string]any)
		if at, ok := p.publishAt[historyString(item, "id")]; ok {
			item["status"].(map[string]any)["publishAt"] = at
		}
	}
	return historyFixtureResult(raw), nil
}

func TestNativeRefreshShowsYouTubeScheduledReleaseOnPublishedTarget(t *testing.T) {
	release := time.Now().UTC().Add(8 * time.Hour).Truncate(time.Second).Format(time.RFC3339)
	// v-000 is private in the fixture; its release was set on YouTube,
	// not through Social.
	pf := &scheduledHistoryFixture{historyFixturePlatform: newHistoryFixture(3), publishAt: map[string]string{"v-000": release}}
	ctx := newSocialCtx(t, pf)
	account := historyAccountFixture(t, ctx, pf.owner)
	post, _ := ctx.AppDB().Exec(`INSERT INTO posts (project_id, body, status) VALUES ('test-proj', 'Nova video', 'published')`)
	postID, _ := post.LastInsertId()
	target, _ := ctx.AppDB().Exec(`INSERT INTO post_targets (post_id, social_account_id, status, platform_post_id, options) VALUES (?, ?, 'published', 'v-000', '{"visibility":"private"}')`, postID, account)
	targetID, _ := target.LastInsertId()

	before := (&App{}).loadTargets(ctx, postID)[0]["native_release"].(map[string]any)
	if before["state"] != "private" || before["source"] != "requested" {
		t.Fatalf("before refresh: %+v", before)
	}
	out, err := (&App{}).toolPostTargetNativeRefresh(ctx, map[string]any{"post_id": postID, "target_id": targetID})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["status"] != "ok" {
		t.Fatalf("refresh = %+v", out)
	}
	if pf.writes != 0 {
		t.Fatalf("refresh wrote upstream %d times", pf.writes)
	}
	after := (&App{}).loadTargets(ctx, postID)[0]["native_release"].(map[string]any)
	if after["state"] != "scheduled" || after["source"] != "observed" || after["release_at"] != release {
		t.Fatalf("after refresh: %+v", after)
	}
	var status string
	ctx.AppDB().QueryRow(`SELECT status FROM post_targets WHERE id=?`, targetID).Scan(&status)
	if status != "published" {
		t.Fatalf("refresh changed delivery status to %q", status)
	}
}

func TestZernioRejectsYouTubeReleaseTimeOption(t *testing.T) {
	if psd := zernioPlatformSpecificData(map[string]any{"release_at": "2027-01-01T00:00:00Z"}); len(psd) != 0 {
		t.Fatalf("release_at leaked into platformSpecificData: %+v", psd)
	}
}
