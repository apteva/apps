package main

// social v0.1 tests cover:
//
//   - account_add: creates pending row, calls StartOAuth, returns url
//   - account_add: rolls back pending row when StartOAuth fails
//   - account_add: rejects unsupported platforms
//   - account_list_pending_pages: needs OAuth-completed row
//   - account_list_pending_pages: returns pages from list_pages tool
//   - account_list_pending_pages: requires_picker=false for Twitter
//   - account_finalize: writes social_accounts row and emits event
//   - account_finalize: refuses without page_id when picker required
//   - account_disconnect: removes social_accounts + DisconnectConnection
//                          when last sibling
//   - account_disconnect: keeps connection alive when siblings exist
//   - post_create: writes posts + post_targets, fans out, calls
//                   post_tweet, marks status=published
//   - post_create: marks failed targets as failed and rolls up to partial
//   - post_retry: resets failed → pending and re-publishes

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// --- stub PlatformClient -------------------------------------------

type recordingPlatform struct {
	mu                sync.Mutex
	executeCalls      []executeCall
	callAppCalls      []callAppCall
	startOAuthCalls   []sdk.OAuthStartRequest
	disconnectCalls   []int64
	nextStartOAuth    *sdk.OAuthStartResult
	nextStartErr      error
	executeResponses  map[string]*sdk.ExecuteResult // keyed by tool name
	callAppResponses  map[string]json.RawMessage    // keyed by "app:tool"
	executeErr        error
	identity          *sdk.InstallIdentity
}

type executeCall struct {
	ConnID int64
	Tool   string
	Input  map[string]any
}

func newRecordingPlatform() *recordingPlatform {
	return &recordingPlatform{
		identity: &sdk.InstallIdentity{
			AppName:   "social",
			InstallID: 99,
			ProjectID: "test-proj",
		},
		executeResponses: map[string]*sdk.ExecuteResult{},
		callAppResponses: map[string]json.RawMessage{},
	}
}

func (p *recordingPlatform) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	return &sdk.PlatformConnection{ID: id, AppSlug: "twitter-api", ProjectID: "test-proj"}, nil
}
func (p *recordingPlatform) ListConnections(filter sdk.ConnectionFilter) ([]sdk.PlatformConnection, error) {
	return nil, nil
}
func (p *recordingPlatform) GetInstance(id int64) (*sdk.PlatformInstance, error) {
	return nil, errors.New("not implemented")
}
func (p *recordingPlatform) SendEvent(int64, string) error { return nil }
func (p *recordingPlatform) SendToChannel(string, string, string) error {
	return nil
}
func (p *recordingPlatform) WhoAmI() (*sdk.InstallIdentity, error) { return p.identity, nil }

