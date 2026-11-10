package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestConnectedUpdateSurvivesLostResponseAndFleetRestart(t *testing.T) {
	token := "fup_" + strings.Repeat("a", 64)
	const managementKey = "tenant-management-key"
	var mu sync.Mutex
	var job *connectedRemoteJob
	postCount, served := 0, "1.2.2"
	revoked := false
	tenant := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		defer mu.Unlock()
		switch r.URL.Path {
		case "/api/auth/me", "/api/projects":
			if r.Header.Get("Authorization") != "Bearer "+managementKey {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if r.URL.Path == "/api/auth/me" {
				_ = json.NewEncoder(w).Encode(map[string]any{"user_id": 42, "role": "admin"})
			} else {
				_ = json.NewEncoder(w).Encode([]map[string]string{{"id": "client-project"}})
			}
		case connectedUpdatePath:
			if r.Header.Get("Authorization") != "Bearer "+token || revoked {
				w.WriteHeader(http.StatusUnauthorized)
				return
			}
			if r.Method == http.MethodPost {
				postCount++
				var req map[string]string
				if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
					t.Error(err)
					w.WriteHeader(http.StatusBadRequest)
					return
				}
				job = &connectedRemoteJob{ID: "accepted", State: "succeeded", Target: req["version"], Previous: "1.2.2", Updated: time.Now()}
				job.Request.ID, job.Request.Policy = req["request_id"], req["agent_policy"]
				// The job was accepted before the reply became unavailable.
				w.WriteHeader(http.StatusServiceUnavailable)
			} else if r.URL.Query().Has("request_id") {
				_ = json.NewEncoder(w).Encode(connectedRemoteUpdate{Job: job})
			} else {
				_ = json.NewEncoder(w).Encode(connectedRemoteUpdate{Supported: true, Version: "1.2.3"})
			}
		case "/api/health":
			if auth := r.Header.Get("Authorization"); auth != "" && auth != "Bearer "+managementKey {
				t.Error("sent an unexpected credential to the health endpoint")
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "cli": served})
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer tenant.Close()
	oldClient := connectedHTTP
	oldManagementClient := httpClient
	client := *tenant.Client()
	client.CheckRedirect = oldClient.CheckRedirect
	connectedHTTP = &client
	httpClient = tenant.Client()
	t.Cleanup(func() { connectedHTTP, httpClient = oldClient, oldManagementClient })

	a, _ := newTestApp(t)
	connected, err := a.toolConnect(nil, map[string]any{
		"slug": "customer", "base_url": tenant.URL, "api_key": managementKey, "owner_email": "client@example.test",
	})
	if err != nil {
		t.Fatal(err)
	}
	id := connected.(map[string]any)["tenant_id"].(string)
	_, originalKey, err := a.store.get(id)
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, body string, operator bool) *httptest.ResponseRecorder {
		w := httptest.NewRecorder()
		req := httptest.NewRequest(method, "/tenants/"+id+"/connected-update", strings.NewReader(body))
		req.Header.Set("X-User-ID", "7")
		if operator {
			req.Header.Set("X-Apteva-Operator-ID", "7")
		}
		a.httpConnectedUpdate(w, req, id)
		return w
	}
	grant := fmt.Sprintf(`{"credential":%q}`, token)
	if w := call(http.MethodPut, grant, false); w.Code != http.StatusForbidden {
		t.Fatalf("untrusted operator got %d", w.Code)
	}
	if w := call(http.MethodPut, grant, true); w.Code != http.StatusNoContent {
		t.Fatalf("enroll: %d %s", w.Code, w.Body.String())
	}
	update := `{"version":"1.2.3","agent_policy":"preserve","confirm":true}`
	if w := call(http.MethodPost, update, true); w.Code != http.StatusAccepted {
		t.Fatalf("queue: %d %s", w.Code, w.Body.String())
	}
	if w := call(http.MethodPost, update, true); w.Code != http.StatusConflict {
		t.Fatalf("second queue: %d %s", w.Code, w.Body.String())
	}
	op, err := a.store.connectedUpdate(id)
	if err != nil || op == nil || len(op.RequestID) != 32 || strings.Contains(string(op.grant), token) {
		t.Fatal("expected a durable request and sealed credential")
	}
	if err := a.runConnectedUpdates(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	restarted := &App{store: a.store, keys: a.keys}
	if err := restarted.runConnectedUpdates(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	op, err = a.store.connectedUpdate(id)
	if err != nil || op == nil || op.Done || op.JobID != "accepted" {
		t.Fatalf("accepted job should be pending exact health verification: %+v", op)
	}
	mu.Lock()
	served = "1.2.3"
	mu.Unlock()
	if err := restarted.runConnectedUpdates(context.Background(), nil); err != nil {
		t.Fatal(err)
	}
	op, err = a.store.connectedUpdate(id)
	if err != nil || op == nil || !op.Done || op.Phase != "succeeded" {
		t.Fatalf("update was not verified: %+v", op)
	}
	saved, savedKey, err := a.store.get(id)
	if err != nil || saved.CurrentVersion != "1.2.3" || saved.InstanceID != 0 || saved.Kind != KindRemote || string(savedKey) != string(originalKey) {
		t.Fatal("wrong tenant version, placement, or management credential after update")
	}
	mu.Lock()
	n := postCount
	revoked = true
	mu.Unlock()
	if n != 1 {
		t.Fatalf("update submitted %d times", n)
	}
	if w := call(http.MethodGet, "", true); w.Code != http.StatusOK || !strings.Contains(w.Body.String(), "revoked") || strings.Contains(w.Body.String(), token) {
		t.Fatalf("revocation response: %d %s", w.Code, w.Body.String())
	}
	if w := call(http.MethodPost, update, true); w.Code != http.StatusBadGateway {
		t.Fatalf("revoked update accepted: %d", w.Code)
	}
	managed, err := a.toolTenantPlatformCall(nil, map[string]any{
		"tenant_id": id, "resource": "projects", "action": "list",
	})
	if err != nil || !strings.Contains(fmt.Sprint(managed), "client-project") {
		t.Fatalf("revoking update access interfered with management: %v %v", managed, err)
	}
}

func TestConnectedUpdateRefusesInsecureEndpoints(t *testing.T) {
	for _, base := range []string{"http://example.test", "https://user:pass@example.test", "https://example.test?key=abc", "https://example.test/#fragment"} {
		if _, err := connectedUpdateURL(&Tenant{BaseURL: base}); err == nil {
			t.Errorf("accepted insecure or ambiguous endpoint %q", base)
		}
	}
}
