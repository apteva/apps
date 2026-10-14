package main

import (
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func retryFixture(t *testing.T, pf *recordingPlatform, platform string) (*sdk.AppCtx, int64, int64) {
	t.Helper()
	ctx := newSocialCtx(t, pf)
	r, err := ctx.AppDB().Exec(`INSERT INTO social_accounts(project_id,platform,connection_id,display_name,status) VALUES('test-proj',?,42,'selected','active')`, platform)
	if err != nil {
		t.Fatal(err)
	}
	account, _ := r.LastInsertId()
	r, err = ctx.AppDB().Exec(`INSERT INTO posts(project_id,body,status) VALUES('test-proj','retry','partial')`)
	if err != nil {
		t.Fatal(err)
	}
	post, _ := r.LastInsertId()
	r, err = ctx.AppDB().Exec(`INSERT INTO post_targets(post_id,social_account_id,status,attempts,options,last_error) VALUES(?,?,'failed',1,'{"privacy_level":"PUBLIC_TO_EVERYONE"}','temporary failure')`, post, account)
	if err != nil {
		t.Fatal(err)
	}
	target, _ := r.LastInsertId()
	return ctx, post, target
}
func TestTargetRetryPreservesPublishedFailedAndPendingSiblings(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["post_tweet"] = &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"data":{"id":"new-selected"}}`)}
	ctx, post, target := retryFixture(t, pf, "twitter")
	var account int64
	ctx.AppDB().QueryRow(`SELECT social_account_id FROM post_targets WHERE id=?`, target).Scan(&account)
	_, err := ctx.AppDB().Exec(`INSERT INTO post_targets(id,post_id,social_account_id,status,platform_post_id,attempts,last_error) VALUES(949,?,?,'published','facebook-existing',1,NULL),(951,?,?,'failed',NULL,2,'sibling failure'),(952,?,?,'pending',NULL,0,NULL)`, post, account, post, account, post, account)
	if err != nil {
		t.Fatal(err)
	}
	before := map[int64]string{}
	for _, id := range []int64{949, 951, 952} {
		var raw string
		ctx.AppDB().QueryRow(`SELECT json_array(status,platform_post_id,attempts,last_error) FROM post_targets WHERE id=?`, id).Scan(&raw)
		before[id] = raw
	}
	out, err := (&App{}).toolPostRetry(ctx, map[string]any{"post_id": post, "target_id": target})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["retried"] != int64(1) || len(pf.executeCalls) != 1 {
		t.Fatalf("selected retry: %+v calls:%+v", out, pf.executeCalls)
	}
	for id, raw := range before {
		var after string
		ctx.AppDB().QueryRow(`SELECT json_array(status,platform_post_id,attempts,last_error) FROM post_targets WHERE id=?`, id).Scan(&after)
		if after != raw {
			t.Fatalf("sibling %d mutated: %s -> %s", id, raw, after)
		}
	}
}
func TestTargetRetryScopesAndUnknownOutcome(t *testing.T) {
	for _, c := range []struct {
		name    string
		args    map[string]any
		failure string
	}{{"missing target", map[string]any{"target_id": 1000}, ""}, {"invalid id", map[string]any{"target_id": -1}, ""}, {"wrong project", map[string]any{"_project_id": "other"}, ""}, {"unknown outcome", nil, "outcome_unknown"}, {"duplicate", nil, "duplicate_content"}} {
		t.Run(c.name, func(t *testing.T) {
			pf := newRecordingPlatform()
			ctx, post, target := retryFixture(t, pf, "twitter")
			if c.name == "wrong project" {
				_, _ = ctx.AppDB().Exec(`UPDATE posts SET project_id='other' WHERE id=?`, post)
			}
			if c.failure != "" {
				_, _ = ctx.AppDB().Exec(`UPDATE post_targets SET failure_code=? WHERE id=?`, c.failure, target)
			}
			args := map[string]any{"post_id": post, "target_id": target}
			for k, v := range c.args {
				args[k] = v
			}
			_, err := (&App{}).toolPostRetry(ctx, args)
			if err != nil {
				t.Fatal(err)
			}
			if len(pf.executeCalls) != 0 {
				t.Fatal("unauthorized/unknown target was published")
			}
			var status string
			ctx.AppDB().QueryRow(`SELECT status FROM post_targets WHERE id=?`, target).Scan(&status)
			if status != "failed" {
				t.Fatalf("target changed: %s", status)
			}
		})
	}
}
func TestTargetRetryReconcilesTikTokBeforeReplacement(t *testing.T) {
	for _, remote := range []string{"PUBLISH_COMPLETE", "PROCESSING_DOWNLOAD", "unexpected"} {
		t.Run(remote, func(t *testing.T) {
			pf := newRecordingPlatform()
			ctx, post, target := retryFixture(t, pf, "tiktok")
			_, _ = ctx.AppDB().Exec(`UPDATE post_targets SET publish_operation_id='existing-operation' WHERE id=?`, target)
			pf.executeResponses["get_publish_status"] = &sdk.ExecuteResult{Success: true, Data: json.RawMessage(fmt.Sprintf(`{"data":{"status":%q,"publicaly_available_post_id":["already-published"]}}`, remote))}
			out, err := (&App{}).toolPostRetry(ctx, map[string]any{"post_id": post, "target_id": target})
			if err != nil {
				t.Fatal(err)
			}
			if out.(map[string]any)["retried"] != 0 || len(pf.executeCalls) != 1 || pf.executeCalls[0].Tool != "get_publish_status" {
				t.Fatalf("created duplicate: %+v %+v", out, pf.executeCalls)
			}
		})
	}
}
func TestTargetRetryTikTokMediaFixPreservesFacebook(t *testing.T) {
	pf := newRecordingPlatform()
	ctx, post, target := retryFixture(t, pf, "tiktok")
	source := fixturePNG(t, 1080, 1924)
	delivery, _, err := normalizeDeliveryImage(source, tikTokPhotoPolicy)
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		_, _ = w.Write(delivery)
	}))
	defer server.Close()
	storageImageFixture(pf, source, 109)
	pf.callAppResponses["storage:files_get"], _ = json.Marshal(map[string]any{"id": 9, "content_type": "image/png", "size_bytes": len(source)})
	pf.callAppResponses["storage:files_get_url"], _ = json.Marshal(map[string]any{"url": server.URL + "/delivery.jpg"})
	_, err = ctx.AppDB().Exec(`UPDATE posts SET media_storage_ids='[9]' WHERE id=?`, post)
	if err != nil {
		t.Fatal(err)
	}
	_, err = ctx.AppDB().Exec(`UPDATE post_targets SET publish_operation_id='old-operation',retryable=0,failure_code='upstream_rejected' WHERE id=?`, target)
	if err != nil {
		t.Fatal(err)
	}
	var account int64
	ctx.AppDB().QueryRow(`SELECT social_account_id FROM post_targets WHERE id=?`, target).Scan(&account)
	_, err = ctx.AppDB().Exec(`INSERT INTO post_targets(id,post_id,social_account_id,status,platform_post_id,attempts) VALUES(949,?,?,'published','facebook-existing',1)`, post, account)
	if err != nil {
		t.Fatal(err)
	}
	pf.executeQueues["get_publish_status"] = []*sdk.ExecuteResult{{Success: true, Data: json.RawMessage(`{"data":{"status":"FAILED","fail_reason":"picture_size_check_failed"}}`)}, {Success: true, Data: json.RawMessage(`{"data":{"status":"PUBLISH_COMPLETE","publicaly_available_post_id":["new-tiktok-post"]}}`)}}
	pf.executeResponses["post_photo"] = &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"data":{"publish_id":"new-operation"}}`)}
	out, err := (&App{}).toolPostRetry(ctx, map[string]any{"post_id": post, "target_id": target})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["retried"] != int64(1) {
		t.Fatalf("%+v", out)
	}
	calls := pf.nonCreatorInfoCalls()
	if len(calls) != 3 || calls[0].Tool != "get_publish_status" || calls[1].Tool != "post_photo" || calls[2].Tool != "get_publish_status" {
		t.Fatalf("reconcile before republish: %+v", calls)
	}
	var status, id, raw string
	var attempts int
	ctx.AppDB().QueryRow(`SELECT status,platform_post_id,attempts FROM post_targets WHERE id=949`).Scan(&status, &id, &attempts)
	if status != "published" || id != "facebook-existing" || attempts != 1 {
		t.Fatal("Facebook changed")
	}
	ctx.AppDB().QueryRow(`SELECT status,platform_post_id,media_delivery_diagnostics FROM post_targets WHERE id=?`, target).Scan(&status, &id, &raw)
	if status != "published" || id != "new-tiktok-post" || !strings.Contains(raw, `"delivery_height":1920`) || !strings.Contains(raw, `"validation":"verified"`) {
		t.Fatalf("recovery diagnostics: %s %s %s", status, id, raw)
	}
	ctx.AppDB().QueryRow(`SELECT operation_id FROM target_delivery_attempts WHERE target_id=?`, target).Scan(&id)
	if id != "old-operation" {
		t.Fatal("previous operation history lost")
	}
	var media string
	ctx.AppDB().QueryRow(`SELECT media_storage_ids FROM posts WHERE id=?`, post).Scan(&media)
	if media != "[9]" {
		t.Fatal("original media association changed")
	}
}
func TestTargetRetryHTTPSelector(t *testing.T) {
	pf := newRecordingPlatform()
	ctx, post, target := retryFixture(t, pf, "twitter")
	_ = ctx
	pf.executeResponses["post_tweet"] = &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"data":{"id":"http-selected"}}`)}
	req := httptest.NewRequest("POST", fmt.Sprintf("/posts/%d/retry?project_id=test-proj", post), strings.NewReader(fmt.Sprintf(`{"target_id":%d}`, target)))
	w := httptest.NewRecorder()
	(&App{}).handlePostsItem(w, req)
	var result map[string]any
	_ = json.Unmarshal(w.Body.Bytes(), &result)
	if w.Code != 200 || result["retried"] != float64(1) || result["target_id"] != float64(target) {
		t.Fatalf("%d %s", w.Code, w.Body.String())
	}
}
