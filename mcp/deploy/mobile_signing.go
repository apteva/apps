package main

import (
	"context"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

const (
	mobileSigningStatusActionRequired = "action_required"
	mobileSigningStatusReady          = "ready"
)

type mobileSigningSecrets struct {
	AppStoreIssuerID      string
	AppStoreKeyID         string
	AppStorePrivateKey    string
	CertificatePrivateKey string
	BundleID              string
	AppStoreAppID         string
	Scheme                string
	WorkspacePath         string
	ProjectPath           string
}

type mobileSigningProviderResult struct {
	SecretRef  string
	ConfigJSON string
	Groups     []string
}

// mobileSigningProvider isolates build-provider secret storage from Apple
// provisioning. New cloud providers implement this interface without changing
// the Apple certificate/profile lifecycle.
type mobileSigningProvider interface {
	Name() string
	ProvisionSigningSecrets(context.Context, *sdk.BoundIntegration, cloudBuildConfig, *Deployment, mobileSigningSecrets) (*mobileSigningProviderResult, error)
}

func mobileSigningProviderFor(name string) (mobileSigningProvider, error) {
	switch normalizeBuildBackend(name) {
	case buildBackendCodemagic:
		return codemagicSigningProvider{}, nil
	default:
		return nil, fmt.Errorf(
			"build provider %q does not expose a Deploy signing-secret adapter; configure signing in that provider or add an adapter implementing mobileSigningProvider",
			name,
		)
	}
}

type mobileSigningSetupResult struct {
	Setup         *MobileSigningSetup `json:"setup"`
	Ready         bool                `json:"ready"`
	ManualActions []string            `json:"manual_actions,omitempty"`
}

func (a *App) setupMobileSigning(ctx context.Context, d *Deployment, providerName string, rotate bool) (*mobileSigningSetupResult, error) {
	a.mobileSigningMu.Lock()
	defer a.mobileSigningMu.Unlock()

	if d == nil {
		return nil, errors.New("deployment required")
	}
	if d.TargetKind != "ios" {
		return nil, errors.New("automatic signing setup currently supports iOS deployments")
	}
	target, err := parseMobileTargetConfig(d.TargetConfigJSON)
	if err != nil {
		return nil, err
	}
	target.BundleID = strings.TrimSpace(target.BundleID)
	if target.BundleID == "" {
		return nil, errors.New("target_config_json.bundle_id is required")
	}
	if strings.TrimSpace(providerName) == "" {
		providerName = d.BuildBackend
	}
	providerName = normalizeBuildBackend(providerName)
	if providerName != normalizeBuildBackend(d.BuildBackend) {
		return nil, fmt.Errorf("provider %q is not this environment's selected build_backend %q", providerName, d.BuildBackend)
	}
	cfg, err := parseCloudBuildConfig(providerName, d.BuildBackendJSON)
	if err != nil {
		return nil, err
	}
	provider, err := mobileSigningProviderFor(providerName)
	if err != nil {
		return nil, err
	}
	providerBound, err := cloudIntegrationFor(providerName)
	if err != nil {
		return nil, err
	}
	appleBound, err := boundIntegration("app_store")
	if err != nil {
		return nil, err
	}
	appleCreds, err := globalCtx.PlatformAPI().GetConnectionCredentials(appleBound.ConnectionID)
	if err != nil {
		return nil, fmt.Errorf("read app_store credentials: %w", err)
	}
	issuerID := strings.TrimSpace(appleCreds.Fields["issuer_id"])
	keyID := strings.TrimSpace(appleCreds.Fields["key_id"])
	apiPrivateKey := strings.TrimSpace(appleCreds.Fields["private_key"])
	if issuerID == "" || keyID == "" || apiPrivateKey == "" {
		return nil, errors.New("app_store integration requires issuer_id, key_id, and private_key")
	}

	existing, err := dbGetMobileSigningSetup(globalCtx.AppDB(), d.ID, d.EnvironmentID, providerName)
	if err != nil {
		return nil, err
	}
	if existing != nil && existing.Status == mobileSigningStatusReady && existing.BundleID != target.BundleID {
		return nil, fmt.Errorf(
			"bundle_id is immutable after signing is configured (%s); create a new deployment for %s",
			existing.BundleID, target.BundleID,
		)
	}
	if existing != nil && existing.Status == mobileSigningStatusReady &&
		existing.ProviderConnectionID == providerBound.ConnectionID && !rotate {
		return &mobileSigningSetupResult{Setup: existing, Ready: true}, nil
	}
	previous := existing
	setup := &MobileSigningSetup{
		DeploymentID: d.ID, EnvironmentID: d.EnvironmentID, Platform: "ios",
		Provider: providerName, ProviderConnectionID: providerBound.ConnectionID,
		BundleID: target.BundleID, Status: "provisioning",
	}
	if previous != nil {
		setup.AppleBundleResourceID = previous.AppleBundleResourceID
		setup.AppStoreAppID = previous.AppStoreAppID
		setup.AppleCertificateID = previous.AppleCertificateID
		setup.AppleProfileID = previous.AppleProfileID
		setup.ProviderSecretRef = previous.ProviderSecretRef
		setup.ProviderConfigJSON = previous.ProviderConfigJSON
		setup.KeyFingerprint = previous.KeyFingerprint
	}
	setup, err = dbUpsertMobileSigningSetup(globalCtx.AppDB(), setup)
	if err != nil {
		return nil, err
	}
	failSetup := func(cause error) error {
		if previous != nil && previous.Status == mobileSigningStatusReady {
			restored := *previous
			restored.LastError = "last replacement failed: " + cause.Error()
			_, _ = dbUpsertMobileSigningSetup(globalCtx.AppDB(), &restored)
			emit("deploy.mobile_signing.failed", map[string]any{
				"deployment_id": restored.DeploymentID, "environment_id": restored.EnvironmentID,
				"provider": restored.Provider, "error": cause.Error(),
			})
			return cause
		}
		return a.failMobileSigningSetup(setup, cause)
	}

	bundleResourceID := setup.AppleBundleResourceID
	if bundleResourceID == "" {
		listed, callErr := executeIntegration(appleBound, "list_bundle_ids", map[string]any{
			"identifier": target.BundleID, "platform": "IOS", "limit": 2,
		})
		if callErr != nil {
			return nil, failSetup(callErr)
		}
		bundleResourceID = firstJSONAPIID(listed)
	}
	if bundleResourceID == "" {
		created, callErr := executeIntegration(appleBound, "register_bundle_id", map[string]any{
			"identifier": target.BundleID,
			"name":       appleResourceName(d),
			"platform":   "IOS",
		})
		if callErr != nil {
			return nil, failSetup(callErr)
		}
		bundleResourceID = jsonStringAt(created, "data", "id")
		if bundleResourceID == "" {
			return nil, failSetup(errors.New("Apple register_bundle_id returned no resource id"))
		}
	}
	setup.AppleBundleResourceID = bundleResourceID

	apps, callErr := executeIntegration(appleBound, "list_apps", map[string]any{
		"bundle_id": target.BundleID, "platform": "IOS", "limit": 2,
	})
	if callErr != nil {
		return nil, failSetup(callErr)
	}
	appStoreAppID := firstJSONAPIID(apps)
	if appStoreAppID == "" {
		if previous != nil && previous.Status == mobileSigningStatusReady {
			return nil, failSetup(fmt.Errorf("App Store Connect app record for bundle ID %s is no longer visible", target.BundleID))
		}
		setup.Status = mobileSigningStatusActionRequired
		setup.LastError = "Create the app record in App Store Connect for bundle ID " + target.BundleID + ", then run setup again."
		setup, err = dbUpsertMobileSigningSetup(globalCtx.AppDB(), setup)
		if err != nil {
			return nil, err
		}
		return &mobileSigningSetupResult{
			Setup: setup, Ready: false,
			ManualActions: []string{
				"Accept any pending Apple Developer or App Store Connect agreements.",
				"Create the app record in App Store Connect using bundle ID " + target.BundleID + ".",
				"Run mobile signing setup again.",
			},
		}, nil
	}
	setup.AppStoreAppID = appStoreAppID

	certificatePrivateKey, csr, fingerprint, err := generateAppleDistributionKey(d, target.BundleID)
	if err != nil {
		return nil, failSetup(err)
	}
	certificate, err := executeIntegration(appleBound, "create_certificate", map[string]any{
		"certificateType": "IOS_DISTRIBUTION",
		"csrContent":      csr,
	})
	if err != nil {
		return nil, failSetup(err)
	}
	certificateID := jsonStringAt(certificate, "data", "id")
	if certificateID == "" {
		return nil, failSetup(errors.New("Apple create_certificate returned no resource id"))
	}
	createdProfileID := ""
	cleanupNewAppleResources := func() {
		if createdProfileID != "" {
			_, _ = executeIntegration(appleBound, "delete_profile", map[string]any{"profile_id": createdProfileID})
		}
		_, _ = executeIntegration(appleBound, "revoke_certificate", map[string]any{"certificate_id": certificateID})
	}

	profile, err := executeIntegration(appleBound, "create_profile", map[string]any{
		"name":            appleProfileName(d),
		"profileType":     "IOS_APP_STORE",
		"bundle_id":       bundleResourceID,
		"certificate_ids": []string{certificateID},
	})
	if err != nil {
		cleanupNewAppleResources()
		return nil, failSetup(err)
	}
	createdProfileID = jsonStringAt(profile, "data", "id")
	if createdProfileID == "" {
		cleanupNewAppleResources()
		return nil, failSetup(errors.New("Apple create_profile returned no resource id"))
	}

	providerResult, err := provider.ProvisionSigningSecrets(ctx, providerBound, cfg, d, mobileSigningSecrets{
		AppStoreIssuerID: issuerID, AppStoreKeyID: keyID, AppStorePrivateKey: apiPrivateKey,
		CertificatePrivateKey: certificatePrivateKey, BundleID: target.BundleID,
		AppStoreAppID: appStoreAppID, Scheme: target.Scheme,
		WorkspacePath: target.WorkspacePath, ProjectPath: target.ProjectPath,
	})
	if err != nil {
		cleanupNewAppleResources()
		return nil, failSetup(err)
	}
	cfg.Groups = uniqueStrings(append(cfg.Groups, providerResult.Groups...))
	cfgBody, err := json.Marshal(cfg)
	if err != nil {
		cleanupNewAppleResources()
		return nil, failSetup(err)
	}
	target.AppStoreAppID = appStoreAppID
	targetBody, err := json.Marshal(target)
	if err != nil {
		cleanupNewAppleResources()
		return nil, failSetup(err)
	}
	if err := persistEffectiveDeploymentConfig(d, map[string]any{
		"build_backend_config_json": string(cfgBody),
		"target_config_json":        string(targetBody),
	}); err != nil {
		cleanupNewAppleResources()
		return nil, failSetup(err)
	}

	setup.Status = mobileSigningStatusReady
	setup.AppleCertificateID = certificateID
	setup.AppleProfileID = createdProfileID
	setup.ProviderSecretRef = providerResult.SecretRef
	setup.ProviderConfigJSON = defaultStr(providerResult.ConfigJSON, "{}")
	setup.KeyFingerprint = fingerprint
	setup.LastError = ""
	setup, err = dbUpsertMobileSigningSetup(globalCtx.AppDB(), setup)
	if err != nil {
		cleanupNewAppleResources()
		return nil, err
	}

	if previous != nil && previous.Status == mobileSigningStatusReady {
		if previous.AppleProfileID != "" && previous.AppleProfileID != setup.AppleProfileID {
			_, _ = executeIntegration(appleBound, "delete_profile", map[string]any{"profile_id": previous.AppleProfileID})
		}
		if previous.AppleCertificateID != "" && previous.AppleCertificateID != setup.AppleCertificateID {
			_, _ = executeIntegration(appleBound, "revoke_certificate", map[string]any{"certificate_id": previous.AppleCertificateID})
		}
	}
	emit("deploy.mobile_signing.ready", map[string]any{
		"deployment_id": d.ID, "environment_id": d.EnvironmentID,
		"provider": providerName, "bundle_id": target.BundleID,
	})
	return &mobileSigningSetupResult{Setup: setup, Ready: true}, nil
}

func (a *App) failMobileSigningSetup(setup *MobileSigningSetup, cause error) error {
	if setup != nil {
		if setup.AppleCertificateID != "" && setup.AppleProfileID != "" && setup.ProviderSecretRef != "" {
			setup.Status = mobileSigningStatusReady
			setup.LastError = "last rotation failed: " + cause.Error()
		} else {
			setup.Status = "failed"
			setup.LastError = cause.Error()
		}
		_, _ = dbUpsertMobileSigningSetup(globalCtx.AppDB(), setup)
		emit("deploy.mobile_signing.failed", map[string]any{
			"deployment_id": setup.DeploymentID, "environment_id": setup.EnvironmentID,
			"provider": setup.Provider, "error": cause.Error(),
		})
	}
	return cause
}

func persistEffectiveDeploymentConfig(d *Deployment, fields map[string]any) error {
	if d.EnvironmentID > 0 {
		if err := dbUpdateEnvironment(globalCtx.AppDB(), d.EnvironmentID, fields); err != nil {
			return err
		}
		if d.EnvironmentName == defaultEnvironmentName {
			return dbUpdateDeployment(globalCtx.AppDB(), d.ProjectID, d.ID, fields)
		}
		return nil
	}
	return dbUpdateDeployment(globalCtx.AppDB(), d.ProjectID, d.ID, fields)
}

func generateAppleDistributionKey(d *Deployment, bundleID string) (privateKeyPEM, csrPEM, fingerprint string, err error) {
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		return "", "", "", fmt.Errorf("generate distribution key: %w", err)
	}
	keyDER := x509.MarshalPKCS1PrivateKey(key)
	privateKeyPEM = string(pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: keyDER}))
	csrDER, err := x509.CreateCertificateRequest(rand.Reader, &x509.CertificateRequest{
		Subject: pkix.Name{CommonName: appleResourceName(d) + " (" + bundleID + ")"},
	}, key)
	if err != nil {
		return "", "", "", fmt.Errorf("create certificate request: %w", err)
	}
	csrPEM = string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE REQUEST", Bytes: csrDER}))
	publicDER, err := x509.MarshalPKIXPublicKey(&key.PublicKey)
	if err != nil {
		return "", "", "", err
	}
	sum := sha256.Sum256(publicDER)
	return privateKeyPEM, csrPEM, hex.EncodeToString(sum[:]), nil
}

