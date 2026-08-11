package main

import (
	"context"
	"crypto"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	pkcs12 "github.com/ggpslop/go-pkcs12"
)

const generatedAndroidKeyAliasPrefix = "upload-"

func (a *App) setupAndroidMobileSigning(ctx context.Context, d *Deployment, providerName string, rotate bool) (*mobileSigningSetupResult, error) {
	target, err := parseMobileTargetConfig(d.TargetConfigJSON)
	if err != nil {
		return nil, err
	}
	packageName := strings.TrimSpace(target.PackageName)
	if packageName == "" {
		return nil, errors.New("target_config_json.package_name is required")
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

	db := globalCtx.AppDB()
	identity, err := dbGetMobileSigningIdentity(db, d.ProjectID, "android", "", packageName)
	if err != nil {
		return nil, err
	}
	if identity == nil {
		identity, err = a.migrateLegacyAndroidSigningIdentity(d.ProjectID, packageName)
		if err != nil {
			return nil, err
		}
	}
	if identity == nil || rotate {
		input, payload, generateErr := generateAndroidSigningIdentity(d.ProjectID, packageName)
		if generateErr != nil {
			return nil, generateErr
		}
		if identity == nil {
			identity, err = a.createMobileSigningIdentity(db, input, payload)
		} else {
			identity, err = a.replaceMobileSigningIdentity(db, identity, input, payload)
		}
		if err != nil {
			return nil, err
		}
	}
	providerConnectionID := int64(0)
	if providerBound != nil {
		providerConnectionID = providerBound.ConnectionID
	}
	existingSetup, err := dbGetMobileSigningSetup(db, d.ID, d.EnvironmentID, providerName)
	if err != nil {
		return nil, err
	}
	if !rotate && existingSetup != nil && existingSetup.Status == mobileSigningStatusReady &&
		existingSetup.IdentityID == identity.ID && existingSetup.PreparedRevision == identity.Revision &&
		existingSetup.ProviderConnectionID == providerConnectionID &&
		provider.SetupMatchesBuildConfig(cfg, existingSetup) {
		existingSetup.LastError = ""
		return &mobileSigningSetupResult{Setup: existingSetup, Identity: identity, Ready: true}, nil
	}
	payload, err := a.decryptMobileSigningPayload(identity)
	if err != nil {
		return nil, err
	}

	setup := &MobileSigningSetup{
		DeploymentID: d.ID, EnvironmentID: d.EnvironmentID, Platform: "android",
		Provider: providerName, BundleID: packageName, Status: "provisioning",
		IdentityID: identity.ID, PreparedRevision: identity.Revision,
		KeyFingerprint:     identity.CertificateSHA256,
		ProviderConfigJSON: "{}", RequiredFeaturesJSON: "[]",
		ProvisionedFeaturesJSON: "[]", ManagedFeaturesJSON: "[]", PlatformStateJSON: "{}",
	}
	if providerBound != nil {
		setup.ProviderConnectionID = providerConnectionID
	}
	if _, err := dbUpsertMobileSigningSetup(db, setup); err != nil {
		return nil, err
	}
	fail := func(cause error) error {
		setup.Status = "failed"
		setup.LastError = cause.Error()
		_, _ = dbUpsertMobileSigningSetup(db, setup)
		emit("deploy.mobile_signing.failed", map[string]any{
			"deployment_id": d.ID, "environment_id": d.EnvironmentID,
			"platform": "android", "provider": providerName, "error": cause.Error(),
		})
		return cause
	}

	providerResult, err := provider.ProvisionSigningSecrets(ctx, providerBound, cfg, d, mobileSigningSecrets{
		Platform: "android", IdentityID: identity.ID, IdentityRevision: identity.Revision,
		ApplicationIdentifier: packageName,
		AndroidKeystoreBase64: payload.KeystoreBase64,
		AndroidKeyAlias:       payload.KeyAlias, AndroidStorePassword: payload.StorePassword,
		AndroidKeyPassword: payload.KeyPassword, CertificateSHA256: identity.CertificateSHA256,
	})
	if err != nil {
		return nil, fail(err)
	}
	if providerResult == nil {
		providerResult = &mobileSigningProviderResult{ConfigJSON: `{}`}
	}
	if len(providerResult.Groups) > 0 {
		cfg.Groups = uniqueStrings(append(cfg.Groups, providerResult.Groups...))
		cfgBody, marshalErr := json.Marshal(cfg)
		if marshalErr != nil {
			return nil, fail(marshalErr)
		}
		if persistErr := persistEffectiveDeploymentConfig(d, map[string]any{
			"build_backend_config_json": string(cfgBody),
		}); persistErr != nil {
			return nil, fail(persistErr)
		}
	}
	setup.Status = mobileSigningStatusReady
	setup.ProviderSecretRef = providerResult.SecretRef
	setup.ProviderConfigJSON = defaultStr(providerResult.ConfigJSON, "{}")
	setup.LastError = ""
	setup, err = dbUpsertMobileSigningSetup(db, setup)
	if err != nil {
		return nil, err
	}
	emit("deploy.mobile_signing.ready", map[string]any{
		"deployment_id": d.ID, "environment_id": d.EnvironmentID,
		"platform": "android", "provider": providerName,
		"package_name": packageName, "identity_revision": identity.Revision,
	})
	return &mobileSigningSetupResult{Setup: setup, Identity: identity, Ready: true}, nil
}

func (a *App) migrateLegacyAndroidSigningIdentity(projectID, packageName string) (*MobileSigningIdentity, error) {
	for _, role := range []string{"android_signing", "play_store"} {
		credentials, err := boundConnectionCredentials(role)
		if err != nil || credentials == nil {
			continue
		}
		fields := credentials.Fields
		encoded := strings.TrimSpace(fields["upload_keystore_base64"])
		storePassword := fields["upload_keystore_password"]
		if encoded == "" && storePassword == "" {
			continue
		}
		if encoded == "" || storePassword == "" {
			return nil, fmt.Errorf("legacy %s upload signing credentials are incomplete", role)
		}
		pfx, decodeErr := base64.StdEncoding.DecodeString(encoded)
		if decodeErr != nil {
			return nil, fmt.Errorf("decode legacy %s upload keystore: %w", role, decodeErr)
		}
		input, payload, importErr := androidSigningIdentityFromPKCS12(
			projectID, packageName, pfx, storePassword,
			fields["upload_key_password"], fields["upload_key_alias"],
		)
		if importErr != nil {
			return nil, fmt.Errorf("migrate legacy %s upload signing identity: %w", role, importErr)
		}
		identity, createErr := a.createMobileSigningIdentity(globalCtx.AppDB(), input, payload)
		if createErr != nil {
			return nil, createErr
		}
		emit("deploy.mobile_signing.legacy_migrated", map[string]any{
			"platform": "android", "project_id": projectID,
			"package_name": packageName, "identity_id": identity.ID,
		})
		return identity, nil
	}
	return nil, nil
}

func mobileSigningProviderBinding(providerName string) (*sdk.BoundIntegration, error) {
	switch normalizeBuildBackend(providerName) {
	case buildBackendLocal, buildBackendRunner:
		return nil, nil
	default:
		return cloudIntegrationFor(providerName)
	}
}

func generateAndroidSigningIdentity(projectID, packageName string) (mobileSigningIdentityInput, mobileSigningSecretPayload, error) {
	key, err := rsa.GenerateKey(rand.Reader, 4096)
	if err != nil {
		return mobileSigningIdentityInput{}, mobileSigningSecretPayload{}, fmt.Errorf("generate Android upload key: %w", err)
	}
	serialLimit := new(big.Int).Lsh(big.NewInt(1), 128)
	serial, err := rand.Int(rand.Reader, serialLimit)
	if err != nil {
		return mobileSigningIdentityInput{}, mobileSigningSecretPayload{}, err
	}
	now := time.Now().UTC()
	template := &x509.Certificate{
		SerialNumber: serial,
		Subject: pkix.Name{
			CommonName:   "Apteva Android Upload " + packageName,
			Organization: []string{"Apteva managed signing"},
		},
		NotBefore: now.Add(-5 * time.Minute),
		NotAfter:  now.AddDate(25, 0, 0),
		KeyUsage:  x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		return mobileSigningIdentityInput{}, mobileSigningSecretPayload{}, fmt.Errorf("create Android upload certificate: %w", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		return mobileSigningIdentityInput{}, mobileSigningSecretPayload{}, err
	}
	storePassword, err := randomSigningSecret(32)
	if err != nil {
		return mobileSigningIdentityInput{}, mobileSigningSecretPayload{}, err
	}
	keyPassword, err := randomSigningSecret(32)
	if err != nil {
		return mobileSigningIdentityInput{}, mobileSigningSecretPayload{}, err
	}
	aliasSuffix, err := randomSigningSecret(9)
	if err != nil {
		return mobileSigningIdentityInput{}, mobileSigningSecretPayload{}, err
	}
	alias := generatedAndroidKeyAliasPrefix + strings.ToLower(aliasSuffix)
	builder, err := pkcs12.NewBuilder(pkcs12.Modern, []byte(storePassword), pkcs12.Options{PrvKeyEntryLen: 1})
	if err != nil {
		return mobileSigningIdentityInput{}, mobileSigningSecretPayload{}, fmt.Errorf("initialize Android upload keystore: %w", err)
	}
	if err := builder.SetPrivateKeyEntry(alias, key, cert, nil, []byte(keyPassword)); err != nil {
		return mobileSigningIdentityInput{}, mobileSigningSecretPayload{}, fmt.Errorf("add Android upload key: %w", err)
	}
	pfx, err := builder.Build()
	if err != nil {
		return mobileSigningIdentityInput{}, mobileSigningSecretPayload{}, fmt.Errorf("encode Android upload keystore: %w", err)
	}
	certPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}))
	sha1Fingerprint, sha256Fingerprint := certificateFingerprints(cert)
	return mobileSigningIdentityInput{
			ProjectID: projectID, Platform: "android", ApplicationIdentifier: packageName,
			Format: "pkcs12", Source: "generated", KeyAlias: alias,
			CertificatePEM: certPEM, CertificateSHA1: sha1Fingerprint,
			CertificateSHA256: sha256Fingerprint, ExpiresAt: cert.NotAfter.Format(time.RFC3339),
			ExternalStateJSON: `{}`,
		}, mobileSigningSecretPayload{
			KeystoreBase64: base64.StdEncoding.EncodeToString(pfx), StorePassword: storePassword,
			KeyPassword: keyPassword, KeyAlias: alias, CertificatePEM: certPEM,
		}, nil
}