func (p *recordingPlatform) ExecuteIntegrationTool(connID int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	p.mu.Lock()
	p.executeCalls = append(p.executeCalls, executeCall{ConnID: connID, Tool: tool, Input: input})
	p.mu.Unlock()
	if p.executeErr != nil {
		return nil, p.executeErr
	}
	if r, ok := p.executeResponses[tool]; ok {
		return r, nil
	}
	return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{}`)}, nil
}

type callAppCall struct {
	AppName string
	Tool    string
	Input   map[string]any
}

func (p *recordingPlatform) CallApp(appName, tool string, input map[string]any) (json.RawMessage, error) {
	p.mu.Lock()
	p.callAppCalls = append(p.callAppCalls, callAppCall{AppName: appName, Tool: tool, Input: input})
	p.mu.Unlock()
	if r, ok := p.callAppResponses[appName+":"+tool]; ok {
		return r, nil
	}
	return json.RawMessage(`{}`), nil
}

func (p *recordingPlatform) StartOAuth(req sdk.OAuthStartRequest) (*sdk.OAuthStartResult, error) {
	p.mu.Lock()
	p.startOAuthCalls = append(p.startOAuthCalls, req)
	p.mu.Unlock()
	if p.nextStartErr != nil {
		return nil, p.nextStartErr
	}
	if p.nextStartOAuth != nil {
		return p.nextStartOAuth, nil
	}
	return &sdk.OAuthStartResult{
		ConnectionID: 7,
		AuthorizeURL: "https://example.com/authorize?state=abc",
		ExpiresAt:    "2026-04-30T15:00:00Z",
	}, nil
}

func (p *recordingPlatform) DisconnectConnection(connID int64) error {
	p.mu.Lock()
	p.disconnectCalls = append(p.disconnectCalls, connID)
	p.mu.Unlock()
	return nil
}

func (p *recordingPlatform) ListOwnedConnections() ([]sdk.PlatformConnection, error) {
	return nil, nil
}

// --- helpers -------------------------------------------------------

func newSocialCtx(t *testing.T, pf sdk.PlatformClient) *sdk.AppCtx {
	t.Helper()
	rec := tk.NewEmitRecorder()
	opts := []tk.Option{
		tk.WithProjectID("test-proj"),
		tk.WithEmitter(rec),
	}
	if pf != nil {
		opts = append(opts, tk.WithPlatform(pf))
	}
	ctx := tk.NewAppCtx(t, "apteva.yaml", opts...)
	globalCtx = ctx
	return ctx
}

// --- account_add ---------------------------------------------------

func TestAccountAdd_HappyPath(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newSocialCtx(t, pf)
	app := &App{}

	out, err := app.toolAccountAdd(ctx, map[string]any{"platform": "twitter"})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(map[string]any)
	if res["authorize_url"] != "https://example.com/authorize?state=abc" {
		t.Errorf("authorize_url not surfaced: %+v", res)
	}
	if res["pending_account_id"].(int64) <= 0 {
		t.Errorf("pending_account_id = %v", res["pending_account_id"])
	}
	// StartOAuth was called with twitter-api slug.
	if len(pf.startOAuthCalls) != 1 {
		t.Fatalf("expected 1 StartOAuth call, got %d", len(pf.startOAuthCalls))
	}
	if pf.startOAuthCalls[0].IntegrationSlug != "twitter-api" {
		t.Errorf("wrong slug: %q", pf.startOAuthCalls[0].IntegrationSlug)
	}
	// pending_accounts row exists.
	var status string
	ctx.AppDB().QueryRow(`SELECT status FROM pending_accounts WHERE id=?`, res["pending_account_id"]).Scan(&status)
	if status != "pending_oauth" {
		t.Errorf("status = %q", status)
	}
}

func TestAccountAdd_UnsupportedPlatform(t *testing.T) {
	ctx := newSocialCtx(t, newRecordingPlatform())
	app := &App{}
	out, _ := app.toolAccountAdd(ctx, map[string]any{"platform": "myspace"})
	res := out.(map[string]any)
	if res["isError"] != true {
		t.Errorf("expected isError=true, got %+v", res)
	}
}

func TestAccountAdd_RollsBackPendingOnStartOAuthError(t *testing.T) {
	pf := newRecordingPlatform()
	pf.nextStartErr = errors.New("auth server down")
	ctx := newSocialCtx(t, pf)
	app := &App{}

	out, _ := app.toolAccountAdd(ctx, map[string]any{"platform": "twitter"})
	res := out.(map[string]any)
	if res["isError"] != true {
		t.Errorf("expected isError=true on StartOAuth failure")
	}
	// No leftover pending row.
	var n int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM pending_accounts`).Scan(&n)
	if n != 0 {
		t.Errorf("expected pending row to be rolled back, got %d row(s)", n)
	}
}

// --- account_list_pending_pages ------------------------------------

func TestListPendingPages_RequiresOAuthComplete(t *testing.T) {
	ctx := newSocialCtx(t, newRecordingPlatform())
	res, _ := ctx.AppDB().Exec(
		`INSERT INTO pending_accounts (project_id, platform, integration_slug, status, expires_at)
		 VALUES ('test-proj', 'facebook', 'facebook-graph', 'pending_oauth', datetime('now','+10 minutes'))`,
	)
	id, _ := res.LastInsertId()
	app := &App{}
	out, _ := app.toolAccountListPendingPages(ctx, map[string]any{"pending_account_id": id})
	r := out.(map[string]any)
	if r["isError"] != true {
		t.Errorf("expected isError=true when OAuth not complete, got %+v", r)
	}
}

func TestListPendingPages_TwitterNoPicker(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newSocialCtx(t, pf)
	res, _ := ctx.AppDB().Exec(
		`INSERT INTO pending_accounts (project_id, platform, integration_slug, connection_id, status, expires_at)
		 VALUES ('test-proj', 'twitter', 'twitter-api', 42, 'ready', datetime('now','+10 minutes'))`,
	)
	id, _ := res.LastInsertId()
	app := &App{}
	out, _ := app.toolAccountListPendingPages(ctx, map[string]any{"pending_account_id": id})
	r := out.(map[string]any)
	if r["requires_picker"] != false {
		t.Errorf("Twitter shouldn't require picker: %+v", r)
	}
}

