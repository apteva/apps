package main

import (
	"encoding/json"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func youtubeDisclosureFixture(t *testing.T) (*recordingPlatform, *sdk.AppCtx, int64) {
	t.Helper()
	pf := newRecordingPlatform()
	// No Location header: the upload stops after init, which is all
	// these tests inspect.
	pf.executeResponses["upload_video_init"] = &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{}`)}
	configureTestVideo(t, pf, 1, 24)
	ctx := newSocialCtx(t, pf)
	r, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, display_name, status)
		 VALUES ('test-proj', 'youtube', 42, 'My Channel', 'active')`,
	)
	acctID, _ := r.LastInsertId()
	return pf, ctx, acctID
}

func youtubeInitStatus(t *testing.T, pf *recordingPlatform) map[string]any {
	t.Helper()
	for _, c := range pf.executeCalls {
		if c.Tool == "upload_video_init" {
			return c.Input["status"].(map[string]any)
		}
	}
	t.Fatalf("no upload_video_init call: %+v", pf.executeCalls)
	return nil
}

func TestYouTubeScheduledUploadSendsSyntheticDisclosure(t *testing.T) {
	pf, ctx, acctID := youtubeDisclosureFixture(t)
	app := &App{}
	out, err := app.toolPostCreate(ctx, map[string]any{
		"mode":              "schedule",
		"body":              "Scheduled AI video",
		"schedule_at":       time.Now().UTC().Add(time.Hour).Format(time.RFC3339),
		"targets":           []any{map[string]any{"social_account_id": acctID, "contains_synthetic_media": true}},
		"media_storage_ids": []any{int64(1)},
	})
	if err != nil {
		t.Fatal(err)
	}
	postID := out.(map[string]any)["post_id"].(int64)
	if len(pf.executeCalls) != 0 {
		t.Fatalf("scheduled post published early: %+v", pf.executeCalls)
	}
	var raw string
	ctx.AppDB().QueryRow(`SELECT options FROM post_targets WHERE post_id=?`, postID).Scan(&raw)
	var stored map[string]any
	if err := json.Unmarshal([]byte(raw), &stored); err != nil || stored["contains_synthetic_media"] != true {
		t.Fatalf("disclosure not persisted on target: %q", raw)
	}
	ctx.AppDB().Exec(`UPDATE posts SET schedule_at=? WHERE id=?`, time.Now().UTC().Add(-time.Minute).Format(time.RFC3339), postID)
	app.publishDuePost(ctx, postID)
	if status := youtubeInitStatus(t, pf); status["containsSyntheticMedia"] != true {
		t.Fatalf("scheduled upload dropped disclosure: %+v", status)
	}
}

func TestYouTubeUploadLeavesUnsetDisclosureUnset(t *testing.T) {
	pf, ctx, acctID := youtubeDisclosureFixture(t)
	if _, err := (&App{}).toolPostCreate(ctx, map[string]any{
		"mode":               "publish",
		"body":               "Plain video",
		"social_account_ids": []any{acctID},
		"media_storage_ids":  []any{int64(1)},
	}); err != nil {
		t.Fatal(err)
	}
	if status := youtubeInitStatus(t, pf); status["containsSyntheticMedia"] != nil {
		t.Fatalf("unset disclosure was sent as %v", status["containsSyntheticMedia"])
	}
}

