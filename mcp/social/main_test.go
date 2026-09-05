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
//   - account_disconnect: preserves history + releases the last connection
//   - account_disconnect: keeps connection alive when siblings exist
//   - post_create: writes posts + post_targets, fans out, calls
//                   post_tweet, marks status=published
//   - post_create: marks failed targets as failed and rolls up to partial
//   - post_retry: resets failed → pending and re-publishes

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
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
	executeQueues    map[string][]*sdk.ExecuteResult
	callAppResponses map[string]json.RawMessage // keyed by "app:tool"
	connections      []sdk.PlatformConnection
	executeErr       error
	identity         *sdk.InstallIdentity
	urlPropertyReady *bool
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
		executeResponses: map[string]*sdk.ExecuteResult{"query_creator_info": {Success: true, Status: 200, Data: json.RawMessage(`{"data":{"privacy_level_options":["PUBLIC_TO_EVERYONE","SELF_ONLY"],"max_video_post_duration_sec":600},"error":{"code":"ok"}}`)}},
		executeQueues:    map[string][]*sdk.ExecuteResult{},
		callAppResponses: map[string]json.RawMessage{},
	}
}

func (p *recordingPlatform) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	return &sdk.PlatformConnection{ID: id, AppSlug: "twitter-api", ProjectID: "test-proj"}, nil
}
func (p *recordingPlatform) ListConnections(filter sdk.ConnectionFilter) ([]sdk.PlatformConnection, error) {
	out := []sdk.PlatformConnection{}
	for _, c := range p.connections {
		if filter.ProjectID != "" && c.ProjectID != filter.ProjectID {
			continue
		}
		if filter.AppSlug != "" && c.AppSlug != filter.AppSlug {
			continue
		}
		out = append(out, c)
	}
	return out, nil
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
	var queued *sdk.ExecuteResult
	if queue := p.executeQueues[tool]; len(queue) > 0 {
		queued = queue[0]
		p.executeQueues[tool] = queue[1:]
	}
	p.mu.Unlock()
	if p.executeErr != nil {
		return nil, p.executeErr
	}
	if queued != nil {
		return queued, nil
	}
	if r, ok := p.executeResponses[tool]; ok {
		return r, nil
	}
	return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{}`)}, nil
}

func (p *recordingPlatform) GetIntegrationURLProperty(int64, string) (*sdk.IntegrationURLPropertyStatus, error) {
	if p.urlPropertyReady == nil {
		return nil, tk.ErrNotImplemented
	}
	return &sdk.IntegrationURLPropertyStatus{Ready: *p.urlPropertyReady}, nil
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
	if id := toInt64Loose(input["id"]); id > 0 {
		if r, ok := p.callAppResponses[fmt.Sprintf("%s:%s:%d", appName, tool, id)]; ok {
			return r, nil
		}
	}
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

func TestAccountAdd_ReusedPickerConnectionPreservesProfileID(t *testing.T) {
	ctx := newSocialCtx(t, newRecordingPlatform())
	app := &App{}

	defaultOut, err := app.toolProfileCreate(ctx, map[string]any{"name": "Default Brand"})
	if err != nil {
		t.Fatal(err)
	}
	defaultProfile := defaultOut.(map[string]any)["profile"].(*Profile)
	selectedOut, err := app.toolProfileCreate(ctx, map[string]any{"name": "Selected Brand"})
	if err != nil {
		t.Fatal(err)
	}
	selectedProfile := selectedOut.(map[string]any)["profile"].(*Profile)

	_, err = ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, display_name, status, profile_id)
		 VALUES ('test-proj', 'facebook', 42, 'Existing Page', 'active', ?)`,
		defaultProfile.ID,
	)
	if err != nil {
		t.Fatal(err)
	}

	out, err := app.toolAccountAdd(ctx, map[string]any{
		"platform":   "facebook",
		"profile_id": selectedProfile.ID,
	})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(map[string]any)
	if res["reused_connection"] != int64(42) {
		t.Fatalf("expected reused connection 42, got %+v", res)
	}
	pendingID := res["pending_account_id"].(int64)

	var gotProfileID int64
	if err := ctx.AppDB().QueryRow(
		`SELECT profile_id FROM pending_accounts WHERE id=?`,
		pendingID,
	).Scan(&gotProfileID); err != nil {
		t.Fatal(err)
	}
	if gotProfileID != selectedProfile.ID {
		t.Fatalf("pending profile_id = %d, want selected profile %d", gotProfileID, selectedProfile.ID)
	}
}

func TestHandleAccountsStart_ForwardsProfileID(t *testing.T) {
	ctx := newSocialCtx(t, newRecordingPlatform())
	app := &App{}

	defaultOut, err := app.toolProfileCreate(ctx, map[string]any{"name": "Default Brand"})
	if err != nil {
		t.Fatal(err)
	}
	defaultProfile := defaultOut.(map[string]any)["profile"].(*Profile)
	selectedOut, err := app.toolProfileCreate(ctx, map[string]any{"name": "Selected Brand"})
	if err != nil {
		t.Fatal(err)
	}
	selectedProfile := selectedOut.(map[string]any)["profile"].(*Profile)

	_, err = ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, display_name, status, profile_id)
		 VALUES ('test-proj', 'facebook', 42, 'Existing Page', 'active', ?)`,
		defaultProfile.ID,
	)
	if err != nil {
		t.Fatal(err)
	}

	body := fmt.Sprintf(`{"platform":"facebook","profile_id":%d}`, selectedProfile.ID)
	req := httptest.NewRequest(http.MethodPost, "/accounts/start?project_id=test-proj", strings.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rr := httptest.NewRecorder()
	app.handleAccountsStart(rr, req)
	if rr.Code != http.StatusOK {
		t.Fatalf("status = %d, body = %s", rr.Code, rr.Body.String())
	}

	var res map[string]any
	if err := json.Unmarshal(rr.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	pendingID := int64(res["pending_account_id"].(float64))
	var gotProfileID int64
	if err := ctx.AppDB().QueryRow(
		`SELECT profile_id FROM pending_accounts WHERE id=?`,
		pendingID,
	).Scan(&gotProfileID); err != nil {
		t.Fatal(err)
	}
	if gotProfileID != selectedProfile.ID {
		t.Fatalf("pending profile_id = %d, want selected profile %d", gotProfileID, selectedProfile.ID)
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
	if r["avatar_url"] != "" {
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

func TestAccountFinalize_FacebookLegacyExpiryPreservesProfile(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["list_pages"] = &sdk.ExecuteResult{
		Success: true,
		Data:    json.RawMessage(`{"data":[{"id":"100","name":"Clara Lance"}]}`),
	}
	ctx := newSocialCtx(t, pf)
	profile, err := ctx.AppDB().Exec(
		`INSERT INTO profiles (project_id, name, slug, is_default) VALUES ('test-proj', 'Clara Lance', 'clara-lance', 0)`,
	)
	if err != nil {
		t.Fatal(err)
	}
	profileID, _ := profile.LastInsertId()
	legacyExpiry := time.Now().UTC().Add(10 * time.Minute)
	pending, err := ctx.AppDB().Exec(
		`INSERT INTO pending_accounts
		   (project_id, platform, integration_slug, connection_id, status, expires_at, profile_id)
		 VALUES ('test-proj', 'facebook', 'facebook-api', 42, 'ready', ?, ?)`,
		legacyExpiry, profileID,
	)
	if err != nil {
		t.Fatal(err)
	}
	pendingID, _ := pending.LastInsertId()

	out, err := (&App{}).toolAccountFinalize(ctx, map[string]any{
		"pending_account_id": pendingID,
		"page_id":            "100",
	})
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	if result["social_account_id"] == nil {
		t.Fatalf("finalize result = %+v", result)
	}
	var gotProfileID int64
	if err := ctx.AppDB().QueryRow(
		`SELECT profile_id FROM social_accounts WHERE id=?`, result["social_account_id"],
	).Scan(&gotProfileID); err != nil {
		t.Fatal(err)
	}
	if gotProfileID != profileID {
		t.Fatalf("profile_id = %d, want %d", gotProfileID, profileID)
	}
}

func TestPendingAccountExpiredSupportsCurrentAndLegacyFormats(t *testing.T) {
	now := time.Date(2026, 7, 14, 14, 0, 0, 0, time.UTC)
	tests := []struct {
		name    string
		raw     string
		expired bool
	}{
		{name: "rfc3339 future", raw: pendingExpiry(now.Add(time.Minute)), expired: false},
		{name: "rfc3339 past", raw: pendingExpiry(now.Add(-time.Minute)), expired: true},
		{name: "legacy future", raw: now.Add(time.Minute).String(), expired: false},
		{name: "legacy past", raw: now.Add(-time.Minute).String(), expired: true},
		{name: "sqlite future", raw: "2026-07-14 14:01:00", expired: false},
		{name: "invalid fails closed", raw: "not-a-time", expired: true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := pendingAccountExpired(tt.raw, now); got != tt.expired {
				t.Fatalf("pendingAccountExpired(%q) = %v, want %v", tt.raw, got, tt.expired)
			}
		})
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

func TestAccountCheck_TwitterProfileOK(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DB_PATH", dir+"/app.db")
	body := []byte{0xff, 0xd8, 0xff, 0xe0, 0x42}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/jpeg")
		w.Write(body)
	}))
	defer srv.Close()

	pf := newRecordingPlatform()
	pf.executeResponses["get_me"] = &sdk.ExecuteResult{
		Success: true,
		Data:    json.RawMessage(fmt.Sprintf(`{"data":{"id":"u1","username":"me","profile_image_url":%q}}`, srv.URL+"/avatar.jpg")),
	}
	ctx := newSocialCtx(t, pf)
	r, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, display_name, status)
		 VALUES ('test-proj', 'twitter', 42, 'old-name', 'active')`,
	)
	acctID, _ := r.LastInsertId()

	app := &App{avatarClient: srv.Client()}
	out, err := app.toolAccountCheck(ctx, map[string]any{"social_account_id": acctID})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(accountCheckResult)
	if res.Status != "ok" {
		t.Fatalf("status = %q, error=%q", res.Status, res.Error)
	}
	if len(pf.executeCalls) != 1 || pf.executeCalls[0].Tool != "get_me" {
		t.Fatalf("calls = %+v", pf.executeCalls)
	}
	if got := pf.executeCalls[0].Input["user.fields"]; !strings.Contains(fmt.Sprint(got), "profile_image_url") {
		t.Fatalf("get_me missing profile_image_url field request: %+v", pf.executeCalls[0].Input)
	}
	var status, displayName, avatarURL string
	ctx.AppDB().QueryRow(`SELECT last_check_status, display_name, COALESCE(avatar_url,'') FROM social_accounts WHERE id=?`, acctID).Scan(&status, &displayName, &avatarURL)
	if status != "ok" {
		t.Errorf("last_check_status = %q", status)
	}
	if displayName != "me" {
		t.Errorf("display_name = %q, want me", displayName)
	}
	if !strings.HasPrefix(avatarURL, "/api/apps/social/avatars/") {
		t.Fatalf("avatar_url was not cached: %q", avatarURL)
	}
}

func TestAccountCheck_FacebookRefreshesPageToken(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["list_pages"] = &sdk.ExecuteResult{
		Success: true,
		Data:    json.RawMessage(`{"data":[{"id":"100","name":"My Page","access_token":"new-page-token"}]}`),
	}
	ctx := newSocialCtx(t, pf)
	r, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, external_account_id, display_name, status, page_credentials)
		 VALUES ('test-proj', 'facebook', 42, '100', 'My Page', 'active', '{"access_token":"old"}')`,
	)
	acctID, _ := r.LastInsertId()

	app := &App{}
	out, err := app.toolAccountCheck(ctx, map[string]any{"social_account_id": acctID})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(accountCheckResult)
	if res.Status != "ok" {
		t.Fatalf("status = %q, error=%q", res.Status, res.Error)
	}
	var creds string
	ctx.AppDB().QueryRow(`SELECT page_credentials FROM social_accounts WHERE id=?`, acctID).Scan(&creds)
	if !strings.Contains(creds, "new-page-token") {
		t.Errorf("page_credentials was not refreshed: %s", creds)
	}
}

func TestAccountCheck_FacebookMissingPageFails(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["list_pages"] = &sdk.ExecuteResult{
		Success: true,
		Data:    json.RawMessage(`{"data":[{"id":"200","name":"Other Page","access_token":"token"}]}`),
	}
	ctx := newSocialCtx(t, pf)
	r, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, external_account_id, display_name, status)
		 VALUES ('test-proj', 'facebook', 42, '100', 'My Page', 'active')`,
	)
	acctID, _ := r.LastInsertId()

	app := &App{}
	out, err := app.toolAccountCheck(ctx, map[string]any{"social_account_id": acctID})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(accountCheckResult)
	if res.Status != "failed" || !strings.Contains(res.Error, "no longer accessible") {
		t.Fatalf("unexpected result: %+v", res)
	}
	var status, lastErr string
	ctx.AppDB().QueryRow(`SELECT last_check_status, last_check_error FROM social_accounts WHERE id=?`, acctID).Scan(&status, &lastErr)
	if status != "failed" || !strings.Contains(lastErr, "no longer accessible") {
		t.Errorf("persisted check = %q %q", status, lastErr)
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
		"mode":               "publish",
		"body":               "hello world",
		"social_account_ids": []any{acctID},
	})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(map[string]any)
	postID := res["post_id"].(int64)
	if res["status"] != "published" {
		t.Fatalf("post_create status = %v, want published", res["status"])
	}
	if res["target_count"] != 1 {
		t.Fatalf("post_create target_count = %v, want 1", res["target_count"])
	}
	targets, ok := res["targets"].([]map[string]any)
	if !ok || len(targets) != 1 {
		t.Fatalf("post_create targets = %#v, want one detailed target", res["targets"])
	}
	if targets[0]["platform_post_id"] != "123" {
		t.Fatalf("post_create target platform_post_id = %v, want 123", targets[0]["platform_post_id"])
	}
	if purl, _ := targets[0]["platform_url"].(string); !strings.Contains(purl, "/status/123") {
		t.Fatalf("post_create target platform_url = %q, want twitter status URL", purl)
	}

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

func TestPostCreateRequiresExplicitModeWithoutSideEffects(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newSocialCtx(t, pf)
	out, err := (&App{}).toolPostCreate(ctx, map[string]any{"body": "do not publish"})
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	if result["isError"] != true || !strings.Contains(fmt.Sprint(result), "mode required") {
		t.Fatalf("missing mode result = %+v", result)
	}
	var posts int
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM posts`).Scan(&posts)
	if posts != 0 || len(pf.executeCalls) != 0 {
		t.Fatalf("missing mode had side effects: posts=%d calls=%+v", posts, pf.executeCalls)
	}
}

func TestDraftLifecycleSupportsMediaAndOptionalApproval(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["post_tweet"] = &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"data":{"id":"draft-published"}}`)}
	ctx := newSocialCtx(t, pf)
	account, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, display_name, status)
		 VALUES ('test-proj', 'twitter', 42, '@me', 'active')`,
	)
	accountID, _ := account.LastInsertId()
	app := &App{}
	created, err := app.toolPostDraftCreate(ctx, map[string]any{
		"body":               "review me",
		"social_account_ids": []any{accountID},
		"media_storage_ids":  []any{int64(71)},
		"media_project_id":   "media-project",
		"approval_required":  true,
	})
	if err != nil {
		t.Fatal(err)
	}
	draft := created.(map[string]any)
	postID := draft["id"].(int64)
	if draft["status"] != "draft" || draft["revision"] != int64(1) || len(pf.executeCalls) != 0 {
		t.Fatalf("draft create = %+v; calls=%+v", draft, pf.executeCalls)
	}
	media := draft["media_storage_ids"].([]int64)
	if len(media) != 1 || media[0] != 71 {
		t.Fatalf("draft media = %+v", media)
	}
	if _, err := app.toolPostDraftSubmit(ctx, map[string]any{"post_id": postID, "expected_revision": 1, "_caller": "agent:writer"}); err != nil {
		t.Fatal(err)
	}
	approved, err := app.toolPostDraftApprove(ctx, map[string]any{"post_id": postID, "expected_revision": 1, "_caller": "human:reviewer"})
	if err != nil {
		t.Fatal(err)
	}
	if approved.(map[string]any)["status"] != "approved" {
		t.Fatalf("approved = %+v", approved)
	}
	// Remove the media before delivery so this unit test does not need a Storage binding.
	updated, err := app.toolPostDraftUpdate(ctx, map[string]any{
		"post_id": postID, "expected_revision": 1, "media_storage_ids": []any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if updated.(map[string]any)["status"] != "draft" || updated.(map[string]any)["revision"] != int64(2) {
		t.Fatalf("updated draft = %+v", updated)
	}
	blocked, err := app.toolPostDraftPublish(ctx, map[string]any{"post_id": postID, "expected_revision": 2, "mode": "publish"})
	if err != nil {
		t.Fatal(err)
	}
	if blocked.(map[string]any)["isError"] != true {
		t.Fatalf("edited approved draft should require reapproval: %+v", blocked)
	}
	if _, err := app.toolPostDraftSubmit(ctx, map[string]any{"post_id": postID, "expected_revision": 2}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolPostDraftApprove(ctx, map[string]any{"post_id": postID, "expected_revision": 2}); err != nil {
		t.Fatal(err)
	}
	published, err := app.toolPostDraftPublish(ctx, map[string]any{"post_id": postID, "expected_revision": 2, "mode": "publish"})
	if err != nil {
		t.Fatal(err)
	}
	if published.(map[string]any)["status"] != "published" || len(pf.executeCalls) != 1 {
		t.Fatalf("published draft = %+v; calls=%+v", published, pf.executeCalls)
	}
	if reviews := published.(map[string]any)["reviews"].([]map[string]any); len(reviews) != 4 {
		t.Fatalf("reviews = %+v", reviews)
	}
}

func TestDraftCanScheduleDirectlyWithoutApproval(t *testing.T) {
	ctx := newSocialCtx(t, newRecordingPlatform())
	account, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, display_name, status)
		 VALUES ('test-proj', 'twitter', 42, '@me', 'active')`,
	)
	accountID, _ := account.LastInsertId()
	app := &App{}
	created, err := app.toolPostDraftCreate(ctx, map[string]any{"body": "later", "social_account_ids": []any{accountID}})
	if err != nil {
		t.Fatal(err)
	}
	postID := created.(map[string]any)["id"].(int64)
	out, err := app.toolPostDraftPublish(ctx, map[string]any{
		"post_id": postID, "expected_revision": 1, "mode": "schedule", "schedule_at": "2026-09-01T10:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["status"] != "scheduled" {
		t.Fatalf("direct draft schedule = %+v", out)
	}
}

