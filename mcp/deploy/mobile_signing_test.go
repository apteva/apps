package main

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"math/big"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"howett.net/plist"
)

func TestGeneratedAndroidSigningIdentityIsJavaCompatible(t *testing.T) {
	input, payload, err := generateAndroidSigningIdentity("p1", "com.example.android")
	if err != nil {
		t.Fatal(err)
	}
	if input.CertificateSHA1 == "" || input.CertificateSHA256 == "" || input.ExpiresAt == "" {
		t.Fatalf("identity metadata incomplete: %#v", input)
	}
	if !strings.HasPrefix(payload.KeyAlias, generatedAndroidKeyAliasPrefix) ||
		payload.KeyPassword == payload.StorePassword {
		t.Fatalf("generated alias/passwords are not independent: alias=%q", payload.KeyAlias)
	}
	pfx, err := base64.StdEncoding.DecodeString(payload.KeystoreBase64)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := androidSigningIdentityFromPKCS12("p1", "com.example.android", pfx, payload.StorePassword, payload.KeyPassword, payload.KeyAlias); err != nil {
		t.Fatalf("generated keystore cannot be imported: %v", err)
	}
	keytool, err := exec.LookPath("keytool")
	if err != nil {
		t.Skip("keytool is unavailable")
	}
	path := filepath.Join(t.TempDir(), "upload.p12")
	if err := os.WriteFile(path, pfx, 0o600); err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(keytool, "-list", "-storetype", "PKCS12", "-keystore", path,
		"-storepass", payload.StorePassword, "-alias", payload.KeyAlias).CombinedOutput()
	if err != nil {
		t.Fatalf("keytool rejected generated keystore alias %q: %v\n%s", payload.KeyAlias, err, output)
	}
}