func TestListPendingPages_FacebookReturnsPages(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["list_pages"] = &sdk.ExecuteResult{
		Success: true,
		Data: json.RawMessage(`{"data":[
			{"id":"100","name":"My Page","picture":{"data":{"url":"https://cdn/x.jpg"}}},
			{"id":"200","name":"Side Project"}
		]}`),
	}
	ctx := newSocialCtx(t, pf)
	res, _ := ctx.AppDB().Exec(
		`INSERT INTO pending_accounts (project_id, platform, integration_slug, connection_id, status, expires_at)
		 VALUES ('test-proj', 'facebook', 'facebook-graph', 42, 'ready', datetime('now','+10 minutes'))`,
	)
	id, _ := res.LastInsertId()
	app := &App{}
	out, err := app.toolAccountListPendingPages(ctx, map[string]any{"pending_account_id": id})
	if err != nil {
		t.Fatal(err)
	}
	r := out.(map[string]any)
	if r["requires_picker"] != true {
		t.Errorf("facebook should require picker")
	}
	pages := r["pages"].([]pageEntry)
	if len(pages) != 2 || pages[0].ID != "100" || pages[0].Name != "My Page" {
		t.Errorf("pages malformed: %+v", pages)
	}
	if pages[0].Avatar != "https://cdn/x.jpg" {
		t.Errorf("nested avatar path didn't resolve: %q", pages[0].Avatar)
	}
}

// --- account_finalize ---------------------------------------------

func TestAccountFinalize_FacebookRequiresPageID(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["list_pages"] = &sdk.ExecuteResult{
		Success: true,
		Data: json.RawMessage(`{"data":[{"id":"100","name":"My Page"}]}`),
	}
	ctx := newSocialCtx(t, pf)
	res, _ := ctx.AppDB().Exec(
		`INSERT INTO pending_accounts (project_id, platform, integration_slug, connection_id, status, expires_at)
		 VALUES ('test-proj', 'facebook', 'facebook-graph', 42, 'ready', datetime('now','+10 minutes'))`,
	)
	id, _ := res.LastInsertId()
	app := &App{}
	out, _ := app.toolAccountFinalize(ctx, map[string]any{"pending_account_id": id})
	r := out.(map[string]any)
	if r["isError"] != true || !strings.Contains(r["content"].([]map[string]any)[0]["text"].(string), "page_id") {
		t.Errorf("expected page_id-required error, got %+v", r)
	}
}

func TestAccountFinalize_FacebookHappyPath(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["list_pages"] = &sdk.ExecuteResult{
		Success: true,
		Data: json.RawMessage(`{"data":[
			{"id":"100","name":"My Restaurant","picture":{"data":{"url":"https://cdn/r.jpg"}}}
		]}`),
	}
	ctx := newSocialCtx(t, pf)
	res, _ := ctx.AppDB().Exec(
		`INSERT INTO pending_accounts (project_id, platform, integration_slug, connection_id, status, expires_at)
		 VALUES ('test-proj', 'facebook', 'facebook-graph', 42, 'ready', datetime('now','+10 minutes'))`,
	)
	pendingID, _ := res.LastInsertId()
	app := &App{}
	out, err := app.toolAccountFinalize(ctx, map[string]any{
		"pending_account_id": pendingID,
		"page_id":            "100",
	})
	if err != nil {
		t.Fatal(err)
	}
	r := out.(map[string]any)
	if r["display_name"] != "My Restaurant" {
		t.Errorf("display_name wrong: %+v", r)
	}
	if r["avatar_url"] != "https://cdn/r.jpg" {
		t.Errorf("avatar_url wrong: %+v", r)
	}
	if r["external_account_id"] != "100" {
		t.Errorf("external_account_id wrong: %+v", r)
	}
	// Row exists in social_accounts.
	var n int
	ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM social_accounts WHERE platform='facebook' AND external_account_id='100'`,
	).Scan(&n)
	if n != 1 {
		t.Errorf("social_accounts row missing: count=%d", n)
	}
}

// --- account_disconnect -------------------------------------------

func TestAccountDisconnect_LastSiblingDisconnectsConnection(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newSocialCtx(t, pf)
	res, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, display_name, status)
		 VALUES ('test-proj', 'twitter', 42, 'me', 'active')`,
	)
	id, _ := res.LastInsertId()
	app := &App{}
	if _, err := app.toolAccountDisconnect(ctx, map[string]any{"id": id}); err != nil {
		t.Fatal(err)
	}
	if len(pf.disconnectCalls) != 1 || pf.disconnectCalls[0] != 42 {
		t.Errorf("expected DisconnectConnection(42), got %+v", pf.disconnectCalls)
	}
}

