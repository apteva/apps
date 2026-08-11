package main

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

func (a *App) mobileSigningIdentityForDeployment(d *Deployment) (*MobileSigningIdentity, error) {
	if d == nil {
		return nil, errors.New("deployment required")
	}
	if globalCtx == nil || globalCtx.AppDB() == nil {
		return nil, errors.New("Deploy database is unavailable")
	}
	target, err := parseMobileTargetConfig(d.TargetConfigJSON)
	if err != nil {
		return nil, err
	}
	switch d.TargetKind {
	case "android":
		return dbGetMobileSigningIdentity(globalCtx.AppDB(), d.ProjectID, "android", "", strings.TrimSpace(target.PackageName))
	case "ios":
		setups, err := dbListMobileSigningSetups(globalCtx.AppDB(), d.ID, d.EnvironmentID)
		if err != nil {
			return nil, err
		}
		for i := range setups {
			if setups[i].Platform == "ios" && setups[i].IdentityID > 0 {
				return dbGetMobileSigningIdentityByID(globalCtx.AppDB(), setups[i].IdentityID)
			}
		}
		return nil, nil
	default:
		return nil, errors.New("mobile signing identity requires an Android or iOS deployment")
	}
}

func (a *App) exportMobileSigningRecovery(d *Deployment) ([]byte, string, error) {
	identity, err := a.mobileSigningIdentityForDeployment(d)
	if err != nil {
		return nil, "", err
	}
	if identity == nil {
		return nil, "", errors.New("mobile signing identity is not configured")
	}
	payload, err := a.decryptMobileSigningPayload(identity)
	if err != nil {
		return nil, "", err
	}
	var output bytes.Buffer
	archive := zip.NewWriter(&output)
	add := func(name string, body []byte) error {
		entry, err := archive.Create(name)
		if err != nil {
			return err
		}
		_, err = entry.Write(body)
		return err
	}
	metadata := map[string]any{
		"platform": identity.Platform, "application_identifier": identity.ApplicationIdentifier,
		"format": identity.Format, "revision": identity.Revision, "source": identity.Source,
		"key_alias": identity.KeyAlias, "certificate_sha1": identity.CertificateSHA1,
		"certificate_sha256": identity.CertificateSHA256, "expires_at": identity.ExpiresAt,
	}
	if identity.Platform == "android" {
		keystore, decodeErr := base64.StdEncoding.DecodeString(payload.KeystoreBase64)
		if decodeErr != nil {
			return nil, "", decodeErr
		}
		if err := add("upload-keystore.p12", keystore); err != nil {
			return nil, "", err
		}
		metadata["store_password"] = payload.StorePassword
		metadata["key_password"] = payload.KeyPassword
		metadata["key_alias"] = payload.KeyAlias
	} else {
		if err := add("distribution-private-key.pem", []byte(payload.PrivateKeyPEM)); err != nil {
			return nil, "", err
		}
		if payload.ProvisioningProfileBase64 != "" {
			profile, decodeErr := base64.StdEncoding.DecodeString(payload.ProvisioningProfileBase64)
			if decodeErr != nil {
				return nil, "", decodeErr
			}
			if err := add("distribution.mobileprovision", profile); err != nil {
				return nil, "", err
			}
		}
	}
	if certificate := defaultStr(payload.CertificatePEM, identity.CertificatePEM); certificate != "" {
		if err := add("certificate.pem", []byte(certificate)); err != nil {
			return nil, "", err
		}
	}
	metadataBody, _ := json.MarshalIndent(metadata, "", "  ")
	if err := add("credentials.json", metadataBody); err != nil {
		return nil, "", err
	}
	if err := add("README.txt", []byte("Sensitive signing recovery archive. Store it encrypted and restrict access.\n")); err != nil {
		return nil, "", err
	}
	if err := archive.Close(); err != nil {
		return nil, "", err
	}
	name := fmt.Sprintf("apteva-%s-signing-%s-r%d.zip", identity.Platform,
		strings.NewReplacer("/", "-", "\\", "-", " ", "-").Replace(identity.ApplicationIdentifier), identity.Revision)
	return output.Bytes(), name, nil
}

func mobileSigningStatusSummary(d *Deployment, setups []MobileSigningSetup, identities []MobileSigningIdentity) map[string]any {
	var setup *MobileSigningSetup
	for i := range setups {
		if d != nil && setups[i].Provider == normalizeBuildBackend(d.BuildBackend) {
			setup = &setups[i]
			break
		}
	}
	if setup == nil && len(setups) > 0 {
		setup = &setups[0]
	}
	var identity *MobileSigningIdentity
	if setup != nil {
		for i := range identities {
			if identities[i].ID == setup.IdentityID {
				identity = &identities[i]
				break
			}
		}
	}
	if identity == nil && len(identities) > 0 {
		identity = &identities[0]
	}
	status := "not_configured"
	provider := ""
	providerReady := false
	if setup != nil {
		status = setup.Status
		provider = setup.Provider
		providerReady = identity != nil && setup.Status == mobileSigningStatusReady &&
			setup.IdentityID == identity.ID && setup.PreparedRevision == identity.Revision
	}
	platform := ""
	if d != nil {
		platform = d.TargetKind
	}
	if platform == "" && identity != nil {
		platform = identity.Platform
	}
	summary := map[string]any{
		"platform": platform,
		"status":   status, "provider": provider, "provider_ready": providerReady,
	}
	if identity != nil {
		summary["identity_id"] = identity.ID
		summary["revision"] = identity.Revision
		summary["application_identifier"] = identity.ApplicationIdentifier
		summary["source"] = identity.Source
		summary["key_alias"] = identity.KeyAlias
		summary["certificate_sha1"] = identity.CertificateSHA1
		summary["certificate_sha256"] = identity.CertificateSHA256
		summary["expires_at"] = identity.ExpiresAt
		if identity.Platform == "android" {
			summary["package_name"] = identity.ApplicationIdentifier
		} else {
			summary["bundle_id"] = identity.ApplicationIdentifier
		}
	}
	return summary
}
