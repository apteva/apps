package main

import (
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	sdk "github.com/apteva/app-sdk"
	"strings"
	"testing"
)

func didwwTestKeys(t *testing.T) ([]*rsa.PrivateKey, []string) {
	t.Helper()
	keys, public := make([]*rsa.PrivateKey, 2), make([]string, 2)
	for i := range keys {
		var err error
		keys[i], err = rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			t.Fatal(err)
		}
		der, err := x509.MarshalPKIXPublicKey(&keys[i].PublicKey)
		if err != nil {
			t.Fatal(err)
		}
		public[i] = string(pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der}))
	}
	return keys, public
}
func TestDIDWWEncryptionMatchesDocumentedFormat(t *testing.T) {
	keys, public := didwwTestKeys(t)
	for _, content := range [][]byte{[]byte("%PDF-1.4 test document"), bytes.Repeat([]byte{0xab}, 32)} {
		encrypted, fingerprint, err := encryptDIDWWDocument(content, public)
		if err != nil {
			t.Fatal(err)
		}
		first, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, keys[0], encrypted[:256], nil)
		if err != nil {
			t.Fatal(err)
		}
		second, err := rsa.DecryptOAEP(sha256.New(), rand.Reader, keys[1], encrypted[256:512], nil)
		if err != nil || !bytes.Equal(first, second) || len(first) != 48 {
			t.Fatalf("invalid dual key envelope: %v", err)
		}
		block, _ := aes.NewCipher(first[:32])
		decrypted := append([]byte(nil), encrypted[512:]...)
		cipher.NewCBCDecrypter(block, first[32:]).CryptBlocks(decrypted, decrypted)
		padding := int(decrypted[len(decrypted)-1])
		if padding < 1 || padding > 16 || !bytes.Equal(decrypted[len(decrypted)-padding:], bytes.Repeat([]byte{byte(padding)}, padding)) || !bytes.Equal(decrypted[:len(decrypted)-padding], content) {
			t.Fatal("document did not round trip with PKCS7 padding")
		}
		expected := []string{}
		for _, key := range public {
			block, _ := pem.Decode([]byte(key))
			sum := sha1.Sum(block.Bytes)
			expected = append(expected, hex.EncodeToString(sum[:]))
		}
		if fingerprint != strings.Join(expected, ":::") {
			t.Fatal("fingerprints do not match public keys")
		}
		again, _, _ := encryptDIDWWDocument(content, public)
		if bytes.Equal(encrypted, again) {
			t.Fatal("encryption reused random key/IV")
		}
	}
	for _, keys := range [][]string{nil, {public[0]}, {public[0], "invalid"}} {
		if _, _, err := encryptDIDWWDocument([]byte("x"), keys); err == nil {
			t.Fatal("invalid keys accepted")
		}
	}
}
func TestDIDWWProofUploadEncryptsBeforeIntegrationAndAttaches(t *testing.T) {
	_, public := didwwTestKeys(t)
	keyJSON, _ := json.Marshal(map[string]any{"data": []any{map[string]any{"attributes": map[string]any{"key": public[0]}}, map[string]any{"attributes": map[string]any{"key": public[1]}}}})
	platform := &answerPlatform{bindings: map[string]any{"carrier": int64(19)}, credentials: &sdk.ConnectionCredentials{Slug: "didww", Fields: map[string]string{}}, integrationResponse: map[string]json.RawMessage{"get_public_keys": keyJSON, "create_encrypted_file": json.RawMessage(`{"ids":["file-1"]}`), "create_identity_proof": json.RawMessage(`{"data":{"id":"proof-1"}}`), "create_address_proof": json.RawMessage(`{"data":{"id":"proof-2"}}`)}}
	app, ctx := withTelephonyTestContext(t, platform)
	content := []byte("%PDF-1.4 private business document")
	result, err := app.complianceRequirementSet(ctx, map[string]any{"kind": "document", "identity_id": "identity-1", "proof_type_id": "type-1", "file": base64.StdEncoding.EncodeToString(content), "file_name": "proof.pdf"})
	if err != nil || result["proof"].(map[string]any)["id"] != "proof-1" {
		t.Fatalf("proof result: %#v %v", result, err)
	}
	if len(platform.integrationCalls) != 3 {
		t.Fatalf("unexpected operations: %#v", platform.integrationCalls)
	}
	upload := platform.integrationCalls[1]
	if upload.Tool != "create_encrypted_file" {
		t.Fatalf("wrong operation: %#v", upload)
	}
	encrypted, _ := base64.StdEncoding.DecodeString(upload.Input["file"].(string))
	if bytes.Contains(encrypted, content) || len(encrypted) < 528 || upload.Input["encrypted_files[encryption_fingerprint]"] == "" {
		t.Fatal("plaintext or missing encryption metadata")
	}
	proof := platform.integrationCalls[2]
	if proof.Tool != "create_identity_proof" || proof.Input["identity_id"] != "identity-1" || proof.Input["proof_type_id"] != "type-1" {
		t.Fatalf("wrong proof target: %#v", proof)
	}
	_, err = app.complianceRequirementSet(ctx, map[string]any{"kind": "document", "address_id": "address-1", "proof_type_id": "type-2", "file_ids": []string{"file-1"}})
	if err != nil || len(platform.integrationCalls) != 4 || platform.integrationCalls[3].Tool != "create_address_proof" {
		t.Fatalf("reuse must not reupload: %v", err)
	}
}
func TestDIDWWProofValidationFailsBeforeProviderMutation(t *testing.T) {
	platform := &answerPlatform{bindings: map[string]any{"carrier": int64(19)}, credentials: &sdk.ConnectionCredentials{Slug: "didww", Fields: map[string]string{}}, integrationResponse: map[string]json.RawMessage{"get_public_keys": json.RawMessage(`{"data":[]}`)}}
	app, ctx := withTelephonyTestContext(t, platform)
	for _, args := range []map[string]any{
		{"identity_id": "i", "address_id": "a", "proof_type_id": "p", "file_ids": []string{"f"}}, {"identity_id": "i", "file_ids": []string{"f"}}, {"identity_id": "i", "proof_type_id": "p", "file": "not base64"}, {"identity_id": "i", "proof_type_id": "p", "file": base64.StdEncoding.EncodeToString([]byte("not PDF")), "file_name": "proof.pdf"}, {"identity_id": "i", "proof_type_id": "p", "file": base64.StdEncoding.EncodeToString([]byte("%PDF-1.4 private")), "file_name": "proof.pdf"},
	} {
		if _, err := app.didwwProofCreate(ctx, args); err == nil {
			t.Fatalf("invalid proof accepted: %#v", args)
		}
	}
	for _, call := range platform.integrationCalls {
		if call.Tool != "get_public_keys" {
			t.Fatalf("mutated provider after failure: %s", call.Tool)
		}
	}
	platform.integrationCalls = nil
	platform.credentials.Slug = "twilio"
	if _, err := app.didwwIdentityGet(ctx, map[string]any{"identity_id": "i"}); err == nil || len(platform.integrationCalls) != 0 {
		t.Fatal("DIDWW operation escaped provider boundary")
	}
}
func TestDIDWWIdentityGetReturnsCompliancePanelShape(t *testing.T) {
	platform := &answerPlatform{bindings: map[string]any{"carrier": int64(19)}, credentials: &sdk.ConnectionCredentials{Slug: "didww", Fields: map[string]string{}}, integrationResponse: map[string]json.RawMessage{"get_identity": json.RawMessage(`{"data":{"id":"i","attributes":{"identity_type":"Business","company_name":"Example"}},"included":[{"id":"proof-1","type":"proofs"}]}`)}}
	app, ctx := withTelephonyTestContext(t, platform)
	result, err := app.complianceProfileGet(ctx, map[string]any{"compliance_id": "i", "resource_kind": "identity"})
	if err != nil {
		t.Fatal(err)
	}
	profile, ok := result["bundle"].(map[string]any)
	if !ok || profile["sid"] != "i" || profile["resource_kind"] != "identity" || len(result["items"].([]any)) != 1 {
		t.Fatalf("panel cannot display identity: %#v", result)
	}
}
