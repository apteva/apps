package main

import (
	"archive/zip"
	"bytes"
	"crypto"
	"crypto/x509"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	sdk "github.com/apteva/app-sdk"
	pkcs12 "github.com/ggpslop/go-pkcs12"
)

// Imported profiles are intentionally discarded: the selected app gets its own profile.
func appleSigningMaterial(data []byte, password string) (mobileSigningSecretPayload, *x509.Certificate, error) {
	var key any
	var cert *x509.Certificate
	if bytes.HasPrefix(data, []byte("PK\x03\x04")) {
		archive, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			return mobileSigningSecretPayload{}, nil, errors.New("invalid signing recovery archive")
		}
		files := map[string][]byte{}
		for _, file := range archive.File {
			if file.Name != "certificate.pem" && file.Name != "distribution-private-key.pem" {
				continue
			}
			if _, duplicate := files[file.Name]; duplicate {
				return mobileSigningSecretPayload{}, nil, errors.New("duplicate signing recovery entry")
			}
			reader, err := file.Open()
			if err != nil {
				return mobileSigningSecretPayload{}, nil, err
			}
			content, readErr := io.ReadAll(io.LimitReader(reader, maxMobileSigningImportBytes+1))
			reader.Close()
			if readErr != nil || len(content) > maxMobileSigningImportBytes {
				return mobileSigningSecretPayload{}, nil, errors.New("oversized signing recovery entry")
			}
			files[file.Name] = content
		}
		certBlock, _ := pem.Decode(files["certificate.pem"])
		keyBlock, _ := pem.Decode(files["distribution-private-key.pem"])
		if certBlock == nil || keyBlock == nil {
			return mobileSigningSecretPayload{}, nil, errors.New("recovery archive requires certificate.pem and distribution-private-key.pem")
		}
		cert, err = x509.ParseCertificate(certBlock.Bytes)
		if err != nil {
			return mobileSigningSecretPayload{}, nil, errors.New("invalid signing certificate")
		}
		key, err = x509.ParsePKCS8PrivateKey(keyBlock.Bytes)
		if err != nil {
			key, err = x509.ParsePKCS1PrivateKey(keyBlock.Bytes)
		}
		if err != nil {
			key, err = x509.ParseECPrivateKey(keyBlock.Bytes)
		}
		if err != nil {
			return mobileSigningSecretPayload{}, nil, errors.New("invalid signing private key")
		}
	} else {
		var err error
		key, cert, _, err = pkcs12.DecodeChain(data, password)
		if err != nil {
			return mobileSigningSecretPayload{}, nil, errors.New("cannot decode signing PKCS#12; check its password")
		}
	}
	signer, ok := key.(crypto.Signer)
	if !ok || cert == nil {
		return mobileSigningSecretPayload{}, nil, errors.New("certificate and private signing key are required")
	}
	public, err := x509.MarshalPKIXPublicKey(signer.Public())
	if err != nil {
		return mobileSigningSecretPayload{}, nil, err
	}
	if !bytes.Equal(public, cert.RawSubjectPublicKeyInfo) {
		return mobileSigningSecretPayload{}, nil, errors.New("private key does not match signing certificate")
	}
	now := time.Now()
	if now.Before(cert.NotBefore) || !cert.NotAfter.After(now.Add(24*time.Hour)) {
		return mobileSigningSecretPayload{}, nil, errors.New("signing certificate is not valid for the next 24 hours")
	}
	if cert.IsCA || cert.KeyUsage&x509.KeyUsageDigitalSignature == 0 {
		return mobileSigningSecretPayload{}, nil, errors.New("certificate cannot sign application code")
	}
	privateDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		return mobileSigningSecretPayload{}, nil, err
	}
	return mobileSigningSecretPayload{PrivateKeyPEM: string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: privateDER})), CertificatePEM: string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: cert.Raw}))}, cert, nil
}