func TestPostList_DoesNotDeadlockLoadingTargets(t *testing.T) {
	ctx := newSocialCtx(t, nil)
	ctx.AppDB().SetMaxOpenConns(1)
	acctRes, err := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, display_name, status)
		 VALUES ('test-proj', 'linkedin', 42, 'AgentDojo', 'active')`,
	)
	if err != nil {
		t.Fatal(err)
	}
	acctID, _ := acctRes.LastInsertId()
	postRes, err := ctx.AppDB().Exec(
		`INSERT INTO posts (project_id, body, status, external_media_urls)
		 VALUES ('test-proj', 'hello', 'published', '["https://cdn.test/thumb.jpg"]')`,
	)
	if err != nil {
		t.Fatal(err)
	}
	postID, _ := postRes.LastInsertId()
	if _, err := ctx.AppDB().Exec(
		`INSERT INTO post_targets (post_id, social_account_id, status, platform_post_id, attempts)
		 VALUES (?, ?, 'published', 'urn:li:share:1', 1)`,
		postID, acctID,
	); err != nil {
		t.Fatal(err)
	}

	done := make(chan any, 1)
	go func() {
		out, err := (&App{}).toolPostList(ctx, map[string]any{})
		if err != nil {
			done <- err
			return
		}
		done <- out
	}()
	select {
	case got := <-done:
		if err, ok := got.(error); ok {
			t.Fatal(err)
		}
		posts := got.(map[string]any)["posts"].([]map[string]any)
		if len(posts) != 1 {
			t.Fatalf("posts len = %d, want 1", len(posts))
		}
		targets := posts[0]["targets"].([]map[string]any)
		if len(targets) != 1 || targets[0]["social_account_id"].(int64) != acctID {
			t.Fatalf("targets = %+v", targets)
		}
		extMedia := posts[0]["external_media_urls"].([]string)
		if len(extMedia) != 1 || extMedia[0] != "https://cdn.test/thumb.jpg" {
			t.Fatalf("external_media_urls = %+v", extMedia)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("toolPostList deadlocked while loading targets")
	}
}

func TestPostList_DateRangeUsesLifecycleDate(t *testing.T) {
	ctx := newSocialCtx(t, nil)
	insert := func(body, status, createdAt, scheduleAt, publishedAt string) int64 {
		t.Helper()
		res, err := ctx.AppDB().Exec(
			`INSERT INTO posts (project_id, body, status, created_at, schedule_at, published_at)
			 VALUES ('test-proj', ?, ?, ?, NULLIF(?, ''), NULLIF(?, ''))`,
			body, status, createdAt, scheduleAt, publishedAt,
		)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}

	scheduledID := insert("scheduled", "scheduled", "2026-06-01 10:00:00", "2026-07-15T10:00:00Z", "")
	publishedID := insert("published", "published", "2026-06-01 10:00:00", "2026-07-01T10:00:00Z", "2026-07-16T10:00:00Z")
	draftID := insert("draft", "draft", "2026-07-17 10:00:00", "", "")
	failedID := insert("failed", "failed", "2026-06-01 10:00:00", "2026-07-18T10:00:00Z", "")
	_ = insert("outside", "scheduled", "2026-06-01 10:00:00", "2026-07-19T10:00:00Z", "")

	out, err := (&App{}).listPosts(ctx, map[string]any{
		"from":  "2026-07-15T00:00:00Z",
		"to":    "2026-07-19T00:00:00Z",
		"limit": 1000,
	}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	posts := out.(map[string]any)["posts"].([]map[string]any)
	got := map[int64]bool{}
	for _, post := range posts {
		got[post["id"].(int64)] = true
	}
	for _, id := range []int64{scheduledID, publishedID, draftID, failedID} {
		if !got[id] {
			t.Errorf("post %d missing from lifecycle date range: %+v", id, got)
		}
	}
	if len(got) != 4 {
		t.Fatalf("range returned %d posts, want 4: %+v", len(got), got)
	}
}

func TestPostList_OrdersByLifecycleDateBeforeLimit(t *testing.T) {
	ctx := newSocialCtx(t, nil)
	for _, row := range []struct {
		body, publishedAt string
	}{
		{body: "newer upstream post", publishedAt: "2026-03-16T10:00:37Z"},
		{body: "older imported later", publishedAt: "2026-02-26T14:33:46Z"},
	} {
		if _, err := ctx.AppDB().Exec(
			`INSERT INTO posts (project_id, body, status, created_at, published_at, source)
			 VALUES ('test-proj', ?, 'published', ?, ?, 'provider')`,
			row.body, row.publishedAt, row.publishedAt,
		); err != nil {
			t.Fatal(err)
		}
	}

	out, err := (&App{}).listPosts(ctx, map[string]any{"limit": 1}, 1000)
	if err != nil {
		t.Fatal(err)
	}
	posts := out.(map[string]any)["posts"].([]map[string]any)
	if len(posts) != 1 || posts[0]["body"] != "newer upstream post" {
		t.Fatalf("ordered posts = %+v, want newer upstream publication first", posts)
	}
}

func TestPostList_DateRangeRejectsInvalidBounds(t *testing.T) {
	ctx := newSocialCtx(t, nil)
	for _, args := range []map[string]any{
		{"from": "not-a-date"},
		{"from": "2026-07-20T00:00:00Z", "to": "2026-07-19T00:00:00Z"},
	} {
		out, err := (&App{}).listPosts(ctx, args, 1000)
		if err != nil {
			t.Fatal(err)
		}
		if out.(map[string]any)["isError"] != true {
			t.Fatalf("invalid bounds were accepted: args=%+v out=%+v", args, out)
		}
	}
}

func TestLoadPostByID_IsProjectScopedAndIncludesTargets(t *testing.T) {
	ctx := newSocialCtx(t, nil)
	accountResult, err := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, display_name, status)
		 VALUES ('test-proj', 'youtube', 42, 'Channel', 'active')`,
	)
	if err != nil {
		t.Fatal(err)
	}
	accountID, _ := accountResult.LastInsertId()
	postResult, err := ctx.AppDB().Exec(
		`INSERT INTO posts (project_id, body, media_storage_ids, external_media_urls, status, schedule_at)
		 VALUES ('test-proj', 'Scheduled video', '[17]', '[]', 'scheduled', '2026-07-20T18:00:00Z')`,
	)
	if err != nil {
		t.Fatal(err)
	}
	postID, _ := postResult.LastInsertId()
	if _, err := ctx.AppDB().Exec(
		`INSERT INTO post_targets (post_id, social_account_id, status) VALUES (?, ?, 'pending')`,
		postID, accountID,
	); err != nil {
		t.Fatal(err)
	}

	post, err := (&App{}).loadPostByID(ctx, "test-proj", postID)
	if err != nil {
		t.Fatal(err)
	}
	if post["body"] != "Scheduled video" || post["status"] != "scheduled" {
		t.Fatalf("unexpected post: %+v", post)
	}
	targets, _ := post["targets"].([]map[string]any)
	if len(targets) != 1 || targets[0]["social_account_id"] != accountID {
		t.Fatalf("unexpected targets: %+v", post["targets"])
	}
	if _, err := (&App{}).loadPostByID(ctx, "other-project", postID); err == nil {
		t.Fatal("cross-project post lookup unexpectedly succeeded")
	}
}

func TestAccountMetrics_OmitsRawAndHonorsHistoryPeriod(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["get_my_channel"] = &sdk.ExecuteResult{
		Success: true,
		Status:  200,
		Data: json.RawMessage(`{
			"items":[{
				"statistics":{"viewCount":"123","subscriberCount":"45","videoCount":"6"},
				"snippet":{"title":"Test channel","description":"provider-only detail"}
			}]
		}`),
	}
	ctx := newSocialCtx(t, pf)
	inserted, err := ctx.AppDB().Exec(
		`INSERT INTO social_accounts
		   (project_id, platform, connection_id, external_account_id, display_name, status)
		 VALUES ('test-proj', 'youtube', 42, 'channel_1', 'Test channel', 'active')`,
	)
	if err != nil {
		t.Fatal(err)
	}
	accountID, _ := inserted.LastInsertId()

	now := time.Now().UTC()
	for _, point := range []struct {
		age   time.Duration
		value int64
	}{
		{age: 3 * 24 * time.Hour, value: 40},
		{age: 20 * 24 * time.Hour, value: 30},
	} {
		if err := insertSocialMetricPoint(
			ctx, "test-proj", 0, accountID, 0, 0, "youtube", "account",
			"followers", "snapshot", now.Add(-point.age).Format(time.RFC3339),
			point.value, "test", "ok", "",
		); err != nil {
			t.Fatal(err)
		}
	}

	out, err := (&App{}).toolAccountMetrics(ctx, map[string]any{
		"social_account_id": accountID,
		"period":            "7d",
	})
	if err != nil {
		t.Fatal(err)
	}
	metrics := out.(accountMetricsResult)
	if metrics.UpdatedAt == "" {
		t.Fatal("fresh metrics response omitted updated_at")
	}
	if _, leaked := metrics.Insights["_refresh"]; leaked {
		t.Fatalf("refresh heartbeat leaked into insights: %+v", metrics.Insights)
	}
	if len(metrics.Raw) != 0 {
		t.Fatalf("raw provider data returned without include_raw: %d bytes", len(metrics.Raw))
	}
	encoded, err := json.Marshal(metrics)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(encoded), `"raw"`) {
		t.Fatalf("default response still contains a raw field: %s", encoded)
	}
	cutoff := now.Add(-7 * 24 * time.Hour)
	for _, point := range metrics.Insights["followers"] {
		parsed, ok := parseMetricPointTime(point.Time)
		if !ok || parsed.Before(cutoff) {
			t.Fatalf("period=7d returned out-of-window point: %+v", point)
		}
	}

	debugOut, err := (&App{}).toolAccountMetrics(ctx, map[string]any{
		"social_account_id": accountID,
		"period":            "7d",
		"include_raw":       true,
	})
	if err != nil {
		t.Fatal(err)
	}
	debugMetrics := debugOut.(accountMetricsResult)
	if len(debugMetrics.Raw) == 0 || !strings.Contains(string(debugMetrics.Raw), "provider-only detail") {
		t.Fatalf("include_raw did not return sanitized provider detail: %s", debugMetrics.Raw)
	}
}

func TestAnalyticsCollector_GlobalInstallRefreshesEveryProjectAndEmitsScopedEvents(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "")
	pf := newRecordingPlatform()
	pf.executeResponses["get_me"] = &sdk.ExecuteResult{
		Success: true,
		Status:  http.StatusOK,
		Data:    json.RawMessage(`{"data":{"username":"test","public_metrics":{"followers_count":10,"following_count":2,"tweet_count":4}}}`),
	}
	rec := tk.NewEmitRecorder()
	ctx := tk.NewAppCtx(t, "apteva.yaml",
		tk.WithProjectID(""),
		tk.WithPlatform(pf),
		tk.WithEmitter(rec),
	)
	globalCtx = ctx
	for _, pid := range []string{"project-a", "project-b"} {
		if _, err := ctx.AppDB().Exec(
			`INSERT INTO social_accounts
			   (project_id, platform, connection_id, display_name, status)
			 VALUES (?, 'twitter', 77, ?, 'active')`,
			pid, pid,
		); err != nil {
			t.Fatal(err)
		}
	}

	if err := (&App{}).runAnalyticsCollector(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	var refreshedProjects int
	if err := ctx.AppDB().QueryRow(
		`SELECT COUNT(DISTINCT project_id) FROM social_metric_points
		  WHERE metric='_refresh' AND period='heartbeat'`,
	).Scan(&refreshedProjects); err != nil {
		t.Fatal(err)
	}
	if refreshedProjects != 2 {
		t.Fatalf("refreshed projects = %d, want 2", refreshedProjects)
	}
	events := rec.EventsByTopic("metrics.updated")
	if len(events) != 2 || events[0].ProjectID != "project-a" || events[1].ProjectID != "project-b" {
		t.Fatalf("metrics events were not project scoped: %+v", events)
	}
	if len(pf.executeCalls) != 2 {
		t.Fatalf("provider calls = %d, want 2", len(pf.executeCalls))
	}

	if err := (&App{}).runAnalyticsCollector(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if len(pf.executeCalls) != 2 {
		t.Fatalf("fresh accounts were collected again: calls=%d", len(pf.executeCalls))
	}
}

func TestAccountMetrics_RejectsInvalidHistoryPeriod(t *testing.T) {
	ctx := newSocialCtx(t, nil)
	for _, period := range []string{"week", "0d", "731d", "-2d"} {
		out, err := (&App{}).toolAccountMetrics(ctx, map[string]any{
			"social_account_id": 1,
			"period":            period,
		})
		if err != nil {
			t.Fatal(err)
		}
		if out.(map[string]any)["isError"] != true {
			t.Fatalf("invalid period %q was accepted: %+v", period, out)
		}
	}
}

func TestAccountMetrics_ZernioGenericProviderUsesCurrentPayloadShape(t *testing.T) {
	pf := newRecordingPlatform()
	now := time.Now().UTC()
	day1 := now.AddDate(0, 0, -2).Format("2006-01-02")
	day2 := now.AddDate(0, 0, -1).Format("2006-01-02")
	pf.executeResponses["get_daily_metrics"] = &sdk.ExecuteResult{
		Success: true,
		Status:  http.StatusOK,
		Data: json.RawMessage(fmt.Sprintf(`{
			"dailyData":[
				{"date":%q,"postCount":2,"platforms":{"linkedin":2},"metrics":{"impressions":100,"reach":80,"likes":7,"comments":2,"shares":1,"views":10}},
				{"date":%q,"postCount":1,"platforms":{"linkedin":1},"metrics":{"impressions":50,"reach":40,"likes":3,"comments":0,"shares":0,"views":5}}
			],
			"platformBreakdown":[]
		}`, day1, day2)),
	}
	pf.executeResponses["get_follower_stats"] = &sdk.ExecuteResult{
		Success: true,
		Status:  http.StatusOK,
		Data: json.RawMessage(fmt.Sprintf(`{
			"accounts":[{"_id":"za_linkedin","platform":"linkedin","currentFollowers":1250}],
			"stats":{"za_linkedin":[{"date":%q,"followers":1230},{"date":%q,"followers":1250}]},
			"granularity":"daily"
		}`, day1, day2)),
	}
	ctx := newSocialCtx(t, pf)
	inserted, err := ctx.AppDB().Exec(
		`INSERT INTO social_accounts
		   (project_id, platform, connection_id, display_name, status, provider_slug, provider_account_id)
		 VALUES ('test-proj', 'facebook', 77, 'Company', 'active', 'zernio', 'za_linkedin')`,
	)
	if err != nil {
		t.Fatal(err)
	}
	accountID, _ := inserted.LastInsertId()

	out, err := (&App{}).toolAccountMetrics(ctx, map[string]any{
		"social_account_id": accountID,
		"period":            "30d",
	})
	if err != nil {
		t.Fatal(err)
	}
	metrics := out.(accountMetricsResult)
	if metrics.Status != "ok" || metrics.Followers != 1250 || metrics.Posts != 3 || metrics.Impressions != 150 || metrics.Reach != 120 || metrics.Views != 15 || metrics.Likes != 10 || metrics.Comments != 2 || metrics.Shares != 1 || metrics.Engagements != 13 {
		t.Fatalf("unexpected zernio account metrics: %+v", metrics)
	}
	if metrics.HistorySource != "social_metric_points" || len(metrics.Insights["followers"]) < 2 || len(metrics.Insights["impressions"]) != 2 || len(metrics.Insights["comments"]) != 2 || metrics.Insights["comments"][1].Value != 0 {
		t.Fatalf("provider history was not normalized and persisted: %+v", metrics.Insights)
	}
	if len(metrics.Raw) != 0 {
		t.Fatalf("raw provider response leaked by default: %s", metrics.Raw)
	}
	if len(pf.executeCalls) != 2 || pf.executeCalls[0].Tool != "get_daily_metrics" || pf.executeCalls[1].Tool != "get_follower_stats" {
		t.Fatalf("unexpected provider calls: %+v", pf.executeCalls)
	}
	if pf.executeCalls[0].Input["accountId"] != "za_linkedin" || pf.executeCalls[0].Input["attribution"] != "received" {
		t.Fatalf("daily metrics input = %+v", pf.executeCalls[0].Input)
	}
	if pf.executeCalls[1].Input["granularity"] != "daily" {
		t.Fatalf("follower metrics input = %+v", pf.executeCalls[1].Input)
	}
}

func TestAccountMetrics_ZernioLinkedInUsesAggregateAnalytics(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeQueues["get_linkedin_aggregate_analytics"] = []*sdk.ExecuteResult{
		{
			Success: true,
			Status:  http.StatusOK,
			Data: json.RawMessage(`{
				"accountId":"za_linkedin","accountType":"personal","aggregation":"TOTAL",
				"analytics":{"impressions":160,"reach":110,"reactions":2,"comments":1,"shares":3,"saves":4,"sends":5,"engagementRate":1.25}
			}`),
		},
		{
			Success: true,
			Status:  http.StatusOK,
			Data: json.RawMessage(`{
				"accountId":"za_linkedin","accountType":"personal","aggregation":"DAILY",
				"analytics":{
					"impressions":[{"date":"2026-07-15","count":60},{"date":"2026-07-16","count":100}],
					"reactions":[{"date":"2026-07-15","count":0},{"date":"2026-07-16","count":2}]
				}
			}`),
		},
	}
	pf.executeResponses["get_follower_stats"] = &sdk.ExecuteResult{
		Success: true,
		Status:  http.StatusOK,
		Data:    json.RawMessage(`{"accounts":[{"_id":"za_linkedin","currentFollowers":659}],"stats":{"za_linkedin":[]}}`),
	}
	ctx := newSocialCtx(t, pf)
	inserted, err := ctx.AppDB().Exec(
		`INSERT INTO social_accounts
		   (project_id, platform, connection_id, display_name, status, provider_slug, provider_account_id)
		 VALUES ('test-proj', 'linkedin', 77, 'Marco Schwartz', 'active', 'zernio', 'za_linkedin')`,
	)
	if err != nil {
		t.Fatal(err)
	}
	accountID, _ := inserted.LastInsertId()

	out, err := (&App{}).toolAccountMetrics(ctx, map[string]any{"social_account_id": accountID, "period": "30d"})
	if err != nil {
		t.Fatal(err)
	}
	metrics := out.(accountMetricsResult)
	if metrics.Status != "ok" || metrics.Followers != 659 || metrics.Impressions != 160 || metrics.Reach != 110 || metrics.Likes != 2 || metrics.Comments != 1 || metrics.Shares != 3 || metrics.Saves != 4 || metrics.Sends != 5 || metrics.Engagements != 15 || metrics.EngagementRate != 1.25 {
		t.Fatalf("unexpected LinkedIn aggregate metrics: %+v", metrics)
	}
	if len(metrics.Insights["impressions"]) != 2 || len(metrics.Insights["likes"]) != 2 {
		t.Fatalf("LinkedIn daily analytics were not normalized: %+v", metrics.Insights)
	}
	if len(metrics.Raw) != 0 {
		t.Fatalf("raw provider response leaked by default: %s", metrics.Raw)
	}
	if len(pf.executeCalls) != 3 || pf.executeCalls[0].Tool != "get_linkedin_aggregate_analytics" || pf.executeCalls[1].Tool != "get_linkedin_aggregate_analytics" || pf.executeCalls[2].Tool != "get_follower_stats" {
		t.Fatalf("unexpected provider calls: %+v", pf.executeCalls)
	}
	if pf.executeCalls[0].Input["aggregation"] != "TOTAL" || pf.executeCalls[1].Input["aggregation"] != "DAILY" {
		t.Fatalf("unexpected LinkedIn analytics inputs: %+v", pf.executeCalls)
	}
}

func TestAccountMetrics_ZernioLinkedInFallsBackToOrganizationAnalytics(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["get_linkedin_aggregate_analytics"] = &sdk.ExecuteResult{
		Success: false,
		Status:  http.StatusBadRequest,
		Data:    json.RawMessage(`{"code":"org_account_not_supported","error":"Use organization analytics"}`),
	}
	pf.executeQueues["get_linkedin_org_aggregate_analytics"] = []*sdk.ExecuteResult{
		{
			Success: true,
			Status:  http.StatusOK,
			Data: json.RawMessage(`{"metrics":{
				"impressions":{"total":900},"unique_impressions":{"total":700},"clicks":{"total":20},
				"likes":{"total":30},"comments":{"total":4},"shares":{"total":2},
				"engagement_rate":{"total":0.0622},"page_views_total":{"total":50}
			}}`),
		},
		{
			Success: true,
			Status:  http.StatusOK,
			Data: json.RawMessage(`{"metrics":{
				"impressions":{"total":900,"values":[{"date":"2026-07-15","value":400},{"date":"2026-07-16","value":500}]},
				"likes":{"total":30,"values":[{"date":"2026-07-15","value":10},{"date":"2026-07-16","value":20}]}
			}}`),
		},
	}
	pf.executeResponses["get_follower_stats"] = &sdk.ExecuteResult{
		Success: true,
		Status:  http.StatusOK,
		Data:    json.RawMessage(`{"accounts":[{"_id":"za_org","currentFollowers":5000}],"stats":{"za_org":[]}}`),
	}
	ctx := newSocialCtx(t, pf)
	inserted, err := ctx.AppDB().Exec(
		`INSERT INTO social_accounts
		   (project_id, platform, connection_id, display_name, status, provider_slug, provider_account_id)
		 VALUES ('test-proj', 'linkedin', 77, 'Organization', 'active', 'zernio', 'za_org')`,
	)
	if err != nil {
		t.Fatal(err)
	}
	accountID, _ := inserted.LastInsertId()

	out, err := (&App{}).toolAccountMetrics(ctx, map[string]any{"social_account_id": accountID, "period": "30d"})
	if err != nil {
		t.Fatal(err)
	}
	metrics := out.(accountMetricsResult)
	if metrics.Status != "ok" || metrics.Followers != 5000 || metrics.Impressions != 900 || metrics.Reach != 700 || metrics.Views != 50 || metrics.Clicks != 20 || metrics.Likes != 30 || metrics.Comments != 4 || metrics.Shares != 2 || metrics.Engagements != 56 || metrics.EngagementRate != 6.22 {
		t.Fatalf("unexpected LinkedIn organization metrics: %+v", metrics)
	}
	if len(metrics.Insights["impressions"]) != 2 || len(metrics.Insights["likes"]) != 2 {
		t.Fatalf("organization history was not normalized: %+v", metrics.Insights)
	}
	if len(pf.executeCalls) != 4 || pf.executeCalls[0].Tool != "get_linkedin_aggregate_analytics" || pf.executeCalls[1].Tool != "get_linkedin_org_aggregate_analytics" || pf.executeCalls[2].Tool != "get_linkedin_org_aggregate_analytics" || pf.executeCalls[3].Tool != "get_follower_stats" {
		t.Fatalf("unexpected organization analytics calls: %+v", pf.executeCalls)
	}
}

func TestPostMetrics_ZernioUsesProviderAdapterForLinkedIn(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["get_analytics"] = &sdk.ExecuteResult{
		Success: true,
		Status:  http.StatusOK,
		Data: json.RawMessage(`{
			"postId":"zp_123",
			"analytics":{"impressions":1000,"reach":800,"likes":20,"comments":4,"shares":3,"views":0},
			"platformAnalytics":[{
				"platform":"linkedin","platformPostId":"urn:li:share:456","accountId":"za_linkedin",
				"analytics":{"impressions":1000,"reach":800,"likes":20,"comments":4,"shares":3,"views":0}
			}]
		}`),
	}
	ctx := newSocialCtx(t, pf)
	accountResult, err := ctx.AppDB().Exec(
		`INSERT INTO social_accounts
		   (project_id, platform, connection_id, display_name, status, provider_slug, provider_account_id)
		 VALUES ('test-proj', 'linkedin', 77, 'Company', 'active', 'zernio', 'za_linkedin')`,
	)
	if err != nil {
		t.Fatal(err)
	}
	accountID, _ := accountResult.LastInsertId()
	postResult, err := ctx.AppDB().Exec(
		`INSERT INTO posts (project_id, body, status, published_at) VALUES ('test-proj', 'Provider post', 'published', datetime('now'))`,
	)
	if err != nil {
		t.Fatal(err)
	}
	postID, _ := postResult.LastInsertId()
	if _, err := ctx.AppDB().Exec(
		`INSERT INTO post_targets
		   (post_id, social_account_id, status, platform_post_id, provider_post_id)
		 VALUES (?, ?, 'published', 'urn:li:share:456', 'zp_123')`,
		postID, accountID,
	); err != nil {
		t.Fatal(err)
	}

	out, err := (&App{}).toolPostMetrics(ctx, map[string]any{"post_id": postID})
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	targets := result["targets"].([]targetMetricsOutcome)
	if len(targets) != 1 || targets[0].Status != "ok" || targets[0].Metrics == nil {
		t.Fatalf("unexpected provider post metrics: %+v", targets)
	}
	got := targets[0].Metrics
	if got.Views != 1000 || got.Likes != 20 || got.Comments != 4 || got.Shares != 3 {
		t.Fatalf("normalized provider post metrics = %+v", got)
	}
	if len(pf.executeCalls) != 1 || pf.executeCalls[0].Tool != "get_analytics" {
		t.Fatalf("unexpected provider calls: %+v", pf.executeCalls)
	}
	if pf.executeCalls[0].Input["postId"] != "zp_123" || pf.executeCalls[0].Input["accountId"] != "za_linkedin" {
		t.Fatalf("post analytics input = %+v", pf.executeCalls[0].Input)
	}
	var points int
	if err := ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM social_metric_points WHERE project_id='test-proj' AND post_id=? AND scope='post'`,
		postID,
	).Scan(&points); err != nil {
		t.Fatal(err)
	}
	if points != 5 {
		t.Fatalf("persisted metric points = %d, want 5 including refresh heartbeat", points)
	}
}

func TestPostMetrics_ZernioFallsBackToExternalPostID(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["get_analytics"] = &sdk.ExecuteResult{
		Success: true,
		Status:  http.StatusOK,
		Data:    json.RawMessage(`{"analytics":{"impressions":42,"likes":2,"comments":1,"shares":0}}`),
	}
	ctx := newSocialCtx(t, pf)
	out := (&App{}).getPostMetrics(ctx, metricsTarget{
		TargetID:          1,
		SocialAccountID:   2,
		ConnID:            77,
		Platform:          "linkedin",
		ExtPostID:         "urn:li:share:external",
		ProviderSlug:      "zernio",
		ProviderAccountID: "za_linkedin",
	})
	if out.Status != "ok" || out.Metrics == nil || out.Metrics.Views != 42 {
		t.Fatalf("external provider post metrics = %+v", out)
	}
	if len(pf.executeCalls) != 1 || pf.executeCalls[0].Input["postId"] != "urn:li:share:external" {
		t.Fatalf("external post id was not forwarded: %+v", pf.executeCalls)
	}
}

func TestAccountCheckAll_DoesNotDeadlock(t *testing.T) {
	ctx := newSocialCtx(t, nil)
	ctx.AppDB().SetMaxOpenConns(1)
	_, _ = ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, display_name, status)
		 VALUES ('test-proj', 'linkedin', 42, 'AgentDojo', 'active')`,
	)

	done := make(chan any, 1)
	go func() {
		out, err := (&App{}).toolAccountCheck(ctx, map[string]any{"all": true})
		if err != nil {
			done <- err
			return
		}
		done <- out
	}()
	select {
	case got := <-done:
		if err, ok := got.(error); ok {
			t.Fatal(err)
		}
		checks := got.(map[string]any)["checks"].([]accountCheckResult)
		if len(checks) != 1 {
			t.Fatalf("checks len = %d, want 1", len(checks))
		}
	case <-time.After(2 * time.Second):
		t.Fatal("toolAccountCheck(all=true) deadlocked")
	}
}