func appleResourceName(d *Deployment) string {
	name := "Apteva"
	if d != nil {
		name += " " + d.Name
		if d.EnvironmentName != "" {
			name += " " + d.EnvironmentName
		}
	}
	return strings.TrimSpace(name)
}

func appleProfileName(d *Deployment) string {
	return appleResourceName(d) + " App Store"
}

type codemagicSigningProvider struct{}

func (codemagicSigningProvider) Name() string { return buildBackendCodemagic }

func (codemagicSigningProvider) ProvisionSigningSecrets(
	_ context.Context,
	bound *sdk.BoundIntegration,
	cfg cloudBuildConfig,
	d *Deployment,
	secrets mobileSigningSecrets,
) (*mobileSigningProviderResult, error) {
	groupName := codemagicSigningGroupName(d)
	groups, err := executeIntegration(bound, "list_app_variable_groups", map[string]any{
		"app_id": cfg.AppID, "page_size": 100, "page": 1,
	})
	if err != nil {
		return nil, err
	}
	groupID := recursiveNamedID(groups, groupName)
	if groupID == "" {
		created, err := executeIntegration(bound, "create_app_variable_group", map[string]any{
			"app_id": cfg.AppID, "name": groupName,
		})
		if err != nil {
			return nil, err
		}
		groupID = firstRecursiveString(created, "_id", "id", "variable_group_id")
		if groupID == "" {
			return nil, errors.New("Codemagic create_app_variable_group returned no group id")
		}
	}

	values := map[string]string{
		"APP_STORE_CONNECT_ISSUER_ID":      secrets.AppStoreIssuerID,
		"APP_STORE_CONNECT_KEY_IDENTIFIER": secrets.AppStoreKeyID,
		"APP_STORE_CONNECT_PRIVATE_KEY":    secrets.AppStorePrivateKey,
		"CERTIFICATE_PRIVATE_KEY":          secrets.CertificatePrivateKey,
		"BUNDLE_ID":                        secrets.BundleID,
		"APP_STORE_ID":                     secrets.AppStoreAppID,
	}
	if secrets.Scheme != "" {
		values["XCODE_SCHEME"] = secrets.Scheme
	}
	if secrets.WorkspacePath != "" {
		values["XCODE_WORKSPACE"] = secrets.WorkspacePath
	}
	if secrets.ProjectPath != "" {
		values["XCODE_PROJECT"] = secrets.ProjectPath
	}

	listed, err := executeIntegration(bound, "list_group_variables", map[string]any{
		"variable_group_id": groupID, "page_size": 100, "page": 1,
	})
	if err != nil {
		return nil, err
	}
	existing := recursiveNamedIDs(listed)
	missing := make([]map[string]any, 0, len(values))
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if variableID := existing[name]; variableID != "" {
			if _, err := executeIntegration(bound, "update_group_variable", map[string]any{
				"variable_group_id": groupID, "variable_id": variableID,
				"value": values[name], "secure": true,
			}); err != nil {
				return nil, err
			}
			continue
		}
		missing = append(missing, map[string]any{"name": name, "value": values[name]})
	}
	if len(missing) > 0 {
		if _, err := executeIntegration(bound, "import_group_variables", map[string]any{
			"variable_group_id": groupID, "secure": true, "variables": missing,
		}); err != nil {
			return nil, err
		}
	}
	configBody, _ := json.Marshal(map[string]any{"group_name": groupName, "group_id": groupID})
	return &mobileSigningProviderResult{
		SecretRef: groupID, ConfigJSON: string(configBody), Groups: []string{groupName},
	}, nil
}

