//go:build integration

package main

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"
)

func TestSidecarPublicAndAdminSecurityBoundary(t *testing.T) {
	sc, cid := newSidecar(t)
	client := &http.Client{Timeout: 10 * time.Second}
	request := func(method, path string, body any, token string) (int, map[string]any, http.Header) {
		t.Helper()
		raw, _ := json.Marshal(body)
		req, err := http.NewRequest(method, sc.URL()+path, bytes.NewReader(raw))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		res, err := client.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		data, _ := io.ReadAll(res.Body)
		var out map[string]any
		_ = json.Unmarshal(data, &out)
		return res.StatusCode, out, res.Header
	}
	for _, path := range []string{"/admin/users", "/admin/organizations", "/admin/clients"} {
		code, _, _ := request("GET", path, nil, "")
		if code != 401 {
			t.Fatalf("unauthenticated %s = %d", path, code)
		}
	}
	code, out, headers := request("POST", "/signup", map[string]any{"client_id": cid, "email": "public@example.com", "password": "GoodPassword123"}, "")
	if code != 201 {
		t.Fatalf("public signup %d %v", code, out)
	}
	if headers.Get("Cache-Control") != "no-store" {
		t.Fatal("token response may be cached")
	}
	access := out["access_token"].(string)
	refresh := out["refresh_token"].(string)
	if code, _, _ := request("GET", "/me", nil, access); code != 200 {
		t.Fatalf("real bearer /me %d", code)
	}
	if code, _, _ := request("GET", "/admin/users", nil, access); code != 401 {
		t.Fatal("user JWT authorized admin route")
	}
	if code, _, _ := request("POST", "/logout", map[string]any{"refresh_token": refresh}, ""); code != 204 {
		t.Fatal("public logout", code)
	}
	if code, _, _ := request("GET", "/me", nil, access); code != 401 {
		t.Fatal("logout did not revoke access", code)
	}
	if code, _, _ := request("POST", "/refresh", map[string]any{"client_id": cid, "refresh_token": refresh}, ""); code != 401 {
		t.Fatal("logout did not revoke refresh", code)
	}
	if code, _, _ := request("GET", "/orgs/default/password/reset", nil, ""); code != 200 {
		t.Fatal("public recovery landing unavailable", code)
	}
	if code, out, _ := request("GET", "/orgs/default/.well-known/openid-configuration", nil, ""); code != 200 || out["oauth_supported"] != false || out["authorization_endpoint"] != nil {
		t.Fatal("false discovery advertising", code, out)
	}
	// A regular platform tool request must not reach the trusted identity-login path.
	payload := map[string]any{"jsonrpc": "2.0", "id": 1, "method": "tools/call", "params": map[string]any{"name": "auth_public_login_identity", "arguments": map[string]any{"client_id": cid, "provider": "device", "provider_user_id": "forged"}}}
	_, out, _ = request("POST", "/mcp", payload, sc.Token())
	encoded, _ := json.Marshal(out)
	if !strings.Contains(string(encoded), "error") && !strings.Contains(string(encoded), "isError") {
		t.Fatal("unbound MCP identity caller accepted", out)
	}
}
