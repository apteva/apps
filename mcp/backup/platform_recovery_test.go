package main

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
)

func TestPublishedSDKRecoveryAdapter(t *testing.T) {
	const pass = "portable recovery passphrase"
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Authorization") != "Bearer outbound-token" || r.Header.Get("X-Apteva-App-Install-ID") != "42" || r.Header.Get("X-Backup-Passphrase") != pass {
			t.Error("missing install authentication or recovery header")
		}
		if r.URL.RawQuery != "" {
			t.Error("unexpected query")
		}
		switch r.URL.Path {
		case "/api/apps/callback/platform/snapshot":
			if r.Method != http.MethodGet {
				t.Error("wrong snapshot method")
			}
			io.WriteString(w, "snapshot")
		case "/api/apps/callback/platform/restore":
			body, _ := io.ReadAll(r.Body)
			if r.Method != http.MethodPost || r.Header.Get("X-Confirm-Restore") != "yes" || r.Header.Get("Content-Type") != "application/gzip" || string(body) != "snapshot" || r.ContentLength != 8 {
				t.Error("invalid restore request")
			}
			io.WriteString(w, `{"restart_required":true}`)
		default:
			t.Error("unexpected management route")
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	t.Setenv("APTEVA_GATEWAY_URL", server.URL)
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "outbound-token")
	t.Setenv("APTEVA_APP_TOKEN", "inbound-token")
	t.Setenv("APTEVA_INSTALL_ID", "42")
	// This platform implements only the published streaming interface, forcing
	// the same recovery adapter used by installations built with SDK v0.77.0.
	manifest := (&App{}).Manifest()
	ctx := sdk.NewAppCtxForTest(&manifest, openTestDB(t), sdk.Config{"encryption_passphrase": pass}, &backupPlatform{}, silentLogger{})
	var out strings.Builder
	if n, err := streamSnapshot(context.Background(), ctx, &out); err != nil || n != 8 || out.String() != "snapshot" {
		t.Fatalf("snapshot n=%d err=%v", n, err)
	}
	if report, err := postRestore(ctx, []byte("snapshot")); err != nil || report["restart_required"] != true {
		t.Fatalf("restore report=%v err=%v", report, err)
	}
	if requests != 2 {
		t.Fatalf("requests=%d", requests)
	}
}

func TestRecoveryAdapterBoundaries(t *testing.T) {
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "")
	t.Setenv("APTEVA_APP_TOKEN", "")
	if _, err := platformRecoveryAPI(&backupPlatform{}); err == nil {
		t.Fatal("accepted missing token")
	}
	t.Setenv("APTEVA_APP_TOKEN", "install-token")
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("followed sensitive redirect") }))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusTemporaryRedirect)
	}))
	defer server.Close()
	t.Setenv("APTEVA_GATEWAY_URL", server.URL)
	api, err := platformRecoveryAPI(&backupPlatform{})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = api.OpenPlatformSnapshotWithPassphrase(context.Background(), "test passphrase"); err == nil || !strings.Contains(err.Error(), "http 307") {
		t.Fatalf("redirect err=%v", err)
	}
	if _, err = api.OpenPlatformSnapshotWithPassphrase(context.Background(), "test\npassphrase"); err == nil {
		t.Fatal("accepted line break")
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = api.OpenPlatformSnapshotWithPassphrase(canceled, "test passphrase"); err == nil {
		t.Fatal("ignored cancellation")
	}
}

func TestRecoveryAdapterBoundsRestoreResponse(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.WriteString(w, strings.Repeat("x", recoveryResponseLimit+1))
	}))
	defer server.Close()
	t.Setenv("APTEVA_GATEWAY_URL", server.URL)
	t.Setenv("APTEVA_OUTBOUND_TOKEN", "install-token")
	api, err := platformRecoveryAPI(&backupPlatform{})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	if _, err = api.RestorePlatformSnapshotWithPassphrase(ctx, strings.NewReader("snapshot"), -1, "test passphrase"); err == nil || !strings.Contains(err.Error(), "exceeds limit") {
		t.Fatalf("unbounded response: %v", err)
	}
}