func TestAndroidMobileSigningSetupProvisionsCodemagic(t *testing.T) {
	platform := &mobileSigningPlatform{}
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"), tk.WithPlatform(platform))
	oldGlobal := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = oldGlobal })
	d, err := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{
		Name: "android-app", TargetKind: "android", SourceKind: "local", SourceRef: t.TempDir(),
		Framework: "android", BuildBackend: "codemagic",
		BuildBackendJSON: `{"app_id":"runner-app","workflow_id":"android","branch":"main","source_mode":"bundle"}`,
		TargetConfigJSON: `{"package_name":"com.example.android"}`,
	})
	if err != nil {
		t.Fatal(err)
	}
	env, err := dbGetEnvironmentByName(ctx.AppDB(), d.ID, defaultEnvironmentName)
	if err != nil {
		t.Fatal(err)
	}
	d = effectiveDeploymentForEnvironment(d, env)

	app := &App{dataDir: t.TempDir()}
	result, err := app.setupMobileSigning(t.Context(), d, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Ready || result.Identity == nil || result.Setup.IdentityID != result.Identity.ID ||
		result.Setup.PreparedRevision != result.Identity.Revision {
		t.Fatalf("result=%+v", result)
	}
	if result.Identity.ApplicationIdentifier != "com.example.android" || result.Identity.CertificateSHA256 == "" {
		t.Fatalf("identity=%+v", result.Identity)
	}
	call := firstSigningCall(platform.calls, "import_group_variables")
	if call == nil {
		t.Fatalf("Codemagic variables were not provisioned: calls=%v", platform.calls)
	}
	names := map[string]bool{}
	for _, variable := range call.Input["variables"].([]map[string]any) {
		names[variable["name"].(string)] = true
	}
	for _, required := range []string{
		"ANDROID_UPLOAD_KEYSTORE_BASE64", "ANDROID_UPLOAD_KEY_ALIAS",
		"ANDROID_UPLOAD_STORE_PASSWORD", "ANDROID_UPLOAD_KEY_PASSWORD", "ANDROID_UPLOAD_CERT_SHA256",
	} {
		if !names[required] {
			t.Errorf("missing secure Codemagic variable %s", required)
		}
	}

	providerCalls := len(platform.calls)
	second, err := app.setupMobileSigning(t.Context(), d, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if second.Identity.ID != result.Identity.ID || second.Identity.Revision != result.Identity.Revision {
		t.Fatalf("idempotent setup changed identity: first=%+v second=%+v", result.Identity, second.Identity)
	}
	if len(platform.calls) != providerCalls {
		t.Fatalf("idempotent setup rewrote provider secrets: before=%d after=%d", providerCalls, len(platform.calls))
	}
}

type mobileSigningPlatform struct {
	tk.BasePlatformClient
	calls          []integrationCall
	appExists      bool
	bundleID       string
	groupID        string
	groupName      string
	variables      map[string]string
	certificateSeq int
	profileSeq     int
	failTool       string
	capabilities   map[string]string
	certificates   map[string]bool
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
	case "list_bundle_id_capabilities":
		items := make([]map[string]any, 0, len(p.capabilities))
		for capabilityType, id := range p.capabilities {
			items = append(items, map[string]any{
				"id": id, "attributes": map[string]any{"capabilityType": capabilityType},
			})
		}
		data, _ = json.Marshal(map[string]any{"data": items})
	case "enable_bundle_id_capability":
		if p.capabilities == nil {
			p.capabilities = map[string]string{}
		}
		capabilityType := input["capabilityType"].(string)
		id := "capability-" + strings.ToLower(capabilityType)
		p.capabilities[capabilityType] = id
		data = json.RawMessage(fmt.Sprintf(
			`{"data":{"id":%q,"attributes":{"capabilityType":%q}}}`,
			id, capabilityType,
		))
	case "create_certificate":
		p.certificateSeq++
		id := fmt.Sprintf("certificate-%d", p.certificateSeq)
		if p.certificates == nil {
			p.certificates = map[string]bool{}
		}
		p.certificates[id] = true
		csrBlock, _ := pem.Decode([]byte(input["csrContent"].(string)))
		csr, _ := x509.ParseCertificateRequest(csrBlock.Bytes)
		caKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		now := time.Now().UTC()
		certificateDER, _ := x509.CreateCertificate(rand.Reader, &x509.Certificate{
			SerialNumber: big.NewInt(int64(p.certificateSeq)), Subject: pkix.Name{CommonName: "Apple Distribution"},
			NotBefore: now.Add(-time.Minute), NotAfter: now.AddDate(1, 0, 0), KeyUsage: x509.KeyUsageDigitalSignature,
		}, &x509.Certificate{
			SerialNumber: big.NewInt(100), Subject: pkix.Name{CommonName: "Test Apple CA"},
			NotBefore: now.Add(-time.Hour), NotAfter: now.AddDate(2, 0, 0), IsCA: true,
			KeyUsage: x509.KeyUsageCertSign, PublicKey: &caKey.PublicKey,
		}, csr.PublicKey, caKey)
		data = json.RawMessage(fmt.Sprintf(`{"data":{"id":%q,"attributes":{"certificateContent":%q}}}`,
			id, base64.StdEncoding.EncodeToString(certificateDER)))
	case "list_certificates":
		items := make([]map[string]any, 0, len(p.certificates))
		for id, available := range p.certificates {
			if available {
				items = append(items, map[string]any{"id": id, "attributes": map[string]any{}})
			}
		}
		data, _ = json.Marshal(map[string]any{"data": items})
	case "revoke_certificate":
		if id, _ := input["certificate_id"].(string); id != "" {
			delete(p.certificates, id)
		}
		data = json.RawMessage(`{}`)
	case "create_profile":
		p.profileSeq++
		data = json.RawMessage(fmt.Sprintf(`{"data":{"id":"profile-%d"}}`, p.profileSeq))
	case "get_profile":
		data = json.RawMessage(`{"data":{"id":"profile","attributes":{"profileContent":"cHJvZmlsZQ=="}}}`)
	case "list_app_variable_groups":
		if p.groupID == "" {
			data = json.RawMessage(`{"groups":[]}`)
		} else {
			data = json.RawMessage(fmt.Sprintf(`{"groups":[{"_id":%q,"name":%q}]}`, p.groupID, p.groupName))
		}
	case "create_app_variable_group":
		p.groupID = "group-1"
		p.groupName = input["name"].(string)
		data = json.RawMessage(fmt.Sprintf(`{"_id":"group-1","name":%q}`, p.groupName))
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
	t.Setenv(mobileSigningVaultKeyFileEnv, filepath.Join(t.TempDir(), "vault-key"))
	ctx := tk.NewAppCtx(t, "apteva.yaml", tk.WithProjectID("p1"), tk.WithPlatform(platform))
	oldGlobal := globalCtx
	globalCtx = ctx
	t.Cleanup(func() { globalCtx = oldGlobal })
	sourceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(sourceDir, "project.yml"), []byte(`
settings:
  base:
    CODE_SIGN_ENTITLEMENTS: App/App.entitlements
`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(sourceDir, "App"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sourceDir, "App", "App.entitlements"), []byte(`
<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0"><dict><key>aps-environment</key><string>$(APNS_ENVIRONMENT)</string></dict></plist>
`), 0o644); err != nil {
		t.Fatal(err)
	}
	d, err := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{
		Name: "ios-app", TargetKind: "ios", SourceKind: "local", SourceRef: sourceDir,
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
	if result.Identity == nil || result.Identity.CertificatePEM == "" ||
		result.Identity.CertificateSHA256 == "" || result.Identity.ExpiresAt == "" {
		t.Fatalf("durable iOS certificate metadata is incomplete: identity=%+v", result.Identity)
	}
	credentials, err := app.iosSigningCredentials(d)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"certificate_private_key", "certificate_pem", "provisioning_profile_base64", "certificate_sha256"} {
		if credentials[field] == "" {
			t.Errorf("generic iOS runner credential %s is missing", field)
		}
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
	enableCall := firstSigningCall(platform.calls, "enable_bundle_id_capability")
	profileCall := firstSigningCall(platform.calls, "create_profile")
	listCapabilitiesCall := firstSigningCall(platform.calls, "list_bundle_id_capabilities")
	if listCapabilitiesCall == nil ||
		listCapabilitiesCall.Input["fields[bundleIdCapabilities]"] != "capabilityType,settings" {
		t.Fatalf("capability list call=%#v", listCapabilitiesCall)
	}
	if _, ok := listCapabilitiesCall.Input["limit"]; ok {
		t.Fatalf("capability relationship request included unsupported limit: %#v", listCapabilitiesCall.Input)
	}
	if enableCall == nil || profileCall == nil ||
		signingCallIndex(platform.calls, "enable_bundle_id_capability") > signingCallIndex(platform.calls, "create_profile") {
		t.Fatalf("capability must be enabled before profile creation: calls=%v", platform.calls)
	}
	if result.Setup.RequirementsHash == "" ||
		!mobileFeaturesContainAll(mobileFeaturesFromJSON(result.Setup.ProvisionedFeaturesJSON), []string{mobileFeatureIOSPushNotifications}) {
		t.Fatalf("requirements not persisted: setup=%+v", result.Setup)
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
	if len(cfg.Groups) != 1 || !strings.HasPrefix(cfg.Groups[0], "apteva-ios-signing-") {
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

	certificateCount := platform.certificateSeq
	profileCount := platform.profileSeq
	second, err := app.setupMobileSigning(t.Context(), d, "", false)
	if err != nil || !second.Ready {
		t.Fatalf("idempotent result=%+v err=%v", second, err)
	}
	if platform.certificateSeq != certificateCount || platform.profileSeq != profileCount {
		t.Fatalf("idempotent setup mutated signing resources: certificates=%d profiles=%d", platform.certificateSeq, platform.profileSeq)
	}
	if signingCallCount(platform.calls, "enable_bundle_id_capability") != 1 ||
		signingCallCount(platform.calls, "disable_bundle_id_capability") != 0 {
		t.Fatalf("idempotent setup mutated Apple capabilities: calls=%v", platform.calls)
	}
}

func TestMobileSigningRepairsProfileWhenSourceRequirementsChange(t *testing.T) {
	platform := &mobileSigningPlatform{appExists: true}
	_, d := newIOSSigningDeployment(t, platform)
	entitlements := filepath.Join(d.SourceRef, "App", "App.entitlements")
	if err := os.WriteFile(entitlements, []byte(`<?xml version="1.0"?><plist version="1.0"><dict/></plist>`), 0o644); err != nil {
		t.Fatal(err)
	}
	app := &App{}
	first, err := app.setupMobileSigning(t.Context(), d, "", false)
	if err != nil {
		t.Fatal(err)
	}
	writeIOSPlist(t, entitlements, plist.XMLFormat, map[string]any{
		"aps-environment": "$(APNS_ENVIRONMENT)",
	})
	providerWrites := signingCallCount(platform.calls, "import_group_variables") +
		signingCallCount(platform.calls, "update_group_variable")
	repaired, err := app.setupMobileSigning(t.Context(), d, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if repaired.Setup.AppleCertificateID != first.Setup.AppleCertificateID ||
		repaired.Setup.AppleProfileID == first.Setup.AppleProfileID {
		t.Fatalf("expected profile-only repair: first=%+v repaired=%+v", first.Setup, repaired.Setup)
	}
	if platform.certificateSeq != 1 {
		t.Fatalf("repair created certificate: %d", platform.certificateSeq)
	}
	if got := signingCallCount(platform.calls, "import_group_variables") +
		signingCallCount(platform.calls, "update_group_variable"); got != providerWrites {
		t.Fatalf("profile repair rewrote provider secrets: before=%d after=%d", providerWrites, got)
	}
	deleted := lastSigningCall(platform.calls, "delete_profile")
	if deleted == nil || deleted.Input["profile_id"] != first.Setup.AppleProfileID {
		t.Fatalf("old profile cleanup=%#v", deleted)
	}
	if signingCallIndex(platform.calls, "delete_profile") <
		lastSigningCallIndex(platform.calls, "create_profile") {
		t.Fatalf("old profile deleted before replacement creation: calls=%v", platform.calls)
	}
}

func TestMobileSigningRepairFailurePreservesReadySetup(t *testing.T) {
	platform := &mobileSigningPlatform{appExists: true}
	_, d := newIOSSigningDeployment(t, platform)
	entitlements := filepath.Join(d.SourceRef, "App", "App.entitlements")
	if err := os.WriteFile(entitlements, []byte(`<?xml version="1.0"?><plist version="1.0"><dict/></plist>`), 0o644); err != nil {
		t.Fatal(err)
	}
	app := &App{}
	first, err := app.setupMobileSigning(t.Context(), d, "", false)
	if err != nil {
		t.Fatal(err)
	}
	writeIOSPlist(t, entitlements, plist.XMLFormat, map[string]any{
		"aps-environment": "production",
	})
	platform.failTool = "create_profile"
	if result, err := app.setupMobileSigning(t.Context(), d, "", false); err == nil || result != nil {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	stored, err := dbGetMobileSigningSetup(globalCtx.AppDB(), d.ID, d.EnvironmentID, "codemagic")
	if err != nil {
		t.Fatal(err)
	}
	if stored.Status != mobileSigningStatusReady ||
		stored.AppleProfileID != first.Setup.AppleProfileID ||
		stored.AppleCertificateID != first.Setup.AppleCertificateID {
		t.Fatalf("stored=%+v", stored)
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
	if second.Setup.AppleCertificateID != first.Setup.AppleCertificateID ||
		second.Setup.IdentityID != first.Setup.IdentityID ||
		second.Setup.PreparedRevision != first.Setup.PreparedRevision {
		t.Fatalf("provider app change replaced durable identity: first=%+v second=%+v", first.Setup, second.Setup)
	}
	var providerConfig map[string]any
	if err := json.Unmarshal([]byte(second.Setup.ProviderConfigJSON), &providerConfig); err != nil {
		t.Fatal(err)
	}
	if providerConfig["app_id"] != "new-runner-app" {
		t.Fatalf("provider config=%v", providerConfig)
	}
}

func TestIOSSigningIdentitySurvivesDeploymentDeletion(t *testing.T) {
	platform := &mobileSigningPlatform{appExists: true}
	ctx, firstDeployment := newIOSSigningDeployment(t, platform)
	app := &App{}
	first, err := app.setupMobileSigning(t.Context(), firstDeployment, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if first.Identity == nil {
		t.Fatal("first setup did not create a durable identity")
	}
	if err := dbDeleteDeployment(ctx.AppDB(), "p1", firstDeployment.ID); err != nil {
		t.Fatal(err)
	}
	identity, err := dbGetMobileSigningIdentity(ctx.AppDB(), "p1", "ios", "issuer-secret", "com.example.ios")
	if err != nil || identity == nil || identity.ID != first.Identity.ID {
		t.Fatalf("identity did not survive deployment deletion: identity=%+v err=%v", identity, err)
	}
	recreated, err := dbCreateDeployment(ctx.AppDB(), "p1", CreateDeploymentInput{
		Name: "ios-app-recreated", TargetKind: "ios", SourceKind: "local", SourceRef: firstDeployment.SourceRef,
		Framework: "ios", BuildBackend: "codemagic",
		BuildBackendJSON: firstDeployment.BuildBackendJSON, TargetConfigJSON: firstDeployment.TargetConfigJSON,
	})
	if err != nil {
		t.Fatal(err)
	}
	env, err := dbGetEnvironmentByName(ctx.AppDB(), recreated.ID, defaultEnvironmentName)
	if err != nil {
		t.Fatal(err)
	}
	second, err := app.setupMobileSigning(t.Context(), effectiveDeploymentForEnvironment(recreated, env), "", false)
	if err != nil {
		t.Fatal(err)
	}
	if second.Identity == nil || second.Identity.ID != first.Identity.ID ||
		second.Setup.AppleCertificateID != first.Setup.AppleCertificateID || platform.certificateSeq != 1 {
		t.Fatalf("recreated deployment replaced identity: first=%+v second=%+v certificates=%d",
			first, second, platform.certificateSeq)
	}
}

func TestLegacyIOSSigningSetupIsNotRotatedImplicitly(t *testing.T) {
	platform := &mobileSigningPlatform{appExists: true}
	ctx, d := newIOSSigningDeployment(t, platform)
	app := &App{}
	first, err := app.setupMobileSigning(t.Context(), d, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`DELETE FROM mobile_signing_identities`); err != nil {
		t.Fatal(err)
	}
	if _, err := ctx.AppDB().Exec(`UPDATE mobile_signing_setups SET identity_id=0, prepared_revision=0 WHERE id=?`, first.Setup.ID); err != nil {
		t.Fatal(err)
	}
	certificateCount := platform.certificateSeq
	result, err := app.setupMobileSigning(t.Context(), d, "", false)
	if err != nil {
		t.Fatal(err)
	}
	if !result.Ready || result.Identity != nil || len(result.ManualActions) == 0 || platform.certificateSeq != certificateCount {
		t.Fatalf("legacy setup rotated unexpectedly: result=%+v certificates=%d", result, platform.certificateSeq)
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

func signingCallIndex(calls []integrationCall, tool string) int {
	for i, call := range calls {
		if call.Tool == tool {
			return i
		}
	}
	return -1
}

func lastSigningCallIndex(calls []integrationCall, tool string) int {
	for i := len(calls) - 1; i >= 0; i-- {
		if calls[i].Tool == tool {
			return i
		}
	}
	return -1
}
