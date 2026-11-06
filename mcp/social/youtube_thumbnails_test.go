package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"sync/atomic"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

const testShortThumbnail = "https://i.ytimg.com/vi/abc123def45/shorts.jpg"

func youtubeThumbnailRead(id, processing, thumbnail string, custom bool) *sdk.ExecuteResult {
	return graphOK(fmt.Sprintf(`{"items":[{"id":%q,"processingDetails":{"processingStatus":%q},
		"contentDetails":{"hasCustomThumbnail":%t},"snippet":{"channelId":"owner","thumbnails":{"shortsAspectRatio":{"url":%q}}}}]}`,
		id, processing, custom, thumbnail))
}

func TestYouTubeThumbnailWaitsForProcessingAndVerifiesSameUpload(t *testing.T) {
	const id = "abc123def45"
	pf := newRecordingPlatform()
	configureTestVideo(t, pf, 7, int64(len(testMP4)))
	// Preserve the normal video upload path and intercept the actual session PUT.
	var uploads atomic.Int32
	uploadServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		uploads.Add(1)
		body, _ := io.ReadAll(r.Body)
		if r.Method != http.MethodPut || string(body) != string(testMP4) {
			t.Error("unexpected video upload request")
		}
		fmt.Fprintf(w, `{"id":%q}`, id)
	}))
	t.Cleanup(uploadServer.Close)
	dest, _ := url.Parse(uploadServer.URL)
	old := youtubeVideoUploadClient
	youtubeVideoUploadClient = &http.Client{Transport: rewriteUploadTransport{http.DefaultTransport, dest}}
	t.Cleanup(func() { youtubeVideoUploadClient = old })
	pf.executeResponses["upload_video_init"] = &sdk.ExecuteResult{Success: true, Status: 200, Headers: map[string]string{"Location": testYouTubeSession}}
	pf.executeResponses["set_thumbnail"] = graphOK(`{"items":[{"shortsAspectRatio":{"url":"` + testShortThumbnail + `"}}]}`)
	// The same Storage stub serves distinct video and image records.
	platform := &thumbnailStoragePlatform{recordingPlatform: pf, imageURL: youtubeTestThumbnailImage(t)}
	ctx := newSocialCtx(t, platform)
	r, err := ctx.AppDB().Exec(`INSERT INTO social_accounts(project_id,platform,connection_id,external_account_id,display_name,status)
		VALUES('test-proj','youtube',42,'owner','YT','active')`)
	if err != nil {
		t.Fatal(err)
	}
	account, _ := r.LastInsertId()
	out, err := (&App{}).toolPostCreate(ctx, map[string]any{
		"mode": "publish", "body": "Short", "media_storage_ids": []any{int64(7)},
		"targets": []any{map[string]any{"social_account_id": account, "thumbnail_storage_id": int64(4903), "thumbnail_project_id": "cover-project"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	post := out.(map[string]any)["post_id"].(int64)
	target := assertYouTubeThumbnailState(t, ctx, post, id, "pending", 1)
	assertYouTubeThumbnailCalls(t, pf, id, "upload_video_init")

	pf.executeQueues["get_video"] = []*sdk.ExecuteResult{
		youtubeThumbnailRead(id, "processing", "", false),
		youtubeThumbnailRead(id, "succeeded", "", false),
		youtubeThumbnailRead(id, "succeeded", "https://i.ytimg.com/vi/abc123def45/old.jpg", true),
		youtubeThumbnailRead(id, "succeeded", testShortThumbnail, true),
	}
	runYouTubeThumbnailTick(t, ctx, post)
	assertYouTubeThumbnailState(t, ctx, post, id, "pending", 1)
	assertYouTubeThumbnailCalls(t, pf, id, "upload_video_init", "get_video")

	runYouTubeThumbnailTick(t, ctx, post)
	assertYouTubeThumbnailState(t, ctx, post, id, "verifying", 1)
	runYouTubeThumbnailTick(t, ctx, post)
	assertYouTubeThumbnailState(t, ctx, post, id, "verifying", 1)

	// A fresh App instance can verify the persisted receipt without another set.
	runYouTubeThumbnailTick(t, ctx, post)
	if got := assertYouTubeThumbnailState(t, ctx, post, id, "verified", 1); got != target {
		t.Fatalf("changed target %d to %d", target, got)
	}
	runYouTubeThumbnailTick(t, ctx, post)
	assertYouTubeThumbnailCalls(t, pf, id, "upload_video_init", "get_video", "get_video", "set_thumbnail", "get_video", "get_video")
	if uploads.Load() != 1 {
		t.Fatalf("expected a single video upload, got %d", uploads.Load())
	}
	if platform.coverProject != "cover-project" {
		t.Fatalf("thumbnail lost Storage project scope: %q", platform.coverProject)
	}
}

func TestYouTubeThumbnailWorkerRejectsUnverifiedOrChangedDelivery(t *testing.T) {
	const id = "abc123def45"
	for _, tt := range []struct {
		name, videoID, state string
		attempts             int
		response             *sdk.ExecuteResult
		calls                []string
	}{
		{name: "processing failed", videoID: id, state: "pending", response: youtubeThumbnailRead(id, "failed", "", false), calls: []string{"get_video"}},
		{name: "wrong native video", videoID: id, state: "pending", response: youtubeThumbnailRead("other-video", "succeeded", "", false), calls: []string{"get_video"}},
		{name: "local video identity changed", videoID: "other-video", state: "pending"},
		{name: "verification timed out", videoID: id, state: "verifying", attempts: youtubeThumbnailVerificationPolls - 1, response: youtubeThumbnailRead(id, "succeeded", "", false), calls: []string{"get_video"}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			pf := newRecordingPlatform()
			pf.executeResponses["get_video"] = tt.response
			ctx := newSocialCtx(t, pf)
			r, err := ctx.AppDB().Exec(`INSERT INTO social_accounts(project_id,platform,connection_id,external_account_id,display_name,status)
				VALUES('test-proj','youtube',42,'owner','YT','active')`)
			if err != nil {
				t.Fatal(err)
			}
			account, _ := r.LastInsertId()
			r, err = ctx.AppDB().Exec(`INSERT INTO posts(project_id,body,status) VALUES ('test-proj','Short','published')`)
			if err != nil {
				t.Fatal(err)
			}
			post, _ := r.LastInsertId()
			receipt, _ := json.Marshal(map[string]string{"shortsAspectRatio": testShortThumbnail})
			if _, err = ctx.AppDB().Exec(`INSERT INTO post_targets(post_id,social_account_id,status,platform_post_id,attempts,youtube_thumbnail_state,
				youtube_thumbnail_video_id,youtube_thumbnail_file_id,youtube_thumbnail_receipt,youtube_thumbnail_attempts)
				VALUES(?,?,'published',?,1,?,?,4903,?,?)`, post, account, tt.videoID, tt.state, id, string(receipt), tt.attempts); err != nil {
				t.Fatal(err)
			}
			runYouTubeThumbnailTick(t, ctx, post)
			assertYouTubeThumbnailState(t, ctx, post, tt.videoID, "failed", 1)
			assertYouTubeThumbnailCalls(t, pf, id, tt.calls...)
		})
	}
}

type thumbnailStoragePlatform struct {
	*recordingPlatform
	imageURL, coverProject string
}

func (p *thumbnailStoragePlatform) CallAppResult(app, tool string, input map[string]any, out any) error {
	if app == "storage" && toInt64Loose(input["id"]) == 4903 {
		p.coverProject, _ = input["_project_id"].(string)
		switch tool {
		case "files_get":
			return json.Unmarshal([]byte(`{"id":4903,"content_type":"image/png","size_bytes":9}`), out)
		case "files_get_url":
			return json.Unmarshal([]byte(fmt.Sprintf(`{"url":%q}`, p.imageURL)), out)
		}
	}
	return p.recordingPlatform.CallAppResult(app, tool, input, out)
}

func youtubeTestThumbnailImage(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		fmt.Fprint(w, "small-png")
	}))
	t.Cleanup(server.Close)
	return server.URL
}

