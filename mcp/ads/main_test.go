package main

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

// recordingPlatform mirrors the social app's testkit pattern: embed
// BasePlatformClient and override only what each test needs. Tests
// that exercise CallAppResult feed wrapped JSON-RPC envelopes through
// callAppResponses to match what the real platform proxy returns.
type recordingPlatform struct {
	tk.BasePlatformClient
	mu                 sync.Mutex
	executeCalls       []executeCall
	callAppCalls       []callAppCall
	startOAuthCalls    []sdk.OAuthStartRequest
	disconnectCalls    []int64
	nextStartOAuth     *sdk.OAuthStartResult
	nextStartErr       error
	listConnections    []sdk.PlatformConnection
	listConnectionsErr error
	executeResponses   map[string]*sdk.ExecuteResult
	callAppResponses   map[string]json.RawMessage
	identity           *sdk.InstallIdentity
}

type executeCall struct {
	ConnID int64
	Tool   string
	Input  map[string]any
}

type callAppCall struct {
	AppName string
	Tool    string
	Input   map[string]any
}

func newRecordingPlatform() *recordingPlatform {
	return &recordingPlatform{
		identity: &sdk.InstallIdentity{
			AppName:   "ads",
			InstallID: 99,
			ProjectID: "test-proj",
		},
		executeResponses: map[string]*sdk.ExecuteResult{},
		callAppResponses: map[string]json.RawMessage{},
	}
}

func (p *recordingPlatform) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	return &sdk.PlatformConnection{ID: id, AppSlug: "facebook-ads", ProjectID: "test-proj"}, nil
}
func (p *recordingPlatform) ListConnections(filter sdk.ConnectionFilter) ([]sdk.PlatformConnection, error) {
	if p.listConnectionsErr != nil {
		return nil, p.listConnectionsErr
	}
	if len(p.listConnections) == 0 {
		return nil, nil
	}
	out := []sdk.PlatformConnection{}
	for _, c := range p.listConnections {
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
func (p *recordingPlatform) WhoAmI() (*sdk.InstallIdentity, error) { return p.identity, nil }

func (p *recordingPlatform) ExecuteIntegrationTool(connID int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	p.mu.Lock()
	p.executeCalls = append(p.executeCalls, executeCall{ConnID: connID, Tool: tool, Input: input})
	p.mu.Unlock()
	if r, ok := p.executeResponses[tool]; ok {
		return r, nil
	}
	return &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{"id":"123"}`)}, nil
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

func (p *recordingPlatform) CallAppResult(appName, tool string, input map[string]any, out any) error {
	raw, err := p.CallApp(appName, tool, input)
	if err != nil {
		return err
	}
	if out == nil || len(raw) == 0 {
		return nil
	}
	// Match the production envelope-strip path so fixtures can feed
	// either wrapped or unwrapped JSON.
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
func (p *recordingPlatform) GetGrants(int64) (*sdk.GrantsResponse, error) {
	return &sdk.GrantsResponse{}, nil
}

// --- helpers --------------------------------------------------------

func newAdsCtx(t *testing.T, pf sdk.PlatformClient) *sdk.AppCtx {
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

func TestAccountAdd_StartsOAuth(t *testing.T) {
	pf := newRecordingPlatform()
	pf.listConnections = []sdk.PlatformConnection{{
		ID: 6, AppSlug: "facebook-ads", ProjectID: "test-proj", Status: "failed",
	}}
	ctx := newAdsCtx(t, pf)
	app := &App{}

	out, err := app.toolAccountAdd(ctx, map[string]any{"platform": "meta"})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(map[string]any)
	if res["authorize_url"] != "https://example.com/authorize?state=abc" {
		t.Fatalf("unexpected authorize_url: %v", res["authorize_url"])
	}
	if len(pf.startOAuthCalls) != 1 {
		t.Fatalf("expected 1 StartOAuth call, got %d", len(pf.startOAuthCalls))
	}
	if pf.startOAuthCalls[0].IntegrationSlug != "facebook-ads" {
		t.Fatalf("wrong slug: %s", pf.startOAuthCalls[0].IntegrationSlug)
	}
	// Pending row was inserted.
	var n int
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM pending_accounts`).Scan(&n)
	if n != 1 {
		t.Fatalf("expected 1 pending_accounts row, got %d", n)
	}
}

func TestAccountAdd_StartsGoogleOAuth(t *testing.T) {
	pf := newRecordingPlatform()
	pf.listConnections = []sdk.PlatformConnection{{
		ID: 6, AppSlug: "google-ads", ProjectID: "test-proj", Status: "pending",
	}}
	ctx := newAdsCtx(t, pf)
	app := &App{}

	out, err := app.toolAccountAdd(ctx, map[string]any{"platform": "google", "force_new": true})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(map[string]any)
	if res["authorize_url"] != "https://example.com/authorize?state=abc" {
		t.Fatalf("unexpected authorize_url: %v", res["authorize_url"])
	}
	if len(pf.startOAuthCalls) != 1 {
		t.Fatalf("expected 1 StartOAuth call, got %d", len(pf.startOAuthCalls))
	}
	if pf.startOAuthCalls[0].IntegrationSlug != "google-ads" {
		t.Fatalf("wrong slug: %s", pf.startOAuthCalls[0].IntegrationSlug)
	}
}

func TestAccountAdd_RejectsUnknownPlatform(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}
	out, err := app.toolAccountAdd(ctx, map[string]any{"platform": "yahoo-ads"})
	if err != nil {
		t.Fatal(err)
	}
	res := out.(map[string]any)
	if res["isError"] != true {
		t.Fatalf("expected isError, got %#v", res)
	}
}