func TestAccountDisconnect_KeepsConnectionWhenSiblingsExist(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newSocialCtx(t, pf)
	r1, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, display_name, status)
		 VALUES ('test-proj', 'facebook', 42, 'page A', 'active')`,
	)
	id1, _ := r1.LastInsertId()
	_, _ = ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, display_name, status)
		 VALUES ('test-proj', 'facebook', 42, 'page B', 'active')`,
	)
	app := &App{}
	if _, err := app.toolAccountDisconnect(ctx, map[string]any{"id": id1}); err != nil {
		t.Fatal(err)
	}
	if len(pf.disconnectCalls) != 0 {
		t.Errorf("connection should not be disconnected when siblings exist; got %+v", pf.disconnectCalls)
	}
}

// --- post_create + publish ---------------------------------------

func TestPostCreate_FansOutAndPublishes(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["post_tweet"] = &sdk.ExecuteResult{
		Success: true,
		Data:    json.RawMessage(`{"data":{"id":"123","text":"hello"}}`),
	}
	ctx := newSocialCtx(t, pf)
	r, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, display_name, status)
		 VALUES ('test-proj', 'twitter', 42, '@me', 'active')`,
	)
	acctID, _ := r.LastInsertId()

	app := &App{}
	out, err := app.toolPostCreate(ctx, map[string]any{
		"body":               "hello world",
		"social_account_ids": []any{acctID},
	})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(map[string]any)
	postID := res["post_id"].(int64)

	// post_tweet was called with text=body (Twitter's BodyField is "text").
	var found bool
	for _, c := range pf.executeCalls {
		if c.Tool == "post_tweet" && c.Input["text"] == "hello world" {
			found = true
		}
	}
	if !found {
		t.Errorf("post_tweet not called as expected: %+v", pf.executeCalls)
	}

	// post_targets row marked published.
	var status, ppid, purl string
	ctx.AppDB().QueryRow(
		`SELECT status, COALESCE(platform_post_id,''), COALESCE(platform_url,'') FROM post_targets WHERE post_id=?`,
		postID,
	).Scan(&status, &ppid, &purl)
	if status != "published" {
		t.Errorf("target status = %q, want published", status)
	}
	if ppid != "123" {
		t.Errorf("platform_post_id = %q", ppid)
	}
	if !strings.Contains(purl, "/status/123") {
		t.Errorf("platform_url = %q", purl)
	}

	// post status rolled up to 'published'.
	var postStatus string
	ctx.AppDB().QueryRow(`SELECT status FROM posts WHERE id=?`, postID).Scan(&postStatus)
	if postStatus != "published" {
		t.Errorf("post status = %q", postStatus)
	}
}

func TestPostCreate_FacebookUsesMessageAndPageId(t *testing.T) {
	// Facebook's post_to_page expects {pageId, message}. Our adapter
	// must remap from the social_accounts row's external_account_id
	// and the post body — not the Twitter-shaped {text, page_id}.
	pf := newRecordingPlatform()
	pf.executeResponses["post_to_page"] = &sdk.ExecuteResult{
		Success: true,
		Data:    json.RawMessage(`{"id":"100_500"}`),
	}
	ctx := newSocialCtx(t, pf)
	r, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, external_account_id, display_name, status)
		 VALUES ('test-proj', 'facebook', 42, '100', 'My Page', 'active')`,
	)
	acctID, _ := r.LastInsertId()

	app := &App{}
	if _, err := app.toolPostCreate(ctx, map[string]any{
		"body":               "hello fb",
		"social_account_ids": []any{acctID},
	}); err != nil {
		t.Fatal(err)
	}
	if len(pf.executeCalls) != 1 {
		t.Fatalf("expected 1 execute call, got %d", len(pf.executeCalls))
	}
	got := pf.executeCalls[0]
	if got.Tool != "post_to_page" {
		t.Errorf("tool = %q, want post_to_page", got.Tool)
	}
	if got.Input["message"] != "hello fb" {
		t.Errorf("message field not set: %+v", got.Input)
	}
	if got.Input["pageId"] != "100" {
		t.Errorf("pageId field not set: %+v", got.Input)
	}
	if _, hasText := got.Input["text"]; hasText {
		t.Errorf("'text' should NOT be sent to facebook (twitter-shaped key): %+v", got.Input)
	}
}

