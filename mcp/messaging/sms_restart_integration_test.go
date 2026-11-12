//go:build integration

package main

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"database/sql"
	"encoding/base64"
	"encoding/json"
	"fmt"
	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestSidecar_SMSPersistsAcrossRestartAndDeduplicatesRedelivery(t *testing.T) {
	pub, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/apps/callback/whoami":
			json.NewEncoder(w).Encode(sdk.InstallIdentity{AppName: "messaging", InstallID: 7, ProjectID: "test-proj", PublicURL: "https://example.com", Bindings: map[string]any{"sms_provider": map[string]any{"ids": []any{147}, "default_id": 147}}})
		case "/api/apps/callback/connections/147":
			json.NewEncoder(w).Encode(sdk.PlatformConnection{ID: 147, AppSlug: "telnyx", Status: "active"})
		case "/api/apps/callback/connections/147/credentials":
			json.NewEncoder(w).Encode(sdk.ConnectionCredentials{ConnectionID: 147, Slug: "telnyx", Fields: map[string]string{"public_key": base64.StdEncoding.EncodeToString(pub)}})
		default:
			json.NewEncoder(w).Encode(map[string]any{"ok": true})
		}
	}))
	defer gateway.Close()
	dbPath := filepath.Join(t.TempDir(), "sms.db")
	options := []tk.Option{tk.WithProjectID("test-proj"), tk.WithEnv("DB_PATH", dbPath), tk.WithEnv("APTEVA_GATEWAY_URL", gateway.URL)}
	first := tk.SpawnSidecar(t, ".", options...)
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	if _, err := dbUpsertSender(db, &senderUpsert{ProjectID: "test-proj", Channel: "sms", Address: "+15550000147", Kind: "phone", Provider: "telnyx", ProviderConnectionID: 147, ProviderIdentityID: "phone-1", Verified: true, SendingEnabled: true, InboundConfig: string(mustJSON(map[string]string{"messaging_profile_id": "profile-1"}))}); err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(`UPDATE senders SET outbound_enabled=0`); err != nil {
		t.Fatal(err)
	}
	raw := smsEvent("persistent-sms", "profile-1", "+15550000147", "TikTok", "Your complete code is 001234")
	deliver := func(sc *tk.Sidecar) {
		t.Helper()
		stamp := fmt.Sprint(time.Now().Unix())
		req, err := http.NewRequest(http.MethodPost, sc.URL()+"/webhooks/telnyx-inbound?connection_id=147", bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("telnyx-timestamp", stamp)
		req.Header.Set("telnyx-signature-ed25519", base64.StdEncoding.EncodeToString(ed25519.Sign(key, append([]byte(stamp+"|"), raw...))))
		response, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		if response.StatusCode != 200 {
			t.Fatalf("webhook status=%d", response.StatusCode)
		}
	}
	deliver(first)
	first.Stop()
	second := tk.SpawnSidecar(t, ".", options...)
	deliver(second)
	out := second.MCP("message_list", map[string]any{"channel": "sms"})
	rows := out["messages"].([]any)
	if len(rows) != 1 || !strings.Contains(rows[0].(map[string]any)["body_text"].(string), "001234") {
		t.Fatalf("restart lost or duplicated SMS: %+v", out)
	}
	id := rows[0].(map[string]any)["id"]
	second.MCP("inbound_redispatch", map[string]any{"id": id})
	var status string
	if err := db.QueryRow(`SELECT route_status FROM messages WHERE provider_message_id='persistent-sms'`).Scan(&status); err != nil || status != "inbox_only" {
		t.Fatalf("restart did not resume inbox-only processing: %s %v", status, err)
	}
}