func TestAccountImportProvider_ZernioCreatesSocialAccounts(t *testing.T) {
	pf := newRecordingPlatform()
	pf.connections = []sdk.PlatformConnection{
		{ID: 99, AppSlug: "zernio", ProjectID: "test-proj", Status: "active"},
	}
	pf.executeResponses["list_accounts"] = &sdk.ExecuteResult{
		Success: true,
		Status:  200,
		Data: json.RawMessage(`{"accounts":[
			{"accountId":"za_1","profileId":{"_id":"zp_1","name":"Default Profile"},"platform":"linkedin","displayName":"Company Page","metadata":{"profileData":{"profilePicture":"https://example.com/a.png"}}},
			{"accountId":"za_2","profileId":"zp_1","platform":"twitter","displayName":"@brand"}
		]}`),
	}
	ctx := newSocialCtx(t, pf)
	app := &App{}
	out, err := app.toolAccountImportProvider(ctx, map[string]any{"provider": "zernio"})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(providerAccountImportResult)
	if res.Status != "ok" || res.Imported != 2 {
		t.Fatalf("unexpected import result: %+v", res)
	}
	var n int
	ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM social_accounts WHERE provider_slug='zernio' AND connection_id=99`,
	).Scan(&n)
	if n != 2 {
		t.Fatalf("provider-backed accounts = %d, want 2", n)
	}
	var platform, providerAccountID, providerProfileID, avatarURL string
	ctx.AppDB().QueryRow(
		`SELECT platform, provider_account_id, provider_profile_id, avatar_url FROM social_accounts WHERE display_name='Company Page'`,
	).Scan(&platform, &providerAccountID, &providerProfileID, &avatarURL)
	if platform != "linkedin" || providerAccountID != "za_1" || providerProfileID != "zp_1" || avatarURL != "https://example.com/a.png" {
		t.Fatalf("stored account = %s %s %s %s", platform, providerAccountID, providerProfileID, avatarURL)
	}
}

func TestZernioUsableAvatarURL_DropsExpiredLinkedInMedia(t *testing.T) {
	past := time.Now().Add(-time.Hour).Unix()
	future := time.Now().Add(48 * time.Hour).Unix()

	expired := fmt.Sprintf("https://media.licdn.com/dms/image/foo?e=%d&v=beta", past)
	if got := zernioUsableAvatarURL(expired); got != "" {
		t.Fatalf("expired LinkedIn avatar = %q, want blank", got)
	}

	valid := fmt.Sprintf("https://media.licdn.com/dms/image/foo?e=%d&v=beta", future)
	if got := zernioUsableAvatarURL(valid); got != valid {
		t.Fatalf("future LinkedIn avatar = %q, want %q", got, valid)
	}

	plain := "https://example.com/a.png"
	if got := zernioUsableAvatarURL(plain); got != plain {
		t.Fatalf("plain avatar = %q, want %q", got, plain)
	}
}

func TestAccountAdd_ZernioStartsProviderOAuth(t *testing.T) {
	pf := newRecordingPlatform()
	pf.connections = []sdk.PlatformConnection{
		{ID: 99, AppSlug: "zernio", ProjectID: "test-proj", Status: "active"},
	}
	pf.executeResponses["list_profiles"] = &sdk.ExecuteResult{
		Success: true,
		Status:  200,
		Data:    json.RawMessage(`{"profiles":[{"id":"zp_1","name":"Default"}]}`),
	}
	pf.executeResponses["get_connect_url"] = &sdk.ExecuteResult{
		Success: true,
		Status:  200,
		Data:    json.RawMessage(`{"url":"https://zernio.example/connect/linkedin","state":"zs_1"}`),
	}
	ctx := newSocialCtx(t, pf)
	app := &App{}
	out, err := app.toolAccountAdd(ctx, map[string]any{"platform": "linkedin", "provider": "zernio"})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(map[string]any)
	if res["authorize_url"] != "https://zernio.example/connect/linkedin" {
		t.Fatalf("authorize_url = %+v", res)
	}
	var provider, providerProfile, state string
	if err := ctx.AppDB().QueryRow(
		`SELECT provider_slug, provider_profile_id, provider_state FROM pending_accounts WHERE id=?`,
		res["pending_account_id"],
	).Scan(&provider, &providerProfile, &state); err != nil {
		t.Fatal(err)
	}
	if provider != "zernio" || providerProfile != "zp_1" || state != "zs_1" {
		t.Fatalf("pending provider metadata = %q %q %q", provider, providerProfile, state)
	}
	var sawConnect bool
	for _, c := range pf.executeCalls {
		if c.Tool == "get_connect_url" && c.Input["platform"] == "linkedin" && c.Input["profileId"] == "zp_1" {
			sawConnect = true
		}
	}
	if !sawConnect {
		t.Fatalf("get_connect_url not called as expected: %+v", pf.executeCalls)
	}
}

func TestAccountFinalize_ZernioLinkedInOrganization(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["select_linkedin_organization"] = &sdk.ExecuteResult{
		Success: true,
		Status:  200,
		Data:    json.RawMessage(`{"accountId":"za_linkedin"}`),
	}
	pf.executeResponses["list_accounts"] = &sdk.ExecuteResult{
		Success: true,
		Status:  200,
		Data:    json.RawMessage(`{"accounts":[{"accountId":"za_linkedin","profileId":"zp_1","platform":"linkedin","displayName":"Company Page"}]}`),
	}
	ctx := newSocialCtx(t, pf)
	res, _ := ctx.AppDB().Exec(
		`INSERT INTO pending_accounts
		   (project_id, platform, integration_slug, connection_id, status, expires_at, provider_slug, provider_profile_id, provider_data)
		 VALUES ('test-proj', 'linkedin', 'zernio', 99, 'ready', datetime('now','+10 minutes'), 'zernio', 'zp_1',
		         '{"tempToken":"tmp_1","userProfile":{"id":"u1","displayName":"User"},"organizations":[{"id":"org_1","name":"Company Page"}]}')`,
	)
	pendingID, _ := res.LastInsertId()
	app := &App{}
	out, err := app.toolAccountFinalize(ctx, map[string]any{
		"pending_account_id": pendingID,
		"page_id":            "org_1",
	})
	if err != nil {
		t.Fatal(err)
	}
	got := out.(map[string]any)
	if got["provider"] != "zernio" || got["display_name"] != "Company Page" {
		t.Fatalf("finalize result = %+v", got)
	}
	var providerAccount string
	if err := ctx.AppDB().QueryRow(
		`SELECT provider_account_id FROM social_accounts WHERE platform='linkedin' AND provider_slug='zernio'`,
	).Scan(&providerAccount); err != nil {
		t.Fatal(err)
	}
	if providerAccount != "za_linkedin" {
		t.Fatalf("provider_account_id = %q", providerAccount)
	}
	var sawSelect bool
	for _, c := range pf.executeCalls {
		if c.Tool == "select_linkedin_organization" && c.Input["accountType"] == "organization" {
			sawSelect = true
		}
	}
	if !sawSelect {
		t.Fatalf("select_linkedin_organization not called: %+v", pf.executeCalls)
	}
}

func TestPostCreate_ZernioProviderPublishesNonNativePlatform(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["create_post"] = &sdk.ExecuteResult{
		Success: true,
		Status:  200,
		Data:    json.RawMessage(`{"id":"zp_123","platformPostId":"li_456","platformUrl":"https://linkedin.example/post/li_456"}`),
	}
	ctx := newSocialCtx(t, pf)
	r, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts
		   (project_id, platform, connection_id, external_account_id, display_name, status,
		    provider_slug, provider_account_id)
		 VALUES ('test-proj', 'linkedin', 99, 'za_1', 'Company Page', 'active',
		         'zernio', 'za_1')`,
	)
	acctID, _ := r.LastInsertId()

	app := &App{}
	out, err := app.toolPostCreate(ctx, map[string]any{
		"mode":               "publish",
		"body":               "plain social copy",
		"social_account_ids": []any{acctID},
	})
	if err != nil {
		t.Fatal(err)
	}
	postID := out.(map[string]any)["post_id"].(int64)
	if len(pf.executeCalls) != 1 || pf.executeCalls[0].Tool != "create_post" {
		t.Fatalf("expected zernio create_post call, got %+v", pf.executeCalls)
	}
	input := pf.executeCalls[0].Input
	if input["content"] != "plain social copy" {
		t.Fatalf("content = %v", input["content"])
	}
	platforms, ok := input["platforms"].([]any)
	if !ok || len(platforms) != 1 {
		t.Fatalf("platforms input = %#v", input["platforms"])
	}
	target := platforms[0].(map[string]any)
	if target["platform"] != "linkedin" || target["accountId"] != "za_1" {
		t.Fatalf("zernio target = %#v", target)
	}
	var status, platformPostID, providerPostID string
	ctx.AppDB().QueryRow(
		`SELECT status, COALESCE(platform_post_id,''), COALESCE(provider_post_id,'')
		   FROM post_targets WHERE post_id=?`,
		postID,
	).Scan(&status, &platformPostID, &providerPostID)
	if status != "published" || platformPostID != "li_456" || providerPostID != "zp_123" {
		t.Fatalf("target = status %q platform %q provider %q", status, platformPostID, providerPostID)
	}
}

func TestZernioDuplicateContentIsTerminalAndReturnsExistingPostID(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["create_post"] = &sdk.ExecuteResult{
		Success: false,
		Status:  409,
		Data:    json.RawMessage(`{"error":"duplicate content","details":{"existingPostId":"zp_existing_42"}}`),
	}
	ctx := newSocialCtx(t, pf)
	account, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts
		   (project_id, platform, connection_id, external_account_id, display_name, status,
		    provider_slug, provider_account_id)
		 VALUES ('test-proj', 'linkedin', 99, 'za_1', 'Company Page', 'active', 'zernio', 'za_1')`,
	)
	accountID, _ := account.LastInsertId()
	app := &App{}
	created, err := app.toolPostCreate(ctx, map[string]any{
		"mode": "publish", "body": "same content", "social_account_ids": []any{accountID},
	})
	if err != nil {
		t.Fatal(err)
	}
	postID := created.(map[string]any)["post_id"].(int64)
	var attempts, retryable, upstreamStatus int
	var code, existingID string
	if err := ctx.AppDB().QueryRow(
		`SELECT attempts, retryable, upstream_status, failure_code, existing_post_id
		   FROM post_targets WHERE post_id=?`, postID,
	).Scan(&attempts, &retryable, &upstreamStatus, &code, &existingID); err != nil {
		t.Fatal(err)
	}
	if attempts != 1 || retryable != 0 || upstreamStatus != 409 || code != "duplicate_content" || existingID != "zp_existing_42" {
		t.Fatalf("duplicate classification attempts=%d retryable=%d status=%d code=%q existing=%q", attempts, retryable, upstreamStatus, code, existingID)
	}
	retry, err := app.toolPostRetry(ctx, map[string]any{"post_id": postID})
	if err != nil {
		t.Fatal(err)
	}
	result := retry.(map[string]any)
	if result["retryable"] != false || result["existing_post_id"] != "zp_existing_42" {
		t.Fatalf("retry result = %+v", result)
	}
	if len(pf.executeCalls) != 1 {
		t.Fatalf("deterministic duplicate was retried: %+v", pf.executeCalls)
	}
	callback := app.publishScheduledPost(ctx, "test-proj", postID)
	if callback["status"] != "terminal" || callback["retryable"] != false || callback["existing_post_id"] != "zp_existing_42" {
		t.Fatalf("jobs callback should acknowledge terminal duplicate: %+v", callback)
	}
	if len(pf.executeCalls) != 1 {
		t.Fatalf("jobs callback repeated deterministic provider call: %+v", pf.executeCalls)
	}
}

func TestZernioDraftMirrorUsesNativeDraftAndUpdateCapabilities(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["create_post"] = &sdk.ExecuteResult{Success: true, Status: 201, Data: json.RawMessage(`{"id":"zd_1","status":"draft"}`)}
	pf.executeResponses["update_post"] = &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"id":"zd_1","status":"draft"}`)}
	ctx := newSocialCtx(t, pf)
	account, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts
		   (project_id, platform, connection_id, display_name, status, provider_slug, provider_account_id)
		 VALUES ('test-proj', 'linkedin', 99, 'Company', 'active', 'zernio', 'za_1')`,
	)
	accountID, _ := account.LastInsertId()
	app := &App{}
	created, err := app.toolPostDraftCreate(ctx, map[string]any{
		"body": "mirrored", "social_account_ids": []any{accountID}, "provider_sync_mode": "mirror",
	})
	if err != nil {
		t.Fatal(err)
	}
	post := created.(map[string]any)
	postID := post["id"].(int64)
	if len(pf.executeCalls) != 1 || pf.executeCalls[0].Tool != "create_post" || pf.executeCalls[0].Input["isDraft"] != true || pf.executeCalls[0].Input["publishNow"] != false {
		t.Fatalf("draft mirror call = %+v", pf.executeCalls)
	}
	var providerID, syncStatus string
	_ = ctx.AppDB().QueryRow(`SELECT provider_post_id, provider_sync_status FROM post_targets WHERE post_id=?`, postID).Scan(&providerID, &syncStatus)
	if providerID != "zd_1" || syncStatus != "draft" {
		t.Fatalf("provider mirror id=%q status=%q", providerID, syncStatus)
	}
	if _, err := app.toolPostDraftUpdate(ctx, map[string]any{"post_id": postID, "expected_revision": 1, "body": "mirrored v2"}); err != nil {
		t.Fatal(err)
	}
	if len(pf.executeCalls) != 2 || pf.executeCalls[1].Tool != "update_post" || pf.executeCalls[1].Input["postId"] != "zd_1" || pf.executeCalls[1].Input["content"] != "mirrored v2" {
		t.Fatalf("draft mirror update = %+v", pf.executeCalls)
	}
}

func TestZernioImportReconcilesDraftAndScheduledLifecycle(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["sync_external_posts"] = &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{}`)}
	pf.executeResponses["list_posts"] = &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{
		"posts":[
			{"id":"zd_import","status":"draft","isDraft":true,"content":"provider draft"},
			{"id":"zs_import","status":"scheduled","content":"provider scheduled","scheduledFor":"2026-09-02T12:00:00Z"}
		]
	}`)}
	ctx := newSocialCtx(t, pf)
	account, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts
		   (project_id, platform, connection_id, display_name, status, provider_slug, provider_account_id)
		 VALUES ('test-proj', 'linkedin', 99, 'Company', 'active', 'zernio', 'za_1')`,
	)
	accountID, _ := account.LastInsertId()
	result := (&App{}).importZernioPosts(ctx, "test-proj", importResult{}, accountID, 99, "za_1", 0, 50)
	if result.Status != "ok" || result.Imported != 2 {
		t.Fatalf("import result = %+v", result)
	}
	rows, err := ctx.AppDB().Query(`SELECT status, requested_mode, COALESCE(schedule_at,''), source FROM posts ORDER BY id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := [][4]string{}
	for rows.Next() {
		var row [4]string
		_ = rows.Scan(&row[0], &row[1], &row[2], &row[3])
		got = append(got, row)
	}
	rows.Close()
	if len(got) != 2 || got[0][0] != "draft" || got[0][1] != "draft" || got[1][0] != "scheduled" || got[1][1] != "schedule" || got[1][2] != "2026-09-02T12:00:00Z" || got[1][3] != "provider" {
		t.Fatalf("imported workflow rows = %+v", got)
	}
	if err := (&App{}).runProviderReconciler(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	var postCount int
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM posts`).Scan(&postCount)
	if postCount != 2 {
		t.Fatalf("automatic reconciliation duplicated provider posts: count=%d", postCount)
	}
}