func TestPostCreate_FailedTargetMarksPartial(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["post_tweet"] = &sdk.ExecuteResult{
		Success: true,
		Data:    json.RawMessage(`{"data":{"id":"123"}}`),
	}
	pf.executeResponses["post_to_page"] = &sdk.ExecuteResult{
		Success: false,
		Status:  500,
		Data:    json.RawMessage(`"page suspended"`),
	}
	ctx := newSocialCtx(t, pf)
	r1, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, display_name, status)
		 VALUES ('test-proj', 'twitter', 42, '@me', 'active')`,
	)
	tid, _ := r1.LastInsertId()
	r2, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, external_account_id, display_name, status)
		 VALUES ('test-proj', 'facebook', 42, '100', 'page', 'active')`,
	)
	fid, _ := r2.LastInsertId()

	app := &App{}
	out, _ := app.toolPostCreate(ctx, map[string]any{
		"body":               "hello",
		"social_account_ids": []any{tid, fid},
	})
	postID := out.(map[string]any)["post_id"].(int64)

	var s string
	ctx.AppDB().QueryRow(`SELECT status FROM posts WHERE id=?`, postID).Scan(&s)
	if s != "partial" {
		t.Errorf("expected partial, got %q", s)
	}
}

func TestPostRetry_RetriesFailedTargets(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["post_tweet"] = &sdk.ExecuteResult{
		Success: true,
		Data:    json.RawMessage(`{"data":{"id":"999"}}`),
	}
	ctx := newSocialCtx(t, pf)
	r, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, display_name, status)
		 VALUES ('test-proj', 'twitter', 42, '@me', 'active')`,
	)
	acctID, _ := r.LastInsertId()
	pr, _ := ctx.AppDB().Exec(
		`INSERT INTO posts (project_id, body, status) VALUES ('test-proj', 'retry me', 'failed')`,
	)
	postID, _ := pr.LastInsertId()
	_, _ = ctx.AppDB().Exec(
		`INSERT INTO post_targets (post_id, social_account_id, status, last_error, attempts)
		 VALUES (?, ?, 'failed', 'rate limit', 1)`, postID, acctID,
	)

	app := &App{}
	out, err := app.toolPostRetry(ctx, map[string]any{"post_id": postID})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(map[string]any)
	if res["retried"].(int64) != 1 {
		t.Errorf("expected retried=1, got %+v", res)
	}
	var status string
	ctx.AppDB().QueryRow(`SELECT status FROM post_targets WHERE post_id=?`, postID).Scan(&status)
	if status != "published" {
		t.Errorf("after retry, target status = %q", status)
	}
}

// --- helpers -------------------------------------------------------

// --- Instagram two-step + storage media URL --------------------

func TestPublishInstagram_TwoStepWithStorageMedia(t *testing.T) {
	pf := newRecordingPlatform()
	// Storage hands back a relative signed URL — same shape as
	// extractStorageGetURL parses (MCP-wrapped JSON in content[]).
	pf.callAppResponses["storage:files_get_url"] = json.RawMessage(
		`{"result":{"content":[{"type":"text","text":"{\"url\":\"/files/77/content?sig=abc&exp=99\",\"file_id\":77}"}]}}`,
	)
	pf.executeResponses["create_media_container"] = &sdk.ExecuteResult{
		Success: true,
		Data:    json.RawMessage(`{"id":"container_42"}`),
	}
	pf.executeResponses["publish_media_container"] = &sdk.ExecuteResult{
		Success: true,
		Data:    json.RawMessage(`{"id":"ig_999"}`),
	}
	ctx := newSocialCtx(t, pf)
	r, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, external_account_id, display_name, status)
		 VALUES ('test-proj', 'instagram', 42, 'ig-acct-1', 'My Brand', 'active')`,
	)
	acctID, _ := r.LastInsertId()

	app := &App{}
	out, err := app.toolPostCreate(ctx, map[string]any{
		"body":               "hello insta",
		"social_account_ids": []any{acctID},
		"media_storage_ids":  []any{int64(77)},
	})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(map[string]any)
	postID := res["post_id"].(int64)

	// Both IG steps were called.
	var sawCreate, sawPublish bool
	for _, c := range pf.executeCalls {
		if c.Tool == "create_media_container" {
			sawCreate = true
			if c.Input["caption"] != "hello insta" {
				t.Errorf("caption not body: %+v", c.Input)
			}
			if c.Input["instagramAccountId"] != "ig-acct-1" {
				t.Errorf("instagramAccountId not extID: %+v", c.Input)
			}
			imgURL, _ := c.Input["imageUrl"].(string)
			if !strings.Contains(imgURL, "/files/77/content") {
				t.Errorf("imageUrl not from storage: %q", imgURL)
			}
		}
		if c.Tool == "publish_media_container" {
			sawPublish = true
			if c.Input["containerId"] != "container_42" {
				t.Errorf("containerId not threaded from step 1: %+v", c.Input)
			}
		}
	}
	if !sawCreate || !sawPublish {
		t.Errorf("expected both IG steps; got %+v", pf.executeCalls)
	}

	// Target row marked published.
	var status string
	ctx.AppDB().QueryRow(`SELECT status FROM post_targets WHERE post_id=?`, postID).Scan(&status)
	if status != "published" {
		t.Errorf("target status = %q", status)
	}
}

