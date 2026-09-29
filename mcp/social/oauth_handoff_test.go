package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func TestOAuthHandoffRouteIsOnlyPublicGET(t *testing.T) {
	app := &App{}
	manifestFile, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatal(err)
	}
	fileManifest, err := sdk.ParseManifest(manifestFile)
	if err != nil {
		t.Fatal(err)
	}
	for name, manifest := range map[string]sdk.Manifest{"embedded": app.Manifest(), "apteva.yaml": *fileManifest} {
		found := false
		for _, route := range manifest.Provides.HTTPRoutes {
			if route.NoAuth {
				if route.Prefix != "/accounts/oauth_done" || route.Method != http.MethodGet {
					t.Fatalf("%s has unexpected public route: %+v", name, route)
				}
				found = true
			}
		}
		if !found {
			t.Fatalf("%s is missing public callback", name)
		}
	}
	var sdkRoute bool
	for _, route := range app.HTTPRoutes() {
		if route.NoAuth {
			if route.Pattern != "/accounts/oauth_done" || route.Method != http.MethodGet {
				t.Fatalf("unexpected public SDK route: %+v", route)
			}
			sdkRoute = true
		}
	}
	if !sdkRoute {
		t.Fatal("SDK callback route is missing")
	}
}

func startNativeOAuthForTest(t *testing.T) (*App, *sdk.AppCtx, *recordingPlatform, int64, url.Values) {
	t.Helper()
	pf := newRecordingPlatform()
	pf.connections = []sdk.PlatformConnection{{ID: 7, AppSlug: "twitter-api", ProjectID: "test-proj", Status: "active"},
		{ID: 8, AppSlug: "twitter-api", ProjectID: "test-proj", Status: "active"}}
	ctx := newSocialCtx(t, pf)
	app := &App{}
	out, err := app.toolAccountAdd(ctx, map[string]any{"platform": "twitter"})
	if err != nil {
		t.Fatal(err)
	}
	result := out.(map[string]any)
	if result["isError"] == true {
		t.Fatalf("OAuth start failed: %+v", result)
	}
	returnURL, err := url.Parse(pf.startOAuthCalls[0].ReturnURL)
	if err != nil {
		t.Fatal(err)
	}
	return app, ctx, pf, result["pending_account_id"].(int64), returnURL.Query()
}

func TestOAuthStartStoresOnlyTokenHashAndExpectedConnection(t *testing.T) {
	_, ctx, _, pendingID, q := startNativeOAuthForTest(t)
	if q.Get("project_id") != "test-proj" || q.Get("pending") != fmt.Sprint(pendingID) || q.Get("callback_token") == "" {
		t.Fatalf("return URL missing callback binding: %v", q)
	}
	var hash, status string
	var connID int64
	if err := ctx.AppDB().QueryRow(`SELECT callback_token_hash, connection_id, status FROM pending_accounts WHERE id=?`, pendingID).
		Scan(&hash, &connID, &status); err != nil {
		t.Fatal(err)
	}
	if hash == q.Get("callback_token") || hash != oauthCallbackTokenHash(q.Get("callback_token")) || connID != 7 || status != "pending_oauth" {
		t.Fatalf("pending binding: hash=%q conn=%d status=%q", hash, connID, status)
	}
}

func TestOAuthCallbackRejectsInvalidHandoffsAndConsumesValidToken(t *testing.T) {
	app, ctx, _, pendingID, base := startNativeOAuthForTest(t)
	callback := func(q url.Values) string {
		t.Helper()
		rec := httptest.NewRecorder()
		app.handleOAuthDone(rec, httptest.NewRequest(http.MethodGet, "/accounts/oauth_done?"+q.Encode(), nil))
		return rec.Body.String()
	}
	valid := url.Values{}
	for key, values := range base {
		valid[key] = append([]string(nil), values...)
	}
	valid.Set("conn_id", "7")
	valid.Set("status", "ok")
	for _, tc := range []struct {
		name string
		edit func(url.Values)
	}{
		{"missing token", func(q url.Values) { q.Del("callback_token") }},
		{"wrong token", func(q url.Values) { q.Set("callback_token", strings.Repeat("a", 64)) }},
		{"missing project", func(q url.Values) { q.Del("project_id") }},
		{"wrong project", func(q url.Values) { q.Set("project_id", "other") }},
		{"wrong connection", func(q url.Values) { q.Set("conn_id", "8") }},
		{"failed OAuth", func(q url.Values) { q.Set("status", "error") }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			q := url.Values{}
			for key, values := range valid {
				q[key] = append([]string(nil), values...)
			}
			tc.edit(q)
			if body := callback(q); !strings.Contains(body, "social.oauth_error") {
				t.Fatalf("callback accepted invalid request: %s", body)
			}
			var status string
			if err := ctx.AppDB().QueryRow(`SELECT status FROM pending_accounts WHERE id=?`, pendingID).Scan(&status); err != nil || status != "pending_oauth" {
				t.Fatalf("pending status=%q err=%v", status, err)
			}
		})
	}
	if body := callback(valid); !strings.Contains(body, "social.oauth_ready") {
		t.Fatalf("valid callback rejected: %s", body)
	}
	var status, hash string
	if err := ctx.AppDB().QueryRow(`SELECT status, callback_token_hash FROM pending_accounts WHERE id=?`, pendingID).Scan(&status, &hash); err != nil || status != "ready" || hash != "" {
		t.Fatalf("pending status=%q token hash=%q err=%v", status, hash, err)
	}
	if body := callback(valid); !strings.Contains(body, "social.oauth_error") {
		t.Fatalf("replayed callback accepted: %s", body)
	}
}

