package main

import (
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type distributionPlatform struct {
	tk.BasePlatformClient
	calls              []integrationCall
	appleMembers       map[string]distributionAudienceMember
	playGroups         []string
	failTool           string
	ignoreTesterUpdate bool
}

func (p *distributionPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{Bindings: map[string]any{
		"app_store":  int64(77),
		"play_store": int64(88),
	}}, nil
}

func (p *distributionPlatform) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	slug := "app-store-connect"
	if id == 88 {
		slug = "google-play-developer"
	}
	return &sdk.PlatformConnection{ID: id, AppSlug: slug, Status: "active"}, nil
}

func (p *distributionPlatform) ExecuteIntegrationTool(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	p.calls = append(p.calls, integrationCall{Tool: tool, Input: input})
	if tool == p.failTool {
		return &sdk.ExecuteResult{
			Success: false,
			Status:  403,
			Data:    json.RawMessage(`{"error":{"message":"forbidden"}}`),
		}, nil
	}
	status := 200
	data := json.RawMessage(`{}`)
	switch tool {
	case "list_beta_groups":
		data = json.RawMessage(`{"data":[{"id":"group-1","attributes":{"name":"Deploy Internal","isInternalGroup":true}}]}`)
	case "get_beta_group":
		data = json.RawMessage(`{"data":{"id":"group-1","attributes":{"name":"Deploy Internal","isInternalGroup":true}}}`)
	case "list_beta_testers":
		email := fmt.Sprint(input["email"])
		var members []distributionAudienceMember
		if email != "<nil>" && email != "" {
			if member, ok := p.appleMembers[email]; ok {
				members = append(members, member)
			}
		} else {
			for _, member := range p.appleMembers {
				if member.State == "in-group" || member.State == "invited" || member.State == "accepted" {
					members = append(members, member)
				}
			}
		}
		payload := map[string]any{"data": []map[string]any{}}
		for _, member := range members {
			payload["data"] = append(payload["data"].([]map[string]any), map[string]any{
				"id": member.ExternalID, "attributes": map[string]any{
					"email": member.Email, "firstName": member.FirstName,
					"lastName": member.LastName, "state": member.State,
				},
			})
		}
		data, _ = json.Marshal(payload)
	case "add_beta_testers_to_beta_group":
		body := input["body"].(map[string]any)
		linkages := body["data"].([]map[string]any)
		for email, member := range p.appleMembers {
			if member.ExternalID == linkages[0]["id"] {
				member.State = "in-group"
				p.appleMembers[email] = member
			}
		}
		status = 204
	case "create_edit":
		data = json.RawMessage(`{"id":"edit-1"}`)
	case "get_track_testers":
		data, _ = json.Marshal(map[string]any{"googleGroups": p.playGroups})
	case "update_track_testers":
		if !p.ignoreTesterUpdate {
			p.playGroups = append([]string{}, stringSliceValue(input["googleGroups"])...)
		}
	case "commit_edit":
		data = json.RawMessage(`{"id":"edit-1","expiryTimeSeconds":"0"}`)
	case "delete_edit":
		status = 204
	}
	return &sdk.ExecuteResult{Success: true, Status: status, Data: data}, nil
}

func withDistributionContext(t *testing.T, platform *distributionPlatform) *sdk.AppCtx {
	t.Helper()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"), tk.WithPlatform(platform))
	oldGlobal := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = oldGlobal })
	return ctx
}

