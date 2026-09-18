package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestOptionalConfigurationsAndStagesPreserveLegacyAPIs(t *testing.T) {
	app, ctx := mountTestApp(t)
	oldOrigin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("old")) }))
	newOrigin := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { _, _ = w.Write([]byte("new")) }))
	t.Cleanup(oldOrigin.Close)
	t.Cleanup(newOrigin.Close)
	created, err := app.toolAPICreate(ctx, map[string]any{"slug": "orders"})
	if err != nil {
		t.Fatal(err)
	}
	api := created.(map[string]any)["api"].(*API)
	if _, err := app.toolRouteAdd(ctx, map[string]any{
		"api_id": api.ID, "method": "GET", "path_pattern": "/orders",
		"target_kind": "http", "target_ref": oldOrigin.URL,
	}); err != nil {
		t.Fatal(err)
	}

	configOut, err := app.toolConfigCreate(ctx, map[string]any{"api_id": api.ID, "name": "initial"})
	if err != nil {
		t.Fatal(err)
	}
	config := configOut.(map[string]any)["configuration"].(*APIConfiguration)
	if config.Version != 1 {
		t.Fatalf("configuration version = %d, want 1", config.Version)
	}

	// The old management path remains mutable and does not mutate the snapshot.
	if _, err := app.toolRouteAdd(ctx, map[string]any{
		"api_id": api.ID, "method": "GET", "path_pattern": "/orders",
		"target_kind": "http", "target_ref": newOrigin.URL,
	}); err != nil {
		t.Fatal(err)
	}
	legacy, _, err := dbMatchRoute(ctx.AppDB(), testProject, api.ID, "GET", "/orders")
	if err != nil || legacy == nil || legacy.TargetRef != newOrigin.URL {
		t.Fatalf("legacy route = %#v, err=%v", legacy, err)
	}
	snapshot, _, err := dbMatchConfigurationRoute(ctx.AppDB(), testProject, api.ID, config.ID, "GET", "/orders")
	if err != nil || snapshot == nil || snapshot.TargetRef != oldOrigin.URL {
		t.Fatalf("snapshot route = %#v, err=%v", snapshot, err)
	}

	stageOut, err := app.toolStageCreate(ctx, map[string]any{
		"api_id": api.ID, "name": "testing", "configuration_id": config.ID,
		"hostname": "testing.example",
	})
	if err != nil {
		t.Fatal(err)
	}
	stage := stageOut.(map[string]any)["stage"].(*APIStage)
	stagedAPI, err := dbGetPublicStageAPI(ctx.AppDB(), testProject, "testing.example")
	if err != nil {
		t.Fatal(err)
	}
	if stagedAPI == nil || stagedAPI.StageID != stage.ID || stagedAPI.ConfigurationID != config.ID {
		t.Fatalf("staged API = %#v", stagedAPI)
	}
	if stagedAPI.Status != "active" {
		t.Fatalf("staged status = %q", stagedAPI.Status)
	}
	stageRequest := httptest.NewRequest(http.MethodGet, "http://testing.example/gw/orders?project_id="+testProject, nil)
	stageRequest.Host = "testing.example"
	stageResponse := httptest.NewRecorder()
	app.handleGateway(stageResponse, stageRequest)
	if stageResponse.Code != http.StatusOK || strings.TrimSpace(stageResponse.Body.String()) != "old" {
		t.Fatalf("staged response = %d %q", stageResponse.Code, stageResponse.Body.String())
	}

	cloneOut, err := app.toolConfigClone(ctx, map[string]any{"api_id": api.ID, "configuration_id": config.ID, "name": "rollback-target"})
	if err != nil {
		t.Fatal(err)
	}
	clone := cloneOut.(map[string]any)["configuration"].(*APIConfiguration)
	if clone.Version != 2 {
		t.Fatalf("cloned configuration version = %d, want 2", clone.Version)
	}
	if _, err := app.toolStagePromote(ctx, map[string]any{"stage_id": stage.ID, "configuration_id": clone.ID}); err != nil {
		t.Fatal(err)
	}
	updatedStage, err := dbGetStage(ctx.AppDB(), testProject, stage.ID)
	if err != nil || updatedStage.ConfigurationID != clone.ID {
		t.Fatalf("promoted stage = %#v, err=%v", updatedStage, err)
	}

	// The legacy API hostname still resolves to the mutable API, not the stage.
	legacyAPI, err := dbGetPublicAPI(ctx.AppDB(), testProject, "slug", "orders")
	if err != nil || legacyAPI == nil || legacyAPI.ConfigurationID != 0 {
		t.Fatalf("legacy API = %#v, err=%v", legacyAPI, err)
	}
}

func TestConfigurationDiffReportsRouteChanges(t *testing.T) {
	app, ctx := mountTestApp(t)
	created, err := app.toolAPICreate(ctx, map[string]any{"slug": "diff"})
	if err != nil {
		t.Fatal(err)
	}
	api := created.(map[string]any)["api"].(*API)
	if _, err := app.toolRouteAdd(ctx, map[string]any{"api_id": api.ID, "method": "GET", "path_pattern": "/one", "target_kind": "http", "target_ref": "https://one.example"}); err != nil {
		t.Fatal(err)
	}
	leftOut, err := app.toolConfigCreate(ctx, map[string]any{"api_id": api.ID})
	if err != nil {
		t.Fatal(err)
	}
	left := leftOut.(map[string]any)["configuration"].(*APIConfiguration)
	if _, err := app.toolRouteAdd(ctx, map[string]any{"api_id": api.ID, "method": "GET", "path_pattern": "/two", "target_kind": "http", "target_ref": "https://two.example"}); err != nil {
		t.Fatal(err)
	}
	rightOut, err := app.toolConfigCreate(ctx, map[string]any{"api_id": api.ID})
	if err != nil {
		t.Fatal(err)
	}
	right := rightOut.(map[string]any)["configuration"].(*APIConfiguration)
	diffOut, err := app.toolConfigDiff(ctx, map[string]any{"left_configuration_id": left.ID, "right_configuration_id": right.ID})
	if err != nil {
		t.Fatal(err)
	}
	diff := diffOut.(map[string]any)["diff"].(map[string]any)
	added := diff["added"].([]string)
	if len(added) != 1 || added[0] != "GET /two" {
		t.Fatalf("added = %#v", added)
	}
}
