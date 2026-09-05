package main

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func TestPinterestPlatformDefinitionIsNativeAndBoardScoped(t *testing.T) {
	def, ok := platforms["pinterest"]
	if !ok {
		t.Fatal("pinterest missing from native platform registry")
	}
	if def.IntegrationSlug != "pinterest" || def.Strategy != "pinterest" || def.ExternalIDField != "board_id" {
		t.Fatalf("unexpected pinterest definition: %+v", def)
	}
	if !def.MediaRequired || def.ListPagesTool != "list_boards" || def.DeleteTool != "delete_pin" {
		t.Fatalf("incomplete pinterest lifecycle: %+v", def)
	}
}

func TestFetchPagesSupportsPinterestItemsAndBookmark(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeQueues["list_boards"] = []*sdk.ExecuteResult{
		{Success: true, Status: http.StatusOK, Data: json.RawMessage(`{"items":[{"id":"b1","name":"First"}],"bookmark":"next"}`)},
		{Success: true, Status: http.StatusOK, Data: json.RawMessage(`{"items":[{"id":"b2","name":"Second"}]}`)},
	}
	ctx := newSocialCtx(t, pf)

	pages, err := (&App{}).fetchPages(ctx, 42, platforms["pinterest"])
	if err != nil {
		t.Fatal(err)
	}
	if len(pages) != 2 || pages[0].ID != "b1" || pages[1].ID != "b2" {
		t.Fatalf("pages = %+v", pages)
	}
	if len(pf.executeCalls) != 2 {
		t.Fatalf("calls = %+v", pf.executeCalls)
	}
	if pf.executeCalls[0].Input["page_size"] != 100 || pf.executeCalls[0].Input["limit"] != nil {
		t.Fatalf("first input = %+v", pf.executeCalls[0].Input)
	}
	if pf.executeCalls[1].Input["bookmark"] != "next" {
		t.Fatalf("bookmark not forwarded: %+v", pf.executeCalls[1].Input)
	}
}

func TestPublishPinterestBuildsNestedImagePin(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["create_pin"] = &sdk.ExecuteResult{
		Success: true,
		Status:  http.StatusCreated,
		Data:    json.RawMessage(`{"id":"987654321"}`),
	}
	ctx := newSocialCtx(t, pf)

	id, pinURL, err := (&App{}).publishPinterest(ctx, platforms["pinterest"], publishJob{
		connID:   42,
		extID:    "board-1",
		body:     "Full Pin description",
		media:    []mediaItem{{URL: "https://agents.example/media/image.jpg", Mime: "image/jpeg"}},
		options:  map[string]any{"title": "Pin title", "link": "https://example.com/story", "alt_text": "A useful diagram", "board_section_id": "section-1"},
		platform: "pinterest",
	})
	if err != nil {
		t.Fatal(err)
	}
	if id != "987654321" || pinURL != "https://www.pinterest.com/pin/987654321/" {
		t.Fatalf("identity = %q %q", id, pinURL)
	}
	if len(pf.executeCalls) != 1 || pf.executeCalls[0].Tool != "create_pin" {
		t.Fatalf("calls = %+v", pf.executeCalls)
	}
	input := pf.executeCalls[0].Input
	if input["board_id"] != "board-1" || input["description"] != "Full Pin description" || input["board_section_id"] != "section-1" {
		t.Fatalf("input = %+v", input)
	}
	media, _ := input["media_source"].(map[string]any)
	if media["source_type"] != "image_url" || media["url"] != "https://agents.example/media/image.jpg" {
		t.Fatalf("media_source = %+v", media)
	}
}