func TestZernioImportPreservesPublishedPlatformHistoryAndRepairsExisting(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["sync_external_posts"] = &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{}`)}
	pf.executeResponses["list_posts"] = &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{
		"posts":[{
			"_id":"69a059c7627edb9b6d64e139",
			"content":"AgentDojo history",
			"createdAt":"2026-02-26T14:33:43.886Z",
			"scheduledFor":"2026-02-26T14:33:42.520Z",
			"status":"published",
			"updatedAt":"2026-02-26T14:33:47.089Z",
			"platforms":[
				{
					"accountId":{"_id":"other-account"},
					"platform":"facebook",
					"platformPostId":"facebook-wrong",
					"platformPostUrl":"https://facebook.example/wrong",
					"publishedAt":"2026-02-26T14:33:45.000Z",
					"scheduledFor":"2026-02-26T14:30:00.000Z",
					"status":"published"
				},
				{
					"accountId":{"_id":"za_linkedin"},
					"platform":"linkedin",
					"platformPostId":"urn:li:share:7432795016680497152",
					"platformPostUrl":"https://www.linkedin.com/feed/update/urn:li:share:7432795016680497152/",
					"publishedAt":"2026-02-26T14:33:46.829Z",
					"scheduledFor":"2026-02-26T14:33:42.520Z",
					"status":"published"
				}
			]
		}]
	}`)}
	ctx := newSocialCtx(t, pf)
	account, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts
		   (project_id, platform, connection_id, display_name, status, provider_slug, provider_account_id)
		 VALUES ('test-proj', 'linkedin', 99, 'Apteva', 'active', 'zernio', 'za_linkedin')`,
	)
	accountID, _ := account.LastInsertId()
	app := &App{}
	result := app.importZernioPosts(ctx, "test-proj", importResult{}, accountID, 99, "za_linkedin", 0, 50)
	if result.Status != "ok" || result.Imported != 1 {
		t.Fatalf("import result = %+v", result)
	}

	assertStored := func() int64 {
		t.Helper()
		var postID int64
		var status, requestedMode, createdAt, scheduleAt, publishedAt, source string
		if err := ctx.AppDB().QueryRow(
			`SELECT id,status,requested_mode,created_at,COALESCE(schedule_at,''),COALESCE(published_at,''),source
			   FROM posts WHERE body='AgentDojo history'`,
		).Scan(&postID, &status, &requestedMode, &createdAt, &scheduleAt, &publishedAt, &source); err != nil {
			t.Fatal(err)
		}
		if status != "published" || requestedMode != postModePublish || source != "provider" ||
			createdAt != "2026-02-26T14:33:43.886Z" || scheduleAt != "2026-02-26T14:33:42.520Z" ||
			publishedAt != "2026-02-26T14:33:46.829Z" {
			t.Fatalf("stored parent = status=%q mode=%q source=%q created=%q scheduled=%q published=%q",
				status, requestedMode, source, createdAt, scheduleAt, publishedAt)
		}
		var targetStatus, platformID, platformURL, targetPublishedAt, providerID string
		var attempts int
		if err := ctx.AppDB().QueryRow(
			`SELECT status,COALESCE(platform_post_id,''),COALESCE(platform_url,''),COALESCE(published_at,''),provider_post_id,attempts
			   FROM post_targets WHERE post_id=?`, postID,
		).Scan(&targetStatus, &platformID, &platformURL, &targetPublishedAt, &providerID, &attempts); err != nil {
			t.Fatal(err)
		}
		if targetStatus != "published" || platformID != "urn:li:share:7432795016680497152" ||
			platformURL != "https://www.linkedin.com/feed/update/urn:li:share:7432795016680497152/" ||
			targetPublishedAt != "2026-02-26T14:33:46.829Z" || providerID != "69a059c7627edb9b6d64e139" || attempts != 0 {
			t.Fatalf("stored target = status=%q platform_id=%q url=%q published=%q provider_id=%q attempts=%d",
				targetStatus, platformID, platformURL, targetPublishedAt, providerID, attempts)
		}
		return postID
	}

	postID := assertStored()
	if _, err := ctx.AppDB().Exec(
		`UPDATE posts SET status='published',requested_mode='schedule',created_at='2026-08-24 09:27:14',published_at='2026-08-27 09:41:51'
		  WHERE id=?`, postID,
	); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(
		`UPDATE post_targets SET status='scheduled',platform_post_id=NULL,platform_url=NULL,published_at=NULL WHERE post_id=?`, postID,
	); err != nil {
		t.Fatal(err)
	}

	result = app.importZernioPosts(ctx, "test-proj", importResult{}, accountID, 99, "za_linkedin", 0, 50)
	if result.Status != "ok" || result.SkippedExisting != 1 {
		t.Fatalf("reconcile result = %+v", result)
	}
	assertStored()
	for _, call := range pf.executeCalls {
		if call.Tool == "create_post" {
			t.Fatalf("history import attempted upstream publication: %+v", pf.executeCalls)
		}
	}
}

func TestProviderReconcilerManagedOnlyDoesNotDiscoverHistory(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["sync_external_posts"] = &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{}`)}
	pf.executeResponses["list_posts"] = &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{
		"posts":[{"id":"unmanaged-history","status":"published","content":"unrelated history"}]
	}`)}
	ctx := newSocialCtx(t, pf)
	account, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts
		   (project_id, platform, connection_id, display_name, status, provider_slug,
		    provider_account_id, provider_profile_id)
		 VALUES ('test-proj', 'linkedin', 99, 'Apteva', 'active', 'zernio', 'za_1', 'zp_1')`,
	)
	accountID, _ := account.LastInsertId()

	if err := (&App{}).runProviderReconciler(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	var postCount int
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM posts`).Scan(&postCount)
	if postCount != 0 {
		t.Fatalf("managed-only reconciliation imported %d unrelated posts", postCount)
	}
	if len(pf.executeCalls) != 0 {
		t.Fatalf("managed-only account with no managed posts called provider history tools: %+v", pf.executeCalls)
	}

	out, err := (&App{}).toolAccountProviderSyncUpdate(ctx, map[string]any{
		"social_account_id": accountID,
		"mode":              providerImportModeFullHistory,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["publishing_unchanged"] != true {
		t.Fatalf("sync update response = %+v", out)
	}
	if err := (&App{}).runProviderReconciler(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM posts`).Scan(&postCount)
	if postCount != 1 {
		t.Fatalf("full-history opt-in imported %d posts, want 1", postCount)
	}
	if len(pf.executeCalls) != 2 || pf.executeCalls[0].Tool != "sync_external_posts" || pf.executeCalls[1].Tool != "list_posts" {
		t.Fatalf("full-history calls = %+v", pf.executeCalls)
	}
	if pf.executeCalls[1].Input["accountId"] != "za_1" || pf.executeCalls[1].Input["profileId"] != "zp_1" {
		t.Fatalf("full-history list was not account/profile scoped: %+v", pf.executeCalls[1].Input)
	}
}

func TestProviderReconcilerManagedOnlyReadsExactKnownPost(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["get_post"] = &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{
		"data":{"post":{
			"id":"known-1","content":"managed post","status":"published","createdAt":"2026-08-01T09:00:00Z",
			"platforms":[{"accountId":{"_id":"za_1","profileId":"zp_1"},"platform":"linkedin",
				"status":"published","platformPostId":"urn:li:share:1","platformPostUrl":"https://linkedin.example/1",
				"publishedAt":"2026-08-01T09:05:00Z"}]
		}}
	}`)}
	ctx := newSocialCtx(t, pf)
	account, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts
		   (project_id, platform, connection_id, display_name, status, provider_slug,
		    provider_account_id, provider_profile_id)
		 VALUES ('test-proj', 'linkedin', 99, 'Apteva', 'active', 'zernio', 'za_1', 'zp_1')`,
	)
	accountID, _ := account.LastInsertId()
	post, _ := ctx.AppDB().Exec(
		`INSERT INTO posts (project_id,body,status,source,provider_sync_mode,requested_mode)
		 VALUES ('test-proj','managed post','scheduled','provider','mirror','schedule')`,
	)
	postID, _ := post.LastInsertId()
	_, _ = ctx.AppDB().Exec(
		`INSERT INTO post_targets (post_id,social_account_id,status,provider_post_id,provider_sync_status)
		 VALUES (?,?,'scheduled','known-1','scheduled')`, postID, accountID,
	)

	if err := (&App{}).runProviderReconciler(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if len(pf.executeCalls) != 1 || pf.executeCalls[0].Tool != "get_post" || pf.executeCalls[0].Input["postId"] != "known-1" {
		t.Fatalf("managed reconciliation calls = %+v", pf.executeCalls)
	}
	var parentStatus, targetStatus, platformID, publishedAt string
	if err := ctx.AppDB().QueryRow(
		`SELECT p.status,t.status,COALESCE(t.platform_post_id,''),COALESCE(t.published_at,'')
		   FROM posts p JOIN post_targets t ON t.post_id=p.id WHERE p.id=?`, postID,
	).Scan(&parentStatus, &targetStatus, &platformID, &publishedAt); err != nil {
		t.Fatal(err)
	}
	if parentStatus != "published" || targetStatus != "published" || platformID != "urn:li:share:1" || publishedAt != "2026-08-01T09:05:00Z" {
		t.Fatalf("managed reconciliation stored parent=%q target=%q id=%q published=%q", parentStatus, targetStatus, platformID, publishedAt)
	}
}

func TestProviderImportDeletionTombstonePreventsReimport(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["sync_external_posts"] = &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{}`)}
	pf.executeResponses["list_posts"] = &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{
		"posts":[{"id":"deleted-provider-post","status":"published","content":"remove me","createdAt":"2026-02-01T10:00:00Z",
			"platforms":[{"accountId":{"_id":"za_1"},"platform":"linkedin","status":"published",
			"platformPostId":"urn:li:share:deleted","platformPostUrl":"https://linkedin.example/deleted",
			"publishedAt":"2026-02-01T10:01:00Z"}]}]
	}`)}
	ctx := newSocialCtx(t, pf)
	account, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts
		   (project_id, platform, connection_id, display_name, status, provider_slug, provider_account_id)
		 VALUES ('test-proj', 'linkedin', 99, 'Apteva', 'active', 'zernio', 'za_1')`,
	)
	accountID, _ := account.LastInsertId()
	app := &App{}
	result := app.importZernioPosts(ctx, "test-proj", importResult{}, accountID, 99, "za_1", 0, 50)
	if result.Imported != 1 {
		t.Fatalf("initial import = %+v", result)
	}
	var postID int64
	_ = ctx.AppDB().QueryRow(`SELECT id FROM posts WHERE body='remove me'`).Scan(&postID)
	if _, err := app.toolPostDelete(ctx, map[string]any{"post_id": postID, "force_local_only": true}); err != nil {
		t.Fatal(err)
	}
	var tombstones int
	_ = ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM provider_import_tombstones
		  WHERE social_account_id=? AND provider_post_id='deleted-provider-post'`, accountID,
	).Scan(&tombstones)
	if tombstones != 1 {
		t.Fatalf("provider tombstones = %d, want 1", tombstones)
	}
	result = app.importZernioPosts(ctx, "test-proj", importResult{}, accountID, 99, "za_1", 0, 50)
	var postCount int
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM posts WHERE body='remove me'`).Scan(&postCount)
	if postCount != 0 || result.Imported != 0 {
		t.Fatalf("deleted provider post was reimported: count=%d result=%+v", postCount, result)
	}
}

func TestProviderImportedScheduleIsNeverPublishedByFallbackWorkerOrRollup(t *testing.T) {
	ctx := newSocialCtx(t, newRecordingPlatform())
	account, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts
		   (project_id, platform, connection_id, display_name, status, provider_slug, provider_account_id)
		 VALUES ('test-proj', 'linkedin', 99, 'Apteva', 'active', 'zernio', 'za_linkedin')`,
	)
	accountID, _ := account.LastInsertId()
	post, _ := ctx.AppDB().Exec(
		`INSERT INTO posts (project_id,body,status,schedule_at,source,provider_sync_mode)
		 VALUES ('test-proj','provider schedule','scheduled','2026-02-26T14:33:42Z','provider','mirror')`,
	)
	postID, _ := post.LastInsertId()
	_, _ = ctx.AppDB().Exec(
		`INSERT INTO post_targets (post_id,social_account_id,status,provider_post_id,provider_sync_status)
		 VALUES (?,?,'scheduled','provider-1','scheduled')`, postID, accountID,
	)

	if err := (&App{}).runScheduledPublisher(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	if got := (&App{}).rollupPostStatus(ctx, postID); got != "scheduled" {
		t.Fatalf("rollup status = %q, want scheduled", got)
	}
	var parentStatus, targetStatus, publishedAt string
	var attempts int
	if err := ctx.AppDB().QueryRow(
		`SELECT p.status,COALESCE(p.published_at,''),t.status,t.attempts
		   FROM posts p JOIN post_targets t ON t.post_id=p.id WHERE p.id=?`, postID,
	).Scan(&parentStatus, &publishedAt, &targetStatus, &attempts); err != nil {
		t.Fatal(err)
	}
	if parentStatus != "scheduled" || targetStatus != "scheduled" || publishedAt != "" || attempts != 0 {
		t.Fatalf("provider schedule mutated: parent=%q target=%q published=%q attempts=%d",
			parentStatus, targetStatus, publishedAt, attempts)
	}
}

