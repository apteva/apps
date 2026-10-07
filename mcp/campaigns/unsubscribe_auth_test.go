package main

import (
	"database/sql"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

func TestOnlyUnsubscribeIsDeclaredPublic(t *testing.T) {
	yaml, err := os.ReadFile("apteva.yaml")
	if err != nil {
		t.Fatal(err)
	}
	fileManifest, err := sdk.ParseManifest(yaml)
	if err != nil {
		t.Fatal(err)
	}
	for name, manifest := range map[string]sdk.Manifest{
		"file": *fileManifest, "embedded": (&App{}).Manifest(),
	} {
		publicCount := 0
		for _, route := range manifest.Provides.HTTPRoutes {
			if route.NoAuth {
				publicCount++
				if route.Prefix != "/unsubscribe" {
					t.Errorf("%s manifest exposes %q", name, route.Prefix)
				}
			}
		}
		if publicCount != 1 {
			t.Errorf("%s manifest has %d public routes, want only unsubscribe", name, publicCount)
		}
	}
}

func TestAnonymousUnsubscribeThroughSidecarAuth(t *testing.T) {
	// Exercise the real SDK token gate, which direct handler tests bypass.
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = io.WriteString(w, `{"ok":true}`)
	}))
	defer gateway.Close()
	dbPath := filepath.Join(t.TempDir(), "campaigns.db")
	sidecar := tk.SpawnSidecar(t, ".", tk.WithEnv("DB_PATH", dbPath), tk.WithEnv("APTEVA_GATEWAY_URL", gateway.URL))
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	for _, query := range []string{
		`INSERT INTO campaigns (id, project_id, name, channel) VALUES (1, 'test-proj', 'Auth test', 'email')`,
		`INSERT INTO campaign_recipients (id, campaign_id, project_id, contact_id, address, status) VALUES (1, 1, 'test-proj', 1, 'test@example.test', 'sent')`,
	} {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	token := generateUnsubscribeToken()
	if err := dbUnsubscribeTokenCreate(db, "test-proj", 1, 1, token); err != nil {
		t.Fatal(err)
	}
	client := &http.Client{Timeout: 5 * time.Second}
	for _, tc := range []struct {
		path   string
		status int
		body   string
	}{
		{"/unsubscribe", http.StatusBadRequest, "missing token"},
		{"/unsubscribe?t=invalid", http.StatusNotFound, "invalid token"},
		{"/unsubscribe?t=" + token, http.StatusOK, "You're unsubscribed."},
		{"/unsubscribe?t=" + token, http.StatusOK, "You're unsubscribed."},
		{"/campaigns", http.StatusUnauthorized, "unauthorized"},
		{"/campaigns/1", http.StatusUnauthorized, "unauthorized"},
		{"/audience", http.StatusUnauthorized, "unauthorized"},
		{"/unsubscribe/extra?t=" + token, http.StatusUnauthorized, "unauthorized"},
		{"/mcp", http.StatusUnauthorized, "unauthorized"},
	} {
		res, err := client.Get(sidecar.URL() + tc.path)
		if err != nil {
			t.Fatal(err)
		}
		body, err := io.ReadAll(res.Body)
		res.Body.Close()
		if err != nil {
			t.Fatal(err)
		}
		if res.StatusCode != tc.status || !strings.Contains(string(body), tc.body) {
			t.Errorf("%s: status=%d body=%q, want status=%d containing %q", tc.path, res.StatusCode, body, tc.status, tc.body)
		}
	}
	var status, usedAt string
	if err := db.QueryRow(`SELECT status FROM campaign_recipients WHERE id = 1`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT COALESCE(used_at, '') FROM campaign_unsubscribe_tokens WHERE token = ?`, token).Scan(&usedAt); err != nil {
		t.Fatal(err)
	}
	if status != RecipUnsubscribed || usedAt == "" {
		t.Fatalf("recipient status=%q token used_at=%q", status, usedAt)
	}
}