func TestIOSDistributionAddsExistingTesterToGroupIdempotently(t *testing.T) {
	platform := &distributionPlatform{appleMembers: map[string]distributionAudienceMember{
		"tester@example.com": {
			Kind: "individual", Email: "tester@example.com", FirstName: "Test",
			LastName: "User", State: "not-in-group", ExternalID: "tester-1",
		},
	}}
	ctx := withDistributionContext(t, platform)
	d, err := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{
		Name: "ios", TargetKind: "ios", SourceKind: "local", SourceRef: "/src", Framework: "ios",
		TargetConfigJSON: `{"app_store_app_id":"app-1","bundle_id":"com.example.ios"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	env, _ := dbEnsureProductionEnvironment(ctx.AppDB(), d)
	d = effectiveDeploymentForEnvironment(d, env)
	app := &App{}
	args := map[string]any{
		"channel":  "internal",
		"audience": []any{map[string]any{"kind": "individual", "email": "tester@example.com"}},
	}
	state, err := app.updateMobileDistribution(d, args)
	if err != nil {
		t.Fatal(err)
	}
	if state.Count != 1 || state.Audience[0].Email != "tester@example.com" || state.GroupID != "group-1" {
		t.Fatalf("state=%+v", state)
	}
	if got := countIntegrationCalls(platform.calls, "add_beta_testers_to_beta_group"); got != 1 {
		t.Fatalf("add relationship calls=%d, want 1; calls=%+v", got, platform.calls)
	}

	platform.calls = nil
	if _, err := app.updateMobileDistribution(d, args); err != nil {
		t.Fatal(err)
	}
	if got := countIntegrationCalls(platform.calls, "add_beta_testers_to_beta_group"); got != 0 {
		t.Fatalf("repeat add relationship calls=%d, want 0; calls=%+v", got, platform.calls)
	}
	if got := countIntegrationCalls(platform.calls, "create_beta_tester"); got != 0 {
		t.Fatalf("repeat create tester calls=%d, want 0; calls=%+v", got, platform.calls)
	}
}

func TestAndroidDistributionReplacesGroupsAndVerifies(t *testing.T) {
	platform := &distributionPlatform{playGroups: []string{"existing@example.com"}}
	ctx := withDistributionContext(t, platform)
	d, err := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{
		Name: "android", TargetKind: "android", SourceKind: "local", SourceRef: "/src", Framework: "android",
		TargetConfigJSON: `{"package_name":"com.example.android"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	env, _ := dbEnsureProductionEnvironment(ctx.AppDB(), d)
	d = effectiveDeploymentForEnvironment(d, env)
	state, err := (&App{}).updateMobileDistribution(d, map[string]any{
		"channel":     "internal",
		"install_url": "https://play.google.com/store/apps/details?id=com.example.android",
		"audience":    []any{map[string]any{"kind": "group", "email": "qa@example.com"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.Count != 1 || state.Audience[0].Email != "qa@example.com" || !state.Synced || state.TesterAccess != "configured" {
		t.Fatalf("state=%+v", state)
	}
	if state.InstallURL != "https://play.google.com/store/apps/details?id=com.example.android" {
		t.Fatalf("install_url=%q", state.InstallURL)
	}
	want := []string{
		"create_edit", "get_track_testers", "update_track_testers", "validate_edit", "commit_edit",
		"create_edit", "get_track_testers", "delete_edit",
	}
	if len(platform.calls) != len(want) {
		t.Fatalf("calls=%+v", platform.calls)
	}
	for i, tool := range want {
		if platform.calls[i].Tool != tool {
			t.Fatalf("call %d=%s, want %s", i, platform.calls[i].Tool, tool)
		}
	}

	platform.calls = nil
	state, err = (&App{}).updateMobileDistribution(d, map[string]any{
		"channel":     "internal",
		"install_url": "https://play.google.com/store/apps/details?id=com.example.android",
		"google_groups": []any{
			"qa@example.com",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !state.Synced || countIntegrationCalls(platform.calls, "update_track_testers") != 0 || countIntegrationCalls(platform.calls, "commit_edit") != 0 {
		t.Fatalf("idempotent state=%+v calls=%+v", state, platform.calls)
	}

	platform.calls = nil
	state, err = (&App{}).updateMobileDistribution(d, map[string]any{
		"channel": "internal", "audience": []any{},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(platform.playGroups) != 0 || !state.Synced || state.TesterAccess != "not_configured" {
		t.Fatalf("removal state=%+v groups=%v", state, platform.playGroups)
	}
	if countIntegrationCalls(platform.calls, "update_track_testers") != 1 || countIntegrationCalls(platform.calls, "commit_edit") != 1 {
		t.Fatalf("removal calls=%+v", platform.calls)
	}

	cfg, err := dbGetMobileStoreConfig(ctx.AppDB(), d.ID, d.EnvironmentID, "android")
	if err != nil || cfg == nil {
		t.Fatalf("store config=%+v err=%v", cfg, err)
	}
	var doc StoreDocument
	if err := json.Unmarshal([]byte(cfg.DesiredJSON), &doc); err != nil {
		t.Fatal(err)
	}
	if channel, ok := doc.Testing.Channels["internal"]; !ok || len(channel.Audience) != 0 {
		t.Fatalf("persisted testing=%+v", doc.Testing)
	}
}

func TestAndroidDistributionCleansUpFailedEdit(t *testing.T) {
	platform := &distributionPlatform{failTool: "update_track_testers"}
	ctx := withDistributionContext(t, platform)
	d, err := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{
		Name: "android", TargetKind: "android", SourceKind: "local", SourceRef: "/src", Framework: "android",
		TargetConfigJSON: `{"package_name":"com.example.android"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	env, _ := dbEnsureProductionEnvironment(ctx.AppDB(), d)
	d = effectiveDeploymentForEnvironment(d, env)
	_, err = (&App{}).updateMobileDistribution(d, map[string]any{
		"channel": "qa-ring", "google_groups": []any{"qa@example.com"},
	})
	if err == nil || !strings.Contains(err.Error(), "replace Google Play testers") {
		t.Fatalf("error=%v", err)
	}
	if countIntegrationCalls(platform.calls, "delete_edit") != 1 || countIntegrationCalls(platform.calls, "commit_edit") != 0 {
		t.Fatalf("calls=%+v", platform.calls)
	}
}

func TestAndroidDistributionFailsWhenCommittedStateDoesNotMatch(t *testing.T) {
	platform := &distributionPlatform{ignoreTesterUpdate: true}
	ctx := withDistributionContext(t, platform)
	d, err := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{
		Name: "android", TargetKind: "android", SourceKind: "local", SourceRef: "/src", Framework: "android",
		TargetConfigJSON: `{"package_name":"com.example.android"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	env, _ := dbEnsureProductionEnvironment(ctx.AppDB(), d)
	d = effectiveDeploymentForEnvironment(d, env)
	_, err = (&App{}).updateMobileDistribution(d, map[string]any{
		"channel": "internal", "google_groups": []any{"qa@example.com"},
	})
	if err == nil || !strings.Contains(err.Error(), "does not match") {
		t.Fatalf("error=%v", err)
	}
}

func TestAndroidReleaseAppliesConfiguredTestersInSameEdit(t *testing.T) {
	platform := &distributionPlatform{}
	ctx := withDistributionContext(t, platform)
	d, err := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{
		Name: "android", TargetKind: "android", SourceKind: "local", SourceRef: "/src", Framework: "android",
		TargetConfigJSON: `{"package_name":"com.example.android"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	env, err := dbEnsureProductionEnvironment(ctx.AppDB(), d)
	if err != nil {
		t.Fatal(err)
	}
	d = effectiveDeploymentForEnvironment(d, env)
	doc := defaultStoreDocument("android")
	doc.Testing.Channels["internal"] = StoreTestingChannel{
		Audience:   []StoreTestingAudience{{Kind: "group", Identifier: "qa@example.com"}},
		InstallURL: "https://play.google.com/store/apps/details?id=com.example.android",
	}
	if _, err := dbUpsertMobileStoreConfig(ctx.AppDB(), d, doc); err != nil {
		t.Fatal(err)
	}
	build, err := dbCreateBuildForEnv(ctx.AppDB(), d.ID, env.ID, "android", "")
	if err != nil {
		t.Fatal(err)
	}
	release, err := dbCreateReleaseForEnv(ctx.AppDB(), d.ID, env.ID, build.ID)
	if err != nil {
		t.Fatal(err)
	}
	meta := mobileReleaseMeta{Platform: "android", PackageName: "com.example.android", VersionCode: "42"}
	if err := (&App{}).publishAndroidVersionToTrack(release.ID, d, "internal", &meta, io.Discard); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"create_edit", "update_track", "get_track_testers", "update_track_testers", "validate_edit", "commit_edit",
		"create_edit", "get_track_testers", "delete_edit",
	}
	if len(platform.calls) != len(want) {
		t.Fatalf("calls=%+v", platform.calls)
	}
	for i, tool := range want {
		if platform.calls[i].Tool != tool {
			t.Fatalf("call %d=%s, want %s", i, platform.calls[i].Tool, tool)
		}
	}
	if meta.TesterAccess != "configured" || meta.TesterCount != 1 || len(meta.TesterGroups) != 1 || meta.TesterGroups[0] != "qa@example.com" {
		t.Fatalf("release metadata=%+v", meta)
	}
	if meta.InstallURL == "" || meta.TesterSyncedAt == "" {
		t.Fatalf("release metadata missing install/sync state: %+v", meta)
	}
}

func TestDistributionRejectsProductionTesterConfiguration(t *testing.T) {
	platform := &distributionPlatform{}
	ctx := withDistributionContext(t, platform)
	d, err := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{
		Name: "android", TargetKind: "android", SourceKind: "local", SourceRef: "/src", Framework: "android",
		TargetConfigJSON: `{"package_name":"com.example.android"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	env, _ := dbEnsureProductionEnvironment(ctx.AppDB(), d)
	d = effectiveDeploymentForEnvironment(d, env)
	_, err = (&App{}).updateMobileDistribution(d, map[string]any{
		"channel": "production", "google_groups": []any{"qa@example.com"},
	})
	if err == nil || !strings.Contains(err.Error(), "do not have a test audience") {
		t.Fatalf("error=%v", err)
	}
}

func TestAndroidDistributionAcceptsCustomAndFormFactorTracks(t *testing.T) {
	for _, track := range []string{"qa-ring", "wear:production", "automotive:beta"} {
		normalized, err := normalizeMobileChannel("android", track)
		if err != nil || normalized != track {
			t.Fatalf("track=%q normalized=%q err=%v", track, normalized, err)
		}
	}
	if !isProductionMobileChannel("android", "wear:production") || isProductionMobileChannel("android", "qa-ring") {
		t.Fatal("form-factor production track classification is incorrect")
	}
}

func TestDistributionRejectsProviderUnsupportedAudienceKind(t *testing.T) {
	platform := &distributionPlatform{}
	ctx := withDistributionContext(t, platform)
	d, err := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{
		Name: "android", TargetKind: "android", SourceKind: "local", SourceRef: "/src", Framework: "android",
		TargetConfigJSON: `{"package_name":"com.example.android"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	env, _ := dbEnsureProductionEnvironment(ctx.AppDB(), d)
	d = effectiveDeploymentForEnvironment(d, env)
	_, err = (&App{}).updateMobileDistribution(d, map[string]any{
		"channel":  "internal",
		"audience": []any{map[string]any{"kind": "individual", "email": "person@example.com"}},
	})
	if err == nil || !strings.Contains(err.Error(), "Google Group") {
		t.Fatalf("error=%v", err)
	}
}

func countIntegrationCalls(calls []integrationCall, tool string) int {
	count := 0
	for _, call := range calls {
		if call.Tool == tool {
			count++
		}
	}
	return count
}
