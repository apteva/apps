package main

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"

	sdk "github.com/apteva/app-sdk"
	"github.com/apteva/app-sdk/testkit"
)

func TestHTTPProjectAndCallerGates(t *testing.T) {
	a := testApp(t)
	a.ctx = testkit.NewAppCtx(t, "apteva.yaml", testkit.WithProjectID("p1"))
	a.providers = func(*sdk.AppCtx, string) ([]Provider, error) { return nil, nil }
	cases := []struct {
		name, path, body, header, value string
		status                          int
	}{
		{name: "read", path: "/rpc", body: `{"tool":"workspace_get","args":{}}`, status: 200},
		{name: "query scope", path: "/rpc?project_id=p2", body: `{"tool":"workspace_get"}`, status: 403},
		{name: "body scope", path: "/rpc", body: `{"tool":"workspace_get","args":{"project_id":"p2"}}`, status: 403},
		{name: "agent bypass", path: "/rpc", body: `{"tool":"demo_load"}`, header: "X-Apteva-Caller-Agent", value: "1", status: 403},
		{name: "unknown tool", path: "/rpc", body: `{"tool":"unknown"}`, status: 404},
		{name: "trailing payload", path: "/rpc", body: `{"tool":"workspace_get"}{}`, status: 400},
		{name: "forged user", path: "/rpc", body: `{"tool":"demo_load"}`, header: "X-Apteva-Trusted-Principal", value: "fake", status: 403},
		{name: "live blocked", path: "/rpc", body: `{"tool":"bet_submit"}`, status: 409},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest("POST", c.path, bytes.NewBufferString(c.body))
			r.Header.Set("Content-Type", "application/json")
			if c.header != "" {
				r.Header.Set(c.header, c.value)
			}
			w := httptest.NewRecorder()
			a.handleRPC(w, r)
			if w.Code != c.status {
				t.Fatalf("want %d got %d: %s", c.status, w.Code, w.Body.String())
			}
		})
	}
}
func TestSignedUserScopeAndSignature(t *testing.T) {
	t.Setenv("APTEVA_APP_TOKEN", "test-install-token")
	raw, _ := json.Marshal(sdk.TrustedPrincipal{Version: 1, UserID: 12, ProjectID: "p1", ExpiresAt: time.Now().Unix() + 60})
	encoded := base64.RawURLEncoding.EncodeToString(raw)
	mac := hmac.New(sha256.New, []byte("test-install-token"))
	mac.Write([]byte(encoded))
	r := httptest.NewRequest("POST", "/rpc", nil)
	r.Header.Set("X-Apteva-Trusted-Principal", encoded)
	r.Header.Set("X-Apteva-Trusted-Principal-Signature", hex.EncodeToString(mac.Sum(nil)))
	p, err := sdk.PrincipalFromRequest(r)
	if err != nil || p.UserID != 12 {
		t.Fatal(err)
	}
	r.Header.Set("X-Apteva-Trusted-Principal-Signature", "00")
	if _, err = sdk.PrincipalFromRequest(r); err == nil {
		t.Fatal("forged signature accepted")
	}
}
func TestSDKToolScopeDoesNotTrustModelArguments(t *testing.T) {
	a := testApp(t)
	app := testkit.NewAppCtx(t, "apteva.yaml", testkit.WithProjectID("p1"))
	call := sdk.WithCaller(context.Background(), &sdk.Caller{AgentID: 1, ProjectID: "p1", DefaultEffect: "allow"})
	for _, tool := range a.MCPTools() {
		if tool.Name == "workspace_get" {
			_, err := tool.HandlerCtx(call, app, map[string]any{"project_id": "p2"})
			code(t, err, "project_mismatch")
		}
	}
}
