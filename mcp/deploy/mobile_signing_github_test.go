package main

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"golang.org/x/crypto/nacl/box"
)

func TestGitHubSigningSecretsAreSealedAndScoped(t *testing.T) {
	public, private, err := box.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	values := map[string]string{"CERTIFICATE_PRIVATE_KEY": "private signing key", "APTEVA_CERTIFICATE_PEM": "public certificate"}
	received := map[string]string{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing connection authentication")
		}
		if r.Method == http.MethodGet && r.URL.Path == "/public-key" {
			json.NewEncoder(w).Encode(map[string]string{"key_id": "key-1", "key": base64.StdEncoding.EncodeToString(public[:])})
			return
		}
		name := strings.TrimPrefix(r.URL.Path, "/APTEVA_SCOPE_")
		if values[name] == "" || r.Method != http.MethodPut {
			t.Errorf("unexpected signing-secret route %s", r.URL.Path)
			w.WriteHeader(400)
			return
		}
		body, _ := io.ReadAll(r.Body)
		if strings.Contains(string(body), values[name]) {
			t.Error("secret request contains plaintext")
		}
		var document struct {
			Value string `json:"encrypted_value"`
			ID    string `json:"key_id"`
		}
		json.Unmarshal(body, &document)
		ciphertext, _ := base64.StdEncoding.DecodeString(document.Value)
		plaintext, ok := box.OpenAnonymous(nil, ciphertext, public, private)
		if !ok || document.ID != "key-1" {
			t.Error("not a valid sealed box for selected repository")
		}
		received[name] = string(plaintext)
		w.WriteHeader(204)
	}))
	defer server.Close()
	if err := provisionGitHubSigningSecrets(t.Context(), server.Client(), server.URL+"/", "test-token", "APTEVA_SCOPE_", values); err != nil {
		t.Fatal(err)
	}
	for name, value := range values {
		if received[name] != value {
			t.Errorf("missing or incorrect %s", name)
		}
	}
}

func TestGitHubSigningFailureDoesNotExposeResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(403)
		w.Write([]byte("private key contents"))
	}))
	defer server.Close()
	err := provisionGitHubSigningSecrets(t.Context(), server.Client(), server.URL+"/", "token", "APTEVA_SCOPE_", map[string]string{"KEY": "private key contents"})
	if err == nil || strings.Contains(err.Error(), "private key contents") || !strings.Contains(err.Error(), "403") {
		t.Fatalf("unsafe or missing signing failure: %v", err)
	}
}

func TestEverySupportedBuildBackendHasManagedSigningAdapter(t *testing.T) {
	for _, name := range []string{buildBackendLocal, buildBackendRunner, buildBackendCodemagic, buildBackendBitrise, buildBackendAppcircle, buildBackendGitHubActions} {
		provider, err := mobileSigningProviderFor(name)
		if err != nil || provider.Name() != name {
			t.Errorf("missing signing adapter for %s: %v", name, err)
		}
	}
}
