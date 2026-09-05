package main

import (
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"database/sql"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

const (
	mobileSigningVaultKeyFilename = ".mobile-signing-vault-key"
	mobileSigningVaultPrefix      = "MSV1"
	mobileSigningVaultKeyFileEnv  = "APTEVA_MOBILE_SIGNING_VAULT_KEY_FILE"
)

type MobileSigningIdentity struct {
	ID                    int64  `json:"id"`
	ProjectID             string `json:"project_id"`
	Platform              string `json:"platform"`
	AuthorityScope        string `json:"authority_scope,omitempty"`
	ApplicationIdentifier string `json:"application_identifier"`
	Format                string `json:"format"`
	Revision              int    `json:"revision"`
	Source                string `json:"source"`
	KeyAlias              string `json:"key_alias,omitempty"`
	CertificatePEM        string `json:"certificate_pem,omitempty"`
	CertificateSHA1       string `json:"certificate_sha1,omitempty"`
	CertificateSHA256     string `json:"certificate_sha256,omitempty"`
	ExpiresAt             string `json:"expires_at,omitempty"`
	ExternalStateJSON     string `json:"external_state_json,omitempty"`
	CreatedAt             string `json:"created_at"`
	UpdatedAt             string `json:"updated_at"`

	EncryptedPayload []byte `json:"-"`
}

type mobileSigningSecretPayload struct {
	KeystoreBase64            string `json:"keystore_base64,omitempty"`
	StorePassword             string `json:"store_password,omitempty"`
	KeyPassword               string `json:"key_password,omitempty"`
	KeyAlias                  string `json:"key_alias,omitempty"`
	PrivateKeyPEM             string `json:"private_key_pem,omitempty"`
	CertificatePEM            string `json:"certificate_pem,omitempty"`
	ProvisioningProfileBase64 string `json:"provisioning_profile_base64,omitempty"`
}

type mobileSigningIdentityInput struct {
	ProjectID             string
	Platform              string
	AuthorityScope        string
	ApplicationIdentifier string
	Format                string
	Source                string
	KeyAlias              string
	CertificatePEM        string
	CertificateSHA1       string
	CertificateSHA256     string
	ExpiresAt             string
	ExternalStateJSON     string
}

const mobileSigningIdentityColumns = `id, project_id, platform, authority_scope,
	application_identifier, format, encrypted_payload, revision, source, key_alias,
	certificate_pem, certificate_sha1, certificate_sha256, expires_at,
	external_state_json, created_at, updated_at`

func (a *App) mobileSigningVaultKey() ([]byte, error) {
	a.mobileSigningKeyMu.Lock()
	defer a.mobileSigningKeyMu.Unlock()
	if len(a.mobileSigningKey) == 32 {
		return append([]byte(nil), a.mobileSigningKey...), nil
	}
	dataDir := strings.TrimSpace(a.dataDir)
	if dataDir == "" {
		dataDir = strings.TrimSpace(os.Getenv("DEPLOY_DATA_DIR"))
	}
	if dataDir == "" && globalCtx != nil {
		dataDir = strings.TrimSpace(globalCtx.DataDir())
	}
	path := strings.TrimSpace(os.Getenv(mobileSigningVaultKeyFileEnv))
	if dataDir == "" && path == "" {
		return nil, errors.New("deploy data directory is not configured")
	}
	if dataDir != "" {
		if err := os.MkdirAll(dataDir, 0o700); err != nil {
			return nil, err
		}
	}
	if path == "" {
		path = filepath.Join(dataDir, mobileSigningVaultKeyFilename)
	} else if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	key, err := os.ReadFile(path)
	if err == nil {
		if len(key) != 32 {
			return nil, errors.New("invalid mobile signing vault key")
		}
		_ = os.Chmod(path, 0o600)
		a.mobileSigningKey = append([]byte(nil), key...)
		return append([]byte(nil), key...), nil
	}
	if !os.IsNotExist(err) {
		return nil, fmt.Errorf("read mobile signing vault key: %w", err)
	}
	key = make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		return nil, fmt.Errorf("generate mobile signing vault key: %w", err)
	}
	tmp := path + ".tmp"
	_ = os.Remove(tmp)
	if err := os.WriteFile(tmp, key, 0o600); err != nil {
		return nil, fmt.Errorf("write mobile signing vault key: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return nil, fmt.Errorf("commit mobile signing vault key: %w", err)
	}
	a.mobileSigningKey = append([]byte(nil), key...)
	return append([]byte(nil), key...), nil
}

func mobileSigningIdentityAAD(projectID, platform, authority, applicationID string, revision int) []byte {
	return []byte(fmt.Sprintf("mobile-signing/v1\n%s\n%s\n%s\n%s\n%d",
		projectID, platform, authority, applicationID, revision))
}

func (a *App) encryptMobileSigningPayload(input mobileSigningIdentityInput, revision int, payload mobileSigningSecretPayload) ([]byte, error) {
	key, err := a.mobileSigningVaultKey()
	if err != nil {
		return nil, err
	}
	plain, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	sealed := gcm.Seal(nil, nonce, plain, mobileSigningIdentityAAD(
		input.ProjectID, input.Platform, input.AuthorityScope, input.ApplicationIdentifier, revision,
	))
	out := append([]byte(mobileSigningVaultPrefix), nonce...)
	out = append(out, sealed...)
	for i := range plain {
		plain[i] = 0
	}
	return out, nil
}

func (a *App) decryptMobileSigningPayload(identity *MobileSigningIdentity) (*mobileSigningSecretPayload, error) {
	if identity == nil {
		return nil, errors.New("mobile signing identity required")
	}
	key, err := a.mobileSigningVaultKey()
	if err != nil {
		return nil, err
	}
	raw := identity.EncryptedPayload
	if len(raw) < len(mobileSigningVaultPrefix) || string(raw[:len(mobileSigningVaultPrefix)]) != mobileSigningVaultPrefix {
		return nil, errors.New("unsupported mobile signing payload format")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	raw = raw[len(mobileSigningVaultPrefix):]
	if len(raw) < gcm.NonceSize() {
		return nil, errors.New("invalid mobile signing payload")
	}
	plain, err := gcm.Open(nil, raw[:gcm.NonceSize()], raw[gcm.NonceSize():], mobileSigningIdentityAAD(
		identity.ProjectID, identity.Platform, identity.AuthorityScope, identity.ApplicationIdentifier, identity.Revision,
	))
	if err != nil {
		return nil, errors.New("decrypt mobile signing payload")
	}
	defer func() {
		for i := range plain {
			plain[i] = 0
		}
	}()
	var payload mobileSigningSecretPayload
	if err := json.Unmarshal(plain, &payload); err != nil {
		return nil, errors.New("decode mobile signing payload")
	}
	return &payload, nil
}

func (a *App) createMobileSigningIdentity(db *sql.DB, input mobileSigningIdentityInput, payload mobileSigningSecretPayload) (*MobileSigningIdentity, error) {
	input.ProjectID = strings.TrimSpace(input.ProjectID)
	input.Platform = strings.ToLower(strings.TrimSpace(input.Platform))
	input.AuthorityScope = strings.TrimSpace(input.AuthorityScope)
	input.ApplicationIdentifier = strings.TrimSpace(input.ApplicationIdentifier)
	if input.ProjectID == "" || input.ApplicationIdentifier == "" || (input.Platform != "android" && input.Platform != "ios") {
		return nil, errors.New("invalid mobile signing identity scope")
	}
	encrypted, err := a.encryptMobileSigningPayload(input, 1, payload)
	if err != nil {
		return nil, err
	}
	now := nowUTC()
	_, err = db.Exec(`INSERT INTO mobile_signing_identities (
		project_id, platform, authority_scope, application_identifier, format,
		encrypted_payload, revision, source, key_alias, certificate_pem,
		certificate_sha1, certificate_sha256, expires_at, external_state_json,
		created_at, updated_at
	) VALUES (?,?,?,?,?,?,1,?,?,?,?,?,?,?,?,?)`,
		input.ProjectID, input.Platform, input.AuthorityScope, input.ApplicationIdentifier,
		input.Format, encrypted, defaultStr(input.Source, "generated"), input.KeyAlias,
		input.CertificatePEM, normalizeCertificateFingerprint(input.CertificateSHA1),
		normalizeCertificateFingerprint(input.CertificateSHA256), input.ExpiresAt,
		defaultStr(input.ExternalStateJSON, "{}"), now, now)
	if err != nil {
		return nil, err
	}
	return dbGetMobileSigningIdentity(db, input.ProjectID, input.Platform, input.AuthorityScope, input.ApplicationIdentifier)
}

func (a *App) replaceMobileSigningIdentity(db *sql.DB, current *MobileSigningIdentity, input mobileSigningIdentityInput, payload mobileSigningSecretPayload) (*MobileSigningIdentity, error) {
	if current == nil {
		return nil, errors.New("mobile signing identity required")
	}
	input.ProjectID = current.ProjectID
	input.Platform = current.Platform
	input.AuthorityScope = current.AuthorityScope
	input.ApplicationIdentifier = current.ApplicationIdentifier
	revision := current.Revision + 1
	encrypted, err := a.encryptMobileSigningPayload(input, revision, payload)
	if err != nil {
		return nil, err
	}
	tx, err := db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`INSERT OR IGNORE INTO signing_identity_revisions(identity_id,revision,identity_json,encrypted_payload,created_at) VALUES(?,?,?,?,?)`, current.ID, current.Revision, mustJSON(current), current.EncryptedPayload, nowUTC()); err != nil {
		return nil, err
	}
	_, err = tx.Exec(`UPDATE mobile_signing_identities SET
		format=?, encrypted_payload=?, revision=?, source=?, key_alias=?, certificate_pem=?,
		certificate_sha1=?, certificate_sha256=?, expires_at=?, external_state_json=?, updated_at=?
		WHERE id=?`,
		input.Format, encrypted, revision, defaultStr(input.Source, current.Source), input.KeyAlias,
		input.CertificatePEM, normalizeCertificateFingerprint(input.CertificateSHA1),
		normalizeCertificateFingerprint(input.CertificateSHA256), input.ExpiresAt,
		defaultStr(input.ExternalStateJSON, "{}"), nowUTC(), current.ID)
	if err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return dbGetMobileSigningIdentityByID(db, current.ID)
}

func dbUpdateMobileSigningIdentityState(db *sql.DB, identityID int64, externalStateJSON string) error {
	_, err := db.Exec(`UPDATE mobile_signing_identities SET external_state_json=?, updated_at=? WHERE id=?`,
		defaultStr(externalStateJSON, "{}"), nowUTC(), identityID)
	return err
}

func dbGetMobileSigningIdentity(db *sql.DB, projectID, platform, authorityScope, applicationIdentifier string) (*MobileSigningIdentity, error) {
	return scanMobileSigningIdentity(db.QueryRow(`SELECT `+mobileSigningIdentityColumns+`
		FROM mobile_signing_identities WHERE project_id=? AND platform=? AND authority_scope=? AND application_identifier=?`,
		projectID, strings.ToLower(strings.TrimSpace(platform)), strings.TrimSpace(authorityScope), strings.TrimSpace(applicationIdentifier)))
}

func dbGetMobileSigningIdentityByID(db *sql.DB, id int64) (*MobileSigningIdentity, error) {
	return scanMobileSigningIdentity(db.QueryRow(`SELECT `+mobileSigningIdentityColumns+`
		FROM mobile_signing_identities WHERE id=?`, id))
}

func scanMobileSigningIdentity(row rowScanner) (*MobileSigningIdentity, error) {
	var identity MobileSigningIdentity
	if err := row.Scan(&identity.ID, &identity.ProjectID, &identity.Platform, &identity.AuthorityScope,
		&identity.ApplicationIdentifier, &identity.Format, &identity.EncryptedPayload, &identity.Revision,
		&identity.Source, &identity.KeyAlias, &identity.CertificatePEM, &identity.CertificateSHA1,
		&identity.CertificateSHA256, &identity.ExpiresAt, &identity.ExternalStateJSON,
		&identity.CreatedAt, &identity.UpdatedAt); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, nil
		}
		return nil, err
	}
	return &identity, nil
}