func runYouTubeThumbnailTick(t *testing.T, ctx *sdk.AppCtx, post int64) {
	t.Helper()
	if _, err := ctx.AppDB().Exec(`UPDATE post_targets SET youtube_thumbnail_due_at=0 WHERE post_id=?`, post); err != nil {
		t.Fatal(err)
	}
	if err := (&App{}).runYouTubeThumbnails(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
}

func assertYouTubeThumbnailState(t *testing.T, ctx *sdk.AppCtx, post int64, video, state string, attempts int) int64 {
	t.Helper()
	rows := (&App{}).loadTargets(ctx, post)
	if len(rows) != 1 || rows[0]["thumbnail_status"] != state || rows[0]["platform_post_id"] != video || rows[0]["attempts"] != attempts || rows[0]["status"] != "published" {
		t.Fatalf("want one %s thumbnail on published video %s, attempts %d: %+v", state, video, attempts, rows)
	}
	return rows[0]["id"].(int64)
}

func assertYouTubeThumbnailCalls(t *testing.T, pf *recordingPlatform, video string, tools ...string) {
	t.Helper()
	if len(pf.executeCalls) != len(tools) {
		t.Fatalf("integration calls = %+v, want %v", pf.executeCalls, tools)
	}
	for i, call := range pf.executeCalls {
		if call.Tool != tools[i] {
			t.Fatalf("integration calls = %+v, want %v", pf.executeCalls, tools)
		}
		switch call.Tool {
		case "get_video":
			if call.Input["id"] != video {
				t.Fatalf("read wrong video: %+v", call)
			}
		case "set_thumbnail":
			if call.Input["videoId"] != video || call.Input["image"] == nil {
				t.Fatalf("set wrong thumbnail: %+v", call)
			}
		}
	}
}
