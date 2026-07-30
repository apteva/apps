package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type testFlightPlatform struct {
	tk.BasePlatformClient
	calls             []integrationCall
	groups            []appleBetaGroup
	buildGroupIDs     []string
	assignmentStatus  int
	assignmentMessage string
	createCount       int
}

func (p *testFlightPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{Bindings: map[string]any{"app_store": int64(77)}}, nil
}

func (p *testFlightPlatform) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	return &sdk.PlatformConnection{ID: id, AppSlug: "app-store-connect", Status: "active"}, nil
}

func (p *testFlightPlatform) ExecuteIntegrationTool(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	p.calls = append(p.calls, integrationCall{Tool: tool, Input: input})
	result := &sdk.ExecuteResult{Success: true, Status: 200, Data: json.RawMessage(`{}`)}
	switch tool {
	case "list_builds":
		result.Data = json.RawMessage(`{"data":[{"id":"build-42","attributes":{"version":"42","processingState":"VALID"}}]}`)
	case "list_beta_groups":
		groups := p.groups
		if name := strings.TrimSpace(fmt.Sprint(input["name"])); name != "" && name != "<nil>" {
			filtered := make([]appleBetaGroup, 0, len(groups))
			for _, group := range groups {
				if strings.EqualFold(group.Name, name) {
					filtered = append(filtered, group)
				}
			}
			groups = filtered
		}
		result.Data = betaGroupsPayload(groups)
	case "get_beta_group":
		id := fmt.Sprint(input["group_id"])
		for _, group := range p.groups {
			if group.ID == id {
				result.Data = betaGroupPayload(group)
				break
			}
		}
	case "create_beta_group":
		p.createCount++
		group := appleBetaGroup{
			ID:         fmt.Sprintf("group-created-%d", p.createCount),
			Name:       fmt.Sprint(input["name"]),
			IsInternal: input["isInternalGroup"] == true,
		}
		p.groups = append(p.groups, group)
		result.Data = betaGroupPayload(group)
	case "add_builds_to_beta_group":
		if p.assignmentStatus >= 400 {
			result.Success = false
			result.Status = p.assignmentStatus
			result.Data, _ = json.Marshal(map[string]any{"errors": []map[string]any{
				{"detail": p.assignmentMessage},
			}})
		} else {
			result.Status = 204
		}
	case "get_build":
		relationships := make([]map[string]any, 0, len(p.buildGroupIDs))
		included := make([]map[string]any, 0, len(p.buildGroupIDs))
		for _, id := range p.buildGroupIDs {
			relationships = append(relationships, map[string]any{"type": "betaGroups", "id": id})
			included = append(included, map[string]any{"type": "betaGroups", "id": id})
		}
		result.Data, _ = json.Marshal(map[string]any{
			"data": map[string]any{
				"id": "build-42",
				"relationships": map[string]any{
					"betaGroups": map[string]any{"data": relationships},
				},
			},
			"included": included,
		})
	}
	return result, nil
}

func betaGroupsPayload(groups []appleBetaGroup) json.RawMessage {
	data := make([]map[string]any, 0, len(groups))
	for _, group := range groups {
		data = append(data, betaGroupResource(group))
	}
	raw, _ := json.Marshal(map[string]any{"data": data})
	return raw
}

func betaGroupPayload(group appleBetaGroup) json.RawMessage {
	raw, _ := json.Marshal(map[string]any{"data": betaGroupResource(group)})
	return raw
}

func betaGroupResource(group appleBetaGroup) map[string]any {
	return map[string]any{
		"type": "betaGroups",
		"id":   group.ID,
		"attributes": map[string]any{
			"name": group.Name, "isInternalGroup": group.IsInternal,
			"hasAccessToAllBuilds": group.HasAccessToAllBuilds,
		},
	}
}

func setupTestFlightRelease(t *testing.T, platform *testFlightPlatform, betaGroupID string) (*App, *Release) {
	t.Helper()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"), tk.WithPlatform(platform))
	oldGlobal := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = oldGlobal })
	deployment, err := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{
		Name: "ios", TargetKind: "ios", SourceKind: "local", SourceRef: "/src", Framework: "ios",
	})
	if err != nil {
		t.Fatal(err)
	}
	env, err := dbEnsureProductionEnvironment(ctx.AppDB(), deployment)
	if err != nil {
		t.Fatal(err)
	}
	build, err := dbCreateBuildForEnv(ctx.AppDB(), deployment.ID, env.ID, "ios", "")
	if err != nil {
		t.Fatal(err)
	}
	release, err := dbCreateReleaseForEnv(ctx.AppDB(), deployment.ID, env.ID, build.ID)
	if err != nil {
		t.Fatal(err)
	}
	meta := mobileReleaseMeta{
		Platform: "ios", AppID: "app-1", BuildNumber: "42", BetaGroupID: betaGroupID,
	}
	if err := dbUpdateRelease(ctx.AppDB(), release.ID, map[string]any{
		"status": "starting", "provider": "app_store_connect", "channel": "internal", "release_meta_json": mustJSON(meta),
	}); err != nil {
		t.Fatal(err)
	}
	release, err = dbGetRelease(ctx.AppDB(), release.ID)
	if err != nil {
		t.Fatal(err)
	}
	return &App{}, release
}