func TestOAuthCallbackRejectsExpiredToken(t *testing.T) {
	app, ctx, _, pendingID, q := startNativeOAuthForTest(t)
	if _, err := ctx.AppDB().Exec(`UPDATE pending_accounts SET expires_at=? WHERE id=?`, pendingExpiry(time.Now().UTC().Add(-time.Second)), pendingID); err != nil {
		t.Fatal(err)
	}
	q.Set("conn_id", "7")
	q.Set("status", "ok")
	rec := httptest.NewRecorder()
	app.handleOAuthDone(rec, httptest.NewRequest(http.MethodGet, "/accounts/oauth_done?"+q.Encode(), nil))
	if !strings.Contains(rec.Body.String(), "social.oauth_error") {
		t.Fatalf("expired callback accepted: %s", rec.Body.String())
	}
}

func TestOAuthReturnURLCannotLeaveHandoffRoute(t *testing.T) {
	for _, raw := range []string{
		"/", "/api/apps/social/accounts/start", "//example.com/steal",
		"/api/apps/social/accounts/oauth_done?project_id=other",
		"/api/apps/social/accounts/oauth_done?next=/steal",
	} {
		if _, err := socialOAuthReturnURL("test-proj", raw); err == nil {
			t.Errorf("accepted return_to %q", raw)
		}
	}
}

func TestZernioOAuthHandoffRetainsProviderVerification(t *testing.T) {
	pf := newRecordingPlatform()
	pf.connections = []sdk.PlatformConnection{{ID: 99, AppSlug: "zernio", ProjectID: "test-proj", Status: "active"}}
	pf.executeResponses["list_profiles"] = &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"profiles":[{"id":"zp_1"}]}`)}
	pf.executeResponses["get_connect_url"] = &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"url":"https://zernio.example/connect","state":"zs_1"}`)}
	pf.executeResponses["complete_oauth_callback"] = &sdk.ExecuteResult{Success: true, Data: json.RawMessage(`{"state":"zs_1"}`)}
	ctx := newSocialCtx(t, pf)
	app := &App{}
	out, err := app.toolAccountAdd(ctx, map[string]any{"platform": "linkedin", "provider": "zernio"})
	if err != nil || out.(map[string]any)["isError"] == true {
		t.Fatalf("start provider OAuth: out=%+v err=%v", out, err)
	}
	pendingID := out.(map[string]any)["pending_account_id"].(int64)
	var callbackURL string
	for _, call := range pf.executeCalls {
		if call.Tool == "get_connect_url" {
			callbackURL, _ = call.Input["redirect_url"].(string)
		}
	}
	u, err := url.Parse(callbackURL)
	if err != nil || u.Query().Get("callback_token") == "" {
		t.Fatalf("provider callback URL=%q err=%v", callbackURL, err)
	}
	q := u.Query()
	q.Set("state", "zs_1")
	q.Set("code", "provider-code")
	call := func(query url.Values) string {
		rec := httptest.NewRecorder()
		app.handleOAuthDone(rec, httptest.NewRequest(http.MethodGet, "/accounts/oauth_done?"+query.Encode(), nil))
		return rec.Body.String()
	}
	wrong := url.Values{}
	for key, values := range q {
		wrong[key] = append([]string(nil), values...)
	}
	wrong.Set("callback_token", strings.Repeat("a", 64))
	if body := call(wrong); !strings.Contains(body, "social.oauth_error") {
		t.Fatalf("provider wrong token accepted: %s", body)
	}
	if body := call(q); !strings.Contains(body, "social.oauth_ready") {
		t.Fatalf("provider callback rejected: %s", body)
	}
	if body := call(q); !strings.Contains(body, "social.oauth_error") {
		t.Fatalf("provider callback replay accepted: %s", body)
	}
	var status, hash string
	if err := ctx.AppDB().QueryRow(`SELECT status, callback_token_hash FROM pending_accounts WHERE id=?`, pendingID).Scan(&status, &hash); err != nil || status != "ready" || hash != "" {
		t.Fatalf("provider pending status=%q hash=%q err=%v", status, hash, err)
	}
}