func TestInboxReply_ZernioBackedDMUsesProvider(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["send_inbox_message"] = &sdk.ExecuteResult{
		Success: true,
		Status:  200,
		Data:    json.RawMessage(`{"id":"reply_1","url":"https://zernio.example/reply_1"}`),
	}
	ctx := newSocialCtx(t, pf)
	r, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts
		   (project_id, platform, connection_id, external_account_id, display_name, status,
		    provider_slug, provider_account_id)
		 VALUES ('test-proj', 'linkedin', 99, 'za_1', 'Company Page', 'active',
		         'zernio', 'za_1')`,
	)
	acctID, _ := r.LastInsertId()
	itemID, _, err := upsertInboxItem(ctx.AppDB(), inboxUpsertInput{
		ProjectID:       "test-proj",
		SocialAccountID: acctID,
		Platform:        "linkedin",
		Kind:            inboxKindDM,
		ExternalID:      "msg_1",
		ExternalPostID:  "conv_1",
		Body:            "hello",
	})
	if err != nil {
		t.Fatal(err)
	}

	app := &App{}
	out, err := app.toolInboxReply(ctx, map[string]any{"id": itemID, "body": "reply"})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(inboxOutcome)
	if res.Status != "ok" || res.ExternalID != "reply_1" {
		t.Fatalf("unexpected reply result: %+v", res)
	}
	if len(pf.executeCalls) != 1 || pf.executeCalls[0].Tool != "send_inbox_message" {
		t.Fatalf("expected zernio send_inbox_message, got %+v", pf.executeCalls)
	}
	if pf.executeCalls[0].Input["conversationId"] != "conv_1" || pf.executeCalls[0].Input["message"] != "reply" {
		t.Fatalf("unexpected zernio reply input: %+v", pf.executeCalls[0].Input)
	}
}

func TestInboxReply_FacebookCommentPublic(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["reply_to_comment"] = &sdk.ExecuteResult{
		Success: true,
		Status:  200,
		Data:    json.RawMessage(`{"id":"reply_1"}`),
	}
	ctx := newSocialCtx(t, pf)
	acctID := insertFacebookInboxAccount(t, ctx)
	itemID, _, err := upsertInboxItem(ctx.AppDB(), inboxUpsertInput{
		ProjectID:        "test-proj",
		SocialAccountID:  acctID,
		Platform:         "facebook",
		Kind:             inboxKindComment,
		ExternalID:       "comment_1",
		ExternalPostID:   "page_1_post_1",
		AuthorExternalID: "user_1",
		Body:             "hello",
	})
	if err != nil {
		t.Fatal(err)
	}

	out, err := (&App{}).toolInboxReply(ctx, map[string]any{"id": itemID, "body": "thanks", "mode": "public"})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(inboxOutcome)
	if res.Status != "ok" || res.ExternalID != "reply_1" {
		t.Fatalf("unexpected facebook public reply result: %+v", res)
	}
	if len(pf.executeCalls) != 1 || pf.executeCalls[0].Tool != "reply_to_comment" {
		t.Fatalf("expected reply_to_comment call, got %+v", pf.executeCalls)
	}
	input := pf.executeCalls[0].Input
	if input["commentId"] != "comment_1" || input["message"] != "thanks" || input["access_token"] != "page_tok" {
		t.Fatalf("unexpected reply_to_comment input: %+v", input)
	}
}

func TestInboxReply_FacebookCommentPrivate(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["facebook_private_reply_to_comment"] = &sdk.ExecuteResult{
		Success: true,
		Status:  200,
		Data:    json.RawMessage(`{"message_id":"dm_1"}`),
	}
	ctx := newSocialCtx(t, pf)
	acctID := insertFacebookInboxAccount(t, ctx)
	itemID, _, err := upsertInboxItem(ctx.AppDB(), inboxUpsertInput{
		ProjectID:        "test-proj",
		SocialAccountID:  acctID,
		Platform:         "facebook",
		Kind:             inboxKindComment,
		ExternalID:       "comment_1",
		ExternalPostID:   "page_1_post_1",
		AuthorExternalID: "user_1",
		Body:             "hello",
	})
	if err != nil {
		t.Fatal(err)
	}

	out, err := (&App{}).toolInboxReply(ctx, map[string]any{"id": itemID, "body": "sent privately", "mode": "private"})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(inboxOutcome)
	if res.Status != "ok" || res.ExternalID != "dm_1" {
		t.Fatalf("unexpected facebook private reply result: %+v", res)
	}
	if len(pf.executeCalls) != 1 || pf.executeCalls[0].Tool != "facebook_private_reply_to_comment" {
		t.Fatalf("expected facebook_private_reply_to_comment call, got %+v", pf.executeCalls)
	}
	input := pf.executeCalls[0].Input
	if input["commentId"] != "comment_1" || input["message"] != "sent privately" || input["access_token"] != "page_tok" {
		t.Fatalf("unexpected private reply input: %+v", input)
	}
}

func TestInboxReply_FacebookDM(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["facebook_send_message"] = &sdk.ExecuteResult{
		Success: true,
		Status:  200,
		Data:    json.RawMessage(`{"message_id":"m_reply_1"}`),
	}
	ctx := newSocialCtx(t, pf)
	acctID := insertFacebookInboxAccount(t, ctx)
	itemID, _, err := upsertInboxItem(ctx.AppDB(), inboxUpsertInput{
		ProjectID:        "test-proj",
		SocialAccountID:  acctID,
		Platform:         "facebook",
		Kind:             inboxKindDM,
		ExternalID:       "m_1",
		ExternalPostID:   "conv_1",
		AuthorExternalID: "psid_1",
		AuthorName:       "Reader",
		Body:             "hello",
	})
	if err != nil {
		t.Fatal(err)
	}

	out, err := (&App{}).toolInboxReply(ctx, map[string]any{"id": itemID, "body": "hello back"})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(inboxOutcome)
	if res.Status != "ok" || res.ExternalID != "m_reply_1" {
		t.Fatalf("unexpected facebook dm reply result: %+v", res)
	}
	if len(pf.executeCalls) != 1 || pf.executeCalls[0].Tool != "facebook_send_message" {
		t.Fatalf("expected facebook_send_message call, got %+v", pf.executeCalls)
	}
	input := pf.executeCalls[0].Input
	if input["pageId"] != "page_1" || input["access_token"] != "page_tok" {
		t.Fatalf("unexpected facebook_send_message input: %+v", input)
	}
	recipient := input["recipient"].(map[string]any)
	message := input["message"].(map[string]any)
	if recipient["id"] != "psid_1" || message["text"] != "hello back" {
		t.Fatalf("unexpected dm payload: %+v", input)
	}
}

func TestInboxSync_FacebookPullsCommentsAndDMs(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["list_media_comments"] = &sdk.ExecuteResult{
		Success: true,
		Status:  200,
		Data: json.RawMessage(`{"data":[{
			"id":"c_1",
			"message":"Nice",
			"created_time":"2026-07-07T10:00:00+0000",
			"from":{"id":"u_1","name":"Reader","picture":{"data":{"url":"https://example.com/u.jpg"}}}
		}]}`),
	}
	pf.executeResponses["facebook_list_conversations"] = &sdk.ExecuteResult{
		Success: true,
		Status:  200,
		Data:    json.RawMessage(`{"data":[{"id":"conv_1"}]}`),
	}
	pf.executeResponses["facebook_get_conversation"] = &sdk.ExecuteResult{
		Success: true,
		Status:  200,
		Data: json.RawMessage(`{"id":"conv_1","messages":{"data":[
			{"id":"m_page","message":"hello","created_time":"2026-07-07T10:01:00+0000","from":{"id":"page_1","name":"Page"}},
			{"id":"m_user","message":"hi","created_time":"2026-07-07T10:02:00+0000","from":{"id":"psid_1","name":"Reader","picture":{"data":{"url":"https://example.com/r.jpg"}}}}
		]}}`),
	}
	pf.executeResponses["facebook_list_tagged"] = &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"data":[]}`)}
	pf.executeResponses["facebook_list_reviews"] = &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"data":[]}`)}
	ctx := newSocialCtx(t, pf)
	acctID := insertFacebookInboxAccount(t, ctx)
	postRes, err := ctx.AppDB().Exec(
		`INSERT INTO posts (project_id, body, status, published_at)
		 VALUES ('test-proj', 'body', 'published', datetime('now'))`,
	)
	if err != nil {
		t.Fatal(err)
	}
	postID, _ := postRes.LastInsertId()
	if _, err := ctx.AppDB().Exec(
		`INSERT INTO post_targets (post_id, social_account_id, status, platform_post_id, published_at)
		 VALUES (?, ?, 'published', 'page_1_post_1', datetime('now'))`,
		postID, acctID,
	); err != nil {
		t.Fatal(err)
	}

	out, err := (&App{}).toolInboxSync(ctx, map[string]any{"social_account_ids": []any{acctID}})
	if err != nil {
		t.Fatal(err)
	}
	rawResults, _ := json.Marshal(out.(map[string]any)["results"])
	var results []struct {
		Platform string `json:"platform"`
		Status   string `json:"status"`
	}
	if err := json.Unmarshal(rawResults, &results); err != nil {
		t.Fatal(err)
	}
	if len(results) != 1 {
		t.Fatalf("results = %+v", results)
	}
	got := results[0]
	if got.Status != "ok" || got.Platform != "facebook" {
		t.Fatalf("sync result = %+v", got)
	}
	var comments, dms int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE kind='comment' AND author_name='Reader'`).Scan(&comments); err != nil {
		t.Fatal(err)
	}
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM inbox_items WHERE kind='dm' AND author_external_id='psid_1'`).Scan(&dms); err != nil {
		t.Fatal(err)
	}
	if comments != 1 || dms != 1 {
		t.Fatalf("synced comments=%d dms=%d, want 1/1", comments, dms)
	}
}

func insertFacebookInboxAccount(t *testing.T, ctx *sdk.AppCtx) int64 {
	t.Helper()
	res, err := ctx.AppDB().Exec(
		`INSERT INTO social_accounts
		   (project_id, platform, connection_id, external_account_id, display_name, status, page_credentials)
		 VALUES ('test-proj', 'facebook', 42, 'page_1', 'Page', 'active', '{"access_token":"page_tok"}')`,
	)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := res.LastInsertId()
	return id
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
		"mode":               "publish",
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

func TestPostCreate_TargetBodyCanSupplyPostBody(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["post_tweet"] = &sdk.ExecuteResult{
		Success: true,
		Data:    json.RawMessage(`{"data":{"id":"target-body","text":"target-only text"}}`),
	}
	ctx := newSocialCtx(t, pf)
	r, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, display_name, status)
		 VALUES ('test-proj', 'twitter', 42, '@me', 'active')`,
	)
	acctID, _ := r.LastInsertId()

	app := &App{}
	out, err := app.toolPostCreate(ctx, map[string]any{
		"mode": "publish",
		"targets": []any{
			map[string]any{
				"social_account_id": acctID,
				"body":              "target-only text",
			},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	postID := out.(map[string]any)["post_id"].(int64)

	if len(pf.executeCalls) != 1 {
		t.Fatalf("expected 1 execute call, got %d", len(pf.executeCalls))
	}
	if got := pf.executeCalls[0].Input["text"]; got != "target-only text" {
		t.Fatalf("post_tweet text = %v, want target-only text; input=%+v", got, pf.executeCalls[0].Input)
	}
	var storedBody string
	if err := ctx.AppDB().QueryRow(`SELECT body FROM posts WHERE id=?`, postID).Scan(&storedBody); err != nil {
		t.Fatal(err)
	}
	if storedBody != "target-only text" {
		t.Fatalf("stored body = %q, want target-only text", storedBody)
	}
}

func TestPostCreate_TargetsStillRequireResolvedBody(t *testing.T) {
	ctx := newSocialCtx(t, newRecordingPlatform())
	r, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, display_name, status)
		 VALUES ('test-proj', 'twitter', 42, '@me', 'active')`,
	)
	acctID, _ := r.LastInsertId()

	app := &App{}
	_, err := app.toolPostCreate(ctx, map[string]any{
		"mode": "publish",
		"targets": []any{
			map[string]any{"social_account_id": acctID},
		},
	})
	if err == nil || !strings.Contains(err.Error(), "targets[0].body") {
		t.Fatalf("err = %v, want missing target body error", err)
	}
}

func TestResolveMediaRequestsAptevaDelivery(t *testing.T) {
	pf := newRecordingPlatform()
	pf.callAppResponses["storage:files_get"] = json.RawMessage(
		`{"result":{"content":[{"type":"text","text":"{\"id\":296,\"content_type\":\"video/mp4\",\"size_bytes\":1024}"}]}}`,
	)
	pf.callAppResponses["storage:files_get_url"] = json.RawMessage(
		`{"result":{"content":[{"type":"text","text":"{\"url\":\"https://agents.example.com/api/apps/storage/files/296/content/video.mp4?sig=abc\"}"}]}}`,
	)
	ctx := newSocialCtx(t, pf)

	media, err := (&App{}).resolveMedia(ctx, []int64{296}, "media-proj")
	if err != nil {
		t.Fatal(err)
	}
	if len(media) != 1 {
		t.Fatalf("resolved media count = %d, want 1", len(media))
	}

	var getURLCall *callAppCall
	for i := range pf.callAppCalls {
		if pf.callAppCalls[i].AppName == "storage" && pf.callAppCalls[i].Tool == "files_get_url" {
			getURLCall = &pf.callAppCalls[i]
			break
		}
	}
	if getURLCall == nil {
		t.Fatal("storage files_get_url was not called")
	}
	if got := getURLCall.Input["delivery"]; got != "apteva" {
		t.Fatalf("files_get_url delivery = %v, want apteva; input=%+v", got, getURLCall.Input)
	}
	if got := getURLCall.Input["ttl_seconds"]; got != 3600 {
		t.Fatalf("files_get_url ttl_seconds = %v, want 3600", got)
	}
	if got := getURLCall.Input["_project_id"]; got != "media-proj" {
		t.Fatalf("files_get_url _project_id = %v, want media-proj", got)
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
		"mode":               "publish",
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

func TestPostCreate_TwitterImageUploadsMediaID(t *testing.T) {
	mediaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("png-bytes"))
	}))
	defer mediaSrv.Close()

	pf := newRecordingPlatform()
	pf.callAppResponses["storage:files_get"] = json.RawMessage(
		`{"result":{"content":[{"type":"text","text":"{\"id\":296,\"content_type\":\"image/png\",\"size_bytes\":9}"}]}}`,
	)
	pf.callAppResponses["storage:files_get_url"] = json.RawMessage(
		`{"result":{"content":[{"type":"text","text":"{\"url\":\"` + mediaSrv.URL + `/image.png\"}"}]}}`,
	)
	pf.executeResponses["upload_media"] = &sdk.ExecuteResult{
		Success: true,
		Data:    json.RawMessage(`{"data":{"id":"media123"}}`),
	}
	pf.executeResponses["post_tweet"] = &sdk.ExecuteResult{
		Success: true,
		Data:    json.RawMessage(`{"data":{"id":"tweet123","text":"hello image"}}`),
	}
	ctx := newSocialCtx(t, pf)
	r, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, display_name, status)
		 VALUES ('test-proj', 'twitter', 42, '@me', 'active')`,
	)
	acctID, _ := r.LastInsertId()

	app := &App{}
	if _, err := app.toolPostCreate(ctx, map[string]any{
		"mode":               "publish",
		"body":               "hello image",
		"social_account_ids": []any{acctID},
		"media_storage_ids":  []any{int64(296)},
		"media_project_id":   "media-proj",
	}); err != nil {
		t.Fatal(err)
	}

	var sawUpload, sawPost bool
	for _, c := range pf.executeCalls {
		switch c.Tool {
		case "upload_media":
			sawUpload = true
			if c.Input["media_category"] != "tweet_image" {
				t.Errorf("media_category = %v", c.Input["media_category"])
			}
			if c.Input["media_type"] != "image/png" {
				t.Errorf("media_type = %v", c.Input["media_type"])
			}
			if c.Input["media"] == "" {
				t.Errorf("base64 media not set: %+v", c.Input)
			}
		case "post_tweet":
			sawPost = true
			media := c.Input["media"].(map[string]any)
			ids := media["media_ids"].([]string)
			if len(ids) != 1 || ids[0] != "media123" {
				t.Errorf("media_ids = %+v", ids)
			}
		}
	}
	if !sawUpload || !sawPost {
		t.Fatalf("expected upload_media and post_tweet calls, got %+v", pf.executeCalls)
	}
}

func TestPostCreate_TwitterVideoUsesChunkedUpload(t *testing.T) {
	videoBytes := []byte("video-bytes")
	mediaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "video/mp4")
		_, _ = w.Write(videoBytes)
	}))
	defer mediaSrv.Close()

	pf := newRecordingPlatform()
	pf.callAppResponses["storage:files_get"] = json.RawMessage(
		fmt.Sprintf(`{"result":{"content":[{"type":"text","text":"{\"id\":298,\"content_type\":\"video/mp4\",\"size_bytes\":%d}"}]}}`, len(videoBytes)),
	)
	pf.callAppResponses["storage:files_get_url"] = json.RawMessage(
		`{"result":{"content":[{"type":"text","text":"{\"url\":\"` + mediaSrv.URL + `/video.mp4\"}"}]}}`,
	)
	pf.executeResponses["upload_init"] = &sdk.ExecuteResult{
		Success: true,
		Data:    json.RawMessage(`{"data":{"id":"media-video"}}`),
	}
	pf.executeResponses["upload_finalize"] = &sdk.ExecuteResult{
		Success: true,
		Data:    json.RawMessage(`{"data":{"id":"media-video","processing_info":{"state":"succeeded"}}}`),
	}
	pf.executeResponses["post_tweet"] = &sdk.ExecuteResult{
		Success: true,
		Data:    json.RawMessage(`{"data":{"id":"tweet-video","text":"hello video"}}`),
	}
	ctx := newSocialCtx(t, pf)
	r, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, display_name, status)
		 VALUES ('test-proj', 'twitter', 42, '@me', 'active')`,
	)
	acctID, _ := r.LastInsertId()

	app := &App{}
	if _, err := app.toolPostCreate(ctx, map[string]any{
		"mode":               "publish",
		"body":               "hello video",
		"social_account_ids": []any{acctID},
		"media_storage_ids":  []any{int64(298)},
		"media_project_id":   "media-proj",
	}); err != nil {
		t.Fatal(err)
	}

	var sawInit, sawAppend, sawFinalize, sawPost bool
	for _, c := range pf.executeCalls {
		switch c.Tool {
		case "upload_init":
			sawInit = true
			if c.Input["media_category"] != "tweet_video" {
				t.Errorf("media_category = %v", c.Input["media_category"])
			}
			if c.Input["total_bytes"] != int64(len(videoBytes)) {
				t.Errorf("total_bytes = %v", c.Input["total_bytes"])
			}
		case "upload_append":
			sawAppend = true
			if c.Input["media_id"] != "media-video" || c.Input["segment_index"] != 0 {
				t.Errorf("append input = %+v", c.Input)
			}
		case "upload_finalize":
			sawFinalize = true
		case "post_tweet":
			sawPost = true
			media := c.Input["media"].(map[string]any)
			ids := media["media_ids"].([]string)
			if len(ids) != 1 || ids[0] != "media-video" {
				t.Errorf("media_ids = %+v", ids)
			}
		}
	}
	if !sawInit || !sawAppend || !sawFinalize || !sawPost {
		t.Fatalf("missing expected X video calls: %+v", pf.executeCalls)
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
		"mode":               "publish",
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
		"mode":               "publish",
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
		"mode":               "publish",
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
		"mode":               "publish",
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
	notReady := false
	pf.urlPropertyReady = &notReady
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
		"mode":              "publish",
		"body":              "ride the wave #fyp",
		"targets":           []any{map[string]any{"social_account_id": acctID, "privacy_level": "PUBLIC_TO_EVERYONE"}},
		"media_storage_ids": []any{int64(7)},
	}); err != nil {
		t.Fatal(err)
	}

	if len(pf.nonCreatorInfoCalls()) != 1 || pf.nonCreatorInfoCalls()[0].Tool != "post_video" {
		t.Fatalf("expected one post_video init call: %+v", pf.nonCreatorInfoCalls())
	}
	in := pf.nonCreatorInfoCalls()[0].Input
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

func TestPublishTikTok_UsesPullFromURLWhenDeliveryReady(t *testing.T) {
	ready := true
	pf := newRecordingPlatform()
	pf.urlPropertyReady = &ready
	pf.callAppResponses["storage:files_get"] = json.RawMessage(
		`{"result":{"content":[{"type":"text","text":"{\"id\":17,\"content_type\":\"video/mp4\",\"size_bytes\":4194304}"}]}}`,
	)
	pf.callAppResponses["storage:files_get_url"] = json.RawMessage(
		`{"result":{"content":[{"type":"text","text":"{\"url\":\"https://agents.test/api/apps/storage/files/17/content/v.mp4?sig=x&exp=1999999999\"}"}]}}`,
	)
	pf.executeResponses["post_video"] = &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"data":{"publish_id":"pull_pub_1"}}`)}
	pf.executeResponses["get_publish_status"] = &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"data":{"status":"PUBLISH_COMPLETE","publicaly_available_post_id":["video_post_1"]}}`)}
	ctx := newSocialCtx(t, pf)
	r, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, display_name, status)
		 VALUES ('test-proj', 'tiktok', 42, '@tt', 'active')`,
	)
	acctID, _ := r.LastInsertId()
	app := &App{}
	if _, err := app.toolPostCreate(ctx, map[string]any{
		"mode": "publish", "body": "verified pull", "targets": []any{map[string]any{"social_account_id": acctID, "privacy_level": "PUBLIC_TO_EVERYONE"}}, "media_storage_ids": []any{int64(17)},
	}); err != nil {
		t.Fatal(err)
	}
	if len(pf.nonCreatorInfoCalls()) < 1 || pf.nonCreatorInfoCalls()[0].Tool != "post_video" {
		t.Fatalf("expected post_video: %+v", pf.nonCreatorInfoCalls())
	}
	source := pf.nonCreatorInfoCalls()[0].Input["source_info"].(map[string]any)
	if source["source"] != "PULL_FROM_URL" || source["video_url"] == "" {
		t.Fatalf("expected PULL_FROM_URL input, got %+v", source)
	}
	if _, exists := source["video_size"]; exists {
		t.Fatalf("pull input must not contain FILE_UPLOAD fields: %+v", source)
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
		"mode":              "publish",
		"body":              "long video",
		"targets":           []any{map[string]any{"social_account_id": acctID, "privacy_level": "PUBLIC_TO_EVERYONE"}},
		"media_storage_ids": []any{int64(8)},
	}); err != nil {
		t.Fatal(err)
	}

	if len(pf.nonCreatorInfoCalls()) != 1 || pf.nonCreatorInfoCalls()[0].Tool != "post_video" {
		t.Fatalf("expected post_video init: %+v", pf.nonCreatorInfoCalls())
	}
	srcInfo := pf.nonCreatorInfoCalls()[0].Input["source_info"].(map[string]any)
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

