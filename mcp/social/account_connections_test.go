package main

import (
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	"net/http/httptest"
	"strings"
	"testing"
)

type unfilteredConnections struct{ *recordingPlatform }

func (p unfilteredConnections) ListConnections(sdk.ConnectionFilter) ([]sdk.PlatformConnection, error) {
	return p.connections, nil
}
func connectionFixture(id int64, platform, pid, status string) sdk.PlatformConnection {
	return sdk.PlatformConnection{ID: id, AppSlug: platforms[platform].IntegrationSlug, Name: fmt.Sprintf("Account %d", id), ProjectID: pid, Status: status}
}
func TestAccountConnectionChoicesScopeStatusAndLabels(t *testing.T) {
	pf := newRecordingPlatform()
	pf.connections = []sdk.PlatformConnection{connectionFixture(12, "pinterest", "test-proj", "active"), connectionFixture(11, "pinterest", "test-proj", "active"), connectionFixture(13, "pinterest", "other", "active"), connectionFixture(14, "youtube", "test-proj", "active"), connectionFixture(15, "pinterest", "test-proj", "disconnected"), connectionFixture(11, "pinterest", "test-proj", "active")}
	ctx := newSocialCtx(t, unfilteredConnections{pf})
	app := &App{}
	created, err := app.toolProfileCreate(ctx, map[string]any{"name": "Outfit Encore"})
	if err != nil {
		t.Fatal(err)
	}
	profile := created.(map[string]any)["profile"].(*Profile)
	_, err = ctx.AppDB().Exec(`INSERT INTO social_accounts(project_id,platform,connection_id,display_name,profile_id) VALUES('test-proj','pinterest',11,'outfitencore',?)`, profile.ID)
	if err != nil {
		t.Fatal(err)
	}
	choices, err := accountConnectionChoices(ctx, "test-proj", platforms["pinterest"])
	if err != nil || len(choices) != 2 || choices[0].ID != 11 || choices[1].ID != 12 || choices[0].Accounts[0].ProfileName != "Outfit Encore" {
		t.Fatalf("%+v %v", choices, err)
	}
	raw, _ := app.toolAccountListConnections(ctx, map[string]any{"platform": "pinterest", "project_id": "other"})
	encoded, _ := json.Marshal(raw)
	if strings.Contains(string(encoded), "Account 13") {
		t.Fatal("argument overrode authenticated project")
	}
	if len(pf.executeCalls) > 0 || len(pf.startOAuthCalls) > 0 {
		t.Fatal("list mutated or queried credentials")
	}
}
func TestAccountAddExplicitConnectionAcrossPlatforms(t *testing.T) {
	for _, platform := range []string{"pinterest", "youtube", "facebook", "instagram", "twitter"} {
		t.Run(platform, func(t *testing.T) {
			pf := newRecordingPlatform()
			pf.connections = []sdk.PlatformConnection{connectionFixture(11, platform, "test-proj", "active"), connectionFixture(12, platform, "test-proj", "active")}
			ctx := newSocialCtx(t, pf)
			out, err := (&App{}).toolAccountAdd(ctx, map[string]any{"platform": platform, "connection_id": 12})
			if err != nil {
				t.Fatal(err)
			}
			result, ok := out.(map[string]any)
			if !ok || result["reused_connection"] != int64(12) {
				t.Fatal(out)
			}
			var connection int64
			var status string
			ctx.AppDB().QueryRow(`SELECT connection_id,status FROM pending_accounts WHERE id=?`, result["pending_account_id"]).Scan(&connection, &status)
			if connection != 12 || status != "ready" || len(pf.startOAuthCalls) > 0 {
				t.Fatalf("%d %s %+v", connection, status, pf.startOAuthCalls)
			}
		})
	}
}
func TestAccountAddRejectsWrongConnectionWithoutSideEffects(t *testing.T) {
	for _, input := range []map[string]any{{"connection_id": 99}, {"connection_id": 13}, {"connection_id": 14}, {"connection_id": 15}, {"connection_id": 11, "force_new": true}, {"connection_id": 1.5}, {"connection_id": 0}, {"connection_id": "invalid"}, {"connection_id": 11, "provider": "zernio"}} {
		t.Run(fmt.Sprint(input), func(t *testing.T) {
			pf := newRecordingPlatform()
			pf.connections = []sdk.PlatformConnection{connectionFixture(11, "pinterest", "test-proj", "active"), connectionFixture(13, "pinterest", "other", "active"), connectionFixture(14, "youtube", "test-proj", "active"), connectionFixture(15, "pinterest", "test-proj", "disconnected")}
			ctx := newSocialCtx(t, unfilteredConnections{pf})
			input["platform"] = "pinterest"
			out, err := (&App{}).toolAccountAdd(ctx, input)
			if err != nil {
				t.Fatal(err)
			}
			encoded, _ := json.Marshal(out)
			if !strings.Contains(string(encoded), `"isError":true`) {
				t.Fatal(out)
			}
			var count int
			ctx.AppDB().QueryRow(`SELECT count(*) FROM pending_accounts`).Scan(&count)
			if count != 0 || len(pf.startOAuthCalls) != 0 {
				t.Fatal("rejected selector had side effects")
			}
		})
	}
}
func TestAccountAddAmbiguousRequiresChoiceAndForceNewStartsOAuth(t *testing.T) {
	pf := newRecordingPlatform()
	pf.connections = []sdk.PlatformConnection{connectionFixture(11, "pinterest", "test-proj", "active"), connectionFixture(12, "pinterest", "test-proj", "active")}
	ctx := newSocialCtx(t, pf)
	app := &App{}
	out, err := app.toolAccountAdd(ctx, map[string]any{"platform": "pinterest"})
	if err != nil || out.(map[string]any)["status"] != "select_connection" {
		t.Fatalf("%+v %v", out, err)
	}
	var count int
	ctx.AppDB().QueryRow(`SELECT count(*) FROM pending_accounts`).Scan(&count)
	if count != 0 || len(pf.startOAuthCalls) != 0 {
		t.Fatal("ambiguous choice mutated state")
	}
	out, err = app.toolAccountAdd(ctx, map[string]any{"platform": "pinterest", "force_new": true})
	if err != nil || len(pf.startOAuthCalls) != 1 {
		t.Fatalf("%+v %v", out, err)
	}
	assertDestinationError(t, out, "authorize_url")
	var status, hash string
	ctx.AppDB().QueryRow(`SELECT status,callback_token_hash FROM pending_accounts`).Scan(&status, &hash)
	if status != "pending_oauth" || hash == "" {
		t.Fatal("fresh OAuth bypassed handoff validation")
	}
}
func TestAccountStartHTTPForwardsConnectionAndForceNew(t *testing.T) {
	pf := newRecordingPlatform()
	pf.connections = []sdk.PlatformConnection{connectionFixture(11, "pinterest", "test-proj", "active"), connectionFixture(12, "pinterest", "test-proj", "active")}
	ctx := newSocialCtx(t, pf)
	app := &App{}
	_ = ctx
	for _, body := range []string{`{"platform":"pinterest","connection_id":12}`, `{"platform":"pinterest","force_new":true}`} {
		w := httptest.NewRecorder()
		app.handleAccountsStart(w, httptest.NewRequest("POST", "/accounts/start?project_id=test-proj", strings.NewReader(body)))
		if w.Code != 200 {
			t.Fatal(w.Body.String())
		}
		if strings.Contains(body, "connection_id") && !strings.Contains(w.Body.String(), `"reused_connection":12`) {
			t.Fatal(w.Body.String())
		}
	}
	if len(pf.startOAuthCalls) != 1 {
		t.Fatal("force_new was dropped")
	}
}
func TestPinterestTwoConnectionsKeepSeparateBoardsAndProfiles(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newSocialCtx(t, pf)
	app := &App{}
	first, _ := app.toolProfileCreate(ctx, map[string]any{"name": "Brand One"})
	second, _ := app.toolProfileCreate(ctx, map[string]any{"name": "Brand Two"})
	profiles := []int64{first.(map[string]any)["profile"].(*Profile).ID, second.(map[string]any)["profile"].(*Profile).ID}
	for i, connection := range []int64{11, 12} {
		pf.connections = append(pf.connections, connectionFixture(connection, "pinterest", "test-proj", "active"))
		pf.executeResponses["get_user_account"] = analyticsOK(fmt.Sprintf(`{"username":"brand%d"}`, i))
		started, err := app.toolAccountAdd(ctx, map[string]any{"platform": "pinterest", "connection_id": connection, "profile_id": profiles[i]})
		if err != nil {
			t.Fatal(err)
		}
		pending := started.(map[string]any)["pending_account_id"]
		out, err := app.toolAccountFinalize(ctx, map[string]any{"pending_account_id": pending})
		if err != nil {
			t.Fatal(err)
		}
		assertDestinationError(t, out, "social_account_id")
		id := out.(map[string]any)["social_account_id"]
		_, err = ctx.AppDB().Exec(`UPDATE social_accounts SET default_destination_id=? WHERE id=?`, fmt.Sprintf("board%d", i), id)
		if err != nil {
			t.Fatal(err)
		}
	}
	var count int
	ctx.AppDB().QueryRow(`SELECT count(*) FROM social_accounts WHERE platform='pinterest'`).Scan(&count)
	if count != 2 {
		t.Fatal(count)
	}
	// Reusing Brand One's grant from Brand Two cannot silently move the account.
	started, _ := app.toolAccountAdd(ctx, map[string]any{"platform": "pinterest", "connection_id": 11, "profile_id": profiles[1]})
	out, err := app.toolAccountFinalize(ctx, map[string]any{"pending_account_id": started.(map[string]any)["pending_account_id"]})
	if err != nil {
		t.Fatal(err)
	}
	assertDestinationError(t, out, "already linked to another")
	var profile int64
	var board string
	ctx.AppDB().QueryRow(`SELECT profile_id,default_destination_id FROM social_accounts WHERE connection_id=11`).Scan(&profile, &board)
	if profile != profiles[0] || board != "board0" {
		t.Fatal("existing brand account changed")
	}
}

func TestAccountConnectionNumericJSONIDDoesNotUseExponentFormatting(t *testing.T) {
	pf := newRecordingPlatform()
	pf.connections = []sdk.PlatformConnection{connectionFixture(2000000, "pinterest", "test-proj", "active")}
	ctx := newSocialCtx(t, pf)
	out, err := (&App{}).toolAccountAdd(ctx, map[string]any{"platform": "pinterest", "connection_id": float64(2000000)})
	if err != nil {
		t.Fatal(err)
	}
	if result, ok := out.(map[string]any); !ok || result["reused_connection"] != int64(2000000) {
		t.Fatal(out)
	}
}