func TestAccountAdd_RollsBackPendingOnOAuthErr(t *testing.T) {
	pf := newRecordingPlatform()
	pf.listConnections = []sdk.PlatformConnection{{
		ID: 6, AppSlug: "facebook-ads", ProjectID: "test-proj", Status: "failed",
	}}
	pf.nextStartErr = errors.New("oauth provider down")
	ctx := newAdsCtx(t, pf)
	app := &App{}
	out, _ := app.toolAccountAdd(ctx, map[string]any{"platform": "meta"})
	res := out.(map[string]any)
	if res["isError"] != true {
		t.Fatalf("expected isError, got %#v", res)
	}
	var n int
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM pending_accounts`).Scan(&n)
	if n != 0 {
		t.Fatalf("expected 0 pending_accounts rows after rollback, got %d", n)
	}
}

func TestAccountAdd_RequiresConfiguredIntegration(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}
	out, err := app.toolAccountAdd(ctx, map[string]any{"platform": "meta"})
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	if result["isError"] != true || result["code"] != "integration_setup_required" {
		t.Fatalf("expected setup-required error, got %#v", result)
	}
	if result["integration_slug"] != "facebook-ads" || result["setup_url"] != "/integrations?app=facebook-ads" {
		t.Fatalf("missing setup metadata: %#v", result)
	}
	if len(pf.startOAuthCalls) != 0 {
		t.Fatalf("OAuth must not start before integration setup: %#v", pf.startOAuthCalls)
	}
	var pending int
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM pending_accounts`).Scan(&pending)
	if pending != 0 {
		t.Fatalf("pending row created before integration setup")
	}
}