func TestPublishTikTok_PhotoPostInput(t *testing.T) {
	mediaSrv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Errorf("preflight method = %s, want GET", r.Method)
		}
		if got := r.Header.Get("Range"); got != "bytes=0-0" {
			t.Errorf("preflight Range = %q, want bytes=0-0", got)
		}
		w.Header().Set("Content-Type", "image/jpeg")
		w.WriteHeader(http.StatusPartialContent)
		_, _ = w.Write([]byte{0xff})
	}))
	defer mediaSrv.Close()

	pf := newRecordingPlatform()
	pf.callAppResponses["storage:files_get"] = json.RawMessage(
		`{"result":{"content":[{"type":"text","text":"{\"id\":9,\"content_type\":\"image/jpeg\",\"size_bytes\":12345}"}]}}`,
	)
	pf.callAppResponses["storage:files_get_url"] = json.RawMessage(
		fmt.Sprintf(`{"result":{"content":[{"type":"text","text":%q}]}}`, `{"url":"`+mediaSrv.URL+`/p.jpg"}`),
	)
	pf.executeResponses["post_photo"] = &sdk.ExecuteResult{
		Success: true,
		Data:    json.RawMessage(`{"data":{"publish_id":"photo_pub_1"}}`),
	}
	pf.executeResponses["get_publish_status"] = &sdk.ExecuteResult{
		Success: true,
		Data:    json.RawMessage(`{"data":{"status":"PUBLISH_COMPLETE","publicaly_available_post_id":["photo_post_1"]}}`),
	}
	ctx := newSocialCtx(t, pf)
	r, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, display_name, status)
		 VALUES ('test-proj', 'tiktok', 42, '@tt', 'active')`,
	)
	acctID, _ := r.LastInsertId()

	app := &App{}
	if _, err := app.toolPostCreate(ctx, map[string]any{
		"mode":              "publish",
		"media_project_id":  "media-proj",
		"body":              "photo description #fyp",
		"media_storage_ids": []any{int64(9)},
		"targets": []any{
			map[string]any{
				"social_account_id": acctID,
				"privacy_level":     "PUBLIC_TO_EVERYONE",
				"title":             "Photo title",
				"auto_add_music":    true,
			},
		},
	}); err != nil {
		t.Fatal(err)
	}

	if len(pf.nonCreatorInfoCalls()) != 2 || pf.nonCreatorInfoCalls()[0].Tool != "post_photo" || pf.nonCreatorInfoCalls()[1].Tool != "get_publish_status" {
		t.Fatalf("expected post_photo followed by get_publish_status: %+v", pf.nonCreatorInfoCalls())
	}
	in := pf.nonCreatorInfoCalls()[0].Input
	if in["post_mode"] != "DIRECT_POST" || in["media_type"] != "PHOTO" {
		t.Fatalf("unexpected photo mode fields: %+v", in)
	}
	postInfo := in["post_info"].(map[string]any)
	if postInfo["description"] != "photo description #fyp" || postInfo["title"] != "Photo title" {
		t.Fatalf("unexpected post_info: %+v", postInfo)
	}
	if postInfo["privacy_level"] != "PUBLIC_TO_EVERYONE" {
		t.Fatalf("privacy = %v, want PUBLIC_TO_EVERYONE", postInfo["privacy_level"])
	}
	if postInfo["auto_add_music"] != true {
		t.Fatalf("auto_add_music not threaded: %+v", postInfo)
	}
	if postInfo["brand_content_toggle"] != false || postInfo["brand_organic_toggle"] != false {
		t.Fatalf("brand toggles not set false: %+v", postInfo)
	}
	srcInfo := in["source_info"].(map[string]any)
	if srcInfo["source"] != "PULL_FROM_URL" || srcInfo["photo_cover_index"] != 0 {
		t.Fatalf("unexpected source_info: %+v", srcInfo)
	}
	images := srcInfo["photo_images"].([]string)
	if len(images) != 1 || images[0] != mediaSrv.URL+"/p.jpg" {
		t.Fatalf("photo_images = %+v", images)
	}
	var proxyCall *callAppCall
	for i := range pf.callAppCalls {
		call := &pf.callAppCalls[i]
		if call.AppName == "storage" && call.Tool == "files_get_url" && call.Input["delivery"] == "proxy" {
			proxyCall = call
			break
		}
	}
	if proxyCall == nil {
		t.Fatalf("TikTok photo did not request a Storage proxy URL: %+v", pf.callAppCalls)
	}
	if got := proxyCall.Input["_project_id"]; got != "media-proj" {
		t.Fatalf("proxy _project_id = %v, want media-proj", got)
	}
	if got := proxyCall.Input["ttl_seconds"]; got != 7200 {
		t.Fatalf("proxy ttl_seconds = %v, want %d", got, tiktokPhotoURLTTLSeconds)
	}
	if got := proxyCall.Input["disposition"]; got != "inline" {
		t.Fatalf("proxy disposition = %v, want inline", got)
	}
	var status, platformPostID string
	ctx.AppDB().QueryRow(
		`SELECT status, COALESCE(platform_post_id,'') FROM post_targets WHERE social_account_id=? ORDER BY id DESC LIMIT 1`, acctID,
	).Scan(&status, &platformPostID)
	if status != "published" || platformPostID != "photo_post_1" {
		t.Fatalf("target = status %q platform_post_id %q", status, platformPostID)
	}
}

func TestPrepareTikTokPhotoStoresHiddenJPEGDerivative(t *testing.T) {
	sourceImage := image.NewNRGBA(image.Rect(0, 0, 16, 16))
	for y := 4; y < 12; y++ {
		for x := 4; x < 12; x++ {
			i := sourceImage.PixOffset(x, y)
			sourceImage.Pix[i] = 255
			sourceImage.Pix[i+3] = 128
		}
	}
	var source bytes.Buffer
	if err := png.Encode(&source, sourceImage); err != nil {
		t.Fatal(err)
	}

	pf := newRecordingPlatform()
	pf.callAppResponses["storage:files_get_content"] = json.RawMessage(
		fmt.Sprintf(`{"result":{"content":[{"type":"text","text":%q}]}}`,
			`{"content_base64":"`+base64.StdEncoding.EncodeToString(source.Bytes())+`"}`),
	)
	pf.callAppResponses["storage:files_upload"] = json.RawMessage(
		`{"result":{"content":[{"type":"text","text":"{\"id\":109,\"name\":\"9-tiktok-photo-jpeg-v1.jpg\",\"size_bytes\":456}"}]}}`,
	)
	ctx := newSocialCtx(t, pf)
	prepared, err := (&App{}).prepareTikTokPhoto(ctx, mediaItem{
		ID: 9, Mime: "image/png", Name: "source.png", Bytes: int64(source.Len()),
	}, "media-proj")
	if err != nil {
		t.Fatal(err)
	}
	if prepared.ID != 109 || prepared.Mime != "image/jpeg" || prepared.Name != "9-tiktok-photo-jpeg-v1.jpg" {
		t.Fatalf("prepared media = %+v", prepared)
	}

	var upload *callAppCall
	for i := range pf.callAppCalls {
		call := &pf.callAppCalls[i]
		if call.AppName == "storage" && call.Tool == "files_upload" {
			upload = call
			break
		}
	}
	if upload == nil {
		t.Fatalf("files_upload was not called: %+v", pf.callAppCalls)
	}
	if upload.Input["folder"] != tiktokJPEGFolder || upload.Input["source"] != "social-tiktok-derivative" {
		t.Fatalf("hidden derivative identity = %+v", upload.Input)
	}
	if upload.Input["content_type"] != "image/jpeg" || upload.Input["visibility"] != "private" {
		t.Fatalf("derivative delivery metadata = %+v", upload.Input)
	}
	if upload.Input["_project_id"] != "media-proj" {
		t.Fatalf("derivative project = %v, want media-proj", upload.Input["_project_id"])
	}
	tags, ok := upload.Input["tags"].([]string)
	if !ok || strings.Join(tags, ",") != "internal,derived,tiktok" {
		t.Fatalf("derivative tags = %#v", upload.Input["tags"])
	}
	jpegBody, err := base64.StdEncoding.DecodeString(upload.Input["content_base64"].(string))
	if err != nil {
		t.Fatal(err)
	}
	converted, err := jpeg.Decode(bytes.NewReader(jpegBody))
	if err != nil {
		t.Fatalf("uploaded derivative is not JPEG: %v", err)
	}
	r, g, b, _ := converted.At(0, 0).RGBA()
	if r < 50000 || g < 50000 || b < 50000 {
		t.Fatalf("transparent background was not composited onto white: rgb16=(%d,%d,%d)", r, g, b)
	}
}

func TestPrepareTikTokPhotoPassesSupportedFormatsThrough(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newSocialCtx(t, pf)
	for _, mime := range []string{"image/jpeg", "image/webp"} {
		item := mediaItem{ID: 9, URL: "https://cdn.test/image", Mime: mime, Name: "image"}
		got, err := (&App{}).prepareTikTokPhoto(ctx, item, "media-proj")
		if err != nil {
			t.Fatal(err)
		}
		if got != item {
			t.Fatalf("%s changed: got %+v want %+v", mime, got, item)
		}
	}
	if len(pf.callAppCalls) != 0 {
		t.Fatalf("supported formats touched Storage: %+v", pf.callAppCalls)
	}
}

func TestPreflightTikTokPhotoURLRejectsRedirect(t *testing.T) {
	destinationHit := false
	destination := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		destinationHit = true
		w.WriteHeader(http.StatusOK)
	}))
	defer destination.Close()
	redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, destination.URL, http.StatusFound)
	}))
	defer redirect.Close()

	err := preflightTikTokPhotoURL(redirect.URL)
	if err == nil || !strings.Contains(err.Error(), "redirect status 302") {
		t.Fatalf("preflight error = %v, want redirect rejection", err)
	}
	if destinationHit {
		t.Fatal("preflight followed the redirect")
	}
}

func TestPublishTikTok_PhotoPostRejectsMixedMedia(t *testing.T) {
	pf := newRecordingPlatform()
	pf.callAppResponses["storage:files_get"] = json.RawMessage(
		`{"result":{"content":[{"type":"text","text":"{\"id\":9,\"content_type\":\"image/jpeg\",\"size_bytes\":12345}"}]}}`,
	)
	pf.callAppResponses["storage:files_get_url"] = json.RawMessage(
		`{"result":{"content":[{"type":"text","text":"{\"url\":\"https://cdn.test/p.jpg\"}"}]}}`,
	)
	pf.callAppResponses["storage:files_get:10"] = json.RawMessage(
		`{"result":{"content":[{"type":"text","text":"{\"id\":10,\"content_type\":\"video/mp4\",\"size_bytes\":99999}"}]}}`,
	)
	pf.callAppResponses["storage:files_get_url:10"] = json.RawMessage(
		`{"result":{"content":[{"type":"text","text":"{\"url\":\"https://cdn.test/v.mp4\"}"}]}}`,
	)
	ctx := newSocialCtx(t, pf)
	r, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, display_name, status)
		 VALUES ('test-proj', 'tiktok', 42, '@tt', 'active')`,
	)
	acctID, _ := r.LastInsertId()

	app := &App{}
	if _, err := app.toolPostCreate(ctx, map[string]any{
		"mode":              "publish",
		"body":              "mixed media",
		"targets":           []any{map[string]any{"social_account_id": acctID, "privacy_level": "PUBLIC_TO_EVERYONE"}},
		"media_storage_ids": []any{int64(9), int64(10)},
	}); err != nil {
		t.Fatal(err)
	}

	if len(pf.nonCreatorInfoCalls()) != 0 {
		t.Fatalf("TikTok integration should not be called for mixed media: %+v", pf.nonCreatorInfoCalls())
	}
	var lastErr string
	ctx.AppDB().QueryRow(
		`SELECT COALESCE(last_error,'') FROM post_targets WHERE social_account_id=? ORDER BY id DESC LIMIT 1`, acctID,
	).Scan(&lastErr)
	if !strings.Contains(lastErr, "cannot mix images") {
		t.Fatalf("last_error = %q, want mixed-media rejection", lastErr)
	}
}

// --- YouTube upload (resumable) -----------------------------------

