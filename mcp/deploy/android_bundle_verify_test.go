package main

import (
	"archive/zip"
	"bytes"
	"crypto"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"go.mozilla.org/pkcs7"
)

func TestVerifyAndroidBundleSignaturePureGo(t *testing.T) {
	input, payload, err := generateAndroidSigningIdentity("p1", "com.example.android")
	if err != nil {
		t.Fatal(err)
	}
	privateKey, certificate := decodeGeneratedAndroidIdentity(t, payload)
	bundle := filepath.Join(t.TempDir(), "app.aab")
	writeTestSignedAAB(t, bundle, privateKey, certificate, []byte("payload"), []byte("payload"))

	fingerprint, err := verifyAndroidBundleSignaturePureGo(bundle, input.CertificateSHA256)
	if err != nil {
		t.Fatal(err)
	}
	if normalizeCertificateFingerprint(fingerprint) != normalizeCertificateFingerprint(input.CertificateSHA256) {
		t.Fatalf("fingerprint=%q want=%q", fingerprint, input.CertificateSHA256)
	}

	wrong := formatCertificateFingerprint(bytes.Repeat([]byte{0x42}, sha256.Size))
	if _, err := verifyAndroidBundleSignaturePureGo(bundle, wrong); err == nil ||
		!strings.Contains(err.Error(), "expected managed upload certificate") {
		t.Fatalf("wrong managed certificate was not rejected: %v", err)
	}
}

func TestVerifyAndroidBundleSignatureRejectsTamperedPayload(t *testing.T) {
	_, payload, err := generateAndroidSigningIdentity("p1", "com.example.android")
	if err != nil {
		t.Fatal(err)
	}
	privateKey, certificate := decodeGeneratedAndroidIdentity(t, payload)
	bundle := filepath.Join(t.TempDir(), "app.aab")
	writeTestSignedAAB(t, bundle, privateKey, certificate, []byte("tampered"), []byte("original"))

	_, fingerprint := certificateFingerprints(certificate)
	if _, err := verifyAndroidBundleSignaturePureGo(bundle, fingerprint); err == nil ||
		!strings.Contains(err.Error(), "payload digest does not match") {
		t.Fatalf("tampered payload was not rejected: %v", err)
	}
}

func decodeGeneratedAndroidIdentity(t *testing.T, payload mobileSigningSecretPayload) (crypto.PrivateKey, *x509.Certificate) {
	t.Helper()
	pfx, err := base64.StdEncoding.DecodeString(payload.KeystoreBase64)
	if err != nil {
		t.Fatal(err)
	}
	privateKey, certificate, err := decodeAndroidPKCS12Key(
		pfx, payload.StorePassword, payload.KeyPassword, payload.KeyAlias,
	)
	if err != nil {
		t.Fatal(err)
	}
	return privateKey, certificate
}

func writeTestSignedAAB(
	t *testing.T,
	path string,
	privateKey crypto.PrivateKey,
	certificate *x509.Certificate,
	payload, signedPayload []byte,
) {
	t.Helper()
	payloadDigest := sha256.Sum256(signedPayload)
	manifest := []byte(fmt.Sprintf(
		"Manifest-Version: 1.0\r\n\r\nName: base/manifest/AndroidManifest.xml\r\nSHA-256-Digest: %s\r\n\r\n",
		base64.StdEncoding.EncodeToString(payloadDigest[:]),
	))
	manifestDigest := sha256.Sum256(manifest)
	signature := []byte(fmt.Sprintf(
		"Signature-Version: 1.0\r\nSHA-256-Digest-Manifest: %s\r\n\r\n",
		base64.StdEncoding.EncodeToString(manifestDigest[:]),
	))
	signedData, err := pkcs7.NewSignedData(signature)
	if err != nil {
		t.Fatal(err)
	}
	if err := signedData.AddSigner(certificate, privateKey, pkcs7.SignerInfoConfig{}); err != nil {
		t.Fatal(err)
	}
	signedData.Detach()
	block, err := signedData.Finish()
	if err != nil {
		t.Fatal(err)
	}

	file, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	for _, entry := range []struct {
		name string
		body []byte
	}{
		{name: "base/manifest/AndroidManifest.xml", body: payload},
		{name: "META-INF/MANIFEST.MF", body: manifest},
		{name: "META-INF/APTEVA.SF", body: signature},
		{name: "META-INF/APTEVA.RSA", body: block},
	} {
		part, createErr := writer.Create(entry.name)
		if createErr != nil {
			t.Fatal(createErr)
		}
		if _, writeErr := part.Write(entry.body); writeErr != nil {
			t.Fatal(writeErr)
		}
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}
