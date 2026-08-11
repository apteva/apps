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
	Platform              string
	IdentityID            int64
	IdentityRevision      int
	ApplicationIdentifier string
	AppStoreIssuerID      string
	AppStoreKeyID         string
	AppStorePrivateKey    string
	CertificatePrivateKey string
	BundleID              string
	AppStoreAppID         string
	Scheme                string
	WorkspacePath         string
	ProjectPath           string
	AndroidKeystoreBase64 string
	AndroidKeyAlias       string
	AndroidStorePassword  string
	AndroidKeyPassword    string
	CertificateSHA256     string
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
	SetupMatchesBuildConfig(cloudBuildConfig, *MobileSigningSetup) bool
	ProvisionSigningSecrets(context.Context, *sdk.BoundIntegration, cloudBuildConfig, *Deployment, mobileSigningSecrets) (*mobileSigningProviderResult, error)
}

func mobileSigningProviderFor(name string) (mobileSigningProvider, error) {
	switch normalizeBuildBackend(name) {
	case buildBackendLocal, buildBackendRunner:
		return transientSigningProvider{name: normalizeBuildBackend(name)}, nil
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
	Setup         *MobileSigningSetup    `json:"setup"`
	Identity      *MobileSigningIdentity `json:"identity,omitempty"`
	Ready         bool                   `json:"ready"`
	ManualActions []string               `json:"manual_actions,omitempty"`
}

func (a *App) setupMobileSigning(ctx context.Context, d *Deployment, providerName string, rotate bool) (*mobileSigningSetupResult, error) {
	a.mobileSigningMu.Lock()
	defer a.mobileSigningMu.Unlock()

	if d == nil {
		return nil, errors.New("deployment required")
	}
	switch d.TargetKind {
	case "android":
		return a.setupAndroidMobileSigning(ctx, d, providerName, rotate)
	case "ios":
		return a.setupIOSMobileSigning(ctx, d, providerName, rotate)
	default:
		return nil, errors.New("mobile signing setup requires an Android or iOS deployment")
	}
}

func (a *App) setupIOSMobileSigning(ctx context.Context, d *Deployment, providerName string, rotate bool) (*mobileSigningSetupResult, error) {
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
	cfg, err := parseMobileSigningBuildConfig(providerName, d.BuildBackendJSON)
	if err != nil {
		return nil, err
	}
	provider, err := mobileSigningProviderFor(providerName)
	if err != nil {
		return nil, err
	}
	providerBound, err := mobileSigningProviderBinding(providerName)
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
	identity, err := dbGetMobileSigningIdentity(globalCtx.AppDB(), d.ProjectID, "ios", issuerID, target.BundleID)
	if err != nil {
		return nil, err
	}
	identityState := iosSigningIdentityStateFrom(identity)
	requirements, err := a.inspectMobileRequirements(ctx, d)
	if err != nil {
		return nil, err
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
	previous := existing
	providerConnectionID := int64(0)
	if providerBound != nil {
		providerConnectionID = providerBound.ConnectionID
	}
	setup := &MobileSigningSetup{
		DeploymentID: d.ID, EnvironmentID: d.EnvironmentID, Platform: "ios",
		Provider: providerName, ProviderConnectionID: providerConnectionID,
		BundleID: target.BundleID, Status: "provisioning",
		RequiredFeaturesJSON: mobileFeaturesJSON(requirements.Features),
		RequirementsHash:     requirements.Hash,
	}
	if previous != nil {
		setup.IdentityID = previous.IdentityID
		setup.PreparedRevision = previous.PreparedRevision
		setup.AppleBundleResourceID = previous.AppleBundleResourceID
		setup.AppStoreAppID = previous.AppStoreAppID
		setup.AppleCertificateID = previous.AppleCertificateID
		setup.AppleProfileID = previous.AppleProfileID
		setup.ProviderSecretRef = previous.ProviderSecretRef
		setup.ProviderConfigJSON = previous.ProviderConfigJSON
		setup.KeyFingerprint = previous.KeyFingerprint
		setup.ProvisionedFeaturesJSON = previous.ProvisionedFeaturesJSON
		setup.ManagedFeaturesJSON = previous.ManagedFeaturesJSON
		setup.PlatformStateJSON = previous.PlatformStateJSON
	}
	if identity != nil {
		setup.IdentityID = identity.ID
		setup.PreparedRevision = identity.Revision
		if setup.AppleBundleResourceID == "" {
			setup.AppleBundleResourceID = identityState.BundleResourceID
		}
		if setup.AppStoreAppID == "" {
			setup.AppStoreAppID = identityState.AppStoreAppID
		}
		if setup.AppleCertificateID == "" {
			setup.AppleCertificateID = identityState.CertificateID
		}
		if setup.AppleProfileID == "" {
			setup.AppleProfileID = identityState.ProfileID
		}
		if setup.KeyFingerprint == "" {
			setup.KeyFingerprint = identityState.KeyFingerprint
		}
	}
	if previous == nil || previous.Status != mobileSigningStatusReady {
		setup, err = dbUpsertMobileSigningSetup(globalCtx.AppDB(), setup)
		if err != nil {
			return nil, err
		}
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

	capabilities, err := reconcileAppleCapabilities(appleBound, bundleResourceID, requirements.Features)
	if err != nil {
		return nil, failSetup(err)
	}
	setup.RequiredFeaturesJSON = mobileFeaturesJSON(requirements.Features)
	setup.ProvisionedFeaturesJSON = mobileFeaturesJSON(capabilities.Provisioned)
	setup.ManagedFeaturesJSON = mobileFeaturesJSON(mergeMobileFeatures(
		mobileFeaturesFromJSON(setup.ManagedFeaturesJSON),
		capabilities.Managed,
	))
	setup.RequirementsHash = requirements.Hash
	setup.PlatformStateJSON = capabilities.StateJSON

	providerMatches := previous != nil &&
		previous.ProviderConnectionID == providerConnectionID &&
		provider.SetupMatchesBuildConfig(cfg, previous)
	requirementsMatch := (previous != nil &&
		previous.RequirementsHash == requirements.Hash &&
		mobileFeaturesContainAll(
			mobileFeaturesFromJSON(previous.ProvisionedFeaturesJSON),
			requirements.Features,
		)) || (identity != nil &&
		identityState.RequirementsHash == requirements.Hash &&
		mobileFeaturesContainAll(
			mobileFeaturesFromJSON(identityState.ProvisionedFeaturesJSON),
			requirements.Features,
		))
	if identity == nil && previous != nil && previous.Status == mobileSigningStatusReady &&
		providerMatches && requirementsMatch && !capabilities.Changed && !rotate {
		legacy := *previous
		legacy.LastError = ""
		legacySetup, upsertErr := dbUpsertMobileSigningSetup(globalCtx.AppDB(), &legacy)
		if upsertErr != nil {
			return nil, upsertErr
		}
		return &mobileSigningSetupResult{
			Setup: legacySetup, Ready: true,
			ManualActions: []string{
				"Rotate signing once to move the existing provider-owned iOS private key into Deploy's durable signing vault.",
			},
		}, nil
	}
	if identity != nil && previous != nil && previous.Status == mobileSigningStatusReady &&
		previous.IdentityID == identity.ID && previous.PreparedRevision == identity.Revision &&
		providerMatches && requirementsMatch && !capabilities.Changed && !rotate {
		ready := *previous
		ready.RequiredFeaturesJSON = setup.RequiredFeaturesJSON
		ready.ProvisionedFeaturesJSON = setup.ProvisionedFeaturesJSON
		ready.ManagedFeaturesJSON = setup.ManagedFeaturesJSON
		ready.RequirementsHash = setup.RequirementsHash
		ready.PlatformStateJSON = setup.PlatformStateJSON
		ready.LastError = ""
		readySetup, err := dbUpsertMobileSigningSetup(globalCtx.AppDB(), &ready)
		if err != nil {
			return nil, err
		}
		return &mobileSigningSetupResult{Setup: readySetup, Identity: identity, Ready: true}, nil
	}

	if identity != nil && !rotate {
		certificateID := defaultStr(setup.AppleCertificateID, identityState.CertificateID)
		available, checkErr := appleCertificateAvailable(appleBound, certificateID)
		if checkErr != nil {
			return nil, failSetup(checkErr)
		}
		if available {
			profileID := setup.AppleProfileID
			createdProfile := false
			newProfileContent := ""
			var createdProfileBody json.RawMessage
			if profileID == "" || !requirementsMatch || capabilities.Changed {
				profile, createErr := executeIntegration(appleBound, "create_profile", map[string]any{
					"name":            appleReplacementProfileName(d, identityState.KeyFingerprint, requirements.Hash),
					"profileType":     "IOS_APP_STORE",
					"bundle_id":       bundleResourceID,
					"certificate_ids": []string{certificateID},
				})
				if createErr != nil {
					return nil, failSetup(createErr)
				}
				createdProfileBody = profile
				profileID = jsonStringAt(profile, "data", "id")
				if profileID == "" {
					return nil, failSetup(errors.New("Apple create_profile returned no resource id"))
				}
				createdProfile = true
			}
			if createdProfile {
				profileContent, profileErr := appleProvisioningProfileContent(appleBound, profileID, createdProfileBody)
				if profileErr != nil {
					_, _ = executeIntegration(appleBound, "delete_profile", map[string]any{"profile_id": profileID})
					return nil, failSetup(profileErr)
				}
				newProfileContent = profileContent
			}
			providerResult := &mobileSigningProviderResult{
				SecretRef: setup.ProviderSecretRef, ConfigJSON: defaultStr(setup.ProviderConfigJSON, "{}"),
			}
			if previous == nil || !providerMatches {
				secretPayload, decryptErr := a.decryptMobileSigningPayload(identity)
				if decryptErr != nil {
					if createdProfile {
						_, _ = executeIntegration(appleBound, "delete_profile", map[string]any{"profile_id": profileID})
					}
					return nil, failSetup(decryptErr)
				}
				providerResult, err = provider.ProvisionSigningSecrets(ctx, providerBound, cfg, d, mobileSigningSecrets{
					Platform: "ios", IdentityID: identity.ID, IdentityRevision: identity.Revision,
					ApplicationIdentifier: target.BundleID,
					AppStoreIssuerID:      issuerID, AppStoreKeyID: keyID, AppStorePrivateKey: apiPrivateKey,
					CertificatePrivateKey: secretPayload.PrivateKeyPEM, BundleID: target.BundleID,
					AppStoreAppID: appStoreAppID, Scheme: target.Scheme,
					WorkspacePath: target.WorkspacePath, ProjectPath: target.ProjectPath,
				})
				if err != nil {
					if createdProfile {
						_, _ = executeIntegration(appleBound, "delete_profile", map[string]any{"profile_id": profileID})
					}
					return nil, failSetup(err)
				}
				cfg.Groups = uniqueStrings(append(cfg.Groups, providerResult.Groups...))
			}
			setup.Status = mobileSigningStatusReady
			setup.IdentityID = identity.ID
			setup.PreparedRevision = identity.Revision
			setup.AppleCertificateID = certificateID
			setup.AppleProfileID = profileID
			setup.ProviderSecretRef = providerResult.SecretRef
			setup.ProviderConfigJSON = defaultStr(providerResult.ConfigJSON, "{}")
			setup.KeyFingerprint = identityState.KeyFingerprint
			setup.LastError = ""
			target.AppStoreAppID = appStoreAppID
			targetBody, marshalErr := json.Marshal(target)
			if marshalErr != nil {
				if createdProfile {
					_, _ = executeIntegration(appleBound, "delete_profile", map[string]any{"profile_id": profileID})
				}
				return nil, failSetup(marshalErr)
			}
			cfgBody, marshalErr := json.Marshal(cfg)
			if marshalErr != nil {
				if createdProfile {
					_, _ = executeIntegration(appleBound, "delete_profile", map[string]any{"profile_id": profileID})
				}
				return nil, failSetup(marshalErr)
			}
			if persistErr := persistEffectiveDeploymentConfig(d, map[string]any{
				"target_config_json": string(targetBody), "build_backend_config_json": string(cfgBody),
			}); persistErr != nil {
				if createdProfile {
					_, _ = executeIntegration(appleBound, "delete_profile", map[string]any{"profile_id": profileID})
				}
				return nil, failSetup(persistErr)
			}
			identityUpdated := false
			if createdProfile {
				secretPayload, decryptErr := a.decryptMobileSigningPayload(identity)
				if decryptErr != nil {
					_, _ = executeIntegration(appleBound, "delete_profile", map[string]any{"profile_id": profileID})
					return nil, failSetup(decryptErr)
				}
				secretPayload.ProvisioningProfileBase64 = newProfileContent
				identity, err = a.replaceMobileSigningIdentity(globalCtx.AppDB(), identity, mobileSigningIdentityInput{
					Format: identity.Format, Source: identity.Source, KeyAlias: identity.KeyAlias,
					CertificatePEM: identity.CertificatePEM, CertificateSHA1: identity.CertificateSHA1,
					CertificateSHA256: identity.CertificateSHA256, ExpiresAt: identity.ExpiresAt,
					ExternalStateJSON: identity.ExternalStateJSON,
				}, *secretPayload)
				if err != nil {
					_, _ = executeIntegration(appleBound, "delete_profile", map[string]any{"profile_id": profileID})
					return nil, failSetup(err)
				}
				identityUpdated = true
				setup.IdentityID = identity.ID
				setup.PreparedRevision = identity.Revision
			}
			setup, err = dbUpsertMobileSigningSetup(globalCtx.AppDB(), setup)
			if err != nil {
				if createdProfile && !identityUpdated {
					_, _ = executeIntegration(appleBound, "delete_profile", map[string]any{"profile_id": profileID})
				}
				return nil, err
			}
			_ = dbUpdateMobileSigningIdentityState(globalCtx.AppDB(), identity.ID, iosSigningIdentityStateJSON(setup, issuerID))
			if previous != nil && previous.AppleProfileID != "" && previous.AppleProfileID != profileID {
				_, _ = executeIntegration(appleBound, "delete_profile", map[string]any{"profile_id": previous.AppleProfileID})
			}
			emit("deploy.mobile_signing.ready", map[string]any{
				"deployment_id": d.ID, "environment_id": d.EnvironmentID,
				"provider": providerName, "bundle_id": target.BundleID,
				"repair": true,
			})
			identity, _ = dbGetMobileSigningIdentityByID(globalCtx.AppDB(), identity.ID)
			return &mobileSigningSetupResult{Setup: setup, Identity: identity, Ready: true}, nil
		}
	}

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
		"name":            appleReplacementProfileName(d, fingerprint, requirements.Hash),
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
	profileContent, err := appleProvisioningProfileContent(appleBound, createdProfileID, profile)
	if err != nil {
		cleanupNewAppleResources()
		return nil, failSetup(err)
	}

	providerResult, err := provider.ProvisionSigningSecrets(ctx, providerBound, cfg, d, mobileSigningSecrets{
		Platform: "ios", ApplicationIdentifier: target.BundleID,
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
	setup.RequiredFeaturesJSON = mobileFeaturesJSON(requirements.Features)
	setup.ProvisionedFeaturesJSON = mobileFeaturesJSON(capabilities.Provisioned)
	setup.ManagedFeaturesJSON = mobileFeaturesJSON(mergeMobileFeatures(
		mobileFeaturesFromJSON(setup.ManagedFeaturesJSON),
		capabilities.Managed,
	))
	setup.RequirementsHash = requirements.Hash
	setup.PlatformStateJSON = capabilities.StateJSON
	setup.LastError = ""
	certificatePEM, certificateSHA1, certificateSHA256, certificateExpiresAt := appleCertificateMetadata(certificate)
	identityInput := mobileSigningIdentityInput{
		ProjectID: d.ProjectID, Platform: "ios", AuthorityScope: issuerID,
		ApplicationIdentifier: target.BundleID, Format: "pem", Source: "generated",
		CertificatePEM: certificatePEM, CertificateSHA1: certificateSHA1,
		CertificateSHA256: certificateSHA256, ExpiresAt: certificateExpiresAt,
		ExternalStateJSON: iosSigningIdentityStateJSON(setup, issuerID),
	}
	identityPayload := mobileSigningSecretPayload{
		PrivateKeyPEM: certificatePrivateKey, CertificatePEM: certificatePEM,
		ProvisioningProfileBase64: profileContent,
	}
	if identity == nil {
		identity, err = a.createMobileSigningIdentity(globalCtx.AppDB(), identityInput, identityPayload)
	} else {
		identity, err = a.replaceMobileSigningIdentity(globalCtx.AppDB(), identity, identityInput, identityPayload)
	}
	if err != nil {
		cleanupNewAppleResources()
		return nil, failSetup(err)
	}
	setup.IdentityID = identity.ID
	setup.PreparedRevision = identity.Revision
	setup, err = dbUpsertMobileSigningSetup(globalCtx.AppDB(), setup)
	if err != nil {
		return nil, failSetup(err)
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
	return &mobileSigningSetupResult{Setup: setup, Identity: identity, Ready: true}, nil
}

func parseMobileSigningBuildConfig(providerName, raw string) (cloudBuildConfig, error) {
	if normalizeBuildBackend(providerName) == buildBackendLocal {
		return cloudBuildConfig{}, nil
	}
	return parseCloudBuildConfig(providerName, raw)
}

type transientSigningProvider struct{ name string }

func (p transientSigningProvider) Name() string { return p.name }

func (transientSigningProvider) SetupMatchesBuildConfig(_ cloudBuildConfig, setup *MobileSigningSetup) bool {
	return setup != nil
}

func (transientSigningProvider) ProvisionSigningSecrets(
	_ context.Context,
	_ *sdk.BoundIntegration,
	_ cloudBuildConfig,
	_ *Deployment,
	_ mobileSigningSecrets,
) (*mobileSigningProviderResult, error) {
	return &mobileSigningProviderResult{ConfigJSON: `{}`}, nil
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

func appleReplacementProfileName(d *Deployment, fingerprint, requirementsHash string) string {
	suffix := strings.TrimSpace(fingerprint)
	if len(suffix) > 8 {
		suffix = suffix[:8]
	}
	requirementSuffix := strings.TrimSpace(requirementsHash)
	if len(requirementSuffix) > 8 {
		requirementSuffix = requirementSuffix[:8]
	}
	if requirementSuffix != "" {
		suffix = strings.Trim(strings.Join([]string{suffix, requirementSuffix}, "-"), "-")
	}
	var nonce [4]byte
	if _, err := rand.Read(nonce[:]); err == nil {
		suffix = strings.Trim(strings.Join([]string{suffix, hex.EncodeToString(nonce[:])}, "-"), "-")
	}
	if suffix == "" {
		return appleResourceName(d) + " App Store"
	}
	return appleResourceName(d) + " App Store " + suffix
}

type codemagicSigningProvider struct{}

func (codemagicSigningProvider) Name() string { return buildBackendCodemagic }

func (codemagicSigningProvider) SetupMatchesBuildConfig(cfg cloudBuildConfig, setup *MobileSigningSetup) bool {
	if setup == nil || strings.TrimSpace(cfg.AppID) == "" {
		return false
	}
	var providerConfig struct {
		AppID string `json:"app_id"`
	}
	if json.Unmarshal([]byte(defaultStr(setup.ProviderConfigJSON, "{}")), &providerConfig) != nil {
		return false
	}
	return strings.TrimSpace(providerConfig.AppID) == strings.TrimSpace(cfg.AppID)
}

func (codemagicSigningProvider) ProvisionSigningSecrets(
	_ context.Context,
	bound *sdk.BoundIntegration,
	cfg cloudBuildConfig,
	d *Deployment,
	secrets mobileSigningSecrets,
) (*mobileSigningProviderResult, error) {
	groupName := codemagicSigningGroupName(d, secrets)
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

	values := map[string]string{}
	if secrets.Platform == "android" {
		values = map[string]string{
			"ANDROID_UPLOAD_KEYSTORE_BASE64": secrets.AndroidKeystoreBase64,
			"ANDROID_UPLOAD_KEY_ALIAS":       secrets.AndroidKeyAlias,
			"ANDROID_UPLOAD_STORE_PASSWORD":  secrets.AndroidStorePassword,
			"ANDROID_UPLOAD_KEY_PASSWORD":    secrets.AndroidKeyPassword,
			"ANDROID_UPLOAD_CERT_SHA256":     secrets.CertificateSHA256,
		}
	} else {
		values = map[string]string{
			"APP_STORE_CONNECT_ISSUER_ID":      secrets.AppStoreIssuerID,
			"APP_STORE_CONNECT_KEY_IDENTIFIER": secrets.AppStoreKeyID,
			"APP_STORE_CONNECT_PRIVATE_KEY":    secrets.AppStorePrivateKey,
			"CERTIFICATE_PRIVATE_KEY":          secrets.CertificatePrivateKey,
			"BUNDLE_ID":                        secrets.BundleID,
			"APP_STORE_ID":                     secrets.AppStoreAppID,
		}
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
	configBody, _ := json.Marshal(map[string]any{
		"app_id": cfg.AppID, "group_name": groupName, "group_id": groupID,
	})
	return &mobileSigningProviderResult{
		SecretRef: groupID, ConfigJSON: string(configBody), Groups: []string{groupName},
	}, nil
}

var nonCodemagicGroupChar = regexp.MustCompile(`[^A-Za-z0-9_-]+`)

func codemagicSigningGroupName(d *Deployment, secrets mobileSigningSecrets) string {
	env := defaultEnvironmentName
	if d != nil && d.EnvironmentName != "" {
		env = d.EnvironmentName
	}
	platform := "mobile"
	if d != nil && (d.TargetKind == "android" || d.TargetKind == "ios") {
		platform = d.TargetKind
	}
	name := ""
	if identifier := strings.TrimSpace(secrets.ApplicationIdentifier); identifier != "" {
		sum := sha256.Sum256([]byte(platform + "\n" + identifier))
		name = fmt.Sprintf("apteva-%s-signing-%s", platform, hex.EncodeToString(sum[:6]))
	} else {
		name = fmt.Sprintf("apteva-%s-%d-%s", platform, d.ID, env)
	}
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