func TestIOSReleaseAllBuildGroupSkipsAssignment(t *testing.T) {
	platform := &testFlightPlatform{groups: []appleBetaGroup{{
		ID: "group-all", Name: "Public", IsInternal: true, HasAccessToAllBuilds: true,
	}}}
	app, release := setupTestFlightRelease(t, platform, "")

	if err := app.syncIOSRelease(release); err != nil {
		t.Fatal(err)
	}
	fresh, _ := dbGetRelease(globalCtx.AppDB(), release.ID)
	if fresh.Status != "live" || fresh.ExternalStatus != "testflight_available" || fresh.ExternalID != "build-42" {
		t.Fatalf("release=%+v", fresh)
	}
	if countIntegrationCalls(platform.calls, "add_builds_to_beta_group") != 0 {
		t.Fatalf("all-build group should skip assignment: calls=%+v", platform.calls)
	}
}

func TestIOSReleaseConfirmedRedundantAssignmentSucceeds(t *testing.T) {
	platform := &testFlightPlatform{
		groups:        []appleBetaGroup{{ID: "group-1", Name: "Deploy Internal", IsInternal: true}},
		buildGroupIDs: []string{"group-1"}, assignmentStatus: 422,
		assignmentMessage: "Cannot add internal group to a build. Builds cannot be assigned to this internal group.",
	}
	app, release := setupTestFlightRelease(t, platform, "group-1")

	if err := app.syncIOSRelease(release); err != nil {
		t.Fatal(err)
	}
	fresh, _ := dbGetRelease(globalCtx.AppDB(), release.ID)
	if fresh.Status != "live" || fresh.ExternalStatus != "testflight_available" {
		t.Fatalf("release=%+v", fresh)
	}
	if countIntegrationCalls(platform.calls, "get_build") != 1 {
		t.Fatalf("expected relationship verification: calls=%+v", platform.calls)
	}
}

func TestIOSReleaseUnrelated422RemainsError(t *testing.T) {
	platform := &testFlightPlatform{
		groups:           []appleBetaGroup{{ID: "group-1", Name: "Deploy Internal", IsInternal: true}},
		assignmentStatus: 422, assignmentMessage: "The relationship is invalid for this resource.",
	}
	app, release := setupTestFlightRelease(t, platform, "group-1")

	err := app.syncIOSRelease(release)
	if err == nil || !strings.Contains(err.Error(), "HTTP 422") {
		t.Fatalf("error=%v", err)
	}
	if countIntegrationCalls(platform.calls, "get_build") != 0 {
		t.Fatalf("unrelated 422 must not be ignored or verified: calls=%+v", platform.calls)
	}
}

func TestIOSReleaseReusesCreatedGroupOnRepeatedPublishing(t *testing.T) {
	platform := &testFlightPlatform{}
	app, release := setupTestFlightRelease(t, platform, "")

	if err := app.syncIOSRelease(release); err != nil {
		t.Fatal(err)
	}
	fresh, _ := dbGetRelease(globalCtx.AppDB(), release.ID)
	if err := app.syncIOSRelease(fresh); err != nil {
		t.Fatal(err)
	}
	if platform.createCount != 1 {
		t.Fatalf("created groups=%d, want 1; calls=%+v", platform.createCount, platform.calls)
	}
	if countIntegrationCalls(platform.calls, "add_builds_to_beta_group") != 2 {
		t.Fatalf("assignment calls=%d, want 2; calls=%+v", countIntegrationCalls(platform.calls, "add_builds_to_beta_group"), platform.calls)
	}
}

func TestIOSReleaseExplicitGroupStillAssignsBuild(t *testing.T) {
	platform := &testFlightPlatform{groups: []appleBetaGroup{{
		ID: "group-explicit", Name: "QA", IsInternal: true,
	}}}
	app, release := setupTestFlightRelease(t, platform, "group-explicit")

	if err := app.syncIOSRelease(release); err != nil {
		t.Fatal(err)
	}
	if countIntegrationCalls(platform.calls, "add_builds_to_beta_group") != 1 {
		t.Fatalf("explicit assignment calls=%d, want 1; calls=%+v", countIntegrationCalls(platform.calls, "add_builds_to_beta_group"), platform.calls)
	}
}

func TestIOSReleaseFailedAssignmentDoesNotCreateDuplicateGroup(t *testing.T) {
	platform := &testFlightPlatform{assignmentStatus: 500, assignmentMessage: "temporary failure"}
	app, release := setupTestFlightRelease(t, platform, "")

	if err := app.syncIOSRelease(release); err == nil {
		t.Fatal("first assignment unexpectedly succeeded")
	}
	fresh, _ := dbGetRelease(globalCtx.AppDB(), release.ID)
	if err := app.syncIOSRelease(fresh); err == nil {
		t.Fatal("second assignment unexpectedly succeeded")
	}
	if platform.createCount != 1 {
		t.Fatalf("created groups=%d, want 1; calls=%+v", platform.createCount, platform.calls)
	}
}
