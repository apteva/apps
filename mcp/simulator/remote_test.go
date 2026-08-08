package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestWorkerHealthRequiresBearerAndReportsProtocol(t *testing.T) {
	worker := &workerServer{cfg: workerConfig{Token: "secret"}}
	server := httptest.NewServer(worker.auth(worker.handleHealth))
	defer server.Close()

	resp, err := http.Get(server.URL)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d", resp.StatusCode)
	}
	req, err := http.NewRequest(http.MethodGet, server.URL, nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "secret")
	resp, err = http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	_ = resp.Body.Close()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("raw-token status=%d", resp.StatusCode)
	}

	client := &remoteWorkerClient{
		baseURL: server.URL, token: "secret",
		http: &http.Client{Timeout: time.Second},
	}
	if err := client.health(context.Background()); err != nil {
		t.Fatalf("health: %v", err)
	}
}

func TestWorkerHealthRejectsVersionMismatch(t *testing.T) {
	handler := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]any{
			"ok": true, "protocol": workerProtocolVersion, "version": "old",
		})
	})
	server := httptest.NewServer(handler)
	defer server.Close()
	client := &remoteWorkerClient{baseURL: server.URL, token: "secret", http: server.Client()}
	err := client.health(context.Background())
	if err == nil || !strings.Contains(err.Error(), "want") {
		t.Fatalf("expected version mismatch, got %v", err)
	}
}

func TestSourceDirToTarGzB64RoundTrip(t *testing.T) {
	source := t.TempDir()
	if err := os.MkdirAll(filepath.Join(source, "app"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "app", "main.txt"), []byte("remote source"), 0o644); err != nil {
		t.Fatal(err)
	}
	encoded, err := sourceDirToTarGzB64(source)
	if err != nil {
		t.Fatal(err)
	}
	dest := t.TempDir()
	if _, err := extractSourceTarGz(encoded, dest); err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile(filepath.Join(dest, "app", "main.txt"))
	if err != nil || string(body) != "remote source" {
		t.Fatalf("round trip body=%q err=%v", body, err)
	}
}

func TestResolvedHostIDExplicitZeroOverridesConfiguredSelection(t *testing.T) {
	args := map[string]any{"host_id": float64(0)}
	if got := resolvedHostID(nil, args, "ios"); got != 0 {
		t.Fatalf("host id=%d, want local override", got)
	}
}

func TestNegativeHostIDIsRejected(t *testing.T) {
	app := &App{}
	if _, err := app.capabilitiesForHost(nil, -1); err == nil {
		t.Fatal("expected a negative host id to be rejected")
	}
}

func TestWorkerModuleRefValidation(t *testing.T) {
	for _, value := range []string{"simulator/v0.1.25", "main", "97035005b48e"} {
		if !validWorkerModuleRef(value) {
			t.Fatalf("expected %q to be valid", value)
		}
	}
	for _, value := range []string{"", "main;reboot", "$(id)", "tag with spaces"} {
		if validWorkerModuleRef(value) {
			t.Fatalf("expected %q to be invalid", value)
		}
	}
}