func appleRegisteredCertificate(bound *sdk.BoundIntegration, cert *x509.Certificate, kind string) (string, error) {
	raw, err := executeIntegration(bound, "list_certificates", map[string]any{"certificate_type": kind, "limit": 200})
	if err != nil {
		return "", fmt.Errorf("validate imported Apple certificate: %w", err)
	}
	var document struct {
		Data []struct {
			ID         string `json:"id"`
			Attributes struct {
				Content string `json:"certificateContent"`
			} `json:"attributes"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &document); err != nil {
		return "", errors.New("decode Apple certificates")
	}
	for _, item := range document.Data {
		der, err := base64.StdEncoding.DecodeString(item.Attributes.Content)
		if err == nil && bytes.Equal(der, cert.Raw) && item.ID != "" {
			return item.ID, nil
		}
	}
	return "", errors.New("signing certificate is not active in the selected Apple account for this platform")
}

func (a *App) inspectAppleSigningImport(d *Deployment, data []byte, password string) (mobileSigningIdentityInput, mobileSigningSecretPayload, error) {
	empty := mobileSigningIdentityInput{}
	platform, ok := appPlatformFor(d.TargetKind)
	if !ok || platform.ApplePlatform == "" {
		return empty, mobileSigningSecretPayload{}, errors.New("Apple deployment required")
	}
	target, err := parseMobileTargetConfig(d.TargetConfigJSON)
	if err != nil {
		return empty, mobileSigningSecretPayload{}, err
	}
	if strings.TrimSpace(target.BundleID) == "" {
		return empty, mobileSigningSecretPayload{}, errors.New("bundle_id is required")
	}
	setups, err := dbListMobileSigningSetups(globalCtx.AppDB(), d.ID, d.EnvironmentID)
	if err != nil {
		return empty, mobileSigningSecretPayload{}, err
	}
	for _, setup := range setups {
		if setup.Status == mobileSigningStatusReady && setup.BundleID != strings.TrimSpace(target.BundleID) {
			return empty, mobileSigningSecretPayload{}, errors.New("bundle_id is immutable after signing is configured; create a new deployment")
		}
	}
	payload, cert, err := appleSigningMaterial(data, password)
	if err != nil {
		return empty, payload, err
	}
	bound, err := selectedIntegration("app_store", d.TargetConfigJSON)
	if err != nil {
		return empty, payload, err
	}
	credentials, err := globalCtx.PlatformAPI().GetConnectionCredentials(bound.ConnectionID)
	if err != nil {
		return empty, payload, err
	}
	issuer := strings.TrimSpace(credentials.Fields["issuer_id"])
	if issuer == "" {
		return empty, payload, errors.New("selected Apple account has no issuer_id")
	}
	certificateID, err := appleRegisteredCertificate(bound, cert, platform.Certificate)
	if err != nil {
		return empty, payload, err
	}
	one, two := certificateFingerprints(cert)
	state, _ := json.Marshal(iosSigningIdentityState{IssuerID: issuer, CertificateID: certificateID, KeyFingerprint: two})
	return mobileSigningIdentityInput{ProjectID: d.ProjectID, Platform: d.TargetKind, AuthorityScope: issuer, ApplicationIdentifier: strings.TrimSpace(target.BundleID), Format: "pem", Source: "imported", CertificatePEM: payload.CertificatePEM, CertificateSHA1: one, CertificateSHA256: two, ExpiresAt: cert.NotAfter.UTC().Format(time.RFC3339), ExternalStateJSON: string(state)}, payload, nil
}

func (a *App) httpAppleSigningImport(w http.ResponseWriter, r *http.Request, d *Deployment, data []byte) {
	input, payload, err := a.inspectAppleSigningImport(d, data, r.FormValue("store_password"))
	if err != nil {
		httpErr(w, http.StatusBadRequest, err.Error())
		return
	}
	a.mobileSigningMu.Lock()
	existing, err := dbGetMobileSigningIdentity(globalCtx.AppDB(), input.ProjectID, input.Platform, input.AuthorityScope, input.ApplicationIdentifier)
	if err != nil {
		a.mobileSigningMu.Unlock()
		httpErr(w, 500, err.Error())
		return
	}
	replacement := existing != nil && existing.CertificateSHA256 != input.CertificateSHA256
	if strings.EqualFold(r.FormValue("inspect_only"), "true") {
		a.mobileSigningMu.Unlock()
		revision := 1
		if existing != nil {
			revision = existing.Revision
			if replacement {
				revision++
			}
		}
		httpJSON(w, map[string]any{"identity": MobileSigningIdentity{ProjectID: input.ProjectID, Platform: input.Platform, AuthorityScope: input.AuthorityScope, ApplicationIdentifier: input.ApplicationIdentifier, Format: input.Format, Source: input.Source, Revision: revision, CertificateSHA1: input.CertificateSHA1, CertificateSHA256: input.CertificateSHA256, ExpiresAt: input.ExpiresAt}, "replacement_required": replacement})
		return
	}
	if replacement && !strings.EqualFold(r.FormValue("confirm_replace"), "true") {
		a.mobileSigningMu.Unlock()
		httpErr(w, 400, "an Apple signing identity already exists; confirm_replace is required")
		return
	}
	identity := existing
	if existing == nil {
		identity, err = a.createMobileSigningIdentity(globalCtx.AppDB(), input, payload)
	} else if replacement {
		identity, err = a.replaceMobileSigningIdentity(globalCtx.AppDB(), existing, input, payload)
	}
	a.mobileSigningMu.Unlock()
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	result, err := a.setupMobileSigning(r.Context(), d, d.BuildBackend, false)
	if err != nil {
		httpErr(w, 400, err.Error())
		return
	}
	emit("deploy.mobile_signing.imported", map[string]any{"deployment_id": d.ID, "environment_id": d.EnvironmentID, "platform": d.TargetKind, "identity_id": identity.ID, "revision": identity.Revision})
	httpJSON(w, result)
}

// Certificates are account resources; profiles remain application resources.
func (a *App) reuseAppleDistributionIdentity(d *Deployment, issuer, bundle string, bound *sdk.BoundIntegration, kind string) (*MobileSigningIdentity, error) {
	rows, err := globalCtx.AppDB().Query(`SELECT `+mobileSigningIdentityColumns+` FROM mobile_signing_identities WHERE project_id=? AND platform=? AND authority_scope=? ORDER BY updated_at DESC, id DESC`, d.ProjectID, d.TargetKind, issuer)
	if err != nil {
		return nil, err
	}
	var candidates []*MobileSigningIdentity
	for rows.Next() {
		identity, err := scanMobileSigningIdentity(rows)
		if err != nil {
			rows.Close()
			return nil, err
		}
		candidates = append(candidates, identity)
	}
	err = rows.Err()
	rows.Close()
	if err != nil {
		return nil, err
	}
	for _, candidate := range candidates {
		block, _ := pem.Decode([]byte(candidate.CertificatePEM))
		if block == nil {
			continue
		}
		cert, err := x509.ParseCertificate(block.Bytes)
		if err != nil || !cert.NotAfter.After(time.Now().Add(24*time.Hour)) || time.Now().Before(cert.NotBefore) {
			continue
		}
		state := iosSigningIdentityStateFrom(candidate)
		available, err := appleCertificateAvailable(bound, state.CertificateID, kind)
		if err != nil {
			return nil, err
		}
		if !available {
			continue
		}
		id, err := appleRegisteredCertificate(bound, cert, kind)
		if err != nil {
			return nil, err
		}
		if id != state.CertificateID {
			return nil, errors.New("stored Apple certificate does not match its resource")
		}
		payload, err := a.decryptMobileSigningPayload(candidate)
		if err != nil {
			return nil, err
		}
		if strings.TrimSpace(payload.PrivateKeyPEM) == "" || payload.CertificatePEM != candidate.CertificatePEM {
			continue
		}
		payload.ProvisioningProfileBase64 = ""
		nextState, _ := json.Marshal(iosSigningIdentityState{IssuerID: issuer, CertificateID: state.CertificateID, KeyFingerprint: state.KeyFingerprint})
		return a.createMobileSigningIdentity(globalCtx.AppDB(), mobileSigningIdentityInput{ProjectID: d.ProjectID, Platform: d.TargetKind, AuthorityScope: issuer, ApplicationIdentifier: bundle, Format: candidate.Format, Source: candidate.Source, CertificatePEM: candidate.CertificatePEM, CertificateSHA1: candidate.CertificateSHA1, CertificateSHA256: candidate.CertificateSHA256, ExpiresAt: candidate.ExpiresAt, ExternalStateJSON: string(nextState)}, *payload)
	}
	return nil, nil
}
