package main

import (
	"archive/zip"
	"bytes"
	"encoding/base64"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestEnsureAndroidBundleSignedVerifiesManagedCertificate(t *testing.T) {
	for _, tool := range []string{"jarsigner", "keytool"} {
		if _, err := exec.LookPath(tool); err != nil {
			t.Skip(tool + " is unavailable")
		}
	}
	input, payload, err := generateAndroidSigningIdentity("p1", "com.example.android")
	if err != nil {
		t.Fatal(err)
	}
	bundlePath := filepath.Join(t.TempDir(), "app.aab")
	file, err := os.Create(bundlePath)
	if err != nil {
		t.Fatal(err)
	}
	writer := zip.NewWriter(file)
	entry, err := writer.Create("base/manifest/AndroidManifest.xml")
	if err != nil {
		t.Fatal(err)
	}
	_, _ = entry.Write([]byte("fixture"))
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
	fields := map[string]string{
		"upload_keystore_base64":    payload.KeystoreBase64,
		"upload_key_alias":          payload.KeyAlias,
		"upload_keystore_password":  payload.StorePassword,
		"upload_key_password":       payload.KeyPassword,
		"upload_certificate_sha256": input.CertificateSHA256,
	}
	var log bytes.Buffer
	if err := ensureAndroidBundleSigned(bundlePath, true, &log, fields); err != nil {
		t.Fatalf("sign managed bundle: %v\n%s", err, log.String())
	}
	if !androidBundleHasSignature(bundlePath) {
		t.Fatal("signed bundle has no JAR signature")
	}
	if !strings.Contains(log.String(), "signed and verified") {
		t.Fatalf("verification was not logged: %s", log.String())
	}

	wrong := make([]byte, 32)
	wrongFingerprint := formatCertificateFingerprint(wrong)
	fields["upload_certificate_sha256"] = wrongFingerprint
	if err := ensureAndroidBundleSigned(bundlePath, true, &bytes.Buffer{}, fields); err == nil ||
		!strings.Contains(err.Error(), "expected managed upload certificate") {
		t.Fatalf("wrong signer was not rejected: %v", err)
	}

	// The generated keystore itself remains opaque to non-secret responses.
	keystore, err := base64.StdEncoding.DecodeString(payload.KeystoreBase64)
	if err != nil || len(keystore) == 0 {
		t.Fatal("generated keystore is invalid")
	}
}