func androidSigningIdentityFromPKCS12(projectID, packageName string, pfx []byte, storePassword, keyPassword, alias string) (mobileSigningIdentityInput, mobileSigningSecretPayload, error) {
	alias = strings.TrimSpace(alias)
	if keyPassword == "" {
		keyPassword = storePassword
	}
	privateKey, cert, err := decodeAndroidPKCS12Key(pfx, storePassword, keyPassword, alias)
	if err != nil {
		return mobileSigningIdentityInput{}, mobileSigningSecretPayload{}, fmt.Errorf("decode Android PKCS#12 keystore: %w", err)
	}
	signer, ok := privateKey.(crypto.Signer)
	if !ok || cert == nil {
		return mobileSigningIdentityInput{}, mobileSigningSecretPayload{}, errors.New("Android keystore must contain one private key and its certificate")
	}
	privatePublic, err := x509.MarshalPKIXPublicKey(signer.Public())
	if err != nil {
		return mobileSigningIdentityInput{}, mobileSigningSecretPayload{}, err
	}
	certificatePublic, err := x509.MarshalPKIXPublicKey(cert.PublicKey)
	if err != nil {
		return mobileSigningIdentityInput{}, mobileSigningSecretPayload{}, err
	}
	if !strings.EqualFold(base64.StdEncoding.EncodeToString(privatePublic), base64.StdEncoding.EncodeToString(certificatePublic)) {
		return mobileSigningIdentityInput{}, mobileSigningSecretPayload{}, errors.New("Android keystore private key does not match its certificate")
	}
	now := time.Now().UTC()
	if now.Before(cert.NotBefore) || !now.Before(cert.NotAfter) {
		return mobileSigningIdentityInput{}, mobileSigningSecretPayload{}, errors.New("Android upload certificate is not currently valid")
	}
	discoveredAlias := ""
	if keyPassword == storePassword {
		blocks, pemErr := pkcs12.ToPEM(pfx, storePassword)
		if pemErr != nil {
			return mobileSigningIdentityInput{}, mobileSigningSecretPayload{}, fmt.Errorf("inspect Android keystore alias: %w", pemErr)
		}
		for _, block := range blocks {
			if strings.Contains(block.Type, "PRIVATE KEY") && strings.TrimSpace(block.Headers["friendlyName"]) != "" {
				discoveredAlias = strings.TrimSpace(block.Headers["friendlyName"])
				break
			}
		}
	}
	if alias == "" {
		alias = defaultStr(discoveredAlias, "1")
	} else if discoveredAlias != "" && !strings.EqualFold(alias, discoveredAlias) {
		return mobileSigningIdentityInput{}, mobileSigningSecretPayload{}, fmt.Errorf(
			"Android keystore alias %q does not match contained key alias %q", alias, discoveredAlias,
		)
	}
	certPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}))
	sha1Fingerprint, sha256Fingerprint := certificateFingerprints(cert)
	return mobileSigningIdentityInput{
			ProjectID: projectID, Platform: "android", ApplicationIdentifier: packageName,
			Format: "pkcs12", Source: "imported", KeyAlias: alias,
			CertificatePEM: certPEM, CertificateSHA1: sha1Fingerprint,
			CertificateSHA256: sha256Fingerprint, ExpiresAt: cert.NotAfter.UTC().Format(time.RFC3339),
			ExternalStateJSON: `{}`,
		}, mobileSigningSecretPayload{
			KeystoreBase64: base64.StdEncoding.EncodeToString(pfx), StorePassword: storePassword,
			KeyPassword: keyPassword, KeyAlias: alias, CertificatePEM: certPEM,
		}, nil
}

