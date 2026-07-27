package main

import (
	"database/sql"
	"errors"
	"strings"
)

type MobileSigningSetup struct {
	ID                    int64  `json:"id"`
	DeploymentID          int64  `json:"deployment_id"`
	EnvironmentID         int64  `json:"environment_id,omitempty"`
	Platform              string `json:"platform"`
	Provider              string `json:"provider"`
	ProviderConnectionID  int64  `json:"provider_connection_id,omitempty"`
	BundleID              string `json:"bundle_id"`
	Status                string `json:"status"`
	AppStoreAppID         string `json:"app_store_app_id,omitempty"`
	AppleBundleResourceID string `json:"apple_bundle_resource_id,omitempty"`
	AppleCertificateID    string `json:"apple_certificate_id,omitempty"`
	AppleProfileID        string `json:"apple_profile_id,omitempty"`
	ProviderSecretRef     string `json:"provider_secret_ref,omitempty"`
	ProviderConfigJSON    string `json:"provider_config_json"`
	KeyFingerprint        string `json:"key_fingerprint,omitempty"`
	LastError             string `json:"last_error,omitempty"`
	CreatedAt             string `json:"created_at"`
	UpdatedAt             string `json:"updated_at"`
}

const mobileSigningSetupColumns = `id, deployment_id, environment_id, platform, provider,
	provider_connection_id, bundle_id, status, app_store_app_id,
	apple_bundle_resource_id, apple_certificate_id, apple_profile_id,
	provider_secret_ref, provider_config_json, key_fingerprint, last_error,
	created_at, updated_at`

func dbGetMobileSigningSetup(db *sql.DB, deploymentID, environmentID int64, provider string) (*MobileSigningSetup, error) {
	row := db.QueryRow(`SELECT `+mobileSigningSetupColumns+`
		FROM mobile_signing_setups
		WHERE deployment_id = ? AND environment_id = ? AND provider = ?`,
		deploymentID, environmentID, normalizeBuildBackend(provider))
	setup, err := scanMobileSigningSetup(row)
	if errors.Is(err, sql.ErrNoRows) {
		return nil, nil
	}
	return setup, err
}

func dbListMobileSigningSetups(db *sql.DB, deploymentID, environmentID int64) ([]MobileSigningSetup, error) {
	rows, err := db.Query(`SELECT `+mobileSigningSetupColumns+`
		FROM mobile_signing_setups
		WHERE deployment_id = ? AND environment_id = ?
		ORDER BY id DESC`, deploymentID, environmentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []MobileSigningSetup{}
	for rows.Next() {
		setup, err := scanMobileSigningSetup(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, *setup)
	}
	return out, rows.Err()
}

func dbUpsertMobileSigningSetup(db *sql.DB, setup *MobileSigningSetup) (*MobileSigningSetup, error) {
	if setup == nil {
		return nil, errors.New("mobile signing setup required")
	}
	now := nowUTC()
	_, err := db.Exec(`
		INSERT INTO mobile_signing_setups (
			deployment_id, environment_id, platform, provider,
			provider_connection_id, bundle_id, status, app_store_app_id,
			apple_bundle_resource_id, apple_certificate_id, apple_profile_id,
			provider_secret_ref, provider_config_json, key_fingerprint, last_error,
			created_at, updated_at
		) VALUES (?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)
		ON CONFLICT(deployment_id, environment_id, provider) DO UPDATE SET
			platform = excluded.platform,
			provider_connection_id = excluded.provider_connection_id,
			bundle_id = excluded.bundle_id,
			status = excluded.status,
			app_store_app_id = excluded.app_store_app_id,
			apple_bundle_resource_id = excluded.apple_bundle_resource_id,
			apple_certificate_id = excluded.apple_certificate_id,
			apple_profile_id = excluded.apple_profile_id,
			provider_secret_ref = excluded.provider_secret_ref,
			provider_config_json = excluded.provider_config_json,
			key_fingerprint = excluded.key_fingerprint,
			last_error = excluded.last_error,
			updated_at = excluded.updated_at
	`, setup.DeploymentID, setup.EnvironmentID, setup.Platform, normalizeBuildBackend(setup.Provider),
		setup.ProviderConnectionID, setup.BundleID, defaultStr(setup.Status, "pending"), setup.AppStoreAppID,
		setup.AppleBundleResourceID, setup.AppleCertificateID, setup.AppleProfileID,
		setup.ProviderSecretRef, defaultStr(setup.ProviderConfigJSON, "{}"), setup.KeyFingerprint, setup.LastError,
		now, now)
	if err != nil {
		return nil, err
	}
	return dbGetMobileSigningSetup(db, setup.DeploymentID, setup.EnvironmentID, setup.Provider)
}

func scanMobileSigningSetup(row rowScanner) (*MobileSigningSetup, error) {
	var setup MobileSigningSetup
	if err := row.Scan(
		&setup.ID, &setup.DeploymentID, &setup.EnvironmentID, &setup.Platform, &setup.Provider,
		&setup.ProviderConnectionID, &setup.BundleID, &setup.Status, &setup.AppStoreAppID,
		&setup.AppleBundleResourceID, &setup.AppleCertificateID, &setup.AppleProfileID,
		&setup.ProviderSecretRef, &setup.ProviderConfigJSON, &setup.KeyFingerprint, &setup.LastError,
		&setup.CreatedAt, &setup.UpdatedAt,
	); err != nil {
		return nil, err
	}
	setup.Provider = strings.TrimSpace(setup.Provider)
	return &setup, nil
}
