package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"

	sdk "github.com/apteva/app-sdk"
)

func cloudIntegrationForSigning(provider string, cfg cloudBuildConfig) (*sdk.BoundIntegration, error) {
	if provider == buildBackendLocal || provider == buildBackendRunner {
		return nil, nil
	}
	return cloudIntegrationForConfig(provider, cfg)
}

type appcircleSigningProvider struct{}

func (appcircleSigningProvider) Name() string { return buildBackendAppcircle }
func (appcircleSigningProvider) SetupMatchesBuildConfig(cfg cloudBuildConfig, setup *MobileSigningSetup) bool {
	if setup == nil || setup.ProviderSecretRef == "" {
		return false
	}
	var saved cloudBuildConfig
	if json.Unmarshal([]byte(setup.ProviderConfigJSON), &saved) != nil {
		return false
	}
	return saved.ProfileID == cfg.ProfileID && saved.ConfigurationID == cfg.ConfigurationID &&
		(cfg.ConnectionID == 0 || cfg.ConnectionID == setup.ProviderConnectionID)
}

func signingSecretEnvironment(secrets mobileSigningSecrets) map[string]string {
	if secrets.Platform == "android" {
		return map[string]string{"ANDROID_UPLOAD_KEYSTORE_BASE64": secrets.AndroidKeystoreBase64, "ANDROID_UPLOAD_KEY_ALIAS": secrets.AndroidKeyAlias, "ANDROID_UPLOAD_STORE_PASSWORD": secrets.AndroidStorePassword, "ANDROID_UPLOAD_KEY_PASSWORD": secrets.AndroidKeyPassword, "ANDROID_UPLOAD_CERT_SHA256": secrets.CertificateSHA256}
	}
	return map[string]string{"APP_STORE_CONNECT_ISSUER_ID": secrets.AppStoreIssuerID, "APP_STORE_CONNECT_KEY_IDENTIFIER": secrets.AppStoreKeyID, "APP_STORE_CONNECT_PRIVATE_KEY": secrets.AppStorePrivateKey, "CERTIFICATE_PRIVATE_KEY": secrets.CertificatePrivateKey, "APTEVA_CERTIFICATE_PEM": secrets.CertificatePEM, "APTEVA_PROVISIONING_PROFILE_BASE64": secrets.ProvisioningProfileBase64, "APTEVA_APPLE_CERT_SHA256": secrets.CertificateSHA256}
}

func (appcircleSigningProvider) ProvisionSigningSecrets(_ context.Context, bound *sdk.BoundIntegration, cfg cloudBuildConfig, d *Deployment, secrets mobileSigningSecrets) (*mobileSigningProviderResult, error) {
	// A fresh revision group avoids mutating keys used by an in-flight build.
	values := signingSecretEnvironment(secrets)
	sum := sha256.Sum256([]byte(mustJSON(values)))
	prefix := fmt.Sprintf("apteva-%s-%d-%d-", secrets.Platform, d.ID, d.EnvironmentID)
	name := fmt.Sprintf("%s%x", prefix, sum[:12])
	raw, err := executeIntegration(bound, "list_build_variable_groups", map[string]any{"Size": 100, "Page": 1})
	if err != nil {
		return nil, err
	}
	group := recursiveNamedID(raw, name)
	if group == "" {
		keys := make([]string, 0, len(values))
		for key := range values {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		variables := make([]map[string]any, 0, len(values))
		for _, key := range keys {
			variables = append(variables, map[string]any{"key": key, "value": values[key], "isSecret": true})
		}
		created, err := executeIntegration(bound, "create_build_variable_group", map[string]any{"name": name, "variables": variables})
		if err != nil {
			return nil, errors.New("Appcircle signing secret upload failed")
		}
		group = firstRecursiveString(created, "id")
		if group == "" {
			return nil, errors.New("Appcircle returned no signing variable group ID")
		}
	}
	configuration, err := executeIntegration(bound, "get_build_configuration", map[string]any{"profileId": cfg.ProfileID, "configurationId": cfg.ConfigurationID})
	if err != nil {
		return nil, err
	}
	object, err := providerObject(configuration)
	if err != nil {
		return nil, err
	}
	var groups []string
	if v := object["environmentVariables"]; len(v) > 0 && string(v) != "null" {
		if err := json.Unmarshal(v, &groups); err != nil {
			return nil, errors.New("Appcircle configuration has invalid environmentVariables")
		}
	}
	managed := recursiveNamedIDs(raw)
	retained := groups[:0]
	for _, id := range groups {
		replace := false
		for groupName, managedID := range managed {
			if strings.HasPrefix(groupName, prefix) && id == managedID {
				replace = true
			}
		}
		if !replace {
			retained = append(retained, id)
		}
	}
	groups = retained
	// Preserve unrelated configuration settings. Only copy writable fields from
	// ConfigurationRequest in Appcircle's published OpenAPI contract.
	writable := strings.Fields("configurationName platformType buildPriority platformSetting workflows autoBuild autoBuildType autoBuildTags distributionProfileId distributionProfileIds publishProfileIds signingIdentities autoDistribute autoPublish autoCancelRedundantPipelines autoStoreSubmit autoAppGallerySubmit autoEnterpriseStoreSend displayBuildStatusBadge sendToSlack slackChannel autoBuildPullRequest pullRequestSourceBranch autoSign autoSignMethodForExport autoSignApiKeyId organizationPoolId runnerArchitecture")
	update := map[string]any{"configurationId": cfg.ConfigurationID, "environmentVariables": uniqueStrings(append(groups, group))}
	for _, key := range writable {
		if value, ok := object[key]; ok {
			update[key] = value
		}
	}
	if _, err := executeIntegration(bound, "update_build_configuration", map[string]any{"profileId": cfg.ProfileID, "configuration": update}); err != nil {
		return nil, fmt.Errorf("attach Appcircle signing group: %w", err)
	}
	return &mobileSigningProviderResult{SecretRef: group, ConfigJSON: mustJSON(cloudBuildConfig{ProfileID: cfg.ProfileID, ConfigurationID: cfg.ConfigurationID, ConnectionID: bound.ConnectionID})}, nil
}