func decodeAndroidPKCS12Key(pfx []byte, storePassword, keyPassword, alias string) (any, *x509.Certificate, error) {
	if keyPassword == "" || keyPassword == storePassword {
		privateKey, cert, _, err := pkcs12.DecodeChain(pfx, storePassword)
		return privateKey, cert, err
	}
	if strings.TrimSpace(alias) == "" {
		return nil, nil, errors.New("key_alias is required when the key password differs from the store password")
	}
	store, err := pkcs12.DecodeAll(pfx, []byte(storePassword), []pkcs12.Password{{
		FriendlyName: alias, Pass: []byte(keyPassword),
	}})
	if err != nil {
		return nil, nil, err
	}
	if len(store.Pairs) != 1 || store.Pairs[0] == nil || store.Pairs[0].PrvKey == nil || store.Pairs[0].Cert == nil {
		return nil, nil, errors.New("Android keystore must contain exactly one private key and certificate")
	}
	return store.Pairs[0].PrvKey, store.Pairs[0].Cert, nil
}

func (a *App) importAndroidSigningIdentity(d *Deployment, pfx []byte, storePassword, keyPassword, alias string, confirmReplace bool) (*MobileSigningIdentity, error) {
	if d == nil || d.TargetKind != "android" {
		return nil, errors.New("Android deployment required")
	}
	target, err := parseMobileTargetConfig(d.TargetConfigJSON)
	if err != nil {
		return nil, err
	}
	packageName := strings.TrimSpace(target.PackageName)
	if packageName == "" {
		return nil, errors.New("target_config_json.package_name is required")
	}
	if len(pfx) == 0 || strings.TrimSpace(storePassword) == "" {
		return nil, errors.New("keystore and store_password are required")
	}
	input, payload, err := androidSigningIdentityFromPKCS12(
		d.ProjectID, packageName, pfx, storePassword, keyPassword, alias,
	)
	if err != nil {
		return nil, err
	}
	a.mobileSigningMu.Lock()
	defer a.mobileSigningMu.Unlock()
	existing, err := dbGetMobileSigningIdentity(globalCtx.AppDB(), d.ProjectID, "android", "", packageName)
	if err != nil {
		return nil, err
	}
	if existing != nil && !confirmReplace {
		return nil, errors.New("an Android signing identity already exists; confirm_replace is required")
	}
	if existing == nil {
		return a.createMobileSigningIdentity(globalCtx.AppDB(), input, payload)
	}
	return a.replaceMobileSigningIdentity(globalCtx.AppDB(), existing, input, payload)
}
