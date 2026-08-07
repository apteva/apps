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
	executeResponder   func(connID int64, tool string, input map[string]any) (*sdk.ExecuteResult, error)
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
	if p.executeResponder != nil {
		return p.executeResponder(connID, tool, input)
	}
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

func TestAccountAdd_ReusesRequestedConnection(t *testing.T) {
	pf := newRecordingPlatform()
	pf.listConnections = []sdk.PlatformConnection{
		{ID: 8, AppSlug: "google-ads", Name: "Agency manager", ProjectID: "test-proj", Status: "active"},
		{ID: 9, AppSlug: "google-ads", Name: "Direct advertiser", ProjectID: "test-proj", Status: "active"},
	}
	ctx := newAdsCtx(t, pf)
	app := &App{}

	out, err := app.toolAccountAdd(ctx, map[string]any{
		"platform":      "google",
		"connection_id": 9,
	})
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	if result["reused_connection"] != int64(9) {
		t.Fatalf("reused connection = %#v", result["reused_connection"])
	}
	if len(pf.startOAuthCalls) != 0 {
		t.Fatalf("reuse unexpectedly started OAuth: %#v", pf.startOAuthCalls)
	}
}

func TestAccountAdd_RejectsForeignOrInactiveConnection(t *testing.T) {
	pf := newRecordingPlatform()
	pf.listConnections = []sdk.PlatformConnection{
		{ID: 8, AppSlug: "google-ads", ProjectID: "test-proj", Status: "failed"},
		{ID: 9, AppSlug: "google-ads", ProjectID: "test-proj", Status: "active"},
	}
	ctx := newAdsCtx(t, pf)
	app := &App{}

	out, err := app.toolAccountAdd(ctx, map[string]any{
		"platform":      "google",
		"connection_id": 8,
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["isError"] != true {
		t.Fatalf("inactive connection was accepted: %#v", out)
	}
}

func TestAccountAdd_RejectsExternalReturnURL(t *testing.T) {
	pf := newRecordingPlatform()
	pf.listConnections = []sdk.PlatformConnection{{
		ID: 6, AppSlug: "google-ads", ProjectID: "test-proj", Status: "pending",
	}}
	ctx := newAdsCtx(t, pf)
	app := &App{}

	out, err := app.toolAccountAdd(ctx, map[string]any{
		"platform":  "google",
		"force_new": true,
		"return_to": "https://attacker.example/callback",
	})
	if err != nil {
		t.Fatal(err)
	}
	if out.(map[string]any)["isError"] != true {
		t.Fatalf("external return URL was accepted: %#v", out)
	}
	if len(pf.startOAuthCalls) != 0 {
		t.Fatalf("external return URL started OAuth: %#v", pf.startOAuthCalls)
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

func TestGoogleAccountPicker_ExpandsManagerHierarchy(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponder = func(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
		switch {
		case tool == "list_accounts":
			return &sdk.ExecuteResult{
				Success: true,
				Status:  200,
				Data:    json.RawMessage(`{"resourceNames":["customers/1111111111"]}`),
			}, nil
		case tool == "search" && strings.Contains(input["query"].(string), "FROM customer LIMIT 1"):
			return &sdk.ExecuteResult{
				Success: true,
				Status:  200,
				Data: json.RawMessage(`{"results":[{"customer":{
					"id":"1111111111",
					"descriptiveName":"Agency Manager",
					"manager":true,
					"status":"ENABLED"
				}}]}`),
			}, nil
		case tool == "search" && strings.Contains(input["query"].(string), "FROM customer_client"):
			return &sdk.ExecuteResult{
				Success: true,
				Status:  200,
				Data: json.RawMessage(`{"results":[
					{"customerClient":{"id":"1111111111","descriptiveName":"Agency Manager","manager":true,"status":"ENABLED","level":"0"}},
					{"customerClient":{"id":"2222222222","descriptiveName":"Client One","currencyCode":"EUR","timeZone":"Europe/Madrid","manager":false,"status":"ENABLED","level":"1"}},
					{"customerClient":{"id":"3333333333","descriptiveName":"Nested Manager","manager":true,"status":"ENABLED","level":"1"}},
					{"customerClient":{"id":"4444444444","descriptiveName":"Client Two","currencyCode":"USD","timeZone":"America/New_York","manager":false,"status":"ENABLED","level":"2"}},
					{"customerClient":{"id":"5555555555","descriptiveName":"Closed Client","manager":false,"status":"CANCELED","level":"1"}}
				]}`),
			}, nil
		default:
			t.Fatalf("unexpected integration call: tool=%s input=%#v", tool, input)
			return nil, nil
		}
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
	pendingID, _ := res.LastInsertId()

	out, err := app.toolAccountListPendingPages(ctx, map[string]any{"pending_account_id": pendingID})
	if err != nil {
		t.Fatal(err)
	}
	pages := out.(map[string]any)["pages"].([]map[string]any)
	if len(pages) != 2 {
		t.Fatalf("expected two advertiser accounts, got %#v", pages)
	}
	if pages[0]["id"] != "2222222222" || pages[0]["name"] != "Client One" {
		t.Fatalf("first client not normalized: %#v", pages[0])
	}
	if pages[1]["id"] != "4444444444" || pages[1]["currency"] != "USD" {
		t.Fatalf("nested client not normalized: %#v", pages[1])
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
	if call.Input["is_adset_budget_sharing_enabled"] != false {
		t.Fatalf("non-CBO campaign must disable ad set budget sharing by default: %#v", call.Input)
	}
}

func TestCampaignList_RequestsDisplayFields(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}

	res, _ := ctx.AppDB().Exec(
		`INSERT INTO ad_accounts (project_id, platform, connection_id, native_account_id, display_name)
		 VALUES ('test-proj','meta',7,'act_42','Test')`,
	)
	acctID, _ := res.LastInsertId()

	if _, err := app.toolCampaignList(ctx, map[string]any{"ad_account_id": acctID, "limit": 50}); err != nil {
		t.Fatal(err)
	}
	call := pf.executeCalls[0]
	if call.Tool != "campaign_list" || call.Input["adAccountId"] != "act_42" {
		t.Fatalf("wrong campaign list call: %#v", call)
	}
	fields, _ := call.Input["fields"].(string)
	for _, field := range []string{"name", "objective", "status", "effective_status", "daily_budget"} {
		if !strings.Contains(fields, field) {
			t.Fatalf("campaign fields missing %q: %s", field, fields)
		}
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
		"ad_account_id":      acctID,
		"campaign_id":        "120000",
		"name":               "AS1",
		"optimization_goal":  "conversions",
		"daily_budget_cents": 100,
		"targeting":          map[string]any{"geo_locations": map[string]any{"countries": []any{"US"}}},
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
	if call.Input["bid_strategy"] != "LOWEST_COST_WITHOUT_CAP" {
		t.Fatalf("budgeted ad set must default to lowest cost: %#v", call.Input)
	}
}

func TestAdSetList_UsesMetaObjectEdge(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}

	res, _ := ctx.AppDB().Exec(
		`INSERT INTO ad_accounts (project_id, platform, connection_id, native_account_id, display_name)
		 VALUES ('test-proj','meta',7,'act_42','Test')`,
	)
	acctID, _ := res.LastInsertId()

	if _, err := app.toolAdSetList(ctx, map[string]any{
		"ad_account_id": acctID,
		"campaign_id":   "120000",
		"limit":         50,
	}); err != nil {
		t.Fatal(err)
	}
	call := pf.executeCalls[0]
	if call.Tool != "adset_list" || call.Input["objectId"] != "120000" {
		t.Fatalf("wrong ad set list edge: %#v", call)
	}
	if _, leaked := call.Input["adAccountId"]; leaked {
		t.Fatalf("adset_list must not send adAccountId: %#v", call.Input)
	}
	if !strings.Contains(call.Input["fields"].(string), "optimization_goal") {
		t.Fatalf("ad set fields missing: %v", call.Input["fields"])
	}
}

func TestAdSetUpdateAndDelete_UseCaseSensitiveMetaID(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["adset_list"] = &sdk.ExecuteResult{
		Success: true,
		Status:  200,
		Data:    json.RawMessage(`{"data":[{"id":"adset_1"}]}`),
	}
	ctx := newAdsCtx(t, pf)
	app := &App{}

	res, _ := ctx.AppDB().Exec(
		`INSERT INTO ad_accounts (project_id, platform, connection_id, native_account_id, display_name)
		 VALUES ('test-proj','meta',7,'act_42','Test')`,
	)
	acctID, _ := res.LastInsertId()

	if _, err := app.toolAdSetUpdate(ctx, map[string]any{
		"ad_account_id": acctID,
		"adset_id":      "adset_1",
		"name":          "Updated",
	}); err != nil {
		t.Fatal(err)
	}
	if len(pf.executeCalls) != 2 {
		t.Fatalf("expected ownership lookup + update, got %#v", pf.executeCalls)
	}
	if pf.executeCalls[0].Input["objectId"] != "act_42" {
		t.Fatalf("ownership lookup must use objectId: %#v", pf.executeCalls[0])
	}
	update := pf.executeCalls[1]
	if update.Tool != "adset_update" || update.Input["adSetId"] != "adset_1" {
		t.Fatalf("ad set update ID is wrong: %#v", update)
	}
	if _, wrongCase := update.Input["adsetId"]; wrongCase {
		t.Fatalf("ad set update leaked wrong-case adsetId: %#v", update.Input)
	}

	if _, err := app.toolAdSetDelete(ctx, map[string]any{
		"ad_account_id": acctID,
		"adset_id":      "adset_1",
	}); err != nil {
		t.Fatal(err)
	}
	remove := pf.executeCalls[3]
	if remove.Tool != "adset_delete" || remove.Input["adSetId"] != "adset_1" {
		t.Fatalf("ad set delete ID is wrong: %#v", remove)
	}
}

func TestAdListAndCreate_UseMetaCreativeShape(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}

	res, _ := ctx.AppDB().Exec(
		`INSERT INTO ad_accounts (project_id, platform, connection_id, native_account_id, display_name)
		 VALUES ('test-proj','meta',7,'act_42','Test')`,
	)
	acctID, _ := res.LastInsertId()

	if _, err := app.toolAdList(ctx, map[string]any{
		"ad_account_id": acctID,
		"adset_id":      "adset_1",
	}); err != nil {
		t.Fatal(err)
	}
	listCall := pf.executeCalls[0]
	if listCall.Tool != "ad_list" || listCall.Input["objectId"] != "adset_1" {
		t.Fatalf("wrong ad list edge: %#v", listCall)
	}
	if !strings.Contains(listCall.Input["fields"].(string), "effective_status") {
		t.Fatalf("ad fields missing: %v", listCall.Input["fields"])
	}

	if _, err := app.toolAdCreate(ctx, map[string]any{
		"ad_account_id": acctID,
		"adset_id":      "adset_1",
		"name":          "Test ad",
		"creative_id":   "creative_1",
	}); err != nil {
		t.Fatal(err)
	}
	createCall := pf.executeCalls[1]
	creative, ok := createCall.Input["creative"].(map[string]any)
	if createCall.Tool != "ad_create" || !ok || creative["creative_id"] != "creative_1" {
		t.Fatalf("wrong ad creative payload: %#v", createCall)
	}
	if _, leaked := createCall.Input["creative_id"]; leaked {
		t.Fatalf("ad_create must not send top-level creative_id: %#v", createCall.Input)
	}
}

// --- creatives -----------------------------------------------------

func TestMetaCreativeCreateVideo_MapsGenericSpec(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}

	res, _ := ctx.AppDB().Exec(
		`INSERT INTO ad_accounts (project_id, platform, connection_id, native_account_id, display_name)
		 VALUES ('test-proj','meta',7,'act_42','Test')`,
	)
	acctID, _ := res.LastInsertId()

	if _, err := app.toolCreativeCreate(ctx, map[string]any{
		"ad_account_id":         acctID,
		"format":                "video",
		"name":                  "Launch video",
		"identity_id":           "page_7",
		"secondary_identity_id": "instagram_8",
		"headline":              "See it work",
		"primary_text":          "A complete walkthrough.",
		"description":           "Two minutes.",
		"destination_url":       "https://example.com/demo",
		"call_to_action":        "watch_more",
		"video_id":              "video_9",
		"thumbnail_hash":        "thumb_hash_10",
		"url_tags":              "utm_source=meta",
	}); err != nil {
		t.Fatal(err)
	}

	call := pf.executeCalls[0]
	if call.Tool != "creative_create" || call.Input["adAccountId"] != "act_42" {
		t.Fatalf("wrong creative create call: %#v", call)
	}
	story, ok := call.Input["object_story_spec"].(map[string]any)
	if !ok || story["page_id"] != "page_7" || story["instagram_user_id"] != "instagram_8" {
		t.Fatalf("missing Meta page identity: %#v", call.Input)
	}
	video, ok := story["video_data"].(map[string]any)
	if !ok || video["video_id"] != "video_9" || video["title"] != "See it work" ||
		video["message"] != "A complete walkthrough." || video["link_description"] != "Two minutes." ||
		video["image_hash"] != "thumb_hash_10" {
		t.Fatalf("video story is wrong: %#v", story)
	}
	cta, ok := video["call_to_action"].(map[string]any)
	if !ok || cta["type"] != "WATCH_MORE" {
		t.Fatalf("video CTA is wrong: %#v", video)
	}
	value, ok := cta["value"].(map[string]any)
	if !ok || value["link"] != "https://example.com/demo" {
		t.Fatalf("video CTA destination is wrong: %#v", cta)
	}
	if call.Input["url_tags"] != "utm_source=meta" {
		t.Fatalf("url tags missing: %#v", call.Input)
	}
}

func TestMetaCreativeCreateCarousel_MapsCards(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}

	res, _ := ctx.AppDB().Exec(
		`INSERT INTO ad_accounts (project_id, platform, connection_id, native_account_id, display_name)
		 VALUES ('test-proj','meta',7,'act_42','Test')`,
	)
	acctID, _ := res.LastInsertId()

	if _, err := app.toolCreativeCreate(ctx, map[string]any{
		"ad_account_id": acctID,
		"format":        "carousel",
		"name":          "Product carousel",
		"identity_id":   "page_7",
		"primary_text":  "Choose a product.",
		"cards": []any{
			map[string]any{"headline": "One", "destination_url": "https://example.com/one", "image_url": "https://cdn.example.com/one.jpg"},
			map[string]any{"headline": "Two", "destination_url": "https://example.com/two", "image_hash": "hash_2", "call_to_action": "shop_now"},
		},
	}); err != nil {
		t.Fatal(err)
	}

	story := pf.executeCalls[0].Input["object_story_spec"].(map[string]any)
	linkData := story["link_data"].(map[string]any)
	children := linkData["child_attachments"].([]any)
	if len(children) != 2 {
		t.Fatalf("wrong carousel card count: %#v", linkData)
	}
	second := children[1].(map[string]any)
	if second["image_hash"] != "hash_2" || second["name"] != "Two" {
		t.Fatalf("second carousel card is wrong: %#v", second)
	}
}

func TestMetaCreativeGetAndDelete_VerifyAccountOwnership(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["creative_list"] = &sdk.ExecuteResult{
		Success: true,
		Status:  200,
		Data:    json.RawMessage(`{"data":[{"id":"creative_1"}]}`),
	}
	ctx := newAdsCtx(t, pf)
	app := &App{}

	res, _ := ctx.AppDB().Exec(
		`INSERT INTO ad_accounts (project_id, platform, connection_id, native_account_id, display_name)
		 VALUES ('test-proj','meta',7,'act_42','Test')`,
	)
	acctID, _ := res.LastInsertId()

	if _, err := app.toolCreativeGet(ctx, map[string]any{"ad_account_id": acctID, "creative_id": "creative_1"}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolCreativeDelete(ctx, map[string]any{"ad_account_id": acctID, "creative_id": "creative_1"}); err != nil {
		t.Fatal(err)
	}
	if len(pf.executeCalls) != 4 ||
		pf.executeCalls[0].Tool != "creative_list" ||
		pf.executeCalls[1].Tool != "creative_get" ||
		pf.executeCalls[2].Tool != "creative_list" ||
		pf.executeCalls[3].Tool != "creative_delete" {
		t.Fatalf("creative ownership sequence is wrong: %#v", pf.executeCalls)
	}
	getFields, _ := pf.executeCalls[1].Input["fields"].(string)
	if strings.Contains(getFields, "created_time") || !strings.Contains(getFields, "object_story_spec") {
		t.Fatalf("creative get fields are not Graph node compatible: %q", getFields)
	}
	if pf.executeCalls[3].Input["creativeId"] != "creative_1" {
		t.Fatalf("creative delete ID is wrong: %#v", pf.executeCalls[3])
	}
}

func TestGoogleCreativeCreateVideo_MapsYouTubeAsset(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}

	res, _ := ctx.AppDB().Exec(
		`INSERT INTO ad_accounts (project_id, platform, connection_id, native_account_id, display_name)
		 VALUES ('test-proj','google',7,'1234567890','Test')`,
	)
	acctID, _ := res.LastInsertId()

	if _, err := app.toolCreativeCreate(ctx, map[string]any{
		"ad_account_id": acctID,
		"format":        "video",
		"name":          "Launch video",
		"video_id":      "abc123xyz89",
	}); err != nil {
		t.Fatal(err)
	}
	call := pf.executeCalls[0]
	if call.Tool != "asset_mutate" || call.Input["customer_id"] != "1234567890" {
		t.Fatalf("wrong Google asset call: %#v", call)
	}
	ops := call.Input["operations"].([]any)
	create := ops[0].(map[string]any)["create"].(map[string]any)
	video := create["youtubeVideoAsset"].(map[string]any)
	if create["name"] != "Launch video" || video["youtubeVideoId"] != "abc123xyz89" {
		t.Fatalf("wrong YouTube asset payload: %#v", create)
	}
}

func TestCreativeUpload_FetchesFromStorage(t *testing.T) {
	pf := newRecordingPlatform()
	// Storage app's files_get_url returns an MCP envelope wrapping our shape.
	pf.callAppResponses["storage:files_get_url"] = json.RawMessage(`{"jsonrpc":"2.0","id":1,"result":{"content":[{"type":"text","text":"{\"id\":42,\"url\":\"https://cdn.example.com/abc.jpg\",\"filename\":\"abc.jpg\",\"mime_type\":\"image/jpeg\"}"}]}}`)
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
	if len(pf.callAppCalls) != 1 || pf.callAppCalls[0].AppName != "storage" || pf.callAppCalls[0].Tool != "files_get_url" {
		t.Fatalf("expected storage:files_get_url call, got %#v", pf.callAppCalls)
	}
	if pf.callAppCalls[0].Input["ttl_seconds"] != 3600 {
		t.Fatalf("storage URL must be long enough for provider ingestion: %#v", pf.callAppCalls[0].Input)
	}
	if len(pf.executeCalls) != 1 || pf.executeCalls[0].Tool != "creative_upload_image" {
		t.Fatalf("expected creative_upload_image execute call, got %#v", pf.executeCalls)
	}
	if pf.executeCalls[0].Input["url"] != "https://cdn.example.com/abc.jpg" {
		t.Fatalf("storage url not forwarded: %v", pf.executeCalls[0].Input["url"])
	}
}

func TestCreativeUpload_MetaImageCapabilityFallsBackToDirectURL(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["creative_upload_image"] = &sdk.ExecuteResult{
		Success: false, Status: 403,
		Data: json.RawMessage(`{"error":{"code":3,"message":"(#3) Application does not have the capability to make this API call."}}`),
	}
	ctx := newAdsCtx(t, pf)
	app := &App{}
	res, _ := ctx.AppDB().Exec(`INSERT INTO ad_accounts (project_id, platform, connection_id, native_account_id, display_name) VALUES ('test-proj','meta',7,'act_42','Test')`)
	acctID, _ := res.LastInsertId()

	out, err := app.toolCreativeUpload(ctx, map[string]any{"ad_account_id": acctID, "kind": "image", "source_url": "https://cdn.example.com/ad.jpg"})
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	if result["upload_mode"] != "direct_url" || result["source_url"] != "https://cdn.example.com/ad.jpg" || result["provider_upload"] != false {
		t.Fatalf("unexpected direct URL fallback: %#v", result)
	}
}

func TestCreativeUpload_MetaOtherErrorsRemainErrors(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["creative_upload_image"] = &sdk.ExecuteResult{Success: false, Status: 400, Data: json.RawMessage(`{"error":{"code":100,"message":"invalid URL"}}`)}
	ctx := newAdsCtx(t, pf)
	app := &App{}
	res, _ := ctx.AppDB().Exec(`INSERT INTO ad_accounts (project_id, platform, connection_id, native_account_id, display_name) VALUES ('test-proj','meta',7,'act_42','Test')`)
	acctID, _ := res.LastInsertId()

	out, _ := app.toolCreativeUpload(ctx, map[string]any{"ad_account_id": acctID, "kind": "image", "source_url": "https://cdn.example.com/ad.jpg"})
	if out.(map[string]any)["isError"] != true {
		t.Fatalf("non-capability provider failure was hidden: %#v", out)
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

func TestCreativeUpload_VideoMapsNameToRequiredTitle(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["creative_upload_video"] = &sdk.ExecuteResult{
		Success: true,
		Status:  200,
		Data:    json.RawMessage(`{"id":"video_123"}`),
	}
	ctx := newAdsCtx(t, pf)
	app := &App{}

	res, _ := ctx.AppDB().Exec(
		`INSERT INTO ad_accounts (project_id, platform, connection_id, native_account_id, display_name)
		 VALUES ('test-proj','meta',7,'act_42','Test')`,
	)
	acctID, _ := res.LastInsertId()

	if _, err := app.toolCreativeUpload(ctx, map[string]any{
		"ad_account_id": acctID,
		"kind":          "video",
		"source_url":    "https://cdn.example.com/clip.mp4",
		"name":          "Launch clip",
	}); err != nil {
		t.Fatal(err)
	}
	call := pf.executeCalls[0]
	if call.Tool != "creative_upload_video" ||
		call.Input["file_url"] != "https://cdn.example.com/clip.mp4" ||
		call.Input["title"] != "Launch clip" {
		t.Fatalf("video upload payload is wrong: %#v", call)
	}
	if _, leaked := call.Input["name"]; leaked {
		t.Fatalf("video upload must use title, not name: %#v", call.Input)
	}
	var tracked int
	if err := ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM ad_resources WHERE ad_account_id=? AND native_asset_id='video_123' AND kind='creative_asset' AND provider_type='video'`,
		acctID,
	).Scan(&tracked); err != nil || tracked != 1 {
		t.Fatalf("uploaded video was not tracked: count=%d err=%v", tracked, err)
	}

	if _, err := app.toolCreativeAssetStatus(ctx, map[string]any{
		"ad_account_id": acctID,
		"asset_id":      "video_123",
		"kind":          "video",
	}); err != nil {
		t.Fatal(err)
	}
	if pf.executeCalls[1].Tool != "video_status" || pf.executeCalls[1].Input["videoId"] != "video_123" {
		t.Fatalf("wrong video status call: %#v", pf.executeCalls[1])
	}

	if _, err := app.toolCreativeAssetDelete(ctx, map[string]any{
		"ad_account_id": acctID,
		"asset_id":      "video_123",
		"kind":          "video",
	}); err != nil {
		t.Fatal(err)
	}
	if pf.executeCalls[2].Tool != "video_delete" ||
		pf.executeCalls[2].Input["adAccountId"] != "act_42" ||
		pf.executeCalls[2].Input["video_id"] != "video_123" {
		t.Fatalf("wrong video delete call: %#v", pf.executeCalls[2])
	}
	if err := ctx.AppDB().QueryRow(
		`SELECT COUNT(*) FROM ad_resources WHERE ad_account_id=? AND native_asset_id='video_123'`,
		acctID,
	).Scan(&tracked); err != nil || tracked != 0 {
		t.Fatalf("deleted video tracking remains: count=%d err=%v", tracked, err)
	}
}

func TestMetaLibraryLists_RequestReadableFields(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}

	res, _ := ctx.AppDB().Exec(
		`INSERT INTO ad_accounts (project_id, platform, connection_id, native_account_id, display_name)
		 VALUES ('test-proj','meta',7,'act_42','Test')`,
	)
	acctID, _ := res.LastInsertId()

	if _, err := app.toolCreativeList(ctx, map[string]any{"ad_account_id": acctID}); err != nil {
		t.Fatal(err)
	}
	if _, err := app.toolAudienceList(ctx, map[string]any{"ad_account_id": acctID}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(pf.executeCalls[0].Input["fields"].(string), "thumbnail_url") {
		t.Fatalf("creative fields missing: %#v", pf.executeCalls[0].Input)
	}
	audienceFields := pf.executeCalls[1].Input["fields"].(string)
	if strings.Contains(audienceFields, "approximate_count,") ||
		!strings.Contains(audienceFields, "approximate_count_lower_bound") ||
		!strings.Contains(audienceFields, "approximate_count_upper_bound") {
		t.Fatalf("audience fields are not Graph v25 compatible: %s", audienceFields)
	}
}

// --- audience_create_lookalike -------------------------------------

func TestAudienceCreateCustom_DefaultsRequiredMetaFields(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newAdsCtx(t, pf)
	app := &App{}

	res, _ := ctx.AppDB().Exec(
		`INSERT INTO ad_accounts (project_id, platform, connection_id, native_account_id, display_name)
		 VALUES ('test-proj','meta',7,'act_42','Test')`,
	)
	acctID, _ := res.LastInsertId()

	if _, err := app.toolAudienceCreateCustom(ctx, map[string]any{
		"ad_account_id": acctID,
		"name":          "Customer list",
	}); err != nil {
		t.Fatal(err)
	}
	call := pf.executeCalls[0]
	if call.Input["subtype"] != "CUSTOM" || call.Input["customer_file_source"] != "USER_PROVIDED_ONLY" {
		t.Fatalf("custom audience defaults missing: %#v", call.Input)
	}
}

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
	if call.Input["origin_audience_id"] != "120000" || call.Input["subtype"] != "LOOKALIKE" {
		t.Fatalf("args not forwarded: %#v", call.Input)
	}
	spec, ok := call.Input["lookalike_spec"].(map[string]any)
	if !ok || spec["country"] != "US" || spec["ratio"] != 0.01 || spec["type"] != "similarity" {
		t.Fatalf("lookalike spec not mapped: %#v", call.Input)
	}
}
