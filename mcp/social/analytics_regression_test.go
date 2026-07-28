package main

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func TestExactJSONIDPreservesLargeNumbers(t *testing.T) {
	var payload struct {
		IDs []exactJSONID `json:"ids"`
	}
	if err := json.Unmarshal([]byte(`{"ids":[7667276869607116037,"7667276869607116038"]}`), &payload); err != nil {
		t.Fatal(err)
	}
	if got := []string{string(payload.IDs[0]), string(payload.IDs[1])}; got[0] != "7667276869607116037" || got[1] != "7667276869607116038" {
		t.Fatalf("large IDs changed during decode: %v", got)
	}
}

func TestTikTokPostMetricsResolvesPublishOperationBeforeQuery(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["get_publish_status"] = &sdk.ExecuteResult{
		Success: true,
		Status:  http.StatusOK,
		Data:    json.RawMessage(`{"data":{"status":"PUBLISH_COMPLETE","publicaly_available_post_id":[7667276869607116037]}}`),
	}
	pf.executeResponses["query_videos"] = &sdk.ExecuteResult{
		Success: true,
		Status:  http.StatusOK,
		Data: json.RawMessage(`{"data":{"videos":[{
			"id":7667276869607116037,
			"share_url":"https://www.tiktok.com/@creator/video/7667276869607116037",
			"view_count":10,"like_count":2,"comment_count":0,"share_count":1
		}]}}`),
	}
	ctx := newSocialCtx(t, pf)
	accountResult, err := ctx.AppDB().Exec(
		`INSERT INTO social_accounts
		   (project_id, platform, connection_id, display_name, status)
		 VALUES ('test-proj', 'tiktok', 42, 'Creator', 'active')`,
	)
	if err != nil {
		t.Fatal(err)
	}
	accountID, _ := accountResult.LastInsertId()
	postResult, err := ctx.AppDB().Exec(
		`INSERT INTO posts (project_id, body, status) VALUES ('test-proj', 'Video', 'published')`,
	)
	if err != nil {
		t.Fatal(err)
	}
	postID, _ := postResult.LastInsertId()
	targetResult, err := ctx.AppDB().Exec(
		`INSERT INTO post_targets
		   (post_id, social_account_id, status, publish_operation_id)
		 VALUES (?, ?, 'published', 'v_pub_file~operation')`,
		postID, accountID,
	)
	if err != nil {
		t.Fatal(err)
	}
	targetID, _ := targetResult.LastInsertId()

	out := (&App{}).getPostMetrics(ctx, metricsTarget{
		TargetID:           targetID,
		SocialAccountID:    accountID,
		ConnID:             42,
		Platform:           "tiktok",
		PublishOperationID: "v_pub_file~operation",
	})
	if out.Status != "ok" || out.PlatformPostID != "7667276869607116037" || out.Metrics == nil || out.Metrics.Views != 10 {
		t.Fatalf("unexpected TikTok metrics: %+v", out)
	}
	if len(pf.executeCalls) != 2 || pf.executeCalls[0].Tool != "get_publish_status" || pf.executeCalls[1].Tool != "query_videos" {
		t.Fatalf("unexpected TikTok calls: %+v", pf.executeCalls)
	}
	filters := pf.executeCalls[1].Input["filters"].(map[string]any)
	ids := filters["video_ids"].([]string)
	if len(ids) != 1 || ids[0] != "7667276869607116037" {
		t.Fatalf("query_videos received wrong ID: %+v", filters)
	}
	var storedID, operationID string
	if err := ctx.AppDB().QueryRow(
		`SELECT COALESCE(platform_post_id,''), publish_operation_id FROM post_targets WHERE id=?`,
		targetID,
	).Scan(&storedID, &operationID); err != nil {
		t.Fatal(err)
	}
	if storedID != "7667276869607116037" || operationID != "" {
		t.Fatalf("resolved identity not persisted: post=%q operation=%q", storedID, operationID)
	}
}