func TestPublishInstagram_NoMediaFails(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newSocialCtx(t, pf)
	r, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, external_account_id, display_name, status)
		 VALUES ('test-proj', 'instagram', 42, 'ig-acct-1', 'My Brand', 'active')`,
	)
	acctID, _ := r.LastInsertId()
	app := &App{}
	out, _ := app.toolPostCreate(ctx, map[string]any{
		"body":               "no media",
		"social_account_ids": []any{acctID},
	})
	postID := out.(map[string]any)["post_id"].(int64)
	var status, lastErr string
	ctx.AppDB().QueryRow(
		`SELECT status, COALESCE(last_error,'') FROM post_targets WHERE post_id=?`, postID,
	).Scan(&status, &lastErr)
	if status != "failed" {
		t.Errorf("expected failed, got %q", status)
	}
	if !strings.Contains(lastErr, "media") {
		t.Errorf("error should mention media: %q", lastErr)
	}
	// Integration was never called.
	if len(pf.executeCalls) != 0 {
		t.Errorf("integration should not be called when media missing: %+v", pf.executeCalls)
	}
}

// --- TikTok nested input -----------------------------------------

func TestPublishTikTok_BuildsNestedInput(t *testing.T) {
	pf := newRecordingPlatform()
	pf.callAppResponses["storage:files_get_url"] = json.RawMessage(
		`{"result":{"content":[{"type":"text","text":"{\"url\":\"https://cdn.test/v.mp4\"}"}]}}`,
	)
	pf.executeResponses["post_video"] = &sdk.ExecuteResult{
		Success: true,
		Data:    json.RawMessage(`{"data":{"publish_id":"pub_xyz"}}`),
	}
	ctx := newSocialCtx(t, pf)
	r, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, display_name, status)
		 VALUES ('test-proj', 'tiktok', 42, '@tt', 'active')`,
	)
	acctID, _ := r.LastInsertId()

	app := &App{}
	if _, err := app.toolPostCreate(ctx, map[string]any{
		"body":               "ride the wave #fyp",
		"social_account_ids": []any{acctID},
		"media_storage_ids":  []any{int64(7)},
	}); err != nil {
		t.Fatal(err)
	}

	if len(pf.executeCalls) != 1 || pf.executeCalls[0].Tool != "post_video" {
		t.Fatalf("expected post_video call: %+v", pf.executeCalls)
	}
	in := pf.executeCalls[0].Input
	postInfo, ok := in["post_info"].(map[string]any)
	if !ok {
		t.Fatalf("post_info not nested: %+v", in)
	}
	if postInfo["title"] != "ride the wave #fyp" {
		t.Errorf("title not threaded into post_info: %+v", postInfo)
	}
	srcInfo, ok := in["source_info"].(map[string]any)
	if !ok {
		t.Fatalf("source_info not nested: %+v", in)
	}
	if srcInfo["source"] != "PULL_FROM_URL" {
		t.Errorf("source = %v", srcInfo["source"])
	}
	if srcInfo["video_url"] != "https://cdn.test/v.mp4" {
		t.Errorf("video_url = %v", srcInfo["video_url"])
	}
}

// --- YouTube unsupported -----------------------------------------