var nonCodemagicGroupChar = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

func codemagicSigningGroupName(d *Deployment) string {
	env := defaultEnvironmentName
	if d != nil && d.EnvironmentName != "" {
		env = d.EnvironmentName
	}
	name := fmt.Sprintf("apteva-ios-%d-%s", d.ID, env)
	name = nonCodemagicGroupChar.ReplaceAllString(name, "-")
	return strings.Trim(name, "-")
}

func recursiveNamedID(raw json.RawMessage, wantedName string) string {
	return recursiveNamedIDs(raw)[wantedName]
}

func recursiveNamedIDs(raw json.RawMessage) map[string]string {
	var value any
	if json.Unmarshal(raw, &value) != nil {
		return map[string]string{}
	}
	out := map[string]string{}
	var walk func(any)
	walk = func(current any) {
		switch item := current.(type) {
		case map[string]any:
			name := firstMapString(item, "name", "key")
			id := firstMapString(item, "_id", "id", "variable_group_id", "variable_id")
			if name != "" && id != "" {
				out[name] = id
			}
			for _, child := range item {
				walk(child)
			}
		case []any:
			for _, child := range item {
				walk(child)
			}
		}
	}
	walk(value)
	return out
}

func uniqueStrings(values []string) []string {
	seen := map[string]bool{}
	out := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" || seen[value] {
			continue
		}
		seen[value] = true
		out = append(out, value)
	}
	return out
}
