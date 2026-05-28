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
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// --- stub PlatformClient -------------------------------------------

type recordingPlatform struct {
	tk.BasePlatformClient
	mu               sync.Mutex
	executeCalls     []executeCall
	callAppCalls     []callAppCall
	startOAuthCalls  []sdk.OAuthStartRequest
	disconnectCalls  []int64
	nextStartOAuth   *sdk.OAuthStartResult
	nextStartErr     error
	executeResponses map[string]*sdk.ExecuteResult // keyed by tool name
	executeSequences map[string][]*sdk.ExecuteResult
	callAppResponses map[string]json.RawMessage // keyed by "app:tool"
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
		executeSequences: map[string][]*sdk.ExecuteResult{},
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
	if seq := p.executeSequences[tool]; len(seq) > 0 {
		r := seq[0]
		p.executeSequences[tool] = seq[1:]
		return r, nil
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

func (p *recordingPlatform) GetGrants(instanceID int64) (*sdk.GrantsResponse, error) {
	return &sdk.GrantsResponse{}, nil
}

func (p *recordingPlatform) CallAppResult(appName, tool string, input map[string]any, out any) error {
	raw, err := p.CallApp(appName, tool, input)
	if err != nil {
		return err
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	// Mirror app-sdk decodeMCPEnvelope: prefer the wrapped
	// {result:{content:[{text:"<inner>"}]}} shape, fall through to
	// direct decode for already-unwrapped bytes.
	var env struct {
		Result *struct {
			Content []struct {
				Text string `json:"text"`
			} `json:"content"`
		} `json:"result"`
	}
	if json.Unmarshal(raw, &env) == nil && env.Result != nil && len(env.Result.Content) > 0 {
		return json.Unmarshal([]byte(env.Result.Content[0].Text), out)
	}
	return json.Unmarshal(raw, out)
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

func TestAccountAdd_UsesSelectedProfile(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newSocialCtx(t, pf)
	app := &App{}

	firstOut, _ := app.toolProfileCreate(ctx, map[string]any{"name": "First"})
	secondOut, _ := app.toolProfileCreate(ctx, map[string]any{"name": "Second"})
	first := firstOut.(map[string]any)["profile"].(*Profile)
	second := secondOut.(map[string]any)["profile"].(*Profile)
	if first.ID == second.ID {
		t.Fatal("test setup created duplicate profile ids")
	}

	out, err := app.toolAccountAdd(ctx, map[string]any{
		"platform":   "twitter",
		"profile_id": second.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	pendingID := out.(map[string]any)["pending_account_id"].(int64)
	var storedProfileID int64
	if err := ctx.AppDB().QueryRow(
		`SELECT COALESCE(profile_id,0) FROM pending_accounts WHERE id=?`,
		pendingID,
	).Scan(&storedProfileID); err != nil {
		t.Fatal(err)
	}
	if storedProfileID != second.ID {
		t.Fatalf("pending profile_id = %d, want selected profile %d", storedProfileID, second.ID)
	}
	if storedProfileID == first.ID {
		t.Fatalf("pending account was assigned to first/default profile %d", first.ID)
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
		Data:    json.RawMessage(`{"data":[{"id":"100","name":"My Page"}]}`),
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

func TestPostCreate_FacebookImageUsesPhotoToolAndStorageProject(t *testing.T) {
	pf := newRecordingPlatform()
	pf.callAppResponses["storage:files_get"] = json.RawMessage(
		`{"result":{"content":[{"type":"text","text":"{\"id\":296,\"content_type\":\"image/png\",\"size_bytes\":1308309}"}]}}`,
	)
	pf.callAppResponses["storage:files_get_url"] = json.RawMessage(
		`{"result":{"content":[{"type":"text","text":"{\"url\":\"https://agents.example.com/api/apps/storage/files/296/content/crop-294.png?sig=abc&exp=99&project_id=media-proj\"}"}]}}`,
	)
	pf.executeResponses["post_photo_to_page"] = &sdk.ExecuteResult{
		Success: true,
		Data:    json.RawMessage(`{"id":"100_501"}`),
	}
	ctx := newSocialCtx(t, pf)
	r, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, external_account_id, display_name, status)
		 VALUES ('test-proj', 'facebook', 42, '100', 'My Page', 'active')`,
	)
	acctID, _ := r.LastInsertId()

	app := &App{}
	if _, err := app.toolPostCreate(ctx, map[string]any{
		"body":               "hello photo",
		"social_account_ids": []any{acctID},
		"media_storage_ids":  []any{int64(296)},
		"media_project_id":   "media-proj",
	}); err != nil {
		t.Fatal(err)
	}

	if len(pf.executeCalls) != 1 {
		t.Fatalf("expected 1 execute call, got %d", len(pf.executeCalls))
	}
	got := pf.executeCalls[0]
	if got.Tool != "post_photo_to_page" {
		t.Fatalf("tool = %q, want post_photo_to_page", got.Tool)
	}
	if got.Input["caption"] != "hello photo" {
		t.Errorf("caption field not set: %+v", got.Input)
	}
	if got.Input["url"] == "" {
		t.Errorf("photo url field not set: %+v", got.Input)
	}
	for _, c := range pf.callAppCalls {
		if c.AppName == "storage" && (c.Tool == "files_get" || c.Tool == "files_get_url") {
			if c.Input["_project_id"] != "media-proj" {
				t.Errorf("%s _project_id = %v, want media-proj", c.Tool, c.Input["_project_id"])
			}
		}
	}
}

func TestPostCreate_AttachedMediaResolutionFailureDoesNotPublishTextOnly(t *testing.T) {
	pf := newRecordingPlatform()
	// files_get_url falls back to an empty response, so resolveMedia
	// fails after seeing that media was attached.
	ctx := newSocialCtx(t, pf)
	r, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, external_account_id, display_name, status)
		 VALUES ('test-proj', 'facebook', 42, '100', 'My Page', 'active')`,
	)
	acctID, _ := r.LastInsertId()

	app := &App{}
	out, err := app.toolPostCreate(ctx, map[string]any{
		"body":               "hello missing photo",
		"social_account_ids": []any{acctID},
		"media_storage_ids":  []any{int64(296)},
		"media_project_id":   "media-proj",
	})
	if err != nil {
		t.Fatal(err)
	}
	postID := out.(map[string]any)["post_id"].(int64)

	if len(pf.executeCalls) != 0 {
		t.Fatalf("integration should not be called when attached media cannot resolve: %+v", pf.executeCalls)
	}
	var targetStatus, lastErr, postStatus string
	ctx.AppDB().QueryRow(`SELECT status, COALESCE(last_error,'') FROM post_targets WHERE post_id=?`, postID).Scan(&targetStatus, &lastErr)
	ctx.AppDB().QueryRow(`SELECT status FROM posts WHERE id=?`, postID).Scan(&postStatus)
	if targetStatus != "failed" || postStatus != "failed" {
		t.Fatalf("statuses = target %q post %q, want failed/failed; err=%q", targetStatus, postStatus, lastErr)
	}
	if !strings.Contains(lastErr, "attached media could not be resolved") {
		t.Errorf("last_error = %q", lastErr)
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
			imgURL, _ := c.Input["image_url"].(string)
			if !strings.Contains(imgURL, "/files/77/content") {
				t.Errorf("image_url not from storage: %q", imgURL)
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

func TestPublishInstagram_RetriesTransientPublishRace(t *testing.T) {
	oldDelays := instagramPublishRetryDelays
	instagramPublishRetryDelays = []time.Duration{0}
	defer func() { instagramPublishRetryDelays = oldDelays }()

	pf := newRecordingPlatform()
	pf.callAppResponses["storage:files_get_url"] = json.RawMessage(
		`{"result":{"content":[{"type":"text","text":"{\"url\":\"/files/88/content?sig=abc&exp=99\",\"file_id\":88}"}]}}`,
	)
	pf.executeResponses["create_media_container"] = &sdk.ExecuteResult{
		Success: true,
		Status:  200,
		Data:    json.RawMessage(`{"id":"container_retry"}`),
	}
	pf.executeSequences["publish_media_container"] = []*sdk.ExecuteResult{
		{
			Success: false,
			Status:  400,
			Data:    json.RawMessage(`{"error":{"code":9007,"message":"Media ID is not available"}}`),
		},
		{
			Success: true,
			Status:  200,
			Data:    json.RawMessage(`{"id":"ig_retry_ok"}`),
		},
	}

	ctx := newSocialCtx(t, pf)
	r, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, external_account_id, display_name, status)
		 VALUES ('test-proj', 'instagram', 42, 'ig-acct-1', 'My Brand', 'active')`,
	)
	acctID, _ := r.LastInsertId()

	app := &App{}
	out, err := app.toolPostCreate(ctx, map[string]any{
		"body":               "hello insta retry",
		"social_account_ids": []any{acctID},
		"media_storage_ids":  []any{int64(88)},
	})
	if err != nil {
		t.Fatal(err)
	}
	postID := out.(map[string]any)["post_id"].(int64)

	publishCalls := 0
	for _, c := range pf.executeCalls {
		if c.Tool == "publish_media_container" {
			publishCalls++
		}
	}
	if publishCalls != 2 {
		t.Fatalf("expected publish_media_container to be retried once, got %d calls: %+v", publishCalls, pf.executeCalls)
	}

	var status, platformPostID, lastErr string
	ctx.AppDB().QueryRow(
		`SELECT status, COALESCE(platform_post_id,''), COALESCE(last_error,'') FROM post_targets WHERE post_id=?`,
		postID,
	).Scan(&status, &platformPostID, &lastErr)
	if status != "published" {
		t.Fatalf("target status = %q, last_error=%q", status, lastErr)
	}
	if platformPostID != "ig_retry_ok" {
		t.Errorf("platform_post_id = %q", platformPostID)
	}
	if lastErr != "" {
		t.Errorf("last_error should be cleared after retry success, got %q", lastErr)
	}
}

func TestPublishInstagram_VideoContinuesWhenStatusProbeAuthFails(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["create_media_container"] = &sdk.ExecuteResult{
		Success: true,
		Status:  200,
		Data:    json.RawMessage(`{"id":"container_video"}`),
	}
	pf.executeResponses["get_container_status"] = &sdk.ExecuteResult{
		Success: false,
		Status:  400,
		Data:    json.RawMessage(`{"error":{"code":100,"error_subcode":33,"message":"Authorization Error","type":"GraphMethodException"}}`),
	}
	pf.executeResponses["publish_media_container"] = &sdk.ExecuteResult{
		Success: true,
		Status:  200,
		Data:    json.RawMessage(`{"id":"ig_video_ok"}`),
	}

	ctx := newSocialCtx(t, pf)
	app := &App{}
	id, _, err := app.publishInstagram(ctx, platforms["instagram"], publishJob{
		connID:    42,
		extID:     "ig-acct-1",
		body:      "video post",
		pageCreds: `{"access_token":"page-token"}`,
		media:     []mediaItem{{URL: "https://example.com/reel.mp4", Mime: "video/mp4", Bytes: 1234}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if id != "ig_video_ok" {
		t.Fatalf("published id = %q", id)
	}

	var sawCreate, sawStatus, sawPublish bool
	for _, c := range pf.executeCalls {
		switch c.Tool {
		case "create_media_container":
			sawCreate = true
			if c.Input["media_type"] != "REELS" {
				t.Errorf("media_type = %v", c.Input["media_type"])
			}
			if c.Input["sync"] != true {
				t.Errorf("sync should be true for video container creation: %+v", c.Input)
			}
		case "get_container_status":
			sawStatus = true
		case "publish_media_container":
			sawPublish = true
		}
	}
	if !sawCreate || !sawStatus || !sawPublish {
		t.Fatalf("expected create, status probe, and publish calls; got %+v", pf.executeCalls)
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

// --- TikTok FILE_UPLOAD init shape -----------------------------------

func TestPublishTikTok_BuildsFileUploadInitInput_SingleChunk(t *testing.T) {
	// 4 MB video — well under the 64 MB single-chunk threshold.
	const videoBytes = 4 * 1024 * 1024
	pf := newRecordingPlatform()
	pf.callAppResponses["storage:files_get"] = json.RawMessage(
		`{"result":{"content":[{"type":"text","text":"{\"id\":7,\"content_type\":\"video/mp4\",\"size_bytes\":4194304}"}]}}`,
	)
	pf.callAppResponses["storage:files_get_url"] = json.RawMessage(
		`{"result":{"content":[{"type":"text","text":"{\"url\":\"https://cdn.test/v.mp4\"}"}]}}`,
	)
	// Init response missing upload_url so the strategy stops at the
	// init step — exercises the init-shape assertions without needing
	// a real HTTP target for the bytes-PUT.
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
		t.Fatalf("expected one post_video init call: %+v", pf.executeCalls)
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
	if srcInfo["source"] != "FILE_UPLOAD" {
		t.Errorf("source = %v, want FILE_UPLOAD (PULL_FROM_URL needs domain verification)", srcInfo["source"])
	}
	if srcInfo["video_size"] != int64(videoBytes) {
		t.Errorf("video_size = %v, want %d", srcInfo["video_size"], videoBytes)
	}
	if srcInfo["chunk_size"] != int64(videoBytes) {
		t.Errorf("single-chunk should set chunk_size = video_size: %v", srcInfo["chunk_size"])
	}
	if srcInfo["total_chunk_count"] != 1 {
		t.Errorf("single-chunk should set total_chunk_count = 1: %v", srcInfo["total_chunk_count"])
	}

	// And the strategy bailed cleanly on the missing upload_url —
	// recorded in post_targets.last_error rather than crashing.
	var lastErr string
	ctx.AppDB().QueryRow(
		`SELECT COALESCE(last_error,'') FROM post_targets WHERE social_account_id=? ORDER BY id DESC LIMIT 1`, acctID,
	).Scan(&lastErr)
	if !strings.Contains(lastErr, "upload_url") {
		t.Errorf("expected error about missing upload_url; got %q", lastErr)
	}
}

func TestPublishTikTok_FileUploadInit_MultiChunkMath(t *testing.T) {
	// 100 MB video — exceeds the 64 MB single-chunk threshold so the
	// strategy switches to multi-chunk: 32 MB chunks, total_count =
	// floor(100 MB / 32 MB) = 3, final chunk absorbs the trailing bytes.
	const videoBytes = 100 * 1024 * 1024
	pf := newRecordingPlatform()
	pf.callAppResponses["storage:files_get"] = json.RawMessage(
		`{"result":{"content":[{"type":"text","text":"{\"id\":8,\"content_type\":\"video/mp4\",\"size_bytes\":104857600}"}]}}`,
	)
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
		"body":               "long video",
		"social_account_ids": []any{acctID},
		"media_storage_ids":  []any{int64(8)},
	}); err != nil {
		t.Fatal(err)
	}

	if len(pf.executeCalls) != 1 || pf.executeCalls[0].Tool != "post_video" {
		t.Fatalf("expected post_video init: %+v", pf.executeCalls)
	}
	srcInfo := pf.executeCalls[0].Input["source_info"].(map[string]any)
	if srcInfo["video_size"] != int64(videoBytes) {
		t.Errorf("video_size = %v, want %d", srcInfo["video_size"], videoBytes)
	}
	if srcInfo["chunk_size"] != int64(32*1024*1024) {
		t.Errorf("chunk_size = %v, want 32 MB", srcInfo["chunk_size"])
	}
	if srcInfo["total_chunk_count"] != 3 {
		t.Errorf("total_chunk_count = %v, want 3 (floor(100/32))", srcInfo["total_chunk_count"])
	}
}

// --- YouTube upload (resumable) -----------------------------------

func TestPublishYouTube_InitCallShape(t *testing.T) {
	// Verify publishYoutube calls upload_video_init with the right
	// snippet shape (title from post body, default privacy=public),
	// and surfaces a clear error when the server didn't forward the
	// Location header (older apteva-server / network drop).
	pf := newRecordingPlatform()
	// Mock the upload_video_init call: success but no Location header,
	// which is exactly the failure mode we want to surface.
	pf.executeResponses["upload_video_init"] = &sdk.ExecuteResult{
		Success: true,
		Status:  200,
		Data:    json.RawMessage(`{}`),
	}
	pf.callAppResponses["storage:files_get"] = json.RawMessage(
		`{"result":{"content":[{"type":"text","text":"{\"id\":1,\"content_type\":\"video/mp4\",\"name\":\"v.mp4\"}"}]}}`,
	)
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
		"body":               "My video title",
		"social_account_ids": []any{acctID},
		"media_storage_ids":  []any{int64(1)},
	})
	postID := out.(map[string]any)["post_id"].(int64)

	// upload_video_init was called with title from body.
	var sawInit bool
	for _, c := range pf.executeCalls {
		if c.Tool == "upload_video_init" {
			sawInit = true
			snippet, _ := c.Input["snippet"].(map[string]any)
			if snippet["title"] != "My video title" {
				t.Errorf("snippet.title = %v, want body", snippet["title"])
			}
			status, _ := c.Input["status"].(map[string]any)
			if status["privacyStatus"] != "public" {
				t.Errorf("default privacyStatus should be public, got %v", status["privacyStatus"])
			}
		}
	}
	if !sawInit {
		t.Errorf("expected upload_video_init call, got %+v", pf.executeCalls)
	}
	// Without a Location header the strategy should surface a clear
	// "no Location" error rather than silently succeeding.
	var lastErr string
	ctx.AppDB().QueryRow(
		`SELECT COALESCE(last_error,'') FROM post_targets WHERE post_id=?`, postID,
	).Scan(&lastErr)
	if !strings.Contains(lastErr, "no Location header") {
		t.Errorf("expected 'no Location header' error; got %q", lastErr)
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
	t.Setenv("APTEVA_PROJECT_ID", "")
	r, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, display_name, status)
		 VALUES ('', 'twitter', 42, '@me', 'active')`,
	)
	acctID, _ := r.LastInsertId()

	app := &App{}
	out, err := app.toolPostCreate(ctx, map[string]any{
		"body":               "later",
		"social_account_ids": []any{acctID},
		"schedule_at":        "2026-05-01T10:00:00Z",
		"_project_id":        "ui-proj",
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
			// v0.5.1+: cross-app {app, path} shape (the old `url`
			// was a relative path that the jobs dispatcher couldn't
			// resolve at fire time).
			if target["app"] != "social" {
				t.Errorf("target.app = %v, want social", target["app"])
			}
			if path, _ := target["path"].(string); path != "/jobs/publish_post" {
				t.Errorf("target.path = %v, want /jobs/publish_post", target["path"])
			}
			if c.Input["idempotency_key"] == "" {
				t.Errorf("missing idempotency_key")
			}
			if c.Input["_project_id"] != "ui-proj" {
				t.Errorf("jobs _project_id = %v, want ui-proj", c.Input["_project_id"])
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

func TestInboxSyncFacebookComments(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["get_page_posts"] = &sdk.ExecuteResult{
		Success: true,
		Status:  200,
		Data: json.RawMessage(`{"data":[
			{"id":"100_200","permalink_url":"https://facebook.test/100_200"}
		]}`),
	}
	pf.executeResponses["list_media_comments"] = &sdk.ExecuteResult{
		Success: true,
		Status:  200,
		Data: json.RawMessage(`{"data":[
			{"id":"c1","message":"hello","created_time":"2026-05-27T10:00:00+0000","from":{"id":"u1","name":"Ada","picture":{"data":{"url":"https://example.test/ada.jpg"}}},"replies":{"data":[
				{"id":"c2","message":"reply","created_time":"2026-05-27T10:01:00+0000","from":{"id":"u2","name":"Ben"}}
			]}}
		]}`),
	}
	ctx := newSocialCtx(t, pf)
	app := &App{}
	r, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, external_account_id, display_name, status, page_credentials)
		 VALUES ('test-proj', 'facebook', 42, '100', 'My Page', 'active', '{"access_token":"PAGE_TOKEN"}')`,
	)
	acctID, _ := r.LastInsertId()

	out := app.syncInbox(ctx, inboxSyncOptions{ProjectID: "test-proj", AccountIDs: []int64{acctID}, Limit: 10})
	if out.Count != 1 || out.Results[0].Status != "ok" {
		t.Fatalf("sync result = %+v", out)
	}
	if out.Results[0].Comments != 2 {
		t.Fatalf("comments inserted = %d", out.Results[0].Comments)
	}
	var count int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE social_account_id=?`, acctID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 2 {
		t.Fatalf("inbox item count = %d", count)
	}
	var parent, thread string
	if err := ctx.AppDB().QueryRow(`SELECT COALESCE(parent_external_id,''), COALESCE(thread_external_id,'') FROM inbox_items WHERE external_id='c2'`).Scan(&parent, &thread); err != nil {
		t.Fatal(err)
	}
	if parent != "c1" || thread != "c1" {
		t.Fatalf("reply threading parent=%q thread=%q", parent, thread)
	}
	var avatar string
	if err := ctx.AppDB().QueryRow(`SELECT COALESCE(author_avatar_url,'') FROM inbox_items WHERE external_id='c1'`).Scan(&avatar); err != nil {
		t.Fatal(err)
	}
	if avatar != "https://example.test/ada.jpg" {
		t.Fatalf("comment avatar = %q", avatar)
	}
}

func TestInboxSyncFacebookDMsMentionsReviews(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["get_page_posts"] = &sdk.ExecuteResult{
		Success: true,
		Status:  200,
		Data:    json.RawMessage(`{"data":[]}`),
	}
	pf.executeResponses["facebook_list_conversations"] = &sdk.ExecuteResult{
		Success: true,
		Status:  200,
		Data:    json.RawMessage(`{"data":[{"id":"thread_1"}]}`),
	}
	pf.executeResponses["list_conversation_messages"] = &sdk.ExecuteResult{
		Success: true,
		Status:  200,
		Data: json.RawMessage(`{"data":[
			{"id":"msg_1","message":"hello dm","created_time":"2026-05-27T10:00:00+0000","from":{"id":"psid_1","name":"Ada"}}
		]}`),
	}
	pf.executeResponses["facebook_list_tagged"] = &sdk.ExecuteResult{
		Success: true,
		Status:  200,
		Data: json.RawMessage(`{"data":[
			{"id":"tag_1","message":"mentioned you","created_time":"2026-05-27T10:01:00+0000","from":{"id":"u1","name":"Ada"},"permalink_url":"https://facebook.test/tag_1"}
		]}`),
	}
	pf.executeResponses["facebook_list_reviews"] = &sdk.ExecuteResult{
		Success: true,
		Status:  200,
		Data: json.RawMessage(`{"data":[
			{"id":"review_1","review_text":"great","recommendation_type":"positive","rating":5,"created_time":"2026-05-27T10:02:00+0000","reviewer":{"id":"u2","name":"Ben"}}
		]}`),
	}
	ctx := newSocialCtx(t, pf)
	app := &App{}
	r, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, external_account_id, display_name, status, page_credentials)
		 VALUES ('test-proj', 'facebook', 42, '100', 'My Page', 'active', '{"access_token":"PAGE_TOKEN"}')`,
	)
	acctID, _ := r.LastInsertId()

	out := app.syncInbox(ctx, inboxSyncOptions{ProjectID: "test-proj", AccountIDs: []int64{acctID}, Limit: 10})
	if out.Count != 1 || out.Results[0].Status != "ok" {
		t.Fatalf("sync result = %+v", out)
	}
	res := out.Results[0]
	if res.DMs != 1 || res.Mentions != 1 || res.Reviews != 1 {
		t.Fatalf("counts = %+v", res)
	}
	for kind, want := range map[string]int{inboxKindDM: 1, inboxKindMention: 1, inboxKindReview: 1} {
		var got int
		if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE social_account_id=? AND kind=?`, acctID, kind).Scan(&got); err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("kind %s count = %d", kind, got)
		}
	}
}

func TestInboxReplyCommentCallsMetaTool(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["reply_to_comment"] = &sdk.ExecuteResult{
		Success: true,
		Status:  200,
		Data:    json.RawMessage(`{"id":"reply_1"}`),
	}
	ctx := newSocialCtx(t, pf)
	r, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, external_account_id, display_name, status, page_credentials)
		 VALUES ('test-proj', 'facebook', 42, '100', 'My Page', 'active', '{"access_token":"PAGE_TOKEN"}')`,
	)
	acctID, _ := r.LastInsertId()
	itemID, _, err := upsertInboxItem(ctx.AppDB(), inboxUpsertInput{
		ProjectID:        "test-proj",
		SocialAccountID:  acctID,
		Platform:         "facebook",
		Kind:             inboxKindComment,
		ExternalID:       "comment_1",
		ThreadExternalID: "comment_1",
		AuthorName:       "Ada",
		Body:             "hello",
		OccurredAt:       time.Date(2026, 5, 27, 10, 0, 0, 0, time.UTC),
		Direction:        "inbound",
	})
	if err != nil {
		t.Fatal(err)
	}
	app := &App{}
	resAny, err := app.toolInboxReply(ctx, map[string]any{"id": itemID, "body": "thanks"})
	if err != nil {
		t.Fatal(err)
	}
	res := resAny.(inboxOutcome)
	if res.Status != "ok" || res.ExternalID != "reply_1" {
		t.Fatalf("reply result = %+v", res)
	}
	if len(pf.executeCalls) != 1 || pf.executeCalls[0].Tool != "reply_to_comment" {
		t.Fatalf("execute calls = %+v", pf.executeCalls)
	}
	if pf.executeCalls[0].Input["access_token"] != "PAGE_TOKEN" {
		t.Fatalf("missing page token: %+v", pf.executeCalls[0].Input)
	}
	var status string
	if err := ctx.AppDB().QueryRow(`SELECT status FROM inbox_items WHERE id=?`, itemID).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != inboxStatusReplied {
		t.Fatalf("parent status = %q", status)
	}
}

func TestInboxReplyFacebookDMCallsMessengerTool(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["facebook_send_message"] = &sdk.ExecuteResult{
		Success: true,
		Status:  200,
		Data:    json.RawMessage(`{"message_id":"msg_reply_1","recipient_id":"psid_1"}`),
	}
	ctx := newSocialCtx(t, pf)
	r, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, external_account_id, display_name, status, page_credentials)
		 VALUES ('test-proj', 'facebook', 42, '100', 'My Page', 'active', '{"access_token":"PAGE_TOKEN"}')`,
	)
	acctID, _ := r.LastInsertId()
	itemID, _, err := upsertInboxItem(ctx.AppDB(), inboxUpsertInput{
		ProjectID:        "test-proj",
		SocialAccountID:  acctID,
		Platform:         "facebook",
		Kind:             inboxKindDM,
		ExternalID:       "msg_1",
		ThreadExternalID: "thread_1",
		AuthorExternalID: "psid_1",
		AuthorName:       "Ada",
		Body:             "hello",
		OccurredAt:       time.Date(2026, 5, 27, 10, 0, 0, 0, time.UTC),
		Direction:        "inbound",
	})
	if err != nil {
		t.Fatal(err)
	}
	app := &App{}
	resAny, err := app.toolInboxReply(ctx, map[string]any{"id": itemID, "body": "thanks"})
	if err != nil {
		t.Fatal(err)
	}
	res := resAny.(inboxOutcome)
	if res.Status != "ok" || res.ExternalID != "msg_reply_1" {
		t.Fatalf("reply result = %+v", res)
	}
	if len(pf.executeCalls) != 1 || pf.executeCalls[0].Tool != "facebook_send_message" {
		t.Fatalf("execute calls = %+v", pf.executeCalls)
	}
	call := pf.executeCalls[0]
	if call.Input["pageId"] != "100" || call.Input["messaging_type"] != "RESPONSE" || call.Input["access_token"] != "PAGE_TOKEN" {
		t.Fatalf("bad messenger args: %+v", call.Input)
	}
}

func TestNormaliseScheduleAt(t *testing.T) {
	cases := []struct {
		name, in     string
		wantContains string // RFC3339 substring (timezone offset varies by host)
		wantErr      bool
	}{
		{"datetime-local (panel format)", "2026-05-03T10:30", "2026-05-03T10:30:00", false},
		{"RFC3339 with Z", "2026-05-03T10:30:00Z", "2026-05-03T10:30:00Z", false},
		{"RFC3339 with offset", "2026-05-03T10:30:00+02:00", "2026-05-03T10:30:00", false},
		{"date only", "2026-05-03", "2026-05-03T00:00:00", false},
		{"sql-ish", "2026-05-03 10:30:00", "2026-05-03T10:30:00", false},
		{"garbage", "tomorrow at noon", "", true},
		{"empty", "", "", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got, err := normaliseScheduleAt(c.in)
			if c.wantErr {
				if err == nil {
					t.Errorf("expected error for %q, got %q", c.in, got)
				}
				return
			}
			if err != nil {
				t.Fatalf("unexpected error for %q: %v", c.in, err)
			}
			if !strings.Contains(got, c.wantContains) {
				t.Errorf("normaliseScheduleAt(%q) = %q, want substring %q", c.in, got, c.wantContains)
			}
		})
	}
}

func TestPostCreate_PersistsJobID(t *testing.T) {
	pf := newRecordingPlatform()
	pf.identity.Bindings = map[string]any{"jobs": float64(101)}
	pf.callAppResponses["jobs:jobs_schedule"] = json.RawMessage(`{"job":{"id":777,"name":"x"}}`)
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
		"schedule_at":        "2026-05-10T10:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	postID := out.(map[string]any)["post_id"].(int64)
	var jobID int64
	ctx.AppDB().QueryRow(`SELECT job_id FROM posts WHERE id=?`, postID).Scan(&jobID)
	if jobID != 777 {
		t.Errorf("expected post.job_id=777 (from extracted response), got %d", jobID)
	}
}

func TestPostReschedule_CancelsOldAndCreatesNew(t *testing.T) {
	pf := newRecordingPlatform()
	pf.identity.Bindings = map[string]any{"jobs": float64(101)}
	// First schedule returns id=100, second returns id=200.
	pf.callAppResponses["jobs:jobs_schedule"] = json.RawMessage(`{"job":{"id":100}}`)
	ctx := newSocialCtx(t, pf)
	r, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, display_name, status)
		 VALUES ('test-proj', 'twitter', 42, '@me', 'active')`,
	)
	acctID, _ := r.LastInsertId()
	app := &App{}
	created, _ := app.toolPostCreate(ctx, map[string]any{
		"body":               "later",
		"social_account_ids": []any{acctID},
		"schedule_at":        "2026-05-10T10:00:00Z",
	})
	postID := created.(map[string]any)["post_id"].(int64)

	// Swap the response for the second call.
	pf.callAppResponses["jobs:jobs_schedule"] = json.RawMessage(`{"job":{"id":200}}`)

	out, err := app.toolPostReschedule(ctx, map[string]any{
		"post_id":     postID,
		"schedule_at": "2026-05-15T14:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(map[string]any)
	if res["job_id"] != int64(200) {
		t.Errorf("new job_id = %v, want 200", res["job_id"])
	}
	// We expect: jobs_cancel(100) + jobs_schedule(new). Plus the
	// original jobs_schedule from create.
	var cancelCalled bool
	for _, c := range pf.callAppCalls {
		if c.AppName == "jobs" && c.Tool == "jobs_cancel" {
			cancelCalled = true
			// JSON unmarshalled args may arrive as float64 or int64
			// depending on whether the test passed the literal or it
			// round-tripped through json.Marshal. Normalise.
			var got int64
			switch v := c.Input["id"].(type) {
			case float64:
				got = int64(v)
			case int64:
				got = v
			case int:
				got = int64(v)
			}
			if got != 100 {
				t.Errorf("jobs_cancel called with %v, want 100", c.Input["id"])
			}
		}
	}
	if !cancelCalled {
		t.Errorf("jobs_cancel was not called for the prior job")
	}
	// Post row should reflect the new job_id + new schedule_at.
	var jid int64
	var when string
	ctx.AppDB().QueryRow(
		`SELECT job_id, COALESCE(schedule_at,'') FROM posts WHERE id=?`, postID,
	).Scan(&jid, &when)
	if jid != 200 {
		t.Errorf("post.job_id=%d, want 200", jid)
	}
	if !strings.Contains(when, "2026-05-15") {
		t.Errorf("post.schedule_at=%q, want a 2026-05-15 timestamp", when)
	}
}

func TestPostReschedule_RefusesPublishedPosts(t *testing.T) {
	pf := newRecordingPlatform()
	pf.identity.Bindings = map[string]any{"jobs": float64(101)}
	ctx := newSocialCtx(t, pf)
	app := &App{}
	r, _ := ctx.AppDB().Exec(
		`INSERT INTO posts (project_id, body, status) VALUES ('test-proj', 'hi', 'published')`,
	)
	postID, _ := r.LastInsertId()
	out, _ := app.toolPostReschedule(ctx, map[string]any{
		"post_id":     postID,
		"schedule_at": "2026-06-01T10:00:00Z",
	})
	if out.(map[string]any)["isError"] != true {
		t.Errorf("expected isError on published post, got %+v", out)
	}
}

func TestPostDelete_CancelsJobAndRemovesRows(t *testing.T) {
	pf := newRecordingPlatform()
	pf.identity.Bindings = map[string]any{"jobs": float64(101)}
	pf.callAppResponses["jobs:jobs_schedule"] = json.RawMessage(`{"job":{"id":555}}`)
	ctx := newSocialCtx(t, pf)
	r, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, display_name, status)
		 VALUES ('test-proj', 'twitter', 42, '@me', 'active')`,
	)
	acctID, _ := r.LastInsertId()
	app := &App{}
	created, _ := app.toolPostCreate(ctx, map[string]any{
		"body":               "later",
		"social_account_ids": []any{acctID},
		"schedule_at":        "2026-05-10T10:00:00Z",
	})
	postID := created.(map[string]any)["post_id"].(int64)

	out, err := app.toolPostDelete(ctx, map[string]any{"post_id": postID})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(map[string]any)
	if res["deleted"] != postID {
		t.Errorf("delete result: %+v", res)
	}
	if res["cancelled_job_id"] != int64(555) {
		t.Errorf("expected cancelled_job_id=555, got %v", res["cancelled_job_id"])
	}
	// Rows actually gone.
	var n int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM posts WHERE id=?`, postID).Scan(&n)
	if n != 0 {
		t.Errorf("post row still present")
	}
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM post_targets WHERE post_id=?`, postID).Scan(&n)
	if n != 0 {
		t.Errorf("post_targets still present")
	}
	// jobs_cancel call recorded.
	var sawCancel bool
	for _, c := range pf.callAppCalls {
		if c.AppName == "jobs" && c.Tool == "jobs_cancel" {
			sawCancel = true
		}
	}
	if !sawCancel {
		t.Errorf("expected jobs_cancel call before delete")
	}
}

