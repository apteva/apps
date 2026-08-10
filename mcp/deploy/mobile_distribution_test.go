package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type distributionPlatform struct {
	tk.BasePlatformClient
	calls        []integrationCall
	appleMembers map[string]distributionAudienceMember
	playGroups   []string
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
		p.playGroups = append([]string{}, stringSliceValue(input["googleGroups"])...)
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

func TestAndroidDistributionUnionsGroupsAndCommits(t *testing.T) {
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
		"channel":  "internal",
		"audience": []any{map[string]any{"kind": "group", "email": "qa@example.com"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if state.Count != 2 || state.Audience[0].Email != "existing@example.com" || state.Audience[1].Email != "qa@example.com" {
		t.Fatalf("state=%+v", state)
	}
	want := []string{"create_edit", "get_track_testers", "update_track_testers", "commit_edit"}
	if len(platform.calls) != len(want) {
		t.Fatalf("calls=%+v", platform.calls)
	}
	for i, tool := range want {
		if platform.calls[i].Tool != tool {
			t.Fatalf("call %d=%s, want %s", i, platform.calls[i].Tool, tool)
		}
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
