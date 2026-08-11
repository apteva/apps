package main

import (
	"bytes"
	"testing"
)

func TestMobileSigningIdentityEncryptedAndDurable(t *testing.T) {
	db := openSchemaDB(t)
	defer db.Close()

	deployment, err := dbCreateDeployment(db, "project-1", CreateDeploymentInput{
		Name: "android-app", SourceKind: "local", SourceRef: "/tmp/android", Framework: "android",
	})
	if err != nil {
		t.Fatal(err)
	}
	dataDir := t.TempDir()
	app := &App{dataDir: dataDir}
	payload := mobileSigningSecretPayload{
		KeystoreBase64: "sensitive-keystore", StorePassword: "store-secret",
		KeyPassword: "key-secret", KeyAlias: "upload",
	}
	identity, err := app.createMobileSigningIdentity(db, mobileSigningIdentityInput{
		ProjectID: "project-1", Platform: "android", ApplicationIdentifier: "com.example.app",
		Format: "pkcs12", Source: "generated", KeyAlias: "upload",
	}, payload)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(identity.EncryptedPayload, []byte(payload.StorePassword)) ||
		bytes.Contains(identity.EncryptedPayload, []byte(payload.KeystoreBase64)) {
		t.Fatal("encrypted payload contains plaintext signing material")
	}

	// A restarted Deploy process can decrypt the identity with the persisted
	// DataDir key, while the identity is independent of deployment lifecycle.
	restarted := &App{dataDir: dataDir}
	decoded, err := restarted.decryptMobileSigningPayload(identity)
	if err != nil {
		t.Fatal(err)
	}
	if decoded.StorePassword != payload.StorePassword || decoded.KeystoreBase64 != payload.KeystoreBase64 {
		t.Fatalf("decoded payload mismatch: %#v", decoded)
	}
	if err := dbDeleteDeployment(db, "project-1", deployment.ID); err != nil {
		t.Fatal(err)
	}
	stillStored, err := dbGetMobileSigningIdentity(db, "project-1", "android", "", "com.example.app")
	if err != nil || stillStored == nil || stillStored.ID != identity.ID {
		t.Fatalf("identity did not survive deployment deletion: identity=%#v err=%v", stillStored, err)
	}
}

func TestMobileSigningIdentityRejectsScopeTampering(t *testing.T) {
	db := openSchemaDB(t)
	defer db.Close()
	app := &App{dataDir: t.TempDir()}
	identity, err := app.createMobileSigningIdentity(db, mobileSigningIdentityInput{
		ProjectID: "project-1", Platform: "ios", AuthorityScope: "issuer-1",
		ApplicationIdentifier: "com.example.app", Format: "pem", Source: "generated",
	}, mobileSigningSecretPayload{PrivateKeyPEM: "private-key"})
	if err != nil {
		t.Fatal(err)
	}
	identity.ApplicationIdentifier = "com.example.other"
	if _, err := app.decryptMobileSigningPayload(identity); err == nil {
		t.Fatal("expected authenticated scope change to prevent decryption")
	}
}
