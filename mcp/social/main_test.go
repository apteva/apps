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
	"strings"
	"sync"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// --- stub PlatformClient -------------------------------------------

type recordingPlatform struct {
	mu               sync.Mutex
	executeCalls     []executeCall
	startOAuthCalls  []sdk.OAuthStartRequest
	disconnectCalls  []int64
	nextStartOAuth   *sdk.OAuthStartResult
	nextStartErr     error
	executeResponses map[string]*sdk.ExecuteResult // keyed by tool name
	executeErr       error
	identity         *sdk.InstallIdentity
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

func (p *recordingPlatform) CallApp(string, string, map[string]any) (json.RawMessage, error) {
	return nil, nil
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
