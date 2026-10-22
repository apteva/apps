package main

import (
	"database/sql"
	"encoding/json"
	sdk "github.com/apteva/app-sdk"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"
)

func destinationTestAccount(t *testing.T, ctx *sdk.AppCtx, provider, defaultID string) int64 {
	t.Helper()
	result, err := ctx.AppDB().Exec(`INSERT INTO social_accounts(project_id,platform,connection_id,external_account_id,provider_slug,provider_account_id,display_name,default_destination_id) VALUES('test-proj','pinterest',42,'',?,'provider-account','Pinterest',?)`, provider, defaultID)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.LastInsertId()
	return id
}
func assertDestinationError(t *testing.T, out any, fragment string) {
	t.Helper()
	raw, _ := json.Marshal(out)
	if !strings.Contains(string(raw), fragment) {
		t.Fatalf("expected %q in %s", fragment, raw)
	}
}
func TestDestinationAdaptersListCreateDefault(t *testing.T) {
	for _, provider := range []string{"native", "zernio"} {
		t.Run(provider, func(t *testing.T) {
			pf := newRecordingPlatform()
			ctx := newSocialCtx(t, pf)
			id := destinationTestAccount(t, ctx, provider, "")
			app := &App{}
			adapter := adapterForDestinations("pinterest", provider)
			listRaw := `{"items":[{"id":"board","name":"Recipes"}]}`
			createRaw := `{"id":"new","name":"New"}`
			if provider == "zernio" {
				listRaw = `{"boards":[{"id":"board","name":"Recipes"}]}`
				createRaw = `{"board":{"id":"new","name":"New"}}`
				pf.executeResponses[adapter.DefaultTool] = analyticsOK(`{"success":true}`)
			}
			pf.executeResponses[adapter.ListTool] = analyticsOK(listRaw)
			pf.executeResponses[adapter.CreateTool] = analyticsOK(createRaw)
			out, err := app.toolDestinationList(ctx, map[string]any{"social_account_id": id})
			if err != nil {
				t.Fatal(err)
			}
			list := out.(destinationListResult)
			if list.Status != "ok" || len(list.Items) != 1 || !list.Capabilities.Create {
				t.Fatal(out)
			}
			out, err = app.toolDestinationCreate(ctx, map[string]any{"social_account_id": id, "name": "New", "privacy": "SECRET"})
			if err != nil {
				t.Fatal(err)
			}
			assertDestinationError(t, out, `new`)
			for _, call := range pf.executeCalls {
				if call.ConnID != 42 {
					t.Fatal(call)
				}
				if provider == "zernio" && call.Input["accountId"] != "provider-account" {
					t.Fatal(call)
				}
			}
			out, err = app.toolDestinationDefault(ctx, map[string]any{"social_account_id": id, "destination_id": "board"})
			if err != nil {
				t.Fatal(err)
			}
			assertDestinationError(t, out, `default_destination_id`)
			b, err := loadDestinationAccount(ctx, "test-proj", id)
			if err != nil || b.DefaultID != "board" || b.DefaultName != "Recipes" {
				t.Fatalf("%+v %v", b, err)
			}
			if provider == "zernio" {
				last := pf.executeCalls[len(pf.executeCalls)-1]
				if last.Tool != adapter.DefaultTool || last.Input["defaultBoardId"] != "board" {
					t.Fatal(last)
				}
			}
		})
	}
}
func TestDestinationPaginationAndIsolation(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newSocialCtx(t, pf)
	id := destinationTestAccount(t, ctx, "native", "")
	pf.executeQueues["list_boards"] = []*sdk.ExecuteResult{analyticsOK(`{"items":[],"bookmark":"next"}`), analyticsOK(`{"items":[{"id":"second","name":"Second"}]}`)}
	b, _ := loadDestinationAccount(ctx, "test-proj", id)
	found, err := findDestination(ctx, b, "second")
	if err != nil || found.ID != "second" || pf.executeCalls[1].Input["bookmark"] != "next" {
		t.Fatalf("%+v %v", found, err)
	}
	before := len(pf.executeCalls)
	for _, action := range []func(*sdk.AppCtx, map[string]any) (any, error){(&App{}).toolDestinationList, (&App{}).toolDestinationCreate, (&App{}).toolDestinationDefault} {
		out, _ := action(ctx.WithProject("other"), map[string]any{"social_account_id": id, "project_id": "test-proj", "name": "Bad", "destination_id": "second"})
		assertDestinationError(t, out, "not found")
	}
	if len(pf.executeCalls) != before {
		t.Fatal("cross-project upstream call")
	}
	pf.executeResponses["list_boards"] = analyticsOK(`{"items":[]}`)
	if _, err = findDestination(ctx, b, "foreign-board"); err == nil {
		t.Fatal("foreign board accepted")
	}
	pf.executeResponses["list_boards"] = analyticsOK(`{}`)
	out, _ := (&App{}).toolDestinationList(ctx, map[string]any{"social_account_id": id})
	assertDestinationError(t, out, "installed integration catalog")
	pf.executeResponses["list_boards"] = analyticsOK(`{"items":[],"bookmark":"loop"}`)
	if _, err = findDestination(ctx, b, "missing"); err == nil || !strings.Contains(err.Error(), "repeated") {
		t.Fatal(err)
	}
}
func TestDestinationFreezeAndDefaultChanges(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newSocialCtx(t, pf)
	id := destinationTestAccount(t, ctx, "native", "old")
	app := &App{}
	targets := []targetSpec{{SocialAccountID: id}}
	if err := freezeTargetDestinations(ctx, "test-proj", targets, true); err != nil {
		t.Fatal(err)
	}
	pf.executeResponses["list_boards"] = analyticsOK(`{"items":[{"id":"new","name":"New"}]}`)
	out, _ := app.toolDestinationDefault(ctx, map[string]any{"social_account_id": id, "destination_id": "new"})
	assertDestinationError(t, out, "default_destination_id")
	if targets[0].Options["destination_id"] != "old" {
		t.Fatal("default rerouted frozen target")
	}
	later := []targetSpec{{SocialAccountID: id}}
	if err := freezeTargetDestinations(ctx, "test-proj", later, true); err != nil || later[0].Options["destination_id"] != "new" {
		t.Fatalf("%+v %v", later, err)
	}
	override := []targetSpec{{SocialAccountID: id, Options: map[string]any{"board_id": "new"}}}
	if err := freezeTargetDestinations(ctx, "test-proj", override, true); err != nil || override[0].Options["board_id"] != nil {
		t.Fatalf("%+v %v", override, err)
	}
	conflict := []targetSpec{{SocialAccountID: id, Options: map[string]any{"destination_id": "new", "boardId": "old"}}}
	if err := freezeTargetDestinations(ctx, "test-proj", conflict, true); err == nil {
		t.Fatal("conflicting aliases accepted")
	}
	foreign := []targetSpec{{SocialAccountID: id, Options: map[string]any{"destination_id": "inaccessible"}}}
	if err := freezeTargetDestinations(ctx, "test-proj", foreign, true); err == nil {
		t.Fatal("foreign override accepted")
	}
}
func TestDestinationRequiredAndUnrelatedAccounts(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newSocialCtx(t, pf)
	id := destinationTestAccount(t, ctx, "native", "")
	draft := []targetSpec{{SocialAccountID: id}}
	if err := freezeTargetDestinations(ctx, "test-proj", draft, false); err != nil {
		t.Fatal(err)
	}
	if err := freezeTargetDestinations(ctx, "test-proj", draft, true); err == nil {
		t.Fatal("publish with no board")
	}
	twitter := analyticsAccount(t, ctx, "test-proj", "twitter", "")
	if err := freezeTargetDestinations(ctx, "test-proj", []targetSpec{{SocialAccountID: twitter}}, true); err != nil {
		t.Fatal(err)
	}
	out, _ := (&App{}).toolDestinationList(ctx, map[string]any{"social_account_id": twitter})
	if out.(destinationListResult).Status != "unsupported" {
		t.Fatal(out)
	}
	before := len(pf.executeCalls)
	out, _ = (&App{}).toolDestinationCreate(ctx, map[string]any{"social_account_id": id, "name": "Board", "privacy": "PROTECTED"})
	assertDestinationError(t, out, "privacy is unsupported")
	if len(pf.executeCalls) != before {
		t.Fatal("invalid create called upstream")
	}
}
func TestZernioDestinationPayloadAndLegacyDefaultGuard(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newSocialCtx(t, pf)
	id := destinationTestAccount(t, ctx, "zernio", "")
	app := &App{}
	pf.executeResponses["list_account_pinterest_boards"] = analyticsOK(`{"boards":[{"id":"old","name":"Old"},{"id":"new","name":"New"}],"defaultBoardId":"old"}`)
	target := []targetSpec{{SocialAccountID: id}}
	if err := freezeTargetDestinations(ctx, "test-proj", target, true); err != nil || target[0].Options["destination_id"] != "old" {
		t.Fatalf("%+v %v", target, err)
	}
	job := publishJob{platform: "pinterest", providerAccountID: "provider-account", options: map[string]any{"destination_id": "old", "title": "Title", "board_section_id": "section"}}
	for _, mode := range []string{postModeDraft, postModeSchedule, postModePublish} {
		input, err := app.zernioWorkflowInput(ctx, job, mode, "2026-12-01T12:00:00Z")
		if err != nil {
			t.Fatal(err)
		}
		psd := input["platforms"].([]any)[0].(map[string]any)["platformSpecificData"].(map[string]any)
		if psd["boardId"] != "old" || psd["boardSectionId"] != "section" || psd["title"] != "Title" {
			t.Fatal(psd)
		}
	}
	_, err := ctx.AppDB().Exec(`INSERT INTO posts(id,project_id,body,status) VALUES(1,'test-proj','Scheduled','scheduled'); INSERT INTO post_targets(post_id,social_account_id,status,options) VALUES(1,?,'pending','{}')`, id)
	if err != nil {
		t.Fatal(err)
	}
	out, _ := app.toolDestinationDefault(ctx, map[string]any{"social_account_id": id, "destination_id": "new"})
	assertDestinationError(t, out, "Existing posts rely")
	for _, call := range pf.executeCalls {
		if call.Tool == "set_default_pinterest_board" {
			t.Fatal("changed unresolved schedule default")
		}
	}
}
func TestDestinationHTTPPathOwnsAccountAndProject(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newSocialCtx(t, pf)
	first := destinationTestAccount(t, ctx, "native", "")
	_ = first
	other := analyticsAccount(t, ctx, "other", "pinterest", "")
	request := httptest.NewRequest(http.MethodPost, "/destinations/accounts/"+toString(other)+"?project_id=test-proj", strings.NewReader(`{"project_id":"other","social_account_id":1,"name":"Bad"}`))
	response := httptest.NewRecorder()
	(&App{}).handleDestinationAccount(response, request)
	if len(pf.executeCalls) != 0 || !strings.Contains(response.Body.String(), "not found") {
		t.Fatal(response.Body.String())
	}
	for _, route := range (&App{}).HTTPRoutes() {
		if strings.HasPrefix(route.Pattern, "/destinations") && route.NoAuth {
			t.Fatal("unauthenticated destination route")
		}
	}
}
func TestDestinationMigrationPreservesScheduledTargets(t *testing.T) {
	db, err := sql.Open("sqlite", filepath.Join(t.TempDir(), "existing.db"))
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	paths, _ := filepath.Glob("migrations/*.sql")
	sort.Strings(paths)
	for _, path := range paths {
		if strings.Contains(path, "026_") {
			continue
		}
		body, _ := os.ReadFile(path)
		if _, err = db.Exec(string(body)); err != nil {
			t.Fatal(path, err)
		}
	}
	_, err = db.Exec(`INSERT INTO social_accounts(id,project_id,platform,connection_id,external_account_id,display_name,provider_slug,provider_data) VALUES(1,'p','pinterest',42,'legacy','Recipes','native','{}'),(2,'p','pinterest',43,'provider-account','Provider','zernio','{"defaultBoardId":"saved"}'); INSERT INTO posts(id,project_id,body,status,revision,approved_revision) VALUES(1,'p','Keep me','scheduled',3,3); INSERT INTO post_targets(id,post_id,social_account_id,status,options,platform_post_id) VALUES(1,1,1,'pending','{}',NULL),(2,1,1,'published','{"destination_id":"override"}','pin'),(3,1,2,'pending','{}',NULL);`)
	if err != nil {
		t.Fatal(err)
	}
	body, _ := os.ReadFile("migrations/026_destinations.sql")
	if _, err = db.Exec(string(body)); err != nil {
		t.Fatal(err)
	}
	for id, want := range map[int]string{1: "legacy", 2: "override", 3: "saved"} {
		var got string
		if err = db.QueryRow(`SELECT json_extract(options,'$.destination_id') FROM post_targets WHERE id=?`, id).Scan(&got); err != nil || got != want {
			t.Fatalf("target %d: %s %v", id, got, err)
		}
	}
	var status, identity string
	var revision, approved int
	db.QueryRow(`SELECT status,revision,approved_revision FROM posts WHERE id=1`).Scan(&status, &revision, &approved)
	db.QueryRow(`SELECT platform_post_id FROM post_targets WHERE id=2`).Scan(&identity)
	if status != "scheduled" || revision != 3 || approved != 3 || identity != "pin" {
		t.Fatalf("history changed: %s %d %d %s", status, revision, approved, identity)
	}
}