func TestPublishYouTube_InitCallShape(t *testing.T) {
	// Verify publishYoutube calls upload_video_init with the right
	// snippet shape (title from post body, default privacy=private),
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
		"mode":               "publish",
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
			if status["selfDeclaredMadeForKids"] != false {
				t.Errorf("selfDeclaredMadeForKids should default false, got %v", status["selfDeclaredMadeForKids"])
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

func TestImportYouTubePreservesDescriptionAndTitle(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["get_my_channel"] = &sdk.ExecuteResult{
		Success: true,
		Data: json.RawMessage(`{
			"items":[{"contentDetails":{"relatedPlaylists":{"uploads":"uploads-1"}}}]
		}`),
	}
	pf.executeResponses["list_playlist_items"] = &sdk.ExecuteResult{
		Success: true,
		Data: json.RawMessage(`{
			"items":[{
				"snippet":{
					"title":"A distinct YouTube title",
					"description":"The full YouTube description.\n\nWith a second paragraph.",
					"publishedAt":"2026-08-23T12:00:00Z",
					"resourceId":{"videoId":"video-1"}
				},
				"contentDetails":{"videoId":"video-1","videoPublishedAt":"2026-08-23T12:00:00Z"}
			}]
		}`),
	}
	ctx := newSocialCtx(t, pf)
	accountRes, err := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, display_name, status)
		 VALUES ('test-proj', 'youtube', 42, 'YouTube Channel', 'active')`,
	)
	if err != nil {
		t.Fatal(err)
	}
	accountID, _ := accountRes.LastInsertId()

	result := (&App{}).importYoutubePosts(
		ctx,
		"test-proj",
		importResult{AccountID: accountID, Platform: "youtube"},
		accountID,
		42,
		0,
		25,
	)
	if result.Status != "ok" || result.Imported != 1 {
		t.Fatalf("import result = %+v, want one imported post", result)
	}

	var body, optionsRaw string
	if err := ctx.AppDB().QueryRow(
		`SELECT p.body, COALESCE(t.options, '')
		   FROM posts p
		   JOIN post_targets t ON t.post_id=p.id
		  WHERE t.social_account_id=? AND t.platform_post_id='video-1'`,
		accountID,
	).Scan(&body, &optionsRaw); err != nil {
		t.Fatal(err)
	}
	if body != "The full YouTube description.\n\nWith a second paragraph." {
		t.Fatalf("post body = %q, want full YouTube description", body)
	}
	var options map[string]any
	if err := json.Unmarshal([]byte(optionsRaw), &options); err != nil {
		t.Fatalf("decode target options %q: %v", optionsRaw, err)
	}
	if options["title"] != "A distinct YouTube title" {
		t.Fatalf("target title = %v, want YouTube title", options["title"])
	}
	if _, duplicated := options["body"]; duplicated {
		t.Fatalf("target options unexpectedly duplicate post body: %s", optionsRaw)
	}
}

func TestPrepareYouTubeThumbnail_PreservesSmallImage(t *testing.T) {
	var source bytes.Buffer
	if err := png.Encode(&source, image.NewRGBA(image.Rect(0, 0, 16, 9))); err != nil {
		t.Fatal(err)
	}

	got, mime, err := prepareYouTubeThumbnail(source.Bytes(), "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if mime != "image/png" {
		t.Fatalf("mime = %q, want image/png", mime)
	}
	if !bytes.Equal(got, source.Bytes()) {
		t.Fatal("small thumbnail bytes changed")
	}
}

func TestPrepareYouTubeThumbnail_CompressesOversizedPNG(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 900, 900))
	var state uint32 = 1
	for i := 0; i < len(img.Pix); i += 4 {
		state = state*1664525 + 1013904223
		img.Pix[i] = byte(state >> 24)
		img.Pix[i+1] = byte(state >> 16)
		img.Pix[i+2] = byte(state >> 8)
		img.Pix[i+3] = 0xff
	}
	var source bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.NoCompression}
	if err := encoder.Encode(&source, img); err != nil {
		t.Fatal(err)
	}
	if int64(source.Len()) <= youtubeThumbnailMaxBytes {
		t.Fatalf("test image is only %d bytes; expected oversized input", source.Len())
	}

	got, mime, err := prepareYouTubeThumbnail(source.Bytes(), "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if mime != "image/jpeg" {
		t.Fatalf("mime = %q, want image/jpeg", mime)
	}
	if int64(len(got)) > youtubeThumbnailMaxBytes {
		t.Fatalf("optimized thumbnail = %d bytes, max %d", len(got), youtubeThumbnailMaxBytes)
	}
	config, err := jpeg.DecodeConfig(bytes.NewReader(got))
	if err != nil {
		t.Fatalf("optimized output is not valid JPEG: %v", err)
	}
	if config.Width != 900 || config.Height != 900 {
		t.Fatalf("optimized dimensions = %dx%d, want 900x900", config.Width, config.Height)
	}
}

func TestPrepareYouTubeThumbnail_RejectsInvalidOversizedImage(t *testing.T) {
	bad := bytes.Repeat([]byte("not-an-image"), int(youtubeThumbnailMaxBytes/12)+1)
	if int64(len(bad)) <= youtubeThumbnailMaxBytes {
		bad = append(bad, make([]byte, youtubeThumbnailMaxBytes-int64(len(bad))+1)...)
	}
	if _, _, err := prepareYouTubeThumbnail(bad, "image/png"); err == nil || !strings.Contains(err.Error(), "decode oversized youtube thumbnail") {
		t.Fatalf("error = %v, want oversized image decode error", err)
	}
}

func TestFetchYouTubeThumbnailEnvelope_UsesOptimizedMime(t *testing.T) {
	img := image.NewNRGBA(image.Rect(0, 0, 900, 900))
	for i := 0; i < len(img.Pix); i += 4 {
		img.Pix[i] = byte(i)
		img.Pix[i+1] = byte(i >> 3)
		img.Pix[i+2] = byte(i >> 7)
		img.Pix[i+3] = 0xff
	}
	var source bytes.Buffer
	encoder := png.Encoder{CompressionLevel: png.NoCompression}
	if err := encoder.Encode(&source, img); err != nil {
		t.Fatal(err)
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(source.Bytes())
	}))
	defer srv.Close()

	envelope, err := fetchYouTubeThumbnailEnvelope(srv.URL, "image/png")
	if err != nil {
		t.Fatal(err)
	}
	if envelope["mimeType"] != "image/jpeg" {
		t.Fatalf("mimeType = %v, want image/jpeg", envelope["mimeType"])
	}
	encoded, _ := envelope["base64"].(string)
	body, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(body)) > youtubeThumbnailMaxBytes {
		t.Fatalf("envelope body = %d bytes, max %d", len(body), youtubeThumbnailMaxBytes)
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
		"mode":               "schedule",
		"body":               "later",
		"social_account_ids": []any{acctID},
		"schedule_at":        "2026-05-01T10:00:00Z",
	})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(map[string]any)
	postID := toInt64Loose(res["post_id"])
	if res["status"] != "scheduled" {
		t.Errorf("post status = %v, want scheduled", res["status"])
	}

	// jobs_schedule was called with run_at + a brokered app_tool target.
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
			if target["kind"] != "app_tool" {
				t.Errorf("target.kind = %v, want app_tool", target["kind"])
			}
			if target["app"] != "social" {
				t.Errorf("target.app = %v, want social", target["app"])
			}
			if tool, _ := target["tool"].(string); tool != "post_publish_scheduled" {
				t.Errorf("target.tool = %v, want post_publish_scheduled", target["tool"])
			}
			input, _ := target["input"].(map[string]any)
			if got := toInt64Loose(input["post_id"]); got != postID {
				t.Errorf("target.input.post_id = %v, want %d", input["post_id"], postID)
			}
			if target["path"] != nil || target["body"] != nil {
				t.Errorf("app_tool target must not contain legacy HTTP fields: %+v", target)
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

func TestSchedule_DedupesIdenticalScheduledPost(t *testing.T) {
	pf := newRecordingPlatform()
	pf.identity.Bindings = map[string]any{"jobs": float64(101)}
	pf.callAppResponses["jobs:jobs_schedule"] = json.RawMessage(`{"job":{"id":100}}`)
	ctx := newSocialCtx(t, pf)
	r, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, display_name, status)
		 VALUES ('test-proj', 'twitter', 42, '@me', 'active')`,
	)
	acctID, _ := r.LastInsertId()

	app := &App{}
	args := map[string]any{
		"mode":               "schedule",
		"body":               "later",
		"social_account_ids": []any{acctID},
		"media_storage_ids":  []any{float64(11)},
		"schedule_at":        "2026-05-01T10:00:00Z",
	}
	first, err := app.toolPostCreate(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	second, err := app.toolPostCreate(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	firstID := first.(map[string]any)["post_id"].(int64)
	secondRes := second.(map[string]any)
	if secondRes["post_id"].(int64) != firstID {
		t.Fatalf("second create made post %v, want existing %d", secondRes["post_id"], firstID)
	}
	if secondRes["deduped"] != true {
		t.Fatalf("second create should report deduped, got %+v", secondRes)
	}
	var postCount int
	if err := ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM posts`).Scan(&postCount); err != nil {
		t.Fatal(err)
	}
	if postCount != 1 {
		t.Fatalf("posts=%d, want 1", postCount)
	}
	schedules := 0
	for _, c := range pf.callAppCalls {
		if c.AppName == "jobs" && c.Tool == "jobs_schedule" {
			schedules++
		}
	}
	if schedules != 1 {
		t.Fatalf("jobs_schedule calls=%d, want 1; calls=%+v", schedules, pf.callAppCalls)
	}
}

func TestSchedule_JobsFailureFallsBackWithoutDuplicatingPost(t *testing.T) {
	pf := newRecordingPlatform()
	pf.identity.Bindings = map[string]any{"jobs": float64(101)}
	pf.callAppResponses["jobs:jobs_schedule"] = json.RawMessage(`not-json`)
	ctx := newSocialCtx(t, pf)
	r, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, display_name, status)
		 VALUES ('test-proj', 'twitter', 42, '@me', 'active')`,
	)
	acctID, _ := r.LastInsertId()

	app := &App{}
	args := map[string]any{
		"mode":               "schedule",
		"body":               "later",
		"social_account_ids": []any{acctID},
		"schedule_at":        "2026-05-01T10:00:00Z",
	}
	out, err := app.toolPostCreate(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	first := out.(map[string]any)
	if first["status"] != "scheduled" || first["worker_fallback"] != true {
		t.Fatalf("expected worker fallback, got %+v", first)
	}
	var postID int64
	var postStatus, targetStatus, lastErr string
	var attempts int
	err = ctx.AppDB().QueryRow(`SELECT id, status FROM posts`).Scan(&postID, &postStatus)
	if err != nil {
		t.Fatal(err)
	}
	err = ctx.AppDB().QueryRow(
		`SELECT status, COALESCE(last_error,''), attempts FROM post_targets WHERE post_id=?`,
		postID,
	).Scan(&targetStatus, &lastErr, &attempts)
	if err != nil {
		t.Fatal(err)
	}
	if postStatus != "scheduled" || targetStatus != "pending" || attempts != 0 || lastErr != "" {
		t.Fatalf("post=%q target=%q attempts=%d err=%q; want scheduled/pending/0", postStatus, targetStatus, attempts, lastErr)
	}

	retry, err := app.toolPostCreate(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	res := retry.(map[string]any)
	if res["post_id"].(int64) != postID {
		t.Fatalf("retry created post %v, want existing %d", res["post_id"], postID)
	}
	if res["deduped"] != true {
		t.Fatalf("duplicate create should reuse worker-scheduled post, got %+v", res)
	}
	var count, jobID int
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM posts`).Scan(&count)
	_ = ctx.AppDB().QueryRow(`SELECT job_id FROM posts WHERE id=?`, postID).Scan(&jobID)
	if count != 1 || jobID != 0 {
		t.Fatalf("count=%d job=%d; want one worker-scheduled post", count, jobID)
	}
	if len(pf.callAppCalls) != 1 {
		t.Fatalf("duplicate create retried jobs scheduling: %+v", pf.callAppCalls)
	}
}

func TestSchedule_UsesWorkerFallbackWhenJobsUnbound(t *testing.T) {
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
		"mode":               "schedule",
		"body":               "later",
		"social_account_ids": []any{acctID},
		"schedule_at":        "2026-05-01T10:00:00Z",
	})
	res := out.(map[string]any)
	if res["status"] != "scheduled" {
		t.Errorf("expected scheduled worker fallback when jobs unbound; got %+v", res)
	}
	var jobID int64
	if err := ctx.AppDB().QueryRow(`SELECT job_id FROM posts WHERE id=?`, res["post_id"]).Scan(&jobID); err != nil {
		t.Fatal(err)
	}
	if jobID != 0 || len(pf.callAppCalls) != 0 {
		t.Fatalf("worker fallback should not create a jobs row: job_id=%d calls=%+v", jobID, pf.callAppCalls)
	}
}

// --- helpers -------------------------------------------------------

func TestPostCreateRejectsCrossProjectAndDuplicateTargets(t *testing.T) {
	ctx := newSocialCtx(t, newRecordingPlatform())
	app := &App{}
	res, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, display_name, status)
		 VALUES ('victim-project', 'twitter', 42, 'victim', 'active')`,
	)
	foreignID, _ := res.LastInsertId()
	out, err := app.toolPostCreate(ctx, map[string]any{
		"mode":               "publish",
		"_project_id":        "victim-project",
		"body":               "must not publish",
		"social_account_ids": []any{foreignID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["isError"] != true {
		t.Fatalf("cross-project target should be rejected: %+v", out)
	}
	var posts int
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM posts`).Scan(&posts)
	if posts != 0 {
		t.Fatalf("cross-project request created %d posts", posts)
	}

	res, _ = ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, display_name, status)
		 VALUES ('test-proj', 'twitter', 43, 'local', 'active')`,
	)
	localID, _ := res.LastInsertId()
	out, err = app.toolPostCreate(ctx, map[string]any{
		"mode":               "publish",
		"body":               "duplicate",
		"social_account_ids": []any{localID, localID},
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["isError"] != true {
		t.Fatalf("duplicate target should be rejected: %+v", out)
	}
}

func TestClaimPostTargetIsAtomic(t *testing.T) {
	ctx := newSocialCtx(t, newRecordingPlatform())
	acct, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, display_name, status)
		 VALUES ('test-proj', 'twitter', 42, 'local', 'active')`,
	)
	acctID, _ := acct.LastInsertId()
	post, _ := ctx.AppDB().Exec(`INSERT INTO posts (project_id, body, status) VALUES ('test-proj', 'x', 'publishing')`)
	postID, _ := post.LastInsertId()
	target, _ := ctx.AppDB().Exec(`INSERT INTO post_targets (post_id, social_account_id) VALUES (?, ?)`, postID, acctID)
	targetID, _ := target.LastInsertId()

	const callers = 16
	var wg sync.WaitGroup
	wins := make(chan bool, callers)
	errs := make(chan error, callers)
	for i := 0; i < callers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			claimed, err := claimPostTarget(ctx, targetID)
			if err != nil {
				errs <- err
				return
			}
			wins <- claimed
		}()
	}
	wg.Wait()
	close(wins)
	close(errs)
	for err := range errs {
		t.Fatalf("claim returned error: %v", err)
	}
	claimed := 0
	for won := range wins {
		if won {
			claimed++
		}
	}
	if claimed != 1 {
		t.Fatalf("atomic claims won=%d, want 1", claimed)
	}
	var attempts int
	_ = ctx.AppDB().QueryRow(`SELECT attempts FROM post_targets WHERE id=?`, targetID).Scan(&attempts)
	if attempts != 1 {
		t.Fatalf("attempts=%d, want 1", attempts)
	}
}

func TestScheduledWorkerFallbackPublishesDuePost(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["post_tweet"] = &sdk.ExecuteResult{Success: true, Status: 201, Data: json.RawMessage(`{"data":{"id":"worker_tweet"}}`)}
	ctx := newSocialCtx(t, pf)
	acct, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, display_name, status)
		 VALUES ('test-proj', 'twitter', 42, 'local', 'active')`,
	)
	acctID, _ := acct.LastInsertId()
	app := &App{}
	out, err := app.toolPostCreate(ctx, map[string]any{
		"mode":               "schedule",
		"body":               "worker post",
		"social_account_ids": []any{acctID},
		"schedule_at":        time.Now().UTC().Add(-time.Minute).Format(time.RFC3339),
	})
	if err != nil {
		t.Fatal(err)
	}
	postID := out.(map[string]any)["post_id"].(int64)
	if err := app.runScheduledPublisher(context.Background(), ctx); err != nil {
		t.Fatal(err)
	}
	var status, platformID string
	_ = ctx.AppDB().QueryRow(`SELECT status, COALESCE(platform_post_id,'') FROM post_targets WHERE post_id=?`, postID).Scan(&status, &platformID)
	if status != "published" || platformID != "worker_tweet" {
		t.Fatalf("worker target status=%q id=%q", status, platformID)
	}
}

func TestAnalyticsCollectorAutomaticallyRefreshesPublishedPostTargets(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["get_tweet_analytics"] = &sdk.ExecuteResult{
		Success: true,
		Status:  http.StatusOK,
		Data: json.RawMessage(`{"data":{"public_metrics":{
			"impression_count":18,"like_count":2,"reply_count":1,
			"retweet_count":1,"quote_count":0,"bookmark_count":3
		}}}`),
	}
	ctx := newSocialCtx(t, pf)
	accountResult, err := ctx.AppDB().Exec(
		`INSERT INTO social_accounts
		   (project_id, platform, connection_id, display_name, status)
		 VALUES ('test-proj', 'twitter', 42, '@test', 'active')`,
	)
	if err != nil {
		t.Fatal(err)
	}
	accountID, _ := accountResult.LastInsertId()
	postResult, err := ctx.AppDB().Exec(
		`INSERT INTO posts (project_id, body, status, published_at)
		 VALUES ('test-proj', 'Published X post', 'published', datetime('now'))`,
	)
	if err != nil {
		t.Fatal(err)
	}
	postID, _ := postResult.LastInsertId()
	targetResult, err := ctx.AppDB().Exec(
		`INSERT INTO post_targets
		   (post_id, social_account_id, status, platform_post_id, published_at)
		 VALUES (?, ?, 'published', 'tweet-1', datetime('now'))`,
		postID, accountID,
	)
	if err != nil {
		t.Fatal(err)
	}
	targetID, _ := targetResult.LastInsertId()

	app := &App{}
	if err := app.collectProjectPostMetrics(context.Background(), ctx, "test-proj"); err != nil {
		t.Fatal(err)
	}
	if len(pf.executeCalls) != 1 || pf.executeCalls[0].Tool != "get_tweet_analytics" {
		t.Fatalf("provider calls = %+v", pf.executeCalls)
	}
	var points, views int
	if err := ctx.AppDB().QueryRow(
		`SELECT COUNT(*), COALESCE(MAX(CASE WHEN metric='views' THEN value END),0)
		   FROM social_metric_points
		  WHERE project_id='test-proj' AND post_target_id=? AND scope='post'`,
		targetID,
	).Scan(&points, &views); err != nil {
		t.Fatal(err)
	}
	if points != 6 || views != 18 {
		t.Fatalf("stored points=%d views=%d", points, views)
	}

	if err := app.collectProjectPostMetrics(context.Background(), ctx, "test-proj"); err != nil {
		t.Fatal(err)
	}
	if len(pf.executeCalls) != 1 {
		t.Fatalf("fresh target was refreshed again: calls=%+v", pf.executeCalls)
	}
}

func TestAnalyticsCollectorBoundsPostRefreshBatch(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["get_tweet_analytics"] = &sdk.ExecuteResult{
		Success: true,
		Status:  http.StatusOK,
		Data:    json.RawMessage(`{"data":{"public_metrics":{"impression_count":1}}}`),
	}
	ctx := newSocialCtx(t, pf)
	accountResult, err := ctx.AppDB().Exec(
		`INSERT INTO social_accounts
		   (project_id, platform, connection_id, display_name, status)
		 VALUES ('test-proj', 'twitter', 42, '@test', 'active')`,
	)
	if err != nil {
		t.Fatal(err)
	}
	accountID, _ := accountResult.LastInsertId()
	tx, err := ctx.AppDB().Begin()
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i < postAnalyticsBatchSize+1; i++ {
		postResult, err := tx.Exec(
			`INSERT INTO posts (project_id, body, status, published_at)
			 VALUES ('test-proj', ?, 'published', datetime('now'))`,
			fmt.Sprintf("post-%d", i),
		)
		if err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
		postID, _ := postResult.LastInsertId()
		if _, err := tx.Exec(
			`INSERT INTO post_targets
			   (post_id, social_account_id, status, platform_post_id, published_at)
			 VALUES (?, ?, 'published', ?, datetime('now'))`,
			postID, accountID, fmt.Sprintf("tweet-%d", i),
		); err != nil {
			tx.Rollback()
			t.Fatal(err)
		}
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}

	if err := (&App{}).collectProjectPostMetrics(context.Background(), ctx, "test-proj"); err != nil {
		t.Fatal(err)
	}
	if len(pf.executeCalls) != postAnalyticsBatchSize {
		t.Fatalf("provider calls = %d, want bounded batch of %d", len(pf.executeCalls), postAnalyticsBatchSize)
	}
}

func TestPostAnalyticsDueUsesAgeAndFailureBackoff(t *testing.T) {
	now := time.Date(2026, 7, 28, 12, 0, 0, 0, time.UTC)
	tests := []struct {
		name        string
		publishedAt time.Time
		lastRun     time.Time
		status      string
		want        bool
	}{
		{name: "new target", publishedAt: now, want: true},
		{name: "recent waits one hour", publishedAt: now.Add(-24 * time.Hour), lastRun: now.Add(-30 * time.Minute), status: "ok", want: false},
		{name: "recent due hourly", publishedAt: now.Add(-24 * time.Hour), lastRun: now.Add(-time.Hour), status: "ok", want: true},
		{name: "month old waits six hours", publishedAt: now.Add(-14 * 24 * time.Hour), lastRun: now.Add(-5 * time.Hour), status: "ok", want: false},
		{name: "old post due weekly", publishedAt: now.Add(-180 * 24 * time.Hour), lastRun: now.Add(-7 * 24 * time.Hour), status: "ok", want: true},
		{name: "failed recent waits six hours", publishedAt: now.Add(-24 * time.Hour), lastRun: now.Add(-5 * time.Hour), status: "failed", want: false},
		{name: "unsupported waits a week", publishedAt: now.Add(-24 * time.Hour), lastRun: now.Add(-6 * 24 * time.Hour), status: "unsupported", want: false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			lastRun := ""
			if !tt.lastRun.IsZero() {
				lastRun = tt.lastRun.Format(time.RFC3339)
			}
			if got := postAnalyticsDue(tt.publishedAt.Format(time.RFC3339), lastRun, tt.status, now); got != tt.want {
				t.Fatalf("postAnalyticsDue() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestWorkersIncludeSchedulingInboxAndAnalytics(t *testing.T) {
	names := map[string]string{}
	for _, worker := range (&App{}).Workers() {
		names[worker.Name] = worker.Schedule
	}
	for name, schedule := range map[string]string{
		"scheduled_publisher": "@every 1m",
		"inbox_collector":     "@every 5m",
		"analytics_collector": "@every 1h",
	} {
		if names[name] != schedule {
			t.Fatalf("worker %q schedule=%q, want %q; all=%+v", name, names[name], schedule, names)
		}
	}
}

func TestJobCallbackReturnsRetryableFailureWithoutCreatingJobs(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["post_tweet"] = &sdk.ExecuteResult{Success: false, Status: 503, Data: json.RawMessage(`{"error":"temporary"}`)}
	ctx := newSocialCtx(t, pf)
	acct, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, display_name, status)
		 VALUES ('test-proj', 'twitter', 42, 'local', 'active')`,
	)
	acctID, _ := acct.LastInsertId()
	post, _ := ctx.AppDB().Exec(`INSERT INTO posts (project_id, body, status, job_id) VALUES ('test-proj', 'x', 'scheduled', 88)`)
	postID, _ := post.LastInsertId()
	_, _ = ctx.AppDB().Exec(`INSERT INTO post_targets (post_id, social_account_id) VALUES (?, ?)`, postID, acctID)
	app := &App{}
	req := httptest.NewRequest(http.MethodPost, "/jobs/publish_post", strings.NewReader(fmt.Sprintf(`{"post_id":%d}`, postID)))
	rr := httptest.NewRecorder()
	app.handleJobPublishPost(rr, req)
	if rr.Code != http.StatusBadGateway {
		t.Fatalf("first callback status=%d body=%s", rr.Code, rr.Body.String())
	}
	var targetStatus string
	_ = ctx.AppDB().QueryRow(`SELECT status FROM post_targets WHERE post_id=?`, postID).Scan(&targetStatus)
	if targetStatus != "pending" {
		t.Fatalf("failed target should be pending for Jobs retry, got %q", targetStatus)
	}
	if len(pf.callAppCalls) != 0 {
		t.Fatalf("publish callback must not create jobs: %+v", pf.callAppCalls)
	}
	pf.executeResponses["post_tweet"] = &sdk.ExecuteResult{Success: true, Status: 201, Data: json.RawMessage(`{"data":{"id":"retry_ok"}}`)}
	rr = httptest.NewRecorder()
	app.handleJobPublishPost(rr, httptest.NewRequest(http.MethodPost, "/jobs/publish_post", strings.NewReader(fmt.Sprintf(`{"post_id":%d}`, postID))))
	if rr.Code != http.StatusOK {
		t.Fatalf("retry callback status=%d body=%s", rr.Code, rr.Body.String())
	}
	_ = ctx.AppDB().QueryRow(`SELECT status FROM post_targets WHERE post_id=?`, postID).Scan(&targetStatus)
	if targetStatus != "published" {
		t.Fatalf("retried target=%q, want published", targetStatus)
	}
}

func TestJobCallbackDoesNotAcknowledgeOverlappingPublisher(t *testing.T) {
	ctx := newSocialCtx(t, newRecordingPlatform())
	acct, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, display_name, status)
		 VALUES ('test-proj', 'twitter', 42, 'local', 'active')`,
	)
	acctID, _ := acct.LastInsertId()
	post, _ := ctx.AppDB().Exec(`INSERT INTO posts (project_id, body, status, job_id) VALUES ('test-proj', 'x', 'publishing', 88)`)
	postID, _ := post.LastInsertId()
	_, _ = ctx.AppDB().Exec(
		`INSERT INTO post_targets (post_id, social_account_id, status, attempts, last_attempt_at)
		 VALUES (?, ?, 'publishing', 1, CURRENT_TIMESTAMP)`,
		postID, acctID,
	)
	rr := httptest.NewRecorder()
	(&App{}).handleJobPublishPost(rr, httptest.NewRequest(http.MethodPost, "/jobs/publish_post", strings.NewReader(fmt.Sprintf(`{"post_id":%d}`, postID))))
	if rr.Code != http.StatusConflict {
		t.Fatalf("overlapping callback status=%d body=%s", rr.Code, rr.Body.String())
	}
}

func TestScheduledPublishToolReturnsPublishedOnlyAfterDownstreamSuccess(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["post_tweet"] = &sdk.ExecuteResult{Success: true, Status: 201, Data: json.RawMessage(`{"data":{"id":"scheduled_ok"}}`)}
	ctx := newSocialCtx(t, pf)
	acct, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, display_name, status)
		 VALUES ('test-proj', 'twitter', 42, 'local', 'active')`,
	)
	acctID, _ := acct.LastInsertId()
	post, _ := ctx.AppDB().Exec(`INSERT INTO posts (project_id, body, status, job_id) VALUES ('test-proj', 'x', 'scheduled', 88)`)
	postID, _ := post.LastInsertId()
	_, _ = ctx.AppDB().Exec(`INSERT INTO post_targets (post_id, social_account_id) VALUES (?, ?)`, postID, acctID)

	out, err := (&App{}).toolPostPublishScheduled(ctx, map[string]any{"post_id": postID})
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	if result["status"] != "published" {
		t.Fatalf("result=%+v, want published", result)
	}
	if len(pf.executeCalls) != 1 {
		t.Fatalf("execute calls=%d, want 1", len(pf.executeCalls))
	}

	out, err = (&App{}).toolPostPublishScheduled(ctx, map[string]any{"post_id": postID})
	if err != nil {
		t.Fatal(err)
	}
	result = out.(map[string]any)
	if result["status"] != "published" || result["idempotent"] != true {
		t.Fatalf("idempotent result=%+v", result)
	}
	if len(pf.executeCalls) != 1 {
		t.Fatalf("idempotent call republished target: calls=%d", len(pf.executeCalls))
	}
}

func TestScheduledPublishToolAcknowledgesDeterministicFailureWithoutRetry(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["post_tweet"] = &sdk.ExecuteResult{Success: false, Status: 400, Data: json.RawMessage(`{"error":"permanent"}`)}
	ctx := newSocialCtx(t, pf)
	acct, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, display_name, status)
		 VALUES ('test-proj', 'twitter', 42, 'local', 'active')`,
	)
	acctID, _ := acct.LastInsertId()
	post, _ := ctx.AppDB().Exec(`INSERT INTO posts (project_id, body, status, job_id) VALUES ('test-proj', 'x', 'publishing', 88)`)
	postID, _ := post.LastInsertId()
	_, _ = ctx.AppDB().Exec(
		`INSERT INTO post_targets (post_id, social_account_id, status, attempts) VALUES (?, ?, 'pending', 0)`,
		postID, acctID,
	)

	out, err := (&App{}).toolPostPublishScheduled(ctx, map[string]any{"post_id": postID})
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	if result["status"] != "terminal" || result["retryable"] != false {
		t.Fatalf("result=%+v, want terminal non-retryable", result)
	}
	if !strings.Contains(toString(result["error"]), "upstream 400") {
		t.Fatalf("result error=%q, want downstream error", result["error"])
	}
	var targetStatus, postStatus string
	var attempts int
	_ = ctx.AppDB().QueryRow(`SELECT status, attempts FROM post_targets WHERE post_id=?`, postID).Scan(&targetStatus, &attempts)
	_ = ctx.AppDB().QueryRow(`SELECT status FROM posts WHERE id=?`, postID).Scan(&postStatus)
	if targetStatus != "failed" || postStatus != "failed" || attempts != 1 {
		t.Fatalf("target=%q post=%q attempts=%d", targetStatus, postStatus, attempts)
	}
	out, err = (&App{}).toolPostPublishScheduled(ctx, map[string]any{"post_id": postID})
	if err != nil || out.(map[string]any)["status"] != "terminal" || len(pf.executeCalls) != 1 {
		t.Fatalf("terminal callback retried provider: out=%+v err=%v calls=%d", out, err, len(pf.executeCalls))
	}
}

func TestScheduledPublishToolCannotCrossProjects(t *testing.T) {
	ctx := newSocialCtx(t, newRecordingPlatform())
	post, _ := ctx.AppDB().Exec(`INSERT INTO posts (project_id, body, status) VALUES ('other-project', 'x', 'scheduled')`)
	postID, _ := post.LastInsertId()
	out, err := (&App{}).toolPostPublishScheduled(ctx, map[string]any{"post_id": postID, "_project_id": "other-project"})
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	if result["status"] != "failed" || result["error"] != "post not found" {
		t.Fatalf("foreign result=%+v", result)
	}
}

func TestPendingAccountsAreScopedExpiredAndSingleUse(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newSocialCtx(t, pf)
	app := &App{}
	foreign, _ := ctx.AppDB().Exec(
		`INSERT INTO pending_accounts (project_id, platform, integration_slug, connection_id, status, expires_at)
		 VALUES ('other-project', 'twitter', 'twitter-api', 42, 'ready', datetime('now','+10 minutes'))`,
	)
	foreignID, _ := foreign.LastInsertId()
	out, _ := app.toolAccountListPendingPages(ctx, map[string]any{"pending_account_id": foreignID, "_project_id": "other-project"})
	if out.(map[string]any)["isError"] != true {
		t.Fatalf("foreign pending row was visible: %+v", out)
	}
	expired, _ := ctx.AppDB().Exec(
		`INSERT INTO pending_accounts (project_id, platform, integration_slug, connection_id, status, expires_at)
		 VALUES ('test-proj', 'twitter', 'twitter-api', 42, 'ready', datetime('now','-1 minute'))`,
	)
	expiredID, _ := expired.LastInsertId()
	out, _ = app.toolAccountFinalize(ctx, map[string]any{"pending_account_id": expiredID})
	if out.(map[string]any)["isError"] != true {
		t.Fatalf("expired pending row finalized: %+v", out)
	}
	ready, _ := ctx.AppDB().Exec(
		`INSERT INTO pending_accounts (project_id, platform, integration_slug, connection_id, status, expires_at)
		 VALUES ('test-proj', 'twitter', 'twitter-api', 42, 'ready', datetime('now','+10 minutes'))`,
	)
	readyID, _ := ready.LastInsertId()
	first, err := app.toolAccountFinalize(ctx, map[string]any{"pending_account_id": readyID})
	if err != nil || first.(map[string]any)["social_account_id"] == nil {
		t.Fatalf("first finalize failed: out=%+v err=%v", first, err)
	}
	second, _ := app.toolAccountFinalize(ctx, map[string]any{"pending_account_id": readyID})
	if second.(map[string]any)["isError"] != true {
		t.Fatalf("pending row replay succeeded: %+v", second)
	}
	var accounts int
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM social_accounts WHERE project_id='test-proj'`).Scan(&accounts)
	if accounts != 1 {
		t.Fatalf("finalize replay created %d accounts", accounts)
	}
}

func TestOAuthCallbackRejectsUnrelatedConnection(t *testing.T) {
	pf := newRecordingPlatform()
	pf.connections = []sdk.PlatformConnection{{ID: 7, AppSlug: "twitter-api", ProjectID: "test-proj", Status: "active"}}
	ctx := newSocialCtx(t, pf)
	res, _ := ctx.AppDB().Exec(
		`INSERT INTO pending_accounts (project_id, platform, integration_slug, status, expires_at)
		 VALUES ('test-proj', 'twitter', 'twitter-api', 'pending_oauth', datetime('now','+10 minutes'))`,
	)
	id, _ := res.LastInsertId()
	app := &App{}
	rr := httptest.NewRecorder()
	app.handleOAuthDone(rr, httptest.NewRequest(http.MethodGet,
		fmt.Sprintf("/accounts/oauth_done?pending=%d&conn_id=99&status=ok&project_id=test-proj", id), nil))
	if !strings.Contains(rr.Body.String(), "social.oauth_error") {
		t.Fatalf("callback did not report rejection: %s", rr.Body.String())
	}
	var status string
	_ = ctx.AppDB().QueryRow(`SELECT status FROM pending_accounts WHERE id=?`, id).Scan(&status)
	if status != "pending_oauth" {
		t.Fatalf("unrelated connection changed pending status to %q", status)
	}
}

func TestAccountDisconnectPreservesHistoryAndHardDeleteRemovesIt(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newSocialCtx(t, pf)
	acct, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, display_name, status)
		 VALUES ('test-proj', 'twitter', 42, 'local', 'active')`,
	)
	acctID, _ := acct.LastInsertId()
	post, _ := ctx.AppDB().Exec(`INSERT INTO posts (project_id, body, status) VALUES ('test-proj', 'history', 'published')`)
	postID, _ := post.LastInsertId()
	_, _ = ctx.AppDB().Exec(`INSERT INTO post_targets (post_id, social_account_id, status, platform_post_id) VALUES (?, ?, 'published', 'tweet')`, postID, acctID)
	app := &App{}
	if _, err := app.toolAccountDisconnect(ctx, map[string]any{"id": acctID}); err != nil {
		t.Fatal(err)
	}
	var status string
	var targets int
	_ = ctx.AppDB().QueryRow(`SELECT status FROM social_accounts WHERE id=?`, acctID).Scan(&status)
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM post_targets WHERE social_account_id=?`, acctID).Scan(&targets)
	if status != "disconnected" || targets != 1 {
		t.Fatalf("soft disconnect status=%q targets=%d", status, targets)
	}
	out, err := app.toolAccountDisconnect(ctx, map[string]any{"id": acctID, "hard_delete": true, "delete_posts": true})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["hard_delete"] != true {
		t.Fatalf("hard delete response: %+v", out)
	}
	var accounts, posts int
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM social_accounts WHERE id=?`, acctID).Scan(&accounts)
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM posts WHERE id=?`, postID).Scan(&posts)
	if accounts != 0 || posts != 0 {
		t.Fatalf("hard delete left account=%d post=%d", accounts, posts)
	}
}

func TestInstagramInboxUsesRequestProjectNotEnvironment(t *testing.T) {
	t.Setenv("APTEVA_PROJECT_ID", "wrong-project")
	pf := newRecordingPlatform()
	pf.executeResponses["list_conversations"] = &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"data":[]}`)}
	pf.executeResponses["list_my_tags"] = &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"data":[]}`)}
	ctx := newSocialCtx(t, pf)
	res, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, external_account_id, display_name, status, page_credentials)
		 VALUES ('test-proj', 'instagram', 42, 'ig-user', 'IG', 'active', '{"access_token":"page-token"}')`,
	)
	accountID, _ := res.LastInsertId()
	out, err := (&App{}).toolInboxSync(ctx, map[string]any{"social_account_ids": []any{accountID}})
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(out)
	if !strings.Contains(string(raw), `"status":"ok"`) {
		t.Fatalf("Instagram sync used wrong project scope: %s", raw)
	}
}