func TestYouTubeMetadataMergeUsesStoredDisclosureWhenReadbackOmitsIt(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newSocialCtx(t, pf)
	// get_video readback that omits containsSyntheticMedia even though
	// YouTube accepted it on upload.
	pf.executeResponses["get_video"] = &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"items":[{"snippet":{"title":"Before","categoryId":"22"},"status":{"privacyStatus":"private","publishAt":"2027-01-01T00:00:00Z"}}]}`)}
	merge := func(eff, requested map[string]any) map[string]any {
		t.Helper()
		input, err := (&App{}).youtubeMergedUpdateInput(ctx, 42, "video", eff, requested)
		if err != nil {
			t.Fatal(err)
		}
		return input["status"].(map[string]any)
	}
	status := merge(map[string]any{"title": "After", "contains_synthetic_media": true}, map[string]any{"title": "After"})
	if status["containsSyntheticMedia"] != true || status["publishAt"] != "2027-01-01T00:00:00Z" {
		t.Fatalf("title edit lost stored disclosure or schedule: %+v", status)
	}
	if status := merge(map[string]any{"title": "After"}, map[string]any{"title": "After"}); status["containsSyntheticMedia"] != nil {
		t.Fatalf("unknown disclosure must stay omitted, not become %v", status["containsSyntheticMedia"])
	}

	// An explicit stored false wins over a readback true.
	pf.executeResponses["get_video"] = &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"items":[{"snippet":{"title":"Before"},"status":{"privacyStatus":"public","containsSyntheticMedia":true}}]}`)}
	status = merge(map[string]any{"contains_synthetic_media": false}, map[string]any{"contains_synthetic_media": false})
	if status["containsSyntheticMedia"] != false {
		t.Fatalf("explicit false not sent: %+v", status)
	}
}

func TestPostEditYouTubeCaptionKeepsSyntheticDisclosure(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["get_video"] = &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"items":[{"snippet":{"title":"Before","description":"Old","categoryId":"22"},"status":{"privacyStatus":"public"}}]}`)}
	pf.executeResponses["update_video"] = &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"id":"video-1"}`)}
	ctx := newSocialCtx(t, pf)
	acct, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, display_name, status)
		 VALUES ('test-proj', 'youtube', 42, 'My Channel', 'active')`,
	)
	acctID, _ := acct.LastInsertId()
	post, _ := ctx.AppDB().Exec(`INSERT INTO posts (project_id, body, status) VALUES ('test-proj', 'Old', 'published')`)
	postID, _ := post.LastInsertId()
	ctx.AppDB().Exec(`INSERT INTO post_targets (post_id, social_account_id, status, platform_post_id, options) VALUES (?, ?, 'published', 'video-1', '{"contains_synthetic_media":true}')`, postID, acctID)

	updateStatus := func() map[string]any {
		t.Helper()
		for i := len(pf.executeCalls) - 1; i >= 0; i-- {
			if pf.executeCalls[i].Tool == "update_video" {
				return pf.executeCalls[i].Input["status"].(map[string]any)
			}
		}
		t.Fatalf("no update_video call: %+v", pf.executeCalls)
		return nil
	}
	if _, err := (&App{}).toolPostEdit(ctx, map[string]any{
		"post_id": postID,
		"targets": []any{map[string]any{"social_account_id": acctID, "title": "New title", "body": "New caption"}},
	}); err != nil {
		t.Fatal(err)
	}
	if status := updateStatus(); status["containsSyntheticMedia"] != true {
		t.Fatalf("caption edit dropped disclosure: %+v", status)
	}
	var raw string
	ctx.AppDB().QueryRow(`SELECT options FROM post_targets WHERE post_id=?`, postID).Scan(&raw)
	var stored map[string]any
	if err := json.Unmarshal([]byte(raw), &stored); err != nil || stored["contains_synthetic_media"] != true {
		t.Fatalf("edit rewrote stored disclosure: %q", raw)
	}

	// Changing only the disclosure is a metadata edit on its own.
	before := len(pf.executeCalls)
	if _, err := (&App{}).toolPostEdit(ctx, map[string]any{
		"post_id": postID,
		"targets": []any{map[string]any{"social_account_id": acctID, "contains_synthetic_media": false}},
	}); err != nil {
		t.Fatal(err)
	}
	if len(pf.executeCalls) == before {
		t.Fatal("disclosure-only edit was skipped")
	}
	if status := updateStatus(); status["containsSyntheticMedia"] != false {
		t.Fatalf("disclosure-only edit not applied: %+v", status)
	}
}

func TestZernioMapsSyntheticDisclosure(t *testing.T) {
	psd := zernioPlatformSpecificData(map[string]any{"title": "x", "contains_synthetic_media": "true"})
	if psd["containsSyntheticMedia"] != true || psd["contains_synthetic_media"] != nil {
		t.Fatalf("platformSpecificData = %+v", psd)
	}
}