func TestPostDelete_FansOutUpstream(t *testing.T) {
	// Three published targets — twitter (delete wired, succeeds),
	// facebook (delete wired, integration returns non-2xx so the
	// outcome is "failed"), instagram (no DeleteTool — outcome
	// "unsupported"). Local rows always go.
	pf := newRecordingPlatform()
	pf.executeResponses["delete_tweet"] = &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{}`)}
	pf.executeResponses["facebook_delete_post"] = &sdk.ExecuteResult{Success: false, Status: 400, Data: json.RawMessage(`{"error":{"message":"page token required"}}`)}
	ctx := newSocialCtx(t, pf)
	app := &App{}

	// Three accounts.
	rTw, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, display_name, status)
		 VALUES ('test-proj', 'twitter', 11, '@me', 'active')`)
	twAcct, _ := rTw.LastInsertId()
	rFb, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, display_name, status, page_credentials)
		 VALUES ('test-proj', 'facebook', 22, 'My Page', 'active', '{"access_token":"PAGE_TOKEN"}')`)
	fbAcct, _ := rFb.LastInsertId()
	rIg, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, display_name, status)
		 VALUES ('test-proj', 'instagram', 33, '@me_ig', 'active')`)
	igAcct, _ := rIg.LastInsertId()

	// One published post with all three targets already in 'published'
	// state with platform_post_ids set (skipping the publish path).
	rPost, _ := ctx.AppDB().Exec(
		`INSERT INTO posts (project_id, body, status) VALUES ('test-proj', 'hi', 'published')`)
	postID, _ := rPost.LastInsertId()
	for _, row := range []struct {
		acct int64
		ext  string
	}{{twAcct, "1234567890"}, {fbAcct, "100_999"}, {igAcct, "ig_abc"}} {
		ctx.AppDB().Exec(
			`INSERT INTO post_targets (post_id, social_account_id, status, platform_post_id)
			 VALUES (?, ?, 'published', ?)`,
			postID, row.acct, row.ext)
	}

	out, err := app.toolPostDelete(ctx, map[string]any{"post_id": postID})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(map[string]any)

	// Each wired DeleteTool got called exactly once.
	var twCalls, fbCalls, igCalls int
	for _, c := range pf.executeCalls {
		switch c.Tool {
		case "delete_tweet":
			twCalls++
			if c.Input["tweet_id"] != "1234567890" {
				t.Errorf("delete_tweet wrong tweet_id: %+v", c.Input)
			}
			if c.ConnID != 11 {
				t.Errorf("delete_tweet wrong conn: %d", c.ConnID)
			}
		case "facebook_delete_post":
			fbCalls++
			if c.Input["postId"] != "100_999" {
				t.Errorf("facebook_delete_post wrong postId: %+v", c.Input)
			}
			if c.Input["access_token"] != "PAGE_TOKEN" {
				t.Errorf("facebook_delete_post missing page token: %+v", c.Input)
			}
		default:
			if strings.Contains(c.Tool, "delete") {
				igCalls++
				t.Errorf("unexpected delete call for %s", c.Tool)
			}
		}
	}
	if twCalls != 1 {
		t.Errorf("expected 1 delete_tweet call, got %d", twCalls)
	}
	if fbCalls != 1 {
		t.Errorf("expected 1 facebook_delete_post call, got %d", fbCalls)
	}
	if igCalls != 0 {
		t.Errorf("expected zero IG delete calls (unsupported)")
	}

	// Per-target outcomes returned.
	upstream, ok := res["upstream"].([]targetDeleteOutcome)
	if !ok {
		t.Fatalf("upstream missing or wrong type: %T", res["upstream"])
	}
	byPlatform := map[string]targetDeleteOutcome{}
	for _, o := range upstream {
		byPlatform[o.Platform] = o
	}
	if byPlatform["twitter"].Status != "deleted" {
		t.Errorf("twitter outcome: %+v", byPlatform["twitter"])
	}
	if byPlatform["facebook"].Status != "failed" {
		t.Errorf("facebook outcome: %+v", byPlatform["facebook"])
	}
	if byPlatform["instagram"].Status != "unsupported" {
		t.Errorf("instagram outcome: %+v", byPlatform["instagram"])
	}

	// Local rows are still gone — best-effort semantics.
	var n int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM posts WHERE id=?`, postID).Scan(&n)
	if n != 0 {
		t.Errorf("post row should be deleted regardless of upstream outcome")
	}
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM post_targets WHERE post_id=?`, postID).Scan(&n)
	if n != 0 {
		t.Errorf("post_targets should be deleted regardless of upstream outcome")
	}
}