func TestPostEditOnlyTouchesRequestedTarget(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["facebook_update_post"] = &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"success":true}`)}
	ctx := newSocialCtx(t, pf)
	acct1, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, display_name, status, page_credentials)
		 VALUES ('test-proj', 'facebook', 41, 'one', 'active', '{"access_token":"token-1"}')`,
	)
	id1, _ := acct1.LastInsertId()
	acct2, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, display_name, status, page_credentials)
		 VALUES ('test-proj', 'facebook', 42, 'two', 'active', '{"access_token":"token-2"}')`,
	)
	id2, _ := acct2.LastInsertId()
	post, _ := ctx.AppDB().Exec(`INSERT INTO posts (project_id, body, status) VALUES ('test-proj', 'old', 'published')`)
	postID, _ := post.LastInsertId()
	_, _ = ctx.AppDB().Exec(`INSERT INTO post_targets (post_id, social_account_id, status, platform_post_id) VALUES (?, ?, 'published', 'post-1')`, postID, id1)
	_, _ = ctx.AppDB().Exec(`INSERT INTO post_targets (post_id, social_account_id, status, platform_post_id) VALUES (?, ?, 'published', 'post-2')`, postID, id2)
	out, err := (&App{}).toolPostEdit(ctx, map[string]any{
		"post_id": postID,
		"targets": []any{map[string]any{"social_account_id": id2, "body": "only two"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(pf.executeCalls) != 1 || pf.executeCalls[0].ConnID != 42 {
		t.Fatalf("edit fan-out touched unrequested targets: %+v; out=%+v", pf.executeCalls, out)
	}
	priorCalls := len(pf.executeCalls)
	out, _ = (&App{}).toolPostEdit(ctx, map[string]any{
		"post_id": postID,
		"body":    "must not persist",
		"targets": []any{map[string]any{"social_account_id": int64(9999), "body": "bad"}},
	})
	if out.(map[string]any)["isError"] != true || len(pf.executeCalls) != priorCalls {
		t.Fatalf("unknown target should fail before edits: out=%+v calls=%+v", out, pf.executeCalls)
	}
	var body string
	_ = ctx.AppDB().QueryRow(`SELECT body FROM posts WHERE id=?`, postID).Scan(&body)
	if body != "old" {
		t.Fatalf("invalid targeted edit changed local body to %q", body)
	}
}

func TestYouTubeMetadataMergePreservesUnspecifiedFields(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["get_video"] = &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{
		"items":[{"snippet":{"title":"Keep title","description":"Keep description","tags":["one"],"categoryId":"22"},
		"status":{"privacyStatus":"public","license":"youtube","embeddable":true,"selfDeclaredMadeForKids":false}}]}`)}
	ctx := newSocialCtx(t, pf)
	input, err := (&App{}).youtubeMergedUpdateInput(ctx, 42, "video-1",
		map[string]any{"visibility": "unlisted"}, map[string]any{"visibility": "unlisted"})
	if err != nil {
		t.Fatal(err)
	}
	snippet := input["snippet"].(map[string]any)
	status := input["status"].(map[string]any)
	if snippet["title"] != "Keep title" || snippet["description"] != "Keep description" || snippet["categoryId"] != "22" {
		t.Fatalf("snippet fields were not preserved: %+v", snippet)
	}
	if status["privacyStatus"] != "unlisted" || status["selfDeclaredMadeForKids"] != false || status["embeddable"] != true {
		t.Fatalf("status merge lost fields: %+v", status)
	}
}

func TestYouTubeThumbnailOnlyEditDoesNotRewriteMetadata(t *testing.T) {
	imageServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write([]byte("small-png"))
	}))
	defer imageServer.Close()
	pf := newRecordingPlatform()
	pf.callAppResponses["storage:files_get"] = json.RawMessage(
		`{"id":4903,"content_type":"image/png","size_bytes":9}`,
	)
	pf.callAppResponses["storage:files_get_url"] = json.RawMessage(fmt.Sprintf(`{"url":%q}`, imageServer.URL))
	pf.executeResponses["set_thumbnail"] = &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"items":[]}`)}
	ctx := newSocialCtx(t, pf)
	acct, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (project_id, platform, connection_id, display_name, status)
		 VALUES ('test-proj', 'youtube', 42, 'channel', 'active')`,
	)
	acctID, _ := acct.LastInsertId()
	post, _ := ctx.AppDB().Exec(
		`INSERT INTO posts (project_id, body, status, media_project_id) VALUES ('test-proj', 'description', 'published', 'test-proj')`,
	)
	postID, _ := post.LastInsertId()
	_, _ = ctx.AppDB().Exec(
		`INSERT INTO post_targets (post_id, social_account_id, status, platform_post_id, options)
		 VALUES (?, ?, 'published', 'video-1', '{"title":"Keep title","body":"Keep description"}')`,
		postID, acctID,
	)
	out, err := (&App{}).toolPostEdit(ctx, map[string]any{
		"post_id": postID,
		"targets": []any{map[string]any{
			"social_account_id":    acctID,
			"thumbnail_storage_id": int64(4903),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(pf.executeCalls) != 1 || pf.executeCalls[0].Tool != "set_thumbnail" {
		t.Fatalf("thumbnail-only edit rewrote metadata: calls=%+v out=%+v", pf.executeCalls, out)
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

	app := &App{avatarClient: srv.Client()}
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

func TestCacheAvatar_OmitsUntrustedFallbackOnUpstreamError(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("DB_PATH", dir+"/app.db")
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", 500)
	}))
	defer srv.Close()
	app := &App{avatarClient: srv.Client()}
	ctx := tk.NewAppCtx(t, "apteva.yaml")
	upstream := srv.URL + "/img"
	if got := app.cacheAvatar(ctx, upstream); got != "" {
		t.Errorf("expected omitted avatar on 5xx, got %q", got)
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
		"mode":               "schedule",
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
		"mode":               "schedule",
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
		"mode":               "schedule",
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

	// A failed remote deletion retains local rows for an explicit retry.
	var n int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM posts WHERE id=?`, postID).Scan(&n)
	if n != 1 {
		t.Errorf("post must remain after an upstream failure")
	}
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM post_targets WHERE post_id=?`, postID).Scan(&n)
	if n == 0 {
		t.Errorf("targets must remain after an upstream failure")
	}
}

func TestPostDelete_ZernioPublishedPostUnsupported(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["delete_post"] = &sdk.ExecuteResult{
		Success: false,
		Status:  400,
		Data:    json.RawMessage(`{"error":"Published posts cannot be deleted"}`),
	}
	ctx := newSocialCtx(t, pf)
	app := &App{}

	accountRes, _ := ctx.AppDB().Exec(
		`INSERT INTO social_accounts (
			project_id, platform, connection_id, display_name, status,
			provider_slug, provider_account_id
		) VALUES ('test-proj', 'linkedin', 36, 'AgentDojo', 'active', 'zernio', 'za_1')`,
	)
	accountID, _ := accountRes.LastInsertId()
	postRes, _ := ctx.AppDB().Exec(
		`INSERT INTO posts (project_id, body, status) VALUES ('test-proj', 'hi', 'published')`,
	)
	postID, _ := postRes.LastInsertId()
	_, _ = ctx.AppDB().Exec(
		`INSERT INTO post_targets (
			post_id, social_account_id, status, platform_post_id, provider_post_id
		) VALUES (?, ?, 'published', 'urn:li:share:1', 'zp_1')`,
		postID, accountID,
	)

	out, err := app.toolPostDelete(ctx, map[string]any{"post_id": postID})
	if err != nil {
		t.Fatal(err)
	}
	upstream := out.(map[string]any)["upstream"].([]targetDeleteOutcome)
	if len(upstream) != 1 {
		t.Fatalf("upstream len = %d, want 1", len(upstream))
	}
	if upstream[0].Status != "unsupported" {
		t.Fatalf("zernio outcome = %+v, want unsupported", upstream[0])
	}
	if len(pf.executeCalls) != 1 || pf.executeCalls[0].Tool != "delete_post" || pf.executeCalls[0].Input["postId"] != "zp_1" {
		t.Fatalf("delete_post call = %+v", pf.executeCalls)
	}
	var n int
	ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM posts WHERE id=?`, postID).Scan(&n)
	if n != 0 {
		t.Fatal("post row should be deleted locally")
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

func TestYouTubeBreakdownMapsDeviceFilterAndRows(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["query_analytics_report"] = &sdk.ExecuteResult{
		Success: true,
		Status:  200,
		Data: json.RawMessage(`{
			"columnHeaders":[
				{"name":"deviceType","columnType":"DIMENSION"},
				{"name":"views","columnType":"METRIC"},
				{"name":"estimatedMinutesWatched","columnType":"METRIC"}
			],
			"rows":[["TV",420,900],["MOBILE",120,240]]
		}`),
	}
	ctx := newSocialCtx(t, pf)
	got := (&App{}).fetchYouTubeBreakdown(ctx, 12, "device", analyticsQuery{
		RangeDays: 28,
		Filters:   map[string][]string{"device": {"tv"}},
	})
	if got.Status != "ok" || len(got.Rows) != 2 {
		t.Fatalf("breakdown = %+v", got)
	}
	if got.Rows[0].Dimensions["device"] != "tv" || got.Rows[0].Metrics["views"] != 420 || got.Rows[0].Metrics["watch_time_minutes"] != 900 {
		t.Fatalf("first row = %+v", got.Rows[0])
	}
	if len(pf.executeCalls) != 1 {
		t.Fatalf("calls = %d, want 1", len(pf.executeCalls))
	}
	input := pf.executeCalls[0].Input
	if input["dimensions"] != "deviceType" || input["filters"] != "deviceType==TV" || input["sort"] != "-views" {
		t.Fatalf("query input = %+v", input)
	}
}

func TestAnalyticsQueryCanonicalizesDimensions(t *testing.T) {
	query := analyticsQueryFromArgs(map[string]any{
		"range":      "90d",
		"breakdowns": []any{"deviceType", "traffic-source", "unknown"},
		"filters": map[string]any{
			"operatingSystem": []any{"ios", "android"},
		},
		"device": "tv,mobile",
	})
	if query.RangeDays != 90 {
		t.Fatalf("range = %d", query.RangeDays)
	}
	if strings.Join(query.Breakdowns, ",") != "device,traffic_source" {
		t.Fatalf("breakdowns = %v", query.Breakdowns)
	}
	if strings.Join(query.Filters["os"], ",") != "ios,android" || strings.Join(query.Filters["device"], ",") != "tv,mobile" {
		t.Fatalf("filters = %#v", query.Filters)
	}
}

func TestMetricBreakdownsPersistWithoutPollutingHistory(t *testing.T) {
	ctx := newSocialCtx(t, newRecordingPlatform())
	result := accountMetricsResult{
		SocialAccountID: 42,
		ProfileID:       3,
		Platform:        "youtube",
		Status:          "ok",
		Insights: insightSeries{
			"views": {{Time: "2026-08-15", Value: 1000}},
		},
		Breakdowns: []analyticsBreakdown{{
			Dimension: "device",
			Status:    "ok",
			Rows: []analyticsBreakdownRow{
				{Dimensions: map[string]string{"device": "tv"}, Metrics: map[string]int64{"views": 600}},
				{Dimensions: map[string]string{"device": "mobile"}, Metrics: map[string]int64{"views": 400}},
			},
		}},
	}
	if err := (&App{}).persistAccountMetrics(ctx, "test-proj", result, analyticsQuery{RangeDays: 28}); err != nil {
		t.Fatal(err)
	}
	history := loadAccountMetricHistory(ctx, "test-proj", 42, 730)
	if len(history["views"]) != 1 || history["views"][0].Value != 1000 {
		t.Fatalf("history polluted by dimensions: %#v", history)
	}
	breakdowns := loadAccountBreakdowns(ctx, "test-proj", 42, (analyticsQuery{RangeDays: 28}).cacheKey())
	if len(breakdowns) != 1 || len(breakdowns[0].Rows) != 2 {
		t.Fatalf("stored breakdowns = %#v", breakdowns)
	}
	if breakdowns[0].Rows[0].Metrics["views"]+breakdowns[0].Rows[1].Metrics["views"] != 1000 {
		t.Fatalf("stored values = %#v", breakdowns[0].Rows)
	}
}

func TestAccountComparisonFiltersUnsupportedAccounts(t *testing.T) {
	results := []accountMetricsResult{
		{
			SocialAccountID: 1, DisplayName: "YouTube A", Platform: "youtube", Views: 1000,
			Breakdowns: []analyticsBreakdown{{Dimension: "device", Status: "ok", Rows: []analyticsBreakdownRow{
				{Dimensions: map[string]string{"device": "tv"}, Metrics: map[string]int64{"views": 700}},
			}}},
		},
		{SocialAccountID: 2, DisplayName: "X A", Platform: "twitter", Views: 5000},
	}
	comparison := aggregateAccountComparison(results, "views", map[string][]string{"device": {"tv"}})
	if len(comparison.Rows) != 1 {
		t.Fatalf("rows = %#v, want only supported account", comparison.Rows)
	}
	if comparison.Rows[0].Dimensions["account_name"] != "YouTube A" || comparison.Rows[0].Metrics["views"] != 700 {
		t.Fatalf("comparison = %#v", comparison.Rows[0])
	}
}

func (p *recordingPlatform) nonCreatorInfoCalls() []executeCall {
	var out []executeCall
	for _, c := range p.executeCalls {
		if c.Tool != "query_creator_info" {
			out = append(out, c)
		}
	}
	return out
}