func TestPinterestAccountConnectWithoutBoardAndReconnectDedup(t *testing.T) {
	pf := newRecordingPlatform()
	pf.executeResponses["get_user_account"] = analyticsOK(`{"username":"Creator","profile_image":""}`)
	ctx := newSocialCtx(t, pf)
	app := &App{}
	for i := 0; i < 2; i++ {
		res, err := ctx.AppDB().Exec(`INSERT INTO pending_accounts(project_id,platform,integration_slug,connection_id,status,expires_at) VALUES('test-proj','pinterest','pinterest',42,'ready',datetime('now','+10 minutes'))`)
		if err != nil {
			t.Fatal(err)
		}
		pending, _ := res.LastInsertId()
		out, err := app.toolAccountFinalize(ctx, map[string]any{"pending_account_id": pending})
		if err != nil {
			t.Fatal(err)
		}
		assertDestinationError(t, out, "social_account_id")
	}
	var count int
	var ext, kind string
	ctx.AppDB().QueryRow(`SELECT count(*),COALESCE(external_account_id,''),account_kind FROM social_accounts WHERE platform='pinterest'`).Scan(&count, &ext, &kind)
	if count != 1 || ext != "" || kind != "account" {
		t.Fatalf("board identity persisted or duplicate: %d %s %s", count, ext, kind)
	}
	for _, call := range pf.executeCalls {
		if call.Tool == "list_boards" {
			t.Fatal("connecting requires boards")
		}
	}
}
func TestDestinationMetricsScopesStaySeparate(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newSocialCtx(t, pf)
	id := destinationTestAccount(t, ctx, "native", "one")
	app := &App{}
	for _, board := range []string{"one", "two"} {
		q := analyticsQuery{Scope: "destination", DestinationID: board, RangeDays: 7}
		res := accountMetricsResult{SocialAccountID: id, Platform: "pinterest", Status: "ok", Available: []string{"impressions"}, Impressions: 7, Insights: insightSeries{"impressions": {{Time: "2026-10-20", Value: 7}}}, Report: newAnalyticsReport("destination", board, "post_aggregation", q, "range")}
		if err := app.persistAccountMetrics(ctx, "test-proj", res, q); err != nil {
			t.Fatal(err)
		}
	}
	var scopes int
	ctx.AppDB().QueryRow(`SELECT count(DISTINCT scope) FROM social_metric_points WHERE social_account_id=?`, id).Scan(&scopes)
	if scopes != 2 {
		t.Fatal("boards mixed", scopes)
	}
	var unsafe int
	ctx.AppDB().QueryRow(`SELECT count(*) FROM social_metric_points WHERE scope='account'`).Scan(&unsafe)
	if unsafe != 0 {
		t.Fatal("board metrics labeled account")
	}
	provider := destinationTestAccount(t, ctx, "zernio", "")
	before := len(pf.executeCalls)
	res := app.getAccountMetrics(ctx, "test-proj", provider, "day", analyticsQuery{Scope: "destination", DestinationID: "one", RangeDays: 7})
	if res.Status != "unsupported" || len(pf.executeCalls) != before {
		t.Fatalf("account stats relabeled board: %+v", res)
	}
}
func TestDestinationDirectAndZernioPublicationOverrides(t *testing.T) {
	for _, provider := range []string{"native", "zernio"} {
		t.Run(provider, func(t *testing.T) {
			pf := newRecordingPlatform()
			ctx := newSocialCtx(t, pf)
			app := &App{}
			job := publishJob{platform: "pinterest", connID: 42, extID: "legacy-board", providerAccountID: "provider-account", body: "Caption", options: map[string]any{"destination_id": "selected", "title": "Title"}, media: []mediaItem{{URL: "https://media.example/pin.jpg", Mime: "image/jpeg"}}}
			if provider == "native" {
				pf.executeResponses["create_pin"] = analyticsOK(`{"id":"pin"}`)
				if _, _, err := app.publishPinterest(ctx, platforms["pinterest"], job); err != nil {
					t.Fatal(err)
				}
				if pf.executeCalls[0].Input["board_id"] != "selected" {
					t.Fatal(pf.executeCalls)
				}
			} else {
				pf.executeResponses["create_post"] = analyticsOK(`{"post":{"id":"provider-post","status":"published","platforms":[{"accountId":"provider-account","status":"published","platformPostId":"pin"}]}}`)
				if _, _, err := app.publishZernio(ctx, job); err != nil {
					t.Fatal(err)
				}
				psd := pf.executeCalls[0].Input["platforms"].([]any)[0].(map[string]any)["platformSpecificData"].(map[string]any)
				if psd["boardId"] != "selected" || psd["title"] != "Title" {
					t.Fatal(psd)
				}
			}
		})
	}
}
func TestPinterestProviderImportPreservesDestinationInDraftRevision(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newSocialCtx(t, pf)
	id := destinationTestAccount(t, ctx, "zernio", "")
	item := map[string]any{"id": "provider-post", "content": "Imported", "status": "draft", "platforms": []any{map[string]any{"accountId": "provider-account", "platform": "pinterest", "platformSpecificData": map[string]any{"boardId": "selected", "boardSectionId": "section"}}}}
	inserted, err := (&App{}).reconcileZernioItem(ctx, "test-proj", id, 0, "provider-account", "", "pinterest", item)
	if err != nil || !inserted {
		t.Fatalf("%v %v", inserted, err)
	}
	var destination string
	ctx.AppDB().QueryRow(`SELECT json_extract(options,'$.destination_id') FROM post_targets WHERE social_account_id=?`, id).Scan(&destination)
	if destination != "selected" {
		t.Fatal("import lost board", destination)
	}
}
func TestDestinationDraftApprovalKeepsFrozenOptions(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newSocialCtx(t, pf)
	id := destinationTestAccount(t, ctx, "native", "old")
	app := &App{}
	out, err := app.toolPostDraftCreate(ctx, map[string]any{"body": "Keep intent", "social_account_ids": []any{id}, "approval_required": true})
	if err != nil {
		t.Fatal(err)
	}
	var postID int64
	ctx.AppDB().QueryRow(`SELECT id FROM posts ORDER BY id DESC LIMIT 1`).Scan(&postID)
	if postID == 0 {
		t.Fatal(out)
	}
	for _, action := range []string{"submit", "approve"} {
		out, err = app.transitionDraftReview(ctx, map[string]any{"post_id": postID, "expected_revision": 1}, action)
		if err != nil {
			t.Fatal(err)
		}
		assertDestinationError(t, out, "post_id")
	}
	pf.executeResponses["list_boards"] = analyticsOK(`{"items":[{"id":"new","name":"New"}]}`)
	out, err = app.toolDestinationDefault(ctx, map[string]any{"social_account_id": id, "destination_id": "new"})
	if err != nil {
		t.Fatal(err)
	}
	assertDestinationError(t, out, "default_destination_id")
	var board, status string
	var revision, approved int
	ctx.AppDB().QueryRow(`SELECT json_extract(t.options,'$.destination_id'),p.status,p.revision,p.approved_revision FROM post_targets t JOIN posts p ON p.id=t.post_id WHERE p.id=?`, postID).Scan(&board, &status, &revision, &approved)
	if board != "old" || status != "approved" || revision != 1 || approved != 1 {
		t.Fatalf("approval intent changed: %s %s %d %d", board, status, revision, approved)
	}
}
func TestDestinationWidgetDoesNotAddBoardAndParentTotals(t *testing.T) {
	pf := newRecordingPlatform()
	ctx := newSocialCtx(t, pf)
	parent := destinationTestAccount(t, ctx, "native", "")
	legacy := analyticsAccount(t, ctx, "test-proj", "pinterest", "board")
	_, err := ctx.AppDB().Exec(`UPDATE social_accounts SET provider_account_id='' WHERE id=?; UPDATE social_accounts SET account_kind='destination_preset',default_destination_id='board' WHERE id=?`, parent, legacy)
	if err != nil {
		t.Fatal(err)
	}
	today := time.Now().UTC().Truncate(24 * time.Hour)
	day := today.AddDate(0, 0, -1).Format(time.RFC3339)
	for _, item := range []struct {
		id    int64
		scope string
		value int64
	}{{parent, "provider_account", 100}, {legacy, "destination:board", 20}} {
		if err := insertSocialMetricPoint(ctx, "test-proj", 0, item.id, 0, 0, "pinterest", item.scope, "impressions", "day", day, item.value, "test", "ok", ""); err != nil {
			t.Fatal(err)
		}
	}
	response := httptest.NewRecorder()
	(&App{}).handlePerformanceWidget(response, httptest.NewRequest("GET", "/widgets/performance?days=7", nil))
	var out struct {
		Accounts []widgetAccount
		Metrics  map[string]widgetMetric
	}
	if err := json.Unmarshal(response.Body.Bytes(), &out); err != nil {
		t.Fatal(err)
	}
	if len(out.Accounts) != 2 || out.Metrics["impressions"].Value == nil || *out.Metrics["impressions"].Value != 100 || out.Accounts[1].AggregateExclusion == "" || *out.Accounts[1].Metrics["impressions"].Value != 20 {
		t.Fatal(response.Body.String())
	}
	if len(pf.executeCalls) != 0 {
		t.Fatal("widget contacted provider")
	}
}
