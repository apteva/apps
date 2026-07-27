package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type mobileSigningPlatform struct {
	tk.BasePlatformClient
	calls          []integrationCall
	appExists      bool
	bundleID       string
	groupID        string
	variables      map[string]string
	certificateSeq int
	profileSeq     int
	failTool       string
}

func (p *mobileSigningPlatform) WhoAmI() (*sdk.InstallIdentity, error) {
	return &sdk.InstallIdentity{Bindings: map[string]any{
		"app_store":   int64(71),
		"cloud_build": int64(72),
	}}, nil
}

func (p *mobileSigningPlatform) GetConnection(id int64) (*sdk.PlatformConnection, error) {
	slug := "app-store-connect"
	if id == 72 {
		slug = "codemagic"
	}
	return &sdk.PlatformConnection{ID: id, AppSlug: slug, Status: "active"}, nil
}

func (p *mobileSigningPlatform) GetConnectionCredentials(id int64) (*sdk.ConnectionCredentials, error) {
	return &sdk.ConnectionCredentials{
		ConnectionID: id,
		Slug:         "app-store-connect",
		Fields: map[string]string{
			"issuer_id":   "issuer-secret",
			"key_id":      "key-secret",
			"private_key": "-----BEGIN PRIVATE KEY-----\napi-secret\n-----END PRIVATE KEY-----",
		},
	}, nil
}

func (p *mobileSigningPlatform) ExecuteIntegrationTool(_ int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	p.calls = append(p.calls, integrationCall{Tool: tool, Input: input})
	if tool == p.failTool {
		return &sdk.ExecuteResult{
			Success: false, Status: http.StatusBadRequest,
			Data: json.RawMessage(`{"error":"synthetic failure"}`),
		}, nil
	}
	var data json.RawMessage
	switch tool {
	case "list_bundle_ids":
		if p.bundleID == "" {
			data = json.RawMessage(`{"data":[]}`)
		} else {
			data = json.RawMessage(fmt.Sprintf(`{"data":[{"id":%q}]}`, p.bundleID))
		}
	case "register_bundle_id":
		p.bundleID = "bundle-resource-1"
		data = json.RawMessage(`{"data":{"id":"bundle-resource-1"}}`)
	case "list_apps":
		if p.appExists {
			data = json.RawMessage(`{"data":[{"id":"app-store-1"}]}`)
		} else {
			data = json.RawMessage(`{"data":[]}`)
		}
	case "create_certificate":
		p.certificateSeq++
		data = json.RawMessage(fmt.Sprintf(`{"data":{"id":"certificate-%d"}}`, p.certificateSeq))
	case "create_profile":
		p.profileSeq++
		data = json.RawMessage(fmt.Sprintf(`{"data":{"id":"profile-%d"}}`, p.profileSeq))
	case "list_app_variable_groups":
		if p.groupID == "" {
			data = json.RawMessage(`{"groups":[]}`)
		} else {
			data = json.RawMessage(fmt.Sprintf(`{"groups":[{"_id":%q,"name":"apteva-ios-1-production"}]}`, p.groupID))
		}
	case "create_app_variable_group":
		p.groupID = "group-1"
		data = json.RawMessage(`{"_id":"group-1","name":"apteva-ios-1-production"}`)
	case "list_group_variables":
		items := make([]map[string]string, 0, len(p.variables))
		for name, id := range p.variables {
			items = append(items, map[string]string{"_id": id, "name": name})
		}
		data, _ = json.Marshal(map[string]any{"variables": items})
	case "import_group_variables":
		if p.variables == nil {
			p.variables = map[string]string{}
		}
		for _, variable := range input["variables"].([]map[string]any) {
			name := variable["name"].(string)
			p.variables[name] = "variable-" + name
		}
		data = json.RawMessage(`{}`)
	default:
		data = json.RawMessage(`{}`)
	}
	return &sdk.ExecuteResult{Success: true, Status: http.StatusOK, Data: data}, nil
}