func TestPostDelete_ForceLocalOnlySkipsUpstream(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newSocialCtx(t, pf)
	app := &App{}

	rTw, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, display_name, status)
		 VALUES ('test-proj', 'twitter', 11, '@me', 'active')`)
	twAcct, _ := rTw.LastInsertId()
	rPost, _ := ctx.AppDB().Exec(
		`INSERT INTO posts (project_id, body, status) VALUES ('test-proj', 'hi', 'published')`)
	postID, _ := rPost.LastInsertId()
	ctx.AppDB().Exec(
		`INSERT INTO post_targets (post_id, social_account_id, status, platform_post_id)
		 VALUES (?, ?, 'published', '1234567890')`, postID, twAcct)

	if _, err := app.toolPostDelete(ctx, map[string]any{
		"post_id":          postID,
		"force_local_only": true,
	}); err != nil {
		t.Fatal(err)
	}
	for _, c := range pf.executeCalls {
		if c.Tool == "delete_tweet" {
			t.Errorf("force_local_only=true should skip upstream calls, got %+v", c)
		}
	}
}

func TestIntArg_AcceptsStringNumbers(t *testing.T) {
	// Smaller models often pass numeric ids as JSON strings.
	// intArg must coerce these so post_delete + post_reschedule
	// don't return "post_id required" on otherwise-valid calls.
	cases := []struct {
		in   map[string]any
		want int
	}{
		{map[string]any{"x": 12}, 12},
		{map[string]any{"x": int64(12)}, 12},
		{map[string]any{"x": float64(12)}, 12},
		{map[string]any{"x": "12"}, 12},
		{map[string]any{"x": "  12 "}, 12},
		{map[string]any{"x": ""}, 0},
		{map[string]any{"x": "abc"}, 0},
		{map[string]any{}, 0},
	}
	for _, c := range cases {
		if got := intArg(c.in, "x", 0); got != c.want {
			t.Errorf("intArg(%v) = %d, want %d", c.in, got, c.want)
		}
	}
}

func TestPostDelete_AcceptsStringPostID(t *testing.T) {
	// Agent simulation: pass post_id as a JSON string.
	pf := newRecordingPlatform()
	pf.identity.Bindings = map[string]any{"jobs": float64(101)}
	ctx := newSocialCtx(t, pf)
	r, _ := ctx.AppDB().Exec(
		`INSERT INTO posts (project_id, body, status) VALUES ('test-proj', 'x', 'failed')`,
	)
	postID, _ := r.LastInsertId()
	app := &App{}
	out, err := app.toolPostDelete(ctx, map[string]any{
		"post_id": fmt.Sprintf("%d", postID), // string!
	})
	if err != nil {
		t.Fatal(err)
	}
	res, ok := out.(map[string]any)
	if !ok {
		t.Fatalf("unexpected response type: %T", out)
	}
	if res["isError"] == true {
		t.Errorf("expected success on string post_id, got %+v", res)
	}
	if res["deleted"] != postID {
		t.Errorf("delete id mismatch: %v vs %d", res["deleted"], postID)
	}
}
