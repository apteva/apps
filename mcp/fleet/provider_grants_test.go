package main

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	sdk "github.com/apteva/app-sdk"
	tk "github.com/apteva/app-sdk/testkit"
)

type providerGrantPlatform struct {
	tk.BasePlatformClient
	connectionID int64
	tool         string
	input        map[string]any
}

func (p *providerGrantPlatform) ExecuteIntegrationTool(connectionID int64, tool string, input map[string]any) (*sdk.ExecuteResult, error) {
	p.connectionID = connectionID
	p.tool = tool
	p.input = input
	return &sdk.ExecuteResult{Success: true, Status: http.StatusOK, Data: json.RawMessage(`{"message_id":"m-1"}`)}, nil
}

func TestProviderGrantExecuteUsesScopedTokenAndTrustedGrant(t *testing.T) {
	platform := &providerGrantPlatform{}
	app, _ := newTestApp(t, tk.WithPlatform(platform))
	tenantID := seedTenant(t, app, "acme", StatusActive)
	token, hash, err := newProviderGrantToken()
	if err != nil {
		t.Fatal(err)
	}
	_, err = app.store.upsertProviderGrant(ProviderGrant{
		TenantID: tenantID, GrantID: "grant-1", AppSlug: "aws-ses", ParentConnectionID: 77,
		Status: "active", AllowedTools: []string{"SendEmail"}, AllowedDomains: []string{"example.com"}, TokenHash: hash,
	})
	if err != nil {
		t.Fatalf("upsert grant: %v", err)
	}
	body := []byte(`{"tool":"SendEmail","input":{"FromEmailAddress":"hello@example.com"}}`)
	req := httptest.NewRequest(http.MethodPost, "/provider-grants/"+tenantID+"/grant-1/execute", bytes.NewReader(body))
	req.Header.Set("Authorization", "Bearer "+token)
	rec := httptest.NewRecorder()
	app.httpProviderGrantExecute(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
	if platform.connectionID != 77 || platform.tool != "SendEmail" {
		t.Fatalf("unexpected provider call: connection=%d tool=%q", platform.connectionID, platform.tool)
	}
	if _, exists := platform.input["parent_connection_id"]; exists {
		t.Fatal("tenant input was allowed to choose the parent connection")
	}
}

func TestProviderGrantExecuteRejectsBadTokenAndConstraints(t *testing.T) {
	platform := &providerGrantPlatform{}
	app, _ := newTestApp(t, tk.WithPlatform(platform))
	tenantID := seedTenant(t, app, "acme", StatusActive)
	token, hash, _ := newProviderGrantToken()
	_, err := app.store.upsertProviderGrant(ProviderGrant{
		TenantID: tenantID, GrantID: "grant-1", AppSlug: "aws-ses", ParentConnectionID: 77,
		Status: "active", AllowedTools: []string{"SendEmail"}, AllowedDomains: []string{"example.com"}, TokenHash: hash,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := func(auth, body string) int {
		req := httptest.NewRequest(http.MethodPost, "/provider-grants/"+tenantID+"/grant-1/execute", bytes.NewBufferString(body))
		req.Header.Set("Authorization", "Bearer "+auth)
		rec := httptest.NewRecorder()
		app.httpProviderGrantExecute(rec, req)
		return rec.Code
	}
	if got := request("wrong", `{"tool":"SendEmail","input":{}}`); got != http.StatusForbidden {
		t.Fatalf("bad token status=%d", got)
	}
	if got := request(token, `{"tool":"SendEmail","input":{"FromEmailAddress":"hello@outside.test"}}`); got != http.StatusForbidden {
		t.Fatalf("constraint status=%d", got)
	}
	if platform.connectionID != 0 {
		t.Fatal("denied request reached the parent provider")
	}
}