func newIOSSigningDeployment(t *testing.T, platform *mobileSigningPlatform) (*sdk.AppCtx, *Deployment) {
	t.Helper()
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"), tk.WithPlatform(platform))
	oldGlobal := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = oldGlobal })
	d, err := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{
		Name: "ios-app", TargetKind: "ios", SourceKind: "local", SourceRef: "/src",
		Framework: "ios", BuildBackend: "codemagic",
		BuildBackendJSON: `{"app_id":"runner-app","workflow_id":"ios","branch":"main","source_mode":"bundle"}`,
		TargetConfigJSON: `{"bundle_id":"com.example.ios","scheme":"Example"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	env, err := dbGetEnvironmentByName(ctx.AppDB(), d.ID, defaultEnvironmentName)
	if err != nil || env == nil {
		t.Fatalf("production environment: %v", err)
	}
	return ctx, effectiveDeploymentForEnvironment(d, env)
}

func TestMobileSigningSetupProvisionsAppleAndCodemagic(t *testing.T) {
	platform := &mobileSigningPlatform{appExists: true}
	ctx, d := newIOSSigningDeployment(t, platform)
	app := &App{}

	result, err := app.setupMobileSigning(t.Context(), d, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Ready || result.Setup.Status != "ready" {
		t.Fatalf("result=%+v", result)
	}
	if result.Setup.AppleCertificateID != "certificate-1" || result.Setup.AppleProfileID != "profile-1" {
		t.Fatalf("setup=%+v", result.Setup)
	}
	if len(result.Setup.KeyFingerprint) != 64 {
		t.Fatalf("fingerprint=%q", result.Setup.KeyFingerprint)
	}
	var providerConfig map[string]any
	if err := json.Unmarshal([]byte(result.Setup.ProviderConfigJSON), &providerConfig); err != nil {
		t.Fatal(err)
	}
	if providerConfig["app_id"] != "runner-app" {
		t.Fatalf("provider config=%v", providerConfig)
	}
	if len(platform.variables) < 6 {
		t.Fatalf("variables=%v", platform.variables)
	}
	importCall := firstSigningCall(platform.calls, "import_group_variables")
	if importCall == nil || importCall.Input["secure"] != true {
		t.Fatalf("secure import call=%#v", importCall)
	}

	env, _ := dbGetEnvironment(ctx.AppDB(), d.EnvironmentID)
	cfg, err := parseCloudBuildConfig(env.BuildBackend, env.BuildBackendJSON)
	if err != nil {
		t.Fatal(err)
	}
	if len(cfg.Groups) != 1 || cfg.Groups[0] != "apteva-ios-1-production" {
		t.Fatalf("groups=%v", cfg.Groups)
	}
	target, _ := parseMobileTargetConfig(env.TargetConfigJSON)
	if target.AppStoreAppID != "app-store-1" {
		t.Fatalf("app store id=%q", target.AppStoreAppID)
	}
	persisted, _ := json.Marshal(struct {
		Setup *MobileSigningSetup
		Env   *DeploymentEnvironment
	}{result.Setup, env})
	for _, secret := range []string{"issuer-secret", "key-secret", "api-secret", "RSA PRIVATE KEY"} {
		if strings.Contains(string(persisted), secret) {
			t.Fatalf("secret %q persisted: %s", secret, persisted)
		}
	}

	callCount := len(platform.calls)
	second, err := app.setupMobileSigning(t.Context(), d, "", false)
	if err != nil || !second.Ready {
		t.Fatalf("idempotent result=%+v err=%v", second, err)
	}
	if len(platform.calls) != callCount {
		t.Fatalf("idempotent setup made %d additional integration calls", len(platform.calls)-callCount)
	}
}

func TestMobileSigningSetupReprovisionsWhenProviderAppChanges(t *testing.T) {
	platform := &mobileSigningPlatform{appExists: true}
	_, d := newIOSSigningDeployment(t, platform)
	app := &App{}

	first, err := app.setupMobileSigning(t.Context(), d, "", false)
	if err != nil {
		t.Fatal(err)
	}
	d.BuildBackendJSON = `{"app_id":"new-runner-app","workflow_id":"ios","branch":"main","source_mode":"bundle"}`
	second, err := app.setupMobileSigning(t.Context(), d, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if second.Setup.AppleCertificateID == first.Setup.AppleCertificateID {
		t.Fatalf("provider app change reused certificate: first=%+v second=%+v", first.Setup, second.Setup)
	}
	var providerConfig map[string]any
	if err := json.Unmarshal([]byte(second.Setup.ProviderConfigJSON), &providerConfig); err != nil {
		t.Fatal(err)
	}
	if providerConfig["app_id"] != "new-runner-app" {
		t.Fatalf("provider config=%v", providerConfig)
	}
}

func TestMobileSigningSetupPausesForManualAppRecordThenResumes(t *testing.T) {
	platform := &mobileSigningPlatform{appExists: false}
	_, d := newIOSSigningDeployment(t, platform)
	app := &App{}

	result, err := app.setupMobileSigning(t.Context(), d, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if result.Ready || result.Setup.Status != "action_required" || len(result.ManualActions) == 0 {
		t.Fatalf("result=%+v", result)
	}
	if platform.bundleID == "" || platform.certificateSeq != 0 {
		t.Fatalf("bundle=%q certificates=%d", platform.bundleID, platform.certificateSeq)
	}

	platform.appExists = true
	resumed, err := app.setupMobileSigning(t.Context(), d, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !resumed.Ready || resumed.Setup.AppleBundleResourceID != platform.bundleID {
		t.Fatalf("resumed=%+v", resumed)
	}
	if signingCallCount(platform.calls, "register_bundle_id") != 1 {
		t.Fatalf("bundle registered more than once: calls=%v", platform.calls)
	}
}

func TestMobileSigningRotationReplacesThenCleansOldAppleResources(t *testing.T) {
	platform := &mobileSigningPlatform{appExists: true}
	_, d := newIOSSigningDeployment(t, platform)
	app := &App{}
	first, err := app.setupMobileSigning(t.Context(), d, "", false)
	if err != nil {
		t.Fatal(err)
	}

	rotated, err := app.setupMobileSigning(t.Context(), d, "", true)
	if err != nil {
		t.Fatal(err)
	}
	if rotated.Setup.AppleCertificateID == first.Setup.AppleCertificateID ||
		rotated.Setup.AppleProfileID == first.Setup.AppleProfileID {
		t.Fatalf("resources not rotated: first=%+v rotated=%+v", first.Setup, rotated.Setup)
	}
	deleteProfile := lastSigningCall(platform.calls, "delete_profile")
	revokeCertificate := lastSigningCall(platform.calls, "revoke_certificate")
	if deleteProfile == nil || deleteProfile.Input["profile_id"] != first.Setup.AppleProfileID {
		t.Fatalf("delete profile=%#v", deleteProfile)
	}
	if revokeCertificate == nil || revokeCertificate.Input["certificate_id"] != first.Setup.AppleCertificateID {
		t.Fatalf("revoke certificate=%#v", revokeCertificate)
	}
	if signingCallCount(platform.calls, "update_group_variable") < 6 {
		t.Fatalf("rotation did not update existing provider variables: calls=%v", platform.calls)
	}
	profileCalls := signingCalls(platform.calls, "create_profile")
	if len(profileCalls) != 2 || profileCalls[0].Input["name"] == profileCalls[1].Input["name"] {
		t.Fatalf("rotation reused profile name: calls=%v", profileCalls)
	}
}

func TestMobileSigningProviderFailureCleansNewAppleResources(t *testing.T) {
	platform := &mobileSigningPlatform{appExists: true, failTool: "import_group_variables"}
	_, d := newIOSSigningDeployment(t, platform)
	result, err := (&App{}).setupMobileSigning(t.Context(), d, "", false)
	if err == nil || result != nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	setup, getErr := dbGetMobileSigningSetup(globalCtx.AppDB(), d.ID, d.EnvironmentID, "codemagic")
	if getErr != nil {
		t.Fatal(getErr)
	}
	if setup.Status != "failed" || setup.AppleCertificateID != "" || setup.AppleProfileID != "" {
		t.Fatalf("setup=%+v", setup)
	}
	if signingCallCount(platform.calls, "delete_profile") != 1 ||
		signingCallCount(platform.calls, "revoke_certificate") != 1 {
		t.Fatalf("cleanup calls=%v", platform.calls)
	}
}

func TestMobileSigningRotationFailureKeepsPreviousReadySetup(t *testing.T) {
	platform := &mobileSigningPlatform{appExists: true}
	_, d := newIOSSigningDeployment(t, platform)
	app := &App{}
	first, err := app.setupMobileSigning(t.Context(), d, "", false)
	if err != nil {
		t.Fatal(err)
	}

	platform.failTool = "update_group_variable"
	result, err := app.setupMobileSigning(t.Context(), d, "", true)
	if err == nil || result != nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	setup, getErr := dbGetMobileSigningSetup(globalCtx.AppDB(), d.ID, d.EnvironmentID, "codemagic")
	if getErr != nil {
		t.Fatal(getErr)
	}
	if setup.Status != "ready" ||
		setup.AppleCertificateID != first.Setup.AppleCertificateID ||
		setup.AppleProfileID != first.Setup.AppleProfileID ||
		!strings.Contains(setup.LastError, "last replacement failed") {
		t.Fatalf("setup=%+v", setup)
	}
	if signingCallCount(platform.calls, "delete_profile") != 1 ||
		signingCallCount(platform.calls, "revoke_certificate") != 1 {
		t.Fatalf("replacement cleanup calls=%v", platform.calls)
	}
}

func firstSigningCall(calls []integrationCall, tool string) *integrationCall {
	for i := range calls {
		if calls[i].Tool == tool {
			return &calls[i]
		}
	}
	return nil
}

func lastSigningCall(calls []integrationCall, tool string) *integrationCall {
	for i := len(calls) - 1; i >= 0; i-- {
		if calls[i].Tool == tool {
			return &calls[i]
		}
	}
	return nil
}

func signingCallCount(calls []integrationCall, tool string) int {
	count := 0
	for _, call := range calls {
		if call.Tool == tool {
			count++
		}
	}
	return count
}

func signingCalls(calls []integrationCall, tool string) []integrationCall {
	var out []integrationCall
	for _, call := range calls {
		if call.Tool == tool {
			out = append(out, call)
		}
	}
	return out
}
