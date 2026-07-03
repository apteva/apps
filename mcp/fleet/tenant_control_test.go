package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func seedTenantWithKey(t *testing.T, app *App, baseURL, apiKey string) string {
	t.Helper()
	enc, err := app.keys.seal([]byte(apiKey))
	if err != nil {
		t.Fatalf("seal api key: %v", err)
	}
	tenant := &Tenant{
		Slug:       "tenant-control",
		Kind:       KindRemote,
		BaseURL:    baseURL,
		OwnerEmail: "ops@example.com",
		Status:     StatusActive,
	}
	if err := app.store.insert(tenant, enc, nil); err != nil {
		t.Fatalf("insert tenant: %v", err)
	}
	return tenant.ID
}

func TestTenantAppToolsAndCall(t *testing.T) {
	app, _ := newTestApp(t)
	var sawList, sawCall bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		if r.URL.Path != "/api/apps/crm/mcp" {
			t.Fatalf("path = %q", r.URL.Path)
		}
		if r.URL.Query().Get("install_id") != "42" || r.URL.Query().Get("project_id") != "proj-1" {
			t.Fatalf("query = %s", r.URL.RawQuery)
		}
		var req struct {
			Method string         `json:"method"`
			Params map[string]any `json:"params"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		w.Header().Set("Content-Type", "application/json")
		switch req.Method {
		case "tools/list":
			sawList = true
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      1,
				"result": map[string]any{
					"tools": []map[string]any{{
						"name":        "contacts_create",
						"description": "Create a contact",
					}},
				},
			})
		case "tools/call":
			sawCall = true
			if req.Params["name"] != "contacts_create" {
				t.Fatalf("tool name = %#v", req.Params["name"])
			}
			args, _ := req.Params["arguments"].(map[string]any)
			if args["display_name"] != "Ada Lovelace" {
				t.Fatalf("arguments = %#v", args)
			}
			inner, _ := json.Marshal(map[string]any{
				"contact": map[string]any{"id": 99, "display_name": "Ada Lovelace"},
			})
			_ = json.NewEncoder(w).Encode(map[string]any{
				"jsonrpc": "2.0",
				"id":      1,
				"result": map[string]any{
					"content": []map[string]any{{"type": "text", "text": string(inner)}},
				},
			})
		default:
			t.Fatalf("method = %q", req.Method)
		}
	}))
	defer srv.Close()
	tenantID := seedTenantWithKey(t, app, srv.URL, "sk-test")

	tools, err := app.toolTenantAppTools(nil, map[string]any{
		"tenant_id":  tenantID,
		"app":        "crm",
		"install_id": 42,
		"project_id": "proj-1",
	})
	if err != nil {
		t.Fatalf("tenant_app_tools: %v", err)
	}
	toolRows := tools.(map[string]any)["tools"].([]any)
	if len(toolRows) != 1 || toolRows[0].(map[string]any)["name"] != "contacts_create" {
		t.Fatalf("unexpected tools: %#v", tools)
	}

	out, err := app.toolTenantAppCall(nil, map[string]any{
		"tenant_id":  tenantID,
		"app":        "crm",
		"tool":       "contacts_create",
		"install_id": 42,
		"project_id": "proj-1",
		"arguments": map[string]any{
			"display_name": "Ada Lovelace",
		},
	})
	if err != nil {
		t.Fatalf("tenant_app_call: %v", err)
	}
	contact := out.(map[string]any)["contact"].(map[string]any)
	if contact["display_name"] != "Ada Lovelace" {
		t.Fatalf("unexpected contact: %#v", out)
	}
	if !sawList || !sawCall {
		t.Fatalf("sawList=%v sawCall=%v", sawList, sawCall)
	}
}

func TestTenantPlatformCallAppsInstallAndConfirm(t *testing.T) {
	app, _ := newTestApp(t)
	var sawInstall, sawDelete bool
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/api/apps/install":
			sawInstall = true
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Fatalf("decode install body: %v", err)
			}
			if body["manifest_url"] != "https://example.com/crm/apteva.yaml" {
				t.Fatalf("install body = %#v", body)
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"install_id": 7, "status": "running"})
		case r.Method == http.MethodDelete && r.URL.Path == "/api/apps/installs/7":
			sawDelete = true
			_ = json.NewEncoder(w).Encode(map[string]any{"status": "deleted"})
		default:
			t.Fatalf("unexpected request %s %s", r.Method, r.URL.String())
		}
	}))
	defer srv.Close()
	tenantID := seedTenantWithKey(t, app, srv.URL, "sk-test")

	out, err := app.toolTenantPlatformCall(nil, map[string]any{
		"tenant_id": tenantID,
		"resource":  "apps",
		"action":    "install",
		"arguments": map[string]any{
			"manifest_url": "https://example.com/crm/apteva.yaml",
			"project_id":   "proj-1",
		},
	})
	if err != nil {
		t.Fatalf("tenant_platform_call install: %v", err)
	}
	result := out.(map[string]any)["result"].(map[string]any)
	if result["install_id"].(float64) != 7 {
		t.Fatalf("unexpected install result: %#v", out)
	}
	if !sawInstall {
		t.Fatal("install route was not called")
	}

	_, err = app.toolTenantPlatformCall(nil, map[string]any{
		"tenant_id": tenantID,
		"resource":  "apps",
		"action":    "uninstall",
		"arguments": map[string]any{"install_id": 7},
	})
	if err == nil || !strings.Contains(err.Error(), "confirm=true") {
		t.Fatalf("uninstall without confirm err = %v", err)
	}
	if sawDelete {
		t.Fatal("delete route called without confirm")
	}

	if _, err := app.toolTenantPlatformCall(nil, map[string]any{
		"tenant_id": tenantID,
		"resource":  "apps",
		"action":    "uninstall",
		"arguments": map[string]any{"install_id": 7, "confirm": true},
	}); err != nil {
		t.Fatalf("tenant_platform_call uninstall: %v", err)
	}
	if !sawDelete {
		t.Fatal("delete route was not called with confirm")
	}
}

func TestTenantInventoryBestEffort(t *testing.T) {
	app, _ := newTestApp(t)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer sk-test" {
			t.Fatalf("authorization = %q", r.Header.Get("Authorization"))
		}
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/projects":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": "proj-1", "name": "Default"}})
		case "/api/apps":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"name": "crm", "install_id": 7}})
		case "/api/instances":
			_ = json.NewEncoder(w).Encode([]map[string]any{{"id": 3, "name": "CRM Agent"}})
		case "/api/connections":
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		case "/api/mcp-servers":
			_ = json.NewEncoder(w).Encode([]map[string]any{})
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	tenantID := seedTenantWithKey(t, app, srv.URL, "sk-test")

	out, err := app.toolTenantInventory(nil, map[string]any{
		"tenant_id":  tenantID,
		"project_id": "proj-1",
	})
	if err != nil {
		t.Fatalf("tenant_inventory: %v", err)
	}
	remote := out.(map[string]any)["remote"].(map[string]any)
	if len(remote["projects"].([]any)) != 1 || len(remote["apps"].([]any)) != 1 {
		t.Fatalf("unexpected inventory: %#v", out)
	}
	if _, ok := out.(map[string]any)["errors"]; ok {
		t.Fatalf("unexpected inventory errors: %#v", out)
	}
}