func TestFacebookVideoMetricsSurviveUnavailableShares(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["facebook_get_video"] = &sdk.ExecuteResult{
		Success: true,
		Status:  http.StatusOK,
		Data: json.RawMessage(`{
			"id":"27830404249960972","post_id":"1032431409544833","views":13,
			"likes":{"summary":{"total_count":2}},
			"comments":{"summary":{"total_count":1}}
		}`),
	}
	pf.executeResponses["facebook_get_post"] = &sdk.ExecuteResult{
		Success: false,
		Status:  http.StatusBadRequest,
		Data:    json.RawMessage(`{"error":{"message":"shares unavailable"}}`),
	}
	ctx := newSocialCtx(t, pf)
	out := (&App{}).getFacebookPostMetrics(ctx, targetMetricsOutcome{
		TargetID:       7,
		Platform:       "facebook",
		PlatformPostID: "27830404249960972",
	}, 42, "123456", `{"access_token":"page-token"}`)
	if out.Status != "ok" || out.Metrics == nil || out.Metrics.Views != 13 || out.Metrics.Likes != 2 || out.Metrics.Comments != 1 {
		t.Fatalf("Facebook core metrics were discarded: %+v", out)
	}
	if len(out.Warnings) != 1 {
		t.Fatalf("expected optional shares warning: %+v", out)
	}
	if len(pf.executeCalls) != 2 || pf.executeCalls[0].Tool != "facebook_get_video" || pf.executeCalls[1].Tool != "facebook_get_post" {
		t.Fatalf("unexpected Facebook calls: %+v", pf.executeCalls)
	}
	if pf.executeCalls[1].Input["postId"] != "123456_1032431409544833" {
		t.Fatalf("feed post ID was not resolved: %+v", pf.executeCalls[1].Input)
	}
}

func TestInstagramMetricsUseSavedAndKeepPartialResults(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["get_media"] = &sdk.ExecuteResult{
		Success: true,
		Status:  http.StatusOK,
		Data:    json.RawMessage(`{"id":"ig1","media_product_type":"REELS","like_count":4,"comments_count":1}`),
	}
	pf.executeQueues["get_media_insights"] = []*sdk.ExecuteResult{
		{Success: true, Status: http.StatusOK, Data: json.RawMessage(`{"data":[{"name":"views","values":[{"value":50}]}]}`)},
		{Success: false, Status: http.StatusBadRequest, Data: json.RawMessage(`{"error":{"message":"reach unavailable"}}`)},
		{Success: true, Status: http.StatusOK, Data: json.RawMessage(`{"data":[{"name":"saved","values":[{"value":3}]}]}`)},
		{Success: false, Status: http.StatusBadRequest, Data: json.RawMessage(`{"error":{"message":"shares unavailable"}}`)},
	}
	ctx := newSocialCtx(t, pf)
	out := (&App{}).getInstagramPostMetrics(ctx, targetMetricsOutcome{
		TargetID:       8,
		Platform:       "instagram",
		PlatformPostID: "ig1",
	}, 42, `{"access_token":"page-token"}`)
	if out.Status != "ok" || out.Metrics == nil || out.Metrics.Views != 50 || out.Metrics.Saves != 3 || out.Metrics.Likes != 4 || out.Metrics.Comments != 1 {
		t.Fatalf("Instagram partial metrics were discarded: %+v", out)
	}
	if len(out.Warnings) != 2 {
		t.Fatalf("expected two optional metric warnings: %+v", out.Warnings)
	}
	var requested []string
	for _, call := range pf.executeCalls {
		if call.Tool == "get_media_insights" {
			requested = append(requested, call.Input["metric"].(string))
		}
	}
	if got := strings.Join(requested, ","); got != "views,reach,saved,shares" {
		t.Fatalf("unexpected Instagram metrics: %s", got)
	}
}

func TestPersistPostMetricsStoresAvailableZero(t *testing.T) {
	ctx := newSocialCtx(t, nil)
	accountResult, err := ctx.AppDB().Exec(
		`INSERT INTO social_accounts
		   (project_id, platform, connection_id, display_name, status)
		 VALUES ('test-proj', 'instagram', 42, 'Creator', 'active')`,
	)
	if err != nil {
		t.Fatal(err)
	}
	accountID, _ := accountResult.LastInsertId()
	postResult, err := ctx.AppDB().Exec(
		`INSERT INTO posts (project_id, body, status) VALUES ('test-proj', 'Post', 'published')`,
	)
	if err != nil {
		t.Fatal(err)
	}
	postID, _ := postResult.LastInsertId()
	targetResult, err := ctx.AppDB().Exec(
		`INSERT INTO post_targets (post_id, social_account_id, status) VALUES (?, ?, 'published')`,
		postID, accountID,
	)
	if err != nil {
		t.Fatal(err)
	}
	targetID, _ := targetResult.LastInsertId()
	err = persistPostMetricOutcome(ctx, "test-proj", 0, postID, targetMetricsOutcome{
		TargetID:        targetID,
		SocialAccountID: accountID,
		Platform:        "instagram",
		Status:          "ok",
		Metrics: &normalizedMetrics{
			Likes:     0,
			Available: []string{"likes"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	var value int64
	if err := ctx.AppDB().QueryRow(
		`SELECT value FROM social_metric_points
		  WHERE project_id='test-proj' AND post_target_id=? AND metric='likes'`,
		targetID,
	).Scan(&value); err != nil {
		t.Fatal(err)
	}
	if value != 0 {
		t.Fatalf("stored zero metric = %d", value)
	}
}
