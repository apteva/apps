package main

import (
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
)

type iosSigningIdentityState struct {
	IssuerID                string `json:"issuer_id"`
	BundleResourceID        string `json:"bundle_resource_id"`
	AppStoreAppID           string `json:"app_store_app_id"`
	CertificateID           string `json:"certificate_id"`
	ProfileID               string `json:"profile_id"`
	KeyFingerprint          string `json:"key_fingerprint"`
	RequirementsHash        string `json:"requirements_hash"`
	RequiredFeaturesJSON    string `json:"required_features_json"`
	ProvisionedFeaturesJSON string `json:"provisioned_features_json"`
	ManagedFeaturesJSON     string `json:"managed_features_json"`
	PlatformStateJSON       string `json:"platform_state_json"`
}

func appleProvisioningProfileContent(bound *sdk.BoundIntegration, profileID string, raw json.RawMessage) (string, error) {
	content := strings.TrimSpace(jsonStringAt(raw, "data", "attributes", "profileContent"))
	if content != "" {
		return content, nil
	}
	profile, err := executeIntegration(bound, "get_profile", map[string]any{"profile_id": profileID})
	if err != nil {
		return "", err
	}
	content = strings.TrimSpace(jsonStringAt(profile, "data", "attributes", "profileContent"))
	if content == "" {
		return "", errors.New("Apple provisioning profile returned no profileContent")
	}
	return content, nil
}

func iosSigningIdentityStateFrom(identity *MobileSigningIdentity) iosSigningIdentityState {
	var state iosSigningIdentityState
	if identity != nil {
		_ = json.Unmarshal([]byte(defaultStr(identity.ExternalStateJSON, "{}")), &state)
	}
	return state
}

func iosSigningIdentityStateJSON(setup *MobileSigningSetup, issuerID string) string {
	if setup == nil {
		return `{}`
	}
	body, _ := json.Marshal(iosSigningIdentityState{
		IssuerID: issuerID, BundleResourceID: setup.AppleBundleResourceID,
		AppStoreAppID: setup.AppStoreAppID, CertificateID: setup.AppleCertificateID,
		ProfileID: setup.AppleProfileID, KeyFingerprint: setup.KeyFingerprint,
		RequirementsHash:        setup.RequirementsHash,
		RequiredFeaturesJSON:    setup.RequiredFeaturesJSON,
		ProvisionedFeaturesJSON: setup.ProvisionedFeaturesJSON,
		ManagedFeaturesJSON:     setup.ManagedFeaturesJSON, PlatformStateJSON: setup.PlatformStateJSON,
	})
	return string(body)
}

func appleCertificateMetadata(raw json.RawMessage) (certificatePEM, sha1Fingerprint, sha256Fingerprint, expiresAt string) {
	encoded := strings.TrimSpace(jsonStringAt(raw, "data", "attributes", "certificateContent"))
	if encoded == "" {
		return "", "", "", ""
	}
	der, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil {
		return "", "", "", ""
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return "", "", "", ""
	}
	one, two := certificateFingerprints(cert)
	return string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw})), one, two,
		cert.NotAfter.UTC().Format(time.RFC3339)
}
