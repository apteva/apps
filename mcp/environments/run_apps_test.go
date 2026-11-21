package main

import (
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
)

// appsByNamePlatform reports the apps a request named, as a server that runs
// apps by name does. With ignoreApps it behaves like an older server.
type appsByNamePlatform struct {
	*orderPlatform
	ignoreApps bool
}

func (p *appsByNamePlatform) CreateRuntime(req sdk.RuntimeCreateRequest) (*sdk.RuntimeSummary, error) {
	summary, err := p.orderPlatform.CreateRuntime(req)
	if err != nil || p.ignoreApps {
		return summary, err
	}
	out := *summary
	for _, app := range req.Apps {
		out.Apps = append(out.Apps, sdk.RuntimeApp{Name: strings.ToLower(app.Name), Kind: "install", Status: "running"})
	}
	return &out, nil
}

func appsByNameTestApp(t *testing.T, ignoreApps bool) (*App, *appsByNamePlatform) {
	t.Helper()
	a, base := orderTestApp(t)
	p := &appsByNamePlatform{orderPlatform: base, ignoreApps: ignoreApps}
	a.svc.ctx = sdk.NewAppCtxForTest(nil, a.svc.db.db, nil, p, nil).WithProject("audit")
	return a, p
}

func TestRunAppsReachRuntimeCreate(t *testing.T) {
	a, p := appsByNameTestApp(t, false)
	spec := EnvironmentSpec{
		Apps:                []sdk.RuntimeAppSource{{Name: "telephony", Version: ">=0.27.1"}},
		IntegrationBindings: []sdk.RuntimeIntegrationBinding{{App: "telephony", Role: "carrier", Slug: "telnyx", Simulate: true}},
		AppConfig:           map[string]map[string]string{"telephony": {"sip_enabled": "true"}},
	}
	if _, err := a.svc.start("", "test", spec); err != nil {
		t.Fatal(err)
	}
	if len(p.created) != 1 {
		t.Fatalf("created=%+v", p.created)
	}
	got := p.created[0]
	if len(got.Apps) != 1 || got.Apps[0].Name != "telephony" || got.Apps[0].Version != ">=0.27.1" || len(got.AppInstallIDs) != 0 {
		t.Fatalf("runtime request apps=%+v installs=%v", got.Apps, got.AppInstallIDs)
	}
	if p.destroyed != 0 {
		t.Fatalf("run torn down: destroy attempts=%d", p.destroyed)
	}
}

func TestRunAppsOlderServerFailsTheRun(t *testing.T) {
	a, p := appsByNameTestApp(t, true)
	_, err := a.svc.start("", "test", EnvironmentSpec{Apps: []sdk.RuntimeAppSource{{Name: "telephony"}}})
	if err == nil || !strings.Contains(err.Error(), "does not run apps by name") || !strings.Contains(err.Error(), "telephony") {
		t.Fatalf("err=%v", err)
	}
	if p.destroyed != 1 {
		t.Fatalf("runtime left behind: destroy attempts=%d", p.destroyed)
	}
}

func TestRunAppsValidation(t *testing.T) {
	tooMany := make([]sdk.RuntimeAppSource, maxRunApps)
	for i := range tooMany {
		tooMany[i] = sdk.RuntimeAppSource{Name: "app-" + string(rune('a'+i))}
	}
	bad := []EnvironmentSpec{
		{Apps: []sdk.RuntimeAppSource{{Name: " "}}},
		{Apps: []sdk.RuntimeAppSource{{Name: "telephony"}, {Name: "Telephony"}}},
		{Apps: tooMany, AppInstallIDs: []int64{7}},
	}
	for i, spec := range bad {
		if err := validateSpec(spec); err == nil {
			t.Fatalf("bad %d accepted", i)
		}
	}
	if err := validateSpec(EnvironmentSpec{Apps: tooMany}); err != nil {
		t.Fatalf("%d apps refused: %v", maxRunApps, err)
	}
	spec, err := orderTestAppSpec(t, `{"apps":[{"name":"telephony","version":">=0.27.1"}]}`)
	if err != nil || len(spec.Apps) != 1 || spec.Apps[0].Version != ">=0.27.1" {
		t.Fatalf("apps did not decode: spec=%+v err=%v", spec.Apps, err)
	}
	if spec, err = orderTestAppSpec(t, `{"apps":[{"version":"1.0.0"}]}`); err != nil || validateSpec(spec) == nil {
		t.Fatalf("an app without a name passed: err=%v", err)
	}
	items := environmentSpecSchema["properties"].(map[string]any)["apps"].(map[string]any)["items"].(map[string]any)
	if required, _ := items["required"].([]string); len(required) != 1 || required[0] != "name" {
		t.Fatalf("apps schema does not require name: %+v", items)
	}
}

func TestRunAppsChangeSpecDigest(t *testing.T) {
	if mustDigest(t, `{"version":1}`) == mustDigest(t, `{"version":1,"apps":[{"name":"telephony"}]}`) {
		t.Fatal("apps did not change spec_digest")
	}
}