func TestPlatforms_ReportsSetupRequired(t *testing.T) {
	pf := newRecordingPlatform()
	newAdsCtx(t, pf)
	app := &App{}
	request := httptest.NewRequest(http.MethodGet, "/platforms?project_id=test-proj", nil)
	response := httptest.NewRecorder()
	app.handlePlatforms(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Platforms []map[string]any `json:"platforms"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	if len(payload.Platforms) != 2 {
		t.Fatalf("platforms=%#v", payload.Platforms)
	}
	for _, platform := range payload.Platforms {
		if platform["can_add"] != false || platform["configured"] != false || platform["state"] != "setup_required" {
			t.Fatalf("unconfigured platform state is wrong: %#v", platform)
		}
		if platform["setup_url"] == "" {
			t.Fatalf("setup URL missing: %#v", platform)
		}
	}
}

func TestPlatforms_DistinguishesReadyAndConnected(t *testing.T) {
	pf := newRecordingPlatform()
	pf.listConnections = []sdk.PlatformConnection{
		{ID: 6, AppSlug: "facebook-ads", ProjectID: "test-proj", Status: "failed"},
		{ID: 8, AppSlug: "google-ads", ProjectID: "test-proj", Status: "active"},
	}
	newAdsCtx(t, pf)
	app := &App{}
	request := httptest.NewRequest(http.MethodGet, "/platforms?project_id=test-proj", nil)
	response := httptest.NewRecorder()
	app.handlePlatforms(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Platforms []map[string]any `json:"platforms"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	byPlatform := map[string]map[string]any{}
	for _, platform := range payload.Platforms {
		byPlatform[platform["platform"].(string)] = platform
	}
	meta := byPlatform["meta"]
	if meta["state"] != "ready" || meta["configured"] != true || meta["can_add"] != true || meta["available"] != false {
		t.Fatalf("Meta readiness state is wrong: %#v", meta)
	}
	google := byPlatform["google"]
	if google["state"] != "connected" || google["connection_count"] != float64(1) || google["available"] != true {
		t.Fatalf("Google connected state is wrong: %#v", google)
	}
}

func TestPlatforms_ReportsReadinessFailureAsUnavailable(t *testing.T) {
	pf := newRecordingPlatform()
	pf.listConnectionsErr = errors.New("platform API unavailable")
	newAdsCtx(t, pf)
	app := &App{}
	request := httptest.NewRequest(http.MethodGet, "/platforms?project_id=test-proj", nil)
	response := httptest.NewRecorder()
	app.handlePlatforms(response, request)
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var payload struct {
		Platforms []map[string]any `json:"platforms"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &payload); err != nil {
		t.Fatal(err)
	}
	for _, platform := range payload.Platforms {
		if platform["state"] != "unavailable" || platform["can_add"] != false || platform["configured"] != false {
			t.Fatalf("readiness failure state is wrong: %#v", platform)
		}
		if platform["unavailable_reason"] != "Could not check integration setup. Refresh and try again." {
			t.Fatalf("readiness failure message is wrong: %#v", platform)
		}
	}
}

func TestOAuthDone_RequiresMatchingActiveConnection(t *testing.T) {
	pf := newRecordingPlatform()
	pf.listConnections = []sdk.PlatformConnection{{
		ID: 7, AppSlug: "facebook-ads", ProjectID: "test-proj", Status: "active",
	}}
	ctx := newAdsCtx(t, pf)
	app := &App{}
	insertPending := func() int64 {
		res, err := ctx.AppDB().Exec(
			`INSERT INTO pending_accounts (project_id, platform, integration_slug, status, expires_at)
			 VALUES ('test-proj','meta','facebook-ads','pending_oauth',datetime('now','+1 hour'))`,
		)
		if err != nil {
			t.Fatal(err)
		}
		id, _ := res.LastInsertId()
		return id
	}

	deniedID := insertPending()
	deniedRequest := httptest.NewRequest(http.MethodGet, "/accounts/oauth_done?status=denied&pending="+strconv.FormatInt(deniedID, 10), nil)
	deniedResponse := httptest.NewRecorder()
	app.handleOAuthDone(deniedResponse, deniedRequest)
	if deniedResponse.Code != http.StatusOK {
		t.Fatalf("denied callback without conn_id status=%d body=%s", deniedResponse.Code, deniedResponse.Body.String())
	}

	badID := insertPending()
	badRequest := httptest.NewRequest(http.MethodGet, "/accounts/oauth_done?status=ok&conn_id=99&pending="+strconv.FormatInt(badID, 10), nil)
	badResponse := httptest.NewRecorder()
	app.handleOAuthDone(badResponse, badRequest)
	if badResponse.Code != http.StatusForbidden {
		t.Fatalf("mismatched connection status=%d body=%s", badResponse.Code, badResponse.Body.String())
	}

	goodID := insertPending()
	goodRequest := httptest.NewRequest(http.MethodGet, "/accounts/oauth_done?status=ok&conn_id=7&pending="+strconv.FormatInt(goodID, 10), nil)
	goodResponse := httptest.NewRecorder()
	app.handleOAuthDone(goodResponse, goodRequest)
	if goodResponse.Code != http.StatusOK {
		t.Fatalf("matching connection status=%d body=%s", goodResponse.Code, goodResponse.Body.String())
	}
	var status string
	var connectionID int64
	_ = ctx.AppDB().QueryRow(`SELECT status, connection_id FROM pending_accounts WHERE id=?`, goodID).Scan(&status, &connectionID)
	if status != "ready" || connectionID != 7 {
		t.Fatalf("pending row status=%s connection=%d", status, connectionID)
	}
}

// --- account_finalize ----------------------------------------------

func TestAccountFinalize_WritesAdAccount(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["account_list"] = &sdk.ExecuteResult{
		Success: true, Status: 200,
		Data: json.RawMessage(`{"data":[{"id":"act_999","name":"My Ad Account","currency":"USD","timezone_name":"America/Los_Angeles"}]}`),
	}
	ctx := newAdsCtx(t, pf)
	app := &App{}

	// seed a ready pending row
	res, err := ctx.AppDB().Exec(
		`INSERT INTO pending_accounts (project_id, platform, integration_slug, connection_id, status, expires_at)
		 VALUES ('test-proj','meta','facebook-ads',7,'ready',datetime('now','+1 hour'))`,
	)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := res.LastInsertId()

	out, err := app.toolAccountFinalize(ctx, map[string]any{
		"pending_account_id": pid,
		"page_id":            "act_999",
	})
	if err != nil {
		t.Fatal(err)
	}
	rmap := out.(map[string]any)
	if rmap["display_name"] != "My Ad Account" {
		t.Fatalf("display_name = %v", rmap["display_name"])
	}
	if rmap["native_account_id"] != "act_999" {
		t.Fatalf("native_account_id = %v", rmap["native_account_id"])
	}
	var n int
	_ = ctx.AppDB().QueryRow(`SELECT COUNT(*) FROM ad_accounts WHERE native_account_id='act_999'`).Scan(&n)
	if n != 1 {
		t.Fatalf("ad_accounts row not inserted")
	}
	if len(pf.executeCalls) != 1 {
		t.Fatalf("expected one account discovery call, got %#v", pf.executeCalls)
	}
	call := pf.executeCalls[0]
	if call.Tool != "account_list" {
		t.Fatalf("account discovery tool = %q", call.Tool)
	}
	if call.Input["fields"] != "id,name,account_id,account_status,currency,timezone_name" {
		t.Fatalf("account discovery fields = %v", call.Input["fields"])
	}
	if call.Input["limit"] != 100 {
		t.Fatalf("account discovery limit = %v", call.Input["limit"])
	}
}

func TestAccountFinalize_RejectsUnknownAdAccount(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["account_list"] = &sdk.ExecuteResult{
		Success: true, Status: 200,
		Data: json.RawMessage(`{"data":[{"id":"act_111","name":"Other"}]}`),
	}
	ctx := newAdsCtx(t, pf)
	app := &App{}

	res, _ := ctx.AppDB().Exec(
		`INSERT INTO pending_accounts (project_id, platform, integration_slug, connection_id, status, expires_at)
		 VALUES ('test-proj','meta','facebook-ads',7,'ready',datetime('now','+1 hour'))`,
	)
	pid, _ := res.LastInsertId()

	out, _ := app.toolAccountFinalize(ctx, map[string]any{
		"pending_account_id": pid,
		"page_id":            "act_999",
	})
	rmap := out.(map[string]any)
	if rmap["isError"] != true {
		t.Fatalf("expected isError, got %#v", rmap)
	}
}

func TestGoogleAccountFinalize_WritesCustomerAccount(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["list_accounts"] = &sdk.ExecuteResult{
		Success: true, Status: 200,
		Data: json.RawMessage(`{"resourceNames":["customers/1234567890"]}`),
	}
	pf.executeResponses["search"] = &sdk.ExecuteResult{
		Success: true, Status: 200,
		Data: json.RawMessage(`{"results":[{"customer":{"id":"1234567890","descriptiveName":"Google Store","currencyCode":"USD","timeZone":"America/New_York"}}]}`),
	}
	ctx := newAdsCtx(t, pf)
	app := &App{}

	res, err := ctx.AppDB().Exec(
		`INSERT INTO pending_accounts (project_id, platform, integration_slug, connection_id, status, expires_at)
		 VALUES ('test-proj','google','google-ads',8,'ready',datetime('now','+1 hour'))`,
	)
	if err != nil {
		t.Fatal(err)
	}
	pid, _ := res.LastInsertId()

	out, err := app.toolAccountFinalize(ctx, map[string]any{
		"pending_account_id": pid,
		"page_id":            "1234567890",
	})
	if err != nil {
		t.Fatal(err)
	}
	rmap := out.(map[string]any)
	if rmap["display_name"] != "Google Store" {
		t.Fatalf("display_name = %v", rmap["display_name"])
	}
	if rmap["native_account_id"] != "1234567890" {
		t.Fatalf("native_account_id = %v", rmap["native_account_id"])
	}
	if rmap["platform"] != "google" {
		t.Fatalf("platform = %v", rmap["platform"])
	}
}

func TestAccountFinalize_RejectsExpiredAndCrossProjectPending(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}

	expired, _ := ctx.AppDB().Exec(
		`INSERT INTO pending_accounts (project_id, platform, integration_slug, connection_id, status, expires_at)
		 VALUES ('test-proj','meta','facebook-ads',7,'ready',datetime('now','-1 minute'))`,
	)
	expiredID, _ := expired.LastInsertId()
	out, _ := app.toolAccountFinalize(ctx, map[string]any{"pending_account_id": expiredID, "page_id": "act_1"})
	if out.(map[string]any)["isError"] != true {
		t.Fatalf("expired pending account was accepted: %#v", out)
	}

	foreign, _ := ctx.AppDB().Exec(
		`INSERT INTO pending_accounts (project_id, platform, integration_slug, connection_id, status, expires_at)
		 VALUES ('other-proj','meta','facebook-ads',7,'ready',datetime('now','+1 hour'))`,
	)
	foreignID, _ := foreign.LastInsertId()
	out, _ = app.toolAccountFinalize(ctx, map[string]any{"pending_account_id": foreignID, "page_id": "act_1"})
	if out.(map[string]any)["isError"] != true {
		t.Fatalf("cross-project pending account was accepted: %#v", out)
	}
}

func TestAccountFinalize_ReactivationUpdatesConnection(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["account_list"] = &sdk.ExecuteResult{
		Success: true, Status: 200,
		Data: json.RawMessage(`{"data":[{"id":"act_111","name":"Reconnected","currency":"EUR"}]}`),
	}
	ctx := newAdsCtx(t, pf)
	app := &App{}
	_, _ = ctx.AppDB().Exec(
		`INSERT INTO ad_accounts (project_id, platform, connection_id, native_account_id, display_name, status)
		 VALUES ('test-proj','meta',5,'act_111','Old','disconnected')`,
	)
	pending, _ := ctx.AppDB().Exec(
		`INSERT INTO pending_accounts (project_id, platform, integration_slug, connection_id, status, expires_at)
		 VALUES ('test-proj','meta','facebook-ads',8,'ready',datetime('now','+1 hour'))`,
	)
	pendingID, _ := pending.LastInsertId()
	out, err := app.toolAccountFinalize(ctx, map[string]any{"pending_account_id": pendingID, "page_id": "act_111"})
	if err != nil || out.(map[string]any)["reactivated"] != true {
		t.Fatalf("reactivation failed: out=%#v err=%v", out, err)
	}
	var connectionID int64
	_ = ctx.AppDB().QueryRow(
		`SELECT connection_id FROM ad_accounts WHERE project_id='test-proj' AND native_account_id='act_111'`,
	).Scan(&connectionID)
	if connectionID != 8 {
		t.Fatalf("connection_id = %d, want 8", connectionID)
	}
}

// --- account_disconnect --------------------------------------------

func TestAccountDisconnect_KeepsSharedIntegrationConnection(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}

	res, _ := ctx.AppDB().Exec(
		`INSERT INTO ad_accounts (project_id, platform, connection_id, native_account_id, display_name)
		 VALUES ('test-proj','meta',7,'act_1','One')`,
	)
	id, _ := res.LastInsertId()

	if _, err := app.toolAccountDisconnect(ctx, map[string]any{"id": id}); err != nil {
		t.Fatal(err)
	}
	if len(pf.disconnectCalls) != 0 {
		t.Fatalf("local account removal must not disconnect the shared integration; got %v", pf.disconnectCalls)
	}
}

func TestAccountDisconnect_KeepsConnectionWhenSiblings(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}

	r1, _ := ctx.AppDB().Exec(
		`INSERT INTO ad_accounts (project_id, platform, connection_id, native_account_id, display_name)
		 VALUES ('test-proj','meta',7,'act_1','One'), ('test-proj','meta',7,'act_2','Two')`,
	)
	first, _ := r1.LastInsertId()
	first-- // first row id is one less than LastInsertId()

	if _, err := app.toolAccountDisconnect(ctx, map[string]any{"id": first}); err != nil {
		t.Fatal(err)
	}
	if len(pf.disconnectCalls) != 0 {
		t.Fatalf("DisconnectConnection should not be called when siblings exist; got %v", pf.disconnectCalls)
	}
}

// --- campaign_create -----------------------------------------------

func TestCampaignCreate_MapsObjectiveAndForwards(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}

	res, _ := ctx.AppDB().Exec(
		`INSERT INTO ad_accounts (project_id, platform, connection_id, native_account_id, display_name)
		 VALUES ('test-proj','meta',7,'act_42','Test')`,
	)
	acctID, _ := res.LastInsertId()

	out, err := app.toolCampaignCreate(ctx, map[string]any{
		"ad_account_id":      acctID,
		"name":               "Black Friday",
		"objective":          "sales",
		"daily_budget_cents": 5000,
		"bid_strategy":       "lowest_cost",
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, isErr := out.(map[string]any)["isError"]; isErr {
		t.Fatalf("got isError: %#v", out)
	}
	if len(pf.executeCalls) != 1 {
		t.Fatalf("expected 1 ExecuteIntegrationTool call, got %d", len(pf.executeCalls))
	}
	call := pf.executeCalls[0]
	if call.Tool != "campaign_create" {
		t.Fatalf("wrong tool: %s", call.Tool)
	}
	if call.Input["objective"] != "OUTCOME_SALES" {
		t.Fatalf("objective not mapped: %v", call.Input["objective"])
	}
	if call.Input["adAccountId"] != "act_42" {
		t.Fatalf("adAccountId wrong: %v", call.Input["adAccountId"])
	}
	if call.Input["daily_budget"] != "5000" {
		t.Fatalf("daily_budget wrong: %v", call.Input["daily_budget"])
	}
	if call.Input["bid_strategy"] != "LOWEST_COST_WITHOUT_CAP" {
		t.Fatalf("bid_strategy not mapped: %v", call.Input["bid_strategy"])
	}
	// special_ad_categories defaulted to [] (Meta requirement)
	if sac, ok := call.Input["special_ad_categories"].([]any); !ok || len(sac) != 0 {
		t.Fatalf("special_ad_categories not defaulted to []: %#v", call.Input["special_ad_categories"])
	}
}

func TestCampaignCreate_PlatformOptionsOverride(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}

	res, _ := ctx.AppDB().Exec(
		`INSERT INTO ad_accounts (project_id, platform, connection_id, native_account_id, display_name)
		 VALUES ('test-proj','meta',7,'act_42','Test')`,
	)
	acctID, _ := res.LastInsertId()

	_, err := app.toolCampaignCreate(ctx, map[string]any{
		"ad_account_id": acctID,
		"name":          "X",
		"objective":     "leads",
		"platform_options": map[string]any{
			"special_ad_categories": []any{"HOUSING"},
			"spend_cap":             "100000",
			"adAccountId":           "act_attacker",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	call := pf.executeCalls[0]
	sac := call.Input["special_ad_categories"].([]any)
	if len(sac) != 1 || sac[0] != "HOUSING" {
		t.Fatalf("special_ad_categories override lost: %#v", sac)
	}
	if call.Input["spend_cap"] != "100000" {
		t.Fatalf("spend_cap passthrough lost: %v", call.Input["spend_cap"])
	}
	if call.Input["adAccountId"] != "act_42" {
		t.Fatalf("protected account id was overridden: %v", call.Input["adAccountId"])
	}
}

func TestCampaignPause_SetsStatusPaused(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["campaign_list"] = &sdk.ExecuteResult{
		Success: true, Status: 200,
		Data: json.RawMessage(`{"data":[{"id":"120000"}]}`),
	}
	ctx := newAdsCtx(t, pf)
	app := &App{}

	res, _ := ctx.AppDB().Exec(
		`INSERT INTO ad_accounts (project_id, platform, connection_id, native_account_id, display_name)
		 VALUES ('test-proj','meta',7,'act_42','Test')`,
	)
	acctID, _ := res.LastInsertId()

	if _, err := app.toolCampaignPause(ctx, map[string]any{
		"ad_account_id": acctID,
		"campaign_id":   "120000",
	}); err != nil {
		t.Fatal(err)
	}
	if len(pf.executeCalls) != 2 {
		t.Fatalf("expected ownership lookup + update, got %#v", pf.executeCalls)
	}
	call := pf.executeCalls[1]
	if call.Tool != "campaign_update" || call.Input["status"] != "PAUSED" {
		t.Fatalf("expected campaign_update with PAUSED, got tool=%s status=%v", call.Tool, call.Input["status"])
	}
}

func TestGoogleCampaignList_NormalizesGAQLRows(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["search"] = &sdk.ExecuteResult{
		Success: true, Status: 200,
		Data: json.RawMessage(`{"results":[{"campaign":{"id":"987","name":"Search Brand","status":"ENABLED","advertisingChannelType":"SEARCH","resourceName":"customers/123/campaigns/987"},"campaignBudget":{"amountMicros":"25000000","resourceName":"customers/123/campaignBudgets/55"}}]}`),
	}
	ctx := newAdsCtx(t, pf)
	app := &App{}

	res, _ := ctx.AppDB().Exec(
		`INSERT INTO ad_accounts (project_id, platform, connection_id, native_account_id, display_name)
		 VALUES ('test-proj','google',8,'123','Google')`,
	)
	acctID, _ := res.LastInsertId()

	out, err := app.toolCampaignList(ctx, map[string]any{"ad_account_id": acctID, "limit": 50})
	if err != nil {
		t.Fatal(err)
	}
	data := out.(map[string]any)["data"].([]map[string]any)
	if len(data) != 1 {
		t.Fatalf("expected 1 campaign, got %#v", data)
	}
	if data[0]["id"] != "987" || data[0]["status"] != "ACTIVE" || data[0]["daily_budget"] != "2500" {
		t.Fatalf("campaign not normalized: %#v", data[0])
	}
}

func TestGoogleCampaignCreate_CreatesBudgetThenCampaign(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["budget_mutate"] = &sdk.ExecuteResult{
		Success: true, Status: 200,
		Data: json.RawMessage(`{"results":[{"resourceName":"customers/123/campaignBudgets/55"}]}`),
	}
	pf.executeResponses["campaign_mutate"] = &sdk.ExecuteResult{
		Success: true, Status: 200,
		Data: json.RawMessage(`{"results":[{"resourceName":"customers/123/campaigns/987"}]}`),
	}
	ctx := newAdsCtx(t, pf)
	app := &App{}

	res, _ := ctx.AppDB().Exec(
		`INSERT INTO ad_accounts (project_id, platform, connection_id, native_account_id, display_name)
		 VALUES ('test-proj','google',8,'123','Google')`,
	)
	acctID, _ := res.LastInsertId()

	if _, err := app.toolCampaignCreate(ctx, map[string]any{
		"ad_account_id":      acctID,
		"name":               "Search Brand",
		"objective":          "traffic",
		"daily_budget_cents": 2500,
	}); err != nil {
		t.Fatal(err)
	}
	if len(pf.executeCalls) != 2 {
		t.Fatalf("expected budget + campaign mutate calls, got %#v", pf.executeCalls)
	}
	if pf.executeCalls[0].Tool != "budget_mutate" || pf.executeCalls[1].Tool != "campaign_mutate" {
		t.Fatalf("wrong tools: %#v", pf.executeCalls)
	}
	ops := pf.executeCalls[1].Input["operations"].([]any)
	create := ops[0].(map[string]any)["create"].(map[string]any)
	if create["campaignBudget"] != "customers/123/campaignBudgets/55" {
		t.Fatalf("campaignBudget not wired: %#v", create)
	}
	if create["status"] != "PAUSED" {
		t.Fatalf("default status not PAUSED: %#v", create)
	}
}

func TestGoogleCampaignCreate_RejectsUnsupportedGenericSemantics(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}
	res, _ := ctx.AppDB().Exec(
		`INSERT INTO ad_accounts (project_id, platform, connection_id, native_account_id, display_name)
		 VALUES ('test-proj','google',8,'123','Google')`,
	)
	acctID, _ := res.LastInsertId()

	out, _ := app.toolCampaignCreate(ctx, map[string]any{
		"ad_account_id": acctID, "name": "Sales", "objective": "sales", "daily_budget_cents": 1000,
	})
	if out.(map[string]any)["isError"] != true || len(pf.executeCalls) != 0 {
		t.Fatalf("unsupported objective was not rejected before mutation: %#v", out)
	}
	out, _ = app.toolCampaignCreate(ctx, map[string]any{
		"ad_account_id": acctID, "name": "Traffic", "objective": "traffic", "lifetime_budget_cents": 1000,
	})
	if out.(map[string]any)["isError"] != true || len(pf.executeCalls) != 0 {
		t.Fatalf("lifetime budget was not rejected before mutation: %#v", out)
	}
}

func TestGoogleCampaignCreate_RemovesBudgetWhenCampaignFails(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["budget_mutate"] = &sdk.ExecuteResult{
		Success: true, Status: 200,
		Data: json.RawMessage(`{"results":[{"resourceName":"customers/123/campaignBudgets/55"}]}`),
	}
	pf.executeResponses["campaign_mutate"] = &sdk.ExecuteResult{
		Success: false, Status: 400, Data: json.RawMessage(`{"error":"invalid campaign"}`),
	}
	ctx := newAdsCtx(t, pf)
	app := &App{}
	res, _ := ctx.AppDB().Exec(
		`INSERT INTO ad_accounts (project_id, platform, connection_id, native_account_id, display_name)
		 VALUES ('test-proj','google',8,'123','Google')`,
	)
	acctID, _ := res.LastInsertId()
	out, _ := app.toolCampaignCreate(ctx, map[string]any{
		"ad_account_id": acctID, "name": "Search", "objective": "traffic", "daily_budget_cents": 1000,
	})
	if out.(map[string]any)["isError"] != true {
		t.Fatalf("expected campaign failure, got %#v", out)
	}
	if len(pf.executeCalls) != 3 || pf.executeCalls[2].Tool != "budget_mutate" {
		t.Fatalf("expected compensating budget removal, got %#v", pf.executeCalls)
	}
	remove := pf.executeCalls[2].Input["operations"].([]any)[0].(map[string]any)["remove"]
	if remove != "customers/123/campaignBudgets/55" {
		t.Fatalf("wrong budget removed: %v", remove)
	}
}

func TestGoogleCampaignUpdate_UpdatesDailyBudget(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["search"] = &sdk.ExecuteResult{
		Success: true, Status: 200,
		Data: json.RawMessage(`{"results":[{"campaign":{"id":"987"},"campaignBudget":{"resourceName":"customers/123/campaignBudgets/55"}}]}`),
	}
	pf.executeResponses["budget_mutate"] = &sdk.ExecuteResult{
		Success: true, Status: 200, Data: json.RawMessage(`{"results":[{"resourceName":"customers/123/campaignBudgets/55"}]}`),
	}
	ctx := newAdsCtx(t, pf)
	app := &App{}
	res, _ := ctx.AppDB().Exec(
		`INSERT INTO ad_accounts (project_id, platform, connection_id, native_account_id, display_name)
		 VALUES ('test-proj','google',8,'123','Google')`,
	)
	acctID, _ := res.LastInsertId()
	out, err := app.toolCampaignUpdate(ctx, map[string]any{
		"ad_account_id": acctID, "campaign_id": "987", "daily_budget_cents": 4200,
	})
	if err != nil || out.(map[string]any)["isError"] == true {
		t.Fatalf("budget update failed: out=%#v err=%v", out, err)
	}
	if len(pf.executeCalls) != 2 || pf.executeCalls[1].Tool != "budget_mutate" {
		t.Fatalf("expected search + budget update, got %#v", pf.executeCalls)
	}
	op := pf.executeCalls[1].Input["operations"].([]any)[0].(map[string]any)
	update := op["update"].(map[string]any)
	if update["amountMicros"] != "42000000" || op["updateMask"] != "amount_micros" {
		t.Fatalf("wrong budget mutation: %#v", op)
	}
}

// --- adset_create --------------------------------------------------

func TestAdSetCreate_RequiresTargeting(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}

	res, _ := ctx.AppDB().Exec(
		`INSERT INTO ad_accounts (project_id, platform, connection_id, native_account_id, display_name)
		 VALUES ('test-proj','meta',7,'act_42','Test')`,
	)
	acctID, _ := res.LastInsertId()

	out, _ := app.toolAdSetCreate(ctx, map[string]any{
		"ad_account_id":     acctID,
		"campaign_id":       "120000",
		"name":              "AS1",
		"optimization_goal": "link_clicks",
		// targeting missing
	})
	rmap := out.(map[string]any)
	if rmap["isError"] != true {
		t.Fatalf("expected isError, got %#v", rmap)
	}
}

func TestAdSetCreate_MapsOptimizationGoal(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}

	res, _ := ctx.AppDB().Exec(
		`INSERT INTO ad_accounts (project_id, platform, connection_id, native_account_id, display_name)
		 VALUES ('test-proj','meta',7,'act_42','Test')`,
	)
	acctID, _ := res.LastInsertId()

	if _, err := app.toolAdSetCreate(ctx, map[string]any{
		"ad_account_id":     acctID,
		"campaign_id":       "120000",
		"name":              "AS1",
		"optimization_goal": "conversions",
		"targeting":         map[string]any{"geo_locations": map[string]any{"countries": []any{"US"}}},
	}); err != nil {
		t.Fatal(err)
	}
	call := pf.executeCalls[0]
	if call.Tool != "adset_create" || call.Input["optimization_goal"] != "OFFSITE_CONVERSIONS" {
		t.Fatalf("optimization_goal not mapped: tool=%s og=%v", call.Tool, call.Input["optimization_goal"])
	}
	if call.Input["billing_event"] != "IMPRESSIONS" {
		t.Fatalf("billing_event default lost: %v", call.Input["billing_event"])
	}
}

// --- creative_upload -----------------------------------------------

func TestCreativeUpload_FetchesFromStorage(t *testing.T) {
	pf := newRecordingPlatform()
	// Storage app's files_get returns an MCP envelope wrapping our shape.
	pf.callAppResponses["storage:files_get"] = json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{\"id\":42,\"url\":\"https://cdn.example.com/abc.jpg\",\"filename\":\"abc.jpg\",\"mime_type\":\"image/jpeg\"}"}]}}`)
	ctx := newAdsCtx(t, pf)
	app := &App{}

	res, _ := ctx.AppDB().Exec(
		`INSERT INTO ad_accounts (project_id, platform, connection_id, native_account_id, display_name)
		 VALUES ('test-proj','meta',7,'act_42','Test')`,
	)
	acctID, _ := res.LastInsertId()

	if _, err := app.toolCreativeUpload(ctx, map[string]any{
		"ad_account_id": acctID,
		"kind":          "image",
		"storage_id":    42,
	}); err != nil {
		t.Fatal(err)
	}
	if len(pf.callAppCalls) != 1 || pf.callAppCalls[0].AppName != "storage" || pf.callAppCalls[0].Tool != "files_get" {
		t.Fatalf("expected storage:files_get call, got %#v", pf.callAppCalls)
	}
	if len(pf.executeCalls) != 1 || pf.executeCalls[0].Tool != "creative_upload_image" {
		t.Fatalf("expected creative_upload_image execute call, got %#v", pf.executeCalls)
	}
	if pf.executeCalls[0].Input["url"] != "https://cdn.example.com/abc.jpg" {
		t.Fatalf("storage url not forwarded: %v", pf.executeCalls[0].Input["url"])
	}
}

func TestCreativeUpload_RejectsWithoutSource(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}

	res, _ := ctx.AppDB().Exec(
		`INSERT INTO ad_accounts (project_id, platform, connection_id, native_account_id, display_name)
		 VALUES ('test-proj','meta',7,'act_42','Test')`,
	)
	acctID, _ := res.LastInsertId()

	out, _ := app.toolCreativeUpload(ctx, map[string]any{
		"ad_account_id": acctID,
		"kind":          "image",
	})
	rmap := out.(map[string]any)
	if rmap["isError"] != true {
		t.Fatalf("expected isError")
	}
	content := rmap["content"].([]map[string]any)[0]["text"].(string)
	if !strings.Contains(content, "storage_id or source_url") {
		t.Fatalf("unexpected error text: %s", content)
	}
}

// --- audience_create_lookalike -------------------------------------

func TestAudienceCreateLookalike_ForwardsArgs(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}

	res, _ := ctx.AppDB().Exec(
		`INSERT INTO ad_accounts (project_id, platform, connection_id, native_account_id, display_name)
		 VALUES ('test-proj','meta',7,'act_42','Test')`,
	)
	acctID, _ := res.LastInsertId()

	if _, err := app.toolAudienceCreateLookalike(ctx, map[string]any{
		"ad_account_id":      acctID,
		"name":               "LAL US 1%",
		"source_audience_id": "120000",
		"country":            "US",
		"ratio":              0.01,
	}); err != nil {
		t.Fatal(err)
	}
	call := pf.executeCalls[0]
	if call.Tool != "audience_create_lookalike" {
		t.Fatalf("wrong tool: %s", call.Tool)
	}
	if call.Input["origin_audience_id"] != "120000" || call.Input["country"] != "US" {
		t.Fatalf("args not forwarded: %#v", call.Input)
	}
	if call.Input["ratio"] != 0.01 {
		t.Fatalf("ratio not forwarded: %v", call.Input["ratio"])
	}
}
