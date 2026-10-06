//go:build integration

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	tk "github.com/apteva/app-sdk/testkit"
)

// Real global Billing and Storage sidecars, linked by the platform callback
// protocol. Fetch from Storage anonymously to verify the URL's signature,
// rather than letting the testkit's authenticated proxy mask a broken link.
func TestSidecar_InvoiceStorageSignedSharing(t *testing.T) {
	identityGateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/apps/callback/whoami" {
			_ = json.NewEncoder(w).Encode(map[string]any{"app_name": "storage", "install_id": 16, "bindings": map[string]any{}})
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
	}))
	defer identityGateway.Close()
	storage := tk.SpawnSidecar(t, "../storage", tk.WithConfig(map[string]string{"default_visibility": "public"}), tk.WithEnv("APTEVA_GATEWAY_URL", identityGateway.URL))
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/apps/callback/apps/storage/call" {
			http.NotFound(w, r)
			return
		}
		var call struct {
			Tool  string         `json:"tool"`
			Input map[string]any `json:"input"`
		}
		if err := json.NewDecoder(r.Body).Decode(&call); err != nil {
			http.Error(w, err.Error(), 400)
			return
		}
		data, err := json.Marshal(map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": call.Tool, "arguments": call.Input}})
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		req, err := http.NewRequestWithContext(r.Context(), "POST", storage.URL()+"/mcp", bytes.NewReader(data))
		if err != nil {
			http.Error(w, err.Error(), 500)
			return
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+storage.Token())
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			http.Error(w, err.Error(), 502)
			return
		}
		defer resp.Body.Close()
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	defer gateway.Close()
	billing := tk.SpawnSidecar(t, ".", tk.WithEnv("APTEVA_GATEWAY_URL", gateway.URL))
	customer := billing.MCP("customers_upsert_by_email", map[string]any{"_project_id": "pdf-project", "email": "pdf@example.test", "defaults": map[string]any{"name": "PDF Customer"}})["customer"].(map[string]any)
	invoice := billing.MCP("invoices_create", map[string]any{"_project_id": "pdf-project", "customer_id": customer["id"], "line_items": []any{map[string]any{"description": "Service", "quantity": 1, "unit_price_cents": 1000}}})["invoice"].(map[string]any)
	billing.MCP("invoices_finalize", map[string]any{"_project_id": "pdf-project", "invoice_id": invoice["id"]})
	result := billing.MCP("invoices_render_pdf", map[string]any{"_project_id": "pdf-project", "invoice_id": invoice["id"], "save_to_storage": true})
	if result["saved"] != true || result["shareable"] != true {
		t.Fatalf("sharing failed: %#v", result)
	}
	expiry := int64(result["expires_at"].(float64))
	if expiry <= time.Now().Unix() {
		t.Fatalf("expired link: %d", expiry)
	}
	signed, err := url.Parse(result["url"].(string))
	if err != nil {
		t.Fatal(err)
	}
	if signed.Query().Get("sig") == "" || signed.Query().Get("project_id") != "pdf-project" || signed.Query().Get("install_id") != "16" {
		t.Fatalf("signature or project missing: %s", signed)
	}
	// Resolve the platform prefix to the real storage sidecar. The platform
	// authenticates its internal hop with the sidecar token, but anonymous
	// signed requests carry no trusted user identity (X-User-ID).
	unsignedPath := strings.TrimPrefix(signed.Path, "/api/apps/storage")
	fetch := func(q url.Values) (int, []byte) {
		t.Helper()
		req, err := http.NewRequest("GET", storage.URL()+unsignedPath+"?"+q.Encode(), nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+storage.Token())
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, err := io.ReadAll(resp.Body)
		if err != nil {
			t.Fatal(err)
		}
		return resp.StatusCode, data
	}
	status, data := fetch(signed.Query())
	if status != 200 || !bytes.HasPrefix(data, []byte("%PDF-")) || len(data) != int(result["size_bytes"].(float64)) {
		t.Fatalf("signed PDF failed: status=%d size=%d", status, len(data))
	}
	q := signed.Query()
	q.Del("sig")
	q.Del("exp")
	if status, _ := fetch(q); status != http.StatusForbidden {
		t.Fatalf("private PDF accessible without signature: %d", status)
	}
	q = signed.Query()
	q.Set("sig", "tampered")
	if status, _ := fetch(q); status != http.StatusForbidden {
		t.Fatalf("tampered signature accepted: %d", status)
	}
	q = signed.Query()
	q.Set("exp", "1")
	if status, _ := fetch(q); status != http.StatusForbidden {
		t.Fatalf("expired signature accepted: %d", status)
	}
	metadata := storage.MCP("files_get", map[string]any{"_project_id": "pdf-project", "id": result["file_id"]})
	file, ok := metadata["file"].(map[string]any)
	if !ok {
		t.Fatalf("stored invoice missing: %#v", metadata)
	}
	if file["visibility"] != "private" || file["project_id"] != "pdf-project" {
		t.Fatalf("invoice privacy or project scope lost: %#v", file)
	}
}