func certificateFingerprints(cert *x509.Certificate) (string, string) {
	if cert == nil {
		return "", ""
	}
	one := sha1.Sum(cert.Raw)
	two := sha256.Sum256(cert.Raw)
	return formatCertificateFingerprint(one[:]), formatCertificateFingerprint(two[:])
}

func formatCertificateFingerprint(raw []byte) string {
	encoded := strings.ToUpper(hex.EncodeToString(raw))
	parts := make([]string, 0, len(encoded)/2)
	for i := 0; i+2 <= len(encoded); i += 2 {
		parts = append(parts, encoded[i:i+2])
	}
	return strings.Join(parts, ":")
}

func normalizeCertificateFingerprint(value string) string {
	value = strings.ToUpper(strings.TrimSpace(value))
	value = strings.NewReplacer(":", "", " ", "", "-", "").Replace(value)
	decoded, err := hex.DecodeString(value)
	if err != nil {
		return ""
	}
	return formatCertificateFingerprint(decoded)
}

func randomSigningSecret(bytes int) (string, error) {
	raw := make([]byte, bytes)
	if _, err := rand.Read(raw); err != nil {
		return "", err
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func (a *App) androidSigningCredentials(d *Deployment) (map[string]string, error) {
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
	packageName := strings.TrimSpace(target.PackageName)
	if packageName == "" {
		return nil, errors.New("target_config_json.package_name is required")
	}
	identity, err := dbGetMobileSigningIdentity(globalCtx.AppDB(), d.ProjectID, "android", "", packageName)
	if err != nil {
		return nil, err
	}
	if identity == nil {
		return nil, errors.New("Android signing identity is not configured")
	}
	payload, err := a.decryptMobileSigningPayload(identity)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"upload_keystore_base64":    payload.KeystoreBase64,
		"upload_key_alias":          payload.KeyAlias,
		"upload_keystore_password":  payload.StorePassword,
		"upload_key_password":       payload.KeyPassword,
		"upload_certificate_sha256": identity.CertificateSHA256,
	}, nil
}

func (a *App) iosSigningCredentials(d *Deployment) (map[string]string, error) {
	identity, err := a.mobileSigningIdentityForDeployment(d)
	if err != nil {
		return nil, err
	}
	if identity == nil {
		return nil, errors.New("iOS signing identity is not configured")
	}
	payload, err := a.decryptMobileSigningPayload(identity)
	if err != nil {
		return nil, err
	}
	if payload.PrivateKeyPEM == "" || payload.CertificatePEM == "" || payload.ProvisioningProfileBase64 == "" {
		return nil, errors.New("iOS signing identity is missing certificate or provisioning profile material")
	}
	return map[string]string{
		"certificate_private_key":     payload.PrivateKeyPEM,
		"certificate_pem":             payload.CertificatePEM,
		"provisioning_profile_base64": payload.ProvisioningProfileBase64,
		"certificate_sha256":          identity.CertificateSHA256,
	}, nil
}

func stageSigningRevision(db *sql.DB, candidate *MobileSigningIdentity) (*MobileSigningIdentity, error) {
	_, err := db.Exec(`INSERT OR IGNORE INTO signing_identity_revisions(identity_id,revision,identity_json,encrypted_payload,created_at) VALUES(?,?,?,?,?)`, candidate.ID, candidate.Revision, mustJSON(candidate), candidate.EncryptedPayload, nowUTC())
	if err != nil {
		return nil, err
	}
	var body string
	var encrypted []byte
	if err = db.QueryRow(`SELECT identity_json,encrypted_payload FROM signing_identity_revisions WHERE identity_id=? AND revision=?`, candidate.ID, candidate.Revision).Scan(&body, &encrypted); err != nil {
		return nil, err
	}
	var saved MobileSigningIdentity
	if err = json.Unmarshal([]byte(body), &saved); err != nil {
		return nil, err
	}
	saved.EncryptedPayload = encrypted
	return &saved, nil
}