func TestPublishPinterestBuildsCarouselAndRejectsUnsafeVideoUploadHost(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["create_pin"] = &sdk.ExecuteResult{Success: true, Status: http.StatusCreated, Data: json.RawMessage(`{"id":"55"}`)}
	ctx := newSocialCtx(t, pf)
	_, _, err := (&App{}).publishPinterest(ctx, platforms["pinterest"], publishJob{
		connID: 1, extID: "board", body: "Carousel", options: map[string]any{},
		media: []mediaItem{
			{URL: "https://agents.example/1.jpg", Mime: "image/jpeg"},
			{URL: "https://agents.example/2.png", Mime: "image/png"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	media := pf.executeCalls[0].Input["media_source"].(map[string]any)
	if media["source_type"] != "multiple_image_urls" || len(media["items"].([]map[string]any)) != 2 {
		t.Fatalf("carousel = %+v", media)
	}
	if pinterestUploadURLAllowed("https://evil.example/upload") {
		t.Fatal("unsafe upload host accepted")
	}
	if !pinterestUploadURLAllowed("https://pinterest-media-upload.s3-accelerate.amazonaws.com/") {
		t.Fatal("official upload host rejected")
	}
}

func TestImportPinterestPreservesDescriptionTitleAndUpstreamTime(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["list_board_pins"] = &sdk.ExecuteResult{
		Success: true,
		Status:  http.StatusOK,
		Data: json.RawMessage(`{"items":[{
			"id":"pin-1","created_at":"2026-02-03T04:05:06Z","title":"Separate title",
			"description":"Full description","link":"https://example.com","alt_text":"Alt",
			"board_section_id":"section-2","media":{"images":{"600x":{"url":"https://i.pinimg.com/pin.jpg"}}}
		}]}`),
	}
	ctx := newSocialCtx(t, pf)
	res, err := ctx.AppDB().Exec(`INSERT INTO social_accounts
		(project_id, platform, connection_id, external_account_id, display_name, status)
		VALUES ('test-proj','pinterest',42,'board-1','Ideas','active')`)
	if err != nil {
		t.Fatal(err)
	}
	accountID, _ := res.LastInsertId()

	out := (&App{}).importAccountPosts(ctx, "test-proj", accountID, 25)
	if out.Status != "ok" || out.Imported != 1 {
		t.Fatalf("import = %+v", out)
	}
	var body, publishedAt, targetPublishedAt, targetOptions, platformURL string
	if err := ctx.AppDB().QueryRow(`SELECT p.body, COALESCE(p.published_at,''), COALESCE(t.published_at,''), COALESCE(t.options,''), COALESCE(t.platform_url,'')
		FROM posts p JOIN post_targets t ON t.post_id=p.id WHERE t.social_account_id=?`, accountID).
		Scan(&body, &publishedAt, &targetPublishedAt, &targetOptions, &platformURL); err != nil {
		t.Fatal(err)
	}
	if body != "Full description" || publishedAt != "2026-02-03T04:05:06Z" || targetPublishedAt != publishedAt {
		t.Fatalf("imported dates/body = %q %q %q", body, publishedAt, targetPublishedAt)
	}
	if platformURL != "https://www.pinterest.com/pin/pin-1/" || !containsJSONValue(targetOptions, "title", "Separate title") {
		t.Fatalf("url/options = %q %s", platformURL, targetOptions)
	}
}

func TestPinterestMetricsNormalizeClicksSavesAndImpressions(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["get_pin_analytics"] = &sdk.ExecuteResult{
		Success: true, Status: http.StatusOK,
		Data: json.RawMessage(`{"all":{"summary_metrics":{"IMPRESSION":120,"PIN_CLICK":7,"OUTBOUND_CLICK":3,"SAVE":11}}}`),
	}
	ctx := newSocialCtx(t, pf)
	out := (&App{}).getPostMetrics(ctx, metricsTarget{
		TargetID: 1, SocialAccountID: 2, ConnID: 42, Platform: "pinterest", ExtPostID: "pin-1",
	})
	if out.Status != "ok" || out.Metrics == nil || out.Metrics.Views != 120 || out.Metrics.Clicks != 3 || out.Metrics.Saves != 11 {
		t.Fatalf("metrics = %+v", out)
	}
}

func TestPinterestDraftAndDirectScheduleRemainOptionalPaths(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newSocialCtx(t, pf)
	res, err := ctx.AppDB().Exec(`INSERT INTO social_accounts
		(project_id, platform, connection_id, external_account_id, display_name, status)
		VALUES ('test-proj','pinterest',42,'board-1','Ideas','active')`)
	if err != nil {
		t.Fatal(err)
	}
	accountID, _ := res.LastInsertId()
	app := &App{}
	draftRaw, err := app.toolPostCreate(ctx, map[string]any{
		"mode": "draft", "body": "Optional draft", "social_account_ids": []any{accountID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if draftRaw.(map[string]any)["status"] != "draft" {
		t.Fatalf("draft = %+v", draftRaw)
	}
	scheduledRaw, err := app.toolPostCreate(ctx, map[string]any{
		"mode": "schedule", "body": "Direct schedule", "social_account_ids": []any{accountID},
		"media_storage_ids": []any{int64(71)}, "schedule_at": time.Now().UTC().Add(24 * time.Hour).Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	if scheduledRaw.(map[string]any)["status"] != "scheduled" {
		t.Fatalf("schedule = %+v", scheduledRaw)
	}
	if len(pf.executeCalls) != 0 {
		t.Fatalf("draft/schedule published early: %+v", pf.executeCalls)
	}
}

func TestEditPinterestOnlySendsRequestedMetadata(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["update_pin"] = &sdk.ExecuteResult{Success: true, Status: http.StatusOK, Data: json.RawMessage(`{"id":"pin-1"}`)}
	ctx := newSocialCtx(t, pf)
	out := (&App{}).editPinterestPost(ctx, targetEditOutcome{TargetID: 1, PlatformPostID: "pin-1"}, 42,
		map[string]any{"body": "Changed", "title": "New title", "link": "https://old.example"},
		map[string]any{"body": "Changed", "title": "New title"},
	)
	if out.Status != "ok" || len(pf.executeCalls) != 1 {
		t.Fatalf("edit = %+v calls=%+v", out, pf.executeCalls)
	}
	input := pf.executeCalls[0].Input
	if input["pin_id"] != "pin-1" || input["description"] != "Changed" || input["title"] != "New title" || input["link"] != nil {
		t.Fatalf("edit input = %+v", input)
	}
}

func TestZernioPinterestCapabilitiesDoNotClaimInbox(t *testing.T) {
	caps := zernioCapabilities("pinterest")
	if caps["inbox"] != false || caps["comments"] != false || caps["native_drafts"] != true || caps["native_scheduling"] != true {
		t.Fatalf("capabilities = %+v", caps)
	}
}

func containsJSONValue(raw, key, want string) bool {
	var value map[string]any
	return json.Unmarshal([]byte(raw), &value) == nil && value[key] == want
}
