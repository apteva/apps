package main

import (
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

type membershipPlatform struct {
	*recordingPlatform
	connection *sdk.PlatformConnection
}

func (p *membershipPlatform) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	return p.connection, nil
}

const testYouTubeSession = "https://www.googleapis.com/upload/youtube/v3/videos?uploadType=resumable&upload_id=fixture"

func membershipFixture(t *testing.T) (*App, *sdk.AppCtx, *membershipPlatform, int64) {
	t.Helper()
	pf := &membershipPlatform{recordingPlatform: newRecordingPlatform(), connection: &sdk.PlatformConnection{ID: 42, AppSlug: "youtube-api", ProjectID: "test-proj", Status: "active"}}
	pf.executeResponses["get_my_channel"] = &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"items":[{"id":"UCone"}]}`)}
	pf.executeResponses["list_membership_levels"] = &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"items":[{"id":"level-1","snippet":{"creatorChannelId":"UCone","levelDetails":{"displayName":"Supporters"}}}]}`)}
	ctx := newSocialCtx(t, pf)
	res, err := ctx.AppDB().Exec(`INSERT INTO social_accounts(project_id,platform,connection_id,external_account_id,display_name,status) VALUES('test-proj','youtube',42,'UCone','One','active')`)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return &App{}, ctx, pf, id
}
func memberOptions(levels ...string) map[string]any {
	if levels == nil {
		levels = []string{}
	}
	return map[string]any{"audience": map[string]any{"type": "members", "level_ids": levels}}
}
func discovery(t *testing.T, a *App, ctx *sdk.AppCtx, id int64, refresh bool) map[string]any {
	t.Helper()
	out, err := a.toolMembershipLevels(ctx, map[string]any{"social_account_id": id, "refresh": refresh})
	if err != nil {
		t.Fatal(err)
	}
	return out.(map[string]any)
}
func TestMembershipDiscoveryIsOptionalAndCached(t *testing.T) {
	a, ctx, pf, id := membershipFixture(t)
	caps, err := a.toolAudienceCapabilities(ctx, map[string]any{"social_account_id": id})
	if err != nil {
		t.Fatal(err)
	}
	if caps.(map[string]any)["capabilities"].(audienceCapabilities).Publishing != "manual" {
		t.Fatal(caps)
	}
	if out := discovery(t, a, ctx, id, false); out["status"] != "unknown" || len(pf.executeCalls) != 0 {
		t.Fatal(out, pf.executeCalls)
	}
	if out := discovery(t, a, ctx, id, true); out["status"] != "available" || out["stale"] != false {
		t.Fatal(out)
	}
	out := discovery(t, a, ctx, id, false)
	levels := out["levels"].([]membershipLevel)
	if len(levels) != 1 || levels[0].ID != "level-1" || levels[0].Name != "Supporters" || levels[0].Price != nil {
		t.Fatal(out)
	}
	if len(pf.executeCalls) != 2 || len(pf.startOAuthCalls) != 0 {
		t.Fatal("did not reuse the existing connection", pf.executeCalls)
	}
}
func TestMembershipDiscoveryEmptyDeniedAndMalformedRemainDistinct(t *testing.T) {
	for _, fixture := range []struct {
		name   string
		res    *sdk.ExecuteResult
		status string
	}{
		{"empty", &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"items":[]}`)}, "empty"},
		{"denied", &sdk.ExecuteResult{Success: false, Status: 403, Data: json.RawMessage(`{"error":{"message":"forbidden"}}`)}, "approval_required"},
		{"missing scope", &sdk.ExecuteResult{Success: false, Status: 403, Data: json.RawMessage(`{"error":{"message":"insufficientPermissions"}}`)}, "permission_required"},
		{"malformed", &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{}`)}, "error"},
		{"other channel", &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"items":[{"id":"bad","snippet":{"creatorChannelId":"UCother"}}]}`)}, "error"},
	} {
		t.Run(fixture.name, func(t *testing.T) {
			a, ctx, pf, id := membershipFixture(t)
			pf.executeResponses["list_membership_levels"] = fixture.res
			if out := discovery(t, a, ctx, id, true); out["status"] != fixture.status {
				t.Fatal(out)
			}
		})
	}
}
func TestMembershipDiscoveryFailurePreservesStaleTiers(t *testing.T) {
	a, ctx, pf, id := membershipFixture(t)
	discovery(t, a, ctx, id, true)
	pf.executeResponses["list_membership_levels"] = &sdk.ExecuteResult{Status: 403, Data: json.RawMessage(`{"error":"denied"}`)}
	out := discovery(t, a, ctx, id, true)
	if out["status"] != "approval_required" || out["stale"] != true || len(out["levels"].([]membershipLevel)) != 1 {
		t.Fatal(out)
	}
	if err := validateAudienceTarget(ctx, "test-proj", targetSpec{SocialAccountID: id, Options: memberOptions("level-1")}); err == nil {
		t.Fatal("stale tier accepted")
	}
}
func TestMembershipConnectionAndProjectIsolation(t *testing.T) {
	for _, change := range []func(*membershipPlatform){func(p *membershipPlatform) { p.connection.ProjectID = "other" }, func(p *membershipPlatform) { p.connection.AppSlug = "twitter-api" }, func(p *membershipPlatform) { p.connection.Status = "disconnected" }} {
		a, ctx, pf, id := membershipFixture(t)
		change(pf)
		discovery(t, a, ctx, id, true)
		if len(pf.executeCalls) != 0 {
			t.Fatal("queried mismatched connection")
		}
	}
	a, ctx, pf, id := membershipFixture(t)
	out, _ := a.toolMembershipLevels(ctx.WithProject("other"), map[string]any{"social_account_id": id, "_project_id": "test-proj", "refresh": true})
	if out.(map[string]any)["isError"] != true || len(pf.executeCalls) != 0 {
		t.Fatal("cross-project discovery")
	}
	pf.executeResponses["get_my_channel"] = &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"items":[{"id":"UCother"}]}`)}
	if out := discovery(t, a, ctx, id, true); out["status"] != "error" || len(pf.executeCalls) != 1 {
		t.Fatal(out)
	}
}
func TestMembershipTierValidationAndDrafts(t *testing.T) {
	a, ctx, pf, id := membershipFixture(t)
	if _, err := validatePostTargets(ctx, "test-proj", []targetSpec{{SocialAccountID: id, Options: memberOptions("unknown")}}); err == nil {
		t.Fatal("unknown tier accepted")
	}
	discovery(t, a, ctx, id, true)
	if err := validateAudienceTarget(ctx, "test-proj", targetSpec{SocialAccountID: id, Options: memberOptions("level-1")}); err != nil {
		t.Fatal(err)
	}
	if err := validateAudienceTarget(ctx, "test-proj", targetSpec{SocialAccountID: id, Options: memberOptions("other-account-tier")}); err == nil {
		t.Fatal("cross-account tier accepted")
	}
	ctx.AppDB().Exec(`UPDATE social_membership_cache SET successful_at='2000-01-01T00:00:00Z'`)
	if err := validateAudienceTarget(ctx, "test-proj", targetSpec{SocialAccountID: id, Options: memberOptions("level-1")}); err == nil {
		t.Fatal("expired cache accepted")
	}
	before := len(pf.executeCalls)
	out, err := a.toolPostCreate(ctx, map[string]any{"mode": "draft", "body": "Video", "targets": []any{map[string]any{"social_account_id": id, "audience": map[string]any{"type": "members"}}}})
	if err != nil || out.(map[string]any)["status"] != "draft" || len(pf.executeCalls) != before {
		t.Fatal(out, err)
	}
}
func TestMembershipAudienceRejectsInvalidInputAndUnsupportedDestinations(t *testing.T) {
	for _, options := range []map[string]any{
		{"audience": "members"}, {"audience": map[string]any{"type": "unexpected"}}, {"audience": map[string]any{"type": "members", "level_ids": nil}}, {"audience": map[string]any{"type": "standard", "level_ids": []string{"one"}}}, memberOptions("same", "same"), {"audience": map[string]any{"type": "members", "extra": true}},
	} {
		if _, err := parseAudience(options); err == nil {
			t.Fatal("invalid request accepted", options)
		}
	}
	_, ctx, _, id := membershipFixture(t)
	ctx.AppDB().Exec(`UPDATE social_accounts SET platform='twitter' WHERE id=?`, id)
	if err := validateAudienceTarget(ctx, "test-proj", targetSpec{SocialAccountID: id, Options: memberOptions()}); err == nil {
		t.Fatal("unsupported destination accepted")
	}
}
func TestMembershipAdapterIsGeneric(t *testing.T) {
	key := "native:future-channel"
	audienceAdapters[key] = audienceAdapter{Capabilities: audienceCapabilities{Publishing: "native"}, Publish: func(_ *App, _ *sdk.AppCtx, _ platformDef, _ publishJob, r audienceRequest) (string, string, error) {
		if r.Type != "members" {
			t.Fatal(r)
		}
		return "future-id", "https://example.test/member", nil
	}}
	defer delete(audienceAdapters, key)
	ctx := newSocialCtx(t, newRecordingPlatform())
	id, _, err := (&App{}).runStrategy(ctx, platformDef{}, publishJob{platform: "future-channel", connID: 42, options: memberOptions()})
	if err != nil || id != "future-id" {
		t.Fatal(id, err)
	}
}
func configureMembershipUpload(t *testing.T, pf *membershipPlatform) {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		w.Write(testMP4)
	}))
	t.Cleanup(server.Close)
	meta, _ := json.Marshal(map[string]any{"id": 1, "content_type": "video/mp4", "name": "video.mp4", "size_bytes": len(testMP4)})
	loc, _ := json.Marshal(map[string]any{"url": server.URL + "/video"})
	pf.callAppResponses["storage:files_get"] = meta
	pf.callAppResponses["storage:files_get_url"] = loc
	pf.executeResponses["upload_video_init"] = &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{}`), Headers: map[string]string{"location": testYouTubeSession}}
	pf.executeResponses["upload_video_chunk"] = &sdk.ExecuteResult{Success: true, Status: 201, Data: json.RawMessage(`{"id":"video-one"}`)}
}
func publishMemberPost(t *testing.T, a *App, ctx *sdk.AppCtx, id int64) int64 {
	t.Helper()
	out, err := a.toolPostCreate(ctx, map[string]any{"mode": "publish", "body": "Video", "targets": []any{map[string]any{"social_account_id": id, "audience": map[string]any{"type": "members"}}}, "media_storage_ids": []any{int64(1)}})
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	postID, ok := result["post_id"].(int64)
	if !ok {
		t.Fatal(result)
	}
	return postID
}
func countIntegrationCalls(pf *membershipPlatform, name string) int {
	n := 0
	for _, call := range pf.executeCalls {
		if call.Tool == name {
			n++
		}
	}
	return n
}
func TestMembershipPublishStagesUnlistedAndResumesFromRange(t *testing.T) {
	a, ctx, pf, id := membershipFixture(t)
	configureMembershipUpload(t, pf)
	pf.executeQueues["upload_video_chunk"] = []*sdk.ExecuteResult{{Status: 308, Headers: map[string]string{"Range": "bytes=0-2"}, Data: json.RawMessage(`{}`)}}
	postID := publishMemberPost(t, a, ctx, id)
	var parent, status, remote, audience string
	var published any
	ctx.AppDB().QueryRow(`SELECT status,published_at FROM posts WHERE id=?`, postID).Scan(&parent, &published)
	ctx.AppDB().QueryRow(`SELECT status,platform_post_id,audience_status FROM post_targets WHERE post_id=?`, postID).Scan(&status, &remote, &audience)
	if parent != "awaiting_audience_setup" || status != parent || remote != "video-one" || audience != "awaiting_setup" || published != nil {
		t.Fatal(parent, status, remote, audience, published)
	}
	chunks := []executeCall{}
	for _, call := range pf.executeCalls {
		if call.ConnID != 42 {
			t.Fatal(call)
		}
		if call.Tool == "upload_video_init" {
			if call.Input["status"].(map[string]any)["privacyStatus"] != "unlisted" || call.Input["upload_content_length"] != int64(len(testMP4)) || call.Input["snippet"].(map[string]any)["categoryId"] != "22" {
				t.Fatal(call)
			}
		}
		if call.Tool == "upload_video_chunk" {
			chunks = append(chunks, call)
		}
	}
	if len(chunks) != 2 || chunks[0].Input["content_range"] != "bytes 0-23/24" || chunks[1].Input["content_range"] != "bytes 3-23/24" {
		t.Fatal(chunks)
	}
	bytes, _ := base64.StdEncoding.DecodeString(chunks[1].Input["video"].(map[string]any)["base64"].(string))
	if string(bytes) != string(testMP4[3:]) {
		t.Fatal(bytes)
	}
	scheduled := a.publishScheduledPost(ctx, "test-proj", postID)
	if scheduled["outcome"] != "awaiting_audience_setup" || countIntegrationCalls(pf, "upload_video_init") != 1 {
		t.Fatal(scheduled)
	}
	before := len(pf.executeCalls)
	a.toolPostRetry(ctx, map[string]any{"post_id": postID})
	if len(pf.executeCalls) != before {
		t.Fatal("awaiting target uploaded again")
	}
	targets := a.loadTargets(ctx, postID)
	if !strings.Contains(targets[0]["audience_setup_url"].(string), "/video/video-one/edit") {
		t.Fatal(targets)
	}
}
func TestMembershipInterruptedUploadRetriesSameSession(t *testing.T) {
	a, ctx, pf, id := membershipFixture(t)
	configureMembershipUpload(t, pf)
	pf.executeQueues["upload_video_chunk"] = []*sdk.ExecuteResult{{Status: 0, Data: json.RawMessage(`{"error":"timeout"}`)}}
	postID := publishMemberPost(t, a, ctx, id)
	var retryable int
	ctx.AppDB().QueryRow(`SELECT retryable FROM post_targets WHERE post_id=?`, postID).Scan(&retryable)
	if retryable != 1 {
		t.Fatal("session timeout should allow a probe")
	}
	pf.executeResponses["upload_video_status"] = &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"id":"already-completed"}`)}
	a.toolPostRetry(ctx, map[string]any{"post_id": postID})
	if countIntegrationCalls(pf, "upload_video_init") != 1 || countIntegrationCalls(pf, "upload_video_chunk") != 1 || countIntegrationCalls(pf, "upload_video_status") != 1 {
		t.Fatal(pf.executeCalls)
	}
	if targets := a.loadTargets(ctx, postID); targets[0]["platform_post_id"] != "already-completed" || targets[0]["status"] != "awaiting_audience_setup" {
		t.Fatal(targets)
	}
}
func TestMembershipMissingRangeAndExpiredSessionNeverReinitialize(t *testing.T) {
	a, ctx, pf, id := membershipFixture(t)
	configureMembershipUpload(t, pf)
	pf.executeQueues["upload_video_chunk"] = []*sdk.ExecuteResult{{Status: 308, Data: json.RawMessage(`{}`)}}
	postID := publishMemberPost(t, a, ctx, id)
	pf.executeResponses["upload_video_status"] = &sdk.ExecuteResult{Status: 410, Data: json.RawMessage(`{}`)}
	a.toolPostRetry(ctx, map[string]any{"post_id": postID})
	a.toolPostRetry(ctx, map[string]any{"post_id": postID})
	var code string
	var retryable int
	ctx.AppDB().QueryRow(`SELECT failure_code,retryable FROM post_targets WHERE post_id=?`, postID).Scan(&code, &retryable)
	if countIntegrationCalls(pf, "upload_video_init") != 1 || code != "outcome_unknown" || retryable != 0 {
		t.Fatal(code, retryable, pf.executeCalls)
	}
}
func TestMembershipRetryAfterAndRecovery(t *testing.T) {
	a, ctx, pf, id := membershipFixture(t)
	configureMembershipUpload(t, pf)
	pf.executeQueues["upload_video_chunk"] = []*sdk.ExecuteResult{{Status: 503, Headers: map[string]string{"Retry-After": "120"}, Data: json.RawMessage(`{}`)}}
	postID := publishMemberPost(t, a, ctx, id)
	before := len(pf.executeCalls)
	a.toolPostRetry(ctx, map[string]any{"post_id": postID})
	if len(pf.executeCalls) != before {
		t.Fatal("ignored Retry-After")
	}
	ctx.AppDB().Exec(`UPDATE post_targets SET status='publishing',last_attempt_at=datetime('now','-2 hours') WHERE post_id=?`, postID)
	a.recoverDeliveries(ctx)
	var status string
	var retryable int
	ctx.AppDB().QueryRow(`SELECT status,retryable FROM post_targets WHERE post_id=?`, postID).Scan(&status, &retryable)
	if status != "failed" || retryable != 1 {
		t.Fatal("lost resumable recovery", status, retryable)
	}
}
func TestMembershipFingerprintAndSessionOriginPreventReplacement(t *testing.T) {
	for _, column := range []string{"youtube_upload_session", "youtube_upload_fingerprint"} {
		t.Run(column, func(t *testing.T) {
			a, ctx, pf, id := membershipFixture(t)
			configureMembershipUpload(t, pf)
			pf.executeResponses["upload_video_chunk"] = &sdk.ExecuteResult{Status: 503, Data: json.RawMessage(`{}`)}
			postID := publishMemberPost(t, a, ctx, id)
			ctx.AppDB().Exec(`UPDATE post_targets SET `+column+`='changed' WHERE post_id=?`, postID)
			a.toolPostRetry(ctx, map[string]any{"post_id": postID})
			if countIntegrationCalls(pf, "upload_video_init") != 1 || countIntegrationCalls(pf, "upload_video_status") != 0 {
				t.Fatal(pf.executeCalls)
			}
		})
	}
}
func TestMembershipConfirmationRequiresExactTargetRevisionAndAttestation(t *testing.T) {
	a, ctx, pf, id := membershipFixture(t)
	configureMembershipUpload(t, pf)
	postID := publishMemberPost(t, a, ctx, id)
	var targetID int64
	ctx.AppDB().QueryRow(`SELECT id FROM post_targets WHERE post_id=?`, postID).Scan(&targetID)
	send := func(project string, revision int, confirmed bool, agent string) int {
		req := httptest.NewRequest("POST", "/audiences/confirm?project_id="+project, strings.NewReader(fmt.Sprintf(`{"post_id":%d,"target_id":%d,"expected_revision":%d,"confirmed":%t}`, postID, targetID, revision, confirmed)))
		req.Header.Set("X-Apteva-Caller-Agent", agent)
		rec := httptest.NewRecorder()
		a.handleAudienceConfirm(rec, req)
		return rec.Code
	}
	if send("test-proj", 0, true, "") != 409 || send("test-proj", 1, false, "") != 400 || send("test-proj", 1, true, "7") != 403 {
		t.Fatal("invalid confirmation accepted")
	}
	globalCtx = ctx.WithProject("other")
	if send("other", 1, true, "") != 409 {
		t.Fatal("cross-project confirmation accepted")
	}
	globalCtx = ctx
	if send("test-proj", 1, true, "") != 200 || send("test-proj", 1, true, "") != 409 {
		t.Fatal("confirmation did not consume pending setup")
	}
	var status, verification, at string
	ctx.AppDB().QueryRow(`SELECT status,audience_status,audience_confirmed_at FROM post_targets WHERE id=?`, targetID).Scan(&status, &verification, &at)
	if status != "published" || verification != "human_confirmed" || at == "" {
		t.Fatal(status, verification, at)
	}
	for _, tool := range a.MCPTools() {
		if strings.Contains(tool.Name, "confirm") {
			t.Fatal("operator confirmation exposed as an agent tool")
		}
	}
	for _, route := range a.HTTPRoutes() {
		if route.NoAuth && route.Pattern != "/accounts/oauth_done" {
			t.Fatal("unexpected public route", route)
		}
	}
}
func TestYouTubeTitleEditPreservesPrivacyAndSyntheticDisclosure(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newSocialCtx(t, pf)
	pf.executeResponses["get_video"] = &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"items":[{"snippet":{"title":"Before","categoryId":"22"},"status":{"privacyStatus":"private","containsSyntheticMedia":true,"publishAt":"2027-01-01T00:00:00Z"}}]}`)}
	input, err := (&App{}).youtubeMergedUpdateInput(ctx, 42, "video", map[string]any{"title": "After"}, map[string]any{"title": "After"})
	if err != nil {
		t.Fatal(err)
	}
	status := input["status"].(map[string]any)
	if status["privacyStatus"] != "private" || status["containsSyntheticMedia"] != true || status["publishAt"] != "2027-01-01T00:00:00Z" {
		t.Fatal(status)
	}
	input, err = (&App{}).youtubeMergedUpdateInput(ctx, 42, "video", map[string]any{"visibility": "unlisted"}, map[string]any{"visibility": "unlisted"})
	if err != nil {
		t.Fatal(err)
	}
	if status := input["status"].(map[string]any); status["publishAt"] != nil || status["privacyStatus"] != "unlisted" {
		t.Fatal("explicit visibility change retained incompatible schedule", status)
	}
}
func TestMembershipCacheConnectionChangeInvalidatesTiers(t *testing.T) {
	a, ctx, _, id := membershipFixture(t)
	discovery(t, a, ctx, id, true)
	ctx.AppDB().Exec(`UPDATE social_accounts SET connection_id=43 WHERE id=?`, id)
	out := discovery(t, a, ctx, id, false)
	if out["status"] != "unknown" || len(out["levels"].([]membershipLevel)) != 0 || out["stale"] != true {
		t.Fatal(out)
	}
	if err := validateAudienceTarget(ctx, "test-proj", targetSpec{SocialAccountID: id, Options: memberOptions("level-1")}); err == nil {
		t.Fatal("old connection's tier accepted")
	}
}

func TestMembershipMigrationPreservesExistingPosts(t *testing.T) {
	db, err := sql.Open("sqlite", t.TempDir()+"/upgrade.db")
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	entries, err := os.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() == "022_audiences.sql" || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		source, err := os.ReadFile("migrations/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.Exec(string(source)); err != nil {
			t.Fatalf("migration %s: %v", entry.Name(), err)
		}
	}
	if _, err := db.Exec(`INSERT INTO social_accounts(id,project_id,platform,connection_id,display_name) VALUES(1,'old','twitter',9,'Existing');INSERT INTO posts(id,project_id,body,status,published_at) VALUES(1,'old','Keep body','published','2026-10-01');INSERT INTO post_targets(id,post_id,social_account_id,status,options,platform_post_id) VALUES(1,1,1,'published','{"body":"Keep target"}','remote-id');`); err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile("migrations/022_audiences.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(source)); err != nil {
		t.Fatal(err)
	}
	var body, status, options, remote, audience, published string
	if err := db.QueryRow(`SELECT p.body,p.status,t.options,t.platform_post_id,t.audience_status,p.published_at FROM posts p JOIN post_targets t ON t.post_id=p.id`).Scan(&body, &status, &options, &remote, &audience, &published); err != nil {
		t.Fatal(err)
	}
	if body != "Keep body" || status != "published" || options != `{"body":"Keep target"}` || remote != "remote-id" || audience != "not_required" || published != "2026-10-01T00:00:00Z" {
		t.Fatal(body, status, options, remote, audience, published)
	}
}
func TestMembershipUploadChecksTheSelectedChannelBeforeCreatingSession(t *testing.T) {
	a, ctx, pf, id := membershipFixture(t)
	configureMembershipUpload(t, pf)
	pf.executeResponses["get_my_channel"] = &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"items":[{"id":"UCdifferent"}]}`)}
	publishMemberPost(t, a, ctx, id)
	if countIntegrationCalls(pf, "upload_video_init") != 0 {
		t.Fatal("staged a video on the wrong channel")
	}
}
func TestMembershipMixedPostRollupNeverClaimsCompletedMembership(t *testing.T) {
	a, ctx, pf, id := membershipFixture(t)
	configureMembershipUpload(t, pf)
	postID := publishMemberPost(t, a, ctx, id)
	res, err := ctx.AppDB().Exec(`INSERT INTO social_accounts(project_id,platform,connection_id,display_name,status) VALUES('test-proj','twitter',43,'X','active')`)
	if err != nil {
		t.Fatal(err)
	}
	other, _ := res.LastInsertId()
	ctx.AppDB().Exec(`INSERT INTO post_targets(post_id,social_account_id,status,retryable) VALUES(?,?,'failed',1)`, postID, other)
	if status := a.rollupPostStatus(ctx, postID); status != "awaiting_audience_setup" {
		t.Fatal(status)
	}
	pf.executeResponses["upload_init"] = &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"data":{"id":"x-media"}}`)}
	pf.executeResponses["upload_finalize"] = &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"data":{"id":"x-media"}}`)}
	pf.executeResponses["post_tweet"] = &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"data":{"id":"tweet-one"}}`)}
	a.toolPostRetry(ctx, map[string]any{"post_id": postID})
	if countIntegrationCalls(pf, "upload_video_init") != 1 || countIntegrationCalls(pf, "post_tweet") != 1 {
		t.Fatal("retry did not isolate failed destination", pf.executeCalls)
	}
	if status := a.rollupPostStatus(ctx, postID); status != "awaiting_audience_setup" {
		t.Fatal(status)
	}
}

func TestMembershipScheduledTierSelectionRefreshesExpiredValidation(t *testing.T) {
	a, ctx, pf, id := membershipFixture(t)
	configureMembershipUpload(t, pf)
	discovery(t, a, ctx, id, true)
	out, err := a.toolPostCreate(ctx, map[string]any{"mode": "draft", "body": "Later members video", "targets": []any{map[string]any{"social_account_id": id, "audience": map[string]any{"type": "members", "level_ids": []string{"level-1"}}}}, "media_storage_ids": []any{int64(1)}})
	if err != nil {
		t.Fatal(err)
	}
	postID := out.(map[string]any)["id"].(int64)
	ctx.AppDB().Exec(`UPDATE posts SET status='scheduled' WHERE id=?`, postID)
	ctx.AppDB().Exec(`UPDATE post_targets SET status='pending' WHERE post_id=?`, postID)
	ctx.AppDB().Exec(`UPDATE social_membership_cache SET successful_at='2000-01-01T00:00:00Z'`)
	a.publishPostTargets(ctx, postID)
	if status := a.rollupPostStatus(ctx, postID); status != "awaiting_audience_setup" {
		t.Fatal(status, a.loadTargets(ctx, postID))
	}
	if countIntegrationCalls(pf, "list_membership_levels") != 2 {
		t.Fatal("expired selected tier was not revalidated")
	}
}