func TestPublishYouTube_UnsupportedFails(t *testing.T) {
	pf := newRecordingPlatform()
	// Provide a storage URL so we get past the MediaRequired check —
	// the deferral happens inside the strategy dispatch.
	pf.callAppResponses["storage:files_get_url"] = json.RawMessage(
		`{"result":{"content":[{"type":"text","text":"{\"url\":\"https://cdn.test/v.mp4\"}"}]}}`,
	)
	ctx := newSocialCtx(t, pf)
	r, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, display_name, status)
		 VALUES ('test-proj', 'youtube', 42, 'My Channel', 'active')`,
	)
	acctID, _ := r.LastInsertId()
	app := &App{}
	out, _ := app.toolPostCreate(ctx, map[string]any{
		"body":               "skip me",
		"social_account_ids": []any{acctID},
		"media_storage_ids":  []any{int64(1)},
	})
	postID := out.(map[string]any)["post_id"].(int64)
	var lastErr string
	ctx.AppDB().QueryRow(
		`SELECT COALESCE(last_error,'') FROM post_targets WHERE post_id=?`, postID,
	).Scan(&lastErr)
	if !strings.Contains(lastErr, "v0.2") {
		t.Errorf("expected v0.2 deferral note; got %q", lastErr)
	}
}

// --- jobs scheduling ---------------------------------------------

func TestSchedule_DispatchesToJobsApp(t *testing.T) {
	pf := newRecordingPlatform()
	// Mark the install as having jobs bound. The testkit's IntegrationFor
	// reads from the install identity's bindings; set jobs to a non-zero
	// install id (kind=app bindings store install ids, not conn ids).
	pf.identity.Bindings = map[string]any{"jobs": float64(101)}
	ctx := newSocialCtx(t, pf)
	r, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, display_name, status)
		 VALUES ('test-proj', 'twitter', 42, '@me', 'active')`,
	)
	acctID, _ := r.LastInsertId()

	app := &App{}
	out, err := app.toolPostCreate(ctx, map[string]any{
		"body":               "later",
		"social_account_ids": []any{acctID},
		"schedule_at":        "2026-05-01T10:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(map[string]any)
	if res["status"] != "scheduled" {
		t.Errorf("post status = %v, want scheduled", res["status"])
	}

	// jobs_schedule was called with run_at + http target back at us.
	var sawSchedule bool
	for _, c := range pf.callAppCalls {
		if c.AppName == "jobs" && c.Tool == "jobs_schedule" {
			sawSchedule = true
			schedule, _ := c.Input["schedule"].(map[string]any)
			if schedule["kind"] != "once" {
				t.Errorf("schedule.kind = %v", schedule["kind"])
			}
			if schedule["run_at"] != "2026-05-01T10:00:00Z" {
				t.Errorf("run_at = %v", schedule["run_at"])
			}
			target, _ := c.Input["target"].(map[string]any)
			if target["kind"] != "http" {
				t.Errorf("target.kind = %v", target["kind"])
			}
			if !strings.Contains(target["url"].(string), "/jobs/publish_post") {
				t.Errorf("target.url = %v", target["url"])
			}
			if c.Input["idempotency_key"] == "" {
				t.Errorf("missing idempotency_key")
			}
		}
	}
	if !sawSchedule {
		t.Errorf("expected jobs_schedule call; got %+v", pf.callAppCalls)
	}

	// Integration was NOT called yet — publishing waits for the job
	// callback.
	if len(pf.executeCalls) != 0 {
		t.Errorf("integration should not run until job fires; got %+v", pf.executeCalls)
	}
}

func TestSchedule_FailsWhenJobsUnbound(t *testing.T) {
	pf := newRecordingPlatform()
	// No jobs binding.
	pf.identity.Bindings = map[string]any{}
	ctx := newSocialCtx(t, pf)
	r, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, display_name, status)
		 VALUES ('test-proj', 'twitter', 42, '@me', 'active')`,
	)
	acctID, _ := r.LastInsertId()

	app := &App{}
	out, _ := app.toolPostCreate(ctx, map[string]any{
		"body":               "later",
		"social_account_ids": []any{acctID},
		"schedule_at":        "2026-05-01T10:00:00Z",
	})
	res := out.(map[string]any)
	if res["isError"] != true {
		t.Errorf("expected isError when jobs unbound; got %+v", res)
	}
}

// --- helpers -------------------------------------------------------

func TestExtractStorageGetURL(t *testing.T) {
	wrapped := json.RawMessage(`{"result":{"content":[{"type":"text","text":"{\"url\":\"/files/1/content?sig=x\",\"file_id\":1}"}]}}`)
	if got := extractStorageGetURL(wrapped); got != "/files/1/content?sig=x" {
		t.Errorf("got %q", got)
	}
	direct := json.RawMessage(`{"url":"/files/2/content?sig=y","file_id":2}`)
	if got := extractStorageGetURL(direct); got != "/files/2/content?sig=y" {
		t.Errorf("direct: got %q", got)
	}
}

func TestExtractContainerID(t *testing.T) {
	if got := extractContainerID(json.RawMessage(`{"id":"c_1"}`)); got != "c_1" {
		t.Errorf("got %q", got)
	}
	if got := extractContainerID(json.RawMessage(`{"containerId":"c_2"}`)); got != "c_2" {
		t.Errorf("got %q", got)
	}
}

func TestExtractTikTokPublishID(t *testing.T) {
	if got := extractTikTokPublishID(json.RawMessage(`{"data":{"publish_id":"pub_1"}}`)); got != "pub_1" {
		t.Errorf("nested: got %q", got)
	}
	if got := extractTikTokPublishID(json.RawMessage(`{"publish_id":"pub_2"}`)); got != "pub_2" {
		t.Errorf("flat: got %q", got)
	}
}

func TestWalkPath(t *testing.T) {
	m := map[string]any{
		"picture": map[string]any{
			"data": map[string]any{
				"url": "https://x.test/img.jpg",
			},
		},
		"id": "42",
	}
	if got := walkPath(m, "picture.data.url"); got != "https://x.test/img.jpg" {
		t.Errorf("nested path: %v", got)
	}
	if got := walkPath(m, "id"); got != "42" {
		t.Errorf("flat path: %v", got)
	}
	if got := walkPath(m, "missing"); got != nil {
		t.Errorf("missing path should be nil, got %v", got)
	}
}

func TestExtractPostIdentity(t *testing.T) {
	id, url := extractPostIdentity("twitter", json.RawMessage(`{"data":{"id":"123"}}`))
	if id != "123" || !strings.Contains(url, "/status/123") {
		t.Errorf("twitter: id=%q url=%q", id, url)
	}
	id, url = extractPostIdentity("facebook", json.RawMessage(`{"id":"100_500"}`))
	if id != "100_500" || !strings.Contains(url, "100_500") {
		t.Errorf("facebook: id=%q url=%q", id, url)
	}
}

func TestCacheAvatar_WritesContentAddressedFile(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DB_PATH", dir+"/app.db")

	// Stub upstream returning a small JPEG. The body bytes drive the
	// content-addressed filename, so any deterministic payload works.
	body := []byte{0xff, 0xd8, 0xff, 0xe0, 0x42}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(body)
	}))
	defer srv.Close()

	app := &App{}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	got := app.cacheAvatar(ctx, srv.URL+"/avatar.jpg")
	wantPrefix := "/api/apps/social/avatars/"
	if !strings.HasPrefix(got, wantPrefix) {
		t.Fatalf("expected local URL with prefix %q, got %q", wantPrefix, got)
	}
	name := strings.TrimPrefix(got, wantPrefix)
	if !strings.HasSuffix(name, ".jpg") {
		t.Errorf("expected .jpg ext, got %q", name)
	}
	// File should exist on disk under data/avatars/.
	avatarPath := dir + "/avatars/" + name
	read, err := os.ReadFile(avatarPath)
	if err != nil {
		t.Fatalf("avatar not written: %v", err)
	}
	if string(read) != string(body) {
		t.Errorf("disk bytes diverged from upstream")
	}

	// Idempotent: same upstream URL → same filename, no second write.
	got2 := app.cacheAvatar(ctx, srv.URL+"/avatar.jpg")
	if got2 != got {
		t.Errorf("not idempotent: %q vs %q", got2, got)
	}

	// Already-cached URL is returned unchanged (no re-fetch).
	already := "/api/apps/social/avatars/abc.png"
	if app.cacheAvatar(ctx, already) != already {
		t.Errorf("already-cached URL should pass through")
	}
}

func TestCacheAvatar_FailsOpenOnUpstreamError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DB_PATH", dir+"/app.db")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", 500)
	}))
	defer srv.Close()
	app := &App{}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	upstream := srv.URL + "/img"
	if got := app.cacheAvatar(ctx, upstream); got != upstream {
		t.Errorf("expected fallback to upstream URL on 5xx, got %q", got)
	}
}

func TestHandleAvatar_RejectsTraversal(t *testing.T) {
	app := &App{}
	for _, name := range []string{"..", "../etc/passwd", "a/b", `a\b`, ""} {
		req := httptest.NewRequest("GET", "/avatars/"+name, nil)
		rec := httptest.NewRecorder()
		app.handleAvatar(rec, req)
		if rec.Code == http.StatusOK {
			t.Errorf("traversal name %q should not 200", name)
		}
	}
}

func TestExtractStorageContentType(t *testing.T) {
	cases := []struct {
		name, in, want string
	}{
		{"direct file wrapper", `{"file":{"content_type":"video/mp4","id":1}}`, "video/mp4"},
		{"direct flat", `{"content_type":"image/png"}`, "image/png"},
		{"jsonrpc wrapped — the CallApp shape that v0.4.3 missed",
			`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{\"file\":{\"content_type\":\"video/mp4\",\"id\":5},\"found\":true}"}]}}`,
			"video/mp4"},
		{"flat content (no jsonrpc layer)",
			`{"content":[{"type":"text","text":"{\"content_type\":\"image/jpeg\"}"}]}`,
			"image/jpeg"},
		{"empty payload", "", ""},
		{"unrecognised shape", `{"something":"else"}`, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := extractStorageContentType(json.RawMessage(c.in))
			if got != c.want {
				t.Errorf("got %q, want %q", got, c.want)
			}
		})
	}
}
