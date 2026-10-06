package main

import (
	"encoding/json"
	"errors"
	sdk "github.com/apteva/app-sdk"
	"net/http/httptest"
	"testing"
	"time"
)

type authorizationPlatform struct {
	financeBindingsPlatform
	state             string
	starts, exchanges int
	failExchange      bool
}

func (p *authorizationPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{InstallID: 17, Bindings: p.bindings}, nil
}
func (p *authorizationPlatform) ExecuteIntegrationTool(id int64, tool string, args map[string]any) (*sdk.ExecuteResult, error) {
	var out any
	switch tool {
	case "get_application":
		out = map[string]any{"redirect_urls": []string{"http://localhost:5280/api/apps/finance/banking/enable/callback?project_id=test-proj&install_id=17"}}
	case "list_banks":
		out = map[string]any{"aspsps": []any{map[string]any{"name": "Test Bank", "country": "ES", "maximum_consent_validity": 3600}}}
	case "start_authorization":
		p.starts++
		p.state = args["state"].(string)
		out = map[string]any{"url": "https://ob.enablebanking.com/auth/test"}
	case "authorize_session":
		p.exchanges++
		if p.failExchange {
			return nil, errors.New("timeout")
		}
		out = map[string]any{"session_id": "authorized-session"}
	default:
		return nil, errors.New("unexpected tool " + tool)
	}
	b, _ := json.Marshal(out)
	return &sdk.ExecuteResult{Success: true, Status: 200, Data: b}, nil
}
func authorizationFixture(t *testing.T) (*App, *sdk.AppCtx, *authorizationPlatform, map[string]any) {
	p := &authorizationPlatform{financeBindingsPlatform: financeBindingsPlatform{bindings: map[string]any{financeConnectionRole: float64(44)}, conns: map[int64]sdk.PlatformConnection{44: {ID: 44, AppSlug: "enable-banking", Status: "active", ProjectID: "test-proj"}}}}
	ctx := newCtxWithPlatform(t, p)
	args := map[string]any{"connection_id": 44, "country": "ES", "bank_name": "Test Bank", "redirect_url": "http://localhost:5280/api/apps/finance/banking/enable/callback?project_id=test-proj&install_id=17"}
	return &App{}, ctx, p, args
}
func TestBankAuthorizationStartCallbackAndSavedSession(t *testing.T) {
	app, ctx, p, args := authorizationFixture(t)
	out, err := app.startBankAuthorization(ctx, args)
	if err != nil {
		t.Fatal(err)
	}
	if out == nil || p.state == "" {
		t.Fatal(out)
	}
	if err = app.completeBankAuthorization(ctx, "wrong-state", "code", ""); err == nil {
		t.Fatal("accepted unknown state")
	}
	if err = app.completeBankAuthorization(ctx, p.state, "code", ""); err != nil {
		t.Fatal(err)
	}
	if err = app.completeBankAuthorization(ctx, p.state, "code", ""); err != nil {
		t.Fatal(err)
	}
	if p.exchanges != 1 {
		t.Fatal("replayed code", p.exchanges)
	}
	w := httptest.NewRecorder()
	app.handleBankAuthorization(w, httptest.NewRequest("GET", "/banking/enable/authorizations?connection_id=44", nil))
	if w.Code != 200 {
		t.Fatal(w.Body.String())
	}
	var response struct {
		Authorizations []struct {
			Session string `json:"session_id"`
		}
	}
	if err = json.Unmarshal(w.Body.Bytes(), &response); err != nil || len(response.Authorizations) != 1 || response.Authorizations[0].Session != "authorized-session" {
		t.Fatal(w.Body.String(), err)
	}
}
func TestBankAuthorizationRejectsInvalidRoutingBankAndExpiredState(t *testing.T) {
	app, ctx, p, args := authorizationFixture(t)
	for _, raw := range []string{"https://example.com/other?project_id=test-proj&install_id=17", "http://example.com/api/apps/finance/banking/enable/callback?project_id=test-proj&install_id=17", "https://example.com/api/apps/finance/banking/enable/callback?project_id=other&install_id=17", "https://example.com/api/apps/finance/banking/enable/callback?project_id=test-proj&install_id=18"} {
		copy := map[string]any{}
		for k, v := range args {
			copy[k] = v
		}
		copy["redirect_url"] = raw
		if _, err := app.startBankAuthorization(ctx, copy); err == nil {
			t.Fatal(raw)
		}
	}
	args["bank_name"] = "Unknown"
	if _, err := app.startBankAuthorization(ctx, args); err == nil {
		t.Fatal("unknown bank accepted")
	}
	args["bank_name"] = "Test Bank"
	if p.starts != 0 {
		t.Fatal("issued invalid authorization")
	}
	if _, err := app.startBankAuthorization(ctx, args); err != nil {
		t.Fatal(err)
	}
	ctx.AppDB().Exec(`UPDATE bank_authorizations SET expires_at=?`, time.Now().Add(-time.Minute).Unix())
	if err := app.completeBankAuthorization(ctx, p.state, "code", ""); err == nil {
		t.Fatal("expired state accepted")
	}
	if p.exchanges != 0 {
		t.Fatal("expired code exchanged")
	}
}
func TestBankAuthorizationFailedExchangeCannotReplay(t *testing.T) {
	app, ctx, p, args := authorizationFixture(t)
	p.failExchange = true
	if _, err := app.startBankAuthorization(ctx, args); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := app.completeBankAuthorization(ctx, p.state, "code", ""); err == nil {
			t.Fatal("expected error")
		}
	}
	if p.exchanges != 1 {
		t.Fatal(p.exchanges)
	}
}
func TestBankAuthorizationCallbackIsPublicOnlyForCallback(t *testing.T) {
	app := &App{}
	found := false
	for _, r := range app.HTTPRoutes() {
		if r.Pattern == "/banking/enable/callback" {
			found = r.NoAuth && r.Method == "GET"
		}
		if r.Pattern == "/banking/enable/authorizations" && r.NoAuth {
			t.Fatal("session list is public")
		}
	}
	if !found {
		t.Fatal("callback inaccessible after bank redirect")
	}
}
