package main

import (
	"encoding/json"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

func youtubeVideoResponse(items ...map[string]any) *sdk.ExecuteResult {
	raw, _ := json.Marshal(map[string]any{"items": items})
	return &sdk.ExecuteResult{Success: true, Status: 200, Data: raw}
}

func youtubeVideoItem(id, owner, privacy string) map[string]any {
	return map[string]any{"id": id, "snippet": map[string]any{"channelId": owner, "title": "T " + id, "publishedAt": "2026-10-01T10:00:00Z"}, "status": map[string]any{"privacyStatus": privacy, "uploadStatus": "processed"}}
}

func TestYouTubeAdapterDerivesCanonicalURLFromVerifiedID(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newSocialCtx(t, pf)
	account := historyAccountFixture(t, ctx, "UC-owner")
	b, _ := loadHistoryAccount(ctx, account)
	run := historyRun{Owner: "UC-owner"}
	// Production examples: ordinary API responses carry no permalink field.
	pf.executeResponses["get_video"] = youtubeVideoResponse(youtubeVideoItem("brjYSjMgSJM", "UC-owner", "public"), youtubeVideoItem("mw0FaSddWfs", "UC-owner", "private"), youtubeVideoItem("unlistedAbc", "UC-owner", "unlisted"))
	posts, err := (&App{}).youtubeNativePostsWithParts(ctx, b, &run, []string{"brjYSjMgSJM", "mw0FaSddWfs", "unlistedAbc"}, true)
	if err != nil || len(posts) != 3 {
		t.Fatal(err, posts)
	}
	for _, p := range posts {
		if valueOrEmpty(p.URL) != "https://www.youtube.com/watch?v="+p.ID || valueOrEmpty(p.URLSource) != youtubeDerivedURLSource {
			t.Errorf("%s: url=%v source=%v", p.ID, valueOrEmpty(p.URL), valueOrEmpty(p.URLSource))
		}
	}
	// A URL says nothing about access: visibility is reported separately.
	if valueOrEmpty(posts[1].Visibility) != "private" || valueOrEmpty(posts[2].Visibility) != "unlisted" {
		t.Fatalf("visibility lost: %+v", posts)
	}

	pf.executeResponses["get_video"] = youtubeVideoResponse(youtubeVideoItem("bad id!", "UC-owner", "public"))
	posts, err = (&App{}).youtubeNativePostsWithParts(ctx, b, &run, []string{"bad id!"}, true)
	if err != nil || posts[0].URL != nil {
		t.Fatalf("malformed ID got a URL: %v %+v", err, posts)
	}
	pf.executeResponses["get_video"] = youtubeVideoResponse(youtubeVideoItem("brjYSjMgSJM", "UC-other", "public"))
	if _, err = (&App{}).youtubeNativePostsWithParts(ctx, b, &run, []string{"brjYSjMgSJM"}, true); err == nil {
		t.Fatal("channel mismatch produced a URL")
	}
}

func TestReconcileRepairsMissingYouTubeURLWithoutPublishing(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["get_my_channel"] = &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"items":[{"id":"UC-owner","contentDetails":{"relatedPlaylists":{"uploads":"UU-owner"}}}]}`)}
	pf.executeResponses["get_video"] = youtubeVideoResponse(youtubeVideoItem("brjYSjMgSJM", "UC-owner", "public"))
	ctx := newSocialCtx(t, pf)
	account := historyAccountFixture(t, ctx, "UC-owner")
	post, _ := ctx.AppDB().Exec(`INSERT INTO posts (project_id, body, status) VALUES ('test-proj', 'HGV video', 'published')`)
	postID, _ := post.LastInsertId()
	target, _ := ctx.AppDB().Exec(`INSERT INTO post_targets (post_id, social_account_id, status, platform_post_id, attempts, options) VALUES (?, ?, 'published', 'brjYSjMgSJM', 1, '{"visibility":"public"}')`, postID, account)
	targetID, _ := target.LastInsertId()

	reconcile := func() map[string]any {
		t.Helper()
		args := map[string]any{"post_id": postID, "target_id": targetID, "social_account_id": account, "refresh_native": true}
		preview, err := (&App{}).toolPostPublicationReconcile(ctx, args)
		if err != nil {
			t.Fatal(err)
		}
		p := preview.(map[string]any)
		args["apply"], args["expected_publication_revision"], args["expected_post_revision"] = true, p["expected_publication_revision"], p["expected_post_revision"]
		out, err := (&App{}).toolPostPublicationReconcile(ctx, args)
		if err != nil {
			t.Fatal(err)
		}
		return out.(map[string]any)
	}
	reconcile()
	var url string
	var attempts int
	ctx.AppDB().QueryRow(`SELECT COALESCE(platform_url,''),attempts FROM post_targets WHERE id=?`, targetID).Scan(&url, &attempts)
	if url != "https://www.youtube.com/watch?v=brjYSjMgSJM" || attempts != 1 {
		t.Fatalf("url=%q attempts=%d", url, attempts)
	}
	if again := reconcile(); again["status"] != "unchanged" {
		t.Fatalf("repeated reconciliation = %+v", again)
	}
	ctx.AppDB().QueryRow(`SELECT attempts FROM post_targets WHERE id=?`, targetID).Scan(&attempts)
	for _, c := range pf.executeCalls {
		if c.Tool != "get_my_channel" && c.Tool != "get_video" {
			t.Fatalf("reconciliation called %s", c.Tool)
		}
	}
	if attempts != 1 {
		t.Fatalf("reconciliation changed publishing attempts to %d", attempts)
	}
}

func facebookInspectFixture(t *testing.T) (*recordingPlatform, *sdk.AppCtx, historyAccount) {
	t.Helper()
	pf := newRecordingPlatform()
	ctx := newSocialCtx(t, pf)
	r, err := ctx.AppDB().Exec(`INSERT INTO social_accounts (project_id, platform, connection_id, external_account_id, display_name, status, page_credentials) VALUES ('test-proj', 'facebook', 41, 'page-1', 'Lily', 'active', '{"access_token":"page-token"}')`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := r.LastInsertId()
	b, err := loadHistoryAccount(ctx, id)
	if err != nil {
		t.Fatal(err)
	}
	return pf, ctx, b
}

func managedFacebookTarget(t *testing.T, ctx *sdk.AppCtx, b historyAccount, nativeID, diagnostics string) {
	t.Helper()
	post, _ := ctx.AppDB().Exec(`INSERT INTO posts (project_id, body, status) VALUES ('test-proj', 'Reel', 'published')`)
	postID, _ := post.LastInsertId()
	if _, err := ctx.AppDB().Exec(`INSERT INTO post_targets (post_id, social_account_id, status, platform_post_id, delivery_diagnostics) VALUES (?, ?, 'published', ?, ?)`, postID, b.ID, nativeID, diagnostics); err != nil {
		t.Fatal(err)
	}
}

func graphOK(body string) *sdk.ExecuteResult {
	return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(body)}
}

func graphError(status int, message string) *sdk.ExecuteResult {
	raw, _ := json.Marshal(map[string]any{"error": map[string]any{"message": message}})
	return &sdk.ExecuteResult{Success: false, Status: status, Data: raw}
}

func onlyReads(t *testing.T, pf *recordingPlatform) {
	t.Helper()
	for _, c := range pf.executeCalls {
		if !strings.HasPrefix(c.Tool, "facebook_get_") {
			t.Fatalf("inspection made a non-read call: %s", c.Tool)
		}
	}
}

func TestFacebookInspectionReadsReelWithVideoFields(t *testing.T) {
	pf, ctx, b := facebookInspectFixture(t)
	managedFacebookTarget(t, ctx, b, "1093462", `{"correlation_id":"c-1"}`)
	// The Page-post field set fails on video objects, as in production.
	pf.executeResponses["facebook_get_post"] = graphError(400, "(#100) Tried accessing nonexisting field (message) on node type (Video)")
	pf.executeResponses["facebook_get_video"] = graphOK(`{"id":"1093462","from":{"id":"page-1"},"created_time":"2026-10-27T18:00:00+0000","permalink_url":"/reel/1093462/","published":true}`)
	run := historyRun{}
	posts, err := (&App{}).inspectNativeHistory(ctx, b, &run, "1093462")
	if err != nil {
		t.Fatal(err)
	}
	p := posts[0]
	n := normalizeNativeObservation(b, p)
	if p.Body != nil || valueOrEmpty(n.URL) != "https://www.facebook.com/reel/1093462/" || n.PublishedAt == nil || run.Scope != "facebook_video" {
		t.Fatalf("reel read = %+v url=%v scope=%s", p, valueOrEmpty(n.URL), run.Scope)
	}
	if len(pf.executeCalls) != 1 || pf.executeCalls[0].Tool != "facebook_get_video" || strings.Contains(toString(pf.executeCalls[0].Input["fields"]), "message") {
		t.Fatalf("calls = %+v", pf.executeCalls)
	}
	onlyReads(t, pf)
}

func TestFacebookInspectionPagePostAndPhotoContracts(t *testing.T) {
	pf, ctx, b := facebookInspectFixture(t)
	pf.executeResponses["facebook_get_post"] = graphOK(`{"id":"page-1_42","from":{"id":"page-1"},"message":"Hello","created_time":"2026-10-27T18:00:00+0000","permalink_url":"https://www.facebook.com/page-1/posts/42","is_published":true}`)
	posts, err := (&App{}).inspectNativeHistory(ctx, b, &historyRun{}, "page-1_42")
	if err != nil || valueOrEmpty(posts[0].Body) != "Hello" {
		t.Fatal(err, posts)
	}
	if fields := toString(pf.executeCalls[0].Input["fields"]); !strings.Contains(fields, "message") {
		t.Fatalf("page post fields = %s", fields)
	}

	managedFacebookTarget(t, ctx, b, "777", "")
	pf.executeResponses["facebook_get_post"] = graphOK(`{"id":"777","from":{"id":"page-1"},"created_time":"2026-10-27T18:00:00+0000","link":"https://www.facebook.com/photo.php?fbid=777"}`)
	posts, err = (&App{}).inspectNativeHistory(ctx, b, &historyRun{}, "777")
	if err != nil || posts[0].Body != nil || valueOrEmpty(posts[0].URL) == "" {
		t.Fatal("photo without caption", err, posts)
	}
	if fields := toString(pf.executeCalls[1].Input["fields"]); strings.Contains(fields, "message") || !strings.Contains(fields, "name") {
		t.Fatalf("photo fields = %s", fields)
	}
	onlyReads(t, pf)
}

func TestFacebookInspectionRejectsWrongPageAndKeepsPermissionErrors(t *testing.T) {
	pf, ctx, b := facebookInspectFixture(t)
	managedFacebookTarget(t, ctx, b, "555", `{"correlation_id":"c-2"}`)
	pf.executeResponses["facebook_get_video"] = graphOK(`{"id":"555","from":{"id":"page-2"}}`)
	if _, err := (&App{}).inspectNativeHistory(ctx, b, &historyRun{}, "555"); err == nil {
		t.Fatal("video owned by another Page was accepted")
	}
	pf.executeResponses["facebook_get_video"] = graphOK(`{"id":"555"}`)
	if _, err := (&App{}).inspectNativeHistory(ctx, b, &historyRun{}, "555"); err == nil {
		t.Fatal("video without verified owner was accepted")
	}
	before := len(pf.executeCalls)
	pf.executeResponses["facebook_get_video"] = graphError(403, "(#10) Requires pages_read_engagement permission")
	if _, err := (&App{}).inspectNativeHistory(ctx, b, &historyRun{}, "555"); err == nil || len(pf.executeCalls) != before+1 {
		t.Fatalf("permission error must fail without retry: err=%v calls=%d", err, len(pf.executeCalls)-before)
	}
}

func TestFacebookInspectionDropsOneUnsupportedOptionalField(t *testing.T) {
	pf, ctx, b := facebookInspectFixture(t)
	managedFacebookTarget(t, ctx, b, "1093463", `{"correlation_id":"c-3"}`)
	pf.executeQueues["facebook_get_video"] = []*sdk.ExecuteResult{
		graphError(400, "(#100) Tried accessing nonexisting field (title) on node type (Video)"),
		graphOK(`{"id":"1093463","from":{"id":"page-1"},"description":"Caption","created_time":"2026-10-27T18:00:00+0000"}`),
	}
	run := historyRun{}
	posts, err := (&App{}).inspectNativeHistory(ctx, b, &run, "1093463")
	if err != nil || valueOrEmpty(posts[0].Body) != "Caption" {
		t.Fatal(err, posts)
	}
	if len(pf.executeCalls) != 2 || strings.Contains(toString(pf.executeCalls[1].Input["fields"]), "title") {
		t.Fatalf("fallback calls = %+v", pf.executeCalls)
	}
	if !strings.Contains(strings.Join(run.Limitations, " "), "title") {
		t.Fatalf("limitations = %v", run.Limitations)
	}
	// A required field is never dropped.
	pf.executeQueues["facebook_get_video"] = []*sdk.ExecuteResult{graphError(400, "(#100) Tried accessing nonexisting field (from) on node type (Video)")}
	before := len(pf.executeCalls)
	if _, err := (&App{}).inspectNativeHistory(ctx, b, &historyRun{}, "1093463"); err == nil || len(pf.executeCalls) != before+1 {
		t.Fatal("ownership field was dropped to force success")
	}
	onlyReads(t, pf)
}
